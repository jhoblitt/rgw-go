//go:build integration

package seam_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
	"github.com/jhoblitt/rgw-go/test/bench/seam"
)

var (
	modeFlag    = flag.String("mode", string(goceph.ModeCallback), "completion mode: sync, callback or pipe")
	concFlag    = flag.String("conc", "1,16,64,256,512", "operations in flight per cell, comma separated")
	shapesFlag  = flag.String("shapes", "", "shapes to run, comma separated; every shape when empty")
	resultsFlag = flag.String("results", "", "append one JSON line per call of a cell to this file; a cell's measurement is its line with the largest n")
	budgetFlag  = flag.Int64("inflight-bytes", 96<<20, "skip a cell whose conc times budget_bytes, the data extent one iteration writes or asks to read, exceeds this. librados's objecter throttles in-flight bytes at 100 MiB, past which submission blocks an OS thread inside C in every mode; goceph's in-flight limiter parks submissions in Go from 15/16 of that. 0 disables the cap.")
	cellTimeout = flag.Duration("cell-timeout", 5*time.Minute, "fail a call of a cell, its Prepare and iterations, or a cell's cleanup, that has not finished by then")
)

// Cell is one JSON result line.
type Cell struct {
	Time    string `json:"time"`
	Release string `json:"release"`
	Mode    string `json:"mode"`
	Shape   string `json:"shape"`
	// BudgetBytes is the shape's Size, the data extent one iteration writes
	// or asks to read, which -inflight-bytes counts. It is not what an
	// iteration transfers: headread4k asks for 4 MiB of a 4 KiB object.
	BudgetBytes int     `json:"budget_bytes"`
	Ops         int     `json:"ops_per_iter"`
	Conc        int     `json:"conc"`
	N           int     `json:"n"`
	NsPerOp     float64 `json:"ns_per_iter"`
	OpsPerSec   float64 `json:"ops_per_sec"`
	P50us       float64 `json:"p50_us"`
	P99us       float64 `json:"p99_us"`
	P999us      float64 `json:"p999_us"`
	MeanUs      float64 `json:"mean_us"`
	CPUusPerOp  float64 `json:"cpu_us_per_op"`
	ThreadsIdle int     `json:"threads_idle"`
	ThreadsPeak int     `json:"threads_peak"`
	GOMAXPROCS  int     `json:"gomaxprocs"`
	Errors      int     `json:"errors"`
}

func BenchmarkSeam(b *testing.B) {
	conf := os.Getenv(cephtest.ConfEnv)
	if conf == "" {
		b.Fatalf("%s is not set; point it at hack/rooket/out/<release>/ceph.conf", cephtest.ConfEnv)
	}
	concs, err := parseConc(*concFlag)
	if err != nil {
		b.Fatal(err)
	}
	shapes := seam.Shapes()
	if *shapesFlag != "" {
		if shapes, err = seam.ByName(strings.Split(*shapesFlag, ",")); err != nil {
			b.Fatal(err)
		}
	}
	ctx := b.Context()
	cluster, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf, Mode: goceph.Mode(*modeFlag)})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if closeErr := cluster.Close(); closeErr != nil {
			b.Error(closeErr)
		}
	})
	release, err := goceph.Release(ctx, cluster)
	if err != nil {
		b.Fatal(err)
	}
	pool, err := cluster.Pool(ctx, cephtest.TestPool, "bench-"+strconv.Itoa(os.Getpid()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if closeErr := pool.Close(); closeErr != nil {
			b.Error(closeErr)
		}
	})
	label := filepath.Base(filepath.Dir(conf))
	for _, sh := range shapes {
		for _, c := range concs {
			if *budgetFlag > 0 && int64(c)*int64(sh.Size()) > *budgetFlag {
				b.Logf("skipping %s at %d: %d budgeted bytes in flight exceeds -inflight-bytes", sh.Name(), c, c*sh.Size())
				continue
			}
			// The Go runtime keeps an idle OS thread rather than destroying
			// it, so on the seam's paths a thread count only rises. Counting
			// before the cell's first call holds what its calibration calls
			// grew against the cell, and removing its objects only after its
			// last call keeps the threads sync mode parks for the removals
			// from counting as any cell's growth.
			idle, idleErr := seam.OSThreads()
			if idleErr != nil {
				b.Fatal(idleErr)
			}
			b.Run(fmt.Sprintf("shape=%s/conc=%d", sh.Name(), c), func(b *testing.B) {
				runCell(b, ctx, pool, release, sh, c, idle, label)
			})
			cleanupCtx, cancel := context.WithTimeout(ctx, *cellTimeout)
			cleanupErr := sh.Cleanup(cleanupCtx, pool)
			cancel()
			if cleanupErr != nil {
				b.Errorf("cleaning up after %s at %d: %v", sh.Name(), c, cleanupErr)
			}
		}
	}
}

// runCell issues b.N iterations of sh from conc workers and reports them
// against idle, the thread count before the cell's first call.
func runCell(b *testing.B, ctx context.Context, pool radosclient.Pool, r denc.Release, sh seam.Shape, conc, idle int, label string) {
	// The deadline fails a stuck call rather than hanging the sweep: past it
	// Prepare and every iteration fail at once, and in the asynchronous
	// modes an operation still waiting gives up.
	cctx, cancel := context.WithTimeout(ctx, *cellTimeout)
	defer cancel()
	if err := sh.Prepare(cctx, pool, r, conc); err != nil {
		b.Fatalf("preparing %s: %v", sh.Name(), err)
	}
	lat := make([]time.Duration, b.N)
	var (
		next, failed atomic.Int64
		mu           sync.Mutex
		first        error
		wg           sync.WaitGroup
	)
	cpu0, err := seam.CPUTime()
	if err != nil {
		b.Fatal(err)
	}
	sampler := seam.NewSampler(10 * time.Millisecond)
	b.ResetTimer()
	for w := range conc {
		wg.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				if i >= b.N {
					return
				}
				start := time.Now()
				runErr := sh.Run(cctx, pool, w, i)
				lat[i] = time.Since(start)
				if runErr != nil {
					failed.Add(1)
					mu.Lock()
					if first == nil {
						first = runErr
					}
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	b.StopTimer()
	peak := sampler.Stop()
	cpu1, err := seam.CPUTime()
	if err != nil {
		b.Fatal(err)
	}

	p := seam.Summarize(lat)
	ops := b.N * sh.Ops()
	opsPerSec := float64(ops) / b.Elapsed().Seconds()
	cpuPerOp := micros(cpu1-cpu0) / float64(ops)
	b.ReportMetric(micros(p.P99), "p99-us")
	b.ReportMetric(micros(p.P50), "p50-us")
	b.ReportMetric(cpuPerOp, "cpu-us/op")
	b.ReportMetric(float64(peak), "threads")
	b.ReportMetric(opsPerSec, "ops/s")
	if *resultsFlag != "" {
		err = appendCell(*resultsFlag, Cell{
			Time:        time.Now().UTC().Format(time.RFC3339),
			Release:     label,
			Mode:        *modeFlag,
			Shape:       sh.Name(),
			BudgetBytes: sh.Size(),
			Ops:         sh.Ops(),
			Conc:        conc,
			N:           b.N,
			NsPerOp:     float64(b.Elapsed().Nanoseconds()) / float64(b.N),
			OpsPerSec:   opsPerSec,
			P50us:       micros(p.P50),
			P99us:       micros(p.P99),
			P999us:      micros(p.P999),
			MeanUs:      micros(p.Mean),
			CPUusPerOp:  cpuPerOp,
			ThreadsIdle: idle,
			ThreadsPeak: peak,
			GOMAXPROCS:  runtime.GOMAXPROCS(0),
			Errors:      int(failed.Load()),
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	if n := failed.Load(); n > 0 {
		b.Fatalf("%d of %d iterations failed; first: %v", n, b.N, first)
	}
}

// parseConc parses -conc, positive counts separated by commas.
func parseConc(s string) ([]int, error) {
	var concs []int
	for f := range strings.SplitSeq(s, ",") {
		c, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || c < 1 {
			return nil, fmt.Errorf("-conc %q: %q is not a positive count", s, f)
		}
		concs = append(concs, c)
	}
	return concs, nil
}

// appendCell appends c to path as one JSON line.
func appendCell(path string, c Cell) error {
	line, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding the %s cell: %w", c.Shape, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

func micros(d time.Duration) float64 { return float64(d) / float64(time.Microsecond) }
