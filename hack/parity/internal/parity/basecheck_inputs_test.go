package parity_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

var _ = Describe("check --base on what a pull request writes", func() {
	var f *baseFixture

	BeforeEach(func() {
		f = newBaseFixture()
	})

	Describe("values that reach git", func() {
		It("never hands git a pin source as an option", func(ctx SpecContext) {
			marker := filepath.Join(GinkgoT().TempDir(), "pwned")
			f.appendPin(squidLedger, "--upload-pack=touch "+marker, parity.AdmitAdditive)
			status, stdout, _, _ := f.check(ctx)
			Expect(marker).NotTo(BeAnExistingFile(), "git ran the pin source as --upload-pack")
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring(`pin 2's source "--upload-pack=touch ` + marker + `" starts with "-"`))
		})

		It("never hands git a base that reads as an option", func(ctx SpecContext) {
			f.base = "--output=" + filepath.Join(GinkgoT().TempDir(), "out")
			status, _, stderr, _ := f.check(ctx)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring(`the base "` + f.base + `" is not a commit`))
		})

		It("refuses a path outside the repository", func(ctx SpecContext) {
			outside := GinkgoT().TempDir()
			l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
			data, err := parity.MarshalLedger(l)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(outside, "x.json"), data, 0o600)).To(Succeed())
			rel, err := filepath.Rel(f.repo.dir, outside)
			Expect(err).NotTo(HaveOccurred())
			status, _, stderr, _ := f.check(ctx, rel)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring(rel + " is not a path inside the repository"))
		})

		It("checks a pin source as a full commit id before any git command runs", func(ctx SpecContext) {
			f.appendPin(squidLedger, "HEAD", parity.AdmitAdditive)
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring("pin 2 (HEAD, additive): the proof fails: HEAD is not a full commit id"))
		})
	})

	Describe("how it prints ledger text", func() {
		It("prints a test id that holds a newline and a workflow command as one escaped line", func(ctx SpecContext) {
			f.appendEntry("t9\n::add-mask::forged", entry(f.pin, "passed"))
			f.appendEntry("t9\n::add-mask::forged", entry(f.pin, "failed"))
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(workflowCommands(stdout)).To(BeEmpty())
			Expect(stdout).To(ContainSubstring(`t9\u000a::add-mask::forged`))
		})

		It("neutralizes a line that starts with a workflow command", func(ctx SpecContext) {
			old := filepath.Join(GinkgoT().TempDir(), "old.json")
			next := filepath.Join(GinkgoT().TempDir(), "new.json")
			l := ledgerOf(map[string][]parity.Entry{"::error::forged": {entry("aaa", "passed")}})
			writeLedgerFile(old, l)
			l.Tests["::error::forged"] = append(l.Tests["::error::forged"], entry("aaa", "failed"))
			writeLedgerFile(next, l)
			status, stdout, _ := run(ctx, "check", "--old", old, "--new", next)
			Expect(status).To(Equal(1))
			Expect(workflowCommands(stdout)).To(BeEmpty())
		})

		It("keeps a reason from closing the summary's code block", func(ctx SpecContext) {
			f.appendEntry("t1", reasoned(entry(f.pin, "failed"), "fixed\n```\n## forged heading"))
			_, _, _, summary := f.check(ctx)
			Expect(strings.Split(summary, "\n")).NotTo(ContainElement("## forged heading"))
			Expect(strings.Split(summary, "\n")).NotTo(ContainElement("```" + "\n" + "## forged heading"))
		})

		It("renders a pin source as code in the summary", func(ctx SpecContext) {
			l := f.repo.readLedgerAt(adminLedger)
			l.Pins = append(l.Pins, parity.Pin{Source: "v0.40.0` **approved** `", Admitted: parity.AdmitBase})
			f.repo.putLedger(adminLedger, l)
			_, _, _, summary := f.check(ctx)
			Expect(summary).To(ContainSubstring("- pin `` v0.40.0` **approved** ` ``, admitted `base`: recorded whole"))
		})
	})

	Describe("what check --base reads from the base", func() {
		It("proves a pin with the base's run.sh, not the pull request's", func(ctx SpecContext) {
			f.ceph.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert False\n")
			changed := f.ceph.commit("change a test")
			f.repo.put("hack/s3tests/run.sh", runShText("src/test/rgw/nothing-here", "file://"+f.ceph.dir))
			f.appendPin(squidLedger, changed, parity.AdmitAdditive)
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring("pin 2 (" + changed + ", additive): the proof fails: "))
		})

		It("refuses a ledger replaced by a symbolic link", func(ctx SpecContext) {
			data, err := os.ReadFile(filepath.Join(f.repo.dir, squidLedger))
			Expect(err).NotTo(HaveOccurred())
			f.repo.put("hack/elsewhere.json", data)
			Expect(os.Remove(filepath.Join(f.repo.dir, squidLedger))).To(Succeed())
			Expect(os.Symlink("../../../hack/elsewhere.json", filepath.Join(f.repo.dir, squidLedger))).To(Succeed())
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring(squidLedger + ": not a regular file"))
		})

		It("refuses a ledger outside the paths it checks", func(ctx SpecContext) {
			l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
			f.repo.putLedger("hack/s3tests/squid-v2.json", l)
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring("hack/s3tests/squid-v2.json: a ledger outside the paths the check covers"))
		})

		It("checks the ledger directories its own build names when given no path", func(ctx SpecContext) {
			f.appendEntry("t1", entry(f.pin, "failed"))
			file := filepath.Join(GinkgoT().TempDir(), "summary.md")
			status, stdout, stderr := run(ctx, "check", "--base", f.base, "--repo", f.repo.dir, "--summary", file)
			Expect(status).To(Equal(1), stderr)
			Expect(stdout).To(ContainSubstring(squidLedger + ": t1 entry 2: passed -> failed without a reason"))
		})

		It("refuses a ledger the pull request turns into the flat form", func(ctx SpecContext) {
			f.repo.put(squidLedger, []byte(`{"suite": "s3tests", "meta": {}, "outcomes": {"t1": "passed"}, "unstable": []}`))
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring(squidLedger + ": a baseline in the flat form, not a ledger"))
		})
	})

	Describe("the reason and append-only rules", func() {
		DescribeTable("refuse a reason nobody can read",
			func(ctx SpecContext, reason string) {
				f.appendEntry("t1", reasoned(entry(f.pin, "failed"), reason))
				status, stdout, _, _ := f.check(ctx)
				Expect(status).To(Equal(1))
				Expect(stdout).To(ContainSubstring("t1 entry 2: passed -> failed without a reason"))
			},
			Entry("a zero-width space", "\u200b"),
			Entry("a word joiner and a non-breaking space", "\u2060\u00a0"),
			Entry("punctuation alone", "-"),
		)

		It("refuses a reason that holds a control or format character", func(ctx SpecContext) {
			f.appendEntry("t1", reasoned(entry(f.pin, "failed"), "fixed\u202e upstream"))
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring(`t1 entry 2: the reason "fixed\\u202e upstream" holds a control or format character`))
		})

		It("refuses a ledger whose object repeats a member name", func(ctx SpecContext) {
			data, err := os.ReadFile(filepath.Join(f.repo.dir, squidLedger))
			Expect(err).NotTo(HaveOccurred())
			edited := strings.Replace(string(data), `    "t2": [`,
				`    "t1": [`+"\n"+`      {"outcome":"error","source":"`+f.pin+`","ceph_version":"19.2.6","recorded":"2026-10-12T16:55:39Z","runs":2,"unstable":false}`+"\n"+
					`    ],`+"\n"+`    "t2": [`, 1)
			f.repo.put(squidLedger, []byte(edited))
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(ContainSubstring(squidLedger + ": parsing the ledger: duplicate"))
		})
	})
})
