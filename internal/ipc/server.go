package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"dsmon/internal/balance"
	"dsmon/internal/proxy/trace"
	"dsmon/internal/store"
)

const requestTimeout = 5 * time.Second

const balanceTrendCacheTTL = 30 * time.Second

const (
	balance24hBucketCount = 288 // 24h / 5min
	balance7dBucketCount  = 336 // 7d / 30min
)

type BalanceSource interface {
	Latest() balance.State
}

type TraceSource interface {
	ActivityBars(time.Time) []int
	TrafficFrame(time.Time, int) trace.Frame
	LastActivityAt() time.Time
}

type LiveSettings struct {
	ListenAddr          string
	BalanceLowThreshold float64
}

type Config struct {
	Path                string
	Store               *store.Store
	Balance             BalanceSource
	Trace               TraceSource
	ListenAddr          string
	BalanceLowThreshold float64
	Settings            func() LiveSettings
	Reload              func(context.Context) error
	Now                 func() time.Time
	Logf                func(format string, args ...any)
}

type Server struct {
	cfg Config

	trendMu  sync.Mutex
	trendAt  time.Time
	trend24h []BalancePoint
	trend7d  []BalancePoint
}

func NewServer(cfg Config) (*Server, error) {
	if cfg.Path == "" {
		return nil, errors.New("ipc socket path is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("ipc store is required")
	}
	if cfg.Balance == nil {
		return nil, errors.New("ipc balance source is required")
	}
	if cfg.Trace == nil {
		return nil, errors.New("ipc trace source is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Server{cfg: cfg}, nil
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.Path), 0o700); err != nil {
		return fmt.Errorf("create ipc directory: %w", err)
	}
	if err := os.Remove(s.cfg.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale ipc socket: %w", err)
	}
	listener, err := net.Listen("unix", s.cfg.Path)
	if err != nil {
		return fmt.Errorf("listen on ipc socket: %w", err)
	}
	if err := os.Chmod(s.cfg.Path, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("secure ipc socket: %w", err)
	}
	defer os.Remove(s.cfg.Path)

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-stop:
		}
	}()
	defer close(stop)

	var connections sync.WaitGroup
	defer connections.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept ipc connection: %w", err)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			defer conn.Close()
			s.serveConnection(ctx, conn)
		}()
	}
}

func (s *Server) serveConnection(ctx context.Context, conn net.Conn) {
	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		var request Request
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			if s.cfg.Logf != nil {
				s.cfg.Logf("ipc request decode: %v", err)
			}
			return
		}
		response := s.handle(ctx, request)
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := encoder.Encode(response); err != nil {
			if s.cfg.Logf != nil {
				s.cfg.Logf("ipc response write: %v", err)
			}
			return
		}
	}
}

func (s *Server) handle(parent context.Context, request Request) Response {
	if request.V != Version {
		return failure(fmt.Errorf("unsupported ipc version %d", request.V))
	}
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()

	var (
		data any
		err  error
	)
	switch request.Cmd {
	case "stats":
		data, err = s.stats(ctx)
	case "trace":
		data = s.trace(request.Width)
	case "series":
		data, err = s.series(ctx, request)
	case "requests":
		data, err = s.requests(ctx, request)
	case "pricing":
		data, err = s.pricing(ctx)
	case "reload":
		if s.cfg.Reload == nil {
			err = errors.New("daemon does not support reload")
		} else {
			err = s.cfg.Reload(ctx)
			data = map[string]bool{"reloaded": err == nil}
		}
	default:
		err = fmt.Errorf("unknown ipc command %q", request.Cmd)
	}
	if err != nil {
		return failure(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return failure(fmt.Errorf("encode ipc data: %w", err))
	}
	return Response{V: Version, OK: true, Data: raw}
}

func failure(err error) Response {
	return Response{V: Version, OK: false, Error: err.Error()}
}

func (s *Server) stats(ctx context.Context) (Stats, error) {
	snapshot, err := s.cfg.Store.MenuBarSnapshot(ctx)
	if err != nil {
		return Stats{}, err
	}
	rows, err := s.cfg.Store.Requests(ctx, 10, 0)
	if err != nil {
		return Stats{}, err
	}
	balanceState := s.cfg.Balance.Latest()
	health := balanceState.Err == nil
	recent := make([]RequestRow, 0, len(rows))
	for _, row := range rows {
		if row.StatusCode >= 500 {
			health = false
		}
		recent = append(recent, convertRequestRow(row))
	}
	hitRate := 0.0
	if snapshot.TodayCacheHitRate != nil {
		hitRate = *snapshot.TodayCacheHitRate * 100
	}
	trend24h, trend7d := s.balanceTrends(ctx, s.cfg.Now())
	listenAddr := s.cfg.ListenAddr
	lowThreshold := s.cfg.BalanceLowThreshold
	if s.cfg.Settings != nil {
		live := s.cfg.Settings()
		listenAddr = live.ListenAddr
		lowThreshold = live.BalanceLowThreshold
	}
	return Stats{
		ProxyRunning:        true,
		ListenAddr:          listenAddr,
		BalanceLowThreshold: lowThreshold,
		Health:              health,
		Balance: Balance{
			Has:                balanceState.HasBalance,
			Total:              balanceState.Balance.Total,
			Granted:            balanceState.Balance.Granted,
			ToppedUp:           balanceState.Balance.ToppedUp,
			Trend24h:           trend24h,
			Trend7d:            trend7d,
			Error:              errorText(balanceState.Err),
			Failures:           balanceState.Failures,
			LastAttemptUnix:    unixSeconds(balanceState.LastAttempt),
			LastSuccessUnix:    unixSeconds(balanceState.LastSuccess),
			NextRefreshSeconds: balanceState.NextRefreshIn.Seconds(),
		},
		Today: Aggregate{
			Requests:         snapshot.TodayRequests,
			TotalTokens:      snapshot.TodayTokens,
			CacheHitRate:     hitRate,
			Cost:             snapshot.TodayCost.Cost,
			UnpricedRequests: snapshot.TodayCost.UnpricedRequests,
		},
		Recent: recent,
	}, nil
}

// balanceTrends 把余额采样压到固定数量的桶。统计页每 2 秒刷新，缓存避免重复扫描 7 天数据。
func (s *Server) balanceTrends(ctx context.Context, now time.Time) ([]BalancePoint, []BalancePoint) {
	s.trendMu.Lock()
	defer s.trendMu.Unlock()
	if !s.trendAt.IsZero() && now.Sub(s.trendAt) < balanceTrendCacheTTL {
		return copyBalancePoints(s.trend24h), copyBalancePoints(s.trend7d)
	}
	samples, err := s.cfg.Store.BalanceSamples(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		return copyBalancePoints(s.trend24h), copyBalancePoints(s.trend7d)
	}
	s.trendAt = now
	s.trend24h = downsampleBalance(samples, now.Add(-24*time.Hour), now, balance24hBucketCount)
	s.trend7d = downsampleBalance(samples, now.Add(-7*24*time.Hour), now, balance7dBucketCount)
	return copyBalancePoints(s.trend24h), copyBalancePoints(s.trend7d)
}

func downsampleBalance(samples []store.BalanceSample, start, end time.Time, count int) []BalancePoint {
	if count <= 0 || !start.Before(end) {
		return nil
	}
	width := end.Sub(start) / time.Duration(count)
	if width <= 0 {
		return nil
	}
	values := make([]float64, count)
	valid := make([]bool, count)
	haveSample := false
	for _, sample := range samples {
		if sample.Timestamp.Before(start) || sample.Timestamp.After(end) {
			continue
		}
		index := int(sample.Timestamp.Sub(start) / width)
		if index >= count {
			index = count - 1
		}
		values[index] = sample.Balance
		valid[index] = true
		haveSample = true
	}
	if !haveSample {
		return nil
	}
	result := make([]BalancePoint, 0, count)
	lastValue := 0.0
	haveLast := false
	for index := range values {
		if valid[index] {
			lastValue = values[index]
			haveLast = true
		} else if haveLast {
			values[index] = lastValue
		} else {
			result = append(result, BalancePoint{
				Unix:    start.Add(time.Duration(index) * width).Unix(),
				Missing: true,
			})
			continue
		}
		result = append(result, BalancePoint{
			Unix:  start.Add(time.Duration(index) * width).Unix(),
			Value: values[index],
		})
	}
	return result
}

func copyBalancePoints(points []BalancePoint) []BalancePoint {
	return append([]BalancePoint(nil), points...)
}

func (s *Server) trace(width int) Trace {
	now := s.cfg.Now()
	frame := s.cfg.Trace.TrafficFrame(now, width)
	activity := s.cfg.Trace.ActivityBars(now)
	return Trace{
		Activity:             activity,
		ActivityBucketSecs:   int(trace.DefaultActivityBucketSeconds / time.Second),
		ActivityFullScale:    3,
		Traffic:              convertTraffic(frame.Buckets),
		TrafficBucketSecs:    frame.BucketSeconds.Seconds(),
		Shift:                frame.Shift,
		Grid:                 append([]float64(nil), frame.GridLines...),
		DownRate:             frame.DownRate,
		UpRate:               frame.UpRate,
		DownScale:            frame.DownScale,
		UpScale:              frame.UpScale,
		DownTop:              frame.DownTop,
		UpTop:                frame.UpTop,
		DownTotal:            frame.DownTotal,
		UpTotal:              frame.UpTotal,
		NowUnixNano:          now.UnixNano(),
		LastActivityUnixNano: unixNano(s.cfg.Trace.LastActivityAt()),
		Health:               s.cfg.Balance.Latest().Err == nil,
	}
}

func (s *Server) series(ctx context.Context, request Request) (Series, error) {
	period := store.UsagePeriod(request.Range)
	var custom *store.TimeRange
	if period == store.PeriodCustom {
		if request.From <= 0 || request.To <= request.From {
			return Series{}, errors.New("custom range requires from < to")
		}
		custom = &store.TimeRange{
			Start: time.Unix(request.From, 0).In(time.Local),
			End:   time.Unix(request.To, 0).In(time.Local),
		}
	}
	interval, ok := store.Interval(period, custom, s.cfg.Now(), time.Local)
	if !ok {
		return Series{}, fmt.Errorf("unsupported usage range %q", request.Range)
	}
	bucket := store.BucketForInterval(interval)
	dimension := store.UsageDimension(request.Dimension)
	if dimension == "" {
		dimension = store.DimensionModel
	}
	points, err := s.cfg.Store.QuerySeriesPoints(ctx, interval.Start, interval.End, bucket, dimension, "", "")
	if err != nil {
		return Series{}, err
	}
	points = store.FillSeriesGaps(points, store.SeriesNames(points), interval.Start, interval.End, bucket, time.Local)
	result := Series{
		Range: request.Range, Dimension: string(dimension), Bucket: string(bucket),
		FromUnix: interval.Start.Unix(), ToUnix: interval.End.Unix(),
	}
	result.Points = make([]SeriesPoint, 0, len(points))
	for _, point := range points {
		result.Points = append(result.Points, SeriesPoint{
			StartUnix: point.BucketStart.Unix(), Series: point.Series,
			Requests: point.Requests, HitTokens: point.HitTokens, MissTokens: point.MissTokens,
			OutTokens: point.OutTokens, Cost: point.Cost,
		})
	}
	return result, nil
}

func (s *Server) requests(ctx context.Context, request Request) (Requests, error) {
	limit := request.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := request.Offset
	if offset < 0 {
		offset = 0
	}
	total, err := s.cfg.Store.RequestCount(ctx)
	if err != nil {
		return Requests{}, err
	}
	rows, err := s.cfg.Store.Requests(ctx, limit, offset)
	if err != nil {
		return Requests{}, err
	}
	result := Requests{Total: total, Offset: offset, Rows: make([]RequestRow, 0, len(rows))}
	for _, row := range rows {
		result.Rows = append(result.Rows, convertRequestRow(row))
	}
	return result, nil
}

func (s *Server) pricing(ctx context.Context) (Pricing, error) {
	count, latest, source, err := s.cfg.Store.PricingSummary(ctx)
	if err != nil {
		return Pricing{}, err
	}
	result := Pricing{Count: count, LatestSource: string(source)}
	if latest != nil {
		result.LatestValidUnix = latest.Unix()
	}
	return result, nil
}

func convertRequestRow(row store.RequestRow) RequestRow {
	return RequestRow{
		ID: row.ID, TimestampUnix: row.Timestamp.Unix(), Model: row.Model, Client: row.Client,
		Endpoint: row.Endpoint, PromptTokens: row.PromptTokens,
		CompletionTokens: row.CompletionTokens, TotalTokens: row.TotalTokens,
		LatencyMS: row.LatencyMS, StatusCode: row.StatusCode, Cost: row.Cost,
	}
}

func convertTraffic(buckets []trace.Bucket) []TrafficBucket {
	result := make([]TrafficBucket, 0, len(buckets))
	for _, bucket := range buckets {
		result = append(result, TrafficBucket{Upload: bucket.Upload, Download: bucket.Download})
	}
	return result
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func unixSeconds(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.Unix()
}

func unixNano(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixNano()
}
