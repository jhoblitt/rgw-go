package driver_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// holdCluster is a fakerados cluster whose pools can hold the next write ops
// calling a cls method until the spec releases them, so that writers going
// through it stop just before that call while others go on.
type holdCluster struct {
	*fakerados.Cluster
	mu      sync.Mutex
	method  string
	left    int
	arrived chan struct{}
	release chan struct{}
}

// hold makes the next n write ops calling method wait until release is
// closed, and closes arrived once all n wait.
func (h *holdCluster) hold(method string, n int) (arrived <-chan struct{}, release chan<- struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.method, h.left, h.arrived, h.release = method, n, make(chan struct{}), make(chan struct{})
	return h.arrived, h.release
}

// take spends one of the holds on op when op calls the held method; last
// reports the hold that was left.
func (h *holdCluster) take(op *radosclient.WriteOp) (arrived, release chan struct{}, last bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.left == 0 {
		return nil, nil, false
	}
	for _, st := range op.Steps() {
		if x, ok := st.(*radosclient.ExecStep); ok && x.Method == h.method {
			h.left--
			return h.arrived, h.release, h.left == 0
		}
	}
	return nil, nil, false
}

func (h *holdCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := h.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return &holdPool{Pool: p, h: h}, nil
}

type holdPool struct {
	radosclient.Pool
	h *holdCluster
}

func (p *holdPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	if arrived, release, last := p.h.take(op); arrived != nil {
		if last {
			close(arrived)
		}
		<-release
	}
	return p.Pool.Write(ctx, oid, op, flags)
}

// condFunc is a link's condition as the specs write it.
type condFunc = func(context.Context, *op.ObjectState) (bool, error)

// ifMatch is If-Match on etag: it holds on a current version with that ETag,
// and rests on that version.
func ifMatch(etag string) condFunc {
	return func(_ context.Context, cur *op.ObjectState) (bool, error) {
		if !cur.Exists || cur.ETag != etag {
			return false, op.ErrPreconditionFailed
		}
		return true, nil
	}
}

// ifNoneMatchAny is If-None-Match: *, which holds when no version is current
// and rests on none.
func ifNoneMatchAny(_ context.Context, cur *op.ObjectState) (bool, error) {
	if cur.Exists {
		return false, op.ErrPreconditionFailed
	}
	return false, nil
}

var _ = Describe("a conditional link", func() {
	var (
		c      *fakerados.Cluster
		hc     *holdCluster
		s      *driver.Store
		rec    *op.BucketRecord
		clock  atomic.Int64
		shard  string
		olhOID string
		// backoffs are the backoffs the specs' conditional links took, which
		// none sleeps.
		backoffsMu sync.Mutex
		backoffs   []time.Duration
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	backedOff := func() []time.Duration {
		backoffsMu.Lock()
		defer backoffsMu.Unlock()
		return slices.Clone(backoffs)
	}
	open := func(ctx context.Context, r denc.Release) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, hc, conf(nil), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(_ context.Context, d time.Duration) error {
			backoffsMu.Lock()
			defer backoffsMu.Unlock()
			backoffs = append(backoffs, d)
			return nil
		})
	}
	BeforeEach(func(ctx SpecContext) {
		clock.Store(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).UnixNano())
		backoffs = nil
		c = newPutCluster()
		c.SetClock(now)
		hc = &holdCluster{Cluster: c}
		open(ctx, denc.Squid)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
		olhOID = olhHeadOID(version(""))
	})
	AfterEach(func() { settle(s) })

	// write is rgw-go's versioned write of version instance up to its link:
	// the prepare, the instance head and the awaited ADD completion.
	write := func(ctx context.Context, instance string) meta.ObjKey {
		GinkgoHelper()
		v := version(instance)
		x := s.NewIndexOpForTest(rec, v, "go-tag-"+instance)
		x.SetBILogFlagsForTest(rgwcls.BILogFlagVersionedOp)
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		ver := writeInstanceHead(ctx, c, v)
		Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: ver}, versionMeta(v))).To(Succeed())
		return v
	}
	put := func(ctx context.Context, instance string) {
		GinkgoHelper()
		Expect(s.SetOLHForTest(ctx, rec, write(ctx, instance), driver.OLHLink{})).To(Succeed())
	}
	link := func(ctx context.Context, instance string, cond condFunc) error {
		GinkgoHelper()
		return s.SetOLHForTest(ctx, rec, write(ctx, instance), driver.OLHLink{Cond: cond})
	}
	unlink := func(ctx context.Context, instance string) error {
		return s.UnlinkObjInstanceForTest(ctx, rec, version(instance), driver.OLHUnlink{})
	}
	// after arms fn to run once, after the n-th later write op on oid lands.
	after := func(pool, oid string, n int, fn func()) {
		seen := 0
		c.AfterWrite(pool, "", oid, func() {
			if seen++; seen == n {
				c.AfterWrite(pool, "", oid, nil)
				fn()
			}
		})
	}

	olhHead := func() *fakerados.Object { return c.Object(testDataPool, "", olhOID) }
	versionHead := func(instance string) *fakerados.Object {
		return c.Object(testDataPool, "", instanceHeadOID(version(instance)))
	}
	pending := func() []string {
		var out []string
		if o := olhHead(); o != nil {
			for name := range o.Xattrs {
				if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
					out = append(out, name)
				}
			}
		}
		slices.Sort(out)
		return out
	}
	target := func() string {
		GinkgoHelper()
		o := olhHead()
		Expect(o).NotTo(BeNil(), "the OLH head")
		b, ok := o.Xattrs[meta.AttrOLHInfo]
		Expect(ok).To(BeTrue(), "the OLH head's olh.info")
		d := denc.NewDecoder(b)
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info.Target.Key.Instance
	}
	listed := func() []string {
		var out []string
		for _, en := range c.ListEntries(rookIndexPool, "", shard, "k") {
			out = append(out, en.Key.Instance)
		}
		return out
	}
	olhLog := func() map[uint64][]rgwcls.OLHLogEntry {
		GinkgoHelper()
		e, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue(), "the OLH entry of k")
		return e.PendingLog
	}
	// gone checks that version instance has no head, instance entry or list
	// entry, as a write that answered PreconditionFailed leaves it.
	gone := func(instance string) {
		GinkgoHelper()
		settle(s)
		Expect(versionHead(instance)).To(BeNil(), "the head of version %q", instance)
		_, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: instance})
		Expect(ok).To(BeFalse(), "the instance entry of version %q", instance)
		Expect(listed()).NotTo(ContainElement(instance), "the list entry of version %q", instance)
	}
	// current checks that version instance is current on the head and in
	// the listing, with no pending attr and the log trimmed.
	current := func(instance string) {
		GinkgoHelper()
		Expect(target()).To(Equal(instance), "olh.info")
		l := listed()
		Expect(l).NotTo(BeEmpty())
		Expect(l[0]).To(Equal(instance), "the current version lists first")
		Expect(pending()).To(BeEmpty(), "no pending attr")
		Expect(olhLog()).To(BeEmpty(), "the log is trimmed")
	}
	// conditionFailed checks that err answers PreconditionFailed and nothing
	// that would read as a conflict.
	conditionFailed := func(err error) {
		GinkgoHelper()
		Expect(err).To(MatchError(op.ErrPreconditionFailed))
		Expect(err).NotTo(MatchError(op.ErrConcurrentModification))
	}

	It("refuses a condition on a link of the null version's data, writing nothing", func(ctx SpecContext) {
		v := write(ctx, "")
		c.ResetCounters()
		called := false
		cond := func(context.Context, *op.ObjectState) (bool, error) {
			called = true
			return false, nil
		}
		err := s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: cond})
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(err).To(MatchError(ContainSubstring("before its head write")))
		Expect(called).To(BeFalse(), "the condition is not evaluated")
		Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero())
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
	})

	It("counts a current delete marker as no version, reading no head for it", func(ctx SpecContext) {
		put(ctx, "v1")
		marker := driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}}
		Expect(s.SetOLHForTest(ctx, rec, version("dm"), marker)).To(Succeed())
		c.ResetCounters()
		var seen []op.ObjectState
		cond := func(ctx context.Context, cur *op.ObjectState) (bool, error) {
			seen = append(seen, *cur)
			return ifNoneMatchAny(ctx, cur)
		}
		Expect(link(ctx, "c", cond)).To(Succeed())
		Expect(seen).To(HaveLen(1))
		Expect([]any{seen[0].Exists, seen[0].DeleteMarker, seen[0].Key}).To(Equal([]any{false, true, version("dm")}))
		Expect(c.Reads(testDataPool, "", instanceHeadOID(version("dm")))).To(BeZero())
		Expect(listed()).To(Equal([]string{"c", "dm", "v1"}))
		current("c")
	})

	Describe("a radosgw writer inits between a conditional write's init and its link", func() {
		DescribeTable("answers by which of the two links first",
			func(ctx SpecContext, r denc.Release, rgwFirst bool) {
				open(ctx, r)
				rw := newRGWWriter(c, s, rec, r, "rgw")
				rw.Prepare(ctx)
				rw.HeadWrite(ctx)
				rw.Complete(ctx)
				v := write(ctx, "go")
				replay := func() {
					rw.Link(ctx)
					rw.ReadLog(ctx)
					rw.Apply(ctx)
					rw.Trim(ctx)
				}
				after(testDataPool, olhOID, 1, func() {
					rw.ReadHead(ctx)
					rw.InitRetrying(ctx)
					if rgwFirst {
						replay()
						return
					}
					after(rookIndexPool, shard, 1, replay)
				})
				err := s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifNoneMatchAny})
				if rgwFirst {
					conditionFailed(err)
					gone("go")
					Expect(listed()).To(Equal([]string{"rgw"}))
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(listed()).To(Equal([]string{"rgw", "go"}))
				}
				current("rgw")
			},
			Entry("it links first, on squid: the conditional write answers 412 and its instance is gone", denc.Squid, true),
			Entry("it links first, on tentacle", denc.Tentacle, true),
			Entry("it links second, on squid: the conditional write succeeds and the radosgw-shaped version is current", denc.Squid, false),
			Entry("it links second, on tentacle", denc.Tentacle, false),
		)
	})

	Describe("an unconditional rgw-go writer inits between a conditional write's init and its link", func() {
		DescribeTable("answers 412, its instance gone, when the other links first",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				v := write(ctx, "go")
				after(testDataPool, olhOID, 1, func() { put(ctx, "u") })
				conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifNoneMatchAny}))
				gone("go")
				Expect(listed()).To(Equal([]string{"u"}))
				current("u")
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)

		DescribeTable("succeeds when the other links second",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				v := write(ctx, "go")
				done := make(chan error, 1)
				after(testDataPool, olhOID, 1, func() {
					u := write(ctx, "u")
					arrived, release := hc.hold("bucket_link_olh", 1)
					go func() { done <- s.SetOLHForTest(ctx, rec, u, driver.OLHLink{}) }()
					Eventually(arrived).WithTimeout(5*time.Second).Should(BeClosed(), "the other writer inits and reaches its link")
					after(rookIndexPool, shard, 1, func() {
						close(release)
						Eventually(done).WithTimeout(5*time.Second).Should(Receive(BeNil()), "the other writer, whose init came before the conditional link")
					})
				})
				Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifNoneMatchAny})).To(Succeed())
				Expect(listed()).To(Equal([]string{"u", "go"}))
				current("u")
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)
	})

	It("answers 412 for If-None-Match: * when another writer removes the current delete marker between the init and the link, an older version current again", func(ctx SpecContext) {
		put(ctx, "v1")
		marker := driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}}
		Expect(s.SetOLHForTest(ctx, rec, version("dm"), marker)).To(Succeed())
		v := write(ctx, "c")
		after(testDataPool, olhOID, 1, func() { Expect(unlink(ctx, "dm")).To(Succeed()) })
		conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifNoneMatchAny}))
		gone("c")
		Expect(listed()).To(Equal([]string{"v1"}), "the version the marker's removal made current, listed at its own epoch")
		current("v1")
	})

	It("answers 412 when another writer's delete marker links between the init and the link", func(ctx SpecContext) {
		put(ctx, "v1")
		v := write(ctx, "c")
		after(testDataPool, olhOID, 1, func() {
			marker := driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}}
			Expect(s.SetOLHForTest(ctx, rec, version("dm"), marker)).To(Succeed())
		})
		conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")}))
		gone("c")
		Expect(listed()).To(Equal([]string{"dm", "v1"}))
		current("dm")
	})

	It("answers 412 for a conditional named delete marker another writer links before, and leaves no marker", func(ctx SpecContext) {
		put(ctx, "v1")
		after(testDataPool, olhOID, 1, func() { put(ctx, "x") })
		marker := driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}, Cond: ifMatch("etag-v1")}
		conditionFailed(s.SetOLHForTest(ctx, rec, version("m"), marker))
		settle(s)
		_, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "m"})
		Expect(ok).To(BeFalse(), "the marker's instance entry")
		Expect(listed()).To(Equal([]string{"x", "v1"}))
		current("x")
	})

	It("answers 412 for If-Match on a key's only version that another writer deletes between the init and the link, and leaves the key no version", func(ctx SpecContext) {
		put(ctx, "v1")
		v := write(ctx, "c")
		after(testDataPool, olhOID, 1, func() { Expect(unlink(ctx, "v1")).To(Succeed()) })
		conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")}))
		gone("c")
		gone("v1")
		Expect(listed()).To(BeEmpty())
		Expect(olhHead()).To(BeNil(), "the OLH is cleared with the key's last version")
		_, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeFalse())
	})

	DescribeTable("answers 412 for If-Match on a key's only version that a radosgw-shaped delete removes between the init and the link",
		func(ctx SpecContext, r denc.Release) {
			open(ctx, r)
			put(ctx, "v1")
			v := write(ctx, "c")
			after(testDataPool, olhOID, 1, func() {
				// unlink_obj_instance's head read and init, then
				// bucket_unlink_instance as radosgw builds it; the replay is
				// update_olh's, which reads the log from its start as rgw-go's
				// replay does.
				rw := newRGWWriter(c, s, rec, r, "v1")
				rw.ReadHead(ctx)
				rw.InitRetrying(ctx)
				u := rgwcls.UnlinkInstanceOp{
					Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, OpTag: rw.opTag, OLHTag: string(rw.head.Attrs[meta.AttrOLHIDTag]),
					BILogFlags: rgwcls.BILogFlagVersionedOp, ZonesTrace: rw.zonesTrace(),
				}
				Expect(rw.shardWrite(ctx, func(w *radosclient.WriteOp) { rgwcls.UnlinkInstance(w, u, r) })).To(Succeed())
				h, err := s.ReadOLHHeadForTest(ctx, rec, version(""))
				Expect(err).NotTo(HaveOccurred())
				Expect(s.UpdateOLHForTest(ctx, rec, h)).To(Succeed())
			})
			conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")}))
			gone("c")
			gone("v1")
			Expect(listed()).To(BeEmpty())
			Expect(olhHead()).To(BeNil(), "the OLH is cleared with the key's last version")
		},
		Entry("on squid", denc.Squid),
		Entry("on tentacle", denc.Tentacle),
	)

	Describe("two If-None-Match: * rgw-go writers of a fresh key", func() {
		It("lets one succeed and answers the other 412 once its wait sees the first's version, its instance gone", func(ctx SpecContext) {
			second := make(chan error, 1)
			waiting, firstDone := make(chan struct{}), make(chan struct{})
			var once sync.Once
			// The second writer's first backoff holds it until the first
			// writer has returned; it is the only writer that waits.
			driver.SetCondSleepForTest(s, func(ctx context.Context, _ time.Duration) error {
				once.Do(func() {
					close(waiting)
					select {
					case <-firstDone:
					case <-ctx.Done():
					}
				})
				return ctx.Err()
			})
			after(testDataPool, olhOID, 1, func() {
				b := write(ctx, "b")
				go func() { second <- s.SetOLHForTest(ctx, rec, b, driver.OLHLink{Cond: ifNoneMatchAny}) }()
				Eventually(waiting).WithTimeout(5*time.Second).Should(BeClosed(), "the second writer met the first's pending attr and backs off")
			})
			Expect(link(ctx, "a", ifNoneMatchAny)).To(Succeed())
			close(firstDone)
			var err error
			Eventually(second).WithTimeout(5 * time.Second).Should(Receive(&err))
			conditionFailed(err)
			gone("b")
			Expect(listed()).To(Equal([]string{"a"}))
			current("a")
		})

		Describe("past the wait's bound", func() {
			var stale string
			BeforeEach(func(ctx SpecContext) {
				// A writer that died after its init leaves a pending attr no
				// writer removes.
				dead := newRGWWriter(c, s, rec, denc.Squid, "dead")
				dead.ReadHead(ctx)
				Expect(dead.Init(ctx)).To(BeTrue())
				stale = meta.AttrOLHPendingPrefix + dead.opTag
			})

			It("lets the one that links first commit, and voids the other, both inited without the pending check", func(ctx SpecContext) {
				var second error
				after(testDataPool, olhOID, 1, func() { second = link(ctx, "b", ifNoneMatchAny) })
				first := link(ctx, "a", ifNoneMatchAny)
				Expect(second).NotTo(HaveOccurred(), "the second writer linked first")
				conditionFailed(first)
				gone("a")
				Expect(listed()).To(Equal([]string{"b"}))
				Expect(target()).To(Equal("b"))
				Expect(pending()).To(ConsistOf(stale), "the dead writer's attr alone stays")
				for _, w := range c.WritesTo(testDataPool, "", olhOID) {
					for _, st := range w.Steps() {
						if x, ok := st.(*radosclient.ExecStep); ok {
							Expect(x.Method).NotTo(Equal("obj_check_attrs_prefix"), "no init past the bound checks for pending attrs")
						}
					}
				}
			})

			It("commits at most one of several writers that all init before any links, and every 412 leaves no instance", func(ctx SpecContext) {
				names := []string{"w0", "w1", "w2", "w3"}
				keys := make([]meta.ObjKey, len(names))
				for i, n := range names {
					keys[i] = write(ctx, n)
				}
				arrived, release := hc.hold("bucket_link_olh", len(names))
				errs := make([]error, len(names))
				var wg sync.WaitGroup
				for i := range names {
					wg.Go(func() { errs[i] = s.SetOLHForTest(ctx, rec, keys[i], driver.OLHLink{Cond: ifNoneMatchAny}) })
				}
				Eventually(arrived).WithTimeout(5*time.Second).Should(BeClosed(), "every writer inits and reaches its link")
				close(release)
				wg.Wait()
				var committed []string
				for i, err := range errs {
					if err == nil {
						committed = append(committed, names[i])
						continue
					}
					conditionFailed(err)
					gone(names[i])
				}
				Expect(len(committed)).To(BeNumerically("<=", 1), "committed: %v", committed)
				Expect(listed()).To(ConsistOf(committed))
			})
		})
	})

	Describe("radosgw's noncurrent transition leaves a pending attr", func() {
		var stale string
		BeforeEach(func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			rw := newRGWWriter(c, s, rec, denc.Squid, "v0")
			rw.epoch = 2
			rw.Run(ctx)
			stale = meta.AttrOLHPendingPrefix + rw.opTag
			Expect(pending()).To(ConsistOf(stale), "radosgw's link at an old epoch logs nothing, so no replay removes its attr")
		})

		It("lets a correct conditional write succeed once the wait's bound passes", func(ctx SpecContext) {
			c.ResetCounters()
			err := link(ctx, "c", ifMatch("etag-v2"))
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Reads(testDataPool, "", olhOID)).To(Equal(driver.OLHCondBackoffs+1), "one read an attempt: each backoff, then the attempt that links")
			Expect(backedOff()).To(HaveLen(driver.OLHCondBackoffs), "the attr was written as the write began, so it is waited on")
			Expect(target()).To(Equal("c"))
			Expect(listed()[0]).To(Equal("c"))
			Expect(pending()).To(ConsistOf(stale))
		})

		It("answers an incorrect one 412 at once", func(ctx SpecContext) {
			c.ResetCounters()
			conditionFailed(link(ctx, "c", ifNoneMatchAny))
			Expect(c.Reads(testDataPool, "", olhOID)).To(Equal(1), "the condition fails before any wait")
			Expect(c.Reads(rookIndexPool, "", shard)).To(Equal(1), "the follow's one log read, which applies nothing")
			Expect(backedOff()).To(BeEmpty())
			gone("c")
			Expect(target()).To(Equal("v2"))
		})
	})

	Describe("If-Match on a pre-versioning object", func() {
		BeforeEach(func(ctx SpecContext) { write(ctx, "") })

		It("is not voided by the class's converted entry at epoch 1", func(ctx SpecContext) {
			Expect(link(ctx, "c", ifMatch("etag-"))).To(Succeed())
			Expect(listed()).To(Equal([]string{"c", ""}), "the plain object converted to the null version at epoch 1")
			current("c")
		})

		It("is voided by a radosgw writer that links between its init and its link", func(ctx SpecContext) {
			rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
			rw.Prepare(ctx)
			rw.HeadWrite(ctx)
			rw.Complete(ctx)
			after(testDataPool, olhOID, 1, func() {
				rw.ReadHead(ctx)
				rw.InitRetrying(ctx)
				rw.Link(ctx)
				rw.ReadLog(ctx)
				rw.Apply(ctx)
				rw.Trim(ctx)
			})
			conditionFailed(link(ctx, "c", ifMatch("etag-")))
			gone("c")
			Expect(listed()).To(Equal([]string{"rgw", ""}))
			current("rgw")
		})
	})

	Describe("the wait on other writers' pending modifications", func() {
		BeforeEach(func(ctx SpecContext) { put(ctx, "v1") })

		Describe("a stopped writer's link, logged and never replayed", func() {
			BeforeEach(func(ctx SpecContext) {
				Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{SkipUpdate: true})).To(Succeed())
				Expect(target()).To(Equal("v1"), "the stopped writer's link is not applied")
			})
			// seenBy is the condition cond, recording each version it is
			// evaluated on into seen.
			seenBy := func(seen *[]string, cond condFunc) condFunc {
				return func(ctx context.Context, cur *op.ObjectState) (bool, error) {
					*seen = append(*seen, cur.Key.Instance)
					return cond(ctx, cur)
				}
			}

			It("is applied before the condition is evaluated, so If-Match on its version succeeds", func(ctx SpecContext) {
				var seen []string
				Expect(link(ctx, "c", seenBy(&seen, ifMatch("etag-v2")))).To(Succeed())
				Expect(seen).To(Equal([]string{"v2"}), "evaluated once, on the version the follow made current")
				Expect(backedOff()).To(BeEmpty(), "the follow's replay removed the attr")
				Expect(listed()).To(Equal([]string{"c", "v2", "v1"}))
				current("c")
			})

			It("is applied before the condition is evaluated when its attr is older than the stale age", func(ctx SpecContext) {
				clock.Add(int64(driver.OLHCondStaleForTest() + time.Millisecond))
				var seen []string
				Expect(link(ctx, "c", seenBy(&seen, ifMatch("etag-v2")))).To(Succeed())
				Expect(seen).To(Equal([]string{"v2"}))
				current("c")
			})

			It("makes If-Match on the version before it fail at once", func(ctx SpecContext) {
				var seen []string
				conditionFailed(link(ctx, "c", seenBy(&seen, ifMatch("etag-v1"))))
				Expect(seen).To(Equal([]string{"v2"}))
				gone("c")
				current("v2")
			})

			It("is evaluated on the head read again when another reader's replay refused the follow's", func(ctx SpecContext) {
				c.BeforeWrite(testDataPool, "", olhOID, func(*fakerados.Object) {
					// The follow's apply is the first write to the OLH head;
					// another reader applies the same log first.
					c.BeforeWrite(testDataPool, "", olhOID, nil)
					h, err := s.ReadOLHHeadForTest(ctx, rec, version(""))
					Expect(err).NotTo(HaveOccurred())
					Expect(s.UpdateOLHForTest(ctx, rec, h)).To(Succeed())
				})
				var seen []string
				Expect(link(ctx, "c", seenBy(&seen, ifMatch("etag-v2")))).To(Succeed())
				Expect(seen).To(Equal([]string{"v2"}), "evaluated on the head as the other reader's replay left it")
				current("c")
			})
		})

		It("removes its version when the context ends before an attempt's head read", func(ctx SpecContext) {
			v := write(ctx, "c")
			wctx, cancel := context.WithCancel(ctx)
			defer cancel()
			c.BeforeWrite(testDataPool, "", olhOID, func(o *fakerados.Object) {
				// The first attempt's init is refused, another writer having
				// moved the OLH tag, and the client goes before the next
				// attempt reads the head.
				c.BeforeWrite(testDataPool, "", olhOID, nil)
				o.Xattrs[meta.AttrOLHIDTag] = []byte("racerracerracerracerracerracer00")
				cancel()
			})
			Expect(s.SetOLHForTest(wctx, rec, v, driver.OLHLink{})).To(MatchError(context.Canceled))
			gone("c")
		})

		It("removes its version when the context ends during the wait", func(ctx SpecContext) {
			dead := newRGWWriter(c, s, rec, denc.Squid, "dead")
			dead.ReadHead(ctx)
			Expect(dead.Init(ctx)).To(BeTrue())
			v := write(ctx, "c")
			wctx, cancel := context.WithCancel(ctx)
			defer cancel()
			driver.SetCondSleepForTest(s, func(ctx context.Context, _ time.Duration) error {
				cancel()
				return ctx.Err()
			})
			Expect(s.SetOLHForTest(wctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")})).To(MatchError(context.Canceled))
			gone("c")
			Expect(target()).To(Equal("v1"))
		})

		It("expires a pending attr past rgw_olh_pending_timeout_sec and links without waiting", func(ctx SpecContext) {
			dead := newRGWWriter(c, s, rec, denc.Squid, "dead")
			dead.ReadHead(ctx)
			Expect(dead.Init(ctx)).To(BeTrue())
			clock.Add(int64(time.Hour + time.Second))
			c.ResetCounters()
			Expect(link(ctx, "c", ifMatch("etag-v1"))).To(Succeed())
			Expect(c.Reads(testDataPool, "", olhOID)).To(Equal(1), "one attempt")
			current("c")
		})

		It("evaluates the condition again after an init the olh.info guard canceled", func(ctx SpecContext) {
			var seen []string
			cond := func(_ context.Context, cur *op.ObjectState) (bool, error) {
				seen = append(seen, cur.Key.Instance)
				return true, nil
			}
			c.BeforeWrite(testDataPool, "", olhOID, func(*fakerados.Object) {
				c.BeforeWrite(testDataPool, "", olhOID, nil)
				put(ctx, "v2")
			})
			Expect(link(ctx, "c", cond)).To(Succeed())
			Expect(seen).To(Equal([]string{"v1", "v2"}))
			Expect(listed()).To(Equal([]string{"c", "v2", "v1"}))
			current("c")
		})

		It("refuses at its init a pending attr that came after its read, and drops that check past the bound", func(ctx SpecContext) {
			dead := newRGWWriter(c, s, rec, denc.Squid, "dead")
			c.BeforeWrite(testDataPool, "", olhOID, func(*fakerados.Object) {
				c.BeforeWrite(testDataPool, "", olhOID, nil)
				dead.ReadHead(ctx)
				Expect(dead.Init(ctx)).To(BeTrue(), "a writer that inits between the read and the init, and dies")
			})
			Expect(link(ctx, "c", ifMatch("etag-v1"))).To(Succeed())
			var checked []bool
			for _, w := range c.WritesTo(testDataPool, "", olhOID) {
				if slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
					x, ok := st.(*radosclient.SetXattrStep)
					return ok && strings.HasPrefix(x.Name, meta.AttrOLHPendingPrefix)
				}) {
					checked = append(checked, slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
						x, ok := st.(*radosclient.ExecStep)
						return ok && x.Method == "obj_check_attrs_prefix"
					}))
				}
			}
			Expect(checked[len(checked)-2:]).To(Equal([]bool{true, false}),
				"the inits that set a pending attr: the conditional link's first, refused, then its last, past the bound")
			Expect(pending()).To(ConsistOf(meta.AttrOLHPendingPrefix + dead.opTag))
			Expect(target()).To(Equal("c"))
		})

		Describe("a pending attr of a writer that died after its init", func() {
			var dead *rgwWriter
			BeforeEach(func(ctx SpecContext) {
				dead = newRGWWriter(c, s, rec, denc.Squid, "dead")
				dead.ReadHead(ctx)
				Expect(dead.Init(ctx)).To(BeTrue())
			})
			// pendingChecks says, for each init since the counters were reset,
			// a write to the OLH head that sets a pending attr, whether it
			// refused pending attrs.
			pendingChecks := func() []bool {
				var out []bool
				for _, w := range c.WritesTo(testDataPool, "", olhOID) {
					if slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
						x, ok := st.(*radosclient.SetXattrStep)
						return ok && strings.HasPrefix(x.Name, meta.AttrOLHPendingPrefix)
					}) {
						out = append(out, slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
							x, ok := st.(*radosclient.ExecStep)
							return ok && x.Method == "obj_check_attrs_prefix"
						}))
					}
				}
				return out
			}

			It("adds no wait when already older than the stale age at the first read, the init leaving out the pending check", func(ctx SpecContext) {
				clock.Add(int64(driver.OLHCondStaleForTest() + time.Millisecond))
				c.ResetCounters()
				Expect(link(ctx, "c", ifMatch("etag-v1"))).To(Succeed())
				Expect(backedOff()).To(BeEmpty())
				Expect(pendingChecks()).To(Equal([]bool{false}), "one init, without the pending check")
				Expect(pending()).To(ConsistOf(meta.AttrOLHPendingPrefix + dead.opTag))
				Expect(target()).To(Equal("c"))
			})

			It("is still waited on when no older than the stale age at the first read", func(ctx SpecContext) {
				clock.Add(int64(driver.OLHCondStaleForTest()))
				c.ResetCounters()
				Expect(link(ctx, "c", ifMatch("etag-v1"))).To(Succeed())
				Expect(backedOff()).To(HaveLen(driver.OLHCondBackoffs))
				Expect(pendingChecks()).To(Equal([]bool{false}), "one init, past the bound")
				Expect(target()).To(Equal("c"))
			})

			It("still refuses at the init another writer's attr that came after the follow expired this one", func(ctx SpecContext) {
				clock.Add(int64(time.Hour + time.Second))
				live := newRGWWriter(c, s, rec, denc.Squid, "live")
				after(testDataPool, olhOID, 1, func() {
					// The follow's removal of the expired attr is the first
					// write to the OLH head; a live writer inits right after it.
					live.ReadHead(ctx)
					Expect(live.Init(ctx)).To(BeTrue())
				})
				c.ResetCounters()
				Expect(link(ctx, "c", ifMatch("etag-v1"))).To(Succeed())
				Expect(pendingChecks()[:2]).To(Equal([]bool{false, true}), "the live writer's init, then the conditional one, which refuses its attr")
				Expect(backedOff()).To(HaveLen(driver.OLHCondBackoffs), "and the link waited on it")
				Expect(pending()).To(ConsistOf(meta.AttrOLHPendingPrefix + live.opTag))
				Expect(target()).To(Equal("c"))
			})
		})

		It("waits by backoffs that double from 5ms to 160ms, each jittered over its upper half, the least of them adding up to the stale age", func() {
			var least time.Duration
			for n := range driver.OLHCondBackoffs {
				d := min(5*time.Millisecond<<n, 160*time.Millisecond)
				least += d / 2
				for range 20 {
					Expect(driver.OLHCondBackoffForTest(n)).To(And(BeNumerically(">=", d/2), BeNumerically("<=", d)), "backoff %d", n)
				}
			}
			Expect(driver.OLHCondStaleForTest()).To(Equal(least))
			Expect(least).To(Equal(317500 * time.Microsecond))
		})
	})

	Describe("the check after the link", func() {
		It("lists past a page of newer versions to find a writer that linked between the init and the link", func(ctx SpecContext) {
			v := write(ctx, "c")
			after(testDataPool, olhOID, 1, func() {
				put(ctx, "x")
				after(rookIndexPool, shard, 1, func() {
					for i := range driver.OLHCondListPage + 2 {
						put(ctx, fmt.Sprintf("n%02d", i))
					}
				})
			})
			conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifNoneMatchAny}))
			gone("c")
			Expect(listed()).To(HaveLen(driver.OLHCondListPage+3), "the newer versions and the writer between")
			current(fmt.Sprintf("n%02d", driver.OLHCondListPage+1))
		})

		It("voids the link, answering the listing's error, when the listing fails", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "c")
			after(rookIndexPool, shard, 1, func() { c.FailNextRead(rookIndexPool, "", shard, syscall.EIO) })
			err := s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")})
			var re *radosclient.Error
			Expect(errors.As(err, &re)).To(BeTrue(), "the listing's answer: %v", err)
			Expect(re.Errno).To(BeEquivalentTo(syscall.EIO))
			Expect(err).NotTo(MatchError(op.ErrPreconditionFailed), "the condition's outcome is unknown")
			gone("c")
			current("v1")
		})

		It("answers 412, with nothing to unlink, when another writer removed its version after the link and the matched version went before it", func(ctx SpecContext) {
			put(ctx, "v0")
			put(ctx, "v1")
			v := write(ctx, "c")
			after(testDataPool, olhOID, 1, func() {
				Expect(unlink(ctx, "v1")).To(Succeed(), "the version the condition matched goes")
				// The removal's index completion is sent in the background;
				// the next write to the shard must be the conditional link.
				settle(s)
				after(rookIndexPool, shard, 1, func() { Expect(unlink(ctx, "c")).To(Succeed()) })
			})
			conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")}))
			gone("c")
			gone("v1")
			current("v0")
		})

		It("answers success when another writer removed its version after the link and the matched version is still listed: the removal shows the link landed", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "c")
			after(rookIndexPool, shard, 1, func() { Expect(unlink(ctx, "c")).To(Succeed()) })
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")})).To(Succeed())
			gone("c")
			current("v1")
		})

		It("answers success when another writer removed its version after the link and the matched version was linked again above the read epoch before it", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "c")
			after(testDataPool, olhOID, 1, func() {
				Expect(s.SetOLHForTest(ctx, rec, version("v1"), driver.OLHLink{})).To(Succeed(), "v1 linked again, current at a new epoch")
				after(rookIndexPool, shard, 1, func() { Expect(unlink(ctx, "c")).To(Succeed()) })
			})
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")})).To(Succeed())
			gone("c")
			current("v1")
		})

		It("serves the void version to a reader that follows the OLH between the link and the check, and the version before it after the void", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "c")
			var during string
			after(testDataPool, olhOID, 1, func() {
				put(ctx, "x")
				after(rookIndexPool, shard, 1, func() {
					st, err := s.StatObject(ctx, rec, version(""))
					Expect(err).NotTo(HaveOccurred())
					during = st.Key.Instance
				})
			})
			conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")}))
			Expect(during).To(Equal("c"), "the reader's follow applied the void version's link")
			st, err := s.StatObject(ctx, rec, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Key.Instance).To(Equal("x"))
			gone("c")
			current("x")
		})

		It("lands the void unlink's replay when a radosgw-shaped writer's replay runs between its log read and its head op", func(ctx SpecContext) {
			put(ctx, "v1")
			rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
			rw.Prepare(ctx)
			rw.HeadWrite(ctx)
			rw.Complete(ctx)
			v := write(ctx, "c")
			after(testDataPool, olhOID, 1, func() {
				// radosgw's writer links between the conditional link's init
				// and its link, and reads its log; its apply waits.
				rw.ReadHead(ctx)
				rw.InitRetrying(ctx)
				rw.Link(ctx)
				rw.ReadLog(ctx)
				after(rookIndexPool, shard, 1, func() {
					// After the conditional link, the next write to the OLH head
					// is the void unlink's init, and the one after it its
					// replay's head op, which radosgw's apply and trim precede.
					writes := 0
					c.BeforeWrite(testDataPool, "", olhOID, func(*fakerados.Object) {
						if writes++; writes == 2 {
							c.BeforeWrite(testDataPool, "", olhOID, nil)
							rw.Apply(ctx)
							rw.Trim(ctx)
						}
					})
				})
			})
			conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")}))
			Expect(rw.applied).To(BeTrue(), "radosgw's apply landed first")
			gone("c")
			Expect(listed()).To(Equal([]string{"rgw", "v1"}))
			current("rgw")
		})

		Describe("the unlink of a void link", func() {
			var v meta.ObjKey
			// betweenThen arms another writer's version between the
			// conditional link's init and its link, which voids it, and fn
			// after its link.
			betweenThen := func(ctx context.Context, fn func()) {
				put(ctx, "v1")
				v = write(ctx, "c")
				after(testDataPool, olhOID, 1, func() {
					put(ctx, "x")
					after(rookIndexPool, shard, 1, fn)
				})
			}

			It("answers 412 once the unlink landed, its replay's failure left to the next update", func(ctx SpecContext) {
				betweenThen(ctx, func() {
					// The next write to the shard is the unlink; the next read
					// after it its replay's log read.
					after(rookIndexPool, shard, 1, func() { c.FailNextRead(rookIndexPool, "", shard, syscall.EIO) })
				})
				conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")}))
				Expect(listed()).To(Equal([]string{"x", "v1"}), "the unlink landed")
				Expect(pending()).NotTo(BeEmpty(), "its replay did not run")
				h, err := s.ReadOLHHeadForTest(ctx, rec, version(""))
				Expect(err).NotTo(HaveOccurred())
				Expect(s.UpdateOLHForTest(ctx, rec, h)).To(Succeed())
				gone("c")
				current("x")
			})

			It("answers its error, not 412, when its outcome is unknown", func(ctx SpecContext) {
				betweenThen(ctx, func() { c.FailNextWrite(rookIndexPool, "", shard, syscall.ETIMEDOUT) })
				err := s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")})
				Expect(err).To(MatchError(radosclient.ErrTimedOut))
				Expect(err).NotTo(MatchError(op.ErrPreconditionFailed))
				Expect(err).To(MatchError(ContainSubstring("outcome unknown")))
				Expect(err).NotTo(MatchError(ContainSubstring("stays linked")))
			})

			It("answers an internal error, not 412, when its init finds the head gone and nothing is unlinked", func(ctx SpecContext) {
				betweenThen(ctx, func() {
					c.BeforeWrite(testDataPool, "", olhOID, func(*fakerados.Object) {
						c.BeforeWrite(testDataPool, "", olhOID, nil)
						c.Remove(testDataPool, "", olhOID)
					})
				})
				err := s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")})
				Expect(err).To(MatchError(op.ErrInternalError))
				Expect(err).NotTo(MatchError(op.ErrPreconditionFailed))
				Expect(err).To(MatchError(ContainSubstring("stays linked")))
				Expect(listed()).To(ContainElement("c"), "nothing was unlinked")
			})
		})

		It("lists the shard a reshard moved the name to when the link's shard is gone", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "c")
			old := shard
			after(testDataPool, olhOID, 1, func() {
				put(ctx, "x")
				after(rookIndexPool, shard, 1, func() {
					// The reshard commits after the link: the name's entries move
					// to the next generation's shard and the old shard goes.
					next := resharded(rec)
					seedShards(c, next)
					shard = indexShardOID(next, "k")
					maps.Copy(c.Object(rookIndexPool, "", shard).Omap, c.Object(rookIndexPool, "", old).Omap)
					seedRecordInstance(c, next)
					c.Remove(rookIndexPool, "", old)
				})
			})
			conditionFailed(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: ifMatch("etag-v1")}))
			Expect(shard).NotTo(Equal(old))
			Expect(c.Object(rookIndexPool, "", old)).To(BeNil())
			gone("c")
			Expect(listed()).To(Equal([]string{"x", "v1"}))
			current("x")
		})

		It("is not made for a null delete marker, whose link replaced the null version", func(ctx SpecContext) {
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
			after(testDataPool, olhOID, 1, func() { put(ctx, "x") })
			marker := driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}, Cond: ifMatch("etag-")}
			Expect(s.SetOLHForTest(ctx, rec, version(""), marker)).To(Succeed(),
				"an unlink of the marker would replay the null data's removal its link logged")
			Expect(listed()).To(Equal([]string{"", "x"}))
			current("")
			Expect(olhHead().Data).To(BeEmpty(), "the marker's replay removed the null data")
		})
	})

	Describe("an expected write tag", func() {
		// null makes k's null version and links it, and returns the write tag
		// its head then holds.
		null := func(ctx context.Context) []byte {
			GinkgoHelper()
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
			return slices.Clone(olhHead().Xattrs[meta.AttrIDTag])
		}
		marker := func(expectTag []byte) driver.OLHLink {
			return driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}, ExpectTag: expectTag}
		}
		// idTagCompare is the index of init's compare of user.rgw.idtag with
		// tag among w's steps, -1 when it has none.
		idTagCompare := func(w fakerados.RecordedWrite, tag []byte) int {
			return slices.IndexFunc(w.Steps(), func(st radosclient.Step) bool {
				x, ok := st.(*radosclient.CmpXattrStep)
				return ok && x.Name == meta.AttrIDTag && string(x.Value) == string(tag)
			})
		}

		Describe("over a plain null object, whose head init gives a fresh write tag", func() {
			BeforeEach(func(ctx SpecContext) { write(ctx, "") })

			It("removes the null data in the replay", func(ctx SpecContext) {
				Expect(s.SetOLHForTest(ctx, rec, version(""), marker([]byte(versionWriteTag(version("")))))).To(Succeed())
				settle(s)
				Expect(olhHead().Data).To(BeEmpty(), "the replay removed the null data under the tag init wrote")
				Expect(listed()).To(Equal([]string{""}), "the null delete marker")
				current("")
			})

			It("still refuses the removal when the head is written again after the init", func(ctx SpecContext) {
				after(testDataPool, olhOID, 1, func() { olhHead().Xattrs[meta.AttrIDTag] = []byte("rewritten") })
				err := s.SetOLHForTest(ctx, rec, version(""), marker([]byte(versionWriteTag(version("")))))
				Expect(err).To(MatchError(driver.ErrRemovalRefused))
				Expect(olhHead().Data).To(Equal([]byte("data-")), "the null data stays")
			})
		})

		It("refuses a mismatch before the init with nothing written", func(ctx SpecContext) {
			null(ctx)
			c.ResetCounters()
			err := s.SetOLHForTest(ctx, rec, version(""), marker([]byte("another-tag")))
			Expect(err).To(MatchError(op.ErrConcurrentModification))
			Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero())
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
		})

		It("compares it in the init ahead of every set, and refuses on the retry after a write that landed before the init", func(ctx SpecContext) {
			tag := null(ctx)
			c.ResetCounters()
			c.BeforeWrite(testDataPool, "", olhOID, func(o *fakerados.Object) {
				c.BeforeWrite(testDataPool, "", olhOID, nil)
				o.Xattrs[meta.AttrIDTag] = []byte("rewritten")
			})
			err := s.SetOLHForTest(ctx, rec, version(""), marker(tag))
			Expect(err).To(MatchError(op.ErrConcurrentModification))
			writes := c.WritesTo(testDataPool, "", olhOID)
			Expect(writes).To(HaveLen(1), "the init, which the compare refused")
			at := idTagCompare(writes[0], tag)
			Expect(at).To(BeNumerically(">=", 0), "the init compares user.rgw.idtag")
			for i, st := range writes[0].Steps() {
				if _, ok := st.(*radosclient.SetXattrStep); ok {
					Expect(i).To(BeNumerically(">", at), "the compare comes ahead of every set")
				}
			}
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "nothing linked")
			Expect(pending()).To(BeEmpty())
		})

		Describe("on a missing head", func() {
			It("inits an empty one through the exclusive create, with no compare", func(ctx SpecContext) {
				Expect(s.SetOLHForTest(ctx, rec, version("m"), driver.OLHLink{DeleteMarker: true, ExpectTag: []byte{}})).To(Succeed())
				init := c.WritesTo(testDataPool, "", olhOID)[0]
				Expect(init.Steps()).To(ContainElement(BeAssignableToTypeOf(&radosclient.CreateStep{})))
				Expect(idTagCompare(init, nil)).To(Equal(-1))
				Expect(listed()).To(Equal([]string{"m"}))
			})

			It("refuses an empty one on the retry when another writer created the head first", func(ctx SpecContext) {
				rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
				c.BeforeWrite(testDataPool, "", olhOID, func(*fakerados.Object) {
					c.BeforeWrite(testDataPool, "", olhOID, nil)
					rw.ReadHead(ctx)
					Expect(rw.Init(ctx)).To(BeTrue())
				})
				err := s.SetOLHForTest(ctx, rec, version("m"), driver.OLHLink{DeleteMarker: true, ExpectTag: []byte{}})
				Expect(err).To(MatchError(op.ErrConcurrentModification))
				Expect(listed()).To(BeEmpty())
				Expect(pending()).To(ConsistOf(meta.AttrOLHPendingPrefix+rw.opTag), "the other writer's alone")
			})

			It("refuses a non-empty one with nothing written", func(ctx SpecContext) {
				c.ResetCounters()
				err := s.SetOLHForTest(ctx, rec, version("m"), driver.OLHLink{DeleteMarker: true, ExpectTag: []byte("a-tag")})
				Expect(err).To(MatchError(op.ErrConcurrentModification))
				Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero())
				Expect(olhHead()).To(BeNil())
			})
		})

		It("guards an unlink's removal alone, its init comparing no write tag", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			c.ResetCounters()
			err := s.UnlinkObjInstanceForTest(ctx, rec, version("v1"), driver.OLHUnlink{ExpectTag: []byte("another-tag")})
			Expect(err).To(MatchError(driver.ErrRemovalRefused))
			init := c.WritesTo(testDataPool, "", olhOID)[0]
			Expect(slices.ContainsFunc(init.Steps(), func(st radosclient.Step) bool {
				x, ok := st.(*radosclient.CmpXattrStep)
				return ok && x.Name == meta.AttrIDTag
			})).To(BeFalse(), "the unlink's init: %v", init.Steps())
			Expect(versionHead("v1")).NotTo(BeNil(), "the removal was refused")
		})
	})
})
