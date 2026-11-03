package rooket_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeKubectl prints the KUBECONFIG it runs under and its arguments.
const fakeKubectl = `#!/usr/bin/env bash
echo "KUBECONFIG=${KUBECONFIG}"
printf '%s\n' "$@"
`

// builderHome is a HOME holding rooket's state for the cluster name, with
// its kubeconfig, and a bin directory holding the fake kubectl, both in a
// directory of the spec's own.
func builderHome(name string) (home, bin string) {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	home, bin = filepath.Join(dir, "home"), filepath.Join(dir, "bin")
	state := filepath.Join(home, ".local", "share", "rooket", name)
	Expect(os.MkdirAll(state, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(state, "kubeconfig"), []byte("apiVersion: v1\n"), 0o600)).To(Succeed())
	Expect(os.MkdirAll(bin, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fakeKubectl), 0o700)).To(Succeed())
	return home, bin
}

func runScript(ctx context.Context, env []string, script string, args ...string) (stdout, stderr string, err error) {
	GinkgoHelper()
	cmd := exec.CommandContext(ctx, "bash", append([]string{script}, args...)...)
	cmd.Env = env
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

var _ = Describe("builder-rooket.sh, which stands in for rooket in a builder", func() {
	It("runs kubectl with KUBECONFIG at the kubeconfig of ROOKET_NAME's cluster, "+
		"as rooket kubectl does, and prints nothing of its own", func(ctx SpecContext) {
		home, bin := builderHome("rgw-go-squid")
		for _, verb := range []string{"kubectl", "k"} {
			stdout, stderr, err := runScript(ctx, []string{
				"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + home, "ROOKET_NAME=rgw-go-squid",
			}, "builder-rooket.sh", verb, "-n", "rook-ceph", "get", "pods")
			Expect(err).NotTo(HaveOccurred(), stderr)
			Expect(stdout).To(Equal("KUBECONFIG=" + filepath.Join(home, ".local/share/rooket/rgw-go-squid/kubeconfig") +
				"\n-n\nrook-ceph\nget\npods\n"))
		}
	})

	DescribeTable("refuses what it cannot do as rooket would, on stderr alone",
		func(ctx SpecContext, name string, args []string, status int, want string) {
			home, bin := builderHome("rgw-go-squid")
			stdout, stderr, err := runScript(ctx, []string{
				"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + home, "ROOKET_NAME=" + name,
			}, "builder-rooket.sh", args...)
			var exitErr *exec.ExitError
			Expect(errors.As(err, &exitErr)).To(BeTrue(), "%v", err)
			Expect(exitErr.ExitCode()).To(Equal(status))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(ContainSubstring(want))
		},
		Entry("a command other than kubectl", "rgw-go-squid", []string{"up"}, 2, "only rooket kubectl runs in a builder"),
		Entry("no cluster selected", "", []string{"kubectl", "get", "pods"}, 1, "no cluster selected"),
		Entry("a cluster with no kubeconfig", "rgw-go-tentacle", []string{"kubectl", "get", "pods"}, 1,
			`no kubeconfig for cluster "rgw-go-tentacle"`),
	)
})

var _ = Describe("in-builder.sh", func() {
	It("mounts the stand-in as rooket, the host's kubectl, and the cluster's rooket state, on the host's network",
		func(ctx SpecContext) {
			home, bin := builderHome("rgw-go-squid")
			dir := GinkgoT().TempDir()
			engine, log := filepath.Join(dir, "engine"), filepath.Join(dir, "log")
			Expect(os.WriteFile(engine, []byte(startEngine), 0o700)).To(Succeed())
			here, err := filepath.Abs(".")
			Expect(err).NotTo(HaveOccurred())
			cmd := exec.CommandContext(ctx, "bash", "in-builder.sh", "squid", "make", "gate")
			cmd.Dir = here
			cmd.Env = []string{
				"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + dir,
				"GOMODCACHE=" + filepath.Join(dir, "mod"), "RGW_GO_ENGINE=" + engine, "FAKE_LOG=" + log,
				"ROOKET=/opt/rooket/bin/rooket",
			}
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))
			data, err := os.ReadFile(log)
			Expect(err).NotTo(HaveOccurred())
			var run []string
			for call := range strings.SplitSeq(strings.TrimSuffix(string(data), "--end--\n"), "--end--\n") {
				if lines := strings.Split(strings.TrimSuffix(call, "\n"), "\n"); lines[0] == "run" {
					run = lines
				}
			}
			Expect(run).To(ContainElements(
				"--network=host",
				here+"/builder-rooket.sh:/usr/local/bin/rooket:ro",
				here+"/builder-rooket.sh:/opt/rooket/bin/rooket:ro",
				filepath.Join(bin, "kubectl")+":/usr/local/bin/kubectl:ro",
				filepath.Join(home, ".local/share/rooket/rgw-go-squid")+":/tmp/.local/share/rooket/rgw-go-squid:ro",
			))
			Expect(slices.Index(run, "make")).To(Equal(len(run)-2), "the command after the image")
		})
})
