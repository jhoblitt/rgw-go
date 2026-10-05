package driver_test

import (
	"bytes"
	"context"
	"strings"
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

var _ = Describe("the head write's cancel", func() {
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		shard string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, time.Now())
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		shard = indexShardOID(rec, "k")
	})
	errno := func(n syscall.Errno) error { return &radosclient.Error{Errno: int32(n), Op: "write"} }

	DescribeTable("is done_cancel: the index entry is canceled unless the write timed out, then the race is judged",
		func(ctx SpecContext, ifMatch, ifNoneMatch string, cause error, wantCancel bool, want error) {
			x := s.NewIndexOpForTest(rec, meta.ObjKey{Name: "k"}, "tag")
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			canceled, err := s.CancelWriteForTest(x, ifMatch, ifNoneMatch, cause)
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(want))
			}
			Expect(canceled).To(Equal(wantCancel))
			settle(s)
			writes := 1
			if wantCancel {
				writes = 2
				Expect(execIn(c.LastWrite(rookIndexPool, "", shard), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).Op).To(Equal(rgwcls.OpCancel))
			}
			Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(writes))
		},
		Entry("a replaced head without conditions is success", "", "", errno(syscall.ECANCELED), true, nil),
		Entry("a removed head without conditions is success", "", "", errno(syscall.ENOENT), true, nil),
		Entry("a created head without conditions is success", "", "", errno(syscall.EEXIST), true, nil),
		Entry("another failure is the error", "", "", errno(syscall.EIO), true, op.ErrUnknown),
		Entry("a timeout leaves the entry pending for listing to repair", "", "", errno(syscall.ETIMEDOUT), false, op.ErrRequestTimedOut),
		Entry("If-Match * on a removed head is 412", "*", "", errno(syscall.ENOENT), true, op.ErrPreconditionFailed),
		Entry("If-Match * on a replaced head is success", "*", "", errno(syscall.ECANCELED), true, nil),
		Entry("If-Match etag on a replaced head is the error", `"e"`, "", errno(syscall.ECANCELED), true, op.ErrConcurrentModification),
		Entry("If-None-Match * on a created head is 412", "", "*", errno(syscall.EEXIST), true, op.ErrPreconditionFailed),
		Entry("If-None-Match * on a removed head is success", "", "*", errno(syscall.ENOENT), true, nil),
		Entry("If-None-Match etag on a removed head is NoSuchKey", "", "e", errno(syscall.ENOENT), true, op.ErrNoSuchKey),
	)
})

var _ = Describe("the head write's guard", func() {
	var (
		c       *fakerados.Cluster
		rec     *op.BucketRecord
		key     meta.ObjKey
		headOID string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		headOID = putBucketID + "_k"
	})
	// seedTagless writes a 6 MiB object, which has a manifest, and drops its
	// write tag, as an object from before write tags has none.
	seedTagless := func(ctx context.Context, s *driver.Store) {
		GinkgoHelper()
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(bytes.Repeat([]byte("o"), 6<<20)), op.PutParams{Size: 6 << 20, Tag: "old"})
		Expect(err).NotTo(HaveOccurred())
		delete(c.Object(testDataPool, "", headOID).Xattrs, meta.AttrIDTag)
	}

	DescribeTable("checks the conditions of a head whose tag radosgw would fake, without the guard",
		func(ctx SpecContext, rel denc.Release) {
			s := openPutStore(ctx, c, rel, nil, time.Now())
			seedTagless(ctx, s)
			_, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "new", IfNoneMatch: "*"})
			Expect(err).To(MatchError(op.ErrPreconditionFailed), "v19.2.6's need_guard skips this check (rgw_rados.cc:6493-6495), losing the update")
			_, err = s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "new", IfMatch: md5hex("other")})
			Expect(err).To(MatchError(op.ErrPreconditionFailed), "check_preconditions runs whatever the guard, rgw_rados.cc:3294-3297 at v20.2.4")
			Expect(c.Object(testDataPool, "", headOID).Data).To(HaveLen(4<<20), "the old head stands")
			_, err = s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "new", IfMatch: "*"})
			Expect(err).NotTo(HaveOccurred())
			Expect(c.LastWrite(testDataPool, "", headOID).Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}), "a fake tag is never compared")
		},
		Entry("on Squid, which v20.2.4's check governs too (docs/exclusions.md)", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)

	It("guards an If-None-Match: * write by existence alone", func(ctx SpecContext) {
		s := openPutStore(ctx, c, denc.Tentacle, nil, time.Now())
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "new", IfNoneMatch: "*"})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.LastWrite(testDataPool, "", headOID).Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}),
			"no cmpxattr for If-None-Match: *, the exclusive create guards")
	})
})

var _ = Describe("the head write's placement, atomicity, category and index knobs", func() {
	const metaHeadOID = putBucketID + "__multipart_m.2~id.meta"
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		metaKey meta.ObjKey
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		metaKey = meta.ObjKey{Name: "m.2~id.meta", NS: meta.NSMultipart}
	})
	nonAtomic := func(ctx context.Context, data []byte) driver.HeadWriteForTest {
		GinkgoHelper()
		extra, err := c.Pool(ctx, extraPoolName, "")
		Expect(err).NotTo(HaveOccurred())
		return driver.HeadWriteForTest{
			Key: metaKey, Data: data, Attrs: attrsWithACL(initiator), Create: true, NonAtomic: true,
			Pool: extra, OID: metaHeadOID, Category: rgwcls.CategoryMultiMeta, Size: uint64(len(data)),
		}
	}

	It("writes a non-atomic head with create(false), obj_remove and no idtag, where it is told, hashed by its hash name", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, metaKey, "")
		x.SetHashNameForTest("m")
		_, err := s.WriteMetaForTest(ctx, rec, nonAtomic(ctx, []byte("data")), x)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		w := c.LastWrite(extraPoolName, "", metaHeadOID)
		steps := w.Steps()
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}))
		Expect(execIn(w, 1, "obj_remove", rgwcls.DecodeObjRemoveOp).KeepAttrPrefixes).To(Equal([]string{meta.AttrOLHPrefix}))
		Expect(steps[2]).To(Equal(&radosclient.WriteFullStep{Data: []byte("data")}))
		Expect(names(steps)).NotTo(ContainElement(meta.AttrIDTag))
		Expect(names(steps)).NotTo(ContainElement(meta.AttrTailTag))
		Expect(names(steps)).NotTo(ContainElement(meta.AttrManifest))
		Expect(c.Object(testDataPool, "", metaHeadOID)).To(BeNil(), "the pool override replaces the bucket's data pool")
		shard := indexShardOID(rec, "m")
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "hashed by the hash name, not the key's name")
		en, ok := c.Entry(rookIndexPool, "", shard, "_multipart_m.2~id.meta")
		Expect(ok).To(BeTrue())
		Expect(en.Meta.Category).To(Equal(rgwcls.CategoryMultiMeta))
	})

	It("resets an existing object without consulting it when the write is non-atomic", func(ctx SpecContext) {
		c.Put(extraPoolName, "", metaHeadOID, []byte("old"))
		old := c.Object(extraPoolName, "", metaHeadOID)
		old.Xattrs[meta.AttrIDTag] = []byte("tag\x00")
		old.Omap["part.00000001"] = []byte("p")
		c.ResetCounters()
		_, err := s.WriteMetaForTest(ctx, rec, nonAtomic(ctx, []byte("new")), s.NewIndexOpForTest(rec, metaKey, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Reads(extraPoolName, "", metaHeadOID)).To(BeZero(),
			"no head read: write_meta's only pass assumes no entry, so get_state caches a missing state (rgw_rados.cc:3147, :3429-3436)")
		Expect(c.Writes(extraPoolName, "", metaHeadOID)).To(Equal(1))
		Expect(c.LastWrite(extraPoolName, "", metaHeadOID).Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}), "no cmpxattr guard")
		obj := c.Object(extraPoolName, "", metaHeadOID)
		Expect(obj.Data).To(Equal([]byte("new")))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrIDTag))
		Expect(obj.Omap).To(BeEmpty(), "obj_remove drops the old object's omap with it")
	})

	DescribeTable("refuses a non-atomic write that carries conditions, which radosgw never makes",
		func(ctx SpecContext, ifMatch, ifNoneMatch string) {
			hw := nonAtomic(ctx, []byte("data"))
			hw.IfMatch, hw.IfNoneMatch = ifMatch, ifNoneMatch
			_, err := s.WriteMetaForTest(ctx, rec, hw, s.NewIndexOpForTest(rec, metaKey, ""))
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(c.Writes(extraPoolName, "", metaHeadOID)).To(BeZero())
			Expect(c.Reads(extraPoolName, "", metaHeadOID)).To(BeZero())
			Expect(c.Writes(rookIndexPool, "", indexShardOID(rec, metaKey.Name))).To(BeZero(), "nothing prepared")
		},
		Entry("If-Match", "*", ""),
		Entry("If-None-Match", "", "*"),
	)

	It("removes the listed entries in the same complete", func(ctx SpecContext) {
		shard := indexShardOID(rec, "k")
		for _, name := range []string{"p1", "p2"} {
			x := s.NewIndexOpForTest(rec, meta.ObjKey{Name: name}, "t-"+name)
			x.SetHashNameForTest("k")
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			x.Complete(rgwcls.EntryVer{Pool: 1, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 10, AccountedSize: 10})
		}
		settle(s)
		Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(2))

		key := meta.ObjKey{Name: "k"}
		x := s.NewIndexOpForTest(rec, key, "tag")
		x.SetRemoveObjsForTest([]rgwcls.ObjKey{{Name: "p1"}, {Name: "p2"}, {Name: "gone"}})
		_, err := s.WriteMetaForTest(ctx, rec, driver.HeadWriteForTest{
			Key: key, Tag: "tag", Data: []byte("abc"), Attrs: attrsWithACL(initiator), Create: true, Size: 3, AccountedSize: 3,
		}, x)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		comp := execIn(c.LastWrite(rookIndexPool, "", shard), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.RemoveObjs).To(Equal([]rgwcls.ObjKey{{Name: "p1"}, {Name: "p2"}, {Name: "gone"}}))
		for _, name := range []string{"p1", "p2"} {
			_, ok := c.Entry(rookIndexPool, "", shard, name)
			Expect(ok).To(BeFalse(), name)
		}
		st := c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain]
		Expect(st.NumEntries).To(BeEquivalentTo(1), "k added, p1 and p2 retired; complete_remove_obj skips a missing key")
		Expect(st.TotalSize).To(BeEquivalentTo(3))
	})

	It("sends the remove list with a cancel", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, meta.ObjKey{Name: "k"}, "tag")
		x.SetRemoveObjsForTest([]rgwcls.ObjKey{{Name: "p1"}})
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		x.Cancel()
		settle(s)
		comp := execIn(c.LastWrite(rookIndexPool, "", indexShardOID(rec, "k")), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.Op).To(Equal(rgwcls.OpCancel))
		Expect(comp.RemoveObjs).To(Equal([]rgwcls.ObjKey{{Name: "p1"}}))
	})

	It("adds no bytes to the quota for a completed multipart upload", func(ctx SpecContext) {
		Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed(), "the period's bucket quota primes the bucket cache with the empty bucket")
		_, err := s.WriteMetaForTest(ctx, rec, driver.HeadWriteForTest{
			Key: meta.ObjKey{Name: "k"}, Tag: "tag", Data: []byte("abc"), Attrs: attrsWithACL(initiator), Create: true,
			Size: 3, AccountedSize: 3, CompleteMultipart: true,
		}, s.NewIndexOpForTest(rec, meta.ObjKey{Name: "k"}, "tag"))
		Expect(err).NotTo(HaveOccurred())
		Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{NumObjects: 1}), "update_stats(owner, bucket, 1, 0, orig_size), rgw_rados.cc:3359-3362")
	})
})
