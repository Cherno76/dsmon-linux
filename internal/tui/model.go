package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"dsmon/internal/ipc"
	"dsmon/internal/store"
)

type Page int

const (
	PageOverview Page = iota
	PageUsage
	PageRequests
	PagePricing
	pageCount
)

const requestPageSize = 100

var pageTitles = []string{"概览", "用量", "请求", "计费"}

type Model struct {
	client *ipc.Client
	ascii  bool
	color  bool

	width  int
	height int

	page      Page
	period    store.UsagePeriod
	dimension store.UsageDimension

	customFrom time.Time
	customTo   time.Time

	stats    ipc.Stats
	trace    ipc.Trace
	series   ipc.Series
	requests ipc.Requests
	pricing  ipc.Pricing

	connectionErr string
	pageErr       string

	seriesDirty   bool
	requestsDirty bool
	pricingDirty  bool
	requestPage   int
	requestRow    int
	requestTop    int

	editingDates bool
	dateFields   [2]string
	dateFocus    int
	help         bool
	ticks        int
}

type usagePeriodOption struct {
	period store.UsagePeriod
	label  string
}

var usagePeriodOptions = []usagePeriodOption{
	{store.PeriodToday, "今日"},
	{store.PeriodYesterday, "昨日"},
	{store.PeriodLast7, "近7天"},
	{store.PeriodLast30, "近30天"},
	{store.PeriodThisMonth, "本月"},
	{store.PeriodLastMonth, "上月"},
}

func NewModel(client *ipc.Client, ascii bool) *Model {
	noColor := os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
	ascii = ascii || noColor
	return &Model{
		client:        client,
		ascii:         ascii,
		color:         !ascii,
		width:         100,
		height:        30,
		period:        store.PeriodToday,
		dimension:     store.DimensionModel,
		seriesDirty:   true,
		requestsDirty: true,
		pricingDirty:  true,
	}
}

type tickMsg time.Time
type statsMsg struct {
	stats ipc.Stats
	err   error
}
type traceMsg struct {
	trace ipc.Trace
	err   error
}
type seriesMsg struct {
	series ipc.Series
	err    error
}
type requestsMsg struct {
	requests ipc.Requests
	err      error
}
type pricingMsg struct {
	pricing ipc.Pricing
	err     error
}
type settingsDoneMsg struct{ err error }

func (m *Model) Init() tea.Cmd {
	commands := m.refreshCommands()
	commands = append(commands, tickCmd())
	return tea.Batch(commands...)
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(at time.Time) tea.Msg { return tickMsg(at) })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.ensureRequestVisible()
		return m, nil
	case tickMsg:
		m.ticks++
		commands := m.refreshCommands()
		commands = append(commands, tickCmd())
		return m, tea.Batch(commands...)
	case statsMsg:
		if msg.err != nil {
			m.connectionErr = msg.err.Error()
		} else {
			m.stats = msg.stats
			m.connectionErr = ""
		}
		return m, nil
	case traceMsg:
		if msg.err != nil {
			m.connectionErr = msg.err.Error()
		} else {
			m.trace = msg.trace
			m.connectionErr = ""
		}
		return m, nil
	case seriesMsg:
		if msg.err != nil {
			m.pageErr = msg.err.Error()
		} else {
			m.series = msg.series
			m.pageErr = ""
		}
		return m, nil
	case requestsMsg:
		if msg.err != nil {
			m.pageErr = msg.err.Error()
		} else {
			m.requests = msg.requests
			m.pageErr = ""
			if m.requests.Total > 0 && m.requests.Offset >= m.requests.Total {
				m.requestPage = (m.requests.Total - 1) / requestPageSize
				m.requestRow = 0
				m.requestsDirty = true
			}
			if m.requestRow >= len(m.requests.Rows) {
				m.requestRow = maxInt(0, len(m.requests.Rows)-1)
			}
			m.ensureRequestVisible()
		}
		return m, nil
	case pricingMsg:
		if msg.err != nil {
			m.pageErr = msg.err.Error()
		} else {
			m.pricing = msg.pricing
			m.pageErr = ""
		}
		return m, nil
	case settingsDoneMsg:
		if msg.err != nil {
			m.pageErr = "设置: " + msg.err.Error()
		} else {
			m.pageErr = ""
		}
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg.String())
	}
	return m, nil
}

func (m *Model) updateKey(key string) (tea.Model, tea.Cmd) {
	if m.help {
		switch key {
		case "esc", "?", "q":
			m.help = false
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil
	}
	if m.editingDates {
		return m.updateDateEntry(key)
	}

	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.help = true
	case "tab", "right":
		m.page = (m.page + 1) % pageCount
		m.pageChanged()
	case "shift+tab", "left":
		m.page = (m.page + pageCount - 1) % pageCount
		m.pageChanged()
	case "m":
		m.dimension = store.DimensionModel
		m.seriesDirty = true
		m.page = PageUsage
	case "c":
		m.dimension = store.DimensionClient
		m.seriesDirty = true
		m.page = PageUsage
	case "r":
		m.seriesDirty = true
		m.requestsDirty = true
		m.pricingDirty = true
		return m, tea.Batch(m.refreshCommands()...)
	case "s":
		executable, err := os.Executable()
		if err != nil {
			m.pageErr = "打开设置失败: " + err.Error()
			return m, nil
		}
		return m, tea.ExecProcess(exec.Command(executable, "settings"), func(err error) tea.Msg {
			return settingsDoneMsg{err: err}
		})
	case "j", "down":
		if m.page == PageRequests && len(m.requests.Rows) > 0 {
			m.requestRow = minInt(m.requestRow+1, len(m.requests.Rows)-1)
			m.ensureRequestVisible()
		}
	case "k", "up":
		if m.page == PageRequests && m.requestRow > 0 {
			m.requestRow--
			m.ensureRequestVisible()
		}
	case "[":
		if m.page == PageRequests && m.requestPage > 0 {
			m.requestPage--
			m.requestRow = 0
			m.requestTop = 0
			m.requestsDirty = true
		}
	case "]":
		if m.page == PageRequests && (m.requestPage+1)*requestPageSize < m.requests.Total {
			m.requestPage++
			m.requestRow = 0
			m.requestTop = 0
			m.requestsDirty = true
		}
	case "1", "2", "3", "4", "5", "6":
		index, _ := strconv.Atoi(key)
		m.period = usagePeriodOptions[index-1].period
		m.page = PageUsage
		m.seriesDirty = true
	}
	return m, tea.Batch(m.refreshCommands()...)
}

func (m *Model) pageChanged() {
	switch m.page {
	case PageUsage:
		m.seriesDirty = true
	case PageRequests:
		m.requestsDirty = true
	case PagePricing:
		m.pricingDirty = true
	}
}

func (m *Model) refreshCommands() []tea.Cmd {
	commands := []tea.Cmd{fetchStatsCmd(m.client), fetchTraceCmd(m.client, maxInt(20, m.width-4))}
	if m.page == PageUsage && (m.seriesDirty || m.ticks%5 == 0) {
		m.seriesDirty = false
		commands = append(commands, fetchSeriesCmd(m.client, m.period, m.dimension, m.customFrom, m.customTo))
	}
	if m.page == PageRequests && (m.requestsDirty || m.ticks%2 == 0) {
		m.requestsDirty = false
		commands = append(commands, fetchRequestsCmd(m.client, requestPageSize, m.requestPage*requestPageSize))
	}
	if m.page == PagePricing && (m.pricingDirty || m.ticks%10 == 0) {
		m.pricingDirty = false
		commands = append(commands, fetchPricingCmd(m.client))
	}
	return commands
}

func fetchStatsCmd(client *ipc.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stats, err := client.Stats(ctx)
		return statsMsg{stats: stats, err: err}
	}
}

func fetchTraceCmd(client *ipc.Client, width int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		value, err := client.Trace(ctx, maxInt(20, width))
		return traceMsg{trace: value, err: err}
	}
}

func fetchSeriesCmd(client *ipc.Client, period store.UsagePeriod, dimension store.UsageDimension,
	from, to time.Time) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		value, err := client.Series(ctx, string(period), string(dimension), from, to)
		return seriesMsg{series: value, err: err}
	}
}

func fetchRequestsCmd(client *ipc.Client, limit, offset int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		value, err := client.Requests(ctx, limit, offset)
		return requestsMsg{requests: value, err: err}
	}
}

func fetchPricingCmd(client *ipc.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		value, err := client.Pricing(ctx)
		return pricingMsg{pricing: value, err: err}
	}
}

func (m *Model) openDateEditor() {
	now := time.Now()
	if m.customFrom.IsZero() || m.customTo.IsZero() {
		m.customFrom = startOfLocalDay(now.AddDate(0, 0, -13))
		m.customTo = startOfLocalDay(now.AddDate(0, 0, 1))
	}
	m.editingDates = true
	m.dateFocus = 0
	m.dateFields[0] = m.customFrom.Format("2006-01-02")
	m.dateFields[1] = m.customTo.AddDate(0, 0, -1).Format("2006-01-02")
}

func (m *Model) updateDateEntry(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.editingDates = false
		if m.period == store.PeriodCustom && (m.customFrom.IsZero() || m.customTo.IsZero()) {
			m.period = store.PeriodLast30
		}
		return m, nil
	case "tab":
		m.dateFocus = (m.dateFocus + 1) % 2
	case "shift+tab":
		m.dateFocus = (m.dateFocus + 1) % 2
	case "enter":
		if m.dateFocus == 0 {
			m.dateFocus = 1
			return m, nil
		}
		from, errFrom := time.ParseInLocation("2006-01-02", strings.TrimSpace(m.dateFields[0]), time.Local)
		to, errTo := time.ParseInLocation("2006-01-02", strings.TrimSpace(m.dateFields[1]), time.Local)
		if errFrom != nil || errTo != nil || to.Before(from) {
			m.pageErr = "日期格式应为 YYYY-MM-DD，且结束日期不能早于开始日期"
			return m, nil
		}
		m.customFrom = startOfLocalDay(from)
		m.customTo = startOfLocalDay(to).AddDate(0, 0, 1)
		m.period = store.PeriodCustom
		m.editingDates = false
		m.seriesDirty = true
		m.pageErr = ""
		return m, tea.Batch(m.refreshCommands()...)
	case "backspace":
		field := m.dateFields[m.dateFocus]
		if field != "" {
			m.dateFields[m.dateFocus] = field[:len(field)-1]
		}
	default:
		if len(key) == 1 && strings.ContainsRune("0123456789-", rune(key[0])) {
			m.dateFields[m.dateFocus] += key
		}
	}
	return m, nil
}

func startOfLocalDay(value time.Time) time.Time {
	local := value.In(time.Local)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
}

func (m *Model) View() string { return renderModel(m) }

func (m *Model) periodLabel() string {
	labels := map[store.UsagePeriod]string{
		store.PeriodToday: "今日", store.PeriodYesterday: "昨日", store.PeriodLast7: "近 7 天",
		store.PeriodLast30: "近 30 天", store.PeriodThisMonth: "本月", store.PeriodLastMonth: "上月",
		store.PeriodCustom: "自定义",
	}
	if label := labels[m.period]; label != "" {
		return label
	}
	return string(m.period)
}

func minInt(first, second int) int {
	if first < second {
		return first
	}
	return second
}

func (m *Model) statusText() string {
	if m.pageErr != "" {
		return m.pageErr
	}
	if m.connectionErr != "" {
		return "daemon 未连接: " + m.connectionErr
	}
	if m.stats.Balance.Error != "" {
		return "余额: " + m.stats.Balance.Error
	}
	return ""
}

func (m *Model) selectedRequest() (ipc.RequestRow, bool) {
	if m.requestRow < 0 || m.requestRow >= len(m.requests.Rows) {
		return ipc.RequestRow{}, false
	}
	return m.requests.Rows[m.requestRow], true
}

func (m *Model) requestVisibleRows() int {
	bodyHeight := maxInt(0, m.height-3)
	listHeight := m.requestListPanelHeight(bodyHeight)
	if listHeight < 3 {
		return 1
	}
	return maxInt(1, listHeight-3)
}

func (m *Model) requestDetailPanelHeight(bodyHeight int) int {
	if _, ok := m.selectedRequest(); !ok {
		return 0
	}
	detailHeight := 6
	if bodyHeight < 12 {
		detailHeight = 4
	}
	if bodyHeight-detailHeight < 3 {
		return 0
	}
	return detailHeight
}

func (m *Model) requestListPanelHeight(bodyHeight int) int {
	if bodyHeight <= 0 {
		return 0
	}
	detailHeight := m.requestDetailPanelHeight(bodyHeight)
	if bodyHeight-detailHeight < 3 {
		return bodyHeight
	}
	return bodyHeight - detailHeight
}

func (m *Model) ensureRequestVisible() {
	visible := m.requestVisibleRows()
	if m.requestRow < m.requestTop {
		m.requestTop = m.requestRow
	}
	if m.requestRow >= m.requestTop+visible {
		m.requestTop = m.requestRow - visible + 1
	}
	maxTop := maxInt(0, len(m.requests.Rows)-visible)
	if m.requestTop > maxTop {
		m.requestTop = maxTop
	}
	if m.requestTop < 0 {
		m.requestTop = 0
	}
}

func (m *Model) debugState() string {
	return fmt.Sprintf("page=%s period=%s", pageTitles[m.page], m.periodLabel())
}
