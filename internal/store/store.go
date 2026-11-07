// Package store 负责 SQLite 持久化。所有写操作都串行进一个 goroutine，
// HTTP handler、余额轮询和维护任务不得绕过它直接写库。
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"dsmon/internal/pricing"
)

var (
	ErrClosed       = errors.New("store closed")
	ErrInvalidRange = errors.New("invalid time range")
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db            *sql.DB
	now           func() time.Time
	loc           *time.Location
	retentionDays int
	writes        chan writeRequest
	writeMu       sync.RWMutex
	closed        bool
	wg            sync.WaitGroup
	closeOnce     sync.Once
	closeErr      error
	timelineMu    sync.RWMutex
	timeline      pricing.Timeline
	lastPruneAt   time.Time // 只由写 goroutine 读写
}

type writeRequest struct {
	ctx  context.Context
	fn   func(context.Context, *sql.Tx) error
	done chan error
}

type options struct {
	pricing       *pricing.Loaded
	now           func() time.Time
	location      *time.Location
	retentionDays int
}

type Option func(*options)

// WithPricing 注入固定规则，测试用它避免依赖用户目录里的覆盖文件。
func WithPricing(loaded pricing.Loaded) Option {
	return func(o *options) { o.pricing = &loaded }
}

// WithClock 注入时钟。纯聚合测试需要可重复的“今天”。
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

func WithLocation(loc *time.Location) Option {
	return func(o *options) { o.location = loc }
}

// WithRetentionDays 控制历史保留期；0 表示永久保留。
func WithRetentionDays(days int) Option {
	return func(o *options) { o.retentionDays = days }
}

// Open 打开（必要时创建）用量库。path 不存在时会把父目录建出来。
func Open(path string, opts ...Option) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is empty")
	}
	cfg := options{now: time.Now, location: time.Local, retentionDays: 180}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.location == nil {
		cfg.location = time.Local
	}
	if cfg.retentionDays < 0 {
		cfg.retentionDays = 0
	}

	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &Store{
		db:            db,
		now:           cfg.now,
		loc:           cfg.location,
		retentionDays: cfg.retentionDays,
		writes:        make(chan writeRequest, 64),
	}
	if err := s.initialize(ctx, cfg.pricing); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.wg.Add(1)
	go s.writeLoop()
	return s, nil
}

func sqliteDSN(path string) string {
	dsn := path
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "_pragma=busy_timeout(2000)&_pragma=journal_mode(WAL)"
}

func (s *Store) initialize(ctx context.Context, loaded *pricing.Loaded) error {
	if err := migrateLegacySchema(ctx, s.db); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	if loaded == nil {
		value := pricing.LoadDefault(nil)
		loaded = &value
	}
	if err := s.bootstrapPricing(ctx, *loaded); err != nil {
		return err
	}
	return s.reloadTimeline(ctx)
}

// migrateLegacySchema 只处理 Mac 老库缺少的新列。全新库直接按最终 schema 建表。
func migrateLegacySchema(ctx context.Context, db *sql.DB) error {
	var name string
	err := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'usage_log'").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy schema: %w", err)
	}

	rows, err := db.QueryContext(ctx, "PRAGMA table_info(usage_log)")
	if err != nil {
		return fmt.Errorf("read usage_log columns: %w", err)
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var column, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &column, &columnType, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan usage_log column: %w", err)
		}
		columns[column] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close usage_log columns: %w", err)
	}

	legacyColumns := []struct {
		name string
		ddl  string
	}{
		{"provider_id", "ALTER TABLE usage_log ADD COLUMN provider_id TEXT DEFAULT ''"},
		{"fixed_cost", "ALTER TABLE usage_log ADD COLUMN fixed_cost REAL DEFAULT 0"},
		{"uuid", "ALTER TABLE usage_log ADD COLUMN uuid TEXT DEFAULT ''"},
		{"user_agent", "ALTER TABLE usage_log ADD COLUMN user_agent TEXT DEFAULT ''"},
		{"pricing_revision", "ALTER TABLE usage_log ADD COLUMN pricing_revision INTEGER DEFAULT 0"},
		{"client", "ALTER TABLE usage_log ADD COLUMN client TEXT DEFAULT ''"},
	}
	for _, column := range legacyColumns {
		if columns[column.name] {
			continue
		}
		if _, err := db.ExecContext(ctx, column.ddl); err != nil {
			return fmt.Errorf("add usage_log.%s: %w", column.name, err)
		}
	}
	_, _ = db.ExecContext(ctx, `
		UPDATE usage_log
		SET uuid = lower(hex(randomblob(16))) || '-' || lower(hex(randomblob(16)))
		WHERE uuid = '' OR uuid IS NULL`)
	return nil
}

func (s *Store) writeLoop() {
	defer s.wg.Done()
	for request := range s.writes {
		request.done <- s.executeWrite(request)
	}
}

func (s *Store) executeWrite(request writeRequest) error {
	tx, err := s.db.BeginTx(request.ctx, nil)
	if err != nil {
		return fmt.Errorf("begin write transaction: %w", err)
	}
	if err := request.fn(request.ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write transaction: %w", err)
	}
	return nil
}

func (s *Store) write(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	s.writeMu.RLock()
	if s.closed {
		s.writeMu.RUnlock()
		return ErrClosed
	}
	request := writeRequest{ctx: ctx, fn: fn, done: make(chan error, 1)}
	select {
	case s.writes <- request:
		s.writeMu.RUnlock()
	case <-ctx.Done():
		s.writeMu.RUnlock()
		return ctx.Err()
	}
	select {
	case err := <-request.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 停止接收写入，等写 goroutine 排空队列后关闭数据库。
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.writeMu.Lock()
		s.closed = true
		close(s.writes)
		s.writeMu.Unlock()
		s.wg.Wait()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

func (s *Store) currentTime() time.Time {
	return s.now().In(s.loc)
}

func (s *Store) setTimeline(timeline pricing.Timeline) {
	s.timelineMu.Lock()
	s.timeline = timeline
	s.timelineMu.Unlock()
}

func (s *Store) currentTimeline() pricing.Timeline {
	s.timelineMu.RLock()
	defer s.timelineMu.RUnlock()
	return s.timeline
}

func (s *Store) PricingTimeline() pricing.Timeline {
	return s.currentTimeline()
}
