package gateway_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
	"github.com/jhoblitt/rgw-go/hack/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/internal/harness/harnessfakes"
)

// The command goldens are the commands the comparison's shell script ran at
// a6ffa64, before this command replaced it, traced with stubbed commands for
// each gateway, the defaults and BENCH_SMOKE=1, with the checks of the parity
// options the chart values have gained since, and the commands of the
// profiles added after it, less the sampler's start and the CPU profile's
// fetch, which ran in the background, so that their place among the rest raced.
var _ = Describe("Runner.Run", func() {
	DescribeTable("runs the traced commands in their order",
		func(ctx SpecContext, gw string, s gateway.Settings, want string) {
			w := newWorld(gw)
			Expect(w.run(ctx, w.config(s))).To(Succeed())
			Expect(w.trace()).To(Equal(golden(want)))
		},
		Entry("for radosgw", "radosgw", gateway.Settings{}, "run-radosgw.txt"),
		Entry("for radosgw, BENCH_SMOKE=1", "radosgw", gateway.Settings{Smoke: "1"}, "run-radosgw-smoke.txt"),
		Entry("for rgw-go", "rgw-go", gateway.Settings{}, "run-rgw-go.txt"),
		Entry("for rgw-go, BENCH_SMOKE=1 with RGW_GO_FLAGS", "rgw-go",
			gateway.Settings{Smoke: "1", RgwGoFlags: "--rados-completions=pipe"}, "run-rgw-go-smoke.txt"),
	)

	It("writes every file the package doc lists for an rgw-go run", func(ctx SpecContext) {
		w := newWorld("rgw-go")
		Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1"}))).To(Succeed())
		want := []string{
			"env.json", "config.json", "run.log", "noop-8.json",
			"comp-small-put-8.json", "comp-large-put-whole.json", "comp-large-put-parts.json", "crc-put-8.json",
		}
		for _, p := range gateway.Profiles {
			want = append(want, p+".log", "samples-"+p+".csv", "cpu-"+p+".pprof", "gctrace-"+p+".log")
			if !slices.Contains([]string{"noop", "comp-small-put", "comp-large-put", "crc-put"}, p) {
				want = append(want, p+".json", p+".csv")
			}
		}
		entries, err := os.ReadDir(w.dir())
		Expect(err).NotTo(HaveOccurred())
		var got []string
		for _, e := range entries {
			got = append(got, e.Name())
		}
		Expect(got).To(ConsistOf(want))
	})

	It("writes no CPU profile or GC trace for a radosgw run", func(ctx SpecContext) {
		w := newWorld("radosgw")
		Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1"}))).To(Succeed())
		Expect(filepath.Glob(filepath.Join(w.dir(), "cpu-*"))).To(BeEmpty())
		Expect(filepath.Glob(filepath.Join(w.dir(), "gctrace-*"))).To(BeEmpty())
		Expect(w.profiles).To(BeEmpty(), "no profile requested of radosgw")
	})

	It("records env.json with the keys and order the report has always read, for rgw-go", func(ctx SpecContext) {
		w := newWorld("rgw-go")
		Expect(w.run(ctx, w.config(gateway.Settings{RgwGoFlags: "--rados-completions=pipe"}))).To(Succeed())
		Expect(w.readFile("env.json")).To(Equal(fmt.Sprintf(`{
  "release": "tentacle",
  "gateway": "rgw-go",
  "ceph_version": "20.2.4",
  "cluster_image": "quay.io/ceph/ceph:v20.2.4",
  "endpoint": "http://127.0.0.1:7481",
  "api_name": "ceph-objectstore",
  "frontends": "beast endpoint=127.0.0.1:7481",
  "tcp_nodelay": "on",
  "pool_max_avail": 8220119040,
  "datasets": {
    "small-put": 3145728000,
    "small-get": 419430400,
    "noop": 0,
    "large-put": 2147483648,
    "large-get": 2147483648,
    "list": 0,
    "comp-small-put": 3145728000,
    "comp-small-get": 419430400,
    "comp-large-put": 2147483648,
    "comp-large-get": 2147483648,
    "crc-put": 3145728000,
    "ver-put": 3144622080,
    "ver-get": 419430400
  },
  "sampled": "pid:777",
  "radosgw_entity": "client.rgw.ceph.objectstore.a",
  "rgw_go_flags": "--rados-completions=pipe",
  "godebug": "gctrace=1",
  "gomaxprocs": "unset",
  "date": "2026-10-13T07:11:45Z",
  "smoke": "0",
  "seconds": 60,
  "conc": "1 8 32 64 128 256",
  "put_objects": 3000,
  "get_objects": 400,
  "large_threads": 32,
  "large_objects": 1,
  "list_threads": 64,
  "list_objects": 1600,
  "comp_class": "COMP_ZLIB",
  "ver_objects": 1570,
  "elbencho": "3.0-25",
  "elbencho_commit": "0637a922cbb71d56625f553750dbfbf12e5f0167",
  "elbencho_image": "docker.io/library/alpine:3.24.2@sha256:294b",
  "elbencho_packages": "gcc-15.2.0-r2 musl-1.2.5-r10",
  "rooket_version": "rooket v0.0.0-20260928053658-3d8551957e08",
  "nproc": %d,
  "mem_total_kib": 65007268,
  "cpu": "AMD Ryzen 9 7950X3D 16-Core Processor",
  "kernel": "7.2.8-100.fc43.x86_64",
  "go_version": "go version go1.27.1 linux/amd64",
  "git_describe": "a6ffa64",
  "clk_tck": 100,
  "load": "0.69 0.74 1.03",
  "repo": "%s",
  "rooket": "rooket",
  "go_tags": "ceph_preview"
}
`, runtime.NumCPU(), w.repo)))
	})

	It("records a radosgw smoke run's endpoint, frontends, pod, settings and datasets in env.json", func(ctx SpecContext) {
		w := newWorld("radosgw")
		Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"list", "small-get"}}))).To(Succeed())
		Expect(w.readFile("env.json")).To(SatisfyAll(
			ContainSubstring(`"endpoint": "http://10.89.0.34:80",`),
			ContainSubstring(`"frontends": "beast port=80 tcp_nodelay=1",`),
			ContainSubstring(`"datasets": {
    "list": 0,
    "small-get": 1638400
  },`),
			ContainSubstring(`"sampled": "pod:`+pod+`",`),
			ContainSubstring(`"godebug": "",`),
			ContainSubstring(`"smoke": "1",`),
			ContainSubstring(`"seconds": 5,`),
			ContainSubstring(`"conc": "8",`)))
	})

	It("takes the rooket version and GOMAXPROCS from the environment where they are set", func(ctx SpecContext) {
		w := newWorld("radosgw")
		r := gateway.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		r.Getenv = func(name string) string {
			return map[string]string{"ROOKET_VERSION": "rooket v1.2.3", "GOMAXPROCS": "8"}[name]
		}
		Expect(r.Run(ctx, w.config(gateway.Settings{Profiles: []string{"list"}}))).To(Succeed())
		Expect(w.readFile("env.json")).To(SatisfyAll(
			ContainSubstring(`"rooket_version": "rooket v1.2.3",`), ContainSubstring(`"gomaxprocs": "8",`)))
		Expect(w.trace()).NotTo(ContainSubstring("rooket version"))
	})

	It("records config.json: each entity's parity options in the chart values' order", func(ctx SpecContext) {
		w := newWorld("radosgw")
		w.options["client.rgw.rgw-go rgw_enable_usage_log"] = "false"
		Expect(w.run(ctx, w.config(gateway.Settings{Profiles: []string{"list"}}))).To(Succeed())
		options := func(usage string) string {
			return `
      "rgw_dynamic_resharding": "false",
      "rgw_enable_gc_threads": "false",
      "rgw_enable_lc_threads": "false",
      "rgw_run_sync_thread": "false",
      "rgw_d3n_l1_local_datacache_enabled": "false",
      "rgw_enable_usage_log": "` + usage + `",
      "rgw_usage_log_tick_interval": "1",
      "rgw_usage_log_flush_threshold": "1",
      "rgw_lc_debug_interval": "10"
`
		}
		Expect(w.readFile("config.json")).To(Equal(`{
  "client.rgw.ceph.objectstore.a": {
    "source": "config show",
    "options": {` + options("true") + `    }
  },
  "client.rgw.rgw-go": {
    "source": "config get",
    "options": {` + options("false") + `    }
  }
}
`))
	})

	It("records an option it cannot read as empty in config.json", func(ctx SpecContext) {
		w := newWorld("radosgw")
		w.failWith["config get client.rgw.rgw-go rgw_run_sync_thread"] = errors.New("exit status 1")
		Expect(w.run(ctx, w.config(gateway.Settings{Profiles: []string{"list"}}))).To(Succeed())
		Expect(w.readFile("config.json")).To(ContainSubstring(`"rgw_run_sync_thread": ""`))
	})

	It("prints the plan, the measured phases' output and the report's answer", func(ctx SpecContext) {
		w := newWorld("radosgw")
		Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"noop", "list"}}))).To(Succeed())
		Expect(w.stdout.String()).To(Equal(
			"noop           dataset           0 B, needs           0 B of  8220119040 B free: fits\n" +
				"list           dataset           0 B, needs           0 B of  8220119040 B free: fits\n" +
				"httpload done\nelbencho done\nelbencho done\n" +
				filepath.Join(w.dir(), "REPORT.md") + ": no phase had an error or a retry\n"))
		Expect(w.readFile("list.log")).To(Equal("elbencho done\nelbencho done\nelbencho done\n"), "the preparation's too")
		Expect(w.readFile("noop.log")).To(Equal("httpload done\n"))
	})

	It("keeps run.log as the log of what ran when, with the output of what ran outside the profiles", func(ctx SpecContext) {
		w := newWorld("rgw-go")
		Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"list"}}))).To(Succeed())
		log := w.readFile("run.log")
		Expect(log).To(SatisfyAll(
			ContainSubstring(`"msg":"gateway comparison","gateway":"rgw-go","cluster":"rgw-go-tentacle","profiles":"list"`),
			ContainSubstring(`"output":"elbencho 3.0-25 is current\n"`),
			ContainSubstring(`"output":"rgw-go (pid 777) serves rgw-go-tentacle's ceph-objectstore at http://127.0.0.1:7481\n"`),
			ContainSubstring(`"output":"list           dataset           0 B, needs           0 B of  8220119040 B free: fits\n"`),
			ContainSubstring(`"msg":"ran","cmd":"radosgw-admin gc process --include-all"`),
			ContainSubstring(`"msg":"ran","cmd":"ceph tell osd.* compact","output":"compacted osd.0 in 1.2 seconds\n"`),
			ContainSubstring(`"msg":"sampled","target":"pid:777"`),
			ContainSubstring(`"msg":"profile done","profile":"list"`),
			ContainSubstring(`"msg":"removed the user and its buckets","user":"bench"`),
			ContainSubstring(`"msg":"collected the garbage and compacted the OSD","bytes_available":"8220119040"`)))
		Expect(log).NotTo(ContainSubstring("bench-secret"), "the secret reaches elbencho through the environment only")
	})

	It("takes a CPU profile of each profile's last phase, for BENCH_SECONDS", func(ctx SpecContext) {
		w := newWorld("rgw-go")
		Expect(w.run(ctx, w.config(gateway.Settings{Seconds: "7", Conc: "8 1"}))).To(Succeed())
		Expect(w.profiles).To(Equal(slices.Repeat([]string{"/debug/pprof/profile?seconds=7"}, len(gateway.Profiles))))
		want := golden("cpu.pprof")
		for _, p := range gateway.Profiles {
			Expect(w.readFile("cpu-"+p+".pprof")).To(Equal(want), "profile %s", p)
		}
	})

	It("keeps each profile's own GC lines from rgw-go's log", func(ctx SpecContext) {
		w := newWorld("rgw-go")
		Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1"}))).To(Succeed())
		// One cycle a measured phase; the start's "gc 1" is no profile's.
		cycles := map[string][]int{
			"small-put": {1}, "small-get": {2}, "noop": nil, "large-put": {3, 4}, "large-get": {5, 6}, "list": {7, 8},
			"comp-small-put": {9}, "comp-small-get": {10}, "comp-large-put": {11, 12}, "comp-large-get": {13, 14},
			"crc-put": {15}, "ver-put": {16}, "ver-get": {17},
		}
		for p, ns := range cycles {
			var want strings.Builder
			for _, n := range ns {
				fmt.Fprintf(&want, "gc %d @41.013s 0%%: 0.042+0.84+0.028 ms clock, 1.3+0.43/1.7/0.10+0.92 ms cpu, 3->4->1 MB, "+
					"4 MB goal, 0 MB stacks, 0 MB globals, 32 P\n", n)
			}
			Expect(w.readFile("gctrace-"+p+".log")).To(Equal(want.String()), "profile %s", p)
		}
	})

	It("keeps a GC line a JSON record tore as it is, and not its rest, for the report to judge", func() {
		dir := GinkgoT().TempDir()
		log, kept := filepath.Join(dir, "rgw-go.log"), filepath.Join(dir, "gctrace-noop.log")
		whole := "gc 310773 @1479.912s 4%: 0.073+0.58+0.031 ms clock, 2.3+1.2/2.4/0+0.99 ms cpu, 10->10->4 MB, 11 MB goal, " +
			"0 MB stacks, 0 MB globals, 32 P\n"
		torn := `gc 310774 @1479.916s 4{"level":"WARN","msg":"bucket listing failed after the response started"}` + "\n"
		Expect(os.WriteFile(log, []byte(`{"level":"INFO","msg":"request"}`+"\n"+whole+torn+
			`%: 0.086+0.61+0.039 ms clock, 2.7+1.6/2.7/0+1.2 ms cpu, 10->10->{"level":"WARN","msg":"request"}`+"\n"+
			"4 MB, 11 MB goal, 0 MB stacks, 0 MB globals, 32 P\n"+
			`gc {"level":"WARN","msg":"torn before its cycle's number"}`+"\n"), 0o600)).To(Succeed())
		Expect(gateway.KeepGCTrace(log, 0, kept)).To(Succeed())
		got, err := os.ReadFile(kept)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal(whole + torn))
	})

	It("samples the radosgw in its pod and rgw-go on the host, each file under its header", func(ctx SpecContext) {
		for _, gw := range []string{"radosgw", "rgw-go"} {
			w := newWorld(gw)
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"noop"}}))).To(Succeed())
			samples := w.readFile("samples-noop.csv")
			Expect(samples).To(HavePrefix("time,utime,stime,threads,rss_kib\n"), "gateway %s", gw)
			Expect(strings.Count(samples, "\n")).To(BeNumerically(">", 1), "gateway %s took samples", gw)
			threads := map[string]string{"radosgw": ",605,156504\n", "rgw-go": ",21,156504\n"}[gw]
			Expect(samples).To(ContainSubstring(threads), "gateway %s", gw)
		}
	})

	Context("refuses", func() {
		It("a radosgw whose frontends leave TCP_NODELAY off, before creating the user", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.frontends = "beast port=80"
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(
				"radosgw serves with the frontends 'beast port=80', which leave TCP_NODELAY off, and both gateways run with it on: " +
					"give the radosgw the chart values' rgwCommandFlags with make cluster-up RELEASE=tentacle, then make populate RELEASE=tentacle"))
			Expect(w.trace()).NotTo(ContainSubstring("user"))
			Expect(filepath.Join(w.dir(), "env.json")).NotTo(BeAnExistingFile())
		})

		It("an rgw-go whose frontend sets tcp_nodelay to anything but 1, and stops it", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.proc[rgwGoPid+"/cmdline"].Data = []byte("rgw-go\x00--rgw-frontends=beast endpoint=127.0.0.1:7481\x00" +
				"--rgw-frontends=beast endpoint=127.0.0.1:7481 tcp_nodelay=0\x00")
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(ContainSubstring(
				"rgw-go serves with the frontends 'beast endpoint=127.0.0.1:7481 tcp_nodelay=0', which leave TCP_NODELAY off")))
			Expect(w.trace()).To(HaveSuffix("ROOKET=rooket REPO/hack/rooket/rgw-go-down.sh tentacle\n"))
		})

		It("a radosgw whose entity lacks a parity option", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.options[radosgwEntity+" rgw_enable_gc_threads"] = "true"
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(
				"client.rgw.ceph.objectstore.a holds rgw_enable_gc_threads=true, not the parity setting false: run make rgw-go-up RELEASE=tentacle first"))
			Expect(w.trace()).NotTo(ContainSubstring("user"))
		})

		It("a radosgw left in an LC mode, naming the mode switch that restores the parity mode", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.options[radosgwEntity+" rgw_enable_lc_threads"] = "true"
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(
				"client.rgw.ceph.objectstore.a holds rgw_enable_lc_threads=true, not the parity setting false: " +
					"run make lc-mode LC=off RELEASE=tentacle first"))
		})

		It("an rgw-go whose entity lacks a parity option once rgw-go-up.sh ran, and stops rgw-go", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.options["client.rgw.rgw-go rgw_usage_log_tick_interval"] = "30"
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(
				"client.rgw.rgw-go holds rgw_usage_log_tick_interval=30, not the parity setting 1: run make rgw-go-up RELEASE=tentacle first"))
			Expect(w.trace()).To(HaveSuffix("ROOKET=rooket REPO/hack/rooket/rgw-go-down.sh tentacle\n"))
		})

		It("a parity option ceph config cannot read", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.failWith["config show "+radosgwEntity+" rgw_run_sync_thread"] = rooketExit{stderr: "no such entity"}
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(ContainSubstring(
				"ceph config show client.rgw.ceph.objectstore.a rgw_run_sync_thread failed: exit status 1: no such entity")))
		})

		It("a radosgw run while an rgw-go runs against the cluster, so that the radosgw run has the host to itself", func(ctx SpecContext) {
			w := newWorld("radosgw")
			Expect(os.WriteFile(filepath.Join(w.out(), "rgw-go.pid"), []byte(rgwGoPid+"\n"), 0o600)).To(Succeed())
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(
				"an rgw-go runs against rgw-go-tentacle: stop it with make rgw-go-down RELEASE=tentacle, so that the radosgw run has the host to itself"))
			Expect(w.trace()).NotTo(ContainSubstring("config show " + radosgwEntity + " rgw_dynamic"))
		})

		It("a profile's dataset over half the data pool's free space, after printing the plan", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.avail = []int64{4000000000}
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(
				"a profile's dataset is more than half of the 4000000000 B the data pool can take: lower its threads or objects"))
			Expect(w.stdout.String()).To(ContainSubstring("small-put      dataset  3145728000 B, needs  6291456000 B of  4000000000 B free: DOES NOT FIT\n"))
			Expect(w.trace()).NotTo(ContainSubstring("rgw-go-up.sh"), "nothing restarted")
			Expect(w.trace()).NotTo(ContainSubstring("user"))
		})

		It("a bench user that exists without the fixed key, which it leaves alone", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
				if slices.Contains(c.Args, "info") && slices.Contains(c.Args, "bench") {
					w.record(w.line(c))
					return []byte(`{"user_id":"bench","keys":[{"access_key":"SOMEONEELSE","secret_key":"theirs"}]}`), nil
				}
				return w.output(ctx, c)
			}
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(
				"the user bench exists without the key BENCH0000000000000AK; remove it and rerun"))
			Expect(w.trace()).NotTo(ContainSubstring("remove-user.sh"))
			Expect(w.trace()).NotTo(ContainSubstring("compact"))
		})

		It("a radosgw pod it cannot tell apart from another", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
				if slices.Contains(c.Args, "jsonpath={.items[*].status.podIP}") {
					return []byte("10.89.0.34 10.89.0.35"), nil
				}
				return w.output(ctx, c)
			}
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError("want one radosgw pod IP, have '10.89.0.34 10.89.0.35'"))
		})

		It("a cluster that is not up, before it creates the run directory", func(ctx SpecContext) {
			w := newWorld("radosgw")
			Expect(os.Remove(filepath.Join(w.out(), "image"))).To(Succeed())
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(fmt.Sprintf(
				"no %s or %s: run make cluster-up RELEASE=tentacle", filepath.Join(w.out(), "ceph.conf"), filepath.Join(w.out(), "image"))))
			Expect(filepath.Join(w.repo, "hack", "bench", "out", release)).NotTo(BeAnExistingFile())
			Expect(w.exec.OutputCallCount()).To(BeZero())
		})

		It("a cluster that is not populated", func(ctx SpecContext) {
			w := newWorld("radosgw")
			Expect(os.Remove(filepath.Join(w.out(), "manifest.json"))).To(Succeed())
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(fmt.Sprintf(
				"no %s; run make populate RELEASE=tentacle first", filepath.Join(w.out(), "manifest.json"))))
			Expect(w.exec.OutputCallCount()).To(BeZero())
		})

		It("chart values with no parity options", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.writeValues([]byte("cephImage:\n  tag: v20.2.4\ncephObjectStores:\n  - name: ceph-objectstore\n    spec: {}\n"))
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(ContainSubstring(
				"has no rgwConfig at cephObjectStores[name=ceph-objectstore].spec.gateway")))
			Expect(w.exec.OutputCallCount()).To(BeZero())
		})
	})

	Context("when the run ends", func() {
		It("removes the bench user and its buckets, stops rgw-go and compacts the OSD after a run that succeeded", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"large-put"}}))).To(Succeed())
			Expect(w.trace()).To(HaveSuffix(exitTail("rgw-go", "bench-large", "bench-large-mpu")))
			Expect([]any{w.user, w.buckets}).To(Equal([]any{false, map[string]bool{}}))
			Expect(filepath.Join(w.out(), "rgw-go.pid")).NotTo(BeAnExistingFile())
		})

		It("does the same after a phase fails, and fails with it", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.failWith["-b 8m --timelimit 5 bench-large-mpu"] = errors.New("exit status 1")
			err := w.run(ctx, w.config(gateway.Settings{Smoke: "1"}))
			Expect(err).To(MatchError(ContainSubstring("large-put: elbencho -w -t 4 -n 0 -N 1 -s 64m -b 8m --timelimit 5 bench-large-mpu failed: exit status 1")))
			Expect(gateway.ExitStatus(ctx, err)).To(Equal(1))
			Expect(w.trace()).To(HaveSuffix(exitTail("rgw-go", "bench-small", "bench-large", "bench-large-mpu")))
			Expect(w.trace()).NotTo(ContainSubstring("hack/bench/report"))
			Expect(w.readFile("run.log")).To(ContainSubstring(`"level":"ERROR","msg":"the run failed","error":"large-put: elbencho`))
		})

		It("stops rgw-go but removes no user when rgw-go-up.sh fails", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.failWith["rgw-go-up.sh"] = errors.New("exit status 1")
			Expect(w.run(ctx, w.config(gateway.Settings{}))).To(MatchError(ContainSubstring("rgw-go-up.sh failed; see " +
				filepath.Join(w.dir(), "run.log"))))
			Expect(w.trace()).To(HaveSuffix("REPO/hack/rooket/rgw-go-up.sh tentacle\nROOKET=rooket REPO/hack/rooket/rgw-go-down.sh tentacle\n"))
		})

		It("fails a run that succeeded when the user's removal fails, and still compacts the OSD", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.failWith["remove-user.sh"] = errors.New("exit status 1")
			err := w.run(ctx, w.config(gateway.Settings{Profiles: []string{"list"}}))
			Expect(err).To(MatchError(ContainSubstring("removing the user bench and its buckets failed; see " + filepath.Join(w.dir(), "run.log"))))
			Expect(gateway.ExitStatus(ctx, err)).To(Equal(1))
			Expect(w.trace()).To(HaveSuffix("tell osd.* compact\n" +
				"ROOKET_NAME=rgw-go-tentacle rooket kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph --connect-timeout=20 --rados-mon-op-timeout=20 df -f json\n"))
		})

		It("fails a run that succeeded when the compaction fails", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.failWith["compact"] = rooketExit{stderr: "timed out"}
			Expect(w.run(ctx, w.config(gateway.Settings{Profiles: []string{"list"}}))).To(MatchError(ContainSubstring(
				"collecting the garbage or compacting the OSD failed; see " + filepath.Join(w.dir(), "run.log") + ": ")))
		})

		It("only logs a failed stop of rgw-go, which leaves the run's outcome as it was", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.failWith["rgw-go-down.sh"] = errors.New("exit status 1")
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"list"}}))).To(Succeed())
			Expect(w.readFile("run.log")).To(ContainSubstring(`"level":"ERROR","msg":"stopping rgw-go failed"`))
			Expect(w.trace()).To(HaveSuffix(exitTail("rgw-go", "bench-list")))
		})

		DescribeTable("exits 128 and the signal when one interrupts a phase, once it has undone what it did",
			func(ctx SpecContext, sig syscall.Signal, status int) {
				w := newWorld("rgw-go")
				sigs := make(chan os.Signal, 1)
				runCtx, stop := harness.WatchSignals(ctx, sigs)
				defer stop()
				w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
					if slices.Contains(c.Args, "--jsonfile") && slices.Contains(c.Args, "bench-small") {
						w.record("interrupted")
						sigs <- sig
						// As the execer does once it has killed the process group.
						<-ctx.Done()
						return nil, context.Cause(ctx)
					}
					return w.output(ctx, c)
				}
				err := w.run(runCtx, w.config(gateway.Settings{Smoke: "1"}))
				Expect(err).To(MatchError(harness.Interrupted{Signal: sig}))
				Expect(gateway.ExitStatus(runCtx, err)).To(Equal(status))
				Expect(w.trace()).To(ContainSubstring("interrupted\n" + strings.Split(exitTail("rgw-go"), "\n")[0] + "\n"))
				Expect(w.trace()).To(HaveSuffix(exitTail("rgw-go", "bench-small")))
				Expect(w.user).To(BeFalse())
			},
			Entry("SIGINT", syscall.SIGINT, 130),
			Entry("SIGTERM", syscall.SIGTERM, 143),
		)

		It("stops the sampler and the CPU profile's fetch of a phase that fails", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.failWith["--s3listobjpar"] = errors.New("exit status 1")
			// A fetch that waits until it is cut short.
			w.mu.Lock()
			w.holdProfile = true
			w.mu.Unlock()
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"list"}}))).To(MatchError(ContainSubstring("--s3listobjpar")))
			Expect(w.readFile("run.log")).To(ContainSubstring(`"msg":"sampled","target":"pid:777"`))
		})
	})

	It("gives every command a deadline of its own", func(ctx SpecContext) {
		w := newWorld("rgw-go")
		Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1"}))).To(Succeed())
		phase := 5*time.Second + 5*time.Minute
		bounds := map[string]time.Duration{
			"cluster": 3 * time.Minute, "sample": time.Second, "local": time.Minute, "build": 15 * time.Minute,
			"elbencho-build": time.Hour, "up": 15 * time.Minute, "down": 2 * time.Minute, "unmeasured": 30 * time.Minute,
			"phase": phase, "compaction": 30 * time.Minute, "purge": 90 * time.Minute,
		}
		kinds := map[string]int{}
		var unbounded []string
		var compaction, purge time.Duration
		for i := range w.exec.OutputCallCount() {
			callCtx, c := w.exec.OutputArgsForCall(i)
			kind := "local"
			switch {
			case sampler(c):
				kind = "sample"
			case c.Name == "rooket" && slices.Contains(c.Args, "compact"):
				kind = "compaction"
				if deadline, ok := callCtx.Deadline(); ok {
					compaction = time.Until(deadline)
				}
			case c.Name == "rooket" && slices.Contains(c.Args, "--purge-objects"):
				kind = "purge"
				if deadline, ok := callCtx.Deadline(); ok {
					purge = time.Until(deadline)
				}
			case c.Name == "rooket" && c.Args[0] == "kubectl":
				kind = "cluster"
			case c.Name == "go" && c.Args[0] != "version":
				kind = "build"
			case strings.HasSuffix(c.Name, "elbencho-build.sh"):
				kind = "elbencho-build"
			case strings.HasSuffix(c.Name, "rgw-go-up.sh"):
				kind = "up"
			case strings.HasSuffix(c.Name, "rgw-go-down.sh"):
				kind = "down"
			case strings.HasSuffix(c.Name, "remove-user.sh"):
				kind = "unmeasured"
			case strings.HasSuffix(c.Name, "elbencho") && slices.Contains(c.Args, "--jsonfile"), strings.HasSuffix(c.Name, "httpload"),
				strings.HasSuffix(c.Name, "s3send") && slices.Contains(c.Args, "-duration"):
				kind = "phase"
			case strings.HasSuffix(c.Name, "s3send"):
				kind = "unmeasured"
			case strings.HasSuffix(c.Name, "elbencho") && !slices.Contains(c.Args, "--version"):
				kind = "unmeasured"
			}
			kinds[kind]++
			if deadline, ok := callCtx.Deadline(); !ok || time.Until(deadline) > bounds[kind] {
				unbounded = append(unbounded, kind+": "+c.String())
			}
		}
		Expect(unbounded).To(BeEmpty(), "commands with no deadline, or one past their bound")
		Expect(kinds).To(HaveKeyWithValue("local", 4), "elbencho --version, rooket version, go version and git describe")
		Expect(kinds).To(HaveKeyWithValue("build", 4), "httpload, s3send, rgw-go and the report")
		Expect(kinds).To(HaveKeyWithValue("phase", 18), "a smoke run's 13 measured elbencho phases, 4 measured s3send puts and 1 httpload run")
		Expect(kinds).To(HaveKeyWithValue("purge", 2), "the versioned bucket's removal after each of the two profiles that fill it")
		Expect(kinds).To(HaveKey("cluster"))
		Expect(kinds).To(HaveKey("unmeasured"))
		Expect(kinds).To(HaveKeyWithValue("compaction", 1))
		// ceph tell osd.* compact returns once the OSD has compacted, so the
		// cluster-call bound would cut it short while the OSD goes on.
		Expect(compaction).To(BeNumerically(">", 29*time.Minute), "the compaction is bounded at 30 min, not the 3 min of a cluster call")
		// A bucket of hundreds of thousands of versions is removed one entry after
		// another, which no run has timed: its bound is its own, not a cluster
		// call's 3 minutes or a preparation's 30.
		Expect(purge).To(BeNumerically(">", 89*time.Minute), "the versioned bucket's removal is bounded at 90 min")
	})

	It("kills a phase whose elbencho hangs at BENCH_SECONDS and the margin, its process group with it, and still undoes the run", func(ctx SpecContext) {
		w := newWorld("rgw-go")
		hang := filepath.Join(GinkgoT().TempDir(), "elbencho")
		Expect(os.WriteFile(hang, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0o700)).To(Succeed()) //nolint:gosec // the fixture is a script the spec runs
		host := harness.NewExecer(time.Second)
		r := gateway.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		r.PhaseMargin = 200 * time.Millisecond
		r.Exec = &harnessfakes.FakeExecer{OutputStub: func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			if slices.Contains(c.Args, "--jsonfile") {
				w.record(w.line(c))
				hung := c
				hung.Name = hang
				return host.Output(ctx, hung)
			}
			return w.output(ctx, c)
		}}
		start := time.Now()
		err := r.Run(ctx, w.config(gateway.Settings{Seconds: "1", Profiles: []string{"list"}}))
		elapsed := time.Since(start)
		Expect(err).To(MatchError(ContainSubstring("list: elbencho --s3listobj 102400 -t 1 --timelimit 1 --infloop bench-list failed")))
		Expect(err).To(MatchError(ContainSubstring("signal: killed")))
		Expect(elapsed).To(BeNumerically("<", 10*time.Second), "returned after %s, not after the hang's 30 s", elapsed)
		Expect(w.trace()).To(HaveSuffix(exitTail("rgw-go", "bench-list")))
		GinkgoWriter.Printf("a 1 s phase with a 200 ms margin returned after %s\n", elapsed)
	})

	Describe("the profiles added after small-put's six", func() {
		// run is a smoke run of the radosgw's profiles, and its trace's lines.
		run := func(ctx SpecContext, profiles ...string) (*world, []string) {
			GinkgoHelper()
			w := newWorld("radosgw")
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: profiles}))).To(Succeed())
			return w, strings.Split(w.trace(), "\n")
		}
		// at is the index of the first line holding every one of parts at or
		// after from, or -1.
		at := func(lines []string, from int, parts ...string) int {
			for i := from; i < len(lines); i++ {
				if lines[i] != "" && !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(lines[i], p) }) {
					return i
				}
			}
			return -1
		}

		DescribeTable("builds s3send for the profiles that send what elbencho cannot, and for no other",
			func(ctx SpecContext, profile string, built bool) {
				w, _ := run(ctx, profile)
				build := "go -C REPO build -o REPO/hack/bench/out/s3send ./hack/bench/s3send\n"
				if built {
					Expect(w.trace()).To(ContainSubstring(build))
				} else {
					Expect(w.trace()).NotTo(ContainSubstring(build))
				}
			},
			Entry("small-put", "small-put", false),
			Entry("noop, which builds httpload", "noop", false),
			Entry("comp-small-put", "comp-small-put", true),
			Entry("comp-large-get", "comp-large-get", true),
			Entry("crc-put", "crc-put", true),
			Entry("ver-put, whose versioning it enables", "ver-put", true),
			Entry("ver-get", "ver-get", true),
		)

		It("signs s3send as the bench user with the secret in its environment and never its arguments", func(ctx SpecContext) {
			w, _ := run(ctx, "crc-put", "ver-put")
			var seen int
			for i := range w.exec.OutputCallCount() {
				_, c := w.exec.OutputArgsForCall(i)
				if !strings.HasSuffix(c.Name, "/s3send") {
					continue
				}
				seen++
				Expect(c.Env).To(ConsistOf("AWS_SECRET_ACCESS_KEY=bench-secret-0123456789abcdefghijklmnopqrstuv"))
				Expect(c.Args[:6]).To(Equal([]string{"-endpoint", "http://10.89.0.34:80", "-key", "BENCH0000000000000AK", "-region", "ceph-objectstore"}))
				Expect(strings.Join(c.Args, " ")).NotTo(ContainSubstring("bench-secret"))
			}
			Expect(seen).To(Equal(2), "crc-put's phase and ver-put's versioning")
		})

		It("writes comp-small-put's keys as small-put's through the class, compressible, bounded by BENCH_SECONDS", func(ctx SpecContext) {
			_, lines := run(ctx, "comp-small-put")
			Expect(at(lines, 0, "s3send -endpoint http://10.89.0.34:80 -key BENCH0000000000000AK -region ceph-objectstore -request-timeout 30s -op put "+
				"-duration 5s -out DIR/comp-small-put-8.json -bucket bench-small -threads 8 -objects 3000 -size 4096 -storage-class COMP_ZLIB -compressible")).
				To(BeNumerically(">", 0))
		})

		It("writes comp-small-get's read set through the class before it reads it as small-get does", func(ctx SpecContext) {
			_, lines := run(ctx, "comp-small-get")
			write := at(lines, 0, "s3send", "-op put -bucket bench-small -threads 8 -objects 50 -size 4096 -storage-class COMP_ZLIB -compressible")
			read := at(lines, 0, "elbencho", "--jsonfile", "-r -t 8 -n 0 -N 50 -s 4k -b 4k --timelimit 5 --infloop bench-small")
			Expect(write).To(BeNumerically(">", 0))
			Expect(read).To(BeNumerically(">", write), "the reads come after the write")
			Expect(at(lines, 0, "elbencho", "-d -t 1 bench-small")).To(BeNumerically("<", write), "the bucket is made first")
		})

		It("writes comp-large-put whole and in 8 MiB parts, and removes each write's objects after it", func(ctx SpecContext) {
			_, lines := run(ctx, "comp-large-put")
			whole := at(lines, 0, "-out DIR/comp-large-put-whole.json -bucket bench-large -threads 4 -objects 1 -size 67108864 -storage-class COMP_ZLIB -compressible")
			parts := at(lines, 0, "-out DIR/comp-large-put-parts.json -bucket bench-large-mpu -threads 4 -objects 1 -size 67108864 "+
				"-storage-class COMP_ZLIB -compressible -block 8388608")
			Expect(whole).To(BeNumerically(">", 0))
			Expect(lines[whole]).NotTo(ContainSubstring("-block"))
			removed := at(lines, whole, "-F -t 4 -n 0 -N 1 --nodelerr bench-large")
			Expect(removed).To(BeNumerically(">", whole))
			Expect(parts).To(BeNumerically(">", removed), "the parts are written after the whole objects are gone")
			Expect(at(lines, parts, "-F -t 4 -n 0 -N 1 --nodelerr bench-large-mpu")).To(BeNumerically(">", parts))
		})

		It("writes comp-large-get's read set through the class before it reads it in ranges and whole", func(ctx SpecContext) {
			_, lines := run(ctx, "comp-large-get")
			write := at(lines, 0, "s3send", "-op put -bucket bench-large -threads 4 -objects 1 -size 67108864 -storage-class COMP_ZLIB -compressible")
			ranges := at(lines, 0, "-r -t 4 -n 0 -N 1 -s 64m -b 4m --timelimit 5 --infloop bench-large")
			whole := at(lines, 0, "-r -t 4 -n 0 -N 1 -s 64m -b 64m --timelimit 5 --infloop bench-large")
			Expect(write).To(BeNumerically(">", 0))
			Expect(ranges).To(BeNumerically(">", write))
			Expect(whole).To(BeNumerically(">", ranges))
		})

		It("sends crc-put's CRC32 and no storage class", func(ctx SpecContext) {
			_, lines := run(ctx, "crc-put")
			put := at(lines, 0, "-out DIR/crc-put-8.json -bucket bench-small -threads 8 -objects 3000 -size 4096 -checksum crc32")
			Expect(put).To(BeNumerically(">", 0))
			Expect(lines[put]).NotTo(ContainSubstring("-storage-class"))
			Expect(lines[put]).NotTo(ContainSubstring("-compressible"), "a checksummed put writes the incompressible bytes")
		})

		It("turns the versioned bucket's versioning on before ver-put's first phase, and removes the bucket with its versions after the last", func(ctx SpecContext) {
			w, lines := run(ctx, "ver-put")
			create := at(lines, 0, "elbencho", "-d -t 1 bench-ver")
			enable := at(lines, 0, "s3send", "-op versioning -bucket bench-ver")
			first := at(lines, 0, "elbencho", "--jsonfile", "bench-ver")
			purge := at(lines, 0, "bucket rm --bucket bench-ver --purge-objects")
			Expect(create).To(BeNumerically(">", 0))
			Expect(enable).To(BeNumerically(">", create), "the bucket exists before its versioning is set")
			Expect(first).To(BeNumerically(">", enable), "every measured put is a versioned one")
			Expect(purge).To(BeNumerically(">", first))
			Expect(lines[first]).To(ContainSubstring("-w -t 8 -n 0 -N 3000 -s 4k -b 4k --timelimit 5 bench-ver"))
			Expect(w.buckets).NotTo(HaveKey("bench-ver"))
			Expect(w.versioned).To(BeEmpty())
			for _, l := range lines {
				if strings.Contains(l, "--s3multidel") {
					Expect(l).NotTo(ContainSubstring("bench-ver"), "a multi-object delete of a versioned bucket would only add delete markers")
				}
			}
		})

		It("logs the versioned bucket's removal's start, its end and how long it took to run.log", func(ctx SpecContext) {
			w, _ := run(ctx, "ver-put")
			log := w.readFile("run.log")
			Expect(log).To(ContainSubstring(`"msg":"removing the versioned bench bucket with its versions","bucket":"bench-ver","limit_seconds":5400`))
			Expect(log).To(ContainSubstring(`"msg":"removed the versioned bench bucket","bucket":"bench-ver","seconds":420`))
		})

		Context("when the versioned bucket's removal", func() {
			// The buckets a run left stale, as an earlier one that died would,
			// so that both kinds of removal have work.
			stale := func(w *world) { w.buckets["bench-small"] = true }
			profile := func(w *world) gateway.Config {
				return w.config(gateway.Settings{Smoke: "1", Profiles: []string{"ver-put"}})
			}

			It("fails, and the run says how long it ran of its bound and does not try again at the exit", func(ctx SpecContext) {
				w := newWorld("radosgw")
				stale(w)
				w.failWith["--purge-objects"] = context.DeadlineExceeded
				err := w.run(ctx, profile(w))
				Expect(err).To(MatchError(ContainSubstring("removing bench-ver with its versions failed after 0 s of the 5400 s allowed: context deadline exceeded")))
				Expect(w.readFile("run.log")).To(ContainSubstring(`"level":"ERROR","msg":"removing the versioned bench bucket failed","bucket":"bench-ver"`))
				Expect(strings.Count(w.trace(), "bucket rm --bucket bench-ver")).To(Equal(1), "one removal, the profile's; the exit leaves the bucket to remove-user.sh")
				Expect(w.readFile("run.log")).To(ContainSubstring(`"msg":"not removing the versioned bench bucket again`))
				Expect(w.trace()).To(ContainSubstring("remove-user.sh tentacle bench"))
				Expect(w.trace()).To(ContainSubstring("--s3multidel 1000 -t 1 bench-small"), "the multi-object delete ran whatever the removal gave")
			})

			It("exits 0 having removed nothing, as radosgw-admin does, the run fails naming the bucket", func(ctx SpecContext) {
				w := newWorld("radosgw")
				w.rmKeeps = true
				err := w.run(ctx, profile(w))
				Expect(err).To(MatchError(ContainSubstring(
					"bench-ver still exists after radosgw-admin bucket rm exited 0, 420 s into the 5400 s allowed")))
				Expect(w.readFile("run.log")).To(ContainSubstring(`"msg":"the versioned bench bucket is still there","bucket":"bench-ver","seconds":420`))
				Expect(strings.Count(w.trace(), "bucket rm --bucket bench-ver")).To(Equal(1))
			})

			DescribeTable("fails, naming the bucket, when reading it back tells nothing of it",
				func(ctx SpecContext, readBack answer, failure string) {
					w := newWorld("radosgw")
					w.readBack = &readBack
					err := w.run(ctx, profile(w))
					Expect(err).To(MatchError(ContainSubstring(
						"reading back bench-ver after radosgw-admin bucket rm exited 0, 420 s into the 5400 s allowed: " + failure)))
					Expect(w.readFile("run.log")).To(ContainSubstring(`"level":"ERROR","msg":"the versioned bench bucket could not be read back","bucket":"bench-ver","seconds":420`))
					Expect(strings.Count(w.trace(), "bucket rm --bucket bench-ver")).To(Equal(1), "not removed again at the exit")
				},
				Entry("an exit status that is neither 0 nor 2", answer{status: 1, stderr: "failure: could not connect to the cluster"},
					"radosgw-admin bucket stats --bucket bench-ver exited 1: failure: could not connect to the cluster"),
				Entry("an output that ends in no exit status", answer{noLine: true, stderr: "Segmentation fault"},
					`radosgw-admin bucket stats --bucket bench-ver ended in no exit status: "Segmentation fault\n"`),
				Entry("a toolbox that could not run the lookup", answer{err: rooketExit{stderr: "error: unable to upgrade connection"}},
					"exit status 1: error: unable to upgrade connection"),
				Entry("a lookup that ran out of its time", answer{err: context.DeadlineExceeded}, "context deadline exceeded"),
			)

			It("still runs when the multi-object delete beside it fails", func(ctx SpecContext) {
				w := newWorld("radosgw")
				stale(w)
				w.failWith["--s3multidel"] = errors.New("exit status 1")
				err := w.run(ctx, profile(w))
				Expect(err).To(MatchError(ContainSubstring("removing the bench objects failed")))
				Expect(w.trace()).To(ContainSubstring("bucket rm --bucket bench-ver --purge-objects"), "the versioned bucket's removal ran whatever the delete gave")
				Expect(w.buckets).NotTo(HaveKey("bench-ver"))
			})
		})

		It("removes nothing and fails nothing on a first run, before any bench bucket exists", func(ctx SpecContext) {
			w := newWorld("radosgw")
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"noop"}}))).To(Succeed())
			Expect(w.trace()).NotTo(ContainSubstring("--s3multidel"))
			Expect(w.trace()).NotTo(ContainSubstring("bucket rm"))
			Expect(strings.Count(w.trace(), gateway.LookupScript)).To(Equal(10), "each bench bucket looked up after the profile and at the exit")
			log := w.readFile("run.log")
			for _, b := range []string{"bench-small", "bench-large", "bench-large-mpu", "bench-list", "bench-ver"} {
				Expect(strings.Count(log, `"msg":"ran","cmd":"radosgw-admin bucket stats --bucket `+b+`","output":"failure: no such bucket\n\nradosgw-admin-status=2\n"`)).
					To(Equal(2), b)
			}
		})

		DescribeTable("empties a bench bucket its lookup finds, skips one it says is gone, and fails the run, emptying nothing of it, when the lookup tells nothing",
			func(ctx SpecContext, bucket string, lookup *answer, emptied bool, failure string) {
				w := newWorld("radosgw")
				if lookup != nil {
					w.lookups[bucket] = *lookup
				}
				err := w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"small-put"}}))
				if failure == "" {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(ContainSubstring("looking up " + bucket + " before removing its objects: " + failure)))
					Expect(w.readFile("run.log")).To(ContainSubstring(
						`"level":"ERROR","msg":"could not tell whether a bench bucket is there, so its objects stay","bucket":"` + bucket + `"`))
				}
				if emptied {
					Expect(w.trace()).To(ContainSubstring("--s3multidel 1000 -t 1 bench-small"))
				} else {
					Expect(w.trace()).NotTo(ContainSubstring("--s3multidel"), "no objects are removed from a bucket nothing is known of")
				}
			},
			Entry("bench-small there, exit status 0", "bench-small", nil, true, ""),
			Entry("bench-small gone, exit status 2", "bench-small", &answer{status: 2, stderr: "failure: no such bucket"}, false, ""),
			Entry("bench-small, exit status 1", "bench-small", &answer{status: 1, stderr: "failure: could not connect to the cluster"}, false,
				"radosgw-admin bucket stats --bucket bench-small exited 1: failure: could not connect to the cluster"),
			Entry("bench-small, an output that ends in no exit status", "bench-small", &answer{noLine: true}, false,
				`radosgw-admin bucket stats --bucket bench-small ended in no exit status: ""`),
			Entry("bench-small, a toolbox that could not run the lookup", "bench-small", &answer{err: rooketExit{stderr: "error: unable to upgrade connection"}}, false,
				"exit status 1: error: unable to upgrade connection"),
			Entry("bench-small, a lookup that ran out of its time", "bench-small", &answer{err: context.DeadlineExceeded}, false, "context deadline exceeded"),
			Entry("bench-large, which small-put never makes, exit status 1, while bench-small is emptied", "bench-large",
				&answer{status: 1, stderr: "failure: could not connect to the cluster"}, true,
				"radosgw-admin bucket stats --bucket bench-large exited 1: failure: could not connect to the cluster"),
		)

		It("shares small-put's keys among ver-put's phases, which keep every version", func(ctx SpecContext) {
			w := newWorld("radosgw")
			Expect(w.run(ctx, w.config(gateway.Settings{Seconds: "5", Conc: "1 8", Profiles: []string{"ver-put"}}))).To(Succeed())
			lines := strings.Split(w.trace(), "\n")
			for _, t := range []string{"1", "8"} {
				Expect(at(lines, 0, "--jsonfile", "-w -t "+t+" -n 0 -N 2666 -s 4k -b 4k --timelimit 5 bench-ver")).To(BeNumerically(">", 0), "phase "+t)
			}
		})

		It("writes ver-get's read set into the versioned bucket before it reads it as small-get does", func(ctx SpecContext) {
			_, lines := run(ctx, "ver-get")
			enable := at(lines, 0, "s3send", "-op versioning -bucket bench-ver")
			write := at(lines, 0, "elbencho", "-w -t 8 -n 0 -N 50 -s 4k -b 4k bench-ver")
			read := at(lines, 0, "elbencho", "--jsonfile", "-r -t 8 -n 0 -N 50 -s 4k -b 4k --timelimit 5 --infloop bench-ver")
			Expect(enable).To(BeNumerically(">", 0))
			Expect(write).To(BeNumerically(">", enable), "every object of the read set is a versioned write")
			Expect(read).To(BeNumerically(">", write))
		})

		It("fails a profile whose s3send fails, and still removes the user and its buckets", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.failWith["-op put -duration"] = errors.New("exit status 1")
			err := w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"crc-put"}}))
			Expect(err).To(MatchError(ContainSubstring(
				"crc-put: s3send -bucket bench-small -threads 8 -objects 3000 -size 4096 -checksum crc32 failed: exit status 1")))
			Expect(gateway.ExitStatus(ctx, err)).To(Equal(1))
			Expect(w.trace()).To(HaveSuffix(exitTail("radosgw", "bench-small")))
		})

		Context("refuses", func() {
			It("comp profiles on a zone whose default placement lacks the class, before creating the user", func(ctx SpecContext) {
				w := newWorld("radosgw")
				delete(w.classes, "COMP_ZLIB")
				Expect(w.run(ctx, w.config(gateway.Settings{Profiles: []string{"comp-small-put"}}))).To(MatchError(
					"the placement default-placement has no storage class COMP_ZLIB: run make populate RELEASE=tentacle first"))
				Expect(w.trace()).NotTo(ContainSubstring("user"))
				Expect(filepath.Join(w.dir(), "env.json")).NotTo(BeAnExistingFile())
			})

			DescribeTable("comp profiles on a class that compresses nothing",
				func(ctx SpecContext, compressor string) {
					w := newWorld("radosgw")
					w.classes["COMP_ZLIB"] = compressor
					Expect(w.run(ctx, w.config(gateway.Settings{Profiles: []string{"comp-large-get"}}))).To(MatchError(ContainSubstring(
						"the storage class COMP_ZLIB of the placement default-placement has no compressor, so the comp profiles would write uncompressed: " +
							"run make populate RELEASE=tentacle first")))
				},
				Entry("a class zone get gives no compression_type", ""),
				Entry("a class whose compressor is none", "none"),
			)

			It("comp profiles on a zone with no placement of the zonegroup's default name", func(ctx SpecContext) {
				w := newWorld("radosgw")
				w.placement = "other-placement"
				Expect(w.run(ctx, w.config(gateway.Settings{Profiles: []string{"comp-small-get"}}))).To(MatchError(
					"the zone has no placement other-placement, the zonegroup's default"))
			})

			It("nothing over the class for the profiles that do not write through it", func(ctx SpecContext) {
				w := newWorld("radosgw")
				delete(w.classes, "COMP_ZLIB")
				Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"small-put", "crc-put", "ver-put"}}))).To(Succeed())
				Expect(w.trace()).NotTo(ContainSubstring("radosgw-admin zone get"))
			})
		})
	})

	DescribeTable("renders a faked rgw-go run's directory with hack/bench/report",
		func(ctx SpecContext, s gateway.Settings, want, notWant []string) {
			gomod, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
			Expect(err).NotTo(HaveOccurred())
			root := filepath.Dir(strings.TrimSpace(string(gomod)))
			w := newWorld("rgw-go")
			host := harness.NewExecer(time.Second)
			w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
				if c.Name == "go" && slices.Contains(c.Args, "run") {
					w.record(w.line(c))
					// The report in this repository, not the world's.
					args := slices.Clone(c.Args)
					args[slices.Index(args, "-C")+1] = root
					return host.Output(ctx, harness.Cmd{Name: "go", Args: args})
				}
				return w.output(ctx, c)
			}
			s.Smoke = "1"
			Expect(w.run(ctx, w.config(s))).To(Succeed())
			Expect(w.stdout.String()).To(HaveSuffix(filepath.Join(w.dir(), "REPORT.md") + ": no phase had an error or a retry\n"))
			report := w.readFile("REPORT.md")
			Expect(report).To(HavePrefix("# Gateway comparison: tentacle\n"))
			for _, part := range want {
				Expect(report).To(ContainSubstring(part))
			}
			for _, part := range notWant {
				Expect(report).NotTo(ContainSubstring(part))
			}
			GinkgoWriter.Println(report)
		},
		Entry("of every profile", gateway.Settings{}, []string{
			"rgw-go is a smoke run, BENCH_SMOKE=1",
			"frontends `beast endpoint=127.0.0.1:7481`, TCP_NODELAY on",
			"cpu-small-put.pprof",
			"| anonymous GET / | 8 |",
			"\n### comp-small-put: throughput and latency\n",
			"| PUT 4 KiB, class COMP_ZLIB | 8 |",
			"| PUT 64 MiB in 8 MiB parts, class COMP_ZLIB (uploads/s) | 4 |",
			"| PUT 4 KiB, CRC32 | 8 |",
			"\n### ver-get: throughput and latency\n",
			"cpu-ver-put.pprof",
		}, nil, SpecTimeout(5*time.Minute)),
		Entry("of the profiles added after small-put's six alone", gateway.Settings{
			Profiles: []string{"comp-small-put", "comp-small-get", "comp-large-put", "comp-large-get", "crc-put", "ver-put", "ver-get"},
		}, []string{
			"\n### comp-large-put: throughput and latency\n",
			"| PUT 4 KiB, CRC32 | 8 |",
			"\n### ver-put: throughput and latency\n",
			"cpu-comp-large-get.pprof",
		}, []string{"\n## small-put\n", "anonymous GET /"}, SpecTimeout(5*time.Minute)),
	)
})

// exitTail is the commands the exit ran, from the bench buckets' removal
// on, for a run of gw that left buckets: a multi-object delete of the plain
// ones, and a removal of bench-ver, the versioned one, with its versions.
func exitTail(gw string, buckets ...string) string {
	const toolbox = "ROOKET_NAME=rgw-go-tentacle rooket kubectl -n rook-ceph exec deploy/rook-ceph-tools -- "
	const admin = toolbox + "radosgw-admin "
	const site = " --rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore\n"
	lookup := func(b string) string { return toolbox + "sh -c " + gateway.LookupScript + " sh " + b + site }
	var stats strings.Builder
	for _, b := range []string{"bench-small", "bench-large", "bench-large-mpu", "bench-list", "bench-ver"} {
		stats.WriteString(lookup(b))
	}
	plain := slices.DeleteFunc(slices.Clone(buckets), func(b string) bool { return b == "bench-ver" })
	if len(plain) > 0 {
		stats.WriteString("AWS_SECRET_ACCESS_KEY=bench-secret-0123456789abcdefghijklmnopqrstuv REPO/hack/bench/out/elbencho " +
			"--s3endpoints http://10.89.0.34:80 --s3key BENCH0000000000000AK --s3region ceph-objectstore --s3nocompress " +
			"--s3reqtimeout 30000 --nolive --s3multidel 1000 -t " + strconv.Itoa(len(plain)) + " " + strings.Join(plain, " ") + "\n")
	}
	if slices.Contains(buckets, "bench-ver") {
		// The bucket is read back after its removal, which exits 0 whether or
		// not it removed anything.
		stats.WriteString(admin + "bucket rm --bucket bench-ver --purge-objects" + site)
		stats.WriteString(lookup("bench-ver"))
	}
	down := ""
	if gw == "rgw-go" {
		down = "ROOKET=rooket REPO/hack/rooket/rgw-go-down.sh tentacle\n"
	}
	return stats.String() + "ROOKET=rooket REPO/hack/admin/remove-user.sh tentacle bench\n" + down +
		"ROOKET_NAME=rgw-go-tentacle rooket kubectl -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin gc process --include-all --rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore\n" +
		"ROOKET_NAME=rgw-go-tentacle rooket kubectl -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin gc list --include-all --rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore\n" +
		"ROOKET_NAME=rgw-go-tentacle rooket kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph --connect-timeout=20 --rados-mon-op-timeout=20 tell osd.* compact\n" +
		"ROOKET_NAME=rgw-go-tentacle rooket kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph --connect-timeout=20 --rados-mon-op-timeout=20 df -f json\n"
}
