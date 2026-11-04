package driver

import (
	"runtime/debug"
	"slices"
	"sync"
	"time"
	"weak"
)

// The free list's idle trim, in one place.
const (
	// bufTrimPeriod is the idle trim's interval, the Go runtime's two-minute
	// forced-GC period.
	bufTrimPeriod = 120 * time.Second
	// bufTrimFloor is the blocks of each size the trim leaves idle, so that
	// the first requests after a quiet spell allocate none.
	bufTrimFloor = 4
)

// bufLists is the free list of the buffers of object data: one list for
// each block size, which the head, read and tail buffers share. get pops the
// block handed back last, LIFO, so the blocks in use stay those whose pages
// are resident; put pushes one. The lists count the bytes they have lent,
// and keep their idle bytes and those lent within limit, the memory budget's
// size: a block put past it is dropped, and a get that allocates past it
// drops idle blocks of other sizes. They never refuse a get: the budget
// bounds what is in use, and a get from an empty list allocates, so the
// lists never take the total past the larger of limit and what is lent.
//
// An idle trim gives back what goes unused. A get or put arms a one-shot
// timer of bufTrimPeriod when none is pending. When it fires, each list
// drops the blocks below the fewest it held over the interval, which no get
// reached, down to bufTrimFloor, and the heap goes back to the OS if any
// were dropped. It re-arms only if the lists were used during the interval,
// so an idle gateway trims once and then sleeps.
type bufLists struct {
	limit int64
	// afterFunc runs f once after d, as time.AfterFunc does, and returns what
	// stops it; freeOSMemory returns the heap to the OS. Specs replace both,
	// and onDraw, when set, sees every block a get hands out again.
	afterFunc    func(d time.Duration, f func()) (stop func() bool)
	freeOSMemory func()
	onDraw       func(p *[]byte)

	mu    sync.Mutex
	lists map[uint64]*bufList
	// idle is the bytes the lists hold, lent those get handed out that
	// neither put nor forget has taken back, and seq the count of puts, which
	// orders the idle blocks by age.
	idle, lent int64
	seq        uint64
	// armed is a trim pending, stop what stops its timer, and used a get or
	// put since it was armed, other than the one that armed it; closed lists
	// arm no trim.
	armed, used, closed bool
	stop                func() bool
}

// bufList is the idle blocks of one size, the last handed back at the end,
// and lowWater the fewest it has held since the last trim.
type bufList struct {
	blocks   []idleBlock
	lowWater int
}

// idleBlock is a block put back, and when, as the lists' put count.
type idleBlock struct {
	p   *[]byte
	seq uint64
}

// newBufLists is a free list that keeps its idle and lent bytes within
// limit.
func newBufLists(limit int64) *bufLists {
	return &bufLists{
		limit:        limit,
		afterFunc:    func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
		freeOSMemory: debug.FreeOSMemory,
	}
}

// get returns a block of n bytes: the last one of that size handed back, or
// a new one.
func (l *bufLists) get(n uint64) *[]byte {
	w := toInt64(n)
	l.mu.Lock()
	l.touchLocked()
	l.lent += w
	if bl := l.lists[n]; bl != nil && len(bl.blocks) > 0 {
		last := len(bl.blocks) - 1
		p := bl.blocks[last].p
		bl.blocks[last] = idleBlock{}
		bl.blocks = bl.blocks[:last]
		bl.lowWater = min(bl.lowWater, last)
		l.idle -= w
		if l.onDraw != nil {
			l.onDraw(p)
		}
		l.mu.Unlock()
		*p = (*p)[:n]
		return p
	}
	l.shedLocked(n)
	l.mu.Unlock()
	return new(make([]byte, n))
}

// shedLocked drops idle blocks of sizes other than n, oldest first, until
// the idle and lent bytes together are within limit or nothing idle is
// left; a get of n bytes that allocates calls it. The floor protects none
// of them: it is for an idle gateway, not a full one. With one block size,
// as at the defaults, the lists hold nothing idle when a get allocates, and
// nothing is dropped.
func (l *bufLists) shedLocked(n uint64) {
	for l.lent > l.limit-l.idle {
		var (
			oldest *bufList
			size   uint64
		)
		for m, bl := range l.lists {
			if m != n && len(bl.blocks) > 0 && (oldest == nil || bl.blocks[0].seq < oldest.blocks[0].seq) {
				oldest, size = bl, m
			}
		}
		if oldest == nil {
			return
		}
		oldest.blocks = slices.Delete(oldest.blocks, 0, 1)
		oldest.lowWater = max(0, oldest.lowWater-1)
		l.idle -= toInt64(size)
	}
}

// put hands p back to the list of its capacity, or drops it when the idle
// bytes and those lent would pass limit with it.
func (l *bufLists) put(p *[]byte) {
	n := uint64(cap(*p))
	w := toInt64(n)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.touchLocked()
	l.lent -= w
	// A block the list never lent takes lent below zero, which under an
	// unbounded limit would overflow the headroom and drop every block.
	if w > l.limit-l.idle-max(l.lent, 0) {
		return
	}
	bl := l.lists[n]
	if bl == nil {
		if l.lists == nil {
			l.lists = make(map[uint64]*bufList)
		}
		bl = &bufList{}
		l.lists[n] = bl
	}
	l.seq++
	bl.blocks = append(bl.blocks, idleBlock{p: p, seq: l.seq})
	l.idle += w
}

// forget takes p, which get lent, out of the lent bytes without keeping it:
// its op was abandoned to its context, or its holder dropped it.
func (l *bufLists) forget(p *[]byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lent -= toInt64(uint64(cap(*p)))
}

// touchLocked records a get or put, arming the trim when none is pending.
func (l *bufLists) touchLocked() {
	switch {
	case l.closed:
	case l.armed:
		l.used = true
	default:
		l.armed = true
		l.armLocked()
	}
}

// armLocked starts the trim's timer. The timer holds the lists weakly, so
// that a pending trim does not keep the blocks of lists whose Store was
// dropped without Close live for a period, as a spec's would be.
func (l *bufLists) armLocked() {
	wp := weak.Make(l)
	l.stop = l.afterFunc(bufTrimPeriod, func() {
		if l := wp.Value(); l != nil {
			l.trim()
		}
	})
}

// trim drops the blocks of each list that no get reached since the last
// trim, from the bottom, down to bufTrimFloor; it returns the heap to the OS
// if it dropped any, and re-arms if the lists were used meanwhile.
func (l *bufLists) trim() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	dropped := false
	for n, bl := range l.lists {
		if k := bl.lowWater - bufTrimFloor; k > 0 {
			bl.blocks = slices.Delete(bl.blocks, 0, k)
			l.idle -= int64(k) * toInt64(n)
			dropped = true
		}
		if len(bl.blocks) == 0 {
			delete(l.lists, n)
		}
		bl.lowWater = len(bl.blocks)
	}
	l.armed, l.used = l.used, false
	if l.armed {
		l.armLocked()
	}
	l.mu.Unlock()
	if dropped {
		l.freeOSMemory()
	}
}

// close stops the pending trim, and the lists arm no other.
func (l *bufLists) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.armed {
		l.stop()
	}
	l.armed = false
}

// idleBytes is the bytes of the blocks the lists hold.
func (l *bufLists) idleBytes() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.idle
}
