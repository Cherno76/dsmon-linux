package store

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"dsmon/internal/pricing"
)

// TestRealMacDatabaseReconciliation 用真实 Mac 快照做导入与聚合对账。
// 默认跳过，避免把 40MB 运行数据或开发机路径带进仓库。
func TestRealMacDatabaseReconciliation(t *testing.T) {
	sourcePath := os.Getenv("DSMON_REAL_DB")
	if sourcePath == "" {
		t.Skip("set DSMON_REAL_DB to a standalone Mac DS-mon usage.db snapshot")
	}
	ctx := context.Background()
	loc := time.FixedZone("CST", 8*3600)

	sourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceDB, err := sql.Open("sqlite", "file:"+sourcePath+"?mode=ro&immutable=1&_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatalf("open source read-only: %v", err)
	}
	defer sourceDB.Close()
	sourceDB.SetMaxOpenConns(1)
	if err := sourceDB.PingContext(ctx); err != nil {
		t.Fatalf("ping source: %v", err)
	}
	var maxTimestamp float64
	if err := sourceDB.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(timestamp), 0) FROM usage_log").Scan(&maxTimestamp); err != nil {
		t.Fatalf("read source time range: %v", err)
	}
	if maxTimestamp <= 0 {
		t.Fatal("source database has no usage rows")
	}
	now := unixTime(maxTimestamp).In(loc).Add(time.Second)
	before := databaseFileSnapshot(t, sourcePath)

	targetPath := filepath.Join(t.TempDir(), "usage.db")
	// 空规则会标记为 fallback：既有计费时间轴保持不变，导入维护不会重算历史费用。
	loaded := pricing.Load("", nil, nil)
	options := []Option{
		WithPricing(loaded),
		WithClock(func() time.Time { return now }),
		WithLocation(loc),
		WithRetentionDays(0),
	}
	result, err := ImportDatabase(ctx, targetPath, sourcePath, options...)
	if err != nil {
		t.Fatalf("import real database: %v", err)
	}
	if !result.Copied {
		t.Fatalf("expected a fresh copy import: %+v", result)
	}
	if after := databaseFileSnapshot(t, sourcePath); !maps.Equal(before, after) {
		t.Fatalf("source snapshot changed during import")
	}
	merged, err := ImportDatabase(ctx, targetPath, sourcePath, options...)
	if err != nil {
		t.Fatalf("re-import real database: %v", err)
	}
	if merged.Copied || merged.Inserted != 0 || merged.Skipped != result.SourceRows ||
		merged.Revisions != 0 || merged.BalanceSamples != 0 {
		t.Fatalf("real database re-import was not idempotent: %+v", merged)
	}

	target, err := Open(targetPath, options...)
	if err != nil {
		t.Fatalf("open imported database: %v", err)
	}
	defer target.Close()

	var sourceRows, sourceSamples, targetRows, targetSamples int
	var sourceCost, sourceTokens, targetCost, targetTokens float64
	if err := sourceDB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost), 0)
		FROM usage_log`).Scan(&sourceRows, &sourceTokens, &sourceCost); err != nil {
		t.Fatal(err)
	}
	if err := sourceDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM balance_sample`).Scan(&sourceSamples); err != nil {
		t.Fatal(err)
	}
	if err := target.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost), 0)
		FROM usage_log`).Scan(&targetRows, &targetTokens, &targetCost); err != nil {
		t.Fatal(err)
	}
	if err := target.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM balance_sample`).Scan(&targetSamples); err != nil {
		t.Fatal(err)
	}
	if sourceRows != targetRows || sourceSamples != targetSamples {
		t.Fatalf("import counts differ: source rows=%d samples=%d, target rows=%d samples=%d",
			sourceRows, sourceSamples, targetRows, targetSamples)
	}
	if sourceTokens != targetTokens || math.Abs(sourceCost-targetCost) > 1e-6 {
		t.Fatalf("import totals differ: source tokens=%v cost=%.9f, target tokens=%v cost=%.9f",
			sourceTokens, sourceCost, targetTokens, targetCost)
	}
	t.Logf("import: rows=%d samples=%d tokens=%.0f cost=%.6f", targetRows, targetSamples, targetTokens, targetCost)

	ranges := []struct {
		name string
		days int
	}{
		{name: "today", days: 1},
		{name: "last7Days", days: 7},
		{name: "last30Days", days: 30},
	}
	dimensions := []struct {
		name      string
		dimension UsageDimension
		column    string
	}{
		{name: "model", dimension: DimensionModel, column: "model"},
		{name: "client", dimension: DimensionClient, column: "client"},
	}
	for _, currentRange := range ranges {
		start := startOfDay(now, loc).AddDate(0, 0, -(currentRange.days - 1))
		var wantRows, wantTokens int
		var wantCost float64
		if err := sourceDB.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost), 0)
			FROM usage_log WHERE timestamp >= ?`, unixSeconds(start)).Scan(
			&wantRows, &wantTokens, &wantCost); err != nil {
			t.Fatal(err)
		}
		actualWindow, err := target.QueryWindow(ctx, currentRange.days, "", 0)
		if err != nil {
			t.Fatalf("%s QueryWindow: %v", currentRange.name, err)
		}
		if actualWindow.RequestCount != wantRows || actualWindow.TotalTokens != wantTokens ||
			math.Abs(actualWindow.EstimatedCost-wantCost) > 1e-6 {
			t.Fatalf("%s total mismatch: source rows=%d tokens=%d cost=%.9f, got rows=%d tokens=%d cost=%.9f",
				currentRange.name, wantRows, wantTokens, wantCost,
				actualWindow.RequestCount, actualWindow.TotalTokens, actualWindow.EstimatedCost)
		}
		t.Logf("%s total: rows=%d tokens=%d cost=%.6f", currentRange.name, wantRows, wantTokens, wantCost)

		for _, dimension := range dimensions {
			want, err := sourceBreakdown(ctx, sourceDB, start, now, dimension.column)
			if err != nil {
				t.Fatalf("%s/%s source breakdown: %v", currentRange.name, dimension.name, err)
			}
			points, err := target.QuerySeriesPoints(ctx, start, now, BucketDay, dimension.dimension, "", "")
			if err != nil {
				t.Fatalf("%s/%s QuerySeriesPoints: %v", currentRange.name, dimension.name, err)
			}
			got := aggregateSeries(points)
			if err := compareReconciliation(got, want); err != nil {
				t.Fatalf("%s/%s: %v", currentRange.name, dimension.name, err)
			}
			t.Logf("%s/%s: %d series", currentRange.name, dimension.name, len(got))
		}
	}
}

type reconciliationMetric struct {
	Requests   int
	HitTokens  int
	MissTokens int
	OutTokens  int
	Cost       float64
}

func aggregateSeries(points []UsageSeriesPoint) map[string]reconciliationMetric {
	result := map[string]reconciliationMetric{}
	for _, point := range points {
		item := result[point.Series]
		item.Requests += point.Requests
		item.HitTokens += point.HitTokens
		item.MissTokens += point.MissTokens
		item.OutTokens += point.OutTokens
		item.Cost += point.Cost
		result[point.Series] = item
	}
	return result
}

func sourceBreakdown(ctx context.Context, db *sql.DB, from, to time.Time,
	column string) (map[string]reconciliationMetric, error) {
	if column != "model" && column != "client" {
		return nil, fmt.Errorf("unsupported breakdown column %q", column)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT CASE WHEN `+column+` = '' THEN ? ELSE `+column+` END AS series,
		       COUNT(*), COALESCE(SUM(cached_tokens), 0),
		       COALESCE(SUM(MAX(0, prompt_tokens - cached_tokens)), 0),
		       COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(cost), 0)
		FROM usage_log WHERE timestamp >= ? AND timestamp < ?
		GROUP BY series ORDER BY series`, ClientUnknown, unixSeconds(from), unixSeconds(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]reconciliationMetric{}
	for rows.Next() {
		var series string
		var item reconciliationMetric
		if err := rows.Scan(&series, &item.Requests, &item.HitTokens, &item.MissTokens,
			&item.OutTokens, &item.Cost); err != nil {
			return nil, err
		}
		result[series] = item
	}
	return result, rows.Err()
}

func compareReconciliation(got, want map[string]reconciliationMetric) error {
	if len(got) != len(want) {
		return fmt.Errorf("series count got=%d want=%d", len(got), len(want))
	}
	keys := make([]string, 0, len(want))
	for key := range want {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		gotItem, ok := got[key]
		if !ok {
			return fmt.Errorf("missing series %q", key)
		}
		wantItem := want[key]
		if gotItem.Requests != wantItem.Requests || gotItem.HitTokens != wantItem.HitTokens ||
			gotItem.MissTokens != wantItem.MissTokens || gotItem.OutTokens != wantItem.OutTokens ||
			math.Abs(gotItem.Cost-wantItem.Cost) > 1e-6 {
			return fmt.Errorf("series %q got=%+v want=%+v", key, gotItem, wantItem)
		}
	}
	return nil
}
