package tui

import (
	"math/bits"
	"testing"
	"time"

	"dsmon/internal/proxy/trace"
)

func TestActivityHistoryScrollsLeftWithBucketPhase(t *testing.T) {
	const width = 40
	counts := make([]int, width*2)
	counts[40] = 3

	atBoundary := time.Unix(0, 0)
	threeQuarters := time.Unix(0, int64(3*trace.DefaultActivityBucketSeconds/4))
	start := activityPeakColumn(RenderActivity(counts, width, 6, atBoundary, 3))
	later := activityPeakColumn(RenderActivity(counts, width, 6, threeQuarters, 3))

	if later >= start {
		t.Fatalf("activity peak moved right with phase: start=%d later=%d", start, later)
	}
}

func activityPeakColumn(rows []string) int {
	columns := len([]rune(rows[0])) * 2
	peak, peakX := -1, -1
	for x := 0; x < columns; x++ {
		filled := 0
		for _, row := range rows {
			value := uint8([]rune(row)[x/2] - 0x2800)
			if x%2 == 0 {
				filled += bits.OnesCount8(value & 0x47)
			} else {
				filled += bits.OnesCount8(value & 0xb8)
			}
		}
		if filled > peak {
			peak, peakX = filled, x
		}
	}
	return peakX
}
