// Command alloc-s3load is a throwaway S3 load driver for the 2026-11-02 allocator measurement,
// never committed. It reproduces the request shapes of hack/bench/gateway's elbencho profiles
// (profiles.go) against a gateway the caller started: T threads, each writing or reading its own
// N objects of a size in blocks of a size, whole PUTs or multipart uploads, whole or ranged GETs,
// bounded by a time limit and, for reads, looping until it. It prints one JSON result.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type config struct {
	endpoint, bucket, op, prefix, label, out string
	threads, objects                         int
	size, block                              int64
	seconds                                  int
	infloop                                  bool
}

type lat struct {
	Mean, P50, P90, P99, P999, Max float64
}

type result struct {
	Label, Op, Bucket         string
	Threads, ObjectsPerThread int
	Size, Block               int64
	TimeLimitS                int
	Infloop                   bool
	Start, End                float64
	ElapsedS                  float64
	Requests, Objects, Errors int64
	Bytes                     int64
	FirstError                string `json:",omitempty"`
	MiBps, RequestsPS, ObjPS  float64
	RequestLatencyMs          lat
	ObjectLatencyMs           lat
	PerThreadObjects          []int64
}

type worker struct {
	reqLat, objLat []time.Duration
	requests       int64
	objects        int64
	bytes          int64
	errors         int64
	firstErr       error
}

func (w *worker) fail(err error) {
	w.errors++
	if w.firstErr == nil {
		w.firstErr = err
	}
}

func main() {
	var c config
	flag.StringVar(&c.endpoint, "endpoint", "", "gateway URL")
	flag.StringVar(&c.bucket, "bucket", "", "bucket")
	flag.StringVar(&c.op, "op", "", "mkbucket, put, get, purge or rmbucket")
	flag.StringVar(&c.prefix, "prefix", "o", "key prefix")
	flag.StringVar(&c.label, "label", "", "label recorded in the result")
	flag.StringVar(&c.out, "out", "", "file the JSON result is written to (stdout too)")
	flag.IntVar(&c.threads, "threads", 1, "threads")
	flag.IntVar(&c.objects, "objects", 1, "objects per thread")
	flag.Int64Var(&c.size, "size", 4096, "object size, bytes")
	flag.Int64Var(&c.block, "block", 0, "block size, bytes: the part size of a write, the range of a read (0: the object size)")
	flag.IntVar(&c.seconds, "seconds", 60, "time limit, seconds")
	flag.BoolVar(&c.infloop, "infloop", false, "reads loop over the thread's objects until the time limit")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if c.block == 0 || c.block > c.size {
		c.block = c.size
	}
	if err := run(context.Background(), c); err != nil {
		slog.Error("alloc-s3load failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func client(c config) *s3.Client {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        c.threads + 16,
		MaxIdleConnsPerHost: c.threads + 16,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	return s3.New(s3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(c.endpoint),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider("BENCH0000000000000AK", "bench-secret-0123456789abcdefghijklmnopqrstuv", ""),
		HTTPClient:                 &http.Client{Transport: tr, Timeout: 5 * time.Minute},
		RetryMaxAttempts:           1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

func run(ctx context.Context, c config) error {
	cl := client(c)
	switch c.op {
	case "mkbucket":
		_, err := cl.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(c.bucket)})
		return err
	case "rmbucket":
		_, err := cl.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(c.bucket)})
		return err
	case "purge":
		return purge(ctx, cl, c)
	case "put", "get":
		return load(ctx, cl, c)
	}
	return fmt.Errorf("unknown op %q", c.op)
}

func key(c config, t, i int) string { return fmt.Sprintf("%s/t%03d/%06d", c.prefix, t, i) }

func load(ctx context.Context, cl *s3.Client, c config) error {
	var data []byte
	if c.op == "put" {
		data = make([]byte, c.size)
		r := rand.New(rand.NewPCG(1, 2))
		for i := 0; i+8 <= len(data); i += 8 {
			v := r.Uint64()
			for j := range 8 {
				data[i+j] = byte(v >> (8 * j))
			}
		}
	}
	ws := make([]*worker, c.threads)
	start := time.Now()
	deadline := start.Add(time.Duration(c.seconds) * time.Second)
	var wg sync.WaitGroup
	for t := range c.threads {
		w := &worker{}
		ws[t] = w
		wg.Go(func() {
			if c.op == "put" {
				putThread(ctx, cl, c, t, w, data, deadline)
			} else {
				getThread(ctx, cl, c, t, w, deadline)
			}
		})
	}
	wg.Wait()
	end := time.Now()
	return report(c, ws, start, end)
}

func putThread(ctx context.Context, cl *s3.Client, c config, t int, w *worker, data []byte, deadline time.Time) {
	for i := range c.objects {
		if time.Now().After(deadline) {
			return
		}
		k := key(c, t, i)
		o0 := time.Now()
		if c.block == c.size {
			r0 := time.Now()
			_, err := cl.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(c.bucket), Key: aws.String(k),
				Body: bytes.NewReader(data), ContentLength: aws.Int64(c.size),
			})
			w.requests++
			w.reqLat = append(w.reqLat, time.Since(r0))
			if err != nil {
				w.fail(err)
				continue
			}
			w.bytes += c.size
		} else if !multipart(ctx, cl, c, k, w, data) {
			continue
		}
		w.objects++
		w.objLat = append(w.objLat, time.Since(o0))
	}
}

func multipart(ctx context.Context, cl *s3.Client, c config, k string, w *worker, data []byte) bool {
	r0 := time.Now()
	cr, err := cl.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(c.bucket), Key: aws.String(k)})
	w.requests++
	w.reqLat = append(w.reqLat, time.Since(r0))
	if err != nil {
		w.fail(err)
		return false
	}
	var parts []types.CompletedPart
	for n, off := int32(1), int64(0); off < c.size; n, off = n+1, off+c.block {
		end := min(off+c.block, c.size)
		r0 = time.Now()
		up, err := cl.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String(c.bucket), Key: aws.String(k), UploadId: cr.UploadId,
			PartNumber: aws.Int32(n), Body: bytes.NewReader(data[off:end]), ContentLength: aws.Int64(end - off),
		})
		w.requests++
		w.reqLat = append(w.reqLat, time.Since(r0))
		if err != nil {
			w.fail(err)
			return false
		}
		w.bytes += end - off
		parts = append(parts, types.CompletedPart{ETag: up.ETag, PartNumber: aws.Int32(n)})
	}
	r0 = time.Now()
	_, err = cl.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(c.bucket), Key: aws.String(k),
		UploadId: cr.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	w.requests++
	w.reqLat = append(w.reqLat, time.Since(r0))
	if err != nil {
		w.fail(err)
		return false
	}
	return true
}

func getThread(ctx context.Context, cl *s3.Client, c config, t int, w *worker, deadline time.Time) {
	buf := make([]byte, 1<<20)
	for pass := 0; pass == 0 || c.infloop; pass++ {
		for i := range c.objects {
			if time.Now().After(deadline) {
				return
			}
			k := key(c, t, i)
			o0 := time.Now()
			ok := true
			for off := int64(0); off < c.size; off += c.block {
				in := &s3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(k)}
				if c.block < c.size {
					in.Range = aws.String(fmt.Sprintf("bytes=%d-%d", off, min(off+c.block, c.size)-1))
				}
				r0 := time.Now()
				n, err := get(ctx, cl, in, buf)
				w.requests++
				w.reqLat = append(w.reqLat, time.Since(r0))
				w.bytes += n
				if err != nil {
					w.fail(err)
					ok = false
					break
				}
			}
			if ok {
				w.objects++
				w.objLat = append(w.objLat, time.Since(o0))
			}
		}
	}
}

func get(ctx context.Context, cl *s3.Client, in *s3.GetObjectInput, buf []byte) (int64, error) {
	out, err := cl.GetObject(ctx, in)
	if err != nil {
		return 0, err
	}
	n, err := io.CopyBuffer(io.Discard, out.Body, buf)
	return n, errors.Join(err, out.Body.Close())
}

// purge lists the bucket and removes every key, a thousand at a time, from eight workers.
func purge(ctx context.Context, cl *s3.Client, c config) error {
	batches := make(chan []types.ObjectIdentifier)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	removed := 0
	for range 8 {
		wg.Go(func() {
			for b := range batches {
				out, err := cl.DeleteObjects(ctx, &s3.DeleteObjectsInput{
					Bucket: aws.String(c.bucket),
					Delete: &types.Delete{Objects: b, Quiet: aws.Bool(true)},
				})
				mu.Lock()
				switch {
				case err != nil:
					errs = append(errs, err)
				case len(out.Errors) > 0:
					errs = append(errs, fmt.Errorf("%d keys not removed, first %s: %s", len(out.Errors),
						aws.ToString(out.Errors[0].Key), aws.ToString(out.Errors[0].Message)))
				default:
					removed += len(b)
				}
				mu.Unlock()
			}
		})
	}
	p := s3.NewListObjectsV2Paginator(cl, &s3.ListObjectsV2Input{Bucket: aws.String(c.bucket)})
	var listErr error
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			listErr = err
			break
		}
		b := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, o := range page.Contents {
			b = append(b, types.ObjectIdentifier{Key: o.Key})
		}
		if len(b) > 0 {
			batches <- b
		}
	}
	close(batches)
	wg.Wait()
	fmt.Printf("{\"Op\":\"purge\",\"Bucket\":%q,\"Removed\":%d}\n", c.bucket, removed)
	return errors.Join(listErr, errors.Join(errs...))
}

func summarize(ds []time.Duration) lat {
	if len(ds) == 0 {
		return lat{}
	}
	slices.Sort(ds)
	ms := func(d time.Duration) float64 { return float64(d) / 1e6 }
	pct := func(p float64) float64 { return ms(ds[min(len(ds)-1, int(p*float64(len(ds))))]) }
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	return lat{Mean: ms(sum) / float64(len(ds)), P50: pct(0.50), P90: pct(0.90), P99: pct(0.99), P999: pct(0.999), Max: ms(ds[len(ds)-1])}
}

func report(c config, ws []*worker, start, end time.Time) error {
	r := result{
		Label: c.label, Op: c.op, Bucket: c.bucket, Threads: c.threads, ObjectsPerThread: c.objects, Size: c.size,
		Block: c.block, TimeLimitS: c.seconds, Infloop: c.infloop,
		Start: float64(start.UnixNano()) / 1e9, End: float64(end.UnixNano()) / 1e9, ElapsedS: end.Sub(start).Seconds(),
	}
	var req, obj []time.Duration
	for _, w := range ws {
		r.Requests += w.requests
		r.Objects += w.objects
		r.Bytes += w.bytes
		r.Errors += w.errors
		if w.firstErr != nil && r.FirstError == "" {
			r.FirstError = w.firstErr.Error()
		}
		r.PerThreadObjects = append(r.PerThreadObjects, w.objects)
		req = append(req, w.reqLat...)
		obj = append(obj, w.objLat...)
	}
	r.MiBps = float64(r.Bytes) / (1 << 20) / r.ElapsedS
	r.RequestsPS = float64(r.Requests) / r.ElapsedS
	r.ObjPS = float64(r.Objects) / r.ElapsedS
	r.RequestLatencyMs = summarize(req)
	r.ObjectLatencyMs = summarize(obj)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	if c.out != "" {
		if err := os.WriteFile(c.out, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	if r.Errors > 0 {
		return fmt.Errorf("%d of %d requests failed, first: %s", r.Errors, r.Requests, r.FirstError)
	}
	return nil
}
