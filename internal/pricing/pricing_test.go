package pricing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testLocation = time.FixedZone("CST", 8*3600)

func testDate(day, hour int) time.Time {
	return time.Date(2026, time.September, day, hour, 0, 0, 0, testLocation)
}

const validRules = `{
  "deepseek": {
    "peak_hours": { "weekday": [[9, 12]], "weekend": [] },
    "models": {
      "fixture-model": { "offpeak": { "miss": 1, "hit": 0.5, "out": 2 } }
    }
  }
}`

func TestBeijingTime(t *testing.T) {
	tests := []struct {
		name    string
		at      time.Time
		hour    int
		weekend bool
	}{
		{"sunday noon", testDate(13, 12), 12, true},
		{"monday noon", testDate(14, 12), 12, false},
		{"midnight", testDate(14, 0), 0, false},
		{"late night", testDate(14, 23), 23, false},
		{"unix sample", time.Unix(1_789_310_153, 0), 22, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BeijingHour(tt.at); got != tt.hour {
				t.Fatalf("BeijingHour() = %d, want %d", got, tt.hour)
			}
			if got := IsBeijingWeekend(tt.at); got != tt.weekend {
				t.Fatalf("IsBeijingWeekend() = %v, want %v", got, tt.weekend)
			}
		})
	}
}

func TestRateSelection(t *testing.T) {
	offpeak := PriceRate{Miss: 1, Hit: 0.5, Out: 2}
	peak := PriceRate{Miss: 2, Hit: 1, Out: 4}
	model := ModelPricing{Weekday: DayPricing{Offpeak: offpeak, Peak: &peak}}
	catalog := Catalog{
		Table:            map[string]ModelPricing{"m": model},
		WeekdayPeakHours: []HourRange{{Start: 9, End: 12}},
	}
	tests := []struct {
		name string
		at   time.Time
		want PriceRate
	}{
		{"inside weekday peak", testDate(14, 10), peak},
		{"outside weekday peak", testDate(14, 13), offpeak},
		{"weekend has no peak window", testDate(13, 10), offpeak},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := catalog.RateFor("m", tt.at)
			if !ok {
				t.Fatal("RateFor() did not find model")
			}
			if got != tt.want {
				t.Fatalf("RateFor() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestComputeCost(t *testing.T) {
	rate := PriceRate{Miss: 1, Hit: 0.5, Out: 2}
	tests := []struct {
		name       string
		prompt     int
		completion int
		cached     int
		want       float64
	}{
		{"split cached input", 1_000_000, 200_000, 400_000, 1.2},
		{"cached above prompt never negative", 100, 0, 500, 0.00025},
		{"zero", 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeCost(tt.prompt, tt.completion, tt.cached, rate)
			if diff := got - tt.want; diff > 1e-12 || diff < -1e-12 {
				t.Fatalf("ComputeCost() = %.12f, want %.12f", got, tt.want)
			}
		})
	}
}

func TestLongestPrefixWins(t *testing.T) {
	flash := ModelPricing{Weekday: DayPricing{Offpeak: PriceRate{Miss: 1}}}
	vision := ModelPricing{Weekday: DayPricing{Offpeak: PriceRate{Miss: 9}}}
	catalog := Catalog{Table: map[string]ModelPricing{
		"deepseek-v4-flash":            flash,
		"deepseek-v4-flash-vision-exp": vision,
	}}
	tests := []struct {
		model string
		want  ModelPricing
		ok    bool
	}{
		{"deepseek-v4-flash", flash, true},
		{"deepseek-v4-flash-vision-exp", vision, true},
		{"deepseek-v4-flash-2026-01", flash, true},
		{"gpt-5.4-mini", ModelPricing{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, ok := catalog.PricingFor(tt.model)
			if ok != tt.ok {
				t.Fatalf("PricingFor() ok = %v, want %v", ok, tt.ok)
			}
			if ok && got.Weekday.Offpeak != tt.want.Weekday.Offpeak {
				t.Fatalf("PricingFor() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadUsesOverrideThenBundled(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "pricing_strategy.json")
	if err := os.WriteFile(override, []byte(validRules), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	loaded := Load(override, []byte(validRules), func(message string) { warnings = append(warnings, message) })
	if loaded.Source != SourceUserOverride || loaded.IsFallback {
		t.Fatalf("source = %s fallback = %v", loaded.Source, loaded.IsFallback)
	}
	if loaded.Text != validRules || loaded.Fingerprint != Fingerprint(validRules) {
		t.Fatal("override text/fingerprint not preserved")
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	loaded = Load(filepath.Join(dir, "missing.json"), []byte(validRules), func(message string) {
		warnings = append(warnings, message)
	})
	if loaded.Source != SourceBundled || loaded.IsFallback {
		t.Fatalf("missing override: source = %s fallback = %v", loaded.Source, loaded.IsFallback)
	}
}

func TestBrokenOverrideIsFallback(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "pricing_strategy.json")
	if err := os.WriteFile(override, []byte(`{"deepseek": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	loaded := Load(override, []byte(validRules), func(message string) { warnings = append(warnings, message) })
	if !loaded.IsFallback || loaded.Source != SourceBundled {
		t.Fatalf("broken override should fall back to bundled: %+v", loaded)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "解析失败") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestParseRejectsBrokenRules(t *testing.T) {
	tests := []string{"", "not json", "{{{", `{"deepseek": {}}`, `{"deepseek":{"models":{"m":{}}}}`}
	for _, text := range tests {
		t.Run(strings.ReplaceAll(text, " ", "_"), func(t *testing.T) {
			if _, err := Parse(text); err == nil {
				t.Fatalf("Parse(%q) accepted broken rules", text)
			}
		})
	}
}

func TestNoUsableRulesAtAll(t *testing.T) {
	var warnings []string
	loaded := Load("", nil, func(message string) { warnings = append(warnings, message) })
	if loaded.Source != SourceNone || !loaded.IsFallback || loaded.Fingerprint != "none" {
		t.Fatalf("loaded = %+v", loaded)
	}
	if len(loaded.Strategy.DeepSeek.Models) != 0 || len(warnings) != 1 {
		t.Fatalf("empty fallback: models=%v warnings=%v", loaded.Strategy.DeepSeek.Models, warnings)
	}
}

func TestTimelineEffectiveAndCost(t *testing.T) {
	cheapText := `{"deepseek":{"peak_hours":{"weekday":[],"weekend":[]},"models":{"deepseek-flash":{"offpeak":{"miss":1,"hit":0.1,"out":2}}}}}`
	priceyText := `{"deepseek":{"peak_hours":{"weekday":[],"weekend":[]},"models":{"deepseek-flash":{"offpeak":{"miss":10,"hit":1,"out":20}}}}}`
	t0 := testDate(14, 1)
	t1 := testDate(14, 2)
	timeline := NewTimeline([]Revision{
		NewRevision(1, t0, SourceBundled, Fingerprint(cheapText), cheapText),
		NewRevision(2, t1, SourceUserOverride, Fingerprint(priceyText), priceyText),
	})
	tests := []struct {
		name string
		at   time.Time
		id   int64
		cost float64
	}{
		{"before first revision uses earliest", t0.Add(-time.Hour), 1, 1},
		{"first revision is inclusive", t0, 1, 1},
		{"just before second", t1.Add(-time.Second), 1, 1},
		{"second revision", t1, 2, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeline.RevisionID(tt.at); got != tt.id {
				t.Fatalf("RevisionID() = %d, want %d", got, tt.id)
			}
			if got := timeline.Cost("deepseek-flash", 1_000_000, 0, 0, tt.at); got != tt.cost {
				t.Fatalf("Cost() = %v, want %v", got, tt.cost)
			}
		})
	}
}
