package op_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// stopStore is the memstore with one call of a named method failing before
// it changes anything, as a gateway that stops there leaves the store.
type stopStore struct {
	*memstore.Store
	stop map[string]error
}

func (s *stopStore) take(method string) error {
	err, ok := s.stop[method]
	if ok {
		delete(s.stop, method)
	}
	return err
}

func (s *stopStore) PutUser(ctx context.Context, rec *op.UserRecord, opts op.PutUserOptions) error {
	if err := s.take("PutUser"); err != nil {
		return err
	}
	return s.Store.PutUser(ctx, rec, opts)
}

func (s *stopStore) RemoveUser(ctx context.Context, rec *op.UserRecord) error {
	if err := s.take("RemoveUser"); err != nil {
		return err
	}
	return s.Store.RemoveUser(ctx, rec)
}

func (s *stopStore) AddAccountUser(ctx context.Context, accountID string, info meta.UserInfo) error {
	if err := s.take("AddAccountUser"); err != nil {
		return err
	}
	return s.Store.AddAccountUser(ctx, accountID, info)
}

func (s *stopStore) RemoveAccountUser(ctx context.Context, accountID, displayName string) error {
	if err := s.take("RemoveAccountUser"); err != nil {
		return err
	}
	return s.Store.RemoveAccountUser(ctx, accountID, displayName)
}

func (s *stopStore) ListAccountUsers(ctx context.Context, accountID, marker string, maxIDs uint32) ([]string, string, error) {
	if err := s.take("ListAccountUsers"); err != nil {
		return nil, "", err
	}
	return s.Store.ListAccountUsers(ctx, accountID, marker, maxIDs)
}

var _ = Describe("account membership that stops part way", func() {
	const acct = "RGW00000000000000001"
	var (
		store *memstore.Store
		stops *stopStore
		env   *op.Env
	)
	BeforeEach(func() {
		pools := meta.NewZonePlacementInfo()
		pools.StorageClasses = meta.ZoneStorageClasses{meta.StorageClassStandard: {}}
		store = memstore.New(memstore.Config{Params: meta.ZoneParams{
			Name: "z1", PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": pools},
		}})
		store.AddAccount(meta.AccountInfo{ID: acct, Name: "acme"})
		stops = &stopStore{Store: store, stop: map[string]error{}}
		env = &op.Env{
			Zone: store, Users: stops, Accounts: stops, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
	})
	run := func(ctx context.Context, o op.Op) error {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		caps := meta.Caps{"users": meta.CapAll, "accounts": meta.CapAll, "account": meta.CapAll}
		u.Caps = caps
		return op.Run(ctx, o, &op.Request{Env: env, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps,
		}})
	}
	stopAt := func(method string, err error) { stops.stop[method] = err }
	newUser := func(ctx context.Context, display, account string) {
		GinkgoHelper()
		o := op.NewCreateUser()
		o.UID, o.DisplayName, o.AccountID = meta.UserID{ID: "u"}, display, account
		Expect(run(ctx, o)).To(Succeed())
	}
	modify := func(change func(*op.ModifyUser)) *op.ModifyUser {
		m := op.NewModifyUser()
		m.UID = meta.UserID{ID: "u"}
		change(m)
		return m
	}
	unrelated := func() *op.ModifyUser {
		return modify(func(m *op.ModifyUser) { m.MaxBuckets = new(int32(7)) })
	}
	stored := func(ctx context.Context) meta.UserInfo {
		GinkgoHelper()
		rec, err := store.GetUser(ctx, meta.UserID{ID: "u"})
		Expect(err).NotTo(HaveOccurred())
		return rec.Info
	}
	members := func(ctx context.Context) []string {
		GinkgoHelper()
		ids, _, err := store.ListAccountUsers(ctx, acct, "", 1000)
		Expect(err).NotTo(HaveOccurred())
		return ids
	}
	removeAccount := func(ctx context.Context) error {
		return run(ctx, &op.RemoveAccount{Params: op.AccountParams{ID: acct}})
	}
	// expectHeld asserts that removing the account is refused while a user
	// names it or its index names a live user.
	expectHeld := func(ctx context.Context) {
		GinkgoHelper()
		err := removeAccount(ctx)
		Expect(err).To(HaveOccurred(), "the account is held")
		Expect(op.AsError(err).Status).To(Equal(409))
		_, err = store.GetAccount(ctx, acct)
		Expect(err).NotTo(HaveOccurred())
	}
	// hasKey reports whether the index holds an entry under name, by
	// removing it: a spec's last check.
	hasKey := func(ctx context.Context, name string) bool {
		GinkgoHelper()
		err := store.RemoveAccountUser(ctx, acct, name)
		if errors.Is(err, op.ErrNoSuchKey) {
			return false
		}
		Expect(err).NotTo(HaveOccurred())
		return true
	}
	join := func() *op.ModifyUser { return modify(func(m *op.ModifyUser) { m.AccountID = acct }) }

	Describe("a join by modify", func() {
		BeforeEach(func(ctx SpecContext) { newUser(ctx, "alpha", "") })

		It("stopped after the buckets moved, before the write, leaves the account held by them", func(ctx SpecContext) {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: meta.UserOwner(meta.UserID{ID: "u"}), Placement: meta.PlacementRule{Name: "default-placement"}})
			Expect(err).NotTo(HaveOccurred())
			stopAt("ListAccountUsers", op.ErrInternalError)
			Expect(run(ctx, join())).To(MatchError(op.ErrInternalError))
			expectHeld(ctx)
			Expect(run(ctx, unrelated())).To(Succeed())
			expectHeld(ctx)
			Expect(run(ctx, join())).To(Succeed(), "the retry")
			Expect(stored(ctx).AccountID).To(Equal(acct))
			Expect(members(ctx)).To(ConsistOf("u"))
			expectHeld(ctx)
		})
		DescribeTable("stopped after its entry, before the user, leaves the account held by the entry",
			func(ctx SpecContext, stop error) {
				stopAt("PutUser", stop)
				Expect(run(ctx, join())).To(MatchError(stop))
				Expect(stored(ctx).AccountID).To(BeEmpty())
				Expect(members(ctx)).To(ConsistOf("u"), "the entry comes first")
				expectHeld(ctx)
				Expect(run(ctx, unrelated())).To(Succeed())
				expectHeld(ctx)
				Expect(run(ctx, join())).To(Succeed(), "the retry")
				Expect(stored(ctx).AccountID).To(Equal(acct))
				expectHeld(ctx)
				Expect(hasKey(ctx, "alpha")).To(BeTrue())
			},
			Entry("on a failure", op.ErrInternalError),
			Entry("on a lost version race", op.ErrConcurrentModification),
		)
	})

	Describe("a create into the account", func() {
		create := func() *op.CreateUser {
			o := op.NewCreateUser()
			o.UID, o.DisplayName, o.AccountID = meta.UserID{ID: "u"}, "alpha", acct
			return o
		}
		It("stopped after its entry, before the user, leaves only an entry naming no user, and the retry joins", func(ctx SpecContext) {
			stopAt("PutUser", op.ErrInternalError)
			Expect(run(ctx, create())).To(MatchError(op.ErrInternalError))
			_, err := store.GetUser(ctx, meta.UserID{ID: "u"})
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			Expect(members(ctx)).To(ConsistOf("u"))
			Expect(run(ctx, create())).To(Succeed(), "the retry")
			expectHeld(ctx)
		})
		It("stopped at its entry stores no user, so no user names the account without an entry", func(ctx SpecContext) {
			stopAt("AddAccountUser", op.ErrInternalError)
			Expect(run(ctx, create())).To(MatchError(op.ErrInternalError))
			_, err := store.GetUser(ctx, meta.UserID{ID: "u"})
			Expect(err).To(MatchError(op.ErrNoSuchUser), "the user is stored only once its entry is")
			Expect(run(ctx, create())).To(Succeed(), "the retry")
			Expect(run(ctx, join())).To(Succeed(), "a modify naming the account changes nothing")
			Expect(members(ctx)).To(ConsistOf("u"))
			expectHeld(ctx)
		})
	})

	Describe("a display name change", func() {
		BeforeEach(func(ctx SpecContext) { newUser(ctx, "alpha", acct) })
		rename := func() *op.ModifyUser { return modify(func(m *op.ModifyUser) { m.DisplayName = "gamma" }) }

		It("stopped after the new entry, before the user, keeps both entries and the retry drops the old one", func(ctx SpecContext) {
			stopAt("PutUser", op.ErrInternalError)
			Expect(run(ctx, rename())).To(MatchError(op.ErrInternalError))
			Expect(stored(ctx).DisplayName).To(Equal("alpha"))
			expectHeld(ctx)
			Expect(run(ctx, unrelated())).To(Succeed())
			expectHeld(ctx)
			Expect(run(ctx, rename())).To(Succeed(), "the retry")
			Expect(stored(ctx).DisplayName).To(Equal("gamma"))
			expectHeld(ctx)
			Expect(hasKey(ctx, "alpha")).To(BeFalse(), "the old entry goes once the user holds the new name")
			Expect(hasKey(ctx, "gamma")).To(BeTrue())
		})
		DescribeTable("stopped after the user, before the old entry goes, keeps the new entry",
			func(ctx SpecContext, retry func() *op.ModifyUser) {
				stopAt("RemoveAccountUser", op.ErrInternalError)
				Expect(run(ctx, rename())).To(MatchError(op.ErrInternalError))
				Expect(stored(ctx).DisplayName).To(Equal("gamma"))
				expectHeld(ctx)
				Expect(run(ctx, retry())).To(Succeed())
				expectHeld(ctx)
				Expect(hasKey(ctx, "gamma")).To(BeTrue(), "the user's entry under its name")
			},
			Entry("retried by the same request", rename),
			Entry("followed by an unrelated modify", unrelated),
		)
		It("keeps the entry of a path change, at a stop and once retried", func(ctx SpecContext) {
			path := func() *op.ModifyUser { return modify(func(m *op.ModifyUser) { m.Path = "/x/" }) }
			stopAt("PutUser", op.ErrInternalError)
			Expect(run(ctx, path())).To(MatchError(op.ErrInternalError))
			expectHeld(ctx)
			Expect(run(ctx, path())).To(Succeed(), "the retry")
			Expect(stored(ctx).Path).To(Equal("/x/"))
			Expect(hasKey(ctx, "alpha")).To(BeTrue(), "the same key, overwritten, not removed")
		})
		It("keeps the entry of a change of case alone, at a stop and once retried", func(ctx SpecContext) {
			upper := func() *op.ModifyUser { return modify(func(m *op.ModifyUser) { m.DisplayName = "ALPHA" }) }
			stopAt("PutUser", op.ErrInternalError)
			Expect(run(ctx, upper())).To(MatchError(op.ErrInternalError))
			expectHeld(ctx)
			Expect(run(ctx, upper())).To(Succeed(), "the retry")
			Expect(stored(ctx).DisplayName).To(Equal("ALPHA"))
			Expect(hasKey(ctx, "alpha")).To(BeTrue(), "cls_user keys the name lower-cased")
		})
		It("adds back a member's missing entry on any later write", func(ctx SpecContext) {
			Expect(store.RemoveAccountUser(ctx, acct, "alpha")).To(Succeed(), "an entry a radosgw write that stopped left out")
			Expect(run(ctx, unrelated())).To(Succeed())
			expectHeld(ctx)
			Expect(hasKey(ctx, "alpha")).To(BeTrue())
		})
		It("leaves the entry alone on a change of the account root flag", func(ctx SpecContext) {
			Expect(run(ctx, modify(func(m *op.ModifyUser) { m.AccountRoot = new(true) }))).To(Succeed())
			Expect(stored(ctx).Type).To(Equal(meta.IdentityRoot))
			Expect(members(ctx)).To(ConsistOf("u"))
			Expect(hasKey(ctx, "alpha")).To(BeTrue())
		})
	})

	Describe("a user removal", func() {
		BeforeEach(func(ctx SpecContext) { newUser(ctx, "alpha", acct) })
		remove := func() *op.RemoveUser {
			d := op.NewRemoveUser()
			d.UID = meta.UserID{ID: "u"}
			return d
		}
		It("stopped after the user, before its entry, leaves only an entry naming no user", func(ctx SpecContext) {
			stopAt("RemoveAccountUser", op.ErrInternalError)
			Expect(run(ctx, remove())).To(MatchError(op.ErrInternalError))
			_, err := store.GetUser(ctx, meta.UserID{ID: "u"})
			Expect(err).To(MatchError(op.ErrNoSuchUser), "the user goes first")
			Expect(run(ctx, remove())).To(MatchError(op.ErrNoSuchUser), "nothing is left to remove")
			Expect(removeAccount(ctx)).To(Succeed(), "an entry naming a gone user holds nothing off")
		})
		It("stopped at the user leaves the user and its entry, and the retry removes both", func(ctx SpecContext) {
			stopAt("RemoveUser", op.ErrInternalError)
			Expect(run(ctx, remove())).To(MatchError(op.ErrInternalError))
			Expect(stored(ctx).AccountID).To(Equal(acct))
			expectHeld(ctx)
			Expect(run(ctx, remove())).To(Succeed(), "the retry")
			Expect(members(ctx)).To(BeEmpty())
			Expect(removeAccount(ctx)).To(Succeed())
		})
	})

	It("holds off the account's removal with an entry whose user does not name the account yet", func(ctx SpecContext) {
		newUser(ctx, "alpha", "")
		Expect(store.AddAccountUser(ctx, acct, stored(ctx))).To(Succeed(), "a join in flight: the entry, not yet the user")
		Expect(removeAccount(ctx)).To(MatchError(op.ErrBucketNotEmpty), "any user an entry names counts, as list_account_users does")
	})
})
