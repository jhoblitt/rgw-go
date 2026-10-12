package op_test

import (
	"context"
	"math"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var _ = Describe("admin usage ops", func() {
	const (
		h1   = uint64(1790596800)
		h2   = h1 + 3600
		acct = "RGW00000000000000001"
	)
	var (
		ctx   context.Context
		store *memstore.Store
		env   *op.Env
	)
	put := func(user, bucket string, epoch uint64, cat string, ops uint64) {
		store.AddUsage(op.UsageRecord{
			Owner: user, Bucket: bucket, Epoch: epoch,
			Categories: map[string]op.UsageData{cat: {BytesReceived: 10 * ops, Ops: ops, SuccessfulOps: ops}},
		})
	}
	BeforeEach(func() {
		ctx = context.Background()
		pools := meta.NewZonePlacementInfo()
		store = memstore.New(memstore.Config{Params: meta.ZoneParams{
			Name: "z1", PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": pools},
		}})
		for _, b := range []struct {
			name  string
			owner meta.Owner
		}{
			{"plain", meta.UserOwner(meta.UserID{ID: "alice"})},
			{"acctb", meta.AccountOwner(acct)},
		} {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{
				Name: b.name, Owner: b.owner, Placement: meta.PlacementRule{Name: "default-placement"},
			})
			Expect(err).NotTo(HaveOccurred())
		}
		env = &op.Env{Zone: store, Users: store, Buckets: store, UsageReader: store, Metrics: op.NopMetrics{}}
		put("alice", "plain", h1, "put_obj", 1)
		put("alice", "plain", h2, "get_obj", 2)
		put("alice", "other", h1, "put_obj", 3)
		put("bob", "b", h2, "get_obj", 4)
	})
	runAs := func(c context.Context, o op.Op, caps meta.Caps) error {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Run(c, o, &op.Request{Env: env, Identity: op.Identity{
			User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps,
		}})
	}
	show := func(mod func(o *op.ShowUsage)) (*op.ShowUsage, error) {
		o := &op.ShowUsage{End: math.MaxUint64, ShowEntries: true, ShowSummary: true}
		if mod != nil {
			mod(o)
		}
		return o, runAs(ctx, o, meta.Caps{"usage": meta.CapRead})
	}
	trim := func(o *op.TrimUsage) error { return runAs(ctx, o, meta.Caps{"usage": meta.CapWrite}) }
	left := func() []string {
		GinkgoHelper()
		recs, _, err := store.ReadUsage(ctx, "", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, r := range recs {
			out = append(out, r.User+"/"+r.Bucket)
		}
		return out
	}

	Describe("ShowUsage", func() {
		It("groups the entries by user in order and sums each user's summary", func() {
			o, err := show(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Entries).To(HaveLen(2))
			Expect(o.Entries[0].User).To(Equal("alice"))
			Expect(o.Entries[0].Buckets).To(HaveLen(2))
			Expect(o.Entries[0].Buckets[0]).To(And(HaveField("Bucket", "other"), HaveField("Epoch", h1)))
			Expect(o.Entries[0].Buckets[1]).To(And(HaveField("Bucket", "plain"), HaveField("Epoch", h1),
				HaveField("Total", op.UsageData{BytesReceived: 30, Ops: 3, SuccessfulOps: 3})), "the first hour's epoch, both hours' sums")
			Expect(o.Entries[1].User).To(Equal("bob"))
			Expect(o.Summary).To(Equal([]op.UsageSummary{
				{
					User: "alice",
					Categories: map[string]op.UsageData{
						"put_obj": {BytesReceived: 40, Ops: 4, SuccessfulOps: 4},
						"get_obj": {BytesReceived: 20, Ops: 2, SuccessfulOps: 2},
					},
					Total: op.UsageData{BytesReceived: 60, Ops: 6, SuccessfulOps: 6},
				},
				{
					User:       "bob",
					Categories: map[string]op.UsageData{"get_obj": {BytesReceived: 40, Ops: 4, SuccessfulOps: 4}},
					Total:      op.UsageData{BytesReceived: 40, Ops: 4, SuccessfulOps: 4},
				},
			}))
		})

		It("filters the summary's categories, total and s3select by the categories asked for", func() {
			store.AddUsage(op.UsageRecord{
				Owner: "carol", Bucket: "c", Epoch: h1, S3Select: op.S3SelectUsage{BytesProcessed: 7},
				Categories: map[string]op.UsageData{"get_obj": {Ops: 1}},
			})
			o, err := show(func(o *op.ShowUsage) { o.Categories = map[string]bool{"get_obj": true} })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Summary[0]).To(Equal(op.UsageSummary{
				User:       "alice",
				Categories: map[string]op.UsageData{"get_obj": {BytesReceived: 20, Ops: 2, SuccessfulOps: 2}},
				Total:      op.UsageData{BytesReceived: 20, Ops: 2, SuccessfulOps: 2},
			}))
			Expect(o.Summary[2].S3Select).To(BeZero(), "s3select is a category the filter leaves out")
			Expect(o.Entries[0].Buckets[1].Categories).To(HaveLen(2), "an entry keeps its categories for the dump to filter")
			o, err = show(func(o *op.ShowUsage) { o.Categories = map[string]bool{"s3select": true} })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Summary[2]).To(Equal(op.UsageSummary{User: "carol", S3Select: op.S3SelectUsage{BytesProcessed: 7}}))
		})

		It("leaves out the entries when they are not shown", func() {
			o, err := show(func(o *op.ShowUsage) { o.ShowEntries = false })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Entries).To(BeNil())
			Expect(o.Summary).To(HaveLen(2))
		})

		It("reads a user's usage, or a bucket's under its owner", func() {
			o, err := show(func(o *op.ShowUsage) { o.UID = "bob" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Entries).To(ConsistOf(HaveField("User", "bob")))
			o, err = show(func(o *op.ShowUsage) { o.UID = "bob"; o.Bucket = "plain" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Entries).To(HaveLen(1))
			Expect(o.Entries[0].User).To(Equal("alice"), "the bucket's owner, whatever the uid")
			Expect(o.Entries[0].Buckets).To(ConsistOf(HaveField("Bucket", "plain")))
		})

		It("reads the epochs in [start, end)", func() {
			o, err := show(func(o *op.ShowUsage) { o.Start = h2 })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Summary).To(HaveLen(2))
			Expect(o.Summary[0].Total.Ops).To(Equal(uint64(2)))
			o, err = show(func(o *op.ShowUsage) { o.End = h2 })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Summary).To(ConsistOf(HaveField("User", "alice")))
		})

		It("answers NoSuchKey for a bucket that does not exist, as load_bucket's ENOENT", func() {
			_, err := show(func(o *op.ShowUsage) { o.Bucket = "missing" })
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})

		It("answers UnknownError, ENOTSUP, for an account's bucket", func() {
			_, err := show(func(o *op.ShowUsage) { o.Bucket = "acctb" })
			Expect(op.AsError(err)).To(And(HaveField("Code", "UnknownError"), HaveField("Status", 500), HaveField("Errno", int(syscall.ENOTSUP))))
		})

		It("opens a user's section again when the user comes back on a later page", func() {
			fake := new(opfakes.FakeUsageReader)
			fake.ReadUsageReturnsOnCall(0, []op.UsageRecord{
				{User: "alice", Bucket: "a", Total: op.UsageData{Ops: 1}, Categories: map[string]op.UsageData{"put_obj": {Ops: 1}}},
				{User: "bob", Bucket: "b"},
			}, true, nil)
			fake.ReadUsageReturnsOnCall(1, []op.UsageRecord{
				{User: "alice", Bucket: "c", Total: op.UsageData{Ops: 2}, Categories: map[string]op.UsageData{"put_obj": {Ops: 2}}},
			}, false, nil)
			env.UsageReader = fake
			o, err := show(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Entries).To(HaveLen(3))
			Expect([]string{o.Entries[0].User, o.Entries[1].User, o.Entries[2].User}).To(Equal([]string{"alice", "bob", "alice"}))
			Expect(o.Summary[0].Total.Ops).To(Equal(uint64(3)), "the summary merges both pages")
			_, user, bucket, start, end, maxRecs, it := fake.ReadUsageArgsForCall(1)
			Expect([]any{user, bucket, start, end, maxRecs}).To(Equal([]any{"", "", uint64(0), uint64(math.MaxUint64), uint32(1000)}))
			Expect(it).NotTo(BeNil())
		})

		It("stops paging when its context ends", func(sctx SpecContext) {
			fake := new(opfakes.FakeUsageReader)
			env.UsageReader = fake
			c, cancel := context.WithCancel(sctx)
			fake.ReadUsageCalls(func(context.Context, string, string, uint64, uint64, uint32, *op.UsageIter) ([]op.UsageRecord, bool, error) {
				cancel()
				return nil, true, nil
			})
			o := &op.ShowUsage{End: math.MaxUint64, ShowEntries: true, ShowSummary: true}
			Expect(runAs(c, o, meta.Caps{"usage": meta.CapRead})).To(MatchError(context.Canceled))
		}, SpecTimeout(10*time.Second))

		It("refuses a caller without usage=read before reading", func() {
			fake := new(opfakes.FakeUsageReader)
			env.UsageReader = fake
			for _, caps := range []meta.Caps{nil, {"usage": meta.CapWrite}, {"users": meta.CapAll, "buckets": meta.CapAll}} {
				Expect(runAs(ctx, &op.ShowUsage{End: math.MaxUint64}, caps)).To(MatchError(op.ErrAccessDenied), "caps %v", caps)
			}
			Expect(fake.ReadUsageCallCount()).To(BeZero())
		})
	})

	Describe("TrimUsage", func() {
		It("refuses to trim everything without remove-all", func() {
			Expect(trim(&op.TrimUsage{End: math.MaxUint64})).To(MatchError(op.ErrInvalidArgument))
			Expect(trim(&op.TrimUsage{UID: "t$", End: math.MaxUint64})).To(MatchError(op.ErrInvalidArgument), "a uid without an id is no user")
			Expect(left()).To(Equal([]string{"alice/other", "alice/plain", "bob/b"}))
		})

		It("trims everything with remove-all", func() {
			Expect(trim(&op.TrimUsage{End: math.MaxUint64, RemoveAll: true})).To(Succeed())
			Expect(left()).To(BeEmpty())
		})

		It("trims a range of every user's usage without remove-all", func() {
			Expect(trim(&op.TrimUsage{Start: h2, End: math.MaxUint64})).To(Succeed())
			Expect(left()).To(Equal([]string{"alice/other", "alice/plain"}))
		})

		It("trims only the user named", func() {
			Expect(trim(&op.TrimUsage{UID: "alice", End: math.MaxUint64})).To(Succeed())
			Expect(left()).To(Equal([]string{"bob/b"}))
		})

		It("trims only the bucket named, under its owner", func() {
			Expect(trim(&op.TrimUsage{UID: "bob", Bucket: "plain", End: math.MaxUint64})).To(Succeed())
			Expect(left()).To(Equal([]string{"alice/other", "bob/b"}))
		})

		It("answers NoSuchKey for a missing bucket and UnknownError for an account's, trimming nothing", func() {
			Expect(trim(&op.TrimUsage{Bucket: "missing", End: math.MaxUint64})).To(MatchError(op.ErrNoSuchKey))
			err := trim(&op.TrimUsage{Bucket: "acctb", End: math.MaxUint64})
			Expect(op.AsError(err)).To(And(HaveField("Code", "UnknownError"), HaveField("Errno", int(syscall.ENOTSUP))))
			Expect(left()).To(Equal([]string{"alice/other", "alice/plain", "bob/b"}))
		})

		It("refuses a caller without usage=write before trimming", func() {
			for _, caps := range []meta.Caps{nil, {"usage": meta.CapRead}, {"users": meta.CapAll, "buckets": meta.CapAll, "metadata": meta.CapAll}} {
				Expect(runAs(ctx, &op.TrimUsage{End: math.MaxUint64, RemoveAll: true}, caps)).To(MatchError(op.ErrAccessDenied), "caps %v", caps)
			}
			Expect(left()).To(Equal([]string{"alice/other", "alice/plain", "bob/b"}))
		})
	})
})
