// Command seam sweeps the seam microbenchmark (test/bench/seam) against one
// release's rooket cluster and measures its floor with rados bench, run in a
// container on the host where the Go process runs. It writes
// hack/bench/out/<release>/seam-<UTC time>/ and renders its REPORT.md with
// hack/bench/report:
//
//	make bench-seam RELEASE=squid|tentacle [BENCH_QUICK=1] [BENCH_PLAN=1] ...
//	go run ./hack/bench/seam squid|tentacle [--quick] [--plan] ...
//
// The floor's rados comes from the floor image, the Ceph image of the release
// of the librados the benchmark binary links, so that both sides use one
// client release; the sweep stops when no such image can be found. Where the
// cluster's own image is another release, its rados bench also runs once at
// each cell the criteria compare, for comparison only.
//
// Each cell runs in each mode in a benchmark process of its own: the runtime
// keeps the OS threads a process grows, so a later cell of the same process
// would start from an earlier one's threads, and go-ceph's completion
// notifier is process-wide. The head and index shapes run at CONC, and read4k
// and write4k at the concurrencies of CONC the throughput and latency
// criteria do not compare, each followed by one floor run. Each cell the
// criteria compare, read4k and write4k at 64 and 256 and write4m at
// BENCH_LARGE_CONC, runs between two floor runs, one just before its three
// modes and one just after, so that the floor brackets the cell rather than
// running long after it; so does read4m at BENCH_LARGE_CONC, which the report
// shows against its floor but does not judge, since rados bench's 4 MiB rand
// does not read as the Go cell does. Then, per asynchronous mode, write4m
// runs at 64 in flight past the byte budget, once with the in-flight
// limiter's bounds above what the cell reaches and once with them derived
// from the objecter throttle; and, per mode, read4k runs at 256 under a CPU
// profile.
//
// A 4 MiB write cell or floor run writes BENCH_WRITE4M_OBJECTS objects, not
// a time window, so that both sides write the same bytes, and starts only
// once the pool reports BENCH_MIN_AVAIL_GIB available. Each rand floor reads
// among conc objects that a write wrote for it, as each seam worker reads its
// own object. Every floor write passes --no-hints: rados bench otherwise sends
// an allocation hint with each write, and the Go cells send none.
//
// BENCH_LIMITER=1 (--limiter=1) re-runs only the over-budget cell, under the
// in-flight limiter: write4m at 64 in flight past the byte budget in sync,
// callback and pipe mode, each in a benchmark process of its own, with the
// limiter's byte bound at BENCH_INFLIGHT_BYTES (104857600, librados's default
// objecter_inflight_op_bytes) and its operation bound derived, as rgw-go
// runs. Each process runs BENCH_OVER_OBJECTS iterations and appends its cells
// to overbudget-<mode>.jsonl in hack/bench/out/<release>/seam-<UTC
// time>-limited/, or -limited-<bytes>/ for another bound. It runs no floor and
// renders no REPORT.md, since hack/bench/report renders only a whole sweep,
// and refuses the settings only the sweep reads.
//
// Every setting is checked before anything touches a cluster or creates a
// file, and BENCH_PLAN=1 prints the run directory and the schedule, each
// benchmark process with its cell and watchdog limit, each floor run and each
// wait for the pool's space, then exits, so that a plan that prints means the
// run's settings pass. A run then checks that the release's ceph.conf and
// image exist, builds the benchmark binary and writes env.json; the run
// directory also holds sweep.log, the run's log; noise.log, the load and the
// busiest processes every 15 s, so that a busy host shows beside the cell it
// touched; bench-<mode>.log, the benchmark processes' output; and, for the
// sweep, seam-<mode>.jsonl, floor.jsonl, <floor run>.txt, the measured rados
// bench runs' output, floor.log, the floor's other rados output, and the CPU
// profiles.
//
// A run that fails partway removes what it left in the pool, the objects of
// the floor namespaces it wrote to and of the benchmark process that failed,
// and exits with the failure's status, whether or not the removal succeeds:
// 130 or 143 when SIGINT or SIGTERM interrupted it, a benchmark process's own
// when one failed, 1 otherwise. It records why in sweep.log as well as on
// stderr.
//
// Nothing else bounds a call stuck in librados: go test's -timeout does not
// apply to benchmarks (testing.(*M).Run stops its alarm before it runs them),
// and a sync-mode call returns only when librados does. So a watchdog stops a
// benchmark process that outlives its limit, SIGTERM and then SIGKILL 30 s
// later, and kills a floor run's container by name, then its client, once it
// outlives its own; the run then fails into the removal above. A benchmark
// process runs one cell, and its limit is that cell's planned duration and
// five minutes; a floor run's is its window and five minutes; the floor's
// rados also fails any OSD or monitor operation that takes over two minutes.
//
// No other command can hang the run either. Each runs in a process group of
// its own, so that the kubectl rooket starts dies with it, and is killed past
// its bound: three minutes a call to the cluster, no more than the space wait
// has left for each of its polls, thirty minutes for each namespace's removal
// after a failure, fifteen minutes for the benchmark's build, the report's and
// the floor image's pull, and one minute for each other command on this host.
//
// Environment, each also a flag (--help lists them):
//
//	BENCH_TIME              benchtime of each time-bounded cell (20s), in whole hours,
//	                        minutes and seconds (20s, 1m30s, or plain seconds), since the
//	                        watchdog's limits derive from it; go test's 1.5s, 500ms and
//	                        100x are refused at startup
//	BENCH_FLOOR_SECONDS     seconds of each time-bounded rados bench run (20)
//	CONC                    concurrencies of the 4 KiB, head and index shapes (1,16,64,256,512)
//	BENCH_LARGE_CONC        concurrencies of read4m and write4m (1,4,16); empty skips them
//	BENCH_WRITE4M_OBJECTS   objects per 4 MiB write cell and floor run (1000, 3.9 GiB)
//	BENCH_OVER_OBJECTS      objects per over-budget cell (640)
//	BENCH_MIN_AVAIL_GIB     pool space a 4 MiB write waits for (5)
//	BENCH_TIMEOUT           limit of each benchmark process, in seconds or as 1h30m (its
//	                        cell's planned duration, ten times BENCH_TIME or 0.1 s an
//	                        iteration of a count-bounded cell, plus 5m)
//	BENCH_FLOOR_TIMEOUT     limit of each floor rados run (its window, plus 5m)
//	BENCH_QUICK=1           5s, 5, CONC=16,256, BENCH_LARGE_CONC=16, 100 and 128 objects;
//	                        any other value is refused
//	BENCH_PLAN=1            print the schedule and exit
//	BENCH_LIMITER=1         re-run only the over-budget cell under the in-flight limiter
//	BENCH_INFLIGHT_BYTES    the limiter re-run's byte bound (104857600)
//	GO_TAGS                 build tags besides integration (ceph_preview)
//	BENCH_FLOOR_IMAGE       floor image (quay.io/ceph/ceph:v<linked librados version>); must run that release
//	ROOKET_ENGINE           container engine that runs rados bench (podman)
//	RGW_GO_TEST_CEPH_CONF   unset, or hack/rooket/out/<release>/ceph.conf: the Go cells
//	                        and the floor must reach the same cluster
//	ROOKET                  the rooket binary (rooket)
//
// Each flag above reads the variable beside it and no other name: --bench-time
// reads BENCH_TIME, --conc CONC and --engine ROOKET_ENGINE. The flags not
// listed, --repo, --config, --log-level and --log-format, read BENCH_ and the
// flag's name upper-cased with "-" as "_" (--repo as BENCH_REPO). Any flag can
// also come from the config file --config (BENCH_CONFIG) names. The command
// line wins over the environment, and the environment over the file. --plan
// and --quick alone mean 1. The run's first log record and env.json name the
// config file read, if any, and the repository, rooket and build tags in
// effect. The root's --log-level (debug, info, warn or error; info by default)
// and --log-format (json or text; json by default) set the logger, which
// writes to stderr and, during a run, to sweep.log.
//
// Managed by go-conventions (references/layout.md owns the main shape).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := harness.NotifyContext(context.Background())
	defer stop()
	if err := seam.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "seam:", err)
		return seam.ExitStatus(ctx, err)
	}
	return 0
}
