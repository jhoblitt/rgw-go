package s3_test

import (
	"net/http"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

var _ = Describe("the bucket policy status route", func() {
	It("binds get_bucket_policy_status in every handler", func() {
		h := newHandler(memstore.New(memstore.Config{}), s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElement("bucket/get_bucket_policy_status"))
	})

	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var w *subresWorld
			BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, rel) })

			DescribeTable("GET answers 200 with PolicyStatus in S3's namespace and IsPublic lower-case",
				func(ctx SpecContext, canned, isPublic string) {
					alice := acl.Owner{ID: "alice", DisplayName: "Alice"}
					p, err := acl.Canned(alice, alice, canned)
					Expect(err).NotTo(HaveOccurred())
					w.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodeACL(p)})

					rec := w.send(w.alice, http.MethodGet, "/plain?policyStatus", "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), "to_mime_type, rgw_rest_s3.cc:4590 at v19.2.7")
					want := `<?xml version="1.0" encoding="UTF-8"?><PolicyStatus xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
						`<IsPublic>` + isPublic + `</IsPublic></PolicyStatus>`
					Expect(rec.Body.String()).To(Equal(want), "rgw_rest_s3.cc:4584-4602 at v19.2.7, 5095-5113 at v20.2.4")
					Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
					Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "end_header names no length")
				},
				Entry("a private bucket", "private", "false"),
				Entry("a bucket whose ACL grants everyone READ", "public-read", "true"),
			)
		})
	}

	Describe("a refused GET", func() {
		var w *subresWorld
		BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

		It("answers a missing bucket 404 with the NoSuchBucket error document and no PolicyStatus", func() {
			rec := w.send(w.alice, http.MethodGet, "/missing?policyStatus", "")
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
			Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchBucket</Code><Message></Message>`+
				`<BucketName>missing</BucketName><RequestId>`+exactHeader(rec.Header(), "x-amz-request-id")+`</RequestId><HostId>4155-z-zg</HostId></Error>`),
				"abort_early, rgw_process.cc:405-409 at v19.2.7")
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		})
		It("answers a requester the bucket's ACL refuses 403 AccessDenied and no PolicyStatus", func() {
			rec := w.send(w.bob, http.MethodGet, "/plain?policyStatus", "")
			Expect(rec.Code).To(Equal(http.StatusForbidden), rec.Body.String())
			Expect(rec.Body.String()).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code>`))
			Expect(rec.Body.String()).NotTo(ContainSubstring("PolicyStatus"))
		})
	})
})
