package batch

import (
	"context"
	"time"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/storage"
	"go.uber.org/zap"
)

type writeRequest struct {
	deviceID string
	encoded  [][]byte
}

// Writer accepts pre-encoded telemetry members on a buffered channel and a
// single goroutine flushes them to Redis, grouped by device. It coalesces every
// request already queued into one flush (up to flushThreshold points) and then
// writes — so under low load a write lands in Redis within microseconds of the
// push (read-after-write stays fast), while bursts batch into few ZADDs.
type Writer struct {
	ch    chan writeRequest
	done  chan struct{}
	store storage.Store
	log   *zap.Logger

	flushThreshold int
}

// New creates a Writer with a buffered channel of the given capacity.
func New(store storage.Store, bufSize int, log *zap.Logger) *Writer {
	return &Writer{
		ch:             make(chan writeRequest, bufSize),
		done:           make(chan struct{}),
		store:          store,
		log:            log,
		flushThreshold: 500,
	}
}

// Push enqueues encoded members for a device. It never blocks: a full channel
// returns false (the caller still answers 2xx — an accepted=0 overflow signal).
func (w *Writer) Push(deviceID string, encoded [][]byte) bool {
	select {
	case w.ch <- writeRequest{deviceID: deviceID, encoded: encoded}:
		return true
	default:
		return false
	}
}

// Run drains the channel until it is closed, then flushes the remainder.
func (w *Writer) Run() {
	pending := make(map[string][][]byte)
	total := 0

	flush := func() {
		if total == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := w.store.AddMulti(ctx, pending); err != nil {
			w.log.Error("batch flush failed", zap.Error(err), zap.Int("points", total))
		}
		cancel()
		pending = make(map[string][][]byte)
		total = 0
	}

	add := func(req writeRequest) {
		pending[req.deviceID] = append(pending[req.deviceID], req.encoded...)
		total += len(req.encoded)
	}

	for {
		req, ok := <-w.ch
		if !ok {
			flush()
			close(w.done)
			return
		}
		add(req)
		// Coalesce whatever else is already queued (non-blocking) before writing.
		drained := false
		for !drained && total < w.flushThreshold {
			select {
			case req, ok := <-w.ch:
				if !ok {
					flush()
					close(w.done)
					return
				}
				add(req)
			default:
				drained = true
			}
		}
		flush()
	}
}

// Close stops accepting writes and waits for the final drain to complete.
func (w *Writer) Close() {
	close(w.ch)
	<-w.done
}
