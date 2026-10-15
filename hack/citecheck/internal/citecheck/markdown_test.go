package citecheck_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// mdLink returns the link --fix writes for a citation of the lines of
// gofixPath at url.
func mdLink(lines, url string) string { return "[rgw_gofix.cc:" + lines + "](" + url + ")" }

// offList is why --form-only fails a citation of a file the list of ceph's
// files lacks, tied to ref, which the list was not made at.
func offList(ref string) string {
	return "cited at " + ref + ", which the list of ceph's files was not made at: link it, or add the ref to " +
		"CITECHECK_FILES_REFS"
}

// pairLead is a registry's lead-in that states the pair convention, pair a
// list item under it that cites rgw_gofix.cc's prepare at each tag, and
// pairLinks that item as --fix writes it.
const (
	pairLead = "- **Evidence:** each pair of lines is v19.2.6's, then v20.2.4's.\n"
	pair     = "  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`; `:14-21`).\n"
	// noScope is why a citation no rule or scope gives a ref has none.
	noScope = `no ref: no "at <tag>" follows it, no [S] or [T] marks it, and neither its sentence, the list ` +
		"items and the paragraph that lead in to it, nor its section's heading names a release"
)

var (
	pairLinks = "  - `RGWGoFix::prepare` opens the pool (" + mdLink("4-11", prepareS) + "; " + mdLink("14-21", prepareT) + ").\n"
	// retryS and retryT are the links to complete's retry loop at each tag.
	retryS = cephURL("v19.2.6", gofixPath, "L17-L23")
	retryT = cephURL("v20.2.4", gofixPath, "L27-L33")
)

var _ = Describe("Plain citations in markdown", func() {
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

	// fixMD runs --fix --markdown-plain on a markdown file holding src and
	// returns the file's path and the exit status.
	fixMD := func(ctx SpecContext, src string) (string, int) {
		f := markdown("doc.md", src)
		return f, run(ctx, "--fix", "--markdown-plain", "--ceph", ceph.dir, f)
	}

	It("reads none without --markdown-plain, as the checks before the sweep do", func(ctx SpecContext) {
		src := "`RGWGoFix::prepare` (`rgw_gofix.cc:4-11` at v19.2.6), and rgw_gofix.cc:4-11 at v19.2.6.\n"
		f := markdown("doc.md", src)
		Expect(run(ctx, "--fix", "--ceph", ceph.dir, f)).To(Equal(0))
		Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0, rewritten: 0\n"))
		stdout.Reset()
		Expect(run(ctx, "--form-only", f)).To(Equal(0))
		Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\n"))
		Expect(read(f)).To(Equal(src))
	})

	Describe("--fix --markdown-plain resolves each and rewrites it into the CL-D1 link", func() {
		DescribeTable("by the rules a Go comment is read by",
			func(ctx SpecContext, src, want string) {
				f, status := fixMD(ctx, src)
				Expect(stdout.String()).To(HaveSuffix(", failed: 0, rewritten: %d\n", strings.Count(want, "](")))
				Expect(status).To(Equal(0))
				Expect(read(f)).To(Equal(want))
			},
			Entry("a code span and the \"at <tag>\" after it",
				"`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11` at v19.2.6).\n",
				"`RGWGoFix::prepare` opens the pool ("+mdLink("4-11", prepareS)+" at v19.2.6).\n"),
			Entry("a citation in prose",
				"`RGWGoFix::prepare` opens the pool (rgw_gofix.cc:4-11 at v19.2.6).\n",
				"`RGWGoFix::prepare` opens the pool ("+mdLink("4-11", prepareS)+" at v19.2.6).\n"),
			Entry("a continuation listed after it, in the file it continues",
				"`RGWGoFix::prepare` and `RGWGoFix::complete` (`rgw_gofix.cc:4-11` and `:13-28` at v19.2.6).\n",
				"`RGWGoFix::prepare` and `RGWGoFix::complete` ("+mdLink("4-11", prepareS)+" and "+mdLink("13-28", completeS)+" at v19.2.6).\n"),
			Entry("a qualified name's part that reads as code, though the whole name does not stand",
				"`rgw::open_quietly` opens it (`rgw_gofix.cc:4-7` at v20.2.4).\n",
				"`rgw::open_quietly` opens it ("+mdLink("4-7", cephURL("v20.2.4", gofixPath, "L4-L7"))+" at v20.2.4).\n"),
			Entry(`its own tag right before it, which an "at <tag>" after a citation listed with it does not override`,
				"`RGWGoFix::prepare` is at v19.2.6 `rgw_gofix.cc:4-11` and `rgw_gofix.cc:14-21` at v20.2.4.\n",
				"`RGWGoFix::prepare` is at v19.2.6 "+mdLink("4-11", prepareS)+" and "+mdLink("14-21", prepareT)+
					" at v20.2.4.\n"),
			Entry("a marker in the code span",
				"`RGWGoFix::prepare` (`rgw_gofix.cc [T]:14-21`).\n", "`RGWGoFix::prepare` ("+mdLink("14-21", prepareT)+").\n"),
			Entry("a marker before the code span, which goes with it",
				"`RGWGoFix::prepare` is [T] `rgw_gofix.cc:14-21`.\n", "`RGWGoFix::prepare` is "+mdLink("14-21", prepareT)+".\n"),
			Entry("a tag before it in its clause",
				"`RGWGoFix::prepare` (v19.2.6 `rgw_gofix.cc:4-11`; v20.2.4 `:14-21`).\n",
				"`RGWGoFix::prepare` (v19.2.6 "+mdLink("4-11", prepareS)+"; v20.2.4 "+mdLink("14-21", prepareT)+").\n"),
			Entry(`the release the words before the "(" it opens name`,
				"radosgw at v20.2.4 runs `RGWGoFix::prepare` (`rgw_gofix.cc:14-21`).\n",
				"radosgw at v20.2.4 runs `RGWGoFix::prepare` ("+mdLink("14-21", prepareT)+").\n"),
			Entry("a citation in a table's cell, the row keeping its cells",
				"| Op | Where |\n| --- | --- |\n| prepare | `rgw_gofix.cc:4-11` at v19.2.6 |\n",
				"| Op | Where |\n| --- | --- |\n| prepare | "+mdLink("4-11", prepareS)+" at v19.2.6 |\n"),
		)

		DescribeTable("or, when those give none, by the scopes around it, the nearest that names a release",
			func(ctx SpecContext, src, want string) {
				f, status := fixMD(ctx, src)
				Expect(stdout.String()).To(HaveSuffix(", failed: 0, rewritten: 1\n"))
				Expect(status).To(Equal(0))
				Expect(read(f)).To(Equal(want))
			},
			Entry("its sentence",
				"At v19.2.6, `RGWGoFix::prepare` opens the pool and returns (`rgw_gofix.cc:4-11`).\n",
				"At v19.2.6, `RGWGoFix::prepare` opens the pool and returns ("+mdLink("4-11", prepareS)+").\n"),
			Entry("its sentence, by a release's codename",
				"Squid's radosgw, as `RGWGoFix::prepare` shows, opens the pool and returns (`rgw_gofix.cc:4-11`).\n",
				"Squid's radosgw, as `RGWGoFix::prepare` shows, opens the pool and returns ("+mdLink("4-11", prepareS)+").\n"),
			Entry("the list item it is nested in",
				"- At v20.2.4:\n  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:14-21`).\n",
				"- At v20.2.4:\n  - `RGWGoFix::prepare` opens the pool ("+mdLink("14-21", prepareT)+").\n"),
			Entry(`the paragraph before its list, which ends with ":"`,
				"What Squid's radosgw does:\n\n- `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`).\n",
				"What Squid's radosgw does:\n\n- `RGWGoFix::prepare` opens the pool ("+mdLink("4-11", prepareS)+").\n"),
			Entry("another cell of its table row that names the release plainly",
				"| Release | Where |\n| --- | --- |\n| **v20.2.4** | `rgw_gofix.cc:14-21` |\n",
				"| Release | Where |\n| --- | --- |\n| **v20.2.4** | "+mdLink("14-21", prepareT)+" |\n"),
			Entry("its column's header, which names the release plainly",
				"| Squid (v19.2.6) | Notes |\n| --- | --- |\n| `rgw_gofix.cc:4-11` | the pool |\n",
				"| Squid (v19.2.6) | Notes |\n| --- | --- |\n| "+mdLink("4-11", prepareS)+" | the pool |\n"),
			Entry("its section's heading, which names it by tag",
				"## RGWGoFix (v20.2.4)\n\n`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:14-21`).\n",
				"## RGWGoFix (v20.2.4)\n\n`RGWGoFix::prepare` opens the pool ("+mdLink("14-21", prepareT)+").\n"),
		)

		It("takes a continuation past its nearest file to one its paragraph names before it, by a strong identifier",
			func(ctx SpecContext) {
				f, status := fixMD(ctx, "In rgw_gofix.cc `write_meta` is retried, and `RGWOp` (`rgw_op_fix.h:4` and `:18` at "+
					"v19.2.6) is the base.\n")
				Expect(stdout.String()).To(Equal("citations checked: 2, failed: 0, rewritten: 2\n"))
				Expect(status).To(Equal(0))
				Expect(read(f)).To(ContainSubstring(" and " + mdLink("18", cephURL("v19.2.6", gofixPath, "L18")) + " at"))
			})

		It("finds a continuation's file in an earlier paragraph of its section, by the identifier beside it",
			func(ctx SpecContext) {
				f, status := fixMD(ctx, "The fixture is `rgw_gofix.cc`.\n\nAt v19.2.6, `write_meta` is retried (`:18`).\n")
				Expect(stdout.String()).To(Equal("citations checked: 1, failed: 0, rewritten: 1\n"))
				Expect(status).To(Equal(0))
				Expect(read(f)).To(ContainSubstring("(" + mdLink("18", cephURL("v19.2.6", gofixPath, "L18")) + ")"))
			})

		It("changes nothing on a second run", func(ctx SpecContext) {
			f, status := fixMD(ctx, "`RGWGoFix::prepare` (`rgw_gofix.cc:4-11` at v19.2.6), `RGWGoFix::complete` (`:13-28`).\n\n"+
				"- At v20.2.4:\n  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:14-21`).\n  - and more (`rgw_gofix.cc:40-50`).\n")
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(HaveSuffix("citations checked: 4, failed: 2, rewritten: 2\n"))
			first := read(f)
			stdout.Reset()
			Expect(run(ctx, "--fix", "--markdown-plain", "--ceph", ceph.dir, f)).To(Equal(1))
			Expect(stdout.String()).To(HaveSuffix("citations checked: 4, failed: 2, rewritten: 0\n"))
			Expect(read(f)).To(Equal(first))
		})
	})

	DescribeTable("--fix --markdown-plain leaves one it cannot resolve as it is and lists why",
		func(ctx SpecContext, src, text, why string) {
			f, status := fixMD(ctx, src)
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(ContainSubstring(fmt.Sprintf(": %s: unresolved: %s\n", text, why)))
			Expect(read(f)).To(ContainSubstring(text))
		},
		Entry("a sentence naming two releases",
			"At v19.2.6 and at v20.2.4, `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`).\n", "rgw_gofix.cc:4-11",
			`no ref: its sentence names v19.2.6 and v20.2.4, and nothing ties one to it: write "at <tag>" after it`),
		Entry("a sentence naming its release only after it",
			"`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`), as at v19.2.6.\n", "rgw_gofix.cc:4-11",
			`no ref: its sentence names v19.2.6 only after it: write "at <tag>" after it`),
		Entry("a sentence naming a release as another citation's own",
			"`RGWGoFix::complete` (`rgw_gofix.cc:13-28` at v19.2.6) calls `RGWGoFix::prepare`, which opens the pool (`rgw_gofix.cc:4-11`).\n",
			"rgw_gofix.cc:4-11", `no ref: its sentence names v19.2.6 as another citation's own: write "at <tag>" after it`),
		Entry("a sentence naming a release only through a link's",
			"`RGWGoFix::complete` (["+"rgw_gofix.cc:13-28]("+completeS+")) calls `RGWGoFix::prepare`, which opens the pool (`rgw_gofix.cc:4-11`).\n",
			"rgw_gofix.cc:4-11", `no ref: its sentence names v19.2.6 as another citation's own: write "at <tag>" after it`),
		Entry("a citation at a ref that is no tag",
			"`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11` on main).\n", "rgw_gofix.cc:4-11",
			"no ref: it is cited on main, which is no tag: link it by hand"),
		Entry("a sentence naming a ref that is no tag",
			"On main, `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`).\n", "rgw_gofix.cc:4-11",
			"no ref: its sentence names main, which is no tag: link it by hand"),
		Entry(`a release the "(" rule finds, where the words before it name another`,
			"The same at v20.2.4, where `RGWGoFix::prepare` is unchanged from v19.2.6 (`rgw_gofix.cc:14-21`).\n", "rgw_gofix.cc:14-21",
			`no ref: the words before it name v19.2.6 and v20.2.4, and nothing ties one to it: write "at <tag>" after it`),
		Entry("a lead-in naming two releases",
			"- At v19.2.6 and v20.2.4:\n  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`).\n", "rgw_gofix.cc:4-11",
			`no ref: its lead-in names v19.2.6 and v20.2.4, and nothing ties one to it: write "at <tag>" after it`),
		Entry("a header's name beside it, whose words do not stand in for it",
			"The `x-open-pool` header is read (`rgw_gofix.cc:4-7` at v20.2.4).\n", "rgw_gofix.cc:4-7",
			"the cited lines of src/rgw/rgw_gofix.cc at v20.2.4, and the 3 either side, hold none of x-open-pool"),
		Entry("a path beside it in a code span, whose words do not stand in for it",
			"The `docs/open_quietly.md` notes say it (`rgw_gofix.cc:4-7` at v20.2.4).\n", "rgw_gofix.cc:4-7",
			"the cited lines of src/rgw/rgw_gofix.cc at v20.2.4, and the 3 either side, hold none of docs/open_quietly.md"),
		Entry("a generic name beside it in a code span, which names no code",
			"The `rgw` opens it (`rgw_gofix.cc:4-7` at v20.2.4).\n", "rgw_gofix.cc:4-7",
			"the words beside it that may name code, rgw, are English words, generic names or parts of a longer literal, "+
				"and no whole code token stands beside it to check the cited lines by: link it by hand"),
		Entry("a common English word beside it in a code span, which is no code token",
			"The pool is open: `open` returns it (`rgw_gofix.cc:4-7` at v20.2.4).\n", "rgw_gofix.cc:4-7",
			"the words beside it that may name code, open, are English words, generic names or parts of a longer literal, and no "+
				"whole code token stands beside it to check the cited lines by: link it by hand"),
		Entry(`a sentence naming a release as another citation's own, after a comma`,
			"`RGWGoFix::complete` retries (`rgw_gofix.cc:13-28`, at v19.2.6), and `RGWGoFix::prepare` opens the pool "+
				"(`rgw_gofix.cc:4-11`).\n",
			"rgw_gofix.cc:4-11", `no ref: its sentence names v19.2.6 as another citation's own: write "at <tag>" after it`),
		Entry("a table whose list before it follows the paragraph that names a release",
			"At v20.2.4:\n\n- one thing\n- another thing\n\n| Op | Where |\n| --- | --- |\n| put | `rgw_gofix.cc:14-21` |\n",
			"rgw_gofix.cc:14-21", noScope),
		Entry("a list whose table before it follows the paragraph that names a release",
			"At v20.2.4:\n\n| A | B |\n| --- | --- |\n| x | y |\n\n- `RGWGoFix::prepare` reads it (`rgw_gofix.cc:14-21`).\n",
			"rgw_gofix.cc:14-21", noScope),
		Entry("a lead-in naming a ref that is no tag",
			"- On main:\n  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`).\n", "rgw_gofix.cc:4-11",
			"no ref: its lead-in names main, which is no tag: link it by hand"),
		Entry("a lead-in naming a release as another citation's own",
			"- As `rgw_gofix.cc:13-28` at v19.2.6 shows:\n  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`).\n",
			"rgw_gofix.cc:4-11", `no ref: its lead-in names v19.2.6 as another citation's own: write "at <tag>" after it`),
		Entry("a section's heading naming two releases",
			"## Squid and v20.2.4\n\n`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:14-21`).\n", "rgw_gofix.cc:14-21",
			`no ref: its section's heading names v19.2.6 and v20.2.4, and nothing ties one to it: write "at <tag>" after it`),
		Entry("a section's heading naming a ref that is no tag",
			"## The main branch\n\n`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:14-21`).\n", "rgw_gofix.cc:14-21",
			"no ref: its section's heading names main, which is no tag: link it by hand"),
		Entry(`a section's heading naming the release a fix landed in`,
			"## Fixed in v20.2.4\n\n`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:14-21`).\n", "rgw_gofix.cc:14-21",
			`no ref: its section's heading names v20.2.4 after "fixed in", which says when something changed, not the `+
				`release its lines are read at: write "at <tag>" after it`),
		Entry(`a sentence naming the release "before" which it held`,
			"Before Tentacle, `rgw_gofix.cc:4-11` opened the pool.\n", "rgw_gofix.cc:4-11",
			`no ref: its sentence names v20.2.4 after "before", which says when something changed, not the release its `+
				`lines are read at: write "at <tag>" after it`),
		Entry(`a sentence naming the release "until" which it held`,
			"Until v20.2.4, `rgw_gofix.cc:4-11` opened the pool.\n", "rgw_gofix.cc:4-11",
			`no ref: its sentence names v20.2.4 after "until", which says when something changed, not the release its `+
				`lines are read at: write "at <tag>" after it`),
		Entry(`a sentence naming a release "pre-" which it held`,
			"In pre-Tentacle radosgw `rgw_gofix.cc:4-11` opened the pool.\n", "rgw_gofix.cc:4-11",
			`no ref: its sentence names v20.2.4 after "pre-", which says when something changed, not the release its `+
				`lines are read at: write "at <tag>" after it`),
		Entry("another cell of the table's header row, with no header row above it",
			"| v20.2.4 | `rgw_gofix.cc:14-21` |\n| --- | --- |\n| a | b |\n", "rgw_gofix.cc:14-21",
			`no ref: its table row names v20.2.4 in a cell with no header row above it: write "at <tag>" after it`),
		Entry(`a sentence naming the release "since" which it holds`,
			"Since v20.2.4, `rgw_gofix.cc:14-21` opens the pool.\n", "rgw_gofix.cc:14-21",
			`no ref: its sentence names v20.2.4 after "since", which says when something changed, not the release its `+
				`lines are read at: write "at <tag>" after it`),
		Entry("another cell of its row naming a release in a column that is no release column",
			"| Fixed in | Where |\n| --- | --- |\n| v20.2.4 | `rgw_gofix.cc:14-21` |\n", "rgw_gofix.cc:14-21",
			`no ref: its table row names v20.2.4 in a column whose header, "Fixed in", names no release column: `+
				`write "at <tag>" after it`),
		Entry("its column's header naming a release among other words",
			"| Fixed at v20.2.4 | Notes |\n| --- | --- |\n| `rgw_gofix.cc:14-21` | the pool |\n", "rgw_gofix.cc:14-21",
			`no ref: its table column's header names v20.2.4 in a cell that holds more than the release: `+
				`write "at <tag>" after it`),
		Entry("a code span holding a marker before its file",
			"`RGWGoFix::prepare` opens the pool (`[T] rgw_gofix.cc:14-21`).\n", "[T] rgw_gofix.cc:14-21",
			"its code span holds a marker before its file, which --fix does not rewrite: link it by hand"),
		Entry("a code span holding a list of line ranges",
			"`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-6,8-11` at v19.2.6).\n", "rgw_gofix.cc:4-6,8-11",
			"its code span holds a list of line ranges, which --fix does not rewrite: link it by hand"),
		Entry("a code span holding a symbol after its lines",
			"It opens the pool (`rgw_gofix.cc:4-11#RGWGoFix::prepare` at v19.2.6).\n", "rgw_gofix.cc:4-11#RGWGoFix::prepare",
			"its code span holds a symbol after its lines, which --fix does not rewrite: link it by hand"),
		Entry("a code span holding an open line range",
			"`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-` at v19.2.6).\n", "rgw_gofix.cc:4-",
			"its code span holds an open line range, which --fix does not rewrite: link it by hand"),
		Entry("another cell of its row naming a release among other words",
			"| Fix | Where |\n| --- | --- |\n| fixed in v20.2.4 | `rgw_gofix.cc:14-21` |\n", "rgw_gofix.cc:14-21",
			`no ref: its table row names v20.2.4 in a cell that holds more than the release: write "at <tag>" after it`),
		Entry("a section's heading naming its release only by a codename",
			"## Tentacle's RGWGoFix\n\n`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:14-21`).\n", "rgw_gofix.cc:14-21",
			`no ref: its section's heading names its release only by a codename: write "at <tag>" after it`),
		Entry("a heading above its section's, which gives no ref",
			"## RGWGoFix (v20.2.4)\n\n### prepare\n\n`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:14-21`).\n", "rgw_gofix.cc:14-21",
			noScope),
		Entry("a continuation listed after citations at two refs",
			"`RGWGoFix::prepare` (["+"rgw_gofix.cc:4-11]("+prepareS+"), ["+"rgw_gofix.cc:14-21]("+prepareT+"), `:6`).\n", ":6",
			`no ref: its clause cites lines at v19.2.6 and v20.2.4, and nothing ties one to it: write "at <tag>" after it`),
		Entry("a continuation with no file before it in its paragraph and no identifier beside it",
			"At v19.2.6 the fixture is in rgw_gofix.cc.\n\nAt v19.2.6, it returns at the end (`:11`).\n", ":11",
			"no file is named before it in its paragraph, and no identifier beside it confirms the file named before that: "+
				"link it by hand"),
		Entry("a continuation whose identifier stands only in a file another paragraph names",
			"In rgw_gofix.cc `write_meta` is retried.\n\n`RGWOp` (`rgw_op_fix.h:4` and `:18` at v19.2.6) is the base, and "+
				"`write_meta` is not its.\n",
			":18", "line 18 is past the end of src/rgw/rgw_op_fix.h, which has 14 lines at v19.2.6"),
	)

	Describe("under a lead-in that states the pair convention, each pair of lines one tag's then the other's", func() {
		DescribeTable("--fix --markdown-plain gives the first of each pair the first tag and the second the second, "+
			"when every citation in the list is in a pair",
			func(ctx SpecContext, src, want string) {
				f, status := fixMD(ctx, src)
				Expect(stdout.String()).To(HaveSuffix(", failed: 0, rewritten: %d\n", strings.Count(want, "](")))
				Expect(status).To(Equal(0))
				Expect(read(f)).To(Equal(want))
			},
			Entry(`a pair a ";" separates`, pairLead+pair, pairLead+pairLinks),
			Entry(`pairs a "," separates, in two items, one naming its file again`,
				pairLead+"  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`, `:14-21`).\n"+
					"  - `write_meta` is retried (`rgw_gofix.cc:17-23`, `rgw_gofix.cc:27-33`).\n",
				pairLead+"  - `RGWGoFix::prepare` opens the pool ("+mdLink("4-11", prepareS)+", "+mdLink("14-21", prepareT)+").\n"+
					"  - `write_meta` is retried ("+mdLink("17-23", retryS)+", "+mdLink("27-33", retryT)+").\n"),
			Entry("the order its statement gives",
				"- A pair of line numbers is v20.2.4's then v19.2.6's.\n  - `RGWGoFix::prepare` opens the pool "+
					"(`rgw_gofix.cc:14-21`; `:4-11`).\n",
				"- A pair of line numbers is v20.2.4's then v19.2.6's.\n  - `RGWGoFix::prepare` opens the pool ("+
					mdLink("14-21", prepareT)+"; "+mdLink("4-11", prepareS)+").\n"),
			Entry(`a statement that cites "at" one tag "and then" the other`,
				"- **Evidence:** paths are under `src/rgw/`, cited at v19.2.6 and then v20.2.4.\n"+pair,
				"- **Evidence:** paths are under `src/rgw/`, cited at v19.2.6 and then v20.2.4.\n"+pairLinks),
			Entry(`a paragraph that leads in to its list, ending with ":"`,
				"Each pair of lines below is v19.2.6's, then v20.2.4's:\n\n"+strings.TrimPrefix(pair, "  "),
				"Each pair of lines below is v19.2.6's, then v20.2.4's:\n\n"+strings.TrimPrefix(pairLinks, "  ")),
			Entry("a lead-in that names its two tags again, and a list that names one in a sentence with no citation",
				"- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is v19.2.6's, then v20.2.4's.\n"+
					"  - At v20.2.4 helpers come first.\n"+pair,
				"- **Evidence:** the same at v19.2.6 and v20.2.4; each pair of lines is v19.2.6's, then v20.2.4's.\n"+
					"  - At v20.2.4 helpers come first.\n"+pairLinks),
		)

		// declined is the reason a citation in an irregular list gets.
		declined := func(why string) string {
			return `no ref: its lead-in gives each pair of lines as v19.2.6's, then v20.2.4's, but ` + why +
				`, so the convention is not applied to its list: write "at <tag>" after it`
		}

		DescribeTable("--fix --markdown-plain applies it to none of the list, and says why, when the list is irregular",
			func(ctx SpecContext, src, text, why string) {
				f, status := fixMD(ctx, src)
				Expect(status).To(Equal(1))
				Expect(stdout.String()).To(ContainSubstring(fmt.Sprintf(": %s: unresolved: %s\n", text, declined(why))))
				Expect(stdout.String()).To(HaveSuffix(", rewritten: 0\n"))
				Expect(read(f)).To(Equal(src))
			},
			Entry("an odd count, a citation that stands alone",
				pairLead+pair+"  - `RGWGoFix::complete` retries (`rgw_gofix.cc:13-28`).\n", "rgw_gofix.cc:4-11",
				"rgw_gofix.cc:13-28 in its list stands alone, in no pair"),
			Entry("three citations run together, which mark no pairs",
				pairLead+"  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`; `:14-21`; `:6`).\n", "rgw_gofix.cc:4-11",
				"3 citations in its list run together, rgw_gofix.cc:4-11 to :6, which marks no pairs"),
			Entry("a pair of two different files",
				pairLead+"  - `RGWGoFix::prepare` and `RGWOp` (`rgw_gofix.cc:4-11`; `rgw_op_fix.h:4-14`).\n", "rgw_gofix.cc:4-11",
				"rgw_gofix.cc:4-11 and rgw_op_fix.h:4-14 in its list, a pair, name two files"),
			Entry("a third tag in the lead-in",
				"- **Evidence:** each pair of lines is v19.2.6's, then v20.2.4's, and the code is the same on main.\n"+pair,
				"rgw_gofix.cc:4-11", "its lead-in names main, a release other than its two tags"),
			Entry("a third tag in the list",
				pairLead+"  - At v18.2.4 the pool was another.\n"+pair, "rgw_gofix.cc:4-11",
				"its list names v18.2.4, a release other than its two tags"),
			Entry("a release named in the sentence of a citation in the list, by a codename",
				pairLead+"  - On Tentacle `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`; `:14-21`).\n", "rgw_gofix.cc:4-11",
				`rgw_gofix.cc:4-11 in its list shares its sentence with "Tentacle", which names a release`),
			Entry("a link in the list, which carries its own ref",
				pairLead+pair+"  - `RGWGoFix::complete` retries ([rgw_gofix.cc:13-28]("+completeS+")).\n", "rgw_gofix.cc:4-11",
				"its list holds a link, rgw_gofix.cc:13-28, which carries its own ref"),
			Entry("a citation whose lines do not hold what is named beside it",
				pairLead+"  - `open_bucket_index_pool` opens it (`rgw_gofix.cc:4-11`; `:30-31`).\n", "rgw_gofix.cc:4-11",
				":30-31 in its list does not resolve"),
			Entry("a lead-in of its own in the list that names one of its tags",
				pairLead+"  - At v20.2.4 only:\n    - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`; `:14-21`).\n",
				"rgw_gofix.cc:4-11", `rgw_gofix.cc:4-11 in its list has a lead-in of its own that names "v20.2.4"`),
			Entry("a citation in it that is not ceph's",
				pairLead+pair+"  - `RGWGoFix::put` (`internal/x/put.go:3`; `:5`).\n", "rgw_gofix.cc:4-11",
				"internal/x/put.go:3 in its list is not ceph's"),
			Entry("a citation in it that cannot be written as a link where it stands",
				pairLead+"  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11`; `:14-21\n    `).\n", "rgw_gofix.cc:4-11",
				":14-21 in its list cannot be written as a link where it stands"),
			Entry("a citation with nothing beside it that names code to check its lines by",
				pairLead+"  - It opens the pool (`rgw_gofix.cc:4-11`; `:14-21`).\n", "rgw_gofix.cc:4-11",
				"nothing beside rgw_gofix.cc:4-11 in its list names code to check its lines by"),
		)

		It("--fix --markdown-plain applies none whose statement names one tag twice", func(ctx SpecContext) {
			src := "- **Evidence:** each pair of lines is v19.2.6's, then v19.2.6's.\n" + pair
			f, status := fixMD(ctx, src)
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(ContainSubstring(": rgw_gofix.cc:4-11: unresolved: no ref: its lead-in gives each " +
				"pair of lines as v19.2.6's, then v19.2.6's, but its statement names v19.2.6 twice, so the convention is not " +
				`applied to its list: write "at <tag>" after it` + "\n"))
			Expect(stdout.String()).To(HaveSuffix(", rewritten: 0\n"))
			Expect(read(f)).To(Equal(src))
		})

		It("--fix --markdown-plain applies none whose pair resolves to two files", func(ctx SpecContext) {
			ceph.write("src/rgw/a/rgw_pairfix.cc", "// a\nint pair_marker() { return 0; }\n")
			ceph.write("src/rgw/b/rgw_pairfix.cc", "// b\nint other_one() { return 0; }\n")
			ceph.commit("pairs at squid")
			ceph.git("tag", "-f", "v19.2.6")
			ceph.write("src/rgw/a/rgw_pairfix.cc", "// a\nint other_one() { return 0; }\n")
			ceph.write("src/rgw/b/rgw_pairfix.cc", "// b\nint pair_marker() { return 0; }\n")
			ceph.commit("pairs at tentacle")
			ceph.git("tag", "-f", "v20.2.4")
			src := pairLead + "  - `pair_marker` returns 0 (`rgw_pairfix.cc:2`; `rgw_pairfix.cc:2`).\n"
			f, status := fixMD(ctx, src)
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(ContainSubstring(": rgw_pairfix.cc:2: unresolved: " + declined("rgw_pairfix.cc:2 and "+
				"rgw_pairfix.cc:2 in its list, a pair, resolve to two files, src/rgw/a/rgw_pairfix.cc and "+
				"src/rgw/b/rgw_pairfix.cc") + "\n"))
			Expect(stdout.String()).To(HaveSuffix(", rewritten: 0\n"))
			Expect(read(f)).To(Equal(src))
		})

		DescribeTable("--fix --markdown-plain lets a citation in an irregular list that carries its own ref keep it, "+
			"and gives the rest none",
			func(ctx SpecContext, src, own, want, text, why string) {
				f, status := fixMD(ctx, src)
				Expect(status).To(Equal(1))
				Expect(stdout.String()).To(ContainSubstring(fmt.Sprintf(": %s: unresolved: %s\n", text, declined(why))))
				Expect(stdout.String()).To(HaveSuffix(", failed: 1, rewritten: 1\n"))
				Expect(read(f)).To(Equal(strings.Replace(src, own, want, 1)))
			},
			Entry(`its own "at <tag>"`,
				pairLead+"  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11` at v19.2.6; `:14-21`).\n",
				"`rgw_gofix.cc:4-11`", mdLink("4-11", prepareS), ":14-21",
				"rgw_gofix.cc:4-11 in its list is cited at v19.2.6"),
			Entry("its own release right before it",
				pairLead+"  - `RGWGoFix::prepare` opens the pool (v19.2.6 `rgw_gofix.cc:4-11`; `:14-21`).\n",
				"`rgw_gofix.cc:4-11`", mdLink("4-11", prepareS), ":14-21",
				"rgw_gofix.cc:4-11 in its list has its own release, v19.2.6, right before it"),
			Entry("its own marker",
				pairLead+"  - `RGWGoFix::prepare` opens the pool (`rgw_gofix.cc [S]:4-11`; `:14-21`).\n",
				"`rgw_gofix.cc [S]:4-11`", mdLink("4-11", prepareS), ":14-21",
				"rgw_gofix.cc [S]:4-11 in its list carries its own [S] marker"),
		)
	})

	DescribeTable("--fix --markdown-plain lists one that resolves but cannot be written as a link where it stands, "+
		"and leaves it as it is",
		func(ctx SpecContext, src, text, why string) {
			f, status := fixMD(ctx, src)
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(Equal(fmt.Sprintf("%s:1: %s: %s\ncitations checked: 1, failed: 1, rewritten: 0\n",
				f, text, why)))
			Expect(read(f)).To(Equal(src))
		},
		Entry("a citation in a heading, whose anchor a link could change",
			"## RGWGoFix::prepare (rgw_gofix.cc:4-11 at v19.2.6)\n\nIt opens the pool.\n", "rgw_gofix.cc:4-11",
			"it is in a heading, whose anchor a link could change: cite it in the text below the heading"),
		Entry("a code span that runs across a line break",
			"`RGWGoFix::prepare` opens the pool (`rgw_gofix.cc:4-11\n` at v19.2.6).\n", "rgw_gofix.cc:4-11",
			"the code span runs across a line break, which rewriting it would rewrap: put it on one line"),
		Entry("a prose citation that runs across a line break",
			"`RGWGoFix::prepare` opens the pool (rgw_gofix.cc\n[S]:4-11).\n", "rgw_gofix.cc [S]:4-11",
			"it runs across a line break, which rewriting it would rewrap: put it on one line"),
		Entry(`one right after a "!"`,
			"`RGWGoFix::prepare` opens the pool !`rgw_gofix.cc:4-11` at v19.2.6.\n", "rgw_gofix.cc:4-11",
			`it follows a "!", which would make the link an image: escape it as "\!"`),
	)

	DescribeTable("leaves alone, and does not count, what is not a plain citation of ceph's code",
		func(ctx SpecContext, src string) {
			f, status := fixMD(ctx, src)
			Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0, rewritten: 0\n"))
			Expect(status).To(Equal(0))
			Expect(read(f)).To(Equal(src))
		},
		Entry("rgw-go's own files", "See `internal/driver/put.go:12` and bucketops.go:290 at v19.2.6.\n"),
		Entry("a fenced code block", "```\n`rgw_gofix.cc:4-11` at v19.2.6\n```\n"),
		Entry("a link's text", "Read [`rgw_gofix.cc:4-11` at v19.2.6](https://example.com/x).\n"),
		Entry("a code span holding more than a citation", "Run `make x rgw_gofix.cc:4-11` at v19.2.6.\n"),
		Entry("a line number with no file before it", "It listens on :8080 by default, as at v19.2.6.\n"),
		Entry("a continuation whose file is named only in another section",
			"The fixture is `rgw_gofix.cc`.\n\n## Retries\n\nAt v19.2.6, `write_meta` is retried (`:18`).\n"),
	)

	Describe("checking with --markdown-plain", func() {
		It("fails each plain citation of ceph's code as not a link, naming ceph's file", func(ctx SpecContext) {
			src := "`RGWGoFix::prepare` (`rgw_gofix.cc:4-11` at v19.2.6), not `internal/x/put.go:3`.\n"
			f := markdown("doc.md", src)
			Expect(run(ctx, "--markdown-plain", "--ceph", ceph.dir, f)).To(Equal(1))
			Expect(stdout.String()).To(Equal(f + ":1: rgw_gofix.cc:4-11: not a link: ceph has src/rgw/rgw_gofix.cc: link it, " +
				"or, if it cites rgw-go's own file, write that file's path from rgw-go's root\n" +
				"citations checked: 1, failed: 1\n"))
			Expect(read(f)).To(Equal(src))
		})

		DescribeTable("takes a citation whose name is the path of one of rgw-go's files from its root for rgw-go's, "+
			"though ceph has the same path", func(ctx SpecContext, gomod string) {
			root := GinkgoT().TempDir()
			for name, text := range map[string]string{
				"go.mod":                 gomod,
				".github/dependabot.yml": "version: 2\n",
				"hack/x.sh":              "#!/bin/sh\n",
			} {
				Expect(os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o750)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(root, name), []byte(text), 0o600)).To(Succeed())
			}
			src := "Dependabot reads `.github/dependabot.yml:1` at v19.2.6, and `hack/x.sh:1` at v21.1.2.\n"
			f := filepath.Join(root, "docs", "a.md")
			Expect(os.MkdirAll(filepath.Dir(f), 0o750)).To(Succeed())
			Expect(os.WriteFile(f, []byte(src), 0o600)).To(Succeed())
			Expect(run(ctx, "--form-only", "--markdown-plain", "--ceph", GinkgoT().TempDir(), f)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\n"))
			stdout.Reset()
			Expect(run(ctx, "--fix", "--markdown-plain", "--ceph", ceph.dir, f)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0, rewritten: 0\n"))
			Expect(read(f)).To(Equal(src))

			stdout.Reset()
			outside := markdown("a.md", src)
			Expect(run(ctx, "--form-only", "--markdown-plain", "--ceph", GinkgoT().TempDir(), outside)).To(Equal(1))
			Expect(stdout.String()).To(HavePrefix(outside + ":1: .github/dependabot.yml:1: not a link: ceph has " +
				".github/dependabot.yml: link it"))
		},
			Entry("its root's go.mod", "module github.com/jhoblitt/rgw-go\n\ngo 1.27\n"),
			Entry("a go.mod that quotes the module path and comments on it",
				"module \"github.com/jhoblitt/rgw-go\" // the gateway\n\ngo 1.27\n"),
		)

		It("with a clone, finds a file at the commit an \"at <commit>\" names", func(ctx SpecContext) {
			ceph.write("src/rgw/rgw_later.cc", "// later\nint later_one() { return 0; }\n")
			commit := ceph.commit("later")
			f := markdown("doc.md", "See `rgw_later.cc:2` at "+commit+".\n")
			Expect(run(ctx, "--markdown-plain", "--ceph", ceph.dir, f)).To(Equal(1))
			Expect(stdout.String()).To(Equal(f + ":1: rgw_later.cc:2: not a link: ceph has src/rgw/rgw_later.cc: link it, " +
				"or, if it cites rgw-go's own file, write that file's path from rgw-go's root\ncitations checked: 1, failed: 1\n"))
		})

		DescribeTable("with a clone, finds a file at the main branch the words after it name",
			func(ctx SpecContext, words string) {
				ceph.write("src/rgw/rgw_later.cc", "// later\nint later_one() { return 0; }\n")
				ceph.commit("later")
				ceph.git("update-ref", "refs/remotes/origin/main", "HEAD")
				f := markdown("doc.md", "See `rgw_later.cc:2` "+words+".\n")
				Expect(run(ctx, "--markdown-plain", "--ceph", ceph.dir, f)).To(Equal(1))
				Expect(stdout.String()).To(Equal(f + ":1: rgw_later.cc:2: not a link: ceph has src/rgw/rgw_later.cc: " +
					"link it, or, if it cites rgw-go's own file, write that file's path from rgw-go's root\n" +
					"citations checked: 1, failed: 1\n"))
			},
			Entry(`"on main"`, "on main"),
			Entry(`"at main"`, "at main"),
			Entry(`"at origin/main"`, "at origin/main"),
		)

		It("counts no common English word as an identifier, whatever files the run is given", func(ctx SpecContext) {
			ceph.write("src/rgw/rgw_bucketfix.cc", "// the bucket comes first\nint check(int bucket) { return bucket; }\n")
			ceph.commit("a bucket")
			ceph.git("tag", "-f", "v20.2.4")
			f, status := fixMD(ctx, "The `bucket` is checked first (`rgw_bucketfix.cc:2` at v20.2.4).\n")
			Expect(status).To(Equal(1))
			Expect(stdout.String()).To(Equal(f + ":1: rgw_bucketfix.cc:2: unresolved: the words beside it that may name " +
				"code, bucket, are English words, generic names or parts of a longer literal, and no whole code token stands beside it to " +
				"check the cited lines by: link it by hand\ncitations checked: 1, failed: 1, rewritten: 0\n"))
		})

		DescribeTable("under --form-only, without a clone, fails a citation whose file the list of ceph's files has, "+
			"whatever its ref",
			func(ctx SpecContext, src, want string) {
				f := markdown("doc.md", src)
				status := run(ctx, "--form-only", "--markdown-plain", "--ceph", GinkgoT().TempDir(), f)
				links := strings.Count(src, "](https://github.com/ceph/")
				if want == "" {
					Expect(stdout.String()).To(Equal(fmt.Sprintf("citations checked: %d, failed: 0\n", links)))
					Expect(status).To(Equal(0))
					return
				}
				Expect(status).To(Equal(1))
				Expect(stdout.String()).To(Equal(fmt.Sprintf("%s:1: %s\ncitations checked: %d, failed: 1\n", f, want, links+1)))
			},
			Entry("a base name", "See `rgw_op.cc:10`.\n", "rgw_op.cc:10: not a link: ceph has src/rgw/rgw_op.cc: link it, or, "+
				"if it cites rgw-go's own file, write that file's path from rgw-go's root"),
			Entry("a continuation of one", "See rgw_op.cc and `:10`.\n", ":10: not a link: ceph has src/rgw/rgw_op.cc: link it, "+
				"or, if it cites rgw-go's own file, write that file's path from rgw-go's root"),
			Entry("a base name rgw-go has too, which its path from rgw-go's root tells apart", "See `run.sh:12`.\n",
				"run.sh:12: not a link: ceph has src/dmclock/benchmark/run.sh: link it, or, if it cites rgw-go's own file, "+
					"write that file's path from rgw-go's root"),
			Entry("rgw-go's file by its path", "See `hack/admin/run.sh:12`.\n", ""),
			Entry("a Go file ceph does not have", "See `internal/s3/putobject.go:87`.\n", ""),
			Entry("a file the list lacks, cited at a tag the list was not made at",
				"See `rgw_kms_cache.cc:20-30` at v21.1.2.\n", "rgw_kms_cache.cc:20-30: "+offList("v21.1.2")),
			Entry("a file the list lacks, at the one release its sentence names, which the list was not made at",
				"At v21.1.2 the cache expires entries (`rgw_kms_cache.cc:40`).\n", "rgw_kms_cache.cc:40: "+offList("v21.1.2")),
			Entry("a file the list lacks, cited at a commit",
				"See `rgw_kms_cache.cc:50` at 7ed73efc1beb.\n", "rgw_kms_cache.cc:50: "+offList("7ed73efc1beb")),
			Entry("a file the list lacks, cited at a tag the list was made at", "See `rgw_kms_cache.cc:20` at v19.2.6.\n", ""),
			Entry("a file the list lacks, cited on main, which an rgw-go file may be too",
				"See `rgw_kms_cache.cc:20-30` on main.\n", ""),
			Entry("a file the list lacks, at a tag the list was not made at, in a paragraph that names another repository",
				"rook's operator starts it (`pkg/operator/ceph/object/controller.go:620` at v1.20.7).\n", ""),
			Entry("a .gperf file", "See `rgw_iam_policy_keywords.gperf:28`.\n",
				"rgw_iam_policy_keywords.gperf:28: not a link: ceph has src/rgw/rgw_iam_policy_keywords.gperf: link it, "+
					"or, if it cites rgw-go's own file, write that file's path from rgw-go's root"),
			Entry("a .in file", "See `ceph.spec.in:1360-1361`.\n",
				"ceph.spec.in:1360-1361: not a link: ceph has ceph.spec.in, src/test/centos-8/ceph.spec.in, "+
					"src/test/fedora-33/ceph.spec.in and 1 more: link it, or, if it cites rgw-go's own file, write that "+
					"file's path from rgw-go's root"),
			Entry("a code span holding a marker before its file", "See `[T] rgw_op.cc:10`.\n",
				"[T] rgw_op.cc:10: not a link: ceph has src/rgw/rgw_op.cc: link it, or, if it cites rgw-go's own file, write "+
					"that file's path from rgw-go's root"),
			Entry("a code span holding a list of line ranges", "See `rgw_op.cc:10,20-30`.\n",
				"rgw_op.cc:10,20-30: not a link: ceph has src/rgw/rgw_op.cc: link it, or, if it cites rgw-go's own file, "+
					"write that file's path from rgw-go's root"),
			Entry("a code span holding a symbol after its lines", "See rgw_op.cc and `:10-20#RGWOp::execute`.\n",
				":10-20#RGWOp::execute: not a link: ceph has src/rgw/rgw_op.cc: link it, or, if it cites rgw-go's own file, "+
					"write that file's path from rgw-go's root"),
			Entry("a code span holding an open line range", "See `rgw_op.cc:10-...`.\n",
				"rgw_op.cc:10-...: not a link: ceph has src/rgw/rgw_op.cc: link it, or, if it cites rgw-go's own file, "+
					"write that file's path from rgw-go's root"),
			Entry("a continuation after a link, of the link's file",
				"See [x](https://github.com/ceph/ceph/blob/v9.9.9/src/rgw/rgw_nope.cc#L1) and `:10`.\n",
				":10: not a link: ceph has src/rgw/rgw_nope.cc: link it, or, if it cites rgw-go's own file, write that "+
					"file's path from rgw-go's root"),
		)
	})
})
