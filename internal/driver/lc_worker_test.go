package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // the zone specs load zones a host may lack

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// lcClock is the clock the worker specs run a cluster's locks and the
// workers on.
type lcClock struct {
	mu sync.Mutex
	t  time.Time
}

func (k *lcClock) now() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.t
}

func (k *lcClock) advance(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.t = k.t.Add(d)
}

// lcWaits records the waits a worker asks for, each answered by on, nil
// for at once.
type lcWaits struct {
	mu    sync.Mutex
	waits []time.Duration
	on    func(ctx context.Context, d time.Duration) error
}

func (f *lcWaits) sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.waits = append(f.waits, d)
	on := f.on
	f.mu.Unlock()
	if on != nil {
		return on(ctx, d)
	}
	return nil
}

func (f *lcWaits) recorded() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.waits)
}

// lcRun is one bucket pass a recorder ran.
type lcRun struct {
	cookie string
	claim  lcClaim
	stopAt time.Time
}

// lcRecorder is the bucket pass of the worker specs: it refuses the keys in
// refused, records each run, and answers result's error for its key, or
// what during returns when set.
type lcRecorder struct {
	mu      sync.Mutex
	refused map[string]bool
	result  map[string]error
	during  func(ctx context.Context, w *lcWorker, c lcClaim) error
	runs    []lcRun
}

func (r *lcRecorder) claimable(_ context.Context, _ *lcWorker, key string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.refused[key], nil
}

func (r *lcRecorder) run(ctx context.Context, w *lcWorker, c lcClaim, stopAt time.Time) error {
	r.mu.Lock()
	r.runs = append(r.runs, lcRun{cookie: w.cookie, claim: c, stopAt: stopAt})
	during, res := r.during, r.result[c.entry.Bucket]
	r.mu.Unlock()
	if during != nil {
		return during(ctx, w, c)
	}
	return res
}

// keys returns the keys of the runs since the last take, in order, and
// forgets them.
func (r *lcRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.runs))
	for _, run := range r.runs {
		keys = append(keys, run.claim.entry.Bucket)
	}
	r.runs = nil
	return keys
}

func (r *lcRecorder) lastRun() lcRun {
	GinkgoHelper()
	r.mu.Lock()
	defer r.mu.Unlock()
	Expect(r.runs).NotTo(BeEmpty())
	return r.runs[len(r.runs)-1]
}

// lcClient is another RADOS client on the cluster: cls_lock keys a holder
// by its client entity and cookie, and every fake request comes from one
// entity, so this client's cookies carry its name.
type lcClient struct {
	*fakerados.Cluster
	name string
}

func (c lcClient) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return lcClientPool{Pool: p, name: c.name}, nil
}

type lcClientPool struct {
	radosclient.Pool
	name string
}

func (p lcClientPool) LockExclusive(ctx context.Context, oid, name, cookie, desc string, d time.Duration, f radosclient.LockFlags) error {
	return p.Pool.LockExclusive(ctx, oid, name, p.name+"/"+cookie, desc, d, f)
}

func (p lcClientPool) Unlock(ctx context.Context, oid, name, cookie string) error {
	return p.Pool.Unlock(ctx, oid, name, p.name+"/"+cookie)
}

// Write puts the client's name on the cookie of each cls_lock lock and
// unlock the op holds, as LockExclusive and Unlock do.
func (p lcClientPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, f radosclient.OpFlags) (uint64, error) {
	for _, st := range op.Steps() {
		e, ok := st.(*radosclient.ExecStep)
		if !ok || e.Class != lock.Class {
			continue
		}
		switch e.Method {
		case "lock":
			lo := lock.DecodeLockOp(denc.NewDecoder(e.In))
			lo.Cookie = p.name + "/" + lo.Cookie
			e.In = encodeLC(lo)
		case "unlock":
			uo := lock.DecodeUnlockOp(denc.NewDecoder(e.In))
			uo.Cookie = p.name + "/" + uo.Cookie
			e.In = encodeLC(uo)
		}
	}
	return p.Pool.Write(ctx, oid, op, f)
}

// lcHookFns are what a spec sets on an lcHookPool, each given an op's
// class.method calls, a lock call's as lock.lock or lock.unlock and a read's
// as read: before runs ahead of the op, given its context, after once it
// has run, and hangs reports whether it hangs. A hung read or write models
// goceph's async op on a PG that is unavailable: it waits for ctx's end and
// abandons the op. A hung LockExclusive or Unlock models go-ceph's
// synchronous call: it waits until release, whatever ctx does.
type lcHookFns struct {
	before func(ctx context.Context, calls []string)
	after  func(calls []string)
	hangs  func(calls []string) bool
}

type lcHooks struct {
	mu      sync.Mutex
	fns     lcHookFns
	release chan struct{}
}

func (h *lcHooks) set(fns lcHookFns) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fns = fns
}

func (h *lcHooks) get() lcHookFns {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fns
}

// at runs the before hook and reports whether calls hang.
func (h *lcHooks) at(ctx context.Context, calls []string) bool {
	fns := h.get()
	if fns.before != nil {
		fns.before(ctx, calls)
	}
	return fns.hangs != nil && fns.hangs(calls)
}

// done runs the after hook.
func (h *lcHooks) done(calls []string) {
	if fns := h.get(); fns.after != nil {
		fns.after(calls)
	}
}

type lcHookCluster struct {
	*fakerados.Cluster
	h *lcHooks
}

func (c lcHookCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return lcHookPool{Pool: p, h: c.h}, nil
}

type lcHookPool struct {
	radosclient.Pool
	h *lcHooks
}

func (p lcHookPool) Read(ctx context.Context, oid string, op *radosclient.ReadOp, f radosclient.OpFlags) (uint64, error) {
	if p.h.at(ctx, []string{"read"}) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	v, err := p.Pool.Read(ctx, oid, op, f)
	p.h.done([]string{"read"})
	return v, err
}

func (p lcHookPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, f radosclient.OpFlags) (uint64, error) {
	var calls []string
	for _, st := range op.Steps() {
		if e, ok := st.(*radosclient.ExecStep); ok {
			calls = append(calls, e.Class+"."+e.Method)
		}
	}
	if p.h.at(ctx, calls) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	v, err := p.Pool.Write(ctx, oid, op, f)
	p.h.done(calls)
	return v, err
}

func (p lcHookPool) LockExclusive(ctx context.Context, oid, name, cookie, desc string, d time.Duration, f radosclient.LockFlags) error {
	if p.h.at(ctx, []string{"lock.lock"}) {
		<-p.h.release
	}
	return p.Pool.LockExclusive(ctx, oid, name, cookie, desc, d, f)
}

func (p lcHookPool) Unlock(ctx context.Context, oid, name, cookie string) error {
	if p.h.at(ctx, []string{"lock.unlock"}) {
		<-p.h.release
	}
	return p.Pool.Unlock(ctx, oid, name, cookie)
}

// newLCCluster is a cluster with the rgw and lock classes.
func newLCCluster() *fakerados.Cluster {
	c := fakerados.New()
	c.RegisterClassMethods("rgw", fakerados.RGWClass(), fakerados.RGWMethods, fakerados.RGWWriteMethods)
	c.RegisterClass("lock", fakerados.LockClass(), fakerados.LockWriteMethods...)
	return c
}

// newLCWorkerStore is a Store holding what the lifecycle worker reads, with
// radosgw's defaults: one worker, a 90 s lock, the window 00:00-06:00, no
// debug interval and sessions cleared.
func newLCWorkerStore(c radosclient.Cluster, shards uint32) *Store {
	return &Store{
		pools: newPoolCache(c),
		zone:  &zoneConfig{Params: meta.ZoneParams{LCPool: meta.Pool{Name: lcSpecPool, NS: lcSpecNS}}},
		opts: options{
			lcMaxObjs: shards, lcThreads: true, lcMaxWorker: 1, lcMaxWPWorker: 1, lcLockMaxTime: 90 * time.Second,
			lcDebugInterval: -time.Second, lcWorkTime: lcWorkTime{endHour: 6}, lcAutoSessionClear: true,
		},
	}
}

func identityPerm(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return p
}

// lcWriteCalls is every class call written to the shard, as class.method.
func lcWriteCalls(c *fakerados.Cluster, oid string) []string {
	var calls []string
	for _, w := range c.WritesTo(lcSpecPool, lcSpecNS, oid) {
		for _, st := range w.Steps() {
			if e, ok := st.(*radosclient.ExecStep); ok {
				calls = append(calls, e.Class+"."+e.Method)
			}
		}
	}
	return calls
}

// failNthWrite fails the n-th write op on oid from now with errno.
func failNthWrite(c *fakerados.Cluster, oid string, n int, errno syscall.Errno) {
	seen := 0
	c.BeforeWrite(lcSpecPool, lcSpecNS, oid, func(*fakerados.Object) {
		if seen++; seen == n {
			c.FailNextWrite(lcSpecPool, lcSpecNS, oid, errno)
		}
	})
}

// lcPairValue is an entry in the pair form releases through v15.2.4 stored,
// pair<string, int> of bucket and status.
func lcPairValue(key string, status uint32) []byte {
	e := denc.NewEncoder()
	e.String(key)
	e.U32(status)
	return e.Bytes()
}

// lcKeyOn is an entry key "<prefix>…:1" that files on shard idx of shards.
func lcKeyOn(prefix string, idx, shards uint32) string {
	for n := 0; ; n++ {
		if key := prefix + strconv.Itoa(n) + ":1"; lcShardIndex(key, shards) == idx {
			return key
		}
	}
}

// radosgwShardPass is RGWLC::process(int index, ...) as radosgw runs it,
// unchanged: no claim predicate, no claim read back, no pair rewrite, and
// the head written after every claim and skip. bucket is its
// bucket_lc_process.
func radosgwShardPass(ctx context.Context, w *lcWorker, idx uint32, bucket func(key string) error) {
	GinkgoHelper()
	pool, err := w.s.lcPool(ctx)
	Expect(err).NotTo(HaveOccurred())
	oid := lcShardOID(idx)
	lock := func() bool {
		for k := range 5 {
			if pool.LockExclusive(ctx, oid, lcLockName, w.cookie, "", w.s.opts.lcLockMaxTime, 0) == nil {
				return true
			}
			_ = w.sleep(ctx, time.Duration(k)*100*time.Millisecond) //nolint:errcheck // the spec's waits return at once
		}
		return false
	}
	held := false
	lockHeld := func() bool {
		held = lock()
		return held
	}
	unlock := func() {
		Expect(pool.Unlock(ctx, oid, lcLockName, w.cookie)).To(Succeed())
		held = false
	}
	advance := func(head *rgwcls.LCHead, key string, now time.Time) bool {
		next, err := lcGetNextEntry(ctx, pool, oid, key)
		if err != nil {
			return false
		}
		head.Marker, head.StartDate = next.Bucket, lcSeconds(now)
		return lcPutHead(ctx, pool, oid, *head) == nil
	}
	done := func(head *rgwcls.LCHead) bool {
		if head.Marker != "" {
			return false
		}
		head.ShardRolloverDate = lcSeconds(w.now())
		_ = lcPutHead(ctx, pool, oid, *head) //nolint:errcheck // radosgw logs it
		return true
	}
	if !lockHeld() {
		return
	}
	defer func() {
		if held {
			unlock()
		}
	}()
	var entry rgwcls.LCEntry
	for {
		now := w.now()
		head, err := lcGetHead(ctx, pool, oid)
		if err != nil {
			return
		}
		if head.Marker == "" && w.allowRollover(now, head.ShardRolloverDate) {
			page, lerr := lcListEntries(ctx, pool, oid, head.Marker, 1)
			if lerr != nil {
				return
			}
			if len(page.Entries) > 0 {
				entry = page.Entries[0]
				head.Marker, head.StartDate, head.ShardRolloverDate = entry.Bucket, lcSeconds(now), 0
			}
		} else {
			entry, err = lcGetEntry(ctx, pool, oid, head.Marker)
			if errors.Is(err, radosclient.ErrNotFound) {
				if !advance(&head, head.Marker, now) || done(&head) {
					return
				}
				continue
			}
			if err != nil {
				return
			}
		}
		if entry.Bucket == "" {
			return
		}
		if !w.due(entry, now) {
			if !advance(&head, entry.Bucket, now) || done(&head) {
				return
			}
			continue
		}
		entry.Status, entry.StartTime = rgwcls.LCStatusProcessing, lcSeconds(now)
		if lcSetEntry(ctx, pool, oid, entry) != nil || !advance(&head, entry.Bucket, now) {
			return
		}
		unlock()
		ret := bucket(entry.Bucket)
		if !lockHeld() {
			return
		}
		if errors.Is(ret, errLCStaleEntry) {
			_ = lcRmEntry(ctx, pool, oid, entry) //nolint:errcheck // radosgw logs it
		} else {
			entry.Status = rgwcls.LCStatusComplete
			if ret != nil {
				entry.Status = rgwcls.LCStatusFailed
			}
			if lcSetEntry(ctx, pool, oid, entry) != nil {
				return
			}
		}
		if done(&head) {
			return
		}
	}
}

// lcIST is five and a half hours ahead of UTC, so a local day and a UTC
// day part at 18:30Z, and lcT0 is 01:30 there on 2026-10-21, inside the
// default work window.
var (
	lcIST = time.FixedZone("IST", 5*3600+1800)
	lcT0  = time.Date(2026, 10, 21, 1, 30, 0, 0, lcIST)
)

var _ = Describe("the lifecycle worker", func() {
	var (
		c     *fakerados.Cluster
		clock *lcClock
		s     *Store
		rec   *lcRecorder
		waits *lcWaits
		ws    *lcWorkers
		w     *lcWorker
	)
	BeforeEach(func() {
		c = newLCCluster()
		clock = &lcClock{t: lcT0}
		c.SetClock(clock.now)
		s = newLCWorkerStore(c, 1)
		rec = &lcRecorder{}
		waits = &lcWaits{}
		ws = newLCWorkers(s, rec)
		ws.now, ws.loc, ws.sleep, ws.perm = clock.now, lcIST, waits.sleep, identityPerm
		w = ws.worker(0)
	})

	pool := func(ctx context.Context) radosclient.Pool {
		GinkgoHelper()
		p, err := s.lcPool(ctx)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	oidOf := func(key string) string { return lcShardOID(lcShardIndex(key, s.opts.lcMaxObjs)) }
	setEntry := func(ctx context.Context, e rgwcls.LCEntry) {
		GinkgoHelper()
		Expect(lcSetEntry(ctx, pool(ctx), oidOf(e.Bucket), e)).To(Succeed())
	}
	entryOf := func(key string) rgwcls.LCEntry {
		GinkgoHelper()
		return lcStoredEntry(c, oidOf(key), key)
	}
	headOf := func(ctx context.Context, oid string) rgwcls.LCHead {
		GinkgoHelper()
		h, err := lcGetHead(ctx, pool(ctx), oid)
		Expect(err).NotTo(HaveOccurred())
		return h
	}
	putHead := func(ctx context.Context, oid string, h rgwcls.LCHead) {
		GinkgoHelper()
		Expect(lcPutHead(ctx, pool(ctx), oid, h)).To(Succeed())
	}
	putRaw := func(ctx context.Context, oid, key string, value []byte) {
		GinkgoHelper()
		op := radosclient.NewWriteOp()
		op.OmapSet(map[string][]byte{key: value})
		_, err := pool(ctx).Write(ctx, oid, op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
	}
	uninitial := func(key string) rgwcls.LCEntry { return rgwcls.LCEntry{Bucket: key} }
	now := func() uint64 { return lcSeconds(clock.now()) }
	headWrites := func(oid string) int {
		n := 0
		for _, call := range lcWriteCalls(c, oid) {
			if call == "rgw.lc_put_head" {
				n++
			}
		}
		return n
	}

	Describe("the schedule", func() {
		DescribeTable("works inside rgw_lifecycle_work_time, local time, both ends included",
			func(window lcWorkTime, at time.Time, want bool) {
				s.opts.lcWorkTime = window
				Expect(w.shouldWork(at)).To(Equal(want))
			},
			Entry("01:30 local in 00:00-06:00, 20:00Z the day before", lcWorkTime{endHour: 6}, time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC), true),
			Entry("06:30 local in 00:00-06:00, 01:00Z", lcWorkTime{endHour: 6}, time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC), false),
			Entry("06:00:59 local, the end minute", lcWorkTime{endHour: 6}, time.Date(2026, 10, 21, 6, 0, 59, 0, lcIST), true),
			Entry("06:01 local", lcWorkTime{endHour: 6}, time.Date(2026, 10, 21, 6, 1, 0, 0, lcIST), false),
			Entry("23:00 local in 22:30-02:15", lcWorkTime{22, 30, 2, 15}, time.Date(2026, 10, 21, 23, 0, 0, 0, lcIST), true),
			Entry("01:00 local in 22:30-02:15, past midnight", lcWorkTime{22, 30, 2, 15}, time.Date(2026, 10, 21, 1, 0, 0, 0, lcIST), true),
			Entry("02:15 local in 22:30-02:15", lcWorkTime{22, 30, 2, 15}, time.Date(2026, 10, 21, 2, 15, 0, 0, lcIST), true),
			Entry("02:16 local in 22:30-02:15", lcWorkTime{22, 30, 2, 15}, time.Date(2026, 10, 21, 2, 16, 0, 0, lcIST), false),
			Entry("22:29 local in 22:30-02:15", lcWorkTime{22, 30, 2, 15}, time.Date(2026, 10, 21, 22, 29, 0, 0, lcIST), false),
		)

		It("always works in debug mode", func() {
			s.opts.lcDebugInterval = 10 * time.Second
			Expect(w.shouldWork(time.Date(2026, 10, 21, 12, 0, 0, 0, lcIST))).To(BeTrue())
		})

		DescribeTable("waits until the window's next local start",
			func(window lcWorkTime, end time.Time, want time.Duration) {
				s.opts.lcWorkTime = window
				Expect(w.nextStart(end.Add(-time.Hour), end)).To(Equal(want))
			},
			Entry("from 23:30 local, 18:00Z, to 00:00 local", lcWorkTime{endHour: 6}, time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC), 30*time.Minute),
			Entry("from 01:30 local to the next day's 00:00", lcWorkTime{endHour: 6}, lcT0, 22*time.Hour+30*time.Minute),
			Entry("from the start itself, a day", lcWorkTime{endHour: 6}, time.Date(2026, 10, 21, 0, 0, 0, 0, lcIST), 24*time.Hour),
			Entry("from 21:00 local to 22:30 local", lcWorkTime{22, 30, 2, 15}, time.Date(2026, 10, 21, 21, 0, 0, 0, lcIST), 90*time.Minute),
			Entry("the end's fraction dropped, as time_t drops it", lcWorkTime{endHour: 6}, time.Date(2026, 10, 21, 23, 59, 59, 500e6, lcIST), time.Second),
		)

		DescribeTable("in debug mode waits what remains of the interval since the pass started, in whole seconds",
			func(took, want time.Duration) {
				s.opts.lcDebugInterval = 10 * time.Second
				Expect(w.nextStart(lcT0, lcT0.Add(took))).To(Equal(want))
			},
			Entry("3.5 s taken", 3500*time.Millisecond, 6*time.Second),
			Entry("the whole interval taken", 10*time.Second, time.Duration(0)),
			Entry("more than the interval taken", 12*time.Second, time.Duration(0)),
		)

		It("passes when the window holds at the start, then waits for the next start, until ctx ends", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			wctx, cancel := context.WithCancel(ctx)
			defer cancel()
			waits.on = func(_ context.Context, d time.Duration) error {
				clock.advance(d)
				if len(waits.recorded()) == 3 {
					cancel()
					return context.Canceled
				}
				return nil
			}
			w.run(wctx)
			Expect(waits.recorded()).To(Equal([]time.Duration{22*time.Hour + 30*time.Minute, 24 * time.Hour, 24 * time.Hour}))
			Expect(rec.take()).To(Equal([]string{":a:1", ":a:1", ":a:1"}), "a pass at the start, then one at each next 00:00")
		})

		It("passes nothing outside the window", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			clock.advance(12 * time.Hour)
			wctx, cancel := context.WithCancel(ctx)
			defer cancel()
			waits.on = func(context.Context, time.Duration) error {
				cancel()
				return context.Canceled
			}
			w.run(wctx)
			Expect(rec.take()).To(BeEmpty())
			Expect(waits.recorded()).To(Equal([]time.Duration{10*time.Hour + 30*time.Minute}))
		})
	})

	Describe("startup", func() {
		It("names worker ix's cookie lc_thrd: <ix>", func() {
			Expect(ws.worker(0).cookie).To(Equal("lc_thrd: 0"))
			Expect(ws.worker(2).cookie).To(Equal("lc_thrd: 2"))
		})

		It("runs rgw_lc_max_worker workers until the root context ends", func(ctx SpecContext) {
			s.opts.lcMaxWorker, s.opts.lcMaxObjs = 3, 3
			s.opts.lcDebugInterval = time.Hour
			for i := range uint32(3) {
				setEntry(ctx, uninitial(lcKeyOn(":b", i, 3)))
			}
			ws.perm = rand.Perm
			ws.sleep = func(ctx context.Context, d time.Duration) error {
				if d < time.Second {
					return sleepCtx(ctx, d)
				}
				<-ctx.Done()
				return ctx.Err()
			}
			arrived := make(chan string, 3)
			release := make(chan struct{})
			rec.during = func(ctx context.Context, w *lcWorker, _ lcClaim) error {
				arrived <- w.cookie
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil
			}
			rctx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- ws.run(rctx) }()
			var cookies []string
			for range 3 {
				var cookie string
				Eventually(arrived).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(Receive(&cookie))
				cookies = append(cookies, cookie)
			}
			Expect(cookies).To(ConsistOf("lc_thrd: 0", "lc_thrd: 1", "lc_thrd: 2"), "each worker held one claim at once")
			close(release)
			cancel()
			Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
		})
	})

	Describe("a pass", func() {
		It("visits every shard once, in a fresh random order each pass", func(ctx SpecContext) {
			s.opts.lcMaxObjs = 8
			ws.perm = rand.Perm
			var order []string
			for i := range uint32(8) {
				oid := lcShardOID(i)
				c.BeforeRead(lcSpecPool, lcSpecNS, oid, func() { order = append(order, oid) })
			}
			var orders [][]string
			for range 6 {
				order = nil
				w.process(ctx)
				order = slices.Compact(order)
				Expect(order).To(ConsistOf("lc.0", "lc.1", "lc.2", "lc.3", "lc.4", "lc.5", "lc.6", "lc.7"))
				orders = append(orders, order)
			}
			Expect(slices.ContainsFunc(orders[1:], func(o []string) bool { return !slices.Equal(o, orders[0]) })).
				To(BeTrue(), "six passes in one order: %v", orders)
		})

		It("skips a shard another holder keeps locked through five tries, waiting 0, 100, 200, 300 and 400 ms", func(ctx SpecContext) {
			s.opts.lcMaxObjs = 2
			held, free := lcKeyOn(":a", 0, 2), lcKeyOn(":b", 1, 2)
			setEntry(ctx, uninitial(held))
			setEntry(ctx, uninitial(free))
			Expect(pool(ctx).LockExclusive(ctx, "lc.0", lcLockName, "lc_thrd: 0 of another gateway", "", time.Hour, 0)).To(Succeed())
			c.ResetCounters()
			w.process(ctx)
			Expect(waits.recorded()).To(Equal([]time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond, 400 * time.Millisecond}))
			Expect(rec.take()).To(Equal([]string{free}), "the other shard is passed")
			Expect(entryOf(held)).To(Equal(uninitial(held)))
			Expect(c.Reads(lcSpecPool, lcSpecNS, "lc.0")).To(BeZero(), "nothing read under another's lock")
		})

		It("warns with the lock's error when the shard lock fails other than as held, and not when it is held", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			logs := &bytes.Buffer{}
			DeferCleanup(captureLog(logs))
			Expect(pool(ctx).LockExclusive(ctx, "lc.0", lcLockName, "lc_thrd: 0 of another gateway", "", time.Hour, 0)).To(Succeed())
			w.process(ctx)
			Expect(logs.String()).To(BeEmpty(), "a held shard is no warning")
			c.RegisterClass("lock", func(*fakerados.ClassCall) ([]byte, int32) { return nil, -int32(syscall.EIO) }, fakerados.LockWriteMethods...)
			w.process(ctx)
			Expect(logs.String()).To(And(ContainSubstring(`"level":"WARN"`), ContainSubstring(`"shard":"lc.0"`), ContainSubstring("input/output error")))
			Expect(rec.take()).To(BeEmpty())
		})

		It("skips a shard whose head does not read, releasing the lock", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			c.FailNextRead(lcSpecPool, lcSpecNS, "lc.0", syscall.EIO)
			w.process(ctx)
			Expect(rec.take()).To(BeEmpty())
			Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty())
		})

		It("claims the first entry, then moves the head on, under the lock, and unlocks for the bucket pass", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			setEntry(ctx, uninitial(":b:1"))
			c.ResetCounters()
			second := lcClient{Cluster: c, name: "client.2"}
			secondPool, err := second.Pool(ctx, lcSpecPool, lcSpecNS)
			Expect(err).NotTo(HaveOccurred())
			var held [][]radosclient.Locker
			c.BeforeWrite(lcSpecPool, lcSpecNS, "lc.0", func(*fakerados.Object) {
				held = append(held, c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName))
			})
			var during []string
			rec.during = func(ctx context.Context, _ *lcWorker, cl lcClaim) error {
				if cl.entry.Bucket != ":a:1" {
					return nil
				}
				Expect(entryOf(":a:1")).To(Equal(rgwcls.LCEntry{Bucket: ":a:1", StartTime: now(), Status: rgwcls.LCStatusProcessing}))
				Expect(headOf(ctx, "lc.0")).To(Equal(rgwcls.LCHead{StartDate: now(), Marker: ":b:1"}))
				Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty(), "unlocked for the bucket")
				Expect(secondPool.LockExclusive(ctx, "lc.0", lcLockName, "lc_thrd: 0", "", time.Minute, 0)).To(Succeed(), "a second client locks the shard")
				Expect(secondPool.Unlock(ctx, "lc.0", lcLockName, "lc_thrd: 0")).To(Succeed())
				during = lcWriteCalls(c, "lc.0")
				return nil
			}
			w.process(ctx)
			Expect(during).To(Equal([]string{"lock.lock", "rgw.lc_set_entry", "rgw.lc_put_head", "lock.unlock"}), "the claim, then the head, under the lock")
			calls := lcWriteCalls(c, "lc.0")
			Expect(calls).To(Equal(slices.Concat(during,
				[]string{"lock.lock", "rgw.lc_set_entry"},                                   // a's post-state
				[]string{"rgw.lc_set_entry", "rgw.lc_put_head", "lock.unlock"},              // b's claim and head
				[]string{"lock.lock", "rgw.lc_set_entry", "rgw.lc_put_head", "lock.unlock"}, // b's post-state, the rollover
			)))
			Expect(held).To(HaveLen(len(calls)))
			for i, call := range calls {
				if strings.HasPrefix(call, "rgw.") {
					Expect(held[i]).To(ConsistOf(HaveField("Cookie", "lc_thrd: 0")), "write %d, %s, under the worker's lock", i, call)
				}
			}
			Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty(), "released at the end")
			Expect(rec.take()).To(Equal([]string{":a:1", ":b:1"}))
		})

		It("stops before the bucket pass, recording nothing, when the claim read back is not its own", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			setEntry(ctx, uninitial(":b:1"))
			other := rgwcls.LCEntry{Bucket: ":a:1", StartTime: now() + 1, Status: rgwcls.LCStatusProcessing}
			seen := 0
			c.BeforeWrite(lcSpecPool, lcSpecNS, "lc.0", func(o *fakerados.Object) {
				if seen++; seen == 3 { // the lock, the claim, then the head's write: another holder's claim lands with it
					o.Omap[":a:1"] = encodeLC(other)
				}
			})
			w.process(ctx)
			Expect(rec.take()).To(BeEmpty())
			Expect(entryOf(":a:1")).To(Equal(other))
			Expect(entryOf(":b:1")).To(Equal(uninitial(":b:1")), "the shard's pass ended")
			Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty())
		})

		DescribeTable("records the bucket pass's result under the claim's start time",
			func(ctx SpecContext, res error, want *rgwcls.LCEntry) {
				setEntry(ctx, uninitial(":a:1"))
				rec.result = map[string]error{":a:1": res}
				w.process(ctx)
				if want == nil {
					Expect(c.Object(lcSpecPool, lcSpecNS, "lc.0").Omap).NotTo(HaveKey(":a:1"))
					return
				}
				want.StartTime = now()
				Expect(entryOf(":a:1")).To(Equal(*want))
			},
			Entry("success completes it", nil, &rgwcls.LCEntry{Bucket: ":a:1", Status: rgwcls.LCStatusComplete}),
			Entry("a failure fails it", errors.New("listing failed"), &rgwcls.LCEntry{Bucket: ":a:1", Status: rgwcls.LCStatusFailed}),
			Entry("a stale entry is removed", fmt.Errorf("%w: gone", errLCStaleEntry), nil),
		)

		It("goes on past a stale entry whose removal fails", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			setEntry(ctx, uninitial(":b:1"))
			rec.result = map[string]error{":a:1": errLCStaleEntry}
			rec.during = func(context.Context, *lcWorker, lcClaim) error {
				failNthWrite(c, "lc.0", 2, syscall.EIO) // the relock, then the removal
				rec.during = nil
				return errLCStaleEntry
			}
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":a:1", ":b:1"}))
			Expect(entryOf(":b:1").Status).To(Equal(rgwcls.LCStatusComplete))
		})

		It("ends the pass, leaving the entry processing, when its post-state does not write", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			setEntry(ctx, uninitial(":b:1"))
			rec.during = func(context.Context, *lcWorker, lcClaim) error {
				failNthWrite(c, "lc.0", 2, syscall.EIO) // the relock, then the post-state
				return nil
			}
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":a:1"}))
			Expect(entryOf(":a:1").Status).To(Equal(rgwcls.LCStatusProcessing))
			Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty())
		})

		It("ends the pass without its post-state, leaving the entry processing, when the shard cannot be locked again", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			setEntry(ctx, uninitial(":b:1"))
			rec.during = func(ctx context.Context, _ *lcWorker, _ lcClaim) error {
				Expect(pool(ctx).LockExclusive(ctx, "lc.0", lcLockName, "lc_thrd: 1 of another gateway", "", time.Hour, 0)).To(Succeed())
				return nil
			}
			logs := &bytes.Buffer{}
			DeferCleanup(captureLog(logs))
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":a:1"}))
			Expect(entryOf(":a:1")).To(Equal(rgwcls.LCEntry{Bucket: ":a:1", StartTime: now(), Status: rgwcls.LCStatusProcessing}))
			Expect(waits.recorded()).To(HaveLen(5), "the backoff's five tries")
			Expect(headOf(ctx, "lc.0").Marker).To(Equal(":b:1"))
			Expect(logs.String()).To(And(ContainSubstring("lifecycle could not lock the shard again"), ContainSubstring(`"error":"`)), "the warning names the lock's error")
		})

		It("says the claim could not be read back, not that it was lost, when its read fails after a bucket", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			logs := &bytes.Buffer{}
			DeferCleanup(captureLog(logs))
			rec.during = func(context.Context, *lcWorker, lcClaim) error {
				c.FailNextRead(lcSpecPool, lcSpecNS, "lc.0", syscall.EIO) // the read back after the relock
				return nil
			}
			w.process(ctx)
			Expect(logs.String()).To(And(ContainSubstring("could not read its claim back"), ContainSubstring("input/output error")))
			Expect(logs.String()).NotTo(ContainSubstring("no longer"))
			Expect(entryOf(":a:1").Status).To(Equal(rgwcls.LCStatusProcessing), "nothing recorded")
		})

		It("stamps the rollover date when the head passes the shard's end", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			setEntry(ctx, uninitial(":b:1"))
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":a:1", ":b:1"}))
			Expect(headOf(ctx, "lc.0")).To(Equal(rgwcls.LCHead{StartDate: now(), ShardRolloverDate: now()}))
		})

		It("gives the bucket pass thread_stop_at, a lifecycle day from now", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			w.process(ctx)
			Expect(rec.lastRun().stopAt).To(Equal(time.Unix(clock.now().Unix()+86400, 0)))
			s.opts.lcDebugInterval = 10 * time.Second
			clock.advance(10 * time.Second)
			w.process(ctx)
			Expect(rec.lastRun().stopAt).To(Equal(time.Unix(clock.now().Unix()+10, 0)))
		})

		DescribeTable("starts the shard over from its first entry only with rollover allowed: after 24 h, with none stored, or in debug mode",
			func(ctx SpecContext, rolledAgo time.Duration, debug, startsOver bool) {
				if debug {
					s.opts.lcDebugInterval = 10 * time.Second
				}
				setEntry(ctx, uninitial(":a:1"))
				setEntry(ctx, uninitial(":b:1"))
				rolled := uint64(0)
				if rolledAgo != 0 {
					rolled = lcSeconds(clock.now().Add(-rolledAgo))
				}
				putHead(ctx, "lc.0", rgwcls.LCHead{StartDate: 1, ShardRolloverDate: rolled})
				var during rgwcls.LCHead
				rec.during = func(ctx context.Context, _ *lcWorker, cl lcClaim) error {
					if cl.entry.Bucket == ":a:1" {
						during = headOf(ctx, "lc.0")
					}
					return nil
				}
				w.process(ctx)
				Expect(rec.take()).To(Equal([]string{":a:1", ":b:1"}), "the head's ENOENT walks the shard either way (docs/ceph-upstream-bugs.md)")
				if startsOver {
					Expect(during.ShardRolloverDate).To(BeZero(), "the rollover clears the date")
				} else {
					Expect(during.ShardRolloverDate).To(Equal(rolled), "the head's own walk keeps it")
				}
				Expect(headOf(ctx, "lc.0").ShardRolloverDate).To(Equal(now()), "stamped at the end")
			},
			Entry("none stored", time.Duration(0), false, true),
			Entry("rolled over 24 h and a second ago", 24*time.Hour+time.Second, false, true),
			Entry("rolled over 24 h ago", 24*time.Hour, false, false),
			Entry("rolled over 23 h ago", 23*time.Hour, false, false),
			Entry("rolled over 23 h ago, in debug mode", 23*time.Hour, true, true),
			Entry("a rollover date ahead of now", -time.Hour, false, false),
		)

		It("skips an entry completed today, local time, and claims one completed on an earlier local day", func(ctx SpecContext) {
			clock.advance(-lcT0.Sub(time.Date(2026, 10, 21, 0, 30, 0, 0, lcIST))) // 00:30 local, 19:00Z the day before
			today := rgwcls.LCEntry{Bucket: ":a:1", StartTime: lcSeconds(time.Date(2026, 10, 21, 0, 15, 0, 0, lcIST)), Status: rgwcls.LCStatusComplete}
			yesterday := rgwcls.LCEntry{Bucket: ":b:1", StartTime: lcSeconds(time.Date(2026, 10, 20, 23, 45, 0, 0, lcIST)), Status: rgwcls.LCStatusComplete}
			Expect(time.Unix(int64(yesterday.StartTime), 0).UTC().YearDay()).To(Equal(clock.now().UTC().YearDay()), "the same UTC day")
			setEntry(ctx, today)
			setEntry(ctx, yesterday)
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":b:1"}))
			Expect(entryOf(":a:1")).To(Equal(today))
		})

		It("in debug mode skips an entry completed less than the interval ago", func(ctx SpecContext) {
			s.opts.lcDebugInterval = 10 * time.Second
			setEntry(ctx, rgwcls.LCEntry{Bucket: ":a:1", StartTime: now() - 9, Status: rgwcls.LCStatusComplete})
			setEntry(ctx, rgwcls.LCEntry{Bucket: ":b:1", StartTime: now() - 10, Status: rgwcls.LCStatusComplete})
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":b:1"}))
		})

		DescribeTable("skips an entry processing in a live session, and reclaims one two lifecycle days old",
			func(ctx SpecContext, debug time.Duration, startedAgo uint64, autoClear, reclaimed bool) {
				s.opts.lcDebugInterval, s.opts.lcAutoSessionClear = debug, autoClear
				setEntry(ctx, rgwcls.LCEntry{Bucket: ":a:1", StartTime: now() - startedAgo, Status: rgwcls.LCStatusProcessing})
				w.process(ctx)
				if !reclaimed {
					Expect(rec.take()).To(BeEmpty())
					Expect(entryOf(":a:1").StartTime).To(Equal(now() - startedAgo))
					return
				}
				Expect(rec.take()).To(Equal([]string{":a:1"}))
				Expect(entryOf(":a:1")).To(Equal(rgwcls.LCEntry{Bucket: ":a:1", StartTime: now(), Status: rgwcls.LCStatusComplete}))
			},
			Entry("two days ago, live", -time.Second, uint64(2*86400), true, false),
			Entry("two days and a second ago, expired", -time.Second, uint64(2*86400+1), true, true),
			Entry("two days and a second ago with rgwlc_auto_session_clear off", -time.Second, uint64(2*86400+1), false, false),
			Entry("two debug intervals and a second ago, expired", 10*time.Second, uint64(21), true, true),
			Entry("two debug intervals ago, live", 10*time.Second, uint64(20), true, false),
		)

		It("moves the head past a key the shard no longer holds", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			setEntry(ctx, uninitial(":c:1"))
			putHead(ctx, "lc.0", rgwcls.LCHead{StartDate: 1, Marker: ":b:1", ShardRolloverDate: now()})
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":c:1"}))
		})
	})

	Describe("the head rule", func() {
		It("leaves the head at a refused entry in the middle of a shard, and every other entry processed each day", func(ctx SpecContext) {
			s.opts.lcDebugInterval = 10 * time.Second
			for _, k := range []string{":a:1", ":e:1", ":z:1"} {
				setEntry(ctx, uninitial(k))
			}
			rec.refused = map[string]bool{":e:1": true}
			for day := range 3 {
				c.ResetCounters()
				w.process(ctx)
				Expect(rec.take()).To(ConsistOf(":a:1", ":z:1"), "day %d", day)
				Expect(entryOf(":e:1")).To(Equal(uninitial(":e:1")), "never claimed")
				Expect(headOf(ctx, "lc.0").Marker).To(Equal(":e:1"), "day %d", day)
				if day > 0 {
					Expect(headWrites("lc.0")).To(BeZero(), "no head write after the first day")
				}
				clock.advance(10 * time.Second)
			}
		})

		It("processes every other bucket once each pass in an rgw-go-only zone with refused entries on several shards", func(ctx SpecContext) {
			s.opts.lcDebugInterval, s.opts.lcMaxObjs = 10*time.Second, 8
			ws.perm = rand.Perm
			var keys []string
			for i := range 48 {
				key := fmt.Sprintf("tenant:bucket-%02d:marker-%02d", i, i)
				keys = append(keys, key)
				setEntry(ctx, uninitial(key))
			}
			byShard := map[string][]string{}
			for _, k := range keys {
				byShard[oidOf(k)] = append(byShard[oidOf(k)], k)
			}
			rec.refused = map[string]bool{}
			for _, oid := range slices.Sorted(maps.Keys(byShard)) {
				if ks := byShard[oid]; len(ks) >= 3 && len(rec.refused) < 3 {
					rec.refused[ks[1]] = true // with entries on both sides
				}
			}
			Expect(rec.refused).To(HaveLen(3))
			var want []string
			for _, k := range keys {
				if !rec.refused[k] {
					want = append(want, k)
				}
			}
			for pass := range 4 {
				w.process(ctx)
				Expect(rec.take()).To(ConsistOf(want), "pass %d", pass)
				clock.advance(10 * time.Second)
			}
		})

		It("follows the head radosgw's worker moves past an entry rgw-go refuses", func(ctx SpecContext) {
			s.opts.lcDebugInterval = 10 * time.Second
			for _, k := range []string{":a:1", ":e:1", ":z:1"} {
				setEntry(ctx, uninitial(k))
			}
			rec.refused = map[string]bool{":e:1": true}
			w.process(ctx)
			Expect(rec.take()).To(ConsistOf(":a:1", ":z:1"))
			var radosgw []string
			rgw := ws.worker(1)
			radosgwShardPass(ctx, rgw, 0, func(key string) error {
				radosgw = append(radosgw, key)
				return nil
			})
			Expect(radosgw).To(Equal([]string{":e:1"}), "radosgw claims E, and skips Z, done this interval")
			Expect(headOf(ctx, "lc.0")).To(HaveField("Marker", ""), "and moves the head to the end")
			Expect(entryOf(":e:1").Status).To(Equal(rgwcls.LCStatusComplete))
			clock.advance(10 * time.Second)
			c.ResetCounters()
			w.process(ctx)
			Expect(rec.take()).To(ConsistOf(":a:1", ":z:1"))
			Expect(headOf(ctx, "lc.0").Marker).To(Equal(":e:1"), "rgw-go starts the shard over from the head radosgw left, and keeps the head at E")
		})
	})

	Describe("a pair-form entry", func() {
		It("is rewritten as an entry when the head reaches it, so a pass processes A, P and Z, and radosgw's next pass does not stall", func(ctx SpecContext) {
			s.opts.lcDebugInterval = 10 * time.Second
			setEntry(ctx, uninitial(":a:1"))
			putRaw(ctx, "lc.0", ":p:1", lcPairValue(":p:1", rgwcls.LCStatusFailed))
			setEntry(ctx, uninitial(":z:1"))
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":a:1", ":p:1", ":z:1"}))
			Expect(entryOf(":p:1")).To(Equal(rgwcls.LCEntry{Bucket: ":p:1", StartTime: now(), Status: rgwcls.LCStatusComplete}))
			clock.advance(10 * time.Second)
			var radosgw []string
			radosgwShardPass(ctx, ws.worker(1), 0, func(key string) error {
				radosgw = append(radosgw, key)
				return nil
			})
			Expect(radosgw).To(Equal([]string{":a:1", ":p:1", ":z:1"}))
			Expect(headOf(ctx, "lc.0").ShardRolloverDate).To(Equal(now()), "radosgw reaches the shard's end")
		})

		It("stalls radosgw's pass, without the rewrite, at the entry before it", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			putRaw(ctx, "lc.0", ":p:1", lcPairValue(":p:1", rgwcls.LCStatusFailed))
			var radosgw []string
			radosgwShardPass(ctx, ws.worker(1), 0, func(key string) error {
				radosgw = append(radosgw, key)
				return nil
			})
			Expect(radosgw).To(BeEmpty())
			Expect(entryOf(":a:1").Status).To(Equal(rgwcls.LCStatusProcessing), "claimed, its head never moved")
		})

		It("is rewritten where the head was left at it, and the pass goes on", func(ctx SpecContext) {
			putRaw(ctx, "lc.0", ":p:1", lcPairValue(":p:1", rgwcls.LCStatusUninitial))
			setEntry(ctx, uninitial(":z:1"))
			putHead(ctx, "lc.0", rgwcls.LCHead{StartDate: 1, Marker: ":p:1", ShardRolloverDate: now()})
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{":p:1", ":z:1"}))
			Expect(entryOf(":p:1").Status).To(Equal(rgwcls.LCStatusComplete))
		})

		It("is rewritten {bucket, 0, status} under the shard lock", func(ctx SpecContext) {
			putRaw(ctx, "lc.0", ":p:1", lcPairValue(":p:1", rgwcls.LCStatusComplete))
			putHead(ctx, "lc.0", rgwcls.LCHead{StartDate: 1, Marker: ":p:1", ShardRolloverDate: now()})
			var during []radosclient.Locker
			seen := 0
			c.BeforeWrite(lcSpecPool, lcSpecNS, "lc.0", func(*fakerados.Object) {
				if seen++; seen == 2 { // the lock, then the rewrite
					during = c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)
				}
			})
			rec.refused = map[string]bool{":p:1": true}
			w.process(ctx)
			Expect(entryOf(":p:1")).To(Equal(rgwcls.LCEntry{Bucket: ":p:1", Status: rgwcls.LCStatusComplete}))
			Expect(during).To(ConsistOf(HaveField("Cookie", "lc_thrd: 0")))
		})

		It("writes no stray entry for a next entry's pair that names another key", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":a:1"))
			stray := lcPairValue(":q:1", rgwcls.LCStatusFailed)
			putRaw(ctx, "lc.0", ":p:1", stray)
			w.process(ctx)
			omap := c.Object(lcSpecPool, lcSpecNS, "lc.0").Omap
			Expect(slices.Sorted(maps.Keys(omap))).To(Equal([]string{":a:1", ":p:1"}), "no :q:1")
			Expect(omap[":p:1"]).To(Equal(stray))
			Expect(entryOf(":a:1").Status).To(Equal(rgwcls.LCStatusProcessing), "claimed, its head never moved, as radosgw's stall leaves it")
			Expect(rec.take()).To(BeEmpty())
		})

		It("ends the shard's pass with the head unmoved at a value that is neither form, and the next shard is passed", func(ctx SpecContext) {
			s.opts.lcMaxObjs = 2
			bad, good := lcKeyOn(":x", 0, 2), lcKeyOn(":y", 1, 2)
			putRaw(ctx, "lc.0", bad, []byte{0xff})
			head := rgwcls.LCHead{StartDate: 1, Marker: bad, ShardRolloverDate: now()}
			putHead(ctx, "lc.0", head)
			setEntry(ctx, uninitial(good))
			w.process(ctx)
			Expect(rec.take()).To(Equal([]string{good}))
			Expect(headOf(ctx, "lc.0")).To(Equal(head))
			Expect(c.Object(lcSpecPool, lcSpecNS, "lc.0").Omap[bad]).To(Equal([]byte{0xff}))
		})
	})

	Describe("a claim", func() {
		var (
			c2      lcClient
			s2      *Store
			second  *lcWorker
			release chan struct{}
		)
		BeforeEach(func() {
			s.opts.lcDebugInterval = 10 * time.Second
			c2 = lcClient{Cluster: c, name: "client.2"}
			s2 = newLCWorkerStore(c2, 1)
			s2.opts = s.opts
			ws2 := newLCWorkers(s2, rec)
			ws2.now, ws2.loc, ws2.sleep, ws2.perm = clock.now, lcIST, waits.sleep, identityPerm
			second = ws2.worker(0)
			release = make(chan struct{})
		})

		It("held by a worker that dies is skipped until its session expires, then reclaimed and completed under the new start time", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":k:1"))
			entered, dead := make(chan struct{}), make(chan struct{})
			rec.during = func(_ context.Context, w *lcWorker, _ lcClaim) error {
				if w != second {
					close(entered)
					<-dead // a death runs no more code
				}
				return nil
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				w.process(ctx)
			}()
			DeferCleanup(func() {
				close(dead)
				Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeClosed())
			})
			Eventually(entered).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeClosed())
			Expect(rec.take()).To(Equal([]string{":k:1"}))
			claimed := rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusProcessing}
			Expect(entryOf(":k:1")).To(Equal(claimed))

			clock.advance(5 * time.Second)
			second.process(ctx)
			Expect(rec.take()).To(BeEmpty(), "the other client skips the live claim")
			clock.advance(15 * time.Second)
			ws.worker(0).process(ctx)
			Expect(rec.take()).To(BeEmpty(), "a worker restarted before the expiry does not process it")
			Expect(entryOf(":k:1")).To(Equal(claimed), "a death leaves the entry processing")

			clock.advance(time.Second)
			second.process(ctx)
			Expect(rec.take()).To(Equal([]string{":k:1"}))
			Expect(entryOf(":k:1")).To(Equal(rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusComplete}))
		})

		Describe("held by a worker that stops during its bucket", func() {
			BeforeEach(func(ctx SpecContext) { setEntry(ctx, uninitial(":k:1")) })

			DescribeTable("records it failed under the claim's start time, under the lock, whatever the cut-short pass returned",
				func(ctx SpecContext, res error) {
					actx, cancel := context.WithCancel(ctx)
					defer cancel()
					rec.during = func(context.Context, *lcWorker, lcClaim) error {
						cancel()
						return res
					}
					var held []radosclient.Locker
					c.BeforeWrite(lcSpecPool, lcSpecNS, "lc.0", func(*fakerados.Object) {
						held = c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)
					})
					w.process(actx)
					Expect(entryOf(":k:1")).To(Equal(rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusFailed}))
					Expect(held).To(ConsistOf(HaveField("Cookie", "lc_thrd: 0")), "the last write, failed, under the worker's lock")
					Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty())
					rec.during = nil
					second.process(ctx)
					Expect(rec.take()).To(Equal([]string{":k:1", ":k:1"}), "due again at the next pass")
				},
				Entry("its context's error", context.Canceled),
				Entry("success", nil),
			)

			It("records nothing when the shard cannot be locked again", func(ctx SpecContext) {
				actx, cancel := context.WithCancel(ctx)
				defer cancel()
				rec.during = func(context.Context, *lcWorker, lcClaim) error {
					Expect(pool(ctx).LockExclusive(ctx, "lc.0", lcLockName, "lc_thrd: 1 of another gateway", "", time.Hour, 0)).To(Succeed())
					cancel()
					return nil
				}
				w.process(actx)
				Expect(entryOf(":k:1")).To(Equal(rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusProcessing}))
				Expect(waits.recorded()).To(HaveLen(5), "the backoff's five tries")
			})

			It("records nothing over a claim no longer its own", func(ctx SpecContext) {
				actx, cancel := context.WithCancel(ctx)
				defer cancel()
				rec.during = func(context.Context, *lcWorker, lcClaim) error {
					setEntry(ctx, uninitial(":k:1")) // a configuration put resets it
					cancel()
					return nil
				}
				w.process(actx)
				Expect(entryOf(":k:1")).To(Equal(uninitial(":k:1")))
				Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty())
			})

			It("gives up, recording nothing, once its bound runs out", func(ctx SpecContext) {
				actx, cancel := context.WithCancel(ctx)
				defer cancel()
				ws.stopBound = 50 * time.Millisecond
				var deadline time.Time
				var bounded bool
				waits.on = func(wctx context.Context, _ time.Duration) error {
					if !bounded {
						deadline, bounded = wctx.Deadline()
						Expect(time.Until(deadline)).To(BeNumerically("<=", 50*time.Millisecond))
					}
					<-wctx.Done() // a wait that would outlast the bound
					return wctx.Err()
				}
				rec.during = func(context.Context, *lcWorker, lcClaim) error {
					Expect(pool(ctx).LockExclusive(ctx, "lc.0", lcLockName, "lc_thrd: 1 of another gateway", "", time.Hour, 0)).To(Succeed())
					cancel()
					return nil
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					w.process(actx)
				}()
				Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeClosed())
				Expect(bounded).To(BeTrue(), "the relock's waits run on a context with a deadline")
				Expect(entryOf(":k:1").Status).To(Equal(rgwcls.LCStatusProcessing))
			})

			Describe("on a pool whose calls can hang", func() {
				var (
					hooks *lcHooks
					hw    *lcWorker
				)
				BeforeEach(func() {
					hooks = &lcHooks{release: make(chan struct{})}
					DeferCleanup(func() { close(hooks.release) })
					hs := newLCWorkerStore(lcHookCluster{Cluster: c, h: hooks}, 1)
					hs.opts = s.opts
					hws := newLCWorkers(hs, rec)
					hws.now, hws.loc, hws.sleep, hws.perm = clock.now, lcIST, waits.sleep, identityPerm
					hws.stopBound = 100 * time.Millisecond
					hw = hws.worker(0)
				})
				hangAll := func([]string) bool { return true }
				// processWithin runs hw's pass on actx and expects it back
				// within the two bounds, the stop's and its unlock's.
				processWithin := func(actx context.Context) {
					GinkgoHelper()
					done := make(chan struct{})
					go func() {
						defer close(done)
						hw.process(actx)
					}()
					Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeClosed())
				}

				It("ends within its bound, writing nothing, when its shard lock never returns", func(ctx SpecContext) {
					actx, cancel := context.WithCancel(ctx)
					defer cancel()
					rec.during = func(context.Context, *lcWorker, lcClaim) error {
						cancel()
						hooks.set(lcHookFns{hangs: hangAll}) // the shard's PG goes unavailable
						return nil
					}
					processWithin(actx)
					hooks.set(lcHookFns{})
					Expect(entryOf(":k:1")).To(Equal(rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusProcessing}))
				})

				It("records failed and ends within its unlock's bound when the unlock never returns", func(ctx SpecContext) {
					actx, cancel := context.WithCancel(ctx)
					defer cancel()
					rec.during = func(context.Context, *lcWorker, lcClaim) error {
						cancel()
						hooks.set(lcHookFns{hangs: func(calls []string) bool { return slices.Contains(calls, "lock.unlock") }})
						return nil
					}
					processWithin(actx)
					hooks.set(lcHookFns{})
					Expect(entryOf(":k:1")).To(Equal(rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusFailed}))
					Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(ConsistOf(HaveField("Cookie", "lc_thrd: 0")),
						"the unlock left in flight; the lock lapses after rgw_lc_lock_max_time")
				})

				It("releases its lock on a bound of its own when recording failed used up the first", func(ctx SpecContext) {
					actx, cancel := context.WithCancel(ctx)
					defer cancel()
					rec.during = func(context.Context, *lcWorker, lcClaim) error {
						cancel()
						hooks.set(lcHookFns{after: func(calls []string) {
							if slices.Contains(calls, "rgw.lc_set_entry") {
								time.Sleep(150 * time.Millisecond) // a slow cluster: the write lands as the 100 ms bound runs out
							}
						}})
						return nil
					}
					processWithin(actx)
					hooks.set(lcHookFns{})
					Expect(entryOf(":k:1").Status).To(Equal(rgwcls.LCStatusFailed))
					Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty(), "the unlock had its own bound")
				})

				It("unlocks on lcUnlockTimeout while it runs, before the bucket and between buckets", func(ctx SpecContext) {
					var bounds []time.Duration
					hooks.set(lcHookFns{before: func(octx context.Context, calls []string) {
						if slices.Contains(calls, "lock.unlock") {
							d, ok := octx.Deadline()
							Expect(ok).To(BeTrue(), "the unlock runs on a bounded context")
							bounds = append(bounds, time.Until(d))
						}
					}})
					hw.process(ctx)
					hooks.set(lcHookFns{})
					Expect(bounds).To(HaveExactElements(
						BeNumerically("~", lcUnlockTimeout, time.Second),
						BeNumerically("~", lcUnlockTimeout, time.Second),
					))
					Expect(entryOf(":k:1").Status).To(Equal(rgwcls.LCStatusComplete))
				})

				It("unlocks within its stop bound when the stop lands while it holds the lock between buckets", func(ctx SpecContext) {
					actx, cancel := context.WithCancel(ctx)
					defer cancel()
					sets := 0
					hooks.set(lcHookFns{
						after: func(calls []string) {
							if slices.Contains(calls, "rgw.lc_set_entry") {
								if sets++; sets == 2 { // the claim, then the post-state: the bucket is done
									cancel()
								}
							}
						},
						hangs: func(calls []string) bool { return sets == 2 && slices.Contains(calls, "lock.unlock") },
					})
					processWithin(actx)
					hooks.set(lcHookFns{})
					Expect(entryOf(":k:1").Status).To(Equal(rgwcls.LCStatusComplete))
				})

				It("unlocks within its stop bound when the stop lands between the claim's first read back and the bucket", func(ctx SpecContext) {
					actx, cancel := context.WithCancel(ctx)
					defer cancel()
					var claimed, headStored, stopped bool
					hooks.set(lcHookFns{
						after: func(calls []string) {
							switch {
							case slices.Contains(calls, "rgw.lc_set_entry"):
								claimed = true
							case claimed && slices.Contains(calls, "rgw.lc_put_head"):
								headStored = true
							case headStored && !stopped && slices.Contains(calls, "read"): // the claim's first read back
								stopped = true
								cancel()
							}
						},
						hangs: func(calls []string) bool { return stopped && slices.Contains(calls, "lock.unlock") },
					})
					processWithin(actx)
					hooks.set(lcHookFns{})
					Expect(rec.take()).To(Equal([]string{":k:1"}), "the bucket pass, reached past the unlock")
					Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(ConsistOf(HaveField("Cookie", "lc_thrd: 0")),
						"the unlock left in flight; the lock lapses after rgw_lc_lock_max_time")
				})

				It("records a pass that returned on its own though the stop lands at its relock", func(ctx SpecContext) {
					actx, cancel := context.WithCancel(ctx)
					defer cancel()
					rec.during = func(context.Context, *lcWorker, lcClaim) error {
						hooks.set(lcHookFns{before: func(_ context.Context, calls []string) {
							if slices.Contains(calls, "lock.lock") {
								cancel()
							}
						}})
						return nil
					}
					hw.process(actx)
					hooks.set(lcHookFns{})
					Expect(entryOf(":k:1")).To(Equal(rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusComplete}))
					Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty())
				})

				DescribeTable("records failed when the stop lands between the claim and its first read back",
					func(ctx SpecContext, at string) {
						actx, cancel := context.WithCancel(ctx)
						defer cancel()
						hooks.set(lcHookFns{after: func(calls []string) {
							if slices.Contains(calls, at) {
								cancel()
							}
						}})
						hw.process(actx)
						hooks.set(lcHookFns{})
						Expect(rec.take()).To(BeEmpty(), "no bucket pass")
						Expect(entryOf(":k:1")).To(Equal(rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusFailed}))
						Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty())
					},
					Entry("at the claim's write", "rgw.lc_set_entry"),
					Entry("at the head's write", "rgw.lc_put_head"),
				)
			})
		})

		It("reclaimed from a stalled worker stops it at its claim's read back, with no post-state", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":k:1"))
			entered := make(chan struct{})
			var lost error
			rec.during = func(ctx context.Context, w *lcWorker, cl lcClaim) error {
				if w != second {
					close(entered)
					<-release
					lost = w.claimHeld(ctx, cl)
					return lost
				}
				return nil
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				w.process(ctx)
			}()
			Eventually(entered).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeClosed())
			clock.advance(21 * time.Second)
			second.process(ctx)
			reclaimed := rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusComplete}
			Expect(entryOf(":k:1")).To(Equal(reclaimed))
			c.ResetCounters()
			close(release)
			Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeClosed())
			Expect(lost).To(MatchError(errLCClaimLost), "the bucket pass's read back at its next page")
			Expect(entryOf(":k:1")).To(Equal(reclaimed))
			Expect(lcWriteCalls(c, "lc.0")).To(Equal([]string{"lock.lock", "lock.unlock"}), "the lock taken again and released, and nothing written")
			Expect(c.Locks(lcSpecPool, lcSpecNS, "lc.0", lcLockName)).To(BeEmpty(), "the lock it took again released")
		})

		It("is read back as lost once reset, removed or reclaimed", func(ctx SpecContext) {
			setEntry(ctx, uninitial(":k:1"))
			mine := lcClaim{entry: rgwcls.LCEntry{Bucket: ":k:1", StartTime: now(), Status: rgwcls.LCStatusProcessing}}
			setEntry(ctx, mine.entry)
			Expect(w.claimHeld(ctx, mine)).To(Succeed())
			setEntry(ctx, rgwcls.LCEntry{Bucket: ":k:1", StartTime: now() + 30, Status: rgwcls.LCStatusProcessing})
			Expect(w.claimHeld(ctx, mine)).To(MatchError(errLCClaimLost))
			setEntry(ctx, uninitial(":k:1"))
			Expect(w.claimHeld(ctx, mine)).To(MatchError(errLCClaimLost))
			Expect(lcRmEntry(ctx, pool(ctx), "lc.0", mine.entry)).To(Succeed())
			Expect(w.claimHeld(ctx, mine)).To(MatchError(errLCClaimLost))
		})
	})

	It("processes every bucket once a day with two workers on two RADOS clients over many shards", func(ctx SpecContext) {
		s.opts.lcMaxObjs, s.opts.lcWorkTime = 8, lcWorkTime{endHour: 23, endMinute: 59}
		s2 := newLCWorkerStore(lcClient{Cluster: c, name: "client.2"}, 8)
		s2.opts = s.opts
		realWaits := sleepCtx
		ws.sleep, ws.perm = realWaits, rand.Perm
		ws2 := newLCWorkers(s2, rec)
		ws2.now, ws2.loc, ws2.sleep, ws2.perm = clock.now, lcIST, realWaits, rand.Perm
		var keys []string
		for i := range 60 {
			key := fmt.Sprintf(":bucket-%02d:m", i)
			keys = append(keys, key)
			setEntry(ctx, uninitial(key))
		}
		rec.during = func(context.Context, *lcWorker, lcClaim) error {
			runtime.Gosched() // let the other worker take the shard meanwhile
			return nil
		}
		for day := range 3 {
			var wg sync.WaitGroup
			wg.Go(func() { ws.worker(0).process(ctx) })
			wg.Go(func() { ws2.worker(0).process(ctx) })
			wg.Wait()
			Expect(rec.take()).To(ConsistOf(keys), "day %d", day)
			for _, k := range keys {
				Expect(entryOf(k)).To(Equal(rgwcls.LCEntry{Bucket: k, StartTime: now(), Status: rgwcls.LCStatusComplete}), "day %d", day)
			}
			clock.advance(24 * time.Hour)
		}
	})
})

var _ = Describe("lcLocalTime", func() {
	DescribeTable("is mktime of the local day's fields at hour:minute, keeping the reference's daylight-saving flag",
		func(ref time.Time, hour, minute int, zone string, want time.Time) {
			loc, err := time.LoadLocation(zone)
			Expect(err).NotTo(HaveOccurred())
			Expect(lcLocalTime(ref, hour, minute, loc)).To(BeTemporally("==", want))
		},
		Entry("midnight of the local day ahead of UTC", time.Date(2026, 10, 20, 19, 0, 0, 0, time.UTC), 0, 0,
			"Asia/Kolkata", time.Date(2026, 10, 20, 18, 30, 0, 0, time.UTC)),
		Entry("an hour past a day, carried to the next", time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), 25, 0,
			"UTC", time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC)),
		Entry("a negative minute, carried back", time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC), 0, -30,
			"UTC", time.Date(2026, 10, 19, 23, 30, 0, 0, time.UTC)),
		Entry("New York's midnight the day it springs forward, read at the reference's EDT", time.Date(2026, 3, 8, 16, 0, 0, 0, time.UTC), 0, 0,
			"America/New_York", time.Date(2026, 3, 8, 4, 0, 0, 0, time.UTC)),
		Entry("New York's 02:30 that day, which the zone skips, read at the reference's EDT", time.Date(2026, 3, 8, 16, 0, 0, 0, time.UTC), 2, 30,
			"America/New_York", time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC)),
		Entry("New York's midnight the day before, the zone's own", time.Date(2026, 3, 7, 16, 0, 0, 0, time.UTC), 0, 0,
			"America/New_York", time.Date(2026, 3, 7, 5, 0, 0, 0, time.UTC)),
	)
})
