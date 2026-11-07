package balance

import (
	"context"
	"errors"
	"time"

	"dsmon/internal/store"
)

type SampleStore interface {
	RecordBalanceSample(context.Context, store.BalanceSample) error
}

type Service struct {
	client *Client
	store  SampleStore
	now    func() time.Time
}

func NewService(client *Client, samples SampleStore, now func() time.Time) (*Service, error) {
	if client == nil {
		return nil, errors.New("balance client is required")
	}
	if samples == nil {
		return nil, errors.New("balance sample store is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{client: client, store: samples, now: now}, nil
}

// Refresh 查一次余额；成功时立刻写一条采样。写库失败仍返回查到的快照，
// 让 TUI 可以先显示余额，同时把持久化失败单独标出来。
func (s *Service) Refresh(ctx context.Context) (Snapshot, error) {
	snapshot, err := s.client.Query(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	sample := store.BalanceSample{
		Timestamp: s.now(),
		Balance:   snapshot.Total,
		Granted:   snapshot.Granted,
		ToppedUp:  snapshot.ToppedUp,
	}
	if err := s.store.RecordBalanceSample(ctx, sample); err != nil {
		return snapshot, &QueryError{Reason: ReasonStorage, Detail: err.Error()}
	}
	return snapshot, nil
}
