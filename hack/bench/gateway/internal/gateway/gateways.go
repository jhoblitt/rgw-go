package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

var pidPattern = regexp.MustCompile(`^\d+$`)

// useRadosgw readies a radosgw run: it refuses to start while an rgw-go
// runs against the cluster, so that the radosgw run has the host to itself,
// checks the radosgw's entity holds the parity options, and names the
// radosgw's pod as what the sampler reads.
func (r *Runner) useRadosgw(ctx context.Context, st *state) error {
	if r.rgwGoRuns(st.cfg) {
		return fmt.Errorf("an rgw-go runs against rgw-go-%s: stop it with make rgw-go-down RELEASE=%s, so that the radosgw run has the host to itself",
			st.cfg.Release, st.cfg.Release)
	}
	if err := r.parityCheck(ctx, st, st.radosgwEntity, "show"); err != nil {
		return err
	}
	st.frontends = st.radosgwFrontends
	st.endpoint = st.radosgwEndpoint
	st.target = "pod:" + st.radosgwPod
	return nil
}

// rgwGoRuns reports whether the rgw-go rgw-go-up.sh started still runs: a
// pid outlives its process and can be reused, so only one whose process is
// named rgw-go counts.
func (r *Runner) rgwGoRuns(cfg Config) bool {
	data, err := os.ReadFile(filepath.Join(cfg.clusterOut(), "rgw-go.pid"))
	if err != nil {
		return false
	}
	pid := strings.TrimSpace(string(data))
	if !pidPattern.MatchString(pid) {
		return false
	}
	comm, err := fs.ReadFile(r.Proc, pid+"/comm")
	return err == nil && strings.TrimRight(string(comm), "\n") == "rgw-go"
}

// startRgwGo restarts rgw-go through rgw-go-up.sh with GODEBUG=gctrace=1 and
// RGW_GO_FLAGS, checks both gateways' entities hold the parity options,
// which rgw-go-up.sh sets, and reads where rgw-go serves, its metrics
// address, its pid and its frontends. From the restart on, the run's exit
// stops rgw-go, a failed restart's included.
func (r *Runner) startRgwGo(ctx context.Context, st *state) error {
	cfg := st.cfg
	// rgw-go-up.sh builds bin/rgw-go before it starts it, and under
	// GODEBUG=gctrace=1 the compiler, a Go program too, prints its own GC
	// lines into the build's output. Built here first, without it, the binary
	// is current there and nothing compiles.
	st.log.InfoContext(ctx, "building rgw-go")
	if err := r.logged(ctx, st, r.BuildTimeout, harness.Cmd{Name: "go", Args: []string{
		"-C", cfg.Repo, "build", "-tags=" + cfg.GoTags, "-o", "bin/rgw-go", "./cmd/rgw-go",
	}}); err != nil {
		return fmt.Errorf("building rgw-go failed; see %s: %w", st.runLog, err)
	}
	st.log.InfoContext(ctx, "restarting rgw-go with GODEBUG=gctrace=1", slog.String("rgw_go_flags", cfg.RgwGoFlags))
	st.rgwGoStarted = true
	if err := r.logged(ctx, st, r.UpTimeout, harness.Cmd{
		Name: filepath.Join(cfg.Repo, "hack", "rooket", "rgw-go-up.sh"),
		Args: append([]string{cfg.Release}, cfg.RgwGoArgs...),
		Env:  []string{"GODEBUG=gctrace=1", "GO_TAGS=" + cfg.GoTags, "ROOKET=" + cfg.Rooket},
	}); err != nil {
		return fmt.Errorf("rgw-go-up.sh failed; see %s: %w", st.runLog, err)
	}
	if err := r.parityCheck(ctx, st, st.radosgwEntity, "show"); err != nil {
		return err
	}
	if err := r.parityCheck(ctx, st, rgwGoEntity, "get"); err != nil {
		return err
	}
	out := cfg.clusterOut()
	endpoint, err := os.ReadFile(filepath.Join(out, "rgw-go.endpoint")) //nolint:gosec // the release's rooket output
	if err != nil {
		return fmt.Errorf("reading rgw-go's endpoint: %w", err)
	}
	st.endpoint = strings.TrimRight(string(endpoint), "\n")
	metrics, err := os.ReadFile(filepath.Join(out, "rgw-go.metrics")) //nolint:gosec // the release's rooket output
	if st.metrics = strings.TrimRight(string(metrics), "\n"); err != nil || st.metrics == "" {
		return errors.New("rgw-go serves no metrics address, so no CPU profile can be taken")
	}
	data, err := os.ReadFile(filepath.Join(out, "rgw-go.pid")) //nolint:gosec // the release's rooket output
	if err != nil {
		return fmt.Errorf("reading rgw-go's pid: %w", err)
	}
	pid := strings.TrimRight(string(data), "\n")
	if !pidPattern.MatchString(pid) {
		return fmt.Errorf("%s holds '%s', not a pid", filepath.Join(out, "rgw-go.pid"), pid)
	}
	st.frontends = frontendsOf(r.Proc, pid)
	st.target = "pid:" + pid
	return nil
}

// frontendsOf is the frontends process pid was started with: the last
// --rgw-frontends on its command line, which is the one ceph's argument
// parsing keeps; empty when none can be read.
func frontendsOf(proc fs.FS, pid string) string {
	cmdline, err := fs.ReadFile(proc, pid+"/cmdline")
	if err != nil {
		return ""
	}
	var value string
	for arg := range strings.SplitSeq(strings.TrimSuffix(string(cmdline), "\x00"), "\x00") {
		for _, flag := range []string{"--rgw-frontends=", "--rgw_frontends="} {
			if v, ok := strings.CutPrefix(arg, flag); ok {
				value = v
			}
		}
	}
	return value
}

// gcLine starts each line GODEBUG=gctrace=1 prints, one per GC cycle.
var gcLine = regexp.MustCompile(`^gc \d+ @`)

// completeLines is the offset in path past its last complete line, where the
// lines a profile adds to rgw-go's log begin.
func completeLines(path string) (int64, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the release's rooket output
	if err != nil {
		return 0, fmt.Errorf("reading rgw-go's log: %w", err)
	}
	return int64(bytes.LastIndexByte(data, '\n') + 1), nil
}

// keepGCTrace writes the GC lines rgw-go logged past offset of its log to
// path, every line that starts as one. GODEBUG=gctrace=1 prints them to
// rgw-go's stderr, which its log holds beside the JSON log, and nothing
// orders the two writers: the runtime writes a GC line in pieces, a write
// each, and slog a record in one write, so a record can land between two
// pieces, ending the GC line early and leaving its rest at the start of a
// later line. Such a torn line is kept as it is, for hack/bench/report to
// tell from a whole one; its rest, and a line torn before its "@", do not
// start as a GC line and are not kept. The report still sees the cycle of a
// line torn before its "@", through the gap it leaves in the cycles' numbers.
func keepGCTrace(log string, offset int64, path string) error {
	f, err := os.Open(log) //nolint:gosec // the path is the release's rooket output
	if err != nil {
		return fmt.Errorf("reading rgw-go's log: %w", err)
	}
	defer f.Close() //nolint:errcheck // read only
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("reading rgw-go's log: %w", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("reading rgw-go's log: %w", err)
	}
	var gc strings.Builder
	for line := range strings.Lines(string(data)) {
		if line = strings.TrimRight(line, "\n"); gcLine.MatchString(line) {
			gc.WriteString(line + "\n")
		}
	}
	if err := os.WriteFile(path, []byte(gc.String()), 0o600); err != nil { //nolint:gosec // the path is in the run directory
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
