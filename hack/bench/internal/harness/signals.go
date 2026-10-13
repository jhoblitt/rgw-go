package harness

import (
	"context"
	"errors"
	"os"
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
	ctx, stop := WatchSignals(parent, sigs)
	return ctx, func() {
		signal.Stop(sigs)
		stop()
	}
}

// WatchSignals returns a copy of parent that the first signal sigs delivers
// cancels, with Interrupted as its cause. Its watcher ends with the copy.
func WatchSignals(parent context.Context, sigs <-chan os.Signal) (context.Context, context.CancelFunc) {
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

// InterruptStatus is the status a command exits with when a signal
// interrupted its run under ctx, as the shell reports one: 128 and the
// signal's number. It reports false when no signal did.
func InterruptStatus(ctx context.Context) (int, bool) {
	if in, ok := errors.AsType[Interrupted](context.Cause(ctx)); ok {
		if sig, ok := in.Signal.(syscall.Signal); ok {
			return 128 + int(sig), true
		}
	}
	return 0, false
}
