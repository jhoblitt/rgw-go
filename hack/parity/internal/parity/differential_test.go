package parity_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// canonical returns the bytes parity writes for a two-entry ledger whose
// test t1 passed, then failed for a reason.
func canonical() string {
	l := ledgerOf(map[string][]parity.Entry{
		"t1": {entry("aaa", "passed"), reasoned(entry("aaa", "failed"), "radosgw changed")},
	})
	data, err := parity.MarshalLedger(l)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

var _ = Describe("one reading of a ledger's bytes", func() {
	It("starts from the form parity writes", func() {
		_, err := parity.ParseLedger([]byte(canonical()))
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("refuses bytes another reader could read otherwise",
		func(edit func(string) string, want string) {
			_, err := parity.ParseLedger([]byte(edit(canonical())))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a member named twice, apart from case",
			func(s string) string {
				return strings.Replace(s, `{"outcome":"failed",`, `{"outcome":"failed","OUTCOME":"passed",`, 1)
			}, "a member name that differs from the schema's"),
		Entry("a member named apart from case",
			func(s string) string { return strings.Replace(s, `{"outcome":"failed",`, `{"Outcome":"failed",`, 1) },
			"a member name that differs from the schema's"),
		Entry("a member name written with an escape",
			func(s string) string {
				return strings.Replace(s, `{"outcome":"failed",`, `{"outc\u006fme":"failed",`, 1)
			},
			"not in the form parity writes"),
		Entry("CRLF line ends", func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") },
			"not in the form parity writes"),
		Entry("blank space around the value", func(s string) string { return "  " + s + "\n" },
			"not in the form parity writes"),
		Entry("a member named twice", func(s string) string {
			return strings.Replace(s, `{"outcome":"failed",`, `{"outcome":"failed","outcome":"passed",`, 1)
		}, "duplicate"),
		Entry("text that is not UTF-8", func(s string) string { return strings.Replace(s, "radosgw changed", "radosgw \xff changed", 1) },
			"not UTF-8"),
		Entry("a byte order mark", func(s string) string { return "\xef\xbb\xbf" + s }, "byte order mark"),
		Entry("a second value after the first", func(s string) string { return s + "{}\n" }, "after the value"),
	)

	It("rewrites a ledger in the form parity writes with fmt, and no other", func(ctx SpecContext) {
		file := filepath.Join(GinkgoT().TempDir(), "l.json")
		Expect(os.WriteFile(file, []byte(strings.ReplaceAll(canonical(), "\n", "\r\n")), 0o600)).To(Succeed())
		status, _, stderr := run(ctx, "validate", file)
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("go run ./hack/parity fmt " + file))
		status, _, stderr = run(ctx, "fmt", file)
		Expect(status).To(Equal(0), stderr)
		data, err := os.ReadFile(file)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(canonical()))

		Expect(os.WriteFile(file, []byte(strings.Replace(canonical(), `{"outcome":"failed",`,
			`{"outcome":"failed","OUTCOME":"passed",`, 1)), 0o600)).To(Succeed())
		status, _, stderr = run(ctx, "fmt", file)
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("a member name that differs from the schema's"))
	})

	It("refuses a flat baseline that names a member twice apart from case", func(ctx SpecContext) {
		file := filepath.Join(GinkgoT().TempDir(), "flat.json")
		Expect(os.WriteFile(file, []byte(`{"suite": "s3tests", "meta": {"release": "squid", "ceph_version": "19.2.6",
  "recorded": "2026-10-12T16:55:39Z", "runs": "2", "s3tests_commit": "aaa"},
  "outcomes": {"t1": "failed"}, "Outcomes": {"t1": "passed"}, "unstable": []}`), 0o600)).To(Succeed())
		status, _, stderr := run(ctx, "migrate", file)
		Expect(status).To(Equal(1))
		Expect(stderr).To(ContainSubstring("a member name that differs from the schema's"))
	})

	It("refuses a candidate that names a member twice apart from case", func(ctx SpecContext) {
		base := filepath.Join(GinkgoT().TempDir(), "base.json")
		writeLedgerFile(base, ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}}))
		cand := filepath.Join(GinkgoT().TempDir(), "cand.json")
		Expect(os.WriteFile(cand, []byte(`{"suite": "s3tests", "meta": {"release": "squid", "ceph_version": "19.2.6",
  "s3tests_commit": "aaa"}, "outcomes": {"t1": "failed"}, "Outcomes": {"t1": "passed"}, "unstable": []}`), 0o600)).To(Succeed())
		status, _, stderr := run(ctx, "diff", "--baseline", base, "--candidate", cand)
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("a member name that differs from the schema's"))
	})

	It("refuses, in check --base, an appended entry that names its outcome twice apart from case", func(ctx SpecContext) {
		f := newBaseFixture()
		data, err := os.ReadFile(filepath.Join(f.repo.dir, squidLedger))
		Expect(err).NotTo(HaveOccurred())
		t1 := `      {"outcome":"passed","source":"` + f.pin + `","ceph_version":"19.2.6","recorded":"2026-10-12T16:55:39Z","runs":2,"unstable":false}`
		Expect(string(data)).To(ContainSubstring(t1 + "\n"))
		hidden := `      {"outcome":"failed","OUTCOME":"passed","source":"` + f.pin + `","ceph_version":"19.2.6","recorded":"2026-10-12T16:55:39Z","runs":2,"unstable":false}`
		f.repo.put(squidLedger, []byte(strings.Replace(string(data), t1+"\n", t1+",\n"+hidden+"\n", 1)))
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring(squidLedger + ": parsing the ledger: a member name that differs from the schema's"))
	})

	It("flags a ledger outside the checked paths whatever the case of its member names", func(ctx SpecContext) {
		f := newBaseFixture()
		f.repo.put("hack/elsewhere.json", []byte(`{"Pins": [], "Tests": {}}`))
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring("hack/elsewhere.json: a ledger outside the paths the check covers"))
	})
})

var _ = Describe("the rules validate holds", func() {
	DescribeTable("refuses an entry's meta that repeats a field of the entry",
		func(key string) {
			l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "failed")}})
			l.Tests["t1"][0].Meta = map[string]string{key: "passed"}
			Expect(parity.Validate(l)).To(ContainElement("t1 entry 1: meta repeats " + key))
		},
		Entry("outcome", "outcome"), Entry("source", "source"), Entry("unstable", "unstable"),
		Entry("reason", "reason"), Entry("meta", "meta"),
	)

	DescribeTable("reads a draft's marker however it is written",
		func(reason string) {
			l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed"), reasoned(entry("aaa", "failed"), reason)}})
			Expect(parity.Validate(l)).To(ContainElement(ContainSubstring("t1 entry 2: the draft's reason")))
		},
		Entry("in capitals", "Unreviewed: recorded by CI"),
		Entry("after a blank-looking character", "\u2800unreviewed: recorded by CI"),
		Entry("with a space before the colon", "unreviewed : recorded by CI"),
	)

	DescribeTable("refuses a name a reader could take for another",
		func(id string) {
			l := ledgerOf(map[string][]parity.Entry{id: {entry("aaa", "passed")}})
			Expect(parity.Validate(l)).To(ContainElement(ContainSubstring("is not printable ASCII")))
		},
		Entry("a Cyrillic letter in a Latin id", "s3tests.functional.test_s3::test_\u0430"),
		Entry("a full-width colon", "s3tests.functional.test_s3\uff1a\uff1atest_a"),
	)
})

var _ = Describe("what parity prints, read back", func() {
	It("renders a backslash so that no escape it writes can be forged", func() {
		Expect(parity.SafeLine("a\\u000ab")).NotTo(Equal(parity.SafeLine("a\nb")))
		Expect(parity.SafeLine(`a\b`)).To(Equal(`a\\b`))
		Expect(parity.SafeLine(`\::x`)).NotTo(Equal(parity.SafeLine("::x")))
	})

	DescribeTable("writes a code span that Markdown reads back as the value",
		func(in, want string) {
			Expect(parity.CodeSpan(in)).To(Equal(want))
		},
		Entry("plain", "abc", "`abc`"),
		Entry("a leading and a trailing space, which CommonMark would strip", " x ", "`  x  `"),
		Entry("a backtick inside", "a`b", "``a`b``"),
		Entry("a leading backtick", "`a", "`` `a ``"),
	)
})
