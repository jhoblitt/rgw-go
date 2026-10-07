package op_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var _ = Describe("admin user key, subuser, caps and quota ops", func() {
	var (
		store *memstore.Store
		env   *op.Env
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{Params: meta.ZoneParams{Name: "z1"}})
		admin := meta.NewUserInfo()
		admin.UserID = meta.UserID{ID: "admin"}
		admin.DisplayName = "Admin"
		admin.Caps = meta.Caps{"users": meta.CapAll}
		store.AddUser(admin)
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
	})
	request := func(caps meta.Caps) *op.Request {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return &op.Request{Env: env, Header: http.Header{}, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps,
		}}
	}
	runAs := func(ctx context.Context, o op.Op, caps meta.Caps) error { return op.Run(ctx, o, request(caps)) }
	run := func(ctx context.Context, o op.Op) error { return runAs(ctx, o, meta.Caps{"users": meta.CapAll}) }
	// runBody runs o with a request body of declared length n, -1 for a
	// chunked one.
	runBody := func(ctx context.Context, o op.Op, body string, n int64) error {
		r := request(meta.Caps{"users": meta.CapAll})
		r.Body, r.ContentLength = strings.NewReader(body), n
		if n >= 0 {
			r.Header.Set("Content-Length", strconv.FormatInt(n, 10))
		}
		return op.Run(ctx, o, r)
	}
	// create stores a user with the given S3 key, none when accessKey is "".
	create := func(uid, accessKey, secret string) {
		info := meta.NewUserInfo()
		info.UserID = meta.ParseUserID(uid)
		info.DisplayName = "This is " + uid
		if accessKey != "" {
			info.AccessKeys = map[string]meta.AccessKey{accessKey: {ID: accessKey, Secret: secret, Active: true}}
		}
		store.AddUser(info)
	}
	stored := func(ctx context.Context, uid string) meta.UserInfo {
		GinkgoHelper()
		rec, err := store.GetUser(ctx, meta.ParseUserID(uid))
		Expect(err).NotTo(HaveOccurred())
		return rec.Info
	}
	newKey := func(uid string) *op.CreateKey {
		k := op.NewCreateKey()
		k.UID = meta.ParseUserID(uid)
		return k
	}
	newSubuser := func(uid, name string) *op.CreateSubuser {
		c := op.NewCreateSubuser()
		c.UID = meta.ParseUserID(uid)
		c.Subuser = name
		return c
	}

	Describe("keys", func() {
		It("generates a key pair, keeps a given access key, and refuses duplicates", func(ctx SpecContext) {
			create("admin2", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
			k := newKey("admin2")
			k.Key.GenerateKey = true
			Expect(run(ctx, k)).To(Succeed())
			Expect(k.Type).To(Equal(op.KeyTypeS3))
			Expect(k.Result.AccessKeys).To(HaveLen(2))
			k = newKey("admin2")
			k.Key.AccessKey = "HDNEZQXZAA6NIWOBOL0U"
			k.Key.GenerateKey = true
			Expect(run(ctx, k)).To(Succeed())
			Expect(k.Result.AccessKeys).To(HaveLen(3))
			Expect(k.Result.AccessKeys["HDNEZQXZAA6NIWOBOL0U"].Secret).To(MatchRegexp(`^[A-Za-z0-9]{40}$`))
			k = newKey("admin2")
			k.Key.AccessKey = "HDNEZQXZAA6NIWOBOL0U"
			k.Key.SecretKey = "new"
			Expect(run(ctx, k)).To(Succeed(), "an existing key is modified, not refused")
			Expect(k.Result.AccessKeys["HDNEZQXZAA6NIWOBOL0U"].Secret).To(Equal("new"))
			Expect(stored(ctx, "admin2").AccessKeys["HDNEZQXZAA6NIWOBOL0U"].Secret).To(Equal("new"))
			create("other", "OTHERKEY0000000000AK", "s")
			flaky := &flakyUsers{Store: store}
			env.Users = flaky
			k = newKey("admin2")
			k.Key.AccessKey = "OTHERKEY0000000000AK"
			k.Key.SecretKey = "s"
			err := run(ctx, k)
			Expect(err).To(MatchError(op.ErrKeyExists))
			Expect(err.Error()).NotTo(ContainSubstring("OTHERKEY0000000000AK"))
			Expect(flaky.puts).To(BeEmpty(), "generate_key refuses it before any write")
			env.Users = store
			Expect(stored(ctx, "other").AccessKeys["OTHERKEY0000000000AK"].Secret).To(Equal("s"))
			k = newKey("admin2")
			k.Key.AccessKey, k.Key.GenerateKey = "X", false
			Expect(run(ctx, k)).To(MatchError(op.ErrInvalidSecretKey))
			k = newKey("admin2")
			k.Key.SecretKey, k.Key.GenerateKey = "only-a-secret", false
			Expect(run(ctx, k)).To(MatchError(op.ErrInvalidAccessKeyID), "check_op's empty access key")
			Expect(stored(ctx, "admin2").AccessKeys).To(HaveLen(3), "nothing written by the refusals")
		})
		It("refuses an access key id another user holds inactive, on every path that names one", func(ctx SpecContext) {
			holder := meta.NewUserInfo()
			holder.UserID = meta.UserID{ID: "holder"}
			holder.AccessKeys = map[string]meta.AccessKey{"AKDORMANT": {ID: "AKDORMANT", Secret: "s", Active: false}}
			store.AddUser(holder)
			create("u", "", "")
			flaky := &flakyUsers{Store: store}
			env.Users = flaky
			k := newKey("u")
			k.Key.AccessKey, k.Key.SecretKey = "AKDORMANT", "mine"
			err := run(ctx, k)
			Expect(err).To(MatchError(op.ErrKeyExists))
			Expect(err.Error()).NotTo(ContainSubstring("AKDORMANT"))
			c := newSubuser("u", "s3sub")
			c.Key = op.UserKeyParams{Type: op.KeyTypeS3, AccessKey: "AKDORMANT", SecretKey: "mine"}
			Expect(run(ctx, c)).To(MatchError(op.ErrKeyExists))
			nu := op.NewCreateUser()
			nu.UID, nu.DisplayName = meta.UserID{ID: "newcomer"}, "N"
			nu.Key.AccessKey, nu.Key.SecretKey = "AKDORMANT", "mine"
			Expect(run(ctx, nu)).To(MatchError(op.ErrKeyExists))
			Expect(flaky.puts).To(BeEmpty(), "nothing written")
			Expect(stored(ctx, "holder").AccessKeys["AKDORMANT"].Secret).To(Equal("s"))
		})
		It("refuses the id when the user listing fails, and falls back to the index where the store cannot list", func(ctx SpecContext) {
			create("u", "", "")
			env.Metadata = listing{MetadataStore: store, err: fmt.Errorf("listing users.uid near AKLIST: %w", op.ErrInternalError)}
			k := newKey("u")
			k.Key.AccessKey, k.Key.SecretKey = "AKLIST", "s"
			err := run(ctx, k)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("AKLIST"))
			env.Metadata = listing{MetadataStore: store, err: op.ErrNotImplemented}
			Expect(run(ctx, k)).To(Succeed(), "the driver lists no users yet; the index check stands alone")
		})
		It("refuses to reactivate a key whose id the index gives another user", func(ctx SpecContext) {
			a := meta.NewUserInfo()
			a.UserID = meta.UserID{ID: "a"}
			a.AccessKeys = map[string]meta.AccessKey{"AKTWICE": {ID: "AKTWICE", Secret: "sa", Active: false}}
			store.AddUser(a)
			b := meta.NewUserInfo()
			b.UserID = meta.UserID{ID: "b"}
			b.AccessKeys = map[string]meta.AccessKey{"AKTWICE": {ID: "AKTWICE", Secret: "sb", Active: true}}
			store.AddUser(b)
			k := newKey("a")
			k.Key.AccessKey, k.Key.GenerateKey, k.Key.Active = "AKTWICE", false, new(true)
			Expect(run(ctx, k)).To(MatchError(op.ErrKeyExists))
			Expect(stored(ctx, "a").AccessKeys["AKTWICE"].Active).To(BeFalse())
		})
		It("refuses a key modify whose id another user holds inactive", func(ctx SpecContext) {
			for _, id := range []string{"a", "b"} {
				u := meta.NewUserInfo()
				u.UserID = meta.UserID{ID: id}
				u.AccessKeys = map[string]meta.AccessKey{"AKBOTH": {ID: "AKBOTH", Secret: "s" + id, Active: false}}
				store.AddUser(u)
			}
			k := newKey("a")
			k.Key.AccessKey, k.Key.SecretKey, k.Key.Active = "AKBOTH", "new", new(true)
			Expect(run(ctx, k)).To(MatchError(op.ErrKeyExists))
			Expect(stored(ctx, "a").AccessKeys["AKBOTH"].Secret).To(Equal("sa"))
			Expect(stored(ctx, "a").AccessKeys["AKBOTH"].Active).To(BeFalse())
		})
		It("refuses a new inactive key over an id another user holds inactive", func(ctx SpecContext) {
			holder := meta.NewUserInfo()
			holder.UserID = meta.UserID{ID: "holder"}
			holder.AccessKeys = map[string]meta.AccessKey{"AKQUIET": {ID: "AKQUIET", Secret: "s", Active: false}}
			store.AddUser(holder)
			create("u", "", "")
			flaky := &flakyUsers{Store: store}
			env.Users = flaky
			k := newKey("u")
			k.Key.AccessKey, k.Key.SecretKey, k.Key.Active = "AKQUIET", "mine", new(false)
			Expect(run(ctx, k)).To(MatchError(op.ErrKeyExists), "two holders would follow any later reactivation")
			Expect(flaky.puts).To(BeEmpty(), "nothing written")
			Expect(stored(ctx, "u").AccessKeys).NotTo(HaveKey("AKQUIET"))
		})
		It("lets a modify that leaves the key inactive, or a removal, through where another user holds the id", func(ctx SpecContext) {
			hold := func(uid string, active bool) {
				u := meta.NewUserInfo()
				u.UserID = meta.UserID{ID: uid}
				u.AccessKeys = map[string]meta.AccessKey{"AKTWICE": {ID: "AKTWICE", Secret: "s" + uid, Active: active}}
				store.AddUser(u)
			}
			hold("a", false)
			hold("b", true)
			k := newKey("a")
			k.Key.AccessKey, k.Key.SecretKey = "AKTWICE", "rotated"
			Expect(run(ctx, k)).To(Succeed(), "a rotation that keeps the key inactive")
			Expect(stored(ctx, "a").AccessKeys["AKTWICE"]).To(HaveField("Secret", "rotated"))
			Expect(stored(ctx, "a").AccessKeys["AKTWICE"].Active).To(BeFalse())
			k = newKey("a")
			k.Key.AccessKey, k.Key.GenerateKey, k.Key.Active = "AKTWICE", false, new(false)
			Expect(run(ctx, k)).To(Succeed(), "an explicit deactivation")
			k = newKey("b")
			k.Key.AccessKey, k.Key.SecretKey = "AKTWICE", "rotated"
			Expect(run(ctx, k)).To(MatchError(op.ErrKeyExists), "a rotation that keeps the key active")
			Expect(stored(ctx, "b").AccessKeys["AKTWICE"].Secret).To(Equal("sb"))
			k = newKey("b")
			k.Key.AccessKey, k.Key.GenerateKey, k.Key.Active = "AKTWICE", false, new(false)
			Expect(run(ctx, k)).To(Succeed(), "a revocation is never refused")
			Expect(stored(ctx, "b").AccessKeys["AKTWICE"].Active).To(BeFalse())
			d := op.NewRemoveKey()
			d.UID, d.Key.AccessKey = meta.UserID{ID: "a"}, "AKTWICE"
			Expect(run(ctx, d)).To(Succeed())
			Expect(stored(ctx, "a").AccessKeys).NotTo(HaveKey("AKTWICE"))
		})
		It("reads every page of the user listing for a holder", func(ctx SpecContext) {
			for i := range 1000 {
				create(fmt.Sprintf("filler%04d", i), "", "")
			}
			holder := meta.NewUserInfo()
			holder.UserID = meta.UserID{ID: "zzzholder"}
			holder.AccessKeys = map[string]meta.AccessKey{"AKPAGE": {ID: "AKPAGE", Secret: "s", Active: false}}
			store.AddUser(holder)
			create("u", "", "")
			k := newKey("u")
			k.Key.AccessKey, k.Key.SecretKey = "AKPAGE", "mine"
			Expect(run(ctx, k)).To(MatchError(op.ErrKeyExists), "the holder is past the first page of 1000")
		})
		It("refuses the id when a listed user cannot be read, naming no credential", func(ctx SpecContext) {
			create("broken", "", "")
			create("u", "", "")
			env.Users = unreadable{
				Store: store, uid: "broken",
				err: fmt.Errorf("reading users.uid/broken holding AKUNREAD: %w", op.ErrInternalError),
			}
			k := newKey("u")
			k.Key.AccessKey, k.Key.SecretKey = "AKUNREAD", "s"
			err := run(ctx, k)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("AKUNREAD"))
			_, err = store.GetUserByAccessKey(ctx, "AKUNREAD")
			Expect(err).To(MatchError(op.ErrNoSuchUser), "nothing written")
		})
		It("generates ids and secrets from crypto/rand with radosgw's lengths and tables", func(ctx SpecContext) {
			create("gen", "", "")
			ids, secrets := map[string]bool{}, map[string]bool{}
			for range 20 {
				k := newKey("gen")
				k.Key.GenerateKey = true
				Expect(run(ctx, k)).To(Succeed())
			}
			for id, key := range stored(ctx, "gen").AccessKeys {
				Expect(id).To(MatchRegexp(`^[0-9A-Z]{20}$`), "PUBLIC_ID_LEN from gen_rand_alphanumeric_upper")
				Expect(key.Secret).To(MatchRegexp(`^[A-Za-z0-9]{40}$`), "SECRET_KEY_LEN from gen_rand_alphanumeric_plain")
				Expect(key.Active).To(BeTrue())
				Expect(key.CreatedAt.IsZero()).To(BeFalse())
				ids[id], secrets[key.Secret] = true, true
			}
			Expect(ids).To(HaveLen(20))
			Expect(secrets).To(HaveLen(20))
		})
		It("applies a named active to a key it modifies and to one it creates", func(ctx SpecContext) {
			create("u", "AK1", "s1")
			k := newKey("u")
			k.Key.AccessKey, k.Key.Active, k.Key.GenerateKey = "AK1", new(false), false
			Expect(run(ctx, k)).To(Succeed())
			Expect(k.Result.AccessKeys["AK1"].Active).To(BeFalse())
			Expect(k.Result.AccessKeys["AK1"].Secret).To(Equal("s1"), "no secret named and none generated")
			k = newKey("u")
			k.Key.AccessKey, k.Key.SecretKey, k.Key.Active = "AK2", "s2", new(false)
			Expect(run(ctx, k)).To(Succeed())
			Expect(k.Result.AccessKeys["AK2"].Active).To(BeFalse(), "radosgw's generate_key makes it active")
		})
		It("finds the user by the access key when the uid names no user, and modifies that key", func(ctx SpecContext) {
			create("holder", "AKHELD", "s1")
			k := newKey("")
			k.Key.AccessKey, k.Key.GenerateKey = "AKHELD", true
			Expect(run(ctx, k)).To(Succeed(), "RGWUser::init falls back to the access key")
			Expect(k.Result.UserID.ID).To(Equal("holder"))
			Expect(stored(ctx, "holder").AccessKeys["AKHELD"].Secret).To(MatchRegexp(`^[A-Za-z0-9]{40}$`), "generate-key rotates the secret")
			k = newKey("")
			k.Key.AccessKey, k.Key.GenerateKey = "AKNOSUCH", true
			Expect(run(ctx, k)).To(MatchError(op.ErrInvalidArgument), "keys.init refuses the anonymous uid")
			k = newKey("nosuch")
			k.Key.GenerateKey = true
			Expect(run(ctx, k)).To(MatchError(op.ErrNoSuchUser))
		})
		It("makes an undefined key type Swift with a subuser, keyed <uid>:<subuser>", func(ctx SpecContext) {
			create("t$u", "", "")
			k := newKey("t$u")
			k.Key.Subuser, k.Key.GenerateKey = "sw", true
			Expect(run(ctx, k)).To(Succeed())
			Expect(k.Type).To(Equal(op.KeyTypeSwift))
			Expect(k.Result.AccessKeys).To(BeEmpty())
			Expect(k.Result.SwiftKeys).To(HaveKey("t$u:sw"))
			sw := k.Result.SwiftKeys["t$u:sw"]
			Expect(sw.ID).To(Equal("t$u:sw"))
			Expect(sw.Subuser).To(Equal("sw"))
			Expect(sw.Secret).To(MatchRegexp(`^[A-Za-z0-9]{40}$`))
			Expect(sw.CreatedAt.IsZero()).To(BeFalse())
			k = newKey("t$u")
			k.Key.Subuser, k.Key.Type, k.Key.GenerateKey = "s3sub", op.KeyTypeS3, true
			Expect(run(ctx, k)).To(Succeed())
			Expect(k.Type).To(Equal(op.KeyTypeS3))
			Expect(k.Result.AccessKeys).To(HaveLen(1))
			for _, key := range k.Result.AccessKeys {
				Expect(key.Subuser).To(Equal("s3sub"), "generate_key records the subuser it was given")
			}
			k = newKey("t$u")
			k.Key.Type, k.Key.GenerateKey = op.KeyTypeSwift, true
			Expect(run(ctx, k)).To(MatchError(op.ErrInvalidAccessKeyID), "a Swift key needs a subuser")
		})
		It("changes a Swift key in place: a rotation keeps it inactive and keeps its creation date", func(ctx SpecContext) {
			create("u", "", "")
			Expect(run(ctx, newSubuser("u", "sw"))).To(Succeed())
			created := stored(ctx, "u").SwiftKeys["u:sw"]
			Expect(created.Active).To(BeTrue())
			k := newKey("u")
			k.Key.Subuser, k.Key.Active, k.Key.GenerateKey = "sw", new(false), false
			Expect(run(ctx, k)).To(Succeed(), "deactivation needs no rotation")
			off := stored(ctx, "u").SwiftKeys["u:sw"]
			Expect(off).To(Equal(meta.AccessKey{ID: "u:sw", Subuser: "sw", Secret: created.Secret, CreatedAt: created.CreatedAt}))
			rotations := map[string]op.Op{}
			kr := newKey("u")
			kr.Key.Subuser, kr.Key.SecretKey = "sw", "rotated"
			rotations["key create with a secret"] = kr
			kg := newKey("u")
			kg.Key.Subuser = "sw"
			rotations["key create generating one"] = kg
			ms := op.NewModifySubuser()
			ms.UID, ms.Subuser = meta.UserID{ID: "u"}, "sw"
			ms.Key.GenSecret = true
			rotations["subuser modify with generate-secret"] = ms
			rotations["subuser created again"] = newSubuser("u", "sw")
			for name, o := range rotations {
				before := stored(ctx, "u").SwiftKeys["u:sw"].Secret
				Expect(run(ctx, o)).To(Succeed(), name)
				after := stored(ctx, "u").SwiftKeys["u:sw"]
				Expect(after.Secret).NotTo(Equal(before), name)
				Expect(after.Secret).NotTo(BeEmpty(), name)
				Expect(after.Active).To(BeFalse(), name+": a revoked key stays revoked")
				Expect(after.CreatedAt).To(Equal(created.CreatedAt), name)
			}
			k = newKey("u")
			k.Key.Subuser, k.Key.Active = "sw", new(true)
			Expect(run(ctx, k)).To(Succeed())
			Expect(stored(ctx, "u").SwiftKeys["u:sw"].Active).To(BeTrue(), "only an explicit active reactivates")
		})
		It("refuses a key change that would leave the key without a secret, storing nothing", func(ctx SpecContext) {
			info := meta.NewUserInfo()
			info.UserID = meta.UserID{ID: "u"}
			info.SwiftKeys = map[string]meta.AccessKey{"u:sw": {ID: "u:sw", Subuser: "sw"}}
			info.AccessKeys = map[string]meta.AccessKey{"AKEMPTY": {ID: "AKEMPTY"}}
			store.AddUser(info)
			before := stored(ctx, "u")
			k := newKey("u")
			k.Key.Subuser, k.Key.GenerateKey = "sw", false
			Expect(run(ctx, k)).To(MatchError(op.ErrInvalidSecretKey), "generate_key's answer to an empty secret")
			k = newKey("u")
			k.Key.AccessKey, k.Key.GenerateKey, k.Key.Active = "AKEMPTY", false, new(true)
			Expect(run(ctx, k)).To(MatchError(op.ErrInvalidSecretKey))
			Expect(stored(ctx, "u")).To(Equal(before))
			k = newKey("u")
			k.Key.Subuser, k.Key.SecretKey = "sw", "now-set"
			Expect(run(ctx, k)).To(Succeed(), "a change that supplies one is taken")
		})
		It("takes a subuser named <uid>:<name> as naming the user, as set_subuser does", func(ctx SpecContext) {
			create("u", "", "")
			create("other", "", "")
			k := newKey("other")
			k.Key.Subuser, k.Key.GenerateKey = "u:sw", true
			Expect(run(ctx, k)).To(Succeed())
			Expect(k.Result.UserID.ID).To(Equal("u"))
			Expect(stored(ctx, "u").SwiftKeys).To(HaveKey("u:sw"))
			Expect(stored(ctx, "other").SwiftKeys).To(BeEmpty())
			k = newKey("")
			k.Key.Subuser, k.Key.GenerateKey = "u:sw2", true
			Expect(run(ctx, k)).To(Succeed(), "no uid at all")
			Expect(stored(ctx, "u").SwiftKeys).To(HaveKey("u:sw2"))
			k = newKey("u")
			k.Key.Subuser, k.Key.GenerateKey = "u:", true
			Expect(run(ctx, k)).To(MatchError(op.ErrInvalidAccessKeyID), "a named but empty subuser still makes the key Swift")
		})
		It("refuses a Swift key on user create and modify, which name no subuser", func(ctx SpecContext) {
			c := op.NewCreateUser()
			c.UID, c.DisplayName = meta.UserID{ID: "sw"}, "S"
			c.Key.Type, c.Key.GenerateKey = op.KeyTypeSwift, true
			Expect(run(ctx, c)).To(MatchError(op.ErrInvalidAccessKeyID), "build_default_swift_kid is empty")
			_, err := store.GetUser(ctx, meta.UserID{ID: "sw"})
			Expect(err).To(MatchError(op.ErrNoSuchUser), "nothing written")
			create("u", "", "")
			m := op.NewModifyUser()
			m.UID = meta.UserID{ID: "u"}
			m.Key.Type, m.Key.SecretKey = op.KeyTypeSwift, "s"
			Expect(run(ctx, m)).To(MatchError(op.ErrInvalidAccessKeyID))
		})
		It("removes an s3 key and reports a missing one as InvalidAccessKeyId", func(ctx SpecContext) {
			create("u", "AK1", "s1")
			d := op.NewRemoveKey()
			d.UID = meta.UserID{ID: "u"}
			d.Key.AccessKey = "AK1"
			Expect(run(ctx, d)).To(Succeed())
			Expect(stored(ctx, "u").AccessKeys).To(BeEmpty())
			Expect(run(ctx, d)).To(MatchError(op.ErrInvalidAccessKeyID))
			d = op.NewRemoveKey()
			d.UID = meta.UserID{ID: "u"}
			Expect(run(ctx, d)).To(MatchError(op.ErrInvalidAccessKeyID), "no access key")
		})
		It("removes a Swift key by its subuser", func(ctx SpecContext) {
			create("u", "", "")
			k := newKey("u")
			k.Key.Subuser, k.Key.GenerateKey = "sw", true
			Expect(run(ctx, k)).To(Succeed())
			d := op.NewRemoveKey()
			d.UID = meta.UserID{ID: "u"}
			d.Key.Type, d.Key.AccessKey = op.KeyTypeSwift, "u:sw"
			Expect(run(ctx, d)).To(MatchError(op.ErrInvalidAccessKeyID), "check_existing_key looks only at <uid>:<subuser>")
			d.Key.Subuser = "sw"
			Expect(run(ctx, d)).To(Succeed())
			Expect(stored(ctx, "u").SwiftKeys).To(BeEmpty())
		})
		It("removes only the named user's key", func(ctx SpecContext) {
			create("a", "AKA", "sa")
			create("b", "AKB", "sb")
			d := op.NewRemoveKey()
			d.UID = meta.UserID{ID: "a"}
			d.Key.AccessKey = "AKB"
			err := run(ctx, d)
			Expect(err).To(MatchError(op.ErrInvalidAccessKeyID))
			Expect(err.Error()).NotTo(ContainSubstring("AKB"))
			Expect(stored(ctx, "b").AccessKeys).To(HaveKey("AKB"))
			Expect(stored(ctx, "a").AccessKeys).To(HaveKey("AKA"))
		})
	})

	Describe("subusers", func() {
		It("creates, modifies and removes a subuser with its keys", func(ctx SpecContext) {
			create("leseb", "AKLESEB", "s")
			c := newSubuser("leseb", "foo")
			c.Perm = "readwrite"
			c.Key = op.UserKeyParams{Type: op.KeyTypeS3, AccessKey: "SUBUSER_ACCESS_KEY", SecretKey: "SUBUSER_SECRET_KEY"}
			Expect(run(ctx, c)).To(Succeed())
			Expect(c.Result.SubUsers["foo"]).To(Equal(meta.SubUser{Name: "foo", Perm: 0x3}))
			Expect(c.Result.SwiftKeys).To(BeEmpty())
			Expect(c.Result.AccessKeys["SUBUSER_ACCESS_KEY"]).To(HaveField("Subuser", "foo"))
			Expect(c.Result.AccessKeys["SUBUSER_ACCESS_KEY"]).To(HaveField("Secret", "SUBUSER_SECRET_KEY"))
			m := op.NewModifySubuser()
			m.UID = c.UID
			m.Subuser = "foo"
			m.Perm = new("read")
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.SubUsers["foo"].Perm).To(Equal(uint32(0x1)))
			r := op.NewRemoveSubuser()
			r.UID = c.UID
			r.Subuser = "foo"
			Expect(run(ctx, r)).To(Succeed())
			info := stored(ctx, "leseb")
			Expect(info.SubUsers).To(BeEmpty())
			Expect(info.AccessKeys).NotTo(HaveKey("SUBUSER_ACCESS_KEY"), "subuser keys are purged")
			Expect(info.AccessKeys).To(HaveKey("AKLESEB"), "the user's own key stays")
			Expect(run(ctx, r)).To(MatchError(op.ErrNoSuchSubUser))
			c2 := newSubuser("leseb", "swift1")
			Expect(run(ctx, c2)).To(Succeed(), "Swift by default, its secret generated")
			Expect(c2.Result.SwiftKeys).To(HaveKey("leseb:swift1"))
			Expect(c2.Result.SwiftKeys["leseb:swift1"].Secret).To(MatchRegexp(`^[A-Za-z0-9]{40}$`))
			Expect(c2.Result.SubUsers["swift1"].Perm).To(BeZero())
			bad := newSubuser("leseb", "x")
			bad.Perm = "read-write"
			Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument))
			Expect(stored(ctx, "leseb").SubUsers).NotTo(HaveKey("x"))
		})
		It("generates an s3 subuser's key when none is named, and when asked to despite one", func(ctx SpecContext) {
			create("u", "", "")
			c := newSubuser("u", "s1")
			c.Key.Type = op.KeyTypeS3
			Expect(run(ctx, c)).To(Succeed())
			Expect(c.Result.AccessKeys).To(HaveLen(1))
			for id, k := range c.Result.AccessKeys {
				Expect(id).To(MatchRegexp(`^[0-9A-Z]{20}$`))
				Expect(k.Subuser).To(Equal("s1"))
			}
			c = newSubuser("u", "s2")
			c.Key = op.UserKeyParams{Type: op.KeyTypeS3, AccessKey: "IGNORED", SecretKey: "ignored", GenAccess: true, GenSecret: true}
			Expect(run(ctx, c)).To(Succeed())
			Expect(c.Result.AccessKeys).NotTo(HaveKey("IGNORED"), "gen-access-key wins over access-key")
			Expect(c.Result.AccessKeys).To(HaveLen(2))
		})
		It("keeps an existing subuser's permission on a second create and rotates its Swift secret", func(ctx SpecContext) {
			create("u", "", "")
			c := newSubuser("u", "sw")
			c.Perm = "full"
			Expect(run(ctx, c)).To(Succeed())
			first := c.Result.SwiftKeys["u:sw"].Secret
			c = newSubuser("u", "sw")
			c.Perm = "read"
			Expect(run(ctx, c)).To(Succeed(), "radosgw refuses no second create")
			Expect(c.Result.SubUsers["sw"].Perm).To(Equal(uint32(0xf)), "std::map::insert keeps the first")
			Expect(c.Result.SwiftKeys["u:sw"].Secret).NotTo(Equal(first), "modify_key generates a new secret")
		})
		It("modifies a subuser's key only when a secret or generate-secret is named", func(ctx SpecContext) {
			create("u", "", "")
			Expect(run(ctx, newSubuser("u", "sw"))).To(Succeed())
			before := stored(ctx, "u").SwiftKeys["u:sw"]
			m := op.NewModifySubuser()
			m.UID, m.Subuser, m.Perm = meta.UserID{ID: "u"}, "sw", new("write")
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.SwiftKeys["u:sw"]).To(Equal(before), "no key op")
			Expect(m.Result.SubUsers["sw"].Perm).To(Equal(uint32(0x2)))
			m = op.NewModifySubuser()
			m.UID, m.Subuser = meta.UserID{ID: "u"}, "sw"
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.SubUsers["sw"].Perm).To(Equal(uint32(0x2)), "no perm named leaves it")
			m = op.NewModifySubuser()
			m.UID, m.Subuser, m.Perm = meta.UserID{ID: "u"}, "sw", new("")
			m.Key.GenSecret = true
			Expect(run(ctx, m)).To(Succeed())
			Expect(m.Result.SubUsers["sw"].Perm).To(BeZero(), "an empty access is no permission")
			Expect(m.Result.SwiftKeys["u:sw"].Secret).NotTo(Equal(before.Secret))
			m = op.NewModifySubuser()
			m.UID, m.Subuser = meta.UserID{ID: "u"}, "sw"
			m.Key.Type, m.Key.GenSecret = op.KeyTypeS3, true
			Expect(run(ctx, m)).To(MatchError(op.ErrInvalidAccessKeyID), "an s3 key op names no access key")
			m = op.NewModifySubuser()
			m.UID, m.Subuser = meta.UserID{ID: "u"}, "nosuch"
			Expect(run(ctx, m)).To(MatchError(op.ErrNoSuchSubUser))
		})
		It("purges only the removed subuser's keys", func(ctx SpecContext) {
			create("u", "AKOWN", "s")
			Expect(run(ctx, newSubuser("u", "s1"))).To(Succeed())
			c := newSubuser("u", "s1")
			c.Key = op.UserKeyParams{Type: op.KeyTypeS3, AccessKey: "AKS1", SecretKey: "x"}
			Expect(run(ctx, c)).To(Succeed())
			c = newSubuser("u", "s2")
			c.Key = op.UserKeyParams{Type: op.KeyTypeS3, AccessKey: "AKS2", SecretKey: "x"}
			Expect(run(ctx, c)).To(Succeed())
			Expect(run(ctx, newSubuser("u", "s2"))).To(Succeed(), "a Swift key for s2 too")
			r := op.NewRemoveSubuser()
			r.UID, r.Subuser = meta.UserID{ID: "u"}, "s1"
			Expect(run(ctx, r)).To(Succeed())
			info := stored(ctx, "u")
			Expect(info.AccessKeys).To(HaveKey("AKOWN"))
			Expect(info.AccessKeys).To(HaveKey("AKS2"))
			Expect(info.AccessKeys).NotTo(HaveKey("AKS1"))
			Expect(info.SwiftKeys).To(HaveKey("u:s2"))
			Expect(info.SwiftKeys).NotTo(HaveKey("u:s1"))
			Expect(info.SubUsers).To(HaveKey("s2"))
		})
		It("checks the user before the subuser's name and permission", func(ctx SpecContext) {
			create("u", "", "")
			bad := newSubuser("nosuch", "x")
			bad.Perm = "bogus"
			Expect(run(ctx, bad)).To(MatchError(op.ErrNoSuchUser))
			Expect(run(ctx, newSubuser("", "x"))).To(MatchError(op.ErrInvalidArgument), "no uid")
			Expect(run(ctx, newSubuser("u", ""))).To(MatchError(op.ErrInvalidArgument), "no name")
			Expect(run(ctx, newSubuser("u", "u:"))).To(MatchError(op.ErrInvalidArgument), "an empty name after the uid")
			r := op.NewRemoveSubuser()
			r.UID = meta.UserID{ID: "u"}
			Expect(run(ctx, r)).To(MatchError(op.ErrInvalidArgument))
			Expect(stored(ctx, "u").SubUsers).To(BeEmpty())
		})
	})

	Describe("caps", func() {
		It("adds and removes caps, returning the array", func(ctx SpecContext) {
			create("test", "", "")
			a := op.NewAddCaps()
			a.UID = meta.UserID{ID: "test"}
			a.Caps = "users=read"
			Expect(run(ctx, a)).To(Succeed())
			Expect(a.Result).To(Equal(meta.Caps{"users": meta.CapRead}))
			Expect(stored(ctx, "test").Caps).To(Equal(meta.Caps{"users": meta.CapRead}))
			r := op.NewRemoveCaps()
			r.UID = a.UID
			r.Caps = "users=read;buckets=write"
			Expect(run(ctx, r)).To(Succeed(), "a type the user lacks is skipped")
			Expect(r.Result).To(BeEmpty())
			a.Caps = ""
			Expect(run(ctx, a)).To(MatchError(op.ErrInvalidCapability))
			a.Caps = "users=read;kittens=read"
			Expect(run(ctx, a)).To(MatchError(op.ErrInvalidCapability))
			Expect(stored(ctx, "test").Caps).To(BeEmpty(), "a refused list writes nothing, its valid caps included")
			r.Caps = ""
			Expect(run(ctx, r)).To(MatchError(op.ErrInvalidCapability))
			a.UID = meta.UserID{ID: "nosuch"}
			a.Caps = "users=read"
			Expect(run(ctx, a)).To(MatchError(op.ErrNoSuchUser))
		})
		It("lets users=write grant any cap, to itself too, as radosgw's design does", func(ctx SpecContext) {
			a := op.NewAddCaps()
			a.UID = meta.UserID{ID: "admin"}
			a.Caps = "users=*;buckets=*;metadata=*;zone=*"
			Expect(runAs(ctx, a, meta.Caps{"users": meta.CapWrite})).To(Succeed())
			Expect(stored(ctx, "admin").Caps).To(Equal(meta.Caps{
				"users": meta.CapAll, "buckets": meta.CapAll, "metadata": meta.CapAll, "zone": meta.CapAll,
			}))
			Expect(runAs(ctx, a, meta.Caps{"users": meta.CapRead})).To(MatchError(op.ErrAccessDenied))
			r := op.NewRemoveCaps()
			r.UID, r.Caps = meta.UserID{ID: "admin"}, "zone=*"
			Expect(runAs(ctx, r, meta.Caps{"users": meta.CapRead, "zone": meta.CapAll})).To(MatchError(op.ErrAccessDenied))
		})
	})

	Describe("quota", func() {
		It("gets and sets the user and bucket quotas", func(ctx SpecContext) {
			create("leseb", "", "")
			s := op.NewSetUserQuota()
			s.UID = meta.UserID{ID: "leseb"}
			s.QuotaType = "user"
			s.Params = op.QuotaParams{MaxObjects: new(int64(100)), Enabled: new(true)}
			Expect(run(ctx, s)).To(Succeed())
			g := op.NewGetUserQuota()
			g.UID = s.UID
			Expect(run(ctx, g)).To(Succeed())
			Expect(g.Result.UserQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: 100, Enabled: true}))
			Expect(g.Result.BucketQuota.Enabled).To(BeFalse())
			g.UID = meta.UserID{}
			Expect(run(ctx, g)).To(MatchError(op.ErrInvalidArgument))
			g.UID = meta.UserID{ID: "nosuch"}
			Expect(run(ctx, g)).To(MatchError(op.ErrNoSuchUser))
			g.UID, g.QuotaType = meta.UserID{ID: "nosuch"}, "users"
			Expect(run(ctx, g)).To(MatchError(op.ErrInvalidArgument), "the quota type is checked first")
		})
		It("defaults each argument to the current value, but resets check_on_raw", func(ctx SpecContext) {
			info := meta.NewUserInfo()
			info.UserID = meta.UserID{ID: "q"}
			info.BucketQuota = meta.Quota{MaxSize: 4096, MaxObjects: 7, Enabled: true, CheckOnRaw: true}
			store.AddUser(info)
			s := op.NewSetUserQuota()
			s.UID, s.QuotaType = info.UserID, "bucket"
			s.Params.MaxSize = new(int64(10))
			Expect(run(ctx, s)).To(Succeed())
			Expect(stored(ctx, "q").BucketQuota).To(Equal(meta.Quota{MaxSize: 10, MaxObjects: 7, Enabled: true}),
				"the quota starts from RGWQuotaInfo(), and no argument sets check_on_raw")
			s.Params = op.QuotaParams{MaxSize: new(int64(10)), MaxSizeKB: new(int64(2))}
			Expect(run(ctx, s)).To(Succeed())
			Expect(stored(ctx, "q").BucketQuota.MaxSize).To(Equal(int64(2048)), "max-size-kb wins")
			Expect(stored(ctx, "q").UserQuota).To(Equal(meta.NewUserInfo().UserQuota), "the other quota is untouched")
		})
		It("takes both quotas from a body when no quota type is named", func(ctx SpecContext) {
			create("leseb", "", "")
			s := op.NewSetUserQuota()
			s.UID = meta.UserID{ID: "leseb"}
			body := `{"user_quota":{"max_size_kb":4096,"max_objects":-1,"enabled":false},"bucket_quota":{"max_size_kb":1024,"max_objects":-1,"enabled":true}}`
			Expect(runBody(ctx, s, body, int64(len(body)))).To(Succeed())
			info := stored(ctx, "leseb")
			Expect(info.UserQuota).To(Equal(meta.Quota{MaxSize: 4096 * 1024, MaxObjects: -1}))
			Expect(info.BucketQuota).To(Equal(meta.Quota{MaxSize: 1024 * 1024, MaxObjects: -1, Enabled: true}))
			body = `{"bucket_quota":{"max_objects":3}}`
			Expect(runBody(ctx, s, body, -1)).To(Succeed(), "chunked")
			info = stored(ctx, "leseb")
			Expect(info.UserQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: -1}), "an absent quota is RGWQuotaInfo()")
			Expect(info.BucketQuota).To(Equal(meta.Quota{MaxObjects: 3}))
		})
		It("takes one quota from a body, and falls back to the arguments for an empty chunked one", func(ctx SpecContext) {
			create("leseb", "", "")
			s := op.NewSetUserQuota()
			s.UID, s.QuotaType = meta.UserID{ID: "leseb"}, "user"
			body := `{"max_objects":9,"enabled":true} trailing text json_spirit leaves unread`
			Expect(runBody(ctx, s, body, int64(len(body)))).To(Succeed())
			Expect(stored(ctx, "leseb").UserQuota).To(Equal(meta.Quota{MaxObjects: 9, Enabled: true}))
			s.Params.MaxObjects = new(int64(5))
			Expect(runBody(ctx, s, "", -1)).To(Succeed())
			Expect(stored(ctx, "leseb").UserQuota).To(Equal(meta.Quota{MaxObjects: 5, Enabled: true}))
		})
		It("reads a body that is no object as one with no members, a lone scalar only when it is the whole body", func(ctx SpecContext) {
			create("leseb", "", "")
			s := op.NewSetUserQuota()
			s.UID, s.QuotaType = meta.UserID{ID: "leseb"}, "user"
			for _, body := range []string{`5`, `[{"max_objects":5}] text`, `"max_objects" text`} {
				Expect(runBody(ctx, s, body, int64(len(body)))).To(Succeed(), body)
				Expect(stored(ctx, "leseb").UserQuota).To(Equal(meta.Quota{}), body)
			}
			Expect(runBody(ctx, s, `5 `, 2)).To(MatchError(op.ErrInvalidArgument), "write_string's length differs")
		})
		It("refuses what radosgw refuses, in radosgw's order", func(ctx SpecContext) {
			create("leseb", "", "")
			s := op.NewSetUserQuota()
			s.UID = meta.UserID{ID: "nosuch"}
			Expect(run(ctx, s)).To(MatchError(op.ErrInvalidArgument), "no quota type and no body, before the lookup")
			s.QuotaType = "objects"
			Expect(run(ctx, s)).To(MatchError(op.ErrInvalidArgument))
			s.QuotaType = "user"
			Expect(runBody(ctx, s, "not json", 8)).To(MatchError(op.ErrNoSuchUser), "the lookup precedes the body")
			s.UID = meta.UserID{}
			Expect(run(ctx, s)).To(MatchError(op.ErrInvalidArgument))
			s.UID = meta.UserID{ID: "leseb"}
			Expect(runBody(ctx, s, "not json", 8)).To(MatchError(op.ErrInvalidArgument))
			Expect(runBody(ctx, s, `{"max_objects":1.5}`, 19)).To(MatchError(op.ErrInvalidArgument))
			big := `{"max_objects":1,"pad":"` + strings.Repeat("x", 1024) + `"}`
			Expect(runBody(ctx, s, big, int64(len(big)))).To(MatchError(op.ErrInvalidRange), "QUOTA_INPUT_MAX_LEN")
			s.QuotaType = ""
			Expect(runBody(ctx, s, "", -1)).To(MatchError(op.ErrInvalidArgument), "an empty body for both quotas")
			Expect(stored(ctx, "leseb").UserQuota).To(Equal(meta.NewUserInfo().UserQuota), "nothing written")
		})
		It("refuses a size that overflows or is negative other than -1, storing nothing", func(ctx SpecContext) {
			create("leseb", "", "")
			before := stored(ctx, "leseb")
			s := op.NewSetUserQuota()
			s.UID, s.QuotaType = meta.UserID{ID: "leseb"}, "user"
			for name, p := range map[string]op.QuotaParams{
				"max-size-kb past int64 in bytes":   {MaxSizeKB: new(int64(9007199254740992))},
				"max-size-kb wrapping to 1024":      {MaxSizeKB: new(int64(18014398509481985))},
				"a negative max-size-kb":            {MaxSizeKB: new(int64(-1))},
				"a negative max-size other than -1": {MaxSize: new(int64(-5))},
			} {
				s.Params = p
				Expect(run(ctx, s)).To(MatchError(op.ErrInvalidArgument), name)
			}
			for _, body := range []string{
				`{"max_size_kb":9007199254740992}`,
				`{"max_size":-2}`,
				`{"max_size_kb":-1}`,
			} {
				s.Params = op.QuotaParams{}
				Expect(runBody(ctx, s, body, int64(len(body)))).To(MatchError(op.ErrInvalidArgument), body)
			}
			all := op.NewSetUserQuota()
			all.UID = s.UID
			body := `{"bucket_quota":{"max_size":-3}}`
			Expect(runBody(ctx, all, body, int64(len(body)))).To(MatchError(op.ErrInvalidArgument))
			Expect(stored(ctx, "leseb")).To(Equal(before))
			s.Params = op.QuotaParams{MaxSize: new(int64(-1)), MaxSizeKB: nil}
			Expect(run(ctx, s)).To(Succeed(), "-1 is radosgw's no limit")
			s.Params = op.QuotaParams{MaxSizeKB: new(int64(9007199254740991))}
			Expect(run(ctx, s)).To(Succeed(), "the largest kb whose bytes fit")
			Expect(stored(ctx, "leseb").UserQuota.MaxSize).To(Equal(int64(9007199254740991 * 1024)))
		})
		It("sets only the named user's quota", func(ctx SpecContext) {
			create("a", "", "")
			create("b", "", "")
			s := op.NewSetUserQuota()
			s.UID, s.QuotaType = meta.UserID{ID: "a"}, "user"
			s.Params.Enabled = new(true)
			Expect(run(ctx, s)).To(Succeed())
			Expect(stored(ctx, "a").UserQuota.Enabled).To(BeTrue())
			Expect(stored(ctx, "b").UserQuota.Enabled).To(BeFalse())
		})
	})

	Describe("security", func() {
		ops := func() []op.Op {
			uid := meta.UserID{ID: "admin"}
			k := op.NewCreateKey()
			k.UID, k.Key.AccessKey, k.Key.GenerateKey = uid, "AKIA", true
			rk := op.NewRemoveKey()
			rk.UID, rk.Key.AccessKey = uid, "AKIA"
			cs := op.NewCreateSubuser()
			cs.UID, cs.Subuser = uid, "s"
			cs.Key.Type, cs.Key.AccessKey = op.KeyTypeS3, "AKIA"
			ms := op.NewModifySubuser()
			ms.UID, ms.Subuser = uid, "s"
			rs := op.NewRemoveSubuser()
			rs.UID, rs.Subuser = uid, "s"
			ac := op.NewAddCaps()
			ac.UID, ac.Caps = uid, "users=*"
			rc := op.NewRemoveCaps()
			rc.UID, rc.Caps = uid, "users=*"
			gq := op.NewGetUserQuota()
			gq.UID = uid
			sq := op.NewSetUserQuota()
			sq.UID, sq.QuotaType = uid, "user"
			return []op.Op{k, rk, cs, ms, rs, ac, rc, gq, sq}
		}
		It("checks the caps before any user lookup, so a refused caller learns nothing", func(ctx SpecContext) {
			users := new(opfakes.FakeUserStore)
			env.Users = users
			for _, o := range ops() {
				Expect(runAs(ctx, o, meta.Caps{"buckets": meta.CapAll, "user-info-without-keys": meta.CapAll})).
					To(MatchError(op.ErrAccessDenied), o.Name())
				other := meta.CapRead
				if o.Name() == "get_quota_info" {
					other = meta.CapWrite
				}
				Expect(runAs(ctx, o, meta.Caps{"users": other})).To(MatchError(op.ErrAccessDenied), o.Name()+" with the other users bit")
			}
			Expect(users.Invocations()).To(BeEmpty())
		})
		It("names no credential in an error when the store fails", func(ctx SpecContext) {
			create("u", "AKSTORED", "s")
			flaky := &flakyUsers{Store: store}
			env.Users = flaky
			flaky.keyReadErr = fmt.Errorf("reading users.keys/AKIALOOKUP: %w", op.ErrInternalError)
			k := newKey("u")
			k.Key.AccessKey, k.Key.SecretKey = "AKIALOOKUP", "s"
			err := run(ctx, k)
			Expect(err).To(MatchError(op.ErrInternalError), "a failed duplicate check refuses the key")
			Expect(err.Error()).NotTo(ContainSubstring("AKIALOOKUP"))
			flaky.keyReadErr = nil
			flaky.putErr = fmt.Errorf("writing users.keys/AKIAPUT with secret SECRETPUT: %w", op.ErrInternalError)
			k = newKey("u")
			k.Key.AccessKey, k.Key.SecretKey = "AKIAPUT", "SECRETPUT"
			err = run(ctx, k)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("AKIAPUT"))
			Expect(err.Error()).NotTo(ContainSubstring("SECRETPUT"))
			s := newSubuser("u", "sub")
			s.Key = op.UserKeyParams{Type: op.KeyTypeS3, AccessKey: "AKIAPUT", SecretKey: "SECRETPUT"}
			err = run(ctx, s)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("SECRETPUT"))
		})
		It("completes a key create on retry after a failed write", func(ctx SpecContext) {
			create("u", "", "")
			flaky := &flakyUsers{Store: store, putErr: op.ErrInternalError}
			env.Users = flaky
			k := newKey("u")
			k.Key.AccessKey, k.Key.SecretKey = "AKRETRY", "s"
			Expect(run(ctx, k)).To(MatchError(op.ErrInternalError))
			flaky.putErr = nil
			k = newKey("u")
			k.Key.AccessKey, k.Key.SecretKey = "AKRETRY", "s"
			Expect(run(ctx, k)).To(Succeed(), "the stored key is modified, as radosgw's retry modifies it")
			Expect(stored(ctx, "u").AccessKeys["AKRETRY"].Secret).To(Equal("s"))
			Expect(flaky.removed).To(BeEmpty(), "nothing is undone by deletion")
		})
		It("writes each change against the version it read", func(ctx SpecContext) {
			create("u", "", "")
			flaky := &flakyUsers{Store: store}
			env.Users = flaky
			a := op.NewAddCaps()
			a.UID, a.Caps = meta.UserID{ID: "u"}, "usage=read"
			Expect(run(ctx, a)).To(Succeed())
			Expect(flaky.puts).To(HaveLen(1))
			Expect(flaky.puts[0].IfVersion).NotTo(BeNil())
			Expect(flaky.puts[0].IfVersion.Ver).NotTo(BeZero())
		})
	})
})

// listing is a metadata store whose listing fails with err.
type listing struct {
	op.MetadataStore
	err error
}

func (l listing) List(context.Context, string, string, int) ([]string, string, bool, error) {
	return nil, "", false, l.err
}

// unreadable is the memstore's user store failing GetUser of uid with err.
type unreadable struct {
	*memstore.Store
	uid string
	err error
}

func (u unreadable) GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error) {
	if id.ID == u.uid {
		return nil, u.err
	}
	return u.Store.GetUser(ctx, id)
}
