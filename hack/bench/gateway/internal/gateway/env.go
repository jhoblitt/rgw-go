package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
)

// ordered is a JSON object whose keys keep their order, as jq writes one.
type ordered []struct {
	key   string
	value any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(kv.key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(kv.value)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// with is o and key, last.
func (o ordered) with(key string, value any) ordered {
	return append(o, struct {
		key   string
		value any
	}{key, value})
}

// entityConfig is one entity's parity options in config.json, and the
// command that read them.
type entityConfig struct {
	Source  string  `json:"source"`
	Options ordered `json:"options"`
}

// writeConfig writes config.json: every parity option as each gateway's
// entity holds it, the radosgw's as config show reads what the running
// radosgw uses, rgw-go's as config get. A value that cannot be read is
// recorded empty.
func (r *Runner) writeConfig(ctx context.Context, st *state) error {
	config := map[string]entityConfig{}
	for _, entity := range []string{st.radosgwEntity, rgwGoEntity} {
		mode := "get"
		if entity == st.radosgwEntity {
			mode = "show"
		}
		ec := entityConfig{Source: "config " + mode}
		for _, o := range st.parity {
			out, err := st.cl.Ceph(ctx, "config", mode, entity, o.name)
			if err != nil {
				st.log.WarnContext(ctx, "cannot read a parity option", slog.String("entity", entity), slog.String("option", o.name),
					slog.Any("error", err))
			}
			ec.Options = ec.Options.with(o.name, strings.TrimRight(string(out), "\n"))
		}
		config[entity] = ec
	}
	return harness.WriteJSON(filepath.Join(st.dir, "config.json"), config)
}

// Environment is a run's env.json: the host, the cluster, the gateway and the
// settings it ran with, which hack/bench/report reads and compares across
// runs. Its keys are in the order gateway comparisons have always written
// them.
type Environment struct {
	Release          string  `json:"release"`
	Gateway          string  `json:"gateway"`
	CephVersion      string  `json:"ceph_version"`
	ClusterImage     string  `json:"cluster_image"`
	Endpoint         string  `json:"endpoint"`
	APIName          string  `json:"api_name"`
	Frontends        string  `json:"frontends"`
	TCPNoDelay       string  `json:"tcp_nodelay"`
	PoolMaxAvail     int64   `json:"pool_max_avail"`
	Datasets         ordered `json:"datasets"`
	Sampled          string  `json:"sampled"`
	RadosgwEntity    string  `json:"radosgw_entity"`
	RgwGoFlags       string  `json:"rgw_go_flags"`
	GODEBUG          string  `json:"godebug"`
	GOMAXPROCS       string  `json:"gomaxprocs"`
	Date             string  `json:"date"`
	Smoke            string  `json:"smoke"`
	Seconds          int64   `json:"seconds"`
	Conc             string  `json:"conc"`
	PutObjects       int64   `json:"put_objects"`
	GetObjects       int64   `json:"get_objects"`
	LargeThreads     int64   `json:"large_threads"`
	LargeObjects     int64   `json:"large_objects"`
	ListThreads      int64   `json:"list_threads"`
	ListObjects      int64   `json:"list_objects"`
	Elbencho         string  `json:"elbencho"`
	ElbenchoCommit   string  `json:"elbencho_commit"`
	ElbenchoImage    string  `json:"elbencho_image"`
	ElbenchoPackages string  `json:"elbencho_packages"`
	RooketVersion    string  `json:"rooket_version"`
	NProc            int     `json:"nproc"`
	MemTotalKiB      int64   `json:"mem_total_kib"`
	CPU              string  `json:"cpu"`
	Kernel           string  `json:"kernel"`
	GoVersion        string  `json:"go_version"`
	GitDescribe      string  `json:"git_describe"`
	ClkTck           int     `json:"clk_tck"`
	Load             string  `json:"load"`
	// The effective settings that name no measured quantity, and the config
	// file the settings were read from, when one was.
	Repo   string `json:"repo"`
	Rooket string `json:"rooket"`
	GoTags string `json:"go_tags"`
	Config string `json:"config,omitzero"`
}

// writeEnvironment writes env.json.
func (r *Runner) writeEnvironment(ctx context.Context, st *state) error {
	cfg := st.cfg
	memTotal, err := harness.MemTotalKiB(r.Proc)
	if err != nil {
		return err
	}
	load, err := harness.Loadavg(r.Proc)
	if err != nil {
		return err
	}
	kernel, err := fs.ReadFile(r.Proc, "sys/kernel/osrelease")
	if err != nil {
		return fmt.Errorf("reading the kernel release: %w", err)
	}
	var datasets ordered
	for _, p := range cfg.Profiles {
		datasets = datasets.with(p, cfg.Dataset(p))
	}
	env := Environment{
		Release:       cfg.Release,
		Gateway:       cfg.Gateway,
		CephVersion:   st.cephVersion,
		ClusterImage:  st.clusterImage,
		Endpoint:      st.endpoint,
		APIName:       st.apiName,
		Frontends:     st.frontends,
		TCPNoDelay:    st.tcpNoDelay,
		PoolMaxAvail:  st.idle,
		Datasets:      datasets,
		Sampled:       st.target,
		RadosgwEntity: st.radosgwEntity,
		RgwGoFlags:    cfg.RgwGoFlags,
		GOMAXPROCS:    cmp.Or(r.Getenv("GOMAXPROCS"), "unset"),
		Date:          r.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Smoke:         "0",
		Seconds:       cfg.Seconds,
		Conc:          cfg.concWords(),
		PutObjects:    cfg.PutObjects,
		GetObjects:    cfg.GetObjects,
		LargeThreads:  cfg.LargeThreads,
		LargeObjects:  cfg.LargeObjects,
		ListThreads:   cfg.ListThreads,
		ListObjects:   cfg.ListObjects,
		NProc:         runtime.NumCPU(),
		MemTotalKiB:   memTotal,
		CPU:           harness.CPUModel(r.Proc),
		Kernel:        strings.TrimSpace(string(kernel)),
		ClkTck:        harness.UserHZ,
		Load:          load,
		Repo:          cfg.Repo,
		Rooket:        cfg.Rooket,
		GoTags:        cfg.GoTags,
		Config:        cfg.ConfigFile,
	}
	if cfg.Gateway == "rgw-go" {
		env.GODEBUG = "gctrace=1"
	}
	if cfg.Smoke {
		env.Smoke = "1"
	}
	// These run in the order the command goldens pin.
	env.Elbencho = r.elbenchoVersion(ctx, st)
	env.ElbenchoCommit, env.ElbenchoImage, env.ElbenchoPackages = elbenchoStamp(cfg)
	env.RooketVersion = r.Getenv("ROOKET_VERSION")
	if env.RooketVersion == "" {
		env.RooketVersion = harness.FirstLine(ctx, r.Exec, r.LocalTimeout,
			harness.Cmd{Name: cfg.Rooket, Args: []string{"version"}, Env: st.cl.Env()})
	}
	env.GoVersion = harness.FirstLine(ctx, r.Exec, r.LocalTimeout, harness.Cmd{Name: "go", Args: []string{"version"}})
	env.GitDescribe = harness.FirstLine(ctx, r.Exec, r.LocalTimeout,
		harness.Cmd{Name: "git", Args: []string{"-C", cfg.Repo, "describe", "--always", "--dirty"}})
	return harness.WriteJSON(filepath.Join(st.dir, "env.json"), env)
}

// elbenchoVersion is the version elbencho --version prints, on its line
// whose second word is Version:, or empty.
func (r *Runner) elbenchoVersion(ctx context.Context, st *state) string {
	out, err := harness.Output(ctx, r.Exec, r.LocalTimeout, harness.Cmd{Name: st.elbencho, Args: []string{"--version"}})
	if err != nil {
		return ""
	}
	var versions []string
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) >= 3 && f[1] == "Version:" {
			versions = append(versions, f[2])
		}
	}
	return strings.Join(versions, "\n")
}

// elbenchoStamp is what hack/bench/out/elbencho.commit, elbencho-build.sh's
// stamp, says of the binary: the commit and the image it was built from, on
// its first line, and the packages it was built with, its second; empty
// where the stamp lacks them.
func elbenchoStamp(cfg Config) (commit, image, packages string) {
	data, err := os.ReadFile(filepath.Join(cfg.Repo, "hack", "bench", "out", "elbencho.commit"))
	if err != nil {
		return "", "", ""
	}
	lines := strings.Split(string(data), "\n")
	if f := strings.Fields(lines[0]); len(f) >= 3 {
		commit, image = f[0], f[2]
	} else if len(f) >= 1 {
		commit = f[0]
	}
	if len(lines) >= 2 {
		packages = lines[1]
	}
	return commit, image, packages
}
