// Package harness holds what the bench commands share: commands run on this
// host under deadlines in process groups of their own, a rooket cluster's
// toolbox, the signals that interrupt a run, and what a run reads of the host
// and the repository.
package harness

//go:generate go tool counterfeiter -generate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Cmd is one command to run: its name, its arguments, the environment it
// takes beyond this process's, and what it reads on stdin, nothing when nil.
// Its standard output and error go to Out as it runs when Out is set, and
// Output then returns none.
type Cmd struct {
	Name  string
	Args  []string
	Env   []string
	Stdin []byte
	Out   io.Writer
}

// String is the command line, for errors and logs.
func (c Cmd) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// Execer runs a command to completion and returns its standard output.
//
//counterfeiter:generate . Execer
type Execer interface {
	Output(ctx context.Context, c Cmd) ([]byte, error)
}

// ExecWaitDelay bounds how long Output waits for a command's output once the
// command has exited or been killed: a process that left its group could
// otherwise hold the pipe open past any deadline.
const ExecWaitDelay = 10 * time.Second

// NewExecer returns the Execer that runs commands on this host, waiting
// waitDelay for their output once they have ended.
func NewExecer(waitDelay time.Duration) Execer { return execer{waitDelay: waitDelay} }

// execer runs commands as child processes, each in a process group of its
// own that the context's end kills whole. rooket runs kubectl as a child that
// shares its output (rooket cmd/kubectl.go), so killing rooket alone would
// leave kubectl holding the pipe Output reads, and Output waiting on it.
type execer struct {
	waitDelay time.Duration
}

func (e execer) Output(ctx context.Context, c Cmd) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...) //nolint:gosec // running the configured tools is the command's purpose
	cmd.Env = append(os.Environ(), c.Env...)
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = e.waitDelay
	if c.Out != nil {
		cmd.Stdout, cmd.Stderr = c.Out, c.Out
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("%s: %w", c, err)
		}
		return nil, nil
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && len(exitErr.Stderr) > 0 {
			return out, fmt.Errorf("%s: %w: %s", c, err, bytes.TrimSpace(exitErr.Stderr))
		}
		return out, fmt.Errorf("%s: %w", c, err)
	}
	return out, nil
}

// Output runs c through e within timeout: its whole process group is killed
// at the deadline, as a toolbox call's is, so no command can hold the run.
func Output(ctx context.Context, e Execer, timeout time.Duration, c Cmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return e.Output(ctx, c)
}

// FirstLine is the first line of c's output, run through e within timeout, or
// empty when c fails.
func FirstLine(ctx context.Context, e Execer, timeout time.Duration, c Cmd) string {
	out, err := Output(ctx, e, timeout, c)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}
