package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"syscall"
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

// renameSteps are the writes of a rename of src to dst, by their index in
// renameWrites.
var renameSteps = []TableEntry{
	Entry("the new entry point", 0),
	Entry("the old instance's new owner and record", 1),
	Entry("the old owner's unlink", 2),
	Entry("the new instance", 3),
	Entry("the new owner's link", 4),
	Entry("the old instance's removal", 5),
	Entry("the old entry point's removal", 6),
	Entry("the record's removal", 7),
}

// entryPointLister is a metadata store whose bucket section lists the
// domain root's entry points, as the RADOS driver's will.
type entryPointLister struct {
	op.MetadataStore
	c *fakerados.Cluster
}

func (l *entryPointLister) List(_ context.Context, section, marker string, _ int) (keys []string, next string, more bool, err error) {
	if section != "bucket" {
		return nil, "", false, op.ErrNotImplemented
	}
	for oid := range l.c.Objects(rookMetaPool, rookRoot) {
		if !strings.HasPrefix(oid, ".bucket.meta.") && oid > marker {
			keys = append(keys, oid)
		}
	}
	slices.Sort(keys)
	if len(keys) > 0 {
		next = keys[len(keys)-1]
	}
	return keys, next, false, nil
}

// lostReplyCluster is c with pools whose write failAfter names lands and
// then answers ETIMEDOUT, as a write does whose reply is lost after the OSD
// applied it, where FailNextWrite fails a write that never lands.
type lostReplyCluster struct {
	*fakerados.Cluster
	mu    sync.Mutex
	oid   string
	n     int
	fired bool
}

// failAfter makes the n-th write to oid from here on land and answer
// ETIMEDOUT.
func (c *lostReplyCluster) failAfter(oid string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.oid, c.n, c.fired = oid, n, false
}

// lose counts a write to oid that landed, and reports whether its reply is
// the one to lose.
func (c *lostReplyCluster) lose(oid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if oid != c.oid || c.n == 0 {
		return false
	}
	c.n--
	c.fired = c.n == 0
	return c.fired
}

// lost reports whether the write failAfter named has landed and lost its
// reply.
func (c *lostReplyCluster) lost() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fired
}

func (c *lostReplyCluster) Pool(ctx context.Context, pool, ns string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, ns)
	if err != nil {
		return nil, err
	}
	return &lostReplyPool{Pool: p, c: c}, nil
}

// lostReplyPool is a pool of a lostReplyCluster.
type lostReplyPool struct {
	radosclient.Pool
	c *lostReplyCluster
}

func (p *lostReplyPool) Write(ctx context.Context, oid string, w *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	v, err := p.Pool.Write(ctx, oid, w, flags)
	if err != nil || !p.c.lose(oid) {
		return v, err
	}
	return v, &radosclient.Error{Errno: int32(syscall.ETIMEDOUT), Op: "write " + oid}
}

// carolOwner is a third owner the bucket admin specs move buckets to.
var carolOwner = meta.UserOwner(meta.UserID{ID: "carol"})

var _ = Describe("BucketAdminStore", func() {
	const (
		indexPool = "ceph-objectstore.rgw.buckets.index"
		zgID      = "zg-ceph-objectstore"
	)
	var (
		c *fakerados.Cluster
		s *driver.Store
	)
	aclOf := func(o meta.Owner, name string) []byte {
		e := denc.NewEncoder()
		acl.DefaultPolicy(o, name).Encode(e, denc.Squid)
		return e.Bytes()
	}
	decodeACL := func(b []byte) acl.Policy {
		GinkgoHelper()
		d := denc.NewDecoder(b)
		p := acl.DecodePolicy(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return p
	}
	createBucket := func(ctx context.Context, name string, owner meta.Owner) *op.BucketRecord {
		GinkgoHelper()
		rec, err := s.CreateBucket(ctx, op.CreateBucketParams{
			Name: name, Owner: owner, Zonegroup: zgID, Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs: map[string][]byte{meta.AttrACL: aclOf(owner, "the owner")}, Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	listOwnerBuckets := func(ctx context.Context, owner meta.Owner) []string {
		GinkgoHelper()
		ents, _, _, err := s.ListUserBuckets(ctx, owner, "", 100)
		Expect(err).NotTo(HaveOccurred())
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Bucket.Name)
		}
		return names
	}
	readEP := func(ctx context.Context, tenant, name string) meta.BucketEntryPoint {
		GinkgoHelper()
		ep, _, _, err := driver.ReadEntryPoint(s, ctx, tenant, name)
		Expect(err).NotTo(HaveOccurred())
		return ep
	}
	objectExists := func(oid string) bool { return c.Object(rookMetaPool, rookRoot, oid) != nil }
	// instancesOf are the names of the instance objects carrying id.
	instancesOf := func(id string) []string {
		names := []string{}
		for oid := range c.Objects(rookMetaPool, rookRoot) {
			if rest, ok := strings.CutPrefix(oid, ".bucket.meta."); ok {
				if name, ok := strings.CutSuffix(rest, ":"+id); ok {
					names = append(names, name)
				}
			}
		}
		return names
	}
	BeforeEach(func(ctx SpecContext) {
		c = newIndexCluster()
		st, err := driver.Open(ctx, c, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		s = st
	})

	Describe("IndexStats", func() {
		It("names the categories and renders the shard strings as radosgw does", func() {
			Expect(driver.CategoryName(rgwcls.CategoryNone)).To(Equal("rgw.none"))
			Expect(driver.CategoryName(rgwcls.CategoryMain)).To(Equal("rgw.main"))
			Expect(driver.CategoryName(rgwcls.CategoryShadow)).To(Equal("rgw.shadow"))
			Expect(driver.CategoryName(rgwcls.CategoryMultiMeta)).To(Equal("rgw.multimeta"))
			Expect(driver.CategoryName(rgwcls.CategoryCloudTiered)).To(Equal("rgw.cloudtiered"))
			Expect(driver.CategoryName(9)).To(Equal("unknown"))
			Expect(driver.ShardString([]string{"5", "", "7"})).To(Equal("0#5,1#,2#7"))
			Expect(driver.ShardString(nil)).To(Equal(""))
		})
		It("sums shard headers per category and renders the shard strings", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			n := int(rec.Info.Layout.Current.Layout.Normal.NumShards)
			Expect(n).To(BeNumerically(">", 3))
			shard := func(i int) string { return fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, i) }
			seedShardHeader(c, indexPool, shard(0), rgwcls.DirHeader{Ver: 9, MasterVer: 2, MaxMarker: "m0", Stats: map[uint8]rgwcls.CategoryStats{
				rgwcls.CategoryMain:      {TotalSize: 4096, TotalSizeRounded: 5000, NumEntries: 3, ActualSize: 4000},
				rgwcls.CategoryMultiMeta: {NumEntries: 1},
			}})
			seedShardHeader(c, indexPool, shard(3), rgwcls.DirHeader{Ver: 4, Stats: map[uint8]rgwcls.CategoryStats{
				rgwcls.CategoryMain: {TotalSize: 1024, TotalSizeRounded: 1024, NumEntries: 1, ActualSize: 1024},
			}})
			st, err := s.IndexStats(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Categories).To(Equal(map[string]op.CategoryStats{
				"rgw.main":      {Size: 5120, SizeRounded: 6024, SizeUtilized: 5024, NumObjects: 4},
				"rgw.multimeta": {NumObjects: 1},
			}))
			Expect(st.Ver).To(HavePrefix("0#9,1#1,2#1,3#4,"), "bucket_init_index leaves each header at version 1")
			Expect(strings.Count(st.Ver, ",")).To(Equal(n - 1))
			Expect(st.MasterVer).To(HavePrefix("0#2,1#0,"))
			Expect(st.MaxMarker).To(HavePrefix("0#m0,1#,"))
		})
		It("reads nothing for an indexless bucket", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			rec.Info.Layout.Current.Layout.Type = meta.IndexIndexless
			c.ResetCounters()
			st, err := s.IndexStats(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(st).To(Equal(op.BucketIndexStats{Categories: map[string]op.CategoryStats{}}))
			Expect(c.Reads(indexPool, "", fmt.Sprintf(".dir.%s.0", rec.Info.Bucket.ID))).To(BeZero())
		})
	})

	Describe("CheckIndex and RebuildIndex", func() {
		var (
			rec  *op.BucketRecord
			oids []string
		)
		shardOID := func(name string) string {
			return rec.Info.IndexShardOID(rec.Info.Layout.Current, shardOf(rec, name))
		}
		BeforeEach(func(ctx SpecContext) {
			rec = createBucket(ctx, "plain", aliceOwner)
			oids = nil
			for i := range rec.Info.Layout.Current.Layout.Normal.NumShards {
				oids = append(oids, rec.Info.IndexShardOID(rec.Info.Layout.Current, i))
			}
			Expect(len(oids)).To(BeNumerically(">", 3))
			seedIndexEntry(c, indexPool, shardOID("a"), entry("a", 10))
			seedIndexEntry(c, indexPool, shardOID("b"), entry("b", 20))
			big := entry("c", 30)
			big.Meta.AccountedSize = 5000
			seedIndexEntry(c, indexPool, shardOID("c"), big)
			gone := entry("gone", 7)
			gone.Exists = false
			seedIndexEntry(c, indexPool, shardOID("gone"), gone)
			flagged := entry("flagged", 9)
			flagged.Flags = rgwcls.FlagVer
			seedIndexEntry(c, indexPool, shardOID("flagged"), flagged)
			upload := entry("", 0)
			upload.Key = rgwcls.ObjKey{Name: meta.ObjKey{Name: "obj.2~abc.meta", NS: meta.NSMultipart}.IndexKeyName()}
			upload.Meta.Category = rgwcls.CategoryMultiMeta
			seedIndexEntry(c, indexPool, shardOID("obj"), upload)
			stored := c.Header(indexPool, "", shardOID("a"))
			stored.TagTimeout, stored.MasterVer, stored.MaxMarker = 30, 2, "m0"
			stored.Stats = map[uint8]rgwcls.CategoryStats{rgwcls.CategoryMain: {NumEntries: 2, TotalSize: 30, TotalSizeRounded: 8192, ActualSize: 30}}
			seedShardHeader(c, indexPool, shardOID("a"), stored)
		})
		calculated := func() map[string]op.CategoryStats {
			return map[string]op.CategoryStats{
				"rgw.main":      {Size: 5030, SizeRounded: 16384, SizeUtilized: 60, NumObjects: 3},
				"rgw.multimeta": {NumObjects: 1},
			}
		}

		It("reports every shard's stored header and the header its existing unflagged entries make, summed", func(ctx SpecContext) {
			c.ResetCounters()
			existing, calc, err := s.CheckIndex(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(existing).To(Equal(map[string]op.CategoryStats{"rgw.main": {Size: 30, SizeRounded: 8192, SizeUtilized: 30, NumObjects: 2}}))
			Expect(calc).To(Equal(calculated()))
			for _, oid := range oids {
				Expect(c.Reads(indexPool, "", oid)).To(Equal(1), "shard %s read once", oid)
				Expect(c.Writes(indexPool, "", oid)).To(BeZero(), "shard %s not written", oid)
			}
		})
		It("rebuilds every shard's header from its entries, as bucket_rebuild_index writes it", func(ctx SpecContext) {
			before := c.Header(indexPool, "", shardOID("a"))
			c.ResetCounters()
			Expect(s.RebuildIndex(ctx, rec)).To(Succeed())
			for _, oid := range oids {
				Expect(c.Writes(indexPool, "", oid)).To(Equal(1), "shard %s written once", oid)
			}
			st, err := s.IndexStats(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Categories).To(Equal(calculated()))
			after := c.Header(indexPool, "", shardOID("a"))
			Expect(after.Ver).To(Equal(before.Ver+1), "write_bucket_header counts the write")
			Expect(after.TagTimeout).To(Equal(uint64(30)), "the tag timeout is kept")
			Expect(after.MasterVer).To(BeZero(), "check_index's header copies no master version")
			Expect(after.MaxMarker).To(BeEmpty(), "nor a max marker")
			existing, calc, err := s.CheckIndex(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(existing).To(Equal(calc))
		})
		It("answers a shard the OSD does not find NoSuchKey, radosgw's -ENOENT", func(ctx SpecContext) {
			c.Remove(indexPool, "", oids[len(oids)-1])
			_, _, err := s.CheckIndex(ctx, rec)
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})
	})

	Describe("RemoveIndexEntries", func() {
		var (
			rec  *op.BucketRecord
			part meta.ObjKey
		)
		shardOID := func(name string) string {
			return rec.Info.IndexShardOID(rec.Info.Layout.Current, shardOf(rec, name))
		}
		BeforeEach(func(ctx SpecContext) {
			rec = createBucket(ctx, "plain", aliceOwner)
			part = meta.ObjKey{Name: "obj.2~abc.1", NS: meta.NSMultipart}
		})

		It("removes a multipart entry from the shard of its object, and a plain one from its own, one omap_rm_keys a shard", func(ctx SpecContext) {
			Expect(shardOf(rec, part.Name)).NotTo(Equal(shardOf(rec, "obj")), "the part's own name hashes elsewhere")
			mine, decoy := shardOID("obj"), shardOID(part.Name)
			partEntry := entry("", 1)
			partEntry.Key = rgwcls.ObjKey{Name: part.IndexKeyName()}
			metaEntry := entry("", 0)
			metaEntry.Key = rgwcls.ObjKey{Name: meta.ObjKey{Name: "obj.2~abc.meta", NS: meta.NSMultipart}.IndexKeyName()}
			seedIndexEntry(c, indexPool, mine, partEntry)
			seedIndexEntry(c, indexPool, mine, metaEntry)
			seedIndexEntry(c, indexPool, mine, entry("obj", 3))
			seedIndexEntry(c, indexPool, decoy, partEntry)
			seedIndexEntry(c, indexPool, shardOID("plain-obj"), entry("plain-obj", 4))
			c.ResetCounters()

			Expect(s.RemoveIndexEntries(ctx, rec, []meta.ObjKey{{Name: "obj"}, part, {Name: "plain-obj"}, part})).To(Succeed())
			_, ok := c.Entry(indexPool, "", mine, "_multipart_obj.2~abc.1")
			Expect(ok).To(BeFalse(), "the part's entry is gone from its object's shard")
			_, ok = c.Entry(indexPool, "", mine, "obj")
			Expect(ok).To(BeFalse())
			_, ok = c.Entry(indexPool, "", mine, "_multipart_obj.2~abc.meta")
			Expect(ok).To(BeTrue(), "an entry not named stays")
			_, ok = c.Entry(indexPool, "", decoy, "_multipart_obj.2~abc.1")
			Expect(ok).To(BeTrue(), "the shard the part's own name hashes to is untouched")
			_, ok = c.Entry(indexPool, "", shardOID("plain-obj"), "plain-obj")
			Expect(ok).To(BeFalse())

			w := c.WritesTo(indexPool, "", mine)
			Expect(w).To(HaveLen(1), "one write for the shard's keys, a std::set of them")
			Expect(w[0].Steps()).To(ConsistOf(&radosclient.OmapRmKeysStep{Keys: []string{"_multipart_obj.2~abc.1", "obj"}}))
			for i := range rec.Info.Layout.Current.Layout.Normal.NumShards {
				oid := rec.Info.IndexShardOID(rec.Info.Layout.Current, i)
				if oid != mine && oid != shardOID("plain-obj") {
					Expect(c.Writes(indexPool, "", oid)).To(BeZero(), "shard %s untouched", oid)
				}
			}
		})
		It("shards a multipart name with fewer than two dots by the empty key, as RGWMPObj::from_meta leaves it", func(ctx SpecContext) {
			odd := meta.ObjKey{Name: "orph.1", NS: meta.NSMultipart}
			e := entry("", 1)
			e.Key = rgwcls.ObjKey{Name: odd.IndexKeyName()}
			seedIndexEntry(c, indexPool, shardOID(""), e)
			Expect(s.RemoveIndexEntries(ctx, rec, []meta.ObjKey{odd})).To(Succeed())
			_, ok := c.Entry(indexPool, "", shardOID(""), odd.IndexKeyName())
			Expect(ok).To(BeFalse())
		})
		It("removes from the one object of an unsharded index", func(ctx SpecContext) {
			rec.Info.Layout.Current.Layout.Normal.NumShards = 0
			oid := rec.Info.IndexShardOID(rec.Info.Layout.Current, 0)
			e := entry("", 1)
			e.Key = rgwcls.ObjKey{Name: part.IndexKeyName()}
			seedIndexEntry(c, indexPool, oid, e)
			Expect(s.RemoveIndexEntries(ctx, rec, []meta.ObjKey{part})).To(Succeed())
			_, ok := c.Entry(indexPool, "", oid, part.IndexKeyName())
			Expect(ok).To(BeFalse())
		})
		It("refuses an indexless bucket, writing nothing", func(ctx SpecContext) {
			rec.Info.Layout.Current.Layout.Type = meta.IndexIndexless
			c.ResetCounters()
			Expect(s.RemoveIndexEntries(ctx, rec, []meta.ObjKey{part})).To(MatchError(op.ErrInvalidArgument))
			Expect(c.WriteOrder(indexPool, "")).To(BeEmpty())
		})
	})

	Describe("ChangeBucketOwner", func() {
		It("changes the owner: ACL, instance owner, entry point and the two cls_user lists", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", nil)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(bobOwner))
			pol := decodeACL(got.Attrs[meta.AttrACL])
			Expect(pol.Owner).To(Equal(acl.Owner{ID: "bob", DisplayName: "Bob"}))
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("plain"))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
			ep := readEP(ctx, "", "plain")
			Expect(ep.Owner).To(Equal(bobOwner))
			Expect(ep.Linked).To(BeTrue())
			Expect(ep.Bucket.ID).To(Equal(rec.Info.Bucket.ID))
			Expect(rec.Version).To(Equal(got.Version), "rec follows the writes")
			Expect(rec.EPVersion).To(Equal(got.EPVersion))
		})
		It("writes in radosgw's order: the old owner's unlink, the instance, the new owner's link, the entry point", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			alicePool, aliceOID := driver.OwnerBucketsObj(s, aliceOwner)
			_, bobOID := driver.OwnerBucketsObj(s, bobOwner)
			c.ResetCounters()
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", nil)).To(Succeed())
			Expect(c.WriteOrder(alicePool.Name, alicePool.NS)).To(Equal([]string{aliceOID, bobOID}))
			Expect(c.WriteOrder(rookMetaPool, rookRoot)).To(Equal([]string{rec.Info.Bucket.InstanceOID(), "plain"}))
		})
		It("renames through link with a new name", func(ctx SpecContext) {
			rec := createBucket(ctx, "initial-name", aliceOwner)
			id, marker := rec.Info.Bucket.ID, rec.Info.Bucket.Marker
			Expect(s.ChangeBucketOwner(ctx, rec, aliceOwner, "Alice", &meta.BucketID{Name: "renamed-name"})).To(Succeed())
			_, err := s.GetBucket(ctx, "", "initial-name")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			got, err := s.GetBucket(ctx, "", "renamed-name")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(id), "the instance id and marker survive")
			Expect(got.Info.Bucket.Marker).To(Equal(marker))
			Expect(got.Info.Bucket.Name).To(Equal("renamed-name"))
			Expect(objectExists(meta.BucketID{Name: "initial-name", ID: id}.InstanceOID())).To(BeFalse(), "old instance removed")
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(ConsistOf("renamed-name"))
			Expect(rec.Info.Bucket.Name).To(Equal("renamed-name"))
		})
		Describe("of a bucket carrying user.rgw.lc", func() {
			var rec *op.BucketRecord
			var oldKey, newKey string
			BeforeEach(func(ctx SpecContext) {
				rec = createBucket(ctx, "initial-name", aliceOwner)
				Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrLC: {1}}, nil)).To(Succeed())
				Expect(s.SetEntry(ctx, rec)).To(Succeed())
				oldKey = ":initial-name:" + rec.Info.Bucket.Marker
				newKey = ":renamed-name:" + rec.Info.Bucket.Marker
				Expect(lcEntries(c)).To(HaveKey(oldKey))
			})

			It("moves the lifecycle entry to the new name, which radosgw's rename leaves under the old one", func(ctx SpecContext) {
				Expect(s.ChangeBucketOwner(ctx, rec, aliceOwner, "Alice", &meta.BucketID{Name: "renamed-name"})).To(Succeed())
				Expect(lcEntries(c)).To(Equal(map[string]rgwcls.LCEntry{newKey: {Bucket: newKey, Status: rgwcls.LCStatusUninitial}}),
					"the entry's key holds the bucket's name (docs/ceph-upstream-bugs.md)")
				Expect(c.Locks(rookLCPool, rookLCNS, lcShardOf(newKey), "lc_process")).To(BeEmpty())
				Expect(c.Locks(rookLCPool, rookLCNS, lcShardOf(oldKey), "lc_process")).To(BeEmpty())
			})

			It("completes the rename when the new entry cannot be filed", func(ctx SpecContext) {
				c.FailNextWrite(rookLCPool, rookLCNS, lcShardOf(newKey), syscall.EIO)
				Expect(s.ChangeBucketOwner(ctx, rec, aliceOwner, "Alice", &meta.BucketID{Name: "renamed-name"})).To(Succeed())
				got, err := s.GetBucket(ctx, "", "renamed-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Attrs).NotTo(HaveKey(op.RenameIntentAttr), "the rename finished")
				Expect(lcEntries(c)).To(BeEmpty(), "the old entry goes all the same, as its name no longer loads the bucket")
			})

			It("leaves the index alone when only the owner changes", func(ctx SpecContext) {
				before := lcEntries(c)
				Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", nil)).To(Succeed())
				Expect(lcEntries(c)).To(Equal(before))
			})
		})
		It("files no lifecycle entry for a renamed bucket without user.rgw.lc", func(ctx SpecContext) {
			rec := createBucket(ctx, "initial-name", aliceOwner)
			Expect(s.ChangeBucketOwner(ctx, rec, aliceOwner, "Alice", &meta.BucketID{Name: "renamed-name"})).To(Succeed())
			Expect(c.Objects(rookLCPool, rookLCNS)).To(BeEmpty())
		})
		It("refuses a new name another bucket holds, writing nothing", func(ctx SpecContext) {
			rec := createBucket(ctx, "mine", aliceOwner)
			theirs := createBucket(ctx, "theirs", carolOwner)
			c.ResetCounters()
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "theirs"})).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(c.WriteOrder(rookMetaPool, rookRoot)).To(BeEmpty())
			alicePool, _ := driver.OwnerBucketsObj(s, aliceOwner)
			Expect(c.WriteOrder(alicePool.Name, alicePool.NS)).To(BeEmpty())
			got, err := s.GetBucket(ctx, "", "theirs")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(theirs.Info.Bucket.ID))
			Expect(got.Info.Owner).To(Equal(carolOwner))
		})
		It("refuses to change the owner of a bucket without an ACL", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			delete(rec.Attrs, meta.AttrACL)
			Expect(s.PutBucketAttrs(ctx, rec, nil, []string{meta.AttrACL})).To(Succeed())
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", nil)).To(MatchError(op.ErrInvalidArgument))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(ConsistOf("plain"), "nothing unlinked")
		})
		It("answers UnknownError for an ACL that does not decode", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{meta.AttrACL: {1, 2}}, nil)).To(Succeed())
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", nil)).To(MatchError(op.ErrUnknown))
		})
		It("refuses a stale record without writing the instance", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			stale := *rec
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
			Expect(s.ChangeBucketOwner(ctx, &stale, bobOwner, "Bob", nil)).To(MatchError(op.ErrConcurrentModification))
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(aliceOwner))
		})

		// holds is the invariant every failure and every later op keeps for
		// the bucket with id: every instance carrying the id is named by an
		// entry point naming it, so radosgw's stale-instance cleanup leaves
		// it; some name loads the bucket; and at most one owner reaches it,
		// through any name or any owner's bucket list.
		holds := func(ctx context.Context, id string) {
			GinkgoHelper()
			for _, name := range instancesOf(id) {
				ep, _, _, err := driver.ReadEntryPoint(s, ctx, "", name)
				Expect(err).NotTo(HaveOccurred(), "instance %s:%s has no entry point", name, id)
				Expect(ep.Bucket.ID).To(Equal(id), "instance %s:%s is not the one its name loads", name, id)
			}
			reached := map[string]bool{}
			// takenFrom are the owners an unfinished rename's record names it
			// takes the bucket from, whose list entries its retry removes.
			takenFrom := map[string]bool{}
			for _, name := range []string{"src", "dst", "third"} {
				got, err := s.GetBucket(ctx, "", name)
				if err != nil || got.Info.Bucket.ID != id {
					continue
				}
				if i, ok := op.DecodeRenameIntent(got.Attrs); ok {
					for _, o := range i.From {
						takenFrom[o] = true
					}
				}
				reached[got.Info.Owner.String()] = true
				Expect(decodeACL(got.Attrs[meta.AttrACL]).Owner.ID).To(Equal(got.Info.Owner.String()), "name %s: the ACL owner is the instance owner", name)
			}
			Expect(reached).NotTo(BeEmpty(), "no name loads the bucket")
			Expect(reached).To(HaveLen(1), "two owners reach the bucket: %v", reached)
			for _, o := range []meta.Owner{aliceOwner, bobOwner, carolOwner} {
				ents, _, _, err := s.ListUserBuckets(ctx, o, "", 100)
				Expect(err).NotTo(HaveOccurred())
				for _, e := range ents {
					if e.Bucket.ID == id && !takenFrom[o.String()] {
						Expect(reached).To(HaveKey(o.String()), "%s lists the bucket without owning it", o)
					}
				}
			}
		}
		// renameWrites are the RADOS writes of a rename of src to dst for bob,
		// in order: the object and which of its writes fails.
		type failAt struct{ pool, ns, oid string }
		renameWrites := func(id string) []failAt {
			alicePool, aliceOID := driver.OwnerBucketsObj(s, aliceOwner)
			_, bobOID := driver.OwnerBucketsObj(s, bobOwner)
			srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
			dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
			return []failAt{
				{rookMetaPool, rookRoot, "dst"},
				{rookMetaPool, rookRoot, srcInst},
				{alicePool.Name, alicePool.NS, aliceOID},
				{rookMetaPool, rookRoot, dstInst},
				{alicePool.Name, alicePool.NS, bobOID},
				{rookMetaPool, rookRoot, srcInst + "#2"},
				{rookMetaPool, rookRoot, "src"},
				{rookMetaPool, rookRoot, dstInst + "#2"},
			}
		}
		// failRenameWith runs the rename of src to dst for bob with its write
		// at step failing, for the bucket setup changes first, and expects it
		// to fail.
		failRenameWith := func(ctx context.Context, step int, setup func(*op.BucketRecord)) string {
			GinkgoHelper()
			rec := createBucket(ctx, "src", aliceOwner)
			setup(rec)
			id := rec.Info.Bucket.ID
			f := renameWrites(id)[step]
			if oid, second := strings.CutSuffix(f.oid, "#2"); second {
				c.AfterWrite(f.pool, f.ns, oid, func() {
					c.AfterWrite(f.pool, f.ns, oid, nil)
					c.FailNextWrite(f.pool, f.ns, oid, syscall.ETIMEDOUT)
				})
			} else {
				c.FailNextWrite(f.pool, f.ns, f.oid, syscall.ETIMEDOUT)
			}
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).NotTo(Succeed())
			holds(ctx, id)
			return id
		}
		failRename := func(ctx context.Context, step int) string {
			GinkgoHelper()
			return failRenameWith(ctx, step, func(*op.BucketRecord) {})
		}
		// retryRename is the same request again: the old name, or, once it
		// no longer loads, the new name its rename's record names, as
		// LinkBucket finds an unfinished rename.
		retryRename := func(ctx context.Context) error {
			rec, err := s.GetBucket(ctx, "", "src")
			if err != nil {
				Expect(err).To(MatchError(op.ErrNoSuchBucket))
				rec, err = s.GetBucket(ctx, "", "dst")
				Expect(err).NotTo(HaveOccurred())
				i, ok := op.DecodeRenameIntent(rec.Attrs)
				Expect(ok).To(BeTrue(), "the old name is gone and the new name records no rename")
				Expect(i.SrcName).To(Equal("src"))
			}
			return s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})
		}
		completed := func(ctx context.Context, id string) {
			GinkgoHelper()
			holds(ctx, id)
			Expect(instancesOf(id)).To(ConsistOf("dst"))
			_, _, _, err := driver.ReadEntryPoint(s, ctx, "", "src")
			Expect(err).To(MatchError(op.ErrNoSuchBucket), "the old entry point is gone")
			got, err := s.GetBucket(ctx, "", "dst")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(bobOwner))
			Expect(got.Attrs).NotTo(HaveKey(op.RenameIntentAttr), "the rename's record is removed")
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("dst"))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
		}
		// refusedBeforeWriting expects retry, a retry of the rename of the
		// bucket with id, to answer ConcurrentModification having written
		// nothing: no instance, entry point or claim.
		refusedBeforeWriting := func(ctx context.Context, id string, retry func() error) {
			GinkgoHelper()
			c.ResetCounters()
			Expect(retry()).To(MatchError(op.ErrConcurrentModification))
			Expect(c.WriteOrder(rookMetaPool, rookRoot)).To(BeEmpty(), "no instance or entry point written")
			Expect(c.WriteOrder(rookMetaPool, rookRoot+".rgw-go-bucket-claims")).To(BeEmpty(), "no claim written")
			holds(ctx, id)
		}
		// loaded is the bucket name loads.
		loaded := func(ctx context.Context, name string) *op.BucketRecord {
			GinkgoHelper()
			got, err := s.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred())
			return got
		}
		// asBefore is the bucket with id as it was before a rename of src
		// for bob: alice's under src, in alice's list alone, with no record,
		// and no instance of its id under another name.
		asBefore := func(ctx context.Context, id string) {
			GinkgoHelper()
			holds(ctx, id)
			Expect(instancesOf(id)).To(ConsistOf("src"))
			got, err := s.GetBucket(ctx, "", "src")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(id))
			Expect(got.Info.Owner).To(Equal(aliceOwner))
			Expect(decodeACL(got.Attrs[meta.AttrACL]).Owner.ID).To(Equal("alice"))
			Expect(got.Attrs).NotTo(HaveKey(op.RenameIntentAttr))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(ConsistOf("src"))
			Expect(listOwnerBuckets(ctx, bobOwner)).To(BeEmpty())
		}
		DescribeTable("leaves the bucket as it was when a bucket created under the new name wins, at any step",
			func(ctx context.Context, step int) {
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				var raced *op.BucketRecord
				var raceErr error
				race := func() {
					raced, raceErr = s.CreateBucket(ctx, op.CreateBucketParams{
						Name: "dst", Owner: carolOwner, Zonegroup: zgID, Placement: meta.PlacementRule{Name: "default-placement"},
						Attrs: map[string][]byte{meta.AttrACL: aclOf(carolOwner, "Carol")}, Exclusive: true,
					})
				}
				if step < 0 {
					c.BeforeRead(rookMetaPool, rookRoot, "dst", func() {
						c.BeforeRead(rookMetaPool, rookRoot, "dst", nil)
						race()
					})
				} else {
					f := renameWrites(id)[step]
					oid, second := strings.CutSuffix(f.oid, "#2")
					seen := 0
					c.AfterWrite(f.pool, f.ns, oid, func() {
						if seen++; second && seen < 2 {
							return
						}
						c.AfterWrite(f.pool, f.ns, oid, nil)
						race()
					})
				}
				err := s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})
				if err != nil {
					Expect(err).To(MatchError(op.ErrBucketAlreadyExists), "a rename fails only by losing its name")
					Expect(raceErr).NotTo(HaveOccurred())
					Expect(raced.Info.Owner).To(Equal(carolOwner))
					asBefore(ctx, id)
					old, gerr := s.GetBucket(ctx, "", "src")
					Expect(gerr).NotTo(HaveOccurred())
					Expect(s.ChangeBucketOwner(ctx, old, carolOwner, "Carol", nil)).To(Succeed(), "a later link is not refused")
					return
				}
				Expect(raceErr).To(HaveOccurred(), "the name the rename secured is not taken")
				completed(ctx, id)
			},
			Entry("before the new name is read", -1),
			Entry("after the new entry point", 0),
			Entry("after the old instance's new owner and record", 1),
			Entry("after the old owner's unlink", 2),
			Entry("after the new instance", 3),
			Entry("after the new owner's link", 4),
			Entry("after the old instance's removal", 5),
			Entry("after the old entry point's removal", 6),
		)
		It("loses a name created between its check and its claim with nothing written", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			id := rec.Info.Bucket.ID
			// The check reads no entry point; the exclusive claim then meets
			// one, as it would a bucket created in between.
			c.FailNextWrite(rookMetaPool, rookRoot, "dst", syscall.EEXIST)
			c.ResetCounters()
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(c.WriteOrder(rookMetaPool, rookRoot)).To(BeEmpty(), "no write after the refused claim")
			alicePool, _ := driver.OwnerBucketsObj(s, aliceOwner)
			Expect(c.WriteOrder(alicePool.Name, alicePool.NS)).To(BeEmpty())
			asBefore(ctx, id)
		})
		It("loses a name created between its read and its exclusive claim, writing nothing", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			id := rec.Info.Bucket.ID
			var raced *op.BucketRecord
			c.BeforeWrite(rookMetaPool, rookRoot, "dst", func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookRoot, "dst", nil)
				raced = createBucket(ctx, "dst", carolOwner)
			})
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrBucketAlreadyExists))
			got, err := s.GetBucket(ctx, "", "dst")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(raced.Info.Bucket.ID), "the exclusive claim leaves the new bucket its name")
			Expect(got.Info.Owner).To(Equal(carolOwner))
			asBefore(ctx, id)
		})
		It("takes a link to another tenant's same-named name for another rename, not the retry", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			i := op.RenameIntent{SrcName: "src", DstTenant: "t1", DstName: "dst", Owner: "bob"}
			Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.RenameIntentAttr: i.Encode()}, nil)).To(Succeed())
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrConcurrentModification))
			_, _, _, err := driver.ReadEntryPoint(s, ctx, "", "dst")
			Expect(err).To(MatchError(op.ErrNoSuchBucket), "nothing claimed")
		})

		// linkByID is PUT /admin/bucket?bucket=<name>&bucket-id=<id>&uid=<uid>
		// over env.
		linkByID := func(ctx context.Context, env *op.Env, name, id string, uid meta.UserID) error {
			l := op.NewLinkBucket()
			l.UID, l.Bucket, l.BucketID = uid, name, id
			admin := meta.NewUserInfo()
			admin.UserID = meta.UserID{ID: "admin"}
			caps := meta.Caps{"buckets": meta.CapAll}
			return op.Run(ctx, l, &op.Request{Env: env, Identity: op.Identity{User: &admin, Owner: meta.UserOwner(admin.UserID), OpMask: op.OpTypeAll, Caps: caps}})
		}
		// listingEnv is the driver's env with a metadata store that lists the
		// domain root's entry points, as the bucket section will list them.
		listingEnv := func() *op.Env {
			env := s.Env()
			env.Metadata = &entryPointLister{MetadataStore: env.Metadata, c: c}
			return env
		}
		seedCarol := func() meta.UserID {
			u := meta.NewUserInfo()
			u.UserID = meta.UserID{ID: "carol"}
			u.DisplayName = "Carol"
			seedUser(c, u, nil, meta.ObjVersion{Ver: 1, Tag: "_carol"})
			return u.UserID
		}
		// radosgwRename plants what radosgw's own rename of src to dst for
		// bob leaves: dst's instance with the new owner, and, when
		// atLastWrite, dst's entry point and bob's link with src's entry
		// point gone, its instance left with the old owner
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1136-L1183 at v19.2.6).
		radosgwRename := func(ctx context.Context, rec *op.BucketRecord, atLastWrite bool) {
			GinkgoHelper()
			info := rec.Info
			info.Bucket.Name = "dst"
			info.Owner = bobOwner
			Expect(driver.WriteInstance(s, ctx, info, true, meta.ObjVersion{Ver: 1, Tag: "_rgw"})).To(Succeed())
			c.Object(rookMetaPool, rookRoot, info.Bucket.InstanceOID()).Xattrs[meta.AttrACL] = aclOf(bobOwner, "Bob")
			if !atLastWrite {
				return
			}
			ep := meta.NewBucketEntryPoint()
			ep.Bucket, ep.Owner, ep.Linked = info.Bucket, bobOwner, true
			_, err := driver.WriteEntryPoint(s, ctx, ep, true, meta.ObjVersion{}, meta.ObjVersion{Ver: 1, Tag: "_rgwep"})
			Expect(err).NotTo(HaveOccurred())
			Expect(driver.LinkBucket(s, ctx, bobOwner, info.Bucket, info.CreationTime.Time)).To(Succeed())
			Expect(driver.RemoveEntryPoint(s, ctx, "", "src", meta.ObjVersion{})).To(Succeed())
		}
		It("refuses a link by bucket id of the instance radosgw's failed rename left behind its last write", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			id := rec.Info.Bucket.ID
			radosgwRename(ctx, rec, true)
			carol := seedCarol()
			Expect(linkByID(ctx, listingEnv(), "src", id, carol)).To(MatchError(op.ErrBucketAlreadyExists), "dst's entry point names the id")
			Expect(linkByID(ctx, s.Env(), "src", id, carol)).To(MatchError(op.ErrBucketAlreadyExists), "the driver lists the section itself")
			_, err := s.GetBucket(ctx, "", "src")
			Expect(err).To(MatchError(op.ErrNoSuchBucket), "no entry point gives the old instance a name")
			got, err := s.GetBucket(ctx, "", "dst")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(bobOwner))
		})
		It("refuses a link by bucket id of the instance radosgw's failed rename wrote before its link", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			id := rec.Info.Bucket.ID
			radosgwRename(ctx, rec, false)
			bob := meta.UserID{ID: "bob"}
			u := meta.NewUserInfo()
			u.UserID, u.DisplayName = bob, "Bob"
			seedUser(c, u, nil, meta.ObjVersion{Ver: 1, Tag: "_bob"})
			Expect(linkByID(ctx, listingEnv(), "dst", id, bob)).To(MatchError(op.ErrBucketAlreadyExists), "src's entry point names the id")
			_, _, _, err := driver.ReadEntryPoint(s, ctx, "", "dst")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			got, err := s.GetBucket(ctx, "", "src")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(aliceOwner))
		})
		It("still relinks by bucket id an instance only its own name loads", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			carol := seedCarol()
			Expect(linkByID(ctx, listingEnv(), "src", rec.Info.Bucket.ID, carol)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "src")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(meta.UserOwner(carol)))
		})

		Describe("a name whose entry point names no instance", func() {
			It("is freed when the rename that reserved it failed before marking the bucket", func(ctx SpecContext) {
				id := failRename(ctx, 1)
				_, attrs, _, err := driver.ReadEntryPoint(s, ctx, "", "dst")
				Expect(err).NotTo(HaveOccurred())
				Expect(attrs).To(HaveKey(op.RenameIntentAttr), "the claim records the rename")
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "dst")).To(Succeed())
				_, _, _, err = driver.ReadEntryPoint(s, ctx, "", "dst")
				Expect(err).To(MatchError(op.ErrNoSuchBucket))
				asBefore(ctx, id)
				Expect(retryRename(ctx)).To(Succeed(), "the rename can still be made")
				completed(ctx, id)
			})
			It("is freed once the bucket moved elsewhere", func(ctx SpecContext) {
				id := failRename(ctx, 1)
				rec, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred())
				Expect(s.ChangeBucketOwner(ctx, rec, carolOwner, "Carol", &meta.BucketID{Name: "third"})).To(Succeed())
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "dst")).To(Succeed())
				_, _, _, err = driver.ReadEntryPoint(s, ctx, "", "dst")
				Expect(err).To(MatchError(op.ErrNoSuchBucket))
				holds(ctx, id)
				createBucket(ctx, "dst", aliceOwner)
			})
			It("is not freed while the rename that reserved it is live", func(ctx SpecContext) {
				id := failRename(ctx, 3)
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "dst")).To(MatchError(op.ErrConcurrentModification))
				Expect(readEP(ctx, "", "dst").Bucket.ID).To(Equal(id))
				Expect(retryRename(ctx)).To(Succeed())
				completed(ctx, id)
			})
			It("is freed when no rename reserved it, and nothing else is written", func(ctx SpecContext) {
				ep := meta.NewBucketEntryPoint()
				ep.Bucket, ep.Owner, ep.Linked = meta.BucketID{Name: "ghost", Marker: "g", ID: "g"}, aliceOwner, true
				_, err := driver.WriteEntryPoint(s, ctx, ep, true, meta.ObjVersion{}, meta.ObjVersion{Ver: 1, Tag: "_g"})
				Expect(err).NotTo(HaveOccurred())
				c.ResetCounters()
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "ghost")).To(Succeed())
				Expect(c.WriteOrder(rookMetaPool, rookRoot)).To(Equal([]string{"ghost"}))
				createBucket(ctx, "ghost", carolOwner)
			})
			It("removes the entry point only under the version it read", func(ctx SpecContext) {
				ep := meta.NewBucketEntryPoint()
				ep.Bucket, ep.Owner, ep.Linked = meta.BucketID{Name: "ghost", Marker: "g", ID: "g"}, aliceOwner, true
				_, err := driver.WriteEntryPoint(s, ctx, ep, true, meta.ObjVersion{}, meta.ObjVersion{Ver: 1, Tag: "_g"})
				Expect(err).NotTo(HaveOccurred())
				c.BeforeWrite(rookMetaPool, rookRoot, "ghost", func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, "ghost", nil)
					claimed := ep
					claimed.Owner = carolOwner
					_, err := driver.WriteEntryPoint(s, ctx, claimed, false, meta.ObjVersion{}, meta.ObjVersion{})
					Expect(err).NotTo(HaveOccurred())
				})
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "ghost")).To(MatchError(op.ErrConcurrentModification))
				Expect(readEP(ctx, "", "ghost").Owner).To(Equal(carolOwner), "the entry point rewritten after the read stays")
			})
			It("leaves a name without an entry point to NoSuchBucket", func(ctx SpecContext) {
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "nothing")).To(MatchError(op.ErrNoSuchBucket))
			})
		})

		Describe("a removal racing a rename", func() {
			It("refuses the removal when the rename's record lands before the removal's claim", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				inst := rec.Info.Bucket.InstanceOID()
				c.BeforeWrite(rookMetaPool, rookRoot, inst, func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, inst, nil)
					cur, err := s.GetBucket(ctx, "", "src")
					Expect(err).NotTo(HaveOccurred())
					i := op.RenameIntent{SrcName: "src", DstName: "dst", Owner: "bob"}
					Expect(s.PutBucketAttrs(ctx, cur, map[string][]byte{op.RenameIntentAttr: i.Encode()}, nil)).To(Succeed())
				})
				Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrRenamePending))
				Expect(readEP(ctx, "", "src").Bucket.ID).To(Equal(id), "the entry point stays")
				Expect(instancesOf(id)).To(ConsistOf("src"))
				Expect(c.Objects(indexPool, "")).To(HaveKey(fmt.Sprintf(".dir.%s.0", id)), "the index stays")
			})
			It("fails the rename when the removal's claim lands before the rename's record", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				inst := rec.Info.Bucket.InstanceOID()
				c.BeforeWrite(rookMetaPool, rookRoot, inst, func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, inst, nil)
					cur, err := s.GetBucket(ctx, "", "src")
					Expect(err).NotTo(HaveOccurred())
					Expect(s.DeleteBucket(ctx, cur)).To(Succeed())
				})
				Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).NotTo(Succeed())
				Expect(instancesOf(id)).To(BeEmpty(), "no instance carries the removed bucket's id")
				Expect(readEP(ctx, "", "dst").Bucket.ID).To(Equal(id), "the claim alone is left")
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "dst")).To(Succeed())
			})
			It("refuses a rename of a bucket whose removal began, until the removal is retried", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				c.FailNextWrite(rookMetaPool, rookRoot, "src", syscall.ETIMEDOUT)
				Expect(s.DeleteBucket(ctx, rec)).NotTo(Succeed())
				cur, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred())
				Expect(cur.Attrs).To(HaveKey(op.RemovingAttr))
				Expect(s.ChangeBucketOwner(ctx, cur, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrConcurrentModification))
				_, _, _, err = driver.ReadEntryPoint(s, ctx, "", "dst")
				Expect(err).To(MatchError(op.ErrNoSuchBucket), "nothing claimed")
				Expect(s.DeleteBucket(ctx, cur)).To(Succeed())
				Expect(instancesOf(id)).To(BeEmpty())
			})
		})
		Describe("races and residue the re-reviews staged", func() {
			It("refuses a link by bucket id of the instance radosgw's failed rename left, across tenants", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				info := rec.Info
				info.Bucket.Tenant, info.Bucket.Name = "t2", "src"
				info.Owner = bobOwner
				Expect(driver.WriteInstance(s, ctx, info, true, meta.ObjVersion{Ver: 1, Tag: "_rgw"})).To(Succeed())
				c.Object(rookMetaPool, rookRoot, info.Bucket.InstanceOID()).Xattrs[meta.AttrACL] = aclOf(bobOwner, "Bob")
				ep := meta.NewBucketEntryPoint()
				ep.Bucket, ep.Owner, ep.Linked = info.Bucket, bobOwner, true
				_, err := driver.WriteEntryPoint(s, ctx, ep, true, meta.ObjVersion{}, meta.ObjVersion{Ver: 1, Tag: "_rgwep"})
				Expect(err).NotTo(HaveOccurred())
				Expect(driver.RemoveEntryPoint(s, ctx, "", "src", meta.ObjVersion{})).To(Succeed())
				carol := seedCarol()
				Expect(linkByID(ctx, listingEnv(), "src", id, carol)).To(MatchError(op.ErrBucketAlreadyExists), "t2/src loads the id under another tenant")
				_, _, _, err = driver.ReadEntryPoint(s, ctx, "", "src")
				Expect(err).To(MatchError(op.ErrNoSuchBucket))
			})
			It("leaves the bucket as it was when a removal claims it before the rename's mark", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				inst := rec.Info.Bucket.InstanceOID()
				var delErr error
				c.BeforeWrite(rookMetaPool, rookRoot, inst, func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, inst, nil)
					cur, err := s.GetBucket(ctx, "", "src")
					Expect(err).NotTo(HaveOccurred())
					c.FailNextWrite(rookMetaPool, rookRoot, "src", syscall.ETIMEDOUT)
					delErr = s.DeleteBucket(ctx, cur)
				})
				Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrConcurrentModification))
				Expect(delErr).To(HaveOccurred())
				asBefore(ctx, id)
			})
			It("clears a removal's claim when it loses the entry point after it, and answers the race", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				c.BeforeWrite(rookMetaPool, rookRoot, "src", func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, "src", nil)
					cur, err := s.GetBucket(ctx, "", "src")
					Expect(err).NotTo(HaveOccurred())
					Expect(s.UnlinkBucketOwner(ctx, cur, aliceOwner)).To(Succeed())
				})
				Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrRemovalRaced))
				cur, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred(), "the bucket stays")
				Expect(cur.Attrs).NotTo(HaveKey(op.RemovingAttr), "its claim is cleared")
				Expect(s.ChangeBucketOwner(ctx, cur, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(Succeed(), "a rename is not refused")
			})
			It("frees the old name's dangling entry point while the record is live, which the retry completes", func(ctx SpecContext) {
				id := failRename(ctx, 6)
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "src")).To(Succeed())
				holds(ctx, id)
				Expect(retryRename(ctx)).To(Succeed())
				completed(ctx, id)
			})
			It("fails a dangling entry point's removal when a retry claims the name after its checks", func(ctx SpecContext) {
				id := failRename(ctx, 1)
				c.BeforeWrite(rookMetaPool, rookRoot, "dst", func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, "dst", nil)
					Expect(retryRename(ctx)).To(Succeed())
				})
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "dst")).To(MatchError(op.ErrConcurrentModification))
				completed(ctx, id)
			})
			It("fails a retry whose mark the dangling entry point's removal wins, leaving the bucket as it was", func(ctx SpecContext) {
				id := failRename(ctx, 1)
				srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
				var rdeErr error
				c.BeforeWrite(rookMetaPool, rookRoot, srcInst, func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, srcInst, nil)
					rdeErr = s.RemoveDanglingEntryPoint(ctx, "", "dst")
				})
				Expect(retryRename(ctx)).To(MatchError(op.ErrConcurrentModification))
				Expect(rdeErr).NotTo(HaveOccurred())
				_, _, _, err := driver.ReadEntryPoint(s, ctx, "", "dst")
				Expect(err).To(MatchError(op.ErrNoSuchBucket))
				asBefore(ctx, id)
			})
			It("leaves the bucket as it was after a failure at the mark, and frees the new name", func(ctx SpecContext) {
				id := failRename(ctx, 1)
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "dst")).To(Succeed())
				asBefore(ctx, id)
			})
			It("refuses to remove an entry point whose instance exists", func(ctx SpecContext) {
				createBucket(ctx, "live", aliceOwner)
				Expect(s.RemoveDanglingEntryPoint(ctx, "", "live")).To(MatchError(op.ErrConcurrentModification))
				Expect(objectExists("live")).To(BeTrue())
				_, err := s.GetBucket(ctx, "", "live")
				Expect(err).NotTo(HaveOccurred())
			})
			It("keeps a link that rewrote the instance after a removal's claim, and clears the claim", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				c.BeforeWrite(rookMetaPool, rookRoot, "src", func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, "src", nil)
					cur, err := s.GetBucket(ctx, "", "src")
					Expect(err).NotTo(HaveOccurred())
					Expect(s.ChangeBucketOwner(ctx, cur, carolOwner, "Carol", nil)).To(Succeed())
				})
				Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrRemovalRaced))
				cur, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred(), "the bucket stays")
				Expect(cur.Info.Owner).To(Equal(carolOwner), "the link's instance write is kept")
				Expect(decodeACL(cur.Attrs[meta.AttrACL]).Owner.ID).To(Equal(carolOwner.String()))
				Expect(readEP(ctx, "", "src").Owner).To(Equal(carolOwner))
				Expect(listOwnerBuckets(ctx, carolOwner)).To(ConsistOf("src"))
				Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
				Expect(cur.Attrs).NotTo(HaveKey(op.RemovingAttr), "the claim is cleared past the link's write")
				Expect(s.ChangeBucketOwner(ctx, cur, carolOwner, "Carol", &meta.BucketID{Name: "dst"})).To(Succeed(), "a later rename is not refused")
			})
			It("leaves a later removal's claim that a lost removal finds in place of its own", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				inst := rec.Info.Bucket.InstanceOID()
				var later []byte
				c.BeforeWrite(rookMetaPool, rookRoot, "src", func(*fakerados.Object) {
					c.BeforeWrite(rookMetaPool, rookRoot, "src", nil)
					cur, err := s.GetBucket(ctx, "", "src")
					Expect(err).NotTo(HaveOccurred())
					Expect(s.ChangeBucketOwner(ctx, cur, carolOwner, "Carol", nil)).To(Succeed())
					cur, err = s.GetBucket(ctx, "", "src")
					Expect(err).NotTo(HaveOccurred())
					c.FailNextWrite(rookMetaPool, rookRoot, "src", syscall.ETIMEDOUT)
					Expect(s.DeleteBucket(ctx, cur)).NotTo(Succeed(), "the later removal fails after its claim")
					later = c.Object(rookMetaPool, rookRoot, inst).Xattrs[op.RemovingAttr]
				})
				Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrRemovalRaced))
				Expect(later).NotTo(BeEmpty())
				Expect(c.Object(rookMetaPool, rookRoot, inst).Xattrs[op.RemovingAttr]).To(Equal(later), "the later removal's claim stays for its retry")
				cur, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred())
				Expect(cur.Info.Owner).To(Equal(carolOwner))
			})
			It("leaves a plain link that fails at its instance write the old owner's, in no list, until a retry", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				c.FailNextWrite(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID(), syscall.ETIMEDOUT)
				Expect(s.ChangeBucketOwner(ctx, rec, carolOwner, "Carol", nil)).NotTo(Succeed())
				got, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Info.Owner).To(Equal(aliceOwner))
				_, recorded := op.DecodeRenameIntent(got.Attrs)
				Expect(recorded).To(BeFalse(), "a link without a rename records nothing")
				Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
				Expect(listOwnerBuckets(ctx, carolOwner)).To(BeEmpty())
				Expect(s.ChangeBucketOwner(ctx, got, carolOwner, "Carol", nil)).To(Succeed(), "nothing refuses the retry")
				got, err = s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Info.Owner).To(Equal(carolOwner))
				Expect(listOwnerBuckets(ctx, carolOwner)).To(ConsistOf("src"))
			})
			It("removes the ACL owner's entry too when it renames a bucket whose ACL owner differs", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				split := *rec
				split.Attrs = maps.Clone(rec.Attrs)
				split.Attrs[meta.AttrACL] = aclOf(carolOwner, "Carol")
				Expect(s.PutBucketInfo(ctx, &split)).To(Succeed())
				Expect(driver.LinkBucket(s, ctx, carolOwner, rec.Info.Bucket, rec.Info.CreationTime.Time)).To(Succeed())
				Expect(listOwnerBuckets(ctx, carolOwner)).To(ConsistOf("src"))
				cur, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred())
				Expect(s.ChangeBucketOwner(ctx, cur, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(Succeed())
				Expect(listOwnerBuckets(ctx, carolOwner)).To(BeEmpty(), "the ACL owner's entry goes")
				Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
				Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("dst"))
			})
			It("refuses every by-id link while one rename's claim names an instance not yet written", func(ctx SpecContext) {
				failRename(ctx, 3)
				other := createBucket(ctx, "other", aliceOwner)
				carol := seedCarol()
				Expect(linkByID(ctx, listingEnv(), "other", other.Info.Bucket.ID, carol)).To(MatchError(op.ErrConcurrentModification))
				Expect(retryRename(ctx)).To(Succeed())
				Expect(linkByID(ctx, listingEnv(), "other", other.Info.Bucket.ID, carol)).To(Succeed(), "the rename's retry clears it")
			})
		})
		DescribeTable("completes a rename that failed at any write when the same rename is retried",
			func(ctx context.Context, step int) {
				id := failRename(ctx, step)
				Expect(retryRename(ctx)).To(Succeed())
				completed(ctx, id)
			},
			renameSteps,
		)
		DescribeTable("moves the lifecycle entry of a bucket carrying user.rgw.lc when the retry of a rename that failed at any write completes",
			func(ctx context.Context, step int) {
				var srcKey, dstKey string
				id := failRenameWith(ctx, step, func(rec *op.BucketRecord) {
					Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrLC: {1}}, nil)).To(Succeed())
					Expect(s.SetEntry(ctx, rec)).To(Succeed())
					srcKey, dstKey = ":src:"+rec.Info.Bucket.Marker, ":dst:"+rec.Info.Bucket.Marker
				})
				Expect(lcEntries(c)).To(Equal(map[string]rgwcls.LCEntry{srcKey: {Bucket: srcKey}}), "the failed attempt moves no entry")
				Expect(retryRename(ctx)).To(Succeed())
				completed(ctx, id)
				Expect(lcEntries(c)).To(Equal(map[string]rgwcls.LCEntry{dstKey: {Bucket: dstKey}}))
			},
			renameSteps,
		)
		DescribeTable("lets no other link or chown extend a rename that failed at any write",
			func(ctx context.Context, step int) {
				id := failRename(ctx, step)
				_, recorded := func() (op.RenameIntent, bool) {
					for _, n := range []string{"src", "dst"} {
						if got, err := s.GetBucket(ctx, "", n); err == nil {
							if i, ok := op.DecodeRenameIntent(got.Attrs); ok {
								return i, true
							}
						}
					}
					return op.RenameIntent{}, false
				}()
				for _, name := range []string{"src", "dst"} {
					for _, other := range []struct {
						what string
						run  func(*op.BucketRecord) error
					}{
						{"a link to carol", func(rec *op.BucketRecord) error {
							return s.ChangeBucketOwner(ctx, rec, carolOwner, "Carol", nil)
						}},
						{"a link to bob under another name", func(rec *op.BucketRecord) error {
							return s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "third"})
						}},
						{"a chown to carol", func(rec *op.BucketRecord) error {
							return s.ChownBucket(ctx, rec, carolOwner, "Carol")
						}},
						{"an unlink", func(rec *op.BucketRecord) error {
							return s.UnlinkBucketOwner(ctx, rec, rec.Info.Owner)
						}},
					} {
						rec, err := s.GetBucket(ctx, "", name)
						if err != nil {
							continue
						}
						err = other.run(rec)
						if recorded {
							Expect(err).To(MatchError(op.ErrConcurrentModification), "%s through %s after a failure at step %d", other.what, name, step)
						}
						holds(ctx, id)
					}
				}
				if recorded {
					Expect(retryRename(ctx)).To(Succeed())
					completed(ctx, id)
				}
			},
			renameSteps,
		)
		It("takes a rename's record as void once another bucket holds its new name", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			i := op.RenameIntent{SrcName: "src", DstName: "dst", Owner: "bob"}
			Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.RenameIntentAttr: i.Encode()}, nil)).To(Succeed())
			createBucket(ctx, "dst", carolOwner)
			Expect(s.ChangeBucketOwner(ctx, rec, carolOwner, "Carol", nil)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "src")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(carolOwner))
			Expect(got.Attrs).NotTo(HaveKey(op.RenameIntentAttr), "the void record is dropped")
		})
		It("refuses a retry of a rename for another owner until the rename completes", func(ctx SpecContext) {
			id := failRename(ctx, 6)
			rec, err := s.GetBucket(ctx, "", "dst")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChangeBucketOwner(ctx, rec, carolOwner, "Carol", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrConcurrentModification))
			Expect(retryRename(ctx)).To(Succeed())
			completed(ctx, id)
		})
		It("leaves the old entry point that names another bucket by the time the rename removes it", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			_, bobOID := driver.OwnerBucketsObj(s, bobOwner)
			pool, _ := driver.OwnerBucketsObj(s, bobOwner)
			other := meta.NewBucketEntryPoint()
			other.Bucket, other.Owner, other.Linked = meta.BucketID{Name: "src", Marker: "other", ID: "other"}, carolOwner, true
			c.AfterWrite(pool.Name, pool.NS, bobOID, func() {
				c.AfterWrite(pool.Name, pool.NS, bobOID, nil)
				_, err := driver.WriteEntryPoint(s, ctx, other, false, meta.ObjVersion{}, meta.ObjVersion{})
				Expect(err).NotTo(HaveOccurred())
			})
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(Succeed())
			Expect(readEP(ctx, "", "src").Bucket.ID).To(Equal("other"), "the entry point re-pointed at another bucket stays")
		})
		It("removes the old entry point only under the version it read", func(ctx SpecContext) {
			rec := createBucket(ctx, "src", aliceOwner)
			other := meta.NewBucketEntryPoint()
			other.Bucket, other.Owner, other.Linked = meta.BucketID{Name: "src", Marker: "other", ID: "other"}, carolOwner, true
			c.AfterWrite(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID(), func() {
				if c.Object(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID()) != nil {
					return
				}
				c.AfterWrite(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID(), nil)
				_, err := driver.WriteEntryPoint(s, ctx, other, false, meta.ObjVersion{}, meta.ObjVersion{})
				Expect(err).NotTo(HaveOccurred())
			})
			Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrConcurrentModification))
			Expect(readEP(ctx, "", "src").Bucket.ID).To(Equal("other"), "an entry point rewritten after the read stays")
		})
		Describe("a bucket config write made through the old name while a rename runs", func() {
			// writeThroughOldName is an S3 PutBucketPolicy that also enables
			// versioning, made through src as the S3 ops make it: the bucket
			// loaded by its name and written whole under the version loaded.
			writeThroughOldName := func(ctx context.Context, policy string) {
				GinkgoHelper()
				rec, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred())
				rec.Info.Flags |= meta.BucketVersioned
				Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrIAMPolicy: []byte(policy)}, nil)).To(Succeed(), "the write is acknowledged")
			}
			hasWrite := func(rec *op.BucketRecord, policy string) {
				GinkgoHelper()
				Expect(rec.Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(policy)))
				Expect(rec.Info.Flags & meta.BucketVersioned).NotTo(BeZero())
			}
			// reconcile is an operator's answer to a refused retry: the
			// configuration the bucket should keep, policy and versioning,
			// written through both names.
			reconcile := func(ctx context.Context, policy string) {
				GinkgoHelper()
				for _, name := range []string{"src", "dst"} {
					rec, err := s.GetBucket(ctx, "", name)
					Expect(err).NotTo(HaveOccurred())
					rec.Info.Flags |= meta.BucketVersioned
					Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrIAMPolicy: []byte(policy)}, nil)).To(Succeed())
				}
			}
			DescribeTable("is kept under the new name, landing at any step between the copy and the old instance's removal",
				func(ctx context.Context, step int) {
					rec := createBucket(ctx, "src", aliceOwner)
					id := rec.Info.Bucket.ID
					f := renameWrites(id)[step]
					c.AfterWrite(f.pool, f.ns, f.oid, func() {
						c.AfterWrite(f.pool, f.ns, f.oid, nil)
						writeThroughOldName(ctx, `{"v":1}`)
					})
					Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(Succeed())
					completed(ctx, id)
					got, err := s.GetBucket(ctx, "", "dst")
					Expect(err).NotTo(HaveOccurred())
					hasWrite(got, `{"v":1}`)
				},
				Entry("after the old instance's new owner and record", 1),
				Entry("after the old owner's unlink", 2),
				Entry("after the new instance", 3),
				Entry("after the new owner's link", 4),
			)
			It("fails the rename with both names loading the bucket when such writes keep landing, refuses the retry, and completes once reconciled", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
				n := 0
				c.AfterWrite(rookMetaPool, rookRoot, dstInst, func() {
					n++
					writeThroughOldName(ctx, fmt.Sprintf(`{"v":%d}`, n))
				})
				Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrConcurrentModification))
				c.AfterWrite(rookMetaPool, rookRoot, dstInst, nil)
				Expect(n).To(Equal(21), "the first copy, then one after each of the 20 lost removals")
				holds(ctx, id)
				got, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred(), "the old name still loads the bucket")
				Expect(got.Info.Bucket.ID).To(Equal(id))
				hasWrite(got, fmt.Sprintf(`{"v":%d}`, n))

				refusedBeforeWriting(ctx, id, func() error { return retryRename(ctx) })
				hasWrite(loaded(ctx, "src"), fmt.Sprintf(`{"v":%d}`, n))
				reconcile(ctx, fmt.Sprintf(`{"v":%d}`, n))
				Expect(retryRename(ctx)).To(Succeed())
				completed(ctx, id)
				got, err = s.GetBucket(ctx, "", "dst")
				Expect(err).NotTo(HaveOccurred())
				hasWrite(got, fmt.Sprintf(`{"v":%d}`, n))
			})
			It("fails the rename rather than copy over a write made through the new name, keeping both writes", func(ctx SpecContext) {
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
				c.AfterWrite(rookMetaPool, rookRoot, dstInst, func() {
					c.AfterWrite(rookMetaPool, rookRoot, dstInst, nil)
					viaNew, err := s.GetBucket(ctx, "", "dst")
					Expect(err).NotTo(HaveOccurred())
					Expect(s.PutBucketAttrs(ctx, viaNew, map[string][]byte{op.AttrIAMPolicy: []byte(`{"new":1}`)}, nil)).To(Succeed())
					writeThroughOldName(ctx, `{"old":1}`)
				})
				Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrConcurrentModification))
				holds(ctx, id)
				got, err := s.GetBucket(ctx, "", "src")
				Expect(err).NotTo(HaveOccurred())
				hasWrite(got, `{"old":1}`)
				got, err = s.GetBucket(ctx, "", "dst")
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"new":1}`)))
			})
			Describe("once the old instance's removal has lost", func() {
				// beforeRemoval runs fn just before the rename of a bucket
				// with id removes the instance under src: that instance's
				// second write after the rename starts, its mark the first.
				beforeRemoval := func(id string, fn func()) {
					srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
					seen := 0
					c.BeforeWrite(rookMetaPool, rookRoot, srcInst, func(*fakerados.Object) {
						if seen++; seen < 2 {
							return
						}
						c.BeforeWrite(rookMetaPool, rookRoot, srcInst, nil)
						fn()
					})
				}
				It("takes an old instance gone by the time it is read again as removed, and completes", func(ctx SpecContext) {
					rec := createBucket(ctx, "src", aliceOwner)
					id := rec.Info.Bucket.ID
					srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
					beforeRemoval(id, func() {
						Expect(driver.RemoveInstance(s, ctx, meta.BucketID{Name: "src", ID: id})).To(Succeed())
					})
					c.ResetCounters()
					Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(Succeed())
					Expect(c.Writes(rookMetaPool, rookRoot, srcInst)).To(Equal(3), "the mark, the removal staged before the rename's, and the rename's")
					applied := slices.DeleteFunc(c.WriteOrder(rookMetaPool, rookRoot), func(oid string) bool { return oid != srcInst })
					Expect(applied).To(HaveLen(2), "the rename's guarded removal lost to the instance being gone")
					completed(ctx, id)
				})
				It("fails the rename with the error of reading the old instance again, both names loading the bucket, refuses the retry, and completes once reconciled", func(ctx SpecContext) {
					rec := createBucket(ctx, "src", aliceOwner)
					id := rec.Info.Bucket.ID
					srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
					beforeRemoval(id, func() {
						writeThroughOldName(ctx, `{"v":1}`)
						c.FailNextRead(rookMetaPool, rookRoot, srcInst, syscall.ETIMEDOUT)
					})
					Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrRequestTimedOut))
					holds(ctx, id)
					hasWrite(loaded(ctx, "src"), `{"v":1}`)
					Expect(loaded(ctx, "dst").Attrs).NotTo(HaveKey(op.AttrIAMPolicy), "nothing was copied past the failed read")

					refusedBeforeWriting(ctx, id, func() error { return retryRename(ctx) })
					hasWrite(loaded(ctx, "src"), `{"v":1}`)
					reconcile(ctx, `{"v":1}`)
					Expect(retryRename(ctx)).To(Succeed())
					completed(ctx, id)
					hasWrite(loaded(ctx, "dst"), `{"v":1}`)
				})
				It("fails the rename with the error of copying the old instance again, both names loading the bucket, refuses the retry, and completes once reconciled", func(ctx SpecContext) {
					rec := createBucket(ctx, "src", aliceOwner)
					id := rec.Info.Bucket.ID
					dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
					beforeRemoval(id, func() {
						writeThroughOldName(ctx, `{"v":1}`)
						c.FailNextWrite(rookMetaPool, rookRoot, dstInst, syscall.ETIMEDOUT)
					})
					Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrRequestTimedOut))
					holds(ctx, id)
					hasWrite(loaded(ctx, "src"), `{"v":1}`)
					Expect(loaded(ctx, "dst").Attrs).NotTo(HaveKey(op.AttrIAMPolicy), "the failed copy wrote nothing")

					refusedBeforeWriting(ctx, id, func() error { return retryRename(ctx) })
					hasWrite(loaded(ctx, "src"), `{"v":1}`)
					reconcile(ctx, `{"v":1}`)
					Expect(retryRename(ctx)).To(Succeed())
					completed(ctx, id)
					hasWrite(loaded(ctx, "dst"), `{"v":1}`)
				})
			})
		})
		Describe("bucket config writes made through either name between a failed rename and its retry", func() {
			// writeVia is an S3 bucket config write through name, made as the
			// S3 ops make it: the bucket loaded by its name and written whole
			// under the version loaded, with attr set to value, or removed
			// when value is empty.
			writeVia := func(ctx context.Context, name, attr, value string) {
				GinkgoHelper()
				rec, err := s.GetBucket(ctx, "", name)
				Expect(err).NotTo(HaveOccurred())
				if value == "" {
					Expect(s.PutBucketAttrs(ctx, rec, nil, []string{attr})).To(Succeed(), "the removal through %s is acknowledged", name)
					return
				}
				Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{attr: []byte(value)}, nil)).To(Succeed(), "the write through %s is acknowledged", name)
			}
			// infoVia is an S3 write of the bucket's info through name, such
			// as PutBucketVersioning or PutBucketRequestPayment.
			infoVia := func(ctx context.Context, name string, change func(*meta.BucketInfo)) {
				GinkgoHelper()
				rec, err := s.GetBucket(ctx, "", name)
				Expect(err).NotTo(HaveOccurred())
				change(&rec.Info)
				Expect(s.PutBucketInfo(ctx, rec)).To(Succeed(), "the write through %s is acknowledged", name)
			}
			versioned := func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned }
			requesterPays := func(on bool) func(*meta.BucketInfo) {
				return func(i *meta.BucketInfo) { i.RequesterPays = on }
			}
			// retryVia is the rename's retry through name: LinkBucket with
			// bucket=src and new-bucket-name=dst, or bucket=dst and no
			// new-bucket-name, which ChangeBucketOwner takes as the rename its
			// record names.
			retryVia := func(ctx context.Context, name string) error {
				rec, err := s.GetBucket(ctx, "", name)
				Expect(err).NotTo(HaveOccurred())
				var to *meta.BucketID
				if name == "src" {
					to = &meta.BucketID{Name: "dst"}
				}
				return s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", to)
			}
			// refusedRetry expects a retry through name to answer
			// ConcurrentModification having written nothing.
			refusedRetry := func(ctx context.Context, id, name string) {
				GinkgoHelper()
				refusedBeforeWriting(ctx, id, func() error { return retryVia(ctx, name) })
			}
			// both makes write through each name, as an operator reconciles
			// the two after a refused retry.
			both := func(write func(name string)) {
				for _, name := range []string{"src", "dst"} {
					write(name)
				}
			}
			// failAtLinkWith is failRename past the new instance's write, for
			// the bucket setup changes first: both names load the bucket, and
			// the new instance holds what the rename copied.
			failAtLinkWith := func(ctx context.Context, setup func(*op.BucketRecord)) string {
				GinkgoHelper()
				rec := createBucket(ctx, "src", aliceOwner)
				id := rec.Info.Bucket.ID
				setup(rec)
				f := renameWrites(id)[4]
				c.FailNextWrite(f.pool, f.ns, f.oid, syscall.ETIMEDOUT)
				Expect(s.ChangeBucketOwner(ctx, loaded(ctx, "src"), bobOwner, "Bob", &meta.BucketID{Name: "dst"})).NotTo(Succeed())
				holds(ctx, id)
				return id
			}
			failAtLink := func(ctx context.Context) string {
				GinkgoHelper()
				return failAtLinkWith(ctx, func(*op.BucketRecord) {})
			}
			withPolicy := func(ctx context.Context) func(*op.BucketRecord) {
				return func(rec *op.BucketRecord) {
					Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrIAMPolicy: []byte(`{"open":1}`)}, nil)).To(Succeed())
				}
			}
			// failPastMark runs a retry by the old name whose copy onto the
			// new instance fails ETIMEDOUT, after its mark of the old one.
			failPastMark := func(ctx context.Context, id string) {
				GinkgoHelper()
				c.FailNextWrite(rookMetaPool, rookRoot, meta.BucketID{Name: "dst", ID: id}.InstanceOID(), syscall.ETIMEDOUT)
				Expect(retryVia(ctx, "src")).To(MatchError(op.ErrRequestTimedOut))
			}
			// ownersACL is an ACL the new owner writes once the rename gave it
			// the bucket: its own, under another display name than the
			// default the rename wrote.
			ownersACL := func() []byte { return aclOf(bobOwner, "Bob, who set this ACL") }

			// failRetryAtLink runs a retry by the old name that copies onto
			// the new instance under the version it read and then fails at
			// the new owner's link.
			failRetryAtLink := func(ctx context.Context, id string) {
				GinkgoHelper()
				f := renameWrites(id)[4]
				c.FailNextWrite(f.pool, f.ns, f.oid, syscall.ETIMEDOUT)
				Expect(retryVia(ctx, "src")).To(MatchError(op.ErrRequestTimedOut))
			}
			DescribeTable("completes a retry when neither name was written since the last attempt",
				func(ctx context.Context, before func(context.Context, string), retry string) {
					id := failAtLink(ctx)
					before(ctx, id)
					Expect(retryVia(ctx, retry)).To(Succeed())
					completed(ctx, id)
				},
				Entry("after a failure at the new owner's link, retried by the old name", func(context.Context, string) {}, "src"),
				Entry("after a failure at the new owner's link, retried by the new name", func(context.Context, string) {}, "dst"),
				Entry("after a retry failed between its mark and its copy, retried by the old name", failPastMark, "src"),
				Entry("after a retry failed between its mark and its copy, retried by the new name", failPastMark, "dst"),
				Entry("after a retry copied over the new instance and failed at the link, retried by the old name", failRetryAtLink, "src"),
				Entry("after a retry copied over the new instance and failed at the link, retried by the new name", failRetryAtLink, "dst"),
			)
			DescribeTable("refuses a retry after a write through either name since the last attempt, writing nothing and keeping the write, until it is written through both",
				func(ctx context.Context, via, retry, what string) {
					setup := func(*op.BucketRecord) {}
					if what == "removal" {
						setup = withPolicy(ctx)
					}
					id := failAtLinkWith(ctx, setup)
					write := func(name string) {
						switch what {
						case "versioning":
							infoVia(ctx, name, versioned)
						case "acl":
							writeVia(ctx, name, meta.AttrACL, string(ownersACL()))
						case "removal":
							writeVia(ctx, name, op.AttrIAMPolicy, "")
						default:
							writeVia(ctx, name, op.AttrIAMPolicy, `{"between":1}`)
						}
					}
					holdsWrite := func(rec *op.BucketRecord) bool {
						switch what {
						case "versioning":
							return rec.Info.Flags&meta.BucketVersioned != 0
						case "acl":
							return bytes.Equal(rec.Attrs[meta.AttrACL], ownersACL())
						case "removal":
							_, ok := rec.Attrs[op.AttrIAMPolicy]
							return !ok
						}
						return bytes.Equal(rec.Attrs[op.AttrIAMPolicy], []byte(`{"between":1}`))
					}
					other := map[string]string{"src": "dst", "dst": "src"}[via]
					write(via)
					refusedRetry(ctx, id, retry)
					Expect(holdsWrite(loaded(ctx, via))).To(BeTrue(), "the %s through %s is kept", what, via)
					Expect(holdsWrite(loaded(ctx, other))).To(BeFalse(), "nothing reached %s", other)

					both(write)
					Expect(retryVia(ctx, retry)).To(Succeed())
					completed(ctx, id)
					Expect(holdsWrite(loaded(ctx, "dst"))).To(BeTrue(), "the %s the names agree on", what)
				},
				Entry("a policy through the new name, retried by the old", "dst", "src", "policy"),
				Entry("a policy through the old name, retried by the new", "src", "dst", "policy"),
				Entry("a policy through the old name, retried by the old", "src", "src", "policy"),
				Entry("a policy through the new name, retried by the new", "dst", "dst", "policy"),
				Entry("versioning through the new name, retried by the old", "dst", "src", "versioning"),
				Entry("versioning through the old name, retried by the new", "src", "dst", "versioning"),
				Entry("the new owner's ACL through the new name, retried by the old", "dst", "src", "acl"),
				Entry("the new owner's ACL through the old name, retried by the new", "src", "dst", "acl"),
				Entry("a policy removal through the new name, retried by the old", "dst", "src", "removal"),
				Entry("a policy removal through the old name, retried by the new", "src", "dst", "removal"),
			)
			DescribeTable("completes a retry that finds only one instance, keeping a write made through its name, as nothing can be lost",
				func(ctx context.Context, step int, via string) {
					id := failRename(ctx, step)
					writeVia(ctx, via, op.AttrIAMPolicy, `{"one":1}`)
					Expect(retryRename(ctx)).To(Succeed())
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"one":1}`)))
				},
				Entry("the old instance, the copy having failed", 3, "src"),
				Entry("the new instance, the old one removed", 6, "dst"),
			)
			It("refuses a retry after the new instance was made anew, though its version counts as far as the last attempt left it", func(ctx SpecContext) {
				id := failAtLink(ctx)
				got := loaded(ctx, "dst")
				attrs := maps.Clone(got.Attrs)
				attrs[op.AttrIAMPolicy] = []byte(`{"anew":1}`)
				Expect(driver.RemoveInstance(s, ctx, got.Info.Bucket)).To(Succeed())
				Expect(driver.WriteInstanceAttrs(s, ctx, got.Info, attrs)).To(Succeed(), "made anew at version 1 of a fresh tag")
				refusedRetry(ctx, id, "src")
				Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"anew":1}`)))

				both(func(name string) { writeVia(ctx, name, op.AttrIAMPolicy, `{"anew":1}`) })
				Expect(retryVia(ctx, "src")).To(Succeed())
				completed(ctx, id)
				Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"anew":1}`)))
			})
			Describe("after a retry failed between its mark of the old instance and its copy onto the new one", func() {
				It("refuses the next retry after a policy written through the new name met that retry's copy, keeping the policy", func(ctx SpecContext) {
					id := failAtLink(ctx)
					dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
					c.BeforeWrite(rookMetaPool, rookRoot, dstInst, func(*fakerados.Object) {
						c.BeforeWrite(rookMetaPool, rookRoot, dstInst, nil)
						writeVia(ctx, "dst", op.AttrIAMPolicy, `{"open":1}`)
					})
					Expect(retryVia(ctx, "src")).To(MatchError(op.ErrConcurrentModification), "its copy lost to the write")
					refusedRetry(ctx, id, "src")
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"open":1}`)))

					both(func(name string) { writeVia(ctx, name, op.AttrIAMPolicy, `{"open":1}`) })
					Expect(retryVia(ctx, "src")).To(Succeed())
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"open":1}`)))
				})
				DescribeTable("refuses a retry after a policy removal through the new name, keeping the removal",
					func(ctx context.Context, retry string) {
						id := failAtLinkWith(ctx, withPolicy(ctx))
						failPastMark(ctx, id)
						writeVia(ctx, "dst", op.AttrIAMPolicy, "")
						refusedRetry(ctx, id, retry)
						Expect(loaded(ctx, "dst").Attrs).NotTo(HaveKey(op.AttrIAMPolicy), "the removal is kept")
						Expect(loaded(ctx, "src").Attrs).To(HaveKey(op.AttrIAMPolicy))

						both(func(name string) { writeVia(ctx, name, op.AttrIAMPolicy, "") })
						Expect(retryVia(ctx, retry)).To(Succeed())
						completed(ctx, id)
						Expect(loaded(ctx, "dst").Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
					},
					Entry("retried by the new name", "dst"),
					Entry("retried by the old name", "src"),
				)
				It("refuses a retry after requester pays was turned off through the new name, keeping it off", func(ctx SpecContext) {
					id := failAtLinkWith(ctx, func(rec *op.BucketRecord) {
						requesterPays(true)(&rec.Info)
						Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
					})
					failPastMark(ctx, id)
					infoVia(ctx, "dst", requesterPays(false))
					refusedRetry(ctx, id, "src")
					Expect(loaded(ctx, "dst").Info.RequesterPays).To(BeFalse(), "turned off through dst")
					Expect(loaded(ctx, "src").Info.RequesterPays).To(BeTrue())

					both(func(name string) { infoVia(ctx, name, requesterPays(false)) })
					Expect(retryVia(ctx, "src")).To(Succeed())
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Info.RequesterPays).To(BeFalse())
				})
				It("refuses a retry after an ACL set through the new name and set back, though the names then hold the same ACL", func(ctx SpecContext) {
					id := failAtLink(ctx)
					failPastMark(ctx, id)
					base := loaded(ctx, "dst").Attrs[meta.AttrACL]
					writeVia(ctx, "dst", meta.AttrACL, string(ownersACL()))
					writeVia(ctx, "dst", meta.AttrACL, string(base))
					refusedRetry(ctx, id, "src")
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(meta.AttrACL, base), "the ACL set back through dst")

					both(func(name string) { writeVia(ctx, name, meta.AttrACL, string(base)) })
					Expect(retryVia(ctx, "src")).To(Succeed())
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(meta.AttrACL, base))
				})
				It("refuses a retry after two policies through the new name, keeping the second", func(ctx SpecContext) {
					id := failAtLink(ctx)
					failPastMark(ctx, id)
					writeVia(ctx, "dst", op.AttrIAMPolicy, `{"first":1}`)
					writeVia(ctx, "dst", op.AttrIAMPolicy, `{"second":1}`)
					refusedRetry(ctx, id, "dst")
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"second":1}`)))

					both(func(name string) { writeVia(ctx, name, op.AttrIAMPolicy, `{"second":1}`) })
					Expect(retryVia(ctx, "dst")).To(Succeed())
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"second":1}`)))
				})
			})
			Describe("when both names were written since the last attempt", func() {
				DescribeTable("refuses a retry though each name wrote other parts, until the names hold the same configuration",
					func(ctx context.Context, retry string) {
						id := failAtLink(ctx)
						writeVia(ctx, "src", op.AttrIAMPolicy, `{"old":1}`)
						infoVia(ctx, "src", versioned)
						writeVia(ctx, "dst", op.AttrLC, "lifecycle")
						refusedRetry(ctx, id, retry)

						writeVia(ctx, "src", op.AttrLC, "lifecycle")
						writeVia(ctx, "dst", op.AttrIAMPolicy, `{"old":1}`)
						infoVia(ctx, "dst", versioned)
						Expect(retryVia(ctx, retry)).To(Succeed())
						completed(ctx, id)
						got := loaded(ctx, "dst")
						Expect(got.Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"old":1}`)))
						Expect(got.Info.Flags & meta.BucketVersioned).NotTo(BeZero())
						Expect(got.Attrs).To(HaveKeyWithValue(op.AttrLC, []byte("lifecycle")))
					},
					Entry("retried by the old name", "src"),
					Entry("retried by the new name", "dst"),
				)
				DescribeTable("refuses a retry after a policy through one name and one written and removed through the other, until the names agree",
					func(ctx context.Context, other, back, retry string) {
						id := failAtLink(ctx)
						writeVia(ctx, other, op.AttrIAMPolicy, `{"open":1}`)
						writeVia(ctx, back, op.AttrIAMPolicy, `{"x":1}`)
						writeVia(ctx, back, op.AttrIAMPolicy, "")
						refusedRetry(ctx, id, retry)
						Expect(loaded(ctx, back).Attrs).NotTo(HaveKey(op.AttrIAMPolicy), "the removal through %s is kept", back)
						Expect(loaded(ctx, other).Attrs).To(HaveKey(op.AttrIAMPolicy), "the policy through %s is kept", other)

						writeVia(ctx, other, op.AttrIAMPolicy, "")
						Expect(retryVia(ctx, retry)).To(Succeed())
						completed(ctx, id)
						Expect(loaded(ctx, "dst").Attrs).NotTo(HaveKey(op.AttrIAMPolicy), "the names agree on the removal")
					},
					Entry("the removal through the new name, retried by the old", "src", "dst", "src"),
					Entry("the removal through the old name, retried by the new", "dst", "src", "dst"),
				)
				It("refuses a retry while the names hold different changes to the bucket's info, until they agree", func(ctx SpecContext) {
					id := failAtLink(ctx)
					infoVia(ctx, "src", versioned)
					infoVia(ctx, "dst", requesterPays(true))
					refusedRetry(ctx, id, "src")
					Expect(loaded(ctx, "src").Info.Flags & meta.BucketVersioned).NotTo(BeZero())
					Expect(loaded(ctx, "dst").Info.RequesterPays).To(BeTrue())

					infoVia(ctx, "src", requesterPays(true))
					infoVia(ctx, "dst", versioned)
					Expect(retryVia(ctx, "dst")).To(Succeed())
					completed(ctx, id)
					got := loaded(ctx, "dst")
					Expect(got.Info.Flags & meta.BucketVersioned).NotTo(BeZero())
					Expect(got.Info.RequesterPays).To(BeTrue())
				})
			})
			Describe("with concurrent retries", func() {
				It("refuses a retry after two concurrent retries each left one name, writing nothing, until the names are reconciled", func(ctx SpecContext) {
					id := failAtLink(ctx)
					srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
					dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
					bobPool, bobOID := driver.OwnerBucketsObj(s, bobOwner)
					bReady := make(chan struct{})
					release := make(chan struct{})
					// letGo lets B past its mark, at the latest when the spec
					// ends, so a failure in A's retry leaves no goroutine held.
					letGo := sync.OnceFunc(func() { close(release) })
					DeferCleanup(letGo)
					bDone := make(chan error, 1)
					// A's mark lands; B reads both instances as A's mark
					// left them and holds before its own mark; A copies and
					// fails at its link; then B marks, and its copy loses
					// to A's.
					c.AfterWrite(rookMetaPool, rookRoot, srcInst, func() {
						c.AfterWrite(rookMetaPool, rookRoot, srcInst, nil)
						c.BeforeWrite(rookMetaPool, rookRoot, srcInst, func(*fakerados.Object) {
							c.BeforeWrite(rookMetaPool, rookRoot, srcInst, nil)
							close(bReady)
							<-release
						})
						go func() {
							defer GinkgoRecover()
							bDone <- retryVia(ctx, "src")
						}()
						Eventually(bReady).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(BeClosed(), "B reached its mark")
						c.AfterWrite(rookMetaPool, rookRoot, dstInst, func() {
							c.AfterWrite(rookMetaPool, rookRoot, dstInst, nil)
							c.FailNextWrite(bobPool.Name, bobPool.NS, bobOID, syscall.ETIMEDOUT)
						})
					})
					errA := retryVia(ctx, "src")
					letGo()
					var errB error
					Eventually(bDone).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(Receive(&errB))
					Expect(errA).To(MatchError(op.ErrRequestTimedOut), "A failed past its copy")
					Expect(errB).To(MatchError(op.ErrConcurrentModification), "B's copy met A's")

					refusedRetry(ctx, id, "src")
					both(func(name string) { writeVia(ctx, name, op.AttrIAMPolicy, `{"agreed":1}`) })
					Expect(retryVia(ctx, "src")).To(Succeed())
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"agreed":1}`)))
				})
				It("refuses a concurrent retry and the next after a write through the old name between them, keeping the write", func(ctx SpecContext) {
					id := failAtLink(ctx)
					srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
					dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
					bobPool, bobOID := driver.OwnerBucketsObj(s, bobOwner)
					// A's mark lands; s2 goes through the old name; B, the
					// concurrent retry, is refused; A copies and fails at
					// its link.
					c.AfterWrite(rookMetaPool, rookRoot, srcInst, func() {
						c.AfterWrite(rookMetaPool, rookRoot, srcInst, nil)
						writeVia(ctx, "src", op.AttrIAMPolicy, `{"s2":1}`)
						refusedRetry(ctx, id, "src")
						c.AfterWrite(rookMetaPool, rookRoot, dstInst, func() {
							c.AfterWrite(rookMetaPool, rookRoot, dstInst, nil)
							c.FailNextWrite(bobPool.Name, bobPool.NS, bobOID, syscall.ETIMEDOUT)
						})
					})
					Expect(retryVia(ctx, "src")).To(MatchError(op.ErrRequestTimedOut), "A failed past its copy")
					refusedRetry(ctx, id, "src")
					Expect(loaded(ctx, "src").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"s2":1}`)), "the write through src is kept")

					both(func(name string) { writeVia(ctx, name, op.AttrIAMPolicy, `{"s2":1}`) })
					Expect(retryVia(ctx, "src")).To(Succeed())
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"s2":1}`)))
				})
				It("fails a retry whose creation of the new instance meets one a concurrent retry created", func(ctx SpecContext) {
					id := failRename(ctx, 3)
					dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
					c.BeforeWrite(rookMetaPool, rookRoot, dstInst, func(*fakerados.Object) {
						c.BeforeWrite(rookMetaPool, rookRoot, dstInst, nil)
						Expect(retryVia(ctx, "src")).To(Succeed(), "the concurrent retry")
						c.ResetCounters()
					})
					Expect(retryVia(ctx, "src")).To(MatchError(op.ErrConcurrentModification))
					alicePool, _ := driver.OwnerBucketsObj(s, aliceOwner)
					Expect(c.WriteOrder(alicePool.Name, alicePool.NS)).To(BeEmpty(), "the retry stops at the instance it finds created")
					Expect(c.WriteOrder(rookMetaPool, rookRoot)).To(BeEmpty(), "the retry stops at the instance it finds created")
					completed(ctx, id)
				})
			})
			It("keeps a write made through the old name during a retry, copying the old instance again", func(ctx SpecContext) {
				id := failAtLink(ctx)
				srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
				c.AfterWrite(rookMetaPool, rookRoot, srcInst, func() {
					c.AfterWrite(rookMetaPool, rookRoot, srcInst, nil)
					writeVia(ctx, "src", op.AttrIAMPolicy, `{"during":1}`)
				})
				Expect(retryVia(ctx, "src")).To(Succeed())
				completed(ctx, id)
				Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"during":1}`)))
			})
			Describe("after an attempt copied the old instance again and then failed", func() {
				// failAfterCopyAgain runs a rename whose copy meets a policy
				// written through the old name, which it copies again, and
				// which then fails at the old instance's removal.
				failAfterCopyAgain := func(ctx context.Context) string {
					GinkgoHelper()
					rec := createBucket(ctx, "src", aliceOwner)
					id := rec.Info.Bucket.ID
					srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
					dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
					copies := 0
					c.AfterWrite(rookMetaPool, rookRoot, dstInst, func() {
						copies++
						switch copies {
						case 1:
							writeVia(ctx, "src", op.AttrIAMPolicy, `{"old":1}`)
						case 2:
							c.AfterWrite(rookMetaPool, rookRoot, dstInst, nil)
							c.FailNextWrite(rookMetaPool, rookRoot, srcInst, syscall.ETIMEDOUT)
						}
					})
					Expect(s.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrRequestTimedOut))
					Expect(copies).To(Equal(2), "the copy, then the copy again after the old name's write")
					holds(ctx, id)
					return id
				}
				It("completes a retry when neither name was written since", func(ctx SpecContext) {
					id := failAfterCopyAgain(ctx)
					Expect(retryVia(ctx, "src")).To(Succeed(), "the instances are as the copy again left them")
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"old":1}`)))
				})
				It("refuses a retry after a policy through the new name set back, though the names then hold the same configuration", func(ctx SpecContext) {
					id := failAfterCopyAgain(ctx)
					writeVia(ctx, "dst", op.AttrIAMPolicy, `{"x":1}`)
					writeVia(ctx, "dst", op.AttrIAMPolicy, `{"old":1}`)
					refusedRetry(ctx, id, "src")

					both(func(name string) { writeVia(ctx, name, op.AttrIAMPolicy, `{"old":1}`) })
					Expect(retryVia(ctx, "src")).To(Succeed())
					completed(ctx, id)
					Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"old":1}`)))
				})
			})
			It("fails a retry whose read of the other name's instance fails, writing nothing, and completes on the next", func(ctx SpecContext) {
				id := failAtLink(ctx)
				dstInst := meta.BucketID{Name: "dst", ID: id}.InstanceOID()
				c.FailNextRead(rookMetaPool, rookRoot, dstInst, syscall.ETIMEDOUT)
				c.ResetCounters()
				Expect(retryVia(ctx, "src")).To(MatchError(op.ErrRequestTimedOut))
				Expect(c.WriteOrder(rookMetaPool, rookRoot)).To(BeEmpty())
				Expect(c.WriteOrder(rookMetaPool, rookRoot+".rgw-go-bucket-claims")).To(BeEmpty())
				holds(ctx, id)

				Expect(retryVia(ctx, "src")).To(Succeed())
				completed(ctx, id)
			})
			Describe("when a write of the rename lands and its reply is lost", func() {
				// w is a gateway over lc, which loses the reply of the write
				// failAfter names once it has landed.
				var (
					lc *lostReplyCluster
					w  *driver.Store
				)
				BeforeEach(func(ctx SpecContext) {
					lc = &lostReplyCluster{Cluster: c}
					st, err := driver.Open(ctx, lc, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{})
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(st.Close)
					w = st
				})
				// lostReply runs an attempt of the rename on w, the first or,
				// with retry, one after a failure at the new owner's link,
				// whose n-th write of the instance under name lands and
				// answers ETIMEDOUT. With during, a policy is written through
				// the old name after the attempt's mark, so the attempt copies
				// the old instance again. It returns the bucket's id.
				lostReply := func(ctx context.Context, retry bool, name string, n int, during bool) string {
					GinkgoHelper()
					var id string
					if retry {
						id = failAtLink(ctx)
					} else {
						id = createBucket(ctx, "src", aliceOwner).Info.Bucket.ID
					}
					if during {
						srcInst := meta.BucketID{Name: "src", ID: id}.InstanceOID()
						c.AfterWrite(rookMetaPool, rookRoot, srcInst, func() {
							c.AfterWrite(rookMetaPool, rookRoot, srcInst, nil)
							writeVia(ctx, "src", op.AttrIAMPolicy, `{"during":1}`)
						})
					}
					lc.failAfter(meta.BucketID{Name: name, ID: id}.InstanceOID(), n)
					rec, err := w.GetBucket(ctx, "", "src")
					Expect(err).NotTo(HaveOccurred())
					Expect(w.ChangeBucketOwner(ctx, rec, bobOwner, "Bob", &meta.BucketID{Name: "dst"})).To(MatchError(op.ErrRequestTimedOut))
					Expect(lc.lost()).To(BeTrue(), "the write landed and its reply was lost")
					holds(ctx, id)
					return id
				}
				DescribeTable("completes the next retry when neither name was written since",
					func(ctx context.Context, retry bool, name string, n int, during bool) {
						id := lostReply(ctx, retry, name, n, during)
						Expect(retryVia(ctx, "src")).To(Succeed())
						completed(ctx, id)
						if during {
							Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"during":1}`)), "the write through the old name is kept")
						}
					},
					Entry("a first attempt's mark of the old instance", false, "src", 1, false),
					Entry("a retry's mark of the old instance", true, "src", 1, false),
					Entry("a first attempt's creation of the new instance", false, "dst", 1, false),
					Entry("a retry's copy onto the new instance", true, "dst", 1, false),
					Entry("a first attempt's copy again after a write through the old name", false, "dst", 2, true),
					Entry("a retry's copy again after a write through the old name", true, "dst", 2, true),
				)
				DescribeTable("refuses the next retry after a write through the new name, writing nothing and keeping the write, until it is written through both",
					func(ctx context.Context, retry bool, name string, n int, during bool) {
						id := lostReply(ctx, retry, name, n, during)
						writeVia(ctx, "dst", op.AttrIAMPolicy, `{"after":1}`)
						refusedRetry(ctx, id, "src")
						Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"after":1}`)))

						both(func(via string) { writeVia(ctx, via, op.AttrIAMPolicy, `{"after":1}`) })
						Expect(retryVia(ctx, "src")).To(Succeed())
						completed(ctx, id)
						Expect(loaded(ctx, "dst").Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(`{"after":1}`)))
					},
					Entry("a retry's mark of the old instance", true, "src", 1, false),
					Entry("a first attempt's creation of the new instance", false, "dst", 1, false),
					Entry("a retry's copy onto the new instance", true, "dst", 1, false),
					Entry("a first attempt's copy again after a write through the old name", false, "dst", 2, true),
					Entry("a retry's copy again after a write through the old name", true, "dst", 2, true),
				)
			})
			Describe("on a gateway whose metadata cache serves, after another gateway's write whose cache notify was lost", func() {
				const controlPool = "ceph-objectstore.rgw.control"
				// other is the gateway the write between the attempts goes
				// through; s, from here, is a gateway whose cache serves.
				var other *driver.Store
				BeforeEach(func(ctx SpecContext) {
					other = s
					st, err := driver.Open(ctx, c, conf(nil), driver.Options{})
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(st.Close)
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
					s = st
				})
				// failNotifies makes the next cache notify sent on each
				// control object fail, or, with n 0, none.
				failNotifies := func(n int) {
					for i := range 8 {
						c.FailNotify(controlPool, "", fmt.Sprintf("notify.%d", i), n, radosclient.ErrPermission)
					}
				}
				// staleWrite caches on s the bucket name loads, then writes a
				// policy through name on other, whose cache notify is lost,
				// and expects s to serve the old copy while RADOS holds the
				// write.
				staleWrite := func(ctx context.Context, name, policy string) {
					GinkgoHelper()
					before := loaded(ctx, name)
					failNotifies(1)
					rec, err := other.GetBucket(ctx, "", name)
					Expect(err).NotTo(HaveOccurred())
					Expect(other.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrIAMPolicy: []byte(policy)}, nil)).To(Succeed())
					failNotifies(0)
					Expect(loaded(ctx, name).Attrs).To(Equal(before.Attrs), "this gateway's cache still holds the old copy")
					inRADOS, err := other.GetBucket(ctx, "", name)
					Expect(err).NotTo(HaveOccurred())
					Expect(inRADOS.Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(policy)), "RADOS holds the write")
				}
				// reconciled writes policy through both names on other, whose
				// cache notifies reach s, then retries through retry and
				// expects the renamed bucket, read from RADOS, to carry it.
				reconciled := func(ctx context.Context, id, retry, policy string) {
					GinkgoHelper()
					for _, name := range []string{"src", "dst"} {
						rec, err := other.GetBucket(ctx, "", name)
						Expect(err).NotTo(HaveOccurred())
						Expect(other.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrIAMPolicy: []byte(policy)}, nil)).To(Succeed())
					}
					Eventually(func(g Gomega) {
						got, err := s.GetBucket(ctx, "", retry)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(got.Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(policy)))
					}).WithTimeout(5*time.Second).WithPolling(5*time.Millisecond).Should(Succeed(), "the notify reached this gateway")
					Expect(retryVia(ctx, retry)).To(Succeed())
					completed(ctx, id)
					got, err := other.GetBucket(ctx, "", "dst")
					Expect(err).NotTo(HaveOccurred())
					Expect(got.Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(policy)))
				}
				DescribeTable("refuses a retry after a write the cache does not show, through the name the retry does not come through, writing nothing",
					func(ctx context.Context, via, retry string) {
						id := failAtLink(ctx)
						loaded(ctx, retry)
						staleWrite(ctx, via, `{"elsewhere":1}`)
						refusedRetry(ctx, id, retry)
						reconciled(ctx, id, retry, `{"elsewhere":1}`)
					},
					Entry("through the new name, retried by the old", "dst", "src"),
					Entry("through the old name, retried by the new", "src", "dst"),
				)
				DescribeTable("refuses a retry through a name whose instance the cache shows stale, writing nothing, and the next while the write stands alone",
					func(ctx context.Context, name string) {
						id := failAtLink(ctx)
						staleWrite(ctx, name, `{"elsewhere":1}`)
						refusedRetry(ctx, id, name)
						refusedRetry(ctx, id, name)
						reconciled(ctx, id, name, `{"elsewhere":1}`)
					},
					Entry("the new name", "dst"),
					Entry("the old name", "src"),
				)
			})
		})
		It("refuses a link by bucket id onto a name another bucket now holds, writing nothing", func(ctx SpecContext) {
			stale := createBucket(ctx, "a", aliceOwner)
			Expect(driver.RemoveEntryPoint(s, ctx, "", "a", meta.ObjVersion{})).To(Succeed())
			live := createBucket(ctx, "a", carolOwner)
			byID, err := s.GetBucketInstance(ctx, stale.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			c.ResetCounters()
			Expect(s.ChangeBucketOwner(ctx, byID, bobOwner, "Bob", nil)).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(c.WriteOrder(rookMetaPool, rookRoot)).To(BeEmpty())
			got, err := s.GetBucket(ctx, "", "a")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(live.Info.Bucket.ID))
			Expect(got.Info.Owner).To(Equal(carolOwner))
		})
		It("links by bucket id under the entry point's version, and creates a missing one exclusively", func(ctx SpecContext) {
			rec := createBucket(ctx, "a", aliceOwner)
			byID, err := s.GetBucketInstance(ctx, rec.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(byID.EPVersion).To(Equal(meta.ObjVersion{}), "a load by id reads no entry point")
			c.AfterWrite(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID(), func() {
				c.AfterWrite(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID(), nil)
				ep := readEP(ctx, "", "a")
				ep.Bucket = meta.BucketID{Name: "a", Marker: "y", ID: "y"}
				_, werr := driver.WriteEntryPoint(s, ctx, ep, false, meta.ObjVersion{}, meta.ObjVersion{})
				Expect(werr).NotTo(HaveOccurred())
			})
			Expect(s.ChangeBucketOwner(ctx, byID, bobOwner, "Bob", nil)).To(MatchError(op.ErrConcurrentModification))
			Expect(readEP(ctx, "", "a").Bucket.ID).To(Equal("y"), "the entry point another writer moved stays")
			Expect(listOwnerBuckets(ctx, bobOwner)).To(BeEmpty(), "the link goes again with the entry point write, as done_err removes it")

			Expect(driver.RemoveEntryPoint(s, ctx, "", "a", meta.ObjVersion{})).To(Succeed())
			byID, err = s.GetBucketInstance(ctx, rec.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChangeBucketOwner(ctx, byID, bobOwner, "Bob", nil)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "a")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(bobOwner))
		})
	})

	Describe("UnlinkBucketOwner", func() {
		It("unlinks: the cls_user entry goes and the entry point says linked=false", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			Expect(s.UnlinkBucketOwner(ctx, rec, aliceOwner)).To(Succeed())
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
			Expect(readEP(ctx, "", "plain").Linked).To(BeFalse())
			Expect(s.UnlinkBucketOwner(ctx, rec, aliceOwner)).To(Succeed(), "an unlinked entry point is done")
		})
		It("removes the named owner's entry, then refuses an entry point another owner holds", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			Expect(driver.LinkBucket(s, ctx, bobOwner, rec.Info.Bucket, rec.Info.CreationTime.Time)).To(Succeed())
			Expect(s.UnlinkBucketOwner(ctx, rec, bobOwner)).To(MatchError(op.ErrInvalidArgument), "do_unlink_bucket's owner mismatch")
			Expect(listOwnerBuckets(ctx, bobOwner)).To(BeEmpty(), "the stale entry is gone, as radosgw removes it first")
			Expect(readEP(ctx, "", "plain").Linked).To(BeTrue())
		})
		It("takes an entry point already gone as done", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			Expect(driver.RemoveEntryPoint(s, ctx, "", "plain", meta.ObjVersion{})).To(Succeed())
			Expect(s.UnlinkBucketOwner(ctx, rec, aliceOwner)).To(Succeed())
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
		})
	})

	Describe("ChownBucket", func() {
		It("chowns keeping other grants and swapping the owner grant", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			pol := decodeACL(rec.Attrs[meta.AttrACL])
			pol.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "carol", Name: "Carol", Permission: acl.PermRead})
			e := denc.NewEncoder()
			pol.Encode(e, denc.Squid)
			Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{meta.AttrACL: e.Bytes()}, nil)).To(Succeed())
			Expect(s.ChownBucket(ctx, rec, bobOwner, "Bob")).To(Succeed())
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			p := decodeACL(got.Attrs[meta.AttrACL])
			Expect(p.Owner).To(Equal(acl.Owner{ID: "bob", DisplayName: "Bob"}))
			var ids []string
			for _, g := range p.ACL.Grants {
				ids = append(ids, g.Key)
			}
			Expect(ids).To(ConsistOf("bob", "carol"))
			Expect(got.Info.Owner).To(Equal(bobOwner))
			Expect(readEP(ctx, "", "plain").Owner).To(Equal(bobOwner))
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("plain"))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
		})
		It("retries past a version another writer moved, as adopt_user_bucket does", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			stale := *rec
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
			Expect(s.ChownBucket(ctx, &stale, bobOwner, "Bob")).To(Succeed())
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(bobOwner))
			Expect(stale.Version).To(Equal(got.Version))
		})
		It("starts a round over when another writer changes the instance mid-round", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			pool, bobOID := driver.OwnerBucketsObj(s, bobOwner)
			raced := 0
			c.AfterWrite(pool.Name, pool.NS, bobOID, func() {
				if raced > 0 {
					return
				}
				raced++
				cur, err := s.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred())
				Expect(s.PutBucketInfo(ctx, cur)).To(Succeed())
			})
			Expect(s.ChownBucket(ctx, rec, bobOwner, "Bob")).To(Succeed())
			Expect(raced).To(Equal(1))
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(bobOwner))
			Expect(decodeACL(got.Attrs[meta.AttrACL]).Owner.ID).To(Equal("bob"))
		})
		It("writes the entry point under the version the round read", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			pool, bobOID := driver.OwnerBucketsObj(s, bobOwner)
			c.AfterWrite(pool.Name, pool.NS, bobOID, func() {
				c.AfterWrite(pool.Name, pool.NS, bobOID, nil)
				ep := readEP(ctx, "", "plain")
				ep.Bucket = meta.BucketID{Name: "plain", Marker: "other", ID: "other"}
				_, err := driver.WriteEntryPoint(s, ctx, ep, false, meta.ObjVersion{}, meta.ObjVersion{})
				Expect(err).NotTo(HaveOccurred())
			})
			Expect(s.ChownBucket(ctx, rec, bobOwner, "Bob")).To(MatchError(op.ErrNoSuchBucket), "the next round finds the name on another instance")
			Expect(readEP(ctx, "", "plain").Bucket.ID).To(Equal("other"), "the entry point another writer moved stays")
		})
		It("refuses a name that now loads another instance", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			gone := *rec
			gone.Info.Bucket.ID = "other"
			Expect(s.ChownBucket(ctx, &gone, bobOwner, "Bob")).To(MatchError(op.ErrNoSuchBucket))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(ConsistOf("plain"))
		})
		It("refuses an instance another owner now holds", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			stale := *rec
			Expect(s.ChownBucket(ctx, rec, bobOwner, "Bob")).To(Succeed())
			Expect(s.ChownBucket(ctx, &stale, carolOwner, "Carol")).To(MatchError(op.ErrNoSuchBucket))
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(bobOwner))
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("plain"))
			Expect(listOwnerBuckets(ctx, carolOwner)).To(BeEmpty())
		})
		It("refuses a round that finds the instance given to another owner since the round before", func(ctx SpecContext) {
			rec := createBucket(ctx, "plain", aliceOwner)
			pool, bobOID := driver.OwnerBucketsObj(s, bobOwner)
			c.AfterWrite(pool.Name, pool.NS, bobOID, func() {
				c.AfterWrite(pool.Name, pool.NS, bobOID, nil)
				cur, err := s.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred())
				cur.Info.Owner = carolOwner
				Expect(s.PutBucketInfo(ctx, cur)).To(Succeed())
			})
			Expect(s.ChownBucket(ctx, rec, bobOwner, "Bob")).To(MatchError(op.ErrNoSuchBucket))
			got, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(carolOwner), "the instance the other writer gave carol stays hers")
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty(), "the lost round's unlink stays")
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("plain"), "and its link")
			Expect(readEP(ctx, "", "plain").Owner).To(Equal(bobOwner), "and its entry point")
		})
	})

	Describe("a user's move into an account", func() {
		const acctID = "RGW00000000000000001"
		var acctOwner meta.Owner
		BeforeEach(func(ctx SpecContext) {
			acctOwner = meta.AccountOwner(acctID)
			u := meta.NewUserInfo()
			u.UserID, u.DisplayName = meta.UserID{ID: "alice"}, "alice"
			seedUser(c, u, nil, meta.ObjVersion{Ver: 1, Tag: "_alice"})
			a := meta.NewAccountInfo()
			a.ID, a.Name = acctID, "acme"
			Expect(s.PutAccount(ctx, &op.AccountRecord{Info: a}, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		})
		// moveAlice is POST /admin/user?uid=alice&account-id=<acctID> over the
		// driver's env.
		moveAlice := func(ctx context.Context) error {
			m := op.NewModifyUser()
			m.UID, m.AccountID = meta.UserID{ID: "alice"}, acctID
			admin := meta.NewUserInfo()
			admin.UserID = meta.UserID{ID: "admin"}
			caps := meta.Caps{"users": meta.CapAll}
			return op.Run(ctx, m, &op.Request{Env: s.Env(), Identity: op.Identity{User: &admin, Owner: meta.UserOwner(admin.UserID), OpMask: op.OpTypeAll, Caps: caps}})
		}
		// strand deletes rec as a delete that fails at its last step, the
		// unlink from alice's list, leaves it: the entry point, the instance
		// and the index gone, and alice's list entry kept.
		strand := func(ctx context.Context, rec *op.BucketRecord) {
			GinkgoHelper()
			pool, oid := driver.OwnerBucketsObj(s, aliceOwner)
			name := rec.Info.Bucket.Name
			c.AfterWrite(rookMetaPool, rookRoot, name, func() {
				c.AfterWrite(rookMetaPool, rookRoot, name, nil)
				c.FailNextWrite(pool.Name, pool.NS, oid, syscall.EIO)
			})
			Expect(s.DeleteBucket(ctx, rec)).NotTo(Succeed())
			Expect(objectExists(name)).To(BeFalse(), "the entry point")
			Expect(instancesOf(rec.Info.Bucket.ID)).To(BeEmpty(), "the instance")
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(ContainElement(name), "alice's list entry")
		}
		It("adopts a live bucket: its owner, entry point, ACL and list entry move to the account", func(ctx SpecContext) {
			createBucket(ctx, "live", aliceOwner)
			Expect(moveAlice(ctx)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "live")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(acctOwner))
			Expect(readEP(ctx, "", "live").Owner).To(Equal(acctOwner))
			p := decodeACL(got.Attrs[meta.AttrACL])
			Expect(p.Owner).To(Equal(acl.Owner{ID: acctID, DisplayName: "acme"}))
			Expect(p.ACL.Grants).To(ConsistOf(HaveField("Key", acctID)), "alice's grant became the account's")
			Expect(listOwnerBuckets(ctx, acctOwner)).To(ConsistOf("live"))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty())
		})
		It("leaves the bucket another owner re-created under a stale entry's name, and adopts the live ones", func(ctx SpecContext) {
			createBucket(ctx, "live", aliceOwner)
			strand(ctx, createBucket(ctx, "data", aliceOwner))
			createBucket(ctx, "data", bobOwner)
			theirs, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(moveAlice(ctx)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(theirs), "bob's instance, ACL and entry point unchanged")
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("data"))
			Expect(listOwnerBuckets(ctx, acctOwner)).To(ConsistOf("live"))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(ConsistOf("data"), "the stale entry stays, as radosgw skips it")
		})
		It("leaves the bucket another owner re-created under a stale entry's name whose instance is left", func(ctx SpecContext) {
			// What radosgw's delete leaves when its instance removal loses a
			// race: the entry point gone, the instance and the list entry kept.
			old := createBucket(ctx, "data", aliceOwner)
			Expect(driver.RemoveEntryPoint(s, ctx, "", "data", meta.ObjVersion{})).To(Succeed())
			createBucket(ctx, "data", bobOwner)
			theirs, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(moveAlice(ctx)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(theirs), "bob's instance, ACL and entry point unchanged")
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("data"))
			Expect(listOwnerBuckets(ctx, acctOwner)).To(BeEmpty())
			left, err := s.GetBucketInstance(ctx, old.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(left.Info.Owner).To(Equal(aliceOwner), "the old instance is not adopted either")
		})
		It("leaves bob's bucket whose instance a stale entry of alice's still names", func(ctx SpecContext) {
			// What a link to bob leaves when its removal of alice's entry
			// fails, a failure radosgw's link logs and goes past.
			createBucket(ctx, "live", aliceOwner)
			theirs := createBucket(ctx, "data", bobOwner)
			Expect(driver.LinkBucket(s, ctx, aliceOwner, theirs.Info.Bucket, time.Time{})).To(Succeed())
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(ContainElement("data"))
			before, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(moveAlice(ctx)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(before), "bob's instance, ACL and entry point unchanged")
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("data"))
			Expect(listOwnerBuckets(ctx, acctOwner)).To(ConsistOf("live"))
		})
		It("follows the entry's instance, not the bucket of alice's the name now loads", func(ctx SpecContext) {
			old := createBucket(ctx, "data", aliceOwner)
			Expect(driver.RemoveEntryPoint(s, ctx, "", "data", meta.ObjVersion{})).To(Succeed())
			createBucket(ctx, "data", aliceOwner)
			Expect(driver.LinkBucket(s, ctx, aliceOwner, old.Info.Bucket, time.Time{})).To(Succeed())
			now, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(moveAlice(ctx)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(now), "the name loads another instance than the entry names, so nothing is chowned")
			left, err := s.GetBucketInstance(ctx, old.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(left.Info.Owner).To(Equal(aliceOwner))
		})
		It("skips a stale entry whose bucket is gone", func(ctx SpecContext) {
			createBucket(ctx, "live", aliceOwner)
			strand(ctx, createBucket(ctx, "gone", aliceOwner))
			Expect(moveAlice(ctx)).To(Succeed())
			Expect(objectExists("gone")).To(BeFalse(), "no entry point is written for the name")
			Expect(listOwnerBuckets(ctx, acctOwner)).To(ConsistOf("live"))
		})
		// withoutID rewrites alice's list entry for name as one carrying no
		// bucket id.
		withoutID := func(ctx context.Context, name string) {
			GinkgoHelper()
			Expect(driver.LinkBucket(s, ctx, aliceOwner, meta.BucketID{Name: name}, time.Time{})).To(Succeed())
			ents, _, _, err := s.ListUserBuckets(ctx, aliceOwner, "", 100)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(ContainElement(And(HaveField("Bucket.Name", name), HaveField("Bucket.ID", ""))), "alice's entry carries no id")
		}
		It("adopts the bucket an entry without an id names, loading it by its name, and drops the entry", func(ctx SpecContext) {
			createBucket(ctx, "data", aliceOwner)
			withoutID(ctx, "data")
			Expect(moveAlice(ctx)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(acctOwner))
			Expect(readEP(ctx, "", "data").Owner).To(Equal(acctOwner))
			Expect(decodeACL(got.Attrs[meta.AttrACL]).Owner).To(Equal(acl.Owner{ID: acctID, DisplayName: "acme"}))
			Expect(listOwnerBuckets(ctx, acctOwner)).To(ConsistOf("data"))
			Expect(listOwnerBuckets(ctx, aliceOwner)).To(BeEmpty(), "the chown unlinks the entry by its name, as radosgw does")
		})
		It("leaves bob's bucket under the name an entry of alice's without an id names", func(ctx SpecContext) {
			createBucket(ctx, "live", aliceOwner)
			strand(ctx, createBucket(ctx, "data", aliceOwner))
			createBucket(ctx, "data", bobOwner)
			withoutID(ctx, "data")
			theirs, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(moveAlice(ctx)).To(Succeed())
			got, err := s.GetBucket(ctx, "", "data")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(theirs), "bob's instance, ACL and entry point unchanged")
			Expect(listOwnerBuckets(ctx, bobOwner)).To(ConsistOf("data"))
			Expect(listOwnerBuckets(ctx, acctOwner)).To(ConsistOf("live"))
		})
	})

	It("syncs an owner's stats", func(ctx SpecContext) {
		createBucket(ctx, "plain", aliceOwner)
		Expect(s.SyncOwnerStats(ctx, aliceOwner)).To(Succeed())
		_, lastSync, _, err := driver.ReadOwnerStats(s, ctx, aliceOwner)
		Expect(err).NotTo(HaveOccurred())
		Expect(lastSync.IsZero()).To(BeFalse())
	})
})
