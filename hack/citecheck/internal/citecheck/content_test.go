package citecheck_test

import (
	"bytes"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// fixtureHdr is src/rgw/rgw_hdr.cc at v20.2.5: get_acl at lines 2-8, which
// reads x-amz-acl in its CGI form at line 4 and a grant header at 5, and
// calls do_write_meta at 6.
const fixtureHdr = `// A fixture for citecheck's content specs.
int get_acl(req_state* s)
{
  const char* acl = s->info.env->get("HTTP_X_AMZ_ACL");
  const char* grant = s->info.env->get("HTTP_X_AMZ_GRANT_READ");
  int r = do_write_meta(s);
  return r;
}
`

// newContentFixture returns the Go specs' fixture with v20.2.5 after it,
// which adds rgw_hdr.cc.
func newContentFixture() *cephFixture {
	r := newGoFixture()
	r.write("src/rgw/rgw_hdr.cc", fixtureHdr)
	r.commit("hdr")
	r.git("tag", "v20.2.5")
	return r
}

const (
	opPath  = "src/rgw/rgw_op_fix.h"
	hdrPath = "src/rgw/rgw_hdr.cc"
	// noIdent is why a link beside which nothing counts is listed.
	noIdent = "listed: no identifier is named beside it to check its lines by"
)

var (
	// opS links class RGWOp, and gofix912 the lines of rgw_gofix.cc that
	// rgw_op_fix.h's 9-12, which name RGWOp, are in it.
	opS      = cephURL("v19.2.6", opPath, "L4-L14")
	gofix912 = cephURL("v19.2.6", gofixPath, "L9-L12")
	// loopAtT is complete's retry loop's lines at v19.2.6, linked at
	// v20.2.4, where they are prepare's end and complete's head.
	loopAtT = cephURL("v20.2.4", gofixPath, "L17-L23")
)

var _ = Describe("The content of links", func() {
	var (
		ceph           *cephFixture
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		ceph = newContentFixture()
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	// content runs --content on a file name holding src and returns what it
	// printed for each link, its file's name dropped, its summary line and
	// its exit status.
	content := func(ctx SpecContext, name, src string) (lines []string, summary string, status int) {
		f := markdown(name, src)
		status = citecheck.ExitStatus(citecheck.Run(ctx, []string{"--content", "--ceph", ceph.dir, f}, nil, stdout, stderr))
		out := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
		for _, l := range out[:len(out)-1] {
			lines = append(lines, strings.TrimPrefix(l, f+":"))
		}
		return lines, out[len(out)-1], status
	}

	DescribeTable("holds a link whose lines hold what its text names",
		func(ctx SpecContext, src string, links int) {
			lines, summary, status := content(ctx, "doc.md", src)
			Expect(lines).To(BeEmpty())
			Expect(summary).To(Equal(strings.NewReplacer("N", string(rune('0'+links))).Replace(
				"links checked: N, held: N, failed: 0, listed: 0")))
			Expect(status).To(Equal(0))
		},
		Entry("an identifier its own text names",
			"Opening is [`open_bucket_index_pool`]("+cephURL("v19.2.6", gofixPath, "L6")+").\n", 1),
		Entry("an identifier named beside it",
			"`RGWGoFix::prepare` opens the pool ([rgw_gofix.cc:4-11]("+prepareS+")).\n", 1),
		Entry("in the header of a block that encloses its lines",
			"`RGWGoFix::complete` retries ([rgw_gofix.cc:17-23]("+cephURL("v19.2.6", gofixPath, "L17-L23")+")).\n", 1),
		Entry("each of a run of citations, by the names that lead in to the run, and a [T] in its text",
			"`RGWGoFix::prepare` at each release ([rgw_gofix.cc:4-11]("+prepareS+"), [\\[T\\]:14-21]("+prepareT+")).\n", 2),
		Entry("each of a run a [T] before it marks, however far down the run",
			"`RGWGoFix::prepare` and `RGWGoFix::complete` are [T] [rgw_gofix.cc:14-21]("+prepareT+"), [:23-38]("+
				completeT+"), [:14-21]("+prepareT+").\n", 3),
		Entry("each of a run a code span citation's [T] marks",
			"`RGWGoFix::prepare` (`[T] rgw_gofix.cc:14-21`, [:14-21]("+prepareT+")); `RGWGoFix::complete` "+
				"([\\[T\\]:23-38]("+completeT+")).\n", 2),
		Entry(`an unmarked link at v20.2.4 an "at [T]" follows`,
			"`RGWGoFix::prepare` ([rgw_gofix.cc:14-21]("+prepareT+") at [T]); `RGWGoFix::complete` ([\\[T\\]:23-38]("+
				completeT+")).\n", 2),
		Entry("an unmarked link at v20.2.4 beside words that name v20.2.4",
			"As v20.2.4 does, `RGWGoFix::prepare` opens the pool ([:14-21]("+prepareT+")); `RGWGoFix::complete` "+
				"follows ([\\[T\\]:23-38]("+completeT+")).\n", 2),
		Entry("a header's name, in its CGI form",
			"`x-amz-acl` is read ([rgw_hdr.cc:4]("+cephURL("v20.2.5", hdrPath, "L4")+")).\n", 1),
		Entry(`a name with a "*", as a pattern`,
			"`HTTP_X_AMZ_GRANT_*` ([rgw_hdr.cc:5]("+cephURL("v20.2.5", hdrPath, "L5")+")).\n", 1),
		Entry(`a name of words joined by "_", as a run of a longer one's`,
			"`write_meta` is called ([rgw_hdr.cc:6]("+cephURL("v20.2.5", hdrPath, "L6")+")).\n", 1),
		Entry("a raw link in a Go comment, rgw-go's own names left out",
			"package fix\n\n// prepare opens the pool, as RGWGoFix::prepare does ("+prepareS+").\nfunc prepare() {}\n", 1),
		Entry("a code span's name with a \"*\" its own text names, as a pattern",
			"Grants are read ([`HTTP_X_AMZ_GRANT_*`]("+cephURL("v20.2.5", hdrPath, "L5")+")).\n", 1),
		Entry(`an "at <tag>" that closes its parenthesis, across another citation's own "at <tag>": declined`,
			"`RGWGoFix::prepare` ([rgw_gofix.cc:4-11]("+prepareS+"); [rgw_gofix.cc:4-11]("+prepareS+") at v19.2.6; "+
				"[rgw_gofix.cc:14-21]("+prepareT+"); at v20.2.4).\n", 3),
		Entry(`an "at <tag>" after it that another tag follows: declined`,
			"`RGWGoFix::prepare` ([rgw_gofix.cc:14-21]("+prepareT+") at v19.2.6 and v20.2.4).\n", 1),
		Entry(`an "at <tag>" after a run that another tag follows: declined for each`,
			"`RGWGoFix::prepare` ([rgw_gofix.cc:14-21]("+prepareT+"), [rgw_gofix.cc:4-11]("+prepareS+") at v19.2.6 and "+
				"v20.2.4).\n", 2),
		Entry("its own text naming two releases: declined",
			"`RGWGoFix::prepare` ([rgw_gofix.cc:14-21 at v19.2.6 and v20.2.4]("+prepareT+")).\n", 1),
		Entry(`an "at <tag>" of its own, which the one closing its parenthesis does not override`,
			"`RGWGoFix::prepare` ([rgw_gofix.cc:4-11]("+prepareS+") at v19.2.6; `open_bucket_index_pool`, "+
				"[rgw_gofix.cc:14-21]("+prepareT+"); at v20.2.4).\n", 2),
		Entry("an unmarked link at v20.2.4 beside words that name Tentacle by its codename",
			"On Tentacle `RGWGoFix::prepare` opens the pool ([rgw_gofix.cc:14-21]("+prepareT+")); `RGWGoFix::complete` "+
				"follows ([\\[T\\]:23-38]("+completeT+")).\n", 2),
		Entry(`an unmarked link at v20.2.4 "at both tags", a tie that names no one release`,
			"`RGWGoFix::prepare` ([rgw_gofix.cc:14-21]("+prepareT+") at both tags); `RGWGoFix::complete` "+
				"([\\[T\\]:23-38]("+completeT+")).\n", 2),
	)

	DescribeTable("fails a link whose text and lines disagree, and lists one it cannot judge",
		func(ctx SpecContext, name, src string, want []string, summary string, status int) {
			lines, gotSummary, gotStatus := content(ctx, name, src)
			Expect(lines).To(Equal(want))
			Expect(gotSummary).To(Equal(summary))
			Expect(gotStatus).To(Equal(status))
		},
		Entry("lines that hold none of the identifiers named beside it", "doc.md",
			"`write_meta` is retried ([rgw_gofix.cc:17-23]("+loopAtT+")).\n",
			[]string{"1: " + loopAtT + ": lines 17-23 of src/rgw/rgw_gofix.cc at v20.2.4 hold none of the identifiers " +
				"named beside it, write_meta, which stand elsewhere in it; the nearest line that holds one is 28; at " +
				"v19.2.6 they hold one"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("lines that hold none of those its own text names", "doc.md",
			"[`write_meta`]("+prepareS+") is retried.\n",
			[]string{"1: " + prepareS + ": lines 4-11 of src/rgw/rgw_gofix.cc at v19.2.6 hold none of the identifiers " +
				"its text names, write_meta; the nearest line that holds one is 18"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("a link that fails a check every link must pass", "doc.md",
			"`RGWGoFix::prepare` ([rgw_gofix.cc:40]("+cephURL("v19.2.6", gofixPath, "L40")+")).\n",
			[]string{"1: " + cephURL("v19.2.6", gofixPath, "L40") + ": line 40 is past the end of src/rgw/rgw_gofix.cc, " +
				"which has 28 lines at v19.2.6"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry(`an "at <tag>" after it that names another release`, "doc.md",
			"`RGWGoFix::prepare` ([rgw_gofix.cc:4-11]("+prepareS+") at v20.2.4).\n",
			[]string{"1: " + prepareS + `: its text names v20.2.4, by the "at v20.2.4" after it, but it links lines at v19.2.6`},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("a [T] in its text", "doc.md",
			"`RGWGoFix::prepare` ([\\[T\\]:4-11]("+prepareS+")).\n",
			[]string{"1: " + prepareS + ": its text names v20.2.4, by the [T] in its text, but it links lines at v19.2.6"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry(`the "at <tag>" that closes its parenthesis`, "doc.md",
			"`RGWGoFix::prepare` (`open`, [rgw_gofix.cc:4-11]("+prepareS+"); at v20.2.4).\n",
			[]string{"1: " + prepareS + `: its text names v20.2.4, by the "at v20.2.4" that closes its parenthesis, but it ` +
				"links lines at v19.2.6"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("an unmarked link at v20.2.4 in a paragraph that marks its Tentacle citations [T]", "doc.md",
			"`RGWGoFix::prepare` opens the pool ([:4-11]("+cephURL("v20.2.4", gofixPath, "L4-L11")+"), [\\[T\\]:14-21]("+
				prepareT+")).\n",
			[]string{"1: " + cephURL("v20.2.4", gofixPath, "L4-L11") + ": its paragraph marks [T] its links at v20.2.4 " +
				"and its text is unmarked, but it links lines at v20.2.4"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("a file its text names that it does not link", "doc.md",
			"`RGWOp` ([rgw_op_fix.h:4-11]("+prepareS+")).\n",
			[]string{"1: " + prepareS + ": its text names rgw_op_fix.h, in its text, but it links src/rgw/rgw_gofix.cc"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("a name its file lacks that the same lines of a file its paragraph links hold", "doc.md",
			"`RGWOp` is declared in [rgw_op_fix.h:4-14]("+opS+"); `RGWOp` links its next ([:9-12]("+gofix912+")).\n",
			[]string{"1: " + gofix912 + ": none of the identifiers named beside it, RGWOp, stands in src/rgw/rgw_gofix.cc " +
				"at v19.2.6, but lines 9-12 of src/rgw/rgw_op_fix.h, which its paragraph names, hold RGWOp"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("lines alone that continue a citation of another file, with nothing beside them", "doc.md",
			"See [rgw_op_fix.h:4-14]("+opS+") and [:9-12]("+gofix912+").\n",
			[]string{
				"1: " + opS + ": " + noIdent,
				"1: " + gofix912 + ": its text names src/rgw/rgw_op_fix.h, as the continuation of " + opS + " before it, " +
					"but it links src/rgw/rgw_gofix.cc, and no identifier is named beside it to check its lines by",
			},
			"links checked: 2, held: 0, failed: 1, listed: 1", 1),
		Entry("a raw link in a Go comment whose lines hold none of the names beside it", "fix.go",
			"package fix\n\n// complete retries write_meta ("+prepareS+").\nfunc complete() {}\n",
			[]string{"3: " + prepareS + ": lines 4-11 of src/rgw/rgw_gofix.cc at v19.2.6 hold none of the identifiers " +
				"named beside it, write_meta, which stand elsewhere in it; the nearest line that holds one is 18"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("a tag right before it that names another release", "doc.md",
			"`RGWGoFix::prepare` opens the pool (v20.2.4 [rgw_gofix.cc:4-11]("+prepareS+")).\n",
			[]string{"1: " + prepareS + ": its text names v20.2.4, right before it, but it links lines at v19.2.6"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("one line that holds none of those its own text names", "doc.md",
			"[`write_meta`]("+cephURL("v19.2.6", gofixPath, "L6")+") is retried.\n",
			[]string{"1: " + cephURL("v19.2.6", gofixPath, "L6") + ": line 6 of src/rgw/rgw_gofix.cc at v19.2.6 holds none " +
				"of the identifiers its text names, write_meta; the nearest line that holds one is 18"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry(`an "at <tag>" after it, the next tag being the next link's own, tied right before it`, "doc.md",
			"`RGWGoFix::prepare` ([rgw_gofix.cc:14-21]("+prepareT+") at v19.2.6, v20.2.4 [rgw_gofix.cc:14-21]("+
				prepareT+")).\n",
			[]string{"1: " + prepareT + `: its text names v19.2.6, by the "at v19.2.6" after it, but it links lines at ` +
				"v20.2.4"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry(`an "at <tag>" after it and, after "and", the next link's own tag`, "doc.md",
			"`RGWGoFix::prepare` ([rgw_gofix.cc:14-21]("+prepareT+") at v19.2.6 and v20.2.4 [rgw_gofix.cc:14-21]("+
				prepareT+")).\n",
			[]string{"1: " + prepareT + `: its text names v19.2.6, by the "at v19.2.6" after it, but it links lines at ` +
				"v20.2.4"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("an unmarked link at v20.2.4 in a [T] paragraph, beside words that name both releases", "doc.md",
			"`RGWGoFix::prepare` parses on Tentacle and fails on Squid ([:14-21]("+prepareT+")); `RGWGoFix::complete` "+
				"follows ([\\[T\\]:23-38]("+completeT+")).\n",
			[]string{"1: " + prepareT + ": its paragraph marks [T] its links at v20.2.4 and its text is unmarked, but " +
				"it links lines at v20.2.4"},
			"links checked: 2, held: 1, failed: 1, listed: 0", 1),
		Entry("one line that holds none of the names beside it, which the same line at the other release holds",
			"doc.md",
			"`write_meta` is retried ([rgw_gofix.cc:18]("+cephURL("v20.2.4", gofixPath, "L18")+")).\n",
			[]string{"1: " + cephURL("v20.2.4", gofixPath, "L18") + ": line 18 of src/rgw/rgw_gofix.cc at v20.2.4 holds " +
				"none of the identifiers named beside it, write_meta, which stand elsewhere in it; the nearest line " +
				"that holds one is 28; at v19.2.6 it holds one"},
			"links checked: 1, held: 0, failed: 1, listed: 0", 1),
		Entry("a qualified name its file lacks that the same lines of a file its section links hold", "doc.md",
			"See [rgw_gofix.cc:13-28]("+completeS+").\n\n`RGWGoFix::prepare` opens it ([rgw_op_fix.h:4]("+
				cephURL("v19.2.6", opPath, "L4")+")).\n",
			[]string{
				"1: " + completeS + ": " + noIdent,
				"3: " + cephURL("v19.2.6", opPath, "L4") + ": none of the identifiers named beside it, RGWGoFix::prepare, " +
					"RGWGoFix, stands in src/rgw/rgw_op_fix.h at v19.2.6, but line 4 of src/rgw/rgw_gofix.cc, which its " +
					"section names, holds RGWGoFix::prepare",
			},
			"links checked: 2, held: 0, failed: 1, listed: 1", 1),
		Entry("nothing in its own table cell, though the row's other cell names code", "doc.md",
			"| Name | Where |\n| --- | --- |\n| `write_meta` | [rgw_gofix.cc:4-11]("+prepareS+") |\n",
			[]string{"3: " + prepareS + ": " + noIdent},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("a name more than 220 bytes before it in its sentence, which is not its", "doc.md",
			"`write_meta` "+strings.Repeat("and then the gateway goes on to read the rest of the request ", 4)+
				"([rgw_gofix.cc:4-11]("+prepareS+")).\n",
			[]string{"1: " + prepareS + ": " + noIdent},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("a name more than 100 bytes after it in its sentence, which is not its", "doc.md",
			"See [rgw_gofix.cc:4-11]("+prepareS+") "+strings.Repeat("and then the gateway goes on to read the rest of "+
				"the request ", 2)+"and `write_meta` too.\n",
			[]string{"1: " + prepareS + ": " + noIdent},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("words after it that lead to the next citation, which are that one's", "doc.md",
			"See [rgw_gofix.cc:4-11]("+prepareS+") and `write_meta` in [rgw_gofix.cc:17-23]("+
				cephURL("v19.2.6", gofixPath, "L17-L23")+").\n",
			[]string{"1: " + prepareS + ": " + noIdent},
			"links checked: 2, held: 1, failed: 0, listed: 1", 0),
		Entry("a name a package the Go file imports qualifies, which is rgw-go's", "fix.go",
			"package fix\n\nimport rgw \"strings\"\n\n// rgw.write_meta is rgw-go's own ("+prepareS+").\n"+
				"var _ = rgw.ToUpper\n",
			[]string{"5: " + prepareS + ": " + noIdent},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("nothing beside it, listed and not failed", "doc.md",
			"See [rgw_gofix.cc:4-11]("+prepareS+").\n",
			[]string{"1: " + prepareS + ": " + noIdent},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("only a common word beside it", "doc.md",
			"The `pool` is opened ([rgw_gofix.cc:4-11]("+prepareS+")).\n",
			[]string{"1: " + prepareS + ": listed: the words beside it that may name code, pool, are English words, " +
				"generic names or parts of a longer literal, and no whole code token stands beside it to check its " +
				"lines by"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("names its file lacks, that no file its paragraph names holds on those lines", "doc.md",
			"`RGWNope::runner` runs ([rgw_gofix.cc:4-11]("+prepareS+")).\n",
			[]string{"1: " + prepareS + ": listed: none of the identifiers named beside it, RGWNope::runner, RGWNope, " +
				"stands in src/rgw/rgw_gofix.cc at v19.2.6"},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
		Entry("only rgw-go's own names beside it, in a Go comment", "fix.go",
			"package fix\n\n// openPool opens the pool ("+prepareS+").\nfunc openPool() {}\n",
			[]string{"3: " + prepareS + ": " + noIdent},
			"links checked: 1, held: 0, failed: 0, listed: 1", 0),
	)

	It("ends a run in which a link fails with how many links failed", func(ctx SpecContext) {
		f := markdown("doc.md", "[`write_meta`]("+prepareS+") is retried.\n")
		err := citecheck.Run(ctx, []string{"--content", "--ceph", ceph.dir, f}, nil, stdout, stderr)
		Expect(citecheck.ExitStatus(err)).To(Equal(1))
		Expect(err).To(MatchError("1 of 1 links failed"))
	})

	DescribeTable("refuses another mode with it, exiting 2 and checking nothing",
		func(ctx SpecContext, other string) {
			err := citecheck.Run(ctx, []string{"--content", other, "plan.md"}, nil, stdout, stderr)
			Expect(citecheck.ExitStatus(err)).To(Equal(2))
			Expect(err).To(MatchError(HavePrefix("--content and " + other + " cannot be combined")))
			Expect(stdout.String()).To(BeEmpty())
		},
		Entry("--fix", "--fix"),
		Entry("--form-only", "--form-only"),
		Entry("--markdown-plain", "--markdown-plain"),
	)
})
