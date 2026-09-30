package op_test

import (
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("LogUsage", func() {
	const accountID = "RGW12345678901234567"
	var (
		t0         time.Time
		alice      meta.Owner
		store      *memstore.Store
		plain, rp  *op.BucketRecord
		bob, carol op.Identity
	)
	identity := func(id, account string) op.Identity {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: id}
		u.AccountID = account
		if account == "" {
			return op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll}
		}
		return op.Identity{User: &u, Owner: meta.AccountOwner(account), Account: &meta.AccountInfo{ID: account}, OpMask: op.OpTypeAll}
	}
	// request is a finished request whose Init loaded rec and whose response
	// went out with status. It began a minute before t0: log_usage stamps the
	// entry when the request is done, not when it began.
	request := func(id op.Identity, bucket string, rec *op.BucketRecord, status int) *op.Request {
		return &op.Request{
			Time:      t0.Add(-time.Minute),
			Bucket:    bucket,
			Identity:  id,
			BucketRec: rec,
			Status:    status,
			Env:       &op.Env{Buckets: store, Usage: store, Now: func() time.Time { return t0 }},
		}
	}
	BeforeEach(func(ctx SpecContext) {
		t0 = time.Date(2026, 9, 29, 12, 34, 56, 0, time.UTC)
		alice = meta.UserOwner(meta.UserID{ID: "alice"})
		store = memstore.New(memstore.Config{})
		load := func(name string) *op.BucketRecord {
			rec, err := store.GetBucket(ctx, "", name)
			Expect(err).NotTo(HaveOccurred(), "bucket %q", name)
			return rec
		}
		for _, name := range []string{"plain", "rp"} {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: name, Owner: alice})
			Expect(err).NotTo(HaveOccurred(), "bucket %q", name)
		}
		rp = load("rp")
		rp.Info.RequesterPays = true
		Expect(store.PutBucketInfo(ctx, rp)).To(Succeed())
		plain, rp = load("plain"), load("rp")
		bob, carol = identity("bob", ""), identity("carol", accountID)
	})

	It("logs nothing for a request on a bucket that does not exist", func(ctx SpecContext) {
		op.LogUsage(ctx, request(bob, "gone", nil, http.StatusNotFound), "get_obj")
		Expect(store.Usage()).To(BeEmpty())
	})
	It(`files a NoSuchKey on an existing bucket under the bucket's owner and the bucket "-"`, func(ctx SpecContext) {
		r := request(bob, "plain", plain, http.StatusNotFound)
		r.BytesOut = 250
		op.LogUsage(ctx, r, "get_obj")
		Expect(store.Usage()).To(Equal([]op.UsageEntry{{
			Owner: alice, Bucket: "-", Time: t0, Category: "get_obj", BytesSent: 250, Ops: 1,
		}}))
	})
	It("names an account user, not its account, as the payer on a requester-pays bucket", func(ctx SpecContext) {
		r := request(carol, "rp", rp, http.StatusOK)
		r.BytesIn, r.BytesOut = 1024, 20
		op.LogUsage(ctx, r, "put_obj")
		Expect(store.Usage()).To(Equal([]op.UsageEntry{{
			Owner: alice, Payer: meta.UserOwner(meta.UserID{ID: "carol"}), Bucket: "rp", Time: t0,
			Category: "put_obj", BytesSent: 20, BytesReceived: 1024, Ops: 1, SuccessfulOps: 1,
		}}))
	})
	It("sets no payer when a requester-pays bucket refuses the request with 403", func(ctx SpecContext) {
		op.LogUsage(ctx, request(carol, "rp", rp, http.StatusForbidden), "get_obj")
		Expect(store.Usage()).To(Equal([]op.UsageEntry{{
			Owner: alice, Bucket: "rp", Time: t0, Category: "get_obj", Ops: 1,
		}}))
	})
	It(`keeps the payer on a refusal other than 403, and files a 404 under "-"`, func(ctx SpecContext) {
		op.LogUsage(ctx, request(carol, "rp", rp, http.StatusNotFound), "get_obj")
		Expect(store.Usage()).To(Equal([]op.UsageEntry{{
			Owner: alice, Payer: meta.UserOwner(meta.UserID{ID: "carol"}), Bucket: "-", Time: t0, Category: "get_obj", Ops: 1,
		}}))
	})
	It(`files ListBuckets under the identity's owner, the account for an account user, with the bucket ""`, func(ctx SpecContext) {
		op.LogUsage(ctx, request(carol, "", nil, http.StatusOK), "list_buckets")
		Expect(store.Usage()).To(Equal([]op.UsageEntry{{
			Owner: meta.AccountOwner(accountID), Time: t0, Category: "list_buckets", Ops: 1, SuccessfulOps: 1,
		}}))
	})
	It(`files a bucketless 404 under "-" too`, func(ctx SpecContext) {
		op.LogUsage(ctx, request(bob, "", nil, http.StatusNotFound), "get_user_info")
		Expect(store.Usage()).To(Equal([]op.UsageEntry{{
			Owner: meta.UserOwner(meta.UserID{ID: "bob"}), Bucket: "-", Time: t0, Category: "get_user_info", Ops: 1,
		}}))
	})
	It("logs nothing for a bucketless request refused at authentication", func(ctx SpecContext) {
		// Authentication failed, so the handler never set an identity on r.
		op.LogUsage(ctx, request(op.Identity{}, "", nil, http.StatusForbidden), "list_buckets")
		Expect(store.Usage()).To(BeEmpty())
	})
	It("keeps an entry that has a payer but no owner, as radosgw's flush keeps it", func(ctx SpecContext) {
		unowned := &op.BucketRecord{Info: meta.BucketInfo{RequesterPays: true}}
		op.LogUsage(ctx, request(carol, "rp", unowned, http.StatusOK), "get_obj")
		Expect(store.Usage()).To(Equal([]op.UsageEntry{{
			Payer: meta.UserOwner(meta.UserID{ID: "carol"}), Bucket: "rp", Time: t0, Category: "get_obj", Ops: 1, SuccessfulOps: 1,
		}}))
	})
	It("logs nothing for a system identity", func(ctx SpecContext) {
		r := request(bob, "plain", plain, http.StatusOK)
		r.Identity.System = true
		op.LogUsage(ctx, r, "get_obj")
		Expect(store.Usage()).To(BeEmpty())
	})
	It("does nothing without an Env", func(ctx SpecContext) {
		r := request(bob, "plain", plain, http.StatusOK)
		r.Env = nil
		Expect(func() { op.LogUsage(ctx, r, "get_obj") }).NotTo(Panic())
	})
	It("does nothing without a usage logger", func(ctx SpecContext) {
		r := request(bob, "plain", plain, http.StatusOK)
		r.Env.Usage = nil
		Expect(func() { op.LogUsage(ctx, r, "get_obj") }).NotTo(Panic())
	})
	DescribeTable("counts the op as successful only for a status rgw_err::is_err does not call an error",
		func(ctx SpecContext, status int, successful uint64) {
			op.LogUsage(ctx, request(bob, "plain", plain, status), "get_obj")
			Expect(store.Usage()).To(HaveExactElements(HaveField("SuccessfulOps", successful)))
		},
		Entry("199, below the success range", 199, uint64(0)),
		Entry("200, its lower bound", 200, uint64(1)),
		Entry("399, its upper bound", 399, uint64(1)),
		Entry("400, above it", 400, uint64(0)),
	)
})
