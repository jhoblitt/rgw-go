package rooket_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// startEngine stands in for podman or docker: it records each call's
// arguments, one a line and then a line "--end--", in $FAKE_LOG, answers info
// as a rootful engine, a detached run with a container id, and inspect with
// $FAKE_PID.
const startEngine = `#!/usr/bin/env bash
{ printf '%s\n' "$@"; echo --end--; } >>"${FAKE_LOG}"
case "$1" in
info) echo false ;;
run) echo c0ffee ;;
inspect) echo "${FAKE_PID}" ;;
esac
`

// runStart runs rgw-go-start.sh for squid with LOG and rgw-go's arguments,
// under nothing of the spec process's environment but PATH and HOME and the
// variables env sets, against the fake engine, and returns its stdout and
// stderr and the engine's run call.
func runStart(ctx context.Context, env []string, args ...string) (stdout, stderr string, run []string, err error) {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	engine, log := filepath.Join(dir, "engine"), filepath.Join(dir, "log")
	Expect(os.WriteFile(engine, []byte(startEngine), 0o700)).To(Succeed())
	cmd := exec.CommandContext(ctx, "bash", append([]string{"rgw-go-start.sh", "squid"}, args...)...)
	cmd.Env = slices.Concat([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"RGW_GO_ENGINE=" + engine, "FAKE_LOG=" + log,
	}, env)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	data, readErr := os.ReadFile(log)
	Expect(readErr).NotTo(HaveOccurred())
	for call := range strings.SplitSeq(strings.TrimSuffix(string(data), "--end--\n"), "--end--\n") {
		if lines := strings.Split(strings.TrimSuffix(call, "\n"), "\n"); lines[0] == "run" {
			run = lines
		}
	}
	return out.String(), errOut.String(), run, err
}

var _ = Describe("rgw-go-start.sh, which starts the rgw-go rgw-go-up.sh runs", func() {
	const image = "ghcr.io/jhoblitt/rgw-go:local-v19.2.7"
	var repo string

	BeforeEach(func() {
		var err error
		repo, err = filepath.Abs(filepath.Join("..", ".."))
		Expect(err).NotTo(HaveOccurred())
	})

	It("starts RGW_GO_IMAGE's rgw-go detached on the host's network as the caller, "+
		"with its log, its arguments and the caller's RGW_GO_* and GODEBUG, and prints its host pid", func(ctx SpecContext) {
		log := filepath.Join(repo, "hack", "rooket", "out", "squid", "rgw-go.log")
		stdout, stderr, run, err := runStart(ctx, []string{
			"RGW_GO_IMAGE=" + image, "RGW_GO_PORT=7481", "GODEBUG=gctrace=1", "FAKE_PID=4242", "UNRELATED=1",
		}, log, "serve", "--metrics-addr=127.0.0.1:9481", "--", "--id", "rgw.rgw-go")
		Expect(err).NotTo(HaveOccurred(), stderr)
		Expect(stdout).To(Equal("4242\n"))

		Expect(run[:11]).To(Equal([]string{
			"run", "-d", "--rm", "--pull=never", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
			"--security-opt", "label=disable", "--network=host", "-v", repo + ":" + repo,
		}))
		i := slices.Index(run, "--entrypoint")
		Expect(i).To(BeNumerically(">", 11))
		var passed []string
		for j := 11; j < i; j += 2 {
			Expect(run[j]).To(Equal("-e"))
			passed = append(passed, run[j+1])
		}
		Expect(passed).To(ConsistOf("RGW_GO_ENGINE", "RGW_GO_IMAGE", "RGW_GO_PORT", "RGW_GO_LOG_FORMAT", "GODEBUG"),
			"each passed by name, so the container takes the caller's value, RGW_GO_LOG_FORMAT the script's json")
		Expect(run[i:]).To(Equal([]string{
			"--entrypoint", "/bin/sh", image, "-c", `exec /usr/bin/rgw-go "$@" >"$0" 2>&1`, log,
			"serve", "--metrics-addr=127.0.0.1:9481", "--", "--id", "rgw.rgw-go",
		}))
	})

	It("refuses a container that ended before its pid was read, rather than print pid 0, "+
		"which kill would take for the caller's process group", func(ctx SpecContext) {
		stdout, stderr, _, err := runStart(ctx, []string{"RGW_GO_IMAGE=" + image, "FAKE_PID=0"}, "/x/rgw-go.log", "serve")
		Expect(err).To(HaveOccurred())
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(Equal("rgw-go-start.sh: rgw-go's container c0ffee ended before it started; see /x/rgw-go.log\n"))
	})

	It("without RGW_GO_IMAGE builds bin/rgw-go on the host and starts it under nohup with its log as its output, "+
		"on lines the script runs", func() {
		data, err := os.ReadFile("rgw-go-start.sh")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(MatchRegexp(
			`(?m)^\tnohup "\$\{repo\}/bin/rgw-go" "\$@" >"\$\{log\}" 2>&1 &\n\techo "\$!"$`))
	})
})
