package goceph

import (
	"context"
	"fmt"
	"sync"

	"github.com/ceph/go-ceph/rados"
)

// reaper owns the completions of operations whose caller gave up on them.
type reaper struct {
	wg sync.WaitGroup
}

// reap releases c once librados reports it done, then calls after. Release
// blocks on Done, so the goroutine stops exactly when librados finishes the
// operation; until then the completion keeps the operation's buffers pinned.
func (r *reaper) reap(c *rados.AioCompletion, after func()) {
	r.wg.Go(func() {
		c.Release()
		after()
	})
}

// wait blocks until every reaped completion has been released.
func (r *reaper) wait() { r.wg.Wait() }

// await waits for c or for ctx. It returns nil once c is done, leaving the
// caller to copy the results, Release c and call after. When ctx ends first
// it hands c and after to the reaper and returns ctx.Err(); the caller must
// touch neither again.
func (r *reaper) await(ctx context.Context, c *rados.AioCompletion, after func()) error {
	select {
	case <-c.Done():
		return nil
	case <-ctx.Done():
		// Prefer a completion that raced the cancellation: its results are
		// already there.
		select {
		case <-c.Done():
			return nil
		default:
		}
		r.reap(c, after)
		return ctx.Err()
	}
}

// aioModes arbitrates go-ceph's process-wide completion notifier between the
// open clusters that use an asynchronous mode.
var aioModes struct {
	mu    sync.Mutex
	mode  Mode
	users int
}

// acquireAioMode selects m's notifier for the process, failing while another
// open cluster uses the other asynchronous mode. ModeSync needs no notifier.
func acquireAioMode(m Mode) error {
	if m == ModeSync {
		return nil
	}
	aioModes.mu.Lock()
	defer aioModes.mu.Unlock()
	if aioModes.users > 0 && aioModes.mode != m {
		return fmt.Errorf("goceph: completion mode %q requested while %d cluster(s) use %q; the notifier is process-wide",
			m, aioModes.users, aioModes.mode)
	}
	if aioModes.mode != m {
		if m == ModePipe {
			rados.SetAioMode(rados.AioModePipe)
		} else {
			rados.SetAioMode(rados.AioModeCallback)
		}
		aioModes.mode = m
	}
	aioModes.users++
	return nil
}

// releaseAioMode drops a cluster's hold on the notifier acquireAioMode took.
func releaseAioMode(m Mode) {
	if m == ModeSync {
		return
	}
	aioModes.mu.Lock()
	defer aioModes.mu.Unlock()
	aioModes.users--
	if aioModes.users == 0 {
		// Back to go-ceph's default, which stops a pipe notifier's goroutine
		// and closes its pipe; every completion has been released by now.
		rados.SetAioMode(rados.AioModeCallback)
		aioModes.mode = ""
	}
}
