package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (s *Store) MetaValue(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read meta %q: %w", key, err)
	}
	return value, true, nil
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		if err != nil {
			return fmt.Errorf("write meta %q: %w", key, err)
		}
		return nil
	})
}

func (s *Store) DeleteMeta(ctx context.Context, key string) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key = ?", key); err != nil {
			return fmt.Errorf("delete meta %q: %w", key, err)
		}
		return nil
	})
}

func (s *Store) RecordBalanceSample(ctx context.Context, sample BalanceSample) error {
	return s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO balance_sample (timestamp, balance, granted, topped_up)
			VALUES (?, ?, ?, ?)`, unixSeconds(sample.Timestamp), sample.Balance, sample.Granted, sample.ToppedUp)
		if err != nil {
			return fmt.Errorf("record balance sample: %w", err)
		}
		return nil
	})
}

func (s *Store) BalanceSamples(ctx context.Context, since time.Time) ([]BalanceSample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT timestamp, balance, granted, topped_up
		FROM balance_sample WHERE timestamp >= ? ORDER BY timestamp ASC`, unixSeconds(since))
	if err != nil {
		return nil, fmt.Errorf("query balance samples: %w", err)
	}
	defer rows.Close()
	var result []BalanceSample
	for rows.Next() {
		var timestamp, balance, granted, toppedUp float64
		if err := rows.Scan(&timestamp, &balance, &granted, &toppedUp); err != nil {
			return nil, fmt.Errorf("scan balance sample: %w", err)
		}
		result = append(result, BalanceSample{
			Timestamp: unixTime(timestamp),
			Balance:   balance,
			Granted:   granted,
			ToppedUp:  toppedUp,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate balance samples: %w", err)
	}
	return result, nil
}
