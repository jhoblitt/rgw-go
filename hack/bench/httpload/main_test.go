package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// emptyListing is the answer radosgw gives the anonymous GET /.
const emptyListing = `<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
	`<Owner><ID>anonymous</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>`

var _ = Describe("httpload", func() {
	var (
		stdout, stderr *bytes.Buffer
		out            string
		signed, served atomic.Int64
	)

	BeforeEach(func() {
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
		out = filepath.Join(GinkgoT().TempDir(), "noop-4.json")
		signed.Store(0)
		served.Store(0)
	})

	server := func(status int, body string) *httptest.Server {
		GinkgoHelper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served.Add(1)
			if r.Header.Get("Authorization") != "" {
				signed.Add(1)
			}
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}))
		DeferCleanup(srv.Close)
		return srv
	}

	load := func(ctx SpecContext, args ...string) int {
		return run(ctx, args, stdout, stderr)
	}

	result := func() Result {
		GinkgoHelper()
		data, err := os.ReadFile(out)
		Expect(err).NotTo(HaveOccurred())
		var r Result
		Expect(json.Unmarshal(data, &r)).To(Succeed())
		return r
	}

	It("loads an answering server for its duration and writes consistent counts and percentiles", func(ctx SpecContext) {
		srv := server(http.StatusOK, emptyListing)
		Expect(load(ctx, "-url", srv.URL+"/", "-threads", "4", "-duration", "1s", "-out", out)).To(Equal(0), stderr.String())
		r := result()
		Expect(r.Threads).To(Equal(4))
		Expect(r.Errors).To(BeZero())
		Expect(r.Requests).To(BeNumerically(">", 100))
		Expect(r.Requests).To(BeNumerically("<=", served.Load()), "a request the server never saw counted")
		Expect(r.DurationS).To(BeNumerically("~", 1, 0.25))
		Expect(r.OpsPerS).To(BeNumerically("~", float64(r.Requests)/r.DurationS, 0.01))
		Expect(r.LatUs.P50).To(BeNumerically(">", 0))
		Expect(r.LatUs.P50).To(BeNumerically("<=", r.LatUs.P99))
		Expect(r.LatUs.P99).To(BeNumerically("<=", r.LatUs.P999))
		Expect(r.Start).To(BeTemporally("~", time.Now(), 5*time.Second))
		Expect(r.Command).To(Equal("httpload -url " + srv.URL + "/ -threads 4 -duration 1s -out " + out))
		Expect(signed.Load()).To(BeZero(), "requests carried an Authorization header")
	})

	It("counts every answer but a 200 as an error and exits 1", func(ctx SpecContext) {
		srv := server(http.StatusForbidden, `<Error><Code>AccessDenied</Code></Error>`)
		Expect(load(ctx, "-url", srv.URL+"/", "-threads", "2", "-duration", "1s", "-out", out)).To(Equal(1))
		r := result()
		Expect(r.Requests).To(BeNumerically(">", 0))
		Expect(r.Errors).To(Equal(r.Requests))
		Expect(stderr.String()).To(ContainSubstring("403"))
	})

	It("counts a 200 without a bucket listing as an error", func(ctx SpecContext) {
		srv := server(http.StatusOK, "<html>a proxy's page</html>")
		Expect(load(ctx, "-url", srv.URL+"/", "-threads", "1", "-duration", "1s", "-out", out)).To(Equal(1))
		Expect(result().Errors).To(BeNumerically(">", 0))
	})

	It("counts a refused connection as an error", func(ctx SpecContext) {
		srv := server(http.StatusOK, emptyListing)
		url := srv.URL + "/"
		srv.Close()
		Expect(load(ctx, "-url", url, "-threads", "1", "-duration", "1s", "-out", out)).To(Equal(1))
		Expect(result().Errors).To(BeNumerically(">", 0))
	})

	DescribeTable("refuses a bad command line with status 2",
		func(ctx SpecContext, args []string, want string) {
			Expect(load(ctx, args...)).To(Equal(2))
			Expect(stderr.String()).To(ContainSubstring(want))
		},
		Entry("no url", []string{"-threads", "1", "-duration", "1s"}, "-url is required"),
		Entry("no threads", []string{"-url", "http://127.0.0.1:1/", "-threads", "0", "-duration", "1s"}, "-threads must be at least 1"),
		Entry("no duration", []string{"-url", "http://127.0.0.1:1/", "-threads", "1", "-duration", "0s"}, "-duration must be positive"),
		Entry("an unknown flag", []string{"-url", "http://127.0.0.1:1/", "-sign"}, "flag provided but not defined: -sign"),
	)
})
