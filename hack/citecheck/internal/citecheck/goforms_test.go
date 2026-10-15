package citecheck_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// notLinkTail is what a plain citation of a file of ceph's that fails says
// to write, after the paths it names, and farTail what a continuation of a
// file only a comment above names says, after the file.
const (
	notLinkTail = ": link it, or, if it cites rgw-go's own file, write that file's path from rgw-go's root"
	farTail     = ", which a comment before its own names: link it, or, if it is no line of that file, name the file it is " +
		"a line of before it in its own comment"
)

// probeContexts are the contexts the re-review's probes run in, and
// probeModes the file each names and its path: against the clone, and under
// --form-only, which reads the list of ceph's files.
var (
	probeContexts = []string{"e", "l", "s"}
	probeModes    = []struct{ file, path string }{{"rgw_gofix.cc", gofixPath}, {"rgw_op.cc", "src/rgw/rgw_op.cc"}}
)

// notLink is why a plain citation of ceph's file at path fails, and far why a
// continuation of a file only a comment above names fails.
func notLink(path string) string { return "not a link: ceph has " + path + notLinkTail }

func far(file string) string { return "not a link: it continues " + file + farTail }

// The forms of a plain citation in a Go comment that the Go-comment sweeps
// found by hand: a continuation whose file only an earlier comment names, a
// file split from its lines by a line break, a citation in a //nolint
// directive's explanation, one in a code block, a line reference in prose,
// and a continuation written after its tag.
var _ = Describe("Go comments' other citation forms", func() {
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

	// report returns what a run prints for the failures, each "<line>:
	// <citation>: <reason>" in the file f, and the counts.
	report := func(f, counts string, failures ...string) string {
		var b strings.Builder
		for _, l := range failures {
			b.WriteString(f + ":" + l + "\n")
		}
		return b.String() + counts + "\n"
	}

	const (
		gofixNotLink = "not a link: ceph has " + gofixPath + notLinkTail
		opNotLink    = "not a link: ceph has src/rgw/rgw_op.cc" + notLinkTail
		farReason    = "not a link: it continues rgw_gofix.cc" + farTail
		codeLink     = "not a link: it is in a code block, which godoc does not link: write it outside the code block"
		codeFix      = "unresolved: it is in a code block, which --fix does not rewrite and godoc does not link: link it by hand, outside the code block"
		proseFix     = "unresolved: it is a line reference in prose, which --fix does not rewrite: link it by hand"
		farFix       = "unresolved: its file, rgw_gofix.cc, is named only in a comment before its own, which --fix does not read a continuation's file from: link it by hand"
		form1        = "// prepare is RGWGoFix::prepare (rgw_gofix.cc:4-11 at v19.2.6).\nfunc prepare() {}\n\n// complete retries write_meta (:13-28 at v19.2.6).\nfunc complete() {}\n"
		form4        = "// The functions in rgw_gofix.cc, at v19.2.6:\n//\n//\tprepare    :4-11\n//\tcomplete   rgw_gofix.cc:13-28\nfunc prepare() {}\n"
		form5        = "// prepare in rgw_gofix.cc opens the pool (line 6) and returns early\n// (lines 7-9 and 11).\nfunc prepare() {}\n"
		notLinkText  = "not a link"
		// parenTail ends the reason of a citation only its parentheses make
		// one.
		parenTail = ", or, if it is no line number, write it outside parentheses"
	)

	DescribeTable("each fails as a plain citation of ceph's code",
		func(ctx SpecContext, comment string, n int, failures ...string) {
			f := markdown("check.go", "package fix\n\n"+comment)
			Expect(run(ctx, "--ceph", ceph.dir, f)).To(Equal(1))
			Expect(stdout.String()).To(Equal(report(f, "citations checked: "+strconv.Itoa(n)+", failed: "+strconv.Itoa(len(failures)), failures...)))
		},
		Entry("a continuation whose file only a comment before its own names", form1, 2,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink, "6: :13-28: "+farReason),
		Entry("a continuation whose file only a link in a comment before its own names",
			"// prepare is "+prepareS+".\nfunc prepare() {}\n\ntype complete struct {\n\t// tries is max_retries (:15).\n\ttries int\n}\n", 2,
			"7: :15: "+farReason+parenTail),
		Entry("a continuation whose file only a file named in a comment before its own names",
			"// The fixture is rgw_gofix.cc.\nvar a = 1\n\n// It retries write_meta (:15-17).\nvar b = 1\n", 1,
			"6: :15-17: "+farReason),
		Entry("a continuation, a single line, in parentheses right after a file named in its own comment, no clause "+
			"given, since outside them it follows its file with a blank alone between",
			"// prepare is in rgw_gofix.cc (:6).\nfunc prepare() {}\n", 1, "3: :6: "+gofixNotLink),
		Entry("a continuation, a single line, after a file named in its own comment, in parentheses",
			"// prepare is in rgw_gofix.cc, which opens the pool (:6).\nfunc prepare() {}\n", 1,
			"3: :6: "+gofixNotLink+parenTail),
		Entry(`a continuation, a single line, with "at <tag>" in parentheses after a file named in its own comment, no `+
			"clause given, since outside them it is still a citation",
			"// prepare in rgw_gofix.cc opens it (:6 at v19.2.6).\nfunc prepare() {}\n", 1, "3: :6: "+gofixNotLink),
		Entry(`a continuation, a single line, in parentheses inside another pair that "at <tag>" follows, after a file `+
			"named in its own comment, no clause given, since outside them it is still a citation",
			"// prepare in rgw_gofix.cc opens it (as x (:6)) at v19.2.6.\nfunc prepare() {}\n", 1, "3: :6: "+gofixNotLink),
		Entry("a continuation, a single line, in parentheses after a citation in its own comment, no clause given, since "+
			"outside them it is listed with it",
			"// prepare is rgw_gofix.cc:4-11 at v19.2.6, (:6).\nfunc prepare() {}\n", 2,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink, "3: :6: "+gofixNotLink),
		Entry("a continuation, a single line, right after a file named in its own comment, a blank alone between",
			"// prepare opens the pool as rgw_gofix.cc :6 does.\nfunc prepare() {}\n", 1, "3: :6: "+gofixNotLink),
		Entry("a continuation, a single line, listed with a citation in its own comment",
			"// prepare is rgw_gofix.cc:4-11 at v19.2.6, and :6 opens the pool.\nfunc prepare() {}\n", 2,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink, "3: :6: "+gofixNotLink),
		Entry(`a continuation, a single line, listed with a citation after a ";"`,
			"// prepare is rgw_gofix.cc:4, :6 at v19.2.6; :14, :16 at v20.2.4.\nfunc prepare() {}\n", 4,
			"3: rgw_gofix.cc:4: "+gofixNotLink, "3: :6: "+gofixNotLink, "3: :14: "+gofixNotLink, "3: :16: "+gofixNotLink),
		Entry(`a continuation, a single line, listed with a citation after "[T] "`,
			"// prepare is rgw_gofix.cc:6, [T] :16.\nfunc prepare() {}\n", 2,
			"3: rgw_gofix.cc:6: "+gofixNotLink, "3: :16: "+gofixNotLink),
		Entry(`a continuation, a single line, listed with a citation after a ";" and then "[T] "`,
			"// prepare is rgw_gofix.cc:4, :6; [T] :16.\nfunc prepare() {}\n", 3,
			"3: rgw_gofix.cc:4: "+gofixNotLink, "3: :6: "+gofixNotLink, "3: :16: "+gofixNotLink),
		Entry("a single line listed after a range, after a file its own comment names without lines, which is none",
			"// prepare in rgw_gofix.cc opens :4-11 and :6.\nfunc prepare() {}\n", 1, "3: :4-11: "+gofixNotLink),
		Entry("a continuation, a single line, listed after a marked continuation",
			"// prepare is rgw_gofix.cc:4 at v19.2.6, which also has [T]:14 and :16.\nfunc prepare() {}\n", 3,
			"3: rgw_gofix.cc:4: "+gofixNotLink, "3: [T]:14: "+gofixNotLink, "3: :16: "+gofixNotLink),
		Entry("a continuation, a single line, listed after a range continuation",
			"// prepare is rgw_gofix.cc:4-11 at v19.2.6, which never completes :5-6 and :8.\nfunc prepare() {}\n", 3,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink, "3: :5-6: "+gofixNotLink, "3: :8: "+gofixNotLink),
		Entry("a single line after words that follow a citation, outside parentheses, which is none",
			"// prepare is rgw_gofix.cc:4, table row :6.\nfunc prepare() {}\n", 1, "3: rgw_gofix.cc:4: "+gofixNotLink),
		Entry(`a continuation, a single line, listed with a citation past that one's "at <tag>"`,
			"// prepare is rgw_gofix.cc:4-11 at v19.2.6, :6 at v20.2.4 or :16.\nfunc prepare() {}\n", 3,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink, "3: :6: "+gofixNotLink, "3: :16: "+gofixNotLink),
		Entry("a continuation, a single line, under a header naming the file its own comment names",
			"// A bare line number below is v19.2.6's rgw_gofix.cc.\n\n// prepare in rgw_gofix.cc opens the pool at :6.\n"+
				"func prepare() {}\n", 1, "5: :6: "+gofixNotLink),
		Entry("a continuation, a single line, under a header naming no file, though its own comment names one",
			"// A bare line number below is v19.2.6's.\n\n// prepare in rgw_gofix.cc opens the pool at :6.\nfunc prepare() {}\n", 1,
			"5: :6: "+gofixNotLink),
		Entry(`a continuation, a single line, after a file named in its own comment, with "at <tag>" after it`,
			"// prepare in rgw_gofix.cc opens it, :6 at v19.2.6.\nfunc prepare() {}\n", 1, "3: :6: "+gofixNotLink),
		Entry("a continuation, a single line, after a file named in its own comment, with a marker",
			"// prepare in rgw_gofix.cc opens it, [T]:16.\nfunc prepare() {}\n", 1, "3: [T]:16: "+gofixNotLink),
		Entry(`a file and a ":" ending a line, and its lines starting the next, and a continuation after them`,
			"// prepare is RGWGoFix::prepare (rgw_gofix.cc:\n// 4-11 at v19.2.6, :14-21 at v20.2.4).\nfunc prepare() {}\n", 2,
			"3: rgw_gofix.cc: 4-11: "+gofixNotLink, "4: :14-21: "+gofixNotLink),
		Entry(`a file and a ":" ending a line, and a range starting the next, outside parentheses`,
			"// prepare is RGWGoFix::prepare, rgw_gofix.cc:\n// 4-11 at v19.2.6.\nfunc prepare() {}\n", 1,
			"3: rgw_gofix.cc: 4-11: "+gofixNotLink),
		Entry(`a file and a ":" ending a line, and a single line starting the next, in parentheses`,
			"// prepare opens the pool (rgw_gofix.cc:\n// 6 at v19.2.6).\nfunc prepare() {}\n", 1,
			"3: rgw_gofix.cc: 6: "+gofixNotLink+parenTail),
		Entry("a file ending a line, and a range starting the next, in parentheses",
			"// prepare is RGWGoFix::prepare (v19.2.6 rgw_gofix.cc\n// 4-11).\nfunc prepare() {}\n", 1,
			"3: rgw_gofix.cc 4-11: "+gofixNotLink+parenTail),
		Entry("a citation in the explanation of a //nolint directive after code",
			"var x = 1 //nolint:gochecknoglobals // as prepare does (rgw_gofix.cc:4-11 at v19.2.6)\n", 1,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink),
		Entry("a citation in the explanation of a //nolint directive on a line of its own",
			"//nolint:gochecknoglobals // as prepare does (rgw_gofix.cc:4-11 at v19.2.6)\nvar x = 1\n", 1,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink),
		Entry("a continuation in a //nolint directive whose file only a comment before it names",
			"// prepare is RGWGoFix::prepare (rgw_gofix.cc:4-11 at v19.2.6).\nfunc prepare() {\n\t_ = 1 //nolint:gosec // as (:6)\n}\n", 2,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink, "5: :6: "+farReason+parenTail),
		Entry("a continuation and a file's citation in a code block", form4, 2,
			"5: :4-11: "+gofixNotLink, "6: rgw_gofix.cc:13-28: "+gofixNotLink),
		Entry("a code block's continuations after a tab",
			"// The functions in rgw_gofix.cc, at v19.2.6:\n//\n//\tprepare\t:4-11\n//\tcomplete\t:13-28\nfunc prepare() {}\n", 2,
			"5: :4-11: "+gofixNotLink, "6: :13-28: "+gofixNotLink),
		Entry(`a code block whose lead-in names its file before a ":"`,
			"// The functions, in rgw_gofix.cc:\n//\n//\tprepare    :4-11\nfunc prepare() {}\n", 1,
			"5: :4-11: "+gofixNotLink),
		Entry("a link in a code block", "// For example:\n//\n//\t"+prepareS+"\nfunc prepare() {}\n", 1,
			"5: "+prepareS+": "+codeLink),
		Entry("each line or range of a line reference in prose in parentheses after its file", form5, 3,
			"3: line 6: "+gofixNotLink+parenTail, "4: lines 7-9: "+gofixNotLink+parenTail, "4: 11: "+gofixNotLink+parenTail),
		Entry("a line reference in prose in parentheses after a link",
			"// prepare is "+prepareS+", which opens the pool (line 6).\nfunc prepare() {}\n", 2,
			"3: line 6: "+gofixNotLink+parenTail),
		Entry("lines in parentheses after a citation in their own comment that hold a range",
			"// prepare is rgw_gofix.cc:4-11 at v19.2.6, which never completes (:5-6 and :8, :13-15 and :18).\n"+
				"func prepare() {}\n", 5,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink, "3: :5-6: "+gofixNotLink, "3: :8: "+gofixNotLink,
			"3: :13-15: "+gofixNotLink, "3: :18: "+gofixNotLink),
		Entry("a single line in parentheses that hold a link",
			"// prepare opens the pool ("+prepareS+", table row :6).\nfunc prepare() {}\n", 2,
			"3: :6: "+notLink(gofixPath)+parenTail),
		Entry("a single line in parentheses that hold a file's citation, after a citation in their own comment",
			"// prepare is rgw_gofix.cc:4-11 at v19.2.6 (rgw_gofix.cc:4, table row :6).\nfunc prepare() {}\n", 3,
			"3: rgw_gofix.cc:4-11: "+gofixNotLink, "3: rgw_gofix.cc:4: "+gofixNotLink, "3: :6: "+gofixNotLink+parenTail),
		Entry("a continuation standing bare after a comma in parentheses, below a comment that names its file",
			"// The fixture is rgw_gofix.cc.\nvar a = 1\n\n// It retries (write_meta, :18).\nvar b = 1\n", 1,
			"6: :18: "+farReason+parenTail),
		Entry("a continuation standing bare after a semicolon in parentheses, below a comment that names its file",
			"// The fixture is rgw_gofix.cc.\nvar a = 1\n\n// It retries (write_meta; :18).\nvar b = 1\n", 1,
			"6: :18: "+farReason+parenTail),
		Entry(`a continuation standing bare before " and " in parentheses, below a comment that names its file`,
			"// The fixture is rgw_gofix.cc.\nvar a = 1\n\n// It retries (:18 and write_meta).\nvar b = 1\n", 1,
			"6: :18: "+farReason+parenTail),
		Entry("a continuation standing bare after a marker and a blank in parentheses, below a comment that names its file",
			"// The fixture is rgw_gofix.cc.\nvar a = 1\n\n// It retries (prepare [S] :6, complete [T] :16).\nvar b = 1\n", 2,
			"6: :6: "+farReason+parenTail, "6: :16: "+farReason+parenTail),
		Entry("a continuation standing bare after a tag in parentheses, below a comment that names its file",
			"// The fixture is rgw_gofix.cc.\nvar a = 1\n\n// It retries (prepare at v19.2.6 :6, complete at v20.2.4 :16).\n"+
				"var b = 1\n", 2, "6: :6: "+farReason+parenTail, "6: :16: "+farReason+parenTail),
		Entry("a continuation written after its tag",
			"// prepare opens the pool (rgw_gofix.cc RGWGoFix::prepare, v19.2.6:4-11).\nfunc prepare() {}\n", 1,
			"3: v19.2.6:4-11: "+gofixNotLink),
	)

	DescribeTable("none is read where nothing ties the lines to a file of ceph's",
		func(ctx SpecContext, comment string) {
			f := markdown("check.go", "package fix\n\n"+comment)
			Expect(run(ctx, "--ceph", ceph.dir, f)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\n"))
		},
		Entry("a continuation whose file a comment before its own names, when that file is rgw-go's",
			"// See bucketops.go:12.\nvar a = 1\n\n// It listens on (:8080).\nvar b = 1\n"),
		Entry("a file ending a line, and a number, not a range, starting the next, in parentheses",
			"// prepare runs (in rgw_gofix.cc\n// 2).\nfunc prepare() {}\n"),
		Entry(`a file and a ":" ending a line, and a word starting with a digit starting the next`,
			"// prepare runs as rgw_gofix.cc:\n// 2nd pass.\nfunc prepare() {}\n"),
		Entry("a quoted key's value in a code block, though its comment names a file of ceph's",
			"// The record of rgw_gofix.cc's run:\n//\n//\t{\"runs\":2}\nvar x = 1\n"),
		Entry("a line reference in prose after rgw-go's file", "// bucketops.go opens the pool (line 6).\nvar x = 1\n"),
		Entry("a line reference in prose whose file another paragraph of its comment names",
			"// prepare is in rgw_gofix.cc.\n//\n// It opens the pool (line 6).\nfunc prepare() {}\n"),
		Entry(`"line" inside a longer word, in parentheses`, "// In rgw_gofix.cc (a deadline 30).\nvar x = 1\n"),
		Entry("a tag and a colon before words", "// rgw_gofix.cc, as v19.2.6: the pool opens.\nvar x = 1\n"),
		Entry("a //nolint directive with no explanation", "// prepare in rgw_gofix.cc.\nvar x = 1 //nolint:gochecknoglobals\n"),
		Entry("a clock time after a file of ceph's", "// In rgw_gofix.cc the job runs at 12:00 daily.\nvar x = 1\n"),
	)

	// The text the task review found failing as a citation, though it cites
	// no lines: FILE stands for a file of ceph's, rgw_gofix.cc against the
	// clone and rgw_op.cc, which the list of ceph's files has, under
	// --form-only, and LINK for a link to lines of it.
	DescribeTable("none is read in text that has no citation's shape, against the clone or under --form-only",
		func(ctx SpecContext, comment string, links int) {
			for _, mode := range []struct{ file, path string }{{"rgw_gofix.cc", gofixPath}, {"rgw_op.cc", "src/rgw/rgw_op.cc"}} {
				src := strings.NewReplacer("FILE", mode.file, "LINK", cephURL("v19.2.6", mode.path, "L4-L11")).Replace(comment)
				f := markdown("probe.go", "package fix\n\n"+src)
				args := []string{"--ceph", ceph.dir, f}
				if mode.file == "rgw_op.cc" {
					args = []string{"--form-only", "--ceph", GinkgoT().TempDir(), f}
				}
				stdout.Reset()
				Expect(run(ctx, args...)).To(Equal(0), "%s: %s", mode.file, stdout.String())
				Expect(stdout.String()).To(Equal("citations checked: "+strconv.Itoa(links)+", failed: 0\n"), mode.file)
			}
		},
		Entry("a port below a comment that links lines of ceph's (A1)",
			"// prepare is LINK.\nfunc prepare() {}\n\n// Serve listens on :8080 by default.\nfunc Serve() {}\n", 1),
		Entry("a port below a comment that names a file of ceph's (A2)",
			"// The gateway follows FILE.\nvar a = 1\n\n// The default address is :7480\nvar b = 1\n", 0),
		Entry("a port in backticks (A3)", "// The gateway follows FILE.\nvar a = 1\n\n// It listens on `:8080`.\nvar b = 1\n", 0),
		Entry("a port in quotes (A4)", "// The gateway follows FILE.\nvar a = 1\n\n// It listens on \":8080\".\nvar b = 1\n", 0),
		Entry("a port in a flag (A5)", "// The gateway follows FILE.\nvar a = 1\n\n// Run it with -addr=:8080.\nvar b = 1\n", 0),
		Entry("a port in a code block (A9)",
			"// The gateway follows FILE.\nvar a = 1\n\n// Usage:\n//\n//\trgw-go serve --listen :8080\nvar b = 1\n", 0),
		Entry("a port in a code block under a header convention",
			"// A bare line number below is v19.2.6's.\n\n// Usage:\n//\n//\trgw-go serve --listen :8080\nvar b = 1\n", 0),
		Entry("a port in a comment after code (A10)", "// The gateway follows FILE.\nvar a = 1\n\nvar b = 1 // on :8080\n", 0),
		Entry("a port in a //nolint directive's explanation (A11)",
			"// The gateway follows FILE.\nvar a = 1\n\nvar b = 1 //nolint:gochecknoglobals // binds :8080\n", 0),
		Entry("port 0 (A12)", "// The gateway follows FILE.\nvar a = 1\n\n// listen binds :0, an ephemeral port.\nvar b = 1\n", 0),
		Entry("a quoted key's value in prose below (G8)", "// The gateway follows FILE.\nvar a = 1\n\n// It writes {\"runs\":2}.\nvar b = 1\n", 0),
		Entry("another project's tag and a number (T1)",
			"// The gateway follows FILE.\nvar a = 1\n\n// as rook v1.15.3:120 sets it.\nvar b = 1\n", 0),
		Entry("a single line before the file its comment names after it (E3)",
			"// The gateway follows FILE.\nvar a = 1\n\n// At :240 of internal/s3/requestattrs.go it is read.\nvar b = 1\n", 0),
		Entry("a port after a file its own comment names (A8)", "// It serves FILE's ops, and listens on :7480.\nvar a = 1\n", 0),
		Entry("a quoted key's value after a file its own comment names (G2)",
			"// It reads FILE's record, {\"runs\":2}.\nvar a = 1\n", 0),
		Entry(`"line" and a number in prose outside parentheses (F1)`,
			"// The default is FILE's; line 3 of the table is the default.\nvar a = 1\n", 0),
		Entry(`"line" and a number in prose after a file, the line no line of it (F2)`,
			"// In FILE, line 2 holds the root element.\nvar a = 1\n", 0),
		Entry(`"lines" and a range in prose outside parentheses (F3)`, "// FILE prints lines 1-2 of the output.\nvar a = 1\n", 0),
		Entry(`"line" and a size (F7)`, "// FILE sends a header line 1 MiB long.\nvar a = 1\n", 0),
		Entry(`a file the list has, then "line" and a number (E8)`, "// In run.sh, line 12 sets it.\nvar a = 1\n", 0),
		Entry("a file ending a line, and a range of times starting the next (K1)",
			"// It retries FILE\n// 2-3 times.\nvar a = 1\n", 0),
		Entry("a file ending a line, and a range of percents starting the next (K2)",
			"// It costs FILE\n// 10-20% of the time.\nvar a = 1\n", 0),
		Entry(`a file and a ":" ending a line, and a status code starting the next (K3)`,
			"// It answers as in FILE:\n// 404 when the bucket is missing.\nvar a = 1\n", 0),
		Entry(`a file and a ":" ending a line, and a count starting the next (K6)`,
			"// The cases of FILE:\n// 3 cases follow.\nvar a = 1\n", 0),
		Entry("a YAML key's value in a code block (G3)", "// FILE's config:\n//\n//\tlisten: :7480\nvar a = 1\n", 0),
		Entry("a single line after a quoted name in a code block (G9)", "// FILE's counts:\n//\n//\t'Count' :3\nvar a = 1\n", 0),
		Entry("a port after a parenthesis that closes before it", "// The gateway (as FILE does) listens on :8080.\nvar a = 1\n", 0),
		Entry("a port after a parenthesis that never closes", "// The gateway (as FILE does, listens on :8080.\nvar a = 1\n", 0),
		Entry("a port standing bare after a parenthesis that never closes", "// The gateway (as FILE does, :8080, and so on.\nvar a = 1\n", 0),
		Entry("a port after a parenthesis that never closes, a closed pair after the port (NF3)",
			"// The gateway (as FILE does, listens on :8080 (by default).\nvar a = 1\n", 0),
		Entry("a port standing bare after a parenthesis that never closes, a closed pair after the port",
			"// The gateway (as FILE does, :8080, (by default).\nvar a = 1\n", 0),
		Entry(`a port between a closed parenthesis and an enumeration's ")"`,
			"// The gateway (as FILE does) listens on :8080, as 2) says.\nvar a = 1\n", 0),
		Entry(`a port standing bare between a closed parenthesis and an enumeration's ")"`,
			"// The gateway (as FILE does), :8080, as 2) says.\nvar a = 1\n", 0),
		Entry("a single line under a header naming another file, after a file its own comment names",
			"// A bare line number below is v19.2.6's rgw_side.cc.\n\n// prepare in FILE opens the pool at :6.\nvar a = 1\n", 0),
		Entry("an aside in parentheses after a link in its own comment", "// prepare is LINK, and the gateway (default :8080) serves it.\nvar a = 1\n", 1),
		Entry("an aside in parentheses on the line after a link in its own comment",
			"// prepare is LINK.\n// The gateway listens on its port (default :8080).\nvar a = 1\n", 1),
		Entry("two ports in one aside after a link in its own comment", "// prepare is LINK (listening on :7480 and :7443).\nvar a = 1\n", 1),
		Entry("a YAML key's value in a code block below a link", "// LINK reads:\n//\n//\tlisten: :7480\nvar a = 1\n", 1),
		Entry("a quoted key's range in a code block below a link", "// LINK reads:\n//\n//\t{\"span\":4-11}\nvar a = 1\n", 1),
		Entry(`a line reference in prose that an enumeration's ")" follows, in no parentheses`,
			"// In FILE the steps are a) lines 4-11) then b) the rest.\nvar a = 1\n", 0),
		Entry("a clock time below a comment that names a file of ceph's",
			"// The gateway follows FILE.\nvar a = 1\n\n// The job runs at 12:00.\nvar b = 1\n", 0),
	)

	// The re-review's probes (rereview-CL4-enforce-r1.md, R01-R30), each a
	// comment of which the first line is prose, run in three contexts: e,
	// below a package doc that names FILE; l, below one that links FILE's
	// lines; and s, its own first line naming FILE. FILE is rgw_gofix.cc
	// against the clone and rgw_op.cc under --form-only, and LINK a link to
	// lines of it.
	probeFile := func(ctx, comment string) (string, int) {
		lines := strings.SplitN(comment, "\n", 2)
		body := "// " + lines[0] + "\n"
		if len(lines) > 1 {
			body += lines[1]
		}
		links := strings.Count(comment, "LINK")
		switch ctx {
		case "e":
			return "// Package fix follows FILE.\npackage fix\n\n" + body + "var X = 1\n", links
		case "l":
			return "// Package fix follows LINK.\npackage fix\n\n" + body + "var X = 1\n", links + 1
		}
		return "package fix\n\n// In FILE, " + strings.TrimPrefix(body, "// ") + "var X = 1\n", links
	}
	runProbe := func(ctx SpecContext, src, file, path string) (int, string) {
		src = strings.NewReplacer("FILE", file, "LINK", cephURL("v19.2.6", path, "L4-L11")).Replace(src)
		f := markdown("probe.go", src)
		args := []string{"--ceph", ceph.dir, f}
		if file == "rgw_op.cc" {
			args = []string{"--form-only", "--ceph", GinkgoT().TempDir(), f}
		}
		stdout.Reset()
		status := run(ctx, args...)
		return status, stdout.String()
	}

	DescribeTable("the re-review's probes, in each context, hold against the clone and under --form-only",
		func(ctx SpecContext, comment string) {
			for _, c := range probeContexts {
				src, links := probeFile(c, comment)
				for _, m := range probeModes {
					status, out := runProbe(ctx, src, m.file, m.path)
					Expect(out).To(Equal("citations checked: "+strconv.Itoa(links)+", failed: 0\n"), "%s %s", c, m.file)
					Expect(status).To(Equal(0), "%s %s", c, m.file)
				}
			}
		},
		Entry("R01", "The gateway listens on its port (default :8080).\n"),
		Entry("R02", "The gateway binds its address (port :7480).\n"),
		Entry("R03", "The gateway serves TLS (see :443 below).\n"),
		Entry("R04", "The handler returns the body (lines 2-3 of the example).\n"),
		Entry("R05", "The status follows HTTP (RFC 7231 section 6.5.1).\n"),
		Entry("R06", "The key is the prefix (buf[2:4]).\n"),
		Entry("R07", "The table holds the grants (rows 10-20).\n"),
		Entry("R08", "A missing bucket is NoSuchBucket (HTTP 404).\n"),
		Entry("R09", "The ratio of reads to writes (3:1).\n"),
		Entry("R10", "The job runs daily (at 10:30).\n"),
		Entry("R11", "The format first appeared (v1.2.3).\n"),
		Entry("R12", "The gateway listens on [::1]:8080 by default ([::1]:8080).\n"),
		Entry("R13", "The record:\n//\n//\t{\"port\":8080,\n//\t \"range\":\"10-20\",\n//\t \"nested\": {\"port\":7480}}\n"),
		Entry("R14", "The config:\n//\n//\tlisten: \":8080\"\n//\tport: 7480\n//\tat: \"10:30\"\n"),
		Entry("R15", "The gateway listens on its port (default \":7480\").\n"),
		Entry("R16", "The gateway listens on its port (default `:7480`).\n"),
		Entry("R17", "Tests bind an ephemeral port (e.g. :0).\n"),
		Entry("R19", "The gateway listens (:8080/tcp).\n"),
		Entry("R20", "The operator sets it (rook v1.15.3:120).\n"),
		Entry("R21", "rgw_op.cc writes the status line first (line 1 of the response).\n"),
		Entry("R22", "The bucket is checked first (as in FILE:\n// 404 for a missing bucket).\n"),
		Entry("R23", "The op retries (as FILE does,\n// 2-3 times).\n"),
		Entry("R24", "prepare is LINK; the gateway listens on :8080.\n"),
		Entry("R25", "prepare opens the pool (LINK at v19.2.6) and binds :0 in tests.\n"),
		Entry("R27", "The client waits (timeout :30s).\n"),
		Entry("R28", "The gateway listens on its port (the :80 default).\n"),
		Entry("R29", "The gateway serves both (listening on :7480 and :7443).\n"),
		Entry("R30", "The probe speaks plain HTTP (HTTP/1.1 at :80).\n"),
	)

	DescribeTable("the re-review's port ranges fail in each context, a range being a citation's shape",
		func(ctx SpecContext, comment, text string) {
			for _, c := range probeContexts {
				src, links := probeFile(c, comment)
				for _, m := range probeModes {
					status, out := runProbe(ctx, src, m.file, m.path)
					Expect(status).To(Equal(1), "%s %s", c, m.file)
					Expect(out).To(ContainSubstring(": "+text+": not a link"), "%s %s", c, m.file)
					Expect(out).To(HaveSuffix("citations checked: "+strconv.Itoa(links+1)+", failed: 1\n"), "%s %s", c, m.file)
				}
			}
		},
		Entry("R18", "The gateway binds a range (ports :8000-8010).\n", ":8000-8010"),
		Entry("R26", "Usage:\n//\n//\trgw-go serve --ports :8000-8010\n", ":8000-8010"),
	)

	Describe("--fix", func() {
		DescribeTable("lists, and does not rewrite, those it cannot tell the file or lines of where they stand",
			func(ctx SpecContext, comment string, n, rewritten int, kept string, failures ...string) {
				f := markdown("fix.go", "package fix\n\n"+comment)
				Expect(run(ctx, "--fix", "--ceph", ceph.dir, f)).To(Equal(1))
				Expect(stdout.String()).To(Equal(report(f, "citations checked: "+strconv.Itoa(n)+", failed: "+strconv.Itoa(len(failures))+
					", rewritten: "+strconv.Itoa(rewritten), failures...)))
				Expect(read(f)).To(ContainSubstring(kept))
			},
			Entry("a continuation whose file only a comment before its own names", form1, 2, 1,
				"(:13-28 at v19.2.6)", "6: :13-28: "+farFix),
			Entry("a code block's citations", form4, 2, 0, "//\tprepare    :4-11\n//\tcomplete   rgw_gofix.cc:13-28\n",
				"5: :4-11: "+codeFix, "6: rgw_gofix.cc:13-28: "+codeFix),
			Entry("a line reference in prose", form5, 3, 0, "(line 6)",
				"3: line 6: "+proseFix, "4: lines 7-9: "+proseFix, "4: 11: "+proseFix),
		)

		DescribeTable("rewrites the others into links as it does any plain citation",
			func(ctx SpecContext, comment, after string) {
				f := markdown("fix.go", "package fix\n\n"+comment)
				Expect(run(ctx, "--fix", "--ceph", ceph.dir, f)).To(Equal(0))
				Expect(stdout.String()).To(Equal("citations checked: 1, failed: 0, rewritten: 1\n"))
				Expect(read(f)).To(Equal("package fix\n\n" + after))
			},
			Entry("a file split from its lines by a line break, the file's name not left before the link",
				"// prepare is RGWGoFix::prepare (rgw_gofix.cc:\n// 4-11 at v19.2.6).\nfunc prepare() {}\n",
				"// prepare is RGWGoFix::prepare\n// ("+prepareS+" at v19.2.6).\nfunc prepare() {}\n"),
			Entry("a citation in a //nolint directive's explanation, in place",
				"var x = 1 //nolint:gochecknoglobals // as prepare does (rgw_gofix.cc:4-11 at v19.2.6)\n",
				"var x = 1 //nolint:gochecknoglobals // as prepare does ("+prepareS+" at v19.2.6)\n"),
			Entry("a continuation written after its tag, at that tag",
				"// prepare opens the pool (rgw_gofix.cc RGWGoFix::prepare, v20.2.4:14-21).\nfunc prepare() {}\n",
				"// prepare opens the pool (rgw_gofix.cc RGWGoFix::prepare,\n// "+prepareT+").\nfunc prepare() {}\n"),
		)
	})

	Describe("--form-only, which has no clone, tells ceph's files by the list of them", func() {
		// The fixture's files are not ceph's, so these cite files the list
		// has, as src/rgw/rgw_op.cc.
		DescribeTable("and fails a plain citation of one in every form, whatever its ref",
			func(ctx SpecContext, comment string, n int, failures ...string) {
				f := markdown("form.go", "package fix\n\n"+comment)
				Expect(run(ctx, "--form-only", "--ceph", GinkgoT().TempDir(), f)).To(Equal(1))
				Expect(stdout.String()).To(Equal(report(f, "citations checked: "+strconv.Itoa(n)+", failed: "+strconv.Itoa(len(failures)), failures...)))
			},
			Entry("a file's citation with no ref", "// prepare (rgw_op.cc:4-11).\nfunc prepare() {}\n", 1,
				"3: rgw_op.cc:4-11: "+opNotLink),
			Entry("a continuation after a link, with no ref",
				"// prepare ("+cephURL("v19.2.6", "src/rgw/rgw_op.cc", "L4-L11")+"); complete (:13-28).\nfunc prepare() {}\n", 2,
				"3: :13-28: "+opNotLink),
			Entry("a bare file name of ceph's, with the remedy for one of rgw-go's (E7)", "// It runs run.sh:12.\nvar x = 1\n", 1,
				"3: run.sh:12: "+notLink("src/dmclock/benchmark/run.sh")),
			Entry("a continuation under a header convention, though its clause names two releases",
				"// A bare line number below is v19.2.6's.\n\n// prepare (the code at v19.2.6 and v20.2.4, :4-11).\nfunc prepare() {}\n", 1,
				"5: :4-11: "+notLinkText),
			Entry("a continuation whose file only a comment before its own names",
				"// prepare (rgw_op.cc:4-11).\nfunc prepare() {}\n\n// complete (:13-28).\nfunc complete() {}\n", 2,
				"3: rgw_op.cc:4-11: "+opNotLink, "6: :13-28: "+far("rgw_op.cc")),
			Entry("a file split from its lines by a line break", "// prepare (rgw_op.cc:\n// 4-11).\nfunc prepare() {}\n", 1,
				"3: rgw_op.cc: 4-11: "+opNotLink),
			Entry("a citation in a //nolint directive", "var x = 1 //nolint:gochecknoglobals // (rgw_op.cc:4-11)\n", 1,
				"3: rgw_op.cc:4-11: "+opNotLink),
			Entry("a code block's citations", "// The ops in rgw_op.cc, at v19.2.6:\n//\n//\tRGWOp   :4-11\nfunc prepare() {}\n", 1,
				"5: :4-11: "+opNotLink),
			Entry("a line reference in prose", "// RGWOp in rgw_op.cc (lines 4-11).\nfunc prepare() {}\n", 1,
				"3: lines 4-11: "+opNotLink+parenTail),
			Entry("a continuation written after its tag", "// RGWOp (rgw_op.cc, v19.2.6:4-11).\nfunc prepare() {}\n", 1,
				"3: v19.2.6:4-11: "+opNotLink),
			Entry("a file the list lacks, cited at a tag the list was not made at",
				"// prepare (rgw_unlisted.cc:4-11 at v18.2.4).\nfunc prepare() {}\n", 1,
				"3: rgw_unlisted.cc:4-11: cited at v18.2.4, which the list of ceph's files was not made at: link it, or add "+
					"the ref to CITECHECK_FILES_REFS"),
		)

		DescribeTable("and passes one of a file it cannot be ceph's",
			func(ctx SpecContext, comment string) {
				f := markdown("form.go", "package fix\n\n"+comment)
				Expect(run(ctx, "--form-only", "--ceph", GinkgoT().TempDir(), f)).To(Equal(0))
				Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\n"))
			},
			Entry("rgw-go's own files, and a Go file at a tag the list was not made at",
				"// See bucketops.go:12, internal/driver/put.go:3 at v19.2.6 and transfer.go:644 at v18.2.4.\nvar x = 1\n"),
			Entry("a file the list lacks, at a tag it was made at", "// rgw_unlisted.cc:4 at v19.2.6, :9.\nvar x = 1\n"),
			Entry("another repository's file at a tag the list was not made at",
				"// go-ceph's rados_unlisted.c:12 at v0.30.0.\nvar x = 1\n"),
		)

		It("passes the path from rgw-go's root of one of rgw-go's files, which ceph has too, and fails it outside a root "+
			"(E6)", func(ctx SpecContext) {
			root := GinkgoT().TempDir()
			for name, text := range map[string]string{
				"go.mod":                 "module github.com/jhoblitt/rgw-go\n\ngo 1.27\n",
				".github/dependabot.yml": "version: 2\n",
			} {
				Expect(os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o750)).To(Succeed())
				Expect(os.WriteFile(filepath.Join(root, name), []byte(text), 0o600)).To(Succeed())
			}
			src := "package fix\n\n// Dependabot reads .github/dependabot.yml:3 at v19.2.6.\nvar x = 1\n"
			f := filepath.Join(root, "internal", "fix", "fix.go")
			Expect(os.MkdirAll(filepath.Dir(f), 0o750)).To(Succeed())
			Expect(os.WriteFile(f, []byte(src), 0o600)).To(Succeed())
			Expect(run(ctx, "--form-only", "--ceph", GinkgoT().TempDir(), f)).To(Equal(0))
			Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\n"))

			stdout.Reset()
			outside := markdown("fix.go", src)
			Expect(run(ctx, "--form-only", "--ceph", GinkgoT().TempDir(), outside)).To(Equal(1))
			Expect(stdout.String()).To(Equal(outside + ":3: .github/dependabot.yml:3: " + notLink(".github/dependabot.yml") +
				"\ncitations checked: 1, failed: 1\n"))
		})
	})
})
