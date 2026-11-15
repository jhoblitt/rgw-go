package parity_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

var _ = Describe("a candidate's venv, held to the constraints", func() {
	var (
		base      *parity.Ledger
		head      string
		cons      parity.Constraints
		notes     []string
		withNotes parity.DiffOption
	)

	// meta returns a candidate's meta for a venv, as record --freeze writes it:
	// the digest of the whole freeze, of its package lines alone, and the
	// Python's minor version.
	meta := func(whole, packages, python string) map[string]string {
		m := candidate(nil).Meta
		m["pytest_freeze"] = parity.FreezeDigest([]byte(whole))
		if packages != "" {
			m["pytest_packages"] = parity.FreezeDigest([]byte(packages))
		}
		if python != "" {
			m["pytest_python"] = python
		}
		return m
	}

	// diff compares a candidate of one passing test with the ledger, holding
	// its venv to the constraints.
	diff := func(m map[string]string, allow bool) ([]string, error) {
		cand := candidate(map[string]string{"t1": "passed"})
		cand.Meta = m
		return parity.Diff(base, cand, allow, parity.KnownList{}, parity.Selection{}, withNotes)
	}

	BeforeEach(func() {
		var err error
		cons, err = parity.ParseConstraints([]byte(venvConstraints), "constraints.txt")
		Expect(err).NotTo(HaveOccurred())
		head = parity.FreezeDigest([]byte(venvConstraints))
		base = ledgerOf(map[string][]parity.Entry{"t1": {frozen(entry("aaa", "passed"), head)}})
		notes = nil
		withNotes = parity.WithConstraints(cons, func(note string) { notes = append(notes, note) })
	})

	It("is the ledger's, and says nothing, when the whole freeze is", func() {
		diffs, err := diff(meta(venvConstraints, venvPackages, "3.14"), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
		Expect(notes).To(BeEmpty())
	})

	It("is accepted, with a note naming both Pythons, when only the Python's patch release is another", func() {
		patch := "# Python 3.14.7\n" + venvPackages
		diffs, err := diff(meta(patch, venvPackages, "3.14"), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
		Expect(notes).To(Equal([]string{"pytest_freeze: baseline " + head + ", candidate " + parity.FreezeDigest([]byte(patch)) +
			", accepted: the packages are constraints.txt's and the Python has its minor version: " +
			"the venv was built by Python 3.14 (patch not recorded), constraints.txt names Python 3.14.4"}))
	})

	It("is compared as any other once accepted", func() {
		cand := candidate(map[string]string{"t1": "failed"})
		cand.Meta = meta("# Python 3.14.7\n"+venvPackages, venvPackages, "3.14")
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{}, withNotes)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{"t1: baseline passed, candidate failed"}))
	})

	It("is refused when a package line differs, naming the digests of the package lines", func() {
		changed := "boto3==1.44.3\nbotocore==1.44.4\nPyYAML==6.0.3\n"
		whole := "# Python 3.14.4\n" + changed
		diffs, err := diff(meta(whole, changed, "3.14"), false)
		Expect(diffs).To(BeEmpty())
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline " + head + ", candidate " + parity.FreezeDigest([]byte(whole)) +
			"; pytest_packages: constraints " + parity.FreezeDigest([]byte(venvPackages)) + ", candidate " + parity.FreezeDigest([]byte(changed)))))
		Expect(notes).To(BeEmpty())
	})

	It("is refused when a package line differs and the Python's patch release too", func() {
		changed := "boto3==1.44.3\nPyYAML==6.0.3\n"
		_, err := diff(meta("# Python 3.14.7\n"+changed, changed, "3.14"), false)
		Expect(err).To(MatchError(ContainSubstring("pytest_packages: constraints " + parity.FreezeDigest([]byte(venvPackages)) +
			", candidate " + parity.FreezeDigest([]byte(changed)))))
		Expect(notes).To(BeEmpty())
	})

	It("is refused when the Python's minor version is another, naming both Pythons", func() {
		minor := "# Python 3.15.0\n" + venvPackages
		diffs, err := diff(meta(minor, venvPackages, "3.15"), false)
		Expect(diffs).To(BeEmpty())
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline " + head + ", candidate " + parity.FreezeDigest([]byte(minor)) +
			"; the venv was built by Python 3.15 (patch not recorded), constraints.txt names Python 3.14.4")))
		Expect(notes).To(BeEmpty())
	})

	It("is refused when its record names neither the package lines nor the Python, as a record without them does", func() {
		patch := "# Python 3.14.7\n" + venvPackages
		_, err := diff(meta(patch, "", ""), false)
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline " + head + ", candidate " + parity.FreezeDigest([]byte(patch)) +
			"; pytest_packages: constraints " + parity.FreezeDigest([]byte(venvPackages)) + ", candidate missing" +
			"; pytest_python: constraints 3.14, candidate missing")))
		Expect(notes).To(BeEmpty())
	})

	It("is refused for a patch release when the constraints are not the venv the ledger's newest entries ran under", func() {
		base = ledgerOf(map[string][]parity.Entry{"t1": {frozen(entry("aaa", "passed"), olderVenv)}})
		patch := "# Python 3.14.7\n" + venvPackages
		_, err := diff(meta(patch, venvPackages, "3.14"), false)
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline " + olderVenv + ", candidate " + parity.FreezeDigest([]byte(patch)) +
			"; constraints.txt's pytest_freeze " + head + " is not the baseline's")))
		Expect(notes).To(BeEmpty())
	})

	It("is not asked for the package lines or the Python by a ledger whose newest entries name no venv", func() {
		base = ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
		_, err := diff(meta("# Python 3.15.0\n"+venvPackages, "", ""), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(notes).To(BeEmpty())
	})

	It("lets another venv through when told to, without a note", func() {
		_, err := diff(meta("# Python 3.15.0\n"+venvPackages, venvPackages, "3.15"), true)
		Expect(err).NotTo(HaveOccurred())
		_, err = diff(meta("# Python 3.14.7\n"+venvPackages, venvPackages, "3.14"), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(notes).To(BeEmpty())
	})

	It("makes no note when the comparison is refused for another reason", func() {
		m := meta("# Python 3.14.7\n"+venvPackages, venvPackages, "3.14")
		m["release"] = "tentacle"
		_, err := diff(m, false)
		Expect(err).To(MatchError(ContainSubstring("release: baseline squid, candidate tentacle")))
		Expect(notes).To(BeEmpty())
	})

	It("takes no note sink", func() {
		cand := candidate(map[string]string{"t1": "passed"})
		cand.Meta = meta("# Python 3.14.7\n"+venvPackages, venvPackages, "3.14")
		_, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{}, parity.WithConstraints(cons, nil))
		Expect(err).NotTo(HaveOccurred())
	})

	It("is judged on the whole freeze alone when no constraints are given", func() {
		cand := candidate(map[string]string{"t1": "passed"})
		cand.Meta = meta("# Python 3.14.7\n"+venvPackages, venvPackages, "3.14")
		_, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).To(MatchError(ContainSubstring("pytest_freeze: baseline " + head + ", candidate " + parity.FreezeDigest([]byte("# Python 3.14.7\n"+venvPackages)))))
		Expect(err).NotTo(MatchError(ContainSubstring("pytest_packages")))
	})
})

var _ = Describe("the diff command with the constraints", func() {
	const (
		patchVenv = "# Python 3.14.7\n" + venvPackages
		minorVenv = "# Python 3.15.0\n" + venvPackages
		moved     = "# Python 3.14.4\nboto3==1.44.3\nbotocore==1.44.4\nPyYAML==6.0.3\n"
	)
	var dir, baseline, constraints string

	// write puts text in a file of dir and returns its path.
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
		return path
	}

	// result records run2's tests into a result file, under the venv whose
	// freeze is text.
	result := func(ctx SpecContext, text string) string {
		out := filepath.Join(dir, "cand.json")
		args := []string{"record", "--format", "junit", "--suite", "s3tests", "--out", out, "--freeze", write("cand.freeze", text)}
		for _, kv := range candMeta("gateway=rgw-go") {
			args = append(args, "--meta", kv)
		}
		status, _, stderr := run(ctx, append(args, "testdata/run2.xml")...)
		Expect(status).To(Equal(0), stderr)
		return out
	}

	BeforeEach(func(ctx SpecContext) {
		dir = GinkgoT().TempDir()
		constraints = write("constraints.txt", venvConstraints)
		baseline = appendLedger(ctx, filepath.Join(dir, "baseline.json"),
			[]string{"--meta", "pytest_freeze=" + parity.FreezeDigest([]byte(venvConstraints))}, "testdata/run1.xml", "testdata/run2.xml")
	})

	It("exits 0 for the venv the ledger's newest entries ran under, with no note", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", result(ctx, venvConstraints), "--constraints", constraints)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(BeEmpty())
	})

	It("exits 0 with a note on stderr for another patch release of the constraints' Python", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", result(ctx, patchVenv), "--constraints", constraints)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(Equal("parity: note: pytest_freeze: baseline " + parity.FreezeDigest([]byte(venvConstraints)) +
			", candidate " + parity.FreezeDigest([]byte(patchVenv)) + ", accepted: the packages are " + constraints +
			"'s and the Python has its minor version: the venv was built by Python 3.14 (patch not recorded), " + constraints +
			" names Python 3.14.4\n"))
	})

	It("exits 2 for another patch release when no constraints are given", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", result(ctx, patchVenv))
		Expect(status).To(Equal(2))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring("pytest_freeze: baseline " + parity.FreezeDigest([]byte(venvConstraints)) +
			", candidate " + parity.FreezeDigest([]byte(patchVenv))))
		Expect(stderr).NotTo(ContainSubstring("pytest_packages"))
	})

	It("exits 2 for another minor version, naming both Pythons, and prints no difference", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", result(ctx, minorVenv), "--constraints", constraints)
		Expect(status).To(Equal(2))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring("the venv was built by Python 3.15 (patch not recorded), " + constraints + " names Python 3.14.4"))
	})

	It("exits 2 for another package line, naming the digests of the package lines", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", result(ctx, moved), "--constraints", constraints)
		Expect(status).To(Equal(2))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring("pytest_packages: constraints " + parity.FreezeDigest([]byte(venvPackages)) +
			", candidate " + parity.FreezeDigest([]byte("boto3==1.44.3\nbotocore==1.44.4\nPyYAML==6.0.3\n"))))
	})

	It("exits 2 for a run whose record names no package lines or Python", func(ctx SpecContext) {
		out := filepath.Join(dir, "old.json")
		args := []string{"record", "--format", "junit", "--suite", "s3tests", "--out", out, "--meta", "pytest_freeze=" + parity.FreezeDigest([]byte(patchVenv))}
		for _, kv := range candMeta("gateway=rgw-go") {
			args = append(args, "--meta", kv)
		}
		status, _, stderr := run(ctx, append(args, "testdata/run2.xml")...)
		Expect(status).To(Equal(0), stderr)
		status, _, stderr = run(ctx, "diff", "--baseline", baseline, "--candidate", out, "--constraints", constraints)
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("pytest_packages: constraints " + parity.FreezeDigest([]byte(venvPackages)) + ", candidate missing"))
	})

	It("exits 2 for constraints it cannot read as a freeze, or a file that is not there", func(ctx SpecContext) {
		cand := result(ctx, venvConstraints)
		status, _, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", cand, "--constraints", write("flat.txt", venvPackages))
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("flat.txt does not start with a line \"# Python X.Y.Z\""))

		status, _, stderr = run(ctx, "diff", "--baseline", baseline, "--candidate", cand, "--constraints", filepath.Join(dir, "none.txt"))
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("reading the constraints: open " + filepath.Join(dir, "none.txt")))
	})

	It("takes the constraints from the command line only, so that no variable widens a comparison that names none", func(ctx SpecContext) {
		GinkgoT().Setenv("PARITY_CONSTRAINTS", constraints)
		status, _, stderr := run(ctx, "diff", "--baseline", baseline, "--candidate", result(ctx, patchVenv))
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("pytest_freeze: baseline"))
	})
})

var _ = Describe("record --freeze", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	// record runs record into a result file, with the run's .freeze file and
	// the other flags, and returns the status, what it printed and the file.
	record := func(ctx SpecContext, args ...string) (status int, stderr, out string) {
		out = filepath.Join(dir, "cand.json")
		base := []string{"record", "--format", "junit", "--suite", "s3tests", "--out", out}
		for _, kv := range candMeta("gateway=rgw-go") {
			base = append(base, "--meta", kv)
		}
		status, _, stderr = run(ctx, append(append(base, args...), "testdata/run2.xml")...)
		return status, stderr, out
	}

	freezeFile := func(text string) string {
		path := filepath.Join(dir, "run.freeze")
		Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
		return path
	}

	It("records the digest of the whole freeze, of its package lines alone, and the Python's minor version", func(ctx SpecContext) {
		text := "# Python 3.14.7\n" + venvPackages
		status, stderr, out := record(ctx, "--freeze", freezeFile(text))
		Expect(status).To(Equal(0), stderr)
		meta := readResult(out).Meta
		Expect(meta).To(HaveKeyWithValue("pytest_freeze", parity.FreezeDigest([]byte(text))))
		Expect(meta).To(HaveKeyWithValue("pytest_packages", parity.FreezeDigest([]byte(venvPackages))))
		Expect(meta).To(HaveKeyWithValue("pytest_python", "3.14"))
	})

	It("records the same package digest for a freeze that differs only in the Python, and another for a changed package", func(ctx SpecContext) {
		_, _, out := record(ctx, "--freeze", freezeFile("# Python 3.14.4\n"+venvPackages))
		first := readResult(out).Meta
		_, _, out = record(ctx, "--freeze", freezeFile("# Python 3.14.9\n"+venvPackages))
		second := readResult(out).Meta
		Expect(second["pytest_packages"]).To(Equal(first["pytest_packages"]))
		Expect(second["pytest_freeze"]).NotTo(Equal(first["pytest_freeze"]))
		_, _, out = record(ctx, "--freeze", freezeFile("# Python 3.14.4\nboto3==1.44.3\nbotocore==1.44.4\nPyYAML==6.0.3\n"))
		Expect(readResult(out).Meta["pytest_packages"]).NotTo(Equal(first["pytest_packages"]))
	})

	It("reads the minor version of a Python as pip's venv names it", func(ctx SpecContext) {
		for python, minor := range map[string]string{"3.14.4": "3.14", "3.9.18": "3.9", "3.15.0rc1": "3.15", "3.13.0+": "3.13"} {
			status, stderr, out := record(ctx, "--freeze", freezeFile("# Python "+python+"\n"+venvPackages))
			Expect(status).To(Equal(0), "%s: %s", python, stderr)
			Expect(readResult(out).Meta).To(HaveKeyWithValue("pytest_python", minor), python)
		}
	})

	DescribeTable("exits 2 for a command line that cannot be what it says",
		func(ctx SpecContext, text string, extra []string, want string) {
			status, stderr, _ := record(ctx, append([]string{"--freeze", freezeFile(text)}, extra...)...)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring(want))
		},
		Entry("a freeze with no Python line", venvPackages, nil, "run.freeze does not start with a line \"# Python X.Y.Z\""),
		Entry("a Python with no minor version", "# Python 3\n"+venvPackages, nil, "run.freeze does not start with a line \"# Python X.Y.Z\""),
		Entry("a pytest_freeze of its own too", "# Python 3.14.4\n"+venvPackages, []string{"--meta", "pytest_freeze=abc"},
			"--meta pytest_freeze=abc: record sets pytest_freeze itself, from --freeze"),
		Entry("a pytest_packages of its own", "# Python 3.14.4\n"+venvPackages, []string{"--meta", "pytest_packages=abc"},
			"--meta pytest_packages=abc: record sets pytest_packages itself, from --freeze"),
	)

	It("exits 2 for a pytest_python of its own without --freeze, for a freeze that is not there", func(ctx SpecContext) {
		status, stderr, _ := record(ctx, "--meta", "pytest_python=3.14")
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("--meta pytest_python=3.14: record sets pytest_python itself, from --freeze"))

		status, stderr, _ = record(ctx, "--freeze", filepath.Join(dir, "none.freeze"))
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("reading the freeze: open " + filepath.Join(dir, "none.freeze")))
	})

	It("is refused for a ledger, whose entries carry no key of a candidate's", func(ctx SpecContext) {
		ledger := filepath.Join(dir, "baseline.json")
		args := ledgerArgs(ledger, []string{"--freeze", freezeFile("# Python 3.14.4\n" + venvPackages)}, "testdata/run1.xml", "testdata/run2.xml")
		status, _, stderr := run(ctx, args...)
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("--freeze is for --out"))
		Expect(ledger).NotTo(BeAnExistingFile())
	})

	It("leaves a ledger's entries without any of the keys when the record names the venv by --meta, as the record path does", func(ctx SpecContext) {
		ledger := appendLedger(ctx, filepath.Join(dir, "baseline.json"),
			[]string{"--meta", "pytest_freeze=" + parity.FreezeDigest([]byte(venvConstraints))}, "testdata/run1.xml", "testdata/run2.xml")
		for id, entries := range readLedger(ledger).Tests {
			for _, e := range entries {
				Expect(e.Meta).To(And(HaveKey("pytest_freeze"), Not(HaveKey("pytest_packages")), Not(HaveKey("pytest_python"))), id)
			}
		}
	})
})
