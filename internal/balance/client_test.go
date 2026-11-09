package balance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func responseWith(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func testClient(t *testing.T, transport http.RoundTripper, key string) *Client {
	t.Helper()
	client, err := NewClient("https://api.deepseek.com/user/balance", key, "Bearer",
		&http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientReadsBalanceAndSendsBearerAuth(t *testing.T) {
	var captured *http.Request
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		return responseWith(200, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"12.34","granted_balance":"2.00","topped_up_balance":"10.34"}]}`), nil
	}), "sk-test-only")

	snapshot, err := client.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 12.34 || snapshot.Granted != 2 || snapshot.ToppedUp != 10.34 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if captured == nil || captured.URL.String() != "https://api.deepseek.com/user/balance" ||
		captured.Header.Get("Authorization") != "Bearer sk-test-only" ||
		captured.Header.Get("Accept") != "application/json" {
		t.Fatalf("request = %+v", captured)
	}
}

func TestClientRejectsEmptyKeyWithoutNetwork(t *testing.T) {
	called := false
	client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return responseWith(200, `{}`), nil
	}), "")
	_, err := client.Query(context.Background())
	if called {
		t.Fatal("empty key still touched the network")
	}
	assertReason(t, err, ReasonNoAPIKey)
}

func TestClientMapsHTTPStatuses(t *testing.T) {
	tests := []struct {
		status int
		reason Reason
	}{
		{status: 401, reason: ReasonAuth},
		{status: 403, reason: ReasonAuth},
		{status: 429, reason: ReasonRateLimited},
		{status: 500, reason: ReasonServiceDown},
		{status: 503, reason: ReasonServiceDown},
		{status: 418, reason: ReasonHTTP},
		{status: 302, reason: ReasonHTTP},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return responseWith(test.status, `{"error":"x"}`), nil
			}), "sk-test-only")
			_, err := client.Query(context.Background())
			assertReason(t, err, test.reason)
			var queryErr *QueryError
			if !errors.As(err, &queryErr) || queryErr.StatusCode != test.status {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}

func TestClientMapsNetworkFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason Reason
	}{
		{name: "deadline", err: context.DeadlineExceeded, reason: ReasonTimedOut},
		{name: "net timeout", err: timeoutError{}, reason: ReasonTimedOut},
		{name: "offline", err: syscall.ENETUNREACH, reason: ReasonOffline},
		{name: "host unreachable", err: syscall.EHOSTUNREACH, reason: ReasonOffline},
		{name: "refused", err: syscall.ECONNREFUSED, reason: ReasonNetwork},
		{name: "generic", err: errors.New("dial failed"), reason: ReasonNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: "Get", URL: "https://api.deepseek.com/user/balance", Err: test.err}
			}), "sk-test-only")
			_, err := client.Query(context.Background())
			assertReason(t, err, test.reason)
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClientTimesOutSlowTransport(t *testing.T) {
	client := testClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}), "sk-test-only")
	client.Timeout = 10 * time.Millisecond
	_, err := client.Query(context.Background())
	assertReason(t, err, ReasonTimedOut)
}

func TestClientRejectsOversizedBody(t *testing.T) {
	client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return responseWith(200, strings.Repeat("x", 64)), nil
	}), "sk-test-only")
	client.MaxBodyBytes = 32
	_, err := client.Query(context.Background())
	assertReason(t, err, ReasonInvalidResponse)
}

func TestParseDeepSeekBalance(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    Snapshot
		wantErr Reason
	}{
		{
			name: "strings",
			body: `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"12.34","granted_balance":"2","topped_up_balance":"10.34"}]}`,
			want: Snapshot{Total: 12.34, Granted: 2, ToppedUp: 10.34},
		},
		{
			name: "numbers and multiple entries",
			body: `{"is_available":true,"balance_infos":[{"total_balance":1,"granted_balance":0.5,"topped_up_balance":0.5},{"total_balance":"2.25","granted_balance":"0","topped_up_balance":2.25}]}`,
			want: Snapshot{Total: 3.25, Granted: 0.5, ToppedUp: 2.75},
		},
		{name: "empty entries", body: `{"is_available":true,"balance_infos":[]}`, want: Snapshot{}},
		{name: "unavailable", body: `{"is_available":false,"balance_infos":[]}`, wantErr: ReasonParseFailed},
		{name: "missing availability", body: `{"balance_infos":[]}`, wantErr: ReasonParseFailed},
		{name: "missing infos", body: `{"is_available":true}`, wantErr: ReasonParseFailed},
		{name: "null infos", body: `{"is_available":true,"balance_infos":null}`, wantErr: ReasonParseFailed},
		{name: "wrong infos type", body: `{"is_available":true,"balance_infos":{}}`, wantErr: ReasonParseFailed},
		{name: "not json", body: `not json`, wantErr: ReasonParseFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseDeepSeekBalance([]byte(test.body))
			if test.wantErr != "" {
				assertReason(t, err, test.wantErr)
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ParseDeepSeekBalance() = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
}

func TestNewClientRejectsBadURL(t *testing.T) {
	for _, raw := range []string{"", "not a url", "/user/balance", "ftp://example.com/balance"} {
		if _, err := NewClient(raw, "key", "Bearer", nil); err == nil {
			t.Fatalf("URL %q was accepted", raw)
		}
	}
}

func TestQueryErrorMessage(t *testing.T) {
	auth := &QueryError{Reason: ReasonAuth, StatusCode: 403}
	if !strings.Contains(auth.Message(), "allowed") {
		t.Fatalf("message = %q", auth.Message())
	}
	httpError := &QueryError{Reason: ReasonHTTP, StatusCode: 418}
	if !bytes.Contains([]byte(httpError.Message()), []byte("418")) {
		t.Fatalf("message = %q", httpError.Message())
	}
}

func assertReason(t *testing.T, err error, reason Reason) {
	t.Helper()
	var queryErr *QueryError
	if !errors.As(err, &queryErr) || queryErr.Reason != reason {
		t.Fatalf("error = %#v, want reason %q", err, reason)
	}
}
