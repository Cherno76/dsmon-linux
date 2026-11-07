package proxy

import (
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := NewLimiter(func() time.Time { return now })
	for index := 0; index < 100; index++ {
		if !limiter.Check("deepseek", 0) || !limiter.Check("deepseek", -1) {
			t.Fatal("unlimited requests were rejected")
		}
	}
	if !limiter.Check("a", 2) || !limiter.Check("a", 2) || limiter.Check("a", 2) {
		t.Fatal("limit was not enforced")
	}
	if !limiter.Check("b", 1) || limiter.Check("b", 1) {
		t.Fatal("provider buckets are not independent")
	}
	now = now.Add(61 * time.Second)
	if !limiter.Check("a", 2) {
		t.Fatal("old timestamps did not expire")
	}
}
