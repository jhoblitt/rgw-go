// Command report renders benchmark results into markdown reports.
//
//	go run ./hack/bench/report seam --dir hack/bench/out/<release>/seam-<date>
//
// seam reads a sweep directory that hack/bench/seam.sh wrote and writes its
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
// env.json, floor.jsonl, and seam-<mode>.jsonl and
// cpu-<mode>-read4k-256.pprof for each of sync, callback and pipe.
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
