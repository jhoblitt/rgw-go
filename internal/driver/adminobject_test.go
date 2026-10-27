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

	// remove sends DELETE /admin/bucket?object=name&bucket=b over the
	// driver's env for an identity holding buckets=*.
	remove := func(ctx context.Context, name string) error {
		admin := meta.NewUserInfo()
		admin.UserID = meta.UserID{ID: "admin"}
		o := op.NewRemoveObjectAdmin()
		o.Bucket, o.Object = "b", name
		return op.Run(ctx, o, &op.Request{Env: s.Env(), Identity: op.Identity{
			User: &admin, Owner: meta.UserOwner(admin.UserID), OpMask: op.OpTypeAll, Caps: meta.Caps{"buckets": meta.CapAll},
		}})
	}
	// versioned sets flags on b and returns its record as loaded again.
	versioned := func(ctx context.Context, flags uint32) *op.BucketRecord {
		GinkgoHelper()
		cur, err := s.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		cur.Info.Flags |= flags
		Expect(s.PutBucketInfo(ctx, cur)).To(Succeed())
		cur, err = s.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		return cur
	}

	It("removes the object of a bucket whose versioning was never enabled", func(ctx SpecContext) {
		Expect(remove(ctx, "k")).To(Succeed())
		settle(s)
		Expect(c.Object(testDataPool, "", headOID())).To(BeNil())
		_, ok := c.Entry(rookIndexPool, "", indexShardOID(rec, "k"), "k")
		Expect(ok).To(BeFalse(), "the index entry")
		Expect(allGCEntries(c)).To(HaveLen(1), "the tails queued for the GC")
	})

	// The removal deletes the plain key with no params, so the bucket's
	// versioning decides, as RadosObject::delete_object passes it, with no
	// marker owner, which it never sets, and reads no retention or legal hold
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2928-L2949 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3766-L3787 at v20.2.4).
	DescribeTable("writes a delete marker with no owner on a versioned bucket, as radosgw's removal of the plain key does",
		func(ctx SpecContext, flags uint32, suspended, held bool) {
			cur := versioned(ctx, flags)
			if held {
				head := c.Object(testDataPool, "", headOID())
				head.Xattrs[meta.AttrObjectRetention] = encode(meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)})
				head.Xattrs[meta.AttrObjectLegalHold] = encode(meta.ObjectLegalHold{Status: "ON"})
			}
			before := dataObjects()

			Expect(remove(ctx, "k")).To(Succeed())
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
				Expect(listed).To(HaveLen(1), "the null delete marker in the object's place")
				Expect(marker.Key.Instance).To(BeEmpty(), "the null version")
				Expect(head.Data).To(BeEmpty(), "the object's data, which the marker's replay removed")
				Expect(allGCEntries(c)).To(HaveLen(1), "its tails queued for the GC")
				return
			}
			Expect(listed).To(HaveLen(2), "the marker over the object, now the noncurrent null version")
			Expect(marker.Key.Instance).NotTo(Or(BeEmpty(), Equal(meta.NullInstance)), "a fresh instance")
			Expect(listed[1].Key.Instance).To(BeEmpty())
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
		Entry("on an object-lock bucket, over an object under compliance retention and a legal hold, neither of which the removal checks",
			uint32(meta.BucketVersioned|meta.BucketObjLockEnabled), false, true),
	)

	It("writes a delete marker for a key with no object on a versioned bucket, as radosgw's versioned delete answers no ENOENT", func(ctx SpecContext) {
		cur := versioned(ctx, meta.BucketVersioned)
		Expect(remove(ctx, "missing")).To(Succeed())
		settle(s)
		listed := c.ListEntries(rookIndexPool, "", indexShardOID(cur, "missing"), "missing")
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].Flags & rgwcls.FlagDeleteMarker).NotTo(BeZero())
	})
})
