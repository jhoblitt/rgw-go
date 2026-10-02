package driver_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("UserStore", func() {
	// writeTag is generate_new_write_ver's tag: an underscore and 23
	// characters of gen_rand_alphanumeric's table (rgw_common.h:1621-1628,
	// random_string.cc:45-51).
	const writeTag = `^_[A-Za-z0-9_-]{23}$`
	var (
		c *fakerados.Cluster
		s *driver.Store
	)
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
	})
	alice := func() meta.UserInfo {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "alice"}
		u.DisplayName, u.Email = "Alice", "Alice@Example.com"
		u.AccessKeys = map[string]meta.AccessKey{"AKALICE": {ID: "AKALICE", Secret: "s", Active: true}}
		return u
	}
	object := func(ns, oid string) *fakerados.Object { return c.Object(rookMetaPool, ns, oid) }
	create := func(ctx SpecContext, info meta.UserInfo) *op.UserRecord {
		GinkgoHelper()
		rec := &op.UserRecord{Info: info}
		Expect(s.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed())
		return rec
	}
	storedVersion := func(ctx SpecContext) meta.ObjVersion {
		GinkgoHelper()
		rec, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		return rec.Version
	}

	Describe("reading", func() {
		It("reads a radosgw-written user with its rgw attrs, version and mtime", func(ctx SpecContext) {
			mtime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			c.SetClock(func() time.Time { return mtime })
			seedUser(c, alice(), map[string][]byte{"user.rgw.iam-policy": []byte("{}"), "ceph.other": {1}}, meta.ObjVersion{Ver: 3, Tag: "t"})
			rec, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.DisplayName).To(Equal("Alice"))
			Expect(rec.Info.AccessKeys).To(Equal(alice().AccessKeys))
			Expect(rec.Attrs).To(HaveKey("user.rgw.iam-policy"))
			Expect(rec.Attrs).NotTo(HaveKey("ceph.other"), "rgw_filter_attrset keeps user.rgw.*")
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 3, Tag: "t"}))
			Expect(rec.Mtime).To(BeTemporally("==", mtime))
		})

		It("is NoSuchUser for a missing or anonymous user", func(ctx SpecContext) {
			_, err := s.GetUser(ctx, meta.UserID{ID: "nobody"})
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			_, err = s.GetUser(ctx, meta.UserID{ID: op.AnonymousUserID})
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			Expect(c.Reads(rookMetaPool, "users.uid", op.AnonymousUserID)).To(BeZero(), "svc_user_rados.cc:125-128")
		})

		It("rejects a uid object that names another user, or does not decode", func(ctx SpecContext) {
			bob := alice()
			bob.UserID = meta.UserID{ID: "bob"}
			c.Put(rookMetaPool, "users.uid", "alice", encode(meta.UserObject{UID: "bob", Info: bob}))
			_, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
			Expect(err).To(MatchError(op.ErrInternalError), "radosgw's -EIO, :143-146")
			c.Put(rookMetaPool, "users.uid", "carol", []byte{1})
			_, err = s.GetUser(ctx, meta.UserID{ID: "carol"})
			Expect(err).To(MatchError(op.ErrInternalError), ":150-153")
		})

		It("resolves an access key and a mixed-case email through the index objects", func(ctx SpecContext) {
			seedUser(c, alice(), nil, meta.ObjVersion{Ver: 1, Tag: "t"})
			rec, err := s.GetUserByAccessKey(ctx, "AKALICE")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.UserID.ID).To(Equal("alice"))
			rec, err = s.GetUserByEmail(ctx, "ALICE@example.COM")
			Expect(err).NotTo(HaveOccurred(), "the index is lower-cased on both sides, :319-321")
			Expect(rec.Info.UserID.ID).To(Equal("alice"))
			_, err = s.GetUserByAccessKey(ctx, "AKNOBODY")
			Expect(err).To(MatchError(op.ErrNoSuchUser))
		})

		It("lower-cases ASCII letters alone in the email index, as boost::to_lower in the C locale", func(ctx SpecContext) {
			info := alice()
			info.Email = "ÉLISE@Example.com"
			create(ctx, info)
			Expect(object("users.email", "Élise@example.com")).NotTo(BeNil())
			rec, err := s.GetUserByEmail(ctx, "ÉLISE@EXAMPLE.COM")
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.UserID.ID).To(Equal("alice"))
			_, err = s.GetUserByEmail(ctx, "élise@example.com")
			Expect(err).To(MatchError(op.ErrNoSuchUser), "a non-ASCII letter keeps its case")
		})

		It("does not resolve an index that names an account", func(ctx SpecContext) {
			const account = "RGW12345678901234567"
			c.Put(rookMetaPool, "users.email", "ops@acme.example", encode(meta.UID(account)))
			_, err := s.GetUserByEmail(ctx, "ops@acme.example")
			Expect(err).To(MatchError(op.ErrNoSuchUser), "rgw::account::validate_id, :700-703")
			Expect(c.Reads(rookMetaPool, "users.uid", account)).To(BeZero())
		})
	})

	Describe("writing", func() {
		It("creates a user exclusively with its indexes as radosgw writes them", func(ctx SpecContext) {
			rec := &op.UserRecord{Info: alice(), Attrs: map[string][]byte{"user.rgw.iam-policy": []byte("p")}}
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed())
			obj := object("users.uid", "alice")
			Expect(obj).NotTo(BeNil())
			Expect(obj.Data).To(Equal(encode(meta.UserObject{UID: "alice", Info: alice()})), "RGWUID then RGWUserInfo, :296-298")
			Expect(obj.Xattrs).To(HaveKey("user.rgw.iam-policy"))
			Expect(object("users.keys", "AKALICE").Data).To(Equal(encode(meta.UID("alice"))), "link_bl, :312-313")
			Expect(object("users.email", "alice@example.com").Data).To(Equal(encode(meta.UID("alice"))))
			Expect(rec.Version.Ver).To(BeEquivalentTo(1))
			Expect(rec.Version.Tag).To(MatchRegexp(writeTag))
			Expect(storedVersion(ctx)).To(Equal(rec.Version))
			Expect(s.PutUser(ctx, &op.UserRecord{Info: alice()}, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrUserAlreadyExists))
		})

		It("stores an explicit opts.Mtime, as store_user_info's mtime argument", func(ctx SpecContext) {
			mtime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			rec := &op.UserRecord{Info: alice()}
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true, Mtime: mtime})).To(Succeed())
			Expect(object("users.uid", "alice").Mtime).To(BeTemporally("==", mtime), "PutParams' mtime, :300")
			Expect(rec.Mtime).To(BeTemporally("==", mtime))
		})

		It("stores now over a record's old mtime on a read-modify-write, rec.Mtime being output only", func(ctx SpecContext) {
			old := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			Expect(s.PutUser(ctx, &op.UserRecord{Info: alice()}, op.PutUserOptions{Exclusive: true, Mtime: old})).To(Succeed())
			rec, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Mtime).To(BeTemporally("==", old))
			rec.Info.DisplayName = "Alice Two"
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &rec.Version})).To(Succeed())
			Expect(rec.Mtime).To(BeTemporally("~", time.Now(), time.Minute), "RadosUser::store_user passes no mtime")
			Expect(object("users.uid", "alice").Mtime).To(BeTemporally("==", rec.Mtime))
		})

		It("updates a user under its version, rewriting only the changed indexes", func(ctx SpecContext) {
			rec := create(ctx, alice())
			v := rec.Version
			rec.Info.Email = "alice2@example.com"
			rec.Info.AccessKeys = map[string]meta.AccessKey{"AKNEW": {ID: "AKNEW", Secret: "s", Active: true}}
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &v})).To(Succeed())
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: v.Tag}), "write_version = read_version + 1, :237-240")
			Expect(object("users.keys", "AKNEW")).NotTo(BeNil())
			Expect(object("users.keys", "AKALICE")).To(BeNil(), "remove_old_indexes, :424-432")
			Expect(object("users.email", "alice@example.com")).To(BeNil())
			Expect(object("users.email", "alice2@example.com")).NotTo(BeNil())
			stale := v
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &stale})).To(MatchError(op.ErrConcurrentModification))
		})

		It("leaves an unchanged index alone and keeps an email index across a change of case", func(ctx SpecContext) {
			rec := create(ctx, alice())
			key, email := object("users.keys", "AKALICE"), object("users.email", "alice@example.com")
			rec.Info.Email = "ALICE@example.com"
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
			Expect(object("users.keys", "AKALICE")).To(BeIdenticalTo(key), "an already active key is skipped, :332-333")
			Expect(object("users.email", "alice@example.com")).To(BeIdenticalTo(email), "boost::iequals, :317 and :416")
		})

		It("overwrites without a check under a zero IfVersion, writing a fresh version", func(ctx SpecContext) {
			first := create(ctx, alice()).Version
			zero := meta.ObjVersion{}
			rec := &op.UserRecord{Info: alice()}
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &zero})).To(Succeed(), "version_for_check is null at ver 0, rgw_common.h:950-955")
			Expect(rec.Version.Ver).To(BeEquivalentTo(1))
			Expect(rec.Version.Tag).To(MatchRegexp(writeTag))
			Expect(rec.Version.Tag).NotTo(Equal(first.Tag), "an empty read tag generates a write version, :234-236")
			Expect(storedVersion(ctx)).To(Equal(rec.Version))
		})

		It("refuses a version with a count and no tag, since a stored version carries one", func(ctx SpecContext) {
			create(ctx, alice())
			v := meta.ObjVersion{Ver: 5}
			Expect(s.PutUser(ctx, &op.UserRecord{Info: alice()}, op.PutUserOptions{IfVersion: &v})).To(MatchError(op.ErrConcurrentModification),
				"obj_version's compare checks the tag, cls_version_types.h:46-49")
		})

		It("refuses the stored count under another tag", func(ctx SpecContext) {
			first := create(ctx, alice()).Version
			other := meta.ObjVersion{Ver: first.Ver, Tag: "other"}
			Expect(s.PutUser(ctx, &op.UserRecord{Info: alice()}, op.PutUserOptions{IfVersion: &other})).To(MatchError(op.ErrConcurrentModification))
			Expect(storedVersion(ctx)).To(Equal(first))
		})

		It("writes a fresh version without IfVersion", func(ctx SpecContext) {
			first := create(ctx, alice()).Version
			rec := &op.UserRecord{Info: alice()}
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
			Expect(rec.Version.Ver).To(BeEquivalentTo(1))
			Expect(rec.Version.Tag).NotTo(Equal(first.Tag), ":234-236")
			Expect(storedVersion(ctx)).To(Equal(rec.Version))
		})

		It("refuses an access key already mapped to another user", func(ctx SpecContext) {
			create(ctx, alice())
			bob := alice()
			bob.UserID, bob.Email = meta.UserID{ID: "bob"}, ""
			Expect(s.PutUser(ctx, &op.UserRecord{Info: bob}, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrKeyExists), ":257-270")
			Expect(object("users.uid", "bob")).To(BeNil(), "prepare fails before put")
		})

		It("takes over an access key whose index names a user that no longer exists", func(ctx SpecContext) {
			c.Put(rookMetaPool, "users.keys", "AKALICE", encode(meta.UID("ghost")))
			bob := alice()
			bob.UserID, bob.Email = meta.UserID{ID: "bob"}, ""
			Expect(s.PutUser(ctx, &op.UserRecord{Info: bob}, op.PutUserOptions{})).To(Succeed(), "the check reads the user the index names, :263-265")
			Expect(object("users.keys", "AKALICE").Data).To(Equal(encode(meta.UID("bob"))))
		})

		It("indexes active access keys alone and drops the index of a key that goes inactive", func(ctx SpecContext) {
			info := alice()
			info.AccessKeys["AKOFF"] = meta.AccessKey{ID: "AKOFF", Secret: "s"}
			rec := create(ctx, info)
			Expect(object("users.keys", "AKOFF")).To(BeNil(), ":329-331")
			rec.Info.AccessKeys["AKALICE"] = meta.AccessKey{ID: "AKALICE", Secret: "s"}
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
			Expect(object("users.keys", "AKALICE")).To(BeNil(), ":424-432")
		})

		It("indexes active Swift keys and refuses one another user holds", func(ctx SpecContext) {
			info := alice()
			info.SwiftKeys = map[string]meta.AccessKey{"alice:swift": {ID: "alice:swift", Secret: "s", Active: true}}
			create(ctx, info)
			Expect(object("users.swift", "alice:swift").Data).To(Equal(encode(meta.UID("alice"))), ":341-351")
			bob := meta.NewUserInfo()
			bob.UserID = meta.UserID{ID: "bob"}
			bob.SwiftKeys = info.SwiftKeys
			Expect(s.PutUser(ctx, &op.UserRecord{Info: bob}, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrKeyExists), ":243-255")
			Expect(object("users.uid", "bob")).To(BeNil())
		})

		It("distributes a cache notify for every metadata write", func(ctx SpecContext) {
			create(ctx, alice())
			var total int
			for i := range 8 {
				total += len(c.Notifies("ceph-objectstore.rgw.control", "", fmt.Sprintf("notify.%d", i)))
			}
			Expect(total).To(Equal(3), "the uid object, the key index and the email index")
		})
	})

	Describe("removing", func() {
		It("removes a user with its indexes and bucket list", func(ctx SpecContext) {
			info := alice()
			info.SwiftKeys = map[string]meta.AccessKey{"alice:swift": {ID: "alice:swift", Secret: "s", Active: true}}
			rec := create(ctx, info)
			c.Put(rookMetaPool, "users.uid", "alice.buckets", nil)
			Expect(s.RemoveUser(ctx, rec)).To(Succeed())
			for _, o := range [][2]string{
				{"users.uid", "alice"},
				{"users.uid", "alice.buckets"},
				{"users.keys", "AKALICE"},
				{"users.email", "alice@example.com"},
				{"users.swift", "alice:swift"},
			} {
				Expect(object(o[0], o[1])).To(BeNil(), "%s/%s", o[0], o[1])
			}
			Expect(s.RemoveUser(ctx, rec)).To(MatchError(op.ErrNoSuchUser))
		})

		It("keeps the bucket list object when the user belongs to an account", func(ctx SpecContext) {
			info := alice()
			info.AccountID = "RGW12345678901234567"
			rec := create(ctx, info)
			c.Put(rookMetaPool, "users.uid", "alice.buckets", nil)
			Expect(s.RemoveUser(ctx, rec)).To(Succeed())
			Expect(object("users.uid", "alice.buckets")).NotTo(BeNil(), ":592-600")
			Expect(object("users.uid", "alice")).To(BeNil())
		})

		It("refuses to remove a user changed since it was read, its indexes and bucket list already gone", func(ctx SpecContext) {
			rec := create(ctx, alice())
			c.Put(rookMetaPool, "users.uid", "alice.buckets", nil)
			stale := *rec
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
			Expect(s.RemoveUser(ctx, &stale)).To(MatchError(op.ErrConcurrentModification))
			Expect(object("users.uid", "alice")).NotTo(BeNil())
			for _, o := range [][2]string{
				{"users.keys", "AKALICE"},
				{"users.email", "alice@example.com"},
				{"users.uid", "alice.buckets"},
			} {
				Expect(object(o[0], o[1])).To(BeNil(), "%s/%s goes first, svc_user_rados.cc:559-600", o[0], o[1])
			}
		})

		It("removes a user unchecked under a zero version, and a missing one is NoSuchUser", func(ctx SpecContext) {
			rec := create(ctx, alice())
			rec.Version = meta.ObjVersion{}
			Expect(s.RemoveUser(ctx, rec)).To(Succeed(), "unchecked")
			Expect(object("users.uid", "alice")).To(BeNil())
			Expect(s.RemoveUser(ctx, rec)).To(MatchError(op.ErrNoSuchUser), "missing")
		})

		It("reads a user back past a stale cache entry, so one gone behind the cache is NoSuchUser", func(ctx SpecContext) {
			rec := create(ctx, alice())
			runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			done := make(chan error, 1)
			go func() { done <- s.Run(runCtx) }()
			DeferCleanup(func() {
				cancel()
				Eventually(done).WithTimeout(time.Second).Should(Receive())
			})
			cached := func() bool {
				_, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
				Expect(err).NotTo(HaveOccurred())
				n := c.Reads(rookMetaPool, "users.uid", "alice")
				_, err = s.GetUser(ctx, meta.UserID{ID: "alice"})
				Expect(err).NotTo(HaveOccurred())
				return c.Reads(rookMetaPool, "users.uid", "alice") == n
			}
			Eventually(cached).WithTimeout(time.Second).WithPolling(5*time.Millisecond).Should(BeTrue(), "the cache serves the user")
			p, err := c.Pool(ctx, rookMetaPool, "users.uid")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(p.Close)
			wop := radosclient.NewWriteOp()
			wop.Remove()
			_, err = p.Write(ctx, "alice", wop, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred(), "removed without a notify, leaving the cache entry stale")
			Expect(s.RemoveUser(ctx, rec)).To(MatchError(op.ErrNoSuchUser))
		})

		It("logs a read-back that fails and reports the change", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			rec := create(ctx, alice())
			stale := *rec
			Expect(s.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
			rc := &recordingCluster{Cluster: c, readErrs: map[string]error{"alice": errors.New("read failed")}}
			s2, err := driver.Open(ctx, rc, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(s2.Close)
			Expect(s2.RemoveUser(ctx, &stale)).To(MatchError(op.ErrConcurrentModification))
			Expect(logs.String()).To(And(
				ContainSubstring(`"msg":"reading back an object whose version check failed"`),
				ContainSubstring("read failed"),
			))
		})
	})

	It("names an owner's bucket list: <uid>.buckets in users.uid, buckets.<id> in the account pool", func() {
		pool, oid := driver.OwnerBucketsObj(s, meta.UserOwner(meta.UserID{Tenant: "t", ID: "alice"}))
		Expect([]any{pool, oid}).To(Equal([]any{meta.ParsePool(rookMetaPool + ":users.uid"), "t$alice.buckets"}), "svc_user_rados.cc:109-113")
		pool, oid = driver.OwnerBucketsObj(s, meta.AccountOwner("RGW12345678901234567"))
		Expect([]any{pool, oid}).To(Equal([]any{meta.ParsePool(rookMetaPool + ":accounts"), "buckets.RGW12345678901234567"}), "account.cc:44-50")
	})

	Describe("reading an account", func() {
		const id = "RGW12345678901234567"
		account := func() meta.AccountInfo {
			a := meta.NewAccountInfo()
			a.ID, a.Tenant, a.Name = id, "acme", "Acme"
			return a
		}
		It("reads account.<id> from the account pool", func(ctx SpecContext) {
			c.Put(rookMetaPool, "accounts", "account."+id, encode(account()))
			got, err := driver.ReadAccount(s, ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(account()))
		})
		It("rejects an account object that names another account", func(ctx SpecContext) {
			other := account()
			other.ID = "RGW00000000000000001"
			c.Put(rookMetaPool, "accounts", "account."+id, encode(other))
			_, err := driver.ReadAccount(s, ctx, id)
			Expect(err).To(MatchError(op.ErrInternalError), "radosgw's -EIO, account.cc:192-196")
		})
		It("is radosgw's ENOENT row, NoSuchKey, for a missing account", func(ctx SpecContext) {
			_, err := driver.ReadAccount(s, ctx, id)
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})
	})
})
