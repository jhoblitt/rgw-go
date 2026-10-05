package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The GC worker is radosgw's RGWGC processor (driver/rados/rgw_gc.cc). A
// bare line number below is v19.2.6's; v20.2.4 has the same code, from
// RGWGC::list (:247) on 12 to 15 lines later.

// gcProcess is the shard lock's name, gc_index_lock_name (:29).
const gcProcess = "gc_process"

// gcListMax is the page RGWGC::process lists (:585).
const gcListMax = 100

// gcUnlockTimeout bounds the shard unlock a pass ends with, which must not
// hold a stopping gateway on an unresponsive cluster; a lock it leaves
// lapses after the pass's budget.
const gcUnlockTimeout = 30 * time.Second

// errGCNoTime is the -EAGAIN RGWGC::process answers before it locks a shard
// when it has no time to hold the lock for (:560-565), which ends the pass.
var errGCNoTime = fmt.Errorf("driver: gc: rgw_gc_processor_max_time is not positive: %w", syscall.EAGAIN)

// gcPool is the zone's gc pool, radosgw's gc_pool_ctx.
func (s *Store) gcPool(ctx context.Context) (radosclient.Pool, error) {
	return s.pools.get(ctx, s.ZoneParams().GCPool)
}

// gcInitialize is RGWGC::initialize (:31-56) through gc_log_init2
// (rgw_gc_log.cc:11-19): each shard is created, checked at cls version 0,
// given its rgw_gc queue and set to version 1 in one op, so a shard already
// moved to the queue answers ECANCELED and is left as it is. radosgw ignores
// every shard's result; only a gc pool that cannot be opened fails it.
func (s *Store) gcInitialize(ctx context.Context) error {
	pool, err := s.gcPool(ctx)
	if err != nil {
		return err
	}
	for _, oid := range s.w.gcShards {
		w := radosclient.NewWriteOp()
		w.Create(false)
		version.Check(w, version.ObjVersion{}, version.CondEQ, s.release)
		gc.QueueInit(w, s.w.opts.gcMaxQueueSize, s.w.opts.gcMaxDeferred, s.release)
		version.Set(w, version.ObjVersion{Ver: 1}, s.release)
		if _, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrCanceled) {
			slog.DebugContext(ctx, "gc shard not initialized", slog.String("shard", oid), slog.Any("error", err))
		}
	}
	return nil
}

// gcWorker is RGWGC::GCWorker with the processor state it reads: which
// shards it has seen move from the omap log to the rgw_gc queue, kept for
// the life of the gateway as transitioned_objects_cache is.
type gcWorker struct {
	s            *Store
	transitioned []bool
	now          func() time.Time
	sleep        func(ctx context.Context, d time.Duration) error
}

func newGCWorker(s *Store) *gcWorker {
	return &gcWorker{s: s, transitioned: make([]bool, len(s.w.gcShards)), now: time.Now, sleep: sleepCtx}
}

// run is GCWorker::entry (:782-809): a pass over every shard for the expired
// entries, then a wait of rgw_gc_processor_period less the whole seconds the
// pass took, none when it took the period or longer, until ctx ends. A period
// that is not positive is waited as one second, where radosgw would run its
// passes without pause (docs/exclusions.md).
func (g *gcWorker) run(ctx context.Context) error {
	period := g.s.w.opts.gcProcessorPeriod
	if period <= 0 {
		slog.ErrorContext(ctx, "gc processor period is not positive; waiting 1 second between passes",
			slog.String("option", "rgw_gc_processor_period"), slog.Int64("value", int64(period)))
		period = 1
	}
	for {
		start := g.now()
		slog.DebugContext(ctx, "garbage collection: start")
		if err := g.process(ctx, true); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "garbage collection process failed", slog.Any("error", err))
		}
		slog.DebugContext(ctx, "garbage collection: stop")
		if goingDown(ctx) {
			return nil
		}
		took := int64(g.now().Sub(start) / time.Second)
		if int64(period) <= took {
			continue
		}
		_ = g.sleep(ctx, time.Duration(int64(period)-took)*time.Second) //nolint:errcheck // it fails only as ctx ends, which goingDown sees next
		if goingDown(ctx) {
			return nil
		}
	}
}

// goingDown is RGWGC::going_down: whether ctx, the gateway's life, has ended.
func goingDown(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// process is RGWGC::process(bool) (:729-748): every shard in turn from a
// random one, each for at most rgw_gc_processor_max_time, then the drain of
// the omap-era tag removals unless ctx has ended. A shard's failure ends the
// pass without the drain.
func (g *gcWorker) process(ctx context.Context, expiredOnly bool) error {
	n := len(g.s.w.gcShards)
	budget := time.Duration(g.s.w.opts.gcProcessorMaxTime) * time.Second
	start := rand.IntN(n) //nolint:gosec // ceph::util::generate_random_number spreads gateways' passes; nothing secret
	io := newGCIO(g)
	defer io.wait()
	for i := range n {
		if err := g.processShard(ctx, (i+start)%n, budget, expiredOnly, io); err != nil {
			return err
		}
	}
	if ctx.Err() == nil {
		io.drain(ctx)
	}
	return nil
}

// processShard is RGWGC::process(int, ...) (:550-727): under the shard's
// gc_process lock, held for budget, it frees the objects of each listed
// entry, a page of gcListMax at a time, until the listing ends or the budget
// is spent. A shard another gateway holds is skipped.
func (g *gcWorker) processShard(ctx context.Context, idx int, budget time.Duration, expiredOnly bool, io *gcIO) error {
	if budget <= 0 {
		return errGCNoTime
	}
	end := g.now().Add(budget)
	pool, err := g.s.gcPool(ctx)
	if err != nil {
		return err
	}
	oid := g.s.w.gcShards[idx]
	err = pool.LockExclusive(ctx, oid, gcProcess, "", "", budget, 0)
	if errno(err) == syscall.EBUSY {
		slog.DebugContext(ctx, "gc shard locked by another processor", slog.String("shard", oid))
		return nil
	}
	if err != nil {
		return err
	}
	g.shardPass(ctx, pool, idx, end, expiredOnly, io)
	// radosgw unlocks on its way down too; an unlock that ctx's end
	// abandoned would hold the shard from every gateway for the budget.
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gcUnlockTimeout)
	defer cancel()
	if err := pool.Unlock(uctx, oid, gcProcess, ""); err != nil {
		slog.DebugContext(ctx, "gc shard unlock failed", slog.String("shard", oid), slog.Any("error", err))
	}
	return nil
}

// gcPage is the listing state of one shard's pass: the marker the next page
// starts at, and what the last listing that answered said, which a failed
// one leaves as it was.
type gcPage struct {
	marker, next string
	truncated    bool
}

// shardPass is the locked part of processShard (:580-717), returning where
// radosgw jumps to done.
func (g *gcWorker) shardPass(ctx context.Context, pool radosclient.Pool, idx int, end time.Time, expiredOnly bool, io *gcIO) {
	oid := g.s.w.gcShards[idx]
	var page gcPage
	for {
		var entries []rgw.GCObjInfo
		var err error
		if !g.transitioned[idx] {
			entries, err = g.omapList(ctx, pool, oid, &page, gcListMax, expiredOnly)
			ver := g.readVersion(ctx, pool, oid)
			if ver == 1 && len(entries) == 0 {
				var pending []rgw.GCObjInfo
				pending, err = g.omapList(ctx, pool, oid, &page, 1, false)
				if len(pending) > 0 {
					return
				}
				g.transitioned[idx] = true
				page.marker = ""
			}
			if ver == 0 && (errors.Is(err, radosclient.ErrNotFound) || len(entries) == 0) {
				return
			}
		}
		if g.transitioned[idx] {
			entries, err = g.queueList(ctx, pool, oid, &page, expiredOnly)
			if len(entries) == 0 {
				return
			}
		}
		if err != nil {
			return
		}
		page.marker = page.next
		if !g.freeEntries(ctx, idx, entries, end, io) {
			return
		}
		if g.transitioned[idx] && len(entries) > 0 {
			if err := io.drainIOs(ctx); err != nil {
				return
			}
			if err := io.removeQueueEntries(ctx, pool, idx, len(entries)); err != nil {
				return
			}
		}
		if !page.truncated {
			return
		}
	}
}

// freeEntries schedules the refcount puts of a page's entries (:633-702)
// and reports whether the pass goes on: not once the budget is spent, nor
// after a failure on a queue-era shard, nor once ctx has ended.
func (g *gcWorker) freeEntries(ctx context.Context, idx int, entries []rgw.GCObjInfo, end time.Time, io *gcIO) bool {
	var lastPool string
	var tailPool radosclient.Pool
	for _, info := range entries {
		if !g.now().Before(end) {
			return false
		}
		if !g.transitioned[idx] {
			if len(info.Chain) == 0 {
				io.scheduleTagRemoval(ctx, idx, info.Tag)
			} else {
				io.addTagIOSize(idx, info.Tag, len(info.Chain))
			}
		}
		for _, obj := range info.Chain {
			// radosgw compares the pool with the last one it opened, "" at
			// first, so an object without a pool reaches an IoCtx it never
			// opened and faults (docs/ceph-upstream-bugs.md); here it is
			// opened by name, which fails as any missing pool does.
			if tailPool == nil || obj.Pool != lastPool {
				p, err := g.s.pools.get(ctx, meta.ParsePool(obj.Pool))
				if err != nil {
					if g.transitioned[idx] {
						return false
					}
					tailPool, lastPool = nil, ""
					slog.ErrorContext(ctx, "gc could not open a pool", slog.String("pool", obj.Pool), slog.Any("error", err))
					continue
				}
				tailPool, lastPool = p, obj.Pool
			}
			if err := io.scheduleIO(ctx, tailPool.WithLocator(obj.Loc), obj.Key.Name, idx, info.Tag); err != nil {
				slog.WarnContext(ctx, "gc failed to schedule a deletion", slog.String("oid", obj.Key.Name), slog.Any("error", err))
				if g.transitioned[idx] {
					return false
				}
			}
			if ctx.Err() != nil {
				return false
			}
		}
	}
	return true
}

// omapList is cls_rgw_gc_list on the shard's omap log: a failure returns no
// entries and leaves page's truncated and next as they were.
func (g *gcWorker) omapList(ctx context.Context, pool radosclient.Pool, oid string, page *gcPage, maxEntries uint32, expiredOnly bool) ([]rgw.GCObjInfo, error) {
	r := radosclient.NewReadOp()
	res := rgw.GCList(r, page.marker, maxEntries, expiredOnly, g.s.release)
	return listResult(ctx, pool, oid, r, page, res.Result)
}

// queueList is cls_rgw_gc_queue_list_entries on the shard's rgw_gc queue,
// a page of gcListMax, with omapList's failure rule.
func (g *gcWorker) queueList(ctx context.Context, pool radosclient.Pool, oid string, page *gcPage, expiredOnly bool) ([]rgw.GCObjInfo, error) {
	r := radosclient.NewReadOp()
	res := gc.QueueList(r, page.marker, gcListMax, expiredOnly, g.s.release)
	return listResult(ctx, pool, oid, r, page, res.Result)
}

func listResult(ctx context.Context, pool radosclient.Pool, oid string, r *radosclient.ReadOp, page *gcPage, result func() (rgw.GCListRet, error)) ([]rgw.GCObjInfo, error) {
	if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return nil, err
	}
	ret, err := result()
	if err != nil {
		return nil, err
	}
	page.truncated, page.next = ret.Truncated, ret.NextMarker
	return ret.Entries, nil
}

// readVersion is cls_version_read with its result ignored, as RGWGC::process
// ignores it (:596-597): the shard's version counter, 0 when it cannot be
// read.
func (g *gcWorker) readVersion(ctx context.Context, pool radosclient.Pool, oid string) uint64 {
	r := radosclient.NewReadOp()
	res := version.Read(r, g.s.release)
	if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return 0
	}
	v, err := res.Version()
	if err != nil {
		return 0
	}
	return v.Ver
}

// errno is the errno a RADOS failure carries, 0 for none.
func errno(err error) syscall.Errno {
	e, ok := errors.AsType[*radosclient.Error](err)
	if !ok {
		return 0
	}
	if e.Errno < 0 {
		return syscall.Errno(-e.Errno)
	}
	return syscall.Errno(e.Errno)
}

// gcIOKind is RGWGCIOManager::IO::Type: a tail's refcount put or an
// omap-era tag removal.
type gcIOKind uint8

const (
	gcTailIO gcIOKind = iota + 1
	gcIndexIO
)

// gcPending is one operation in flight, an RGWGCIOManager::IO.
type gcPending struct {
	kind gcIOKind
	idx  int
	oid  string
	tag  string
	done chan struct{}
	err  error
}

// gcIO is RGWGCIOManager (:340-548): the operations in flight, completed in
// the order they started; the objects each omap-era tag still waits on; and
// the tags each omap-era shard has freed, removed rgw_gc_max_trim_chunk at a
// time. Only the worker's goroutine touches it.
type gcIO struct {
	g *gcWorker
	// maxAIO and trimChunk are rgw_gc_max_concurrent_io and
	// rgw_gc_max_trim_chunk as the size_t radosgw compares them as, so a
	// negative one bounds nothing (docs/ceph-upstream-bugs.md).
	maxAIO, trimChunk uint64
	ios               []*gcPending
	tagLeft           []map[string]int
	removeTags        [][]string
	wg                sync.WaitGroup
}

func newGCIO(g *gcWorker) *gcIO {
	n := len(g.s.w.gcShards)
	io := &gcIO{
		g:          g,
		maxAIO:     uint64(int64(g.s.w.opts.gcMaxConcurrentIO)), //nolint:gosec // size_t max_aio, as radosgw converts it
		trimChunk:  uint64(int64(g.s.w.opts.gcMaxTrimChunk)),    //nolint:gosec // the size_t cast at :462
		tagLeft:    make([]map[string]int, n),
		removeTags: make([][]string, n),
	}
	for i := range io.tagLeft {
		io.tagLeft[i] = map[string]int{}
	}
	return io
}

// start runs fn in the background as the newest operation in flight.
func (io *gcIO) start(p *gcPending, fn func() error) {
	p.done = make(chan struct{})
	io.ios = append(io.ios, p)
	io.wg.Go(func() {
		p.err = fn()
		close(p.done)
	})
}

// wait waits for every operation started, whether or not it was handled,
// so none outlives the pass.
func (io *gcIO) wait() { io.wg.Wait() }

// scheduleIO is schedule_io (:382-404): while more than maxAIO operations
// are in flight it completes the oldest, failing on a failure only when shard
// idx has moved to the queue, then starts the refcount put of the tail oid
// under tag, at pool full too, as radosgw's ioctx sets pool_full_try (:677).
// ctx's end stops the wait without starting the put.
func (io *gcIO) scheduleIO(ctx context.Context, pool radosclient.Pool, oid string, idx int, tag string) error {
	for uint64(len(io.ios)) > io.maxAIO {
		if goingDown(ctx) {
			return nil
		}
		if err := io.handleNext(ctx); err != nil && io.g.transitioned[idx] {
			return err
		}
	}
	w := radosclient.NewWriteOp()
	refcount.Put(w, tag, true, io.g.s.release)
	io.start(&gcPending{kind: gcTailIO, idx: idx, oid: oid, tag: tag}, func() error {
		_, err := pool.Write(ctx, oid, w, radosclient.OpFlagFullTry)
		return err
	})
	return nil
}

// handleNext is handle_next_completion (:406-438): it waits for the oldest
// operation, an ENOENT counting as done, and for a freed tail of an omap-era
// shard counts its tag down.
func (io *gcIO) handleNext(ctx context.Context) error {
	p := io.ios[0]
	<-p.done
	io.ios[0] = nil
	io.ios = io.ios[1:]
	err := p.err
	if errors.Is(err, radosclient.ErrNotFound) {
		err = nil
	}
	transitioned := io.g.transitioned[p.idx]
	if p.kind == gcIndexIO && !transitioned {
		if err != nil {
			slog.WarnContext(ctx, "gc cleanup of tags on a gc shard failed", slog.Int("shard_index", p.idx), slog.Any("error", err))
		}
		return err
	}
	if err != nil {
		slog.WarnContext(ctx, "gc could not remove an object", slog.String("oid", p.oid), slog.Any("error", err))
		return err
	}
	if !transitioned {
		io.scheduleTagRemoval(ctx, p.idx, p.tag)
	}
	return nil
}

// scheduleTagRemoval is schedule_tag_removal (:446-465): a tag still waiting
// on objects is counted down, and one waiting on none joins its shard's
// removals, which go out once they reach rgw_gc_max_trim_chunk.
func (io *gcIO) scheduleTagRemoval(ctx context.Context, idx int, tag string) {
	if left, ok := io.tagLeft[idx][tag]; ok {
		left--
		if left != 0 {
			io.tagLeft[idx][tag] = left
			return
		}
		delete(io.tagLeft[idx], tag)
	}
	io.removeTags[idx] = append(io.removeTags[idx], tag)
	if uint64(len(io.removeTags[idx])) >= io.trimChunk {
		io.flushRemoveTags(ctx, idx)
	}
}

// addTagIOSize is add_tag_io_size (:467-470): the number of objects tag
// waits on, kept as first recorded.
func (io *gcIO) addTagIOSize(idx int, tag string, n int) {
	if _, ok := io.tagLeft[idx][tag]; !ok {
		io.tagLeft[idx][tag] = n
	}
}

// drainIOs is drain_ios (:472-484): every operation completed, reporting the
// last failure, or the -EAGAIN of ctx's end.
func (io *gcIO) drainIOs(ctx context.Context) error {
	var last error
	for len(io.ios) > 0 {
		if ctx.Err() != nil {
			return fmt.Errorf("driver: gc: drain abandoned: %w", syscall.EAGAIN)
		}
		if err := io.handleNext(ctx); err != nil {
			last = err
		}
	}
	return last
}

// drain is RGWGCIOManager::drain (:486-491): the operations completed, the
// tag removals of every omap-era shard sent, an empty one too, as
// flush_remove_tags sends it, and those completed.
func (io *gcIO) drain(ctx context.Context) {
	_ = io.drainIOs(ctx) //nolint:errcheck // radosgw ignores the drain's result
	for idx := range io.removeTags {
		if !io.g.transitioned[idx] {
			io.flushRemoveTags(ctx, idx)
		}
	}
	_ = io.drainIOs(ctx) //nolint:errcheck // as above
}

// flushRemoveTags is flush_remove_tags (:493-523): the shard's gc_remove of
// the tags it has freed, in the background; the list is cleared even when
// the removal cannot be sent.
func (io *gcIO) flushRemoveTags(ctx context.Context, idx int) {
	tags := io.removeTags[idx]
	io.removeTags[idx] = nil
	pool, err := io.g.s.gcPool(ctx)
	if err != nil {
		slog.WarnContext(ctx, "gc failed to remove tags on a gc shard", slog.Int("shard_index", idx), slog.Any("error", err))
		return
	}
	oid := io.g.s.w.gcShards[idx]
	w := radosclient.NewWriteOp()
	rgw.GCRemove(w, tags, io.g.s.release)
	io.start(&gcPending{kind: gcIndexIO, idx: idx}, func() error {
		_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
		return err
	})
}

// removeQueueEntries is remove_queue_entries (:535-547): the first n
// entries of the shard's queue go.
func (io *gcIO) removeQueueEntries(ctx context.Context, pool radosclient.Pool, idx, n int) error {
	w := radosclient.NewWriteOp()
	gc.QueueRemoveEntries(w, uint32(n), io.g.s.release) //nolint:gosec // a page holds at most gcListMax entries
	if _, err := pool.Write(ctx, io.g.s.w.gcShards[idx], w, radosclient.OpFlagNone); err != nil {
		slog.ErrorContext(ctx, "gc failed to remove queue entries", slog.Int("shard_index", idx), slog.Any("error", err))
		return err
	}
	return nil
}
