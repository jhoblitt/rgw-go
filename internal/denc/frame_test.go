package denc_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

var _ = Describe("struct framing", func() {
	It("writes the six-byte header with the payload length patched in", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(3, 1)
		e.U32(0xAABBCCDD)
		e.EndStruct(f)
		Expect(e.Bytes()).To(Equal([]byte{3, 1, 4, 0, 0, 0, 0xDD, 0xCC, 0xBB, 0xAA}))
	})
	It("patches nested struct lengths independently", func() {
		e := denc.NewEncoder()
		outer := e.BeginStruct(2, 1)
		inner := e.BeginStruct(1, 1)
		e.U8(0x42)
		e.EndStruct(inner)
		e.U8(0x43)
		e.EndStruct(outer)
		Expect(e.Bytes()).To(Equal([]byte{2, 1, 8, 0, 0, 0, 1, 1, 1, 0, 0, 0, 0x42, 0x43}))
	})
	DescribeTable("round-trips the encoder's standard header",
		func(begin func(*denc.Decoder) denc.Header) {
			e := denc.NewEncoder()
			f := e.BeginStruct(4, 2)
			e.String("x")
			e.EndStruct(f)
			e.U8(0x99)

			d := denc.NewDecoder(e.Bytes())
			h := begin(d)
			Expect(h.Version).To(Equal(uint8(4)))
			Expect(h.Compat).To(Equal(uint8(2)))
			Expect(d.String()).To(Equal("x"))
			d.EndStruct(h)
			Expect(d.U8()).To(Equal(uint8(0x99)))
			Expect(d.Err()).NotTo(HaveOccurred())
		},
		Entry("standard decoder", func(d *denc.Decoder) denc.Header { return d.BeginStruct(4) }),
		Entry("legacy decoder", func(d *denc.Decoder) denc.Header { return d.BeginStructLegacy(4, 2, 2, 3) }),
	)
	It("skips unknown trailing fields written by a newer encoder", func() {
		d := denc.NewDecoder([]byte{5, 1, 8, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0})
		h := d.BeginStruct(3)
		Expect(h.Version).To(Equal(uint8(5)))
		Expect(d.U32()).To(Equal(uint32(1)))
		d.EndStruct(h)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.Remaining()).To(BeZero(), "the second u32 was skipped")
	})
	It("refuses a compat version newer than the decoder", func() {
		d := denc.NewDecoder([]byte{9, 7, 0, 0, 0, 0})
		d.BeginStruct(3)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
	It("refuses a struct length longer than the remaining bytes", func() {
		d := denc.NewDecoder([]byte{1, 1, 5, 0, 0, 0, 0xFF})
		d.BeginStruct(1)
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
	})
	It("fails when the decoder reads past the struct end", func() {
		d := denc.NewDecoder([]byte{1, 1, 1, 0, 0, 0, 0xFF, 0xEE})
		h := d.BeginStruct(1)
		d.U16()
		d.EndStruct(h)
		Expect(d.Err()).To(MatchError(denc.ErrOverread))
	})
	It("leaves the offset alone when ending a struct after a failure", func() {
		d := denc.NewDecoder([]byte{1, 1, 4, 0, 0, 0, 0xFF, 0xEE, 0xDD, 0xCC})
		h := d.BeginStruct(1)
		d.U8()
		d.Fail(denc.ErrIncompatible)
		d.EndStruct(h)
		Expect(d.Offset()).To(Equal(7))
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})

	Describe("legacy compat-length framing", func() {
		It("reads the compat byte and length when struct_v is at or above compatv", func() {
			d := denc.NewDecoder([]byte{10, 3, 1, 0, 0, 0, 0x42})
			h := d.BeginStructLegacy(10, 3, 3, 0)
			Expect(h.Version).To(Equal(uint8(10)))
			Expect(d.U8()).To(Equal(uint8(0x42)))
			d.EndStruct(h)
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("reads neither compat nor length for an old struct_v", func() {
			d := denc.NewDecoder([]byte{2, 0x42})
			h := d.BeginStructLegacy(10, 3, 3, 0)
			Expect(h.Version).To(Equal(uint8(2)))
			Expect(d.U8()).To(Equal(uint8(0x42)), "payload follows the version byte directly")
			d.EndStruct(h)
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("skips the three high bytes of an old 32-bit version", func() {
			d := denc.NewDecoder([]byte{2, 0, 0, 0, 0x42})
			h := d.BeginStructLegacy(23, 9, 9, 3)
			Expect(h.Version).To(Equal(uint8(2)))
			Expect(d.U8()).To(Equal(uint8(0x42)))
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("reads the compat byte but no length between compatv and lenv", func() {
			d := denc.NewDecoder([]byte{4, 2, 0x42, 0x43})
			h := d.BeginStructLegacy(10, 3, 5, 0)
			Expect(h.Version).To(Equal(uint8(4)))
			Expect(h.Compat).To(Equal(uint8(2)))
			Expect(d.U8()).To(Equal(uint8(0x42)))
			d.EndStruct(h)
			Expect(d.Offset()).To(Equal(3), "with no length EndStruct does not move the offset")
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("does not skip when the compat byte is present", func() {
			d := denc.NewDecoder([]byte{9, 9, 1, 0, 0, 0, 0x42})
			h := d.BeginStructLegacy(23, 9, 9, 3)
			Expect(d.U8()).To(Equal(uint8(0x42)))
			d.EndStruct(h)
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(d.Remaining()).To(BeZero())
		})
		It("fails when the skipped bytes are missing", func() {
			d := denc.NewDecoder([]byte{2, 0})
			d.BeginStructLegacy(23, 9, 9, 3)
			Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		})
		It("refuses a compat version newer than the decoder", func() {
			d := denc.NewDecoder([]byte{12, 11, 0, 0, 0, 0})
			d.BeginStructLegacy(10, 3, 3, 0)
			Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
		})
		It("skips unknown trailing fields when a length is present", func() {
			d := denc.NewDecoder([]byte{10, 3, 2, 0, 0, 0, 0x42, 0x43, 0x44})
			h := d.BeginStructLegacy(10, 3, 3, 0)
			d.U8()
			d.EndStruct(h)
			Expect(d.U8()).To(Equal(uint8(0x44)))
			Expect(d.Err()).NotTo(HaveOccurred())
		})
	})
})
