package denc_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

var _ = Describe("Encoder primitives", func() {
	DescribeTable("encode to Ceph's little-endian layout",
		func(write func(*denc.Encoder), want []byte) {
			e := denc.NewEncoder()
			write(e)
			Expect(e.Bytes()).To(Equal(want))
			Expect(e.Len()).To(Equal(len(want)))
		},
		Entry("u8", func(e *denc.Encoder) { e.U8(0xAB) }, []byte{0xAB}),
		Entry("u16", func(e *denc.Encoder) { e.U16(0x1234) }, []byte{0x34, 0x12}),
		Entry("u32", func(e *denc.Encoder) { e.U32(0x01020304) }, []byte{4, 3, 2, 1}),
		Entry("u64", func(e *denc.Encoder) { e.U64(0x0102030405060708) }, []byte{8, 7, 6, 5, 4, 3, 2, 1}),
		Entry("i32", func(e *denc.Encoder) { e.I32(-2) }, []byte{0xFE, 0xFF, 0xFF, 0xFF}),
		Entry("i64", func(e *denc.Encoder) { e.I64(-2) }, []byte{0xFE, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}),
		Entry("bool true", func(e *denc.Encoder) { e.Bool(true) }, []byte{1}),
		Entry("bool false", func(e *denc.Encoder) { e.Bool(false) }, []byte{0}),
		Entry("string", func(e *denc.Encoder) { e.String("ab") }, []byte{2, 0, 0, 0, 'a', 'b'}),
		Entry("empty string", func(e *denc.Encoder) { e.String("") }, []byte{0, 0, 0, 0}),
		Entry("bufferlist", func(e *denc.Encoder) { e.Bytes32([]byte{9}) }, []byte{1, 0, 0, 0, 9}),
		Entry("raw", func(e *denc.Encoder) { e.Raw([]byte{7, 8}) }, []byte{7, 8}),
		Entry("time", func(e *denc.Encoder) { e.Time(time.Unix(0x01020304, 0x05060708).UTC()) }, []byte{4, 3, 2, 1, 8, 7, 6, 5}),
		Entry("zero time as the epoch radosgw writes for an unset time", func(e *denc.Encoder) { e.Time(time.Time{}) }, []byte{0, 0, 0, 0, 0, 0, 0, 0}),
		Entry("pre-1970 time wraps its seconds to u32", func(e *denc.Encoder) { e.Time(time.Unix(-1, 5)) }, []byte{0xFF, 0xFF, 0xFF, 0xFF, 5, 0, 0, 0}),
		Entry("post-2106 time truncates its seconds to u32", func(e *denc.Encoder) { e.Time(time.Unix(1<<32+3, 0)) }, []byte{3, 0, 0, 0, 0, 0, 0, 0}),
	)
})
