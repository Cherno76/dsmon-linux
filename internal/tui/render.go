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

var toneStyles = map[tone]lipgloss.Style{
	toneTitle:       lipgloss.NewStyle().Foreground(lipgloss.Color("#a9b1d6")).Bold(true),
	toneAccent:      lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")),
	toneMuted:       lipgloss.NewStyle().Foreground(lipgloss.Color("#565f89")),
	toneOK:          lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a")),
	toneWarn:        lipgloss.NewStyle().Foreground(lipgloss.Color("#e0af68")),
	toneError:       lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e")),
	toneDown:        lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e")),
	toneUp:          lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")),
	toneBoxAccount:  lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a")),
	toneBoxActivity: lipgloss.NewStyle().Foreground(lipgloss.Color("#ad8ee6")),
	toneBoxTraffic:  lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e")),
	toneBoxRequests: lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")),
	toneSelected:    lipgloss.NewStyle().Reverse(true),
	toneDivLine:     lipgloss.NewStyle().Foreground(lipgloss.Color("#414868")),
}

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
	lines []uiLine
}

func newRenderer(width, limit int, ascii, color bool) *renderer {
	return &renderer{
		width: maxInt(1, width),
		limit: maxInt(0, limit),
		color: color,
		ascii: ascii,
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
		text := renderTone(line.text, line.tone, r.color)
		if len(line.segments) > 0 {
			text = renderSegments(line.segments, r.color)
		}
		result = append(result, renderTone(line.prefix, line.borderTone, r.color)+
			text+
			renderTone(line.suffix, line.borderTone, r.color))
	}
	return result
}

func renderTone(text string, valueTone tone, color bool) string {
	if text == "" || !color || valueTone == toneNormal {
		return text
	}
	if style, ok := toneStyles[valueTone]; ok {
		return style.Render(text)
	}
	return text
}

func renderSegments(segments []uiSegment, color bool) string {
	if !color {
		return segmentsText(segments)
	}
	var builder strings.Builder
	for _, segment := range segments {
		if segment.color != "" {
			builder.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(segment.color)).Render(segment.text))
			continue
		}
		builder.WriteString(renderTone(segment.text, segment.tone, color))
	}
	return builder.String()
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
		height-2, r.ascii, r.color)
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
	footer := m.renderFooter(width)
	bodyHeight := height - len(header) - len(footer)
	body := m.renderBody(width, maxInt(0, bodyHeight))
	lines := make([]string, 0, height)
	lines = append(lines, header...)
	lines = append(lines, body...)
	lines = append(lines, footer...)
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderHeader(width int) []string {
	stateText, stateTone := "● 正常", toneOK
	if m.connectionErr != "" {
		stateText, stateTone = "● 未连接", toneError
	} else if !m.stats.Health {
		stateText, stateTone = "● 异常", toneError
	} else if m.stats.Balance.Error != "" {
		stateText, stateTone = "● 余额异常", toneWarn
	}
	balanceText := "余额 --"
	if m.stats.Balance.Has {
		balanceText = "余额 " + formatBalanceMoney(m.stats.Balance.Total)
	}
	address := m.stats.ListenAddr
	if address == "" {
		address = "127.0.0.1:18080"
	}

	header := newRenderer(width, 2, m.ascii, m.color)
	if width >= 80 {
		header.add(stateTone, " DS-mon  %s  %s  %s", stateText, address, balanceText)
	} else {
		header.add(stateTone, " DS-mon  %s  %s", stateText, balanceText)
	}
	var tabs []string
	for index, title := range pageTitles {
		if Page(index) == m.page {
			tabs = append(tabs, "["+title+"]")
		} else {
			tabs = append(tabs, " "+title+" ")
		}
	}
	header.add(toneAccent, " %s", strings.Join(tabs, "  "))
	return header.finish()
}

func (m *Model) renderFooter(width int) []string {
	text := " Tab 切页  1-6 区间  m/c 分组  j/k 选择  [ ] 翻页  r 刷新  s 设置  ? 帮助  q 退出"
	if m.help {
		text = " Esc/? 返回  q 退出"
	} else if m.editingDates {
		text = " Tab 切换  Enter 确认  Esc 取消"
	} else if width < 80 {
		text = " Tab 切页  1-6 区间  m/c 分组  s 设置  ? 帮助  q 退出"
	}
	footer := newRenderer(width, 1, m.ascii, m.color)
	footer.add(toneMuted, " %s", text)
	return footer.finish()
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
	r := newRenderer(width, height, m.ascii, m.color)
	statusHeight := minInt(7, maxInt(6, height/6))
	activityTitle := "活动 · 2 分钟 · 3 req/格"
	if width >= 100 {
		activityTitle += " · 最近 120 秒"
	}
	if r.width >= 60 {
		// 账户占左侧 2/3，活动占右侧 1/3；流量独占下一行拿满宽度。
		activityWidth := r.width / 3
		accountWidth := r.width - 1 - activityWidth
		account := m.renderPanelBlock("账户", accountWidth, statusHeight, toneBoxAccount, m.appendOverviewStatus)
		activity := m.renderPanelBlock(activityTitle, activityWidth, statusHeight, toneBoxActivity, func(panel *renderer) {
			m.appendActivityChart(panel, panel.remaining())
		})
		for row := 0; row < statusHeight; row++ {
			r.addRaw(account[row] + " " + activity[row])
		}
	} else {
		r.panel("账户", statusHeight, toneBoxAccount, m.appendOverviewStatus)
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
		r.panel(activityTitle, chartSpace, toneBoxActivity, func(panel *renderer) {
			m.appendActivityChart(panel, panel.remaining())
		})
	}
	if recentHeight > 0 {
		r.panel("最近请求", recentHeight, toneBoxRequests, func(panel *renderer) {
			m.appendRecentRows(panel, panel.remaining())
		})
	}
	return r.finish()
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
	title := fmt.Sprintf("流量 · %d 秒", seconds)
	if showScale {
		title += " · 自动量程"
	}
	return title
}

func (m *Model) renderPanelBlock(title string, width, height int, borderTone tone,
	body func(*renderer)) []string {
	panel := newRenderer(width, height, m.ascii, m.color)
	panel.panel(title, height, borderTone, body)
	return panel.finish()
}

func (m *Model) appendOverviewStatus(r *renderer) {
	balanceTone := toneTitle
	if m.stats.Balance.Has && m.stats.BalanceLowThreshold > 0 &&
		m.stats.Balance.Total <= m.stats.BalanceLowThreshold {
		balanceTone = toneWarn
	}
	r.add(balanceTone, " 余额  %s", balanceValue(m.stats.Balance))
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
		unpriced = fmt.Sprintf(" · 未计价 %d", today.UnpricedRequests)
	}
	if r.remaining() > 0 {
		r.add(toneAccent, " 今日  %s 请求 · %s tokens · 命中 %.1f%% · 花费 %s%s",
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
		return " 查询失败: " + balance.Error
	}
	if !balance.Has {
		return " 等待第一次余额查询"
	}
	text := fmt.Sprintf(" 充值 %s · 赠送 %s", formatBalanceMoney(balance.ToppedUp), formatBalanceMoney(balance.Granted))
	if balance.LastSuccessUnix > 0 {
		text += " · 更新 " + time.Unix(balance.LastSuccessUnix, 0).Format("15:04:05")
	}
	if balance.Failures > 0 {
		text += fmt.Sprintf(" · 连续失败 %d 次", balance.Failures)
	}
	if width < 80 {
		return fmt.Sprintf(" 充值 %s · 赠送 %s", formatBalanceMoney(balance.ToppedUp), formatBalanceMoney(balance.Granted))
	}
	return text
}

func (m *Model) balanceTrendLine(width int) string {
	return segmentsText(m.balanceTrendSegments(width))
}

func (m *Model) balanceTrendSegments(width int) []uiSegment {
	points24, points7 := m.stats.Balance.Trend24h, m.stats.Balance.Trend7d
	if len(points24) == 0 && len(points7) == 0 {
		return []uiSegment{{text: " 余额  24h --  7d --", tone: toneNormal}}
	}
	range24, range7 := balanceRangeLabel(points24), balanceRangeLabel(points7)
	showRanges := width >= 64
	showAxis := width >= 56
	series := 1
	if len(points7) > 0 {
		series = 2
	}
	fixedWidth := lipgloss.Width(" 余额  24h ")
	if series == 2 {
		fixedWidth += lipgloss.Width("  7d ")
	}
	if showRanges {
		fixedWidth += lipgloss.Width(range24) + 1
		if series == 2 {
			fixedWidth += lipgloss.Width(range7) + 1
		}
	}
	if showAxis {
		fixedWidth += lipgloss.Width("  时间→")
	}
	sparkWidth := minInt(24, maxInt(1, (width-fixedWidth)/series))

	segments := []uiSegment{{text: " 余额  24h ", tone: toneNormal}}
	if showRanges && range24 != "" {
		segments = append(segments, uiSegment{text: range24 + " ", tone: toneMuted})
	}
	segments = append(segments, balanceSparklineSegments(points24, sparkWidth, !m.ascii)...)
	if len(points7) > 0 {
		segments = append(segments, uiSegment{text: "  7d ", tone: toneNormal})
		if showRanges && range7 != "" {
			segments = append(segments, uiSegment{text: range7 + " ", tone: toneMuted})
		}
		segments = append(segments, balanceSparklineSegments(points7, sparkWidth, !m.ascii)...)
	}
	if showAxis {
		segments = append(segments, uiSegment{text: "  时间→", tone: toneMuted})
	}
	return segments
}

func balanceRangeLabel(points []ipc.BalancePoint) string {
	if len(points) == 0 {
		return ""
	}
	minimum, maximum := points[0].Value, points[0].Value
	for _, point := range points[1:] {
		minimum = math.Min(minimum, point.Value)
		maximum = math.Max(maximum, point.Value)
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
	if len(points) == 0 || width <= 0 {
		return []uiSegment{{text: "--", tone: toneNormal}}
	}
	sampleWidth := width
	if braille {
		sampleWidth *= 2
	}
	values := make([]float64, 0, minInt(sampleWidth, len(points)))
	step := float64(maxInt(1, len(points)-1)) / float64(maxInt(1, sampleWidth-1))
	for column := 0; column < sampleWidth && len(points) > 0; column++ {
		index := int(math.Round(float64(column) * step))
		if index >= len(points) {
			index = len(points) - 1
		}
		values = append(values, points[index].Value)
	}
	if len(values) == 0 {
		return []uiSegment{{text: "--", tone: toneNormal}}
	}
	if braille {
		return brailleSparklineSegments(values, width)
	}
	return []uiSegment{{text: sparkline(values), tone: toneNormal}}
}

func brailleSparkline(values []float64, width int) string {
	return segmentsText(brailleSparklineSegments(values, width))
}

func brailleSparklineSegments(values []float64, width int) []uiSegment {
	if len(values) == 0 || width <= 0 {
		return []uiSegment{{text: "--", tone: toneNormal}}
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	canvas := newBrailleCanvas(width, 1)
	ratios := make([]float64, width)
	for x := 0; x < width*2; x++ {
		index := int(math.Round(float64(x) / float64(maxInt(1, width*2-1)) * float64(len(values)-1)))
		if index >= len(values) {
			index = len(values) - 1
		}
		ratio := 0.5
		level := 2
		if maximum > minimum {
			ratio = math.Min(1, math.Max(0, (values[index]-minimum)/(maximum-minimum)))
			level = 3 - int(math.Round(ratio*3))
		}
		if ratio > ratios[x/2] {
			ratios[x/2] = ratio
		}
		for y := level; y < 4; y++ {
			canvas.dot(x, y)
		}
	}
	runes := []rune(canvas.rows()[0])
	segments := make([]uiSegment, 0, width)
	for index := 0; index < len(runes); index++ {
		segments = append(segments, uiSegment{
			text:  string(runes[index]),
			tone:  toneNormal,
			color: trendGradientColor(ratios[index]),
		})
	}
	return segments
}

func trendGradientColor(ratio float64) string {
	ratio = math.Min(1, math.Max(0, ratio))
	if ratio < 0.5 {
		return blendHexColor("#f7768e", "#e0af68", ratio*2)
	}
	return blendHexColor("#e0af68", "#9ece6a", (ratio-0.5)*2)
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
	level = math.Min(1, math.Max(0, level))
	if level < 0.5 {
		return blendHexColor("#9ece6a", "#e0af68", level*2)
	}
	return blendHexColor("#e0af68", "#f7768e", (level-0.5)*2)
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
	chartTone := toneOK
	if !m.trace.Health {
		chartTone = toneError
	}
	for _, row := range rows {
		r.add(chartTone, "  %s", row)
	}
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
		color := trafficNetRowColor(row, laneHeight, false)
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
		return trafficGradientColor(ratio, false)
	}
	ratio := 1.0
	if upCells > 1 {
		ratio = float64(row-downCells) / float64(upCells-1)
	}
	return trafficGradientColor(ratio, true)
}

func trafficGradientColor(ratio float64, upload bool) string {
	ratio = math.Min(1, math.Max(0, ratio))
	// 与 Omarchy 当前 btop 主题一致：下载黄→粉，上传绿→青→蓝。
	start, middle, end := "#e0af68", "#f7768e", "#f7768e"
	if upload {
		start, middle, end = "#9ece6a", "#449dab", "#7aa2f7"
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
		r.add(toneMuted, " 暂无请求记录")
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
		columns = []column{{"", 1}, {"时间", 11}, {"模型", 24}, {"客户端", 15}, {"tokens", 9}, {"状态", 5}, {"延迟", 8}, {"费用", 9}}
	case width >= 80:
		columns = []column{{"", 1}, {"时间", 11}, {"模型", 18}, {"客户端", 11}, {"tokens", 8}, {"状态", 4}, {"费用", 8}}
	case width >= 40:
		columns = []column{{"", 1}, {"时间", 8}, {"模型", 7}, {"tokens", 6}, {"状态", 4}, {"费用", 8}}
	default:
		columns = []column{{"", 1}, {"时间", 7}, {"模型", 5}, {"tokens", 5}, {"状态", 3}}
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

func requestCells(row ipc.RequestRow, selected bool, columns []column) []string {
	marker := " "
	if selected {
		marker = ">"
	}
	at := time.Unix(row.TimestampUnix, 0).Format("01-02 15:04")
	cost := formatMoney(row.Cost)
	if len(columns) >= 8 {
		return []string{marker, at, defaultValue(row.Model, "未知"), defaultValue(row.Client, store.ClientUnknown),
			formatCount(row.TotalTokens), fmt.Sprintf("%d", row.StatusCode), fmt.Sprintf("%.0fms", row.LatencyMS), cost}
	}
	if len(columns) >= 7 {
		return []string{marker, at, defaultValue(row.Model, "未知"), defaultValue(row.Client, store.ClientUnknown),
			formatCount(row.TotalTokens), fmt.Sprintf("%d", row.StatusCode), cost}
	}
	if len(columns) == 6 {
		return []string{marker, at, defaultValue(row.Model, "未知"),
			formatCount(row.TotalTokens), fmt.Sprintf("%d", row.StatusCode), cost}
	}
	return []string{marker, at, defaultValue(row.Model, "未知"),
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
	r := newRenderer(width, height, m.ascii, m.color)
	rangeText, bucketText := usageRangeText(m.series)
	groupText := "模型"
	if m.dimension == store.DimensionClient {
		groupText = "客户端"
	}
	controlLines := 2
	if m.editingDates {
		controlLines += 4
		if width-2 < 60 {
			controlLines++
		}
	}
	controlHeight := minInt(controlLines+2, height)
	r.panel("查询范围", controlHeight, toneBoxActivity, func(panel *renderer) {
		panel.add(toneAccent, "%s", periodSelector(m.period))
		panel.add(toneMuted, " 分组  %s (m/c)  ·  %s  ·  %s桶", groupText, rangeText, bucketText)
		if m.editingDates {
			m.appendDateEditor(panel)
		}
	})

	seriesTotals, pointsByBucket, bucketCount := usageChartData(m.series)
	if len(m.series.Points) == 0 {
		if r.remaining() > 0 {
			r.panel("用量趋势 · "+m.periodLabel(), r.remaining(), toneBoxAccount, func(panel *renderer) {
				panel.add(toneMuted, " 暂无用量")
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
		r.panel(usageChartTitle(m.periodLabel(), groupText, width), chartHeight, toneBoxAccount,
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
		r.panel("系列明细 · 按"+groupText, tableHeight, toneBoxRequests, func(panel *renderer) {
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

func usageChartTitle(periodLabel, groupText string, width int) string {
	switch {
	case width >= 72:
		return fmt.Sprintf("用量趋势 · tokens · %s · 按%s", periodLabel, groupText)
	case width >= 52:
		return fmt.Sprintf("用量趋势 · tokens · %s", periodLabel)
	default:
		return fmt.Sprintf("用量趋势 · %s", periodLabel)
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
		level := 1.0
		if plotRows > 1 {
			level = 1 - float64(row)/float64(plotRows-1)
		}
		barColor := usageBarColor(level)
		var builder strings.Builder
		for column := 0; column < len(columns); column++ {
			segments := columns[column]
			total := columnTotals[column]
			if total <= 0 || levelBottom >= float64(total)/float64(maxBucket) {
				builder.WriteRune(' ')
				continue
			}
			cumulative := 0.0
			chosen := 0
			for _, item := range segments {
				segmentTop := cumulative + float64(item.tokens())/float64(maxBucket)
				if levelTop > cumulative && levelBottom < segmentTop {
					chosen = seriesOrder[item.Name]
					break
				}
				cumulative = segmentTop
			}
			builder.WriteString(usageBarGlyph(chosen, m.ascii))
		}
		r.addSegments(toneNormal,
			uiSegment{text: fmt.Sprintf("%*s %s", labelWidth, tickLabel, axis), tone: toneMuted},
			uiSegment{text: builder.String(), tone: toneNormal, color: barColor})
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
	legend := make([]string, 0, limit)
	for index := 0; index < limit; index++ {
		marker := usageBarGlyph(index, m.ascii)
		legend = append(legend, marker+" "+totals[index].Name)
	}
	r.add(toneMuted, " %s", strings.Join(legend, "  "))
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
		columns = []column{{"系列", 32}, {"请求", 8}, {"tokens", 12}, {"命中", 8}, {"费用", 10}}
	case width >= 80:
		columns = []column{{"系列", 22}, {"请求", 7}, {"tokens", 10}, {"命中", 7}, {"费用", 9}}
	default:
		columns = []column{{"系列", 12}, {"tokens", 9}, {"费用", 8}}
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
		return []string{item.Name, formatCount(item.Requests), formatCount(item.tokens()),
			fmt.Sprintf("%.1f%%", hitRate), formatMoney(item.Cost)}
	}
	return []string{item.Name, formatCount(item.tokens()), formatMoney(item.Cost)}
}

func (m *Model) appendDateEditor(r *renderer) {
	r.add(toneTitle, " ─ 自定义日期 ─────────────────────────")
	mark := func(index int) string {
		if index == m.dateFocus {
			return "[" + m.dateFields[index] + "]"
		}
		return " " + m.dateFields[index] + " "
	}
	if r.width >= 60 {
		r.add(toneNormal, " 开始 %s    结束 %s", mark(0), mark(1))
	} else {
		r.add(toneNormal, " 开始 %s", mark(0))
		r.add(toneNormal, " 结束 %s", mark(1))
	}
	r.add(toneMuted, " YYYY-MM-DD  Tab 切换  Enter 确认  Esc 取消")
}

func (m *Model) renderRequests(width, height int) []string {
	r := newRenderer(width, height, m.ascii, m.color)
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
		title := fmt.Sprintf("请求 %d–%d / %d · 第 %d/%d 页", first, last, total, page, pages)
		r.panel(title, listHeight, toneBoxRequests, func(panel *renderer) {
			columns := requestColumns(panel.width)
			panel.add(toneTitle, " %s", formatColumns(columnTitles(columns), columns))
			if len(m.requests.Rows) == 0 {
				panel.add(toneMuted, " 暂无请求记录")
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
		r.panel("请求详情", detailHeight, toneBoxActivity, func(panel *renderer) {
			panel.add(toneMuted, " 选中 %s  %s / %s", time.Unix(selected.TimestampUnix, 0).Format("2006-01-02 15:04:05"),
				defaultValue(selected.Model, "未知"), defaultValue(selected.Client, store.ClientUnknown))
			panel.add(toneMuted, " prompt %s · completion %s · total %s · %.0fms · HTTP %d · %s",
				formatCount(selected.PromptTokens), formatCount(selected.CompletionTokens),
				formatCount(selected.TotalTokens), selected.LatencyMS, selected.StatusCode, formatMoney(selected.Cost))
			panel.add(toneMuted, " %s", defaultValue(selected.Endpoint, "endpoint 未知"))
		})
	}
	return r.finish()
}

func (m *Model) renderPricing(width, height int) []string {
	r := newRenderer(width, height, m.ascii, m.color)
	summaryHeight := minInt(5, height)
	r.panel("计费修订", summaryHeight, toneBoxTraffic, func(panel *renderer) {
		panel.add(toneNormal, " 修订数    %d", m.pricing.Count)
		if m.pricing.LatestValidUnix > 0 {
			panel.add(toneNormal, " 最近生效  %s",
				time.Unix(m.pricing.LatestValidUnix, 0).Format("2006-01-02 15:04"))
		} else {
			panel.add(toneMuted, " 最近生效  --")
		}
		panel.add(toneNormal, " 来源      %s", pricingSourceText(m.pricing.LatestSource))
	})
	if r.remaining() > 0 {
		r.panel("说明", r.remaining(), toneBoxRequests, func(panel *renderer) {
			panel.add(toneMuted, " 计费修订只读；模型输出不会直接写库。")
			panel.add(toneMuted, " 自动检查、差异预览和人工确认在 Phase 6 接入。")
		})
	}
	return r.finish()
}

func pricingSourceText(source string) string {
	switch source {
	case "bundled":
		return "内置规则"
	case "userOverride":
		return "用户覆盖"
	case "remote":
		return "远端更新"
	case "none", "":
		return "无"
	default:
		return source
	}
}

func (m *Model) renderHelp(width, height int) []string {
	r := newRenderer(width, height, m.ascii, m.color)
	r.panel("帮助 · 快捷键", height, toneBoxRequests, func(panel *renderer) {
		panel.add(toneAccent, " 页面")
		panel.add(toneNormal, "   Tab / ← →      切换概览、用量、请求、计费")
		panel.add(toneNormal, "   1–6             用量页时间范围")
		panel.add(toneNormal, "   m / c           按模型 / 客户端分组")
		panel.add(toneAccent, " 列表")
		panel.add(toneNormal, "   j / k  ↓ / ↑    选择请求")
		panel.add(toneNormal, "   [ / ]           上一页 / 下一页")
		panel.add(toneAccent, " 其他")
		panel.add(toneNormal, "   r               立即刷新")
		panel.add(toneNormal, "   s               打开设置（退出当前界面后返回）")
		panel.add(toneNormal, "   ? / Esc         打开 / 关闭帮助")
		panel.add(toneNormal, "   q / Ctrl+C      退出 TUI（不停止 daemon）")
		panel.add(toneMuted, " 窄屏自动隐藏次要列；NO_COLOR=1 使用 ASCII 回退。")
		if width < 80 {
			panel.add(toneMuted, " 终端太窄时请放大窗口查看完整标签。")
		}
	})
	return r.finish()
}

func periodSelector(period store.UsagePeriod) string {
	parts := make([]string, 0, len(usagePeriodOptions))
	for index, item := range usagePeriodOptions {
		text := fmt.Sprintf("%d%s", index+1, item.label)
		if item.period == period {
			text = "[" + text + "]"
		}
		parts = append(parts, text)
	}
	return " " + strings.Join(parts, " ")
}

func usageRangeText(series ipc.Series) (string, string) {
	bucket := "小时"
	if series.Bucket == string(store.BucketDay) {
		bucket = "天"
	}
	if series.FromUnix <= 0 || series.ToUnix <= series.FromUnix {
		return "区间 --", bucket
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
	text := fmt.Sprintf(" 请求 %s · %s tokens · 命中 %.1f%% · 花费 %s",
		formatCount(requests), formatCount(hit+miss+out), hitRate, formatMoney(cost))
	if unpriced > 0 {
		text += fmt.Sprintf(" · 未计价 %d", unpriced)
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
