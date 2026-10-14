package citecheck_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

var _ = Describe("LinkFor", func() {
	span := func(path string, from, to int) citecheck.Citation {
		return citecheck.Citation{
			Line: 1, Text: "a span", Form: citecheck.CodeSpan, Ref: "v19.2.7", Path: path,
			From: from, To: to, Symbol: "RGWOp::execute",
		}
	}

	DescribeTable("the link that replaces a code span citation",
		func(path string, from, to int, want string) {
			Expect(citecheck.LinkFor(span(path, from, to))).To(Equal(want))
		},
		Entry("a range, its symbol dropped and no title given", "src/rgw/rgw_op.cc", 5158, 5163,
			"[rgw_op.cc:5158-5163](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5158-L5163)"),
		Entry("one line", "src/rgw/rgw_op.cc", 42, 42,
			"[rgw_op.cc:42](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L42)"),
		Entry("a file at the repository's root", "CMakeLists.txt", 1, 3,
			"[CMakeLists.txt:1-3](https://github.com/ceph/ceph/blob/v19.2.7/CMakeLists.txt#L1-L3)"),
		Entry("a path GitHub needs percent-encoded, a plus kept", "qa/suites/rgw+x/%", 1, 1,
			"[%:1](https://github.com/ceph/ceph/blob/v19.2.7/qa/suites/rgw+x/%25#L1)"),
		Entry("a pipe, which would end a table's cell", "src/a|b.cc", 1, 1,
			`[a\|b.cc:1](https://github.com/ceph/ceph/blob/v19.2.7/src/a%7Cb.cc#L1)`),
		Entry("underscores that would emphasize, and one between letters that cannot", "src/pybind/mgr/__init_x__.py", 2, 2,
			`[\_\_init_x\_\_.py:2](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/__init_x__.py#L2)`),
		Entry("a backslash, a star and a tilde", `src/a\b*c~d.cc`, 1, 1,
			`[a\\b\*c\~d.cc:1](https://github.com/ceph/ceph/blob/v19.2.7/src/a%5Cb%2Ac~d.cc#L1)`),
	)

	It("refuses a span whose link would read back as another citation", func() {
		c := span("src/rgw/rgw_op.cc", 1, 1)
		c.Ref = "release/v1"
		_, err := citecheck.LinkFor(c)
		Expect(err).To(MatchError("the link [rgw_op.cc:1](https://github.com/ceph/ceph/blob/release/v1/src/rgw/rgw_op.cc#L1) " +
			"written for a span reads back as another citation"))
	})
})
