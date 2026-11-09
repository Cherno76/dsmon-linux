package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dsmon/internal/ipc"
)

func TestStatusSnapshotCarriesBarWidgetFields(t *testing.T) {
	now := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.Local)
	stats := ipc.Stats{
		ProxyRunning:        true,
		Health:              true,
		BalanceLowThreshold: 10,
		Balance: ipc.Balance{
			Has:   true,
			Total: 22.22,
			Error: "",
		},
		Today: ipc.Aggregate{
			Requests: 7,
			Cost:     0.42,
		},
	}
	trace := ipc.Trace{
		NowUnixNano:          now.UnixNano(),
		LastActivityUnixNano: now.Add(-3 * time.Second).UnixNano(),
	}

	snapshot := snapshotFromIPC(stats, trace)
	if !snapshot.Online || !snapshot.Health || snapshot.BalanceLow || !snapshot.BalanceHas ||
		snapshot.Balance != 22.22 || snapshot.RequestsToday != 7 ||
		snapshot.CostToday != 0.42 ||
		snapshot.LastActivityUnixNano != trace.LastActivityUnixNano ||
		snapshot.ServerNowUnixNano != trace.NowUnixNano ||
		snapshot.LastActivityUnixMS != trace.LastActivityUnixNano/int64(time.Millisecond) ||
		snapshot.ServerNowUnixMS != trace.NowUnixNano/int64(time.Millisecond) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestStatusSnapshotMarksLowBalance(t *testing.T) {
	snapshot := snapshotFromIPC(ipc.Stats{
		ProxyRunning:        true,
		Health:              true,
		BalanceLowThreshold: 50,
		Balance: ipc.Balance{
			Has:   true,
			Total: 48.8,
		},
	}, ipc.Trace{})
	if !snapshot.BalanceLow {
		t.Fatalf("snapshot did not mark low balance: %+v", snapshot)
	}
}

func TestStatusOfflineSnapshotDoesNotLeakErrorText(t *testing.T) {
	snapshot := offlineStatusSnapshot()
	if snapshot.Online || snapshot.ProxyRunning {
		t.Fatalf("offline snapshot = %+v", snapshot)
	}
	var output bytes.Buffer
	if err := writeStatusJSON(&output, snapshot); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "socket") || strings.Contains(output.String(), "dial") {
		t.Fatalf("offline JSON leaked daemon error text: %q", output.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("offline JSON is invalid: %v", err)
	}
	if online, _ := decoded["online"].(bool); online {
		t.Fatalf("offline JSON = %q", output.String())
	}
}

func TestStatusRejectsUnknownOptions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runStatus([]string{"--watch"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown option") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
