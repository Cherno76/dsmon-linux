package store

import (
	"fmt"
	"time"
)

// UsagePeriod 是「用量信息」页的六个预设时间维度。
type UsagePeriod string

const (
	PeriodToday     UsagePeriod = "today"
	PeriodYesterday UsagePeriod = "yesterday"
	PeriodLast7     UsagePeriod = "last7Days"
	PeriodLast30    UsagePeriod = "last30Days"
	PeriodThisMonth UsagePeriod = "thisMonth"
	PeriodLastMonth UsagePeriod = "lastMonth"
	PeriodCustom    UsagePeriod = "custom"
)

var AllUsagePeriods = []UsagePeriod{
	PeriodToday, PeriodYesterday, PeriodLast7, PeriodLast30,
	PeriodThisMonth, PeriodLastMonth,
}

func (p UsagePeriod) NeedsCustomRange() bool { return p == PeriodCustom }

// TimeRange 是半开区间 [Start, End)。
type TimeRange struct {
	Start time.Time
	End   time.Time
}

func (r TimeRange) Duration() time.Duration { return r.End.Sub(r.Start) }

// CustomInterval 把两个日期按整天对齐，并容忍用户把起止选反。
func CustomInterval(first, second time.Time, loc *time.Location) TimeRange {
	if loc == nil {
		loc = time.Local
	}
	if first.After(second) {
		first, second = second, first
	}
	start := startOfDay(first, loc)
	lastDay := startOfDay(second, loc)
	return TimeRange{Start: start, End: lastDay.AddDate(0, 0, 1)}
}

// Interval 计算预设时间维度的实际区间。custom 只在 PeriodCustom 时使用。
func Interval(period UsagePeriod, custom *TimeRange, now time.Time, loc *time.Location) (TimeRange, bool) {
	if loc == nil {
		loc = time.Local
	}
	today := startOfDay(now, loc)
	switch period {
	case PeriodToday:
		return TimeRange{Start: today, End: today.AddDate(0, 0, 1)}, true
	case PeriodYesterday:
		return TimeRange{Start: today.AddDate(0, 0, -1), End: today}, true
	case PeriodLast7:
		return TimeRange{Start: today.AddDate(0, 0, -6), End: now}, true
	case PeriodLast30:
		return TimeRange{Start: today.AddDate(0, 0, -29), End: now}, true
	case PeriodThisMonth:
		start := MonthStart(now, loc)
		return TimeRange{Start: start, End: start.AddDate(0, 1, 0)}, true
	case PeriodLastMonth:
		thisMonth := MonthStart(now, loc)
		return TimeRange{Start: thisMonth.AddDate(0, -1, 0), End: thisMonth}, true
	case PeriodCustom:
		if custom == nil || !custom.Start.Before(custom.End) {
			return TimeRange{}, false
		}
		return *custom, true
	default:
		return TimeRange{}, false
	}
}

// BucketForInterval 按跨度选桶：一天窗口按小时，长窗口按天。
func BucketForInterval(r TimeRange) UsageBucket {
	if r.Duration() <= 48*time.Hour {
		return BucketHour
	}
	return BucketDay
}

func (b UsageBucket) Seconds() time.Duration {
	if b == BucketHour {
		return time.Hour
	}
	return 24 * time.Hour
}

func (b UsageBucket) KeyFormat() string {
	if b == BucketHour {
		return "2006-01-02 15"
	}
	return "2006-01-02"
}

// AxisDomain 返回图表横轴。今天/昨天固定撑满 24 小时；其余区间最多多盖一个桶。
func AxisDomain(period UsagePeriod, interval TimeRange, bucket UsageBucket, lastBucket *time.Time, loc *time.Location) TimeRange {
	if loc == nil {
		loc = time.Local
	}
	unit := bucket.Seconds()
	if period == PeriodToday || period == PeriodYesterday {
		return TimeRange{Start: interval.Start, End: interval.Start.AddDate(0, 0, 1)}
	}
	end := interval.End
	if lastBucket != nil && lastBucket.Add(unit).After(end) {
		end = lastBucket.Add(unit)
	}
	if minimum := interval.Start.Add(2 * unit); minimum.After(end) {
		end = minimum
	}
	return TimeRange{Start: interval.Start, End: end}
}

// HoverSnapNearest 按桶心吸附。边界点归右边那一格。
func HoverSnapNearest(at time.Time, buckets []time.Time, unit time.Duration) *time.Time {
	if len(buckets) == 0 {
		return nil
	}
	probe := at.Add(-unit / 2)
	best := buckets[0]
	bestDistance := absDuration(best.Sub(probe))
	for _, bucket := range buckets[1:] {
		distance := absDuration(bucket.Sub(probe))
		if distance <= bestDistance {
			best = bucket
			bestDistance = distance
		}
	}
	return &best
}

// DateRangeText 把区间写成实际日期；半开区间的末日要减一天。
func DateRangeText(r TimeRange) string {
	first := time.Date(r.Start.Year(), r.Start.Month(), r.Start.Day(), 0, 0, 0, 0, r.Start.Location())
	last := startOfDay(r.End.Add(-time.Nanosecond), r.End.Location())
	firstText := fmt.Sprintf("%d/%d/%d", first.Year(), int(first.Month()), first.Day())
	if first.Equal(last) {
		return firstText
	}
	lastText := fmt.Sprintf("%d/%d/%d", last.Year(), int(last.Month()), last.Day())
	return firstText + " – " + lastText
}

func absDuration(duration time.Duration) time.Duration {
	if duration < 0 {
		return -duration
	}
	return duration
}
