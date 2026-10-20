package s3_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// someBlockSettings is the block the specs' someDoc puts.
var someBlockSettings = acl.PublicAccessBlock{BlockPublicACLs: true, RestrictPublicBuckets: true}

var _ = Describe("bucket public access block routes", func() {
	const (
		target  = "/plain?publicAccessBlock"
		someDoc = `<PublicAccessBlockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
			`<BlockPublicAcls>true</BlockPublicAcls><RestrictPublicBuckets>TRUE</RestrictPublicBuckets>` +
			`</PublicAccessBlockConfiguration>`
		// notFound is the message RGWGetBucketPublicAccessBlock::execute sets
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8698-L8699 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9666-L9667 at v20.2.4).
		notFound = "The public access block configuration was not found"
	)
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

	// expectEmpty checks rec is status with no body and no type, as an
	// end_header(s) with an empty formatter sends it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L611-L619 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L616-L624 at v20.2.4),
	// and a 200 with the Content-Length: 0 the frontend completes it with. A
	// 204 carries no length from either gateway, whatever the recorder holds:
	// radosgw's ConLenControllingFilter drops the frontend's
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_client_io_filters.h#L342-L364 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_client_io_filters.h#L342-L364 at v20.2.4),
	// and net/http deletes the one writeEmpty sets (net/http/server.go:1488-1490
	// at go1.27.1), so a 204's is not checked.
	expectEmpty := func(rec *httptest.ResponseRecorder, status int) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Body.String()).To(BeEmpty())
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
		if status != http.StatusNoContent {
			Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
		}
	}
	// expectUnwritten checks plain still has the instance version before.
	expectUnwritten := func(ctx context.Context, before meta.ObjVersion) {
		GinkgoHelper()
		Expect(w.bucket(ctx).Version).To(Equal(before), "nothing written")
	}
	// errorDocument is the document end_header's error branch sends for rec.
	errorDocument := func(rec *httptest.ResponseRecorder, code, message string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>` + message + `</Message>` +
			`<BucketName>plain</BucketName><RequestId>` + exactHeader(rec.Header(), "x-amz-request-id") +
			`</RequestId><HostId>4155-z-zg</HostId></Error>`
	}

	It("binds the three routes in every handler", func() {
		h := newHandler(w.store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements("bucket/get_bucket_public_access_block",
			"bucket/put_bucket_public_access_block", "bucket/delete_bucket_public_access_block"))
	})

	It("puts with 200 and no body, gets the stored block, and deletes with 204", func(ctx SpecContext) {
		expectEmpty(w.send(w.alice, http.MethodPut, target, someDoc), http.StatusOK)
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, encodePublicAccess(someBlockSettings)))

		rec := w.send(w.alice, http.MethodGet, target, "")
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), "to_mime_type, rgw_rest_s3.cc:4619 at v19.2.7")
		want := `<?xml version="1.0" encoding="UTF-8"?><PublicAccessBlockConfiguration>` +
			`<BlockPublicAcls>true</BlockPublicAcls><IgnorePublicAcls>false</IgnorePublicAcls>` +
			`<BlockPublicPolicy>false</BlockPublicPolicy><RestrictPublicBuckets>true</RestrictPublicBuckets>` +
			`</PublicAccessBlockConfiguration>`
		Expect(rec.Body.String()).To(Equal(want), "dump_xml after dump_start, rgw_rest_s3.cc:4613-4624 at v19.2.7")
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "the length the frontend adds, not dump_content_length's")

		expectEmpty(w.send(w.alice, http.MethodDelete, target, ""), http.StatusNoContent)
		Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrPublicAccess))
	})

	It("replaces a stored block whole", func(ctx SpecContext) {
		w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{
			BlockPublicACLs: true, IgnorePublicACLs: true, BlockPublicPolicy: true, RestrictPublicBuckets: true,
		})})
		expectEmpty(w.send(w.alice, http.MethodPut, target, `<PublicAccessBlockConfiguration/>`), http.StatusOK)
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, encodePublicAccess(acl.PublicAccessBlock{})))
	})

	It("answers a DELETE on a bucket without a block 204", func() {
		expectEmpty(w.send(w.alice, http.MethodDelete, target, ""), http.StatusNoContent)
	})

	Describe("an error from the op's execute, which radosgw follows with a block document past the Content-Length", func() {
		DescribeTable("is the error document alone, its bytes ending at its Content-Length",
			func(ctx SpecContext, stored []byte, admin bool, status int, code, message string) {
				buf := captureLog()
				if stored != nil {
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: stored})
				}
				auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
					return &op.AuthResult{Identity: op.Identity{
						User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: w.alice.Info.OpMask, Admin: admin,
					}}, nil
				})
				rec := serveReq(s3.NewHandler(w.env, auth, testConfig(s3.Config{})), http.MethodGet, target, nil)
				Expect(rec.Code).To(Equal(status), rec.Body.String())
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
				Expect(rec.Body.String()).To(Equal(errorDocument(rec, code, message)),
					"end_header's error branch, rgw_rest.cc:620-625 at v19.2.7, and nothing after it")
				Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
				if status < http.StatusInternalServerError {
					Expect(logRecords(buf)).To(BeEmpty(), "WriteError logs no 4xx")
					return
				}
				Expect(logRecords(buf)).To(ConsistOf(SatisfyAll(
					HaveKeyWithValue("level", "ERROR"),
					HaveKeyWithValue("msg", "request failed"),
					HaveKeyWithValue("code", code),
					HaveKeyWithValue("request_id", exactHeader(rec.Header(), "x-amz-request-id")),
				)), "WriteError logs an UnknownError once")
			},
			Entry("a bucket without a block: 404 NoSuchPublicAccessBlockConfiguration",
				nil, false, http.StatusNotFound, "NoSuchPublicAccessBlockConfiguration", notFound),
			Entry("a block that does not decode, which in rgw-go only an admin reaches: radosgw's -EIO, 500 UnknownError",
				[]byte{1}, true, http.StatusInternalServerError, "UnknownError", ""),
		)

		It("keeps a keep-alive connection in step", func(ctx SpecContext) {
			srv := httptest.NewServer(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})))
			DeferCleanup(srv.Close)
			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(conn.Close)
			host := srv.Listener.Addr().String()
			_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\nGET /plain?versioning HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
				target, host, host)
			Expect(err).NotTo(HaveOccurred())
			br := bufio.NewReader(conn)
			first, err := http.ReadResponse(br, nil)
			Expect(err).NotTo(HaveOccurred())
			body, err := io.ReadAll(first.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(first.StatusCode).To(Equal(http.StatusNotFound), string(body))
			Expect(string(body)).To(HaveSuffix("</Error>"))
			second, err := http.ReadResponse(br, nil)
			Expect(err).NotTo(HaveOccurred(), "the next response starts where the 404's Content-Length ends")
			body, err = io.ReadAll(second.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(second.StatusCode).To(Equal(http.StatusOK), string(body))
			Expect(string(body)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><VersioningConfiguration`))
		})
	})

	Describe("PUT", func() {
		DescribeTable("answers a document it does not read 400 MalformedXML and stores nothing",
			func(ctx SpecContext, body string) {
				before := w.bucket(ctx).Version
				rec := w.send(w.alice, http.MethodPut, target, body, "Content-Length", strconv.Itoa(len(body)))
				Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
				Expect(rec.Body.String()).To(Equal(errorDocument(rec, "MalformedXML", "")))
				expectUnwritten(ctx, before)
			},
			Entry("a misspelled setting, which radosgw stores false (docs/exclusions.md)",
				`<PublicAccessBlockConfiguration><BlockPublicACLs>true</BlockPublicACLs></PublicAccessBlockConfiguration>`),
			Entry("an empty body, which expat refuses", ``),
		)
		It("refuses a requester neither owner nor policy admits with 403 AccessDenied, before it reads the body", func(ctx SpecContext) {
			before := w.bucket(ctx).Version
			body := &countingBody{r: strings.NewReader(someDoc)}
			rec := serveReq(s3.NewHandler(w.env, authAs(w.bob), testConfig(s3.Config{})), http.MethodPut, target, body)
			Expect(rec.Code).To(Equal(http.StatusForbidden), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
			Expect(body.n).To(BeZero(), "get_params runs in execute, after verify_permission, rgw_op.cc:8633-8644 at v19.2.7")
			expectUnwritten(ctx, before)
		})
		It("answers a missing bucket 404 NoSuchBucket before it reads the body", func() {
			body := &countingBody{r: strings.NewReader(someDoc)}
			rec := serveReq(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})), http.MethodPut,
				"/missing?publicAccessBlock", body)
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
			Expect(body.n).To(BeZero())
		})
		It("refuses a body past rgw_max_put_param_size as 416 InvalidRange, unread", func(ctx SpecContext) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			before := w.bucket(ctx).Version
			rec := w.send(w.alice, http.MethodPut, target, someDoc)
			Expect(rec.Code).To(Equal(http.StatusRequestedRangeNotSatisfiable), "read_all_input's -ERANGE, rgw_rest.cc:1556-1557 at v19.2.7")
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRange</Code>"))
			expectUnwritten(ctx, before)
		})
		It("reads a chunked body whole, bounded by read_all_chunked_input's buffers", func(ctx SpecContext) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			chunked := func(n int) *httptest.ResponseRecorder {
				return serveReq(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})), http.MethodPut, target,
					io.MultiReader(strings.NewReader(someDoc+strings.Repeat(" ", n-len(someDoc)))))
			}
			before := w.bucket(ctx).Version
			rec := chunked(4096)
			Expect(rec.Code).To(Equal(http.StatusRequestedRangeNotSatisfiable), "a full 4096-byte buffer past the limit, rgw_rest.cc:1526-1532 at v19.2.7")
			expectUnwritten(ctx, before)
			expectEmpty(chunked(4095), http.StatusOK)
			Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, encodePublicAccess(someBlockSettings)))
		})
		It("refuses a request without a Content-Length with 411 MissingContentLength", func() {
			rec := w.send(w.alice, http.MethodPut, target, "")
			Expect(rec.Code).To(Equal(http.StatusLengthRequired), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>MissingContentLength</Code>"))
		})
		It("refuses a body whose payload hash does not match, storing nothing", func(ctx SpecContext) {
			before := w.bucket(ctx).Version
			rec := w.sendTampered(target, someDoc)
			Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>XAmzContentSHA256Mismatch</Code>"))
			expectUnwritten(ctx, before)
		})
	})

	It("refuses a GET and a DELETE by a requester neither owner nor policy admits with 403 AccessDenied", func(ctx SpecContext) {
		w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(someBlockSettings)})
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			rec := w.send(w.bob, method, target, "")
			Expect(rec.Code).To(Equal(http.StatusForbidden), "%s: %s", method, rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"), method)
		}
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, encodePublicAccess(someBlockSettings)))
	})

	It("serves a requester the bucket policy admits, the op deciding alone", func(ctx SpecContext) {
		const grant = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/bob"]},` +
			`"Action":["s3:GetBucketPublicAccessBlock","s3:PutBucketPublicAccessBlock"],"Resource":"arn:aws:s3:::plain"}]}`
		Expect(w.send(w.alice, http.MethodPut, "/plain?policy", grant).Code).To(Equal(http.StatusNoContent))
		expectEmpty(w.send(w.bob, http.MethodPut, target, someDoc), http.StatusOK)
		Expect(w.send(w.bob, http.MethodGet, target, "").Code).To(Equal(http.StatusOK))
		expectEmpty(w.send(w.bob, http.MethodDelete, target, ""), http.StatusNoContent)
		Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrPublicAccess))
	})
})
