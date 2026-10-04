package driver_test

import (
	"bytes"
	"context"
	"sync"
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
		Expect(sent.all()).To(HaveLen(2), "the retry was sent once and refused: process() logs and moves on, rgw_rados.cc:925-929 at v19.2.6")
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
