package op_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// indexCheckTimeout bounds every index check spec: the check pages through
// the bucket listing until the store says it is done.
const indexCheckTimeout = 30 * time.Second

// listingRecorder is a bucket store that records each listing's
// parameters and can run a hook after a listing returns.
type listingRecorder struct {
	op.BucketStore
	mu     sync.Mutex
	calls  []op.ListObjectsParams
	after  func(p op.ListObjectsParams)
	stuckN int // the multipart listings that answer truncated without moving
}

func (l *listingRecorder) ListObjects(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	l.mu.Lock()
	l.calls = append(l.calls, p)
	after, stuck := l.after, l.stuckN > 0
	l.mu.Unlock()
	if stuck && p.NS == meta.NSMultipart {
		return op.ListObjectsResult{Truncated: true, NextMarker: p.Marker}, nil
	}
	res, err := l.BucketStore.ListObjects(ctx, rec, p)
	if after != nil {
		after(p)
	}
	return res, err
}

func (l *listingRecorder) listings() []op.ListObjectsParams {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]op.ListObjectsParams(nil), l.calls...)
}

// adminRecorder is a bucket admin store that records the index check's
// calls.
type adminRecorder struct {
	op.BucketAdminStore
	mu       sync.Mutex
	removed  [][]meta.ObjKey
	rebuilds int
}

func (a *adminRecorder) RemoveIndexEntries(ctx context.Context, rec *op.BucketRecord, keys []meta.ObjKey) error {
	a.mu.Lock()
	a.removed = append(a.removed, append([]meta.ObjKey(nil), keys...))
	a.mu.Unlock()
	return a.BucketAdminStore.RemoveIndexEntries(ctx, rec, keys)
}

func (a *adminRecorder) RebuildIndex(ctx context.Context, rec *op.BucketRecord) error {
	a.mu.Lock()
	a.rebuilds++
	a.mu.Unlock()
	return a.BucketAdminStore.RebuildIndex(ctx, rec)
}

var _ = Describe("admin bucket index check", func() {
	var (
		store  *memstore.Store
		env    *op.Env
		lister *listingRecorder
		admin  *adminRecorder
	)
	setup := func(rel denc.Release) {
		GinkgoHelper()
		store = memstore.New(memstore.Config{Release: rel})
		lister = &listingRecorder{BucketStore: store}
		admin = &adminRecorder{BucketAdminStore: store}
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: lister, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: admin, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
		_, err := store.CreateBucket(context.Background(), op.CreateBucketParams{
			Name: "test", Owner: meta.UserOwner(meta.UserID{ID: "admin"}), Zonegroup: store.ZoneGroup().ID,
			Placement: meta.PlacementRule{Name: "default-placement"},
		})
		Expect(err).NotTo(HaveOccurred())
	}
	BeforeEach(func() { setup(denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := store.GetBucket(ctx, "", "test")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	setShards := func(ctx context.Context, n uint32) {
		GinkgoHelper()
		rec := bucket(ctx)
		rec.Info.Layout.Current.Layout.Normal.NumShards = n
		Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	put := func(ctx context.Context, key meta.ObjKey) {
		GinkgoHelper()
		_, err := store.PutObject(ctx, bucket(ctx), key, strings.NewReader("x"), op.PutParams{Size: 1})
		Expect(err).NotTo(HaveOccurred())
	}
	mp := func(name string) meta.ObjKey { return meta.ObjKey{Name: name, NS: meta.NSMultipart} }
	multipartNames := func(ctx context.Context) []string {
		GinkgoHelper()
		res, err := store.ListObjects(ctx, bucket(ctx), op.ListObjectsParams{NS: meta.NSMultipart, MaxKeys: 5000})
		Expect(err).NotTo(HaveOccurred())
		names := []string{}
		for _, e := range res.Entries {
			names = append(names, e.Key.Name)
		}
		return names
	}
	runWith := func(ctx context.Context, o op.Op, caps meta.Caps) error {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Run(ctx, o, &op.Request{Env: env, Identity: op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps}})
	}
	check := func(ctx context.Context, fix, objects bool) (*op.CheckBucketIndex, error) {
		o := op.NewCheckBucketIndex()
		o.Bucket, o.Fix, o.CheckObjects = "test", fix, objects
		return o, runWith(ctx, o, meta.Caps{"buckets": meta.CapWrite})
	}
	// seedUpload is an upload's meta entry and two parts, and an orphan part
	// whose upload has no meta entry.
	seedUpload := func(ctx context.Context) {
		GinkgoHelper()
		for _, n := range []string{"obj.2~abc.meta", "obj.2~abc.1", "obj.2~abc.2", "orph.2~zzz.1"} {
			put(ctx, mp(n))
		}
	}

	It("is check_bucket_index, checking buckets=write", func(ctx SpecContext) {
		Expect(op.NewCheckBucketIndex().Name()).To(Equal("check_bucket_index"))
		seedUpload(ctx)
		for _, caps := range []meta.Caps{nil, {"buckets": meta.CapRead}, {"users": meta.CapAll, "metadata": meta.CapAll}} {
			o := op.NewCheckBucketIndex()
			o.Bucket, o.Fix = "test", true
			Expect(runWith(ctx, o, caps)).To(MatchError(op.ErrAccessDenied), "caps %v", caps)
		}
		Expect(lister.listings()).To(BeEmpty(), "nothing is read for a refused caller")
		Expect(admin.removed).To(BeEmpty())
		Expect(admin.rebuilds).To(BeZero())
		Expect(multipartNames(ctx)).To(ContainElement("orph.2~zzz.1"))
	}, SpecTimeout(indexCheckTimeout))

	It("reports a part whose upload has no meta entry by its index key, removing it only with fix", func(ctx SpecContext) {
		seedUpload(ctx)
		o, err := check(ctx, false, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.InvalidMultipart).To(Equal([]string{"_multipart_orph.2~zzz.1"}))
		Expect(o.Objects).To(BeNil(), "no object check without check-objects")
		Expect(multipartNames(ctx)).To(ContainElement("orph.2~zzz.1"), "untouched without fix")
		Expect(admin.removed).To(BeEmpty())
		Expect(admin.rebuilds).To(BeZero())
		Expect(o.Existing).To(HaveKey("rgw.main"))
		Expect(o.Calculated).To(HaveKey("rgw.main"))

		o, err = check(ctx, true, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.InvalidMultipart).To(Equal([]string{"_multipart_orph.2~zzz.1"}), "the entries fixed are the ones reported")
		Expect(multipartNames(ctx)).To(Equal([]string{"obj.2~abc.1", "obj.2~abc.2", "obj.2~abc.meta"}))
		Expect(admin.rebuilds).To(Equal(1), "fix rebuilds the headers")

		o, err = check(ctx, true, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.InvalidMultipart).To(BeEmpty())
	}, SpecTimeout(indexCheckTimeout))

	It("lists the multipart namespace with versions, a thousand entries a page", func(ctx SpecContext) {
		for i := range 1001 {
			put(ctx, mp(fmt.Sprintf("orph%04d.2~zzz.1", i)))
		}
		o, err := check(ctx, true, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.InvalidMultipart).To(HaveLen(1001))
		Expect(o.InvalidMultipart[0]).To(Equal("_multipart_orph0000.2~zzz.1"))
		var pages []op.ListObjectsParams
		for _, p := range lister.listings() {
			if p.NS == meta.NSMultipart && p.Prefix == "" {
				pages = append(pages, p)
			}
		}
		Expect(len(pages)).To(BeNumerically(">=", 2))
		Expect(pages[0]).To(Equal(op.ListObjectsParams{NS: meta.NSMultipart, ListVersions: true, MaxKeys: 1000}))
		Expect(pages[1].Marker).To(Equal("orph0999.2~zzz.1"), "the second page starts after the first")
		Expect(admin.removed).To(HaveLen(2), "removals go a thousand at a time")
		Expect(admin.removed[0]).To(HaveLen(1000))
		Expect(admin.removed[1]).To(Equal([]meta.ObjKey{mp("orph1000.2~zzz.1")}))
		Expect(multipartNames(ctx)).To(BeEmpty())
	}, SpecTimeout(indexCheckTimeout))

	It("fails a listing that stops moving rather than looping", func(ctx SpecContext) {
		seedUpload(ctx)
		lister.stuckN = 1
		short, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, err := check(short, true, false)
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(admin.removed).To(BeEmpty())
	}, SpecTimeout(indexCheckTimeout))

	Describe("a live upload", func() {
		// reupload is the head of a part sent twice: the second upload of
		// the part takes "<key>.<32 random characters>" for its prefix
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L414-L439 at v19.2.6).
		const reupload = "obj.0123456789abcdefghijABCDEFGHIJ.1"

		It("has a re-uploaded part reported as radosgw reports it, and kept by fix", func(ctx SpecContext) {
			seedUpload(ctx)
			put(ctx, mp(reupload))
			o, err := check(ctx, true, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.InvalidMultipart).To(Equal([]string{"_multipart_" + reupload, "_multipart_orph.2~zzz.1"}))
			Expect(multipartNames(ctx)).To(ContainElement(reupload), "an entry of an object with a live upload stays")
			Expect(multipartNames(ctx)).NotTo(ContainElement("orph.2~zzz.1"), "an orphan of an object without one goes")
			for _, batch := range admin.removed {
				Expect(batch).NotTo(ContainElement(mp(reupload)))
			}
		}, SpecTimeout(indexCheckTimeout))

		It("is found by a listing after the check's own, so an upload begun as the check ran keeps its part", func(ctx SpecContext) {
			put(ctx, mp(reupload))
			once := sync.Once{}
			lister.after = func(p op.ListObjectsParams) {
				if p.NS == meta.NSMultipart && p.Prefix == "" {
					once.Do(func() { put(ctx, mp("obj.2~late.meta")) })
				}
			}
			o, err := check(ctx, true, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.InvalidMultipart).To(Equal([]string{"_multipart_" + reupload}))
			Expect(multipartNames(ctx)).To(ContainElement(reupload))
		}, SpecTimeout(indexCheckTimeout))
	})

	Describe("the report's order", func() {
		// againstOrder are two object names whose shards of 11 run against
		// their names' order.
		againstOrder := func() (first, second string) {
			GinkgoHelper()
			names := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
			for i := range names {
				for j := i + 1; j < len(names); j++ {
					a, _ := meta.IndexShard(names[i], 11)
					b, _ := meta.IndexShard(names[j], 11)
					if a > b {
						return names[i], names[j]
					}
				}
			}
			Fail("no two names hash against their order")
			return "", ""
		}
		seed := func(ctx context.Context) (first, second string) {
			GinkgoHelper()
			first, second = againstOrder()
			setShards(ctx, 11)
			for _, n := range []string{first + ".2~a.1", first + ".2~a.2", second + ".2~b.1"} {
				put(ctx, mp(n))
			}
			return first, second
		}
		It("is key order on Squid, all_objs being a map", func(ctx SpecContext) {
			first, second := seed(ctx)
			o, err := check(ctx, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.InvalidMultipart).To(Equal([]string{
				"_multipart_" + first + ".2~a.1", "_multipart_" + first + ".2~a.2", "_multipart_" + second + ".2~b.1",
			}))
		}, SpecTimeout(indexCheckTimeout))
		It("is shard by shard on Tentacle, key order within a shard", func(ctx SpecContext) {
			setup(denc.Tentacle)
			first, second := seed(ctx)
			o, err := check(ctx, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.InvalidMultipart).To(Equal([]string{
				"_multipart_" + second + ".2~b.1", "_multipart_" + first + ".2~a.1", "_multipart_" + first + ".2~a.2",
			}))
		}, SpecTimeout(indexCheckTimeout))
	})

	DescribeTable("finds what each release's check finds",
		func(ctx SpecContext, rel denc.Release, names, want []string) {
			setup(rel)
			setShards(ctx, 0)
			for _, n := range names {
				put(ctx, mp(n))
			}
			o, err := check(ctx, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.InvalidMultipart).To(Equal(want))
		},
		Entry("Squid reports a name without a dot unless <name>.meta is there", denc.Squid,
			[]string{"nodot", "kept", "kept.meta"}, []string{"_multipart_nodot"}),
		Entry("Tentacle skips a name without a dot", denc.Tentacle,
			[]string{"nodot", "kept", "kept.meta"}, []string{}),
		Entry("Squid takes a part whose meta entry is anywhere as valid", denc.Squid,
			[]string{"a.2~x.1", "a.2~x.1.2~y.1", "a.2~x.1.2~y.meta", "a.2~x.meta"}, []string{}),
		Entry("Tentacle reports parts another upload's meta entry follows", denc.Tentacle,
			[]string{"x.2~a.1", "x.2~a.2", "y.2~b.meta"}, []string{"_multipart_x.2~a.1", "_multipart_x.2~a.2"}),
		Entry("Tentacle reports a part another upload's entries separate from its meta entry on the shard",
			denc.Tentacle,
			[]string{"a.2~x.1", "a.2~x.1.2~y.1", "a.2~x.1.2~y.meta", "a.2~x.meta"}, []string{"_multipart_a.2~x.1"}),
	)

	It("keeps on Tentacle a part it reports whose object has a live upload", func(ctx SpecContext) {
		setup(denc.Tentacle)
		setShards(ctx, 0)
		for _, n := range []string{"a.2~x.1", "a.2~x.1.2~y.1", "a.2~x.1.2~y.meta", "a.2~x.meta"} {
			put(ctx, mp(n))
		}
		o, err := check(ctx, true, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.InvalidMultipart).To(Equal([]string{"_multipart_a.2~x.1"}))
		Expect(multipartNames(ctx)).To(ContainElement("a.2~x.1"))
	}, SpecTimeout(indexCheckTimeout))

	It("refuses an indexless bucket on Tentacle, whose multipart check reads shards", func(ctx SpecContext) {
		setup(denc.Tentacle)
		rec := bucket(ctx)
		rec.Info.Layout.Current.Layout.Type = meta.IndexIndexless
		Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
		_, err := check(ctx, true, false)
		Expect(err).To(MatchError(op.ErrInvalidArgument))
		Expect(admin.rebuilds).To(BeZero())
	}, SpecTimeout(indexCheckTimeout))

	It("refuses check-objects without fix, removing nothing", func(ctx SpecContext) {
		seedUpload(ctx)
		_, err := check(ctx, false, true)
		Expect(err).To(MatchError(op.ErrInvalidArgument))
		Expect(multipartNames(ctx)).To(ContainElement("orph.2~zzz.1"))
		Expect(admin.rebuilds).To(BeZero())
	}, SpecTimeout(indexCheckTimeout))

	It("lists the objects with check-objects and fix, without versions, a name each", func(ctx SpecContext) {
		put(ctx, meta.ObjKey{Name: "b"})
		put(ctx, meta.ObjKey{Name: "a"})
		seedUpload(ctx)
		o, err := check(ctx, true, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Objects).To(Equal([]string{"a", "b"}))
		var plain []op.ListObjectsParams
		for _, p := range lister.listings() {
			if p.NS == "" {
				plain = append(plain, p)
			}
		}
		Expect(plain).To(Equal([]op.ListObjectsParams{{MaxKeys: 1000}}))
	}, SpecTimeout(indexCheckTimeout))

	It("answers a bucket that does not exist NoSuchKey, and no bucket InvalidArgument", func(ctx SpecContext) {
		o := op.NewCheckBucketIndex()
		o.Bucket = "foo"
		Expect(runWith(ctx, o, meta.Caps{"buckets": meta.CapWrite})).To(MatchError(op.ErrNoSuchKey))
		o = op.NewCheckBucketIndex()
		Expect(runWith(ctx, o, meta.Caps{"buckets": meta.CapWrite})).To(MatchError(op.ErrInvalidArgument))
	}, SpecTimeout(indexCheckTimeout))
})
