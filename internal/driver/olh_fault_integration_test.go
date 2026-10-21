//go:build integration

package driver_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("the integration build's fault point after a versioned link", Label("integration"), func() {
	It("stops the writer before its replay, leaving its pending attr and log entry for the next reader", func(ctx SpecContext) {
		c := newPutCluster()
		rel := denc.Squid
		s, err := driver.Open(ctx, c, conf(nil), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		rec := testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard := indexShardOID(rec, "k")
		v := meta.ObjKey{Name: "k", Instance: "v1"}
		x := s.NewIndexOpForTest(rec, v, "go-tag-v1")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		ver := writeInstanceHead(ctx, c, v)
		Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: ver}, versionMeta(v))).To(Succeed())

		s.StopAfterOLHLinkForTest(true)
		Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(Succeed())
		s.StopAfterOLHLinkForTest(false)
		head := c.Object(testDataPool, "", olhHeadOID(v))
		var pending []string
		for name := range head.Xattrs {
			if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
				pending = append(pending, name)
			}
		}
		Expect(pending).To(HaveLen(1), "the link's pending attr")
		Expect(head.Xattrs).NotTo(HaveKey(meta.AttrOLHInfo), "the link is not applied")
		e, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue())
		Expect(e.PendingLog).To(HaveKey(uint64(2)), "the link's log entry")

		h, err := s.ReadOLHHeadForTest(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.UpdateOLHForTest(ctx, rec, h)).To(Succeed())
		e, _ = c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(e.PendingLog).To(BeEmpty(), "the next reader's replay finished the write")
		Expect(c.Object(testDataPool, "", olhHeadOID(v)).Xattrs).To(HaveKey(meta.AttrOLHInfo))
	})
})
