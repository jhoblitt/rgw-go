package goceph

import (
	"context"
	"syscall"

	"github.com/ceph/go-ceph/rados"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// TranslateRead drives the read translator against b and discards the finishers.
func TranslateRead(b readBuilder, op *radosclient.ReadOp) error {
	_, err := translateRead(b, op)
	return err
}

// TranslateWrite drives the write translator against b and discards the finishers.
func TranslateWrite(b writeBuilder, op *radosclient.WriteOp) error {
	_, err := translateWrite(b, op)
	return err
}

// TranslateFlags exposes the op-flag mapping, dropping the error the specs never trigger.
func TranslateFlags(f radosclient.OpFlags) rados.OperationFlags {
	out, err := translateFlags(f)
	if err != nil {
		panic(err)
	}
	return out
}

// ToSeamError exposes the go-ceph to seam error mapping.
var ToSeamError = toSeamError

// StepError exposes the per-step error an operation outcome yields for the
// go-ceph step at index i.
func StepError(name string, err error, i int) error { return newOutcome(name, err).step(i) }

// CompleteWrite translates op against b, completes it as if go-ceph's
// Operate or AioCompletion.Err had returned err, and returns the error Write
// would.
func CompleteWrite(b writeBuilder, op *radosclient.WriteOp, err error) error {
	fs, terr := translateWrite(b, op)
	if terr != nil {
		return terr
	}
	o := newOutcome("write obj", err)
	finish(fs, o)
	return o.err
}

// WatchStopped returns a channel closed once w's dispatch goroutine exits.
func WatchStopped(w radosclient.Watch) <-chan struct{} {
	gw, ok := w.(*watch)
	if !ok {
		panic("not a goceph watch")
	}
	return gw.stopped
}

// ReleaseFor exposes the require_osd_release to denc.Release mapping.
var ReleaseFor = releaseFor

// WithDefaults exposes Config's defaulting.
func WithDefaults(c Config) Config { return c.withDefaults() }

// CheckLibradosLine exposes the refusal of a librados line older than Squid.
var CheckLibradosLine = checkLibradosLine

// ExplainConnect exposes how a failed connect is annotated with the
// librados version's AES256KRB5 floor.
var ExplainConnect = explainConnect

// LibradosVersion reports the version of the librados the process runs.
func LibradosVersion() string { return libradosVersion() }

// SetLibradosVersion makes Connect see v as the librados version until the
// returned restore runs.
func SetLibradosVersion(v string) (restore func()) {
	old := libradosVersion
	libradosVersion = func() string { return v }
	return func() { libradosVersion = old }
}

// Limiter exposes the in-flight limiter.
type Limiter struct{ l *limiter }

// NewLimiter returns a limiter admitting maxOps operations and maxBytes
// payload bytes at once.
func NewLimiter(maxOps int, maxBytes int64) Limiter { return Limiter{newLimiter(maxOps, maxBytes)} }

// Acquire takes one operation and n bytes, parking on ctx.
func (l Limiter) Acquire(ctx context.Context, n int64) error { return l.l.acquire(ctx, n) }

// Release returns what Acquire took for n bytes.
func (l Limiter) Release(n int64) { l.l.release(n) }

// Stats reports what the limiter holds and how often a submission parked.
func (l Limiter) Stats() radosclient.Stats { return l.l.stats() }

// DeriveLimits sizes a limiter from the objecter options in opts, as Connect
// does from librados's; an option opts lacks is one librados cannot read.
func DeriveLimits(opts map[string]string) (ops int, bytes int64) {
	return deriveLimits(context.Background(), func(name string) (string, error) {
		v, ok := opts[name]
		if !ok {
			return "", &radosclient.Error{Errno: int32(syscall.ENOENT), Op: "config get " + name}
		}
		return v, nil
	})
}

// Weight exposes an op's payload weight.
var Weight = weight

// ConfiguredOption reads one option through ConfiguredOptions.
func ConfiguredOption(ctx context.Context, cfg Config, option string) (string, error) {
	vals, err := ConfiguredOptions(ctx, cfg, option)
	return vals[option], err
}
