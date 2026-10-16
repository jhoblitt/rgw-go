package selection_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/selection"
)

// repoRoot is the repository's root, from this package's directory.
const repoRoot = "../../../.."

// moduleSources are what go build ./hack/s3tests reads, from the
// repository's root.
var moduleSources = []string{"go.mod", "go.sum", "hack/s3tests/main.go", "hack/s3tests/internal", "internal"}

// runShTree copies the repository's run.sh, the lib.sh it sources and its
// PIN into a tree of their own laid out as the repository is, with run.sh's
// markers and the lines of the deselect lists at 0239625 beside run.sh. With
// the module, the tree links the sources run.sh builds the command from. It
// returns the tree's root.
func runShTree(withModule bool) string {
	root := GinkgoT().TempDir()
	files := map[string]string{
		filepath.Join(repoRoot, "hack/s3tests/run.sh"):     "hack/s3tests/run.sh",
		filepath.Join(repoRoot, "hack/s3tests/PIN"):        "hack/s3tests/PIN",
		filepath.Join(repoRoot, "hack/rooket/lib.sh"):      "hack/rooket/lib.sh",
		filepath.Join(runSh2f63a04, selection.MarkersFile): "hack/s3tests/" + selection.MarkersFile,
	}
	for from, to := range files {
		data := readFile(from)
		Expect(os.MkdirAll(filepath.Join(root, filepath.Dir(to)), 0o750)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, to), []byte(data), 0o600)).To(Succeed())
	}
	layFrozenLists(filepath.Join(root, "hack/s3tests"))
	if withModule {
		for _, path := range moduleSources {
			from, err := filepath.Abs(filepath.Join(repoRoot, path))
			Expect(err).NotTo(HaveOccurred())
			Expect(os.Symlink(from, filepath.Join(root, path))).To(Succeed())
		}
	}
	return root
}

// runSh runs the tree's run.sh with args, in the spec's environment plus env.
// S3TESTS_REPO names an empty directory, so that a run that gets as far as
// the fetch fails there without reaching the network.
func runSh(ctx context.Context, root string, env []string, args ...string) (ok bool, stdout, stderr string) {
	cmd := exec.CommandContext(ctx, "bash", append([]string{filepath.Join(root, "hack/s3tests/run.sh")}, args...)...)
	cmd.Env = append(append(os.Environ(), "S3TESTS_REPO="+GinkgoT().TempDir()), env...)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	return cmd.Run() == nil, out.String(), errOut.String()
}

// ambient are S3TESTS_ settings each of which would fail the command, were
// it to read them: a lists directory, TESTS file and config file that are
// not there, and log settings it does not know.
func ambient() []string {
	missing := filepath.Join(GinkgoT().TempDir(), "missing")
	return []string{
		"S3TESTS_LISTS=" + missing, "S3TESTS_TESTS=" + missing, "S3TESTS_CONFIG=" + missing,
		"S3TESTS_LOG_LEVEL=bogus", "S3TESTS_LOG_FORMAT=bogus",
	}
}

// writeRunConf writes the conf a squid radosgw run named r1 leaves in the
// tree, and returns its path.
func writeRunConf(root string) string {
	conf := filepath.Join(root, "hack/s3tests/out/squid-radosgw-r1.conf")
	Expect(os.MkdirAll(filepath.Dir(conf), 0o750)).To(Succeed())
	Expect(os.WriteFile(conf, []byte("[DEFAULT]\n"), 0o600)).To(Succeed())
	return conf
}

var _ = Describe("run.sh", func() {
	Describe("--print-deselect", func() {
		It("prints the digest the command makes of the lists beside run.sh", func(ctx SpecContext) {
			ok, stdout, stderr := runSh(ctx, runShTree(true), nil, "--print-deselect")
			Expect(ok).To(BeTrue(), stderr)
			Expect(stdout).To(Equal("999e22cc41a0\n"))
		}, SpecTimeout(5*time.Minute))

		It("rebuilds a command older than the tree's sources", func(ctx SpecContext) {
			root := runShTree(true)
			stale, err := exec.LookPath("true")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.MkdirAll(filepath.Join(root, "bin"), 0o750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(root, "bin/s3tests"), []byte(readFile(stale)), 0o700)).To(Succeed()) //nolint:gosec // the stale command must be executable
			ok, stdout, stderr := runSh(ctx, root, nil, "--print-deselect")
			Expect(ok).To(BeTrue(), stderr)
			Expect(stdout).To(Equal("999e22cc41a0\n"))
		}, SpecTimeout(5*time.Minute))

		It("names the command that builds the command when it cannot build it", func(ctx SpecContext) {
			ok, _, stderr := runSh(ctx, runShTree(false), nil, "--print-deselect")
			Expect(ok).To(BeFalse())
			Expect(stderr).To(ContainSubstring("go build -o bin/s3tests ./hack/s3tests"))
		}, SpecTimeout(5*time.Minute))

		It("takes no S3TESTS_ setting from the environment, nor a config file one names", func(ctx SpecContext) {
			ok, stdout, stderr := runSh(ctx, runShTree(true), ambient(), "--print-deselect")
			Expect(ok).To(BeTrue(), stderr)
			Expect(stdout).To(Equal("999e22cc41a0\n"))
		}, SpecTimeout(5*time.Minute))
	})

	Describe("a run", func() {
		It("takes no S3TESTS_ setting from the environment, nor a config file one names, for its arguments",
			func(ctx SpecContext) {
				root := runShTree(true)
				conf := writeRunConf(root)
				ok, _, stderr := runSh(ctx, root, ambient(), "squid", "radosgw", "r1")
				Expect(ok).To(BeFalse(), "the fetch from an empty directory fails")
				Expect(stderr).NotTo(ContainSubstring("s3tests:"))
				Expect(conf).NotTo(BeAnExistingFile(), "the run got past its arguments to removing its outputs")
			}, SpecTimeout(5*time.Minute))

		DescribeTable("refuses a TESTS file before it removes the run's outputs or fetches anything",
			func(ctx SpecContext, text *string, want string) {
				root := runShTree(true)
				conf := writeRunConf(root)
				tests := filepath.Join(GinkgoT().TempDir(), "tests.txt")
				if text != nil {
					Expect(os.WriteFile(tests, []byte(*text), 0o600)).To(Succeed())
				}
				ok, _, stderr := runSh(ctx, root, nil, "squid", "radosgw", "r1", tests)
				Expect(ok).To(BeFalse())
				Expect(stderr).To(ContainSubstring(want))
				Expect(conf).To(BeAnExistingFile())
				Expect(filepath.Join(root, "hack/s3tests/out/src")).NotTo(BeAnExistingFile())
			},
			Entry("that is not there", nil, "no tests file ", SpecTimeout(5*time.Minute)),
			Entry("that names no test", new("# none yet\n"), " names no test", SpecTimeout(5*time.Minute)),
		)
	})
})
