package s3_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// expectNoContent checks rec is a 204 with no body and no type, as
// end_header sends a DeleteObject's (rgw_rest_s3.cc:3455-3470).
func expectNoContent(rec *httptest.ResponseRecorder) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(204), rec.Body.String())
	Expect(rec.Body.Len()).To(BeZero())
	Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
	Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
}

var _ = Describe("delete_obj", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })

	It("answers 204 with no body for an existing key, and removes it", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodDelete, "/plain/src", "")
		expectNoContent(rec)
		Expect(rec.Header()).NotTo(HaveKey("X-Amz-Version-Id"))
		Expect(rec.Header()).NotTo(HaveKey("X-Amz-Delete-Marker"))
		Expect(w.stat(ctx, "src").Exists).To(BeFalse())
	})
	It("answers 204 for a missing key, as send_response turns -ENOENT into success", func() {
		expectNoContent(w.send(w.alice, http.MethodDelete, "/plain/missing", ""))
	})
	It("answers a missing bucket 404 and a requester the bucket refuses 403", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodDelete, "/nope/k", ""), 404, "NoSuchBucket")
		expectError(w.send(w.bob, http.MethodDelete, "/plain/src", ""), 403, "AccessDenied")
		Expect(w.stat(ctx, "src").Exists).To(BeTrue())
	})
	It("marks a requester-pays delete by a non-owner", func(ctx SpecContext) {
		w.grantBob(ctx, acl.PermWrite)
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.RequesterPays = true })
		rec := w.send(w.bob, http.MethodDelete, "/plain/src", "", "X-Amz-Request-Payer", "requester")
		expectNoContent(rec)
		Expect(rec.Header().Get("X-Amz-Request-Charged")).To(Equal("requester"))
	})
	DescribeTable("refuses a condition that does not parse with InvalidArgument and keeps the object",
		func(ctx SpecContext, hdr ...string) {
			expectError(w.send(w.alice, http.MethodDelete, "/plain/src", "", hdr...), 400, "InvalidArgument")
			Expect(w.stat(ctx, "src").Exists).To(BeTrue())
		},
		Entry("x-amz-delete-if-unmodified-since that parse_date refuses", "X-Amz-Delete-If-Unmodified-Since", "garbage"),
		Entry("x-amz-delete-if-unmodified-since sent empty", "X-Amz-Delete-If-Unmodified-Since", ""),
		Entry("x-amz-if-match-size that is no number", "X-Amz-If-Match-Size", "1x"),
		Entry("x-amz-if-match-last-modified-time that is no date", "X-Amz-If-Match-Last-Modified-Time", "yesterday"),
	)
	It("url-decodes the date conditions as get_params does", func(ctx SpecContext) {
		expectNoContent(w.send(w.alice, http.MethodDelete, "/plain/src", "", "X-Amz-Delete-If-Unmodified-Since", "2026-09-28T12%3A00%3A00Z"))
		Expect(w.stat(ctx, "src").Exists).To(BeFalse())
	})
	It("keeps an object changed after x-amz-delete-if-unmodified-since, with 412", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodDelete, "/plain/src", "", "X-Amz-Delete-If-Unmodified-Since", "2026-09-28T11:00:00Z"),
			412, "PreconditionFailed")
		Expect(w.stat(ctx, "src").Exists).To(BeTrue())
	})
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, rel) })
			DescribeTable("checks Tentacle's match conditions, which v19.2.6 lacks",
				func(ctx SpecContext, deleted bool, hdr ...string) {
					rec := w.send(w.alice, http.MethodDelete, "/plain/src", "", hdr...)
					if deleted {
						expectNoContent(rec)
					} else {
						expectError(rec, 412, "PreconditionFailed")
					}
					Expect(w.stat(ctx, "src").Exists).To(Equal(!deleted))
				},
				Entry("If-Match naming another ETag", false, "If-Match", `"wrong"`),
				Entry("If-Match sent empty, a condition no ETag meets", false, "If-Match", ""),
				Entry("If-Match naming the ETag", true, "If-Match", `"`+md5Hex("hello")+`"`),
				Entry("x-amz-if-match-size of another size", false, "X-Amz-If-Match-Size", "4"),
				Entry("x-amz-if-match-size of the size", true, "X-Amz-If-Match-Size", "5"),
				Entry("x-amz-if-match-last-modified-time of another time", false, "X-Amz-If-Match-Last-Modified-Time", "Mon, 28 Sep 2026 11:00:00 GMT"),
				Entry("x-amz-if-match-last-modified-time of the mtime", true, "X-Amz-If-Match-Last-Modified-Time", "Mon, 28 Sep 2026 12:00:00 GMT"),
			)
		})
	}
})
