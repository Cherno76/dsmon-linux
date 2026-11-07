package store

import "time"

func unixSeconds(at time.Time) float64 {
	return float64(at.UnixNano()) / 1e9
}
