package memstore_test

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
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

var _ = Describe("metadata", func() {
	const acctID = "RGW00000000000000001"
	var (
		store *memstore.Store
		clk   *clock
		plain *op.BucketRecord
	)
	addUser := func(id string) {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: id}
		u.DisplayName = id
		u.Email = id + "@example.com"
		u.AccessKeys = map[string]meta.AccessKey{"AK" + id: {ID: "AK" + id, Secret: "SK" + id, Active: true}}
		store.AddUser(u)
	}
	get := func(ctx context.Context, section, key string) op.MetadataEntry {
		GinkgoHelper()
		e, err := store.Get(ctx, section, key)
		Expect(err).NotTo(HaveOccurred(), "%s:%s", section, key)
		return e
	}
	BeforeEach(func(ctx SpecContext) {
		store, clk = newStore()
		addUser("alice")
		addUser("bob")
		plain = mustCreate(ctx, store, "", "plain", owner("alice"))
	})

	It("serves the user section from the users, with radosgw's document", func(ctx SpecContext) {
		e := get(ctx, "user", "alice")
		Expect(e.Doc).To(BeAssignableToTypeOf(meta.UserCompleteInfo{}))
		var doc map[string]any
		Expect(json.Unmarshal(e.Data, &doc)).To(Succeed())
		Expect(doc).To(HaveKeyWithValue("user_id", "alice"))
		Expect(doc).To(HaveKey("attrs"))
		keys, next, more, err := store.List(ctx, "user", "", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{keys, next, more}).To(Equal([]any{[]string{"alice"}, "alice", true}))
		keys, _, more, err = store.List(ctx, "user", next, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{keys, more}).To(Equal([]any{[]string{"bob"}, false}))
		_, err = store.Get(ctx, "user", "nobody")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		_, err = store.Get(ctx, "otp", "x")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		_, _, _, err = store.List(ctx, "otp", "", 10)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
	})

	It("puts a user under the version it read, writing the document's version", func(ctx SpecContext) {
		e := get(ctx, "user", "alice")
		var u meta.UserCompleteInfo
		Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
		u.Info.Email = "alice2@example.com"
		data, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		write := meta.ObjVersion{Ver: e.Version.Ver + 1, Tag: e.Version.Tag}
		clk.t = start.Add(1e9)
		Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data, Version: write}, op.PutMetadataOptions{IfVersion: &e.Version})).To(Succeed())
		rec, err := store.GetUserByEmail(ctx, "alice2@example.com")
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Version).To(Equal(write))
		Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &e.Version})).
			To(MatchError(op.ErrConcurrentModification), "stale version")
	})

	DescribeTable("refuses a user document before writing anything",
		func(ctx SpecContext, key string, edit func(u *meta.UserCompleteInfo), want error) {
			e := get(ctx, "user", "alice")
			var u meta.UserCompleteInfo
			Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
			edit(&u)
			data, err := json.Marshal(u)
			Expect(err).NotTo(HaveOccurred())
			Expect(store.Put(ctx, "user", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(want))
			Expect(get(ctx, "user", "alice").Version).To(Equal(e.Version))
			_, err = store.GetUser(ctx, meta.UserID{ID: "dave"})
			Expect(err).To(MatchError(op.ErrNoSuchUser))
		},
		Entry("a document naming another user than the key", "dave", func(*meta.UserCompleteInfo) {}, op.ErrInvalidArgument),
		Entry("an email another user holds", "alice", func(u *meta.UserCompleteInfo) { u.Info.Email = "BOB@example.com" }, op.ErrEmailExists),
		Entry("a key without a secret", "alice", func(u *meta.UserCompleteInfo) {
			u.Info.AccessKeys["AKnew"] = meta.AccessKey{ID: "AKnew", Active: true}
		}, op.ErrInvalidSecretKey),
		Entry("an account that does not exist", "alice", func(u *meta.UserCompleteInfo) { u.Info.AccountID = acctID }, op.ErrInvalidArgument),
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

	It("refuses a key holding a NUL byte in every section and op, as the RADOS driver does", func(ctx SpecContext) {
		for _, section := range []string{"user", "bucket", "bucket.instance"} {
			key := "plain:" + plain.Info.Bucket.ID + "\x00x"
			_, err := store.Get(ctx, section, key)
			Expect(err).To(MatchError(op.ErrInvalidRequest), section)
			Expect(store.Put(ctx, section, key, op.MetadataEntry{Data: json.RawMessage(`{}`)}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidRequest), section)
			Expect(store.Remove(ctx, section, key)).To(MatchError(op.ErrInvalidRequest), section)
		}
		u := docOf[meta.UserCompleteInfo](get(ctx, "user", "alice"))
		u.Info.Email = "alice@example.com\x00x"
		data, err := json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
		u = docOf[meta.UserCompleteInfo](get(ctx, "user", "alice"))
		u.Info.AccountID = acctID + "\x00x"
		data, err = json.Marshal(u)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(ContainSubstring("NUL byte")), "an account id")
		ep := docOf[meta.BucketEntryPoint](get(ctx, "bucket", "plain"))
		ep.Owner = owner("alice\x00x")
		data, err = json.Marshal(ep)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(ContainSubstring("NUL byte")), "an entry point owner")
		bi := docOf[meta.BucketCompleteInfo](get(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID))
		bi.Info.Owner = owner("alice\x00x")
		data, err = json.Marshal(bi)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", "plain:"+plain.Info.Bucket.ID, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(ContainSubstring("NUL byte")), "an instance owner")
	})

	It("refuses a document that does not decode", func(ctx SpecContext) {
		for _, section := range []string{"user", "bucket", "bucket.instance"} {
			Expect(store.Put(ctx, section, "plain:"+plain.Info.Bucket.ID, op.MetadataEntry{Data: json.RawMessage(`{"user_id":`)}, op.PutMetadataOptions{})).
				To(MatchError(op.ErrInvalidArgument), section)
		}
	})

	It("removes a user, refusing one whose bucket list holds a bucket", func(ctx SpecContext) {
		Expect(store.Remove(ctx, "user", "alice")).To(MatchError(op.ErrBucketAlreadyExists))
		Expect(store.Remove(ctx, "user", "bob")).To(Succeed())
		_, err := store.GetUserByAccessKey(ctx, "AKbob")
		Expect(err).To(MatchError(op.ErrNoSuchUser))
		Expect(store.Remove(ctx, "user", "bob")).To(MatchError(op.ErrNoSuchKey))
	})

	It("serves entry points and instances under radosgw's keys", func(ctx SpecContext) {
		e := get(ctx, "bucket", "plain")
		Expect(docOf[meta.BucketEntryPoint](e).Bucket.ID).To(Equal(plain.Info.Bucket.ID))
		Expect(e.Version).To(Equal(plain.EPVersion))
		key := "plain:" + plain.Info.Bucket.ID
		e = get(ctx, "bucket.instance", key)
		Expect(docOf[meta.BucketCompleteInfo](e).Info.Bucket.Name).To(Equal("plain"))
		Expect(e.Version).To(Equal(plain.Version))
		tb := mustCreate(ctx, store, "t1", "tb", owner("alice"))
		keys, _, _, err := store.List(ctx, "bucket", "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(Equal([]string{"plain", "t1/tb"}))
		keys, _, _, err = store.List(ctx, "bucket.instance", "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(ConsistOf(key, "t1/tb:"+tb.Info.Bucket.ID))
		_, err = store.Get(ctx, "bucket.instance", "other:"+plain.Info.Bucket.ID)
		Expect(err).To(MatchError(op.ErrNoSuchKey), "an id under another name")
	})

	It("puts an entry point's linked flag and refuses an owner apart from the instance's", func(ctx SpecContext) {
		e := get(ctx, "bucket", "plain")
		ep := docOf[meta.BucketEntryPoint](e)
		ep.Linked = false
		data, err := json.Marshal(ep)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &e.Version})).To(Succeed())
		after := get(ctx, "bucket", "plain")
		Expect(docOf[meta.BucketEntryPoint](after).Linked).To(BeFalse())
		Expect(after.Version).To(Equal(meta.ObjVersion{Ver: e.Version.Ver + 1, Tag: e.Version.Tag}))
		ep.Owner = owner("bob")
		data, err = json.Marshal(ep)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
		ep.Owner, ep.Bucket.ID = owner("alice"), "other"
		data, err = json.Marshal(ep)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrBucketAlreadyExists))
		Expect(store.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &e.Version})).
			To(MatchError(op.ErrConcurrentModification))
		Expect(get(ctx, "bucket", "plain").Version).To(Equal(after.Version), "nothing written")
	})

	It("creates an instance from a put and names it with an entry point put", func(ctx SpecContext) {
		info := meta.NewBucketInfo()
		info.Bucket = meta.BucketID{Name: "fresh", Marker: "z.1.9", ID: "z.1.9"}
		info.Owner = owner("bob")
		info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
		bad := info
		bad.Bucket.Marker = "z.1.1"
		data, err := json.Marshal(meta.BucketCompleteInfo{Info: bad})
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", "fresh:z.1.9", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
		data, err = json.Marshal(meta.BucketCompleteInfo{Info: info})
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", "fresh:"+plain.Info.Bucket.ID, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).
			To(MatchError(op.ErrBucketAlreadyExists), "an id another instance carries")
		Expect(store.Put(ctx, "bucket.instance", "fresh:z.1.9", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
		_, err = store.GetBucket(ctx, "", "fresh")
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "an instance alone names no bucket")
		ep := meta.NewBucketEntryPoint()
		ep.Bucket, ep.Owner, ep.Linked = info.Bucket, owner("bob"), true
		data, err = json.Marshal(ep)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket", "fresh", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
		got, err := store.GetBucket(ctx, "", "fresh")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Info.Owner).To(Equal(owner("bob")))
		ents, _, _, err := store.ListUserBuckets(ctx, owner("bob"), "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(HaveLen(1))
	})

	It("keeps a stored instance's layout and placement, refusing a change to its layout", func(ctx SpecContext) {
		key := "plain:" + plain.Info.Bucket.ID
		e := get(ctx, "bucket.instance", key)
		bi := docOf[meta.BucketCompleteInfo](e)
		bi.Info.RequesterPays = true
		bi.Info.PlacementRule = meta.PlacementRule{Name: "elsewhere"}
		data, err := json.Marshal(bi)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &e.Version})).To(Succeed())
		got, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Info.RequesterPays).To(BeTrue())
		Expect(got.Info.PlacementRule).To(Equal(plain.Info.PlacementRule))
		bi.Info.Layout.Current.Layout.Normal.NumShards += 5
		data, err = json.Marshal(bi)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
		bi.Info.Layout = plain.Info.Layout
		bi.Info.Owner = owner("bob")
		data, err = json.Marshal(bi)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
		bi.Info.Owner = plain.Info.Owner
		bi.Attrs = meta.AttrsJSON{"ceph.objclass.version": []byte("garbage")}
		data, err = json.Marshal(bi)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", key, op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument),
			"an attr outside user.rgw.")
	})

	It("refuses a new instance whose id a stored instance carries as its marker", func(ctx SpecContext) {
		rec, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		rec.Info.Bucket.Marker = "m.old"
		Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
		info := meta.NewBucketInfo()
		info.Bucket = meta.BucketID{Name: "reuse", Marker: "m.old", ID: "m.old"}
		info.Owner = owner("alice")
		info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
		data, err := json.Marshal(meta.BucketCompleteInfo{Info: info})
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", "reuse:m.old", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(MatchError(op.ErrBucketAlreadyExists))
		_, err = store.Get(ctx, "bucket.instance", "reuse:m.old")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
	})

	It("removes only an instance no entry point names, and nothing on Tentacle", func(ctx SpecContext) {
		key := "plain:" + plain.Info.Bucket.ID
		Expect(store.Remove(ctx, "bucket.instance", key)).To(MatchError(op.ErrConcurrentModification))
		Expect(store.Remove(ctx, "bucket", "plain")).To(MatchError(op.ErrConcurrentModification))
		Expect(store.Remove(ctx, "bucket", "nosuch")).To(MatchError(op.ErrNoSuchKey))
		info := meta.NewBucketInfo()
		info.Bucket = meta.BucketID{Name: "stale", Marker: "s.1", ID: "s.1"}
		info.Owner = owner("alice")
		info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
		data, err := json.Marshal(meta.BucketCompleteInfo{Info: info})
		Expect(err).NotTo(HaveOccurred())
		Expect(store.Put(ctx, "bucket.instance", "stale:s.1", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
		Expect(store.Remove(ctx, "bucket.instance", "stale:s.1")).To(Succeed())
		Expect(store.Remove(ctx, "bucket.instance", "stale:s.1")).To(MatchError(op.ErrNoSuchKey))

		tentacle := memstore.New(memstore.Config{Release: denc.Tentacle})
		Expect(tentacle.Put(ctx, "bucket.instance", "stale:s.1", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
		Expect(tentacle.Remove(ctx, "bucket.instance", "stale:s.1")).To(Succeed())
		_, err = tentacle.Get(ctx, "bucket.instance", "stale:s.1")
		Expect(err).NotTo(HaveOccurred(), "Tentacle leaves the instance")
	})
})
