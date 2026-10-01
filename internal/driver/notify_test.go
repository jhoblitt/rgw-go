package driver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// recorder is a list the notifier's goroutines append to while a spec reads
// it.
type recorder[T any] struct {
	mu sync.Mutex
	v  []T
}

func (r *recorder[T]) add(x T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.v = append(r.v, x)
}

func (r *recorder[T]) get() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.v)
}

// sentNotify is one Notify call: what was asked for, whether or not the
// cluster delivered it.
type sentNotify struct {
	oid     string
	payload []byte
	timeout time.Duration
}

// notifyLog records every Notify call before passing it on, since the fake
// keeps only the payloads it delivered.
type notifyLog struct {
	radosclient.Pool
	sent recorder[sentNotify]
}

func (p *notifyLog) Notify(ctx context.Context, oid string, payload []byte, timeout time.Duration) ([]radosclient.NotifyAck, error) {
	p.sent.add(sentNotify{oid: oid, payload: slices.Clone(payload), timeout: timeout})
	return p.Pool.Notify(ctx, oid, payload, timeout)
}

var _ = Describe("controlOIDs", func() {
	It("names rgw_num_control_oids objects", func() {
		Expect(controlOIDs(8)).To(Equal([]string{"notify.0", "notify.1", "notify.2", "notify.3", "notify.4", "notify.5", "notify.6", "notify.7"}))
	})
	It("uses the legacy single object for zero and one object for a negative count", func() {
		Expect(controlOIDs(0)).To(Equal([]string{"notify"}), "compat_oid, svc_notify.cc:203")
		Expect(controlOIDs(-3)).To(Equal([]string{"notify.0"}), "num_watchers <= 0 becomes 1, svc_notify.cc:205")
	})
	It("counts only the low 32 bits, as the int radosgw holds the count in does", func() {
		Expect(controlOIDs(1<<32 + 2)).To(Equal([]string{"notify.0", "notify.1"}))
		Expect(controlOIDs(1<<32)).To(Equal([]string{"notify"}), "0 as an int: the legacy object")
		Expect(controlOIDs(1<<31)).To(Equal([]string{"notify.0"}), "-2^31 as an int: one object")
	})
})

var _ = Describe("notifier", func() {
	const control = "zone.rgw.control"
	var (
		c       *fakerados.Cluster
		pool    *notifyLog
		n       *notifier
		handled *recorder[meta.CacheNotifyInfo]
		enabled *recorder[bool]
		sleeps  *recorder[time.Duration]
	)
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(captureLog(GinkgoWriter))
		c = fakerados.New()
		p, err := c.Pool(ctx, control, "")
		Expect(err).NotTo(HaveOccurred())
		pool = &notifyLog{Pool: p}
		handled, enabled, sleeps = &recorder[meta.CacheNotifyInfo]{}, &recorder[bool]{}, &recorder[time.Duration]{}
		n = newNotifier(pool, controlOIDs(8), denc.Squid, 10, handled.add, enabled.add)
		n.sleep = func(_ context.Context, d time.Duration) error { sleeps.add(d); return nil }
	})

	// start runs the notifier as a Store worker until the spec ends, and waits
	// for Store.Run to count its stop as clean.
	start := func(ctx context.Context) {
		var s Store
		s.AddWorker("control-watch", n.run)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- s.Run(runCtx) }()
		DeferCleanup(func() {
			cancel()
			Eventually(done).WithTimeout(time.Second).Should(Receive(Succeed()))
		})
	}

	// watched waits until every control object has one watch.
	watched := func() {
		for _, oid := range controlOIDs(8) {
			Eventually(func() int { return c.Watches(control, "", oid) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(1), oid)
		}
	}

	It("picks the control object by ceph_str_hash_linux of the cache name", func() {
		Expect(meta.StrHashLinux("a")).To(BeEquivalentTo(17138), "(97<<4 + 97>>4) * 11")
		Expect(n.pick("a")).To(Equal("notify.2"), "17138 mod 8")
	})

	It("creates, picks and notifies the legacy object alone when it is the only one", func(ctx SpecContext) {
		legacy := newNotifier(pool, controlOIDs(0), denc.Squid, 10, handled.add, enabled.add)
		Expect(legacy.createControlObjects(ctx)).To(Succeed())
		Expect(c.Object(control, "", "notify")).NotTo(BeNil())
		Expect(c.Object(control, "", "notify.0")).To(BeNil())
		Expect(legacy.pick("a")).To(Equal("notify"))
		info := meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: meta.RawObj{Pool: meta.ParsePool("zone.rgw.meta:root"), OID: "plain"}}
		Expect(legacy.distribute(ctx, "zone.rgw.meta+root+plain", info)).To(Succeed())
		Expect(c.Notifies(control, "", "notify")).To(Equal([][]byte{encodeAt(info, denc.Squid)}))
	})

	It("creates every control object and leaves an existing one alone", func(ctx SpecContext) {
		c.Put(control, "", "notify.3", []byte("x"))
		Expect(n.createControlObjects(ctx)).To(Succeed())
		for i := range 8 {
			Expect(c.Object(control, "", fmt.Sprintf("notify.%d", i))).NotTo(BeNil(), "notify.%d", i)
		}
		Expect(c.Object(control, "", "notify.3").Data).To(Equal([]byte("x")), "create(false) keeps the data")
	})

	It("fails naming the control object it cannot create", func(ctx SpecContext) {
		Expect(pool.Close()).To(Succeed())
		Expect(n.createControlObjects(ctx)).To(And(MatchError(radosclient.ErrClosed), MatchError(ContainSubstring("notify.0"))))
	})

	It("distributes the record to the picked object as radosgw encodes it, with librados's default timeout", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		info := meta.CacheNotifyInfo{
			Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: meta.ParsePool("zone.rgw.meta:root"), OID: "plain"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("d")},
		}
		Expect(n.distribute(ctx, "a", info)).To(Succeed())
		got := c.Notifies(control, "", "notify.2")
		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(Equal(encodeAt(info, denc.Squid)))
		Expect(pool.sent.get()).To(Equal([]sentNotify{{oid: "notify.2", payload: got[0], timeout: 0}}), "0 selects librados's default notify timeout")
	})

	It("retries a timed-out notify with invalidations up to rgw_max_notify_retries", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		info := meta.CacheNotifyInfo{
			Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: meta.ParsePool("p"), OID: "o"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("d")}, NS: "ns",
		}
		inv := encodeAt(meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: info.Obj}, denc.Squid)
		payloads := func() [][]byte {
			var out [][]byte
			for _, s := range pool.sent.get() {
				out = append(out, s.payload)
			}
			return out
		}

		c.FailNotify(control, "", "notify.2", 3, radosclient.ErrTimedOut)
		Expect(n.distribute(ctx, "a", info)).To(Succeed(), "the fourth attempt succeeds")
		Expect(payloads()).To(Equal([][]byte{encodeAt(info, denc.Squid), inv, inv, inv}),
			"svc_notify.cc:482-487: the retries are invalidations of the same object, carrying nothing else")
		Expect(c.Notifies(control, "", "notify.2")).To(Equal([][]byte{inv}), "only the last attempt was delivered")

		n.maxRetries = 2
		c.FailNotify(control, "", "notify.2", 5, radosclient.ErrTimedOut)
		Expect(n.distribute(ctx, "a", info)).To(MatchError(radosclient.ErrTimedOut), "gives up after maxRetries and reports it")
		Expect(payloads()[4:]).To(Equal([][]byte{encodeAt(info, denc.Squid), inv, inv}), "one notify and two retries")
	})

	It("returns any other notify failure at once, without retrying", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		c.FailNotify(control, "", "notify.2", 1, radosclient.ErrPermission)
		info := meta.CacheNotifyInfo{Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: meta.ParsePool("p"), OID: "o"}}
		Expect(n.distribute(ctx, "a", info)).To(MatchError(radosclient.ErrPermission))
		Expect(pool.sent.get()).To(HaveLen(1), "svc_notify.cc:482: only ETIMEDOUT is retried")
	})

	It("registers a watch on every object and enables the cache once all are up", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- n.run(runCtx) }()
		watched()
		Eventually(enabled.get).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true}))
		cancel()
		Eventually(done).WithTimeout(time.Second).Should(Receive(MatchError(context.Canceled)), "a clean stop for Store.Run")
		for _, oid := range controlOIDs(8) {
			Expect(c.Watches(control, "", oid)).To(BeZero(), "%s closed on exit", oid)
		}
		Expect(enabled.get()).To(Equal([]bool{true, false}), "remove_watcher disables the cache on shutdown")
	})

	It("disables the cache when a watch is lost and re-enables it when the watch is back", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		start(ctx)
		Eventually(enabled.get).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true}))
		c.BreakWatches(control, "", "notify.5", errors.New("osd down"))
		Eventually(enabled.get).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true, false, true}))
		Expect(sleeps.get()).To(Equal([]time.Duration{time.Second}), "one backoff before re-registering")
		Expect(c.Watches(control, "", "notify.5")).To(Equal(1))
	})

	It("retries past one hundred failures without stopping, backing off up to thirty seconds and resetting on success", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		c.FailWatch(control, "", "notify.1", 120, errors.New("no osd"))
		start(ctx)
		Eventually(func() int { return c.Watches(control, "", "notify.1") }).WithTimeout(2 * time.Second).WithPolling(time.Millisecond).Should(Equal(1))
		got := sleeps.get()
		Expect(got).To(HaveLen(120), "every failure slept once; the 101st was not fatal (tracker #80992)")
		Expect(got[:6]).To(Equal([]time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}))
		Expect(got[6:]).To(HaveEach(30*time.Second), "capped")
		Eventually(enabled.get).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true}), "enabled only once notify.1 is up")

		c.BreakWatches(control, "", "notify.1", errors.New("again"))
		Eventually(func() int { return len(sleeps.get()) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(121))
		Expect(sleeps.get()[120]).To(Equal(time.Second), "reset after the successful registration")
	})

	It("re-creates a control object deleted under it and registers its watch again", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		start(ctx)
		Eventually(enabled.get).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true}))
		wop := radosclient.NewWriteOp()
		wop.Remove()
		_, err := pool.Write(ctx, "notify.4", wop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Eventually(enabled.get).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true, false, true}))
		Expect(c.Object(control, "", "notify.4")).NotTo(BeNil(), "re-created")
		Expect(c.Watches(control, "", "notify.4")).To(Equal(1))
		Expect(sleeps.get()).To(Equal([]time.Duration{time.Second, 2 * time.Second}),
			"the backoff after the loss, then after the ENOENT that re-created it")
	})

	It("treats a watch whose error channel closes as lost, and backs off rather than spinning", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		n.sleep = func(ctx context.Context, d time.Duration) error {
			sleeps.add(d)
			<-ctx.Done()
			return ctx.Err()
		}
		start(ctx)
		Eventually(enabled.get).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true}))
		Expect(pool.Close()).To(Succeed(), "closing the pool closes its watches without an error")
		Eventually(func() int { return len(sleeps.get()) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(8))
		Expect(sleeps.get()).To(HaveEach(time.Second))
		Expect(enabled.get()).To(Equal([]bool{true, false}))
	})

	It("decodes a notify and hands it to the cache, ignoring one that does not decode", func(ctx SpecContext) {
		Expect(n.createControlObjects(ctx)).To(Succeed())
		start(ctx)
		watched()
		info := meta.CacheNotifyInfo{
			Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: meta.ParsePool("p:ns"), OID: "o", Loc: "l"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData | meta.CacheFlagXattrs, Data: []byte("d"), Xattrs: map[string][]byte{"user.rgw.acl": []byte("a")}},
			Ofs:     7, NS: "ns",
		}
		_, err := pool.Notify(ctx, "notify.2", encodeAt(info, denc.Squid), 0)
		Expect(err).NotTo(HaveOccurred())
		_, err = pool.Notify(ctx, "notify.2", []byte{1, 2, 3}, 0)
		Expect(err).NotTo(HaveOccurred(), "a bad payload is acked, not failed (svc_notify.cc:77-80)")
		Expect(handled.get()).To(Equal([]meta.CacheNotifyInfo{info}))
	})
})
