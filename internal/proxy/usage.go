package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
)

type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
}

func ExtractUsage(body []byte) (TokenUsage, bool) {
	if len(body) == 0 {
		return TokenUsage{}, false
	}
	var jsonObject map[string]any
	if json.Unmarshal(body, &jsonObject) == nil {
		merged := map[string]any{}
		MergeUsageObject(jsonObject, merged)
		usage, ok := TokenUsageFromMap(merged)
		return usage, ok
	}
	merged := map[string]any{}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := line[len("data: "):]
		if bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var object map[string]any
		if json.Unmarshal(payload, &object) != nil {
			continue
		}
		MergeUsageObject(object, merged)
	}
	usage, ok := TokenUsageFromMap(merged)
	return usage, ok
}

func MergeUsageObject(object map[string]any, merged map[string]any) {
	candidates := []any{
		object["usage"],
		asObject(asObject(object["response"])["usage"]),
		asObject(asObject(object["message"])["usage"]),
	}
	if response, ok := object["response"].(map[string]any); ok {
		candidates[1] = response["usage"]
	}
	if message, ok := object["message"].(map[string]any); ok {
		candidates[2] = message["usage"]
	}
	for _, candidate := range candidates {
		usage, ok := candidate.(map[string]any)
		if !ok || !looksLikeUsage(usage) {
			continue
		}
		for key, value := range usage {
			number, isNumber := jsonNumber(value)
			if isNumber && number > 0 {
				merged[key] = number
				continue
			}
			if _, exists := merged[key]; !exists {
				merged[key] = value
			}
		}
	}
}

func TokenUsageFromMap(values map[string]any) (TokenUsage, bool) {
	if !looksLikeUsage(values) {
		return TokenUsage{}, false
	}
	prompt := firstNumber(values, "prompt_tokens", "input_tokens")
	completion := firstNumber(values, "completion_tokens", "output_tokens")
	total := firstNumber(values, "total_tokens")
	if total == 0 {
		total = prompt + completion
	}
	return TokenUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		CachedTokens:     CachedTokens(values),
		ReasoningTokens:  ReasoningTokens(values),
	}, true
}

func CachedTokens(values map[string]any) int {
	if details := asObject(values["prompt_tokens_details"]); details != nil {
		if value := firstNumber(details, "cached_tokens"); value != 0 {
			return value
		}
	}
	if value := firstNumber(values, "prompt_cache_hit_tokens"); value != 0 {
		return value
	}
	if details := asObject(values["input_tokens_details"]); details != nil {
		if value := firstNumber(details, "cached_tokens"); value != 0 {
			return value
		}
	}
	return firstNumber(values, "cache_read_input_tokens")
}

func ReasoningTokens(values map[string]any) int {
	if details := asObject(values["completion_tokens_details"]); details != nil {
		if value := firstNumber(details, "reasoning_tokens"); value != 0 {
			return value
		}
	}
	if details := asObject(values["output_tokens_details"]); details != nil {
		return firstNumber(details, "reasoning_tokens")
	}
	return 0
}

func looksLikeUsage(values map[string]any) bool {
	for _, key := range []string{"prompt_tokens", "input_tokens", "completion_tokens", "output_tokens"} {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}

func firstNumber(values map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if number, ok := jsonNumber(value); ok {
				return number
			}
		}
	}
	return 0
}

func jsonNumber(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		return int(number), true
	case float32:
		return int(number), true
	case int:
		return number, true
	case int64:
		return int(number), true
	case json.Number:
		value, err := number.Int64()
		return int(value), err == nil
	default:
		return 0, false
	}
}

func asObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	if object == nil {
		return map[string]any{}
	}
	return object
}

func RequestModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &payload) != nil || strings.TrimSpace(payload.Model) == "" {
		return "unknown"
	}
	return payload.Model
}
