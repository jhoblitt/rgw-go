package fakerados_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("RGWClass's index transaction", func() {
	const (
		index = "zone.rgw.buckets.index"
		shard = ".dir.zone.4155.1.0"
	)
	var (
		c   *fakerados.Cluster
		p   radosclient.Pool
		now time.Time
	)
	seedHeader := func(h rgwcls.DirHeader) {
		if c.Object(index, "", shard) == nil {
			c.Put(index, "", shard, nil)
		}
		e := denc.NewEncoder()
		h.Encode(e, denc.Squid)
		c.Object(index, "", shard).OmapHdr = e.Bytes()
	}
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
		now = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
		c.SetClock(func() time.Time { return now })
		var err error
		p, err = c.Pool(ctx, index, "")
		Expect(err).NotTo(HaveOccurred())
		seedHeader(rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{}})
	})
	prepareKey := func(ctx context.Context, key rgwcls.ObjKey, tag string, mod rgwcls.ModifyOp) error {
		return writeErr(ctx, p, shard, func(op *radosclient.WriteOp) {
			op.AssertExists()
			rgwcls.GuardBucketResharding(op, denc.Squid)
			rgwcls.BucketPrepareOp(op, rgwcls.PrepareOp{Op: mod, Key: key, Tag: tag, Locator: "loc"}, denc.Squid)
		})
	}
	prepare := func(ctx context.Context, tag string) error {
		return prepareKey(ctx, rgwcls.ObjKey{Name: "k"}, tag, rgwcls.OpAdd)
	}
	complete := func(ctx context.Context, co rgwcls.CompleteOp) error {
		if co.Key.Name == "" {
			co.Key.Name = "k"
		}
		return writeErr(ctx, p, shard, func(op *radosclient.WriteOp) {
			op.AssertExists()
			rgwcls.GuardBucketResharding(op, denc.Squid)
			rgwcls.BucketCompleteOp(op, co, denc.Squid)
		})
	}
	add := func(tag string, epoch uint64, etag string, size uint64) rgwcls.CompleteOp {
		return rgwcls.CompleteOp{
			Op: rgwcls.OpAdd, Tag: tag, Ver: rgwcls.EntryVer{Pool: 7, Epoch: epoch},
			Meta: rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: size, AccountedSize: size, ETag: etag},
		}
	}
	cancel := func(tag string) rgwcls.CompleteOp {
		return rgwcls.CompleteOp{Op: rgwcls.OpCancel, Tag: tag, Ver: rgwcls.EntryVer{Pool: -1, Epoch: 0}}
	}
	entry := func(key string) (rgwcls.DirEntry, bool) { return c.Entry(index, "", shard, key) }
	header := func() rgwcls.DirHeader { return c.Header(index, "", shard) }

	It("names the emulated methods the class registers as writes", func() {
		Expect(fakerados.RGWWriteMethods).To(ConsistOf("bucket_init_index", "bucket_prepare_op", "bucket_complete_op", "set_bucket_resharding"),
			"CLS_METHOD_WR, cls_rgw.cc:4691, :4697-4698 and :4752-4753 at v19.2.6")
	})

	It("reads the reshard status without the WR flag: a read op calling it on a missing shard is ENOENT", func(ctx SpecContext) {
		for _, method := range []string{"guard_bucket_resharding", "get_bucket_resharding"} {
			op := radosclient.NewReadOp()
			if method == "guard_bucket_resharding" {
				rgwcls.GuardBucketResharding(op, denc.Squid)
			} else {
				rgwcls.GetBucketResharding(op, denc.Squid)
			}
			_, err := p.Read(ctx, "absent", op, radosclient.OpFlagNone)
			Expect(err).To(MatchError(radosclient.ErrNotFound), "%s: a read op finds no object to run on (cls_rgw.cc:4756-4759)", method)
			Expect(c.Object(index, "", "absent")).To(BeNil(), method)
		}
	})

	It("sets the header's reshard status with set_bucket_resharding, counting the write", func(ctx SpecContext) {
		seedHeader(rgwcls.DirHeader{Ver: 4})
		Expect(writeErr(ctx, p, shard, func(op *radosclient.WriteOp) {
			rgwcls.SetBucketResharding(op, rgwcls.InstanceEntry{ReshardStatus: rgwcls.ReshardInProgress}, denc.Squid)
		})).To(Succeed())
		Expect(header()).To(Equal(rgwcls.DirHeader{Ver: 5, NewInstance: rgwcls.InstanceEntry{ReshardStatus: rgwcls.ReshardInProgress}}))
	})

	It("prepares an unset entry pending under the tag, then completes an ADD into the entry and the Main stats", func(ctx SpecContext) {
		Expect(prepare(ctx, "t")).To(Succeed())
		en, ok := entry("k")
		Expect(ok).To(BeTrue())
		Expect(en).To(Equal(rgwcls.DirEntry{
			Key: rgwcls.ObjKey{Name: "k"}, Ver: rgwcls.EntryVer{Pool: -1}, Locator: "loc",
			PendingMap: []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: now, Op: uint8(rgwcls.OpAdd)}}},
		}), "cls_rgw.cc:840-865")
		Expect(header().Ver).To(BeZero(), "prepare leaves the header alone")

		Expect(complete(ctx, add("t", 42, "e", 5))).To(Succeed())
		en, ok = entry("k")
		Expect(ok).To(BeTrue())
		Expect(en).To(Equal(rgwcls.DirEntry{
			Key: rgwcls.ObjKey{Name: "k"}, Ver: rgwcls.EntryVer{Pool: 7, Epoch: 42}, Exists: true, Locator: "loc", Tag: "t",
			Meta: rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, ETag: "e"},
		}))
		h := header()
		Expect(h.Ver).To(BeEquivalentTo(1), "write_bucket_header counts the write")
		Expect(h.Stats).To(Equal(map[uint8]rgwcls.CategoryStats{
			rgwcls.CategoryMain: {NumEntries: 1, TotalSize: 5, TotalSizeRounded: 4096, ActualSize: 5},
		}))
	})

	It("stamps the entry with the header version it read and clears every flag but the versioned one", func(ctx SpecContext) {
		seedHeader(rgwcls.DirHeader{Ver: 9})
		Expect(prepare(ctx, "t")).To(Succeed())
		Expect(complete(ctx, add("t", 1, "e", 1))).To(Succeed())
		en, _ := entry("k")
		Expect(en.IndexVer).To(BeEquivalentTo(9))
		Expect(en.Flags).To(BeZero())
		Expect(header().Ver).To(BeEquivalentTo(10))
	})

	It("refuses a completion whose tag is not pending with EINVAL, changing nothing", func(ctx SpecContext) {
		Expect(prepare(ctx, "t")).To(Succeed())
		err := complete(ctx, add("other", 1, "e", 1))
		Expect(err).To(MatchError(radosclient.ErrInvalid), "cls_rgw.cc:1069-1078")
		en, _ := entry("k")
		Expect(en.PendingMap).To(HaveLen(1))
		Expect(header().Ver).To(BeZero())
	})

	It("turns a completion at an older epoch of the same pool into a cancel that keeps its epoch (tracker #80894)", func(ctx SpecContext) {
		Expect(prepare(ctx, "a")).To(Succeed())
		Expect(prepare(ctx, "b")).To(Succeed())
		Expect(complete(ctx, add("a", 10, "ten", 10))).To(Succeed())
		Expect(complete(ctx, add("b", 5, "five", 5))).To(Succeed())
		en, _ := entry("k")
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: 7, Epoch: 5}), "cls_rgw.cc:1094 copies the version before the cancel writes back")
		Expect(en.Meta.ETag).To(Equal("ten"))
		Expect(en.PendingMap).To(BeEmpty())
		Expect(header().Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(1))
	})

	It("resets an entry's version with radosgw's -1:0 cancel, so a later completion applies at any epoch", func(ctx SpecContext) {
		for _, tag := range []string{"a", "b", "c"} {
			Expect(prepare(ctx, tag)).To(Succeed())
		}
		Expect(complete(ctx, add("a", 10, "ten", 10))).To(Succeed())
		Expect(complete(ctx, cancel("b"))).To(Succeed())
		en, _ := entry("k")
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}))
		Expect(en.Meta.ETag).To(Equal("ten"))
		Expect(complete(ctx, add("c", 3, "three", 3))).To(Succeed())
		en, _ = entry("k")
		Expect(en.Meta.ETag).To(Equal("three"))
		Expect(header().Stats[rgwcls.CategoryMain]).To(Equal(rgwcls.CategoryStats{NumEntries: 1, TotalSize: 3, TotalSizeRounded: 4096, ActualSize: 3}))
	})

	It("removes the entry a cancel leaves neither existing nor pending", func(ctx SpecContext) {
		Expect(prepare(ctx, "t")).To(Succeed())
		Expect(complete(ctx, cancel("t"))).To(Succeed())
		_, ok := entry("k")
		Expect(ok).To(BeFalse(), "cls_rgw.cc:1097-1108")
		Expect(header().Ver).To(BeEquivalentTo(1), "the header is written whatever the op")
	})

	It("keeps a deleted entry another tag still has pending, as not existing, and unaccounts it", func(ctx SpecContext) {
		Expect(prepare(ctx, "a")).To(Succeed())
		Expect(complete(ctx, add("a", 1, "e", 5))).To(Succeed())
		Expect(prepare(ctx, "b")).To(Succeed())
		Expect(prepare(ctx, "c")).To(Succeed())
		mtime := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		Expect(complete(ctx, rgwcls.CompleteOp{
			Op: rgwcls.OpDel, Tag: "b", Ver: rgwcls.EntryVer{Pool: 7, Epoch: 2},
			Meta: rgwcls.DirEntryMeta{Category: rgwcls.CategoryNone, Mtime: mtime},
		})).To(Succeed())
		en, ok := entry("k")
		Expect(ok).To(BeTrue())
		Expect(en.Exists).To(BeFalse())
		Expect(en.Meta).To(Equal(rgwcls.DirEntryMeta{Category: rgwcls.CategoryNone, Mtime: mtime}))
		Expect(en.PendingMap).To(HaveLen(1))
		Expect(en.PendingMap[0].Tag).To(Equal("c"))
		Expect(header().Stats[rgwcls.CategoryMain]).To(Equal(rgwcls.CategoryStats{}))
	})

	It("removes a deleted entry nothing else has pending", func(ctx SpecContext) {
		Expect(prepare(ctx, "a")).To(Succeed())
		Expect(complete(ctx, add("a", 1, "e", 5))).To(Succeed())
		Expect(prepare(ctx, "b")).To(Succeed())
		Expect(complete(ctx, rgwcls.CompleteOp{Op: rgwcls.OpDel, Tag: "b", Ver: rgwcls.EntryVer{Pool: 7, Epoch: 2}})).To(Succeed())
		_, ok := entry("k")
		Expect(ok).To(BeFalse())
	})

	It("removes and unaccounts the entries a completion lists in remove_objs", func(ctx SpecContext) {
		Expect(prepareKey(ctx, rgwcls.ObjKey{Name: "old"}, "o", rgwcls.OpAdd)).To(Succeed())
		Expect(complete(ctx, rgwcls.CompleteOp{
			Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: "old"}, Tag: "o", Ver: rgwcls.EntryVer{Pool: 7, Epoch: 1},
			Meta: rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 3, AccountedSize: 3},
		})).To(Succeed())
		Expect(prepare(ctx, "t")).To(Succeed())
		co := add("t", 2, "e", 5)
		co.RemoveObjs = []rgwcls.ObjKey{{Name: "old"}, {Name: "missing"}}
		Expect(complete(ctx, co)).To(Succeed(), "a key that is not there is skipped")
		_, ok := entry("old")
		Expect(ok).To(BeFalse())
		Expect(header().Stats[rgwcls.CategoryMain]).To(Equal(rgwcls.CategoryStats{NumEntries: 1, TotalSize: 5, TotalSizeRounded: 4096, ActualSize: 5}))
	})

	It("refuses a prepare without a tag with EINVAL, and a versioned key, which it does not emulate", func(ctx SpecContext) {
		Expect(prepare(ctx, "")).To(MatchError(radosclient.ErrInvalid), "cls_rgw.cc:826-829")
		err := prepareKey(ctx, rgwcls.ObjKey{Name: "k", Instance: "v1"}, "t", rgwcls.OpAdd)
		Expect(err).To(MatchError(radosclient.ErrNotSupported))
	})

	It("refuses a completion with EINVAL when the header does not decode", func(ctx SpecContext) {
		Expect(prepare(ctx, "t")).To(Succeed())
		c.Object(index, "", shard).OmapHdr = []byte{7}
		err := writeErr(ctx, p, shard, func(op *radosclient.WriteOp) {
			rgwcls.BucketCompleteOp(op, rgwcls.CompleteOp{Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: "k"}, Tag: "t"}, denc.Squid)
		})
		Expect(err).To(MatchError(radosclient.ErrInvalid), "cls_rgw.cc:1059-1064")
	})

	DescribeTable("guards an index write on the shard header's reshard status, as Squid's class does",
		func(ctx SpecContext, status uint8, refused bool) {
			seedHeader(rgwcls.DirHeader{NewInstance: rgwcls.InstanceEntry{ReshardStatus: status}})
			err := prepare(ctx, "t")
			if !refused {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(radosclient.ErrBusyResharding), "the guard returns ret_err verbatim, cls_rgw.cc:4597-4599")
			_, ok := entry("k")
			Expect(ok).To(BeFalse(), "the refused op writes nothing")
		},
		Entry("not resharding", rgwcls.ReshardNone, false),
		Entry("in progress", rgwcls.ReshardInProgress, true),
		Entry("done, which Squid's class refuses", rgwcls.ReshardDone, true),
	)

	It("answers the shard header's reshard entry from get_bucket_resharding on a read op", func(ctx SpecContext) {
		seedHeader(rgwcls.DirHeader{Ver: 3, NewInstance: rgwcls.InstanceEntry{ReshardStatus: rgwcls.ReshardInProgress}})
		op := radosclient.NewReadOp()
		res := rgwcls.GetBucketResharding(op, denc.Squid)
		_, err := p.Read(ctx, shard, op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Result()).To(Equal(rgwcls.InstanceEntry{ReshardStatus: rgwcls.ReshardInProgress}))
		Expect(header().Ver).To(BeEquivalentTo(3))

		op = radosclient.NewReadOp()
		rgwcls.GetBucketResharding(op, denc.Squid)
		_, err = p.Read(ctx, "absent", op, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})

	It("reports a key or header it has not got as absent and zero", func() {
		_, ok := c.Entry(index, "", "absent", "k")
		Expect(ok).To(BeFalse())
		Expect(c.Header(index, "", "absent")).To(Equal(rgwcls.DirHeader{}))
	})
})
