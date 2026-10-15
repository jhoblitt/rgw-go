package rooket_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Lines of rgw-go's log as rgw-go-up.sh starts it under GODEBUG=gctrace=1, as
// the gateway bench does: JSON records, main's closing error, and GC lines,
// which either can tear.
const (
	serving = `{"time":"2026-10-15T03:32:50.842465964-07:00","level":"INFO","msg":"serving","addrs":["127.0.0.1:7481"],` +
		`"metrics_addr":"127.0.0.1:9481","zone":"ceph-objectstore","release":"squid"}`
	request = `{"time":"2026-10-15T03:57:30.583966873-07:00","level":"WARN","msg":"bucket listing failed after the response started",` +
		`"error":"write tcp4 127.0.0.1:7481->127.0.0.1:35898: write: broken pipe"}`
	// quoting is a record whose text holds what main's error starts with.
	quoting = `{"time":"2026-10-15T03:57:31.000000000-07:00","level":"WARN","msg":"request failed","key":"rgw-go: a key"}`
	exitErr = "rgw-go: serve: listen tcp 127.0.0.1:7481: bind: address already in use"
	wholeGC = "gc 2 @0.020s 2%: 0.052+0.23+0.057 ms clock, 1.6+0.033/0.89/0.018+1.8 ms cpu, 3->4->1 MB, 4 MB goal, " +
		"0 MB stacks, 0 MB globals, 32 P"
	// gcStart and gcRest are a GC line torn in two, the rest starting the next
	// line; gcMid and gcEnd are that rest torn again.
	gcStart = "gc 3 @0.031s 2"
	gcRest  = "%: 0.086+0.61+0.039 ms clock, 2.7+1.6/2.7/0+1.2 ms cpu, 10->10->4 MB, 11 MB goal, 0 MB stacks, 0 MB globals, 32 P"
	gcMid   = "%: 0.086+0.61+0.039 ms clock, 2.7+1.6/2.7/0+1.2 ms cpu, 10->10->"
	gcEnd   = "4 MB, 11 MB goal, 0 MB stacks, 0 MB globals, 32 P"
)

// writeLog writes lines, each ended by a newline, to rgw-go.log in dir, a
// directory of the spec's own when it is empty, and returns its path.
func writeLog(dir string, lines ...string) string {
	GinkgoHelper()
	if dir == "" {
		dir = GinkgoT().TempDir()
	}
	path := filepath.Join(dir, "rgw-go.log")
	Expect(os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)).To(Succeed())
	return path
}

// libFunc runs lib.sh's function fn on the log at path, sourced under
// errexit and pipefail as the scripts source it, and returns what it printed.
func libFunc(ctx context.Context, fn, path string) string {
	GinkgoHelper()
	lib, err := filepath.Abs("lib.sh")
	Expect(err).NotTo(HaveOccurred())
	cmd := exec.CommandContext(ctx, "bash", "-c", `set -euo pipefail; script=spec; source "$1"; "$2" "$3"`, "bash", lib, fn, path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "%s: %s", fn, stderr.String())
	return string(out)
}

// downTree copies rgw-go-down.sh and lib.sh into a directory of the spec's
// own, writes lines as squid's rgw-go.log there, and starts a process named
// rgw-go, whose pid it writes where rgw-go-up.sh would. It returns the
// script's path, the pid, and the channel that receives the process's end,
// which the spec's end brings at the latest.
func downTree(ctx context.Context, lines ...string) (script string, pid int, exited <-chan error) {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	for _, name := range []string{"rgw-go-down.sh", "lib.sh"} {
		data, err := os.ReadFile(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(root, name), data, 0o700)).To(Succeed())
	}
	out := filepath.Join(root, "out", "squid")
	Expect(os.MkdirAll(out, 0o750)).To(Succeed())
	writeLog(out, lines...)
	// bash renames itself, since rgw-go-down.sh signals only a process named
	// rgw-go; TERM ends it.
	fake := exec.CommandContext(ctx, "bash", "-c", `printf %s rgw-go >"/proc/$$/comm" && while :; do sleep 1; done`)
	Expect(fake.Start()).To(Succeed())
	done := make(chan error, 1)
	go func() { done <- fake.Wait() }()
	pid = fake.Process.Pid
	Eventually(os.ReadFile).WithArguments(fmt.Sprintf("/proc/%d/comm", pid)).
		WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Equal([]byte("rgw-go\n")))
	Expect(os.WriteFile(filepath.Join(out, "rgw-go.pid"), []byte(strconv.Itoa(pid)+"\n"), 0o600)).To(Succeed())
	return filepath.Join(root, "rgw-go-down.sh"), pid, done
}

// runDown runs rgw-go-down.sh for squid.
func runDown(ctx context.Context, script string) (ok bool, stdout, stderr string) {
	cmd := exec.CommandContext(ctx, "bash", script, "squid")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	return cmd.Run() == nil, out.String(), errOut.String()
}

var _ = Describe("rgw-go's log, which GC lines share under GODEBUG=gctrace=1", func() {
	DescribeTable("serving_records, which rgw-go-up.sh reads the metrics address from, finds the serving record",
		func(ctx SpecContext, lines []string) {
			Expect(libFunc(ctx, "serving_records", writeLog("", lines...))).To(Equal(serving + "\n"))
		},
		Entry("on a line of its own", []string{wholeGC, serving, request}),
		Entry("after a GC line's first pieces, whose rest starts the next line", []string{gcStart + serving, gcRest}),
		Entry("after the rest of a torn GC line", []string{gcStart + request, gcMid + serving, gcEnd}),
	)

	It("serving_records prints nothing for a log without the serving record", func(ctx SpecContext) {
		Expect(libFunc(ctx, "serving_records", writeLog("", wholeGC, gcStart+request, gcRest))).To(BeEmpty())
	})

	It("is what rgw-go-up.sh reads the serving record with", func() {
		data, err := os.ReadFile("rgw-go-up.sh")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`line=$(serving_records "${log}")`))
	})

	DescribeTable("exit_error finds the error main ended the run with",
		func(ctx SpecContext, lines []string) {
			Expect(libFunc(ctx, "exit_error", writeLog("", lines...))).To(Equal(exitErr + "\n"))
		},
		Entry("on the log's last line", []string{serving, wholeGC, exitErr}),
		Entry("after a GC line's first pieces, whose rest is the log's last line", []string{serving, gcStart + exitErr, gcRest}),
		Entry("before whole GC lines", []string{serving, exitErr, wholeGC}),
	)

	It("exit_error prints nothing for a run that ended without one, though a record quotes its start", func(ctx SpecContext) {
		Expect(libFunc(ctx, "exit_error", writeLog("", serving, quoting, gcStart+request, gcRest))).To(BeEmpty())
	})

	Context("rgw-go-down.sh", func() {
		It("fails with main's error behind GC output, though GC lines follow it", func(ctx SpecContext) {
			script, pid, exited := downTree(ctx, serving, gcStart+exitErr, gcRest, wholeGC)
			ok, stdout, stderr := runDown(ctx, script)
			Expect(ok).To(BeFalse(), stdout)
			Expect(stderr).To(Equal(fmt.Sprintf("rgw-go-down.sh: rgw-go (pid %d) stopped with an error: %s\n", pid, exitErr)))
			Eventually(exited).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).
				Should(Receive(MatchError("signal: terminated")))
		})

		It("reports a stop without an error among torn GC lines and a record quoting main's error's start", func(ctx SpecContext) {
			script, pid, exited := downTree(ctx, serving, quoting, gcStart+request, gcRest)
			ok, stdout, stderr := runDown(ctx, script)
			Expect(ok).To(BeTrue(), stderr)
			Expect(stdout).To(Equal(fmt.Sprintf("rgw-go (pid %d) stopped\n", pid)))
			Eventually(exited).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).
				Should(Receive(MatchError("signal: terminated")))
		})
	})
})
