package seam

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Interrupted is the cause of a run's context when a signal interrupted the
// run.
type Interrupted struct {
	Signal os.Signal
}

func (i Interrupted) Error() string { return "signal received: " + i.Signal.String() }

// NotifyContext returns a copy of parent that the first SIGINT or SIGTERM
// cancels with Interrupted as its cause, and the function that stops watching
// for them.
func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	ctx, stop := watchSignals(parent, sigs)
	return ctx, func() {
		signal.Stop(sigs)
		stop()
	}
}

// watchSignals returns a copy of parent that the first signal sigs delivers
// cancels, with Interrupted as its cause. Its watcher ends with the copy.
func watchSignals(parent context.Context, sigs <-chan os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	go func() {
		select {
		case sig := <-sigs:
			cancel(Interrupted{Signal: sig})
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(nil) }
}

// ExitStatus is the status the command exits with after a run under ctx
// returned err, as the shell reports one: 128 and the signal's number when a
// signal interrupted the run; else a failed benchmark process's own, 128 and
// the signal's number for one a signal ended; 1 for any other failure; 0 for
// none.
func ExitStatus(ctx context.Context, err error) int {
	if err == nil {
		return 0
	}
	if in, ok := errors.AsType[Interrupted](context.Cause(ctx)); ok {
		if sig, ok := in.Signal.(syscall.Signal); ok {
			return 128 + int(sig)
		}
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
