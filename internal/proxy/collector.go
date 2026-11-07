package proxy

import (
	"bytes"
	"encoding/json"
)

const (
	ResponsePreviewBytes            = 600
	DefaultMaxBufferedResponseBytes = 8 * 1024 * 1024
)

// ResponseBodyCollector 在转发响应的同时提取用量。流式响应只保留行缓冲和
// 解析后的 usage；非流式 JSON 才按上限缓存，避免长会话把整段正文留在内存里。
type ResponseBodyCollector struct {
	isStreaming      bool
	maxBufferedBytes int

	totalBytes  int
	didTruncate bool
	preview     []byte
	lineBuffer  []byte
	jsonBuffer  []byte
	mergedUsage map[string]any

	serverSearch *ServerSearchState
}

func NewResponseBodyCollector(isStreaming bool, maxBufferedBytes int) *ResponseBodyCollector {
	if maxBufferedBytes <= 0 {
		maxBufferedBytes = DefaultMaxBufferedResponseBytes
	}
	return &ResponseBodyCollector{
		isStreaming:      isStreaming,
		maxBufferedBytes: maxBufferedBytes,
		mergedUsage:      map[string]any{},
		serverSearch:     NewServerSearchState(),
	}
}

func (c *ResponseBodyCollector) Append(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	c.totalBytes += len(chunk)

	if remaining := ResponsePreviewBytes - len(c.preview); remaining > 0 {
		take := len(chunk)
		if take > remaining {
			take = remaining
		}
		c.preview = append(c.preview, chunk[:take]...)
	}

	if c.isStreaming {
		c.consumeLines(chunk)
		return
	}
	if len(c.jsonBuffer) >= c.maxBufferedBytes {
		c.didTruncate = true
		return
	}
	room := c.maxBufferedBytes - len(c.jsonBuffer)
	take := len(chunk)
	if take > room {
		take = room
		c.didTruncate = true
	}
	c.jsonBuffer = append(c.jsonBuffer, chunk[:take]...)
}

func (c *ResponseBodyCollector) Usage() (TokenUsage, bool) {
	if c.isStreaming {
		if len(c.mergedUsage) == 0 {
			return TokenUsage{}, false
		}
		return TokenUsageFromMap(c.mergedUsage)
	}
	if c.didTruncate {
		return TokenUsage{}, false
	}
	return ExtractUsage(c.jsonBuffer)
}

func (c *ResponseBodyCollector) CompleteBody() ([]byte, bool) {
	if c.isStreaming || c.didTruncate {
		return nil, false
	}
	return c.jsonBuffer, true
}

func (c *ResponseBodyCollector) ServerSearchCount() int {
	if c.isStreaming {
		return c.serverSearch.Count()
	}
	if c.didTruncate {
		return 0
	}
	return ServerSearchCount(c.jsonBuffer)
}

func (c *ResponseBodyCollector) PreviewText() string {
	return string(c.preview)
}

func (c *ResponseBodyCollector) TotalBytes() int   { return c.totalBytes }
func (c *ResponseBodyCollector) DidTruncate() bool { return c.didTruncate }

func (c *ResponseBodyCollector) consumeLines(chunk []byte) {
	c.lineBuffer = append(c.lineBuffer, chunk...)
	for {
		newline := bytes.IndexByte(c.lineBuffer, '\n')
		if newline < 0 {
			break
		}
		line := c.lineBuffer[:newline]
		c.consumeLine(line)
		c.lineBuffer = c.lineBuffer[newline+1:]
	}
	if len(c.lineBuffer) > c.maxBufferedBytes {
		c.didTruncate = true
		c.lineBuffer = nil
	}
}

func (c *ResponseBodyCollector) consumeLine(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data: ")) {
		return
	}
	payload := trimmed[len("data: "):]
	if bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var object map[string]any
	if json.Unmarshal(payload, &object) != nil {
		return
	}
	MergeUsageObject(object, c.mergedUsage)
	c.serverSearch.Merge(object)
}
