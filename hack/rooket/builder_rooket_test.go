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

// inBuilder runs in-builder.sh for squid's make gate against the fake engine,
// with ROOKET set and env added to its environment, and returns the engine's
// run call, the script's directory, and the HOME and bin directory
// builderHome made.
func inBuilder(ctx context.Context, env ...string) (run []string, here, home, bin string) {
	GinkgoHelper()
	home, bin = builderHome("rgw-go-squid")
	dir := GinkgoT().TempDir()
	engine, log := filepath.Join(dir, "engine"), filepath.Join(dir, "log")
	Expect(os.WriteFile(engine, []byte(startEngine), 0o700)).To(Succeed())
	here, err := filepath.Abs(".")
	Expect(err).NotTo(HaveOccurred())
	cmd := exec.CommandContext(ctx, "bash", "in-builder.sh", "squid", "make", "gate")
	cmd.Dir = here
	cmd.Env = slices.Concat([]string{
		"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + dir,
		"GOMODCACHE=" + filepath.Join(dir, "mod"), "RGW_GO_ENGINE=" + engine, "FAKE_LOG=" + log,
		"ROOKET=/opt/rooket/bin/rooket",
	}, env)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	data, err := os.ReadFile(log)
	Expect(err).NotTo(HaveOccurred())
	for call := range strings.SplitSeq(strings.TrimSuffix(string(data), "--end--\n"), "--end--\n") {
		if lines := strings.Split(strings.TrimSuffix(call, "\n"), "\n"); lines[0] == "run" {
			run = lines
		}
	}
	return run, here, home, bin
}

// passed are the variables a run call passes by name, each an -e NAME pair.
func passed(run []string) []string {
	var names []string
	for i, a := range run[:len(run)-1] {
		if a == "-e" && !strings.Contains(run[i+1], "=") {
			names = append(names, run[i+1])
		}
	}
	return names
}

var _ = Describe("in-builder.sh", func() {
	It("mounts the stand-in as rooket, the host's kubectl, and the cluster's rooket state, on the host's network",
		func(ctx SpecContext) {
			run, here, home, bin := inBuilder(ctx)
			Expect(run).To(ContainElements(
				"--network=host",
				here+"/builder-rooket.sh:/usr/local/bin/rooket:ro",
				here+"/builder-rooket.sh:/opt/rooket/bin/rooket:ro",
				filepath.Join(bin, "kubectl")+":/usr/local/bin/kubectl:ro",
				filepath.Join(home, ".local/share/rooket/rgw-go-squid")+":/tmp/.local/share/rooket/rgw-go-squid:ro",
			))
			Expect(slices.Index(run, "make")).To(Equal(len(run)-2), "the command after the image")
		})

	DescribeTable("passes the Makefile's RGW_GO_SERVICE_MAP into the builder by name when it is set",
		func(ctx SpecContext, env, want []string) {
			run, _, _, _ := inBuilder(ctx, env...)
			Expect(passed(run)).To(Equal(want))
		},
		Entry("set false, as the Makefile exports it", []string{"RGW_GO_SERVICE_MAP=false"},
			[]string{"ROOKET_NAME", "RGW_GO_SERVICE_MAP"}),
		Entry("unset, as outside the Makefile's targets", []string(nil), []string{"ROOKET_NAME"}),
	)
})
