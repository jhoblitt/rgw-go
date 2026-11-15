package selection_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// venvConstraints is a small constraints.txt: a freeze, the Python then
	// the packages, as a venv leaves one.
	venvConstraints = "# Python 3.14.4\nboto3==1.44.3\nbotocore==1.44.3\n"
	// venvPackages are the lines of venvConstraints below the Python's.
	venvPackages = "boto3==1.44.3\nbotocore==1.44.3\n"

	stubPython3 = `#!/usr/bin/env bash
# The python3 run.sh builds its venv with: python3 -m venv DIR lays out a venv
# whose python and pip are stubs that answer from the spec's environment.
set -euo pipefail
[[ "$1 $2" == "-m venv" ]] || { echo "python3 $*: the stub builds venvs only" >&2; exit 99; }
mkdir -p "$3/bin"
cat >"$3/bin/python" <<'EOS'
#!/usr/bin/env bash
echo "Python ${STUB_PYTHON}"
EOS
cat >"$3/bin/pip" <<'EOS'
#!/usr/bin/env bash
case "$1" in
install) echo "$*" >>"${STUB_PIP_LOG}" ;;
freeze) cat "${STUB_FREEZE}" ;;
esac
EOS
chmod +x "$3/bin/python" "$3/bin/pip"
`
	// stubRooket marks that run.sh reached its cluster, the step after the
	// venv, and ends the run's process group there: wait_rgw would retry for
	// three minutes.
	stubRooket = `#!/usr/bin/env bash
: >"${STUB_REACHED}"
read -r -a stat <"/proc/$$/stat"
kill -KILL -- "-${stat[4]}"
`
)

// cephRepo returns a repository that serves the suite at one commit, as
// jhoblitt/ceph does, and the commit.
func cephRepo() (dir, commit string) {
	dir = GinkgoT().TempDir()
	git := func(args ...string) string {
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=s3tests", "GIT_AUTHOR_EMAIL=s3tests@rgw-go.test",
			"GIT_COMMITTER_NAME=s3tests", "GIT_COMMITTER_EMAIL=s3tests@rgw-go.test")
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "maintenance.auto", "false")
	git("config", "gc.auto", "0")
	git("config", "uploadpack.allowFilter", "true")
	git("config", "uploadpack.allowAnySHA1InWant", "true")
	suite := filepath.Join(dir, "src/test/rgw/s3-tests")
	Expect(os.MkdirAll(suite, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(suite, "requirements.txt"), []byte("boto3\nbotocore\n"), 0o600)).To(Succeed())
	git("add", "-A")
	git("commit", "-q", "--no-verify", "-m", "the suite")
	return dir, git("rev-parse", "HEAD")
}

// venvRun is a run.sh run that builds a venv under stubs.
type venvRun struct {
	root, bin, pipLog, freeze, reached string
	python                             string
	repo, commit                       string
	// gateway is the gateway the run is of, radosgw unless a spec says
	// another, and baseline the ledger S3TESTS_BASELINE names, if any.
	gateway, baseline string
}

// newVenvRun lays a run.sh tree with constraints, the stubs, and a repository
// the pin is fetched from, for a venv whose pip freeze lists packages. The
// tree links hack/parity, which run.sh builds to check the venv.
func newVenvRun(constraints, packages string) *venvRun {
	v := &venvRun{root: runShTree(true, lists0239625), bin: GinkgoT().TempDir(), python: "3.14.4", gateway: "radosgw"}
	parity, err := filepath.Abs(filepath.Join(repoRoot, "hack/parity"))
	Expect(err).NotTo(HaveOccurred())
	Expect(os.Symlink(parity, filepath.Join(v.root, "hack/parity"))).To(Succeed())
	v.repo, v.commit = cephRepo()
	for name, text := range map[string]string{"python3": stubPython3, "rooket": stubRooket} {
		Expect(os.WriteFile(filepath.Join(v.bin, name), []byte(text), 0o700)).To(Succeed()) //nolint:gosec // a stub the spec runs
	}
	state := GinkgoT().TempDir()
	v.pipLog, v.freeze, v.reached = filepath.Join(state, "pip.log"), filepath.Join(state, "freeze"), filepath.Join(state, "reached")
	v.setConstraints(constraints)
	v.setFreeze(packages)
	return v
}

func (v *venvRun) setConstraints(text string) {
	Expect(os.WriteFile(filepath.Join(v.root, "hack/s3tests/constraints.txt"), []byte(text), 0o600)).To(Succeed())
}

func (v *venvRun) setFreeze(packages string) {
	Expect(os.WriteFile(v.freeze, []byte(packages), 0o600)).To(Succeed())
}

// run runs run.sh for a run of the pr set against the gateway, which builds
// the venv and then, at the cluster, ends in the stub rooket, and returns what
// it printed.
func (v *venvRun) run(ctx context.Context) (stdout, stderr string) {
	Expect(os.Remove(v.reached)).To(Or(Succeed(), MatchError(os.ErrNotExist)))
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(v.root, "hack/s3tests/run.sh"), "squid", v.gateway, "r1", "pr")
	cmd.Env = append(os.Environ(),
		"PATH="+v.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"ROOKET="+filepath.Join(v.bin, "rooket"),
		"S3TESTS_REPO="+v.repo, "S3TESTS_COMMIT="+v.commit,
		"STUB_PYTHON="+v.python, "STUB_PIP_LOG="+v.pipLog, "STUB_FREEZE="+v.freeze, "STUB_REACHED="+v.reached)
	if v.baseline != "" {
		cmd.Env = append(cmd.Env, "S3TESTS_BASELINE="+v.baseline)
	}
	// The stub rooket ends the process group, which holds every subshell
	// run.sh started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	// The stub ends a run that gets as far as the cluster, and a refusal exits
	// 1, so an error is the usual end.
	if err := cmd.Run(); err != nil {
		GinkgoWriter.Printf("run.sh: %v\n", err)
	}
	return out.String(), errOut.String()
}

func (v *venvRun) reachedCluster() bool {
	_, err := os.Stat(v.reached)
	return err == nil
}

func (v *venvRun) installs() []string {
	data, err := os.ReadFile(v.pipLog)
	if os.IsNotExist(err) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

var _ = Describe("run.sh's venv", func() {
	It("installs the suite's requirements and pytest-timeout under constraints.txt, and goes on when the venv holds those packages",
		func(ctx SpecContext) {
			v := newVenvRun(venvConstraints, venvPackages)
			_, stderr := v.run(ctx)
			Expect(stderr).NotTo(ContainSubstring("run.sh:"))
			Expect(stderr).NotTo(ContainSubstring("parity:"))
			Expect(v.installs()).To(ConsistOf(
				"install -q --disable-pip-version-check -c " + filepath.Join(v.root, "hack/s3tests/constraints.txt") +
					" -r " + filepath.Join(v.root, "hack/s3tests/out/src/src/test/rgw/s3-tests/requirements.txt") + " pytest-timeout"))
			Expect(v.reachedCluster()).To(BeTrue(), "run.sh went on past the venv to the cluster")
			freeze, err := os.ReadFile(filepath.Join(v.root, "hack/s3tests/out/squid-radosgw-r1.freeze"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(freeze)).To(Equal("# Python 3.14.4\n" + venvPackages))
		}, SpecTimeout(5*time.Minute))

	DescribeTable("stops before the cluster, showing both sides, when the venv holds other packages than constraints.txt names",
		func(ctx SpecContext, packages string, want ...string) {
			v := newVenvRun(venvConstraints, packages)
			_, stderr := v.run(ctx)
			Expect(stderr).To(ContainSubstring("parity: the venv holds other packages than " +
				filepath.Join(v.root, "hack/s3tests/constraints.txt") + " names (< constraints, > venv):"))
			for _, line := range want {
				Expect(stderr).To(ContainSubstring(line))
			}
			Expect(v.reachedCluster()).To(BeFalse())
		},
		Entry("a package at another version", "boto3==1.44.3\nbotocore==1.44.4\n", "< botocore==1.44.3", "> botocore==1.44.4", SpecTimeout(5*time.Minute)),
		Entry("a package it does not name, as a new requirement installs at its latest version",
			"boto3==1.44.3\nbotocore==1.44.3\nmoto==5.2.0\n", "> moto==5.2.0", SpecTimeout(5*time.Minute)),
		Entry("a package missing", "boto3==1.44.3\n", "< botocore==1.44.3", SpecTimeout(5*time.Minute)),
	)

	It("only warns of another Python, since the packages are the constraints'", func(ctx SpecContext) {
		v := newVenvRun(venvConstraints, venvPackages)
		v.python = "3.14.7"
		_, stderr := v.run(ctx)
		Expect(stderr).To(ContainSubstring("parity: warning: the venv was built by Python 3.14.7, " +
			filepath.Join(v.root, "hack/s3tests/constraints.txt") + " names Python 3.14.4: this run's pytest_freeze will differ from the ledgers'"))
		Expect(v.reachedCluster()).To(BeTrue())
	}, SpecTimeout(5*time.Minute))

	Describe("of rgw-go, whose run diff compares with the ledger", func() {
		// current is the repository's constraints, which both ledgers' newest
		// entries were recorded under, and its Python.
		var current, python, packages, ledger string

		BeforeEach(func() {
			current = readFile(filepath.Join(repoRoot, "hack/s3tests/constraints.txt"))
			header, rest, _ := strings.Cut(current, "\n")
			python, packages = strings.TrimPrefix(header, "# Python "), rest
			ledger = filepath.Join(repoRoot, "test/s3tests/baseline/squid.json")
		})

		// newRgwGoRun lays a run of rgw-go in a venv under the repository's
		// constraints, built by the Python python.
		newRgwGoRun := func(python string) *venvRun {
			v := newVenvRun(current, packages)
			v.gateway, v.baseline, v.python = "rgw-go", ledger, python
			return v
		}

		It("goes on when the venv is the one the ledger's newest entries were recorded under", func(ctx SpecContext) {
			v := newRgwGoRun(python)
			_, stderr := v.run(ctx)
			Expect(stderr).NotTo(ContainSubstring("parity:"))
			Expect(v.reachedCluster()).To(BeTrue(), "run.sh went on past the venv to the cluster")
		}, SpecTimeout(5*time.Minute))

		It("stops at its start, as diff would at its end, when the Python is another, naming both digests", func(ctx SpecContext) {
			v := newRgwGoRun("3.14.0")
			_, stderr := v.run(ctx)
			sum := func(text string) string { sha := sha256.Sum256([]byte(text)); return hex.EncodeToString(sha[:])[:12] }
			Expect(stderr).To(ContainSubstring("parity: diff would refuse this run against " + ledger + " before it compared any outcome: " +
				"pytest_freeze: baseline " + sum(current) + ", candidate " + sum("# Python 3.14.0\n"+packages)))
			Expect(v.reachedCluster()).To(BeFalse())
		}, SpecTimeout(5*time.Minute))

		It("takes the ledger from the tree's committed ones unless S3TESTS_BASELINE names another", func(ctx SpecContext) {
			v := newRgwGoRun("3.14.0")
			v.baseline = ""
			_, stderr := v.run(ctx)
			Expect(stderr).To(ContainSubstring("parity: reading the ledger: open " +
				filepath.Join(v.root, "test/s3tests/baseline/squid.json")))
			Expect(v.reachedCluster()).To(BeFalse())
		}, SpecTimeout(5*time.Minute))

		It("lets another Python through under PARITY_ALLOW_META_DRIFT, which diff reads too", func(ctx SpecContext) {
			GinkgoT().Setenv("PARITY_ALLOW_META_DRIFT", "true")
			v := newRgwGoRun("3.14.0")
			_, stderr := v.run(ctx)
			Expect(stderr).NotTo(ContainSubstring("parity:"))
			Expect(v.reachedCluster()).To(BeTrue())
		}, SpecTimeout(5*time.Minute))
	})

	It("builds the venv once for a commit and constraints, and again when the constraints change", func(ctx SpecContext) {
		v := newVenvRun(venvConstraints, venvPackages)
		v.run(ctx)
		v.run(ctx)
		Expect(v.installs()).To(HaveLen(1), "the second run reused the venv")

		v.setConstraints("# Python 3.14.4\nboto3==1.44.4\nbotocore==1.44.4\n")
		v.setFreeze("boto3==1.44.4\nbotocore==1.44.4\n")
		_, stderr := v.run(ctx)
		Expect(stderr).NotTo(ContainSubstring("run.sh:"))
		Expect(v.installs()).To(HaveLen(2), "the changed constraints rebuilt it")
	}, SpecTimeout(5*time.Minute))

	It("stops when constraints.txt is not there, or is a symbolic link", func(ctx SpecContext) {
		v := newVenvRun(venvConstraints, venvPackages)
		file := filepath.Join(v.root, "hack/s3tests/constraints.txt")
		Expect(os.Remove(file)).To(Succeed())
		_, stderr := v.run(ctx)
		Expect(stderr).To(ContainSubstring("run.sh: no " + file + ": the freeze of the venv the ledgers were recorded under, as a regular file"))

		Expect(os.WriteFile(file+".real", []byte(venvConstraints), 0o600)).To(Succeed())
		Expect(os.Symlink(file+".real", file)).To(Succeed())
		_, stderr = v.run(ctx)
		Expect(stderr).To(ContainSubstring("run.sh: no " + file))
		Expect(v.reachedCluster()).To(BeFalse())
		Expect(v.installs()).To(BeEmpty())
	}, SpecTimeout(5*time.Minute))
})
