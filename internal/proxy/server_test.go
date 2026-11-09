package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"dsmon/internal/pricing"
	"dsmon/internal/proxy/trace"
	"dsmon/internal/store"
)

var testNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))

func newProxyTestStore(t *testing.T) *store.Store {
	t.Helper()
	text := `{"deepseek":{"models":{"deepseek-chat":{"offpeak":{"miss":1,"hit":0.1,"out":2}}}}}`
	strategy, err := pricing.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	loaded := pricing.Loaded{
		Strategy: strategy, Source: pricing.SourceBundled,
		Text: text, Fingerprint: pricing.Fingerprint(text),
	}
	database, err := store.Open(t.TempDir()+"/usage.db",
		store.WithClock(func() time.Time { return testNow }),
		store.WithLocation(testNow.Location()),
		store.WithPricing(loaded),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func newProxyTestServer(t *testing.T, database *store.Store, upstream string, mutate func(*Config)) *Server {
	t.Helper()
	cfg := Config{
		Store:         database,
		OpenAIBase:    upstream,
		AnthropicBase: upstream,
		ToolsBase:     upstream,
		APIKey:        "sk-real-key",
		ProviderID:    "deepseek",
		Now:           func() time.Time { return testNow },
		Logf:          func(string, ...any) {},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	server, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func proxyClient(t *testing.T, server *Server) (*http.Client, string) {
	t.Helper()
	upstream := httptest.NewServer(server)
	t.Cleanup(upstream.Close)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	return client, upstream.URL
}

func postJSON(t *testing.T, client *http.Client, target, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer client-placeholder")
	request.Header.Set("User-Agent", "opencode/1.18.25")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitForRequests(t *testing.T, database *store.Store, want int) store.AggregatedUsage {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		usage, err := database.QueryWindow(context.Background(), 1, "deepseek", 0)
		if err != nil {
			t.Fatal(err)
		}
		if usage.RequestCount >= want {
			return usage
		}
		if time.Now().After(deadline) {
			t.Fatalf("request count=%d, want %d", usage.RequestCount, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServerForwardsJSONAndRecordsUsage(t *testing.T) {
	database := newProxyTestStore(t)
	seen := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-real-key" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "" {
			t.Errorf("unexpected x-api-key = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"model":"deepseek-chat"`)) {
			t.Errorf("upstream body = %q", body)
		}
		seen <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":40}}}`)
	}))
	defer upstream.Close()

	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	response := postJSON(t, client, proxyURL+"/v1/chat/completions",
		`{"model":"deepseek-chat","messages":[]}`)
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"prompt_tokens":100`)) {
		t.Fatalf("response = %d %q", response.StatusCode, body)
	}
	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive request")
	}

	usage := waitForRequests(t, database, 1)
	if usage.PromptTokens != 100 || usage.CompletionTokens != 20 || usage.TotalTokens != 120 || usage.CachedTokens != 40 {
		t.Fatalf("recorded usage = %+v", usage)
	}
	recent, err := database.RecentRecords(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].StatusCode != 200 || recent[0].Model != "deepseek-chat" ||
		recent[0].UserAgent != "opencode/1.18.25" {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestServerPassesThroughModelDiscoveryWithoutRecordingUsage(t *testing.T) {
	database := newProxyTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models/deepseek-flash" {
			t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("include"); got != "details" {
			t.Errorf("query include = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-real-key" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "" {
			t.Errorf("unexpected x-api-key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Metadata", "preserved")
		_, _ = io.WriteString(w, `{"id":"deepseek-flash","object":"model"}`)
	}))
	defer upstream.Close()

	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	request, err := http.NewRequest(http.MethodGet,
		proxyURL+"/v1/models/deepseek-flash?include=details", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer client-placeholder")
	request.Header.Set("User-Agent", "codex-cli/0.167.0")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("X-Upstream-Metadata") != "preserved" ||
		!bytes.Contains(body, []byte(`"id":"deepseek-flash"`)) {
		t.Fatalf("response = %d headers=%v body=%q", response.StatusCode, response.Header, body)
	}

	recent, err := database.RecentRecords(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 0 {
		t.Fatalf("model discovery was recorded as usage: %+v", recent)
	}
}

func TestServerRoutesAnthropicModelDiscoveryToAnthropicBase(t *testing.T) {
	database := newProxyTestStore(t)
	openAICalled := make(chan struct{}, 1)
	openAI := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		openAICalled <- struct{}{}
	}))
	defer openAI.Close()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "sk-real-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("authorization leaked = %q", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got != anthropicDefaultVersion {
			t.Errorf("anthropic-version = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-chat"}]}`)
	}))
	defer anthropic.Close()

	server := newProxyTestServer(t, database, openAI.URL, func(cfg *Config) {
		cfg.AnthropicBase = anthropic.URL
	})
	client, proxyURL := proxyClient(t, server)
	request, err := http.NewRequest(http.MethodGet, proxyURL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Api-Key", "client-placeholder")
	request.Header.Set("Anthropic-Version", anthropicDefaultVersion)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	select {
	case <-openAICalled:
		t.Fatal("anthropic model discovery used the OpenAI base")
	default:
	}
}

func TestServerModelDiscoveryFailureDoesNotRecordUsage(t *testing.T) {
	database := newProxyTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	response, err := client.Get(proxyURL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.StatusCode)
	}

	recent, err := database.RecentRecords(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 0 {
		t.Fatalf("failed model discovery was recorded as usage: %+v", recent)
	}
}

func TestServerRejectsUnsupportedAuxiliaryRoutes(t *testing.T) {
	database := newProxyTestStore(t)
	called := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called <- struct{}{}
	}))
	defer upstream.Close()

	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/v1/embeddings"},
		{method: http.MethodPost, path: "/v1/models"},
		{method: http.MethodGet, path: "/v1/models/../../admin"},
	}
	for _, test := range tests {
		request, err := http.NewRequest(test.method, proxyURL+test.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s status = %d, want 404", test.method, test.path, response.StatusCode)
		}
	}
	select {
	case <-called:
		t.Fatal("unsupported auxiliary route reached upstream")
	default:
	}
}

func TestServerOverridesAnthropicAuthentication(t *testing.T) {
	database := newProxyTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != "sk-real-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("authorization leaked = %q", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got != anthropicDefaultVersion {
			t.Errorf("anthropic-version = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":7,"output_tokens":3}}`)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)

	request, _ := http.NewRequest(http.MethodPost, proxyURL+"/v1/messages",
		strings.NewReader(`{"model":"deepseek-chat","max_tokens":8,"messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer placeholder")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	waitForRequests(t, database, 1)
}

func TestServerReconfigureAppliesWithoutReplacingListener(t *testing.T) {
	database := newProxyTestStore(t)
	auth := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()

	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	response := postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	_ = response.Body.Close()
	if got := <-auth; got != "Bearer sk-real-key" {
		t.Fatalf("initial authorization = %q", got)
	}

	if err := server.Reconfigure(Config{
		Store: database, OpenAIBase: upstream.URL, AnthropicBase: upstream.URL,
		ToolsBase: upstream.URL, APIKey: "sk-new-key", ProviderID: "deepseek",
		Now: func() time.Time { return testNow }, Logf: func(string, ...any) {},
	}); err != nil {
		t.Fatalf("reconfigure: %v", err)
	}
	response = postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	_ = response.Body.Close()
	if got := <-auth; got != "Bearer sk-new-key" {
		t.Fatalf("updated authorization = %q", got)
	}
	if err := server.Reconfigure(Config{
		Store: database, ListenAddr: "127.0.0.1:18081", OpenAIBase: upstream.URL,
		AnthropicBase: upstream.URL, ToolsBase: upstream.URL, APIKey: "sk-new-key",
		ProviderID: "deepseek", Now: func() time.Time { return testNow },
		Logf: func(string, ...any) {},
	}); err == nil {
		t.Fatal("reconfigure changed the listener address")
	}
}

func TestServerFlushesSSEBeforeUpstreamFinishes(t *testing.T) {
	database := newProxyTestStore(t)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		flusher.Flush()
		<-release
		_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	response := postJSON(t, client, proxyURL+"/v1/chat/completions",
		`{"model":"deepseek-chat","stream":true,"messages":[]}`)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		value, err := reader.ReadString('\n')
		if err != nil {
			errs <- err
			return
		}
		line <- value
	}()
	select {
	case got := <-line:
		if !strings.Contains(got, `"first"`) {
			t.Fatalf("first line = %q", got)
		}
	case err := <-errs:
		t.Fatalf("read first SSE line: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("first SSE event was buffered until upstream finished")
	}
	close(release)
	_, _ = io.Copy(io.Discard, reader)
	usage := waitForRequests(t, database, 1)
	if usage.PromptTokens != 9 || usage.CompletionTokens != 4 {
		t.Fatalf("stream usage = %+v", usage)
	}
}

func TestServerPreservesCompressedBytesAndStillRecordsUsage(t *testing.T) {
	database := newProxyTestStore(t)
	plain := []byte(`{"choices":[],"usage":{"prompt_tokens":31,"completion_tokens":8}}`)
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	_, _ = gzipWriter.Write(plain)
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), compressed.Bytes()...)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "gzip" {
			t.Errorf("accept-encoding = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
		_, _ = w.Write(raw)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)

	request, _ := http.NewRequest(http.MethodPost, proxyURL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat","messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept-Encoding", "gzip")
	request.Header.Set("Authorization", "Bearer placeholder")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	forwarded, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "gzip" ||
		response.Header.Get("Content-Length") != fmt.Sprint(len(raw)) || !bytes.Equal(forwarded, raw) {
		t.Fatalf("forwarded response = %d headers=%v bytes_equal=%v", response.StatusCode,
			response.Header, bytes.Equal(forwarded, raw))
	}
	usage := waitForRequests(t, database, 1)
	if usage.PromptTokens != 31 || usage.CompletionTokens != 8 {
		t.Fatalf("compressed usage = %+v", usage)
	}
}

func TestServerPreservesZstdSSEAndStillRecordsUsage(t *testing.T) {
	database := newProxyTestStore(t)
	plain := []byte("data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9}}\n\n" +
		"data: [DONE]\n\n")
	var compressed bytes.Buffer
	writer, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write(plain)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), compressed.Bytes()...)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "zstd" {
			t.Errorf("accept-encoding = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "zstd")
		_, _ = w.Write(raw)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)

	request, _ := http.NewRequest(http.MethodPost, proxyURL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-flash","stream":true,"messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept-Encoding", "zstd")
	request.Header.Set("Authorization", "Bearer placeholder")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	forwarded, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "zstd" ||
		!bytes.Equal(forwarded, raw) {
		t.Fatalf("forwarded response = %d headers=%v bytes_equal=%v", response.StatusCode,
			response.Header, bytes.Equal(forwarded, raw))
	}
	usage := waitForRequests(t, database, 1)
	if usage.PromptTokens != 7 || usage.CompletionTokens != 2 || usage.TotalTokens != 9 {
		t.Fatalf("zstd SSE usage = %+v", usage)
	}
}

func TestServerRecordsFailedRequests(t *testing.T) {
	database := newProxyTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	response := postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}
	usage := waitForRequests(t, database, 1)
	if usage.TotalTokens != 0 || usage.EstimatedCost != 0 {
		t.Fatalf("failed request usage = %+v", usage)
	}
	recent, _ := database.RecentRecords(context.Background(), 1)
	if len(recent) != 1 || recent[0].StatusCode != 500 || recent[0].Model != "deepseek-chat" {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestServerRecordsUnreachableUpstream(t *testing.T) {
	database := newProxyTestStore(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := "http://" + listener.Addr().String()
	_ = listener.Close()
	server := newProxyTestServer(t, database, unreachable, nil)
	client, proxyURL := proxyClient(t, server)
	response := postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d", response.StatusCode)
	}
	usage := waitForRequests(t, database, 1)
	if usage.TotalTokens != 0 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestServerToolSearchFixedCost(t *testing.T) {
	database := newProxyTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/search" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"title":"found"}]}`)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	response := postJSON(t, client, proxyURL+"/v1/tools/search", `{"query":"dsmon"}`)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	usage := waitForRequests(t, database, 1)
	if usage.EstimatedCost != ToolCostPerSuccess {
		t.Fatalf("tool cost = %v", usage.EstimatedCost)
	}
	recent, _ := database.RecentRecords(context.Background(), 1)
	if len(recent) != 1 || recent[0].Model != "Search API" {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestServerSearchUsageAddsFixedCostAfterBillingStart(t *testing.T) {
	database := newProxyTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"unknown-model","usage":{"prompt_tokens":5,"completion_tokens":2},"output":[{"type":"web_search_call","id":"ws_1","status":"completed"},{"type":"web_search_call","id":"ws_1","status":"completed"}]}`)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	response := postJSON(t, client, proxyURL+"/v1/responses", `{"model":"unknown-model"}`)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	usage := waitForRequests(t, database, 1)
	if usage.PromptTokens != 5 || usage.CompletionTokens != 2 || usage.EstimatedCost != ToolCostPerSuccess {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestServerRateLimit(t *testing.T) {
	database := newProxyTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, func(cfg *Config) { cfg.RPMLimit = 1 })
	client, proxyURL := proxyClient(t, server)
	first := postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	_ = first.Body.Close()
	second := postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	_ = second.Body.Close()
	if first.StatusCode != http.StatusOK || second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("statuses = %d, %d", first.StatusCode, second.StatusCode)
	}
	waitForRequests(t, database, 2)
}

func TestServerConcurrencyLimit(t *testing.T) {
	database := newProxyTestStore(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, func(cfg *Config) { cfg.MaxConcurrent = 1 })
	client, proxyURL := proxyClient(t, server)

	done := make(chan *http.Response, 1)
	go func() {
		done <- postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not reach upstream")
	}
	second := postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	_ = second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d", second.StatusCode)
	}
	close(release)
	first := <-done
	_ = first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", first.StatusCode)
	}
}

func TestServerRejectsUnknownRouteAndOversizedBody(t *testing.T) {
	database := newProxyTestStore(t)
	server := newProxyTestServer(t, database, "http://127.0.0.1:1", func(cfg *Config) {
		cfg.MaxRequestBytes = 10
	})
	client, proxyURL := proxyClient(t, server)

	unknown, err := client.Post(proxyURL+"/v1/unknown", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown status = %d", unknown.StatusCode)
	}

	oversized := postJSON(t, client, proxyURL+"/v1/chat/completions", `{"model":"deepseek-chat"}`)
	_ = oversized.Body.Close()
	if oversized.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d", oversized.StatusCode)
	}

	chunkedRequest, _ := http.NewRequest(http.MethodPost, proxyURL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-chat"}`))
	chunkedRequest.ContentLength = -1
	chunkedRequest.Header.Set("Content-Type", "application/json")
	chunked, err := client.Do(chunkedRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = chunked.Body.Close()
	if chunked.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized status = %d", chunked.StatusCode)
	}
}

func TestServerOffloadsInlineImagesBeforeForwarding(t *testing.T) {
	database := newProxyTestStore(t)
	filesCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files":
			filesCalls++
			body, _ := io.ReadAll(r.Body)
			if got := r.Header.Get("Authorization"); got != "Bearer sk-real-key" {
				t.Errorf("files authorization = %q", got)
			}
			if !bytes.Contains(body, []byte(`name="purpose"`)) ||
				!bytes.Contains(body, []byte("user_data")) {
				t.Errorf("files multipart body missing purpose: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"file-api-server-test"}`)
		case "/v1/responses":
			body, _ := io.ReadAll(r.Body)
			if bytes.Contains(body, []byte("data:image")) {
				t.Errorf("upstream still received inline image: %s", body)
			}
			if !bytes.Contains(body, []byte("file-api-server-test")) {
				t.Errorf("upstream body has no file_id: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := newProxyTestServer(t, database, upstream.URL, func(cfg *Config) {
		cfg.MaxRequestBytes = 1024
	})
	server.offload.actAbove = 10
	server.offload.target = 5
	client, proxyURL := proxyClient(t, server)
	requestBody := `{"model":"deepseek-chat","input":[{"content":[{"type":"input_image","image_url":"` +
		testImageDataURL(4096, "image/png") + `"}]}]}`
	response := postJSON(t, client, proxyURL+"/v1/responses", requestBody)
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("response = %d %s", response.StatusCode, body)
	}
	if filesCalls != 1 {
		t.Fatalf("files calls = %d, want 1", filesCalls)
	}
	usage := waitForRequests(t, database, 1)
	if usage.PromptTokens != 3 || usage.CompletionTokens != 2 || usage.TotalTokens != 5 {
		t.Fatalf("recorded usage = %+v", usage)
	}
}

func TestImageOffloadRoutesStillRejectOversizedBodiesWithoutImages(t *testing.T) {
	database := newProxyTestStore(t)
	server := newProxyTestServer(t, database, "http://127.0.0.1:1", func(cfg *Config) {
		cfg.MaxRequestBytes = 64
	})
	client, proxyURL := proxyClient(t, server)
	requestBody := `{"model":"deepseek-chat","input":[{"content":[{"type":"input_text","text":"` +
		strings.Repeat("x", 128) + `"}]}]}`
	response := postJSON(t, client, proxyURL+"/v1/responses", requestBody)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 after offload finds no image", response.StatusCode)
	}
}

func TestNewServerRejectsNonLoopbackListener(t *testing.T) {
	database := newProxyTestStore(t)
	_, err := NewServer(Config{
		Store: database, ListenAddr: "0.0.0.0:18080",
		OpenAIBase: "https://api.deepseek.com",
	})
	if err == nil {
		t.Fatal("non-loopback listen address was accepted")
	}
}

func TestServerDrivesActivityAndTrafficCurves(t *testing.T) {
	database := newProxyTestStore(t)
	responseBody := `{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
	requestBody := `{"model":"deepseek-chat"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseBody)
	}))
	defer upstream.Close()
	server := newProxyTestServer(t, database, upstream.URL, nil)
	client, proxyURL := proxyClient(t, server)
	response := postJSON(t, client, proxyURL+"/v1/chat/completions", requestBody)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	activity := server.ActivityBars(testNow)
	if len(activity) == 0 || activity[len(activity)-1] != 1 {
		t.Fatalf("activity bars = %v", activity)
	}
	if got := server.LastActivityAt(); !got.Equal(testNow) {
		t.Fatalf("last activity = %v, want %v", got, testNow)
	}
	var upload, download int
	for _, bucket := range server.TrafficBuckets(testNow) {
		upload += bucket.Upload
		download += bucket.Download
	}
	if upload != len(requestBody) || download != len(responseBody) {
		t.Fatalf("traffic upload=%d download=%d, want %d/%d", upload, download,
			len(requestBody), len(responseBody))
	}
}

func TestCurveStateTreatsUploadAndDownloadAsActivity(t *testing.T) {
	current := testNow
	curves := &curveState{
		activity: trace.NewActivity(testNow),
		traffic:  trace.NewTraffic(testNow),
		now:      func() time.Time { return current },
	}

	curves.RecordRequest()
	if !curves.lastActivityAt.Equal(testNow) {
		t.Fatalf("request activity = %v", curves.lastActivityAt)
	}

	current = testNow.Add(time.Second)
	curves.RecordUpload(1024)
	if !curves.lastActivityAt.Equal(current) {
		t.Fatalf("upload activity = %v, want %v", curves.lastActivityAt, current)
	}

	current = testNow.Add(2 * time.Second)
	curves.RecordDownload(2048)
	if !curves.lastActivityAt.Equal(current) {
		t.Fatalf("download activity = %v, want %v", curves.lastActivityAt, current)
	}

	previous := curves.lastActivityAt
	current = testNow.Add(3 * time.Second)
	curves.RecordUpload(0)
	if !curves.lastActivityAt.Equal(previous) {
		t.Fatalf("zero-byte upload changed activity to %v", curves.lastActivityAt)
	}
}

func TestServerUnifiesCloseTrafficScales(t *testing.T) {
	database := newProxyTestStore(t)
	server := newProxyTestServer(t, database, "http://127.0.0.1:1", nil)
	server.curves.RecordUpload(24_000)
	server.curves.RecordDownload(28_000)
	frame := server.TrafficFrame(testNow, 120)
	if frame.UpScale != 20_000 || frame.DownScale != 20_000 {
		t.Fatalf("close scales were not unified: down=%v up=%v", frame.DownScale, frame.UpScale)
	}
}

func TestServerKeepsDoubleTrafficScalesIndependent(t *testing.T) {
	database := newProxyTestStore(t)
	server := newProxyTestServer(t, database, "http://127.0.0.1:1", nil)
	server.curves.RecordUpload(20_000)
	server.curves.RecordDownload(40_000)
	frame := server.TrafficFrame(testNow, 120)
	if frame.UpScale != 10_000 || frame.DownScale != 30_000 {
		t.Fatalf("double scales should stay independent: down=%v up=%v", frame.DownScale, frame.UpScale)
	}
}

func TestServerTrafficFrameLimitsBucketsToRequestedWidth(t *testing.T) {
	database := newProxyTestStore(t)
	server := newProxyTestServer(t, database, "http://127.0.0.1:1", nil)
	frame := server.TrafficFrame(testNow, 20)
	if len(frame.Buckets) != 40 {
		t.Fatalf("visible traffic buckets = %d, want width*2 = 40", len(frame.Buckets))
	}
}

func TestServerEnforcesHeaderLimit(t *testing.T) {
	database := newProxyTestStore(t)
	server := newProxyTestServer(t, database, "http://127.0.0.1:1", nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ctx, listener) }()

	connection, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(connection, "POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Huge: %s\r\n\r\n",
		strings.Repeat("x", MaxHeaderBytes+1024))
	status, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "431") {
		t.Fatalf("status line = %q", status)
	}
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestRequestHeadBytesIncludesHostAndLength(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18080/v1/responses?x=1", nil)
	request.Host = "127.0.0.1:18080"
	request.ContentLength = 123
	request.Header.Set("X-Test", "yes")
	got := requestHeadBytes(request)
	want := len("POST") + 1 + len("/v1/responses?x=1") + 1 + len("HTTP/1.1") + 2 +
		len("Host: ") + len("127.0.0.1:18080") + 2 +
		len("Content-Length: ") + len("123") + 2 +
		len("X-Test: ") + len("yes") + 2 + 2
	if got != want {
		t.Fatalf("requestHeadBytes=%d, want %d", got, want)
	}
}
