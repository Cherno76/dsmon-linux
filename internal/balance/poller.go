package balance

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	DefaultRefreshInterval = 60 * time.Second
	DefaultMaxBackoff      = 5 * time.Minute
)

type Refresher interface {
	Refresh(context.Context) (Snapshot, error)
}

type PollerConfig struct {
	Interval   time.Duration
	MaxBackoff time.Duration
	Now        func() time.Time
	Logf       func(format string, args ...any)
}

type State struct {
	Balance       Snapshot
	HasBalance    bool
	Err           error
	Failures      int
	LastAttempt   time.Time
	LastSuccess   time.Time
	NextRefreshIn time.Duration
}

type Poller struct {
	refresher Refresher
	cfg       PollerConfig

	mu    sync.RWMutex
	state State
}

func NewPoller(refresher Refresher, cfg PollerConfig) (*Poller, error) {
	if refresher == nil {
		return nil, errors.New("balance refresher is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultRefreshInterval
	}
	if cfg.MaxBackoff <= 0 || cfg.MaxBackoff < cfg.Interval {
		cfg.MaxBackoff = maxDuration(DefaultMaxBackoff, cfg.Interval)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Poller{refresher: refresher, cfg: cfg}, nil
}

// Run 立即刷新一次，之后按 60 秒周期执行；连续失败会指数退避，成功后恢复基础周期。
func (p *Poller) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	delay := time.Duration(0)
	for {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return
		}

		snapshot, err := p.refresher.Refresh(ctx)
		if ctx.Err() != nil {
			return
		}
		now := p.cfg.Now()
		if err == nil {
			delay = p.cfg.Interval
			p.update(State{
				Balance: snapshot, HasBalance: true, LastAttempt: now, LastSuccess: now,
				NextRefreshIn: delay,
			}, true)
			continue
		}

		p.mu.Lock()
		failures := p.state.Failures + 1
		lastSuccess := p.state.LastSuccess
		balance := p.state.Balance
		hasBalance := p.state.HasBalance
		// 查询已经成功、只是采样落库失败时，新余额仍然是可信的。
		// 保留它，避免因为磁盘/数据库故障把界面上的余额退回旧值。
		storageFailure := isStorageError(err)
		if storageFailure {
			lastSuccess = now
		}
		p.mu.Unlock()
		delay = BackoffDelay(failures, p.cfg.Interval, p.cfg.MaxBackoff)
		if storageFailure {
			balance = snapshot
			hasBalance = true
		}
		p.update(State{
			Balance: balance, HasBalance: hasBalance, Err: err,
			Failures: failures, LastAttempt: now, LastSuccess: lastSuccess,
			NextRefreshIn: delay,
		}, false)
	}
}

func (p *Poller) Latest() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

func (p *Poller) update(next State, success bool) {
	p.mu.Lock()
	previousErr := p.state.Err
	if success {
		if previousErr != nil && p.cfg.Logf != nil {
			p.cfg.Logf("余额查询恢复：¥%.2f", next.Balance.Total)
		}
	} else if next.Err != nil && (previousErr == nil || previousErr.Error() != next.Err.Error()) {
		if p.cfg.Logf != nil {
			p.cfg.Logf("余额查询失败：%s（%d 次，%s 后重试）", next.Err.Error(), next.Failures, next.NextRefreshIn)
		}
	}
	p.state = next
	p.mu.Unlock()
}

func BackoffDelay(failures int, interval, maxBackoff time.Duration) time.Duration {
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	if maxBackoff <= 0 || maxBackoff < interval {
		maxBackoff = interval
	}
	if failures <= 1 {
		return interval
	}
	delay := interval
	for index := 1; index < failures; index++ {
		if delay >= maxBackoff/2 {
			return maxBackoff
		}
		delay *= 2
	}
	if delay > maxBackoff {
		return maxBackoff
	}
	return delay
}

func maxDuration(first, second time.Duration) time.Duration {
	if first > second {
		return first
	}
	return second
}

func isStorageError(err error) bool {
	var queryErr *QueryError
	return errors.As(err, &queryErr) && queryErr.Reason == ReasonStorage
}
