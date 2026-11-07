package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"dsmon/internal/config"
	"dsmon/internal/ipc"
)

type settingsFieldDef struct {
	label  string
	short  string
	help   string
	secret bool
	toggle bool
}

const (
	settingsAPIKey = iota
	settingsOpenAIBase
	settingsAnthropicBase
	settingsToolsBase
	settingsListenPort
	settingsRPMLimit
	settingsBalanceLow
	settingsRetentionDays
	settingsPricingSource
	settingsPricingAuto
	settingsFieldCount
)

var settingsFields = [settingsFieldCount]settingsFieldDef{
	{label: "API key", short: "API key", help: "留空保留旧密钥；输入只显示掩码，保存时 AES-GCM 加密", secret: true},
	{label: "OpenAI base URL", short: "OpenAI", help: "OpenAI 兼容接口的绝对 http(s) 地址"},
	{label: "Anthropic base URL", short: "Anthropic", help: "Anthropic Messages 接口的绝对 http(s) 地址"},
	{label: "Tools base URL", short: "Tools", help: "Search / Fetch 工具接口地址"},
	{label: "监听端口", short: "端口", help: "固定监听 127.0.0.1；端口范围 1–65535"},
	{label: "RPM 上限", short: "RPM", help: "每提供商 60 秒窗口上限；0 表示关闭"},
	{label: "余额低阈值", short: "余额阈值", help: "低于该值时余额文字变黄；0 表示关闭"},
	{label: "保留天数", short: "保留天数", help: "0 表示永久保留；上限 3650 天"},
	{label: "计费源 URL", short: "计费源", help: "Phase 6 抓取计费页面使用，只标记待确认，不自动写库"},
	{label: "自动检查计费", short: "自动检查", help: "每 7 天检查一次，只生成待确认更新", toggle: true},
}

type SettingsModel struct {
	client *ipc.Client
	ascii  bool
	color  bool

	width  int
	height int

	settings config.ProxySettings
	hasKey   bool
	editors  []string

	focus   int
	editing bool
	saving  bool

	status     string
	statusTone tone
}

type settingsSaveMsg struct {
	settings  config.ProxySettings
	newKey    bool
	hadKey    bool
	saveErr   error
	reloadErr error
}

func NewSettingsModel(client *ipc.Client, ascii bool, settings config.ProxySettings, hasKey bool) *SettingsModel {
	noColor := os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
	ascii = ascii || noColor
	model := &SettingsModel{
		client:   client,
		ascii:    ascii,
		color:    !ascii,
		width:    80,
		height:   24,
		settings: settings,
		hasKey:   hasKey,
		editors:  make([]string, settingsFieldCount),
	}
	model.resetEditors()
	return model
}

func RunSettings(client *ipc.Client, ascii bool) error {
	settings, hasKey, err := config.LoadEditableSettings()
	if err != nil {
		return fmt.Errorf("读取设置: %w", err)
	}
	program := tea.NewProgram(NewSettingsModel(client, ascii, settings, hasKey), tea.WithAltScreen())
	_, err = program.Run()
	return err
}

func (m *SettingsModel) Init() tea.Cmd { return nil }

func (m *SettingsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		return m, nil
	case settingsSaveMsg:
		m.saving = false
		if msg.saveErr != nil {
			m.status = "保存失败: " + msg.saveErr.Error()
			m.statusTone = toneError
			return m, nil
		}
		m.settings = msg.settings
		m.hasKey = msg.hadKey || msg.newKey
		m.resetEditors()
		m.editing = false
		if msg.reloadErr != nil {
			if reloadOffline(msg.reloadErr) {
				m.status = "已保存；daemon 未连接，下次启动生效"
				m.statusTone = toneWarn
			} else {
				m.status = "已保存，但热加载失败: " + msg.reloadErr.Error()
				m.statusTone = toneError
			}
			return m, nil
		}
		m.status = "已保存并热加载"
		m.statusTone = toneOK
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg.String())
	}
	return m, nil
}

func (m *SettingsModel) updateKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.saving {
		return m, nil
	}
	if m.editing {
		switch key {
		case "esc":
			m.revertEditor()
			m.editing = false
		case "enter":
			m.editing = false
			m.status = ""
		case "tab":
			m.editing = false
			m.focus = (m.focus + 1) % settingsFieldCount
		case "shift+tab":
			m.editing = false
			m.focus = (m.focus + settingsFieldCount - 1) % settingsFieldCount
		case "up":
			m.editing = false
			m.focus = (m.focus + settingsFieldCount - 1) % settingsFieldCount
		case "down":
			m.editing = false
			m.focus = (m.focus + 1) % settingsFieldCount
		case "ctrl+s":
			return m, m.startSave()
		case "backspace", "ctrl+h":
			m.editors[m.focus] = trimLastRune(m.editors[m.focus])
		default:
			if utf8.RuneCountInString(key) == 1 && key[0] >= 0x20 && key[0] != 0x7f {
				m.editors[m.focus] += key
			}
		}
		return m, nil
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "tab", "down":
		m.focus = (m.focus + 1) % settingsFieldCount
	case "shift+tab", "up":
		m.focus = (m.focus + settingsFieldCount - 1) % settingsFieldCount
	case "enter", " ":
		if settingsFields[m.focus].toggle {
			m.settings.PricingAutoCheck = !m.settings.PricingAutoCheck
			m.status = "自动检查已切换，尚未保存"
			m.statusTone = toneMuted
			return m, nil
		}
		m.editing = true
		m.status = ""
	case "ctrl+s":
		return m, m.startSave()
	}
	return m, nil
}

func (m *SettingsModel) startSave() tea.Cmd {
	settings, apiKey, err := m.commit()
	if err != nil {
		m.status = "校验失败: " + err.Error()
		m.statusTone = toneError
		return nil
	}
	m.saving = true
	m.status = "保存中…"
	m.statusTone = toneAccent
	return settingsSaveCmd(m.client, settings, apiKey, m.hasKey)
}

func settingsSaveCmd(client *ipc.Client, settings config.ProxySettings, apiKey string, hadKey bool) tea.Cmd {
	return func() tea.Msg {
		if err := config.SaveProxySettings(settings, apiKey); err != nil {
			return settingsSaveMsg{saveErr: err}
		}
		result := settingsSaveMsg{
			settings: settings,
			newKey:   strings.TrimSpace(apiKey) != "",
			hadKey:   hadKey,
		}
		if normalized, savedHasKey, err := config.LoadEditableSettings(); err == nil {
			result.settings = normalized
			result.hadKey = savedHasKey
		}
		if client == nil {
			result.reloadErr = errors.New("ipc socket path is empty")
			return result
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		result.reloadErr = client.Reload(ctx)
		return result
	}
}

func reloadOffline(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "ipc socket path is empty") {
		return true
	}
	var operation *net.OpError
	return errors.As(err, &operation) && operation.Op == "dial"
}

func (m *SettingsModel) commit() (config.ProxySettings, string, error) {
	settings := m.settings
	settings.OpenAIBase = strings.TrimSpace(m.editors[settingsOpenAIBase])
	settings.AnthropicBase = strings.TrimSpace(m.editors[settingsAnthropicBase])
	settings.ToolsBase = strings.TrimSpace(m.editors[settingsToolsBase])
	settings.PricingSourceURL = strings.TrimSpace(m.editors[settingsPricingSource])

	port, err := parseListenPort(m.editors[settingsListenPort])
	if err != nil {
		return config.ProxySettings{}, "", err
	}
	settings.ListenAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	rpm, err := parseNonNegativeInt(m.editors[settingsRPMLimit], "RPM 上限")
	if err != nil {
		return config.ProxySettings{}, "", err
	}
	settings.RPMLimit = rpm
	threshold, err := strconv.ParseFloat(strings.TrimSpace(m.editors[settingsBalanceLow]), 64)
	if err != nil {
		return config.ProxySettings{}, "", errors.New("余额阈值必须是数字")
	}
	settings.BalanceLowThreshold = threshold
	retention, err := parseNonNegativeInt(m.editors[settingsRetentionDays], "保留天数")
	if err != nil {
		return config.ProxySettings{}, "", err
	}
	settings.RetentionDays = retention
	settings.APIKey = ""

	oldDefaultBalance := strings.TrimRight(m.settings.OpenAIBase, "/") + config.DefaultBalancePath
	if strings.TrimSpace(settings.BalanceURL) == "" ||
		strings.TrimRight(settings.BalanceURL, "/") == strings.TrimRight(oldDefaultBalance, "/") {
		settings.BalanceURL = strings.TrimRight(settings.OpenAIBase, "/") + config.DefaultBalancePath
	}
	if err := config.ValidateProxySettings(settings); err != nil {
		return config.ProxySettings{}, "", err
	}
	return settings, m.editors[settingsAPIKey], nil
}

func parseListenPort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, ":") {
		host, port, err := net.SplitHostPort(raw)
		if err != nil {
			return 0, fmt.Errorf("监听端口格式错误: %w", err)
		}
		if host != "127.0.0.1" {
			return 0, errors.New("监听地址必须使用 127.0.0.1")
		}
		raw = port
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("监听端口必须是 1–65535")
	}
	return port, nil
}

func parseNonNegativeInt(raw, name string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s必须是整数", name)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s不能为负数", name)
	}
	return value, nil
}

func (m *SettingsModel) revertEditor() {
	if m.focus == settingsAPIKey {
		m.editors[m.focus] = ""
		return
	}
	m.editors[m.focus] = m.initialEditor(m.focus)
}

func (m *SettingsModel) resetEditors() {
	for index := 0; index < settingsFieldCount; index++ {
		m.editors[index] = m.initialEditor(index)
	}
	m.editors[settingsAPIKey] = ""
}

func (m *SettingsModel) initialEditor(index int) string {
	switch index {
	case settingsOpenAIBase:
		return m.settings.OpenAIBase
	case settingsAnthropicBase:
		return m.settings.AnthropicBase
	case settingsToolsBase:
		return m.settings.ToolsBase
	case settingsListenPort:
		if _, port, err := net.SplitHostPort(m.settings.ListenAddr); err == nil {
			return port
		}
		return m.settings.ListenAddr
	case settingsRPMLimit:
		return strconv.Itoa(m.settings.RPMLimit)
	case settingsBalanceLow:
		return strconv.FormatFloat(m.settings.BalanceLowThreshold, 'g', -1, 64)
	case settingsRetentionDays:
		return strconv.Itoa(m.settings.RetentionDays)
	case settingsPricingSource:
		return m.settings.PricingSourceURL
	default:
		return ""
	}
}

func (m *SettingsModel) View() string { return renderSettings(m) }

func renderSettings(m *SettingsModel) string {
	if m == nil {
		return ""
	}
	width := maxInt(1, m.width)
	height := maxInt(1, m.height)
	header := newRenderer(width, 2, m.ascii, m.color)
	header.add(toneTitle, " DS-mon 设置")
	header.add(toneMuted, " ↑↓/tab 选择 · enter 编辑 · ctrl+s 保存 · q 退出")
	bodyHeight := maxInt(0, height-4)
	body := newRenderer(width, bodyHeight, m.ascii, m.color)
	for index := 0; index < settingsFieldCount; index++ {
		field := settingsFields[index]
		selected := index == m.focus
		if selected {
			label := field.label
			if field.secret && m.hasKey && m.editors[index] == "" {
				label += "（已保存）"
			}
			body.add(toneAccent, " ▶ %s", label)
			value := m.fieldValue(index)
			if m.editing && index == m.focus {
				value = m.editorText(index)
			}
			body.add(toneSelected, "   %s", value)
			body.add(toneMuted, "   %s", field.help)
			continue
		}
		label := field.short
		body.add(toneNormal, "   %-12s %s", label, m.fieldValue(index))
	}
	status := newRenderer(width, 1, m.ascii, m.color)
	if m.status != "" {
		status.add(m.statusTone, " %s", m.status)
	} else {
		status.add(toneMuted, " 改动只在 ctrl+s 后写入；API key 不显示明文")
	}
	footer := newRenderer(width, 1, m.ascii, m.color)
	if m.editing {
		footer.add(toneMuted, " 输入中  Enter 完成  Tab 下一项  Esc 撤销")
	} else {
		footer.add(toneMuted, " 设置写入 ~/.config/dsmon/config.toml  ·  ctrl+s 保存  ·  q 退出")
	}

	lines := append(header.finish(), body.finish()...)
	lines = append(lines, status.finish()...)
	lines = append(lines, footer.finish()...)
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines, "\n")
}

func (m *SettingsModel) fieldValue(index int) string {
	if index == settingsPricingAuto {
		if m.settings.PricingAutoCheck {
			return "开"
		}
		return "关"
	}
	if index == settingsAPIKey {
		if m.editors[index] != "" {
			return "********"
		}
		if m.hasKey {
			return "********（留空保留）"
		}
		return "未设置（留空保留）"
	}
	return m.editorText(index)
}

func (m *SettingsModel) editorText(index int) string {
	text := m.editors[index]
	if index == settingsAPIKey && text != "" {
		return "********"
	}
	if text == "" {
		return "（空）"
	}
	return tailText(text, maxInt(4, m.width-18))
}

func tailText(text string, width int) string {
	if width <= 0 || lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	used := 0
	start := len(runes)
	for start > 0 {
		next := lipgloss.Width(string(runes[start-1]))
		if used+next > width-1 {
			break
		}
		start--
		used += next
	}
	return "…" + string(runes[start:])
}

func trimLastRune(text string) string {
	if text == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(text)
	return text[:len(text)-size]
}
