package op_test

import (
	"context"
	"errors"
	"fmt"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// purgeIndex is a bucket's listing with versions on one index shard, as
// PurgeObjects meets it: entries in list order, a name's versions together,
// and a marker resolved as the class resolves it, after the entry it names,
// or past every version of its name when it names no instance or one that is
// gone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L440 at v20.2.4),
// but at the name's first version left for a marker without an instance
// sent with RelistMarker. A next marker names the page's last entry, "null"
// for an empty instance on a versioned bucket, as the RADOS store names it.
type purgeIndex struct {
	versioned bool
	entries   []meta.ObjKey
	deleted   []meta.ObjKey
	// stuck, when set, picks the entries whose delete answers NoSuchKey and
	// removes nothing, as an entry listed but not removable answers.
	stuck func(meta.ObjKey) bool
	// fail, when set, is every delete's answer.
	fail error
	// onList, when set, sees each page before the pass does.
	onList func(page []meta.ObjKey)
	// lists counts the listings, which end in an error past maxPurgeLists so
	// that a pass that never ends fails its spec.
	lists int
}

const maxPurgeLists = 50

var errListedForEver = errors.New("the purge listed the bucket again and again")

func (x *purgeIndex) start(p op.ListObjectsParams) int {
	if p.Marker == "" {
		return 0
	}
	at, past := len(x.entries), len(x.entries)
	for i, k := range x.entries {
		if k.Name >= p.Marker && at == len(x.entries) {
			at = i
		}
		if k.Name > p.Marker {
			past = i
			break
		}
	}
	if p.MarkerInstance == "" {
		if p.RelistMarker {
			return at
		}
		return past
	}
	instance := p.MarkerInstance
	if instance == meta.NullInstance {
		instance = ""
	}
	if i := slices.Index(x.entries, meta.ObjKey{Name: p.Marker, Instance: instance}); i >= 0 {
		return i + 1
	}
	return past
}

func (x *purgeIndex) list(_ context.Context, _ *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	if x.lists++; x.lists > maxPurgeLists {
		return op.ListObjectsResult{}, errListedForEver
	}
	from := x.start(p)
	to := min(from+p.MaxKeys, len(x.entries))
	page := slices.Clone(x.entries[from:to])
	res := op.ListObjectsResult{Truncated: to < len(x.entries)}
	for _, k := range page {
		res.Entries = append(res.Entries, op.ObjectEntry{Key: k})
	}
	if res.Truncated {
		last := page[len(page)-1]
		res.NextMarker, res.NextMarkerInstance = last.Name, last.Instance
		if x.versioned && last.Instance == "" {
			res.NextMarkerInstance = meta.NullInstance
		}
	}
	if x.onList != nil {
		x.onList(page)
	}
	return res, nil
}

func (x *purgeIndex) remove(key meta.ObjKey) bool {
	i := slices.Index(x.entries, key)
	if i < 0 || (x.stuck != nil && x.stuck(key)) {
		return false
	}
	x.entries = slices.Delete(x.entries, i, i+1)
	return true
}

func (x *purgeIndex) del(_ context.Context, _ *op.BucketRecord, key meta.ObjKey, _ op.DeleteParams) (*op.DeleteObjectResult, error) {
	x.deleted = append(x.deleted, key)
	if x.fail != nil {
		return nil, x.fail
	}
	if x.versioned && key.Instance == meta.NullInstance {
		key.Instance = ""
	}
	if !x.remove(key) {
		return nil, fmt.Errorf("%s: %w", key.Name, op.ErrNoSuchKey)
	}
	return &op.DeleteObjectResult{}, nil
}

// versionsOf is n versions of name, newest first, as a name's versions
// list.
func versionsOf(name string, n int) []meta.ObjKey {
	out := make([]meta.ObjKey, 0, n)
	for i := range n {
		out = append(out, meta.ObjKey{Name: name, Instance: fmt.Sprintf("v%04d", n-i)})
	}
	return out
}

// namesOf is one version each of n names, prefix and a number.
func namesOf(prefix string, n int) []meta.ObjKey {
	out := make([]meta.ObjKey, 0, n)
	for i := range n {
		out = append(out, meta.ObjKey{Name: fmt.Sprintf("%s%04d", prefix, i), Instance: "v1"})
	}
	return out
}

var _ = Describe("PurgeObjects", func() {
	var (
		buckets *opfakes.FakeBucketStore
		objects *opfakes.FakeObjectStore
		env     *op.Env
		x       *purgeIndex
	)
	bucket := func(flags uint32) *op.BucketRecord {
		rec := &op.BucketRecord{}
		rec.Info.Flags = flags
		return rec
	}
	BeforeEach(func() {
		x = &purgeIndex{}
		buckets = &opfakes.FakeBucketStore{}
		buckets.ListObjectsCalls(func(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
			return x.list(ctx, rec, p)
		})
		objects = &opfakes.FakeObjectStore{}
		objects.DeleteObjectCalls(func(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
			return x.del(ctx, rec, key, p)
		})
		env = &op.Env{Buckets: buckets, Objects: objects}
	})
	markers := func() []string {
		out := make([]string, 0, buckets.ListObjectsCallCount())
		for i := range buckets.ListObjectsCallCount() {
			_, _, p := buckets.ListObjectsArgsForCall(i)
			out = append(out, p.Marker+"@"+p.MarkerInstance)
		}
		return out
	}
	// relisted checks that every page after the first resumed at the name
	// the page before it ended on, as RelistMarker resumes, and that each
	// is RadosBucket::remove's listing.
	relisted := func() {
		GinkgoHelper()
		for i := range buckets.ListObjectsCallCount() {
			_, _, p := buckets.ListObjectsArgsForCall(i)
			want := op.ListObjectsParams{ListVersions: true, AllowUnordered: true, MaxKeys: 1000}
			if i > 0 {
				Expect(p.Marker).NotTo(BeEmpty(), "page %d's marker", i)
				want.Marker, want.RelistMarker = p.Marker, true
			}
			Expect(p).To(Equal(want), "page %d", i)
		}
	}

	Describe("on a versioned bucket", func() {
		BeforeEach(func() { x.versioned = true })

		It("removes every version of a key whose versions span pages, resuming at the key's versions left", func(ctx SpecContext) {
			x.entries = append(versionsOf("a", 3), versionsOf("k", 1500)...)
			x.entries = append(x.entries, meta.ObjKey{Name: "k"}, meta.ObjKey{Name: "z"})
			Expect(op.PurgeObjects(ctx, env, bucket(meta.BucketVersioned))).To(Succeed())
			Expect(x.entries).To(BeEmpty(), "a page ending on a version of k it deleted leaves no version of k behind")
			Expect(markers()).To(Equal([]string{"@", "k@"}), "the second page at k's first version left")
			relisted()
		})

		It("removes every version of many keys whose versions pages end among", func(ctx SpecContext) {
			for i := range 30 {
				x.entries = append(x.entries, versionsOf(fmt.Sprintf("k%02d", i), 70)...)
			}
			Expect(op.PurgeObjects(ctx, env, bucket(meta.BucketVersioned))).To(Succeed())
			Expect(x.entries).To(BeEmpty())
			Expect(buckets.ListObjectsCallCount()).To(Equal(3), "2100 versions, 1000 a page")
			relisted()
		})

		DescribeTable("names an entry listed without an instance null, so no delete writes a delete marker",
			func(ctx SpecContext, flags uint32) {
				x.entries = []meta.ObjKey{{Name: "k", Instance: "v2"}, {Name: "k", Instance: "dm"}, {Name: "k"}, {Name: "pre"}}
				Expect(op.PurgeObjects(ctx, env, bucket(flags))).To(Succeed())
				Expect(x.deleted).To(Equal([]meta.ObjKey{
					{Name: "k", Instance: "v2"},
					{Name: "k", Instance: "dm"},
					{Name: "k", Instance: meta.NullInstance},
					{Name: "pre", Instance: meta.NullInstance},
				}), "a version and a marker by their instances, the null version and a pre-versioning object as null")
				for i := range objects.DeleteObjectCallCount() {
					_, _, _, p := objects.DeleteObjectArgsForCall(i)
					Expect(p).To(Equal(op.DeleteParams{}), "delete %d checks nothing, as rgw_remove_object passes no condition", i)
				}
				Expect(x.entries).To(BeEmpty())
			},
			Entry("with versioning enabled", uint32(meta.BucketVersioned)),
			Entry("with versioning suspended, where a delete of the plain key would replace the null version with a marker",
				uint32(meta.BucketVersioned|meta.BucketVersionsSuspended)),
		)

		It("goes on over a page that mixes entries it cannot remove with ones it removes", func(ctx SpecContext) {
			x.entries = namesOf("m", 1500)
			x.stuck = func(k meta.ObjKey) bool { return k.Name[len(k.Name)-1]%3 == 0 }
			Expect(op.PurgeObjects(ctx, env, bucket(meta.BucketVersioned))).To(Succeed())
			Expect(x.entries).To(HaveEach(Satisfy(x.stuck)), "every entry it could remove is gone")
			Expect(x.entries).To(HaveLen(600))
		})

		It("goes on past a page whose entries a second purge removed first", func(ctx SpecContext) {
			x.entries = namesOf("o", 1500)
			first := true
			x.onList = func(page []meta.ObjKey) {
				if first {
					first = false
					for _, k := range page {
						x.remove(k)
					}
				}
			}
			Expect(op.PurgeObjects(ctx, env, bucket(meta.BucketVersioned))).To(Succeed())
			Expect(x.entries).To(BeEmpty(), "the 500 the other purge had not reached")
		})

		It("goes on past pages of entries it cannot remove, spread over many names, at the head of the listing", func(ctx SpecContext) {
			x.entries = append(namesOf("a", 2000), versionsOf("b", 500)...)
			x.stuck = func(k meta.ObjKey) bool { return k.Name != "b" }
			Expect(op.PurgeObjects(ctx, env, bucket(meta.BucketVersioned))).To(Succeed())
			Expect(x.entries).To(Equal(namesOf("a", 2000)), "every version of b gone, the entries it cannot remove left")
			Expect(markers()).To(Equal([]string{"@", "a0999@", "a1998@"}), "two pages that delete nothing, each starting further on")
		})

		DescribeTable("steps past a key holding more than a page of versions it cannot remove, and purges the keys after it",
			func(ctx SpecContext, before int, want []string) {
				x.entries = append(append(namesOf("a", before), versionsOf("k", 1500)...), namesOf("m", 500)...)
				x.stuck = func(k meta.ObjKey) bool { return k.Name == "k" }
				Expect(op.PurgeObjects(ctx, env, bucket(meta.BucketVersioned))).To(Succeed())
				Expect(x.entries).To(Equal(versionsOf("k", 1500)), "only k's versions left, for the bucket delete to refuse")
				Expect(markers()).To(Equal(want))
				_, _, last := buckets.ListObjectsArgsForCall(buckets.ListObjectsCallCount() - 1)
				Expect(last).To(Equal(op.ListObjectsParams{Marker: "k", ListVersions: true, AllowUnordered: true, MaxKeys: 1000}),
					"the page past every version of k")
			},
			Entry("at the head of the listing: two pages start at k's first version", 0, []string{"@", "k@", "k@"}),
			Entry("after keys it removes: a page of k's alone, then the same page again", 300, []string{"@", "k@", "k@", "k@"}),
		)

		It("returns a delete's failure other than NoSuchKey, deleting nothing after it", func(ctx SpecContext) {
			x.fail = errors.New("delete failed")
			x.entries = versionsOf("k", 3)
			Expect(op.PurgeObjects(ctx, env, bucket(meta.BucketVersioned))).To(MatchError(x.fail))
			Expect(x.deleted).To(HaveLen(1))
		})
	})

	It("pages a bucket that was never versioned by the next marker, deleting each entry as listed", func(ctx SpecContext) {
		for i := range 1500 {
			x.entries = append(x.entries, meta.ObjKey{Name: fmt.Sprintf("o%04d", i)})
		}
		Expect(op.PurgeObjects(ctx, env, bucket(0))).To(Succeed())
		Expect(x.entries).To(BeEmpty())
		Expect(markers()).To(Equal([]string{"@", "o0999@"}), "radosgw's paging, which skips nothing for a key without versions")
		Expect(x.deleted).To(HaveEach(HaveField("Instance", "")), "each key as listed")
	})
})
