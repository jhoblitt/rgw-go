package vhost

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("vhost", func() {
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
