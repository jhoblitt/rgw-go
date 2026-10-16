package s3_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

var _ = Describe("bucket versioning routes", func() {
	const (
		enable  = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`
		suspend = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Suspended</Status></VersioningConfiguration>`
	)
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

	setFlags := func(ctx context.Context, flags uint32) {
		GinkgoHelper()
		rec := w.bucket(ctx)
		rec.Info.Flags = flags
		Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	// expectRefused checks that rec answered status with code, and that
	// plain's instance is still at before.
	expectRefused := func(ctx context.Context, rec *httptest.ResponseRecorder, before meta.ObjVersion, status int, code string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<Code>" + code + "</Code>"))
		Expect(w.bucket(ctx).Version).To(Equal(before), "nothing written")
	}

	It("binds both routes in every handler", func() {
		h := newHandler(w.store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements("bucket/get_bucket_versioning", "bucket/set_bucket_versioning"))
	})

	DescribeTable("GET renders VersioningConfiguration in S3's namespace, with Status and MfaDelete once the bucket was versioned",
		func(ctx SpecContext, flags uint32, inner string) {
			setFlags(ctx, flags)
			rec := w.send(w.alice, http.MethodGet, "/plain?versioning", "")
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), "to_mime_type, rgw_rest_s3.cc:2165 at v19.2.7")
			want := `<?xml version="1.0" encoding="UTF-8"?><VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
				inner + `</VersioningConfiguration>`
			Expect(rec.Body.String()).To(Equal(want), "rgw_rest_s3.cc:2160-2177 at v19.2.7")
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
		},
		Entry("never versioned: the section closed with an end tag", uint32(0), ``),
		Entry("enabled", uint32(meta.BucketVersioned), `<Status>Enabled</Status><MfaDelete>Disabled</MfaDelete>`),
		Entry("suspended with MFA delete", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended|meta.BucketMFAEnabled),
			`<Status>Suspended</Status><MfaDelete>Enabled</MfaDelete>`),
	)
	It("refuses a GET to a requester the bucket's ACL refuses", func() {
		Expect(w.send(w.bob, http.MethodGet, "/plain?versioning", "").Code).To(Equal(http.StatusForbidden))
	})

	Describe("PUT", func() {
		It("answers 200 with no body under radosgw's type, and sets the flags", func(ctx SpecContext) {
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?versioning", suspend), http.StatusOK)
			Expect(w.bucket(ctx).Info.Flags).To(Equal(uint32(meta.BucketVersioned | meta.BucketVersionsSuspended)))
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?versioning", enable), http.StatusOK)
			Expect(w.bucket(ctx).Info.Flags).To(Equal(uint32(meta.BucketVersioned)))
			rec := w.send(w.alice, http.MethodGet, "/plain?versioning", "")
			Expect(rec.Body.String()).To(ContainSubstring(`<Status>Enabled</Status><MfaDelete>Disabled</MfaDelete>`))
		})
		DescribeTable("answers a document it refuses 400 MalformedXML, where radosgw answers InvalidArgument, and writes nothing",
			func(ctx SpecContext, body string) {
				before := w.bucket(ctx).Version
				expectRefused(ctx, w.send(w.alice, http.MethodPut, "/plain?versioning", body, "Content-Length", strconv.Itoa(len(body))),
					before, http.StatusBadRequest, "MalformedXML")
			},
			Entry("an empty body", ``),
			Entry("one that does not parse", `<VersioningConfiguration>`),
			Entry("one without VersioningConfiguration", `<Versioning><Status>Enabled</Status></Versioning>`),
			Entry("an invalid Status", strings.Replace(enable, "Enabled", "enabled", 1)),
			Entry("an invalid MfaDelete", strings.Replace(enable, "</Status>", "</Status><MfaDelete>On</MfaDelete>", 1)),
			Entry("a DOCTYPE", `<!DOCTYPE VersioningConfiguration>`+enable),
		)
		It("refuses a requester the bucket's ACL refuses before it reads the body", func(ctx SpecContext) {
			before := w.bucket(ctx).Version
			expectRefused(ctx, w.send(w.bob, http.MethodPut, "/plain?versioning", "not a document"), before, http.StatusForbidden, "AccessDenied")
		})
		It("answers a missing bucket 404 NoSuchBucket before it reads the body", func() {
			rec := w.send(w.alice, http.MethodPut, "/missing?versioning", "not a document")
			Expect(rec.Code).To(Equal(http.StatusNotFound))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
		})
		It("refuses a body past rgw_max_put_param_size as 416 InvalidRange, as the other param bodies are", func(ctx SpecContext) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			before := w.bucket(ctx).Version
			expectRefused(ctx, w.send(w.alice, http.MethodPut, "/plain?versioning", enable), before, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
		})
		It("refuses a chunked body past rgw_max_put_param_size as 416 InvalidRange", func(ctx SpecContext) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			before := w.bucket(ctx).Version
			body := enable + strings.Repeat(" ", 4096-len(enable))
			rec := serveReq(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})), http.MethodPut, "/plain?versioning",
				io.MultiReader(strings.NewReader(body)))
			expectRefused(ctx, rec, before, http.StatusRequestedRangeNotSatisfiable, "InvalidRange")
		})
		It("refuses a request without a Content-Length with MissingContentLength", func() {
			Expect(w.send(w.alice, http.MethodPut, "/plain?versioning", "").Code).To(Equal(http.StatusLengthRequired))
		})
		It("refuses a body whose payload hash does not match, writing nothing", func(ctx SpecContext) {
			before := w.bucket(ctx).Version
			expectRefused(ctx, w.sendTampered("/plain?versioning", enable), before, http.StatusBadRequest, "XAmzContentSHA256Mismatch")
		})
		It("refuses an MFA delete change, which no x-amz-mfa verifies, with 403 AccessDenied", func(ctx SpecContext) {
			before := w.bucket(ctx).Version
			body := strings.Replace(enable, "</Status>", "</Status><MfaDelete>Enabled</MfaDelete>", 1)
			expectRefused(ctx, w.send(w.alice, http.MethodPut, "/plain?versioning", body, "x-amz-mfa", "serial 123456"),
				before, http.StatusForbidden, "AccessDenied")
		})
		It("refuses to suspend an object-lock bucket with 409 InvalidBucketState and radosgw's message", func(ctx SpecContext) {
			setFlags(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled)
			before := w.bucket(ctx).Version
			rec := w.send(w.alice, http.MethodPut, "/plain?versioning", suspend)
			expectRefused(ctx, rec, before, http.StatusConflict, "InvalidBucketState")
			Expect(rec.Body.String()).To(ContainSubstring("<Message>bucket versioning cannot be disabled on buckets with object lock enabled</Message>"))
		})
		It("refuses a body nested deeper than the schema in a bounded number of allocations", func(ctx SpecContext) {
			body := strings.Replace(enable, "</Status>", "</Status>"+strings.Repeat("<a>", 1<<17)+strings.Repeat("</a>", 1<<17), 1)
			before := w.bucket(ctx).Version
			var rec *httptest.ResponseRecorder
			allocs := testing.AllocsPerRun(1, func() { rec = w.send(w.alice, http.MethodPut, "/plain?versioning", body) })
			expectRefused(ctx, rec, before, http.StatusBadRequest, "MalformedXML")
			Expect(allocs).To(BeNumerically("<", 5000), "a tree of the document takes an element a level")
		})
	})
})
