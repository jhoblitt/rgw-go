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

		// record records the run files with meta into a result file in dir.
		record := func(ctx context.Context, name string, meta []string, files ...string) string {
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
			baseline = record(ctx, "baseline.json", []string{"release=squid", "s3tests_commit=aaa", "gateway=radosgw"},
				"testdata/run1.xml", "testdata/run2.xml")
		})

		It("exits 0 for the last baseline run recorded alone", func(ctx SpecContext) {
			cand := record(ctx, "cand.json", []string{"release=squid", "s3tests_commit=aaa", "gateway=rgw-go"}, "testdata/run2.xml")
			status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(BeEmpty())
		})

		It("exits 1 and prints each difference", func(ctx SpecContext) {
			first := record(ctx, "first.json", []string{"release=squid"}, "testdata/run1.xml")
			cand := record(ctx, "cand.json", []string{"release=squid"}, "testdata/run-short.xml")
			status, stdout, stderr := run(ctx, "diff", "--baseline", first, "--candidate", cand)
			Expect(status).To(Equal(1))
			Expect(stdout).To(Equal("s3tests.functional.test_s3::test_f: baseline passed, candidate missing\n"))
			Expect(stderr).To(ContainSubstring("1 differences"))
		})

		It("exits 2 when the meta differs, and compares under --allow-meta-drift", func(ctx SpecContext) {
			cand := record(ctx, "cand.json", []string{"release=squid", "s3tests_commit=bbb"}, "testdata/run2.xml")
			status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(2))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("s3tests_commit"))
			status, _, stderr = run(ctx, "diff", "--allow-meta-drift", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(0), stderr)
		})

		It("exits 2 when the candidate lacks a compared key the baseline carries", func(ctx SpecContext) {
			cand := record(ctx, "cand.json", []string{"release=squid"}, "testdata/run2.xml")
			status, _, stderr := run(ctx, "diff", "--allow-meta-drift", "--baseline", baseline, "--candidate", cand)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring("s3tests_commit: baseline aaa, candidate missing"))
		})

		It("skips the --known list's differences and fails on a stale entry", func(ctx SpecContext) {
			cand := record(ctx, "cand.json", []string{"release=squid", "s3tests_commit=aaa"}, "testdata/run-short.xml")
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
			func(ctx SpecContext, want string, args func() []string) {
				status, _, stderr := run(ctx, append([]string{"diff"}, args()...)...)
				Expect(status).To(Equal(2))
				Expect(stderr).To(ContainSubstring(want))
			},
			Entry("no baseline", "--baseline", func() []string { return []string{"--candidate", baseline} }),
			Entry("no candidate", "--candidate", func() []string { return []string{"--baseline", baseline} }),
			Entry("an unreadable candidate", "missing.json", func() []string {
				return []string{"--baseline", baseline, "--candidate", filepath.Join(dir, "missing.json")}
			}),
			Entry("a candidate that is not a result", "bad.json", func() []string {
				bad := filepath.Join(dir, "bad.json")
				Expect(os.WriteFile(bad, []byte("[]"), 0o600)).To(Succeed())
				return []string{"--baseline", baseline, "--candidate", bad}
			}),
			Entry("a candidate with no outcomes", "no outcomes", func() []string {
				empty := filepath.Join(dir, "empty.json")
				Expect(os.WriteFile(empty, []byte(`{"suite": "s3tests", "meta": {}, "outcomes": {}}`), 0o600)).To(Succeed())
				return []string{"--baseline", baseline, "--candidate", empty}
			}),
			Entry("a candidate with an unknown outcome", "xfailed", func() []string {
				odd := filepath.Join(dir, "odd.json")
				Expect(os.WriteFile(odd, []byte(`{"suite": "s3tests", "outcomes": {"t1": "xfailed"}}`), 0o600)).To(Succeed())
				return []string{"--baseline", baseline, "--candidate", odd}
			}),
			Entry("a known list that does not compile", "line 1", func() []string {
				known := filepath.Join(dir, "known.txt")
				Expect(os.WriteFile(known, []byte("t[\n"), 0o600)).To(Succeed())
				return []string{"--baseline", baseline, "--candidate", baseline, "--known", known}
			}),
			Entry("a stray argument", "stray", func() []string { return []string{"--baseline", baseline, "--candidate", baseline, "stray"} }),
		)
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
