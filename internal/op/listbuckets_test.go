package op_test

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("ListBuckets", func() {
	var (
		store *memstore.Store
		env   *op.Env
		alice *op.UserRecord
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{})
		env = &op.Env{Zone: store, Users: store, Buckets: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
		for _, name := range []string{"a", "b"} {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: name, Owner: meta.UserOwner(alice.Info.UserID)})
			Expect(err).NotTo(HaveOccurred())
		}
		bob := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, OpMask: op.OpTypeAll})
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "c", Owner: meta.UserOwner(bob.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
	})
	identity := func(rec *op.UserRecord) op.Identity {
		return op.Identity{User: &rec.Info, Owner: meta.UserOwner(rec.Info.UserID), OpMask: rec.Info.OpMask}
	}
	// run executes o and returns its Begin and Page calls in order: "begin",
	// then each page as its comma-joined bucket names.
	run := func(ctx context.Context, o *op.ListBuckets, r *op.Request) ([]string, error) {
		var calls []string
		o.Begin = func() error {
			calls = append(calls, "begin")
			return nil
		}
		o.Page = func(page []meta.BucketEnt) error {
			names := make([]string, 0, len(page))
			for _, b := range page {
				names = append(names, b.Bucket.Name)
			}
			calls = append(calls, strings.Join(names, ","))
			return nil
		}
		err := op.Run(ctx, o, r)
		return calls, err
	}
	It("is radosgw's list_buckets, a read authorized as s3:ListAllMyBuckets", func() {
		o := &op.ListBuckets{}
		Expect(o.Name()).To(Equal("list_buckets"))
		Expect(o.Action()).To(Equal(policy.S3ListAllMyBuckets))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
	})
	It("hands the identity's own buckets to Page in name order, after Begin", func(ctx SpecContext) {
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", "a,b"}))
	})
	It("reads rgw_list_buckets_max_chunk buckets at a time and hands on each page as it is read", func(ctx SpecContext) {
		env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_list_buckets_max_chunk": "1"})
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", "a", "b"}))
	})
	It("stops at a page that lists nothing, as radosgw's loop stops at an empty marker", func(ctx SpecContext) {
		env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_list_buckets_max_chunk": "0"})
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", ""}), "a zero chunk reads nothing, and the listing ends there")
	})
	It("reads the default chunk for a chunk option below zero or past an int32", func(ctx SpecContext) {
		for _, v := range []string{"-1", "4294967296"} {
			users := &opfakes.FakeUserStore{}
			env.Users = users
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_list_buckets_max_chunk": v})
			_, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
			Expect(err).NotTo(HaveOccurred(), v)
			Expect(users.ListUserBucketsCallCount()).To(Equal(1), v)
			_, _, _, maxEntries := users.ListUserBucketsArgsForCall(0)
			Expect(maxEntries).To(Equal(1000), "rgw_list_buckets_max_chunk %s", v)
		}
	})
	It("stops at Limit and starts after Marker", func(ctx SpecContext) {
		calls, err := run(ctx, &op.ListBuckets{Limit: 1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", "a"}))
		calls, err = run(ctx, &op.ListBuckets{Limit: -1, Marker: "a"}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", "b"}))
	})
	It("begins an empty listing for a zero Limit without touching the store", func(ctx SpecContext) {
		users := &opfakes.FakeUserStore{}
		env.Users = users
		calls, err := run(ctx, &op.ListBuckets{Limit: 0}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin"}))
		Expect(users.ListUserBucketsCallCount()).To(BeZero())
	})
	It("begins an empty listing for an anonymous identity without touching the store", func(ctx SpecContext) {
		users := &opfakes.FakeUserStore{}
		env.Users = users
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: op.Anonymous()})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin"}))
		Expect(users.ListUserBucketsCallCount()).To(BeZero(), "radosgw skips list_buckets() for the anonymous user")
	})
	It("lists the identity's owner, the account for an account user", func(ctx SpecContext) {
		users := &opfakes.FakeUserStore{}
		env.Users = users
		id := identity(alice)
		id.Owner = meta.AccountOwner("RGW11111111111111111")
		_, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: id})
		Expect(err).NotTo(HaveOccurred())
		Expect(users.ListUserBucketsCallCount()).To(Equal(1))
		_, owner, marker, _ := users.ListUserBucketsArgsForCall(0)
		Expect(owner.String()).To(Equal("RGW11111111111111111"))
		Expect(marker).To(BeEmpty())
	})
	It("returns a failed first page before Begin and a failed later page after it", func(ctx SpecContext) {
		users := &opfakes.FakeUserStore{}
		env.Users = users
		users.ListUserBucketsReturnsOnCall(0, nil, "", false, op.ErrInternalError)
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(calls).To(BeEmpty(), "send_response_begin renders the error document")
		users.ListUserBucketsReturnsOnCall(1, []meta.BucketEnt{{Bucket: meta.BucketID{Name: "a"}}}, "a", true, nil)
		users.ListUserBucketsReturnsOnCall(2, nil, "", false, op.ErrInternalError)
		calls, err = run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(calls).To(Equal([]string{"begin", "a"}), "the response is under way when the second page fails")
	})
	It("denies an identity whose op mask lacks read", func(ctx SpecContext) {
		id := identity(alice)
		id.OpMask = op.OpTypeWrite
		_, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: id})
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})
	Describe("an admin identity", func() {
		var authz *opfakes.FakeAuthorizer
		BeforeEach(func() {
			authz = &opfakes.FakeAuthorizer{}
			env.Authz = authz
		})
		admin := func() op.Identity {
			id := identity(alice)
			id.Admin = true
			return id
		}
		It("lists past the authorizer's access denial, which VerifyPermission returns for Run to override", func(ctx SpecContext) {
			authz.VerifyUserReturns(op.ErrAccessDenied)
			calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: admin()})
			Expect(err).NotTo(HaveOccurred())
			Expect(calls).To(Equal([]string{"begin", "a,b"}))
			_, _, action := authz.VerifyUserArgsForCall(0)
			Expect(action).To(Equal(policy.S3ListAllMyBuckets))
		})
		It("gets any other error from the authorizer", func(ctx SpecContext) {
			authz.VerifyUserReturns(op.ErrInternalError)
			calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: admin()})
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(calls).To(BeEmpty())
		})
	})
})
