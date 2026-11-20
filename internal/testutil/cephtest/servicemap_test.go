package cephtest_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

var _ = Describe("ServiceDeparture", func() {
	// The bounds keep the production ones' order, close < re-registered,
	// with a settle and a poll far below both, scaled to milliseconds.
	const (
		closeBound = 500 * time.Millisecond
		againBound = 1500 * time.Millisecond
		settle     = 100 * time.Millisecond
		poll       = 10 * time.Millisecond
	)
	var (
		ours, again cephtest.ServiceRegistration
		departure   cephtest.ServiceDeparture
	)

	BeforeEach(func() {
		ours = cephtest.ServiceRegistration{StartEpoch: 7, StartStamp: "2026-11-20T04:43:30.100000+0000", Raw: `{"start_epoch":7}`}
		again = cephtest.ServiceRegistration{StartEpoch: 9, StartStamp: "2026-11-20T04:44:15.185000+0000", Raw: `{"start_epoch":9}`}
		departure = cephtest.ServiceDeparture{
			Who: "the spec's", Key: "4242", Registered: ours, Exit: "it stopped", Exited: time.Now(),
		}
	})

	// listing answers each poll from a timeline of what the map lists,
	// measured from the exit: the entry of the first span that has not
	// ended, nil within a span with no entry, and after the last span.
	type span struct {
		until time.Duration
		entry *cephtest.ServiceRegistration
	}
	listing := func(spans ...span) func(Gomega) *cephtest.ServiceRegistration {
		return func(Gomega) *cephtest.ServiceRegistration {
			since := time.Since(departure.Exited)
			for _, s := range spans {
				if since < s.until {
					return s.entry
				}
			}
			return nil
		}
	}
	await := func(ctx context.Context, read func(Gomega) *cephtest.ServiceRegistration) *cephtest.ServiceRegistration {
		return departure.AwaitWithin(ctx, read, closeBound, againBound, settle, poll)
	}
	reportEntry := func() types.ReportEntry {
		entries := CurrentSpecReport().ReportEntries
		Expect(entries).To(HaveLen(1))
		return entries[0]
	}

	It("records the close when the registration leaves and nothing follows it", func(ctx SpecContext) {
		got := await(ctx, listing(span{100 * time.Millisecond, &ours}))

		Expect(got).To(BeNil())
		Expect(time.Since(departure.Exited)).To(BeNumerically(">=", 100*time.Millisecond+settle), "the absence must hold for the settle")
		Expect(reportEntry().Name).To(Equal("the spec's registration left the map by its close"))
		Expect(reportEntry().Value.String()).To(And(
			ContainSubstring("after it stopped"), ContainSubstring("no other registration under 4242 in the 100ms after")))
	})

	It("records the entry registered again, even when a poll first found the key empty", func(ctx SpecContext) {
		got := await(ctx, listing(
			span{50 * time.Millisecond, &ours},
			span{90 * time.Millisecond, nil},
			span{800 * time.Millisecond, &again},
		))

		Expect(got).To(HaveValue(Equal(again)))
		Expect(reportEntry().Name).To(Equal("the spec's entry was registered again as it stopped"))
		Expect(reportEntry().Value.String()).To(And(
			ContainSubstring("start_epoch 9, start_stamp 2026-11-20T04:44:15.185000+0000"),
			ContainSubstring("where it was start_epoch 7, start_stamp 2026-11-20T04:43:30.100000+0000"),
			ContainSubstring("after it stopped"),
			ContainSubstring(again.Raw)))
	})

	It("fails, naming the close's path and the registration, once the registration outlasts the close's bound", func(ctx SpecContext) {
		failure := InterceptGomegaFailures(func() { await(ctx, listing(span{time.Hour, &ours})) })

		Expect(failure).To(ConsistOf(And(
			ContainSubstring("the close did not remove the spec's registration under 4242"),
			ContainSubstring("start_epoch 7, start_stamp 2026-11-20T04:43:30.100000+0000"),
			ContainSubstring("after it stopped, past 500ms"),
			ContainSubstring(ours.Raw))))
		Expect(time.Since(departure.Exited)).To(BeNumerically("<", againBound), "the close's bound, not the re-registered one's")
		Expect(CurrentSpecReport().ReportEntries).To(BeEmpty())
	})

	It("fails, naming the re-registration's path and its registration, once the entry outlasts the beacon grace's bound", func(ctx SpecContext) {
		// A settle of 1 s puts the backstop 1 s past the bound, so a stalled
		// poll under -race cannot let it fire first.
		failure := InterceptGomegaFailures(func() {
			departure.AwaitWithin(ctx, listing(span{50 * time.Millisecond, &ours}, span{time.Hour, &again}),
				closeBound, againBound, time.Second, poll)
		})

		Expect(failure).To(ConsistOf(And(
			ContainSubstring("the spec's entry under 4242 was registered again"),
			ContainSubstring("start_epoch 9, start_stamp 2026-11-20T04:44:15.185000+0000"),
			ContainSubstring("where it was start_epoch 7"),
			ContainSubstring("after it stopped, past 1.5s"),
			ContainSubstring(again.Raw))))
		Expect(time.Since(departure.Exited)).To(BeNumerically(">=", againBound))
		Expect(CurrentSpecReport().ReportEntries).To(BeEmpty())
	})

	It("times the close's bound from the exit, not from the call", func(ctx SpecContext) {
		departure.Exited = time.Now().Add(-2 * closeBound)
		called := time.Now()
		failure := InterceptGomegaFailures(func() {
			await(ctx, func(Gomega) *cephtest.ServiceRegistration { return &ours })
		})

		Expect(failure).To(ConsistOf(ContainSubstring("the close did not remove the spec's registration under 4242")))
		Expect(time.Since(called)).To(BeNumerically("<", closeBound), "a bound timed from the call would need its full length")
	})

	It("times the re-registered bound from the exit, not from the call", func(ctx SpecContext) {
		departure.Exited = time.Now().Add(-againBound - closeBound)
		called := time.Now()
		failure := InterceptGomegaFailures(func() {
			await(ctx, func(Gomega) *cephtest.ServiceRegistration { return &again })
		})

		Expect(failure).To(ConsistOf(ContainSubstring("the spec's entry under 4242 was registered again")))
		Expect(time.Since(called)).To(BeNumerically("<", closeBound), "a bound timed from the call would need its full length")
	})

	It("counts an absence only over reads that succeed", func(ctx SpecContext) {
		departure.Exited = time.Now()
		const failing = 3 * settle
		got := await(ctx, func(g Gomega) *cephtest.ServiceRegistration {
			if since := time.Since(departure.Exited); since >= 3*poll && since < failing {
				g.Expect("service dump").To(BeEmpty(), "the reader's own failure")
			}
			return nil
		})

		Expect(got).To(BeNil())
		Expect(time.Since(departure.Exited)).To(BeNumerically(">=", failing+settle),
			"the settle must restart at the first read that succeeds after the failures")
	})

	It("reports the entry gone from the first read that found it absent, across a restarted settle", func(ctx SpecContext) {
		departure.Exited = time.Now()
		got := await(ctx, func(g Gomega) *cephtest.ServiceRegistration {
			if since := time.Since(departure.Exited); since >= 5*poll && since < 4*settle {
				g.Expect("service dump").To(BeEmpty(), "the reader's own failure")
			}
			return nil
		})

		Expect(got).To(BeNil())
		Expect(reportEntry().Value.String()).To(HavePrefix("0s after it stopped"),
			"the settle restarted after the failed reads, but the entry left at the first read")
	})

	It("fails, naming the last successful read and the failed reads, when the reads stop succeeding", func(ctx SpecContext) {
		departure.Exited = time.Now()
		failure := InterceptGomegaFailures(func() {
			await(ctx, func(g Gomega) *cephtest.ServiceRegistration {
				if time.Since(departure.Exited) >= 10*poll {
					g.Expect("service dump").To(BeEmpty(), "the reader's own failure")
				}
				return &ours
			})
		})

		Expect(failure).To(ConsistOf(And(
			MatchRegexp(`the last successful read, [0-9.]+m?s after it stopped, finding start_epoch 7`),
			ContainSubstring(ours.Raw),
			MatchRegexp(`; [1-9][0-9]* of [1-9][0-9]* reads failed`),
			ContainSubstring("the reader's own failure"))))
		Expect(CurrentSpecReport().ReportEntries).To(BeEmpty())
	})

	It("fails, saying no read succeeded, when the map cannot be read", func(ctx SpecContext) {
		failure := InterceptGomegaFailures(func() {
			await(ctx, func(g Gomega) *cephtest.ServiceRegistration {
				g.Expect("service dump").To(BeEmpty(), "the reader's own failure")
				return nil
			})
		})

		Expect(failure).To(ConsistOf(And(
			ContainSubstring("the spec's entry under 4242"),
			ContainSubstring("no other registration seen; no read succeeded"),
			MatchRegexp(`; ([1-9][0-9]*) of ([1-9][0-9]*) reads failed`),
			ContainSubstring("the reader's own failure"))))
		Expect(CurrentSpecReport().ReportEntries).To(BeEmpty())
	})
})
