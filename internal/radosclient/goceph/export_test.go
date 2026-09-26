package goceph

import (
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
