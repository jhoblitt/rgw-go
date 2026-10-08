package driver_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The owners the bucket specs create for.
var (
	aliceOwner = meta.UserOwner(meta.UserID{ID: "alice"})
	bobOwner   = meta.UserOwner(meta.UserID{ID: "bob"})
)

var _ = Describe("CreateBucket and DeleteBucket", func() {
	const (
		indexPool = "ceph-objectstore.rgw.buckets.index"
		zoneID    = "zone-ceph-objectstore"
		zgID      = "zg-ceph-objectstore"
	)
	var (
		c *fakerados.Cluster
		s *driver.Store
	)
	params := func(name string) op.CreateBucketParams {
		return op.CreateBucketParams{
			Name: name, Owner: aliceOwner, Zonegroup: zgID,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{meta.AttrACL: {1}}, Exclusive: true,
		}
	}
	open := func(ctx context.Context, kv map[string]string) *driver.Store {
		GinkgoHelper()
		st, err := driver.Open(ctx, c, conf(kv), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		return st
	}
	// uncached is a store that reads every metadata object from RADOS, so it
	// sees a change another client makes without a cache notify.
	uncached := func(ctx context.Context) *driver.Store {
		return open(ctx, map[string]string{"rgw_cache_enabled": "false"})
	}
	// caching is a store whose metadata cache serves: its workers run, and
	// every control watch is up.
	caching := func(ctx context.Context) *driver.Store {
		GinkgoHelper()
		st := open(ctx, nil)
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan error, 1)
		go func() { done <- st.Run(runCtx) }()
		DeferCleanup(func() {
			cancel()
			Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive())
		})
		Eventually(func(g Gomega) {
			_, err := st.GetBucket(ctx, "", "probe")
			g.Expect(err).To(MatchError(op.ErrNoSuchBucket))
			n := c.Reads(rookMetaPool, rookRoot, "probe")
			_, err = st.GetBucket(ctx, "", "probe")
			g.Expect(err).To(MatchError(op.ErrNoSuchBucket))
			g.Expect(c.Reads(rookMetaPool, rookRoot, "probe")).To(Equal(n), "the second lookup is served from the cache")
		}).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Succeed())
		return st
	}
	shards := func() map[string]*fakerados.Object { return c.Objects(indexPool, "") }
	instances := func(name string) []string {
		var oids []string
		for oid := range c.Objects(rookMetaPool, rookRoot) {
			if strings.HasPrefix(oid, ".bucket.meta."+name+":") {
				oids = append(oids, oid)
			}
		}
		return oids
	}
	linked := func(ctx context.Context, st *driver.Store, owner meta.Owner) []string {
		GinkgoHelper()
		ents, _, _, err := st.ListUserBuckets(ctx, owner, "", 10)
		Expect(err).NotTo(HaveOccurred())
		ids := make([]string, 0, len(ents))
		for _, e := range ents {
			ids = append(ids, e.Bucket.Name+":"+e.Bucket.ID)
		}
		return ids
	}
	// ghostEntryPoint stores an entry point for name naming an instance that
	// does not exist.
	ghostEntryPoint := func(name string) {
		ep := meta.NewBucketEntryPoint()
		ep.Bucket = meta.BucketID{Name: name, Marker: "ghost", ID: "ghost"}
		ep.Owner, ep.Linked = bobOwner, true
		c.Put(rookMetaPool, rookRoot, name, encode(ep))
	}
	// indexless makes the zone's default placement indexless.
	indexless := func() {
		oid := meta.ZoneInfoOID(zoneID)
		zp := meta.DecodeZoneParams(denc.NewDecoder(c.Object(meta.RootPool, "", oid).Data))
		pi := zp.PlacementPools["default-placement"]
		pi.IndexType = uint8(meta.IndexIndexless)
		zp.PlacementPools["default-placement"] = pi
		c.Put(meta.RootPool, "", oid, encode(zp))
	}
	writesTo := func(oid string) int { return c.Writes(rookMetaPool, rookRoot, oid) }
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = newIndexCluster()
		s = open(ctx, nil)
	})

	Describe("CreateBucket", func() {
		It("creates the index shards, the instance, the entry point and the owner link as radosgw does", func(ctx SpecContext) {
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			id := rec.Info.Bucket.ID
			Expect(id).To(Equal(zoneID+".4155.1"), "create_bucket_id: <zone id>.<instance id>.<n>, rgw_rados.cc:2344-2352")
			Expect(rec.Info.Bucket.Marker).To(Equal(id))
			Expect(rec.Info.Layout.Current.Layout.Normal.NumShards).To(BeEquivalentTo(11), "zone.bucket_index_max_shards")
			Expect(rec.Info.Layout.Logs).To(HaveLen(1), "init_default_bucket_layout adds the in-index log layout")
			Expect(rec.Info.HasInstanceObj).To(BeFalse(), "radosgw stopped setting it in Octopus")
			Expect(rec.Info.Zonegroup).To(Equal(zgID))
			Expect(rec.Info.Owner).To(Equal(aliceOwner))
			for i := range 11 {
				oid := fmt.Sprintf(".dir.%s.%d", id, i)
				Expect(shards()).To(HaveKey(oid), "shard %d", i)
				Expect(shards()[oid].OmapHdr).NotTo(BeEmpty(), "bucket_init_index wrote the header of shard %d", i)
			}
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.plain:"+id)).NotTo(BeNil())
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.plain:"+id).Xattrs).To(HaveKeyWithValue(meta.AttrACL, []byte{1}))
			ep, _, epv, err := driver.ReadEntryPoint(s, ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(ep.Bucket.ID).To(Equal(id))
			Expect(ep.Linked).To(BeTrue())
			Expect(ep.Owner).To(Equal(aliceOwner))
			Expect(epv).To(Equal(rec.EPVersion))
			Expect(rec.EntryPoint.Linked).To(BeTrue())
			Expect(rec.Version.Ver).To(BeEquivalentTo(1))
			Expect(rec.EPVersion.Ver).To(BeEquivalentTo(1))
			Expect(rec.EPVersion.Tag).NotTo(Equal(rec.Version.Tag), "each object gets its own fresh write version")
			Expect(linked(ctx, s, aliceOwner)).To(Equal([]string{"plain:" + id}))
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket).To(Equal(rec.Info.Bucket))
			again, err := s.CreateBucket(ctx, params("second"))
			Expect(err).NotTo(HaveOccurred())
			Expect(again.Info.Bucket.ID).To(Equal(zoneID+".4155.2"), "next_bucket_id counts up")
		})
		It("gives the index rgw_override_bucket_index_max_shards shards, unclamped, when it is set", func(ctx SpecContext) {
			st := open(ctx, map[string]string{"rgw_override_bucket_index_max_shards": "3"})
			rec, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.Layout.Current.Layout.Normal.NumShards).To(BeEquivalentTo(3), "rgw_bucket.cc:2790-2796 at v19.2.6")
			Expect(shards()).To(HaveLen(3))
		})
		DescribeTable("creates the shard objects of an indexless bucket on Squid alone",
			func(ctx SpecContext, release string, wantShards int) {
				c.SetRequiredOSDRelease(release)
				indexless()
				st := open(ctx, nil)
				rec, err := st.CreateBucket(ctx, params("plain"))
				Expect(err).NotTo(HaveOccurred())
				Expect(rec.Info.Layout.Current.Layout.Type).To(Equal(meta.IndexIndexless))
				Expect(rec.Info.Layout.Logs).To(BeEmpty(), "no in-index log for an indexless index")
				Expect(shards()).To(HaveLen(wantShards))
			},
			Entry("Squid's init_index creates them whatever the type, svc_bi_rados.cc:354-372 at v19.2.6", "squid", 11),
			Entry("Tentacle's creates none, svc_bi_rados.cc:456-458 at v20.2.4", "tentacle", 0),
		)
		It("returns the existing record with BucketAlreadyExists and cleans up the losing instance", func(ctx SpecContext) {
			first, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			again, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(again).NotTo(BeNil())
			Expect(again.Info.Bucket.ID).To(Equal(first.Info.Bucket.ID), "the caller decides by owner (rgw_sal_rados.cc:173-185)")
			Expect(instances("plain")).To(HaveLen(1), "the loser's instance was removed, rgw_rados.cc:2434-2447")
			Expect(shards()).To(HaveLen(11), "and its shards")
			Expect(linked(ctx, s, aliceOwner)).To(HaveLen(1))
		})
		It("links an existing bucket of the same owner whose owner-list entry is missing", func(ctx SpecContext) {
			first, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(driver.UnlinkBucket(s, ctx, aliceOwner, first.Info.Bucket)).To(Succeed())
			Expect(linked(ctx, s, aliceOwner)).To(BeEmpty())
			again, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(again.Info.Bucket.ID).To(Equal(first.Info.Bucket.ID))
			Expect(linked(ctx, s, aliceOwner)).To(Equal([]string{"plain:" + first.Info.Bucket.ID}),
				"a partial creation is repaired, rgw_sal_rados.cc:173-190")
		})
		It("leaves another owner's existing bucket alone and links nothing", func(ctx SpecContext) {
			first, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			p := params("plain")
			p.Owner = bobOwner
			again, err := s.CreateBucket(ctx, p)
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(again.Info.Owner).To(Equal(aliceOwner))
			Expect(again.Info.Bucket.ID).To(Equal(first.Info.Bucket.ID))
			Expect(linked(ctx, s, bobOwner)).To(BeEmpty(), "rgw_sal_rados.cc:182-184 returns before the link")
			Expect(instances("plain")).To(HaveLen(1))
		})
		It("unlinks when the entry point was deleted concurrently", func(ctx SpecContext) {
			st := uncached(ctx)
			c.AfterWrite(rookMetaPool, rookRoot, "plain", func() { c.Remove(rookMetaPool, rookRoot, "plain") })
			rec, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred(), "rgw_sal_rados.cc:210-227 answers success")
			Expect(rec).NotTo(BeNil())
			Expect(linked(ctx, st, aliceOwner)).To(BeEmpty(), "unlinked")
		})
		It("unlinks a same-owner re-link whose entry point is deleted after the link", func(ctx SpecContext) {
			st := uncached(ctx)
			first, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(driver.UnlinkBucket(st, ctx, aliceOwner, first.Info.Bucket)).To(Succeed())
			c.AfterWrite(rookMetaPool, "users.uid", "alice.buckets", func() {
				c.AfterWrite(rookMetaPool, "users.uid", "alice.buckets", nil)
				c.Remove(rookMetaPool, rookRoot, "plain")
			})
			_, err = st.CreateBucket(ctx, params("plain"))
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(linked(ctx, st, aliceOwner)).To(BeEmpty())
		})
		It("tries again with a new instance when the bucket it lost to is gone", func(ctx SpecContext) {
			calls := 0
			c.BeforeWrite(rookMetaPool, rookRoot, "plain", func(*fakerados.Object) {
				calls++
				if calls == 1 {
					ghostEntryPoint("plain")
				} else {
					c.Remove(rookMetaPool, rookRoot, "plain")
				}
			})
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.Bucket.ID).To(Equal(zoneID+".4155.2"), "rgw_rados.cc:2425-2429 continues with a new id")
			Expect(rec.Info.Layout.Logs).To(HaveLen(1), "a fresh layout, where radosgw's carries the first try's log")
			Expect(instances("plain")).To(Equal([]string{".bucket.meta.plain:" + rec.Info.Bucket.ID}),
				"the first try's instance is removed, which radosgw leaves behind")
			Expect(shards()).To(HaveLen(11), "and its index")
			Expect(linked(ctx, s, aliceOwner)).To(Equal([]string{"plain:" + rec.Info.Bucket.ID}))
		})
		It("gives up after twenty tries with radosgw's -ENOENT, NoSuchKey with no message", func(ctx SpecContext) {
			c.BeforeWrite(rookMetaPool, rookRoot, "plain", func(*fakerados.Object) { ghostEntryPoint("plain") })
			_, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).To(MatchError(op.ErrNoSuchKey), "rgw_rados.cc:2455-2457 logs and returns -ENOENT")
			Expect(op.AsError(err).Message).To(BeEmpty())
			Expect(instances("plain")).To(BeEmpty())
			Expect(shards()).To(BeEmpty())
			Expect(linked(ctx, s, aliceOwner)).To(BeEmpty())
		})
		It("removes the shards it created when one fails to initialize, and writes nothing else", func(ctx SpecContext) {
			c.FailNextWrite(indexPool, "", ".dir."+zoneID+".4155.1.3", syscall.EIO)
			_, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).To(HaveOccurred())
			Expect(shards()).To(BeEmpty(), "CLSRGWIssueBucketIndexInit::cleanup, cls_rgw_client.cc:236-242")
			Expect(instances("plain")).To(BeEmpty())
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
		})
		It("leaves alone another owner's bucket that lands before its entry point", func(ctx SpecContext) {
			c.BeforeWrite(rookMetaPool, rookRoot, "plain", func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookRoot, "plain", nil)
				p := params("plain")
				p.Owner = bobOwner
				_, err := s.CreateBucket(ctx, p)
				Expect(err).NotTo(HaveOccurred())
			})
			got, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(got.Info.Owner).To(Equal(bobOwner))
			Expect(instances("plain")).To(Equal([]string{".bucket.meta.plain:" + got.Info.Bucket.ID}), "alice's try is removed")
			Expect(linked(ctx, s, aliceOwner)).To(BeEmpty())
			Expect(linked(ctx, s, bobOwner)).To(Equal([]string{"plain:" + got.Info.Bucket.ID}))
		})
		It("removes its try when the bucket it lost to cannot be read", func(ctx SpecContext) {
			c.BeforeWrite(rookMetaPool, rookRoot, "plain", func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookRoot, "plain", nil)
				ghostEntryPoint("plain")
				c.FailNextRead(rookMetaPool, rookRoot, "plain", syscall.EIO)
			})
			_, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).To(HaveOccurred())
			Expect(instances("plain")).To(BeEmpty())
			Expect(shards()).To(BeEmpty())
		})
		It("unlinks its own entry when another owner's bucket replaced the entry point after the link", func(ctx SpecContext) {
			st := uncached(ctx)
			c.AfterWrite(rookMetaPool, "users.uid", "alice.buckets", func() {
				c.AfterWrite(rookMetaPool, "users.uid", "alice.buckets", nil)
				ghostEntryPoint("plain")
			})
			_, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(linked(ctx, st, aliceOwner)).To(BeEmpty(), "radosgw unlinks only for a missing entry point, rgw_sal_rados.cc:218-227")
		})
		It("re-links the owner's live bucket when its own late link replaced that bucket's entry", func(ctx SpecContext) {
			var second *op.BucketRecord
			c.AfterWrite(rookMetaPool, rookRoot, "plain", func() {
				c.AfterWrite(rookMetaPool, rookRoot, "plain", nil)
				first, err := s.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred())
				Expect(s.DeleteBucket(ctx, first)).To(Succeed(), "a DELETE completes before the create links")
				second, err = s.CreateBucket(ctx, params("plain"))
				Expect(err).NotTo(HaveOccurred(), "and the same owner creates the bucket again")
			})
			_, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(linked(ctx, s, aliceOwner)).To(Equal([]string{"plain:" + second.Info.Bucket.ID}),
				"the late link pointed the entry at the deleted instance; the live bucket stays listed, as radosgw keeps it")
		})
		It("returns a link failure after unlinking a new bucket", func(ctx SpecContext) {
			c.FailNextWrite(rookMetaPool, "users.uid", "alice.buckets", syscall.EIO)
			_, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).To(HaveOccurred())
			Expect(linked(ctx, s, aliceOwner)).To(BeEmpty())
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil(), "the bucket stays for its owner to re-create")
		})
	})

	Describe("DeleteBucket", func() {
		It("refuses to delete a bucket with objects and deletes an empty one completely", func(ctx SpecContext) {
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			shard := ".dir." + rec.Info.Bucket.ID + "." + strconv.FormatUint(uint64(shardOf(rec, "k")), 10)
			seedIndexEntry(c, indexPool, shard, entry("k", 1))
			Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty))
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil())
			c.Object(indexPool, "", shard).Omap = map[string][]byte{}
			Expect(s.DeleteBucket(ctx, rec)).To(Succeed())
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
			Expect(instances("plain")).To(BeEmpty())
			Expect(shards()).To(BeEmpty(), "clean_index")
			Expect(linked(ctx, s, aliceOwner)).To(BeEmpty())
			_, err = s.GetBucket(ctx, "", "plain")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
		It("does not count a multipart-namespace entry as content", func(ctx SpecContext) {
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			shard := ".dir." + rec.Info.Bucket.ID + ".0"
			e := entry("k.2~abc.meta", 0)
			e.Key = rgwcls.ObjKey{Name: meta.ObjKey{Name: "k.2~abc.meta", NS: meta.NSMultipart}.IndexKeyName()}
			Expect(e.Key.Name).To(Equal("_multipart_k.2~abc.meta"))
			seedIndexEntry(c, indexPool, shard, e)
			Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "rgw_rados.cc:5220-5227 checks the empty namespace only")
		})
		It("counts an escaped name beginning with an underscore as content", func(ctx SpecContext) {
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			shard := ".dir." + rec.Info.Bucket.ID + "." + strconv.FormatUint(uint64(shardOf(rec, "__under")), 10)
			seedIndexEntry(c, indexPool, shard, entry("_under", 1))
			Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty), "parse_raw_oid unescapes __under into the empty namespace")
		})
		It("syncs the owner's stats before deleting and unlinks last", func(ctx SpecContext) {
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Writes(rookMetaPool, "users.uid", "alice.buckets")).To(Equal(1), "the link")
			Expect(s.DeleteBucket(ctx, rec)).To(Succeed())
			Expect(c.Writes(rookMetaPool, "users.uid", "alice.buckets")).To(Equal(3), "the stats sync, then the unlink")
		})
		It("skips the emptiness check and the stats sync for another zonegroup's bucket", func(ctx SpecContext) {
			p := params("plain")
			p.Zonegroup = "elsewhere"
			rec, err := s.CreateBucket(ctx, p)
			Expect(err).NotTo(HaveOccurred())
			seedIndexEntry(c, indexPool, ".dir."+rec.Info.Bucket.ID+"."+strconv.FormatUint(uint64(shardOf(rec, "k")), 10), entry("k", 1))
			Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "rgw_rados.cc:5188-5192")
			Expect(c.Writes(rookMetaPool, "users.uid", "alice.buckets")).To(Equal(2), "the link and the unlink only")
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
		})
		It("removes the bucket under the entry-point version it re-reads", func(ctx SpecContext) {
			st := uncached(ctx)
			rec, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			c.Object(rookMetaPool, rookRoot, "plain").Xattrs[version.XattrName] = encode(version.ObjVersion{Ver: 9, Tag: "other"})
			Expect(st.DeleteBucket(ctx, rec)).To(Succeed(), "RadosBucket::remove passes a fresh tracker, rgw_sal_rados.cc:441-445")
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
			Expect(instances("plain")).To(BeEmpty())
			Expect(shards()).To(BeEmpty())
			Expect(linked(ctx, st, aliceOwner)).To(BeEmpty())
		})
		It("returns ConcurrentModification and leaves the instance, index and owner link when the entry point changes before its removal", func(ctx SpecContext) {
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			c.BeforeWrite(rookMetaPool, rookRoot, "plain", func(o *fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookRoot, "plain", nil)
				o.Xattrs[version.XattrName] = encode(version.ObjVersion{Ver: 9, Tag: "other"})
			})
			Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrConcurrentModification), "rgw_rados.cc:5279-5285 returns the removal's error")
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil())
			Expect(instances("plain")).To(HaveLen(1))
			Expect(shards()).To(HaveLen(11))
			Expect(linked(ctx, s, aliceOwner)).To(HaveLen(1))
		})
		It("leaves an entry point that names another instance and removes its own", func(ctx SpecContext) {
			st := uncached(ctx)
			rec, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			ghostEntryPoint("plain")
			Expect(st.DeleteBucket(ctx, rec)).To(Succeed(), "rgw_rados.cc:5264-5276")
			ep, _, _, err := driver.ReadEntryPoint(st, ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(ep.Bucket.ID).To(Equal("ghost"))
			Expect(instances("plain")).To(BeEmpty())
			Expect(shards()).To(BeEmpty())
			Expect(linked(ctx, st, aliceOwner)).To(BeEmpty())
		})
		It("carries on when the entry point is already gone", func(ctx SpecContext) {
			st := uncached(ctx)
			rec, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			c.Remove(rookMetaPool, rookRoot, "plain")
			Expect(st.DeleteBucket(ctx, rec)).To(Succeed())
			Expect(instances("plain")).To(BeEmpty())
			Expect(linked(ctx, st, aliceOwner)).To(BeEmpty())
		})
		It("answers NoSuchKey when the instance is already gone, as remove's reload does", func(ctx SpecContext) {
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(driver.RemoveInstance(s, ctx, rec.Info.Bucket)).To(Succeed())
			Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrNoSuchKey), "load_bucket's -ENOENT, rgw_sal_rados.cc:357-360")
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil())
			Expect(linked(ctx, s, aliceOwner)).To(HaveLen(1))
		})
		It("tolerates an index already gone for another zonegroup's bucket", func(ctx SpecContext) {
			p := params("plain")
			p.Zonegroup = "elsewhere"
			rec, err := s.CreateBucket(ctx, p)
			Expect(err).NotTo(HaveOccurred())
			for oid := range shards() {
				c.Remove(indexPool, "", oid)
			}
			Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "IndexCleanWriter ignores ENOENT")
			Expect(instances("plain")).To(BeEmpty())
			Expect(linked(ctx, s, aliceOwner)).To(BeEmpty())
		})
		It("cleans the index generation the reloaded instance names, after a reshard", func(ctx SpecContext) {
			stale, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			cur, err := s.GetBucketInstance(ctx, stale.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			for oid := range shards() {
				c.Remove(indexPool, "", oid)
			}
			gen := meta.NewIndexLayoutGen()
			gen.Gen, gen.Layout.Normal.NumShards = 1, 3
			cur.Info.Layout.Current = gen
			Expect(s.PutBucketInfo(ctx, cur)).To(Succeed())
			for i := range uint32(3) {
				seedShardHeader(c, indexPool, cur.Info.IndexShardOID(gen, i), rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{}})
			}
			Expect(shards()).To(HaveLen(3))
			Expect(s.DeleteBucket(ctx, stale)).To(Succeed())
			Expect(shards()).To(BeEmpty(), "remove reloads the bucket first, rgw_sal_rados.cc:356-360")
			Expect(instances("plain")).To(BeEmpty())
		})
		DescribeTable("removes the instance under the version it reloaded, reading it again when another write changed it",
			func(ctx SpecContext, cached bool) {
				st := uncached(ctx)
				if cached {
					st = caching(ctx)
				}
				rec, err := st.CreateBucket(ctx, params("plain"))
				Expect(err).NotTo(HaveOccurred())
				oid := ".bucket.meta.plain:" + rec.Info.Bucket.ID
				raced := false
				c.BeforeWrite(rookMetaPool, rookRoot, oid, func(o *fakerados.Object) {
					if !raced && o != nil && o.Xattrs[op.RemovingAttr] != nil {
						raced = true
						o.Xattrs[version.XattrName] = encode(version.ObjVersion{Ver: 9, Tag: "other"})
					}
				})
				before := writesTo(oid)
				Expect(st.DeleteBucket(ctx, rec)).To(Succeed())
				Expect(raced).To(BeTrue())
				Expect(writesTo(oid)-before).To(Equal(3), "the removal's claim, then the first removal lost to the change and the second, under the version read again, removed it")
				Expect(instances("plain")).To(BeEmpty())
				Expect(shards()).To(BeEmpty())
				Expect(linked(ctx, st, aliceOwner)).To(BeEmpty())
			},
			Entry("through the metadata cache, which the lost removal leaves stale", true),
			Entry("without the cache", false),
		)
		It("counts an owner object removed before its unlink as unlinked", func(ctx SpecContext) {
			rec, err := s.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			writes := 0
			c.BeforeWrite(rookMetaPool, "users.uid", "alice.buckets", func(*fakerados.Object) {
				writes++
				if writes == 2 {
					c.Remove(rookMetaPool, "users.uid", "alice.buckets")
				}
			})
			Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "the entry went with the object; the stats sync was the first write")
			Expect(writes).To(Equal(2), "the hook removed the owner object before the unlink, its second write")
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
		})
		It("leaves the owner's entry of a bucket re-created between its reads", func(ctx SpecContext) {
			st := uncached(ctx)
			first, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			var second *op.BucketRecord
			c.BeforeRead(rookMetaPool, rookRoot, "plain", func() {
				c.BeforeRead(rookMetaPool, rookRoot, "plain", nil)
				Expect(st.DeleteBucket(ctx, first)).To(Succeed(), "a second DELETE of the same bucket completes")
				second, err = st.CreateBucket(ctx, params("plain"))
				Expect(err).NotTo(HaveOccurred(), "and its owner creates it again")
			})
			Expect(st.DeleteBucket(ctx, first)).To(Succeed(), "the first DELETE skips the entry point naming the new instance")
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil())
			Expect(linked(ctx, st, aliceOwner)).To(Equal([]string{"plain:" + second.Info.Bucket.ID}),
				"radosgw unlinks by name and drops the new bucket from its owner's list, rgw_sal_rados.cc:461-462")
		})
		It("answers Tentacle's NoSuchKey for an indexless bucket it created, and keeps the bucket", func(ctx SpecContext) {
			c.SetRequiredOSDRelease("tentacle")
			indexless()
			st := open(ctx, nil)
			rec, err := st.CreateBucket(ctx, params("plain"))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.DeleteBucket(ctx, rec)).To(MatchError(op.ErrNoSuchKey), "check_bucket_empty lists shard objects Tentacle never created")
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil())
		})
	})
})
