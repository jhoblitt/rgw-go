package driver

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// completion is one bucket_complete_op request and the index change it
// belongs to, a copy of the request's own so that the request and the
// completion never share it.
type completion struct {
	x indexOp
	c rgw.CompleteOp
}

// completionManager is RGWIndexCompletionManager (driver/rados/rgw_rados.cc,
// a bare line number below being v19.2.6's): bucket_complete_op writes sent
// off the request path, and the worker that sends again the ones a reshard
// refused. Each submitted write runs in its own goroutine under the
// manager's context, which Run's context ending cancels; the worker runs as
// the Store's index-completions worker.
type completionManager struct {
	s      *Store
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool         // run has stopped: nothing is sent any more
	queue    []completion // refused by a reshard, waiting for run
	inflight int          // submitted or being retried, and not yet done
}

// newCompletionManager returns the manager of s's completions, its context
// carrying ctx's values but not its cancellation.
func newCompletionManager(ctx context.Context, s *Store) *completionManager {
	mctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &completionManager{s: s, ctx: mctx, cancel: cancel, wake: make(chan struct{}, 1)}
}

// submit is cls_obj_complete_op's aio_operate and handle_completion
// (:9514-9518, :994-1021): the write runs in its own goroutine and the
// request does not wait for it. A busy-resharding answer queues the
// completion for run; any other failure is logged and the completion
// dropped, leaving the pending entry for listing to reconcile.
func (m *completionManager) submit(x *indexOp, c rgw.CompleteOp) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		slog.WarnContext(m.ctx, "bucket index completion dropped: the completion manager has stopped",
			slog.String("bucket", x.obj.Bucket.Name), slog.String("key", c.Key.Name), slog.Int("op", int(c.Op)))
		return
	}
	m.inflight++
	m.wg.Add(1)
	m.mu.Unlock()
	comp := completion{x: *x, c: c}
	go func() {
		defer m.wg.Done()
		x := &comp.x
		sh, err := x.currentShard(m.ctx)
		if err == nil {
			_, err = sh.pool.Write(m.ctx, sh.oid, x.completeOp(c), radosclient.OpFlagNone)
		}
		busy := errors.Is(err, radosclient.ErrBusyResharding)
		m.mu.Lock()
		m.inflight--
		queued := busy && !m.closed
		if queued {
			m.queue = append(m.queue, comp)
			select {
			case m.wake <- struct{}{}:
			default:
			}
		}
		m.mu.Unlock()
		if err != nil && !queued {
			slog.WarnContext(m.ctx, "bucket index completion failed; the pending entry is left for listing to reconcile",
				slog.String("bucket", x.obj.Bucket.Name), slog.String("key", c.Key.Name), slog.Int("op", int(c.Op)), slog.Any("error", err))
		}
	}()
}

// pending returns the completions submitted or queued and not yet done.
func (m *completionManager) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inflight + len(m.queue)
}

// run is RGWIndexCompletionManager::process (:875-938) as the Store's
// worker: each completion a reshard refused is sent again, one at a time.
// When ctx ends, the writes in flight are canceled and awaited and the
// queued ones abandoned, as the manager's stop abandons them (:804-819).
func (m *completionManager) run(ctx context.Context) error {
	stop := context.AfterFunc(ctx, m.cancel)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.closed = true
			m.queue = nil
			m.mu.Unlock()
			m.cancel()
			m.wg.Wait()
			return nil
		case <-m.wake:
			m.mu.Lock()
			batch := m.queue
			m.queue = nil
			m.inflight += len(batch)
			m.mu.Unlock()
			for i := range batch {
				if m.ctx.Err() == nil {
					m.retry(&batch[i])
				}
				m.mu.Lock()
				m.inflight--
				m.mu.Unlock()
			}
		}
	}
}

// retry is one pass of process's loop (:890-937): the bucket instance read
// again and the completion sent to the shard it names, under the reshard
// guard. Unlike the first attempt, process sends the request without the
// object locator: its cls_rgw_bucket_complete_op call leaves it at its
// default (:918-919). A failure is logged and the completion dropped.
func (m *completionManager) retry(comp *completion) {
	x := &comp.x
	rec, err := m.s.GetBucketInstance(m.ctx, x.rec.Info.Bucket)
	if err != nil {
		slog.ErrorContext(m.ctx, "bucket index completion dropped: the bucket instance cannot be read again",
			slog.String("bucket", x.obj.Bucket.Name), slog.String("key", comp.c.Key.Name), slog.Any("error", err))
		return
	}
	x.rec, x.shard = rec, nil
	c := comp.c
	c.Locator = ""
	err = x.guardReshard(m.ctx, func(sh indexShard) error {
		_, werr := sh.pool.Write(m.ctx, sh.oid, x.completeOp(c), radosclient.OpFlagNone)
		return werr
	})
	if err != nil {
		slog.ErrorContext(m.ctx, "bucket index completion failed after a reshard; the pending entry is left for listing to reconcile",
			slog.String("bucket", x.obj.Bucket.Name), slog.String("key", c.Key.Name), slog.Any("error", err))
	}
}
