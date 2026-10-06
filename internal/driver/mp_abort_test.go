package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// holdLock makes another client, client.999, the holder of the
// RGWCompleteMultipart lock on the meta object oid, as a completion running
// on another gateway holds it.
func holdLock(ctx context.Context, c *fakerados.Cluster, oid string) {
	GinkgoHelper()
	p, err := c.Pool(ctx, extraPoolName, "")
	Expect(err).NotTo(HaveOccurred())
	w := radosclient.NewWriteOp()
	lock.LockExisting(w, "RGWCompleteMultipart", "", "", 10*time.Minute, denc.Squid)
	_, err = p.Write(ctx, oid, w, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred())
	obj := c.Object(extraPoolName, "", oid)
	info := lock.DecodeInfo(denc.NewDecoder(obj.Xattrs["lock.RGWCompleteMultipart"]))
	for id, li := range info.Lockers {
		delete(info.Lockers, id)
		id.Locker.Num = 999
		info.Lockers[id] = li
	}
	obj.Xattrs["lock.RGWCompleteMultipart"] = encode(info)
}

// releaseLock drops every holder of the RGWCompleteMultipart lock on the meta
// object oid.
func releaseLock(c *fakerados.Cluster, oid string) {
	delete(c.Object(extraPoolName, "", oid).Xattrs, "lock.RGWCompleteMultipart")
}

// bumpVersion increments the cls_version of o, as a part registration
// racing a removal does.
func bumpVersion(o *fakerados.Object) {
	v := version.DecodeObjVersion(denc.NewDecoder(o.Xattrs[version.XattrName]))
	v.Ver++
	o.Xattrs[version.XattrName] = encode(v)
}

// ownLock takes the RGWCompleteMultipart lock on the meta object oid as this
// gateway's own completion takes it: the same client, cookie "".
func ownLock(ctx context.Context, c *fakerados.Cluster, oid string) {
	GinkgoHelper()
	p, err := c.Pool(ctx, extraPoolName, "")
	Expect(err).NotTo(HaveOccurred())
	w := radosclient.NewWriteOp()
	lock.LockExisting(w, "RGWCompleteMultipart", "", "", 10*time.Minute, denc.Squid)
	_, err = p.Write(ctx, oid, w, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred())
}

// multipartEntry is an index entry for name in the multipart namespace.
func multipartEntry(name string) rgwcls.DirEntry {
	e := entry(name, 0)
	e.Key = rgwcls.ObjKey{Name: meta.ObjKey{Name: name, NS: meta.NSMultipart}.IndexKeyName()}
	return e
}

var _ = Describe("Abort", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		mtime   time.Time
		clock   *specClock
		shard   string
		headOID string
		before  op.Stats
		stores  []*driver.Store
	)
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		clock = &specClock{t: mtime}
		c = newPutCluster()
		c.SetClock(clock.Now)
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		driver.SetClock(s, clock.Now)
		stores = []*driver.Store{s}
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		shard = indexShardOID(rec, "k")
		headOID = putBucketID + "_k"
		rec.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: -1, Enabled: true}
		Expect(s.CheckQuota(ctx, rec, rec.Info.Owner, 0, 0)).To(Succeed())
	})
	AfterEach(func() {
		for _, st := range stores {
			settle(st)
		}
	})
	partAttrs := func() map[string][]byte {
		return map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(initiator, "Alice"))}
	}
	putPart := func(ctx context.Context, st *driver.Store, up *op.Upload, n, size int) string {
		GinkgoHelper()
		r, err := st.PutPart(ctx, up, n, bytes.NewReader(bytes.Repeat([]byte{byte('a' + n)}, size)), op.PutParams{Attrs: partAttrs(), Size: int64(size)})
		Expect(err).NotTo(HaveOccurred())
		return r.ETag
	}
	// uploadWithParts creates an upload of k and puts a part of each size,
	// returning the upload and the parts' ETags.
	uploadWithParts := func(ctx context.Context, sizes ...int) (*op.Upload, []string) {
		GinkgoHelper()
		up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		var etags []string
		for i, n := range sizes {
			etags = append(etags, putPart(ctx, s, up, i+1, n))
		}
		settle(s)
		before = driver.CachedBucketStatsForTest(s, rec)
		return up, etags
	}
	completeParts := func(etags []string) []op.CompletePart {
		out := make([]op.CompletePart, len(etags))
		for i, e := range etags {
			out[i] = op.CompletePart{Number: i + 1, ETag: `"` + e + `"`}
		}
		return out
	}
	partEntry := func(up *op.Upload, n int) string { return partIndexKey("k."+up.ID, n) }
	// partsIntact checks that the meta object of up stands and that each of
	// its first n parts has its index entry, its part info and its head.
	partsIntact := func(up *op.Upload, n int) {
		GinkgoHelper()
		metaObj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
		Expect(metaObj).NotTo(BeNil(), "the meta object stays")
		for i := 1; i <= n; i++ {
			_, ok := c.Entry(rookIndexPool, "", shard, partEntry(up, i))
			Expect(ok).To(BeTrue(), "part %d's index entry", i)
			Expect(metaObj.Omap).To(HaveKey(meta.MultipartPartKey(uint32(i))), "part %d's info", i) //nolint:gosec // a small part number
			Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, i))).NotTo(BeNil(), "part %d's head", i)
		}
	}
	statsDelta := func() []int64 {
		now := driver.CachedBucketStatsForTest(s, rec)
		return []int64{int64(now.NumObjects) - int64(before.NumObjects), int64(now.Size) - int64(before.Size)} //nolint:gosec // spec sizes are small
	}
	// leaveMeta completes up with its meta object's removal failing, as a
	// gateway that stops after the head write leaves it.
	leaveMeta := func(ctx context.Context, up *op.Upload, etags []string) {
		GinkgoHelper()
		cl, head, oid := c, headOID, metaOID(rec, "k", up.ID)
		armed := false
		cl.BeforeWrite(extraPoolName, "", oid, func(*fakerados.Object) {
			if !armed && cl.Object(testDataPool, "", head) != nil {
				armed = true
				cl.FailNextWrite(extraPoolName, "", oid, syscall.EIO)
			}
		})
		_, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(c.Object(extraPoolName, "", oid)).NotTo(BeNil(), "the failed removal left the meta object")
		c.BeforeWrite(extraPoolName, "", oid, nil)
	}

	It("queues every part object under the upload id after removing the meta object with their entries", func(ctx SpecContext) {
		up, _ := uploadWithParts(ctx, 5<<20, 3<<20)
		s.SetRandForTest(fixedRand(reprefix))
		putPart(ctx, s, up, 1, 5<<20)
		settle(s)
		before = driver.CachedBucketStatsForTest(s, rec)
		mOID := metaOID(rec, "k", up.ID)
		c.ResetCounters()
		Expect(s.Abort(ctx, up)).To(Succeed())
		settle(s)

		Expect(c.Object(extraPoolName, "", mOID)).To(BeNil())
		writes := c.WritesTo(extraPoolName, "", mOID)
		Expect(writes).To(HaveLen(3), "the lock, the removal, the unlock")
		Expect(writes[0].Steps()[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}), "the lock first")
		Expect(execStep(writes[0].Steps()[1]).Method).To(Equal("lock"))
		del := writes[1]
		Expect(del.Flags()).To(Equal(radosclient.OpFlagFullTry))
		Expect(execMethods(writes[1:2])).To(Equal([]string{"assert_locked", "obj_remove", "check_conds"}),
			"the removal asserts the lock this abort took, so it fails rather than remove an upload a completion took over")
		cookie := lock.DecodeLockOp(denc.NewDecoder(execStep(writes[0].Steps()[1]).In)).Cookie
		assert := lock.DecodeAssertOp(denc.NewDecoder(execStep(del.Steps()[0]).In))
		Expect(assert).To(Equal(lock.AssertOp{Name: "RGWCompleteMultipart", Type: lock.TypeExclusive, Cookie: cookie}))
		Expect(execMethods(writes[2:])).To(Equal([]string{"unlock"}), "RGWAbortMultipart::execute unlocks afterwards; ENOENT is ignored")

		for _, name := range []string{partEntry(up, 1), partIndexKey("k."+reprefix, 1), partEntry(up, 2), metaIndexKey("k", up.ID)} {
			_, ok := c.Entry(rookIndexPool, "", shard, name)
			Expect(ok).To(BeFalse(), name)
		}
		dels := indexCompletions(c, shard, rgwcls.OpDel)
		Expect(dels).To(HaveLen(1))
		Expect(dels[0].RemoveObjs).To(ConsistOf(
			rgwcls.ObjKey{Name: partIndexKey("k."+reprefix, 1)}, rgwcls.ObjKey{Name: partEntry(up, 1)}, rgwcls.ObjKey{Name: partEntry(up, 2)},
		), "the current two parts' heads and the old part 1's")
		hdr := c.Header(rookIndexPool, "", shard)
		Expect(hdr.Stats[rgwcls.CategoryMain].NumEntries).To(BeZero())
		Expect(hdr.Stats[rgwcls.CategoryMultiMeta].NumEntries).To(BeZero())

		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID)))
		Expect(entries).To(HaveLen(1), "one chain under the tag")
		Expect(entries[0].Tag).To(Equal(up.ID))
		Expect(chainOIDs(entries)).To(ConsistOf(
			partHeadOID("k."+reprefix, 1), partShadowOID("k."+reprefix, 1, 1),
			partHeadOID("k."+up.ID, 2),
			partHeadOID("k."+up.ID, 1), partShadowOID("k."+up.ID, 1, 1),
		), "current parts' heads and stripes plus the past prefix's")
		for _, o := range chainOIDs(entries) {
			Expect(c.Object(testDataPool, "", o)).NotTo(BeNil(), "%s waits for the GC worker", o)
		}
		Expect(allGCEntries(c)).To(HaveLen(1))
		Expect(statsDelta()).To(Equal([]int64{-1, -(8 << 20)}), "abortmp: parts_accounted_size, the current parts' 5+3 MiB, not the meta object's size")
	})

	It("answers NoSuchUpload for an unknown id and 503 while another client holds the lock", func(ctx SpecContext) {
		Expect(s.Abort(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: key})).To(MatchError(op.ErrNoSuchUpload))
		up, _ := uploadWithParts(ctx, 5<<20)
		holdLock(ctx, c, metaOID(rec, "k", up.ID))
		Expect(s.Abort(ctx, up)).To(MatchError(op.ErrServiceUnavailable), "EBUSY, rgw_common.cc:134 at v19.2.6")
		partsIntact(up, 1)
		Expect(allGCEntries(c)).To(BeEmpty())
	})

	It("answers 503 while this gateway's own completion holds the lock", func(ctx SpecContext) {
		up, _ := uploadWithParts(ctx, 5<<20)
		ownLock(ctx, c, metaOID(rec, "k", up.ID))
		Expect(s.Abort(ctx, up)).To(MatchError(op.ErrServiceUnavailable),
			"lock_obj's EBUSY: the abort's cookie is another locker of the exclusive lock (cls_lock.cc:201-212 at v19.2.6)")
		partsIntact(up, 1)
		Expect(allGCEntries(c)).To(BeEmpty())
		Expect(c.Locks(extraPoolName, "", metaOID(rec, "k", up.ID), "RGWCompleteMultipart")).To(HaveLen(1), "the completion's hold stands")
	})

	It("locks, asserts and unlocks under a cookie of its own", func(ctx SpecContext) {
		var cookies []string
		for _, name := range []string{"k", "other"} {
			s.SetRandForTest(fixedRand(name + "COOKIECOOKIECOOKIECOOKIECOOKIE"))
			up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: name}, op.UploadParams{Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
			Expect(err).NotTo(HaveOccurred())
			mOID := metaOID(rec, name, up.ID)
			Expect(s.Abort(ctx, up)).To(Succeed())
			writes := c.WritesTo(extraPoolName, "", mOID)
			lockOp := lock.DecodeLockOp(denc.NewDecoder(execStep(writes[len(writes)-3].Steps()[1]).In))
			assertOp := lock.DecodeAssertOp(denc.NewDecoder(execStep(writes[len(writes)-2].Steps()[0]).In))
			unlockOp := lock.DecodeUnlockOp(denc.NewDecoder(execStep(writes[len(writes)-1].Steps()[0]).In))
			Expect(lockOp.Cookie).To(MatchRegexp(`^[0-9a-f]{32}$`), "128 random bits, never the completion's \"\"")
			Expect([]string{assertOp.Cookie, unlockOp.Cookie}).To(Equal([]string{lockOp.Cookie, lockOp.Cookie}))
			cookies = append(cookies, lockOp.Cookie)
		}
		Expect(cookies[0]).NotTo(Equal(cookies[1]), "each abort its own")
	})

	It("restarts the round when a part lands during the abort, and queues it too", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "pad"}, strings.NewReader("pad"), op.PutParams{Attrs: attrsWithACL(initiator), Size: 3, Tag: "t"})
		Expect(err).NotTo(HaveOccurred(), "bytes beyond the parts', so the cache's clamp at zero hides nothing")
		up, _ := uploadWithParts(ctx, 5<<20)
		mOID := metaOID(rec, "k", up.ID)
		writes, raced := 0, false
		cl, st, u := c, s, up
		cl.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
			writes++
			if writes == 2 && !raced { // the first removal, after the lock
				raced = true
				_, err := st.PutPart(ctx, u, 2, bytes.NewReader(bytes.Repeat([]byte("z"), 5<<20)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
				Expect(err).NotTo(HaveOccurred())
			}
		})
		Expect(s.Abort(ctx, up)).To(Succeed())
		settle(s)
		Expect(raced).To(BeTrue())
		Expect(c.Object(extraPoolName, "", mOID)).To(BeNil())
		Expect(execMethods(c.WritesTo(extraPoolName, "", mOID))).To(ContainElements("obj_remove", "mp_upload_part_info_update", "obj_remove"))
		entries := allGCEntries(c)
		Expect(entries).To(HaveLen(1), "nothing is queued for the canceled round")
		Expect(chainOIDs(entries)).To(ConsistOf(
			partHeadOID("k."+up.ID, 1), partShadowOID("k."+up.ID, 1, 1),
			partHeadOID("k."+up.ID, 2), partShadowOID("k."+up.ID, 2, 1),
		), "the second round saw part 2")
		for n := 1; n <= 2; n++ {
			_, ok := c.Entry(rookIndexPool, "", shard, partEntry(up, n))
			Expect(ok).To(BeFalse(), "part %d", n)
		}
		cancels := indexCompletions(c, shard, rgwcls.OpCancel)
		Expect(cancels).NotTo(BeEmpty())
		for _, cn := range cancels {
			Expect(cn.RemoveObjs).To(BeEmpty(), "a canceled removal keeps the parts' entries")
		}
		Expect(statsDelta()).To(Equal([]int64{0, -(5 << 20)}),
			"part 2 added one object and 5 MiB; the abort drops one object and both parts' 10 MiB, part 1 counted once across the rounds")
	})

	It("aborts an upload with no parts", func(ctx SpecContext) {
		up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Abort(ctx, up)).To(Succeed())
		Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil())
		Expect(allGCEntries(c)).To(BeEmpty())
		writes := c.WritesTo(extraPoolName, "", metaOID(rec, "k", up.ID))
		Expect(execMethods(writes[len(writes)-2:len(writes)-1])).To(Equal([]string{"assert_locked", "obj_remove"}),
			"no version xattr yet: version_for_check is null, so no check")
	})

	It("skips a part without a manifest and still counts it", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "pad"}, strings.NewReader("pad"), op.PutParams{Attrs: attrsWithACL(initiator), Size: 3, Tag: "t"})
		Expect(err).NotTo(HaveOccurred(), "bytes beyond the parts', so the cache's clamp at zero hides nothing")
		up, _ := uploadWithParts(ctx, 5<<20)
		seedPartInfo(c, metaOID(rec, "k", up.ID), meta.MultipartPartKey(2), 2, md5hex("two"))
		Expect(s.Abort(ctx, up)).To(Succeed())
		settle(s)
		Expect(chainOIDs(allGCEntries(c))).To(ConsistOf(partHeadOID("k."+up.ID, 1), partShadowOID("k."+up.ID, 1, 1)))
		dels := indexCompletions(c, shard, rgwcls.OpDel)
		Expect(dels).To(HaveLen(1))
		Expect(dels[0].RemoveObjs).To(ConsistOf(rgwcls.ObjKey{Name: partEntry(up, 1)}, rgwcls.ObjKey{Name: partEntry(up, 2)}),
			"the manifest-less part's head would be named by the upload's own prefix")
		Expect(statsDelta()).To(Equal([]int64{-1, -(5<<20 + 2)}))
	})

	Describe("safety", func() {
		It("removes only the meta object of an upload whose recorded completion stands (ceph/ceph#72103)", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, etags)
			mOID := metaOID(rec, "k", up.ID)
			metaSize := int64(len(c.Object(extraPoolName, "", mOID).Data))
			before = driver.CachedBucketStatsForTest(s, rec)
			c.ResetCounters()
			Expect(s.Abort(ctx, up)).To(Succeed(), "the upload is gone, and the object it completed stays")
			settle(s)
			Expect(c.Object(extraPoolName, "", mOID)).To(BeNil())
			Expect(execMethods(c.WritesTo(extraPoolName, "", mOID))).To(Equal([]string{"lock", "assert_locked", "obj_remove", "check_conds", "unlock"}))
			Expect(c.Reads(extraPoolName, "", mOID)).To(Equal(1), "the meta object's attrs; no part listing")
			Expect(allGCEntries(c)).To(BeEmpty(), "no part goes to the GC")
			dels := indexCompletions(c, shard, rgwcls.OpDel)
			Expect(dels).To(HaveLen(1))
			Expect(dels[0].RemoveObjs).To(BeEmpty(), "the meta object alone, as the PR removes it")
			for n := 1; n <= 2; n++ {
				Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, n))).NotTo(BeNil(), "part %d's data", n)
			}
			Expect(c.Object(testDataPool, "", headOID).Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-first\x00")))
			Expect(statsDelta()).To(Equal([]int64{-1, -metaSize}), "a plain delete_obj: the meta object's own size")
		})
		It("retries the meta-only removal when a part races it", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, etags)
			mOID := metaOID(rec, "k", up.ID)
			writes, raced := 0, false
			cl := c
			cl.BeforeWrite(extraPoolName, "", mOID, func(o *fakerados.Object) {
				writes++
				if writes == 2 && !raced {
					raced = true
					bumpVersion(o)
				}
			})
			Expect(s.Abort(ctx, up)).To(Succeed())
			Expect(raced).To(BeTrue())
			Expect(c.Object(extraPoolName, "", mOID)).To(BeNil())
			Expect(allGCEntries(c)).To(BeEmpty())
			Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, 1))).NotTo(BeNil())
		})
		DescribeTable("aborts as radosgw does when the recorded head does not carry the tag",
			func(ctx SpecContext, stage func(ctx context.Context, up *op.Upload, etags []string)) {
				up, etags := uploadWithParts(ctx, 5<<20)
				stage(ctx, up, etags)
				Expect(s.Abort(ctx, up)).To(Succeed())
				settle(s)
				Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil())
				Expect(chainOIDs(c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID))))).To(
					ContainElements(partHeadOID("k."+up.ID, 1), partShadowOID("k."+up.ID, 1, 1)))
			},
			Entry("a crash between the record and the head write", func(_ context.Context, up *op.Upload, _ []string) {
				obj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
				obj.Xattrs["user.rgw.mp_completion_tag"] = []byte("tx-crashed\x00")
				obj.Xattrs["user.rgw.mp_completion_instance"] = []byte("null")
			}),
			Entry("a recorded object since overwritten, whose tails the overwrite queued", func(ctx context.Context, up *op.Upload, etags []string) {
				up.WriteTag = "tx-first"
				leaveMeta(ctx, up, etags)
				_, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrsWithACL(initiator), Size: 3, Tag: "t"})
				Expect(err).NotTo(HaveOccurred())
			}),
		)
		It("refuses, with NoSuchUpload and nothing changed, an upload whose parts a live head names without a record (tracker #80896)", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, etags)
			mOID := metaOID(rec, "k", up.ID)
			obj := c.Object(extraPoolName, "", mOID)
			delete(obj.Xattrs, "user.rgw.mp_completion_tag")
			delete(obj.Xattrs, "user.rgw.mp_completion_instance")
			c.ResetCounters()
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrNoSuchUpload), "a floor radosgw's completion left the meta object; v19.2.6 and v20.2.4 would queue the object's parts")
			settle(s)
			Expect(execMethods(c.WritesTo(extraPoolName, "", mOID))).To(Equal([]string{"lock", "unlock"}))
			Expect(c.WriteOrder(rookIndexPool, "")).To(BeEmpty(), "no index change")
			for n := 1; n <= 2; n++ {
				Expect(c.Object(extraPoolName, "", mOID).Omap).To(HaveKey(meta.MultipartPartKey(uint32(n))), "part %d's info", n) //nolint:gosec // a small part number
				Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, n))).NotTo(BeNil(), "part %d's data", n)
			}
			Expect(allGCEntries(c)).To(BeEmpty())

			By("once the object is overwritten, its tails queued, the upload aborts")
			_, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrsWithACL(initiator), Size: 3, Tag: "t"})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Abort(ctx, up)).To(Succeed())
			Expect(c.Object(extraPoolName, "", mOID)).To(BeNil())
		})
		It("refuses an upload whose object names only a re-uploaded part's current prefix", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			s.SetRandForTest(fixedRand(reprefix))
			etag := putPart(ctx, s, up, 1, 5<<20)
			settle(s)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, []string{etag})
			mOID := metaOID(rec, "k", up.ID)
			delete(c.Object(extraPoolName, "", mOID).Xattrs, "user.rgw.mp_completion_tag")
			delete(c.Object(extraPoolName, "", mOID).Xattrs, "user.rgw.mp_completion_instance")
			m := meta.DecodeManifest(denc.NewDecoder(c.Object(testDataPool, "", headOID).Xattrs[meta.AttrManifest]))
			Expect(m.Prefix).To(Equal("k."+reprefix), "the head names the part's random prefix, not the upload's")
			queued := chainOIDs(allGCEntries(c))
			Expect(queued).To(ConsistOf(partHeadOID("k."+up.ID, 1), partShadowOID("k."+up.ID, 1, 1)), "the completion queued the replaced upload of part 1")
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrNoSuchUpload))
			settle(s)
			Expect(chainOIDs(allGCEntries(c))).To(Equal(queued), "nothing more is queued")
			Expect(c.Object(extraPoolName, "", mOID)).NotTo(BeNil())
			Expect(c.Object(testDataPool, "", partHeadOID("k."+reprefix, 1))).NotTo(BeNil(), "the object's data")
		})
		It("refuses an upload whose object names only a part's past prefix", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			s.SetRandForTest(fixedRand(reprefix))
			etag := putPart(ctx, s, up, 1, 5<<20)
			settle(s)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, []string{etag})
			mOID := metaOID(rec, "k", up.ID)
			delete(c.Object(extraPoolName, "", mOID).Xattrs, "user.rgw.mp_completion_tag")
			delete(c.Object(extraPoolName, "", mOID).Xattrs, "user.rgw.mp_completion_instance")

			By("part 1 uploaded once more, so the object's prefix survives only in its history")
			s.SetRandForTest(fixedRand(secondPrefix))
			putPart(ctx, s, up, 1, 5<<20)
			settle(s)
			info := storedPart(c, mOID, meta.MultipartPartKey(1))
			Expect(info.Manifest.Prefix).To(Equal("k." + secondPrefix))
			Expect(info.PastPrefixes).To(ContainElement("k." + reprefix))
			queued := chainOIDs(allGCEntries(c))
			Expect(queued).NotTo(ContainElement(partHeadOID("k."+reprefix, 1)))
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrNoSuchUpload))
			settle(s)
			Expect(chainOIDs(allGCEntries(c))).To(Equal(queued), "nothing more is queued")
			Expect(c.Object(testDataPool, "", partHeadOID("k."+reprefix, 1))).NotTo(BeNil(), "the object's data")
		})
		It("refuses an upload recorded as completed into a version, which it cannot read", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			obj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
			obj.Xattrs["user.rgw.mp_completion_tag"] = []byte("tx-versioned\x00")
			obj.Xattrs["user.rgw.mp_completion_instance"] = []byte("v1")
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrNotImplemented))
			settle(s)
			partsIntact(up, 1)
			Expect(allGCEntries(c)).To(BeEmpty())
		})
		It("keeps the upload whole and abortable when the meta object's removal fails", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			writes := 0
			cl := c
			cl.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
				writes++
				if writes == 2 {
					cl.FailNextWrite(extraPoolName, "", mOID, syscall.EIO)
				}
			})
			Expect(s.Abort(ctx, up)).To(HaveOccurred())
			settle(s)
			partsIntact(up, 2)
			Expect(allGCEntries(c)).To(BeEmpty(), "nothing is queued before the removal")
			cancels := indexCompletions(c, shard, rgwcls.OpCancel)
			Expect(cancels).To(HaveLen(1))
			Expect(cancels[0].RemoveObjs).To(BeEmpty(), "delete_obj's cancel sends remove_objs (rgw_rados.cc:5969 at v19.2.6); this one keeps the entries")
			Expect(c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")).To(BeEmpty(), "unlocked")

			Expect(s.Abort(ctx, up)).To(Succeed(), "abortable again")
			Expect(c.Object(extraPoolName, "", mOID)).To(BeNil())
			Expect(allGCEntries(c)).To(HaveLen(1))
		})
		It("queues nothing after a removal that timed out", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			writes := 0
			cl := c
			cl.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
				writes++
				if writes == 2 {
					cl.FailNextWrite(extraPoolName, "", mOID, syscall.ETIMEDOUT)
				}
			})
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			Expect(allGCEntries(c)).To(BeEmpty(), "the meta object may stand, so its parts must not be freed")
			Expect(indexCompletions(c, shard, rgwcls.OpCancel)).To(BeEmpty(), "the pending entry is left for a listing to reconcile")
		})
		It("queues nothing when another gateway removed the meta object under it", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			writes := 0
			cl := c
			cl.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
				writes++
				if writes == 2 { // a floor radosgw's bucket delete aborts without the lock
					cl.Remove(extraPoolName, "", mOID)
				}
			})
			before = driver.CachedBucketStatsForTest(s, rec)
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrNoSuchUpload), "delete_obj's -ENOENT (rgw_sal_rados.cc:3262 at v19.2.6)")
			settle(s)
			Expect(allGCEntries(c)).To(BeEmpty(), "whoever removed it owns the parts")
			Expect(statsDelta()).To(Equal([]int64{0, 0}), "delete_obj returns -ENOENT before the quota cache update")
		})
		It("removes the meta object before it queues the parts", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			gcOID := fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID))
			var metaAtQueue []*fakerados.Object
			cl := c
			cl.BeforeWrite(gcPoolName, gcNS, gcOID, func(*fakerados.Object) {
				metaAtQueue = append(metaAtQueue, cl.Object(extraPoolName, "", mOID))
			})
			Expect(s.Abort(ctx, up)).To(Succeed())
			Expect(metaAtQueue).To(HaveLen(1))
			Expect(metaAtQueue[0]).To(BeNil(), "a crash between the two leaks the parts rather than leave an upload over freed stripes")
		})
		It("fails closed when its lock lapsed before the removal", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			lapsed := false
			k := clock
			c.BeforeRead(testDataPool, "", headOID, func() {
				if !lapsed {
					lapsed = true
					k.Advance(11 * time.Minute)
				}
			})
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrServiceUnavailable), "assert_locked's EBUSY")
			Expect(lapsed).To(BeTrue())
			settle(s)
			partsIntact(up, 1)
			Expect(allGCEntries(c)).To(BeEmpty())
		})
		It("has its removal refused on the OSD once its lock lapsed and this gateway's own completion took it", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			writes, retaken := 0, false
			cl, k := c, clock
			cl.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
				writes++
				if writes == 2 && !retaken { // the removal, sent with the term's margin left
					retaken = true
					k.Advance(11 * time.Minute)
					ownLock(ctx, cl, mOID)
				}
			})
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrServiceUnavailable),
				"assert_locked's EBUSY: the holder is this gateway's client under cookie \"\", not the abort's")
			Expect(retaken).To(BeTrue())
			settle(s)
			partsIntact(up, 1)
			Expect(allGCEntries(c)).To(BeEmpty())
			Expect(c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")).To(HaveLen(1), "the abort's unlock, under its own cookie, leaves the completion's hold")
		})
		It("has a removal that lands after the client gave up refused once its lock lapsed and this gateway's own completion took it", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			writes := 0
			cl := c
			cl.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
				writes++
				if writes == 2 {
					cl.FailNextWrite(extraPoolName, "", mOID, syscall.ETIMEDOUT)
				}
			})
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			Expect(allGCEntries(c)).To(BeEmpty())
			c.BeforeWrite(extraPoolName, "", mOID, nil)

			By("the removal the client stopped waiting for runs on the OSD after the lapse and the re-lock")
			var removal []radosclient.Step
			for _, w := range c.WritesTo(extraPoolName, "", mOID) {
				if slices.Contains(execMethods([]fakerados.RecordedWrite{w}), "obj_remove") {
					removal = w.Steps()
				}
			}
			Expect(removal).NotTo(BeEmpty())
			clock.Advance(11 * time.Minute)
			ownLock(ctx, c, mOID)
			late := radosclient.NewWriteOp()
			for _, st := range removal {
				x := execStep(st)
				late.Exec(x.Class, x.Method, x.In)
			}
			p, err := c.Pool(ctx, extraPoolName, "")
			Expect(err).NotTo(HaveOccurred())
			_, err = p.Write(ctx, mOID, late, radosclient.OpFlagFullTry)
			Expect(op.FromRADOS(err, op.ScopeUpload)).To(MatchError(op.ErrServiceUnavailable), "EBUSY: assert_locked names the abort's cookie")
			partsIntact(up, 1)
			Expect(allGCEntries(c)).To(BeEmpty())
		})
		DescribeTable("sends the removal only while the margin of the lock's term remains",
			func(ctx SpecContext, elapsed time.Duration, want error) {
				up, _ := uploadWithParts(ctx, 5<<20)
				moved := false
				k := clock
				c.BeforeRead(testDataPool, "", headOID, func() {
					if !moved {
						moved = true
						k.Advance(elapsed)
					}
				})
				err := s.Abort(ctx, up)
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
					Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil())
				} else {
					Expect(err).To(MatchError(want))
					Expect(allGCEntries(c)).To(BeEmpty())
				}
			},
			Entry("more than 30 s of the 600 s term left", 9*time.Minute+29*time.Second, nil),
			Entry("30 s left", 9*time.Minute+30*time.Second, op.ErrServiceUnavailable),
		)
		It("fails closed when another client broke its lock and took it", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			taken := false
			cl := c
			cl.BeforeRead(testDataPool, "", headOID, func() {
				if !taken {
					taken = true
					releaseLock(cl, mOID)
					holdLock(ctx, cl, mOID)
				}
			})
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrServiceUnavailable), "assert_locked's EBUSY")
			settle(s)
			partsIntact(up, 1)
			Expect(allGCEntries(c)).To(BeEmpty())
		})
		It("gives up with 409 after fifteen racing parts, keeping the upload whole", func(ctx SpecContext) {
			up, _ := uploadWithParts(ctx, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			writes := 0
			c.BeforeWrite(extraPoolName, "", mOID, func(o *fakerados.Object) {
				writes++
				if writes > 1 && o != nil {
					bumpVersion(o)
				}
			})
			c.ResetCounters()
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrConcurrentModification), "ECANCELED, rgw_common.cc:141 at v19.2.6")
			settle(s)
			Expect(execMethods(c.WritesTo(extraPoolName, "", mOID))).To(HaveLen(1+15*3+1), "the lock, fifteen removals, the unlock")
			partsIntact(up, 1)
			Expect(allGCEntries(c)).To(BeEmpty())
		})
	})
})

var _ = Describe("DeleteBucket aborts in-flight uploads", func() {
	const zgID = "zg-ceph-objectstore"
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
	)
	create := func(ctx context.Context, zonegroup string) *op.BucketRecord {
		GinkgoHelper()
		r, err := s.CreateBucket(ctx, op.CreateBucketParams{
			Name: "plain", Owner: initiator, Zonegroup: zonegroup,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{meta.AttrACL: {1}}, Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		return r
	}
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
		DeferCleanup(s.Close)
		rec = create(ctx, zgID)
	})
	AfterEach(func() { settle(s) })
	upload := func(ctx context.Context, r *op.BucketRecord, name string, parts int) *op.Upload {
		GinkgoHelper()
		up, err := s.CreateUpload(ctx, r, meta.ObjKey{Name: name}, op.UploadParams{Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		for n := 1; n <= parts; n++ {
			_, err := s.PutPart(ctx, up, n, bytes.NewReader(bytes.Repeat([]byte("p"), 5<<20)), op.PutParams{Attrs: attrsWithACL(initiator), Size: 5 << 20})
			Expect(err).NotTo(HaveOccurred())
		}
		settle(s)
		return up
	}
	shardOID := func(r *op.BucketRecord, name string) string {
		return fmt.Sprintf(".dir.%s.%d", r.Info.Bucket.ID, shardOf(r, name))
	}

	It("aborts every upload in pages of 1000, counting each once, and then deletes the bucket", func(ctx SpecContext) {
		up1 := upload(ctx, rec, "k", 1)
		s.SetRandForTest(fixedRand(secondPrefix))
		up2 := upload(ctx, rec, "other", 0)
		for i := range 1001 { // meta entries without objects, each a tolerated NoSuchUpload; 1003 > 1000 makes a second page
			name := fmt.Sprintf("ghost%04d", i)
			seedIndexEntry(c, rookIndexPool, shardOID(rec, name), multipartEntry(name+".2~x.meta"))
		}
		n, err := driver.AbortMultipartsForTest(s, ctx, rec)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(1003), "the two real uploads and the 1001 skipped ones, once each, where radosgw aborts page one again on page two")
		Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up1.ID))).To(BeNil())
		Expect(c.Object(extraPoolName, "", metaOID(rec, "other", up2.ID))).To(BeNil())
		Expect(c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up1.ID)))).To(HaveLen(1), "the part's stripes wait for the GC worker")

		for i := range 1001 {
			name := fmt.Sprintf("ghost%04d", i)
			delete(c.Object(rookIndexPool, "", shardOID(rec, name)).Omap, "_multipart_"+name+".2~x.meta")
		}
		Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "the second listing finds nothing to abort")
		Expect(c.Objects(rookIndexPool, "")).To(BeEmpty(), "clean_index ran after the aborts")
		Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
	})
	It("aborts through DeleteBucket, skips a meta entry whose object is gone, and fails the delete on any other abort error", func(ctx SpecContext) {
		seedIndexEntry(c, rookIndexPool, shardOID(rec, "ghost"), multipartEntry("ghost.2~x.meta"))
		up := upload(ctx, rec, "k", 1)
		mOID := metaOID(rec, "k", up.ID)
		holdLock(ctx, c, mOID)
		Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrServiceUnavailable), "abort_multiparts returns the abort's error and remove stops there")
		Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil(), "the entry point survives")
		Expect(c.Object(extraPoolName, "", mOID)).NotTo(BeNil())
		releaseLock(c, mOID)
		Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "the ghost entry is skipped, the real upload aborted")
		Expect(c.Object(extraPoolName, "", mOID)).To(BeNil())
		Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
		Expect(allGCEntries(c)).To(HaveLen(1))
	})
	It("skips, with a warning, an upload whose parts back an object, and counts it as radosgw's num_deleted does", func(ctx SpecContext) {
		up := upload(ctx, rec, "k", 1)
		mOID := metaOID(rec, "k", up.ID)
		cl, head := c, rec.Info.Bucket.Marker+"_k"
		armed := false
		cl.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
			if !armed && cl.Object(testDataPool, "", head) != nil {
				armed = true
				cl.FailNextWrite(extraPoolName, "", mOID, syscall.EIO)
			}
		})
		etag := md5hex(bytes.Repeat([]byte("p"), 5<<20))
		_, err := s.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: etag}})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		c.BeforeWrite(extraPoolName, "", mOID, nil)
		delete(c.Object(extraPoolName, "", mOID).Xattrs, "user.rgw.mp_completion_tag")
		delete(c.Object(extraPoolName, "", mOID).Xattrs, "user.rgw.mp_completion_instance")
		var logs syncBuffer
		DeferCleanup(driver.CaptureLog(&logs))
		n, err := driver.AbortMultipartsForTest(s, ctx, rec)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(1), "radosgw's abort of it returns 0, which num_deleted counts")
		Expect(c.Object(extraPoolName, "", mOID)).NotTo(BeNil())
		Expect(allGCEntries(c)).To(BeEmpty())
		Expect(logs.String()).To(ContainSubstring(`"level":"WARN","msg":"not aborting a multipart upload whose parts back an object`))
		Expect(logs.String()).NotTo(ContainSubstring("not found for cleanup"))
	})
	It("fails the delete with 503 while this gateway's own completion holds an upload's lock", func(ctx SpecContext) {
		up := upload(ctx, rec, "k", 1)
		ownLock(ctx, c, metaOID(rec, "k", up.ID))
		Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrServiceUnavailable))
		Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil(), "the entry point survives")
	})
	It("refuses a bucket with an object before it aborts anything", func(ctx SpecContext) {
		up := upload(ctx, rec, "k", 1)
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "o"}, strings.NewReader("x"), op.PutParams{Attrs: attrsWithACL(initiator), Size: 1, Tag: "t"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty))
		Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).NotTo(BeNil())
	})
	It("does not list or abort anything for a bucket another zonegroup owns", func(ctx SpecContext) {
		Expect(s.DeleteBucket(ctx, rec)).To(Succeed())
		foreign := create(ctx, "elsewhere")
		up := upload(ctx, foreign, "k", 1)
		c.ResetCounters()
		Expect(s.DeleteBucket(ctx, foreign)).To(Succeed())
		for i := range 11 {
			Expect(c.Reads(rookIndexPool, "", fmt.Sprintf(".dir.%s.%d", foreign.Info.Bucket.ID, i))).To(BeZero(), "results.is_truncated = own_bucket: no listing at all")
		}
		Expect(c.Writes(extraPoolName, "", metaOID(foreign, "k", up.ID))).To(BeZero())
		Expect(c.Object(extraPoolName, "", metaOID(foreign, "k", up.ID))).NotTo(BeNil(), "abort_multiparts runs only under own_bucket")
	})
})
