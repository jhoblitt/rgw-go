package gateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness/harnessfakes"
)

const (
	// runStart names the run directory, gateway-<gateway>-20261013T071145Z.
	release       = "tentacle"
	pod           = "rook-ceph-rgw-ceph-objectstore-a-6d9f8b7c5-x2x4z"
	radosgwEntity = "client.rgw.ceph.objectstore.a"
	dataPool      = "ceph-objectstore.rgw.buckets.data"
	// podPid is the radosgw's pid in its pod, and rgwGoPid the rgw-go's on the
	// host.
	podPid   = "4321"
	rgwGoPid = "777"
	// free is what ceph df says the data pool can take, the least an idle
	// cluster could in the seam sweeps.
	free = 8220119040
)

var (
	runStart = time.Date(2026, 10, 13, 7, 11, 45, 0, time.UTC)
	// toolboxArgs is how every toolbox call starts.
	toolboxArgs = []string{"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--"}
	// parity are the parity options at their parity values, as the chart
	// values hold them.
	parity = map[string]string{
		"rgw_dynamic_resharding": "false", "rgw_enable_gc_threads": "false", "rgw_enable_lc_threads": "false",
		"rgw_run_sync_thread": "false", "rgw_d3n_l1_local_datacache_enabled": "false", "rgw_enable_usage_log": "true",
		"rgw_usage_log_tick_interval": "1", "rgw_usage_log_flush_threshold": "1",
	}
)

// world is a fake host and Tentacle cluster with one radosgw, answering as
// the stubs the command goldens were traced with did: a repository holding
// the release's rooket output, chart values and elbencho stamp; the commands'
// answers; /proc; and rgw-go's metrics address.
type world struct {
	gateway string
	repo    string
	exec    *harnessfakes.FakeExecer
	proc    fstest.MapFS
	stdout  *bytes.Buffer
	metrics *httptest.Server

	mu sync.Mutex
	// clock is the world's time, which only the Runner's sleeps move, so that
	// a wait that never ends fails at its deadline rather than spinning.
	clock time.Time
	// events are the commands run, but the sampler's, and the sleeps, in the
	// order they ran, as the command goldens write them.
	events []string
	// probe, when set, has every event record the background state beside it
	// in states, and every read of the gateway's process or request of its
	// CPU profile outside that state recorded in strays.
	probe  bool
	states []background
	strays []string
	// sampled counts the sampler's reads of the radosgw pod.
	sampled int
	// profiles are the CPU profiles' requests, and holdProfile holds each
	// until its client gives it up.
	profiles    []string
	holdProfile bool
	user        bool
	buckets     map[string]bool
	// avail is what each ceph df reports in turn, the last from then on.
	avail []int64
	// radosgws is how many radosgw entries each service dump lists in turn,
	// and queued how many entries each gc list does, the last from then on.
	radosgws []int
	queued   []int
	// frontends are the radosgw's, as the service map and config show give
	// them.
	frontends string
	// options answers config show and get by entity and option, where it
	// differs from parity.
	options map[string]string
	// failWith answers the commands whose line contains a key with its error.
	failWith map[string]error
	// gc is rgw-go's next GC cycle.
	gc int
}

func newWorld(gw string) *world {
	w := &world{
		gateway:   gw,
		repo:      GinkgoT().TempDir(),
		exec:      &harnessfakes.FakeExecer{},
		stdout:    &bytes.Buffer{},
		buckets:   map[string]bool{},
		clock:     runStart,
		avail:     []int64{free},
		radosgws:  []int{1},
		queued:    []int{0},
		frontends: "beast port=80 tcp_nodelay=1",
		options:   map[string]string{},
		failWith:  map[string]error{},
		proc: fstest.MapFS{
			"meminfo":              {Data: []byte("MemTotal:       65007268 kB\nMemFree:        12345678 kB\n")},
			"cpuinfo":              {Data: []byte("processor\t: 0\nmodel name\t: AMD Ryzen 9 7950X3D 16-Core Processor\n")},
			"loadavg":              {Data: []byte("0.69 0.74 1.03 2/1203 98765\n")},
			"sys/kernel/osrelease": {Data: []byte("7.2.8-100.fc43.x86_64\n")},
			rgwGoPid + "/comm":     {Data: []byte("rgw-go\n")},
			rgwGoPid + "/cmdline": {Data: []byte(strings.Join([]string{
				"/repo/bin/rgw-go", "serve", "--metrics-addr=127.0.0.1:9481", "--", "--id", "rgw.rgw-go",
				"--rgw-frontends=beast endpoint=127.0.0.1:7481", "",
			}, "\x00"))},
			rgwGoPid + "/stat":   {Data: []byte(procStat("rgw-go", 2216, 543))},
			rgwGoPid + "/status": {Data: []byte(procStatus(21, 156504))},
		},
	}
	out := w.out()
	Expect(os.MkdirAll(out, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(out, "ceph.conf"), []byte("[global]\n"), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(out, "image"), []byte("quay.io/ceph/ceph:v20.2.4\n"), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(out, "manifest.json"), []byte(`{"realm":"ceph-objectstore","zonegroup":"ceph-objectstore",`+
		`"zone":"ceph-objectstore","pools":{"data":"`+dataPool+`","index":"ceph-objectstore.rgw.buckets.index"}}`), 0o600)).To(Succeed())
	// The release's own chart values, whose rgwConfig are the parity options.
	values, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "rooket", release, "values", "rook-ceph-cluster.yaml"))
	Expect(err).NotTo(HaveOccurred())
	w.writeValues(values)
	Expect(os.MkdirAll(filepath.Join(w.repo, "hack", "bench", "out"), 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(w.repo, "hack", "bench", "out", "elbencho.commit"),
		[]byte("0637a922cbb71d56625f553750dbfbf12e5f0167 3.0-25 docker.io/library/alpine:3.24.2@sha256:294b\n"+
			"gcc-15.2.0-r2 musl-1.2.5-r10\n"), 0o600)).To(Succeed())
	profile, err := os.ReadFile(filepath.Join("testdata", "cpu.pprof"))
	Expect(err).NotTo(HaveOccurred())
	w.metrics = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		w.mu.Lock()
		w.profiles = append(w.profiles, req.URL.Path+"?"+req.URL.RawQuery)
		hold := w.holdProfile
		w.stray("a CPU profile request", func(b background) bool { return b.profiling })
		w.mu.Unlock()
		if hold {
			<-req.Context().Done()
			return
		}
		// The handler's goroutine is no spec's, so a client gone early is
		// noted rather than failed.
		if _, err := rw.Write(profile); err != nil {
			GinkgoWriter.Printf("writing a CPU profile: %v\n", err)
		}
	}))
	DeferCleanup(w.metrics.Close)
	w.exec.OutputStub = w.output
	return w
}

// procStat is a /proc/<pid>/stat line of a process named comm that has used
// utime and stime clock ticks.
func procStat(comm string, utime, stime int) string {
	return fmt.Sprintf("1 (%s) S 1 1 1 0 -1 4194560 12345 0 0 0 %d %d 0 0 20 0 21 0 123456 0 0\n", comm, utime, stime)
}

// procStatus is a /proc/<pid>/status with threads and rss KiB resident.
func procStatus(threads, rss int) string {
	return fmt.Sprintf("Name:\tproc\nState:\tS (sleeping)\nVmRSS:\t%d kB\nThreads:\t%d\n", rss, threads)
}

// out is the release's rooket output.
func (w *world) out() string {
	return filepath.Join(w.repo, "hack", "rooket", "out", release)
}

// writeValues writes the release's chart values.
func (w *world) writeValues(values []byte) {
	dir := filepath.Join(w.repo, "hack", "rooket", release, "values")
	Expect(os.MkdirAll(dir, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "rook-ceph-cluster.yaml"), values, 0o600)).To(Succeed())
}

// dir is the run's directory.
func (w *world) dir() string {
	return filepath.Join(w.repo, "hack", "bench", "out", release, "gateway-"+w.gateway+"-20261013T071145Z")
}

// line is a command as the goldens write it: the environment the run gives
// it, then its command line, the run directory written DIR and the
// repository REPO.
func (w *world) line(c harness.Cmd) string {
	line := strings.Join(append(slices.Clone(c.Env), c.String()), " ")
	return strings.ReplaceAll(strings.ReplaceAll(line, w.dir(), "DIR"), w.repo, "REPO")
}

func (w *world) record(event string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
	if w.probe {
		w.states = append(w.states, w.background())
	}
}

// now is the world's time.
func (w *world) now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.clock
}

// sleep stands in for the Runner's sleeps, recording each as sleep <seconds>
// and moving the clock past it.
func (w *world) sleep(ctx context.Context, d time.Duration) error {
	w.record(fmt.Sprintf("sleep %d", d/time.Second))
	w.mu.Lock()
	w.clock = w.clock.Add(d)
	w.mu.Unlock()
	return context.Cause(ctx)
}

// background is whether the sampler and a CPU profile's fetch run, as the
// run's own log records their starts and ends.
type background struct {
	sampling, profiling bool
}

// background reads the background state from run.log: a sampler runs from
// its "sampling" record to its "sampled" one, which the sampler writes as it
// ends, and a fetch from "taking the CPU profile" to "took the CPU profile".
func (w *world) background() background {
	data, err := os.ReadFile(filepath.Join(w.dir(), "run.log"))
	if err != nil {
		return background{}
	}
	log := string(data)
	return background{
		sampling:  strings.Count(log, `"msg":"sampling"`) > strings.Count(log, `"msg":"sampled"`),
		profiling: strings.Count(log, `"msg":"taking the CPU profile"`) > strings.Count(log, `"msg":"took the CPU profile"`),
	}
}

// stray records what happened, when probing, unless the background state
// allows it. The caller holds mu.
func (w *world) stray(what string, allowed func(background) bool) {
	if w.probe && !allowed(w.background()) {
		w.strays = append(w.strays, fmt.Sprintf("%s after event %d", what, len(w.events)))
	}
}

// watchedFS is the world's /proc, which reports each read of rgw-go's stat,
// the sampler's of the host's process.
type watchedFS struct {
	fstest.MapFS
	w *world
}

func (f watchedFS) Open(name string) (fs.File, error) {
	f.saw(name)
	return f.MapFS.Open(name)
}

func (f watchedFS) ReadFile(name string) ([]byte, error) {
	f.saw(name)
	return f.MapFS.ReadFile(name)
}

func (f watchedFS) saw(name string) {
	if name != rgwGoPid+"/stat" {
		return
	}
	f.w.mu.Lock()
	defer f.w.mu.Unlock()
	f.w.stray("a read of rgw-go's process", func(b background) bool { return b.sampling })
}

// sampler reports whether c is one of the sampler's reads of the radosgw pod,
// which run beside the rest.
func sampler(c harness.Cmd) bool {
	return slices.Contains(c.Args, pod) && slices.Contains(c.Args, "-c")
}

// output answers a command as the host and the cluster would.
func (w *world) output(_ context.Context, c harness.Cmd) ([]byte, error) {
	if !sampler(c) {
		w.record(w.line(c))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	line := c.String()
	for key, err := range w.failWith {
		if strings.Contains(line, key) {
			return nil, err
		}
	}
	args := c.Args
	if c.Name == "rooket" && slices.Equal(args[:min(len(args), len(toolboxArgs))], toolboxArgs) {
		args = args[len(toolboxArgs):]
	}
	switch {
	case c.Name == "rooket" && args[0] == "version":
		return []byte("rooket v0.0.0-20260928053658-3d8551957e08\nbuilt by go1.27.1\n"), nil
	case c.Name == "rooket" && args[0] == "kubectl":
		return w.kubectl(args[1:])
	case c.Name == "rooket" && args[0] == "ceph":
		return w.ceph(args)
	case c.Name == "rooket" && args[0] == "radosgw-admin":
		return w.admin(args[1 : len(args)-3])
	case c.Name == "go" && args[0] == "version":
		return []byte("go version go1.27.1 linux/amd64\n"), nil
	case c.Name == "go" && slices.Contains(args, "run"):
		return []byte(filepath.Join(args[len(args)-1], "REPORT.md") + ": no phase had an error or a retry\n"), nil
	case c.Name == "go":
		return nil, nil
	case c.Name == "git":
		return []byte("a6ffa64\n"), nil
	case strings.HasSuffix(c.Name, "/elbencho") && slices.Equal(args, []string{"--version"}):
		return []byte("elbencho\n * Version: 3.0-25\n * Build date: Oct 12 2026\n"), nil
	case strings.HasSuffix(c.Name, "/elbencho"):
		return nil, w.elbencho(c)
	case strings.HasSuffix(c.Name, "/httpload"):
		return nil, w.httpload(c)
	case strings.HasSuffix(c.Name, "/elbencho-build.sh"):
		fmt.Fprintln(c.Out, "elbencho 3.0-25 is current")
		return nil, nil
	case strings.HasSuffix(c.Name, "/rgw-go-up.sh"):
		return nil, w.rgwGoUp(c)
	case strings.HasSuffix(c.Name, "/rgw-go-down.sh"):
		for _, f := range []string{"rgw-go.pid", "rgw-go.endpoint", "rgw-go.metrics"} {
			Expect(os.RemoveAll(filepath.Join(w.out(), f))).To(Succeed())
		}
		fmt.Fprintln(c.Out, "rgw-go (pid 777) stopped")
		return nil, nil
	case strings.HasSuffix(c.Name, "/remove-user.sh"):
		w.user, w.buckets = false, map[string]bool{}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s", line)
}

// kubectl answers rooket's kubectl outside the toolbox.
func (w *world) kubectl(args []string) ([]byte, error) {
	switch {
	case slices.Contains(args, "jsonpath={.items[*].status.podIP}"):
		return []byte("10.89.0.34"), nil
	case slices.Contains(args, "jsonpath={.items[*].metadata.name}"):
		return []byte(pod), nil
	case slices.Contains(args, "sh"):
		w.sampled++
		w.stray("the sampler's search of the pod", func(b background) bool { return b.sampling })
		return []byte(podPid + "\n"), nil
	case slices.Contains(args, "cat"):
		w.sampled++
		w.stray("a read of the pod's radosgw", func(b background) bool { return b.sampling })
		return []byte(procStat("radosgw", 300+w.sampled, 100) + procStatus(605, 156504)), nil
	}
	return nil, fmt.Errorf("unexpected kubectl %s", strings.Join(args, " "))
}

// ceph answers the ceph CLI in the toolbox.
func (w *world) ceph(args []string) ([]byte, error) {
	switch {
	case slices.Contains(args, "service"):
		md, err := json.Marshal(map[string]string{
			"id": "ceph.objectstore.a", "realm_name": "ceph-objectstore", "zonegroup_name": "ceph-objectstore",
			"zone_name": "ceph-objectstore", "frontend_config#0": w.frontends,
		})
		Expect(err).NotTo(HaveOccurred())
		daemons := []string{`"summary":""`}
		for gid := range next(&w.radosgws) {
			daemons = append(daemons, fmt.Sprintf(`"%d":{"start_stamp":"2026-10-12T21:00:00.000000+0000","metadata":%s}`, 4242+gid, md))
		}
		return fmt.Appendf(nil, `{"services":{"rgw":{"daemons":{%s}}}}`, strings.Join(daemons, ",")), nil
	case slices.Contains(args, "rgw_frontends"):
		return []byte(w.frontends + "\n"), nil
	case slices.Contains(args, "config"):
		entity, option := args[len(args)-2], args[len(args)-1]
		if v, ok := w.options[entity+" "+option]; ok {
			return []byte(v + "\n"), nil
		}
		return []byte(parity[option] + "\n"), nil
	case slices.Contains(args, "df"):
		avail := w.avail[0]
		if len(w.avail) > 1 {
			w.avail = w.avail[1:]
		}
		return fmt.Appendf(nil, `{"pools":[{"name":".mgr","stats":{"max_avail":9286684000}},{"name":%q,"stats":{"max_avail":%d}}]}`,
			dataPool, avail), nil
	case slices.Contains(args, "compact"):
		return []byte("compacted osd.0 in 1.2 seconds\n"), nil
	}
	return nil, fmt.Errorf("unexpected ceph %s", strings.Join(args, " "))
}

// admin answers radosgw-admin, its site options cut.
func (w *world) admin(args []string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	switch {
	case cmd == "gc process --include-all":
		return nil, nil
	case cmd == "gc list --include-all":
		entries := make([]string, next(&w.queued))
		for i := range entries {
			entries[i] = fmt.Sprintf(`{"tag":"%d"}`, i)
		}
		return []byte("[" + strings.Join(entries, ",") + "]\n"), nil
	case cmd == "zonegroup get":
		return []byte(`{"name":"ceph-objectstore","api_name":"ceph-objectstore"}`), nil
	case cmd == "user info --uid bench":
		if !w.user {
			return nil, errors.New("exit status 22: could not fetch user info: no user info saved")
		}
		return []byte(benchUser), nil
	case strings.HasPrefix(cmd, "user create --uid bench"):
		w.user = true
		return []byte(benchUser), nil
	case strings.HasPrefix(cmd, "bucket stats --bucket "):
		if !w.buckets[args[len(args)-1]] {
			return nil, errors.New("exit status 2: failure: no such bucket")
		}
		return []byte("{}\n"), nil
	}
	return nil, fmt.Errorf("unexpected radosgw-admin %s", cmd)
}

// next is the first of answers, which it drops unless it is the last.
func next[T any](answers *[]T) T {
	a := (*answers)[0]
	if len(*answers) > 1 {
		*answers = (*answers)[1:]
	}
	return a
}

// benchUser is radosgw-admin's account of the user bench with its fixed key.
const benchUser = `{"user_id":"bench","keys":[{"user":"bench","access_key":"BENCH0000000000000AK",` +
	`"secret_key":"bench-secret-0123456789abcdefghijklmnopqrstuv"}]}`

// flag is the value after name in args, or empty.
func flag(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// size is elbencho's size argument in bytes.
func size(s string) int64 {
	GinkgoHelper()
	unit := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		unit, s = 1<<10, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		unit, s = 1<<20, strings.TrimSuffix(s, "m")
	case s == "":
		s = "0"
	}
	n, err := strconv.ParseInt(s, 10, 64)
	Expect(err).NotTo(HaveOccurred())
	return n * unit
}

// elbencho runs elbencho as it would: a bucket it creates exists from then
// on, and a measured phase appends a line to its JSON and CSV results; an
// rgw-go's GC logs a cycle during it.
func (w *world) elbencho(c harness.Cmd) error {
	args := c.Args
	if slices.Contains(args, "-d") {
		for _, a := range args {
			if strings.HasPrefix(a, "bench-") {
				w.buckets[a] = true
			}
		}
	}
	fmt.Fprintln(c.Out, "elbencho done")
	jsonFile := flag(args, "--jsonfile")
	if jsonFile == "" {
		return nil
	}
	phase := "WRITE"
	switch {
	case slices.Contains(args, "-r"):
		phase = "READ"
	case slices.Contains(args, "--s3listobj"):
		phase = "LISTOBJ"
	case slices.Contains(args, "--s3listobjpar"):
		phase = "LISTOBJ_P"
	}
	histogram := map[string]any{"histogram": map[string]any{"buckets": []any{map[string]any{"range": "500-600us", "count": "100"}}}}
	line, err := json.Marshal(map[string]any{
		"phase_type": phase, "iso_start_date": "2026-10-13T07:11:50.000+0000",
		"config": map[string]any{
			"threads": flag(args, "-t"), "file_size": strconv.FormatInt(size(flag(args, "-s")), 10),
			"block_size": strconv.FormatInt(size(flag(args, "-b")), 10), "command": "elbencho " + strings.Join(args, " "),
		},
		"last_done": map[string]any{
			"elapsed_time_ms": "2000", "entries": "100", "ios": "100", "bytes": "409600",
			"latency": map[string]any{"IO": histogram, "entries": histogram},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	appendFile(jsonFile, string(line)+"\n")
	appendFile(flag(args, "--csvfile"), "ISO date,label,phase\n")
	if w.gateway == "rgw-go" {
		w.gc++
		appendFile(filepath.Join(w.out(), "rgw-go.log"), fmt.Sprintf(
			`{"level":"INFO","msg":"request"}`+"\ngc %d @41.013s 0%%: 0.042+0.84+0.028 ms clock, 1.3+0.43/1.7/0.10+0.92 ms cpu, 3->4->1 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 32 P\n",
			w.gc))
	}
	return nil
}

// httpload runs httpload as it would, writing its result.
func (w *world) httpload(c harness.Cmd) error {
	threads, err := strconv.Atoi(flag(c.Args, "-threads"))
	Expect(err).NotTo(HaveOccurred())
	result, err := json.Marshal(map[string]any{
		"threads": threads, "duration_s": 5.0, "requests": 392, "errors": 0,
		"lat_us": map[string]any{"p50": 41022.771, "p99": 42051.836, "p999": 42078.596},
		"start":  "2026-10-13T07:11:50Z", "command": "httpload " + strings.Join(c.Args, " "),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(flag(c.Args, "-out"), result, 0o600)).To(Succeed())
	fmt.Fprintln(c.Out, "httpload done")
	return nil
}

// rgwGoUp starts rgw-go as rgw-go-up.sh would, writing its endpoint, metrics
// address, pid and log.
func (w *world) rgwGoUp(c harness.Cmd) error {
	for name, data := range map[string]string{
		"rgw-go.endpoint": "http://127.0.0.1:7481\n",
		"rgw-go.metrics":  w.metrics.Listener.Addr().String() + "\n",
		"rgw-go.pid":      rgwGoPid + "\n",
		"rgw-go.log":      `{"level":"INFO","msg":"serving","metrics_addr":"127.0.0.1:9481"}` + "\ngc 1 @0.010s 0%: 0.01+0.1+0.01 ms clock\n",
	} {
		Expect(os.WriteFile(filepath.Join(w.out(), name), []byte(data), 0o600)).To(Succeed())
	}
	fmt.Fprintln(c.Out, "rgw-go (pid 777) serves rgw-go-tentacle's ceph-objectstore at http://127.0.0.1:7481")
	return nil
}

func appendFile(path, data string) {
	GinkgoHelper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	Expect(err).NotTo(HaveOccurred())
	_, err = f.WriteString(data)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.Close()).To(Succeed())
}

// runner points r at the world.
func (w *world) runner(r *gateway.Runner) {
	r.Exec, r.Proc = w.exec, watchedFS{MapFS: w.proc, w: w}
	r.Now = w.now
	r.Getenv = func(string) string { return "" }
	r.Sleep = w.sleep
	r.HTTP = w.metrics.Client()
	r.Stdout = w.stdout
	r.Stderr = GinkgoWriter
	r.Handler = func(out io.Writer) slog.Handler { return slog.NewJSONHandler(out, nil) }
	r.SampleEvery, r.SampleTimeout = time.Second, time.Second
}

// config is the run of the world's gateway with s's settings.
func (w *world) config(s gateway.Settings) gateway.Config {
	GinkgoHelper()
	s.Release, s.Gateway, s.Repo = release, w.gateway, w.repo
	cfg, err := s.Check()
	Expect(err).NotTo(HaveOccurred())
	return cfg
}

func (w *world) run(ctx context.Context, cfg gateway.Config) error {
	r := gateway.NewRunner(io.Discard, io.Discard, nil)
	w.runner(r)
	return r.Run(ctx, cfg)
}

// golden is a testdata file's content.
func golden(name string) string {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

// readFile is a file of the run's directory.
func (w *world) readFile(name string) string {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join(w.dir(), name))
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

// trace is the world's events as a golden writes them.
func (w *world) trace() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.events, "\n") + "\n"
}
