package driver_test

import (
	"context"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("the key holder index", func() {
	const holdersNS = "users.keys.rgw-go-key-holders"
	var (
		c *fakerados.Cluster
		s *driver.Store
	)
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = newIndexCluster()
		st, err := driver.Open(ctx, c, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		s = st
	})
	userWith := func(id string, keys ...meta.AccessKey) meta.UserInfo {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: id}
		u.AccessKeys = map[string]meta.AccessKey{}
		for _, k := range keys {
			u.AccessKeys[k.ID] = k
		}
		return u
	}
	put := func(ctx context.Context, u meta.UserInfo) error {
		cur, err := s.GetUser(ctx, u.UserID)
		if err != nil {
			return s.PutUser(ctx, &op.UserRecord{Info: u}, op.PutUserOptions{Exclusive: true})
		}
		return s.PutUser(ctx, &op.UserRecord{Info: u}, op.PutUserOptions{IfVersion: &cur.Version})
	}
	holderOf := func(key string) string {
		GinkgoHelper()
		o := c.Object(rookMetaPool, holdersNS, key)
		if o == nil {
			return ""
		}
		d := denc.NewDecoder(o.Data)
		uid := meta.DecodeUID(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return string(uid)
	}

	It("claims every access key, active or not, before it writes the user", func(ctx SpecContext) {
		var claimed []string
		c.BeforeWrite(rookMetaPool, "users.uid", "alice", func(*fakerados.Object) {
			claimed = []string{holderOf("AKON"), holderOf("AKOFF")}
		})
		Expect(put(ctx, userWith("alice",
			meta.AccessKey{ID: "AKON", Secret: "s", Active: true},
			meta.AccessKey{ID: "AKOFF", Secret: "s", Active: false}))).To(Succeed())
		Expect(claimed).To(Equal([]string{"alice", "alice"}), "both keys claimed when the user is written")
		Expect(c.Object(rookMetaPool, "users.keys", "AKOFF")).To(BeNil(), "radosgw's index names active keys alone")
		holder, err := s.FindKeyHolder(ctx, "AKOFF")
		Expect(err).NotTo(HaveOccurred())
		Expect(holder).To(Equal(meta.UserID{ID: "alice"}))
	})

	It("refuses the second of two writes claiming one key id, writing nothing for it", func(ctx SpecContext) {
		c.BeforeWrite(rookMetaPool, holdersNS, "AKRACE", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, holdersNS, "AKRACE", nil)
			Expect(put(ctx, userWith("carol", meta.AccessKey{ID: "AKRACE", Secret: "c", Active: false}))).To(Succeed())
		})
		err := put(ctx, userWith("bob", meta.AccessKey{ID: "AKRACE", Secret: "b", Active: false}))
		Expect(err).To(MatchError(op.ErrKeyExists))
		Expect(holderOf("AKRACE")).To(Equal("carol"))
		Expect(c.Object(rookMetaPool, "users.uid", "bob")).To(BeNil())
	})

	It("releases a dropped key and every key of a removed user, but never another user's entry", func(ctx SpecContext) {
		Expect(put(ctx, userWith("alice",
			meta.AccessKey{ID: "AKA", Secret: "s", Active: false},
			meta.AccessKey{ID: "AKB", Secret: "s", Active: true}))).To(Succeed())
		Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKB", Secret: "s", Active: true}))).To(Succeed())
		Expect(holderOf("AKA")).To(BeEmpty(), "the dropped key")
		Expect(holderOf("AKB")).To(Equal("alice"))
		rec, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.RemoveUser(ctx, rec)).To(Succeed())
		Expect(holderOf("AKB")).To(BeEmpty(), "the removed user's key")

		seedUser(c, userWith("dave", meta.AccessKey{ID: "AKX", Secret: "s", Active: true}), nil, meta.ObjVersion{Ver: 1, Tag: "_d"})
		c.Put(rookMetaPool, holdersNS, "AKX", encode(meta.UID("erin")))
		Expect(put(ctx, userWith("dave", meta.AccessKey{ID: "AKX", Secret: "rotated", Active: true}))).To(Succeed(),
			"a key dave holds and keeps")
		Expect(put(ctx, userWith("dave", meta.AccessKey{ID: "AKX", Secret: "rotated", Active: false}))).To(Succeed(),
			"a revocation always goes through")
		Expect(put(ctx, userWith("dave", meta.AccessKey{ID: "AKX", Secret: "rotated", Active: true}))).To(MatchError(op.ErrKeyExists),
			"a reactivation gains the key")
		Expect(put(ctx, userWith("dave"))).To(Succeed())
		Expect(holderOf("AKX")).To(Equal("erin"), "another user's entry stays")
	})

	It("refuses the id of an inactive key a user radosgw wrote holds, which no entry records", func(ctx SpecContext) {
		DeferCleanup(driver.SetHolderWalkPageForTest(1))
		for _, id := range []string{"f1", "f2", "f3", "f4", "f5"} {
			seedUser(c, userWith(id), nil, meta.ObjVersion{Ver: 1, Tag: "_" + id})
		}
		seedUser(c, userWith("frank", meta.AccessKey{ID: "AKOLD", Secret: "s", Active: false}), nil, meta.ObjVersion{Ver: 1, Tag: "_f"})
		Expect(c.Object(rookMetaPool, holdersNS, "AKOLD")).To(BeNil(), "radosgw writes no holder entry")
		Expect(c.Object(rookMetaPool, "users.keys", "AKOLD")).To(BeNil(), "and indexes no inactive key")
		holder, err := s.FindKeyHolder(ctx, "AKOLD")
		Expect(err).NotTo(HaveOccurred())
		Expect(holder).To(Equal(meta.UserID{ID: "frank"}), "found by reading every page of users")
		for _, active := range []bool{false, true} {
			err = put(ctx, userWith("gail", meta.AccessKey{ID: "AKOLD", Secret: "g", Active: active}))
			Expect(err).To(MatchError(op.ErrKeyExists), "active %t", active)
			Expect(err.Error()).NotTo(ContainSubstring("AKOLD"))
		}
		Expect(c.Object(rookMetaPool, "users.uid", "gail")).To(BeNil())
		Expect(holderOf("AKOLD")).To(BeEmpty(), "gail claimed nothing")
		rec, err := s.GetUser(ctx, meta.UserID{ID: "frank"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &rec.Version})).To(Succeed())
		Expect(holderOf("AKOLD")).To(Equal("frank"), "frank's next write claims it")
	})

	It("refuses a key when a listed user cannot be read, naming no credential", func(ctx SpecContext) {
		seedUser(c, userWith("frank", meta.AccessKey{ID: "AKOLD", Secret: "s", Active: false}), nil, meta.ObjVersion{Ver: 1, Tag: "_f"})
		c.FailNextRead(rookMetaPool, "users.uid", "frank", syscall.EIO)
		err := put(ctx, userWith("gail", meta.AccessKey{ID: "AKOLD", Secret: "g", Active: false}))
		Expect(err).To(HaveOccurred())
		Expect(err).NotTo(MatchError(op.ErrNoSuchUser))
		Expect(err.Error()).NotTo(ContainSubstring("AKOLD"))
		Expect(c.Object(rookMetaPool, "users.uid", "gail")).To(BeNil())
		Expect(holderOf("AKOLD")).To(BeEmpty())
	})

	It("gives exactly one of two concurrent creates of one key id", func(ctx SpecContext) {
		results := make(chan error, 2)
		for _, id := range []string{"lea", "max"} {
			go func() {
				defer GinkgoRecover()
				results <- put(ctx, userWith(id, meta.AccessKey{ID: "AKBOTH", Secret: id, Active: false}))
			}()
		}
		errs := []error{<-results, <-results}
		Expect(errs).To(ContainElement(BeNil()))
		Expect(errs).To(ContainElement(MatchError(op.ErrKeyExists)))
		holder := holderOf("AKBOTH")
		Expect(holder).To(BeElementOf("lea", "max"))
		loser := map[string]string{"lea": "max", "max": "lea"}[holder]
		Expect(c.Object(rookMetaPool, "users.uid", loser)).To(BeNil(), "the loser wrote no user")
	})

	It("finds the holder radosgw's index names for an active key it wrote", func(ctx SpecContext) {
		seedUser(c, userWith("hank", meta.AccessKey{ID: "AKRGW", Secret: "s", Active: true}), nil, meta.ObjVersion{Ver: 1, Tag: "_h"})
		holder, err := s.FindKeyHolder(ctx, "AKRGW")
		Expect(err).NotTo(HaveOccurred())
		Expect(holder).To(Equal(meta.UserID{ID: "hank"}))
		Expect(put(ctx, userWith("ivy", meta.AccessKey{ID: "AKRGW", Secret: "i", Active: false}))).To(MatchError(op.ErrKeyExists),
			"an inactive key over radosgw's active one")
	})

	It("takes no entry from a user that no longer holds the key", func(ctx SpecContext) {
		c.Put(rookMetaPool, holdersNS, "AKSTALE", encode(meta.UID("ghost")))
		Expect(put(ctx, userWith("jill", meta.AccessKey{ID: "AKSTALE", Secret: "j", Active: true}))).To(MatchError(op.ErrKeyExists))
		Expect(holderOf("AKSTALE")).To(Equal("ghost"))
	})

	It("reads an entry in an account id's form as a holder, as a claim reads it", func(ctx SpecContext) {
		c.Put(rookMetaPool, holdersNS, "AKACCT", encode(meta.UID("RGW00000000000000001")))
		holder, err := s.FindKeyHolder(ctx, "AKACCT")
		Expect(err).NotTo(HaveOccurred())
		Expect(holder).To(Equal(meta.ParseUserID("RGW00000000000000001")))
		Expect(put(ctx, userWith("lisa", meta.AccessKey{ID: "AKACCT", Secret: "l", Active: true}))).To(MatchError(op.ErrKeyExists))
	})

	It("lets a deactivation and a suspension through when the index cannot be read or written, and walks no user", func(ctx SpecContext) {
		Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKREV", Secret: "s", Active: true}))).To(Succeed())
		c.FailNextRead(rookMetaPool, holdersNS, "AKREV", syscall.EIO)
		Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKREV", Secret: "s", Active: false}))).To(Succeed(), "a deactivation")
		suspended := userWith("alice", meta.AccessKey{ID: "AKREV", Secret: "s", Active: false})
		suspended.Suspended = 1
		c.FailNextRead(rookMetaPool, holdersNS, "AKREV", syscall.EIO)
		Expect(put(ctx, suspended)).To(Succeed(), "a suspension")
		rec, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.Suspended).To(Equal(uint8(1)))
		Expect(rec.Info.AccessKeys["AKREV"].Active).To(BeFalse())

		seedUser(c, userWith("frank", meta.AccessKey{ID: "AKRGW2", Secret: "s", Active: true}), nil, meta.ObjVersion{Ver: 1, Tag: "_f"})
		seedUser(c, userWith("zed"), nil, meta.ObjVersion{Ver: 1, Tag: "_z"})
		c.ResetCounters()
		c.FailNextWrite(rookMetaPool, holdersNS, "AKRGW2", syscall.EIO)
		Expect(put(ctx, userWith("frank", meta.AccessKey{ID: "AKRGW2", Secret: "s", Active: false}))).To(Succeed(),
			"a deactivation of a key no entry records, whose entry cannot be created")
		Expect(c.Reads(rookMetaPool, "users.uid", "zed")).To(BeZero(), "no walk of the users for a key the user keeps")
		Expect(put(ctx, userWith("frank", meta.AccessKey{ID: "AKRGW2", Secret: "s", Active: false}))).To(Succeed())
		Expect(holderOf("AKRGW2")).To(Equal("frank"), "a later write claims it, best effort")
	})

	It("keeps the entry of a key a release reads just before a write of the user touches it", func(ctx SpecContext) {
		Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKREL", Secret: "s", Active: true}))).To(Succeed())
		c.BeforeWrite(rookMetaPool, holdersNS, "AKREL", func(o *fakerados.Object) {
			c.BeforeWrite(rookMetaPool, holdersNS, "AKREL", nil)
			Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKREL", Secret: "s", Active: true}))).To(Succeed(),
				"the key added back between the release's read and its removal")
		})
		Expect(put(ctx, userWith("alice"))).To(Succeed(), "the write that drops the key and releases it")
		Expect(holderOf("AKREL")).To(Equal("alice"), "the release's version check fails, so the entry stays")
		rec, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.AccessKeys).To(HaveKey("AKREL"))
	})

	It("creates a gained key's entry again when a release removes it after the claim and before the user is written", func(ctx SpecContext) {
		c.BeforeWrite(rookMetaPool, "users.uid", "alice", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, "users.uid", "alice", nil)
			c.Remove(rookMetaPool, holdersNS, "AKLATE")
		})
		Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKLATE", Secret: "s", Active: false}))).To(Succeed())
		Expect(holderOf("AKLATE")).To(Equal("alice"), "the write reads its entry again once the user is written")
	})

	It("releases the entry it creates again when a later write of the user dropped the key before the re-read", func(ctx SpecContext) {
		c.AfterWrite(rookMetaPool, "users.uid", "alice", func() {
			c.AfterWrite(rookMetaPool, "users.uid", "alice", nil)
			Expect(put(ctx, userWith("alice"))).To(Succeed(), "a second write drops the key and releases it")
		})
		Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKGONE", Secret: "s", Active: false}))).To(Succeed())
		Expect(holderOf("AKGONE")).To(BeEmpty(), "no entry names alice, who no longer holds the key")
		Expect(put(ctx, userWith("bob", meta.AccessKey{ID: "AKGONE", Secret: "b", Active: false}))).To(Succeed(), "the id is free")
	})

	It("claims a key again whose entry a release removes between the claim's read and its touch", func(ctx SpecContext) {
		Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKGAP", Secret: "s", Active: false}))).To(Succeed())
		c.BeforeWrite(rookMetaPool, holdersNS, "AKGAP", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, holdersNS, "AKGAP", nil)
			c.Remove(rookMetaPool, holdersNS, "AKGAP")
		})
		Expect(put(ctx, userWith("alice", meta.AccessKey{ID: "AKGAP", Secret: "s", Active: true}))).To(Succeed(),
			"a reactivation, which gains the key")
		Expect(holderOf("AKGAP")).To(Equal("alice"), "created again once the touch finds it gone")
	})

	It("names the index by its kind, never by the key, in errors", func(ctx SpecContext) {
		c.FailNextRead(rookMetaPool, holdersNS, "AKSECRETID", syscall.EIO)
		err := put(ctx, userWith("kate", meta.AccessKey{ID: "AKSECRETID", Secret: "k", Active: true}))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("user key holder index"))
		Expect(err.Error()).NotTo(ContainSubstring("AKSECRETID"))
		_, err = s.FindKeyHolder(ctx, "AKSECRETID")
		Expect(err).To(MatchError(op.ErrNoSuchUser))
	})
})

var _ = Describe("the email index claim", func() {
	var (
		c *fakerados.Cluster
		s *driver.Store
	)
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = newIndexCluster()
		st, err := driver.Open(ctx, c, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		s = st
	})
	userWith := func(id, email string) meta.UserInfo {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: id}
		u.Email = email
		return u
	}
	put := func(ctx context.Context, u meta.UserInfo) error {
		cur, err := s.GetUser(ctx, u.UserID)
		if err != nil {
			return s.PutUser(ctx, &op.UserRecord{Info: u}, op.PutUserOptions{})
		}
		return s.PutUser(ctx, &op.UserRecord{Info: u}, op.PutUserOptions{IfVersion: &cur.Version})
	}
	indexNames := func(email string) string {
		GinkgoHelper()
		o := c.Object(rookMetaPool, "users.email", email)
		if o == nil {
			return ""
		}
		d := denc.NewDecoder(o.Data)
		uid := meta.DecodeUID(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return string(uid)
	}

	It("claims a new email before it writes the user", func(ctx SpecContext) {
		var named string
		c.BeforeWrite(rookMetaPool, "users.uid", "alice", func(*fakerados.Object) { named = indexNames("alice@example.com") })
		Expect(put(ctx, userWith("alice", "Alice@Example.com"))).To(Succeed())
		Expect(named).To(Equal("alice"))
	})

	It("refuses the second of two writes giving one email to two users, writing neither its user nor its index", func(ctx SpecContext) {
		Expect(put(ctx, userWith("bob", ""))).To(Succeed())
		c.BeforeWrite(rookMetaPool, "users.email", "shared@example.com", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, "users.email", "shared@example.com", nil)
			Expect(put(ctx, userWith("carol", "shared@example.com"))).To(Succeed())
		})
		err := put(ctx, userWith("bob", "SHARED@example.com"))
		Expect(err).To(MatchError(op.ErrEmailExists))
		Expect(err.Error()).NotTo(ContainSubstring("shared"))
		Expect(indexNames("shared@example.com")).To(Equal("carol"))
		rec, err := s.GetUser(ctx, meta.UserID{ID: "bob"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.Email).To(BeEmpty())
	})

	It("takes an index naming the user as its own, and refuses one naming an account or another user", func(ctx SpecContext) {
		c.Put(rookMetaPool, "users.email", "dana@example.com", encode(meta.UID("dana")))
		Expect(put(ctx, userWith("dana", "dana@example.com"))).To(Succeed(), "a write that stopped before the user")
		c.Put(rookMetaPool, "users.email", "acct@example.com", encode(meta.UID("RGW00000000000000001")))
		Expect(put(ctx, userWith("erin", "acct@example.com"))).To(MatchError(op.ErrEmailExists))
		c.Put(rookMetaPool, "users.email", "ghost@example.com", encode(meta.UID("ghost")))
		Expect(put(ctx, userWith("erin", "ghost@example.com"))).To(MatchError(op.ErrEmailExists),
			"another user's index, whether or not that user still exists")
		Expect(c.Object(rookMetaPool, "users.uid", "erin")).To(BeNil())
	})

	It("removes a shared email's index only while it names the departing user, so an account that takes it keeps it", func(ctx SpecContext) {
		shared := op.PutUserOptions{Exclusive: true, SharedEmail: true}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("alice", "shared@example.com")}, shared)).To(Succeed())
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("bob", "shared@example.com")}, shared)).To(Succeed())
		Expect(indexNames("shared@example.com")).To(Equal("bob"))
		bob, err := s.GetUser(ctx, meta.UserID{ID: "bob"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.RemoveUser(ctx, bob)).To(Succeed())
		Expect(c.Object(rookMetaPool, "users.email", "shared@example.com")).To(BeNil(), "bob's index goes, alice is left unindexed")
		acct := &op.AccountRecord{Info: meta.AccountInfo{ID: "RGW00000000000000001", Name: "acme", Email: "shared@example.com"}}
		Expect(s.PutAccount(ctx, acct, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		alice, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		alice.Info.Email = "alice@example.com"
		Expect(s.PutUser(ctx, alice, op.PutUserOptions{IfVersion: &alice.Version})).To(Succeed())
		Expect(indexNames("shared@example.com")).To(Equal("RGW00000000000000001"), "alice's change leaves the account's index")
		alice, err = s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.RemoveUser(ctx, alice)).To(Succeed())
		Expect(indexNames("alice@example.com")).To(BeEmpty())
		Expect(indexNames("shared@example.com")).To(Equal("RGW00000000000000001"))
	})

	It("never writes a shared email's index over an account that took it after the claim read it", func(ctx SpecContext) {
		shared := op.PutUserOptions{Exclusive: true, SharedEmail: true}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("bob", "shared@example.com")}, shared)).To(Succeed())
		c.BeforeWrite(rookMetaPool, "users.uid", "alice", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, "users.uid", "alice", nil)
			bob, err := s.GetUser(ctx, meta.UserID{ID: "bob"})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.RemoveUser(ctx, bob)).To(Succeed(), "bob leaves after alice's claim read his index")
			acct := &op.AccountRecord{Info: meta.AccountInfo{ID: "RGW00000000000000001", Name: "acme", Email: "shared@example.com"}}
			Expect(s.PutAccount(ctx, acct, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed(), "and an account takes the email")
		})
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("alice", "shared@example.com")}, shared)).To(MatchError(op.ErrEmailExists))
		Expect(indexNames("shared@example.com")).To(Equal("RGW00000000000000001"), "the account keeps its index")
		_, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred(), "alice is written, holding the email unindexed")
	})

	It("finishes the user's indexes and releases before it answers a shared email's 409", func(ctx SpecContext) {
		shared := op.PutUserOptions{SharedEmail: true}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("bob", "shared@example.com")}, op.PutUserOptions{Exclusive: true, SharedEmail: true})).To(Succeed())
		alice := userWith("alice", "")
		alice.AccessKeys = map[string]meta.AccessKey{"AKTWO": {ID: "AKTWO", Secret: "s", Active: true}}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: alice}, op.PutUserOptions{Exclusive: true})).To(Succeed())
		c.BeforeWrite(rookMetaPool, "users.uid", "alice", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, "users.uid", "alice", nil)
			bob, err := s.GetUser(ctx, meta.UserID{ID: "bob"})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.RemoveUser(ctx, bob)).To(Succeed())
			acct := &op.AccountRecord{Info: meta.AccountInfo{ID: "RGW00000000000000001", Name: "acme", Email: "shared@example.com"}}
			Expect(s.PutAccount(ctx, acct, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		})
		cur, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		next := userWith("alice", "shared@example.com")
		next.AccessKeys = map[string]meta.AccessKey{"AKONE": {ID: "AKONE", Secret: "s", Active: true}}
		shared.IfVersion = &cur.Version
		Expect(s.PutUser(ctx, &op.UserRecord{Info: next}, shared)).To(MatchError(op.ErrEmailExists))
		got, err := s.GetUserByAccessKey(ctx, "AKONE")
		Expect(err).NotTo(HaveOccurred(), "the gained key's index is written, so it authenticates")
		Expect(got.Info.UserID).To(Equal(meta.UserID{ID: "alice"}))
		holder, err := s.FindKeyHolder(ctx, "AKTWO")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "the dropped key's entry is released")
		Expect(holder).To(BeZero())
		Expect(c.Object(rookMetaPool, "users.keys", "AKTWO")).To(BeNil(), "and its index removed")
	})

	It("writes a shared email's index over another sharer's that changed after the claim read it", func(ctx SpecContext) {
		shared := op.PutUserOptions{Exclusive: true, SharedEmail: true}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("bob", "shared@example.com")}, shared)).To(Succeed())
		c.BeforeWrite(rookMetaPool, "users.uid", "alice", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, "users.uid", "alice", nil)
			Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("carol", "shared@example.com")}, shared)).To(Succeed())
		})
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("alice", "shared@example.com")}, shared)).To(Succeed())
		Expect(indexNames("shared@example.com")).To(Equal("alice"), "claimed again from carol's index")
	})

	It("keeps an email's index another sharer writes between a removal's read and its removal", func(ctx SpecContext) {
		shared := op.PutUserOptions{Exclusive: true, SharedEmail: true}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("bob", "shared@example.com")}, shared)).To(Succeed())
		c.BeforeWrite(rookMetaPool, "users.email", "shared@example.com", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, "users.email", "shared@example.com", nil)
			Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("carol", "shared@example.com")}, shared)).To(Succeed())
		})
		bob, err := s.GetUser(ctx, meta.UserID{ID: "bob"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.RemoveUser(ctx, bob)).To(Succeed())
		Expect(indexNames("shared@example.com")).To(Equal("carol"))
	})

	It("writes over another user's index after the user when users may share an email, but never an account's", func(ctx SpecContext) {
		c.Put(rookMetaPool, "users.email", "shared@example.com", encode(meta.UID("carol")))
		var before string
		c.BeforeWrite(rookMetaPool, "users.uid", "bob", func(*fakerados.Object) { before = indexNames("shared@example.com") })
		shared := op.PutUserOptions{SharedEmail: true}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("bob", "shared@example.com")}, shared)).To(Succeed())
		Expect(before).To(Equal("carol"), "radosgw writes the index after the user")
		Expect(indexNames("shared@example.com")).To(Equal("bob"))
		c.Put(rookMetaPool, "users.email", "acct@example.com", encode(meta.UID("RGW00000000000000001")))
		Expect(s.PutUser(ctx, &op.UserRecord{Info: userWith("erin", "acct@example.com")}, shared)).To(MatchError(op.ErrEmailExists))
		Expect(indexNames("acct@example.com")).To(Equal("RGW00000000000000001"))
	})
})
