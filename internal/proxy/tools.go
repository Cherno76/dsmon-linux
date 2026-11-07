package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type ToolEndpoint int

const (
	ToolNone ToolEndpoint = iota
	ToolSearch
	ToolFetch
)

const ToolCostPerSuccess = 5.0 / 1000

var ServerSearchBillingStart = time.Unix(1_793_462_400, 0)

func DetectToolEndpoint(path string) ToolEndpoint {
	switch strings.ToLower(NormalizeEndpoint(path)) {
	case "/v1/tools/search":
		return ToolSearch
	case "/v1/tools/fetch":
		return ToolFetch
	default:
		return ToolNone
	}
}

func (t ToolEndpoint) Endpoint() string {
	switch t {
	case ToolSearch:
		return "/v1/tools/search"
	case ToolFetch:
		return "/v1/tools/fetch"
	default:
		return ""
	}
}

func (t ToolEndpoint) UsageLabel() string {
	switch t {
	case ToolSearch:
		return "Search API"
	case ToolFetch:
		return "Fetch API"
	default:
		return ""
	}
}

func (t ToolEndpoint) IsBillableSuccess(statusCode int, responseBody []byte) bool {
	if statusCode != 200 {
		return false
	}
	var payload map[string]any
	if json.Unmarshal(responseBody, &payload) != nil {
		return false
	}
	switch t {
	case ToolSearch:
		results, ok := payload["results"].([]any)
		return ok && len(results) > 0
	case ToolFetch:
		content, ok := payload["content"].(string)
		return ok && strings.TrimSpace(content) != ""
	default:
		return false
	}
}

type ServerSearchState struct {
	mu    sync.Mutex
	seen  map[string]bool
	count int
}

func NewServerSearchState() *ServerSearchState {
	return &ServerSearchState{seen: map[string]bool{}}
}

func (s *ServerSearchState) Merge(object map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	eventType, _ := object["type"].(string)
	if eventType == "response.output_item.done" {
		if item, ok := object["item"].(map[string]any); ok {
			s.addResponseItem(item, -1)
		}
	}
	if eventType == "response.web_search_call.completed" {
		if id := stringValue(object["item_id"]); id != "" {
			s.add(id)
		} else if id := stringValue(object["id"]); id != "" {
			s.add(id)
		} else if index, ok := jsonNumber(object["output_index"]); ok {
			s.add(fmt.Sprintf("response-index-%d", index))
		} else {
			s.add(fmt.Sprintf("response-completed-%d", s.count))
		}
	}
	if eventType == "content_block_start" {
		if block, ok := object["content_block"].(map[string]any); ok {
			s.addAnthropicBlock(block)
		}
	}
	if response, ok := object["response"].(map[string]any); ok {
		s.addContainers(response)
	}
	if message, ok := object["message"].(map[string]any); ok {
		s.addContainers(message)
	}
	s.addContainers(object)
}

func (s *ServerSearchState) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *ServerSearchState) addContainers(object map[string]any) {
	if output, ok := object["output"].([]any); ok {
		fallback := 0
		for _, raw := range output {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			itemType, _ := item["type"].(string)
			if itemType == "web_search_call" || itemType == "tool_search_call" {
				s.addResponseItem(item, fallback)
				fallback++
			}
		}
	}
	if content, ok := object["content"].([]any); ok {
		hasServerUse := false
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if ok && isAnthropicServerSearch(block) {
				hasServerUse = true
				s.addAnthropicBlock(block)
			}
		}
		if !hasServerUse {
			for _, raw := range content {
				block, ok := raw.(map[string]any)
				if !ok || stringValue(block["type"]) != "web_search_tool_result" {
					continue
				}
				id := stringValue(block["tool_use_id"])
				if id == "" {
					id = stringValue(block["id"])
				}
				if id == "" {
					id = fmt.Sprintf("web-result-%d", s.count)
				}
				s.add(id)
			}
		}
	}
}

func (s *ServerSearchState) addResponseItem(item map[string]any, fallbackIndex int) {
	itemType := stringValue(item["type"])
	if itemType != "web_search_call" && itemType != "tool_search_call" {
		return
	}
	status := stringValue(item["status"])
	if status == "failed" || status == "incomplete" {
		return
	}
	key := stringValue(item["id"])
	if key == "" {
		key = stringValue(item["call_id"])
	}
	if key == "" {
		if index, ok := jsonNumber(item["output_index"]); ok {
			key = fmt.Sprintf("response-index-%d", index)
		}
	}
	if key == "" && fallbackIndex >= 0 {
		key = fmt.Sprintf("response-item-%d", fallbackIndex)
	}
	if key == "" {
		key = fmt.Sprintf("response-item-%d", s.count)
	}
	s.add(key)
}

func (s *ServerSearchState) addAnthropicBlock(block map[string]any) {
	if !isAnthropicServerSearch(block) {
		return
	}
	key := stringValue(block["id"])
	if key == "" {
		key = stringValue(block["tool_use_id"])
	}
	if key == "" {
		key = fmt.Sprintf("server-search-%d", s.count)
	}
	s.add(key)
}

func (s *ServerSearchState) add(key string) {
	if s.seen[key] {
		return
	}
	s.seen[key] = true
	s.count++
}

func isAnthropicServerSearch(block map[string]any) bool {
	return stringValue(block["type"]) == "server_tool_use" && stringValue(block["name"]) == "web_search"
}

func ServerSearchCount(responseBody []byte) int {
	state := NewServerSearchState()
	var object map[string]any
	if json.Unmarshal(responseBody, &object) == nil {
		state.Merge(object)
	}
	return state.Count()
}

func ServerSearchCost(searchCount int, at time.Time) float64 {
	if at.Before(ServerSearchBillingStart) || searchCount <= 0 {
		return 0
	}
	return float64(searchCount) * ToolCostPerSuccess
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func splitDataLines(body []byte) []map[string]any {
	var result []map[string]any
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		payload := line[len("data: "):]
		var object map[string]any
		if bytes.Equal(payload, []byte("[DONE]")) || json.Unmarshal(payload, &object) != nil {
			continue
		}
		result = append(result, object)
	}
	return result
}
