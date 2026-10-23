package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
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

	It("builds a preflight's canonical request over its Access-Control-Request-Method", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodOptions, "http://s3.example.com/bucket/key", nil)
		req.Header.Set("Access-Control-Request-Method", http.MethodPut)
		sdkSign(ctx, req, unsignedPayload)
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.canonicalRequest).To(HavePrefix("PUT\n/bucket/key\n"))

		pre := sdkPresign(ctx, httptest.NewRequestWithContext(ctx, http.MethodOptions, "http://s3.example.com/bucket/key", nil))
		pre.Header.Set("Access-Control-Request-Method", http.MethodDelete)
		d, err = authDataV4(newRequestView(pre), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.canonicalRequest).To(HavePrefix("DELETE\n/bucket/key\n"), "presigned")

		get := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/bucket/key", nil)
		get.Header.Set("Access-Control-Request-Method", http.MethodPut)
		sdkSign(ctx, get, unsignedPayload)
		d, err = authDataV4(newRequestView(get), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.canonicalRequest).To(HavePrefix("GET\n"), "only an OPTIONS request takes the header's method")
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

// attackHeaders are the x-amz-* headers upstream's CVE-2026-54330 guard
// refuses unsigned
// (https://github.com/ceph/ceph/blob/v19.2.7/src/test/rgw/test_rgw_auth_sigv4.cc#L141-L184 at v19.2.7),
// each a way for the holder of a signature to act with authority its signer
// never gave, and a name that only starts with the one header the rule
// exempts.
var attackHeaders = []string{
	"X-Amz-Acl", "X-Amz-Copy-Source", "X-Amz-Grant-Full-Control", "X-Amz-Grant-Read", "X-Amz-Grant-Write",
	"X-Amz-Grant-Read-Acp", "X-Amz-Grant-Write-Acp", "X-Amz-Meta-Injected", "X-Amz-Metadata-Directive",
	"X-Amz-Security-Token", "X-Amz-Bypass-Governance-Retention", "X-Amz-Object-Lock-Mode",
	"X-Amz-Object-Lock-Retain-Until-Date", "X-Amz-Object-Lock-Legal-Hold", "X-Amz-Server-Side-Encryption",
	"X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id", "X-Amz-Server-Side-Encryption-Customer-Algorithm",
	"X-Amz-Server-Side-Encryption-Customer-Key", "X-Amz-Server-Side-Encryption-Customer-Key-Md5",
	"X-Amz-Decoded-Content-Length", "X-Amz-Trailer", "X-Amz-Website-Redirect-Location", "X-Amz-Tagging",
	"X-Amz-Tagging-Directive", "X-Amz-Storage-Class", "X-Amz-Checksum-Crc32c", "X-Amz-Content-Sha256-Extra",
}

func attackHeaderEntries() []TableEntry {
	entries := make([]TableEntry, 0, len(attackHeaders))
	for _, h := range attackHeaders {
		entries = append(entries, Entry(h, h))
	}
	return entries
}

// renamedPrefixes are the meta prefixes beside X-Amz- that radosgw's
// init_meta_info files under x-amz- names
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L413-L464 at v19.2.7)
// and rgw-go reads as ordinary headers.
var renamedPrefixes = []string{"X-Goog-", "X-Dho-", "X-Rgw-", "X-Object-", "X-Container-", "X-Account-"}

func renamedPrefixEntries() []TableEntry {
	entries := make([]TableEntry, 0, len(renamedPrefixes))
	for _, p := range renamedPrefixes {
		entries = append(entries, Entry(p, p))
	}
	return entries
}

// signedReq is a method request for an object carrying hdr, signed with
// SigV4 header auth.
func signedReq(ctx context.Context, method string, hdr map[string]string) *http.Request {
	req := httptest.NewRequestWithContext(ctx, method, "http://s3.example.com/bucket/key", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	sdkSign(ctx, req, unsignedPayload)
	return req
}

// presignedReq is a method request for an object carrying hdr, presigned as
// sdkPresign presigns it, as a client sends it: to the URL, with hdr's
// headers.
func presignedReq(ctx context.Context, method string, hdr map[string]string) *http.Request {
	src := httptest.NewRequestWithContext(ctx, method, "http://s3.example.com/bucket/key", nil)
	for k, v := range hdr {
		src.Header.Set(k, v)
	}
	req := sdkPresign(ctx, src)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req
}

// routed is a request and whether authDataV4 takes it as presigned.
type routed struct {
	presigned bool
	req       *http.Request
}

// onBothRoutes is a method request carrying hdr, header-signed and
// presigned.
func onBothRoutes(ctx context.Context, method string, hdr map[string]string) []routed {
	return []routed{{false, signedReq(ctx, method, hdr)}, {true, presignedReq(ctx, method, hdr)}}
}

// helloHash is sha256("hello").
const helloHash = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

// signOverHash signs req with SigV4 header auth over the payload hash hash
// while req carries no x-amz-content-sha256 header, which leaves the header
// out of SignedHeaders, as the signer of s3-tests'
// test_sigv4_without_signed_content_sha256 does. It then sends each of sent
// as one x-amz-content-sha256 value, in order.
func signOverHash(ctx context.Context, req *http.Request, creds aws.Credentials, t time.Time, hash string, sent ...string) *http.Request {
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	Expect(signer.SignHTTP(ctx, creds, req, hash, "s3", "us-east-1", t)).To(Succeed())
	Expect(req.Header.Get("Authorization")).NotTo(ContainSubstring(payloadHashHeader))
	for _, v := range sent {
		req.Header.Add("X-Amz-Content-Sha256", v)
	}
	return req
}

var _ = Describe("the signed-header rule", func() {
	var cfg Config

	BeforeEach(func() {
		cfg = DefaultConfig()
	})

	// A repeated header is read by its last value alone, for the signature
	// and the completer alike, so a value prepended to a captured request
	// neither skips nor reframes the body check.
	DescribeTable("takes an unsigned x-amz-content-sha256 as the payload hash, which the signature covers",
		func(ctx SpecContext, sent []string, verifies bool, kind payloadKind) {
			req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/bucket/key", strings.NewReader("hello"))
			req.Header.Set("Content-Length", "5")
			signOverHash(ctx, req, testCredentials(), signingTime, helloHash, sent...)
			d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.canonicalRequest).NotTo(ContainSubstring(payloadHashHeader+":"), "the header is not a canonical header")
			Expect(d.canonicalRequest).To(HaveSuffix("\n" + d.payloadHash))
			Expect(d.payload.kind).To(Equal(kind), "the completer the payload hash picks")
			Expect(d.verify(testSecret)).To(Equal(verifies))
		},
		Entry("the hash it was signed over", []string{helloHash}, true, payloadSingle),
		Entry("UNSIGNED-PAYLOAD, which would skip the body check", []string{unsignedPayload}, false, payloadNone),
		Entry("another body's hash", []string{sha256Hex("jello")}, false, payloadSingle),
		Entry("an aws-chunked trailer form, which would reframe the body", []string{streamingUnsignedTrailer}, false, payloadChunked),
		Entry("none, which is UNSIGNED-PAYLOAD", nil, false, payloadNone),
		Entry("UNSIGNED-PAYLOAD prepended to the signed hash", []string{unsignedPayload, helloHash}, true, payloadSingle),
		Entry("UNSIGNED-PAYLOAD appended to the signed hash", []string{helloHash, unsignedPayload}, false, payloadNone),
	)

	DescribeTable("holds a presigned request's unsigned x-amz-content-sha256 to the UNSIGNED-PAYLOAD its URL signs",
		func(ctx SpecContext, sent []string, verifies bool, kind payloadKind) {
			req := sdkPresign(ctx, httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/bucket/key", nil))
			req.Body, req.ContentLength = io.NopCloser(strings.NewReader("hello")), 5
			for _, v := range sent {
				req.Header.Add("X-Amz-Content-Sha256", v)
			}
			d, err := authDataV4(newRequestView(req), true, &cfg, signingTime)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.payloadHash).To(Equal(sent[len(sent)-1]))
			Expect(d.payload.kind).To(Equal(kind), "the completer the payload hash picks")
			Expect(d.verify(testSecret)).To(Equal(verifies))
		},
		Entry("UNSIGNED-PAYLOAD", []string{unsignedPayload}, true, payloadNone),
		Entry("a body hash, which would hold the body to another value", []string{helloHash}, false, payloadSingle),
		Entry("an aws-chunked trailer form, which would reframe the body", []string{streamingUnsignedTrailer}, false, payloadChunked),
		Entry("an aws-chunked trailer form prepended to UNSIGNED-PAYLOAD", []string{streamingUnsignedTrailer, unsignedPayload}, true, payloadNone),
		Entry("an aws-chunked trailer form appended to UNSIGNED-PAYLOAD", []string{unsignedPayload, streamingUnsignedTrailer}, false, payloadChunked),
	)

	It("accepts an unsigned Content-Type and holds a signed one to its value", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/bucket/key", nil)
		sdkSign(ctx, req, unsignedPayload)
		req.Header.Set("Content-Type", "application/octet-stream")
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.canonicalRequest).NotTo(ContainSubstring("content-type"))
		Expect(d.verify(testSecret)).To(BeTrue())

		pre := presignedGet(ctx, "http://s3.example.com/bucket/key")
		pre.Header.Set("Content-Type", "application/octet-stream")
		d, err = authDataV4(newRequestView(pre), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.verify(testSecret)).To(BeTrue(), "a presigned URL signs host alone")

		signed := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/bucket/key", nil)
		signed.Header.Set("Content-Type", "application/octet-stream")
		sdkSign(ctx, signed, unsignedPayload)
		signed.Header.Set("Content-Type", "text/html")
		d, err = authDataV4(newRequestView(signed), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.canonicalRequest).To(ContainSubstring("\ncontent-type:text/html\n"))
		Expect(d.verify(testSecret)).To(BeFalse(), "a signed Content-Type changed after signing")
	})

	DescribeTable("refuses any other unsigned x-amz header on both routes, beside an unsigned Content-Type or not",
		func(ctx SpecContext, name string) {
			for _, ct := range []string{"", "application/json"} {
				hdr := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/bucket/key", nil)
				sdkSign(ctx, hdr, unsignedPayload)
				pre := presignedGet(ctx, "http://s3.example.com/bucket/key")
				for _, req := range []*http.Request{hdr, pre} {
					req.Header.Set(name, "attacker-supplied")
					if ct != "" {
						req.Header.Set("Content-Type", ct)
					}
				}
				_, err := authDataV4(newRequestView(hdr), false, &cfg, signingTime)
				Expect(err).To(MatchError(op.ErrAccessDenied), "header route, Content-Type %q", ct)
				_, err = authDataV4(newRequestView(pre), true, &cfg, signingTime)
				Expect(err).To(MatchError(op.ErrAccessDenied), "presigned route, Content-Type %q", ct)
			}
		},
		attackHeaderEntries(),
	)

	// Whoever holds a signed request or a presigned URL could otherwise add
	// x-amz-mfa and spend a check of the requester's device with each send.
	It("holds x-amz-mfa to the signature on both routes, and takes it unsigned only under rgw_sigv4_insecure", func(ctx SpecContext) {
		const mfa = "SERIAL 123456"
		for _, c := range onBothRoutes(ctx, http.MethodPut, map[string]string{"X-Amz-Mfa": mfa}) {
			d, err := authDataV4(newRequestView(c.req), c.presigned, &cfg, signingTime)
			Expect(err).NotTo(HaveOccurred(), "signed, presigned %t", c.presigned)
			Expect(d.canonicalRequest).To(ContainSubstring("\nx-amz-mfa:"+mfa+"\n"), "signed, presigned %t", c.presigned)
			Expect(d.verify(testSecret)).To(BeTrue(), "signed, presigned %t", c.presigned)
		}
		insecure := cfg
		insecure.Insecure = true
		for _, c := range onBothRoutes(ctx, http.MethodPut, nil) {
			c.req.Header.Set("X-Amz-Mfa", mfa)
			_, err := authDataV4(newRequestView(c.req), c.presigned, &cfg, signingTime)
			Expect(err).To(MatchError(op.ErrAccessDenied), "added after signing, presigned %t", c.presigned)
			d, err := authDataV4(newRequestView(c.req), c.presigned, &insecure, signingTime)
			Expect(err).NotTo(HaveOccurred(), "added under rgw_sigv4_insecure, presigned %t", c.presigned)
			Expect(d.verify(testSecret)).To(BeTrue(), "radosgw's insecure mode signs around it, presigned %t", c.presigned)
		}
	})

	// radosgw files each under its x-amz- name beside the signed header,
	// overriding or joining its value; rgw-go reads none of them.
	DescribeTable("accepts an unsigned header under a prefix radosgw renames to x-amz-, beside the signed x-amz- one, on both routes",
		func(ctx SpecContext, prefix string) {
			for _, suffix := range []string{"Meta-Foo", "Server-Side-Encryption", "Acl", "Copy-Source", "Tagging"} {
				name, amz := prefix+suffix, "X-Amz-"+suffix
				for _, c := range onBothRoutes(ctx, http.MethodPut, map[string]string{amz: "signed"}) {
					c.req.Header.Set(name, "unsigned")
					d, err := authDataV4(newRequestView(c.req), c.presigned, &cfg, signingTime)
					Expect(err).NotTo(HaveOccurred(), "%s unsigned, presigned %t", name, c.presigned)
					Expect(d.canonicalRequest).NotTo(ContainSubstring(asciiLower(name)), "%s is no canonical header, presigned %t", name, c.presigned)
					Expect(d.verify(testSecret)).To(BeTrue(), "%s unsigned, presigned %t", name, c.presigned)
				}
			}
		},
		renamedPrefixEntries(),
	)

	DescribeTable("signs a header under a prefix radosgw renames to x-amz- that SignedHeaders lists, as any other, on both routes",
		func(ctx SpecContext, prefix string) {
			for _, suffix := range []string{"Meta-Foo", "Server-Side-Encryption"} {
				name := prefix + suffix
				for _, c := range onBothRoutes(ctx, http.MethodPut, map[string]string{name: "signed"}) {
					d, err := authDataV4(newRequestView(c.req), c.presigned, &cfg, signingTime)
					Expect(err).NotTo(HaveOccurred(), "%s signed, presigned %t", name, c.presigned)
					Expect(d.canonicalRequest).To(ContainSubstring("\n"+asciiLower(name)+":signed\n"), "presigned %t", c.presigned)
					Expect(d.verify(testSecret)).To(BeTrue(), "%s signed, presigned %t", name, c.presigned)
					c.req.Header.Set(name, "changed")
					d, err = authDataV4(newRequestView(c.req), c.presigned, &cfg, signingTime)
					Expect(err).NotTo(HaveOccurred(), "%s changed, presigned %t", name, c.presigned)
					Expect(d.verify(testSecret)).To(BeFalse(), "%s changed after signing, presigned %t", name, c.presigned)
				}
			}
		},
		renamedPrefixEntries(),
	)

	It("takes the payload hash from x-amz-content-sha256 alone, under none of the renamed prefixes", func(ctx SpecContext) {
		for _, prefix := range renamedPrefixes {
			name := prefix + "Content-Sha256"
			hdr := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/bucket/key", strings.NewReader("hello"))
			hdr.Header.Set("Content-Length", "5")
			signOverHash(ctx, hdr, testCredentials(), signingTime, helloHash, helloHash)
			pre := presignedReq(ctx, http.MethodPut, nil)
			pre.Header.Set("X-Amz-Content-Sha256", unsignedPayload)
			for _, c := range []routed{{false, hdr}, {true, pre}} {
				want := c.req.Header.Get("X-Amz-Content-Sha256")
				c.req.Header.Set(name, sha256Hex("jello"))
				d, err := authDataV4(newRequestView(c.req), c.presigned, &cfg, signingTime)
				Expect(err).NotTo(HaveOccurred(), "%s unsigned, presigned %t", name, c.presigned)
				Expect(d.payloadHash).To(Equal(want), "%s, presigned %t", name, c.presigned)
				Expect(d.verify(testSecret)).To(BeTrue(), "%s, presigned %t", name, c.presigned)
			}
		}
	})

	// rgw-cache adds X-Rgw-Auth unsigned to the authentication subrequest it
	// sends for a client's GET. radosgw would store it on a write as
	// user.rgw.x-amz-auth; rgw-go reads it on no method.
	DescribeTable("accepts an unsigned x-rgw-auth, in any case, on both routes",
		func(ctx SpecContext, method string) {
			for _, spelling := range []string{"X-Rgw-Auth", "x-rgw-auth", "X-RGW-AUTH"} {
				for _, c := range onBothRoutes(ctx, method, nil) {
					c.req.Header[spelling] = []string{"yes"}
					d, err := authDataV4(newRequestView(c.req), c.presigned, &cfg, signingTime)
					Expect(err).NotTo(HaveOccurred(), "%s, presigned %t", spelling, c.presigned)
					Expect(d.canonicalRequest).NotTo(ContainSubstring("x-rgw-auth"), "it is not a canonical header")
					Expect(d.verify(testSecret)).To(BeTrue(), "%s, presigned %t", spelling, c.presigned)
				}
			}
		},
		Entry("GET, which rgw-cache sends it on", http.MethodGet),
		Entry("PUT, a write", http.MethodPut),
	)

	It("is the boto2 fallback's rule too", func(ctx SpecContext) {
		signed := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://example.com:8080/bucket/key", nil)
		signed.Host = "example.com:8080"
		req := sdkPresign(ctx, signed)
		req.Host = "example.com"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Amz-Content-Sha256", unsignedPayload)
		d, err := authDataV4(newRequestView(withLocalPort(req, 8080, false)), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).NotTo(BeNil(), "the second canonicalization accepts what the first did")
		Expect(d.verify(testSecret)).To(BeTrue(), "through the host:8080 attempt")
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
