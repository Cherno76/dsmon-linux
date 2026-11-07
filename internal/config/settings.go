package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultRetentionDays  = 180
	DefaultPricingSource  = "https://api-docs.deepseek.com/quick_start/pricing"
	MaxRetentionDays      = 3650
	configFilePermissions = 0o600
)

type fileSettings struct {
	settings  ProxySettings
	apiKeyEnc string
}

func defaultProxySettings() ProxySettings {
	return ProxySettings{
		ListenAddr:          DefaultListenAddr,
		OpenAIBase:          DefaultOpenAIBase,
		AnthropicBase:       DefaultAnthropicBase,
		ToolsBase:           "",
		APIPath:             DefaultAPIPath,
		ProviderID:          DefaultProviderID,
		AuthPrefix:          DefaultAuthPrefix,
		BalanceURL:          "",
		RetentionDays:       DefaultRetentionDays,
		PricingSourceURL:    DefaultPricingSource,
		PricingAutoCheck:    true,
		BalanceLowThreshold: 0,
	}
}

// LoadEditableSettings 读取设置文件本身，不叠加环境变量，也不返回明文 key。
func LoadEditableSettings() (ProxySettings, bool, error) {
	return LoadEditableSettingsFrom(ConfigPath())
}

func LoadEditableSettingsFrom(path string) (ProxySettings, bool, error) {
	file, err := loadSettingsFile(path)
	if err != nil {
		return ProxySettings{}, false, err
	}
	return normalizeSettings(file.settings), file.apiKeyEnc != "", nil
}

// SaveProxySettings 原子写入设置。newAPIKey 为空时保留已加密的 key。
func SaveProxySettings(settings ProxySettings, newAPIKey string) error {
	return SaveProxySettingsTo(ConfigPath(), EncryptionKeyPath(), settings, newAPIKey)
}

func SaveProxySettingsTo(path, keyPath string, settings ProxySettings, newAPIKey string) error {
	settings = normalizeSettings(settings)
	settings.APIKey = ""
	if err := ValidateProxySettings(settings); err != nil {
		return err
	}
	existing, err := loadSettingsFile(path)
	if err != nil {
		return err
	}
	apiKeyEnc := existing.apiKeyEnc
	if strings.TrimSpace(newAPIKey) != "" {
		key, err := loadOrCreateKey(keyPath)
		if err != nil {
			return err
		}
		apiKeyEnc, err = encryptSecret(key, strings.TrimSpace(newAPIKey))
		if err != nil {
			return err
		}
	}
	return atomicWriteFile(path, marshalSettings(settings, apiKeyEnc), configFilePermissions)
}

func loadSettingsFile(path string) (fileSettings, error) {
	result := fileSettings{settings: defaultProxySettings()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return fileSettings{}, fmt.Errorf("read settings: %w", err)
	}
	parsed, err := parseSettingsTOML(data)
	if err != nil {
		return fileSettings{}, fmt.Errorf("parse settings %s: %w", path, err)
	}
	return parsed, nil
}

func normalizeSettings(settings ProxySettings) ProxySettings {
	settings.ListenAddr = strings.TrimSpace(settings.ListenAddr)
	settings.OpenAIBase = strings.TrimSpace(settings.OpenAIBase)
	settings.AnthropicBase = strings.TrimSpace(settings.AnthropicBase)
	settings.ToolsBase = strings.TrimSpace(settings.ToolsBase)
	settings.APIPath = strings.TrimSpace(settings.APIPath)
	settings.ProviderID = strings.TrimSpace(settings.ProviderID)
	settings.AuthPrefix = strings.TrimSpace(settings.AuthPrefix)
	settings.BalanceURL = strings.TrimSpace(settings.BalanceURL)
	settings.PricingSourceURL = strings.TrimSpace(settings.PricingSourceURL)
	if settings.ListenAddr == "" {
		settings.ListenAddr = DefaultListenAddr
	}
	if settings.OpenAIBase == "" {
		settings.OpenAIBase = DefaultOpenAIBase
	}
	if settings.AnthropicBase == "" {
		settings.AnthropicBase = DefaultAnthropicBase
	}
	if settings.ToolsBase == "" {
		settings.ToolsBase = settings.OpenAIBase
	}
	if settings.APIPath == "" {
		settings.APIPath = DefaultAPIPath
	}
	if settings.ProviderID == "" {
		settings.ProviderID = DefaultProviderID
	}
	if settings.AuthPrefix == "" {
		settings.AuthPrefix = DefaultAuthPrefix
	}
	if settings.BalanceURL == "" {
		settings.BalanceURL = strings.TrimRight(settings.OpenAIBase, "/") + DefaultBalancePath
	}
	if settings.RetentionDays < 0 {
		settings.RetentionDays = 0
	}
	if settings.PricingSourceURL == "" {
		settings.PricingSourceURL = DefaultPricingSource
	}
	return settings
}

func ValidateProxySettings(settings ProxySettings) error {
	settings = normalizeSettings(settings)
	host, port, err := net.SplitHostPort(settings.ListenAddr)
	if err != nil {
		return fmt.Errorf("监听地址格式错误: %w", err)
	}
	if host != "127.0.0.1" {
		return errors.New("监听地址必须使用 127.0.0.1")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("监听端口必须是 1–65535")
	}
	for _, item := range []struct {
		name string
		raw  string
	}{
		{"OpenAI base URL", settings.OpenAIBase},
		{"Anthropic base URL", settings.AnthropicBase},
		{"Tools base URL", settings.ToolsBase},
		{"余额 URL", settings.BalanceURL},
	} {
		if err := validateHTTPURL(item.raw); err != nil {
			return fmt.Errorf("%s: %w", item.name, err)
		}
	}
	if settings.PricingSourceURL != "" {
		if err := validateHTTPURL(settings.PricingSourceURL); err != nil {
			return fmt.Errorf("计费源 URL: %w", err)
		}
	}
	if !strings.HasPrefix(settings.APIPath, "/") {
		return errors.New("API path 必须以 / 开头")
	}
	if settings.ProviderID == "" || settings.AuthPrefix == "" {
		return errors.New("provider id 和鉴权前缀不能为空")
	}
	if settings.RPMLimit < 0 {
		return errors.New("RPM 上限不能为负数")
	}
	if settings.RetentionDays < 0 || settings.RetentionDays > MaxRetentionDays {
		return fmt.Errorf("保留天数必须在 0–%d 之间", MaxRetentionDays)
	}
	if settings.BalanceLowThreshold < 0 || math.IsNaN(settings.BalanceLowThreshold) || math.IsInf(settings.BalanceLowThreshold, 0) {
		return errors.New("余额阈值必须是非负有限数")
	}
	return nil
}

func validateHTTPURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("解析 URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%q 必须是绝对 http(s) URL", raw)
	}
	return nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return fmt.Errorf("set settings permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync settings: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close settings: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}

func marshalSettings(settings ProxySettings, apiKeyEnc string) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("# dsmon settings. API key is encrypted; never commit this file.\n")
	buffer.WriteString("[proxy]\n")
	fmt.Fprintf(&buffer, "listen = %s\n", strconv.Quote(settings.ListenAddr))
	fmt.Fprintf(&buffer, "openai_base = %s\n", strconv.Quote(settings.OpenAIBase))
	fmt.Fprintf(&buffer, "anthropic_base = %s\n", strconv.Quote(settings.AnthropicBase))
	fmt.Fprintf(&buffer, "tools_base = %s\n", strconv.Quote(settings.ToolsBase))
	fmt.Fprintf(&buffer, "api_path = %s\n", strconv.Quote(settings.APIPath))
	fmt.Fprintf(&buffer, "provider_id = %s\n", strconv.Quote(settings.ProviderID))
	fmt.Fprintf(&buffer, "auth_prefix = %s\n", strconv.Quote(settings.AuthPrefix))
	fmt.Fprintf(&buffer, "rpm_limit = %d\n", settings.RPMLimit)
	fmt.Fprintf(&buffer, "api_key_enc = %s\n\n", strconv.Quote(apiKeyEnc))
	buffer.WriteString("[balance]\n")
	fmt.Fprintf(&buffer, "url = %s\n", strconv.Quote(settings.BalanceURL))
	fmt.Fprintf(&buffer, "low_threshold = %s\n\n", strconv.FormatFloat(settings.BalanceLowThreshold, 'g', -1, 64))
	buffer.WriteString("[storage]\n")
	fmt.Fprintf(&buffer, "retention_days = %d\n\n", settings.RetentionDays)
	buffer.WriteString("[pricing]\n")
	fmt.Fprintf(&buffer, "source_url = %s\n", strconv.Quote(settings.PricingSourceURL))
	fmt.Fprintf(&buffer, "auto_check = %t\n", settings.PricingAutoCheck)
	return buffer.Bytes()
}

func parseSettingsTOML(data []byte) (fileSettings, error) {
	result := fileSettings{settings: defaultProxySettings()}
	seen := map[string]bool{}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(stripTOMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			switch section {
			case "proxy", "balance", "storage", "pricing":
			default:
				return fileSettings{}, fmt.Errorf("line %d: unknown section [%s]", lineNumber, section)
			}
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			return fileSettings{}, fmt.Errorf("line %d: expected key = value", lineNumber)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		raw = strings.TrimSpace(raw)
		identity := section + "." + key
		if seen[identity] {
			return fileSettings{}, fmt.Errorf("line %d: duplicate key %s", lineNumber, identity)
		}
		seen[identity] = true
		value, err := parseTOMLValue(raw)
		if err != nil {
			return fileSettings{}, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if err := applySettingValue(&result, section, key, value); err != nil {
			return fileSettings{}, fmt.Errorf("line %d: %w", lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fileSettings{}, err
	}
	return result, nil
}

func applySettingValue(result *fileSettings, section, key string, value any) error {
	identity := section + "." + key
	asString := func() (string, error) {
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("%s expects a string", identity)
		}
		return text, nil
	}
	asInt := func() (int, error) {
		number, ok := value.(int)
		if !ok {
			return 0, fmt.Errorf("%s expects an integer", identity)
		}
		return number, nil
	}
	asFloat := func() (float64, error) {
		switch number := value.(type) {
		case float64:
			return number, nil
		case int:
			return float64(number), nil
		default:
			return 0, fmt.Errorf("%s expects a number", identity)
		}
	}
	asBool := func() (bool, error) {
		flag, ok := value.(bool)
		if !ok {
			return false, fmt.Errorf("%s expects true or false", identity)
		}
		return flag, nil
	}
	var err error
	switch identity {
	case "proxy.listen":
		result.settings.ListenAddr, err = asString()
	case "proxy.openai_base":
		result.settings.OpenAIBase, err = asString()
	case "proxy.anthropic_base":
		result.settings.AnthropicBase, err = asString()
	case "proxy.tools_base":
		result.settings.ToolsBase, err = asString()
	case "proxy.api_path":
		result.settings.APIPath, err = asString()
	case "proxy.provider_id":
		result.settings.ProviderID, err = asString()
	case "proxy.auth_prefix":
		result.settings.AuthPrefix, err = asString()
	case "proxy.rpm_limit":
		result.settings.RPMLimit, err = asInt()
	case "proxy.api_key_enc":
		result.apiKeyEnc, err = asString()
	case "balance.url":
		result.settings.BalanceURL, err = asString()
	case "balance.low_threshold":
		result.settings.BalanceLowThreshold, err = asFloat()
	case "storage.retention_days":
		result.settings.RetentionDays, err = asInt()
	case "pricing.source_url":
		result.settings.PricingSourceURL, err = asString()
	case "pricing.auto_check":
		result.settings.PricingAutoCheck, err = asBool()
	default:
		return fmt.Errorf("unknown key %s.%s", section, key)
	}
	return err
}

func parseTOMLValue(raw string) (any, error) {
	if raw == "" {
		return nil, errors.New("empty value")
	}
	if strings.HasPrefix(raw, "\"") {
		value, err := strconv.Unquote(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid quoted string: %w", err)
		}
		return value, nil
	}
	if strings.HasPrefix(raw, "'") {
		if len(raw) < 2 || !strings.HasSuffix(raw, "'") {
			return nil, errors.New("unterminated literal string")
		}
		return raw[1 : len(raw)-1], nil
	}
	if raw == "true" {
		return true, nil
	}
	if raw == "false" {
		return false, nil
	}
	if number, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return int(number), nil
	}
	if number, err := strconv.ParseFloat(raw, 64); err == nil {
		return number, nil
	}
	return nil, fmt.Errorf("unsupported value %q", raw)
}

func stripTOMLComment(line string) string {
	inDouble, inSingle, escaped := false, false, false
	for index, value := range line {
		switch {
		case escaped:
			escaped = false
		case value == '\\' && inDouble:
			escaped = true
		case value == '"' && !inSingle:
			inDouble = !inDouble
		case value == '\'' && !inDouble:
			inSingle = !inSingle
		case value == '#' && !inDouble && !inSingle:
			return line[:index]
		}
	}
	return line
}
