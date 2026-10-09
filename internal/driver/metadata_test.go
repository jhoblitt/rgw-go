package driver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// docOf is e's Doc as the document type T.
func docOf[T any](e op.MetadataEntry) T {
	GinkgoHelper()
	d, ok := e.Doc.(T)
	if !ok {
		Fail(fmt.Sprintf("the entry's document is %T", e.Doc))
	}
	return d
}

var _ = Describe("MetadataStore", func() {
	const (
		zgID      = "zg-ceph-objectstore"
		indexPool = "ceph-objectstore.rgw.buckets.index"
		acctID    = "RGW00000000000000001"
	)
	var (
		c          *fakerados.Cluster
		s          *driver.Store
		alice, bob meta.UserID
		plain      *op.BucketRecord
	)
	aclOf := func(o meta.Owner) []byte {
		e := denc.NewEncoder()
		acl.DefaultPolicy(o, "the owner").Encode(e, denc.Squid)
		return e.Bytes()
	}
	putUser := func(ctx context.Context, id meta.UserID) {
		GinkgoHelper()
		info := meta.NewUserInfo()
		info.UserID = id
		info.DisplayName = strings.ToUpper(id.ID)
		info.Email = id.ID + "@example.com"
		info.AccessKeys = map[string]meta.AccessKey{"AK" + id.ID: {ID: "AK" + id.ID, Secret: "SK" + id.ID, Active: true}}
		Expect(s.PutUser(ctx, &op.UserRecord{Info: info}, op.PutUserOptions{Exclusive: true})).To(Succeed())
	}
	createBucket := func(ctx context.Context, tenant, name string, owner meta.UserID) *op.BucketRecord {
		GinkgoHelper()
		o := meta.UserOwner(owner)
		rec, err := s.CreateBucket(ctx, op.CreateBucketParams{
			Tenant: tenant, Name: name, Owner: o, Zonegroup: zgID, Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs: map[string][]byte{meta.AttrACL: aclOf(o)}, Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	listOwnerBuckets := func(ctx context.Context, owner meta.Owner) []string {
		GinkgoHelper()
		ents, _, _, err := s.ListUserBuckets(ctx, owner, "", 100)
		Expect(err).NotTo(HaveOccurred())
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Bucket.Name)
		}
		return names
	}
	get := func(ctx context.Context, section, key string) op.MetadataEntry {
		GinkgoHelper()
		e, err := s.Get(ctx, section, key)
		Expect(err).NotTo(HaveOccurred(), "%s:%s", section, key)
		return e
	}
	// race makes the next write of the domain-root object oid meet a
	// version another writer stored after the put read it.
	race := func(ns, oid string) {
		c.BeforeWrite(rookMetaPool, ns, oid, func(o *fakerados.Object) {
			c.BeforeWrite(rookMetaPool, ns, oid, nil)
			if o != nil {
				e := denc.NewEncoder()
				version.ObjVersion{Ver: 99, Tag: "racer"}.Encode(e, denc.Squid)
				o.Xattrs[version.XattrName] = e.Bytes()
			}
		})
	}
	shardsExist := func(info *meta.BucketInfo) bool {
		n := max(info.Layout.Current.Layout.Normal.NumShards, 1)
		for i := range n {
			if c.Object(indexPool, "", info.IndexShardOID(info.Layout.Current, i)) == nil {
				return false
			}
		}
		return true
	}
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = newIndexCluster()
		st, err := driver.Open(ctx, c, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		s = st
		alice, bob = meta.UserID{ID: "alice"}, meta.UserID{ID: "bob"}
		putUser(ctx, alice)
		putUser(ctx, bob)
		plain = createBucket(ctx, "", "plain", alice)
	})

	It("gets and lists users, skipping the .buckets objects", func(ctx SpecContext) {
		e := get(ctx, "user", "alice")
		Expect(e.Version.Ver).NotTo(BeZero())
		var doc map[string]any
		Expect(json.Unmarshal(e.Data, &doc)).To(Succeed())
		Expect(doc).To(HaveKeyWithValue("user_id", "alice"))
		Expect(doc).To(HaveKey("attrs"))
		Expect(e.Doc).To(BeAssignableToTypeOf(meta.UserCompleteInfo{}))
		Expect(c.Object(rookMetaPool, "users.uid", "alice.buckets")).NotTo(BeNil(), "alice's bucket list exists")
		keys, next, more, err := s.List(ctx, "user", "", 1000)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(ConsistOf("alice", "bob"))
		Expect(more).To(BeFalse())
		Expect(next).To(BeEmpty())
		_, err = s.Get(ctx, "user", "nosuch")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		_, err = s.Get(ctx, "kittens", "x")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		_, _, _, err = s.List(ctx, "kittens", "", 10)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
	})

	It("puts a user with its indexes, under the version it read", func(ctx SpecContext) {
		e := get(ctx, "user", "alice")
		var u meta.UserCompleteInfo
		Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
		u.Info.DisplayName = "Alice Renamed"
		u.Info.Email = "alice2@example.com"
		data, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		write := meta.ObjVersion{Ver: e.Version.Ver + 1, Tag: e.Version.Tag}
		Expect(s.Put(ctx, "user", "alice", op.MetadataEntry{Data: data, Version: write}, op.PutMetadataOptions{IfVersion: &e.Version})).To(Succeed())
		rec, err := s.GetUserByEmail(ctx, "alice2@example.com")
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.DisplayName).To(Equal("Alice Renamed"))
		Expect(rec.Version).To(Equal(write), "the document's version is the one written")
		_, err = s.GetUserByEmail(ctx, "alice@example.com")
		Expect(err).To(MatchError(op.ErrNoSuchUser), "the old email's index is gone")
		stale := e.Version
		Expect(s.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &stale})).To(MatchError(op.ErrConcurrentModification))
	})

	It("writes a user under the version it read, so a racing write wins", func(ctx SpecContext) {
		e := get(ctx, "user", "alice")
		var u meta.UserCompleteInfo
		Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
		u.Info.DisplayName = "Raced"
		data, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		race("users.uid", "alice")
		Expect(s.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrConcurrentModification))
		rec, err := s.GetUser(ctx, alice)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.DisplayName).To(Equal("ALICE"))
	})

	It("creates a user from a put exclusively and links it into its account", func(ctx SpecContext) {
		a := meta.NewAccountInfo()
		a.ID, a.Name = acctID, "acme"
		Expect(s.PutAccount(ctx, &op.AccountRecord{Info: a}, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "carol"}
		u.DisplayName = "Carol"
		u.AccountID = acctID
		data, err := json.Marshal(meta.UserCompleteInfo{Info: u})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Put(ctx, "user", "carol", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
		ids, _, err := s.ListAccountUsers(ctx, acctID, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(ConsistOf("carol"))
		zero := meta.ObjVersion{}
		Expect(s.Put(ctx, "user", "carol", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &zero})).
			To(MatchError(op.ErrConcurrentModification), "the put read a user where the caller read none")
	})

	DescribeTable("refuses a user document before writing anything",
		func(ctx SpecContext, key string, edit func(u *meta.UserCompleteInfo), want error) {
			e := get(ctx, "user", "alice")
			var u meta.UserCompleteInfo
			Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
			edit(&u)
			data, err := json.Marshal(u)
			Expect(err).NotTo(HaveOccurred())
			before := c.Writes(rookMetaPool, "users.uid", "alice")
			Expect(s.Put(ctx, "user", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(want))
			Expect(c.Writes(rookMetaPool, "users.uid", "alice")).To(Equal(before), "alice's object is not written")
			Expect(c.Object(rookMetaPool, "users.uid", "dave")).To(BeNil(), "no other user is written")
		},
		Entry("a document naming another user than the key", "dave", func(*meta.UserCompleteInfo) {}, op.ErrInvalidArgument),
		Entry("an email another user holds", "alice", func(u *meta.UserCompleteInfo) { u.Info.Email = "BOB@example.com" }, op.ErrEmailExists),
		Entry("a key without a secret", "alice", func(u *meta.UserCompleteInfo) {
			u.Info.AccessKeys["AKnew"] = meta.AccessKey{ID: "AKnew", Active: true}
		}, op.ErrInvalidSecretKey),
		Entry("an account that does not exist", "alice", func(u *meta.UserCompleteInfo) { u.Info.AccountID = acctID }, op.ErrInvalidArgument),
		Entry("a key id another user holds, the document's copy inactive", "alice", func(u *meta.UserCompleteInfo) {
			u.Info.AccessKeys["AKbob"] = meta.AccessKey{ID: "AKbob", Secret: "mine", Active: false}
		}, op.ErrKeyExists),
		Entry("a root user outside an account", "alice", func(u *meta.UserCompleteInfo) { u.Info.Type = meta.IdentityRoot }, op.ErrInvalidArgument),
		Entry("a user id in an account id's form", "RGW00000000000000002", func(u *meta.UserCompleteInfo) {
			u.Info.UserID = meta.UserID{ID: "RGW00000000000000002"}
		}, op.ErrInvalidArgument),
		Entry("a tenant in an account id's form", "RGW00000000000000002$alice", func(u *meta.UserCompleteInfo) {
			u.Info.UserID = meta.UserID{Tenant: "RGW00000000000000002", ID: "alice"}
		}, op.ErrInvalidArgument),
		Entry("an attr outside user.rgw., which would replace the object's version", "alice", func(u *meta.UserCompleteInfo) {
			u.Attrs, u.HasAttrs = meta.AttrsJSON{"ceph.objclass.version": []byte("garbage")}, true
		}, op.ErrInvalidArgument),
	)

	It("refuses a key id another user holds before it links the user into its account", func(ctx SpecContext) {
		a := meta.NewAccountInfo()
		a.ID, a.Name = acctID, "acme"
		Expect(s.PutAccount(ctx, &op.AccountRecord{Info: a}, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		u := meta.NewUserInfo()
		u.UserID, u.DisplayName, u.AccountID = meta.UserID{ID: "carol"}, "Carol", acctID
		u.AccessKeys = map[string]meta.AccessKey{"AKbob": {ID: "AKbob", Secret: "mine", Active: false}}
		data, err := json.Marshal(meta.UserCompleteInfo{Info: u})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Put(ctx, "user", "carol", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrKeyExists))
		ids, _, err := s.ListAccountUsers(ctx, acctID, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(BeEmpty(), "nothing written, the account's index included")
	})

	It("refuses a reactivation of a key another user holds before it relinks the user in its account", func(ctx SpecContext) {
		a := meta.NewAccountInfo()
		a.ID, a.Name = acctID, "acme"
		Expect(s.PutAccount(ctx, &op.AccountRecord{Info: a}, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		u := meta.NewUserInfo()
		u.UserID, u.DisplayName, u.AccountID = meta.UserID{ID: "carol"}, "Carol", acctID
		u.AccessKeys = map[string]meta.AccessKey{"AKY": {ID: "AKY", Secret: "c", Active: false}}
		seedUser(c, u, nil, meta.ObjVersion{Ver: 1, Tag: "_c"})
		Expect(s.AddAccountUser(ctx, acctID, u)).To(Succeed())
		c.Put(rookMetaPool, "users.keys.rgw-go-key-holders", "AKY", encode(meta.UID("bob")))
		u.DisplayName = "Caroline"
		u.AccessKeys["AKY"] = meta.AccessKey{ID: "AKY", Secret: "c", Active: true}
		data, err := json.Marshal(meta.UserCompleteInfo{Info: u})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Put(ctx, "user", "carol", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrKeyExists))
		ids, _, err := s.ListAccountUsers(ctx, acctID, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(Equal([]string{"carol"}), "no entry under the new name")
	})

	It("refuses an account of another tenant", func(ctx SpecContext) {
		a := meta.NewAccountInfo()
		a.ID, a.Tenant, a.Name = acctID, "t9", "acme"
		Expect(s.PutAccount(ctx, &op.AccountRecord{Info: a}, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		e := get(ctx, "user", "alice")
		var u meta.UserCompleteInfo
		Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
		u.Info.AccountID = acctID
		data, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
	})

	It("creates a user exclusively, so a create racing it wins", func(ctx SpecContext) {
		u := meta.NewUserInfo()
		u.UserID, u.DisplayName = meta.UserID{ID: "carol"}, "Carol"
		data, err := json.Marshal(meta.UserCompleteInfo{Info: u})
		Expect(err).NotTo(HaveOccurred())
		c.BeforeWrite(rookMetaPool, "users.uid", "carol", func(*fakerados.Object) {
			c.BeforeWrite(rookMetaPool, "users.uid", "carol", nil)
			c.Put(rookMetaPool, "users.uid", "carol", []byte("the racer's"))
		})
		Expect(s.Put(ctx, "user", "carol", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrUserAlreadyExists))
		Expect(c.Object(rookMetaPool, "users.uid", "carol").Data).To(Equal([]byte("the racer's")))
	})

	It("names no credential in its errors or logs", func(ctx SpecContext) {
		var logs strings.Builder
		DeferCleanup(driver.CaptureLog(&logs))
		e := get(ctx, "user", "alice")
		var u meta.UserCompleteInfo
		Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
		u.Info.Email = "bob@example.com"
		u.Info.AccessKeys["AKSECRETID"] = meta.AccessKey{ID: "AKSECRETID", Secret: "SKSECRETVALUE", Active: true}
		data, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		var errs []error
		errs = append(errs, s.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{}))
		errs = append(errs, s.Put(ctx, "user", "alice", op.MetadataEntry{Data: json.RawMessage(
			`{"user_id":"alice","keys":[{"access_key":"AKSECRETID","secret_key":"SKSECRETVALUE","create_date":"SKSECRETVALUE"}]}`)}, op.PutMetadataOptions{}))
		errs = append(errs, s.Put(ctx, "user", "alice", op.MetadataEntry{Data: json.RawMessage(
			`{"user_id":"alice","keys":[{"access_key":"AKSECRETID","secret_key":SKSECRETVALUE}]}`)}, op.PutMetadataOptions{}))
		for i, err := range errs {
			Expect(err).To(HaveOccurred(), "put %d", i)
			Expect(err.Error()).NotTo(ContainSubstring("SKSECRETVALUE"), "put %d", i)
			Expect(err.Error()).NotTo(ContainSubstring("AKSECRETID"), "put %d", i)
			Expect(err.Error()).NotTo(ContainSubstring("bob@example.com"), "put %d", i)
		}
		Expect(logs.String()).NotTo(ContainSubstring("SKSECRETVALUE"))
		Expect(logs.String()).NotTo(ContainSubstring("AKSECRETID"))
	})

	It("refuses an email an account holds", func(ctx SpecContext) {
		a := meta.NewAccountInfo()
		a.ID, a.Name, a.Email = acctID, "acme", "ops@acme.example"
		Expect(s.PutAccount(ctx, &op.AccountRecord{Info: a}, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
		e := get(ctx, "user", "alice")
		var u meta.UserCompleteInfo
		Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
		u.Info.Email = "Ops@Acme.example"
		data, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrEmailExists))
		got, err := s.GetAccountByEmail(ctx, "ops@acme.example")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Info.ID).To(Equal(acctID), "the account keeps its email")
	})

	DescribeTable("refuses a document that does not decode, writing nothing",
		func(ctx SpecContext, section, oidNS, oid, doc string) {
			key := map[string]string{"user": "alice", "bucket": "plain", "bucket.instance": "plain:" + plain.Info.Bucket.ID}[section]
			if section == "bucket.instance" {
				oid = ".bucket.meta." + key
			}
			before := c.Writes(rookMetaPool, oidNS, oid)
			Expect(s.Put(ctx, section, key, op.MetadataEntry{Data: json.RawMessage(doc)}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
			Expect(c.Writes(rookMetaPool, oidNS, oid)).To(Equal(before))
		},
		Entry("a user's max_buckets past int's range", "user", "users.uid", "alice", `{"user_id":"alice","max_buckets":99999999999}`),
		Entry("an entry point's linked that is no boolean", "bucket", rookRoot, "plain", `{"bucket":{"name":"plain"},"linked":"maybe"}`),
		Entry("an instance's negative shard count", "bucket.instance", rookRoot, "", `{"bucket_info":{"num_shards":-1}}`),
		Entry("text that is no JSON", "user", "users.uid", "alice", `{"user_id":`),
	)

	It("serves bucket entry points and bucket instances under radosgw's keys", func(ctx SpecContext) {
		e := get(ctx, "bucket", "plain")
		var ep meta.BucketEntryPoint
		Expect(json.Unmarshal(e.Data, &ep)).To(Succeed())
		Expect(ep.Bucket.ID).To(Equal(plain.Info.Bucket.ID))
		Expect(e.Doc).To(BeAssignableToTypeOf(meta.BucketEntryPoint{}))
		Expect(e.Version).To(Equal(plain.EPVersion))
		key := "plain:" + plain.Info.Bucket.ID
		e = get(ctx, "bucket.instance", key)
		var bi meta.BucketCompleteInfo
		Expect(json.Unmarshal(e.Data, &bi)).To(Succeed())
		Expect(bi.Info.Bucket.Name).To(Equal("plain"))
		Expect(bi.Attrs).To(HaveKey(meta.AttrACL))
		Expect(e.Doc).To(BeAssignableToTypeOf(meta.BucketCompleteInfo{}))
		Expect(e.Version).To(Equal(plain.Version))
		keys, _, _, err := s.List(ctx, "bucket", "", 1000)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(ConsistOf("plain"))
		keys, _, _, err = s.List(ctx, "bucket.instance", "", 1000)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(ConsistOf(key))
		tb := createBucket(ctx, "t1", "tb", alice)
		keys, _, _, err = s.List(ctx, "bucket", "", 1000)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(ConsistOf("plain", "t1/tb"))
		keys, _, _, err = s.List(ctx, "bucket.instance", "", 1000)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(ContainElement("t1/tb:" + tb.Info.Bucket.ID))
		e = get(ctx, "bucket.instance", "t1/tb:"+tb.Info.Bucket.ID)
		Expect(docOf[meta.BucketCompleteInfo](e).Info.Bucket.Tenant).To(Equal("t1"))
		_, err = s.Get(ctx, "bucket", "nosuch")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		_, err = s.Get(ctx, "bucket.instance", "plain:nosuch")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		_, err = s.Get(ctx, "bucket.instance", "plain")
		Expect(err).To(MatchError(op.ErrNoSuchKey), "a key naming no instance")
	})

	It("answers a bucket instance carried opaque with ErrOpaqueJSON", func(ctx SpecContext) {
		info := plain.Info
		info.Website = meta.RawStruct{2, 1, 0, 0, 0, 0}
		Expect(driver.WriteInstance(s, ctx, info, false, meta.ObjVersion{Ver: 9, Tag: "w"})).To(Succeed())
		_, err := s.Get(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID)
		Expect(err).To(MatchError(meta.ErrOpaqueJSON))
	})

	Describe("a NUL byte, which go-ceph cuts an object name at", func() {
		It("refuses a key holding one in every section and op, removing nothing", func(ctx SpecContext) {
			id := plain.Info.Bucket.ID
			inst := get(ctx, "bucket.instance", "plain:"+id)
			ep := get(ctx, "bucket", "plain")
			user := get(ctx, "user", "alice")
			for _, k := range []struct {
				section, key string
				data         json.RawMessage
			}{
				{"bucket", "plain\x00x", ep.Data},
				{"bucket.instance", "plain:" + id + "\x00x", inst.Data},
				{"user", "alice\x00x", user.Data},
			} {
				_, err := s.Get(ctx, k.section, k.key)
				Expect(err).To(MatchError(op.ErrInvalidRequest), "get %s", k.section)
				Expect(s.Put(ctx, k.section, k.key, op.MetadataEntry{Data: k.data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidRequest), "put %s", k.section)
				Expect(s.Remove(ctx, k.section, k.key)).To(MatchError(op.ErrInvalidRequest), "remove %s", k.section)
			}
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.plain:"+id)).NotTo(BeNil(), "plain's instance, which the cut key names, stays")
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil())
		})

		It("refuses a document naming one in a bucket id, marker, placement pool, email or key id", func(ctx SpecContext) {
			var (
				data []byte
				err  error
			)
			for _, edit := range []func(*meta.BucketEntryPoint){
				func(ep *meta.BucketEntryPoint) { ep.Bucket.ID += "\x00x" },
				func(ep *meta.BucketEntryPoint) { ep.Owner = meta.UserOwner(meta.UserID{ID: "alice\x00x"}) },
				func(ep *meta.BucketEntryPoint) { ep.Owner = meta.AccountOwner(acctID + "\x00x") },
			} {
				epDoc := docOf[meta.BucketEntryPoint](get(ctx, "bucket", "plain"))
				edit(&epDoc)
				data, err = json.Marshal(epDoc)
				Expect(err).NotTo(HaveOccurred())
				Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(And(MatchError(op.ErrInvalidArgument), MatchError(ContainSubstring("NUL byte"))))
			}

			for _, edit := range []func(*meta.BucketInfo){
				func(i *meta.BucketInfo) { i.Bucket.Marker += "\x00x" },
				func(i *meta.BucketInfo) { i.Bucket.ExplicitPlacement.DataPool = meta.Pool{Name: "data\x00x"} },
				func(i *meta.BucketInfo) { i.Owner = meta.UserOwner(meta.UserID{ID: "alice\x00x"}) },
			} {
				bi := docOf[meta.BucketCompleteInfo](get(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID))
				edit(&bi.Info)
				data, err = json.Marshal(bi)
				Expect(err).NotTo(HaveOccurred())
				Expect(s.Put(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(And(MatchError(op.ErrInvalidArgument), MatchError(ContainSubstring("NUL byte"))))
			}

			bi := docOf[meta.BucketCompleteInfo](get(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID))
			bi.Attrs[meta.AttrACL+"\x00x"] = []byte("v")
			data, err = json.Marshal(bi)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Put(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).
				To(MatchError(ContainSubstring("NUL byte")), "an attr name, which would set the ACL")

			for _, edit := range []func(*meta.UserInfo){
				func(u *meta.UserInfo) { u.Email = "alice@example.com\x00x" },
				func(u *meta.UserInfo) { u.AccountID = acctID + "\x00x" },
				func(u *meta.UserInfo) {
					u.AccessKeys["AK\x00x"] = meta.AccessKey{ID: "AK\x00x", Secret: "s", Active: true}
				},
			} {
				u := docOf[meta.UserCompleteInfo](get(ctx, "user", "alice"))
				edit(&u.Info)
				data, err = json.Marshal(u)
				Expect(err).NotTo(HaveOccurred())
				Expect(s.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(And(MatchError(op.ErrInvalidArgument), MatchError(ContainSubstring("NUL byte"))))
			}
		})
	})

	Describe("an entry point put", func() {
		epDoc := func(ctx context.Context, edit func(*meta.BucketEntryPoint)) (json.RawMessage, meta.ObjVersion) {
			GinkgoHelper()
			e := get(ctx, "bucket", "plain")
			var ep meta.BucketEntryPoint
			Expect(json.Unmarshal(e.Data, &ep)).To(Succeed())
			edit(&ep)
			data, err := json.Marshal(ep)
			Expect(err).NotTo(HaveOccurred())
			return data, e.Version
		}

		It("unlinks and links its owner's list as linked changes", func(ctx SpecContext) {
			data, v := epDoc(ctx, func(ep *meta.BucketEntryPoint) { ep.Linked = false })
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &v})).To(Succeed())
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(BeEmpty())
			data, v = epDoc(ctx, func(ep *meta.BucketEntryPoint) { ep.Linked = true })
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &v})).To(Succeed())
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("plain"))
		})

		It("links the list again on a retry of a put that stopped after its write", func(ctx SpecContext) {
			Expect(s.UnlinkBucketOwner(ctx, plain, meta.UserOwner(alice))).To(Succeed())
			data, _ := epDoc(ctx, func(ep *meta.BucketEntryPoint) { ep.Linked = true })
			pool, oid := driver.OwnerBucketsObj(s, meta.UserOwner(alice))
			c.FailNextWrite(pool.Name, pool.NS, oid, 5)
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).NotTo(Succeed())
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(BeEmpty(), "the entry point is written, the list is not")
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("plain"), "the retry links the list")
		})

		It("moves the list to the instance's owner from an entry point that named another", func(ctx SpecContext) {
			ep := plain.EntryPoint
			ep.Owner = meta.UserOwner(bob)
			_, err := driver.WriteEntryPoint(s, ctx, ep, false, plain.EPVersion, meta.ObjVersion{Ver: 7, Tag: "legacy"})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.LinkBucketForTest(ctx, meta.UserOwner(bob), plain.Info.Bucket)).To(Succeed())
			data, v := epDoc(ctx, func(ep *meta.BucketEntryPoint) { ep.Owner = meta.UserOwner(alice) })
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &v})).To(Succeed())
			Expect(listOwnerBuckets(ctx, meta.UserOwner(bob))).To(BeEmpty())
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("plain"))
		})

		DescribeTable("refuses, writing nothing",
			func(ctx SpecContext, key string, edit func(*meta.BucketEntryPoint), want error) {
				data, _ := epDoc(ctx, edit)
				before := c.Writes(rookMetaPool, rookRoot, "plain")
				Expect(s.Put(ctx, "bucket", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(want))
				Expect(c.Writes(rookMetaPool, rookRoot, "plain")).To(Equal(before))
				Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("plain"))
				Expect(listOwnerBuckets(ctx, meta.UserOwner(bob))).To(BeEmpty())
			},
			Entry("an owner other than the instance's", "plain", func(ep *meta.BucketEntryPoint) { ep.Owner = meta.UserOwner(meta.UserID{ID: "bob"}) }, op.ErrInvalidArgument),
			Entry("another bucket id", "plain", func(ep *meta.BucketEntryPoint) { ep.Bucket.ID = "other" }, op.ErrBucketAlreadyExists),
			Entry("a document naming another bucket than the key", "other", func(*meta.BucketEntryPoint) {}, op.ErrInvalidArgument),
			Entry("an entry point from before version 8", "plain", func(ep *meta.BucketEntryPoint) { ep.HasBucketInfo = true }, op.ErrInvalidArgument),
		)

		It("refuses a put racing another write of the entry point", func(ctx SpecContext) {
			data, _ := epDoc(ctx, func(ep *meta.BucketEntryPoint) { ep.Linked = false })
			race(rookRoot, "plain")
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrConcurrentModification))
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("plain"), "a lost race changes no list")
		})

		It("refuses a put whose version the caller read is stale", func(ctx SpecContext) {
			data, v := epDoc(ctx, func(ep *meta.BucketEntryPoint) { ep.Linked = false })
			stale := meta.ObjVersion{Ver: v.Ver - 1, Tag: v.Tag}
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &stale})).To(MatchError(op.ErrConcurrentModification))
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("plain"))
		})

		It("creates an entry point exclusively, so a create racing it wins", func(ctx SpecContext) {
			ep := plain.EntryPoint
			Expect(driver.RemoveEntryPoint(s, ctx, "", "plain", plain.EPVersion)).To(Succeed())
			data, err := json.Marshal(ep)
			Expect(err).NotTo(HaveOccurred())
			c.BeforeWrite(rookMetaPool, rookRoot, "plain", func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookRoot, "plain", nil)
				c.Put(rookMetaPool, rookRoot, "plain", []byte("the racer's"))
			})
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(c.Object(rookMetaPool, rookRoot, "plain").Data).To(Equal([]byte("the racer's")))
		})

		It("creates an entry point only for an instance that exists and no other name loads", func(ctx SpecContext) {
			ep := plain.EntryPoint
			Expect(driver.RemoveEntryPoint(s, ctx, "", "plain", plain.EPVersion)).To(Succeed())
			data, err := json.Marshal(ep)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
			Expect(docOf[meta.BucketEntryPoint](get(ctx, "bucket", "plain")).Bucket.ID).To(Equal(plain.Info.Bucket.ID))

			other := plain.Info
			other.Bucket.Name = "alias"
			Expect(driver.WriteInstance(s, ctx, other, true, meta.ObjVersion{Ver: 1, Tag: "a"})).To(Succeed())
			alias := ep
			alias.Bucket.Name = "alias"
			data, err = json.Marshal(alias)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Put(ctx, "bucket", "alias", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).
				To(MatchError(op.ErrBucketAlreadyExists), "plain loads the id already")
			Expect(c.Object(rookMetaPool, rookRoot, "alias")).To(BeNil())

			ghost := ep
			ghost.Bucket = meta.BucketID{Name: "ghost", Marker: "g.1", ID: "g.1"}
			data, err = json.Marshal(ghost)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Put(ctx, "bucket", "ghost", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
			Expect(c.Object(rookMetaPool, rookRoot, "ghost")).To(BeNil())
		})

		It("refuses a bucket a live rename holds", func(ctx SpecContext) {
			intent := op.RenameIntent{SrcTenant: "", SrcName: "plain", DstName: "renamed", Owner: "bob", From: []string{"alice"}}
			attrs := maps.Clone(plain.Attrs)
			attrs[op.RenameIntentAttr] = intent.Encode()
			rec := *plain
			rec.Attrs = attrs
			Expect(s.PutBucketInfo(ctx, &rec)).To(Succeed())
			data, _ := epDoc(ctx, func(ep *meta.BucketEntryPoint) { ep.Linked = false })
			Expect(s.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrRenamePending))
		})
	})

	Describe("the bucket claim", func() {
		const claims = rookRoot + ".rgw-go-bucket-claims"
		const (
			id      = "z.1.9"
			instOid = ".bucket.meta.fresh:" + id
		)
		fresh := func(name string, owner meta.UserID) json.RawMessage {
			GinkgoHelper()
			info := meta.NewBucketInfo()
			info.Bucket = meta.BucketID{Name: name, Marker: id, ID: id}
			info.Owner = meta.UserOwner(owner)
			info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
			info.Zonegroup = zgID
			data, err := json.Marshal(meta.BucketCompleteInfo{Info: info})
			Expect(err).NotTo(HaveOccurred())
			return data
		}
		putInst := func(ctx context.Context, name string, data json.RawMessage) error {
			return s.Put(ctx, "bucket.instance", name+":"+id, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})
		}
		// owned is fresh's stored instance document with owner as its owner.
		owned := func(ctx context.Context, owner meta.UserID) json.RawMessage {
			GinkgoHelper()
			bi := docOf[meta.BucketCompleteInfo](get(ctx, "bucket.instance", "fresh:"+id))
			bi.Info.Owner = meta.UserOwner(owner)
			data, err := json.Marshal(bi)
			Expect(err).NotTo(HaveOccurred())
			return data
		}
		putEP := func(ctx context.Context, owner meta.UserID) error {
			ep := meta.NewBucketEntryPoint()
			ep.Bucket = meta.BucketID{Name: "fresh", Marker: id, ID: id}
			ep.Owner = meta.UserOwner(owner)
			ep.Linked = true
			data, err := json.Marshal(ep)
			Expect(err).NotTo(HaveOccurred())
			return s.Put(ctx, "bucket", "fresh", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})
		}
		instOwner := func(ctx context.Context) string {
			GinkgoHelper()
			return docOf[meta.BucketCompleteInfo](get(ctx, "bucket.instance", "fresh:"+id)).Info.Owner.String()
		}

		It("gives one of two new instances naming one id under two names the id", func(ctx SpecContext) {
			c.BeforeWrite(rookMetaPool, claims, id, func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, claims, id, nil)
				Expect(putInst(ctx, "b", fresh("b", bob))).To(Succeed(), "the second put, between the first's scan and its claim")
			})
			Expect(putInst(ctx, "a", fresh("a", alice))).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.a:"+id)).To(BeNil(), "the first wrote no instance")
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.b:"+id)).NotTo(BeNil())
		})

		It("refuses a new instance whose id another instance carries as its marker", func(ctx SpecContext) {
			info := plain.Info
			info.Bucket.Marker = id
			Expect(driver.WriteInstance(s, ctx, info, false, meta.ObjVersion{Ver: 9, Tag: "l"})).To(Succeed(),
				"an instance a reshard before Reef left with its old id as its marker")
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(c.Object(rookMetaPool, rookRoot, instOid)).To(BeNil())
		})

		It("refuses an entry point put an instance put changes the owner under, between its check and its write", func(ctx SpecContext) {
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(Succeed())
			c.BeforeWrite(rookMetaPool, claims, id, func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, claims, id, nil)
				Expect(putInst(ctx, "fresh", owned(ctx, bob))).To(Succeed(), "no entry point names the instance yet")
			})
			Expect(putEP(ctx, alice)).To(MatchError(op.ErrConcurrentModification))
			Expect(c.Object(rookMetaPool, rookRoot, "fresh")).To(BeNil(), "no entry point written")
			Expect(instOwner(ctx)).To(Equal("bob"))
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("plain"))
		})

		It("refuses an instance put changing the owner that an entry point put lands under, between its check and its write", func(ctx SpecContext) {
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(Succeed())
			doc := owned(ctx, bob)
			c.BeforeWrite(rookMetaPool, claims, id, func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, claims, id, nil)
				Expect(putEP(ctx, alice)).To(Succeed())
			})
			Expect(putInst(ctx, "fresh", doc)).To(MatchError(op.ErrConcurrentModification))
			Expect(instOwner(ctx)).To(Equal("alice"))
			Expect(docOf[meta.BucketEntryPoint](get(ctx, "bucket", "fresh")).Owner.String()).To(Equal("alice"))
		})

		It("refuses an entry point put landing between an instance put's claim and its write", func(ctx SpecContext) {
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(Succeed())
			doc := owned(ctx, bob)
			c.BeforeWrite(rookMetaPool, rookRoot, instOid, func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookRoot, instOid, nil)
				Expect(putEP(ctx, alice)).To(MatchError(op.ErrConcurrentModification), "the claim names bob as the owner being written")
			})
			Expect(putInst(ctx, "fresh", doc)).To(Succeed())
			Expect(c.Object(rookMetaPool, rookRoot, "fresh")).To(BeNil())
			Expect(instOwner(ctx)).To(Equal("bob"))
		})

		It("reads the claim before the instance, so an owner change landing between is seen", func(ctx SpecContext) {
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(Succeed())
			doc := owned(ctx, bob)
			c.BeforeRead(rookMetaPool, claims, id, func() {
				c.BeforeRead(rookMetaPool, claims, id, nil)
				Expect(putInst(ctx, "fresh", doc)).To(Succeed())
			})
			Expect(putEP(ctx, alice)).To(MatchError(op.ErrInvalidArgument), "the instance it reads is bob's")
			Expect(c.Object(rookMetaPool, rookRoot, "fresh")).To(BeNil())
		})

		It("reads the claim before the entry point, so an entry point landing between is seen", func(ctx SpecContext) {
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(Succeed())
			doc := owned(ctx, bob)
			c.BeforeRead(rookMetaPool, claims, id, func() {
				c.BeforeRead(rookMetaPool, claims, id, nil)
				Expect(putEP(ctx, alice)).To(Succeed())
			})
			Expect(putInst(ctx, "fresh", doc)).To(MatchError(op.ErrInvalidArgument), "the entry point it reads is alice's")
			Expect(instOwner(ctx)).To(Equal("alice"))
		})

		It("refuses an entry point put whose claim another put creates after its instance read", func(ctx SpecContext) {
			info := meta.NewBucketInfo()
			Expect(json.Unmarshal(fresh("fresh", alice), &struct {
				Info *meta.BucketInfo `json:"bucket_info"`
			}{&info})).To(Succeed())
			Expect(driver.WriteInstance(s, ctx, info, true, meta.ObjVersion{Ver: 1, Tag: "r"})).To(Succeed(), "an instance without a claim, as radosgw writes one")
			doc := owned(ctx, bob)
			c.BeforeWrite(rookMetaPool, claims, id, func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, claims, id, nil)
				Expect(putInst(ctx, "fresh", doc)).To(Succeed())
			})
			Expect(putEP(ctx, alice)).To(MatchError(op.ErrConcurrentModification))
			Expect(c.Object(rookMetaPool, rookRoot, "fresh")).To(BeNil())
			Expect(instOwner(ctx)).To(Equal("bob"))
		})

		It("clears its mark when its write loses a version race, so a put of another owner goes through", func(ctx SpecContext) {
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(Succeed())
			doc := owned(ctx, alice)
			race(rookRoot, instOid)
			Expect(putInst(ctx, "fresh", doc)).To(MatchError(op.ErrConcurrentModification))
			var claim map[string]string
			Expect(json.Unmarshal(c.Object(rookMetaPool, claims, id).Data, &claim)).To(Succeed())
			Expect(claim).NotTo(HaveKey("pending_owner"), "the failed put's mark is cleared")
			Expect(putInst(ctx, "fresh", owned(ctx, bob))).To(Succeed())
		})

		It("leaves its mark when its context ends after the mark, as its write may still land", func(ctx SpecContext) {
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(Succeed())
			doc := owned(ctx, alice)
			pctx, cancel := context.WithCancel(ctx)
			defer cancel()
			c.AfterWrite(rookMetaPool, claims, id, func() {
				c.AfterWrite(rookMetaPool, claims, id, nil)
				cancel()
			})
			Expect(s.Put(pctx, "bucket.instance", "fresh:"+id, op.MetadataEntry{Data: doc}, op.PutMetadataOptions{})).NotTo(Succeed())
			var claim map[string]string
			Expect(json.Unmarshal(c.Object(rookMetaPool, claims, id).Data, &claim)).To(Succeed())
			Expect(claim).To(HaveKeyWithValue("pending_owner", "alice"), "the mark stays")
			Expect(putInst(ctx, "fresh", owned(ctx, bob))).To(MatchError(op.ErrConcurrentModification))
		})

		It("lets a retry of a put that stopped after its claim through, and no put naming another owner", func(ctx SpecContext) {
			Expect(putInst(ctx, "fresh", fresh("fresh", alice))).To(Succeed())
			c.FailNextWrite(rookMetaPool, rookRoot, instOid, 5)
			Expect(putInst(ctx, "fresh", owned(ctx, alice))).NotTo(Succeed(), "stopped with alice's write pending")
			Expect(putInst(ctx, "fresh", owned(ctx, bob))).To(MatchError(op.ErrConcurrentModification))
			Expect(instOwner(ctx)).To(Equal("alice"))
			Expect(putInst(ctx, "fresh", owned(ctx, alice))).To(Succeed(), "the retry")
			Expect(putInst(ctx, "fresh", owned(ctx, bob))).To(Succeed(), "the retry cleared the claim")
		})

		// reput writes section key's stored document back, as metadata sync
		// or a get and put does.
		reput := func(ctx context.Context, section, key string) error {
			e := get(ctx, section, key)
			return s.Put(ctx, section, key, op.MetadataEntry{Data: e.Data}, op.PutMetadataOptions{})
		}
		claimOf := func() map[string]string {
			GinkgoHelper()
			o := c.Object(rookMetaPool, claims, plain.Info.Bucket.ID)
			Expect(o).NotTo(BeNil())
			var got map[string]string
			Expect(json.Unmarshal(o.Data, &got)).To(Succeed())
			return got
		}

		It("moves a renamed bucket's claim to its new name before the rename writes there", func(ctx SpecContext) {
			Expect(reput(ctx, "bucket", "plain")).To(Succeed(), "the put claims the id for plain")
			var atWrite map[string]string
			c.BeforeWrite(rookMetaPool, rookRoot, "moved", func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookRoot, "moved", nil)
				atWrite = claimOf()
			})
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChangeBucketOwner(ctx, rec, meta.UserOwner(alice), "ALICE", &meta.BucketID{Name: "moved"})).To(Succeed())
			Expect(atWrite).To(HaveKeyWithValue("bucket", "moved"), "moved before the new entry point is written")
			Expect(claimOf()).To(Equal(map[string]string{"bucket": "moved"}), "the old name dropped once the rename is done")
			Expect(reput(ctx, "bucket", "moved")).To(Succeed())
			Expect(reput(ctx, "bucket.instance", "moved:"+plain.Info.Bucket.ID)).To(Succeed())
		})

		It("leaves neither name blocked when a rename stops after moving the claim, and lets its retry finish", func(ctx SpecContext) {
			Expect(reput(ctx, "bucket", "plain")).To(Succeed())
			c.FailNextWrite(rookMetaPool, rookRoot, "moved", 5)
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChangeBucketOwner(ctx, rec, meta.UserOwner(alice), "ALICE", &meta.BucketID{Name: "moved"})).NotTo(Succeed())
			Expect(claimOf()).To(HaveKeyWithValue("from", "plain"))
			Expect(reput(ctx, "bucket", "plain")).To(Succeed(), "the old name's puts still find the claim theirs")
			Expect(reput(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID)).To(Succeed())
			rec, err = s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChangeBucketOwner(ctx, rec, meta.UserOwner(alice), "ALICE", &meta.BucketID{Name: "moved"})).To(Succeed(), "the retry")
			Expect(reput(ctx, "bucket", "moved")).To(Succeed())
			Expect(reput(ctx, "bucket.instance", "moved:"+plain.Info.Bucket.ID)).To(Succeed())
		})

		It("leaves the old name in a claim a put has marked when the rename ends, so that put's clear goes through", func(ctx SpecContext) {
			Expect(reput(ctx, "bucket", "plain")).To(Succeed())
			moved := plain.Info.Bucket
			moved.Name = "moved"
			var clear func() error
			c.AfterWrite(rookMetaPool, rookRoot, ".bucket.meta.moved:"+plain.Info.Bucket.ID, func() {
				c.AfterWrite(rookMetaPool, rookRoot, ".bucket.meta.moved:"+plain.Info.Bucket.ID, nil)
				var err error
				clear, err = driver.HoldClaimForTest(s, ctx, moved, meta.UserOwner(alice))
				Expect(err).NotTo(HaveOccurred(), "a put of moved marks the claim as the rename writes there")
			})
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChangeBucketOwner(ctx, rec, meta.UserOwner(alice), "ALICE", &meta.BucketID{Name: "moved"})).To(Succeed())
			Expect(clear).NotTo(BeNil())
			Expect(claimOf()).To(HaveKeyWithValue("from", "plain"), "the rename leaves the marked claim alone")
			Expect(clear()).To(Succeed())
			Expect(claimOf()).NotTo(HaveKey("pending_owner"), "the put's clear lands")
		})

		It("keeps the claim through a link, a chown and an unlink, which change neither the name nor the id", func(ctx SpecContext) {
			Expect(reput(ctx, "bucket", "plain")).To(Succeed())
			before := claimOf()
			rec, err := s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChangeBucketOwner(ctx, rec, meta.UserOwner(bob), "BOB", nil)).To(Succeed(), "a link")
			Expect(reput(ctx, "bucket", "plain")).To(Succeed())
			rec, err = s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ChownBucket(ctx, rec, meta.UserOwner(alice), "ALICE")).To(Succeed(), "a chown")
			Expect(reput(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID)).To(Succeed())
			rec, err = s.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(s.UnlinkBucketOwner(ctx, rec, meta.UserOwner(alice))).To(Succeed(), "an unlink")
			Expect(reput(ctx, "bucket", "plain")).To(Succeed())
			Expect(claimOf()).To(HaveKeyWithValue("bucket", before["bucket"]))
		})

		It("names the claim by its kind, never by the bucket id, in errors", func(ctx SpecContext) {
			c.FailNextRead(rookMetaPool, claims, id, 5)
			err := putInst(ctx, "fresh", fresh("fresh", alice))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("bucket claim"))
			Expect(err.Error()).NotTo(ContainSubstring(id))
		})
	})

	Describe("a bucket instance put", func() {
		instDoc := func(ctx context.Context, key string, edit func(*meta.BucketCompleteInfo)) (json.RawMessage, meta.ObjVersion) {
			GinkgoHelper()
			e := get(ctx, "bucket.instance", key)
			var bi meta.BucketCompleteInfo
			Expect(json.Unmarshal(e.Data, &bi)).To(Succeed())
			edit(&bi)
			data, err := json.Marshal(bi)
			Expect(err).NotTo(HaveOccurred())
			return data, e.Version
		}
		freshInfo := func(name, id string) meta.BucketInfo {
			info := meta.NewBucketInfo()
			info.Bucket = meta.BucketID{Name: name, Marker: id, ID: id}
			info.Owner = meta.UserOwner(alice)
			info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
			info.Zonegroup = zgID
			info.Layout.Current.Layout.Normal.NumShards = 3
			return info
		}

		It("creates a bucket instance from a put and initializes its index", func(ctx SpecContext) {
			data, err := json.Marshal(meta.BucketCompleteInfo{Info: freshInfo("fresh", "z.1.9")})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Put(ctx, "bucket.instance", "fresh:z.1.9", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
			got, err := s.GetBucketInstance(ctx, meta.BucketID{Name: "fresh", ID: "z.1.9"})
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Layout.Current.Layout.Type).To(Equal(meta.IndexNormal))
			Expect(got.Info.Layout.Current.Layout.Normal.NumShards).To(Equal(uint32(3)))
			Expect(shardsExist(&got.Info)).To(BeTrue())
			Expect(s.Put(ctx, "bucket.instance", "fresh:z.1.9", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed(),
				"a retry finds the instance and its shards and writes it again")
		})

		It("takes the bucket's tenant, name and id from the key", func(ctx SpecContext) {
			info := freshInfo("elsewhere", "z.1.9")
			info.Bucket.Tenant = "t9"
			data, err := json.Marshal(meta.BucketCompleteInfo{Info: info})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Put(ctx, "bucket.instance", "t1/fresh:z.1.9", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
			got := docOf[meta.BucketCompleteInfo](get(ctx, "bucket.instance", "t1/fresh:z.1.9")).Info.Bucket
			Expect(got.Tenant).To(Equal("t1"))
			Expect(got.Name).To(Equal("fresh"))
		})

		DescribeTable("refuses a new instance, writing nothing",
			func(ctx SpecContext, key string, edit func(*meta.BucketInfo), want error) {
				info := freshInfo("fresh", "z.1.9")
				edit(&info)
				data, err := json.Marshal(meta.BucketCompleteInfo{Info: info})
				Expect(err).NotTo(HaveOccurred())
				Expect(s.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(want))
				id, _, _ := strings.Cut(strings.TrimPrefix(key, "fresh:"), "/")
				Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.fresh:"+id)).To(BeNil())
			},
			Entry("a marker that is not its id", "fresh:z.1.9", func(i *meta.BucketInfo) { i.Bucket.Marker = "z.1.1" }, op.ErrInvalidArgument),
			Entry("a placement rule the zone lacks", "fresh:z.1.9", func(i *meta.BucketInfo) { i.PlacementRule.Name = "nosuch" }, op.ErrInvalidArgument),
			Entry("no placement rule", "fresh:z.1.9", func(i *meta.BucketInfo) { i.PlacementRule = meta.PlacementRule{} }, op.ErrInvalidArgument),
			Entry("a key naming no instance", "fresh", func(*meta.BucketInfo) {}, op.ErrInvalidArgument),
		)

		It("refuses a new instance whose id another instance carries", func(ctx SpecContext) {
			id := plain.Info.Bucket.ID
			data, err := json.Marshal(meta.BucketCompleteInfo{Info: freshInfo("alias", id)})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Put(ctx, "bucket.instance", "alias:"+id, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.alias:"+id)).To(BeNil())
		})

		It("keeps a stored instance's placement and the attrs rgw-go keeps for itself", func(ctx SpecContext) {
			key := "plain:" + plain.Info.Bucket.ID
			rec := *plain
			rec.Info.Layout.Current.Gen = 2
			rec.Info.Layout.Logs = append(rec.Info.Layout.Logs, meta.LogLayoutFromIndex(1, rec.Info.Layout.Current))
			Expect(s.PutBucketInfo(ctx, &rec)).To(Succeed())
			data, v := instDoc(ctx, key, func(bi *meta.BucketCompleteInfo) {
				bi.Info.RequesterPays = true
				bi.Info.PlacementRule = meta.PlacementRule{Name: "elsewhere"}
				bi.Attrs[op.RemovingAttr] = []byte("planted")
			})
			Expect(s.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &v})).To(Succeed())
			got, err := s.GetBucketInstance(ctx, plain.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.RequesterPays).To(BeTrue())
			Expect(got.Info.PlacementRule).To(Equal(plain.Info.PlacementRule))
			Expect(got.Attrs).NotTo(HaveKey(op.RemovingAttr), "a document cannot plant rgw-go's own attrs")
			Expect(got.Info.Layout).To(Equal(rec.Info.Layout), "the generation and logs the document cannot carry")
		})

		It("keeps a void rename record the stored instance carries, and refuses one a removal claims", func(ctx SpecContext) {
			createBucket(ctx, "", "taken", bob)
			key := "plain:" + plain.Info.Bucket.ID
			rec := *plain
			rec.Attrs = maps.Clone(plain.Attrs)
			rec.Attrs[op.RenameIntentAttr] = op.RenameIntent{SrcName: "plain", DstName: "taken", Owner: "bob"}.Encode()
			Expect(s.PutBucketInfo(ctx, &rec)).To(Succeed())
			data, v := instDoc(ctx, key, func(bi *meta.BucketCompleteInfo) {
				bi.Info.RequesterPays = true
				delete(bi.Attrs, op.RenameIntentAttr)
			})
			Expect(s.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &v})).To(Succeed())
			got, err := s.GetBucketInstance(ctx, plain.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Attrs).To(HaveKey(op.RenameIntentAttr), "the stored record stays")
			rec = *got
			rec.Attrs = maps.Clone(got.Attrs)
			rec.Attrs[op.RemovingAttr] = []byte("claim")
			Expect(s.PutBucketInfo(ctx, &rec)).To(Succeed())
			data, _ = instDoc(ctx, key, func(bi *meta.BucketCompleteInfo) { bi.Info.RequesterPays = false })
			Expect(s.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrConcurrentModification))
		})

		It("creates an instance exclusively, so a create racing it wins", func(ctx SpecContext) {
			data, err := json.Marshal(meta.BucketCompleteInfo{Info: freshInfo("fresh", "z.1.9")})
			Expect(err).NotTo(HaveOccurred())
			c.BeforeWrite(rookMetaPool, rookRoot, ".bucket.meta.fresh:z.1.9", func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookRoot, ".bucket.meta.fresh:z.1.9", nil)
				c.Put(rookMetaPool, rookRoot, ".bucket.meta.fresh:z.1.9", []byte("the racer's"))
			})
			Expect(s.Put(ctx, "bucket.instance", "fresh:z.1.9", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrConcurrentModification))
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.fresh:z.1.9").Data).To(Equal([]byte("the racer's")))
		})

		DescribeTable("refuses a change to a stored instance, writing nothing",
			func(ctx SpecContext, edit func(*meta.BucketCompleteInfo), want error) {
				key := "plain:" + plain.Info.Bucket.ID
				data, _ := instDoc(ctx, key, edit)
				before := c.Writes(rookMetaPool, rookRoot, ".bucket.meta."+key)
				Expect(s.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(want))
				Expect(c.Writes(rookMetaPool, rookRoot, ".bucket.meta."+key)).To(Equal(before))
			},
			Entry("another shard count", func(bi *meta.BucketCompleteInfo) { bi.Info.Layout.Current.Layout.Normal.NumShards++ }, op.ErrInvalidArgument),
			Entry("another marker", func(bi *meta.BucketCompleteInfo) { bi.Info.Bucket.Marker = "m2" }, op.ErrInvalidArgument),
			Entry("an owner other than the entry point's", func(bi *meta.BucketCompleteInfo) { bi.Info.Owner = meta.UserOwner(meta.UserID{ID: "bob"}) }, op.ErrInvalidArgument),
			Entry("an attr outside user.rgw., which would replace the object's version", func(bi *meta.BucketCompleteInfo) {
				bi.Attrs["ceph.objclass.version"] = []byte("garbage")
			}, op.ErrInvalidArgument),
		)

		It("refuses to turn object lock off", func(ctx SpecContext) {
			key := "plain:" + plain.Info.Bucket.ID
			rec := *plain
			rec.Info.Flags |= meta.BucketObjLockEnabled | meta.BucketVersioned
			Expect(s.PutBucketInfo(ctx, &rec)).To(Succeed())
			data, _ := instDoc(ctx, key, func(bi *meta.BucketCompleteInfo) { bi.Info.Flags &^= meta.BucketObjLockEnabled })
			Expect(s.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
		})

		It("refuses a put racing another write of the instance", func(ctx SpecContext) {
			key := "plain:" + plain.Info.Bucket.ID
			data, _ := instDoc(ctx, key, func(bi *meta.BucketCompleteInfo) { bi.Info.RequesterPays = true })
			race(rookRoot, ".bucket.meta."+key)
			Expect(s.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrConcurrentModification))
			got, err := s.GetBucketInstance(ctx, plain.Info.Bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.RequesterPays).To(BeFalse())
		})
	})

	Describe("removal", func() {
		It("removes a user and its indexes, refusing one whose bucket list holds a bucket", func(ctx SpecContext) {
			Expect(s.Remove(ctx, "user", "alice")).To(MatchError(op.ErrBucketAlreadyExists))
			_, err := s.GetUser(ctx, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Remove(ctx, "user", "bob")).To(Succeed())
			_, err = s.GetUser(ctx, bob)
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			_, err = s.GetUserByAccessKey(ctx, "AKbob")
			Expect(err).To(MatchError(op.ErrNoSuchUser))
			Expect(s.Remove(ctx, "user", "bob")).To(MatchError(op.ErrNoSuchKey))
		})

		It("removes an entry point only once its instance is gone", func(ctx SpecContext) {
			Expect(s.Remove(ctx, "bucket", "plain")).To(MatchError(op.ErrConcurrentModification))
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).NotTo(BeNil())
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("plain"))
			Expect(driver.RemoveInstance(s, ctx, plain.Info.Bucket)).To(Succeed())
			Expect(s.Remove(ctx, "bucket", "plain")).To(Succeed())
			Expect(c.Object(rookMetaPool, rookRoot, "plain")).To(BeNil())
			Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(BeEmpty())
			Expect(s.Remove(ctx, "bucket", "plain")).To(MatchError(op.ErrNoSuchKey))
		})

		It("removes only an instance no entry point names, under its version", func(ctx SpecContext) {
			key := "plain:" + plain.Info.Bucket.ID
			Expect(s.Remove(ctx, "bucket.instance", key)).To(MatchError(op.ErrConcurrentModification))
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta."+key)).NotTo(BeNil())
			stale := plain.Info
			stale.Bucket.ID, stale.Bucket.Marker = "old.1", "old.1"
			Expect(driver.WriteInstance(s, ctx, stale, true, meta.ObjVersion{Ver: 1, Tag: "s"})).To(Succeed())
			race(rookRoot, ".bucket.meta.plain:old.1")
			Expect(s.Remove(ctx, "bucket.instance", "plain:old.1")).To(MatchError(op.ErrConcurrentModification))
			claimed := stale
			claimed.Bucket.ID, claimed.Bucket.Marker = "old.2", "old.2"
			Expect(driver.WriteInstanceAttrs(s, ctx, claimed, map[string][]byte{op.RemovingAttr: []byte("claim")})).To(Succeed())
			Expect(s.Remove(ctx, "bucket.instance", "plain:old.2")).To(MatchError(op.ErrConcurrentModification), "a removal claims it")
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.plain:old.2")).NotTo(BeNil())
			Expect(s.Remove(ctx, "bucket.instance", "plain:old.1")).To(Succeed())
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.plain:old.1")).To(BeNil())
			Expect(s.Remove(ctx, "bucket.instance", "plain:old.1")).To(MatchError(op.ErrNoSuchKey))
			Expect(s.Remove(ctx, "kittens", "x")).To(MatchError(op.ErrNoSuchKey))
		})

		It("removes no instance on Tentacle, whose handler leaves them to trimming", func(ctx SpecContext) {
			rel := denc.Tentacle
			t, err := driver.Open(ctx, c, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{Release: &rel})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(t.Close)
			stale := plain.Info
			stale.Bucket.ID, stale.Bucket.Marker = "old.1", "old.1"
			Expect(driver.WriteInstance(t, ctx, stale, true, meta.ObjVersion{Ver: 1, Tag: "s"})).To(Succeed())
			Expect(t.Remove(ctx, "bucket.instance", "plain:old.1")).To(Succeed())
			Expect(c.Object(rookMetaPool, rookRoot, ".bucket.meta.plain:old.1")).NotTo(BeNil())
			Expect(t.Remove(ctx, "bucket.instance", "plain:nosuch")).To(MatchError(op.ErrNoSuchKey))
		})
	})

	It("pages listings with the seam's tokens and rejects a foreign marker", func(ctx SpecContext) {
		for i := range 5 {
			putUser(ctx, meta.UserID{ID: fmt.Sprintf("u%d", i)})
		}
		var all []string
		marker := ""
		for range 20 {
			keys, next, more, err := s.List(ctx, "user", marker, 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(keys)).To(BeNumerically("<=", 2))
			all = append(all, keys...)
			if !more {
				break
			}
			marker = next
		}
		Expect(all).To(ConsistOf("u0", "u1", "u2", "u3", "u4", "alice", "bob"))
		_, _, _, err := s.List(ctx, "user", "3:b55a9110:root::u0:head", 2)
		Expect(err).To(MatchError(op.ErrInvalidArgument))
		keys, next, more, err := s.List(ctx, "user", "", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(BeEmpty())
		Expect(next).To(BeEmpty())
		Expect(more).To(BeTrue(), "a key follows")
	})
})
