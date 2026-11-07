package proxy

import (
	"math"
	"testing"
	"time"
)

func TestToolEndpointDetectionAndBilling(t *testing.T) {
	if DetectToolEndpoint("/v1/tools/search?x=1") != ToolSearch ||
		DetectToolEndpoint("/V1/TOOLS/FETCH") != ToolFetch ||
		DetectToolEndpoint("/v1/responses") != ToolNone {
		t.Fatal("tool endpoint detection is wrong")
	}
	nonEmpty := []byte(`{"results":[{"title":"found"}]}`)
	empty := []byte(`{"results":[]}`)
	if !ToolSearch.IsBillableSuccess(200, nonEmpty) || ToolSearch.IsBillableSuccess(200, empty) ||
		ToolSearch.IsBillableSuccess(500, nonEmpty) || ToolSearch.IsBillableSuccess(200, []byte("not json")) {
		t.Fatal("search billing rules are wrong")
	}
	if !ToolFetch.IsBillableSuccess(200, []byte(`{"title":"page","content":"# Body"}`)) ||
		ToolFetch.IsBillableSuccess(200, []byte(`{"title":"page","content":" \n "}`)) {
		t.Fatal("fetch billing rules are wrong")
	}
	if ToolCostPerSuccess != 0.005 {
		t.Fatalf("tool cost = %v", ToolCostPerSuccess)
	}
}

func TestServerSearchUsageDeduplicates(t *testing.T) {
	state := NewServerSearchState()
	state.Merge(map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{"type": "web_search_call", "id": "ws_1", "status": "completed"},
	})
	state.Merge(map[string]any{
		"type": "response.completed",
		"response": map[string]any{"output": []any{
			map[string]any{"type": "web_search_call", "id": "ws_1", "status": "completed"},
			map[string]any{"type": "tool_search_call", "id": "ts_1", "status": "completed"},
		}},
	})
	if state.Count() != 2 {
		t.Fatalf("search count = %d", state.Count())
	}
	anthropic := []byte(`{"content":[{"type":"server_tool_use","id":"srv_1","name":"web_search"},{"type":"web_search_tool_result","tool_use_id":"srv_1"}]}`)
	if got := ServerSearchCount(anthropic); got != 1 {
		t.Fatalf("anthropic search count = %d", got)
	}
}

func TestServerSearchBillingStart(t *testing.T) {
	before := ServerSearchBillingStart.Add(-time.Second)
	if got := ServerSearchCost(3, before); got != 0 {
		t.Fatalf("pre-billing cost = %v", got)
	}
	if got := ServerSearchCost(3, ServerSearchBillingStart); math.Abs(got-0.015) > 1e-12 {
		t.Fatalf("billing cost = %v", got)
	}
}
