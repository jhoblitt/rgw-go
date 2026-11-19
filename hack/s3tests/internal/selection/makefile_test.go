package selection_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	return makeDryRunIn(ctx, repoRoot, env, args...)
}

// makeDryRunIn is makeDryRun in the directory dir.
func makeDryRunIn(ctx context.Context, dir string, env []string, args ...string) []string {
	out, err := makeDryRunCmd(ctx, dir, env, args...).CombinedOutput()
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

// makeDryRunCmd returns the make -n of args in dir that makeDryRunIn runs.
func makeDryRunCmd(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "make", append([]string{"-C", dir, "-n", "--no-print-directory"}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, env...)
	return cmd
}

// recordTree copies the repository's Makefile into a tree of its own beside
// the .gate-only.ids files Squid's two runs of the pr set would leave, each
// holding ids, so that a spec of make s3tests-record, which records the
// gate-only tests only when the selection left one out, decides what it
// left. It returns the tree's root.
func recordTree(ids string) string {
	root := GinkgoT().TempDir()
	Expect(os.WriteFile(filepath.Join(root, "Makefile"), []byte(readFile(filepath.Join(repoRoot, "Makefile"))), 0o600)).To(Succeed())
	Expect(os.MkdirAll(filepath.Join(root, "hack/s3tests/out"), 0o750)).To(Succeed())
	for _, run := range []string{"base1", "base2"} {
		path := filepath.Join(root, "hack/s3tests/out/squid-radosgw-"+run+".gate-only.ids")
		Expect(os.WriteFile(path, []byte(ids), 0o600)).To(Succeed())
	}
	return root
}

// The gate-only ids recordTree is given: a selection that left one test out,
// and one that, as the repository's did before any area landed its
// gate-only lines, left none.
const (
	gateOnlyOne  = "s3tests/functional/test_s3.py::test_lifecycle_expiration\n"
	gateOnlyNone = ""
)

// gateOnlyIDs is the .gate-only.ids file make s3tests-record leaves and runs
// alone on Squid: its second run's.
const gateOnlyIDs = "hack/s3tests/out/squid-radosgw-base2.gate-only.ids"

// chainedRecords returns the records the commands chain in braces, as make
// s3tests-record's one recording command does, each from its ceph_version to
// its run files.
func chainedRecords(commands []string) []string {
	var out []string
	for _, c := range commands {
		parts := strings.Split(c, "{ ceph_version=")
		for _, part := range parts[1:] {
			record, _, _ := strings.Cut(part, "; }")
			out = append(out, "ceph_version="+record)
		}
	}
	return out
}

// recording returns the one command among commands that chains records.
func recording(commands []string) string {
	var found []string
	for _, c := range commands {
		if strings.Contains(c, "{ ceph_version=") {
			found = append(found, c)
		}
	}
	Expect(found).To(HaveLen(1))
	return found[0]
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

// lcModes returns, in order, a "lc-mode MODE" for each run of the s3tests
// command's lc-mode among commands and a "run.sh ARGS" for each run of
// run.sh.
func lcModes(commands []string) []string {
	var out []string
	for _, c := range commands {
		if _, args, ok := strings.Cut(c, "go run ./hack/s3tests lc-mode "); ok {
			_, mode, ok := strings.Cut(args, " --mode ")
			Expect(ok).To(BeTrue(), "lc-mode is given its mode: %s", c)
			mode, _, _ = strings.Cut(mode, " ")
			out = append(out, "lc-mode "+mode)
		}
		if _, args, ok := strings.Cut(c, "hack/s3tests/run.sh "); ok && !strings.HasPrefix(args, "--") {
			out = append(out, "run.sh "+strings.TrimSpace(args))
		}
	}
	return out
}

// records returns the commands among commands that record a run.
func records(commands []string) []string {
	var out []string
	for _, c := range commands {
		if strings.Contains(c, "bin/parity record ") {
			out = append(out, c)
		}
	}
	return out
}

var _ = Describe("the Makefile's s3-tests targets", func() {
	DescribeTable("set the LC mode LC names before each run",
		func(ctx SpecContext, args, want []string) {
			Expect(lcModes(makeDryRun(ctx, nil, args...))).To(Equal(want))
		},
		Entry("the parity mode, off, by default", []string{"s3tests", "RELEASE=squid"},
			[]string{"lc-mode off", "run.sh squid radosgw local pr"}),
		Entry("the mode LC names for make s3tests", []string{"s3tests", "RELEASE=squid", "LC=both"},
			[]string{"lc-mode both", "run.sh squid radosgw local pr"}),
		Entry("the mode LC names for make s3tests-parity", []string{"s3tests-parity", "RELEASE=squid", "LC=rgw-go"},
			[]string{"lc-mode rgw-go", "run.sh squid rgw-go parity pr"}),
		Entry("the mode LC names before each of make s3tests-record-tests's runs",
			[]string{"s3tests-record-tests", "RELEASE=squid", "LC=radosgw", "TESTS=t.txt"},
			[]string{"lc-mode radosgw", "run.sh squid radosgw tests1 gate t.txt", "lc-mode radosgw", "run.sh squid radosgw tests2 gate t.txt"}),
	)

	It("runs the admin suite in the parity mode, whatever LC says", func(ctx SpecContext) {
		var got []string
		for _, c := range makeDryRun(ctx, nil, "admin-suite", "RELEASE=squid", "LC=radosgw") {
			if _, args, ok := strings.Cut(c, "go run ./hack/s3tests lc-mode "); ok {
				_, mode, _ := strings.Cut(args, " --mode ")
				mode, _, _ = strings.Cut(mode, " ")
				got = append(got, "lc-mode "+mode)
			}
			if strings.Contains(c, "hack/admin/run.sh ") {
				got = append(got, "admin/run.sh")
			}
		}
		Expect(got).To(Equal([]string{"lc-mode off", "admin/run.sh"}))
	})

	It("adds the storage classes after populate.sh, giving the command every flag", func(ctx SpecContext) {
		commands := makeDryRun(ctx, nil, "populate", "RELEASE=squid", "ROOKET=/opt/rooket")
		root, err := filepath.Abs(repoRoot)
		Expect(err).NotTo(HaveOccurred())
		var got []string
		for _, c := range commands {
			if strings.Contains(c, "hack/rooket/populate.sh") || strings.Contains(c, "./hack/s3tests classes") {
				got = append(got, strings.Join(strings.Fields(c), " "))
			}
		}
		Expect(got).To(Equal([]string{
			"ROOKET=/opt/rooket hack/rooket/populate.sh squid",
			`go run ./hack/s3tests classes --config "" --log-level info --log-format json --release squid ` +
				"--rooket /opt/rooket --root " + root,
		}))
	})

	It("gives lc-mode every flag, so that no S3TESTS_ setting changes what it sets", func(ctx SpecContext) {
		var got []string
		for _, c := range makeDryRun(ctx, nil, "lc-mode", "RELEASE=tentacle", "LC=radosgw", "ROOKET=/opt/rooket") {
			if strings.Contains(c, "go run ./hack/s3tests lc-mode ") {
				got = append(got, c)
			}
		}
		Expect(got).To(HaveLen(1))
		root, err := filepath.Abs(repoRoot)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Join(strings.Fields(got[0]), " ")).To(Equal(`go run ./hack/s3tests lc-mode --config "" --log-level info --log-format json ` +
			`--release tentacle --mode radosgw --rooket /opt/rooket --root ` + root + ` --make make`))
	})

	DescribeTable("record the LC mode as the meta lc",
		func(ctx SpecContext, args []string, n int) {
			got := records(makeDryRun(ctx, nil, args...))
			Expect(got).To(HaveLen(n))
			for _, c := range got {
				Expect(c).To(ContainSubstring(" --meta lc=radosgw "))
			}
		},
		Entry("make s3tests-parity's result", []string{"s3tests-parity", "RELEASE=squid", "LC=radosgw"}, 1),
		Entry("make s3tests-record-tests's ledger entries",
			[]string{"s3tests-record-tests", "RELEASE=squid", "LC=radosgw", "TESTS=t.txt"}, 1),
	)

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

var _ = Describe("make s3tests-record", func() {
	It("runs the pr set in the parity mode, then the gate-only tests its selection left out in radosgw's, "+
		"whatever SET says", func(ctx SpecContext) {
		got := lcModes(makeDryRunIn(ctx, recordTree(gateOnlyOne), []string{"SET=gate"}, "s3tests-record", "RELEASE=squid", "SET=gate"))
		Expect(got).To(Equal([]string{
			"lc-mode off", "run.sh squid radosgw base1 pr",
			"lc-mode off", "run.sh squid radosgw base2 pr",
			"lc-mode radosgw", "run.sh squid radosgw tests1 gate " + gateOnlyIDs,
			"lc-mode radosgw", "run.sh squid radosgw tests2 gate " + gateOnlyIDs,
		}))
	})

	It("runs the pr set alone when its selection left no gate-only test out", func(ctx SpecContext) {
		got := lcModes(makeDryRunIn(ctx, recordTree(gateOnlyNone), nil, "s3tests-record", "RELEASE=squid"))
		Expect(got).To(Equal([]string{
			"lc-mode off", "run.sh squid radosgw base1 pr",
			"lc-mode off", "run.sh squid radosgw base2 pr",
		}))
	})

	It("refuses runs of the pr set that left different gate-only tests out before it records", func(ctx SpecContext) {
		commands := makeDryRunIn(ctx, recordTree(gateOnlyOne), nil, "s3tests-record", "RELEASE=squid")
		i := slices.IndexFunc(commands, func(c string) bool { return strings.HasPrefix(c, "cmp -s ") })
		Expect(i).To(BeNumerically(">=", 0))
		Expect(commands[i]).To(HavePrefix("cmp -s hack/s3tests/out/squid-radosgw-base1.gate-only.ids " + gateOnlyIDs + " || "))
		Expect(slices.Index(commands, recording(commands))).To(BeNumerically(">", i))
	})

	It("appends the pr set as a full record that leaves the gate-only tests, then those as a subset record, "+
		"each carrying its LC mode as the meta lc", func(ctx SpecContext) {
		got := chainedRecords(makeDryRunIn(ctx, recordTree(gateOnlyOne), nil, "s3tests-record", "RELEASE=squid"))
		Expect(got).To(HaveLen(2))
		Expect(got[0]).To(ContainSubstring(" --leave " + gateOnlyIDs + " "))
		Expect(got[0]).NotTo(ContainSubstring("--subset"))
		Expect(got[0]).To(ContainSubstring(" --meta lc=off "))
		Expect(got[0]).To(HaveSuffix(" hack/s3tests/out/squid-radosgw-base1.xml hack/s3tests/out/squid-radosgw-base2.xml"))
		Expect(got[1]).To(ContainSubstring(" --subset "))
		Expect(got[1]).NotTo(ContainSubstring("--leave"))
		Expect(got[1]).To(ContainSubstring(" --meta lc=radosgw "))
		Expect(got[1]).To(HaveSuffix(" hack/s3tests/out/squid-radosgw-tests1.xml hack/s3tests/out/squid-radosgw-tests2.xml"))
	})

	It("appends both records to a copy beside OUT, which replaces OUT after the second", func(ctx SpecContext) {
		commands := makeDryRunIn(ctx, recordTree(gateOnlyOne), nil, "s3tests-record", "RELEASE=squid", "OUT=/x/squid.json")
		c := recording(commands)
		Expect(c).To(HavePrefix(`scratch=/x/.squid.json.recording && trap 'rm -f "$scratch"' EXIT && rm -f "$scratch" && `))
		Expect(c).To(ContainSubstring(`cp /x/squid.json "$scratch"`))
		for _, record := range chainedRecords(commands) {
			Expect(record).To(ContainSubstring(` --ledger "$scratch" `))
		}
		Expect(c).NotTo(ContainSubstring("--ledger /x/squid.json"))
		Expect(c).To(MatchRegexp(`; }; } && +mv -f "\$scratch" /x/squid\.json$`))
		Expect(strings.Index(c, "mv -f")).To(BeNumerically(">", strings.LastIndex(c, "bin/parity record ")))
	})

	It("gives the full record REASONS and PIN_REASON, the gate-only record GATE_ONLY_REASONS, and both DRAFT",
		func(ctx SpecContext) {
			got := chainedRecords(makeDryRunIn(ctx, recordTree(gateOnlyOne), nil, "s3tests-record", "RELEASE=squid",
				"REASONS=pr.txt", "PIN_REASON=the pin moved", "GATE_ONLY_REASONS=gate.txt", "DRAFT=recorded by CI"))
			Expect(got).To(HaveLen(2))
			Expect(got[0]).To(SatisfyAll(ContainSubstring(" --reasons pr.txt "), ContainSubstring(` --pin-reason "the pin moved" `),
				ContainSubstring(` --draft "recorded by CI" `)))
			Expect(got[1]).To(SatisfyAll(ContainSubstring(" --reasons gate.txt "), ContainSubstring(` --draft "recorded by CI" `),
				Not(ContainSubstring("--pin-reason"))))
		})

	DescribeTable("refuses LC, which no longer chooses a record's mode, before it runs anything",
		func(ctx SpecContext, env, args []string) {
			out, err := makeDryRunCmd(ctx, recordTree(gateOnlyOne), env, append([]string{"s3tests-record", "RELEASE=squid"}, args...)...).
				CombinedOutput()
			Expect(err).To(HaveOccurred(), "%s", out)
			Expect(string(out)).To(ContainSubstring("s3tests-record takes no LC"))
			Expect(string(out)).NotTo(ContainSubstring("run.sh"))
		},
		Entry("on the command line", nil, []string{"LC=radosgw"}),
		Entry("from the environment", []string{"LC=off"}, nil),
	)
})
