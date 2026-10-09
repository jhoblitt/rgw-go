package memstore_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("memstore admin stores", func() {
	const (
		acct1 = "RGW00000000000000001"
		acct2 = "RGW00000000000000002"
	)
	var store *memstore.Store
	BeforeEach(func() { store, _ = newStore() })

	Describe("accounts", func() {
		It("indexes accounts by name and email, case-insensitively for email", func(ctx SpecContext) {
			rec := store.AddAccount(meta.AccountInfo{ID: acct1, Tenant: "t", Name: "acme", Email: "Ops@Acme.example"})
			byName, err := store.GetAccountByName(ctx, "t", "acme")
			Expect(err).NotTo(HaveOccurred())
			Expect(byName.Info.ID).To(Equal(rec.Info.ID))
			byEmail, err := store.GetAccountByEmail(ctx, "ops@acme.EXAMPLE")
			Expect(err).NotTo(HaveOccurred())
			Expect(byEmail.Info.ID).To(Equal(rec.Info.ID))
			_, err = store.GetAccountByName(ctx, "other", "acme")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "another tenant")
			_, err = store.GetAccountByName(ctx, "t", "ACME")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "names match case-sensitively")
			name, err := store.AccountName(ctx, rec.Info.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(name).To(Equal("acme"))
		})
		It("refuses a second account with the same name or email", func(ctx SpecContext) {
			store.AddAccount(meta.AccountInfo{ID: acct1, Name: "acme", Email: "a@b"})
			dup := &op.AccountRecord{Info: meta.AccountInfo{ID: acct2, Name: "acme"}}
			Expect(store.PutAccount(ctx, dup, nil, op.PutAccountOptions{Exclusive: true})).To(MatchError(op.ErrAccountAlreadyExists), "name")
			dup.Info.Name = "other"
			dup.Info.Email = "A@B"
			Expect(store.PutAccount(ctx, dup, nil, op.PutAccountOptions{Exclusive: true})).To(MatchError(op.ErrAccountAlreadyExists), "email")
			_, err := store.GetAccount(ctx, acct2)
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "nothing was written")
		})
		It("refuses an email a user holds, the two sharing one index", func(ctx SpecContext) {
			store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, Email: "alice@example.com"})
			rec := &op.AccountRecord{Info: meta.AccountInfo{ID: acct1, Name: "acme", Email: "Alice@example.com"}}
			Expect(store.PutAccount(ctx, rec, nil, op.PutAccountOptions{Exclusive: true})).To(MatchError(op.ErrAccountAlreadyExists))
			_, err := store.GetAccountByEmail(ctx, "alice@example.com")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "a user's email names no account")
		})
		It("refuses a user an account's email, which radosgw lets the user take", func(ctx SpecContext) {
			store.AddAccount(meta.AccountInfo{ID: acct1, Name: "acme", Email: "ops@acme.example"})
			user := &op.UserRecord{Info: meta.UserInfo{UserID: meta.UserID{ID: "alice"}, Email: "Ops@Acme.example"}}
			Expect(store.PutUser(ctx, user, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrEmailExists))
			_, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
			Expect(err).To(MatchError(op.ErrNoSuchUser), "nothing written")
			got, err := store.GetAccountByEmail(ctx, "ops@acme.example")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.ID).To(Equal(acct1), "the account keeps its email")
		})
		It("refuses an exclusive put over an existing id", func(ctx SpecContext) {
			store.AddAccount(meta.AccountInfo{ID: acct1, Name: "acme"})
			again := &op.AccountRecord{Info: meta.AccountInfo{ID: acct1, Name: "acme2"}}
			Expect(store.PutAccount(ctx, again, nil, op.PutAccountOptions{Exclusive: true})).To(MatchError(op.ErrAccountAlreadyExists))
			_, err := store.GetAccountByName(ctx, "", "acme2")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "the refused name was not indexed")
		})
		It("refuses to change an account's id", func(ctx SpecContext) {
			rec := store.AddAccount(meta.AccountInfo{ID: acct1, Name: "acme"})
			old := rec.Info
			rec.Info.ID = acct2
			Expect(store.PutAccount(ctx, rec, &old, op.PutAccountOptions{})).To(MatchError(op.ErrInvalidArgument))
		})
		It("creates at version 1 and bumps the count under the tag on each write", func(ctx SpecContext) {
			rec := &op.AccountRecord{Info: meta.AccountInfo{ID: acct1, Name: "acme"}}
			Expect(store.PutAccount(ctx, rec, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
			Expect(rec.Version.Ver).To(BeEquivalentTo(1), "created")
			Expect(rec.Version.Tag).To(MatchRegexp(tagPattern), "tag")
			Expect(rec.Mtime).To(Equal(start), "mtime")
			first := rec.Version
			old := rec.Info
			rec.Info.MaxBuckets = 7
			Expect(store.PutAccount(ctx, rec, &old, op.PutAccountOptions{})).To(Succeed())
			Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: first.Ver + 1, Tag: first.Tag}), "bumped")
			got, err := store.GetAccount(ctx, acct1)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(rec))
		})
		It("refuses a write whose version lost a race", func(ctx SpecContext) {
			rec := store.AddAccount(meta.AccountInfo{ID: acct1, Name: "acme"})
			stale := *rec
			old := rec.Info
			Expect(store.PutAccount(ctx, rec, &old, op.PutAccountOptions{})).To(Succeed())
			stale.Info.MaxUsers = 3
			Expect(store.PutAccount(ctx, &stale, &old, op.PutAccountOptions{})).To(MatchError(op.ErrConcurrentModification), "put")
			Expect(store.RemoveAccount(ctx, &stale)).To(MatchError(op.ErrConcurrentModification), "remove")
		})
		It("moves the name and email indexes on rename and drops them on remove", func(ctx SpecContext) {
			rec := store.AddAccount(meta.AccountInfo{ID: acct1, Name: "acme", Email: "a@b"})
			old := rec.Info
			rec.Info.Name = "acme2"
			rec.Info.Email = "c@d"
			Expect(store.PutAccount(ctx, rec, &old, op.PutAccountOptions{})).To(Succeed())
			_, err := store.GetAccountByName(ctx, "", "acme")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "old name")
			_, err = store.GetAccountByEmail(ctx, "a@b")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "old email")
			byEmail, err := store.GetAccountByEmail(ctx, "c@d")
			Expect(err).NotTo(HaveOccurred())
			Expect(byEmail.Info.Name).To(Equal("acme2"))
			Expect(store.AddAccountUser(ctx, acct1, meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "Alice"})).To(Succeed())
			Expect(store.RemoveAccount(ctx, rec)).To(Succeed())
			_, err = store.GetAccountByName(ctx, "", "acme2")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "name")
			_, err = store.GetAccountByEmail(ctx, "c@d")
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "email")
			_, err = store.GetAccount(ctx, rec.Info.ID)
			Expect(err).To(MatchError(op.ErrNoSuchEntity), "account")
			ids, _, err := store.ListAccountUsers(ctx, acct1, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(BeEmpty(), "users index")
			Expect(store.RemoveAccount(ctx, rec)).To(MatchError(op.ErrNoSuchEntity), "again")
		})
		It("keeps the account users index keyed by display name", func(ctx SpecContext) {
			store.AddAccount(meta.AccountInfo{ID: acct1, Name: "acme"})
			Expect(store.AddAccountUser(ctx, acct1, meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "Alice"})).To(Succeed())
			Expect(store.AddAccountUser(ctx, acct1, meta.UserInfo{UserID: meta.UserID{ID: "u2"}, DisplayName: "Bob"})).To(Succeed())
			ids, next, err := store.ListAccountUsers(ctx, acct1, "", 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"u1"}))
			Expect(next).NotTo(BeEmpty())
			ids, next, err = store.ListAccountUsers(ctx, acct1, next, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"u2"}))
			Expect(next).To(BeEmpty())
			Expect(store.RemoveAccountUser(ctx, acct1, "Alice")).To(Succeed())
			ids, _, err = store.ListAccountUsers(ctx, acct1, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"u2"}))
		})
		It("folds display names to lower case as cls_user keys them", func(ctx SpecContext) {
			Expect(store.AddAccountUser(ctx, acct1, meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "Alice"})).To(Succeed())
			Expect(store.AddAccountUser(ctx, acct1, meta.UserInfo{UserID: meta.UserID{ID: "u2"}, DisplayName: "ALICE"})).To(Succeed())
			ids, _, err := store.ListAccountUsers(ctx, acct1, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"u2"}), "the second add overwrote the first")
			Expect(store.RemoveAccountUser(ctx, acct1, "alice")).To(Succeed())
			Expect(store.RemoveAccountUser(ctx, acct1, "alice")).To(MatchError(op.ErrNoSuchKey), "a key the index lacks")
		})
		It("lists at most cls_user's 1000 users a call", func(ctx SpecContext) {
			for i := range 1001 {
				id := string(rune('a'+i/26/26%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i%26))
				Expect(store.AddAccountUser(ctx, acct1, meta.UserInfo{UserID: meta.UserID{ID: id}, DisplayName: id})).To(Succeed())
			}
			ids, next, err := store.ListAccountUsers(ctx, acct1, "", 5000)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(HaveLen(1000))
			Expect(next).To(Equal(ids[999]), "the last key returned")
			ids, next, err = store.ListAccountUsers(ctx, acct1, next, 5000)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(HaveLen(1))
			Expect(next).To(BeEmpty())
		})
	})

	Describe("usage", func() {
		put := func(user, bucket string, epoch uint64, cat string, ops uint64) {
			store.AddUsage(op.UsageRecord{User: user, Owner: user, Bucket: bucket, Epoch: epoch, Categories: map[string]op.UsageData{cat: {Ops: ops, SuccessfulOps: ops}}})
		}

		It("records usage for the specs and reads it back", func(ctx SpecContext) {
			store.AddUsage(op.UsageRecord{User: "alice", Owner: "alice", Bucket: "plain", Epoch: 3600, Categories: map[string]op.UsageData{"put_obj": {Ops: 1, SuccessfulOps: 1}}})
			var it op.UsageIter
			recs, truncated, err := store.ReadUsage(ctx, "alice", "", 0, ^uint64(0), 1000, &it)
			Expect(err).NotTo(HaveOccurred())
			Expect(truncated).To(BeFalse())
			Expect(recs).To(HaveLen(1))
			Expect(recs[0].Bucket).To(Equal("plain"))
		})
		It("aggregates a page per user and bucket as rgw_usage_log_entry::aggregate does", func(ctx SpecContext) {
			put("alice", "b1", 7200, "get_obj", 2)
			put("alice", "b1", 3600, "put_obj", 1)
			put("alice", "b1", 3600, "get_obj", 1)
			store.AddUsage(op.UsageRecord{
				Owner: "alice", Payer: "carol", Bucket: "rp", Epoch: 3600, S3Select: op.S3SelectUsage{BytesProcessed: 5},
				Total: op.UsageData{Ops: 99}, Categories: map[string]op.UsageData{"get_obj": {BytesSent: 10, Ops: 1}},
			})
			recs, truncated, err := store.ReadUsage(ctx, "", "", 0, ^uint64(0), 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(truncated).To(BeFalse())
			Expect(recs).To(Equal([]op.UsageRecord{
				{
					User: "alice", Owner: "alice", Bucket: "b1", Epoch: 3600,
					Total:      op.UsageData{Ops: 4, SuccessfulOps: 4},
					Categories: map[string]op.UsageData{"get_obj": {Ops: 3, SuccessfulOps: 3}, "put_obj": {Ops: 1, SuccessfulOps: 1}},
				},
				{
					User: "carol", Owner: "alice", Payer: "carol", Bucket: "rp", Epoch: 3600,
					Total:      op.UsageData{BytesSent: 10, Ops: 1},
					Categories: map[string]op.UsageData{"get_obj": {BytesSent: 10, Ops: 1}},
					S3Select:   op.S3SelectUsage{BytesProcessed: 5},
				},
			}), "the payer keys the record and the total is the categories' sum")
		})
		It("filters by user, bucket and the [start, end) epoch range", func(ctx SpecContext) {
			put("alice", "b1", 3600, "get_obj", 1)
			put("alice", "b2", 3600, "get_obj", 1)
			put("alice", "b1", 7200, "get_obj", 1)
			put("bob", "b1", 3600, "get_obj", 1)
			recs, _, err := store.ReadUsage(ctx, "alice", "b1", 3600, 7200, 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(HaveLen(1))
			Expect(recs[0]).To(And(
				HaveField("User", "alice"), HaveField("Bucket", "b1"), HaveField("Epoch", BeEquivalentTo(3600)),
				HaveField("Total", op.UsageData{Ops: 1, SuccessfulOps: 1}),
			))
		})
		It("pages by records and continues from the iterator", func(ctx SpecContext) {
			put("alice", "b1", 3600, "get_obj", 1)
			put("alice", "b2", 3600, "get_obj", 1)
			put("bob", "b1", 3600, "get_obj", 1)
			var it op.UsageIter
			recs, truncated, err := store.ReadUsage(ctx, "", "", 0, ^uint64(0), 2, &it)
			Expect(err).NotTo(HaveOccurred())
			Expect(truncated).To(BeTrue())
			Expect(recs).To(HaveLen(2))
			Expect(recs[1].Bucket).To(Equal("b2"))
			recs, truncated, err = store.ReadUsage(ctx, "", "", 0, ^uint64(0), 2, &it)
			Expect(err).NotTo(HaveOccurred())
			Expect(truncated).To(BeFalse())
			Expect(recs).To(HaveLen(1))
			Expect(recs[0].User).To(Equal("bob"))
		})
		It("trims the records a read with the same filters would return", func(ctx SpecContext) {
			put("alice", "b1", 3600, "get_obj", 1)
			put("alice", "b2", 3600, "get_obj", 1)
			put("bob", "b1", 3600, "get_obj", 1)
			Expect(store.TrimUsage(ctx, "alice", "b1", 0, ^uint64(0))).To(Succeed())
			recs, _, err := store.ReadUsage(ctx, "", "", 0, ^uint64(0), 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(HaveLen(2))
			Expect([]string{recs[0].User + "/" + recs[0].Bucket, recs[1].User + "/" + recs[1].Bucket}).To(Equal([]string{"alice/b2", "bob/b1"}))
			Expect(store.TrimUsage(ctx, "", "", 0, ^uint64(0))).To(Succeed(), "trimming everything")
			recs, _, err = store.ReadUsage(ctx, "", "", 0, ^uint64(0), 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(BeEmpty())
			Expect(store.TrimUsage(ctx, "", "", 0, ^uint64(0))).To(Succeed(), "nothing left to trim")
		})
		It("keeps its own copy of a seeded record", func(ctx SpecContext) {
			rec := op.UsageRecord{User: "alice", Owner: "alice", Bucket: "b1", Epoch: 3600, Categories: map[string]op.UsageData{"get_obj": {Ops: 1}}}
			store.AddUsage(rec)
			rec.Categories["get_obj"] = op.UsageData{Ops: 50}
			recs, _, err := store.ReadUsage(ctx, "alice", "", 0, ^uint64(0), 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(recs[0].Categories).To(Equal(map[string]op.UsageData{"get_obj": {Ops: 1}}))
		})
	})

	Describe("ChownBucket", func() {
		encodeACL := func(p acl.Policy) []byte {
			e := denc.NewEncoder()
			p.Encode(e, denc.Squid)
			return e.Bytes()
		}
		decodeACL := func(b []byte) acl.Policy {
			GinkgoHelper()
			d := denc.NewDecoder(b)
			p := acl.DecodePolicy(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			return p
		}
		It("moves the bucket to the new owner and swaps the old owner's grant for the new one's", func(ctx SpecContext) {
			pol := acl.DefaultPolicy(owner("alice"), "Alice")
			pol.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermRead})
			rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
				Name: "b", Owner: owner("alice"), Attrs: map[string][]byte{meta.AttrACL: encodeACL(pol)},
			})
			Expect(err).NotTo(HaveOccurred())
			ver := rec.Version
			Expect(store.ChownBucket(ctx, rec, meta.AccountOwner(acct1), "acme")).To(Succeed())

			got, err := store.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(meta.AccountOwner(acct1)), "instance owner")
			Expect(got.EntryPoint.Owner).To(Equal(meta.AccountOwner(acct1)), "entry point owner, as link() rewrites it")
			Expect(got.Version.Ver).To(Equal(ver.Ver+1), "the instance was rewritten")
			Expect(rec.Version).To(Equal(got.Version), "rec follows the write")
			p := decodeACL(got.Attrs[meta.AttrACL])
			Expect(p.Owner).To(Equal(acl.Owner{ID: acct1, DisplayName: "acme"}))
			var grants []string
			for _, g := range p.ACL.Grants {
				grants = append(grants, g.Key+"="+g.Grant.Name)
			}
			Expect(grants).To(ConsistOf("bob=Bob", acct1+"=acme"), "alice's grant replaced, bob's kept")
			ents, _, _, err := store.ListUserBuckets(ctx, owner("alice"), "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(BeEmpty(), "unlinked from the old owner")
			ents, _, _, err = store.ListUserBuckets(ctx, meta.AccountOwner(acct1), "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ents).To(HaveLen(1), "linked to the new owner")
		})
		It("leaves a bucket without an ACL without one, and refuses a missing bucket", func(ctx SpecContext) {
			rec := mustCreate(ctx, store, "", "b", owner("alice"))
			Expect(store.ChownBucket(ctx, rec, owner("bob"), "Bob")).To(Succeed())
			got, err := store.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(owner("bob")))
			Expect(got.Attrs).NotTo(HaveKey(meta.AttrACL))
			Expect(store.ChownBucket(ctx, &op.BucketRecord{}, owner("bob"), "Bob")).To(MatchError(op.ErrNoSuchBucket))
		})
		It("retries past a version another writer moved, as adopt_user_bucket does", func(ctx SpecContext) {
			rec := mustCreate(ctx, store, "", "b", owner("alice"))
			stale := *rec
			Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
			Expect(store.ChownBucket(ctx, &stale, owner("bob"), "Bob")).To(Succeed())
			got, err := store.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(owner("bob")))
		})
	})

	DescribeTable("answers NotImplemented from the bucket admin store",
		func(ctx SpecContext, call func(context.Context, *memstore.Store) error) {
			Expect(call(ctx, store)).To(MatchError(op.ErrNotImplemented))
		},
		Entry("CheckIndex", func(ctx context.Context, s *memstore.Store) error {
			_, _, err := s.CheckIndex(ctx, &op.BucketRecord{})
			return err
		}),
		Entry("RebuildIndex", func(ctx context.Context, s *memstore.Store) error {
			return s.RebuildIndex(ctx, &op.BucketRecord{})
		}),
		Entry("RemoveIndexEntries", func(ctx context.Context, s *memstore.Store) error {
			return s.RemoveIndexEntries(ctx, &op.BucketRecord{}, []meta.ObjKey{{Name: "k"}})
		}),
	)

	Describe("bucket administration", func() {
		withACL := func(ctx context.Context, name string, o meta.Owner, display string) *op.BucketRecord {
			GinkgoHelper()
			e := denc.NewEncoder()
			acl.DefaultPolicy(o, display).Encode(e, denc.Squid)
			rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: name, Owner: o, Attrs: map[string][]byte{meta.AttrACL: e.Bytes()}})
			Expect(err).NotTo(HaveOccurred())
			return rec
		}
		policyOf := func(rec *op.BucketRecord) acl.Policy {
			GinkgoHelper()
			d := denc.NewDecoder(rec.Attrs[meta.AttrACL])
			p := acl.DecodePolicy(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			return p
		}
		names := func(ctx context.Context, o meta.Owner) []string {
			GinkgoHelper()
			ents, _, _, err := store.ListUserBuckets(ctx, o, "", 100)
			Expect(err).NotTo(HaveOccurred())
			var out []string
			for _, e := range ents {
				out = append(out, e.Bucket.Name)
			}
			return out
		}
		It("sums the objects into the Main category with radosgw's shard strings", func(ctx SpecContext) {
			rec := mustCreate(ctx, store, "", "b", owner("alice"))
			st, err := store.IndexStats(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Categories).To(BeEmpty())
			mustPut(ctx, store, rec, "k1", "hello")
			mustPut(ctx, store, rec, "k2", "abc")
			st, err = store.IndexStats(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Categories).To(Equal(map[string]op.CategoryStats{"rgw.main": {Size: 8, SizeRounded: 8192, SizeUtilized: 8, NumObjects: 2}}))
			Expect(st.Ver).To(MatchRegexp(`^0#[1-9][0-9]*$`))
			Expect(st.MasterVer).To(Equal("0#0"))
			Expect(st.MaxMarker).To(Equal("0#"))
			rec.Info.Layout.Current.Layout.Normal.NumShards = 3
			st, err = store.IndexStats(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.MasterVer).To(Equal("0#0,1#0,2#0"))
			Expect(st.MaxMarker).To(Equal("0#,1#,2#"))
			_, err = store.IndexStats(ctx, &op.BucketRecord{})
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
		It("changes the owner: ACL, instance owner, entry point and the owners' lists", func(ctx SpecContext) {
			rec := withACL(ctx, "b", owner("alice"), "Alice")
			Expect(store.ChangeBucketOwner(ctx, rec, owner("bob"), "Bob", nil)).To(Succeed())
			got, err := store.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(owner("bob")))
			Expect(got.EntryPoint.Owner).To(Equal(owner("bob")))
			Expect(got.EntryPoint.Linked).To(BeTrue())
			Expect(policyOf(got).Owner).To(Equal(acl.Owner{ID: "bob", DisplayName: "Bob"}))
			Expect(policyOf(got).ACL.Grants).To(HaveLen(1), "a default ACL replaces the old one")
			Expect(names(ctx, owner("bob"))).To(ConsistOf("b"))
			Expect(names(ctx, owner("alice"))).To(BeEmpty())
			Expect(rec.Version).To(Equal(got.Version))
		})
		It("renames, keeping the instance id and marker, and refuses a name another bucket holds", func(ctx SpecContext) {
			rec := withACL(ctx, "old", owner("alice"), "Alice")
			id := rec.Info.Bucket.ID
			taken := mustCreate(ctx, store, "", "taken", owner("carol"))
			Expect(store.ChangeBucketOwner(ctx, rec, owner("alice"), "Alice", &meta.BucketID{Name: "taken"})).To(MatchError(op.ErrBucketAlreadyExists))
			got, err := store.GetBucket(ctx, "", "taken")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(taken.Info.Bucket.ID))
			Expect(store.ChangeBucketOwner(ctx, rec, owner("alice"), "Alice", &meta.BucketID{Tenant: "t", Name: "new"})).To(Succeed())
			_, err = store.GetBucket(ctx, "", "old")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			got, err = store.GetBucket(ctx, "t", "new")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Bucket.ID).To(Equal(id))
			Expect(got.Info.Bucket.Marker).To(Equal(rec.Info.Bucket.Marker))
			Expect(rec.Info.Bucket).To(Equal(got.Info.Bucket))
		})
		It("refuses a bucket without an ACL", func(ctx SpecContext) {
			rec := mustCreate(ctx, store, "", "b", owner("alice"))
			Expect(store.ChangeBucketOwner(ctx, rec, owner("bob"), "Bob", nil)).To(MatchError(op.ErrInvalidArgument))
			got, err := store.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Info.Owner).To(Equal(owner("alice")))
		})
		It("unlinks: linked=false, and the entry point's other owner refused", func(ctx SpecContext) {
			rec := withACL(ctx, "b", owner("alice"), "Alice")
			Expect(store.UnlinkBucketOwner(ctx, rec, owner("bob"))).To(MatchError(op.ErrInvalidArgument), "do_unlink_bucket's owner mismatch")
			Expect(store.UnlinkBucketOwner(ctx, rec, owner("alice"))).To(Succeed())
			got, err := store.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			Expect(got.EntryPoint.Linked).To(BeFalse())
			Expect(store.UnlinkBucketOwner(ctx, rec, owner("bob"))).To(Succeed(), "an unlinked entry point is done")
		})
		It("purges every object and upload with bypass-gc and leaves the bucket", func(ctx SpecContext) {
			rec := mustCreate(ctx, store, "", "b", owner("alice"))
			mustPut(ctx, store, rec, "k1", "x")
			_, err := store.CreateUpload(ctx, rec, meta.ObjKey{Name: "up"}, op.UploadParams{})
			Expect(err).NotTo(HaveOccurred())
			other := mustCreate(ctx, store, "", "other", owner("alice"))
			mustPut(ctx, store, other, "keep", "x")
			Expect(store.PurgeBypassGC(ctx, rec)).To(Succeed())
			res, err := store.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Entries).To(BeEmpty())
			res, err = store.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10, NS: "multipart"})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Entries).To(BeEmpty())
			Expect(store.DeleteBucket(ctx, rec)).To(Succeed())
			res, err = store.ListObjects(ctx, other, op.ListObjectsParams{MaxKeys: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Entries).To(HaveLen(1))
		})
		It("syncs owner stats as a no-op, its stats being always current", func(ctx SpecContext) {
			Expect(store.SyncOwnerStats(ctx, owner("alice"))).To(Succeed())
		})
		It("lists the bucket metadata section by radosgw's keys", func(ctx SpecContext) {
			mustCreate(ctx, store, "", "b1", owner("alice"))
			mustCreate(ctx, store, "t", "b2", owner("alice"))
			keys, next, more, err := store.List(ctx, "bucket", "", 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys).To(Equal([]string{"b1"}))
			Expect(more).To(BeTrue())
			keys, _, more, err = store.List(ctx, "bucket", next, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys).To(Equal([]string{"t/b2"}))
			Expect(more).To(BeFalse())
		})
	})

	Describe("realms and periods", func() {
		var (
			realm  meta.Realm
			period meta.Period
		)
		BeforeEach(func() {
			realm = meta.Realm{ID: "r1", Name: "gold", CurrentPeriod: "p1", Epoch: 2}
			period = meta.Period{ID: "p1", Epoch: 3, RealmID: "r1", RealmEpoch: 2}
			store = memstore.New(memstore.Config{Realm: realm, Period: period})
		})

		DescribeTable("finds the seeded realm as RGWRealm::init does",
			func(ctx SpecContext, id, name string) {
				got, err := store.GetRealm(ctx, id, name)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(realm))
			},
			Entry("by id", "r1", ""),
			Entry("by name", "", "gold"),
			Entry("as the default", "", ""),
			Entry("by id before name", "r1", "nosuch"),
		)
		DescribeTable("answers NoSuchKey for a realm it lacks",
			func(ctx SpecContext, id, name string) {
				_, err := store.GetRealm(ctx, id, name)
				Expect(err).To(MatchError(op.ErrNoSuchKey))
			},
			Entry("by id", "nosuch", ""),
			Entry("by name", "", "nosuch"),
		)
		It("has no default realm when none was seeded", func(ctx SpecContext) {
			bare, _ := newStore()
			_, err := bare.GetRealm(ctx, "", "")
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			def, names, err := bare.ListRealms(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(def).To(BeEmpty())
			Expect(names).To(BeEmpty())
		})
		It("lists the realms and the default", func(ctx SpecContext) {
			def, names, err := store.ListRealms(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(def).To(Equal("r1"))
			Expect(names).To(Equal([]string{"gold"}))
		})
		DescribeTable("finds a period as RGWPeriod::init does",
			func(ctx SpecContext, realmID, periodID string, epoch uint32) {
				got, err := store.GetPeriod(ctx, realmID, periodID, epoch)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(period))
			},
			Entry("by id and epoch", "", "p1", uint32(3)),
			Entry("by id at its latest epoch", "", "p1", uint32(0)),
			Entry("the realm's current period", "r1", "", uint32(0)),
			Entry("the default realm's current period", "", "", uint32(0)),
		)
		DescribeTable("answers NoSuchKey for a period it lacks",
			func(ctx SpecContext, realmID, periodID string, epoch uint32) {
				_, err := store.GetPeriod(ctx, realmID, periodID, epoch)
				Expect(err).To(MatchError(op.ErrNoSuchKey))
			},
			Entry("another epoch", "", "p1", uint32(2)),
			Entry("another id", "", "p2", uint32(0)),
			Entry("a realm it lacks", "nosuch", "", uint32(0)),
		)
		It("reads back the period config it was given, and NotFound before", func(ctx SpecContext) {
			_, err := store.GetPeriodConfig(ctx, "r1")
			Expect(err).To(MatchError(op.ErrNotFound))
			cfg := meta.PeriodConfig{UserRateLimit: meta.RateLimitInfo{MaxReadOps: 5, Enabled: true}}
			Expect(store.PutPeriodConfig(ctx, "r1", cfg)).To(Succeed())
			got, err := store.GetPeriodConfig(ctx, "r1")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(cfg))
			_, err = store.GetPeriodConfig(ctx, "")
			Expect(err).To(MatchError(op.ErrNotFound), "another realm's config")
		})
	})
})
