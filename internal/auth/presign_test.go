package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// sdkPresign presigns req for 300 seconds over UNSIGNED-PAYLOAD, as
// aws-sdk-go-v2's S3 presign client does, and returns the request a client
// then sends: the presigned URL with the Host the signer signed.
func sdkPresign(ctx context.Context, req *http.Request) *http.Request {
	q := req.URL.Query()
	q.Set("X-Amz-Expires", "300")
	req.URL.RawQuery = q.Encode()
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	u, _, err := signer.PresignHTTP(ctx, testCredentials(), req, unsignedPayload, "s3", "us-east-1", signingTime)
	Expect(err).NotTo(HaveOccurred())
	out := httptest.NewRequestWithContext(ctx, req.Method, u, nil)
	out.Host = req.Host
	return out
}

// withLocalPort is req as it arrives on a listener at port, over TLS when
// secure.
func withLocalPort(req *http.Request, port int, secure bool) *http.Request {
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}))
	if secure {
		req.TLS = &tls.ConnectionState{}
	}
	return req
}

func presignedGet(ctx context.Context, target string) *http.Request {
	return sdkPresign(ctx, httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil))
}

var _ = Describe("SigV4 presigned authentication", func() {
	var cfg Config

	BeforeEach(func() {
		cfg = DefaultConfig()
	})

	It("verifies a URL the reference client presigned, inside its window, without a skew check", func(ctx SpecContext) {
		req := presignedGet(ctx, "http://s3.example.com/bucket/key?response-content-type=text%2Fplain")
		for _, now := range []time.Time{signingTime.Add(299 * time.Second), signingTime.Add(-time.Hour)} {
			d, err := authDataV4(newRequestView(req), true, &cfg, now)
			Expect(err).NotTo(HaveOccurred(), "at %s", now)
			Expect(d.presigned).To(BeTrue())
			Expect(d.payloadHash).To(Equal(unsignedPayload))
			ok, err := d.verify(testSecret)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue(), "the secret the client signed with, at %s", now)
		}
	})

	It("expires at date plus X-Amz-Expires, in whole seconds, with radosgw's message", func(ctx SpecContext) {
		req := presignedGet(ctx, "http://s3.example.com/bucket/key")
		_, err := authDataV4(newRequestView(req), true, &cfg, signingTime.Add(300*time.Second))
		Expect(err).To(MatchError(op.ErrAccessDenied))
		opErr, _ := errors.AsType[*op.Error](err)
		Expect(opErr).To(HaveField("Message", "The pre-signed URL has expired"))
		_, err = authDataV4(newRequestView(req), true, &cfg, signingTime.Add(299*time.Second+999*time.Millisecond))
		Expect(err).NotTo(HaveOccurred())
	})

	It("reads the clock through a double as radosgw does, so a second's last nanoseconds count as the next", func(ctx SpecContext) {
		req := presignedGet(ctx, "http://s3.example.com/bucket/key")
		_, err := authDataV4(newRequestView(req), true, &cfg, signingTime.Add(300*time.Second-10*time.Nanosecond))
		Expect(err).To(MatchError(op.ErrAccessDenied), "299.99999999 s after the date is 300 s")
		_, err = authDataV4(newRequestView(req), true, &cfg, signingTime.Add(300*time.Second-time.Microsecond))
		Expect(err).NotTo(HaveOccurred(), "299.999999 s after the date is 299 s")
	})

	It("accepts a presigned URL signed with host:port arriving with a bare Host (boto2 fallback)", func(ctx SpecContext) {
		signed := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://example.com:8080/bucket/key", nil)
		signed.Host = "example.com:8080"
		req := sdkPresign(ctx, signed)
		req.Host = "example.com" // a port-forward stripped it
		req = withLocalPort(req, 8080, false)
		d, err := authDataV4(newRequestView(req), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).NotTo(BeNil())
		Expect(d.canonicalRequest).To(ContainSubstring("\nhost:example.com\n"), "the plain attempt signs the Host as it came")
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue(), "the plain attempt fails, the host:8080 attempt matches")
	})

	It("has no fallback on the scheme's default port, under TLS on 443, or for header auth", func(ctx SpecContext) {
		req := presignedGet(ctx, "http://example.com/bucket/key")
		d, err := authDataV4(newRequestView(withLocalPort(req, 80, false)), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).To(BeNil(), "port 80 without TLS")
		d, err = authDataV4(newRequestView(withLocalPort(req, 443, true)), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).To(BeNil(), "port 443 with TLS")
		d, err = authDataV4(newRequestView(withLocalPort(req, 80, true)), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).NotTo(BeNil(), "port 80 with TLS")
		d, err = authDataV4(newRequestView(withLocalPort(req, 8443, true)), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).NotTo(BeNil(), "port 8443 with TLS")
		hdr := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/bucket/key", nil)
		sdkSign(ctx, hdr, unsignedPayload)
		d, err = authDataV4(newRequestView(withLocalPort(hdr, 8080, false)), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).To(BeNil(), "header auth")
	})

	It("has no fallback when host is not a signed header", func(ctx SpecContext) {
		cfg.Insecure = true // otherwise the unsigned host token refuses the request first
		req := withLocalPort(rawRequest(ctx, http.MethodGet, "http://example.com/b/k?X-Amz-Algorithm=AWS4-HMAC-SHA256"+
			"&X-Amz-Credential=AKIDEXAMPLE%2F20150830%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20150830T123600Z"+
			"&X-Amz-Expires=300&X-Amz-SignedHeaders=Host&X-Amz-Signature=abc", nil), 8080, false)
		d, err := authDataV4(newRequestView(req), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).To(BeNil(), "the token Host is not host")
	})

	It("still fails when neither the bare host nor host:port matches", func(ctx SpecContext) {
		req := presignedGet(ctx, "http://example.com/bucket/key")
		req.Host = "example.com:8080" // signed bare, arrives with a port: radosgw fails both attempts
		req = withLocalPort(req, 8080, false)
		d, err := authDataV4(newRequestView(req), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).NotTo(BeNil())
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse(), "example.com:8080 and example.com:8080:8080 both differ from example.com")
	})
})

var _ = Describe("parseV4Query", func() {
	const (
		cred = "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIDEXAMPLE%2F20150830%2Fus-east-1%2Fs3%2Faws4_request"
		date = "&X-Amz-Date=" + testDate
		rest = "&X-Amz-Expires=300&X-Amz-SignedHeaders=host&X-Amz-Signature=abc"
	)

	parse := func(ctx context.Context, query string, hdr map[string][]string, now time.Time) (*v4Credentials, error) {
		return parseV4Query(newRequestView(rawRequest(ctx, http.MethodGet, "/b/k?"+query, hdr)), now)
	}

	It("takes every field from the query, the date as sent", func(ctx SpecContext) {
		c, err := parse(ctx, cred+date+rest+"&X-Amz-Security-Token=tok",
			map[string][]string{"X-Amz-Date": {"19700101T000000Z"}, "X-Amz-Security-Token": {"header"}}, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(*c).To(Equal(v4Credentials{
			accessKey: testAccessKey, scope: testScope, signedHeaders: "host", signature: "abc",
			date: testDate, sessionToken: "tok",
		}))
	})

	// The credential is parse_v4_query_string's first check, EPERM at both
	// tags (rgw_auth_s3.cc:290-293 at v19.2.6, :293-296 at v20.2.4), which is
	// 403 AccessDenied (rgw_common.cc:89 at v19.2.6, :90 at v20.2.4).
	DescribeTable("refuses a missing or empty X-Amz-Credential before anything else, with AccessDenied",
		func(ctx SpecContext, query string) {
			_, err := parse(ctx, query, nil, signingTime.Add(time.Hour))
			Expect(err).To(MatchError(op.ErrAccessDenied))
			opErr, _ := errors.AsType[*op.Error](err)
			Expect(opErr).To(HaveField("Message", ""), "not the expiry the URL also has")
		},
		Entry("missing", "X-Amz-Algorithm=AWS4-HMAC-SHA256"+date+rest),
		Entry("empty", "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential="+date+rest),
	)

	It("finds a parameter only under the spelling RGWHTTPArgs lower-cases", func(ctx SpecContext) {
		_, err := parse(ctx, cred+date+"&X-AMZ-Expires=300&X-Amz-SignedHeaders=host&X-Amz-Signature=abc", nil, signingTime)
		Expect(err).To(MatchError(op.ErrAccessDenied), "X-AMZ-Expires is not x-amz-expires")
		_, err = parse(ctx, cred+date+"&x-amz-expires=300&X-Amz-SignedHeaders=host&X-Amz-Signature=abc", nil, signingTime)
		Expect(err).NotTo(HaveOccurred(), "x-amz-expires is")
	})

	It("refuses an expired URL before it reads the signed headers, the signature or the token", func(ctx SpecContext) {
		_, err := parse(ctx, cred+date+"&X-Amz-Expires=300&X-Amz-Security-Token=", nil, signingTime.Add(time.Hour))
		opErr, _ := errors.AsType[*op.Error](err)
		Expect(opErr).To(HaveField("Message", "The pre-signed URL has expired"))
	})

	It("checks the credential's shape last, as InvalidArgument", func(ctx SpecContext) {
		bad := "X-Amz-Credential=AKIDEXAMPLE%2F20150830" + date
		_, err := parse(ctx, bad+"&X-Amz-Expires=300&X-Amz-SignedHeaders=host", nil, signingTime)
		Expect(err).To(MatchError(op.ErrAccessDenied), "no signature")
		_, err = parse(ctx, bad+rest, nil, signingTime)
		Expect(err).To(MatchError(op.ErrInvalidArgument), "a malformed credential")
	})

	DescribeTable("adds the expiry in uint64_t as radosgw does",
		func(ctx SpecContext, query string, expired bool) {
			_, err := parse(ctx, query, nil, signingTime)
			if expired {
				Expect(err).To(MatchError(op.ErrAccessDenied))
				return
			}
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("a date before 1970 more than the expiry wraps past now", cred+"&X-Amz-Date=19691231T000000Z"+rest, false),
		Entry("a date before 1970 by less than the expiry wraps to a small sum", cred+"&X-Amz-Date=19691231T235959Z"+rest, true),
		Entry("a date at 1970 is in the past", cred+"&X-Amz-Date=19700101T000000Z"+rest, true),
	)
})
