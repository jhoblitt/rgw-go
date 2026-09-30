package s3_test

import (
	"context"
	"crypto/tls"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

// virtualHosted is a gateway whose rgw_dns_name is s3.example.com.
func virtualHosted() s3.Config {
	return s3.Config{DNSNames: []string{"s3.example.com"}}
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
		Entry("an empty pair is an empty key", "acl&", url.Values{"acl": {""}, "": {""}}),
		Entry("one leading ? is skipped", "?acl", url.Values{"acl": {""}}),
		Entry("only the exact X-Amz- spelling is lowercased", "X-AMZ-Date=d&Foo-X-Amz-Bar=e", url.Values{"X-AMZ-Date": {"d"}, "foo-x-amz-bar": {"e"}}),
	)
	Describe("Host", func() {
		DescribeTable("names the bucket only inside a configured domain",
			func(ctx SpecContext, host, target, bucket, object string) {
				p, err := parse(ctx, "GET", target, map[string]string{"Host": host}, virtualHosted())
				Expect(err).NotTo(HaveOccurred())
				Expect(p.Req.Bucket).To(Equal(bucket), "bucket")
				Expect(p.Req.Object.Name).To(Equal(object), "object")
			},
			Entry("subdomain is the bucket", "plain.s3.example.com", "/k", "plain", "k"),
			Entry("port is stripped first", "plain.s3.example.com:7480", "/k", "plain", "k"),
			Entry("the bare domain is path style", "s3.example.com", "/plain/k", "plain", "k"),
			Entry("the bare domain with a port is path style", "s3.example.com:443", "/plain/k", "plain", "k"),
			Entry("an IPv4 literal is path style", "10.0.0.7:7480", "/plain/k", "plain", "k"),
			Entry("an IPv6 literal is path style", "[fd00::7]:7480", "/plain/k", "plain", "k"),
			Entry("dotted digits are an IPv4 literal whatever their values", "999.1.2.3", "/plain/k", "plain", "k"),
			Entry("an unknown host that is a valid bucket name is the bucket (CNAME)", "photos.example.org", "/k", "photos.example.org", "k"),
			Entry("an unknown host that is not a valid bucket name is path style", "ab", "/plain/k", "plain", "k"),
			Entry("a name matched without a dot before it is a CNAME", "photos3.example.com", "/k", "photos3.example.com", "k"),
			Entry("the domain matches in any case and the bucket keeps its case", "MyBucket.S3.Example.COM", "/k", "MyBucket", "k"),
			Entry("a virtual-hosted root is the bucket", "plain.s3.example.com", "/", "plain", ""),
			Entry("a virtual-hosted key decodes once", "plain.s3.example.com", "/a%2Fb", "plain", "a/b"),
			Entry("the Host's bucket is decoded along with the path", "my%2Dbucket.s3.example.com", "/k", "my-bucket", "k"),
		)
		It("tries the configured names in the sorted order of radosgw's hostname set", func(ctx SpecContext) {
			cfg := s3.Config{DNSNames: []string{"s3.example.com", "", "example.com", "x.s3.example.com"}}
			p, err := parse(ctx, "GET", "/k", map[string]string{"Host": "a.x.s3.example.com"}, cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Bucket).To(Equal("a.x.s3"), "example.com sorts first of the three names the host ends in")
		})
		It("never takes the Host as a bucket when no names are configured", func(ctx SpecContext) {
			p, err := parse(ctx, "GET", "/plain/k", map[string]string{"Host": "photos.example.org"}, s3.Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Bucket).To(Equal("plain"))
			Expect(p.Req.Host).To(Equal("photos.example.org"))
		})
		DescribeTable("records the Host without its port, lowercased",
			func(ctx SpecContext, header, want string) {
				p, err := parse(ctx, "GET", "/plain/k", map[string]string{"Host": header}, s3.Config{})
				Expect(err).NotTo(HaveOccurred())
				Expect(p.Req.Host).To(Equal(want))
			},
			Entry("a name with a port", "S3.Example.com:7480", "s3.example.com"),
			Entry("an IPv6 literal with a port", "[FD00::7]:7480", "fd00::7"),
			Entry("an IPv6 literal without a port", "[fd00::7]", "fd00::7"),
			Entry("an unclosed bracket still loses a trailing :<digits>", "[fd00::7", "[fd00:"),
			Entry("no Host at all", "", ""),
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

var _ = Describe("PostAuthInit", func() {
	DescribeTable("returns the errors radosgw's postauth_init returns, in its order",
		func(ctx SpecContext, target string, hdr map[string]string, authTenant string, want *op.Error) {
			p, err := parse(ctx, "GET", target, hdr, s3.Config{})
			Expect(err).NotTo(HaveOccurred(), "ParseRequest leaves the names to PostAuthInit")
			Expect(s3.PostAuthInit(p, authTenant)).To(MatchError(want))
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
	)
	DescribeTable("gives the request its tenant",
		func(ctx SpecContext, target, authTenant, tenant, bucket string) {
			p, err := parse(ctx, "GET", target, nil, s3.Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s3.PostAuthInit(p, authTenant)).To(Succeed())
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
			Expect(s3.PostAuthInit(p, "")).To(Succeed())
		},
		Entry("an object name of exactly 1024 bytes", "/plain/"+strings.Repeat("k", 1024), nil),
		Entry("a short bucket name, left to the bucket lookup", "/ab/k", nil),
		Entry("a copy source whose empty bucket init leaves unset", "/b/k", copySource("//src")),
		Entry("a part copy's source, which init does not parse", "/b/k?uploadId=u&partNumber=1", copySource("t-1:src/k")),
		Entry("a ranged copy's source, which init does not parse", "/b/k",
			map[string]string{"X-Amz-Copy-Source": "t-1:src/k", "X-Amz-Copy-Source-Range": "bytes=0-1"}),
		Entry("a copy source parse_copy_location refuses, which the handler lifecycle answers with 400 before authentication", "/b/k", copySource("t-1:src")),
		Entry("a copy source with an empty key, refused the same way", "/b/k", copySource("t-1:src/")),
	)
})
