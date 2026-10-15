package cli

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// NotifyContext returns a copy of ctx that ends on SIGINT or SIGTERM, which
// stop radosgw
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L134-L135,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_signal.cc#L84-L85 at v19.2.6
// and v20.2.4), and takes SIGHUP and SIGUSR1 without ending. radosgw reopens
// its log files on SIGHUP
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L127,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_signal.cc#L40-L45), and
// rgw-go writes only to stderr. radosgw sends SIGUSR1 to its own frontend threads to
// wake them
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_frontend.cc#L104), so its
// SIGTERM handler, which it also registers for SIGUSR1
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L136), skips the
// shutdown for that signal
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_signal.cc#L84). Its stop
// function ends the copy and the notification, after which the signals act as they
// did before.
func NotifyContext(ctx context.Context) (context.Context, context.CancelFunc) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1)
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchSignals(ctx, sigs, cancel)
	}()
	return ctx, func() {
		signal.Stop(sigs)
		cancel()
		<-done
	}
}

// watchSignals logs each SIGHUP and SIGUSR1 and calls cancel on any other
// signal from sigs. It returns then, or when ctx ends.
func watchSignals(ctx context.Context, sigs <-chan os.Signal, cancel context.CancelFunc) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-sigs:
			if sig == syscall.SIGHUP || sig == syscall.SIGUSR1 {
				slog.DebugContext(ctx, "ignoring signal", slog.String("signal", sig.String()))
				continue
			}
			slog.InfoContext(ctx, "stopping", slog.String("signal", sig.String()))
			cancel()
			return
		}
	}
}
