package parity_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

var _ = Describe("venv --mode compare and diff --constraints", func() {
	const (
		patchVenv = "# Python 3.14.7\n" + venvPackages
		minorVenv = "# Python 3.15.0\n" + venvPackages
		moved     = "# Python 3.14.4\nboto3==1.44.3\nbotocore==1.44.4\nPyYAML==6.0.3\n"
		// otherVenv is a venv the ledger might have been recorded under that
		// is not the constraints'.
		otherVenv = "# Python 3.14.4\nboto3==1.44.2\nbotocore==1.44.2\nPyYAML==6.0.3\n"
	)
	digest := func(text string) string { return parity.FreezeDigest([]byte(text)) }

	// A candidate's record is made from the .freeze file with record --freeze,
	// as make s3tests-parity does, so that venv, which reads the file, and diff,
	// which reads the record, are given one venv.
	DescribeTable("give one verdict on one .freeze file",
		func(ctx SpecContext, text, recordedUnder string, accepted bool, want ...string) {
			dir := GinkgoT().TempDir()
			write := func(name, content string) string {
				path := filepath.Join(dir, name)
				Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
				return path
			}
			constraints := write("constraints.txt", venvConstraints)
			baseline := appendLedger(ctx, filepath.Join(dir, "baseline.json"),
				[]string{"--meta", "pytest_freeze=" + digest(recordedUnder)}, "testdata/run1.xml", "testdata/run2.xml")
			freezeFile := write("run.freeze", text)
			cand := filepath.Join(dir, "cand.json")
			args := []string{"record", "--format", "junit", "--suite", "s3tests", "--out", cand, "--freeze", freezeFile}
			for _, kv := range candMeta("gateway=rgw-go") {
				args = append(args, "--meta", kv)
			}
			status, _, stderr := run(ctx, append(args, "testdata/run2.xml")...)
			Expect(status).To(Equal(0), stderr)

			venvStatus, _, venvErr := run(ctx, "venv", "--mode", "compare", "--baseline", baseline, "--constraints", constraints, "--freeze", freezeFile)
			diffStatus, _, diffErr := run(ctx, "diff", "--baseline", baseline, "--candidate", cand, "--constraints", constraints)
			if accepted {
				Expect(venvStatus).To(Equal(0), venvErr)
				Expect(diffStatus).To(Equal(0), diffErr)
			} else {
				Expect(venvStatus).NotTo(BeZero(), "venv accepted it, diff said: %s", diffErr)
				Expect(diffStatus).NotTo(BeZero(), "diff accepted it, venv said: %s", venvErr)
			}
			if accepted && len(want) == 0 {
				Expect(venvErr).To(BeEmpty())
				Expect(diffErr).To(BeEmpty())
			}
			for _, w := range want {
				Expect(venvErr).To(ContainSubstring(w), "venv")
				Expect(diffErr).To(ContainSubstring(w), "diff")
			}
		},
		Entry("the venv the ledger was recorded under, both silent", venvConstraints, venvConstraints, true),
		Entry("another patch release of its Python, both with the note", patchVenv, venvConstraints, true,
			"parity: note: pytest_freeze: baseline "+digest(venvConstraints)+", candidate "+digest(patchVenv)+", accepted:"),
		Entry("another minor version, both refused on one line", minorVenv, venvConstraints, false,
			"pytest_freeze: baseline "+digest(venvConstraints)+", candidate "+digest(minorVenv)),
		Entry("another package version, both refused", moved, venvConstraints, false),
		Entry("a patch release when the constraints are not the venv the ledger was recorded under, both refused on one line",
			patchVenv, otherVenv, false,
			"pytest_freeze: baseline "+digest(otherVenv)+", candidate "+digest(patchVenv), "is not the baseline's"),
	)
})
