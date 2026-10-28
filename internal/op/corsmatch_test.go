package op_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// readersCORS lets every origin GET and https://app.example alone PUT and
// POST.
func readersCORS() *cors.Configuration {
	return &cors.Configuration{Rules: []cors.Rule{
		{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}},
		{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodPut | cors.MethodPost, AllowedOrigins: []string{"https://app.example"}},
	}}
}

// appReadCORS lets https://app.example alone GET.
func appReadCORS() *cors.Configuration {
	return &cors.Configuration{Rules: []cors.Rule{
		{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"https://app.example"}},
	}}
}

// renamed is an op under another name.
type renamed struct {
	*recorder
	name string
}

func (r renamed) Name() string { return r.name }

var _ = Describe("Run's check of a request's Origin against its bucket's CORS configuration", func() {
	var (
		rec *recorder
		req *op.Request
	)
	// req is a PUT of an object from https://other.example, which
	// readersCORS lets GET alone.
	BeforeEach(func() {
		rec = &recorder{mask: op.OpTypeWrite}
		zone := &opfakes.FakeZoneInfo{}
		zone.ZoneReturns(meta.Zone{Name: "rw"})
		req = &op.Request{
			Method: http.MethodPut, Bucket: "b", Object: meta.ObjKey{Name: "k"},
			Header: http.Header{"Origin": {"https://other.example"}}, Identity: op.Anonymous(), Env: &op.Env{Zone: zone},
			BucketRec: &op.BucketRecord{
				Info:  meta.BucketInfo{Bucket: meta.BucketID{Name: "b"}},
				Attrs: map[string][]byte{op.AttrCORS: readersCORS().Encode()},
			},
		}
	})
	// expectRefused asserts that err is the check's refusal, radosgw's
	// -EACCES, an AccessDenied with no message, and that the op ran no
	// further than VerifyPermission.
	expectRefused := func(err error) {
		GinkgoHelper()
		Expect(err).To(MatchError(op.ErrAccessDenied))
		Expect(err).NotTo(BeIdenticalTo(op.ErrAccessDenied), "the op mask's refusal, not the check's")
		Expect(op.AsError(err).Message).To(BeEmpty())
		Expect(rec.calls).To(Equal([]string{"init", "verify"}))
	}
	expectServed := func(err error) {
		GinkgoHelper()
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	}
	setCORS := func(c *cors.Configuration) { req.BucketRec.Attrs[op.AttrCORS] = c.Encode() }

	It("refuses a request whose Origin no rule allows for its method, and serves one a rule allows", func(ctx SpecContext) {
		expectRefused(op.Run(ctx, rec, req))

		rec.calls = nil
		req.Header.Set("Origin", "https://app.example")
		expectServed(op.Run(ctx, rec, req))
	})
	It("takes the request's own method, not its Access-Control-Request-Method", func(ctx SpecContext) {
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		expectRefused(op.Run(ctx, rec, req))

		rec.calls, rec.mask = nil, op.OpTypeRead
		req.Method = http.MethodGet
		req.Header.Set("Access-Control-Request-Method", http.MethodPut)
		expectServed(op.Run(ctx, rec, req))
	})
	It("refuses an admin, whom the permission check would let through", func(ctx SpecContext) {
		req.Identity.Admin = true
		rec.permErr = op.ErrAccessDenied
		expectRefused(op.Run(ctx, rec, req))
	})
	It("refuses ahead of a permission error that is not an access denial", func(ctx SpecContext) {
		rec.permErr = op.ErrNoSuchKey
		expectRefused(op.Run(ctx, rec, req))
	})
	DescribeTable("refuses POST object and a website GET as it refuses any request",
		func(ctx SpecContext, method, key string, website bool, c *cors.Configuration) {
			req.Method, req.Object, req.Website = method, meta.ObjKey{Name: key}, website
			setCORS(c)
			req.Header.Set("Origin", "https://app.example")
			expectServed(op.Run(ctx, rec, req))

			rec.calls = nil
			req.Header.Set("Origin", "https://other.example")
			expectRefused(op.Run(ctx, rec, req))
		},
		Entry("POST object, a POST at bucket scope", http.MethodPost, "", false, readersCORS()),
		Entry("a website GET", http.MethodGet, "index.html", true, appReadCORS()),
	)
	DescribeTable("passes",
		func(ctx SpecContext, change func(*op.Request)) {
			change(req)
			expectServed(op.Run(ctx, rec, req))
		},
		Entry("a request without an Origin", func(r *op.Request) { r.Header.Del("Origin") }),
		Entry("a request whose Origin is empty, as verify_cors_match passes one", func(r *op.Request) { r.Header.Set("Origin", "") }),
		Entry("a request whose bucket has no CORS configuration", func(r *op.Request) { delete(r.BucketRec.Attrs, op.AttrCORS) }),
		Entry("a request without an Origin whose bucket's configuration does not decode", func(r *op.Request) {
			r.Header.Del("Origin")
			r.BucketRec.Attrs[op.AttrCORS] = []byte{0xff}
		}),
		Entry("a service-scope request, which loads no bucket", func(r *op.Request) {
			r.Bucket, r.Object, r.BucketRec = "", meta.ObjKey{}, nil
		}),
	)
	It("reads the last of a repeated Origin, as radosgw's RGWEnv holds it", func(ctx SpecContext) {
		req.Header = http.Header{"Origin": {"https://app.example", "https://other.example"}}
		expectRefused(op.Run(ctx, rec, req))

		rec.calls = nil
		req.Header = http.Header{"Origin": {"https://other.example", "https://app.example"}}
		expectServed(op.Run(ctx, rec, req))
	})
	It("answers a configuration that does not decode with read_bucket_cors's 500, an admin too", func(ctx SpecContext) {
		req.BucketRec.Attrs[op.AttrCORS] = []byte{0xff}
		req.Identity.Admin = true
		err := op.Run(ctx, rec, req)
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(op.AsError(err).Status).To(Equal(http.StatusInternalServerError))
		Expect(rec.calls).To(Equal([]string{"init", "verify"}))
	})
	DescribeTable("answers an earlier refusal first",
		func(ctx SpecContext, change func(*op.Request, *recorder), want types.GomegaMatcher, calls []string) {
			change(req, rec)
			Expect(op.Run(ctx, rec, req)).To(want)
			Expect(rec.calls).To(Equal(calls))
		},
		Entry("Init's, the bucket load's PermanentRedirect among them, which leaves the bucket loaded",
			func(_ *op.Request, o *recorder) { o.initErr = op.ErrPermanentRedirect },
			MatchError(op.ErrPermanentRedirect), []string{"init"}),
		Entry("a refusal radosgw makes before verify_op_mask",
			func(_ *op.Request, o *recorder) { o.permErr = op.BeforeVerify(op.ErrUserSuspended) },
			MatchError(op.ErrUserSuspended), []string{"init", "verify"}),
		Entry("the op mask's",
			func(r *op.Request, _ *recorder) { r.Identity.OpMask = op.OpTypeRead },
			BeIdenticalTo(op.ErrAccessDenied), []string{"init", "verify"}),
	)
	It("leaves the preflight to its own op", func(ctx SpecContext) {
		expectServed(op.Run(ctx, renamed{recorder: rec, name: op.OptionsCORSName}, req))
	})
	It("passes the read of a website's error document, which serve_errordoc makes unchecked", func(ctx SpecContext) {
		req.Method, req.Website, req.WebsiteErrorDoc = http.MethodGet, true, true
		setCORS(appReadCORS())
		expectServed(op.Run(ctx, rec, req))
	})
})

var _ = Describe("Request.BucketCORS", func() {
	It("decodes the attr once while the record holds the same bytes, and again once a write replaces them", func() {
		r := &op.Request{BucketRec: &op.BucketRecord{Attrs: map[string][]byte{op.AttrCORS: readersCORS().Encode()}}}
		first, err := r.BucketCORS()
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(Equal(decodedCORS(readersCORS())))
		again, err := r.BucketCORS()
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(BeIdenticalTo(first), "the origin check and the response's headers share one decode")

		r.BucketRec.Attrs[op.AttrCORS] = appReadCORS().Encode()
		replaced, err := r.BucketCORS()
		Expect(err).NotTo(HaveOccurred())
		Expect(replaced).To(Equal(decodedCORS(appReadCORS())))
	})
	It("answers no configuration for no record or no attr, and UnknownError each time for one that does not decode", func() {
		r := &op.Request{}
		Expect(r.BucketCORS()).To(BeNil())
		r.BucketRec = &op.BucketRecord{Attrs: map[string][]byte{}}
		Expect(r.BucketCORS()).To(BeNil())
		r.BucketRec.Attrs[op.AttrCORS] = []byte{0xff}
		for range 2 {
			c, err := r.BucketCORS()
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(c).To(BeNil())
		}
	})
	It("answers UnknownError for an empty attr, which is a configuration that does not decode", func() {
		r := &op.Request{BucketRec: &op.BucketRecord{Attrs: map[string][]byte{op.AttrCORS: {}}}}
		_, err := r.BucketCORS()
		Expect(err).To(MatchError(op.ErrUnknown))
	})
})
