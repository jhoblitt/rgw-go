package cli_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/cli"
)

var _ = Describe("ServeAndRun", func() {
	It("ends the store's context only once the frontend has drained", func(ctx SpecContext) {
		sctx, cancel := context.WithCancel(ctx)
		defer cancel()
		g, gctx := errgroup.WithContext(sctx)
		drained := make(chan struct{})
		runEnded := make(chan struct{})
		cli.ServeAndRun(gctx, g,
			func(fctx context.Context) error {
				<-fctx.Done()
				<-drained
				return nil
			},
			func(rctx context.Context) error {
				<-rctx.Done()
				close(runEnded)
				return nil
			})

		cancel()
		Consistently(runEnded).WithTimeout(200*time.Millisecond).WithPolling(10*time.Millisecond).ShouldNot(BeClosed(),
			"the workers stop while the frontend drains")
		close(drained)
		Eventually(runEnded).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(BeClosed())
		Expect(g.Wait()).To(Succeed())
	})

	It("ends the frontend when the store's workers fail", func(ctx SpecContext) {
		g, gctx := errgroup.WithContext(ctx)
		errWorker := errors.New("worker failed")
		cli.ServeAndRun(gctx, g,
			func(fctx context.Context) error {
				<-fctx.Done()
				return nil
			},
			func(context.Context) error { return errWorker })
		done := make(chan error, 1)
		go func() { done <- g.Wait() }()
		Eventually(done).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(MatchError(errWorker)))
	})
})
