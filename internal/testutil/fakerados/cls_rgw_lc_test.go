package fakerados_test

import (
	"context"
	"fmt"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// lcPair encodes a shard entry in the form releases through v15.2.4 stored,
// pair<string, int> of bucket and status, with no start time.
func lcPair(bucket string, status uint32) []byte {
	e := denc.NewEncoder()
	e.String(bucket)
	e.U32(status)
	return e.Bytes()
}

// lcListRequest encodes cls_rgw_lc_list_entries_op at struct_v v, as a client
// that predates the op's struct_v 3 sends it.
func lcListRequest(v uint8, marker string, maxEntries uint32) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(v, 1)
	e.String(marker)
	e.U32(maxEntries)
	e.EndStruct(f)
	return e.Bytes()
}

// lcHeadV1 encodes h as cls_rgw_lc_obj_head at version 1, which carries no
// rollover date.
func lcHeadV1(h rgwcls.LCHead) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	e.U64(h.StartDate)
	e.String(h.Marker)
	e.EndStruct(f)
	return e.Bytes()
}

// The lifecycle shard methods, rgw_cls_lc_get_entry through
// rgw_cls_lc_get_head
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4175-L4358 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4577-L4760 at v20.2.4,
// the same code), driven through the cls_rgw lc client.
var _ = Describe("RGWClass's lc methods", func() {
	const (
		lcPool = "zone.rgw.log"
		lcNS   = "lc"
		oid    = "lc.7"
	)
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClassMethods("rgw", fakerados.RGWClass(), fakerados.RGWMethods, fakerados.RGWWriteMethods)
		var err error
		p, err = c.Pool(ctx, lcPool, lcNS)
		Expect(err).NotTo(HaveOccurred())
	})
	entry := func(name string, start uint64, status uint32) rgwcls.LCEntry {
		return rgwcls.LCEntry{Bucket: ":" + name + ":zone.4127.1", StartTime: start, Status: status}
	}
	stored := func() *fakerados.Object { return c.Object(lcPool, lcNS, oid) }
	setEntries := func(ctx context.Context, entries ...rgwcls.LCEntry) {
		GinkgoHelper()
		Expect(writeErr(ctx, p, oid, func(op *radosclient.WriteOp) {
			for _, en := range entries {
				rgwcls.LCSetEntry(op, en)
			}
		})).To(Succeed())
	}
	stage := func(ctx context.Context, key string, value []byte) {
		GinkgoHelper()
		Expect(writeErr(ctx, p, oid, func(op *radosclient.WriteOp) {
			op.OmapSet(map[string][]byte{key: value})
		})).To(Succeed())
	}
	rmEntry := func(ctx context.Context, en rgwcls.LCEntry) error {
		return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { rgwcls.LCRmEntry(op, en) })
	}
	putHead := func(ctx context.Context, h rgwcls.LCHead) error {
		return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { rgwcls.LCPutHead(op, h) })
	}
	execWrite := func(ctx context.Context, method string, in []byte) error {
		return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { op.Exec("rgw", method, in) })
	}
	execRead := func(ctx context.Context, method string, in []byte) error {
		r := radosclient.NewReadOp()
		r.Exec("rgw", method, in)
		_, err := p.Read(ctx, oid, r, radosclient.OpFlagNone)
		return err
	}
	getEntry := func(ctx context.Context, marker string) (rgwcls.LCEntry, error) {
		r := radosclient.NewReadOp()
		res := rgwcls.LCGetEntry(r, marker)
		if _, err := p.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
			return rgwcls.LCEntry{}, err
		}
		return res.Result()
	}
	nextEntry := func(ctx context.Context, marker string) (rgwcls.LCEntry, error) {
		r := radosclient.NewReadOp()
		res := rgwcls.LCGetNextEntry(r, marker)
		if _, err := p.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
			return rgwcls.LCEntry{}, err
		}
		return res.Result()
	}
	list := func(ctx context.Context, marker string, maxEntries uint32) (rgwcls.LCListEntriesRet, error) {
		r := radosclient.NewReadOp()
		res := rgwcls.LCListEntries(r, marker, maxEntries)
		if _, err := p.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
			return rgwcls.LCListEntriesRet{}, err
		}
		return res.Result()
	}
	getHead := func(ctx context.Context) (rgwcls.LCHead, error) {
		r := radosclient.NewReadOp()
		res := rgwcls.LCGetHead(r)
		if _, err := p.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
			return rgwcls.LCHead{}, err
		}
		return res.Result()
	}

	It("registers lc_set_entry, lc_rm_entry and lc_put_head alone of the seven as writes", func() {
		// The class registers those three CLS_METHOD_RD | CLS_METHOD_WR and the
		// others CLS_METHOD_RD
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4734-L4740 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5153-L5159 at v20.2.4).
		writes := []string{"lc_set_entry", "lc_rm_entry", "lc_put_head"}
		reads := []string{"lc_get_entry", "lc_get_next_entry", "lc_get_head", "lc_list_entries"}
		Expect(fakerados.RGWMethods).To(ContainElements(append(writes, reads...)))
		Expect(fakerados.RGWWriteMethods).To(ContainElements(writes))
		for _, m := range reads {
			Expect(fakerados.RGWWriteMethods).NotTo(ContainElement(m), "%s is CLS_METHOD_RD only", m)
		}
	})

	DescribeTable("answers EINVAL for a request that does not decode, changing nothing",
		func(ctx SpecContext, method string, write bool) {
			a := entry("a", 10, rgwcls.LCStatusProcessing)
			setEntries(ctx, a)
			cut := []byte{2, 2, 0x40, 0, 0, 0}
			var err error
			if write {
				err = execWrite(ctx, method, cut)
			} else {
				err = execRead(ctx, method, cut)
			}
			Expect(err).To(MatchError(radosclient.ErrInvalid), "struct_len past the request's end")
			Expect(stored().Omap).To(Equal(map[string][]byte{a.Bucket: encodeV(a)}))
			Expect(stored().OmapHdr).To(BeEmpty())
		},
		Entry("lc_get_entry, cls_rgw.cc:4180-4186 at v19.2.7, :4582-4588 at v20.2.4", "lc_get_entry", false),
		Entry("lc_set_entry, cls_rgw.cc:4204-4210 at v19.2.7, :4606-4612 at v20.2.4", "lc_set_entry", true),
		Entry("lc_rm_entry, cls_rgw.cc:4224-4230 at v19.2.7, :4626-4632 at v20.2.4", "lc_rm_entry", true),
		Entry("lc_get_next_entry, cls_rgw.cc:4241-4247 at v19.2.7, :4643-4649 at v20.2.4", "lc_get_next_entry", false),
		Entry("lc_list_entries, cls_rgw.cc:4275-4282 at v19.2.7, :4677-4684 at v20.2.4", "lc_list_entries", false),
		Entry("lc_put_head, cls_rgw.cc:4320-4326 at v19.2.7, :4722-4728 at v20.2.4", "lc_put_head", true),
	)

	It("answers ENOENT for each read of a missing shard, and for lc_rm_entry, creating nothing", func(ctx SpecContext) {
		_, err := getEntry(ctx, entry("a", 0, 0).Bucket)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "lc_get_entry")
		_, err = nextEntry(ctx, "")
		Expect(err).To(MatchError(radosclient.ErrNotFound), "lc_get_next_entry")
		_, err = list(ctx, "", 10)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "lc_list_entries")
		_, err = getHead(ctx)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "lc_get_head")
		// cls_cxx_map_remove_key is CEPH_OSD_OP_OMAPRMKEYS, ENOENT on an
		// object that does not exist
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7959-L7972 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8036-L8049 at v20.2.4).
		Expect(rmEntry(ctx, entry("a", 0, 0))).To(MatchError(radosclient.ErrNotFound), "lc_rm_entry")
		Expect(stored()).To(BeNil())
	})

	It("creates the shard with lc_set_entry, storing the entry under its bucket, which lc_get_entry reads", func(ctx SpecContext) {
		a := entry("a", 1790596800, rgwcls.LCStatusProcessing)
		setEntries(ctx, a)
		Expect(stored()).NotTo(BeNil())
		Expect(stored().Omap).To(HaveKeyWithValue(a.Bucket, encodeV(a)), "cls_rgw_lc_entry, ENCODE_START(1, 1)")
		Expect(getEntry(ctx, a.Bucket)).To(Equal(a))

		a.Status, a.StartTime = rgwcls.LCStatusComplete, 1790600400
		setEntries(ctx, a)
		Expect(getEntry(ctx, a.Bucket)).To(Equal(a), "a second set replaces the entry")
		_, err := getEntry(ctx, entry("b", 0, 0).Bucket)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "a bucket the shard does not hold")
	})

	It("stores the pair of a version 1 lc_set_entry request as an entry with start 0", func(ctx SpecContext) {
		// cls_rgw_lc_set_entry_op reads the pair below version 2
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L1319-L1329 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L1357-L1367 at v20.2.4).
		old := entry("old", 0, rgwcls.LCStatusComplete)
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.Raw(lcPair(old.Bucket, old.Status))
		e.EndStruct(f)
		Expect(execWrite(ctx, "lc_set_entry", e.Bytes())).To(Succeed())
		Expect(stored().Omap).To(HaveKeyWithValue(old.Bucket, encodeV(old)), "cls_rgw_lc_entry, not the pair")
		Expect(getEntry(ctx, old.Bucket)).To(Equal(old))
	})

	It("removes one entry with lc_rm_entry, keeping the shard and the others, and takes a bucket it does not hold", func(ctx SpecContext) {
		a, b := entry("a", 0, rgwcls.LCStatusUninitial), entry("b", 0, rgwcls.LCStatusUninitial)
		setEntries(ctx, a, b)
		Expect(rmEntry(ctx, rgwcls.LCEntry{Bucket: a.Bucket, Status: rgwcls.LCStatusFailed})).To(Succeed(),
			"the class reads only the bucket")
		_, err := getEntry(ctx, a.Bucket)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		Expect(getEntry(ctx, b.Bucket)).To(Equal(b))
		Expect(rmEntry(ctx, a)).To(Succeed(), "OMAPRMKEYS of a key the object lacks")
		Expect(stored().Omap).To(HaveLen(1))
	})

	It("walks the entries in key order with lc_get_next_entry, and answers an empty entry past the last", func(ctx SpecContext) {
		a, b, d := entry("a", 10, 1), entry("b", 20, 2), entry("d", 30, 3)
		setEntries(ctx, d, a, b)
		Expect(nextEntry(ctx, "")).To(Equal(a), "the first of all")
		Expect(nextEntry(ctx, a.Bucket)).To(Equal(b), "the first key after the marker, not the marker's")
		Expect(nextEntry(ctx, entry("c", 0, 0).Bucket)).To(Equal(d), "a marker the shard does not hold")
		Expect(nextEntry(ctx, d.Bucket)).To(Equal(rgwcls.LCEntry{}), "past the last entry, not an error")
	})

	It("answers lc_get_next_entry with an empty entry on a shard of no entries", func(ctx SpecContext) {
		Expect(putHead(ctx, rgwcls.LCHead{Marker: "m"})).To(Succeed())
		Expect(nextEntry(ctx, "")).To(Equal(rgwcls.LCEntry{}))
	})

	It("pages lc_list_entries strictly after the marker, at most the maximum, truncated while a key remains", func(ctx SpecContext) {
		var all []rgwcls.LCEntry
		for i := range 5 {
			all = append(all, entry(fmt.Sprintf("b%d", i), uint64(100+i), rgwcls.LCStatusComplete))
		}
		setEntries(ctx, all[3], all[0], all[4], all[1], all[2])
		ret, err := list(ctx, "", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Entries).To(Equal(all[:2]))
		Expect(ret.Truncated).To(BeTrue())
		ret, err = list(ctx, all[1].Bucket, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Entries).To(Equal(all[2:4]), "after the marker, never the marker's own entry")
		Expect(ret.Truncated).To(BeTrue())
		ret, err = list(ctx, all[3].Bucket, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Entries).To(Equal(all[4:]))
		Expect(ret.Truncated).To(BeFalse(), "no key remains")
		ret, err = list(ctx, all[2].Bucket, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Entries).To(Equal(all[3:]))
		Expect(ret.Truncated).To(BeFalse(), "a page that takes exactly the keys left is not truncated")
		ret, err = list(ctx, all[4].Bucket, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Entries).To(BeEmpty())
		Expect(ret.Truncated).To(BeFalse())
		Expect(ret.Version).To(HaveValue(Equal(uint8(3))), "the client's request version")
	})

	It("answers a maximum of 0 with no entries, truncated while a key remains", func(ctx SpecContext) {
		a, b := entry("a", 0, 0), entry("b", 0, 0)
		setEntries(ctx, a, b)
		ret, err := list(ctx, "", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Entries).To(BeEmpty())
		Expect(ret.Truncated).To(BeTrue())
		ret, err = list(ctx, b.Bucket, 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Entries).To(BeEmpty())
		Expect(ret.Truncated).To(BeFalse())
	})

	It("caps an lc_list_entries page at osd_max_omap_entries_per_request", func(ctx SpecContext) {
		entries := make([]rgwcls.LCEntry, 1025)
		for i := range entries {
			entries[i] = entry(fmt.Sprintf("b%04d", i), 0, 0)
		}
		setEntries(ctx, entries...)
		ret, err := list(ctx, "", 5000)
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Entries).To(HaveLen(1024))
		Expect(ret.Truncated).To(BeTrue())
	})

	It("replies to an lc_list_entries request at its struct_v, the bucket-to-status map for version 2", func(ctx SpecContext) {
		a, b := entry("a", 1790596800, rgwcls.LCStatusProcessing), entry("b", 1790596900, rgwcls.LCStatusComplete)
		setEntries(ctx, a, b)
		r := radosclient.NewReadOp()
		res := r.Exec("rgw", "lc_list_entries", lcListRequest(2, "", 1))
		_, err := p.Read(ctx, oid, r, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		out, err := res.Bytes()
		Expect(err).NotTo(HaveOccurred())
		Expect(out[0]).To(Equal(uint8(2)), "ENCODE_START(compat_v, 1)")
		d := denc.NewDecoder(out)
		ret := rgwcls.DecodeLCListEntriesRet(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(ret.Entries).To(Equal([]rgwcls.LCEntry{{Bucket: a.Bucket, Status: a.Status}}), "a map carries no start time")
		Expect(ret.Truncated).To(BeTrue(), "version 2 carries the truncation flag")
	})

	Describe("a value stored in the pair form of releases through v15.2.4", func() {
		// docs/ceph-upstream-bugs.md, "cls_rgw's lc_get_entry and
		// lc_get_next_entry refuse an lc entry stored in its old pair form,
		// which stalls the shard".
		var a, old, d rgwcls.LCEntry
		BeforeEach(func(ctx SpecContext) {
			a, old, d = entry("a", 10, 1), entry("old", 0, rgwcls.LCStatusComplete), entry("z", 30, 3)
			setEntries(ctx, a, d)
			stage(ctx, old.Bucket, lcPair(old.Bucket, old.Status))
		})

		It("is EIO from lc_get_entry, whose read_omap_entry reads no pair", func(ctx SpecContext) {
			_, err := getEntry(ctx, old.Bucket)
			Expect(err).To(haveErrno(syscall.EIO))
			Expect(getEntry(ctx, a.Bucket)).To(Equal(a), "the shard's other entries still read")
		})

		It("is EIO from lc_get_next_entry, when it is the next entry", func(ctx SpecContext) {
			_, err := nextEntry(ctx, a.Bucket)
			Expect(err).To(haveErrno(syscall.EIO))
			Expect(nextEntry(ctx, old.Bucket)).To(Equal(d), "a marker at the pair key moves past it")
		})

		It("is the pair's bucket and status with start 0 from lc_list_entries", func(ctx SpecContext) {
			ret, err := list(ctx, "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.Entries).To(Equal([]rgwcls.LCEntry{a, old, d}), "the pair read as {bucket, 0, status}")
		})
	})

	It("answers EIO from all three entry reads for a value that is neither an entry nor a pair", func(ctx SpecContext) {
		a := entry("a", 10, 1)
		bad := entry("bad", 0, 0)
		setEntries(ctx, a)
		stage(ctx, bad.Bucket, []byte{1, 2, 3})
		_, err := getEntry(ctx, bad.Bucket)
		Expect(err).To(haveErrno(syscall.EIO), "lc_get_entry")
		_, err = nextEntry(ctx, a.Bucket)
		Expect(err).To(haveErrno(syscall.EIO), "lc_get_next_entry")
		_, err = list(ctx, "", 10)
		Expect(err).To(haveErrno(syscall.EIO), "lc_list_entries")
		ret, err := list(ctx, bad.Bucket, 10)
		Expect(err).NotTo(HaveOccurred(), "a page that does not reach the value")
		Expect(ret.Entries).To(BeEmpty())
	})

	It("writes the head as the omap header with lc_put_head, creating the shard, and reads it back with lc_get_head", func(ctx SpecContext) {
		h := rgwcls.LCHead{StartDate: 1790596800, Marker: entry("b", 0, 0).Bucket, ShardRolloverDate: 1790553600}
		Expect(putHead(ctx, h)).To(Succeed())
		Expect(stored()).NotTo(BeNil())
		Expect(stored().OmapHdr).To(Equal(encodeV(h)), "cls_rgw_lc_obj_head, ENCODE_START(2, 2)")
		Expect(stored().Omap).To(BeEmpty())
		Expect(getHead(ctx)).To(Equal(h))
		h.Marker = ""
		Expect(putHead(ctx, h)).To(Succeed())
		Expect(getHead(ctx)).To(Equal(h), "a second put replaces the head")
	})

	It("answers lc_get_head with the zero head for a shard without a header, and EINVAL for one that does not decode", func(ctx SpecContext) {
		setEntries(ctx, entry("a", 0, 0))
		Expect(getHead(ctx)).To(Equal(rgwcls.LCHead{}))
		stored().OmapHdr = []byte{2, 2, 0xff, 0xff, 0, 0}
		_, err := getHead(ctx)
		Expect(err).To(MatchError(radosclient.ErrInvalid))
	})

	// cls_rgw_lc_obj_head reads no rollover date below version 2 and writes
	// version 2
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L1272-L1294 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L1309-L1331 at v20.2.4).
	It("stores a version 1 head from lc_put_head at version 2, with a zero rollover date", func(ctx SpecContext) {
		h := rgwcls.LCHead{StartDate: 1790596800, Marker: entry("b", 0, 0).Bucket}
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.Raw(lcHeadV1(h))
		e.EndStruct(f)
		Expect(execWrite(ctx, "lc_put_head", e.Bytes())).To(Succeed())
		Expect(stored().OmapHdr).To(Equal(encodeV(h)), "ENCODE_START(2, 2), not the request's version 1")
		Expect(getHead(ctx)).To(Equal(h))
	})

	It("reads a stored version 1 head with lc_get_head, with a zero rollover date", func(ctx SpecContext) {
		h := rgwcls.LCHead{StartDate: 1790596800, Marker: entry("b", 0, 0).Bucket}
		setEntries(ctx, entry("a", 0, 0))
		stored().OmapHdr = lcHeadV1(h)
		Expect(getHead(ctx)).To(Equal(h))
	})
})
