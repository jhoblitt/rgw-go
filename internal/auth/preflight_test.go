package auth_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// signedRoute builds a request for method and target, a path and an optional
// query, carrying hdr and signed for key with secret at fixedNow on one of
// the four signed routes. The specs below sign with wrongSecret, so a
// request that passes passed without its signature compared.
type signedRoute struct {
	name  string
	build func(ctx context.Context, method, target string, hdr http.Header, key, secret string) *http.Request
}

const wrongSecret = "not-the-secret"

func withQuery(target, q string) string {
	if strings.Contains(target, "?") {
		return target + "&" + q
	}
	return target + "?" + q
}

func newRequest(ctx context.Context, method, target string, hdr http.Header) *http.Request {
	req := httptest.NewRequestWithContext(ctx, method, "http://s3.example.com"+target, nil)
	maps.Copy(req.Header, hdr)
	return req
}

func v2Signature(secret, sts string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(sts))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

var (
	v4Header = signedRoute{"SigV4 header", func(ctx context.Context, method, target string, hdr http.Header, key, secret string) *http.Request {
		return signWith(ctx, newRequest(ctx, method, target, hdr), key, secret, fixedNow())
	}}
	v4Query = signedRoute{"SigV4 presigned", func(ctx context.Context, method, target string, hdr http.Header, key, secret string) *http.Request {
		base := newRequest(ctx, method, withQuery(target, "X-Amz-Expires=300"), hdr)
		creds := aws.Credentials{AccessKeyID: key, SecretAccessKey: secret}
		u, _, err := sdkSigner().PresignHTTP(ctx, creds, base, "UNSIGNED-PAYLOAD", "s3", "us-east-1", fixedNow())
		Expect(err).NotTo(HaveOccurred())
		parsed, err := url.Parse(u)
		Expect(err).NotTo(HaveOccurred())
		return newRequest(ctx, method, parsed.RequestURI(), hdr)
	}}
	v2Header = signedRoute{"SigV2 header", func(ctx context.Context, method, target string, hdr http.Header, key, secret string) *http.Request {
		req := newRequest(ctx, method, target, hdr)
		req.Header.Set("Date", v2Date)
		req.Header.Set("Authorization", "AWS "+key+":"+v2Signature(secret, method+"\n\n\n"+v2Date+"\n"+target))
		return req
	}}
	v2Query = signedRoute{"SigV2 presigned", func(ctx context.Context, method, target string, hdr http.Header, key, secret string) *http.Request {
		sig := v2Signature(secret, method+"\n\n\n"+v2Expires+"\n"+target)
		q := "AWSAccessKeyId=" + key + "&Expires=" + v2Expires + "&Signature=" + url.QueryEscape(sig)
		return newRequest(ctx, method, withQuery(target, q), hdr)
	}}
	signedRoutes = []signedRoute{v4Header, v4Query, v2Header, v2Query}
)

// preflightHeaders are a browser's CORS preflight headers, asking for method.
func preflightHeaders(method string) http.Header {
	return http.Header{"Origin": {"https://example.org"}, "Access-Control-Request-Method": {method}}
}

// noMethod is a preflight's headers without Access-Control-Request-Method.
func noMethod() http.Header { return http.Header{"Origin": {"https://example.org"}} }

// requestIDElement is an error document's RequestId, which every request
// draws anew.
var requestIDElement = regexp.MustCompile(`<RequestId>[^<]*</RequestId>`)

var _ = Describe("Verifier on a signed CORS preflight", func() {
	var (
		store *memstore.Store
		cfg   auth.Config
		v     *auth.Verifier
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("alice", nil))
		cfg = auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
	})

	It("grants a preflight for each CORS method whatever its signature, on every signed route", func(ctx SpecContext) {
		for _, f := range signedRoutes {
			for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead} {
				req := f.build(ctx, http.MethodOptions, "/plain/k", preflightHeaders(m), accessKeyOf("alice"), wrongSecret)
				res, err := v.Authenticate(ctx, req, 0)
				Expect(err).NotTo(HaveOccurred(), "%s asking for %s", f.name, m)
				Expect(res.Identity.AccessKey).To(Equal(accessKeyOf("alice")), "%s asking for %s", f.name, m)
				Expect(res.Identity.User.UserID).To(Equal(meta.UserID{ID: "alice"}), "%s asking for %s", f.name, m)
				Expect(res.Identity.Unverified).To(BeTrue(), "%s asking for %s: granted on the key's id alone", f.name, m)
			}
		}
	})

	It("marks no identity unverified whose signature it compared", func(ctx SpecContext) {
		for _, f := range signedRoutes {
			for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete} {
				res, err := v.Authenticate(ctx, f.build(ctx, m, "/plain/k", preflightHeaders(http.MethodGet), accessKeyOf("alice"), secretOf("alice")), anyPayload)
				Expect(err).NotTo(HaveOccurred(), "%s %s", f.name, m)
				Expect(res.Identity.Unverified).To(BeFalse(), "%s %s", f.name, m)
			}
		}
		req := newRequest(ctx, http.MethodOptions, "/plain/k", preflightHeaders(http.MethodGet))
		res, err := v.Authenticate(ctx, req, 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Anonymous).To(BeTrue())
		Expect(res.Identity.Unverified).To(BeFalse(), "an anonymous preflight claims no key")
	})

	DescribeTable("refuses a preflight whose Access-Control-Request-Method names no CORS method with 400 InvalidArgument, before the key is looked up",
		func(ctx SpecContext, hdr http.Header) {
			for _, f := range signedRoutes {
				users := &opfakes.FakeUserStore{}
				users.GetUserByAccessKeyReturns(nil, op.ErrNoSuchUser)
				req := f.build(ctx, http.MethodOptions, "/plain/k", hdr, "AKNOBODY", wrongSecret)
				_, err := auth.New(cfg, users, nil).Authenticate(ctx, req, 0)
				Expect(err).To(MatchError(op.ErrInvalidArgument), f.name)
				Expect(users.GetUserByAccessKeyCallCount()).To(BeZero(), f.name)
			}
		},
		Entry("a missing one", noMethod()),
		Entry("an empty one", preflightHeaders("")),
		Entry("a CORS method in lower case, which strcmp tells apart", preflightHeaders("get")),
		Entry("a method no CORS rule can allow", preflightHeaders(http.MethodPatch)),
		Entry("OPTIONS itself", preflightHeaders(http.MethodOptions)),
		Entry("COPY, which a CORS rule's flags have room for but get_cors_method_flags never yields", preflightHeaders("COPY")),
		Entry("a repeated one whose last value names no CORS method, the value radosgw's environment keeps",
			http.Header{"Origin": {"https://example.org"}, "Access-Control-Request-Method": {http.MethodGet, http.MethodPatch}}),
	)

	It("takes the last value of a repeated Access-Control-Request-Method", func(ctx SpecContext) {
		hdr := http.Header{"Origin": {"https://example.org"}, "Access-Control-Request-Method": {http.MethodPatch, http.MethodPut}}
		for _, f := range signedRoutes {
			res, err := v.Authenticate(ctx, f.build(ctx, http.MethodOptions, "/plain/k", hdr, accessKeyOf("alice"), wrongSecret), 0)
			Expect(err).NotTo(HaveOccurred(), f.name)
			Expect(res.Identity.AccessKey).To(Equal(accessKeyOf("alice")), f.name)
		}
	})

	// A forged GET tells an unknown key from a known one too, by
	// InvalidAccessKeyId against SignatureDoesNotMatch.
	It("answers a preflight with an unknown key 403 InvalidAccessKeyId, as a forged GET", func(ctx SpecContext) {
		for _, f := range signedRoutes {
			for _, m := range []string{http.MethodOptions, http.MethodGet} {
				req := f.build(ctx, m, "/plain/k", preflightHeaders(http.MethodGet), "AKNOBODY", wrongSecret)
				_, err := v.Authenticate(ctx, req, 0)
				Expect(err).To(MatchError(op.ErrInvalidAccessKeyID), "%s %s", f.name, m)
			}
		}
	})

	It("takes no rgwx-uid on a system user's preflight", func(ctx SpecContext) {
		rec := store.AddUser(user("carol", func(u *meta.UserInfo) { u.System = 1 }))
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyReturns(rec, nil)
		accounts := &opfakes.FakeAccountStore{}
		for _, f := range signedRoutes {
			for _, uid := range []string{"t1%24bob", "RGW00000000000000001"} {
				req := f.build(ctx, http.MethodOptions, "/plain/k?rgwx-uid="+uid, preflightHeaders(http.MethodGet), accessKeyOf("carol"), wrongSecret)
				res, err := auth.New(cfg, users, accounts).Authenticate(ctx, req, 0)
				Expect(err).NotTo(HaveOccurred(), "%s rgwx-uid=%s", f.name, uid)
				Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "carol"})), "%s rgwx-uid=%s", f.name, uid)
				Expect(res.Identity.Tenant).To(BeEmpty(), "%s rgwx-uid=%s", f.name, uid)
			}
		}
		Expect(users.GetUserCallCount()).To(BeZero(), "the rgwx-uid user is not looked up")
		Expect(accounts.GetAccountCallCount()).To(BeZero(), "the rgwx-uid account is not looked up")
	})

	// A forged request of any other method gets the same refusal, so the
	// preflight tells its sender nothing a forged GET does not.
	DescribeTable("refuses a preflight whose key does not resolve to an active key in its user's record, as a forged request of any method",
		func(ctx SpecContext, mutate func(*op.UserRecord), want error) {
			rec, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
			Expect(err).NotTo(HaveOccurred())
			mutate(rec)
			users := &opfakes.FakeUserStore{}
			users.GetUserByAccessKeyReturns(rec, nil)
			for _, f := range signedRoutes {
				for _, m := range []string{http.MethodOptions, http.MethodGet} {
					req := f.build(ctx, m, "/plain/k", preflightHeaders(http.MethodGet), accessKeyOf("alice"), wrongSecret)
					_, err := auth.New(cfg, users, nil).Authenticate(ctx, req, 0)
					Expect(err).To(MatchError(want), "%s %s", f.name, m)
				}
			}
		},
		Entry("a key the index knows but the user's record lost",
			func(rec *op.UserRecord) { rec.Info.AccessKeys = nil }, op.ErrAccessDenied),
		Entry("an inactive key a store returns the user of",
			func(rec *op.UserRecord) {
				k := rec.Info.AccessKeys[accessKeyOf("alice")]
				k.Active = false
				rec.Info.AccessKeys[accessKeyOf("alice")] = k
			}, op.ErrInvalidAccessKeyID),
		Entry("an account user whose account does not load",
			func(rec *op.UserRecord) { rec.Info.AccountID = "RGW00000000000000001" }, op.ErrAccessDenied),
	)

	// Every method but OPTIONS has its signature compared, whatever the
	// request says of preflights or of another method.
	DescribeTable("refuses a request that is not an OPTIONS request with a wrong signature",
		func(ctx SpecContext, method, target string, hdr http.Header) {
			for _, f := range signedRoutes {
				req := f.build(ctx, method, target, hdr, accessKeyOf("alice"), wrongSecret)
				_, err := v.Authenticate(ctx, req, 0)
				Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch), f.name)
			}
		},
		Entry("a GET with a preflight's headers", http.MethodGet, "/plain/k", preflightHeaders(http.MethodGet)),
		Entry("a HEAD with a preflight's headers", http.MethodHead, "/plain/k", preflightHeaders(http.MethodHead)),
		Entry("a PUT with a preflight's headers", http.MethodPut, "/plain/k", preflightHeaders(http.MethodPut)),
		Entry("a POST with a preflight's headers", http.MethodPost, "/plain?delete", preflightHeaders(http.MethodPost)),
		Entry("a DELETE with a preflight's headers", http.MethodDelete, "/plain/k", preflightHeaders(http.MethodDelete)),
		Entry("the method token options in lower case, which is not OPTIONS", "options", "/plain/k", preflightHeaders(http.MethodGet)),
		Entry("a PUT whose X-HTTP-Method-Override says OPTIONS", http.MethodPut, "/plain/k",
			http.Header{"X-Http-Method-Override": {http.MethodOptions}, "Access-Control-Request-Method": {http.MethodPut}}),
		Entry("a DELETE whose X-HTTP-Method and X-Method-Override say OPTIONS", http.MethodDelete, "/plain/k",
			http.Header{"X-Http-Method": {http.MethodOptions}, "X-Method-Override": {http.MethodOptions}}),
		Entry("a GET whose query asks for OPTIONS", http.MethodGet, "/plain/k?X-HTTP-Method-Override=OPTIONS&_method=OPTIONS&method=OPTIONS",
			preflightHeaders(http.MethodGet)),
		Entry("a PUT on the CORS sub-resource", http.MethodPut, "/plain?cors", preflightHeaders(http.MethodPut)),
	)

	// radosgw takes the canonical method, and so the refusal, after the
	// credentials and the canonical headers and before the payload checks;
	// on SigV2, after Content-MD5 and the date and before the skew check.
	DescribeTable("refuses a preflight's missing method where radosgw's signing reaches it",
		func(ctx SpecContext, build func(context.Context) *http.Request, want error) {
			users := &opfakes.FakeUserStore{}
			users.GetUserByAccessKeyReturns(nil, op.ErrNoSuchUser)
			_, err := auth.New(cfg, users, nil).Authenticate(ctx, build(ctx), 0)
			Expect(err).To(MatchError(want))
			Expect(users.GetUserByAccessKeyCallCount()).To(BeZero())
		},
		Entry("after a SigV4 date's skew", func(ctx context.Context) *http.Request {
			return signWith(ctx, newRequest(ctx, http.MethodOptions, "/plain/k", noMethod()), "AKNOBODY", wrongSecret, fixedNow().Add(-time.Hour))
		}, op.ErrRequestTimeTooSkewed),
		Entry("after an unsigned x-amz- header", func(ctx context.Context) *http.Request {
			req := signWith(ctx, newRequest(ctx, http.MethodOptions, "/plain/k", noMethod()), "AKNOBODY", wrongSecret, fixedNow())
			req.Header.Set("X-Amz-Meta-Late", "1")
			return req
		}, op.ErrAccessDenied),
		Entry("after a presigned SigV4 URL's expiry", func(ctx context.Context) *http.Request {
			return newRequest(ctx, http.MethodOptions, "/plain/k?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKNOBODY%2F20150830%2Fus-east-1%2Fs3%2Faws4_request"+
				"&X-Amz-Date=20150830T100000Z&X-Amz-Expires=1&X-Amz-SignedHeaders=host&X-Amz-Signature=00", noMethod())
		}, op.ErrAccessDenied),
		Entry("before an empty payload's checksum mismatch", func(ctx context.Context) *http.Request {
			return signWith(ctx, withPayloadHash(newRequest(ctx, http.MethodOptions, "/plain/k", noMethod()), helloSHA256), "AKNOBODY", wrongSecret, fixedNow())
		}, op.ErrInvalidArgument),
		Entry("before the refusal of a signed payload the route does not take", func(ctx context.Context) *http.Request {
			req := withPayloadHash(newBodyRequest(ctx, http.MethodOptions, "http://s3.example.com/plain/k", "hello"), helloSHA256)
			req.Header.Set("Origin", "https://example.org")
			return signWith(ctx, req, "AKNOBODY", wrongSecret, fixedNow())
		}, op.ErrInvalidArgument),
		Entry("after a SigV2 Content-MD5 that is not base64", func(ctx context.Context) *http.Request {
			hdr := noMethod()
			hdr.Set("Content-Md5", "!!")
			return v2Header.build(ctx, http.MethodOptions, "/plain/k", hdr, "AKNOBODY", wrongSecret)
		}, op.ErrAccessDenied),
		Entry("after a SigV2 request without a date", func(ctx context.Context) *http.Request {
			req := v2Header.build(ctx, http.MethodOptions, "/plain/k", noMethod(), "AKNOBODY", wrongSecret)
			req.Header.Del("Date")
			return req
		}, op.ErrAccessDenied),
		Entry("before a SigV2 date's skew", func(ctx context.Context) *http.Request {
			req := v2Header.build(ctx, http.MethodOptions, "/plain/k", noMethod(), "AKNOBODY", wrongSecret)
			req.Header.Set("Date", "Sun, 30 Aug 2015 10:36:00 GMT")
			return req
		}, op.ErrInvalidArgument),
		Entry("after a presigned SigV2 URL's expiry", func(ctx context.Context) *http.Request {
			return newRequest(ctx, http.MethodOptions, "/plain/k?AWSAccessKeyId=AKNOBODY&Expires=1&Signature=x", noMethod())
		}, op.ErrAccessDenied),
		Entry("after a presigned SigV2 URL's empty session token", func(ctx context.Context) *http.Request {
			return newRequest(ctx, http.MethodOptions, "/plain/k?AWSAccessKeyId=AKNOBODY&Expires="+v2Expires+"&Signature=x&x-amz-security-token=", noMethod())
		}, op.ErrAccessDenied),
	)

	It("still refuses a skewed SigV2 preflight that names its method", func(ctx SpecContext) {
		req := v2Header.build(ctx, http.MethodOptions, "/plain/k", preflightHeaders(http.MethodGet), accessKeyOf("alice"), wrongSecret)
		req.Header.Set("Date", "Sun, 30 Aug 2015 10:36:00 GMT")
		_, err := v.Authenticate(ctx, req, 0)
		Expect(err).To(MatchError(op.ErrRequestTimeTooSkewed))
	})

	It("still refuses a signed payload the preflight does not take, after the method", func(ctx SpecContext) {
		req := withPayloadHash(newBodyRequest(ctx, http.MethodOptions, "http://s3.example.com/plain/k", "hello"), helloSHA256)
		req.Header.Set("Access-Control-Request-Method", http.MethodPut)
		_, err := v.Authenticate(ctx, signWith(ctx, req, accessKeyOf("alice"), wrongSecret, fixedNow()), 0)
		Expect(err).To(MatchError(op.ErrNotImplemented))
	})
})

var _ = Describe("Signed OPTIONS requests through the s3 and admin handlers", func() {
	newHandler := func(rel denc.Release) (*s3.Handler, *memstore.Store) {
		store := memstore.New(memstore.Config{Release: rel, Now: fixedNow})
		store.AddUser(user("alice", nil))
		store.AddUser(user("sue", func(u *meta.UserInfo) { u.Suspended = 1 }))
		store.AddUser(user("carol", func(u *meta.UserInfo) { u.System = 1 }))
		store.AddUser(user("dave", func(u *meta.UserInfo) { u.Admin = 1 }))
		store.AddUser(user("t1$bob", nil))
		store.AddUser(user("frank", func(u *meta.UserInfo) { u.AccountID = "RGW00000000000000001" }))
		store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Tenant: "acct-tenant", Name: "acme"})
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		cfg.DNSNames = []string{"s3.example.com"}
		env := &op.Env{Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}, Now: fixedNow}
		return s3.NewHandler(env, auth.New(cfg, store, store), s3.Config{DNSNames: cfg.DNSNames}), store
	}
	serve := func(h http.Handler, req *http.Request) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	// onHost is req sent to host, as a virtual-hosted request names its
	// bucket.
	onHost := func(req *http.Request, host string) *http.Request {
		req.Host = host
		return req
	}
	// answer is a response less its request id, which every request draws
	// anew.
	type answer struct {
		code   int
		body   string
		header http.Header
	}
	answerTo := func(h http.Handler, req *http.Request) answer {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		hdr := rec.Header().Clone()
		delete(hdr, "x-amz-request-id")
		return answer{rec.Code, requestIDElement.ReplaceAllString(rec.Body.String(), ""), hdr}
	}

	// A forged preflight proves only that its sender knows the key's id, so
	// nothing of the key's user may reach its answer: a forged request of any
	// other method, which radosgw and rgw-go answer SignatureDoesNotMatch
	// whoever the key's user is, tells its sender no more.
	DescribeTable("answers a forged preflight alike for every key that resolves",
		func(ctx SpecContext, rel denc.Release) {
			h, _ := newHandler(rel)
			callers := []struct{ name, key, query string }{
				{"a suspended user", accessKeyOf("sue"), ""},
				{"a system user", accessKeyOf("carol"), ""},
				{"a system user naming a user that does not load", accessKeyOf("carol"), "rgwx-uid=nobody"},
				{"a system user naming an account that does not load", accessKeyOf("carol"), "rgwx-uid=RGW99999999999999999"},
				{"a system user naming another tenant's user", accessKeyOf("carol"), "rgwx-uid=t1%24bob"},
				{"an admin", accessKeyOf("dave"), ""},
				{"a tenanted user", accessKeyOf("t1$bob"), ""},
				{"an account user", accessKeyOf("frank"), ""},
			}
			for _, target := range []string{"/plain/k", "/t2:plain/k", "/plain"} {
				for _, f := range signedRoutes {
					for _, c := range callers {
						at := target
						if c.query != "" {
							at = withQuery(target, c.query)
						}
						want := answerTo(h, f.build(ctx, http.MethodOptions, at, preflightHeaders(http.MethodGet), accessKeyOf("alice"), wrongSecret))
						Expect(want.code).To(Equal(http.StatusNotImplemented), "%s %s: %s", f.name, at, want.body)
						got := answerTo(h, f.build(ctx, http.MethodOptions, at, preflightHeaders(http.MethodGet), c.key, wrongSecret))
						Expect(got).To(Equal(want), "%s %s, %s", f.name, at, c.name)

						code, body := serve(h, f.build(ctx, http.MethodGet, at, nil, c.key, wrongSecret))
						Expect(code).To(Equal(http.StatusForbidden), "%s GET %s, %s: %s", f.name, at, c.name, body)
						Expect(body).To(ContainSubstring("<Code>SignatureDoesNotMatch</Code>"), "%s GET %s, %s", f.name, at, c.name)
					}
				}
			}
		},
		Entry("on Squid", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)

	// A bucket token that ends at its colon names no bucket, so radosgw files
	// the request under its identity's owner; a forged preflight's is
	// accounted as an anonymous request's instead, as is everything else of
	// it, while a forged GET files nothing at all.
	usageCase := func(ctx SpecContext, rel denc.Release, target, id string) {
		key, owner := accessKeyOf(id), meta.UserOwner(meta.ParseUserID(id))
		if id == "frank" {
			owner = meta.AccountOwner("RGW00000000000000001")
		}
		for _, f := range signedRoutes {
			h, anonStore := newHandler(rel)
			code, body := serve(h, newRequest(ctx, http.MethodOptions, target, preflightHeaders(http.MethodGet)))
			Expect(code).To(Equal(http.StatusBadRequest), "%s, anonymous: %s", f.name, body)
			want := anonStore.Usage()
			Expect(want).To(HaveLen(1), "%s, anonymous", f.name)
			Expect(want[0].Owner).To(Equal(meta.UserOwner(meta.UserID{ID: op.AnonymousUserID})), "%s, anonymous", f.name)

			h, store := newHandler(rel)
			code, body = serve(h, f.build(ctx, http.MethodOptions, target, preflightHeaders(http.MethodGet), key, wrongSecret))
			Expect(code).To(Equal(http.StatusBadRequest), "%s: %s", f.name, body)
			Expect(body).To(ContainSubstring("<Code>InvalidBucketName</Code>"), f.name)
			got := store.Usage()
			Expect(got).To(Equal(want), "%s", f.name)
			for _, e := range got {
				Expect(e.Owner).NotTo(Equal(owner), "%s files nothing under the key's owner", f.name)
				Expect(e.Payer).NotTo(Equal(owner), "%s charges nothing to the key's owner", f.name)
			}

			h, store = newHandler(rel)
			serve(h, f.build(ctx, http.MethodGet, target, nil, key, wrongSecret))
			Expect(store.Usage()).To(BeEmpty(), "%s, a forged GET", f.name)
		}
	}
	usageEntries := func() []TableEntry {
		var entries []TableEntry
		for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
			for _, target := range []string{"/t:", "/t:/k"} {
				for _, id := range []string{"alice", "t1$bob", "frank", "sue"} {
					entries = append(entries, Entry(fmt.Sprintf("%s, OPTIONS %s as %s", rel, target, id), rel, target, id))
				}
			}
		}
		return entries
	}
	DescribeTable("accounts a forged preflight's usage as an anonymous request's, never under its key's user", usageCase, usageEntries())

	// SigV2 takes Tentacle's method rule on Squid too.
	DescribeTable("answers a signed preflight as radosgw authenticates it, and 501 NotImplemented until CORS serves it",
		func(ctx SpecContext, rel denc.Release) {
			h, _ := newHandler(rel)
			for _, f := range signedRoutes {
				code, body := serve(h, f.build(ctx, http.MethodOptions, "/plain/k", preflightHeaders(http.MethodGet), accessKeyOf("alice"), wrongSecret))
				Expect(code).To(Equal(http.StatusNotImplemented), "%s, a wrong signature: %s", f.name, body)
				Expect(body).To(ContainSubstring("<Code>NotImplemented</Code>"), f.name)

				code, body = serve(h, f.build(ctx, http.MethodOptions, "/plain/k", noMethod(), accessKeyOf("alice"), secretOf("alice")))
				Expect(code).To(Equal(http.StatusBadRequest), "%s, no method: %s", f.name, body)
				Expect(body).To(ContainSubstring("<Code>InvalidArgument</Code>"), f.name)

				code, body = serve(h, f.build(ctx, http.MethodOptions, "/plain/k", preflightHeaders(http.MethodGet), "AKNOBODY", wrongSecret))
				Expect(code).To(Equal(http.StatusForbidden), "%s, an unknown key: %s", f.name, body)
				Expect(body).To(ContainSubstring("<Code>InvalidAccessKeyId</Code>"), f.name)
			}
		},
		Entry("on Squid", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)

	DescribeTable("runs the preflight and no other op for every OPTIONS request it authenticates",
		func(ctx SpecContext, rel denc.Release) {
			h, _ := newHandler(rel)
			var ran []string
			h.Register("options_cors", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
				ran = append(ran, r.Identity.AccessKey)
				w.WriteHeader(http.StatusNoContent)
				return nil
			})
			type target struct{ host, path string }
			var targets []target
			for _, key := range []string{
				"acl", "cors", "delete", "encryption", "lifecycle", "location", "logging", "mdsearch", "notification",
				"object-lock", "policy", "policyStatus", "publicAccessBlock", "replication", "requestPayment", "tagging",
				"uploads", "versioning", "versions", "website", "list-type=2", "usage",
			} {
				targets = append(targets, target{"s3.example.com", "/plain?" + key}, target{"plain.s3.example.com", "/?" + key})
			}
			for _, key := range []string{
				"acl", "attributes", "layout", "legal-hold", "restore", "retention", "select-type=2", "tagging",
				"uploadId=u", "uploads", "partNumber=1&uploadId=u", "versionId=v", "torrent", "append&position=0",
			} {
				targets = append(targets, target{"s3.example.com", "/plain/k?" + key}, target{"plain.s3.example.com", "/k?" + key})
			}
			targets = append(targets,
				target{"s3.example.com", "/plain"}, target{"s3.example.com", "/plain/k"},
				target{"plain.s3.example.com", "/"}, target{"plain.s3.example.com", "/k"})
			overrides := []http.Header{
				preflightHeaders(http.MethodGet),
				{"Access-Control-Request-Method": {http.MethodPut}, "X-Http-Method-Override": {http.MethodPut}, "X-Amz-Copy-Source": {"plain/src"}},
			}
			for _, t := range targets {
				for _, hdr := range overrides {
					for _, f := range signedRoutes {
						ran = nil
						code, body := serve(h, onHost(f.build(ctx, http.MethodOptions, t.path, hdr, accessKeyOf("alice"), wrongSecret), t.host))
						Expect(code).To(Equal(http.StatusNoContent), "%s %s%s %v: %s", f.name, t.host, t.path, hdr, body)
						Expect(ran).To(Equal([]string{accessKeyOf("alice")}), "%s %s%s %v", f.name, t.host, t.path, hdr)
					}
				}
			}
		},
		Entry("on Squid", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)

	// Dispatch refuses these before authentication, so an unknown key is not
	// even looked up.
	DescribeTable("refuses an OPTIONS request no preflight serves before it authenticates",
		func(ctx SpecContext, rel denc.Release) {
			h, _ := newHandler(rel)
			for _, t := range []struct{ host, path string }{
				{"s3.example.com", "/"},
				{"s3.example.com", "/?usage"},
				{"s3.example.com", "/plain?uploadId=u"},
				{"s3.example.com", "/plain?partNumber=1"},
				{"s3.example.com", "/plain?versionId=v"},
				{"s3.example.com", "/plain?append"},
				{"s3.example.com", "/plain?torrent"},
				{"plain.s3.example.com", "/?uploadId=u"},
				{"plain.s3.example.com", "/?versionId=v"},
			} {
				for _, f := range signedRoutes {
					code, body := serve(h, onHost(f.build(ctx, http.MethodOptions, t.path, preflightHeaders(http.MethodGet), "AKNOBODY", wrongSecret), t.host))
					Expect(code).To(Equal(http.StatusMethodNotAllowed), "%s %s%s: %s", f.name, t.host, t.path, body)
				}
			}
		},
		Entry("on Squid", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)

	DescribeTable("refuses an OPTIONS request to the admin API before it authenticates",
		func(ctx SpecContext, rel denc.Release) {
			store := memstore.New(memstore.Config{Release: rel, Now: fixedNow})
			store.AddUser(user("alice", func(u *meta.UserInfo) { u.Caps = meta.Caps{"users": 3, "buckets": 3, "metadata": 3, "usage": 3} }))
			cfg := auth.DefaultConfig()
			cfg.Now = fixedNow
			env := &op.Env{Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
			h := admin.NewHandler(env, auth.New(cfg, store, store), admin.Config{Prefix: "admin"})
			for _, target := range []string{
				"/admin/info", "/admin/config?type=zone", "/admin/user?uid=alice", "/admin/user?key", "/admin/bucket", "/admin/bucket?policy",
				"/admin/metadata/user", "/admin/usage", "/admin/account", "/admin/ratelimit", "/admin/realm", "/admin/realm/period",
			} {
				for _, key := range []string{"AKNOBODY", accessKeyOf("alice")} {
					for _, f := range signedRoutes {
						code, body := serve(h, f.build(ctx, http.MethodOptions, target, preflightHeaders(http.MethodGet), key, wrongSecret))
						Expect(code).To(Equal(http.StatusMethodNotAllowed), "%s %s as %s: %s", f.name, target, key, body)
					}
				}
			}
		},
		Entry("on Squid", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)
})
