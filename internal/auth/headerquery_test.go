package auth_test

import (
	"context"
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // a part's ETag is its MD5
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/aws/aws-sdk-go-v2/aws"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// headerV2 is a SigV2 request for method and target, a path and query,
// carrying body and hdr, signed in its Authorization header for alice at
// v2Date over sts.
func headerV2(ctx context.Context, method, target, body string, hdr map[string]string, sts string) *http.Request {
	req := httptest.NewRequestWithContext(ctx, method, "http://s3.example.com"+target, nil)
	if body != "" {
		req = newBodyRequest(ctx, method, "http://s3.example.com"+target, body)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req.Header.Set("Date", v2Date)
	mac := hmac.New(sha1.New, []byte(secretOf("alice")))
	mac.Write([]byte(sts))
	req.Header.Set("Authorization", "AWS "+accessKeyOf("alice")+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return req
}

// partParams is a multipart store that records the PutParams of each part it
// writes.
type partParams struct {
	op.MultipartStore
	params *[]op.PutParams
}

func (p partParams) PutPart(ctx context.Context, up *op.Upload, n int, body io.Reader, pp op.PutParams) (*op.PartResult, error) {
	*p.params = append(*p.params, pp)
	return p.MultipartStore.PutPart(ctx, up, n, body, pp)
}

var _ = Describe("The query parameters a SigV2 signature does not cover", func() {
	var (
		store *memstore.Store
		env   *op.Env
		v     *auth.Verifier
		h     *s3.Handler
	)

	// The authorizer allows every request, so that an anonymous one can
	// write; the specs are about what a write stores.
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		alice := store.AddUser(user("alice", nil))
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
		env = &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store,
			Usage: store, Metadata: store, Authz: &opfakes.FakeAuthorizer{}, Metrics: op.NopMetrics{},
		}
		h = s3.NewHandler(env, v, s3.Config{})
	})

	serve := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	stored := func(ctx context.Context, key string) map[string]string {
		rec, err := store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: key})
		Expect(err).NotTo(HaveOccurred(), "object %s", key)
		return userMeta(st.Attrs)
	}
	initUpload := func(ctx context.Context) string {
		rec := serve(headerV2(ctx, http.MethodPost, "/b/k?uploads", "", map[string]string{"X-Amz-Meta-A": "h"},
			"POST\n\n\n"+v2Date+"\nx-amz-meta-a:h\n/b/k?uploads"))
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		var res struct {
			UploadID string `xml:"UploadId"`
		}
		Expect(xml.Unmarshal(rec.Body.Bytes(), &res)).To(Succeed())
		return res.UploadID
	}

	It("stores none of a PutObject's query meta, beside the meta it signs in its headers", func(ctx SpecContext) {
		rec := serve(headerV2(ctx, http.MethodPut, "/b/k?x-amz-meta-x=1&X-AMZ-META-A=q", "hello", map[string]string{"X-Amz-Meta-A": "h"},
			"PUT\n\n\n"+v2Date+"\nx-amz-meta-a:h\n/b/k"))
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(stored(ctx, "k")).To(Equal(map[string]string{"x-amz-meta-a": "h"}),
			"radosgw stores x-amz-meta-x and joins the query's a to the header's (rgw_rest_s3.cc:2589-2603 at v19.2.7)")
	})

	It("stores none of an UploadPart's query meta on the part", func(ctx SpecContext) {
		var params []op.PutParams
		env.Multipart = partParams{MultipartStore: store, params: &params}
		id := initUpload(ctx)
		rec := serve(headerV2(ctx, http.MethodPut, "/b/k?partNumber=1&uploadId="+id+"&x-amz-meta-x=1", "hello",
			map[string]string{"X-Amz-Meta-A": "h"}, "PUT\n\n\n"+v2Date+"\nx-amz-meta-a:h\n/b/k?partNumber=1&uploadId="+id))
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(params).To(HaveLen(1))
		Expect(userMeta(params[0].Attrs)).To(Equal(map[string]string{"x-amz-meta-a": "h"}))
	})

	It("stores none of a CompleteMultipartUpload's query meta on the object", func(ctx SpecContext) {
		id := initUpload(ctx)
		part := serve(headerV2(ctx, http.MethodPut, "/b/k?partNumber=1&uploadId="+id, "hello", nil,
			"PUT\n\n\n"+v2Date+"\n/b/k?partNumber=1&uploadId="+id))
		Expect(part.Code).To(Equal(http.StatusOK), part.Body.String())
		etag := md5.Sum([]byte("hello"))
		body := "<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>" + hex.EncodeToString(etag[:]) +
			"</ETag></Part></CompleteMultipartUpload>"
		rec := serve(headerV2(ctx, http.MethodPost, "/b/k?uploadId="+id+"&x-amz-meta-x=1", body, nil,
			"POST\n\n\n"+v2Date+"\n/b/k?uploadId="+id))
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(stored(ctx, "k")).To(Equal(map[string]string{"x-amz-meta-a": "h"}), "the upload's meta alone")
	})

	It("still refuses a PutObject whose query asks for encryption, which it must not store in the clear", func(ctx SpecContext) {
		rec := serve(headerV2(ctx, http.MethodPut, "/b/k?x-amz-server-side-encryption=AES256", "hello", nil,
			"PUT\n\n\n"+v2Date+"\n/b/k"))
		Expect(rec.Code).To(Equal(http.StatusNotImplemented), rec.Body.String())
		b, err := store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		st, err := store.StatObject(ctx, b, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeFalse())
	})

	DescribeTable("names the route each request authenticated on",
		func(ctx SpecContext, build func(context.Context) *http.Request, want op.AuthRoute) {
			res, err := v.Authenticate(ctx, build(ctx), anyPayload)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Route).To(Equal(want))
		},
		Entry("a SigV2 request signed in its header", func(ctx context.Context) *http.Request {
			return headerV2(ctx, http.MethodGet, "/b/k?x-amz-meta-x=1", "", nil, "GET\n\n\n"+v2Date+"\n/b/k")
		}, op.RouteSigV2Header),
		Entry("a presigned SigV2 request", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "x-amz-meta-x=1", nil, "GET\n\n\n"+v2Expires+"\nx-amz-meta-x:1\n/b/k")
		}, op.RouteSigV2Query),
		Entry("a SigV4 request signed in its header", func(ctx context.Context) *http.Request {
			return signedAs(ctx, newGet(ctx, "http://s3.example.com/b/k?x-amz-meta-x=1"), "alice")
		}, op.RouteSigV4Header),
		Entry("a presigned SigV4 request", func(ctx context.Context) *http.Request {
			return presignedV4(ctx, http.MethodGet, "http://s3.example.com/b/k?x-amz-meta-x=1&X-Amz-Expires=300", "")
		}, op.RouteSigV4Query),
		Entry("an anonymous request, which signs nothing", func(ctx context.Context) *http.Request {
			return newGet(ctx, "http://s3.example.com/b/k?x-amz-meta-x=1")
		}, op.RouteAnonymous),
	)

	DescribeTable("leaves a PutObject storing its query meta on every other route",
		func(ctx SpecContext, build func(context.Context) *http.Request) {
			rec := serve(build(ctx))
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(stored(ctx, "k")).To(Equal(map[string]string{"x-amz-meta-x": "1"}))
		},
		Entry("a presigned SigV2 request, which signs it", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-x=1", nil, "PUT\n\n\n"+v2Expires+"\nx-amz-meta-x:1\n/b/k")
		}),
		Entry("a SigV4 request signed in its header", func(ctx context.Context) *http.Request {
			return signedAs(ctx, newBodyRequest(ctx, http.MethodPut, "http://s3.example.com/b/k?x-amz-meta-x=1", "hello"), "alice")
		}),
		Entry("a presigned SigV4 request", func(ctx context.Context) *http.Request {
			return presignedV4(ctx, http.MethodPut, "http://s3.example.com/b/k?x-amz-meta-x=1&X-Amz-Expires=300", "hello")
		}),
		Entry("an anonymous request, which may send the same names as headers", func(ctx context.Context) *http.Request {
			return newBodyRequest(ctx, http.MethodPut, "http://s3.example.com/b/k?x-amz-meta-x=1", "hello")
		}),
	)

	// PutObject answers any request for encryption 501 before it
	// authorizes, so the condition keys are read here in the handler's own
	// order: parse, authenticate, apply, then the authorizer's environment.
	DescribeTable("takes no encryption condition key from the query on either SigV2 route, neither of which signs it",
		func(ctx SpecContext, build func(context.Context) *http.Request, want OmegaMatcher) {
			req := build(ctx)
			p, err := s3.ParseRequest(req, s3.Config{}, fixedNow())
			Expect(err).NotTo(HaveOccurred())
			res, err := v.Authenticate(ctx, req, anyPayload)
			Expect(err).NotTo(HaveOccurred(), "%s", req.URL)
			s3.ApplyAuth(p.Req, res)
			env := authz.BuildEnv(authz.Config{Now: fixedNow}, p.Req, policy.S3PutObject)
			Expect(env.Lookup("s3:x-amz-server-side-encryption")).To(want)
		},
		Entry("the header route, beside the signed header", func(ctx context.Context) *http.Request {
			return headerV2(ctx, http.MethodPut, "/b/k?x-amz-server-side-encryption=aws:kms", "hello",
				map[string]string{"X-Amz-Server-Side-Encryption": "AES256"},
				"PUT\n\n\n"+v2Date+"\nx-amz-server-side-encryption:AES256\n/b/k")
		}, Equal([]string{"AES256"})),
		Entry("the presigned route, beside the signed header", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodPut, "/b/k", "x-amz-server-side-encryption=aws:kms",
				map[string]string{"X-Amz-Server-Side-Encryption": "AES256"},
				"PUT\n\n\n"+v2Expires+"\nx-amz-server-side-encryption:AES256\n/b/k")
		}, Equal([]string{"AES256"})),
		Entry("the presigned route, the query alone", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodPut, "/b/k", "x-amz-server-side-encryption=aws:kms", nil, "PUT\n\n\n"+v2Expires+"\n/b/k")
		}, BeEmpty()),
		Entry("a presigned SigV4 request, whose canonical query signs it", func(ctx context.Context) *http.Request {
			return presignedV4(ctx, http.MethodPut, "http://s3.example.com/b/k?x-amz-server-side-encryption=aws:kms&X-Amz-Expires=300", "hello")
		}, Equal([]string{"aws:kms"})),
	)
})

// presignedV4 is a presigned SigV4 request for method and target, presigned
// for alice at fixedNow, carrying body.
func presignedV4(ctx context.Context, method, target, body string) *http.Request {
	creds := aws.Credentials{AccessKeyID: accessKeyOf("alice"), SecretAccessKey: secretOf("alice")}
	signed, _, err := sdkSigner().PresignHTTP(ctx, creds, httptest.NewRequestWithContext(ctx, method, target, nil),
		"UNSIGNED-PAYLOAD", "s3", "us-east-1", fixedNow())
	Expect(err).NotTo(HaveOccurred())
	if body == "" {
		return httptest.NewRequestWithContext(ctx, method, signed, nil)
	}
	return newBodyRequest(ctx, method, signed, body)
}
