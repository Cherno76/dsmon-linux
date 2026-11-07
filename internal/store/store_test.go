package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dsmon/internal/pricing"
)

var testLocation = time.FixedZone("CST", 8*3600)

var testNow = time.Date(2026, time.September, 16, 14, 30, 0, 0, testLocation)

func rules(miss, hit, out float64, model string) string {
	return fmt.Sprintf(`{
  "deepseek": {
    "peak_hours": { "weekday": [], "weekend": [] },
    "models": {
      "%s": { "offpeak": { "miss": %g, "hit": %g, "out": %g } }
    }
  }
}`, model, miss, hit, out)
}

func loadedRules(t *testing.T, text string) pricing.Loaded {
	t.Helper()
	strategy, err := pricing.Parse(text)
	if err != nil {
		t.Fatalf("parse test rules: %v", err)
	}
	return pricing.Loaded{
		Strategy:    strategy,
		Source:      pricing.SourceUserOverride,
		Fingerprint: pricing.Fingerprint(text),
		Text:        text,
	}
}

func openTestStore(t *testing.T, path, ruleText string) *Store {
	t.Helper()
	store, err := Open(path,
		WithPricing(loadedRules(t, ruleText)),
		WithClock(func() time.Time { return testNow }),
		WithLocation(testLocation),
		WithRetentionDays(0))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func recordAt(at time.Time, model string) UsageRecord {
	return UsageRecord{
		UUID:             fmt.Sprintf("uuid-%d-%s", at.UnixNano(), model),
		Timestamp:        at,
		ProviderID:       "deepseek",
		Model:            model,
		Endpoint:         "/v1/chat/completions",
		PromptTokens:     1_000,
		CompletionTokens: 100,
		TotalTokens:      1_100,
		CachedTokens:     400,
		LatencyMS:        120,
		StatusCode:       200,
		UserAgent:        "opencode/1.18.25 ai-sdk/4",
	}
}

// rateRecordAt 用 1M 未命中输入，让费用正好等于 miss 单价，便于直接断言金额。
func rateRecordAt(at time.Time, model string) UsageRecord {
	return UsageRecord{
		UUID:         fmt.Sprintf("uuid-rate-%d", at.UnixNano()),
		Timestamp:    at,
		ProviderID:   "deepseek",
		Model:        model,
		Endpoint:     "/v1/chat/completions",
		PromptTokens: 1_000_000,
		TotalTokens:  1_000_000,
		LatencyMS:    120,
		StatusCode:   200,
		UserAgent:    "opencode/1.18.25 ai-sdk/4",
	}
}

func TestEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))

	daily, err := store.QueryWindow(ctx, 1, "", 0)
	if err != nil || daily.RequestCount != 0 {
		t.Fatalf("window = %+v, err = %v", daily, err)
	}
	hourly, err := store.QueryHourlyBreakdown(ctx, "")
	if err != nil || len(hourly) != 24 {
		t.Fatalf("hourly len = %d, err = %v", len(hourly), err)
	}
	for _, bar := range hourly {
		if bar.RequestCount != 0 {
			t.Fatalf("empty hourly bucket = %+v", bar)
		}
	}
	snapshot, err := store.MenuBarSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestCacheHitRate != nil || snapshot.TodayCacheHitRate != nil {
		t.Fatalf("empty snapshot rates = %+v", snapshot)
	}
	if snapshot.TodayCost != (CostSummary{}) || snapshot.TodayTokens != 0 || snapshot.TodayRequests != 0 {
		t.Fatalf("empty snapshot = %+v", snapshot)
	}
	if snapshot.PreviousWindow == nil || snapshot.PreviousWindow.Requests != 0 ||
		snapshot.PreviousWindow.Tokens != 0 || snapshot.PreviousWindow.HitRate != nil {
		t.Fatalf("empty previous window = %+v", snapshot.PreviousWindow)
	}
	recent, err := store.RecentRecords(ctx, 5)
	if err != nil || len(recent) != 0 {
		t.Fatalf("recent = %v, err = %v", recent, err)
	}
}

func TestInsertReadBackAndDedup(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	record := recordAt(testNow.Add(-time.Hour), "deepseek-flash")
	if err := store.Insert(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, record); err != nil {
		t.Fatal(err)
	}
	recent, err := store.RecentRecords(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatalf("dedup should keep one row, got %d", len(recent))
	}
	row := recent[0]
	if row.ID != record.UUID || row.Model != record.Model || row.StatusCode != 200 || row.LatencyMS != 120 {
		t.Fatalf("recent row = %+v", row)
	}
}

func TestHitRatesUseLatestAndToday(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	first := recordAt(time.Date(2026, 9, 16, 1, 0, 0, 0, testLocation), "deepseek-flash")
	first.CachedTokens = 250
	second := recordAt(time.Date(2026, 9, 16, 2, 0, 0, 0, testLocation), "deepseek-flash")
	second.CachedTokens = 500
	for _, record := range []UsageRecord{first, second} {
		if err := store.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.MenuBarSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertFloat(t, "latest rate", *snapshot.LatestCacheHitRate, 0.5)
	assertFloat(t, "today rate", *snapshot.TodayCacheHitRate, 0.375)
}

func TestAggregatesBreakdownsAndWindows(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	three := recordAt(time.Date(2026, 9, 16, 3, 20, 0, 0, testLocation), "deepseek-flash")
	fifteen := recordAt(time.Date(2026, 9, 16, 15, 20, 0, 0, testLocation), "deepseek-flash")
	fifteen.PromptTokens, fifteen.CachedTokens, fifteen.CompletionTokens = 500, 100, 50
	fifteen.TotalTokens = 550
	for _, record := range []UsageRecord{three, fifteen} {
		if err := store.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := store.QueryWindow(ctx, 1, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if usage.RequestCount != 2 || usage.PromptTokens != 1500 || usage.CompletionTokens != 150 || usage.CachedTokens != 500 {
		t.Fatalf("aggregate = %+v", usage)
	}
	assertFloat(t, "cache hit rate", usage.CacheHitRate(), 500.0/1500.0*100)

	hourly, err := store.QueryHourlyBreakdown(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if hourly[3].MissTokens != 600 || hourly[3].HitTokens != 400 || hourly[3].OutTokens != 100 || hourly[3].RequestCount != 1 {
		t.Fatalf("03:00 bucket = %+v", hourly[3])
	}
	if hourly[15].MissTokens != 400 || hourly[15].RequestCount != 1 {
		t.Fatalf("15:00 bucket = %+v", hourly[15])
	}
	if hourly[9].RequestCount != 0 {
		t.Fatalf("09:00 should be empty: %+v", hourly[9])
	}

	week, err := store.QueryDailyBreakdown(ctx, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(week) != 7 || week[len(week)-1].RequestCount != 2 || week[0].RequestCount != 0 {
		t.Fatalf("daily buckets = %+v", week)
	}
	month, err := store.QueryDailyBreakdown(ctx, 30, "")
	if err != nil || len(month) != 30 {
		t.Fatalf("month buckets len = %d, err = %v", len(month), err)
	}
}

func TestWindowIncludesTheRightDays(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	today := time.Date(2026, 9, 16, 5, 0, 0, 0, testLocation)
	records := []UsageRecord{
		recordAt(today, "deepseek-flash"),
		recordAt(today.AddDate(0, 0, -6), "deepseek-flash"),
		recordAt(today.AddDate(0, 0, -7), "deepseek-flash"),
	}
	for _, record := range records {
		if err := store.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	checks := []struct {
		days int
		want int
	}{{1, 1}, {7, 2}, {30, 3}}
	for _, check := range checks {
		usage, err := store.QueryWindow(ctx, check.days, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if usage.RequestCount != check.want {
			t.Fatalf("days=%d requests=%d want=%d", check.days, usage.RequestCount, check.want)
		}
	}
	bars, err := store.QueryDailyBreakdown(ctx, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if bars[0].RequestCount != 1 || bars[len(bars)-1].RequestCount != 1 {
		t.Fatalf("7-day buckets = %+v", bars)
	}
}

func TestWindowTotalsUseOneSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	today := time.Date(2026, 9, 16, 5, 0, 0, 0, testLocation)
	for _, at := range []time.Time{today, today.AddDate(0, 0, -3), today.AddDate(0, 0, -20)} {
		record := recordAt(at, "deepseek-flash")
		if err := store.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	totals, err := store.WindowTotals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Today != 1_100 || totals.Week != 2_200 || totals.Month != 3_300 {
		t.Fatalf("window totals = %+v", totals)
	}
}

func TestUnpricedAndFixedCost(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	unknown := recordAt(testNow.Add(-time.Hour), "gpt-5.4-mini")
	if err := store.Insert(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	usage, err := store.QueryWindow(ctx, 1, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if usage.UnpricedRequests != 1 || usage.EstimatedCost != 0 {
		t.Fatalf("unknown model aggregate = %+v", usage)
	}

	tool := rateRecordAt(testNow.Add(-30*time.Minute), "deepseek-flash")
	tool.PromptTokens, tool.TotalTokens = 0, 0
	tool.Endpoint = "/v1/tools/search"
	tool.FixedCost = 0.005
	if err := store.Insert(ctx, tool); err != nil {
		t.Fatal(err)
	}
	usage, err = store.QueryWindow(ctx, 1, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertFloat(t, "fixed cost", usage.EstimatedCost, 0.005)

	pricey := rules(10, 1, 20, "deepseek-flash")
	repriced, imported, err := store.ImportPricingRevision(ctx, pricey, pricing.SourceUserOverride,
		testNow.Add(-time.Hour), "")
	if err != nil || !imported || repriced != 1 {
		t.Fatalf("import repriced=%d imported=%v err=%v", repriced, imported, err)
	}
	usage, err = store.QueryWindow(ctx, 1, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	assertFloat(t, "fixed cost survives reprice", usage.EstimatedCost, 0.005)
}

func TestSnapshotCarriesPreviousWindow(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	yesterday := time.Date(2026, 9, 15, 13, 0, 0, 0, testLocation)
	past := recordAt(yesterday, "deepseek-flash")
	past.PromptTokens, past.CompletionTokens, past.TotalTokens, past.CachedTokens = 2_000, 200, 2_200, 800
	today := recordAt(time.Date(2026, 9, 16, 10, 0, 0, 0, testLocation), "deepseek-flash")
	for _, record := range []UsageRecord{past, today} {
		if err := store.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.MenuBarSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PreviousWindow == nil {
		t.Fatal("missing previous window")
	}
	if snapshot.PreviousWindow.Requests != 1 || snapshot.PreviousWindow.Tokens != 2_200 {
		t.Fatalf("previous = %+v", snapshot.PreviousWindow)
	}
	assertFloat(t, "previous hit rate", *snapshot.PreviousWindow.HitRate, 0.4)
}

func TestClientBreakdownAndFilters(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	open := recordAt(testNow.Add(-3*time.Hour), "deepseek-flash")
	codex := recordAt(testNow.Add(-2*time.Hour), "deepseek-flash")
	codex.UserAgent = "Codex Desktop/0.154.0-alpha.6.2 (Mac OS 27.0; arm64)"
	unknown := recordAt(testNow.Add(-time.Hour), "deepseek-flash")
	unknown.UserAgent = ""
	other := recordAt(testNow.Add(-30*time.Minute), "gpt-5.4-mini")
	other.ProviderID = "other"
	for _, record := range []UsageRecord{open, codex, unknown, other} {
		if err := store.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	clients, err := store.QueryClientBreakdown(ctx, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 3 {
		t.Fatalf("clients = %+v", clients)
	}
	found := map[string]bool{}
	for _, client := range clients {
		found[client.Client] = true
	}
	for _, want := range []string{"opencode", "Codex Desktop", ClientUnknown} {
		if !found[want] {
			t.Fatalf("missing client %q in %+v", want, clients)
		}
	}
	filtered, err := store.QueryWindow(ctx, 1, "other", 0)
	if err != nil || filtered.RequestCount != 1 || filtered.UnpricedRequests != 1 {
		t.Fatalf("provider filter = %+v, err = %v", filtered, err)
	}
	available, err := store.AvailableClients(ctx, testNow.Add(-24*time.Hour), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) != 3 {
		t.Fatalf("available clients = %v", available)
	}
}

func TestTotalsSeriesAndLifetime(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	first := recordAt(time.Date(2026, 9, 16, 13, 0, 0, 0, testLocation), "deepseek-flash")
	second := recordAt(time.Date(2026, 9, 16, 13, 30, 0, 0, testLocation), "deepseek-flash")
	second.UserAgent = "Codex Desktop/1.0"
	for _, record := range []UsageRecord{first, second} {
		if err := store.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	from := time.Date(2026, 9, 16, 13, 0, 0, 0, testLocation)
	to := time.Date(2026, 9, 16, 13, 30, 0, 0, testLocation)
	usage, err := store.QueryTotals(ctx, from, to, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if usage.RequestCount != 1 {
		t.Fatalf("half-open totals = %+v", usage)
	}
	lifetime, err := store.LifetimeCost(ctx)
	if err != nil || lifetime <= 0 {
		t.Fatalf("lifetime = %v, err = %v", lifetime, err)
	}
	points, err := store.QuerySeriesPoints(ctx, startOfDay(testNow, testLocation),
		startOfDay(testNow, testLocation).AddDate(0, 0, 1), BucketHour, DimensionModel, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].Requests != 2 || points[0].HitTokens != 800 || points[0].MissTokens != 1200 {
		t.Fatalf("model series = %+v", points)
	}
	byClient, err := store.QuerySeriesPoints(ctx, startOfDay(testNow, testLocation),
		startOfDay(testNow, testLocation).AddDate(0, 0, 1), BucketHour, DimensionClient, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(byClient) != 2 {
		t.Fatalf("client series = %+v", byClient)
	}
}

func TestMetaAndBalanceSamples(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	if err := store.SetMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	value, ok, err := store.MetaValue(ctx, "k")
	if err != nil || !ok || value != "v" {
		t.Fatalf("meta = %q %v %v", value, ok, err)
	}
	if err := store.DeleteMeta(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.MetaValue(ctx, "k"); err != nil || ok {
		t.Fatalf("deleted meta ok=%v err=%v", ok, err)
	}
	samples := []BalanceSample{
		{Timestamp: time.Unix(1000, 0), Balance: 12.34, Granted: 5, ToppedUp: 7.34},
		{Timestamp: time.Unix(2000, 0), Balance: 11.84, Granted: 5, ToppedUp: 6.84},
	}
	for _, sample := range samples {
		if err := store.RecordBalanceSample(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.BalanceSamples(ctx, time.Unix(0, 0))
	if err != nil || len(got) != 2 {
		t.Fatalf("samples = %+v err=%v", got, err)
	}
	if got[1].Balance != 11.84 {
		t.Fatalf("sample order = %+v", got)
	}
}

func TestPruneOldRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	store, err := Open(path,
		WithPricing(loadedRules(t, rules(1, 0.1, 2, "deepseek-flash"))),
		WithClock(func() time.Time { return testNow }),
		WithLocation(testLocation),
		WithRetentionDays(180))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := recordAt(testNow.AddDate(0, 0, -181), "deepseek-flash")
	if err := store.Insert(ctx, old); err != nil {
		t.Fatal(err)
	}
	recent, err := store.RecentRecords(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 0 {
		t.Fatalf("old record should be pruned: %+v", recent)
	}
}

func TestSetRetentionDaysPrunesImmediately(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	store, err := Open(path,
		WithPricing(loadedRules(t, rules(1, 0.1, 2, "deepseek-flash"))),
		WithClock(func() time.Time { return testNow }),
		WithLocation(testLocation),
		WithRetentionDays(0))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Insert(ctx, recordAt(testNow.AddDate(0, 0, -181), "deepseek-flash")); err != nil {
		t.Fatal(err)
	}
	if err := store.Insert(ctx, recordAt(testNow.AddDate(0, 0, -10), "deepseek-flash")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRetentionDays(ctx, 180); err != nil {
		t.Fatal(err)
	}
	rows, err := store.RecentRecords(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Model != "deepseek-flash" {
		t.Fatalf("retention switch kept the wrong rows: %+v", rows)
	}
	if err := store.SetRetentionDays(ctx, -1); err == nil {
		t.Fatal("negative retention was accepted")
	}
}

func TestHistoryKeepsCostWhenRulesChange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	cheap := rules(1, 0.1, 2, "deepseek-flash")
	pricey := rules(10, 1, 20, "deepseek-flash")
	first := openTestStore(t, path, cheap)
	old := rateRecordAt(testNow.Add(-time.Hour), "deepseek-flash")
	if err := first.Insert(ctx, old); err != nil {
		t.Fatal(err)
	}
	beforeCost, beforeRevision := storedCost(t, first, old.UUID)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path, WithPricing(loadedRules(t, pricey)),
		WithClock(func() time.Time { return testNow }), WithLocation(testLocation), WithRetentionDays(0))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	afterCost, afterRevision := storedCost(t, second, old.UUID)
	assertFloat(t, "history cost", afterCost, beforeCost)
	if afterRevision != beforeRevision {
		t.Fatalf("history revision changed: %d -> %d", beforeRevision, afterRevision)
	}
	fresh := rateRecordAt(testNow, "deepseek-flash")
	if err := second.Insert(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	freshCost, freshRevision := storedCost(t, second, fresh.UUID)
	assertFloat(t, "fresh cost", freshCost, 10)
	if freshRevision == beforeRevision {
		t.Fatal("fresh row should belong to the new revision")
	}
}

func TestImportWithPastValidFromRepricesOnlyThatWindow(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	old := rateRecordAt(testNow.Add(-2*time.Hour), "deepseek-flash")
	newer := rateRecordAt(testNow.Add(-time.Hour), "deepseek-flash")
	for _, record := range []UsageRecord{old, newer} {
		if err := store.Insert(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	repriced, imported, err := store.ImportPricingRevision(ctx,
		rules(10, 1, 20, "deepseek-flash"), pricing.SourceUserOverride, testNow.Add(-90*time.Minute), "")
	if err != nil || !imported || repriced != 1 {
		t.Fatalf("repriced=%d imported=%v err=%v", repriced, imported, err)
	}
	oldCost, oldRevision := storedCost(t, store, old.UUID)
	newCost, newRevision := storedCost(t, store, newer.UUID)
	assertFloat(t, "old cost", oldCost, 1)
	assertFloat(t, "new cost", newCost, 10)
	if oldRevision == newRevision {
		t.Fatal("rows should belong to different revisions")
	}
}

func TestImportingSameRulesTwiceIsNoop(t *testing.T) {
	ctx := context.Background()
	text := rules(1, 0.1, 2, "deepseek-flash")
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), text)
	repriced, imported, err := store.ImportPricingRevision(ctx, text, pricing.SourceUserOverride, testNow, "")
	if err != nil || imported || repriced != 0 {
		t.Fatalf("same rules: repriced=%d imported=%v err=%v", repriced, imported, err)
	}
	count, _, _, err := store.PricingSummary(ctx)
	if err != nil || count != 1 {
		t.Fatalf("revisions=%d err=%v", count, err)
	}
}

func TestFallbackRulesDoNotBecomeRevision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	cheap := rules(1, 0.1, 2, "deepseek-flash")
	first := openTestStore(t, path, cheap)
	record := rateRecordAt(testNow.Add(-time.Hour), "deepseek-flash")
	if err := first.Insert(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	fallback := loadedRules(t, rules(99, 9, 99, "deepseek-flash"))
	fallback.IsFallback = true
	second, err := Open(path, WithPricing(fallback), WithClock(func() time.Time { return testNow }),
		WithLocation(testLocation), WithRetentionDays(0))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	count, _, _, err := second.PricingSummary(ctx)
	if err != nil || count != 1 {
		t.Fatalf("fallback added revision: count=%d err=%v", count, err)
	}
	cost, _ := storedCost(t, second, record.UUID)
	assertFloat(t, "fallback history cost", cost, 1)
}

func TestMaintenanceBackfillsOnlyNewlyPricedRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	first := openTestStore(t, path, rules(1, 0.1, 2, "other-model"))
	record := rateRecordAt(testNow.Add(-time.Minute), "deepseek-flash")
	if err := first.Insert(ctx, record); err != nil {
		t.Fatal(err)
	}
	cost, _ := storedCost(t, first, record.UUID)
	assertFloat(t, "initial unknown cost", cost, 0)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path, WithPricing(loadedRules(t, rules(3, 0.3, 6, "deepseek-flash"))),
		WithClock(func() time.Time { return testNow }), WithLocation(testLocation), WithRetentionDays(0))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.PerformStartupMaintenance(ctx); err != nil {
		t.Fatal(err)
	}
	cost, _ = storedCost(t, second, record.UUID)
	assertFloat(t, "backfilled cost", cost, 3)
}

func TestLegacyDatabaseIsMigratedWithoutRepricing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "usage.db")
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `
		CREATE TABLE usage_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp REAL NOT NULL,
			model TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			prompt_tokens INTEGER DEFAULT 0,
			completion_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			cached_tokens INTEGER DEFAULT 0,
			reasoning_tokens INTEGER DEFAULT 0,
			latency_ms REAL DEFAULT 0,
			status_code INTEGER DEFAULT 200,
			cost REAL DEFAULT 0
		);
		INSERT INTO usage_log (timestamp, model, endpoint, prompt_tokens, completion_tokens,
			total_tokens, cached_tokens, reasoning_tokens, latency_ms, status_code, cost)
		VALUES (?, 'deepseek-flash', '/v1/chat/completions', 1000000, 0, 1000000, 0, 0, 120, 200, 7.5)
	`, unixSeconds(testNow.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t, path, rules(1, 0.1, 2, "deepseek-flash"))
	var cost float64
	var revision int64
	if err := store.db.QueryRowContext(ctx,
		"SELECT cost, pricing_revision FROM usage_log ORDER BY id LIMIT 1").Scan(&cost, &revision); err != nil {
		t.Fatal(err)
	}
	assertFloat(t, "legacy cost", cost, 7.5)
	if revision <= 0 {
		t.Fatalf("legacy revision = %d", revision)
	}
}

func TestConcurrentInsertsGoThroughOneWriter(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "usage.db"), rules(1, 0.1, 2, "deepseek-flash"))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			record := recordAt(testNow.Add(-time.Duration(index)*time.Minute), "deepseek-flash")
			record.PromptTokens += index
			if err := store.Insert(ctx, record); err != nil {
				t.Errorf("insert %d: %v", index, err)
			}
		}(i)
	}
	wg.Wait()
	usage, err := store.QueryWindow(ctx, 1, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if usage.RequestCount != 16 {
		t.Fatalf("request count = %d, want 16", usage.RequestCount)
	}
}

func TestClientIdentity(t *testing.T) {
	tests := []struct {
		ua   string
		want string
	}{
		{"opencode/1.17.18 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14", "opencode"},
		{"Codex Desktop/0.154.0-alpha.6.2 (Mac OS 27.0; arm64)", "Codex Desktop"},
		{"Mozilla/5.0 (compatible; codewhale/0.8.47; +https://github.com/Hmbown/CodeWhale)", "codewhale"},
		{"Mozilla/5.0 (compatible; SomeTool/2.1; +https://example.com)", "sometool"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15", ClientBrowser},
		{"curl/8.7.1", "curl"},
		{"Go-http-client/1.1", "go-http-client"},
		{"node", "node"},
		{"SomeTool-2/1.0", "sometool"},
		{"mytool_3/1.0", "mytool"},
		{"Some.Tool.4/1.0", "some.tool"},
		{"node.js", "node.js"},
		{"", ClientUnknown},
		{"   ", ClientUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.ua, func(t *testing.T) {
			if got := ClientFromUserAgent(tt.ua); got != tt.want {
				t.Fatalf("ClientFromUserAgent(%q) = %q, want %q", tt.ua, got, tt.want)
			}
		})
	}
}

func storedCost(t *testing.T, store *Store, uuid string) (float64, int64) {
	t.Helper()
	var cost float64
	var revision int64
	if err := store.db.QueryRow(`
		SELECT cost, pricing_revision FROM usage_log WHERE uuid = ? LIMIT 1`, uuid).Scan(&cost, &revision); err != nil {
		t.Fatalf("read stored cost: %v", err)
	}
	return cost, revision
}

func assertFloat(t *testing.T, name string, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("%s = %.12f, want %.12f", name, got, want)
	}
}
