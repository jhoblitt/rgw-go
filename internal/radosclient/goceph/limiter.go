package goceph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync/atomic"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The defaults of librados's objecter throttle, objecter_inflight_ops and
// objecter_inflight_op_bytes in src/common/options/global.yaml.in.
const (
	objecterOpsDefault   = 1024
	objecterBytesDefault = 100 << 20
)

// opsMargin is how many of the objecter's operations the limiter leaves to
// the calls that take its budget without passing the limiter: a lock call
// holds one while it runs and an object listing one for the whole listing.
// Watch and notify register linger operations, which take none.
const opsMargin = 16

// limiter is the in-flight limiter: two weighted semaphores, operations and
// payload bytes, sized under librados's objecter throttle so a submission
// parks a goroutine here instead of an OS thread inside C. Once the objecter's
// budget is spent, Objecter::_op_submit_with_budget blocks the submitting
// thread in Throttle::get until another operation completes, and a blocked
// cgo call pins its thread.
type limiter struct {
	ops, bytes  *semaphore.Weighted
	maxBytes    int64
	inflightOps atomic.Int64
	inflightB   atomic.Int64
	waits       atomic.Uint64
}

func newLimiter(maxOps int, maxBytes int64) *limiter {
	return &limiter{
		ops:      semaphore.NewWeighted(int64(maxOps)),
		bytes:    semaphore.NewWeighted(maxBytes),
		maxBytes: maxBytes,
	}
}

// acquire takes one operation and n bytes, parking on ctx. n is clamped to
// the byte budget: x/sync's Acquire of more than the semaphore holds waits
// for ctx alone, so an oversized op takes the whole budget instead. acquire
// holds nothing when it returns an error.
func (l *limiter) acquire(ctx context.Context, n int64) error {
	n = min(n, l.maxBytes)
	parked := false
	if !l.ops.TryAcquire(1) {
		parked = true
		l.waits.Add(1)
		if err := l.ops.Acquire(ctx, 1); err != nil {
			return err
		}
	}
	if !l.bytes.TryAcquire(n) {
		if !parked {
			l.waits.Add(1)
		}
		if err := l.bytes.Acquire(ctx, n); err != nil {
			l.ops.Release(1)
			return err
		}
	}
	l.inflightOps.Add(1)
	l.inflightB.Add(n)
	return nil
}

// release returns what acquire took for n bytes. The in-flight counts drop
// before the semaphores admit anyone else, so they never exceed the limits.
func (l *limiter) release(n int64) {
	n = min(n, l.maxBytes)
	l.inflightOps.Add(-1)
	l.inflightB.Add(-n)
	l.bytes.Release(n)
	l.ops.Release(1)
}

// stats reports the operations and bytes the limiter holds and how many
// submissions parked on it.
func (l *limiter) stats() radosclient.Stats {
	return radosclient.Stats{
		InflightOps:   l.inflightOps.Load(),
		InflightBytes: l.inflightB.Load(),
		ThrottleWaits: l.waits.Load(),
	}
}

// deriveLimits sizes the limiter from librados's own throttle, less opsMargin
// operations and one sixteenth of the bytes. The byte margin absorbs what
// librados budgets and weight leaves out: xattr and omap payloads and xattr
// comparisons (Objecter::calc_op_budget). An option librados cannot report
// as a count falls back to its default, with one warning for both. A limit
// of 0 turns librados's throttle off (Throttle::get admits everything under a
// zero maximum), so it turns the limiter's off too.
func deriveLimits(ctx context.Context, get func(string) (string, error)) (ops int, bytes int64) {
	maxOps, opsErr := objecterLimit(get, "objecter_inflight_ops", objecterOpsDefault)
	maxBytes, bytesErr := objecterLimit(get, "objecter_inflight_op_bytes", objecterBytesDefault)
	if err := errors.Join(opsErr, bytesErr); err != nil {
		slog.WarnContext(ctx, "cannot read the objecter throttle, sizing the in-flight limiter from its defaults",
			slog.Any("error", err))
	}

	switch {
	case maxOps == 0 || maxOps > math.MaxInt:
		ops = math.MaxInt
	case maxOps <= opsMargin:
		ops = 1
	default:
		ops = int(maxOps) - opsMargin
	}
	switch {
	case maxBytes == 0 || maxBytes > math.MaxInt64:
		bytes = math.MaxInt64
	default:
		bytes = int64(maxBytes)
		bytes -= bytes / 16
	}
	return ops, bytes
}

// objecterLimit reads the named count through get, returning def and the
// reason when it cannot.
func objecterLimit(get func(string) (string, error), name string, def uint64) (uint64, error) {
	v, err := get(name)
	if err != nil {
		return def, err
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return def, fmt.Errorf("goceph: %s: %w", name, err)
	}
	return n, nil
}

// weight is an op's payload: read lengths, write and append data, exec
// inputs. It saturates rather than wrapping.
func weight(steps []radosclient.Step) int64 {
	var total int64
	for _, s := range steps {
		var n int64
		switch s := s.(type) {
		case *radosclient.ReadStep:
			if s.Length > math.MaxInt64 {
				return math.MaxInt64
			}
			n = int64(s.Length)
		case *radosclient.WriteFullStep:
			n = int64(len(s.Data))
		case *radosclient.WriteStep:
			n = int64(len(s.Data))
		case *radosclient.AppendStep:
			n = int64(len(s.Data))
		case *radosclient.ExecStep:
			n = int64(len(s.In))
		}
		if n > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += n
	}
	return total
}

// counter counts the operations submitted in one direction and their
// payload bytes.
type counter struct {
	ops, bytes atomic.Uint64
}

// add counts one operation of n payload bytes.
func (c *counter) add(n int64) {
	c.ops.Add(1)
	if n > 0 {
		c.bytes.Add(uint64(n))
	}
}
