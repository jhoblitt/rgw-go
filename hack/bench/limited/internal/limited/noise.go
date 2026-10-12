package limited

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// busyPercent is the share of one CPU a process must use over the
	// window to be named in noise.log, and busyMax how many are named.
	busyPercent = 5.0
	busyMax     = 12
	// userHZ is the clock-tick rate of /proc/<pid>/stat's utime and stime,
	// USER_HZ, which Linux fixes at 100 for user space.
	userHZ = 100
)

// noise samples the host's load and its busiest processes into noise.log,
// so that a busy host shows beside the cell it touched.
type noise struct {
	proc   fs.FS
	now    func() time.Time
	window time.Duration
	sleep  func(ctx context.Context, d time.Duration) error
}

// run writes a sample every interval until ctx ends.
func (n noise) run(ctx context.Context, w io.Writer, every time.Duration) {
	for {
		if err := n.sample(ctx, w); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "sampling the host's noise", slog.Any("error", err))
		}
		if n.sleep(ctx, every-n.window) != nil {
			return
		}
	}
}

// sample writes the time and load, then the processes that used
// busyPercent of a CPU or more over the window, busiest first. The time and
// load come first, so that a run ending inside the window still shows them.
func (n noise) sample(ctx context.Context, w io.Writer) error {
	load, err := loadavg(n.proc)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "== %s load %s\n", n.now().UTC().Format("15:04:05"), load)
	if err != nil {
		return err
	}
	before := cpuTicks(n.proc)
	err = n.sleep(ctx, n.window)
	if err != nil {
		return err
	}
	after := cpuTicks(n.proc)
	type busy struct {
		percent float64
		comm    string
	}
	var procs []busy
	for pid, a := range after {
		// A pid reused within the window is another process.
		b, ok := before[pid]
		if !ok || b.comm != a.comm || a.ticks < b.ticks {
			continue
		}
		percent := float64(a.ticks-b.ticks) * 100 / userHZ / n.window.Seconds()
		if percent >= busyPercent {
			procs = append(procs, busy{percent, a.comm})
		}
	}
	slices.SortFunc(procs, func(x, y busy) int {
		return cmp.Or(cmp.Compare(y.percent, x.percent), strings.Compare(x.comm, y.comm))
	})
	var b strings.Builder
	for _, p := range procs[:min(len(procs), busyMax)] {
		fmt.Fprintf(&b, "%.1f%% %s\n", p.percent, p.comm)
	}
	_, err = io.WriteString(w, b.String())
	return err
}

// procTicks is a process's name and the CPU time it has used, in clock
// ticks.
type procTicks struct {
	comm  string
	ticks uint64
}

// cpuTicks reads every process's CPU time from /proc. A process that ends
// while it is read is left out.
func cpuTicks(proc fs.FS) map[int]procTicks {
	entries, err := fs.ReadDir(proc, ".")
	if err != nil {
		return nil
	}
	ticks := map[int]procTicks{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := fs.ReadFile(proc, e.Name()+"/stat")
		if err != nil {
			continue
		}
		if t, ok := parseStat(string(data)); ok {
			ticks[pid] = t
		}
	}
	return ticks
}

// parseStat reads the name and utime plus stime of /proc/<pid>/stat. The
// name sits in parentheses and may hold spaces and parentheses itself, so
// the fields are counted from the last closing parenthesis: state is the
// first after it, utime the twelfth and stime the thirteenth.
func parseStat(stat string) (procTicks, bool) {
	open := strings.IndexByte(stat, '(')
	end := strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return procTicks{}, false
	}
	f := strings.Fields(stat[end+1:])
	if len(f) < 13 {
		return procTicks{}, false
	}
	utime, uerr := strconv.ParseUint(f[11], 10, 64)
	stime, serr := strconv.ParseUint(f[12], 10, 64)
	if uerr != nil || serr != nil {
		return procTicks{}, false
	}
	return procTicks{comm: stat[open+1 : end], ticks: utime + stime}, true
}

// sleepContext waits d, or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
