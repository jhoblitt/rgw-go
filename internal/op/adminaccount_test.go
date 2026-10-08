package op_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var _ = Describe("admin account ops", func() {
	const (
		id1 = "RGW00000000000000001"
		id2 = "RGW00000000000000002"
	)
	var (
		store *memstore.Store
		env   *op.Env
	)
	newEnv := func(rel denc.Release) {
		pools := meta.NewZonePlacementInfo()
		pools.StorageClasses = meta.ZoneStorageClasses{meta.StorageClassStandard: {}}
		store = memstore.New(memstore.Config{Release: rel, Params: meta.ZoneParams{
			Name: "z1", PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": pools},
		}})
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
	}
	BeforeEach(func() { newEnv(denc.Squid) })
	runAs := func(ctx context.Context, o op.Op, caps meta.Caps) error {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Run(ctx, o, &op.Request{Env: env, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps,
		}})
	}
	run := func(ctx context.Context, o op.Op) error {
		return runAs(ctx, o, meta.Caps{"accounts": meta.CapAll, "account": meta.CapAll})
	}
	create := func(ctx context.Context, p op.AccountParams) meta.AccountInfo {
		GinkgoHelper()
		o := &op.CreateAccount{Params: p}
		Expect(run(ctx, o)).To(Succeed())
		return o.Result
	}
	get := func(ctx context.Context, p op.AccountParams) (meta.AccountInfo, error) {
		o := &op.GetAccountInfo{Params: p}
		err := run(ctx, o)
		return o.Result, err
	}

	Describe("create", func() {
		It("creates an account with the id and name given and radosgw's defaults", func(ctx SpecContext) {
			got := create(ctx, op.AccountParams{ID: id1, Name: "acme", Tenant: "t", Email: "ops@acme.example"})
			want := meta.NewAccountInfo()
			want.ID, want.Name, want.Tenant, want.Email = id1, "acme", "t", "ops@acme.example"
			Expect(got).To(Equal(want))
			rec, err := store.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info).To(Equal(want))
		})
		It("applies the limits given and the configured default quotas", func(ctx SpecContext) {
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{
				"rgw_account_default_quota_max_size": "1024", "rgw_account_default_quota_max_objects": "-1",
				"rgw_bucket_default_quota_max_objects": "7", "rgw_bucket_default_quota_max_size": "-1",
			})
			got := create(ctx, op.AccountParams{
				ID: id1, MaxUsers: new(int32(1)), MaxRoles: new(int32(2)), MaxGroups: new(int32(3)),
				MaxAccessKeys: new(int32(4)), MaxBuckets: new(int32(5)),
			})
			Expect([]int32{got.MaxUsers, got.MaxRoles, got.MaxGroups, got.MaxAccessKeys, got.MaxBuckets}).To(Equal([]int32{1, 2, 3, 4, 5}))
			Expect(got.Quota).To(Equal(meta.Quota{MaxSize: 1024, MaxObjects: -1, Enabled: true}), "rgw_quota.cc:1022-1032")
			Expect(got.BucketQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: 7, Enabled: true}))
		})
		It("refuses an id an account holds", func(ctx SpecContext) {
			create(ctx, op.AccountParams{ID: id1, Name: "acme"})
			Expect(run(ctx, &op.CreateAccount{Params: op.AccountParams{ID: id1, Name: "other"}})).To(MatchError(op.ErrAccountAlreadyExists))
			Expect(run(ctx, &op.CreateAccount{Params: op.AccountParams{ID: id2, Name: "acme"}})).To(MatchError(op.ErrAccountAlreadyExists))
		})
		It("generates the id when none is given: RGW and 17 random digits", func(ctx SpecContext) {
			seen := map[string]bool{}
			for range 20 {
				got := create(ctx, op.AccountParams{})
				Expect(got.ID).To(MatchRegexp(`^RGW[0-9]{17}$`))
				Expect(seen).NotTo(HaveKey(got.ID))
				seen[got.ID] = true
			}
		})
		DescribeTable("refuses an account name validate_name refuses, writing nothing",
			func(ctx SpecContext, name, msg string) {
				err := run(ctx, &op.CreateAccount{Params: op.AccountParams{ID: id1, Name: name}})
				Expect(err).To(MatchError(op.ErrInvalidArgument))
				Expect(op.AsError(err).Message).To(Equal(msg))
				_, err = store.GetAccount(ctx, id1)
				Expect(err).To(MatchError(op.ErrNoSuchEntity))
			},
			Entry("the tenant delimiter", "a$b", "account name must not contain $"),
			Entry("the metadata delimiter", "a:b", "account name must not contain :"),
			Entry("invalid UTF-8", "a\xffb", "account name must be valid utf8"),
			Entry("a UTF-16 surrogate", "a\xed\xa0\x80", "account name must be valid utf8"),
		)
		DescribeTable("refuses an id validate_id refuses",
			func(ctx SpecContext, id, msg string) {
				err := run(ctx, &op.CreateAccount{Params: op.AccountParams{ID: id}})
				Expect(err).To(MatchError(op.ErrInvalidArgument))
				Expect(op.AsError(err).Message).To(Equal(msg))
			},
			Entry("too short", "RGW0000000000000001", "account id must be 20 bytes long"),
			Entry("another prefix", "ABC00000000000000001", "account id must start with RGW"),
			Entry("a letter in the digits", "RGW0000000000000000a", "account id must end with numeric digits"),
			Entry("a user id", "alice", "account id must be 20 bytes long"),
		)
		It("answers an empty name as radosgw does, with no name", func(ctx SpecContext) {
			got := create(ctx, op.AccountParams{ID: id1})
			Expect(got.Name).To(BeEmpty(), "create validates a name only when one is given, rgw_account.cc:119-123")
		})
	})

	Describe("get", func() {
		BeforeEach(func(ctx SpecContext) {
			create(ctx, op.AccountParams{ID: id1, Tenant: "t", Name: "acme", Email: "Ops@Acme.example"})
		})
		It("finds an account by id, by tenant and name, and by email", func(ctx SpecContext) {
			for _, p := range []op.AccountParams{{ID: id1}, {Tenant: "t", Name: "acme"}, {Email: "ops@acme.example"}} {
				got, err := get(ctx, p)
				Expect(err).NotTo(HaveOccurred(), "%+v", p)
				Expect(got.ID).To(Equal(id1))
			}
			_, err := get(ctx, op.AccountParams{Name: "acme"})
			Expect(err).To(MatchError(op.ErrNoSuchKey), "the name is looked up in the empty tenant")
		})
		It("requires an id, a name or an email", func(ctx SpecContext) {
			_, err := get(ctx, op.AccountParams{Tenant: "t"})
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(op.AsError(err).Message).To(Equal("requires --account-id or --account-name or --email"))
		})
		It("answers a missing account with the raw ENOENT's NoSuchKey", func(ctx SpecContext) {
			for _, p := range []op.AccountParams{{ID: id2}, {Name: "nosuch"}, {Email: "nosuch@example.com"}} {
				_, err := get(ctx, p)
				Expect(err).To(MatchError(op.ErrNoSuchKey), "%+v", p)
			}
		})
	})

	Describe("modify", func() {
		BeforeEach(func(ctx SpecContext) {
			create(ctx, op.AccountParams{ID: id1, Tenant: "t", Name: "acme"})
			create(ctx, op.AccountParams{ID: id2, Tenant: "t", Name: "other"})
		})
		modify := func(ctx context.Context, p op.AccountParams) (meta.AccountInfo, error) {
			o := &op.ModifyAccount{Params: p}
			err := run(ctx, o)
			return o.Result, err
		}
		It("renames an account and sets what is given", func(ctx SpecContext) {
			got, err := modify(ctx, op.AccountParams{ID: id1, Name: "acme2", Email: "a@b.example", MaxUsers: new(int32(9))})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Name).To(Equal("acme2"))
			Expect(got.Email).To(Equal("a@b.example"))
			Expect(got.MaxUsers).To(BeEquivalentTo(9))
			Expect(got.MaxRoles).To(BeEquivalentTo(meta.DefaultAccountRoleLimit), "unchanged")
			byName, err := get(ctx, op.AccountParams{Tenant: "t", Name: "acme2"})
			Expect(err).NotTo(HaveOccurred())
			Expect(byName.ID).To(Equal(id1))
		})
		It("refuses a change of tenant, and takes the same tenant", func(ctx SpecContext) {
			_, err := modify(ctx, op.AccountParams{ID: id1, Tenant: "u"})
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(op.AsError(err).Message).To(Equal("cannot modify account tenant"))
			_, err = modify(ctx, op.AccountParams{ID: id1, Tenant: "t", MaxUsers: new(int32(3))})
			Expect(err).NotTo(HaveOccurred())
		})
		It("refuses a name validate_name refuses", func(ctx SpecContext) {
			_, err := modify(ctx, op.AccountParams{ID: id1, Name: "a$b"})
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		})
		It("answers a name another account holds with the BucketAlreadyExists row, as modify passes EEXIST through", func(ctx SpecContext) {
			_, err := modify(ctx, op.AccountParams{ID: id1, Name: "other"})
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
			got, err := get(ctx, op.AccountParams{ID: id1})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Name).To(Equal("acme"))
		})
		It("sets the account quota and then the bucket quota by scope", func(ctx SpecContext) {
			got, err := modify(ctx, op.AccountParams{ID: id1, QuotaScope: "account", QuotaMaxSize: new(int64(1073741824)), QuotaMaxObjects: new(int64(1000)), QuotaEnabled: new(true)})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Quota).To(Equal(meta.Quota{MaxSize: 1073741824, MaxObjects: 1000, Enabled: true}))
			Expect(got.BucketQuota).To(Equal(meta.NewAccountInfo().BucketQuota))
			got, err = modify(ctx, op.AccountParams{ID: id1, QuotaScope: "bucket", QuotaMaxObjects: new(int64(5))})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.BucketQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: 5}))
			Expect(got.Quota).To(Equal(meta.Quota{MaxSize: 1073741824, MaxObjects: 1000, Enabled: true}), "the account quota stays")
		})
		It("refuses a maximum size below -1, which would read as no limit, writing nothing", func(ctx SpecContext) {
			_, err := modify(ctx, op.AccountParams{ID: id1, QuotaScope: "account", QuotaMaxSize: new(int64(-2)), QuotaEnabled: new(true)})
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			got, err := get(ctx, op.AccountParams{ID: id1})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Quota).To(Equal(meta.NewAccountInfo().Quota))
			_, err = modify(ctx, op.AccountParams{ID: id1, QuotaScope: "account", QuotaMaxSize: new(int64(-1))})
			Expect(err).NotTo(HaveOccurred(), "-1 is no limit")
		})
		It("answers a missing account with NoSuchKey", func(ctx SpecContext) {
			_, err := modify(ctx, op.AccountParams{ID: "RGW00000000000000009", Name: "x"})
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})
	})

	Describe("remove", func() {
		remove := func(ctx context.Context, p op.AccountParams) error { return run(ctx, &op.RemoveAccount{Params: p}) }
		BeforeEach(func(ctx SpecContext) { create(ctx, op.AccountParams{ID: id1, Name: "acme", Email: "ops@acme.example"}) })
		It("removes an account whose users index names only users that are gone, as list_account_users skips them", func(ctx SpecContext) {
			Expect(store.AddAccountUser(ctx, id1, meta.UserInfo{UserID: meta.UserID{ID: "gone"}, DisplayName: "gone"})).To(Succeed())
			Expect(remove(ctx, op.AccountParams{ID: id1})).To(Succeed(), "rgw_sal_rados.cc:1420-1429")
		})
		It("finds a user past a page of entries whose users are gone", func(ctx SpecContext) {
			for i := range 101 {
				name := fmt.Sprintf("gone%03d", i)
				Expect(store.AddAccountUser(ctx, id1, meta.UserInfo{UserID: meta.UserID{ID: name}, DisplayName: name})).To(Succeed())
			}
			live := meta.NewUserInfo()
			live.UserID, live.DisplayName, live.AccountID = meta.UserID{ID: "live"}, "zz-live", id1
			store.AddUser(live)
			Expect(store.AddAccountUser(ctx, id1, live)).To(Succeed())
			Expect(remove(ctx, op.AccountParams{ID: id1})).To(MatchError(op.ErrBucketNotEmpty))
		})
		It("refuses an account with a user, with radosgw's message", func(ctx SpecContext) {
			u1 := meta.NewUserInfo()
			u1.UserID, u1.DisplayName, u1.AccountID = meta.UserID{ID: "u1"}, "u1", id1
			store.AddUser(u1)
			Expect(store.AddAccountUser(ctx, id1, meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "u1"})).To(Succeed())
			err := remove(ctx, op.AccountParams{ID: id1})
			Expect(err).To(MatchError(op.ErrBucketNotEmpty), "-ENOTEMPTY, rgw_account.cc:316-319")
			Expect(op.AsError(err).Message).To(Equal("The account cannot be deleted until all users are removed."))
			_, err = store.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
		})
		It("refuses an account with a bucket", func(ctx SpecContext) {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: meta.AccountOwner(id1), Placement: meta.PlacementRule{Name: "default-placement"}})
			Expect(err).NotTo(HaveOccurred())
			err = remove(ctx, op.AccountParams{ID: id1})
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists), "-EEXIST, rgw_account.cc:343-346")
			Expect(op.AsError(err).Message).To(Equal("The account cannot be deleted until all buckets are removed."))
		})
		It("removes an empty account, found by name or email too", func(ctx SpecContext) {
			Expect(remove(ctx, op.AccountParams{Email: "ops@acme.example"})).To(Succeed())
			_, err := get(ctx, op.AccountParams{ID: id1})
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			Expect(remove(ctx, op.AccountParams{ID: id1})).To(MatchError(op.ErrNoSuchKey))
			create(ctx, op.AccountParams{ID: id2, Name: "acme"})
			Expect(remove(ctx, op.AccountParams{Name: "acme"})).To(Succeed())
		})
		It("answers an account removed meanwhile with NoSuchKey", func(ctx SpecContext) {
			fake := &opfakes.FakeAccountStore{}
			rec, err := store.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			fake.GetAccountReturns(rec, nil)
			fake.RemoveAccountReturns(fmt.Errorf("account %s: %w", id1, op.ErrNoSuchEntity))
			env.Accounts = fake
			Expect(remove(ctx, op.AccountParams{ID: id1})).To(MatchError(op.ErrNoSuchKey))
		})
	})

	Describe("permissions", func() {
		DescribeTable("checks the caps each release's op checks",
			func(ctx SpecContext, rel denc.Release, o func() op.Op, caps meta.Caps, admitted bool) {
				newEnv(rel)
				create(ctx, op.AccountParams{ID: id1, Name: "acme"})
				err := runAs(ctx, o(), caps)
				if admitted {
					Expect(err).To(Or(Not(HaveOccurred()), Not(MatchError(op.ErrAccessDenied))))
				} else {
					Expect(err).To(MatchError(op.ErrAccessDenied))
				}
			},
			Entry("get on Squid with account=read", denc.Squid, accountGetOp, meta.Caps{"account": meta.CapRead}, true),
			Entry("get on Squid with accounts=read", denc.Squid, accountGetOp, meta.Caps{"accounts": meta.CapRead}, false),
			Entry("get on Tentacle with accounts=read", denc.Tentacle, accountGetOp, meta.Caps{"accounts": meta.CapRead}, true),
			Entry("get on Tentacle with account=read", denc.Tentacle, accountGetOp, meta.Caps{"account": meta.CapRead}, false),
			Entry("remove on Squid with account=write", denc.Squid, accountRemoveOp, meta.Caps{"account": meta.CapWrite}, true),
			Entry("remove on Squid with accounts=write", denc.Squid, accountRemoveOp, meta.Caps{"accounts": meta.CapWrite}, false),
			Entry("remove on Tentacle with accounts=write", denc.Tentacle, accountRemoveOp, meta.Caps{"accounts": meta.CapWrite}, true),
			Entry("remove on Tentacle with account=write", denc.Tentacle, accountRemoveOp, meta.Caps{"account": meta.CapWrite}, false),
			Entry("create on Squid with accounts=write", denc.Squid, accountCreateOp, meta.Caps{"accounts": meta.CapWrite}, true),
			Entry("create with accounts=read", denc.Tentacle, accountCreateOp, meta.Caps{"accounts": meta.CapRead}, false),
			Entry("create with account=write", denc.Squid, accountCreateOp, meta.Caps{"account": meta.CapWrite}, false),
			Entry("modify on Squid with accounts=write", denc.Squid, accountModifyOp, meta.Caps{"accounts": meta.CapWrite}, true),
			Entry("modify with account=write", denc.Tentacle, accountModifyOp, meta.Caps{"account": meta.CapWrite}, false),
		)
		It("checks the caps before any lookup, so a refused caller learns nothing of the account", func(ctx SpecContext) {
			fake := &opfakes.FakeAccountStore{}
			env.Accounts = fake
			users := &opfakes.FakeUserStore{}
			env.Users = users
			for _, o := range []op.Op{
				&op.GetAccountInfo{Params: op.AccountParams{ID: id2}},
				&op.GetAccountInfo{Params: op.AccountParams{Name: "nosuch"}},
				&op.ModifyAccount{Params: op.AccountParams{ID: id2, Name: "x"}},
				&op.RemoveAccount{Params: op.AccountParams{Email: "x@y"}},
				&op.CreateAccount{Params: op.AccountParams{ID: id2, Name: "x"}},
			} {
				Expect(runAs(ctx, o, meta.Caps{"users": meta.CapAll})).To(MatchError(op.ErrAccessDenied), "%T", o)
			}
			Expect(fake.Invocations()).To(BeEmpty())
			Expect(users.Invocations()).To(BeEmpty())
		})
		It("strips the store's error text, which can name the email, from what it returns", func(ctx SpecContext) {
			fake := &opfakes.FakeAccountStore{}
			fake.GetAccountByEmailReturns(nil, fmt.Errorf("reading users.email/secret.person@acme.example: %w", op.ErrInternalError))
			fake.PutAccountReturns(fmt.Errorf("writing users.email/secret.person@acme.example: %w", op.ErrInternalError))
			env.Accounts = fake
			err := run(ctx, &op.GetAccountInfo{Params: op.AccountParams{Email: "secret.person@acme.example"}})
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("secret.person"))
			err = run(ctx, &op.CreateAccount{Params: op.AccountParams{ID: id1, Email: "secret.person@acme.example"}})
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("secret.person"))
		})
	})
})

func accountGetOp() op.Op {
	return &op.GetAccountInfo{Params: op.AccountParams{ID: "RGW00000000000000001"}}
}

func accountRemoveOp() op.Op {
	return &op.RemoveAccount{Params: op.AccountParams{ID: "RGW00000000000000001"}}
}

func accountCreateOp() op.Op {
	return &op.CreateAccount{Params: op.AccountParams{ID: "RGW00000000000000002"}}
}

func accountModifyOp() op.Op {
	return &op.ModifyAccount{Params: op.AccountParams{ID: "RGW00000000000000001", MaxUsers: new(int32(2))}}
}
