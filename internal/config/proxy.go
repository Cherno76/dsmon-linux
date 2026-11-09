package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultListenAddr    = "127.0.0.1:18080"
	DefaultOpenAIBase    = "https://api.deepseek.com"
	DefaultAnthropicBase = "https://api.deepseek.com/anthropic"
	DefaultProviderID    = "deepseek"
	DefaultAPIPath       = "/v1"
	DefaultAuthPrefix    = "Bearer"
	DefaultBalancePath   = "/user/balance"
	apiKeyCredentialName = "dsmon-api-key"
)

// ProxySettings 是 daemon 的运行配置。APIKey 只在内存中保存，落盘时写入密文。
type ProxySettings struct {
	ListenAddr          string
	OpenAIBase          string
	AnthropicBase       string
	ToolsBase           string
	APIPath             string
	ProviderID          string
	AuthPrefix          string
	APIKey              string
	RPMLimit            int
	BalanceURL          string
	BalanceLowThreshold float64
	RetentionDays       int
	PricingSourceURL    string
	PricingAutoCheck    bool
	ActivityCardEnabled bool
}

func LoadProxySettings() (ProxySettings, error) {
	return LoadProxySettingsFrom(ConfigPath(), EncryptionKeyPath())
}

func LoadProxySettingsFrom(path, keyPath string) (ProxySettings, error) {
	file, err := loadSettingsFile(path)
	if err != nil {
		return ProxySettings{}, err
	}
	settings := file.settings
	settings.ListenAddr = envOrDefault("DSMON_LISTEN_ADDR", settings.ListenAddr)
	settings.OpenAIBase = firstEnvOrDefault([]string{"DSMON_OPENAI_BASE", "DSMON_UPSTREAM_BASE"}, settings.OpenAIBase)
	settings.AnthropicBase = envOrDefault("DSMON_ANTHROPIC_BASE", settings.AnthropicBase)
	settings.ToolsBase = envOrDefault("DSMON_TOOLS_BASE", settings.ToolsBase)
	settings.APIPath = envOrDefault("DSMON_API_PATH", settings.APIPath)
	settings.ProviderID = envOrDefault("DSMON_PROVIDER_ID", settings.ProviderID)
	settings.AuthPrefix = envOrDefault("DSMON_AUTH_PREFIX", settings.AuthPrefix)
	settings.BalanceURL = envOrDefault("DSMON_BALANCE_URL", settings.BalanceURL)
	if raw := strings.TrimSpace(os.Getenv("DSMON_RPM_LIMIT")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return ProxySettings{}, errors.New("DSMON_RPM_LIMIT must be a non-negative integer")
		}
		settings.RPMLimit = limit
	}
	settings = normalizeSettings(settings)
	apiKey, err := resolveAPIKey(file.apiKeyEnc, keyPath)
	if err != nil {
		return ProxySettings{}, err
	}
	settings.APIKey = apiKey
	if err := ValidateProxySettings(settings); err != nil {
		return ProxySettings{}, err
	}
	return settings, nil
}

func resolveAPIKey(apiKeyEnc, keyPath string) (string, error) {
	if apiKey := firstEnv("DSMON_API_KEY", "DEEPSEEK_API_KEY"); apiKey != "" {
		return apiKey, nil
	}
	if apiKeyEnc != "" {
		key, err := loadKey(keyPath)
		if err != nil {
			return "", err
		}
		apiKey, err := decryptSecret(key, apiKeyEnc)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(apiKey) == "" {
			return "", errors.New("encrypted API key is empty")
		}
		return strings.TrimSpace(apiKey), nil
	}
	credentialDir := strings.TrimSpace(os.Getenv("CREDENTIALS_DIRECTORY"))
	if credentialDir == "" {
		return "", errors.New("API key is not set; save it in `dsmon settings` or provide DSMON_API_KEY")
	}
	raw, err := os.ReadFile(filepath.Join(credentialDir, apiKeyCredentialName))
	if err != nil {
		return "", fmt.Errorf("read systemd API key credential: %w", err)
	}
	apiKey := strings.TrimSpace(string(raw))
	if apiKey == "" {
		return "", errors.New("systemd API key credential is empty")
	}
	return apiKey, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func firstEnvOrDefault(names []string, fallback string) string {
	if value := firstEnv(names...); value != "" {
		return value
	}
	return fallback
}
