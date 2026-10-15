package citecheck_test

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

const (
	// remedy ends the line a failing plain citation of a file of ceph's prints.
	remedy    = notLinkTail + "\n"
	gofixPath = "src/rgw/rgw_gofix.cc"
	// prepareS and prepareT link RGWGoFix::prepare at each release, and
	// completeS and completeT RGWGoFix::complete.
	prepareS  = "https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_gofix.cc#L4-L11"
	prepareT  = "https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_gofix.cc#L14-L21"
	completeS = "https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_gofix.cc#L13-L28"
	completeT = "https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_gofix.cc#L23-L38"
)

var _ = Describe("Go comments", func() {
	var (
		ceph           *cephFixture
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		ceph = newGoFixture()
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	run := func(ctx SpecContext, args ...string) int {
		return citecheck.ExitStatus(citecheck.Run(ctx, args, nil, stdout, stderr))
	}

	read := func(name string) string {
		data, err := os.ReadFile(name)
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}

	// fixGo runs --fix on a Go file holding src and returns the file's
	// path.
	fixGo := func(ctx SpecContext, src string) (string, int) {
		f := markdown("fix.go", src)
		return f, run(ctx, "--fix", "--ceph", ceph.dir, f)
	}

	Describe("--fix resolves each plain citation of ceph's code and rewrites it into the link", func() {
		DescribeTable("to the lines at the ref its rules give",
			func(ctx SpecContext, comment string, links ...string) {
				f, status := fixGo(ctx, "package fix\n\n"+comment+"func prepare() {}\n")
				Expect(stdout.String()).To(HaveSuffix(", failed: 0, rewritten: %d\n", len(links)))
				Expect(status).To(Equal(0))
				for _, l := range links {
					Expect(read(f)).To(ContainSubstring(l))
				}
			},
			Entry(`"at <tag>" after it`,
				"// prepare is RGWGoFix::prepare (rgw_gofix.cc:4-11 at v19.2.6).\n", prepareS),
			Entry(`"at <tag>" on the comment's next line`,
				"// prepare is RGWGoFix::prepare (rgw_gofix.cc:14-21 at\n// v20.2.4).\n", prepareT),
			Entry(`"at <tag>" after the citations listed with it`,
				"// prepare and complete are rgw_gofix.cc:4-11 and :13-28 at v19.2.6.\n", prepareS, completeS),
			Entry(`", at <tag>" closing the citations listed before it`,
				"// prepare and complete (rgw_gofix.cc:4-11, :13-28, at v19.2.6).\n", prepareS, completeS),
			Entry(`the one release the words before the "(" it opens name`,
				"// radosgw at v20.2.4 prepares (rgw_gofix.cc:14-21).\n", prepareT),
			Entry(`a release's codename before the "(" it opens`,
				"// Tentacle's prepare (rgw_gofix.cc:14-21).\n", prepareT),
			Entry(`"at both tags", v19.2.6, when the lines are the same at each`,
				"// twin_alpha (driver/rados/rgw_twin.cc:2 at both tags).\n",
				cephURL("v19.2.6", "src/rgw/driver/rados/rgw_twin.cc", "L2")),
			Entry("each its own tag",
				"// complete is RGWGoFix::complete (rgw_gofix.cc:13-28 at v19.2.6, :23-38 at\n// v20.2.4).\n",
				completeS, completeT),
			Entry(`its own tag right before it, which an "at <tag>" after a citation listed with it does not override`,
				"// prepare is at v19.2.6 rgw_gofix.cc:4-11 and rgw_gofix.cc:14-21 at v20.2.4.\n", prepareS, prepareT),
			Entry(`"at <tag>" closing a parenthesis, past the releases before its "(", which are not its first citation's own`,
				"// twin_alpha at v19.2.6 and v20.2.4 (driver/rados/rgw_twin.cc:2, :2 at both tags).\n",
				cephURL("v19.2.6", "src/rgw/driver/rados/rgw_twin.cc", "L2"),
				cephURL("v19.2.6", "src/rgw/driver/rados/rgw_twin.cc", "L2")),
			Entry("a [T] marker on it", "// prepare is RGWGoFix::prepare (rgw_gofix.cc [T]:14-21).\n", prepareT),
			Entry("an [S] marker on it, and a [T] one on the continuation after it",
				"// prepare is RGWGoFix::prepare (rgw_gofix.cc [S]:4-11, [T]:14-21).\n", prepareS, prepareT),
			Entry("a tag before it in its clause",
				"// prepare is RGWGoFix::prepare (rgw_gofix.cc:4-11 at v19.2.6; v20.2.4 :14-21).\n",
				prepareS, prepareT),
			Entry("the ref of the citation before it in its clause, for a continuation",
				"// prepare and complete are [T] rgw_gofix.cc:14-21, :23-38.\n", prepareT, completeT),
			Entry("a header convention naming the tag and the file",
				"// A bare line number is v19.2.6's rgw_gofix.cc.\n\n// prepare is RGWGoFix::prepare (:4-11).\n", prepareS),
			Entry("a header convention naming the file before the tag",
				"// A bare line number is rgw_gofix.cc's at v19.2.6.\n\n// prepare is RGWGoFix::prepare (:4-11).\n", prepareS),
			Entry("a header convention naming a file's bare lines",
				"// A bare rgw_gofix.cc line is v19.2.6's.\n\n// prepare is RGWGoFix::prepare (rgw_gofix.cc:4-11).\n", prepareS),
			Entry(`a header convention, past an "in main" after it, which names no branch`,
				"// A bare rgw_gofix.cc line is v19.2.6's.\n\n// prepare is RGWGoFix::prepare (rgw_gofix.cc:4-11 in main).\n", prepareS),
			Entry("a header convention naming the tag, the file its paragraph names",
				"// The fixture is RGWGoFix (rgw_gofix.cc). A bare line number below is\n// v19.2.6's.\n\n"+
					"// prepare is RGWGoFix::prepare (:4-11; v20.2.4 :14-21).\n", prepareS, prepareT),
		)

		DescribeTable("to the path its file's name has at the ref",
			func(ctx SpecContext, comment, link string) {
				f, status := fixGo(ctx, "package fix\n\n"+comment+"func prepare() {}\n")
				Expect(stdout.String()).To(Equal("citations checked: 1, failed: 0, rewritten: 1\n"))
				Expect(status).To(Equal(0))
				Expect(read(f)).To(ContainSubstring(link))
			},
			Entry("the one path a base name has", "// prepare (rgw_gofix.cc:6 at v19.2.6).\n",
				cephURL("v19.2.6", gofixPath, "L6")),
			Entry("the one path a partial path has", "// twin_alpha (driver/rados/rgw_twin.cc:2 at v19.2.6).\n",
				cephURL("v19.2.6", "src/rgw/driver/rados/rgw_twin.cc", "L2")),
			Entry("of two under src/rgw/, the one whose lines hold the identifier named beside it",
				"// twin_beta (rgw_twin.cc:2 at v19.2.6).\n", cephURL("v19.2.6", "src/rgw/services/rgw_twin.cc", "L2")),
			Entry("of one under src/rgw/ and one elsewhere, the one under src/rgw/",
				"// the side (rgw_side.cc:2 at v19.2.6).\n", cephURL("v19.2.6", "src/rgw/rgw_side.cc", "L2")),
		)

		DescribeTable("a continuation to the file of the citation or file name nearest before it in its comment",
			func(ctx SpecContext, comment string, links ...string) {
				f, status := fixGo(ctx, "package fix\n\n"+comment+"func prepare() {}\n")
				Expect(stdout.String()).To(HaveSuffix(", failed: 0, rewritten: %d\n", len(links)))
				Expect(status).To(Equal(0))
				for _, l := range links {
					Expect(read(f)).To(ContainSubstring(l))
				}
			},
			Entry("a chain through the comment's paragraphs",
				"// complete is RGWGoFix::complete (rgw_gofix.cc:13-28 at v19.2.6, :23-38 at\n// v20.2.4).\n//\n"+
					"// It tries max_retries times (:15-17 at v19.2.6; v20.2.4 :25-27).\n",
				completeS, completeT, cephURL("v19.2.6", gofixPath, "L15-L17"), cephURL("v20.2.4", gofixPath, "L25-L27")),
			Entry("a file named without lines, on the line before",
				"// prepare is RGWGoFix::prepare in rgw_gofix.cc\n// :4-11 at v19.2.6.\n", prepareS),
			Entry("a file named right before it, in a clause that names another file too",
				"// the side (rgw_side.cc:2 at v19.2.6, and rgw_gofix.cc\n// :4-11 at v19.2.6).\n",
				cephURL("v19.2.6", "src/rgw/rgw_side.cc", "L2"), prepareS),
			Entry("the release right before it, in a clause that names another release too",
				"// prepare in rgw_gofix.cc (the code at v19.2.6, and v20.2.4 :14-21).\n", prepareT),
			Entry("past a nearer file by a strong identifier, to the file a header gives the ref",
				"// A bare line number is v19.2.6's rgw_gofix.cc.\n\n// complete retries write_meta, in rgw_gofix.cc, after twin_alpha\n"+
					"// (driver/rados/rgw_twin.cc:2 at v19.2.6) and write_meta again (:18).\n",
				cephURL("v19.2.6", "src/rgw/driver/rados/rgw_twin.cc", "L2"), cephURL("v19.2.6", gofixPath, "L18")),
			Entry("past a nearer file whose cited lines do not hold the identifier named beside it",
				"// RGWGoFix::prepare (rgw_gofix.cc:4-11 at v19.2.6) calls twin_alpha (rgw_twin.cc\n"+
					"// :2 at v19.2.6), and complete retries write_meta (:18 at v19.2.6).\n",
				prepareS, cephURL("v19.2.6", "src/rgw/driver/rados/rgw_twin.cc", "L2"), cephURL("v19.2.6", gofixPath, "L18")),
		)

		DescribeTable("and drops the [S] or [T] that gave the citations their ref once each is a link",
			func(ctx SpecContext, comment string, links ...string) {
				f, status := fixGo(ctx, "package fix\n\n"+comment+"func prepare() {}\n")
				Expect(stdout.String()).To(HaveSuffix(", failed: 0, rewritten: %d\n", len(links)))
				Expect(status).To(Equal(0))
				for _, l := range links {
					Expect(read(f)).To(ContainSubstring(l))
				}
				Expect(read(f)).NotTo(ContainSubstring("[T]"))
			},
			Entry("a marker before a continuation",
				"// prepare (rgw_gofix.cc:4-11 at v19.2.6; [T] :14-21) opens the pool.\n", prepareS, prepareT),
			Entry("a marker before a file name", "// prepare is [T] rgw_gofix.cc:14-21.\n", prepareT),
			Entry("a marker before a list", "// prepare and complete are [T] rgw_gofix.cc:14-21, :23-38.\n", prepareT, completeT),
			Entry(`a marker before the "(" a citation opens, which stays`, "// The fixture is [T] (rgw_gofix.cc:14-21).\n",
				"("+prepareT+")"),
		)

		It("keeps the marker of a list when a file listed after its citation stays plain", func(ctx SpecContext) {
			f, status := fixGo(ctx, "package fix\n\n// prepare is [T] rgw_gofix.cc:14-21, rgw_side.cc:2.\nfunc prepare() {}\n")
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(HaveSuffix("citations checked: 2, failed: 1, rewritten: 1\n"))
			Expect(read(f)).To(And(ContainSubstring("[T]"), ContainSubstring(prepareT), ContainSubstring("rgw_side.cc:2")))
		})

		It("keeps the marker of a list one of whose citations stays plain", func(ctx SpecContext) {
			f, status := fixGo(ctx, "package fix\n\n// prepare and more are [T] rgw_gofix.cc:14-21, :40-50.\nfunc prepare() {}\n")
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(HaveSuffix("citations checked: 2, failed: 1, rewritten: 1\n"))
			Expect(read(f)).To(And(ContainSubstring("[T]"), ContainSubstring(prepareT), ContainSubstring(":40-50")))
		})

		It("takes a content match from the header of a block enclosing the cited lines", func(ctx SpecContext) {
			f, status := fixGo(ctx, "package fix\n\n// The failure is logged as RGWGoFix::complete ends (rgw_gofix.cc:24-26\n"+
				"// at v19.2.6).\nfunc prepare() {}\n")
			Expect(stdout.String()).To(Equal("citations checked: 1, failed: 0, rewritten: 1\n"))
			Expect(status).To(Equal(0))
			Expect(read(f)).To(ContainSubstring(cephURL("v19.2.6", gofixPath, "L24-L26")))
		})

		It("does not take an identifier the Go file's own code writes for one ceph's lines must hold", func(ctx SpecContext) {
			f, status := fixGo(ctx, "package fix\n\nimport \"strings\"\n\n"+
				"// openPool opens the pool as prepare does (rgw_gofix.cc:4-11 at v19.2.6),\n"+
				"// splitting with strings.SplitSeq.\nfunc openPool() { _ = strings.SplitSeq }\n")
			Expect(stdout.String()).To(Equal("citations checked: 1, failed: 0, rewritten: 1\n"))
			Expect(status).To(Equal(0))
			Expect(read(f)).To(ContainSubstring(prepareS))
		})
	})

	DescribeTable("--fix leaves a plain citation it cannot resolve as it is and lists why",
		func(ctx SpecContext, comment string, line int, text, why string) {
			src := "package fix\n\n" + comment + "func prepare() {}\n"
			f, status := fixGo(ctx, src)
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(HavePrefix(fmt.Sprintf("%s:%d: %s: unresolved: %s\n", f, line, text,
				strings.ReplaceAll(why, "CEPH", ceph.dir))))
			Expect(read(f)).To(Equal(src))
		},
		Entry("no ref", "// prepare is RGWGoFix::prepare (rgw_gofix.cc:4-11).\n", 3, "rgw_gofix.cc:4-11",
			`no ref: no "at <tag>" follows it, no [S] or [T] marks it, and no comment above says what a bare line number means`),
		Entry("a ref the clone does not have", "// prepare (rgw_gofix.cc:4-11 at v9.9.9).\n", 3, "rgw_gofix.cc:4-11",
			"ref v9.9.9 names no tag or commit in CEPH"),
		Entry("a base name two files under src/rgw/ have, and no identifier to tell them apart",
			"// a twin (rgw_twin.cc:2 at v19.2.6).\n", 3, "rgw_twin.cc:2",
			"rgw_twin.cc is any of src/rgw/driver/rados/rgw_twin.cc, src/rgw/services/rgw_twin.cc at v19.2.6, "+
				"and no identifier beside it tells which"),
		Entry("a file not at its ref", "// fresh (rgw_new.cc:2 at v19.2.6).\n", 3, "rgw_new.cc:2", "rgw_new.cc is no file at v19.2.6"),
		Entry("lines past the end of the file", "// prepare (rgw_gofix.cc:20-30 at v19.2.6).\n", 3, "rgw_gofix.cc:20-30",
			"line 30 is past the end of src/rgw/rgw_gofix.cc, which has 28 lines at v19.2.6"),
		Entry("cited lines that hold no identifier named beside it, nor sit in a block headed by one",
			"// prepare calls open_bucket_index_pool (rgw_gofix.cc:13-20 at v19.2.6).\n", 3, "rgw_gofix.cc:13-20",
			"the cited lines of src/rgw/rgw_gofix.cc at v19.2.6, and the 3 either side, hold none of open_bucket_index_pool"),
		Entry("a continuation whose candidates' lines hold no identifier named beside it",
			"// prepare ("+prepareS+") calls open_bucket_index_pool (:24-26 at v19.2.6).\n", 3, ":24-26",
			"the cited lines, and the 3 either side, hold none of open_bucket_index_pool in src/rgw/rgw_gofix.cc at v19.2.6"),
		Entry("a continuation whose farther file was tried only for the strong identifiers",
			"// RGWOp ("+cephURL("v19.2.6", "src/rgw/rgw_op_fix.h", "L4")+") and twin_alpha in rgw_gofix.cc (:12 at v19.2.6).\n",
			3, ":12", "the cited lines, and the 3 either side, hold none of RGWOp, twin_alpha in src/rgw/rgw_gofix.cc, "+
				"nor of twin_alpha in src/rgw/rgw_op_fix.h at v19.2.6"),
		Entry("a continuation with no identifier beside it in a clause that names two files",
			"// prepare ("+prepareS+" and "+cephURL("v19.2.6", "src/rgw/rgw_side.cc", "L2")+" at v19.2.6, :14-21 and :2 at\n"+
				"// v20.2.4).\n", 3, ":14-21",
			"its clause names rgw_gofix.cc and rgw_side.cc, and no identifier beside it tells which it cites: link it by hand"),
		Entry("a clause naming two releases, neither of them right before it",
			"// The fixture at v19.2.6 and v20.2.4: rgw_gofix.cc:4-11.\n", 3, "rgw_gofix.cc:4-11",
			`no ref: its clause names v19.2.6 and v20.2.4, and nothing ties one to it: write "at <tag>" after it`),
		Entry(`a continuation after another citation's own "at <tag>"`,
			"// prepare ("+prepareS+" at v19.2.6, :14-21).\n", 3, ":14-21",
			`no ref: no "at <tag>" follows it, no [S] or [T] marks it, and no comment above says what a bare line number means`),
		Entry(`"at its head <commit>" after it, which a header convention's tag does not override`,
			"// A bare line number below is v19.2.6's.\n\n// as the fix does (rgw_gofix.cc:4-11 at its head 2c1db8239cc).\n", 5,
			"rgw_gofix.cc:4-11", "no ref: it is cited at 2c1db8239cc, which is no tag: link it by hand"),
		Entry(`"at commit <commit>" after it, which a header convention's tag does not override`,
			"// A bare line number below is v19.2.6's.\n\n// as the fix does (rgw_gofix.cc:4-11 at commit 2c1db8239cc).\n", 5,
			"rgw_gofix.cc:4-11", "no ref: it is cited at 2c1db8239cc, which is no tag: link it by hand"),
		Entry(`"at main" after it, which a header convention's tag does not override`,
			"// A bare line number below is v19.2.6's.\n\n// prepare (rgw_gofix.cc:4-11 at main).\n", 5,
			"rgw_gofix.cc:4-11", "no ref: it is cited at main, which is no tag: link it by hand"),
		Entry(`a citation whose "'s" godoc would link as part of the URL`,
			"// A bare line number below is v19.2.6's.\n\n// The pool opens at rgw_gofix.cc:6's call.\n", 5, "rgw_gofix.cc:6",
			`godoc would link the "'s" after it as part of the URL: reword it so that a blank, or a "." or "," that `+
				`ends the clause, follows the citation`),
		Entry("a continuation whose nearest file is too short",
			"// prepare ("+prepareS+") and write_meta (:20-30 at v19.2.6).\n", 3, ":20-30",
			"line 30 is past the end of src/rgw/rgw_gofix.cc, which has 28 lines at v19.2.6"),
		Entry("a continuation with no file before it and a header naming none",
			"// A bare line number is v19.2.6's.\n\n// prepare (:4-11).\n", 5, ":4-11",
			"no citation or file name comes before it in its comment, and the header convention names no file"),
		Entry("a paragraph naming another repository",
			"// go-ceph's binding reaches prepare (rgw_gofix.cc:4-11 at v19.2.6).\n", 3, "rgw_gofix.cc:4-11",
			"its paragraph names go-ceph, whose files citecheck does not resolve: if the file is ceph's, link it by hand"),
		Entry("a header's ref where the words before it name another release",
			"// A bare line number is v19.2.6's rgw_gofix.cc.\n\n// prepare moved as v20.2.4 moved it (see :14-21).\n", 5, ":14-21",
			`the words before it name v20.2.4, but only the header convention gives it a ref, v19.2.6: write "at <tag>" after it`),
		Entry("a header naming one file, for another file's citation",
			"// A bare line number is v19.2.6's rgw_gofix.cc.\n\n// The side is rgw_side.cc:2.\n", 5, "rgw_side.cc:2",
			`no ref: only the header convention gives one, v19.2.6, and it gives it to rgw_gofix.cc's lines, not `+
				`rgw_side.cc's: write "at <tag>" after it`),
		Entry("a header naming one file, for another file's continuation",
			"// A bare line number is v19.2.6's rgw_gofix.cc.\n\n// The side is rgw_side.cc\n// (:2).\n", 6, ":2",
			`no ref: only the header convention gives one, v19.2.6, and it gives it to rgw_gofix.cc's lines, not `+
				`rgw_side.cc's: write "at <tag>" after it`),
		Entry("a continuation after words, not listed with the citation whose ref it would take",
			"// prepare ("+prepareT+" at v20.2.4, then :23-38).\n", 3, ":23-38",
			`no ref: no "at <tag>" follows it, no [S] or [T] marks it, and no comment above says what a bare line number means`),
		Entry("two releases named before the parenthesis it opens",
			"// Squid and v20.2.4 both prepare (rgw_gofix.cc:14-21).\n", 3, "rgw_gofix.cc:14-21",
			`no ref: no "at <tag>" follows it, no [S] or [T] marks it, and no comment above says what a bare line number means`),
		Entry("lines cited at both tags that are not the same at each",
			"// prepare (rgw_gofix.cc:4-11 at both tags).\n", 3, "rgw_gofix.cc:4-11",
			"it is cited at both tags, but lines 4-11 of src/rgw/rgw_gofix.cc are not the same at v19.2.6 and v20.2.4: "+
				`write "at <tag>" after it`),
		Entry("a continuation whose nearest file lacks the identifier and whose farther one holds only a generic one",
			"// RGWOp ("+cephURL("v19.2.6", "src/rgw/rgw_op_fix.h", "L4")+") is what rgw_gofix.cc\n"+
				"// (:12 at v19.2.6) overrides.\n", 4, ":12",
			"the cited lines of src/rgw/rgw_gofix.cc at v19.2.6, its nearest file, and the 3 either side, hold none of "+
				"RGWOp, and none of those is distinctive enough to take it to a farther file"),
		Entry("a continuation a farther file's enclosing block alone would take",
			"// In rgw_gofix.cc, twin_alpha ("+cephURL("v19.2.6", "src/rgw/driver/rados/rgw_twin.cc", "L2")+
				") logs as\n// RGWGoFix logs at its end (:25 at v19.2.6).\n",
			4, ":25", "line 25 is past the end of src/rgw/driver/rados/rgw_twin.cc, which has 2 lines at v19.2.6"),
	)

	It("lists a citation in a list item that runs across a line break, which it does not rewrap", func(ctx SpecContext) {
		src := "package fix\n\n// prepare:\n//\n//   - opens the index pool, rgw_gofix.cc\n//     [S]:6, first.\nfunc prepare() {}\n"
		f, status := fixGo(ctx, src)
		Expect(status).To(Equal(1))
		Expect(stdout.String()).To(Equal(f + ":5: rgw_gofix.cc [S]:6: it runs across a line break in a comment citecheck " +
			"does not rewrap: put it on one line\ncitations checked: 1, failed: 1, rewritten: 0\n"))
		Expect(read(f)).To(Equal(src))
	})

	DescribeTable("--fix leaves alone, and does not count, what is not a citation of ceph's code",
		func(ctx SpecContext, src string) {
			f, status := fixGo(ctx, src)
			Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0, rewritten: 0\n"))
			Expect(status).To(Equal(0))
			Expect(read(f)).To(Equal(src))
		},
		Entry("rgw-go's own files", "package fix\n\n// See bucketops.go:290 and internal/driver/put.go:12-14 at v19.2.6.\nvar x = 1\n"),
		Entry("a file ceph does not have, and a continuation of it",
			"package fix\n\n// net/http/transfer.go:644, :727 at go1.27.1.\nvar x = 1\n"),
		Entry("a line number with no file before it and no header",
			"package fix\n\n// It listens on :8080 by default.\nvar x = 1\n"),
		Entry("a slice expression", "package fix\n\n// In rgw_gofix.cc, b[:4] is the magic.\nvar x = 1\n"),
		Entry("a directive other than //nolint", "package fix\n\n//go:generate echo rgw_gofix.cc:4-11 at v19.2.6\n\n"+
			"var x = 1 //lint:ignore U1000 // rgw_gofix.cc:4-11 at v19.2.6\n"),
		Entry("a string", "package fix\n\nvar x = \"rgw_gofix.cc:4-11 at v19.2.6\"\n"),
		Entry("a block comment", "package fix\n\n/* rgw_gofix.cc:4-11 at v19.2.6 */\nvar x = 1\n"),
		Entry("a cgo preamble of // lines", "package fix\n\n// #include <stdlib.h>\n// rgw_gofix.cc:4-11 at v19.2.6\nimport \"C\"\n"),
		Entry("a cgo preamble in an import list", "package fix\n\nimport (\n\t// rgw_gofix.cc:4-11 at v19.2.6\n\t\"C\"\n)\n"),
	)

	Describe("rewriting", func() {
		DescribeTable("keeps a link whole, and wraps the paragraph it touched to the paragraph's widest line",
			func(ctx SpecContext, before, after string) {
				f, status := fixGo(ctx, before)
				Expect(status).To(Equal(0))
				Expect(read(f)).To(Equal(after))
			},
			Entry("the lines before the citation kept, the link on its own line",
				"package fix\n\n// prepare opens the index pool first, as RGWGoFix::prepare does\n"+
					"// (rgw_gofix.cc:4-11 at v19.2.6), and fails when it cannot be opened.\nfunc prepare() {}\n",
				"package fix\n\n// prepare opens the index pool first, as RGWGoFix::prepare does\n"+
					"// ("+prepareS+" at v19.2.6),\n// and fails when it cannot be opened.\nfunc prepare() {}\n"),
			Entry("the lines after it kept once the new lines end where an old one did",
				"package fix\n\n// complete retries write_meta as v19.2.6's rgw_gofix.cc:13-28\n"+
					"// does, up to max_retries times, and logs a failure it cannot\n// get past.\nfunc complete() {}\n",
				"package fix\n\n// complete retries write_meta as v19.2.6's\n// "+completeS+"\n"+
					"// does, up to max_retries times, and logs a failure it cannot\n// get past.\nfunc complete() {}\n"),
			Entry(`an ", at <tag>" closing a list kept on the last link's line`,
				"package fix\n\n// prepare and complete are, in order, the fixture's two functions\n"+
					"// (rgw_gofix.cc:4-11, :13-28, at v19.2.6), each returning r.\nfunc prepare() {}\n",
				"package fix\n\n// prepare and complete are, in order, the fixture's two functions\n// ("+prepareS+",\n// "+
					completeS+", at v19.2.6),\n// each returning r.\nfunc prepare() {}\n"),
			Entry("an indented comment, its prefix kept",
				"package fix\n\nfunc prepare() {\n\t// open_bucket_index_pool first (rgw_gofix.cc:6 at v19.2.6), as\n"+
					"\t// radosgw does.\n}\n",
				"package fix\n\nfunc prepare() {\n\t// open_bucket_index_pool first\n\t// ("+cephURL("v19.2.6", gofixPath, "L6")+
					" at v19.2.6),\n\t// as radosgw does.\n}\n"),
		)

		DescribeTable("makes the edit in place, rewrapping nothing",
			func(ctx SpecContext, before, after string) {
				f, status := fixGo(ctx, before)
				Expect(status).To(Equal(0))
				Expect(read(f)).To(Equal(after))
			},
			Entry("in a list item",
				"package fix\n\n// prepare:\n//\n//   - opens the pool (rgw_gofix.cc:6 at v19.2.6), then\n//     returns.\nfunc prepare() {}\n",
				"package fix\n\n// prepare:\n//\n//   - opens the pool ("+cephURL("v19.2.6", gofixPath, "L6")+" at v19.2.6), then\n"+
					"//     returns.\nfunc prepare() {}\n"),
			Entry("in a comment after code",
				"package fix\n\nvar x = 1 // rgw_gofix.cc:6 at v19.2.6\n",
				"package fix\n\nvar x = 1 // "+cephURL("v19.2.6", gofixPath, "L6")+" at v19.2.6\n"),
			Entry("in a paragraph a line of whose rewrapping would read as a list item",
				"package fix\n\n// prepare opens, at v19.2.6, rgw_gofix.cc:6 - the index pool -\n// and returns.\nfunc prepare() {}\n",
				"package fix\n\n// prepare opens, at v19.2.6, "+cephURL("v19.2.6", gofixPath, "L6")+" - the index pool -\n"+
					"// and returns.\nfunc prepare() {}\n"),
			Entry("in a paragraph whose lines hold two blanks running",
				"package fix\n\n// prepare opens.  Then it\n// returns (rgw_gofix.cc:6 at v19.2.6).\nfunc prepare() {}\n",
				"package fix\n\n// prepare opens.  Then it\n// returns ("+cephURL("v19.2.6", gofixPath, "L6")+" at v19.2.6).\n"+
					"func prepare() {}\n"),
		)

		It("writes a file gofmt leaves as it is, and a second run rewrites nothing", func(ctx SpecContext) {
			src := "// Package fix is a fixture. A bare line number is v19.2.6's rgw_gofix.cc.\npackage fix\n\n" +
				"// prepare is RGWGoFix::prepare (:4-11; [T] :14-21), which opens the index\n" +
				"// pool with open_bucket_index_pool (:6) and fails when it cannot:\n//\n" +
				"//   - the pool is missing (rgw_gofix.cc:7-9 at v19.2.6);\n//   - or not ours.\n//\n" +
				"// # Retries\n//\n// complete retries write_meta up to max_retries times\n// (:17-23), then logs (:20-30).\n" +
				"func prepare() {\n\t_ = 1 // complete (:13-28)\n}\n"
			f, status := fixGo(ctx, src)
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(Equal(f + ":13: :20-30: unresolved: line 30 is past the end of src/rgw/rgw_gofix.cc, " +
				"which has 28 lines at v19.2.6\ncitations checked: 7, failed: 1, rewritten: 6\n"))
			first := read(f)
			formatted, err := format.Source([]byte(first))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(formatted)).To(Equal(first))
			Expect(strings.Count(first, "https://github.com/ceph/ceph/blob/")).To(Equal(6))

			stdout.Reset()
			Expect(run(ctx, "--fix", "--ceph", ceph.dir, f)).To(Equal(1))
			Expect(stdout.String()).To(HaveSuffix("citations checked: 7, failed: 1, rewritten: 0\n"))
			Expect(read(f)).To(Equal(first))
		})
	})

	Describe("checking", func() {
		It("fails each plain citation of ceph's code as not a link, and checks each link", func(ctx SpecContext) {
			src := "package fix\n\n// prepare (rgw_gofix.cc:4-11 at v19.2.6, " + prepareT + ") and\n" +
				"// " + cephURL("v19.2.6", gofixPath, "L40") + ", but not bucketops.go:12.\nfunc prepare() {}\n"
			f := markdown("check.go", src)
			Expect(run(ctx, "--ceph", ceph.dir, f)).To(Equal(1))
			Expect(stdout.String()).To(Equal(
				f + ":3: rgw_gofix.cc:4-11: not a link: ceph has src/rgw/rgw_gofix.cc" + remedy +
					f + ":4: " + cephURL("v19.2.6", gofixPath, "L40") + ": line 40 is past the end of src/rgw/rgw_gofix.cc, " +
					"which has 28 lines at v19.2.6\n" +
					"citations checked: 3, failed: 2\n"))
			Expect(read(f)).To(Equal(src))
		})

		It("checks markdown and Go files in one run", func(ctx SpecContext) {
			md := markdown("plan.md", "Read ["+"rgw_gofix.cc:4-11]("+prepareS+").\n")
			g := markdown("fix.go", "package fix\n\n// prepare is "+prepareT+".\nfunc prepare() {}\n")
			Expect(run(ctx, "--ceph", ceph.dir, md, g)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 2, failed: 0\n"))
		})

		It("reads a .gperf and a .in file as it reads the others", func(ctx SpecContext) {
			ceph.write("src/rgw/rgw_fix_keywords.gperf", "%%\nVersion, TokenKind::top\n")
			ceph.write("src/rgw/rgw_fix.yaml.spec.in", "export CFLAGS\n")
			ceph.commit("gperf and in")
			ceph.git("tag", "-f", "v19.2.6")
			f, status := fixGo(ctx, "package fix\n\n// keywords (rgw_fix_keywords.gperf:2 at v19.2.6) and the flags\n"+
				"// (rgw_fix.yaml.spec.in:1 at v19.2.6).\nfunc prepare() {}\n")
			Expect(stdout.String()).To(Equal("citations checked: 2, failed: 0, rewritten: 2\n"))
			Expect(status).To(Equal(0))
			Expect(read(f)).To(ContainSubstring(cephURL("v19.2.6", "src/rgw/rgw_fix_keywords.gperf", "L2")))
			Expect(read(f)).To(ContainSubstring(cephURL("v19.2.6", "src/rgw/rgw_fix.yaml.spec.in", "L1")))
		})

		It("under --form-only fails, without a clone, a plain citation of a file the list of ceph's files has, with a "+
			"ref or none, and a malformed link", func(ctx SpecContext) {
			src := "package fix\n\n// prepare (rgw_op.cc:4-11 at v19.2.6), (rgw_op.cc:4, :9) and bucketops.go:9 at v19.2.6;\n" +
				"// " + cephURL("v19.2.6", gofixPath, "L11-L4") + ".\nfunc prepare() {}\n"
			f := markdown("form.go", src)
			Expect(run(ctx, "--form-only", "--ceph", GinkgoT().TempDir(), f)).To(Equal(1))
			Expect(stdout.String()).To(Equal(
				f + ":3: rgw_op.cc:4-11: not a link: ceph has src/rgw/rgw_op.cc" + remedy +
					f + ":3: rgw_op.cc:4: not a link: ceph has src/rgw/rgw_op.cc" + remedy +
					f + ":3: :9: not a link: ceph has src/rgw/rgw_op.cc" + remedy +
					f + ":4: " + cephURL("v19.2.6", gofixPath, "L11-L4") + ": lines 11-4 run backwards\n" +
					"citations checked: 4, failed: 4\n"))
		})
	})
})
