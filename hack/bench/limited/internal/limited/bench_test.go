package limited_test

import (
	"bytes"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/limited/internal/limited"
)

// The watchdog runs /bin/sh, never a benchmark, so these specs stay
// hermetic.
var _ = Describe("Watchdog", func() {
	var out *bytes.Buffer

	BeforeEach(func() {
		out = &bytes.Buffer{}
	})

	sh := func(script string, limit, grace time.Duration) limited.Process {
		return limited.Process{
			Path: "/bin/sh", Args: []string{"-c", script}, Env: []string{"LIMITED_SPEC=seen"},
			Out: out, Limit: limit, Grace: grace,
		}
	}

	It("runs a process to its end with its environment, and returns its pid", func(ctx SpecContext) {
		pid, err := limited.Watchdog.Bench(ctx, sh(`echo "$LIMITED_SPEC"; echo err >&2`, 10*time.Second, time.Second))
		Expect(err).NotTo(HaveOccurred())
		Expect(pid).To(BeNumerically(">", 0))
		Expect(out.String()).To(Equal("seen\nerr\n"))
	})

	It("returns a failed process's exit, which is no stop", func(ctx SpecContext) {
		pid, err := limited.Watchdog.Bench(ctx, sh("exit 3", 10*time.Second, time.Second))
		Expect(pid).To(BeNumerically(">", 0))
		Expect(err).To(And(BeAssignableToTypeOf(&exec.ExitError{}), HaveField("ExitCode()", 3)))
		Expect(err).NotTo(MatchError(limited.ErrStopped))
	})

	It("stops a process past its limit with SIGTERM", func(ctx SpecContext) {
		start := time.Now()
		_, err := limited.Watchdog.Bench(ctx, sh("exec sleep 30", 200*time.Millisecond, 10*time.Second))
		Expect(err).To(MatchError(limited.ErrStopped))
		Expect(err).To(MatchError("stopped past its limit of 200ms"))
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second), "SIGTERM ends sleep at once, well inside the grace")
	})

	It("kills a process that ignores SIGTERM once the grace has passed", func(ctx SpecContext) {
		start := time.Now()
		_, err := limited.Watchdog.Bench(ctx, sh(`trap "" TERM; exec sleep 30`, 200*time.Millisecond, 300*time.Millisecond))
		Expect(err).To(MatchError(limited.ErrStopped))
		elapsed := time.Since(start)
		Expect(elapsed).To(BeNumerically(">=", 500*time.Millisecond), "it waits out the grace after SIGTERM")
		Expect(elapsed).To(BeNumerically("<", 10*time.Second), "SIGKILL ends it long before sleep would")
	})

	It("reports a binary that cannot start with no pid", func(ctx SpecContext) {
		pid, err := limited.Watchdog.Bench(ctx, limited.Process{Path: "/nonexistent/seam.test", Out: out, Limit: time.Second})
		Expect(pid).To(BeZero())
		Expect(err).To(MatchError(ContainSubstring("starting /nonexistent/seam.test")))
	})
})
