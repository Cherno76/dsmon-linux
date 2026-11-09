package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dsmon/internal/config"
	"dsmon/internal/store"
	"dsmon/internal/version"
)

func createCLIImportSource(t *testing.T, path string) {
	t.Helper()
	source, err := store.Open(path)
	if err != nil {
		t.Fatalf("open import source: %v", err)
	}
	record := store.UsageRecord{
		UUID:             "cli-import-row",
		Timestamp:        time.Now().Add(-time.Hour),
		ProviderID:       "deepseek",
		Model:            "deepseek-flash",
		Endpoint:         "/v1/chat/completions",
		PromptTokens:     1_000,
		CompletionTokens: 100,
		TotalTokens:      1_100,
		StatusCode:       200,
		UserAgent:        "opencode/1.18.25",
	}
	if err := source.Insert(context.Background(), record); err != nil {
		_ = source.Close()
		t.Fatalf("insert import row: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close import source: %v", err)
	}
}

func TestRunCommandValidation(t *testing.T) {
	t.Setenv("DSMON_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOutput string
		wantError  string
	}{
		{name: "no arguments", args: nil, wantCode: 2, wantError: "usage:"},
		{name: "version", args: []string{"version"}, wantCode: 0, wantOutput: "dsmon " + version.Current},
		{name: "help", args: []string{"help"}, wantCode: 0, wantOutput: "dsmon import"},
		{name: "unknown command", args: []string{"wat"}, wantCode: 2, wantError: `unknown command "wat"`},
		{name: "tui help", args: []string{"tui", "--help"}, wantCode: 0, wantOutput: "usage: dsmon tui"},
		{name: "settings help", args: []string{"settings", "--help"}, wantCode: 0, wantOutput: "usage: dsmon settings"},
		{name: "settings unknown option", args: []string{"settings", "--bad"}, wantCode: 2, wantError: "unknown option"},
		{name: "status help", args: []string{"status", "--help"}, wantCode: 0, wantOutput: "usage: dsmon status"},
		{name: "serve help", args: []string{"serve", "--help"}, wantCode: 0, wantOutput: "dsmon serve"},
		{name: "serve missing key", args: []string{"serve"}, wantCode: 1, wantError: "API key is not set"},
		{name: "import without source", args: []string{"import"}, wantCode: 2, wantError: "missing source database"},
		{name: "import two sources", args: []string{"import", "a.db", "b.db"}, wantCode: 2, wantError: "exactly one source"},
		{name: "import into without path", args: []string{"import", "--into"}, wantCode: 2, wantError: "--into requires"},
		{name: "import unknown option", args: []string{"import", "--bad", "a.db"}, wantCode: 2, wantError: "unknown option"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr)
			if code != test.wantCode {
				t.Fatalf("exit code = %d, want %d; stdout=%q stderr=%q", code, test.wantCode, stdout.String(), stderr.String())
			}
			if test.wantOutput != "" && !strings.Contains(stdout.String(), test.wantOutput) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), test.wantOutput)
			}
			if test.wantError != "" && !strings.Contains(stderr.String(), test.wantError) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), test.wantError)
			}
		})
	}
}

func TestRunImportCopiesAndMergesDefaultTarget(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	sourcePath := filepath.Join(root, "mac-usage.db")
	createCLIImportSource(t, sourcePath)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"import", sourcePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("copy import exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "copied 1 rows") {
		t.Fatalf("copy import output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"import", sourcePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("merge import exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "inserted 0, skipped 1") {
		t.Fatalf("merge import output = %q", stdout.String())
	}

	target, err := store.Open(config.DatabasePath())
	if err != nil {
		t.Fatalf("open imported target: %v", err)
	}
	defer target.Close()
	rows, err := target.RecentRecords(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "cli-import-row" {
		t.Fatalf("imported rows = %+v", rows)
	}
}

func TestRunImportIntoExplicitTarget(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	sourcePath := filepath.Join(root, "mac-usage.db")
	targetPath := filepath.Join(root, "explicit", "usage.db")
	createCLIImportSource(t, sourcePath)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"import", "--into", targetPath, sourcePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("import exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), targetPath) || !strings.Contains(stdout.String(), "copied 1 rows") {
		t.Fatalf("import output = %q", stdout.String())
	}
}

func TestRunImportReportsOperationalError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "missing.db")
	if code := run([]string{"import", missing}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "dsmon import:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
