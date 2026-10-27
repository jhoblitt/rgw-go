package driver_test

import (
	"bytes"
	"context"
	"fmt"
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

// syncBuffer is a log sink the completion goroutines write while a spec
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// completeOps records the bucket_complete_op requests the pools of a
// hookCluster send, in order.
type completeOps struct {
	mu  sync.Mutex
	ops []rgwcls.CompleteOp
}

func (r *completeOps) record(_ string, w *radosclient.WriteOp) {
	for _, st := range w.Steps() {
		if e, ok := st.(*radosclient.ExecStep); ok && e.Method == "bucket_complete_op" {
			r.mu.Lock()
			r.ops = append(r.ops, rgwcls.DecodeCompleteOp(denc.NewDecoder(e.In)))
			r.mu.Unlock()
		}
	}
}

func (r *completeOps) all() []rgwcls.CompleteOp {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rgwcls.CompleteOp(nil), r.ops...)
}

// pollCluster is a cluster whose pools count the get_bucket_resharding reads
// they send, the polls of a wait for a reshard.
type pollCluster struct {
	radosclient.Cluster
	polls atomic.Int64
}

func (c *pollCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return &pollPool{Pool: p, polls: &c.polls}, nil
}

type pollPool struct {
	radosclient.Pool
	polls *atomic.Int64
}

func (p *pollPool) Read(ctx context.Context, oid string, rop *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	if callsMethod(rop.Steps(), "get_bucket_resharding") {
		p.polls.Add(1)
	}
	return p.Pool.Read(ctx, oid, rop, flags)
}

// runStore starts s's workers until the returned stop is called, and stop
// reports what Run returned.
func runStore(ctx context.Context, s *driver.Store) (stop func() error) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Run(runCtx) }()
	return func() error {
		cancel()
		var err error
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(&err))
		return err
	}
}

// bucketShardOIDs is every index shard object of rec's current layout.
func bucketShardOIDs(rec *op.BucketRecord) []string {
	var oids []string
	for i := range rec.Info.Layout.Current.Layout.Normal.NumShards {
		oids = append(oids, rec.Info.IndexShardOID(rec.Info.Layout.Current, i))
	}
	return oids
}

var _ = Describe("the completion manager", func() {
	const bucketID = "zone-ceph-objectstore.4156.1"
	var (
		c    *fakerados.Cluster
		sent *completeOps
		s    *driver.Store
		rec  *op.BucketRecord
		key  meta.ObjKey
		oid  string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newIndexCluster()
		key = meta.ObjKey{Name: "k"}
		sent = &completeOps{}
		var err error
		s, err = driver.Open(ctx, &hookCluster{Cluster: c, afterWrite: sent.record}, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		s.SetReshardWaitForTest(20 * time.Millisecond)
		rec = testBucket(bucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		oid = indexShardOID(rec, "k")
	})
	// run starts the Store's workers until the returned stop is called, and
	// stop reports what Run returned.
	run := func(ctx context.Context) (stop func() error) {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- s.Run(runCtx) }()
		return func() error {
			cancel()
			var err error
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(&err))
			return err
		}
	}
	exists := func(oid, name string) func() bool {
		return func() bool { en, ok := c.Entry(rookIndexPool, "", oid, name); return ok && en.Exists }
	}

	It("retries a busy-resharding complete from the worker once the shard is free again", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		stop := run(ctx)

		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(s.PendingCompletionsForTest).Should(Equal(1), "queued for the retry worker")
		Eventually(func() int { return c.Reads(rookIndexPool, "", oid) }).WithTimeout(5*time.Second).Should(BeNumerically(">=", 1), "the worker waits out the reshard")
		Consistently(exists(oid, "k")).WithTimeout(30 * time.Millisecond).Should(BeFalse())
		Expect(s.PendingCompletionsForTest()).To(Equal(1), "a retry counts until it is done")

		setReshardStatus(ctx, c, oid, rgwcls.ReshardNone)
		Eventually(exists(oid, "k")).WithTimeout(5 * time.Second).Should(BeTrue())
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		Expect(stop()).To(Succeed())
	})

	// completeSyncLater sends y's awaited completion in a goroutine and returns
	// its answer.
	completeSyncLater := func(ctx context.Context, y *driver.IndexOp) chan error {
		done := make(chan error, 1)
		go func() {
			done <- y.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 2}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		}()
		return done
	}

	It("holds a later index call to the shard behind the retry of a completion a reshard refused, which the reshard leaves pending", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() []rgwcls.CompleteOp { return sent.all() }).Should(HaveLen(1), "refused, and queued for the retry worker, which is not running yet")
		setReshardStatus(ctx, c, oid, rgwcls.ReshardNone)

		y := s.NewIndexOpForTest(rec, key, "other")
		Expect(y.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		done := completeSyncLater(ctx, y)
		Consistently(done).WithTimeout(200*time.Millisecond).ShouldNot(Receive(), "the call waits for the refused completion's retry")
		stop := run(ctx)
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(Succeed()))
		ops := sent.all()
		Expect(ops).To(HaveLen(3))
		Expect([]string{ops[1].Tag, ops[2].Tag}).To(Equal([]string{"tag", "other"}), "the retry lands before the later call")
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		Expect(stop()).To(Succeed())
	})

	// nameOnShard is the first of o0, o1, ... whose shard of rec is oid when
	// same, and another when not.
	nameOnShard := func(oid string, same bool) string {
		for i := 0; ; i++ {
			if name := fmt.Sprintf("o%d", i); (indexShardOID(rec, name) == oid) == same {
				return name
			}
		}
	}

	It("holds a later index call to the same name, on the shard the reshard moves it to, behind that retry, and lets it go once Run stops abandoning the retry", func(ctx SpecContext) {
		s.SetReshardWaitForTest(time.Hour)
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		stop := run(ctx)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() int { return c.Reads(rookIndexPool, "", oid) }).WithTimeout(5*time.Second).Should(BeNumerically(">=", 1), "the worker's retry waits out the reshard")

		next := resharded(rec)
		seedShards(c, next)
		Expect(indexShardOID(next, "k")).NotTo(Equal(oid))
		y := s.NewIndexOpForTest(next, key, "other")
		Expect(y.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		done := completeSyncLater(ctx, y)
		Consistently(done).WithTimeout(200*time.Millisecond).ShouldNot(Receive(), "the reshard moves the entry by its name, so a call to that name waits on any shard")
		Expect(stop()).To(Succeed())
		Eventually(done).WithTimeout(5*time.Second).Should(Receive(Succeed()), "the abandoned retry no longer holds it")
	})

	It("lets a later index call to another name of the bucket go while a refused completion's retry waits out the reshard", func(ctx SpecContext) {
		s.SetReshardWaitForTest(time.Hour)
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		stop := run(ctx)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() int { return c.Reads(rookIndexPool, "", oid) }).WithTimeout(5*time.Second).Should(BeNumerically(">=", 1), "the worker's retry waits out the reshard")

		y := s.NewIndexOpForTest(rec, meta.ObjKey{Name: nameOnShard(oid, false)}, "other")
		Expect(y.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Eventually(completeSyncLater(ctx, y)).WithTimeout(5*time.Second).Should(Receive(Succeed()), "the retried completion's pending op is on k's entry, which no other name's call can take")
		Expect(exists(oid, "k")()).To(BeFalse(), "the retry still waits out the reshard")
		Expect(stop()).To(Succeed())
	})

	It("holds a later index call behind the completions in flight for its name alone, whatever the shard", func(ctx SpecContext) {
		ac := &aroundCluster{Cluster: c}
		st, err := driver.Open(ctx, ac, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		x := st.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		y := st.NewIndexOpForTest(rec, meta.ObjKey{Name: nameOnShard(oid, true)}, "other")
		Expect(y.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		next := resharded(rec)
		seedShards(c, next)
		z := st.NewIndexOpForTest(next, key, "moved")
		Expect(z.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())

		arrived, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		DeferCleanup(release)
		ac.set(onFirst("bucket_complete_op", func(send sendFunc) (uint64, error) {
			close(arrived)
			<-held
			return send()
		}))
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(arrived).WithTimeout(5 * time.Second).Should(BeClosed())
		Eventually(completeSyncLater(ctx, y)).WithTimeout(5*time.Second).Should(Receive(Succeed()), "another name's entry, though on k's shard")
		moved := completeSyncLater(ctx, z)
		Consistently(moved).WithTimeout(200*time.Millisecond).ShouldNot(Receive(), "k's entry, on the shard a reshard moves it to")
		release()
		Eventually(moved).WithTimeout(5 * time.Second).Should(Receive(Succeed()))
		Eventually(st.PendingCompletionsForTest).Should(BeZero())
	})

	It("queues a refused completion once while an identical one waits for its retry, and holds a call waiting on either behind that one retry", func(ctx SpecContext) {
		ac := &aroundCluster{Cluster: c}
		st, err := driver.Open(ctx, ac, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		x := st.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		y := st.NewIndexOpForTest(rec, key, "other")
		Expect(y.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)

		arrived, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		DeferCleanup(release)
		var holding atomic.Bool
		ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
			if callsMethod(w.Steps(), "bucket_complete_op") && holding.CompareAndSwap(false, true) {
				close(arrived)
				<-held
			}
			v, err := send()
			sent.record("", w)
			return v, err
		})
		ver, m := rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain}
		x.Complete(ver, m)
		Eventually(arrived).WithTimeout(5 * time.Second).Should(BeClosed())
		done := completeSyncLater(ctx, y)
		Consistently(done).WithTimeout(100*time.Millisecond).ShouldNot(Receive(), "the call waits on the first send, held")
		x.Complete(ver, m)
		Eventually(st.QueuedCompletionsForTest).WithTimeout(5*time.Second).Should(Equal(1), "the second, refused and queued")
		release()
		Eventually(func() int { return len(sent.all()) }).WithTimeout(5*time.Second).Should(Equal(2), "the first, refused")
		Eventually(st.PendingCompletionsForTest).WithTimeout(5*time.Second).Should(Equal(1), "the first is not queued again")
		Consistently(done).WithTimeout(100*time.Millisecond).ShouldNot(Receive(), "the call waiting on the first follows it into the queued retry")

		setReshardStatus(ctx, c, oid, rgwcls.ReshardNone)
		stop := runStore(ctx, st)
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(Succeed()))
		ops := sent.all()
		Expect(ops).To(HaveLen(4), "two refused first sends, one retry, and the call")
		Expect([]string{ops[2].Tag, ops[3].Tag}).To(Equal([]string{"tag", "other"}), "the retry lands before the call")
		Eventually(st.PendingCompletionsForTest).Should(BeZero())
		Expect(stop()).To(Succeed())
	})

	// refusedSends counts the completions tagged tag that reached a shard.
	refusedSends := func(tag string) func() int {
		return func() int {
			n := 0
			for _, c := range sent.all() {
				if c.Tag == tag {
					n++
				}
			}
			return n
		}
	}

	// The call that waits shares the refused completion's shard in both specs
	// below, so it waits whether it reads the manager's state before the
	// refused send hands over to its retry or after.
	It("lets a later call to one bucket go once that bucket's refused completion is retried, while another bucket's retry still waits out its reshard", func(ctx SpecContext) {
		s.SetReshardWaitForTest(time.Hour)
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		stop := run(ctx)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() int { return c.Reads(rookIndexPool, "", oid) }).WithTimeout(5*time.Second).Should(BeNumerically(">=", 1), "bucket A's retry waits out its reshard")

		recB := testBucket("zone-ceph-objectstore.4156.2", 11)
		seedShards(c, recB)
		seedRecordInstance(c, recB)
		oidB := indexShardOID(recB, "k")
		xb := s.NewIndexOpForTest(recB, key, "tag-b")
		Expect(xb.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		yb := s.NewIndexOpForTest(recB, key, "other-b")
		Expect(yb.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		c.FailNextWrite(rookIndexPool, "", oidB, syscall.Errno(2300))
		xb.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(refusedSends("tag-b")).WithTimeout(5*time.Second).Should(BeNumerically(">=", 1), "bucket B's completion refused, its reshard already over")

		done := completeSyncLater(ctx, yb)
		Eventually(done).WithTimeout(5*time.Second).Should(Receive(Succeed()), "B's call answers once B's retry lands")
		Expect(exists(oidB, "k")()).To(BeTrue(), "B's retry landed")
		Expect(exists(oid, "k")()).To(BeFalse(), "A's retry is still waiting out A's reshard")
		Expect(stop()).To(Succeed())
	})

	It("lets a call waiting on a refused completion's retry go when Run stops, whether it stops before the retry starts or as the retry runs", func(ctx SpecContext) {
		for round := range 20 {
			st, err := driver.Open(ctx, &hookCluster{Cluster: c, afterWrite: sent.record}, conf(nil), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			tag := fmt.Sprintf("tag-%d", round)
			x := st.NewIndexOpForTest(rec, key, tag)
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			y := st.NewIndexOpForTest(rec, key, fmt.Sprintf("other-%d", round))
			Expect(y.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			c.FailNextWrite(rookIndexPool, "", oid, syscall.Errno(2300))
			x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: uint64(round) + 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
			Eventually(st.QueuedCompletionsForTest).WithTimeout(5*time.Second).Should(Equal(1), "round %d: refused, and queued for a worker not yet running", round)
			Expect(refusedSends(tag)()).To(Equal(1), "round %d: sent once", round)
			done := completeSyncLater(ctx, y)

			// Run's select takes either its stop or its wake first: the queued
			// retry is released as Run stops, or started and then canceled.
			runCtx, cancel := context.WithCancel(ctx)
			cancel()
			Expect(st.Run(runCtx)).To(Succeed())
			Eventually(done).WithTimeout(5*time.Second).Should(Receive(Succeed()), "round %d: the call is released", round)
			Expect(st.PendingCompletionsForTest()).To(BeZero(), "round %d", round)
			Expect(st.Close()).To(Succeed())
		}
	})

	It("waits a bucket's reshard out with one poller for all the completions the reshard refused, those refused while it waits included, and sends every one once it ends", func(ctx SpecContext) {
		const n = 20
		wait := 50 * time.Millisecond
		pc := &pollCluster{Cluster: &hookCluster{Cluster: c, afterWrite: sent.record}}
		st, err := driver.Open(ctx, pc, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		st.SetReshardWaitForTest(wait)
		names := make([]string, n)
		xs := make([]*driver.IndexOp, n)
		for i := range names {
			names[i] = fmt.Sprintf("k%d", i)
			xs[i] = st.NewIndexOpForTest(rec, meta.ObjKey{Name: names[i]}, "tag-"+names[i])
			Expect(xs[i].Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		}
		for _, o := range bucketShardOIDs(rec) {
			setReshardStatus(ctx, c, o, rgwcls.ReshardInProgress)
		}
		stop := runStore(ctx, st)
		start := time.Now()
		xs[0].Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(pc.polls.Load).WithTimeout(5*time.Second).Should(BeNumerically(">=", 1), "the bucket's retrier waits the reshard out")
		for _, x := range xs[1:] {
			x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		}
		Eventually(func() int { return len(sent.all()) }).WithTimeout(5*time.Second).Should(BeNumerically(">=", n), "every first send refused")
		// One poller reads the status once per wait, and once more without a
		// wait when a pass of the guard ends.
		Consistently(func() int64 { return pc.polls.Load() - int64(time.Since(start)/wait) }).
			WithTimeout(400*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeNumerically("<=", 3), "one poller for the bucket")

		for _, o := range bucketShardOIDs(rec) {
			setReshardStatus(ctx, c, o, rgwcls.ReshardNone)
		}
		for _, name := range names {
			Eventually(exists(indexShardOID(rec, name), name)).WithTimeout(5*time.Second).Should(BeTrue(), "%s's retry landed", name)
		}
		Eventually(st.PendingCompletionsForTest).WithTimeout(5 * time.Second).Should(BeZero())
		Expect(stop()).To(Succeed())
	})

	It("reissues a refused completion as radosgw's retry thread does, against the fresh instance's shard and without the object locator", func(ctx SpecContext) {
		u := meta.ObjKey{Name: "_u"}
		uOID := indexShardOID(rec, "_u")
		x := s.NewIndexOpForTest(rec, u, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, uOID, rgwcls.ReshardInProgress)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() []rgwcls.CompleteOp { return sent.all() }).Should(HaveLen(1))
		Expect(s.PendingCompletionsForTest()).To(Equal(1))

		// The reshard finishes: the instance names generation 1, whose
		// shard holds the entry the reshard copied.
		next := nextGeneration(c, rec)
		nextOID := indexShardOID(next, "_u")
		c.Object(rookIndexPool, "", nextOID).Omap["__u"] = c.Object(rookIndexPool, "", uOID).Omap["__u"]
		stop := run(ctx)
		Eventually(exists(nextOID, "__u")).WithTimeout(5 * time.Second).Should(BeTrue())
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		Expect(stop()).To(Succeed())

		ops := sent.all()
		Expect(ops).To(HaveLen(2))
		Expect(ops[0].Locator).To(Equal("_u"), "cls_obj_complete_op passes obj.key.get_loc(), rgw_rados.cc:9512-9513 at v19.2.6")
		retried := ops[0]
		retried.Locator = ""
		Expect(ops[1]).To(Equal(retried), "process() leaves cls_rgw_bucket_complete_op's locator at its default, rgw_rados.cc:918-919 at v19.2.6")
	})

	It("retries a queued completion whose shard a reshard removed on the shard the bucket now names", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() []rgwcls.CompleteOp { return sent.all() }).Should(HaveLen(1))

		// The worker reads the instance, which still names the old shard, and
		// the reshard commits before its completion reaches that shard.
		next := commitReshardBefore(c, rec, "k")
		nextOID := indexShardOID(next, "k")
		stop := run(ctx)
		Eventually(exists(nextOID, "k")).WithTimeout(5 * time.Second).Should(BeTrue())
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		Expect(stop()).To(Succeed())
		Expect(sent.all()).To(HaveLen(3), "the refused first attempt, the retry the missing shard refused, and the retry that landed")
		Expect(c.Reads(rookIndexPool, "", oid)).To(BeZero(), "the guard re-read the bucket without waiting: rgw_rados.cc:905, :7929-7933 at v19.2.7")
	})

	It("drops a complete the class refuses for any other reason, and logs it", func(ctx SpecContext) {
		var buf syncBuffer
		DeferCleanup(driver.CaptureLog(&buf))
		x := s.NewIndexOpForTest(rec, key, "never-prepared")
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() []rgwcls.CompleteOp { return sent.all() }).Should(HaveLen(1))
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		Eventually(buf.String).Should(ContainSubstring("bucket index completion failed"))
		_, ok := c.Entry(rookIndexPool, "", oid, "k")
		Expect(ok).To(BeFalse(), "EINVAL: the tag was never pending (cls_rgw.cc:1069-1078)")
	})

	It("drops a queued completion whose bucket instance cannot be read again, and logs it", func(ctx SpecContext) {
		var buf syncBuffer
		DeferCleanup(driver.CaptureLog(&buf))
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		c.Remove(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() []rgwcls.CompleteOp { return sent.all() }).Should(HaveLen(1))
		stop := run(ctx)
		Eventually(s.PendingCompletionsForTest).WithTimeout(5 * time.Second).Should(BeZero())
		Expect(buf.String()).To(ContainSubstring("bucket index completion dropped"))
		Expect(sent.all()).To(HaveLen(1), "process() continues past a BucketShard it cannot init, rgw_rados.cc:898-903 at v19.2.6")
		en, _ := c.Entry(rookIndexPool, "", oid, "k")
		Expect(en.PendingMap).To(HaveLen(1), "left pending for listing to reconcile")
		Expect(stop()).To(Succeed())
	})

	It("drops a retried completion the shard refuses for another reason, and logs it", func(ctx SpecContext) {
		var buf syncBuffer
		DeferCleanup(driver.CaptureLog(&buf))
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() []rgwcls.CompleteOp { return sent.all() }).Should(HaveLen(1))
		c.Remove(rookIndexPool, "", oid)
		stop := run(ctx)
		Eventually(s.PendingCompletionsForTest).WithTimeout(5 * time.Second).Should(BeZero())
		Expect(buf.String()).To(ContainSubstring("bucket index completion failed after a reshard"))
		Expect(sent.all()).To(HaveLen(2), "the retry was sent once: the bucket read again names the same missing shard, and process() logs and moves on, rgw_rados.cc:925-929 at v19.2.6")
		Expect(stop()).To(Succeed())
	})

	It("stops with Run, abandoning the retry it is waiting on", func(ctx SpecContext) {
		s.SetReshardWaitForTest(time.Hour)
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
		stop := run(ctx)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(func() int { return c.Reads(rookIndexPool, "", oid) }).WithTimeout(5 * time.Second).Should(BeNumerically(">=", 1))
		Expect(stop()).To(Succeed())
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
	})

	It("sends nothing once Run has stopped", func(ctx SpecContext) {
		var buf syncBuffer
		DeferCleanup(driver.CaptureLog(&buf))
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		stop := run(ctx)
		Expect(stop()).To(Succeed())
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Expect(s.PendingCompletionsForTest()).To(BeZero())
		Expect(sent.all()).To(BeEmpty())
		Expect(buf.String()).To(ContainSubstring("bucket index completion dropped"))
	})
})
