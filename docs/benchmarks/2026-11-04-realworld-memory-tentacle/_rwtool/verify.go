package main

import (
	"context"
	"crypto/md5" //nolint:gosec // S3's ETag of a single-part object is its MD5
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// verifyCmd reads every object of a bucket whole, THREADS at a time, and
// checks each body's MD5 against its single-part ETag, so that a gateway
// which served bytes from a reused buffer is caught.
func verifyCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	endpoint := fs.String("endpoint", "", "gateway URL")
	bucket := fs.String("bucket", "", "bucket")
	threads := fs.Int("threads", 32, "GETs in flight")
	passes := fs.Int("passes", 1, "times every object is read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *endpoint == "" || *bucket == "" {
		return errors.New("verify needs -endpoint and -bucket")
	}
	cl := newClient(*endpoint, *threads)
	type obj struct{ key, etag string }
	var objs []obj
	p := s3.NewListObjectsV2Paginator(cl, &s3.ListObjectsV2Input{Bucket: bucket})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("listing %s: %w", *bucket, err)
		}
		for _, o := range page.Contents {
			objs = append(objs, obj{aws.ToString(o.Key), strings.Trim(aws.ToString(o.ETag), `"`)})
		}
	}
	work := make(chan obj)
	var checked, bad, failed atomic.Int64
	var mu sync.Mutex
	var firstBad []string
	var wg sync.WaitGroup
	for range *threads {
		wg.Go(func() {
			for o := range work {
				out, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: aws.String(o.key)})
				if err != nil {
					failed.Add(1)
					continue
				}
				h := md5.New() //nolint:gosec // see the import
				_, err = io.Copy(h, out.Body)
				out.Body.Close()
				if err != nil {
					failed.Add(1)
					continue
				}
				checked.Add(1)
				if sum := hex.EncodeToString(h.Sum(nil)); sum != o.etag {
					bad.Add(1)
					mu.Lock()
					if len(firstBad) < 10 {
						firstBad = append(firstBad, o.key+" md5 "+sum+" etag "+o.etag)
					}
					mu.Unlock()
				}
			}
		})
	}
	for range *passes {
		for _, o := range objs {
			work <- o
		}
	}
	close(work)
	wg.Wait()
	fmt.Printf("{\"bucket\":%q,\"objects\":%d,\"passes\":%d,\"checked\":%d,\"mismatched\":%d,\"failed\":%d,\"first_mismatches\":%q}\n",
		*bucket, len(objs), *passes, checked.Load(), bad.Load(), failed.Load(), firstBad)
	if bad.Load() > 0 || failed.Load() > 0 {
		return fmt.Errorf("%d mismatched, %d failed", bad.Load(), failed.Load())
	}
	return nil
}
