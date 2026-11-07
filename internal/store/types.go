package store

import "time"

// UsageRecord 是一条待落库的请求用量。Cost 与 pricing_revision 由 store 计算。
type UsageRecord struct {
	UUID             string
	Timestamp        time.Time
	ProviderID       string
	Model            string
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
	LatencyMS        float64
	StatusCode       int
	UserAgent        string
	FixedCost        float64
}

type AggregatedUsage struct {
	RequestCount     int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
	AvgLatencyMS     float64
	EstimatedCost    float64
	UnpricedRequests int
}

// CacheHitRate 返回百分数（0~100），与 Mac 版界面一致。
func (u AggregatedUsage) CacheHitRate() float64 {
	if u.PromptTokens <= 0 {
		return 0
	}
	return float64(u.CachedTokens) / float64(u.PromptTokens) * 100
}

type CostSummary struct {
	Cost             float64
	UnpricedRequests int
}

type PreviousWindowUsage struct {
	Tokens   int
	Requests int
	HitRate  *float64
	Cost     float64
}

type MenuBarSnapshot struct {
	LatestCacheHitRate *float64
	TodayCacheHitRate  *float64
	TodayCost          CostSummary
	TodayTokens        int
	TodayRequests      int
	PreviousWindow     *PreviousWindowUsage
}

type WindowTokenTotals struct {
	Today int
	Week  int
	Month int
}

type ClientUsage struct {
	Client           string
	RequestCount     int
	PromptTokens     int
	CachedTokens     int
	CompletionTokens int
	TotalTokens      int
	Cost             float64
	UnpricedRequests int
}

func (u ClientUsage) CacheHitRate() float64 {
	if u.PromptTokens <= 0 {
		return 0
	}
	return float64(u.CachedTokens) / float64(u.PromptTokens) * 100
}

type RecentRequestRow struct {
	ID         string
	Timestamp  time.Time
	Model      string
	LatencyMS  float64
	StatusCode int
	UserAgent  string
}

type RequestRow struct {
	ID               string
	Timestamp        time.Time
	Model            string
	Client           string
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
	LatencyMS        float64
	StatusCode       int
	Cost             float64
	UserAgent        string
}

type TokenBar struct {
	Label            string
	MissTokens       int
	HitTokens        int
	OutTokens        int
	RequestCount     int
	Cost             float64
	UnpricedRequests int
}

func (b TokenBar) TotalTokens() int {
	return b.MissTokens + b.HitTokens + b.OutTokens
}

func (b TokenBar) HasData() bool {
	return b.TotalTokens() > 0 || b.Cost > 0
}

type BalanceSample struct {
	Timestamp time.Time
	Balance   float64
	Granted   float64
	ToppedUp  float64
}

type UsageBucket string

const (
	BucketHour UsageBucket = "hour"
	BucketDay  UsageBucket = "day"
)

type UsageDimension string

const (
	DimensionModel  UsageDimension = "model"
	DimensionClient UsageDimension = "client"
)

type UsageSeriesPoint struct {
	BucketStart time.Time
	Series      string
	Requests    int
	HitTokens   int
	MissTokens  int
	OutTokens   int
	Cost        float64
}

func (p UsageSeriesPoint) TokenCount() int {
	return p.HitTokens + p.MissTokens + p.OutTokens
}
