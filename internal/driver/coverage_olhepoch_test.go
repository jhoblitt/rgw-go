package driver_test

import (
	"bytes"
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

// This pins docs/differences.md, "OLH epochs differ by OSD release": every
// link and unlink an S3 request makes sends epoch 0, as radosgw does for a
// requester that is not a system user, so the class takes the name's next
// epoch.
var _ = Describe("the OLH epoch an S3 request sends", func() {
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		shard string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
	})
	AfterEach(func() { settle(s) })
	// epochs decodes the OLH epoch of every bucket_link_olh and
	// bucket_unlink_instance request sent to k's shard.
	epochs := func(method string) []uint64 {
		var out []uint64
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			for _, st := range w.Steps() {
				x, ok := st.(*radosclient.ExecStep)
				if !ok || x.Method != method {
					continue
				}
				d := denc.NewDecoder(x.In)
				switch method {
				case "bucket_link_olh":
					out = append(out, rgwcls.DecodeLinkOLHOp(d).OLHEpoch)
				case "bucket_unlink_instance":
					out = append(out, rgwcls.DecodeUnlinkInstanceOp(d).OLHEpoch)
				}
				Expect(d.Err()).NotTo(HaveOccurred())
			}
		}
		return out
	}
	put := func(ctx SpecContext, instance string) {
		GinkgoHelper()
		attrs := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k", Instance: instance}, bytes.NewReader([]byte("one")),
			op.PutParams{Attrs: attrs, Size: 3, Tag: "tag-" + instance})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
	}

	It("sends epoch 0 in a versioned PUT's link and a delete marker's", func(ctx SpecContext) {
		put(ctx, "v1")
		put(ctx, "v2")
		_, err := s.DeleteObject(ctx, rec, meta.ObjKey{Name: "k"}, op.DeleteParams{})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(epochs("bucket_link_olh")).To(Equal([]uint64{0, 0, 0}), "two versions and a delete marker")
	})

	It("sends epoch 0 in a version's removal", func(ctx SpecContext) {
		put(ctx, "v1")
		_, err := s.DeleteObject(ctx, rec, meta.ObjKey{Name: "k", Instance: "v1"}, op.DeleteParams{})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(epochs("bucket_unlink_instance")).To(Equal([]uint64{0}))
	})
})
