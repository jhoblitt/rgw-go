package parity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// run runs the parity command with args and returns its exit status, what
// it printed, and the error main would print. Run installs a default logger,
// so the previous one, and the log package's output it redirects, come back
// when the spec ends.
func run(ctx context.Context, args ...string) (status int, stdout, errText string) {
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	var out, errOut bytes.Buffer
	err := parity.Run(ctx, args, strings.NewReader(""), &out, &errOut)
	if err != nil {
		errText = err.Error()
	}
	return parity.ExitStatus(err), out.String(), errOut.String() + errText
}

// readResult reads a result file the record command wrote.
func readResult(path string) parity.Result {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	var res parity.Result
	Expect(json.Unmarshal(data, &res)).To(Succeed())
	return res
}

// candMeta returns the meta pairs of a squid run at the s3-tests commit aaa
// on 19.2.6, and extra.
func candMeta(extra ...string) []string {
	return append([]string{"release=squid", "s3tests_commit=aaa", "ceph_version=19.2.6"}, extra...)
}

// ledgerArgs returns the command line that appends the run files to the
// ledger at path as radosgw's, with flags.
func ledgerArgs(path string, flags []string, files ...string) []string {
	args := []string{"record", "--format", "junit", "--suite", "s3tests", "--ledger", path}
	for _, kv := range candMeta("gateway=radosgw") {
		args = append(args, "--meta", kv)
	}
	return append(append(args, flags...), files...)
}

// appendLedger appends the run files to the ledger at path, failing the
// spec unless the record succeeds, and returns path.
func appendLedger(ctx context.Context, path string, flags []string, files ...string) string {
	status, _, stderr := run(ctx, ledgerArgs(path, flags, files...)...)
	Expect(status).To(Equal(0), stderr)
	return path
}

// readLedger reads the ledger at path.
func readLedger(path string) *parity.Ledger {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	l, err := parity.ParseLedger(data)
	Expect(err).NotTo(HaveOccurred())
	return l
}

// writeLedgerFile writes l to path.
func writeLedgerFile(path string, l *parity.Ledger) {
	data, err := parity.MarshalLedger(l)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
}

var _ = Describe("the parity command", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	Describe("record", func() {
		It("writes the result file with the given meta, the run count and the recording time", func(ctx SpecContext) {
			out := filepath.Join(dir, "squid.json")
			status, stdout, stderr := run(ctx, "record", "--format", "junit", "--suite", "s3tests", "--out", out,
				"--meta", "release=squid", "--meta", "pytest_freeze=a=b,c", "testdata/run1.xml", "testdata/run2.xml")
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(ContainSubstring("6 tests"))
			res := readResult(out)
			Expect(res.Suite).To(Equal("s3tests"))
			Expect(res.Meta).To(HaveKeyWithValue("release", "squid"))
			Expect(res.Meta).To(HaveKeyWithValue("pytest_freeze", "a=b,c"))
			Expect(res.Meta).To(HaveKeyWithValue("runs", "2"))
			recorded, err := time.Parse(time.RFC3339, res.Meta["recorded"])
			Expect(err).NotTo(HaveOccurred())
			Expect(recorded).To(BeTemporally("~", time.Now(), time.Minute))
			Expect(res.Outcomes).To(HaveLen(6))
			Expect(res.Unstable).To(Equal([]string{"s3tests.functional.test_s3::test_c"}))
		})

		It("writes an empty unstable list rather than null", func(ctx SpecContext) {
			out := filepath.Join(dir, "one.json")
			status, _, stderr := run(ctx, "record", "--format", "junit", "--suite", "s3tests", "--out", out, "testdata/run1.xml")
			Expect(status).To(Equal(0), stderr)
			data, err := os.ReadFile(out)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(`"unstable": []`))
		})

		It("takes a flag from its PARITY_ environment variable", func(ctx SpecContext) {
			GinkgoT().Setenv("PARITY_SUITE", "admin")
			out := filepath.Join(dir, "env.json")
			status, _, stderr := run(ctx, "record", "--format", "gotest", "--out", out, "testdata/gotest.jsonl")
			Expect(status).To(Equal(0), stderr)
			Expect(readResult(out).Suite).To(Equal("admin"))
		})

		It("takes --meta from the command line only, never PARITY_META or the config file", func(ctx SpecContext) {
			GinkgoT().Setenv("PARITY_META", "release=bogus gateway=bogus")
			cfg := filepath.Join(dir, "parity.yaml")
			Expect(os.WriteFile(cfg, []byte("suite: admin\nmeta:\n  - ceph_version=bogus\n"), 0o600)).To(Succeed())

			out := filepath.Join(dir, "none.json")
			status, _, stderr := run(ctx, "record", "--config", cfg, "--format", "gotest", "--out", out, "testdata/gotest.jsonl")
			Expect(status).To(Equal(0), stderr)
			res := readResult(out)
			Expect(res.Suite).To(Equal("admin"), "the config file's suite")
			Expect(res.Meta).To(SatisfyAll(HaveLen(2), HaveKey("runs"), HaveKey("recorded")))

			out = filepath.Join(dir, "given.json")
			status, _, stderr = run(ctx, "record", "--config", cfg, "--format", "gotest", "--out", out,
				"--meta", "release=squid", "testdata/gotest.jsonl")
			Expect(status).To(Equal(0), stderr)
			Expect(readResult(out).Meta).To(SatisfyAll(HaveLen(3), HaveKeyWithValue("release", "squid")))
		})

		It("exits 1 without writing when a run lacks a test", func(ctx SpecContext) {
			out := filepath.Join(dir, "short.json")
			status, _, stderr := run(ctx, "record", "--format", "junit", "--suite", "s3tests", "--out", out,
				"testdata/run1.xml", "testdata/run-short.xml")
			Expect(status).To(Equal(1))
			Expect(stderr).To(ContainSubstring("test_f"))
			Expect(out).NotTo(BeAnExistingFile())
		})

		DescribeTable("exits 2 on a bad command line",
			func(ctx SpecContext, want string, args ...string) {
				status, _, stderr := run(ctx, append([]string{"record"}, args...)...)
				Expect(status).To(Equal(2))
				Expect(stderr).To(ContainSubstring(want))
			},
			Entry("no format", "--format", "--suite", "s3tests", "--out", "x.json", "testdata/run1.xml"),
			Entry("an unknown format", "tap", "--format", "tap", "--suite", "s3tests", "--out", "x.json", "testdata/run1.xml"),
			Entry("no suite", "--suite", "--format", "junit", "--out", "x.json", "testdata/run1.xml"),
			Entry("no output file", "--out", "--format", "junit", "--suite", "s3tests", "testdata/run1.xml"),
			Entry("no run file", "run file", "--format", "junit", "--suite", "s3tests", "--out", "x.json"),
			Entry("a meta without =", "k=v", "--format", "junit", "--suite", "s3tests", "--out", "x.json", "--meta", "release", "testdata/run1.xml"),
			Entry("a meta with no value", "k=v", "--format", "junit", "--suite", "s3tests", "--out", "x.json", "--meta", "release=", "testdata/run1.xml"),
			Entry("a meta given twice", "release", "--format", "junit", "--suite", "s3tests", "--out", "x.json",
				"--meta", "release=squid", "--meta", "release=tentacle", "testdata/run1.xml"),
			Entry("a meta record sets itself", "runs", "--format", "junit", "--suite", "s3tests", "--out", "x.json", "--meta", "runs=3", "testdata/run1.xml"),
			Entry("an unknown flag", "unknown flag", "--fromat", "junit", "--suite", "s3tests", "--out", "x.json", "testdata/run1.xml"),
			Entry("a single-dash long flag", "unknown shorthand flag", "-format", "junit", "--suite", "s3tests", "--out", "x.json", "testdata/run1.xml"),
		)
	})

	Describe("diff", func() {
		var baseline string

		// result records the run files with meta into a result file in dir.
		result := func(ctx context.Context, name string, meta []string, files ...string) string {
			out := filepath.Join(dir, name)
			args := []string{"record", "--format", "junit", "--suite", "s3tests", "--out", out}
			for _, kv := range meta {
				args = append(args, "--meta", kv)
			}
			status, _, stderr := run(ctx, append(args, files...)...)
			Expect(status).To(Equal(0), stderr)
			return out
		}

		BeforeEach(func(ctx SpecContext) {
			baseline = appendLedger(ctx, filepath.Join(dir, "baseline.json"), nil, "testdata/run1.xml", "testdata/run2.xml")
		})

		It("exits 0 for the last baseline run recorded alone", func(ctx SpecContext) {
			cand := result(ctx, "cand.json", candMeta("gateway=rgw-go"), "testdata/run2.xml")
			status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(BeEmpty())
		})

		It("exits 1 and prints each difference", func(ctx SpecContext) {
			first := appendLedger(ctx, filepath.Join(dir, "first.json"), nil, "testdata/run1.xml", "testdata/run1.xml")
			cand := result(ctx, "cand.json", candMeta(), "testdata/run-short.xml")
			status, stdout, stderr := run(ctx, "diff", "--baseline", first, "--candidate", cand)
			Expect(status).To(Equal(1))
			Expect(stdout).To(Equal("s3tests.functional.test_s3::test_f: baseline passed, candidate missing\n"))
			Expect(stderr).To(ContainSubstring("1 differences"))
		})

		It("exits 1 naming each test whose entry is stale, and compares them under --allow-meta-drift", func(ctx SpecContext) {
			cand := result(ctx, "cand.json", []string{"release=squid", "s3tests_commit=aaa", "ceph_version=19.2.7"}, "testdata/run2.xml")
			status, stdout, _ := run(ctx, "diff", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring("s3tests.functional.test_s3::test_a: baseline stale, re-record\n"))
			status, _, stderr := run(ctx, "diff", "--allow-meta-drift", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(0), stderr)
		})

		It("exits 2 when the candidate ran at another source, and compares under --allow-meta-drift", func(ctx SpecContext) {
			cand := result(ctx, "cand.json", []string{"release=squid", "s3tests_commit=bbb", "ceph_version=19.2.6"}, "testdata/run2.xml")
			status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(2))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("s3tests_commit: baseline aaa, candidate bbb"))
			status, _, stderr = run(ctx, "diff", "--allow-meta-drift", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(0), stderr)
		})

		It("exits 2 when the candidate lacks the source key", func(ctx SpecContext) {
			cand := result(ctx, "cand.json", []string{"release=squid", "ceph_version=19.2.6"}, "testdata/run2.xml")
			status, _, stderr := run(ctx, "diff", "--allow-meta-drift", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring("s3tests_commit: baseline aaa, candidate missing"))
		})

		It("exits 2 for a baseline in the flat form, naming the conversion", func(ctx SpecContext) {
			flat := result(ctx, "flat.json", candMeta(), "testdata/run2.xml")
			cand := result(ctx, "cand.json", candMeta(), "testdata/run2.xml")
			status, _, stderr := run(ctx, "diff", "--baseline", flat, "--candidate", cand)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring("flat form, not a ledger: convert it with go run ./hack/parity migrate " + flat))
		})

		It("skips the --known list's differences and fails on a stale entry", func(ctx SpecContext) {
			cand := result(ctx, "cand.json", candMeta(), "testdata/run-short.xml")
			known := filepath.Join(dir, "known.txt")
			Expect(os.WriteFile(known, []byte("# rgw-go lacks test_f's feature\ns3tests.functional.test_s3::test_f\n"), 0o600)).To(Succeed())
			status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", cand, "--known", known)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(BeEmpty())

			Expect(os.WriteFile(known, []byte("s3tests.functional.test_s3::test_[af]\n"), 0o600)).To(Succeed())
			status, stdout, _ = run(ctx, "diff", "--baseline", baseline, "--candidate", cand, "--known", known)
			Expect(status).To(Equal(1))
			Expect(stdout).To(Equal("s3tests.functional.test_s3::test_a: known difference no longer differs\n"))
		})

		DescribeTable("exits 2 when it cannot compare",
			func(ctx SpecContext, want string, args func(cand string) []string) {
				cand := result(ctx, "cand.json", candMeta(), "testdata/run2.xml")
				status, _, stderr := run(ctx, append([]string{"diff"}, args(cand)...)...)
				Expect(status).To(Equal(2))
				Expect(stderr).To(ContainSubstring(want))
			},
			Entry("no baseline", "--baseline", func(cand string) []string { return []string{"--candidate", cand} }),
			Entry("no candidate", "--candidate", func(string) []string { return []string{"--baseline", baseline} }),
			Entry("an unreadable candidate", "missing.json", func(string) []string {
				return []string{"--baseline", baseline, "--candidate", filepath.Join(dir, "missing.json")}
			}),
			Entry("a candidate that is not a result", "bad.json", func(string) []string {
				bad := filepath.Join(dir, "bad.json")
				Expect(os.WriteFile(bad, []byte("[]"), 0o600)).To(Succeed())
				return []string{"--baseline", baseline, "--candidate", bad}
			}),
			Entry("a candidate with no outcomes", "no outcomes", func(string) []string {
				empty := filepath.Join(dir, "empty.json")
				Expect(os.WriteFile(empty, []byte(`{"suite": "s3tests", "meta": {}, "outcomes": {}}`), 0o600)).To(Succeed())
				return []string{"--baseline", baseline, "--candidate", empty}
			}),
			Entry("a candidate with an unknown outcome", "xfailed", func(string) []string {
				odd := filepath.Join(dir, "odd.json")
				Expect(os.WriteFile(odd, []byte(`{"suite": "s3tests", "outcomes": {"t1": "xfailed"}}`), 0o600)).To(Succeed())
				return []string{"--baseline", baseline, "--candidate", odd}
			}),
			Entry("a baseline that breaks the reason rule", "without a reason", func(cand string) []string {
				l := readLedger(baseline)
				id := "s3tests.functional.test_s3::test_a"
				flip := l.Tests[id][0]
				flip.Outcome = "failed"
				l.Tests[id] = append(l.Tests[id], flip)
				writeLedgerFile(baseline, l)
				return []string{"--baseline", baseline, "--candidate", cand}
			}),
			Entry("a known list that does not compile", "line 1", func(cand string) []string {
				known := filepath.Join(dir, "known.txt")
				Expect(os.WriteFile(known, []byte("t[\n"), 0o600)).To(Succeed())
				return []string{"--baseline", baseline, "--candidate", cand, "--known", known}
			}),
			Entry("a stray argument", "stray", func(cand string) []string {
				return []string{"--baseline", baseline, "--candidate", cand, "stray"}
			}),
		)
	})

	Describe("record --ledger", func() {
		var ledger string

		BeforeEach(func() {
			ledger = filepath.Join(dir, "squid.json")
		})

		It("starts a missing ledger, then appends nothing for an unchanged record", func(ctx SpecContext) {
			status, stdout, stderr := run(ctx, ledgerArgs(ledger, nil, "testdata/run1.xml", "testdata/run2.xml")...)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(ContainSubstring("pinned aaa, admitted base"))
			Expect(stdout).To(ContainSubstring("appended 6 entries, 0 needing a reason"))
			l := readLedger(ledger)
			Expect(l.Tests).To(HaveLen(6))
			Expect(l.Tests["s3tests.functional.test_s3::test_c"][0].Unstable).To(BeTrue())

			status, stdout, stderr = run(ctx, ledgerArgs(ledger, nil, "testdata/run1.xml", "testdata/run2.xml")...)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(ContainSubstring("appended 0 entries"))
		})

		It("exits 1 naming a flip without a reason, the ledger unchanged, and appends it with one", func(ctx SpecContext) {
			appendLedger(ctx, ledger, nil, "testdata/run1.xml", "testdata/run1.xml")
			before, err := os.ReadFile(ledger)
			Expect(err).NotTo(HaveOccurred())

			status, _, stderr := run(ctx, ledgerArgs(ledger, nil, "testdata/run2.xml", "testdata/run2.xml")...)
			Expect(status).To(Equal(1))
			Expect(stderr).To(ContainSubstring("s3tests.functional.test_s3::test_c: passed -> failed needs a reason"))
			after, err := os.ReadFile(ledger)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))

			reasons := filepath.Join(dir, "reasons.txt")
			Expect(os.WriteFile(reasons, []byte("s3tests.functional.test_s3::test_c radosgw fails it since 19.2.6\n"), 0o600)).To(Succeed())
			status, stdout, stderr := run(ctx, ledgerArgs(ledger, []string{"--reasons", reasons}, "testdata/run2.xml", "testdata/run2.xml")...)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(HaveSuffix("appended 1 entries, 1 needing a reason, for the 6 tests of 2 runs, 0 unstable, to " + ledger +
				"\ns3tests.functional.test_s3::test_c: passed -> failed: radosgw fails it since 19.2.6\n"))
		})

		Describe("a draft", func() {
			var draft string

			BeforeEach(func(ctx SpecContext) {
				appendLedger(ctx, ledger, nil, "testdata/run1.xml", "testdata/run1.xml")
				data, err := os.ReadFile(ledger)
				Expect(err).NotTo(HaveOccurred())
				draft = filepath.Join(dir, "draft.json")
				Expect(os.WriteFile(draft, data, 0o600)).To(Succeed())
				status, stdout, stderr := run(ctx, ledgerArgs(draft, []string{"--draft", "recorded by CI"}, "testdata/run2.xml", "testdata/run2.xml")...)
				Expect(status).To(Equal(0), stderr)
				Expect(stdout).To(ContainSubstring("s3tests.functional.test_s3::test_c: passed -> failed: unreviewed: recorded by CI\n"))
			})

			It("is refused by validate and check, and accepted by check --draft", func(ctx SpecContext) {
				status, stdout, _ := run(ctx, "validate", draft)
				Expect(status).To(Equal(1))
				Expect(stdout).To(Equal(draft + `: s3tests.functional.test_s3::test_c entry 2: the draft's reason "unreviewed: recorded by CI"; ` +
					"replace it with the change's own before committing\n"))
				status, stdout, _ = run(ctx, "check", "--old", ledger, "--new", draft)
				Expect(status).To(Equal(1))
				Expect(stdout).To(ContainSubstring(`the draft's reason "unreviewed: recorded by CI"`))
				status, stdout, stderr := run(ctx, "check", "--draft", "--list-reasoned", "--old", ledger, "--new", draft)
				Expect(status).To(Equal(0), stderr)
				Expect(stdout).To(Equal("s3tests.functional.test_s3::test_c: passed -> failed: unreviewed: recorded by CI\n"))
			})

			It("is a baseline diff compares with", func(ctx SpecContext) {
				cand := filepath.Join(dir, "cand.json")
				args := []string{"record", "--format", "junit", "--suite", "s3tests", "--out", cand}
				for _, kv := range candMeta("gateway=rgw-go") {
					args = append(args, "--meta", kv)
				}
				status, _, stderr := run(ctx, append(args, "testdata/run2.xml")...)
				Expect(status).To(Equal(0), stderr)
				status, stdout, stderr := run(ctx, "diff", "--baseline", draft, "--candidate", cand)
				Expect(status).To(Equal(0), stderr)
				Expect(stdout).To(BeEmpty())
			})
		})

		It("never reads --draft from PARITY_DRAFT", func(ctx SpecContext) {
			GinkgoT().Setenv("PARITY_DRAFT", "from the environment")
			appendLedger(ctx, ledger, nil, "testdata/run1.xml", "testdata/run1.xml")
			status, _, stderr := run(ctx, ledgerArgs(ledger, nil, "testdata/run2.xml", "testdata/run2.xml")...)
			Expect(status).To(Equal(1))
			Expect(stderr).To(ContainSubstring("needs a reason"))
		})

		It("leaves the tests a --subset record did not run alone", func(ctx SpecContext) {
			appendLedger(ctx, ledger, nil, "testdata/run1.xml", "testdata/run1.xml")
			status, stdout, stderr := run(ctx, ledgerArgs(ledger, []string{"--subset"}, "testdata/run-short.xml", "testdata/run-short.xml")...)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(ContainSubstring("appended 0 entries"))
			Expect(readLedger(ledger).Tests["s3tests.functional.test_s3::test_f"]).To(HaveLen(1))
		})

		DescribeTable("exits 2 on a bad command line",
			func(ctx SpecContext, want string, args ...string) {
				status, _, stderr := run(ctx, append([]string{"record", "--format", "junit", "--suite", "s3tests"}, args...)...)
				Expect(status).To(Equal(2))
				Expect(stderr).To(ContainSubstring(want))
			},
			Entry("one run file", "at least 2 run files", "--ledger", "l.json", "testdata/run1.xml"),
			Entry("both outputs", "exclude each other", "--ledger", "l.json", "--out", "x.json", "testdata/run1.xml", "testdata/run2.xml"),
			Entry("a ledger flag with --out", "are for --ledger", "--out", "x.json", "--subset", "testdata/run1.xml"),
			Entry("no output", "--out or --ledger is required", "testdata/run1.xml"),
		)
	})

	Describe("check", func() {
		var old, next string

		BeforeEach(func(ctx SpecContext) {
			old = appendLedger(ctx, filepath.Join(dir, "old.json"), nil, "testdata/run1.xml", "testdata/run1.xml")
			data, err := os.ReadFile(old)
			Expect(err).NotTo(HaveOccurred())
			next = filepath.Join(dir, "new.json")
			Expect(os.WriteFile(next, data, 0o600)).To(Succeed())
		})

		It("exits 0 for a ledger that only appends, listing the reasoned entries on request", func(ctx SpecContext) {
			reasons := filepath.Join(dir, "reasons.txt")
			Expect(os.WriteFile(reasons, []byte("s3tests.functional.test_s3::test_c radosgw fails it since 19.2.6\n"), 0o600)).To(Succeed())
			appendLedger(ctx, next, []string{"--reasons", reasons}, "testdata/run2.xml", "testdata/run2.xml")
			status, stdout, stderr := run(ctx, "check", "--old", old, "--new", next)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(BeEmpty())
			status, stdout, stderr = run(ctx, "check", "--old", old, "--new", next, "--list-reasoned")
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(Equal("s3tests.functional.test_s3::test_c: passed -> failed: radosgw fails it since 19.2.6\n"))
		})

		It("exits 1 naming an edited entry", func(ctx SpecContext) {
			l := readLedger(next)
			l.Tests["s3tests.functional.test_s3::test_a"][0].Outcome = "error"
			writeLedgerFile(next, l)
			status, stdout, _ := run(ctx, "check", "--old", old, "--new", next)
			Expect(status).To(Equal(1))
			Expect(stdout).To(Equal("s3tests.functional.test_s3::test_a entry 1: changed or moved; an entry is never edited\n"))
		})

		It("exits 2 for a file it cannot read as a ledger", func(ctx SpecContext) {
			status, _, stderr := run(ctx, "check", "--old", old, "--new", filepath.Join(dir, "missing.json"))
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring("missing.json"))
		})
	})

	Describe("validate", func() {
		It("exits 0 for valid ledgers and 1 naming each problem", func(ctx SpecContext) {
			good := appendLedger(ctx, filepath.Join(dir, "good.json"), nil, "testdata/run1.xml", "testdata/run2.xml")
			status, _, stderr := run(ctx, "validate", good, good)
			Expect(status).To(Equal(0), stderr)

			l := readLedger(good)
			l.Tests["s3tests.functional.test_s3::test_a"][0].Runs = 1
			bad := filepath.Join(dir, "bad.json")
			writeLedgerFile(bad, l)
			status, stdout, _ := run(ctx, "validate", good, bad)
			Expect(status).To(Equal(1))
			Expect(stdout).To(Equal(bad + ": s3tests.functional.test_s3::test_a entry 1: recorded from 1 runs, fewer than 2\n"))
		})

		It("exits 2 without a ledger to read", func(ctx SpecContext) {
			status, _, _ := run(ctx, "validate")
			Expect(status).To(Equal(2))
		})
	})

	Describe("migrate", func() {
		It("converts a flat baseline in place, and refuses a ledger", func(ctx SpecContext) {
			flat := filepath.Join(dir, "flat.json")
			args := []string{"record", "--format", "junit", "--suite", "s3tests", "--out", flat}
			for _, kv := range candMeta("gateway=radosgw") {
				args = append(args, "--meta", kv)
			}
			status, _, stderr := run(ctx, append(args, "testdata/run1.xml", "testdata/run2.xml")...)
			Expect(status).To(Equal(0), stderr)

			status, stdout, stderr := run(ctx, "migrate", flat)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(Equal("converted " + flat + ": 6 tests, pinned at aaa\n"))
			l := readLedger(flat)
			Expect(l.Tests["s3tests.functional.test_s3::test_c"]).To(ConsistOf(HaveField("Unstable", true)))
			Expect(l.Tests["s3tests.functional.test_s3::test_a"][0].Meta).To(Equal(map[string]string{"gateway": "radosgw"}))

			status, _, stderr = run(ctx, "migrate", flat)
			Expect(status).To(Equal(1))
			Expect(stderr).To(ContainSubstring("is a ledger already"))
		})
	})

	Describe("the log flags", func() {
		// record runs a record with args before the command's own, so that the
		// root's flags take effect.
		record := func(ctx context.Context, args ...string) (int, string) {
			args = append(args, "record", "--format", "junit", "--suite", "s3tests",
				"--out", filepath.Join(dir, "log.json"), "testdata/run1.xml")
			status, _, stderr := run(ctx, args...)
			return status, stderr
		}

		DescribeTable("install the default logger they name",
			func(ctx SpecContext, args []string, handler slog.Handler, enabled, disabled slog.Level) {
				status, stderr := record(ctx, args...)
				Expect(status).To(Equal(0), stderr)
				Expect(slog.Default().Handler()).To(BeAssignableToTypeOf(handler))
				Expect(slog.Default().Enabled(ctx, enabled)).To(BeTrue(), "level %s", enabled)
				Expect(slog.Default().Enabled(ctx, disabled)).To(BeFalse(), "level %s", disabled)
			},
			Entry("json at info by default", nil, &slog.JSONHandler{}, slog.LevelInfo, slog.LevelDebug),
			Entry("text at debug", []string{"--log-format=text", "--log-level=debug"}, &slog.TextHandler{}, slog.LevelDebug, slog.LevelDebug-1),
			Entry("json at error", []string{"--log-format=json", "--log-level=error"}, &slog.JSONHandler{}, slog.LevelError, slog.LevelWarn),
		)

		It("reads the level from PARITY_LOG_LEVEL", func(ctx SpecContext) {
			GinkgoT().Setenv("PARITY_LOG_LEVEL", "warn")
			status, stderr := record(ctx)
			Expect(status).To(Equal(0), stderr)
			Expect(slog.Default().Enabled(ctx, slog.LevelInfo)).To(BeFalse())
			Expect(slog.Default().Enabled(ctx, slog.LevelWarn)).To(BeTrue())
		})

		It("read the level from the config file PARITY_CONFIG names", func(ctx SpecContext) {
			cfg := filepath.Join(dir, "parity.yaml")
			Expect(os.WriteFile(cfg, []byte("log-level: error\n"), 0o600)).To(Succeed())
			GinkgoT().Setenv("PARITY_CONFIG", cfg)
			status, stderr := record(ctx)
			Expect(status).To(Equal(0), stderr)
			Expect(slog.Default().Enabled(ctx, slog.LevelWarn)).To(BeFalse())
			Expect(slog.Default().Enabled(ctx, slog.LevelError)).To(BeTrue())
		})

		DescribeTable("exit 2 on a value they do not know",
			func(ctx SpecContext, arg, want string) {
				status, stderr := record(ctx, arg)
				Expect(status).To(Equal(2))
				Expect(stderr).To(ContainSubstring(want))
				Expect(filepath.Join(dir, "log.json")).NotTo(BeAnExistingFile())
			},
			Entry("an unknown level", "--log-level=loud", "parsing log level"),
			Entry("an unknown format", "--log-format=xml", `unknown log format "xml"`),
		)
	})

	It("exits 2 on an unknown command", func(ctx SpecContext) {
		status, _, stderr := run(ctx, "compare")
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring(`unknown command "compare"`))
	})

	It("prints help naming both commands when given none", func(ctx SpecContext) {
		status, stdout, _ := run(ctx)
		Expect(status).To(Equal(0))
		Expect(stdout).To(ContainSubstring("record"))
		Expect(stdout).To(ContainSubstring("diff"))
	})
})
