package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"syscall"
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

var _ = Describe("gcShard", func() {
	DescribeTable("is RGWGC::tag_index",
		func(ctx SpecContext, maxObjs, tag string, want int) {
			s, err := driver.Open(ctx, newIndexCluster(), conf(map[string]string{"rgw_gc_max_objs": maxObjs}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(driver.GCShardForTest(s, tag)).To(Equal(want))
		},
		// XXH64 with seed 8675309 (rgw_gc.h:27), from ceph's src/xxHash: "" 0xef287162ccb95a3a,
		// "tx-old\x00" 0xcfbf586c12d844fb, "tx-put\x00" 0xf69706c59eddf022, the 57-byte
		// transaction tag 0x5c08d368a6c0f208; rgw_shards_mod keeps the low 32 bits. A modulo
		// over the whole hash gives 24, 27, 0 and 19 for the 32-shard rows, and the tag
		// without its NUL, "tx-old", is shard 15.
		Entry("an empty tag", "32", "", 13),
		Entry("a write tag with its NUL", "32", "tx-old\x00", 14),
		Entry("another write tag with its NUL", "32", "tx-put\x00", 24),
		Entry("a radosgw transaction id, longer than one 32-byte stripe", "32", "tx00000a1b2c3d4e5f6a7b8-0068d7a1b2-4155-ceph-objectstore\x00", 8),
		Entry("above 7877 shards the second prime reduces", "7878", "tx-old\x00", 864),
		Entry("at rgw_shards_max the low 32 bits modulo 65521", "65521", "tx00000a1b2c3d4e5f6a7b8-0068d7a1b2-4155-ceph-objectstore\x00", 47070),
	)
})

var _ = Describe("enqueueGC", func() {
	const (
		chunk = 4096
		tails = 49
	)
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		gcOID   string
		oldTail func(n int) string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": "4096", "rgw_obj_stripe_size": "4096"}, now)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(bytes.Repeat([]byte("g"), chunk*(tails+1))),
			op.PutParams{Size: chunk * (tails + 1), Tag: "tx-old"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		gcOID = fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-old\x00"))
		oldTail = func(n int) string { return tailOID(putPrefix, n) }
		s.SetRandForTest(fixedRand(secondPrefix))
	})
	overwrite := func(ctx context.Context) {
		GinkgoHelper()
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "tx-new"})
		Expect(err).NotTo(HaveOccurred())
	}
	chainOIDs := func(chain []rgwcls.GCObj) []string {
		out := make([]string, 0, len(chain))
		for _, o := range chain {
			out = append(out, o.Key.Name)
		}
		return out
	}
	allTails := func() []string {
		out := make([]string, 0, tails)
		for n := 1; n <= tails; n++ {
			out = append(out, oldTail(n))
		}
		return out
	}

	It("splits the chain into entries whose estimated encoding stays within rgw_max_chunk_size", func(ctx SpecContext) {
		overwrite(ctx)
		entries := c.GCEntries(gcPoolName, gcNS, gcOID)
		// The estimate is 45 bytes for the request around the 7-byte tag and
		// 207 or 209 bytes for each object, as its name has one or two
		// digits, so 19, 19 and the last 11 fit 4096 (rgw_gc.cc:72-109).
		Expect(entries).To(HaveLen(3))
		sizes := []int{len(entries[0].Chain), len(entries[1].Chain), len(entries[2].Chain)}
		Expect(sizes).To(Equal([]int{19, 19, 11}))
		var got []string
		for _, e := range entries {
			Expect(e.Tag).To(Equal("tx-old\x00"))
			got = append(got, chainOIDs(e.Chain)...)
		}
		Expect(got).To(Equal(allTails()), "every stripe but the head, in order")
	})

	It("deletes inline what a refused batch and the rest of the chain hold, the batch's last object twice", func(ctx SpecContext) {
		enqueues := 0
		queue := fakerados.GCQueueClass()
		c.RegisterClass("rgw_gc", func(call *fakerados.ClassCall) ([]byte, int32) {
			enqueues++
			if enqueues > 1 {
				return nil, -int32(syscall.EIO)
			}
			return queue(call)
		}, fakerados.GCQueueWriteMethods...)
		c.ResetCounters()
		overwrite(ctx)
		entries := c.GCEntries(gcPoolName, gcNS, gcOID)
		Expect(entries).To(HaveLen(1))
		Expect(chainOIDs(entries[0].Chain)).To(Equal(allTails()[:19]))
		for n := 1; n <= tails; n++ {
			want := 0
			switch {
			case n == 38:
				want = 2 // rgw_gc.cc:89-96: --it leaves the batch's last object at the start of the remainder
			case n > 19:
				want = 1
			}
			Expect(c.Writes(testDataPool, "", oldTail(n))).To(Equal(want), "tail %d", n)
			if n > 19 {
				Expect(c.Object(testDataPool, "", oldTail(n))).To(BeNil(), "tail %d", n)
			} else {
				Expect(c.Object(testDataPool, "", oldTail(n))).NotTo(BeNil(), "tail %d, queued for the GC worker", n)
			}
		}
	})

	It("sends an object whose estimate alone passes rgw_max_chunk_size in an entry of its own", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": "200", "rgw_obj_stripe_size": "200"}, time.Now())
		small := meta.ObjKey{Name: "small"}
		_, err := s.PutObject(ctx, rec, small, bytes.NewReader(bytes.Repeat([]byte("h"), 1000)), op.PutParams{Size: 1000, Tag: "tx-small"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		s.SetRandForTest(fixedRand(secondPrefix))
		_, err = s.PutObject(ctx, rec, small, strings.NewReader("new"), op.PutParams{Size: 3, Tag: "tx-new"})
		Expect(err).NotTo(HaveOccurred())
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-small\x00")))
		var sizes []int
		for _, e := range entries {
			sizes = append(sizes, len(e.Chain))
		}
		Expect(sizes).To(Equal([]int{1, 1, 1, 1}), "radosgw loops forever here (docs/exclusions.md)")
	})
})
