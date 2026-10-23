package harness_test

import (
	"bytes"
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

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
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

	sh := func(script string) harness.Cmd {
		return harness.Cmd{Name: "/bin/sh", Args: []string{"-c", script}, Env: []string{"CHILD=" + pidFile}}
	}

	It("returns a command's output", func(ctx SpecContext) {
		out, err := harness.NewExecer(10*time.Second).Output(ctx, sh(`echo "$CHILD"`))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal(pidFile + "\n"))
	})

	It("passes the command its stdin", func(ctx SpecContext) {
		c := sh("cat")
		c.Stdin = []byte("seam-w0-0\nseam-w1-1\n")
		out, err := harness.NewExecer(10*time.Second).Output(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(Equal("seam-w0-0\nseam-w1-1\n"))
	})

	It("streams a command's output and errors to Out as it runs, returning none", func(ctx SpecContext) {
		var out bytes.Buffer
		c := sh(`echo out; echo err >&2; echo "$CHILD"`)
		c.Out = &out
		got, err := harness.NewExecer(10*time.Second).Output(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeNil())
		Expect(out.String()).To(Equal("out\nerr\n" + pidFile + "\n"))
	})

	It("names a streaming command when it fails, its errors left in Out", func(ctx SpecContext) {
		var out bytes.Buffer
		c := sh("echo no such bucket >&2; exit 3")
		c.Out = &out
		_, err := harness.NewExecer(10*time.Second).Output(ctx, c)
		Expect(err).To(MatchError("/bin/sh -c echo no such bucket >&2; exit 3: exit status 3"))
		Expect(out.String()).To(Equal("no such bucket\n"))
	})

	It("kills a streaming command's whole process group at its deadline", func(ctx SpecContext) {
		deadline, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		c := sh(`sleep 30 & echo $! > "$CHILD"; wait; true`)
		c.Out = &bytes.Buffer{}
		start := time.Now()
		_, err := harness.NewExecer(10*time.Second).Output(deadline, c)
		elapsed := time.Since(start)
		Expect(err).To(MatchError(ContainSubstring("signal: killed")))
		Expect(elapsed).To(BeNumerically("<", 3*time.Second), "returned after %s, not after the child's 30 s or the 10 s wait delay", elapsed)
		pid := childPid(pidFile)
		Eventually(func() string { return processState(pid) }).WithTimeout(5*time.Second).WithPolling(20*time.Millisecond).
			Should(Or(Equal("gone"), Equal("Z")), "the child %d died with its group", pid)
	})

	It("names the command and its stderr when it fails", func(ctx SpecContext) {
		_, err := harness.NewExecer(10*time.Second).Output(ctx, sh("echo no such pool >&2; exit 2"))
		Expect(err).To(MatchError(`/bin/sh -c echo no such pool >&2; exit 2: exit status 2: no such pool`))
	})

	It("kills the command's whole process group at its deadline, so a child holding its output cannot keep it waiting", func(ctx SpecContext) {
		deadline, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := harness.NewExecer(10*time.Second).Output(deadline, sh(`sleep 30 & echo $! > "$CHILD"; wait; true`))
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
		_, err := harness.NewExecer(300*time.Millisecond).Output(ctx, sh(`sleep 30 & echo $! > "$CHILD"`))
		elapsed := time.Since(start)
		pid := childPid(pidFile)
		DeferCleanup(syscall.Kill, pid, syscall.SIGKILL)
		Expect(err).To(MatchError(exec.ErrWaitDelay))
		Expect(elapsed).To(BeNumerically("<", 3*time.Second), "returned after %s, not after the leftover's 30 s", elapsed)
	})
})
