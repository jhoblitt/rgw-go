package limited

//go:generate go tool counterfeiter -generate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Cmd is one command to run: its name, its arguments and the environment it
// takes beyond this process's.
type Cmd struct {
	Name string
	Args []string
	Env  []string
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

// execWaitDelay bounds how long Output waits for a command's output once the
// command has exited or been killed: a process that left its group could
// otherwise hold the pipe open past any deadline.
const execWaitDelay = 10 * time.Second

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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = e.waitDelay
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && len(exitErr.Stderr) > 0 {
			return out, fmt.Errorf("%s: %w: %s", c, err, bytes.TrimSpace(exitErr.Stderr))
		}
		return out, fmt.Errorf("%s: %w", c, err)
	}
	return out, nil
}

// cluster reaches a release's rooket cluster through its Rook toolbox, which
// runs the cluster's Ceph image with the admin keyring.
type cluster struct {
	exec    Execer
	rooket  string
	release string
	// callTimeout bounds each toolbox call, so that a wedged cluster or
	// kubectl session fails the call rather than hanging the run.
	callTimeout time.Duration
}

// toolbox runs args in the toolbox, within callTimeout. Every rooket call
// names its cluster through ROOKET_NAME: without it rooket falls back to a
// name derived from the working directory.
func (c cluster) toolbox(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	return c.exec.Output(ctx, Cmd{
		Name: c.rooket,
		Args: append([]string{"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--"}, args...),
		Env:  []string{"ROOKET_NAME=rgw-go-" + c.release},
	})
}

// ceph runs the ceph CLI in the toolbox, bounding both the connect and each
// mon operation, so a wedged mon fails the command rather than hanging it.
func (c cluster) ceph(ctx context.Context, args ...string) ([]byte, error) {
	return c.toolbox(ctx, append([]string{"ceph", "--connect-timeout=20", "--rados-mon-op-timeout=20"}, args...)...)
}

// poolAvail returns the bytes ceph df says the pool can still take.
func (c cluster) poolAvail(ctx context.Context) (int64, error) {
	out, err := c.ceph(ctx, "df", "-f", "json")
	if err != nil {
		return 0, fmt.Errorf("reading ceph df: %w", err)
	}
	return decodePoolAvail(out, pool)
}

// decodePoolAvail reads the named pool's max_avail from ceph df -f json.
func decodePoolAvail(data []byte, name string) (int64, error) {
	var df struct {
		Pools []struct {
			Name  string `json:"name"`
			Stats struct {
				MaxAvail *int64 `json:"max_avail"`
			} `json:"stats"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(data, &df); err != nil {
		return 0, fmt.Errorf("decoding ceph df: %w", err)
	}
	for _, p := range df.Pools {
		if p.Name != name {
			continue
		}
		if p.Stats.MaxAvail == nil {
			return 0, fmt.Errorf("ceph df gives pool %s no max_avail", name)
		}
		return *p.Stats.MaxAvail, nil
	}
	return 0, fmt.Errorf("ceph df lists no pool %s", name)
}

// objecter returns the client's value of the named objecter option, or
// unknown when the cluster cannot say.
func (c cluster) objecter(ctx context.Context, name string) string {
	out, err := c.ceph(ctx, "config", "get", "client.admin", name)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// rados runs the rados CLI in the toolbox against the pool, failing any OSD
// or monitor operation that takes over two minutes, as the sweep's floor runs
// do, rather than waiting on a stuck placement group. Killing kubectl at a
// call's deadline ends the call here, but the rados it started in the
// toolbox's pod can outlive the exec session; these timeouts end it there.
func (c cluster) rados(ctx context.Context, args ...string) ([]byte, error) {
	return c.toolbox(ctx, append([]string{"rados", "--rados-osd-op-timeout=120", "--rados-mon-op-timeout=120", "-p", pool}, args...)...)
}

// objects lists the pool's objects in namespace ns, or in every namespace
// when ns is empty.
func (c cluster) objects(ctx context.Context, ns string) ([]string, error) {
	args := []string{"ls", "--all"}
	if ns != "" {
		args = []string{"-N", ns, "ls"}
	}
	out, err := c.rados(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("listing the objects in %s: %w", pool, err)
	}
	var names []string
	for line := range strings.Lines(string(out)) {
		if line = strings.TrimRight(line, "\n"); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// purgeBatch is how many names one rados rm takes.
const purgeBatch = 1024

// purge removes every object left in namespace ns, the objects of a
// benchmark process that did not end cleanly: one that does removes its own.
func (c cluster) purge(ctx context.Context, log *slog.Logger, ns string) error {
	names, err := c.objects(ctx, ns)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	log.InfoContext(ctx, "removing the objects left", slog.String("namespace", ns), slog.Int("objects", len(names)))
	for batch := range slices.Chunk(names, purgeBatch) {
		if _, err := c.rados(ctx, append([]string{"-N", ns, "rm"}, batch...)...); err != nil {
			return fmt.Errorf("removing the objects left in %s: %w", ns, err)
		}
	}
	log.InfoContext(ctx, "removed the objects left", slog.String("namespace", ns))
	return nil
}
