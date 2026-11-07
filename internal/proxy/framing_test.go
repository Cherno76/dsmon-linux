package proxy

import (
	"net/http"
	"testing"
)

func header(values ...string) http.Header {
	result := http.Header{}
	for index := 0; index+1 < len(values); index += 2 {
		result.Add(values[index], values[index+1])
	}
	return result
}

func TestResolveBodyFraming(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		kind    framingKind
		length  int64
	}{
		{name: "lowercase", headers: header("content-length", "34"), kind: framingFixed, length: 34},
		{name: "spaces", headers: header("CONTENT-LENGTH", " 56 "), kind: framingFixed, length: 56},
		{name: "negative clamps", headers: header("Content-Length", "-5"), kind: framingFixed, length: 0},
		{name: "invalid ignored", headers: header("Content-Length", "abc"), kind: framingNone},
		{name: "no body", headers: http.Header{}, kind: framingNone},
		{name: "chunked", headers: header("Transfer-Encoding", "chunked"), kind: framingChunked},
		{name: "chunked list", headers: header("transfer-encoding", "gzip, chunked"), kind: framingChunked},
		{name: "chunked wins", headers: header("Transfer-Encoding", "chunked", "Content-Length", "10"), kind: framingChunked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			framing := ResolveBodyFraming(test.headers)
			if framing.Kind() != test.kind {
				t.Fatalf("kind = %v, want %v", framing.Kind(), test.kind)
			}
			if test.kind == framingFixed && framing.Length() != test.length {
				t.Fatalf("length = %d, want %d", framing.Length(), test.length)
			}
		})
	}
}

func TestBodyFramingCompleteness(t *testing.T) {
	fixed := FixedBodyFraming(10)
	if fixed.IsComplete(9) || !fixed.IsComplete(10) || !fixed.IsComplete(11) {
		t.Fatal("fixed framing completeness is wrong")
	}
	if !NoBodyFraming().IsComplete(0) {
		t.Fatal("no body should be complete immediately")
	}
	if ChunkedBodyFraming().IsComplete(999_999) {
		t.Fatal("chunked must not be treated as complete without a decoder")
	}
}

func TestProxyReceiveProgress(t *testing.T) {
	var progress ReceiveProgress
	const headerBytes = 100
	const bodyBytes = 20 * 1024 * 1024
	const chunk = 64 * 1024
	progress.Append(int64(chunk))
	progress.HeaderComplete(chunk-headerBytes, header("Content-Length", "20971520"))
	if progress.ReceivedBodyBytes() != chunk-headerBytes || progress.IsBodyComplete() {
		t.Fatalf("initial progress = %+v", progress)
	}
	remaining := bodyBytes - (chunk - headerBytes)
	for remaining > 0 {
		n := chunk
		if remaining < n {
			n = remaining
		}
		progress.Append(int64(n))
		remaining -= n
	}
	if progress.ReceivedBodyBytes() != bodyBytes || !progress.IsBodyComplete() {
		t.Fatalf("complete progress = %+v", progress)
	}
}

func TestReceiveProgressHeaderLimits(t *testing.T) {
	var fragmented ReceiveProgress
	fragmented.Append(40 * 1024)
	fragmented.Append(40 * 1024)
	if !fragmented.HeaderTooLarge() {
		t.Fatal("fragmented oversized header was not detected")
	}
	var oneRead ReceiveProgress
	oneRead.Append(70 * 1024)
	oneRead.HeaderComplete(0, header("Content-Length", "0"))
	if !oneRead.HeaderTooLarge() {
		t.Fatal("single-read oversized header was not detected")
	}
	var normal ReceiveProgress
	normal.Append(800)
	normal.HeaderComplete(0, header("Content-Length", "0"))
	if normal.HeaderTooLarge() {
		t.Fatal("normal header was flagged")
	}
}

func TestReceiveProgressRepeatedHeaderAccountingDoesNotDrift(t *testing.T) {
	var progress ReceiveProgress
	progress.Append(10)
	progress.HeaderComplete(0, header("Content-Length", "5"))
	progress.Append(5)
	progress.HeaderComplete(5, header("Content-Length", "999"))
	if progress.ReceivedBodyBytes() != 5 || !progress.IsBodyComplete() {
		t.Fatalf("progress drifted: %+v", progress)
	}
}

func TestConnectionCloseAndReuse(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		want    bool
	}{
		{name: "default", headers: http.Header{}, want: false},
		{name: "keep alive", headers: header("Connection", "keep-alive"), want: false},
		{name: "close", headers: header("Connection", "close"), want: true},
		{name: "case", headers: header("connection", "Close"), want: true},
		{name: "token", headers: header("Connection", "TE, close"), want: true},
		{name: "suffix", headers: header("Connection", "close-ish"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClientWantsClose(test.headers); got != test.want {
				t.Fatalf("wants close = %v, want %v", got, test.want)
			}
		})
	}
	if !CanReuseConnection(false, false, true) || CanReuseConnection(false, true, true) ||
		CanReuseConnection(false, false, false) || CanReuseConnection(true, false, true) {
		t.Fatal("connection reuse rules are wrong")
	}
}
