package gateway

import (
	"context"
	"log/slog"
	"time"
)

// RunWith is Run with adjust applied to the Runner before it runs, so that a
// spec can replace its seams with fakes.
var RunWith = run

// SampleLine is sampleLine, so that a spec can feed it a process's stat line
// and status directly.
var SampleLine = sampleLine

// NoDelay is noDelay, so that a spec can read the TCP_NODELAY of frontends.
var NoDelay = noDelay

// KeepGCTrace is keepGCTrace, so that a spec can feed it a log of its own.
var KeepGCTrace = keepGCTrace

// Sample samples target into path until stop closes or ctx ends, finding its
// pid through pid and reading it through read, so that a spec can drive the
// sampler with a process and a clock of its own.
func Sample(ctx context.Context, stop <-chan struct{}, path, target string,
	pid func(context.Context) (string, error), read func(context.Context, string) ([]byte, error),
	now func() time.Time, every time.Duration, log *slog.Logger,
) error {
	return sampler{target: target, pid: pid, read: read, now: now, every: every, log: log}.run(ctx, stop, path)
}
