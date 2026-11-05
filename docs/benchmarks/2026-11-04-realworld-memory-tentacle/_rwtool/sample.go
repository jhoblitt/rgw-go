package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sample is one line of a sampler's output.
type Sample struct {
	T float64 `json:"t"`
	// Status holds /proc/PID/status's VmRSS, RssAnon, RssFile, RssShmem and
	// VmHWM in kB, and Threads.
	Status map[string]int64 `json:"status"`
	// Rollup holds smaps_rollup's Rss, Pss, Pss_Anon and Pss_File in kB,
	// absent where the sampler may not read it (radosgw's, from the host).
	Rollup map[string]int64 `json:"rollup,omitempty"`
	// Cg holds the cgroup's memory.current and memory.peak as current and
	// peak, and memory.stat's fields, all in bytes.
	Cg map[string]int64 `json:"cg"`
	// Go holds the selected series of the gateway's /metrics.
	Go map[string]float64 `json:"go,omitempty"`
}

var (
	statusKeys = []string{"VmRSS", "RssAnon", "RssFile", "RssShmem", "VmHWM", "Threads"}
	rollupKeys = []string{"Rss", "Pss", "Pss_Anon", "Pss_File"}
	cgStatKeys = []string{
		"anon", "file", "active_file", "inactive_file", "kernel", "kernel_stack", "pagetables",
		"sock", "shmem", "file_mapped", "slab", "pgfault", "pgmajfault",
	}
	// metricPrefixes are the /metrics series a sample keeps.
	metricPrefixes = []string{
		"go_memstats_", "go_gc_gomemlimit_bytes", "go_gc_gogc_percent", "go_gc_duration_seconds_count",
		"go_threads", "go_goroutines", "process_resident_memory_bytes", "rgw_go_memory_budget_",
	}
	// summedMetrics are series kept as one sum over their labels.
	summedMetrics = []string{"rgw_go_requests_in_flight", "rgw_go_request_bytes_total", "rgw_go_requests_total"}
)

func sampleCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sample", flag.ContinueOnError)
	pid := fs.Int("pid", 0, "the process")
	cg := fs.String("cgroup", "", "the process's cgroup directory")
	out := fs.String("out", "", "the JSON-lines file samples are appended to; sampling stops when OUT.stop exists")
	metrics := fs.String("metrics", "", "HOST:PORT of the gateway's /metrics, empty for none")
	interval := fs.Duration("interval", time.Second, "the sampling interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pid <= 0 || *cg == "" || *out == "" {
		return errors.New("sample needs -pid, -cgroup and -out")
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	client := &http.Client{Timeout: *interval * 9 / 10}
	proc := fmt.Sprintf("/proc/%d", *pid)
	rollup := true
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		if _, err := os.Stat(*out + ".stop"); err == nil {
			return nil
		}
		s := Sample{T: float64(time.Now().UnixNano()) / 1e9}
		b, err := os.ReadFile(proc + "/status")
		if err != nil {
			// The process has gone.
			return nil
		}
		s.Status = parseKV(b, statusKeys)
		if rollup {
			b, err := os.ReadFile(proc + "/smaps_rollup")
			if err != nil {
				rollup = false
			} else {
				s.Rollup = parseKV(b, rollupKeys)
			}
		}
		s.Cg = readCgroup(*cg)
		if *metrics != "" {
			s.Go = scrape(ctx, client, "http://"+*metrics+"/metrics")
		}
		line, err := json.Marshal(s)
		if err != nil {
			return err
		}
		w.Write(line)
		w.WriteByte('\n')
		if err := w.Flush(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// parseKV reads "Key: value [kB]" lines, keeping keys.
func parseKV(b []byte, keys []string) map[string]int64 {
	m := make(map[string]int64, len(keys))
	for line := range strings.Lines(string(b)) {
		k, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		for _, want := range keys {
			if k != want {
				continue
			}
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			if v, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
				m[k] = v
			}
		}
	}
	return m
}

func readCgroup(dir string) map[string]int64 {
	m := map[string]int64{}
	for _, name := range []string{"current", "peak"} {
		b, err := os.ReadFile(filepath.Join(dir, "memory."+name))
		if err != nil {
			continue
		}
		if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			m[name] = v
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return m
	}
	for line := range strings.Lines(string(b)) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		for _, want := range cgStatKeys {
			if k == want {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					m[k] = n
				}
			}
		}
	}
	return m
}

func scrape(ctx context.Context, client *http.Client, url string) map[string]float64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	return selectMetrics(b)
}

// selectMetrics parses Prometheus text exposition and keeps the series
// metricPrefixes and summedMetrics name, the latter summed over their labels.
func selectMetrics(b []byte) map[string]float64 {
	m := map[string]float64{}
	for line := range bytes.Lines(b) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		series, value, ok := splitSeries(string(line))
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(series, "{")
		for _, s := range summedMetrics {
			if name == s {
				m[name] += value
			}
		}
		for _, p := range metricPrefixes {
			if strings.HasPrefix(name, p) {
				m[series] = value
			}
		}
	}
	return m
}

// splitSeries splits "name{labels} value [timestamp]" into the series and
// its value. A label value may hold spaces, so the series ends at the
// closing brace when there is one.
func splitSeries(line string) (string, float64, bool) {
	var series, rest string
	if i := strings.LastIndexByte(line, '}'); i >= 0 {
		series, rest = line[:i+1], line[i+1:]
	} else {
		var ok bool
		series, rest, ok = strings.Cut(line, " ")
		if !ok {
			return "", 0, false
		}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", 0, false
	}
	return series, v, true
}
