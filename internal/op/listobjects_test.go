package op_test

import (
	"context"
	"errors"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("ListObjects", func() {
	var (
		store *memstore.Store
		env   *op.Env
		alice *op.UserRecord
	)
	newStore := func(ctx context.Context, rel denc.Release) {
		store = memstore.New(memstore.Config{Release: rel})
		env = &op.Env{Zone: store, Users: store, Buckets: store, Objects: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		for i := range 5 {
			_, err := store.PutObject(ctx, rec, meta.ObjKey{Name: fmt.Sprintf("k%d", i)}, strings.NewReader("v"), op.PutParams{Size: 1})
			Expect(err).NotTo(HaveOccurred())
		}
	}
	BeforeEach(func(ctx SpecContext) { newStore(ctx, denc.Squid) })
	request := func() *op.Request {
		return &op.Request{
			Env: env, Bucket: "plain",
			Identity: op.Identity{User: &alice.Info, Owner: meta.UserOwner(alice.Info.UserID), OpMask: alice.Info.OpMask},
		}
	}
	// setInfo rewrites the bucket's instance through change.
	setInfo := func(ctx context.Context, change func(*meta.BucketInfo)) {
		rec, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		change(&rec.Info)
		Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
	}

	It("is radosgw's list_bucket for both versions, a read authorized as s3:ListBucket", func() {
		o := &op.ListObjects{V2: true}
		Expect(o.Name()).To(Equal("list_bucket"), "RGWListBucket::name, rgw_op.h:970 at v19.2.6, which the v2 handler inherits")
		Expect(o.Action()).To(Equal(policy.S3ListBucket))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
		Expect((&op.ListObjects{ListVersions: true}).Action()).To(Equal(policy.S3ListBucketVersions), "rgw_op.cc:3054-3057")
	})
	DescribeTable("bounds max-keys as parse_value_and_bound does",
		func(ctx SpecContext, maxKeys string, conf map[string]string, wantMax, wantEntries int, truncated bool) {
			if conf != nil {
				env.Conf = cephconf.NewOptions(cephconf.MapGetter(conf))
			}
			o := &op.ListObjects{MaxKeys: maxKeys}
			Expect(op.Run(ctx, o, request())).To(Succeed())
			Expect(o.Max).To(Equal(wantMax))
			Expect(o.Result.Entries).To(HaveLen(wantEntries))
			Expect(o.Result.Truncated).To(Equal(truncated))
		},
		Entry("absent: the S3 default of 1000", "", nil, 1000, 5, false),
		Entry("zero lists nothing", "0", nil, 0, 0, true),
		Entry("a page", "2", nil, 2, 2, true),
		Entry("above rgw_max_listing_results: capped", "5000", map[string]string{"rgw_max_listing_results": "3"}, 3, 3, true),
		Entry("negative: raised to 0", "-4", nil, 0, 0, true),
	)
	DescribeTable("bounds max-keys by each release's rgw_max_listing_results default",
		func(ctx SpecContext, rel denc.Release, want int) {
			newStore(ctx, rel)
			o := &op.ListObjects{MaxKeys: "25000"}
			Expect(op.Run(ctx, o, request())).To(Succeed())
			Expect(o.Max).To(Equal(want))
		},
		Entry("Squid, 1000: rgw.yaml.in:3428-3435 at v19.2.6", denc.Squid, 1000),
		Entry("Tentacle, 5000: rgw.yaml.in:3613-3620 at v20.2.4", denc.Tentacle, 5000),
	)
	It("answers InvalidArgument for a max-keys that is not a number, before the permission check", func(ctx SpecContext) {
		r := request()
		r.Identity = op.Anonymous()
		err := op.Run(ctx, &op.ListObjects{MaxKeys: "x"}, r)
		Expect(err).To(MatchError(op.ErrInvalidArgument), "get_params runs first in RGWListBucket::verify_permission, rgw_op.cc:3035-3038")
	})
	It("refuses an unordered listing with a delimiter", func(ctx SpecContext) {
		err := op.Run(ctx, &op.ListObjects{AllowUnordered: true, Delimiter: "/"}, request())
		Expect(err).To(MatchError(op.ErrInvalidArgument), "rgw_op.cc:3085-3090")
	})
	It("lists unordered without a delimiter", func(ctx SpecContext) {
		o := &op.ListObjects{AllowUnordered: true}
		Expect(op.Run(ctx, o, request())).To(Succeed())
		Expect(o.Result.Entries).To(HaveLen(5))
	})
	It("passes its marker, prefix, delimiter and versions to the store", func(ctx SpecContext) {
		buckets := &opfakes.FakeBucketStore{}
		rec, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		buckets.GetBucketReturns(rec, nil)
		env.Buckets = buckets
		o := &op.ListObjects{Prefix: "p", Delimiter: "/", Marker: "m", MaxKeys: "7", ListVersions: true}
		Expect(op.Run(ctx, o, request())).To(Succeed())
		_, _, p := buckets.ListObjectsArgsForCall(0)
		Expect(p).To(Equal(op.ListObjectsParams{Prefix: "p", Delimiter: "/", Marker: "m", MaxKeys: 7, ListVersions: true}))
	})
	It("answers NoSuchBucket for a missing bucket before its parameters", func(ctx SpecContext) {
		r := request()
		r.Bucket = "missing"
		Expect(op.Run(ctx, &op.ListObjects{MaxKeys: "x"}, r)).To(MatchError(op.ErrNoSuchBucket),
			"rgw_build_bucket_policies fails init_permissions, rgw_op.cc:538-541 at v19.2.6")
	})
	It("refuses an indexless bucket on Tentacle with radosgw's message", func(ctx SpecContext) {
		newStore(ctx, denc.Tentacle)
		setInfo(ctx, func(i *meta.BucketInfo) { i.Layout.Current.Layout.Type = meta.IndexIndexless })
		err := op.Run(ctx, &op.ListObjects{}, request())
		Expect(err).To(MatchError(op.ErrMethodNotAllowed), "v20.2.4 rgw_op.cc:3319-3324")
		e, ok := errors.AsType[*op.Error](err)
		Expect(ok).To(BeTrue())
		Expect(e.Message).To(Equal("Indexless buckets cannot be listed"))
	})
	It("lists an indexless bucket on Squid", func(ctx SpecContext) {
		setInfo(ctx, func(i *meta.BucketInfo) { i.Layout.Current.Layout.Type = meta.IndexIndexless })
		o := &op.ListObjects{}
		Expect(op.Run(ctx, o, request())).To(Succeed())
		Expect(o.Result.Entries).To(HaveLen(5))
	})
	It("lists the versions of a bucket whose versioning was never enabled", func(ctx SpecContext) {
		o := &op.ListObjects{ListVersions: true}
		Expect(op.Run(ctx, o, request())).To(Succeed())
		Expect(o.Result.Entries).To(HaveLen(5))
	})
	DescribeTable("answers NotImplemented for the versions of a bucket whose versioning was ever enabled",
		func(ctx SpecContext, flags uint32) {
			setInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= flags })
			Expect(op.Run(ctx, &op.ListObjects{ListVersions: true}, request())).To(MatchError(op.ErrNotImplemented))
			o := &op.ListObjects{}
			Expect(op.Run(ctx, o, request())).To(Succeed(), "a plain listing is served")
		},
		Entry("enabled", uint32(meta.BucketVersioned)),
		Entry("suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended)),
	)
})
