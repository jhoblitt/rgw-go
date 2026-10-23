package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The lifecycle worker is radosgw's RGWLC processor: rgw_lc_max_worker
// workers, each passing over every lifecycle shard under its lc_process lock
// and claiming, one at a time, the buckets whose entries are due
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1994-L2465 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2051-L2532 at v20.2.4).
// It runs radosgw's protocol, so radosgw's workers and rgw-go's may share a
// zone, but where docs/exclusions.md, "rgw-go's lifecycle worker departs
// from radosgw's shard protocol in three places", says it departs: an entry
// the bucket pass may not claim keeps the shard's head (lcShard.walkPast), a
// claim no longer the worker's gets no post-state (lcWorker.claimHeld), and
// a worker stopped during a bucket records it failed (lcShard.recordStop).

const (
	// lcLockTries and lcLockBackoff are the shard pass's SimpleBackoff(5,
	// 50ms): five tries, the k-th failed one, the last included, followed by
	// a wait of k × 100 ms
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1832-L1863 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1888-L1919 at v20.2.4).
	lcLockTries   = 5
	lcLockBackoff = 100 * time.Millisecond
	// lcDaySeconds is secs_in_a_day, the lifecycle day outside debug mode.
	lcDaySeconds = 86400
	// lcStopBound bounds, once a bucket's pass has returned or the worker
	// stops, the relock with its backoff, the claim read back and the
	// post-state's write: the backoff's 1 s of waits and a few round trips,
	// inside the gateway's shutdown after its HTTP drain. The unlock after
	// them has a bound of its own, lcStopBound again, as has every unlock
	// once the worker stops.
	lcStopBound = 5 * time.Second
)

var (
	// errLCStaleEntry is a bucket pass's -ENOENT: the entry's bucket does
	// not load, or loads with another marker, and the post-state removes
	// the entry.
	errLCStaleEntry = errors.New("lifecycle entry names no bucket")
	// errLCClaimLost is a claim the shard no longer holds as the worker made
	// it: reclaimed, reset by a configuration put, or removed.
	errLCClaimLost = errors.New("lifecycle claim lost")
)

// lcBucketPass is what a worker runs on the buckets of a shard: the
// predicate that decides whether it may claim a bucket, and
// bucket_lc_process itself.
type lcBucketPass interface {
	// claimable reports whether w may claim the bucket whose entry key is
	// key, "tenant:name:marker". It runs under the shard's lock, on an
	// entry that is due. A bucket it refuses is not claimed and the shard's
	// head is not moved past it, so a radosgw worker sharing the zone
	// reaches it; an error refuses it too.
	claimable(ctx context.Context, w *lcWorker, key string) (bool, error)
	// run processes the claimed bucket until stopAt, thread_stop_at, with
	// the shard unlocked. An error wrapping errLCStaleEntry removes the
	// entry, any other error leaves it failed, and nil complete. A run stops
	// on any error w.claimHeld returns, a lost claim or a failed read, and
	// returns it; what a run returns over a lost claim is not recorded.
	run(ctx context.Context, w *lcWorker, c lcClaim, stopAt time.Time) error
}

// lcClaim is a worker's claim on a bucket: the shard and the entry as the
// claim wrote it, processing since its start time.
type lcClaim struct {
	shard uint32
	entry rgwcls.LCEntry
}

// lcWorkers are RGWLC's processor, rgw_lc_max_worker workers over the
// shards.
type lcWorkers struct {
	s    *Store
	pass lcBucketPass
	// now, sleep and loc are the workers' clock, waits and time zone:
	// time.Now, sleepCtx and time.Local, the zone glibc's localtime_r reads
	// radosgw's days and work window in.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	loc   *time.Location
	// perm is random_sequence, the shards in a fresh random order each pass.
	perm func(n int) []int
	// stopBound is lcStopBound.
	stopBound time.Duration
}

func newLCWorkers(s *Store, pass lcBucketPass) *lcWorkers {
	return &lcWorkers{s: s, pass: pass, now: time.Now, sleep: sleepCtx, loc: time.Local, perm: rand.Perm, stopBound: lcStopBound}
}

// run is start_processor
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2467-L2477 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2534-L2544 at v20.2.4):
// workers 0 to rgw_lc_max_worker-1, run until ctx ends.
func (ws *lcWorkers) run(ctx context.Context) error {
	var wg sync.WaitGroup
	for ix := range ws.s.opts.lcMaxWorker {
		w := ws.worker(ix)
		wg.Go(func() { w.run(ctx) })
	}
	wg.Wait()
	return nil
}

// lcWorker is one LCWorker. cookie is its thr_name, "lc_thrd: <ix>", the
// cookie it takes shard locks under
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L601-L603 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L602-L604 at v20.2.4).
type lcWorker struct {
	*lcWorkers
	ix     int
	cookie string
}

func (ws *lcWorkers) worker(ix int) *lcWorker {
	return &lcWorker{lcWorkers: ws, ix: ix, cookie: "lc_thrd: " + strconv.Itoa(ix)}
}

// run is LCWorker::entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L201-L231 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L201-L231 at v20.2.4):
// a pass over every shard when the work window holds at the start, then a
// wait until the next start, until ctx ends.
func (w *lcWorker) run(ctx context.Context) {
	for {
		start := w.now()
		if w.shouldWork(start) {
			slog.DebugContext(ctx, "lifecycle: start", slog.Int("worker", w.ix))
			w.process(ctx)
			slog.DebugContext(ctx, "lifecycle: stop", slog.Int("worker", w.ix))
		}
		if goingDown(ctx) {
			return
		}
		end := w.now()
		wait := w.nextStart(start, end)
		slog.DebugContext(ctx, "lifecycle: next start scheduled", slog.Time("next", end.Add(wait)), slog.Int("worker", w.ix))
		if w.sleep(ctx, wait) != nil {
			return
		}
	}
}

// process is RGWLC::process over every shard: the shards in a fresh random
// order, until ctx ends. v20.2.4's restore expirer after the shards is not
// run (docs/exclusions.md).
func (w *lcWorker) process(ctx context.Context) {
	for _, idx := range w.perm(int(w.s.opts.lcMaxObjs)) {
		if goingDown(ctx) {
			return
		}
		w.processShard(ctx, uint32(idx)) //nolint:gosec // a shard index, below rgw_lc_max_objs
	}
}

// shouldWork is should_work
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2510-L2539 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2577-L2606 at v20.2.4):
// always in debug mode, otherwise whether now's local hour and minute fall
// in rgw_lifecycle_work_time, both ends included; a window whose end hour
// is before its start hour runs past midnight.
func (w *lcWorker) shouldWork(now time.Time) bool {
	if w.s.opts.lcDebugInterval > 0 {
		return true
	}
	wt := w.s.opts.lcWorkTime
	local := now.In(w.loc)
	hour, endHour := local.Hour(), wt.endHour
	if wt.endHour < wt.startHour {
		if hour <= wt.endHour {
			hour += 24
		}
		endHour += 24
	}
	m := hour*60 + local.Minute()
	return m >= wt.startHour*60+wt.startMinute && m <= endHour*60+wt.endMinute
}

// nextStart is schedule_next_start_time
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2541-L2570 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2608-L2637 at v20.2.4),
// the wait after a pass that started at start and ended at now: in debug
// mode what remains of the interval since start, in whole seconds and none
// when it has passed; otherwise the seconds to the work window's start
// today, local time, or a day later when that has come.
func (w *lcWorker) nextStart(start, now time.Time) time.Duration {
	if debug := w.s.opts.lcDebugInterval; debug > 0 {
		return max(start.Add(debug).Sub(now)/time.Second, 0) * time.Second
	}
	wt := w.s.opts.lcWorkTime
	tt := now.Unix()
	secs := lcLocalTime(time.Unix(tt, 0), wt.startHour, wt.startMinute, w.loc).Unix() - tt
	if secs <= 0 {
		secs += lcDaySeconds
	}
	return time.Duration(secs) * time.Second
}

// lcLocalTime is mktime of localtime_r(ref)'s fields with the time of day
// set to hour:minute:00, out-of-range fields carried as mktime carries
// them, read as lifecycle's roundToDay reads round_to_day
// (internal/lifecycle/expire.go): the zone's own time when the zone has it
// and its daylight-saving flag there is ref's, and otherwise that time at
// the offset ref has, as glibc's mktime reads a time with the tm_isdst
// localtime_r set.
func lcLocalTime(ref time.Time, hour, minute int, loc *time.Location) time.Time {
	local := ref.In(loc)
	y, m, d := local.Date()
	t := time.Date(y, m, d, hour, minute, 0, 0, loc)
	want := time.Date(y, m, d, hour, minute, 0, 0, time.UTC)
	ty, tm, td := t.Date()
	th, tmi, ts := t.Clock()
	if t.IsDST() != local.IsDST() || !time.Date(ty, tm, td, th, tmi, ts, 0, time.UTC).Equal(want) {
		_, offset := local.Zone()
		t = time.Date(y, m, d, hour, minute, 0, 0, time.FixedZone("", offset))
	}
	return t
}

// intervalSeconds is the lifecycle day expired_session and thread_stop_at
// count in: rgw_lc_debug_interval when positive, else secs_in_a_day.
func (w *lcWorker) intervalSeconds() int64 {
	if debug := w.s.opts.lcDebugInterval; debug > 0 {
		return int64(debug / time.Second)
	}
	return lcDaySeconds
}

// expiredSession is expired_session
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2025-L2043 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2088-L2106 at v20.2.4):
// whether a claim made at started has lapsed, two lifecycle days on, with
// rgwlc_auto_session_clear set.
func (w *lcWorker) expiredSession(started uint64, now time.Time) bool {
	if !w.s.opts.lcAutoSessionClear {
		return false
	}
	return int64(started)+2*w.intervalSeconds() < now.Unix() //nolint:gosec // time_t started, as radosgw converts it
}

// alreadyRunToday is already_run_today
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2149-L2171 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2213-L2235 at v20.2.4):
// in debug mode whether less than the interval has passed since start;
// otherwise whether less than a day has passed since local midnight of
// start's day.
func (w *lcWorker) alreadyRunToday(start uint64, now time.Time) bool {
	st := time.Unix(int64(start), 0) //nolint:gosec // time_t start_date, as radosgw converts it
	if debug := w.s.opts.lcDebugInterval; debug > 0 {
		return now.Sub(st) < debug
	}
	return now.Sub(lcLocalTime(st, 0, 0, w.loc)) < lcDaySeconds*time.Second
}

// allowRollover is allow_shard_rollover
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2134-L2147 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2198-L2211 at v20.2.4):
// whether a pass may start the shard over from its first entry: in debug
// mode, with no rollover date stored, or more than a day after it.
func (w *lcWorker) allowRollover(now time.Time, rollover uint64) bool {
	r, n := int64(rollover), now.Unix() //nolint:gosec // time_t shard_rollover_date, as cls_rgw_lc_obj_head holds it
	return (r < n && n-r > lcDaySeconds) || r == 0 || w.s.opts.lcDebugInterval > 0
}

// due reports whether e is due a claim at now: not processing in a live
// session, and not completed today
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2349-L2378 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2416-L2445 at v20.2.4).
func (w *lcWorker) due(e rgwcls.LCEntry, now time.Time) bool {
	switch e.Status {
	case rgwcls.LCStatusProcessing:
		return w.expiredSession(e.StartTime, now)
	case rgwcls.LCStatusComplete:
		return !w.alreadyRunToday(e.StartTime, now)
	}
	return true
}

// stopAt is thread_stop_at
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2045-L2051 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2108-L2114 at v20.2.4):
// a lifecycle day from now.
func (w *lcWorker) stopAt() time.Time {
	return time.Unix(w.now().Unix()+w.intervalSeconds(), 0)
}

// lcSeconds is t as the time_t radosgw stores in an entry or the head.
func lcSeconds(t time.Time) uint64 {
	return uint64(t.Unix()) //nolint:gosec // the time_t radosgw stores, as it converts it
}

// claimHeld is the claim re-read: nil while the shard holds c's entry as
// the claim wrote it, errLCClaimLost once it does not, or the read's error.
// The worker reads it before the bucket and before the post-state, and a
// bucket pass at each listing page, which stops on any error it returns.
func (w *lcWorker) claimHeld(ctx context.Context, c lcClaim) error {
	pool, err := w.s.lcPool(ctx)
	if err != nil {
		return err
	}
	got, err := lcGetEntry(ctx, pool, lcShardOID(c.shard), c.entry.Bucket)
	if errors.Is(err, radosclient.ErrNotFound) {
		return fmt.Errorf("%w: the entry of %s is gone", errLCClaimLost, c.entry.Bucket)
	}
	if err != nil {
		return fmt.Errorf("reading the lifecycle claim on %s: %w", c.entry.Bucket, err)
	}
	if got != c.entry {
		return fmt.Errorf("%w: %s is %+v, not %+v", errLCClaimLost, c.entry.Bucket, got, c.entry)
	}
	return nil
}

// lcShard is one shard's pass by one worker.
type lcShard struct {
	w    *lcWorker
	pool radosclient.Pool
	idx  uint32
	oid  string
}

// processShard is RGWLC::process(int index, ...)
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2252-L2465 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2317-L2532 at v20.2.4):
// under the shard's lc_process lock, taken with the backoff for
// rgw_lc_lock_max_time, it claims each due entry from the head on, until the
// head reaches the shard's end. A shard still locked after the backoff is
// skipped.
func (w *lcWorker) processShard(ctx context.Context, idx uint32) {
	pool, err := w.s.lcPool(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "lifecycle could not open the lc pool", slog.Any("error", err))
		return
	}
	sh := &lcShard{w: w, pool: pool, idx: idx, oid: lcShardOID(idx)}
	if err := sh.lock(ctx); err != nil {
		attrs := []any{slog.String("shard", sh.oid), slog.Int("worker", w.ix), slog.Any("error", err)}
		if lcLockHeld(err) {
			slog.DebugContext(ctx, "lifecycle shard locked by another holder", attrs...)
		} else {
			slog.WarnContext(ctx, "lifecycle could not lock the shard", attrs...)
		}
		return
	}
	if sh.run(ctx) {
		sh.unlock(ctx, w.unlockBound(ctx))
	}
}

// lock is wait_backoff over the shard's lock: any failure to take it counts
// as a try, as lock_lambda counts it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2268-L2287 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2333-L2352 at v20.2.4).
// It returns nil once it holds the lock, else the last try's error, or the
// error of a wait ctx's end stopped.
func (sh *lcShard) lock(ctx context.Context) error {
	var err error
	for k := range lcLockTries {
		if err = sh.tryLock(ctx); err == nil {
			return nil
		}
		if serr := sh.w.sleep(ctx, time.Duration(k)*lcLockBackoff); serr != nil {
			return errors.Join(err, serr)
		}
	}
	return fmt.Errorf("locking lifecycle shard %s: %w", sh.oid, err)
}

// tryLock takes the shard's lc_process lock with the op radosgw's
// LCRadosSerializer sends through Lock::lock_exclusive: one write op holding
// cls_lock's lock, exclusive, under the worker's cookie, with no tag, no
// description, rgw_lc_lock_max_time and no flags
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/lock/cls_lock_client.cc#L32-L61 at v19.2.7
// and https://github.com/ceph/ceph/blob/v19.2.7/src/cls/lock/cls_lock_client.cc#L241-L250,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/lock/cls_lock_client.cc#L32-L61 at v20.2.4
// and https://github.com/ceph/ceph/blob/v20.2.4/src/cls/lock/cls_lock_client.cc#L241-L250),
// the op rados_lock_exclusive sends too. It goes as a write op, which
// goceph's callback and pipe completion modes stop waiting for at ctx's
// end, where rados_lock_exclusive waits for the OSD whatever ctx does
// (docs/cgo-limitations.md). Its sync mode, --rados-completions=sync, waits
// for the OSD as rados_lock_exclusive does, and in every mode the client's
// Close still waits for an op left in flight
// ("Cancellation stops only the client"). A lock left in flight that lands
// later holds the shard until rgw_lc_lock_max_time lapses it.
func (sh *lcShard) tryLock(ctx context.Context) error {
	w := radosclient.NewWriteOp()
	lock.Lock(w, lock.LockOp{Name: lcLockName, Type: lock.TypeExclusive, Cookie: sh.w.cookie, Duration: sh.w.s.opts.lcLockMaxTime}, sh.w.s.release)
	_, err := sh.pool.Write(ctx, sh.oid, w, radosclient.OpFlagNone)
	return err
}

// unlock releases the shard's lock under the worker's cookie with cls_lock's
// unlock, as Lock::unlock sends it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/lock/cls_lock_client.cc#L63-L81 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/lock/cls_lock_client.cc#L63-L81 at v20.2.4),
// as a write op, as tryLock sends its lock, on a context detached from ctx
// and bounded by bound, so a stopping worker still releases the shard. A
// failure is logged; the lock then lapses after rgw_lc_lock_max_time.
func (sh *lcShard) unlock(ctx context.Context, bound time.Duration) {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bound)
	defer cancel()
	w := radosclient.NewWriteOp()
	lock.Unlock(w, lcLockName, sh.w.cookie, sh.w.s.release)
	if _, err := sh.pool.Write(uctx, sh.oid, w, radosclient.OpFlagNone); err != nil {
		slog.DebugContext(ctx, "lifecycle shard unlock failed", slog.String("shard", sh.oid), slog.Int("worker", sh.w.ix), slog.Any("error", err))
	}
}

// unlockBound is the bound an unlock gets: lcUnlockTimeout while the
// worker runs, and stopBound once ctx, its life, has ended, so a stopping
// worker gives up on an unlock within the stop's own bound.
func (w *lcWorker) unlockBound(ctx context.Context) time.Duration {
	if goingDown(ctx) {
		return w.stopBound
	}
	return lcUnlockTimeout
}

// lcLockHeld reports whether err is a lock another holder keeps, EBUSY, or
// this cookie's, EEXIST, or the end of the worker's life: a shard busy, as
// several workers per gateway make ordinary. radosgw logs every failure to
// lock at level 0
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2282-L2287 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2347-L2352 at v20.2.4);
// rgw-go warns of the others, which keep lifecycle from running.
func lcLockHeld(err error) bool {
	e := errno(err)
	return e == syscall.EBUSY || e == syscall.EEXIST || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// run is the locked loop of the shard pass. It reports whether the worker
// still holds the lock, which a pass ends without once it could not take
// the lock again after a bucket, or once ctx ended during one.
func (sh *lcShard) run(ctx context.Context) bool {
	w := sh.w
	for {
		now := w.now()
		head, err := lcGetHead(ctx, sh.pool, sh.oid)
		if err != nil {
			sh.logFailure(ctx, "reading the shard's head", err)
			return true
		}
		var entry rgwcls.LCEntry
		if head.Marker == "" && w.allowRollover(now, head.ShardRolloverDate) {
			page, lerr := lcListEntries(ctx, sh.pool, sh.oid, head.Marker, 1)
			if lerr != nil {
				sh.logFailure(ctx, "listing the shard's first entry", lerr)
				return true
			}
			if len(page.Entries) > 0 {
				entry = page.Entries[0]
				head.Marker, head.StartDate, head.ShardRolloverDate = entry.Bucket, lcSeconds(now), 0
			}
		} else {
			entry, err = sh.headEntry(ctx, head.Marker)
			if errors.Is(err, radosclient.ErrNotFound) {
				if !sh.updateHead(ctx, &head, head.Marker, now) {
					return true
				}
				continue
			}
			if err != nil {
				sh.logFailure(ctx, "reading the head's entry", err)
				return true
			}
		}
		if entry.Bucket == "" {
			return true
		}
		if !w.due(entry, now) {
			if !sh.updateHead(ctx, &head, entry.Bucket, now) {
				return true
			}
			continue
		}
		if !sh.claimable(ctx, entry.Bucket) {
			return sh.walkPast(ctx, entry.Bucket)
		}
		c, err := sh.claim(ctx, entry.Bucket, now)
		if err == nil {
			err = sh.advanceHead(ctx, &head, entry.Bucket, now)
		}
		if err != nil {
			return sh.claimFailed(ctx, c)
		}
		locked, goOn := sh.process(ctx, c)
		if !goOn {
			return locked
		}
		if sh.shardDone(ctx, &head) || goingDown(ctx) {
			return true
		}
	}
}

// walkPast is the pass on from refused, an entry the bucket pass may not
// claim: the shard's head stays at it, and a private marker walks the
// entries after it to the shard's end, then from the shard's first entry
// back to it, claiming each due entry the bucket pass may claim, and
// writing no head. radosgw's pass would claim the entry and move the head
// on; leaving the head lets radosgw's worker reach it, and wrapping reaches
// the entries before it, which radosgw's walk reaches only when its head
// does. It reports whether the worker still holds the lock.
func (sh *lcShard) walkPast(ctx context.Context, refused string) bool {
	w := sh.w
	marker, wrapped := refused, false
	for !goingDown(ctx) {
		next, err := sh.nextEntry(ctx, marker)
		if err != nil {
			sh.logFailure(ctx, "reading the next entry", err)
			return true
		}
		if next.Bucket == "" {
			if wrapped {
				return true
			}
			marker, wrapped = "", true
			continue
		}
		if wrapped && next.Bucket >= refused {
			return true
		}
		marker = next.Bucket
		now := w.now()
		if !w.due(next, now) || !sh.claimable(ctx, next.Bucket) {
			continue
		}
		c, err := sh.claim(ctx, next.Bucket, now)
		if err != nil {
			return sh.claimFailed(ctx, c)
		}
		if locked, goOn := sh.process(ctx, c); !goOn {
			return locked
		}
	}
	return true
}

// claimable asks the bucket pass whether the worker may claim key; an
// error is a refusal.
func (sh *lcShard) claimable(ctx context.Context, key string) bool {
	ok, err := sh.w.pass.claimable(ctx, sh.w, key)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not decide a bucket's claim; leaving it unclaimed",
			slog.String("shard", sh.oid), slog.String("bucket", key), slog.Any("error", err))
		return false
	}
	if !ok {
		slog.DebugContext(ctx, "lifecycle leaves a bucket unclaimed", slog.String("shard", sh.oid), slog.String("bucket", key))
	}
	return ok
}

// claim sets key's entry processing since now
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2395-L2403 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2462-L2470 at v20.2.4).
// It returns the claim it tried to make, written or not.
func (sh *lcShard) claim(ctx context.Context, key string, now time.Time) (lcClaim, error) {
	c := lcClaim{shard: sh.idx, entry: rgwcls.LCEntry{Bucket: key, StartTime: lcSeconds(now), Status: rgwcls.LCStatusProcessing}}
	if err := lcSetEntry(ctx, sh.pool, sh.oid, c.entry); err != nil {
		sh.logFailure(ctx, "claiming "+key, err)
		return c, err
	}
	return c, nil
}

// claimFailed ends the pass after a claim or the head's advance after it
// failed, the lock held, and reports whether the worker still holds the
// lock. When ctx, the worker's life, ended meanwhile, the claim may have
// landed, its write left in flight, so recordStop records it failed if it
// did.
func (sh *lcShard) claimFailed(ctx context.Context, c lcClaim) bool {
	if !goingDown(ctx) {
		return true
	}
	sh.recordStop(ctx, c, true)
	return false
}

// process runs the bucket pass on c and records its result
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2414-L2455 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2481-L2522 at v20.2.4):
// the claim read back, the lock released for the pass, and, once the pass
// returns, the lock taken again with the backoff, the claim read back again,
// and the post-state written, all three on a context detached from ctx and
// bounded by stopBound, so a stop that lands meanwhile does not cut them
// short. A pass is stopped with the lock held when a read finds the claim
// lost, and without it when the lock cannot be taken again, leaving the
// entry processing until its session expires. When ctx ends before the
// first read back or during the bucket, recordStop records the claim
// failed. It reports whether the worker holds the lock and whether the pass
// goes on.
func (sh *lcShard) process(ctx context.Context, c lcClaim) (locked, goOn bool) {
	w := sh.w
	if err := w.claimHeld(ctx, c); err != nil {
		if goingDown(ctx) {
			sh.recordStop(ctx, c, true)
			return false, false
		}
		sh.logFailure(ctx, "reading the claim back", err)
		return true, false
	}
	sh.unlock(ctx, w.unlockBound(ctx))
	res := w.pass.run(ctx, w, c, w.stopAt())
	if goingDown(ctx) {
		sh.recordStop(ctx, c, false)
		return false, false
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.stopBound)
	defer cancel()
	if err := sh.lock(dctx); err != nil {
		slog.WarnContext(ctx, "lifecycle could not lock the shard again after a bucket; its entry stays processing",
			slog.String("shard", sh.oid), slog.String("bucket", c.entry.Bucket), slog.Int("worker", w.ix), slog.Any("error", err))
		return false, false
	}
	if err := w.claimHeld(dctx, c); err != nil {
		sh.logClaimNotRead(ctx, false, c, err)
		return true, false
	}
	return true, sh.postState(dctx, c, res)
}

// logClaimNotRead logs a claim read back that failed, after a bucket or as
// the worker stops: lost, when another writer changed the entry, or the
// read's own error.
func (sh *lcShard) logClaimNotRead(ctx context.Context, stopping bool, c lcClaim, err error) {
	attrs := []any{slog.String("shard", sh.oid), slog.String("bucket", c.entry.Bucket), slog.Int("worker", sh.w.ix), slog.Any("error", err)}
	lost := errors.Is(err, errLCClaimLost)
	switch {
	case stopping && lost:
		slog.WarnContext(ctx, "lifecycle stopped during a bucket whose claim is no longer its own; recording nothing", attrs...)
	case stopping:
		slog.WarnContext(ctx, "lifecycle stopped during a bucket and could not read its claim back; recording nothing", attrs...)
	case lost:
		slog.WarnContext(ctx, "lifecycle claim no longer this worker's; recording nothing", attrs...)
	default:
		slog.WarnContext(ctx, "lifecycle could not read its claim back after a bucket; recording nothing", attrs...)
	}
}

// recordStop records c failed, never complete, once ctx, the worker's life,
// ended between the claim and its first read back or during its bucket's
// pass, which was cut short: the bucket is due again at the next pass that
// reaches it. radosgw's shutdown instead hangs in its work pool's drain and
// records nothing, leaving the entry processing for two lifecycle days
// (docs/ceph-upstream-bugs.md, "radosgw's lifecycle worker hangs shutdown in
// WorkPool::drain and claims a bucket after it is told to stop"). On a
// context detached from ctx and bounded by stopBound, it takes the lock
// again with the backoff unless held says the worker holds it, reads the
// claim back, and writes failed; then it releases the lock on a bound of its
// own. A step that fails, or the bound running out, records nothing, as a
// gateway that dies records nothing. In goceph's callback and pipe
// completion modes every RADOS call here returns at its bound whatever the
// cluster does; in its sync mode each waits for the OSD, and in every mode
// the client's Close still waits for an op left in flight
// (docs/cgo-limitations.md, "Cancellation stops only the client"), so a
// lock or unlock on an unavailable PG still holds the gateway's exit. One
// that lands later leaves the shard locked until rgw_lc_lock_max_time
// lapses it.
func (sh *lcShard) recordStop(ctx context.Context, c lcClaim, held bool) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sh.w.stopBound)
	defer cancel()
	attrs := []any{slog.String("shard", sh.oid), slog.String("bucket", c.entry.Bucket), slog.Int("worker", sh.w.ix)}
	if !held {
		if err := sh.lock(dctx); err != nil {
			slog.WarnContext(ctx, "lifecycle stopped during a bucket and could not lock its shard; its entry stays processing",
				append(attrs, slog.Any("error", err))...)
			return
		}
	}
	defer sh.unlock(ctx, sh.w.stopBound)
	if err := sh.w.claimHeld(dctx, c); err != nil {
		sh.logClaimNotRead(ctx, true, c, err)
		return
	}
	e := c.entry
	e.Status = rgwcls.LCStatusFailed
	if err := lcSetEntry(dctx, sh.pool, sh.oid, e); err != nil {
		sh.logFailure(ctx, "recording a stopped bucket failed", err)
		return
	}
	slog.InfoContext(ctx, "lifecycle stopped during a bucket; its entry records failed", attrs...)
}

// postState records the bucket pass's result res on c's entry: a stale
// entry removed, a removal's failure logged and ignored as radosgw ignores
// it, and otherwise the entry failed or complete under the claim's start
// time. It reports whether the pass goes on, which a failed write ends.
func (sh *lcShard) postState(ctx context.Context, c lcClaim, res error) bool {
	if errors.Is(res, errLCStaleEntry) {
		slog.InfoContext(ctx, "lifecycle removes a stale entry", slog.String("shard", sh.oid), slog.String("bucket", c.entry.Bucket))
		if err := lcRmEntry(ctx, sh.pool, sh.oid, c.entry); err != nil {
			sh.logFailure(ctx, "removing a stale entry", err)
		}
		return true
	}
	e := c.entry
	e.Status = rgwcls.LCStatusComplete
	if res != nil {
		e.Status = rgwcls.LCStatusFailed
		slog.WarnContext(ctx, "lifecycle bucket pass failed", slog.String("shard", sh.oid), slog.String("bucket", c.entry.Bucket), slog.Any("error", res))
	}
	if err := lcSetEntry(ctx, sh.pool, sh.oid, e); err != nil {
		sh.logFailure(ctx, "recording a bucket's result", err)
		return false
	}
	return true
}

// updateHead is update_head
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2226-L2250 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2291-L2315 at v20.2.4):
// the head moved past key, and the shard's end checked. It reports whether
// the pass goes on.
func (sh *lcShard) updateHead(ctx context.Context, head *rgwcls.LCHead, key string, now time.Time) bool {
	if sh.advanceHead(ctx, head, key, now) != nil {
		return false
	}
	return !sh.shardDone(ctx, head)
}

// advanceHead is advance_head
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2173-L2201 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2237-L2266 at v20.2.4):
// the head's marker set to the entry after key, "" past the shard's last,
// stamped now, and stored.
func (sh *lcShard) advanceHead(ctx context.Context, head *rgwcls.LCHead, key string, now time.Time) error {
	next, err := sh.nextEntry(ctx, key)
	if err != nil {
		sh.logFailure(ctx, "reading the next entry", err)
		return err
	}
	head.Marker, head.StartDate = next.Bucket, lcSeconds(now)
	if err := lcPutHead(ctx, sh.pool, sh.oid, *head); err != nil {
		sh.logFailure(ctx, "storing the shard's head", err)
		return err
	}
	return nil
}

// shardDone is check_if_shard_done
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2203-L2224 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2268-L2289 at v20.2.4):
// a head past the shard's end gets the rollover date of now, stored with a
// failure logged, and ends the pass.
func (sh *lcShard) shardDone(ctx context.Context, head *rgwcls.LCHead) bool {
	if head.Marker != "" {
		return false
	}
	head.ShardRolloverDate = lcSeconds(sh.w.now())
	if err := lcPutHead(ctx, sh.pool, sh.oid, *head); err != nil {
		sh.logFailure(ctx, "storing the shard's rollover date", err)
	}
	return true
}

// headEntry is lc_get_entry of the head's key. An entry stored in the pair
// form releases through v15.2.4 wrote, which the class answers EIO for
// (docs/ceph-upstream-bugs.md, "cls_rgw's lc_get_entry and
// lc_get_next_entry refuse an lc entry stored in its old pair form, which
// stalls the shard"), is read from the shard's omap as lc_list_entries
// reads a value, an entry else the pair, and written back as an entry
// before the pass goes on. A value that is neither, or a pair naming
// another key, fails the read.
func (sh *lcShard) headEntry(ctx context.Context, key string) (rgwcls.LCEntry, error) {
	e, err := lcGetEntry(ctx, sh.pool, sh.oid, key)
	if errno(err) != syscall.EIO {
		return e, err
	}
	r := radosclient.NewReadOp()
	res := r.OmapGetValsByKeys([]string{key})
	if _, rerr := sh.pool.Read(ctx, sh.oid, r, radosclient.OpFlagNone); rerr != nil {
		return rgwcls.LCEntry{}, errors.Join(err, rerr)
	}
	b, ok := res.Values[key]
	if res.Err != nil || !ok {
		return rgwcls.LCEntry{}, errors.Join(err, res.Err)
	}
	pe, ok := lcListedValue(b)
	if !ok || pe.Bucket != key {
		return rgwcls.LCEntry{}, fmt.Errorf("the value under %s is not its lifecycle entry in either form: %w", key, err)
	}
	return sh.rewritePair(ctx, pe)
}

// nextEntry is lc_get_next_entry after marker, the empty entry past the
// shard's last. A next entry the class answers EIO for is read from the
// shard's omap, the first key after marker and its value, as headEntry
// reads the head's: a pair naming that key is written back as an entry and
// the next entry read again, and a value that is neither, or a pair naming
// another key, fails the read and is left as it is. lc_list_entries is not
// used for it: it names a pair's entry by the pair's bucket, not its key.
func (sh *lcShard) nextEntry(ctx context.Context, marker string) (rgwcls.LCEntry, error) {
	e, err := lcGetNextEntry(ctx, sh.pool, sh.oid, marker)
	if errno(err) != syscall.EIO {
		return e, err
	}
	r := radosclient.NewReadOp()
	res := r.OmapGetVals(marker, "", 1)
	if _, rerr := sh.pool.Read(ctx, sh.oid, r, radosclient.OpFlagNone); rerr != nil {
		return rgwcls.LCEntry{}, errors.Join(err, rerr)
	}
	if res.Err != nil || len(res.Values) != 1 {
		return rgwcls.LCEntry{}, errors.Join(err, res.Err)
	}
	key := slices.Collect(maps.Keys(res.Values))[0]
	pe, ok := lcListedValue(res.Values[key])
	if !ok || pe.Bucket != key {
		return rgwcls.LCEntry{}, fmt.Errorf("the value under %s is not its lifecycle entry in either form: %w", key, err)
	}
	if _, werr := sh.rewritePair(ctx, pe); werr != nil {
		return rgwcls.LCEntry{}, werr
	}
	return lcGetNextEntry(ctx, sh.pool, sh.oid, marker)
}

// rewritePair stores pe, an entry read from its pair form, as the entry
// {bucket, 0, status}, the form radosgw stores from v15.2.5.
func (sh *lcShard) rewritePair(ctx context.Context, pe rgwcls.LCEntry) (rgwcls.LCEntry, error) {
	e := rgwcls.LCEntry{Bucket: pe.Bucket, Status: pe.Status}
	if err := lcSetEntry(ctx, sh.pool, sh.oid, e); err != nil {
		return rgwcls.LCEntry{}, fmt.Errorf("rewriting the pair-form entry %s: %w", pe.Bucket, err)
	}
	slog.InfoContext(ctx, "lifecycle rewrote an entry stored in its pair form", slog.String("shard", sh.oid), slog.String("bucket", pe.Bucket))
	return e, nil
}

// lcListedValue reads an entry's stored value as rgw_cls_lc_list_entries
// does, "try backward compat": as cls_rgw_lc_entry, else as the pair of
// bucket and status with no start time
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4291-L4310 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4693-L4712 at v20.2.4).
// It reports false when the value is neither.
func lcListedValue(b []byte) (rgwcls.LCEntry, bool) {
	d := denc.NewDecoder(b)
	if e := rgwcls.DecodeLCEntry(d); d.Err() == nil {
		return e, true
	}
	d = denc.NewDecoder(b)
	e := rgwcls.LCEntry{Bucket: d.String(), Status: d.U32()}
	return e, d.Err() == nil
}

// logFailure logs the failure of a step of the shard pass, which ends it or
// is ignored as radosgw ignores it.
func (sh *lcShard) logFailure(ctx context.Context, step string, err error) {
	slog.WarnContext(ctx, "lifecycle shard pass step failed",
		slog.String("shard", sh.oid), slog.Int("worker", sh.w.ix), slog.String("step", step), slog.Any("error", err))
}

// lcLoadBucket loads the bucket an entry key names, as bucket_lc_process
// does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1687-L1718 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1743-L1774 at v20.2.4):
// the key's tenant and name, then a marker check. A bucket that does not
// load, or loads with another marker, as a bucket re-created under the
// name does, is errLCStaleEntry, both of radosgw's -ENOENT. A key of fewer
// than three parts names no bucket either; radosgw indexes past it
// (docs/ceph-upstream-bugs.md, "radosgw's bucket_lc_process indexes past a
// lifecycle entry key of fewer than three parts").
func (s *Store) lcLoadBucket(ctx context.Context, key string) (*op.BucketRecord, error) {
	parts := strings.Split(key, ":")
	if len(parts) < 3 {
		return nil, fmt.Errorf("%w: key %q", errLCStaleEntry, key)
	}
	rec, err := s.GetBucket(ctx, parts[0], parts[1])
	if errors.Is(err, op.ErrNoSuchBucket) {
		return nil, fmt.Errorf("%w: %s does not load: %w", errLCStaleEntry, key, err)
	}
	if err != nil {
		return nil, fmt.Errorf("loading the bucket of lifecycle entry %s: %w", key, err)
	}
	if rec.Info.Bucket.Marker != parts[2] {
		return nil, fmt.Errorf("%w: %s now has the marker %s", errLCStaleEntry, key, rec.Info.Bucket.Marker)
	}
	return rec, nil
}
