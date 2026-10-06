package rgwtext_test

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

var _ = Describe("the C library's integer readers", func() {
	DescribeTable("Strtoll is strtoll in base 10",
		func(in string, want int64, wantEnd int, wantERange bool) {
			v, end, erange := rgwtext.Strtoll(in)
			Expect([]any{v, end, erange}).To(Equal([]any{want, wantEnd, wantERange}), "%q", in)
		},
		Entry("digits", "604800", int64(604800), 6, false),
		Entry("digits then text", "300abc", int64(300), 3, false),
		Entry("the C locale's white space and a sign", "\t\n\v\f\r -5", int64(-5), 8, false),
		Entry("a plus sign", "+7", int64(7), 2, false),
		Entry("no digits", "abc", int64(0), 0, false),
		Entry("a sign alone", "-", int64(0), 0, false),
		Entry("white space alone", "  ", int64(0), 0, false),
		Entry("empty", "", int64(0), 0, false),
		Entry("a NUL ends the digits", "12\x003", int64(12), 2, false),
		Entry("LLONG_MAX", "9223372036854775807", int64(math.MaxInt64), 19, false),
		Entry("LLONG_MIN", "-9223372036854775808", int64(math.MinInt64), 20, false),
		Entry("past LLONG_MAX, saturated", "9223372036854775808", int64(math.MaxInt64), 19, true),
		Entry("past LLONG_MIN, saturated", "-9223372036854775809", int64(math.MinInt64), 20, true),
		Entry("past ULLONG_MAX, saturated", "99999999999999999999", int64(math.MaxInt64), 20, true),
		Entry("past ULLONG_MAX negated, saturated", "-99999999999999999999", int64(math.MinInt64), 21, true),
	)
	DescribeTable("Strtoull is strtoull in base 10",
		func(in string, want uint64, wantEnd int, wantERange bool) {
			v, end, erange := rgwtext.Strtoull(in)
			Expect([]any{v, end, erange}).To(Equal([]any{want, wantEnd, wantERange}), "%q", in)
		},
		Entry("digits", "24", uint64(24), 2, false),
		Entry("ULLONG_MAX", "18446744073709551615", uint64(math.MaxUint64), 20, false),
		Entry("past ULLONG_MAX, saturated", "18446744073709551616", uint64(math.MaxUint64), 20, true),
		Entry("a minus negates", "-1", uint64(math.MaxUint64), 2, false),
		Entry("a minus negates ULLONG_MAX to 1", "-18446744073709551615", uint64(1), 21, false),
		Entry("a negative overflow saturates", "-18446744073709551616", uint64(math.MaxUint64), 21, true),
		Entry("no digits", " -x", uint64(0), 0, false),
	)
	DescribeTable("Atoll is atoll",
		func(in string, want int64) { Expect(rgwtext.Atoll(in)).To(Equal(want), "%q", in) },
		Entry("digits", "604800", int64(604800)),
		Entry("trailing junk", "300abc", int64(300)),
		Entry("leading space and sign", "  -5", int64(-5)),
		Entry("no digits", "abc", int64(0)),
		Entry("empty", "", int64(0)),
		Entry("overflow saturates as strtoll does", "99999999999999999999", int64(math.MaxInt64)),
		Entry("negative overflow saturates", "-99999999999999999999", int64(math.MinInt64)),
	)
	DescribeTable("ScanInt is sscanf's %d",
		func(in string, want int32, wantRest string, wantOK bool) {
			v, rest, ok := rgwtext.ScanInt(in)
			Expect(ok).To(Equal(wantOK), "%q", in)
			if wantOK {
				Expect([]any{v, rest}).To(Equal([]any{want, wantRest}), "%q", in)
			}
		},
		Entry("digits and the rest", "12.5", int32(12), ".5", true),
		Entry("white space and a sign", " -12x", int32(-12), "x", true),
		Entry("past an int, truncated", "4294967297", int32(1), "", true),
		Entry("past a long, saturated then truncated", "99999999999999999999", int32(-1), "", true),
		Entry("a sign alone", "-", int32(0), "", false),
		Entry("no digits", ".5", int32(0), "", false),
	)
})

var _ = Describe("radosgw's integer readers", func() {
	DescribeTable("StringToLL is stringtoll and StringToL stringtol",
		func(in string, want int64, wantOK bool) {
			v, ok := rgwtext.StringToLL(in)
			Expect([]any{v, ok}).To(Equal([]any{want, wantOK}), "%q", in)
			v32, ok := rgwtext.StringToL(in)
			Expect([]any{v32, ok}).To(Equal([]any{int32(want), wantOK}), "%q", in) //nolint:gosec // stringtol's cast
		},
		Entry("digits", "-12", int64(-12), true),
		Entry("empty is 0", "", int64(0), true),
		Entry("white space alone", " ", int64(0), false),
		Entry("trailing text", "12x", int64(0), false),
		Entry("trailing white space", "12 ", int64(0), false),
		Entry("text after a NUL is not read", "5\x00x", int64(5), true),
		Entry("LLONG_MAX refused", "9223372036854775807", int64(0), false),
		Entry("an overflow, LLONG_MAX, refused", "9223372036854775808", int64(0), false),
		Entry("LLONG_MIN kept", "-9223372036854775808", int64(math.MinInt64), true),
		Entry("an underflow, LLONG_MIN, kept", "-9223372036854775809", int64(math.MinInt64), true),
		Entry("past an int32, truncated by stringtol", "4294967297", int64(4294967297), true),
	)
	DescribeTable("StringToULL is stringtoull and StringToUL stringtoul",
		func(in string, want uint64, wantOK bool) {
			v, ok := rgwtext.StringToULL(in)
			Expect([]any{v, ok}).To(Equal([]any{want, wantOK}), "%q", in)
			v32, ok := rgwtext.StringToUL(in)
			Expect([]any{v32, ok}).To(Equal([]any{uint32(want), wantOK}), "%q", in) //nolint:gosec // stringtoul's cast
		},
		Entry("digits", "12", uint64(12), true),
		Entry("empty is 0", "", uint64(0), true),
		Entry("a sign alone", "+", uint64(0), false),
		Entry("-1, negated to ULLONG_MAX, refused", "-1", uint64(0), false),
		Entry("-2, negated below ULLONG_MAX", "-2", uint64(math.MaxUint64-1), true),
		Entry("ULLONG_MAX refused", "18446744073709551615", uint64(0), false),
		Entry("an overflow, ULLONG_MAX, refused", "18446744073709551616", uint64(0), false),
		Entry("past a uint32, truncated by stringtoul", "4294967296", uint64(4294967296), true),
		Entry("text after a NUL is not read", "7\x00x", uint64(7), true),
	)
	DescribeTable("StrictStrtoll is strict_strtoll in base 10",
		func(in string, want int64, wantOK bool) {
			v, ok := rgwtext.StrictStrtoll(in)
			Expect([]any{v, ok}).To(Equal([]any{want, wantOK}), "%q", in)
		},
		Entry("digits after white space and a sign", " +1024", int64(1024), true),
		Entry("LLONG_MAX kept", "9223372036854775807", int64(math.MaxInt64), true),
		Entry("LLONG_MIN kept", "-9223372036854775808", int64(math.MinInt64), true),
		Entry("an overflow refused", "9223372036854775808", int64(0), false),
		Entry("an underflow refused", "-9223372036854775809", int64(0), false),
		Entry("empty refused", "", int64(0), false),
		Entry("trailing white space refused", "1 ", int64(0), false),
		Entry("a NUL is a byte after the digits", "1\x002", int64(0), false),
	)
})
