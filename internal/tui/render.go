package tui

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"dsmon/internal/ipc"
	"dsmon/internal/proxy/trace"
	"dsmon/internal/store"
	"dsmon/internal/version"
)

type tone int

const (
	toneNormal tone = iota
	toneTitle
	toneAccent
	toneMuted
	toneOK
	toneWarn
	toneError
	toneDown
	toneUp
	toneBoxAccount
	toneBoxActivity
	toneBoxTraffic
	toneBoxRequests
	toneSelected
	toneDivLine
)

type uiLine struct {
	text       string
	tone       tone
	prefix     string
	suffix     string
	borderTone tone
	segments   []uiSegment
	raw        bool
}

type uiSegment struct {
	text  string
	tone  tone
	color string
}

type renderer struct {
	width int
	limit int
	color bool
	ascii bool
	theme palette
	lines []uiLine
}

func newRenderer(width, limit int, ascii, color bool, themes ...palette) *renderer {
	theme := fallbackPalette()
	if len(themes) > 0 {
		theme = themes[0]
	}
	return &renderer{
		width: maxInt(1, width),
		limit: maxInt(0, limit),
		color: color,
		ascii: ascii,
		theme: theme,
	}
}

func (r *renderer) add(lineTone tone, format string, args ...any) {
	if r == nil || len(r.lines) >= r.limit {
		return
	}
	text := sanitizeLine(fmt.Sprintf(format, args...))
	r.lines = append(r.lines, uiLine{text: fitText(text, r.width), tone: lineTone})
}

func (r *renderer) addFramed(prefix, suffix string, lineTone, borderTone tone, text string) {
	if r == nil || len(r.lines) >= r.limit {
		return
	}
	innerWidth := maxInt(0, r.width-lipgloss.Width(prefix)-lipgloss.Width(suffix))
	r.lines = append(r.lines, uiLine{
		text:       fitText(sanitizeLine(text), innerWidth),
		tone:       lineTone,
		prefix:     prefix,
		suffix:     suffix,
		borderTone: borderTone,
	})
}

func (r *renderer) addFramedSegments(prefix, suffix string, lineTone, borderTone tone, segments []uiSegment) {
	if r == nil || len(r.lines) >= r.limit {
		return
	}
	innerWidth := maxInt(0, r.width-lipgloss.Width(prefix)-lipgloss.Width(suffix))
	fitted := fitSegments(segments, innerWidth)
	r.lines = append(r.lines, uiLine{
		text:       segmentsText(fitted),
		tone:       lineTone,
		prefix:     prefix,
		suffix:     suffix,
		borderTone: borderTone,
		segments:   fitted,
	})
}

func (r *renderer) addRaw(text string) {
	if r == nil || len(r.lines) >= r.limit {
		return
	}
	r.lines = append(r.lines, uiLine{text: text, raw: true})
}

func (r *renderer) addSegments(lineTone tone, segments ...uiSegment) {
	if r == nil || len(r.lines) >= r.limit {
		return
	}
	fitted := fitSegments(segments, r.width)
	r.lines = append(r.lines, uiLine{text: segmentsText(fitted), tone: lineTone, segments: fitted})
}

func (r *renderer) blank() { r.add(toneNormal, "") }

func (r *renderer) remaining() int { return maxInt(0, r.limit-len(r.lines)) }

func (r *renderer) paddedLines() []uiLine {
	for len(r.lines) < r.limit {
		r.lines = append(r.lines, uiLine{text: strings.Repeat(" ", r.width)})
	}
	return r.lines
}

func (r *renderer) finish() []string {
	result := make([]string, 0, len(r.lines))
	for _, line := range r.paddedLines() {
		if line.raw {
			result = append(result, line.text)
			continue
		}
		text := renderTone(line.text, line.tone, r.color, r.theme)
		if len(line.segments) > 0 {
			text = renderSegments(line.segments, r.color, r.theme)
		}
		result = append(result, renderTone(line.prefix, line.borderTone, r.color, r.theme)+
			text+
			renderTone(line.suffix, line.borderTone, r.color, r.theme))
	}
	return result
}

func renderTone(text string, valueTone tone, color bool, theme palette) string {
	if text == "" || !color || valueTone == toneNormal {
		return text
	}
	if style, ok := theme.style(valueTone); ok {
		return style.Render(text)
	}
	return text
}

func renderSegments(segments []uiSegment, color bool, theme palette) string {
	if !color {
		return segmentsText(segments)
	}
	var builder strings.Builder
	for _, segment := range segments {
		if segment.color != "" {
			builder.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(segment.color)).Render(segment.text))
			continue
		}
		builder.WriteString(renderTone(segment.text, segment.tone, color, theme))
	}
	return builder.String()
}

func (p palette) style(valueTone tone) (lipgloss.Style, bool) {
	var color string
	switch valueTone {
	case toneTitle:
		color = p.Title
	case toneAccent:
		color = p.Accent
	case toneMuted:
		color = p.Muted
	case toneOK:
		color = p.OK
	case toneWarn:
		color = p.Warn
	case toneError:
		color = p.Error
	case toneDown:
		color = p.Down
	case toneUp:
		color = p.Up
	case toneBoxAccount:
		color = p.BoxAccount
	case toneBoxActivity:
		color = p.BoxActivity
	case toneBoxTraffic:
		color = p.BoxTraffic
	case toneBoxRequests:
		color = p.BoxRequests
	case toneSelected:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(p.SelectedFG)).
			Background(lipgloss.Color(p.SelectedBG)), true
	case toneDivLine:
		color = p.DivLine
	default:
		return lipgloss.Style{}, false
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	if valueTone == toneTitle {
		style = style.Bold(true)
	}
	return style, true
}

func fitSegments(segments []uiSegment, width int) []uiSegment {
	if width <= 0 {
		return nil
	}
	fitted := make([]uiSegment, 0, len(segments))
	used := 0
	stopped := false
	for _, segment := range segments {
		var part strings.Builder
		for _, value := range sanitizeLine(segment.text) {
			valueWidth := lipgloss.Width(string(value))
			if used+valueWidth > width {
				stopped = true
				break
			}
			part.WriteRune(value)
			used += valueWidth
		}
		if part.Len() > 0 {
			fitted = appendSegment(fitted, uiSegment{text: part.String(), tone: segment.tone, color: segment.color})
		}
		if stopped {
			break
		}
	}
	if used < width {
		fitted = appendSegment(fitted, uiSegment{text: strings.Repeat(" ", width-used), tone: toneNormal})
	}
	return fitted
}

func appendSegment(segments []uiSegment, segment uiSegment) []uiSegment {
	if segment.text == "" {
		return segments
	}
	if last := len(segments) - 1; last >= 0 && segments[last].tone == segment.tone && segments[last].color == segment.color {
		segments[last].text += segment.text
		return segments
	}
	return append(segments, segment)
}

func segmentsText(segments []uiSegment) string {
	var builder strings.Builder
	for _, segment := range segments {
		builder.WriteString(segment.text)
	}
	return builder.String()
}

func (r *renderer) panel(title string, height int, borderTone tone, body func(*renderer)) {
	if r == nil || r.remaining() <= 0 || height <= 0 {
		return
	}
	height = minInt(height, r.remaining())
	if height < 2 {
		r.add(borderTone, "%s", panelTopLine(r.width, r.ascii))
		return
	}
	left, right := panelSides(r.ascii)
	if topPrefix, topSuffix, shownTitle := panelTop(r.width, title, r.ascii); shownTitle != "" {
		r.addFramed(topPrefix, topSuffix, toneTitle, borderTone, shownTitle)
	} else {
		r.add(borderTone, "%s", panelTopLine(r.width, r.ascii))
	}
	inner := newRenderer(maxInt(0, r.width-lipgloss.Width(left)-lipgloss.Width(right)),
		height-2, r.ascii, r.color, r.theme)
	if body != nil {
		body(inner)
	}
	for _, line := range inner.paddedLines() {
		if len(line.segments) > 0 {
			r.addFramedSegments(left, right, line.tone, borderTone, line.segments)
		} else {
			r.addFramed(left, right, line.tone, borderTone, line.text)
		}
	}
	r.add(borderTone, "%s", panelBottom(r.width, r.ascii))
}

func panelSides(ascii bool) (string, string) {
	if ascii {
		return "|", "|"
	}
	return "│", "│"
}

func panelTop(width int, title string, ascii bool) (string, string, string) {
	width = maxInt(1, width)
	title = sanitizeLine(strings.TrimSpace(title))
	if width < 8 || title == "" {
		return "", "", ""
	}
	prefix, middle, corner := "╭─┐", "┌", "╮"
	line := "─"
	if ascii {
		prefix, middle, corner, line = "+-+", "+", "+", "-"
	}
	titleWidth := width - lipgloss.Width(prefix) - lipgloss.Width(middle) - lipgloss.Width(corner)
	if titleWidth <= 0 {
		return "", "", ""
	}
	title = truncateText(title, titleWidth)
	fill := width - lipgloss.Width(prefix) - lipgloss.Width(title) - lipgloss.Width(middle) - lipgloss.Width(corner)
	if title == "" || fill < 0 {
		return "", "", ""
	}
	return prefix, middle + strings.Repeat(line, fill) + corner, title
}

func panelTopLine(width int, ascii bool) string {
	width = maxInt(1, width)
	if width == 1 {
		if ascii {
			return "-"
		}
		return "─"
	}
	if ascii {
		return "+" + strings.Repeat("-", width-2) + "+"
	}
	return "╭" + strings.Repeat("─", width-2) + "╮"
}

func panelBottom(width int, ascii bool) string {
	width = maxInt(1, width)
	if width == 1 {
		if ascii {
			return "-"
		}
		return "─"
	}
	if ascii {
		return "+" + strings.Repeat("-", width-2) + "+"
	}
	return "╰" + strings.Repeat("─", width-2) + "╯"
}

func truncateText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	var builder strings.Builder
	used := 0
	for _, value := range text {
		valueWidth := lipgloss.Width(string(value))
		if used+valueWidth > width {
			break
		}
		builder.WriteRune(value)
		used += valueWidth
	}
	return builder.String()
}

func sanitizeLine(text string) string {
	return strings.Map(func(value rune) rune {
		switch {
		case value == '\n' || value == '\r' || value == '\t':
			return ' '
		case value < 0x20 || value == 0x7f:
			return ' '
		default:
			return value
		}
	}, text)
}

func fitText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	var builder strings.Builder
	used := 0
	for _, value := range text {
		valueWidth := lipgloss.Width(string(value))
		if used+valueWidth > width {
			break
		}
		builder.WriteRune(value)
		used += valueWidth
	}
	if used < width {
		builder.WriteString(strings.Repeat(" ", width-used))
	}
	return builder.String()
}

func renderModel(m *Model) string {
	if m == nil {
		return ""
	}
	width := maxInt(1, m.width)
	height := maxInt(1, m.height)
	header := m.renderHeader(width)
	bodyHeight := height - len(header)
	body := m.renderBody(width, maxInt(0, bodyHeight))
	lines := make([]string, 0, height)
	lines = append(lines, header...)
	lines = append(lines, body...)
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderHeader(width int) []string {
	stateText, stateTone := "● Online", toneOK
	if m.connectionErr != "" {
		stateText, stateTone = "● Offline", toneError
	} else if !m.stats.Health {
		stateText, stateTone = "● Error", toneError
	} else if m.stats.Balance.Error != "" {
		stateText, stateTone = "● Balance error", toneWarn
	}
	balanceText := "Balance --"
	if m.stats.Balance.Has {
		balanceText = "Balance " + formatBalanceMoney(m.stats.Balance.Total)
	}
	address := m.stats.ListenAddr
	if address == "" {
		address = "127.0.0.1:18080"
	}

	header := newRenderer(width, 2, m.ascii, m.color, m.theme)
	if width >= 80 {
		header.add(stateTone, " DS-mon %s  %s  %s  %s",
			version.Current, stateText, address, balanceText)
	} else {
		header.add(stateTone, " DS-mon %s  %s  %s",
			version.Current, stateText, balanceText)
	}
	header.addSegments(toneNormal, m.navigationSegments(width)...)
	return header.finish()
}

const navigationTextColor = "#ffffff"

func (m *Model) navigationSegments(width int) []uiSegment {
	segments := []uiSegment{{text: " ", tone: toneNormal}}
	for index, title := range pageTitles {
		if index > 0 {
			segments = append(segments, uiSegment{text: "  ", tone: toneNormal})
		}
		segments = append(segments,
			shortcutWord(title, pageShortcutKeys[index], Page(index) == m.page, m.theme.Accent)...)
	}
	leftWidth := lipgloss.Width(segmentsText(segments))

	actions := make([]uiSegment, 0, 8)
	for index, item := range []struct {
		text string
		key  byte
	}{
		{text: "settings", key: 's'},
		{text: "help", key: 'h'},
		{text: "quit", key: 'q'},
	} {
		if index > 0 {
			actions = append(actions, uiSegment{text: "  ", tone: toneNormal})
		}
		actions = append(actions, shortcutWord(item.text, item.key, false, m.theme.Accent)...)
	}
	rightWidth := lipgloss.Width(segmentsText(actions))
	if gap := width - leftWidth - rightWidth; gap >= 1 {
		segments = append(segments, uiSegment{text: strings.Repeat(" ", gap), tone: toneNormal})
		segments = append(segments, actions...)
	}
	return segments
}

func shortcutWord(text string, key byte, active bool, accent string) []uiSegment {
	index := strings.IndexByte(text, key)
	if index < 0 {
		index = 0
	}
	segments := make([]uiSegment, 0, 6)
	if active {
		segments = append(segments, uiSegment{text: "[", color: navigationTextColor})
	}
	if index > 0 {
		segments = append(segments, uiSegment{text: text[:index], color: navigationTextColor})
	}
	segments = append(segments, uiSegment{text: text[index : index+1], tone: toneAccent, color: accent})
	if index+1 < len(text) {
		segments = append(segments, uiSegment{text: text[index+1:], color: navigationTextColor})
	}
	if active {
		segments = append(segments, uiSegment{text: "]", color: navigationTextColor})
	}
	return segments
}

func (m *Model) renderBody(width, height int) []string {
	if height <= 0 {
		return nil
	}
	if m.help {
		return m.renderHelp(width, height)
	}
	switch m.page {
	case PageUsage:
		return m.renderUsage(width, height)
	case PageRequests:
		return m.renderRequests(width, height)
	case PagePricing:
		return m.renderPricing(width, height)
	default:
		return m.renderOverview(width, height)
	}
}

func (m *Model) renderOverview(width, height int) []string {
	r := newRenderer(width, height, m.ascii, m.color, m.theme)
	statusHeight := minInt(7, maxInt(6, height/6))
	if r.width >= 60 {
		// 账户占左侧 2/3，活动占右侧 1/3；流量独占下一行拿满宽度。
		activityWidth := r.width / 3
		accountWidth := r.width - 1 - activityWidth
		activityTitle := m.activityTitle(maxInt(1, activityWidth-2))
		account := m.renderPanelBlock("Account", accountWidth, statusHeight, toneBoxAccount, m.appendOverviewStatus)
		activity := m.renderPanelBlock(activityTitle, activityWidth, statusHeight, toneBoxActivity, func(panel *renderer) {
			m.appendActivityChart(panel, panel.remaining())
		})
		for row := 0; row < statusHeight; row++ {
			r.addRaw(account[row] + " " + activity[row])
		}
	} else {
		r.panel("Account", statusHeight, toneBoxAccount, m.appendOverviewStatus)
	}

	remaining := r.remaining()
	if remaining <= 0 {
		return r.finish()
	}
	recentHeight := minInt(11, maxInt(5, height/4))
	if remaining-recentHeight < 6 {
		recentHeight = 0
	}
	recentHeight = minInt(recentHeight, remaining)
	chartSpace := remaining - recentHeight

	if r.width >= 60 {
		if chartSpace > 0 {
			trafficTitle := m.trafficTitle(maxInt(1, r.width-2), chartSpace, r.width >= 80)
			r.panel(trafficTitle, chartSpace, toneBoxTraffic, func(panel *renderer) {
				m.appendTrafficChart(panel, panel.remaining())
			})
		}
	} else if chartSpace >= 6 {
		activityTitle := m.activityTitle(maxInt(1, r.width-2))
		activityHeight := chartSpace / 2
		trafficHeight := chartSpace - activityHeight
		if chartSpace >= 9 {
			trafficHeight = chartSpace * 3 / 5
			activityHeight = chartSpace - trafficHeight
			if activityHeight < 4 {
				activityHeight = 4
				trafficHeight = chartSpace - activityHeight
			}
		}
		trafficTitle := m.trafficTitle(maxInt(1, r.width-2), trafficHeight, r.width >= 80)
		r.panel(activityTitle, activityHeight, toneBoxActivity, func(panel *renderer) {
			m.appendActivityChart(panel, panel.remaining())
		})
		r.panel(trafficTitle, trafficHeight, toneBoxTraffic, func(panel *renderer) {
			m.appendTrafficChart(panel, panel.remaining())
		})
	} else if chartSpace > 0 {
		activityTitle := m.activityTitle(maxInt(1, r.width-2))
		r.panel(activityTitle, chartSpace, toneBoxActivity, func(panel *renderer) {
			m.appendActivityChart(panel, panel.remaining())
		})
	}
	if recentHeight > 0 {
		r.panel("Recent requests", recentHeight, toneBoxRequests, func(panel *renderer) {
			m.appendRecentRows(panel, panel.remaining())
		})
	}
	return r.finish()
}

func (m *Model) activityTitle(width int) string {
	bucketSeconds := float64(m.trace.ActivityBucketSecs)
	if bucketSeconds <= 0 {
		bucketSeconds = trace.DefaultActivityBucketSeconds.Seconds()
	}
	seconds := maxInt(1, int(math.Round(float64(maxInt(1, width)*2)*bucketSeconds)))
	if width < 28 {
		return fmt.Sprintf("Activity · %ds", seconds)
	}
	return fmt.Sprintf("Activity · %d sec · 3 req/cell", seconds)
}

func (m *Model) trafficTitle(width, height int, showScale bool) string {
	statsWidth := trafficStatsWidth(width, height)
	graphWidth := width
	if statsWidth > 0 {
		graphWidth = maxInt(1, width-statsWidth-1)
	}
	bucketSeconds := m.trace.TrafficBucketSecs
	if bucketSeconds <= 0 {
		bucketSeconds = trace.DefaultTrafficBucketSeconds.Seconds()
	}
	seconds := maxInt(1, int(math.Round(float64(graphWidth*2)*bucketSeconds)))
	title := fmt.Sprintf("Traffic · %d sec", seconds)
	if showScale {
		title += " · auto scale"
	}
	return title
}

func (m *Model) renderPanelBlock(title string, width, height int, borderTone tone,
	body func(*renderer)) []string {
	panel := newRenderer(width, height, m.ascii, m.color, m.theme)
	panel.panel(title, height, borderTone, body)
	return panel.finish()
}

func (m *Model) appendOverviewStatus(r *renderer) {
	balanceTone := toneTitle
	if m.stats.Balance.Has && m.stats.BalanceLowThreshold > 0 &&
		m.stats.Balance.Total <= m.stats.BalanceLowThreshold {
		balanceTone = toneWarn
	}
	r.add(balanceTone, " Balance  %s", balanceValue(m.stats.Balance))
	if r.remaining() > 0 {
		r.add(toneMuted, "%s", balanceDetails(m.stats.Balance, r.width))
	}
	if r.remaining() > 0 {
		trend := m.balanceTrendSegments(r.width)
		if len(trend) > 0 {
			r.addSegments(toneNormal, trend...)
		}
	}
	today := m.stats.Today
	unpriced := ""
	if today.UnpricedRequests > 0 {
		unpriced = fmt.Sprintf(" · %d unpriced", today.UnpricedRequests)
	}
	if r.remaining() > 0 {
		r.add(toneAccent, " Today  %s requests · %s tokens · %.1f%% hit · %s spent%s",
			formatCount(today.Requests), formatCount(today.TotalTokens), today.CacheHitRate,
			formatMoney(today.Cost), unpriced)
	}
	if status := m.statusText(); status != "" && r.remaining() > 0 {
		r.add(toneError, " ! %s", status)
	}
}

func balanceValue(balance ipc.Balance) string {
	if !balance.Has {
		return "--"
	}
	return formatBalanceMoney(balance.Total)
}

func balanceDetails(balance ipc.Balance, width int) string {
	if balance.Error != "" {
		return " Query failed: " + balance.Error
	}
	if !balance.Has {
		return " Waiting for first balance query"
	}
	text := fmt.Sprintf(" Topped up %s · Granted %s", formatBalanceMoney(balance.ToppedUp), formatBalanceMoney(balance.Granted))
	if balance.LastSuccessUnix > 0 {
		text += " · Updated " + time.Unix(balance.LastSuccessUnix, 0).Format("15:04:05")
	}
	if balance.Failures > 0 {
		text += fmt.Sprintf(" · %d consecutive failures", balance.Failures)
	}
	if width < 80 {
		return fmt.Sprintf(" Topped up %s · Granted %s", formatBalanceMoney(balance.ToppedUp), formatBalanceMoney(balance.Granted))
	}
	return text
}

func (m *Model) balanceTrendLine(width int) string {
	return segmentsText(m.balanceTrendSegments(width))
}

func (m *Model) balanceTrendSegments(width int) []uiSegment {
	points24, points7 := m.stats.Balance.Trend24h, m.stats.Balance.Trend7d
	if len(points24) == 0 && len(points7) == 0 {
		return []uiSegment{{text: " Balance  24h --  7d --", tone: toneNormal}}
	}
	label24 := balanceTrendLabel("24h", points24, 24*time.Hour)
	label7 := balanceTrendLabel("7d", points7, 7*24*time.Hour)
	range24, range7 := balanceRangeLabel(points24), balanceRangeLabel(points7)
	showRanges := width >= 64
	showAxis := width >= 56
	series := 1
	if len(points7) > 0 {
		series = 2
	}
	fixedWidth := lipgloss.Width(" Balance  " + label24 + " ")
	if series == 2 {
		fixedWidth += lipgloss.Width("  " + label7 + " ")
	}
	if showRanges {
		fixedWidth += lipgloss.Width(range24) + 1
		if series == 2 {
			fixedWidth += lipgloss.Width(range7) + 1
		}
	}
	if showAxis {
		fixedWidth += lipgloss.Width("  Time→")
	}
	sparkWidth := minInt(24, maxInt(1, (width-fixedWidth)/series))

	segments := []uiSegment{{text: " Balance  " + label24 + " ", tone: toneNormal}}
	if showRanges && range24 != "" {
		segments = append(segments, uiSegment{text: range24 + " ", tone: toneMuted})
	}
	segments = append(segments, m.balanceSparklineSegments(points24, sparkWidth, !m.ascii, 24*time.Hour)...)
	if len(points7) > 0 {
		segments = append(segments, uiSegment{text: "  " + label7 + " ", tone: toneNormal})
		if showRanges && range7 != "" {
			segments = append(segments, uiSegment{text: range7 + " ", tone: toneMuted})
		}
		segments = append(segments, m.balanceSparklineSegments(points7, sparkWidth, !m.ascii,
			7*24*time.Hour)...)
	}
	if showAxis {
		segments = append(segments, uiSegment{text: "  Time→", tone: toneMuted})
	}
	return segments
}

func balanceTrendLabel(name string, points []ipc.BalancePoint, window time.Duration) string {
	coverage := balanceCoverage(points)
	if coverage <= 0 || window <= 0 || coverage >= window*3/4 {
		return name
	}
	return name + "·" + compactBalanceCoverage(coverage)
}

func balanceCoverage(points []ipc.BalancePoint) time.Duration {
	if len(points) == 0 {
		return 0
	}
	interval := int64(0)
	if len(points) > 1 && points[1].Unix > points[0].Unix {
		interval = points[1].Unix - points[0].Unix
	}
	first, last := int64(0), int64(0)
	haveValue := false
	for _, point := range points {
		if point.Missing {
			continue
		}
		if !haveValue {
			first = point.Unix
			haveValue = true
		}
		last = point.Unix
	}
	if !haveValue {
		return 0
	}
	return time.Duration(last-first+interval) * time.Second
}

func compactBalanceCoverage(coverage time.Duration) string {
	if coverage <= 0 {
		return "--"
	}
	if coverage < time.Hour {
		return fmt.Sprintf("%dm", int(math.Round(coverage.Minutes())))
	}
	hours := coverage.Hours()
	if hours < 10 {
		value := fmt.Sprintf("%.1f", hours)
		value = strings.TrimSuffix(value, ".0")
		return value + "h"
	}
	if hours < 48 {
		return fmt.Sprintf("%.0fh", hours)
	}
	days := hours / 24
	value := fmt.Sprintf("%.0f", days)
	if days < 10 {
		value = strings.TrimSuffix(fmt.Sprintf("%.1f", days), ".0")
	}
	return value + "d"
}

func balanceRangeLabel(points []ipc.BalancePoint) string {
	minimum, maximum := 0.0, 0.0
	haveValue := false
	for _, point := range points {
		if point.Missing {
			continue
		}
		if !haveValue {
			minimum, maximum = point.Value, point.Value
			haveValue = true
			continue
		}
		minimum = math.Min(minimum, point.Value)
		maximum = math.Max(maximum, point.Value)
	}
	if !haveValue {
		return ""
	}
	return compactBalanceValue(minimum) + "–" + compactBalanceValue(maximum)
}

func compactBalanceValue(value float64) string {
	switch {
	case math.Abs(value) >= 1000:
		return fmt.Sprintf("%.0f", value)
	case math.Abs(value) >= 100:
		return fmt.Sprintf("%.1f", value)
	default:
		return fmt.Sprintf("%.2f", value)
	}
}

func balanceSparkline(points []ipc.BalancePoint, width int, braille bool) string {
	return segmentsText(balanceSparklineSegments(points, width, braille))
}

func balanceSparklineSegments(points []ipc.BalancePoint, width int, braille bool) []uiSegment {
	return balanceSparklineSegmentsWithPalette(points, width, braille, fallbackPalette())
}

func (m *Model) balanceSparklineSegments(points []ipc.BalancePoint, width int, braille bool,
	windows ...time.Duration) []uiSegment {
	return balanceSparklineSegmentsWithPalette(points, width, braille, m.theme, windows...)
}

func balanceSparklineSegmentsWithPalette(points []ipc.BalancePoint, width int,
	braille bool, theme palette, windows ...time.Duration) []uiSegment {
	if len(points) == 0 || width <= 0 {
		return []uiSegment{{text: "--", tone: toneNormal}}
	}
	if len(windows) > 0 && windows[0] > 0 {
		coverage := balanceCoverage(points)
		if coverage > 0 && coverage < windows[0]*3/4 {
			points = compactBalancePoints(points)
		}
	}
	sampleWidth := width
	if braille {
		sampleWidth *= 2
	}
	values := make([]float64, sampleWidth)
	valid := make([]bool, sampleWidth)
	haveValue := false
	for column := 0; column < sampleWidth; column++ {
		index := int(float64(column) / float64(sampleWidth) * float64(len(points)))
		if index >= len(points) {
			index = len(points) - 1
		}
		if points[index].Missing {
			continue
		}
		values[column] = points[index].Value
		valid[column] = true
		haveValue = true
	}
	if !haveValue {
		return []uiSegment{{text: "--", tone: toneNormal}}
	}
	if braille {
		return brailleBalanceSparklineSegments(values, valid, width, theme)
	}
	return asciiBalanceSparklineSegments(values, valid)
}

func compactBalancePoints(points []ipc.BalancePoint) []ipc.BalancePoint {
	result := make([]ipc.BalancePoint, 0, len(points))
	for _, point := range points {
		if !point.Missing {
			result = append(result, point)
		}
	}
	return result
}

func brailleBalanceSparklineSegments(values []float64, valid []bool, width int, theme palette) []uiSegment {
	if len(values) == 0 || width <= 0 {
		return []uiSegment{{text: "--", tone: toneNormal}}
	}
	minimum, maximum := 0.0, 0.0
	haveValue := false
	for index, value := range values {
		if !valid[index] {
			continue
		}
		if !haveValue {
			minimum, maximum = value, value
			haveValue = true
			continue
		}
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	canvas := newBrailleCanvas(width, 1)
	ratios := make([]float64, width)
	dotExists := make([]bool, width)
	for x := 0; x < width*2; x++ {
		if x >= len(values) || !valid[x] {
			continue
		}
		ratio := 0.5
		level := 2
		if maximum > minimum {
			ratio = math.Min(1, math.Max(0, (values[x]-minimum)/(maximum-minimum)))
			level = 3 - int(math.Round(ratio*3))
		}
		if !dotExists[x/2] || ratio > ratios[x/2] {
			ratios[x/2] = ratio
			dotExists[x/2] = true
		}
		for y := level; y < 4; y++ {
			canvas.dot(x, y)
		}
	}
	runes := []rune(canvas.rows()[0])
	segments := make([]uiSegment, 0, width)
	for index := 0; index < len(runes); index++ {
		if !dotExists[index] {
			segments = append(segments, uiSegment{text: " ", tone: toneNormal})
			continue
		}
		segments = append(segments, uiSegment{
			text:  string(runes[index]),
			tone:  toneNormal,
			color: theme.trendGradientColor(ratios[index]),
		})
	}
	return segments
}

func asciiBalanceSparklineSegments(values []float64, valid []bool) []uiSegment {
	if len(values) == 0 {
		return []uiSegment{{text: "--", tone: toneNormal}}
	}
	minimum, maximum := 0.0, 0.0
	haveValue := false
	for index, value := range values {
		if !valid[index] {
			continue
		}
		if !haveValue {
			minimum, maximum = value, value
			haveValue = true
			continue
		}
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	if !haveValue {
		return []uiSegment{{text: strings.Repeat(" ", len(values)), tone: toneNormal}}
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var builder strings.Builder
	for index, value := range values {
		if !valid[index] {
			builder.WriteByte(' ')
			continue
		}
		block := len(blocks) / 2
		if maximum > minimum {
			block = int(math.Round((value - minimum) / (maximum - minimum) *
				float64(len(blocks)-1)))
		}
		builder.WriteRune(blocks[block])
	}
	return []uiSegment{{text: builder.String(), tone: toneNormal}}
}

func trendGradientColor(ratio float64) string {
	return fallbackPalette().trendGradientColor(ratio)
}

func (p palette) trendGradientColor(ratio float64) string {
	ratio = math.Min(1, math.Max(0, ratio))
	if ratio < 0.5 {
		return blendHexColor(p.TempEnd, p.TempMid, ratio*2)
	}
	return blendHexColor(p.TempMid, p.TempStart, (ratio-0.5)*2)
}

func blendHexColor(from, to string, ratio float64) string {
	ratio = math.Min(1, math.Max(0, ratio))
	fromR, fromG, fromB := parseHexColor(from)
	toR, toG, toB := parseHexColor(to)
	return fmt.Sprintf("#%02x%02x%02x",
		int(math.Round(float64(fromR)+(float64(toR-fromR)*ratio))),
		int(math.Round(float64(fromG)+(float64(toG-fromG)*ratio))),
		int(math.Round(float64(fromB)+(float64(toB-fromB)*ratio))))
}

func parseHexColor(value string) (int, int, int) {
	parsed, err := strconv.ParseUint(strings.TrimPrefix(value, "#"), 16, 32)
	if err != nil {
		return 0, 0, 0
	}
	return int(parsed>>16) & 0xff, int(parsed>>8) & 0xff, int(parsed) & 0xff
}

func usageBarColor(level float64) string {
	return fallbackPalette().usageBarColor(level)
}

func (p palette) usageBarColor(level float64) string {
	level = math.Min(1, math.Max(0, level))
	if level < 0.5 {
		return blendHexColor(p.TempStart, p.TempMid, level*2)
	}
	return blendHexColor(p.TempMid, p.TempEnd, (level-0.5)*2)
}

func usageBarGlyph(index int, ascii bool) string {
	if ascii {
		runes := []rune("#*+=-~.:")
		return string(runes[index%len(runes)])
	}
	runes := []rune("⣿⠿⡿⢿⣷⣾⣻⢾")
	return string(runes[index%len(runes)])
}

func (m *Model) appendActivityChart(r *renderer, height int) {
	chartWidth := maxInt(1, r.width-2)
	if height <= 0 {
		return
	}
	if m.ascii {
		for row := 0; row < height-1; row++ {
			r.blank()
		}
		r.add(toneOK, "  %s", RenderActivityBlocks(m.trace.Activity, chartWidth))
		return
	}
	rows := RenderActivity(m.trace.Activity, chartWidth, height, traceNow(m.trace), m.trace.ActivityFullScale)
	if !m.trace.Health {
		// 异常时保留整块红色覆盖，不把故障画成正常的绿→红渐变。
		for _, row := range rows {
			r.add(toneError, "  %s", row)
		}
		return
	}
	for rowIndex, row := range rows {
		color := m.theme.activityRowColor(rowIndex, height)
		r.addSegments(toneNormal, uiSegment{
			text: "  " + row, tone: toneNormal, color: color,
		})
	}
}

func (p palette) activityRowColor(row, height int) string {
	ratio := 0.0
	if height > 1 {
		ratio = 1 - float64(row)/float64(height-1)
	}
	return p.usageBarColor(ratio)
}

func (m *Model) appendTrafficChart(r *renderer, laneHeight int) {
	if laneHeight <= 0 {
		return
	}
	chartWidth := maxInt(1, r.width-2)
	frame := traceFrame(m.trace)
	if m.ascii {
		downHeight := (laneHeight + 1) / 2
		m.appendTrafficSparkline(r, frame, false, chartWidth, downHeight)
		m.appendTrafficSparkline(r, frame, true, chartWidth, laneHeight-downHeight)
		return
	}
	statsWidth := trafficStatsWidth(r.width, laneHeight)
	graphWidth := r.width
	if statsWidth > 0 {
		graphWidth -= statsWidth + 1
	}
	graphWidth = maxInt(1, graphWidth)
	frame = traceFrameForWidth(m.trace, float64(graphWidth*2))
	rows := RenderTrafficNet(frame, graphWidth, laneHeight)
	stats := trafficNetStats(frame, statsWidth, laneHeight)
	for row := 0; row < laneHeight; row++ {
		graphRow := rows[row]
		color := m.theme.trafficNetRowColor(row, laneHeight, false)
		segments := make([]uiSegment, 0, 4)
		if row == 0 || row == laneHeight-1 {
			scale := trace.FormatNetScale(frame.DownScale)
			if row == laneHeight-1 {
				scale = trace.FormatNetScale(frame.UpScale)
			}
			label, rest := splitTrafficPrefix(graphRow, scale)
			segments = append(segments, uiSegment{text: label, tone: toneMuted})
			segments = append(segments, uiSegment{text: rest, tone: toneNormal, color: color})
		} else {
			segments = append(segments, uiSegment{text: graphRow, tone: toneNormal, color: color})
		}
		if statsWidth > 0 {
			segments = append(segments, uiSegment{text: " ", tone: toneNormal})
			segments = append(segments, trafficStatsSegments(stats[row])...)
		}
		r.addSegments(toneNormal, segments...)
	}
}

func trafficStatsWidth(width, height int) int {
	if height < 4 || width < 36 {
		return 0
	}
	// btop Net 的统计框宽 27；窗口更窄时退到 19，并隐藏 bit 速率。
	if width >= 43 {
		return 27
	}
	return 19
}

func trafficNetRowColor(row, height int, ascii bool) string {
	return fallbackPalette().trafficNetRowColor(row, height, ascii)
}

func (p palette) trafficNetRowColor(row, height int, ascii bool) string {
	if ascii || height <= 0 {
		return ""
	}
	downCells := (height + 1) / 2
	upCells := height - downCells
	if row < downCells {
		ratio := 1.0
		if downCells > 1 {
			ratio = 1 - float64(row)/float64(downCells-1)
		}
		return p.trafficGradientColor(ratio, false)
	}
	ratio := 1.0
	if upCells > 1 {
		ratio = float64(row-downCells) / float64(upCells-1)
	}
	return p.trafficGradientColor(ratio, true)
}

func trafficGradientColor(ratio float64, upload bool) string {
	return fallbackPalette().trafficGradientColor(ratio, upload)
}

func (p palette) trafficGradientColor(ratio float64, upload bool) string {
	ratio = math.Min(1, math.Max(0, ratio))
	start, middle, end := p.DownloadStart, p.DownloadMid, p.DownloadEnd
	if upload {
		start, middle, end = p.UploadStart, p.UploadMid, p.UploadEnd
	}
	if ratio < 0.5 {
		return blendHexColor(start, middle, ratio*2)
	}
	return blendHexColor(middle, end, (ratio-0.5)*2)
}

func splitTrafficPrefix(row, prefix string) (string, string) {
	rowRunes := []rune(row)
	prefixRunes := []rune(prefix)
	if len(prefixRunes) > len(rowRunes) {
		prefixRunes = prefixRunes[:len(rowRunes)]
	}
	copy(rowRunes, prefixRunes)
	return string(rowRunes[:len(prefixRunes)]), string(rowRunes[len(prefixRunes):])
}

// trafficNetStats 复刻 btop Net 的右侧统计框：单个居中框，上标题 download、
// 下标题 upload，下载统计贴着上边框、上传统计贴着下边框。
func trafficNetStats(frame trace.Frame, width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	rows := make([]string, height)
	for row := range rows {
		rows[row] = strings.Repeat(" ", width)
	}
	boxHeight := minInt(9, height)
	if boxHeight < 2 {
		return rows
	}
	start := height/2 - boxHeight/2
	if start < 0 {
		start = 0
	}
	if start+boxHeight > height {
		start = height - boxHeight
	}
	box := trafficStatsBox(frame, width, boxHeight)
	copy(rows[start:start+boxHeight], box)
	return rows
}

func trafficStatsBox(frame trace.Frame, width, height int) []string {
	rows := make([]string, height)
	inner := maxInt(1, width-2)
	for row := range rows {
		rows[row] = trafficStatsSide(false) + strings.Repeat(" ", inner) + trafficStatsSide(true)
	}
	if height < 2 {
		return rows
	}
	rows[0] = trafficStatsTop(width, "download", false)
	rows[height-1] = trafficStatsBottom(width, "upload", false)
	wide := width >= 20
	set := func(row int, text string) {
		if row <= 0 || row >= height-1 {
			return
		}
		rows[row] = trafficStatsSide(false) + trafficStatPair(text, "", inner) + trafficStatsSide(true)
	}
	set(1, trafficNetSpeedLine(frame, false, inner, wide))
	if height >= 8 {
		set(2, trafficNetTopLine(frame, false, inner, wide))
	}
	if height >= 6 {
		totalRow := 2
		if height >= 8 {
			totalRow = 3
		}
		set(totalRow, trafficNetTotalLine(frame, false, inner, wide))
	}
	upSpeedRow := height - height/2
	set(upSpeedRow, trafficNetSpeedLine(frame, true, inner, wide))
	if height >= 8 {
		set(upSpeedRow+1, trafficNetTopLine(frame, true, inner, wide))
	}
	if height >= 6 {
		totalRow := upSpeedRow + 1
		if height >= 8 {
			totalRow++
		}
		set(totalRow, trafficNetTotalLine(frame, true, inner, wide))
	}
	return rows
}

func trafficNetSpeedLine(frame trace.Frame, upload bool, inner int, wide bool) string {
	arrow, rate := "▼", frame.DownRate
	if upload {
		arrow, rate = "▲", frame.UpRate
	}
	right := ""
	if wide {
		right = "(" + trace.FormatNetSpeed(rate, true) + ")"
	}
	return trafficStatPair(arrow+" "+trace.FormatNetSpeed(rate, false), right, inner)
}

func trafficNetTopLine(frame trace.Frame, upload bool, inner int, wide bool) string {
	arrow, top := "▼", frame.DownTop
	if upload {
		arrow, top = "▲", frame.UpTop
	}
	field := 9
	if wide {
		field = 17
	}
	return trafficStatPair(arrow+" Top:", padLeftText("("+trace.FormatNetSpeed(top, true)+")", field), inner)
}

func trafficNetTotalLine(frame trace.Frame, upload bool, inner int, wide bool) string {
	arrow, total := "▼", frame.DownTotal
	if upload {
		arrow, total = "▲", frame.UpTotal
	}
	field := 8
	if wide {
		field = 16
	}
	return trafficStatPair(arrow+" Total:", padLeftText(trace.FormatNetTotal(float64(total)), field), inner)
}

func trafficStatPair(left, right string, width int) string {
	if width <= 0 {
		return ""
	}
	left = truncateText(left, width)
	right = truncateText(right, width)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return padText(left+right, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func padLeftText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = sanitizeLine(text)
	if lipgloss.Width(text) >= width {
		return text
	}
	return strings.Repeat(" ", width-lipgloss.Width(text)) + text
}

func trafficStatsTop(width int, title string, ascii bool) string {
	return trafficStatsBorder(width, title, ascii, false)
}

func trafficStatsBottom(width int, title string, ascii bool) string {
	return trafficStatsBorder(width, title, ascii, true)
}

func trafficStatsBorder(width int, title string, ascii, bottom bool) string {
	prefix, joint, corner, line := "╭─┐", "┌", "╮", "─"
	if bottom {
		prefix, joint, corner = "╰─┘", "└", "╯"
	}
	if ascii {
		prefix, joint, corner, line = "+-+", "+", "+", "-"
	}
	title = sanitizeLine(title)
	fill := width - lipgloss.Width(prefix) - lipgloss.Width(title) - lipgloss.Width(joint) - lipgloss.Width(corner)
	if fill < 0 {
		return fitText(prefix+title+joint+corner, width)
	}
	return prefix + title + joint + strings.Repeat(line, fill) + corner
}

func trafficStatsSide(right bool) string {
	if right {
		return "│"
	}
	return "│"
}

// trafficStatsSegments 把 btop 统计子框拆成“灰色边框 + 正常内容”。
// 终端里线条粗细由字符决定，无法真的画细；用主题的 div_line 灰色让边框后退，
// 视觉上就会接近 btop 的细灰线。
func trafficStatsSegments(row string) []uiSegment {
	if row == "" {
		return nil
	}
	for _, title := range []string{"download", "upload"} {
		index := strings.Index(row, title)
		if index < 0 {
			continue
		}
		segments := make([]uiSegment, 0, 3)
		if prefix := row[:index]; prefix != "" {
			segments = append(segments, uiSegment{text: prefix, tone: toneDivLine})
		}
		segments = append(segments, uiSegment{text: title, tone: toneTitle})
		if suffix := row[index+len(title):]; suffix != "" {
			segments = append(segments, uiSegment{text: suffix, tone: toneDivLine})
		}
		return segments
	}
	runes := []rune(row)
	if len(runes) == 1 {
		return []uiSegment{{text: row, tone: toneDivLine}}
	}
	return []uiSegment{
		{text: string(runes[0]), tone: toneDivLine},
		{text: string(runes[1 : len(runes)-1]), tone: toneNormal},
		{text: string(runes[len(runes)-1]), tone: toneDivLine},
	}
}

func (m *Model) appendTrafficRows(r *renderer, rows []string, lineTone tone, label string) {
	for index, row := range rows {
		if index == 0 {
			row = overlayTrafficLabel(row, label)
		}
		r.add(lineTone, "  %s", row)
	}
}

func (m *Model) appendTrafficSparkline(r *renderer, frame trace.Frame, upload bool, width, height int) {
	if height <= 0 {
		return
	}
	lineTone := toneDown
	if upload {
		lineTone = toneUp
	}
	for row := 0; row < height-1; row++ {
		r.blank()
	}
	row := overlayTrafficLabel(trafficSparkline(frame, upload, width), trafficLaneLabel(frame, upload))
	r.add(lineTone, "  %s", row)
}

func trafficLaneLabel(frame trace.Frame, upload bool) string {
	if upload {
		return "↑ " + trace.FormatRate(frame.UpRate)
	}
	return "↓ " + trace.FormatRate(frame.DownRate)
}

func overlayTrafficLabel(row, label string) string {
	rowRunes := []rune(row)
	labelRunes := []rune(label)
	if len(labelRunes) > len(rowRunes) {
		labelRunes = labelRunes[:len(rowRunes)]
	}
	copy(rowRunes, labelRunes)
	return string(rowRunes)
}

func trafficSparkline(frame trace.Frame, upload bool, width int) string {
	if width <= 0 || len(frame.Buckets) == 0 {
		return strings.Repeat(" ", maxInt(0, width))
	}
	values := make([]float64, 0, width)
	allZero := true
	for column := 0; column < width; column++ {
		position := float64(column) / float64(maxInt(1, width-1)) * float64(len(frame.Buckets)-1)
		index := int(math.Round(position))
		if index >= len(frame.Buckets) {
			index = len(frame.Buckets) - 1
		}
		value := frame.Buckets[index].Download
		if upload {
			value = frame.Buckets[index].Upload
		}
		if value > 0 {
			allZero = false
		}
		values = append(values, float64(value))
	}
	if allZero {
		return strings.Repeat(" ", width)
	}
	return sparkline(values)
}

func (m *Model) appendRecentRows(r *renderer, limit int) {
	if limit <= 0 {
		return
	}
	columns := requestColumns(r.width)
	r.add(toneMuted, " %s", formatColumns(columnTitles(columns), columns))
	if len(m.stats.Recent) == 0 {
		r.add(toneMuted, " No requests yet")
		return
	}
	for index := 0; index < len(m.stats.Recent) && index < limit; index++ {
		cells := requestCells(m.stats.Recent[index], false, columns)
		r.add(toneNormal, " %s", formatColumns(cells, columns))
	}
}

type column struct {
	title string
	width int
}

func requestColumns(width int) []column {
	var columns []column
	switch {
	case width >= 120:
		columns = []column{{"", 1}, {"Time", 11}, {"Model", 24}, {"Client", 15}, {"tokens", 9}, {"Status", 6}, {"Latency", 8}, {"Cost", 9}}
	case width >= 80:
		columns = []column{{"", 1}, {"Time", 11}, {"Model", 18}, {"Client", 11}, {"tokens", 8}, {"Status", 6}, {"Cost", 9}}
	case width >= 40:
		columns = []column{{"", 1}, {"Time", 8}, {"Model", 7}, {"tokens", 6}, {"Status", 6}, {"Cost", 9}}
	default:
		columns = []column{{"", 1}, {"Time", 7}, {"Model", 5}, {"tokens", 5}, {"Status", 6}}
	}
	used := 1
	for _, item := range columns {
		used += item.width
	}
	used += len(columns) - 1
	extra := width - used
	if extra > 0 {
		columns[2].width += extra
	}
	if extra < 0 {
		need := -extra
		for index := len(columns) - 1; index >= 2 && need > 0; index-- {
			shrink := minInt(need, maxInt(0, columns[index].width-3))
			columns[index].width -= shrink
			need -= shrink
		}
		if need > 0 {
			columns[2].width = maxInt(3, columns[2].width-need)
		}
	}
	return columns
}

func columnTitles(columns []column) []string {
	result := make([]string, len(columns))
	for index, item := range columns {
		result[index] = item.title
	}
	return result
}

func displayClientName(name string) string {
	switch strings.TrimSpace(name) {
	case "", store.ClientUnknown:
		return "Unknown"
	case store.ClientBrowser:
		return "Browser"
	case "Codex Desktop":
		return "codex"
	default:
		return name
	}
}

func displaySeriesName(name string) string {
	switch strings.TrimSpace(name) {
	case store.ClientUnknown:
		return "Unknown"
	case store.ClientBrowser:
		return "Browser"
	case "Codex Desktop":
		return "codex"
	default:
		return name
	}
}

func requestCells(row ipc.RequestRow, selected bool, columns []column) []string {
	marker := " "
	if selected {
		marker = ">"
	}
	at := time.Unix(row.TimestampUnix, 0).Format("01-02 15:04")
	cost := formatMoney(row.Cost)
	if len(columns) >= 8 {
		return []string{marker, at, defaultValue(row.Model, "Unknown"), displayClientName(row.Client),
			formatCount(row.TotalTokens), fmt.Sprintf("%d", row.StatusCode), fmt.Sprintf("%.0fms", row.LatencyMS), cost}
	}
	if len(columns) >= 7 {
		return []string{marker, at, defaultValue(row.Model, "Unknown"), displayClientName(row.Client),
			formatCount(row.TotalTokens), fmt.Sprintf("%d", row.StatusCode), cost}
	}
	if len(columns) == 6 {
		return []string{marker, at, defaultValue(row.Model, "Unknown"),
			formatCount(row.TotalTokens), fmt.Sprintf("%d", row.StatusCode), cost}
	}
	return []string{marker, at, defaultValue(row.Model, "Unknown"),
		formatCount(row.TotalTokens), fmt.Sprintf("%d", row.StatusCode)}
}

func formatColumns(cells []string, columns []column) string {
	parts := make([]string, 0, len(columns))
	for index := range columns {
		value := ""
		if index < len(cells) {
			value = cells[index]
		}
		parts = append(parts, padText(value, columns[index].width))
	}
	return strings.Join(parts, " ")
}

func padText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = sanitizeLine(text)
	if lipgloss.Width(text) > width {
		return fitText(text, width)
	}
	return text + strings.Repeat(" ", width-lipgloss.Width(text))
}

func (m *Model) renderUsage(width, height int) []string {
	r := newRenderer(width, height, m.ascii, m.color, m.theme)
	rangeText, bucketText := usageRangeText(m.series)
	controlLines := 2
	if m.editingDates {
		controlLines += 4
		if width-2 < 60 {
			controlLines++
		}
	}
	controlHeight := minInt(controlLines+2, height)
	r.panel("Query range", controlHeight, toneBoxActivity, func(panel *renderer) {
		panel.addSegments(toneNormal, periodSelectorSegments(m.period, m.theme.Accent)...)
		dimension := dimensionSelectorSegments(m.dimension, m.theme.Accent)
		dimension = append(dimension,
			uiSegment{text: "  ·  " + rangeText + "  ·  " + bucketText + " buckets", tone: toneMuted})
		panel.addSegments(toneNormal, dimension...)
		if m.editingDates {
			m.appendDateEditor(panel)
		}
	})

	seriesTotals, pointsByBucket, bucketCount := usageChartData(m.series)
	if len(m.series.Points) == 0 {
		if r.remaining() > 0 {
			r.panel("Usage trend · "+m.periodLabel(), r.remaining(), toneBoxAccount, func(panel *renderer) {
				panel.add(toneMuted, " No usage yet")
			})
		}
		return r.finish()
	}

	remaining := r.remaining()
	if remaining <= 0 {
		return r.finish()
	}
	tableBody := minInt(len(seriesTotals)+1, maxInt(2, remaining/3))
	tableHeight := tableBody + 2
	if tableHeight > remaining {
		tableHeight = remaining
	}
	chartHeight := remaining - tableHeight
	if chartHeight < 3 {
		chartHeight = remaining
		tableHeight = 0
	}
	if chartHeight > 0 {
		r.panel(usageChartTitle(m.periodLabel(), width), chartHeight, toneBoxAccount,
			func(panel *renderer) {
				panel.add(toneAccent, "%s", usageSummary(m.series.Points))
				if panel.remaining() <= 0 {
					return
				}
				chartRows := panel.remaining()
				if chartRows > 1 {
					chartRows--
				}
				m.appendUsageChart(panel, seriesTotals, pointsByBucket, bucketCount, chartRows)
				if panel.remaining() > 0 {
					m.appendUsageLegend(panel, seriesTotals)
				}
			})
	}
	if tableHeight > 0 {
		r.panel("Series details", tableHeight, toneBoxRequests, func(panel *renderer) {
			m.appendUsageTable(panel, seriesTotals, panel.remaining())
		})
	}
	return r.finish()
}

type usageTotal struct {
	Name       string
	Requests   int
	HitTokens  int
	MissTokens int
	OutTokens  int
	Cost       float64
}

func (u usageTotal) tokens() int { return u.HitTokens + u.MissTokens + u.OutTokens }

func (p palette) usageSeriesColors() []string {
	candidates := []string{
		p.Accent, p.BoxAccount, p.BoxActivity, p.Up, p.Warn, p.Error, p.DivLine,
	}
	seen := make(map[string]bool, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, color := range candidates {
		color = strings.ToLower(strings.TrimSpace(color))
		if color == "" || seen[color] {
			continue
		}
		seen[color] = true
		result = append(result, color)
	}
	if len(result) == 0 {
		return []string{navigationTextColor}
	}
	return result
}

func usageSeriesColorMap(totals []usageTotal, theme palette) map[string]string {
	names := make([]string, 0, len(totals))
	for _, item := range totals {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	colors := theme.usageSeriesColors()
	result := make(map[string]string, len(names))
	for index, name := range names {
		result[name] = colors[index%len(colors)]
	}
	return result
}

func usageChartData(series ipc.Series) ([]usageTotal, [][]usageTotal, int) {
	if len(series.Points) == 0 {
		return nil, nil, 0
	}
	totals := map[string]*usageTotal{}
	from := time.Unix(series.FromUnix, 0)
	to := time.Unix(series.ToUnix, 0)
	step := time.Hour
	if series.Bucket == string(store.BucketDay) {
		step = 24 * time.Hour
	}
	bucketCount := int(math.Ceil(to.Sub(from).Seconds() / step.Seconds()))
	if bucketCount < 1 {
		bucketCount = 1
	}
	if bucketCount > 1000 {
		bucketCount = 1000
	}
	buckets := make([][]usageTotal, bucketCount)
	for _, point := range series.Points {
		index := int(point.StartUnix - series.FromUnix)
		if series.Bucket == string(store.BucketDay) {
			index /= int(step.Seconds())
		} else {
			index /= int(step.Seconds())
		}
		if index < 0 || index >= bucketCount {
			continue
		}
		buckets[index] = append(buckets[index], usageTotal{
			Name: point.Series, Requests: point.Requests, HitTokens: point.HitTokens,
			MissTokens: point.MissTokens, OutTokens: point.OutTokens, Cost: point.Cost,
		})
		item := totals[point.Series]
		if item == nil {
			item = &usageTotal{Name: point.Series}
			totals[point.Series] = item
		}
		item.Requests += point.Requests
		item.HitTokens += point.HitTokens
		item.MissTokens += point.MissTokens
		item.OutTokens += point.OutTokens
		item.Cost += point.Cost
	}
	list := make([]usageTotal, 0, len(totals))
	for _, item := range totals {
		list = append(list, *item)
	}
	sort.Slice(list, func(first, second int) bool {
		if list[first].tokens() == list[second].tokens() {
			return list[first].Name < list[second].Name
		}
		return list[first].tokens() > list[second].tokens()
	})
	return list, buckets, bucketCount
}

func usageChartColumns(buckets [][]usageTotal, bucketCount, width int) ([][]usageTotal, []int, int) {
	width = maxInt(1, width)
	columns := make([][]usageTotal, width)
	totals := make([]int, width)
	maximum := 0
	for column := 0; column < width; column++ {
		start, end := usageBucketRange(bucketCount, width, column)
		for _, bucket := range buckets[start:end] {
			columns[column] = append(columns[column], bucket...)
		}
		for _, item := range columns[column] {
			totals[column] += item.tokens()
		}
		maximum = maxInt(maximum, totals[column])
	}
	return columns, totals, maximum
}

func usageBucketRange(bucketCount, columns, column int) (int, int) {
	if bucketCount <= 0 {
		return 0, 0
	}
	start := column * bucketCount / columns
	end := (column + 1) * bucketCount / columns
	if end <= start {
		end = start + 1
	}
	if start >= bucketCount {
		start = bucketCount - 1
	}
	if end > bucketCount {
		end = bucketCount
	}
	return start, end
}

func usageChartTitle(periodLabel string, width int) string {
	switch {
	case width >= 72:
		return fmt.Sprintf("Usage trend · tokens · %s", periodLabel)
	case width >= 52:
		return fmt.Sprintf("Usage trend · tokens · %s", periodLabel)
	default:
		return fmt.Sprintf("Usage trend · %s", periodLabel)
	}
}

func compactTokenCount(value int) string {
	switch {
	case value >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(value)/1_000_000_000)
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fK", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

func usageChartGeometry(buckets [][]usageTotal, bucketCount, width int) ([][]usageTotal, []int, int, int) {
	labelWidth := 4
	plotWidth := maxInt(1, width-labelWidth-2)
	var columns [][]usageTotal
	var columnTotals []int
	var maxBucket int
	for attempt := 0; attempt < 3; attempt++ {
		columns, columnTotals, maxBucket = usageChartColumns(buckets, bucketCount, plotWidth)
		nextLabelWidth := usageYAxisLabelWidth(maxBucket)
		nextPlotWidth := maxInt(1, width-nextLabelWidth-2)
		if nextLabelWidth == labelWidth && nextPlotWidth == plotWidth {
			break
		}
		labelWidth = nextLabelWidth
		plotWidth = nextPlotWidth
	}
	return columns, columnTotals, maxBucket, labelWidth
}

func usageYAxisLabelWidth(maxTokens int) int {
	labels := []string{compactTokenCount(maxTokens), compactTokenCount(maxTokens / 2), "0"}
	width := 1
	for _, label := range labels {
		width = maxInt(width, lipgloss.Width(label))
	}
	return width
}

func usageXAxisLabels(series ipc.Series) []string {
	if series.FromUnix <= 0 || series.ToUnix <= series.FromUnix {
		return nil
	}
	from := time.Unix(series.FromUnix, 0).In(time.Local)
	to := time.Unix(series.ToUnix, 0).In(time.Local)
	layout := "15:04"
	if series.Bucket == string(store.BucketDay) {
		layout = "01/02"
	}
	middle := from.Add(to.Sub(from) / 2)
	end := to
	if series.Bucket == string(store.BucketDay) && to.Hour() == 0 && to.Minute() == 0 &&
		to.Second() == 0 && to.After(from) {
		end = to.AddDate(0, 0, -1)
	}
	labels := []string{from.Format(layout), middle.Format(layout), end.Format(layout)}
	if series.Bucket != string(store.BucketDay) && to.Hour() == 0 && to.Minute() == 0 &&
		to.Second() == 0 && to.After(from) {
		labels[len(labels)-1] = "24:00"
	}
	return labels
}

func placeUsageXAxisLabels(labels []string, width int) string {
	if width <= 0 || len(labels) == 0 {
		return ""
	}
	cells := []rune(strings.Repeat(" ", width))
	place := func(label string, start int) bool {
		label = truncateText(label, width)
		labelWidth := lipgloss.Width(label)
		if start < 0 || start+labelWidth > width {
			return false
		}
		for offset, value := range []rune(label) {
			cells[start+offset] = value
		}
		return true
	}
	startLabel := truncateText(labels[0], width)
	endLabel := truncateText(labels[len(labels)-1], width)
	startWidth := lipgloss.Width(startLabel)
	endWidth := lipgloss.Width(endLabel)
	endStart := width - endWidth
	place(startLabel, 0)
	place(endLabel, endStart)
	if len(labels) >= 3 && startWidth+2 < endStart {
		middle := truncateText(labels[1], width)
		middleWidth := lipgloss.Width(middle)
		middleStart := (width - middleWidth) / 2
		if middleStart >= startWidth+2 && middleStart+middleWidth+1 <= endStart {
			place(middle, middleStart)
		}
	}
	return string(cells)
}

func (m *Model) appendUsageChart(r *renderer, totals []usageTotal, buckets [][]usageTotal,
	bucketCount, height int) {
	if height <= 0 || bucketCount <= 0 {
		return
	}
	columns, columnTotals, maxBucket, labelWidth := usageChartGeometry(buckets, bucketCount, r.width)
	if len(columns) == 0 || maxBucket <= 0 {
		return
	}
	seriesOrder := make(map[string]int, len(totals))
	for index, item := range totals {
		seriesOrder[item.Name] = index
	}
	seriesColors := usageSeriesColorMap(totals, m.theme)
	vertical, tick, corner, horizontal, arrow, joint := "│", "┤", "└", "─", "→", "┬"
	if m.ascii {
		vertical, tick, corner, horizontal, arrow, joint = "|", "+", "+", "-", ">", "+"
	}
	axisRows := 2
	if height < 4 {
		axisRows = 1
	}
	plotRows := height - axisRows
	if plotRows < 0 {
		plotRows = 0
	}
	middleRow := plotRows / 2
	for row := 0; row < plotRows; row++ {
		levelBottom := float64(plotRows-1-row) / float64(plotRows)
		levelTop := levelBottom + 1/float64(plotRows)
		tickLabel := ""
		isTick := false
		switch {
		case row == 0:
			tickLabel, isTick = compactTokenCount(maxBucket), true
		case middleRow > 0 && middleRow < plotRows-1 && row == middleRow:
			tickLabel, isTick = compactTokenCount(maxBucket/2), true
		case row == plotRows-1:
			tickLabel, isTick = "0", true
		}
		axis := vertical
		if isTick {
			axis = tick
		}
		barSegments := make([]uiSegment, 0, len(columns)/4+1)
		var run strings.Builder
		runColor := ""
		flushRun := func() {
			if run.Len() == 0 {
				return
			}
			barSegments = append(barSegments, uiSegment{
				text: run.String(), tone: toneNormal, color: runColor,
			})
			run.Reset()
		}
		for column := 0; column < len(columns); column++ {
			segments := columns[column]
			total := columnTotals[column]
			glyph := " "
			color := ""
			if total <= 0 || levelBottom >= float64(total)/float64(maxBucket) {
			} else {
				cumulative := 0.0
				chosen := 0
				chosenName := ""
				for _, item := range segments {
					segmentTop := cumulative + float64(item.tokens())/float64(maxBucket)
					if levelTop > cumulative && levelBottom < segmentTop {
						chosen = seriesOrder[item.Name]
						chosenName = item.Name
						break
					}
					cumulative = segmentTop
				}
				glyph = usageBarGlyph(chosen, m.ascii)
				color = seriesColors[chosenName]
			}
			if color != runColor {
				flushRun()
				runColor = color
			}
			run.WriteString(glyph)
		}
		flushRun()
		lineSegments := []uiSegment{{
			text: fmt.Sprintf("%*s %s", labelWidth, tickLabel, axis), tone: toneMuted,
		}}
		lineSegments = append(lineSegments, barSegments...)
		r.addSegments(toneNormal, lineSegments...)
	}
	axisLine := []rune(strings.Repeat(horizontal, len(columns)))
	if len(axisLine) > 0 {
		if len(axisLine) >= 3 {
			axisLine[len(axisLine)/2] = []rune(joint)[0]
			axisLine[len(axisLine)-1] = []rune(arrow)[0]
		} else {
			axisLine[len(axisLine)-1] = []rune(arrow)[0]
		}
	}
	r.addSegments(toneNormal,
		uiSegment{text: fmt.Sprintf("%*s %s", labelWidth, "", corner), tone: toneMuted},
		uiSegment{text: string(axisLine), tone: toneMuted})
	if height < 4 {
		return
	}
	labels := usageXAxisLabels(m.series)
	if len(labels) > 0 {
		r.add(toneMuted, "%s%s", strings.Repeat(" ", labelWidth+2), placeUsageXAxisLabels(labels, len(columns)))
	}
}

func (m *Model) appendUsageLegend(r *renderer, totals []usageTotal) {
	if len(totals) == 0 {
		return
	}
	limit := 4
	if r.width >= 120 {
		limit = 7
	}
	if limit > len(totals) {
		limit = len(totals)
	}
	colors := usageSeriesColorMap(totals, m.theme)
	legend := []uiSegment{{text: " ", tone: toneNormal}}
	for index := 0; index < limit; index++ {
		marker := usageBarGlyph(index, m.ascii)
		name := displaySeriesName(totals[index].Name)
		color := colors[totals[index].Name]
		legend = append(legend,
			uiSegment{text: marker + " " + name, tone: toneNormal, color: color},
			uiSegment{text: "  ", tone: toneMuted})
	}
	r.addSegments(toneNormal, legend...)
}

func (m *Model) appendUsageTable(r *renderer, totals []usageTotal, rows int) {
	columns := usageTableColumns(r.width)
	r.add(toneTitle, " %s", formatColumns(usageColumnTitles(columns), columns))
	if rows <= 1 {
		return
	}
	for index := 0; index < len(totals) && index < rows-1; index++ {
		cells := usageTableCells(totals[index], len(columns))
		r.add(toneNormal, " %s", formatColumns(cells, columns))
	}
}

func usageTableColumns(width int) []column {
	var columns []column
	switch {
	case width >= 120:
		columns = []column{{"Series", 32}, {"Requests", 9}, {"tokens", 12}, {"Hit", 8}, {"Cost", 10}}
	case width >= 80:
		columns = []column{{"Series", 22}, {"Requests", 9}, {"tokens", 10}, {"Hit", 7}, {"Cost", 9}}
	default:
		columns = []column{{"Series", 12}, {"tokens", 9}, {"Cost", 8}}
	}
	used := len(columns) - 1
	for _, item := range columns {
		used += item.width
	}
	if extra := width - used - 1; extra > 0 {
		columns[0].width += extra
	}
	return columns
}

func usageColumnTitles(columns []column) []string {
	result := make([]string, len(columns))
	for index, item := range columns {
		result[index] = item.title
	}
	return result
}

func usageTableCells(item usageTotal, count int) []string {
	hitRate := 0.0
	promptTokens := item.HitTokens + item.MissTokens
	if promptTokens > 0 {
		hitRate = float64(item.HitTokens) / float64(promptTokens) * 100
	}
	if count >= 5 {
		return []string{displaySeriesName(item.Name), formatCount(item.Requests), formatCount(item.tokens()),
			fmt.Sprintf("%.1f%%", hitRate), formatMoney(item.Cost)}
	}
	return []string{displaySeriesName(item.Name), formatCount(item.tokens()), formatMoney(item.Cost)}
}

func (m *Model) appendDateEditor(r *renderer) {
	r.add(toneTitle, " ─ Custom date ──────────────────────────")
	mark := func(index int) string {
		if index == m.dateFocus {
			return "[" + m.dateFields[index] + "]"
		}
		return " " + m.dateFields[index] + " "
	}
	if r.width >= 60 {
		r.add(toneNormal, " Start %s    End %s", mark(0), mark(1))
	} else {
		r.add(toneNormal, " Start %s", mark(0))
		r.add(toneNormal, " End %s", mark(1))
	}
	r.add(toneMuted, " YYYY-MM-DD  Tab switch  Enter confirm  Esc cancel")
}

func (m *Model) renderRequests(width, height int) []string {
	r := newRenderer(width, height, m.ascii, m.color, m.theme)
	total := m.requests.Total
	first := 0
	last := 0
	if len(m.requests.Rows) > 0 {
		first = m.requests.Offset + 1
		last = m.requests.Offset + len(m.requests.Rows)
	}
	pages := maxInt(1, (total+requestPageSize-1)/requestPageSize)
	page := m.requestPage + 1
	if total == 0 {
		page = 1
	}
	listHeight := m.requestListPanelHeight(height)
	detailHeight := height - listHeight
	if listHeight > 0 {
		title := fmt.Sprintf("Requests %d–%d / %d · page %d/%d", first, last, total, page, pages)
		r.panel(title, listHeight, toneBoxRequests, func(panel *renderer) {
			columns := requestColumns(panel.width)
			panel.add(toneTitle, " %s", formatColumns(columnTitles(columns), columns))
			if len(m.requests.Rows) == 0 {
				panel.add(toneMuted, " No requests yet")
				return
			}
			start := minInt(m.requestTop, len(m.requests.Rows))
			end := minInt(len(m.requests.Rows), start+panel.remaining())
			for index := start; index < end; index++ {
				lineTone := toneNormal
				if index == m.requestRow {
					lineTone = toneSelected
				}
				cells := requestCells(m.requests.Rows[index], index == m.requestRow, columns)
				panel.add(lineTone, " %s", formatColumns(cells, columns))
			}
		})
	}
	if selected, ok := m.selectedRequest(); ok && detailHeight >= 2 {
		r.panel("Request details", detailHeight, toneBoxActivity, func(panel *renderer) {
			panel.add(toneMuted, " Selected %s  %s / %s", time.Unix(selected.TimestampUnix, 0).Format("2006-01-02 15:04:05"),
				defaultValue(selected.Model, "Unknown"), displayClientName(selected.Client))
			panel.add(toneMuted, " prompt %s · completion %s · total %s · %.0fms · HTTP %d · %s",
				formatCount(selected.PromptTokens), formatCount(selected.CompletionTokens),
				formatCount(selected.TotalTokens), selected.LatencyMS, selected.StatusCode, formatMoney(selected.Cost))
			panel.add(toneMuted, " %s", defaultValue(selected.Endpoint, "unknown endpoint"))
		})
	}
	return r.finish()
}

func (m *Model) renderPricing(width, height int) []string {
	r := newRenderer(width, height, m.ascii, m.color, m.theme)
	summaryHeight := minInt(5, height)
	r.panel("Pricing revisions", summaryHeight, toneBoxTraffic, func(panel *renderer) {
		panel.add(toneNormal, " Revisions      %d", m.pricing.Count)
		if m.pricing.LatestValidUnix > 0 {
			panel.add(toneNormal, " Latest         %s",
				time.Unix(m.pricing.LatestValidUnix, 0).Format("2006-01-02 15:04"))
		} else {
			panel.add(toneMuted, " Latest         --")
		}
		panel.add(toneNormal, " Source         %s", pricingSourceText(m.pricing.LatestSource))
	})
	if r.remaining() > 0 {
		r.panel("Notes", r.remaining(), toneBoxRequests, func(panel *renderer) {
			panel.add(toneMuted, " Pricing revisions are read-only; model output is never written directly.")
			panel.add(toneMuted, " Automatic checks, diff previews, and manual approval arrive in Phase 6.")
		})
	}
	return r.finish()
}

func pricingSourceText(source string) string {
	switch source {
	case "bundled":
		return "Bundled"
	case "userOverride":
		return "User override"
	case "remote":
		return "Remote update"
	case "none", "":
		return "None"
	default:
		return source
	}
}

func (m *Model) renderHelp(width, height int) []string {
	r := newRenderer(width, height, m.ascii, m.color, m.theme)
	r.panel("Help · Shortcuts", height, toneBoxRequests, func(panel *renderer) {
		panel.add(toneAccent, " Pages")
		panel.add(toneNormal, "   o / u / e / p  Overview, Usage, Requests, Pricing")
		panel.add(toneNormal, "   Tab / ← →      Next / previous page")
		panel.add(toneNormal, "   1–6            Usage time range")
		panel.add(toneNormal, "   m / c          Group by model / client")
		panel.add(toneAccent, " Lists")
		panel.add(toneNormal, "   j / k  ↓ / ↑   Select request")
		panel.add(toneNormal, "   [ / ]          Previous / next page")
		panel.add(toneAccent, " Other")
		panel.add(toneNormal, "   r              Refresh now")
		panel.add(toneNormal, "   s              Open settings (returns to the dashboard)")
		panel.add(toneNormal, "   h / ? / Esc    Open / close help")
		panel.add(toneNormal, "   q / Ctrl+C     Quit TUI (keeps the daemon running)")
		panel.add(toneMuted, " Narrow terminals hide secondary columns; NO_COLOR=1 uses ASCII fallback.")
		if width < 80 {
			panel.add(toneMuted, " Enlarge the terminal to see every label.")
		}
	})
	return r.finish()
}

func periodSelector(period store.UsagePeriod) string {
	return segmentsText(periodSelectorSegments(period, fallbackPalette().Accent))
}

func periodSelectorSegments(period store.UsagePeriod, accent string) []uiSegment {
	segments := make([]uiSegment, 0, len(usagePeriodOptions)*5)
	superscript := []string{"¹", "²", "³", "⁴", "⁵", "⁶"}
	for index, item := range usagePeriodOptions {
		if index > 0 {
			segments = append(segments, uiSegment{text: "  ", tone: toneNormal})
		}
		active := item.period == period
		if active {
			segments = append(segments, uiSegment{text: "[", color: navigationTextColor})
		}
		segments = append(segments, uiSegment{text: superscript[index], tone: toneAccent, color: accent})
		segments = append(segments, uiSegment{text: item.label, color: navigationTextColor})
		if active {
			segments = append(segments, uiSegment{text: "]", color: navigationTextColor})
		}
	}
	return segments
}

func dimensionSelectorSegments(dimension store.UsageDimension, accent string) []uiSegment {
	segments := []uiSegment{{text: "by ", color: navigationTextColor}}
	segments = append(segments,
		shortcutWord("model", 'm', dimension == store.DimensionModel, accent)...)
	segments = append(segments, uiSegment{text: " ", tone: toneNormal})
	segments = append(segments,
		shortcutWord("client", 'c', dimension == store.DimensionClient, accent)...)
	return segments
}

func usageRangeText(series ipc.Series) (string, string) {
	bucket := "hour"
	if series.Bucket == string(store.BucketDay) {
		bucket = "day"
	}
	if series.FromUnix <= 0 || series.ToUnix <= series.FromUnix {
		return "range --", bucket
	}
	from := time.Unix(series.FromUnix, 0).In(time.Local)
	to := time.Unix(series.ToUnix, 0).In(time.Local)
	return store.DateRangeText(store.TimeRange{Start: from, End: to}), bucket
}

func usageSummary(points []ipc.SeriesPoint) string {
	var requests, hit, miss, out, unpriced int
	var cost float64
	for _, point := range points {
		requests += point.Requests
		hit += point.HitTokens
		miss += point.MissTokens
		out += point.OutTokens
		cost += point.Cost
		if point.Cost == 0 && point.Requests > 0 {
			unpriced++
		}
	}
	hitRate := 0.0
	if hit+miss > 0 {
		hitRate = float64(hit) / float64(hit+miss) * 100
	}
	text := fmt.Sprintf(" Requests %s · %s tokens · %.1f%% hit · %s spent",
		formatCount(requests), formatCount(hit+miss+out), hitRate, formatMoney(cost))
	if unpriced > 0 {
		text += fmt.Sprintf(" · %d unpriced", unpriced)
	}
	return text
}

func traceFrame(value ipc.Trace) trace.Frame {
	buckets := make([]trace.Bucket, 0, len(value.Traffic))
	for _, bucket := range value.Traffic {
		buckets = append(buckets, trace.Bucket{Upload: bucket.Upload, Download: bucket.Download})
	}
	bucketSeconds := time.Duration(value.TrafficBucketSecs * float64(time.Second))
	if bucketSeconds <= 0 {
		bucketSeconds = time.Second
	}
	return trace.Frame{
		Buckets:       buckets,
		BucketSeconds: bucketSeconds,
		Shift:         value.Shift,
		GridLines:     append([]float64(nil), value.Grid...),
		DownRate:      value.DownRate,
		UpRate:        value.UpRate,
		DownScale:     value.DownScale,
		UpScale:       value.UpScale,
		DownTop:       value.DownTop,
		UpTop:         value.UpTop,
		DownTotal:     value.DownTotal,
		UpTotal:       value.UpTotal,
	}
}

func traceFrameForWidth(value ipc.Trace, blockWidth float64) trace.Frame {
	frame := traceFrame(value)
	if blockWidth <= 0 {
		return frame
	}
	rebuilt := trace.MakeFrame(frame.Buckets, frame.BucketSeconds, traceNow(value), blockWidth)
	rebuilt.DownRate, rebuilt.UpRate = frame.DownRate, frame.UpRate
	rebuilt.DownScale, rebuilt.UpScale = frame.DownScale, frame.UpScale
	rebuilt.DownTop, rebuilt.UpTop = frame.DownTop, frame.UpTop
	rebuilt.DownTotal, rebuilt.UpTotal = frame.DownTotal, frame.UpTotal
	return rebuilt
}

func traceNow(value ipc.Trace) time.Time {
	if value.NowUnixNano != 0 {
		return time.Unix(0, value.NowUnixNano)
	}
	return time.Now()
}

func sparkline(values []float64) string {
	if len(values) == 0 {
		return "--"
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var builder strings.Builder
	for _, value := range values {
		index := 0
		if maximum > minimum {
			index = int(math.Round((value - minimum) / (maximum - minimum) * float64(len(blocks)-1)))
		} else {
			index = len(blocks) / 2
		}
		builder.WriteRune(blocks[index])
	}
	return builder.String()
}

func defaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func formatMoney(value float64) string { return fmt.Sprintf("¥%.4f", value) }

func formatBalanceMoney(value float64) string { return fmt.Sprintf("¥%.2f", value) }

func formatCount(value int) string {
	text := fmt.Sprintf("%d", value)
	if value < 1000 {
		return text
	}
	negative := strings.HasPrefix(text, "-")
	if negative {
		text = text[1:]
	}
	var builder strings.Builder
	for index, digit := range text {
		if index > 0 && (len(text)-index)%3 == 0 {
			builder.WriteByte(',')
		}
		builder.WriteRune(digit)
	}
	if negative {
		return "-" + builder.String()
	}
	return builder.String()
}
