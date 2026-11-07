package proxy

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

const collectorSSEBody = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":321,\"output_tokens\":1}}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":45}}\n\n" +
	"data: [DONE]\n\n"

func appendInChunks(collector *ResponseBodyCollector, body []byte, chunkSize int) {
	for start := 0; start < len(body); start += chunkSize {
		end := start + chunkSize
		if end > len(body) {
			end = len(body)
		}
		collector.Append(body[start:end])
	}
}

func TestCollectorStreamingMatchesWholeBodyAtAnyChunkBoundary(t *testing.T) {
	whole, ok := ExtractUsage([]byte(collectorSSEBody))
	if !ok {
		t.Fatal("whole body usage missing")
	}
	for _, chunkSize := range []int{1, 3, 7, 64, 4096, len(collectorSSEBody)} {
		collector := NewResponseBodyCollector(true, DefaultMaxBufferedResponseBytes)
		appendInChunks(collector, []byte(collectorSSEBody), chunkSize)
		got, ok := collector.Usage()
		if !ok || got.PromptTokens != whole.PromptTokens || got.CompletionTokens != whole.CompletionTokens {
			t.Fatalf("chunk=%d usage=%+v ok=%v, want %+v", chunkSize, got, ok, whole)
		}
		if got.PromptTokens != 321 || got.CompletionTokens != 45 {
			t.Fatalf("chunk=%d usage=%+v", chunkSize, got)
		}
		if collector.TotalBytes() != len(collectorSSEBody) || collector.DidTruncate() {
			t.Fatalf("chunk=%d total=%d truncate=%v", chunkSize, collector.TotalBytes(), collector.DidTruncate())
		}
	}
}

func TestCollectorStreamingDoesNotRetainDeltaBody(t *testing.T) {
	large := strings.Repeat("x", 50_000)
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"" + large + "\"}}]}\n" +
		"data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n"
	collector := NewResponseBodyCollector(true, DefaultMaxBufferedResponseBytes)
	appendInChunks(collector, []byte(body), 1024)
	usage, ok := collector.Usage()
	if !ok || usage.PromptTokens != 10 || usage.CompletionTokens != 5 {
		t.Fatalf("usage=%+v ok=%v", usage, ok)
	}
	if _, ok := collector.CompleteBody(); ok {
		t.Fatal("streaming collector exposed a complete body")
	}
}

func TestCollectorNonStreamingBuffersAndParses(t *testing.T) {
	body := []byte(`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40}}}`)
	collector := NewResponseBodyCollector(false, DefaultMaxBufferedResponseBytes)
	appendInChunks(collector, body, 5)
	usage, ok := collector.Usage()
	if !ok || usage.PromptTokens != 100 || usage.CompletionTokens != 20 || usage.CachedTokens != 40 {
		t.Fatalf("usage=%+v ok=%v", usage, ok)
	}
	complete, ok := collector.CompleteBody()
	if !ok || !bytes.Equal(complete, body) || collector.DidTruncate() {
		t.Fatalf("complete=%q ok=%v truncate=%v", complete, ok, collector.DidTruncate())
	}
}

func TestCollectorNonStreamingLimitIsReportedAndBounded(t *testing.T) {
	collector := NewResponseBodyCollector(false, 100)
	collector.Append(bytes.Repeat([]byte("y"), 500))
	if collector.TotalBytes() != 500 || !collector.DidTruncate() {
		t.Fatalf("total=%d truncate=%v", collector.TotalBytes(), collector.DidTruncate())
	}
	if _, ok := collector.Usage(); ok {
		t.Fatal("truncated body produced usage")
	}
	if body, ok := collector.CompleteBody(); ok || body != nil {
		t.Fatalf("truncated body exposed as complete: %q", body)
	}
}

func TestCollectorPreviewIsCapped(t *testing.T) {
	collector := NewResponseBodyCollector(true, DefaultMaxBufferedResponseBytes)
	collector.Append(bytes.Repeat([]byte("z"), 5000))
	if got := len(collector.PreviewText()); got != ResponsePreviewBytes {
		t.Fatalf("preview bytes=%d, want %d", got, ResponsePreviewBytes)
	}
}

func TestCollectorIgnoresGarbageAndEmptyChunks(t *testing.T) {
	collector := NewResponseBodyCollector(true, DefaultMaxBufferedResponseBytes)
	collector.Append(nil)
	body := []byte(": comment\nretry: 1000\nevent: ping\ndata: not-json\ndata: [DONE]\n")
	collector.Append(body)
	if _, ok := collector.Usage(); ok {
		t.Fatal("garbage produced usage")
	}
	if collector.TotalBytes() != len(body) || collector.DidTruncate() {
		t.Fatalf("total=%d truncate=%v", collector.TotalBytes(), collector.DidTruncate())
	}
}

func TestCollectorServerSearchSurvivesChunkBoundaries(t *testing.T) {
	body := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"web_search_call\",\"id\":\"ws_1\",\"status\":\"completed\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"web_search_call\",\"id\":\"ws_1\",\"status\":\"completed\"}]}}\n\n"
	collector := NewResponseBodyCollector(true, DefaultMaxBufferedResponseBytes)
	appendInChunks(collector, []byte(body), 2)
	if got := collector.ServerSearchCount(); got != 1 {
		t.Fatalf("server search count=%d", got)
	}
}

func TestAnalyzerDecodesGzipWithoutChangingForwardedBytes(t *testing.T) {
	plain := []byte(`{"usage":{"prompt_tokens":12,"completion_tokens":3}}`)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	raw := compressed.Bytes()
	for _, size := range []int{1, 2, 5, len(raw)} {
		collector := NewResponseBodyCollector(false, DefaultMaxBufferedResponseBytes)
		analyzer := NewDecodedResponseAnalyzer(collector, "gzip")
		for start := 0; start < len(raw); start += size {
			end := start + size
			if end > len(raw) {
				end = len(raw)
			}
			analyzer.Append(raw[start:end])
		}
		analyzer.Close()
		usage, ok := collector.Usage()
		if !ok || usage.PromptTokens != 12 || usage.CompletionTokens != 3 {
			t.Fatalf("size=%d usage=%+v ok=%v", size, usage, ok)
		}
	}
}

func TestAnalyzerDrainsUnknownEncoding(t *testing.T) {
	collector := NewResponseBodyCollector(true, DefaultMaxBufferedResponseBytes)
	analyzer := NewDecodedResponseAnalyzer(collector, "br")
	analyzer.Append([]byte("not-brotli"))
	analyzer.Close()
	if _, ok := collector.Usage(); ok {
		t.Fatal("unknown encoding produced usage")
	}
}

func TestAnalyzerDecodesZstdWithoutChangingForwardedBytes(t *testing.T) {
	plain := []byte(`{"usage":{"prompt_tokens":9,"completion_tokens":2}}`)
	var compressed bytes.Buffer
	writer, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	raw := compressed.Bytes()
	for _, size := range []int{1, 3, 7, len(raw)} {
		collector := NewResponseBodyCollector(false, DefaultMaxBufferedResponseBytes)
		analyzer := NewDecodedResponseAnalyzer(collector, "zstd")
		for start := 0; start < len(raw); start += size {
			end := start + size
			if end > len(raw) {
				end = len(raw)
			}
			analyzer.Append(raw[start:end])
		}
		analyzer.Close()
		usage, ok := collector.Usage()
		if !ok || usage.PromptTokens != 9 || usage.CompletionTokens != 2 {
			t.Fatalf("size=%d usage=%+v ok=%v", size, usage, ok)
		}
	}
}
