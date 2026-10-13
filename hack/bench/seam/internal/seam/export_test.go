package seam

import (
	"context"
	"io/fs"
	"strings"
	"time"
)

// RunWith is Run with adjust applied to the Runner before it runs, so that a
// spec can replace its seams with fakes.
var RunWith = run

// Watchdog is the Launcher that runs benchmark processes on this host, so
// that a spec can run its stop against a real process.
var Watchdog Launcher = watchdog{}

// Sample is one noise sample against proc at now, with sleep standing in for
// the wait across the window.
func Sample(ctx context.Context, proc fs.FS, now time.Time, window time.Duration,
	sleep func(context.Context, time.Duration) error,
) (string, error) {
	var b strings.Builder
	err := noise{proc: proc, now: func() time.Time { return now }, window: window, sleep: sleep}.sample(ctx, &b)
	return b.String(), err
}

// ParseStat is parseStat's comm and ticks, so that a spec can feed it a
// /proc/<pid>/stat line directly.
func ParseStat(stat string) (string, uint64, bool) {
	t, ok := parseStat(stat)
	return t.comm, t.ticks, ok
}
