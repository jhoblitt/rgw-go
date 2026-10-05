package driver_test

import (
	"bytes"
	"context"
	"maps"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// noQuota is a disabled quota with no limits, as radosgw's default.
var noQuota = meta.Quota{MaxSize: -1, MaxObjects: -1}

// stallingCluster is a fakerados cluster whose reads of one object, once
// stall is set, send on entered and then wait for their context to end,
// which they fail with.
type stallingCluster struct {
	*fakerados.Cluster
	pool, oid string
	stall     atomic.Bool
	entered   chan struct{}
}

func (c *stallingCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil || pool != c.pool {
		return p, err
	}
	return &stallingPool{Pool: p, c: c}, nil
}

type stallingPool struct {
	radosclient.Pool
	c *stallingCluster
}

func (p *stallingPool) Read(ctx context.Context, oid string, rop *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	if oid == p.c.oid && p.c.stall.Load() {
		p.c.entered <- struct{}{}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return p.Pool.Read(ctx, oid, rop, flags)
}

var _ = Describe("StatsStore", func() {
	const (
		statsBucketID = "zone-ceph-objectstore.4156.1"
		uids          = "users.uid"
		acctID        = "RGW00000000000000001"
	)
	var (
		t0     time.Time
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		clock  *fakeClock
		shard0 string
	)

	seedAlice := func(bq, uq meta.Quota) {
		info := meta.NewUserInfo()
		info.UserID, info.DisplayName = meta.UserID{ID: "alice"}, "Alice"
		info.BucketQuota, info.UserQuota = bq, uq
		seedUser(c, info, nil, meta.ObjVersion{Ver: 1, Tag: "alice"})
	}
	setPeriodQuotas := func(bq, uq meta.Quota) {
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			p.PeriodConfig.BucketQuota, p.PeriodConfig.UserQuota = bq, uq
		})
	}
	openOver := func(ctx context.Context, cluster radosclient.Cluster, kv map[string]string) *driver.Store {
		GinkgoHelper()
		opts := map[string]string{"rgw_zone": "ceph-objectstore"}
		maps.Copy(opts, kv)
		st, err := driver.Open(ctx, cluster, conf(opts), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		driver.SetClockForTest(st, clock)
		return st
	}
	open := func(ctx context.Context, kv map[string]string) *driver.Store {
		GinkgoHelper()
		return openOver(ctx, c, kv)
	}
	shardReads := func() int { return c.Reads(rookIndexPool, "", shard0) }
	ownerHeader := func(owner string, h user.Header) {
		if c.Object(rookMetaPool, uids, owner+".buckets") == nil {
			c.Put(rookMetaPool, uids, owner+".buckets", nil)
		}
		c.Object(rookMetaPool, uids, owner+".buckets").OmapHdr = encode(h)
	}
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		clock = newFakeClock(t0)
		c = newIndexCluster()
		c.SetClock(clock.Now)
		setPeriodQuotas(noQuota, noQuota)
		seedAlice(meta.Quota{MaxObjects: 2, MaxSize: -1, Enabled: true}, meta.Quota{MaxSize: 8192, MaxObjects: -1, Enabled: true})
		rec = testBucket(statsBucketID, 2)
		seedShards(c, rec)
		shard0 = rec.Info.IndexShardOID(rec.Info.Layout.Current, 0)
		seedShardHeader(c, rookIndexPool, shard0, rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{
			rgwcls.CategoryMain:      {TotalSize: 4000, TotalSizeRounded: 4096, NumEntries: 1},
			rgwcls.CategoryMultiMeta: {TotalSize: 100, NumEntries: 1},
		}})
		seedRecordInstance(c, rec)
		s = open(ctx, nil)
	})

	It("reports the Main category for HEAD and every category for the quota", func(ctx SpecContext) {
		st, err := s.BucketStats(ctx, rec)
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(Equal(op.Stats{Size: 4000, SizeRounded: 4096, NumObjects: 1}), "load_bucket_stats, rgw_op.cc:3003-3016")
		err = s.CheckQuota(ctx, rec, alice, 0, 1)
		Expect(err).To(MatchError(op.ErrQuotaExceeded), "1 Main + 1 MultiMeta + 1 > 2: the quota sums every category, rgw_quota.cc:278-284")
		Expect(err).To(MatchError(ContainSubstring("bucket")))
	})

	It("reports zero stats for an indexless bucket without reading an index", func(ctx SpecContext) {
		rec.Info.Layout.Current.Layout.Type = meta.IndexIndexless
		seedRecordInstance(c, rec)
		Expect(s.BucketStats(ctx, rec)).To(Equal(op.Stats{}))
		Expect(s.CheckQuota(ctx, rec, alice, 0, 2)).To(Succeed(), "an indexless bucket counts as empty, rgw_quota.cc:261-264")
		Expect(shardReads()).To(BeZero())
	})

	It("reports an owner's stats from the header of its buckets object", func(ctx SpecContext) {
		Expect(s.UserStats(ctx, alice)).To(Equal(op.Stats{}), "no buckets object yet")
		ownerHeader("alice", user.Header{Stats: user.Stats{TotalEntries: 3, TotalBytes: 15, TotalBytesRounded: 8192}})
		Expect(s.UserStats(ctx, alice)).To(Equal(op.Stats{Size: 15, SizeRounded: 8192, NumObjects: 3}))
	})

	Describe("the stats caches", func() {
		It("fetches stats on a cold cache once and serves them until the ttl, then fetches again", func(ctx SpecContext) {
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			before := shardReads()
			Expect(before).To(Equal(1), "a cold cache reads the index")
			clock.Advance(299 * time.Second)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			driver.WaitQuotaRefreshesForTest(s)
			Expect(shardReads()).To(Equal(before), "a hit inside the ttl, before its refresh time")
			clock.Advance(302 * time.Second)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			driver.WaitQuotaRefreshesForTest(s)
			Expect(shardReads()).To(Equal(before+2), "expired: fetched again, and the refresh due since 300 s runs too, rgw_quota.cc:148-164")
		})

		It("refreshes once in the background past half the ttl and serves the cached stats meanwhile", func(ctx SpecContext) {
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			before := shardReads()
			seedShardHeader(c, rookIndexPool, shard0, rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{
				rgwcls.CategoryMain: {TotalSize: 8000, TotalSizeRounded: 8192, NumEntries: 2},
			}})
			clock.Advance(300 * time.Second)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed(), "the cached two objects, not the stored two")
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed(), "a refresh is under way: no second one")
			driver.WaitQuotaRefreshesForTest(s)
			Expect(shardReads()).To(Equal(before+1), "one refresh, rgw_quota.cc:101-112")
			Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 8000, SizeRounded: 8192, NumObjects: 2}))
			Expect(s.CheckQuota(ctx, rec, alice, 0, 1)).To(MatchError(op.ErrQuotaExceeded), "the refreshed stats")
			driver.WaitQuotaRefreshesForTest(s)
			Expect(shardReads()).To(Equal(before+1), "the refresh set a new refresh time")
		})

		It("leaves an entry whose refresh failed without a refresh time until it expires", func(ctx SpecContext) {
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			before := shardReads()
			good := c.Object(rookIndexPool, "", shard0).OmapHdr
			c.Object(rookIndexPool, "", shard0).OmapHdr = []byte{1}
			clock.Advance(300 * time.Second)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed(), "served from the cache")
			driver.WaitQuotaRefreshesForTest(s)
			Expect(shardReads()).To(Equal(before+1), "the refresh ran and failed")
			clock.Advance(299 * time.Second)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			driver.WaitQuotaRefreshesForTest(s)
			Expect(shardReads()).To(Equal(before+1), "no refresh time to pass: async_refresh_fail only logs, rgw_quota.cc:114-118")
			c.Object(rookIndexPool, "", shard0).OmapHdr = good
			clock.Advance(time.Second)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			driver.WaitQuotaRefreshesForTest(s)
			Expect(shardReads()).To(Equal(before+2), "expired: one fetch, and still no refresh")
		})

		It("cancels the refreshes under way at Close and waits for them", func(ctx SpecContext) {
			stalling := &stallingCluster{Cluster: c, pool: rookIndexPool, oid: shard0, entered: make(chan struct{}, 1)}
			st := openOver(ctx, stalling, nil)
			Expect(st.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			stalling.stall.Store(true)
			clock.Advance(300 * time.Second)
			Expect(st.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Eventually(stalling.entered).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(), "the refresh is reading")
			closed := make(chan error, 1)
			go func() { closed <- st.Close() }()
			Eventually(closed).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()),
				"no refresh outlives the pools it reads, as ~RGWQuotaCache's put_wait, rgw_quota.cc:82-84")
			refreshed := make(chan struct{})
			go func() {
				driver.WaitQuotaRefreshesForTest(st)
				close(refreshed)
			}()
			Eventually(refreshed).WithTimeout(100*time.Millisecond).WithPolling(time.Millisecond).Should(BeClosed(), "the refresh was canceled")
		})

		It("moves an entry it finds to the front, so the least recently used is evicted", func(ctx SpecContext) {
			s = open(ctx, map[string]string{"rgw_bucket_quota_cache_size": "2"})
			buckets := []*op.BucketRecord{rec}
			for _, id := range []string{"zone-ceph-objectstore.4156.2", "zone-ceph-objectstore.4156.3"} {
				b := testBucket(id, 1)
				seedShards(c, b)
				seedRecordInstance(c, b)
				buckets = append(buckets, b)
			}
			reads := func(b *op.BucketRecord) int {
				return c.Reads(rookIndexPool, "", b.Info.IndexShardOID(b.Info.Layout.Current, 0))
			}
			a, b, third := buckets[0], buckets[1], buckets[2]
			for _, r := range []*op.BucketRecord{a, b, a, third, a} {
				Expect(s.CheckQuota(ctx, r, alice, 0, 0)).To(Succeed())
			}
			Expect(reads(a)).To(Equal(1), "found again before the third bucket came, so b was the back, lru_map.h _find")
			Expect(s.CheckQuota(ctx, b, alice, 0, 0)).To(Succeed())
			Expect(reads(b)).To(Equal(2), "evicted")
		})

		It("evicts the least recently used entry past rgw_bucket_quota_cache_size", func(ctx SpecContext) {
			s = open(ctx, map[string]string{"rgw_bucket_quota_cache_size": "1"})
			other := testBucket("zone-ceph-objectstore.4156.2", 1)
			seedShards(c, other)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Expect(s.CheckQuota(ctx, other, alice, 0, 0)).To(Succeed())
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Expect(shardReads()).To(Equal(2), "evicted by the other bucket, lru_map.h")
		})

		It("reads the bucket's instance again and counts a bucket whose instance is gone as empty", func(ctx SpecContext) {
			gone := testBucket("zone-ceph-objectstore.4156.9", 1)
			seedShards(c, gone)
			seedShardHeader(c, rookIndexPool, gone.Info.IndexShardOID(gone.Info.Layout.Current, 0), rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{
				rgwcls.CategoryMain: {TotalSize: 1, TotalSizeRounded: 4096, NumEntries: 1},
			}})
			gone.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: 1, Enabled: true}
			Expect(s.CheckQuota(ctx, gone, alice, 0, 1)).To(Succeed(), "load_bucket's ENOENT is zero stats, rgw_quota.cc:164-168")
			Expect(c.Reads(rookIndexPool, "", gone.Info.IndexShardOID(gone.Info.Layout.Current, 0))).To(BeZero())
		})

		It("caches nothing when rgw_bucket_quota_cache_size is 0", func(ctx SpecContext) {
			s = open(ctx, map[string]string{"rgw_bucket_quota_cache_size": "0"})
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Expect(shardReads()).To(Equal(2))
		})

		It("never serves cached stats when rgw_bucket_quota_ttl is not positive", func(ctx SpecContext) {
			s = open(ctx, map[string]string{"rgw_bucket_quota_ttl": "-5"})
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			driver.WaitQuotaRefreshesForTest(s)
			Expect(shardReads()).To(Equal(3), "two fetches and the refresh the expired entry was due, rgw_quota.cc:136-139, :148-164")
		})

		It("adjusts both caches, floors each field at zero and marks the bucket for the owner sync", func(ctx SpecContext) {
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Expect(s.AdjustStats(ctx, rec, alice, 1, 5000, 0)).To(Succeed())
			Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 9100, SizeRounded: 4096 + 8192, NumObjects: 3}))
			Expect(driver.CachedOwnerStatsForTest(s, alice)).To(Equal(op.Stats{Size: 5000, SizeRounded: 8192, NumObjects: 1}))
			Expect(s.AdjustStats(ctx, rec, alice, -10, 0, 1<<40)).To(Succeed())
			Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{}), "floored at zero, rgw_quota.cc:192-208")
			Expect(driver.CachedOwnerStatsForTest(s, alice)).To(Equal(op.Stats{}))
			bob := meta.UserOwner(meta.UserID{ID: "bob"})
			Expect(s.AdjustStats(ctx, rec, bob, 1, 1, 0)).To(Succeed())
			Expect(driver.ModifiedBucketsForTest(s)).To(Equal(map[string]meta.Owner{driver.BucketStatsKeyForTest(rec): alice}),
				"the first owner recorded stays, rgw_quota.cc:704-715")
		})

		It("keeps no modified buckets without the quota workers to sync them", func(ctx SpecContext) {
			s = open(ctx, map[string]string{"rgw_enable_quota_threads": "false"})
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Expect(s.AdjustStats(ctx, rec, alice, 1, 1, 0)).To(Succeed())
			Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 4101, SizeRounded: 8192, NumObjects: 3}), "the caches still adjust")
			Expect(driver.ModifiedBucketsForTest(s)).To(BeEmpty())
		})

		It("adjusts nothing a cache does not hold", func(ctx SpecContext) {
			Expect(s.AdjustStats(ctx, rec, alice, 1, 5000, 0)).To(Succeed())
			Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{}))
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
			Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 4100, SizeRounded: 4096, NumObjects: 2}), "fetched, not adjusted")
		})
	})

	Describe("CheckQuota", func() {
		It("compares rounded sizes by default and raw sizes under check_on_raw", func(ctx SpecContext) {
			rec.Info.Quota = meta.Quota{MaxSize: 8192, MaxObjects: -1, Enabled: true}
			Expect(s.CheckQuota(ctx, rec, alice, 4096, 1)).To(Succeed(), "4096 + rounded(4096) is not over 8192, rgw_quota.cc:780-783")
			Expect(s.CheckQuota(ctx, rec, alice, 1, 1)).To(Succeed(), "4096 + rounded(1) = 8192")
			Expect(s.CheckQuota(ctx, rec, alice, 4097, 1)).To(MatchError(op.ErrQuotaExceeded), "4096 + rounded(4097) = 12288")
			rec.Info.Quota.CheckOnRaw = true
			Expect(s.CheckQuota(ctx, rec, alice, 4092, 1)).To(Succeed(), "raw: 4100 + 4092 = 8192, rgw_quota.cc:825-827")
			Expect(s.CheckQuota(ctx, rec, alice, 4093, 1)).To(MatchError(op.ErrQuotaExceeded), "raw: 4100 + 4093")
		})

		It("takes the bucket quota from the bucket, then the owner, then the period", func(ctx SpecContext) {
			rec.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: 3, Enabled: true}
			Expect(s.CheckQuota(ctx, rec, alice, 0, 1)).To(Succeed(), "the bucket's 3 over alice's 2, rgw_op.cc:1438-1442")
			rec.Info.Quota = noQuota
			Expect(s.CheckQuota(ctx, rec, alice, 0, 1)).To(MatchError(op.ErrQuotaExceeded), "alice's 2")
			seedAlice(noQuota, noQuota)
			setPeriodQuotas(meta.Quota{MaxSize: -1, MaxObjects: 1, Enabled: true}, noQuota)
			s = open(ctx, nil)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(MatchError(op.ErrQuotaExceeded), "the period's 1, rgw_sal_rados.cc:1937-1941")
			setPeriodQuotas(noQuota, noQuota)
			s = open(ctx, nil)
			reads, ownerReads := shardReads(), c.Reads(rookMetaPool, uids, "alice.buckets")
			Expect(s.CheckQuota(ctx, rec, alice, 1<<40, 1<<20)).To(Succeed(), "no quota enabled, rgw_quota.cc:919-921")
			Expect(shardReads()).To(Equal(reads), "nothing fetched")
			Expect(c.Reads(rookMetaPool, uids, "alice.buckets")).To(Equal(ownerReads))
		})

		It("enforces the owner's user quota against its buckets object's header", func(ctx SpecContext) {
			seedAlice(noQuota, meta.Quota{MaxSize: 8192, MaxObjects: -1, Enabled: true})
			ownerHeader("alice", user.Header{Stats: user.Stats{TotalEntries: 2, TotalBytes: 8000, TotalBytesRounded: 8192}})
			s = open(ctx, nil)
			Expect(shardReads()).To(BeZero())
			err := s.CheckQuota(ctx, rec, alice, 1, 1)
			Expect(err).To(MatchError(op.ErrQuotaExceeded), "8192 + rounded(1)")
			Expect(err).To(MatchError(ContainSubstring("user")))
			Expect(shardReads()).To(BeZero(), "only the user quota is enabled")
			seedAlice(noQuota, noQuota)
			s = open(ctx, nil)
			Expect(s.CheckQuota(ctx, rec, alice, 1, 1)).To(Succeed())
		})

		It("takes the period's user quota when the owner's is disabled", func(ctx SpecContext) {
			seedAlice(noQuota, noQuota)
			setPeriodQuotas(noQuota, meta.Quota{MaxSize: -1, MaxObjects: 0, Enabled: true})
			s = open(ctx, nil)
			Expect(s.CheckQuota(ctx, rec, alice, 0, 1)).To(MatchError(op.ErrQuotaExceeded))
		})

		It("uses an account owner's quotas", func(ctx SpecContext) {
			acct := meta.NewAccountInfo()
			acct.ID, acct.Name = acctID, "acme"
			acct.BucketQuota = meta.Quota{MaxSize: -1, MaxObjects: 1, Enabled: true}
			c.Put(rookMetaPool, "accounts", "account."+acctID, encode(acct))
			owner := meta.AccountOwner(acctID)
			rec.Info.Owner = owner
			Expect(s.CheckQuota(ctx, rec, owner, 0, 0)).To(MatchError(op.ErrQuotaExceeded), "rgw_op.cc:1398-1407")
		})

		It("fails as init_quota does when the owner cannot be loaded", func(ctx SpecContext) {
			bob := meta.UserOwner(meta.UserID{ID: "bob"})
			Expect(s.CheckQuota(ctx, rec, bob, 0, 0)).To(MatchError(op.ErrNoSuchKey), "load_user's ENOENT, rgw_op.cc:1428-1433")
			Expect(s.CheckQuota(ctx, rec, meta.AccountOwner(acctID), 0, 0)).To(MatchError(op.ErrNoSuchKey))
		})
	})

	Describe("the bucket sync worker", func() {
		It("writes each modified bucket's Main stats into its owner's entry every rgw_user_quota_bucket_sync_interval", func(ctx SpecContext) {
			Expect(driver.LinkBucket(s, ctx, alice, rec.Info.Bucket, t0)).To(Succeed())
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- s.RunBucketsSyncForTest(runCtx) }()
			Eventually(clock.Periods).WithTimeout(time.Second).WithPolling(time.Millisecond).
				Should(Equal([]time.Duration{180 * time.Second}), "rgw_quota.cc:378-381")
			Expect(s.AdjustStats(ctx, rec, alice, 1, 1, 0)).To(Succeed())
			clock.Advance(179 * time.Second)
			Consistently(func() map[string]meta.Owner { return driver.ModifiedBucketsForTest(s) }).
				WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(HaveLen(1))
			clock.Advance(time.Second)
			readStats := func() op.Stats {
				st, _, _, err := driver.ReadOwnerStats(s, ctx, alice)
				Expect(err).NotTo(HaveOccurred())
				return st
			}
			Eventually(readStats).WithTimeout(time.Second).WithPolling(time.Millisecond).
				Should(Equal(op.Stats{Size: 4000, SizeRounded: 4096, NumObjects: 1}), "the index's Main stats, not the cache's guess")
			Expect(driver.ModifiedBucketsForTest(s)).To(BeEmpty())
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
			Expect(clock.Periods()).To(BeEmpty())
		})

		It("syncs the buckets modified before it started at once", func(ctx SpecContext) {
			Expect(driver.LinkBucket(s, ctx, alice, rec.Info.Bucket, t0)).To(Succeed())
			Expect(s.AdjustStats(ctx, rec, alice, 1, 1, 0)).To(Succeed())
			runCtx, cancel := context.WithCancel(ctx)
			DeferCleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- s.RunBucketsSyncForTest(runCtx) }()
			Eventually(func() op.Stats {
				st, _, _, err := driver.ReadOwnerStats(s, ctx, alice)
				Expect(err).NotTo(HaveOccurred())
				return st
			}).WithTimeout(time.Second).WithPolling(time.Millisecond).
				Should(Equal(op.Stats{Size: 4000, SizeRounded: 4096, NumObjects: 1}), "the loop body runs before the first wait, rgw_quota.cc:361-376; no tick has come")
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
		})

		DescribeTable("waits a second, logging an error, when an interval is not positive",
			func(ctx SpecContext, option, value string, run func(*driver.Store, context.Context) error) {
				var logs bytes.Buffer
				DeferCleanup(driver.CaptureLog(&logs))
				st := open(ctx, map[string]string{option: value})
				runCtx, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				go func() { done <- run(st, runCtx) }()
				Eventually(clock.Periods).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]time.Duration{time.Second}))
				cancel()
				Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
				Expect(logs.String()).To(ContainSubstring(`"option":"` + option + `","value":` + value))
			},
			Entry("a zero bucket sync interval", "rgw_user_quota_bucket_sync_interval", "0", (*driver.Store).RunBucketsSyncForTest),
			Entry("a negative bucket sync interval", "rgw_user_quota_bucket_sync_interval", "-5", (*driver.Store).RunBucketsSyncForTest),
			Entry("a zero owner sync interval", "rgw_user_quota_sync_interval", "0", (*driver.Store).RunUserSyncForTest),
			Entry("a negative owner sync interval", "rgw_user_quota_sync_interval", "-5", (*driver.Store).RunUserSyncForTest),
		)
	})

	Describe("the owner sync workers", func() {
		var day time.Duration
		BeforeEach(func(ctx SpecContext) {
			day = 24 * time.Hour
			Expect(driver.LinkBucket(s, ctx, alice, rec.Info.Bucket, t0.Add(-2*day))).To(Succeed())
		})
		lastSync := func(ctx context.Context, owner meta.Owner) time.Time {
			GinkgoHelper()
			_, synced, _, err := driver.ReadOwnerStats(s, ctx, owner)
			Expect(err).NotTo(HaveOccurred())
			return synced
		}
		setTimes := func(owner string, synced, updated time.Time) {
			obj := c.Object(rookMetaPool, uids, owner+".buckets")
			if obj == nil {
				c.Put(rookMetaPool, uids, owner+".buckets", nil)
				obj = c.Object(rookMetaPool, uids, owner+".buckets")
			}
			obj.OmapHdr = encode(user.Header{LastStatsSync: synced, LastStatsUpdate: updated})
		}
		run := func(ctx context.Context, fn func(*driver.Store, context.Context) error) (context.CancelFunc, <-chan error) {
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- fn(s, runCtx) }()
			DeferCleanup(cancel)
			return cancel, done
		}

		It("runs a full sync at start, skipping idle and recently synced owners, then every rgw_user_quota_sync_interval", func(ctx SpecContext) {
			for _, id := range []string{"bob", "carol"} {
				info := meta.NewUserInfo()
				info.UserID = meta.UserID{ID: id}
				seedUser(c, info, nil, meta.ObjVersion{Ver: 1, Tag: id})
			}
			setTimes("alice", t0.Add(-2*day), t0.Add(-day))
			setTimes("bob", t0.Add(-day), t0.Add(-2*day))
			setTimes("carol", t0.Add(-time.Hour), t0)
			cancel, done := run(ctx, (*driver.Store).RunUserSyncForTest)
			Eventually(func() time.Time { return lastSync(ctx, alice) }).WithTimeout(time.Second).WithPolling(time.Millisecond).
				Should(BeTemporally("==", t0), "synced: rgw_sync_all_stats then complete_flush_stats, rgw_user.cc:16-55")
			Eventually(clock.Periods).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]time.Duration{day}))
			Expect(s.ListUserBuckets(ctx, alice, "", 10)).To(HaveExactElements(
				HaveField("Count", BeEquivalentTo(1))), "the bucket's entry holds its Main stats")
			Expect(lastSync(ctx, meta.UserOwner(meta.UserID{ID: "bob"}))).To(BeTemporally("==", t0.Add(-day)), "idle, rgw_quota.cc:636-640")
			Expect(lastSync(ctx, meta.UserOwner(meta.UserID{ID: "carol"}))).To(BeTemporally("==", t0.Add(-time.Hour)), "synced within the wait, rgw_quota.cc:642-648")

			setTimes("carol", t0.Add(-time.Hour), t0)
			clock.Advance(day)
			Eventually(func() time.Time { return lastSync(ctx, meta.UserOwner(meta.UserID{ID: "carol"})) }).
				WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeTemporally("==", t0.Add(day)), "the next pass")
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
		})

		It("syncs idle owners too under rgw_user_quota_sync_idle_users", func(ctx SpecContext) {
			s = open(ctx, map[string]string{"rgw_user_quota_sync_idle_users": "true"})
			setTimes("alice", t0.Add(-day-time.Second), t0.Add(-2*day))
			_, done := run(ctx, (*driver.Store).RunUserSyncForTest)
			Eventually(func() time.Time { return lastSync(ctx, alice) }).WithTimeout(time.Second).WithPolling(time.Millisecond).
				Should(BeTemporally("==", t0))
			Consistently(done).WithTimeout(10 * time.Millisecond).ShouldNot(Receive())
		})

		It("skips a listed bucket it cannot load, and stops at one whose stats it cannot read", func(ctx SpecContext) {
			gone := meta.BucketID{Name: "gone", Marker: "m-gone", ID: "m-gone"}
			Expect(driver.LinkBucket(s, ctx, alice, gone, t0)).To(Succeed())
			Expect(s.SyncAllStatsForTest(ctx, alice)).To(Succeed(), "rgw_user.cc:32-36")
			Expect(lastSync(ctx, alice)).To(BeTemporally("==", t0))
			c.Object(rookIndexPool, "", shard0).OmapHdr = []byte{1}
			clock.Advance(time.Hour)
			Expect(s.SyncAllStatsForTest(ctx, alice)).NotTo(Succeed(), "rgw_user.cc:37-41")
			Expect(lastSync(ctx, alice)).To(BeTemporally("==", t0), "no completion")
		})

		It("skips an account whose info does not load, as get_owner_tenant fails", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			acct := meta.NewAccountInfo()
			acct.ID, acct.Name = "RGW00000000000000002", "other"
			c.Put(rookMetaPool, "accounts", "account."+acctID, encode(acct))
			_, done := run(ctx, (*driver.Store).RunAccountSyncForTest)
			Eventually(clock.Periods).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]time.Duration{day}), "the pass ended")
			Expect(c.Object(rookMetaPool, "accounts", "buckets."+acctID)).To(BeNil(),
				"no full sync and no complete_flush_stats, rgw_quota.cc:650-654")
			Expect(logs.String()).To(ContainSubstring("owner stats sync failed"))
			Expect(logs.String()).To(ContainSubstring(acctID))
			Consistently(done).WithTimeout(10*time.Millisecond).ShouldNot(Receive(), "the worker goes on")
		})

		It("syncs an account's buckets in the account worker", func(ctx SpecContext) {
			acct := meta.NewAccountInfo()
			acct.ID, acct.Name = acctID, "acme"
			c.Put(rookMetaPool, "accounts", "account."+acctID, encode(acct))
			owner := meta.AccountOwner(acctID)
			other := testBucket("zone-ceph-objectstore.4156.3", 1)
			other.Info.Bucket.Name = "acme-bucket"
			other.Info.Owner = owner
			seedShards(c, other)
			seedShardHeader(c, rookIndexPool, other.Info.IndexShardOID(other.Info.Layout.Current, 0), rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{
				rgwcls.CategoryMain: {TotalSize: 7, TotalSizeRounded: 4096, NumEntries: 1},
			}})
			seedRecordInstance(c, other)
			Expect(driver.LinkBucket(s, ctx, owner, other.Info.Bucket, t0)).To(Succeed())
			_, done := run(ctx, (*driver.Store).RunAccountSyncForTest)
			Eventually(func() op.Stats {
				st, _, _, err := driver.ReadOwnerStats(s, ctx, owner)
				Expect(err).NotTo(HaveOccurred())
				return st
			}).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(op.Stats{Size: 7, SizeRounded: 4096, NumObjects: 1}))
			Expect(lastSync(ctx, alice)).To(BeZero(), "the user worker's, not this one's")
			Consistently(done).WithTimeout(10 * time.Millisecond).ShouldNot(Receive())
		})
	})

	It("runs the three quota workers only with rgw_enable_quota_threads", func(ctx SpecContext) {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- s.Run(runCtx) }()
		Eventually(clock.Periods).WithTimeout(time.Second).WithPolling(time.Millisecond).
			Should(ConsistOf(180*time.Second, 24*time.Hour, 24*time.Hour), "rgw_quota.cc:488-495")
		cancel()
		Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))

		clock = newFakeClock(t0)
		noThreads := open(ctx, map[string]string{"rgw_enable_quota_threads": "false"})
		runCtx, cancel = context.WithCancel(ctx)
		go func() { done <- noThreads.Run(runCtx) }()
		Consistently(clock.Periods).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeEmpty())
		cancel()
		Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(Receive(Succeed()))
	})
})
