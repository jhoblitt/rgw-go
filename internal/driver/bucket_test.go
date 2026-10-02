package driver_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// seedInstance stores info as a bucket instance object with attrs at
// version v.
func seedInstance(c *fakerados.Cluster, info meta.BucketInfo, attrs map[string][]byte, v meta.ObjVersion) {
	seedVersioned(c, info.Bucket.InstanceOID(), encode(info), attrs, v)
}

// seedBucket stores a bucket in the zone seedRookZone seeds for the store
// "ceph-objectstore" as radosgw's create_bucket does: the entry point naming
// info's instance, at epV and without attrs (put_linked_bucket_info,
// driver/rados/rgw_rados.cc:9039-9075 at v19.2.6), and the instance holding
// info and attrs at instV.
func seedBucket(c *fakerados.Cluster, info meta.BucketInfo, attrs map[string][]byte, epV, instV meta.ObjVersion) {
	ep := meta.BucketEntryPoint{Bucket: info.Bucket, Owner: info.Owner, CreationTime: info.CreationTime, Linked: true}
	seedVersioned(c, info.Bucket.EntryPointOID(), encode(ep), nil, epV)
	seedInstance(c, info, attrs, instV)
}

// encodeOldEntryPoint is an entry point as radosgw wrote it before version
// 8: an RGWBucketInfo encoding of a struct version below 8, which
// RGWBucketEntryPoint::decode hands whole to RGWBucketInfo::decode
// (rgw_common.h:1134-1142 at v19.2.6, :1177-1185 at v20.2.4). info's current
// encoding relabelled version 7 is one: a version-7 decoder reads the fields
// up to the placement rule and skips the rest by the struct's length.
func encodeOldEntryPoint(info meta.BucketInfo) []byte {
	b := encode(info)
	b[0] = 7
	return b
}

var _ = Describe("BucketStore: entry point and instance", func() {
	const instOID = ".bucket.meta.plain:zone-ceph-objectstore.4155.1"
	var (
		c *fakerados.Cluster
		s *driver.Store
	)
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		// The watches outlive this node, so they run on a context its end
		// does not cancel.
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan error, 1)
		go func() { done <- s.Run(runCtx) }()
		DeferCleanup(func() {
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive())
		})
		// The cache serves once every control watch is up; from then a
		// lookup of a missing bucket reads RADOS once and caches its absence.
		Eventually(func(g Gomega) {
			_, err := s.GetBucket(ctx, "", "probe")
			g.Expect(err).To(MatchError(op.ErrNoSuchBucket))
			n := c.Reads(rookMetaPool, rookRoot, "probe")
			_, err = s.GetBucket(ctx, "", "probe")
			g.Expect(err).To(MatchError(op.ErrNoSuchBucket))
			g.Expect(c.Reads(rookMetaPool, rookRoot, "probe")).To(Equal(n), "the second lookup is served from the cache")
		}).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Succeed())
	})
	plain := func(tenant string) meta.BucketInfo {
		info := meta.NewBucketInfo()
		info.Bucket = meta.BucketID{Tenant: tenant, Name: "plain", Marker: "zone-ceph-objectstore.4155.1", ID: "zone-ceph-objectstore.4155.1"}
		info.Owner = meta.UserOwner(meta.UserID{Tenant: tenant, ID: "alice"})
		info.Zonegroup, info.PlacementRule = "zg-ceph-objectstore", meta.PlacementRule{Name: "default-placement"}
		info.CreationTime = meta.Time{Time: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
		return info
	}

	Describe("reading", func() {
		It("reads a radosgw-written bucket: the entry point, then the instance it names and its attrs", func(ctx SpecContext) {
			seedBucket(c, plain(""), map[string][]byte{"user.rgw.acl": {9}}, meta.ObjVersion{Ver: 1, Tag: "ep"}, meta.ObjVersion{Ver: 4, Tag: "bi"})
			instMtime := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
			c.Object(rookMetaPool, rookRoot, "plain").Mtime = instMtime.Add(-time.Hour)
			c.Object(rookMetaPool, rookRoot, instOID).Mtime = instMtime
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.EntryPoint.Linked).To(BeTrue())
			Expect(rec.EntryPoint.Bucket).To(Equal(plain("").Bucket))
			Expect(rec.Info.Bucket.ID).To(Equal("zone-ceph-objectstore.4155.1"))
			Expect(rec.Attrs).To(HaveKeyWithValue("user.rgw.acl", []byte{9}), "the instance's attrs, rgw_bucket.cc:3109-3118")
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 4, Tag: "bi"}))
			Expect(rec.EPVersion).To(Equal(meta.ObjVersion{Ver: 1, Tag: "ep"}), "load_bucket's bucket_version, rgw_sal_rados.cc:615-634")
			Expect(rec.Mtime).To(BeTemporally("==", instMtime), "the instance's mtime")
			Expect(c.Reads(rookMetaPool, rookRoot, "plain")).To(Equal(1))
			_, err = s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Reads(rookMetaPool, rookRoot, "plain")).To(Equal(1), "served from the cache")
			Expect(c.Reads(rookMetaPool, rookRoot, instOID)).To(Equal(1))
		})

		It("keeps only the user.rgw. attrs, so a rewrite leaves the version the class set", func(ctx SpecContext) {
			seedBucket(c, plain(""), map[string][]byte{"user.rgw.acl": {9}, "ceph.other": {1}}, meta.ObjVersion{Ver: 1, Tag: "ep"}, meta.ObjVersion{Ver: 4, Tag: "bi"})
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Attrs).To(Equal(map[string][]byte{"user.rgw.acl": {9}}), "raw_attrs is false, svc_sys_obj_core.cc:196-197")
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
			Expect(storedVersion(c, instOID)).To(Equal(meta.ObjVersion{Ver: 5, Tag: "bi"}), "the attrs set after the class's inc leave its version")
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 5, Tag: "bi"}))
		})

		It("keeps only the user.rgw. attrs of the entry point too", func(ctx SpecContext) {
			info := plain("")
			ep := meta.BucketEntryPoint{Bucket: info.Bucket, Owner: info.Owner, Linked: true}
			seedVersioned(c, "plain", encode(ep), map[string][]byte{"user.rgw.acl": {9}, "ceph.other": {1}}, meta.ObjVersion{Ver: 2, Tag: "ep"})
			got, attrs, v, err := driver.ReadEntryPoint(s, ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Bucket).To(Equal(info.Bucket))
			Expect(attrs).To(Equal(map[string][]byte{"user.rgw.acl": {9}}), "raw_attrs is false, svc_bucket_sobj.cc:221-224")
			Expect(v).To(Equal(meta.ObjVersion{Ver: 2, Tag: "ep"}))
		})

		It("names tenanted buckets the way radosgw does", func(ctx SpecContext) {
			seedBucket(c, plain("t1"), nil, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 1, Tag: "b"})
			Expect(c.Object(rookMetaPool, rookRoot, "t1/plain")).NotTo(BeNil())
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.t1:plain:zone-ceph-objectstore.4155.1")).NotTo(BeNil())
			rec, err := s.GetBucket(ctx, "t1", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.Bucket.Tenant).To(Equal("t1"))
			_, err = s.GetBucket(ctx, "", "plain")
			Expect(err).To(MatchError(op.ErrNoSuchBucket), "the tenant is part of the name")
		})

		It("is NoSuchBucket for a missing entry point, and caches the negative lookup", func(ctx SpecContext) {
			_, err := s.GetBucket(ctx, "", "nope")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			_, err = s.GetBucket(ctx, "", "nope")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			Expect(c.Reads(rookMetaPool, rookRoot, "nope")).To(Equal(1))
		})

		It("is NoSuchBucket for an entry point whose instance is missing", func(ctx SpecContext) {
			info := plain("")
			ep := meta.BucketEntryPoint{Bucket: info.Bucket, Owner: info.Owner, Linked: true}
			seedVersioned(c, "plain", encode(ep), nil, meta.ObjVersion{Ver: 1, Tag: "e"})
			_, err := s.GetBucket(ctx, "", "plain")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			Expect(c.Reads(rookMetaPool, rookRoot, instOID)).To(Equal(1))
		})

		It("answers a record that does not decode with radosgw's -EIO, which S3 renders as UnknownError", func(ctx SpecContext) {
			c.Put(rookMetaPool, rookRoot, "broken", []byte{1})
			_, err := s.GetBucket(ctx, "", "broken")
			Expect(err).To(MatchError(op.ErrUnknown), "read_bucket_entrypoint_info, svc_bucket_sobj.cc:229-235")
			info := plain("")
			ep := meta.BucketEntryPoint{Bucket: info.Bucket, Owner: info.Owner, Linked: true}
			seedVersioned(c, "plain", encode(ep), nil, meta.ObjVersion{Ver: 1, Tag: "e"})
			seedVersioned(c, instOID, []byte{1}, nil, meta.ObjVersion{Ver: 1, Tag: "b"})
			_, err = s.GetBucket(ctx, "", "plain")
			Expect(err).To(MatchError(op.ErrUnknown), "do_read_bucket_instance_info, :363-369")
			_, err = s.GetBucketInstance(ctx, info.Bucket)
			Expect(err).To(MatchError(op.ErrUnknown))
		})

		It("is NoSuchBucket for an entry point from before version 8, whose instance radosgw's load_bucket looks up under the empty bucket", func(ctx SpecContext) {
			info := plain("")
			c.Put(rookMetaPool, rookRoot, "plain", encodeOldEntryPoint(info))
			_, err := s.GetBucket(ctx, "", "plain")
			Expect(err).To(MatchError(op.ErrNoSuchBucket), "RGWBucketCtl::read_bucket_info has no has_bucket_info branch, rgw_bucket.cc:3085-3129")
			Expect(c.Reads(rookMetaPool, rookRoot, ".bucket.meta.")).To(Equal(1), "get_bi_meta_key of the empty bucket is the empty key")
		})

		It("reads an instance directly by id, without the entry point", func(ctx SpecContext) {
			info := plain("")
			seedBucket(c, info, nil, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 2, Tag: "b"})
			rec, err := s.GetBucketInstance(ctx, info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: "b"}))
			Expect(rec.EPVersion).To(BeZero())
			Expect(rec.EntryPoint.Bucket).To(Equal(info.Bucket))
			Expect(c.Reads(rookMetaPool, rookRoot, "plain")).To(BeZero(), "load_bucket with a bucket id, rgw_sal_rados.cc:623-629")
			_, err = s.GetBucketInstance(ctx, meta.BucketID{Name: "plain", ID: "other"})
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
	})

	Describe("writing", func() {
		It("writes the instance under its version and reports a lost race", func(ctx SpecContext) {
			seedBucket(c, plain(""), nil, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 1, Tag: "b"})
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			stale := *rec
			rec.Info.Quota = meta.Quota{MaxObjects: 5, MaxSize: -1, Enabled: true}
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: "b"}), "cls_version inc")
			obj := c.Object(rookMetaPool, rookRoot, instOID)
			Expect(obj.Data).To(Equal(encode(rec.Info)))
			Expect(rec.Mtime).To(BeTemporally("~", time.Now(), time.Minute))
			Expect(obj.Mtime).To(BeTemporally("==", rec.Mtime))
			Expect(s.PutBucketInfo(ctx, &stale)).To(MatchError(op.ErrConcurrentModification))
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Quota.MaxObjects).To(BeEquivalentTo(5))
			Expect(got.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: "b"}))
		})

		It("merges attrs into a full instance write, setting then removing", func(ctx SpecContext) {
			seedBucket(c, plain(""), map[string][]byte{"user.rgw.acl": {1}, "user.rgw.x-amz-tagging": {2}}, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 1, Tag: "b"})
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			set := map[string][]byte{"user.rgw.iam-policy": []byte("p"), "user.rgw.cors": {3}}
			Expect(s.PutBucketAttrs(ctx, rec, set, []string{"user.rgw.x-amz-tagging", "user.rgw.cors"})).To(Succeed())
			obj := c.Object(rookMetaPool, rookRoot, instOID)
			Expect(obj.Xattrs).To(HaveKeyWithValue("user.rgw.iam-policy", []byte("p")))
			Expect(obj.Xattrs).To(HaveKeyWithValue("user.rgw.acl", []byte{1}))
			Expect(obj.Xattrs).NotTo(HaveKey("user.rgw.x-amz-tagging"))
			Expect(obj.Xattrs).NotTo(HaveKey("user.rgw.cors"), "a name both set and removed is removed")
			Expect(obj.Data).To(Equal(encode(rec.Info)), "the whole instance is rewritten, rgw_bucket.cc:3295-3302")
			Expect(rec.Attrs).To(Equal(map[string][]byte{"user.rgw.acl": {1}, "user.rgw.iam-policy": []byte("p")}))
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: "b"}))
		})

		It("leaves the record's attrs alone when the attrs write loses a race", func(ctx SpecContext) {
			seedBucket(c, plain(""), map[string][]byte{"user.rgw.acl": {1}}, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 1, Tag: "b"})
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			stale := *rec
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
			Expect(s.PutBucketAttrs(ctx, &stale, map[string][]byte{"user.rgw.iam-policy": []byte("p")}, []string{"user.rgw.acl"})).
				To(MatchError(op.ErrConcurrentModification))
			Expect(stale.Attrs).To(Equal(map[string][]byte{"user.rgw.acl": {1}}))
			Expect(stale.Version).To(Equal(meta.ObjVersion{Ver: 1, Tag: "b"}))
		})

		It("reads the entry point before setting attrs on an instance without has_instance_obj, as set_bucket_instance_attrs does", func(ctx SpecContext) {
			info := plain("")
			seedInstance(c, info, map[string][]byte{"user.rgw.acl": {1}}, meta.ObjVersion{Ver: 1, Tag: "b"})
			rec, err := s.GetBucketInstance(ctx, info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{"user.rgw.iam-policy": []byte("p")}, nil)).
				To(MatchError(op.ErrNoSuchBucket), "convert_old_bucket_info's read, rgw_bucket.cc:3251-3257")
			Expect(c.Object(rookMetaPool, rookRoot, instOID).Xattrs).NotTo(HaveKey("user.rgw.iam-policy"))
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed(), "put_info reads no entry point, rgw_sal_rados.cc:753-757")
		})

		It("only removes attrs without reading the entry point, as radosgw removes them through put_info", func(ctx SpecContext) {
			info := plain("")
			seedInstance(c, info, map[string][]byte{"user.rgw.acl": {1}, "user.rgw.x-amz-tagging": {2}}, meta.ObjVersion{Ver: 1, Tag: "b"})
			c.Put(rookMetaPool, rookRoot, "plain", encodeOldEntryPoint(info))
			rec, err := s.GetBucketInstance(ctx, info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.PutBucketAttrs(ctx, rec, nil, []string{"user.rgw.x-amz-tagging"})).
				To(Succeed(), "RGWDeleteBucketTags erases the attr and calls put_info, rgw_op.cc:1232-1235")
			Expect(c.Reads(rookMetaPool, rookRoot, "plain")).To(BeZero())
			obj := c.Object(rookMetaPool, rookRoot, instOID)
			Expect(obj.Xattrs).NotTo(HaveKey("user.rgw.x-amz-tagging"))
			Expect(obj.Xattrs).To(HaveKey("user.rgw.acl"))
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: "b"}))
		})

		It("writes the attrs of an instance with has_instance_obj without reading the entry point", func(ctx SpecContext) {
			info := plain("")
			info.HasInstanceObj = true
			seedInstance(c, info, nil, meta.ObjVersion{Ver: 1, Tag: "b"})
			rec, err := s.GetBucketInstance(ctx, info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{"user.rgw.iam-policy": []byte("p")}, nil)).To(Succeed())
			Expect(c.Reads(rookMetaPool, rookRoot, "plain")).To(BeZero(), "rgw_bucket.cc:3286")
			Expect(c.Object(rookMetaPool, rookRoot, instOID).Xattrs).To(HaveKey("user.rgw.iam-policy"))
		})

		It("refuses an attrs write to a bucket whose entry point is from before version 8, which radosgw would convert", func(ctx SpecContext) {
			info := plain("")
			seedInstance(c, info, nil, meta.ObjVersion{Ver: 1, Tag: "b"})
			c.Put(rookMetaPool, rookRoot, "plain", encodeOldEntryPoint(info))
			rec, err := s.GetBucketInstance(ctx, info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			err = s.PutBucketAttrs(ctx, rec, map[string][]byte{"user.rgw.iam-policy": []byte("p")}, nil)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err).To(MatchError(ContainSubstring("plain")))
			Expect(c.Object(rookMetaPool, rookRoot, instOID).Xattrs).NotTo(HaveKey("user.rgw.iam-policy"))
		})
	})

	Describe("the object seams", func() {
		It("writes an entry point at the release, and refuses an exclusive write over one as BucketAlreadyExists", func(ctx SpecContext) {
			info := plain("")
			ep := meta.BucketEntryPoint{Bucket: info.Bucket, Owner: info.Owner, CreationTime: info.CreationTime, Linked: true}
			v, err := driver.WriteEntryPoint(s, ctx, ep, true, meta.ObjVersion{}, meta.ObjVersion{Ver: 1, Tag: "e"})
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(meta.ObjVersion{Ver: 1, Tag: "e"}))
			Expect(c.Object(rookMetaPool, rookRoot, "plain").Data).To(Equal(encode(ep)))
			Expect(storedVersion(c, "plain")).To(Equal(meta.ObjVersion{Ver: 1, Tag: "e"}))
			_, err = driver.WriteEntryPoint(s, ctx, ep, true, meta.ObjVersion{}, meta.ObjVersion{Ver: 1, Tag: "x"})
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			_, err = driver.WriteEntryPoint(s, ctx, ep, false, meta.ObjVersion{Ver: 1, Tag: "x"}, meta.ObjVersion{})
			Expect(err).To(MatchError(op.ErrConcurrentModification))
			v, err = driver.WriteEntryPoint(s, ctx, ep, false, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{})
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(meta.ObjVersion{Ver: 2, Tag: "e"}))
		})

		It("removes an entry point under its version, and a missing one is NoSuchBucket", func(ctx SpecContext) {
			seedBucket(c, plain(""), nil, meta.ObjVersion{Ver: 3, Tag: "e"}, meta.ObjVersion{Ver: 1, Tag: "b"})
			Expect(driver.RemoveEntryPoint(s, ctx, "", "plain", meta.ObjVersion{Ver: 2, Tag: "e"})).To(MatchError(op.ErrConcurrentModification))
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil())
			Expect(driver.RemoveEntryPoint(s, ctx, "", "plain", meta.ObjVersion{Ver: 3, Tag: "e"})).To(Succeed())
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
			Expect(driver.RemoveEntryPoint(s, ctx, "", "plain", meta.ObjVersion{})).To(MatchError(op.ErrNoSuchBucket))
		})

		It("takes an exclusive write over an existing instance for success and leaves the instance", func(ctx SpecContext) {
			info := plain("")
			seedInstance(c, info, nil, meta.ObjVersion{Ver: 1, Tag: "b"})
			changed := info
			changed.Zonegroup = "elsewhere"
			Expect(driver.WriteInstance(s, ctx, changed, true, meta.ObjVersion{Ver: 1, Tag: "n"})).To(Succeed(), "store_bucket_instance_info, svc_bucket_sobj.cc:548-558")
			Expect(c.Object(rookMetaPool, rookRoot, instOID).Data).To(Equal(encode(info)))
			Expect(storedVersion(c, instOID)).To(Equal(meta.ObjVersion{Ver: 1, Tag: "b"}))
		})

		It("takes the removal of a missing instance for success", func(ctx SpecContext) {
			info := plain("")
			seedInstance(c, info, nil, meta.ObjVersion{Ver: 1, Tag: "b"})
			Expect(driver.RemoveInstance(s, ctx, info.Bucket)).To(Succeed())
			Expect(c.Object(rookMetaPool, rookRoot, instOID)).To(BeNil())
			Expect(driver.RemoveInstance(s, ctx, info.Bucket)).To(Succeed(), "remove_bucket_instance_info, svc_bucket_sobj.cc:577-580")
		})
	})
})
