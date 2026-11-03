// Package memlimit sets the Go runtime's soft memory limit from the memory
// limit of the process's cgroup. The runtime reads a cgroup's CPU limit but
// not its memory limit, and under GOGC's default it lets the heap grow to
// twice the live heap before collecting it, so under a container's
// memory.max a heap with no limit is OOM-killed where one with a limit would
// have been collected first. Only cgroup v2 is read.
package memlimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"strconv"
)

// DefaultRatio is the share of the cgroup's memory limit the Go runtime is
// given by default. The rest is for the memory the runtime does not count:
// the C heap, tcmalloc's, which holds librados's buffers, and the binary and
// its libraries.
const DefaultRatio = 0.8

var (
	// ErrOperatorLimit is a GOMEMLIMIT in the environment, which the runtime
	// applied at start.
	ErrOperatorLimit = errors.New("the operator's GOMEMLIMIT stands")
	// ErrDisabled is a ratio of 0.
	ErrDisabled = errors.New("the memory limit ratio is 0")
	// ErrNoCgroupV2 is a process whose memory no cgroup v2 controller
	// limits: cgroup v1, or a hybrid hierarchy whose memory controller is
	// v1's.
	ErrNoCgroupV2 = errors.New("no cgroup v2 memory controller")
	// ErrNoLimit is a cgroup v2 path whose every memory.max is max.
	ErrNoLimit = errors.New("no finite memory.max")
)

// ParseRatio parses s as the share of the cgroup's limit to give the Go
// runtime: a number in (0, 1], or 0 for none.
func ParseRatio(s string) (float64, error) {
	r, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(r) {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if r < 0 || r > 1 {
		return 0, fmt.Errorf("%q is outside 0 to 1", s)
	}
	return r, nil
}

// Sizer sets the Go runtime's soft memory limit from a cgroup's memory
// limit.
type Sizer struct {
	Cgroup Cgroup
	// LookupEnv reads the environment, as os.LookupEnv does.
	LookupEnv func(key string) (string, bool)
	// SetLimit sets the runtime's soft memory limit and returns the previous
	// one, only reading it for a negative limit, as debug.SetMemoryLimit
	// does.
	SetLimit func(limit int64) int64
}

// The running process's cgroup, environment and Go memory limit, which
// specs replace so that none reads or sets the test binary's own.
var (
	processCgroup = Self()
	processEnv    = os.LookupEnv
	processLimit  = debug.SetMemoryLimit
)

// ForProcess is the running process's Sizer.
func ForProcess() Sizer {
	return Sizer{Cgroup: processCgroup, LookupEnv: processEnv, SetLimit: processLimit}
}

// Result is what Size did.
type Result struct {
	// Cgroup is the cgroup's memory limit, zero when Size did not read one.
	Cgroup Limit
	Ratio  float64
	// Limit is the runtime's soft memory limit once Size returns: the one it
	// set, or the one that stood.
	Limit int64
	// Reason is why Size set no limit, nil when it set one.
	Reason error
}

// Size sets the runtime's soft memory limit to ratio, which ParseRatio
// accepts, times the cgroup's memory limit, rounded down to a byte, and logs
// one line saying what it did. It sets none when GOMEMLIMIT is in the
// environment, for a ratio of 0, or when no cgroup v2 memory limit covers
// the process. A cgroup file it cannot read or parse is logged as a warning
// and never returned, so that it never stops a start.
func (s Sizer) Size(ctx context.Context, ratio float64) Result {
	r := Result{Ratio: ratio}
	// The runtime takes an empty GOMEMLIMIT as unset
	// (runtime/mgcpacer.go:1414-1418 at go1.27.3).
	if v, ok := s.LookupEnv("GOMEMLIMIT"); ok && v != "" {
		r.Reason = ErrOperatorLimit
	} else if ratio == 0 {
		r.Reason = ErrDisabled
	} else {
		r.Cgroup, r.Reason = s.Cgroup.Read()
	}
	if r.Reason == nil {
		r.Limit = scale(r.Cgroup.Bytes, ratio)
		s.SetLimit(r.Limit)
	} else {
		r.Limit = s.SetLimit(-1)
	}
	r.log(ctx)
	return r
}

// scale is ratio times n rounded down, held at math.MaxInt64, which float64
// rounds up to 2^63.
func scale(n int64, ratio float64) int64 {
	f := math.Floor(float64(n) * ratio)
	if f >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(f)
}

func (r Result) log(ctx context.Context) {
	var attrs []slog.Attr
	if r.Cgroup.File != "" {
		attrs = append(attrs, slog.String("cgroup_file", r.Cgroup.File), slog.Int64("cgroup_limit_bytes", r.Cgroup.Bytes))
	}
	attrs = append(attrs, slog.Float64("ratio", r.Ratio), slog.Int64("memory_limit_bytes", r.Limit))
	if r.Reason == nil {
		slog.LogAttrs(ctx, slog.LevelInfo, "go memory limit set from the cgroup", attrs...)
		return
	}
	level := slog.LevelWarn
	for _, expected := range []error{ErrOperatorLimit, ErrDisabled, ErrNoCgroupV2, ErrNoLimit} {
		if errors.Is(r.Reason, expected) {
			level = slog.LevelInfo
		}
	}
	slog.LogAttrs(ctx, level, "go memory limit not set from the cgroup", append(attrs, slog.String("reason", r.Reason.Error()))...)
}
