// Command gateway runs the gateway comparison's profiles against one gateway
// of one release's populated rooket cluster, radosgw or the rgw-go on the
// host, and renders the run's REPORT.md with hack/bench/report:
//
//	make bench-gateway RELEASE=squid|tentacle GATEWAY=radosgw|rgw-go [PROFILES=...] [BENCH_SMOKE=1] ...
//	go run ./hack/bench/gateway squid|tentacle radosgw|rgw-go [PROFILE...] [--smoke] [--plan] ...
//
// Each run writes hack/bench/out/<release>/gateway-<gateway>-<UTC time>/:
//
//	env.json                the host, the cluster, the gateway and the settings
//	config.json             the parity options as each gateway's entity holds them
//	<profile>.json, .csv    elbencho's results, one line per measured phase
//	noop-<t>.json           httpload's result at t goroutines
//	<profile>.log           what elbencho, httpload and the preparation printed
//	samples-<profile>.csv   the gateway process once a second
//	cpu-<profile>.pprof     rgw-go only: a CPU profile of the profile's last phase
//	gctrace-<profile>.log   rgw-go only: the GC lines it logged during the profile
//	run.log                 what ran when: the command's log and the output of
//	                        what it ran outside the profiles
//
// Profiles, in the order given (all of them by default):
//
//	small-put  4 KiB PUTs at each concurrency of BENCH_CONC, BENCH_PUT_OBJECTS per thread
//	small-get  4 KiB GETs at each concurrency, looping over a read set written first
//	noop       httpload's anonymous GET / at each concurrency, which radosgw answers
//	           without touching RADOS
//	large-put  64 MiB PUTs, whole and in 8 MiB parts, each removed after it
//	large-get  64 MiB objects read in 4 MiB ranges and whole, looping over a set written first
//	list       ListObjectsV2 over BENCH_LIST_THREADS * BENCH_LIST_OBJECTS keys, from one
//	           thread and with one prefix per thread
//
// Every measured phase is bounded by BENCH_SECONDS; a phase that finishes its
// objects first ends early. A read phase loops over objects written for it
// before it starts, so it never reads one that is not there. Each phase and
// each preparation is followed by two seconds of quiet, so that the samples a
// phase's CPU is read between see little of its neighbors.
//
// The clusters' one OSD holds 10 GiB, and the parity options turn radosgw's
// garbage collector off. Each profile's dataset, the most object data it
// holds at once, is sized from its threads, objects per thread and object
// size: small-put's lower concurrencies overwrite keys its top one writes,
// the large profiles write BENCH_LARGE_THREADS * BENCH_LARGE_OBJECTS objects
// of 64 MiB at a time and remove them before the next write, and list's keys
// are empty. A run starts by collecting any garbage left queued and reading
// the data pool's free space, and refuses to start when a profile's dataset is
// more than half of it; the other half is room for the index and the OSD's own
// metadata. Before each write the run waits for twice that write's dataset to
// be free. After each profile it removes every bench object and collects the
// garbage, so the next profile starts from the space the run started with.
//
// Both gateways run with TCP_NODELAY on, and the run refuses a gateway whose
// frontends leave it off: radosgw's beast sets it only for tcp_nodelay=1
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_frontend.cc#L702-L706,
// :1165 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L643-L647,
// :1083 at v20.2.4), which the chart values give it, and rgw-go keeps Go's
// unless its frontend sets tcp_nodelay to anything else (docs/exclusions.md,
// "TCP options"). env.json records the gateway's frontends and the TCP_NODELAY
// they give.
//
// elbencho signs as the user bench, whose keys are fixed, so that every run
// writes the same commands; it is created through radosgw-admin and removed
// with its buckets whenever the run ends, so that make gate passes after it.
// The run then collects the garbage that leaves and compacts the OSD, so that
// the next run, rgw-go's after radosgw's, starts on an OSD without the
// tombstones this one's deletes left. For rgw-go the command restarts it
// through hack/rooket/rgw-go-up.sh with GODEBUG=gctrace=1 and RGW_GO_FLAGS, and
// stops it through rgw-go-down.sh when the run ends; a radosgw run refuses to
// start while an rgw-go runs against the cluster. Both refuse a gateway whose
// entity does not hold the parity options, which rgw-go-up.sh sets; the
// command reads them from the release's chart values, which make
// parity-options-check holds equal to hack/rooket/lib.sh's parity_options.
//
// The sampler reads the gateway's process once a second, appending to
// samples-<profile>.csv, under the header time,utime,stime,threads,rss_kib,
// the host's time in seconds since the epoch at the middle of the read, the
// process's user and system CPU in seconds (/proc/<pid>/stat fields 14 and
// 15), its thread count (Threads) and its resident memory in KiB (VmRSS, of
// /proc/<pid>/status). For rgw-go it reads /proc on the host; for radosgw it
// reads the one process named radosgw in the rgw container of its Rook pod
// through rooket's kubectl exec, since Rook gives the pod a shared process
// namespace when its log collector runs (rook pkg/operator/ceph/object/
// spec.go), so the radosgw is not PID 1 there. A sample that fails is tried
// once more and then skipped; the report interpolates the CPU across the gap.
//
// Every setting is checked before anything touches a cluster or creates a
// file. BENCH_PLAN=1 then prints each profile's dataset against
// BENCH_IDLE_AVAIL and exits, failing when one needs more; make
// bench-plan-check, part of make check, runs it for the defaults. A run that
// fails partway, or that SIGINT or SIGTERM interrupts, still removes the bench
// user with its buckets, stops the rgw-go it started, collects the garbage and
// compacts the OSD, and exits 130 or 143 when interrupted, 1 for any other
// failure. A failed removal or compaction fails the run; a failed stop of
// rgw-go is logged and leaves the outcome as it was.
//
// No command can hang the run: each runs in a process group of its own,
// killed whole past its bound: three minutes a call to the cluster, ten
// seconds a read of the radosgw pod's process, BENCH_SECONDS and five minutes
// a measured phase, an httpload run or a CPU profile, thirty minutes each
// unmeasured elbencho command, a preparation or a removal, remove-user.sh,
// and the OSD's compaction, which returns only once the OSD has compacted,
// fifteen minutes each go build, the report and rgw-go-up.sh, which can
// restart the radosgw, an hour elbencho's build, two minutes rgw-go-down.sh,
// and one minute each other command on this host.
//
// Environment, each also a flag (--help lists them):
//
//	BENCH_SECONDS        each measured phase's time limit (60)
//	BENCH_CONC           the small and noop profiles' concurrencies ("1 8 32 64 128 256")
//	BENCH_PUT_OBJECTS    small-put's objects per thread (3000)
//	BENCH_GET_OBJECTS    small-get's read set, objects per thread at the highest concurrency (400)
//	BENCH_LARGE_THREADS  the large profiles' threads (32)
//	BENCH_LARGE_OBJECTS  the large profiles' objects per thread (1)
//	BENCH_LIST_THREADS   the list profile's threads (64)
//	BENCH_LIST_OBJECTS   the list profile's keys per thread (1600)
//	BENCH_SMOKE=1        a run that only proves the tooling: 5 s phases, BENCH_CONC=8, 50
//	                     objects per reader, 4 large threads and 16 keys per list thread;
//	                     explicit settings still win. Its numbers measure nothing. Any
//	                     other value is refused.
//	BENCH_PLAN=1         print each profile's dataset and the free space it needs against
//	                     BENCH_IDLE_AVAIL bytes free, without touching the cluster, and fail
//	                     when one needs more. Any other value is refused.
//	BENCH_IDLE_AVAIL     the data pool's free bytes a plan is judged against; refused
//	                     without BENCH_PLAN=1
//	RGW_GO_FLAGS         rgw-go serve flags for rgw-go-up.sh, such as --rados-completions=pipe
//	GO_TAGS              build tags (ceph_preview); CGO_* come from the environment
//	ROOKET               the rooket binary (rooket)
//
// Counts and concurrencies are whole numbers from 1 to 9 digits, each
// concurrency and each profile given at most once. RGW_GO_FLAGS, GO_TAGS and
// ROOKET are read from those names only. The other flags read BENCH_ and the
// flag's name upper-cased with "-" as "_" (--repo as BENCH_REPO), and every
// flag the config file --config (BENCH_CONFIG) names. The command line wins
// over the environment, and the environment over the file. --smoke and --plan
// alone mean 1. The run's first log record and env.json name the config file
// read, if any, and the repository, rooket and build tags in effect. The
// root's --log-level (debug, info, warn or error; info by default) and
// --log-format (json or text; json by default) set the logger, which writes to
// stderr and, during a run, to run.log.
//
// Managed by go-conventions (references/layout.md owns the main shape).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := harness.NotifyContext(context.Background())
	defer stop()
	if err := gateway.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		return gateway.ExitStatus(ctx, err)
	}
	return 0
}
