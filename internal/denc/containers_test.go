package denc_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

func encString(e *denc.Encoder, s string) { e.String(s) }
func decString(d *denc.Decoder) string    { return d.String() }
func encU32(e *denc.Encoder, v uint32)    { e.U32(v) }
func decU32(d *denc.Decoder) uint32       { return d.U32() }

var _ = Describe("containers", func() {
	Describe("slices", func() {
		It("encodes a count then the elements", func() {
			e := denc.NewEncoder()
			denc.EncodeSlice(e, []string{"a", "bc"}, encString)
			want := []byte{2, 0, 0, 0, 1, 0, 0, 0, 'a', 2, 0, 0, 0, 'b', 'c'}
			Expect(e.Bytes()).To(Equal(want))

			d := denc.NewDecoder(want)
			Expect(denc.DecodeSlice(d, decString)).To(Equal([]string{"a", "bc"}))
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("decodes an empty slice as nil", func() {
			d := denc.NewDecoder([]byte{0, 0, 0, 0})
			Expect(denc.DecodeSlice(d, decString)).To(BeNil())
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("returns nil when the elements run short", func() {
			d := denc.NewDecoder([]byte{2, 0, 0, 0, 1, 0, 0, 0})
			Expect(denc.DecodeSlice(d, decU32)).To(BeNil())
			Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		})
		It("fails a hostile 2^32-1 count against a short buffer", func() {
			d := denc.NewDecoder([]byte{0xFF, 0xFF, 0xFF, 0xFF, 1, 0, 0, 0})
			Expect(denc.DecodeSlice(d, decU32)).To(BeNil())
			Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		})
	})

	Describe("maps", func() {
		It("encodes keys in sorted order", func() {
			e := denc.NewEncoder()
			denc.EncodeMap(e, map[string]uint32{"b": 2, "a": 1}, encString, encU32)
			want := []byte{
				2, 0, 0, 0,
				1, 0, 0, 0, 'a', 1, 0, 0, 0,
				1, 0, 0, 0, 'b', 2, 0, 0, 0,
			}
			Expect(e.Bytes()).To(Equal(want))

			d := denc.NewDecoder(want)
			Expect(denc.DecodeMap(d, decString, decU32)).To(Equal(map[string]uint32{"a": 1, "b": 2}))
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("keeps the first value for a duplicated key", func() {
			d := denc.NewDecoder([]byte{
				2, 0, 0, 0,
				1, 0, 0, 0, 'a', 1, 0, 0, 0,
				1, 0, 0, 0, 'a', 2, 0, 0, 0,
			})
			Expect(denc.DecodeMap(d, decString, decU32)).To(Equal(map[string]uint32{"a": 1}))
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(d.Remaining()).To(BeZero(), "the duplicate's value is still consumed")
		})
		It("keeps the last value for a duplicated key under DecodeMapLast", func() {
			d := denc.NewDecoder([]byte{
				3, 0, 0, 0,
				1, 0, 0, 0, 'a', 1, 0, 0, 0,
				1, 0, 0, 0, 'b', 5, 0, 0, 0,
				1, 0, 0, 0, 'a', 2, 0, 0, 0,
			})
			Expect(denc.DecodeMapLast(d, decString, decU32)).To(Equal(map[string]uint32{"a": 2, "b": 5}))
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(d.Remaining()).To(BeZero())
		})
		It("decodes an empty or short map as nil under DecodeMapLast", func() {
			Expect(denc.DecodeMapLast(denc.NewDecoder([]byte{0, 0, 0, 0}), decString, decU32)).To(BeNil())
			d := denc.NewDecoder([]byte{2, 0, 0, 0, 1, 0, 0, 0, 'a', 1, 0, 0, 0})
			Expect(denc.DecodeMapLast(d, decString, decU32)).To(BeNil())
			Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		})
		It("decodes an empty map as nil", func() {
			d := denc.NewDecoder([]byte{0, 0, 0, 0})
			Expect(denc.DecodeMap(d, decString, decU32)).To(BeNil())
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("fails a hostile 2^32-1 count against a short buffer", func() {
			d := denc.NewDecoder([]byte{0xFF, 0xFF, 0xFF, 0xFF, 1, 0, 0, 0, 'a'})
			Expect(denc.DecodeMap(d, decString, decU32)).To(BeNil())
			Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		})
		It("returns nil when the entries run short", func() {
			d := denc.NewDecoder([]byte{2, 0, 0, 0, 1, 0, 0, 0, 'a', 1, 0, 0, 0})
			Expect(denc.DecodeMap(d, decString, decU32)).To(BeNil())
			Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		})
	})

	Describe("the attrs map", func() {
		It("encodes sorted keys with bufferlist values", func() {
			e := denc.NewEncoder()
			denc.EncodeStringMap(e, map[string][]byte{"b": {1}, "a": {}})
			want := []byte{
				2, 0, 0, 0,
				1, 0, 0, 0, 'a',
				0, 0, 0, 0,
				1, 0, 0, 0, 'b',
				1, 0, 0, 0, 1,
			}
			Expect(e.Bytes()).To(Equal(want))

			d := denc.NewDecoder(want)
			Expect(denc.DecodeStringMap(d)).To(Equal(map[string][]byte{"a": nil, "b": {1}}))
			Expect(d.Err()).NotTo(HaveOccurred())
		})
	})

	Describe("optionals", func() {
		It("writes a single zero byte for nil", func() {
			e := denc.NewEncoder()
			denc.EncodeOptional(e, nil, encU32)
			Expect(e.Bytes()).To(Equal([]byte{0}))

			d := denc.NewDecoder(e.Bytes())
			Expect(denc.DecodeOptional(d, decU32)).To(BeNil())
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("writes 1 then the value when present", func() {
			v := uint32(7)
			e := denc.NewEncoder()
			denc.EncodeOptional(e, &v, encU32)
			Expect(e.Bytes()).To(Equal([]byte{1, 7, 0, 0, 0}))

			d := denc.NewDecoder(e.Bytes())
			Expect(denc.DecodeOptional(d, decU32)).To(HaveValue(Equal(uint32(7))))
			Expect(d.Err()).NotTo(HaveOccurred())
		})
		It("returns nil when the value runs short", func() {
			d := denc.NewDecoder([]byte{1, 7})
			Expect(denc.DecodeOptional(d, decU32)).To(BeNil())
			Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
		})
	})
})
