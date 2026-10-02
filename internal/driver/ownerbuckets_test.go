package driver_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// readGate holds each read until want reads are in flight at once, or a
// second has passed, and records the most reads it saw in flight and the
// objects read.
type readGate struct {
	mu       sync.Mutex
	want     int
	inFlight int
	peak     int
	reads    []string
	full     chan struct{}
}

func newReadGate(want int) *readGate { return &readGate{want: want, full: make(chan struct{})} }

// gatedCluster is a fakerados cluster whose handles on pool read through
// gate.
type gatedCluster struct {
	*fakerados.Cluster
	pool string
	gate *readGate
}

func (c *gatedCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil || pool != c.pool {
		return p, err
	}
	return &gatedPool{Pool: p, gate: c.gate}, nil
}

type gatedPool struct {
	radosclient.Pool
	gate *readGate
}

func (p *gatedPool) Read(ctx context.Context, oid string, rop *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	g := p.gate
	g.mu.Lock()
	g.inFlight++
	g.peak = max(g.peak, g.inFlight)
	g.reads = append(g.reads, oid)
	if g.inFlight == g.want {
		select {
		case <-g.full:
		default:
			close(g.full)
		}
	}
	g.mu.Unlock()
	select {
	case <-g.full:
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}()
	return p.Pool.Read(ctx, oid, rop, flags)
}

var _ = Describe("the owner's bucket list", func() {
	const (
		index = "ceph-objectstore.rgw.buckets.index"
		uids  = "users.uid"
	)
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		alice meta.Owner
	)
	open := func(ctx context.Context, cluster radosclient.Cluster, kv map[string]string) *driver.Store {
		GinkgoHelper()
		opts := map[string]string{"rgw_zone": "ceph-objectstore"}
		maps.Copy(opts, kv)
		st, err := driver.Open(ctx, cluster, conf(opts), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		return st
	}
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		c.RegisterClass("user", fakerados.UserClass(), fakerados.UserWriteMethods...)
		c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
		seedRookZone(c, "ceph-objectstore", true)
		s = open(ctx, c, nil)
		alice = meta.UserOwner(meta.UserID{ID: "alice"})
	})
	bucket := func(name string) meta.BucketID {
		return meta.BucketID{Name: name, Marker: "m-" + name, ID: "m-" + name}
	}
	// sharded is bucket a in the zone's default placement with n index
	// shards at generation 0.
	sharded := func(n uint32) meta.BucketInfo {
		info := meta.NewBucketInfo()
		info.Bucket = bucket("a")
		info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
		info.Layout = meta.NewBucketLayout()
		info.Layout.Current.Layout.Normal.NumShards = n
		return info
	}
	// main is a shard header holding stats in the Main category.
	main := func(size, rounded, entries uint64) rgwcls.DirHeader {
		return rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{
			rgwcls.CategoryMain: {TotalSize: size, TotalSizeRounded: rounded, NumEntries: entries},
		}}
	}
	// seedEntries stores entries in alice's bucket list as the class does,
	// one omap value per bucket name.
	seedEntries := func(entries map[string][]byte) {
		if c.Object(rookMetaPool, uids, "alice.buckets") == nil {
			c.Put(rookMetaPool, uids, "alice.buckets", nil)
		}
		maps.Copy(c.Object(rookMetaPool, uids, "alice.buckets").Omap, entries)
	}
	entryOf := func(name string) []byte {
		return encode(user.BucketEntry{Bucket: user.Bucket{Name: name, Marker: "m-" + name, BucketID: "m-" + name}, Size: 1, SizeRounded: 4096, Count: 1})
	}
	names := func(ents []meta.BucketEnt) []string {
		out := make([]string, 0, len(ents))
		for _, e := range ents {
			out = append(out, e.Bucket.Name)
		}
		return out
	}

	Describe("linking and listing", func() {
		It("links buckets with cls_user and lists them in pages", func(ctx SpecContext) {
			for _, n := range []string{"c", "a", "b"} {
				Expect(driver.LinkBucket(s, ctx, alice, bucket(n), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))).To(Succeed())
			}
			ents, next, more, err := s.ListUserBuckets(ctx, alice, "", 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(2))
			Expect(ents[0].Bucket.Name).To(Equal("a"))
			Expect(ents[1].Bucket.Name).To(Equal("b"))
			Expect(ents[0].CreationTime.Time).To(BeTemporally("==", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
			Expect(more).To(BeTrue())
			Expect(next).To(Equal("b"), "the class's marker, buckets.cc:134-136")
			ents, next, more, err = s.ListUserBuckets(ctx, alice, next, 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1))
			Expect(ents[0].Bucket.Name).To(Equal("c"))
			Expect(ents[0].Bucket.Marker).To(Equal("m-c"))
			Expect(ents[0].Bucket.ID).To(Equal("m-c"))
			Expect(more).To(BeFalse())
			Expect(next).To(BeEmpty())
		})

		It("lists nothing for an owner without a buckets object", func(ctx SpecContext) {
			ents, next, more, err := s.ListUserBuckets(ctx, alice, "", 10)
			Expect(err).NotTo(HaveOccurred(), "ENOENT is an empty list, buckets.cc:108-111")
			Expect(ents).To(BeEmpty())
			Expect(next).To(BeEmpty())
			Expect(more).To(BeFalse())
		})

		It("gives listed buckets the owner's tenant", func(ctx SpecContext) {
			t1 := meta.UserOwner(meta.UserID{Tenant: "t1", ID: "alice"})
			Expect(driver.LinkBucket(s, ctx, t1, meta.BucketID{Tenant: "t1", Name: "x", Marker: "m", ID: "m"}, time.Now())).To(Succeed())
			Expect(c.Object(rookMetaPool, uids, "t1$alice.buckets")).NotTo(BeNil())
			ents, _, _, err := s.ListUserBuckets(ctx, t1, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1))
			Expect(ents[0].Bucket.Tenant).To(Equal("t1"))
		})

		It("uses the account's buckets object and tenant for an account owner", func(ctx SpecContext) {
			acct := meta.NewAccountInfo()
			acct.ID, acct.Tenant = "RGW00000000000000001", "t9"
			c.Put(rookMetaPool, "accounts", "account.RGW00000000000000001", encode(acct))
			owner := meta.AccountOwner("RGW00000000000000001")
			Expect(driver.LinkBucket(s, ctx, owner, bucket("z"), time.Now())).To(Succeed())
			Expect(c.Object(rookMetaPool, "accounts", "buckets.RGW00000000000000001")).NotTo(BeNil(), "account.cc:44-50")
			ents, _, _, err := s.ListUserBuckets(ctx, owner, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1))
			Expect(ents[0].Bucket.Tenant).To(Equal("t9"))
		})

		It("unlinks a bucket", func(ctx SpecContext) {
			Expect(driver.LinkBucket(s, ctx, alice, bucket("a"), time.Now())).To(Succeed())
			Expect(driver.UnlinkBucket(s, ctx, alice, bucket("a"))).To(Succeed())
			ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(BeEmpty())
		})

		It("unlinks a bucket the list does not hold without creating the list", func(ctx SpecContext) {
			Expect(driver.UnlinkBucket(s, ctx, alice, bucket("a"))).To(Succeed(), "idempotent removal, cls_user.cc:263-266")
			Expect(c.Object(rookMetaPool, uids, "alice.buckets")).To(BeNil())
		})

		It("refuses a bucket without a name, as the class does", func(ctx SpecContext) {
			Expect(driver.LinkBucket(s, ctx, alice, meta.BucketID{Marker: "m", ID: "m"}, time.Now())).
				To(MatchError(radosclient.ErrInvalid), "get_existing_bucket_entry, cls_user.cc:53-55")
			Expect(c.Object(rookMetaPool, uids, "alice.buckets")).To(BeNil())
		})

		It("stamps a link without a creation time with the clock", func(ctx SpecContext) {
			now := time.Date(2026, 9, 2, 3, 4, 5, 6000, time.UTC)
			driver.SetClock(s, func() time.Time { return now })
			Expect(driver.LinkBucket(s, ctx, alice, bucket("a"), time.Time{})).To(Succeed())
			ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1))
			Expect(ents[0].CreationTime.Time).To(BeTemporally("==", now), "buckets.cc:52-56")
		})

		It("records an explicitly placed bucket's pools in its entry, as rgw_bucket::convert does", func(ctx SpecContext) {
			b := bucket("a")
			b.ExplicitPlacement = meta.DataPlacement{
				DataPool:      meta.Pool{Name: "legacy.data"},
				DataExtraPool: meta.Pool{Name: "legacy.extra", NS: "x"},
				IndexPool:     meta.Pool{Name: "legacy.index"},
			}
			Expect(driver.LinkBucket(s, ctx, alice, b, time.Now())).To(Succeed())
			d := denc.NewDecoder(c.Object(rookMetaPool, uids, "alice.buckets").Omap["a"])
			e := user.DecodeBucketEntry(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(e.Bucket).To(Equal(user.Bucket{
				Name: "a", Marker: "m-a", BucketID: "m-a",
				DataPool: "legacy.data", DataExtraPool: "legacy.extra:x", IndexPool: "legacy.index",
			}), "rgw_basic_types.cc:52-60")
		})

		It("keeps a re-linked entry's stats and takes its new bucket id and creation time", func(ctx SpecContext) {
			t1, t2 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
			info := sharded(0)
			seedShardHeader(c, index, ".dir.m-a", main(15, 8192, 3))
			Expect(driver.LinkBucket(s, ctx, alice, info.Bucket, t1)).To(Succeed())
			_, err := driver.SyncBucketOwnerStats(s, ctx, alice, &info)
			Expect(err).NotTo(HaveOccurred())
			Expect(driver.LinkBucket(s, ctx, alice, meta.BucketID{Name: "a", Marker: "m-a2", ID: "m-a2"}, t2)).To(Succeed())
			ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1))
			Expect(ents[0].Bucket.ID).To(Equal("m-a2"), "cls_user.cc:158-160")
			Expect(ents[0].Bucket.Marker).To(Equal("m-a"), "the marker is kept")
			Expect(ents[0].CreationTime.Time).To(BeTemporally("==", t2), "cls_user.cc:161-162")
			Expect(ents[0].Size).To(BeEquivalentTo(15), "the stats are kept, cls_user.cc:175-180")
			stats, _, _, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(stats).To(Equal(op.Stats{Size: 15, SizeRounded: 8192, NumObjects: 3}))
		})

		It("lists past the class's 1000-entry page while fewer than max are collected", func(ctx SpecContext) {
			entries := map[string][]byte{}
			for i := range 1001 {
				n := fmt.Sprintf("b%04d", i)
				entries[n] = entryOf(n)
			}
			seedEntries(entries)
			ents, next, more, err := s.ListUserBuckets(ctx, alice, "", 1500)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1001), "two class calls, buckets.cc:97-132")
			Expect(ents[1000].Bucket.Name).To(Equal("b1000"))
			Expect(more).To(BeFalse())
			Expect(next).To(BeEmpty())
			ents, next, more, err = s.ListUserBuckets(ctx, alice, "", 1000)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1000))
			Expect(more).To(BeTrue())
			Expect(next).To(Equal("b0999"))
		})

		It("skips an entry the class cannot decode, as list_buckets does", func(ctx SpecContext) {
			seedEntries(map[string][]byte{"a": entryOf("a"), "b": {1}, "c": entryOf("c")})
			ents, _, more, err := s.ListUserBuckets(ctx, alice, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ents)).To(Equal([]string{"a", "c"}), "cls_user.cc:340-348")
			Expect(more).To(BeFalse())
		})

		It("lists nothing for a maximum of 0 and ends the listing, as radosgw's one class call of 0 entries does", func(ctx SpecContext) {
			for _, n := range []string{"a", "b"} {
				Expect(driver.LinkBucket(s, ctx, alice, bucket(n), time.Now())).To(Succeed())
			}
			reads := c.Reads(rookMetaPool, uids, "alice.buckets")
			ents, next, more, err := s.ListUserBuckets(ctx, alice, "", 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(BeEmpty())
			Expect(next).To(BeEmpty(), "the class truncates before the first key and names none, cls_user.cc:329-353")
			Expect(more).To(BeFalse(), "an empty next marker ends radosgw's listing, rgw_op.cc:2604")
			Expect(c.Reads(rookMetaPool, uids, "alice.buckets")).To(Equal(reads+1), "buckets.cc:97-106")
		})

		It("refuses a negative maximum, which radosgw's unsigned maximum cannot be asked for, without reading", func(ctx SpecContext) {
			Expect(driver.LinkBucket(s, ctx, alice, bucket("a"), time.Now())).To(Succeed())
			reads := c.Reads(rookMetaPool, uids, "alice.buckets")
			_, _, _, err := s.ListUserBuckets(ctx, alice, "", -1)
			Expect(err).To(MatchError(op.ErrInternalError), "buckets.cc:83")
			Expect(c.Reads(rookMetaPool, uids, "alice.buckets")).To(Equal(reads))
		})

		It("fails when the bucket list cannot be read", func(ctx SpecContext) {
			boom := errors.New("osd down")
			rs := open(ctx, &recordingCluster{Cluster: c, readErrs: map[string]error{"alice.buckets": boom}}, nil)
			_, _, _, err := rs.ListUserBuckets(ctx, alice, "", 10)
			Expect(err).To(MatchError(boom))
		})
	})

	Describe("owner stats", func() {
		It("reads zero owner stats when the object is absent, and the header when it exists", func(ctx SpecContext) {
			stats, lastSync, lastUpdate, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred(), "ENOENT is zero stats, buckets.cc:170-172")
			Expect(stats).To(Equal(op.Stats{}))
			Expect(lastSync).To(BeZero())
			Expect(lastUpdate).To(BeZero())
			seedEntries(nil)
			c.Object(rookMetaPool, uids, "alice.buckets").OmapHdr = encode(user.Header{
				Stats:         user.Stats{TotalEntries: 3, TotalBytes: 15, TotalBytesRounded: 8192},
				LastStatsSync: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), LastStatsUpdate: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
			})
			stats, lastSync, lastUpdate, err = driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(stats).To(Equal(op.Stats{Size: 15, SizeRounded: 8192, NumObjects: 3}))
			Expect(lastSync).To(BeTemporally("==", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))
			Expect(lastUpdate).To(BeTemporally("==", time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)))
		})

		It("answers UnknownError for a header the class cannot decode", func(ctx SpecContext) {
			seedEntries(nil)
			c.Object(rookMetaPool, uids, "alice.buckets").OmapHdr = []byte{1}
			_, _, _, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(op.FromRADOS(err, op.ScopeService)).To(MatchError(op.ErrUnknown), "the class's -EIO, cls_user.cc:91-96")
		})

		It("syncs a bucket's Main-category index stats into the owner's entry", func(ctx SpecContext) {
			info := sharded(2)
			seedShardHeader(c, index, ".dir.m-a.0", rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{
				rgwcls.CategoryMain:      {TotalSize: 10, TotalSizeRounded: 4096, NumEntries: 1},
				rgwcls.CategoryMultiMeta: {TotalSize: 99, NumEntries: 9},
			}})
			seedShardHeader(c, index, ".dir.m-a.1", main(5, 4096, 2))
			Expect(driver.LinkBucket(s, ctx, alice, info.Bucket, time.Now())).To(Succeed())
			ent, err := driver.SyncBucketOwnerStats(s, ctx, alice, &info)
			Expect(err).NotTo(HaveOccurred())
			Expect(ent.Size).To(BeEquivalentTo(15), "Main only, svc_bi_rados.cc:414-421")
			Expect(ent.SizeRounded).To(BeEquivalentTo(8192))
			Expect(ent.Count).To(BeEquivalentTo(3))
			Expect(ent.Bucket).To(Equal(info.Bucket))
			Expect(ent.PlacementRule).To(Equal(info.PlacementRule), "svc_bi_rados.cc:424")
			stats, _, _, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(stats).To(Equal(op.Stats{Size: 15, SizeRounded: 8192, NumObjects: 3}))
			ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1))
			Expect(ents[0].Size).To(BeEquivalentTo(15))
			Expect(ents[0].SizeRounded).To(BeEquivalentTo(8192))
			Expect(ents[0].Count).To(BeEquivalentTo(3))
		})

		It("replaces an entry's stats on a later sync and moves the header by the difference", func(ctx SpecContext) {
			a, b := sharded(0), sharded(0)
			b.Bucket = bucket("b")
			seedShardHeader(c, index, ".dir.m-a", main(15, 8192, 3))
			seedShardHeader(c, index, ".dir.m-b", main(100, 102400, 10))
			for _, info := range []*meta.BucketInfo{&a, &b} {
				Expect(driver.LinkBucket(s, ctx, alice, info.Bucket, time.Now())).To(Succeed())
				_, err := driver.SyncBucketOwnerStats(s, ctx, alice, info)
				Expect(err).NotTo(HaveOccurred())
			}
			seedShardHeader(c, index, ".dir.m-a", main(5, 4096, 1))
			_, err := driver.SyncBucketOwnerStats(s, ctx, alice, &a)
			Expect(err).NotTo(HaveOccurred())
			stats, _, _, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(stats).To(Equal(op.Stats{Size: 105, SizeRounded: 106496, NumObjects: 11}), "cls_user.cc:165-187")
		})

		It("skips a bucket the owner's list does not hold, as a sync racing a removal", func(ctx SpecContext) {
			now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
			driver.SetClock(s, func() time.Time { return now })
			info := sharded(0)
			seedShardHeader(c, index, ".dir.m-a", main(15, 8192, 3))
			_, err := driver.SyncBucketOwnerStats(s, ctx, alice, &info)
			Expect(err).NotTo(HaveOccurred(), "the entry must exist, buckets.cc:149; cls_user.cc:151-153")
			ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(BeEmpty())
			stats, _, lastUpdate, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(stats).To(Equal(op.Stats{}))
			Expect(lastUpdate).To(BeTemporally("==", now), "the header is written whatever the entries, cls_user.cc:194-199")
		})

		It("subtracts a synced bucket's stats when it is unlinked", func(ctx SpecContext) {
			info := sharded(0)
			seedShardHeader(c, index, ".dir.m-a", main(15, 8192, 3))
			Expect(driver.LinkBucket(s, ctx, alice, info.Bucket, time.Now())).To(Succeed())
			_, err := driver.SyncBucketOwnerStats(s, ctx, alice, &info)
			Expect(err).NotTo(HaveOccurred())
			Expect(driver.UnlinkBucket(s, ctx, alice, info.Bucket)).To(Succeed())
			stats, _, _, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(stats).To(Equal(op.Stats{}), "cls_user.cc:278-282")
		})

		It("completes a stats sync at the clock's time", func(ctx SpecContext) {
			t0, t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
			driver.SetClock(s, func() time.Time { return t0 })
			Expect(driver.LinkBucket(s, ctx, alice, bucket("a"), time.Now())).To(Succeed())
			driver.SetClock(s, func() time.Time { return t1 })
			Expect(driver.CompleteOwnerStatsSync(s, ctx, alice)).To(Succeed())
			_, lastSync, lastUpdate, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(lastSync).To(BeTemporally("==", t1), "cls_user.cc:225-226")
			Expect(lastUpdate).To(BeTemporally("==", t0), "set_buckets_info's op time, cls_user.cc:194-195")
		})
	})

	Describe("the bucket index", func() {
		It("reads each shard's header, in shard order", func(ctx SpecContext) {
			info := sharded(3)
			for i := range 3 {
				seedShardHeader(c, index, fmt.Sprintf(".dir.m-a.%d", i), rgwcls.DirHeader{Ver: uint64(i) + 7})
			}
			headers, err := driver.ReadShardHeaders(s, ctx, &info)
			Expect(err).NotTo(HaveOccurred())
			Expect(headers).To(HaveLen(3))
			for i, h := range headers {
				Expect(h.Ver).To(BeEquivalentTo(i+7), "shard %d", i)
			}
		})

		It("reads at most rgw_bucket_index_max_aio shards at once", func(ctx SpecContext) {
			gate := newReadGate(2)
			gs := open(ctx, &gatedCluster{Cluster: c, pool: index, gate: gate}, map[string]string{"rgw_bucket_index_max_aio": "2"})
			info := sharded(6)
			for i := range 6 {
				seedShardHeader(c, index, fmt.Sprintf(".dir.m-a.%d", i), main(1, 4096, 1))
			}
			ent, err := driver.ReadIndexStats(gs, ctx, &info)
			Expect(err).NotTo(HaveOccurred())
			Expect(ent.Count).To(BeEquivalentTo(6))
			gate.mu.Lock()
			defer gate.mu.Unlock()
			Expect(gate.peak).To(Equal(2), "cls_bucket_head, svc_bi_rados.cc:342-343")
			Expect(gate.reads).To(ConsistOf(".dir.m-a.0", ".dir.m-a.1", ".dir.m-a.2", ".dir.m-a.3", ".dir.m-a.4", ".dir.m-a.5"))
		})

		It("reads an unsharded index from the bucket's own .dir object", func(ctx SpecContext) {
			info := sharded(0)
			seedShardHeader(c, index, ".dir.m-a", main(15, 8192, 3))
			ent, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).NotTo(HaveOccurred())
			Expect([]uint64{ent.Size, ent.SizeRounded, ent.Count}).To(Equal([]uint64{15, 8192, 3}))
		})

		It("reads a shard without a header as the zero header, as bucket_list does", func(ctx SpecContext) {
			info := sharded(0)
			c.Put(index, "", ".dir.m-a", nil)
			headers, err := driver.ReadShardHeaders(s, ctx, &info)
			Expect(err).NotTo(HaveOccurred(), "read_bucket_header, cls_rgw.cc:464-485")
			Expect(headers).To(HaveLen(1))
			Expect(headers[0].Stats).To(BeEmpty())
		})

		It("reads a shard bucket_init_index created as an empty header, and the class refuses to create it again", func(ctx SpecContext) {
			pool, err := c.Pool(ctx, index, "")
			Expect(err).NotTo(HaveOccurred())
			wop := radosclient.NewWriteOp()
			wop.Create(true)
			rgwcls.BucketInitIndex(wop)
			_, err = pool.Write(ctx, ".dir.m-a", wop, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			info := sharded(0)
			headers, err := driver.ReadShardHeaders(s, ctx, &info)
			Expect(err).NotTo(HaveOccurred())
			Expect(headers).To(HaveLen(1))
			Expect(headers[0].Ver).To(BeEquivalentTo(1), "write_bucket_header's increment, cls_rgw.cc:696-702")
			Expect(headers[0].Stats).To(BeEmpty())
			wop = radosclient.NewWriteOp()
			rgwcls.BucketInitIndex(wop)
			_, err = pool.Write(ctx, ".dir.m-a", wop, radosclient.OpFlagNone)
			Expect(err).To(MatchError(radosclient.ErrInvalid), "index already initialized, cls_rgw.cc:756-759")
		})

		It("fails naming a shard it cannot read", func(ctx SpecContext) {
			info := sharded(3)
			seedShardHeader(c, index, ".dir.m-a.0", main(1, 4096, 1))
			seedShardHeader(c, index, ".dir.m-a.2", main(1, 4096, 1))
			_, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(err).To(MatchError(ContainSubstring(".dir.m-a.1")))
		})

		It("reads the index pool of an explicitly placed bucket", func(ctx SpecContext) {
			info := sharded(0)
			info.Bucket.ExplicitPlacement = meta.DataPlacement{DataPool: meta.Pool{Name: "legacy.data"}, IndexPool: meta.Pool{Name: "legacy.index"}}
			info.PlacementRule = meta.PlacementRule{Name: "gone-placement"}
			seedShardHeader(c, "legacy.index", ".dir.m-a", main(15, 8192, 3))
			ent, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).NotTo(HaveOccurred(), "svc_bi_rados.cc:47-51")
			Expect(ent.Size).To(BeEquivalentTo(15))
		})

		It("reads the zonegroup default placement's index pool for a bucket stored without a rule", func(ctx SpecContext) {
			info := sharded(0)
			info.PlacementRule = meta.PlacementRule{}
			seedShardHeader(c, index, ".dir.m-a", main(15, 8192, 3))
			ent, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).NotTo(HaveOccurred(), "svc_bi_rados.cc:56-59")
			Expect(ent.Size).To(BeEquivalentTo(15))
		})

		It("a bucket whose stored class is absent from the zone still lists", func(ctx SpecContext) {
			info := sharded(0)
			info.PlacementRule = meta.PlacementRule{Name: "default-placement", StorageClass: "GLACIER"}
			seedShardHeader(c, index, ".dir.m-a", main(15, 8192, 3))
			ent, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).NotTo(HaveOccurred(), "the index pool is the placement's whatever the class, svc_bi_rados.cc:60-66")
			Expect(ent.Size).To(BeEquivalentTo(15))
		})

		It("refuses a placement the zone lacks as InvalidArgument", func(ctx SpecContext) {
			info := sharded(0)
			info.PlacementRule = meta.PlacementRule{Name: "elsewhere"}
			_, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).To(MatchError(op.ErrInvalidArgument), "radosgw's -EINVAL, svc_bi_rados.cc:60-64")
			Expect(err).NotTo(MatchError(op.ErrInvalidLocationConstraint))
		})

		It("refuses a rule with a storage class and no name, which radosgw looks up by its empty name", func(ctx SpecContext) {
			info := sharded(0)
			info.PlacementRule = meta.PlacementRule{StorageClass: "GLACIER"}
			seedShardHeader(c, index, ".dir.m-a", main(15, 8192, 3))
			_, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).To(MatchError(op.ErrInvalidArgument), "the rule is not empty, rgw_placement_types.h:29-31; svc_bi_rados.cc:56-64")
			Expect(c.Reads(index, "", ".dir.m-a")).To(BeZero())
		})

		It("opens the default placement's index for a bucket without a rule though the default names a class the zone lacks", func(ctx SpecContext) {
			c2 := fakerados.New()
			c2.SetRequiredOSDRelease("squid")
			c2.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
			c2.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
			ids := seedRookZone(c2, "ceph-objectstore", false)
			d := denc.NewDecoder(c2.Object(meta.RootPool, "", meta.ZoneGroupInfoOID(ids.zgID)).Data)
			zg := meta.DecodeZoneGroup(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			zg.DefaultPlacement.StorageClass = "GLACIER"
			c2.Put(meta.RootPool, "", meta.ZoneGroupInfoOID(ids.zgID), encode(zg))
			s2 := open(ctx, c2, nil)
			Expect(s2.ZoneGroup().DefaultPlacement).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "GLACIER"}))
			info := sharded(0)
			info.PlacementRule = meta.PlacementRule{}
			seedShardHeader(c2, index, ".dir.m-a", main(15, 8192, 3))
			ent, err := driver.ReadIndexStats(s2, ctx, &info)
			Expect(err).NotTo(HaveOccurred(), "only the default's name is taken, svc_bi_rados.cc:56-66")
			Expect(ent.Size).To(BeEquivalentTo(15))
		})

		It("issues no shard read after the first one fails", func(ctx SpecContext) {
			ls := open(ctx, c, map[string]string{"rgw_bucket_index_max_aio": "1"})
			info := sharded(3)
			seedShardHeader(c, index, ".dir.m-a.1", main(1, 4096, 1))
			seedShardHeader(c, index, ".dir.m-a.2", main(1, 4096, 1))
			_, err := driver.ReadIndexStats(ls, ctx, &info)
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(err).To(MatchError(ContainSubstring(".dir.m-a.0")))
			Expect(c.Reads(index, "", ".dir.m-a.0")).To(Equal(1))
			Expect(c.Reads(index, "", ".dir.m-a.1")).To(BeZero(), "CLSRGWConcurrentIO issues nothing after a failure, cls_rgw_client.cc:25-75")
			Expect(c.Reads(index, "", ".dir.m-a.2")).To(BeZero())
		})

		It("reads no shard on a canceled context", func(ctx SpecContext) {
			info := sharded(2)
			seedShardHeader(c, index, ".dir.m-a.0", main(1, 4096, 1))
			seedShardHeader(c, index, ".dir.m-a.1", main(1, 4096, 1))
			_, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).NotTo(HaveOccurred(), "the pool is open from here on")
			before := []int{c.Reads(index, "", ".dir.m-a.0"), c.Reads(index, "", ".dir.m-a.1")}
			cctx, cancel := context.WithCancel(ctx)
			cancel()
			_, err = driver.ReadIndexStats(s, cctx, &info)
			Expect(err).To(MatchError(context.Canceled))
			Expect([]int{c.Reads(index, "", ".dir.m-a.0"), c.Reads(index, "", ".dir.m-a.1")}).To(Equal(before))
		})

		It("refuses a bucket without a bucket id before reading its index", func(ctx SpecContext) {
			info := sharded(0)
			info.Bucket.ID = ""
			_, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).To(MatchError(op.ErrUnknown), "radosgw's -EIO, svc_bi_rados.cc:83-86")
			Expect(c.Reads(index, "", ".dir.")).To(BeZero())
		})
	})

	Describe("the class emulators the specs run on", func() {
		// list runs the user class's list_buckets on alice's bucket list.
		list := func(ctx context.Context, marker, end string, n int32) user.ListBucketsRet {
			GinkgoHelper()
			pool, err := c.Pool(ctx, rookMetaPool, uids)
			Expect(err).NotTo(HaveOccurred())
			rop := radosclient.NewReadOp()
			res := user.ListBuckets(rop, marker, end, n, denc.Squid)
			_, err = pool.Read(ctx, "alice.buckets", rop, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			var ret user.ListBucketsRet
			ret.Entries, ret.Marker, ret.Truncated, err = res.Entries()
			Expect(err).NotTo(HaveOccurred())
			return ret
		}
		listed := func(ret user.ListBucketsRet) []string {
			out := make([]string, 0, len(ret.Entries))
			for i := range ret.Entries {
				out = append(out, ret.Entries[i].Bucket.Name)
			}
			return out
		}

		It("stops list_buckets before its end marker, which ends the listing", func(ctx SpecContext) {
			entries := map[string][]byte{}
			for i := range 10 {
				n := fmt.Sprintf("b%04d", i)
				entries[n] = entryOf(n)
			}
			seedEntries(entries)
			ret := list(ctx, "", "b0003", 5)
			Expect(listed(ret)).To(Equal([]string{"b0000", "b0001", "b0002"}))
			Expect(ret.Truncated).To(BeFalse(), "the end marker clears the truncation, cls_user.cc:335-338")
			Expect(ret.Marker).To(BeEmpty(), "cls_user.cc:351-353")
		})

		It("reads a negative list_buckets count as the class's 1000, as size_t does", func(ctx SpecContext) {
			entries := map[string][]byte{}
			for i := range 1001 {
				n := fmt.Sprintf("b%04d", i)
				entries[n] = entryOf(n)
			}
			seedEntries(entries)
			ret := list(ctx, "", "", -1)
			Expect(ret.Entries).To(HaveLen(1000), "cls_user.cc:310-312")
			Expect(ret.Truncated).To(BeTrue())
			Expect(ret.Marker).To(Equal("b0999"))
		})

		It("leaves the header untouched when remove_bucket removes an unsynced entry", func(ctx SpecContext) {
			old := user.BucketEntry{Bucket: user.Bucket{Name: "old", Marker: "m-old", BucketID: "m-old"}, Size: 5, SizeRounded: 4096, Count: 1}
			seedEntries(map[string][]byte{"old": encode(old)})
			hdr := encode(user.Header{Stats: user.Stats{TotalEntries: 7, TotalBytes: 77, TotalBytesRounded: 8192}})
			c.Object(rookMetaPool, uids, "alice.buckets").OmapHdr = hdr
			Expect(driver.UnlinkBucket(s, ctx, alice, meta.BucketID{Name: "old", Marker: "m-old", ID: "m-old"})).To(Succeed())
			obj := c.Object(rookMetaPool, uids, "alice.buckets")
			Expect(obj.Omap).NotTo(HaveKey("old"))
			Expect(obj.OmapHdr).To(Equal(hdr), "cls_user.cc:278-280")
		})

		It("answers EIO for a shard header the rgw class cannot decode", func(ctx SpecContext) {
			info := sharded(0)
			c.Put(index, "", ".dir.m-a", nil)
			c.Object(index, "", ".dir.m-a").OmapHdr = []byte{1}
			_, err := driver.ReadIndexStats(s, ctx, &info)
			Expect(err).To(MatchError(ContainSubstring(".dir.m-a")))
			var rerr *radosclient.Error
			Expect(errors.As(err, &rerr)).To(BeTrue(), "the OSD's answer reaches the caller")
			Expect(rerr.Errno).To(BeEquivalentTo(syscall.EIO), "read_bucket_header's -EIO, cls_rgw.cc:476-482")
			Expect(op.FromRADOS(err, op.ScopeBucket)).To(MatchError(op.ErrUnknown))
		})

		It("recounts the header a page at a time and keeps only the last page's sum, as reset_user_stats2 does", func(ctx SpecContext) {
			entries := map[string][]byte{}
			for i := range 1001 {
				n := fmt.Sprintf("b%04d", i)
				entries[n] = entryOf(n)
			}
			seedEntries(entries)
			pool, err := c.Pool(ctx, rookMetaPool, uids)
			Expect(err).NotTo(HaveOccurred())
			at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
			var (
				marker string
				acc    user.Stats
				pages  int
			)
			for {
				rop := radosclient.NewReadOp()
				res := user.ResetStats2(rop, at, marker, acc, denc.Squid)
				_, err = pool.Read(ctx, "alice.buckets", rop, radosclient.OpFlagReturnVec)
				Expect(err).NotTo(HaveOccurred())
				var ret user.ResetStats2Ret
				ret, err = res.Result()
				Expect(err).NotTo(HaveOccurred())
				pages++
				marker, acc = ret.Marker, ret.AccStats
				if !ret.Truncated {
					break
				}
			}
			Expect(pages).To(Equal(2))
			Expect(marker).To(BeEmpty())
			stats, lastSync, lastUpdate, err := driver.ReadOwnerStats(s, ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(stats).To(Equal(op.Stats{Size: 1, SizeRounded: 4096, NumObjects: 1}),
				"the class starts every page from zero, cls_user.cc:459 and :482")
			Expect(lastSync).To(BeZero(), "a fresh header, cls_user.cc:457")
			Expect(lastUpdate).To(BeTemporally("==", at))
		})
	})
})
