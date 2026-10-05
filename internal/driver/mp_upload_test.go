package driver_test

import (
	"strings"
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

var _ = Describe("CreateUpload and GetUpload", func() {
	const uploadID = "2~" + putPrefix
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		key   meta.ObjKey
		now   time.Time
		shard string
		oid   string
	)
	BeforeEach(func(ctx SpecContext) {
		now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		clock := now
		c.SetClock(func() time.Time { return clock })
		s = openPutStore(ctx, c, denc.Squid, nil, now)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		shard = indexShardOID(rec, "k")
		oid = metaOID(rec, "k", uploadID)
	})

	It("writes radosgw's meta object and index entry", func(ctx SpecContext) {
		attrs := map[string][]byte{
			meta.AttrACL:              encode(acl.DefaultPolicy(initiator, "Alice")),
			meta.AttrContentType:      []byte("text/plain\x00"),
			meta.AttrMetaPrefix + "k": []byte("v\x00"),
		}
		cold := meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}
		up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: cold, Attrs: attrs})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(up.ID).To(Equal(uploadID), "the v2 prefix and gen_rand_alphanumeric's 31 characters (rgw_sal_rados.cc:3278-3283)")
		Expect(up.Key).To(Equal(key))
		Expect(up.Bucket).To(BeIdenticalTo(rec))
		Expect(up.Owner).To(Equal(initiator))
		Expect(up.OwnerName).To(Equal("Alice"))
		Expect(up.Placement).To(Equal(cold))
		Expect(up.Initiated).To(Equal(now))
		Expect(up.Attrs).To(Equal(attrs))

		obj := c.Object(extraPoolName, "", oid)
		Expect(obj).NotTo(BeNil(), "the meta object lives in the data-extra pool")
		Expect(c.Object(testDataPool, "", oid)).To(BeNil())
		d := denc.NewDecoder(obj.Data)
		Expect(meta.DecodeMultipartUploadInfo(d)).To(Equal(meta.MultipartUploadInfo{DestPlacement: cold}))
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(obj.Data).To(Equal(encode(meta.MultipartUploadInfo{DestPlacement: cold})), "Squid's version 2")
		Expect(obj.Xattrs).To(HaveKeyWithValue(meta.AttrACL, attrs[meta.AttrACL]))
		Expect(obj.Xattrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")))
		Expect(obj.Xattrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"k", []byte("v\x00")))
		Expect(obj.Xattrs).To(HaveKey(meta.AttrPGVer))
		Expect(obj.Xattrs).To(HaveKeyWithValue(meta.AttrSourceZone, []byte{0x39, 0x30, 0, 0}))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrIDTag), "RadosMultipartUpload::init never sets the object atomic")
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrTailTag))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrManifest))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrStorageClass))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrETag))
		Expect(obj.Mtime).To(Equal(now))

		w := c.LastWrite(extraPoolName, "", oid)
		steps := w.Steps()
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}), "PUT_OBJ_EXCL is consulted nowhere: create(false)")
		Expect(execIn(w, 1, "obj_remove", rgwcls.DecodeObjRemoveOp).KeepAttrPrefixes).To(Equal([]string{meta.AttrOLHPrefix}))
		Expect(steps[2]).To(Equal(&radosclient.WriteFullStep{Data: obj.Data}))
		Expect(names(steps)).To(Equal([]string{meta.AttrACL, meta.AttrContentType, meta.AttrMetaPrefix + "k", meta.AttrSourceZone}))
		mt, ok := w.Mtime()
		Expect([]any{mt, ok}).To(Equal([]any{now, true}))
		Expect(c.Writes(extraPoolName, "", oid)).To(Equal(1), "the non-atomic write has no guarded second pass")

		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "prepare and complete on the shard of the upload key")
		writes := c.WritesTo(rookIndexPool, "", shard)
		prep := execIn(writes[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect(prep.Key).To(Equal(rgwcls.ObjKey{Name: metaIndexKey("k", uploadID)}))
		Expect(prep.Locator).To(BeEmpty())
		Expect(prep.Tag).To(Equal("_"+putPrefix), "a random index tag: the write is non-atomic, so it has no write tag")
		comp := execIn(writes[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(comp.Tag).To(Equal(prep.Tag))
		Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: c.PoolID(extraPoolName), Epoch: obj.Version}))

		en, ok := c.Entry(rookIndexPool, "", shard, metaIndexKey("k", uploadID))
		Expect(ok).To(BeTrue())
		Expect(en.Exists).To(BeTrue())
		Expect(en.Meta.Category).To(Equal(rgwcls.CategoryMultiMeta))
		Expect(en.Meta.Size).To(BeEquivalentTo(len(obj.Data)))
		Expect(en.Meta.AccountedSize).To(BeZero())
		Expect(en.Meta.Mtime).To(Equal(now))
		Expect(en.Meta.Owner).To(Equal("alice"))
		Expect(en.Meta.OwnerDisplayName).To(Equal("Alice"))
		Expect(en.Meta.ContentType).To(Equal("text/plain"))
		Expect(en.Meta.ETag).To(BeEmpty())
		Expect(en.Meta.StorageClass).To(BeEmpty())
		Expect(en.Tag).To(Equal("_" + putPrefix))
		hdr := c.Header(rookIndexPool, "", shard)
		Expect(hdr.Stats[rgwcls.CategoryMultiMeta].NumEntries).To(BeEquivalentTo(1))
		Expect(hdr.Stats[rgwcls.CategoryMultiMeta].TotalSize).To(BeZero())
		Expect(hdr.Stats).NotTo(HaveKey(rgwcls.CategoryMain))
		Expect(s.StatsForTest()).To(Equal(driver.QuotaDeltas{Objs: 1}), "update_stats(owner, bucket, 1, 0, 0)")
	})

	It("logs neither its prepare nor its complete, as init passes log_op false, while a PUT in the same zone logs both", func(ctx SpecContext) {
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			zg := p.PeriodMap.ZoneGroups["zg-ceph-objectstore"]
			z := zg.Zones[putZoneID]
			z.LogData = true
			zg.Zones[putZoneID] = z
			p.PeriodMap.ZoneGroups["zg-ceph-objectstore"] = zg
		})
		s2 := openPutStore(ctx, c, denc.Squid, nil, now)
		Expect(s2.Zone().LogData).To(BeTrue())
		_, err := s2.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		settle(s2)
		writes := c.WritesTo(rookIndexPool, "", shard)
		Expect(writes).To(HaveLen(2))
		Expect(execIn(writes[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp).LogOp).To(BeFalse())
		Expect(execIn(writes[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).LogOp).To(BeFalse())

		_, err = s2.PutObject(ctx, rec, key, strings.NewReader("x"), op.PutParams{Size: 1, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		settle(s2)
		writes = c.WritesTo(rookIndexPool, "", shard)
		Expect(writes).To(HaveLen(4))
		Expect(execIn(writes[2], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp).LogOp).To(BeTrue())
		Expect(execIn(writes[3], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).LogOp).To(BeTrue())
	})

	It("reads the upload back with the ACL owner, the mtime and the placement", func(ctx SpecContext) {
		created, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		up, err := s.GetUpload(ctx, rec, key, created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(up.ID).To(Equal(created.ID))
		Expect(up.Bucket).To(BeIdenticalTo(rec))
		Expect(up.Key).To(Equal(key))
		Expect(up.Owner).To(Equal(initiator))
		Expect(up.OwnerName).To(Equal("Alice"))
		Expect(up.Initiated).To(Equal(created.Initiated))
		Expect(up.Placement).To(Equal(meta.PlacementRule{Name: "default-placement"}))
		Expect(up.Attrs).To(HaveKey(meta.AttrACL))
		Expect(up.Attrs).To(HaveKey(meta.AttrSourceZone), "get_obj_attrs gives every user.rgw. attr")
	})

	It("takes the owner from the meta object's ACL", func(ctx SpecContext) {
		bob := meta.UserOwner(meta.UserID{Tenant: "t", ID: "bob"})
		seedMeta(ctx, c, rec, "k", "2~x", meta.PlacementRule{Name: "default-placement"},
			map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(bob, "Bob"))}, nil)
		up, err := s.GetUpload(ctx, rec, key, "2~x")
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Owner).To(Equal(bob))
		Expect(up.OwnerName).To(Equal("Bob"))
	})

	It("answers NoSuchUpload for an unknown id", func(ctx SpecContext) {
		_, err := s.GetUpload(ctx, rec, key, "2~nope")
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
	})

	It("encodes Tentacle's version 4 upload info with no checksum", func(ctx SpecContext) {
		s2 := openPutStore(ctx, c, denc.Tentacle, nil, now)
		up, err := s2.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		data := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID)).Data
		Expect(data[0]).To(Equal(byte(4)))
		Expect(data[len(data)-4:]).To(Equal([]byte{0, 0, 0, 0}), "cksum_type none and FLAG_CKSUM_NONE")
		got, err := s2.GetUpload(ctx, rec, key, up.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Placement).To(Equal(meta.PlacementRule{Name: "default-placement"}))
	})

	It("writes nothing when no extra pool resolves", func(ctx SpecContext) {
		editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			z.PlacementPools = map[string]meta.ZonePlacementInfo{"other": z.PlacementPools["default-placement"]}
		})
		s2 := openPutStore(ctx, c, denc.Squid, nil, now)
		gone := testBucket(putBucketID, 11)
		gone.Info.PlacementRule = meta.PlacementRule{Name: "gone"}
		_, err := s2.CreateUpload(ctx, gone, key, op.UploadParams{Owner: initiator, Attrs: attrsWithACL(initiator)})
		Expect(err).To(MatchError(op.ErrUnknown), "get_obj_head_ref's -EIO")
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
	})

	It("answers NoSuchUpload, naming the pool, when the data-extra pool is missing, which it does not create", func(ctx SpecContext) {
		c.FailPool(extraPoolName)
		_, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, Attrs: attrsWithACL(initiator)})
		Expect(err).To(MatchError(op.ErrNoSuchUpload), "docs/exclusions.md, \"No bootstrap\": radosgw creates the pool and the upload")
		Expect(err.Error()).To(ContainSubstring(extraPoolName))
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
	})
})
