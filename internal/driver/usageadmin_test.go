package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// usageTimeout bounds a spec that pages the usage log, so a walk that never
// ends fails the spec rather than the suite.
const usageTimeout = 20 * time.Second

var _ = Describe("the usage log's read and trim", func() {
	const (
		logPool = "ceph-objectstore.rgw.log"
		usageNS = "usage"
	)
	hour := func(h int) uint64 {
		return uint64(time.Date(2026, 9, 28, h, 0, 0, 0, time.UTC).Unix())
	}
	var (
		c     *fakerados.Cluster
		store *driver.Store
		p     radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClassMethods("rgw", fakerados.RGWClass(), fakerados.RGWMethods, fakerados.RGWWriteMethods)
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		store, err = driver.Open(ctx, c, conf(map[string]string{
			"rgw_zone": "ceph-objectstore", "rgw_usage_max_shards": "4", "rgw_usage_max_user_shards": "2",
		}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(store.Close)
		p, err = c.Pool(ctx, logPool, usageNS)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(p.Close)
	})
	entry := func(owner, payer, bucket string, at uint64, cat string, ops uint64) rgwcls.UsageLogEntry {
		d := rgwcls.UsageData{Ops: ops, SuccessfulOps: ops}
		return rgwcls.UsageLogEntry{
			Owner: owner, Payer: payer, Bucket: bucket, Epoch: at, TotalUsage: d,
			UsageMap: map[string]rgwcls.UsageData{cat: d},
		}
	}
	// addAt adds entries to user's usage object at index through
	// user_usage_log_add, as a flush does.
	addAt := func(ctx context.Context, user string, index uint32, entries ...rgwcls.UsageLogEntry) {
		GinkgoHelper()
		w := radosclient.NewWriteOp()
		rgwcls.UsageLogAdd(w, rgwcls.UsageLogInfo{Entries: entries}, denc.Squid)
		_, err := p.Write(ctx, driver.UsageOIDForTest(store, user, index), w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
	}
	logUsage := func(ctx context.Context, user, bucket string, at uint64, cat string, ops uint64) {
		GinkgoHelper()
		addAt(ctx, user, 0, entry(user, "", bucket, at, cat, ops))
	}
	logUsagePayer := func(ctx context.Context, owner, payer, bucket string, at uint64, cat string, ops uint64) {
		GinkgoHelper()
		addAt(ctx, payer, 0, entry(owner, payer, bucket, at, cat, ops))
	}
	// logBuckets logs one put_obj of user in each of n buckets
	// "<prefix>NNNN" in the hour at.
	logBuckets := func(ctx context.Context, user, prefix string, at uint64, n int) {
		GinkgoHelper()
		entries := make([]rgwcls.UsageLogEntry, n)
		for i := range n {
			entries[i] = entry(user, "", fmt.Sprintf("%s%04d", prefix, i), at, "put_obj", 1)
		}
		addAt(ctx, user, 0, entries...)
	}
	readAll := func(ctx context.Context, user, bucket string) []op.UsageRecord {
		GinkgoHelper()
		var all []op.UsageRecord
		var it op.UsageIter
		for range 100 {
			page, more, err := store.ReadUsage(ctx, user, bucket, 0, math.MaxUint64, 1000, &it)
			Expect(err).NotTo(HaveOccurred())
			all = append(all, page...)
			if !more {
				return all
			}
		}
		Fail("the read never ended")
		return nil
	}
	// trims counts the user_usage_log_trim calls sent to user's usage object
	// at index.
	trims := func(user string, index uint32) int {
		n := 0
		for _, w := range c.WritesTo(logPool, usageNS, driver.UsageOIDForTest(store, user, index)) {
			for _, st := range w.Steps() {
				if e, ok := st.(*radosclient.ExecStep); ok && e.Method == "user_usage_log_trim" {
					n++
				}
			}
		}
		return n
	}

	Describe("ReadUsage", func() {
		It("reads a user's usage across its shards and pages", func(ctx SpecContext) {
			logUsage(ctx, "alice", "plain", hour(1), "put_obj", 3)
			logUsage(ctx, "alice", "plain", hour(2), "get_obj", 1)
			addAt(ctx, "alice", 1, entry("alice", "", "other", hour(1), "put_obj", 1))
			logUsage(ctx, "bob", "b", hour(1), "put_obj", 1)
			var it op.UsageIter
			recs, truncated, err := store.ReadUsage(ctx, "alice", "", 0, math.MaxUint64, 1000, &it)
			Expect(err).NotTo(HaveOccurred())
			Expect(truncated).To(BeFalse())
			Expect(recs).To(HaveLen(2), "aggregated per (user, bucket), over both of alice's shards")
			Expect(recs[0].Bucket).To(Equal("other"))
			Expect(recs[1]).To(Equal(op.UsageRecord{
				User: "alice", Owner: "alice", Bucket: "plain", Epoch: hour(1),
				Total: op.UsageData{Ops: 4, SuccessfulOps: 4},
				Categories: map[string]op.UsageData{
					"put_obj": {Ops: 3, SuccessfulOps: 3},
					"get_obj": {Ops: 1, SuccessfulOps: 1},
				},
			}))
			Expect(it).To(Equal(op.UsageIter{Index: 2}), "the walk went round to the first shard")
			recs, _, err = store.ReadUsage(ctx, "", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(HaveLen(3), "every shard")
			recs, _, err = store.ReadUsage(ctx, "alice", "plain", hour(2), hour(3), 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(HaveLen(1))
			Expect(recs[0].Categories).To(Equal(map[string]op.UsageData{"get_obj": {Ops: 1, SuccessfulOps: 1}}))
			logBuckets(ctx, "carol", "b", hour(1), 2500)
			all := readAll(ctx, "carol", "")
			Expect(all).To(HaveLen(2500))
			Expect(all[2499].Bucket).To(Equal("b2499"))
		}, SpecTimeout(usageTimeout))

		It("merges a bucket's records from two shards, keeping the first shard's epoch and owner", func(ctx SpecContext) {
			logUsage(ctx, "alice", "plain", hour(2), "put_obj", 1)
			addAt(ctx, "alice", 1, entry("bob", "alice", "plain", hour(1), "get_obj", 2))
			recs, _, err := store.ReadUsage(ctx, "alice", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(ConsistOf(And(
				HaveField("Owner", "alice"), HaveField("Epoch", hour(2)),
				HaveField("Total", op.UsageData{Ops: 3, SuccessfulOps: 3}),
			)), "rgw_usage_log_entry::aggregate on read_usage's page")
		})

		It("files a requester-pays record under its payer and keeps its owner", func(ctx SpecContext) {
			logUsagePayer(ctx, "alice", "bob", "plain", hour(1), "get_obj", 1)
			recs, _, err := store.ReadUsage(ctx, "bob", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(ConsistOf(And(HaveField("User", "bob"), HaveField("Owner", "alice"), HaveField("Payer", "bob"))))
		})

		It("counts a page by records and goes on from the class's iterator", func(ctx SpecContext) {
			logBuckets(ctx, "carol", "b", hour(1), 5)
			var it op.UsageIter
			recs, truncated, err := store.ReadUsage(ctx, "carol", "", 0, math.MaxUint64, 2, &it)
			Expect(err).NotTo(HaveOccurred())
			Expect(truncated).To(BeTrue())
			Expect(recs).To(HaveLen(2))
			Expect(it).To(Equal(op.UsageIter{ReadIter: fmt.Sprintf("carol_%011d_b0001", hour(1))}))
			recs, truncated, err = store.ReadUsage(ctx, "carol", "", 0, math.MaxUint64, 2, &it)
			Expect(err).NotTo(HaveOccurred())
			Expect(truncated).To(BeTrue())
			Expect([]string{recs[0].Bucket, recs[1].Bucket}).To(Equal([]string{"b0002", "b0003"}))
		})

		It("ends the listing when a shard fills the page exactly without truncation, as radosgw does", func(ctx SpecContext) {
			logBuckets(ctx, "zed", "b", hour(1), 2)
			addAt(ctx, "zed", 1, entry("zed", "", "later", hour(1), "put_obj", 1))
			var it op.UsageIter
			recs, truncated, err := store.ReadUsage(ctx, "zed", "", 0, math.MaxUint64, 2, &it)
			Expect(err).NotTo(HaveOccurred())
			Expect(recs).To(HaveLen(2))
			Expect(truncated).To(BeFalse(), "read_usage stops on num reaching 0 before reading the next shard")
			Expect(it.Index).To(Equal(uint32(1)))
		})

		It("reads nothing, untruncated, where no usage object exists", func(ctx SpecContext) {
			recs, truncated, err := store.ReadUsage(ctx, "nobody", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
			Expect(err).NotTo(HaveOccurred())
			Expect(truncated).To(BeFalse())
			Expect(recs).To(BeEmpty())
		})

		It("fails on a read failure other than ENOENT", func(ctx SpecContext) {
			logUsage(ctx, "alice", "plain", hour(1), "put_obj", 1)
			c.FailNextRead(logPool, usageNS, driver.UsageOIDForTest(store, "alice", 0), syscall.EIO)
			_, _, err := store.ReadUsage(ctx, "alice", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("TrimUsage", func() {
		It("bases a shard's round cap on ceil(n/1000)+1", func() {
			Expect([]int{
				driver.UsageTrimRoundsForTest(0), driver.UsageTrimRoundsForTest(1), driver.UsageTrimRoundsForTest(1000),
				driver.UsageTrimRoundsForTest(1001), driver.UsageTrimRoundsForTest(2500),
			}).To(Equal([]int{1, 2, 2, 3, 4}))
		})

		It("trims until ENODATA", func(ctx SpecContext) {
			logBuckets(ctx, "carol", "b", hour(1), 2500)
			logUsage(ctx, "dave", "d", hour(1), "put_obj", 1)
			Expect(store.TrimUsage(ctx, "carol", "", 0, math.MaxUint64)).To(Succeed())
			Expect(readAll(ctx, "carol", "")).To(BeEmpty())
			Expect(trims("carol", 0)).To(Equal(4), "three rounds that trim and one ENODATA")
			Expect(trims("carol", 1)).To(Equal(1), "a shard without a usage object takes one round, ENOENT")
			Expect(readAll(ctx, "dave", "")).To(HaveLen(1), "only the user named")
		}, SpecTimeout(usageTimeout))

		// interleave logs one put_obj of eve in each of the buckets in each
		// of 900 hours, the keys of an hour's buckets next to one another.
		interleave := func(ctx context.Context, buckets ...rgwcls.UsageLogEntry) {
			GinkgoHelper()
			var entries []rgwcls.UsageLogEntry
			for h := range 900 {
				for _, b := range buckets {
					b.Epoch = hour(h)
					entries = append(entries, b)
				}
			}
			addAt(ctx, "eve", 0, entries...)
		}

		It("goes on while rounds remove fewer records, as skipped records pile up at the window's front", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			interleave(ctx, entry("eve", "", "a", 0, "put_obj", 1), entry("eve", "", "b", 0, "put_obj", 1))
			Expect(store.TrimUsage(ctx, "eve", "b", 0, math.MaxUint64)).To(Succeed())
			Expect(readAll(ctx, "eve", "b")).To(BeEmpty(), "every b record trimmed")
			Expect(readAll(ctx, "eve", "a")).To(ConsistOf(HaveField("Total.Ops", uint64(900))), "every a record kept")
			Expect(trims("eve", 0)).To(Equal(5), "rounds removing 500, 250, 125 and 25, then ENODATA, as radosgw's loop")
			Expect(logs.String()).NotTo(ContainSubstring("usage trim stopped"))
		}, SpecTimeout(usageTimeout))

		It("trims a payer's own records before it gives up on the records it paid for (tracker #72593)", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			interleave(ctx, entry("eve", "", "own", 0, "put_obj", 1), entry("alice", "eve", "rp", 0, "get_obj", 1))
			Expect(store.TrimUsage(ctx, "eve", "", 0, math.MaxUint64)).To(Succeed())
			Expect(readAll(ctx, "eve", "")).To(ConsistOf(And(HaveField("Bucket", "rp"), HaveField("Total.Ops", uint64(900)))),
				"every record eve owns trimmed, every payer-keyed one left")
			Expect(bytes.Count(logs.Bytes(), []byte("a round removed nothing"))).To(Equal(1))
			Expect(logs.String()).To(ContainSubstring("72593"))
		}, SpecTimeout(usageTimeout))

		It("gives up on payer-keyed records once a round removes nothing, with one warning (tracker #72593)", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			logUsagePayer(ctx, "alice", "bob", "plain", hour(1), "get_obj", 1)
			Expect(store.TrimUsage(ctx, "bob", "", 0, math.MaxUint64)).To(Succeed())
			Expect(trims("bob", 0)).To(Equal(1))
			Expect(logs.String()).To(ContainSubstring("72593"))
			Expect(logs.String()).To(ContainSubstring(driver.UsageOIDForTest(store, "bob", 0)))
			Expect(bytes.Count(logs.Bytes(), []byte("usage trim stopped"))).To(Equal(1))
			Expect(readAll(ctx, "bob", "")).To(HaveLen(1), "still there, as on Squid and Tentacle")
		})

		It("gives up on a bucket filter stuck behind other records (tracker #58136)", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			logBuckets(ctx, "dan", "a", hour(1), 1000)
			logUsage(ctx, "dan", "zzz", hour(1), "put_obj", 1)
			Expect(store.TrimUsage(ctx, "dan", "zzz", 0, math.MaxUint64)).To(Succeed())
			Expect(readAll(ctx, "dan", "zzz")).To(HaveLen(1), "not trimmed by the class on Squid and Tentacle")
			Expect(trims("dan", 0)).To(Equal(1), "the first round removed nothing")
			Expect(logs.String()).To(ContainSubstring("58136"))
		}, SpecTimeout(usageTimeout))

		It("stops at its round cap while rounds still remove records", func(ctx SpecContext) {
			var logs bytes.Buffer
			DeferCleanup(driver.CaptureLog(&logs))
			DeferCleanup(driver.SetUsageTrimCapFactorForTest(1))
			interleave(ctx, entry("eve", "", "a", 0, "put_obj", 1), entry("eve", "", "b", 0, "put_obj", 1))
			Expect(store.TrimUsage(ctx, "eve", "b", 0, math.MaxUint64)).To(Succeed())
			Expect(trims("eve", 0)).To(Equal(driver.UsageTrimRoundsForTest(1001)), "the first read counts 1001")
			Expect(readAll(ctx, "eve", "b")).To(ConsistOf(HaveField("Total.Ops", uint64(25))), "500, 250 and 125 trimmed")
			Expect(bytes.Count(logs.Bytes(), []byte("usage trim stopped at its round cap"))).To(Equal(1))
			Expect(logs.String()).NotTo(ContainSubstring("72593"))
		}, SpecTimeout(usageTimeout))

		It("trims a bucket's records and every shard's without a user", func(ctx SpecContext) {
			logUsage(ctx, "alice", "plain", hour(1), "put_obj", 1)
			logUsage(ctx, "alice", "other", hour(1), "put_obj", 1)
			logUsage(ctx, "bob", "b", hour(2), "put_obj", 1)
			Expect(store.TrimUsage(ctx, "alice", "plain", 0, math.MaxUint64)).To(Succeed())
			Expect(readAll(ctx, "", "")).To(ConsistOf(HaveField("Bucket", "other"), HaveField("Bucket", "b")))
			Expect(store.TrimUsage(ctx, "", "", hour(2), math.MaxUint64)).To(Succeed())
			Expect(readAll(ctx, "", "")).To(ConsistOf(HaveField("Bucket", "other")))
			Expect(store.TrimUsage(ctx, "", "", 0, math.MaxUint64)).To(Succeed())
			Expect(readAll(ctx, "", "")).To(BeEmpty())
		})

		It("fails on a trim failure other than ENODATA and ENOENT", func(ctx SpecContext) {
			logUsage(ctx, "alice", "plain", hour(1), "put_obj", 1)
			c.FailNextWrite(logPool, usageNS, driver.UsageOIDForTest(store, "alice", 0), syscall.EIO)
			Expect(store.TrimUsage(ctx, "alice", "", 0, math.MaxUint64)).NotTo(Succeed())
			Expect(readAll(ctx, "alice", "")).To(HaveLen(1))
		})

		It("stops when its context ends", func(ctx SpecContext) {
			logUsage(ctx, "alice", "plain", hour(1), "put_obj", 1)
			done, cancel := context.WithCancel(ctx)
			cancel()
			Expect(store.TrimUsage(done, "alice", "", 0, math.MaxUint64)).To(MatchError(context.Canceled))
			_, _, err := store.ReadUsage(done, "alice", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
			Expect(err).To(MatchError(context.Canceled))
		})
	})
})
