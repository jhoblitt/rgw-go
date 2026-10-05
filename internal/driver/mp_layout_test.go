package driver_test

import (
	"context"
	"maps"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// extraPoolName is the data-extra pool of the zone seedRookZone seeds for the
// store "ceph-objectstore"; its objects are in no namespace.
const extraPoolName = "ceph-objectstore.rgw.buckets.non-ec"

// metaOID is the RADOS name of the meta object of upload id of key in rec's
// bucket.
func metaOID(rec *op.BucketRecord, key, id string) string {
	return rec.Info.Bucket.Marker + "__multipart_" + key + "." + id + ".meta"
}

// metaIndexKey is the index key of that meta object.
func metaIndexKey(key, id string) string { return "_multipart_" + key + "." + id + ".meta" }

// initiator is the fixture bucket's owner, alice, under a name of its own:
// naming put_test.go's alice from a file ordered before ownerbuckets_test.go
// stretches that variable's span over the local alice there, which govet's
// shadow check reports.
var initiator = meta.UserOwner(meta.UserID{ID: "alice"})

// attrsWithACL is an attr set holding owner's default ACL, with the display
// name "Alice".
func attrsWithACL(owner meta.Owner) map[string][]byte {
	return map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(owner, "Alice"))}
}

// seedMeta stores what radosgw's CreateMultipartUpload leaves for upload id
// of key in rec's bucket: in the data-extra pool, the meta object holding the
// Squid encoding of the multipart_upload_info naming dest, with attrs and,
// when v is not nil, v as its cls_version xattr; and on the shard of key, its
// MultiMeta index entry, prepared and completed through the rgw class.
func seedMeta(ctx context.Context, c *fakerados.Cluster, rec *op.BucketRecord, key, id string, dest meta.PlacementRule, attrs map[string][]byte, v *version.ObjVersion) {
	GinkgoHelper()
	oid := metaOID(rec, key, id)
	data := encode(meta.MultipartUploadInfo{DestPlacement: dest})
	c.Put(extraPoolName, "", oid, data)
	obj := c.Object(extraPoolName, "", oid)
	maps.Copy(obj.Xattrs, attrs)
	if v != nil {
		obj.Xattrs[version.XattrName] = encode(*v)
	}
	p, err := c.Pool(ctx, rookIndexPool, "")
	Expect(err).NotTo(HaveOccurred())
	shard := indexShardOID(rec, key)
	idx := rgwcls.ObjKey{Name: metaIndexKey(key, id)}
	w := radosclient.NewWriteOp()
	rgwcls.BucketPrepareOp(w, rgwcls.PrepareOp{Op: rgwcls.OpAdd, Key: idx, Tag: "_seed"}, denc.Squid)
	_, err = p.Write(ctx, shard, w, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred())
	w = radosclient.NewWriteOp()
	rgwcls.BucketCompleteOp(w, rgwcls.CompleteOp{
		Op: rgwcls.OpAdd, Key: idx, Tag: "_seed", Ver: rgwcls.EntryVer{Pool: c.PoolID(extraPoolName), Epoch: 1},
		Meta: rgwcls.DirEntryMeta{Category: rgwcls.CategoryMultiMeta, Size: uint64(len(data)), Mtime: obj.Mtime},
	}, denc.Squid)
	_, err = p.Write(ctx, shard, w, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("multipart layout", func() {
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
	)
	open := func(ctx context.Context) *driver.Store {
		return openPutStore(ctx, c, denc.Squid, nil, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	}
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = open(ctx)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
	})
	withRule := func(rule meta.PlacementRule) *op.BucketRecord {
		r := testBucket(putBucketID, 11)
		r.Info.PlacementRule = rule
		return r
	}
	editPlacement := func(edit func(pi *meta.ZonePlacementInfo)) {
		editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			pi := z.PlacementPools["default-placement"]
			edit(&pi)
			z.PlacementPools["default-placement"] = pi
		})
	}

	It("puts the meta object in the placement's data-extra pool whatever the storage class", func(ctx SpecContext) {
		ref, err := s.MetaRefForTest(ctx, rec, meta.ObjKey{Name: "multipart.bin"}, "2~AkTb")
		Expect(err).NotTo(HaveOccurred())
		Expect(ref.Pool.Name()).To(Equal(extraPoolName))
		Expect(ref.OID).To(Equal(rec.Info.Bucket.Marker + "__multipart_multipart.bin.2~AkTb.meta"))
		Expect(ref.Loc).To(BeEmpty(), "a namespaced key has no locator")
		Expect(ref.Key).To(Equal(meta.ObjKey{Name: "multipart.bin.2~AkTb.meta", NS: meta.NSMultipart}))

		cold := withRule(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"})
		ref, err = s.MetaRefForTest(ctx, cold, meta.ObjKey{Name: "multipart.bin"}, "2~AkTb")
		Expect(err).NotTo(HaveOccurred())
		Expect(ref.Pool.Name()).To(Equal(extraPoolName), "get_data_extra_pool ignores the class")
	})

	It("names a key that starts with an underscore without escaping it, as the namespace already sets it apart", func(ctx SpecContext) {
		ref, err := s.MetaRefForTest(ctx, rec, meta.ObjKey{Name: "_u"}, "2~x")
		Expect(err).NotTo(HaveOccurred())
		Expect(ref.OID).To(Equal(rec.Info.Bucket.Marker + "__multipart__u.2~x.meta"))
		Expect(ref.Loc).To(BeEmpty())
	})

	DescribeTable("resolves the data-extra pool as rgw_get_obj_data_pool does for in_extra_data",
		func(ctx SpecContext, edit func(), bucket func() *op.BucketRecord, want string) {
			if edit != nil {
				edit()
			}
			s2 := open(ctx)
			ref, err := s2.MetaRefForTest(ctx, bucket(), meta.ObjKey{Name: "k"}, "2~x")
			Expect(err).NotTo(HaveOccurred())
			Expect(ref.Pool.Name()).To(Equal(want))
		},
		Entry("STANDARD's data pool when the placement names no extra pool", func() {
			editPlacement(func(pi *meta.ZonePlacementInfo) { pi.DataExtraPool = meta.Pool{} })
		}, func() *op.BucketRecord { return rec }, testDataPool),
		Entry("the zonegroup default placement's extra pool for a rule the zone lacks", nil,
			func() *op.BucketRecord { return withRule(meta.PlacementRule{Name: "gone"}) }, extraPoolName),
		Entry("the zonegroup default placement's extra pool for an empty rule", nil,
			func() *op.BucketRecord { return withRule(meta.PlacementRule{}) }, extraPoolName),
		Entry("the bucket's explicit data-extra pool", nil, func() *op.BucketRecord {
			r := testBucket(putBucketID, 11)
			r.Info.Bucket.ExplicitPlacement = meta.DataPlacement{DataPool: meta.Pool{Name: "exp.data"}, DataExtraPool: meta.Pool{Name: "exp.extra"}}
			return r
		}, "exp.extra"),
		Entry("the bucket's explicit data pool when it names no extra pool", nil, func() *op.BucketRecord {
			r := testBucket(putBucketID, 11)
			r.Info.Bucket.ExplicitPlacement = meta.DataPlacement{DataPool: meta.Pool{Name: "exp.data"}}
			return r
		}, "exp.data"),
	)

	It("answers UnknownError, radosgw's -EIO, when neither placement resolves", func(ctx SpecContext) {
		gone := withRule(meta.PlacementRule{Name: "gone"})
		editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			z.PlacementPools = map[string]meta.ZonePlacementInfo{"other": z.PlacementPools["default-placement"]}
		})
		s2 := open(ctx)
		_, err := s2.MetaRefForTest(ctx, gone, meta.ObjKey{Name: "k"}, "2~x")
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(err.Error()).To(ContainSubstring(`"gone"`))
	})

	It("answers InvalidArgument for an extra pool without a name, as librados refuses to open one", func(ctx SpecContext) {
		editPlacement(func(pi *meta.ZonePlacementInfo) {
			pi.DataExtraPool = meta.Pool{}
			pi.StorageClasses[meta.StorageClassStandard] = meta.ZoneStorageClass{}
		})
		s2 := open(ctx)
		_, err := s2.MetaRefForTest(ctx, rec, meta.ObjKey{Name: "k"}, "2~x")
		Expect(err).To(MatchError(op.ErrInvalidArgument))
	})

	It("reads the meta object in one op composing version, xattrs, stat and the first chunk", func(ctx SpecContext) {
		cold := meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}
		seedMeta(ctx, c, rec, "multipart.bin", "2~AkTb", cold, attrsWithACL(initiator), &version.ObjVersion{Ver: 3, Tag: "t"})
		c.Object(extraPoolName, "", metaOID(rec, "multipart.bin", "2~AkTb")).Xattrs["user.other"] = []byte("x")
		c.ResetCounters()
		st, err := s.ReadMetaForTest(ctx, rec, meta.ObjKey{Name: "multipart.bin"}, "2~AkTb")
		Expect(err).NotTo(HaveOccurred())
		oid := metaOID(rec, "multipart.bin", "2~AkTb")
		Expect(st.Ref.OID).To(Equal(oid))
		Expect(c.Reads(extraPoolName, "", oid)).To(Equal(1))
		steps := c.LastRead(extraPoolName, "", oid).Steps()
		Expect(steps).To(HaveLen(4))
		Expect(steps[0]).To(BeAssignableToTypeOf(&radosclient.ExecStep{}))
		Expect(steps[0]).To(HaveField("Class", "version"))
		Expect(steps[0]).To(HaveField("Method", "read"), "cls_version_read first: RGWObjVersionTracker::prepare_op_for_read")
		Expect(steps[1]).To(BeAssignableToTypeOf(&radosclient.GetXattrsStep{}))
		Expect(steps[2]).To(BeAssignableToTypeOf(&radosclient.StatStep{}))
		Expect(steps[3]).To(BeAssignableToTypeOf(&radosclient.ReadStep{}))
		Expect(steps[3]).To(HaveField("Offset", BeZero()))
		Expect(steps[3]).To(HaveField("Length", BeEquivalentTo(4<<20)), "raw_obj_stat's first chunk, rgw_max_chunk_size")
		Expect(st.Info.DestPlacement).To(Equal(cold))
		Expect(st.Version).To(Equal(version.ObjVersion{Ver: 3, Tag: "t"}))
		Expect(st.Attrs).To(HaveKey(meta.AttrACL))
		Expect(st.Attrs).NotTo(HaveKey("user.other"), "rgw_filter_attrset keeps user.rgw. only")
		Expect(st.Attrs).NotTo(HaveKey(version.XattrName))
		Expect(st.Size).To(BeEquivalentTo(len(encode(meta.MultipartUploadInfo{DestPlacement: cold}))))
		Expect(st.Mtime).To(Equal(c.Object(extraPoolName, "", oid).Mtime))
	})

	It("reads a meta object without a version as version zero", func(ctx SpecContext) {
		seedMeta(ctx, c, rec, "k", "2~x", meta.PlacementRule{Name: "default-placement"}, attrsWithACL(initiator), nil)
		st, err := s.ReadMetaForTest(ctx, rec, meta.ObjKey{Name: "k"}, "2~x")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Version).To(Equal(version.ObjVersion{}))
	})

	DescribeTable("refuses as get_info does",
		func(ctx SpecContext, seed func(), want error) {
			seed()
			_, err := s.ReadMetaForTest(ctx, rec, meta.ObjKey{Name: "k"}, "2~x")
			Expect(err).To(MatchError(want))
		},
		Entry("a missing object is NoSuchUpload", func() {}, op.ErrNoSuchUpload),
		Entry("an empty head is NoSuchUpload, rgw_sal_rados.cc:3700-3702", func() {
			c.Put(extraPoolName, "", metaOID(rec, "k", "2~x"), nil)
		}, op.ErrNoSuchUpload),
		Entry("an upload info that does not decode is radosgw's -EIO, UnknownError", func() {
			c.Put(extraPoolName, "", metaOID(rec, "k", "2~x"), []byte{0xff, 0xff, 0xff})
		}, op.ErrUnknown),
		Entry("a missing extra pool is NoSuchUpload: radosgw would create it and find nothing", func() {
			c.FailPool(extraPoolName)
		}, op.ErrNoSuchUpload),
	)

	It("does not take a regular object named like a meta object for the upload", func(ctx SpecContext) {
		name := "k.2~x.meta"
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: name}, strings.NewReader(""), op.PutParams{Size: 0, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		c.Put(extraPoolName, "", rec.Info.Bucket.Marker+"_"+name, encode(meta.MultipartUploadInfo{}))
		_, err = s.ReadMetaForTest(ctx, rec, meta.ObjKey{Name: "k"}, "2~x")
		Expect(err).To(MatchError(op.ErrNoSuchUpload), "only the multipart namespace holds meta objects")
	})
})
