// Command httpload drives one URL with anonymous GETs from a fixed number of
// goroutines for a fixed time, the gateway comparison's no-op baseline:
// elbencho issues object operations only, and cannot send a bare GET / with
// no Authorization header, which radosgw answers with the empty
// ListAllMyBucketsResult without touching RADOS (rgw_op.cc,
// RGWListBuckets::execute, "skipping list_buckets() for anonymous user").
//
//	httpload -url http://127.0.0.1:7481/ -threads 64 -duration 60s -out noop-64.json
//
// The goroutines share one keep-alive transport with an idle connection per
// goroutine, and read and discard every body. Each answer must be a 200 whose
// body holds a ListAllMyBucketsResult; any other answer, and any failed
// request, is an error, and httpload exits 1 when there is one, as the
// elbencho profiles fail on theirs. A request still in flight when the time
// is up is abandoned and counted nowhere. The result is one JSON object:
// threads, duration_s (the measured window), requests (those that ended,
// failed ones included), errors, ops_per_s, lat_us (p50, p99 and p999 of the
// listings, in µs, nearest rank), start (when the window opened) and command.
//
// It takes only the standard library's flags, single-dash as the gateway
// command passes them, and exits 2 on a bad command line.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Result is what httpload writes.
type Result struct {
	Threads   int       `json:"threads"`
	DurationS float64   `json:"duration_s"`
	Requests  int64     `json:"requests"`
	Errors    int64     `json:"errors"`
	OpsPerS   float64   `json:"ops_per_s"`
	LatUs     Latency   `json:"lat_us"`
	Start     time.Time `json:"start"`
	Command   string    `json:"command"`
}

// Latency holds the latency percentiles of the successful requests.
type Latency struct {
	P50  float64 `json:"p50"`
	P99  float64 `json:"p99"`
	P999 float64 `json:"p999"`
}

// listing is what the body of an answer to the anonymous GET / must hold.
var listing = []byte("<ListAllMyBucketsResult")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run parses args, loads the URL and writes the result, returning the exit
// status: 0, 1 when a request failed, or 2 for a bad command line.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("httpload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "the URL to GET, anonymously")
	threads := fs.Int("threads", 1, "goroutines, each with one request in flight")
	duration := fs.Duration("duration", 60*time.Second, "how long to send requests")
	out := fs.String("out", "", "the result file; stdout when empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var usage []string
	if *url == "" {
		usage = append(usage, "-url is required")
	}
	if *threads < 1 {
		usage = append(usage, "-threads must be at least 1")
	}
	if *duration <= 0 {
		usage = append(usage, "-duration must be positive")
	}
	if len(usage) > 0 {
		fmt.Fprintf(stderr, "httpload: %s\n", strings.Join(usage, "; "))
		return 2
	}

	r, failures := load(ctx, *url, *threads, *duration)
	r.Command = strings.Join(append([]string{"httpload"}, args...), " ")
	data, err := json.Marshal(r)
	if err != nil {
		fmt.Fprintf(stderr, "httpload: %v\n", err)
		return 1
	}
	data = append(data, '\n')
	if *out == "" {
		_, err = stdout.Write(data)
	} else {
		err = os.WriteFile(*out, data, 0o600)
	}
	if err != nil {
		fmt.Fprintf(stderr, "httpload: writing the result: %v\n", err)
		return 1
	}
	if r.Errors > 0 {
		fmt.Fprintf(stderr, "httpload: %d of %d requests failed: %s\n", r.Errors, r.Requests, failures)
		return 1
	}
	return 0
}

// worker is one goroutine's tally.
type worker struct {
	lat      []time.Duration
	requests int64
	failures map[string]int64
}

// load sends GETs to url from threads goroutines until duration has passed or
// ctx ends, and returns the result and a summary of the failures by kind.
func load(ctx context.Context, url string, threads int, duration time.Duration) (r Result, failures string) {
	transport := &http.Transport{
		MaxIdleConns:        threads,
		MaxIdleConnsPerHost: threads,
		DisableCompression:  true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	start := time.Now()
	ctx, cancel := context.WithDeadline(ctx, start.Add(duration))
	defer cancel()
	workers := make([]worker, threads)
	var wg sync.WaitGroup
	for i := range workers {
		w := &workers[i]
		w.failures = map[string]int64{}
		wg.Go(func() { w.loop(ctx, client, url) })
	}
	wg.Wait()
	elapsed := time.Since(start)

	r = Result{Threads: threads, DurationS: elapsed.Seconds(), Start: start}
	var lat []time.Duration
	byKind := map[string]int64{}
	for i := range workers {
		w := &workers[i]
		r.Requests += w.requests
		lat = append(lat, w.lat...)
		for kind, n := range w.failures {
			byKind[kind] += n
			r.Errors += n
		}
	}
	if elapsed > 0 {
		r.OpsPerS = float64(r.Requests) / elapsed.Seconds()
	}
	slices.Sort(lat)
	r.LatUs = Latency{P50: percentile(lat, 0.50), P99: percentile(lat, 0.99), P999: percentile(lat, 0.999)}
	var kinds []string
	for _, kind := range slices.Sorted(maps.Keys(byKind)) {
		kinds = append(kinds, fmt.Sprintf("%s ×%d", kind, byKind[kind]))
	}
	return r, strings.Join(kinds, ", ")
}

// loop sends one GET after another until ctx ends.
func (w *worker) loop(ctx context.Context, client *http.Client, url string) {
	for ctx.Err() == nil {
		t0 := time.Now()
		ok, kind := get(ctx, client, url)
		if ctx.Err() != nil {
			return
		}
		w.requests++
		if !ok {
			w.failures[kind]++
			continue
		}
		w.lat = append(w.lat, time.Since(t0))
	}
}

// get sends one anonymous GET and reads its body, and reports whether the
// answer was the bucket listing; when not, kind names what it was instead.
func get(ctx context.Context, client *http.Client, url string) (ok bool, kind string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false, err.Error()
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	body, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return false, "reading the body: " + err.Error()
	case resp.StatusCode != http.StatusOK:
		return false, resp.Status
	case !bytes.Contains(body, listing):
		return false, "200 without a ListAllMyBucketsResult"
	}
	return true, ""
}

// percentile is the nearest-rank p-quantile of the sorted latencies, in µs.
func percentile(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := max(int(math.Ceil(p*float64(len(sorted))))-1, 0)
	return float64(sorted[i].Nanoseconds()) / 1e3
}
