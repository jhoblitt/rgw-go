package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// LookupScript is the bucket lookup's script, so that a fake toolbox can
// answer it.
const LookupScript = lookupScript

// LookUpBucket is the lookup of bucket that the removal of the bench objects
// and the versioned bucket's read-back make, through ex as the toolbox of the
// site realm r, zonegroup zg and zone z.
func LookUpBucket(ctx context.Context, ex harness.Execer, bucket string) (bool, error) {
	st := &state{
		cl:   harness.Cluster{Exec: ex, Rooket: "rooket", Release: "tentacle", CallTimeout: time.Minute},
		site: site{realm: "r", zonegroup: "zg", zone: "z"},
		out:  slog.New(slog.DiscardHandler),
	}
	return st.bucketExists(ctx, bucket)
}

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
