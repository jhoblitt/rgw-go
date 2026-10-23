package seam

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// Environment is a run's env.json: the host, the cluster and the settings
// it ran with, which hack/bench/report reads. The floor's fields and the
// sweep's settings are omitted from the limiter re-run's, which has neither,
// and the limiter's fields from the sweep's.
type Environment struct {
	Release           string `json:"release"`
	CephVersion       string `json:"ceph_version"`
	ClusterImage      string `json:"cluster_image"`
	FloorImage        string `json:"floor_image,omitzero"`
	FloorImageVersion string `json:"floor_image_version,omitzero"`
	LibradosVersion   string `json:"librados_version"`
	LibradosPath      string `json:"librados_path"`
	// FloorNoHints records that every floor write passes --no-hints.
	FloorNoHints  bool   `json:"floor_no_hints,omitzero"`
	Date          string `json:"date"`
	RooketVersion string `json:"rooket_version"`
	NProc         int    `json:"nproc"`
	MemTotalKiB   int64  `json:"mem_total_kib"`
	CPU           string `json:"cpu"`
	Kernel        string `json:"kernel"`
	GoVersion     string `json:"go_version"`
	HostLibrados  string `json:"host_librados"`
	GitDescribe   string `json:"git_describe"`
	GOMAXPROCS    string `json:"gomaxprocs"`
	BenchTime     string `json:"bench_time,omitzero"`
	FloorSeconds  int64  `json:"floor_seconds,omitzero"`
	Conc          string `json:"conc,omitzero"`
	// LargeConc is a pointer so that the sweep records an empty value, which
	// skipped the 4 MiB cells.
	LargeConc               *string `json:"large_conc,omitzero"`
	Write4mObjects          int64   `json:"write4m_objects,omitzero"`
	OverObjects             int64   `json:"over_objects"`
	ObjecterInflightOps     string  `json:"objecter_inflight_ops"`
	ObjecterInflightOpBytes string  `json:"objecter_inflight_op_bytes"`
	PoolMaxAvail            int64   `json:"pool_max_avail"`
	Load                    string  `json:"load"`
	Limited                 bool    `json:"limited,omitzero"`
	InflightBytes           int64   `json:"inflight_bytes,omitzero"`
	// The effective settings that name no measured quantity, and the config
	// file the settings were read from, when one was.
	Repo   string `json:"repo"`
	Rooket string `json:"rooket"`
	GoTags string `json:"go_tags"`
	Config string `json:"config,omitzero"`
}

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// environment collects the run's env.json, once the run knows the librados
// the benchmark links, the client release the cells ran, and the sweep its
// floor image.
func (r *Runner) environment(ctx context.Context, st *state, lib librados) (Environment, error) {
	cfg, cl := st.cfg, st.cl
	cephVersion, err := harness.PinnedVersion(cfg.Repo, cfg.Release)
	if err != nil {
		return Environment{}, err
	}
	memTotal, err := harness.MemTotalKiB(r.Proc)
	if err != nil {
		return Environment{}, err
	}
	load, err := harness.Loadavg(r.Proc)
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
		rooketVersion = r.firstLine(ctx, harness.Cmd{Name: cfg.Rooket, Args: []string{"version"}})
	}
	env := Environment{
		Release:                 cfg.Release,
		CephVersion:             cephVersion,
		ClusterImage:            st.clusterImage,
		LibradosVersion:         lib.version,
		LibradosPath:            lib.path,
		Date:                    r.Now().UTC().Format("2006-01-02T15:04:05Z"),
		RooketVersion:           rooketVersion,
		NProc:                   runtime.NumCPU(),
		MemTotalKiB:             memTotal,
		CPU:                     harness.CPUModel(r.Proc),
		Kernel:                  strings.TrimSpace(string(kernel)),
		GoVersion:               r.firstLine(ctx, harness.Cmd{Name: "go", Args: []string{"version"}}),
		HostLibrados:            r.hostLibrados(ctx),
		GitDescribe:             r.firstLine(ctx, harness.Cmd{Name: "git", Args: []string{"-C", cfg.Repo, "describe", "--always", "--dirty"}}),
		GOMAXPROCS:              cmp.Or(r.Getenv("GOMAXPROCS"), "unset"),
		OverObjects:             cfg.OverObjects,
		ObjecterInflightOps:     cl.objecter(ctx, "objecter_inflight_ops"),
		ObjecterInflightOpBytes: cl.objecter(ctx, "objecter_inflight_op_bytes"),
		PoolMaxAvail:            avail,
		Load:                    load,
		Repo:                    cfg.Repo,
		Rooket:                  cfg.Rooket,
		GoTags:                  cfg.GoTags,
		Config:                  cfg.ConfigFile,
	}
	if cfg.Limiter {
		env.Limited, env.InflightBytes = true, cfg.InflightBytes
		return env, nil
	}
	env.FloorImage, env.FloorImageVersion, env.FloorNoHints = st.floorImage, st.floorVersion, true
	env.BenchTime = seconds(cfg.BenchTime)
	env.FloorSeconds = cfg.FloorSeconds
	env.Conc = joinConcs(cfg.Conc)
	large := joinConcs(cfg.LargeConc)
	env.LargeConc = &large
	env.Write4mObjects = cfg.Write4mObjects
	return env, nil
}

// joinConcs is concs as CONC writes them.
func joinConcs(concs []int) string {
	parts := make([]string, len(concs))
	for i, n := range concs {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// librados is the library the benchmark binary links: its path and the Ceph
// version of the package that owns it.
type librados struct {
	path, version string
}

// firstLine is the first line of c's output, or empty when c fails.
func (r *Runner) firstLine(ctx context.Context, c harness.Cmd) string {
	return harness.FirstLine(ctx, r.Exec, r.LocalTimeout, c)
}

// linkedLibrados returns the librados bin links.
func (r *Runner) linkedLibrados(ctx context.Context, bin string) (librados, error) {
	out, err := r.local(ctx, r.LocalTimeout, harness.Cmd{Name: "ldd", Args: []string{bin}})
	if err != nil {
		return librados{}, fmt.Errorf("listing the libraries %s links: %w", bin, err)
	}
	var lib string
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == "librados.so.2" && f[1] == "=>" {
			lib = f[2]
			break
		}
	}
	if lib == "" {
		return librados{}, fmt.Errorf("%s links no librados.so.2", bin)
	}
	if lib, err = r.Resolve(lib); err != nil {
		return librados{}, fmt.Errorf("resolving %s: %w", lib, err)
	}
	version, err := r.packageVersion(ctx, lib)
	if err != nil {
		return librados{}, fmt.Errorf("cannot tell which Ceph release the librados %s links belongs to: %w", bin, err)
	}
	if !versionPattern.MatchString(version) {
		return librados{}, fmt.Errorf("the librados %s links, %s, is version %q, not X.Y.Z", bin, lib, version)
	}
	return librados{path: lib, version: version}, nil
}

// packageVersion is the upstream version of the rpm or deb package that owns
// path: a deb version's epoch and Debian revision are dropped.
func (r *Runner) packageVersion(ctx context.Context, path string) (string, error) {
	if out, err := r.local(ctx, r.LocalTimeout, harness.Cmd{Name: "rpm", Args: []string{"-qf", "--qf", "%{VERSION}", path}}); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	owner, err := r.local(ctx, r.LocalTimeout, harness.Cmd{Name: "dpkg", Args: []string{"-S", path}})
	if err != nil {
		return "", fmt.Errorf("neither rpm nor dpkg owns %s: %w", path, err)
	}
	pkg, _, _ := strings.Cut(strings.TrimSpace(string(owner)), ":")
	out, err := r.local(ctx, r.LocalTimeout, harness.Cmd{Name: "dpkg-query", Args: []string{"-W", "-f", "${Version}", pkg}})
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
	if out, err := r.local(ctx, r.LocalTimeout, harness.Cmd{Name: "rpm", Args: []string{"-q", "--qf", "%{VERSION}-%{RELEASE}", "librados2"}}); err == nil {
		return strings.TrimSpace(string(out))
	}
	out, err := r.local(ctx, r.LocalTimeout, harness.Cmd{Name: "dpkg-query", Args: []string{"-W", "-f", "${Version}", "librados2"}})
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return v
	}
	return "unknown"
}
