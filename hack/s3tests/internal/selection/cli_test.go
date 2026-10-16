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

// emptyLists returns a lists directory whose deselect lists name nothing.
func emptyLists() string {
	dir := GinkgoT().TempDir()
	layLists(dir, "")
	return dir
}

// frozenLists returns a lists directory that holds the lines of the deselect
// lists at 0239625.
func frozenLists() string {
	dir := GinkgoT().TempDir()
	layFrozenLists(dir)
	return dir
}

// layFrozenLists writes the lines of the deselect lists at 0239625 into the
// lists the command reads in dir, so that the command reads those lines
// however many lists it names.
func layFrozenLists(dir string) {
	var text strings.Builder
	for _, name := range frozenListNames {
		text.WriteString(readFile(filepath.Join(lists0239625, name)))
	}
	layLists(dir, text.String())
}

// layLists writes text into the first of the lists the command reads in dir,
// and leaves the others empty.
func layLists(dir, text string) {
	for i, name := range selection.DeselectLists {
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
		status, stdout, stderr := run(ctx, "", "hash", "--lists", frozenLists())
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal("999e22cc41a0\n"))
	})

	It("takes the lists directory from S3TESTS_LISTS", func(ctx SpecContext) {
		setenv("S3TESTS_LISTS", frozenLists())
		status, stdout, stderr := run(ctx, "", "hash")
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(Equal("999e22cc41a0\n"))
	})

	It("prints the ids it selects from the collection on stdin, a line each", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, readFile(filepath.Join(pinAB228B1, "collect.txt")),
			"select", "--lists", frozenLists())
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
			status, stdout, stderr := run(ctx, collection, append([]string{"select", "--lists", frozenLists()}, args...)...)
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
})
