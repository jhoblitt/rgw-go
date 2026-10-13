// Command seam re-runs the seam microbenchmark's over-budget cell under
// the in-flight limiter against one release's rooket cluster:
//
//	make bench-seam RELEASE=squid|tentacle BENCH_LIMITER=1 [BENCH_PLAN=1] [BENCH_INFLIGHT_BYTES=N]
//	go run ./hack/bench/seam squid|tentacle --limiter=1 [--plan] [--inflight-bytes N] ...
//
// It runs write4m at 64 in flight past the byte budget, -budget-bytes=0, in
// sync, callback and pipe mode, each in a benchmark process of its own, with
// the limiter's byte bound at --inflight-bytes (104857600, librados's default
// objecter_inflight_op_bytes, by default) and its operation bound derived, as
// rgw-go runs. Each process runs --over-objects iterations (640, or 128 with
// --quick) and appends its cells to overbudget-<mode>.jsonl in
// hack/bench/out/<release>/seam-<UTC time>-limited/, or -limited-<bytes>/ for
// another bound, beside env.json, sweep.log, noise.log and
// bench-<mode>.log. It renders no REPORT.md: hack/bench/report renders only
// a whole sweep.
//
// Every setting is checked before anything touches a cluster or creates a
// file, and --plan prints the run directory and each process's arguments and
// watchdog limit, then exits, so that a plan that prints means the run's
// settings pass. A run then checks that the release's ceph.conf and image
// exist, builds the benchmark binary, waits for --min-avail-gib (5) of free
// space in the pool before each process, and writes env.json. A watchdog
// stops a process past its limit, --timeout or a tenth of a second an object
// plus five minutes, with SIGTERM and then SIGKILL 30 s later; a process
// that fails, stopped or not, fails the run once what it left in the pool is
// removed, and the run records why in sweep.log as well as on stderr.
//
// No command can hang the run. Each runs in a process group of its own, so
// that the kubectl rooket starts dies with it, and is killed past its bound:
// three minutes a call to the cluster, no more than the space wait has left
// for each of its polls, five minutes for the whole removal after a failed
// process, fifteen minutes for the benchmark's build, and one minute for
// each other command on this host (ldd, rpm and dpkg, git, go version and
// rooket version). rados itself fails any OSD or monitor operation that
// takes over two minutes.
//
// Every flag can also come from the environment: --limiter, --plan,
// --inflight-bytes, --over-objects, --quick, --timeout, --min-avail-gib and
// --repo as BENCH_ and the name upper-cased with "-" as "_" (BENCH_PLAN,
// BENCH_INFLIGHT_BYTES), and --ceph-conf, --rooket and --go-tags as
// RGW_GO_TEST_CEPH_CONF, ROOKET and GO_TAGS, the names hack/bench/seam.sh
// reads; and from the config file --config names. The command line wins over
// the environment, and the environment over the file. --limiter, --plan and
// --quick take 1 or nothing (--plan alone is --plan=1); any other value is
// refused. The root's --log-level
// (debug, info, warn or error; info by default) and --log-format (json or
// text; json by default) set the logger, which writes to stderr and, during
// a run, to sweep.log.
//
// Managed by go-conventions (references/layout.md owns the main shape).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := seam.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "seam:", err)
		return 1
	}
	return 0
}
