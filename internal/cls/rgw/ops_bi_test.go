package rgw_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The class registers bi_get RD, so it is added to a read op.
var _ func(*radosclient.ReadOp, rgw.ObjKey, rgw.BIIndexType, denc.Release) *rgw.BIGetResult = rgw.BIGet

// biGetReply writes rgw_cls_bi_get_ret v1 around rgw_cls_bi_entry v1 from
// their C++ encode bodies.
func biGetReply(typ uint8, idx string, data []byte) []byte {
	return build(func(e *denc.Encoder) {
		f := e.BeginStruct(1, 1)
		be := e.BeginStruct(1, 1)
		e.U8(typ)
		e.String(idx)
		e.Bytes32(data)
		e.EndStruct(be)
		e.EndStruct(f)
	})
}

// biGetRan returns a bi_get result whose op ran with out and rval.
func biGetRan(typ rgw.BIIndexType, out []byte, rval int32) *rgw.BIGetResult {
	GinkgoHelper()
	op := radosclient.NewReadOp()
	res := rgw.BIGet(op, rgw.ObjKey{Name: "obj"}, typ, denc.Squid)
	execResult(op).Set(out, rval)
	return res
}

var _ = Describe("the bi_get goldens", func() {
	for _, o := range []goldentest.Options{skipJSON, tentacleSkipJSON} {
		It("rgw_cls_bi_entry at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_bi_entry", o, rgw.DecodeBIEntry, enc[rgw.BIEntry])
		})
		It("rgw_cls_bi_get_op at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_bi_get_op", o, rgw.DecodeBIGetOp, enc[rgw.BIGetOp])
		})
		It("rgw_cls_bi_get_ret at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_bi_get_ret", o, rgw.DecodeBIGetRet, enc[rgw.BIGetRet])
		})
	}
})

var _ = Describe("bi_get", func() {
	DescribeTable("BIIndexType carries the class's values",
		func(typ rgw.BIIndexType, want uint8) {
			Expect(uint8(typ)).To(Equal(want))
		},
		Entry("Plain", rgw.BIIndexPlain, uint8(1)),
		Entry("Instance", rgw.BIIndexInstance, uint8(2)),
		Entry("OLH", rgw.BIIndexOLH, uint8(3)),
	)

	It("sends every corpus request on a read op", func() {
		for _, c := range goldens("rgw_cls_bi_get_op") {
			v := decodeWhole(c.Bin, rgw.DecodeBIGetOp)
			op := radosclient.NewReadOp()
			rgw.BIGet(op, v.Key, v.Type, denc.Squid)
			Expect(soleExec(op.Steps(), "bi_get").In).To(Equal(c.ReEnc), "%s/%s", c.Archive, c.Name)
		}
	})

	It("writes the type as one byte after the key", func() {
		want := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			objKey(e, "obj", "inst")
			e.U8(3)
			e.EndStruct(f)
		})
		for _, r := range releases {
			op := radosclient.NewReadOp()
			rgw.BIGet(op, rgw.ObjKey{Name: "obj", Instance: "inst"}, rgw.BIIndexOLH, r)
			Expect(soleExec(op.Steps(), "bi_get").In).To(Equal(want), "%v", r)
		}
	})

	It("answers the entry of every rgw_cls_bi_get_ret golden", func() {
		for _, c := range goldens("rgw_cls_bi_get_ret") {
			got, err := biGetRan(rgw.BIIndexPlain, c.Bin, 0).Result()
			Expect(err).NotTo(HaveOccurred(), "%s/%s", c.Archive, c.Name)
			Expect(got).To(Equal(decodeWhole(c.Bin, rgw.DecodeBIGetRet).Entry), "%s/%s", c.Archive, c.Name)
		}
	})

	It("decodes a stored index entry of every version the corpus holds", func() {
		for _, c := range goldens("rgw_bucket_dir_entry") {
			res := biGetRan(rgw.BIIndexInstance, biGetReply(2, "\x801000_obj\x00iinst", c.Bin), 0)
			got, err := res.DirEntry()
			Expect(err).NotTo(HaveOccurred(), "%s/%s", c.Archive, c.Name)
			Expect(got).To(Equal(decodeWhole(c.Bin, rgw.DecodeDirEntry)), "%s/%s", c.Archive, c.Name)
		}
	})

	It("decodes every stored OLH entry the corpus holds", func() {
		for _, c := range goldens("rgw_bucket_olh_entry") {
			res := biGetRan(rgw.BIIndexOLH, biGetReply(3, "\x801001_obj", c.Bin), 0)
			got, err := res.OLHEntry()
			Expect(err).NotTo(HaveOccurred(), "%s/%s", c.Archive, c.Name)
			Expect(got).To(Equal(decodeWhole(c.Bin, rgw.DecodeOLHEntry)), "%s/%s", c.Archive, c.Name)
		}
	})

	It("ignores bytes past the stored entry, as radosgw's decode does", func() {
		c := goldens("rgw_bucket_olh_entry")[0]
		res := biGetRan(rgw.BIIndexOLH, biGetReply(3, "\x801001_obj", append(append([]byte{}, c.Bin...), 0xff)), 0)
		got, err := res.OLHEntry()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(decodeWhole(c.Bin, rgw.DecodeOLHEntry)))
	})

	It("answers ENOENT for a missing entry from the entry and both decoders", func() {
		res := biGetRan(rgw.BIIndexInstance, nil, -2)
		_, err := res.Result()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		_, err = res.DirEntry()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		_, err = res.OLHEntry()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})

	It("names bi_get and the index key in a stored entry that fails to decode", func() {
		res := biGetRan(rgw.BIIndexOLH, biGetReply(3, "\x801001_obj", []byte{1, 1, 100, 0, 0, 0}), 0)
		_, err := res.OLHEntry()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
		Expect(err).To(MatchError(ContainSubstring(`rgw: decoding bi_get entry "\x801001_obj"`)))
		_, err = res.DirEntry()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
	})

	It("names bi_get in a reply that fails to decode", func() {
		_, err := biGetRan(rgw.BIIndexPlain, []byte{1, 1, 100, 0, 0, 0}, 0).Result()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
		Expect(err).To(MatchError(ContainSubstring("rgw: decoding bi_get reply")))
	})
})
