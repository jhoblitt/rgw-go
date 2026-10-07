package s3_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// blockingDeletes is an object store whose DeleteObject of key waits until
// release is closed.
type blockingDeletes struct {
	op.ObjectStore
	key     string
	release <-chan struct{}
}

func (b blockingDeletes) DeleteObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) error {
	if key.Name == b.key {
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.ObjectStore.DeleteObject(ctx, rec, key, p)
}

// serveWorld serves w as alice behind a real HTTP server, so that a spec
// sees the framing a client sees.
func serveWorld(w *writeWorld) *httptest.Server {
	srv := httptest.NewServer(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})))
	DeferCleanup(srv.Close)
	return srv
}

// doRequest sends method, path and body to srv; hdr alternates header names
// and values. The response comes back unread.
func doRequest(ctx context.Context, srv *httptest.Server, method, path, body string, hdr ...string) *http.Response {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := srv.Client().Do(req)
	Expect(err).NotTo(HaveOccurred())
	return resp
}

var _ = Describe("streamed responses", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })

	It("streams DeleteObjects chunked, the open tag first and each element as its key completes", func(ctx SpecContext) {
		w.put(ctx, "a", "x", nil)
		w.put(ctx, "b", "y", nil)
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		DeferCleanup(unblock)
		w.env.Objects = blockingDeletes{ObjectStore: w.store, key: "b", release: release}
		w.conf["rgw_multi_obj_del_max_aio"] = "1"
		srv := serveWorld(w)
		resp := doRequest(ctx, srv, http.MethodPost, "/plain?delete", deleteDoc("<Key>a</Key>", "<Key>b</Key>"))
		DeferCleanup(resp.Body.Close)
		Expect(resp.StatusCode).To(Equal(200))
		Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "end_header(..., CHUNKED_TRANSFER_ENCODING), rgw_rest_s3.cc:4267")
		Expect(resp.ContentLength).To(BeEquivalentTo(-1))
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"))
		var (
			mu  sync.Mutex
			got bytes.Buffer
		)
		done := make(chan struct{})
		go func() { // stops when the body ends or the spec closes it
			defer GinkgoRecover()
			defer close(done)
			buf := make([]byte, 512)
			for {
				n, err := resp.Body.Read(buf)
				mu.Lock()
				got.Write(buf[:n])
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
		read := func() string {
			mu.Lock()
			defer mu.Unlock()
			return got.String()
		}
		opened := deleteResultOpen + "<Deleted><Key>a</Key></Deleted>"
		Eventually(read).WithTimeout(5*time.Second).WithPolling(10*time.Millisecond).Should(Equal(opened),
			"a's element arrives while b's delete is still blocked")
		Consistently(read).WithTimeout(50*time.Millisecond).WithPolling(10*time.Millisecond).Should(Equal(opened),
			"nothing more until b completes")
		unblock()
		Eventually(read).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Equal(opened + "<Deleted><Key>b</Key></Deleted></DeleteResult>"))
		Eventually(done).WithTimeout(5 * time.Second).Should(BeClosed())
	})
	DescribeTable("answers a DeleteObjects that fails before its first key with the status line alone, as send_status does",
		func(ctx SpecContext, body string, hdr ...string) {
			w.conf["rgw_delete_multi_obj_max_num"] = "1"
			resp := doRequest(ctx, serveWorld(w), http.MethodPost, "/plain?delete", body, hdr...)
			b, err := io.ReadAll(resp.Body)
			Expect(resp.Body.Close()).To(Succeed())
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(400))
			Expect(b).To(BeEmpty(), "no error document: execute calls send_status alone, rgw_op.cc:7102-7104 at v19.2.6")
			Expect(resp.ContentLength).To(BeZero(), "the Content-Length: 0 a response without end_header gets, rgw_client_io_filters.h:221-253")
			Expect(resp.TransferEncoding).To(BeEmpty())
			for _, h := range []string{"X-Amz-Request-Id", "Server", "Content-Type", "Accept-Ranges"} {
				Expect(resp.Header).NotTo(HaveKey(h), "dump_trans_id and the type run only in end_header")
			}
		},
		Entry("more keys than rgw_delete_multi_obj_max_num", deleteDoc("<Key>a</Key>", "<Key>b</Key>")),
		Entry("a document that does not parse", "<Delete><Object>"),
		Entry("an empty body", "", "Content-Length", "0"),
	)
	It("frames CopyObject's result as radosgw does: chunked, no Progress for a local copy", func(ctx SpecContext) {
		resp := doRequest(ctx, serveWorld(w), http.MethodPut, "/plain/dst", "", "X-Amz-Copy-Source", "/plain/src")
		DeferCleanup(resp.Body.Close)
		Expect(resp.StatusCode).To(Equal(200))
		Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "send_partial_response(0), rgw_rest_s3.cc:3566-3588")
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"))
		b, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(MatchRegexp(`^<\?xml version="1\.0" encoding="UTF-8"\?><CopyObjectResult xmlns="http://s3\.amazonaws\.com/doc/2006-03-01/"><LastModified>[0-9T:.Z-]+</LastModified><ETag>&quot;[0-9a-f]{32}&quot;</ETag></CopyObjectResult>$`),
			"dump_format escapes the quotes, rgw_rest_s3.cc:3598")
		Expect(string(b)).NotTo(ContainSubstring("<Progress>"))
	})
	It("answers a CopyObject that fails with the error document and its length", func(ctx SpecContext) {
		resp := doRequest(ctx, serveWorld(w), http.MethodPut, "/plain/dst", "", "X-Amz-Copy-Source", "/plain/missing")
		b, err := io.ReadAll(resp.Body)
		Expect(resp.Body.Close()).To(Succeed())
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(404))
		Expect(resp.ContentLength).To(BeEquivalentTo(len(b)))
		Expect(string(b)).To(ContainSubstring("<Code>NoSuchKey</Code>"))
	})
})

var _ = Describe("deleteResultElement", func() {
	DescribeTable("renders send_partial_response's elements",
		func(res op.DeleteResult, quiet bool, want string) {
			Expect(s3.DeleteResultElementForTest(res, quiet)).To(Equal(want))
		},
		Entry("a plain delete", op.DeleteResult{Key: meta.ObjKey{Name: "k"}}, false, "<Deleted><Key>k</Key></Deleted>"),
		Entry("a versioned delete without a versionId that created a marker",
			op.DeleteResult{Key: meta.ObjKey{Name: "key"}, DeleteMarker: true, MarkerVersionID: "mv1"}, false,
			"<Deleted><Key>key</Key><DeleteMarker>true</DeleteMarker><DeleteMarkerVersionId>mv1</DeleteMarkerVersionId></Deleted>"),
		Entry("a delete of one version names it",
			op.DeleteResult{Key: meta.ObjKey{Name: "key", Instance: "v7"}}, false,
			"<Deleted><Key>key</Key><VersionId>v7</VersionId></Deleted>"),
		Entry("quiet hides a success", op.DeleteResult{Key: meta.ObjKey{Name: "k"}}, true, ""),
		Entry("quiet still reports a failure, with its empty VersionId",
			op.DeleteResult{Key: meta.ObjKey{Name: "k"}, Err: op.ErrAccessDenied}, true,
			"<Error><Key>k</Key><VersionId></VersionId><Code>AccessDenied</Code><Message>AccessDenied</Message></Error>"),
		Entry("a failure names the key's version",
			op.DeleteResult{Key: meta.ObjKey{Name: "k", Instance: "v1"}, Err: op.ErrPreconditionFailed}, false,
			"<Error><Key>k</Key><VersionId>v1</VersionId><Code>PreconditionFailed</Code><Message>PreconditionFailed</Message></Error>"),
		Entry("names are escaped as xml_stream_escaper escapes them",
			op.DeleteResult{Key: meta.ObjKey{Name: "a&b<c>\"d'e\x01"}}, false,
			"<Deleted><Key>a&amp;b&lt;c&gt;&quot;d&apos;e&#x01;</Key></Deleted>"),
	)
})
