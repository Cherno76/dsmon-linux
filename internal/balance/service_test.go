package balance

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"dsmon/internal/pricing"
	"dsmon/internal/store"
)

var balanceTestNow = time.Date(2026, 11, 7, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60))

func openBalanceTestStore(t *testing.T) *store.Store {
	t.Helper()
	text := `{"deepseek":{"models":{"deepseek-chat":{"offpeak":{"miss":1,"hit":0.1,"out":2}}}}}`
	strategy, err := pricing.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(t.TempDir(), "usage.db"),
		store.WithClock(func() time.Time { return balanceTestNow }),
		store.WithLocation(balanceTestNow.Location()),
		store.WithPricing(pricing.Loaded{
			Strategy: strategy, Source: pricing.SourceBundled,
			Text: text, Fingerprint: pricing.Fingerprint(text),
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestServiceRecordsSuccessfulBalanceSample(t *testing.T) {
	database := openBalanceTestStore(t)
	client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return responseWith(200, `{"is_available":true,"balance_infos":[{"total_balance":"12.34","granted_balance":"2.00","topped_up_balance":"10.34"}]}`), nil
	}), "sk-test-only")
	service, err := NewService(client, database, func() time.Time { return balanceTestNow })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Refresh(context.Background())
	if err != nil || snapshot.Total != 12.34 {
		t.Fatalf("Refresh() = %+v, %v", snapshot, err)
	}
	samples, err := database.BalanceSamples(context.Background(), balanceTestNow.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].Balance != 12.34 || samples[0].Granted != 2 ||
		samples[0].ToppedUp != 10.34 || !samples[0].Timestamp.Equal(balanceTestNow) {
		t.Fatalf("samples = %+v", samples)
	}
}

func TestServiceDoesNotRecordFailedQueries(t *testing.T) {
	database := openBalanceTestStore(t)
	client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return responseWith(200, `not json`), nil
	}), "sk-test-only")
	service, _ := NewService(client, database, func() time.Time { return balanceTestNow })
	if _, err := service.Refresh(context.Background()); err == nil {
		t.Fatal("parse failure was accepted")
	}
	samples, err := database.BalanceSamples(context.Background(), balanceTestNow.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Fatalf("failed query recorded samples: %+v", samples)
	}
}

func TestServiceReportsStorageFailureButReturnsSnapshot(t *testing.T) {
	client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return responseWith(200, `{"is_available":true,"balance_infos":[{"total_balance":"5"}]}`), nil
	}), "sk-test-only")
	service, _ := NewService(client, failingSampleStore{}, nil)
	snapshot, err := service.Refresh(context.Background())
	if snapshot.Total != 5 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	assertReason(t, err, ReasonStorage)
}

type failingSampleStore struct{}

func (failingSampleStore) RecordBalanceSample(context.Context, store.BalanceSample) error {
	return errors.New("disk full")
}

func TestServiceRequiresDependencies(t *testing.T) {
	client := testClient(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return responseWith(200, ""), nil
	}), "key")
	if _, err := NewService(nil, failingSampleStore{}, nil); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err := NewService(client, nil, nil); err == nil {
		t.Fatal("nil store accepted")
	}
}
