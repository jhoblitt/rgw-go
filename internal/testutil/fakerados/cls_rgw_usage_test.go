package fakerados_test

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("RGWClass's usage read, trim and clear", func() {
	const (
		logPool = "zone.rgw.log"
		usageNS = "usage"
		oid     = "usage.3"
		epoch   = 1790596800
		hour    = 3600
	)
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
		var err error
		p, err = c.Pool(ctx, logPool, usageNS)
		Expect(err).NotTo(HaveOccurred())
	})
	entry := func(owner, payer, bucket string, at uint64, cat string, ops uint64) rgwcls.UsageLogEntry {
		d := rgwcls.UsageData{Ops: ops, SuccessfulOps: ops}
		return rgwcls.UsageLogEntry{
			Owner: owner, Payer: payer, Bucket: bucket, Epoch: at, TotalUsage: d,
			UsageMap: map[string]rgwcls.UsageData{cat: d},
		}
	}
	add := func(ctx context.Context, entries ...rgwcls.UsageLogEntry) {
		GinkgoHelper()
		Expect(writeErr(ctx, p, oid, func(op *radosclient.WriteOp) {
			rgwcls.UsageLogAdd(op, rgwcls.UsageLogInfo{Entries: entries}, denc.Squid)
		})).To(Succeed())
	}
	// buckets returns n owner-keyed entries of user, one per bucket
	// "<prefix>NNNN", in the hour at.
	buckets := func(user, prefix string, at uint64, n int) []rgwcls.UsageLogEntry {
		out := make([]rgwcls.UsageLogEntry, n)
		for i := range n {
			out[i] = entry(user, "", fmt.Sprintf("%s%04d", prefix, i), at, "put_obj", 1)
		}
		return out
	}
	read := func(ctx context.Context, q rgwcls.UsageReadOp) (rgwcls.UsageReadRet, error) {
		r := radosclient.NewReadOp()
		res := rgwcls.UsageLogRead(r, q, denc.Squid)
		if _, err := p.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
			return rgwcls.UsageReadRet{}, err
		}
		return res.Result()
	}
	trim := func(ctx context.Context, t rgwcls.UsageTrimOp) error {
		return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { rgwcls.UsageLogTrim(op, t, denc.Squid) })
	}
	all := func() rgwcls.UsageTrimOp { return rgwcls.UsageTrimOp{EndEpoch: ^uint64(0)} }
	byUser := func(user string) rgwcls.UsageTrimOp {
		t := all()
		t.User = user
		return t
	}
	clearUsage := func(op *radosclient.WriteOp) { rgwcls.UsageLogClear(op) }
	entriesLeft := func() int { return len(c.UsageEntries(logPool, usageNS, oid)) }
	users := func(ret rgwcls.UsageReadRet) []string {
		var out []string
		for _, k := range slices.SortedFunc(maps.Keys(ret.Usage), cmpUserBucket) {
			out = append(out, k.User+"/"+k.Bucket)
		}
		return out
	}

	Describe("user_usage_log_read", func() {
		It("reads a user's records from its by-user keys, keyed by payer or owner and merged per bucket", func(ctx SpecContext) {
			add(ctx,
				entry("alice", "", "plain", epoch, "put_obj", 3),
				entry("alice", "", "plain", epoch+hour, "get_obj", 1),
				entry("alice", "carol", "rp", epoch, "get_obj", 2),
				entry("bob", "", "b", epoch, "put_obj", 1),
			)
			ret, err := read(ctx, rgwcls.UsageReadOp{EndEpoch: ^uint64(0), Owner: "alice"})
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.Truncated).To(BeFalse())
			Expect(ret.NextIter).To(BeEmpty())
			Expect(users(ret)).To(Equal([]string{"alice/plain"}), "carol paid for rp, so it is filed under carol")
			Expect(ret.Usage[rgwcls.UserBucket{User: "alice", Bucket: "plain"}]).To(Equal(rgwcls.UsageLogEntry{
				Owner: "alice", Bucket: "plain", Epoch: epoch,
				TotalUsage: rgwcls.UsageData{Ops: 4, SuccessfulOps: 4},
				UsageMap: map[string]rgwcls.UsageData{
					"put_obj": {Ops: 3, SuccessfulOps: 3},
					"get_obj": {Ops: 1, SuccessfulOps: 1},
				},
			}), "the first hour's owner and epoch, the hours' sums")
			ret, err = read(ctx, rgwcls.UsageReadOp{EndEpoch: ^uint64(0), Owner: "carol"})
			Expect(err).NotTo(HaveOccurred())
			Expect(users(ret)).To(Equal([]string{"carol/rp"}))
			Expect(ret.Usage[rgwcls.UserBucket{User: "carol", Bucket: "rp"}].Owner).To(Equal("alice"))
		})

		It("reads the epochs in [start, end) and the bucket asked for", func(ctx SpecContext) {
			add(ctx,
				entry("alice", "", "plain", epoch, "put_obj", 1),
				entry("alice", "", "plain", epoch+hour, "get_obj", 1),
				entry("alice", "", "plain", epoch+2*hour, "delete_obj", 1),
				entry("alice", "", "other", epoch+hour, "put_obj", 1),
			)
			ret, err := read(ctx, rgwcls.UsageReadOp{StartEpoch: epoch + hour, EndEpoch: epoch + 2*hour, Owner: "alice", Bucket: "plain"})
			Expect(err).NotTo(HaveOccurred())
			Expect(users(ret)).To(Equal([]string{"alice/plain"}))
			Expect(ret.Usage[rgwcls.UserBucket{User: "alice", Bucket: "plain"}].UsageMap).To(HaveKey("get_obj"))
			Expect(ret.Usage[rgwcls.UserBucket{User: "alice", Bucket: "plain"}].UsageMap).To(HaveLen(1))
		})

		It("stops at its window of 1000 keys, truncated, and goes on from the last key it read", func(ctx SpecContext) {
			add(ctx, buckets("alice", "b", epoch, 1001)...)
			ret, err := read(ctx, rgwcls.UsageReadOp{EndEpoch: ^uint64(0), Owner: "alice"})
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.Usage).To(HaveLen(1000), "MAX_ENTRIES when the request asks for none")
			Expect(ret.Truncated).To(BeTrue())
			Expect(ret.NextIter).To(Equal(fmt.Sprintf("alice_%011d_b0999", epoch)))
			ret, err = read(ctx, rgwcls.UsageReadOp{EndEpoch: ^uint64(0), Owner: "alice", Iter: ret.NextIter})
			Expect(err).NotTo(HaveOccurred())
			Expect(users(ret)).To(Equal([]string{"alice/b1000"}))
			Expect(ret.Truncated).To(BeFalse())
			ret, err = read(ctx, rgwcls.UsageReadOp{EndEpoch: ^uint64(0), Owner: "alice", MaxEntries: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.Usage).To(HaveLen(10), "max_entries bounds the window")
			Expect(ret.Truncated).To(BeTrue())
		})

		It("reads every user's records from the by-time keys when no user is named", func(ctx SpecContext) {
			add(ctx,
				entry("alice", "", "plain", epoch, "put_obj", 1),
				entry("alice", "carol", "rp", epoch+hour, "get_obj", 1),
				entry("bob", "", "b", epoch+hour, "put_obj", 1),
			)
			ret, err := read(ctx, rgwcls.UsageReadOp{EndEpoch: ^uint64(0)})
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.Truncated).To(BeFalse(), "the by-user keys sort past the end's by-time key")
			Expect(users(ret)).To(Equal([]string{"alice/plain", "bob/b", "carol/rp"}))
			ret, err = read(ctx, rgwcls.UsageReadOp{StartEpoch: epoch + hour, EndEpoch: ^uint64(0)})
			Expect(err).NotTo(HaveOccurred())
			Expect(users(ret)).To(Equal([]string{"bob/b", "carol/rp"}))
		})

		It("answers ENOENT for an object that does not exist", func(ctx SpecContext) {
			_, err := read(ctx, rgwcls.UsageReadOp{EndEpoch: ^uint64(0), Owner: "alice"})
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})
	})

	Describe("user_usage_log_trim", func() {
		It("removes the records in range and answers ENODATA once none are left", func(ctx SpecContext) {
			add(ctx,
				entry("alice", "", "plain", epoch, "put_obj", 1),
				entry("alice", "", "plain", epoch+hour, "get_obj", 1),
				entry("alice", "", "other", epoch, "put_obj", 1),
				entry("bob", "", "b", epoch, "put_obj", 1),
			)
			Expect(trim(ctx, byUser("alice"))).To(Succeed(), "the first round finds records")
			Expect(c.UsageEntries(logPool, usageNS, oid)).To(ConsistOf(HaveField("Owner", "bob")))
			Expect(trim(ctx, byUser("alice"))).To(MatchError(radosclient.ErrNoData), "the second finds none")
		})

		It("trims every user's records by time when no user is named", func(ctx SpecContext) {
			add(ctx,
				entry("alice", "", "plain", epoch, "put_obj", 1),
				entry("bob", "", "b", epoch+hour, "put_obj", 1),
			)
			Expect(trim(ctx, rgwcls.UsageTrimOp{StartEpoch: epoch + hour, EndEpoch: ^uint64(0)})).To(Succeed())
			Expect(c.UsageEntries(logPool, usageNS, oid)).To(ConsistOf(HaveField("Owner", "alice")))
			Expect(trim(ctx, all())).To(Succeed())
			Expect(entriesLeft()).To(BeZero())
			Expect(c.Object(logPool, usageNS, oid).Omap).To(BeEmpty(), "both keys of each record go")
			Expect(trim(ctx, all())).To(MatchError(radosclient.ErrNoData))
		})

		It("never removes a payer-keyed record, answering 0 every round (tracker #72593)", func(ctx SpecContext) {
			add(ctx, entry("alice", "bob", "plain", epoch, "get_obj", 1))
			for round := range 5 {
				Expect(trim(ctx, byUser("bob"))).To(Succeed(), "round %d", round)
			}
			Expect(entriesLeft()).To(Equal(1))
		})

		It("removes the owner's record of the payer-keyed record's hour and bucket instead", func(ctx SpecContext) {
			add(ctx,
				entry("alice", "bob", "plain", epoch, "get_obj", 1),
				entry("alice", "", "plain", epoch, "put_obj", 1),
			)
			Expect(trim(ctx, byUser("bob"))).To(Succeed())
			Expect(c.UsageEntries(logPool, usageNS, oid)).To(ConsistOf(HaveField("Payer", "bob")), "alice's own record went")
		})

		It("makes no progress with a bucket filter behind 1000 other buckets' records (tracker #58136)", func(ctx SpecContext) {
			add(ctx, buckets("dan", "a", epoch, 1000)...)
			add(ctx, entry("dan", "", "zzz", epoch, "put_obj", 1))
			t := byUser("dan")
			t.Bucket = "zzz"
			for round := range 3 {
				Expect(trim(ctx, t)).To(Succeed(), "round %d", round)
			}
			Expect(entriesLeft()).To(Equal(1001), "nothing removed")
		})

		It("trims a bucket's records when they lie inside the first window", func(ctx SpecContext) {
			add(ctx, entry("dan", "", "a", epoch, "put_obj", 1), entry("dan", "", "b", epoch, "put_obj", 1))
			t := byUser("dan")
			t.Bucket = "b"
			Expect(trim(ctx, t)).To(Succeed())
			Expect(c.UsageEntries(logPool, usageNS, oid)).To(ConsistOf(HaveField("Bucket", "a")))
			Expect(trim(ctx, t)).To(MatchError(radosclient.ErrNoData))
		})

		It("answers ENOENT for an object that does not exist, creating nothing", func(ctx SpecContext) {
			Expect(trim(ctx, all())).To(MatchError(radosclient.ErrNotFound))
			err := writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { op.Exec("rgw", "user_usage_log_trim", []byte{1}) })
			Expect(err).To(MatchError(radosclient.ErrNotFound), "cls_cxx_stat comes before the request's decode")
			Expect(c.Object(logPool, usageNS, oid)).To(BeNil())
		})

		It("refuses a request that does not decode with EINVAL", func(ctx SpecContext) {
			add(ctx, entry("alice", "", "plain", epoch, "put_obj", 1))
			err := writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { op.Exec("rgw", "user_usage_log_trim", []byte{1}) })
			Expect(err).To(haveErrno(syscall.EINVAL))
			Expect(entriesLeft()).To(Equal(1))
		})
	})

	Describe("usage_log_clear", func() {
		It("empties the object's omap", func(ctx SpecContext) {
			add(ctx, entry("alice", "", "plain", epoch, "put_obj", 1), entry("bob", "", "b", epoch, "put_obj", 1))
			Expect(writeErr(ctx, p, oid, clearUsage)).To(Succeed())
			Expect(c.Object(logPool, usageNS, oid).Omap).To(BeEmpty())
		})

		It("answers 0 for an object that does not exist, creating nothing", func(ctx SpecContext) {
			Expect(writeErr(ctx, p, oid, clearUsage)).To(Succeed())
			Expect(c.Object(logPool, usageNS, oid)).To(BeNil())
		})
	})
})

// cmpUserBucket orders rgw_user_bucket keys by user, then bucket.
func cmpUserBucket(a, b rgwcls.UserBucket) int {
	return cmp.Or(strings.Compare(a.User, b.User), strings.Compare(a.Bucket, b.Bucket))
}
