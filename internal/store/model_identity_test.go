package store

import (
	"testing"
	"time"
)

func beijingDate(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, rangeLocation)
}

func TestModelAliasCutoff(t *testing.T) {
	tests := []struct {
		name  string
		model string
		at    time.Time
		want  string
	}{
		{"flash before cutoff", "deepseek-v4-flash", beijingDate(2026, 9, 9, 23), "deepseek-v4-flash"},
		{"flash at cutoff", "deepseek-v4-flash", beijingDate(2026, 9, 10, 0), "deepseek-flash"},
		{"vision before cutoff", "deepseek-v4-flash-vision-exp", beijingDate(2026, 9, 9, 12), "deepseek-v4-flash-vision-exp"},
		{"vision after cutoff", "deepseek-v4-flash-vision-exp", beijingDate(2026, 9, 11, 12), "deepseek-flash"},
		{"canonical untouched", "deepseek-flash", beijingDate(2026, 9, 16, 12), "deepseek-flash"},
		{"pro untouched", "deepseek-v4-pro", beijingDate(2026, 9, 16, 12), "deepseek-v4-pro"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ModelDisplayName(tt.model, tt.at); got != tt.want {
				t.Fatalf("ModelDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
	if !IsDeprecatedModelAlias("deepseek-v4-flash", beijingDate(2026, 9, 16, 12)) {
		t.Fatal("flash should be deprecated alias after cutoff")
	}
	if IsDeprecatedModelAlias("deepseek-v4-pro", beijingDate(2026, 9, 16, 12)) {
		t.Fatal("pro should not be deprecated")
	}
	if IsDeprecatedModelAlias("deepseek-v4-flash", beijingDate(2026, 9, 9, 12)) {
		t.Fatal("flash should not be alias before cutoff")
	}
}

func TestConsolidateSeries(t *testing.T) {
	bucket := beijingDate(2026, 9, 16, 10)
	points := []UsageSeriesPoint{
		{BucketStart: bucket, Series: "deepseek-flash", Requests: 3, MissTokens: 300, Cost: 0.3},
		{BucketStart: bucket, Series: "deepseek-flash", Requests: 1, MissTokens: 100, Cost: 0.1},
		{BucketStart: bucket, Series: "deepseek-v4-pro", Requests: 2, MissTokens: 200, Cost: 2},
	}
	merged := ConsolidateSeries(points)
	if len(merged) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	var flash *UsageSeriesPoint
	for i := range merged {
		if merged[i].Series == "deepseek-flash" {
			flash = &merged[i]
		}
	}
	if flash == nil || flash.Requests != 4 || flash.TokenCount() != 400 || flash.Cost != 0.4 {
		t.Fatalf("flash = %+v", flash)
	}
}

func TestRenameThenConsolidate(t *testing.T) {
	bucket := beijingDate(2026, 9, 16, 10)
	raw := []UsageSeriesPoint{
		{BucketStart: bucket, Series: "deepseek-flash", Requests: 5, MissTokens: 500, Cost: 0.5},
		{BucketStart: bucket, Series: "deepseek-v4-flash", Requests: 1, MissTokens: 100, Cost: 0.1},
	}
	shaped := make([]UsageSeriesPoint, 0, len(raw))
	for _, point := range raw {
		shaped = append(shaped, point.Renamed(ModelDisplayName(point.Series, point.BucketStart)))
	}
	merged := ConsolidateSeries(shaped)
	if len(merged) != 1 || merged[0].Series != "deepseek-flash" || merged[0].Requests != 6 || merged[0].Cost != 0.6 {
		t.Fatalf("merged = %+v", merged)
	}
}
