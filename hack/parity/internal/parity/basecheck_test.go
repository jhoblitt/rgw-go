package parity_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// put writes a file at path under the repository's root.
func (r *gitRepo) put(path string, data []byte) {
	full := filepath.Join(r.dir, path)
	Expect(os.MkdirAll(filepath.Dir(full), 0o750)).To(Succeed())
	Expect(os.WriteFile(full, data, 0o600)).To(Succeed())
}

// putLedger writes l at path under the repository's root.
func (r *gitRepo) putLedger(path string, l *parity.Ledger) {
	data, err := parity.MarshalLedger(l)
	Expect(err).NotTo(HaveOccurred())
	r.put(path, data)
}

// readLedgerAt reads the ledger at path under the repository's root.
func (r *gitRepo) readLedgerAt(path string) *parity.Ledger {
	return readLedger(filepath.Join(r.dir, path))
}

// pinLine is the one line by which run.sh may set S3TESTS_COMMIT.
const pinLine = "readonly S3TESTS_COMMIT=${S3TESTS_COMMIT:-${pin}}"

// runShText returns a run.sh that takes the pin from the PIN file, runs the
// suite in dir and fetches it from url.
func runShText(dir, url string) []byte {
	return []byte("#!/usr/bin/env bash\n" +
		"S3TESTS_REPO=${S3TESTS_REPO:-" + url + "}\n" +
		"IFS= read -r pin <\"$(dirname \"$0\")/PIN\"\n" +
		pinLine + "\n" +
		"readonly S3TESTS_DIR=" + dir + "\n" +
		"readonly TEST_FILES=(s3tests/functional/test_s3.py s3tests/functional/test_headers.py)\n")
}

// pinText returns a PIN file's text for commit.
func pinText(commit string) []byte {
	return []byte(commit + "\n")
}

const (
	squidLedger = "test/s3tests/baseline/squid.json"
	adminLedger = "test/admin/baseline/squid.json"
)

// baseFixture is a Ceph clone at an s3-tests pin and a repository whose
// base commit holds run.sh and a ledger of each suite at it.
type baseFixture struct {
	ceph, repo *gitRepo
	pin, base  string
}

func newBaseFixture() *baseFixture {
	f := &baseFixture{ceph: newGitRepo()}
	f.pin = f.ceph.seed()
	f.ceph.serveTrusted()

	f.repo = newGitRepo()
	f.repo.put("hack/s3tests/run.sh", runShText(suiteDir, "file://"+f.ceph.dir))
	f.repo.put("hack/s3tests/PIN", pinText(f.pin))
	s3 := ledgerOf(map[string][]parity.Entry{"t1": {entry(f.pin, "passed")}, "t2": {entry(f.pin, "failed")}})
	s3.Pins[0].Source = f.pin
	f.repo.putLedger(squidLedger, s3)
	admin := ledgerOf(map[string][]parity.Entry{"TestA": {entry("v0.39.0", "passed")}})
	admin.Suite, admin.SourceKey, admin.Pins[0].Source = "admin", "go_ceph_tag", "v0.39.0"
	f.repo.putLedger(adminLedger, admin)
	f.base = f.repo.commit("base")
	return f
}

// check runs check --base over args, by default the ledger directories,
// and returns its status, what it printed and the summary it wrote.
func (f *baseFixture) check(ctx context.Context, args ...string) (status int, stdout, stderr, summary string) {
	file := filepath.Join(GinkgoT().TempDir(), "summary.md")
	if len(args) == 0 {
		args = []string{"test/s3tests/baseline", "test/admin/baseline"}
	}
	status, stdout, stderr = run(ctx, append([]string{"check", "--base", f.base, "--repo", f.repo.dir, "--summary", file}, args...)...)
	data, err := os.ReadFile(file)
	if err == nil {
		summary = string(data)
	}
	return status, stdout, stderr, summary
}

// appendEntry appends e to test id of the working tree's squid ledger.
func (f *baseFixture) appendEntry(id string, e parity.Entry) {
	l := f.repo.readLedgerAt(squidLedger)
	l.Tests[id] = append(l.Tests[id], e)
	f.repo.putLedger(squidLedger, l)
}

// appendPin appends a pin to the working tree's ledger at path.
func (f *baseFixture) appendPin(path, source string, admitted parity.Admission) {
	l := f.repo.readLedgerAt(path)
	Expect(parity.AddPin(l, source, admitted)).To(Succeed())
	f.repo.putLedger(path, l)
}

var _ = Describe("check --base", func() {
	var f *baseFixture

	BeforeEach(func() {
		f = newBaseFixture()
	})

	It("passes ledgers the working tree leaves alone, writing no summary", func(ctx SpecContext) {
		status, stdout, stderr, summary := f.check(ctx)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(BeEmpty())
		Expect(summary).To(BeEmpty())
	})

	It("passes an appended entry and lists it, with its reason, in the summary", func(ctx SpecContext) {
		f.appendEntry("t1", reasoned(entry(f.pin, "failed"), "radosgw 19.2.7 answers 400"))
		status, stdout, stderr, summary := f.check(ctx)
		Expect(status).To(Equal(0), stderr+stdout)
		Expect(summary).To(ContainSubstring("### `" + squidLedger + "`\n"))
		Expect(summary).To(ContainSubstring("1 appended entries change an outcome or an unstable flag, or retire a test:\n\n```\n" +
			"t1: passed -> failed: radosgw 19.2.7 answers 400\n```"))
	})

	It("refuses an appended entry whose reason is a draft's", func(ctx SpecContext) {
		f.appendEntry("t1", reasoned(entry(f.pin, "failed"), "unreviewed: recorded by CI"))
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(Equal(squidLedger + `: t1 entry 2: the draft's reason "unreviewed: recorded by CI"; ` +
			"replace it with the change's own before committing\n"))
	})

	It("refuses an edited entry", func(ctx SpecContext) {
		l := f.repo.readLedgerAt(squidLedger)
		l.Tests["t2"][0].Outcome = "passed"
		f.repo.putLedger(squidLedger, l)
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(Equal(squidLedger + ": t2 entry 1: changed or moved; an entry is never edited\n"))
	})

	It("refuses a removed ledger", func(ctx SpecContext) {
		Expect(os.Remove(filepath.Join(f.repo.dir, adminLedger))).To(Succeed())
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(Equal(adminLedger + ": removed; a ledger is never removed\n"))
	})

	It("validates a new ledger, a draft refused", func(ctx SpecContext) {
		l := ledgerOf(map[string][]parity.Entry{"t1": {entry(f.pin, "passed"), reasoned(entry(f.pin, "failed"), "unreviewed: x")}})
		l.Release, l.Pins[0].Source = "tentacle", f.pin
		f.repo.putLedger("test/s3tests/baseline/tentacle.json", l)
		status, stdout, _, summary := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring("test/s3tests/baseline/tentacle.json: t1 entry 2: the draft's reason"))
		Expect(summary).To(ContainSubstring("t1: passed -> failed: unreviewed: x"))
	})

	It("checks a ledger the base holds in the flat form against its conversion", func(ctx SpecContext) {
		flat := parity.Result{
			Suite: "s3tests",
			Meta: map[string]string{
				"s3tests_commit": f.pin, "release": "squid", "ceph_version": "19.2.6", "recorded": "2026-10-12T16:55:39Z", "runs": "2",
			},
			Outcomes: map[string]string{"t1": "passed", "t2": "failed"},
			Unstable: []string{},
		}
		data, err := json.MarshalIndent(flat, "", "  ")
		Expect(err).NotTo(HaveOccurred())
		f.repo.put(squidLedger, data)
		f.base = f.repo.commit("flat")
		l, err := parity.Migrate(flat)
		Expect(err).NotTo(HaveOccurred())
		f.repo.putLedger(squidLedger, l)

		status, stdout, stderr, summary := f.check(ctx)
		Expect(status).To(Equal(0), stderr+stdout)
		Expect(summary).To(ContainSubstring("The base holds this ledger in the flat form"))

		l.Tests["t1"][0].Outcome = "error"
		f.repo.putLedger(squidLedger, l)
		status, stdout, _, _ = f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring("t1 entry 1: changed or moved"))
	})

	Describe("an appended pin", func() {
		var added string

		BeforeEach(func() {
			f.ceph.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert True\n")
			added = f.ceph.commit("add a test")
		})

		It("is proven again from a treeless fetch of run.sh's S3TESTS_REPO", func(ctx SpecContext) {
			f.appendPin(squidLedger, added, parity.AdmitAdditive)
			status, stdout, stderr, summary := f.check(ctx)
			Expect(status).To(Equal(0), stderr+stdout)
			Expect(summary).To(ContainSubstring("- pin `" + added + "`, admitted `additive`: proven over `" + f.pin + "`\n"))
		})

		It("is refused when the proof admits it another way", func(ctx SpecContext) {
			f.appendPin(squidLedger, added, parity.AdmitReworded)
			status, stdout, _, summary := f.check(ctx, "--ceph-repo", "file://"+f.ceph.dir, "test/s3tests/baseline")
			Expect(status).To(Equal(1))
			Expect(stdout).To(Equal(squidLedger + ": pin 2 (" + added + ", reworded): the proof admits it additive\n"))
			Expect(summary).To(ContainSubstring("admitted `reworded`: **not proven**"))
		})

		It("is refused when the proof fails, naming the line, against a local clone", func(ctx SpecContext) {
			f.ceph.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert False\n")
			changed := f.ceph.commit("change a test")
			f.appendPin(squidLedger, added, parity.AdmitAdditive)
			f.appendPin(squidLedger, changed, parity.AdmitAdditive)
			status, stdout, _, _ := f.check(ctx, "--clone", f.ceph.dir, "test/s3tests/baseline")
			Expect(status).To(Equal(1))
			Expect(stdout).To(HavePrefix(squidLedger + ": pin 3 (" + changed + ", additive): the proof fails: "))
			Expect(stdout).To(ContainSubstring("test_headers.py: line 2 of " + added + " is changed or removed:     assert True"))
			Expect(stdout).NotTo(ContainSubstring("pin 2 ("), "pin 2 is proven")
		})

		It("is refused in a ledger whose source is not an s3-tests commit", func(ctx SpecContext) {
			f.appendPin(adminLedger, "v0.40.0", parity.AdmitAdditive)
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(stdout).To(Equal(adminLedger + ": pin 2 (v0.40.0, additive): only an s3-tests pin can be proven additive\n"))
		})
	})

	DescribeTable("exits 2 on a bad command line",
		func(ctx SpecContext, want string, args ...string) {
			status, _, stderr := run(ctx, append([]string{"check"}, args...)...)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring(want))
		},
		Entry("--base with --old", "--base excludes", "--base", "HEAD", "--old", "x.json", "test"),
		Entry("--base with --draft", "--base excludes", "--base", "HEAD", "--draft", "test"),
		Entry("paths without --base", "only --base takes paths", "--old", "x.json", "--new", "y.json", "test"),
	)
})

// basePinFixture appends to the working tree's squid ledger a base pin at
// a new commit of rgw-go-s3tests, with the pin's members after "admitted"
// given by extra, and an entry at it for each test: t1 carried forward as
// it was, t2 flipped to passed for a reason.
func basePinFixture(f *baseFixture, extra string) string {
	f.ceph.put("CHANGES", []byte("a commit to pin\n"))
	return basePinAt(f, f.ceph.commit("a commit to pin"), extra)
}

// basePinAt appends to the working tree's squid ledger a base pin at src,
// as basePinFixture does.
func basePinAt(f *baseFixture, src, extra string) string {
	l := f.repo.readLedgerAt(squidLedger)
	l.Pins = append(l.Pins, parity.Pin{Source: src, Admitted: parity.AdmitBase})
	l.Tests["t1"] = append(l.Tests["t1"], entry(src, "passed"))
	l.Tests["t2"] = append(l.Tests["t2"], reasoned(entry(src, "passed"), "fixed upstream"))
	data, err := parity.MarshalLedger(l)
	Expect(err).NotTo(HaveOccurred())
	pin := `{"source":"` + src + `","admitted":"base"`
	f.repo.put(squidLedger, []byte(strings.Replace(string(data), pin+"}", pin+extra+"}", 1)))
	return src
}

var _ = Describe("a base pin a pull request appends", func() {
	var f *baseFixture

	BeforeEach(func() {
		f = newBaseFixture()
	})

	It("is refused without a reason", func(ctx SpecContext) {
		src := basePinFixture(f, "")
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring(squidLedger + ": pin 2 (" + src + ", base) needs a reason: it re-stamps outcomes at an unproven source"))
	})

	It("is refused with a draft's reason", func(ctx SpecContext) {
		basePinFixture(f, `,"reason":"unreviewed: recorded by CI"`)
		status, stdout, _, _ := f.check(ctx)
		Expect(status).To(Equal(1))
		Expect(stdout).To(ContainSubstring(`pin 2's reason "unreviewed: recorded by CI" is a draft's`))
	})

	It("passes with a reason, and the summary names every outcome it re-stamps and those carried forward", func(ctx SpecContext) {
		src := basePinFixture(f, `,"reason":"the s3-tests pin moved past a rewritten commit"`)
		status, stdout, stderr, summary := f.check(ctx)
		Expect(status).To(Equal(0), stdout+stderr)
		Expect(summary).To(ContainSubstring("- pin `" + src + "`, admitted `base`, for the reason " +
			"`the s3-tests pin moved past a rewritten commit`: recorded whole, unproven\n"))
		Expect(summary).To(ContainSubstring("2 outcomes re-stamped at `" + src + "`, **1 carried forward unchanged**:"))
		Expect(summary).To(ContainSubstring("t1: passed, unchanged\nt2: failed -> passed\n"))
	})
})
