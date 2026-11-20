package policy

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Amazon S3's wildcard matching", func() {
	DescribeTable("amazonLike reads * and ? alone, and every other character as itself",
		func(v, pattern string, want bool) {
			Expect(amazonLike(v, pattern)).To(Equal(want))
		},
		Entry("an opening bracket, matched by itself", "[a]", "[a]", true),
		Entry("a bracket expression, which matches no letter", "a", "[a]", false),
		Entry("a closing bracket alone", "]", "]", true),
		Entry("a backslash, matched by itself", `\a`, `\a`, true),
		Entry("a backslash, which escapes nothing", "a", `\a`, false),
		Entry("a backslash before a star, which still matches any run", `a\b`, `a\*`, true),
		Entry("a backslash before a star, against the star alone", "a*", `a\*`, false),
		Entry("a trailing star over more characters", "abc", "a*", true),
		Entry("a trailing star over none", "a", "a*", true),
		Entry("two empty strings", "", "", true),
		Entry("an empty value under a star", "", "*", true),
		Entry("an empty value under a question mark", "", "?", false),
		Entry("a value under an empty pattern", "x", "", false),
		Entry("a question mark over a multibyte character", "é", "?", true),
		Entry("a question mark over a multibyte character and a literal", "éa", "?a", true),
		Entry("two question marks over one multibyte character", "é", "??", false),
		Entry("a star that must give back what it took", "abcbc", "a*bc", true),
		Entry("a question mark where the value is too short", "ab", "a?c", false),
		Entry("a literal multibyte character", "aéb", "a*b", true),
	)

	DescribeTable("amazonLikeUnits reads a ? as one UTF-16 unit, and agrees with amazonLike within the Basic Multilingual Plane",
		func(v, pattern string, units, points bool) {
			Expect(amazonLikeUnits(v, pattern)).To(Equal(units))
			Expect(amazonLike(v, pattern)).To(Equal(points))
		},
		Entry("a question mark over a character of two units", "\U0001F600", "?", false, true),
		Entry("two question marks over a character of two units", "\U0001F600", "??", true, false),
		Entry("a question mark and a star over a character of two units", "\U0001F600", "?*", true, true),
		Entry("a question mark, a star and a question mark over a character of two units", "\U0001F600", "?*?", true, false),
		Entry("a character of two units, matched by itself", "a\U0001F600b", "a\U0001F600b", true, true),
		Entry("a character of two units under a star", "a\U0001F600b", "a*b", true, true),
		Entry("a character of two units against another that shares its first unit", "\U0001F600", "\U0001F601", false, false),
		Entry("a question mark over a multibyte character of one unit", "é", "?", true, true),
		Entry("two question marks over a multibyte character of one unit", "é", "??", false, false),
		Entry("a bracket expression, which matches no letter", "a", "[a]", false, false),
		Entry("a star that must give back what it took", "abcbc", "a*bc", true, true),
		Entry("two empty strings", "", "", true, true),
		Entry("an empty value under a question mark", "", "?", false, false),
		Entry("a byte that is not UTF-8, matched by itself", "a\xffb", "a\xffb", true, true),
		Entry("a byte that is not UTF-8 against another", "a\xffb", "a\xfeb", false, false),
		Entry("a byte that is not UTF-8 against the replacement character", "a\xffb", "a�b", false, false),
	)

	DescribeTable("amazonARNLike matches each of the six colon-delimited components alone",
		func(v, pattern string, want bool) {
			Expect(amazonARNLike(v, pattern)).To(Equal(want))
		},
		Entry("a wildcard in the resource", "arn:aws:s3:::b/k", "arn:aws:s3:::b/*", true),
		Entry("a resource holding colons", "arn:aws:s3:::b/k:x", "arn:aws:s3:::b/*", true),
		Entry("a wildcard for a whole component", "arn:aws:s3:::b", "arn:aws:*:::b", true),
		Entry("a wildcard that would reach into the next component", "arn:aws:s3:x::b", "arn:aws:*:::b", false),
		Entry("a pattern of fewer components", "arn:aws:s3:::b", "arn:aws:s3:*", false),
		Entry("a value of fewer components", "b/k", "arn:aws:s3:::b/*", false),
		Entry("a bracket expression, matched by itself", "arn:aws:s3:::[b]/k", "arn:aws:s3:::[b]/*", true),
		Entry("a bracket expression, which matches no letter", "arn:aws:s3:::b/k", "arn:aws:s3:::[b]/*", false),
	)
})
