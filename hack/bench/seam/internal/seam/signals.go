package seam

import (
	"context"
	"errors"
	"os/exec"
	"syscall"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// ExitStatus is the status the command exits with after a run under ctx
// returned err, as the shell reports one: 128 and the signal's number when a
// signal interrupted the run; else a failed benchmark process's own, 128 and
// the signal's number for one a signal ended; 1 for any other failure; 0 for
// none.
func ExitStatus(ctx context.Context, err error) int {
	if err == nil {
		return 0
	}
	if status, ok := harness.InterruptStatus(ctx); ok {
		return status
	}
	pe, ok := errors.AsType[*processError](err)
	if !ok {
		return 1
	}
	exitErr, ok := errors.AsType[*exec.ExitError](pe.err)
	if !ok {
		return 1
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return max(exitErr.ExitCode(), 1)
}
