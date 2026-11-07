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
		return "未设置 API key"
	case ReasonInvalidResponse:
		return "余额接口响应无效"
	case ReasonParseFailed:
		return "余额数据解析失败"
	case ReasonAuth:
		if e.StatusCode == 403 {
			return "API key 没有余额查询权限"
		}
		return "API key 无效或已过期"
	case ReasonRateLimited:
		return "余额查询被限流"
	case ReasonServiceDown:
		return "余额服务暂时不可用"
	case ReasonHTTP:
		return fmt.Sprintf("余额查询失败（HTTP %d）", e.StatusCode)
	case ReasonTimedOut:
		return "余额查询超时"
	case ReasonOffline:
		return "网络不可用"
	case ReasonNetwork:
		if e.Detail != "" {
			return "网络错误：" + e.Detail
		}
		return "网络错误"
	case ReasonStorage:
		return "余额已取到，但写入采样失败"
	default:
		return "余额查询失败"
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
