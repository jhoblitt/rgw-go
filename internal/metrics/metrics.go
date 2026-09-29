package metrics

import (
	"io/fs"
	"maps"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/version"
)

// Registry is rgw-go's Prometheus registry: a minimal surface (per-op counts
// and latency, in-flight requests, RADOS ops and bytes), nothing radosgw's
// perf counters or the ops log cover.
type Registry struct {
	reg      *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	bytesIn  prometheus.Counter
	bytesOut prometheus.Counter
	inFlight prometheus.Gauge
	rados    *radosCollector
}

var _ op.Metrics = (*Registry)(nil)

// New returns a registry with the build-info gauge set from version.Read.
func New() *Registry {
	info := version.Read()
	build := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rgw_go_build_info",
		Help: "A metric with a constant '1' value labeled by the version and revision rgw-go was built from and the Go version that built it.",
		ConstLabels: prometheus.Labels{
			"version":    info.Version,
			"revision":   info.Revision,
			"go_version": info.GoVersion,
		},
	})
	build.Set(1)
	bodyBytes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rgw_go_request_bytes_total",
		Help: "Body bytes read from requests (in) and written to responses (out).",
	}, []string{"direction"})
	r := &Registry{
		reg: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rgw_go_requests_total",
			Help: "Requests served, by radosgw op name and the hundreds class of the HTTP status.",
		}, []string{"op", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "rgw_go_request_duration_seconds",
			Help: "Time taken to serve a request, by radosgw op name.",
			// Seventeen bounds doubling from 1 ms end at 65.536 s, the first
			// past a minute.
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 17),
		}, []string{"op"}),
		bytesIn:  bodyBytes.WithLabelValues("in"),
		bytesOut: bodyBytes.WithLabelValues("out"),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rgw_go_requests_in_flight",
			Help: "Requests being served.",
		}),
		rados: &radosCollector{reporters: make(map[string]radosclient.StatsReporter)},
	}
	r.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		build, r.requests, r.duration, bodyBytes, r.inFlight, r.rados,
		threadsCollector{proc: os.DirFS("/proc")},
	)
	return r
}

// InFlight adds delta to the requests-in-flight gauge; it implements
// op.Metrics.
func (r *Registry) InFlight(delta int) {
	r.inFlight.Add(float64(delta))
}

// Observe counts one finished request under its op and status class, records
// its latency and adds its body bytes; it implements op.Metrics.
func (r *Registry) Observe(opName string, status int, elapsed time.Duration, bytesIn, bytesOut int64) {
	r.requests.WithLabelValues(opName, statusClass(status)).Inc()
	r.duration.WithLabelValues(opName).Observe(elapsed.Seconds())
	// A counter panics on a negative add.
	if bytesIn > 0 {
		r.bytesIn.Add(float64(bytesIn))
	}
	if bytesOut > 0 {
		r.bytesOut.Add(float64(bytesOut))
	}
}

// WatchRADOS reports s's counters under the completion mode label, reading
// them on every scrape. A later call for the same mode replaces s, and a nil
// s stops reporting mode.
func (r *Registry) WatchRADOS(mode string, s radosclient.StatsReporter) {
	r.rados.watch(mode, s)
}

// Handler serves the registry in the Prometheus text format, or in the
// protobuf format to a scraper that asks for it.
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}

var statusClasses = [...]string{"1xx", "2xx", "3xx", "4xx", "5xx"}

// statusClass returns the hundreds class of an HTTP status. Status 0 is a
// handler that wrote nothing, which net/http answers with 200. A code outside
// RFC 9110's 100-599 is a server fault: net/http panics rather than send one
// outside 100-999 (checkWriteHeaderCode), and one above 599 is no HTTP status.
func statusClass(status int) string {
	switch {
	case status == 0:
		return statusClasses[1]
	case status < 100 || status > 599:
		return statusClasses[4]
	}
	return statusClasses[status/100-1]
}

var (
	radosOps = prometheus.NewDesc("rgw_go_rados_ops_total",
		"RADOS read and write operations submitted to librados, by kind; object listings, watches, notifies and advisory-lock calls are not counted.",
		[]string{"mode", "kind"}, nil)
	radosBytes = prometheus.NewDesc("rgw_go_rados_bytes_total",
		"Payload bytes submitted to librados with the operations rgw_go_rados_ops_total counts, not bytes returned: the requested lengths and class-method inputs of reads, the object data and class-method inputs of writes.",
		[]string{"mode", "kind"}, nil)
	radosOpsInFlight = prometheus.NewDesc("rgw_go_rados_ops_in_flight",
		"RADOS read and write operations the in-flight limiter holds: submitted and not yet completed, abandoned ones included.",
		[]string{"mode"}, nil)
	radosBytesInFlight = prometheus.NewDesc("rgw_go_rados_bytes_in_flight",
		"Payload bytes the in-flight limiter holds for read and write operations not yet completed; a read holds its requested length however small the object turns out to be, as librados's objecter throttle budgets it.",
		[]string{"mode"}, nil)
	radosThrottleWaits = prometheus.NewDesc("rgw_go_rados_throttle_waits_total",
		"RADOS read and write operations that parked on the in-flight limiter before submission.",
		[]string{"mode"}, nil)
)

// radosCollector reads each watched transport's counters once per scrape.
type radosCollector struct {
	mu        sync.Mutex
	reporters map[string]radosclient.StatsReporter
}

func (c *radosCollector) watch(mode string, s radosclient.StatsReporter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s == nil {
		delete(c.reporters, mode)
		return
	}
	c.reporters[mode] = s
}

func (c *radosCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{radosOps, radosBytes, radosOpsInFlight, radosBytesInFlight, radosThrottleWaits} {
		ch <- d
	}
}

func (c *radosCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	reporters := maps.Clone(c.reporters)
	c.mu.Unlock()
	for mode, s := range reporters {
		st := s.Stats()
		ch <- prometheus.MustNewConstMetric(radosOps, prometheus.CounterValue, float64(st.ReadOps), mode, "read")
		ch <- prometheus.MustNewConstMetric(radosOps, prometheus.CounterValue, float64(st.WriteOps), mode, "write")
		ch <- prometheus.MustNewConstMetric(radosBytes, prometheus.CounterValue, float64(st.ReadBytes), mode, "read")
		ch <- prometheus.MustNewConstMetric(radosBytes, prometheus.CounterValue, float64(st.WriteBytes), mode, "write")
		ch <- prometheus.MustNewConstMetric(radosOpsInFlight, prometheus.GaugeValue, float64(st.InflightOps), mode)
		ch <- prometheus.MustNewConstMetric(radosBytesInFlight, prometheus.GaugeValue, float64(st.InflightBytes), mode)
		ch <- prometheus.MustNewConstMetric(radosThrottleWaits, prometheus.CounterValue, float64(st.ThrottleWaits), mode)
	}
}

var processThreads = prometheus.NewDesc("rgw_go_process_threads",
	"OS threads in the whole process, librados's own included, from the Threads field of /proc/self/status; go_threads counts only the Go runtime's thread records.",
	nil, nil)

// threadsCollector reports the kernel's thread count for the process on every
// scrape. go_threads counts the Go runtime's M records instead: it misses
// librados's messenger workers, std::threads that never call into Go
// (PosixNetworkStack::spawn_worker), and counts the spare M cgo keeps for
// callbacks from C threads, which has no thread of its own (runtime.mstartm0).
// A status file it cannot read or parse drops the gauge rather than failing
// the scrape.
type threadsCollector struct{ proc fs.FS }

func (c threadsCollector) Describe(ch chan<- *prometheus.Desc) { ch <- processThreads }

func (c threadsCollector) Collect(ch chan<- prometheus.Metric) {
	status, err := fs.ReadFile(c.proc, "self/status")
	if err != nil {
		return
	}
	for line := range strings.Lines(string(status)) {
		v, ok := strings.CutPrefix(line, "Threads:")
		if !ok {
			continue
		}
		if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
			ch <- prometheus.MustNewConstMetric(processThreads, prometheus.GaugeValue, float64(n))
		}
		return
	}
}
