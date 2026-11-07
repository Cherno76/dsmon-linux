// Package trace 提供活动曲线和流量曲线的纯逻辑数据环。
package trace

import (
	"math"
	"time"
)

const (
	DefaultActivityBucketSeconds = 6 * time.Second
	DefaultActivityBucketCount   = 20
)

// Activity 按绝对时间分桶，记录请求发出次数；读取路径也会补零，避免空闲时曲线冻住。
type Activity struct {
	bucketSeconds time.Duration
	bucketCount   int
	counts        []int
	anchor        int64
}

func NewActivity(now time.Time) *Activity {
	return NewActivityWith(DefaultActivityBucketSeconds, DefaultActivityBucketCount, now)
}

func NewActivityWith(bucketSeconds time.Duration, bucketCount int, now time.Time) *Activity {
	if bucketCount < 1 {
		bucketCount = 1
	}
	return &Activity{
		bucketSeconds: bucketSeconds,
		bucketCount:   bucketCount,
		counts:        make([]int, bucketCount),
		anchor:        BucketIndex(now, bucketSeconds) - int64(bucketCount-1),
	}
}

func BucketIndex(at time.Time, bucketSeconds time.Duration) int64 {
	if bucketSeconds <= 0 {
		return 0
	}
	return int64(math.Floor(float64(at.UnixNano()) / float64(bucketSeconds)))
}

func BucketPhase(at time.Time, bucketSeconds time.Duration) float64 {
	if bucketSeconds <= 0 {
		return 0
	}
	value := float64(at.UnixNano()) / float64(bucketSeconds)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value - math.Floor(value)
}

func (a *Activity) BucketSeconds() time.Duration { return a.bucketSeconds }
func (a *Activity) BucketCount() int             { return a.bucketCount }
func (a *Activity) Anchor() int64                { return a.anchor }
func (a *Activity) Counts() []int                { return append([]int(nil), a.counts...) }

func (a *Activity) currentIndex() int64 { return a.anchor + int64(a.bucketCount-1) }

func (a *Activity) Advance(to time.Time) {
	target := BucketIndex(to, a.bucketSeconds)
	shift := target - a.currentIndex()
	if shift <= 0 {
		return
	}
	if shift >= int64(a.bucketCount) {
		a.counts = make([]int, a.bucketCount)
		a.anchor = target - int64(a.bucketCount-1)
		return
	}
	copy(a.counts, a.counts[shift:])
	for index := a.bucketCount - int(shift); index < a.bucketCount; index++ {
		a.counts[index] = 0
	}
	a.anchor += shift
}

func (a *Activity) Record(now time.Time) {
	a.Advance(now)
	a.counts[a.bucketCount-1]++
}

func (a *Activity) Bars(now time.Time) []int {
	copyTrace := *a
	copyTrace.counts = append([]int(nil), a.counts...)
	copyTrace.Advance(now)
	return copyTrace.counts
}

func (a *Activity) IsEmpty(now time.Time) bool {
	for _, count := range a.Bars(now) {
		if count != 0 {
			return false
		}
	}
	return true
}
