package parity_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

const (
	repoRunSh = "../../../s3tests/run.sh"
	repoPin   = "../../../s3tests/PIN"
	repoLib   = "../../../rooket/lib.sh"
)

var (
	nul = string([]byte{0})
	bom = string(rune(0xfeff))
)

// runShTree copies the repository's run.sh and the lib.sh it sources into
// a directory of their own, with pin as its PIN file's text, and returns
// run.sh's path there.
func runShTree(pin string) string {
	root := GinkgoT().TempDir()
	for from, to := range map[string]string{repoRunSh: "hack/s3tests/run.sh", repoLib: "hack/rooket/lib.sh"} {
		data, err := os.ReadFile(from)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Join(root, filepath.Dir(to)), 0o750)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, to), data, 0o700)).To(Succeed())
	}
	Expect(os.WriteFile(filepath.Join(root, "hack/s3tests/PIN"), []byte(pin), 0o600)).To(Succeed())
	return filepath.Join(root, "hack/s3tests/run.sh")
}

// printCommit runs run.sh --print-commit with env added to an environment
// that sets neither S3TESTS_COMMIT nor S3TESTS_REPO.
func printCommit(ctx context.Context, runSh string, env ...string) (ok bool, stdout, stderr string) {
	cmd := exec.CommandContext(ctx, "bash", runSh, "--print-commit")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "S3TESTS_COMMIT=") && !strings.HasPrefix(kv, "S3TESTS_REPO=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	return cmd.Run() == nil, out.String(), errOut.String()
}

var _ = Describe("the PIN file", func() {
	DescribeTable("is read by run.sh and parity by one rule: exactly 40 lowercase hex digits and a newline",
		func(ctx SpecContext, text string, accepted bool) {
			runSh := runShTree(text)
			ok, stdout, stderr := printCommit(ctx, runSh)
			pin, err := parity.ReadPinFile(filepath.Join(filepath.Dir(runSh), "PIN"))
			Expect(ok).To(Equal(accepted), "run.sh: %s%s", stdout, stderr)
			Expect(err == nil).To(Equal(accepted), "parity: %v", err)
			if accepted {
				Expect(stdout).To(Equal(aPin + "\n"))
				Expect(pin).To(Equal(aPin))
			} else {
				Expect(stderr).To(ContainSubstring("is not exactly 40 lowercase hex digits and a newline"))
				Expect(err).To(MatchError(ContainSubstring("is not exactly 40 lowercase hex digits and a newline")))
			}
		},
		Entry("the pin and a newline", aPin+"\n", true),
		Entry("upper case", strings.ToUpper(aPin)+"\n", false),
		Entry("a carriage return before the newline", aPin+cr+"\n", false),
		Entry("no newline", aPin, false),
		Entry("a blank before the newline", aPin+" \n", false),
		Entry("a blank before the pin", " "+aPin+"\n", false),
		Entry("a second line", aPin+"\n"+aPin+"\n", false),
		Entry("a blank line after", aPin+"\n\n", false),
		Entry("39 digits and two newlines", aPin[:39]+"\n\n", false),
		Entry("a byte order mark", bom+aPin+"\n", false),
		Entry("a NUL among the digits", aPin[:39]+nul+"\n", false),
		Entry("41 digits", aPin+"8\n", false),
	)

	It("is refused by both as a symbolic link", func(ctx SpecContext) {
		runSh := runShTree(aPin + "\n")
		dir := filepath.Dir(runSh)
		Expect(os.Rename(filepath.Join(dir, "PIN"), filepath.Join(dir, "PIN.real"))).To(Succeed())
		Expect(os.Symlink("PIN.real", filepath.Join(dir, "PIN"))).To(Succeed())
		ok, _, _ := printCommit(ctx, runSh)
		Expect(ok).To(BeFalse())
		_, err := parity.ReadPinFile(filepath.Join(dir, "PIN"))
		Expect(err).To(MatchError(ContainSubstring("is not a regular file")))
	})

	It("is what --print-commit prints, and says so", func(ctx SpecContext) {
		ok, stdout, stderr := printCommit(ctx, runShTree(aPin+"\n"))
		Expect(ok).To(BeTrue(), stderr)
		Expect(stdout).To(Equal(aPin + "\n"))
		Expect(stderr).To(ContainSubstring("s3-tests " + aPin + ", from "))
		Expect(stderr).To(ContainSubstring("/hack/s3tests/PIN"))
	})

	It("gives way to S3TESTS_COMMIT in the environment, which --print-commit names", func(ctx SpecContext) {
		other := strings.Repeat("e", 40)
		ok, stdout, stderr := printCommit(ctx, runShTree(aPin+"\n"), "S3TESTS_COMMIT="+other)
		Expect(ok).To(BeTrue(), stderr)
		Expect(stdout).To(Equal(other + "\n"))
		Expect(stderr).To(ContainSubstring("s3-tests " + other + ", from the environment, not "))
		Expect(stderr).To(ContainSubstring("PIN's " + aPin))
	})

	It("holds the repository's pin, which run.sh takes from it alone and both s3-tests ledgers end at", func() {
		pin, err := parity.ReadPinFile(repoPin)
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(repoRunSh)
		Expect(err).NotTo(HaveOccurred())
		Expect(parity.PinSourceProblems(string(data), "run.sh")).To(BeEmpty())
		for _, release := range []string{"squid", "tentacle"} {
			l := readLedger("../../../../test/s3tests/baseline/" + release + ".json")
			Expect(l.Pins[len(l.Pins)-1].Source).To(Equal(pin), release)
		}
	})
})
