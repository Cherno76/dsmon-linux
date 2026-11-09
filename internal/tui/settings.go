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
	"dsmon/internal/version"
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
	{label: "API key", short: "API key", help: "Leave blank to keep the current key; input is masked and AES-GCM encrypted on save", secret: true},
	{label: "OpenAI base URL", short: "OpenAI", help: "Absolute http(s) URL for the OpenAI-compatible API"},
	{label: "Anthropic base URL", short: "Anthropic", help: "Absolute http(s) URL for the Anthropic Messages API"},
	{label: "Tools base URL", short: "Tools", help: "URL for the Search / Fetch tool endpoints"},
	{label: "Listen port", short: "Port", help: "Always binds to 127.0.0.1; valid range is 1–65535"},
	{label: "RPM limit", short: "RPM", help: "Per-provider 60-second request limit; 0 disables it"},
	{label: "Balance low threshold", short: "Low balance", help: "Balance turns yellow below this value; 0 disables it"},
	{label: "Retention days", short: "Retention", help: "0 keeps data forever; maximum is 3650 days"},
	{label: "Pricing source URL", short: "Pricing URL", help: "Used by the Phase 6 pricing-page fetch; updates only await confirmation"},
	{label: "Auto-check pricing", short: "Auto-check", help: "Checks every 7 days and only creates updates awaiting confirmation", toggle: true},
}

type SettingsModel struct {
	client *ipc.Client
	ascii  bool
	color  bool
	theme  palette

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
		theme:    loadPalette(),
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
		return fmt.Errorf("load settings: %w", err)
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
			m.status = "Save failed: " + msg.saveErr.Error()
			m.statusTone = toneError
			return m, nil
		}
		m.settings = msg.settings
		m.hasKey = msg.hadKey || msg.newKey
		m.resetEditors()
		m.editing = false
		if msg.reloadErr != nil {
			if reloadOffline(msg.reloadErr) {
				m.status = "Saved; daemon offline, applies on next start"
				m.statusTone = toneWarn
			} else {
				m.status = "Saved, but hot reload failed: " + msg.reloadErr.Error()
				m.statusTone = toneError
			}
			return m, nil
		}
		m.status = "Saved and hot-reloaded"
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
			m.status = "Pricing auto-check toggled; not saved yet"
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
		m.status = "Validation failed: " + err.Error()
		m.statusTone = toneError
		return nil
	}
	m.saving = true
	m.status = "Saving..."
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
	rpm, err := parseNonNegativeInt(m.editors[settingsRPMLimit], "RPM limit")
	if err != nil {
		return config.ProxySettings{}, "", err
	}
	settings.RPMLimit = rpm
	threshold, err := strconv.ParseFloat(strings.TrimSpace(m.editors[settingsBalanceLow]), 64)
	if err != nil {
		return config.ProxySettings{}, "", errors.New("balance threshold must be a number")
	}
	settings.BalanceLowThreshold = threshold
	retention, err := parseNonNegativeInt(m.editors[settingsRetentionDays], "retention days")
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
			return 0, fmt.Errorf("invalid listen port: %w", err)
		}
		if host != "127.0.0.1" {
			return 0, errors.New("listen address must use 127.0.0.1")
		}
		raw = port
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("listen port must be 1–65535")
	}
	return port, nil
}

func parseNonNegativeInt(raw, name string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s cannot be negative", name)
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
	header := newRenderer(width, 2, m.ascii, m.color, m.theme)
	header.add(toneTitle, " DS-mon %s Settings", version.Current)
	header.add(toneMuted, " ↑↓/tab select · enter edit · ctrl+s save · q quit")
	bodyHeight := maxInt(0, height-4)
	body := newRenderer(width, bodyHeight, m.ascii, m.color, m.theme)
	for index := 0; index < settingsFieldCount; index++ {
		field := settingsFields[index]
		selected := index == m.focus
		if selected {
			label := field.label
			if field.secret && m.hasKey && m.editors[index] == "" {
				label += " (saved)"
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
	status := newRenderer(width, 1, m.ascii, m.color, m.theme)
	if m.status != "" {
		status.add(m.statusTone, " %s", m.status)
	} else {
		status.add(toneMuted, " Changes are written only after ctrl+s; API keys are never shown in plaintext")
	}
	footer := newRenderer(width, 1, m.ascii, m.color, m.theme)
	if m.editing {
		footer.add(toneMuted, " Editing  Enter done  Tab next  Esc revert")
	} else {
		footer.add(toneMuted, " Writes ~/.config/dsmon/config.toml  ·  ctrl+s save  ·  q quit")
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
			return "On"
		}
		return "Off"
	}
	if index == settingsAPIKey {
		if m.editors[index] != "" {
			return "********"
		}
		if m.hasKey {
			return "******** (leave blank to keep)"
		}
		return "Not set (leave blank to keep)"
	}
	return m.editorText(index)
}

func (m *SettingsModel) editorText(index int) string {
	text := m.editors[index]
	if index == settingsAPIKey && text != "" {
		return "********"
	}
	if text == "" {
		return "(empty)"
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
