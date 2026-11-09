package trace

import (
	"reflect"
	"testing"
	"time"
)

func TestActivityRecordAndGaps(t *testing.T) {
	at := time.Unix(1_699_999_980, 0) // 30 秒边界
	tests := []struct {
		name    string
		records []time.Duration
		want    []int
	}{
		{name: "same bucket", records: []time.Duration{0, 10 * time.Second}, want: []int{0, 0, 0, 2}},
		{name: "skip buckets", records: []time.Duration{0, 60 * time.Second}, want: []int{0, 1, 0, 1}},
		{name: "gap resets", records: []time.Duration{0, 150 * time.Second}, want: []int{0, 0, 0, 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			activity := NewActivityWith(30*time.Second, 4, at)
			for _, offset := range test.records {
				activity.Record(at.Add(offset))
			}
			if got := activity.Counts(); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("counts = %v, want %v", got, test.want)
			}
		})
	}
}

func TestActivityReadPathFillsZerosWithoutMutation(t *testing.T) {
	at := time.Unix(1_699_999_980, 0)
	activity := NewActivityWith(30*time.Second, 4, at)
	activity.Record(at)
	if got := activity.Bars(at.Add(60 * time.Second)); !reflect.DeepEqual(got, []int{0, 1, 0, 0}) {
		t.Fatalf("bars = %v", got)
	}
	if got := activity.Counts(); !reflect.DeepEqual(got, []int{0, 0, 0, 1}) {
		t.Fatalf("bars mutated trace: %v", got)
	}
	if !activity.IsEmpty(at.Add(600*time.Second)) || activity.IsEmpty(at.Add(10*time.Second)) {
		t.Fatal("empty window detection failed")
	}
}

func TestActivityIgnoresClockRollback(t *testing.T) {
	at := time.Unix(1_699_999_980, 0)
	activity := NewActivityWith(30*time.Second, 4, at)
	activity.Record(at)
	anchor := activity.Anchor()
	activity.Record(at.Add(-30 * time.Second))
	if activity.Anchor() != anchor {
		t.Fatalf("anchor moved backwards: %d -> %d", anchor, activity.Anchor())
	}
	if got := activity.Counts(); !reflect.DeepEqual(got, []int{0, 0, 0, 2}) {
		t.Fatalf("rollback request was dropped: %v", got)
	}
}

func TestActivityAlignmentAndDefaults(t *testing.T) {
	at := time.Unix(1_699_999_980, 0)
	early := NewActivityWith(30*time.Second, 4, at)
	late := NewActivityWith(30*time.Second, 4, at.Add(60*time.Second))
	early.Record(at.Add(60 * time.Second))
	late.Record(at.Add(60 * time.Second))
	if early.Anchor() != late.Anchor() || !reflect.DeepEqual(early.Counts(), late.Counts()) {
		t.Fatalf("absolute alignment differs: early=%d/%v late=%d/%v",
			early.Anchor(), early.Counts(), late.Anchor(), late.Counts())
	}
	defaults := NewActivity(at)
	if defaults.BucketCount() != 3600 || defaults.BucketSeconds() != 2*time.Second ||
		len(defaults.Counts()) != 3600 {
		t.Fatalf("defaults = %d buckets x %s", defaults.BucketCount(), defaults.BucketSeconds())
	}
}

func TestBucketPhase(t *testing.T) {
	at := time.Unix(0, 0)
	for k := int64(0); k < 5; k++ {
		boundary := time.Unix(k*15, 0)
		if got := BucketPhase(boundary, 15*time.Second); got != 0 {
			t.Fatalf("boundary phase = %v", got)
		}
		if got := BucketPhase(boundary.Add(7500*time.Millisecond), 15*time.Second); got != 0.5 {
			t.Fatalf("half phase = %v", got)
		}
	}
	if got := BucketPhase(at, 0); got != 0 {
		t.Fatalf("zero bucket phase = %v", got)
	}
	got := BucketPhase(time.Unix(-1, 0), 15*time.Second)
	if got < 0 || got >= 1 || abs(got-14.0/15.0) > 1e-9 {
		t.Fatalf("negative epoch phase = %v", got)
	}
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
