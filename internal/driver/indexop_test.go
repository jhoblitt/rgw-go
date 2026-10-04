package driver_test

import (
	"context"
	"strings"
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

// rookIndexPool is the index pool of the zone seedRookZone seeds for the
// store "ceph-objectstore".
const rookIndexPool = "ceph-objectstore.rgw.buckets.index"

// testBucket is a Rook-shaped bucket record "plain" with the given id and
// shard count at index generation 0.
func testBucket(id string, shards uint32) *op.BucketRecord {
	info := meta.NewBucketInfo()
	info.Bucket = meta.BucketID{Name: "plain", Marker: id, ID: id}
	info.Owner = meta.UserOwner(meta.UserID{ID: "alice"})
	info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
	info.Layout = meta.NewBucketLayout()
	info.Layout.Current.Layout.Normal.NumShards = shards
	return &op.BucketRecord{Info: info}
}

// seedShards creates the shards of rec's current index generation with
// empty headers.
func seedShards(c *fakerados.Cluster, rec *op.BucketRecord) {
	gen := rec.Info.Layout.Current
	for i := range max(gen.Layout.Normal.NumShards, 1) {
		seedShardHeader(c, rookIndexPool, rec.Info.IndexShardOID(gen, i), rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{}})
	}
}

// seedRecordInstance stores rec's bucket instance in the domain root, where
// a reshard's refresh reads it.
func seedRecordInstance(c *fakerados.Cluster, rec *op.BucketRecord) {
	seedInstance(c, rec.Info, nil, meta.ObjVersion{Ver: 1, Tag: "instance"})
}

// indexShardOID is the oid of the current generation's shard that holds name.
func indexShardOID(rec *op.BucketRecord, name string) string {
	sh, _ := meta.IndexShard(name, rec.Info.Layout.Current.Layout.Normal.NumShards)
	return rec.Info.IndexShardOID(rec.Info.Layout.Current, sh)
}

// nextGeneration is rec resharded in place: the same instance at index
// generation 1, its shards seeded and its instance stored.
func nextGeneration(c *fakerados.Cluster, rec *op.BucketRecord) *op.BucketRecord {
	next := testBucket(rec.Info.Bucket.ID, rec.Info.Layout.Current.Layout.Normal.NumShards)
	next.Info.Layout.Current.Gen = 1
	seedShards(c, next)
	seedRecordInstance(c, next)
	return next
}

// setReshardStatus sets a shard header's reshard status with a write op, so
// the change takes the cluster's lock while the driver runs.
func setReshardStatus(ctx context.Context, c *fakerados.Cluster, oid string, status uint8) {
	GinkgoHelper()
	p, err := c.Pool(ctx, rookIndexPool, "")
	Expect(err).NotTo(HaveOccurred())
	w := radosclient.NewWriteOp()
	rgwcls.SetBucketResharding(w, rgwcls.InstanceEntry{ReshardStatus: status}, denc.Squid)
	_, err = p.Write(ctx, oid, w, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred())
}

// execIn decodes the input of a write's step i, an exec of method.
func execIn[T any](w fakerados.RecordedWrite, i int, method string, dec func(*denc.Decoder) T) T {
	GinkgoHelper()
	steps := w.Steps()
	Expect(len(steps)).To(BeNumerically(">", i))
	step, ok := steps[i].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "step %d is %T", i, steps[i])
	Expect(step.Method).To(Equal(method))
	d := denc.NewDecoder(step.In)
	v := dec(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return v
}

// newIndexCluster is a cluster with the classes the index specs need and
// the zone seedRookZone seeds.
func newIndexCluster() *fakerados.Cluster {
	c := fakerados.New()
	c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
	c.RegisterClass("user", fakerados.UserClass(), fakerados.UserWriteMethods...)
	c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
	seedRookZone(c, "ceph-objectstore", true)
	return c
}

var _ = Describe("indexOp", func() {
	const (
		bucketID = "zone-ceph-objectstore.4156.1"
		tag      = "tx0000000000000000000001-0068d7a1b2-4155-ceph-objectstore"
	)
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
		key meta.ObjKey
		oid string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newIndexCluster()
		key = meta.ObjKey{Name: "k"}
		var err error
		s, err = driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		s.SetReshardWaitForTest(time.Millisecond)
		rec = testBucket(bucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		oid = indexShardOID(rec, "k")
	})
	trace := func() []string { return []string{s.Zone().ID + ":plain:" + bucketID} }

	It("prepares on the shard meta.IndexShard names, guarded, with Squid's zones_trace", func(ctx SpecContext) {
		Expect(s.NewIndexOpForTest(rec, key, tag).Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(1))
		w := c.LastWrite(rookIndexPool, "", oid)
		Expect(w.Steps()).To(HaveLen(3))
		Expect(w.Steps()[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		Expect(execIn(w, 1, "guard_bucket_resharding", rgwcls.DecodeGuardOp).RetErr).To(BeEquivalentTo(-2300))
		Expect(execIn(w, 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)).To(Equal(rgwcls.PrepareOp{
			Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: "k"}, Tag: tag, ZonesTrace: trace(),
		}), "rgw_rados.cc:9456-9479 at v19.2.6; log_op false in a single zone")
		Expect(trace()).To(Equal([]string{"zone-ceph-objectstore:plain:" + bucketID}))
		en, ok := c.Entry(rookIndexPool, "", oid, "k")
		Expect(ok).To(BeTrue())
		Expect(en.PendingMap).To(HaveLen(1))
		Expect(en.Exists).To(BeFalse())
	})

	It("sends neither log_op nor zones_trace in a prepare on Tentacle, and the zone trace in its completion", func(ctx SpecContext) {
		t := denc.Tentacle
		s2, err := driver.Open(ctx, c, conf(nil), driver.Options{Release: &t})
		Expect(err).NotTo(HaveOccurred())
		x := s2.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		p := execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect(p.ZonesTrace).To(BeEmpty(), "v20.2.4 cls_rgw_client.cc:164-175")
		Expect(p.LogOp).To(BeFalse())
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{})
		Eventually(func() int { return c.Writes(rookIndexPool, "", oid) }).Should(Equal(2))
		comp := execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.ZonesTrace).To(Equal(trace()), "v20.2.4 rgw_rados.cc:10433-10437")
		Expect(comp.LogOp).To(BeFalse(), "need_to_log_data is false in a single zone")
	})

	It("escapes and locates a key that starts with an underscore", func(ctx SpecContext) {
		Expect(s.NewIndexOpForTest(rec, meta.ObjKey{Name: "_u"}, "tag").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		p := execIn(c.LastWrite(rookIndexPool, "", indexShardOID(rec, "_u")), 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect(p.Key.Name).To(Equal("__u"), "get_index_key_name escapes the leading underscore")
		Expect(p.Locator).To(Equal("_u"), "get_loc keeps the name as the locator")
	})

	It("uses the bare .dir.<id> object for an unsharded index", func(ctx SpecContext) {
		flat := testBucket("zone-ceph-objectstore.4156.2", 0)
		seedShards(c, flat)
		Expect(s.NewIndexOpForTest(flat, key, "tag").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(c.Writes(rookIndexPool, "", ".dir.zone-ceph-objectstore.4156.2")).To(Equal(1))
	})

	It("does nothing at all for an indexless bucket", func(ctx SpecContext) {
		blind := testBucket("zone-ceph-objectstore.4156.3", 0)
		blind.Info.Layout.Current.Layout.Type = meta.IndexIndexless
		x := s.NewIndexOpForTest(blind, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(x.PreparedForTest()).To(BeFalse(), "UpdateIndex::prepare returns before setting prepared, rgw_rados.cc:7082-7084 at v19.2.6")
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{})
		x.CompleteDel(rgwcls.EntryVer{Pool: 7, Epoch: 1}, time.Time{})
		x.Cancel()
		Expect(s.PendingCompletionsForTest()).To(BeZero(), "UpdateIndex::blind")
		Expect(c.Writes(rookIndexPool, "", ".dir.zone-ceph-objectstore.4156.3")).To(BeZero())
	})

	It("tags an op the request gave no tag with append_rand_alpha's 32 characters", func(ctx SpecContext) {
		s.SetRandForTest(func(n int) string { return strings.Repeat("x", n) })
		Expect(s.NewIndexOpForTest(rec, key, "").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		p := execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect(p.Tag).To(Equal("_"+strings.Repeat("x", 31)), "rgw_common.h:1621-1628 at v19.2.6")
	})

	It("completes an ADD off the request path with the entry radosgw writes", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		mtime := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		meta := rgwcls.DirEntryMeta{
			Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, Mtime: mtime,
			ETag: "5d41402abc4b2a76b9719d911017c592", Owner: "alice", OwnerDisplayName: "Alice", ContentType: "text/plain",
		}
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 42}, meta)
		Eventually(func() int { return c.Writes(rookIndexPool, "", oid) }).Should(Equal(2))
		w := c.LastWrite(rookIndexPool, "", oid)
		Expect(w.Steps()[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		execIn(w, 1, "guard_bucket_resharding", rgwcls.DecodeGuardOp)
		Expect(execIn(w, 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)).To(Equal(rgwcls.CompleteOp{
			Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: "k"}, Ver: rgwcls.EntryVer{Pool: 7, Epoch: 42}, Meta: meta,
			Tag: "tag", ZonesTrace: trace(),
		}), "rgw_rados.cc:9481-9523 at v19.2.6")
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		en, _ := c.Entry(rookIndexPool, "", oid, "k")
		Expect(en.Exists).To(BeTrue())
		Expect(en.Meta.ETag).To(Equal("5d41402abc4b2a76b9719d911017c592"))
		Expect(en.PendingMap).To(BeEmpty())
		Expect(c.Header(rookIndexPool, "", oid).Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(1))
	})

	It("completes under the category its caller names, as UpdateIndex::complete's argument", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMultiMeta, Size: 3})
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		comp := execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.Meta.Category).To(Equal(rgwcls.CategoryMultiMeta), "rgw_rados.cc:9498-9499 at v19.2.6")
		Expect(c.Header(rookIndexPool, "", oid).Stats[rgwcls.CategoryMultiMeta].NumEntries).To(BeEquivalentTo(1))
	})

	It("completes a DEL with the removed mtime under category None", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpDel)).To(Succeed())
		mtime := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		x.CompleteDel(rgwcls.EntryVer{Pool: 7, Epoch: 43}, mtime)
		Eventually(func() int { return c.Writes(rookIndexPool, "", oid) }).Should(Equal(2))
		comp := execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.Op).To(Equal(rgwcls.OpDel))
		Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: 7, Epoch: 43}))
		Expect(comp.Meta).To(Equal(rgwcls.DirEntryMeta{Category: rgwcls.CategoryNone, Mtime: mtime}), "rgw_rados.cc:9536-9551 at v19.2.6")
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		_, ok := c.Entry(rookIndexPool, "", oid, "k")
		Expect(ok).To(BeFalse(), "a never-completed key is removed by the DEL")
	})

	It("cancels with pool -1 and epoch 0, as cls_obj_complete_cancel does", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		x.Cancel()
		Eventually(func() int { return c.Writes(rookIndexPool, "", oid) }).Should(Equal(2))
		comp := execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.Op).To(Equal(rgwcls.OpCancel))
		Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "rgw_rados.cc:9553-9563 at v19.2.6")
		Expect(comp.Meta).To(Equal(rgwcls.DirEntryMeta{Category: rgwcls.CategoryNone}))
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		_, ok := c.Entry(rookIndexPool, "", oid, "k")
		Expect(ok).To(BeFalse(), "cls_rgw.cc:1097-1108: nothing existed and nothing else is pending")
	})

	It("resets an existing entry's version with its cancel, so a stale completion applies after it (tracker #80894)", func(ctx SpecContext) {
		first := s.NewIndexOpForTest(rec, key, "tag-1")
		Expect(first.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		lost := s.NewIndexOpForTest(rec, key, "tag-2")
		Expect(lost.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		stale := s.NewIndexOpForTest(rec, key, "tag-3")
		Expect(stale.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		first.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 10}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 10, AccountedSize: 10, ETag: "ten"})
		Eventually(func() int { return c.Writes(rookIndexPool, "", oid) }).Should(Equal(4))
		lost.Cancel()
		Eventually(func() int { return c.Writes(rookIndexPool, "", oid) }).Should(Equal(5))
		en, _ := c.Entry(rookIndexPool, "", oid, "k")
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "cls_rgw.cc:1094 copies the cancel's version onto the entry")
		Expect(en.Meta.ETag).To(Equal("ten"))
		stale.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 5}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, ETag: "five"})
		Eventually(func() string { en, _ := c.Entry(rookIndexPool, "", oid, "k"); return en.Meta.ETag }).Should(Equal("five"),
			"the pool comparison fails (cls_rgw.cc:1082-1083), so the older completion applies, as it does behind radosgw")
	})

	It("returns a prepare the shard refuses for another reason as it is", func(ctx SpecContext) {
		c.Remove(rookIndexPool, "", oid)
		err := s.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "the shard must exist: assert_exists")
		Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(1))
	})

	It("marks the op prepared only once a prepare succeeded, as UpdateIndex::prepare does", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.PreparedForTest()).To(BeFalse())
		shard := c.Object(rookIndexPool, "", oid)
		c.Remove(rookIndexPool, "", oid)
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(MatchError(radosclient.ErrNotFound))
		Expect(x.PreparedForTest()).To(BeFalse(), "a failed prepare leaves it unset")
		c.Put(rookIndexPool, "", oid, nil)
		c.Object(rookIndexPool, "", oid).OmapHdr = shard.OmapHdr
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(x.PreparedForTest()).To(BeTrue(), "rgw_rados.cc:7104 at v19.2.6, :7955 at v20.2.4")
	})

	It("answers UnknownError for a bucket without an id, as open_bucket_index_base's EIO", func(ctx SpecContext) {
		rec.Info.Bucket.ID = ""
		err := s.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
		Expect(err).To(MatchError(op.ErrUnknown))
	})

	It("answers UnknownError for an index hash type radosgw does not know, as its ENOTSUP", func(ctx SpecContext) {
		rec.Info.Layout.Current.Layout.Normal.HashType = meta.HashMod + 1
		err := s.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
		Expect(err).To(MatchError(op.ErrUnknown), "svc_bi_rados.cc:269-270 at v19.2.6; ENOTSUP has no S3 row")
		Expect(c.Writes(rookIndexPool, "", oid)).To(BeZero())
	})

	Describe("behind a reshard", func() {
		It("waits out a reshard on prepare and retries once the shard is free", func(ctx SpecContext) {
			s.SetReshardWaitForTest(20 * time.Millisecond)
			setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
			done := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(done)
				Eventually(func() int { return c.Reads(rookIndexPool, "", oid) }).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).
					Should(BeNumerically(">=", 1))
				setReshardStatus(ctx, c, oid, rgwcls.ReshardNone)
			}()
			Expect(s.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			Eventually(done).Should(BeClosed())
			Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(4), "the staging write, the refused prepare, the reshard's end and the prepare that landed")
			Expect(c.Reads(rookIndexPool, "", oid)).To(BeNumerically(">=", 1), "get_bucket_resharding polls")
			en, ok := c.Entry(rookIndexPool, "", oid, "k")
			Expect(ok).To(BeTrue())
			Expect(en.PendingMap).To(HaveLen(1))
		})

		It("gives up after ten busy polls with ErrBusyResharding, which the op layer answers as radosgw's 500", func(ctx SpecContext) {
			s.SetReshardWaitForTest(0)
			setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
			err := s.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
			Expect(err).To(MatchError(radosclient.ErrBusyResharding))
			Expect(op.FromRADOS(err, op.ScopeObject)).To(MatchError(op.ErrUnknown), "ERR_BUSY_RESHARDING has no S3 row (rgw_common.cc)")
			Expect(c.Reads(rookIndexPool, "", oid)).To(Equal(10*10), "10 guard attempts, each followed by 10 polls")
			Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(1 + 10))
		})

		DescribeTable("reads a reshard status as busy or finished as each release does",
			func(ctx SpecContext, release denc.Release, status uint8, busy bool) {
				st, err := driver.Open(ctx, c, conf(nil), driver.Options{Release: &release})
				Expect(err).NotTo(HaveOccurred())
				st.SetReshardWaitForTest(0)
				setReshardStatus(ctx, c, oid, status)
				next := nextGeneration(c, rec)
				nextOID := indexShardOID(next, "k")
				Expect(nextOID).NotTo(Equal(oid))

				err = st.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
				if busy {
					Expect(err).To(MatchError(radosclient.ErrBusyResharding))
					Expect(c.Writes(rookIndexPool, "", nextOID)).To(BeZero())
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(c.Reads(rookIndexPool, "", oid)).To(Equal(1), "one poll")
				Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(1+1), "the staging write and the refused prepare")
				Expect(c.Writes(rookIndexPool, "", nextOID)).To(Equal(1), "the prepare the refreshed instance's shard took")
				_, ok := c.Entry(rookIndexPool, "", nextOID, "k")
				Expect(ok).To(BeTrue())
			},
			Entry("Squid, in progress", denc.Squid, rgwcls.ReshardInProgress, true),
			Entry("Squid, done: resharding_in_progress() is false", denc.Squid, rgwcls.ReshardDone, false),
			Entry("Squid, in log record: resharding_in_progress() is false", denc.Squid, rgwcls.ReshardInLogRecord, false),
			Entry("Tentacle, in progress", denc.Tentacle, rgwcls.ReshardInProgress, true),
			Entry("Tentacle, done: resharding() is true", denc.Tentacle, rgwcls.ReshardDone, true),
			Entry("Tentacle, in log record: resharding() is true", denc.Tentacle, rgwcls.ReshardInLogRecord, true),
		)

		It("ends in ErrBusyResharding when a shard whose status reads as finished keeps refusing the guard", func(ctx SpecContext) {
			s.SetReshardWaitForTest(0)
			setReshardStatus(ctx, c, oid, rgwcls.ReshardDone)
			err := s.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
			Expect(err).To(MatchError(radosclient.ErrBusyResharding))
			Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(1+10), "ten attempts on the same shard")
			Expect(c.Reads(rookIndexPool, "", oid)).To(Equal(10), "one poll after each")
		})

		It("refreshes the instance when the polled shard is gone, and retries on the shard it names", func(ctx SpecContext) {
			setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
			next := nextGeneration(c, rec)
			// The refused prepare is the second write to the old shard; a
			// failed op leaves no trace, so the shard goes once it ran.
			rc := &hookCluster{Cluster: c, afterWrite: func(o string, _ *radosclient.WriteOp) {
				if o == oid && c.Writes(rookIndexPool, "", oid) == 2 {
					c.Remove(rookIndexPool, "", oid)
				}
			}}
			st, err := driver.Open(ctx, rc, conf(nil), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			st.SetReshardWaitForTest(0)
			Expect(st.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			Expect(c.Reads(rookIndexPool, "", oid)).To(Equal(1), "the poll that found no shard")
			_, ok := c.Entry(rookIndexPool, "", indexShardOID(next, "k"), "k")
			Expect(ok).To(BeTrue())
		})

		It("answers UnknownError for a reshard status that does not decode", func(ctx SpecContext) {
			rgw := fakerados.RGWClass()
			c.RegisterClass("rgw", func(call *fakerados.ClassCall) ([]byte, int32) {
				switch call.Method {
				case "guard_bucket_resharding":
					return nil, -rgwcls.ErrBusyResharding
				case "get_bucket_resharding":
					return []byte{1, 1, 9, 0, 0, 0}, 0
				}
				return rgw(call)
			}, fakerados.RGWWriteMethods...)
			err := s.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
			Expect(err).To(MatchError(op.ErrUnknown), "cls_rgw_get_bucket_resharding's EIO")
			Expect(err).To(MatchError(ContainSubstring(oid)))
		})

		It("answers NoSuchKey, as radosgw does, when the bucket is deleted while the write waits out its reshard", func(ctx SpecContext) {
			setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
			// The bucket goes once the refused prepare ran: its instance
			// and its index shards, as RGWRados::delete_bucket removes them.
			rc := &hookCluster{Cluster: c, afterWrite: func(o string, _ *radosclient.WriteOp) {
				if o == oid && c.Writes(rookIndexPool, "", oid) == 2 {
					c.Remove(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())
					c.Remove(rookIndexPool, "", oid)
				}
			}}
			st, err := driver.Open(ctx, rc, conf(nil), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			st.SetReshardWaitForTest(0)
			err = st.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
			Expect(err).To(MatchError(op.ErrNoSuchKey), "fetch_new_bucket_info's -ENOENT, rgw_common.cc:97 at v19.2.6, :98 at v20.2.4")
			Expect(err).NotTo(MatchError(op.ErrNoSuchBucket))
		})

		It("answers NoSuchKey when the bucket is gone although its shard reads as no longer resharding", func(ctx SpecContext) {
			s.SetReshardWaitForTest(0)
			c.Remove(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())
			c.RegisterClass("rgw", func(call *fakerados.ClassCall) ([]byte, int32) {
				if call.Method == "guard_bucket_resharding" {
					return nil, -rgwcls.ErrBusyResharding
				}
				return fakerados.RGWClass()(call)
			}, fakerados.RGWWriteMethods...)
			err := s.NewIndexOpForTest(rec, key, "tag").Prepare(ctx, rgwcls.OpAdd)
			Expect(err).To(MatchError(op.ErrNoSuchKey), "rgw_rados.cc:7837-7844 at v19.2.6, :8779-8786 at v20.2.4")
		})

		It("stops waiting when the request's context ends", func(ctx SpecContext) {
			s.SetReshardWaitForTest(time.Hour)
			setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
			reqCtx, cancel := context.WithCancel(ctx)
			go func() {
				defer GinkgoRecover()
				Eventually(func() int { return c.Reads(rookIndexPool, "", oid) }).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).
					Should(Equal(1))
				cancel()
			}()
			err := s.NewIndexOpForTest(rec, key, "tag").Prepare(reqCtx, rgwcls.OpAdd)
			Expect(err).To(MatchError(context.Canceled))
		})
	})
})

// hookCluster is a fakerados cluster whose pools call afterWrite with each
// write op once it has run.
type hookCluster struct {
	*fakerados.Cluster
	afterWrite func(oid string, op *radosclient.WriteOp)
}

func (c *hookCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return &hookPool{Pool: p, afterWrite: c.afterWrite}, nil
}

type hookPool struct {
	radosclient.Pool
	afterWrite func(oid string, op *radosclient.WriteOp)
}

func (p *hookPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	v, err := p.Pool.Write(ctx, oid, op, flags)
	p.afterWrite(oid, op)
	return v, err
}
