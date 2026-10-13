package seam

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Environment is a run's env.json: the host, the cluster and the settings
// it ran with. Its fields are those hack/bench/seam.sh records for the limited
// run, under the same names.
type Environment struct {
	Release                 string `json:"release"`
	CephVersion             string `json:"ceph_version"`
	ClusterImage            string `json:"cluster_image"`
	LibradosVersion         string `json:"librados_version"`
	LibradosPath            string `json:"librados_path"`
	Date                    string `json:"date"`
	RooketVersion           string `json:"rooket_version"`
	NProc                   int    `json:"nproc"`
	MemTotalKiB             int64  `json:"mem_total_kib"`
	CPU                     string `json:"cpu"`
	Kernel                  string `json:"kernel"`
	GoVersion               string `json:"go_version"`
	HostLibrados            string `json:"host_librados"`
	GitDescribe             string `json:"git_describe"`
	GOMAXPROCS              string `json:"gomaxprocs"`
	OverObjects             int64  `json:"over_objects"`
	ObjecterInflightOps     string `json:"objecter_inflight_ops"`
	ObjecterInflightOpBytes string `json:"objecter_inflight_op_bytes"`
	PoolMaxAvail            int64  `json:"pool_max_avail"`
	Load                    string `json:"load"`
	Limited                 bool   `json:"limited"`
	InflightBytes           int64  `json:"inflight_bytes"`
}

var (
	cephTagPattern = regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`)
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// environment collects the run's env.json. The benchmark binary, bin, must
// be built: its librados is the client release the cells ran.
func (r *Runner) environment(ctx context.Context, cfg Config, cl cluster, bin, clusterImage string) (Environment, error) {
	cephVersion, err := pinnedVersion(cfg)
	if err != nil {
		return Environment{}, err
	}
	libPath, libVersion, err := r.linkedLibrados(ctx, bin)
	if err != nil {
		return Environment{}, err
	}
	memTotal, err := memTotalKiB(r.Proc)
	if err != nil {
		return Environment{}, err
	}
	load, err := loadavg(r.Proc)
	if err != nil {
		return Environment{}, err
	}
	kernel, err := fs.ReadFile(r.Proc, "sys/kernel/osrelease")
	if err != nil {
		return Environment{}, fmt.Errorf("reading the kernel release: %w", err)
	}
	avail, err := cl.poolAvail(ctx)
	if err != nil {
		return Environment{}, err
	}
	rooketVersion := r.Getenv("ROOKET_VERSION")
	if rooketVersion == "" {
		rooketVersion = r.firstLine(ctx, Cmd{Name: cfg.Rooket, Args: []string{"version"}})
	}
	return Environment{
		Release:                 cfg.Release,
		CephVersion:             cephVersion,
		ClusterImage:            clusterImage,
		LibradosVersion:         libVersion,
		LibradosPath:            libPath,
		Date:                    r.Now().UTC().Format("2006-01-02T15:04:05Z"),
		RooketVersion:           rooketVersion,
		NProc:                   runtime.NumCPU(),
		MemTotalKiB:             memTotal,
		CPU:                     cpuModel(r.Proc),
		Kernel:                  strings.TrimSpace(string(kernel)),
		GoVersion:               r.firstLine(ctx, Cmd{Name: "go", Args: []string{"version"}}),
		HostLibrados:            r.hostLibrados(ctx),
		GitDescribe:             r.firstLine(ctx, Cmd{Name: "git", Args: []string{"-C", cfg.Repo, "describe", "--always", "--dirty"}}),
		GOMAXPROCS:              cmp.Or(r.Getenv("GOMAXPROCS"), "unset"),
		OverObjects:             cfg.OverObjects,
		ObjecterInflightOps:     cl.objecter(ctx, "objecter_inflight_ops"),
		ObjecterInflightOpBytes: cl.objecter(ctx, "objecter_inflight_op_bytes"),
		PoolMaxAvail:            avail,
		Load:                    load,
		Limited:                 true,
		InflightBytes:           cfg.InflightBytes,
	}, nil
}

// firstLine is the first line of c's output, or empty when c fails.
func (r *Runner) firstLine(ctx context.Context, c Cmd) string {
	out, err := r.local(ctx, r.LocalTimeout, c)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}

// pinnedVersion is the Ceph version the release's chart values pin, the one
// place a release's Ceph image is set.
func pinnedVersion(cfg Config) (string, error) {
	path := filepath.Join(cfg.Repo, "hack", "rooket", cfg.Release, "values", "rook-ceph-cluster.yaml")
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

// linkedLibrados returns the path of the librados bin links and the Ceph
// version of the package that owns it.
func (r *Runner) linkedLibrados(ctx context.Context, bin string) (path, version string, err error) {
	out, err := r.local(ctx, r.LocalTimeout, Cmd{Name: "ldd", Args: []string{bin}})
	if err != nil {
		return "", "", fmt.Errorf("listing the libraries %s links: %w", bin, err)
	}
	var lib string
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == "librados.so.2" && f[1] == "=>" {
			lib = f[2]
			break
		}
	}
	if lib == "" {
		return "", "", fmt.Errorf("%s links no librados.so.2", bin)
	}
	if lib, err = r.Resolve(lib); err != nil {
		return "", "", fmt.Errorf("resolving %s: %w", lib, err)
	}
	version, err = r.packageVersion(ctx, lib)
	if err != nil {
		return "", "", fmt.Errorf("cannot tell which Ceph release the librados %s links belongs to: %w", bin, err)
	}
	if !versionPattern.MatchString(version) {
		return "", "", fmt.Errorf("the librados %s links, %s, is version %q, not X.Y.Z", bin, lib, version)
	}
	return lib, version, nil
}

// packageVersion is the upstream version of the rpm or deb package that owns
// path: a deb version's epoch and Debian revision are dropped.
func (r *Runner) packageVersion(ctx context.Context, path string) (string, error) {
	if out, err := r.local(ctx, r.LocalTimeout, Cmd{Name: "rpm", Args: []string{"-qf", "--qf", "%{VERSION}", path}}); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	owner, err := r.local(ctx, r.LocalTimeout, Cmd{Name: "dpkg", Args: []string{"-S", path}})
	if err != nil {
		return "", fmt.Errorf("neither rpm nor dpkg owns %s: %w", path, err)
	}
	pkg, _, _ := strings.Cut(strings.TrimSpace(string(owner)), ":")
	out, err := r.local(ctx, r.LocalTimeout, Cmd{Name: "dpkg-query", Args: []string{"-W", "-f", "${Version}", pkg}})
	if err != nil {
		return "", fmt.Errorf("reading the version of %s: %w", pkg, err)
	}
	version := strings.TrimSpace(string(out))
	if _, rest, ok := strings.Cut(version, ":"); ok {
		version = rest
	}
	version, _, _ = strings.Cut(version, "-")
	return version, nil
}

// hostLibrados is the package version of the host's librados2, which can
// differ from the release of the cluster, or unknown. rpm prints "package
// librados2 is not installed" to stdout, so a query's output counts only when
// it succeeds.
func (r *Runner) hostLibrados(ctx context.Context) string {
	if out, err := r.local(ctx, r.LocalTimeout, Cmd{Name: "rpm", Args: []string{"-q", "--qf", "%{VERSION}-%{RELEASE}", "librados2"}}); err == nil {
		return strings.TrimSpace(string(out))
	}
	out, err := r.local(ctx, r.LocalTimeout, Cmd{Name: "dpkg-query", Args: []string{"-W", "-f", "${Version}", "librados2"}})
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return v
	}
	return "unknown"
}

// memTotalKiB is /proc/meminfo's MemTotal.
func memTotalKiB(proc fs.FS) (int64, error) {
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

// cpuModel is /proc/cpuinfo's first model name, or empty where it has none.
func cpuModel(proc fs.FS) string {
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

// loadavg is /proc/loadavg's three load averages.
func loadavg(proc fs.FS) (string, error) {
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
