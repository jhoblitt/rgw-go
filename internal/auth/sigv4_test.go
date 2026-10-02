package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

const (
	testAccessKey = "AKIDEXAMPLE"
	testSecret    = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testScope     = "20150830/us-east-1/s3/aws4_request"
	testDate      = "20150830T123600Z"
)

var signingTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

func testCredentials() aws.Credentials {
	return aws.Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecret}
}

// sdkSign signs req the way aws-sdk-go-v2's S3 client does: its
// ContentSHA256Header middleware sends the payload hash as
// x-amz-content-sha256 before SignHTTP runs, the path is escaped once, the
// service is s3 and the region us-east-1.
func sdkSign(ctx context.Context, req *http.Request, payloadHash string) {
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	Expect(signer.SignHTTP(ctx, testCredentials(), req, payloadHash, "s3", "us-east-1", signingTime)).To(Succeed())
}

// goCephSign signs req the way go-ceph's rgw/admin client does
// (rgw/admin/radosgw.go): a default signer over UNSIGNED-PAYLOAD, with no
// x-amz-content-sha256 header.
func goCephSign(ctx context.Context, req *http.Request) {
	Expect(v4.NewSigner().SignHTTP(ctx, testCredentials(), req, unsignedPayload, "s3", "us-east-1", signingTime)).To(Succeed())
}

func v4Request(ctx context.Context, authorization string, hdr map[string][]string) *http.Request {
	req := rawRequest(ctx, http.MethodGet, "http://example.amazonaws.com/", hdr)
	req.Header.Set("Authorization", authorization)
	return req
}

func amzDate() map[string][]string { return map[string][]string{"X-Amz-Date": {testDate}} }

func fixedSign(sig string) func(string) (string, error) {
	return func(string) (string, error) { return sig, nil }
}

var _ = Describe("SigV4 header authentication", func() {
	var cfg Config

	BeforeEach(func() {
		cfg = DefaultConfig()
	})

	It("verifies a request the reference client signed", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/bucket/key?list-type=2&prefix=a%2Fb&max-keys=10", nil)
		sdkSign(ctx, req, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.accessKey).To(Equal(testAccessKey))
		Expect(d.v4.scope).To(Equal(testScope))
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue(), "the secret the client signed with")
		ok, err = d.verify("not-the-secret")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse(), "a different secret")
	})

	It("verifies a PUT with metadata, content-type and a signed payload hash", func(ctx SpecContext) {
		body := "hello"
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/bucket/key", strings.NewReader(body))
		// The SDK signs content-length from req.ContentLength; net/http's
		// server keeps the header that carried it, which httptest leaves out.
		req.Header.Set("Content-Length", strconv.Itoa(len(body)))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Amz-Meta-Color", "  navy   blue ")
		sum := sha256.Sum256([]byte(body))
		sdkSign(ctx, req, hex.EncodeToString(sum[:]))
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.payloadHash).To(Equal(hex.EncodeToString(sum[:])))
		Expect(d.canonicalRequest).To(ContainSubstring("x-amz-meta-color:navy blue\n"))
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue(), "the secret the client signed with")
	})

	It("takes a missing x-amz-content-sha256 as UNSIGNED-PAYLOAD, as go-ceph's admin client relies on", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/admin/user?uid=alice", nil)
		goCephSign(ctx, req)
		Expect(req.Header).NotTo(HaveKey("X-Amz-Content-Sha256"))
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.payloadHash).To(Equal(unsignedPayload))
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue(), "the secret the client signed with")
	})

	It("rejects an unsigned x-amz header unless rgw_sigv4_insecure", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/bucket/key", nil)
		sdkSign(ctx, req, unsignedPayload)
		req.Header.Set("X-Amz-Meta-Injected", "after signing")
		_, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		insecure := cfg
		insecure.Insecure = true
		_, err = authDataV4(newRequestView(req), false, &insecure, signingTime)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects a skewed request with RequestTimeTooSkewed", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/", nil)
		sdkSign(ctx, req, unsignedPayload)
		_, err := authDataV4(newRequestView(req), false, &cfg, signingTime.Add(15*time.Minute+time.Second))
		Expect(err).To(MatchError(op.ErrRequestTimeTooSkewed))
		_, err = authDataV4(newRequestView(req), false, &cfg, signingTime.Add(-15*time.Minute))
		Expect(err).NotTo(HaveOccurred())
	})

	It("derives the signature AWS's algorithm derives", func() {
		creq := "GET\n/\n\nhost:example.amazonaws.com\nx-amz-date:20150830T123600Z\n\nhost;x-amz-date\nUNSIGNED-PAYLOAD"
		Expect(signatureV4(signingKeyV4(testSecret, testScope), stringToSignV4(testDate, testScope, creq))).
			To(Equal("e466c56105faeb99883599d048aacd655397ca4edbe9d383f9076947bedcd780"), "computed with Python's hmac and hashlib")
	})

	It("takes a scope field without a following slash as the rest of the scope, as parse_cred_scope does", func() {
		Expect(signingKeyV4(testSecret, "20150830/us-east-1")).To(Equal(signingKeyV4(testSecret, "20150830/us-east-1/us-east-1/aws4_request")),
			"npos + 1 wraps to 0, so the service is the region again")
	})
})

var _ = Describe("parseV4Header", func() {
	const cred = "Credential=AKIDEXAMPLE/" + testScope

	It("keeps the last of a repeated key and ignores unknown keys", func(ctx SpecContext) {
		c, err := parseV4Header(newRequestView(v4Request(ctx,
			"AWS4-HMAC-SHA256 "+cred+", SignedHeaders=host, Signature=aaa, Signature=bbb, Extra=x", amzDate())), signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.signature).To(Equal("bbb"))
		Expect(c.signedHeaders).To(Equal("host"))
	})

	It("trims each key and value and drops empty tokens", func(ctx SpecContext) {
		c, err := parseV4Header(newRequestView(v4Request(ctx,
			"AWS4-HMAC-SHA256 ,,"+cred+" ,\tSignedHeaders = host;x-amz-date ,Signature=abc,", amzDate())), signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.accessKey).To(Equal(testAccessKey))
		Expect(c.scope).To(Equal(testScope))
		Expect(c.signedHeaders).To(Equal("host;x-amz-date"))
		Expect(c.signature).To(Equal("abc"))
	})

	It("skips whatever byte follows the algorithm name", func(ctx SpecContext) {
		c, err := parseV4Header(newRequestView(v4Request(ctx, "AWS4-HMAC-SHA256,"+cred+",SignedHeaders=host,Signature=abc", amzDate())), signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.signature).To(Equal("abc"))
	})

	It("refuses an Authorization of the algorithm name alone", func(ctx SpecContext) {
		_, err := parseV4Header(newRequestView(v4Request(ctx, "AWS4-HMAC-SHA256", amzDate())), signingTime)
		Expect(err).To(MatchError(op.ErrInvalidArgument))
	})

	It("does not fall back to Date when X-Amz-Date is present but malformed", func(ctx SpecContext) {
		_, err := parseV4Header(newRequestView(v4Request(ctx, "AWS4-HMAC-SHA256 "+cred+", SignedHeaders=host, Signature=abc",
			map[string][]string{"X-Amz-Date": {"Sun, 30 Aug 2015 12:36:00 GMT"}, "Date": {testDate}})), signingTime)
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})

	It("checks the date and its skew before the credential's shape", func(ctx SpecContext) {
		bad := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830, SignedHeaders=host, Signature=abc"
		_, err := parseV4Header(newRequestView(v4Request(ctx, bad, nil)), signingTime)
		Expect(err).To(MatchError(op.ErrAccessDenied), "no date")
		_, err = parseV4Header(newRequestView(v4Request(ctx, bad, amzDate())), signingTime.Add(time.Hour))
		Expect(err).To(MatchError(op.ErrRequestTimeTooSkewed), "a skewed date")
		_, err = parseV4Header(newRequestView(v4Request(ctx, bad, amzDate())), signingTime)
		Expect(err).To(MatchError(op.ErrInvalidArgument), "a good date")
	})

	It("keeps the session token", func(ctx SpecContext) {
		c, err := parseV4Header(newRequestView(v4Request(ctx, "AWS4-HMAC-SHA256 "+cred+", SignedHeaders=host, Signature=abc",
			map[string][]string{"X-Amz-Date": {testDate}, "X-Amz-Security-Token": {"tok"}})), signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.sessionToken).To(Equal("tok"))
	})
})

var _ = Describe("canonicalHeadersV4", func() {
	It("appends the port for the boto2 fallback to host only", func(ctx SpecContext) {
		rv := newRequestView(rawRequest(ctx, http.MethodGet, "http://example.amazonaws.com/", map[string][]string{"X-Amz-Date": {testDate}}))
		h, err := canonicalHeadersV4(rv, "host;x-amz-date", false, "8080")
		Expect(err).NotTo(HaveOccurred())
		Expect(h).To(Equal("host:example.amazonaws.com:8080\nx-amz-date:" + testDate + "\n"))
	})

	It("matches the content-md5 token ignoring case, as its HTTP_ name does", func(ctx SpecContext) {
		rv := newRequestView(rawRequest(ctx, http.MethodGet, "http://example.amazonaws.com/", map[string][]string{"Content-Md5": {"!!!"}}))
		_, err := canonicalHeadersV4(rv, "Content-MD5;host", true, "")
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})

	It("keys each header by its token as sent", func(ctx SpecContext) {
		rv := newRequestView(rawRequest(ctx, http.MethodGet, "http://example.amazonaws.com/", nil))
		_, err := canonicalHeadersV4(rv, "Host", false, "")
		Expect(err).To(MatchError(op.ErrAccessDenied), "the host check looks for the token host")
		h, err := canonicalHeadersV4(rv, "Host", true, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(h).To(Equal("Host:example.amazonaws.com\n"))
	})

	It("does not take an x_amz_ header for an x-amz- one", func(ctx SpecContext) {
		rv := newRequestView(rawRequest(ctx, http.MethodGet, "http://example.amazonaws.com/", map[string][]string{"X_amz_meta": {"v"}}))
		h, err := canonicalHeadersV4(rv, "host", false, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(h).To(Equal("host:example.amazonaws.com\n"))
	})
})

var _ = Describe("canonicalQueryV4", func() {
	DescribeTable("is get_v4_canonical_qs",
		func(raw string, presigned bool, want string) {
			Expect(canonicalQueryV4(raw, presigned)).To(Equal(want), "%q", raw)
		},
		Entry("empty", "", false, ""),
		Entry("empty tokens dropped", "&&a=1&&", false, "a=1"),
		Entry("a pair is trimmed", " a = 1 ", false, "a=1"),
		Entry("a bare key keeps its spaces", " a ", false, "%20a%20="),
		Entry("split at the first =", "a=b=c", false, "a=b%3Dc"),
		Entry("X-Amz-Signature kept when not presigned", "X-Amz-Signature=abc&a=1", false, "X-Amz-Signature=abc&a=1"),
		Entry("X-Amz-Signature dropped ignoring ASCII case when presigned", "x-amz-signature=abc&a=1", true, "a=1"),
		Entry("only ASCII case is folded, so a long s is no s", "X-Amz-\u017fignature=abc", true, "X-Amz-%C5%BFignature=abc"),
		Entry("sorted by key alone, so equal keys keep their order", "b=1&a=2&a=1", false, "a=2&a=1&b=1"),
	)
})

var _ = Describe("authData.verify", func() {
	It("falls back to the alternative signature", func() {
		d := &authData{signature: "alt", sign: fixedSign("main"), altSign: fixedSign("alt")}
		Expect(d.verify(testSecret)).To(BeTrue(), "the fallback matches")
		d.altSign = fixedSign("other")
		Expect(d.verify(testSecret)).To(BeFalse(), "neither matches")
		d.altSign = nil
		Expect(d.verify(testSecret)).To(BeFalse(), "no fallback")
	})
})

var _ = Describe("timeSkewOK", func() {
	It("allows fifteen minutes either way", func() {
		Expect(timeSkewOK(signingTime, signingTime.Add(authGrace))).To(BeTrue(), "fifteen minutes ahead")
		Expect(timeSkewOK(signingTime, signingTime.Add(-authGrace))).To(BeTrue(), "fifteen minutes behind")
		Expect(timeSkewOK(signingTime, signingTime.Add(authGrace+time.Nanosecond))).To(BeFalse(), "just over ahead")
		Expect(timeSkewOK(signingTime, signingTime.Add(-authGrace-time.Nanosecond))).To(BeFalse(), "just over behind")
	})
})
