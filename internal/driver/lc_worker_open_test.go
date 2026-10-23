package driver_test

import (
	"context"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("the lifecycle worker of an opened Store", func() {
	var c *fakerados.Cluster
	open := func(ctx context.Context, kv map[string]string) *driver.Store {
		GinkgoHelper()
		st, err := driver.Open(ctx, c, conf(kv), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		return st
	}
	BeforeEach(func() { c = newIndexCluster() })

	DescribeTable("is a worker Open registers with rgw_enable_lc_threads and a positive rgw_lc_max_worker",
		func(ctx SpecContext, kv map[string]string, registered bool) {
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			s := open(ctx, kv)
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.Run(runCtx) }()
			cancel()
			Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
			Expect(logs.String()).To(ContainSubstring(`"msg":"worker started","worker":"index-completions"`), "every worker logs its start before Run returns")
			if registered {
				Expect(logs.String()).To(ContainSubstring(`"msg":"worker started","worker":"lifecycle"`))
			} else {
				Expect(logs.String()).NotTo(ContainSubstring(`"worker":"lifecycle"`))
			}
		},
		Entry("radosgw's defaults", nil, true),
		Entry("rgw_enable_lc_threads off", map[string]string{"rgw_enable_lc_threads": "false"}, false),
		Entry("rgw_lc_max_worker 0", map[string]string{"rgw_lc_max_worker": "0"}, false),
		Entry("a negative rgw_lc_max_worker", map[string]string{"rgw_lc_max_worker": "-1"}, false),
	)

	Describe("on the stale entries and the empty configurations bucket_lc_process meets", func() {
		params := func(name string) op.CreateBucketParams {
			return op.CreateBucketParams{
				Name: name, Owner: meta.UserOwner(meta.UserID{ID: "alice"}), Zonegroup: "zg-ceph-objectstore",
				Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs:     map[string][]byte{meta.AttrACL: {1}}, Exclusive: true,
			}
		}
		// withLC creates name with a lifecycle configuration and its entry, as
		// PutBucketLifecycleConfiguration leaves them, and returns its key.
		withLC := func(ctx context.Context, s *driver.Store, name string) (*op.BucketRecord, string) {
			GinkgoHelper()
			rec, err := s.CreateBucket(ctx, params(name))
			Expect(err).NotTo(HaveOccurred())
			Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrLC: lcAttr(expireRule("expire", ""))}, nil)).To(Succeed())
			Expect(s.SetEntry(ctx, rec)).To(Succeed())
			return rec, ":" + name + ":" + rec.Info.Bucket.Marker
		}
		// deleteLeavingEntry deletes rec as a bucket delete whose lifecycle
		// entry removal fails does.
		deleteLeavingEntry := func(ctx context.Context, s *driver.Store, rec *op.BucketRecord, key string) {
			GinkgoHelper()
			c.FailNextWrite(rookLCPool, rookLCNS, lcShardOf(key), syscall.EIO)
			Expect(s.DeleteBucket(ctx, rec)).To(Succeed())
			Expect(lcEntries(c)).To(HaveKey(key))
		}

		It("drops the entry of a bucket that no longer loads, and of one re-created under its name, and completes a live bucket's", func(ctx SpecContext) {
			s := open(ctx, nil)
			gone, goneKey := withLC(ctx, s, "gone")
			deleteLeavingEntry(ctx, s, gone, goneKey)
			first, oldKey := withLC(ctx, s, "plain")
			deleteLeavingEntry(ctx, s, first, oldKey)
			_, liveKey := withLC(ctx, s, "plain")
			Expect(liveKey).NotTo(Equal(oldKey), "the bucket re-created under the name has another marker")

			now := time.Now()
			s.LCPassForTest(ctx, now, time.UTC)
			entries := lcEntries(c)
			Expect(entries).NotTo(HaveKey(goneKey), "a name that no longer loads")
			Expect(entries).NotTo(HaveKey(oldKey), "a bucket that loads with another marker")
			Expect(entries).To(HaveKeyWithValue(liveKey, rgwcls.LCEntry{Bucket: liveKey, StartTime: uint64(now.Unix()), Status: rgwcls.LCStatusComplete}))
		})

		It("drops an entry whose key does not split into a tenant, a name and a marker", func(ctx SpecContext) {
			s := open(ctx, nil)
			_, err := s.CreateBucket(ctx, params("short"))
			Expect(err).NotTo(HaveOccurred())
			const key = ":short" // a tenant and a name that load, and no marker
			oid := lcShardOf(key)
			c.Put(rookLCPool, rookLCNS, oid, nil)
			c.Object(rookLCPool, rookLCNS, oid).Omap[key] = encode(rgwcls.LCEntry{Bucket: key})
			s.LCPassForTest(ctx, time.Now(), time.UTC)
			Expect(lcEntries(c)).NotTo(HaveKey(key))
		})

		It("completes the entry of a bucket without user.rgw.lc, as bucket_lc_process does", func(ctx SpecContext) {
			s := open(ctx, nil)
			rec, err := s.CreateBucket(ctx, params("bare"))
			Expect(err).NotTo(HaveOccurred())
			Expect(s.SetEntry(ctx, rec)).To(Succeed())
			key := ":bare:" + rec.Info.Bucket.Marker
			now := time.Now()
			s.LCPassForTest(ctx, now, time.UTC)
			Expect(lcEntries(c)).To(HaveKeyWithValue(key,
				rgwcls.LCEntry{Bucket: key, StartTime: uint64(now.Unix()), Status: rgwcls.LCStatusComplete}))
		})

		It("completes a stale entry with rgwlc_skip_bucket_step, whose bucket pass succeeds before it loads anything", func(ctx SpecContext) {
			s := open(ctx, map[string]string{"rgwlc_skip_bucket_step": "true"})
			gone, goneKey := withLC(ctx, s, "gone")
			deleteLeavingEntry(ctx, s, gone, goneKey)
			now := time.Now()
			s.LCPassForTest(ctx, now, time.UTC)
			Expect(lcEntries(c)).To(HaveKeyWithValue(goneKey,
				rgwcls.LCEntry{Bucket: goneKey, StartTime: uint64(now.Unix()), Status: rgwcls.LCStatusComplete}))
		})
	})
})
