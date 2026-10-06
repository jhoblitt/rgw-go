package op_test

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("ParseValueAndBound", func() {
	DescribeTable("parses and bounds a list limit as radosgw's max-keys does",
		func(input string, want int) {
			Expect(op.ParseValueAndBound(input, 0, 1000, 1000)).To(Equal(want), "input %q", input)
		},
		Entry("a plain number", "7", 7),
		Entry("white space either side", " 7 ", 7),
		Entry("a plus sign", "+7", 7),
		Entry("zero, which clients send to probe for an empty bucket", "0", 0),
		Entry("above the upper bound", "5000", 1000),
		Entry("below the lower bound", "-3", 0),
		Entry("2^31, negative once held in a C int", "2147483648", 0),
		Entry("2^32+1, one once held in a C int", "4294967297", 1),
		Entry("the C locale's six white-space bytes on either side", "\t\n\v\f\r 7\t\n\v\f\r ", 7),
		Entry("bytes after a NUL, which ends the C string", "7\x00x", 7),
		Entry("an empty value, which is also an absent one", "", 1000),
	)
	DescribeTable("saturates at the long limits as strtol does, then keeps the low 32 bits",
		func(input string, want int) {
			Expect(op.ParseValueAndBound(input, -10, 10, 0)).To(Equal(want), "input %q", input)
		},
		Entry("above LONG_MAX, whose low 32 bits are -1", "99999999999999999999", -1),
		Entry("below LONG_MIN, whose low 32 bits are 0", "-99999999999999999999", 0),
		Entry("LONG_MAX itself, whose low 32 bits are -1", "9223372036854775807", -1),
		Entry("LONG_MIN itself, whose low 32 bits are 0", "-9223372036854775808", 0),
	)
	It("holds the default in a C int too", func() {
		Expect(op.ParseValueAndBound("", 0, 1000, 1<<32+7)).To(Equal(7))
	})
	It("holds the bounds in a C int too", func() {
		Expect(op.ParseValueAndBound("5", math.MinInt64, -(1<<32)+3, 0)).To(Equal(3), "an upper bound below INT_MIN, whose low 32 bits are 3")
		Expect(op.ParseValueAndBound("5", 1<<32+3, math.MaxInt64, 0)).To(Equal(3), "a lower bound above INT_MAX, whose low 32 bits are 3")
	})
	DescribeTable("refuses input with no number, or with more than white space after it",
		func(input string) {
			_, err := op.ParseValueAndBound(input, 0, 1000, 1000)
			Expect(err).To(MatchError(op.ErrInvalidArgument), "input %q", input)
		},
		Entry("trailing garbage", "7x"),
		Entry("garbage after trailing white space", "7 x"),
		Entry("a trailing U+00A0 no-break space, which the C locale does not count as white space", "7\u00a0"),
		Entry("a trailing U+0085 next line, which the C locale does not count either", "7\u0085"),
		Entry("no digits", "x"),
		Entry("white space alone, which is not empty", "   "),
		Entry("a sign alone", "-"),
		Entry("white space between the sign and the digits", "+ 7"),
		Entry("a NUL first, which leaves the C string empty", "\x00"),
		Entry("a hexadecimal prefix, as base 10 stops at the x", "0x10"),
	)
})
