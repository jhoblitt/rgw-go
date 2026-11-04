package driver

import (
	"bytes"
	"fmt"
	"math"
	"runtime"
	"slices"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// trimTimers is the free list's timer factory for the specs: it records the
// trims armed and those stopped, and a spec fires them by hand.
type trimTimers struct {
	mu      sync.Mutex
	pending []*trimTimer
	periods []time.Duration
	stops   int
}

type trimTimer struct{ f func() }

func (t *trimTimers) afterFunc(d time.Duration, f func()) func() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	tt := &trimTimer{f: f}
	t.pending = append(t.pending, tt)
	t.periods = append(t.periods, d)
	return func() bool {
		t.mu.Lock()
		defer t.mu.Unlock()
		i := slices.Index(t.pending, tt)
		if i < 0 {
			return false
		}
		t.pending = slices.Delete(t.pending, i, i+1)
		t.stops++
		return true
	}
}

// armed is the trims armed and neither fired nor stopped.
func (t *trimTimers) armed() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

// take is the one trim armed, as its timer hands it to the goroutine that
// runs it: stopping the timer no longer reaches it.
func (t *trimTimers) take() func() {
	GinkgoHelper()
	t.mu.Lock()
	defer t.mu.Unlock()
	Expect(t.pending).To(HaveLen(1), "one trim is armed")
	f := t.pending[0].f
	t.pending = nil
	return f
}

// fire runs the one trim armed, as its timer would.
func (t *trimTimers) fire() {
	GinkgoHelper()
	trim := t.take()
	trim()
}

var _ = Describe("the free list", func() {
	var (
		timers *trimTimers
		frees  int
	)
	BeforeEach(func() {
		timers = &trimTimers{}
		frees = 0
	})
	lists := func(limit int64) *bufLists {
		l := newBufLists(limit)
		l.afterFunc = timers.afterFunc
		l.freeOSMemory = func() { frees++ }
		return l
	}
	block := func(n int) *[]byte { return new(make([]byte, n)) }
	// lendBack puts p as a block l lent.
	lendBack := func(l *bufLists, p *[]byte) {
		l.mu.Lock()
		l.lent += int64(cap(*p))
		l.mu.Unlock()
		l.put(p)
	}
	// fill lends back k blocks of n bytes, returned in the order put.
	fill := func(l *bufLists, k, n int) []*[]byte {
		out := make([]*[]byte, k)
		for i := range out {
			out[i] = block(n)
			lendBack(l, out[i])
		}
		return out
	}
	// idleOf is the blocks of n bytes l holds, oldest first.
	idleOf := func(l *bufLists, n uint64) []*[]byte {
		l.mu.Lock()
		defer l.mu.Unlock()
		var out []*[]byte
		if bl := l.lists[n]; bl != nil {
			for _, b := range bl.blocks {
				out = append(out, b.p)
			}
		}
		return out
	}
	idle := func(l *bufLists, n uint64) int { return len(idleOf(l, n)) }
	lent := func(l *bufLists) int64 {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.lent
	}

	It("hands back the block put last first", func() {
		l := lists(1 << 20)
		put := fill(l, 3, 8)
		for i := 2; i >= 0; i-- {
			Expect(l.get(8)).To(BeIdenticalTo(put[i]), "block %d", i)
		}
		fresh := l.get(8)
		for i, p := range put {
			Expect(fresh).NotTo(BeIdenticalTo(p), "an empty list allocates rather than lend block %d again", i)
		}
		Expect(*fresh).To(HaveLen(8))
	})

	It("keeps a list for each block size, and hands out a block whole", func() {
		l := lists(1 << 20)
		small, large := block(8), block(16)
		*large = (*large)[:3]
		lendBack(l, large)
		lendBack(l, small)
		Expect(l.get(16)).To(BeIdenticalTo(large), "the last block of 16 bytes, though one of 8 went back after it")
		Expect(*large).To(HaveLen(16), "the length its last holder left is undone")
		other := l.get(16)
		Expect(other).NotTo(BeIdenticalTo(small), "a list of another size lends nothing")
		Expect(*other).To(HaveCap(16))
		Expect(l.get(8)).To(BeIdenticalTo(small))
	})

	It("drops a block put past the cap, which counts the idle bytes of every size", func() {
		l := lists(32)
		put := fill(l, 3, 8)
		lendBack(l, block(16))
		Expect(l.idleBytes()).To(BeEquivalentTo(24), "16 more bytes would take the lists past 32")
		Expect(idle(l, 16)).To(BeZero())
		last := block(8)
		lendBack(l, last)
		lendBack(l, block(8))
		Expect(l.idleBytes()).To(BeEquivalentTo(32))
		Expect(l.get(8)).To(BeIdenticalTo(last), "the block past the cap was dropped, not kept")
		Expect(l.get(8)).To(BeIdenticalTo(put[2]))
	})

	It("lends no block to two holders at once under concurrent gets and puts", func() {
		const workers, rounds, n = 8, 500, 64
		l := lists(16 * n)
		var wg sync.WaitGroup
		errs := make([]error, workers)
		for w := range workers {
			wg.Go(func() {
				mark := bytes.Repeat([]byte{byte(w + 1)}, n)
				for range rounds {
					p := l.get(n)
					copy(*p, mark)
					runtime.Gosched()
					if !bytes.Equal(*p, mark) {
						errs[w] = fmt.Errorf("worker %d's block holds another holder's bytes", w)
						return
					}
					l.put(p)
				}
			})
		}
		wg.Wait()
		Expect(errs).To(HaveEach(Succeed()))
		Expect(l.idleBytes()).To(BeNumerically("<=", 16*n))
		Expect(l.idleBytes()).To(BeEquivalentTo(idle(l, n) * n))
		Expect(lent(l)).To(BeZero(), "every block lent came back")
	})

	It("counts what it lends until it is put back or forgotten", func() {
		l := lists(1 << 20)
		kept, lost := l.get(8), l.get(16)
		Expect(lent(l)).To(BeEquivalentTo(24))
		l.put(kept)
		l.forget(lost)
		Expect(lent(l)).To(BeZero(), "a block that will not come back is no longer lent")
		Expect(l.idleBytes()).To(BeEquivalentTo(8), "and is not kept")
	})

	Describe("past the budget", func() {
		It("drops idle blocks of other sizes, past the floor, until idle and lent fit the budget again", func() {
			l := lists(64)
			put := fill(l, bufTrimFloor, 8)
			held := []*[]byte{l.get(16), l.get(16)}
			Expect(idle(l, 8)).To(Equal(4), "32 lent and 32 idle fit 64")
			held = append(held, l.get(16))
			Expect(idleOf(l, 8)).To(Equal(put[2:]), "the two oldest went for the 16 bytes lent past 64")
			held = append(held, l.get(16))
			Expect(idle(l, 8)).To(BeZero(), "the floor protects none of them")
			Expect(l.idleBytes() + lent(l)).To(BeEquivalentTo(64))
			held = append(held, l.get(16))
			Expect(*held[4]).To(HaveLen(16), "with nothing idle left, a get past the budget still allocates")
			Expect(l.idleBytes()+lent(l)).To(BeEquivalentTo(80), "the total is what is lent, past the budget")
		})

		It("drops the oldest idle block first, whatever its size", func() {
			l := lists(32)
			older, younger, small := block(8), block(8), block(4)
			lendBack(l, older)
			lendBack(l, small)
			lendBack(l, younger)
			l.get(16)
			Expect(idleOf(l, 8)).To(Equal([]*[]byte{younger}), "16 lent and 20 idle are past 32: the block of 8 put first went")
			Expect(idleOf(l, 4)).To(Equal([]*[]byte{small}))
			Expect(l.idleBytes() + lent(l)).To(BeEquivalentTo(28))
		})

		It("drops a block put back while what is lent is past the budget, and keeps one once idle and lent fit", func() {
			l := lists(64)
			held := make([]*[]byte, 5)
			for i := range held {
				held[i] = l.get(16)
			}
			Expect(lent(l)).To(BeEquivalentTo(80))
			l.put(held[0])
			Expect(idle(l, 16)).To(BeZero(), "64 lent and 16 idle would pass 64")
			Expect(l.idleBytes()+lent(l)).To(BeEquivalentTo(64), "within max(64, 64)")
			l.put(held[1])
			Expect(idleOf(l, 16)).To(Equal([]*[]byte{held[1]}), "48 lent and 16 idle fit 64")
			Expect(l.idleBytes() + lent(l)).To(BeEquivalentTo(64))
		})

		It("keeps a block it never lent under an unbounded limit", func() {
			l := lists(math.MaxInt64)
			p := block(8)
			l.put(p)
			Expect(idleOf(l, 8)).To(Equal([]*[]byte{p}), "a lent count below zero would overflow the limit's headroom and drop it")
		})
	})

	Describe("trim", func() {
		It("trims an idle list once, down to the floor, from the bottom, then arms nothing", func() {
			l := lists(1 << 20)
			put := fill(l, 10, 8)
			Expect(timers.periods).To(Equal([]time.Duration{bufTrimPeriod}), "the first put armed one trim")
			timers.fire()
			Expect(idle(l, 8)).To(Equal(10), "the list started the interval empty")
			Expect(frees).To(BeZero())
			timers.fire()
			Expect(idle(l, 8)).To(Equal(bufTrimFloor))
			Expect(l.idleBytes()).To(BeEquivalentTo(bufTrimFloor * 8))
			Expect(frees).To(Equal(1), "the heap went back to the OS once")
			Expect(timers.armed()).To(BeZero(), "an idle list arms no further trim")
			for i := 9; i >= 10-bufTrimFloor; i-- {
				Expect(l.get(8)).To(BeIdenticalTo(put[i]), "the blocks put last are kept: block %d", i)
			}
			Expect(timers.periods).To(HaveEach(Equal(120 * time.Second)))
		})

		It("keeps the working set of a list in steady use, and drops only blocks below the interval's low-water mark", func() {
			l := lists(1 << 20)
			fill(l, 10, 8)
			timers.fire()
			use := func() {
				taken := []*[]byte{l.get(8), l.get(8), l.get(8)}
				for _, p := range taken {
					l.put(p)
				}
			}
			use()
			timers.fire()
			Expect(idle(l, 8)).To(Equal(3+bufTrimFloor), "of the 7 blocks no get reached, all but the floor went")
			Expect(frees).To(Equal(1))
			use()
			timers.fire()
			Expect(idle(l, 8)).To(Equal(3+bufTrimFloor), "the working set of 3 and the floor stay")
			Expect(frees).To(Equal(1), "nothing more was dropped")
			Expect(timers.armed()).To(Equal(1), "a list in use keeps a trim armed")
		})

		It("trims each size's list by its own low-water mark", func() {
			l := lists(1 << 20)
			fill(l, 6, 8)
			fill(l, 9, 16)
			timers.fire()
			a, b := l.get(16), l.get(16)
			l.put(b)
			l.put(a)
			timers.fire()
			Expect(idle(l, 8)).To(Equal(bufTrimFloor), "no get reached the list of 8")
			Expect(idle(l, 16)).To(Equal(2+bufTrimFloor), "two of the list of 16 were in use")
			Expect(l.idleBytes()).To(BeEquivalentTo(bufTrimFloor*8 + (2+bufTrimFloor)*16))
		})

		It("leaves the heap alone when nothing is dropped", func() {
			l := lists(1 << 20)
			fill(l, bufTrimFloor, 8)
			timers.fire()
			timers.fire()
			Expect(idle(l, 8)).To(Equal(bufTrimFloor))
			Expect(frees).To(BeZero(), "a list at its floor drops nothing")
			Expect(timers.armed()).To(BeZero())
		})

		It("arms the trim again on a get or put after an idle trim", func() {
			l := lists(1 << 20)
			fill(l, 6, 8)
			timers.fire()
			timers.fire()
			Expect(timers.armed()).To(BeZero())
			p := l.get(8)
			Expect(timers.armed()).To(Equal(1), "the get armed a trim")
			l.put(p)
			Expect(timers.armed()).To(Equal(1), "a trim pending is not armed twice")
			timers.fire()
			Expect(timers.armed()).To(Equal(1), "the put used the lists during the interval")
		})

		It("stops a closed list's pending trim, and arms none after", func() {
			l := lists(1 << 20)
			fill(l, 10, 8)
			Expect(timers.armed()).To(Equal(1))
			l.close()
			Expect(timers.armed()).To(BeZero(), "close stopped the trim")
			Expect(timers.stops).To(Equal(1))
			l.put(l.get(8))
			Expect(timers.armed()).To(BeZero(), "a closed list arms no trim")
			Expect(idle(l, 8)).To(Equal(10))
		})

		It("does nothing in a trim whose timer fired as its list closed", func() {
			l := lists(1 << 20)
			fill(l, 10, 8)
			timers.fire()
			trim := timers.take()
			l.close()
			trim()
			Expect(idle(l, 8)).To(Equal(10), "nothing was dropped")
			Expect(frees).To(BeZero())
			Expect(timers.armed()).To(BeZero(), "nothing was armed")
		})

		It("stops the pending trim when its Store closes", func() {
			s := NewStoreForTest(nil, ZoneForTest{}, ReadConfigForTest{Chunk: 8, MaxReq: 8, Window: 32})
			s.bufs = lists(1 << 20)
			s.bufs.put(s.bufs.get(8))
			Expect(timers.armed()).To(Equal(1))
			Expect(s.Close()).To(Succeed())
			Expect(timers.armed()).To(BeZero(), "the closed Store's trim never fires")
			Expect(timers.stops).To(Equal(1))
		})

		It("lets lists whose owner is gone be collected with a trim pending", func() {
			collected := make(chan struct{})
			func() {
				l := lists(1 << 20)
				lendBack(l, block(8))
				runtime.AddCleanup(l, func(ch chan struct{}) { close(ch) }, collected)
			}()
			Expect(timers.armed()).To(Equal(1))
			Eventually(func() chan struct{} {
				runtime.GC()
				return collected
			}).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(BeClosed())
			timers.fire()
			Expect(frees).To(BeZero(), "the trim of lists already collected does nothing")
		})
	})
})
