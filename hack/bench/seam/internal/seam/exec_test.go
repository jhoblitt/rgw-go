package seam_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
)

// processState is a process's state letter from /proc, or "gone".
func processState(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "gone"
	}
	_, after, ok := strings.Cut(string(data), ") ")
	if !ok || after == "" {
		return "gone"
	}
	return after[:1]
}

// childPid reads the pid a spec's /bin/sh wrote, waiting for it to appear.
func childPid(path string) int {
	var pid int
	Eventually(func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err
	}).WithTimeout(5 * time.Second).WithPolling(20 * time.Millisecond).Should(Succeed())
	return pid
}

// The real execer runs /bin/sh, never a cluster command, so these specs stay
// hermetic. Each shell forks a long-lived child that shares its output, as
// rooket forks kubectl.
var _ = Describe("Execer", func() {
	var pidFile string

	BeforeEach(func() {
		pidFile = filepath.Join(GinkgoT().TempDir(), "child")
	})

	sh := func(script string) seam.Cmd {
		return seam.Cmd{Name: "/bin/sh", Args: []string{"-c", script}, Env: []string{"CHILD=" + pidFile}}
	}

	It("returns a command's output", func(ctx SpecContext) {
		out, err := seam.NewExecer(10*time.Second).Output(ctx, sh(`echo "$CHILD"`))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(pidFile + "\n"))
	})

	It("names the command and its stderr when it fails", func(ctx SpecContext) {
		_, err := seam.NewExecer(10*time.Second).Output(ctx, sh("echo no such pool >&2; exit 2"))
		Expect(err).To(MatchError(`/bin/sh -c echo no such pool >&2; exit 2: exit status 2: no such pool`))
	})

	It("kills the command's whole process group at its deadline, so a child holding its output cannot keep it waiting", func(ctx SpecContext) {
		deadline, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := seam.NewExecer(10*time.Second).Output(deadline, sh(`sleep 30 & echo $! > "$CHILD"; wait; true`))
		elapsed := time.Since(start)
		Expect(err).To(MatchError(ContainSubstring("signal: killed")))
		Expect(elapsed).To(BeNumerically("<", 3*time.Second), "returned after %s, not after the child's 30 s or the 10 s wait delay", elapsed)
		GinkgoWriter.Printf("a 300ms deadline returned after %s\n", elapsed)
		pid := childPid(pidFile)
		Eventually(func() string { return processState(pid) }).WithTimeout(5*time.Second).WithPolling(20*time.Millisecond).
			Should(Or(Equal("gone"), Equal("Z")), "the child %d died with its group", pid)
	})

	It("returns once the command exits, though a process it left behind keeps its output open", func(ctx SpecContext) {
		start := time.Now()
		_, err := seam.NewExecer(300*time.Millisecond).Output(ctx, sh(`sleep 30 & echo $! > "$CHILD"`))
		elapsed := time.Since(start)
		pid := childPid(pidFile)
		DeferCleanup(syscall.Kill, pid, syscall.SIGKILL)
		Expect(err).To(MatchError(exec.ErrWaitDelay))
		Expect(elapsed).To(BeNumerically("<", 3*time.Second), "returned after %s, not after the leftover's 30 s", elapsed)
	})
})
