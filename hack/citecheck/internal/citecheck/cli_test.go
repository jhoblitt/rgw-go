package citecheck_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// holding cites two ranges that hold at v1.0.0, each in a link.
const holding = "# Plan\n\nRead::prepare is [rgw_fixture.cc:6-13](https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L6-L13);\n" +
	"Write::prepare is [here](https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L15-L19).\n"

// readPrepare is a code span citing Read::prepare where it holds at v1.0.0,
// and readPrepareLink the link --fix writes for it.
const (
	readPrepare     = "`src/rgw/rgw_fixture.cc:6-13@v1.0.0#Store::Object::Read::prepare`"
	readPrepareLink = "[rgw_fixture.cc:6-13](https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L6-L13)"
)

var _ = Describe("Run", func() {
	var (
		ceph           *cephFixture
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		ceph = newCephFixture()
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

	It("prints only the count and exits 0 when every citation holds", func(ctx SpecContext) {
		Expect(run(ctx, "--ceph", ceph.dir, markdown("plan.md", holding))).To(Equal(0))
		Expect(stdout.String()).To(Equal("citations checked: 2, failed: 0\n"))
	})

	It("prints FILE:LINE, the citation and its reason for each that fails, then the count, and exits 1", func(ctx SpecContext) {
		good := markdown("good.md", holding)
		bad := markdown("bad.md", "Moved:\n\n- see https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_nope.cc#L3.\n"+
			"- [rgw_fixture.cc:8](https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L48)\n")
		err := citecheck.Run(ctx, []string{"--ceph", ceph.dir, good, bad}, nil, stdout, stderr)
		Expect(citecheck.ExitStatus(err)).To(Equal(1))
		Expect(err).To(MatchError("2 of 4 citations failed"))
		Expect(stdout.String()).To(Equal(
			bad + ":3: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_nope.cc#L3: " +
				"path src/rgw/rgw_nope.cc does not exist at v1.0.0\n" +
				bad + ":4: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L48: " +
				"line 48 is past the end of src/rgw/rgw_fixture.cc, which has 43 lines at v1.0.0\n" +
				"citations checked: 4, failed: 2\n"))
	})

	It("fails every code span citation as not a link, one that holds and one that is malformed alike, "+
		"and leaves the file as it is", func(ctx SpecContext) {
		text := "Read " + readPrepare + ", not `:6716-6727@v20.2.4`.\n"
		md := markdown("spans.md", text)
		Expect(run(ctx, "--ceph", ceph.dir, md)).To(Equal(1))
		Expect(stdout.String()).To(Equal(
			md + ":1: src/rgw/rgw_fixture.cc:6-13@v1.0.0#Store::Object::Read::prepare: not a link\n" +
				md + ":1: :6716-6727@v20.2.4: not a link\n" +
				"citations checked: 2, failed: 2\n"))
		Expect(read(md)).To(Equal(text))
	})

	DescribeTable("fails a link a code span holds as not a link, and leaves the file as it is",
		func(ctx SpecContext, summary string, args ...string) {
			text := "Read (`[rgw_fixture.cc:6-13](https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L6-L13)`).\n"
			md := markdown("coded.md", text)
			Expect(run(ctx, append(args, "--ceph", ceph.dir, md)...)).To(Equal(1))
			Expect(stdout.String()).To(Equal(md + ":1: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L6-L13: " +
				inCodeSpan + "\n" + summary))
			Expect(read(md)).To(Equal(text))
		},
		Entry("by default", "citations checked: 1, failed: 1\n"),
		Entry("under --form-only", "citations checked: 1, failed: 1\n", "--form-only"),
		Entry("under --fix, which reports it", "citations checked: 1, failed: 1, rewritten: 0\n", "--fix"),
	)

	It("passes a file with no citations", func(ctx SpecContext) {
		Expect(run(ctx, "--ceph", ceph.dir, markdown("prose.md", "No citations here.\n"))).To(Equal(0))
		Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\n"))
	})

	It("reads the clone from CITECHECK_CEPH", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_CEPH", ceph.dir)
		Expect(run(ctx, markdown("plan.md", holding))).To(Equal(0))
	})

	It("takes --ceph over CITECHECK_CEPH", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_CEPH", GinkgoT().TempDir())
		Expect(run(ctx, "--ceph", ceph.dir, markdown("plan.md", holding))).To(Equal(0))
	})

	It("reads the clone from the config file's ceph", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_CEPH", "")
		cfg := markdown("citecheck.yaml", "ceph: "+ceph.dir+"\n")
		Expect(run(ctx, "--config", cfg, markdown("plan.md", holding))).To(Equal(0))
	})

	It("reads none of the RGW_GO_ settings meant for rgw-go", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_CEPH", ceph.dir)
		GinkgoT().Setenv("RGW_GO_CEPH_SRC", GinkgoT().TempDir())
		GinkgoT().Setenv("RGW_GO_CONFIG", filepath.Join(GinkgoT().TempDir(), "missing.yaml"))
		GinkgoT().Setenv("RGW_GO_LOG_FORMAT", "xml")
		Expect(run(ctx, markdown("plan.md", holding))).To(Equal(0))
	})

	DescribeTable("a refused command line exits 2 and checks nothing",
		func(ctx SpecContext, want string, args ...string) {
			err := citecheck.Run(ctx, args, nil, stdout, stderr)
			Expect(citecheck.ExitStatus(err)).To(Equal(2))
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(stdout.String()).To(BeEmpty())
		},
		Entry("no file", "no file given"),
		Entry("an unknown flag", "unknown flag: --nope", "--nope", "plan.md"),
		Entry("an unknown log format", `unknown log format "xml"`, "--log-format", "xml", "plan.md"),
		Entry("--fix with --form-only", "--fix and --form-only cannot be combined", "--fix", "--form-only", "plan.md"),
	)

	It("refuses --fix with --form-only when the environment sets one", func(ctx SpecContext) {
		GinkgoT().Setenv("CITECHECK_FORM_ONLY", "true")
		err := citecheck.Run(ctx, []string{"--fix", "plan.md"}, nil, stdout, stderr)
		Expect(err).To(MatchError(HavePrefix("--fix and --form-only cannot be combined")))
	})

	It("exits 2, before checking any file, when a file cannot be read", func(ctx SpecContext) {
		missing := filepath.Join(GinkgoT().TempDir(), "missing.md")
		err := citecheck.Run(ctx, []string{"--ceph", ceph.dir, markdown("plan.md", holding), missing}, nil, stdout, stderr)
		Expect(citecheck.ExitStatus(err)).To(Equal(2))
		Expect(err).To(MatchError(ContainSubstring("reading " + missing)))
		Expect(stdout.String()).To(BeEmpty())
	})

	It("exits 2 when --ceph is not a git repository", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		err := citecheck.Run(ctx, []string{"--ceph", dir, markdown("plan.md", holding)}, nil, stdout, stderr)
		Expect(citecheck.ExitStatus(err)).To(Equal(2))
		Expect(err).To(MatchError(HavePrefix(dir + " is not a git repository")))
	})

	Describe("--form-only", func() {
		It("passes a well-formed link without a clone, whether or not the clone would hold it", func(ctx SpecContext) {
			md := markdown("links.md", holding+"Gone: https://github.com/ceph/ceph/blob/v9.9.9/src/rgw/rgw_nope.cc#L3\n")
			Expect(run(ctx, "--form-only", "--ceph", GinkgoT().TempDir(), md)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 3, failed: 0\n"))
		})

		It("fails a code span citation as not a link, and a link whose form is wrong by its reason", func(ctx SpecContext) {
			md := markdown("forms.md", "- "+readPrepare+"\n"+
				"- https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L10-12\n"+
				"- https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L12-L10\n"+
				"- https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L0\n"+
				"- https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/../rgw/rgw_fixture.cc#L1\n")
			Expect(run(ctx, "--form-only", "--ceph", GinkgoT().TempDir(), md)).To(Equal(1))
			Expect(stdout.String()).To(Equal(
				md + ":1: src/rgw/rgw_fixture.cc:6-13@v1.0.0#Store::Object::Read::prepare: not a link\n" +
					md + ":2: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L10-12: " +
					"anchor #L10-12 is not #L<line> or #L<from>-L<to>\n" +
					md + ":3: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L12-L10: lines 12-10 run backwards\n" +
					md + ":4: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L0: " +
					"line 0 does not exist: lines count from 1\n" +
					md + ":5: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/../rgw/rgw_fixture.cc#L1: " +
					"path src/rgw/../rgw/rgw_fixture.cc is not a clean path from the repository's root\n" +
					"citations checked: 5, failed: 5\n"))
		})
	})

	Describe("--fix", func() {
		fixRun := func(ctx SpecContext, files ...string) int {
			return run(ctx, append([]string{"--fix", "--ceph", ceph.dir}, files...)...)
		}

		DescribeTable("rewrites a code span citation that holds into a link, and nothing else",
			func(ctx SpecContext, before, after string) {
				before = strings.ReplaceAll(before, "ABBREV", ceph.v1[:10])
				after = strings.ReplaceAll(after, "ABBREV", ceph.v1[:10])
				md := markdown("plan.md", before)
				Expect(fixRun(ctx, md)).To(Equal(0))
				Expect(stdout.String()).To(Equal("citations checked: 1, failed: 0, rewritten: 1\n"))
				Expect(read(md)).To(Equal(after))
			},
			Entry("a span with a symbol, which the link drops",
				"Read ("+readPrepare+").\n", "Read ("+readPrepareLink+").\n"),
			Entry("a span with no symbol",
				"Read `src/rgw/rgw_fixture.cc:15-19@v1.0.0`.\n",
				"Read [rgw_fixture.cc:15-19](https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L15-L19).\n"),
			Entry("a span citing one line",
				"At `src/rgw/rgw_fixture.cc:43@v2.0.0`:\n",
				"At [rgw_fixture.cc:43](https://github.com/ceph/ceph/blob/v2.0.0/src/rgw/rgw_fixture.cc#L43):\n"),
			Entry("a span whose range is one line",
				"At `src/rgw/rgw_fixture.cc:43-43@v2.0.0`:\n",
				"At [rgw_fixture.cc:43](https://github.com/ceph/ceph/blob/v2.0.0/src/rgw/rgw_fixture.cc#L43):\n"),
			Entry("a span in double backticks with blanks, its whole run replaced",
				"Read `` "+strings.Trim(readPrepare, "`")+" `` here.\n", "Read "+readPrepareLink+" here.\n"),
			Entry("a span at an abbreviated commit id",
				"Read `src/rgw/rgw_fixture.cc:6@ABBREV#Store::Object::Read::prepare`.\n",
				"Read [rgw_fixture.cc:6](https://github.com/ceph/ceph/blob/ABBREV/src/rgw/rgw_fixture.cc#L6).\n"),
			Entry("a span on a CRLF line wrapped among others, which keep their bytes",
				"# Plan\r\n\r\n- First,\r\n  "+readPrepare+"  \r\n  then\ttabs.\r\n",
				"# Plan\r\n\r\n- First,\r\n  "+readPrepareLink+"  \r\n  then\ttabs.\r\n"),
			Entry("a span in a table's cell, the row keeping its cells",
				"| Op | Where |\n| --- | --- |\n| read | "+readPrepare+" |\n",
				"| Op | Where |\n| --- | --- |\n| read | "+readPrepareLink+" |\n"),
		)

		It("rewrites a span beside a link and leaves the link as it was", func(ctx SpecContext) {
			link := "[rgw_fixture.cc:15](https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L15)"
			md := markdown("plan.md", "Read "+readPrepare+" ("+link+"), "+link+readPrepare+".\n")
			Expect(fixRun(ctx, md)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 4, failed: 0, rewritten: 2\n"))
			Expect(read(md)).To(Equal("Read " + readPrepareLink + " (" + link + "), " + link + readPrepareLink + ".\n"))
		})

		It("leaves a span in a fenced block alone and does not count it", func(ctx SpecContext) {
			text := "```md\n" + readPrepare + "\n```\n\n" + readPrepare + "\n"
			md := markdown("plan.md", text)
			Expect(fixRun(ctx, md)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 1, failed: 0, rewritten: 1\n"))
			Expect(read(md)).To(Equal("```md\n" + readPrepare + "\n```\n\n" + readPrepareLink + "\n"))
		})

		It("reports a span that fails and leaves it alone, rewrites the one that holds, and exits 1", func(ctx SpecContext) {
			moved := "`src/rgw/rgw_fixture.cc:8@v2.0.0#Store::Object::Read::prepare`"
			md := markdown("plan.md", "Moved: "+moved+";\nhere: "+readPrepare+".\n")
			err := citecheck.Run(ctx, []string{"--fix", "--ceph", ceph.dir, md}, nil, stdout, stderr)
			Expect(citecheck.ExitStatus(err)).To(Equal(1))
			Expect(err).To(MatchError("1 of 2 citations failed"))
			Expect(stdout.String()).To(Equal(
				md + ":1: src/rgw/rgw_fixture.cc:8@v2.0.0#Store::Object::Read::prepare: " +
					"the cited lines do not hold prepare, and no block enclosing them is headed by it\n" +
					"citations checked: 2, failed: 1, rewritten: 1\n"))
			Expect(read(md)).To(Equal("Moved: " + moved + ";\nhere: " + readPrepareLink + ".\n"))
		})

		It("reports a span with no path by that reason and leaves it alone", func(ctx SpecContext) {
			text := "At the other release, `:6059-6070@v19.2.6#RGWGetCORS::execute`.\n"
			md := markdown("short.md", text)
			Expect(fixRun(ctx, md)).To(Equal(1))
			Expect(stdout.String()).To(Equal(
				md + ":1: :6059-6070@v19.2.6#RGWGetCORS::execute: the citation has no path: write the file's path from " +
					"ceph's repository root before \":6059-6070@v19.2.6#RGWGetCORS::execute\"\n" +
					"citations checked: 1, failed: 1, rewritten: 0\n"))
			Expect(read(md)).To(Equal(text))
		})

		DescribeTable("reports a span that holds but cannot be rewritten where it stands, and leaves it alone",
			func(ctx SpecContext, text, why string) {
				ceph.git("tag", "release/v1.0.0", "v1.0.0")
				md := markdown("plan.md", text)
				Expect(fixRun(ctx, md)).To(Equal(1))
				Expect(stdout.String()).To(HaveSuffix(": " + why + "\n" + "citations checked: 1, failed: 1, rewritten: 0\n"))
				Expect(read(md)).To(Equal(text))
			},
			Entry("one that runs across a line break", "Read `\nsrc/rgw/rgw_fixture.cc:6-13@v1.0.0` here.\n",
				"the code span runs across a line break, which rewriting it would rewrap: put it on one line"),
			Entry("one that is a link's text", "Read ["+readPrepare+"](https://example.com/read).\n",
				"the code span is in a link's text, which cannot hold another link: write the text without it"),
			Entry(`one right after a "!", which would make the link an image`, "Read !"+readPrepare+".\n",
				`the code span follows a "!", which would make the link an image: escape it as "\!"`),
			Entry("one at a tag whose name holds a slash", "Read `src/rgw/rgw_fixture.cc:6-13@release/v1.0.0`.\n",
				`ref release/v1.0.0 holds a "/", which a link cannot tell from the path after it: cite its commit id`),
		)

		It("checks links as well and reports one that fails", func(ctx SpecContext) {
			md := markdown("plan.md", readPrepare+" https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L48\n")
			Expect(fixRun(ctx, md)).To(Equal(1))
			Expect(stdout.String()).To(Equal(
				md + ":1: https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L48: " +
					"line 48 is past the end of src/rgw/rgw_fixture.cc, which has 43 lines at v1.0.0\n" +
					"citations checked: 2, failed: 1, rewritten: 1\n"))
		})

		It("changes nothing on a second run", func(ctx SpecContext) {
			md := markdown("plan.md", "Read "+readPrepare+" and `src/rgw/rgw_fixture.cc:8@v2.0.0#Store::Object::Read::prepare`.\n")
			Expect(fixRun(ctx, md)).To(Equal(1))
			first := read(md)
			stdout.Reset()
			Expect(fixRun(ctx, md)).To(Equal(1))
			Expect(stdout.String()).To(HaveSuffix("citations checked: 2, failed: 1, rewritten: 0\n"))
			Expect(read(md)).To(Equal(first))
		})

		It("writes no file it rewrote nothing in, and keeps the mode of one it rewrote", func(ctx SpecContext) {
			links := markdown("links.md", holding)
			past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
			Expect(os.Chtimes(links, past, past)).To(Succeed())
			spans := markdown("spans.md", readPrepare+"\n")
			Expect(os.Chmod(spans, 0o640)).To(Succeed())
			Expect(fixRun(ctx, links, spans)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 3, failed: 0, rewritten: 1\n"))
			Expect(read(spans)).To(Equal(readPrepareLink + "\n"))
			Expect(os.Stat(links)).To(HaveField("ModTime()", BeTemporally("==", past)))
			info, err := os.Stat(spans)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o640)))
		})

		It("exits 2 when it cannot write a file it rewrote", func(ctx SpecContext) {
			if os.Geteuid() == 0 {
				Skip("root writes a read-only file")
			}
			md := markdown("plan.md", readPrepare+"\n")
			Expect(os.Chmod(md, 0o400)).To(Succeed())
			err := citecheck.Run(ctx, []string{"--fix", "--ceph", ceph.dir, md}, nil, stdout, stderr)
			Expect(citecheck.ExitStatus(err)).To(Equal(2))
			Expect(err).To(MatchError(HavePrefix("writing " + md)))
			Expect(read(md)).To(Equal(readPrepare + "\n"))
		})
	})
})
