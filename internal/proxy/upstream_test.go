package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTargetURL(t *testing.T) {
	tests := []struct {
		name        string
		base        string
		apiPath     string
		requestPath string
		want        string
		wantError   bool
	}{
		{name: "join", base: "https://api.deepseek.com", apiPath: "/v1", requestPath: "/v1/chat/completions", want: "https://api.deepseek.com/v1/chat/completions"},
		{name: "base slash", base: "https://api.deepseek.com/", apiPath: "v1", requestPath: "/v1/responses", want: "https://api.deepseek.com/v1/responses"},
		{name: "avoid duplicate", base: "https://api.deepseek.com", apiPath: "/v1", requestPath: "/v1/messages", want: "https://api.deepseek.com/v1/messages"},
		{name: "base path", base: "https://api.deepseek.com/anthropic", apiPath: "/v1", requestPath: "/v1/messages", want: "https://api.deepseek.com/anthropic/v1/messages"},
		{name: "base already has api path", base: "https://api.deepseek.com/v1", apiPath: "/v1", requestPath: "/v1/chat/completions", want: "https://api.deepseek.com/v1/chat/completions"},
		{name: "nested base already has api path", base: "https://api.deepseek.com/anthropic/v1/", apiPath: "v1", requestPath: "/v1/messages", want: "https://api.deepseek.com/anthropic/v1/messages"},
		{name: "api path boundary", base: "https://api.deepseek.com/proxy-v1", apiPath: "/v1", requestPath: "/v1/responses", want: "https://api.deepseek.com/proxy-v1/v1/responses"},
		{name: "query", base: "https://api.deepseek.com", apiPath: "/v1", requestPath: "/v1/models?limit=5", want: "https://api.deepseek.com/v1/models?limit=5"},
		{name: "invalid", base: "https://a b.com", apiPath: "/v1", requestPath: "/v1/x", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := TargetURL(test.base, test.apiPath, test.requestPath)
			if test.wantError {
				if err == nil {
					t.Fatalf("TargetURL() = %q, want error", got)
				}
				return
			}
			if err != nil || got.String() != test.want {
				t.Fatalf("TargetURL() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestRequestHeaderForwardingRules(t *testing.T) {
	for _, name := range []string{"Host", "host", "Content-Length", "Transfer-Encoding", "Authorization", "X-Api-Key", "Connection"} {
		if ShouldForwardRequestHeader(name) {
			t.Fatalf("%s should not be forwarded", name)
		}
	}
	for _, name := range []string{"User-Agent", "Accept", "Content-Type", "anthropic-version", "Accept-Encoding"} {
		if !ShouldForwardRequestHeader(name) {
			t.Fatalf("%s should be forwarded", name)
		}
	}
	if !ClientUsesAPIKeyHeader(header("X-Api-Key", "placeholder")) ||
		ClientUsesAPIKeyHeader(header("Authorization", "Bearer placeholder")) {
		t.Fatal("auth style detection is wrong")
	}
}

func TestBuildUpstreamRequest(t *testing.T) {
	body := []byte(`{"model":"deepseek-flash"}`)
	request, err := BuildUpstreamRequest(context.Background(), http.MethodPost,
		"/v1/chat/completions", header(
			"Host", "127.0.0.1:18080",
			"Content-Length", "26",
			"User-Agent", "DSmonTests/1.0",
			"Accept", "text/event-stream",
			"Authorization", "Bearer sk-placeholder",
		), body, "https://api.deepseek.com", "/v1", "sk-real-key", "Bearer")
	if err != nil {
		t.Fatal(err)
	}
	if request.Method != http.MethodPost || request.URL.String() != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("request = %s %s", request.Method, request.URL)
	}
	if request.Header.Get("User-Agent") != "DSmonTests/1.0" ||
		request.Header.Get("Accept") != "text/event-stream" ||
		request.Header.Get("Authorization") != "Bearer sk-real-key" ||
		request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", request.Header)
	}
	if strings.Contains(request.Header.Get("Authorization"), "placeholder") || request.Host != "api.deepseek.com" {
		t.Fatalf("request host/auth = %q / %q", request.Host, request.Header.Get("Authorization"))
	}
	gotBody, _ := io.ReadAll(request.Body)
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body = %q", gotBody)
	}

	apiKeyRequest, err := BuildUpstreamRequest(context.Background(), http.MethodPost, "/v1/messages",
		header("x-api-key", "placeholder"), body, "https://api.deepseek.com/anthropic", "/v1", "sk-real-key", "Bearer")
	if err != nil {
		t.Fatal(err)
	}
	if apiKeyRequest.Header.Get("X-Api-Key") != "sk-real-key" || apiKeyRequest.Header.Get("Authorization") != "" {
		t.Fatalf("api-key request headers = %v", apiKeyRequest.Header)
	}
}

func TestProtocolDetectionAndEndpointNormalization(t *testing.T) {
	if DetectProtocol("/v1/messages", nil) != ProtocolAnthropic ||
		DetectProtocol("/v1/chat/completions", header("anthropic-version", "2023-06-01")) != ProtocolAnthropic ||
		DetectProtocol("/v1/responses", nil) != ProtocolOpenAI {
		t.Fatal("protocol detection is wrong")
	}
	tests := map[string]string{
		"/v1/tools/search?x=1": "/v1/tools/search",
		"/V1/TOOLS/FETCH":      "/v1/tools/fetch",
		"/v1/messages?x=1":     "/v1/messages",
		"/v1/responses":        "/v1/responses",
		"/chat/completions":    "/v1/chat/completions",
	}
	for input, want := range tests {
		if got := NormalizeEndpoint(input); got != want {
			t.Fatalf("NormalizeEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
}
