package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("AccountStore", func() {
	const (
		id1        = "RGW00000000000000001"
		id2        = "RGW00000000000000002"
		id3        = "RGW00000000000000003"
		accountsNS = "accounts"
		emailNS    = "users.email"
		writeTag   = `^_[A-Za-z0-9_-]{23}$`
	)
	var (
		c    *fakerados.Cluster
		s    *driver.Store
		logs *bytes.Buffer
	)
	BeforeEach(func(ctx SpecContext) {
		logs = &bytes.Buffer{}
		DeferCleanup(driver.CaptureLog(logs))
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		c.RegisterClass("user", fakerados.UserClass(), fakerados.UserWriteMethods...)
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
	})
	newAccount := func(id, tenant, name, email string) meta.AccountInfo {
		a := meta.NewAccountInfo()
		a.ID, a.Tenant, a.Name, a.Email = id, tenant, name, email
		return a
	}
	object := func(ns, oid string) *fakerados.Object { return c.Object(rookMetaPool, ns, oid) }
	exists := func(ns, oid string) bool { return object(ns, oid) != nil }
	redirectTo := func(ns, oid string) string {
		GinkgoHelper()
		o := object(ns, oid)
		Expect(o).NotTo(BeNil(), "%s/%s", ns, oid)
		d := denc.NewDecoder(o.Data)
		uid := meta.DecodeUID(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return string(uid)
	}
	create := func(ctx context.Context, info meta.AccountInfo) *op.AccountRecord {
		GinkgoHelper()
		rec := &op.AccountRecord{Info: info}
		Expect(s.PutAccount(ctx, rec, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		return rec
	}
	holders := func(ctx context.Context, name string) []string {
		GinkgoHelper()
		var out []string
		for _, id := range []string{id1, id2, id3} {
			rec, err := s.GetAccount(ctx, id)
			if err == nil && rec.Info.Name == name {
				out = append(out, id)
			}
		}
		return out
	}
	modify := func(ctx context.Context, id string, change func(*meta.AccountInfo)) (*op.AccountRecord, error) {
		GinkgoHelper()
		rec, err := s.GetAccount(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		old := rec.Info
		change(&rec.Info)
		return rec, s.PutAccount(ctx, rec, &old, op.PutAccountOptions{})
	}

	Describe("writing and reading", func() {
		It("creates an account with its name and email redirects", func(ctx SpecContext) {
			rec := &op.AccountRecord{Info: newAccount(id1, "t", "acme", "Ops@Acme.example"), Attrs: map[string][]byte{"user.rgw.x": []byte("v")}}
			Expect(s.PutAccount(ctx, rec, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
			Expect(rec.Version.Ver).To(BeEquivalentTo(2), "the nameless first write, then the whole account under its version")
			Expect(rec.Version.Tag).To(MatchRegexp(writeTag), "generate_new_write_ver, rgw_account.cc:163-164")
			Expect(object(accountsNS, "account."+id1).Data).To(Equal(encode(rec.Info)))
			got, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info).To(Equal(rec.Info))
			Expect(got.Attrs).To(HaveKeyWithValue("user.rgw.x", []byte("v")))
			Expect(got.Version).To(Equal(rec.Version))
			Expect(got.Mtime).To(BeTemporally("==", rec.Mtime))
			Expect(redirectTo(accountsNS, "name.t$acme")).To(Equal(id1), "account.cc:92-100, :353-365")
			Expect(redirectTo(emailNS, "ops@acme.example")).To(Equal(id1), "lower-cased, account.cc:103-114")
			byName, err := s.GetAccountByName(ctx, "t", "acme")
			Expect(err).NotTo(HaveOccurred())
			Expect(byName.Info.ID).To(Equal(id1))
			byEmail, err := s.GetAccountByEmail(ctx, "OPS@acme.example")
			Expect(err).NotTo(HaveOccurred())
			Expect(byEmail.Info.ID).To(Equal(id1))
			name, err := s.AccountName(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			Expect(name).To(Equal("acme"))
			Expect(s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "t", "", "")}, nil, op.PutAccountOptions{Exclusive: true})).
				To(MatchError(op.ErrAccountAlreadyExists))
		})

		It("is NoSuchEntity for a missing account, name or email, and InternalError for an object naming another id", func(ctx SpecContext) {
			_, err := s.GetAccount(ctx, id1)
			Expect(err).To(MatchError(op.ErrNoSuchEntity))
			_, err = s.AccountName(ctx, id1)
			Expect(err).To(MatchError(op.ErrNoSuchEntity))
			_, err = s.GetAccountByName(ctx, "", "nosuch")
			Expect(err).To(MatchError(op.ErrNoSuchEntity))
			_, err = s.GetAccountByEmail(ctx, "nosuch@example.com")
			Expect(err).To(MatchError(op.ErrNoSuchEntity))
			c.Put(rookMetaPool, accountsNS, "account."+id1, encode(newAccount(id2, "", "x", "")))
			_, err = s.GetAccount(ctx, id1)
			Expect(err).To(MatchError(op.ErrInternalError), "read's id mismatch is -EIO, account.cc:192-196")
			c.Put(rookMetaPool, accountsNS, "account."+id2, []byte{1})
			_, err = s.GetAccount(ctx, id2)
			Expect(err).To(MatchError(op.ErrInternalError))
		})

		It("does not read a user's email as an account", func(ctx SpecContext) {
			c.Put(rookMetaPool, emailNS, "alice@example.com", encode(meta.UID("alice")))
			_, err := s.GetAccountByEmail(ctx, "alice@example.com")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "account.cc:233-236")
			Expect(c.Reads(rookMetaPool, accountsNS, "account.alice")).To(BeZero())
		})

		It("refuses a name or email another account or a user holds, writing nothing", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "t", "acme", "ops@acme.example"))
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "t", "acme", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "account.cc:296-310")
			err = s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "other", "OPS@acme.example")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "account.cc:312-326")
			c.Put(rookMetaPool, emailNS, "alice@example.com", encode(meta.UID("alice")))
			err = s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "other", "alice@example.com")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "accounts and users share the email index")
			Expect(exists(accountsNS, "account."+id2)).To(BeFalse())
			_, err = s.GetAccountByName(ctx, "", "other")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "a name claimed before the refused email names no account that holds it")
			Expect(redirectTo(emailNS, "alice@example.com")).To(Equal("alice"))
			Expect(redirectTo(emailNS, "ops@acme.example")).To(Equal(id1))
		})

		It("moves the name and email redirects on a change, under the version it read", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "t", "acme", "ops@acme.example"))
			rec, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name, a.Email = "acme2", "OPS2@acme.example" })
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Version.Ver).To(BeEquivalentTo(3), "cls_version_inc under the read tag, past the create's two writes")
			Expect(exists(accountsNS, "name.t$acme")).To(BeFalse())
			Expect(redirectTo(accountsNS, "name.t$acme2")).To(Equal(id1))
			Expect(exists(emailNS, "ops@acme.example")).To(BeFalse())
			Expect(redirectTo(emailNS, "ops2@acme.example")).To(Equal(id1))
			_, err = s.GetAccountByName(ctx, "t", "acme")
			Expect(err).To(MatchError(op.ErrNoSuchEntity))
		})

		It("keeps the email redirect across a change of case", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", "ops@acme.example"))
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Email = "OPS@acme.example" })
			Expect(err).NotTo(HaveOccurred())
			Expect(redirectTo(emailNS, "ops@acme.example")).To(Equal(id1), "boost::iequals, account.cc:258-259: the same redirect, claimed again")
			Expect(c.Objects(rookMetaPool, emailNS)).To(HaveLen(1))
		})

		It("refuses a write under a stale version, changing no redirect", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", ""))
			stale, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			_, err = modify(ctx, id1, func(a *meta.AccountInfo) { a.MaxUsers = 5 })
			Expect(err).NotTo(HaveOccurred())
			old := stale.Info
			stale.Info.Name = "taken-by-a-stale-record"
			Expect(s.PutAccount(ctx, stale, &old, op.PutAccountOptions{})).To(MatchError(op.ErrConcurrentModification))
			_, err = s.GetAccountByName(ctx, "", "taken-by-a-stale-record")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "the claim names an account that does not hold the name")
			create(ctx, newAccount(id2, "", "taken-by-a-stale-record", ""))
			Expect(redirectTo(accountsNS, "name.$taken-by-a-stale-record")).To(Equal(id2), "and is taken over")
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id1))
			got, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.MaxUsers).To(BeEquivalentTo(5))
		})

		It("refuses a change of id", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", ""))
			old := rec.Info
			rec.Info.ID = id2
			Expect(s.PutAccount(ctx, rec, &old, op.PutAccountOptions{})).To(MatchError(op.ErrInvalidArgument), "account.cc:264-267")
			Expect(exists(accountsNS, "account."+id2)).To(BeFalse())
		})

		It("claims the redirects before the account object and drops the old ones after it", func(ctx SpecContext) {
			var order []string
			for _, o := range [][2]string{
				{accountsNS, "account." + id1},
				{accountsNS, "name.$acme"},
				{emailNS, "ops@acme.example"},
				{accountsNS, "name.$acme2"},
				{emailNS, "two@acme.example"},
			} {
				c.AfterWrite(rookMetaPool, o[0], o[1], func() { order = append(order, o[1]) })
			}
			create(ctx, newAccount(id1, "", "acme", "ops@acme.example"))
			Expect(order).To(Equal([]string{"account." + id1, "name.$acme", "ops@acme.example", "account." + id1}),
				"the nameless account, its claims, then the whole account")
			order = nil
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name, a.Email = "acme2", "two@acme.example" })
			Expect(err).NotTo(HaveOccurred())
			Expect(order).To(Equal([]string{"name.$acme2", "two@acme.example", "account." + id1, "name.$acme", "ops@acme.example"}))
		})

		It("writes over a stored account only under a version", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", ""))
			before := object(accountsNS, "account."+id1).Data
			blind := &op.AccountRecord{Info: newAccount(id1, "", "acme", "")}
			blind.Info.MaxUsers = 1
			old := blind.Info
			Expect(s.PutAccount(ctx, blind, &old, op.PutAccountOptions{})).To(MatchError(op.ErrAccountAlreadyExists), "a zero version writes only as a create")
			Expect(object(accountsNS, "account."+id1).Data).To(Equal(before))
			Expect(s.RemoveAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "", "acme", "")})).To(MatchError(op.ErrConcurrentModification))
			Expect(exists(accountsNS, "account."+id1)).To(BeTrue())
		})
	})

	Describe("recovering from a write that stopped part way", func() {
		It("leaves an old redirect a rename could not drop for a later account to take", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "name.$acme", syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "acme2" })
			Expect(err).NotTo(HaveOccurred(), "radosgw ignores the old redirect's failure, account.cc:343-352")
			Expect(redirectTo(accountsNS, "name.$acme2")).To(Equal(id1), "claimed before the account object")
			_, err = s.GetAccountByName(ctx, "", "acme")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "the old redirect names an account that no longer holds the name")
			create(ctx, newAccount(id2, "", "acme", ""))
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id2), "a stale redirect is no holder")
		})

		It("writes nothing when a claim fails, and a retry completes", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "name.$acme2", syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "acme2" })
			Expect(err).To(HaveOccurred())
			got, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Name).To(Equal("acme"), "the account holds no name without its redirect")
			_, err = modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "acme2" })
			Expect(err).NotTo(HaveOccurred(), "the retry")
			Expect(redirectTo(accountsNS, "name.$acme2")).To(Equal(id1))
		})

		It("refuses a retry whose name another account took after a failed claim, leaving one holder", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", "a@x.example"))
			c.FailNextWrite(rookMetaPool, accountsNS, "name.$acme2", syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name, a.Email = "acme2", "b@x.example" })
			Expect(err).To(HaveOccurred())
			create(ctx, newAccount(id2, "", "acme2", "b@x.example"))
			_, err = modify(ctx, id1, func(a *meta.AccountInfo) { a.Name, a.Email = "acme2", "b@x.example" })
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "never 200 for a name another account holds")
			got, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Name).To(Equal("acme"))
			Expect(got.Info.Email).To(Equal("a@x.example"))
			byName, err := s.GetAccountByName(ctx, "", "acme2")
			Expect(err).NotTo(HaveOccurred())
			Expect(byName.Info.ID).To(Equal(id2))
		})

		It("claims the redirects of an unchanged name and email on any write", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", "ops@acme.example"))
			c.Remove(rookMetaPool, accountsNS, "name.$acme")
			c.Remove(rookMetaPool, emailNS, "ops@acme.example")
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.MaxUsers = 3 })
			Expect(err).NotTo(HaveOccurred())
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id1), "a redirect a radosgw write left out is written again")
			Expect(redirectTo(emailNS, "ops@acme.example")).To(Equal(id1))
		})

		It("refuses any write of an account whose name another live account holds", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", ""))
			two := newAccount(id2, "", "acme", "")
			c.Put(rookMetaPool, accountsNS, "account."+id2, encode(two))
			object(accountsNS, "account."+id2).Xattrs[version.XattrName] = encode(meta.ObjVersion{Ver: 1, Tag: "t"})
			c.Put(rookMetaPool, accountsNS, "name.$acme", encode(meta.UID(id2)))
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.MaxUsers = 3 })
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "radosgw's two holders: never 200 for the one the redirect does not name")
			got, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.MaxUsers).To(BeEquivalentTo(meta.DefaultAccountUserLimit))
		})

		It("lets a later account take a claim whose account write stopped", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "account."+id1, syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "acme2" })
			Expect(err).To(HaveOccurred())
			Expect(redirectTo(accountsNS, "name.$acme2")).To(Equal(id1), "the claim stays")
			create(ctx, newAccount(id2, "", "acme2", ""))
			Expect(redirectTo(accountsNS, "name.$acme2")).To(Equal(id2))
			_, err = modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "acme2" })
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists))
		})

		It("fences a holder before taking its stale redirect, so the holder's write in flight fails", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "old", ""))
			rec, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			once := false
			c.AfterWrite(rookMetaPool, accountsNS, "name.$new", func() {
				if once {
					return
				}
				once = true
				create(ctx, newAccount(id2, "", "new", ""))
			})
			old := rec.Info
			rec.Info.Name = "new"
			Expect(s.PutAccount(ctx, rec, &old, op.PutAccountOptions{})).To(MatchError(op.ErrConcurrentModification),
				"id2 took the claim between it and the write, and its fence moved id1's version")
			got, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Name).To(Equal("old"), "no second holder")
			Expect(redirectTo(accountsNS, "name.$new")).To(Equal(id2))
		})

		It("leaves one holder when another account takes a create's claim in flight", func(ctx SpecContext) {
			took := false
			c.AfterWrite(rookMetaPool, accountsNS, "name.$new", func() {
				if took {
					return
				}
				took = true
				create(ctx, newAccount(id2, "", "new", ""))
			})
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrConcurrentModification), "id2's takeover fenced id1's nameless object")
			Expect(holders(ctx, "new")).To(Equal([]string{id2}))
			byName, err := s.GetAccountByName(ctx, "", "new")
			Expect(err).NotTo(HaveOccurred())
			Expect(byName.Info.ID).To(Equal(id2))
		})

		It("leaves at most a nameless account when a create stops, which no lookup finds and which can be removed", func(ctx SpecContext) {
			c.FailNextWrite(rookMetaPool, accountsNS, "name.$x", syscall.ETIMEDOUT)
			armed := false
			c.AfterWrite(rookMetaPool, accountsNS, "account."+id1, func() {
				if armed {
					return
				}
				armed = true
				c.FailNextWrite(rookMetaPool, accountsNS, "account."+id1, syscall.ETIMEDOUT)
			})
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "", "x", "x@y.example")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(HaveOccurred())
			left, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred(), "the stopped create's own removal failed too")
			Expect(left.Info.Name).To(BeEmpty())
			Expect(left.Info.Email).To(BeEmpty())
			_, err = s.GetAccountByName(ctx, "", "x")
			Expect(err).To(MatchError(op.ErrNoSuchEntity))
			_, err = s.GetAccountByEmail(ctx, "x@y.example")
			Expect(err).To(MatchError(op.ErrNoSuchEntity))
			Expect(s.RemoveAccount(ctx, left)).To(Succeed())
			create(ctx, newAccount(id2, "", "x", "x@y.example"))
			Expect(holders(ctx, "x")).To(Equal([]string{id2}))
		})

		It("leaves one holder when the fenced holder claims again before the takeover removes its redirect", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "old", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "account."+id1, syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
			Expect(err).To(HaveOccurred(), "leaves the claim name.$new naming id1, which holds old")
			fired := false
			c.AfterWrite(rookMetaPool, accountsNS, "account."+id1, func() {
				if fired {
					return
				}
				fired = true
				_, rerr := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
				Expect(rerr).NotTo(HaveOccurred(), "id1 claims its redirect again and writes under the fenced version it read")
			})
			err = s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(fired).To(BeTrue(), "id1's rename ran after id2's fence")
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "id1 claimed again, so id2's versioned removal failed")
			Expect(holders(ctx, "new")).To(Equal([]string{id1}))
			Expect(exists(accountsNS, "account."+id2)).To(BeFalse(), "the refused create's nameless object is removed")
		})

		It("reads the holder again when the fence loses to the holder's own write", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "old", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "account."+id1, syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
			Expect(err).To(HaveOccurred())
			landed := false
			c.BeforeWrite(rookMetaPool, accountsNS, "account."+id1, func(*fakerados.Object) {
				if landed {
					return
				}
				landed = true
				// A write of id1 that lands the name without its redirect,
				// as a gateway predating the claim would, just before the fence.
				c.Put(rookMetaPool, accountsNS, "account."+id1, encode(newAccount(id1, "", "new", "")))
				object(accountsNS, "account."+id1).Xattrs[version.XattrName] = encode(meta.ObjVersion{Ver: 50, Tag: "peer"})
			})
			err = s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(landed).To(BeTrue())
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "the fence failed, and the holder read again holds the name")
			Expect(holders(ctx, "new")).To(Equal([]string{id1}))
		})

		It("removes a stale redirect only under the version it read, so a claim made meanwhile stands", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "old", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "account."+id1, syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
			Expect(err).To(HaveOccurred())
			raced := false
			c.BeforeWrite(rookMetaPool, accountsNS, "name.$new", func(*fakerados.Object) {
				if raced {
					return
				}
				raced = true
				create(ctx, newAccount(id3, "", "new", ""))
			})
			err = s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(raced).To(BeTrue(), "id3 took the stale redirect over between id2's read of it and id2's removal")
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists))
			Expect(holders(ctx, "new")).To(Equal([]string{id3}))
			Expect(redirectTo(accountsNS, "name.$new")).To(Equal(id3))
		})

		It("takes a holder stored without a version for a holder", func(ctx SpecContext) {
			c.Put(rookMetaPool, accountsNS, "account."+id1, encode(newAccount(id1, "", "old", "")))
			c.Put(rookMetaPool, accountsNS, "name.$new", encode(meta.UID(id1)))
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "an object without a version cannot be fenced")
			Expect(redirectTo(accountsNS, "name.$new")).To(Equal(id1))
			Expect(exists(accountsNS, "account."+id2)).To(BeFalse())
		})

		It("reads a claimed redirect past a stale cache", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "old", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "account."+id1, syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
			Expect(err).To(HaveOccurred(), "leaves name.$new naming id1")
			runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			done := make(chan error, 1)
			go func() { done <- s.Run(runCtx) }()
			DeferCleanup(func() {
				cancel()
				Eventually(done).WithTimeout(time.Second).Should(Receive())
			})
			cached := func() bool {
				_, lerr := s.GetAccountByName(ctx, "", "new")
				Expect(lerr).To(MatchError(op.ErrNoSuchEntity), "id1 does not hold new")
				n := c.Reads(rookMetaPool, accountsNS, "name.$new")
				_, lerr = s.GetAccountByName(ctx, "", "new")
				Expect(lerr).To(MatchError(op.ErrNoSuchEntity))
				return c.Reads(rookMetaPool, accountsNS, "name.$new") == n
			}
			Eventually(cached).WithTimeout(time.Second).WithPolling(5*time.Millisecond).Should(BeTrue(), "this gateway caches name.$new naming id1")
			// Another gateway gave the name to id3; this one missed the notify.
			c.Put(rookMetaPool, accountsNS, "account."+id3, encode(newAccount(id3, "", "new", "")))
			object(accountsNS, "account."+id3).Xattrs[version.XattrName] = encode(meta.ObjVersion{Ver: 2, Tag: "peer"})
			c.Put(rookMetaPool, accountsNS, "name.$new", encode(meta.UID(id3)))
			object(accountsNS, "name.$new").Xattrs[version.XattrName] = encode(meta.ObjVersion{Ver: 1, Tag: "peer"})
			err = s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "id3 holds new in RADOS")
			Expect(redirectTo(accountsNS, "name.$new")).To(Equal(id3))
		})

		It("refuses a holder's own claim when the redirect was taken over before its rewrite", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "old", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "account."+id1, syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
			Expect(err).To(HaveOccurred(), "leaves name.$new naming id1, which holds old")
			fired, took := false, false
			c.AfterWrite(rookMetaPool, accountsNS, "account."+id1, func() {
				if fired {
					return
				}
				fired = true
				c.BeforeWrite(rookMetaPool, accountsNS, "name.$new", func(*fakerados.Object) {
					if took {
						return
					}
					took = true
					// A peer took the redirect over for id3, which holds new.
					c.Put(rookMetaPool, accountsNS, "account."+id3, encode(newAccount(id3, "", "new", "")))
					object(accountsNS, "account."+id3).Xattrs[version.XattrName] = encode(meta.ObjVersion{Ver: 2, Tag: "peer"})
					c.Put(rookMetaPool, accountsNS, "name.$new", encode(meta.UID(id3)))
					object(accountsNS, "name.$new").Xattrs[version.XattrName] = encode(meta.ObjVersion{Ver: 1, Tag: "peer"})
				})
				_, rerr := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
				Expect(rerr).To(MatchError(op.ErrAccountAlreadyExists), "id1's rewrite under the version it read failed, and id3 holds new")
			})
			err = s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(fired).To(BeTrue())
			Expect(took).To(BeTrue())
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists))
			Expect(holders(ctx, "new")).To(Equal([]string{id3}))
		})

		It("leaves one holder when a holder's own rewrite of its redirect fails", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "old", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "account."+id1, syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
			Expect(err).To(HaveOccurred())
			fired := false
			c.AfterWrite(rookMetaPool, accountsNS, "account."+id1, func() {
				if fired {
					return
				}
				fired = true
				c.FailNextWrite(rookMetaPool, accountsNS, "name.$new", syscall.ETIMEDOUT)
				_, rerr := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "new" })
				Expect(rerr).To(MatchError(op.ErrRequestTimedOut), "id1's rewrite failed, so it writes no account")
			})
			err = s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(fired).To(BeTrue())
			Expect(err).NotTo(HaveOccurred())
			Expect(holders(ctx, "new")).To(Equal([]string{id2}))
		})

		It("leaves one holder when two creates both read a name's redirect missing", func(ctx SpecContext) {
			fired := false
			c.BeforeWrite(rookMetaPool, accountsNS, "name.$new", func(*fakerados.Object) {
				if fired {
					return
				}
				fired = true
				create(ctx, newAccount(id2, "", "new", ""))
			})
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(fired).To(BeTrue(), "id2's create ran between id1's read of the redirect and its exclusive create")
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists))
			Expect(holders(ctx, "new")).To(Equal([]string{id2}))
			Expect(exists(accountsNS, "account."+id1)).To(BeFalse())
		})

		It("removes a refused create's nameless object only under the version it wrote", func(ctx SpecContext) {
			create(ctx, newAccount(id2, "", "x", ""))
			once := false
			c.AfterWrite(rookMetaPool, accountsNS, "account."+id1, func() {
				if once {
					return
				}
				once = true
				// Another create of the caller-supplied id1 replaces the blank.
				blank, err := s.GetAccount(ctx, id1)
				Expect(err).NotTo(HaveOccurred())
				Expect(s.RemoveAccount(ctx, blank)).To(Succeed())
				create(ctx, newAccount(id1, "", "y", ""))
			})
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "", "x", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "id2 holds x")
			Expect(holders(ctx, "y")).To(Equal([]string{id1}), "the other create's account stands")
		})

		It("gives up the name a refused create claimed when its email is taken", func(ctx SpecContext) {
			create(ctx, newAccount(id2, "", "other", "ops@acme.example"))
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "", "acme", "ops@acme.example")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists))
			Expect(exists(accountsNS, "name.$acme")).To(BeFalse(), "no redirect left naming an account with no object")
			Expect(exists(accountsNS, "account."+id1)).To(BeFalse())
		})

		It("keeps a refused create's nameless object when it cannot give up the name it claimed", func(ctx SpecContext) {
			create(ctx, newAccount(id2, "", "other", "ops@acme.example"))
			armed := false
			c.AfterWrite(rookMetaPool, accountsNS, "name.$acme", func() {
				if armed {
					return
				}
				armed = true
				c.FailNextWrite(rookMetaPool, accountsNS, "name.$acme", syscall.EIO)
			})
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "", "acme", "ops@acme.example")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists))
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id1))
			left, err := s.GetAccount(ctx, id1)
			Expect(err).NotTo(HaveOccurred(), "the redirect still names an account that exists")
			Expect(left.Info.Name).To(BeEmpty())
			create(ctx, newAccount(id3, "", "acme", ""))
			Expect(holders(ctx, "acme")).To(Equal([]string{id3}), "the stale redirect is taken over")
		})

		It("gives up a refused create's name only under the version its claim left", func(ctx SpecContext) {
			create(ctx, newAccount(id2, "", "other", "ops@acme.example"))
			took := false
			c.AfterWrite(rookMetaPool, accountsNS, "name.$acme", func() {
				if took {
					return
				}
				took = true
				create(ctx, newAccount(id3, "", "acme", ""))
			})
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id1, "", "acme", "ops@acme.example")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(took).To(BeTrue(), "id3 took the name over from id1's nameless object")
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists))
			byName, err := s.GetAccountByName(ctx, "", "acme")
			Expect(err).NotTo(HaveOccurred())
			Expect(byName.Info.ID).To(Equal(id3))
		})

		It("reads a holder past a stale cache before taking its redirect over", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "y", ""))
			runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			done := make(chan error, 1)
			go func() { done <- s.Run(runCtx) }()
			DeferCleanup(func() {
				cancel()
				Eventually(done).WithTimeout(time.Second).Should(Receive())
			})
			cached := func() bool {
				_, err := s.GetAccount(ctx, id1)
				Expect(err).NotTo(HaveOccurred())
				n := c.Reads(rookMetaPool, accountsNS, "account."+id1)
				_, err = s.GetAccount(ctx, id1)
				Expect(err).NotTo(HaveOccurred())
				return c.Reads(rookMetaPool, accountsNS, "account."+id1) == n
			}
			Eventually(cached).WithTimeout(time.Second).WithPolling(5*time.Millisecond).Should(BeTrue(), "this gateway caches id1 named y")
			// Another gateway renamed id1 to x; this one missed the notify.
			renamed := newAccount(id1, "", "x", "")
			c.Put(rookMetaPool, accountsNS, "account."+id1, encode(renamed))
			object(accountsNS, "account."+id1).Xattrs[version.XattrName] = encode(meta.ObjVersion{Ver: 9, Tag: "peer"})
			c.Put(rookMetaPool, accountsNS, "name.$x", encode(meta.UID(id1)))
			writes := c.Writes(rookMetaPool, accountsNS, "account."+id1)
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "x", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "id1 holds x in RADOS")
			Expect(redirectTo(accountsNS, "name.$x")).To(Equal(id1))
			Expect(c.Writes(rookMetaPool, accountsNS, "account."+id1)).To(Equal(writes), "a live holder is not fenced")
		})

		It("leaves a removal's stale redirects for a later account to take", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", "ops@acme.example"))
			c.FailNextWrite(rookMetaPool, accountsNS, "name.$acme", syscall.ETIMEDOUT)
			c.FailNextWrite(rookMetaPool, emailNS, "ops@acme.example", syscall.ETIMEDOUT)
			Expect(s.RemoveAccount(ctx, rec)).To(Succeed(), "radosgw ignores each redirect's failure, account.cc:409-426")
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id1))
			Expect(s.RemoveAccount(ctx, rec)).To(MatchError(op.ErrNoSuchEntity))
			_, err := s.GetAccountByName(ctx, "", "acme")
			Expect(err).To(MatchError(op.ErrNoSuchEntity))
			_, err = s.GetAccountByEmail(ctx, "ops@acme.example")
			Expect(err).To(MatchError(op.ErrNoSuchEntity))

			create(ctx, newAccount(id2, "", "acme", "ops@acme.example"))
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id2))
			Expect(redirectTo(emailNS, "ops@acme.example")).To(Equal(id2))
		})

		It("leaves a name another account took meanwhile, its redirect held under a version of its own", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", ""))
			c.FailNextWrite(rookMetaPool, accountsNS, "name.$acme", syscall.ETIMEDOUT)
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "acme2" })
			Expect(err).NotTo(HaveOccurred())
			create(ctx, newAccount(id2, "", "acme", ""))
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id2))
			_, err = modify(ctx, id1, func(a *meta.AccountInfo) { a.Name = "acme" })
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists), "the name is id2's now")
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id2))
		})
	})

	Describe("removing", func() {
		It("removes the account, then its redirects and its users index", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "t", "acme", "ops@acme.example"))
			Expect(s.AddAccountUser(ctx, id1, meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "Alice", Path: "/"})).To(Succeed())
			Expect(exists(accountsNS, "users."+id1)).To(BeTrue())
			var order []string
			for _, o := range [][2]string{{accountsNS, "account." + id1}, {accountsNS, "name.t$acme"}, {emailNS, "ops@acme.example"}, {accountsNS, "users." + id1}} {
				c.AfterWrite(rookMetaPool, o[0], o[1], func() { order = append(order, o[1]) })
			}
			Expect(s.RemoveAccount(ctx, rec)).To(Succeed())
			Expect(order).To(Equal([]string{"account." + id1, "name.t$acme", "ops@acme.example", "users." + id1}), "account.cc:401-435")
			for _, o := range [][2]string{{accountsNS, "account." + id1}, {accountsNS, "name.t$acme"}, {emailNS, "ops@acme.example"}, {accountsNS, "users." + id1}} {
				Expect(exists(o[0], o[1])).To(BeFalse(), "%s/%s", o[0], o[1])
			}
		})

		DescribeTable("refuses to remove an account that still has a role, a group, an OpenID Connect provider or a topic",
			func(ctx SpecContext, seed func(), message string) {
				rec := create(ctx, newAccount(id1, "", "acme", ""))
				seed()
				err := s.RemoveAccount(ctx, rec)
				Expect(err).To(MatchError(op.ErrBucketNotEmpty), "rgw_account.cc:366-456's -ENOTEMPTY")
				Expect(op.AsError(err).Message).To(Equal(message))
				Expect(exists(accountsNS, "account."+id1)).To(BeTrue())
				Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id1))
			},
			Entry("a role", func() { seedResource(c, "roles."+id1, "r1") }, "The account cannot be deleted until all roles are removed."),
			Entry("a group", func() { seedResource(c, "groups."+id1, "g1") }, "The account cannot be deleted until all groups are removed."),
			Entry("a topic, which radosgw misses", func() { seedResource(c, "topics."+id1, "t1") }, "The account cannot be deleted until all topics are removed."),
			Entry("an OpenID Connect provider", func() {
				c.Put(rookMetaPool, "oidc", id1+"oidc_url.idp.example.com", []byte{1})
			}, "The account cannot be deleted until all OpenIDConnectProviders are removed."),
		)

		It("removes an account whose resource indexes are empty or missing, and another account's provider holds nothing off", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", ""))
			c.Put(rookMetaPool, accountsNS, "roles."+id1, nil)
			c.Put(rookMetaPool, "oidc", id2+"oidc_url.idp.example.com", []byte{1})
			Expect(s.RemoveAccount(ctx, rec)).To(Succeed())
		})

		It("refuses the removal when the oidc pool cannot be listed", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", ""))
			failing, err := driver.Open(ctx, oidcListFails{c}, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(failing.Close)
			Expect(failing.RemoveAccount(ctx, rec)).To(HaveOccurred())
			Expect(exists(accountsNS, "account."+id1)).To(BeTrue())
		})

		It("refuses the removal when a resource index cannot be read", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", ""))
			c.Put(rookMetaPool, accountsNS, "groups."+id1, nil)
			c.FailNextRead(rookMetaPool, accountsNS, "groups."+id1, syscall.EIO)
			Expect(s.RemoveAccount(ctx, rec)).To(HaveOccurred())
			Expect(exists(accountsNS, "account."+id1)).To(BeTrue())
		})

		It("refuses a removal under a stale version and removes nothing", func(ctx SpecContext) {
			stale := create(ctx, newAccount(id1, "", "acme", ""))
			_, err := modify(ctx, id1, func(a *meta.AccountInfo) { a.MaxUsers = 5 })
			Expect(err).NotTo(HaveOccurred())
			Expect(s.RemoveAccount(ctx, stale)).To(MatchError(op.ErrConcurrentModification))
			Expect(exists(accountsNS, "account."+id1)).To(BeTrue())
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id1))
		})

		It("leaves a name or email redirect that names someone else", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", "ops@acme.example"))
			c.Put(rookMetaPool, accountsNS, "name.$acme", encode(meta.UID(id2)))
			c.Put(rookMetaPool, emailNS, "ops@acme.example", encode(meta.UID("alice")))
			// A gateway that has not cached the redirects reads what RADOS holds.
			cold, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(cold.Close)
			Expect(cold.RemoveAccount(ctx, rec)).To(Succeed())
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id2), "radosgw removes it unread, account.cc:409-417")
			Expect(redirectTo(emailNS, "ops@acme.example")).To(Equal("alice"), "radosgw removes it unread, account.cc:418-426")
		})

		It("removes a redirect only under the version it read", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", ""))
			runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			done := make(chan error, 1)
			go func() { done <- s.Run(runCtx) }()
			DeferCleanup(func() {
				cancel()
				Eventually(done).WithTimeout(time.Second).Should(Receive())
			})
			cached := func() bool {
				_, err := s.GetAccountByName(ctx, "", "acme")
				Expect(err).NotTo(HaveOccurred())
				n := c.Reads(rookMetaPool, accountsNS, "name.$acme")
				_, err = s.GetAccountByName(ctx, "", "acme")
				Expect(err).NotTo(HaveOccurred())
				return c.Reads(rookMetaPool, accountsNS, "name.$acme") == n
			}
			Eventually(cached).WithTimeout(time.Second).WithPolling(5*time.Millisecond).Should(BeTrue(), "the cache serves the redirect")
			c.Put(rookMetaPool, accountsNS, "name.$acme", encode(meta.UID(id2)))
			Expect(s.RemoveAccount(ctx, rec)).To(Succeed())
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id2), "rewritten behind the cache; read past it, it names id2")
		})

		It("leaves the redirects a create of the same id claimed while the removal ran", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "new", "ops@acme.example"))
			fired := false
			c.AfterWrite(rookMetaPool, accountsNS, "account."+id1, func() {
				if fired {
					return
				}
				fired = true
				create(ctx, newAccount(id1, "", "new", "ops@acme.example"))
			})
			Expect(s.RemoveAccount(ctx, rec)).To(Succeed())
			Expect(fired).To(BeTrue())
			Expect(redirectTo(accountsNS, "name.$new")).To(Equal(id1))
			Expect(redirectTo(emailNS, "ops@acme.example")).To(Equal(id1))
			err := s.PutAccount(ctx, &op.AccountRecord{Info: newAccount(id2, "", "new", "")}, nil, op.PutAccountOptions{Exclusive: true})
			Expect(err).To(MatchError(op.ErrAccountAlreadyExists))
			Expect(holders(ctx, "new")).To(Equal([]string{id1}))
		})

		It("refuses the removal when a redirect cannot be read, removing nothing", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", ""))
			c.FailNextRead(rookMetaPool, accountsNS, "name.$acme", syscall.EIO)
			Expect(s.RemoveAccount(ctx, rec)).To(HaveOccurred())
			Expect(exists(accountsNS, "account."+id1)).To(BeTrue())
			Expect(redirectTo(accountsNS, "name.$acme")).To(Equal(id1))
		})

		It("logs a redirect it could not remove without naming the email", func(ctx SpecContext) {
			rec := create(ctx, newAccount(id1, "", "acme", "secret.person@acme.example"))
			c.FailNextWrite(rookMetaPool, emailNS, "secret.person@acme.example", syscall.EIO)
			Expect(s.RemoveAccount(ctx, rec)).To(Succeed())
			Expect(logs.String()).To(ContainSubstring(id1))
			Expect(logs.String()).NotTo(ContainSubstring("secret.person"))
		})
	})

	Describe("the users index", func() {
		It("keeps the account users index", func(ctx SpecContext) {
			create(ctx, newAccount(id1, "", "acme", ""))
			alice := meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "Alice", Path: "/"}
			Expect(s.AddAccountUser(ctx, id1, alice)).To(Succeed())
			Expect(s.AddAccountUser(ctx, id1, alice)).To(Succeed(), "not exclusive, svc_user_rados.cc:365-366")
			o := object(accountsNS, "users."+id1)
			Expect(o.Omap).To(HaveKey("alice"))
			d := denc.NewDecoder(o.Omap["alice"])
			res := user.DecodeAccountResource(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(res.Name).To(Equal("Alice"))
			Expect(res.Path).To(Equal("/"))
			md := denc.NewDecoder(res.Metadata)
			Expect(user.DecodeResourceMetadata(md)).To(Equal(user.ResourceMetadata{UserID: "u1"}), "users.cc:34-40")
			ids, next, err := s.ListAccountUsers(ctx, id1, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"u1"}))
			Expect(next).To(BeEmpty(), "users.cc:154-156 clears an untruncated marker")
			Expect(s.RemoveAccountUser(ctx, id1, "ALICE")).To(Succeed(), "keyed lower-cased")
			Expect(s.RemoveAccountUser(ctx, id1, "Alice")).To(MatchError(op.ErrNoSuchKey), "users::remove returns the class's ENOENT")
			ids, next, err = s.ListAccountUsers(ctx, id2, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(BeEmpty(), "a missing index object, users.cc:131-135")
			Expect(next).To(BeEmpty())
		})

		It("pages the index by its raw keys", func(ctx SpecContext) {
			for i, name := range []string{"Carol", "alice", "Bob"} {
				Expect(s.AddAccountUser(ctx, id1, meta.UserInfo{UserID: meta.UserID{ID: fmt.Sprintf("u%d", i)}, DisplayName: name})).To(Succeed())
			}
			ids, next, err := s.ListAccountUsers(ctx, id1, "", 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"u1", "u2"}))
			Expect(next).To(Equal("bob"), "the raw lower-cased key")
			ids, next, err = s.ListAccountUsers(ctx, id1, next, 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"u0"}))
			Expect(next).To(BeEmpty())
		})

		It("fails a listing over an entry whose metadata does not decode", func(ctx SpecContext) {
			Expect(s.AddAccountUser(ctx, id1, meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "a"})).To(Succeed())
			e := denc.NewEncoder()
			user.AccountResource{Name: "b", Metadata: []byte{1}}.Encode(e, denc.Squid)
			object(accountsNS, "users."+id1).Omap["b"] = e.Bytes()
			_, _, err := s.ListAccountUsers(ctx, id1, "", 10)
			Expect(err).To(MatchError(op.ErrInternalError), "users.cc:143-150's -EIO")
		})
	})

	It("names the objects as rgwrados::account does", func(ctx SpecContext) {
		create(ctx, newAccount(id1, "Ten", "Acme", "A@B.example"))
		Expect(exists(accountsNS, "account."+id1)).To(BeTrue())
		Expect(exists(accountsNS, "name.Ten$Acme")).To(BeTrue(), "the name keeps its case, account.cc:92-95")
		Expect(exists(emailNS, "a@b.example")).To(BeTrue())
		Expect(object(accountsNS, "account."+id1).Mtime).To(BeTemporally("~", time.Now(), time.Minute))
	})
})

// seedResource stores one cls_user account resource named name in the
// account pool's index object oid.
func seedResource(c *fakerados.Cluster, oid, name string) {
	if c.Object(rookMetaPool, "accounts", oid) == nil {
		c.Put(rookMetaPool, "accounts", oid, nil)
	}
	c.Object(rookMetaPool, "accounts", oid).Omap[name] = encode(user.AccountResource{Name: name, Path: "/"})
}

// oidcListFails is the cluster with every listing of the oidc namespace
// failing with EIO.
type oidcListFails struct{ *fakerados.Cluster }

func (c oidcListFails) Pool(ctx context.Context, pool, ns string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, ns)
	if err != nil || ns != "oidc" {
		return p, err
	}
	return listFails{Pool: p}, nil
}

type listFails struct{ radosclient.Pool }

func (listFails) ListObjects(context.Context, func(oid, locator string) error) error {
	return &radosclient.Error{Errno: int32(syscall.EIO), Op: "list objects"}
}
