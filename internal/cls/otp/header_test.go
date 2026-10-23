package otp_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

var _ = Describe("otp_header", func() {
	// otp_header is ENCODE_START(1, 1) around a std::set<std::string>: a u32
	// count, then each id as a u32 length and its bytes, in the set's order
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L48-L64 at v19.2.7 and v20.2.4).
	const twoIDs = "010114000000" + // otp_header (1,1), 20 bytes
		"02000000" + // two ids
		"04000000" + "64657631" + // "dev1"
		"04000000" + "64657632" // "dev2"

	DescribeTable("encodes the hand-built golden of the class's set",
		func(ids []string, golden string) {
			Expect(encode(otp.Header{IDs: ids})).To(Equal(unhex(golden)))
		},
		Entry("two ids", []string{"dev1", "dev2"}, twoIDs),
		Entry("ids out of order and repeated, as the set holds each once in order", []string{"dev2", "dev1", "dev2"}, twoIDs),
		Entry("no ids", nil, "010104000000"+"00000000"),
		Entry("an id with a byte above 0x7f after an ASCII one, as std::string compares bytes unsigned",
			[]string{"\xc3\xa9", "z"}, "01010f000000"+"02000000"+"01000000"+"7a"+"02000000"+"c3a9"),
	)

	It("decodes the golden", func() {
		Expect(decodeAll(unhex(twoIDs), otp.DecodeHeader)).To(Equal(otp.Header{IDs: []string{"dev1", "dev2"}}))
	})

	It("is kept under the class's header key", func() {
		Expect(otp.HeaderKey).To(Equal("header"))
	})

	It("refuses a struct whose compat is newer than version 1", func() {
		b := unhex(twoIDs)
		b[0], b[1] = 2, 2
		d := denc.NewDecoder(b)
		otp.DecodeHeader(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
})
