// Command s3send sends the S3 requests the gateway comparison's elbencho
// cannot: a PUT into a storage class, a PUT carrying the CRC32 an AWS SDK
// sends, and the PutBucketVersioning that turns a bucket's versioning on.
// elbencho has no option for a storage class (the pinned fork's source names
// none), runs the AWS SDK with its default checksums off, and at the pinned
// commit sets a bucket's versioning to Suspended where its create phase asks
// for it, so none of the three is within its reach.
//
//	s3send -op put -endpoint http://127.0.0.1:7481 -region ceph-objectstore -key AK -bucket bench-small \
//	  -threads 64 -objects 3000 -size 4096 -storage-class COMP_ZLIB -compressible \
//	  -duration 60s -out comp-small-put-64.json
//	s3send -op versioning -endpoint ... -region ... -key AK -bucket bench-ver
//
// The secret key is read from AWS_SECRET_ACCESS_KEY, never from the command
// line, as elbencho takes it.
//
// put starts -threads goroutines, rank 0 to threads-1, and the goroutine of
// rank r writes -objects objects named r<r>-f<i>, i from 0, one after another,
// until they are all written or -duration has passed. These are the names
// elbencho gives the objects of its ranks with -n 0 (source/workers/
// LocalWorker.cpp, s3ModeIterateObjects), so an elbencho read phase over the
// same -threads and -objects reads what s3send wrote. An object of -size bytes
// goes in one PutObject unless -block is smaller, when it goes as a multipart
// upload of -block-byte parts, the last shorter, aborted when it cannot be
// finished. -storage-class names the class, which S3 takes on the PutObject or
// on the CreateMultipartUpload. -checksum crc32 sends what botocore sends by
// default (httpchecksum.py, resolve_request_checksum_algorithm): the algorithm
// in x-amz-sdk-checksum-algorithm and the value in x-amz-checksum-crc32 over
// plain HTTP, in an aws-chunked trailer instead over TLS. The Go SDK sends the
// value alone when it is left to its default, and Tentacle's radosgw builds its
// PutObject checksum filter, RGWPutObj_Cksum::Factory
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611),
// only from an algorithm header, which cksum_algorithm_hdr looks for
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L65-L82),
// so that form would measure a PUT radosgw does not checksum; Squid's radosgw
// checksums none. The payload is random, or with -compressible bytes of 16
// values, whose four bits of entropy deflate shrinks to about half. The SDK
// retries nothing: a failed request is an error, and so is one that moves no
// byte for -request-timeout, sending none of its body and receiving none of
// its answer, the wait for the answer counting as nothing moving. That is the
// bound elbencho's --s3reqtimeout puts on a request through curl's low-speed
// abort ("The request fails if no data was transferred for this long", the
// pinned fork's source/ProgArgs.cpp), and the gateway command gives both the
// same value: a gateway that stops answering some threads fails the run in
// place of leaving a clean exit and a lower rate, and one that is slow but
// moving is measured. It bounds every request, those of -op versioning
// included. The abort of an upload s3send gave up on also has a bound of its
// own, 30 s, since it runs after the time is up or an interrupt.
//
// The result is one JSON object: op, threads, duration_s (the measured
// window), objects (those stored), errors, in_flight (the objects whose
// request the end of the time or an interrupt cut), bytes stored, size, block,
// storage_class and checksum as given, ops_per_s, mib_per_s, lat_us (p50, p99
// and p999 of the objects stored, in µs, nearest rank), start (when the window
// opened) and command. An object in flight when the time is up is abandoned
// and counted only in in_flight. After writing the result s3send exits 1 when
// there is an error, when it stored no object, which is what a gateway that
// holds every request leaves, and when SIGINT or SIGTERM cut it short, as the
// elbencho profiles fail on theirs.
//
// It takes only the standard library's flags, single-dash as the gateway
// command passes them, and exits 2 on a bad command line.
package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// maxSize is the largest object -size takes, which s3send holds in memory.
const maxSize = 1 << 30

// abortTimeout bounds the abort of a multipart upload s3send gave up on, which
// runs past the end of the time and past an interrupt.
const abortTimeout = 30 * time.Second

// errStalled is the error of a request that moved no byte for
// -request-timeout.
var errStalled = errors.New("no byte sent or received within -request-timeout")

// Result is what s3send writes for a put.
type Result struct {
	Op        string  `json:"op"`
	Threads   int     `json:"threads"`
	DurationS float64 `json:"duration_s"`
	Objects   int64   `json:"objects"`
	Errors    int64   `json:"errors"`
	// InFlight counts the objects whose request was in flight when the time ran
	// out or s3send was interrupted.
	InFlight int64 `json:"in_flight"`
	Bytes    int64 `json:"bytes"`
	// Size, Block, StorageClass and Checksum are the flags that shaped the
	// objects, which a report names a row by.
	Size         int64     `json:"size"`
	Block        int64     `json:"block,omitzero"`
	StorageClass string    `json:"storage_class,omitzero"`
	Checksum     string    `json:"checksum,omitzero"`
	OpsPerS      float64   `json:"ops_per_s"`
	MiBPerS      float64   `json:"mib_per_s"`
	LatUs        Latency   `json:"lat_us"`
	Start        time.Time `json:"start"`
	Command      string    `json:"command"`
}

// Latency holds the latency percentiles of the objects stored.
type Latency struct {
	P50  float64 `json:"p50"`
	P99  float64 `json:"p99"`
	P999 float64 `json:"p999"`
}

// config is a run's flags.
type config struct {
	op, endpoint, region, key, secret, bucket string
	threads, objects                          int
	size, block                               int64
	duration, requestTimeout                  time.Duration
	class, checksum                           string
	compressible                              bool
	out                                       string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run parses args, sends the requests and writes the result, returning the
// exit status: 0, 1 when a request failed, or 2 for a bad command line.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("s3send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := config{secret: os.Getenv("AWS_SECRET_ACCESS_KEY")}
	fs.StringVar(&c.op, "op", "put", "put, or versioning to enable the bucket's versioning")
	fs.StringVar(&c.endpoint, "endpoint", "", "the gateway's URL")
	fs.StringVar(&c.region, "region", "", "the region to sign for (us-east-1 when empty)")
	fs.StringVar(&c.key, "key", "", "the access key; the secret is AWS_SECRET_ACCESS_KEY")
	fs.StringVar(&c.bucket, "bucket", "", "the bucket")
	fs.IntVar(&c.threads, "threads", 1, "goroutines, each writing the objects of its own rank")
	fs.IntVar(&c.objects, "objects", 1, "objects each goroutine writes")
	fs.Int64Var(&c.size, "size", 4096, "an object's size, in bytes")
	fs.Int64Var(&c.block, "block", 0, "a multipart upload's part size, in bytes; 0, or at least -size, puts each object whole")
	fs.DurationVar(&c.duration, "duration", 0, "how long to send requests; 0 sends until every object is written")
	fs.DurationVar(&c.requestTimeout, "request-timeout", 30*time.Second,
		"how long a request may send none of its body and receive none of its answer before it fails, the wait for the answer counting; 0 sets no bound")
	fs.StringVar(&c.class, "storage-class", "", "the storage class to put into; the bucket's default when empty")
	fs.StringVar(&c.checksum, "checksum", "", "crc32 to send a CRC32 with each object; none when empty")
	fs.BoolVar(&c.compressible, "compressible", false, "write bytes of 16 values, which deflate compresses to about half, in place of random ones")
	fs.StringVar(&c.out, "out", "", "the result file; stdout when empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if usage := c.usage(); len(usage) > 0 {
		fmt.Fprintf(stderr, "s3send: %s\n", strings.Join(usage, "; "))
		return 2
	}

	if c.op == "versioning" {
		if err := c.enableVersioning(ctx); err != nil {
			fmt.Fprintf(stderr, "s3send: %v\n", err)
			return 1
		}
		return 0
	}

	r, failures := c.load(ctx)
	// ctx is the caller's, so only an interrupt has ended it.
	interrupted := ctx.Err() != nil
	r.Command = strings.Join(append([]string{"s3send"}, args...), " ")
	data, err := json.Marshal(r)
	if err != nil {
		fmt.Fprintf(stderr, "s3send: %v\n", err)
		return 1
	}
	data = append(data, '\n')
	if c.out == "" {
		_, err = stdout.Write(data)
	} else {
		err = os.WriteFile(c.out, data, 0o600)
	}
	if err != nil {
		fmt.Fprintf(stderr, "s3send: writing the result: %v\n", err)
		return 1
	}
	status := 0
	if r.Errors > 0 {
		fmt.Fprintf(stderr, "s3send: %d of %d objects failed: %s\n", r.Errors, r.Errors+r.Objects, failures)
		status = 1
	}
	if interrupted {
		fmt.Fprintf(stderr, "s3send: interrupted after %.1f s with %d objects stored: the result is partial\n", r.DurationS, r.Objects)
		status = 1
	}
	if r.Objects == 0 && r.Errors == 0 {
		fmt.Fprintf(stderr, "s3send: stored no object in %.1f s: %d requests were in flight when it ended\n", r.DurationS, r.InFlight)
		status = 1
	}
	return status
}

// usage lists what is wrong with the flags.
func (c config) usage() []string {
	var usage []string
	for _, f := range []struct{ name, value string }{{"-endpoint", c.endpoint}, {"-bucket", c.bucket}, {"-key", c.key}} {
		if f.value == "" {
			usage = append(usage, f.name+" is required")
		}
	}
	if c.secret == "" {
		usage = append(usage, "AWS_SECRET_ACCESS_KEY is not set")
	}
	if c.op != "put" && c.op != "versioning" {
		usage = append(usage, fmt.Sprintf("-op is %q, not put or versioning", c.op))
	}
	if c.threads < 1 {
		usage = append(usage, "-threads must be at least 1")
	}
	if c.objects < 1 {
		usage = append(usage, "-objects must be at least 1")
	}
	if c.size < 1 || c.size > maxSize {
		usage = append(usage, fmt.Sprintf("-size must be from 1 to %d", maxSize))
	}
	if c.block < 0 {
		usage = append(usage, "-block must not be negative")
	}
	if c.duration < 0 {
		usage = append(usage, "-duration must not be negative")
	}
	if c.requestTimeout < 0 {
		usage = append(usage, "-request-timeout must not be negative")
	}
	if c.checksum != "" && c.checksum != "crc32" {
		usage = append(usage, fmt.Sprintf("-checksum is %q, not crc32", c.checksum))
	}
	if c.checksum != "" && c.multipart() {
		usage = append(usage, "-checksum applies to whole-object PUTs only")
	}
	return usage
}

// multipart reports whether an object goes as a multipart upload.
func (c config) multipart() bool {
	return c.block > 0 && c.block < c.size
}

// client is an S3 client for the gateway: path style, no retries, the SDK's
// checksums only where a request asks for one, as elbencho's are, and every
// request bounded by -request-timeout.
func (c config) client(transport *http.Transport) *s3.Client {
	return s3.New(s3.Options{
		Region:                     cmp.Or(c.region, "us-east-1"),
		BaseEndpoint:               new(c.endpoint),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(c.key, c.secret, ""),
		HTTPClient:                 &http.Client{Transport: watched(transport, c.requestTimeout)},
		RetryMaxAttempts:           1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// enableVersioning turns the bucket's versioning on and reads it back.
func (c config) enableVersioning(ctx context.Context) error {
	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := c.client(transport)
	_, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket:                  &c.bucket,
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	})
	if err != nil {
		return fmt.Errorf("enabling versioning on %s: %w", c.bucket, err)
	}
	got, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &c.bucket})
	if err != nil {
		return fmt.Errorf("reading the versioning of %s: %w", c.bucket, err)
	}
	if got.Status != types.BucketVersioningStatusEnabled {
		return fmt.Errorf("%s reports versioning %q after it was enabled", c.bucket, got.Status)
	}
	return nil
}

// watched is base with each request canceled once the transport has read no
// byte of its body to send and it has received no byte of its answer, headers
// or body, for timeout, waiting for the answer counting as neither, or base
// itself when timeout is 0.
func watched(base http.RoundTripper, timeout time.Duration) http.RoundTripper {
	if timeout == 0 {
		return base
	}
	return watchedTransport{base: base, timeout: timeout}
}

type watchedTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t watchedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	w := startWatch(t.timeout, cancel)
	req = req.WithContext(ctx)
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &watchedBody{ReadCloser: req.Body, w: w}
		if getBody := req.GetBody; getBody != nil {
			req.GetBody = func() (io.ReadCloser, error) {
				body, err := getBody()
				if err != nil {
					return nil, err
				}
				return &watchedBody{ReadCloser: body, w: w}, nil
			}
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		w.end()
		return nil, w.blame(err)
	}
	// The answer's headers are bytes of it received.
	w.moved()
	resp.Body = &watchedBody{ReadCloser: resp.Body, w: w, answer: true}
	return resp, nil
}

// watch cancels a request once it has moved no byte for timeout.
type watch struct {
	timeout time.Duration
	cancel  context.CancelCauseFunc
	// err is what a stalled request fails with, naming the bound.
	err   error
	start time.Time
	// last is when a byte last moved, as the time since start.
	last atomic.Int64

	mu           sync.Mutex
	timer        *time.Timer
	ended, fired bool
}

func startWatch(timeout time.Duration, cancel context.CancelCauseFunc) *watch {
	w := &watch{timeout: timeout, cancel: cancel, err: fmt.Errorf("%w (%s)", errStalled, timeout), start: time.Now()}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer = time.AfterFunc(timeout, w.check)
	return w
}

// moved notes that a byte moved.
func (w *watch) moved() {
	w.last.Store(int64(time.Since(w.start)))
}

// check cancels the request when no byte has moved for the timeout, and
// otherwise waits again, for the timeout from the last byte that did.
func (w *watch) check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended {
		return
	}
	if idle := time.Since(w.start) - time.Duration(w.last.Load()); idle < w.timeout {
		w.timer.Reset(w.timeout - idle)
		return
	}
	w.fired = true
	w.cancel(w.err)
}

// stop ends the watch once the answer has arrived whole.
func (w *watch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ended = true
	w.timer.Stop()
}

// end stops the watch and ends the request's context, once nothing more of
// the request is read.
func (w *watch) end() {
	w.stop()
	w.cancel(nil)
}

// blame is err, or err as a stall when the watch canceled the request, which
// the transport reports as a cancellation.
func (w *watch) blame(err error) error {
	w.mu.Lock()
	fired := w.fired
	w.mu.Unlock()
	if !fired || errors.Is(err, errStalled) {
		return err
	}
	return fmt.Errorf("%w: %w", w.err, err)
}

// watchedBody is a request's body or its answer's, each byte read from it
// noted on the request's watch. The answer's stops the watch when it is read
// to its end, and ends it when it is closed.
type watchedBody struct {
	io.ReadCloser
	w      *watch
	answer bool
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.w.moved()
	}
	switch {
	case !b.answer || err == nil:
	case errors.Is(err, io.EOF):
		b.w.stop()
	default:
		err = b.w.blame(err)
	}
	return n, err
}

func (b *watchedBody) Close() error {
	err := b.ReadCloser.Close()
	if b.answer {
		b.w.end()
	}
	return err
}

// worker is one goroutine's tally.
type worker struct {
	lat      []time.Duration
	errors   int64
	inFlight int64
	bytes    int64
	failures map[string]int64
}

// load writes the objects from threads goroutines until each has written its
// own or duration has passed, and returns the result and a summary of the
// failures by kind.
func (c config) load(ctx context.Context) (r Result, failures string) {
	transport := &http.Transport{
		MaxIdleConns:        c.threads,
		MaxIdleConnsPerHost: c.threads,
		DisableCompression:  true,
	}
	defer transport.CloseIdleConnections()
	p := &putter{
		client: c.client(transport), bucket: c.bucket, body: payload(c.size, c.compressible),
		class: types.StorageClass(c.class),
	}
	if c.multipart() {
		p.block = c.block
	}
	if c.checksum == "crc32" {
		p.algorithm = types.ChecksumAlgorithmCrc32
	}

	start := time.Now()
	if c.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, start.Add(c.duration))
		defer cancel()
	}
	workers := make([]worker, c.threads)
	var wg sync.WaitGroup
	for rank := range workers {
		w := &workers[rank]
		w.failures = map[string]int64{}
		wg.Go(func() { w.loop(ctx, p, rank, c.objects) })
	}
	wg.Wait()
	elapsed := time.Since(start)

	r = Result{
		Op: c.op, Threads: c.threads, DurationS: elapsed.Seconds(), Start: start,
		Size: c.size, Block: p.block, StorageClass: c.class, Checksum: c.checksum,
	}
	var lat []time.Duration
	byKind := map[string]int64{}
	for i := range workers {
		w := &workers[i]
		r.Objects += int64(len(w.lat))
		r.Errors += w.errors
		r.InFlight += w.inFlight
		r.Bytes += w.bytes
		lat = append(lat, w.lat...)
		for kind, n := range w.failures {
			byKind[kind] += n
		}
	}
	if elapsed > 0 {
		r.OpsPerS = float64(r.Objects) / elapsed.Seconds()
		r.MiBPerS = float64(r.Bytes) / (1 << 20) / elapsed.Seconds()
	}
	slices.Sort(lat)
	r.LatUs = Latency{P50: percentile(lat, 0.50), P99: percentile(lat, 0.99), P999: percentile(lat, 0.999)}
	var kinds []string
	for _, kind := range slices.Sorted(maps.Keys(byKind)) {
		kinds = append(kinds, fmt.Sprintf("%s ×%d", kind, byKind[kind]))
	}
	return r, strings.Join(kinds, ", ")
}

// loop writes the objects of rank one after another until ctx ends.
func (w *worker) loop(ctx context.Context, p *putter, rank, objects int) {
	for i := range objects {
		if ctx.Err() != nil {
			return
		}
		t0 := time.Now()
		err := p.put(ctx, fmt.Sprintf("r%d-f%d", rank, i))
		if ctx.Err() != nil {
			w.inFlight++
			return
		}
		if err != nil {
			w.errors++
			w.failures[kind(err)]++
			continue
		}
		w.lat = append(w.lat, time.Since(t0))
		w.bytes += int64(len(p.body))
	}
}

// putter writes one object after another into a bucket.
type putter struct {
	client *s3.Client
	bucket string
	// body is every object's bytes, shared by all goroutines and never written
	// to.
	body []byte
	// block is the part size of a multipart upload, or 0 for a whole object.
	block     int64
	class     types.StorageClass
	algorithm types.ChecksumAlgorithm
}

// put stores one object under key.
func (p *putter) put(ctx context.Context, key string) error {
	if p.block > 0 {
		return p.putParts(ctx, key)
	}
	_, err := p.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &p.bucket, Key: &key, Body: bytes.NewReader(p.body),
		StorageClass: p.class, ChecksumAlgorithm: p.algorithm,
	})
	return err
}

// putParts stores one object under key as a multipart upload of block-byte
// parts, aborting the upload when it cannot finish it.
func (p *putter) putParts(ctx context.Context, key string) (err error) {
	created, err := p.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: &p.bucket, Key: &key, StorageClass: p.class,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		// The upload's context may be the one that ended.
		abort, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
		defer cancel()
		if _, aerr := p.client.AbortMultipartUpload(abort, &s3.AbortMultipartUploadInput{
			Bucket: &p.bucket, Key: &key, UploadId: created.UploadId,
		}); aerr != nil {
			err = &abortError{err: err, abort: aerr}
		}
	}()
	var parts []types.CompletedPart
	size := int64(len(p.body))
	for off, n := int64(0), int32(1); off < size; off, n = off+p.block, n+1 {
		sent, uerr := p.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: &p.bucket, Key: &key, UploadId: created.UploadId, PartNumber: new(n),
			Body: bytes.NewReader(p.body[off:min(off+p.block, size)]),
		})
		if uerr != nil {
			return uerr
		}
		parts = append(parts, types.CompletedPart{ETag: sent.ETag, PartNumber: new(n)})
	}
	_, err = p.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &p.bucket, Key: &key, UploadId: created.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	return err
}

// abortError is the failure of a multipart upload, err, and of the abort that
// followed it.
type abortError struct{ err, abort error }

func (e *abortError) Error() string {
	return fmt.Sprintf("%v; aborting the upload: %v", e.err, e.abort)
}

func (e *abortError) Unwrap() []error { return []error{e.err, e.abort} }

// kind names what a failed request was without its request id or key, so that
// the failures of a run sum by kind: that it moved nothing for
// -request-timeout, that an abort was not answered within its own bound, what
// stopped the request from being sent, or the status and code of the
// gateway's answer; an upload whose abort failed too is named by both. Only
// an abort ends a request with a deadline: -request-timeout ends one with
// errStalled, and the run's own end is not an error.
func kind(err error) string {
	if a, ok := errors.AsType[*abortError](err); ok {
		return kind(a.err) + ", and its abort: " + kind(a.abort)
	}
	switch {
	case errors.Is(err, errStalled):
		return errStalled.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("no answer within the abort's own %s", abortTimeout)
	}
	if sendErr, ok := errors.AsType[*url.Error](err); ok {
		return sendErr.Err.Error()
	}
	if resp, ok := errors.AsType[*awshttp.ResponseError](err); ok {
		answer := strconv.Itoa(resp.HTTPStatusCode())
		if api, ok := errors.AsType[smithy.APIError](err); ok {
			answer += " " + api.ErrorCode()
		}
		return answer
	}
	return err.Error()
}

// payload is size bytes: random, or with compressible of 16 values, whose four
// bits of entropy deflate shrinks to about half.
func payload(size int64, compressible bool) []byte {
	buf := make([]byte, size)
	// crypto/rand never fails.
	_, _ = rand.Read(buf)
	if compressible {
		for i := range buf {
			buf[i] = 'a' + buf[i]&0x0f
		}
	}
	return buf
}

// percentile is the nearest-rank p-quantile of the sorted latencies, in µs.
func percentile(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := max(int(math.Ceil(p*float64(len(sorted))))-1, 0)
	return float64(sorted[i].Nanoseconds()) / 1e3
}
