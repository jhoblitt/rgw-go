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
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// sizeClass is one object size of the mix and its share of the requests.
type sizeClass struct {
	Name   string
	Size   int64
	Weight int
	// Keys is how many objects of the class each of the get and put key
	// sets holds. GETs read only the get set and PUTs overwrite only the put
	// set, both written by prep. So the garbage the overwrites leave,
	// collected during the run, is never an object a GET is reading. The
	// sets are small because the cluster's one OSD has 10 GiB.
	Keys int
}

// The owner's ruling: 70% GET, 30% PUT; 4 KiB 50%, 1 MiB 30%, 64 MiB 15%,
// 256 MiB 5% of the requests.
var (
	classes = []sizeClass{
		{"4k", 4 << 10, 50, 1000},
		{"1m", 1 << 20, 30, 100},
		{"64m", 64 << 20, 15, 4},
		{"256m", 256 << 20, 5, 2},
	}
	getPercent = 70
)

// request is one entry of the mix's sequence.
type request struct {
	Put   bool
	Class int
	Key   int
	// Prep marks a PUT that writes the get set.
	Prep bool
}

func (r request) key() string {
	set := "get"
	if r.Put && !r.Prep {
		set = "put"
	}
	return fmt.Sprintf("%s/%s/%05d", set, classes[r.Class].Name, r.Key)
}

// sequence is the mix's first n requests for seed: every variant is given
// the same requests in the same order.
func sequence(seed uint64, n int) []request {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	total := 0
	for _, c := range classes {
		total += c.Weight
	}
	seq := make([]request, n)
	for i := range seq {
		r := request{Put: rng.IntN(100) >= getPercent}
		w := rng.IntN(total)
		for j, c := range classes {
			if w < c.Weight {
				r.Class = j
				break
			}
			w -= c.Weight
		}
		r.Key = rng.IntN(classes[r.Class].Keys)
		seq[i] = r
	}
	return seq
}

// MixResult is what mix writes.
type MixResult struct {
	Op        string    `json:"op"`
	Endpoint  string    `json:"endpoint"`
	Threads   int       `json:"threads"`
	Seed      uint64    `json:"seed"`
	Planned   int       `json:"planned"`
	Issued    int64     `json:"issued"`
	Done      int64     `json:"done"`
	Errors    int64     `json:"errors"`
	Start     time.Time `json:"start"`
	DurationS float64   `json:"duration_s"`
	Bytes     int64     `json:"bytes"`
	MiBPerS   float64   `json:"mib_per_s"`
	ReqPerS   float64   `json:"req_per_s"`
	Stopped   string    `json:"stopped,omitempty"`
	// PausedS counts the once-a-second ticks at which the pause file existed.
	PausedS   int64                `json:"paused_s"`
	Classes   map[string]ClassStat `json:"classes"`
	FirstErrs []string             `json:"first_errors,omitempty"`
	Command   string               `json:"command"`
}

// ClassStat is one op and size class's requests.
type ClassStat struct {
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	Bytes    int64   `json:"bytes"`
	P50ms    float64 `json:"p50_ms"`
	P99ms    float64 `json:"p99_ms"`
	MaxMs    float64 `json:"max_ms"`
}

func mixCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mix", flag.ContinueOnError)
	op := fs.String("op", "run", "prep (write the get set) or run (the mix)")
	endpoint := fs.String("endpoint", "", "gateway URL")
	bucket := fs.String("bucket", "rw-mix", "bucket")
	threads := fs.Int("threads", 32, "requests in flight")
	requests := fs.Int("requests", 0, "requests the run issues, the sequence's first N")
	seconds := fs.Int("seconds", 0, "a time limit for the run, 0 for none")
	seed := fs.Uint64("seed", 20261104, "the sequence's seed")
	out := fs.String("out", "", "the JSON result")
	timeline := fs.String("timeline", "", "a once-a-second CSV of requests in flight, done and bytes")
	stopFile := fs.String("stop", "", "a file whose existence stops new requests")
	pauseFile := fs.String("pause", "", "a file whose existence holds new requests back")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *endpoint == "" {
		return errors.New("mix needs -endpoint")
	}
	cl := newClient(*endpoint, *threads)
	var seq []request
	switch *op {
	case "prep":
		if _, err := cl.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: bucket}); err != nil &&
			!strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") {
			return fmt.Errorf("creating %s: %w", *bucket, err)
		}
		for j, c := range classes {
			for k := range c.Keys {
				seq = append(seq, request{Put: true, Class: j, Key: k, Prep: true},
					request{Put: true, Class: j, Key: k})
			}
		}
	case "run":
		if *requests <= 0 {
			return errors.New("mix -op run needs -requests")
		}
		seq = sequence(*seed, *requests)
	default:
		return fmt.Errorf("unknown -op %q", *op)
	}
	res := drive(ctx, cl, *bucket, seq, *threads, time.Duration(*seconds)*time.Second, *stopFile, *pauseFile, *timeline)
	res.Op, res.Endpoint, res.Threads, res.Seed = *op, *endpoint, *threads, *seed
	res.Command = strings.Join(os.Args, " ")
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	fmt.Println(string(b))
	if res.Errors > 0 {
		return fmt.Errorf("%d of %d requests failed", res.Errors, res.Done)
	}
	return nil
}

// newClient is s3load's client: path-style, us-east-1, the bench user's
// keys, checksums only when required, no retries, one transport with an
// idle connection per thread and 16 more.
func newClient(endpoint string, threads int) *s3.Client {
	tr := &http.Transport{
		MaxIdleConns:        threads + 16,
		MaxIdleConnsPerHost: threads + 16,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	return s3.New(s3.Options{
		BaseEndpoint:               aws.String(endpoint),
		Region:                     "us-east-1",
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider("BENCH0000000000000AK", "bench-secret-0123456789abcdefghijklmnopqrstuv", ""),
		HTTPClient:                 &http.Client{Transport: tr},
		Retryer:                    aws.NopRetryer{},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

type outcome struct {
	class int
	put   bool
	bytes int64
	dur   time.Duration
	err   error
}

func drive(ctx context.Context, cl *s3.Client, bucket string, seq []request, threads int,
	limit time.Duration, stopFile, pauseFile, timeline string,
) MixResult {
	exists := func(p string) bool {
		if p == "" {
			return false
		}
		_, err := os.Stat(p)
		return err == nil
	}
	// One read-only body for every PUT: a request reads its prefix.
	var maxSize int64
	for _, c := range classes {
		maxSize = max(maxSize, c.Size)
	}
	body := make([]byte, maxSize)
	rand.NewChaCha8([32]byte{1}).Read(body) //nolint:errcheck // ChaCha8.Read never fails

	var next, issued, done, errs, inflight, doneBytes atomic.Int64
	var stopped atomic.Value
	stopped.Store("")
	start := time.Now()
	deadline := time.Time{}
	if limit > 0 {
		deadline = start.Add(limit)
	}
	results := make(chan outcome, threads*4)
	var wg sync.WaitGroup
	for range threads {
		wg.Go(func() {
			for {
				// A pause holds new requests back while the ones in flight
				// finish, so that the garbage collector catches up.
				for exists(pauseFile) && !exists(stopFile) && ctx.Err() == nil {
					time.Sleep(100 * time.Millisecond)
				}
				i := next.Add(1) - 1
				if i >= int64(len(seq)) || ctx.Err() != nil {
					return
				}
				if !deadline.IsZero() && time.Now().After(deadline) {
					stopped.Store("time limit")
					return
				}
				if exists(stopFile) {
					stopped.Store("stop file")
					return
				}
				r := seq[i]
				key := r.key()
				issued.Add(1)
				inflight.Add(1)
				o := one(ctx, cl, bucket, key, r, body)
				inflight.Add(-1)
				done.Add(1)
				if o.err != nil {
					errs.Add(1)
				} else {
					doneBytes.Add(o.bytes)
				}
				results <- o
			}
		})
	}
	// Once a second: the timeline's line, and the seconds the run was paused.
	var pausedTicks atomic.Int64
	tlDone := make(chan struct{})
	tlCtx, tlCancel := context.WithCancel(ctx)
	defer tlCancel()
	go func() {
		defer close(tlDone)
		w := bufio.NewWriter(io.Discard)
		if timeline != "" {
			f, err := os.Create(timeline)
			if err == nil {
				defer f.Close()
				w = bufio.NewWriter(f)
			}
		}
		defer w.Flush()
		fmt.Fprintln(w, "t,inflight,done,errors,bytes,paused")
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			p := 0
			if exists(pauseFile) {
				p = 1
				pausedTicks.Add(1)
			}
			fmt.Fprintf(w, "%.3f,%d,%d,%d,%d,%d\n", float64(time.Now().UnixNano())/1e9,
				inflight.Load(), done.Load(), errs.Load(), doneBytes.Load(), p)
			w.Flush()
			select {
			case <-t.C:
			case <-tlCtx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	type acc struct {
		n, e, b int64
		durs    []time.Duration
	}
	accs := map[string]*acc{}
	var firstErrs []string
	for o := range results {
		op := "get"
		if o.put {
			op = "put"
		}
		k := op + "_" + classes[o.class].Name
		a := accs[k]
		if a == nil {
			a = &acc{}
			accs[k] = a
		}
		a.n++
		if o.err != nil {
			a.e++
			if len(firstErrs) < 10 {
				firstErrs = append(firstErrs, o.err.Error())
			}
			continue
		}
		a.b += o.bytes
		a.durs = append(a.durs, o.dur)
	}
	elapsed := time.Since(start)
	tlCancel()
	<-tlDone
	res := MixResult{
		Planned: len(seq), Issued: issued.Load(), Done: done.Load(), Errors: errs.Load(),
		Start: start, DurationS: elapsed.Seconds(), Bytes: doneBytes.Load(),
		Stopped: stopped.Load().(string), Classes: map[string]ClassStat{}, FirstErrs: firstErrs,
		PausedS: pausedTicks.Load(),
	}
	res.MiBPerS = float64(res.Bytes) / (1 << 20) / elapsed.Seconds()
	res.ReqPerS = float64(res.Done) / elapsed.Seconds()
	for k, a := range accs {
		slices.Sort(a.durs)
		res.Classes[k] = ClassStat{
			Requests: a.n, Errors: a.e, Bytes: a.b,
			P50ms: ms(quantile(a.durs, 0.50)), P99ms: ms(quantile(a.durs, 0.99)), MaxMs: ms(quantile(a.durs, 1)),
		}
	}
	return res
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// quantile is the nearest-rank q quantile of the sorted durs.
func quantile(durs []time.Duration, q float64) time.Duration {
	if len(durs) == 0 {
		return 0
	}
	i := int(float64(len(durs))*q+0.999999) - 1
	return durs[min(max(i, 0), len(durs)-1)]
}

func one(ctx context.Context, cl *s3.Client, bucket, key string, r request, body []byte) outcome {
	c := classes[r.Class]
	o := outcome{class: r.Class, put: r.Put}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	t0 := time.Now()
	if r.Put {
		_, err := cl.PutObject(rctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key),
			Body: bytes.NewReader(body[:c.Size]), ContentLength: aws.Int64(c.Size),
		})
		o.dur, o.err = time.Since(t0), err
		if err == nil {
			o.bytes = c.Size
		}
		return o
	}
	out, err := cl.GetObject(rctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		o.dur, o.err = time.Since(t0), err
		return o
	}
	n, err := io.Copy(io.Discard, out.Body)
	out.Body.Close()
	o.dur = time.Since(t0)
	if err == nil && n != c.Size {
		err = fmt.Errorf("GET %s: %d bytes, want %d", key, n, c.Size)
	}
	o.bytes, o.err = n, err
	return o
}
