package auth_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"encoding/base64"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// headerSignedV2 is a SigV2 header-signed request for method and target,
// signed as alice at v2Date over sts.
func headerSignedV2(ctx context.Context, method, target, sts string) *http.Request {
	req := httptest.NewRequestWithContext(ctx, method, "http://s3.example.com"+target, nil)
	req.Header.Set("Date", v2Date)
	mac := hmac.New(sha1.New, []byte(secretOf("alice")))
	mac.Write([]byte(sts))
	req.Header.Set("Authorization", "AWS "+accessKeyOf("alice")+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return req
}

// Each forged request below signs the string a legitimate request signs,
// with a separator moved inside a value: refused once the request has
// authenticated, so a request radosgw refuses earlier gets radosgw's error.
var _ = Describe("SigV2 separators inside a signed value", func() {
	var (
		store *memstore.Store
		v     *auth.Verifier
		h     *s3.Handler
	)

	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		alice := store.AddUser(user("alice", nil))
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
		env := &op.Env{Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		h = s3.NewHandler(env, v, s3.Config{})
	})

	serve := func(req *http.Request) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	stored := func(ctx context.Context, key string) bool {
		rec, err := store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: key})
		Expect(err).NotTo(HaveOccurred())
		return st.Exists
	}

	It("refuses a CopyObject whose signed copy source is moved into Expires, which F15's refusal does not see", func(ctx SpecContext) {
		Expect(serve(aliceV2(ctx, http.MethodPut, "/b/src", "", nil, "PUT\n\n\n"+v2Expires+"\n/b/src"))).To(Equal(http.StatusOK))
		const sts = "PUT\n\n\n" + v2Expires + "\nx-amz-copy-source:b/src\n/b/k"
		moved := func() *http.Request {
			return presignedV2(ctx, http.MethodPut, "/b/k", "", nil, accessKeyOf("alice"), secretOf("alice"),
				v2Expires+"%0Ax-amz-copy-source:b/src", sts)
		}
		_, err := v.Authenticate(ctx, moved(), anyPayload)
		Expect(err).To(SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("the Expires parameter signs a line break"))))
		Expect(serve(moved())).To(Equal(http.StatusForbidden))
		Expect(stored(ctx, "k")).To(BeFalse(), "no PutObject of the request's body")

		asSigned := aliceV2(ctx, http.MethodPut, "/b/k", "", map[string]string{"X-Amz-Copy-Source": "b/src"}, sts)
		Expect(serve(asSigned)).To(Equal(http.StatusOK), "the same signature with the header as signed copies")
		Expect(stored(ctx, "k")).To(BeTrue())
	})

	It("refuses a presigned UploadPart whose upload id is packed into its part number, which would otherwise store a PutObject", func(ctx SpecContext) {
		const sts = "PUT\n\n\n" + v2Expires + "\n/b/k?partNumber=1&uploadId=U"
		Expect(serve(aliceV2(ctx, http.MethodPut, "/b/k", "partNumber=1%26uploadId%3DU", nil, sts))).To(Equal(http.StatusForbidden))
		Expect(stored(ctx, "k")).To(BeFalse())
	})

	DescribeTable("refuses a forged separator, with radosgw's error first",
		func(ctx SpecContext, build func(context.Context) *http.Request, want OmegaMatcher) {
			_, err := v.Authenticate(ctx, build(ctx), anyPayload)
			Expect(err).To(want)
		},
		Entry("an Expires line break with a wrong signature", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodGet, "/b/k", "", nil, accessKeyOf("alice"), "not-the-secret",
				v2Expires+"%0Ax-amz-acl:private", "GET\n\n\n"+v2Expires+"\nx-amz-acl:private\n/b/k")
		}, MatchError(op.ErrSignatureDoesNotMatch)),
		Entry("an expired Expires with a line break", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodGet, "/b/k", "", nil, accessKeyOf("alice"), secretOf("alice"),
				"1440000000%0Ax-amz-acl:private", "GET\n\n\n1440000000\nx-amz-acl:private\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("expired")))),
		Entry("a signed canned ACL moved into Expires", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodGet, "/b/k", "", nil, accessKeyOf("alice"), secretOf("alice"),
				v2Expires+"%0Ax-amz-acl:private", "GET\n\n\n"+v2Expires+"\nx-amz-acl:private\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("the Expires parameter signs a line break")))),
		Entry("a CR in Expires", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodGet, "/b/k", "", nil, accessKeyOf("alice"), secretOf("alice"),
				v2Expires+"%0D", "GET\n\n\n"+v2Expires+"\r\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("the Expires parameter signs a line break")))),
		Entry("a query meta name with a colon, signed as the value x:y", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a:x=y", nil, "PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:x:y\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring(`"x-amz-meta-a:x" signs a colon in its name`)))),
		Entry("a GET's signed versionId packed into its response-content-disposition", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "response-content-disposition=x%26versionId%3DV", nil,
				"GET\n\n\n"+v2Expires+"\n/b/k?response-content-disposition=x&versionId=V")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("response-content-disposition's value spells the sub-resource versionId")))),
		Entry("an empty sub-resource packed at a value's end", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodPost, "/b/k", "response-content-type=x%26uploads", nil,
				"POST\n\n\n"+v2Expires+"\n/b/k?response-content-type=x&uploads")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("response-content-type's value spells the sub-resource uploads")))),
		Entry("a packed sub-resource with a wrong signature", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodGet, "/b/k", "response-content-disposition=x%26versionId%3DV", nil,
				accessKeyOf("alice"), "not-the-secret", v2Expires, "GET\n\n\n"+v2Expires+"\n/b/k?response-content-disposition=x&versionId=V")
		}, MatchError(op.ErrSignatureDoesNotMatch)),
		Entry("an admin sub-resource, written without a value, packed at a value's end", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "partNumber=1%26policy", nil, "GET\n\n\n"+v2Expires+"\n/b/k?partNumber=1&policy")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("partNumber's value spells the sub-resource policy")))),
		Entry("a packed sub-resource on the header route", func(ctx context.Context) *http.Request {
			return headerSignedV2(ctx, http.MethodPut, "/b/k?partNumber=1%26uploadId%3DU",
				"PUT\n\n\n"+v2Date+"\n/b/k?partNumber=1&uploadId=U")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("partNumber's value spells the sub-resource uploadId")))),
	)

	DescribeTable("authenticates the separators no other request signs alike",
		func(ctx SpecContext, query, sts string) {
			_, err := v.Authenticate(ctx, aliceV2(ctx, http.MethodGet, "/b/k", query, nil, "GET\n\n\n"+v2Expires+"\n"+sts), anyPayload)
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("a filename holding '&'", "response-content-disposition=attachment%3B%20filename%3D%22a%26b.txt%22",
			`/b/k?response-content-disposition=attachment; filename="a&b.txt"`),
		Entry("a filename holding '&' and the start of a sub-resource's name", "response-content-disposition=a%26versioning.txt",
			"/b/k?response-content-disposition=a&versioning.txt"),
		Entry("a sub-resource name that sorts before the value's own", "response-content-type=x%26acl",
			"/b/k?response-content-type=x&acl"),
		Entry("a listed name the request never files, so the resource never writes", "response-content-type=x%26object-lock",
			"/b/k?response-content-type=x&object-lock"),
		Entry("an admin sub-resource with a value, which the resource writes without one", "partNumber=1%26policy%3Dx",
			"/b/k?partNumber=1&policy=x"),
		Entry("a colon in a query meta value", "x-amz-meta-a=x:y", "x-amz-meta-a:x:y\n/b/k"),
	)

	It("stores a query meta value holding a colon as signed", func(ctx SpecContext) {
		Expect(serve(aliceV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a=x:y", nil, "PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:x:y\n/b/k"))).To(Equal(http.StatusOK))
		rec, err := store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(userMeta(st.Attrs)).To(Equal(map[string]string{"x-amz-meta-a": "x:y"}))
	})
})
