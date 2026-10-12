package driver_test

import (
	"bytes"
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
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
})
