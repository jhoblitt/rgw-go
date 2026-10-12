package limited

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ErrStopped reports a benchmark process the watchdog stopped past its
// limit.
var ErrStopped = errors.New("stopped past its limit")

// Process is one benchmark process: the binary, its arguments, the
// environment it takes beyond this process's, where its output goes, and the
// watchdog's limit and grace.
type Process struct {
	Path  string
	Args  []string
	Env   []string
	Out   io.Writer
	Limit time.Duration
	Grace time.Duration
}

// Launcher runs a benchmark process to its end and returns its pid, zero when
// it never started.
//
//counterfeiter:generate . Launcher
type Launcher interface {
	Bench(ctx context.Context, p Process) (int, error)
}

// watchdog runs benchmark processes under a limit. Nothing else bounds a call
// stuck in librados: go test's -timeout does not apply to benchmarks
// (testing.(*M).Run stops its alarm before it runs them), and a sync-mode
// call returns only when librados does. Past the limit the process gets
// SIGTERM, and SIGKILL once the grace has passed too.
type watchdog struct{}

func (watchdog) Bench(ctx context.Context, p Process) (int, error) {
	limited, cancel := context.WithTimeout(ctx, p.Limit)
	defer cancel()
	cmd := exec.CommandContext(limited, p.Path, p.Args...) //nolint:gosec // the binary is the benchmark this command built
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stdout, cmd.Stderr = p.Out, p.Out
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = p.Grace
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting %s: %w", p.Path, err)
	}
	pid := cmd.Process.Pid
	err := cmd.Wait()
	if ctx.Err() == nil && errors.Is(limited.Err(), context.DeadlineExceeded) {
		return pid, fmt.Errorf("%w of %s", ErrStopped, p.Limit)
	}
	return pid, err
}
