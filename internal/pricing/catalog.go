package pricing

import (
	"sort"
	"time"
)

// HourRange 是半开区间 [Start, End)，按北京时间整点。
type HourRange struct {
	Start int
	End   int
}

// PriceRate 是单档费率。
type PriceRate struct {
	Miss float64
	Hit  float64
	Out  float64
}

type DayPricing struct {
	Offpeak PriceRate
	Peak    *PriceRate
}

// ModelPricing 描述单个模型的峰谷价。Weekend 为 nil 表示周末沿用工作日两档。
type ModelPricing struct {
	Weekday DayPricing
	Weekend *DayPricing
}

// Catalog 是一份生效中的完整计费表：模型表 + 峰时窗口。
type Catalog struct {
	Table            map[string]ModelPricing
	WeekdayPeakHours []HourRange
	WeekendPeakHours []HourRange
}

// EmptyCatalog 返回一张空表；未知模型按 0 元计，而不是猜测价格。
func EmptyCatalog() Catalog {
	return Catalog{Table: map[string]ModelPricing{}}
}

// NewCatalog 从已解析的规则构造计费表。strategy 为 nil 时返回空表。
func NewCatalog(strategy *Strategy) Catalog {
	if strategy == nil {
		return EmptyCatalog()
	}
	table := make(map[string]ModelPricing, len(strategy.DeepSeek.Models))
	for model, buckets := range strategy.DeepSeek.Models {
		if buckets.Offpeak == nil {
			continue
		}
		weekday := DayPricing{
			Offpeak: convertRate(*buckets.Offpeak),
			Peak:    convertRatePtr(buckets.Peak),
		}
		var weekend *DayPricing
		if buckets.WeekendOffpeak != nil || buckets.WeekendPeak != nil {
			offpeak := buckets.WeekendOffpeak
			if offpeak == nil {
				offpeak = buckets.Offpeak
			}
			peak := buckets.WeekendPeak
			if peak == nil {
				peak = buckets.Peak
			}
			weekend = &DayPricing{Offpeak: convertRate(*offpeak), Peak: convertRatePtr(peak)}
		}
		table[model] = ModelPricing{Weekday: weekday, Weekend: weekend}
	}

	var hours *PeakHours
	if strategy.DeepSeek.PeakHours != nil {
		hours = strategy.DeepSeek.PeakHours
	}
	return Catalog{
		Table:            table,
		WeekdayPeakHours: convertHours(peakHourSlice(hours, true)),
		WeekendPeakHours: convertHours(peakHourSlice(hours, false)),
	}
}

func peakHourSlice(hours *PeakHours, weekday bool) [][]int {
	if hours == nil {
		return nil
	}
	if weekday {
		return hours.Weekday
	}
	return hours.Weekend
}

func convertRate(rate Rate) PriceRate {
	return PriceRate{Miss: rate.Miss, Hit: rate.Hit, Out: rate.Out}
}

func convertRatePtr(rate *Rate) *PriceRate {
	if rate == nil {
		return nil
	}
	converted := convertRate(*rate)
	return &converted
}

func convertHours(raw [][]int) []HourRange {
	out := make([]HourRange, 0, len(raw))
	for _, pair := range raw {
		if len(pair) != 2 || pair[0] < 0 || pair[1] > 24 || pair[0] > pair[1] {
			continue
		}
		out = append(out, HourRange{Start: pair[0], End: pair[1]})
	}
	return out
}

// ModelNames 返回排序后的模型名，供设置页展示。
func (c Catalog) ModelNames() []string {
	names := make([]string, 0, len(c.Table))
	for name := range c.Table {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PricingFor 先精确匹配，再按最长前缀匹配。日期后缀不能把 vision 型号误判成 flash。
func (c Catalog) PricingFor(model string) (ModelPricing, bool) {
	if exact, ok := c.Table[model]; ok {
		return exact, true
	}
	best := ""
	for key := range c.Table {
		if len(key) <= len(best) || len(key) > len(model) {
			continue
		}
		if model[:len(key)] == key {
			best = key
		}
	}
	if best == "" {
		return ModelPricing{}, false
	}
	value, ok := c.Table[best]
	return value, ok
}

func (c Catalog) IsPriced(model string) bool {
	_, ok := c.PricingFor(model)
	return ok
}

// IsPeak 按北京时间判断某时刻是否落在对应日类型的峰时窗口里。
func (c Catalog) IsPeak(at time.Time) bool {
	hour := BeijingHour(at)
	windows := c.WeekdayPeakHours
	if IsBeijingWeekend(at) {
		windows = c.WeekendPeakHours
	}
	for _, window := range windows {
		if hour >= window.Start && hour < window.End {
			return true
		}
	}
	return false
}

// RateFor 返回某模型在该时刻适用的费率。
func (c Catalog) RateFor(model string, at time.Time) (PriceRate, bool) {
	pricing, ok := c.PricingFor(model)
	if !ok {
		return PriceRate{}, false
	}
	return pricing.RateAt(at, c.IsPeak(at)), true
}

// Cost 计算 token 费用；未知模型返回 0。
func (c Catalog) Cost(model string, promptTokens, completionTokens, cachedTokens int, at time.Time) float64 {
	rate, ok := c.RateFor(model, at)
	if !ok {
		return 0
	}
	return ComputeCost(promptTokens, completionTokens, cachedTokens, rate)
}

// RateAt 选择该时刻生效的档位。
func (m ModelPricing) RateAt(at time.Time, isPeak bool) PriceRate {
	day := m.Weekday
	if IsBeijingWeekend(at) && m.Weekend != nil {
		day = *m.Weekend
	}
	if isPeak && day.Peak != nil {
		return *day.Peak
	}
	return day.Offpeak
}

// ComputeCost 区分缓存命中与未命中的输入。缓存数大于输入数时，
// 未命中部分按 0 计，但命中部分仍按缓存数计，和 Mac 版保持一致。
func ComputeCost(promptTokens, completionTokens, cachedTokens int, rate PriceRate) float64 {
	missTokens := promptTokens - cachedTokens
	if missTokens < 0 {
		missTokens = 0
	}
	return float64(missTokens)/1_000_000*rate.Miss +
		float64(cachedTokens)/1_000_000*rate.Hit +
		float64(completionTokens)/1_000_000*rate.Out
}
