package store

import (
	"regexp"
	"strings"
)

const (
	// ClientUnknown 表示没有 UA 或无法识别的客户端。
	ClientUnknown = "未知"
	// ClientBrowser 是伪装成浏览器但没有产品名的 UA 的兜底桶。
	ClientBrowser = "浏览器"
	// ClientRuleVersion 改动识别规则时必须加一，启动维护会据此重推导历史。
	ClientRuleVersion = 3
	// ClientInferenceVersion 改动无 UA 历史的反推规则时必须加一。
	ClientInferenceVersion = 2
)

var trailingVersion = regexp.MustCompile(`[-_.][0-9]+$`)

var knownClients = []struct {
	needle string
	name   string
}{
	{"opencode", "opencode"},
	{"codex desktop", "codex"},
	{"codewhale", "codewhale"},
}

// HiddenClients 不应该出现在筛选列表里，但数据与费用统计照常保留。
var HiddenClients = map[string]bool{"xctest": true}

// ClientFromUserAgent 把带版本号、运行时信息的 UA 归一化成一个稳定客户端名。
func ClientFromUserAgent(raw string) string {
	ua := strings.TrimSpace(raw)
	if ua == "" {
		return ClientUnknown
	}
	lower := strings.ToLower(ua)
	for _, known := range knownClients {
		if strings.Contains(lower, known.needle) {
			return known.name
		}
	}
	if strings.HasPrefix(lower, "mozilla/") {
		if product, ok := compatibilityProduct(ua); ok {
			return product
		}
		return ClientBrowser
	}
	if slash := strings.IndexByte(ua, '/'); slash >= 0 {
		name := strings.TrimSpace(ua[:slash])
		if name != "" {
			return canonicalClient(name)
		}
	}
	return canonicalClient(ua)
}

func compatibilityProduct(ua string) (string, bool) {
	index := strings.Index(strings.ToLower(ua), "compatible;")
	if index < 0 {
		return "", false
	}
	for _, chunk := range strings.Split(ua[index+len("compatible;"):], ";") {
		text := strings.TrimSpace(chunk)
		slash := strings.IndexByte(text, '/')
		if slash < 0 {
			continue
		}
		name := strings.TrimSpace(text[:slash])
		if name != "" {
			return canonicalClient(name), true
		}
	}
	return "", false
}

func canonicalClient(name string) string {
	text := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(text, "codex") {
		return "codex"
	}
	if match := trailingVersion.FindStringIndex(text); match != nil && match[0] > 0 {
		text = text[:match[0]]
	}
	runes := []rune(text)
	if len(runes) > 40 {
		text = string(runes[:40])
	}
	return text
}
