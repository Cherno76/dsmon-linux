package proxy

import (
	"net/http"
	"testing"
)

func TestCopyResponseHeaders(t *testing.T) {
	source := header(
		"Connection", "keep-alive",
		"Keep-Alive", "timeout=5",
		"Transfer-Encoding", "chunked",
		"Content-Type", "application/json",
		"Content-Encoding", "gzip",
		"Content-Length", "56",
	)
	destination := http.Header{}
	CopyResponseHeaders(destination, source, false)
	if destination.Get("Connection") != "" || destination.Get("Transfer-Encoding") != "" ||
		destination.Get("Keep-Alive") != "" {
		t.Fatalf("hop-by-hop headers leaked: %v", destination)
	}
	if destination.Get("Content-Type") != "application/json" ||
		destination.Get("Content-Encoding") != "gzip" || destination.Get("Content-Length") != "56" {
		t.Fatalf("end-to-end headers lost: %v", destination)
	}
	streaming := http.Header{}
	CopyResponseHeaders(streaming, source, true)
	if streaming.Get("Content-Length") != "" || streaming.Get("Content-Encoding") != "gzip" {
		t.Fatalf("streaming headers = %v", streaming)
	}
}

func TestResponseConnectionHeaderTokens(t *testing.T) {
	source := header("Connection", "X-Drop-Me, keep-alive", "X-Drop-Me", "secret", "X-Keep", "yes")
	destination := http.Header{}
	CopyResponseHeaders(destination, source, false)
	if destination.Get("X-Drop-Me") != "" || destination.Get("X-Keep") != "yes" {
		t.Fatalf("connection-token headers = %v", destination)
	}
}

func TestIsStreamingResponse(t *testing.T) {
	if !IsStreamingResponse(header("Content-Type", "text/event-stream; charset=utf-8")) {
		t.Fatal("SSE response not detected")
	}
	if IsStreamingResponse(header("Content-Type", "application/json")) {
		t.Fatal("JSON response detected as SSE")
	}
}
