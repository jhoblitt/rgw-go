package cli_test

import (
	"context"
	"log"
	"log/slog"
	"os"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"

	"github.com/jhoblitt/rgw-go/internal/cli"
)

// debugLogs sends slog's default logger, at debug level, to a buffer until
// the spec ends. slog.SetDefault also points the log package's output at the
// new handler, and restoring the old default logger leaves it there, so the
// cleanup puts log's writer and flags back too.
func debugLogs() *gbytes.Buffer {
	logs := gbytes.NewBuffer()
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return logs
}

// watch runs WatchSignals over a channel the spec sends on, until the
// returned context ends; done closes when WatchSignals returns.
func watch(ctx context.Context) (sigs chan<- os.Signal, wctx context.Context, done <-chan struct{}) {
	ch := make(chan os.Signal, 1)
	wctx, cancel := context.WithCancel(ctx)
	DeferCleanup(cancel)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		cli.WatchSignals(wctx, ch, cancel)
	}()
	return ch, wctx, finished
}

var _ = Describe("NotifyContext", func() {
	It("takes SIGHUP, on which radosgw reopens its logs, without ending, and logs it", func(ctx SpecContext) {
		logs := debugLogs()
		sctx, stop := cli.NotifyContext(ctx)
		DeferCleanup(stop)

		// Uncaught, SIGHUP ends this process.
		Expect(syscall.Kill(os.Getpid(), syscall.SIGHUP)).To(Succeed())
		Eventually(logs).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).
			Should(gbytes.Say(`"level":"DEBUG","msg":"ignoring signal","signal":"hangup"`))
		Expect(sctx.Err()).NotTo(HaveOccurred())
	})

	It("ends when its stop function runs", func(ctx SpecContext) {
		sctx, stop := cli.NotifyContext(ctx)
		stop()
		Expect(sctx.Done()).To(BeClosed())
	})
})

var _ = Describe("WatchSignals", func() {
	It("keeps serving on SIGUSR1, for which radosgw's SIGTERM handler skips the shutdown, and logs it", func(ctx SpecContext) {
		logs := debugLogs()
		sigs, wctx, _ := watch(ctx)
		sigs <- syscall.SIGUSR1
		Eventually(logs).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).
			Should(gbytes.Say(`"level":"DEBUG","msg":"ignoring signal","signal":"user defined signal 1"`))
		Expect(wctx.Err()).NotTo(HaveOccurred())
	})

	DescribeTable("ends on the signals radosgw stops on, and on no other",
		func(ctx SpecContext, sig os.Signal) {
			sigs, wctx, done := watch(ctx)
			sigs <- syscall.SIGHUP
			sigs <- syscall.SIGUSR1
			Consistently(wctx.Done()).WithTimeout(50*time.Millisecond).WithPolling(10*time.Millisecond).
				ShouldNot(BeClosed(), "SIGHUP and SIGUSR1 end nothing")
			sigs <- sig
			Eventually(wctx.Done()).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(BeClosed())
			Eventually(done).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(BeClosed())
		},
		Entry("SIGTERM", syscall.SIGTERM),
		Entry("SIGINT", os.Interrupt),
	)
})
