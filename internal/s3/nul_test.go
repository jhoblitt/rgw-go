package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// invoker is a counterfeiter fake, which records every call made on it.
type invoker interface {
	Invocations() map[string][][]any
}

var _ = Describe("a NUL in a request argument that names a RADOS object", func() {
	var (
		h      *s3.Handler
		fakes  map[string]invoker
		authed int
	)
	BeforeEach(func() {
		zone, users, accounts := &opfakes.FakeZoneInfo{}, &opfakes.FakeUserStore{}, &opfakes.FakeAccountStore{}
		buckets, objects, multipart := &opfakes.FakeBucketStore{}, &opfakes.FakeObjectStore{}, &opfakes.FakeMultipartStore{}
		stats, metadata, authz := &opfakes.FakeStatsStore{}, &opfakes.FakeMetadataStore{}, &opfakes.FakeAuthorizer{}
		fakes = map[string]invoker{
			"zone": zone, "users": users, "accounts": accounts, "buckets": buckets, "objects": objects,
			"multipart": multipart, "stats": stats, "metadata": metadata, "authz": authz,
		}
		env := &op.Env{
			Zone: zone, Users: users, Accounts: accounts, Buckets: buckets, Objects: objects, Multipart: multipart,
			Stats: stats, Metadata: metadata, Authz: authz, Metrics: op.NopMetrics{}, HostID: "4155-z-zg",
		}
		authed = 0
		auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			authed++
			return nil, op.ErrAccessDenied
		})
		h = s3.NewHandler(env, auth, testConfig(s3.Config{}))
	})

	DescribeTable("is refused with 400 InvalidArgument before authentication, touching no store",
		func(method, target string, hdr ...string) {
			rec := serveReq(h, method, target, nil, hdr...)
			Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"))
			Expect(rec.Body.String()).NotTo(ContainSubstring("<BucketName>"), "refused before postauth_init names the bucket")
			Expect(authed).To(BeZero(), "authentication reads the access key's index object")
			for name, f := range fakes {
				Expect(f.Invocations()).To(BeEmpty(), "the %s store", name)
			}
		},
		Entry("versionId, an object instance", http.MethodGet, "/plain/k?versionId=a%00b"),
		Entry("versionId on a write", http.MethodDelete, "/plain/k?versionId=a%00b"),
		Entry("uploadId, a multipart upload's meta and part objects", http.MethodPut, "/plain/k?partNumber=1&uploadId=a%00b"),
		Entry("uploadId on a listing of parts", http.MethodGet, "/plain/k?uploadId=a%00b"),
		Entry("a query argument's name, which as x-amz-meta-* names an xattr", http.MethodPut, "/plain/k?x-amz-meta-a%00b=v"),
		Entry("the copy source's bucket", http.MethodPut, "/plain/k", "X-Amz-Copy-Source", "/pl%00ain/src"),
		Entry("the copy source's tenant", http.MethodPut, "/plain/k", "X-Amz-Copy-Source", "t%00x:plain/src"),
		Entry("the copy source's key", http.MethodPut, "/plain/k", "X-Amz-Copy-Source", "/plain/s%00rc"),
		Entry("the copy source's versionId", http.MethodPut, "/plain/k", "X-Amz-Copy-Source", "/plain/src?versionId=a%00b"),
		Entry("the copy source's versionId, where a decode of the whole value would come out empty",
			http.MethodPut, "/plain/k", "X-Amz-Copy-Source", "/plain/src?versionId=%00&x=%zz"),
		Entry("a part copy's source, which is decoded whole", http.MethodPut, "/plain/k?partNumber=1&uploadId=u",
			"X-Amz-Copy-Source", "plain/src%3FversionId=a%00b"),
		Entry("a ranged part copy's source", http.MethodPut, "/plain/k?partNumber=1&uploadId=u",
			"X-Amz-Copy-Source", "plain/s%00rc", "X-Amz-Copy-Source-Range", "bytes=0-1"),
		Entry("a part copy's source with a '?' before the NUL, which only that parse keeps in the key",
			http.MethodPut, "/plain/k?partNumber=1&uploadId=u", "X-Amz-Copy-Source", "plain/s?rc%00"),
	)

	DescribeTable("is left to the op where the argument names nothing in RADOS",
		func(method, target string, hdr ...string) {
			rec := serveReq(h, method, target, nil, hdr...)
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
			Expect(authed).To(Equal(1), "the request reaches authentication")
		},
		Entry("a listing's prefix and marker, which reach the index only inside an encoded class call",
			http.MethodGet, "/plain?prefix=a%00b&marker=c%00d&delimiter=%00"),
		Entry("an argument read as a C string", http.MethodGet, "/plain?list-type=2&encoding-type=url%00x"),
		Entry("a versionId whose escape the S3 parse decodes once", http.MethodGet, "/plain/k?versionId=a%2500b"),
		Entry("a partNumber, which is read as a number", http.MethodPut, "/plain/k?partNumber=1%00&uploadId=u"),
	)

	It("judges the copy source the ops read, the last of several", func() {
		send := func(values ...string) {
			req := httptest.NewRequestWithContext(GinkgoT().Context(), http.MethodPut, "/plain/k", nil)
			for _, v := range values {
				req.Header.Add("X-Amz-Copy-Source", v)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
		}
		send("/plain/s%00rc", "/plain/src")
		Expect(authed).To(Equal(1), "an earlier value names nothing")
		send("/plain/src", "/plain/s%00rc")
		Expect(authed).To(Equal(1), "the last value is refused")
	})
})
