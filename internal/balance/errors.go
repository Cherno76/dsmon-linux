package balance

import "fmt"

type Reason string

const (
	ReasonNoAPIKey        Reason = "no_api_key"
	ReasonInvalidResponse Reason = "invalid_response"
	ReasonParseFailed     Reason = "parse_failed"
	ReasonAuth            Reason = "auth"
	ReasonRateLimited     Reason = "rate_limited"
	ReasonServiceDown     Reason = "service_down"
	ReasonHTTP            Reason = "http"
	ReasonTimedOut        Reason = "timed_out"
	ReasonOffline         Reason = "offline"
	ReasonNetwork         Reason = "network"
	ReasonStorage         Reason = "storage"
)

// QueryError 保留失败分类，TUI 只显示 Message()，测试和状态栏可以按 Reason 判断。
type QueryError struct {
	Reason     Reason
	StatusCode int
	Detail     string
}

func (e *QueryError) Error() string { return e.Message() }

func (e *QueryError) Message() string {
	if e == nil {
		return ""
	}
	switch e.Reason {
	case ReasonNoAPIKey:
		return "API key is not set"
	case ReasonInvalidResponse:
		return "invalid balance response"
	case ReasonParseFailed:
		return "failed to parse balance data"
	case ReasonAuth:
		if e.StatusCode == 403 {
			return "API key is not allowed to query balance"
		}
		return "API key is invalid or expired"
	case ReasonRateLimited:
		return "balance query was rate limited"
	case ReasonServiceDown:
		return "balance service is temporarily unavailable"
	case ReasonHTTP:
		return fmt.Sprintf("balance query failed (HTTP %d)", e.StatusCode)
	case ReasonTimedOut:
		return "balance query timed out"
	case ReasonOffline:
		return "network unavailable"
	case ReasonNetwork:
		if e.Detail != "" {
			return "network error: " + e.Detail
		}
		return "network error"
	case ReasonStorage:
		return "balance fetched, but writing the sample failed"
	default:
		return "balance query failed"
	}
}

func statusError(statusCode int) *QueryError {
	switch {
	case statusCode == 401 || statusCode == 403:
		return &QueryError{Reason: ReasonAuth, StatusCode: statusCode}
	case statusCode == 429:
		return &QueryError{Reason: ReasonRateLimited, StatusCode: statusCode}
	case statusCode >= 500 && statusCode <= 599:
		return &QueryError{Reason: ReasonServiceDown, StatusCode: statusCode}
	default:
		return &QueryError{Reason: ReasonHTTP, StatusCode: statusCode}
	}
}
