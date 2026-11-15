package parity_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

var _ = Describe("venv for a compare run", func() {
	const (
		anotherPatch = "# Python 3.14.7\n" + venvPackages
		anotherMinor = "# Python 3.15.0\n" + venvPackages
		theVenv      = "# Python 3.14.4\n" + venvPackages
	)
	var dir, baseline, constraints string

	// write puts text in a file of dir and returns its path.
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
		return path
	}

	// compare runs venv in compare mode against the baseline, for a venv
	// whose freeze file holds text.
	compare := func(ctx SpecContext, text string, extra ...string) (status int, stdout, stderr string) {
		args := []string{"venv", "--mode", "compare", "--baseline", baseline, "--constraints", constraints, "--freeze", write("run.freeze", text)}
		return run(ctx, append(args, extra...)...)
	}

	BeforeEach(func(ctx SpecContext) {
		dir = GinkgoT().TempDir()
		constraints = write("constraints.txt", venvConstraints)
		// The ledger's newest entries ran under the venv the constraints name.
		baseline = appendLedger(ctx, filepath.Join(dir, "baseline.json"),
			[]string{"--meta", "pytest_freeze=" + parity.FreezeDigest([]byte(venvConstraints))}, "testdata/run1.xml", "testdata/run2.xml")
	})

	It("succeeds, silently, for the venv the ledger's newest entries ran under", func(ctx SpecContext) {
		status, stdout, stderr := compare(ctx, theVenv)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(BeEmpty())
	})

	It("goes on with a note, naming both Pythons, when only the Python's patch release is another", func(ctx SpecContext) {
		status, stdout, stderr := compare(ctx, anotherPatch)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(Equal("parity: note: pytest_freeze: baseline " + parity.FreezeDigest([]byte(venvConstraints)) +
			", candidate " + parity.FreezeDigest([]byte(anotherPatch)) + ", accepted: the packages are " + constraints +
			"'s and the Python has its minor version: the venv was built by Python 3.14.7, " + constraints + " names Python 3.14.4\n"))
	})

	It("refuses another Python minor version at the start, as diff would at the end, naming both digests and both Pythons", func(ctx SpecContext) {
		status, stdout, stderr := compare(ctx, anotherMinor)
		Expect(status).To(Equal(1))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(Equal("diff would refuse this run against " + baseline + " before it compared any outcome: " +
			"pytest_freeze: baseline " + parity.FreezeDigest([]byte(venvConstraints)) + ", candidate " + parity.FreezeDigest([]byte(anotherMinor)) +
			"; the venv was built by Python 3.15.0, " + constraints + " names Python 3.14.4"))
	})

	It("refuses with the line diff prints for the same venv", func(ctx SpecContext) {
		_, _, stderr := compare(ctx, anotherMinor)
		status, _, recErr := run(ctx, "record", "--format", "junit", "--suite", "s3tests", "--out", filepath.Join(dir, "cand.json"),
			"--meta", "release=squid", "--meta", "s3tests_commit=aaa", "--meta", "ceph_version=19.2.6",
			"--freeze", write("cand.freeze", anotherMinor), "testdata/run2.xml")
		Expect(status).To(Equal(0), recErr)
		status, _, diffErr := run(ctx, "diff", "--baseline", baseline, "--candidate", filepath.Join(dir, "cand.json"), "--constraints", constraints)
		Expect(status).To(Equal(2))
		line := "pytest_freeze: baseline " + parity.FreezeDigest([]byte(venvConstraints)) + ", candidate " + parity.FreezeDigest([]byte(anotherMinor))
		Expect(diffErr).To(ContainSubstring(line))
		Expect(stderr).To(ContainSubstring(line))
	})

	It("refuses a patch release too when the constraints are not the venv the ledger was recorded under", func(ctx SpecContext) {
		baseline = appendLedger(ctx, filepath.Join(dir, "moved.json"),
			[]string{"--meta", "pytest_freeze=" + parity.FreezeDigest([]byte("# Python 3.14.4\nboto3==1.44.2\nbotocore==1.44.2\nPyYAML==6.0.3\n"))},
			"testdata/run1.xml", "testdata/run2.xml")
		status, _, stderr := compare(ctx, anotherPatch)
		Expect(status).To(Equal(1))
		Expect(stderr).To(ContainSubstring(constraints + "'s pytest_freeze " + parity.FreezeDigest([]byte(venvConstraints)) + " is not the baseline's"))
	})

	It("lets another Python through under --allow-meta-drift, as diff does, and under PARITY_ALLOW_META_DRIFT, without a note", func(ctx SpecContext) {
		status, _, stderr := compare(ctx, anotherMinor, "--allow-meta-drift")
		Expect(status).To(Equal(0), stderr)
		Expect(stderr).To(BeEmpty())
		GinkgoT().Setenv("PARITY_ALLOW_META_DRIFT", "true")
		status, _, stderr = compare(ctx, anotherMinor)
		Expect(status).To(Equal(0), stderr)
		Expect(stderr).To(BeEmpty())
	})

	It("stops for the packages first, before it asks the ledger", func(ctx SpecContext) {
		status, _, stderr := compare(ctx, "# Python 3.14.7\nboto3==1.44.3\nPyYAML==6.0.3\n")
		Expect(status).To(Equal(1))
		Expect(stderr).To(ContainSubstring("the venv holds other packages than " + constraints + " names"))
		Expect(stderr).NotTo(ContainSubstring("diff would refuse"))
	})

	It("asks nothing of the venv when the ledger's newest entries name none", func(ctx SpecContext) {
		baseline = appendLedger(ctx, filepath.Join(dir, "bare.json"), nil, "testdata/run1.xml", "testdata/run2.xml")
		status, _, stderr := compare(ctx, anotherMinor)
		Expect(status).To(Equal(0), stderr)
		Expect(stderr).To(BeEmpty())
	})

	It("exits 2 for a baseline that is not there or is not a ledger", func(ctx SpecContext) {
		baseline = filepath.Join(dir, "none.json")
		status, _, stderr := compare(ctx, theVenv)
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("reading the ledger"))

		baseline = write("flat.json", "{}")
		status, _, _ = compare(ctx, theVenv)
		Expect(status).To(Equal(2))
	})

	DescribeTable("exits 2 for a command line that names the mode wrongly",
		func(ctx SpecContext, args []string, want string) {
			status, _, stderr := run(ctx, append([]string{"venv", "--constraints", constraints, "--freeze", write("run.freeze", theVenv)}, args...)...)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring(want))
		},
		Entry("a mode that is neither", []string{"--mode", "both"}, `--mode must be record or compare, not "both"`),
		Entry("compare with no baseline", []string{"--mode", "compare"}, "--baseline is required with --mode compare"),
		Entry("a baseline for a record run", []string{"--baseline", "x.json"}, "--baseline is for --mode compare"),
	)

	It("only warns of another Python in a record run, which the record decides", func(ctx SpecContext) {
		status, _, stderr := run(ctx, "venv", "--constraints", constraints, "--freeze", write("run.freeze", anotherPatch))
		Expect(status).To(Equal(0), stderr)
		Expect(stderr).To(ContainSubstring("parity: warning: the venv was built by Python 3.14.7"))
	})
})
