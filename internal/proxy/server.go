package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dsmon/internal/proxy/trace"
	"dsmon/internal/store"
)

const (
	MaxConcurrentRequests   = 64
	MaxRequestBodyBytes     = 20 * 1024 * 1024
	RequestReceiveTimeout   = 120 * time.Second
	DefaultListenAddress    = "127.0.0.1:18080"
	DefaultResponseBuffer   = 8 * 1024 * 1024
	DefaultToolBuffer       = 20 * 1024 * 1024
	anthropicDefaultVersion = "2023-06-01"
)

type Config struct {
	Store               *store.Store
	ListenAddr          string
	OpenAIBase          string
	AnthropicBase       string
	ToolsBase           string
	APIPath             string
	ProviderID          string
	APIKey              string
	AuthPrefix          string
	RPMLimit            int
	MaxConcurrent       int
	MaxRequestBytes     int64
	RequestTimeout      time.Duration
	ResponseBufferBytes int
	ToolResponseBuffer  int
	Now                 func() time.Time
	Logf                func(format string, args ...any)
}

type route struct {
	endpoint string
	base     string
	protocol Protocol
	tool     ToolEndpoint
	// auxiliary 路由只透传元数据，不写 usage_log，避免污染计费和统计。
	auxiliary bool
}

type Server struct {
	cfgMu     sync.RWMutex
	cfg       Config
	transport *http.Transport
	limiter   *Limiter
	curves    *curveState
	slots     chan struct{}
}

type curveState struct {
	mu       sync.Mutex
	activity *trace.Activity
	traffic  *trace.Traffic
	ratchet  trace.Ratchet
	rateHold trace.RateHold
	now      func() time.Time
}

func NewServer(cfg Config) (*Server, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	now := cfg.Now()
	return &Server{
		cfg:       cfg,
		transport: newUpstreamTransport(),
		limiter:   NewLimiter(cfg.Now),
		curves: &curveState{
			activity: trace.NewActivity(now),
			traffic:  trace.NewTraffic(now),
			now:      cfg.Now,
		},
		slots: make(chan struct{}, cfg.MaxConcurrent),
	}, nil
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.Store == nil {
		return Config{}, errors.New("proxy store is required")
	}
	cfg.ListenAddr = defaultString(cfg.ListenAddr, DefaultListenAddress)
	cfg.OpenAIBase = defaultString(cfg.OpenAIBase, "https://api.deepseek.com")
	cfg.AnthropicBase = defaultString(cfg.AnthropicBase, "https://api.deepseek.com/anthropic")
	cfg.ToolsBase = defaultString(cfg.ToolsBase, cfg.OpenAIBase)
	cfg.APIPath = defaultString(cfg.APIPath, "/v1")
	cfg.ProviderID = defaultString(cfg.ProviderID, "deepseek")
	cfg.AuthPrefix = defaultString(cfg.AuthPrefix, "Bearer")
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = MaxConcurrentRequests
	}
	if cfg.MaxConcurrent > MaxConcurrentRequests {
		cfg.MaxConcurrent = MaxConcurrentRequests
	}
	if cfg.MaxRequestBytes <= 0 || cfg.MaxRequestBytes > MaxRequestBodyBytes {
		cfg.MaxRequestBytes = MaxRequestBodyBytes
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = RequestReceiveTimeout
	}
	if cfg.ResponseBufferBytes <= 0 {
		cfg.ResponseBufferBytes = DefaultResponseBuffer
	}
	if cfg.ToolResponseBuffer <= 0 {
		cfg.ToolResponseBuffer = DefaultToolBuffer
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if err := validateListenAddress(cfg.ListenAddr); err != nil {
		return Config{}, err
	}
	for name, base := range map[string]string{
		"OpenAI": cfg.OpenAIBase, "Anthropic": cfg.AnthropicBase, "Tools": cfg.ToolsBase,
	} {
		if err := validateBaseURL(base); err != nil {
			return Config{}, fmt.Errorf("%s base URL: %w", name, err)
		}
	}
	if cfg.RPMLimit < 0 {
		cfg.RPMLimit = 0
	}

	return cfg, nil
}

// Reconfigure 原地替换运行配置。监听地址不能这样改；端口变化由 daemon 新建 listener。
func (s *Server) Reconfigure(cfg Config) error {
	current := s.config()
	if cfg.Store == nil {
		cfg.Store = current.Store
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return err
	}
	if normalized.Store != current.Store {
		return errors.New("proxy reconfigure cannot change the store")
	}
	if normalized.ListenAddr != current.ListenAddr {
		return fmt.Errorf("proxy reconfigure cannot change listen address from %s to %s",
			current.ListenAddr, normalized.ListenAddr)
	}
	s.cfgMu.Lock()
	s.cfg = normalized
	s.cfgMu.Unlock()
	return nil
}

func (s *Server) config() Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func newUpstreamTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func validateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", address, err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("listen address %q must use host 127.0.0.1", address)
	}
	if port == "" {
		return fmt.Errorf("listen address %q needs a port", address)
	}
	return nil
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse %q: %w", raw, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%q must be an absolute http(s) URL", raw)
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := s.config()
	if requestHeadBytes(r) > MaxHeaderBytes {
		writeProxyError(w, http.StatusRequestHeaderFieldsTooLarge, "request headers exceed 64KB limit")
		return
	}
	target, ok := routeFor(cfg, r)
	if !ok {
		writeProxyError(w, http.StatusNotFound, "unknown proxy route")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		writeProxyError(w, http.StatusServiceUnavailable, "too many concurrent requests")
		return
	}

	started := cfg.Now()
	body, err := readRequestBody(w, r, cfg.MaxRequestBytes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRequestBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		if !target.auxiliary {
			s.recordFailure(cfg, requestAccount{
				started: started, endpoint: target.endpoint, model: "unknown",
				userAgent: r.UserAgent(), providerID: cfg.ProviderID,
			}, status)
		}
		writeProxyError(w, status, err.Error())
		return
	}

	model := RequestModel(body)
	if target.tool != ToolNone {
		model = target.tool.UsageLabel()
	}
	account := requestAccount{
		started: started, endpoint: target.endpoint, model: model,
		userAgent: r.UserAgent(), providerID: cfg.ProviderID,
	}
	recordFailure := func(statusCode int) {
		if !target.auxiliary {
			s.recordFailure(cfg, account, statusCode)
		}
	}
	if cfg.RPMLimit > 0 && !s.limiter.Check(cfg.ProviderID, cfg.RPMLimit) {
		recordFailure(http.StatusTooManyRequests)
		writeProxyError(w, http.StatusTooManyRequests,
			fmt.Sprintf("rate limit exceeded: %d requests per minute", cfg.RPMLimit))
		return
	}

	headers := upstreamHeaders(cfg, r, target)
	upstreamRequest, err := BuildUpstreamRequest(r.Context(), r.Method, r.URL.RequestURI(), headers,
		body, target.base, cfg.APIPath, cfg.APIKey, cfg.AuthPrefix)
	if err != nil {
		recordFailure(http.StatusBadGateway)
		cfg.Logf("build upstream request: %v", err)
		writeProxyError(w, http.StatusBadGateway, "cannot build upstream request")
		return
	}
	upstreamRequest.Body = &uploadRecorder{ReadCloser: upstreamRequest.Body, server: s}

	s.curves.RecordRequest()
	response, err := s.transport.RoundTrip(upstreamRequest)
	if err != nil {
		recordFailure(http.StatusBadGateway)
		cfg.Logf("upstream request %s failed: %v", target.endpoint, err)
		writeProxyError(w, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer response.Body.Close()

	statusCode := response.StatusCode
	latencyMS := millisecondsSince(started, cfg.Now())
	streaming := IsStreamingResponse(response.Header)
	bufferLimit := cfg.ResponseBufferBytes
	if target.tool != ToolNone {
		bufferLimit = cfg.ToolResponseBuffer
	}
	collector := NewResponseBodyCollector(streaming, bufferLimit)
	analyzer := NewDecodedResponseAnalyzer(collector, response.Header.Get("Content-Encoding"))

	CopyResponseHeaders(w.Header(), response.Header, streaming)
	w.WriteHeader(statusCode)
	copyErr := s.copyResponse(w, response.Body, analyzer, streaming)
	analyzer.Close()
	if copyErr != nil {
		cfg.Logf("relay response %s: %v", target.endpoint, copyErr)
	}

	if statusCode != http.StatusOK {
		account.started = started
		recordFailure(statusCode)
		return
	}
	if !target.auxiliary {
		s.recordSuccess(cfg, account, target.tool, collector, latencyMS)
	}
}

func requestHeadBytes(r *http.Request) int {
	total := len(r.Method) + 1 + len(r.URL.RequestURI()) + 1 + len(r.Proto) + 2
	total += len("Host: ") + len(r.Host) + 2
	if r.ContentLength >= 0 {
		total += len("Content-Length: ") + len(fmt.Sprint(r.ContentLength)) + 2
	}
	if len(r.TransferEncoding) > 0 {
		total += len("Transfer-Encoding: ") + len(strings.Join(r.TransferEncoding, ", ")) + 2
	}
	for name, values := range r.Header {
		for _, value := range values {
			total += len(name) + 2 + len(value) + 2
		}
	}
	return total + 2
}

func routeFor(cfg Config, r *http.Request) (route, bool) {
	path := r.URL.Path
	switch path {
	case "/v1/chat/completions", "/v1/responses":
		return route{
			endpoint: path, base: cfg.OpenAIBase, protocol: ProtocolOpenAI,
		}, true
	case "/v1/messages":
		return route{
			endpoint: "/v1/messages", base: cfg.AnthropicBase, protocol: ProtocolAnthropic,
		}, true
	case "/v1/tools/search":
		return route{
			endpoint: "/v1/tools/search", base: cfg.ToolsBase,
			protocol: ProtocolOpenAI, tool: ToolSearch,
		}, true
	case "/v1/tools/fetch":
		return route{
			endpoint: "/v1/tools/fetch", base: cfg.ToolsBase,
			protocol: ProtocolOpenAI, tool: ToolFetch,
		}, true
	default:
		if isModelDiscoveryPath(path) &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) {
			protocol := DetectProtocol(path, r.Header)
			base := cfg.OpenAIBase
			if protocol == ProtocolAnthropic {
				base = cfg.AnthropicBase
			}
			return route{
				endpoint: path, base: base, protocol: protocol, auxiliary: true,
			}, true
		}
		return route{}, false
	}
}

func isModelDiscoveryPath(path string) bool {
	if path == "/v1/models" {
		return true
	}
	return strings.HasPrefix(path, "/v1/models/") && !strings.Contains(path, "..")
}

func upstreamHeaders(cfg Config, r *http.Request, target route) http.Header {
	headers := r.Header.Clone()
	if target.protocol == ProtocolAnthropic {
		headers.Del("Authorization")
		if cfg.APIKey != "" {
			headers.Set("x-api-key", "local-placeholder")
		}
		if headers.Get("anthropic-version") == "" {
			headers.Set("anthropic-version", anthropicDefaultVersion)
		}
		return headers
	}
	headers.Del("x-api-key")
	if cfg.APIKey != "" {
		headers.Set("Authorization", "Bearer local-placeholder")
	}
	return headers
}

func readRequestBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	defer r.Body.Close()
	if r.ContentLength > limit {
		return nil, errRequestBodyTooLarge
	}
	reader := http.MaxBytesReader(w, r.Body, limit+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errRequestBodyTooLarge
		}
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, errRequestBodyTooLarge
	}
	return body, nil
}

var errRequestBodyTooLarge = errors.New("request body exceeds 20MB limit")

func (s *Server) copyResponse(w http.ResponseWriter, body io.Reader,
	analyzer *DecodedResponseAnalyzer, streaming bool) error {
	buffer := make([]byte, 32*1024)
	flusher, _ := w.(http.Flusher)
	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			s.curves.RecordDownload(n)
			analyzer.Append(chunk)
			if _, writeErr := w.Write(chunk); writeErr != nil {
				return writeErr
			}
			if streaming && flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (s *Server) recordSuccess(cfg Config, account requestAccount, tool ToolEndpoint,
	collector *ResponseBodyCollector, latencyMS float64) {
	if tool != ToolNone {
		body, ok := collector.CompleteBody()
		if !ok || !tool.IsBillableSuccess(http.StatusOK, body) {
			return
		}
		account.record(cfg, http.StatusOK, latencyMS, TokenUsage{}, ToolCostPerSuccess)
		return
	}
	usage, ok := collector.Usage()
	if !ok {
		return
	}
	fixedCost := ServerSearchCost(collector.ServerSearchCount(), cfg.Now())
	account.record(cfg, http.StatusOK, latencyMS, usage, fixedCost)
}

func (s *Server) recordFailure(cfg Config, account requestAccount, statusCode int) {
	latencyMS := millisecondsSince(account.started, cfg.Now())
	account.record(cfg, statusCode, latencyMS, TokenUsage{}, 0)
}

type requestAccount struct {
	started    time.Time
	endpoint   string
	model      string
	userAgent  string
	providerID string
}

func (a requestAccount) record(cfg Config, statusCode int, latencyMS float64,
	usage TokenUsage, fixedCost float64) {
	model := a.model
	if strings.TrimSpace(model) == "" {
		model = "unknown"
	}
	record := store.UsageRecord{
		UUID:             newUUID(),
		Timestamp:        cfg.Now(),
		ProviderID:       a.providerID,
		Model:            model,
		Endpoint:         a.endpoint,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CachedTokens:     usage.CachedTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		LatencyMS:        latencyMS,
		StatusCode:       statusCode,
		UserAgent:        a.userAgent,
		FixedCost:        fixedCost,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cfg.Store.Insert(ctx, record); err != nil {
		cfg.Logf("store usage record: %v", err)
	}
}

type uploadRecorder struct {
	io.ReadCloser
	server *Server
}

func (r *uploadRecorder) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.server.curves.RecordUpload(n)
	}
	return n, err
}

func millisecondsSince(start, end time.Time) float64 {
	return float64(end.Sub(start)) / float64(time.Millisecond)
}

func writeProxyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, message+"\n")
}

func (s *Server) ActivityBars(now time.Time) []int {
	s.curves.mu.Lock()
	defer s.curves.mu.Unlock()
	if now.IsZero() {
		now = s.curves.now()
	}
	return s.curves.activity.Bars(now)
}

func (s *Server) TrafficBuckets(now time.Time) []trace.Bucket {
	s.curves.mu.Lock()
	defer s.curves.mu.Unlock()
	if now.IsZero() {
		now = s.curves.now()
	}
	return s.curves.traffic.Snapshot(now)
}

// TrafficFrame 在 daemon 内维护量程棘轮和速率迟滞，TUI 每次只取稳定后的帧。
func (s *Server) TrafficFrame(now time.Time, cellWidth int) trace.Frame {
	if cellWidth < 1 {
		cellWidth = 120
	}
	s.curves.mu.Lock()
	defer s.curves.mu.Unlock()
	if now.IsZero() {
		now = s.curves.now()
	}
	buckets := s.curves.traffic.Snapshot(now)
	visibleBuckets := cellWidth * 2
	if len(buckets) > visibleBuckets {
		buckets = buckets[len(buckets)-visibleBuckets:]
	}
	frame := trace.MakeFrame(buckets, s.curves.traffic.BucketSeconds(), now, float64(cellWidth*2))
	scales := s.curves.ratchet.Scales(frame.DownPeak, frame.UpPeak, now)
	downScale, upScale := trace.UnifyScales(scales.Down, scales.Up)
	rates := s.curves.rateHold.Apply(frame.DownRate, frame.UpRate, now)
	stats := s.curves.traffic.Stats()
	frame.DownScale, frame.UpScale = downScale, upScale
	frame.DownRate, frame.UpRate = rates.Down, rates.Up
	frame.DownTop, frame.UpTop = stats.DownloadTop, stats.UploadTop
	frame.DownTotal, frame.UpTotal = stats.DownloadTotal, stats.UploadTotal
	return frame
}

func (c *curveState) RecordRequest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.activity.Record(c.now())
}

func (c *curveState) RecordUpload(bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.traffic.Record(bytes, 0, c.now())
}

func (c *curveState) RecordDownload(bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.traffic.Record(0, bytes, c.now())
}

func (s *Server) Listen() (net.Listener, error) {
	cfg := s.config()
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}
	return listener, nil
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	listener, err := s.Listen()
	if err != nil {
		return err
	}
	defer listener.Close()
	return s.Serve(ctx, listener)
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	cfg := s.config()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() {
		return fmt.Errorf("proxy listener %s is not loopback", listener.Addr())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	httpServer := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: cfg.RequestTimeout,
		ReadTimeout:       cfg.RequestTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    MaxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}

	stopShutdown := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				_ = httpServer.Close()
			}
		case <-stopShutdown:
		}
	}()

	err := httpServer.Serve(listener)
	close(stopShutdown)
	<-shutdownDone
	s.transport.CloseIdleConnections()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func newUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:]
}
