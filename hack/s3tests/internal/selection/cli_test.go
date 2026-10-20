package selection_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/selection"
)

// run runs the command with stdin's text and returns its exit status and
// what it printed.
func run(ctx context.Context, stdin string, args ...string) (status int, stdout, stderr string) {
	var out, errOut strings.Builder
	err := selection.Run(ctx, args, strings.NewReader(stdin), &out, &errOut)
	if err != nil {
		errOut.WriteString(err.Error())
	}
	return selection.ExitStatus(err), out.String(), errOut.String()
}

// emptyLists returns a lists directory whose deselect and include lists name
// nothing.
func emptyLists() string {
	dir := GinkgoT().TempDir()
	layLists(dir, "")
	return dir
}

// frozenLists returns a lists directory that holds the lines of the frozen
// deselect lists in from, and an empty include list.
func frozenLists(from string) string {
	dir := GinkgoT().TempDir()
	layFrozenLists(dir, from)
	return dir
}

// layFrozenLists writes the lines of the frozen deselect lists in from into
// the lists the command reads in dir, so that the command reads those lines
// however many lists it names, and leaves the include lists empty.
func layFrozenLists(dir, from string) {
	var text strings.Builder
	for _, name := range frozenListNames {
		text.WriteString(readFile(filepath.Join(from, name)))
	}
	layLists(dir, text.String())
}

// layLists writes text into the first of the deselect lists the command reads
// in dir, and leaves the others and the include lists empty.
func layLists(dir, text string) {
	for i, name := range selection.DeselectLists {
		var data []byte
		if i == 0 {
			data = []byte(text)
		}
		Expect(os.WriteFile(filepath.Join(dir, name), data, 0o600)).To(Succeed())
	}
	layInclude(dir, "")
}

// layInclude writes text into the first of the include lists the command
// reads in dir, and leaves the others empty.
func layInclude(dir, text string) {
	for i, name := range selection.IncludeLists {
		var data []byte
		if i == 0 {
			data = []byte(text)
		}
		Expect(os.WriteFile(filepath.Join(dir, name), data, 0o600)).To(Succeed())
	}
}

// readFile returns the text of the file at path.
func readFile(path string) string {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

// writeFile writes text to name in a directory of its own and returns its path.
func writeFile(name, text string) string {
	path := filepath.Join(GinkgoT().TempDir(), name)
	Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
	return path
}

// setenv sets the environment variable key to value for the spec.
func setenv(key, value string) {
	DeferCleanup(os.Unsetenv, key)
	Expect(os.Setenv(key, value)).To(Succeed())
}

var _ = Describe("the s3tests command", func() {
	It("prints the marker expression of the lists directory's markers", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, "", "expr", "--lists", runSh2f63a04)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal(readFile(filepath.Join(runSh2f63a04, "expr.txt")) + "\n"))
	})

	It("prints the marker expression when the TESTS file names a test", func(ctx SpecContext) {
		tests := writeFile("tests.txt", s3+"test_a\n")
		status, stdout, stderr := run(ctx, "", "expr", "--lists", runSh2f63a04, "--tests", tests)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal(readFile(filepath.Join(runSh2f63a04, "expr.txt")) + "\n"))
	})

	It("refuses in expr a TESTS file that is not there, as run.sh did before it changed anything, and exits 2",
		func(ctx SpecContext) {
			tests := filepath.Join(GinkgoT().TempDir(), "tests.txt")
			status, stdout, stderr := run(ctx, "", "expr", "--lists", runSh2f63a04, "--tests", tests)
			Expect(status).To(Equal(2))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("no tests file " + tests))
		})

	It("refuses in expr a TESTS file that names no test, as run.sh did before it changed anything, and exits 1",
		func(ctx SpecContext) {
			tests := writeFile("tests.txt", "# none yet\n")
			status, stdout, stderr := run(ctx, "", "expr", "--lists", runSh2f63a04, "--tests", tests)
			Expect(status).To(Equal(1))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring("the tests file " + tests + " names no test"))
		})

	It("prints the digest of the lists directory's deselect lists", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, "", "hash", "--lists", frozenLists(lists0239625))
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal("999e22cc41a0\n"))
	})

	It("takes the lists directory from S3TESTS_LISTS", func(ctx SpecContext) {
		setenv("S3TESTS_LISTS", frozenLists(lists0239625))
		status, stdout, stderr := run(ctx, "", "hash")
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal("999e22cc41a0\n"))
	})

	It("prints the ids it selects from the collection on stdin, a line each", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, readFile(filepath.Join(pinAB228B1, "collect.txt")),
			"select", "--lists", frozenLists(lists0239625))
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal(readFile(filepath.Join(pinAB228B1, "selected.txt"))))
		Expect(stderr).To(ContainSubstring(`"selected":455`))
		Expect(stderr).To(ContainSubstring(`"collected":565`))
	})

	Describe("given an empty --tests, as run.sh gives it for a run of the whole set", func() {
		var collection, two string

		BeforeEach(func() {
			collection = readFile(filepath.Join(pinAB228B1, "collect.txt"))
			ids := fileLines(filepath.Join(pinAB228B1, "selected.txt"))
			two = writeFile("two.txt", ids[0]+"\n"+ids[1]+"\n")
		})

		// selected returns how many ids select prints for the collection, the
		// frozen lists and args.
		selected := func(ctx context.Context, args ...string) int {
			status, stdout, stderr := run(ctx, collection, append([]string{"select", "--lists", frozenLists(lists0239625)}, args...)...)
			Expect(status).To(Equal(0), stderr)
			return strings.Count(stdout, "\n")
		}

		It("selects the whole set whatever TESTS file S3TESTS_TESTS names", func(ctx SpecContext) {
			setenv("S3TESTS_TESTS", two)
			Expect(selected(ctx)).To(Equal(2), "S3TESTS_TESTS applies without --tests")
			Expect(selected(ctx, "--tests", "")).To(Equal(455))
		})

		It("selects the whole set whatever TESTS file the config file names", func(ctx SpecContext) {
			setenv("S3TESTS_CONFIG", writeFile("config.yaml", "tests: "+two+"\n"))
			Expect(selected(ctx)).To(Equal(2), "the config file's tests applies without --tests")
			Expect(selected(ctx, "--tests", "")).To(Equal(455))
		})
	})

	It("selects only the TESTS file's tests", func(ctx SpecContext) {
		tests := writeFile("tests.txt", s3+"test_b # why\n")
		status, stdout, stderr := run(ctx, s3+"test_a\n"+s3+"test_b\n", "select", "--lists", emptyLists(), "--tests", tests)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal(s3 + "test_b\n"))
	})

	It("prints every refusal, prints no id and exits 1", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		layLists(dir, s3+"test_a\n"+s3+"test_gone\n")
		status, stdout, stderr := run(ctx, s3+"test_a\n"+s3+"test_ab\n", "select", "--lists", dir)
		Expect(status).To(Equal(1))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring(s3 + "test_a would also deselect " + s3 + "test_ab"))
		Expect(stderr).To(ContainSubstring(s3 + "test_gone names no test the markers leave"))
	})

	It("refuses a TESTS file that is not there, as run.sh did, and exits 2", func(ctx SpecContext) {
		tests := filepath.Join(GinkgoT().TempDir(), "tests.txt")
		status, stdout, stderr := run(ctx, s3+"test_a\n", "select", "--lists", emptyLists(), "--tests", tests)
		Expect(status).To(Equal(2))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring("no tests file " + tests))
	})

	It("exits 2 for a lists directory without the lists", func(ctx SpecContext) {
		status, _, stderr := run(ctx, "", "hash", "--lists", GinkgoT().TempDir())
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring(selection.DeselectLists[0]))
	})

	DescribeTable("exits 2 for a lists directory without an include list",
		func(ctx SpecContext, args ...string) {
			dir := emptyLists()
			Expect(os.Remove(filepath.Join(dir, selection.IncludeLists[0]))).To(Succeed())
			status, stdout, stderr := run(ctx, s3+"test_a\n", append(args, "--lists", dir)...)
			Expect(status).To(Equal(2))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring(selection.IncludeLists[0]))
		},
		Entry("in hash", "hash"),
		Entry("in select", "select"),
	)

	It("prints a digest that covers the include list", func(ctx SpecContext) {
		dir := frozenLists(lists0239625)
		layInclude(dir, s3+"test_x # why\n")
		status, stdout, stderr := run(ctx, "", "hash", "--lists", dir)
		Expect(status).To(Equal(0), stderr)
		want := selection.Hash(frozenDeselect(lists0239625), lines(s3+"test_x"))
		Expect(want).NotTo(Equal("999e22cc41a0"))
		Expect(stdout).To(Equal(want + "\n"))
	})

	Describe("given the 7cfa64a collections, the a8fe2b0 lists and an empty include list, as a pull request's check runs",
		func() {
			It("selects the 457 ids both ledgers record at that pin", func(ctx SpecContext) {
				unfiltered := filepath.Join(pin7CFA64A, "collect-unfiltered.txt")
				status, stdout, stderr := run(ctx, readFile(filepath.Join(pin7CFA64A, "collect.txt")),
					"select", "--lists", frozenLists(listsA8FE2B0), "--unfiltered", unfiltered)
				Expect(status).To(Equal(0), stderr)
				Expect(stdout).To(Equal(readFile(filepath.Join(pin7CFA64A, "selected.txt"))))
				Expect(stderr).To(ContainSubstring(`"selected":457`))
			})

			It("prints the digest both ledgers record at that pin", func(ctx SpecContext) {
				status, stdout, stderr := run(ctx, "", "hash", "--lists", frozenLists(listsA8FE2B0))
				Expect(status).To(Equal(0), stderr)
				Expect(stdout).To(Equal("499666025fc3\n"))
			})
		})

	It("adds the include list's tests from the collection --unfiltered names, in its order", func(ctx SpecContext) {
		dir := emptyLists()
		layInclude(dir, "# area\n"+s3+"test_b # why\n")
		unfiltered := writeFile("unfiltered.txt", s3+"test_a\n"+s3+"test_b\n"+s3+"test_c\n\n3 tests collected in 0.1s\n")
		status, stdout, stderr := run(ctx, s3+"test_a\n"+s3+"test_c\n", "select", "--lists", dir, "--unfiltered", unfiltered)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal(s3 + "test_a\n" + s3 + "test_b\n" + s3 + "test_c\n"))
	})

	It("refuses an include line and exits 1", func(ctx SpecContext) {
		dir := emptyLists()
		layInclude(dir, s3+"test_gone\n")
		unfiltered := writeFile("unfiltered.txt", s3+"test_a\n")
		status, stdout, stderr := run(ctx, s3+"test_a\n", "select", "--lists", dir, "--unfiltered", unfiltered)
		Expect(status).To(Equal(1))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring(s3 + "test_gone names no collected test"))
	})

	It("exits 2 for an --unfiltered file it cannot read, naming it", func(ctx SpecContext) {
		unfiltered := filepath.Join(GinkgoT().TempDir(), "unfiltered.txt")
		status, stdout, stderr := run(ctx, s3+"test_a\n", "select", "--lists", emptyLists(), "--unfiltered", unfiltered)
		Expect(status).To(Equal(2))
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring(unfiltered))
	})

	Describe("given --unfiltered, even empty", func() {
		var dir, unfiltered string

		BeforeEach(func() {
			dir = emptyLists()
			layInclude(dir, s3+"test_b\n")
			unfiltered = writeFile("unfiltered.txt", s3+"test_a\n"+s3+"test_b\n")
		})

		// selected returns select's exit status and the ids it prints for a
		// collection of test_a alone, the include list of test_b and args.
		selected := func(ctx context.Context, args ...string) (int, string) {
			status, stdout, _ := run(ctx, s3+"test_a\n", append([]string{"select", "--lists", dir}, args...)...)
			return status, stdout
		}

		It("takes no collection from S3TESTS_UNFILTERED", func(ctx SpecContext) {
			setenv("S3TESTS_UNFILTERED", unfiltered)
			status, stdout := selected(ctx)
			Expect(status).To(Equal(0), "S3TESTS_UNFILTERED applies without --unfiltered")
			Expect(stdout).To(Equal(s3 + "test_a\n" + s3 + "test_b\n"))
			status, stdout = selected(ctx, "--unfiltered", "")
			Expect(status).To(Equal(1))
			Expect(stdout).To(BeEmpty())
		})

		It("takes no collection from the config file", func(ctx SpecContext) {
			setenv("S3TESTS_CONFIG", writeFile("config.yaml", "unfiltered: "+unfiltered+"\n"))
			status, stdout := selected(ctx)
			Expect(status).To(Equal(0), "the config file's unfiltered applies without --unfiltered")
			Expect(stdout).To(Equal(s3 + "test_a\n" + s3 + "test_b\n"))
			status, stdout = selected(ctx, "--unfiltered", "")
			Expect(status).To(Equal(1))
			Expect(stdout).To(BeEmpty())
		})
	})
})
