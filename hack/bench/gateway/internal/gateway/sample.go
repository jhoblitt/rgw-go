package gateway

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// sampleHeader heads a new samples file; hack/bench/report's gateway reader
// finds its columns by these names.
const sampleHeader = "time,utime,stime,threads,rss_kib\n"

// findRadosgw prints the pid of each process named radosgw in the radosgw
// pod's rgw container. Rook gives the pod a shared process namespace when its
// log collector runs (rook pkg/operator/ceph/object/spec.go), so the radosgw
// is not PID 1 there.
const findRadosgw = `for c in /proc/[0-9]*/comm; do [ "$(cat "$c" 2>/dev/null)" = radosgw ] && basename "$(dirname "$c")"; done; true`

// sampler samples a gateway process once every interval, appending one CSV
// line per sample: the host's time in seconds since the epoch, the process's
// user and system CPU in seconds (/proc/<pid>/stat fields 14 and 15, in
// clock ticks of USER_HZ), its thread count (Threads) and its resident
// memory in KiB (VmRSS, of /proc/<pid>/status).
type sampler struct {
	// target is pid:<pid>, a process on the host, or pod:<name>, the radosgw
	// of a Rook pod in rook-ceph.
	target string
	// pid finds the process to sample, and read reads its stat line, then its
	// status.
	pid   func(ctx context.Context) (string, error)
	read  func(ctx context.Context, pid string) ([]byte, error)
	now   func() time.Time
	every time.Duration
	log   *slog.Logger
}

// newSampler is the sampler of st's target: a pid's through /proc, a pod's
// through rooket's kubectl exec in its rgw container, each of the pod's
// reads within SampleTimeout.
func (r *Runner) newSampler(st *state) sampler {
	s := sampler{target: st.target, now: r.Now, every: r.SampleEvery, log: st.out}
	kind, name, _ := strings.Cut(st.target, ":")
	if kind == "pid" {
		s.pid = func(context.Context) (string, error) { return name, nil }
		s.read = func(_ context.Context, pid string) ([]byte, error) {
			stat, err := fs.ReadFile(r.Proc, pid+"/stat")
			if err != nil {
				return nil, err
			}
			status, err := fs.ReadFile(r.Proc, pid+"/status")
			if err != nil {
				return nil, err
			}
			return append(stat, status...), nil
		}
		return s
	}
	s.pid = func(ctx context.Context) (string, error) {
		out, err := st.cl.Kubectl(ctx, "-n", "rook-ceph", "exec", name, "-c", "rgw", "--", "sh", "-c", findRadosgw)
		if err != nil {
			return "", fmt.Errorf("cannot list the processes of pod %s: %w", name, err)
		}
		pids := strings.TrimRight(string(out), "\n")
		if !pidPattern.MatchString(pids) {
			return "", fmt.Errorf("pod %s runs no single process named radosgw: pids '%s'", name, strings.ReplaceAll(pids, "\n", " "))
		}
		return pids, nil
	}
	cl := st.cl
	cl.CallTimeout = r.SampleTimeout
	s.read = func(ctx context.Context, pid string) ([]byte, error) {
		return cl.Kubectl(ctx, "-n", "rook-ceph", "exec", name, "-c", "rgw", "--",
			"cat", "/proc/"+pid+"/stat", "/proc/"+pid+"/status")
	}
	return s
}

// startSampler samples st's target into path until the returned task is
// stopped. run.log records the start here, and the sampler's end, with its
// counts, once it has stopped.
func (r *Runner) startSampler(ctx context.Context, st *state, path string) *running {
	st.out.InfoContext(ctx, "sampling", slog.String("target", st.target), slog.String("file", path))
	s := r.newSampler(st)
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- s.run(ctx, stop, path) }()
	return &running{end: func() { close(stop) }, done: done}
}

// run samples until stop closes or ctx ends, the first sample at once and
// then one an interval, however long a sample takes: kubectl exec takes a
// good part of a second. A new file gets the header first. A sample that
// fails, as a kubectl exec does when the apiserver hiccups, is tried once
// more and then skipped: the report interpolates the CPU across the gap, and
// one lost second must not end the run. Only a process it cannot find or a
// file it cannot write fails it.
func (s sampler) run(ctx context.Context, stop <-chan struct{}, path string) (err error) {
	pid, err := s.pid(ctx)
	if err != nil {
		return err
	}
	f, err := harness.OpenLog(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	if info.Size() == 0 {
		if _, err := f.WriteString(sampleHeader); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	var taken, skipped int
	defer func() {
		s.log.InfoContext(ctx, "sampled", slog.String("target", s.target), slog.Int("taken", taken), slog.Int("skipped", skipped))
	}()
	wait := time.NewTimer(0)
	defer wait.Stop()
	next := s.now()
	for {
		line, err := s.sample(ctx, pid)
		if err != nil && ctx.Err() == nil {
			line, err = s.sample(ctx, pid)
		}
		switch {
		case err == nil:
			if _, werr := f.WriteString(line); werr != nil {
				return fmt.Errorf("writing %s: %w", path, werr)
			}
			taken++
		case ctx.Err() != nil:
			return nil
		default:
			skipped++
			s.log.WarnContext(ctx, "skipped a sample", slog.String("target", s.target), slog.String("pid", pid),
				slog.Int("skipped", skipped), slog.Any("error", err))
		}
		// A stop during the sample ends the sampler once it is written.
		select {
		case <-stop:
			return nil
		default:
		}
		next = next.Add(s.every)
		wait.Reset(max(next.Sub(s.now()), 0))
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return nil
		case <-wait.C:
		}
	}
}

// sample is one sample's CSV line. The read happens somewhere within its
// round trip, so the sample's time is the middle of it.
func (s sampler) sample(ctx context.Context, pid string) (string, error) {
	t0 := s.now()
	data, err := s.read(ctx, pid)
	t1 := s.now()
	if err != nil {
		return "", err
	}
	return sampleLine(t0.Add(t1.Sub(t0)/2), data)
}

// sampleLine is the CSV line of process data, its stat line then its status,
// read at t.
func sampleLine(t time.Time, data []byte) (string, error) {
	statLine, status, _ := strings.Cut(string(data), "\n")
	stat, ok := harness.ParseStat(statLine)
	if !ok {
		return "", fmt.Errorf("no utime and stime in the stat line %q", statLine)
	}
	var threads, rss string
	for line := range strings.Lines(status) {
		switch f := strings.Fields(line); {
		case len(f) < 2:
		case f[0] == "Threads:":
			threads = f[1]
		case f[0] == "VmRSS:":
			rss = f[1]
		}
	}
	nThreads, err := strconv.ParseInt(threads, 10, 64)
	if err != nil {
		return "", fmt.Errorf("no thread count in the status: %w", err)
	}
	nRSS, err := strconv.ParseInt(rss, 10, 64)
	if err != nil {
		return "", fmt.Errorf("no resident memory in the status: %w", err)
	}
	return fmt.Sprintf("%.3f,%.2f,%.2f,%d,%d\n", float64(t.UnixNano())/1e9,
		float64(stat.UTime)/harness.UserHZ, float64(stat.STime)/harness.UserHZ, nThreads, nRSS), nil
}
