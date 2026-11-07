package proxy

import (
	"net/http"
	"strings"
)

func IsStreamingResponse(headers http.Header) bool {
	return strings.Contains(strings.ToLower(headers.Get("Content-Type")), "text/event-stream")
}

// CopyResponseHeaders 只剥离逐跳头；压缩响应原样转发，因为我们从不自动解压正文。
func CopyResponseHeaders(destination, source http.Header, streaming bool) {
	connectionHeaders := connectionHeaderTokens(source)
	for key, values := range source {
		lowerKey := strings.ToLower(key)
		if isHopByHopHeader(lowerKey) || connectionHeaders[lowerKey] {
			continue
		}
		if streaming && lowerKey == "content-length" {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch name {
	case "connection", "proxy-connection", "keep-alive", "transfer-encoding", "te",
		"trailer", "upgrade", "proxy-authenticate", "proxy-authorization":
		return true
	default:
		return false
	}
}
