package op_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// refusingPuts is the memory store with every user write refused before it
// stores anything.
type refusingPuts struct {
	*memstore.Store
}

func (refusingPuts) PutUser(context.Context, *op.UserRecord, op.PutUserOptions) error {
	return errors.Join(op.ErrInternalError, errors.New("the user write was refused"))
}

// These specs pin docs/differences.md, "The admin user routes answer store
// failures, and keep the account users index clean where radosgw leaves
// entries behind" and "The admin key, subuser, caps and quota routes differ
// from radosgw in seven ways".
var _ = Describe("admin user ops where they differ from radosgw", func() {
	const acct = "RGW00000000000000002"
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
		store.AddAccount(meta.AccountInfo{ID: acct, Name: "other"})
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
	})
	request := func() *op.Request {
		caps := meta.Caps{"users": meta.CapAll}
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return &op.Request{Env: env, Header: http.Header{}, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps,
		}}
	}
	run := func(ctx context.Context, o op.Op) error { return op.Run(ctx, o, request()) }
	runBody := func(ctx context.Context, o op.Op, body string) error {
		r := request()
		r.Body, r.ContentLength = strings.NewReader(body), int64(len(body))
		r.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return op.Run(ctx, o, r)
	}
	member := func(uid, name string) *op.CreateUser {
		o := op.NewCreateUser()
		o.UID = meta.UserID{ID: uid}
		o.DisplayName, o.AccountID = name, acct
		return o
	}
	accountUsers := func(ctx context.Context) []string {
		GinkgoHelper()
		ids, _, err := store.ListAccountUsers(ctx, acct, "", 1000)
		Expect(err).NotTo(HaveOccurred())
		return ids
	}

	Describe("the account users index", func() {
		It("adds a member's entry before it stores the user, so a refused write leaves the entry and no member", func(ctx SpecContext) {
			env.Users = refusingPuts{Store: store}
			Expect(run(ctx, member("m1", "member-one"))).To(MatchError(op.ErrInternalError))
			_, err := store.GetUser(ctx, meta.UserID{ID: "m1"})
			Expect(err).To(MatchError(op.ErrNoSuchUser), "the user write was refused")
			Expect(accountUsers(ctx)).To(ConsistOf("m1"), "the entry went first, where radosgw links after the store")
		})

		It("adds an unchanged member's missing entry on any write of the user", func(ctx SpecContext) {
			Expect(run(ctx, member("m1", "member-one"))).To(Succeed())
			Expect(store.RemoveAccountUser(ctx, acct, "member-one")).To(Succeed())
			m := op.NewModifyUser()
			m.UID = meta.UserID{ID: "m1"}
			m.MaxBuckets = new(int32(7))
			Expect(run(ctx, m)).To(Succeed())
			Expect(accountUsers(ctx)).To(ConsistOf("m1"), "the link is unchanged, where radosgw links only a changed one")
		})

		It("removes a non-root member whose entry is already gone", func(ctx SpecContext) {
			Expect(run(ctx, member("m1", "member-one"))).To(Succeed())
			Expect(store.RemoveAccountUser(ctx, acct, "member-one")).To(Succeed())
			d := op.NewRemoveUser()
			d.UID = meta.UserID{ID: "m1"}
			Expect(run(ctx, d)).To(Succeed(), "radosgw answers NoSuchUser for good")
			_, err := store.GetUser(ctx, d.UID)
			Expect(err).To(MatchError(op.ErrNoSuchUser))
		})

		It("accepts a display name an entry naming a gone user holds, and rewrites the entry", func(ctx SpecContext) {
			ghost := meta.NewUserInfo()
			ghost.UserID, ghost.DisplayName, ghost.AccountID = meta.UserID{ID: "ghost"}, "Taken", acct
			Expect(store.AddAccountUser(ctx, acct, ghost)).To(Succeed())
			Expect(run(ctx, member("fresh", "taken"))).To(Succeed(), "radosgw refuses the name the stale entry holds")
			Expect(accountUsers(ctx)).To(ConsistOf("fresh"))
		})

		It("accepts a display name an entry holds for a live user who no longer carries it", func(ctx SpecContext) {
			Expect(run(ctx, member("old", "other-name"))).To(Succeed())
			stale, err := store.GetUser(ctx, meta.UserID{ID: "old"})
			Expect(err).NotTo(HaveOccurred())
			info := stale.Info
			info.DisplayName = "Taken"
			Expect(store.AddAccountUser(ctx, acct, info)).To(Succeed(), "an entry under the old user's former name")
			Expect(run(ctx, member("fresh", "Taken"))).To(Succeed(), "the check reads each entry's user's current name")
			Expect(accountUsers(ctx)).To(ConsistOf("old", "fresh"))
		})
	})

	Describe("the key routes and the Swift key index", func() {
		It("finds no user through a Swift key create's access key, answering as for a request naming no user", func(ctx SpecContext) {
			holder := meta.NewUserInfo()
			holder.UserID, holder.DisplayName = meta.UserID{ID: "holder"}, "Holder"
			holder.AccessKeys = map[string]meta.AccessKey{"AKHELD": {ID: "AKHELD", Secret: "s1", Active: true}}
			holder.SwiftKeys = map[string]meta.AccessKey{"holder:sw": {ID: "holder:sw", Subuser: "sw", Secret: "s2", Active: true}}
			store.AddUser(holder)
			for _, key := range []string{"AKHELD", "holder:sw"} {
				k := op.NewCreateKey()
				k.Key.Type, k.Key.AccessKey, k.Key.GenerateKey = op.KeyTypeSwift, key, true
				Expect(run(ctx, k)).To(MatchError(op.ErrInvalidArgument), "no uid, access-key %q", key)
				k = op.NewCreateKey()
				k.UID = meta.UserID{ID: "nosuch"}
				k.Key.Type, k.Key.AccessKey, k.Key.GenerateKey = op.KeyTypeSwift, key, true
				Expect(run(ctx, k)).To(MatchError(op.ErrNoSuchUser), "uid of no user, access-key %q", key)
			}
			rec, err := store.GetUser(ctx, holder.UserID)
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.SwiftKeys).To(Equal(holder.SwiftKeys), "nothing written")
		})

		It("does not check a Swift key create's access key against another user's Swift keys", func(ctx SpecContext) {
			a := meta.NewUserInfo()
			a.UserID, a.DisplayName = meta.UserID{ID: "a"}, "A"
			a.SwiftKeys = map[string]meta.AccessKey{"a:sw": {ID: "a:sw", Subuser: "sw", Secret: "s", Active: true}}
			store.AddUser(a)
			b := meta.NewUserInfo()
			b.UserID, b.DisplayName = meta.UserID{ID: "b"}, "B"
			store.AddUser(b)
			k := op.NewCreateKey()
			k.UID = b.UserID
			k.Key.Type, k.Key.Subuser, k.Key.AccessKey, k.Key.GenerateKey = op.KeyTypeSwift, "sw2", "a:sw", true
			Expect(run(ctx, k)).To(Succeed(), "radosgw answers 409 KeyExists")
			Expect(k.Result.SwiftKeys).To(HaveKey("b:sw2"), "the id is <uid>:<subuser>, the access key discarded")
		})
	})

	Describe("a quota set's JSON body", func() {
		var s *op.SetUserQuota
		BeforeEach(func() {
			u := meta.NewUserInfo()
			u.UserID, u.DisplayName = meta.UserID{ID: "leseb"}, "Leseb"
			store.AddUser(u)
			s = op.NewSetUserQuota()
			s.UID, s.QuotaType = u.UserID, "user"
		})
		quota := func(ctx context.Context) meta.Quota {
			GinkgoHelper()
			rec, err := store.GetUser(ctx, meta.UserID{ID: "leseb"})
			Expect(err).NotTo(HaveOccurred())
			return rec.Info.UserQuota
		}

		DescribeTable("refuses what json_spirit's grammar takes and Go's does not, storing nothing",
			func(ctx context.Context, body string) {
				before := quota(ctx)
				Expect(runBody(ctx, s, body)).To(MatchError(op.ErrInvalidArgument))
				Expect(quota(ctx)).To(Equal(before))
			},
			Entry("a stray comma in an object", `{"max_objects":5,}`),
			Entry("a stray comma in an array", `[1,]`),
			Entry("a form feed before the object", "\f{\"max_objects\":5}"),
		)

		DescribeTable("takes a lone number as sent, which radosgw's write-back comparison refuses",
			func(ctx context.Context, body string) {
				Expect(runBody(ctx, s, body)).To(Succeed())
				Expect(quota(ctx)).To(Equal(meta.Quota{}), "a value that is no object sets no member")
			},
			Entry("1.0", `1.0`),
			Entry("-0", `-0`),
		)
	})
})
