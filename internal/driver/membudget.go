package driver

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/memlimit"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// The memory budget's defaults, in one place.
const (
	// DefaultMemoryBudgetRatio is the share of the Go memory limit the
	// budget takes when Options names none: the free list the budget caps
	// keeps its blocks live, so the rest of the limit is the other live heap
	// and the GC's headroom.
	DefaultMemoryBudgetRatio = 0.5
	// noLimitShare is the share of physical memory that stands in for the Go
	// memory limit when none is in force, the share --memory-limit-ratio
	// gives the runtime of a cgroup's.
	noLimitShare = memlimit.DefaultRatio
	// memStartWait is how long a stage's first draw waits before it is
	// answered SlowDown: a 2 GiB pod's budget turns over in about a second and
	// a half at the measured rates, about 600 MiB/s of 64 MiB transfers
	// (docs/benchmarks/2026-11-03-buffers-tentacle/REPORT.md, load 2), so a
	// longer wait is overload, and each waiter keeps its
	// rgw_max_concurrent_requests slot while it waits.
	memStartWait = 5 * time.Second
)

// memClass is a kind of draw on the memory budget.
type memClass int

const (
	// memPiece is a buffer whose release waits on nothing that waits on the
	// budget: a RADOS op, a client socket, or a pipe fed by a stage a hold
	// covers.
	memPiece memClass = iota
	// memHold is a buffer a stage keeps while it draws pieces.
	memHold
	// memPrefetch is a prefetched head, kept only while there is room now.
	memPrefetch
	memClasses
)

func (c memClass) String() string { return [...]string{"piece", "hold", "prefetch"}[c] }

// memBudget is the gateway-wide budget for the buffers of object data,
// sized from the Go memory limit. A draw that does not fit waits for room,
// where radosgw would allocate. Three classes keep it from deadlocking:
//
//   - a piece's release never waits on the budget, so pieces always come
//     back, and the room holds and prefetches cannot take, a quarter of the
//     budget, is always theirs;
//   - a stage makes at most one hold draw, its first, holding no hold
//     (memStage.hold), and holds take at most half the budget, so a stage
//     that holds one only ever waits for pieces;
//   - prefetch draws never wait.
//
// Room that frees goes first to the pieces of stages already drawing, in
// order, then to the earlier of the waiting first pieces and holds that
// fits. A stage's first draw waits at most memStartWait and is then
// answered SlowDown; any later draw waits as long as its context lives, so
// the budget never fails a transfer it has let start.
//
// The budget does not defend against slow clients: a client that sends a
// body or takes a response slowly holds its shares as long as it does. Three
// defenses it leaves to others:
//
//   - a fill deadline on the buffers a client fills;
//   - a cap on the shares one request may hold, which memStage counts;
//   - the minimum transfer rate the threat model proposes for every body and
//     response, which ends the requests whose shares do not move.
type memBudget struct {
	size, holdCap, prefetchCap int64
	// pieceRoom is the room holds and prefetches cannot take, and so the most
	// one piece takes.
	pieceRoom int64
	startWait time.Duration

	mu    sync.Mutex
	held  [memClasses]int64
	total int64
	peak  int64
	seq   uint64
	// continuing are the waiting pieces of stages that have drawn before,
	// starting the waiting first pieces, and holds the waiting holds, each
	// in arrival order.
	continuing, starting, holds memQueue
	waits, slowdowns            [memPrefetch]uint64
	waited                      [memPrefetch]time.Duration
	prefetchSkips               uint64
}

// memQueue is draws waiting in arrival order.
type memQueue []*memWaiter

func (q *memQueue) front() *memWaiter {
	if len(*q) == 0 {
		return nil
	}
	return (*q)[0]
}

func (q *memQueue) remove(wt *memWaiter) {
	if i := slices.Index(*q, wt); i >= 0 {
		*q = slices.Delete(*q, i, i+1)
	}
}

func newMemBudget(size int64, startWait time.Duration) *memBudget {
	b := &memBudget{size: size, holdCap: size / 2, prefetchCap: size / 4, startWait: startWait}
	b.pieceRoom = size - b.holdCap - b.prefetchCap
	return b
}

// share is what a buffer of n bytes of class c takes: n, but no more than
// the class can ever hold, so an oversized buffer takes all of it and
// proceeds alone.
func (b *memBudget) share(c memClass, n uint64) int64 {
	limit := b.pieceRoom
	switch c {
	case memHold:
		limit = b.holdCap
	case memPrefetch:
		limit = b.prefetchCap
	}
	return int64(min(n, uint64(limit))) //nolint:gosec // within [0, limit]
}

// memWaiter is a draw waiting in one of the queues.
type memWaiter struct {
	class   memClass
	n       int64
	seq     uint64
	ready   chan struct{}
	granted bool
	queue   *memQueue
}

// acquire takes the share of a buffer of n bytes of class c, which is a
// piece or a hold, waiting for room, and returns it. A starting draw waits
// at most startWait and then fails with op.ErrSlowDown. A draw that fails
// holds nothing.
func (b *memBudget) acquire(ctx context.Context, c memClass, starting bool, n uint64) (int64, error) {
	w := b.share(c, n)
	if w == 0 {
		return 0, nil
	}
	q := &b.continuing
	switch {
	case c == memHold:
		q, starting = &b.holds, true
	case starting:
		q = &b.starting
	}
	b.mu.Lock()
	b.seq++
	wt := &memWaiter{class: c, n: w, seq: b.seq, ready: make(chan struct{}), queue: q}
	*q = append(*q, wt)
	b.grantLocked()
	if wt.granted {
		b.mu.Unlock()
		return w, nil
	}
	b.waits[c]++
	b.mu.Unlock()

	began := time.Now()
	var expired <-chan time.Time
	if starting && b.startWait > 0 {
		t := time.NewTimer(b.startWait)
		defer t.Stop()
		expired = t.C
	}
	var err error
	select {
	case <-wt.ready:
	case <-ctx.Done():
		err = ctx.Err()
	case <-expired:
		err = fmt.Errorf("%w: waited %s for %d bytes of the memory budget's %s room", op.ErrSlowDown, b.startWait, w, c)
	}
	slowdown := err != nil && ctx.Err() == nil
	b.mu.Lock()
	b.waited[c] += time.Since(began)
	if err != nil {
		if wt.granted {
			// Granted as the wait ended: give it back, as x/sync's Acquire
			// does.
			b.giveLocked(c, w)
		} else {
			wt.queue.remove(wt)
		}
		if slowdown {
			b.slowdowns[c]++
		}
		b.grantLocked()
	}
	b.mu.Unlock()
	if err != nil {
		if slowdown {
			slog.InfoContext(ctx, "memory budget wait reached its deadline",
				slog.String("class", c.String()), slog.Int64("bytes", w), slog.Duration("wait", b.startWait))
		}
		return 0, err
	}
	return w, nil
}

// tryPrefetch takes a prefetch share of n bytes when it fits now and nothing
// waits, and reports whether it did.
func (b *memBudget) tryPrefetch(n uint64) (int64, bool) {
	w := b.share(memPrefetch, n)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.continuing)+len(b.starting)+len(b.holds) > 0 || !b.fitsLocked(memPrefetch, w) {
		b.prefetchSkips++
		return 0, false
	}
	b.takeLocked(memPrefetch, w)
	return w, true
}

// release gives back w of class c that acquire or tryPrefetch took.
func (b *memBudget) release(c memClass, w int64) {
	if w == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.giveLocked(c, w)
	b.grantLocked()
}

// prefetchRelease is the release, at most once, of a prefetch share w held
// for the state of a request ctx serves, whose copy of the head lives as
// long as the state: when ctx ends, which net/http does when the handler
// returns, or earlier when the driver discards the state. It is nil for no
// share.
func (b *memBudget) prefetchRelease(ctx context.Context, w int64) func() {
	if w == 0 {
		return nil
	}
	release := sync.OnceFunc(func() { b.release(memPrefetch, w) })
	stop := context.AfterFunc(ctx, release)
	return func() {
		stop()
		release()
	}
}

func (b *memBudget) fitsLocked(c memClass, w int64) bool {
	if w > b.size-b.total {
		return false
	}
	switch c {
	case memHold:
		return w <= b.holdCap-b.held[memHold]
	case memPrefetch:
		return w <= b.prefetchCap-b.held[memPrefetch]
	}
	return true
}

func (b *memBudget) takeLocked(c memClass, w int64) {
	b.held[c] += w
	b.total += w
	b.peak = max(b.peak, b.total)
}

func (b *memBudget) giveLocked(c memClass, w int64) {
	b.held[c] -= w
	b.total -= w
}

// grantLocked grants the waiters room allows: continuing pieces in order
// while the first fits, and once none waits, the earlier of the first
// starting piece and the first hold that fits.
func (b *memBudget) grantLocked() {
	for {
		if wt := b.continuing.front(); wt != nil {
			if !b.fitsLocked(memPiece, wt.n) {
				return
			}
			b.grantWaiterLocked(wt)
			continue
		}
		var pick *memWaiter
		for _, wt := range []*memWaiter{b.starting.front(), b.holds.front()} {
			if wt != nil && b.fitsLocked(wt.class, wt.n) && (pick == nil || wt.seq < pick.seq) {
				pick = wt
			}
		}
		if pick == nil {
			return
		}
		b.grantWaiterLocked(pick)
	}
}

func (b *memBudget) grantWaiterLocked(wt *memWaiter) {
	wt.queue.remove(wt)
	b.takeLocked(wt.class, wt.n)
	wt.granted = true
	close(wt.ready)
}

// MemoryBudgetStats is a snapshot of the memory budget.
type MemoryBudgetStats struct {
	// Size is the budget; HoldCap and PrefetchCap are the most holds and
	// prefetches take of it.
	Size, HoldCap, PrefetchCap int64
	// Held is the bytes each class holds, by class: piece, hold, prefetch.
	PieceHeld, HoldHeld, PrefetchHeld int64
	// Waiting is the draws waiting now; Waits the draws that waited, not
	// those granted at once; Waited the time they waited; Slowdowns the
	// starting draws that reached their deadline.
	PieceWaiting, HoldWaiting     int64
	PieceWaits, HoldWaits         uint64
	PieceWaited, HoldWaited       time.Duration
	PieceSlowdowns, HoldSlowdowns uint64
	// PrefetchSkipped counts the prefetches refused, for want of room or
	// because another draw waited.
	PrefetchSkipped uint64
	// FreeListIdle is the bytes of buffers the free list keeps for reuse, at
	// most Size.
	FreeListIdle int64
}

func (b *memBudget) stats() MemoryBudgetStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return MemoryBudgetStats{
		Size: b.size, HoldCap: b.holdCap, PrefetchCap: b.prefetchCap,
		PieceHeld: b.held[memPiece], HoldHeld: b.held[memHold], PrefetchHeld: b.held[memPrefetch],
		PieceWaiting: int64(len(b.continuing) + len(b.starting)), HoldWaiting: int64(len(b.holds)),
		PieceWaits: b.waits[memPiece], HoldWaits: b.waits[memHold],
		PieceWaited: b.waited[memPiece], HoldWaited: b.waited[memHold],
		PieceSlowdowns: b.slowdowns[memPiece], HoldSlowdowns: b.slowdowns[memHold],
		PrefetchSkipped: b.prefetchSkips,
	}
}

// MemoryBudgetStats reports the memory budget and the free list it caps; the
// metrics package reads it on every scrape.
func (s *Store) MemoryBudgetStats() MemoryBudgetStats {
	st := s.mem.stats()
	st.FreeListIdle = s.bufs.idleBytes()
	return st
}

// memStage is one driver entry's draws on the budget: a PUT, a part, a
// read, a copy. Its first draw is a starting one; its hold, if it takes
// one, is that first draw. A stage an outer one's hold covers draws no hold,
// and its draws continue the outer stage's; one whose read pieces are
// covered too draws nothing for them. pieces counts the piece shares it
// holds.
type memStage struct {
	b             *memBudget
	holdCovered   bool
	piecesCovered bool
	drawn         bool
	held          int64
	pieces        atomic.Int64
}

// newStage is a stage of its own.
func (b *memBudget) newStage() *memStage { return &memStage{b: b} }

// coveredStage is a stage within one whose hold covers its holds and, with
// pieces, its read pieces.
func (b *memBudget) coveredStage(pieces bool) *memStage {
	return &memStage{b: b, holdCovered: true, piecesCovered: pieces, drawn: true}
}

// errSecondHold is a hold drawn by a stage that has drawn before, which
// could hold a hold while it waits for another.
var errSecondHold = fmt.Errorf("%w: a memory budget hold drawn after the stage's first draw", op.ErrInternalError)

// hold takes the stage's hold of n bytes, as its first draw; a hold of
// nothing is no draw.
func (st *memStage) hold(ctx context.Context, n uint64) error {
	if st.holdCovered || n == 0 {
		return nil
	}
	if st.drawn {
		return errSecondHold
	}
	st.drawn = true
	w, err := st.b.acquire(ctx, memHold, true, n)
	st.held = w
	return err
}

// piece takes a piece share of n bytes and returns it, nothing when the
// stage's read pieces are covered.
func (st *memStage) piece(ctx context.Context, n uint64) (int64, error) {
	if st.piecesCovered {
		return 0, nil
	}
	starting := !st.drawn
	st.drawn = true
	w, err := st.b.acquire(ctx, memPiece, starting, n)
	st.pieces.Add(w)
	return w, err
}

// servedHead records that the stage served bytes a prefetch holds, which
// counts as its first draw.
func (st *memStage) servedHead() { st.drawn = true }

// give gives back a piece share w the stage took.
func (st *memStage) give(w int64) {
	if w == 0 {
		return
	}
	st.pieces.Add(-w)
	st.b.release(memPiece, w)
}

// done gives back the stage's hold.
func (st *memStage) done() {
	st.b.release(memHold, st.held)
	st.held = 0
}

// memoryBudgetSize is the budget for a Go memory limit of limit bytes,
// math.MaxInt64 for none, at ratio, 0 meaning DefaultMemoryBudgetRatio,
// floored at floor; physical reads the physical memory that stands in for a
// missing limit. A limit of 0 or less asks for no budget, which only specs
// do.
func memoryBudgetSize(ctx context.Context, limit int64, ratio float64, floor int64, physical func() (uint64, error)) int64 {
	if limit <= 0 {
		return math.MaxInt64
	}
	if ratio == 0 {
		ratio = DefaultMemoryBudgetRatio
	}
	from := "go memory limit"
	if limit == math.MaxInt64 {
		ram, err := physical()
		if err != nil || ram == 0 {
			slog.WarnContext(ctx, "no go memory limit and no physical memory to size the memory budget from; it bounds nothing",
				slog.Any("error", err))
			return math.MaxInt64
		}
		limit, from = scaleBytes(min(ram, math.MaxInt64), noLimitShare), "physical memory"
	}
	size := scaleBytes(uint64(limit), ratio) //nolint:gosec // limit is positive
	attrs := []slog.Attr{
		slog.String("from", from), slog.Int64("limit_bytes", limit), slog.Float64("ratio", ratio),
	}
	if size < floor {
		slog.LogAttrs(ctx, slog.LevelWarn, "memory budget raised to the least one request needs",
			append(attrs, slog.Int64("share_bytes", size), slog.Int64("budget_bytes", floor))...)
		return floor
	}
	slog.LogAttrs(ctx, slog.LevelInfo, "memory budget sized", append(attrs, slog.Int64("budget_bytes", size))...)
	return size
}

// scaleBytes is ratio times n rounded down, held at math.MaxInt64.
func scaleBytes(n uint64, ratio float64) int64 {
	f := math.Floor(float64(n) * ratio)
	if f >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(f)
}

// physicalMemory is the host's total RAM, sysinfo's totalram.
func physicalMemory() (uint64, error) {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err != nil {
		return 0, fmt.Errorf("sysinfo: %w", err)
	}
	return si.Totalram * uint64(si.Unit), nil
}

// memBudgetFloor is the least budget the options let every request run
// in: four of the largest piece, the room pieces always keep, and twice the
// largest hold, a copy's read window, head or decoder blocks, and
// compressor buffers.
func memBudgetFloor(r readConfig, w writeOptions) int64 {
	piece := max(w.chunkSize, r.maxReq, min(w.stripeSize, w.putWindow+1))
	hold := satAdd(r.window, satAdd(compressorHold(w.chunkSize), 2*w.chunkSize))
	return toInt64(max(satMul(4, piece), satMul(2, hold)))
}

// compressorHold is what a compressing write's two block buffers hold for
// blocks of chunk bytes: the plaintext block, and its encoded form with
// room for every codec's expansion and the slack of its growth.
func compressorHold(chunk uint64) uint64 { return satAdd(2*chunk, chunk/2) }

func satAdd(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}

func satMul(k, n uint64) uint64 {
	if n > math.MaxUint64/k {
		return math.MaxUint64
	}
	return k * n
}
