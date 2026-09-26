package denc_test

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

var _ = Describe("Decoder primitives", func() {
	DescribeTable("decode Ceph's little-endian layout",
		func(in []byte, read func(*denc.Decoder) any, want any) {
			d := denc.NewDecoder(in)
			Expect(read(d)).To(Equal(want))
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(d.Remaining()).To(BeZero())
			Expect(d.Offset()).To(Equal(len(in)))
		},
		Entry("u8", []byte{0xAB}, func(d *denc.Decoder) any { return d.U8() }, uint8(0xAB)),
		Entry("u16", []byte{0x34, 0x12}, func(d *denc.Decoder) any { return d.U16() }, uint16(0x1234)),
		Entry("u32", []byte{4, 3, 2, 1}, func(d *denc.Decoder) any { return d.U32() }, uint32(0x01020304)),
		Entry("u64", []byte{8, 7, 6, 5, 4, 3, 2, 1}, func(d *denc.Decoder) any { return d.U64() }, uint64(0x0102030405060708)),
		Entry("i32", []byte{0xFE, 0xFF, 0xFF, 0xFF}, func(d *denc.Decoder) any { return d.I32() }, int32(-2)),
		Entry("i64", []byte{0xFE, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, func(d *denc.Decoder) any { return d.I64() }, int64(-2)),
		Entry("bool true", []byte{1}, func(d *denc.Decoder) any { return d.Bool() }, true),
		Entry("bool false", []byte{0}, func(d *denc.Decoder) any { return d.Bool() }, false),
		Entry("string", []byte{2, 0, 0, 0, 'a', 'b'}, func(d *denc.Decoder) any { return d.String() }, "ab"),
		Entry("empty string", []byte{0, 0, 0, 0}, func(d *denc.Decoder) any { return d.String() }, ""),
		Entry("bufferlist", []byte{1, 0, 0, 0, 9}, func(d *denc.Decoder) any { return d.Bytes32() }, []byte{9}),
		Entry("raw", []byte{7, 8}, func(d *denc.Decoder) any { return d.Raw(2) }, []byte{7, 8}),
		Entry("time", []byte{4, 3, 2, 1, 8, 7, 6, 5}, func(d *denc.Decoder) any { return d.Time() }, time.Unix(0x01020304, 0x05060708).UTC()),
	)

	It("records a short buffer once and returns zero values afterwards", func() {
		d := denc.NewDecoder([]byte{1, 0})
		Expect(d.U32()).To(BeZero())
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		Expect(d.U8()).To(BeZero(), "calls after the first error return zero values")
	})
	It("decodes the all-zero epoch as Go's zero time", func() {
		d := denc.NewDecoder([]byte{0, 0, 0, 0, 0, 0, 0, 0})
		Expect(d.Time().IsZero()).To(BeTrue())
		Expect(d.Err()).NotTo(HaveOccurred())
	})
	It("accepts any non-zero byte as true", func() {
		Expect(denc.NewDecoder([]byte{7}).Bool()).To(BeTrue())
	})
	It("fails a string whose length runs past the buffer", func() {
		d := denc.NewDecoder([]byte{5, 0, 0, 0, 'a'})
		Expect(d.String()).To(BeEmpty())
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
	})
	It("returns copies that do not alias the input", func() {
		in := []byte{1, 0, 0, 0, 9, 8}
		d := denc.NewDecoder(in)
		bl := d.Bytes32()
		raw := d.Raw(1)
		in[4], in[5] = 0, 0
		Expect(bl).To(Equal([]byte{9}))
		Expect(raw).To(Equal([]byte{8}))
	})
	It("rejects a negative raw length", func() {
		d := denc.NewDecoder([]byte{1})
		Expect(d.Raw(-1)).To(BeNil())
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
	})
	It("keeps the first error when a caller fails after an internal failure", func() {
		d := denc.NewDecoder(nil)
		d.U8()
		d.Fail(errors.New("bad enum"))
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
	})
	It("records a caller-raised error and returns zero values afterwards", func() {
		errBad := errors.New("bad enum")
		d := denc.NewDecoder([]byte{1})
		d.Fail(errBad)
		Expect(d.U8()).To(BeZero())
		Expect(d.Err()).To(MatchError(errBad))
		Expect(d.Offset()).To(BeZero(), "a failed decoder does not advance")
	})
})
