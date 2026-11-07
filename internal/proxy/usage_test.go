package proxy

import "testing"

func TestExtractPlainUsage(t *testing.T) {
	usage, ok := ExtractUsage([]byte(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	if !ok || usage.PromptTokens != 10 || usage.CompletionTokens != 2 || usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, ok=%v", usage, ok)
	}
	if _, ok := ExtractUsage([]byte(`{"choices":[]}`)); ok {
		t.Fatal("usage missing but extraction succeeded")
	}
	if _, ok := ExtractUsage([]byte("not json")); ok {
		t.Fatal("invalid body produced usage")
	}
	if _, ok := ExtractUsage(nil); ok {
		t.Fatal("empty body produced usage")
	}
}

func TestExtractSSEUsage(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20}}\n\n" +
		"data: [DONE]\n"
	usage, ok := ExtractUsage([]byte(body))
	if !ok || usage.PromptTokens != 100 || usage.CompletionTokens != 20 {
		t.Fatalf("usage = %+v, ok=%v", usage, ok)
	}
}

func TestExtractNestedAndAnthropicUsage(t *testing.T) {
	nested, ok := ExtractUsage([]byte(`data: {"response":{"usage":{"input_tokens":7,"output_tokens":3}}}`))
	if !ok || nested.PromptTokens != 7 || nested.CompletionTokens != 3 {
		t.Fatalf("nested usage = %+v, ok=%v", nested, ok)
	}
	anthropic := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":321,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":45}}\n"
	usage, ok := ExtractUsage([]byte(anthropic))
	if !ok || usage.PromptTokens != 321 || usage.CompletionTokens != 45 {
		t.Fatalf("anthropic usage = %+v, ok=%v", usage, ok)
	}
}

func TestLaterZeroDoesNotOverwriteEarlierValue(t *testing.T) {
	body := "data: {\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":10}}\n" +
		"data: {\"usage\":{\"completion_tokens\":0}}\n"
	usage, ok := ExtractUsage([]byte(body))
	if !ok || usage.CompletionTokens != 10 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestCachedAndReasoningTokenVariants(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		kind string
		want int
	}{
		{name: "prompt details", raw: map[string]any{"prompt_tokens_details": map[string]any{"cached_tokens": float64(12)}}, kind: "cached", want: 12},
		{name: "prompt cache hit", raw: map[string]any{"prompt_cache_hit_tokens": float64(34)}, kind: "cached", want: 34},
		{name: "input details", raw: map[string]any{"input_tokens_details": map[string]any{"cached_tokens": float64(56)}}, kind: "cached", want: 56},
		{name: "anthropic cache", raw: map[string]any{"cache_read_input_tokens": float64(78)}, kind: "cached", want: 78},
		{name: "completion reasoning", raw: map[string]any{"completion_tokens_details": map[string]any{"reasoning_tokens": float64(9)}}, kind: "reasoning", want: 9},
		{name: "output reasoning", raw: map[string]any{"output_tokens_details": map[string]any{"reasoning_tokens": float64(11)}}, kind: "reasoning", want: 11},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got int
			if test.kind == "cached" {
				got = CachedTokens(test.raw)
			} else {
				got = ReasoningTokens(test.raw)
			}
			if got != test.want {
				t.Fatalf("got %d, want %d", got, test.want)
			}
		})
	}
}
