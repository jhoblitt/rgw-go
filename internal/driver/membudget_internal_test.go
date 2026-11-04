package driver

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// draw is one acquire run in the background, its share and error delivered
// on done.
type draw struct {
	share int64
	err   error
	done  chan struct{}
}

func startDraw(ctx context.Context, b *memBudget, c memClass, starting bool, n uint64) *draw {
	d := &draw{done: make(chan struct{})}
	go func() {
		defer close(d.done)
		d.share, d.err = b.acquire(ctx, c, starting, n)
	}()
	return d
}

func (d *draw) finished() bool {
	select {
	case <-d.done:
		return true
	default:
		return false
	}
}

var _ = Describe("the memory budget", func() {
	// A 16-byte budget: holds take at most 8, prefetches 4, and 4 is always
	// left to pieces.
	var b *memBudget
	BeforeEach(func() { b = newMemBudget(16, 0) })

	held := func() int64 {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.total
	}
	// waiting is how many draws wait in every queue.
	waiting := func() int {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.continuing) + len(b.starting) + len(b.holds)
	}
	take := func(ctx context.Context, c memClass, starting bool, n uint64) int64 {
		GinkgoHelper()
		w, err := b.acquire(ctx, c, starting, n)
		Expect(err).NotTo(HaveOccurred())
		return w
	}
	// fill spends the whole budget on pieces, each at most the quarter
	// pieces always keep.
	fill := func(ctx context.Context) {
		GinkgoHelper()
		for range 4 {
			take(ctx, memPiece, false, 4)
		}
	}
	// queued starts a draw and waits until it is queued.
	queued := func(ctx context.Context, c memClass, starting bool, n uint64) *draw {
		GinkgoHelper()
		before := waiting()
		d := startDraw(ctx, b, c, starting, n)
		Eventually(waiting).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(before+1), "the draw waits")
		return d
	}

	It("grants a piece that fits at once, and makes one that does not wait until room frees", func(ctx SpecContext) {
		for range 4 {
			Expect(take(ctx, memPiece, false, 4)).To(BeEquivalentTo(4))
		}
		d := queued(ctx, memPiece, false, 4)
		Consistently(d.finished).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeFalse())
		b.release(memPiece, 4)
		Eventually(d.done).WithTimeout(time.Second).Should(BeClosed())
		Expect(d.err).NotTo(HaveOccurred())
		Expect(held()).To(BeEquivalentTo(16))
	})

	It("serves waiting continuing pieces first in, first out", func(ctx SpecContext) {
		fill(ctx)
		first := queued(ctx, memPiece, false, 4)
		second := queued(ctx, memPiece, false, 4)
		b.release(memPiece, 4)
		Eventually(first.done).WithTimeout(time.Second).Should(BeClosed())
		Consistently(second.finished).WithTimeout(50*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeFalse(),
			"the later waiter waits for the next room")
	})

	It("serves a continuing piece before a starting one that waited longer", func(ctx SpecContext) {
		fill(ctx)
		starting := queued(ctx, memPiece, true, 4)
		continuing := queued(ctx, memPiece, false, 4)
		b.release(memPiece, 4)
		Eventually(continuing.done).WithTimeout(time.Second).Should(BeClosed())
		Consistently(starting.finished).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeFalse())
	})

	It("serves the earlier of a waiting first piece and a waiting hold", func(ctx SpecContext) {
		fill(ctx)
		piece := queued(ctx, memPiece, true, 4)
		hold := queued(ctx, memHold, true, 4)
		b.release(memPiece, 4)
		Eventually(piece.done).WithTimeout(time.Second).Should(BeClosed())
		Consistently(hold.finished).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeFalse())
	})

	It("does not let a hold the hold cap stops block a first piece behind it", func(ctx SpecContext) {
		take(ctx, memHold, true, 8)
		hold := queued(ctx, memHold, true, 1)
		Expect(take(ctx, memPiece, true, 4)).To(BeEquivalentTo(4), "a piece that fits is granted at once")
		Consistently(hold.finished).WithTimeout(50*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeFalse(),
			"the hold waits for the hold cap, not for room")
	})

	It("caps holds at half the budget and prefetches at a quarter, keeping a quarter for pieces", func(ctx SpecContext) {
		Expect(take(ctx, memHold, true, 8)).To(BeEquivalentTo(8))
		w, ok := b.tryPrefetch(4)
		Expect(ok).To(BeTrue())
		Expect(w).To(BeEquivalentTo(4))
		_, ok = b.tryPrefetch(1)
		Expect(ok).To(BeFalse(), "the prefetch cap is spent")
		Expect(take(ctx, memPiece, false, 4)).To(BeEquivalentTo(4), "the largest piece fits with both caps spent")
		b.release(memPiece, 4)
		b.release(memPrefetch, 4)
		hold := queued(ctx, memHold, true, 1)
		Consistently(hold.finished).WithTimeout(50*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeFalse(),
			"room for it, but the hold cap is spent")
		b.release(memHold, 8)
		Eventually(hold.done).WithTimeout(time.Second).Should(BeClosed())
	})

	It("clamps an oversized draw to the most its class can hold, so it proceeds alone", func(ctx SpecContext) {
		Expect(take(ctx, memPiece, false, 100)).To(BeEquivalentTo(4), "a piece takes the quarter pieces always keep")
		Expect(take(ctx, memHold, true, 100)).To(BeEquivalentTo(8), "a hold takes the hold cap")
	})

	It("refuses a prefetch while any draw waits, and never makes one wait", func(ctx SpecContext) {
		fill(ctx)
		queued(ctx, memPiece, false, 4)
		b.release(memPiece, 2)
		_, ok := b.tryPrefetch(1)
		Expect(ok).To(BeFalse(), "room for the prefetch, but a piece waits")
		Expect(b.stats().PrefetchSkipped).To(BeEquivalentTo(1))
	})

	It("answers SlowDown for a starting draw past its deadline and holds nothing", func(ctx SpecContext) {
		b = newMemBudget(16, 20*time.Millisecond)
		fill(ctx)
		_, err := b.acquire(ctx, memPiece, true, 4)
		Expect(err).To(MatchError(op.ErrSlowDown))
		_, err = b.acquire(ctx, memHold, true, 4)
		Expect(err).To(MatchError(op.ErrSlowDown))
		Expect(held()).To(BeEquivalentTo(16))
		Expect(waiting()).To(BeZero())
		Expect(b.stats()).To(And(HaveField("PieceSlowdowns", BeEquivalentTo(1)), HaveField("HoldSlowdowns", BeEquivalentTo(1))))
	}, SpecTimeout(5*time.Second))

	It("lets a continuing draw wait past the deadline", func(ctx SpecContext) {
		b = newMemBudget(16, 20*time.Millisecond)
		fill(ctx)
		d := queued(ctx, memPiece, false, 4)
		Consistently(d.finished).WithTimeout(100 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeFalse())
		b.release(memPiece, 4)
		Eventually(d.done).WithTimeout(time.Second).Should(BeClosed())
		Expect(d.err).NotTo(HaveOccurred())
	})

	It("waits with no deadline for as long as the context lives", func(ctx SpecContext) {
		fill(ctx)
		dctx, cancel := context.WithCancel(ctx)
		d := queued(dctx, memPiece, true, 4)
		Consistently(d.finished).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeFalse())
		cancel()
		Eventually(d.done).WithTimeout(time.Second).Should(BeClosed())
		Expect(d.err).To(MatchError(context.Canceled))
		Expect(held()).To(BeEquivalentTo(16))
		Expect(waiting()).To(BeZero(), "the canceled draw left its queue")
	})

	It("gives back a share granted as its draw was canceled", func(ctx SpecContext) {
		for range 200 {
			b = newMemBudget(16, 0)
			fill(ctx)
			dctx, cancel := context.WithCancel(ctx)
			d := queued(dctx, memPiece, false, 4)
			var wg sync.WaitGroup
			wg.Go(func() { b.release(memPiece, 4) })
			wg.Go(cancel)
			wg.Wait()
			<-d.done
			if d.err == nil {
				b.release(memPiece, d.share)
			}
			b.release(memPiece, 12)
			Expect(held()).To(BeZero(), "nothing kept, whichever won")
		}
	})

	It("counts the draws waiting and those that waited, and the time they waited", func(ctx SpecContext) {
		fill(ctx)
		next := queued(ctx, memPiece, false, 4)
		first := queued(ctx, memPiece, true, 4)
		hold := queued(ctx, memHold, true, 4)
		Expect(b.stats()).To(And(HaveField("PieceWaiting", BeEquivalentTo(2)), HaveField("HoldWaiting", BeEquivalentTo(1))),
			"a transfer's first piece waits as a piece, as its next one does")
		for _, d := range []*draw{next, first, hold} {
			b.release(memPiece, 4)
			<-d.done
		}
		st := b.stats()
		Expect(st).To(And(HaveField("PieceWaits", BeEquivalentTo(2)), HaveField("HoldWaits", BeEquivalentTo(1)),
			HaveField("PieceWaiting", BeZero()), HaveField("HoldWaiting", BeZero()),
			HaveField("PieceHeld", BeEquivalentTo(12)), HaveField("HoldHeld", BeEquivalentTo(4)), HaveField("Size", BeEquivalentTo(16)),
			HaveField("HoldCap", BeEquivalentTo(8)), HaveField("PrefetchCap", BeEquivalentTo(4))))
		Expect(st.PieceWaited).To(BeNumerically(">", 0))
		Expect(st.HoldWaited).To(BeNumerically(">", 0))
	})

	It("gives a prefetch share back once, by its release or when its context ends", func(ctx SpecContext) {
		w, ok := b.tryPrefetch(4)
		Expect(ok).To(BeTrue())
		rctx, cancel := context.WithCancel(ctx)
		release := b.prefetchRelease(rctx, w)
		release()
		release()
		Expect(held()).To(BeZero())
		w, _ = b.tryPrefetch(4)
		b.prefetchRelease(rctx, w)
		cancel()
		Eventually(held).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeZero(), "the request ended")
		Expect(b.prefetchRelease(ctx, 0)).To(BeNil(), "no share, no release")
	})

	Describe("a stage", func() {
		It("takes its hold as its first draw, and refuses one after", func(ctx SpecContext) {
			st := b.newStage()
			Expect(st.hold(ctx, 0)).To(Succeed(), "a hold of nothing is no draw")
			Expect(st.hold(ctx, 4)).To(Succeed())
			_, err := st.piece(ctx, 4)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.hold(ctx, 4)).To(MatchError(errSecondHold))
			Expect(errSecondHold).To(MatchError(op.ErrInternalError))
		})

		It("refuses a hold after a piece", func(ctx SpecContext) {
			st := b.newStage()
			_, err := st.piece(ctx, 4)
			Expect(err).NotTo(HaveOccurred())
			Expect(errors.Is(st.hold(ctx, 4), errSecondHold)).To(BeTrue())
		})

		It("draws its first piece as a starting one and the rest as continuing ones", func(ctx SpecContext) {
			b = newMemBudget(16, 20*time.Millisecond)
			fill(ctx)
			_, err := b.newStage().piece(ctx, 4)
			Expect(err).To(MatchError(op.ErrSlowDown), "a first piece has the deadline")
			st := b.newStage()
			st.servedHead()
			d := make(chan error, 1)
			go func() {
				_, err := st.piece(ctx, 4)
				d <- err
			}()
			Consistently(d).WithTimeout(60*time.Millisecond).WithPolling(5*time.Millisecond).ShouldNot(Receive(),
				"a stage that served a prefetched head continues")
			b.release(memPiece, 4)
			Eventually(d).WithTimeout(time.Second).Should(Receive(BeNil()))
		}, SpecTimeout(5*time.Second))

		It("draws nothing under a covering stage's hold, and for covered read pieces", func(ctx SpecContext) {
			Expect(b.coveredStage(false).hold(ctx, 8)).To(Succeed())
			w, err := b.coveredStage(true).piece(ctx, 4)
			Expect(err).NotTo(HaveOccurred())
			Expect(w).To(BeZero())
			Expect(held()).To(BeZero())
			w, err = b.coveredStage(false).piece(ctx, 4)
			Expect(err).NotTo(HaveOccurred())
			Expect(w).To(BeEquivalentTo(4), "a covered writer's pieces are its own")
		})

		It("gives its hold back when done and counts the pieces it holds", func(ctx SpecContext) {
			st := b.newStage()
			Expect(st.hold(ctx, 8)).To(Succeed())
			w, err := st.piece(ctx, 4)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.pieces.Load()).To(BeEquivalentTo(4))
			st.give(w)
			st.done()
			Expect(st.pieces.Load()).To(BeZero())
			Expect(held()).To(BeZero())
		})
	})
})

var _ = Describe("the memory budget's size", func() {
	ram := func(n uint64, err error) func() (uint64, error) {
		return func() (uint64, error) { return n, err }
	}
	DescribeTable("from the Go memory limit",
		func(ctx SpecContext, limit int64, ratio float64, floor int64, physical func() (uint64, error), want int64) {
			Expect(memoryBudgetSize(ctx, limit, ratio, floor, physical)).To(Equal(want))
		},
		Entry("none asked for", int64(0), 0.0, int64(0), ram(1000, nil), int64(math.MaxInt64)),
		Entry("half the limit by default", int64(1000), 0.0, int64(0), ram(0, nil), int64(500)),
		Entry("the ratio given", int64(1000), 0.25, int64(0), ram(0, nil), int64(250)),
		Entry("80% of physical memory standing in for no limit", int64(math.MaxInt64), 0.0, int64(0), ram(1000, nil), int64(400)),
		Entry("unbounded when physical memory cannot be read", int64(math.MaxInt64), 0.0, int64(0), ram(0, errors.New("no sysinfo")), int64(math.MaxInt64)),
		Entry("the floor when the share is smaller", int64(100), 0.0, int64(300), ram(0, nil), int64(300)),
	)

	It("reads physical memory from sysinfo", func() {
		n, err := physicalMemory()
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeNumerically(">", 0))
	})
})
