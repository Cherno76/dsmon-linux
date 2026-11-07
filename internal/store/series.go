package store

import (
	"sort"
	"time"
)

func (p UsageSeriesPoint) Renamed(series string) UsageSeriesPoint {
	p.Series = series
	return p
}

func (p UsageSeriesPoint) ID() string {
	return p.BucketStart.UTC().Format(time.RFC3339Nano) + "|" + p.Series
}

// SeriesNames 返回点集里出现过的系列名，按字典序排列。
func SeriesNames(points []UsageSeriesPoint) []string {
	seen := make(map[string]struct{}, len(points))
	for _, point := range points {
		seen[point.Series] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ConsolidateSeries 把同桶同系列的点相加。
func ConsolidateSeries(points []UsageSeriesPoint) []UsageSeriesPoint {
	type key struct {
		bucket time.Time
		series string
	}
	merged := make(map[key]UsageSeriesPoint, len(points))
	for _, point := range points {
		itemKey := key{bucket: point.BucketStart, series: point.Series}
		existing, ok := merged[itemKey]
		if !ok {
			merged[itemKey] = point
			continue
		}
		existing.Requests += point.Requests
		existing.HitTokens += point.HitTokens
		existing.MissTokens += point.MissTokens
		existing.OutTokens += point.OutTokens
		existing.Cost += point.Cost
		merged[itemKey] = existing
	}
	result := make([]UsageSeriesPoint, 0, len(merged))
	for _, point := range merged {
		result = append(result, point)
	}
	sortSeriesPoints(result)
	return result
}

// FillSeriesGaps 给区间内每个桶、每个系列补 0。空结果保持为空。
func FillSeriesGaps(points []UsageSeriesPoint, series []string, from, to time.Time,
	bucket UsageBucket, loc *time.Location) []UsageSeriesPoint {
	if len(points) == 0 || len(series) == 0 || !from.Before(to) {
		return points
	}
	if loc == nil {
		loc = time.Local
	}
	existing := make(map[string]UsageSeriesPoint, len(points))
	for _, point := range points {
		existing[point.ID()] = point
	}

	cursor := from.In(loc)
	if bucket == BucketHour {
		cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day(), cursor.Hour(), 0, 0, 0, loc)
	} else {
		cursor = startOfDay(cursor, loc)
	}
	filled := make([]UsageSeriesPoint, 0, len(points))
	for cursor.Before(to) {
		for _, name := range series {
			empty := UsageSeriesPoint{BucketStart: cursor, Series: name}
			if point, ok := existing[empty.ID()]; ok {
				filled = append(filled, point)
				delete(existing, empty.ID())
			} else {
				filled = append(filled, empty)
			}
		}
		if bucket == BucketHour {
			cursor = cursor.Add(time.Hour)
		} else {
			cursor = cursor.AddDate(0, 0, 1)
		}
	}
	for _, point := range existing {
		filled = append(filled, point)
	}
	sortSeriesPoints(filled)
	return filled
}

type SeriesCost struct {
	Series string
	Cost   float64
}

// SeriesCostShares 按金额倒序汇总系列；0 元系列不进来。
func SeriesCostShares(points []UsageSeriesPoint) []SeriesCost {
	totals := map[string]float64{}
	for _, point := range points {
		totals[point.Series] += point.Cost
	}
	result := make([]SeriesCost, 0, len(totals))
	for series, cost := range totals {
		if cost > 0 {
			result = append(result, SeriesCost{Series: series, Cost: cost})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Cost == result[j].Cost {
			return result[i].Series > result[j].Series
		}
		return result[i].Cost > result[j].Cost
	})
	return result
}

func sortSeriesPoints(points []UsageSeriesPoint) {
	sort.Slice(points, func(i, j int) bool {
		if points[i].BucketStart.Equal(points[j].BucketStart) {
			return points[i].Series < points[j].Series
		}
		return points[i].BucketStart.Before(points[j].BucketStart)
	})
}
