package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dsmon/internal/pricing"
)

// ImportResult 描述一次导入做了什么。Copied=true 表示目标库原本不存在，
// 直接复制了一份已校验的源库；否则为合并。
type ImportResult struct {
	Source         string
	Target         string
	Copied         bool
	SourceRows     int
	Inserted       int
	Skipped        int
	Revisions      int
	BalanceSamples int
}

// ImportDatabase 把 Mac 版（或旧版 Linux）usage.db 导入目标库。
func ImportDatabase(ctx context.Context, targetPath, sourcePath string, opts ...Option) (ImportResult, error) {
	if targetPath == "" || sourcePath == "" {
		return ImportResult{}, errors.New("target and source database paths are required")
	}
	result := ImportResult{Source: sourcePath, Target: targetPath}
	if samePath(targetPath, sourcePath) {
		return result, errors.New("source and target database are the same file")
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return result, fmt.Errorf("stat source database: %w", err)
	}

	preparedPath, cleanup, err := prepareImportSource(ctx, sourcePath)
	if err != nil {
		return result, err
	}
	defer cleanup()

	sourceDB, err := sql.Open("sqlite", "file:"+preparedPath+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return result, fmt.Errorf("open prepared source: %w", err)
	}
	defer sourceDB.Close()
	if err := sourceDB.PingContext(ctx); err != nil {
		return result, fmt.Errorf("ping prepared source: %w", err)
	}
	usageColumns, err := tableColumns(ctx, sourceDB, "usage_log")
	if err != nil {
		return result, err
	}
	if len(usageColumns) == 0 {
		return result, errors.New("source database has no usage_log table")
	}
	if err := validateUsageColumns(usageColumns); err != nil {
		return result, err
	}
	result.SourceRows, err = tableCount(ctx, sourceDB, "usage_log")
	if err != nil {
		return result, err
	}
	revisionColumns, _ := tableColumns(ctx, sourceDB, "pricing_revision")
	result.Revisions, _ = tableCount(ctx, sourceDB, "pricing_revision")
	sampleColumns, _ := tableColumns(ctx, sourceDB, "balance_sample")
	result.BalanceSamples, _ = tableCount(ctx, sourceDB, "balance_sample")

	if _, err := os.Stat(targetPath); errors.Is(err, os.ErrNotExist) {
		if err := copyFileAtomic(preparedPath, targetPath); err != nil {
			return result, err
		}
		store, err := Open(targetPath, opts...)
		if err != nil {
			return result, fmt.Errorf("open copied target: %w", err)
		}
		if err := store.PerformStartupMaintenance(ctx); err != nil {
			_ = store.Close()
			return result, fmt.Errorf("maintain copied target: %w", err)
		}
		if err := store.Close(); err != nil {
			return result, fmt.Errorf("close copied target: %w", err)
		}
		result.Copied = true
		result.Inserted = result.SourceRows
		return result, nil
	} else if err != nil {
		return result, fmt.Errorf("stat target database: %w", err)
	}

	store, err := Open(targetPath, opts...)
	if err != nil {
		return result, fmt.Errorf("open target database: %w", err)
	}
	defer store.Close()

	revisionMap := map[int64]int64{}
	if len(revisionColumns) > 0 {
		revisions, err := readSourceRevisions(ctx, sourceDB, revisionColumns)
		if err != nil {
			return result, err
		}
		var importedRevisions int
		revisionMap, importedRevisions, err = store.mergePricingRevisions(ctx, revisions)
		if err != nil {
			return result, err
		}
		result.Revisions = importedRevisions
	}

	tag := importTag(targetPath, sourcePath)
	rows, err := readSourceUsage(ctx, sourceDB, usageColumns, tag, revisionMap)
	if err != nil {
		return result, err
	}
	for start := 0; start < len(rows); start += 1000 {
		end := start + 1000
		if end > len(rows) {
			end = len(rows)
		}
		inserted, skipped, err := store.importUsageBatch(ctx, rows[start:end])
		if err != nil {
			return result, err
		}
		result.Inserted += inserted
		result.Skipped += skipped
	}

	importedSamples := 0
	if len(sampleColumns) > 0 {
		samples, err := readSourceBalanceSamples(ctx, sourceDB, sampleColumns)
		if err != nil {
			return result, err
		}
		for start := 0; start < len(samples); start += 1000 {
			end := start + 1000
			if end > len(samples) {
				end = len(samples)
			}
			inserted, err := store.importBalanceSamples(ctx, samples[start:end])
			if err != nil {
				return result, err
			}
			importedSamples += inserted
		}
	}
	result.BalanceSamples = importedSamples
	if err := store.PerformStartupMaintenance(ctx); err != nil {
		return result, fmt.Errorf("maintain imported target: %w", err)
	}
	return result, nil
}

func prepareImportSource(ctx context.Context, sourcePath string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "dsmon-import-*")
	if err != nil {
		return "", nil, fmt.Errorf("create import temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	preparedPath := filepath.Join(dir, filepath.Base(sourcePath))
	if err := copyFile(sourcePath, preparedPath); err != nil {
		cleanup()
		return "", nil, err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := copyFileIfExists(sourcePath+suffix, preparedPath+suffix); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	db, err := sql.Open("sqlite", sqliteDSN(preparedPath))
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("open source copy: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("ping source copy: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("checkpoint source copy: %w", err)
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("check source integrity: %w", err)
	}
	if integrity != "ok" {
		cleanup()
		return "", nil, fmt.Errorf("source integrity check failed: %s", integrity)
	}
	return preparedPath, cleanup, nil
}

func validateUsageColumns(columns map[string]bool) error {
	required := []string{"timestamp", "model", "endpoint", "prompt_tokens", "completion_tokens"}
	for _, name := range required {
		if !columns[name] {
			return fmt.Errorf("source usage_log is missing column %q", name)
		}
	}
	return nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(table)+")")
	if err != nil {
		return nil, fmt.Errorf("read %s columns: %w", table, err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("scan %s column: %w", table, err)
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func tableCount(ctx context.Context, db *sql.DB, table string) (int, error) {
	columns, err := tableColumns(ctx, db, table)
	if err != nil || len(columns) == 0 {
		return 0, err
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdentifier(table)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return count, nil
}

type sourceRevision struct {
	ID          int64
	ValidFrom   float64
	Fingerprint string
	Source      string
	Text        string
	ImportedAt  float64
}

func readSourceRevisions(ctx context.Context, db *sql.DB, columns map[string]bool) ([]sourceRevision, error) {
	selectText := revisionSelect(columns)
	rows, err := db.QueryContext(ctx, selectText+" ORDER BY valid_from ASC, id ASC")
	if err != nil {
		return nil, fmt.Errorf("query source revisions: %w", err)
	}
	defer rows.Close()
	var revisions []sourceRevision
	for rows.Next() {
		var item sourceRevision
		if err := rows.Scan(&item.ID, &item.ValidFrom, &item.Fingerprint, &item.Source, &item.Text, &item.ImportedAt); err != nil {
			return nil, fmt.Errorf("scan source revision: %w", err)
		}
		revisions = append(revisions, item)
	}
	return revisions, rows.Err()
}

func revisionSelect(columns map[string]bool) string {
	expr := func(name, fallback string) string {
		if columns[name] {
			return "COALESCE(" + quoteIdentifier(name) + ", " + fallback + ")"
		}
		return fallback
	}
	return "SELECT " +
		expr("id", "0") + ", " +
		expr("valid_from", "0") + ", " +
		expr("fingerprint", "''") + ", " +
		expr("source", "''") + ", " +
		expr("text", "''") + ", " +
		expr("imported_at", "0") +
		" FROM pricing_revision"
}

type importedUsageRow struct {
	UUID             string
	Timestamp        float64
	ProviderID       string
	Model            string
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CachedTokens     int
	ReasoningTokens  int
	LatencyMS        float64
	StatusCode       int
	Cost             float64
	FixedCost        float64
	UserAgent        string
	PricingRevision  int64
	Client           string
}

func readSourceUsage(ctx context.Context, db *sql.DB, columns map[string]bool, tag string,
	revisionMap map[int64]int64) ([]importedUsageRow, error) {
	rows, err := db.QueryContext(ctx, usageSelect(columns))
	if err != nil {
		return nil, fmt.Errorf("query source usage: %w", err)
	}
	defer rows.Close()
	var result []importedUsageRow
	for rows.Next() {
		var id int64
		var item importedUsageRow
		if err := rows.Scan(&id, &item.Timestamp, &item.Model, &item.Endpoint,
			&item.PromptTokens, &item.CompletionTokens, &item.TotalTokens,
			&item.CachedTokens, &item.ReasoningTokens, &item.LatencyMS,
			&item.StatusCode, &item.ProviderID, &item.Cost, &item.FixedCost,
			&item.UUID, &item.UserAgent, &item.PricingRevision, &item.Client); err != nil {
			return nil, fmt.Errorf("scan source usage: %w", err)
		}
		if item.UUID == "" {
			item.UUID = fmt.Sprintf("import-%s-%d", tag, id)
		}
		if item.TotalTokens == 0 {
			item.TotalTokens = item.PromptTokens + item.CompletionTokens
		}
		if item.ProviderID == "" {
			item.ProviderID = "deepseek"
		}
		// 没有 UA 的旧行交给启动维护按 endpoint 反推；这里写成“未知”会挡住反推。
		if item.Client == "" && item.UserAgent != "" {
			item.Client = ClientFromUserAgent(item.UserAgent)
		}
		item.PricingRevision = revisionMap[item.PricingRevision]
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source usage: %w", err)
	}
	return result, nil
}

func usageSelect(columns map[string]bool) string {
	expr := func(name, fallback string) string {
		if columns[name] {
			return "COALESCE(" + quoteIdentifier(name) + ", " + fallback + ")"
		}
		return fallback
	}
	return "SELECT " +
		expr("id", "0") + ", " +
		expr("timestamp", "0") + ", " +
		expr("model", "''") + ", " +
		expr("endpoint", "''") + ", " +
		expr("prompt_tokens", "0") + ", " +
		expr("completion_tokens", "0") + ", " +
		expr("total_tokens", "0") + ", " +
		expr("cached_tokens", "0") + ", " +
		expr("reasoning_tokens", "0") + ", " +
		expr("latency_ms", "0") + ", " +
		expr("status_code", "200") + ", " +
		expr("provider_id", "''") + ", " +
		expr("cost", "0") + ", " +
		expr("fixed_cost", "0") + ", " +
		expr("uuid", "''") + ", " +
		expr("user_agent", "''") + ", " +
		expr("pricing_revision", "0") + ", " +
		expr("client", "''") +
		" FROM usage_log ORDER BY timestamp ASC, id ASC"
}

type importedBalanceSample struct {
	Timestamp float64
	Balance   float64
	Granted   float64
	ToppedUp  float64
}

func readSourceBalanceSamples(ctx context.Context, db *sql.DB, columns map[string]bool) ([]importedBalanceSample, error) {
	expr := func(name, fallback string) string {
		if columns[name] {
			return "COALESCE(" + quoteIdentifier(name) + ", " + fallback + ")"
		}
		return fallback
	}
	rows, err := db.QueryContext(ctx, "SELECT "+
		expr("timestamp", "0")+", "+expr("balance", "0")+", "+
		expr("granted", "0")+", "+expr("topped_up", "0")+
		" FROM balance_sample ORDER BY timestamp ASC")
	if err != nil {
		return nil, fmt.Errorf("query source balance samples: %w", err)
	}
	defer rows.Close()
	var samples []importedBalanceSample
	for rows.Next() {
		var item importedBalanceSample
		if err := rows.Scan(&item.Timestamp, &item.Balance, &item.Granted, &item.ToppedUp); err != nil {
			return nil, fmt.Errorf("scan source balance sample: %w", err)
		}
		samples = append(samples, item)
	}
	return samples, rows.Err()
}

func (s *Store) importUsageBatch(ctx context.Context, rows []importedUsageRow) (inserted, skipped int, err error) {
	err = s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, row := range rows {
			result, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO usage_log (
					uuid, timestamp, provider_id, model, endpoint,
					prompt_tokens, completion_tokens, total_tokens, cached_tokens,
					reasoning_tokens, latency_ms, status_code, cost, fixed_cost,
					user_agent, pricing_revision, client
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				row.UUID, row.Timestamp, row.ProviderID, row.Model, row.Endpoint,
				row.PromptTokens, row.CompletionTokens, row.TotalTokens, row.CachedTokens,
				row.ReasoningTokens, row.LatencyMS, row.StatusCode, row.Cost, row.FixedCost,
				row.UserAgent, row.PricingRevision, row.Client)
			if err != nil {
				return fmt.Errorf("import usage row: %w", err)
			}
			affected, _ := result.RowsAffected()
			if affected > 0 {
				inserted++
			} else {
				skipped++
			}
		}
		return nil
	})
	return inserted, skipped, err
}

func (s *Store) mergePricingRevisions(ctx context.Context, source []sourceRevision) (map[int64]int64, int, error) {
	result := make(map[int64]int64, len(source))
	imported := 0
	var nextTimeline pricing.Timeline
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		existing, err := loadPricingRevisions(ctx, tx)
		if err != nil {
			return err
		}
		byFingerprint := map[string]int64{}
		for _, revision := range existing {
			if _, ok := byFingerprint[revision.Fingerprint]; !ok {
				byFingerprint[revision.Fingerprint] = revision.ID
			}
		}
		for _, revision := range source {
			fingerprint := revision.Fingerprint
			if fingerprint == "" {
				fingerprint = pricing.Fingerprint(revision.Text)
			}
			if existingID, ok := byFingerprint[fingerprint]; ok {
				result[revision.ID] = existingID
				continue
			}
			newID, err := insertPricingRevision(ctx, tx, revision.Text, pricing.Source(revision.Source),
				fingerprint, unixTime(revision.ValidFrom), unixTime(revision.ImportedAt))
			if err != nil {
				return err
			}
			result[revision.ID] = newID
			byFingerprint[fingerprint] = newID
			imported++
		}
		revisions, err := loadPricingRevisions(ctx, tx)
		if err != nil {
			return err
		}
		nextTimeline = pricing.NewTimeline(revisions)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	s.setTimeline(nextTimeline)
	return result, imported, nil
}

func (s *Store) importBalanceSamples(ctx context.Context, samples []importedBalanceSample) (int, error) {
	inserted := 0
	err := s.write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, sample := range samples {
			result, err := tx.ExecContext(ctx, `
				INSERT INTO balance_sample (timestamp, balance, granted, topped_up)
				SELECT ?, ?, ?, ?
				WHERE NOT EXISTS (
					SELECT 1 FROM balance_sample
					WHERE timestamp = ? AND balance = ? AND granted = ? AND topped_up = ?
				)`, sample.Timestamp, sample.Balance, sample.Granted, sample.ToppedUp,
				sample.Timestamp, sample.Balance, sample.Granted, sample.ToppedUp)
			if err != nil {
				return fmt.Errorf("import balance sample: %w", err)
			}
			affected, _ := result.RowsAffected()
			inserted += int(affected)
		}
		return nil
	})
	return inserted, err
}

func copyFileAtomic(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".dsmon-import-*")
	if err != nil {
		return fmt.Errorf("create target temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := copyInto(tmp, source); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync target temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close target temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("chmod target temp file: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("install target database: %w", err)
	}
	return nil
}

func copyFile(source, target string) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create target file: %w", err)
	}
	defer out.Close()
	if err := copyInto(out, source); err != nil {
		return err
	}
	return nil
}

func copyInto(out *os.File, source string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer in.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy database: %w", err)
	}
	return nil
}

func copyFileIfExists(source, target string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat source companion: %w", err)
	}
	return copyFile(source, target)
}

func samePath(first, second string) bool {
	firstAbs, errFirst := filepath.Abs(first)
	secondAbs, errSecond := filepath.Abs(second)
	if errFirst == nil && errSecond == nil && filepath.Clean(firstAbs) == filepath.Clean(secondAbs) {
		return true
	}
	firstInfo, errFirst := os.Stat(first)
	secondInfo, errSecond := os.Stat(second)
	return errFirst == nil && errSecond == nil && os.SameFile(firstInfo, secondInfo)
}

func importTag(target, source string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(source) + "\x00" + filepath.Clean(target)))
	return hex.EncodeToString(sum[:4])
}
