package goceph_test

import (
	"context"
	"math"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

// limiterTimeout fails a spec whose acquire never returns, which is how a
// limiter that stopped clamping or releasing would show, rather than hang it.
const limiterTimeout = 5 * time.Second

var _ = Describe("limiter", func() {
	It("admits up to the op limit and parks the next submission until a release", func(ctx SpecContext) {
		l := goceph.NewLimiter(2, 1<<20)
		Expect(l.Acquire(ctx, 10)).To(Succeed())
		Expect(l.Acquire(ctx, 10)).To(Succeed())
		parked := make(chan error, 1)
		go func() { parked <- l.Acquire(ctx, 10) }()
		Consistently(parked).WithTimeout(50*time.Millisecond).WithPolling(5*time.Millisecond).
			ShouldNot(Receive(), "third op must wait")
		l.Release(10)
		Eventually(parked).WithTimeout(time.Second).WithPolling(5*time.Millisecond).
			Should(Receive(Succeed()), "released op admits the waiter")
		Expect(l.Stats().ThrottleWaits).To(BeEquivalentTo(1), "one wait counted")
	}, SpecTimeout(limiterTimeout))

	It("parks on bytes when ops remain", func(ctx SpecContext) {
		l := goceph.NewLimiter(100, 1000)
		Expect(l.Acquire(ctx, 900)).To(Succeed())
		parked := make(chan error, 1)
		go func() { parked <- l.Acquire(ctx, 200) }()
		Consistently(parked).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).ShouldNot(Receive())
		l.Release(900)
		Eventually(parked).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(Succeed()))
		Expect(l.Stats().ThrottleWaits).To(BeEquivalentTo(1), "a wait for bytes counts too")
	}, SpecTimeout(limiterTimeout))

	It("counts a submission that parks for an op and then for bytes once", func(ctx SpecContext) {
		l := goceph.NewLimiter(2, 100)
		Expect(l.Acquire(ctx, 100)).To(Succeed(), "all the bytes")
		Expect(l.Acquire(ctx, 0)).To(Succeed(), "the last op")
		parked := make(chan error, 1)
		go func() { parked <- l.Acquire(ctx, 50) }()
		Consistently(parked).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).ShouldNot(Receive())
		l.Release(0)
		Consistently(parked).WithTimeout(50*time.Millisecond).WithPolling(5*time.Millisecond).
			ShouldNot(Receive(), "the op is free, the bytes are not")
		l.Release(100)
		Eventually(parked).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(Succeed()))
		Expect(l.Stats().ThrottleWaits).To(BeEquivalentTo(1))
	}, SpecTimeout(limiterTimeout))

	It("clamps a payload larger than the whole budget so it can never deadlock", func(ctx SpecContext) {
		l := goceph.NewLimiter(4, 1000)
		Expect(l.Acquire(ctx, 5000)).To(Succeed(), "oversized op takes the whole budget")
		Expect(l.Stats().InflightBytes).To(BeEquivalentTo(1000))
		l.Release(5000)
		Expect(l.Stats().InflightBytes).To(BeZero())
	}, SpecTimeout(limiterTimeout))

	It("returns the context error while parked", func(ctx SpecContext) {
		l := goceph.NewLimiter(1, 1000)
		Expect(l.Acquire(ctx, 1)).To(Succeed())
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		Expect(l.Acquire(short, 1)).To(MatchError(context.DeadlineExceeded))
		Expect(l.Stats().InflightOps).To(BeEquivalentTo(1), "a refused acquire holds nothing")
		Expect(l.Stats().InflightBytes).To(BeEquivalentTo(1))
	}, SpecTimeout(limiterTimeout))

	It("returns the context error while parked for bytes, releasing the op it took", func(ctx SpecContext) {
		l := goceph.NewLimiter(2, 1000)
		Expect(l.Acquire(ctx, 1000)).To(Succeed())
		short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		Expect(l.Acquire(short, 1)).To(MatchError(context.DeadlineExceeded))
		Expect(l.Stats().InflightOps).To(BeEquivalentTo(1), "a refused acquire holds nothing")
		Expect(l.Acquire(ctx, 0)).To(Succeed(), "the refused acquire's op is free again")
	}, SpecTimeout(limiterTimeout))

	It("derives its limits from the objecter options with a margin", func() {
		logs := captureLogs()
		ops, bytes := goceph.DeriveLimits(map[string]string{"objecter_inflight_ops": "1024", "objecter_inflight_op_bytes": "104857600"})
		Expect(ops).To(Equal(1008), "objecter_inflight_ops less 16 for the calls that take its budget around the limiter")
		Expect(bytes).To(BeEquivalentTo(98304000), "fifteen sixteenths of objecter_inflight_op_bytes")
		Expect(logs.String()).To(BeEmpty())
	})

	DescribeTable("falls back to librados's defaults, less the margin, for an option it cannot use, with one warning",
		func(opts map[string]string) {
			logs := captureLogs()
			ops, bytes := goceph.DeriveLimits(opts)
			Expect(ops).To(Equal(1008))
			Expect(bytes).To(BeEquivalentTo(98304000))
			Expect(strings.Split(strings.TrimSpace(logs.String()), "\n")).To(ConsistOf(
				ContainSubstring(`"msg":"cannot read the objecter throttle, sizing the in-flight limiter from its defaults"`)))
		},
		Entry("options librados cannot read", map[string]string{}),
		Entry("values that are not counts", map[string]string{"objecter_inflight_ops": "many", "objecter_inflight_op_bytes": "-1"}),
	)

	It("turns its limits off where librados's throttle is off", func() {
		ops, bytes := goceph.DeriveLimits(map[string]string{"objecter_inflight_ops": "0", "objecter_inflight_op_bytes": "0"})
		Expect(ops).To(Equal(math.MaxInt))
		Expect(bytes).To(BeEquivalentTo(math.MaxInt64))
	})

	It("keeps one operation and every byte when librados's limits are within the margin", func() {
		ops, bytes := goceph.DeriveLimits(map[string]string{"objecter_inflight_ops": "16", "objecter_inflight_op_bytes": "15"})
		Expect(ops).To(Equal(1))
		Expect(bytes).To(BeEquivalentTo(15))
	})
})
