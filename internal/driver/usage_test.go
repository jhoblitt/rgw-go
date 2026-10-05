package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// fakeClock is a clock a spec moves by hand. The tickers it makes fire as
// Advance passes their periods, dropping a tick their channel has no room
// for, as a time.Ticker does. Unlike time.NewTicker, NewTicker takes a
// period that is not positive, so that a spec sees it; that ticker never
// fires.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

type fakeTicker struct {
	clock   *fakeClock
	c       chan time.Time
	period  time.Duration
	next    time.Time
	stopped bool
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

// Now returns the clock's time.
func (k *fakeClock) Now() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.now
}

// NewTicker returns a ticker firing every d from now on.
func (k *fakeClock) NewTicker(d time.Duration) driver.Ticker {
	k.mu.Lock()
	defer k.mu.Unlock()
	t := &fakeTicker{clock: k, c: make(chan time.Time, 1), period: d, next: k.now.Add(d)}
	k.tickers = append(k.tickers, t)
	return t
}

// Periods returns the periods of the tickers made and not stopped, oldest
// first.
func (k *fakeClock) Periods() []time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []time.Duration
	for _, t := range k.tickers {
		if !t.stopped {
			out = append(out, t.period)
		}
	}
	return out
}

// Advance moves the clock on by d, firing every ticker whose next tick d
// passes.
func (k *fakeClock) Advance(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.now = k.now.Add(d)
	for _, t := range k.tickers {
		for !t.stopped && t.period > 0 && !t.next.After(k.now) {
			select {
			case t.c <- t.next:
			default:
			}
			t.next = t.next.Add(t.period)
		}
	}
}

// heldCluster is a fakerados cluster whose writes to one pool and namespace
// wait until release closes, or until their context ends, which the write
// then fails with. Each write first sends on entered.
type heldCluster struct {
	*fakerados.Cluster
	pool, ns string
	entered  chan struct{}
	release  chan struct{}
}

func (c *heldCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil || pool != c.pool || namespace != c.ns {
		return p, err
	}
	return &heldPool{Pool: p, held: c}, nil
}

type heldPool struct {
	radosclient.Pool
	held *heldCluster
}

func (p *heldPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	p.held.entered <- struct{}{}
	select {
	case <-p.held.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	return p.Pool.Write(ctx, oid, op, flags)
}

func (t *fakeTicker) C() <-chan time.Time { return t.c }

func (t *fakeTicker) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.stopped = true
}

var _ = Describe("the usage log", func() {
	const (
		logPool = "ceph-objectstore.rgw.log"
		usageNS = "usage"
	)
	hour := func(h int) uint64 {
		return uint64(time.Date(2026, 9, 28, h, 0, 0, 0, time.UTC).Unix())
	}
	var (
		// t0 is in the hour that starts at 12:00 UTC.
		t0                  time.Time
		c                   *fakerados.Cluster
		s                   *driver.Store
		clock               *fakeClock
		alice, carol, david meta.Owner
	)
	openOver := func(ctx context.Context, cluster radosclient.Cluster, kv map[string]string) *driver.Store {
		GinkgoHelper()
		opts := map[string]string{
			"rgw_zone": "ceph-objectstore", "rgw_enable_usage_log": "true", "rgw_usage_max_shards": "4",
			"rgw_usage_max_user_shards": "1", "rgw_usage_log_flush_threshold": "2",
		}
		maps.Copy(opts, kv)
		st, err := driver.Open(ctx, cluster, conf(opts), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		st.ResetUsageLogForTest(t0, time.UTC)
		st.SetTickerForTest(clock.NewTicker)
		return st
	}
	open := func(ctx context.Context, kv map[string]string) *driver.Store {
		GinkgoHelper()
		return openOver(ctx, c, kv)
	}
	// records returns the usage records of the object usage_log_hash names
	// for user at index on s.
	records := func(user string, index uint32) []rgwcls.UsageLogEntry {
		return c.UsageEntries(logPool, usageNS, driver.UsageOIDForTest(s, user, index))
	}
	usageObjects := func(ctx context.Context) []string {
		GinkgoHelper()
		p, err := c.Pool(ctx, logPool, usageNS)
		Expect(err).NotTo(HaveOccurred())
		var oids []string
		Expect(p.ListObjects(ctx, func(oid, _ string) error {
			oids = append(oids, oid)
			return nil
		})).To(Succeed())
		Expect(p.Close()).To(Succeed())
		return oids
	}
	get := func(owner meta.Owner, bucket string, at time.Time) op.UsageEntry {
		return op.UsageEntry{Owner: owner, Bucket: bucket, Time: at, Category: "get_obj", BytesSent: 5, Ops: 1, SuccessfulOps: 1}
	}
	// runFlush runs the flush worker on a context the spec cancels, once it
	// holds its ticker, and returns that cancel and the channel the worker's
	// result arrives on.
	runFlush := func(ctx context.Context, st *driver.Store) (context.CancelFunc, <-chan error) {
		GinkgoHelper()
		runCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		done := make(chan error, 1)
		go func() { done <- st.RunUsageFlushForTest(runCtx) }()
		Eventually(clock.Periods).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(HaveLen(1), "the worker made its ticker")
		return cancel, done
	}
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		t0 = time.Date(2026, 9, 28, 12, 34, 56, 0, time.UTC)
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
		seedRookZone(c, "ceph-objectstore", true)
		clock = newFakeClock(t0)
		s = open(ctx, nil)
		alice = meta.UserOwner(meta.UserID{ID: "alice"})
		carol = meta.UserOwner(meta.UserID{ID: "carol"})
		david = meta.UserOwner(meta.UserID{ID: "david"})
	})

	Describe("usage_log_hash", func() {
		It("shards a user by the hash of its name, and an unnamed one by the index alone", func() {
			Expect(driver.UsageOIDForTest(s, "alice", 0)).To(Equal(fmt.Sprintf("usage.%d", meta.StrHashLinux("alice")%4)))
			Expect(driver.UsageOIDForTest(s, "alice", 7)).To(Equal(driver.UsageOIDForTest(s, "alice", 0)), "rgw_usage_max_user_shards is 1")
			Expect(driver.UsageOIDForTest(s, "", 5)).To(Equal("usage.1"), "5 % 4")
		})

		It("adds the index modulo rgw_usage_max_user_shards to the hash", func(ctx SpecContext) {
			two := open(ctx, map[string]string{"rgw_usage_max_user_shards": "2"})
			Expect(driver.UsageOIDForTest(two, "carol", 3)).To(Equal(fmt.Sprintf("usage.%d", (1+meta.StrHashLinux("carol"))%4)))
		})
	})

	It("merges the entries of one user, bucket and hour into one record and writes it to the zone's usage log", func(ctx SpecContext) {
		s.Log(ctx, op.UsageEntry{Owner: alice, Bucket: "plain", Time: t0, Category: "put_obj", BytesReceived: 10, Ops: 1, SuccessfulOps: 1})
		s.Log(ctx, op.UsageEntry{Owner: alice, Bucket: "plain", Time: t0.Add(time.Minute), Category: "get_obj", BytesSent: 5, Ops: 1})
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		Expect(usageObjects(ctx)).To(ConsistOf(driver.UsageOIDForTest(s, "alice", 0)))
		recs := records("alice", 0)
		Expect(recs).To(HaveLen(1), "one record per user, bucket and hour")
		Expect(recs[0]).To(Equal(rgwcls.UsageLogEntry{
			Owner: "alice", Bucket: "plain", Epoch: hour(12),
			TotalUsage: rgwcls.UsageData{BytesSent: 5, BytesReceived: 10, Ops: 2, SuccessfulOps: 1},
			UsageMap: map[string]rgwcls.UsageData{
				"put_obj": {BytesReceived: 10, Ops: 1, SuccessfulOps: 1},
				"get_obj": {BytesSent: 5, Ops: 1},
			},
		}))
	})

	It("merges a flush into the records the usage log already holds", func(ctx SpecContext) {
		s.Log(ctx, get(alice, "plain", t0))
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		s.Log(ctx, get(alice, "plain", t0.Add(time.Minute)))
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		recs := records("alice", 0)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0].UsageMap).To(Equal(map[string]rgwcls.UsageData{"get_obj": {BytesSent: 10, Ops: 2, SuccessfulOps: 2}}))
		Expect(recs[0].TotalUsage).To(Equal(rgwcls.UsageData{BytesSent: 10, Ops: 2, SuccessfulOps: 2}))
	})

	It("files a requester-pays entry under the payer and keeps the owner", func(ctx SpecContext) {
		e := get(alice, "plain", t0)
		e.Payer = carol
		s.Log(ctx, e)
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		Expect(driver.UsageOIDForTest(s, "carol", 0)).NotTo(Equal(driver.UsageOIDForTest(s, "alice", 0)), "the two users' objects differ")
		Expect(records("alice", 0)).To(BeEmpty())
		recs := records("carol", 0)
		Expect(recs).To(HaveLen(1), "rgw_log.cc:159-165 keys by the payer")
		Expect(recs[0].Payer).To(Equal("carol"))
		Expect(recs[0].Owner).To(Equal("alice"))
	})

	It("keeps the first owner of the entries one payer merges under a bucket and hour", func(ctx SpecContext) {
		for _, owner := range []meta.Owner{alice, david} {
			e := get(owner, "-", t0)
			e.Payer = carol
			s.Log(ctx, e)
		}
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		recs := records("carol", 0)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0].Owner).To(Equal("alice"), "rgw_usage_log_entry::aggregate keeps a set owner")
		Expect(recs[0].TotalUsage.Ops).To(BeEquivalentTo(2))
	})

	It("gives the next user of a flush the next index, which shards it when rgw_usage_max_user_shards is above 1", func(ctx SpecContext) {
		s = open(ctx, map[string]string{"rgw_usage_max_user_shards": "2"})
		s.Log(ctx, get(alice, "plain", t0))
		s.Log(ctx, get(carol, "other", t0))
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		Expect(driver.UsageOIDForTest(s, "carol", 1)).NotTo(Equal(driver.UsageOIDForTest(s, "carol", 0)))
		Expect(records("carol", 1)).To(ContainElement(HaveField("Owner", "carol")), "carol is the second user in order")
		Expect(records("carol", 0)).To(BeEmpty())
	})

	Describe("the hour an entry is filed under", func() {
		It("stays the round timestamp's until a request comes more than an hour after it", func(ctx SpecContext) {
			at := map[string]time.Time{
				"on-the-hour":  time.Date(2026, 9, 28, 13, 0, 0, 900_000_000, time.UTC),
				"after":        time.Date(2026, 9, 28, 13, 0, 1, 0, time.UTC),
				"earlier-hour": t0,
			}
			for _, b := range []string{"on-the-hour", "after", "earlier-hour"} {
				s.Log(ctx, get(alice, b, at[b]))
			}
			Expect(s.FlushUsageForTest(ctx)).To(Succeed())
			epochs := map[string]uint64{}
			for _, r := range records("alice", 0) {
				epochs[r.Bucket] = r.Epoch
			}
			Expect(epochs).To(Equal(map[string]uint64{
				"on-the-hour":  hour(12),
				"after":        hour(13),
				"earlier-hour": hour(13),
			}), "insert_user compares whole seconds with > and never moves the round timestamp back")
		})

		It("starts at the hour of the gateway's local time zone", func(ctx SpecContext) {
			s.ResetUsageLogForTest(t0, time.FixedZone("IST", 5*3600+1800))
			s.Log(ctx, get(alice, "plain", t0))
			Expect(s.FlushUsageForTest(ctx)).To(Succeed())
			Expect(records("alice", 0)).To(ContainElement(HaveField("Epoch", hour(12)+1800)), "18:00 IST is 12:30 UTC")
		})
	})

	It("skips an entry with neither a payer nor an owner at the flush, after counting it", func(ctx SpecContext) {
		var logs bytes.Buffer
		DeferCleanup(driver.CaptureLog(&logs))
		for i := range 3 {
			s.Log(ctx, op.UsageEntry{Bucket: fmt.Sprintf("b%d", i), Time: t0, Category: "get_obj", Ops: 1})
		}
		Expect(logs.String()).To(ContainSubstring(`"msg":"usage log entry has no user; skipping","bucket":"b2"`),
			"the third entry passed the threshold and flushed")
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		Expect(usageObjects(ctx)).To(BeEmpty(), "rgw_rados.cc:1645-1648")
	})

	It("files an entry with a payer and no owner under the payer", func(ctx SpecContext) {
		s.Log(ctx, op.UsageEntry{Payer: carol, Bucket: "plain", Time: t0, Category: "get_obj", Ops: 1})
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		Expect(records("carol", 0)).To(ConsistOf(HaveField("Payer", "carol")))
	})

	Describe("flushing", func() {
		It("flushes from the logging goroutine past the threshold, even once that request's context has ended", func(ctx SpecContext) {
			reqCtx, cancel := context.WithCancel(ctx)
			cancel()
			for i := range 2 {
				s.Log(reqCtx, get(alice, fmt.Sprintf("b%d", i), t0))
			}
			Expect(usageObjects(ctx)).To(BeEmpty(), "two entries do not pass a threshold of 2")
			s.Log(reqCtx, get(alice, "b2", t0))
			Expect(records("alice", 0)).To(HaveLen(3), "the third distinct entry passed the threshold")
			s.Log(reqCtx, get(alice, "b2", t0.Add(time.Minute)))
			s.Log(reqCtx, get(alice, "b3", t0))
			s.Log(reqCtx, get(alice, "b3", t0.Add(time.Minute)))
			Expect(records("alice", 0)).To(HaveLen(3), "an entry merged into one already held is not counted again")
		})

		It("flushes on every tick of rgw_usage_log_tick_interval and stops its ticker when its context ends", func(ctx SpecContext) {
			s.Log(ctx, get(alice, "plain", t0))
			cancel, done := runFlush(ctx, s)
			Expect(clock.Periods()).To(Equal([]time.Duration{30 * time.Second}))
			clock.Advance(29 * time.Second)
			Consistently(usageObjects).WithArguments(ctx).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeEmpty())
			clock.Advance(time.Second)
			Eventually(func() []rgwcls.UsageLogEntry { return records("alice", 0) }).
				WithTimeout(time.Second).WithPolling(time.Millisecond).Should(HaveLen(1))
			s.Log(ctx, get(alice, "later", t0))
			clock.Advance(30 * time.Second)
			Eventually(func() []rgwcls.UsageLogEntry { return records("alice", 0) }).
				WithTimeout(time.Second).WithPolling(time.Millisecond).Should(HaveLen(2))
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
			Expect(clock.Periods()).To(BeEmpty())
		})

		It("flushes once more when its context ends", func(ctx SpecContext) {
			cancel, done := runFlush(ctx, s)
			s.Log(ctx, get(alice, "plain", t0))
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
			Expect(records("alice", 0)).To(HaveLen(1), "~UsageLogger flushes")
		})

		It("returns the final flush's failure", func(ctx SpecContext) {
			c.FailPool(logPool)
			s.Log(ctx, get(alice, "plain", t0))
			cancel, done := runFlush(ctx, s)
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).
				Should(Receive(MatchError(ContainSubstring("final usage log flush"))))
		})

		Describe("behind a flush whose write is stuck", func() {
			var held *heldCluster
			BeforeEach(func(ctx SpecContext) {
				held = &heldCluster{
					Cluster: c, pool: logPool, ns: usageNS,
					entered: make(chan struct{}, 8), release: make(chan struct{}),
				}
				s = openOver(ctx, held, nil)
				s.SetUsageFinalFlushTimeoutForTest(50 * time.Millisecond)
				// Registered after the store's Close, so it runs before it.
				DeferCleanup(func() { close(held.release) })
			})

			It("gives up the final flush at its timeout while a threshold flush holds the semaphore", func(ctx SpecContext) {
				cancel, done := runFlush(ctx, s)
				logged := make(chan struct{})
				go func() {
					defer close(logged)
					for i := range 3 {
						s.Log(ctx, get(alice, fmt.Sprintf("b%d", i), t0))
					}
				}()
				Eventually(held.entered).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(), "the threshold flush is writing")
				s.Log(ctx, get(alice, "late", t0))
				cancel()
				var err error
				Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(&err))
				Expect(err).To(MatchError(ContainSubstring("final usage log flush: waiting for the usage log flush under way")))
				Expect(err).To(MatchError(ContainSubstring("usage log final flush timed out")))
				Consistently(logged).WithTimeout(20*time.Millisecond).WithPolling(time.Millisecond).ShouldNot(BeClosed(),
					"the threshold flush, on its request's goroutine, still waits")
			})

			It("gives up the tick flush under way and the final flush at the timeout", func(ctx SpecContext) {
				s.Log(ctx, get(alice, "plain", t0))
				cancel, done := runFlush(ctx, s)
				clock.Advance(30 * time.Second)
				Eventually(held.entered).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(), "the tick flush is writing")
				s.Log(ctx, get(alice, "late", t0))
				cancel()
				Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).
					Should(Receive(MatchError(ContainSubstring("final usage log flush"))))
				Expect(records("alice", 0)).To(BeEmpty())
			})
		})

		DescribeTable("ticks every second, logging an error, when rgw_usage_log_tick_interval is not positive",
			func(ctx SpecContext, tick string) {
				var logs bytes.Buffer
				DeferCleanup(driver.CaptureLog(&logs))
				st := open(ctx, map[string]string{"rgw_usage_log_tick_interval": tick})
				cancel, done := runFlush(ctx, st)
				Expect(clock.Periods()).To(Equal([]time.Duration{time.Second}))
				cancel()
				Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
				Expect(logs.String()).To(ContainSubstring(`"option":"rgw_usage_log_tick_interval","value":` + tick))
			},
			Entry("zero", "0"),
			Entry("negative", "-5"),
		)

		It("returns the first failed write and drops the batch, as log_usage does", func(ctx SpecContext) {
			var adds int
			c.RegisterClass("rgw", func(call *fakerados.ClassCall) ([]byte, int32) {
				if call.Method == "user_usage_log_add" {
					adds++
					if adds == 2 {
						return nil, -int32(syscall.EIO)
					}
				}
				return fakerados.RGWClass()(call)
			}, fakerados.RGWWriteMethods...)
			s.Log(ctx, get(alice, "plain", t0))
			s.Log(ctx, get(carol, "other", t0))
			Expect(driver.UsageOIDForTest(s, "carol", 1)).To(Equal("usage.1"), "written first")
			Expect(driver.UsageOIDForTest(s, "alice", 0)).To(Equal("usage.2"))
			Expect(s.FlushUsageForTest(ctx)).To(MatchError(ContainSubstring("usage.2")))
			Expect(records("carol", 1)).To(HaveLen(1))
			Expect(records("alice", 0)).To(BeEmpty())
			Expect(s.FlushUsageForTest(ctx)).To(Succeed())
			Expect(adds).To(Equal(2), "the failed batch is not written again")
		})

		It("logs a threshold flush that fails", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			c.FailPool(logPool)
			for i := range 3 {
				s.Log(ctx, get(alice, fmt.Sprintf("b%d", i), t0))
			}
			Expect(logs.String()).To(ContainSubstring(`"msg":"usage log flush failed"`))
		})
	})

	Describe("with rgw_enable_usage_log off", func() {
		It("logs nothing", func(ctx SpecContext) {
			off := open(ctx, map[string]string{"rgw_enable_usage_log": "false"})
			for i := range 3 {
				off.Log(ctx, get(alice, fmt.Sprintf("b%d", i), t0))
			}
			Expect(off.FlushUsageForTest(ctx)).To(Succeed())
			Expect(usageObjects(ctx)).To(BeEmpty())
		})

		It("runs no flush worker, which it does when on", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			off := open(ctx, map[string]string{"rgw_enable_usage_log": "false"})
			for _, st := range []*driver.Store{s, off} {
				runCtx, cancel := context.WithCancel(ctx)
				cancel()
				Expect(st.Run(runCtx)).To(Succeed())
			}
			Expect(bytes.Count(logs.Bytes(), []byte(`"worker":"usage-flush"`))).To(Equal(1))
		})
	})
})
