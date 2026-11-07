package store

import (
	"testing"
	"time"
)

var rangeLocation = time.FixedZone("CST", 8*3600)

var rangeNow = time.Date(2026, time.September, 16, 14, 30, 0, 0, rangeLocation)

func rangeDate(day int, hour int) time.Time {
	return time.Date(2026, time.September, day, hour, 0, 0, 0, rangeLocation)
}

func mustInterval(t *testing.T, period UsagePeriod, custom *TimeRange) TimeRange {
	t.Helper()
	value, ok := Interval(period, custom, rangeNow, rangeLocation)
	if !ok {
		t.Fatalf("Interval(%s) returned no range", period)
	}
	return value
}

func TestPeriodIntervals(t *testing.T) {
	tests := []struct {
		name   string
		period UsagePeriod
		start  time.Time
		end    time.Time
		bucket UsageBucket
	}{
		{"today", PeriodToday, rangeDate(16, 0), rangeDate(17, 0), BucketHour},
		{"yesterday", PeriodYesterday, rangeDate(15, 0), rangeDate(16, 0), BucketHour},
		{"last 7", PeriodLast7, rangeDate(10, 0), rangeNow, BucketDay},
		{"last 30", PeriodLast30, time.Date(2026, 8, 18, 0, 0, 0, 0, rangeLocation), rangeNow, BucketDay},
		{"this month", PeriodThisMonth, rangeDate(1, 0),
			time.Date(2026, time.October, 1, 0, 0, 0, 0, rangeLocation), BucketDay},
		{"last month", PeriodLastMonth, time.Date(2026, 8, 1, 0, 0, 0, 0, rangeLocation),
			rangeDate(1, 0), BucketDay},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mustInterval(t, tt.period, nil)
			if !got.Start.Equal(tt.start) || !got.End.Equal(tt.end) {
				t.Fatalf("interval = %s..%s, want %s..%s", got.Start, got.End, tt.start, tt.end)
			}
			if bucket := BucketForInterval(got); bucket != tt.bucket {
				t.Fatalf("bucket = %s, want %s", bucket, tt.bucket)
			}
		})
	}
}

func TestCustomPeriodRequiresValidRange(t *testing.T) {
	start := rangeNow.AddDate(0, 0, -3)
	custom := TimeRange{Start: start, End: rangeNow}
	if got, ok := Interval(PeriodCustom, &custom, rangeNow, rangeLocation); !ok || !got.Start.Equal(start) {
		t.Fatalf("custom interval = %+v ok=%v", got, ok)
	}
	if _, ok := Interval(PeriodCustom, nil, rangeNow, rangeLocation); ok {
		t.Fatal("custom without range should be invalid")
	}
	if _, ok := Interval(PeriodCustom, &TimeRange{Start: rangeNow, End: rangeNow}, rangeNow, rangeLocation); ok {
		t.Fatal("empty custom range should be invalid")
	}
}

func TestCustomIntervalSnapsToWholeDays(t *testing.T) {
	earlier := rangeNow.AddDate(0, 0, -3)
	rangeValue := CustomInterval(rangeNow, earlier, rangeLocation)
	if !rangeValue.Start.Equal(rangeDate(13, 0)) || !rangeValue.End.Equal(rangeDate(17, 0)) {
		t.Fatalf("custom interval = %+v", rangeValue)
	}
	if rangeValue.Duration() != 4*24*time.Hour {
		t.Fatalf("duration = %s", rangeValue.Duration())
	}
	sameDay := CustomInterval(rangeNow, rangeNow.Add(-2*time.Hour), rangeLocation)
	if sameDay.Duration() != 24*time.Hour {
		t.Fatalf("same day duration = %s", sameDay.Duration())
	}
}

func TestAllPeriodsAreOffered(t *testing.T) {
	if len(AllUsagePeriods) != 6 {
		t.Fatalf("periods = %v", AllUsagePeriods)
	}
	for _, period := range AllUsagePeriods {
		if period == PeriodCustom {
			t.Fatalf("custom range must not be offered as a preset: %v", AllUsagePeriods)
		}
		if period.NeedsCustomRange() != (period == PeriodCustom) {
			t.Fatalf("NeedsCustomRange(%s) = %v", period, period.NeedsCustomRange())
		}
	}
}

func TestCalendarPeriodsCoverWholeUnit(t *testing.T) {
	tests := []struct {
		name   string
		period UsagePeriod
		bucket UsageBucket
		count  int
	}{
		{"today 24 hours", PeriodToday, BucketHour, 24},
		{"yesterday 24 hours", PeriodYesterday, BucketHour, 24},
		{"this month days", PeriodThisMonth, BucketDay, 30},
		{"last month days", PeriodLastMonth, BucketDay, 31},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interval := mustInterval(t, tt.period, nil)
			if got := int(interval.Duration() / tt.bucket.Seconds()); got != tt.count {
				t.Fatalf("bucket count = %d, want %d: %+v", got, tt.count, interval)
			}
		})
	}
}

func TestAxisDomain(t *testing.T) {
	today := mustInterval(t, PeriodToday, nil)
	axis := AxisDomain(PeriodToday, today, BucketHour, nil, rangeLocation)
	if axis.Duration() != 24*time.Hour || !axis.Start.Equal(today.Start) {
		t.Fatalf("today axis = %+v", axis)
	}
	midnight := rangeDate(16, 0)
	short := TimeRange{Start: midnight, End: midnight.Add(2 * time.Minute)}
	axis = AxisDomain(PeriodToday, short, BucketHour, &midnight, rangeLocation)
	if axis.Duration() != 24*time.Hour {
		t.Fatalf("short today axis = %+v", axis)
	}
	week := mustInterval(t, PeriodLast7, nil)
	lastBucket := rangeDate(15, 0)
	axis = AxisDomain(PeriodLast7, week, BucketDay, &lastBucket, rangeLocation)
	if axis.End.Before(lastBucket.Add(24 * time.Hour)) {
		t.Fatalf("axis does not cover last bucket: %+v", axis)
	}
	if axis.End.Sub(week.End) >= 24*time.Hour {
		t.Fatalf("axis overshoots too far: %+v", axis)
	}
}

func TestHoverSnapNearest(t *testing.T) {
	day := rangeDate(16, 0)
	buckets := make([]time.Time, 5)
	for i := range buckets {
		buckets[i] = day.Add(time.Duration(i) * time.Hour)
	}
	tests := []struct {
		offset time.Duration
		want   int
	}{
		{3*time.Hour + time.Minute, 3},
		{3*time.Hour + 50*time.Minute, 3},
		{3*time.Hour + 3599*time.Second, 3},
		{4 * time.Hour, 4},
		{-30 * time.Second, 0},
	}
	for _, tt := range tests {
		got := HoverSnapNearest(day.Add(tt.offset), buckets, time.Hour)
		if got == nil || !got.Equal(buckets[tt.want]) {
			t.Fatalf("offset %s snapped to %v, want %v", tt.offset, got, buckets[tt.want])
		}
	}
	if HoverSnapNearest(day, nil, time.Hour) != nil {
		t.Fatal("empty buckets should return nil")
	}
	sparse := []time.Time{day, day.Add(5 * time.Hour)}
	if got := HoverSnapNearest(day.Add(2*time.Hour), sparse, time.Hour); got == nil || !got.Equal(sparse[0]) {
		t.Fatalf("sparse snap = %v", got)
	}
	if got := HoverSnapNearest(day.Add(4*time.Hour+24*time.Minute), sparse, time.Hour); got == nil || !got.Equal(sparse[1]) {
		t.Fatalf("sparse snap = %v", got)
	}
}

func TestBucketSecondsAreShared(t *testing.T) {
	if BucketHour.Seconds() != time.Hour || BucketDay.Seconds() != 24*time.Hour {
		t.Fatal("bucket seconds mismatch")
	}
}

func TestFillSeriesGaps(t *testing.T) {
	start := startOfDay(rangeNow.AddDate(0, 0, -9), rangeLocation)
	first := start.AddDate(0, 0, 2)
	second := start.AddDate(0, 0, 8)
	points := []UsageSeriesPoint{
		{BucketStart: first, Series: "v4-pro", Requests: 14, HitTokens: 1, MissTokens: 1, OutTokens: 1, Cost: 1},
		{BucketStart: second, Series: "v4-pro", Requests: 25, HitTokens: 1, MissTokens: 1, OutTokens: 1, Cost: 1},
	}
	filled := FillSeriesGaps(points, []string{"v4-pro"}, start, rangeNow, BucketDay, rangeLocation)
	if len(filled) != 10 {
		t.Fatalf("filled count = %d", len(filled))
	}
	got := make([]int, len(filled))
	for i, point := range filled {
		got[i] = point.Requests
	}
	want := []int{0, 0, 14, 0, 0, 0, 0, 0, 25, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filled requests = %v, want %v", got, want)
		}
	}

	axisStart := startOfDay(rangeNow, rangeLocation)
	axisEnd := axisStart.AddDate(0, 0, 1)
	hourly := FillSeriesGaps([]UsageSeriesPoint{{BucketStart: axisStart, Series: "s", Requests: 3}},
		[]string{"s"}, axisStart, axisEnd, BucketHour, rangeLocation)
	if len(hourly) != 24 || hourly[0].Requests != 3 || hourly[23].BucketStart != axisStart.Add(23*time.Hour) {
		t.Fatalf("hourly fill = %+v", hourly)
	}
	if FillSeriesGaps(nil, []string{"a"}, axisStart, rangeNow, BucketDay, rangeLocation) != nil {
		t.Fatal("empty input should stay empty")
	}
}

func TestSeriesCostShares(t *testing.T) {
	points := []UsageSeriesPoint{
		{BucketStart: rangeNow, Series: "a", Cost: 1.5},
		{BucketStart: rangeNow, Series: "b", Cost: 0.5},
		{BucketStart: rangeNow, Series: "a", Cost: 0.5},
		{BucketStart: rangeNow, Series: "c", Cost: 0},
	}
	shares := SeriesCostShares(points)
	if len(shares) != 2 || shares[0].Series != "a" || shares[0].Cost != 2 || shares[1].Series != "b" {
		t.Fatalf("shares = %+v", shares)
	}
}

func TestDateRangeText(t *testing.T) {
	today := startOfDay(rangeNow, rangeLocation)
	tests := []struct {
		name  string
		value TimeRange
		want  string
	}{
		{"single day", TimeRange{Start: today, End: rangeNow}, "2026/9/16"},
		{"week", TimeRange{Start: today.AddDate(0, 0, -6), End: rangeNow}, "2026/9/10 – 2026/9/16"},
		{"cross month", TimeRange{
			Start: time.Date(2026, 8, 30, 0, 0, 0, 0, rangeLocation),
			End:   time.Date(2026, 9, 2, 0, 0, 0, 0, rangeLocation),
		}, "2026/8/30 – 2026/9/1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DateRangeText(tt.value); got != tt.want {
				t.Fatalf("DateRangeText() = %q, want %q", got, tt.want)
			}
		})
	}
}
