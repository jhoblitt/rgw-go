package main

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	benchKey    = "BENCH0000000000000AK"
	benchSecret = "bench-secret-0123456789abcdefghijklmnopqrstuv"
)

// request is one request the fake S3 served.
type request struct {
	method, path string
	query        url.Values
	header       http.Header
	body         []byte
}

// fakeS3 answers the requests s3send sends the way radosgw does, keeping what
// they stored.
type fakeS3 struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []request
	// objects are the completed objects by path, and parts the uploads' parts
	// by upload id and part number.
	objects map[string][]byte
	parts   map[string]map[int][]byte
	uploads int
	// versioning is what the bucket's versioning status is, and reports what
	// GetBucketVersioning says in its place when set.
	versioning, reports string
	// fail answers a request with the status it returns when that is not 0, and
	// delay holds every request before it is served. hang holds every request,
	// and hold those it is true of, until their client gives them up, as a
	// gateway that answers nothing does.
	fail  func(request) int
	delay time.Duration
	hang  bool
	hold  func(request) bool
	// serve, when set, answers in the fake's place each request it returns
	// true for, having read nothing of those it returns false for.
	serve func(w http.ResponseWriter, r *http.Request) bool
	// release is closed as the spec ends, before the server closes, which
	// waits for every handler: a handler that holds a request whose body it
	// has not read waits for it, since the server notices a client that went
	// away only once a handler has read its request.
	release chan struct{}
	// conns counts the connections the server accepted, hung the requests it
	// held for good, and inflight and peak the requests being served now and
	// the most there were at once.
	conns, hung, inflight, peak atomic.Int64
	// readBuffer, when above 0, caps the receive buffer of each connection the
	// server accepts, and capped counts the connections it capped.
	readBuffer, capped atomic.Int64
}

func newFakeS3() *fakeS3 {
	f := &fakeS3{objects: map[string][]byte{}, parts: map[string]map[int][]byte{}, release: make(chan struct{})}
	f.srv = httptest.NewUnstartedServer(f)
	f.srv.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		f.conns.Add(1)
		if n := f.readBuffer.Load(); n > 0 {
			if tcp, ok := c.(*net.TCPConn); ok && tcp.SetReadBuffer(int(n)) == nil {
				f.capped.Add(1)
			}
		}
	}
	f.srv.Start()
	DeferCleanup(f.srv.Close)
	DeferCleanup(func() { close(f.release) })
	return f
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for p := f.peak.Load(); n > p && !f.peak.CompareAndSwap(p, n); p = f.peak.Load() {
	}
	if f.serve != nil && f.serve(w, r) {
		return
	}
	// The body is read first: the server notices a client that went away only
	// once a handler has read its request.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	req := request{method: r.Method, path: r.URL.Path, query: q, header: r.Header.Clone(), body: body}
	if f.hang || (f.hold != nil && f.hold(req)) {
		f.hung.Add(1)
		<-r.Context().Done()
		return
	}
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.fail != nil {
		if status := f.fail(req); status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, `<Error><Code>Refused</Code><Message>refused</Message></Error>`)
			return
		}
	}
	switch {
	case q.Has("versioning") && r.Method == http.MethodPut:
		var cfg struct{ Status string }
		if err := xml.Unmarshal(body, &cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.versioning = cfg.Status
	case q.Has("versioning"):
		status := f.versioning
		if f.reports != "" {
			status = f.reports
		}
		fmt.Fprintf(w, `<VersioningConfiguration><Status>%s</Status></VersioningConfiguration>`, status)
	case q.Has("uploads") && r.Method == http.MethodPost:
		f.uploads++
		id := "u" + strconv.Itoa(f.uploads)
		f.parts[id] = map[int][]byte{}
		fmt.Fprintf(w, `<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, id)
	case q.Has("partNumber"):
		n, err := strconv.Atoi(q.Get("partNumber"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.parts[q.Get("uploadId")][n] = body
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, n))
	case q.Has("uploadId") && r.Method == http.MethodPost:
		var done struct{ Part []struct{ PartNumber int } }
		if err := xml.Unmarshal(body, &done); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var whole []byte
		for _, p := range done.Part {
			whole = append(whole, f.parts[q.Get("uploadId")][p.PartNumber]...)
		}
		f.objects[r.URL.Path] = whole
		fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>"done"</ETag></CompleteMultipartUploadResult>`)
	case q.Has("uploadId"):
		delete(f.parts, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		f.objects[r.URL.Path] = body
		w.Header().Set("ETag", `"etag"`)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.String(), http.StatusNotImplemented)
	}
}

// seen is the requests served so far.
func (f *fakeS3) seen() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// stored is the paths of the objects stored so far, sorted.
func (f *fakeS3) stored() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	paths := make([]string, 0, len(f.objects))
	for p := range f.objects {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths
}

// puts is the requests that stored a whole object.
func (f *fakeS3) puts() []request {
	var puts []request
	for _, r := range f.seen() {
		if r.method == http.MethodPut && !r.query.Has("partNumber") && !r.query.Has("versioning") {
			puts = append(puts, r)
		}
	}
	return puts
}

var _ = Describe("s3send", func() {
	var (
		stdout, stderr *bytes.Buffer
		out            string
	)

	BeforeEach(func() {
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
		out = filepath.Join(GinkgoT().TempDir(), "result.json")
		GinkgoT().Setenv("AWS_SECRET_ACCESS_KEY", benchSecret)
	})

	// args are the flags every run takes, then the spec's.
	args := func(f *fakeS3, more ...string) []string {
		return append([]string{
			"-endpoint", f.srv.URL, "-region", "ceph-objectstore", "-key", benchKey, "-bucket", "bench-small",
		}, more...)
	}

	load := func(ctx context.Context, a ...string) int {
		return run(ctx, a, stdout, stderr)
	}

	result := func() Result {
		GinkgoHelper()
		data, err := os.ReadFile(out)
		Expect(err).NotTo(HaveOccurred())
		var r Result
		Expect(json.Unmarshal(data, &r)).To(Succeed())
		return r
	}

	Describe("-op put", func() {
		It("writes -objects objects for each of -threads ranks, named as elbencho names the objects of its ranks", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-op", "put", "-threads", "3", "-objects", "2", "-size", "100", "-out", out)...)).To(Equal(0), stderr.String())
			Expect(f.stored()).To(Equal([]string{
				"/bench-small/r0-f0", "/bench-small/r0-f1", "/bench-small/r1-f0", "/bench-small/r1-f1", "/bench-small/r2-f0", "/bench-small/r2-f1",
			}))
			for path, body := range f.objects {
				Expect(body).To(HaveLen(100), path)
			}
			r := result()
			Expect([]any{r.Op, r.Threads, r.Objects, r.Errors, r.InFlight, r.Bytes}).
				To(Equal([]any{"put", 3, int64(6), int64(0), int64(0), int64(600)}))
			Expect([]any{r.Size, r.Block, r.StorageClass, r.Checksum}).To(Equal([]any{int64(100), int64(0), "", ""}))
			Expect(r.OpsPerS).To(BeNumerically("~", float64(r.Objects)/r.DurationS, 0.01))
			Expect(r.MiBPerS).To(BeNumerically("~", float64(r.Bytes)/(1<<20)/r.DurationS, 0.01))
			Expect(r.LatUs.P50).To(BeNumerically(">", 0))
			Expect(r.LatUs.P50).To(BeNumerically("<=", r.LatUs.P99))
			Expect(r.LatUs.P99).To(BeNumerically("<=", r.LatUs.P999))
			Expect(r.Start).To(BeTemporally("~", time.Now(), 5*time.Second))
		})

		It("keeps the secret out of the command line it records and signs as the key in the region", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", "10", "-out", out)...)).To(Equal(0), stderr.String())
			Expect(result().Command).NotTo(ContainSubstring("bench-secret"))
			Expect(f.puts()).To(HaveLen(1))
			auth := f.puts()[0].header.Get("Authorization")
			Expect(auth).To(HavePrefix("AWS4-HMAC-SHA256 Credential=" + benchKey + "/"))
			Expect(auth).To(ContainSubstring("/ceph-objectstore/s3/aws4_request"))
		})

		It("writes every object when -duration is unset, and stops at -duration with objects left", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-threads", "2", "-objects", "5", "-size", "10", "-out", out)...)).To(Equal(0), stderr.String())
			Expect(result().Objects).To(Equal(int64(10)))

			g := newFakeS3()
			Expect(load(ctx, args(g, "-threads", "2", "-objects", "100000000", "-size", "10", "-duration", "500ms", "-out", out)...)).
				To(Equal(0), stderr.String())
			r := result()
			Expect(r.Objects).To(BeNumerically(">", 0))
			// Every object it counts reached the gateway, and the requests it cut
			// are the only others that may have.
			Expect(len(g.puts())).To(BeNumerically(">=", int(r.Objects)))
			Expect(len(g.puts())).To(BeNumerically("<=", int(r.Objects+r.InFlight)))
			// The window ends no sooner than the time given; how much later is the
			// host's.
			Expect(r.DurationS).To(BeNumerically(">=", 0.5))
			Expect(r.DurationS).To(BeNumerically("<", 10))
			Expect(r.InFlight).To(BeNumerically("<=", 2), "a request a thread at most")
		})

		It("reuses one connection for each thread's every object", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-threads", "8", "-objects", "50", "-size", "10", "-out", out)...)).To(Equal(0), stderr.String())
			Expect(f.puts()).To(HaveLen(400))
			// The transport hands a connection back as a response is read, a moment
			// after the next request may ask for one, and a loaded host stretches
			// that moment, so the connections are not counted to the thread: what
			// separates a pool that holds a connection for each thread from the
			// transport's default of two idle a host, which has 8 threads dial for
			// most of their requests, is how many requests a connection carries.
			Expect(float64(len(f.puts()))/float64(f.conns.Load())).To(BeNumerically(">=", 4),
				"requests for each of the %d connections of 8 threads", f.conns.Load())
		})

		It("sends no storage class and no checksum unless it is asked to", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "2", "-size", "64", "-out", out)...)).To(Equal(0), stderr.String())
			Expect(f.puts()).To(HaveLen(2))
			for _, r := range f.puts() {
				for name := range r.header {
					Expect(name).NotTo(HavePrefix("X-Amz-Checksum"), "a checksum header")
					Expect(name).NotTo(Equal("X-Amz-Sdk-Checksum-Algorithm"))
					Expect(name).NotTo(Equal("X-Amz-Storage-Class"))
				}
			}
		})

		It("puts a whole object into the storage class named", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-threads", "2", "-objects", "1", "-size", "64", "-storage-class", "COMP_ZLIB", "-out", out)...)).
				To(Equal(0), stderr.String())
			Expect(f.puts()).To(HaveLen(2))
			for _, r := range f.puts() {
				Expect(r.header.Get("X-Amz-Storage-Class")).To(Equal("COMP_ZLIB"), r.path)
			}
			Expect(result().StorageClass).To(Equal("COMP_ZLIB"), "the result names the class a report names its row by")
		})

		It("puts an object larger than -block as a multipart upload of -block parts in the storage class", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "2", "-size", "20", "-block", "8", "-storage-class", "COMP_ZLIB", "-out", out)...)).
				To(Equal(0), stderr.String())
			var steps []string
			var partSizes []int
			for _, r := range f.seen() {
				switch {
				case r.query.Has("uploads"):
					steps = append(steps, "create "+r.header.Get("X-Amz-Storage-Class"))
				case r.query.Has("partNumber"):
					steps = append(steps, "part "+r.header.Get("X-Amz-Storage-Class"))
					partSizes = append(partSizes, len(r.body))
				case r.method == http.MethodPost:
					steps = append(steps, "complete")
				}
			}
			upload := []string{"create COMP_ZLIB", "part ", "part ", "part ", "complete"}
			Expect(steps).To(Equal(slices.Concat(upload, upload)), "the class goes on the create, which is where S3 takes it")
			Expect(partSizes).To(Equal([]int{8, 8, 4, 8, 8, 4}))
			Expect(f.stored()).To(Equal([]string{"/bench-small/r0-f0", "/bench-small/r0-f1"}))
			for path, body := range f.objects {
				Expect(body).To(HaveLen(20), path)
			}
			r := result()
			Expect([]any{r.Bytes, r.Size, r.Block}).To(Equal([]any{int64(40), int64(20), int64(8)}))
		})

		It("sends a CRC32 as botocore sends it by default over plain HTTP, in headers and a body that is the object", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "2", "-size", "4096", "-checksum", "crc32", "-out", out)...)).
				To(Equal(0), stderr.String())
			Expect(f.puts()).To(HaveLen(2))
			for _, r := range f.puts() {
				var sum [4]byte
				binary.BigEndian.PutUint32(sum[:], crc32.ChecksumIEEE(r.body))
				Expect(r.body).To(HaveLen(4096), "a body framed as aws-chunked would be longer")
				Expect(r.header.Get("X-Amz-Sdk-Checksum-Algorithm")).To(Equal("CRC32"), "radosgw finds the checksum through this header")
				Expect(r.header.Get("X-Amz-Checksum-Crc32")).To(Equal(base64.StdEncoding.EncodeToString(sum[:])))
				Expect(r.header.Get("X-Amz-Trailer")).To(BeEmpty())
				Expect(r.header.Get("Content-Encoding")).To(BeEmpty())
				Expect(r.header.Get("X-Amz-Content-Sha256")).To(HaveLen(64), "a signed payload, as over plain HTTP S3 requires")
			}
			Expect(result().Checksum).To(Equal("crc32"))
		})

		It("writes bytes deflate shrinks to about half with -compressible, and bytes it does not shrink otherwise", func(ctx SpecContext) {
			deflated := func(body []byte) float64 {
				var packed bytes.Buffer
				zw, err := flate.NewWriter(&packed, flate.DefaultCompression)
				Expect(err).NotTo(HaveOccurred())
				_, err = zw.Write(body)
				Expect(err).NotTo(HaveOccurred())
				Expect(zw.Close()).To(Succeed())
				return float64(packed.Len()) / float64(len(body))
			}
			f := newFakeS3()
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", "1048576", "-compressible", "-out", out)...)).
				To(Equal(0), stderr.String())
			Expect(deflated(f.puts()[0].body)).To(BeNumerically("<", 0.6))
			Expect(deflated(f.puts()[0].body)).To(BeNumerically(">", 0.4))

			g := newFakeS3()
			Expect(load(ctx, args(g, "-threads", "1", "-objects", "1", "-size", "1048576", "-out", out)...)).To(Equal(0), stderr.String())
			Expect(deflated(g.puts()[0].body)).To(BeNumerically(">", 0.99))
		})

		It("counts a PUT the gateway fails as an error without retrying it, and exits 1 with the result written", func(ctx SpecContext) {
			f := newFakeS3()
			f.fail = func(r request) int {
				if r.method == http.MethodPut && r.path == "/bench-small/r1-f1" {
					return http.StatusServiceUnavailable
				}
				return 0
			}
			Expect(load(ctx, args(f, "-threads", "2", "-objects", "3", "-size", "10", "-out", out)...)).To(Equal(1))
			r := result()
			Expect([]any{r.Objects, r.Errors}).To(Equal([]any{int64(5), int64(1)}))
			Expect(f.puts()).To(HaveLen(6), "one request for each object: the SDK retries a 503, and s3send lets it not")
			Expect(stderr.String()).To(ContainSubstring("503 Refused"))
		})

		It("counts a refused connection as an error", func(ctx SpecContext) {
			f := newFakeS3()
			a := args(f, "-threads", "1", "-objects", "2", "-size", "10", "-out", out)
			f.srv.Close()
			Expect(load(ctx, a...)).To(Equal(1))
			Expect(result().Errors).To(Equal(int64(2)))
		})

		It("aborts a multipart upload it could not finish, and counts the object as an error", func(ctx SpecContext) {
			f := newFakeS3()
			f.fail = func(r request) int {
				if r.query.Get("partNumber") == "3" {
					return http.StatusInternalServerError
				}
				return 0
			}
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", "20", "-block", "8", "-out", out)...)).To(Equal(1))
			Expect(result().Errors).To(Equal(int64(1)))
			var aborted []string
			for _, r := range f.seen() {
				if r.method == http.MethodDelete {
					aborted = append(aborted, r.query.Get("uploadId"))
				}
			}
			Expect(aborted).To(ConsistOf("u1"), "the uploads aborted; u1 is the one begun")
			Expect(f.stored()).To(BeEmpty())
		})

		It("counts a request the gateway holds past -request-timeout as an error, and exits 1 though the other threads stored theirs", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			f.hold = func(r request) bool { return strings.Contains(r.path, "/r0-") }
			Expect(load(ctx, args(f, "-threads", "3", "-objects", "4", "-size", "10", "-request-timeout", "200ms", "-duration", "5s", "-out", out)...)).
				To(Equal(1))
			r := result()
			Expect([]any{r.Objects, r.Errors, r.InFlight}).To(Equal([]any{int64(8), int64(4), int64(0)}), "rank 0's four, none left in flight")
			Expect(f.hung.Load()).To(Equal(int64(4)), "each of rank 0's requests reached the gateway")
			Expect(stderr.String()).To(ContainSubstring("4 of 12 objects failed: no byte sent or received within -request-timeout ×4"))
		})

		DescribeTable("bounds each request of a multipart upload by -request-timeout, and aborts the upload it gave up on",
			func(ctx SpecContext, hold func(request) bool, aborts []string) {
				f := newFakeS3()
				f.hold = hold
				Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", "20", "-block", "8", "-request-timeout", "200ms", "-duration", "5s", "-out", out)...)).
					To(Equal(1))
				Expect([]any{result().Objects, result().Errors}).To(Equal([]any{int64(0), int64(1)}))
				Expect(f.hung.Load()).To(Equal(int64(1)), "the request it held was the one that ran out of its time")
				var aborted []string
				for _, r := range f.seen() {
					if r.method == http.MethodDelete {
						aborted = append(aborted, r.query.Get("uploadId"))
					}
				}
				Expect(aborted).To(ConsistOf(aborts))
			},
			Entry("the CreateMultipartUpload, so that there is no upload to abort", SpecTimeout(30*time.Second),
				func(r request) bool { return r.query.Has("uploads") }, []string{}),
			Entry("the second part", SpecTimeout(30*time.Second),
				func(r request) bool { return r.query.Get("partNumber") == "2" }, []string{"u1"}),
			Entry("the CompleteMultipartUpload", SpecTimeout(30*time.Second),
				func(r request) bool { return r.method == http.MethodPost && r.query.Has("uploadId") }, []string{"u1"}),
		)

		It("puts no bound on a request with -request-timeout 0, where a shorter one fails it", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			// The gateway holds each answer 600 ms once it has read the request.
			f.delay = 600 * time.Millisecond
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", "10", "-request-timeout", "200ms", "-out", out)...)).
				To(Equal(1), "600 ms with nothing moving is past a 200 ms bound")
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", "10", "-request-timeout", "0", "-out", out)...)).To(Equal(0), stderr.String())
			Expect(result().Objects).To(Equal(int64(1)))
			Expect(watched(http.DefaultTransport, 0)).To(BeIdenticalTo(http.DefaultTransport), "0 puts no watch on a request at all")
		})

		It("exits 1, counting the requests it cut, when the gateway holds every request until the time is up", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			f.hang = true
			Expect(load(ctx, args(f, "-threads", "3", "-objects", "5", "-size", "10", "-duration", "1s", "-out", out)...)).To(Equal(1))
			Expect(f.hung.Load()).To(Equal(int64(3)), "every thread's request reached the gateway")
			r := result()
			Expect([]any{r.Objects, r.Errors, r.InFlight}).To(Equal([]any{int64(0), int64(0), int64(3)}))
			Expect(stderr.String()).To(ContainSubstring("stored no object in "))
			Expect(stderr.String()).To(ContainSubstring("3 requests were in flight when it ended"))
		})

		It("exits 1 and says the result is partial when it is interrupted after storing objects", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			runCtx, interrupt := context.WithCancel(ctx)
			defer interrupt()
			status := make(chan int, 1)
			go func() {
				defer GinkgoRecover()
				status <- load(runCtx, args(f, "-threads", "1", "-objects", "1000000", "-size", "10", "-out", out)...)
			}()
			Eventually(func() int { return len(f.stored()) }).WithTimeout(10 * time.Second).WithPolling(5 * time.Millisecond).
				Should(BeNumerically(">=", 5))
			interrupt()
			Eventually(status).WithTimeout(10 * time.Second).Should(Receive(Equal(1)))
			r := result()
			Expect(r.Objects).To(BeNumerically(">", 0), "objects were stored before the interrupt")
			// The gateway stored 5 at least, and the interrupt may have cut the
			// request of the last before its answer came.
			Expect(r.Objects + r.InFlight).To(BeNumerically(">=", 5))
			Expect(r.Objects).To(BeNumerically("<", 1000000))
			Expect(stderr.String()).To(ContainSubstring("interrupted after "))
			Expect(stderr.String()).To(ContainSubstring("the result is partial"))
		})

		It("exits 1 when it is interrupted with every request held", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			f.hang = true
			runCtx, interrupt := context.WithCancel(ctx)
			defer interrupt()
			status := make(chan int, 1)
			go func() {
				defer GinkgoRecover()
				status <- load(runCtx, args(f, "-threads", "2", "-objects", "5", "-size", "10", "-out", out)...)
			}()
			Eventually(f.hung.Load).WithTimeout(10 * time.Second).WithPolling(5 * time.Millisecond).Should(Equal(int64(2)))
			interrupt()
			Eventually(status).WithTimeout(10 * time.Second).Should(Receive(Equal(1)))
			Expect(result().InFlight).To(Equal(int64(2)))
			Expect(stderr.String()).To(ContainSubstring("interrupted after "))
		})
	})

	Describe("-op versioning", func() {
		It("enables versioning on the bucket and writes no result", func(ctx SpecContext) {
			f := newFakeS3()
			Expect(load(ctx, args(f, "-op", "versioning")...)).To(Equal(0), stderr.String())
			Expect(f.versioning).To(Equal("Enabled"))
			Expect(f.stored()).To(BeEmpty())
		})

		It("fails when the bucket does not report versioning enabled afterwards", func(ctx SpecContext) {
			f := newFakeS3()
			f.reports = "Suspended"
			Expect(load(ctx, args(f, "-op", "versioning")...)).To(Equal(1))
			Expect(stderr.String()).To(ContainSubstring("Suspended"))
		})

		It("fails when the bucket refuses the configuration", func(ctx SpecContext) {
			f := newFakeS3()
			f.fail = func(request) int { return http.StatusForbidden }
			Expect(load(ctx, args(f, "-op", "versioning")...)).To(Equal(1))
			Expect(stderr.String()).To(ContainSubstring("403"))
		})
	})

	Describe("-request-timeout, which fails a request that moves no byte for that long, as elbencho's --s3reqtimeout does", func() {
		// bound is short enough to wait out and long against the gaps between
		// the bytes of the slow transfers below, 25 ms at most.
		const bound = 300 * time.Millisecond
		const stalled = "no byte sent or received within -request-timeout"

		It("lets an upload the gateway reads slowly but steadily run past it", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			// The gateway reads the first 8 MiB of 32 in 256 KiB steps 25 ms apart,
			// 800 ms, then the rest at once, so that the client does not wait out
			// its socket buffers' draining with nothing left to send, which moves
			// nothing. Its receive buffer is capped at a step, so that only the
			// client's send buffer is left to hold what was sent and not read.
			const size, paced, step, gap = 32 << 20, 8 << 20, 256 << 10, 25 * time.Millisecond
			f := newFakeS3()
			f.readBuffer.Store(step)
			f.serve = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodPut {
					return false
				}
				buf := make([]byte, step)
				for range paced / step {
					if _, err := io.ReadFull(r.Body, buf); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return true
					}
					time.Sleep(gap)
				}
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return true
				}
				w.Header().Set("ETag", `"etag"`)
				return true
			}
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", strconv.Itoa(size), "-request-timeout", bound.String(), "-out", out)...)).
				To(Equal(0), stderr.String())
			r := result()
			Expect(r.Objects).To(Equal(int64(1)))
			Expect(time.Duration(r.LatUs.P50*float64(time.Microsecond))).To(BeNumerically(">=", paced/step*gap),
				"the gateway took 800 ms to read the object, past the bound")
			Expect(f.capped.Load()).To(BeNumerically(">", 0), "the gateway's receive buffer was capped")
		})

		It("fails an upload the gateway stops reading, naming the bound", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			f.serve = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodPut {
					return false
				}
				// 1 MiB of the 32, then nothing.
				if _, err := io.CopyN(io.Discard, r.Body, 1<<20); err == nil {
					<-f.release
				}
				return true
			}
			start := time.Now()
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", strconv.Itoa(32<<20), "-request-timeout", bound.String(), "-out", out)...)).
				To(Equal(1))
			Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
			r := result()
			Expect([]any{r.Objects, r.Errors}).To(Equal([]any{int64(0), int64(1)}))
			Expect(stderr.String()).To(ContainSubstring("1 of 1 objects failed: " + stalled + " ×1"))
		})

		It("lets an answer the gateway sends slowly but steadily run past it, -op versioning's", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			const answer, gap = `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`, 15 * time.Millisecond
			f := newFakeS3()
			f.serve = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodGet || !r.URL.Query().Has("versioning") {
					return false
				}
				rc := http.NewResponseController(w)
				w.WriteHeader(http.StatusOK)
				for i := range len(answer) {
					if _, err := io.WriteString(w, answer[i:i+1]); err != nil {
						return true
					}
					if err := rc.Flush(); err != nil {
						return true
					}
					time.Sleep(gap)
				}
				return true
			}
			start := time.Now()
			Expect(load(ctx, args(f, "-op", "versioning", "-request-timeout", bound.String())...)).To(Equal(0), stderr.String())
			Expect(time.Since(start)).To(BeNumerically(">=", time.Duration(len(answer))*gap), "a byte every 15 ms, past the bound")
		})

		It("fails an answer the gateway stops sending, naming the bound, -op versioning's", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			f.serve = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodGet || !r.URL.Query().Has("versioning") {
					return false
				}
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `<VersioningConfiguration><Status>`)
				if err := http.NewResponseController(w).Flush(); err == nil {
					<-r.Context().Done()
				}
				return true
			}
			start := time.Now()
			Expect(load(ctx, args(f, "-op", "versioning", "-request-timeout", bound.String())...)).To(Equal(1))
			Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
			Expect(stderr.String()).To(SatisfyAll(ContainSubstring("reading the versioning of bench-small"), ContainSubstring(stalled+" (300ms)")))
		})

		It("counts an answer's headers arriving as a byte moved", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			// The headers 0.6 of the bound after the request and the body 0.6 after
			// them: nothing moves for longer than the bound only if the headers count
			// for nothing.
			const step = bound * 6 / 10
			f := newFakeS3()
			f.serve = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodGet || !r.URL.Query().Has("versioning") {
					return false
				}
				time.Sleep(step)
				w.WriteHeader(http.StatusOK)
				if err := http.NewResponseController(w).Flush(); err != nil {
					return true
				}
				time.Sleep(step)
				fmt.Fprint(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
				return true
			}
			Expect(load(ctx, args(f, "-op", "versioning", "-request-timeout", bound.String())...)).To(Equal(0), stderr.String())
		})

		It("fails a request whose answer the gateway holds, naming the bound, -op versioning's", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			f.hold = func(r request) bool { return r.method == http.MethodPut && r.query.Has("versioning") }
			start := time.Now()
			Expect(load(ctx, args(f, "-op", "versioning", "-request-timeout", bound.String())...)).To(Equal(1))
			Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
			Expect(f.hung.Load()).To(Equal(int64(1)))
			Expect(stderr.String()).To(SatisfyAll(ContainSubstring("enabling versioning on bench-small"), ContainSubstring(stalled+" (300ms)")))
		})

		It("names an upload's failure, then its abort's", SpecTimeout(30*time.Second), func(ctx SpecContext) {
			f := newFakeS3()
			f.fail = func(r request) int {
				if r.query.Get("partNumber") == "2" {
					return http.StatusInternalServerError
				}
				return 0
			}
			f.hold = func(r request) bool { return r.method == http.MethodDelete }
			Expect(load(ctx, args(f, "-threads", "1", "-objects", "1", "-size", "20", "-block", "8", "-request-timeout", bound.String(), "-out", out)...)).
				To(Equal(1))
			Expect(stderr.String()).To(ContainSubstring("1 of 1 objects failed: 500 Refused, and its abort: " + stalled + " ×1"))
		})

		It("names an abort that ran out of its own bound for that bound", func() {
			Expect(kind(&abortError{err: errors.New("500 InternalError"), abort: context.DeadlineExceeded})).
				To(Equal("500 InternalError, and its abort: no answer within the abort's own 30s"))
		})
	})

	It("runs its threads side by side", func(ctx SpecContext) {
		f := newFakeS3()
		f.delay = 200 * time.Millisecond
		Expect(load(ctx, args(f, "-threads", "4", "-objects", "3", "-size", "10", "-out", out)...)).To(Equal(0), stderr.String())
		Expect(result().Objects).To(Equal(int64(12)))
		Expect(f.peak.Load()).To(BeNumerically(">", 1), "requests held 200 ms each, one thread after another, would never overlap")
	})

	DescribeTable("refuses a bad command line with status 2",
		func(ctx SpecContext, env string, a []string, want string) {
			GinkgoT().Setenv("AWS_SECRET_ACCESS_KEY", env)
			Expect(load(ctx, a...)).To(Equal(2))
			Expect(stderr.String()).To(ContainSubstring(want))
		},
		Entry("no endpoint", benchSecret, []string{"-key", "k", "-bucket", "b"}, "-endpoint is required"),
		Entry("no bucket", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-key", "k"}, "-bucket is required"),
		Entry("no key", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-bucket", "b"}, "-key is required"),
		Entry("no secret in the environment", "", []string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b"},
			"AWS_SECRET_ACCESS_KEY is not set"),
		Entry("an unknown op", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-op", "get"},
			`-op is "get", not put or versioning`),
		Entry("no threads", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-threads", "0"},
			"-threads must be at least 1"),
		Entry("no objects", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-objects", "0"},
			"-objects must be at least 1"),
		Entry("an empty object", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-size", "0"},
			"-size must be from 1 to 1073741824"),
		Entry("an object past a gibibyte", benchSecret,
			[]string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-size", "1073741825"}, "-size must be from 1 to 1073741824"),
		Entry("a negative block", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-block", "-1"},
			"-block must not be negative"),
		Entry("a negative duration", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-duration", "-1s"},
			"-duration must not be negative"),
		Entry("a negative request timeout", benchSecret,
			[]string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-request-timeout", "-1s"}, "-request-timeout must not be negative"),
		Entry("a checksum algorithm it does not send", benchSecret,
			[]string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-checksum", "sha256"}, `-checksum is "sha256", not crc32`),
		Entry("a checksum on a multipart upload, which would need the algorithm on every part", benchSecret,
			[]string{"-endpoint", "http://127.0.0.1:1", "-key", "k", "-bucket", "b", "-size", "20", "-block", "8", "-checksum", "crc32"},
			"-checksum applies to whole-object PUTs only"),
		Entry("an unknown flag", benchSecret, []string{"-endpoint", "http://127.0.0.1:1", "-sign"}, "flag provided but not defined: -sign"),
	)
})
