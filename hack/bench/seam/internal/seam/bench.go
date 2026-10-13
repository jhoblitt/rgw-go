package seam

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

// ErrStopped reports a benchmark process or a floor run the watchdog stopped
// past its limit.
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

// Container is one floor run: the engine runs rados in a container named
// Name, with Args after run --rm --name Name, its output going to Out, under
// the watchdog's limit and grace.
type Container struct {
	Engine string
	Name   string
	Args   []string
	Out    io.Writer
	Limit  time.Duration
	Grace  time.Duration
}

// Launcher runs the sweep's processes to their end under the watchdog: Bench
// a benchmark process, returning its pid, zero when it never started; Floor a
// floor run's container.
//
//counterfeiter:generate . Launcher
type Launcher interface {
	Bench(ctx context.Context, p Process) (int, error)
	Floor(ctx context.Context, c Container) error
}

// watchdog runs benchmark processes and floor runs under a limit. Nothing
// else bounds a call stuck in librados: go test's -timeout does not apply to
// benchmarks (testing.(*M).Run stops its alarm before it runs them), and a
// sync-mode call returns only when librados does.
type watchdog struct{}

// Bench runs a benchmark process. Past the limit it gets SIGTERM, and SIGKILL
// once the grace has passed too.
func (watchdog) Bench(ctx context.Context, p Process) (int, error) {
	bounded, cancel := context.WithTimeout(ctx, p.Limit)
	defer cancel()
	cmd := exec.CommandContext(bounded, p.Path, p.Args...) //nolint:gosec // the binary is the benchmark this command built
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stdout, cmd.Stderr = p.Out, p.Out
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = p.Grace
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting %s: %w", p.Path, err)
	}
	pid := cmd.Process.Pid
	err := cmd.Wait()
	if err != nil && ctx.Err() == nil && errors.Is(bounded.Err(), context.DeadlineExceeded) {
		// The wait's error says how the process ended, SIGTERM or SIGKILL,
		// and so its exit status.
		return pid, fmt.Errorf("%w of %s: %w", ErrStopped, p.Limit, err)
	}
	return pid, err
}

// Floor runs a floor run's container. Past the limit, or once ctx ends, it
// kills the container by name, since rados is PID 1 there, which ignores
// SIGTERM, and stopping the engine's client leaves the container running;
// then it SIGKILLs the client should it outlive the grace, since a client
// that stalls before the container runs leaves the kill nothing to kill. A
// run that fails has its container killed too, should the client have left
// it running.
func (watchdog) Floor(ctx context.Context, c Container) error {
	cmd := exec.Command(c.Engine, append([]string{"run", "--rm", "--name", c.Name}, c.Args...)...) //nolint:gosec,noctx // the configured engine, bounded by the watchdog below rather than a context
	cmd.Stdout, cmd.Stderr = c.Out, c.Out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = c.Grace
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", c.Engine, err)
	}
	// Buffered, so that the waiter ends once the client does, whether or not
	// anything still receives.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	limit := time.NewTimer(c.Limit)
	defer limit.Stop()
	var stopped error
	select {
	case err := <-done:
		if err != nil {
			c.kill(ctx)
		}
		return err
	case <-limit.C:
		stopped = fmt.Errorf("%w of %s", ErrStopped, c.Limit)
	case <-ctx.Done():
		stopped = context.Cause(ctx)
	}
	c.kill(ctx)
	grace := time.NewTimer(c.Grace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return errors.Join(stopped, fmt.Errorf("killing the client of %s: %w", c.Name, err))
		}
		<-done
	}
	return stopped
}

// kill kills the container by name within the grace. Its failure is no
// failure of the run: a container already gone, removed by --rm, fails it.
func (c Container) kill(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.Grace)
	defer cancel()
	//nolint:errcheck,gosec // a kill that fails leaves only the client to stop; the configured engine and the name this run gave the container
	exec.CommandContext(ctx, c.Engine, "kill", c.Name).Run()
}

// processError is a benchmark process's failure, which carries its exit
// status.
type processError struct {
	mode string
	err  error
}

func (e *processError) Error() string { return "bench " + e.mode + ": " + e.err.Error() }

func (e *processError) Unwrap() error { return e.err }
