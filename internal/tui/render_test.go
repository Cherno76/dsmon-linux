package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dsmon/internal/ipc"
	"dsmon/internal/proxy/trace"
	"dsmon/internal/store"
	"dsmon/internal/version"
)

func TestPeriodSelectorShowsSixExplicitRanges(t *testing.T) {
	segments := periodSelectorSegments(store.PeriodThisMonth, "#abcdef")
	got := segmentsText(segments)
	for _, want := range []string{"¹Today", "²Yest.", "³7d", "⁴30d", "[⁵Month]", "⁶Prev. m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("period selector missing %q: %q", want, got)
		}
	}
	for _, unwanted := range []string{"[7", "⁷", "Custom", "1–7", "1-7", "switch"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("period selector still contains %q: %q", unwanted, got)
		}
	}
	var shortcuts string
	for _, segment := range segments {
		if segment.color == "#abcdef" {
			shortcuts += segment.text
		}
	}
	if shortcuts != "¹²³⁴⁵⁶" {
		t.Fatalf("period shortcuts = %q", shortcuts)
	}
}

func TestDimensionSelectorUsesBracketAndAccentKeys(t *testing.T) {
	segments := dimensionSelectorSegments(store.DimensionModel, "#abcdef")
	if got := segmentsText(segments); got != "by [model] client" {
		t.Fatalf("dimension selector = %q", got)
	}
	var shortcuts string
	for _, segment := range segments {
		if segment.color == "#abcdef" {
			shortcuts += segment.text
		}
	}
	if shortcuts != "mc" {
		t.Fatalf("dimension shortcuts = %q", shortcuts)
	}
}

func TestPeriodKeysStopAtSix(t *testing.T) {
	model := sampleModel()
	model.period = store.PeriodToday
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("7")})
	if model.period != store.PeriodToday {
		t.Fatalf("key 7 changed period to %q", model.period)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("6")})
	if model.period != store.PeriodLastMonth {
		t.Fatalf("key 6 period = %q, want %q", model.period, store.PeriodLastMonth)
	}
}

func sampleModel() *Model {
	now := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.Local)
	midnight := time.Date(2026, time.November, 7, 0, 0, 0, 0, time.Local)
	model := NewModel(nil, false)
	model.color = false
	model.theme = fallbackPalette()
	model.stats = ipc.Stats{
		ProxyRunning: true,
		ListenAddr:   "127.0.0.1:18080",
		Health:       true,
		Balance: ipc.Balance{
			Has: true, Total: 22.22, Granted: 0, ToppedUp: 22.22,
			LastSuccessUnix: now.Unix(),
			Trend24h:        []ipc.BalancePoint{{Unix: now.Add(-2 * time.Hour).Unix(), Value: 23}, {Unix: now.Unix(), Value: 22.22}},
			Trend7d:         []ipc.BalancePoint{{Unix: now.Add(-48 * time.Hour).Unix(), Value: 25}, {Unix: now.Unix(), Value: 22.22}},
		},
		Today: ipc.Aggregate{
			Requests: 3, TotalTokens: 1234, CacheHitRate: 41.5, Cost: 0.1234,
		},
		Recent: []ipc.RequestRow{
			{TimestampUnix: now.Unix(), Model: "deepseek-v4-flash", Client: "opencode", TotalTokens: 900, StatusCode: 200, Cost: 0.1},
			{TimestampUnix: now.Add(-time.Minute).Unix(), Model: "deepseek-chat", Client: "Codex Desktop", TotalTokens: 334, StatusCode: 200, Cost: 0.0234},
		},
	}
	activity := make([]int, trace.DefaultActivityBucketCount)
	activity[len(activity)-2], activity[len(activity)-1] = 1, 2
	traffic := make([]ipc.TrafficBucket, trace.DefaultTrafficBucketCount)
	traffic[len(traffic)-1] = ipc.TrafficBucket{Upload: 512, Download: 4096}
	model.trace = ipc.Trace{
		Activity: activity, ActivityBucketSecs: int(trace.DefaultActivityBucketSeconds / time.Second),
		ActivityFullScale: 3,
		Traffic:           traffic, TrafficBucketSecs: trace.DefaultTrafficBucketSeconds.Seconds(), Shift: 2,
		Grid: []float64{10, 30}, DownRate: 4096, UpRate: 512,
		DownScale: 8192, UpScale: 2048, NowUnixNano: now.UnixNano(),
		Health: true,
	}
	model.series = ipc.Series{
		Range: string(store.PeriodToday), Dimension: string(store.DimensionModel), Bucket: string(store.BucketHour),
		FromUnix: midnight.Unix(), ToUnix: now.Unix(),
		Points: []ipc.SeriesPoint{
			{StartUnix: midnight.Add(10 * time.Hour).Unix(), Series: "deepseek-v4-flash",
				Requests: 2, HitTokens: 200, MissTokens: 500, OutTokens: 100, Cost: 0.1},
			{StartUnix: midnight.Add(11 * time.Hour).Unix(), Series: "deepseek-v4-flash",
				Requests: 1, HitTokens: 100, MissTokens: 200, OutTokens: 50, Cost: 0.0234},
			{StartUnix: midnight.Add(11 * time.Hour).Unix(), Series: "deepseek-chat",
				Requests: 1, HitTokens: 20, MissTokens: 40, OutTokens: 10, Cost: 0.01},
		},
	}
	model.requests = ipc.Requests{
		Total: 3, Offset: 0,
		Rows: []ipc.RequestRow{
			{ID: "3", TimestampUnix: now.Unix(), Model: "deepseek-v4-flash", Client: "opencode",
				Endpoint: "/v1/chat/completions", PromptTokens: 800, CompletionTokens: 100,
				TotalTokens: 900, LatencyMS: 245, StatusCode: 200, Cost: 0.1},
			{ID: "2", TimestampUnix: now.Add(-time.Minute).Unix(), Model: "deepseek-chat", Client: "Codex Desktop",
				Endpoint: "/v1/responses", PromptTokens: 300, CompletionTokens: 34,
				TotalTokens: 334, LatencyMS: 120, StatusCode: 200, Cost: 0.0234},
		},
	}
	model.pricing = ipc.Pricing{Count: 2, LatestValidUnix: now.AddDate(0, 0, -1).Unix(), LatestSource: "bundled"}
	return model
}

func assertCanvas(t *testing.T, model *Model, width, height int) string {
	t.Helper()
	model.width, model.height = width, height
	output := renderModel(model)
	lines := strings.Split(output, "\n")
	if len(lines) != height {
		t.Fatalf("%dx%d: got %d lines", width, height, len(lines))
	}
	for index, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("%dx%d line %d width = %d, want %d: %q", width, height, index, got, width, line)
		}
	}
	return output
}

func TestRenderFitsEveryPageAtSupportedSizes(t *testing.T) {
	sizes := []struct {
		width  int
		height int
	}{{40, 24}, {80, 24}, {200, 60}}
	for _, size := range sizes {
		for _, page := range []Page{PageOverview, PageUsage, PageRequests, PagePricing} {
			model := sampleModel()
			model.page = page
			assertCanvas(t, model, size.width, size.height)
		}
	}
}

func TestRenderEmptyAndSingleRequestStates(t *testing.T) {
	empty := NewModel(nil, false)
	empty.color = false
	for _, page := range []Page{PageOverview, PageUsage, PageRequests, PagePricing} {
		empty.page = page
		output := assertCanvas(t, empty, 80, 24)
		if !strings.Contains(output, "No ") && page != PagePricing {
			t.Fatalf("empty page %d lacks an empty-state message: %q", page, output)
		}
	}

	single := sampleModel()
	single.page = PageRequests
	single.requests.Total = 1
	single.requests.Rows = single.requests.Rows[:1]
	output := assertCanvas(t, single, 80, 24)
	for _, want := range []string{"1–1 / 1", "Selected", "/v1/chat/completions"} {
		if !strings.Contains(output, want) {
			t.Fatalf("single request page missing %q", want)
		}
	}
}

func TestRenderUsesTitledPanels(t *testing.T) {
	tests := []struct {
		page   Page
		titles []string
	}{
		{PageOverview, []string{"╭─┐Account┌", "╭─┐Activity", "╭─┐Traffic", "╭─┐Recent requests┌"}},
		{PageUsage, []string{"╭─┐Query range┌", "╭─┐Usage trend", "╭─┐Series details"}},
		{PageRequests, []string{"╭─┐Requests 1–2 / 3", "╭─┐Request details┌"}},
		{PagePricing, []string{"╭─┐Pricing revisions┌", "╭─┐Notes┌"}},
	}
	for _, test := range tests {
		model := sampleModel()
		model.page = test.page
		model.ascii = false
		model.color = false
		output := assertCanvas(t, model, 100, 30)
		for _, title := range test.titles {
			if !strings.Contains(output, title) {
				t.Fatalf("page %s missing panel title %q", pageTitles[test.page], title)
			}
		}
		if !strings.Contains(output, "╰──") {
			t.Fatalf("page %s missing panel bottom border", pageTitles[test.page])
		}
	}

	ascii := sampleModel()
	ascii.ascii = true
	ascii.color = false
	output := assertCanvas(t, ascii, 80, 24)
	if !strings.Contains(output, "+-+Account+") || strings.Contains(output, "╭─┐") {
		t.Fatalf("ASCII panel fallback = %q", output)
	}
}

func TestNavigationUsesLowercaseHotkeysAndRightAlignedActions(t *testing.T) {
	model := sampleModel()
	model.page = PageOverview
	segments := model.navigationSegments(100)
	text := segmentsText(segments)
	for _, want := range []string{"overview", "usage", "requests", "pricing", "settings", "help", "quit"} {
		if !strings.Contains(text, want) {
			t.Fatalf("navigation missing %q: %q", want, text)
		}
	}
	if got := lipgloss.Width(text); got != 100 {
		t.Fatalf("navigation width = %d, want 100: %q", got, text)
	}
	if !strings.HasSuffix(strings.TrimRight(text, " "), "quit") {
		t.Fatalf("actions are not right aligned: %q", text)
	}

	var shortcuts []string
	whiteText := false
	for _, segment := range segments {
		if segment.color == model.theme.Accent {
			shortcuts = append(shortcuts, segment.text)
		}
		if segment.color == navigationTextColor && strings.TrimSpace(segment.text) != "" {
			whiteText = true
		}
	}
	if got, want := strings.Join(shortcuts, ""), "ouepshq"; got != want {
		t.Fatalf("navigation shortcuts = %q, want %q", got, want)
	}
	if !whiteText {
		t.Fatal("navigation text was not rendered in white")
	}
}

func TestNavigationShortcutsSwitchPagesAndHelp(t *testing.T) {
	model := sampleModel()
	tests := []struct {
		key  string
		page Page
	}{
		{"u", PageUsage},
		{"e", PageRequests},
		{"p", PagePricing},
		{"o", PageOverview},
	}
	for _, test := range tests {
		_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(test.key)})
		if model.page != test.page {
			t.Fatalf("key %q page = %v, want %v", test.key, model.page, test.page)
		}
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if !model.help {
		t.Fatal("h did not open help")
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if model.help {
		t.Fatal("h did not close help")
	}
}

func TestFooterHintIsRemoved(t *testing.T) {
	model := sampleModel()
	output := assertCanvas(t, model, 100, 30)
	if strings.Contains(output, "Tab pages") || strings.Contains(output, "1-6 range") {
		t.Fatalf("footer hint is still present: %q", output)
	}
}

func TestBrandShowsVersion(t *testing.T) {
	model := sampleModel()
	output := assertCanvas(t, model, 100, 30)
	if !strings.Contains(output, "DS-mon "+version.Current) {
		t.Fatalf("dashboard header is missing version: %q", output)
	}

	settings := NewSettingsModel(nil, true, settingsFixture(), false)
	settingsOutput := renderSettingsAt(t, settings, 100, 30)
	if !strings.Contains(settingsOutput, "DS-mon "+version.Current+" Settings") {
		t.Fatalf("settings header is missing version: %q", settingsOutput)
	}
}

func TestPanelBordersMatchBtopBoxShape(t *testing.T) {
	tests := []struct {
		name      string
		width     int
		title     string
		ascii     bool
		wantStart string
		wantEnd   string
	}{
		{"unicode", 32, "Activity", false, "╭─┐Activity┌", "╮"},
		{"unicode long title", 32, "Recent requests", false, "╭─┐Recent requests┌", "╮"},
		{"ascii", 32, "Activity", true, "+-+Activity+", "+"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := newRenderer(test.width, 3, test.ascii, false)
			r.panel(test.title, 3, toneBoxActivity, func(panel *renderer) { panel.add(toneNormal, "x") })
			lines := r.finish()
			if len(lines) != 3 {
				t.Fatalf("panel lines = %d, want 3", len(lines))
			}
			for index, line := range lines {
				if got := lipgloss.Width(line); got != test.width {
					t.Fatalf("line %d width = %d, want %d: %q", index, got, test.width, line)
				}
			}
			if !strings.HasPrefix(lines[0], test.wantStart) || !strings.HasSuffix(lines[0], test.wantEnd) {
				t.Fatalf("top border = %q, want prefix %q and suffix %q", lines[0], test.wantStart, test.wantEnd)
			}
			if test.ascii {
				if !strings.HasPrefix(lines[2], "+-") || !strings.HasSuffix(lines[2], "-+") {
					t.Fatalf("ASCII bottom border = %q", lines[2])
				}
			} else if !strings.HasPrefix(lines[2], "╰─") || !strings.HasSuffix(lines[2], "─╯") {
				t.Fatalf("Unicode bottom border = %q", lines[2])
			}
		})
	}
}

func TestRenderHelpDateEditorAndASCIIFallback(t *testing.T) {
	model := sampleModel()
	model.help = true
	output := assertCanvas(t, model, 80, 24)
	if !strings.Contains(output, "Help") || !strings.Contains(output, "Tab") {
		t.Fatalf("help page = %q", output)
	}

	model.help = false
	model.page = PageUsage
	model.editingDates = true
	model.dateFields = [2]string{"2026-11-01", "2026-11-07"}
	output = assertCanvas(t, model, 80, 24)
	if !strings.Contains(output, "Custom date") || !strings.Contains(output, "[2026-11-01]") {
		t.Fatalf("date editor = %q", output)
	}

	model.editingDates = false
	model.page = PageOverview
	model.ascii = true
	output = assertCanvas(t, model, 80, 24)
	if strings.ContainsAny(output, "⣿⣷⣶") {
		t.Fatalf("ASCII fallback still contains Braille: %q", output)
	}
}

func TestActivityValueAtUsesSmoothInterpolation(t *testing.T) {
	counts := []int{0, 0, 1, 0, 0}
	if got := activityValueAt(counts, 0); got != 0 {
		t.Fatalf("activity start = %v, want 0", got)
	}
	if got := activityValueAt(counts, 2); got != 1 {
		t.Fatalf("activity peak = %v, want 1", got)
	}
	if got := activityValueAt(counts, float64(len(counts)-1)); got != 0 {
		t.Fatalf("activity end = %v, want 0", got)
	}
	smooth := activityValueAt(counts, 1.5)
	if math.Abs(smooth-0.5) < 1e-9 {
		t.Fatalf("activity midpoint is still linear: %v", smooth)
	}
	if smooth <= 0.5 {
		t.Fatalf("activity midpoint = %v, want a rounded shoulder above linear", smooth)
	}
	for step := 0; step <= 40; step++ {
		value := activityValueAt(counts, float64(step)/10)
		if value < 0 || value > 1.000001 {
			t.Fatalf("activity overshoot at step %d: %v", step, value)
		}
	}
}

func TestCanvasRenderersRespectSize(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	activity := RenderActivity([]int{0, 1, 2, 3, 2, 1, 0, 1, 2, 3}, 40, 6, now, 3)
	if len(activity) != 6 {
		t.Fatalf("activity rows = %d", len(activity))
	}
	for _, row := range activity {
		if lipgloss.Width(row) != 40 {
			t.Fatalf("activity row width = %d", lipgloss.Width(row))
		}
	}
	if got := lipgloss.Width(RenderActivityBlocks([]int{1, 2, 3}, 33)); got != 33 {
		t.Fatalf("block width = %d", got)
	}

	buckets := make([]trace.Bucket, trace.DefaultTrafficBucketCount)
	buckets[len(buckets)-1] = trace.Bucket{Upload: 1024, Download: 4096}
	frame := trace.MakeFrame(buckets, time.Second, now, 80)
	down, up := RenderTraffic(frame, 40, 5)
	if len(down) != 3 || len(up) != 2 {
		t.Fatalf("traffic rows = %d/%d", len(down), len(up))
	}
	for _, rows := range [][]string{down, up} {
		for _, row := range rows {
			if lipgloss.Width(row) != 40 {
				t.Fatalf("traffic row width = %d", lipgloss.Width(row))
			}
		}
	}
}

func TestTrafficPanelShowsBothLanes(t *testing.T) {
	model := sampleModel()
	model.page = PageOverview
	model.ascii = true
	output := assertCanvas(t, model, 100, 30)
	for _, label := range []string{"↓ 4.0K/s", "↑ 512B/s"} {
		if !strings.Contains(output, label) {
			t.Fatalf("traffic panel missing %q: %q", label, output)
		}
	}
}

func TestOverviewActivitySharesAccountRowAtRightThird(t *testing.T) {
	model := sampleModel()
	model.page = PageOverview
	model.ascii = false
	output := assertCanvas(t, model, 100, 40)
	lines := strings.Split(output, "\n")
	accountRow := -1
	for index, line := range lines {
		if !strings.HasPrefix(line, "╭─┐Account") {
			continue
		}
		accountRow = index
		if !strings.Contains(line, "Activity ·") {
			t.Fatalf("activity is not on the account row: %q", line)
		}
		activityByte := strings.Index(line, "Activity")
		activityColumn := lipgloss.Width(line[:activityByte])
		if activityColumn < 100*2/3 || activityColumn > 100*2/3+6 {
			t.Fatalf("activity starts at column %d, want right third around %d", activityColumn, 100*2/3)
		}
		break
	}
	if accountRow < 0 {
		t.Fatalf("account panel is missing: %q", output)
	}
	for index, line := range lines {
		if strings.HasPrefix(line, "╭─┐Traffic · ") {
			if index <= accountRow {
				t.Fatalf("traffic panel is above the account row: %d <= %d", index, accountRow)
			}
			if lipgloss.Width(line) != 100 {
				t.Fatalf("traffic panel is not full width: %q", line)
			}
			return
		}
	}
	t.Fatalf("full-width traffic panel is missing: %q", output)
}

func TestTrafficGradientMatchesBtopTheme(t *testing.T) {
	tests := []struct {
		ratio  float64
		upload bool
		want   string
	}{
		{0, false, "#e0af68"},
		{1, false, "#f7768e"},
		{0, true, "#9ece6a"},
		{0.5, true, "#449dab"},
		{1, true, "#7aa2f7"},
	}
	for _, test := range tests {
		if got := trafficGradientColor(test.ratio, test.upload); got != test.want {
			t.Fatalf("gradient ratio=%v upload=%v = %q, want %q", test.ratio, test.upload, got, test.want)
		}
	}
}

func TestTrafficPanelMatchesBtopNetLayout(t *testing.T) {
	model := sampleModel()
	model.page = PageOverview
	model.ascii = false
	model.trace.DownTop = 16 * 1024
	model.trace.UpTop = 2 * 1024
	model.trace.DownTotal = 898 * 1024 * 1024 * 1024
	model.trace.UpTotal = 486 * 1024 * 1024 * 1024
	output := assertCanvas(t, model, 100, 40)
	for _, want := range []string{"download", "upload", "▼", "▲", "Top:", "Total:", "KiB/s", "Kibps", "GiB"} {
		if !strings.Contains(output, want) {
			t.Fatalf("btop net layout missing %q: %q", want, output)
		}
	}
}

func TestTrafficNetUsesMirroredSharedCanvas(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	buckets := make([]trace.Bucket, trace.DefaultTrafficBucketCount)
	for index := len(buckets) - 20; index < len(buckets); index++ {
		buckets[index] = trace.Bucket{Upload: 2 * 1024, Download: 8 * 1024}
	}
	frame := trace.MakeFrame(buckets, trace.DefaultTrafficBucketSeconds, now, 120)
	rows := RenderTrafficNet(frame, 60, 9)
	if len(rows) != 9 {
		t.Fatalf("net rows = %d, want 9", len(rows))
	}
	for index, row := range rows {
		if lipgloss.Width(row) != 60 {
			t.Fatalf("row %d width = %d, want 60", index, lipgloss.Width(row))
		}
	}
	top := strings.Join(rows[:4], "")
	bottom := strings.Join(rows[5:], "")
	if !strings.ContainsFunc(top, func(value rune) bool { return value >= 0x2800 && value <= 0x28ff }) {
		t.Fatalf("download half has no Braille: %q", rows[:4])
	}
	if !strings.ContainsFunc(bottom, func(value rune) bool { return value >= 0x2800 && value <= 0x28ff }) {
		t.Fatalf("upload half has no Braille: %q", rows[5:])
	}
}

func TestTrafficStatsBoxIsSingleCenteredBtopPanel(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	frame := trace.MakeFrame(make([]trace.Bucket, 120), trace.DefaultTrafficBucketSeconds, now, 120)
	box := trafficStatsBox(frame, 27, 9)
	if !strings.Contains(box[0], "download") {
		t.Fatalf("download title not embedded in top border: %q", box[0])
	}
	if !strings.Contains(box[len(box)-1], "upload") {
		t.Fatalf("upload title not embedded in bottom border: %q", box[len(box)-1])
	}
	for index, row := range box {
		if lipgloss.Width(row) != 27 {
			t.Fatalf("stats box row %d width = %d, want 27", index, lipgloss.Width(row))
		}
	}
	for index := 1; index < len(box)-1; index++ {
		if !strings.HasPrefix(box[index], "│") || !strings.HasSuffix(box[index], "│") {
			t.Fatalf("stats box row %d is not a single bordered panel: %q", index, box[index])
		}
	}
	rows := trafficNetStats(frame, 27, 16)
	if len(rows) != 16 {
		t.Fatalf("stats rows = %d, want 16", len(rows))
	}
	// 9 行高的统计框应居中，上下各留出空白，而不是把标题贴到面板边框上。
	if strings.TrimSpace(rows[0]) != "" || strings.TrimSpace(rows[1]) != "" {
		t.Fatalf("stats box is not vertically centered: %q", rows[:4])
	}
	start := -1
	for index, row := range rows {
		if strings.Contains(row, "download") {
			start = index
			break
		}
	}
	if start < 0 || start != len(rows)/2-9/2 {
		t.Fatalf("stats box start = %d, want %d", start, len(rows)/2-9/2)
	}
}

func TestTrafficStatsBoxUsesGrayDividerBorder(t *testing.T) {
	frame := trace.MakeFrame(make([]trace.Bucket, 120), trace.DefaultTrafficBucketSeconds,
		time.Unix(1_700_000_000, 0), 120)
	box := trafficStatsBox(frame, 27, 9)
	top := trafficStatsSegments(box[0])
	if len(top) != 3 || top[0].tone != toneDivLine || top[2].tone != toneDivLine {
		t.Fatalf("top border is not gray: %+v", top)
	}
	if top[1].tone != toneTitle || top[1].text != "download" {
		t.Fatalf("download title segment = %+v", top[1])
	}
	middle := trafficStatsSegments(box[2])
	if len(middle) != 3 || middle[0].tone != toneDivLine || middle[2].tone != toneDivLine {
		t.Fatalf("side borders are not gray: %+v", middle)
	}
	if middle[1].tone != toneNormal {
		t.Fatalf("stats content tone = %v, want normal", middle[1].tone)
	}
}

func TestTrafficNetHasNoVerticalGrid(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	frame := trace.MakeFrame(make([]trace.Bucket, 120), trace.DefaultTrafficBucketSeconds, now, 120)
	frame.GridLines = []float64{5, 25, 45}
	rows := RenderTrafficNet(frame, 60, 9)
	for index, row := range rows {
		if index == 4 || index == 5 {
			continue
		}
		if strings.TrimSpace(row) != "" {
			t.Fatalf("row %d should be empty without net data, got %q", index, row)
		}
	}
	if strings.TrimSpace(rows[4]) == "" || strings.TrimSpace(rows[5]) == "" {
		t.Fatalf("download/upload baselines are missing: %q %q", rows[4], rows[5])
	}
}

func TestTrafficNetNewestBucketStaysAtRightEdge(t *testing.T) {
	buckets := make([]trace.Bucket, 120)
	buckets[119] = trace.Bucket{Upload: 1000, Download: 1000}
	frame := trace.Frame{Buckets: buckets, BucketSeconds: time.Second, DownScale: 1000, UpScale: 1000}
	width := 44
	rows := RenderTrafficNet(frame, width, 9)
	downColumns := trafficDataColumns(rows, 0, 4)
	upColumns := trafficDataColumns(rows, 6, 9)
	for name, columns := range map[string][]int{"download": downColumns, "upload": upColumns} {
		if len(columns) != 1 {
			t.Fatalf("%s raw bucket spans %d columns, want one vertical line: %v", name, len(columns), columns)
		}
		rightmost := columns[len(columns)-1]
		if rightmost < width-2 {
			t.Fatalf("%s newest bucket ends at column %d, want near right edge %d", name, rightmost, width-1)
		}
	}
}

func TestTrafficNetOldBucketScrollsLeft(t *testing.T) {
	buckets := make([]trace.Bucket, 120)
	buckets[60] = trace.Bucket{Upload: 1000, Download: 1000}
	frame := trace.Frame{Buckets: buckets, BucketSeconds: time.Second, DownScale: 1000, UpScale: 1000}
	width := 44
	rows := RenderTrafficNet(frame, width, 9)
	downColumns := trafficDataColumns(rows, 0, 4)
	upColumns := trafficDataColumns(rows, 6, 9)
	if len(downColumns) == 0 || len(upColumns) == 0 {
		t.Fatal("historical spike did not render")
	}
	if downColumns[len(downColumns)-1] >= width/2 {
		t.Fatalf("historical download spike stayed at the right edge: %v", downColumns)
	}
	if upColumns[len(upColumns)-1] >= width/2 {
		t.Fatalf("historical upload spike stayed at the right edge: %v", upColumns)
	}
}

func TestTrafficNetNarrowWindowDropsSamplesOlderThanVisibleWindow(t *testing.T) {
	buckets := make([]trace.Bucket, 120)
	buckets[75] = trace.Bucket{Download: 1000}
	frame := trace.Frame{Buckets: buckets, BucketSeconds: time.Second, DownScale: 1000, UpScale: 1000}
	if columns := trafficDataColumns(RenderTrafficNet(frame, 20, 9), 0, 4); len(columns) != 0 {
		t.Fatalf("old sample outside the narrow window is still compressed into view: %v", columns)
	}

	buckets = make([]trace.Bucket, 120)
	buckets[100] = trace.Bucket{Download: 1000}
	frame.Buckets = buckets
	if columns := trafficDataColumns(RenderTrafficNet(frame, 20, 9), 0, 4); len(columns) == 0 {
		t.Fatal("sample inside the narrow window did not render")
	}
}

func TestTrafficNetWideWindowKeepsOneDotColumnPerBucket(t *testing.T) {
	buckets := make([]trace.Bucket, 2)
	buckets[1] = trace.Bucket{Download: 1000, Upload: 1000}
	frame := trace.Frame{Buckets: buckets, BucketSeconds: time.Second, DownScale: 1000, UpScale: 1000}
	rows := RenderTrafficNet(frame, 20, 9)
	downColumns := trafficDataColumns(rows, 0, 4)
	upColumns := trafficDataColumns(rows, 6, 9)
	for name, columns := range map[string][]int{"download": downColumns, "upload": upColumns} {
		if len(columns) != 1 {
			t.Fatalf("%s wide-window columns = %v, want one fixed dot column", name, columns)
		}
		if columns[0] != 19 {
			t.Fatalf("%s spike column = %d, want right-aligned column 19", name, columns[0])
		}
	}
}

func TestTrafficNetBaselineSpansFullWidthWithShortHistory(t *testing.T) {
	buckets := make([]trace.Bucket, 2)
	buckets[1] = trace.Bucket{Download: 1000, Upload: 1000}
	frame := trace.Frame{Buckets: buckets, BucketSeconds: time.Second, DownScale: 1000, UpScale: 1000}
	rows := RenderTrafficNet(frame, 20, 9)
	for _, rowIndex := range []int{4, 5} {
		if strings.ContainsRune(rows[rowIndex], ' ') {
			t.Fatalf("baseline row %d does not span the full width: %q", rowIndex, rows[rowIndex])
		}
	}
}

func TestTrafficNetSingleBucketWidthStaysStableWhileScrolling(t *testing.T) {
	buckets := make([]trace.Bucket, 2)
	buckets[1] = trace.Bucket{Download: 1000, Upload: 1000}
	frame := trace.Frame{Buckets: buckets, BucketSeconds: time.Second, DownScale: 1000, UpScale: 1000}
	for shift := 0; shift < 8; shift++ {
		frame.Shift = float64(shift)
		rows := RenderTrafficNet(frame, 20, 9)
		for name, columns := range map[string][]int{
			"download": trafficDataColumns(rows, 0, 4),
			"upload":   trafficDataColumns(rows, 6, 9),
		} {
			if len(columns) != 1 {
				t.Fatalf("shift %d: %s rendered %d vertical lines, want 1: %v", shift, name, len(columns), columns)
			}
		}
	}
}

func trafficDataColumns(rows []string, from, to int) []int {
	seen := make(map[int]bool)
	var columns []int
	for row := from; row < to && row < len(rows); row++ {
		for column, value := range []rune(rows[row]) {
			if value != ' ' && !seen[column] {
				seen[column] = true
				columns = append(columns, column)
			}
		}
	}
	sort.Ints(columns)
	return columns
}

func TestBalanceTrendUsesBrailleByDefault(t *testing.T) {
	model := sampleModel()
	model.page = PageOverview
	model.ascii = false
	line := model.balanceTrendLine(100)
	if !strings.ContainsFunc(line, func(value rune) bool {
		return value >= 0x2800 && value <= 0x28ff
	}) {
		t.Fatalf("balance trend has no Braille dots: %q", line)
	}
	if strings.ContainsAny(line, "▁▂▃▄▅▆▇█") {
		t.Fatalf("balance trend still uses block sparkline: %q", line)
	}
}

func TestBalanceTrendASCIIFallbackUsesBlocks(t *testing.T) {
	model := sampleModel()
	model.ascii = true
	line := model.balanceTrendLine(100)
	if !strings.ContainsAny(line, "▁▂▃▄▅▆▇█") {
		t.Fatalf("ASCII balance trend has no block sparkline: %q", line)
	}
	if strings.ContainsFunc(line, func(value rune) bool {
		return value >= 0x2800 && value <= 0x28ff
	}) {
		t.Fatalf("ASCII balance trend still has Braille dots: %q", line)
	}
}

func TestBalanceSparklineKeepsRequestedWidth(t *testing.T) {
	points := []ipc.BalancePoint{{Unix: 1, Value: 20}, {Unix: 2, Value: 21}, {Unix: 3, Value: 19}}
	for _, braille := range []bool{false, true} {
		if got := lipgloss.Width(balanceSparkline(points, 24, braille)); got != 24 {
			t.Fatalf("braille=%v width = %d, want 24", braille, got)
		}
	}
}

func TestBalanceTrendLeavesUnsampledTimeEmpty(t *testing.T) {
	points := []ipc.BalancePoint{
		{Unix: 1, Missing: true},
		{Unix: 2, Missing: true},
		{Unix: 3, Value: 20},
		{Unix: 4, Value: 19},
	}
	for _, braille := range []bool{false, true} {
		line := balanceSparkline(points, 4, braille)
		if lipgloss.Width(line) != 4 {
			t.Fatalf("braille=%v width = %d, want 4: %q", braille, lipgloss.Width(line), line)
		}
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("braille=%v missing lead-in was stretched: %q", braille, line)
		}
		if strings.TrimSpace(line[2:]) == "" {
			t.Fatalf("braille=%v sampled tail was not drawn: %q", braille, line)
		}
	}
}

func TestBalanceRangeIgnoresMissingBuckets(t *testing.T) {
	points := []ipc.BalancePoint{
		{Missing: true},
		{Value: 21.5},
		{Missing: true},
		{Value: 20},
	}
	if got := balanceRangeLabel(points); got != "20.00–21.50" {
		t.Fatalf("range = %q", got)
	}
}

func TestBalanceTrendExpandsShortCoverageAndLabelsItsSpan(t *testing.T) {
	points := make([]ipc.BalancePoint, 288)
	for index := range points {
		points[index] = ipc.BalancePoint{Unix: int64(index * 300), Missing: true}
	}
	for index := 258; index < len(points); index++ {
		points[index].Missing = false
		points[index].Value = 60 - float64(index-258)/10
	}
	if got := balanceTrendLabel("24h", points, 24*time.Hour); got != "24h·2.5h" {
		t.Fatalf("label = %q, want 24h·2.5h", got)
	}

	model := sampleModel()
	strict := segmentsText(balanceSparklineSegments(points, 8, true))
	expanded := segmentsText(model.balanceSparklineSegments(points, 8, true, 24*time.Hour))
	if !strings.HasPrefix(strict, "      ") {
		t.Fatalf("strict trend did not preserve leading gap: %q", strict)
	}
	if strings.HasPrefix(expanded, " ") {
		t.Fatalf("short-coverage trend was not expanded: %q", expanded)
	}
}

func TestBalanceTrendGradientVariesWithHeight(t *testing.T) {
	points := []ipc.BalancePoint{{Unix: 1, Value: 10}, {Unix: 2, Value: 20}}
	segments := balanceSparklineSegments(points, 4, true)
	if len(segments) != 4 {
		t.Fatalf("gradient segments = %d, want 4", len(segments))
	}
	if segments[0].color != "#f7768e" {
		t.Fatalf("low trend color = %q, want #f7768e", segments[0].color)
	}
	if segments[len(segments)-1].color != "#9ece6a" {
		t.Fatalf("high trend color = %q, want #9ece6a", segments[len(segments)-1].color)
	}
}

func TestBalanceTrendLabelsAxes(t *testing.T) {
	model := sampleModel()
	model.ascii = false
	line := model.balanceTrendLine(100)
	for _, want := range []string{"Balance", "24h", "7d", "22.22–23.00", "22.22–25.00", "Time→"} {
		if !strings.Contains(line, want) {
			t.Fatalf("balance trend axes missing %q: %q", want, line)
		}
	}
}

func TestUsageChartLabelsAxesAndFillsWidth(t *testing.T) {
	model := sampleModel()
	model.page = PageUsage
	model.ascii = false
	output := assertCanvas(t, model, 100, 30)
	for _, want := range []string{"tokens", "by [model] client", "┤", "└", "→", "⣿", "00:00", "06:00", "12:00"} {
		if !strings.Contains(output, want) {
			t.Fatalf("usage chart axes missing %q: %q", want, output)
		}
	}

	_, buckets, bucketCount := usageChartData(model.series)
	columns, totals, maximum := usageChartColumns(buckets, bucketCount, 40)
	if len(columns) != 40 || len(totals) != 40 || maximum <= 0 {
		t.Fatalf("usage columns = %d/%d max=%d, want 40/40/positive", len(columns), len(totals), maximum)
	}
	nonZero := 0
	for _, total := range totals {
		if total > 0 {
			nonZero++
		}
	}
	if nonZero < 4 {
		t.Fatalf("usage bars occupy only %d columns; chart did not scale to width", nonZero)
	}
}

func TestUsageChartGrowsFromBottom(t *testing.T) {
	model := sampleModel()
	model.ascii = false
	r := newRenderer(24, 6, false, false)
	totals := []usageTotal{{Name: "half", HitTokens: 50}, {Name: "full", HitTokens: 100}}
	buckets := [][]usageTotal{{{Name: "half", HitTokens: 50}}, {{Name: "full", HitTokens: 100}}}
	model.appendUsageChart(r, totals, buckets, 2, 6)
	lines := r.finish()
	if len(lines) != 6 {
		t.Fatalf("usage chart lines = %d, want 6", len(lines))
	}
	columns, _, _, labelWidth := usageChartGeometry(buckets, 2, r.width)
	leftWidth := len(columns) / 2
	for index := 0; index < 2; index++ {
		bars := []rune(lines[index])[labelWidth+2:]
		if strings.TrimSpace(string(bars[:leftWidth])) != "" {
			t.Fatalf("top row %d should be empty for the 50%% column: %q", index, lines[index])
		}
	}
	for index := 2; index < 4; index++ {
		bars := []rune(lines[index])[labelWidth+2:]
		if !strings.Contains(string(bars[:leftWidth]), usageBarGlyph(0, false)) {
			t.Fatalf("bottom row %d should be filled for the 50%% column: %q", index, lines[index])
		}
	}
	if !strings.Contains(lines[4], "└") {
		t.Fatalf("x axis missing from row 4: %q", lines[4])
	}
	if !strings.Contains(lines[5], "00:00") {
		t.Fatalf("x labels missing from row 5: %q", lines[5])
	}
}

func TestUsageXAxisLabelsUseBucketUnits(t *testing.T) {
	hourly := sampleModel().series
	got := usageXAxisLabels(hourly)
	want := []string{"00:00", "06:00", "12:00"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("hour labels = %v, want %v", got, want)
	}

	daily := hourly
	daily.Bucket = string(store.BucketDay)
	daily.FromUnix = time.Date(2026, time.November, 1, 0, 0, 0, 0, time.Local).Unix()
	daily.ToUnix = time.Date(2026, time.November, 4, 0, 0, 0, 0, time.Local).Unix()
	got = usageXAxisLabels(daily)
	want = []string{"11/01", "11/02", "11/03"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("day labels = %v, want %v", got, want)
	}

	daily.ToUnix = time.Date(2026, time.December, 1, 0, 0, 0, 0, time.Local).Unix()
	got = usageXAxisLabels(daily)
	want = []string{"11/01", "11/16", "11/30"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("full month labels = %v, want %v", got, want)
	}
}

func TestUsageXAxisLabelsStayInsidePlotWidth(t *testing.T) {
	line := placeUsageXAxisLabels([]string{"00:00", "06:00", "12:00"}, 30)
	if got := lipgloss.Width(line); got != 30 {
		t.Fatalf("x label row width = %d, want 30", got)
	}
	for _, want := range []string{"00:00", "06:00", "12:00"} {
		if !strings.Contains(line, want) {
			t.Fatalf("x label row missing %q: %q", want, line)
		}
	}
}

func TestUsageChartYAxisLabelsStayAligned(t *testing.T) {
	model := sampleModel()
	model.ascii = false
	r := newRenderer(40, 8, false, false)
	totals := []usageTotal{
		{Name: "half", HitTokens: 800_000},
		{Name: "full", HitTokens: 1_600_000},
	}
	buckets := [][]usageTotal{
		{{Name: "half", HitTokens: 800_000}},
		{{Name: "full", HitTokens: 1_600_000}},
	}
	model.appendUsageChart(r, totals, buckets, 2, 8)
	lines := r.finish()
	axisColumn := -1
	for row := 0; row < 6; row++ {
		runes := []rune(lines[row])
		found := -1
		for index, value := range runes {
			if value == '┤' || value == '│' {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("row %d has no y axis: %q", row, lines[row])
		}
		if axisColumn < 0 {
			axisColumn = found
		}
		if found != axisColumn {
			t.Fatalf("row %d y axis column = %d, want %d: %q", row, found, axisColumn, lines[row])
		}
	}
	if !strings.Contains(lines[2], "800.0K") && !strings.Contains(lines[3], "800.0K") {
		t.Fatalf("middle tick label missing: %q", strings.Join(lines[:6], "\n"))
	}
}

func TestHeightGradientGoesGreenYellowRed(t *testing.T) {
	cases := []struct {
		level float64
		want  string
	}{
		{0, "#9ece6a"},
		{0.5, "#e0af68"},
		{1, "#f7768e"},
	}
	for _, tt := range cases {
		if got := usageBarColor(tt.level); got != tt.want {
			t.Fatalf("usageBarColor(%v) = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestUsageChartColorsStackedSeriesBySeries(t *testing.T) {
	model := sampleModel()
	model.ascii = false
	r := newRenderer(24, 6, false, true)
	model.theme = fallbackPalette()
	totals := []usageTotal{{Name: "beta", HitTokens: 50}, {Name: "alpha", HitTokens: 50}}
	buckets := [][]usageTotal{{{Name: "alpha", HitTokens: 50}, {Name: "beta", HitTokens: 50}}}
	model.appendUsageChart(r, totals, buckets, 1, 6)
	colors := usageSeriesColorMap(totals, model.theme)
	if colors["alpha"] == colors["beta"] {
		t.Fatalf("series colors are not distinct: %+v", colors)
	}
	seen := map[string]bool{}
	for _, line := range r.lines {
		for _, segment := range line.segments {
			if segment.color != "" {
				seen[segment.color] = true
			}
		}
	}
	if !seen[colors["alpha"]] || !seen[colors["beta"]] {
		t.Fatalf("stacked series colors missing: seen=%v colors=%v", seen, colors)
	}
}

func TestPanelPreservesSegmentColors(t *testing.T) {
	r := newRenderer(40, 3, false, true)
	r.panel("Trend", 3, toneBoxAccount, func(panel *renderer) {
		panel.addSegments(toneNormal,
			uiSegment{text: "A", tone: toneNormal, color: "#f7768e"},
			uiSegment{text: "B", tone: toneNormal, color: "#9ece6a"})
	})
	for _, line := range r.lines {
		if len(line.segments) >= 2 && line.segments[0].color == "#f7768e" && line.segments[1].color == "#9ece6a" {
			return
		}
	}
	t.Fatalf("panel dropped segment colors: %+v", r.lines)
}

func TestTrafficRenderSplitsRequestedHeight(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	buckets := make([]trace.Bucket, trace.DefaultTrafficBucketCount)
	buckets[len(buckets)-1] = trace.Bucket{Upload: 1024, Download: 4096}
	frame := trace.MakeFrame(buckets, time.Second, now, 80)
	for _, height := range []int{1, 2, 5, 6} {
		down, up := RenderTraffic(frame, 40, height)
		if len(down)+len(up) != height {
			t.Fatalf("height %d produced %d+%d rows", height, len(down), len(up))
		}
	}
}

func TestTrafficRendererUsesBrailleDots(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	buckets := make([]trace.Bucket, trace.DefaultTrafficBucketCount)
	for index := len(buckets) - 8; index < len(buckets); index++ {
		buckets[index] = trace.Bucket{Upload: 4096, Download: 16 * 1024}
	}
	frame := trace.MakeFrame(buckets, trace.DefaultTrafficBucketSeconds, now, 160)
	down, up := RenderTraffic(frame, 80, 8)
	for _, rows := range [][]string{down, up} {
		if !strings.ContainsFunc(strings.Join(rows, ""), func(value rune) bool {
			return value >= 0x2800 && value <= 0x28ff
		}) {
			t.Fatalf("traffic lane has no Braille dots: %q", rows)
		}
	}
}

func TestActivityHasSingleHorizontalBaselineAndNoVerticalGrid(t *testing.T) {
	now := time.Unix(104*int64(trace.DefaultActivityBucketSeconds/time.Second), 0)
	rows := RenderActivity(make([]int, 20), 40, 6, now, 1)
	for rowIndex, row := range rows[:len(rows)-1] {
		if strings.TrimSpace(row) != "" {
			t.Fatalf("row %d should stay empty: %q", rowIndex, row)
		}
	}
	baseline := rows[len(rows)-1]
	if strings.TrimSpace(baseline) == "" {
		t.Fatal("activity baseline is missing")
	}
	if strings.ContainsFunc(baseline, func(value rune) bool {
		return value != '⣀'
	}) {
		t.Fatalf("activity baseline is not continuous: %q", baseline)
	}
}

func TestActivityCurveStillRendersWithoutGrid(t *testing.T) {
	now := time.Unix(104*int64(trace.DefaultActivityBucketSeconds/time.Second), 0)
	counts := make([]int, 20)
	counts[10] = 1
	rows := RenderActivity(counts, 40, 6, now, 3)
	if strings.TrimSpace(strings.Join(rows[:2], "")) != "" {
		t.Fatalf("top rows should stay empty without a grid: %q", rows)
	}
	if !strings.ContainsFunc(strings.Join(rows, ""), func(value rune) bool {
		return value >= 0x2800 && value <= 0x28ff
	}) {
		t.Fatalf("activity curve did not render: %q", rows)
	}
}

func TestActivityWindowFollowsPanelWidth(t *testing.T) {
	model := sampleModel()
	model.trace.ActivityBucketSecs = int(trace.DefaultActivityBucketSeconds / time.Second)
	if got := model.activityTitle(30); got != "Activity · 120 sec · 3 req/cell" {
		t.Fatalf("activity title = %q", got)
	}

	counts := make([]int, trace.DefaultActivityBucketCount)
	counts[len(counts)-1] = 3
	now := time.Unix(104*int64(trace.DefaultActivityBucketSeconds/time.Second), 0)
	rows := RenderActivity(counts, 40, 6, now, 3)
	runes := []rune(rows[0])
	for index := 0; index < len(runes)-1; index++ {
		if runes[index] != ' ' {
			t.Fatalf("activity history was stretched into column %d: %q", index, rows[0])
		}
	}
	if runes[len(runes)-1] == ' ' {
		t.Fatalf("latest activity bucket is not right aligned: %q", rows[0])
	}
}

func TestActivityGradientColorsRowsByHeight(t *testing.T) {
	model := sampleModel()
	model.ascii = false
	model.theme = fallbackPalette()
	model.trace.Health = true
	model.trace.Activity = make([]int, trace.DefaultActivityBucketCount)
	model.trace.ActivityFullScale = 3
	model.trace.Activity[len(model.trace.Activity)-1] = 3
	r := newRenderer(40, 6, false, true, model.theme)
	model.appendActivityChart(r, 6)
	if len(r.lines) != 6 {
		t.Fatalf("activity rows = %d, want 6", len(r.lines))
	}
	top := r.lines[0].segments[0].color
	bottom := r.lines[len(r.lines)-1].segments[0].color
	if top != "#f7768e" || bottom != "#9ece6a" {
		t.Fatalf("activity gradient top=%q bottom=%q", top, bottom)
	}
}

func TestActivityFaultStaysSolidRed(t *testing.T) {
	model := sampleModel()
	model.ascii = false
	model.trace.Health = false
	model.trace.Activity = make([]int, trace.DefaultActivityBucketCount)
	model.trace.ActivityFullScale = 3
	r := newRenderer(40, 6, false, true, model.theme)
	model.appendActivityChart(r, 6)
	for index, line := range r.lines {
		if line.tone != toneError {
			t.Fatalf("row %d tone = %v, want error", index, line.tone)
		}
		for _, segment := range line.segments {
			if segment.color != "" {
				t.Fatalf("fault row %d used gradient color %q", index, segment.color)
			}
		}
	}
}

func TestTrafficGridUsesDenseDots(t *testing.T) {
	frame := trace.Frame{
		Buckets:       make([]trace.Bucket, trace.DefaultTrafficBucketCount),
		BucketSeconds: trace.DefaultTrafficBucketSeconds,
		GridLines:     []float64{4, 20},
		DownScale:     1024,
		UpScale:       1024,
	}
	rows := renderTrafficLane(frame, 40, 2, false)
	if !strings.Contains(rows[0], "⡇") {
		t.Fatalf("vertical grid is not a four-dot column: %q", rows[0])
	}
	if !strings.Contains(rows[1], "⠉⠉⠉") {
		t.Fatalf("horizontal grid is not a continuous dot row: %q", rows[1])
	}
}

func TestTrafficSparklineKeepsIdleFlat(t *testing.T) {
	frame := trace.Frame{Buckets: make([]trace.Bucket, 10), BucketSeconds: time.Second}
	if got := trafficSparkline(frame, false, 24); got != strings.Repeat(" ", 24) {
		t.Fatalf("idle sparkline = %q", got)
	}
}

func TestRequestSelectionScrollsViewport(t *testing.T) {
	model := sampleModel()
	model.page = PageRequests
	model.width, model.height = 80, 24
	model.requests.Total = 30
	model.requests.Rows = make([]ipc.RequestRow, 30)
	base := model.requests.Rows[0]
	for index := range model.requests.Rows {
		model.requests.Rows[index] = base
		model.requests.Rows[index].ID = fmt.Sprintf("%d", index)
	}
	for index := 0; index < 25; index++ {
		_, _ = model.updateKey("j")
	}
	if model.requestRow != 25 || model.requestTop == 0 {
		t.Fatalf("selection did not scroll: row=%d top=%d", model.requestRow, model.requestTop)
	}
	if model.requestRow < model.requestTop || model.requestRow >= model.requestTop+model.requestVisibleRows() {
		t.Fatalf("selected row is outside viewport: row=%d top=%d visible=%d",
			model.requestRow, model.requestTop, model.requestVisibleRows())
	}
}
