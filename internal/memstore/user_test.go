package memstore_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

func aliceInfo() meta.UserInfo {
	u := meta.NewUserInfo()
	u.UserID = meta.UserID{ID: "alice"}
	u.Email = "Alice@Example.com"
	u.AccessKeys = map[string]meta.AccessKey{"AKALICE": {ID: "AKALICE", Secret: "s", Active: true}}
	return u
}

var _ = Describe("users", func() {
	var (
		store *memstore.Store
		clk   *clock
	)
	BeforeEach(func() { store, clk = newStore() })

	It("adds a user with a fresh version and index entries", func(ctx SpecContext) {
		rec := store.AddUser(aliceInfo())
		Expect(rec.Version.Ver).To(BeEquivalentTo(1), "version")
		Expect(rec.Version.Tag).To(MatchRegexp(tagPattern), "tag")
		Expect(rec.Mtime).To(Equal(start), "mtime")
		got, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(rec))
	})
	It("fills an access key's empty ID from its map key", func() {
		info := aliceInfo()
		info.AccessKeys = map[string]meta.AccessKey{"AKALICE": {Secret: "s"}}
		Expect(store.AddUser(info).Info.AccessKeys["AKALICE"].ID).To(Equal("AKALICE"))
	})
	It("reports a missing user as NoSuchUser", func(ctx SpecContext) {
		_, err := store.GetUser(ctx, meta.UserID{ID: "nobody"})
		Expect(err).To(MatchError(op.ErrNoSuchUser), "by id")
		_, err = store.GetUserByAccessKey(ctx, "AKNOBODY")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "by access key")
		_, err = store.GetUserByEmail(ctx, "nobody@example.com")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "by email")
	})
	It("resolves access keys exactly and emails case-insensitively, as radosgw lowercases the email index", func(ctx SpecContext) {
		store.AddUser(aliceInfo())
		byKey, err := store.GetUserByAccessKey(ctx, "AKALICE")
		Expect(err).NotTo(HaveOccurred())
		Expect(byKey.Info.UserID.ID).To(Equal("alice"), "by access key")
		_, err = store.GetUserByAccessKey(ctx, "akalice")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "an access key is case-sensitive")
		byEmail, err := store.GetUserByEmail(ctx, "aLICE@example.COM")
		Expect(err).NotTo(HaveOccurred())
		Expect(byEmail.Info.UserID.ID).To(Equal("alice"), "by email")
	})
	It("creates a missing user at version 1", func(ctx SpecContext) {
		rec := &op.UserRecord{Info: aliceInfo()}
		Expect(store.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed())
		Expect(rec.Version.Ver).To(BeEquivalentTo(1), "version")
		Expect(rec.Version.Tag).To(MatchRegexp(tagPattern), "tag")
		Expect(rec.Mtime).To(Equal(start), "mtime")
		got, err := store.GetUserByAccessKey(ctx, "AKALICE")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(rec))
	})
	It("refuses an exclusive put of an existing user", func(ctx SpecContext) {
		store.AddUser(aliceInfo())
		Expect(store.PutUser(ctx, &op.UserRecord{Info: aliceInfo()}, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrUserAlreadyExists))
	})
	It("refuses a put under a version other than the stored one", func(ctx SpecContext) {
		rec := store.AddUser(aliceInfo())
		stale := rec.Version
		Expect(store.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &stale})).To(Succeed(), "current version")
		Expect(store.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &stale})).To(MatchError(op.ErrConcurrentModification), "stale version")
		other := meta.ObjVersion{Ver: rec.Version.Ver, Tag: "other"}
		Expect(store.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &other})).To(MatchError(op.ErrConcurrentModification), "same count, other tag")
	})
	It("writes a fresh version without IfVersion, as PutOperation::prepare generates one", func(ctx SpecContext) {
		first := store.AddUser(aliceInfo()).Version
		rec := &op.UserRecord{Info: aliceInfo()}
		Expect(store.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
		Expect(rec.Version.Ver).To(BeEquivalentTo(1), "a fresh count")
		Expect(rec.Version.Tag).To(MatchRegexp(tagPattern), "a fresh tag")
		Expect(rec.Version.Tag).NotTo(Equal(first.Tag), "not the stored tag")
		got, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Version).To(Equal(rec.Version), "stored")
	})
	It("writes the read version plus one under IfVersion and rewrites the indexes", func(ctx SpecContext) {
		rec := store.AddUser(aliceInfo())
		first := rec.Version
		clk.t = start.Add(time.Minute)
		rec.Info.Email = "alice@new.example"
		rec.Info.AccessKeys = map[string]meta.AccessKey{"AKNEW": {ID: "AKNEW", Secret: "t", Active: true}}
		Expect(store.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &first})).To(Succeed())
		Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: first.Ver + 1, Tag: first.Tag}), "version")
		Expect(rec.Mtime).To(Equal(clk.t), "mtime")
		_, err := store.GetUserByAccessKey(ctx, "AKALICE")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "the old key")
		_, err = store.GetUserByEmail(ctx, "alice@example.com")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "the old email")
		got, err := store.GetUserByAccessKey(ctx, "AKNEW")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(rec), "the new key")
		got, err = store.GetUserByEmail(ctx, "alice@new.example")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(rec), "the new email")
	})
	It("refuses an active access key another user holds with KeyExists, before writing anything", func(ctx SpecContext) {
		store.AddUser(aliceInfo())
		bob := meta.NewUserInfo()
		bob.UserID = meta.UserID{ID: "bob"}
		bob.AccessKeys = map[string]meta.AccessKey{"AKALICE": {ID: "AKALICE", Secret: "x", Active: true}}
		Expect(store.PutUser(ctx, &op.UserRecord{Info: bob}, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrKeyExists), "active")
		_, err := store.GetUser(ctx, meta.UserID{ID: "bob"})
		Expect(err).To(MatchError(op.ErrNoSuchUser), "bob was not written")
		bob.AccessKeys["AKALICE"] = meta.AccessKey{ID: "AKALICE", Secret: "x"}
		Expect(store.PutUser(ctx, &op.UserRecord{Info: bob}, op.PutUserOptions{Exclusive: true})).To(Succeed(), "an inactive key is not checked")
		holder, err := store.GetUserByAccessKey(ctx, "AKALICE")
		Expect(err).NotTo(HaveOccurred())
		Expect(holder.Info.UserID.ID).To(Equal("alice"), "the index still names alice")
	})
	It("indexes active access keys only, dropping a key's index when it goes inactive", func(ctx SpecContext) {
		info := aliceInfo()
		info.AccessKeys["AKOFF"] = meta.AccessKey{ID: "AKOFF", Secret: "s"}
		rec := &op.UserRecord{Info: info}
		Expect(store.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed())
		_, err := store.GetUserByAccessKey(ctx, "AKOFF")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "an inactive key has no index")
		rec.Info.AccessKeys["AKALICE"] = meta.AccessKey{ID: "AKALICE", Secret: "s"}
		Expect(store.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
		_, err = store.GetUserByAccessKey(ctx, "AKALICE")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "a deactivated key loses its index")
	})
	It("hands out copies the caller may change freely", func(ctx SpecContext) {
		rec := store.AddUser(aliceInfo())
		rec.Info.AccessKeys["AKSTOLEN"] = meta.AccessKey{ID: "AKSTOLEN"}
		rec.Info.Email = "changed@example.com"
		got, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Info.AccessKeys).To(HaveLen(1), "keys")
		Expect(got.Info.Email).To(Equal("Alice@Example.com"), "email")
		delete(got.Info.AccessKeys, "AKALICE")
		_, err = store.GetUserByAccessKey(ctx, "AKALICE")
		Expect(err).NotTo(HaveOccurred(), "the index survives a change to a returned copy")
	})
	It("removes a user and its indexes", func(ctx SpecContext) {
		rec := store.AddUser(aliceInfo())
		Expect(store.RemoveUser(ctx, rec)).To(Succeed())
		_, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).To(MatchError(op.ErrNoSuchUser), "by id")
		_, err = store.GetUserByAccessKey(ctx, "AKALICE")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "by access key")
		_, err = store.GetUserByEmail(ctx, "alice@example.com")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "by email")
		Expect(store.RemoveUser(ctx, rec)).To(MatchError(op.ErrNoSuchUser), "again")
	})
	It("lists the owner's buckets sorted by name, paged after a marker", func(ctx SpecContext) {
		for _, name := range []string{"c", "a", "b"} {
			mustCreate(ctx, store, "", name, owner("alice"))
		}
		mustCreate(ctx, store, "", "x", owner("bob"))
		names := func(ents []meta.BucketEnt) []string {
			var out []string
			for _, e := range ents {
				out = append(out, e.Bucket.Name)
			}
			return out
		}
		ents, next, more, err := store.ListUserBuckets(ctx, owner("alice"), "", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(ents)).To(Equal([]string{"a", "b"}), "first page")
		Expect(next).To(Equal("b"), "first page's next")
		Expect(more).To(BeTrue(), "first page leaves c")
		ents, next, more, err = store.ListUserBuckets(ctx, owner("alice"), next, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(ents)).To(Equal([]string{"c"}), "second page")
		Expect(next).To(Equal("c"), "second page's next")
		Expect(more).To(BeFalse(), "second page is the last")
		ents, next, more, err = store.ListUserBuckets(ctx, owner("carol"), "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{ents, next, more}).To(Equal([]any{[]meta.BucketEnt(nil), "", false}), "an owner with no buckets")
	})
	It("fills each bucket entry's creation time, size and count from the bucket", func(ctx SpecContext) {
		rec := mustCreate(ctx, store, "", "a", owner("alice"))
		mustPut(ctx, store, rec, "k1", "12345")
		mustPut(ctx, store, rec, "k2", "")
		ents, _, _, err := store.ListUserBuckets(ctx, owner("alice"), "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(Equal([]meta.BucketEnt{{
			Bucket:        rec.Info.Bucket,
			Size:          5,
			SizeRounded:   4096,
			CreationTime:  meta.Time{Time: start},
			Count:         2,
			PlacementRule: meta.PlacementRule{Name: "default-placement"},
		}}))
	})
})
