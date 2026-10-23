package clusterconf_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/internal/harness/harnessfakes"
	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/clusterconf"
)

const (
	radosgwEntity = "client.rgw.ceph.objectstore.a"
	rgwGoEntity   = "client.rgw.rgw-go"
	lcOption      = "rgw_enable_lc_threads"
	site          = "ceph-objectstore"
	placement     = "default-placement"
	dataPool      = "ceph-objectstore.rgw.buckets.data"
	frontends     = "beast port=80 tcp_nodelay=1"
)

// Prefixes of the commands the world tells apart.
var (
	toolboxCeph  = []string{"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--", "ceph", "--connect-timeout=20", "--rados-mon-op-timeout=20"}
	toolboxAdmin = []string{"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--", "radosgw-admin"}
	operator     = []string{"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-operator", "--", "radosgw-admin"}
	siteArgs     = []string{"--rgw-realm=" + site, "--rgw-zonegroup=" + site, "--rgw-zone=" + site}
	operatorArgs = []string{
		"--cluster=rook-ceph", "--conf=/var/lib/rook/rook-ceph/rook-ceph.config",
		"--name=client.admin", "--keyring=/var/lib/rook/rook-ceph/client.admin.keyring",
	}
)

// instance is a radosgw daemon as the service map lists it, and the
// rgw_enable_lc_threads it read when it built its store.
type instance struct {
	gid     int64
	started time.Time
	lc      string
}

// world is a fake Squid cluster with one radosgw and an rgw-go on the host,
// answering the commands Apply runs as the cluster would. Its clock moves
// only with the Configurer's sleeps and the changes it makes.
type world struct {
	exec  *harnessfakes.FakeExecer
	root  string
	proc  fstest.MapFS
	clock time.Time
	// events are the commands run, and the sleeps, in their order.
	events []string

	// radosgws are the instances the service map lists; a restart or a
	// reload lists the old beside the new for lingering dumps.
	radosgws  []instance
	lingering int
	nextGID   int64
	// mons are each entity's rgw_enable_lc_threads in the mons' store, and
	// log their config log.
	mons map[string]string
	log  []map[string]any

	// rgwConfig is the object store's rgwConfig option; generation and
	// observed its metadata.generation and status.observedGeneration. Rook
	// reconciles a patch at the reconcileAfter'th read of the object store,
	// and never when stuck.
	rgwConfig            string
	generation, observed int64
	pending, stuck       bool
	reads                int
	reconcileAfter       int

	// The zonegroup's and the current period's classes of the default
	// placement, and the zone's, each with its codec, empty for none.
	zonegroupClasses, periodClasses []string
	zoneClasses                     map[string]string
	// noReload keeps the radosgw from reloading a committed period,
	// commitDrops a commit from carrying the classes, dumpErrAfterCommit
	// fails every service dump once a period is committed, and
	// noDefaultPlacement leaves the zonegroup without one. zonePools names a
	// zone class's pool where it is not STANDARD's.
	noReload, commitDrops, noDefaultPlacement bool
	dumpErrAfterCommit, dumpErr               error
	zonePools                                 map[string]string

	// rgwGoPid is the running rgw-go's, empty when none runs, and
	// rgwGoFlags the serve flags it was started with.
	rgwGoPid   string
	rgwGoFlags []string
	nextPid    int
}

// newWorld is a cluster in the parity mode: neither worker runs, the classes
// are in place, and the radosgw started after its options were last set.
func newWorld() *world {
	w := &world{
		exec:             &harnessfakes.FakeExecer{},
		root:             GinkgoT().TempDir(),
		proc:             fstest.MapFS{},
		clock:            time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
		nextGID:          4100,
		mons:             map[string]string{radosgwEntity: "false", rgwGoEntity: "false"},
		rgwConfig:        "false",
		generation:       1,
		observed:         1,
		reconcileAfter:   2,
		zonegroupClasses: []string{"COMP_LZ4", "FROZEN", "LUKEWARM", "STANDARD"},
		periodClasses:    []string{"COMP_LZ4", "FROZEN", "LUKEWARM", "STANDARD"},
		zoneClasses:      map[string]string{"STANDARD": "", "COMP_LZ4": "lz4", "LUKEWARM": "", "FROZEN": ""},
		nextPid:          700,
	}
	w.change(radosgwEntity, "false", -2*time.Hour)
	w.change(rgwGoEntity, "false", -2*time.Hour)
	w.radosgws = []instance{{gid: w.gid(), started: w.clock.Add(-time.Hour), lc: "false"}}
	w.startRgwGo()
	w.exec.OutputStub = w.output
	return w
}

func (w *world) gid() int64 {
	w.nextGID++
	return w.nextGID
}

// change logs entity's option set to value at offset from now.
func (w *world) change(entity, value string, offset time.Duration) {
	w.mons[entity] = value
	w.log = append(w.log, map[string]any{
		"version": len(w.log) + 1, "timestamp": stamp(w.clock.Add(offset)), "name": "",
		"changes": []map[string]string{{"name": entity + "/" + lcOption, "new_value": value}},
	})
}

// stamp is t as the mons and the manager print a utime_t in Ceph's images.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000") + "+0000" }

// startRgwGo starts an rgw-go with the serve flags given, recording its pid
// and its flags, one a line, as rgw-go-up.sh does.
func (w *world) startRgwGo(flags ...string) {
	w.nextPid++
	w.rgwGoPid = strconv.Itoa(w.nextPid)
	Expect(os.MkdirAll(w.out(), 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(w.out(), "rgw-go.pid"), []byte(w.rgwGoPid+"\n"), 0o600)).To(Succeed())
	var lines strings.Builder
	for _, f := range flags {
		lines.WriteString(f + "\n")
	}
	Expect(os.WriteFile(filepath.Join(w.out(), "rgw-go.flags"), []byte(lines.String()), 0o600)).To(Succeed())
	w.rgwGoFlags = flags
	w.proc[w.rgwGoPid+"/comm"] = &fstest.MapFile{Data: []byte("rgw-go\n")}
}

// out is the release's rooket output.
func (w *world) out() string { return filepath.Join(w.root, "hack", "rooket", "out", "squid") }

// stopRgwGo leaves rgw-go's pid file naming a process that has gone.
func (w *world) stopRgwGo() {
	delete(w.proc, w.rgwGoPid+"/comm")
	w.rgwGoPid = ""
}

// restart replaces the radosgw with one that reads the mons' option now,
// listing the old one beside it for one dump.
func (w *world) restart() {
	w.clock = w.clock.Add(time.Second)
	w.radosgws = append(w.radosgws, instance{gid: w.gid(), started: w.clock, lc: w.mons[radosgwEntity]})
	w.lingering = 1
}

func (w *world) configurer() *clusterconf.Configurer {
	return &clusterconf.Configurer{
		Cluster:  harness.Cluster{Exec: w.exec, Rooket: "rooket", Release: "squid", CallTimeout: time.Minute},
		Root:     w.root,
		Make:     "make",
		Proc:     w.proc,
		Now:      func() time.Time { return w.clock },
		Sleep:    w.sleep,
		SiteWait: 3 * time.Minute, SitePoll: 5 * time.Second, RookWait: 5 * time.Minute, RestartTimeout: 15 * time.Minute,
		Out: GinkgoWriter,
	}
}

func (w *world) sleep(ctx context.Context, d time.Duration) error {
	w.events = append(w.events, "sleep "+d.String())
	w.clock = w.clock.Add(d)
	return ctx.Err()
}

// changes are the events that change the cluster, in their order.
func (w *world) changes() []string {
	var out []string
	for _, e := range w.events {
		for _, p := range []string{
			"kubectl -n rook-ceph patch", "ceph config set", "kubectl -n rook-ceph rollout restart",
			"operator", "RGW_GO_LC_THREADS=",
		} {
			if strings.HasPrefix(e, p) {
				out = append(out, e)
			}
		}
	}
	return out
}

// output answers c as the cluster would, recording it as an event.
func (w *world) output(_ context.Context, c harness.Cmd) ([]byte, error) {
	if c.Name == "make" {
		w.events = append(w.events, strings.Join(append(slices.Clone(c.Env), c.String()), " "))
		return nil, w.makeUp(c)
	}
	Expect(c.Name).To(Equal("rooket"))
	Expect(c.Env).To(Equal([]string{"ROOKET_NAME=rgw-go-squid"}))
	args := c.Args
	switch {
	case hasPrefix(args, toolboxCeph):
		args = args[len(toolboxCeph):]
		w.events = append(w.events, "ceph "+strings.Join(args, " "))
		return w.ceph(args)
	case hasPrefix(args, toolboxAdmin):
		args = args[len(toolboxAdmin):]
		Expect(args).To(HaveLen(2 + len(siteArgs)))
		Expect(args[2:]).To(Equal(siteArgs))
		w.events = append(w.events, "radosgw-admin "+strings.Join(args[:2], " "))
		return w.admin(args[:2])
	case hasPrefix(args, operator):
		args = args[len(operator):]
		Expect(args[len(args)-len(operatorArgs)-len(siteArgs):]).To(Equal(slices.Concat(siteArgs, operatorArgs)))
		args = args[:len(args)-len(operatorArgs)-len(siteArgs)]
		w.events = append(w.events, "operator "+strings.Join(args, " "))
		return nil, w.operator(args)
	default:
		Expect(args[:2]).To(Equal([]string{"kubectl", "-n"}))
		w.events = append(w.events, strings.Join(args, " "))
		return w.kubectl(args[2:])
	}
}

func hasPrefix(args, prefix []string) bool {
	return len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix)
}

func (w *world) ceph(args []string) ([]byte, error) {
	line := strings.Join(args, " ")
	switch {
	case line == "service dump -f json":
		if w.dumpErr != nil {
			return nil, w.dumpErr
		}
		return w.serviceDump(), nil
	case line == "config show "+radosgwEntity+" rgw_frontends":
		return []byte(frontends + "\n"), nil
	case line == "config show "+radosgwEntity+" "+lcOption:
		return []byte(w.radosgws[len(w.radosgws)-1].lc + "\n"), nil
	case strings.HasPrefix(line, "config get "):
		return []byte(w.mons[args[2]] + "\n"), nil
	case strings.HasPrefix(line, "config set "):
		w.clock = w.clock.Add(time.Second)
		w.change(args[2], args[4], 0)
		return nil, nil
	case line == "config log 1000 -f json":
		return json.Marshal(w.log)
	}
	return nil, fmt.Errorf("the world has no answer to ceph %s", line)
}

func (w *world) serviceDump() []byte {
	daemons := map[string]any{"summary": ""}
	listed := w.radosgws[len(w.radosgws)-1:]
	if w.lingering > 0 && len(w.radosgws) > 1 {
		listed = w.radosgws[len(w.radosgws)-2:]
		w.lingering--
	}
	for _, r := range listed {
		daemons[strconv.FormatInt(r.gid, 10)] = map[string]any{
			"gid": r.gid, "start_stamp": stamp(r.started),
			"metadata": map[string]string{
				"id": "ceph.objectstore.a", "realm_name": site, "zonegroup_name": site, "zone_name": site,
				"frontend_config#0": frontends,
			},
		}
	}
	data, err := json.Marshal(map[string]any{"services": map[string]any{"rgw": map[string]any{"daemons": daemons}}})
	Expect(err).NotTo(HaveOccurred())
	return data
}

func (w *world) kubectl(args []string) ([]byte, error) {
	line := strings.Join(args, " ")
	switch {
	case line == "rook-ceph get cephobjectstore ceph-objectstore -o json":
		w.reads++
		if w.pending && !w.stuck && w.reads >= w.reconcileAfter {
			w.pending = false
			w.observed = w.generation
			if w.mons[radosgwEntity] != w.rgwConfig {
				w.clock = w.clock.Add(time.Second)
				w.change(radosgwEntity, w.rgwConfig, 0)
			}
		}
		return json.Marshal(map[string]any{
			"metadata": map[string]any{"name": "ceph-objectstore", "generation": w.generation},
			"spec": map[string]any{"gateway": map[string]any{"rgwConfig": map[string]string{
				"rgw_dynamic_resharding": "false", lcOption: w.rgwConfig,
			}}},
			"status": map[string]any{"observedGeneration": w.observed, "phase": "Ready", "message": "reconciling"},
		})
	case strings.HasPrefix(line, "rook-ceph patch cephobjectstore ceph-objectstore --type merge -p "):
		var patch struct {
			Spec struct {
				Gateway struct {
					RgwConfig map[string]string `json:"rgwConfig"`
				} `json:"gateway"`
			} `json:"spec"`
		}
		Expect(json.Unmarshal([]byte(args[len(args)-1]), &patch)).To(Succeed())
		Expect(patch.Spec.Gateway.RgwConfig).To(HaveLen(1), "the patch changes the one option")
		w.rgwConfig = patch.Spec.Gateway.RgwConfig[lcOption]
		w.generation++
		w.pending, w.reads = true, 0
		return nil, nil
	case line == "rook-ceph rollout restart deploy -l app=rook-ceph-rgw":
		w.restart()
		return nil, nil
	case line == "rook-ceph rollout status deploy -l app=rook-ceph-rgw --timeout=5m":
		return nil, nil
	}
	return nil, fmt.Errorf("the world has no answer to kubectl %s", line)
}

func (w *world) admin(args []string) ([]byte, error) {
	target := func(classes []string) map[string]any {
		def := placement
		if w.noDefaultPlacement {
			def = ""
		}
		return map[string]any{
			"name": site, "default_placement": def,
			"placement_targets": []map[string]any{{"name": placement, "storage_classes": classes}},
		}
	}
	switch strings.Join(args, " ") {
	case "zonegroup get":
		return json.Marshal(target(w.zonegroupClasses))
	case "period get":
		return json.Marshal(map[string]any{"period_map": map[string]any{"zonegroups": []any{target(w.periodClasses)}}})
	case "zone get":
		classes := map[string]any{}
		for class, codec := range w.zoneClasses {
			c := map[string]string{"data_pool": dataPool}
			if pool, ok := w.zonePools[class]; ok {
				c["data_pool"] = pool
			}
			if codec != "" {
				c["compression_type"] = codec
			}
			classes[class] = c
		}
		return json.Marshal(map[string]any{"placement_pools": []any{
			map[string]any{"key": placement, "val": map[string]any{"storage_classes": classes}},
		}})
	}
	return nil, fmt.Errorf("the world has no answer to radosgw-admin %s", strings.Join(args, " "))
}

func (w *world) operator(args []string) error {
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "zonegroup placement add --placement-id "+placement+" --storage-class "):
		w.zonegroupClasses = append(w.zonegroupClasses, args[len(args)-1])
		return nil
	case strings.HasPrefix(line, "zone placement add --placement-id "+placement+" --storage-class "):
		Expect(args[len(args)-2:]).To(Equal([]string{"--data-pool", dataPool}))
		if !slices.Contains(w.zonegroupClasses, args[6]) {
			return errors.New("the zonegroup's placement target lists no such class")
		}
		w.zoneClasses[args[6]] = ""
		return nil
	case line == "period update --commit":
		if !w.commitDrops {
			w.periodClasses = slices.Clone(w.zonegroupClasses)
		}
		w.dumpErr = w.dumpErrAfterCommit
		if !w.noReload {
			w.restart()
		}
		return nil
	}
	return fmt.Errorf("the world has no answer to the operator's radosgw-admin %s", line)
}

// makeUp is make rgw-go-up: rgw-go-up.sh stops rgw-go, sets its entity's
// option from RGW_GO_LC_THREADS, and starts it.
func (w *world) makeUp(c harness.Cmd) error {
	Expect(c.Args).To(HaveLen(6))
	Expect(c.Args[:5]).To(Equal([]string{"-C", w.root, "rgw-go-up", "RELEASE=squid", "ROOKET=rooket"}))
	given, ok := strings.CutPrefix(c.Args[5], "RGW_GO_FLAGS=")
	Expect(ok).To(BeTrue(), "make rgw-go-up is given RGW_GO_FLAGS")
	Expect(c.Env).To(HaveLen(1))
	value, ok := strings.CutPrefix(c.Env[0], "RGW_GO_LC_THREADS=")
	Expect(ok).To(BeTrue(), "make rgw-go-up is given RGW_GO_LC_THREADS")
	if w.rgwGoPid != "" {
		w.stopRgwGo()
	}
	w.clock = w.clock.Add(time.Second)
	if w.mons[rgwGoEntity] != value {
		w.change(rgwGoEntity, value, 0)
	}
	w.startRgwGo(strings.Fields(given)...)
	return nil
}

// apply runs Apply for mode against the world.
func (w *world) apply(ctx context.Context, mode clusterconf.Mode) (clusterconf.State, error) {
	return w.configurer().Apply(ctx, mode)
}

// trace is the world's events, a line each, for a failure message.
func (w *world) trace() string {
	var b bytes.Buffer
	for _, e := range w.events {
		b.WriteString(e + "\n")
	}
	return b.String()
}
