package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEditableSettingsDefaults(t *testing.T) {
	clearProxyEnvironment(t)
	configPath, _ := testConfigPaths(t)
	settings, hasKey, err := LoadEditableSettingsFrom(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if hasKey {
		t.Fatal("missing config reported an API key")
	}
	if settings.ListenAddr != DefaultListenAddr || settings.OpenAIBase != DefaultOpenAIBase ||
		settings.AnthropicBase != DefaultAnthropicBase || settings.ToolsBase != DefaultOpenAIBase ||
		settings.BalanceURL != DefaultOpenAIBase+DefaultBalancePath ||
		settings.RetentionDays != DefaultRetentionDays || settings.PricingSourceURL != DefaultPricingSource ||
		!settings.PricingAutoCheck || settings.APIKey != "" {
		t.Fatalf("defaults = %+v", settings)
	}
}

func TestSaveProxySettingsRoundTripAndPermissions(t *testing.T) {
	clearProxyEnvironment(t)
	configPath, keyPath := testConfigPaths(t)
	settings := defaultProxySettings()
	settings.ListenAddr = "127.0.0.1:19090"
	settings.OpenAIBase = "http://127.0.0.1:19001/v1"
	settings.AnthropicBase = "http://127.0.0.1:19002/v1"
	settings.ToolsBase = "http://127.0.0.1:19003/v1"
	settings.RPMLimit = 60
	settings.BalanceURL = "http://127.0.0.1:19004/user/balance"
	settings.BalanceLowThreshold = 12.5
	settings.RetentionDays = 45
	settings.PricingSourceURL = "http://127.0.0.1:19005/pricing"
	settings.PricingAutoCheck = false
	const secret = "sk-round-trip-secret"

	if err := SaveProxySettingsTo(configPath, keyPath, settings, secret); err != nil {
		t.Fatal(err)
	}
	assertMode(t, configPath, 0o600)
	assertMode(t, keyPath, 0o600)

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configText := string(configBytes)
	if strings.Contains(configText, secret) {
		t.Fatal("config file contains the plaintext API key")
	}
	if !strings.Contains(configText, `api_key_enc = "v1:`) {
		t.Fatalf("config has no encrypted key: %s", configText)
	}

	editable, hasKey, err := LoadEditableSettingsFrom(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !hasKey || editable.APIKey != "" {
		t.Fatalf("editable settings leaked or lost key state: %+v, hasKey=%v", editable, hasKey)
	}
	loaded, err := LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != secret || loaded.ListenAddr != settings.ListenAddr ||
		loaded.OpenAIBase != settings.OpenAIBase || loaded.AnthropicBase != settings.AnthropicBase ||
		loaded.ToolsBase != settings.ToolsBase || loaded.RPMLimit != settings.RPMLimit ||
		loaded.BalanceURL != settings.BalanceURL || loaded.BalanceLowThreshold != settings.BalanceLowThreshold ||
		loaded.RetentionDays != settings.RetentionDays ||
		loaded.PricingSourceURL != settings.PricingSourceURL || loaded.PricingAutoCheck {
		t.Fatalf("loaded settings = %+v", loaded)
	}
}

func TestSaveProxySettingsKeepsOrReplacesEncryptedKey(t *testing.T) {
	clearProxyEnvironment(t)
	configPath, keyPath := testConfigPaths(t)
	settings := defaultProxySettings()
	if err := SaveProxySettingsTo(configPath, keyPath, settings, "first-key"); err != nil {
		t.Fatal(err)
	}
	settings.ListenAddr = "127.0.0.1:19091"
	if err := SaveProxySettingsTo(configPath, keyPath, settings, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "first-key" || loaded.ListenAddr != "127.0.0.1:19091" {
		t.Fatalf("blank key did not preserve the old secret: %+v", loaded)
	}

	if err := SaveProxySettingsTo(configPath, keyPath, settings, "second-key"); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "second-key" {
		t.Fatal("new API key did not replace the old one")
	}
}

func TestLoadProxySettingsEnvironmentOverridesFileAndKeyPrecedence(t *testing.T) {
	clearProxyEnvironment(t)
	configPath, keyPath := testConfigPaths(t)
	settings := defaultProxySettings()
	if err := SaveProxySettingsTo(configPath, keyPath, settings, "config-key"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSMON_LISTEN_ADDR", "127.0.0.1:19100")
	t.Setenv("DSMON_OPENAI_BASE", "http://127.0.0.1:19101")
	t.Setenv("DSMON_ANTHROPIC_BASE", "http://127.0.0.1:19102")
	t.Setenv("DSMON_TOOLS_BASE", "http://127.0.0.1:19103")
	t.Setenv("DSMON_BALANCE_URL", "http://127.0.0.1:19104/user/balance")
	t.Setenv("DSMON_RPM_LIMIT", "7")
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-key")
	loaded, err := LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "deepseek-key" || loaded.ListenAddr != "127.0.0.1:19100" ||
		loaded.OpenAIBase != "http://127.0.0.1:19101" ||
		loaded.AnthropicBase != "http://127.0.0.1:19102" ||
		loaded.ToolsBase != "http://127.0.0.1:19103" ||
		loaded.BalanceURL != "http://127.0.0.1:19104/user/balance" || loaded.RPMLimit != 7 {
		t.Fatalf("environment overrides = %+v", loaded)
	}
	t.Setenv("DSMON_API_KEY", "dsmon-key")
	loaded, err = LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "dsmon-key" {
		t.Fatal("DSMON_API_KEY did not take precedence over DEEPSEEK_API_KEY")
	}
}

func TestLoadProxySettingsCredentialFallbackWithoutFileKey(t *testing.T) {
	clearProxyEnvironment(t)
	configPath, keyPath := testConfigPaths(t)
	if err := SaveProxySettingsTo(configPath, keyPath, defaultProxySettings(), ""); err != nil {
		t.Fatal(err)
	}
	credentialDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credentialDir, apiKeyCredentialName), []byte("credential-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDir)
	loaded, err := LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIKey != "credential-key" {
		t.Fatal("systemd credential fallback was not used")
	}
}

func TestLoadProxySettingsRejectsMalformedFile(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "unknown section", data: "[wat]\nvalue = 1\n", want: "unknown section"},
		{name: "unknown key", data: "[proxy]\nwat = 1\n", want: "unknown key"},
		{name: "wrong type", data: "[proxy]\nrpm_limit = \"60\"\n", want: "expects an integer"},
		{name: "duplicate key", data: "[proxy]\nrpm_limit = 1\nrpm_limit = 2\n", want: "duplicate key"},
		{name: "unterminated string", data: "[proxy]\nlisten = \"127.0.0.1:18080\n", want: "invalid quoted string"},
		{name: "bad port", data: "[proxy]\nlisten = \"127.0.0.1:70000\"\n", want: "监听端口"},
		{name: "non-loopback", data: "[proxy]\nlisten = \"0.0.0.0:18080\"\n", want: "127.0.0.1"},
		{name: "bad URL", data: "[proxy]\nopenai_base = \"not-a-url\"\n", want: "OpenAI base URL"},
		{name: "bad retention", data: "[storage]\nretention_days = 3651\n", want: "保留天数"},
		{name: "bad threshold", data: "[balance]\nlow_threshold = -1\n", want: "余额阈值"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearProxyEnvironment(t)
			t.Setenv("DSMON_API_KEY", "test-key")
			configPath, keyPath := testConfigPaths(t)
			if err := os.WriteFile(configPath, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProxySettingsFrom(configPath, keyPath); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSaveProxySettingsNeverOverwritesInvalidFile(t *testing.T) {
	clearProxyEnvironment(t)
	configPath, keyPath := testConfigPaths(t)
	valid := defaultProxySettings()
	if err := SaveProxySettingsTo(configPath, keyPath, valid, "key"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.ListenAddr = "0.0.0.0:18080"
	if err := SaveProxySettingsTo(configPath, keyPath, invalid, "key"); err == nil {
		t.Fatal("invalid listen address was accepted")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid settings changed the existing config")
	}

	corrupt := []byte("[proxy]\nunknown = true\n")
	if err := os.WriteFile(configPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveProxySettingsTo(configPath, keyPath, valid, "key"); err == nil {
		t.Fatal("corrupt config was overwritten")
	}
	after, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(corrupt) {
		t.Fatal("corrupt config changed after a failed save")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
