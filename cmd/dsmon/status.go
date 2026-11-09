package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"dsmon/internal/config"
	"dsmon/internal/ipc"
)

type statusSnapshot struct {
	Online               bool    `json:"online"`
	ProxyRunning         bool    `json:"proxy_running"`
	Health               bool    `json:"health"`
	BalanceError         bool    `json:"balance_error"`
	BalanceLow           bool    `json:"balance_low"`
	BalanceHas           bool    `json:"balance_has"`
	Balance              float64 `json:"balance"`
	RequestsToday        int     `json:"requests_today"`
	CostToday            float64 `json:"cost_today"`
	LastActivityUnixNano int64   `json:"last_activity_unix_nano,omitempty"`
	ServerNowUnixNano    int64   `json:"server_now_unix_nano,omitempty"`
	LastActivityUnixMS   int64   `json:"last_activity_unix_ms,omitempty"`
	ServerNowUnixMS      int64   `json:"server_now_unix_ms,omitempty"`
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "help", "--help", "-h":
			statusUsage(stdout)
			return 0
		case "--json":
			jsonOutput = true
		default:
			fmt.Fprintf(stderr, "dsmon status: unknown option %q\n\n", arg)
			statusUsage(stderr)
			return 2
		}
	}

	client := ipc.NewClient(config.RuntimeSocketPath())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stats, statsErr := client.Stats(ctx)
	trace, traceErr := client.Trace(ctx, 20)
	snapshot := offlineStatusSnapshot()
	var err error
	switch {
	case statsErr != nil:
		err = statsErr
	case traceErr != nil:
		err = traceErr
	default:
		snapshot = snapshotFromIPC(stats, trace)
	}

	if jsonOutput {
		if err := writeStatusJSON(stdout, snapshot); err != nil {
			fmt.Fprintf(stderr, "dsmon status: %v\n", err)
			return 1
		}
		return 0
	}
	if err != nil {
		fmt.Fprintf(stdout, "DS-mon offline\n")
		return 0
	}
	fmt.Fprintf(stdout, "DS-mon online: %d requests today, cost %.6f",
		snapshot.RequestsToday, snapshot.CostToday)
	if snapshot.BalanceHas {
		fmt.Fprintf(stdout, ", balance %.2f", snapshot.Balance)
	}
	fmt.Fprintln(stdout)
	return 0
}

func snapshotFromIPC(stats ipc.Stats, trace ipc.Trace) statusSnapshot {
	lastActivityMS := int64(0)
	if trace.LastActivityUnixNano > 0 {
		lastActivityMS = trace.LastActivityUnixNano / int64(time.Millisecond)
	}
	serverNowMS := int64(0)
	if trace.NowUnixNano > 0 {
		serverNowMS = trace.NowUnixNano / int64(time.Millisecond)
	}
	return statusSnapshot{
		Online:       true,
		ProxyRunning: stats.ProxyRunning,
		Health:       stats.Health,
		BalanceError: strings.TrimSpace(stats.Balance.Error) != "",
		BalanceLow: stats.BalanceLowThreshold > 0 && stats.Balance.Has &&
			stats.Balance.Total < stats.BalanceLowThreshold,
		BalanceHas:           stats.Balance.Has,
		Balance:              stats.Balance.Total,
		RequestsToday:        stats.Today.Requests,
		CostToday:            stats.Today.Cost,
		LastActivityUnixNano: trace.LastActivityUnixNano,
		ServerNowUnixNano:    trace.NowUnixNano,
		LastActivityUnixMS:   lastActivityMS,
		ServerNowUnixMS:      serverNowMS,
	}
}

func offlineStatusSnapshot() statusSnapshot {
	return statusSnapshot{}
}

func writeStatusJSON(writer io.Writer, snapshot statusSnapshot) error {
	encoder := json.NewEncoder(writer)
	return encoder.Encode(snapshot)
}

func statusUsage(writer io.Writer) {
	fmt.Fprint(writer, `usage: dsmon status [--json]

Print the daemon status and the small snapshot used by the Omarchy bar widget.
--json never includes API keys, authorization headers, request bodies, or raw
upstream error text.
`)
}
