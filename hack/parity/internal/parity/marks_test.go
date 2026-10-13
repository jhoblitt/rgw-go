package parity_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

var _ = Describe("a @pytest.mark line an additive move adds", func() {
	DescribeTable("is admitted as the mark's name, then spaces and a comment at most",
		func(line string) {
			Expect(parity.MarkLine(line)).To(BeTrue())
		},
		Entry("a name alone", "@pytest.mark.fails_on_rgw"),
		Entry("a name and a comment, as the suite writes them", "@pytest.mark.fails_on_aws # only supported for directory buckets"),
		Entry("a name and spaces", "@pytest.mark.slow   "),
	)

	DescribeTable("is refused with anything more",
		func(line string) {
			Expect(parity.MarkLine(line)).To(BeFalse())
		},
		Entry("empty parentheses", "@pytest.mark.slow()"),
		Entry("a tab before the comment", "@pytest.mark.slow\t# why"),
		Entry("a tab in the comment", "@pytest.mark.slow  # a\tb"),
		Entry("a string", "@pytest.mark.x('a')"),
		Entry("a prefixed string", "@pytest.mark.x(rb'a')"),
		Entry("a unicode string", "@pytest.mark.x(u'a')"),
		Entry("an f-string", "@pytest.mark.x(f'{1}')"),
		Entry("an escape before the closing quote", "@pytest.mark.x('a\\')"),
		Entry("a number", "@pytest.mark.x(1)"),
		Entry("a constant", "@pytest.mark.x(True)"),
		Entry("a list", "@pytest.mark.x([1, 2])"),
		Entry("a keyword", "@pytest.mark.skipif(True, reason='r')"),
		Entry("implicit concatenation", "@pytest.mark.x('a' 'b')"),
		Entry("a walrus in brackets", "@pytest.mark.x([y := 1])"),
		Entry("a lambda in brackets", "@pytest.mark.x([lambda: 1])"),
		Entry("a bracket closing the call early", "@pytest.mark.x(1)(print(2))"),
		Entry("a line continued by a backslash", "@pytest.mark.x \\"),
		Entry("a comment continued by a backslash", "@pytest.mark.x  # c \\"),
		Entry("a semicolon", "@pytest.mark.x; pass"),
		Entry("a second decorator on the line", "@pytest.mark.x @pytest.mark.y"),
		Entry("an attribute after the mark", "@pytest.mark.x.y"),
		Entry("a full-width letter", "@pytest.mark.\uff58"),
		Entry("a full-width letter after an ASCII one", "@pytest.mark.x\uff58"),
		Entry("a non-breaking space", "@pytest.mark.x\u00a0"),
		Entry("a comment that is not ASCII", "@pytest.mark.x  # caf\u00e9"),
		Entry("a carriage return in the comment", "@pytest.mark.x  # c\r@print(1) or pytest.mark.x"),
		Entry("a line separator in the comment", "@pytest.mark.x  # c\u2028x"),
		Entry("a form feed", "@pytest.mark.x\f"),
		Entry("a mark under another name", "@mark.x"),
	)
})

var _ = Describe("the markers a pytest.ini registers", func() {
	It("are the names in [pytest]'s markers, on the key's line and the indented lines after it", func() {
		ini := "[pytest]\n" +
			"markers = first\n" +
			"    second: a description\n" +
			"    third(arg): a description\n" +
			"    not a name: a description\n" +
			"addopts = -x\n" +
			"    -v\n" +
			"[tool:pytest]\n" +
			"markers =\n" +
			"    elsewhere\n"
		Expect(parity.IniMarkers(ini)).To(Equal(map[string]bool{"first": true, "second": true, "third": true}))
	})
})
