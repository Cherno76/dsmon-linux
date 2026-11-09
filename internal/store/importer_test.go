package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dsmon/internal/pricing"
)

const legacyImportSchema = `
CREATE TABLE usage_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp REAL NOT NULL,
    provider_id TEXT DEFAULT '',
    model TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    prompt_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    total_tokens INTEGER DEFAULT 0,
    cached_tokens INTEGER DEFAULT 0,
    reasoning_tokens INTEGER DEFAULT 0,
    latency_ms REAL DEFAULT 0,
    status_code INTEGER DEFAULT 200,
    cost REAL DEFAULT 0,
    fixed_cost REAL DEFAULT 0
);
CREATE TABLE balance_sample (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp REAL NOT NULL,
    balance REAL NOT NULL,
    granted REAL DEFAULT 0,
    topped_up REAL DEFAULT 0
);
`

type fixtureUsage struct {
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

func openImportFixture(t *testing.T, path string, legacy bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	schema := schemaSQL
	if legacy {
		schema = legacyImportSchema
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		t.Fatalf("create fixture schema: %v", err)
	}
	return db
}

func insertFixtureUsage(t *testing.T, db *sql.DB, row fixtureUsage) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO usage_log (
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
		t.Fatalf("insert fixture usage: %v", err)
	}
}

func insertLegacyUsage(t *testing.T, db *sql.DB, timestamp float64, endpoint string, cost float64) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO usage_log (
			timestamp, provider_id, model, endpoint, prompt_tokens,
			completion_tokens, total_tokens, cost
		) VALUES (?, 'deepseek', 'deepseek-flash', ?, 100, 10, 110, ?)`,
		timestamp, endpoint, cost)
	if err != nil {
		t.Fatalf("insert legacy usage: %v", err)
	}
}

func importTestOptions(t *testing.T, ruleText string) []Option {
	t.Helper()
	return []Option{
		WithPricing(loadedRules(t, ruleText)),
		WithClock(func() time.Time { return testNow }),
		WithLocation(testLocation),
		WithRetentionDays(0),
	}
}

func unixFixtureTime(at time.Time) float64 {
	return float64(at.UnixNano()) / 1e9
}

func databaseFileSnapshot(t *testing.T, path string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		data, err := os.ReadFile(candidate)
		if os.IsNotExist(err) {
			result[candidate] = "missing"
			continue
		}
		if err != nil {
			t.Fatalf("read database file %s: %v", candidate, err)
		}
		sum := sha256.Sum256(data)
		result[candidate] = hex.EncodeToString(sum[:])
	}
	return result
}

func TestImportDatabaseCopiesLegacySource(t *testing.T) {
	ctx := context.Background()
	ruleText := rules(1, 0.1, 2, "deepseek-flash")
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "mac-usage.db")
	targetPath := filepath.Join(dir, "data", "usage.db")
	base := testNow.Add(-3 * time.Hour)

	source := openImportFixture(t, sourcePath, true)
	insertLegacyUsage(t, source, unixFixtureTime(base), "/v1/responses", 0.25)
	insertLegacyUsage(t, source, unixFixtureTime(base.Add(time.Hour)), "/v1/chat/completions", 0.5)
	if _, err := source.Exec(`INSERT INTO balance_sample (timestamp, balance, granted, topped_up)
		VALUES (?, 12.5, 3, 9.5)`, unixFixtureTime(base)); err != nil {
		t.Fatalf("insert balance sample: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	before := databaseFileSnapshot(t, sourcePath)

	result, err := ImportDatabase(ctx, targetPath, sourcePath, importTestOptions(t, ruleText)...)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !result.Copied || result.SourceRows != 2 || result.Inserted != 2 || result.Skipped != 0 {
		t.Fatalf("unexpected copy result: %+v", result)
	}
	if result.Revisions != 0 || result.BalanceSamples != 1 {
		t.Fatalf("unexpected copied revision/sample counts: %+v", result)
	}
	if after := databaseFileSnapshot(t, sourcePath); !reflect.DeepEqual(before, after) {
		t.Fatalf("source database was modified:\nbefore=%v\nafter=%v", before, after)
	}

	target := openTestStore(t, targetPath, ruleText)
	rows, err := target.db.QueryContext(ctx, `
		SELECT endpoint, cost, client, pricing_revision, total_tokens
		FROM usage_log ORDER BY timestamp ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []struct {
		endpoint, client string
		cost             float64
		revision, total  int64
	}
	for rows.Next() {
		var item struct {
			endpoint, client string
			cost             float64
			revision, total  int64
		}
		if err := rows.Scan(&item.endpoint, &item.cost, &item.client, &item.revision, &item.total); err != nil {
			t.Fatal(err)
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("copied rows = %d, want 2", len(got))
	}
	if got[0].endpoint != "/v1/responses" || got[0].client != "codex" ||
		got[0].cost != 0.25 || got[0].revision == 0 || got[0].total != 110 {
		t.Fatalf("first copied row = %+v", got[0])
	}
	if got[1].endpoint != "/v1/chat/completions" || got[1].client != "opencode" || got[1].cost != 0.5 {
		t.Fatalf("second copied row = %+v", got[1])
	}
	var samples int
	if err := target.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM balance_sample`).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if samples != 1 {
		t.Fatalf("balance samples = %d, want 1", samples)
	}
}

func TestImportDatabaseMergesRemapsAndDedupes(t *testing.T) {
	ctx := context.Background()
	ruleText := rules(1, 0.1, 2, "deepseek-flash")
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "mac-usage.db")
	targetPath := filepath.Join(dir, "usage.db")
	base := testNow.Add(-3 * time.Hour)
	overlapAt := base.Add(30 * time.Minute)
	overlap := recordAt(overlapAt, "deepseek-flash")
	balanceAt := base.Add(-time.Hour)

	target := openTestStore(t, targetPath, ruleText)
	if err := target.Insert(ctx, overlap); err != nil {
		t.Fatal(err)
	}
	if err := target.RecordBalanceSample(ctx, BalanceSample{
		Timestamp: balanceAt, Balance: 20, Granted: 5, ToppedUp: 15,
	}); err != nil {
		t.Fatal(err)
	}
	revisions, err := target.PricingRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 {
		t.Fatalf("target revisions = %d, want 1", len(revisions))
	}
	targetRevision := revisions[0].ID
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	source := openImportFixture(t, sourcePath, false)
	if _, err := source.Exec(`
		INSERT INTO pricing_revision (id, valid_from, fingerprint, source, text, imported_at)
		VALUES (7, ?, ?, 'bundled', ?, ?)`,
		unixFixtureTime(base.AddDate(0, 0, -30)), pricing.Fingerprint(ruleText), ruleText,
		unixFixtureTime(base)); err != nil {
		t.Fatalf("insert source revision: %v", err)
	}
	insertFixtureUsage(t, source, fixtureUsage{
		UUID: "source-overlap", Timestamp: unixFixtureTime(overlapAt), ProviderID: "deepseek",
		Model: "deepseek-flash", Endpoint: overlap.Endpoint, PromptTokens: overlap.PromptTokens,
		CompletionTokens: overlap.CompletionTokens, TotalTokens: overlap.TotalTokens,
		Cost: 99, StatusCode: 200, UserAgent: overlap.UserAgent, PricingRevision: 7,
	})
	insertFixtureUsage(t, source, fixtureUsage{
		Timestamp: unixFixtureTime(base.Add(-2 * time.Hour)), ProviderID: "deepseek",
		Model: "deepseek-flash", Endpoint: "/v1/chat/completions", PromptTokens: 500,
		CompletionTokens: 50, TotalTokens: 550, Cost: 1.25, StatusCode: 200,
		UserAgent: "opencode/1.18.25 ai-sdk/4", PricingRevision: 7,
	})
	insertFixtureUsage(t, source, fixtureUsage{
		UUID: "responses-row", Timestamp: unixFixtureTime(base.Add(-3 * time.Hour)),
		ProviderID: "deepseek", Model: "deepseek-flash", Endpoint: "/v1/responses",
		PromptTokens: 300, CompletionTokens: 30, TotalTokens: 330, Cost: 0.75,
		StatusCode: 200, PricingRevision: 7,
	})
	for _, sample := range []struct {
		at                         time.Time
		balance, granted, toppedUp float64
	}{
		{balanceAt, 20, 5, 15},
		{balanceAt.Add(time.Minute), 18, 5, 13},
	} {
		if _, err := source.Exec(`INSERT INTO balance_sample (timestamp, balance, granted, topped_up)
			VALUES (?, ?, ?, ?)`, unixFixtureTime(sample.at), sample.balance, sample.granted, sample.toppedUp); err != nil {
			t.Fatalf("insert source balance sample: %v", err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	result, err := ImportDatabase(ctx, targetPath, sourcePath, importTestOptions(t, ruleText)...)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if result.Copied || result.SourceRows != 3 || result.Inserted != 2 || result.Skipped != 1 {
		t.Fatalf("unexpected merge result: %+v", result)
	}
	if result.Revisions != 0 || result.BalanceSamples != 1 {
		t.Fatalf("unexpected merge revision/sample counts: %+v", result)
	}
	second, err := ImportDatabase(ctx, targetPath, sourcePath, importTestOptions(t, ruleText)...)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Inserted != 0 || second.Skipped != 3 || second.Revisions != 0 || second.BalanceSamples != 0 {
		t.Fatalf("second import should be a no-op: %+v", second)
	}

	target = openTestStore(t, targetPath, ruleText)
	var count int
	if err := target.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_log`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("merged usage rows = %d, want 3", count)
	}
	var overlapUUID string
	var overlapCost float64
	if err := target.db.QueryRowContext(ctx, `SELECT uuid, cost FROM usage_log WHERE timestamp = ? AND prompt_tokens = ?`,
		unixFixtureTime(overlapAt), overlap.PromptTokens).Scan(&overlapUUID, &overlapCost); err != nil {
		t.Fatal(err)
	}
	if overlapUUID != overlap.UUID || overlapCost == 99 {
		t.Fatalf("overlap was overwritten: uuid=%q cost=%v", overlapUUID, overlapCost)
	}
	var newUUID, newClient string
	var newRevision int64
	if err := target.db.QueryRowContext(ctx, `
		SELECT uuid, client, pricing_revision FROM usage_log
		WHERE endpoint = '/v1/chat/completions' AND prompt_tokens = 500`).
		Scan(&newUUID, &newClient, &newRevision); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(newUUID, "import-") || newClient != "opencode" || newRevision != targetRevision {
		t.Fatalf("new row uuid=%q client=%q revision=%d", newUUID, newClient, newRevision)
	}
	var inferredClient string
	if err := target.db.QueryRowContext(ctx, `SELECT client FROM usage_log WHERE uuid = 'responses-row'`).Scan(&inferredClient); err != nil {
		t.Fatal(err)
	}
	if inferredClient != "codex" {
		t.Fatalf("legacy row client = %q, want endpoint inference", inferredClient)
	}
	if err := target.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM balance_sample`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("merged balance samples = %d, want 2", count)
	}
}

func TestImportDatabaseImportsNewPricingRevision(t *testing.T) {
	ctx := context.Background()
	targetRules := rules(1, 0.1, 2, "deepseek-flash")
	sourceRules := rules(3, 0.3, 4, "deepseek-flash")
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "mac-usage.db")
	targetPath := filepath.Join(dir, "usage.db")
	base := testNow.Add(-5 * time.Hour)

	target := openTestStore(t, targetPath, targetRules)
	originalRevisions, err := target.PricingRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	source := openImportFixture(t, sourcePath, false)
	if _, err := source.Exec(`
		INSERT INTO pricing_revision (id, valid_from, fingerprint, source, text, imported_at)
		VALUES (3, ?, ?, 'remote', ?, ?)`,
		unixFixtureTime(base.AddDate(0, 0, -10)), pricing.Fingerprint(sourceRules), sourceRules,
		unixFixtureTime(base)); err != nil {
		t.Fatalf("insert source revision: %v", err)
	}
	insertFixtureUsage(t, source, fixtureUsage{
		UUID: "new-revision-row", Timestamp: unixFixtureTime(base), ProviderID: "deepseek",
		Model: "deepseek-flash", Endpoint: "/v1/chat/completions", PromptTokens: 1000,
		TotalTokens: 1000, Cost: 7.75, StatusCode: 200, PricingRevision: 3,
	})
	if err := source.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	result, err := ImportDatabase(ctx, targetPath, sourcePath, importTestOptions(t, targetRules)...)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Inserted != 1 || result.Skipped != 0 || result.Revisions != 1 {
		t.Fatalf("unexpected revision import result: %+v", result)
	}

	target = openTestStore(t, targetPath, targetRules)
	revisions, err := target.PricingRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != len(originalRevisions)+1 {
		t.Fatalf("target revisions = %d, want %d", len(revisions), len(originalRevisions)+1)
	}
	var importedRevision pricing.Revision
	for _, revision := range revisions {
		if revision.Fingerprint == pricing.Fingerprint(sourceRules) {
			importedRevision = revision
		}
	}
	if importedRevision.Fingerprint != pricing.Fingerprint(sourceRules) ||
		importedRevision.Source != pricing.SourceRemote || importedRevision.ID == 0 {
		t.Fatalf("imported revision = %+v", importedRevision)
	}
	var revision int64
	var cost float64
	if err := target.db.QueryRowContext(ctx, `
		SELECT pricing_revision, cost FROM usage_log WHERE uuid = 'new-revision-row'`).
		Scan(&revision, &cost); err != nil {
		t.Fatal(err)
	}
	if revision != importedRevision.ID || cost != 7.75 {
		t.Fatalf("imported row revision=%d cost=%v", revision, cost)
	}
	second, err := ImportDatabase(ctx, targetPath, sourcePath, importTestOptions(t, targetRules)...)
	if err != nil {
		t.Fatal(err)
	}
	if second.Inserted != 0 || second.Skipped != 1 || second.Revisions != 0 {
		t.Fatalf("second revision import should be a no-op: %+v", second)
	}
}

func TestImportDatabaseIncludesSourceWAL(t *testing.T) {
	ctx := context.Background()
	ruleText := rules(1, 0.1, 2, "deepseek-flash")
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "mac-usage.db")
	targetPath := filepath.Join(dir, "usage.db")
	base := testNow.Add(-time.Hour)

	source := openImportFixture(t, sourcePath, false)
	if _, err := source.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatalf("disable autocheckpoint: %v", err)
	}
	insertFixtureUsage(t, source, fixtureUsage{
		UUID: "wal-row", Timestamp: unixFixtureTime(base), ProviderID: "deepseek",
		Model: "deepseek-flash", Endpoint: "/v1/chat/completions", PromptTokens: 10,
		CompletionTokens: 1, TotalTokens: 11, Cost: 0.01, StatusCode: 200,
	})
	if _, err := os.Stat(sourcePath + "-wal"); err != nil {
		t.Fatalf("expected an active WAL file: %v", err)
	}
	defer source.Close()

	result, err := ImportDatabase(ctx, targetPath, sourcePath, importTestOptions(t, ruleText)...)
	if err != nil {
		t.Fatalf("import while WAL is active: %v", err)
	}
	if !result.Copied || result.SourceRows != 1 || result.Inserted != 1 {
		t.Fatalf("unexpected WAL import result: %+v", result)
	}
	target := openTestStore(t, targetPath, ruleText)
	var count int
	if err := target.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_log WHERE uuid = 'wal-row'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("WAL row count = %d, want 1", count)
	}
}

func TestImportDatabaseRejectsInvalidSources(t *testing.T) {
	ctx := context.Background()
	ruleText := rules(1, 0.1, 2, "deepseek-flash")

	tests := []struct {
		name        string
		setup       func(t *testing.T, dir string) (target, source string)
		wantMessage string
	}{
		{
			name: "missing file",
			setup: func(t *testing.T, dir string) (string, string) {
				return filepath.Join(dir, "target.db"), filepath.Join(dir, "missing.db")
			},
			wantMessage: "stat source database",
		},
		{
			name: "no usage table",
			setup: func(t *testing.T, dir string) (string, string) {
				source := filepath.Join(dir, "empty.db")
				db, err := sql.Open("sqlite", sqliteDSN(source))
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Ping(); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(dir, "target.db"), source
			},
			wantMessage: "no usage_log",
		},
		{
			name: "missing required column",
			setup: func(t *testing.T, dir string) (string, string) {
				source := filepath.Join(dir, "incomplete.db")
				db, err := sql.Open("sqlite", sqliteDSN(source))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE usage_log (
					timestamp REAL, model TEXT, prompt_tokens INTEGER, completion_tokens INTEGER)`); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(dir, "target.db"), source
			},
			wantMessage: `missing column "endpoint"`,
		},
		{
			name: "same source and target",
			setup: func(t *testing.T, dir string) (string, string) {
				source := filepath.Join(dir, "same.db")
				db := openImportFixture(t, source, false)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				return source, source
			},
			wantMessage: "same file",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, source := test.setup(t, t.TempDir())
			_, err := ImportDatabase(ctx, target, source, importTestOptions(t, ruleText)...)
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("ImportDatabase() error = %v, want substring %q", err, test.wantMessage)
			}
		})
	}
}

func TestImportTagStable(t *testing.T) {
	first := importTag("/tmp/a/usage.db", "/tmp/mac/usage.db")
	second := importTag("/tmp/a/usage.db", "/tmp/mac/usage.db")
	other := importTag("/tmp/b/usage.db", "/tmp/mac/usage.db")
	if first != second || first == other || len(first) != 8 {
		t.Fatalf("import tags: first=%q second=%q other=%q", first, second, other)
	}
}
