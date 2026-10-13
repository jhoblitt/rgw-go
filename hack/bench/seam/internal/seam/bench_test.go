package seam_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
)

// signalOf is the signal that ended the process err reports, or 0.
func signalOf(err error) syscall.Signal {
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return 0
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0
	}
	return ws.Signal()
}

// The watchdog runs /bin/sh, never a benchmark or a container engine, so
// these specs stay hermetic.
var _ = Describe("Watchdog", func() {
	var out *bytes.Buffer

	BeforeEach(func() {
		out = &bytes.Buffer{}
	})

	sh := func(script string, limit, grace time.Duration) seam.Process {
		return seam.Process{
			Path: "/bin/sh", Args: []string{"-c", script}, Env: []string{"SEAM_SPEC=seen"},
			Out: out, Limit: limit, Grace: grace,
		}
	}

	It("runs a process to its end with its environment, and returns its pid", func(ctx SpecContext) {
		pid, err := seam.Watchdog.Bench(ctx, sh(`echo "$SEAM_SPEC"; echo err >&2`, 10*time.Second, time.Second))
		Expect(err).NotTo(HaveOccurred())
		Expect(pid).To(BeNumerically(">", 0))
		Expect(out.String()).To(Equal("seen\nerr\n"))
	})

	It("returns a failed process's exit, which is no stop", func(ctx SpecContext) {
		pid, err := seam.Watchdog.Bench(ctx, sh("exit 3", 10*time.Second, time.Second))
		Expect(pid).To(BeNumerically(">", 0))
		Expect(err).To(And(BeAssignableToTypeOf(&exec.ExitError{}), HaveField("ExitCode()", 3)))
		Expect(err).NotTo(MatchError(seam.ErrStopped))
	})

	It("stops a process past its limit with SIGTERM", func(ctx SpecContext) {
		start := time.Now()
		_, err := seam.Watchdog.Bench(ctx, sh("exec sleep 30", 200*time.Millisecond, 10*time.Second))
		Expect(err).To(MatchError(seam.ErrStopped))
		Expect(err).To(MatchError("stopped past its limit of 200ms: signal: terminated"))
		Expect(signalOf(err)).To(Equal(syscall.SIGTERM), "the status the run exits with")
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second), "SIGTERM ends sleep at once, well inside the grace")
	})

	It("kills a process that ignores SIGTERM once the grace has passed", func(ctx SpecContext) {
		start := time.Now()
		_, err := seam.Watchdog.Bench(ctx, sh(`trap "" TERM; exec sleep 30`, 200*time.Millisecond, 300*time.Millisecond))
		Expect(err).To(MatchError(seam.ErrStopped))
		Expect(signalOf(err)).To(Equal(syscall.SIGKILL), "the status the run exits with")
		elapsed := time.Since(start)
		Expect(elapsed).To(BeNumerically(">=", 500*time.Millisecond), "it waits out the grace after SIGTERM")
		Expect(elapsed).To(BeNumerically("<", 10*time.Second), "SIGKILL ends it long before sleep would")
	})

	It("reports a binary that cannot start with no pid", func(ctx SpecContext) {
		pid, err := seam.Watchdog.Bench(ctx, seam.Process{Path: "/nonexistent/seam.test", Out: out, Limit: time.Second})
		Expect(pid).To(BeZero())
		Expect(err).To(MatchError(ContainSubstring("starting /nonexistent/seam.test")))
	})

	Context("running a floor run's container", func() {
		var dir, engine string

		BeforeEach(func() {
			dir = GinkgoT().TempDir()
			// The fake engine's run is the container: it records its pid
			// under the container's name and runs as SEAM_RUN says, ignoring
			// SIGTERM as rados does as PID 1. Its kill records the name and,
			// unless SEAM_KILL is stuck, kills the run by its pid.
			engine = filepath.Join(dir, "engine")
			Expect(os.WriteFile(engine, []byte(`#!/bin/sh
dir=$(dirname "$0")
case "$1" in
run)
	echo $$ >"$dir/$4.pid"
	shift 4
	echo "args: $*"
	case "$SEAM_RUN" in
	fail) exit 3 ;;
	hang) trap "" TERM; exec sleep 30 ;;
	esac
	;;
kill)
	echo "$2" >>"$dir/killed"
	[ "$SEAM_KILL" = stuck ] || kill -KILL "$(cat "$dir/$2.pid")"
	;;
esac
`), 0o700)).To(Succeed()) //nolint:gosec // the fixture is a script the spec runs
		})

		container := func(limit, grace time.Duration) seam.Container {
			return seam.Container{Engine: engine, Name: "rgw-go-bench-floor-1-1", Args: []string{"image", "rados", "bench"}, Out: out, Limit: limit, Grace: grace}
		}
		killed := func() string {
			data, err := os.ReadFile(filepath.Join(dir, "killed"))
			if errors.Is(err, os.ErrNotExist) {
				return ""
			}
			Expect(err).NotTo(HaveOccurred())
			return string(data)
		}

		It("runs the container to its end, named, and kills nothing", func(ctx SpecContext) {
			GinkgoT().Setenv("SEAM_RUN", "")
			Expect(seam.Watchdog.Floor(ctx, container(10*time.Second, time.Second))).To(Succeed())
			Expect(out.String()).To(Equal("args: image rados bench\n"))
			Expect(filepath.Join(dir, "rgw-go-bench-floor-1-1.pid")).To(BeARegularFile())
			Expect(killed()).To(BeEmpty())
		})

		It("kills a failed run's container by name, should its client have left it running", func(ctx SpecContext) {
			GinkgoT().Setenv("SEAM_RUN", "fail")
			err := seam.Watchdog.Floor(ctx, container(10*time.Second, time.Second))
			Expect(err).To(And(BeAssignableToTypeOf(&exec.ExitError{}), HaveField("ExitCode()", 3)))
			Expect(killed()).To(Equal("rgw-go-bench-floor-1-1\n"))
		})

		It("kills the container by name past its limit", func(ctx SpecContext) {
			GinkgoT().Setenv("SEAM_RUN", "hang")
			start := time.Now()
			err := seam.Watchdog.Floor(ctx, container(200*time.Millisecond, 10*time.Second))
			Expect(err).To(MatchError(seam.ErrStopped))
			Expect(err).To(MatchError("stopped past its limit of 200ms"))
			Expect(killed()).To(Equal("rgw-go-bench-floor-1-1\n"))
			Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second), "the kill ends the run, well inside the grace")
		})

		It("SIGKILLs a client that outlives its container's kill once the grace has passed", func(ctx SpecContext) {
			GinkgoT().Setenv("SEAM_RUN", "hang")
			GinkgoT().Setenv("SEAM_KILL", "stuck")
			start := time.Now()
			err := seam.Watchdog.Floor(ctx, container(200*time.Millisecond, 300*time.Millisecond))
			Expect(err).To(MatchError(seam.ErrStopped))
			Expect(killed()).To(Equal("rgw-go-bench-floor-1-1\n"))
			elapsed := time.Since(start)
			Expect(elapsed).To(BeNumerically(">=", 500*time.Millisecond), "it waits out the grace after the kill")
			Expect(elapsed).To(BeNumerically("<", 10*time.Second), "SIGKILL ends the client long before sleep would")
		})

		It("reports an engine that cannot start", func(ctx SpecContext) {
			c := container(time.Second, time.Second)
			c.Engine = "/nonexistent/podman"
			Expect(seam.Watchdog.Floor(ctx, c)).To(MatchError(ContainSubstring("starting /nonexistent/podman")))
		})
	})
})
