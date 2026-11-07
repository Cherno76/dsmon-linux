package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"dsmon/internal/config"
	"dsmon/internal/store"
)

func TestServeRuntimeReloadSameAndChangedPort(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("DSMON_API_KEY", "runtime-test-key")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	for _, name := range []string{
		"DSMON_LISTEN_ADDR", "DSMON_OPENAI_BASE", "DSMON_UPSTREAM_BASE",
		"DSMON_ANTHROPIC_BASE", "DSMON_TOOLS_BASE", "DSMON_BALANCE_URL", "DSMON_RPM_LIMIT",
	} {
		t.Setenv(name, "")
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	balanceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"is_available":true,"balance_infos":[{"total_balance":"10","granted_balance":"0","topped_up_balance":"10"}]}`)
	}))
	defer balanceServer.Close()

	database, err := store.Open(filepath.Join(root, "usage.db"), store.WithRetentionDays(0))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	portA := freePort(t)
	settings := testRuntimeSettings(portA, upstream.URL, balanceServer.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := log.New(io.Discard, "", 0)
	runtime, err := newServeRuntime(ctx, database, settings, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	waitForHTTPStatus(t, portA, http.StatusNotFound)

	if err := config.SaveProxySettings(settings, ""); err != nil {
		t.Fatal(err)
	}
	oldServer := runtime.current().server
	settings.RPMLimit = 5
	settings.BalanceLowThreshold = 9.5
	if err := config.SaveProxySettings(settings, ""); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Reload(context.Background()); err != nil {
		t.Fatalf("same-port reload: %v", err)
	}
	if runtime.current().server != oldServer {
		t.Fatal("same-port reload replaced the HTTP listener")
	}
	if live := runtime.Settings(); live.ListenAddr != settings.ListenAddr || live.BalanceLowThreshold != 9.5 {
		t.Fatalf("same-port live settings = %+v", live)
	}
	waitForHTTPStatus(t, portA, http.StatusNotFound)

	portB := freePort(t)
	settings.ListenAddr = "127.0.0.1:" + strconv.Itoa(portB)
	if err := config.SaveProxySettings(settings, ""); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Reload(context.Background()); err != nil {
		t.Fatalf("port-change reload: %v", err)
	}
	if runtime.current().server == oldServer {
		t.Fatal("port-change reload reused the old proxy server")
	}
	if live := runtime.Settings(); live.ListenAddr != settings.ListenAddr {
		t.Fatalf("changed-port live settings = %+v", live)
	}
	waitForHTTPStatus(t, portB, http.StatusNotFound)
	waitForPortClosed(t, portA)

	settings.ListenAddr = "0.0.0.0:" + strconv.Itoa(portB)
	if err := config.SaveProxySettings(settings, ""); err == nil {
		t.Fatal("invalid listen address was saved")
	}
	if live := runtime.Settings(); live.ListenAddr != "127.0.0.1:"+strconv.Itoa(portB) {
		t.Fatalf("failed save changed the daemon: %+v", live)
	}
}

func testRuntimeSettings(port int, upstream, balanceURL string) config.ProxySettings {
	return config.ProxySettings{
		ListenAddr:          "127.0.0.1:" + strconv.Itoa(port),
		OpenAIBase:          upstream,
		AnthropicBase:       upstream,
		ToolsBase:           upstream,
		APIPath:             "/v1",
		ProviderID:          "deepseek",
		AuthPrefix:          "Bearer",
		RPMLimit:            0,
		BalanceURL:          balanceURL,
		BalanceLowThreshold: 0,
		RetentionDays:       0,
		PricingSourceURL:    config.DefaultPricingSource,
		PricingAutoCheck:    true,
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitForHTTPStatus(t *testing.T, port, want int) {
	t.Helper()
	client := &http.Client{Timeout: 300 * time.Millisecond}
	address := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(address)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not return status %d", address, want)
}

func waitForPortClosed(t *testing.T, port int) {
	t.Helper()
	address := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s is still accepting connections", address)
}
