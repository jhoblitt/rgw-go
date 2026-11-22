package driver_test

import (
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// This pins docs/differences.md, "A purging bucket removal checks the index
// is empty at the end": the removal a purge ends with skips the index count,
// not the listing's check, so an object written after the purge listed the
// bucket fails the removal with BucketNotEmpty, where radosgw removes the
// bucket over it.
var _ = Describe("the removal a purge ends with", func() {
	It("refuses a bucket whose listing shows an object, keeping the bucket", func(ctx SpecContext) {
		const indexPool = "ceph-objectstore.rgw.buckets.index"
		c := newIndexCluster()
		s, err := driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		rec, err := s.CreateBucket(ctx, op.CreateBucketParams{
			Name: "plain", Owner: aliceOwner, Zonegroup: "zg-ceph-objectstore",
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{meta.AttrACL: {1}}, Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		shard := ".dir." + rec.Info.Bucket.ID + "." + strconv.FormatUint(uint64(shardOf(rec, "late")), 10)
		seedIndexEntry(c, indexPool, shard, entry("late", 1))

		Expect(s.DeleteBucketPurged(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty))
		Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil(), "the entry point stays")
		_, err = s.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred(), "the bucket stays for a retry to purge")
	})
})
