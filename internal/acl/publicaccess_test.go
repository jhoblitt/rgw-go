package acl_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// publicAccessBytes writes PublicAccessBlockConfiguration as its encode body
// does (rgw_public_access.h:45-52): ENCODE_START(v, compat) around the four
// flags in declaration order, then whatever a later version appends.
func publicAccessBytes(v, compat uint8, flags [4]bool, appended ...byte) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(v, compat)
	for _, b := range flags {
		e.Bool(b)
	}
	e.Raw(appended)
	e.EndStruct(f)
	return e.Bytes()
}

var _ = Describe("PublicAccessBlock", func() {
	DescribeTable("reads and writes each flag at its place in ENCODE_START(1, 1)",
		func(flags [4]bool, want acl.PublicAccessBlock) {
			b := publicAccessBytes(1, 1, flags)
			Expect(decodeWhole(b, acl.DecodePublicAccessBlock)).To(Equal(want))
			for _, r := range releases {
				Expect(encodeAt(want, r)).To(Equal(b), "release %v", r)
			}
		},
		Entry("BlockPublicAcls first", [4]bool{true, false, false, false},
			acl.PublicAccessBlock{BlockPublicACLs: true}),
		Entry("IgnorePublicAcls second", [4]bool{false, true, false, false},
			acl.PublicAccessBlock{IgnorePublicACLs: true}),
		Entry("BlockPublicPolicy third", [4]bool{false, false, true, false},
			acl.PublicAccessBlock{BlockPublicPolicy: true}),
		Entry("RestrictPublicBuckets fourth", [4]bool{false, false, false, true},
			acl.PublicAccessBlock{RestrictPublicBuckets: true}),
		Entry("two flags together", [4]bool{true, false, true, false},
			acl.PublicAccessBlock{BlockPublicACLs: true, BlockPublicPolicy: true}),
		Entry("the zero value", [4]bool{}, acl.PublicAccessBlock{}),
	)
	It("skips what a later version appends, as DECODE_START(1) does", func() {
		b := publicAccessBytes(2, 1, [4]bool{false, true, false, false}, 0xAA)
		Expect(decodeWhole(b, acl.DecodePublicAccessBlock)).To(Equal(acl.PublicAccessBlock{IgnorePublicACLs: true}))
	})
	It("refuses a compat version above 1", func() {
		d := denc.NewDecoder(publicAccessBytes(2, 2, [4]bool{}))
		acl.DecodePublicAccessBlock(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
})
