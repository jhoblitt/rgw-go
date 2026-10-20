package vhost

import (
	"net/http"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// gateway is the names of a gateway whose S3 name is s3.example.com and
// whose website name is web.example.com, with s3website enabled or not.
func gateway(websiteAPI bool) Names {
	return Names{S3: []string{"s3.example.com"}, Website: []string{"web.example.com"}, WebsiteAPI: websiteAPI}
}

var _ = Describe("vhost", func() {
	DescribeTable("RequestTarget is req_info's request_uri and request_params",
		func(target, wantPath, wantQuery string) {
			req := &http.Request{RequestURI: target, URL: &url.URL{Path: "/unused"}}
			path, query := RequestTarget(req)
			Expect([]string{path, query}).To(Equal([]string{wantPath, wantQuery}), "%q", target)
		},
		Entry("the path and query as sent, a byte Go would escape kept", "/b/a|b%2F?x=1&y", "/b/a|b%2F", "x=1&y"),
		Entry("an absolute-form target reduced to its path", "http://h.example.com:8080/k%20v?y", "/k%20v", "y"),
		Entry("a slash in the query that get_abs_path takes for the path's", "http://h?x=/y", "/y", "x=/y"),
		Entry("a scheme get_abs_path does not reduce", "ftp://h/k?q", "ftp://h/k", "q"),
	)
	DescribeTable("Hostname is req_info.host after RGWREST::preprocess",
		func(host, want string) { Expect(Hostname(host)).To(Equal(want), "%q", host) },
		Entry("a bare name", "example.com", "example.com"),
		Entry("a port", "example.com:8080", "example.com"),
		Entry("the case the client sent", "S3.Example.com:7480", "S3.Example.com"),
		Entry("a bracketed IPv6 literal with a port", "[::1]:8080", "::1"),
		Entry("a bracketed IPv6 literal", "[::1]", "::1"),
		Entry("a trailing colon", "example.com:", "example.com"),
		Entry("a non-numeric port, cut at the first colon", "example.com:http", "example.com"),
		Entry("an unclosed bracket, which first loses its trailing :digits", "[::1", "[:"),
		Entry("empty", "", ""),
	)

	DescribeTable("Domain is the name rgw_find_host_in_domains matched, as cut from the host",
		func(host string, names []string, want string) { Expect(Domain(host, names)).To(Equal(want)) },
		Entry("a subdomain's name, in the Host's case", "bkt.S3.Example.com", []string{"s3.example.com"}, "S3.Example.com"),
		Entry("the name itself", "s3.example.com", []string{"s3.example.com"}, "s3.example.com"),
		Entry("the least matching name", "a.b.example.com", []string{"example.com", "b.example.com"}, "b.example.com"),
		Entry("a CNAME host matches none", "www.example.org", []string{"s3.example.com"}, ""),
		Entry("no names configured", "s3.example.com", nil, ""),
		Entry("no host", "", []string{"s3.example.com"}, ""),
	)

	DescribeTable("Bucket is rgw_find_host_in_domains with the CNAME fallback",
		func(host string, names []string, want string) { Expect(Bucket(host, names)).To(Equal(want)) },
		Entry("subdomain", "bkt.s3.example.com", []string{"s3.example.com"}, "bkt"),
		Entry("a case-insensitive suffix, the bucket in the Host's case", "BKT.S3.Example.COM", []string{"s3.example.com"}, "BKT"),
		Entry("only ASCII letters fold", "bkt.s3.éxample.com", []string{"s3.Éxample.com"}, "bkt.s3.éxample.com"),
		Entry("equal to a name is path style", "s3.example.com", []string{"s3.example.com"}, ""),
		Entry("cname fallback", "www.example.org", []string{"s3.example.com"}, "www.example.org"),
		Entry("ip literal is never a bucket", "10.0.0.1", []string{"s3.example.com"}, ""),
		Entry("ipv6 literal is never a bucket", "::1", []string{"s3.example.com"}, ""),
		Entry("too short for a bucket", "ab", []string{"s3.example.com"}, ""),
		Entry("no names configured", "bkt.s3.example.com", nil, ""),
		Entry("only empty names configured", "www.example.org", []string{""}, ""),
		Entry("suffix without a dot keeps looking", "xs3.example.com", []string{"s3.example.com"}, "xs3.example.com"),
		Entry("the least matching name wins, in whatever order the names come",
			"a.x.s3.example.com", []string{"s3.example.com", "", "example.com", "x.s3.example.com"}, "a.x.s3"),
		Entry("an empty subdomain falls back to the whole host", ".s3.example.com", []string{"s3.example.com"}, ".s3.example.com"),
		Entry("no host", "", []string{"s3.example.com"}, ""),
	)

	DescribeTable("Resolve reads the bucket, the domain and the website match RGWREST::preprocess reads",
		func(host string, n Names, want Target) { Expect(Resolve(host, n)).To(Equal(want), "%q", host) },
		Entry("a website host is a website request at the service",
			"web.example.com", gateway(true),
			Target{Domain: "web.example.com", Website: true}),
		Entry("a bucket subdomain of a website host names the bucket, in the Host's case",
			"Site.WEB.example.com", gateway(true),
			Target{Bucket: "Site", Domain: "WEB.example.com", Website: true}),
		Entry("a website match replaces the S3 match the Host is also in",
			"site.web.example.com", Names{S3: []string{"example.com"}, Website: []string{"web.example.com"}, WebsiteAPI: true},
			Target{Bucket: "site", Domain: "web.example.com", Website: true}),
		Entry("an empty subdomain of a website host falls back to the whole Host, still a website request",
			".web.example.com", gateway(true),
			Target{Bucket: ".web.example.com", Domain: "web.example.com", Website: true}),
		Entry("a website name equal to the Host replaces the S3 subdomain",
			"site.example.com", Names{S3: []string{"example.com"}, Website: []string{"site.example.com"}, WebsiteAPI: true},
			Target{Domain: "site.example.com", Website: true}),
		Entry("the least matching website name, in whatever order the names come",
			"a.x.web.example.com", Names{Website: []string{"x.web.example.com", "", "web.example.com"}, WebsiteAPI: true},
			Target{Bucket: "a.x", Domain: "web.example.com", Website: true}),
		Entry("a plain S3 host is no website request",
			"site.s3.example.com", gateway(true),
			Target{Bucket: "site", Domain: "s3.example.com"}),
		Entry("a website host without s3website falls back to the whole Host, not a website request",
			"site.web.example.com", gateway(false),
			Target{Bucket: "site.web.example.com"}),
		Entry("a CNAME to a website host is the whole Host's bucket, not a website request",
			"www.example.org", gateway(true),
			Target{Bucket: "www.example.org"}),
		Entry("website names alone let the Host name the bucket",
			"www.example.org", Names{Website: []string{"web.example.com"}},
			Target{Bucket: "www.example.org"}),
		Entry("website names that are all empty do not",
			"www.example.org", Names{Website: []string{""}, WebsiteAPI: true},
			Target{}),
		Entry("an IP literal names no bucket under website names alone",
			"192.0.2.1", Names{Website: []string{"web.example.com"}, WebsiteAPI: true},
			Target{}),
		Entry("no Host", "", gateway(true), Target{}),
	)

	DescribeTable("LooksLikeIPAddress is looks_like_ip_address",
		func(s string, want bool) { Expect(LooksLikeIPAddress(s)).To(Equal(want)) },
		Entry("dotted quad", "10.0.0.1", true),
		Entry("digits of any value", "999.1.2.3", true),
		Entry("a trailing period counts as the third", "1.2.3.", true),
		Entry("two periods", "1.2.3", false),
		Entry("four periods", "1.2.3.4.5", false),
		Entry("a leading period", ".1.2.3", false),
		Entry("a doubled period", "1..2.3", false),
		Entry("ipv6", "::1", true),
		Entry("ipv4-mapped ipv6", "::ffff:10.0.0.1", true),
		Entry("a hostname", "s3.example.com", false),
	)

	DescribeTable("validBucketName is validate_bucket_name",
		func(s string, want bool) { Expect(validBucketName(s)).To(Equal(want)) },
		Entry("empty names no bucket", "", true),
		Entry("two bytes", "ab", false),
		Entry("three bytes", "abc", true),
		Entry("255 bytes", strings.Repeat("a", 255), true),
		Entry("256 bytes", strings.Repeat("a", 256), false),
		Entry("a slash", "a/bc", false),
		Entry("a 0xff byte", "ab\xff", false),
		Entry("another byte above 0x7f", "ab\xfe", true),
	)
})
