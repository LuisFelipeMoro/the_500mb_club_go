package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ErrNotReady is returned by LazyStore methods before the background dialer has
// established the first healthy Redis connection.
var ErrNotReady = errors.New("storage: redis not ready")

// LazyStore implements Store without blocking the process at boot. A background
// goroutine dials Redis with a tight backoff and publishes the live client via
// an atomic pointer; until then every call returns ErrNotReady so /readyz
// reports 503 (not connection-refused) and the HTTP server can listen from t0.
// rueidis auto-reconnects on blips, so the dial loop only needs to win once.
type LazyStore struct {
	cur atomic.Pointer[RueidisStore] // nil until first successful dial+ping
}

// NewLazy returns a LazyStore and kicks off the background dialer immediately.
func NewLazy(addr string, log *zap.Logger) *LazyStore {
	s := &LazyStore{}
	go s.dialLoop(addr, log)
	return s
}

func (s *LazyStore) dialLoop(addr string, log *zap.Logger) {
	backoff := 50 * time.Millisecond // tight: flip /readyz fast once redis is up
	for attempt := 1; ; attempt++ {
		store, err := NewRueidisStore(addr)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = store.Ping(ctx)
			cancel()
			if err == nil {
				s.cur.Store(store)
				log.Info("redis ready", zap.Int("attempt", attempt))
				return
			}
			store.Close()
		}
		time.Sleep(backoff)
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

func (s *LazyStore) get() (*RueidisStore, error) {
	if st := s.cur.Load(); st != nil {
		return st, nil
	}
	return nil, ErrNotReady
}

func (s *LazyStore) Ping(ctx context.Context) error {
	st, err := s.get()
	if err != nil {
		return err
	}
	return st.Ping(ctx)
}

func (s *LazyStore) AddMulti(ctx context.Context, batches map[string][][]byte) error {
	st, err := s.get()
	if err != nil {
		return err
	}
	return st.AddMulti(ctx, batches)
}

func (s *LazyStore) Range(ctx context.Context, deviceID string, fromTS, toTS, offset, count int64) ([][]byte, error) {
	st, err := s.get()
	if err != nil {
		return nil, err
	}
	return st.Range(ctx, deviceID, fromTS, toTS, offset, count)
}

func (s *LazyStore) LastN(ctx context.Context, deviceID string, n int64) ([][]byte, error) {
	st, err := s.get()
	if err != nil {
		return nil, err
	}
	return st.LastN(ctx, deviceID, n)
}

func (s *LazyStore) Close() {
	if st := s.cur.Load(); st != nil {
		st.Close()
	}
}
