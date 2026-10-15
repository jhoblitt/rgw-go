// Command report renders benchmark results into markdown reports.
//
//	go run ./hack/bench/report seam --dir hack/bench/out/<release>/seam-<date>
//
// seam reads a sweep directory that hack/bench/seam wrote and writes its
// REPORT.md there, then prints the report's path and which of the callback
// and pipe modes pass all four of the cgo question's criteria. It reads:
//
//   - every seam*.jsonl file, the cells BenchmarkSeam (test/bench/seam)
//     appended, keeping each cell's line with the largest n, since testing.B
//     calls a cell more than once while it calibrates b.N. Every mode must
//     have every cell another mode has.
//   - every overbudget*.jsonl file, cells run past the byte budget on
//     purpose, one per mode, shape, concurrency and pair of in-flight limits.
//   - floor.jsonl, one rados bench run per line, matched to the mirror shape
//     it names, read4k, write4k, read4m or write4m, at its concurrency.
//   - env.json, the run's environment, rendered as it is.
//   - every cpu-<mode>-read4k-256.pprof, through go tool pprof, which must
//     be on PATH.
//
// It renders nothing, and names what is missing, unless the directory holds
// env.json, floor.jsonl, seam-<mode>.jsonl and cpu-<mode>-read4k-256.pprof
// for each of sync, callback and pipe, and overbudget-<mode>.jsonl for
// callback and pipe with one cell whose in-flight limits were lifted and one
// whose were derived. A cell in a file named for another mode, or a floor
// line whose op or size is not its shape's, is refused.
//
//	go run ./hack/bench/report gateway --dir DIR [--dir DIR...] [--out FILE]
//
// gateway reads the run directories hack/bench/gateway wrote and renders
// them side by side, each run's columns in the order given and, beside every
// run but the first radosgw one, its ratio to that run: per profile, a row
// per variant and concurrency with requests, objects and MiB per second, the
// p50, p99 and p999 latencies, and the gateway's CPU per request, thread
// high-water, peak RSS, errors and retries. It reads:
//
//   - env.json, which names the release, the gateway, its frontends and their
//     TCP_NODELAY, and the run's settings; runs of different releases, of
//     different profiles, or that differ in a setting that shapes the
//     numbers, the phases' sizes, the client, the host or TCP_NODELAY, are
//     refused.
//   - every <profile>.json, elbencho's results, one line per phase. The
//     latency percentiles come from its latency histogram, as elbencho prints
//     them, since its JSON carries none.
//   - every noop-<t>.json, httpload's, whose rows sit beside small-get's at
//     the same concurrency.
//   - samples-<profile>.csv, the gateway command's sampler's, for the CPU,
//     threads and RSS.
//   - gctrace-<profile>.log, rgw-go's GODEBUG=gctrace=1 lines, for its GC
//     cycles and pauses; a line its JSON log tore, and a cycle missing from
//     the lines' numbers, count as cycles whose pauses are unknown, and the
//     report says how many lines were torn.
//   - config.json, the parity options.
//
// One run's report is written to its REPORT.md, several runs' to stdout, and
// either to --out when it is given; the command prints the file it wrote and
// whether any phase had an error or a retry.
//
// --dir can also come from REPORT_DIR, and every flag but --config from the
// config file --config names. The root's --log-level (debug, info, warn or
// error; info by default) and --log-format (json or text; json by default)
// set the logger, which writes to stderr. report exits 1 when it cannot
// render the directory; a criterion that fails is a finding, not an error.
//
// Managed by go-conventions (references/layout.md owns the main shape).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jhoblitt/rgw-go/hack/bench/report/internal/report"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := report.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	return 0
}
