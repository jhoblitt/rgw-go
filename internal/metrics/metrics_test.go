package metrics_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jhoblitt/rgw-go/internal/metrics"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/version"
)

// reporter is a radosclient.StatsReporter returning the Stats a spec last
// set; a scrape reads it on the test server's goroutine.
type reporter struct {
	mu sync.Mutex
	s  radosclient.Stats
}

func (r *reporter) set(s radosclient.Stats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s = s
}

func (r *reporter) Stats() radosclient.Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.s
}

// scrape GETs url as a scraper that does not ask for protobuf does.
func scrape(ctx context.Context, url string) (*http.Response, string) {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	Expect(err).NotTo(HaveOccurred())
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return resp, string(body)
}

const requestsFamily = `# HELP rgw_go_requests_total Requests served, by radosgw op name and the hundreds class of the HTTP status.
# TYPE rgw_go_requests_total counter
`

// radosFamilies is every rgw_go_rados_ family for Stats{ReadOps: 12,
// WriteOps: 5, ReadBytes: 3 MiB, WriteBytes: 8 KiB, InflightOps: 3,
// InflightBytes: 1 MiB, ThrottleWaits: 2} under mode "callback".
const radosFamilies = `# HELP rgw_go_rados_ops_total RADOS read and write operations submitted to librados, by kind; object listings, watches, notifies and advisory-lock calls are not counted.
# TYPE rgw_go_rados_ops_total counter
rgw_go_rados_ops_total{kind="read",mode="callback"} 12
rgw_go_rados_ops_total{kind="write",mode="callback"} 5
# HELP rgw_go_rados_bytes_total Payload bytes submitted to librados with the operations rgw_go_rados_ops_total counts, not bytes returned: the requested lengths and class-method inputs of reads, the object data and class-method inputs of writes.
# TYPE rgw_go_rados_bytes_total counter
rgw_go_rados_bytes_total{kind="read",mode="callback"} 3145728
rgw_go_rados_bytes_total{kind="write",mode="callback"} 8192
# HELP rgw_go_rados_ops_in_flight RADOS read and write operations the in-flight limiter holds: submitted and not yet completed, abandoned ones included.
# TYPE rgw_go_rados_ops_in_flight gauge
rgw_go_rados_ops_in_flight{mode="callback"} 3
# HELP rgw_go_rados_bytes_in_flight Payload bytes the in-flight limiter holds for read and write operations not yet completed; a read holds its requested length however small the object turns out to be, as librados's objecter throttle budgets it.
# TYPE rgw_go_rados_bytes_in_flight gauge
rgw_go_rados_bytes_in_flight{mode="callback"} 1048576
# HELP rgw_go_rados_throttle_waits_total RADOS read and write operations that parked on the in-flight limiter before submission.
# TYPE rgw_go_rados_throttle_waits_total counter
rgw_go_rados_throttle_waits_total{mode="callback"} 2
`

var radosNames = []string{
	"rgw_go_rados_ops_total",
	"rgw_go_rados_bytes_total",
	"rgw_go_rados_ops_in_flight",
	"rgw_go_rados_bytes_in_flight",
	"rgw_go_rados_throttle_waits_total",
}

const threadsFamily = `# HELP rgw_go_process_threads OS threads in the whole process, librados's own included, from the Threads field of /proc/self/status; go_threads counts only the Go runtime's thread records.
# TYPE rgw_go_process_threads gauge
`

// sample returns the value of the unlabeled sample name in a text scrape.
func sample(body, name string) float64 {
	GinkgoHelper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + ` (\S+)$`).FindStringSubmatch(body)
	Expect(m).To(HaveLen(2), "no %s sample in the scrape", name)
	v, err := strconv.ParseFloat(m[1], 64)
	Expect(err).NotTo(HaveOccurred())
	return v
}

// ownThreads reads the Threads field of /proc/self/status without the
// package's help.
func ownThreads() float64 {
	GinkgoHelper()
	status, err := os.ReadFile("/proc/self/status")
	Expect(err).NotTo(HaveOccurred())
	m := regexp.MustCompile(`(?m)^Threads:\s+(\d+)$`).FindSubmatch(status)
	Expect(m).To(HaveLen(2), "no Threads field in /proc/self/status")
	n, err := strconv.ParseFloat(string(m[1]), 64)
	Expect(err).NotTo(HaveOccurred())
	return n
}

var _ = Describe("Registry", func() {
	var (
		reg *metrics.Registry
		srv *httptest.Server
	)

	BeforeEach(func() {
		reg = metrics.New()
		srv = httptest.NewServer(reg.Handler())
		DeferCleanup(srv.Close)
	})

	It("serves per-op counts, requests in flight, body bytes and the transport's counters as text", func(ctx SpecContext) {
		var m op.Metrics = reg
		m.Observe("list_buckets", http.StatusOK, 3*time.Millisecond, 0, 512)
		m.Observe("list_buckets", http.StatusOK, 5*time.Millisecond, 0, 1024)
		m.Observe("get_obj", http.StatusNotFound, time.Millisecond, 0, 243)
		m.Observe("get_obj", http.StatusNotModified, 2*time.Millisecond, 0, 0)
		m.Observe("put_obj", http.StatusOK, 40*time.Millisecond, 4096, 0)
		m.InFlight(1)
		m.InFlight(1)
		m.InFlight(-1)
		fake := &reporter{}
		fake.set(radosclient.Stats{
			ReadOps: 7, WriteOps: 3, ReadBytes: 4 << 20, WriteBytes: 4096,
			InflightOps: 2, InflightBytes: 8192, ThrottleWaits: 5,
		})
		reg.WatchRADOS("callback", fake)

		resp, body := scrape(ctx, srv.URL)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Header.Get("Content-Type")).To(HavePrefix("text/plain"))
		for _, line := range []string{
			`rgw_go_requests_total{op="list_buckets",status="2xx"} 2`,
			`rgw_go_requests_total{op="get_obj",status="4xx"} 1`,
			`rgw_go_requests_total{op="get_obj",status="3xx"} 1`,
			`rgw_go_requests_total{op="put_obj",status="2xx"} 1`,
			`rgw_go_requests_in_flight 1`,
			`rgw_go_request_bytes_total{direction="in"} 4096`,
			`rgw_go_request_bytes_total{direction="out"} 1779`,
			`rgw_go_rados_ops_total{kind="read",mode="callback"} 7`,
			`rgw_go_rados_throttle_waits_total{mode="callback"} 5`,
		} {
			Expect(body).To(ContainSubstring("\n" + line + "\n"))
		}
		Expect(body).To(ContainSubstring("\nrgw_go_build_info{"))
	})

	DescribeTable("counts a status under its hundreds class",
		func(status int, class string) {
			reg.Observe("get_obj", status, time.Millisecond, 0, 0)
			want := requestsFamily + fmt.Sprintf("rgw_go_requests_total{op=\"get_obj\",status=%q} 1\n", class)
			Expect(testutil.ScrapeAndCompare(srv.URL, strings.NewReader(want), "rgw_go_requests_total")).To(Succeed())
		},
		Entry("a 101 Switching Protocols is 1xx", http.StatusSwitchingProtocols, "1xx"),
		Entry("a 206 Partial Content is 2xx", http.StatusPartialContent, "2xx"),
		Entry("a 304 Not Modified is 3xx", http.StatusNotModified, "3xx"),
		Entry("a 403 AccessDenied is 4xx", http.StatusForbidden, "4xx"),
		Entry("a 503 SlowDown is 5xx", http.StatusServiceUnavailable, "5xx"),
		Entry("a status never written is net/http's implicit 200", 0, "2xx"),
		Entry("a code net/http refuses to send is a server fault", 42, "5xx"),
		Entry("a code past HTTP's 599 is a server fault", 600, "5xx"),
	)

	It("records latency in buckets doubling from 1 ms to the first past a minute", func() {
		// Binary fractions of a second, so the sum prints exactly.
		reg.Observe("get_obj", http.StatusOK, 3906250*time.Nanosecond, 0, 0) // 2^-8 s
		reg.Observe("get_obj", http.StatusOK, 7812500*time.Nanosecond, 0, 0) // 2^-7 s
		reg.Observe("get_obj", http.StatusOK, 64*time.Second, 0, 0)
		want := `# HELP rgw_go_request_duration_seconds Time taken to serve a request, by radosgw op name.
# TYPE rgw_go_request_duration_seconds histogram
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.001"} 0
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.002"} 0
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.004"} 1
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.008"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.016"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.032"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.064"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.128"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.256"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="0.512"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="1.024"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="2.048"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="4.096"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="8.192"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="16.384"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="32.768"} 2
rgw_go_request_duration_seconds_bucket{op="get_obj",le="65.536"} 3
rgw_go_request_duration_seconds_bucket{op="get_obj",le="+Inf"} 3
rgw_go_request_duration_seconds_sum{op="get_obj"} 64.01171875
rgw_go_request_duration_seconds_count{op="get_obj"} 3
`
		Expect(testutil.ScrapeAndCompare(srv.URL, strings.NewReader(want), "rgw_go_request_duration_seconds")).To(Succeed())
	})

	It("never lowers the body byte counters", func() {
		reg.Observe("put_obj", http.StatusOK, time.Millisecond, 100, 10)
		reg.Observe("put_obj", http.StatusInternalServerError, time.Millisecond, -1, -1)
		want := `# HELP rgw_go_request_bytes_total Body bytes read from requests (in) and written to responses (out).
# TYPE rgw_go_request_bytes_total counter
rgw_go_request_bytes_total{direction="in"} 100
rgw_go_request_bytes_total{direction="out"} 10
`
		Expect(testutil.ScrapeAndCompare(srv.URL, strings.NewReader(want), "rgw_go_request_bytes_total")).To(Succeed())
	})

	It("reads the transport's counters on every scrape", func(ctx SpecContext) {
		fake := &reporter{}
		fake.set(radosclient.Stats{ReadOps: 1})
		reg.WatchRADOS("callback", fake)
		_, body := scrape(ctx, srv.URL)
		Expect(body).To(ContainSubstring("\n" + `rgw_go_rados_ops_total{kind="read",mode="callback"} 1` + "\n"))

		fake.set(radosclient.Stats{
			ReadOps: 12, WriteOps: 5, ReadBytes: 3 << 20, WriteBytes: 8 << 10,
			InflightOps: 3, InflightBytes: 1 << 20, ThrottleWaits: 2,
		})
		Expect(testutil.ScrapeAndCompare(srv.URL, strings.NewReader(radosFamilies), radosNames...)).To(Succeed())
	})

	It("reports each completion mode under its own label, the latest reporter for a mode winning", func(ctx SpecContext) {
		first, second, syncMode := &reporter{}, &reporter{}, &reporter{}
		first.set(radosclient.Stats{ReadOps: 1})
		second.set(radosclient.Stats{ReadOps: 2})
		syncMode.set(radosclient.Stats{ReadOps: 3})
		reg.WatchRADOS("callback", first)
		reg.WatchRADOS("callback", second)
		reg.WatchRADOS("sync", syncMode)

		resp, body := scrape(ctx, srv.URL)
		Expect(resp.StatusCode).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring("\n" + `rgw_go_rados_ops_total{kind="read",mode="callback"} 2` + "\n"))
		Expect(body).To(ContainSubstring("\n" + `rgw_go_rados_ops_total{kind="read",mode="sync"} 3` + "\n"))
	})

	It("stops reporting a mode watched with a nil reporter", func(ctx SpecContext) {
		fake := &reporter{}
		reg.WatchRADOS("callback", fake)
		_, body := scrape(ctx, srv.URL)
		Expect(body).To(ContainSubstring("\n" + `rgw_go_rados_ops_in_flight{mode="callback"} 0` + "\n"))

		reg.WatchRADOS("callback", nil)
		resp, body := scrape(ctx, srv.URL)
		Expect(resp.StatusCode).To(Equal(http.StatusOK), body)
		Expect(body).NotTo(ContainSubstring("rgw_go_rados_"))
	})

	It("labels the build info gauge with the binary's version, revision and Go version", func() {
		info := version.Read()
		want := "# HELP rgw_go_build_info A metric with a constant '1' value labeled by the version and revision rgw-go was built from and the Go version that built it.\n" +
			"# TYPE rgw_go_build_info gauge\n" +
			fmt.Sprintf("rgw_go_build_info{go_version=%q,revision=%q,version=%q} 1\n", info.GoVersion, info.Revision, info.Version)
		Expect(testutil.ScrapeAndCompare(srv.URL, strings.NewReader(want), "rgw_go_build_info")).To(Succeed())
	})

	It("serves the Go runtime's thread count and the process's resident memory", func(ctx SpecContext) {
		_, body := scrape(ctx, srv.URL)
		Expect(body).To(MatchRegexp(`(?m)^go_threads [1-9]`))
		Expect(body).To(MatchRegexp(`(?m)^process_resident_memory_bytes [1-9]`))
	})

	It("reports the kernel's thread count for the whole process", func(ctx SpecContext) {
		// The runtime ends a thread only when a goroutine exits locked to it,
		// which nothing here does, so the kernel's count only grows.
		before := ownThreads()
		_, body := scrape(ctx, srv.URL)
		after := ownThreads()
		threads := sample(body, "rgw_go_process_threads")
		Expect(threads).To(BeNumerically(">=", before))
		Expect(threads).To(BeNumerically("<=", after))
	})
})

var _ = Describe("the process thread gauge", func() {
	DescribeTable("reads the Threads field of the proc status file",
		func(proc fstest.MapFS, want string) {
			Expect(testutil.CollectAndCompare(metrics.ThreadsCollector(proc), strings.NewReader(want), "rgw_go_process_threads")).To(Succeed())
		},
		Entry("the Threads field among the others", fstest.MapFS{
			"self/status": {Data: []byte("Name:\trgw-go\nState:\tS (sleeping)\nThreads:\t49\nSigQ:\t0/63448\n")},
		}, threadsFamily+"rgw_go_process_threads 49\n"),
		Entry("no status file drops the gauge rather than failing the scrape", fstest.MapFS{}, ""),
		Entry("a status without a Threads field drops the gauge", fstest.MapFS{
			"self/status": {Data: []byte("Name:\trgw-go\nState:\tS (sleeping)\n")},
		}, ""),
		Entry("a Threads field that is not a count drops the gauge", fstest.MapFS{
			"self/status": {Data: []byte("Threads:\tmany\n")},
		}, ""),
	)
})
