package proxy

import (
	"sync"
	"time"
)

type Limiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
	now     func() time.Time
}

func NewLimiter(now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{buckets: map[string][]time.Time{}, now: now}
}

func (l *Limiter) Check(providerID string, limit int) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-60 * time.Second)
	timestamps := l.buckets[providerID]
	filtered := timestamps[:0]
	for _, at := range timestamps {
		if !at.Before(cutoff) {
			filtered = append(filtered, at)
		}
	}
	if len(filtered) >= limit {
		l.buckets[providerID] = filtered
		return false
	}
	l.buckets[providerID] = append(filtered, now)
	return true
}
