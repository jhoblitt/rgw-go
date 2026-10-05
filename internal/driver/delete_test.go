package driver_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // generate_fake_tag's digest
	"encoding/hex"
	"fmt"
	"slices"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// deleteTag is the tag a delete prepares under, append_rand_alpha's "_" and
// 31 characters, which the PUT fixture's random source fixes.
const deleteTag = "_" + putPrefix

var _ = Describe("DeleteObject", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		mtime   time.Time
		body    []byte
		headOID string
		tail    string
		shard   string
	)
	// setup opens the store at rel and writes k, 6 MiB with one tail, under
	// the write tag tx-put, then forgets the ops that took.
	setup := func(ctx context.Context, rel denc.Release) {
		GinkgoHelper()
		s = openPutStore(ctx, c, rel, nil, mtime)
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(body), op.PutParams{
			Attrs: map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(attrsOwner, "Alice"))},
			Size:  int64(len(body)), Tag: "tx-put",
		})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		c.ResetCounters()
	}
	indexWrites := func() []fakerados.RecordedWrite {
		GinkgoHelper()
		settle(s)
		return c.WritesTo(rookIndexPool, "", shard)
	}
	gcEntries := func() []rgwcls.GCObjInfo {
		return c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-put\x00")))
	}
	BeforeEach(func() {
		// A fraction of a second, which the S3 conditions drop.
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 250_000_000, time.UTC)
		c = newPutCluster()
		clock := mtime
		c.SetClock(func() time.Time { return clock })
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		body = bytes.Repeat([]byte("d"), 6<<20)
		headOID = putBucketID + "_k"
		tail = tailOID(putPrefix, 1)
		shard = indexShardOID(rec, "k")
	})

	It("deletes with radosgw's stat, prepare, guarded remove, complete_del and GC enqueue", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		epoch := c.Object(testDataPool, "", headOID).Version
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
		Expect(c.Reads(testDataPool, "", headOID)).To(Equal(1), "one get_obj_state")
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
		w := c.LastWrite(testDataPool, "", headOID)
		Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry), "ioctx.set_pool_full_try, rgw_rados.cc:5943")
		steps := w.Steps()
		Expect(steps).To(HaveLen(2))
		Expect(steps[0]).To(Equal(&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-put\x00")}),
			"prepare_atomic_modification's guard, rgw_rados.cc:6508-6513")
		Expect(execIn(w, 1, "obj_remove", rgwcls.DecodeObjRemoveOp).KeepAttrPrefixes).To(Equal([]string{"user.rgw.olh."}), "remove_rgw_head_obj")
		_, stamped := w.Mtime()
		Expect(stamped).To(BeFalse())

		writes := indexWrites()
		Expect(writes).To(HaveLen(2), "the prepare and complete_del")
		prep := execIn(writes[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect(prep.Op).To(Equal(rgwcls.OpDel))
		Expect(prep.Tag).To(Equal(deleteTag), "the removal sets no write tag: UpdateIndex::prepare's append_rand_alpha, rgw_rados.cc:7087-7093")
		comp := execIn(writes[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.Op).To(Equal(rgwcls.OpDel))
		Expect(comp.Tag).To(Equal(deleteTag))
		Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: epoch + 1}), "the remove's version")
		Expect(comp.Meta).To(Equal(rgwcls.DirEntryMeta{Category: rgwcls.CategoryNone, Mtime: mtime}), "cls_obj_complete_del: the removed object's mtime")
		_, ok := c.Entry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeFalse())
		Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeZero())

		entries := gcEntries()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal("tx-put\x00"), "complete_atomic_modification under the tail tag")
		Expect(entries[0].Chain).To(Equal([]rgwcls.GCObj{{Pool: testDataPool, Key: rgwcls.ObjKey{Name: tail}}}))
		Expect(c.Object(testDataPool, "", tail)).NotTo(BeNil(), "tails wait for the GC worker")
	})

	It("adjusts the quota cache by one object and the accounted size", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 6 << 20, SizeRounded: 6 << 20, NumObjects: 1}), "the PUT")
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
		Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{}), "update_stats(-1, 0, accounted_size), rgw_rados.cc:5984")
	})

	It("removes a compressed object's original size from the quota cache", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		ci := meta.NewCompressionInfo()
		ci.Type, ci.OrigSize = "zlib", 2<<20
		c.Object(testDataPool, "", headOID).Xattrs[meta.AttrCompression] = encode(ci)
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
		Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 4 << 20, SizeRounded: 4 << 20}),
			"accounted_size is the compression's orig_size, not the stored size")
	})

	It("queues nothing for an object held in its head", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader([]byte("small")), op.PutParams{Size: 5, Tag: "tx-put"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
		Expect(gcEntries()).To(BeEmpty())
	})

	It("answers NoSuchKey for a missing head without touching the index", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		missing := meta.ObjKey{Name: "missing"}
		Expect(s.DeleteObject(ctx, rec, missing, op.DeleteParams{})).To(MatchError(op.ErrNoSuchKey))
		Expect(c.Reads(testDataPool, "", putBucketID+"_missing")).To(Equal(1))
		Expect(c.Writes(rookIndexPool, "", indexShardOID(rec, "missing"))).To(BeZero(), "rgw_rados.cc:5903-5912: ENOENT before the prepare")
	})

	It("leaves a pending index entry whose head is gone to the listing", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		staleShard := indexShardOID(rec, "stale")
		stale := entry("stale", 1)
		// A pending op past the 120 s tag timeout, which the class drops.
		stale.PendingMap = []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: mtime.Add(-time.Hour), Op: uint8(rgwcls.OpDel)}}}
		seedIndexEntry(c, rookIndexPool, staleShard, stale)
		Expect(s.DeleteObject(ctx, rec, meta.ObjKey{Name: "stale"}, op.DeleteParams{})).To(MatchError(op.ErrNoSuchKey))
		Expect(c.Writes(rookIndexPool, "", staleShard)).To(BeZero())
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"k"}), "check_disk_state finds no head")
		Eventually(func() bool {
			_, ok := c.Entry(rookIndexPool, "", staleShard, "stale")
			return ok
		}).Should(BeFalse(), "its CEPH_RGW_REMOVE suggestion")
	})

	It("completes the delete when the head vanished between the stat and the remove", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		c.BeforeWrite(testDataPool, "", headOID, func(*fakerados.Object) { c.Remove(testDataPool, "", headOID) })
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed(), "ENOENT from the remove is success, rgw_rados.cc:5955-5967")
		writes := indexWrites()
		Expect(writes).To(HaveLen(2))
		comp := execIn(writes[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.Op).To(Equal(rgwcls.OpDel))
		Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: c.PoolID(testDataPool)}), "the failed remove returned no version")
		Expect(gcEntries()).To(HaveLen(1), "complete_atomic_modification runs on ENOENT too")
		Expect(driver.CachedBucketStatsForTest(s, rec).NumObjects).To(BeZero())
	})

	It("cancels with radosgw's -1:0 and reports the race when the tag changed underneath", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		c.BeforeWrite(testDataPool, "", headOID, func(o *fakerados.Object) { o.Xattrs["user.rgw.idtag"] = []byte("tx-racer\x00") })
		err := s.DeleteObject(ctx, rec, key, op.DeleteParams{})
		Expect(err).To(MatchError(op.ErrConcurrentModification), "ECANCELED; the op answers 204 (rgw_op.cc:5302-5304)")
		writes := indexWrites()
		Expect(writes).To(HaveLen(2), "the prepare and the cancel")
		cancel := execIn(writes[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(cancel.Op).To(Equal(rgwcls.OpCancel))
		Expect(cancel.Tag).To(Equal(deleteTag))
		Expect(cancel.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "index_op.cancel, rgw_rados.cc:5968-5972")
		Expect(c.Object(testDataPool, "", headOID)).NotTo(BeNil(), "the racer's object stands")
		Expect(gcEntries()).To(BeEmpty())
		Expect(driver.CachedBucketStatsForTest(s, rec).NumObjects).To(BeEquivalentTo(1), "the quota cache is left alone")
	})

	It("keeps Squid's guard on Tentacle, so a replacement made after the read stands", func(ctx SpecContext) {
		setup(ctx, denc.Tentacle)
		c.BeforeWrite(testDataPool, "", headOID, func(o *fakerados.Object) {
			o.Xattrs["user.rgw.idtag"] = []byte("tx-racer\x00")
			o.Data = []byte("replacement")
		})
		err := s.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatch: `"` + md5hex(body) + `"`})
		Expect(err).To(MatchError(op.ErrConcurrentModification), "v20.2.4 sends no guard and would remove the replacement (docs/exclusions.md)")
		Expect(c.Object(testDataPool, "", headOID).Data).To(Equal([]byte("replacement")))
		writes := indexWrites()
		Expect(writes).To(HaveLen(2))
		Expect(execIn(writes[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).Op).To(Equal(rgwcls.OpCancel))
		Expect(gcEntries()).To(BeEmpty(), "the tails the delete read are not queued")
	})

	It("sends no guard for a head whose tag radosgw would fake, and queues its tails under the fake tag", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		head := c.Object(testDataPool, "", headOID)
		delete(head.Xattrs, meta.AttrIDTag)
		delete(head.Xattrs, meta.AttrTailTag)
		sum := md5.Sum(append(slices.Clone(head.Xattrs[meta.AttrManifest]), head.Xattrs[meta.AttrETag]...))
		fake := tail + "_" + hex.EncodeToString(sum[:]) + "\x00"
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
		w := c.LastWrite(testDataPool, "", headOID)
		Expect(w.Steps()).To(HaveLen(1), "no cmpxattr: need_guard is false for a fake tag, rgw_rados.cc:6493-6495")
		execIn(w, 0, "obj_remove", rgwcls.DecodeObjRemoveOp)
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, fake)))
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal(fake), "generate_fake_tag, rgw_rados.cc:6052-6083")
		Expect(entries[0].Chain).To(Equal([]rgwcls.GCObj{{Pool: testDataPool, Key: rgwcls.ObjKey{Name: tail}}}))
	})

	It("leaves the pending entry alone when the remove times out", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		c.FailNextWrite(testDataPool, "", headOID, syscall.ETIMEDOUT)
		err := s.DeleteObject(ctx, rec, key, op.DeleteParams{})
		Expect(err).To(MatchError(op.ErrRequestTimedOut))
		Consistently(func() int { return c.Writes(rookIndexPool, "", shard) }).WithTimeout(50*time.Millisecond).Should(Equal(1),
			"the prepare only: neither complete_del nor cancel, rgw_rados.cc:5951-5954")
		en, ok := c.Entry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue())
		Expect(en.PendingMap).To(HaveLen(1), "the listing's check_disk_state reconciles it")
		Expect(gcEntries()).To(BeEmpty())
		Expect(driver.CachedBucketStatsForTest(s, rec).NumObjects).To(BeEquivalentTo(1))
	})

	It("refuses a delete of an object modified after x-amz-delete-if-unmodified-since, and checks it in the op otherwise", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{UnmodifiedSince: mtime.Add(-time.Second)})).To(MatchError(op.ErrPreconditionFailed))
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "refused before the prepare")
		Expect(c.Writes(testDataPool, "", headOID)).To(BeZero())
		since := mtime.Truncate(time.Second)
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{UnmodifiedSince: since})).To(Succeed(), "the same second")
		w := c.LastWrite(testDataPool, "", headOID)
		Expect(execIn(w, 0, "obj_check_mtime", rgwcls.DecodeCheckMtimeOp)).To(Equal(rgwcls.CheckMtimeOp{Mtime: since, Type: rgwcls.MtimeLE}),
			"appended before prepare_atomic_modification's guard, rgw_rados.cc:5874 and :5914; S3 requests compare in whole seconds")
		Expect(w.Steps()[1]).To(BeAssignableToTypeOf(&radosclient.CmpXattrStep{}))
		execIn(w, 2, "obj_remove", rgwcls.DecodeObjRemoveOp)
	})

	It("cancels when the object changed after the stat under x-amz-delete-if-unmodified-since", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		c.BeforeWrite(testDataPool, "", headOID, func(o *fakerados.Object) { o.Mtime = mtime.Add(time.Hour) })
		err := s.DeleteObject(ctx, rec, key, op.DeleteParams{UnmodifiedSince: mtime})
		Expect(err).To(MatchError(op.ErrConcurrentModification), "obj_check_mtime's ECANCELED")
		Expect(c.Object(testDataPool, "", headOID)).NotTo(BeNil())
		Expect(execIn(indexWrites()[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).Op).To(Equal(rgwcls.OpCancel))
	})

	DescribeTable("refuses a delete whose conditions fail before touching the index",
		func(ctx SpecContext, rel denc.Release, p func() op.DeleteParams) {
			setup(ctx, rel)
			Expect(s.DeleteObject(ctx, rec, key, p())).To(MatchError(op.ErrPreconditionFailed))
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
			Expect(c.Object(testDataPool, "", headOID)).NotTo(BeNil())
		},
		Entry("If-Match naming another ETag on Tentacle", denc.Tentacle, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"nope"`} }),
		Entry("If-Match naming another ETag on Squid, as Tentacle", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"nope"`} }),
		Entry("x-amz-if-match-size on Tentacle", denc.Tentacle, func() op.DeleteParams { return op.DeleteParams{IfMatchSize: new(uint64(1))} }),
		Entry("x-amz-if-match-size on Squid, as Tentacle", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatchSize: new(uint64(1))} }),
		Entry("x-amz-if-match-last-modified-time on Tentacle", denc.Tentacle, func() op.DeleteParams {
			return op.DeleteParams{IfMatchLastModified: mtime.Add(time.Second)}
		}),
		Entry("x-amz-if-match-last-modified-time on Squid, as Tentacle", denc.Squid, func() op.DeleteParams {
			return op.DeleteParams{IfMatchLastModified: mtime.Add(time.Second)}
		}),
		Entry("x-amz-delete-if-unmodified-since on Tentacle", denc.Tentacle, func() op.DeleteParams {
			return op.DeleteParams{UnmodifiedSince: mtime.Add(-time.Second)}
		}),
	)

	DescribeTable("honors the three match headers with Tentacle's checks, guarded on both releases",
		func(ctx SpecContext, rel denc.Release) {
			setup(ctx, rel)
			size := uint64(len(body))
			p := op.DeleteParams{
				IfMatch: `"` + md5hex(body) + `"`, IfMatchSize: &size, IfMatchLastModified: mtime.Add(500 * time.Millisecond),
				UnmodifiedSince: mtime.Add(time.Hour),
			}
			Expect(s.DeleteObject(ctx, rec, key, p)).To(Succeed())
			w := c.LastWrite(testDataPool, "", headOID)
			Expect(w.Steps()).To(HaveLen(4))
			Expect(execIn(w, 0, "obj_check_mtime", rgwcls.DecodeCheckMtimeOp)).To(Equal(rgwcls.CheckMtimeOp{Mtime: p.UnmodifiedSince, Type: rgwcls.MtimeLE}))
			Expect(execIn(w, 1, "obj_check_mtime", rgwcls.DecodeCheckMtimeOp)).To(Equal(rgwcls.CheckMtimeOp{Mtime: p.IfMatchLastModified, Type: rgwcls.MtimeEQ}),
				"cls_obj_check_mtime EQ for last-modified-match, rgw_rados.cc:6665-6668 at v20.2.4")
			Expect(w.Steps()[2]).To(Equal(&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-put\x00")}),
				"Squid's guard, kept on Tentacle (docs/exclusions.md)")
			execIn(w, 3, "obj_remove", rgwcls.DecodeObjRemoveOp)
			Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
		},
		Entry("on Squid", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)

	It("passes If-Match: * for an existing object", func(ctx SpecContext) {
		setup(ctx, denc.Tentacle)
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatch: "*"})).To(Succeed())
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
	})
})
