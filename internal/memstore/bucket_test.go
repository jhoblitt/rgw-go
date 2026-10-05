package memstore_test

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// listNames lists rec and returns the entries' names with the result.
func listNames(ctx context.Context, store *memstore.Store, rec *op.BucketRecord, p op.ListObjectsParams) (names []string, res op.ListObjectsResult) {
	GinkgoHelper()
	res, err := store.ListObjects(ctx, rec, p)
	Expect(err).NotTo(HaveOccurred())
	for _, e := range res.Entries {
		names = append(names, e.Key.Name)
	}
	return names, res
}

var _ = Describe("buckets", func() {
	var (
		store *memstore.Store
		clk   *clock
	)
	BeforeEach(func() { store, clk = newStore() })

	It("creates the entry point and instance in radosgw's shape", func(ctx SpecContext) {
		quota := meta.Quota{MaxSize: -1, MaxObjects: 10, Enabled: true}
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Name:      "plain",
			Owner:     owner("alice"),
			Zonegroup: "zg-id",
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{meta.AttrACL: []byte("acl")},
			Quota:     quota,
		})
		Expect(err).NotTo(HaveOccurred())
		id := meta.BucketID{Name: "plain", Marker: "default.1.1", ID: "default.1.1"}
		Expect(rec.Info.Bucket).To(Equal(id), "<zone id>.<counter>.1, the marker equal to it")
		Expect(rec.Info.Owner).To(Equal(owner("alice")), "owner")
		Expect(rec.Info.Zonegroup).To(Equal("zg-id"), "zonegroup")
		Expect(rec.Info.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement"}), "placement")
		Expect(rec.Info.Quota).To(Equal(quota), "quota")
		Expect(rec.Info.CreationTime).To(Equal(meta.Time{Time: start}), "creation time")
		Expect(rec.Attrs).To(Equal(map[string][]byte{meta.AttrACL: []byte("acl")}), "attrs")
		Expect(rec.EntryPoint).To(Equal(meta.BucketEntryPoint{Bucket: id, Owner: owner("alice"), CreationTime: meta.Time{Time: start}, Linked: true}), "entry point")
		for name, v := range map[string]meta.ObjVersion{"instance": rec.Version, "entry point": rec.EPVersion} {
			Expect(v.Ver).To(BeEquivalentTo(1), "%s version", name)
			Expect(v.Tag).To(MatchRegexp(tagPattern), "%s tag", name)
		}
		byName, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(byName).To(Equal(rec), "by name")
		byID, err := store.GetBucketInstance(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(byID).To(Equal(rec), "by instance")
	})
	It("numbers each new bucket and keeps tenants apart", func(ctx SpecContext) {
		a := mustCreate(ctx, store, "", "b", owner("alice"))
		t := mustCreate(ctx, store, "t1", "b", owner("alice"))
		Expect([]string{a.Info.Bucket.ID, t.Info.Bucket.ID}).To(Equal([]string{"default.1.1", "default.2.1"}))
		got, err := store.GetBucket(ctx, "t1", "b")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Info.Bucket).To(Equal(meta.BucketID{Tenant: "t1", Name: "b", Marker: "default.2.1", ID: "default.2.1"}))
	})
	DescribeTable("answers a create of an existing name with the existing record and BucketAlreadyExists, as create_bucket does",
		func(ctx SpecContext, exclusive bool) {
			existing := mustCreate(ctx, store, "", "b", owner("alice"))
			mustPut(ctx, store, existing, "k", "v")
			got, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: owner("bob"), Exclusive: exclusive})
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(got).To(Equal(existing), "the existing record, for the op to compare owners")
			byName, err := store.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			Expect(byName).To(Equal(existing), "the name still links the first instance")
		},
		Entry("exclusive", true),
		Entry("not exclusive", false),
	)
	It("reports a missing bucket or instance as NoSuchBucket", func(ctx SpecContext) {
		_, err := store.GetBucket(ctx, "", "nope")
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "by name")
		_, err = store.GetBucketInstance(ctx, meta.BucketID{Name: "nope", ID: "default.9.1"})
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "by instance")
		_, err = store.ListObjects(ctx, &op.BucketRecord{}, op.ListObjectsParams{MaxKeys: 1})
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "listing")
	})
	It("refuses to delete a bucket with objects, and deletes it empty", func(ctx SpecContext) {
		rec := mustCreate(ctx, store, "", "b", owner("alice"))
		mustPut(ctx, store, rec, "k", "v")
		Expect(store.DeleteBucket(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty), "with an object")
		Expect(store.DeleteObject(ctx, rec, meta.ObjKey{Name: "k"}, op.DeleteParams{})).To(Succeed())
		Expect(store.DeleteBucket(ctx, rec)).To(Succeed(), "empty")
		_, err := store.GetBucket(ctx, "", "b")
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "by name, deleted")
		_, err = store.GetBucketInstance(ctx, rec.Info.Bucket)
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "by instance, deleted")
		ents, _, _, err := store.ListUserBuckets(ctx, owner("alice"), "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(BeEmpty(), "the owner's list")
		Expect(store.DeleteBucket(ctx, rec)).To(MatchError(op.ErrNoSuchBucket), "again")
	})
	It("guards PutBucketInfo by the instance version and bumps it", func(ctx SpecContext) {
		rec := mustCreate(ctx, store, "", "b", owner("alice"))
		stale := *rec
		first := rec.Version
		clk.t = start.Add(time.Minute)
		rec.Info.Flags = meta.BucketVersioned
		Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
		Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: first.Ver + 1, Tag: first.Tag}), "version")
		Expect(rec.Mtime).To(Equal(clk.t), "mtime")
		got, err := store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(rec), "stored")
		Expect(store.PutBucketInfo(ctx, &stale)).To(MatchError(op.ErrConcurrentModification), "stale version")
	})
	It("guards PutBucketAttrs by the instance version and applies set, then rm", func(ctx SpecContext) {
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: owner("alice"), Attrs: map[string][]byte{"a": []byte("1"), "b": []byte("2")}})
		Expect(err).NotTo(HaveOccurred())
		stale := *rec
		Expect(store.PutBucketAttrs(ctx, rec, map[string][]byte{"c": []byte("3"), "b": []byte("x")}, []string{"a", "b"})).To(Succeed())
		Expect(rec.Attrs).To(Equal(map[string][]byte{"c": []byte("3")}), "the caller's record")
		Expect(rec.Version.Ver).To(BeEquivalentTo(2), "version")
		got, err := store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Attrs).To(Equal(map[string][]byte{"c": []byte("3")}), "stored")
		Expect(store.PutBucketAttrs(ctx, &stale, nil, []string{"c"})).To(MatchError(op.ErrConcurrentModification), "stale version")
	})
	It("hands out copies the caller may change freely", func(ctx SpecContext) {
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: owner("alice"), Attrs: map[string][]byte{"a": []byte("1")}})
		Expect(err).NotTo(HaveOccurred())
		rec.Attrs["a"][0] = 'x'
		rec.Info.Owner.User.ID = "mallory"
		got, err := store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Attrs).To(Equal(map[string][]byte{"a": []byte("1")}), "attrs")
		Expect(got.Info.Owner).To(Equal(owner("alice")), "owner")
	})

	Context("ListObjects", func() {
		var rec *op.BucketRecord
		BeforeEach(func(ctx SpecContext) {
			rec = mustCreate(ctx, store, "", "b", owner("alice"))
			for _, k := range []string{"c", "b/2", "a", "d/x/1", "_u", "b/1"} {
				mustPut(ctx, store, rec, k, k)
			}
		})
		It("lists keys in index order", func(ctx SpecContext) {
			names, res := listNames(ctx, store, rec, op.ListObjectsParams{MaxKeys: 10})
			Expect(names).To(Equal([]string{"_u", "a", "b/1", "b/2", "c", "d/x/1"}))
			Expect(res.Truncated).To(BeFalse(), "truncated")
			Expect(res.NextMarker).To(BeEmpty(), "next marker")
		})
		It("starts after the marker and keeps to the prefix", func(ctx SpecContext) {
			names, _ := listNames(ctx, store, rec, op.ListObjectsParams{Marker: "a", MaxKeys: 10})
			Expect(names).To(Equal([]string{"b/1", "b/2", "c", "d/x/1"}), "after a")
			names, _ = listNames(ctx, store, rec, op.ListObjectsParams{Prefix: "b/", MaxKeys: 10})
			Expect(names).To(Equal([]string{"b/1", "b/2"}), "under b/")
		})
		It("rolls keys holding the delimiter after the prefix into common prefixes", func(ctx SpecContext) {
			names, res := listNames(ctx, store, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 10})
			Expect(names).To(Equal([]string{"_u", "a", "c"}), "entries")
			Expect(res.CommonPrefixes).To(Equal([]string{"b/", "d/"}), "prefixes")
			_, res = listNames(ctx, store, rec, op.ListObjectsParams{Prefix: "d/", Delimiter: "/", MaxKeys: 10})
			Expect(res.CommonPrefixes).To(Equal([]string{"d/x/"}), "prefixes under d/")
		})
		It("pages entries and common prefixes together, each prefix counting one", func(ctx SpecContext) {
			names, res := listNames(ctx, store, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 3})
			Expect(names).To(Equal([]string{"_u", "a"}), "first page's entries")
			Expect(res.CommonPrefixes).To(Equal([]string{"b/"}), "first page's prefixes")
			Expect(res.Truncated).To(BeTrue(), "first page truncated")
			Expect(res.NextMarker).To(Equal("b/"), "first page's next marker, the last prefix")
			names, res = listNames(ctx, store, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 3, Marker: res.NextMarker})
			Expect(names).To(Equal([]string{"c"}), "second page's entries")
			Expect(res.CommonPrefixes).To(Equal([]string{"d/"}), "second page's prefixes")
			Expect(res.Truncated).To(BeFalse(), "second page is the last")
			Expect(res.NextMarker).To(BeEmpty(), "no next marker")
		})
		It("skips the rest of a common prefix a marker falls inside, as list_objects_ordered fast-forwards it", func(ctx SpecContext) {
			names, res := listNames(ctx, store, rec, op.ListObjectsParams{Delimiter: "/", Marker: "b/1", MaxKeys: 10})
			Expect(names).To(Equal([]string{"c"}), "entries")
			Expect(res.CommonPrefixes).To(Equal([]string{"d/"}), "prefixes")
		})
		It("marks the last key returned when a page fills", func(ctx SpecContext) {
			names, res := listNames(ctx, store, rec, op.ListObjectsParams{MaxKeys: 2})
			Expect(names).To(Equal([]string{"_u", "a"}))
			Expect(res.Truncated).To(BeTrue(), "truncated")
			Expect(res.NextMarker).To(Equal("a"), "next marker")
		})
		It("returns nothing but truncation for MaxKeys 0", func(ctx SpecContext) {
			names, res := listNames(ctx, store, rec, op.ListObjectsParams{MaxKeys: 0})
			Expect(names).To(BeEmpty(), "entries")
			Expect(res.Truncated).To(BeTrue(), "truncated")
		})
		It("fills each entry from its object", func(ctx SpecContext) {
			_, res := listNames(ctx, store, rec, op.ListObjectsParams{Prefix: "c", MaxKeys: 1})
			Expect(res.Entries).To(Equal([]op.ObjectEntry{{
				Key:          meta.ObjKey{Name: "c"},
				Size:         1,
				Mtime:        start,
				ETag:         md5Hex([]byte("c")),
				StorageClass: meta.StorageClassStandard,
				IsLatest:     true,
				Exists:       true,
			}}), "an unversioned entry is current, as is_current has it")
		})
		It("names the owner of each object's ACL, as the index entry's meta.owner does", func(ctx SpecContext) {
			e := denc.NewEncoder()
			acl.Policy{Owner: acl.Owner{ID: "alice", DisplayName: "Alice"}}.Encode(e, denc.Squid)
			_, err := store.PutObject(ctx, rec, meta.ObjKey{Name: "owned"}, strings.NewReader("v"),
				op.PutParams{Size: 1, Attrs: map[string][]byte{meta.AttrACL: e.Bytes()}})
			Expect(err).NotTo(HaveOccurred())
			_, res := listNames(ctx, store, rec, op.ListObjectsParams{Prefix: "owned", MaxKeys: 1})
			Expect(res.Entries).To(HaveLen(1))
			Expect(res.Entries[0].Owner).To(Equal(owner("alice")))
			Expect(res.Entries[0].OwnerDisplayName).To(Equal("Alice"))
		})
		It("lists unordered without common prefixes and refuses a delimiter", func(ctx SpecContext) {
			names, res := listNames(ctx, store, rec, op.ListObjectsParams{MaxKeys: 10, AllowUnordered: true})
			Expect(names).To(ConsistOf("_u", "a", "b/1", "b/2", "c", "d/x/1"))
			Expect(res.CommonPrefixes).To(BeEmpty())
			_, err := store.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10, AllowUnordered: true, Delimiter: "/"})
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		})
		It("lists the upload namespace for NS multipart", func(ctx SpecContext) {
			up, err := store.CreateUpload(ctx, rec, meta.ObjKey{Name: "big"}, op.UploadParams{Owner: owner("alice"), OwnerName: "Alice"})
			Expect(err).NotTo(HaveOccurred())
			_, res := listNames(ctx, store, rec, op.ListObjectsParams{NS: "multipart", MaxKeys: 10})
			Expect(res.Entries).To(Equal([]op.ObjectEntry{{
				Key:              meta.ObjKey{Name: "big." + up.ID + ".meta", NS: "multipart"},
				Mtime:            start,
				Owner:            owner("alice"),
				OwnerDisplayName: "Alice",
				StorageClass:     meta.StorageClassStandard,
				Exists:           true,
			}}))
		})
	})
})
