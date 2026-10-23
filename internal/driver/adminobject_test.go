package driver_test

import (
	"bytes"
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
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("RemoveObjectAdmin over the RADOS store", func() {
	const zgID = "zg-ceph-objectstore"
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
	)
	headOID := func() string { return rec.Info.Bucket.Marker + "_k" }
	// dataObjects is the data pool's objects named under the bucket's
	// marker: the head of k and its tail stripes.
	dataObjects := func() map[string]*fakerados.Object {
		out := map[string]*fakerados.Object{}
		for oid, o := range c.Objects(testDataPool, "") {
			if strings.HasPrefix(oid, rec.Info.Bucket.Marker+"_") {
				out[oid] = o
			}
		}
		return out
	}
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_cache_enabled": "false"},
			time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
		DeferCleanup(s.Close)
		var err error
		rec, err = s.CreateBucket(ctx, op.CreateBucketParams{
			Name: "b", Owner: initiator, Zonegroup: zgID,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     attrsWithACL(initiator), Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		body := bytes.Repeat([]byte("d"), 9<<20)
		_, err = s.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, bytes.NewReader(body),
			op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
	})
	AfterEach(func() { settle(s) })

	// remove sends DELETE /admin/bucket?object=k&bucket=b over the driver's
	// env for an identity holding buckets=*.
	remove := func(ctx context.Context) error {
		admin := meta.NewUserInfo()
		admin.UserID = meta.UserID{ID: "admin"}
		o := op.NewRemoveObjectAdmin()
		o.Bucket, o.Object = "b", "k"
		return op.Run(ctx, o, &op.Request{Env: s.Env(), Identity: op.Identity{
			User: &admin, Owner: meta.UserOwner(admin.UserID), OpMask: op.OpTypeAll, Caps: meta.Caps{"buckets": meta.CapAll},
		}})
	}

	It("removes the object of a bucket whose versioning was never enabled", func(ctx SpecContext) {
		Expect(remove(ctx)).To(Succeed())
		settle(s)
		Expect(c.Object(testDataPool, "", headOID())).To(BeNil())
		_, ok := c.Entry(rookIndexPool, "", indexShardOID(rec, "k"), "k")
		Expect(ok).To(BeFalse(), "the index entry")
		Expect(allGCEntries(c)).To(HaveLen(1), "the tails queued for the GC")
	})

	DescribeTable("answers NotImplemented and removes nothing where radosgw writes a delete marker",
		func(ctx SpecContext, flags uint32) {
			cur, err := s.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			cur.Info.Flags |= flags
			Expect(s.PutBucketInfo(ctx, cur)).To(Succeed())
			head := c.Object(testDataPool, "", headOID())
			Expect(head).NotTo(BeNil())
			for name := range head.Xattrs {
				Expect(name).NotTo(HavePrefix(meta.AttrOLHPrefix), "a plain head, which the delete's olh check lets through")
			}
			before := dataObjects()
			Expect(len(before)).To(BeNumerically(">", 1), "the head and its tails")
			shard := indexShardOID(rec, "k")
			c.ResetCounters()

			Expect(remove(ctx)).To(MatchError(op.ErrNotImplemented),
				"RGWBucket::remove_object keys no instance and passes the bucket's versioning (rgw_bucket.cc:274-289, rgw_sal_rados.cc:2933-2937)")
			settle(s)
			Expect(dataObjects()).To(Equal(before), "the head and its tails")
			Expect(c.Writes(testDataPool, "", headOID())).To(BeZero())
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
			en, ok := c.Entry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeTrue(), "the index entry")
			Expect(en.Exists).To(BeTrue())
			Expect(allGCEntries(c)).To(BeEmpty(), "nothing queued for the GC")
		},
		Entry("on a bucket whose versioning is enabled", uint32(meta.BucketVersioned)),
		Entry("on a bucket whose versioning is suspended, where the marker is the null version", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended)),
		Entry("on an object-lock bucket, which radosgw creates versioned", uint32(meta.BucketVersioned|meta.BucketObjLockEnabled)),
	)

	// The store's half of the removal is the delete RemoveObjectAdmin makes:
	// the plain key with no params, so the bucket's versioning decides, as
	// RadosObject::delete_object passes it, with no marker owner, which it
	// never sets
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2928-L2949 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3766-L3787 at v20.2.4).
	DescribeTable("writes a delete marker with no owner through the store's delete the removal makes on a versioned bucket",
		func(ctx SpecContext, flags uint32, suspended, held bool) {
			cur, err := s.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			cur.Info.Flags |= flags
			Expect(s.PutBucketInfo(ctx, cur)).To(Succeed())
			cur, err = s.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			if held {
				head := c.Object(testDataPool, "", headOID())
				head.Xattrs[meta.AttrObjectRetention] = encode(meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)})
				head.Xattrs[meta.AttrObjectLegalHold] = encode(meta.ObjectLegalHold{Status: "ON"})
			}
			before := dataObjects()

			res, err := s.DeleteObject(ctx, cur, meta.ObjKey{Name: "k"}, op.DeleteParams{})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			listed := c.ListEntries(rookIndexPool, "", indexShardOID(cur, "k"), "k")
			Expect(listed).NotTo(BeEmpty())
			marker := listed[0]
			Expect(marker.Flags&(rgwcls.FlagDeleteMarker|rgwcls.FlagCurrent)).To(Equal(rgwcls.FlagDeleteMarker|rgwcls.FlagCurrent), "the current version")
			Expect(marker.Meta).To(Equal(rgwcls.DirEntryMeta{Mtime: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}),
				"no owner or display name, and the delete time")
			head := c.Object(testDataPool, "", headOID())
			Expect(head).NotTo(BeNil(), "k's OLH head")
			if suspended {
				Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: meta.NullInstance, DeleteMarker: true}))
				Expect(listed).To(HaveLen(1), "the null delete marker in the object's place")
				Expect(head.Data).To(BeEmpty(), "the object's data, which the marker's replay removed")
				Expect(allGCEntries(c)).To(HaveLen(1), "its tails queued for the GC")
				return
			}
			Expect(res.DeleteMarker).To(BeTrue())
			Expect(res.VersionID).NotTo(Or(BeEmpty(), Equal(meta.NullInstance)), "a fresh instance")
			Expect(listed).To(HaveLen(2), "the marker over the object, now the noncurrent null version")
			Expect([]string{marker.Key.Instance, listed[1].Key.Instance}).To(Equal([]string{res.VersionID, ""}))
			Expect(dataObjects()).To(HaveLen(len(before)), "the object's head and tails kept")
			Expect(head.Data).NotTo(BeEmpty(), "the null version's data in the OLH head")
			if held {
				Expect(head.Xattrs).To(SatisfyAll(HaveKey(meta.AttrObjectRetention), HaveKey(meta.AttrObjectLegalHold)),
					"the null version keeps its retention and legal hold")
			}
			Expect(allGCEntries(c)).To(BeEmpty())
		},
		Entry("on a bucket whose versioning is enabled, under a fresh instance", uint32(meta.BucketVersioned), false, false),
		Entry("on a bucket whose versioning is suspended, as the null version that replaces the object",
			uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), true, false),
		Entry("on an object-lock bucket, over an object under compliance retention and a legal hold, neither of which the delete checks",
			uint32(meta.BucketVersioned|meta.BucketObjLockEnabled), false, true),
	)
})
