package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/sse"
)

func encodeEncryption(c sse.BucketEncryptionConfig) []byte {
	e := denc.NewEncoder()
	c.Encode(e, denc.Squid)
	return e.Bytes()
}

// The documents are the bodies boto3 sends for s3-tests'
// _put_bucket_encryption_s3 and _put_bucket_encryption_kms, the latter with
// the bucket key on and the key id the suite falls back to without a KMS.
const (
	aesEncryptionDoc = `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm>` +
		`</ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`
	kmsEncryptionDoc = `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>aws:kms</SSEAlgorithm>` +
		`<KMSMasterKeyID>fool-me-again</KMSMasterKeyID></ApplyServerSideEncryptionByDefault>` +
		`<BucketKeyEnabled>true</BucketKeyEnabled></Rule></ServerSideEncryptionConfiguration>`
)

// aesEncryption is aesEncryptionDoc's configuration.
func aesEncryption() sse.BucketEncryptionConfig {
	return sse.BucketEncryptionConfig{Rule: &sse.ServerSideEncryptionConfiguration{
		Apply: sse.ApplyServerSideEncryptionByDefault{SSEAlgorithm: "AES256"},
	}}
}

// kmsEncryption is kmsEncryptionDoc's configuration.
func kmsEncryption() sse.BucketEncryptionConfig {
	return sse.BucketEncryptionConfig{Rule: &sse.ServerSideEncryptionConfiguration{
		Apply:            sse.ApplyServerSideEncryptionByDefault{SSEAlgorithm: "aws:kms", KMSMasterKeyID: "fool-me-again"},
		BucketKeyEnabled: true,
	}}
}

var _ = Describe("bucket encryption configuration routes", func() {
	const (
		target = "/plain?encryption"
		// notFound is the message RGWGetBucketEncryption::execute sets
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8825-L8829 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9793-L9797 at v20.2.4).
		notFound = "The server side encryption configuration was not found"
	)
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

	// expectEmpty checks rec is status with no body and no type, as
	// end_header(s) sends a response whose formatter is empty
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L611-L619 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L616-L624 at v20.2.4),
	// and a 200 with the Content-Length: 0 the frontend completes it with. A
	// 204's length is not checked: neither gateway sends one.
	expectEmpty := func(rec *httptest.ResponseRecorder, status int) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Body.String()).To(BeEmpty())
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
		if status != http.StatusNoContent {
			Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
		}
	}
	// expectDocument checks rec is 200 with doc after the XML declaration,
	// as application/xml, with the length the frontend adds.
	expectDocument := func(rec *httptest.ResponseRecorder, doc string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), "to_mime_type, rgw_rest_s3.cc:3896 at v19.2.7")
		want := `<?xml version="1.0" encoding="UTF-8"?>` + doc
		Expect(rec.Body.String()).To(Equal(want), "dump_start, then encode_xml, rgw_rest_s3.cc:3897-3903 at v19.2.7")
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "the length the frontend adds, not dump_content_length's")
	}
	// errorDocument is the document end_header's error branch sends for rec.
	errorDocument := func(rec *httptest.ResponseRecorder, code, message string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>` + message + `</Message>` +
			`<BucketName>plain</BucketName><RequestId>` + exactHeader(rec.Header(), "x-amz-request-id") +
			`</RequestId><HostId>4155-z-zg</HostId></Error>`
	}
	expectUnwritten := func(ctx context.Context, before meta.ObjVersion) {
		GinkgoHelper()
		Expect(w.bucket(ctx).Version).To(Equal(before), "nothing written")
	}

	It("binds the three routes in every handler", func() {
		h := newHandler(w.store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements("bucket/get_bucket_encryption",
			"bucket/put_bucket_encryption", "bucket/delete_bucket_encryption"))
	})

	It("puts AES256 with 200 and no body, gets it in S3's namespace, and deletes it with 204", func(ctx SpecContext) {
		expectEmpty(w.send(w.alice, http.MethodPut, target, aesEncryptionDoc), http.StatusOK)
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encodeEncryption(aesEncryption())))

		expectDocument(w.send(w.alice, http.MethodGet, target, ""), `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault></Rule>`+
			`</ServerSideEncryptionConfiguration>`)

		expectEmpty(w.send(w.alice, http.MethodDelete, target, ""), http.StatusNoContent)
		Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrBucketEncryption))
		rec := w.send(w.alice, http.MethodGet, target, "")
		Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
		Expect(rec.Body.String()).To(Equal(errorDocument(rec, "ServerSideEncryptionConfigurationNotFoundError", notFound)))
	})

	It("gets aws:kms with its key id, and a bucket key that is on as true, which botocore reads as on", func(ctx SpecContext) {
		expectEmpty(w.send(w.alice, http.MethodPut, target, kmsEncryptionDoc), http.StatusOK)
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encodeEncryption(kmsEncryption())))
		expectDocument(w.send(w.alice, http.MethodGet, target, ""), `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>aws:kms</SSEAlgorithm><KMSMasterKeyID>fool-me-again</KMSMasterKeyID>`+
			`</ApplyServerSideEncryptionByDefault><BucketKeyEnabled>true</BucketKeyEnabled></Rule></ServerSideEncryptionConfiguration>`)
	})

	It("replaces a stored configuration whole", func(ctx SpecContext) {
		w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: encodeEncryption(kmsEncryption())})
		expectEmpty(w.send(w.alice, http.MethodPut, target, aesEncryptionDoc), http.StatusOK)
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encodeEncryption(aesEncryption())))
	})

	It("deletes a configuration with the SSE-S3 key id beside it, and answers a DELETE of none 204", func(ctx SpecContext) {
		w.setBucketAttrs(ctx, map[string][]byte{
			op.AttrBucketEncryption: encodeEncryption(aesEncryption()), op.AttrBucketEncryptionKeyID: []byte("key"),
		})
		expectEmpty(w.send(w.alice, http.MethodDelete, target, ""), http.StatusNoContent)
		Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrBucketEncryption))
		Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrBucketEncryptionKeyID))
		expectEmpty(w.send(w.alice, http.MethodDelete, target, ""), http.StatusNoContent)
	})

	DescribeTable("answers a GET the op refuses with the error document alone, its bytes ending at its Content-Length",
		func(ctx SpecContext, stored []byte, status int, code, message string) {
			buf := captureLog()
			if stored != nil {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: stored})
			}
			rec := w.send(w.alice, http.MethodGet, target, "")
			Expect(rec.Code).To(Equal(status), rec.Body.String())
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
			Expect(rec.Body.String()).To(Equal(errorDocument(rec, code, message)),
				"send_response encodes the configuration only when op_ret is 0, rgw_rest_s3.cc:3899-3903 at v19.2.7")
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
			if status < http.StatusInternalServerError {
				Expect(logRecords(buf)).To(BeEmpty(), "WriteError logs no 4xx")
				return
			}
			Expect(logRecords(buf)).To(ConsistOf(SatisfyAll(
				HaveKeyWithValue("level", "ERROR"),
				HaveKeyWithValue("code", code),
				HaveKeyWithValue("request_id", exactHeader(rec.Header(), "x-amz-request-id")),
			)), "WriteError logs an UnknownError once")
		},
		Entry("a bucket without a configuration: 404 ServerSideEncryptionConfigurationNotFoundError",
			nil, http.StatusNotFound, "ServerSideEncryptionConfigurationNotFoundError", notFound),
		Entry("a configuration that does not decode: radosgw's -EIO, 500 UnknownError",
			[]byte{1}, http.StatusInternalServerError, "UnknownError", ""),
	)

	Describe("PUT", func() {
		DescribeTable("refuses a document S3 does not take with 400 MalformedXML, keeping the stored configuration",
			func(ctx SpecContext, body string) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: encodeEncryption(kmsEncryption())})
				before := w.bucket(ctx).Version
				rec := w.send(w.alice, http.MethodPut, target, body, "Content-Length", strconv.Itoa(len(body)))
				Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
				Expect(rec.Body.String()).To(Equal(errorDocument(rec, "MalformedXML", "")))
				expectUnwritten(ctx, before)
				Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encodeEncryption(kmsEncryption())))
			},
			Entry("an SSEAlgorithm of AES-256, which radosgw stores",
				strings.Replace(aesEncryptionDoc, "AES256", "AES-256", 1)),
			Entry("a misspelled SseAlgorithm, which radosgw drops, storing a rule with no algorithm",
				strings.ReplaceAll(aesEncryptionDoc, "SSEAlgorithm", "SseAlgorithm")),
			Entry("a KMSMasterKeyID beside AES256, which radosgw stores",
				strings.Replace(aesEncryptionDoc, "</SSEAlgorithm>", "</SSEAlgorithm><KMSMasterKeyID>k</KMSMasterKeyID>", 1)),
			Entry("an empty body, which expat refuses", ``),
		)
		It("refuses a requester the bucket does not admit with 403 AccessDenied, before it reads the body", func(ctx SpecContext) {
			before := w.bucket(ctx).Version
			body := &countingBody{r: strings.NewReader(aesEncryptionDoc)}
			rec := serveReq(s3.NewHandler(w.env, authAs(w.bob), testConfig(s3.Config{})), http.MethodPut, target, body)
			Expect(rec.Code).To(Equal(http.StatusForbidden), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
			Expect(body.n).To(BeZero(), "execute calls get_params after verify_permission, rgw_op.cc:8780 at v19.2.7")
			expectUnwritten(ctx, before)
		})
		It("answers a missing bucket 404 NoSuchBucket before it reads the body", func() {
			body := &countingBody{r: strings.NewReader(aesEncryptionDoc)}
			rec := serveReq(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})), http.MethodPut,
				"/missing?encryption", body)
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
			Expect(body.n).To(BeZero())
		})
		It("refuses a body past rgw_max_put_param_size as 416 InvalidRange, unread", func(ctx SpecContext) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			before := w.bucket(ctx).Version
			body := &countingBody{r: strings.NewReader(aesEncryptionDoc)}
			req := httptest.NewRequestWithContext(ctx, http.MethodPut, target, body)
			req.ContentLength = int64(len(aesEncryptionDoc))
			req.Header.Set("Content-Length", strconv.Itoa(len(aesEncryptionDoc)))
			rec := httptest.NewRecorder()
			s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusRequestedRangeNotSatisfiable), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRange</Code>"))
			Expect(body.n).To(BeZero(), "read_all_input refuses the declared length before it reads, "+
				"rgw_rest.cc:1556-1557 at v19.2.7 and v20.2.4")
			expectUnwritten(ctx, before)
		})
		It("refuses a request without a Content-Length with 411 MissingContentLength", func() {
			rec := w.send(w.alice, http.MethodPut, target, "")
			Expect(rec.Code).To(Equal(http.StatusLengthRequired), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>MissingContentLength</Code>"))
		})
	})

	It("refuses a GET and a DELETE by a requester the bucket does not admit with 403 AccessDenied", func(ctx SpecContext) {
		w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: encodeEncryption(aesEncryption())})
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			rec := w.send(w.bob, method, target, "")
			Expect(rec.Code).To(Equal(http.StatusForbidden), "%s: %s", method, rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"), method)
		}
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encodeEncryption(aesEncryption())))
	})

	It("serves the PUT and the DELETE to a requester the bucket policy grants s3:PutBucketEncryption alone, and refuses the GET", func(ctx SpecContext) {
		const grant = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/bob"]},` +
			`"Action":"s3:PutBucketEncryption","Resource":"arn:aws:s3:::plain"}]}`
		Expect(w.send(w.alice, http.MethodPut, "/plain?policy", grant).Code).To(Equal(http.StatusNoContent))
		expectEmpty(w.send(w.bob, http.MethodPut, target, aesEncryptionDoc), http.StatusOK)
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encodeEncryption(aesEncryption())))
		rec := w.send(w.bob, http.MethodGet, target, "")
		Expect(rec.Code).To(Equal(http.StatusForbidden), "GET is s3:GetBucketEncryption: %s", rec.Body.String())
		expectEmpty(w.send(w.bob, http.MethodDelete, target, ""), http.StatusNoContent)
		Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrBucketEncryption))
	})
})
