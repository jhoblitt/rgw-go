package fakerados_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// bi_get keys its read by type: Plain takes the name verbatim, Instance goes
// through encode_obj_index_key, which keeps an empty instance on the plain
// key, and OLH takes the OLH data key of the name; the reply carries the
// stored value as it is
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2617-L2664 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L339-L353 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2785-L2832 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L389-L403 at v20.2.4).
var _ = Describe("RGWClass's bi_get", func() {
	var s *olhShard
	BeforeEach(func(ctx SpecContext) {
		s = newOLHShard(ctx)
		s.omap()["k"] = encodeV(s.plain())
		s.stage(s.version("A"))
		s.mustLink(ctx, linkOp("A"))
	})
	get := func(ctx context.Context, k rgwcls.ObjKey, typ rgwcls.BIIndexType) (*rgwcls.BIGetResult, error) {
		r := radosclient.NewReadOp()
		res := rgwcls.BIGet(r, k, typ, denc.Squid)
		_, err := s.p.Read(ctx, olhOID, r, radosclient.OpFlagNone)
		return res, err
	}
	mustGet := func(ctx context.Context, k rgwcls.ObjKey, typ rgwcls.BIIndexType) rgwcls.BIEntry {
		GinkgoHelper()
		res, err := get(ctx, k, typ)
		Expect(err).NotTo(HaveOccurred())
		en, err := res.Result()
		Expect(err).NotTo(HaveOccurred())
		return en
	}
	dirEntry := func(e rgwcls.BIEntry) rgwcls.DirEntry {
		GinkgoHelper()
		d := denc.NewDecoder(e.Data)
		en := rgwcls.DecodeDirEntry(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return en
	}

	It("reads a Plain key as the name verbatim, whatever the instance", func(ctx SpecContext) {
		Expect(mustGet(ctx, vkey("A"), rgwcls.BIIndexPlain)).To(Equal(rgwcls.BIEntry{Type: rgwcls.BIIndexPlain, Idx: "k", Data: s.omap()["k"]}))
		raw := rgwcls.ObjKey{Name: verInstKey("A")}
		Expect(mustGet(ctx, raw, rgwcls.BIIndexPlain)).To(Equal(rgwcls.BIEntry{Type: rgwcls.BIIndexPlain, Idx: verInstKey("A"), Data: s.omap()[verInstKey("A")]}),
			"a name in the instance namespace reads that entry")
	})

	It("reads an Instance key under the instance namespace, with no mapping of null", func(ctx SpecContext) {
		e := mustGet(ctx, vkey("A"), rgwcls.BIIndexInstance)
		Expect(e.Idx).To(Equal(verInstKey("A")))
		Expect(dirEntry(e)).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)))
		_, err := get(ctx, vkey("null"), rgwcls.BIIndexInstance)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "the class reads instance null under its own key")
	})

	It("reads an Instance key with no instance as the plain key, which once the name is versioned is the version marker", func(ctx SpecContext) {
		e := mustGet(ctx, vkey(""), rgwcls.BIIndexInstance)
		Expect(e.Idx).To(Equal("k"))
		Expect(dirEntry(e)).To(Equal(versionMarker()), "not the null version under the instance namespace")
		Expect(s.entry(verInstKey(""))).To(Equal(asVersion(s.plain(), flagsNoncurrent, 1)))
	})

	It("reads an OLH key as the name's OLH data key, whatever the instance", func(ctx SpecContext) {
		e := mustGet(ctx, vkey("B"), rgwcls.BIIndexOLH)
		Expect(e).To(Equal(rgwcls.BIEntry{Type: rgwcls.BIIndexOLH, Idx: rgwcls.OLHDataKey("k"), Data: s.omap()[rgwcls.OLHDataKey("k")]}))
	})

	It("answers ENOENT for a key with no entry and EINVAL for a type it does not know", func(ctx SpecContext) {
		_, err := get(ctx, rgwcls.ObjKey{Name: "other"}, rgwcls.BIIndexPlain)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		_, err = get(ctx, rgwcls.ObjKey{Name: "other"}, rgwcls.BIIndexOLH)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		for _, typ := range []rgwcls.BIIndexType{0, 4} {
			_, err = get(ctx, vkey("A"), typ)
			Expect(err).To(MatchError(radosclient.ErrInvalid), "type %d", typ)
		}
	})
})
