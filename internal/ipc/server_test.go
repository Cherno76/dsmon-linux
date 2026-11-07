package ipc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dsmon/internal/balance"
	"dsmon/internal/proxy/trace"
	"dsmon/internal/store"
)

type fakeBalanceSource struct{ state balance.State }

func (f fakeBalanceSource) Latest() balance.State { return f.state }

type fakeTraceSource struct{}

func (fakeTraceSource) ActivityBars(time.Time) []int {
	values := make([]int, trace.DefaultActivityBucketCount)
	values[len(values)-1] = 2
	return values
}

func (fakeTraceSource) TrafficFrame(now time.Time, width int) trace.Frame {
	buckets := make([]trace.Bucket, trace.DefaultTrafficBucketCount)
	buckets[len(buckets)-1] = trace.Bucket{Upload: 512, Download: 4096}
	return trace.MakeFrame(buckets, trace.DefaultTrafficBucketSeconds, now, float64(width*2))
}

func TestServerAndClientRoundTrip(t *testing.T) {
	now := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.Local)
	database, err := store.Open(filepath.Join(t.TempDir(), "usage.db"),
		store.WithClock(func() time.Time { return now }),
		store.WithLocation(time.Local),
		store.WithRetentionDays(0))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for index := 0; index < 2; index++ {
		record := store.UsageRecord{
			UUID:             "ipc-row-" + string(rune('a'+index)),
			Timestamp:        now.Add(-time.Duration(30+index*10) * time.Minute),
			ProviderID:       "deepseek",
			Model:            "deepseek-chat",
			Endpoint:         "/v1/chat/completions",
			PromptTokens:     1000,
			CompletionTokens: 100,
			TotalTokens:      1100,
			CachedTokens:     400,
			LatencyMS:        120,
			StatusCode:       200,
			UserAgent:        "opencode/1.18.25",
		}
		if err := database.Insert(context.Background(), record); err != nil {
			t.Fatalf("insert usage: %v", err)
		}
	}
	for _, sample := range []store.BalanceSample{
		{Timestamp: now.Add(-90 * time.Minute), Balance: 30},
		{Timestamp: now.Add(-30 * time.Minute), Balance: 25},
	} {
		if err := database.RecordBalanceSample(context.Background(), sample); err != nil {
			t.Fatalf("record balance: %v", err)
		}
	}

	socket := filepath.Join(t.TempDir(), "dsmon.sock")
	reloaded := make(chan struct{}, 1)
	server, err := NewServer(Config{
		Path:       socket,
		Store:      database,
		Balance:    fakeBalanceSource{state: balance.State{HasBalance: true, Balance: balance.Snapshot{Total: 25}}},
		Trace:      fakeTraceSource{},
		ListenAddr: "127.0.0.1:18080",
		Settings: func() LiveSettings {
			return LiveSettings{ListenAddr: "127.0.0.1:18081", BalanceLowThreshold: 10}
		},
		Reload: func(context.Context) error {
			reloaded <- struct{}{}
			return nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe(ctx) }()
	defer func() {
		cancel()
		select {
		case serveErr := <-done:
			if serveErr != nil {
				t.Errorf("ipc serve: %v", serveErr)
			}
		case <-time.After(2 * time.Second):
			t.Error("ipc server did not stop")
		}
		if err := database.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	waitForSocket(t, socket)
	client := NewClient(socket)
	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()

	stats, err := client.Stats(callCtx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !stats.ProxyRunning || stats.ListenAddr != "127.0.0.1:18081" ||
		stats.BalanceLowThreshold != 10 || stats.Today.Requests != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if !stats.Balance.Has || stats.Balance.Total != 25 {
		t.Fatalf("balance = %+v", stats.Balance)
	}
	if len(stats.Balance.Trend24h) == 0 || len(stats.Balance.Trend7d) == 0 {
		t.Fatalf("balance trends = %+v / %+v", stats.Balance.Trend24h, stats.Balance.Trend7d)
	}

	page, err := client.Requests(callCtx, 1, 0)
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	if page.Total != 2 || page.Offset != 0 || len(page.Rows) != 1 {
		t.Fatalf("request page = %+v", page)
	}
	if page.Rows[0].Client != "opencode" || page.Rows[0].TotalTokens != 1100 {
		t.Fatalf("request row = %+v", page.Rows[0])
	}
	second, err := client.Requests(callCtx, 1, 1)
	if err != nil || second.Offset != 1 || len(second.Rows) != 1 {
		t.Fatalf("second request page = %+v, err = %v", second, err)
	}

	series, err := client.Series(callCtx, string(store.PeriodToday), string(store.DimensionModel), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if series.Bucket != string(store.BucketHour) || len(series.Points) != 24 {
		t.Fatalf("series = %+v", series)
	}
	if series.FromUnix != time.Date(2026, time.November, 7, 0, 0, 0, 0, time.Local).Unix() ||
		series.ToUnix != time.Date(2026, time.November, 8, 0, 0, 0, 0, time.Local).Unix() {
		t.Fatalf("series domain = %d..%d", series.FromUnix, series.ToUnix)
	}
	if series.Points[11].Requests != 2 || series.Points[23].Requests != 0 {
		t.Fatalf("series points were not filled with future zeros: %+v", series.Points)
	}

	pricing, err := client.Pricing(callCtx)
	if err != nil {
		t.Fatalf("pricing: %v", err)
	}
	if pricing.Count < 1 || pricing.LatestSource == "" {
		t.Fatalf("pricing = %+v", pricing)
	}

	frame, err := client.Trace(callCtx, 40)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	if len(frame.Activity) != trace.DefaultActivityBucketCount || len(frame.Traffic) != trace.DefaultTrafficBucketCount {
		t.Fatalf("trace size = %d/%d", len(frame.Activity), len(frame.Traffic))
	}
	if frame.TrafficBucketSecs != trace.DefaultTrafficBucketSeconds.Seconds() {
		t.Fatalf("traffic bucket seconds = %v", frame.TrafficBucketSecs)
	}
	if frame.DownRate <= 0 || frame.UpRate <= 0 || len(frame.Grid) == 0 {
		t.Fatalf("trace frame = %+v", frame)
	}
	if _, err := client.Do(callCtx, Request{Cmd: "unknown"}); err == nil {
		t.Fatal("unknown command succeeded")
	}
	if err := client.Reload(callCtx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("reload callback was not called")
	}
}

func TestServerReloadFailureAndMissingCallback(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "usage.db"), store.WithRetentionDays(0))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	base := Config{
		Path:    filepath.Join(t.TempDir(), "dsmon.sock"),
		Store:   database,
		Balance: fakeBalanceSource{},
		Trace:   fakeTraceSource{},
	}
	withoutReload, err := NewServer(base)
	if err != nil {
		t.Fatal(err)
	}
	response := withoutReload.handle(context.Background(), Request{V: Version, Cmd: "reload"})
	if response.OK || !strings.Contains(response.Error, "does not support reload") {
		t.Fatalf("missing reload response = %+v", response)
	}

	base.Reload = func(context.Context) error { return errors.New("端口被占用") }
	withFailure, err := NewServer(base)
	if err != nil {
		t.Fatal(err)
	}
	response = withFailure.handle(context.Background(), Request{V: Version, Cmd: "reload"})
	if response.OK || response.Error != "端口被占用" {
		t.Fatalf("failed reload response = %+v", response)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ipc socket %s was not created", path)
}
