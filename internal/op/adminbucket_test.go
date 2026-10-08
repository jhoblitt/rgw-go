package op_test

import (
	"context"
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

var _ = Describe("admin bucket ops", func() {
	const acct = "RGW00000000000000001"
	var (
		store *memstore.Store
		env   *op.Env
	)
	encodeACL := func(owner meta.Owner, name string) []byte {
		e := denc.NewEncoder()
		acl.DefaultPolicy(owner, name).Encode(e, denc.Squid)
		return e.Bytes()
	}
	decodeACL := func(b []byte) acl.Policy {
		GinkgoHelper()
		d := denc.NewDecoder(b)
		p := acl.DecodePolicy(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return p
	}
	uid := func(id string) meta.UserID { return meta.UserID{ID: id} }
	createBucket := func(ctx context.Context, name, owner string) *op.BucketRecord {
		GinkgoHelper()
		o := meta.UserOwner(uid(owner))
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Name: name, Owner: o, Zonegroup: store.ZoneGroup().ID,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{meta.AttrACL: encodeACL(o, "This is "+owner)},
			Quota:     meta.Quota{MaxSize: -1, MaxObjects: -1},
		})
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	putObject := func(ctx context.Context, bucket, key string) {
		GinkgoHelper()
		rec, err := store.GetBucket(ctx, "", bucket)
		Expect(err).NotTo(HaveOccurred())
		_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: key}, strings.NewReader("hello"), op.PutParams{Size: 5})
		Expect(err).NotTo(HaveOccurred())
	}
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{})
		for _, u := range []struct{ id, caps, account string }{
			{"admin", "buckets=*;users=*", ""},
			{"test-user1", "", ""},
			{"test-user2", "", ""},
			{"acctuser", "", acct},
		} {
			info := meta.NewUserInfo()
			info.UserID = uid(u.id)
			info.DisplayName = "This is " + u.id
			info.AccountID = u.account
			if u.caps != "" {
				Expect(info.Caps.AddString(u.caps)).To(Succeed())
			}
			store.AddUser(info)
		}
		store.AddAccount(meta.AccountInfo{ID: acct, Name: "acme"})
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, BucketAdmin: store, Metadata: store, Usage: store, Metrics: op.NopMetrics{},
		}
		createBucket(ctx, "test", "admin")
	})
	runAs := func(ctx context.Context, o op.Op, caps meta.Caps) error {
		u := meta.NewUserInfo()
		u.UserID = uid("admin")
		u.Caps = caps
		return op.Run(ctx, o, &op.Request{Env: env, Identity: op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps}})
	}
	run := func(ctx context.Context, o op.Op) error {
		return runAs(ctx, o, meta.Caps{"buckets": meta.CapAll})
	}
	runBody := func(ctx context.Context, o op.Op, body string) error {
		caps := meta.Caps{"buckets": meta.CapAll}
		u := meta.NewUserInfo()
		u.UserID = uid("admin")
		r := &op.Request{
			Env: env, Identity: op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps},
			Body: strings.NewReader(body), ContentLength: int64(len(body)),
		}
		return op.Run(ctx, o, r)
	}

	Describe("info", func() {
		It("returns a single bucket's stats, NoSuchBucket when absent", func(ctx SpecContext) {
			putObject(ctx, "test", "k1")
			o := op.NewBucketInfo()
			o.Bucket = "test"
			o.Stats = true
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Single).NotTo(BeNil())
			Expect(o.Single.Rec.Info.Bucket.Name).To(Equal("test"))
			Expect(o.Single.HasIndex).To(BeTrue())
			Expect(o.Single.Index.Categories).To(HaveKeyWithValue("rgw.main", op.CategoryStats{Size: 5, SizeRounded: 4096, SizeUtilized: 5, NumObjects: 1}))
			Expect(o.Single.Tags).To(BeNil())
			o = op.NewBucketInfo()
			o.Bucket = "foo"
			Expect(run(ctx, o)).To(MatchError(op.ErrNoSuchBucket))
		})
		It("reads no index stats for another zonegroup's bucket", func(ctx SpecContext) {
			rec, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			rec.Info.Zonegroup = "elsewhere"
			Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
			fake := new(opfakes.FakeBucketAdminStore)
			env.BucketAdmin = fake
			o := op.NewBucketInfo()
			o.Bucket = "test"
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Single.HasIndex).To(BeFalse())
			Expect(fake.IndexStatsCallCount()).To(BeZero())
		})
		It("decodes the bucket's tags", func(ctx SpecContext) {
			rec, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			var set tags.Set
			Expect(set.Add("k", "v", tags.MaxBucketTags)).To(Succeed())
			e := denc.NewEncoder()
			set.Encode(e, denc.Squid)
			Expect(store.PutBucketAttrs(ctx, rec, map[string][]byte{tags.Attr: e.Bytes()}, nil)).To(Succeed())
			o := op.NewBucketInfo()
			o.Bucket = "test"
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Single.Tags).To(Equal(&set))
		})
		It("finds a tenanted bucket named tenant/name", func(ctx SpecContext) {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Tenant: "t1", Name: "tb", Owner: meta.UserOwner(uid("admin")), Zonegroup: store.ZoneGroup().ID})
			Expect(err).NotTo(HaveOccurred())
			o := op.NewBucketInfo()
			o.Bucket = "t1/tb"
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Single.Rec.Info.Bucket.Tenant).To(Equal("t1"))
		})
		It("lists every bucket, and a user's with or without stats, paged", func(ctx SpecContext) {
			createBucket(ctx, "b2", "test-user1")
			o := op.NewBucketInfo()
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Names).To(ConsistOf("test", "b2"))
			Expect(o.Single).To(BeNil())
			Expect(o.Entries).To(BeNil())

			o = op.NewBucketInfo()
			o.UID = uid("test-user1")
			o.UIDGiven = true
			o.Stats = true
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Entries).To(HaveLen(1))
			Expect(o.Entries[0].Rec.Info.Owner).To(Equal(meta.UserOwner(uid("test-user1"))))
			Expect(o.Paged).To(BeFalse())

			createBucket(ctx, "b3", "test-user1")
			o = op.NewBucketInfo()
			o.UID = uid("test-user1")
			o.UIDGiven = true
			o.MaxEntries = 1
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Paged).To(BeTrue())
			Expect(o.Names).To(Equal([]string{"b2"}))
			Expect(o.Count).To(Equal(uint64(1)))
			Expect(o.Truncated).To(BeTrue())
			Expect(o.NextMarker).To(Equal("b2"))

			o.Marker = o.NextMarker
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Names).To(Equal([]string{"b3"}))
			Expect(o.Truncated).To(BeFalse())

			o = op.NewBucketInfo()
			o.UID = uid("nosuch")
			o.UIDGiven = true
			Expect(run(ctx, o)).To(MatchError(op.ErrNoSuchKey))
		})
		It("lists every bucket with stats, the tenanted ones split at the slash", func(ctx SpecContext) {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Tenant: "t1", Name: "tb", Owner: meta.UserOwner(uid("admin")), Zonegroup: store.ZoneGroup().ID})
			Expect(err).NotTo(HaveOccurred())
			o := op.NewBucketInfo()
			o.Stats = true
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Entries).To(HaveLen(2))
			names := []string{o.Entries[0].Rec.Info.Bucket.Tenant + "/" + o.Entries[0].Rec.Info.Bucket.Name, o.Entries[1].Rec.Info.Bucket.Tenant + "/" + o.Entries[1].Rec.Info.Bucket.Name}
			Expect(names).To(ConsistOf("/test", "t1/tb"))
		})
		It("lists an account user's buckets as the account's", func(ctx SpecContext) {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "ab", Owner: meta.AccountOwner(acct), Zonegroup: store.ZoneGroup().ID})
			Expect(err).NotTo(HaveOccurred())
			o := op.NewBucketInfo()
			o.UID = uid("acctuser")
			o.UIDGiven = true
			Expect(run(ctx, o)).To(Succeed())
			Expect(o.Names).To(Equal([]string{"ab"}))
		})
		It("answers a failed listing with its error", func(ctx SpecContext) {
			md := new(opfakes.FakeMetadataStore)
			md.ListReturns(nil, "", false, op.ErrNotImplemented)
			env.Metadata = md
			Expect(run(ctx, op.NewBucketInfo())).To(MatchError(op.ErrNotImplemented))
		})
	})

	Describe("link and unlink", func() {
		It("links a bucket to another user, checks the id, renames, and unlinks", func(ctx SpecContext) {
			rec, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			l := op.NewLinkBucket()
			l.UID = uid("test-user2")
			l.Bucket = "test"
			l.BucketID = rec.Info.Bucket.ID
			Expect(run(ctx, l)).To(Succeed())
			got, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(meta.UserOwner(l.UID)))
			Expect(decodeACL(got.Attrs[meta.AttrACL]).Owner).To(Equal(acl.Owner{ID: "test-user2", DisplayName: "This is test-user2"}))

			l.BucketID = "wrong"
			Expect(run(ctx, l)).To(MatchError(op.ErrNoSuchKey), "a bucket id loads that instance, rgw_bucket.cc:196-197 and rgw_sal_rados.cc:616-629")

			l = op.NewLinkBucket()
			l.UID = uid("test-user2")
			l.Bucket = "test"
			l.NewBucketName = "renamed"
			Expect(run(ctx, l)).To(Succeed())
			_, err = store.GetBucket(ctx, "", "test")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			got, err = store.GetBucket(ctx, "", "renamed")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(rec.Info.Bucket.ID))

			u := op.NewUnlinkBucket()
			u.UID = uid("test-user2")
			u.Bucket = "renamed"
			Expect(run(ctx, u)).To(Succeed())
			got, err = store.GetBucket(ctx, "", "renamed")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.EntryPoint.Linked).To(BeFalse())

			Expect(run(ctx, &op.LinkBucket{Bucket: "renamed"})).To(MatchError(op.ErrInvalidArgument), "needs uid or account-id")
			Expect(run(ctx, &op.UnlinkBucket{Bucket: "renamed"})).To(MatchError(op.ErrInvalidArgument), "needs uid or account-id")
			l = op.NewLinkBucket()
			l.UID = uid("acctuser")
			l.Bucket = "renamed"
			Expect(run(ctx, l)).To(MatchError(op.ErrInvalidArgument), "account users cannot own buckets")
			l = op.NewLinkBucket()
			l.UID = uid("test-user2")
			l.Bucket = "nosuch"
			Expect(run(ctx, l)).To(MatchError(op.ErrNoSuchKey))
			l = op.NewLinkBucket()
			l.UID = uid("nosuch")
			l.Bucket = "renamed"
			Expect(run(ctx, l)).To(MatchError(op.ErrNoSuchKey), "load_user's ENOENT")
		})
		It("refuses a rename onto a name another bucket holds, changing nothing", func(ctx SpecContext) {
			other := createBucket(ctx, "taken", "test-user1")
			l := op.NewLinkBucket()
			l.UID = uid("test-user2")
			l.Bucket = "test"
			l.NewBucketName = "taken"
			Expect(run(ctx, l)).To(MatchError(op.ErrBucketAlreadyExists))
			got, err := store.GetBucket(ctx, "", "taken")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(other.Info.Bucket.ID))
			got, err = store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(meta.UserOwner(uid("admin"))), "nothing written")
		})
		It("moves a tenanted bucket into the user's tenant, as the uid's tenant names the key", func(ctx SpecContext) {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{
				Tenant: "t1", Name: "tb", Owner: meta.UserOwner(uid("admin")), Zonegroup: store.ZoneGroup().ID,
				Attrs: map[string][]byte{meta.AttrACL: encodeACL(meta.UserOwner(uid("admin")), "A")},
			})
			Expect(err).NotTo(HaveOccurred())
			l := op.NewLinkBucket()
			l.UID = uid("test-user1")
			l.Bucket = "t1/tb"
			Expect(run(ctx, l)).To(Succeed())
			_, err = store.GetBucket(ctx, "t1", "tb")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			got, err := store.GetBucket(ctx, "", "tb")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(meta.UserOwner(uid("test-user1"))))
		})
		It("links to an account with the account's name as display name", func(ctx SpecContext) {
			l := op.NewLinkBucket()
			l.AccountID = acct
			l.Bucket = "test"
			Expect(run(ctx, l)).To(Succeed())
			got, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(meta.AccountOwner(acct)))
			Expect(decodeACL(got.Attrs[meta.AttrACL]).Owner.DisplayName).To(Equal("acme"))
			l.AccountID = "RGW00000000000000009"
			Expect(run(ctx, l)).To(MatchError(op.ErrNoSuchKey), "load_account_by_id's ENOENT")
		})
	})

	Describe("link by bucket id", func() {
		byID := func(ctx context.Context) error {
			GinkgoHelper()
			rec, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			l := op.NewLinkBucket()
			l.UID, l.Bucket, l.BucketID = uid("test-user2"), "test", rec.Info.Bucket.ID
			return run(ctx, l)
		}
		owner := func(ctx context.Context) meta.Owner {
			GinkgoHelper()
			got, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			return got.Info.Owner
		}
		It("links when no other name loads the bucket's id", func(ctx SpecContext) {
			createBucket(ctx, "other", "test-user1")
			Expect(byID(ctx)).To(Succeed())
			Expect(owner(ctx)).To(Equal(meta.UserOwner(uid("test-user2"))))
		})
		It("refuses when another name loads the bucket's id", func(ctx SpecContext) {
			createBucket(ctx, "other", "test-user1")
			env.Buckets = &sameIDUnder{Store: store, name: "other", as: "test"}
			Expect(byID(ctx)).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(owner(ctx)).To(Equal(meta.UserOwner(uid("admin"))))
		})
		DescribeTable("refuses rather than link blind",
			func(ctx context.Context, listErr, want error) {
				md := new(opfakes.FakeMetadataStore)
				md.ListReturns(nil, "", false, listErr)
				env.Metadata = md
				Expect(byID(ctx)).To(MatchError(want))
				Expect(owner(ctx)).To(Equal(meta.UserOwner(uid("admin"))))
			},
			Entry("a store that cannot list the bucket section", op.ErrNotImplemented, op.ErrNotImplemented),
			Entry("a listing that fails", errors.Join(op.ErrInternalError, errors.New("rados")), op.ErrInternalError),
		)
		It("refuses when a listed name loads nothing", func(ctx SpecContext) {
			md := new(opfakes.FakeMetadataStore)
			md.ListReturns([]string{"ghost", "test"}, "test", false, nil)
			env.Metadata = md
			Expect(byID(ctx)).To(MatchError(op.ErrConcurrentModification))
		})
		DescribeTable("refuses a listing it cannot finish",
			func(ctx context.Context, stub func(*opfakes.FakeMetadataStore), want error) {
				md := new(opfakes.FakeMetadataStore)
				stub(md)
				env.Metadata = md
				Expect(byID(ctx)).To(MatchError(want))
				Expect(owner(ctx)).To(Equal(meta.UserOwner(uid("admin"))))
			},
			Entry("a section the store reports missing", func(md *opfakes.FakeMetadataStore) {
				md.ListReturns(nil, "", false, op.ErrNotFound)
			}, op.ErrNotFound),
			Entry("a page that makes no progress", func(md *opfakes.FakeMetadataStore) {
				md.ListReturns([]string{"test"}, "", true, nil)
			}, op.ErrInternalError),
		)
		It("refuses when a listed bucket cannot be read", func(ctx SpecContext) {
			env.Buckets = &failingRead{Store: store, name: "other", err: errors.Join(op.ErrInternalError, errors.New("rados"))}
			md := new(opfakes.FakeMetadataStore)
			md.ListReturns([]string{"other", "test"}, "test", false, nil)
			env.Metadata = md
			Expect(byID(ctx)).To(MatchError(op.ErrInternalError))
			Expect(owner(ctx)).To(Equal(meta.UserOwner(uid("admin"))))
		})
		It("refuses when the bucket's own name under another tenant loads its id", func(ctx SpecContext) {
			env.Buckets = &sameIDUnder{Store: store, tenant: "t1", name: "test", as: "test"}
			md := new(opfakes.FakeMetadataStore)
			md.ListReturns([]string{"t1/test", "test"}, "test", false, nil)
			env.Metadata = md
			Expect(byID(ctx)).To(MatchError(op.ErrBucketAlreadyExists))
		})
		It("reads every page of the listing", func(ctx SpecContext) {
			createBucket(ctx, "other", "test-user1")
			env.Buckets = &sameIDUnder{Store: store, name: "other", as: "test"}
			md := new(opfakes.FakeMetadataStore)
			md.ListReturnsOnCall(0, []string{"test"}, "test", true, nil)
			md.ListReturnsOnCall(1, []string{"other"}, "other", false, nil)
			env.Metadata = md
			Expect(byID(ctx)).To(MatchError(op.ErrBucketAlreadyExists))
			Expect(md.ListCallCount()).To(Equal(2))
		})
	})

	Describe("admin removal of a name that loads no bucket", func() {
		It("frees a dangling entry point through the store", func(ctx SpecContext) {
			admin := new(opfakes.FakeBucketAdminStore)
			env.BucketAdmin = admin
			d := op.NewRemoveBucketAdmin()
			d.Bucket = "ghost"
			Expect(run(ctx, d)).To(Succeed())
			Expect(admin.RemoveDanglingEntryPointCallCount()).To(Equal(1))
			_, tenant, name := admin.RemoveDanglingEntryPointArgsForCall(0)
			Expect([]string{tenant, name}).To(Equal([]string{"", "ghost"}))
			admin.RemoveDanglingEntryPointReturns(op.ErrRenamePending)
			Expect(run(ctx, d)).To(MatchError(op.ErrConcurrentModification))
		})
		It("answers NoSuchBucket for a name without an entry point", func(ctx SpecContext) {
			d := op.NewRemoveBucketAdmin()
			d.Bucket = "ghost"
			Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchBucket))
		})
	})

	It("answers an S3 DeleteBucket that lost its entry point after claiming the bucket with 409", func(ctx SpecContext) {
		rec, err := store.GetBucket(ctx, "", "test")
		Expect(err).NotTo(HaveOccurred())
		buckets := new(opfakes.FakeBucketStore)
		buckets.DeleteBucketReturns(op.ErrRemovalRaced)
		env.Buckets = buckets
		r := &op.Request{Env: env, BucketRec: rec}
		Expect((&op.DeleteBucket{}).Execute(ctx, r)).To(MatchError(op.ErrConcurrentModification), "the bucket stays, so not success")
	})
	It("answers an S3 DeleteBucket the store refuses for an unfinished rename with 409", func(ctx SpecContext) {
		rec, err := store.GetBucket(ctx, "", "test")
		Expect(err).NotTo(HaveOccurred())
		buckets := new(opfakes.FakeBucketStore)
		buckets.DeleteBucketReturns(op.ErrRenamePending)
		env.Buckets = buckets
		r := &op.Request{Env: env, BucketRec: rec}
		Expect((&op.DeleteBucket{}).Execute(ctx, r)).To(MatchError(op.ErrConcurrentModification), "not the lost race taken as success")
	})

	Describe("link to a tenanted account", func() {
		It("keeps the bucket in the account's tenant", func(ctx SpecContext) {
			const tacct = "RGW00000000000000002"
			store.AddAccount(meta.AccountInfo{ID: tacct, Tenant: "t1", Name: "tacme"})
			o := meta.UserOwner(uid("admin"))
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{
				Tenant: "t1", Name: "ab", Owner: o, Zonegroup: store.ZoneGroup().ID,
				Attrs: map[string][]byte{meta.AttrACL: encodeACL(o, "A")},
			})
			Expect(err).NotTo(HaveOccurred())
			l := op.NewLinkBucket()
			l.AccountID = tacct
			l.Bucket = "t1/ab"
			Expect(run(ctx, l)).To(Succeed())
			got, err := store.GetBucket(ctx, "t1", "ab")
			Expect(err).NotTo(HaveOccurred(), "radosgw would move it to the empty tenant")
			Expect(got.Info.Owner).To(Equal(meta.AccountOwner(tacct)))
			_, err = store.GetBucket(ctx, "", "ab")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
	})

	Describe("an unfinished rename", func() {
		mark := func(ctx context.Context, name string, i op.RenameIntent) {
			GinkgoHelper()
			rec := createBucket(ctx, name, "admin")
			Expect(store.PutBucketAttrs(ctx, rec, map[string][]byte{op.RenameIntentAttr: i.Encode()}, nil)).To(Succeed())
		}
		It("is completed by a retry naming the old name once only the new one loads", func(ctx SpecContext) {
			mark(ctx, "dst", op.RenameIntent{SrcName: "src", DstName: "dst", Owner: "test-user2"})
			l := op.NewLinkBucket()
			l.UID, l.Bucket, l.NewBucketName = uid("test-user2"), "src", "dst"
			Expect(run(ctx, l)).To(Succeed())
			got, err := store.GetBucket(ctx, "", "dst")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(meta.UserOwner(uid("test-user2"))))
		})
		DescribeTable("refuses every removal of the bucket while the rename is live, by either name",
			func(ctx context.Context, name string, remove func(*op.RemoveBucketAdmin)) {
				mark(ctx, name, op.RenameIntent{SrcName: "src", DstName: "dst", Owner: "test-user2"})
				putObject(ctx, name, "k1")
				d := op.NewRemoveBucketAdmin()
				d.Bucket = name
				remove(d)
				Expect(run(ctx, d)).To(MatchError(op.ErrConcurrentModification))
				rec, err := store.GetBucket(ctx, "", name)
				Expect(err).NotTo(HaveOccurred())
				res, err := store.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Entries).To(HaveLen(1), "nothing purged")
			},
			Entry("the old name, purging", "src", func(d *op.RemoveBucketAdmin) { d.PurgeObjects = true }),
			Entry("the old name, bypassing the GC", "src", func(d *op.RemoveBucketAdmin) { d.BypassGC = true }),
			Entry("the new name, purging", "dst", func(d *op.RemoveBucketAdmin) { d.PurgeObjects = true }),
			Entry("the new name, bypassing the GC", "dst", func(d *op.RemoveBucketAdmin) { d.BypassGC = true }),
		)
		It("refuses a removal without purge while the rename is live", func(ctx SpecContext) {
			mark(ctx, "src", op.RenameIntent{SrcName: "src", DstName: "dst", Owner: "test-user2"})
			d := op.NewRemoveBucketAdmin()
			d.Bucket = "src"
			Expect(run(ctx, d)).To(MatchError(op.ErrConcurrentModification))
			_, err := store.GetBucket(ctx, "", "src")
			Expect(err).NotTo(HaveOccurred())
		})
		It("refuses an S3 DeleteBucket while the rename is live, and allows it once the record is void", func(ctx SpecContext) {
			mark(ctx, "src", op.RenameIntent{SrcName: "src", DstName: "dst", Owner: "test-user2"})
			rec, err := store.GetBucket(ctx, "", "src")
			Expect(err).NotTo(HaveOccurred())
			u := meta.NewUserInfo()
			u.UserID = uid("admin")
			r := &op.Request{Env: env, BucketRec: rec, Identity: op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll}}
			Expect((&op.DeleteBucket{}).Execute(ctx, r)).To(MatchError(op.ErrConcurrentModification), "not taken as the store's lost race")
			_, err = store.GetBucket(ctx, "", "src")
			Expect(err).NotTo(HaveOccurred())
			createBucket(ctx, "dst", "test-user1")
			Expect((&op.DeleteBucket{}).Execute(ctx, r)).To(Succeed(), "another bucket holds the new name: the record is void")
			_, err = store.GetBucket(ctx, "", "src")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
		It("lets a removal go on once the record is void", func(ctx SpecContext) {
			mark(ctx, "src", op.RenameIntent{SrcName: "src", DstName: "dst", Owner: "test-user2"})
			createBucket(ctx, "dst", "test-user1")
			d := op.NewRemoveBucketAdmin()
			d.Bucket, d.PurgeObjects = "src", true
			Expect(run(ctx, d)).To(Succeed())
		})
		DescribeTable("is not taken up by a request it does not record",
			func(ctx context.Context, i op.RenameIntent, bucket, newName string) {
				mark(ctx, "dst", i)
				l := op.NewLinkBucket()
				l.UID, l.Bucket, l.NewBucketName = uid("test-user2"), bucket, newName
				Expect(run(ctx, l)).To(MatchError(op.ErrNoSuchKey))
				got, err := store.GetBucket(ctx, "", "dst")
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Info.Owner).To(Equal(meta.UserOwner(uid("admin"))))
			},
			Entry("another old name", op.RenameIntent{SrcName: "other", DstName: "dst"}, "src", "dst"),
			Entry("another new name", op.RenameIntent{SrcName: "src", DstName: "elsewhere"}, "src", "dst"),
			Entry("no new name", op.RenameIntent{SrcName: "src", DstName: "dst"}, "src", ""),
		)
	})

	Describe("remove", func() {
		It("removes a bucket, purging objects only when asked", func(ctx SpecContext) {
			putObject(ctx, "test", "k1")
			d := op.NewRemoveBucketAdmin()
			d.Bucket = "test"
			Expect(run(ctx, d)).To(MatchError(op.ErrBucketNotEmpty))
			d.PurgeObjects = true
			Expect(run(ctx, d)).To(Succeed())
			Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchBucket))
		})
		It("purges with bypass-gc whatever purge-objects says", func(ctx SpecContext) {
			putObject(ctx, "test", "k1")
			d := op.NewRemoveBucketAdmin()
			d.Bucket = "test"
			d.BypassGC = true
			Expect(run(ctx, d)).To(Succeed(), "remove_bucket takes remove_bypass_gc without consulting delete_children, rgw_bucket.cc:1303-1304")
			_, err := store.GetBucket(ctx, "", "test")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchBucket))
		})
		It("leaves the bucket when the bypass-gc data pass fails", func(ctx SpecContext) {
			fake := new(opfakes.FakeBucketAdminStore)
			fake.PurgeBypassGCReturns(op.ErrNotImplemented)
			env.BucketAdmin = fake
			d := op.NewRemoveBucketAdmin()
			d.Bucket = "test"
			d.BypassGC = true
			Expect(run(ctx, d)).To(MatchError(op.ErrNotImplemented))
			_, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred(), "remove_bypass_gc returns before remove, rgw_sal_rados.cc:483-590")
			fake.PurgeBypassGCReturns(op.ErrNoSuchKey)
			Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchBucket), "the op answers every -ENOENT as NoSuchBucket, rgw_rest_bucket.cc:245-247")
		})
		It("refuses another zonegroup's bucket with PermanentRedirect", func(ctx SpecContext) {
			rec, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			rec.Info.Zonegroup = "elsewhere"
			Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
			d := op.NewRemoveBucketAdmin()
			d.Bucket = "test"
			d.PurgeObjects = true
			Expect(run(ctx, d)).To(MatchError(op.ErrPermanentRedirect))
			_, err = store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
		})
		It("completes a purge that failed partway when retried", func(ctx SpecContext) {
			putObject(ctx, "test", "k1")
			putObject(ctx, "test", "k2")
			flaky := &failingDeletes{Store: store, failAfter: 1}
			env.Objects = flaky
			d := op.NewRemoveBucketAdmin()
			d.Bucket = "test"
			d.PurgeObjects = true
			Expect(run(ctx, d)).To(MatchError(errFlaky))
			env.Objects = store
			Expect(run(ctx, d)).To(Succeed())
			_, err := store.GetBucket(ctx, "", "test")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
	})

	Describe("quota", func() {
		setQuota := func(params op.QuotaParams) *op.SetBucketQuota {
			q := op.NewSetBucketQuota()
			q.UID, q.UIDGiven = uid("admin"), true
			q.Bucket, q.BucketGiven = "test", true
			q.Params = params
			return q
		}
		storedQuota := func(ctx context.Context) meta.Quota {
			GinkgoHelper()
			got, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			return got.Info.Quota
		}
		It("sets the bucket quota from arguments over the current one", func(ctx SpecContext) {
			Expect(run(ctx, setQuota(op.QuotaParams{MaxSizeKB: new(int64(1000000)), Enabled: new(true)}))).To(Succeed())
			Expect(storedQuota(ctx)).To(Equal(meta.Quota{MaxSize: 1000000 * 1024, MaxObjects: -1, Enabled: true}))
			Expect(run(ctx, setQuota(op.QuotaParams{MaxObjects: new(int64(7))}))).To(Succeed())
			Expect(storedQuota(ctx)).To(Equal(meta.Quota{MaxSize: 1000000 * 1024, MaxObjects: 7, Enabled: true}), "absent arguments keep the current values")
		})
		It("sets the bucket quota from a JSON body", func(ctx SpecContext) {
			Expect(runBody(ctx, setQuota(op.QuotaParams{}), `{"enabled":true,"max_size":4096,"max_objects":3}`)).To(Succeed())
			Expect(storedQuota(ctx)).To(Equal(meta.Quota{MaxSize: 4096, MaxObjects: 3, Enabled: true}))
		})
		DescribeTable("refuses a size that would lift the limit, storing nothing",
			func(ctx context.Context, params op.QuotaParams, body string) {
				before := storedQuota(ctx)
				Expect(runBody(ctx, setQuota(params), body)).To(MatchError(op.ErrInvalidArgument))
				Expect(storedQuota(ctx)).To(Equal(before))
			},
			Entry("max-size-kb=-1", op.QuotaParams{MaxSizeKB: new(int64(-1))}, ""),
			Entry("max-size-kb overflowing kb*1024", op.QuotaParams{MaxSizeKB: new(int64(1) << 54)}, ""),
			Entry("max-size below -1", op.QuotaParams{MaxSize: new(int64(-2))}, ""),
			Entry("a body with a negative size", op.QuotaParams{}, `{"max_size":-5}`),
			Entry("a body that is not JSON", op.QuotaParams{}, `nope`),
		)
		It("requires the uid and bucket arguments to exist, empty or not", func(ctx SpecContext) {
			q := setQuota(op.QuotaParams{})
			q.UIDGiven = false
			Expect(run(ctx, q)).To(MatchError(op.ErrInvalidArgument))
			q = setQuota(op.QuotaParams{MaxObjects: new(int64(1))})
			q.UID = meta.UserID{}
			Expect(run(ctx, q)).To(Succeed(), "an empty uid exists")
			q = setQuota(op.QuotaParams{})
			q.BucketGiven = false
			Expect(run(ctx, q)).To(MatchError(op.ErrInvalidArgument))
			q = setQuota(op.QuotaParams{})
			q.Bucket = "foo"
			Expect(run(ctx, q)).To(MatchError(op.ErrNoSuchKey))
			q = setQuota(op.QuotaParams{})
			q.UID = uid("nosuch")
			Expect(run(ctx, q)).To(MatchError(op.ErrNoSuchKey))
		})
	})

	Describe("policy and object removal", func() {
		It("reads the bucket's and an object's policy", func(ctx SpecContext) {
			p := op.NewGetBucketPolicyAdmin()
			p.Bucket = "test"
			Expect(run(ctx, p)).To(Succeed())
			Expect(p.Result.Owner.ID).To(Equal("admin"))
			p.Bucket = "foo"
			Expect(run(ctx, p)).To(MatchError(op.ErrNoSuchKey))
			p = op.NewGetBucketPolicyAdmin()
			p.Bucket = "test"
			p.Object = "missing"
			Expect(run(ctx, p)).To(MatchError(op.ErrNoSuchKey))

			rec, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "withacl"}, strings.NewReader("x"), op.PutParams{
				Size: 1, Attrs: map[string][]byte{meta.AttrACL: encodeACL(meta.UserOwner(uid("test-user1")), "U1")},
			})
			Expect(err).NotTo(HaveOccurred())
			p.Object = "withacl"
			Expect(run(ctx, p)).To(Succeed())
			Expect(p.Result.Owner).To(Equal(acl.Owner{ID: "test-user1", DisplayName: "U1"}))

			_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "noacl"}, strings.NewReader("x"), op.PutParams{Size: 1})
			Expect(err).NotTo(HaveOccurred())
			p.Object = "noacl"
			Expect(run(ctx, p)).To(MatchError(op.ErrUnknown), "get_attr's ENODATA, which no S3 error names")
		})
		It("answers UnknownError for a bucket ACL that does not decode", func(ctx SpecContext) {
			rec, err := store.GetBucket(ctx, "", "test")
			Expect(err).NotTo(HaveOccurred())
			Expect(store.PutBucketAttrs(ctx, rec, map[string][]byte{meta.AttrACL: []byte("junk")}, nil)).To(Succeed())
			p := op.NewGetBucketPolicyAdmin()
			p.Bucket = "test"
			Expect(run(ctx, p)).To(MatchError(op.ErrUnknown), "decode_bl's EIO")
		})
		It("removes an object through the admin route", func(ctx SpecContext) {
			putObject(ctx, "test", "k1")
			r := op.NewRemoveObjectAdmin()
			r.Bucket = "test"
			r.Object = "k1"
			Expect(run(ctx, r)).To(Succeed())
			Expect(run(ctx, r)).To(MatchError(op.ErrNoSuchKey))
			r.Bucket = "foo"
			Expect(run(ctx, r)).To(MatchError(op.ErrNoSuchKey))
		})
		DescribeTable("answers NoSuchKey for a name no object can carry, without asking the store",
			func(ctx context.Context, name string) {
				objs := new(opfakes.FakeObjectStore)
				env.Objects = objs
				r := op.NewRemoveObjectAdmin()
				r.Bucket = "test"
				r.Object = name
				Expect(run(ctx, r)).To(MatchError(op.ErrNoSuchKey))
				p := op.NewGetBucketPolicyAdmin()
				p.Bucket = "test"
				p.Object = name
				if name != "" {
					Expect(run(ctx, p)).To(MatchError(op.ErrNoSuchKey))
				}
				Expect(objs.Invocations()).To(BeEmpty())
			},
			Entry("empty", ""),
			Entry("over 1024 bytes", strings.Repeat("a", 1025)),
			Entry("not UTF-8", "a\xffb"),
		)
	})

	Describe("security", func() {
		It("checks the caps before any lookup, so a refused caller learns nothing", func(ctx SpecContext) {
			buckets := new(opfakes.FakeBucketStore)
			users := new(opfakes.FakeUserStore)
			admin := new(opfakes.FakeBucketAdminStore)
			objs := new(opfakes.FakeObjectStore)
			md := new(opfakes.FakeMetadataStore)
			accounts := new(opfakes.FakeAccountStore)
			env.Buckets, env.Users, env.BucketAdmin, env.Objects, env.Metadata, env.Accounts = buckets, users, admin, objs, md, accounts
			info := op.NewBucketInfo()
			info.Bucket = "test"
			list := op.NewBucketInfo()
			link := op.NewLinkBucket()
			link.UID, link.Bucket = uid("test-user1"), "test"
			acctLink := op.NewLinkBucket()
			acctLink.AccountID, acctLink.Bucket = acct, "test"
			unlink := op.NewUnlinkBucket()
			unlink.UID, unlink.Bucket = uid("admin"), "test"
			rm := op.NewRemoveBucketAdmin()
			rm.Bucket, rm.PurgeObjects = "test", true
			q := op.NewSetBucketQuota()
			q.UID, q.UIDGiven, q.Bucket, q.BucketGiven = uid("admin"), true, "test", true
			q.Params.MaxObjects = new(int64(1))
			pol := op.NewGetBucketPolicyAdmin()
			pol.Bucket, pol.Object = "test", "k"
			obj := op.NewRemoveObjectAdmin()
			obj.Bucket, obj.Object = "test", "k"
			for _, o := range []op.Op{info, list, link, acctLink, unlink, rm, q, pol, obj} {
				Expect(runAs(ctx, o, meta.Caps{"users": meta.CapAll, "metadata": meta.CapAll})).To(MatchError(op.ErrAccessDenied), o.Name())
				need := meta.CapWrite
				if o.Name() == "get_bucket_info" || o.Name() == "get_policy" {
					need = meta.CapRead
				}
				Expect(runAs(ctx, o, meta.Caps{"buckets": meta.CapAll &^ need})).To(MatchError(op.ErrAccessDenied), o.Name()+" with the other buckets bit")
			}
			Expect(buckets.Invocations()).To(BeEmpty())
			Expect(users.Invocations()).To(BeEmpty())
			Expect(admin.Invocations()).To(BeEmpty())
			Expect(objs.Invocations()).To(BeEmpty())
			Expect(md.Invocations()).To(BeEmpty())
			Expect(accounts.Invocations()).To(BeEmpty())
		})
		It("drops a user store's error text before it can reach a log", func(ctx SpecContext) {
			users := new(opfakes.FakeUserStore)
			users.GetUserReturns(nil, errors.Join(op.ErrInternalError, errors.New("users.keys AKIASECRET")))
			env.Users = users
			l := op.NewLinkBucket()
			l.UID, l.Bucket = uid("test-user1"), "test"
			err := run(ctx, l)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(err.Error()).NotTo(ContainSubstring("AKIASECRET"))
		})
	})
})

// errFlaky is failingDeletes' injected failure.
var errFlaky = errors.New("flaky delete")

// failingDeletes is the memstore's object store whose DeleteObject fails
// once failAfter deletions have succeeded.
type failingDeletes struct {
	*memstore.Store
	failAfter, done int
}

func (f *failingDeletes) DeleteObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) error {
	if f.done >= f.failAfter {
		return errFlaky
	}
	f.done++
	return f.Store.DeleteObject(ctx, rec, key, p)
}

// sameIDUnder is the memstore's bucket store with the bucket of name loaded
// as the bucket as holds, as an entry point naming another name's instance
// would load it.
type sameIDUnder struct {
	*memstore.Store
	tenant, name, as string
}

func (s *sameIDUnder) GetBucket(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
	if tenant == s.tenant && name == s.name {
		return s.Store.GetBucket(ctx, "", s.as)
	}
	return s.Store.GetBucket(ctx, tenant, name)
}

// failingRead is the memstore's bucket store whose GetBucket of name fails
// with err.
type failingRead struct {
	*memstore.Store
	name string
	err  error
}

func (s *failingRead) GetBucket(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
	if tenant == "" && name == s.name {
		return nil, s.err
	}
	return s.Store.GetBucket(ctx, tenant, name)
}
