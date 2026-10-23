package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// retentionBody is a PutObjectRetention document in S3's namespace.
func retentionBody(mode, until string) string {
	return `<Retention xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Mode>` + mode +
		`</Mode><RetainUntilDate>` + until + `</RetainUntilDate></Retention>`
}

// legalHoldBody is a PutObjectLegalHold document in S3's namespace.
func legalHoldBody(status string) string {
	return `<LegalHold xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>` + status + `</Status></LegalHold>`
}

// The retentions the specs put: one to 2030-01-03 and one that shortens it,
// to 2030-01-01.
var (
	retentionInForce = retentionBody("GOVERNANCE", "2030-01-03T00:00:00Z")
	retentionShorter = retentionBody("GOVERNANCE", "2030-01-01T00:00:00Z")
)

var _ = Describe("object retention and legal hold routes", func() {
	const xmlDecl = `<?xml version="1.0" encoding="UTF-8"?>`
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) {
		w = newSubresWorld(ctx, denc.Squid)
		lockBucket(ctx, w)
	})

	// expectEmpty checks rec is 200 with no body and no type, as end_header(s)
	// sends one, with the Content-Length: 0 the frontend completes it with.
	expectEmpty := func(rec *httptest.ResponseRecorder) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Body.String()).To(BeEmpty())
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
	}
	// expectDocument checks rec is 200 with doc after the XML declaration
	// dump_start writes, as application/xml, with its length.
	expectDocument := func(rec *httptest.ResponseRecorder, doc string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(xmlDecl + doc))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(xmlDecl + doc))))
	}
	expectError := func(rec *httptest.ResponseRecorder, status int, code, message string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(xmlDecl + `<Error><Code>` + code + `</Code><Message>` + message + `</Message>` +
			`<BucketName>plain</BucketName><RequestId>` + exactHeader(rec.Header(), "x-amz-request-id") +
			`</RequestId><HostId>4155-z-zg</HostId></Error>`))
	}
	attrs := func(ctx context.Context, key meta.ObjKey) map[string][]byte {
		GinkgoHelper()
		st, err := w.store.StatObject(ctx, w.bucket(ctx), key)
		Expect(err).NotTo(HaveOccurred())
		return st.Attrs
	}

	It("binds the four routes in every handler", func() {
		h := newHandler(w.store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements("object/put_obj_retention", "object/get_obj_retention",
			"object/put_obj_legal_hold", "object/get_obj_legal_hold"))
	})

	It("puts a retention with 200 and no body, and gets it back without a namespace, its date in nine digits", func() {
		expectEmpty(w.send(w.alice, http.MethodPut, "/plain/k?retention", retentionBody("COMPLIANCE", "2030-01-01T00:00:00.5Z")))
		expectDocument(w.send(w.alice, http.MethodGet, "/plain/k?retention", ""),
			`<Retention><Mode>COMPLIANCE</Mode><RetainUntilDate>2030-01-01T00:00:00.500000000Z</RetainUntilDate></Retention>`)
	})
	It("puts a legal hold with 200 and no body, and gets it back without a namespace", func() {
		expectEmpty(w.send(w.alice, http.MethodPut, "/plain/k?legal-hold", legalHoldBody("ON")))
		expectDocument(w.send(w.alice, http.MethodGet, "/plain/k?legal-hold", ""), `<LegalHold><Status>ON</Status></LegalHold>`)
	})
	It("puts and gets the noncurrent version versionId names, leaving the current alone", func(ctx SpecContext) {
		expectEmpty(w.send(w.alice, http.MethodPut, "/plain/k?retention&versionId=v1", retentionInForce))
		expectEmpty(w.send(w.alice, http.MethodPut, "/plain/k?legal-hold&versionId=v1", legalHoldBody("ON")))
		Expect(attrs(ctx, meta.ObjKey{Name: "k"})).NotTo(Or(HaveKey(meta.AttrObjectRetention), HaveKey(meta.AttrObjectLegalHold)))
		expectDocument(w.send(w.alice, http.MethodGet, "/plain/k?retention&versionId=v1", ""),
			`<Retention><Mode>GOVERNANCE</Mode><RetainUntilDate>2030-01-03T00:00:00.000000000Z</RetainUntilDate></Retention>`)
		expectDocument(w.send(w.alice, http.MethodGet, "/plain/k?legal-hold&versionId=v1", ""), `<LegalHold><Status>ON</Status></LegalHold>`)
		expectError(w.send(w.alice, http.MethodGet, "/plain/k?retention", ""), http.StatusNotFound, "ObjectLockConfigurationNotFoundError", "")
	})

	DescribeTable("reads x-amz-bypass-governance-retention url-decoded and \"true\" in any case",
		func(ctx SpecContext, value string, bypassed bool) {
			expectEmpty(w.send(w.alice, http.MethodPut, "/plain/k?retention", retentionInForce))
			rec := w.send(w.alice, http.MethodPut, "/plain/k?retention", retentionShorter, "X-Amz-Bypass-Governance-Retention", value)
			if bypassed {
				expectEmpty(rec)
				return
			}
			expectError(rec, http.StatusForbidden, "AccessDenied",
				"proposed retain-until date shortens an existing retention period and governance bypass check failed")
		},
		Entry("true", "true", true),
		Entry("TRUE", "TRUE", true),
		Entry("%74rue", "%74rue", true),
		Entry("yes", "yes", false),
	)
	It("refuses the bypass to an owner a bucket policy denies s3:BypassGovernanceRetention on the object", func(ctx SpecContext) {
		expectEmpty(w.send(w.alice, http.MethodPut, "/plain/k?retention", retentionInForce))
		const deny = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/alice"]},` +
			`"Action":"s3:BypassGovernanceRetention","Resource":"arn:aws:s3:::plain/*"}]}`
		Expect(w.send(w.alice, http.MethodPut, "/plain?policy", deny).Code).To(Equal(http.StatusNoContent))
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?retention", retentionShorter, "X-Amz-Bypass-Governance-Retention", "true"),
			http.StatusForbidden, "AccessDenied",
			"proposed retain-until date shortens an existing retention period and governance bypass check failed")
	})

	DescribeTable("answers a bucket without object lock 400 InvalidRequest with radosgw's message, its apostrophe escaped as dump_string escapes it",
		func(ctx SpecContext, method, target, body, message string) {
			rec := w.bucket(ctx)
			rec.Info.Flags = meta.BucketVersioned
			Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
			expectError(w.send(w.alice, method, target, body), http.StatusBadRequest, "InvalidRequest", message)
		},
		Entry("PUT ?retention", http.MethodPut, "/plain/k?retention", retentionInForce,
			"object retention can&apos;t be set if bucket object lock not configured"),
		Entry("GET ?retention", http.MethodGet, "/plain/k?retention", "", "bucket object lock not configured"),
		Entry("PUT ?legal-hold", http.MethodPut, "/plain/k?legal-hold", legalHoldBody("ON"),
			"object legal hold can&apos;t be set if bucket object lock not enabled"),
		Entry("GET ?legal-hold", http.MethodGet, "/plain/k?legal-hold", "", "bucket object lock not configured"),
	)
	It("answers a retain-until date in the past 400 InvalidArgument with radosgw's message", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?retention", retentionBody("GOVERNANCE", "2020-01-01T00:00:00Z")),
			http.StatusBadRequest, "InvalidArgument", "the retain-until date must be in the future")
	})
	It("answers a retain-until date past 2554-07-21T23:34:33.709551615Z 400 MalformedXML", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?retention", retentionBody("GOVERNANCE", "2554-07-21T23:34:34Z")),
			http.StatusBadRequest, "MalformedXML", "")
	})
	DescribeTable("answers a retention attr that does not decode 500 UnknownError, and logs it",
		func(ctx SpecContext, method, body string) {
			e := denc.NewEncoder()
			meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}.Encode(e, denc.Squid)
			short := e.Bytes()[:len(e.Bytes())-1]
			setObjectAttr(ctx, w, meta.ObjKey{Name: "k"}, meta.AttrObjectRetention, short)
			buf := captureLog()
			expectError(w.send(w.alice, method, "/plain/k?retention", body), http.StatusInternalServerError, "UnknownError", "")
			Expect(logRecords(buf)).To(ConsistOf(HaveKeyWithValue("code", "UnknownError")))
		},
		Entry("PUT", http.MethodPut, retentionShorter),
		Entry("GET", http.MethodGet, ""),
	)

	DescribeTable("answers a missing version 404 NoSuchKey",
		func(method, target, body string) {
			rec := w.send(w.alice, method, target, body)
			expectError(rec, http.StatusNotFound, "NoSuchKey", "")
			Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"), "Squid adds none")
		},
		Entry("PUT ?retention", http.MethodPut, "/plain/k?retention&versionId=nope", retentionInForce),
		Entry("GET ?retention", http.MethodGet, "/plain/k?retention&versionId=nope", ""),
		Entry("PUT ?legal-hold", http.MethodPut, "/plain/nope?legal-hold", legalHoldBody("ON")),
		Entry("GET ?legal-hold", http.MethodGet, "/plain/nope?legal-hold", ""),
	)
	It("sends Tentacle's x-amz-delete-marker: false with a GET's 404, as read_permissions does", func(ctx SpecContext) {
		w = newSubresWorld(ctx, denc.Tentacle)
		lockBucket(ctx, w)
		rec := w.send(w.alice, http.MethodGet, "/plain/nope?retention", "")
		expectError(rec, http.StatusNotFound, "NoSuchKey", "")
		Expect(rec.Header()).To(HaveKeyWithValue("x-amz-delete-marker", []string{"false"}), "radosgw's spelling, once")
		rec = w.send(w.alice, http.MethodPut, "/plain/nope?retention", retentionInForce)
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"), "a PUT gets none")
	})

	DescribeTable("refuses a body past rgw_max_put_param_size as 416 InvalidRange, unread",
		func(ctx SpecContext, target, body string) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			counted := &countingBody{r: strings.NewReader(body)}
			req := httptest.NewRequestWithContext(ctx, http.MethodPut, target, counted)
			req.ContentLength = int64(len(body))
			req.Header.Set("Content-Length", strconv.Itoa(len(body)))
			rec := httptest.NewRecorder()
			s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})).ServeHTTP(rec, req)
			expectError(rec, http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "")
			Expect(counted.n).To(BeZero(), "read_all_input refuses the declared length before it reads")
			Expect(attrs(ctx, meta.ObjKey{Name: "k"})).NotTo(Or(HaveKey(meta.AttrObjectRetention), HaveKey(meta.AttrObjectLegalHold)))
		},
		Entry("PUT ?retention", "/plain/k?retention", retentionInForce),
		Entry("PUT ?legal-hold", "/plain/k?legal-hold", legalHoldBody("ON")),
	)
	DescribeTable("refuses a body whose payload hash does not match with 400 XAmzContentSHA256Mismatch, writing nothing",
		func(ctx SpecContext, target, body string) {
			rec := w.sendTampered(target, body)
			Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>XAmzContentSHA256Mismatch</Code>"))
			Expect(attrs(ctx, meta.ObjKey{Name: "k"})).NotTo(Or(HaveKey(meta.AttrObjectRetention), HaveKey(meta.AttrObjectLegalHold)))
		},
		Entry("PUT ?retention", "/plain/k?retention", retentionInForce),
		Entry("PUT ?legal-hold", "/plain/k?legal-hold", legalHoldBody("ON")),
	)
	It("refuses bob, whom nothing grants the actions, 403 AccessDenied before it reads the body", func(ctx SpecContext) {
		for _, target := range []string{"/plain/k?retention", "/plain/k?legal-hold"} {
			counted := &countingBody{r: strings.NewReader(retentionInForce)}
			rec := serveReq(s3.NewHandler(w.env, authAs(w.bob), testConfig(s3.Config{})), http.MethodPut, target, counted)
			Expect(rec.Code).To(Equal(http.StatusForbidden), target)
			Expect(counted.n).To(BeZero(), target)
		}
		Expect(attrs(ctx, meta.ObjKey{Name: "k"})).NotTo(Or(HaveKey(meta.AttrObjectRetention), HaveKey(meta.AttrObjectLegalHold)))
	})
	It("serves the same on Tentacle", func(ctx SpecContext) {
		w = newSubresWorld(ctx, denc.Tentacle)
		lockBucket(ctx, w)
		expectEmpty(w.send(w.alice, http.MethodPut, "/plain/k?retention", retentionInForce))
		expectDocument(w.send(w.alice, http.MethodGet, "/plain/k?retention", ""),
			`<Retention><Mode>GOVERNANCE</Mode><RetainUntilDate>2030-01-03T00:00:00.000000000Z</RetainUntilDate></Retention>`)
	})
})

// lockBucket gives w's bucket plain versioning and object lock, a current
// version of k and a noncurrent one, v1, each under alice's ACL.
func lockBucket(ctx context.Context, w *subresWorld) {
	GinkgoHelper()
	rec := w.bucket(ctx)
	rec.Info.Flags = meta.BucketVersioned | meta.BucketObjLockEnabled
	Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
	aliceACL := encodeACL(acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice"))
	for _, key := range []meta.ObjKey{{Name: "k"}, {Name: "k", Instance: "v1"}} {
		_, err := w.store.PutObject(ctx, w.bucket(ctx), key, strings.NewReader("x"),
			op.PutParams{Size: 1, Attrs: map[string][]byte{meta.AttrACL: aliceACL}})
		Expect(err).NotTo(HaveOccurred())
	}
}

// setObjectAttr sets the attr name of key in w's bucket plain to v.
func setObjectAttr(ctx context.Context, w *subresWorld, key meta.ObjKey, name string, v []byte) {
	GinkgoHelper()
	st, err := w.store.StatObject(ctx, w.bucket(ctx), key)
	Expect(err).NotTo(HaveOccurred())
	Expect(w.store.SetObjectAttrs(ctx, st, map[string][]byte{name: v}, nil)).To(Succeed())
}
