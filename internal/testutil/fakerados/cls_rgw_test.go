package fakerados_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
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
		Expect(fakerados.RGWWriteMethods).To(ConsistOf("bucket_init_index", "bucket_prepare_op", "bucket_complete_op", "set_bucket_resharding",
			"mp_upload_part_info_update", "obj_remove", "obj_store_pg_ver", "gc_set_entry",
			"user_usage_log_add", "dir_suggest_changes"),
			"CLS_METHOD_WR, cls_rgw.cc:4691, :4697-4698, :4705-4706, :4716, :4722, :4728, :4743 and :4752-4753 at v19.2.6")
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

var _ = Describe("RGWClass's mp_upload_part_info_update", func() {
	const (
		extra   = "zone.rgw.buckets.non-ec"
		metaOID = "zone.4155.1__multipart_k.2~id.meta"
		key     = "part.00000001"
	)
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
		var err error
		p, err = c.Pool(ctx, extra, "")
		Expect(err).NotTo(HaveOccurred())
		c.Put(extra, "", metaOID, nil)
	})
	part := func(prefix string, past ...string) meta.UploadPartInfo {
		target := meta.Obj{Bucket: meta.BucketID{Name: "b", Marker: "zone.4155.1", ID: "zone.4155.1"}, Key: meta.ObjKey{Name: "k"}}
		m := meta.NewPartManifest(target, meta.PlacementRule{}, meta.PlacementRule{Name: "default-placement"}, prefix, 1, 4<<20)
		m.SetObjSize(5)
		return meta.UploadPartInfo{
			Num: 1, Size: 5, ETag: "e", Modified: time.Date(2026, 10, 4, 1, 2, 3, 0, time.UTC),
			Manifest: m, Compression: meta.NewCompressionInfo(), AccountedSize: 5, PastPrefixes: past,
		}
	}
	encodeAt := func(info meta.UploadPartInfo, r denc.Release) []byte {
		e := denc.NewEncoder()
		info.Encode(e, r)
		return e.Bytes()
	}
	update := func(ctx context.Context, info meta.UploadPartInfo, r denc.Release) error {
		return writeErr(ctx, p, metaOID, func(op *radosclient.WriteOp) {
			op.AssertExists()
			rgwcls.MPUploadPartInfoUpdate(op, key, encodeAt(info, r), r)
		})
	}
	stored := func() meta.UploadPartInfo {
		GinkgoHelper()
		b, ok := c.Object(extra, "", metaOID).Omap[key]
		Expect(ok).To(BeTrue(), "no part info under %s", key)
		d := denc.NewDecoder(b)
		info := meta.DecodeUploadPartInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info
	}

	It("stores the part under its key, re-encoded at Squid as the emulated class's OSD does", func(ctx SpecContext) {
		info := part("k.2~id")
		Expect(update(ctx, info, denc.Tentacle)).To(Succeed())
		Expect(c.Object(extra, "", metaOID).Omap[key]).To(Equal(encodeAt(info, denc.Squid)))
	})

	It("carries the stored part's prefix and past prefixes into the new part's, as a set", func(ctx SpecContext) {
		Expect(update(ctx, part("k.2~id"), denc.Squid)).To(Succeed())
		Expect(update(ctx, part("k.RAND1"), denc.Squid)).To(Succeed())
		Expect(stored().PastPrefixes).To(Equal([]string{"k.2~id"}), "cls_rgw.cc:4379-4383 at v19.2.6")
		Expect(update(ctx, part("k.RAND2", "k.2~id", "k.other"), denc.Squid)).To(Succeed())
		Expect(stored().PastPrefixes).To(Equal([]string{"k.2~id", "k.RAND1", "k.other"}))
		Expect(stored().Manifest.Prefix).To(Equal("k.RAND2"))
	})

	It("refuses with EEXIST a part whose prefix is a past one, storing nothing", func(ctx SpecContext) {
		Expect(update(ctx, part("k.2~id"), denc.Squid)).To(Succeed())
		Expect(update(ctx, part("k.RAND1"), denc.Squid)).To(Succeed())
		before := c.Object(extra, "", metaOID).Omap[key]
		Expect(update(ctx, part("k.2~id"), denc.Squid)).To(MatchError(radosclient.ErrExists), "cls_rgw.cc:4385-4394 at v19.2.6")
		Expect(c.Object(extra, "", metaOID).Omap[key]).To(Equal(before))
	})

	It("refuses a part whose own past prefixes hold its prefix, with nothing stored", func(ctx SpecContext) {
		Expect(update(ctx, part("k.2~id", "k.2~id"), denc.Squid)).To(MatchError(radosclient.ErrExists))
		Expect(c.Object(extra, "", metaOID).Omap).NotTo(HaveKey(key))
	})

	It("adds no prefix for a stored part whose manifest is empty, and adds an empty one for a manifest with rules", func(ctx SpecContext) {
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 2)
		e.U32(1)
		e.U64(5)
		e.String("e")
		e.Time(time.Time{})
		e.EndStruct(f)
		c.Object(extra, "", metaOID).Omap[key] = e.Bytes()
		Expect(update(ctx, part(""), denc.Squid)).To(Succeed(), "an empty manifest's prefix is not carried")
		Expect(stored().PastPrefixes).To(BeEmpty())
		Expect(update(ctx, part(""), denc.Squid)).To(MatchError(radosclient.ErrExists), "a manifest with rules carries its prefix, empty or not")
	})

	It("answers EINVAL for a request that does not decode and EIO for a stored part that does not", func(ctx SpecContext) {
		err := writeErr(ctx, p, metaOID, func(op *radosclient.WriteOp) {
			op.Exec("rgw", "mp_upload_part_info_update", []byte{1, 1, 9, 0, 0, 0})
		})
		Expect(err).To(MatchError(radosclient.ErrInvalid))
		c.Object(extra, "", metaOID).Omap[key] = []byte{5}
		Expect(update(ctx, part("k.2~id"), denc.Squid)).To(haveErrno(syscall.EIO), "read_omap_entry, cls_rgw.cc:924-930 at v19.2.6")
	})

	It("creates a missing meta object as cls_cxx_map_set_val does, which radosgw's assert_exists prevents", func(ctx SpecContext) {
		const gone = "zone.4155.1__multipart_k.2~gone.meta"
		err := writeErr(ctx, p, gone, func(op *radosclient.WriteOp) {
			op.AssertExists()
			rgwcls.MPUploadPartInfoUpdate(op, key, encodeAt(part("k.2~gone"), denc.Squid), denc.Squid)
		})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		Expect(c.Object(extra, "", gone)).To(BeNil())
		Expect(writeErr(ctx, p, gone, func(op *radosclient.WriteOp) {
			rgwcls.MPUploadPartInfoUpdate(op, key, encodeAt(part("k.2~gone"), denc.Squid), denc.Squid)
		})).To(Succeed())
		Expect(c.Object(extra, "", gone).Omap).To(HaveKey(key))
	})
})

var _ = Describe("RGWClass on a head object", func() {
	const data = "zone.rgw.buckets.data"
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
		var err error
		p, err = c.Pool(ctx, data, "")
		Expect(err).NotTo(HaveOccurred())
		c.Put(data, "", "head", []byte("old data"))
		obj := c.Object(data, "", "head")
		obj.Xattrs["user.rgw.idtag"] = []byte("old\x00")
		obj.Xattrs["user.rgw.olh.ver"] = []byte("3")
		obj.Xattrs["user.rgw.olh.info"] = []byte("i")
		obj.Omap["k"] = []byte("v")
	})

	It("removes the object with obj_remove when no prefix is kept", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "head", func(op *radosclient.WriteOp) { rgwcls.ObjRemove(op, nil, denc.Squid) })).To(Succeed())
		Expect(c.Object(data, "", "head")).To(BeNil(), "cls_rgw.cc:2423-2425")
		Expect(writeErr(ctx, p, "head", func(op *radosclient.WriteOp) { rgwcls.ObjRemove(op, nil, denc.Squid) })).
			To(MatchError(radosclient.ErrNotFound), "cls_cxx_remove of a missing object")
	})

	It("keeps only the xattrs under the prefixes, and the op's later steps build the new head on them", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "head", func(op *radosclient.WriteOp) {
			op.Create(false)
			rgwcls.ObjRemove(op, []string{"user.rgw.olh."}, denc.Squid)
			op.SetXattr("user.rgw.idtag", []byte("new\x00"))
			op.WriteFull([]byte("new"))
		})).To(Succeed())
		obj := c.Object(data, "", "head")
		Expect(obj.Data).To(Equal([]byte("new")))
		Expect(obj.Omap).To(BeEmpty())
		Expect(obj.Xattrs).To(Equal(map[string][]byte{
			"user.rgw.olh.ver": []byte("3"), "user.rgw.olh.info": []byte("i"), "user.rgw.idtag": []byte("new\x00"),
		}), "cls_rgw.cc:2427-2479")
	})

	It("stores the version the object had before the op as 8 little-endian bytes with obj_store_pg_ver", func(ctx SpecContext) {
		before := c.Object(data, "", "head").Version
		Expect(writeErr(ctx, p, "head", func(op *radosclient.WriteOp) { rgwcls.ObjStorePGVer(op, "user.rgw.pg_ver", denc.Squid) })).To(Succeed())
		Expect(c.Object(data, "", "head").Xattrs).To(HaveKeyWithValue("user.rgw.pg_ver", binary.LittleEndian.AppendUint64(nil, before)))
		Expect(writeErr(ctx, p, "fresh", func(op *radosclient.WriteOp) { rgwcls.ObjStorePGVer(op, "user.rgw.pg_ver", denc.Squid) })).To(Succeed())
		Expect(c.Object(data, "", "fresh").Xattrs).To(HaveKeyWithValue("user.rgw.pg_ver", make([]byte, 8)), "a new object")
	})
})

var _ = Describe("RGWClass on a gc shard", func() {
	const logPool = "zone.rgw.log"
	var (
		c   *fakerados.Cluster
		p   radosclient.Pool
		now time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
		now = time.Date(2026, 10, 4, 12, 0, 0, 5, time.UTC)
		c.SetClock(func() time.Time { return now })
		var err error
		p, err = c.Pool(ctx, logPool, "gc")
		Expect(err).NotTo(HaveOccurred())
	})
	decodeInfo := func(b []byte) rgwcls.GCObjInfo {
		GinkgoHelper()
		d := denc.NewDecoder(b)
		info := rgwcls.DecodeGCObjInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info
	}

	It("stores a gc_set_entry under its tag and its due time, and moves the time key on a second one", func(ctx SpecContext) {
		info := rgwcls.GCObjInfo{Tag: "t\x00", Chain: []rgwcls.GCObj{{Pool: "data", Key: rgwcls.ObjKey{Name: "tail_1"}}}}
		Expect(writeErr(ctx, p, "gc.3", func(op *radosclient.WriteOp) { rgwcls.GCSetEntry(op, 7200, info, denc.Squid) })).To(Succeed())
		due := now.Add(2 * time.Hour)
		want := info
		want.Time = due
		obj := c.Object(logPool, "gc", "gc.3")
		Expect(obj.Omap).To(HaveLen(2))
		Expect(decodeInfo(obj.Omap["0_t\x00"])).To(Equal(want), "cls_rgw.cc:3924")
		Expect(decodeInfo(obj.Omap[fmt.Sprintf("1_%011d.%09d", due.Unix(), 5)])).To(Equal(want), "cls_rgw.cc:3928, get_time_key")

		now = now.Add(time.Minute)
		Expect(writeErr(ctx, p, "gc.3", func(op *radosclient.WriteOp) { rgwcls.GCSetEntry(op, 60, info, denc.Squid) })).To(Succeed())
		obj = c.Object(logPool, "gc", "gc.3")
		Expect(obj.Omap).To(HaveLen(2), "the old time key goes, cls_rgw.cc:3900-3909")
		Expect(obj.Omap).To(HaveKey(fmt.Sprintf("1_%011d.%09d", now.Add(time.Minute).Unix(), 5)))
	})
})

var _ = Describe("RGWClass's user_usage_log_add", func() {
	const (
		logPool = "zone.rgw.log"
		usageNS = "usage"
		oid     = "usage.3"
		epoch   = 1790596800
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
	add := func(ctx context.Context, entries ...rgwcls.UsageLogEntry) error {
		return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) {
			rgwcls.UsageLogAdd(op, rgwcls.UsageLogInfo{Entries: entries}, denc.Squid)
		})
	}
	entry := func(owner, payer, cat string, ops uint64) rgwcls.UsageLogEntry {
		d := rgwcls.UsageData{Ops: ops, SuccessfulOps: ops}
		return rgwcls.UsageLogEntry{
			Owner: owner, Payer: payer, Bucket: "b", Epoch: epoch, TotalUsage: d,
			UsageMap: map[string]rgwcls.UsageData{cat: d},
		}
	}

	It("stores each entry under its by-time and by-user keys, filed under its payer when it has one", func(ctx SpecContext) {
		own, paid := entry("alice", "", "get_obj", 1), entry("alice", "carol", "get_obj", 1)
		Expect(add(ctx, own, paid)).To(Succeed())
		Expect(slices.Sorted(maps.Keys(c.Object(logPool, usageNS, oid).Omap))).To(Equal([]string{
			"01790596800_alice_b", "01790596800_carol_b", "alice_01790596800_b", "carol_01790596800_b",
		}), "usage_record_name_by_time and usage_record_name_by_user")
		Expect(c.UsageEntries(logPool, usageNS, oid)).To(Equal([]rgwcls.UsageLogEntry{own, paid}))
	})

	It("adds an entry's counters to the record its key already holds", func(ctx SpecContext) {
		Expect(add(ctx, entry("alice", "", "get_obj", 1))).To(Succeed())
		Expect(add(ctx, entry("alice", "", "put_obj", 2))).To(Succeed())
		Expect(c.UsageEntries(logPool, usageNS, oid)).To(ConsistOf(rgwcls.UsageLogEntry{
			Owner: "alice", Bucket: "b", Epoch: epoch,
			TotalUsage: rgwcls.UsageData{Ops: 3, SuccessfulOps: 3},
			UsageMap: map[string]rgwcls.UsageData{
				"get_obj": {Ops: 1, SuccessfulOps: 1},
				"put_obj": {Ops: 2, SuccessfulOps: 2},
			},
		}))
	})

	It("refuses a request that does not decode with EINVAL", func(ctx SpecContext) {
		err := writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { op.Exec("rgw", "user_usage_log_add", []byte{1}) })
		Expect(err).To(haveErrno(syscall.EINVAL))
		Expect(c.Object(logPool, usageNS, oid)).To(BeNil())
	})
})
