package memstore_test

import (
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("stats, quota, usage and metadata", func() {
	var (
		store *memstore.Store
		clk   *clock
	)
	BeforeEach(func() { store, clk = newStore() })

	It("sums a bucket's objects, rounding each up to 4 KiB as radosgw accounts them", func(ctx SpecContext) {
		rec := mustCreate(ctx, store, "", "b", owner("alice"))
		for key, size := range map[string]int{"one": 1, "page": 4096, "more": 4097, "empty": 0} {
			mustPut(ctx, store, rec, key, strings.Repeat("x", size))
		}
		Expect(store.BucketStats(ctx, rec)).To(Equal(op.Stats{Size: 8194, SizeRounded: 16384, NumObjects: 4}))
		_, err := store.BucketStats(ctx, &op.BucketRecord{})
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "a missing bucket")
	})
	It("sums the owner's buckets", func(ctx SpecContext) {
		a := mustCreate(ctx, store, "", "a", owner("alice"))
		b := mustCreate(ctx, store, "", "b", owner("alice"))
		c := mustCreate(ctx, store, "", "c", owner("bob"))
		mustPut(ctx, store, a, "k", "12")
		mustPut(ctx, store, b, "k", "345")
		mustPut(ctx, store, c, "k", "6789")
		Expect(store.UserStats(ctx, owner("alice"))).To(Equal(op.Stats{Size: 5, SizeRounded: 8192, NumObjects: 2}))
		Expect(store.UserStats(ctx, owner("carol"))).To(Equal(op.Stats{}), "an owner with no buckets")
	})

	Context("CheckQuota", func() {
		var rec *op.BucketRecord
		BeforeEach(func(ctx SpecContext) {
			info := meta.NewUserInfo()
			info.UserID = meta.UserID{ID: "alice"}
			store.AddUser(info)
			rec = mustCreate(ctx, store, "", "b", owner("alice"))
			mustPut(ctx, store, rec, "k1", "0123456789")
			mustPut(ctx, store, rec, "k2", "")
		})
		DescribeTable("refuses a write that would take an enabled bucket quota past a limit",
			func(ctx SpecContext, quota meta.Quota, addBytes, addObjs int64, want error) {
				rec.Info.Quota = quota
				err := store.CheckQuota(ctx, rec, owner("alice"), addBytes, addObjs)
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(want))
				}
			},
			Entry("a disabled quota", meta.Quota{MaxSize: 0, MaxObjects: 0}, int64(1), int64(1), nil),
			Entry("objects up to the limit", meta.Quota{MaxSize: -1, MaxObjects: 3, Enabled: true}, int64(0), int64(1), nil),
			Entry("objects past the limit", meta.Quota{MaxSize: -1, MaxObjects: 2, Enabled: true}, int64(0), int64(1), op.ErrQuotaExceeded),
			Entry("bytes up to the limit, the stored and added sizes each rounded up to 4 KiB",
				meta.Quota{MaxSize: 8192, MaxObjects: -1, Enabled: true}, int64(1), int64(1), nil),
			Entry("bytes past the limit once rounded, though 11 raw bytes would fit",
				meta.Quota{MaxSize: 8191, MaxObjects: -1, Enabled: true}, int64(1), int64(1), op.ErrQuotaExceeded),
			Entry("raw bytes up to the limit under CheckOnRaw",
				meta.Quota{MaxSize: 15, MaxObjects: -1, Enabled: true, CheckOnRaw: true}, int64(5), int64(1), nil),
			Entry("raw bytes past the limit under CheckOnRaw",
				meta.Quota{MaxSize: 15, MaxObjects: -1, Enabled: true, CheckOnRaw: true}, int64(6), int64(1), op.ErrQuotaExceeded),
			Entry("negative limits, which are none", meta.Quota{MaxSize: -1, MaxObjects: -1, Enabled: true}, int64(1<<40), int64(1<<20), nil),
			Entry("negative bytes, which count as none since radosgw's are unsigned: an over-quota bucket stays over",
				meta.Quota{MaxSize: 4095, MaxObjects: -1, Enabled: true}, int64(-4096), int64(0), op.ErrQuotaExceeded),
			Entry("negative objects, which count as none likewise",
				meta.Quota{MaxSize: -1, MaxObjects: 1, Enabled: true}, int64(0), int64(-1), op.ErrQuotaExceeded),
		)
		It("then checks the owning user's quota over all their buckets", func(ctx SpecContext) {
			other := mustCreate(ctx, store, "", "other", owner("alice"))
			mustPut(ctx, store, other, "k", "v")
			u, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
			Expect(err).NotTo(HaveOccurred())
			u.Info.UserQuota = meta.Quota{MaxSize: -1, MaxObjects: 3, Enabled: true}
			Expect(store.PutUser(ctx, u, op.PutUserOptions{})).To(Succeed())
			Expect(store.CheckQuota(ctx, rec, owner("alice"), 0, 1)).To(MatchError(op.ErrQuotaExceeded), "three objects already")
			Expect(store.CheckQuota(ctx, rec, owner("bob"), 0, 1)).To(Succeed(), "an owner with no user record")
		})
	})

	It("logs usage entries and hands out copies of the log", func(ctx SpecContext) {
		e := op.UsageEntry{Owner: owner("alice"), Bucket: "b", Time: start, Category: "put_obj", BytesReceived: 5, Ops: 1, SuccessfulOps: 1}
		store.Log(ctx, e)
		store.Log(ctx, op.UsageEntry{Owner: owner("alice"), Category: "list_buckets", Ops: 1})
		log := store.Usage()
		Expect(log).To(HaveLen(2))
		Expect(log[0]).To(Equal(e), "the first entry, as logged")
		log[0].Owner.User.ID = "mallory"
		Expect(store.Usage()[0]).To(Equal(e), "a copy")
	})

	Context("metadata", func() {
		It("reports a missing key as NotFound", func(ctx SpecContext) {
			_, err := store.Get(ctx, "user", "nobody")
			Expect(err).To(MatchError(op.ErrNotFound), "get")
			Expect(store.Remove(ctx, "user", "nobody")).To(MatchError(op.ErrNotFound), "remove")
		})
		It("puts an entry at version 1 and bumps it on each put", func(ctx SpecContext) {
			data := json.RawMessage(`{"user_id":"alice"}`)
			Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
			e, err := store.Get(ctx, "user", "alice")
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{e.Key, e.Data, e.Version.Ver, e.Mtime}).To(Equal([]any{"alice", data, uint64(1), start}))
			Expect(e.Version.Tag).To(MatchRegexp(tagPattern), "tag")
			clk.t = start.Add(time.Minute)
			Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &e.Version})).To(Succeed())
			again, err := store.Get(ctx, "user", "alice")
			Expect(err).NotTo(HaveOccurred())
			Expect(again.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: e.Version.Tag}), "bumped")
			Expect(again.Mtime).To(Equal(clk.t), "mtime")
			Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &e.Version})).
				To(MatchError(op.ErrConcurrentModification), "stale version")
		})
		It("keeps sections apart and removes an entry", func(ctx SpecContext) {
			Expect(store.Put(ctx, "user", "k", op.MetadataEntry{}, op.PutMetadataOptions{})).To(Succeed())
			Expect(store.Put(ctx, "bucket", "k", op.MetadataEntry{}, op.PutMetadataOptions{})).To(Succeed())
			Expect(store.Remove(ctx, "user", "k")).To(Succeed())
			_, err := store.Get(ctx, "user", "k")
			Expect(err).To(MatchError(op.ErrNotFound), "removed")
			_, err = store.Get(ctx, "bucket", "k")
			Expect(err).NotTo(HaveOccurred(), "the other section's entry")
		})
		It("lists a section's keys sorted, paged after a marker", func(ctx SpecContext) {
			for _, k := range []string{"c", "a", "b"} {
				Expect(store.Put(ctx, "user", k, op.MetadataEntry{}, op.PutMetadataOptions{})).To(Succeed())
			}
			keys, next, more, err := store.List(ctx, "user", "", 2)
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{keys, next, more}).To(Equal([]any{[]string{"a", "b"}, "b", true}), "first page")
			keys, next, more, err = store.List(ctx, "user", next, 2)
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{keys, next, more}).To(Equal([]any{[]string{"c"}, "c", false}), "second page")
			keys, _, more, err = store.List(ctx, "bucket", "", 2)
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{keys, more}).To(Equal([]any{[]string(nil), false}), "an empty section")
		})
	})
})
