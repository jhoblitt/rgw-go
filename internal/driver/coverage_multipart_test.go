package driver_test

import (
	"bytes"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// docs/differences.md, "A part whose head write lost a race is not
// registered": radosgw answers a part head write that fails with
// ECANCELED, ENOENT or EEXIST as a lost race and success, registers the
// part and then removes its objects; rgw-go removes them and fails the
// UploadPart with 500 InternalError without registering it.
var _ = Describe("a part whose head write lost a race, as docs/differences.md records it", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		up      *op.Upload
		prefix  string
		metaObj string
	)
	BeforeEach(func(ctx SpecContext) {
		mtime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		c.SetClock(func() time.Time { return mtime })
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		var err error
		up, err = s.CreateUpload(ctx, rec, meta.ObjKey{Name: "k"}, op.UploadParams{
			Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator),
		})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		prefix = "k." + up.ID
		metaObj = metaOID(rec, "k", up.ID)
	})

	DescribeTable("removes the part and registers nothing, answering InternalError",
		func(ctx SpecContext, errno syscall.Errno) {
			head := partHeadOID(prefix, 1)
			// the part head's first write is its stripe's exclusive create;
			// the head write after it, write_meta's, is the one refused
			c.BeforeWrite(testDataPool, "", head, func(o *fakerados.Object) {
				if o != nil {
					c.FailNextWrite(testDataPool, "", head, errno)
					c.BeforeWrite(testDataPool, "", head, nil)
				}
			})
			attrs := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(initiator, "Alice"))}
			_, err := s.PutPart(ctx, up, 1, bytes.NewReader(mibPattern(5)), op.PutParams{Attrs: attrs, Size: 5 << 20})
			Expect(err).To(MatchError(op.ErrInternalError), "errno %d", errno)
			settle(s)
			Expect(c.Object(testDataPool, "", head)).To(BeNil(), "the part head is removed")
			Expect(c.Object(testDataPool, "", partShadowOID(prefix, 1, 1))).To(BeNil(), "its shadow stripe is removed")
			Expect(c.Object(extraPoolName, "", metaObj).Omap).NotTo(HaveKey("part.00000001"), "no part is registered")
		},
		Entry("ECANCELED, a head replaced since its read", syscall.ECANCELED),
		Entry("ENOENT, a head removed since its read", syscall.ENOENT),
		Entry("EEXIST, a head created since its read", syscall.EEXIST),
	)
})
