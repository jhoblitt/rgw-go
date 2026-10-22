package harness

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// UserHZ is the clock-tick rate of /proc/<pid>/stat's utime and stime,
// USER_HZ, which Linux fixes at 100 for user space.
const UserHZ = 100

var cephTagPattern = regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`)

// PinnedVersion is the Ceph version the release's chart values pin, the one
// place a release's Ceph image is set.
func PinnedVersion(repo, release string) (string, error) {
	path := filepath.Join(repo, "hack", "rooket", release, "values", "rook-ceph-cluster.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // the path is the release's own chart values
	if err != nil {
		return "", fmt.Errorf("reading the chart values: %w", err)
	}
	var values struct {
		CephImage struct {
			Tag string `yaml:"tag"`
		} `yaml:"cephImage"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	m := cephTagPattern.FindStringSubmatch(values.CephImage.Tag)
	if m == nil {
		return "", fmt.Errorf("%s: cephImage.tag %q is not a vX.Y.Z version", path, values.CephImage.Tag)
	}
	return m[1], nil
}

// MemTotalKiB is /proc/meminfo's MemTotal.
func MemTotalKiB(proc fs.FS) (int64, error) {
	data, err := fs.ReadFile(proc, "meminfo")
	if err != nil {
		return 0, fmt.Errorf("reading meminfo: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == "MemTotal:" {
			n, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parsing MemTotal %q: %w", f[1], err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("meminfo has no MemTotal: %w", sc.Err())
}

// CPUModel is /proc/cpuinfo's first model name, or empty where it has none.
func CPUModel(proc fs.FS) string {
	data, err := fs.ReadFile(proc, "cpuinfo")
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(data)) {
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "model name" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Loadavg is /proc/loadavg's three load averages.
func Loadavg(proc fs.FS) (string, error) {
	data, err := fs.ReadFile(proc, "loadavg")
	if err != nil {
		return "", fmt.Errorf("reading loadavg: %w", err)
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return "", fmt.Errorf("loadavg %q has no three load averages", data)
	}
	return strings.Join(f[:3], " "), nil
}

// Stat is what a run reads of /proc/<pid>/stat: the process's name and its
// user and system CPU time, in clock ticks of UserHZ.
type Stat struct {
	Comm         string
	UTime, STime uint64
}

// ParseStat reads the name, utime and stime of /proc/<pid>/stat. The name
// sits in parentheses and may hold spaces and parentheses itself, so the
// fields are counted from the last closing parenthesis: state is the first
// after it, utime the twelfth and stime the thirteenth.
func ParseStat(stat string) (Stat, bool) {
	open := strings.IndexByte(stat, '(')
	end := strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return Stat{}, false
	}
	f := strings.Fields(stat[end+1:])
	if len(f) < 13 {
		return Stat{}, false
	}
	utime, uerr := strconv.ParseUint(f[11], 10, 64)
	stime, serr := strconv.ParseUint(f[12], 10, 64)
	if uerr != nil || serr != nil {
		return Stat{}, false
	}
	return Stat{Comm: stat[open+1 : end], UTime: utime, STime: stime}, true
}

// Sleep waits d, or until ctx ends, returning why it ended: a signal's
// Interrupted when one interrupted the run.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}
