package seam

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// resolveFloorImage settles the image whose rados runs the floor: the Ceph
// image of the release of the librados the benchmark links, so that the Go
// cells and their floor use one client release, unless FloorImage names
// another, which must run that release all the same. It pulls the image when
// the engine lacks it, and fails when none can be found or it runs another
// release.
func (r *Runner) resolveFloorImage(ctx context.Context, st *state, lib librados) error {
	engine := st.cfg.Engine
	image := cmp.Or(st.cfg.FloorImage, "quay.io/ceph/ceph:v"+lib.version)
	if _, err := r.local(ctx, r.LocalTimeout, harness.Cmd{Name: engine, Args: []string{"image", "inspect", image}}); err != nil {
		st.log.InfoContext(ctx, "pulling the floor image", slog.String("image", image))
		out, err := r.local(ctx, r.PullTimeout, harness.Cmd{Name: engine, Args: []string{"pull", image}})
		r.floorHeader(st, engine+" pull "+image)
		if _, werr := st.floorLog.Write(out); werr != nil {
			return fmt.Errorf("writing floor.log: %w", werr)
		}
		if err != nil {
			return fmt.Errorf("no floor image %s for librados %s: the floor's rados must be the release the benchmark links: %w",
				image, lib.version, err)
		}
	}
	out, err := r.local(ctx, r.LocalTimeout, harness.Cmd{Name: engine, Args: []string{"run", "--rm", image, "rados", "--version"}})
	if err != nil {
		return fmt.Errorf("cannot run rados --version in %s: %w", image, err)
	}
	// ceph version 19.2.6 (<commit>) squid (stable)
	line, _, _ := strings.Cut(string(out), "\n")
	var version string
	if f := strings.Fields(line); len(f) >= 3 {
		version = f[2]
	}
	if version != lib.version {
		return fmt.Errorf("the floor image %s runs Ceph %s, but the benchmark links librados %s", image, version, lib.version)
	}
	st.floorImage, st.floorVersion = image, version
	st.log.InfoContext(ctx, "the floor's rados comes from the floor image", slog.String("floor_image", image),
		slog.String("ceph_version", version), slog.String("librados_path", lib.path), slog.String("cluster_image", st.clusterImage))
	return nil
}

// floor measures a shape's floor at conc once with one image's rados and
// removes what it wrote: a write over FloorSeconds, or of Write4mObjects
// objects at 4 MiB once the pool has the room, or a rand among conc objects a
// write wrote for it, as each seam worker reads its own object. A run from
// the cluster's image is skipped where that is the floor image.
func (r *Runner) floor(ctx context.Context, st *state, f floorStep) error {
	image := st.floorImage
	if f.cluster {
		if st.clusterImage == st.floorImage {
			return nil
		}
		image = st.clusterImage
	}
	m := measured{
		image: image, shape: f.shape, size: f.size(), conc: f.conc, seconds: f.window(st.cfg), bracket: f.bracket,
		run: fmt.Sprintf("floor-%s-%d-%s-%s", f.shape, f.conc, cmp.Or(f.bracket, "single"), imageTag(image)),
	}
	ns := m.namespace()
	st.left.addFloor(ns)
	switch {
	case f.waits():
		if err := r.waitAvail(ctx, st, fmt.Sprintf("the write4m floor at %d", f.conc)); err != nil {
			return err
		}
		m.op, m.maxObjects = "write", st.cfg.Write4mObjects
	case f.write():
		m.op = "write"
	default:
		if err := r.unmeasured(ctx, st, image, floorCapSeconds, "-N", ns,
			"bench", strconv.Itoa(floorCapSeconds), "write", "-t", strconv.Itoa(f.conc), "-b", strconv.FormatInt(m.size, 10),
			"--max-objects", strconv.Itoa(f.conc), "--no-cleanup", "--no-hints", "--run-name", m.run); err != nil {
			return fmt.Errorf("writing the rand set of %s: %w", m.run, err)
		}
		m.op, m.objects = "rand", f.conc
	}
	if err := r.radosBench(ctx, st, m); err != nil {
		return err
	}
	if err := r.unmeasured(ctx, st, image, 0, "-N", ns, "cleanup", "--run-name", m.run, "-t", "64"); err != nil {
		return fmt.Errorf("rados cleanup of %s: %w", m.run, err)
	}
	return nil
}

// imageTag is what follows an image reference's last colon, its tag.
func imageTag(image string) string {
	return image[strings.LastIndexByte(image, ':')+1:]
}

// measured is one rados bench run that measures a floor: the image whose
// rados runs it, the shape it mirrors, the op, object size and concurrency,
// the seconds it runs and the objects it stops at (0 for no limit), the
// objects a rand reads among, its run name and its bracket.
type measured struct {
	image, shape, op string
	size             int64
	conc             int
	seconds          int64
	maxObjects       int64
	objects          int
	run, bracket     string
}

// namespace is the floor namespace of the run's object size.
func (m measured) namespace() string {
	return floorNamespace + strconv.FormatInt(m.size, 10)
}

// floorLine is one line of floor.jsonl, as hack/bench/report reads it.
type floorLine struct {
	Kind          string  `json:"kind"`
	Tool          string  `json:"tool"`
	Image         string  `json:"image"`
	Shape         string  `json:"shape"`
	Op            string  `json:"op"`
	Size          int64   `json:"size"`
	Conc          int     `json:"conc"`
	Bracket       string  `json:"bracket"`
	Seconds       int64   `json:"seconds"`
	MaxObjects    int64   `json:"max_objects"`
	Objects       int     `json:"objects"`
	Ops           int64   `json:"ops"`
	ElapsedS      float64 `json:"elapsed_s"`
	OpsPerSec     float64 `json:"ops_per_sec"`
	MeanUs        float64 `json:"mean_us"`
	BandwidthMBps float64 `json:"bandwidth_mbps"`
}

// radosBench runs m, keeping its output in <run>.txt, and appends its
// summary to floor.jsonl as the shape's floor.
func (r *Runner) radosBench(ctx context.Context, st *state, m measured) error {
	args := []string{"-N", m.namespace(), "bench", strconv.FormatInt(m.seconds, 10), m.op, "-t", strconv.Itoa(m.conc), "--run-name", m.run}
	if m.op == "write" {
		// rados bench otherwise sends an allocation hint with each write, and
		// the Go cells send none.
		args = append(args, "-b", strconv.FormatInt(m.size, 10), "--no-cleanup", "--no-hints")
		if m.maxObjects != 0 {
			args = append(args, "--max-objects", strconv.FormatInt(m.maxObjects, 10))
		}
	} else {
		args = append(args, "--no-verify")
	}
	raw := filepath.Join(st.dir, m.run+".txt")
	out, err := os.Create(raw) //nolint:gosec // the path is in the run directory
	if err != nil {
		return fmt.Errorf("creating %s: %w", raw, err)
	}
	err = errors.Join(r.rados(ctx, st, m.image, m.seconds, out, args...), out.Close())
	data, rerr := os.ReadFile(raw) //nolint:gosec // the path is in the run directory
	if err != nil {
		if tail := lastLines(data, 5); tail != "" {
			return fmt.Errorf("rados bench failed: %w: %s", err, tail)
		}
		return fmt.Errorf("rados bench failed: %w", err)
	}
	if rerr != nil {
		return fmt.Errorf("reading %s: %w", raw, rerr)
	}
	sum, err := parseSummary(data)
	if err != nil {
		return fmt.Errorf("no summary in %s: %w", raw, err)
	}
	line := floorLine{
		Kind: "floor", Tool: "rados bench", Image: m.image, Shape: m.shape, Op: m.op, Size: m.size, Conc: m.conc,
		Bracket: m.bracket, Seconds: m.seconds, MaxObjects: m.maxObjects, Objects: m.objects,
		Ops: sum.ops, ElapsedS: sum.elapsed, OpsPerSec: float64(sum.ops) / sum.elapsed,
		MeanUs: sum.latency * 1e6, BandwidthMBps: sum.bandwidth,
	}
	encoded, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("encoding the floor of %s: %w", m.run, err)
	}
	f, err := harness.OpenLog(filepath.Join(st.dir, "floor.jsonl"))
	if err != nil {
		return err
	}
	_, err = f.Write(append(encoded, '\n'))
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("appending to floor.jsonl: %w", err)
	}
	st.log.InfoContext(ctx, "floor", slog.String("shape", m.shape), slog.Int("conc", m.conc), slog.String("line", string(encoded)))
	return nil
}

// summary is what rados bench's summary reports of a run.
type summary struct {
	elapsed   float64
	ops       int64
	bandwidth float64
	latency   float64
}

// summaryLines are the lines of rados bench's summary a floor reads, by
// what each reports.
var summaryLines = map[string]string{
	"Total time run:":     "time run",
	"Total writes made:":  "operations made",
	"Total reads made:":   "operations made",
	"Bandwidth (MB/sec):": "bandwidth",
	"Average Latency(s):": "mean latency",
}

// parseSummary reads rados bench's summary: the time run, the operations
// made, the bandwidth and the mean latency. The summary's Average IOPS is
// truncated to an integer, so a rate is the operations made over the time
// run. Where a line repeats, the last one counts.
func parseSummary(out []byte) (summary, error) {
	values := map[string]string{}
	for line := range strings.Lines(string(out)) {
		for prefix, what := range summaryLines {
			if f := strings.Fields(line); strings.HasPrefix(line, prefix) && len(f) > 0 {
				values[what] = f[len(f)-1]
			}
		}
	}
	var (
		s    summary
		errs []error
	)
	number := func(what string, into *float64) {
		v, ok := values[what]
		if !ok {
			errs = append(errs, fmt.Errorf("no %s", what))
			return
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			errs = append(errs, fmt.Errorf("the %s, %q, is not a number", what, v))
			return
		}
		*into = n
	}
	number("time run", &s.elapsed)
	number("bandwidth", &s.bandwidth)
	number("mean latency", &s.latency)
	if v, ok := values["operations made"]; !ok {
		errs = append(errs, errors.New("no operations made"))
	} else if n, err := strconv.ParseInt(v, 10, 64); err != nil {
		errs = append(errs, fmt.Errorf("the operations made, %q, are not a whole number", v))
	} else {
		s.ops = n
	}
	if err := errors.Join(errs...); err != nil {
		return summary{}, err
	}
	if s.elapsed <= 0 {
		return summary{}, fmt.Errorf("the time run, %g s, is no time to divide by", s.elapsed)
	}
	return s, nil
}

// lastLines is the last n lines of out.
func lastLines(out []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	return strings.Join(lines[max(len(lines)-n, 0):], "\n")
}

// unmeasured runs rados from image for the floor without measuring
// anything, a rand floor's write or a cleanup, its output going to
// floor.log.
func (r *Runner) unmeasured(ctx context.Context, st *state, image string, window int64, args ...string) error {
	r.floorHeader(st, "rados "+strings.Join(args, " ")+" from "+image)
	return r.rados(ctx, st, image, window, st.floorLog, args...)
}

// floorHeader opens a command's output in floor.log.
func (r *Runner) floorHeader(st *state, what string) {
	fmt.Fprintf(st.floorLog, "== %s %s\n", r.Now().UTC().Format("15:04:05"), what)
}

// rados runs rados from image against the pool in a container of its own,
// bounded twice: librados fails an OSD or monitor operation that takes over
// two minutes, and the watchdog stops the run once its window, window
// seconds, and watchdogMargin have passed, or FloorTimeout. Each container is
// named for this process and the run's count, so that a stuck one can be
// killed by name.
func (r *Runner) rados(ctx context.Context, st *state, image string, window int64, out io.Writer, args ...string) error {
	st.floorRuns++
	name := fmt.Sprintf("rgw-go-bench-floor-%d-%d", r.Pid, st.floorRuns)
	limit := st.cfg.floorLimit(window)
	// rados bench names its objects after the host name, so every run takes
	// the same one: rand and cleanup find what an earlier write wrote.
	runArgs := append([]string{
		"--network", "host", "--hostname", "rgw-go-bench-floor", "-v", st.cfg.clusterOut() + ":/etc/ceph:ro", image,
		"rados", "-c", "/etc/ceph/ceph.conf", "-k", "/etc/ceph/ceph.client.admin.keyring",
		"--rados-osd-op-timeout=120", "--rados-mon-op-timeout=120", "-p", pool,
	}, args...)
	st.log.InfoContext(ctx, "rados", slog.String("image", image), slog.String("container", name),
		slog.String("args", strings.Join(args, " ")), slog.Int64("limit_s", int64(limit/time.Second)), slog.String("load", r.load()))
	err := r.Launcher.Floor(ctx, Container{Engine: st.cfg.Engine, Name: name, Args: runArgs, Out: out, Limit: limit, Grace: r.Grace})
	if errors.Is(err, ErrStopped) {
		st.log.ErrorContext(ctx, "rados stopped past its limit", slog.String("container", name), slog.Int64("limit_s", int64(limit/time.Second)))
	}
	return err
}
