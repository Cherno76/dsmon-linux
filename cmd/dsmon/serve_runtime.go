package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"dsmon/internal/balance"
	"dsmon/internal/config"
	"dsmon/internal/ipc"
	"dsmon/internal/proxy"
	"dsmon/internal/proxy/trace"
	"dsmon/internal/store"
)

type serveGeneration struct {
	settings config.ProxySettings
	server   *proxy.Server
	listener net.Listener
	retiring atomic.Bool

	proxyCancel context.CancelFunc
	proxyDone   chan error

	poller       *balance.Poller
	pollerCancel context.CancelFunc
	pollerDone   chan struct{}
}

type serveRuntime struct {
	parent context.Context
	store  *store.Store
	logger *log.Logger

	reloadMu  sync.Mutex
	mu        sync.RWMutex
	gen       *serveGeneration
	fatal     chan error
	closing   bool
	closeOnce sync.Once
}

func newServeRuntime(parent context.Context, database *store.Store, settings config.ProxySettings,
	logger *log.Logger) (*serveRuntime, error) {
	if database == nil {
		return nil, errors.New("serve runtime requires a store")
	}
	if parent == nil {
		parent = context.Background()
	}
	runtime := &serveRuntime{
		parent: parent,
		store:  database,
		logger: logger,
		fatal:  make(chan error, 4),
	}
	generation, err := runtime.startGeneration(settings)
	if err != nil {
		return nil, err
	}
	runtime.gen = generation
	return runtime, nil
}

func (r *serveRuntime) startGeneration(settings config.ProxySettings) (*serveGeneration, error) {
	if r.parent.Err() != nil {
		return nil, r.parent.Err()
	}
	server, err := proxy.NewServer(proxyConfig(settings, r.store, r.logger))
	if err != nil {
		return nil, fmt.Errorf("create proxy: %w", err)
	}
	listener, err := server.Listen()
	if err != nil {
		return nil, err
	}
	poller, err := newBalancePoller(settings, r.store, r.logger)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	proxyCtx, proxyCancel := context.WithCancel(r.parent)
	pollerCtx, pollerCancel := context.WithCancel(r.parent)
	generation := &serveGeneration{
		settings:     settings,
		server:       server,
		listener:     listener,
		proxyCancel:  proxyCancel,
		proxyDone:    make(chan error, 1),
		poller:       poller,
		pollerCancel: pollerCancel,
		pollerDone:   make(chan struct{}),
	}
	go func() {
		err := server.Serve(proxyCtx, listener)
		generation.proxyDone <- err
		if err != nil && !generation.retiring.Load() && r.parent.Err() == nil {
			select {
			case r.fatal <- fmt.Errorf("proxy server stopped: %w", err):
			default:
			}
		}
	}()
	go func() {
		defer close(generation.pollerDone)
		poller.Run(pollerCtx)
	}()
	return generation, nil
}

func newBalancePoller(settings config.ProxySettings, database *store.Store,
	logger *log.Logger) (*balance.Poller, error) {
	client, err := balance.NewClient(settings.BalanceURL, settings.APIKey, settings.AuthPrefix, nil)
	if err != nil {
		return nil, fmt.Errorf("create balance client: %w", err)
	}
	service, err := balance.NewService(client, database, time.Now)
	if err != nil {
		return nil, fmt.Errorf("create balance service: %w", err)
	}
	cfg := balance.PollerConfig{}
	if logger != nil {
		cfg.Logf = logger.Printf
	}
	poller, err := balance.NewPoller(service, cfg)
	if err != nil {
		return nil, fmt.Errorf("create balance poller: %w", err)
	}
	return poller, nil
}

func proxyConfig(settings config.ProxySettings, database *store.Store, logger *log.Logger) proxy.Config {
	cfg := proxy.Config{
		Store:         database,
		ListenAddr:    settings.ListenAddr,
		OpenAIBase:    settings.OpenAIBase,
		AnthropicBase: settings.AnthropicBase,
		ToolsBase:     settings.ToolsBase,
		APIPath:       settings.APIPath,
		ProviderID:    settings.ProviderID,
		APIKey:        settings.APIKey,
		AuthPrefix:    settings.AuthPrefix,
		RPMLimit:      settings.RPMLimit,
		Now:           time.Now,
	}
	if logger != nil {
		cfg.Logf = logger.Printf
	}
	return cfg
}

func (r *serveRuntime) current() *serveGeneration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.gen
}

func (r *serveRuntime) setCurrent(generation *serveGeneration) {
	r.mu.Lock()
	r.gen = generation
	r.mu.Unlock()
}

func (r *serveRuntime) Fatal() <-chan error { return r.fatal }

func (r *serveRuntime) Settings() ipc.LiveSettings {
	generation := r.current()
	if generation == nil {
		return ipc.LiveSettings{}
	}
	return ipc.LiveSettings{
		ListenAddr:          generation.settings.ListenAddr,
		BalanceLowThreshold: generation.settings.BalanceLowThreshold,
	}
}

func (r *serveRuntime) Latest() balance.State {
	generation := r.current()
	if generation == nil || generation.poller == nil {
		return balance.State{}
	}
	return generation.poller.Latest()
}

func (r *serveRuntime) ActivityBars(now time.Time) []int {
	generation := r.current()
	if generation == nil {
		return make([]int, trace.DefaultActivityBucketCount)
	}
	return generation.server.ActivityBars(now)
}

func (r *serveRuntime) TrafficFrame(now time.Time, width int) trace.Frame {
	generation := r.current()
	if generation == nil {
		return trace.Frame{}
	}
	return generation.server.TrafficFrame(now, width)
}

// Reload 先完成读取、校验和资源准备，再切换当前代；失败时旧代继续服务。
func (r *serveRuntime) Reload(ctx context.Context) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if r.closing {
		return errors.New("daemon is shutting down")
	}
	settings, err := config.LoadProxySettings()
	if err != nil {
		return fmt.Errorf("重新读取配置失败: %w", err)
	}
	old := r.current()
	if old == nil {
		return errors.New("daemon is not running")
	}
	if old.settings.ListenAddr == settings.ListenAddr {
		return r.reloadCurrentGeneration(ctx, old, settings)
	}
	return r.replaceGeneration(ctx, old, settings)
}

func (r *serveRuntime) reloadCurrentGeneration(ctx context.Context, old *serveGeneration,
	settings config.ProxySettings) error {
	poller, err := newBalancePoller(settings, r.store, r.logger)
	if err != nil {
		return err
	}
	if err := r.store.SetRetentionDays(ctx, settings.RetentionDays); err != nil {
		return fmt.Errorf("应用保留天数失败: %w", err)
	}
	newProxyConfig := proxyConfig(settings, r.store, r.logger)
	if err := old.server.Reconfigure(newProxyConfig); err != nil {
		return fmt.Errorf("应用代理配置失败: %w", err)
	}
	pollerCtx, pollerCancel := context.WithCancel(r.parent)
	next := &serveGeneration{
		settings:     settings,
		server:       old.server,
		listener:     old.listener,
		proxyCancel:  old.proxyCancel,
		proxyDone:    old.proxyDone,
		poller:       poller,
		pollerCancel: pollerCancel,
		pollerDone:   make(chan struct{}),
	}
	r.setCurrent(next)
	go func() {
		defer close(next.pollerDone)
		poller.Run(pollerCtx)
	}()
	old.cancelPoller()
	return nil
}

func (r *serveRuntime) replaceGeneration(ctx context.Context, old *serveGeneration,
	settings config.ProxySettings) error {
	next, err := r.startGeneration(settings)
	if err != nil {
		return fmt.Errorf("启动新监听失败: %w", err)
	}
	if err := r.store.SetRetentionDays(ctx, settings.RetentionDays); err != nil {
		next.stop()
		return fmt.Errorf("应用保留天数失败: %w", err)
	}
	r.setCurrent(next)
	old.retiring.Store(true)
	old.cancelPoller()
	old.stopProxyAsync(r.logger)
	return nil
}

func (g *serveGeneration) cancelPoller() {
	if g == nil || g.pollerCancel == nil {
		return
	}
	g.pollerCancel()
	<-g.pollerDone
}

func (g *serveGeneration) stopProxyAsync(logger *log.Logger) {
	if g == nil || g.proxyCancel == nil {
		return
	}
	g.proxyCancel()
	go func() {
		if err := <-g.proxyDone; err != nil && logger != nil {
			logger.Printf("retired proxy: %v", err)
		}
	}()
}

func (g *serveGeneration) stop() {
	if g == nil {
		return
	}
	g.retiring.Store(true)
	g.cancelPoller()
	if g.proxyCancel != nil {
		g.proxyCancel()
		<-g.proxyDone
	}
}

func (r *serveRuntime) Close() {
	r.closeOnce.Do(func() {
		r.reloadMu.Lock()
		r.closing = true
		generation := r.current()
		r.reloadMu.Unlock()
		if generation != nil {
			generation.stop()
		}
	})
}

func runServe(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help", "-h":
			serveUsage(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "dsmon serve: unknown option %q\n\n", args[0])
			serveUsage(stderr)
			return 2
		}
	}

	settings, err := config.LoadProxySettings()
	if err != nil {
		fmt.Fprintf(stderr, "dsmon serve: %v\n", err)
		return 1
	}
	database, err := store.Open(config.DatabasePath(), store.WithRetentionDays(settings.RetentionDays))
	if err != nil {
		fmt.Fprintf(stderr, "dsmon serve: open database: %v\n", err)
		return 1
	}
	defer func() { _ = database.Close() }()

	maintenanceCtx, cancelMaintenance := context.WithTimeout(context.Background(), 30*time.Second)
	err = database.PerformStartupMaintenance(maintenanceCtx)
	cancelMaintenance()
	if err != nil {
		fmt.Fprintf(stderr, "dsmon serve: startup maintenance: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(stderr, "", log.LstdFlags)
	runtime, err := newServeRuntime(ctx, database, settings, logger)
	if err != nil {
		fmt.Fprintf(stderr, "dsmon serve: %v\n", err)
		return 1
	}
	defer runtime.Close()

	ipcServer, err := ipc.NewServer(ipc.Config{
		Path:     config.RuntimeSocketPath(),
		Store:    database,
		Balance:  runtime,
		Trace:    runtime,
		Settings: runtime.Settings,
		Reload:   runtime.Reload,
		Logf:     logger.Printf,
	})
	if err != nil {
		fmt.Fprintf(stderr, "dsmon serve: %v\n", err)
		return 1
	}
	ipcDone := make(chan error, 1)
	go func() { ipcDone <- ipcServer.ListenAndServe(ctx) }()
	fmt.Fprintf(stdout, "dsmon serve: listening on http://%s\n", settings.ListenAddr)

	var (
		runErr      error
		ipcErr      error
		ipcReceived bool
	)
	select {
	case <-ctx.Done():
	case err := <-runtime.Fatal():
		runErr = err
	case err := <-ipcDone:
		ipcReceived = true
		ipcErr = err
		if err != nil {
			runErr = fmt.Errorf("ipc server: %w", err)
		} else {
			runErr = errors.New("ipc server stopped")
		}
	}
	stop()
	runtime.Close()
	if !ipcReceived {
		ipcErr = <-ipcDone
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "dsmon serve: %v\n", runErr)
		return 1
	}
	if ipcErr != nil && !errors.Is(ipcErr, context.Canceled) {
		fmt.Fprintf(stderr, "dsmon serve: ipc server: %v\n", ipcErr)
		return 1
	}
	return 0
}
