package clusterconf

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// lcOption is the option an LC mode sets on each gateway's entity.
const lcOption = "rgw_enable_lc_threads"

// Configurer readies a release's rooket cluster for the lifecycle tests, the
// storage classes they name and the LC mode, and for the MFA tests, their
// user's TOTP device.
type Configurer struct {
	Cluster harness.Cluster
	// Root is the repository's root: its Makefile runs rgw-go-up, and its
	// hack/rooket/out/<release>/rgw-go.pid names the rgw-go that runs.
	Root string
	// Make is the make that runs make rgw-go-up.
	Make string
	// Proc is /proc, where the pid's process is checked to be rgw-go.
	Proc fs.FS
	Now  func() time.Time
	// Sleep waits between polls, or until ctx ends.
	Sleep func(ctx context.Context, d time.Duration) error
	// SiteWait bounds each wait on the service map, as the hack/rooket
	// scripts' does, and SitePoll is its poll.
	SiteWait, SitePoll time.Duration
	// RookWait bounds the wait for Rook to reconcile a changed rgwConfig.
	RookWait time.Duration
	// RestartTimeout bounds a rollout of the radosgw and make rgw-go-up.
	RestartTimeout time.Duration
	// Out takes the output of the rollout and of make rgw-go-up as they run.
	Out io.Writer
}

// State is what each gateway runs once Apply has set a mode, as read from
// the running daemons.
type State struct {
	// RadosgwEntity is the radosgw's config entity, RadosgwLC the
	// rgw_enable_lc_threads its running daemon reports, and RadosgwStarted
	// its service map entry's start_stamp.
	RadosgwEntity, RadosgwLC, RadosgwStarted string
	// RgwGoPID is the running rgw-go's pid, empty when none runs, and
	// RgwGoLC the rgw_enable_lc_threads its entity held when it started.
	RgwGoPID, RgwGoLC string
}

// Lines are the state as lc-mode prints it, a line each.
func (s State) Lines() []string {
	rgwGo := "rgw-go " + rgwGoEntity + ": not running"
	if s.RgwGoPID != "" {
		rgwGo = fmt.Sprintf("rgw-go %s: %s %s, pid %s", rgwGoEntity, lcOption, s.RgwGoLC, s.RgwGoPID)
	}
	return []string{
		fmt.Sprintf("radosgw %s: %s %s, started %s", s.RadosgwEntity, lcOption, s.RadosgwLC, s.RadosgwStarted),
		rgwGo,
	}
}

// Apply runs radosgw's lifecycle worker and rgw-go's exactly as mode m
// says, restarting each gateway whose running daemon reads otherwise and
// leaving alone each that already runs as m says. rgw-go's half is planned,
// its serve flags read and checked, before either half changes anything, so
// that a restart it would refuse leaves the cluster as it was.
func (c *Configurer) Apply(ctx context.Context, m Mode) (State, error) {
	rgw, err := c.radosgw(ctx)
	if err != nil {
		return State{}, err
	}
	plan, err := c.planRgwGo(ctx, m.RgwGo())
	if err != nil {
		return State{}, fmt.Errorf("setting rgw-go's %s: %w", lcOption, err)
	}
	if err = c.setRadosgw(ctx, &rgw, m.Radosgw()); err != nil {
		return State{}, fmt.Errorf("setting radosgw's %s: %w", lcOption, err)
	}
	st := State{RadosgwEntity: rgw.entity, RadosgwLC: rgw.lc, RadosgwStarted: rgw.StartStamp}
	if st.RgwGoPID, st.RgwGoLC, err = c.setRgwGo(ctx, plan); err != nil {
		return State{}, fmt.Errorf("setting rgw-go's %s: %w", lcOption, err)
	}
	return st, nil
}

// ClassState is where the storage classes s3-tests names are, and whether
// EnsureClasses added them.
type ClassState struct {
	Placement, DataPool string
	Added               bool
}

// Line is the state as classes prints it.
func (s ClassState) Line() string {
	how := "present"
	if s.Added {
		how = "added"
	}
	return fmt.Sprintf("storage classes %s in %s on %s: %s", ConfValue(), s.Placement, s.DataPool, how)
}

// EnsureClasses adds the storage classes s3-tests names to the cluster. An
// rgw-go reads the zone only when it starts, so when the classes are added
// while one runs it warns that the rgw-go knows none of them until make
// rgw-go-up restarts it.
func (c *Configurer) EnsureClasses(ctx context.Context) (ClassState, error) {
	rgw, err := c.radosgw(ctx)
	if err != nil {
		return ClassState{}, err
	}
	var st ClassState
	if st.Placement, st.DataPool, st.Added, err = c.ensureClasses(ctx, &rgw); err != nil {
		return ClassState{}, fmt.Errorf("adding the storage classes %s: %w", ConfValue(), err)
	}
	if pid := c.rgwGoPID(); st.Added && pid != "" {
		slog.WarnContext(ctx, "the running rgw-go reads the zone only when it starts; restart it with make rgw-go-up",
			slog.String("pid", pid), slog.String("release", c.Cluster.Release))
	}
	return st, nil
}

// cephValue runs the ceph CLI in the toolbox and returns its output's first
// line, which is where config get and config show print a value.
func (c *Configurer) cephValue(ctx context.Context, args ...string) (string, error) {
	out, err := c.Cluster.Ceph(ctx, args...)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line), nil
}

// kubectlLong runs a rooket kubectl command that outlasts a toolbox call,
// within RestartTimeout, its output going to Out.
func (c *Configurer) kubectlLong(ctx context.Context, args ...string) error {
	_, err := harness.Output(ctx, c.Cluster.Exec, c.RestartTimeout, harness.Cmd{
		Name: c.Cluster.Rooket,
		Args: append([]string{"kubectl"}, args...),
		Env:  c.Cluster.Env(),
		Out:  c.Out,
	})
	return err
}

// poll is harness.Poll.Until bounded by wait, polling every SitePoll.
func (c *Configurer) poll(ctx context.Context, wait time.Duration, try func() (bool, error),
	timeout func(last error) error,
) error {
	return harness.Poll{Now: c.Now, Sleep: c.Sleep, Wait: wait, Every: c.SitePoll}.Until(ctx, try, timeout)
}

// seconds is d in whole seconds, for messages.
func seconds(d time.Duration) int64 { return int64(d / time.Second) }
