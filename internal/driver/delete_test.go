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
	"github.com/onsi/gomega/gbytes"

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

var _ = Describe("checkDiskState's multipart-part sweep", func() {
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		mtime time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		clock := mtime
		c.SetClock(func() time.Time { return clock })
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
	})
	// partNameOnShard is the first of head.2~u.1, head.2~u.2, ... whose own
	// name hashes to head's shard when same, and to another when not.
	partNameOnShard := func(head string, same bool) string {
		for i := 1; ; i++ {
			name := fmt.Sprintf("%s.2~u.%d", head, i)
			if (shardOf(rec, name) == shardOf(rec, head)) == same {
				return name
			}
		}
	}
	// completeDels decodes the bucket_complete_op DELs among writes.
	completeDels := func(writes []fakerados.RecordedWrite) []rgwcls.CompleteOp {
		var out []rgwcls.CompleteOp
		for _, w := range writes {
			for _, st := range w.Steps() {
				if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_complete_op" {
					if c := rgwcls.DecodeCompleteOp(denc.NewDecoder(x.In)); c.Op == rgwcls.OpDel {
						out = append(out, c)
					}
				}
			}
		}
		return out
	}
	// seedMultipartHead writes head, then gives it an explicit manifest with
	// one piece in the head and one in the multipart namespace, the shortest
	// way to name a part head (get_implicit_location puts a part's first
	// stripe there, rgw_obj_manifest.cc:236-239). The head's entry is left
	// pending, as when the completion's complete did not land, and the
	// part's entry sits where the part writer put it: on the shard the
	// HEAD's name hashes to, accounted in its header. It returns the head's
	// mtime.
	seedMultipartHead := func(ctx context.Context, head, part string) time.Time {
		GinkgoHelper()
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: head}, bytes.NewReader(bytes.Repeat([]byte("h"), 1<<20)), op.PutParams{
			Attrs: map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(attrsOwner, "Alice"))}, Size: 1 << 20, Tag: "tx-mp",
		})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		headObj := meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: head}}
		m := meta.NewManifest()
		m.ExplicitObjs, m.ObjSize, m.Obj = true, 2<<20, headObj
		m.Objs = map[uint64]meta.ManifestPart{
			0:       {Loc: headObj, Size: 1 << 20},
			1 << 20: {Loc: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: part, NS: meta.NSMultipart}}, Size: 1 << 20},
		}
		c.Object(testDataPool, "", putBucketID+"_"+head).Xattrs[meta.AttrManifest] = encode(m)
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: head})
		Expect(err).NotTo(HaveOccurred())
		headShard := indexShardOID(rec, head)
		pending := entry(head, 1<<20)
		pending.Exists = false
		// A pending op inside the tag timeout: the listing's suggestion for
		// the head changes nothing, so only the sweep changes the shard.
		pending.PendingMap = []rgwcls.PendingEntry{{Tag: "tx-mp", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: mtime, Op: uint8(rgwcls.OpAdd)}}}
		c.Object(rookIndexPool, "", headShard).Omap[head] = encode(pending)
		partEntry := entry("part", 1<<20)
		partEntry.Key.Name = meta.ObjKey{Name: part, NS: meta.NSMultipart}.IndexKeyName()
		seedIndexEntry(c, rookIndexPool, headShard, partEntry)
		h := c.Header(rookIndexPool, "", headShard)
		main := h.Stats[rgwcls.CategoryMain]
		main.NumEntries++
		main.TotalSize += 1 << 20
		main.TotalSizeRounded += 1 << 20
		main.ActualSize += 1 << 20
		h.Stats[rgwcls.CategoryMain] = main
		seedShardHeader(c, rookIndexPool, headShard, h)
		c.ResetCounters()
		return st.Mtime
	}

	// seedImplicitHead writes head, then gives it the manifest a completed
	// upload of parts 6 MiB parts leaves, striped at 4 MiB under the prefix
	// head.2~u, with no data in the head, and leaves the head's entry
	// pending. It returns the head's mtime and the parts' names.
	seedImplicitHead := func(ctx context.Context, head string, parts int) (time.Time, []string) {
		GinkgoHelper()
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: head}, bytes.NewReader([]byte("x")), op.PutParams{Size: 1, Tag: "tx-mp"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		headObj := meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: head}}
		m := meta.NewManifest()
		m.Obj, m.ObjSize, m.Prefix = headObj, uint64(parts)*(6<<20), head+".2~u"
		m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1, PartSize: 6 << 20, StripeMaxSize: 4 << 20}}
		m.TailPlacement = meta.BucketPlacement{Bucket: rec.Info.Bucket, PlacementRule: rec.Info.PlacementRule}
		obj := c.Object(testDataPool, "", putBucketID+"_"+head)
		obj.Data = nil
		obj.Xattrs[meta.AttrManifest] = encode(m)
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: head})
		Expect(err).NotTo(HaveOccurred())
		pending := entry(head, uint64(parts)*(6<<20))
		pending.Exists = false
		pending.PendingMap = []rgwcls.PendingEntry{{Tag: "tx-mp", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: mtime, Op: uint8(rgwcls.OpAdd)}}}
		c.Object(rookIndexPool, "", indexShardOID(rec, head)).Omap[head] = encode(pending)
		c.ResetCounters()
		names := make([]string, parts)
		for i := range names {
			names[i] = fmt.Sprintf("%s.2~u.%d", head, i+1)
		}
		return st.Mtime, names
	}
	// delsByKey is every complete_op DEL sent to the bucket's shards: the
	// shards each index key's went to.
	delsByKey := func() map[string][]string {
		out := map[string][]string{}
		for i := range rec.Info.Layout.Current.Layout.Normal.NumShards {
			oid := rec.Info.IndexShardOID(rec.Info.Layout.Current, i)
			for _, d := range completeDels(c.WritesTo(rookIndexPool, "", oid)) {
				out[d.Key.Name] = append(out[d.Key.Name], oid)
			}
		}
		return out
	}
	// partKeys maps each part's index key to the shard its own name hashes to.
	partKeys := func(parts ...string) map[string][]string {
		out := map[string][]string{}
		for _, p := range parts {
			out[meta.ObjKey{Name: p, NS: meta.NSMultipart}.IndexKeyName()] = []string{indexShardOID(rec, p)}
		}
		return out
	}

	It("sends one complete_del per part of an implicit multipart manifest, on the shard each part's name hashes to", func(ctx SpecContext) {
		headMtime, parts := seedImplicitHead(ctx, "big", 3)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"big"}))
		settle(s)
		Expect(delsByKey()).To(Equal(partKeys(parts...)), "each part's first stripe is in the multipart namespace; its other stripes are shadow (rgw_obj_manifest.cc:223-245)")
		for i := range rec.Info.Layout.Current.Layout.Normal.NumShards {
			for _, d := range completeDels(c.WritesTo(rookIndexPool, "", rec.Info.IndexShardOID(rec.Info.Layout.Current, i))) {
				Expect(d.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}))
				Expect(d.Meta.Mtime).To(BeTemporally("==", headMtime))
			}
		}
	})

	It("stops the sweep with a warning past its stripe bound, and the listing goes on", func(ctx SpecContext) {
		logs := gbytes.NewBuffer()
		DeferCleanup(driver.CaptureLog(logs))
		_, parts := seedImplicitHead(ctx, "big", 3)
		s.SetSweepLimitForTest(3)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"big"}))
		settle(s)
		Expect(delsByKey()).To(Equal(partKeys(parts[:2]...)), "parts 1 and 2 lie in the first three stripes; part 3 stays in the index")
		Expect(logs).To(gbytes.Say(`could not walk the manifest for its multipart parts' index entries`))
		Expect(logs).To(gbytes.Say(`too many stripes`))
	})

	It("sends delete_obj_index's complete_del for every multipart stripe to the shard the part's own name hashes to", func(ctx SpecContext) {
		part := partNameOnShard("mp", false)
		headMtime := seedMultipartHead(ctx, "mp", part)
		partShard := indexShardOID(rec, part)
		headShard := indexShardOID(rec, "mp")
		partKey := meta.ObjKey{Name: part, NS: meta.NSMultipart}.IndexKeyName()
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"mp"}), "the head is reconciled and kept")
		var dels []rgwcls.CompleteOp
		Eventually(func() []rgwcls.CompleteOp {
			dels = completeDels(c.WritesTo(rookIndexPool, "", partShard))
			return dels
		}).Should(HaveLen(1), "delete_obj_index, rgw_rados.cc:6033-6050")
		del := dels[0]
		Expect(del.Tag).To(BeEmpty(), "an UpdateIndex never prepared: its optag is empty")
		Expect(del.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}))
		Expect(del.Key).To(Equal(rgwcls.ObjKey{Name: partKey}))
		Expect(del.Meta).To(Equal(rgwcls.DirEntryMeta{Category: rgwcls.CategoryNone, Mtime: headMtime}), "cls_obj_complete_del: ent.meta.mtime = astate->mtime")
		Expect(del.RemoveObjs).To(BeEmpty())
		settle(s)
		Expect(completeDels(c.WritesTo(rookIndexPool, "", headShard))).To(BeEmpty(), "nothing is sent to the shard that holds the entry")
		_, ok := c.Entry(rookIndexPool, "", headShard, partKey)
		Expect(ok).To(BeTrue(),
			"radosgw's sweep misses the entry on a multi-shard bucket: 'not on disk, no action' on the wrong shard (cls_rgw.cc:1127-1140); bucket check reports it")
	})

	It("retires the entry when the part's name hashes to the shard that holds it", func(ctx SpecContext) {
		part := partNameOnShard("mp", true)
		seedMultipartHead(ctx, "mp", part)
		shard := indexShardOID(rec, "mp")
		partKey := meta.ObjKey{Name: part, NS: meta.NSMultipart}.IndexKeyName()
		before := c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries
		_, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			_, ok := c.Entry(rookIndexPool, "", shard, partKey)
			return ok
		}).Should(BeFalse())
		settle(s)
		Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(Equal(before-1), "unaccount_entry dropped the part; the head stays")
		_, ok := c.Entry(rookIndexPool, "", shard, "mp")
		Expect(ok).To(BeTrue())
	})

	It("sends nothing for a head whose manifest has no multipart stripe", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "plain"}, bytes.NewReader(bytes.Repeat([]byte("x"), 6<<20)), op.PutParams{Size: 6 << 20, Tag: "tx-plain"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		shard := indexShardOID(rec, "plain")
		pending := entry("plain", 6<<20)
		pending.Exists = false
		pending.PendingMap = []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: mtime}}}
		c.Object(rookIndexPool, "", shard).Omap["plain"] = encode(pending)
		c.ResetCounters()
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"plain"}))
		settle(s)
		for i := range rec.Info.Layout.Current.Layout.Normal.NumShards {
			oid := rec.Info.IndexShardOID(rec.Info.Layout.Current, i)
			Expect(completeDels(c.WritesTo(rookIndexPool, "", oid))).To(BeEmpty(), "the head's and the shadow tail's stripes are not in the multipart namespace")
		}
	})

	It("sends nothing for a pending entry whose head is gone", func(ctx SpecContext) {
		shard := indexShardOID(rec, "gone")
		gone := entry("gone", 1)
		gone.PendingMap = []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: mtime}}}
		seedIndexEntry(c, rookIndexPool, shard, gone)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Entries).To(BeEmpty())
		settle(s)
		Expect(completeDels(c.WritesTo(rookIndexPool, "", shard))).To(BeEmpty(), "check_disk_state returns -ENOENT before the sweep (rgw_rados.cc:10362-10378)")
	})
})
