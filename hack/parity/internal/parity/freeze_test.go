package parity_test

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

const (
	repoConstraints = "../../../s3tests/constraints.txt"
	// olderVenv and currentVenv are the digests of two venvs the ledgers
	// were recorded under, the older with botocore 1.44.0 and the current
	// with botocore 1.44.3.
	olderVenv   = "f1f5b2da6f99"
	currentVenv = "20202734bd43"
)

// frozen returns e recorded under the venv whose digest is freeze.
func frozen(e parity.Entry, freeze string) parity.Entry {
	e.Meta = map[string]string{"pytest_freeze": freeze}
	return e
}

// recordedAt returns e recorded at when.
func recordedAt(e parity.Entry, when string) parity.Entry {
	e.Recorded = when
	return e
}

// withFreeze returns the result with the venv digest freeze in its meta.
func withFreeze(res parity.Result, freeze string) parity.Result {
	res.Meta["pytest_freeze"] = freeze
	return res
}

var _ = Describe("the venv a candidate ran under", func() {
	var base *parity.Ledger

	// t1 and t2 were last recorded under the older venv on the 11th, and
	// t3, a test added since, under the current one on the 14th: the
	// ledger's newest entries decide, though most of its entries do not.
	BeforeEach(func() {
		base = ledgerOf(map[string][]parity.Entry{
			"t1": {frozen(recordedAt(entry("aaa", "passed"), "2026-11-11T00:47:31Z"), olderVenv)},
			"t2": {frozen(recordedAt(entry("aaa", "failed"), "2026-11-11T00:47:31Z"), olderVenv)},
			"t3": {frozen(recordedAt(entry("aaa", "passed"), "2026-11-14T13:06:14Z"), currentVenv)},
		})
	})

	allPass := func() map[string]string { return map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"} }

	It("is compared with the ledger's newest entries', and the run is judged when the two agree", func() {
		diffs, err := parity.Diff(base, withFreeze(candidate(allPass()), currentVenv), false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})

	It("refuses a run in another venv, naming both, before it compares any outcome", func() {
		wrong := map[string]string{"t1": "failed", "t2": "passed", "t3": "failed"}
		diffs, err := parity.Diff(base, withFreeze(candidate(wrong), olderVenv), false, parity.KnownList{}, parity.Selection{})
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline " + currentVenv + ", candidate " + olderVenv)))
		Expect(diffs).To(BeEmpty())
	})

	It("names the venv with the source and release when they differ too", func() {
		cand := withFreeze(candidate(allPass()), olderVenv)
		cand.Meta["release"] = "tentacle"
		_, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).To(MatchError(ContainSubstring("release: baseline squid, candidate tentacle")))
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline " + currentVenv + ", candidate " + olderVenv)))
	})

	It("compares a run in another venv when told to, which a local try of a newer client needs", func() {
		wrong := map[string]string{"t1": "failed", "t2": "failed", "t3": "passed"}
		diffs, err := parity.Diff(base, withFreeze(candidate(wrong), olderVenv), true, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{"t1: baseline passed, candidate failed"}))
	})

	It("refuses a run that names no venv, drift allowed or not", func() {
		for _, drift := range []bool{false, true} {
			_, err := parity.Diff(base, candidate(allPass()), drift, parity.KnownList{}, parity.Selection{})
			Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline "+currentVenv+", candidate missing")), "drift %v", drift)
		}
	})

	It("asks nothing of a run when the ledger's newest entries name no venv", func() {
		bare := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
		diffs, err := parity.Diff(bare, withFreeze(candidate(map[string]string{"t1": "passed"}), olderVenv), false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})

	It("takes the newest entries of a retired test too", func() {
		base.Tests["t1"] = append(base.Tests["t1"],
			reasoned(frozen(recordedAt(entry("aaa", "retired"), "2026-11-20T09:00:00Z"), olderVenv), "deselected"))
		_, err := parity.Diff(base, withFreeze(candidate(allPass()), currentVenv), false, parity.KnownList{}, parity.Selection{})
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline " + olderVenv + ", candidate " + currentVenv)))
	})

	It("refuses a ledger whose newest entries were recorded in one second under two venvs", func() {
		base.Tests["t4"] = []parity.Entry{frozen(recordedAt(entry("aaa", "passed"), "2026-11-14T13:06:14Z"), olderVenv)}
		_, err := parity.Diff(base, withFreeze(candidate(allPass()), currentVenv), false, parity.KnownList{}, parity.Selection{})
		Expect(err).To(MatchError(ContainSubstring("the ledger's newest entries, recorded 2026-11-14T13:06:14Z, carry different pytest_freeze values")))
	})

	Describe("recorded by the parity command", func() {
		var dir, ledger string

		// result records run2's tests into a result file under the venv freeze.
		result := func(ctx SpecContext, freeze string) string {
			out := filepath.Join(dir, "cand-"+freeze+".json")
			args := []string{"record", "--format", "junit", "--suite", "s3tests", "--out", out}
			for _, kv := range candMeta("gateway=rgw-go") {
				args = append(args, "--meta", kv)
			}
			if freeze != "" {
				args = append(args, "--meta", "pytest_freeze="+freeze)
			}
			status, _, stderr := run(ctx, append(args, "testdata/run2.xml")...)
			Expect(status).To(Equal(0), stderr)
			return out
		}

		BeforeEach(func(ctx SpecContext) {
			dir = GinkgoT().TempDir()
			ledger = appendLedger(ctx, filepath.Join(dir, "baseline.json"), []string{"--meta", "pytest_freeze=" + currentVenv},
				"testdata/run1.xml", "testdata/run2.xml")
		})

		It("exits 2 for a run in another venv, naming both on stderr and printing no difference", func(ctx SpecContext) {
			status, stdout, stderr := run(ctx, "diff", "--baseline", ledger, "--candidate", result(ctx, olderVenv))
			Expect(status).To(Equal(2))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("pytest_freeze: baseline " + currentVenv + ", candidate " + olderVenv))
		})

		It("exits 0 for the run in the ledger's venv, and under --allow-meta-drift for another", func(ctx SpecContext) {
			status, _, stderr := run(ctx, "diff", "--baseline", ledger, "--candidate", result(ctx, currentVenv))
			Expect(status).To(Equal(0), stderr)
			status, _, stderr = run(ctx, "diff", "--allow-meta-drift", "--baseline", ledger, "--candidate", result(ctx, olderVenv))
			Expect(status).To(Equal(0), stderr)
		})

		It("exits 2 for a run that names no venv", func(ctx SpecContext) {
			status, _, stderr := run(ctx, "diff", "--allow-meta-drift", "--baseline", ledger, "--candidate", result(ctx, ""))
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring("pytest_freeze: baseline " + currentVenv + ", candidate missing"))
		})
	})
})

var _ = Describe("a record that moves the venv", func() {
	var first *parity.Ledger

	// under returns a record of squid's 19.2.6 at aaa under the venv freeze.
	under := func(freeze string, outcomes map[string]string) parity.Result {
		return withFreeze(recorded("aaa", outcomes), freeze)
	}
	same := func() map[string]string { return map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"} }

	BeforeEach(func() {
		first, _ = mustAppend(nil, under(olderVenv, same()), parity.RecordOptions{})
	})

	It("keeps the venv in each entry's meta", func() {
		Expect(first.Tests["t1"][0].Meta).To(HaveKeyWithValue("pytest_freeze", olderVenv))
	})

	It("appends nothing for a record under the venv the newest entries ran under", func() {
		res := under(olderVenv, same())
		res.Meta["recorded"] = "2026-10-13T00:00:00Z"
		out, appended := mustAppend(first, res, parity.RecordOptions{})
		Expect(appended).To(BeEmpty())
		Expect(out).To(Equal(first))
	})

	It("is refused as a subset, which would leave tests without an entry under the new venv", func() {
		_, _, err := parity.AppendRecord(first, under(currentVenv, map[string]string{"t1": "passed"}),
			parity.RecordOptions{Subset: true, PinReason: "botocore moved"})
		Expect(err).To(MatchError(ContainSubstring("a subset record cannot move the venv")))
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze " + currentVenv + ", where the ledger's newest entries ran under " + olderVenv)))
	})

	It("is refused without a reason, as a base pin is", func() {
		_, _, err := parity.AppendRecord(first, under(currentVenv, same()), parity.RecordOptions{})
		Expect(err).To(MatchError(ContainSubstring("needs --pin-reason")))
		Expect(err).To(MatchError(ContainSubstring(currentVenv + ", where the ledger's newest entries ran under " + olderVenv)))
	})

	It("is refused when the record names no venv", func() {
		_, _, err := parity.AppendRecord(first, recorded("aaa", same()), parity.RecordOptions{})
		Expect(err).To(MatchError(ContainSubstring("the record carries no pytest_freeze, which the ledger's newest entries do: " + olderVenv)))
	})

	It("is refused with a reason a reader cannot read or a draft's", func() {
		for _, reason := range []string{"--", "unreviewed: botocore moved"} {
			_, _, err := parity.AppendRecord(first, under(currentVenv, same()), parity.RecordOptions{PinReason: reason})
			Expect(err).To(HaveOccurred(), reason)
		}
	})

	It("appends an entry for every test, with the reason, where the outcomes hold", func() {
		res := under(currentVenv, same())
		res.Meta["recorded"] = "2026-11-14T13:06:14Z"
		out, appended := mustAppend(first, res, parity.RecordOptions{PinReason: "CI builds botocore 1.44.3, the constraints now name it"})
		Expect(ids(appended)).To(Equal([]string{"t1", "t2", "t3"}))
		for _, id := range []string{"t1", "t2", "t3"} {
			Expect(out.Tests[id]).To(HaveLen(2), id)
			last := out.Tests[id][1]
			Expect(last.Reason).To(Equal("CI builds botocore 1.44.3, the constraints now name it"), id)
			Expect(last.Meta).To(HaveKeyWithValue("pytest_freeze", currentVenv), id)
			Expect(last.Source).To(Equal("aaa"), id)
		}
		Expect(out.Pins).To(Equal(first.Pins), "the chain is the same: no source moved")
		Expect(parity.Validate(out)).To(BeEmpty())
		freeze, err := parity.HeadFreeze(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(freeze).To(Equal(currentVenv), "the next comparison is held to the new venv")
	})

	It("still needs its own reason for a test whose outcome changes, and the move's reason does not stand for it", func() {
		flipped := same()
		flipped["t1"] = "failed"
		_, _, err := parity.AppendRecord(first, under(currentVenv, flipped), parity.RecordOptions{PinReason: "botocore moved"})
		Expect(err).To(MatchError(ContainSubstring("t1: passed -> failed needs a reason")))

		out, _ := mustAppend(first, under(currentVenv, flipped), parity.RecordOptions{
			PinReason: "botocore moved", Reasons: map[string]string{"t1": "botocore 1.44.3 sends the checksum radosgw refuses"},
		})
		Expect(out.Tests["t1"][1].Reason).To(Equal("botocore 1.44.3 sends the checksum radosgw refuses"))
		Expect(out.Tests["t2"][1].Reason).To(Equal("botocore moved"))
	})

	It("gives a draft's reason to the entries it re-stamps, which validate refuses until each is explained", func() {
		out, _ := mustAppend(first, under(currentVenv, same()), parity.RecordOptions{Draft: "recorded by CI"})
		Expect(out.Tests["t2"][1].Reason).To(Equal(parity.DraftMarker + " recorded by CI"))
		Expect(parity.ValidateDraft(out)).To(BeEmpty())
		Expect(parity.Validate(out)).To(ContainElement(ContainSubstring("t2 entry 2: the draft's reason")))
	})

	It("gives a new test no reason, as it has no entry to re-stamp", func() {
		more := same()
		more["t4"] = "passed"
		out, appended := mustAppend(first, under(currentVenv, more), parity.RecordOptions{PinReason: "botocore moved"})
		Expect(ids(appended)).To(Equal([]string{"t1", "t2", "t3", "t4"}))
		Expect(out.Tests["t4"]).To(HaveLen(1))
		Expect(out.Tests["t4"][0].Reason).To(BeEmpty())
	})

	It("retires a test it did not run, as any full record does", func() {
		out, _ := mustAppend(first, under(currentVenv, map[string]string{"t1": "passed", "t2": "failed"}),
			parity.RecordOptions{PinReason: "botocore moved", Reasons: map[string]string{"t3": "deselected"}})
		Expect(out.Tests["t3"][1].Outcome).To(Equal("retired"))
	})

	It("is not asked of a ledger whose newest entries name no venv", func() {
		bare, _ := mustAppend(nil, recorded("aaa", same()), parity.RecordOptions{})
		out, appended := mustAppend(bare, under(currentVenv, same()), parity.RecordOptions{})
		Expect(appended).To(BeEmpty())
		Expect(out).To(Equal(bare))
	})

	It("takes a pin reason only when it moves the venv or appends a pin", func() {
		_, _, err := parity.AppendRecord(first, under(olderVenv, same()), parity.RecordOptions{PinReason: "moved"})
		Expect(err).To(MatchError(ContainSubstring("the record ran at the chain's head aaa and appends no pin or venv move")))
	})

	It("lets a base pin's reason stand for a venv that moves with it", func() {
		res := withFreeze(recorded("bbb", same()), currentVenv)
		out, _ := mustAppend(first, res, parity.RecordOptions{PinReason: "the pin moved, and the constraints with it"})
		Expect(out.Pins[1].Reason).To(Equal("the pin moved, and the constraints with it"))
		Expect(out.Tests["t1"][1].Reason).To(BeEmpty(), "the pin carries the reason, not each entry")
	})

	Describe("run as the record command", func() {
		var dir string

		BeforeEach(func() {
			dir = GinkgoT().TempDir()
		})

		recordWith := func(ctx SpecContext, ledger, freeze string, flags ...string) (int, string, string) {
			return run(ctx, ledgerArgs(ledger, append([]string{"--meta", "pytest_freeze=" + freeze}, flags...),
				"testdata/run1.xml", "testdata/run2.xml")...)
		}

		It("stops without --pin-reason and writes nothing, then appends with it and prints the move", func(ctx SpecContext) {
			ledger := filepath.Join(dir, "squid.json")
			status, _, stderr := recordWith(ctx, ledger, olderVenv)
			Expect(status).To(Equal(0), stderr)
			before, err := os.ReadFile(ledger)
			Expect(err).NotTo(HaveOccurred())

			status, _, stderr = recordWith(ctx, ledger, currentVenv)
			Expect(status).To(Equal(1))
			Expect(stderr).To(ContainSubstring("needs --pin-reason"))
			after, err := os.ReadFile(ledger)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))

			status, stdout, stderr := recordWith(ctx, ledger, currentVenv, "--pin-reason", "the constraints name botocore 1.44.3")
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(ContainSubstring("moved the venv, pytest_freeze " + olderVenv + " to " + currentVenv +
				": the constraints name botocore 1.44.3\n"))
			Expect(stdout).To(ContainSubstring("appended 6 entries, 0 needing a reason"))
			l := readLedger(ledger)
			Expect(l.Pins).To(HaveLen(1))
			Expect(l.Tests["s3tests.functional.test_s3::test_a"]).To(HaveLen(2))
			status, _, stderr = run(ctx, "validate", ledger)
			Expect(status).To(Equal(0), stderr)
		})
	})
})

var _ = Describe("hack/s3tests/constraints.txt", func() {
	var lines, names []string

	BeforeEach(func() {
		info, err := os.Lstat(repoConstraints)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Type()).To(BeZero(), "%s is a regular file, as run.sh requires, not a link or another type of file", repoConstraints)
		data, err := os.ReadFile(repoConstraints)
		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(HaveSuffix("\n"))
		sc := bufio.NewScanner(bytes.NewReader(data))
		lines, names = nil, nil
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		Expect(sc.Err()).NotTo(HaveOccurred())
		for _, l := range lines[min(1, len(lines)):] {
			name, _, _ := strings.Cut(l, "==")
			names = append(names, strings.ToLower(name))
		}
	})

	It("is the freeze a venv leaves: the Python, then each package at its version, sorted as pip sorts them", func() {
		Expect(lines).NotTo(BeEmpty())
		Expect(lines[0]).To(MatchRegexp(`^# Python 3\.\d+\.\d+$`))
		pkg := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*==[A-Za-z0-9][A-Za-z0-9.!+_-]*$`)
		for _, l := range lines[1:] {
			Expect(l).To(MatchRegexp(pkg.String()), "every other line is name==version, so that pip reads it as a constraint and run.sh compares it with pip freeze")
		}
		Expect(names).To(Equal(slices.Sorted(slices.Values(names))), "pip freeze lists packages sorted by lower-cased name")
		Expect(slices.Compact(slices.Clone(names))).To(Equal(names), "no package twice")
	})

	It("pins every package the s3-tests requirements need, botocore among them", func() {
		Expect(names).To(ContainElements("boto3", "botocore", "pytest", "pytest-timeout", "pyyaml", "munch", "gevent", "isodate",
			"requests", "pytz", "httplib2", "lxml", "tox"))
	})

	It("is the freeze both ledgers' newest entries were recorded under, so that a fresh venv's freeze equals the ledgers'", func() {
		data, err := os.ReadFile(repoConstraints)
		Expect(err).NotTo(HaveOccurred())
		digest := parity.FreezeDigest(data)
		for _, release := range []string{"squid", "tentacle"} {
			freeze, err := parity.HeadFreeze(readLedger("../../../../test/s3tests/baseline/" + release + ".json"))
			Expect(err).NotTo(HaveOccurred(), release)
			Expect(freeze).To(Equal(digest), "%s: make s3tests-record under the changed constraints, with PIN_REASON, before they land", release)
		}
	})
})
