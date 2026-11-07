package balance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultTimeout          = 8 * time.Second
	DefaultMaxResponseBytes = 1 * 1024 * 1024
	DefaultBalancePath      = "/user/balance"
	DefaultAuthPrefix       = "Bearer"
)

type Snapshot struct {
	Total    float64
	Granted  float64
	ToppedUp float64
}

type Client struct {
	URL          string
	APIKey       string
	AuthPrefix   string
	HTTPClient   *http.Client
	Timeout      time.Duration
	MaxBodyBytes int64
	UserAgent    string
}

func NewClient(balanceURL, apiKey, authPrefix string, httpClient *http.Client) (*Client, error) {
	if err := validateURL(balanceURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(authPrefix) == "" {
		authPrefix = DefaultAuthPrefix
	}
	if httpClient == nil {
		httpClient = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Client{
		URL:          balanceURL,
		APIKey:       apiKey,
		AuthPrefix:   authPrefix,
		HTTPClient:   httpClient,
		Timeout:      DefaultTimeout,
		MaxBodyBytes: DefaultMaxResponseBytes,
		UserAgent:    "dsmon/0.1",
	}, nil
}

func validateURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse balance URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("balance URL %q must be absolute http(s)", raw)
	}
	return nil
}

func (c *Client) Query(ctx context.Context) (Snapshot, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return Snapshot{}, &QueryError{Reason: ReasonNoAPIKey}
	}
	if c.HTTPClient == nil {
		return Snapshot{}, &QueryError{Reason: ReasonInvalidResponse, Detail: "HTTP client is nil"}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.URL, nil)
	if err != nil {
		return Snapshot{}, &QueryError{Reason: ReasonInvalidResponse, Detail: err.Error()}
	}
	request.Header.Set("Authorization", c.AuthPrefix+" "+c.APIKey)
	request.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		request.Header.Set("User-Agent", c.UserAgent)
	}

	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return Snapshot{}, classifyNetworkError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, statusError(response.StatusCode)
	}

	maxBody := c.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return Snapshot{}, classifyNetworkError(err)
	}
	if int64(len(body)) > maxBody {
		return Snapshot{}, &QueryError{Reason: ReasonInvalidResponse, Detail: "balance response is too large"}
	}
	return ParseDeepSeekBalance(body)
}

func ParseDeepSeekBalance(body []byte) (Snapshot, error) {
	var payload struct {
		Available *bool           `json:"is_available"`
		Infos     json.RawMessage `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Snapshot{}, &QueryError{Reason: ReasonParseFailed, Detail: err.Error()}
	}
	if payload.Available == nil || !*payload.Available {
		return Snapshot{}, &QueryError{Reason: ReasonParseFailed, Detail: "is_available is false"}
	}
	if len(payload.Infos) == 0 || string(payload.Infos) == "null" {
		return Snapshot{}, &QueryError{Reason: ReasonParseFailed, Detail: "balance_infos is missing"}
	}
	var infos []map[string]any
	if err := json.Unmarshal(payload.Infos, &infos); err != nil {
		return Snapshot{}, &QueryError{Reason: ReasonParseFailed, Detail: err.Error()}
	}

	var snapshot Snapshot
	for _, info := range infos {
		if value, ok := balanceNumber(info["total_balance"]); ok {
			snapshot.Total += value
		}
		if value, ok := balanceNumber(info["granted_balance"]); ok {
			snapshot.Granted += value
		}
		if value, ok := balanceNumber(info["topped_up_balance"]); ok {
			snapshot.ToppedUp += value
		}
	}
	return snapshot, nil
}

func balanceNumber(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	case float64:
		number = typed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

func classifyNetworkError(err error) *QueryError {
	if errors.Is(err, context.DeadlineExceeded) {
		return &QueryError{Reason: ReasonTimedOut}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &QueryError{Reason: ReasonTimedOut}
	}
	if errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTDOWN) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETRESET) {
		return &QueryError{Reason: ReasonOffline}
	}
	return &QueryError{Reason: ReasonNetwork, Detail: err.Error()}
}
