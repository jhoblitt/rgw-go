package driver_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
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
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
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
		// failRename runs the rename of src to dst for bob with its write at
		// step failing, and expects it to fail.
		failRename := func(ctx context.Context, step int) string {
			GinkgoHelper()
			rec := createBucket(ctx, "src", aliceOwner)
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
		// (rgw_bucket.cc:1136-1183 at v19.2.6).
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
	})

	It("syncs an owner's stats", func(ctx SpecContext) {
		createBucket(ctx, "plain", aliceOwner)
		Expect(s.SyncOwnerStats(ctx, aliceOwner)).To(Succeed())
		_, lastSync, _, err := driver.ReadOwnerStats(s, ctx, aliceOwner)
		Expect(err).NotTo(HaveOccurred())
		Expect(lastSync.IsZero()).To(BeFalse())
	})
})
