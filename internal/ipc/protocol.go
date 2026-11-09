// Package ipc 提供 daemon 与 TUI 之间的本机 Unix socket 协议。
package ipc

import "encoding/json"

const Version = 1

type Request struct {
	V         int    `json:"v"`
	Cmd       string `json:"cmd"`
	Range     string `json:"range,omitempty"`
	Dimension string `json:"dimension,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Width     int    `json:"width,omitempty"`
	From      int64  `json:"from,omitempty"`
	To        int64  `json:"to,omitempty"`
}

type Response struct {
	V     int             `json:"v"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type Aggregate struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	CacheHitRate     float64 `json:"cache_hit_rate"`
	Cost             float64 `json:"cost"`
	UnpricedRequests int     `json:"unpriced_requests"`
}

type Balance struct {
	Has                bool           `json:"has"`
	Total              float64        `json:"total"`
	Granted            float64        `json:"granted"`
	ToppedUp           float64        `json:"topped_up"`
	Trend24h           []BalancePoint `json:"trend_24h,omitempty"`
	Trend7d            []BalancePoint `json:"trend_7d,omitempty"`
	Error              string         `json:"error,omitempty"`
	Failures           int            `json:"failures"`
	LastAttemptUnix    int64          `json:"last_attempt_unix"`
	LastSuccessUnix    int64          `json:"last_success_unix"`
	NextRefreshSeconds float64        `json:"next_refresh_seconds"`
}

type BalancePoint struct {
	Unix    int64   `json:"unix"`
	Value   float64 `json:"value"`
	Missing bool    `json:"missing,omitempty"`
}

type Stats struct {
	ProxyRunning        bool         `json:"proxy_running"`
	ListenAddr          string       `json:"listen_addr"`
	BalanceLowThreshold float64      `json:"balance_low_threshold"`
	ActivityCardEnabled bool         `json:"activity_card_enabled"`
	Health              bool         `json:"health"`
	Balance             Balance      `json:"balance"`
	Today               Aggregate    `json:"today"`
	Recent              []RequestRow `json:"recent"`
}

type Trace struct {
	Activity             []int           `json:"activity"`
	ActivityBucketSecs   int             `json:"activity_bucket_seconds"`
	ActivityFullScale    int             `json:"activity_full_scale"`
	Traffic              []TrafficBucket `json:"traffic"`
	TrafficBucketSecs    float64         `json:"traffic_bucket_seconds"`
	Shift                float64         `json:"shift"`
	Grid                 []float64       `json:"grid"`
	DownRate             float64         `json:"down_rate"`
	UpRate               float64         `json:"up_rate"`
	DownScale            float64         `json:"down_scale"`
	UpScale              float64         `json:"up_scale"`
	DownTop              float64         `json:"down_top"`
	UpTop                float64         `json:"up_top"`
	DownTotal            int64           `json:"down_total"`
	UpTotal              int64           `json:"up_total"`
	NowUnixNano          int64           `json:"now_unix_nano"`
	LastActivityUnixNano int64           `json:"last_activity_unix_nano,omitempty"`
	Health               bool            `json:"health"`
}

type TrafficBucket struct {
	Upload   int `json:"upload"`
	Download int `json:"download"`
}

type SeriesPoint struct {
	StartUnix  int64   `json:"start_unix"`
	Series     string  `json:"series"`
	Requests   int     `json:"requests"`
	HitTokens  int     `json:"hit_tokens"`
	MissTokens int     `json:"miss_tokens"`
	OutTokens  int     `json:"out_tokens"`
	Cost       float64 `json:"cost"`
}

type Series struct {
	Range     string        `json:"range"`
	Dimension string        `json:"dimension"`
	Bucket    string        `json:"bucket"`
	FromUnix  int64         `json:"from_unix"`
	ToUnix    int64         `json:"to_unix"`
	Points    []SeriesPoint `json:"points"`
}

type RequestRow struct {
	ID               string  `json:"id"`
	TimestampUnix    int64   `json:"timestamp_unix"`
	Model            string  `json:"model"`
	Client           string  `json:"client"`
	Endpoint         string  `json:"endpoint"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	LatencyMS        float64 `json:"latency_ms"`
	StatusCode       int     `json:"status_code"`
	Cost             float64 `json:"cost"`
}

type Requests struct {
	Total  int          `json:"total"`
	Offset int          `json:"offset"`
	Rows   []RequestRow `json:"rows"`
}

type Pricing struct {
	Count           int    `json:"count"`
	LatestValidUnix int64  `json:"latest_valid_unix"`
	LatestSource    string `json:"latest_source"`
}
