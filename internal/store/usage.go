package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Insert 写入一条用量记录。相同时间、模型、提供商与 token 数的重复记录由唯一索引忽略。
func (s *Store) Insert(ctx context.Context, record UsageRecord) error {
	timeline := s.currentTimeline()
	tokenCost := timeline.Cost(record.Model, record.PromptTokens, record.CompletionTokens,
		record.CachedTokens, record.Timestamp)
	revisionID := timeline.RevisionID(record.Timestamp)
	client := ClientFromUserAgent(record.UserAgent)

	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO usage_log (
				uuid, timestamp, provider_id, model, endpoint,
				prompt_tokens, completion_tokens, total_tokens, cached_tokens,
				reasoning_tokens, latency_ms, status_code, cost, user_agent,
				pricing_revision, client, fixed_cost
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.UUID, unixSeconds(record.Timestamp), record.ProviderID, record.Model, record.Endpoint,
			record.PromptTokens, record.CompletionTokens, record.TotalTokens, record.CachedTokens,
			record.ReasoningTokens, record.LatencyMS, record.StatusCode,
			tokenCost+record.FixedCost, record.UserAgent, revisionID, client, record.FixedCost)
		if err != nil {
			return fmt.Errorf("insert usage: %w", err)
		}
		return s.pruneIfDue(ctx, tx)
	})
}

func (s *Store) pruneIfDue(ctx context.Context, tx *sql.Tx) error {
	now := s.currentTime()
	if !s.lastPruneAt.IsZero() && now.Sub(s.lastPruneAt) < 24*time.Hour {
		return nil
	}
	s.lastPruneAt = now
	if s.retentionDays <= 0 {
		return nil
	}
	cutoff := now.AddDate(0, 0, -s.retentionDays)
	if _, err := tx.ExecContext(ctx, "DELETE FROM usage_log WHERE timestamp < ?", unixSeconds(cutoff)); err != nil {
		return fmt.Errorf("prune usage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM balance_sample WHERE timestamp < ?", unixSeconds(cutoff)); err != nil {
		return fmt.Errorf("prune balance samples: %w", err)
	}
	return nil
}

// PerformStartupMaintenance 补算缺失费用、回填客户端归类并清理过期数据。
// 这是写操作，因此整段都跑在单写 goroutine 里。
func (s *Store) PerformStartupMaintenance(ctx context.Context) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := s.refreshClientNames(ctx, tx); err != nil {
			return err
		}
		timeline := s.currentTimeline()
		if _, err := repricingMissing(ctx, tx, timeline); err != nil {
			return err
		}
		if s.retentionDays > 0 {
			cutoff := s.currentTime().AddDate(0, 0, -s.retentionDays)
			if _, err := tx.ExecContext(ctx, "DELETE FROM usage_log WHERE timestamp < ?", unixSeconds(cutoff)); err != nil {
				return fmt.Errorf("prune usage: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM balance_sample WHERE timestamp < ?", unixSeconds(cutoff)); err != nil {
				return fmt.Errorf("prune balance samples: %w", err)
			}
			s.lastPruneAt = s.currentTime()
		}
		return nil
	})
}

// SetRetentionDays 在运行期切换保留期，并立即按新设置清理一次。
// 值只允许在这里和写 goroutine 内修改，避免与 Insert 的定时清理竞争。
func (s *Store) SetRetentionDays(ctx context.Context, days int) error {
	if days < 0 {
		return errors.New("retention days cannot be negative")
	}
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		s.retentionDays = days
		s.lastPruneAt = time.Time{}
		return s.pruneIfDue(ctx, tx)
	})
}

func (s *Store) RederiveClientNames(ctx context.Context) (int, error) {
	var changed int64
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			"SELECT id, user_agent FROM usage_log WHERE user_agent <> ''")
		if err != nil {
			return fmt.Errorf("read user agents: %w", err)
		}
		type update struct {
			id     int64
			client string
		}
		var updates []update
		for rows.Next() {
			var id int64
			var ua string
			if err := rows.Scan(&id, &ua); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan user agent: %w", err)
			}
			updates = append(updates, update{id: id, client: ClientFromUserAgent(ua)})
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close user agents: %w", err)
		}
		for _, item := range updates {
			result, err := tx.ExecContext(ctx, "UPDATE usage_log SET client = ? WHERE id = ? AND client <> ?",
				item.client, item.id, item.client)
			if err != nil {
				return fmt.Errorf("rederive client: %w", err)
			}
			n, _ := result.RowsAffected()
			changed += n
		}
		return nil
	})
	return int(changed), err
}

func (s *Store) refreshClientNames(ctx context.Context, tx *sql.Tx) error {
	stored := ""
	_ = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'client_rule_version'").Scan(&stored)
	if stored != fmt.Sprint(ClientRuleVersion) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE usage_log SET client = '' WHERE user_agent <> ''`); err != nil {
			return fmt.Errorf("reset client names: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO meta (key, value) VALUES ('client_rule_version', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(ClientRuleVersion)); err != nil {
			return fmt.Errorf("save client rule version: %w", err)
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, user_agent FROM usage_log
		WHERE user_agent <> '' AND (client = '' OR client IS NULL)`)
	if err != nil {
		return fmt.Errorf("read rows needing client: %w", err)
	}
	type update struct {
		id     int64
		client string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var ua string
		if err := rows.Scan(&id, &ua); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan row needing client: %w", err)
		}
		updates = append(updates, update{id: id, client: ClientFromUserAgent(ua)})
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close rows needing client: %w", err)
	}
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, "UPDATE usage_log SET client = ? WHERE id = ?", item.client, item.id); err != nil {
			return fmt.Errorf("backfill client: %w", err)
		}
	}

	stored = ""
	_ = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'client_inference_version'").Scan(&stored)
	if stored != fmt.Sprint(ClientInferenceVersion) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE usage_log SET client = 'codex'
			WHERE client = 'Codex Desktop' OR lower(client) LIKE 'codex-%'`); err != nil {
			return fmt.Errorf("merge legacy codex clients: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE usage_log SET client = CASE
				WHEN endpoint LIKE '%/responses%' THEN 'codex'
				WHEN endpoint LIKE '%/chat/completions%' THEN 'opencode'
				ELSE client END
			WHERE user_agent = '' AND client = ''`); err != nil {
			return fmt.Errorf("infer legacy clients: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO meta (key, value) VALUES ('client_inference_version', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(ClientInferenceVersion)); err != nil {
			return fmt.Errorf("save client inference version: %w", err)
		}
	}
	return nil
}
