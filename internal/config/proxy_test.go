package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProxySettingsDefaults(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("DEEPSEEK_API_KEY", "sk-test-only")
	configPath, keyPath := testConfigPaths(t)
	settings, err := LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ListenAddr != DefaultListenAddr || settings.OpenAIBase != DefaultOpenAIBase ||
		settings.AnthropicBase != DefaultAnthropicBase || settings.ToolsBase != DefaultOpenAIBase ||
		settings.APIPath != DefaultAPIPath || settings.ProviderID != DefaultProviderID ||
		settings.AuthPrefix != DefaultAuthPrefix || settings.APIKey != "sk-test-only" ||
		settings.RPMLimit != 0 || settings.BalanceURL != DefaultOpenAIBase+DefaultBalancePath ||
		settings.RetentionDays != DefaultRetentionDays || !settings.PricingAutoCheck ||
		settings.PricingSourceURL != DefaultPricingSource {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestLoadProxySettingsEnvironment(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("DSMON_API_KEY", "dsmon-key")
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-key")
	t.Setenv("DSMON_LISTEN_ADDR", "127.0.0.1:19090")
	t.Setenv("DSMON_UPSTREAM_BASE", "http://127.0.0.1:19001")
	t.Setenv("DSMON_ANTHROPIC_BASE", "http://127.0.0.1:19002/anthropic")
	t.Setenv("DSMON_BALANCE_URL", "http://127.0.0.1:19003/user/balance")
	t.Setenv("DSMON_RPM_LIMIT", "60")
	configPath, keyPath := testConfigPaths(t)
	settings, err := LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if settings.APIKey != "dsmon-key" || settings.ListenAddr != "127.0.0.1:19090" ||
		settings.OpenAIBase != "http://127.0.0.1:19001" ||
		settings.ToolsBase != "http://127.0.0.1:19001" ||
		settings.AnthropicBase != "http://127.0.0.1:19002/anthropic" ||
		settings.BalanceURL != "http://127.0.0.1:19003/user/balance" || settings.RPMLimit != 60 {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestLoadProxySettingsRejectsMissingKeyAndBadLimit(t *testing.T) {
	clearProxyEnvironment(t)
	configPath, keyPath := testConfigPaths(t)
	if _, err := LoadProxySettingsFrom(configPath, keyPath); err == nil {
		t.Fatal("missing key was accepted")
	}
	t.Setenv("DSMON_API_KEY", "key")
	t.Setenv("DSMON_RPM_LIMIT", "-1")
	if _, err := LoadProxySettingsFrom(configPath, keyPath); err == nil {
		t.Fatal("negative RPM limit was accepted")
	}
}

func TestLoadProxySettingsReadsSystemdCredential(t *testing.T) {
	clearProxyEnvironment(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, apiKeyCredentialName), []byte("credential-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	configPath, keyPath := testConfigPaths(t)
	settings, err := LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if settings.APIKey != "credential-key" {
		t.Fatalf("API key was not loaded from the credential directory")
	}
}

func TestLoadProxySettingsPrefersEnvironmentOverCredential(t *testing.T) {
	clearProxyEnvironment(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, apiKeyCredentialName), []byte("credential-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	t.Setenv("DSMON_API_KEY", "environment-key")
	configPath, keyPath := testConfigPaths(t)
	settings, err := LoadProxySettingsFrom(configPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if settings.APIKey != "environment-key" {
		t.Fatalf("environment did not take precedence")
	}
}

func TestLoadProxySettingsReportsCredentialReadFailure(t *testing.T) {
	clearProxyEnvironment(t)
	t.Setenv("CREDENTIALS_DIRECTORY", t.TempDir())
	configPath, keyPath := testConfigPaths(t)
	if _, err := LoadProxySettingsFrom(configPath, keyPath); err == nil {
		t.Fatal("missing credential was accepted")
	}
}

func testConfigPaths(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "config.toml"), filepath.Join(dir, "enc_key")
}

func clearProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"DSMON_API_KEY", "DEEPSEEK_API_KEY", "DSMON_LISTEN_ADDR", "DSMON_OPENAI_BASE",
		"DSMON_UPSTREAM_BASE", "DSMON_ANTHROPIC_BASE", "DSMON_TOOLS_BASE", "DSMON_API_PATH",
		"DSMON_PROVIDER_ID", "DSMON_AUTH_PREFIX", "DSMON_RPM_LIMIT", "DSMON_BALANCE_URL",
		"CREDENTIALS_DIRECTORY",
	} {
		t.Setenv(name, "")
	}
}
