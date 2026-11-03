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
// weight's bytes, sized under librados's objecter throttle so a submission
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
// operations and one sixteenth of the bytes. weight charges every op at least
// what librados budgets it, and librados budgets no bytes to the calls that
// pass around the limiter, a lock's class call and a listing's PGNLS, so the
// byte margin is slack. An option librados cannot report as a count falls
// back to its default, with one warning for both. A limit of 0 turns
// librados's throttle off (Throttle::get admits everything under a zero
// maximum), so it turns the limiter's off too.
func deriveLimits(ctx context.Context, get func(string) (string, error)) (ops int, bytes int64) {
	maxOps, opsErr := objecterLimit(get, "objecter_inflight_ops", objecterOpsDefault)
	maxBytes, bytesErr := objecterLimit(get, "objecter_inflight_op_bytes", objecterBytesDefault)
	if err := errors.Join(opsErr, bytesErr); err != nil {
		slog.WarnContext(ctx, "cannot read the objecter throttle, sizing the in-flight limiter from its defaults",
			slog.Any("error", err))
	}

	// A count past what an int holds on every platform is no limit in
	// practice, so it is taken as none.
	ops = math.MaxInt
	if maxOps != 0 && maxOps <= math.MaxInt32 {
		ops = max(int(maxOps)-opsMargin, 1)
	}
	bytes = math.MaxInt64
	if maxBytes != 0 && maxBytes <= math.MaxInt64 {
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

// weight is the bytes an op takes from the limiter: what librados's
// Objecter::calc_op_budget charges it, and its exec inputs. calc_op_budget
// charges a write-mode step its indata, a read-mode step its extent's length
// or, for an xattr step, its name and value lengths, and a class call nothing,
// since ceph_osd_op_mode_read leaves CEPH_OSD_OP_CALL out of the read mode
// (https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.cc#L3502-L3518,
// https://github.com/ceph/ceph/blob/v19.2.7/src/osdc/Objecter.cc#L3338-L3354).
// Building an exec copies its input into librados's memory all the same, so
// weight counts it, and the limiter's byte budget bounds those copies. It
// saturates rather than wrapping.
func weight(steps []radosclient.Step) int64 {
	return sumSteps(steps, stepWeight)
}

// payload is the bytes the Stats counters count for an op: read lengths,
// write and append data, exec inputs.
func payload(steps []radosclient.Step) int64 {
	return sumSteps(steps, func(s radosclient.Step) int64 {
		switch s.(type) {
		case *radosclient.ReadStep, *radosclient.WriteFullStep, *radosclient.WriteStep,
			*radosclient.AppendStep, *radosclient.ExecStep:
			return stepWeight(s)
		}
		return 0
	})
}

// sumSteps adds f over steps, saturating rather than wrapping.
func sumSteps(steps []radosclient.Step, f func(radosclient.Step) int64) int64 {
	var total int64
	for _, s := range steps {
		n := f(s)
		if n > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += n
	}
	return total
}

// stepWeight is one step's share of weight, from what librados's C op
// builders put in each kind's step through Objecter's add_data, add_xattr and
// add_xattr_cmp
// (https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.h#L176-L208,
// the same code at v19.2.7). It panics on a kind it does not know, so that a
// step kind added to the seam cannot weigh 0 unnoticed.
func stepWeight(s radosclient.Step) int64 {
	switch s := s.(type) {
	case *radosclient.ReadStep:
		// librados charges a length past math.MaxInt64, negative as an
		// int64, nothing; readStep cannot make a buffer that long anyway.
		if s.Length > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(s.Length)
	case *radosclient.WriteFullStep:
		return int64(len(s.Data))
	case *radosclient.WriteStep:
		return int64(len(s.Data))
	case *radosclient.AppendStep:
		return int64(len(s.Data))
	case *radosclient.ExecStep:
		return int64(len(s.In))
	case *radosclient.SetXattrStep:
		// add_xattr's indata is the name, then the value.
		return int64(len(s.Name)) + int64(len(s.Value))
	case *radosclient.RmXattrStep:
		return int64(len(s.Name))
	case *radosclient.CmpXattrStep:
		// CMPXATTR is a read-mode xattr step, in a write op too.
		return int64(len(s.Name)) + int64(len(s.Value))
	case *radosclient.OmapSetStep:
		// omap_set's indata
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.h#L1359-L1363)
		// is a std::map<std::string, bufferlist> as container_base encodes it,
		// a uint32 count and then each entry
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/include/denc.h#L969-L979).
		// Each key and each value is a uint32 length and its bytes, as
		// denc_traits encodes a std::basic_string
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/include/denc.h#L718-L737)
		// and a ceph::buffer::list
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/include/denc.h#L826-L838).
		// The code is the same at v19.2.7.
		n := int64(4)
		for k, v := range s.Values {
			n += 4 + int64(len(k)) + 4 + int64(len(v))
		}
		return n
	case *radosclient.OmapRmKeysStep:
		// omap_rm_keys's indata is the encoded std::set<std::string>
		// librados builds from the keys, which holds a repeated key once
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_c.cc#L3925-L3938,
		// the same at v19.2.7).
		n := int64(4)
		seen := make(map[string]struct{}, len(s.Keys))
		for _, k := range s.Keys {
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			n += 4 + int64(len(k))
		}
		return n
	case *radosclient.CreateStep, *radosclient.RemoveStep, *radosclient.ZeroStep,
		*radosclient.TruncateStep, *radosclient.OmapClearStep, *radosclient.SetAllocHintStep:
		// Write-mode steps whose indata is empty; a zero's length is an
		// extent, which calc_op_budget reads only in a read-mode step.
		return 0
	case *radosclient.StatStep, *radosclient.AssertExistsStep, *radosclient.AssertVersionStep,
		*radosclient.GetXattrsStep, *radosclient.OmapGetValsStep, *radosclient.OmapGetValsByKeysStep,
		*radosclient.OmapGetKeysStep, *radosclient.OmapCmpStep:
		// Read-mode steps that are neither an extent nor an xattr with
		// lengths.
		return 0
	case *radosclient.StepFlagsStep:
		// It sets the flags of the step before it and adds none.
		return 0
	}
	panic(fmt.Sprintf("goceph: no weight for a %T step", s))
}

// counter counts the operations submitted in one direction and their
// payload bytes.
type counter struct {
	ops, bytes atomic.Uint64
}

// add counts one operation of steps and its payload. It takes the steps
// rather than a byte count so that the limiter's weight, which counts more,
// cannot be passed for the payload.
func (c *counter) add(steps []radosclient.Step) {
	c.ops.Add(1)
	if n := payload(steps); n > 0 {
		c.bytes.Add(uint64(n))
	}
}
