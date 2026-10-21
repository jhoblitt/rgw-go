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
// markers, the lines of the frozen deselect lists in lists and an empty
// include list beside run.sh. With the module, the tree links the sources
// run.sh builds the command from. It returns the tree's root.
func runShTree(withModule bool, lists string) string {
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
	layFrozenLists(filepath.Join(root, "hack/s3tests"), lists)
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
// it to read them: a lists directory, TESTS file, collection and config file
// that are not there, a gate-only ids file in a directory that is not there,
// and log settings and a set it does not know.
func ambient() []string {
	missing := filepath.Join(GinkgoT().TempDir(), "missing")
	return []string{
		"S3TESTS_LISTS=" + missing, "S3TESTS_TESTS=" + missing, "S3TESTS_UNFILTERED=" + missing,
		"S3TESTS_GATE_ONLY_IDS=" + filepath.Join(missing, "run.gate-only.ids"),
		"S3TESTS_CONFIG=" + missing, "S3TESTS_LOG_LEVEL=bogus", "S3TESTS_LOG_FORMAT=bogus", "S3TESTS_SET=bogus",
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
			ok, stdout, stderr := runSh(ctx, runShTree(true, lists0239625), nil, "--print-deselect")
			Expect(ok).To(BeTrue(), stderr)
			Expect(stdout).To(Equal("999e22cc41a0\n"))
		}, SpecTimeout(5*time.Minute))

		It("rebuilds a command older than the tree's sources", func(ctx SpecContext) {
			root := runShTree(true, lists0239625)
			stale, err := exec.LookPath("true")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.MkdirAll(filepath.Join(root, "bin"), 0o750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(root, "bin/s3tests"), []byte(readFile(stale)), 0o700)).To(Succeed()) //nolint:gosec // the stale command must be executable
			ok, stdout, stderr := runSh(ctx, root, nil, "--print-deselect")
			Expect(ok).To(BeTrue(), stderr)
			Expect(stdout).To(Equal("999e22cc41a0\n"))
		}, SpecTimeout(5*time.Minute))

		It("names the command that builds the command when it cannot build it", func(ctx SpecContext) {
			ok, _, stderr := runSh(ctx, runShTree(false, lists0239625), nil, "--print-deselect")
			Expect(ok).To(BeFalse())
			Expect(stderr).To(ContainSubstring("go build -o bin/s3tests ./hack/s3tests"))
		}, SpecTimeout(5*time.Minute))

		It("takes no S3TESTS_ setting from the environment, nor a config file one names", func(ctx SpecContext) {
			ok, stdout, stderr := runSh(ctx, runShTree(true, lists0239625), ambient(), "--print-deselect")
			Expect(ok).To(BeTrue(), stderr)
			Expect(stdout).To(Equal("999e22cc41a0\n"))
		}, SpecTimeout(5*time.Minute))

		It("prints, for the a8fe2b0 lists and an empty include list, the digest both ledgers record at 7cfa64a",
			func(ctx SpecContext) {
				ok, stdout, stderr := runSh(ctx, runShTree(true, listsA8FE2B0), nil, "--print-deselect")
				Expect(ok).To(BeTrue(), stderr)
				Expect(stdout).To(Equal("499666025fc3\n"))
			}, SpecTimeout(5*time.Minute))
	})

	Describe("a run", func() {
		It("takes no S3TESTS_ setting from the environment, nor a config file one names, for its arguments",
			func(ctx SpecContext) {
				root := runShTree(true, lists0239625)
				conf := writeRunConf(root)
				ok, _, stderr := runSh(ctx, root, ambient(), "squid", "radosgw", "r1", "pr")
				Expect(ok).To(BeFalse(), "the fetch from an empty directory fails")
				Expect(stderr).NotTo(ContainSubstring("s3tests:"))
				Expect(conf).NotTo(BeAnExistingFile(), "the run got past its arguments to removing its outputs")
			}, SpecTimeout(5*time.Minute))

		DescribeTable("refuses a TESTS file before it removes the run's outputs or fetches anything",
			func(ctx SpecContext, text *string, want string) {
				root := runShTree(true, lists0239625)
				conf := writeRunConf(root)
				tests := filepath.Join(GinkgoT().TempDir(), "tests.txt")
				if text != nil {
					Expect(os.WriteFile(tests, []byte(*text), 0o600)).To(Succeed())
				}
				ok, _, stderr := runSh(ctx, root, nil, "squid", "radosgw", "r1", "pr", tests)
				Expect(ok).To(BeFalse())
				Expect(stderr).To(ContainSubstring(want))
				Expect(conf).To(BeAnExistingFile())
				Expect(filepath.Join(root, "hack/s3tests/out/src")).NotTo(BeAnExistingFile())
			},
			Entry("that is not there", nil, "no tests file ", SpecTimeout(5*time.Minute)),
			Entry("that names no test", new("# none yet\n"), " names no test", SpecTimeout(5*time.Minute)),
		)

		DescribeTable("refuses a set other than pr or gate before it removes the run's outputs",
			func(ctx SpecContext, args ...string) {
				root := runShTree(true, lists0239625)
				conf := writeRunConf(root)
				ok, _, stderr := runSh(ctx, root, nil, append([]string{"squid", "radosgw", "r1"}, args...)...)
				Expect(ok).To(BeFalse())
				Expect(stderr).To(ContainSubstring("usage: run.sh squid|tentacle radosgw|rgw-go RUN-ID pr|gate [TESTS-FILE]"))
				Expect(conf).To(BeAnExistingFile())
			},
			Entry("none", SpecTimeout(5*time.Minute)),
			Entry("another word", "all", SpecTimeout(5*time.Minute)),
			Entry("a TESTS file in its place", "tests.txt", SpecTimeout(5*time.Minute)),
		)
	})
})
