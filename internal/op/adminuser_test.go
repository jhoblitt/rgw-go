package op_test

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var _ = Describe("admin user ops", func() {
	const (
		acct1 = "RGW00000000000000001" // tenant t1, "acme"
		acct2 = "RGW00000000000000002" // tenant "", "other"
		acct3 = "RGW00000000000000003" // tenant "", "third"
	)
	var (
		store *memstore.Store
		env   *op.Env
	)
	BeforeEach(func() {
		pools := meta.NewZonePlacementInfo()
		pools.StorageClasses = meta.ZoneStorageClasses{meta.StorageClassStandard: {}, "FOO": {}}
		store = memstore.New(memstore.Config{Params: meta.ZoneParams{
			Name: "z1", PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": pools},
		}})
		admin := meta.NewUserInfo()
		admin.UserID = meta.UserID{ID: "admin"}
		admin.DisplayName = "Admin"
		admin.Caps = meta.Caps{"users": meta.CapAll}
		store.AddUser(admin)
		store.AddAccount(meta.AccountInfo{ID: acct1, Tenant: "t1", Name: "acme"})
		store.AddAccount(meta.AccountInfo{ID: acct2, Name: "other"})
		store.AddAccount(meta.AccountInfo{ID: acct3, Name: "third"})
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
	})
	identity := func(caps meta.Caps) op.Identity {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps}
	}
	runAs := func(ctx context.Context, o op.Op, caps meta.Caps) error {
		return op.Run(ctx, o, &op.Request{Env: env, Identity: identity(caps)})
	}
	run := func(ctx context.Context, o op.Op) error { return runAs(ctx, o, meta.Caps{"users": meta.CapAll}) }
	newCreate := func(uid, email, accessKey, secret string) *op.CreateUser {
		o := op.NewCreateUser()
		o.UID = meta.UserID{ID: uid}
		o.DisplayName = "This is " + uid
		o.Email = email
		o.Key.AccessKey, o.Key.SecretKey = accessKey, secret
		o.Key.GenerateKey = true
		return o
	}
	create := func(ctx context.Context, uid, email, accessKey, secret string) *op.CreateUser {
		GinkgoHelper()
		o := newCreate(uid, email, accessKey, secret)
		Expect(run(ctx, o)).To(Succeed())
		return o
	}
	modify := func(uid string) *op.ModifyUser {
		m := op.NewModifyUser()
		m.UID = meta.UserID{ID: uid}
		return m
	}
	bucket := func(ctx context.Context, name string, owner meta.Owner) *op.BucketRecord {
		GinkgoHelper()
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: name, Owner: owner, Placement: meta.PlacementRule{Name: "default-placement"}})
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	user := func(id string) meta.Owner { return meta.UserOwner(meta.UserID{ID: id}) }

	Describe("create", func() {
		It("creates a user with radosgw's defaults and reports it", func(ctx SpecContext) {
			o := op.NewCreateUser()
			o.UID = meta.UserID{ID: "leseb"}
			o.DisplayName = "This is leseb"
			o.Email = "leseb@example.com"
			o.Caps = "users=read"
			o.UserOpMask = new(uint32(0x4))
			o.Key.GenerateKey = true
			o.PlacementTags = []string{"fast", "ssd"}
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Result.MaxBuckets).To(Equal(int32(1000)))
			Expect(o.Result.Caps).To(Equal(meta.Caps{"users": meta.CapRead}))
			Expect(o.Result.OpMask).To(Equal(uint32(0x4)))
			Expect(o.Result.AccessKeys).To(HaveLen(1))
			for id, k := range o.Result.AccessKeys {
				Expect(id).To(MatchRegexp(`^[A-Z0-9]{20}$`))
				Expect(k.ID).To(Equal(id))
				Expect(k.Secret).To(MatchRegexp(`^[A-Za-z0-9]{40}$`))
				Expect(k.Active).To(BeTrue())
				Expect(k.CreatedAt.IsZero()).To(BeFalse())
			}
			Expect(o.Result.Type).To(Equal(meta.IdentityRGW))
			Expect(o.Result.Path).To(Equal("/"))
			Expect(o.Result.CreateDate.IsZero()).To(BeFalse())
			Expect(o.Result.BucketQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: -1}))
			rec, err := store.GetUser(ctx, o.UID)
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.PlacementTags).To(Equal([]string{"fast", "ssd"}))
			Expect(rec.Info.Email).To(Equal("leseb@example.com"))
		})
		It("stores the key it is given, and generates only the part it is not", func(ctx SpecContext) {
			o := create(ctx, "a", "", "AKIAGIVEN", "given-secret")
			Expect(o.Result.AccessKeys).To(HaveKeyWithValue("AKIAGIVEN", HaveField("Secret", "given-secret")))
			o = create(ctx, "b", "", "AKIAHALF", "")
			Expect(o.Result.AccessKeys).To(HaveKeyWithValue("AKIAHALF", HaveField("Secret", MatchRegexp(`^[A-Za-z0-9]{40}$`))))
			o = newCreate("c", "", "", "")
			o.Key.GenerateKey = false
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Result.AccessKeys).To(BeEmpty(), "generate-key=false and no key named")
		})
		It("refuses duplicates the way radosgw names them", func(ctx SpecContext) {
			create(ctx, "leseb", "leseb@example.com", "AKIA1", "s1")
			Expect(run(ctx, newCreate("leseb", "x@y", "", ""))).To(MatchError(op.ErrUserAlreadyExists))
			Expect(run(ctx, newCreate("other", "Leseb@Example.com", "", ""))).To(MatchError(op.ErrEmailExists))
			Expect(run(ctx, newCreate("other2", "", "AKIA1", "s2"))).To(MatchError(op.ErrKeyExists))
			Expect(run(ctx, newCreate("", "", "", ""))).To(MatchError(op.ErrInvalidArgument), "an empty uid is the anonymous user")
			Expect(run(ctx, newCreate("anonymous", "", "", ""))).To(MatchError(op.ErrInvalidArgument))
			o := newCreate("nodisplay", "", "", "")
			o.DisplayName = ""
			Expect(run(ctx, o)).To(MatchError(op.ErrInvalidArgument))
			o = newCreate("bad", "", "", "")
			o.UID.Tenant = "te-nant"
			Expect(run(ctx, o)).To(MatchError(op.ErrInvalidTenantName))
			Expect(run(ctx, newCreate("RGW00000000000000009", "", "", ""))).To(MatchError(op.ErrInvalidArgument), "uid shaped like an account id")
			o = newCreate("u", "", "", "")
			o.UID.Tenant = "RGW00000000000000009"
			Expect(run(ctx, o)).To(MatchError(op.ErrInvalidArgument), "tenant shaped like an account id")
			_, err := store.GetUser(ctx, meta.UserID{ID: "other2"})
			Expect(err).To(MatchError(op.ErrNoSuchUser), "nothing written")
		})
		It("refuses a supplied access key another user holds and names no credential in the error", func(ctx SpecContext) {
			create(ctx, "leseb", "leseb@example.com", "AKIASECRETHOLDER", "s1")
			err := run(ctx, newCreate("thief", "", "AKIASECRETHOLDER", "mine"))
			Expect(err).To(MatchError(op.ErrKeyExists))
			Expect(err.Error()).NotTo(ContainSubstring("AKIASECRETHOLDER"))
			err = run(ctx, newCreate("thief", "leseb@example.com", "", ""))
			Expect(err).To(MatchError(op.ErrEmailExists))
			Expect(err.Error()).NotTo(ContainSubstring("leseb@example.com"))
			rec, err := store.GetUser(ctx, meta.UserID{ID: "leseb"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.AccessKeys["AKIASECRETHOLDER"].Secret).To(Equal("s1"), "the holder keeps its key")
		})
		It("requires a secret for an explicit key and rejects bad caps, writing nothing", func(ctx SpecContext) {
			o := newCreate("k", "", "AKIA2", "")
			o.Key.GenerateKey = false
			Expect(run(ctx, o)).To(MatchError(op.ErrInvalidSecretKey))
			o = newCreate("k", "", "", "")
			o.Key.GenerateKey = false
			o.Key.SecretKey = "only-a-secret"
			Expect(run(ctx, o)).To(MatchError(op.ErrInvalidAccessKeyID), "check_op's empty access key")
			o = newCreate("c", "", "", "")
			o.Caps = "kittens=read"
			Expect(run(ctx, o)).To(MatchError(op.ErrInvalidCapability))
			for _, id := range []string{"k", "c"} {
				_, err := store.GetUser(ctx, meta.UserID{ID: id})
				Expect(err).To(MatchError(op.ErrNoSuchUser), id)
			}
		})
		It("refuses the system flag from a non-system requester and an unknown placement", func(ctx SpecContext) {
			o := newCreate("s", "", "", "")
			o.System = new(true)
			Expect(run(ctx, o)).To(MatchError(op.ErrInvalidArgument))
			o = newCreate("p", "", "", "")
			o.DefaultPlacement = &meta.PlacementRule{Name: "nosuch"}
			Expect(run(ctx, o)).To(MatchError(op.ErrInvalidArgument))
			o = newCreate("p", "", "", "")
			o.DefaultPlacement = &meta.PlacementRule{Name: "default-placement", StorageClass: "FOO"}
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Result.DefaultPlacement).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "FOO"}))
		})
		It("joins an account with a matching tenant and indexes the user there", func(ctx SpecContext) {
			o := newCreate("root1", "", "", "")
			o.UID.Tenant = "t1"
			o.DisplayName = "Root"
			o.AccountID = acct1
			o.AccountRoot = new(true)
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Result.Type).To(Equal(meta.IdentityRoot))
			Expect(o.Result.AccountID).To(Equal(acct1))
			ids, _, err := store.ListAccountUsers(ctx, acct1, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(ConsistOf("root1"))

			bad := newCreate("root2", "", "", "")
			bad.DisplayName = "root2"
			bad.AccountID = acct1
			Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument), "tenant \"\" is not the account's t1")
			bad = newCreate("root3", "", "", "")
			bad.AccountRoot = new(true)
			Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument), "account-root needs an account")
			bad = newCreate("root4", "", "", "")
			bad.UID.Tenant = "t1"
			bad.AccountID = acct1
			bad.DisplayName = "has space"
			Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument), "IAM user name")
			bad = newCreate("root5", "", "", "")
			bad.DisplayName = "r5"
			bad.AccountID = "RGW1"
			Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument), "not an account id")
			bad = newCreate("root6", "", "", "")
			bad.DisplayName = "r6"
			bad.AccountID = "RGW00000000000000099"
			Expect(run(ctx, bad)).To(MatchError(op.ErrNoSuchKey), "radosgw's ENOENT from create")
		})
		It("refuses a display name another user of the account holds, case-insensitively", func(ctx SpecContext) {
			o := newCreate("u1", "", "", "")
			o.DisplayName, o.AccountID = "Alpha", acct2
			Expect(run(ctx, o)).To(Succeed())
			o = newCreate("u2", "", "", "")
			o.DisplayName, o.AccountID = "alpha", acct2
			Expect(run(ctx, o)).To(MatchError(op.ErrUserAlreadyExists), "create maps PutOperation's EEXIST")
			_, err := store.GetUser(ctx, meta.UserID{ID: "u2"})
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			o = newCreate("u3", "", "", "")
			o.DisplayName, o.AccountID = "alpha", acct3
			Expect(run(ctx, o)).To(Succeed(), "another account's users are no conflict")
		})
	})

	Describe("modify", func() {
		It("modifies only what the request names", func(ctx SpecContext) {
			create(ctx, "leseb", "leseb@example.com", "", "")
			m := modify("leseb")
			m.Email = new("leseb@leseb.com")
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.Email).To(Equal("leseb@leseb.com"))
			Expect(m.Result.DisplayName).To(Equal("This is leseb"), "untouched")
			Expect(m.Result.AccessKeys).To(HaveLen(1), "no key op")
			m = modify("leseb")
			m.MaxBuckets = new(int32(-1))
			m.UserOpMask = new(op.OpTypeRead)
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.MaxBuckets).To(Equal(int32(-1)))
			Expect(m.Result.Email).To(Equal("leseb@leseb.com"), "nil Email leaves it")
			rec, err := store.GetUser(ctx, meta.UserID{ID: "leseb"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.OpMask).To(Equal(op.OpTypeRead))
			m = modify("leseb")
			m.Email = new("")
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.Email).To(BeEmpty(), "an empty email clears it")
			_, err = store.GetUserByEmail(ctx, "leseb@leseb.com")
			Expect(err).To(MatchError(op.ErrNoSuchUser), "and its index")
		})
		It("refuses a missing user, another user's email, and a user found through someone else's credentials", func(ctx SpecContext) {
			create(ctx, "leseb", "leseb@example.com", "AKIA9", "s9")
			create(ctx, "other", "other@example.com", "", "")
			Expect(run(ctx, modify("nosuch"))).To(MatchError(op.ErrNoSuchUser))
			m := modify("leseb")
			m.Email = new("other@example.com")
			Expect(run(ctx, m)).To(MatchError(op.ErrEmailExists))
			m = modify("nosuch")
			m.Email = new("other@example.com")
			Expect(run(ctx, m)).To(MatchError(op.ErrInvalidArgument), "check_op's user id mismatch")
			m = modify("")
			m.Key.AccessKey = "AKIA9"
			Expect(run(ctx, m)).To(MatchError(op.ErrInvalidArgument), "no uid is the anonymous user")
			m = modify("leseb")
			m.System = new(true)
			Expect(run(ctx, m)).To(MatchError(op.ErrInvalidArgument), "a non-system requester")
		})
		It("adds a key the request names and refuses one another user holds", func(ctx SpecContext) {
			create(ctx, "leseb", "", "AKIAMINE", "s1")
			create(ctx, "other", "", "AKIATHEIRS", "s2")
			m := modify("leseb")
			m.Key.AccessKey, m.Key.SecretKey = "AKIANEW", "s3"
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.AccessKeys).To(HaveKey("AKIANEW"))
			Expect(m.Result.AccessKeys).To(HaveKey("AKIAMINE"))
			m = modify("leseb")
			m.Key.AccessKey, m.Key.SecretKey = "AKIATHEIRS", "stolen"
			err := run(ctx, m)
			Expect(err).To(MatchError(op.ErrKeyExists))
			Expect(err.Error()).NotTo(ContainSubstring("AKIATHEIRS"))
			rec, err := store.GetUser(ctx, meta.UserID{ID: "other"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.AccessKeys["AKIATHEIRS"].Secret).To(Equal("s2"))
			m = modify("leseb")
			m.Key.AccessKey, m.Key.SecretKey = "AKIAMINE", "rotated"
			Expect(run(ctx, m)).To(Succeed(), "a key the user holds is modified")
			Expect(m.Result.AccessKeys["AKIAMINE"].Secret).To(Equal("rotated"))
		})
		It("suspends the user's buckets with the user", func(ctx SpecContext) {
			create(ctx, "leseb", "", "", "")
			bucket(ctx, "b1", user("leseb"))
			bucket(ctx, "b2", user("admin"))
			m := modify("leseb")
			m.Suspended = new(true)
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.Suspended).To(Equal(uint8(1)))
			rec, err := store.GetBucket(ctx, "", "b1")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.Flags & meta.BucketSuspended).NotTo(BeZero())
			rec, err = store.GetBucket(ctx, "", "b2")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.Flags&meta.BucketSuspended).To(BeZero(), "another user's bucket")
			m = modify("leseb")
			m.Suspended = new(false)
			Expect(run(ctx, m)).To(Succeed())
			rec, err = store.GetBucket(ctx, "", "b1")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.Flags & meta.BucketSuspended).To(BeZero())
		})
		It("adopts buckets when joining an account and refuses to leave one", func(ctx SpecContext) {
			create(ctx, "u", "", "", "")
			bucket(ctx, "ub", user("u"))
			m := modify("u")
			m.DisplayName = "u"
			m.AccountID = acct2
			Expect(run(ctx, m)).To(Succeed())
			rec, err := store.GetBucket(ctx, "", "ub")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.Owner).To(Equal(meta.AccountOwner(acct2)))
			ids, _, err := store.ListAccountUsers(ctx, acct2, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(ConsistOf("u"))
			m = modify("u")
			m.AccountID = acct3
			Expect(run(ctx, m)).To(MatchError(op.ErrInvalidArgument), "users cannot be moved out of their account")
			m = modify("u")
			m.AccountID = acct2
			Expect(run(ctx, m)).To(Succeed(), "naming its own account is no move")
		})
		It("answers a missing account as modify maps radosgw's ENOENT, and keeps the IAM name rule", func(ctx SpecContext) {
			create(ctx, "u", "", "", "")
			m := modify("u")
			m.AccountID = "RGW00000000000000099"
			Expect(run(ctx, m)).To(MatchError(op.ErrNoSuchUser))
			m = modify("u")
			m.AccountID = acct2
			Expect(run(ctx, m)).To(MatchError(op.ErrInvalidArgument), "\"This is u\" is no IAM user name")
			m = modify("u")
			m.AccountRoot = new(true)
			Expect(run(ctx, m)).To(MatchError(op.ErrInvalidArgument), "account-root needs an account")
		})
		It("moves the account index entry when an account user is renamed", func(ctx SpecContext) {
			o := newCreate("u1", "", "", "")
			o.DisplayName, o.AccountID = "alpha", acct2
			Expect(run(ctx, o)).To(Succeed())
			o = newCreate("u2", "", "", "")
			o.DisplayName, o.AccountID = "beta", acct2
			Expect(run(ctx, o)).To(Succeed())
			m := modify("u1")
			m.DisplayName = "Beta"
			Expect(run(ctx, m)).To(MatchError(op.ErrBucketAlreadyExists), "modify answers PutOperation's EEXIST from the table")
			m = modify("u1")
			m.DisplayName = "gamma"
			Expect(run(ctx, m)).To(Succeed())
			o = newCreate("u3", "", "", "")
			o.DisplayName, o.AccountID = "alpha", acct2
			Expect(run(ctx, o)).To(Succeed(), "the old name is free")
			ids, _, err := store.ListAccountUsers(ctx, acct2, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(ConsistOf("u1", "u2", "u3"))
		})
	})

	Describe("info", func() {
		It("answers an unknown access key without a uid as keys.init refuses the anonymous uid", func(ctx SpecContext) {
			g := op.NewGetUserInfo()
			g.AccessKey = "AKIANOSUCH"
			Expect(run(ctx, g)).To(MatchError(op.ErrInvalidArgument))
			g = op.NewGetUserInfo()
			g.UID = meta.UserID{ID: "nosuch"}
			g.AccessKey = "AKIANOSUCH"
			Expect(run(ctx, g)).To(MatchError(op.ErrNoSuchUser), "a uid that is not the anonymous user")
		})
		It("reads a user by uid or access key with stats", func(ctx SpecContext) {
			create(ctx, "leseb", "", "AKIA3", "s3")
			b := bucket(ctx, "b", user("leseb"))
			_, err := store.PutObject(ctx, b, meta.ObjKey{Name: "o"}, strings.NewReader("hello"), op.PutParams{Size: 5})
			Expect(err).NotTo(HaveOccurred())
			g := op.NewGetUserInfo()
			g.AccessKey = "AKIA3"
			g.FetchStats = true
			Expect(run(ctx, g)).To(Succeed())
			Expect(g.Result.UserID.ID).To(Equal("leseb"))
			Expect(g.Stats).To(Equal(&op.Stats{Size: 5, SizeRounded: 4096, NumObjects: 1}))
			Expect(g.DumpKeys).To(BeTrue(), "users=read")
			g = op.NewGetUserInfo()
			g.UID = meta.UserID{ID: "leseb"}
			Expect(run(ctx, g)).To(Succeed())
			Expect(g.Stats).To(BeNil(), "stats only when asked")
			g = op.NewGetUserInfo()
			Expect(run(ctx, g)).To(MatchError(op.ErrInvalidArgument), "neither uid nor access-key")
			g = op.NewGetUserInfo()
			g.UID = meta.UserID{ID: "nosuch"}
			Expect(run(ctx, g)).To(MatchError(op.ErrNoSuchUser))
			g = op.NewGetUserInfo()
			g.UID = meta.UserID{ID: "nosuch"}
			g.AccessKey = "AKIA3"
			Expect(run(ctx, g)).To(Succeed(), "RGWUser::init falls back to the access key")
			Expect(g.Result.UserID.ID).To(Equal("leseb"))
		})
		It("accepts user-info-without-keys=read for info only, without the keys", func(ctx SpecContext) {
			create(ctx, "leseb", "", "", "")
			g := op.NewGetUserInfo()
			g.UID = meta.UserID{ID: "leseb"}
			Expect(runAs(ctx, g, meta.Caps{"user-info-without-keys": meta.CapRead})).To(Succeed())
			Expect(g.DumpKeys).To(BeFalse())
			Expect(runAs(ctx, newCreate("x", "", "", ""), meta.Caps{"user-info-without-keys": meta.CapRead})).To(MatchError(op.ErrAccessDenied))
			id := identity(meta.Caps{"user-info-without-keys": meta.CapRead})
			id.Admin = true
			g = op.NewGetUserInfo()
			g.UID = meta.UserID{ID: "leseb"}
			Expect(op.Run(ctx, g, &op.Request{Env: env, Identity: id})).To(Succeed())
			Expect(g.DumpKeys).To(BeTrue(), "an admin sees keys")
		})
	})

	Describe("remove", func() {
		It("removes a user, refusing when buckets remain unless purging", func(ctx SpecContext) {
			create(ctx, "leseb", "", "", "")
			b := bucket(ctx, "b1", user("leseb"))
			_, err := store.PutObject(ctx, b, meta.ObjKey{Name: "o"}, strings.NewReader("x"), op.PutParams{Size: 1})
			Expect(err).NotTo(HaveOccurred())
			_, err = store.CreateUpload(ctx, b, meta.ObjKey{Name: "mp"}, op.UploadParams{})
			Expect(err).NotTo(HaveOccurred())
			d := op.NewRemoveUser()
			d.UID = meta.UserID{ID: "leseb"}
			Expect(run(ctx, d)).To(MatchError(op.ErrBucketAlreadyExists))
			_, err = store.GetUser(ctx, d.UID)
			Expect(err).NotTo(HaveOccurred(), "kept")
			d.PurgeData = true
			Expect(run(ctx, d)).To(Succeed())
			_, err = store.GetUser(ctx, d.UID)
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			_, err = store.GetBucket(ctx, "", "b1")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchUser))
			Expect(run(ctx, op.NewRemoveUser())).To(MatchError(op.ErrInvalidArgument), "no uid")
		})
		It("purges only the removed user's buckets", func(ctx SpecContext) {
			create(ctx, "leseb", "", "", "")
			create(ctx, "other", "", "", "")
			bucket(ctx, "mine", user("leseb"))
			theirs := bucket(ctx, "theirs", user("other"))
			_, err := store.PutObject(ctx, theirs, meta.ObjKey{Name: "keep"}, strings.NewReader("x"), op.PutParams{Size: 1})
			Expect(err).NotTo(HaveOccurred())
			d := op.NewRemoveUser()
			d.UID = meta.UserID{ID: "leseb"}
			d.PurgeData = true
			Expect(run(ctx, d)).To(Succeed())
			_, err = store.GetBucket(ctx, "", "mine")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			rec, err := store.GetBucket(ctx, "", "theirs")
			Expect(err).NotTo(HaveOccurred())
			st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: "keep"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeTrue())
		})
		It("leaves a bucket its list names when another owner holds it", func(ctx SpecContext) {
			users := new(opfakes.FakeUserStore)
			leseb := meta.NewUserInfo()
			leseb.UserID = meta.UserID{ID: "leseb"}
			users.GetUserReturns(&op.UserRecord{Info: leseb}, nil)
			users.ListUserBucketsReturnsOnCall(0, []meta.BucketEnt{{Bucket: meta.BucketID{Name: "theirs"}}}, "theirs", false, nil)
			env.Users = users
			bucket(ctx, "theirs", user("other"))
			d := op.NewRemoveUser()
			d.UID = meta.UserID{ID: "leseb"}
			d.PurgeData = true
			Expect(run(ctx, d)).To(Succeed())
			_, err := store.GetBucket(ctx, "", "theirs")
			Expect(err).NotTo(HaveOccurred())
			Expect(users.RemoveUserCallCount()).To(Equal(1))
		})
		It("drops an account user's index entry, the root user's included", func(ctx SpecContext) {
			for _, u := range []struct {
				id   string
				root bool
			}{{"member", false}, {"root", true}} {
				o := newCreate(u.id, "", "", "")
				o.DisplayName, o.AccountID = u.id, acct2
				if u.root {
					o.AccountRoot = new(true)
				}
				Expect(run(ctx, o)).To(Succeed())
			}
			for _, id := range []string{"member", "root"} {
				d := op.NewRemoveUser()
				d.UID = meta.UserID{ID: id}
				Expect(run(ctx, d)).To(Succeed(), id)
			}
			ids, _, err := store.ListAccountUsers(ctx, acct2, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(BeEmpty())
			o := newCreate("again", "", "", "")
			o.DisplayName, o.AccountID = "root", acct2
			Expect(run(ctx, o)).To(Succeed(), "the root's name is free again")
		})
	})

	Describe("list", func() {
		It("lists users through the metadata section with a 1000 cap", func(ctx SpecContext) {
			create(ctx, "a", "", "", "")
			create(ctx, "b", "", "", "")
			l := op.NewListUsers()
			l.MaxEntries = 5000
			Expect(run(ctx, l)).To(Succeed())
			Expect(l.Keys).To(Equal([]string{"a", "admin", "b"}))
			Expect(l.Truncated).To(BeFalse())
			Expect(l.Count).To(Equal(uint64(3)))
			Expect(l.NextMarker).To(BeEmpty())
			l = op.NewListUsers()
			l.MaxEntries = 1
			Expect(run(ctx, l)).To(Succeed())
			Expect(l.Keys).To(Equal([]string{"a"}))
			Expect(l.Truncated).To(BeTrue())
			Expect(l.NextMarker).To(Equal("a"))
			l2 := op.NewListUsers()
			l2.Marker = l.NextMarker
			Expect(run(ctx, l2)).To(Succeed())
			Expect(l2.Keys).To(Equal([]string{"admin", "b"}))
			Expect(runAs(ctx, op.NewListUsers(), meta.Caps{"users": meta.CapWrite})).To(MatchError(op.ErrAccessDenied))
		})
	})

	Describe("partial failures", func() {
		var flaky *flakyUsers
		BeforeEach(func() {
			flaky = &flakyUsers{Store: store}
			env.Users = flaky
		})
		It("stores a new user non-exclusively, as RGWUser::update does", func(ctx SpecContext) {
			create(ctx, "leseb", "", "", "")
			Expect(flaky.puts).To(Equal([]op.PutUserOptions{{}}))
		})
		It("creates over an email index that names no user", func(ctx SpecContext) {
			store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000007", Name: "holder", Email: "stale@example.com"})
			create(ctx, "leseb", "stale@example.com", "", "")
			rec, err := store.GetUserByEmail(ctx, "stale@example.com")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.UserID.ID).To(Equal("leseb"), "the index is overwritten, as radosgw overwrites it")
		})
		It("creates a second user with an email another holds when rgw_user_unique_email is off", func(ctx SpecContext) {
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_user_unique_email": "false"})
			create(ctx, "first", "shared@example.com", "", "")
			create(ctx, "second", "shared@example.com", "", "")
			rec, err := store.GetUserByEmail(ctx, "shared@example.com")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.UserID.ID).To(Equal("second"), "the last write holds the index, as in radosgw")
			_, err = store.GetUser(ctx, meta.UserID{ID: "first"})
			Expect(err).NotTo(HaveOccurred())
		})
		It("leaves a create that failed after writing as radosgw does, naming no credential", func(ctx SpecContext) {
			flaky.putErr = fmt.Errorf("writing users.email/leseb@example.com and users.keys/AKIAPARTIAL: %w", op.ErrInternalError)
			err := run(ctx, newCreate("leseb", "leseb@example.com", "AKIAPARTIAL", "s"))
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("leseb@example.com"))
			Expect(err.Error()).NotTo(ContainSubstring("AKIAPARTIAL"))
			Expect(flaky.removed).To(BeEmpty(), "nothing is undone")
			_, err = store.GetUser(ctx, meta.UserID{ID: "leseb"})
			Expect(err).NotTo(HaveOccurred(), "the uid object stays")
			flaky.putErr = nil
			Expect(run(ctx, newCreate("leseb", "", "", ""))).To(MatchError(op.ErrUserAlreadyExists), "a retry finds it, as radosgw's does")
		})
		It("fails a lookup by access key on a store error without naming the key", func(ctx SpecContext) {
			flaky.keyReadErr = fmt.Errorf("reading users.keys/AKIALOOKUP: %w", op.ErrInternalError)
			err := run(ctx, newCreate("leseb", "", "AKIALOOKUP", "s"))
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("AKIALOOKUP"))
			g := op.NewGetUserInfo()
			g.AccessKey = "AKIALOOKUP"
			err = run(ctx, g)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("AKIALOOKUP"))
		})
		It("completes a removal on retry after the user's removal failed, its index entry still in place", func(ctx SpecContext) {
			o := newCreate("member", "", "", "")
			o.DisplayName, o.AccountID = "member", acct2
			Expect(run(ctx, o)).To(Succeed())
			flaky.removeErr = op.ErrConcurrentModification
			d := op.NewRemoveUser()
			d.UID = meta.UserID{ID: "member"}
			Expect(run(ctx, d)).To(MatchError(op.ErrConcurrentModification))
			ids, _, err := store.ListAccountUsers(ctx, acct2, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(ConsistOf("member"), "the user goes before its entry, so the entry still holds off the account's removal")
			Expect(run(ctx, d)).To(Succeed(), "the retry")
			_, err = store.GetUser(ctx, d.UID)
			Expect(err).To(MatchError(op.ErrNoSuchUser))
		})
		It("answers NoSuchUser for a bucket gone between its load and its removal", func(ctx SpecContext) {
			create(ctx, "leseb", "", "", "")
			bucket(ctx, "b1", user("leseb"))
			env.Buckets = deleteGone{Store: store}
			d := op.NewRemoveUser()
			d.UID = meta.UserID{ID: "leseb"}
			d.PurgeData = true
			Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchUser))
			_, err := store.GetUser(ctx, d.UID)
			Expect(err).NotTo(HaveOccurred(), "nothing of the user removed")
		})
		It("answers NoSuchUser for a listed bucket that is gone, and removes the user on retry", func(ctx SpecContext) {
			create(ctx, "leseb", "", "", "")
			bucket(ctx, "b1", user("leseb"))
			env.Buckets = goneBucket{Store: store, name: "b1"}
			d := op.NewRemoveUser()
			d.UID = meta.UserID{ID: "leseb"}
			d.PurgeData = true
			Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchUser))
			_, err := store.GetUser(ctx, d.UID)
			Expect(err).NotTo(HaveOccurred(), "nothing of the user removed")
			env.Buckets = store
			Expect(run(ctx, d)).To(Succeed())
		})
	})

	Describe("security", func() {
		It("checks the caps before any user lookup, so a refused caller learns nothing", func(ctx SpecContext) {
			users := new(opfakes.FakeUserStore)
			env.Users = users
			noUsersCap := meta.Caps{"buckets": meta.CapAll, "user-info-without-keys": meta.CapWrite}
			info := op.NewGetUserInfo()
			info.UID = meta.UserID{ID: "admin"}
			rm := op.NewRemoveUser()
			rm.UID = meta.UserID{ID: "admin"}
			md := modify("admin")
			md.Email = new("x@example.com")
			for _, o := range []op.Op{newCreate("admin", "a@b", "AKIA", "s"), md, info, rm, op.NewListUsers()} {
				Expect(runAs(ctx, o, noUsersCap)).To(MatchError(op.ErrAccessDenied), o.Name())
				Expect(runAs(ctx, o, meta.Caps{"users": meta.CapAll &^ needs(o)})).To(MatchError(op.ErrAccessDenied), o.Name()+" with the other users bit")
			}
			Expect(users.Invocations()).To(BeEmpty())
		})
	})
})

// flakyUsers is the memstore's user store with failures put in: putErr fails
// PutUser after the user is stored; keyReadErr fails every
// GetUserByAccessKey; removeErr fails one RemoveUser. puts and removed
// record the calls.
type flakyUsers struct {
	*memstore.Store
	putErr     error
	keyReadErr error
	removeErr  error
	puts       []op.PutUserOptions
	removed    []op.UserRecord
}

func (f *flakyUsers) GetUserByAccessKey(ctx context.Context, key string) (*op.UserRecord, error) {
	if f.keyReadErr != nil {
		return nil, f.keyReadErr
	}
	return f.Store.GetUserByAccessKey(ctx, key)
}

func (f *flakyUsers) PutUser(ctx context.Context, rec *op.UserRecord, opts op.PutUserOptions) error {
	f.puts = append(f.puts, opts)
	if err := f.Store.PutUser(ctx, rec, opts); err != nil {
		return err
	}
	return f.putErr
}

func (f *flakyUsers) RemoveUser(ctx context.Context, rec *op.UserRecord) error {
	f.removed = append(f.removed, *rec)
	if err := f.removeErr; err != nil {
		f.removeErr = nil
		return err
	}
	return f.Store.RemoveUser(ctx, rec)
}

// goneBucket is the memstore's bucket store with one bucket read as gone.
type goneBucket struct {
	*memstore.Store
	name string
}

func (g goneBucket) GetBucket(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
	if name == g.name {
		return nil, op.ErrNoSuchBucket
	}
	return g.Store.GetBucket(ctx, tenant, name)
}

// deleteGone is the memstore's bucket store whose DeleteBucket finds every
// bucket gone, as a concurrent delete leaves it.
type deleteGone struct{ *memstore.Store }

func (deleteGone) DeleteBucket(context.Context, *op.BucketRecord) error { return op.ErrNoSuchBucket }

// needs is the users bit an op's check_caps asks for.
func needs(o op.Op) uint32 {
	switch o.Name() {
	case "get_user_info", "list_user":
		return meta.CapRead
	}
	return meta.CapWrite
}
