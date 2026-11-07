package balance

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 60 * time.Second},
		{failures: 1, want: 60 * time.Second},
		{failures: 2, want: 120 * time.Second},
		{failures: 3, want: 240 * time.Second},
		{failures: 4, want: 300 * time.Second},
		{failures: 9, want: 300 * time.Second},
	}
	for _, test := range tests {
		if got := BackoffDelay(test.failures, 60*time.Second, 5*time.Minute); got != test.want {
			t.Fatalf("BackoffDelay(%d) = %s, want %s", test.failures, got, test.want)
		}
	}
}

type fakeRefresher struct {
	mu        sync.Mutex
	calls     int
	failUntil int
}

func (f *fakeRefresher) Refresh(context.Context) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failUntil {
		return Snapshot{}, &QueryError{Reason: ReasonNetwork, Detail: "temporary"}
	}
	return Snapshot{Total: 12.34, Granted: 2, ToppedUp: 10.34}, nil
}

func (f *fakeRefresher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestPollerRetriesWithBackoffAndRecovers(t *testing.T) {
	refresher := &fakeRefresher{failUntil: 2}
	poller, err := NewPoller(refresher, PollerConfig{
		Interval:   10 * time.Millisecond,
		MaxBackoff: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		state := poller.Latest()
		if state.HasBalance && state.Err == nil && state.Failures == 0 && refresher.Calls() >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("poller did not recover: state=%+v calls=%d", state, refresher.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop after cancellation")
	}
	state := poller.Latest()
	if state.Balance.Total != 12.34 || state.LastSuccess.IsZero() || state.NextRefreshIn != 10*time.Millisecond {
		t.Fatalf("final state = %+v", state)
	}
}

func TestPollerStopsOnCanceledContext(t *testing.T) {
	poller, _ := NewPoller(&fakeRefresher{}, PollerConfig{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller ignored a canceled context")
	}
	if !poller.Latest().LastAttempt.IsZero() {
		t.Fatal("canceled poller made a request")
	}
}

type storageFailureRefresher struct {
	mu    sync.Mutex
	calls int
}

func (f *storageFailureRefresher) Refresh(context.Context) (Snapshot, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return Snapshot{Total: 7.5, Granted: 1, ToppedUp: 6.5},
		&QueryError{Reason: ReasonStorage, Detail: "disk full"}
}

func (f *storageFailureRefresher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestPollerKeepsFetchedBalanceWhenOnlyStorageFails(t *testing.T) {
	refresher := &storageFailureRefresher{}
	poller, err := NewPoller(refresher, PollerConfig{
		Interval:   10 * time.Millisecond,
		MaxBackoff: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		state := poller.Latest()
		if state.HasBalance && state.Balance.Total == 7.5 {
			if !isStorageError(state.Err) || state.Failures == 0 || state.LastSuccess.IsZero() {
				cancel()
				t.Fatalf("state = %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("poller lost fetched balance: state=%+v calls=%d", state, refresher.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop after cancellation")
	}
}

func TestNewPollerRejectsMissingRefresher(t *testing.T) {
	if _, err := NewPoller(nil, PollerConfig{}); err == nil {
		t.Fatal("nil refresher accepted")
	}
}
