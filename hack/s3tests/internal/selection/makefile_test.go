package selection_test

import (
	"context"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// makeDryRun returns the commands make -n prints for args in the repository,
// each with its continuation lines joined to it, under an environment of
// PATH and HOME alone plus env, so that no variable of the caller's, nor the
// jobserver of a make running go test, reaches it. make -n still runs the
// recipes' $(MAKE) lines, so a target's sub-makes print their commands too.
func makeDryRun(ctx context.Context, env []string, args ...string) []string {
	cmd := exec.CommandContext(ctx, "make", append([]string{"-C", repoRoot, "-n", "--no-print-directory"}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, env...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "make -n %s: %s", strings.Join(args, " "), out)
	var commands []string
	joined := ""
	for line := range strings.SplitSeq(strings.TrimSuffix(string(out), "\n"), "\n") {
		if part, more := strings.CutSuffix(line, "\\"); more {
			joined += part
			continue
		}
		commands = append(commands, joined+line)
		joined = ""
	}
	return commands
}

// runs returns the arguments of each run of run.sh among commands, after its
// path, leaving out --print-commit and --print-deselect.
func runs(commands []string) []string {
	var out []string
	for _, c := range commands {
		_, args, ok := strings.Cut(c, "hack/s3tests/run.sh ")
		if ok && !strings.HasPrefix(args, "--") {
			out = append(out, strings.TrimSpace(args))
		}
	}
	return out
}

// diffs returns the commands among commands that run the comparison.
func diffs(commands []string) []string {
	var out []string
	for _, c := range commands {
		if strings.Contains(c, "bin/parity diff ") {
			out = append(out, c)
		}
	}
	return out
}

var _ = Describe("the Makefile's s3-tests targets", func() {
	DescribeTable("run the set",
		func(ctx SpecContext, env, args, want []string) {
			Expect(runs(makeDryRun(ctx, env, args...))).To(Equal(want))
		},
		Entry("the pr set for make s3tests by default", nil,
			[]string{"s3tests", "RELEASE=squid"}, []string{"squid radosgw local pr"}),
		Entry("the set SET names for make s3tests", nil,
			[]string{"s3tests", "RELEASE=squid", "SET=gate", "TESTS=t.txt"}, []string{"squid radosgw local gate t.txt"}),
		Entry("the pr set for make s3tests-parity by default", nil,
			[]string{"s3tests-parity", "RELEASE=squid"}, []string{"squid rgw-go parity pr"}),
		Entry("the gate set for make s3tests-parity SET=gate", nil,
			[]string{"s3tests-parity", "RELEASE=squid", "SET=gate"}, []string{"squid rgw-go parity gate"}),
		Entry("the gate set for both of make s3tests-record's runs, whatever SET says, so that a full record "+
			"retires no gate-only test", []string{"SET=pr"},
			[]string{"s3tests-record", "RELEASE=squid", "SET=pr"},
			[]string{"squid radosgw base1 gate", "squid radosgw base2 gate"}),
		Entry("the gate set for both of make s3tests-record-tests's runs, whatever SET says, so that TESTS may "+
			"name a gate-only test", []string{"SET=pr"},
			[]string{"s3tests-record-tests", "RELEASE=squid", "SET=pr", "TESTS=t.txt"},
			[]string{"squid radosgw tests1 gate t.txt", "squid radosgw tests2 gate t.txt"}),
	)

	It("compares the pr set's run with the ledger over the tests it selected, given its gate-only tests", func(ctx SpecContext) {
		got := diffs(makeDryRun(ctx, nil, "s3tests-parity", "RELEASE=squid"))
		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(HaveSuffix(" --selection hack/s3tests/out/squid-rgw-go-parity.ids" +
			" --gate-only hack/s3tests/out/squid-rgw-go-parity.gate-only.ids"))
	})

	It("appends the comparison's dropped tests to SUMMARY when it is set, and to no file when it is not",
		func(ctx SpecContext) {
			got := diffs(makeDryRun(ctx, nil, "s3tests-parity", "RELEASE=squid", "SUMMARY=/run/summary.md"))
			Expect(got).To(HaveLen(1))
			Expect(got[0]).To(ContainSubstring(` --summary "/run/summary.md"`))
			got = diffs(makeDryRun(ctx, nil, "s3tests-parity", "RELEASE=squid"))
			Expect(got).To(HaveLen(1))
			Expect(got[0]).NotTo(ContainSubstring("--summary"))
		})

	It("compares the gate set's run with every test of the ledger", func(ctx SpecContext) {
		got := diffs(makeDryRun(ctx, nil, "s3tests-parity", "RELEASE=squid", "SET=gate"))
		Expect(got).To(HaveLen(1))
		Expect(got[0]).NotTo(ContainSubstring("--selection"))
		Expect(got[0]).NotTo(ContainSubstring("--gate-only"))
	})
})
