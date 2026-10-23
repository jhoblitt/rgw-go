package s3_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

func parse(ctx context.Context, method, target string, hdr map[string]string, cfg s3.Config) (s3.Parsed, error) {
	req := httptest.NewRequestWithContext(ctx, method, target, nil)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	return s3.ParseRequest(req, cfg, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
}

// readRequest reads an HTTP/1.1 request line for target with host as its
// Host header, as the server does, so an absolute-form target's authority
// becomes req.Host and its query is left unchecked.
func readRequest(target, host string) *http.Request {
	raw := "GET " + target + " HTTP/1.1\r\nHost: " + host + "\r\n\r\n"
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	Expect(err).NotTo(HaveOccurred(), "net/http reads %q", target)
	return req
}

// virtualHosted is a gateway whose rgw_dns_name is s3.example.com.
func virtualHosted() s3.Config {
	return s3.Config{DNSNames: []string{"s3.example.com"}}
}

// websiteGateway is a gateway whose rgw_dns_name is s3.example.com and whose
// rgw_dns_s3website_name is web.example.com, under rgw_enable_apis apis.
func websiteGateway(apis string) s3.Config {
	a, err := cephconf.EnabledAPIs(cephconf.NewOptions(cephconf.MapGetter{"rgw_enable_apis": apis}))
	Expect(err).NotTo(HaveOccurred())
	return s3.Config{
		DNSNames: []string{"s3.example.com"}, WebsiteNames: []string{"web.example.com"},
		Website: a.Website, WebsiteFirst: a.WebsiteFirst,
	}
}

var _ = Describe("ParseRequest", func() {
	DescribeTable("splits tenant, bucket and object from the path",
		func(ctx SpecContext, target, tenant, bucket, object, instance string, explicit bool) {
			p, err := parse(ctx, "GET", target, nil, s3.Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Tenant).To(Equal(tenant), "tenant")
			Expect(p.Req.Bucket).To(Equal(bucket), "bucket")
			Expect(p.Req.Object).To(Equal(meta.ObjKey{Name: object, Instance: instance}), "object")
			Expect(p.ExplicitTenant).To(Equal(explicit), "explicit tenant")
		},
		Entry("service", "/", "", "", "", "", false),
		Entry("bucket", "/plain", "", "plain", "", "", false),
		Entry("bucket with trailing slash", "/plain/", "", "plain", "", "", false),
		Entry("object with slashes", "/plain/a/b/c.txt", "", "plain", "a/b/c.txt", "", false),
		Entry("percent-encoded slash decodes once", "/plain/a%2Fb", "", "plain", "a/b", "", false),
		Entry("an encoded percent decodes only once", "/plain/a%252Fb", "", "plain", "a%2Fb", "", false),
		Entry("plus stays a plus in the path", "/plain/a+b", "", "plain", "a+b", "", false),
		Entry("an empty segment after the bucket starts the object name", "/plain//k", "", "plain", "/k", "", false),
		Entry("an empty first segment names no bucket", "//k", "", "", "", "", false),
		Entry("tenanted bucket", "/t1:tenanted/k", "t1", "tenanted", "k", "", true),
		Entry("explicit legacy tenant", "/:plain/k", "", "plain", "k", "", true),
		Entry("only the first colon separates the tenant", "/t1:a:b/k", "t1", "a:b", "k", "", true),
		Entry("versionId fills the instance", "/plain/k?versionId=v1", "", "plain", "k", "v1", false),
		Entry("a short bucket name is left to the bucket lookup", "/ab", "", "ab", "", "", false),
		Entry("a token with nothing after its colon stays whole", "/t1:/k", "", "t1:", "k", "", false),
		Entry("a bad tenant waits for PostAuthInit", "/t-1:b/k", "t-1", "b", "k", "", true),
		Entry("a key radosgw rejects waits for PostAuthInit", "/plain/%ff%fe", "", "plain", "\xff\xfe", "", false),
	)
	DescribeTable("rejects what radosgw rejects before authentication",
		func(ctx SpecContext, method, target string, want *op.Error) {
			_, err := parse(ctx, method, target, nil, s3.Config{})
			Expect(err).To(MatchError(want))
		},
		Entry("NUL in the path is ERR_ZERO_IN_URL", "GET", "/plain/a%00b", op.ErrInvalidRequest),
		Entry("a NUL is found before the method is judged", "PATCH", "/plain/a%00b", op.ErrInvalidRequest),
		Entry("a method radosgw has no op for", "PATCH", "/plain/k", op.ErrMethodNotAllowed),
		Entry("COPY, which only Swift serves", "COPY", "/plain/k", op.ErrMethodNotAllowed),
		Entry("a method spelled in lower case", "get", "/plain/k", op.ErrMethodNotAllowed),
	)
	It("lowercases X-Amz- query keys except their dashes and decodes plus as space", func(ctx SpecContext) {
		p, err := parse(ctx, "GET", "/plain/k?X-Amz-Signature=abc&Prefix=a+b", nil, s3.Config{})
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Req.Query.Get("x-amz-signature")).To(Equal("abc"))
		Expect(p.Req.Query.Has("X-Amz-Signature")).To(BeFalse())
		Expect(p.Req.Query.Get("Prefix")).To(Equal("a b"))
	})
	DescribeTable("reads the query as RGWHTTPArgs::parse does",
		func(ctx SpecContext, query string, want url.Values) {
			p, err := parse(ctx, "GET", "/plain/k?"+query, nil, s3.Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Query).To(Equal(want), "query")
			Expect(p.Req.RawQuery).To(Equal(query), "raw query")
		},
		Entry("a key without a value", "acl", url.Values{"acl": {""}}),
		Entry("the last of a repeated key wins", "prefix=a&prefix=b", url.Values{"prefix": {"b"}}),
		Entry("a semicolon is an ordinary character", "prefix=a;b", url.Values{"prefix": {"a;b"}}),
		Entry("a pair is decoded before it is split at =", "a%3Db=c", url.Values{"a": {"b=c"}}),
		Entry("a malformed escape empties its pair", "a=%zz&prefix=p", url.Values{"": {""}, "prefix": {"p"}}),
		Entry("a truncated escape ends its pair", "prefix=ab%4", url.Values{"prefix": {"ab"}}),
		Entry("a lone percent sign ends its pair", "prefix=ab%", url.Values{"prefix": {"ab"}}),
		Entry("an escaped NUL is kept", "prefix=a%00b", url.Values{"prefix": {"a\x00b"}}),
		Entry("an empty pair is an empty key", "acl&", url.Values{"acl": {""}, "": {""}}),
		Entry("one leading ? is skipped", "?acl", url.Values{"acl": {""}}),
		Entry("only the exact X-Amz- spelling is lowercased", "X-AMZ-Date=d&Foo-X-Amz-Bar=e", url.Values{"X-AMZ-Date": {"d"}, "foo-x-amz-bar": {"e"}}),
	)
	Describe("Host", func() {
		DescribeTable("puts the bucket vhost reads from the Host in front of the path",
			func(ctx SpecContext, host, target, bucket, object string) {
				p, err := parse(ctx, "GET", target, map[string]string{"Host": host}, virtualHosted())
				Expect(err).NotTo(HaveOccurred())
				Expect(p.Req.Bucket).To(Equal(bucket), "bucket")
				Expect(p.Req.Object.Name).To(Equal(object), "object")
			},
			Entry("subdomain is the bucket, its port stripped first", "plain.s3.example.com:7480", "/k", "plain", "k"),
			Entry("the bare domain is path style", "s3.example.com", "/plain/k", "plain", "k"),
			Entry("a virtual-hosted root is the bucket", "plain.s3.example.com", "/", "plain", ""),
			Entry("a virtual-hosted key decodes once", "plain.s3.example.com", "/a%2Fb", "plain", "a/b"),
			Entry("the Host's bucket is decoded along with the path", "my%2Dbucket.s3.example.com", "/k", "my-bucket", "k"),
			Entry("a plus stays a plus in a virtual-hosted path", "plain.s3.example.com", "/a+b", "plain", "a+b"),
			Entry("a bad hex digit in the Host's bucket empties the whole URI", "my%zz.s3.example.com", "/k", "", ""),
		)
		DescribeTable("records the Host without its port, lowercased",
			func(ctx SpecContext, header, want string) {
				p, err := parse(ctx, "GET", "/plain/k", map[string]string{"Host": header}, s3.Config{})
				Expect(err).NotTo(HaveOccurred())
				Expect(p.Req.Host).To(Equal(want))
			},
			Entry("a name with a port", "S3.Example.com:7480", "s3.example.com"),
			Entry("an IPv6 literal with a port", "[FD00::7]:7480", "fd00::7"),
		)
		DescribeTable("keeps the path the client sent for SigV4",
			func(ctx SpecContext, host, target, path, rawPath string) {
				p, err := parse(ctx, "GET", target, map[string]string{"Host": host}, virtualHosted())
				Expect(err).NotTo(HaveOccurred())
				Expect(p.Req.Path).To(Equal(path), "path")
				Expect(p.Req.RawPath).To(Equal(rawPath), "raw path")
			},
			Entry("path style", "s3.example.com", "/plain/a%2Fb", "/plain/a/b", "/plain/a%2Fb"),
			Entry("virtual-hosted", "plain.s3.example.com", "/a%2Fb", "/a/b", "/a%2Fb"),
		)
	})
	// req_info reduces an absolute-form target with get_abs_path before it
	// splits off the query, and preprocess decodes what is left: the path
	// and query SigV2 and SigV4 sign, not net/http's URL.
	DescribeTable("routes on the request target as req_info splits it",
		func(target, host, uri, bucket, object, rawQuery string, query url.Values) {
			req := readRequest(target, host)
			Expect(s3.DecodedURI(req, virtualHosted())).To(Equal(uri), "decoded URI")
			p, err := s3.ParseRequest(req, virtualHosted(), time.Unix(1, 0))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Bucket).To(Equal(bucket), "bucket")
			Expect(p.Req.Object.Name).To(Equal(object), "object")
			Expect(p.Req.RawQuery).To(Equal(rawQuery), "raw query")
			Expect(p.Req.Query).To(Equal(query), "query")
		},
		Entry("origin form", "/b/k?acl", "s3.example.com", "/b/k", "b", "k", "acl", url.Values{"acl": {""}}),
		Entry("absolute form", "http://s3.example.com/b/k?acl", "x", "/b/k", "b", "k", "acl", url.Values{"acl": {""}}),
		Entry("absolute form over https", "https://s3.example.com/b/k", "x", "/b/k", "b", "k", "", url.Values{}),
		Entry("a query holding the target's first slash", "http://s3.example.com?x=/b/k", "x",
			"/b/k", "b", "k", "x=/b/k", url.Values{"x": {"/b/k"}}),
		Entry("a query holding the first slash and a second ?, after which the arguments are", "http://s3.example.com?versionId=v1&x/b/k?acl", "x",
			"/b/k", "b", "k", "acl", url.Values{"acl": {""}}),
		Entry("an encoded slash in a query-held path", "http://s3.example.com?x=/b/a%2Fb", "x",
			"/b/a/b", "b", "a/b", "x=/b/a%2Fb", url.Values{"x": {"/b/a/b"}}),
		Entry("a plus in a query-held path stays a plus", "http://s3.example.com?x/b/a+b?", "x", "/b/a+b", "b", "a+b", "", url.Values{}),
		Entry("an absolute-form target without a slash names no bucket", "http://s3.example.com", "x",
			"http://s3.example.com", "", "", "", url.Values{}),
		Entry("a query-only absolute-form target names no bucket", "http://s3.example.com?acl", "x",
			"http://s3.example.com", "", "", "acl", url.Values{"acl": {""}}),
		Entry("another scheme is kept whole and names no bucket", "ftp://s3.example.com/b/k", "x",
			"ftp://s3.example.com/b/k", "", "", "", url.Values{}),
		Entry("an upper-case scheme is kept whole", "HTTP://s3.example.com/b/k", "x",
			"HTTP://s3.example.com/b/k", "", "", "", url.Values{}),
		Entry("a scheme without an authority is kept whole", "foo:/b/k", "s3.example.com", "foo:/b/k", "", "", "", url.Values{}),
		Entry("the asterisk form names no bucket", "*", "s3.example.com", "*", "", "", "", url.Values{}),
		Entry("an encoded space", "/b/a%20b", "s3.example.com", "/b/a b", "b", "a b", "", url.Values{}),
		Entry("a semicolon is an ordinary character", "/b/k;v=1", "s3.example.com", "/b/k;v=1", "b", "k;v=1", "", url.Values{}),
		Entry("a double slash starts the key with a slash", "/b//k", "s3.example.com", "/b//k", "b", "/k", "", url.Values{}),
		Entry("virtual-hosted origin form", "/k", "b.s3.example.com", "/b/k", "b", "k", "", url.Values{}),
		Entry("a virtual-hosted query holding the first slash", "http://b.s3.example.com?x/k?acl", "x",
			"/b/k", "b", "k", "acl", url.Values{"acl": {""}}),
		Entry("a virtual-hosted target without a slash is a key", "http://b.s3.example.com", "x",
			"/b/http://b.s3.example.com", "b", "http://b.s3.example.com", "", url.Values{}),
	)
	DescribeTable("finds a malformed percent-escape net/http leaves unchecked in the URI it decodes",
		func(target, host string, want bool) {
			Expect(s3.MalformedPath(readRequest(target, host), virtualHosted())).To(Equal(want))
		},
		Entry("a bad hex digit in a query-held path", "http://s3.example.com?x=/b/%zz", "x", true),
		Entry("a bad hex digit in a virtual-hosted query-held path", "http://b.s3.example.com?x/%zz?", "x", true),
		Entry("a truncated escape in a query-held path", "http://s3.example.com?x=/b/k%4", "x", true),
		Entry("a lone percent sign ending a query-held path", "http://s3.example.com?x=/b/k%", "x", true),
		Entry("a bad hex digit in a target kept whole", "foo:%zz", "s3.example.com", true),
		Entry("a bad hex digit in the Host's bucket", "/k", "my%zz.s3.example.com", true),
		Entry("a lone percent sign ending the Host's bucket", "/", "my%.s3.example.com", true),
		Entry("a well-formed escape in the Host's bucket", "/k", "my%2Dbucket.s3.example.com", false),
		Entry("a bad hex digit in a Host taken whole as the bucket", "/k", "my%zz.example.org", true),
		Entry("a bad hex digit in the query alone", "http://s3.example.com/b/k?x=%zz", "x", false),
		Entry("a well-formed escape in a query-held path", "http://s3.example.com?x=/b/a%2Fb", "x", false),
		Entry("an origin-form path", "/b/a%2Fb", "s3.example.com", false),
	)
	// The table takes no release: RGWREST::preprocess reads a Host alike at
	// v19.2.7 and v20.2.4, and rgw-go gives a request with no Host
	// v20.2.4's flag on both.
	DescribeTable("reads a website request's bucket, domain and flag, for each rgw_enable_apis order",
		func(ctx SpecContext, apis, host, bucket, domain string, website bool) {
			p, err := parse(ctx, "GET", "/k", map[string]string{"Host": host}, websiteGateway(apis))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Bucket).To(Equal(bucket), "bucket")
			Expect(p.Req.Domain).To(Equal(domain), "domain")
			Expect(p.Req.Website).To(Equal(website), "website")
		},
		Entry("s3 before s3website: a website host", "s3, s3website", "web.example.com", "k", "web.example.com", true),
		Entry("s3 before s3website: a bucket subdomain of a website host",
			"s3, s3website", "site.web.example.com", "site", "web.example.com", true),
		Entry("s3 before s3website: a CNAME to a website host", "s3, s3website", "www.example.org", "www.example.org", "", false),
		Entry("s3 before s3website: a plain S3 host", "s3, s3website", "site.s3.example.com", "site", "s3.example.com", false),
		Entry("s3 before s3website: no Host", "s3, s3website", "", "k", "", false),
		Entry("s3website before s3: a website host", "s3website, s3", "web.example.com", "k", "web.example.com", true),
		Entry("s3website before s3: a bucket subdomain of a website host",
			"s3website, s3", "site.web.example.com", "site", "web.example.com", true),
		Entry("s3website before s3: a CNAME to a website host", "s3website, s3", "www.example.org", "www.example.org", "", true),
		Entry("s3website before s3: a plain S3 host", "s3website, s3", "site.s3.example.com", "site", "s3.example.com", true),
		Entry("s3website before s3: no Host, as v20.2.4 flags it", "s3website, s3", "", "k", "", true),
		Entry("s3website without s3: a website host", "s3website", "web.example.com", "k", "web.example.com", true),
		Entry("s3website without s3: a bucket subdomain of a website host",
			"s3website", "site.web.example.com", "site", "web.example.com", true),
		Entry("s3website without s3: a CNAME to a website host", "s3website", "www.example.org", "www.example.org", "", true),
		Entry("s3website without s3: a plain S3 host", "s3website", "site.s3.example.com", "site", "s3.example.com", true),
		Entry("s3website without s3: no Host", "s3website", "", "k", "", true),
		Entry("s3 without s3website: a website host, taken whole as the bucket", "s3", "web.example.com", "web.example.com", "", false),
		Entry("s3 without s3website: a bucket subdomain of a website host, taken whole as the bucket",
			"s3", "site.web.example.com", "site.web.example.com", "", false),
		Entry("s3 without s3website: a CNAME to a website host", "s3", "www.example.org", "www.example.org", "", false),
		Entry("s3 without s3website: a plain S3 host", "s3", "site.s3.example.com", "site", "s3.example.com", false),
		Entry("s3 without s3website: no Host", "s3", "", "k", "", false),
	)
	DescribeTable("takes WebsiteFirst only with Website, as preprocess checks s3website_enabled with the rank",
		func(ctx SpecContext, host string) {
			cfg := s3.Config{DNSNames: []string{"s3.example.com"}, WebsiteNames: []string{"web.example.com"}, WebsiteFirst: true}
			p, err := parse(ctx, "GET", "/k", map[string]string{"Host": host}, cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Website).To(BeFalse(), "website, host %q", host)
		},
		Entry("a plain S3 host", "site.s3.example.com"),
		Entry("no Host", ""),
	)
	It("fills the plain fields", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, "PUT", "/plain/k", strings.NewReader("body"))
		req.Header.Set("Referer", "http://ref/")
		req.RemoteAddr = "10.1.2.3:5000"
		p, err := s3.ParseRequest(req, s3.Config{}, time.Unix(1, 0))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Req.Method).To(Equal("PUT"))
		Expect(p.Req.ContentLength).To(BeEquivalentTo(4))
		Expect(p.Req.Referer).To(Equal("http://ref/"))
		Expect(p.Req.RemoteAddr).To(Equal("10.1.2.3:5000"))
		Expect(p.Req.RawPath).To(Equal("/plain/k"))
		Expect(p.Req.Time).To(Equal(time.Unix(1, 0)))
		Expect(p.Req.TLS).To(BeFalse())
		Expect(p.Req.Header.Get("Referer")).To(Equal("http://ref/"), "header")
		body, err := io.ReadAll(p.Req.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("body"), "body")
	})
	It("marks a request that arrived over TLS", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, "GET", "/plain/k", nil)
		req.TLS = &tls.ConnectionState{}
		p, err := s3.ParseRequest(req, s3.Config{}, time.Unix(1, 0))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Req.TLS).To(BeTrue())
	})
})

// copySource is the x-amz-copy-source header of a request.
func copySource(v string) map[string]string {
	return map[string]string{"X-Amz-Copy-Source": v}
}

// postAuthInit runs PostAuthInit on p as an identity of tenant authTenant.
func postAuthInit(ctx context.Context, p s3.Parsed, authTenant string) error {
	p.Req.Identity.Tenant = authTenant
	return s3.PostAuthInit(ctx, p)
}

var _ = Describe("PostAuthInit", func() {
	DescribeTable("returns the errors radosgw's postauth_init returns, in its order",
		func(ctx SpecContext, target string, hdr map[string]string, authTenant string, want *op.Error) {
			p, err := parse(ctx, "GET", target, hdr, s3.Config{})
			Expect(err).NotTo(HaveOccurred(), "ParseRequest leaves the names to PostAuthInit")
			Expect(postAuthInit(ctx, p, authTenant)).To(MatchError(want))
		},
		Entry("empty bucket after the tenant colon", "/t1:/k", nil, "", op.ErrInvalidBucketName),
		Entry("a lone colon", "/:/k", nil, "", op.ErrInvalidBucketName),
		Entry("an empty bucket is found before a bad tenant", "/t-1:/k", nil, "", op.ErrInvalidBucketName),
		Entry("a bad tenant character", "/t-1:b/k", nil, "", op.ErrInvalidTenantName),
		Entry("a bad tenant from the identity", "/b/k", nil, "t-1", op.ErrInvalidTenantName),
		Entry("a bad tenant is found before a bad object name", "/t-1:b/"+strings.Repeat("k", 1025), nil, "", op.ErrInvalidTenantName),
		Entry("an object name over 1024 bytes", "/plain/"+strings.Repeat("k", 1025), nil, "", op.ErrInvalidObjectName),
		Entry("an object name that is not UTF-8", "/plain/%ff%fe", nil, "", op.ErrInvalidObjectName),
		Entry("an object name holding a UTF-16 surrogate", "/plain/%ed%a0%80", nil, "", op.ErrInvalidObjectName),
		Entry("a bad object name is found before a bad copy source", "/plain/%ff", copySource("t-1:src/k"), "", op.ErrInvalidObjectName),
		Entry("a copy source with nothing after its tenant colon", "/b/k", copySource("/t1:/src"), "", op.ErrInvalidBucketName),
		Entry("a copy source's bad tenant, whatever the method", "/b/k", copySource("t-1:src/k"), "", op.ErrInvalidTenantName),
		Entry("a copy source takes the identity's tenant, not the URL's", "/t1:b/k", copySource("src/k"), "t-1", op.ErrInvalidTenantName),
		Entry("a copy source decoded before it is split", "/b/k", copySource("t%2D1:src/k"), "", op.ErrInvalidTenantName),
		Entry("a copy source whose escaped slash splits it", "/b/k", copySource("t-1:src%2Fk"), "", op.ErrInvalidTenantName),
	)
	DescribeTable("gives the request its tenant",
		func(ctx SpecContext, target, authTenant, tenant, bucket string) {
			p, err := parse(ctx, "GET", target, nil, s3.Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(postAuthInit(ctx, p, authTenant)).To(Succeed())
			Expect(p.Req.Tenant).To(Equal(tenant), "tenant")
			Expect(p.Req.Bucket).To(Equal(bucket), "bucket")
		},
		Entry("the identity's for a bucket named alone", "/b/k", "t2", "t2", "b"),
		Entry("the URL's when it names one", "/t1:b/k", "t2", "t1", "b"),
		Entry("the legacy tenant when the URL names it", "/:b/k", "t2", "", "b"),
		Entry("the identity's at service scope", "/", "t2", "t2", ""),
	)
	DescribeTable("passes what radosgw's postauth_init passes",
		func(ctx SpecContext, target string, hdr map[string]string) {
			p, err := parse(ctx, "PUT", target, hdr, s3.Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(postAuthInit(ctx, p, "")).To(Succeed())
		},
		Entry("an object name of exactly 1024 bytes", "/plain/"+strings.Repeat("k", 1024), nil),
		Entry("a short bucket name, left to the bucket lookup", "/ab/k", nil),
		Entry("a copy source whose empty bucket init leaves unset", "/b/k", copySource("//src")),
		Entry("a part copy's source, which init does not parse", "/b/k?uploadId=u&partNumber=1", copySource("t-1:src/k")),
		Entry("a ranged copy's source, which init does not parse", "/b/k",
			map[string]string{"X-Amz-Copy-Source": "t-1:src/k", "X-Amz-Copy-Source-Range": "bytes=0-1"}),
		Entry("a copy source parse_copy_location refuses, which the handler lifecycle answers with 400 before authentication", "/b/k", copySource("t-1:src")),
		Entry("a copy source with an empty key, refused the same way", "/b/k", copySource("t-1:src/")),
		Entry("a copy source whose bad hex digit empties it", "/b/k", copySource("t-1:src/k%zz")),
		Entry("a copy source whose truncated escape leaves the key empty", "/b/k", copySource("t-1:src/%4")),
	)
})
