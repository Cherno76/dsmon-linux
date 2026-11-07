package proxy

import (
	"net/http"
	"strconv"
	"strings"
)

type framingKind int

const (
	framingNone framingKind = iota
	framingFixed
	framingChunked
)

// BodyFraming 描述客户端请求体的长度语义。实际 HTTP 解析交给 net/http，
// 这层仍保留给边界测试和显式拒绝声明超限的请求。
type BodyFraming struct {
	kind   framingKind
	length int64
}

func NoBodyFraming() BodyFraming { return BodyFraming{kind: framingNone} }
func FixedBodyFraming(length int64) BodyFraming {
	return BodyFraming{kind: framingFixed, length: maxInt64(0, length)}
}
func ChunkedBodyFraming() BodyFraming { return BodyFraming{kind: framingChunked} }

func (f BodyFraming) Kind() framingKind { return f.kind }
func (f BodyFraming) Length() int64     { return f.length }

func (f BodyFraming) IsComplete(receivedBodyBytes int64) bool {
	switch f.kind {
	case framingNone:
		return true
	case framingFixed:
		return receivedBodyBytes >= f.length
	default:
		return false
	}
}

func (f BodyFraming) DeclaredLength() (int64, bool) {
	return f.length, f.kind == framingFixed
}

func ResolveBodyFraming(headers http.Header) BodyFraming {
	if IsChunked(headers) {
		return ChunkedBodyFraming()
	}
	if length, ok := ContentLength(headers); ok {
		return FixedBodyFraming(length)
	}
	return NoBodyFraming()
}

func ContentLength(headers http.Header) (int64, bool) {
	for key, values := range headers {
		if !strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			length, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil {
				return length, true
			}
		}
	}
	return 0, false
}

func IsChunked(headers http.Header) bool {
	for key, values := range headers {
		if !strings.EqualFold(key, "Transfer-Encoding") {
			continue
		}
		for _, value := range values {
			if strings.Contains(strings.ToLower(value), "chunked") {
				return true
			}
		}
	}
	return false
}

type ReceiveProgress struct {
	appendedBytes int64
	headerBytes   *int64
	framing       BodyFraming
}

const MaxHeaderBytes = 64 * 1024

func (p *ReceiveProgress) Append(received int64) {
	if received > 0 {
		p.appendedBytes += received
	}
}

func (p *ReceiveProgress) HeaderComplete(bodyBytesInMessage int64, headers http.Header) {
	if p.headerBytes != nil {
		return
	}
	value := p.appendedBytes - bodyBytesInMessage
	if value < 0 {
		value = 0
	}
	p.headerBytes = &value
	p.framing = ResolveBodyFraming(headers)
}

func (p *ReceiveProgress) HeaderTooLarge() bool {
	if p.headerBytes != nil {
		return *p.headerBytes > MaxHeaderBytes
	}
	return p.appendedBytes > MaxHeaderBytes
}

func (p *ReceiveProgress) ReceivedBodyBytes() int64 {
	if p.headerBytes == nil {
		return 0
	}
	value := p.appendedBytes - *p.headerBytes
	if value < 0 {
		return 0
	}
	return value
}

func (p *ReceiveProgress) IsBodyComplete() bool {
	return p.headerBytes != nil && p.framing.IsComplete(p.ReceivedBodyBytes())
}

func ClientWantsClose(headers http.Header) bool {
	for key, values := range headers {
		if !strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				if strings.EqualFold(strings.TrimSpace(token), "close") {
					return true
				}
			}
		}
	}
	return false
}

func CanReuseConnection(clientWantsClose, responseIsStreaming, responseHasContentLength bool) bool {
	return !clientWantsClose && !responseIsStreaming && responseHasContentLength
}

func maxInt64(first, second int64) int64 {
	if first > second {
		return first
	}
	return second
}
