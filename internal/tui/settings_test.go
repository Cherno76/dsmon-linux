package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"dsmon/internal/config"
	"dsmon/internal/ipc"
)

func settingsFixture() config.ProxySettings {
	return config.ProxySettings{
		ListenAddr:          "127.0.0.1:18080",
		OpenAIBase:          "https://api.deepseek.com",
		AnthropicBase:       "https://api.deepseek.com/anthropic",
		ToolsBase:           "https://api.deepseek.com",
		APIPath:             "/v1",
		ProviderID:          "deepseek",
		AuthPrefix:          "Bearer",
		RPMLimit:            60,
		BalanceURL:          "https://api.deepseek.com/user/balance",
		BalanceLowThreshold: 10,
		RetentionDays:       180,
		PricingSourceURL:    config.DefaultPricingSource,
		PricingAutoCheck:    true,
	}
}

func renderSettingsAt(t *testing.T, model *SettingsModel, width, height int) string {
	t.Helper()
	model.width, model.height = width, height
	output := renderSettings(model)
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

func TestSettingsRenderFitsSizesAndNeverShowsPlaintextKey(t *testing.T) {
	model := NewSettingsModel(nil, true, settingsFixture(), true)
	model.editors[settingsAPIKey] = "sk-never-render-this"
	for _, size := range []struct{ width, height int }{{40, 24}, {80, 24}, {200, 60}} {
		output := renderSettingsAt(t, model, size.width, size.height)
		if strings.Contains(output, "sk-never-render-this") {
			t.Fatalf("%dx%d rendered the plaintext API key", size.width, size.height)
		}
		if !strings.Contains(output, "********") {
			t.Fatalf("%dx%d did not render the key mask", size.width, size.height)
		}
	}
}

func TestSettingsSaveEncryptsKeyAndReportsOfflineReload(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("DSMON_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	client := ipc.NewClient(filepath.Join(root, "missing.sock"))
	model := NewSettingsModel(client, true, settingsFixture(), false)
	model.color = false
	model.updateKey("enter")
	for _, key := range []string{"s", "k", "-", "s", "e", "t", "t", "i", "n", "g"} {
		model.updateKey(key)
	}
	if output := renderSettingsAt(t, model, 80, 24); strings.Contains(output, "sk-setting") {
		t.Fatal("editing rendered the plaintext key")
	}
	_, command := model.updateKey("ctrl+s")
	if command == nil {
		t.Fatalf("ctrl+s returned no command: %s", model.status)
	}
	_, _ = model.Update(command())
	if !strings.Contains(model.status, "Saved") || !strings.Contains(model.status, "next start") {
		t.Fatalf("offline save status = %q", model.status)
	}
	if model.editors[settingsAPIKey] != "" {
		t.Fatal("API key input was not cleared after save")
	}
	configPath := config.ConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-setting") {
		t.Fatal("config file contains the plaintext key")
	}
	loaded, err := config.LoadProxySettingsFrom(configPath, config.EncryptionKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "sk-setting" {
		t.Fatal("saved key did not round-trip")
	}
}

func TestSettingsInvalidPortDoesNotWriteFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("DSMON_API_KEY", "test-key")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	settings := settingsFixture()
	if err := config.SaveProxySettings(settings, "secret-key"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	model := NewSettingsModel(nil, true, settings, true)
	model.color = false
	model.focus = settingsListenPort
	model.editors[settingsListenPort] = "70000"
	_, command := model.updateKey("ctrl+s")
	if command != nil {
		_, _ = model.Update(command())
	}
	if !strings.Contains(model.status, "listen port") {
		t.Fatalf("status = %q", model.status)
	}
	after, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid port changed the config file")
	}
}

func TestSettingsCommitDerivesBalanceURLAndTogglesPricing(t *testing.T) {
	model := NewSettingsModel(nil, true, settingsFixture(), false)
	model.editors[settingsOpenAIBase] = "http://127.0.0.1:19001"
	model.editors[settingsListenPort] = "127.0.0.1:19090"
	model.settings.PricingAutoCheck = false
	settings, _, err := model.commit()
	if err != nil {
		t.Fatal(err)
	}
	if settings.ListenAddr != "127.0.0.1:19090" ||
		settings.BalanceURL != "http://127.0.0.1:19001/user/balance" ||
		settings.PricingAutoCheck {
		t.Fatalf("committed settings = %+v", settings)
	}
}
