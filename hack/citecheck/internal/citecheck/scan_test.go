package citecheck_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// inCodeSpan is why a link a code span holds fails.
const inCodeSpan = "not a link: a code span holds it, which renders it as code: take it out of the backticks"

var _ = Describe("Scan", func() {
	scan := func(md string) []citecheck.Citation { return citecheck.Scan([]byte(md)) }

	It("finds a code span citing a range with a symbol", func() {
		Expect(scan("Read: `src/rgw/driver/rados/rgw_rados.cc:6536-6542@v19.2.6#RGWRados::Object::Read::prepare`.\n")).
			To(Equal([]citecheck.Citation{{
				Line: 1, Text: "src/rgw/driver/rados/rgw_rados.cc:6536-6542@v19.2.6#RGWRados::Object::Read::prepare",
				Form: citecheck.CodeSpan, Ref: "v19.2.6", Path: "src/rgw/driver/rados/rgw_rados.cc", From: 6536, To: 6542,
				Symbol: "RGWRados::Object::Read::prepare", Start: 6, End: 91,
			}}))
	})

	It("finds a code span citing one line, with blanks inside its backticks", func() {
		Expect(scan("See `` src/rgw/rgw_op.cc:42@0123abc ``.\n")).To(Equal([]citecheck.Citation{{
			Line: 1, Text: "src/rgw/rgw_op.cc:42@0123abc", Form: citecheck.CodeSpan, Ref: "0123abc", Path: "src/rgw/rgw_op.cc",
			From: 42, To: 42, Start: 4, End: 38,
		}}))
	})

	It("finds a GitHub link to a range in a markdown link", func() {
		Expect(scan("[prepare](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L10-L20).\n")).
			To(Equal([]citecheck.Citation{{
				Line: 1, Text: "https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L10-L20",
				Ref: "v20.2.4", Path: "src/rgw/rgw_op.cc", From: 10, To: 20,
			}}))
	})

	It("finds a bare GitHub link to one line, ending before the prose's punctuation", func() {
		Expect(scan("At https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7, it fails.\n")).
			To(Equal([]citecheck.Citation{{
				Line: 1, Text: "https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7",
				Ref: "v19.2.6", Path: "src/rgw/rgw_op.cc", From: 7, To: 7,
			}}))
	})

	It("decodes a link's path, and allows a query and the columns of a selection", func() {
		Expect(scan("<https://github.com/ceph/ceph/blob/v19.2.6/qa/suites/%25.yaml?plain=1#L3C2-L5C9>\n")).
			To(Equal([]citecheck.Citation{{
				Line: 1, Text: "https://github.com/ceph/ceph/blob/v19.2.6/qa/suites/%25.yaml?plain=1#L3C2-L5C9",
				Ref: "v19.2.6", Path: "qa/suites/%.yaml", From: 3, To: 5,
			}}))
	})

	It("reports the line each citation starts on, in document order", func() {
		cites := scan("# Plan\n\nFirst `a.cc:1@v1`, then\n`b.cc:2@v1` and\nhttps://github.com/ceph/ceph/blob/v1/c.cc#L3\n\n- `d.cc:5@v1`\n")
		Expect(cites).To(HaveLen(4))
		var at []string
		for _, c := range cites {
			at = append(at, c.Path)
			Expect(c.Line).To(Equal(c.From+2), "line of %s", c.Path)
		}
		Expect(at).To(Equal([]string{"a.cc", "b.cc", "c.cc", "d.cc"}))
	})

	It("locates a code span's bytes, backticks included, past CRLF line endings and multibyte text", func() {
		md := "# Plan\r\n\r\nThe café's `` src/rgw/rgw_op.cc:5@v1 `` and `a.cc:7@v1`.\r\n"
		cites := scan(md)
		Expect(cites).To(HaveLen(2))
		Expect(md[cites[0].Start:cites[0].End]).To(Equal("`` src/rgw/rgw_op.cc:5@v1 ``"))
		Expect(md[cites[1].Start:cites[1].End]).To(Equal("`a.cc:7@v1`"))
		Expect(cites[1].Line).To(Equal(3))
	})

	// span returns the one code span citation md holds.
	span := func(md string) citecheck.Citation {
		var spans []citecheck.Citation
		for _, c := range scan(md) {
			if c.Form == citecheck.CodeSpan {
				spans = append(spans, c)
			}
		}
		Expect(spans).To(HaveLen(1), "code span citations in %q", md)
		return spans[0]
	}

	DescribeTable("a code span citation --fix cannot rewrite where it stands",
		func(md, why string) { Expect(span(md).NoFix).To(Equal(why), "in %q", md) },
		Entry("one that runs across a line break", "A span `\na.cc:1@v1\n` here.\n",
			"the code span runs across a line break, which rewriting it would rewrap: put it on one line"),
		Entry("one that is a link's text", "[`a.cc:1@v1`](https://github.com/ceph/ceph/blob/v1/a.cc#L1)\n",
			"the code span is in a link's text, which cannot hold another link: write the text without it"),
		Entry("one in a link's text among other words, the link on the next line",
			"[see `a.cc:1@v1`\nthere](https://example.com)\n",
			"the code span is in a link's text, which cannot hold another link: write the text without it"),
		Entry("one in a reference link's text", "[`a.cc:1@v1`][ref]\n",
			"the code span is in a link's text, which cannot hold another link: write the text without it"),
		Entry("one in a link's text that holds brackets of its own", "[x [y] `a.cc:1@v1`](https://example.com)\n",
			"the code span is in a link's text, which cannot hold another link: write the text without it"),
		Entry("one whose ref holds a slash", "`a.cc:1@release/v1`\n",
			`ref release/v1 holds a "/", which a link cannot tell from the path after it: cite its commit id`),
		Entry(`one right after a "!"`, "See !`a.cc:1@v1` here.\n",
			`the code span follows a "!", which would make the link an image: escape it as "\!"`),
		Entry(`one in double backticks right after a "!"`, "Bang!`` a.cc:1@v1 ``\n",
			`the code span follows a "!", which would make the link an image: escape it as "\!"`),
		Entry(`one right after a "!" that an escaped backslash precedes`, "\\\\!`a.cc:1@v1`\n",
			`the code span follows a "!", which would make the link an image: escape it as "\!"`),
	)

	DescribeTable("a code span citation --fix can rewrite",
		func(md string) { Expect(span(md).NoFix).To(BeEmpty(), "in %q", md) },
		Entry("one beside a link", "`a.cc:1@v1` ([a.cc:1](https://github.com/ceph/ceph/blob/v1/a.cc#L1))\n"),
		Entry("one in brackets that are not a link's text", "[`a.cc:1@v1`] and [b] (c)\n"),
		Entry("one after a link's text closes", "[x](https://example.com) `a.cc:1@v1`\n"),
		Entry("one after an escaped bracket", "\\[`a.cc:1@v1`](not a link)\n"),
		Entry("one in a table's cell", "| a | `a.cc:1@v1` |\n"),
		Entry(`one after an escaped "!"`, "\\!`a.cc:1@v1`\n"),
		Entry(`one after a "!" and a blank`, "Yes! `a.cc:1@v1`\n"),
		Entry(`one at the start of a line after a line ending in "!"`, "Yes!\n`a.cc:1@v1`\n"),
	)

	It("holds a link outside a code span in each place a code span is near", func() {
		md := "`x` https://github.com/ceph/ceph/blob/v1/a.cc#L1 `y`\n" +
			"a ` stray https://github.com/ceph/ceph/blob/v1/b.cc#L2\n\n" +
			"\\`https://github.com/ceph/ceph/blob/v1/c.cc#L3\\`\n\n" +
			"`spans`\r\n\r\nhttps://github.com/ceph/ceph/blob/v1/d.cc#L4\r\n" +
			"[`https://github.com/ceph/ceph/blob/v1/e.cc#L5`](https://github.com/ceph/ceph/blob/v1/e.cc#L5)\n"
		cites := scan(md)
		var paths []string
		for _, c := range cites {
			paths = append(paths, c.Path)
			Expect(c.Problem).To(BeEmpty(), "problem of %s", c.Text)
		}
		Expect(paths).To(Equal([]string{"a.cc", "b.cc", "c.cc", "d.cc", "e.cc", "e.cc"}))
	})

	It("pairs a code span across a line break within a paragraph", func() {
		cites := scan("A span ``opens\nhere`` and `e.cc:2@v1` follows.\n")
		Expect(cites).To(HaveLen(1))
		Expect(cites[0].Path).To(Equal("e.cc"))
		Expect(cites[0].Line).To(Equal(2))
	})

	It("does not pair a backtick in one list item with the next item's", func() {
		cites := scan("- a stray ` here\n- `f.cc:3@v1`\n")
		Expect(cites).To(HaveLen(1))
		Expect(cites[0].Path).To(Equal("f.cc"))
	})

	DescribeTable("text that is not a citation",
		func(md string) { Expect(scan(md)).To(BeEmpty()) },
		Entry("the inline form in prose, outside a code span", "See src/rgw/rgw_op.cc:42@v19.2.6 there.\n"),
		Entry("the inline form behind escaped backticks", "See \\`src/rgw/rgw_op.cc:42@v19.2.6\\` there.\n"),
		Entry("a code span with more than a citation", "Run `see src/rgw/rgw_op.cc:42@v19.2.6`.\n"),
		Entry("a code span with no ref", "At `src/rgw/rgw_op.cc:42`.\n"),
		Entry("a module version in a code span", "Run `go get github.com/klauspost/compress@v1.19.1`.\n"),
		Entry("a backtick fenced block",
			"```\n`src/rgw/rgw_op.cc:42@v19.2.6`\nhttps://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L42\n```\n"),
		Entry("a tilde fenced block inside a list item, with a longer closing fence",
			"- item\n\n   ~~~go\n   // https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L42\n   ~~~~\n"),
		Entry("a GitHub link with no line anchor", "https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc\n"),
		Entry("a GitHub link with another anchor", "https://github.com/ceph/ceph/blob/v19.2.6/README.md#building\n"),
		Entry("a link to another repository", "https://github.com/jhoblitt/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L42\n"),
	)

	It("scans past a fenced block once it closes", func() {
		cites := scan("```sh\necho `g.cc:1@v1`\n```\n`h.cc:5@v1`\n")
		Expect(cites).To(HaveLen(1))
		Expect(cites[0].Path).To(Equal("h.cc"))
		Expect(cites[0].Line).To(Equal(4))
	})

	It("reads a line of three backticks with backticks after it as a code span, not a fence", func() {
		cites := scan("```x``` and `i.cc:1@v1`\n")
		Expect(cites).To(HaveLen(1))
		Expect(cites[0].Path).To(Equal("i.cc"))
	})

	DescribeTable("a malformed citation",
		func(md, problem string) {
			cites := scan(md)
			Expect(cites).To(HaveLen(1))
			Expect(cites[0].Problem).To(Equal(problem))
		},
		Entry("a link whose second line lacks its L",
			"https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L10-12\n",
			"anchor #L10-12 is not #L<line> or #L<from>-L<to>"),
		Entry("a citation with no path", "`:6716-6727@v20.2.4`\n",
			`the citation has no path: write the file's path from ceph's repository root before ":6716-6727@v20.2.4"`),
		Entry("a citation with a symbol and no path", "`:6059@v19.2.6#RGWGetCORS::execute`\n",
			`the citation has no path: write the file's path from ceph's repository root before ":6059@v19.2.6#RGWGetCORS::execute"`),
		Entry("a symbol that is not a C++ name", "`src/rgw/rgw_op.cc:1@v19.2.6#RGWOp::execute()`\n",
			`symbol "RGWOp::execute()" is not a C++ name`),
		Entry("an empty symbol", "`src/rgw/rgw_op.cc:1@v19.2.6#`\n", `symbol "" is not a C++ name`),
		Entry("a line number too large to read", "`src/rgw/rgw_op.cc:99999999999999999999@v19.2.6`\n",
			"line 99999999999999999999 is out of range"),
		Entry("a link path whose percent escape is broken",
			"https://github.com/ceph/ceph/blob/v19.2.6/src/%zz.cc#L1\n", `path "src/%zz.cc" is not percent-encoded correctly`),
		Entry("a link a code span holds", "See `https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L42` there.\n",
			inCodeSpan),
		Entry("a markdown link a code span holds",
			"(`[:748-752](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L748-L752)`).\n", inCodeSpan),
		Entry("a malformed link a code span holds", "`https://github.com/ceph/ceph/blob/v1/a.cc#L10-12`\n", inCodeSpan),
		Entry("a link on the second line of a code span, past CRLF line endings",
			"# Plan\r\n\r\nA span `see\r\nhttps://github.com/ceph/ceph/blob/v1/a.cc#L1` here.\r\n", inCodeSpan),
	)

	It("accepts a destructor and an operator as a symbol", func() {
		cites := scan("`a.cc:1@v1#RGWRados::~RGWRados` `b.cc:1@v1#rgw_obj_key::operator==`\n")
		Expect(cites).To(HaveLen(2))
		Expect(cites[0].Problem).To(BeEmpty())
		Expect(cites[1].Problem).To(BeEmpty())
	})
})
