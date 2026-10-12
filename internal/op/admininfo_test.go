package op_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("admin ops", func() {
	const fsid = "75d1938b-2949-4933-8386-fb2d1449ff03"
	var (
		store *memstore.Store
		env   *op.Env
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{Params: meta.ZoneParams{Name: "z1"}})
		env = &op.Env{Zone: store, Metrics: op.NopMetrics{}, ClusterID: fsid, Usage: store}
	})
	withCaps := func(caps meta.Caps) op.Identity {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps}
	}
	It("checks the info cap and returns the fsid", func(ctx SpecContext) {
		o := op.NewGetInfo()
		r := &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapRead})}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.ClusterID).To(Equal(fsid))
	})
	It("denies a missing cap and an anonymous identity", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapWrite})})).To(MatchError(op.ErrAccessDenied))
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: op.Anonymous()})).To(MatchError(op.ErrAccessDenied))
	})
	It("lets an admin identity through without caps, as rgw_process_authenticated does", func(ctx SpecContext) {
		id := withCaps(nil)
		id.Admin = true
		o := &op.GetInfo{}
		Expect(op.Run(ctx, o, &op.Request{Env: env, Identity: id})).To(Succeed())
		Expect(o.ClusterID).To(Equal(fsid))
	})
	It("passes the op-mask check with a zero mask", func(ctx SpecContext) {
		id := withCaps(meta.Caps{"info": meta.CapRead})
		id.OpMask = 0
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: id})).To(Succeed())
	})
	It("reads the zone params under zone=read", func(ctx SpecContext) {
		o := op.NewGetZoneConfig()
		r := &op.Request{Env: env, Identity: withCaps(meta.Caps{"zone": meta.CapAll})}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.Params.Name).To(Equal("z1"))
		Expect(op.Run(ctx, op.NewGetZoneConfig(), &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapRead})})).To(MatchError(op.ErrAccessDenied))
	})
	It("names itself and asks for no action and no op-mask bit", func() {
		Expect((&op.GetInfo{}).Name()).To(Equal("get_info"))
		Expect((&op.GetZoneConfig{}).Name()).To(Equal("get_zone_config"))
		Expect((&op.GetInfo{}).Action()).To(Equal(policy.ActionNone))
		Expect((&op.GetInfo{}).OpMask()).To(BeZero())
	})
	It("logs no usage: radosgw registers the admin API without set_logging", func(ctx SpecContext) {
		r := &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapRead, "zone": meta.CapRead})}
		Expect(op.Run(ctx, &op.GetInfo{}, r)).To(Succeed())
		Expect(op.Run(ctx, &op.GetZoneConfig{}, r)).To(Succeed())
		Expect(store.Usage()).To(BeEmpty())
	})
})

// racingUsers runs race after each user it reads, as another writer that
// changes the user between the op's read and its write.
type racingUsers struct {
	op.UserStore
	race func(ctx context.Context)
}

func (u racingUsers) GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error) {
	rec, err := u.UserStore.GetUser(ctx, id)
	if err == nil && u.race != nil {
		u.race(ctx)
	}
	return rec, err
}

// optsUsers records the options of each user write.
type optsUsers struct {
	op.UserStore
	opts *[]op.PutUserOptions
}

func (u optsUsers) PutUser(ctx context.Context, rec *op.UserRecord, opts op.PutUserOptions) error {
	*u.opts = append(*u.opts, opts)
	return u.UserStore.PutUser(ctx, rec, opts)
}

// racingBuckets is racingUsers for the bucket a ratelimit op reads.
type racingBuckets struct {
	op.BucketStore
	race func(ctx context.Context)
}

func (b racingBuckets) GetBucket(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
	rec, err := b.BucketStore.GetBucket(ctx, tenant, name)
	if err == nil && b.race != nil {
		b.race(ctx)
	}
	return rec, err
}

var _ = Describe("admin realm, period and ratelimit ops", func() {
	var (
		store    *memstore.Store
		env      *op.Env
		realm    meta.Realm
		zoneRead meta.Caps
		rlAll    meta.Caps
		yes      bool
	)
	BeforeEach(func(ctx SpecContext) {
		zoneRead = meta.Caps{"zone": meta.CapRead}
		rlAll = meta.Caps{"ratelimit": meta.CapAll}
		yes = true
		realm = meta.Realm{ID: "R", Name: "r1", CurrentPeriod: "P", Epoch: 2}
		period := meta.NewPeriod()
		period.ID, period.Epoch, period.RealmID = "P", 2, "R"
		store = memstore.New(memstore.Config{Params: meta.ZoneParams{Name: "z1"}, Realm: realm, Period: period})
		period.Epoch = 1
		store.AddPeriod(period)
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{Tenant: "t", ID: "alice"}
		store.AddUser(info)
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Tenant: "t", Name: "b", Owner: meta.UserOwner(info.UserID), Placement: meta.PlacementRule{Name: "default-placement"},
		})
		Expect(err).NotTo(HaveOccurred())
		env = &op.Env{Zone: store, Users: store, Buckets: store, Realms: store, Metrics: op.NopMetrics{}}
	})
	runAs := func(ctx context.Context, o op.Op, caps meta.Caps) error {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Run(ctx, o, &op.Request{Env: env, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps,
		}})
	}
	attrOf := func(ctx context.Context, user bool) []byte {
		GinkgoHelper()
		if user {
			rec, err := store.GetUser(ctx, meta.UserID{Tenant: "t", ID: "alice"})
			Expect(err).NotTo(HaveOccurred())
			return rec.Attrs[meta.AttrRateLimit]
		}
		rec, err := store.GetBucket(ctx, "t", "b")
		Expect(err).NotTo(HaveOccurred())
		return rec.Attrs[meta.AttrRateLimit]
	}
	encoded := func(l meta.RateLimitInfo) []byte {
		e := denc.NewEncoder()
		l.Encode(e, denc.Squid)
		return e.Bytes()
	}
	ptr := func(v int64) *int64 { return &v }

	Describe("the realm and period getters", func() {
		It("read the realm by name, id or default, the realm list, and a period", func(ctx SpecContext) {
			for _, o := range []*op.GetRealm{{RealmName: "r1"}, {ID: "R"}, {}} {
				Expect(runAs(ctx, o, zoneRead)).To(Succeed())
				Expect(o.Result).To(Equal(realm))
			}
			l := &op.ListRealms{}
			Expect(runAs(ctx, l, zoneRead)).To(Succeed())
			Expect(l.DefaultID).To(Equal("R"))
			Expect(l.Names).To(Equal([]string{"r1"}))
			p := &op.GetPeriod{}
			Expect(runAs(ctx, p, zoneRead)).To(Succeed())
			Expect(p.Result.Epoch).To(BeEquivalentTo(2), "the default realm's current period at its latest epoch")
			p = &op.GetPeriod{RealmID: "R", PeriodID: "P", Epoch: 1}
			Expect(runAs(ctx, p, zoneRead)).To(Succeed())
			Expect(p.Result.Epoch).To(BeEquivalentTo(1))
		})
		It("answer NoSuchKey for what the store lacks", func(ctx SpecContext) {
			Expect(runAs(ctx, &op.GetRealm{RealmName: "nosuch"}, zoneRead)).To(MatchError(op.ErrNoSuchKey))
			Expect(runAs(ctx, &op.GetPeriod{PeriodID: "P", Epoch: 9}, zoneRead)).To(MatchError(op.ErrNoSuchKey))
		})
		DescribeTable("refuse a caller without zone=read before reading anything",
			func(ctx SpecContext, caps meta.Caps) {
				g := &op.GetRealm{}
				Expect(runAs(ctx, g, caps)).To(MatchError(op.ErrAccessDenied))
				Expect(g.Result).To(BeZero())
				l := &op.ListRealms{}
				Expect(runAs(ctx, l, caps)).To(MatchError(op.ErrAccessDenied))
				Expect(l.Names).To(BeNil())
				p := &op.GetPeriod{}
				Expect(runAs(ctx, p, caps)).To(MatchError(op.ErrAccessDenied))
				Expect(p.Result.ID).To(BeEmpty())
			},
			Entry("no caps", meta.Caps(nil)),
			Entry("zone=write", meta.Caps{"zone": meta.CapWrite}),
			Entry("another type", meta.Caps{"users": meta.CapAll, "ratelimit": meta.CapAll}),
		)
		It("name themselves as radosgw does", func() {
			Expect((&op.GetRealm{}).Name()).To(Equal("get_realm"))
			Expect((&op.ListRealms{}).Name()).To(Equal("list_realms"))
			Expect((&op.GetPeriod{}).Name()).To(Equal("get_period"))
			Expect((&op.GetRateLimit{}).Name()).To(Equal("get_ratelimit_info"))
			Expect((&op.SetRateLimit{}).Name()).To(Equal("put_ratelimit_info"))
		})
	})

	Describe("ratelimit", func() {
		DescribeTable("refuses a request naming no scope it serves with InvalidArgument",
			func(ctx SpecContext, o *op.GetRateLimit) {
				Expect(runAs(ctx, o, rlAll)).To(MatchError(op.ErrInvalidArgument))
			},
			Entry("no scope", &op.GetRateLimit{UID: "t$alice", Bucket: "b"}),
			Entry("bucket scope without a bucket", &op.GetRateLimit{Scope: "bucket", UID: "t$alice"}),
			Entry("user scope without a uid", &op.GetRateLimit{Scope: "user", Bucket: "b"}),
			Entry("anon scope, which only the global config has", &op.GetRateLimit{Scope: "anon"}),
			Entry("an unknown scope", &op.GetRateLimit{Scope: "group", UID: "t$alice"}),
		)
		It("reads the global config whatever the scope, the zero config when there is none", func(ctx SpecContext) {
			o := &op.GetRateLimit{Scope: "bucket", Bucket: "b", Tenant: "t", Global: true}
			Expect(runAs(ctx, o, rlAll)).To(Succeed())
			Expect(o.Period).To(BeZero())
		})
		It("reads the zero limit for a user or bucket with no ratelimit attr", func(ctx SpecContext) {
			o := &op.GetRateLimit{Scope: "user", UID: "t$alice"}
			Expect(runAs(ctx, o, rlAll)).To(Succeed())
			Expect(o.Result).To(BeZero())
		})
		It("sets then reads a user's limits, keeping the limits it is not given", func(ctx SpecContext) {
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "user", UID: "t$alice", MaxReadOps: ptr(5), MaxWriteBytes: ptr(7)}, rlAll)).To(Succeed())
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "user", UID: "t$alice", MaxReadOps: ptr(-1), MaxWriteOps: ptr(0), Enabled: &yes}, rlAll)).To(Succeed())
			want := meta.RateLimitInfo{MaxReadOps: 5, MaxWriteBytes: 7, Enabled: true}
			Expect(attrOf(ctx, true)).To(Equal(encoded(want)), "version 1, as Squid and Tentacle write it")
			o := &op.GetRateLimit{Scope: "user", UID: "t$alice"}
			Expect(runAs(ctx, o, meta.Caps{"ratelimit": meta.CapRead})).To(Succeed())
			Expect(o.Result).To(Equal(want))
			Expect(attrOf(ctx, false)).To(BeNil(), "the bucket is untouched")
		})
		It("sets then reads a bucket's limits", func(ctx SpecContext) {
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "bucket", Bucket: "b", Tenant: "t", MaxReadOps: ptr(1), MaxWriteOps: ptr(2), MaxReadBytes: ptr(3), MaxWriteBytes: ptr(4), Enabled: &yes}, rlAll)).To(Succeed())
			want := meta.RateLimitInfo{MaxReadOps: 1, MaxWriteOps: 2, MaxReadBytes: 3, MaxWriteBytes: 4, Enabled: true}
			Expect(attrOf(ctx, false)).To(Equal(encoded(want)))
			o := &op.GetRateLimit{Scope: "bucket", Bucket: "b", Tenant: "t"}
			Expect(runAs(ctx, o, rlAll)).To(Succeed())
			Expect(o.Result).To(Equal(want))
			Expect(attrOf(ctx, true)).To(BeNil(), "the user is untouched")
		})
		DescribeTable("sets each global scope in the realm's period config alone",
			func(ctx SpecContext, scope string, pick func(meta.PeriodConfig) meta.RateLimitInfo) {
				Expect(runAs(ctx, &op.SetRateLimit{Scope: scope, Global: true, MaxReadOps: ptr(2)}, rlAll)).To(Succeed())
				cfg, err := store.GetPeriodConfig(ctx, "R")
				Expect(err).NotTo(HaveOccurred())
				Expect(pick(cfg)).To(Equal(meta.RateLimitInfo{MaxReadOps: 2}))
				Expect(cfg.BucketRateLimit.MaxReadOps+cfg.UserRateLimit.MaxReadOps+cfg.AnonRateLimit.MaxReadOps).To(BeEquivalentTo(2), "one scope set")
				o := &op.GetRateLimit{Global: true}
				Expect(runAs(ctx, o, rlAll)).To(Succeed())
				Expect(o.Period).To(Equal(cfg))
			},
			Entry("bucket", "bucket", func(c meta.PeriodConfig) meta.RateLimitInfo { return c.BucketRateLimit }),
			Entry("anon", "anon", func(c meta.PeriodConfig) meta.RateLimitInfo { return c.AnonRateLimit }),
			Entry("user", "user", func(c meta.PeriodConfig) meta.RateLimitInfo { return c.UserRateLimit }),
		)
		It("refuses an unknown global scope with InvalidArgument and writes nothing", func(ctx SpecContext) {
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "group", Global: true, MaxReadOps: ptr(2)}, rlAll)).To(MatchError(op.ErrInvalidArgument))
			_, err := store.GetPeriodConfig(ctx, "R")
			Expect(err).To(MatchError(op.ErrNotFound))
		})
		DescribeTable("refuses a set that configures nothing with InvalidArgument, writing nothing",
			func(ctx SpecContext, o *op.SetRateLimit) {
				Expect(runAs(ctx, o, rlAll)).To(MatchError(op.ErrInvalidArgument))
				Expect(attrOf(ctx, true)).To(BeNil())
			},
			Entry("no limit and no enabled", &op.SetRateLimit{Scope: "user", UID: "t$alice"}),
			Entry("negative limits alone, which radosgw ignores", &op.SetRateLimit{Scope: "user", UID: "t$alice", MaxReadOps: ptr(-1), MaxWriteOps: ptr(-5)}),
		)
		It("answers NoSuchKey, radosgw's -ENOENT, for a missing user or bucket", func(ctx SpecContext) {
			Expect(runAs(ctx, &op.GetRateLimit{Scope: "user", UID: "nosuch"}, rlAll)).To(MatchError(op.ErrNoSuchKey))
			Expect(runAs(ctx, &op.GetRateLimit{Scope: "bucket", Bucket: "nosuch"}, rlAll)).To(MatchError(op.ErrNoSuchKey))
			Expect(runAs(ctx, &op.GetRateLimit{Scope: "bucket", Bucket: "b"}, rlAll)).To(MatchError(op.ErrNoSuchKey), "the bucket is t's, and tenant names no tenant")
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "user", UID: "alice", Enabled: &yes}, rlAll)).To(MatchError(op.ErrNoSuchKey))
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "bucket", Bucket: "nosuch", Enabled: &yes}, rlAll)).To(MatchError(op.ErrNoSuchKey))
		})
		It("answers UnknownError, radosgw's -EIO, for a ratelimit attr that does not decode", func(ctx SpecContext) {
			rec, err := store.GetUser(ctx, meta.UserID{Tenant: "t", ID: "alice"})
			Expect(err).NotTo(HaveOccurred())
			rec.Attrs = map[string][]byte{meta.AttrRateLimit: {1}}
			Expect(store.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
			Expect(runAs(ctx, &op.GetRateLimit{Scope: "user", UID: "t$alice"}, rlAll)).To(MatchError(op.ErrUnknown))
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "user", UID: "t$alice", Enabled: &yes}, rlAll)).To(MatchError(op.ErrUnknown))
			Expect(attrOf(ctx, true)).To(Equal([]byte{1}), "nothing written")
		})
		It("writes the user as every user write does: its account entry restored, its groups kept, the email rule applied", func(ctx SpecContext) {
			const acct = "RGW00000000000000001"
			store.AddAccount(meta.AccountInfo{ID: acct, Name: "acme"})
			info := meta.NewUserInfo()
			info.UserID, info.DisplayName, info.AccountID = meta.UserID{ID: "member"}, "Member", acct
			info.GroupIDs = []string{"g1"}
			store.AddUser(info)
			Expect(store.AddAccountUser(ctx, acct, info)).To(Succeed())
			Expect(store.RemoveAccountUser(ctx, acct, "Member")).To(Succeed(), "an entry a stopped write left missing")
			env.Accounts = store
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_user_unique_email": "false"})
			var writes []op.PutUserOptions
			env.Users = optsUsers{UserStore: store, opts: &writes}
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "user", UID: "member", Enabled: &yes}, rlAll)).To(Succeed())
			ids, _, err := store.ListAccountUsers(ctx, acct, "", 1000)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(ConsistOf("member"), "store_user with no old_info links the account again")
			rec, err := store.GetUser(ctx, meta.UserID{ID: "member"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.AccountID).To(Equal(acct))
			Expect(rec.Info.GroupIDs).To(Equal([]string{"g1"}))
			Expect(rec.Attrs).To(HaveKey(meta.AttrRateLimit))
			Expect(writes).To(HaveLen(1))
			Expect(writes[0].SharedEmail).To(BeTrue(), "rgw_user_unique_email is off")
			Expect(writes[0].IfVersion).NotTo(BeNil())
		})
		It("loses a race with another user write as radosgw's versioned write does, keeping that write", func(ctx SpecContext) {
			raced := false
			env.Users = racingUsers{UserStore: store, race: func(ctx context.Context) {
				if raced {
					return
				}
				raced = true
				rec, err := store.GetUser(ctx, meta.UserID{Tenant: "t", ID: "alice"})
				Expect(err).NotTo(HaveOccurred())
				rec.Info.DisplayName = "raced"
				Expect(store.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &rec.Version})).To(Succeed())
			}}
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "user", UID: "t$alice", Enabled: &yes}, rlAll)).To(MatchError(op.ErrConcurrentModification))
			rec, err := store.GetUser(ctx, meta.UserID{Tenant: "t", ID: "alice"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.DisplayName).To(Equal("raced"))
			Expect(rec.Attrs).NotTo(HaveKey(meta.AttrRateLimit))
		})
		It("loses a race with another bucket write as radosgw's versioned write does, keeping that write", func(ctx SpecContext) {
			raced := false
			env.Buckets = racingBuckets{BucketStore: store, race: func(ctx context.Context) {
				if raced {
					return
				}
				raced = true
				rec, err := store.GetBucket(ctx, "t", "b")
				Expect(err).NotTo(HaveOccurred())
				Expect(store.PutBucketAttrs(ctx, rec, map[string][]byte{"user.rgw.raced": []byte("x")}, nil)).To(Succeed())
			}}
			Expect(runAs(ctx, &op.SetRateLimit{Scope: "bucket", Bucket: "b", Tenant: "t", Enabled: &yes}, rlAll)).To(MatchError(op.ErrConcurrentModification))
			rec, err := store.GetBucket(ctx, "t", "b")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Attrs).To(HaveKeyWithValue("user.rgw.raced", []byte("x")))
			Expect(rec.Attrs).NotTo(HaveKey(meta.AttrRateLimit))
		})
		DescribeTable("refuses a caller without radosgw's cap before reading or writing anything",
			func(ctx SpecContext, caps meta.Caps, set bool) {
				var o op.Op = &op.GetRateLimit{Scope: "user", UID: "t$alice"}
				if set {
					o = &op.SetRateLimit{Scope: "user", UID: "t$alice", Enabled: &yes}
				}
				reads := 0
				env.Users = racingUsers{UserStore: store, race: func(context.Context) { reads++ }}
				Expect(runAs(ctx, o, caps)).To(MatchError(op.ErrAccessDenied))
				Expect(reads).To(BeZero())
				Expect(attrOf(ctx, true)).To(BeNil())
			},
			Entry("get without caps", meta.Caps(nil), false),
			Entry("get with ratelimit=write", meta.Caps{"ratelimit": meta.CapWrite}, false),
			Entry("get with another type", meta.Caps{"users": meta.CapAll, "zone": meta.CapAll}, false),
			Entry("set without caps", meta.Caps(nil), true),
			Entry("set with ratelimit=read", meta.Caps{"ratelimit": meta.CapRead}, true),
			Entry("set with another type", meta.Caps{"users": meta.CapAll, "buckets": meta.CapAll}, true),
		)
	})
})
