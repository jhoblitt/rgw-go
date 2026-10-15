package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// v2Sign is a client's SigV2 signature: base64 of HMAC-SHA1 over the string
// to sign.
func v2Sign(secret, sts string) string {
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(sts))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

const v2TestDate = "Sun, 30 Aug 2015 12:36:00 GMT"

var _ = Describe("SigV2", func() {
	var cfg Config

	BeforeEach(func() {
		cfg = DefaultConfig()
	})

	It("verifies a header-signed request and rejects a wrong secret", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/b/k?acl", nil)
		req.Header.Set("Date", v2TestDate)
		sts := "GET\n\n\n" + v2TestDate + "\n/b/k?acl"
		req.Header.Set("Authorization", "AWS "+testAccessKey+":"+v2Sign(testSecret, sts))
		d, err := authDataV2(newRequestView(req), &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.accessKey).To(Equal(testAccessKey))
		Expect(d.stringToSign).To(Equal(sts))
		Expect(d.presigned).To(BeFalse())
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue(), "the secret the client signed with")
		ok, err = d.verify("wrong")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse(), "another secret")
		_, err = d.verify("")
		Expect(err).To(MatchError(op.ErrInvalidArgument), "get_v2_signature throws EINVAL for an empty secret")
	})

	DescribeTable("splits the Authorization header at its last colon",
		func(ctx SpecContext, credentials, wantKey, wantSig string) {
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/b/k", nil)
			req.Header.Set("Date", v2TestDate)
			req.Header.Set("Authorization", "AWS "+credentials)
			d, err := authDataV2(newRequestView(req), &cfg, signingTime)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.accessKey).To(Equal(wantKey))
			Expect(d.signature).To(Equal(wantSig))
		},
		Entry("a colon in the key stays in the key", "a:b:c", "a:b", "c"),
		Entry("no colon leaves both empty", "abc", "", ""),
	)

	It("refuses an empty x-amz-security-token header", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/b/k", nil)
		req.Header.Set("Date", v2TestDate)
		req.Header.Set("X-Amz-Security-Token", "")
		req.Header.Set("Authorization", "AWS "+testAccessKey+":sig")
		_, err := authDataV2(newRequestView(req), &cfg, signingTime)
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})

	It("refuses a Date header far in the future as skewed", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/b/k", nil)
		req.Header.Set("Date", "Wed, 06 Oct 2151 19:04:16 GMT")
		req.Header.Set("Authorization", "AWS "+testAccessKey+":sig")
		_, err := authDataV2(newRequestView(req), &cfg, signingTime)
		Expect(err).To(MatchError(op.ErrRequestTimeTooSkewed))
	})

	It("adds the query's meta and token after the headers' on the query route, which an empty Authorization takes",
		func(ctx SpecContext) {
			target := "/b/k?x-amz-meta-b=2&X-Amz-Meta-A=1&X-AMZ-META-B=3&x-amz-security-token=tok" +
				"&AWSAccessKeyId=AK&Expires=1441000000&Signature=sig%2B%2F%3D"
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com"+target, nil)
			req.Header.Set("Authorization", "")
			req.Header.Set("X-Amz-Meta-A", "h")
			req.Header.Set("X-Amz-Meta-Z", "h")
			d, err := authDataV2(newRequestView(req), &cfg, time.Date(2015, 8, 31, 5, 46, 39, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			Expect(d.presigned).To(BeTrue())
			Expect(d.accessKey).To(Equal("AK"))
			Expect(d.signature).To(Equal("sig+/="))
			// The two maps are written one after the other, so x-amz-meta-a
			// appears twice. RGWHTTPArgs keeps X-AMZ-META-B apart from
			// x-amz-meta-b, and its std::map visits the upper-case name first.
			Expect(d.stringToSign).To(Equal("GET\n\n\n1441000000\nx-amz-meta-a:h\nx-amz-meta-z:h\n" +
				"x-amz-meta-a:1\nx-amz-meta-b:3,2\nx-amz-security-token:tok\n/b/k"))
		})

	DescribeTable("reads a presigned Expires with atoll and signs it as a C string",
		func(ctx SpecContext, expires string, wantExpired bool, wantDate string) {
			target := "http://s3.example.com/b/k?AWSAccessKeyId=AK&Signature=s&Expires=" + url.QueryEscape(expires)
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			d, err := authDataV2(newRequestView(req), &cfg, time.Unix(1440999999, 0))
			if wantExpired {
				Expect(err).To(MatchError(op.ErrAccessDenied), "%q", expires)
				return
			}
			Expect(err).NotTo(HaveOccurred(), "%q", expires)
			Expect(d.stringToSign).To(Equal("GET\n\n\n"+wantDate+"\n/b/k"), "%q", expires)
		},
		Entry("digits then text", "1441000000junk", false, "1441000000junk"),
		Entry("white space and a sign", " +1441000000", false, " +1441000000"),
		Entry("a value past LLONG_MAX, saturated", "99999999999999999999", false, "99999999999999999999"),
		Entry("bytes after a NUL, read by neither", "1441000000\x00x", false, "1441000000"),
		Entry("no digits, which is 0", "abc", true, ""),
		Entry("the current second", "1440999999", true, ""),
		Entry("a negative overflow, saturated", "-99999999999999999999", true, ""),
	)

	DescribeTable("canonicalStringV2 reads the request date as rgw_create_s3_canonical_header does",
		func(ctx SpecContext, hdr map[string][]string, want time.Time) {
			rv := newRequestView(rawRequest(ctx, http.MethodGet, "/b/k", hdr))
			_, got, err := canonicalStringV2(rv, false, nil)
			if want.IsZero() {
				Expect(err).To(HaveOccurred())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeTemporally("==", want))
		},
		Entry("a numeric zone is subtracted",
			map[string][]string{"Date": {"Sun, 30 Aug 2015 13:36:00 +0100"}}, signingTime),
		Entry("the year is checked as written, before second 60 rolls it into 1970",
			map[string][]string{"Date": {"Wed, 31 Dec 1969 23:59:60 GMT"}}, time.Time{}),
		Entry("the year is checked as written, before the zone moves it into 1970",
			map[string][]string{"Date": {"Wed, 31 Dec 1969 23:30:00 -0100"}}, time.Time{}),
		Entry("the year is checked as written, before the zone moves it out of 1970",
			map[string][]string{"Date": {"Thu, 01 Jan 1970 00:30:00 +0100"}}, time.Date(1969, 12, 31, 23, 30, 0, 0, time.UTC)),
		Entry("an ISO 8601 year is checked as written, before second 60 rolls it into 1970",
			map[string][]string{"X-Amz-Date": {"19691231T235960Z"}}, time.Time{}),
		Entry("an x-amz-date that does not parse is final",
			map[string][]string{"X-Amz-Date": {"garbage"}, "Date": {v2TestDate}}, time.Time{}),
	)

	DescribeTable("amzHeadersV2 is init_meta_info's x_meta_map of the X-Amz- headers alone",
		func(ctx SpecContext, hdr map[string][]string, want map[string]string) {
			Expect(amzHeadersV2(newRequestView(rawRequest(ctx, http.MethodGet, "/b/k", hdr)))).To(Equal(want))
		},
		Entry("no other meta prefix files under x-amz-",
			map[string][]string{
				"X-Goog-A": {"1"}, "X-Dho-B": {"2"}, "X-Rgw-C": {"3"}, "X-Object-D": {"4"},
				"X-Container-E": {"5"}, "X-Account-F": {"6"}, "X-Amz-G": {"7"},
			},
			map[string]string{"x-amz-g": "7"}),
		Entry("a name under another prefix leaves the X-Amz- value of its x-amz- name alone",
			map[string][]string{"X-Goog-Meta-Q": {"3"}, "X-Amz-Meta-Q": {"2 \t"}, "X-Account-Meta-Q": {"1 "}},
			map[string]string{"x-amz-meta-q": "2 \t"}),
		Entry("an underscore after the prefix keeps its place",
			map[string][]string{"X-Amz-Meta-A_b": {"1"}},
			map[string]string{"x-amz-meta-a_b": "1"}),
		Entry("a prefix spelled otherwise is no meta prefix",
			map[string][]string{"X_amz_meta": {"1"}, "X-Amzmeta": {"2"}, "Content-Md5": {"3"}},
			map[string]string{}),
	)

	// internal/s3's requestattrs_test.go stores "2" for the same map, so the
	// line signed is the value stored.
	It("keeps one value of spellings of one x-amz- name that the header map keeps apart, the last in byte order", func(ctx SpecContext) {
		req := rawRequest(ctx, http.MethodGet, "/b/k", nil)
		req.Header["x-amz-meta-q"] = []string{"2"}
		req.Header["X-Amz-Meta-Q"] = []string{"0", "1 "}
		Expect(amzHeadersV2(newRequestView(req))).To(Equal(map[string]string{"x-amz-meta-q": "2"}))
	})

	DescribeTable("canonicalResourceV2 puts the Host's bucket in front of the target",
		func(ctx SpecContext, method, target, host, want string) {
			req := httptest.NewRequestWithContext(ctx, method, target, nil)
			req.Host = host
			Expect(canonicalResourceV2(newRequestView(req), []string{"s3.example.com"})).To(Equal(want))
		},
		Entry("a port does not hide the bucket", http.MethodGet, "/k", "bkt.s3.example.com:8080", "/bkt/k"),
		Entry("a slash goes in front of a target without one", http.MethodOptions, "*", "bkt.s3.example.com", "/bkt/*"),
	)
})

// v2Expires is a presigned SigV2 URL's Expires, later than signingTime.
const v2Expires = "1441000000"

// v2Routed is a SigV2 request on one route and the string to sign its
// signature is over.
type v2Routed struct {
	presigned bool
	req       *http.Request
	sts       string
}

// v2OnBothRoutes is a PUT for /b/k carrying hdr, signed with SigV2 on the
// header route and presigned. resend, when not nil, then changes each one's
// headers as a holder of the signature would, the signature kept.
func v2OnBothRoutes(ctx context.Context, hdr map[string][]string, resend func(http.Header)) []v2Routed {
	var out []v2Routed
	for _, presigned := range []bool{false, true} {
		target := "http://s3.example.com/b/k"
		if presigned {
			target += "?AWSAccessKeyId=" + testAccessKey + "&Expires=" + v2Expires
		}
		req := rawRequest(ctx, http.MethodPut, target, hdr)
		if !presigned {
			req.Header.Set("Date", v2TestDate)
		}
		sts, _, err := canonicalStringV2(newRequestView(req), presigned, nil)
		Expect(err).NotTo(HaveOccurred())
		sig := v2Sign(testSecret, sts)
		if presigned {
			signed := httptest.NewRequestWithContext(ctx, http.MethodPut, target+"&Signature="+url.QueryEscape(sig), nil)
			signed.Header = req.Header
			req = signed
		} else {
			req.Header.Set("Authorization", "AWS "+testAccessKey+":"+sig)
		}
		if resend != nil {
			resend(req.Header)
		}
		out = append(out, v2Routed{presigned, req, sts})
	}
	return out
}

// moveHeader is a resend that sends the header from under the name to, its
// values kept; to is used as written.
func moveHeader(from, to string) func(http.Header) {
	return func(h http.Header) {
		vs := h.Values(from)
		Expect(vs).NotTo(BeEmpty(), "the signer sent %s", from)
		h.Del(from)
		h[to] = vs
	}
}

// verifiesV2 reports whether the signer's signature holds for c, once its
// auth data parse.
func verifiesV2(c v2Routed, cfg *Config) bool {
	d, err := authDataV2(newRequestView(c.req), cfg, signingTime)
	Expect(err).NotTo(HaveOccurred(), "presigned %t", c.presigned)
	ok, err := d.verify(testSecret)
	Expect(err).NotTo(HaveOccurred(), "presigned %t", c.presigned)
	return ok
}

// directiveSuffixes are directives the handlers read from X-Amz-, the name
// after the prefix and a value a signer might send.
var directiveSuffixes = [][2]string{
	{"Copy-Source", "b/src"},
	{"Copy-Source-If-Match", `"etag"`},
	{"Metadata-Directive", "REPLACE"},
	{"Acl", "public-read"},
	{"Grant-Full-Control", `id="u"`},
	{"Storage-Class", "STANDARD_IA"},
	{"Tagging", "k=v"},
	{"Object-Lock-Mode", "GOVERNANCE"},
	{"Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z"},
	{"Object-Lock-Legal-Hold", "ON"},
	{"Server-Side-Encryption-Customer-Key", "a2V5"},
	{"Meta-Foo", "bar"},
}

// radosgw's string to sign carries a header under any meta prefix under its
// x-amz- name, so its signature survives the header's move between prefixes
// (docs/ceph-upstream-bugs.md, "radosgw's SigV2 signature survives moving a
// signed x-amz directive under another meta prefix"). rgw-go's names the
// X-Amz- headers alone.
var _ = Describe("SigV2 under the meta prefixes", func() {
	var cfg Config

	BeforeEach(func() {
		cfg = DefaultConfig()
	})

	DescribeTable("ignores an unsigned header under another meta prefix, beside the signed X-Amz- one, on both routes",
		func(ctx SpecContext, prefix string) {
			for _, d := range directiveSuffixes {
				amz, other := "X-Amz-"+d[0], prefix+d[0]
				add := func(h http.Header) { h.Set(other, "unsigned") }
				for _, c := range v2OnBothRoutes(ctx, map[string][]string{amz: {d[1]}}, add) {
					sts, _, err := canonicalStringV2(newRequestView(c.req), c.presigned, nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(sts).To(Equal(c.sts), "%s adds no line, presigned %t", other, c.presigned)
					Expect(verifiesV2(c, &cfg)).To(BeTrue(), "%s unsigned, presigned %t", other, c.presigned)
				}
			}
		},
		renamedPrefixEntries(),
	)

	DescribeTable("fails the signature of a signed directive moved off X-Amz-, on both routes",
		func(ctx SpecContext, prefix string) {
			for _, d := range directiveSuffixes {
				amz, moved := "X-Amz-"+d[0], prefix+d[0]
				for _, c := range v2OnBothRoutes(ctx, map[string][]string{amz: {d[1]}}, moveHeader(amz, moved)) {
					Expect(c.sts).To(ContainSubstring("\n"+asciiLower(amz)+":"), "the signer's line, presigned %t", c.presigned)
					Expect(verifiesV2(c, &cfg)).To(BeFalse(), "%s, presigned %t", moved, c.presigned)
				}
			}
		},
		renamedPrefixEntries(),
	)

	// The signer's header under another prefix signs nothing, so the request
	// that carries it under X-Amz- signs a line the signer never signed.
	DescribeTable("fails the signature of a header the signer sent under another prefix, resent under X-Amz-, beside an X-Amz- one or not, on both routes",
		func(ctx SpecContext, prefix string) {
			for _, d := range directiveSuffixes {
				name, amz := prefix+d[0], "X-Amz-"+d[0]
				line := "\n" + asciiLower(amz) + ":"
				for _, c := range v2OnBothRoutes(ctx, map[string][]string{name: {d[1]}}, moveHeader(name, amz)) {
					Expect(c.sts).NotTo(ContainSubstring(line), "the signer's string to sign, %s, presigned %t", name, c.presigned)
					Expect(verifiesV2(c, &cfg)).To(BeFalse(), "%s resent as %s, presigned %t", name, amz, c.presigned)
				}
				resend := func(h http.Header) {
					h.Set(amz, h.Get(name))
					h.Del(name)
				}
				for _, c := range v2OnBothRoutes(ctx, map[string][]string{amz: {"signed"}, name: {d[1]}}, resend) {
					Expect(c.sts).To(ContainSubstring(line+"signed\n"), "the signer's %s alone, presigned %t", amz, c.presigned)
					Expect(verifiesV2(c, &cfg)).To(BeFalse(), "%s resent over %s, presigned %t", name, amz, c.presigned)
				}
			}
		},
		renamedPrefixEntries(),
	)

	// X-Account- sorts before X-Amz-, so radosgw signs both requests as the
	// joined line x-amz-acl:private,public-read.
	It("fails the signature of the swap that keeps radosgw's joined value, on both routes", func(ctx SpecContext) {
		signer := map[string][]string{"X-Amz-Acl": {"private"}, "X-Goog-Acl": {"public-read"}}
		swap := func(h http.Header) {
			h.Del("X-Goog-Acl")
			h.Set("X-Account-Acl", "private")
			h.Set("X-Amz-Acl", "public-read")
		}
		for _, c := range v2OnBothRoutes(ctx, signer, swap) {
			Expect(c.sts).To(ContainSubstring("\nx-amz-acl:private\n"), "the signer's X-Amz-Acl alone, presigned %t", c.presigned)
			Expect(verifiesV2(c, &cfg)).To(BeFalse(), "the swap, presigned %t", c.presigned)
		}
	})

	It("verifies each directive under X-Amz-, beside a header under another prefix, on both routes", func(ctx SpecContext) {
		for _, d := range directiveSuffixes {
			name := "X-Amz-" + d[0]
			for _, c := range v2OnBothRoutes(ctx, map[string][]string{name: {d[1]}, "X-Goog-Meta-Foo": {"baz"}}, nil) {
				Expect(c.sts).To(ContainSubstring("\n"+asciiLower(name)+":"+d[1]+"\n"), "presigned %t", c.presigned)
				Expect(c.sts).NotTo(ContainSubstring("baz"), "presigned %t", c.presigned)
				Expect(verifiesV2(c, &cfg)).To(BeTrue(), "%s, presigned %t", name, c.presigned)
			}
		}
	})

	It("fails the signature of a directive moved under another prefix in any case", func(ctx SpecContext) {
		for _, spelling := range []string{"x-goog-copy-source", "X-GOOG-COPY-SOURCE", "x-Account-copy-SOURCE"} {
			for _, c := range v2OnBothRoutes(ctx, map[string][]string{"X-Amz-Copy-Source": {"b/src"}}, moveHeader("X-Amz-Copy-Source", spelling)) {
				Expect(verifiesV2(c, &cfg)).To(BeFalse(), "%s, presigned %t", spelling, c.presigned)
			}
		}
	})
})
