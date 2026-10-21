package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// lockConfigDoc is a PutObjectLockConfiguration body in S3's namespace: a
// GOVERNANCE rule of one day.
const lockConfigDoc = `<ObjectLockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
	`<ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days>` +
	`</DefaultRetention></Rule></ObjectLockConfiguration>`

var _ = Describe("bucket object-lock configuration routes", func() {
	const target = "/plain?object-lock"
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

	setFlags := func(ctx context.Context, flags uint32) {
		GinkgoHelper()
		rec := w.bucket(ctx)
		rec.Info.Flags = flags
		Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	// expectEmpty checks rec is 200 with no body and no type, as end_header(s)
	// sends one (RGWPutBucketObjectLock_ObjStore_S3::send_response,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4495-L4502 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5006-L5013 at v20.2.4),
	// with the Content-Length: 0 the frontend completes it with.
	expectEmpty := func(rec *httptest.ResponseRecorder) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Body.String()).To(BeEmpty())
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
	}
	// expectDocument checks rec is 200 with doc after the XML declaration, as
	// application/xml, with the length the frontend adds
	// (RGWGetBucketObjectLock_ObjStore_S3::send_response,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4504-L4518 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5015-L5029 at v20.2.4).
	expectDocument := func(rec *httptest.ResponseRecorder, doc string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		want := `<?xml version="1.0" encoding="UTF-8"?>` + doc
		Expect(rec.Body.String()).To(Equal(want))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
	}
	errorDocument := func(rec *httptest.ResponseRecorder, code, message string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>` + message + `</Message>` +
			`<BucketName>plain</BucketName><RequestId>` + exactHeader(rec.Header(), "x-amz-request-id") +
			`</RequestId><HostId>4155-z-zg</HostId></Error>`
	}
	expectError := func(rec *httptest.ResponseRecorder, status int, code, message string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(errorDocument(rec, code, message)))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
	}

	It("binds both routes in every handler", func() {
		h := newHandler(w.store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements("bucket/get_bucket_object_lock", "bucket/put_bucket_object_lock"))
	})

	It("puts a rule with 200 and no body, and gets it back without a namespace", func(ctx SpecContext) {
		setFlags(ctx, meta.BucketVersioned)
		expectEmpty(w.send(w.alice, http.MethodPut, target, lockConfigDoc))
		Expect(w.bucket(ctx).Info.ObjLockEnabled()).To(BeTrue())
		expectDocument(w.send(w.alice, http.MethodGet, target, ""), `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled>`+
			`<Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days></DefaultRetention></Rule></ObjectLockConfiguration>`)
	})
	It("gets a lock bucket that stores no rule as ObjectLockEnabled alone", func(ctx SpecContext) {
		setFlags(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled)
		expectDocument(w.send(w.alice, http.MethodGet, target, ""),
			`<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`)
	})
	It("gets a rule of years as Years", func(ctx SpecContext) {
		setFlags(ctx, meta.BucketVersioned)
		doc := strings.Replace(strings.Replace(lockConfigDoc, "<Days>1</Days>", "<Years>2</Years>", 1), "GOVERNANCE", "COMPLIANCE", 1)
		expectEmpty(w.send(w.alice, http.MethodPut, target, doc))
		expectDocument(w.send(w.alice, http.MethodGet, target, ""), `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled>`+
			`<Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Years>2</Years></DefaultRetention></Rule></ObjectLockConfiguration>`)
	})

	DescribeTable("answers a GET the op refuses with the error document alone",
		func(ctx SpecContext, flags uint32, lock []byte, status int, code string) {
			buf := captureLog()
			rec := w.bucket(ctx)
			rec.Info.Flags, rec.Info.ObjLock = flags, lock
			Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
			expectError(w.send(w.alice, http.MethodGet, target, ""), status, code, "")
			if status < http.StatusInternalServerError {
				Expect(logRecords(buf)).To(BeEmpty(), "WriteError logs no 4xx")
				return
			}
			Expect(logRecords(buf)).To(ConsistOf(HaveKeyWithValue("code", code)), "WriteError logs an UnknownError once")
		},
		Entry("a bucket without object lock: 404 ObjectLockConfigurationNotFoundError, with no message",
			uint32(meta.BucketVersioned), nil, http.StatusNotFound, "ObjectLockConfigurationNotFoundError"),
		Entry("a lock that does not decode: 500 UnknownError",
			uint32(meta.BucketVersioned|meta.BucketObjLockEnabled), []byte{1, 1, 1, 0, 0, 0, 1}, http.StatusInternalServerError, "UnknownError"),
	)

	Describe("PUT", func() {
		DescribeTable("refuses a bucket whose versioning is not enabled with 409 InvalidBucketState and radosgw's message",
			func(ctx SpecContext, flags uint32) {
				setFlags(ctx, flags)
				before := w.bucket(ctx).Version
				expectError(w.send(w.alice, http.MethodPut, target, lockConfigDoc), http.StatusConflict, "InvalidBucketState",
					"Object lock cannot be enabled unless the bucket has versioning enabled")
				Expect(w.bucket(ctx).Version).To(Equal(before))
			},
			Entry("never versioned", uint32(0)),
			Entry("suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended)),
		)
		DescribeTable("refuses a rule whose period is no positive one, or runs past 2554, with 400 InvalidRetentionPeriod and radosgw's message",
			func(ctx SpecContext, from, to string) {
				setFlags(ctx, meta.BucketVersioned)
				expectError(w.send(w.alice, http.MethodPut, target, strings.Replace(lockConfigDoc, from, to, 1)), http.StatusBadRequest,
					"InvalidRetentionPeriod", "retention period must be a positive integer value")
				Expect(w.bucket(ctx).Info.ObjLockEnabled()).To(BeFalse())
			},
			Entry("Days of 0", "<Days>1</Days>", "<Days>0</Days>"),
			Entry("Years of -1", "<Days>1</Days>", "<Years>-1</Years>"),
			Entry("Years 600 from now", "<Days>1</Days>", "<Years>600</Years>"),
		)
		DescribeTable("refuses a document that does not parse or decode with 400 MalformedXML and no message",
			func(ctx SpecContext, from, to string) {
				setFlags(ctx, meta.BucketVersioned)
				expectError(w.send(w.alice, http.MethodPut, target, strings.Replace(lockConfigDoc, from, to, 1)), http.StatusBadRequest,
					"MalformedXML", "")
				Expect(w.bucket(ctx).Info.ObjLockEnabled()).To(BeFalse())
			},
			Entry("both Days and Years", "<Days>1</Days>", "<Days>1</Days><Years>1</Years>"),
			Entry("a Mode outside GOVERNANCE and COMPLIANCE", "GOVERNANCE", "abc"),
			Entry("an ObjectLockEnabled other than Enabled", ">Enabled<", ">Disabled<"),
			Entry("a body that does not parse", "</ObjectLockConfiguration>", ""),
		)
		It("refuses a requester the bucket does not admit with 403 AccessDenied, before it reads the body", func(ctx SpecContext) {
			setFlags(ctx, meta.BucketVersioned)
			before := w.bucket(ctx).Version
			body := &countingBody{r: strings.NewReader(lockConfigDoc)}
			rec := serveReq(s3.NewHandler(w.env, authAs(w.bob), testConfig(s3.Config{})), http.MethodPut, target, body)
			Expect(rec.Code).To(Equal(http.StatusForbidden), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
			Expect(body.n).To(BeZero(), "execute calls get_params after verify_permission, rgw_op.cc:8259 at v19.2.7")
			Expect(w.bucket(ctx).Version).To(Equal(before))
		})
		It("refuses a body whose payload hash does not match with 400 XAmzContentSHA256Mismatch, writing nothing", func(ctx SpecContext) {
			setFlags(ctx, meta.BucketVersioned)
			rec := w.sendTampered(target, lockConfigDoc)
			Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>XAmzContentSHA256Mismatch</Code>"))
			Expect(w.bucket(ctx).Info.ObjLockEnabled()).To(BeFalse())
		})
		It("answers a missing bucket 404 NoSuchBucket", func() {
			rec := w.send(w.alice, http.MethodPut, "/missing?object-lock", lockConfigDoc)
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
		})
	})

	It("serves the PUT to a requester the bucket policy grants s3:PutBucketObjectLockConfiguration alone, and refuses the GET", func(ctx SpecContext) {
		setFlags(ctx, meta.BucketVersioned)
		const grant = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/bob"]},` +
			`"Action":"s3:PutBucketObjectLockConfiguration","Resource":"arn:aws:s3:::plain"}]}`
		Expect(w.send(w.alice, http.MethodPut, "/plain?policy", grant).Code).To(Equal(http.StatusNoContent))
		expectEmpty(w.send(w.bob, http.MethodPut, target, lockConfigDoc))
		Expect(w.bucket(ctx).Info.ObjLockEnabled()).To(BeTrue())
		rec := w.send(w.bob, http.MethodGet, target, "")
		Expect(rec.Code).To(Equal(http.StatusForbidden), "GET is s3:GetBucketObjectLockConfiguration: %s", rec.Body.String())
	})
	It("locks a bucket PutBucketVersioning enabled, whose versioning PutBucketVersioning then refuses to suspend with 409", func(ctx SpecContext) {
		const (
			enable  = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`
			suspend = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Suspended</Status></VersioningConfiguration>`
		)
		rec := w.send(w.alice, http.MethodPut, "/plain?versioning", enable)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		expectEmpty(w.send(w.alice, http.MethodPut, target, lockConfigDoc))
		before := w.bucket(ctx)
		Expect(before.Info.Flags).To(Equal(uint32(meta.BucketVersioned | meta.BucketObjLockEnabled)))
		expectError(w.send(w.alice, http.MethodPut, "/plain?versioning", suspend), http.StatusConflict, "InvalidBucketState",
			"bucket versioning cannot be disabled on buckets with object lock enabled")
		after := w.bucket(ctx)
		Expect(after.Info.Flags).To(Equal(before.Info.Flags), "rgw_op.cc:2788-2793 at v19.2.7, :3020-3025 at v20.2.4")
		Expect(after.Version).To(Equal(before.Version), "nothing written")
	})
	It("serves the same on Tentacle", func(ctx SpecContext) {
		w = newSubresWorld(ctx, denc.Tentacle)
		setFlags(ctx, meta.BucketVersioned)
		expectEmpty(w.send(w.alice, http.MethodPut, target, lockConfigDoc))
		expectDocument(w.send(w.alice, http.MethodGet, target, ""), `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled>`+
			`<Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days></DefaultRetention></Rule></ObjectLockConfiguration>`)
	})
})
