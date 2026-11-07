package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) RecentRecords(ctx context.Context, limit int) ([]RecentRequestRow, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT uuid, timestamp, model, latency_ms, status_code, user_agent
		FROM usage_log ORDER BY timestamp DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent records: %w", err)
	}
	defer rows.Close()

	var result []RecentRequestRow
	for rows.Next() {
		var uuid, model, ua string
		var unix float64
		var latency float64
		var status int
		if err := rows.Scan(&uuid, &unix, &model, &latency, &status, &ua); err != nil {
			return nil, fmt.Errorf("scan recent record: %w", err)
		}
		id := uuid
		if id == "" {
			id = strconv.FormatFloat(unix, 'f', -1, 64)
		}
		result = append(result, RecentRequestRow{
			ID:         id,
			Timestamp:  unixTime(unix),
			Model:      model,
			LatencyMS:  latency,
			StatusCode: status,
			UserAgent:  ua,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent records: %w", err)
	}
	return result, nil
}

func (s *Store) Requests(ctx context.Context, limit, offset int) ([]RequestRow, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT uuid, timestamp, model,
		       CASE WHEN client = '' OR client IS NULL THEN ? ELSE client END,
		       endpoint, prompt_tokens, completion_tokens, total_tokens, cached_tokens,
		       reasoning_tokens, latency_ms, status_code, cost, user_agent
		FROM usage_log ORDER BY timestamp DESC, id DESC LIMIT ? OFFSET ?`,
		ClientUnknown, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query request page: %w", err)
	}
	defer rows.Close()

	var result []RequestRow
	for rows.Next() {
		var item RequestRow
		var timestamp float64
		if err := rows.Scan(&item.ID, &timestamp, &item.Model, &item.Client, &item.Endpoint,
			&item.PromptTokens, &item.CompletionTokens, &item.TotalTokens, &item.CachedTokens,
			&item.ReasoningTokens, &item.LatencyMS, &item.StatusCode, &item.Cost, &item.UserAgent); err != nil {
			return nil, fmt.Errorf("scan request page: %w", err)
		}
		item.Timestamp = unixTime(timestamp)
		if item.ID == "" {
			item.ID = fmt.Sprintf("%.0f", timestamp)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request page: %w", err)
	}
	return result, nil
}

func (s *Store) RequestCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_log").Scan(&count); err != nil {
		return 0, fmt.Errorf("count requests: %w", err)
	}
	return count, nil
}

func (s *Store) QueryWindow(ctx context.Context, days int, providerID string, daysAgo int) (AggregatedUsage, error) {
	return queryWindow(ctx, s.db, s.currentTime(), s.loc, days, providerID, daysAgo)
}

func queryWindow(ctx context.Context, q queryer, now time.Time, loc *time.Location,
	days int, providerID string, daysAgo int) (AggregatedUsage, error) {
	if days <= 0 || daysAgo < 0 {
		return AggregatedUsage{}, ErrInvalidRange
	}
	today := startOfDay(now, loc)
	start := today.AddDate(0, 0, -((days - 1) + days*daysAgo))
	var end *time.Time
	if daysAgo > 0 {
		value := now.AddDate(0, 0, -(days * daysAgo))
		end = &value
	}
	args := []any{unixSeconds(start)}
	where := "timestamp >= ?"
	if end != nil {
		where += " AND timestamp < ?"
		args = append(args, unixSeconds(*end))
	}
	if providerID != "" {
		where += " AND provider_id = ?"
		args = append(args, providerID)
	}
	sqlText := aggregateSelect + " FROM usage_log WHERE " + where
	row := q.QueryRowContext(ctx, sqlText, args...)
	return scanAggregate(row)
}

const aggregateSelect = `
	SELECT COUNT(*),
	       COALESCE(SUM(prompt_tokens), 0), COALESCE(SUM(completion_tokens), 0),
	       COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cached_tokens), 0),
	       COALESCE(SUM(reasoning_tokens), 0), COALESCE(AVG(latency_ms), 0),
	       COALESCE(SUM(cost), 0),
	       COALESCE(SUM(CASE WHEN cost = 0 AND (prompt_tokens + completion_tokens) > 0 THEN 1 ELSE 0 END), 0)`

func scanAggregate(row *sql.Row) (AggregatedUsage, error) {
	var usage AggregatedUsage
	err := row.Scan(&usage.RequestCount, &usage.PromptTokens, &usage.CompletionTokens,
		&usage.TotalTokens, &usage.CachedTokens, &usage.ReasoningTokens,
		&usage.AvgLatencyMS, &usage.EstimatedCost, &usage.UnpricedRequests)
	if err != nil {
		return AggregatedUsage{}, fmt.Errorf("scan aggregate: %w", err)
	}
	return usage, nil
}

func (s *Store) QueryHourlyBreakdown(ctx context.Context, providerID string) ([]TokenBar, error) {
	now := s.currentTime()
	start := startOfDay(now, s.loc)
	modifier := sqliteZoneModifier(s.loc, now)
	args := []any{modifier, unixSeconds(start)}
	where := "timestamp >= ?"
	if providerID != "" {
		where += " AND provider_id = ?"
		args = append(args, providerID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT CAST(strftime('%H', timestamp, 'unixepoch', ?) AS INTEGER) AS hour,
		       SUM(MAX(0, prompt_tokens - cached_tokens)), SUM(cached_tokens),
		       SUM(completion_tokens), COUNT(*), COALESCE(SUM(cost), 0),
		       SUM(CASE WHEN cost = 0 AND (prompt_tokens + completion_tokens) > 0 THEN 1 ELSE 0 END)
		FROM usage_log WHERE `+where+` GROUP BY hour ORDER BY hour`, args...)
	if err != nil {
		return nil, fmt.Errorf("query hourly breakdown: %w", err)
	}
	defer rows.Close()

	buckets := map[int]TokenBar{}
	for rows.Next() {
		var hour int
		var bucket TokenBar
		if err := rows.Scan(&hour, &bucket.MissTokens, &bucket.HitTokens, &bucket.OutTokens,
			&bucket.RequestCount, &bucket.Cost, &bucket.UnpricedRequests); err != nil {
			return nil, fmt.Errorf("scan hourly breakdown: %w", err)
		}
		buckets[hour] = bucket
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hourly breakdown: %w", err)
	}
	result := make([]TokenBar, 24)
	for hour := 0; hour < 24; hour++ {
		bucket := buckets[hour]
		bucket.Label = fmt.Sprintf("%02d:00", hour)
		result[hour] = bucket
	}
	return result, nil
}

func (s *Store) QueryDailyBreakdown(ctx context.Context, days int, providerID string) ([]TokenBar, error) {
	if days <= 0 {
		return nil, ErrInvalidRange
	}
	now := s.currentTime()
	today := startOfDay(now, s.loc)
	start := today.AddDate(0, 0, -(days - 1))
	modifier := sqliteZoneModifier(s.loc, now)
	args := []any{modifier, unixSeconds(start)}
	where := "timestamp >= ?"
	if providerID != "" {
		where += " AND provider_id = ?"
		args = append(args, providerID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT date(timestamp, 'unixepoch', ?) AS day,
		       SUM(MAX(0, prompt_tokens - cached_tokens)), SUM(cached_tokens),
		       SUM(completion_tokens), COUNT(*), COALESCE(SUM(cost), 0),
		       SUM(CASE WHEN cost = 0 AND (prompt_tokens + completion_tokens) > 0 THEN 1 ELSE 0 END)
		FROM usage_log WHERE `+where+` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("query daily breakdown: %w", err)
	}
	defer rows.Close()

	buckets := map[string]TokenBar{}
	for rows.Next() {
		var day string
		var bucket TokenBar
		if err := rows.Scan(&day, &bucket.MissTokens, &bucket.HitTokens, &bucket.OutTokens,
			&bucket.RequestCount, &bucket.Cost, &bucket.UnpricedRequests); err != nil {
			return nil, fmt.Errorf("scan daily breakdown: %w", err)
		}
		buckets[day] = bucket
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily breakdown: %w", err)
	}
	result := make([]TokenBar, 0, days)
	for offset := days - 1; offset >= 0; offset-- {
		day := today.AddDate(0, 0, -offset)
		key := day.Format("2006-01-02")
		bucket := buckets[key]
		bucket.Label = day.Format("01-02")
		result = append(result, bucket)
	}
	return result, nil
}

func (s *Store) QueryClientBreakdown(ctx context.Context, days int, providerID string) ([]ClientUsage, error) {
	if days <= 0 {
		return nil, ErrInvalidRange
	}
	now := s.currentTime()
	start := startOfDay(now, s.loc).AddDate(0, 0, -(days - 1))
	args := []any{ClientUnknown, unixSeconds(start)}
	where := "timestamp >= ?"
	if providerID != "" {
		where += " AND provider_id = ?"
		args = append(args, providerID)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT CASE WHEN client = '' THEN ? ELSE client END AS name,
		       COUNT(*), SUM(prompt_tokens), SUM(cached_tokens), SUM(completion_tokens),
		       SUM(total_tokens), COALESCE(SUM(cost), 0),
		       SUM(CASE WHEN cost = 0 AND (prompt_tokens + completion_tokens) > 0 THEN 1 ELSE 0 END)
		FROM usage_log WHERE `+where+`
		GROUP BY name ORDER BY COALESCE(SUM(cost), 0) DESC, name ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query client breakdown: %w", err)
	}
	defer rows.Close()

	var result []ClientUsage
	for rows.Next() {
		var item ClientUsage
		if err := rows.Scan(&item.Client, &item.RequestCount, &item.PromptTokens, &item.CachedTokens,
			&item.CompletionTokens, &item.TotalTokens, &item.Cost, &item.UnpricedRequests); err != nil {
			return nil, fmt.Errorf("scan client breakdown: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client breakdown: %w", err)
	}
	return result, nil
}

func (s *Store) MenuBarSnapshot(ctx context.Context) (MenuBarSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MenuBarSnapshot{}, fmt.Errorf("begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := s.currentTime()
	today := startOfDay(now, s.loc)
	tomorrow := today.AddDate(0, 0, 1)
	latest, err := latestCacheHitRate(ctx, tx)
	if err != nil {
		return MenuBarSnapshot{}, err
	}
	todayRate, err := cacheHitRateBetween(ctx, tx, today, tomorrow)
	if err != nil {
		return MenuBarSnapshot{}, err
	}
	cost, err := costBetween(ctx, tx, today, tomorrow)
	if err != nil {
		return MenuBarSnapshot{}, err
	}
	tokens, err := scalarInt(ctx, tx,
		"SELECT COALESCE(SUM(total_tokens), 0) FROM usage_log WHERE timestamp >= ? AND timestamp < ?",
		unixSeconds(today), unixSeconds(tomorrow))
	if err != nil {
		return MenuBarSnapshot{}, err
	}
	requests, err := scalarInt(ctx, tx,
		"SELECT COUNT(*) FROM usage_log WHERE timestamp >= ? AND timestamp < ?",
		unixSeconds(today), unixSeconds(tomorrow))
	if err != nil {
		return MenuBarSnapshot{}, err
	}
	previous, err := queryWindow(ctx, tx, now, s.loc, 1, "", 1)
	if err != nil {
		return MenuBarSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return MenuBarSnapshot{}, fmt.Errorf("commit snapshot: %w", err)
	}

	snapshot := MenuBarSnapshot{
		LatestCacheHitRate: latest,
		TodayCacheHitRate:  todayRate,
		TodayCost:          cost,
		TodayTokens:        tokens,
		TodayRequests:      requests,
	}
	previousUsage := PreviousWindowUsage{
		Tokens:   previous.TotalTokens,
		Requests: previous.RequestCount,
		Cost:     previous.EstimatedCost,
	}
	if previous.PromptTokens > 0 {
		hitRate := float64(previous.CachedTokens) / float64(previous.PromptTokens)
		previousUsage.HitRate = &hitRate
	}
	snapshot.PreviousWindow = &previousUsage
	return snapshot, nil
}

func latestCacheHitRate(ctx context.Context, q queryer) (*float64, error) {
	var prompt, cached int
	err := q.QueryRowContext(ctx,
		"SELECT prompt_tokens, cached_tokens FROM usage_log ORDER BY timestamp DESC LIMIT 1").Scan(&prompt, &cached)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query latest hit rate: %w", err)
	}
	if prompt <= 0 {
		return nil, nil
	}
	value := float64(cached) / float64(prompt)
	return &value, nil
}

func cacheHitRateBetween(ctx context.Context, q queryer, from, to time.Time) (*float64, error) {
	var miss, hit int
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(MAX(0, prompt_tokens - cached_tokens)), 0), COALESCE(SUM(cached_tokens), 0)
		FROM usage_log WHERE timestamp >= ? AND timestamp < ?`, unixSeconds(from), unixSeconds(to)).Scan(&miss, &hit)
	if err != nil {
		return nil, fmt.Errorf("query hit rate: %w", err)
	}
	if miss+hit <= 0 {
		return nil, nil
	}
	value := float64(hit) / float64(miss+hit)
	return &value, nil
}

func costBetween(ctx context.Context, q queryer, from, to time.Time) (CostSummary, error) {
	var summary CostSummary
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(cost), 0),
		       COALESCE(SUM(CASE WHEN cost = 0 AND (prompt_tokens + completion_tokens) > 0 THEN 1 ELSE 0 END), 0)
		FROM usage_log WHERE timestamp >= ? AND timestamp < ?`, unixSeconds(from), unixSeconds(to)).Scan(
		&summary.Cost, &summary.UnpricedRequests)
	if err != nil {
		return CostSummary{}, fmt.Errorf("query cost: %w", err)
	}
	return summary, nil
}

func scalarInt(ctx context.Context, q queryer, sqlText string, args ...any) (int, error) {
	var value int
	if err := q.QueryRowContext(ctx, sqlText, args...).Scan(&value); err != nil {
		return 0, fmt.Errorf("query scalar: %w", err)
	}
	return value, nil
}

func (s *Store) WindowTotals(ctx context.Context) (WindowTokenTotals, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WindowTokenTotals{}, fmt.Errorf("begin window totals: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.currentTime()
	today, err := queryWindow(ctx, tx, now, s.loc, 1, "", 0)
	if err != nil {
		return WindowTokenTotals{}, err
	}
	week, err := queryWindow(ctx, tx, now, s.loc, 7, "", 0)
	if err != nil {
		return WindowTokenTotals{}, err
	}
	month, err := queryWindow(ctx, tx, now, s.loc, 30, "", 0)
	if err != nil {
		return WindowTokenTotals{}, err
	}
	if err := tx.Commit(); err != nil {
		return WindowTokenTotals{}, fmt.Errorf("commit window totals: %w", err)
	}
	return WindowTokenTotals{Today: today.TotalTokens, Week: week.TotalTokens, Month: month.TotalTokens}, nil
}

func (s *Store) QueryTotals(ctx context.Context, from, to time.Time, client, providerID string) (AggregatedUsage, error) {
	if !from.Before(to) {
		return AggregatedUsage{}, ErrInvalidRange
	}
	args := []any{unixSeconds(from), unixSeconds(to)}
	where := "timestamp >= ? AND timestamp < ?"
	if client != "" {
		where += " AND client = ?"
		args = append(args, client)
	}
	if providerID != "" {
		where += " AND provider_id = ?"
		args = append(args, providerID)
	}
	return scanAggregate(s.db.QueryRowContext(ctx, aggregateSelect+" FROM usage_log WHERE "+where, args...))
}

func (s *Store) LifetimeCost(ctx context.Context) (float64, error) {
	var cost float64
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(cost), 0) FROM usage_log").Scan(&cost); err != nil {
		return 0, fmt.Errorf("query lifetime cost: %w", err)
	}
	return cost, nil
}

func (s *Store) AvailableClients(ctx context.Context, from, to time.Time) ([]string, error) {
	if !from.Before(to) {
		return nil, ErrInvalidRange
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT CASE WHEN client = '' THEN ? ELSE client END AS name
		FROM usage_log WHERE timestamp >= ? AND timestamp < ? ORDER BY name`,
		ClientUnknown, unixSeconds(from), unixSeconds(to))
	if err != nil {
		return nil, fmt.Errorf("query clients: %w", err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan client: %w", err)
		}
		result = append(result, name)
	}
	return result, rows.Err()
}

func (s *Store) QuerySeriesPoints(ctx context.Context, from, to time.Time, bucket UsageBucket,
	dimension UsageDimension, client, providerID string) ([]UsageSeriesPoint, error) {
	if !from.Before(to) {
		return nil, ErrInvalidRange
	}
	modifier := sqliteZoneModifier(s.loc, s.currentTime())
	var bucketExpr, layout string
	switch bucket {
	case BucketHour:
		bucketExpr = "strftime('%Y-%m-%d %H:00:00', timestamp, 'unixepoch', ?)"
		layout = "2006-01-02 15:04:05"
	case BucketDay:
		bucketExpr = "date(timestamp, 'unixepoch', ?)"
		layout = "2006-01-02"
	default:
		return nil, fmt.Errorf("unsupported bucket %q", bucket)
	}
	var column string
	switch dimension {
	case DimensionModel:
		column = "model"
	case DimensionClient:
		column = "client"
	default:
		return nil, fmt.Errorf("unsupported dimension %q", dimension)
	}

	args := []any{modifier, ClientUnknown, unixSeconds(from), unixSeconds(to)}
	where := "timestamp >= ? AND timestamp < ?"
	if client != "" {
		where += " AND client = ?"
		args = append(args, client)
	}
	if providerID != "" {
		where += " AND provider_id = ?"
		args = append(args, providerID)
	}
	sqlText := `SELECT ` + bucketExpr + ` AS bucket,
		CASE WHEN ` + column + ` = '' THEN ? ELSE ` + column + ` END AS series,
		COUNT(*), SUM(cached_tokens), SUM(MAX(0, prompt_tokens - cached_tokens)),
		SUM(completion_tokens), COALESCE(SUM(cost), 0)
		FROM usage_log WHERE ` + where + ` GROUP BY bucket, series ORDER BY bucket ASC, series ASC`
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("query series: %w", err)
	}
	defer rows.Close()
	var result []UsageSeriesPoint
	for rows.Next() {
		var key, series string
		var point UsageSeriesPoint
		if err := rows.Scan(&key, &series, &point.Requests, &point.HitTokens, &point.MissTokens,
			&point.OutTokens, &point.Cost); err != nil {
			return nil, fmt.Errorf("scan series: %w", err)
		}
		point.Series = series
		parsed, err := time.ParseInLocation(layout, key, s.loc)
		if err != nil {
			return nil, fmt.Errorf("parse series bucket %q: %w", key, err)
		}
		point.BucketStart = parsed
		result = append(result, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate series: %w", err)
	}
	return result, nil
}

func startOfDay(at time.Time, loc *time.Location) time.Time {
	local := at.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

func sqliteZoneModifier(loc *time.Location, at time.Time) string {
	_, offset := at.In(loc).Zone()
	return fmt.Sprintf("%+d minutes", offset/60)
}
