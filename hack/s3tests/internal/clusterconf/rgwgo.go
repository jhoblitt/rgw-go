package clusterconf

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// rgwGoEntity is the config entity rgw-go-up.sh runs rgw-go as.
const rgwGoEntity = "client.rgw.rgw-go"

// flagPattern is a serve flag RGW_GO_FLAGS can carry: make passes it to
// rgw-go-up.sh through the shell, which would split or expand anything else.
var flagPattern = regexp.MustCompile(`^[A-Za-z0-9_./:=,@+-]+$`)

// out is the release's rooket output, where rgw-go-up.sh leaves rgw-go's
// pid and serve flags.
func (c *Configurer) out() string {
	return filepath.Join(c.Root, "hack", "rooket", "out", c.Cluster.Release)
}

// rgwGoPID is the pid of the rgw-go rgw-go-up.sh started, empty when none
// runs.
func (c *Configurer) rgwGoPID() string {
	return harness.RgwGoPID(c.Proc, filepath.Join(c.out(), "rgw-go.pid"))
}

// rgwGoFlags are the serve flags rgw-go-up.sh started the running rgw-go
// with, which it records a line each beside rgw-go's pid.
func (c *Configurer) rgwGoFlags() ([]string, error) {
	path := filepath.Join(c.out(), "rgw-go.flags")
	data, err := os.ReadFile(path) //nolint:gosec // the release's rooket output
	if err != nil {
		return nil, fmt.Errorf("reading the serve flags the running rgw-go was started with: %w; "+
			"restart it with make rgw-go-up RELEASE=%s first", err, c.Cluster.Release)
	}
	var flags []string
	for line := range strings.Lines(string(data)) {
		if line = strings.TrimSuffix(line, "\n"); line == "" {
			continue
		}
		if !flagPattern.MatchString(line) {
			return nil, fmt.Errorf("the running rgw-go was started with the serve flag %q, which RGW_GO_FLAGS cannot "+
				"carry to make rgw-go-up: restart it with hack/rooket/rgw-go-up.sh %s once the mode is set", line,
				c.Cluster.Release)
		}
		flags = append(flags, line)
	}
	return flags, nil
}

// rgwGoPlan is what setRgwGo does: the option rgw-go must run with, the
// running rgw-go's pid and option, and, when it must restart, why and the
// serve flags it restarts with.
type rgwGoPlan struct {
	want, pid, lc, why string
	flags              []string
}

// planRgwGo plans rgw-go's half of a mode that runs its lifecycle worker
// when on, changing nothing. rgw-go reads the option once, when it starts,
// from its entity, which rgw-go-up.sh sets from RGW_GO_LC_THREADS before it
// starts rgw-go and no one else writes, so the entity holds what the running
// rgw-go read. rgw-go restarts through make rgw-go-up when it runs
// otherwise, with the serve flags it was started with, and starts, with
// none, when none runs and the mode runs its worker.
func (c *Configurer) planRgwGo(ctx context.Context, on bool) (rgwGoPlan, error) {
	p := rgwGoPlan{want: strconv.FormatBool(on), pid: c.rgwGoPID()}
	var err error
	if p.lc, err = c.cephValue(ctx, "config", "get", rgwGoEntity, lcOption); err != nil {
		return rgwGoPlan{}, fmt.Errorf("reading %s's %s: %w", rgwGoEntity, lcOption, err)
	}
	switch {
	case p.pid == "" && on:
		p.why = "no rgw-go runs"
	case p.pid != "" && p.lc != p.want:
		p.why = "it runs with " + lcOption + " " + p.lc
		if p.flags, err = c.rgwGoFlags(); err != nil {
			return rgwGoPlan{}, err
		}
	}
	return p, nil
}

// setRgwGo carries out p, and returns the running rgw-go's pid, empty when
// none runs, and its entity's option.
func (c *Configurer) setRgwGo(ctx context.Context, p rgwGoPlan) (pid, lc string, err error) {
	if p.why == "" {
		return p.pid, p.lc, nil
	}
	want := p.want
	slog.InfoContext(ctx, "restarting rgw-go through make rgw-go-up", slog.String(lcOption, want),
		slog.String("rgw_go_flags", strings.Join(p.flags, " ")), slog.String("reason", p.why))
	if _, err = harness.Output(ctx, c.Cluster.Exec, c.RestartTimeout, harness.Cmd{
		Name: c.Make,
		Args: []string{
			"-C", c.Root, "rgw-go-up", "RELEASE=" + c.Cluster.Release, "ROOKET=" + c.Cluster.Rooket,
			"RGW_GO_FLAGS=" + strings.Join(p.flags, " "),
		},
		Env: []string{"RGW_GO_LC_THREADS=" + want},
		Out: c.Out,
	}); err != nil {
		return "", "", fmt.Errorf("make rgw-go-up: %w", err)
	}
	if pid = c.rgwGoPID(); pid == "" {
		return "", "", fmt.Errorf("make rgw-go-up left no rgw-go running against rgw-go-%s", c.Cluster.Release)
	}
	if lc, err = c.cephValue(ctx, "config", "get", rgwGoEntity, lcOption); err != nil {
		return "", "", fmt.Errorf("reading %s's %s: %w", rgwGoEntity, lcOption, err)
	}
	if lc != want {
		return "", "", fmt.Errorf("make rgw-go-up left %s's %s %s, not %s", rgwGoEntity, lcOption, lc, want)
	}
	return pid, lc, nil
}
