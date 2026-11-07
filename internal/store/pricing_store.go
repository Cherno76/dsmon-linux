package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"dsmon/internal/pricing"
)

func (s *Store) reloadTimeline(ctx context.Context) error {
	revisions, err := loadPricingRevisions(ctx, s.db)
	if err != nil {
		return err
	}
	s.setTimeline(pricing.NewTimeline(revisions))
	return nil
}

func loadPricingRevisions(ctx context.Context, q queryer) ([]pricing.Revision, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, valid_from, source, fingerprint, text
		FROM pricing_revision ORDER BY valid_from ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query pricing revisions: %w", err)
	}
	defer rows.Close()

	var revisions []pricing.Revision
	for rows.Next() {
		var id int64
		var validFrom float64
		var source, fingerprint, text string
		if err := rows.Scan(&id, &validFrom, &source, &fingerprint, &text); err != nil {
			return nil, fmt.Errorf("scan pricing revision: %w", err)
		}
		revisions = append(revisions, pricing.NewRevision(id, unixTime(validFrom),
			pricing.Source(source), fingerprint, text))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pricing revisions: %w", err)
	}
	return revisions, nil
}

func (s *Store) PricingRevisions(ctx context.Context) ([]pricing.Revision, error) {
	return loadPricingRevisions(ctx, s.db)
}

func (s *Store) PricingSummary(ctx context.Context) (count int, latestValidFrom *time.Time, latestSource pricing.Source, err error) {
	revisions, err := s.PricingRevisions(ctx)
	if err != nil {
		return 0, nil, pricing.SourceNone, err
	}
	count = len(revisions)
	if len(revisions) > 0 {
		latest := revisions[len(revisions)-1]
		value := latest.ValidFrom
		latestValidFrom = &value
		latestSource = latest.Source
	}
	return count, latestValidFrom, latestSource, nil
}

// ImportPricingRevision 按 validFrom 落一条新修订，只重算它生效之后的行。
// 指纹与最新一条相同时是 no-op。
func (s *Store) ImportPricingRevision(ctx context.Context, ruleText string, source pricing.Source,
	validFrom time.Time, fingerprint string) (repriced int, imported bool, err error) {
	if fingerprint == "" {
		fingerprint = pricing.Fingerprint(ruleText)
	}
	if latest, ok := s.currentTimeline().Latest(); ok && latest.Fingerprint == fingerprint {
		return 0, false, nil
	}

	var nextTimeline pricing.Timeline
	err = s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		revisions, err := loadPricingRevisions(ctx, tx)
		if err != nil {
			return err
		}
		existing := pricing.NewTimeline(revisions)
		if latest, ok := existing.Latest(); ok && latest.Fingerprint == fingerprint {
			nextTimeline = existing
			return nil
		}
		if _, err := insertPricingRevision(ctx, tx, ruleText, source, fingerprint, validFrom, s.currentTime()); err != nil {
			return err
		}
		revisions, err = loadPricingRevisions(ctx, tx)
		if err != nil {
			return err
		}
		nextTimeline = pricing.NewTimeline(revisions)
		repriced, err = repriceRows(ctx, tx, nextTimeline, validFrom)
		if err != nil {
			return err
		}
		imported = true
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	if imported {
		s.setTimeline(nextTimeline)
	}
	return repriced, imported, nil
}

func (s *Store) bootstrapPricing(ctx context.Context, loaded pricing.Loaded) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pricing bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	revisions, err := loadPricingRevisions(ctx, tx)
	if err != nil {
		return err
	}
	if len(revisions) == 0 {
		hasUsage, err := usageRowCount(ctx, tx)
		if err != nil {
			return err
		}
		validFrom := s.currentTime()
		if hasUsage {
			validFrom = time.Unix(0, 0)
		}
		id, err := insertPricingRevision(ctx, tx, loaded.Text, loaded.Source, loaded.Fingerprint,
			validFrom, s.currentTime())
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE usage_log SET pricing_revision = ? WHERE pricing_revision = 0", id); err != nil {
			return fmt.Errorf("backfill pricing revision: %w", err)
		}
		return tx.Commit()
	}

	latest := revisions[len(revisions)-1]
	if latest.Fingerprint == loaded.Fingerprint || loaded.IsFallback {
		return tx.Commit()
	}
	if _, err := insertPricingRevision(ctx, tx, loaded.Text, loaded.Source,
		loaded.Fingerprint, s.currentTime(), s.currentTime()); err != nil {
		return err
	}
	revisions, err = loadPricingRevisions(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := repriceRows(ctx, tx, pricing.NewTimeline(revisions), s.currentTime()); err != nil {
		return err
	}
	return tx.Commit()
}

func insertPricingRevision(ctx context.Context, tx *sql.Tx, ruleText string, source pricing.Source,
	fingerprint string, validFrom, importedAt time.Time) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO pricing_revision (valid_from, fingerprint, source, text, imported_at)
		VALUES (?, ?, ?, ?, ?)`, unixSeconds(validFrom), fingerprint, string(source), ruleText, unixSeconds(importedAt))
	if err != nil {
		return 0, fmt.Errorf("insert pricing revision: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read pricing revision id: %w", err)
	}
	return id, nil
}

func usageRowCount(ctx context.Context, q queryer) (bool, error) {
	count, err := scalarInt(ctx, q, "SELECT COUNT(*) FROM usage_log")
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// repriceRows 按每行自己的时间戳选择修订。固定费用单独保留，只改 token 部分。
func repriceRows(ctx context.Context, tx *sql.Tx, timeline pricing.Timeline, from time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, timestamp, model, prompt_tokens, completion_tokens, cached_tokens, fixed_cost
		FROM usage_log
		WHERE timestamp >= ? AND endpoint NOT LIKE '/v1/tools/%'`, unixSeconds(from))
	if err != nil {
		return 0, fmt.Errorf("query rows to reprice: %w", err)
	}
	type pending struct {
		id       int64
		cost     float64
		revision int64
	}
	var updates []pending
	for rows.Next() {
		var id int64
		var timestamp, fixedCost float64
		var model string
		var prompt, completion, cached int
		if err := rows.Scan(&id, &timestamp, &model, &prompt, &completion, &cached, &fixedCost); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan row to reprice: %w", err)
		}
		at := unixTime(timestamp)
		cost := timeline.Cost(model, prompt, completion, cached, at) + fixedCost
		updates = append(updates, pending{id: id, cost: cost, revision: timeline.RevisionID(at)})
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close rows to reprice: %w", err)
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx,
			"UPDATE usage_log SET cost = ?, pricing_revision = ? WHERE id = ?",
			update.cost, update.revision, update.id); err != nil {
			return 0, fmt.Errorf("update repriced row: %w", err)
		}
	}
	return len(updates), nil
}

// repricingMissing 只补“有 token 但费用为 0”的行，用最新修订的价格；
// 它回答的是“现在知道的价格”，不会改写已经有价的历史。
func repricingMissing(ctx context.Context, tx *sql.Tx, timeline pricing.Timeline) (int, error) {
	latest, ok := timeline.Latest()
	if !ok {
		return 0, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, timestamp, model, prompt_tokens, completion_tokens, cached_tokens, fixed_cost
		FROM usage_log
		WHERE cost = 0 AND (prompt_tokens + completion_tokens) > 0
		  AND endpoint NOT LIKE '/v1/tools/%'`)
	if err != nil {
		return 0, fmt.Errorf("query missing costs: %w", err)
	}
	type pending struct {
		id   int64
		cost float64
	}
	var updates []pending
	for rows.Next() {
		var id int64
		var timestamp, fixedCost float64
		var model string
		var prompt, completion, cached int
		if err := rows.Scan(&id, &timestamp, &model, &prompt, &completion, &cached, &fixedCost); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan missing cost: %w", err)
		}
		cost := latest.Catalog.Cost(model, prompt, completion, cached, unixTime(timestamp))
		if cost <= 0 {
			continue
		}
		updates = append(updates, pending{id: id, cost: cost + fixedCost})
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close missing costs: %w", err)
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx,
			"UPDATE usage_log SET cost = ?, pricing_revision = ? WHERE id = ?",
			update.cost, latest.ID, update.id); err != nil {
			return 0, fmt.Errorf("backfill missing cost: %w", err)
		}
	}
	return len(updates), nil
}

func unixTime(value float64) time.Time {
	seconds := int64(value)
	nanos := int64((value - float64(seconds)) * 1e9)
	return time.Unix(seconds, nanos)
}
