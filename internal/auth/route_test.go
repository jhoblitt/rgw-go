package auth

import (
	"bufio"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/vhost"
)

// A request is served on the path and arguments the S3 handler and the
// router read, and authenticated over the canonical forms below; these specs
// hold the two to the same request target, as radosgw holds decoded_uri and
// the canonical forms to req_info's request_uri and request_params.
var _ = Describe("the route and the signatures", func() {
	var cfg s3.Config
	BeforeEach(func() {
		cfg = s3.Config{DNSNames: []string{"s3.example.com"}}
	})

	read := func(target, host string) *http.Request {
		raw := "GET " + target + " HTTP/1.1\r\nHost: " + host + "\r\n\r\n"
		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
		Expect(err).NotTo(HaveOccurred(), "net/http reads %q", target)
		return req
	}

	// signedV4Route is the path SigV4 authenticates: the bucket the signed
	// Host names, decoded on its own, in front of the decoded canonical URI,
	// as preprocess puts that bucket in front of the path it routes by. The
	// route decodes the two together, so an escape that does not decode
	// within the bucket alone tells them apart.
	signedV4Route := func(req *http.Request, rv *requestView) string {
		path := rgwtext.URLDecode(canonicalURIV4(rv.rawPath), false)
		b := vhost.Resolve(vhost.Hostname(req.Host), cfg.HostNames()).Bucket
		if b == "" {
			return path
		}
		bucket := rgwtext.URLDecode("/"+b, false)
		if strings.HasPrefix(path, "/") {
			return bucket + path
		}
		return bucket + "/" + path
	}

	DescribeTable("agree on every target the router serves",
		func(target, host string) {
			req := read(target, host)
			Expect(s3.MalformedPath(req, cfg.HostNames())).To(BeFalse(), "the router serves %q", target)
			route := s3.DecodedURI(req, cfg.HostNames())
			rv := newRequestView(req)

			resource, _, _ := strings.Cut(canonicalResourceV2(rv, cfg.HostNames()), "?")
			Expect(route).To(Equal(rgwtext.URLDecode(resource, false)), "SigV2 canonical resource %q", resource)
			Expect(route).To(Equal(signedV4Route(req, rv)), "SigV4 canonical URI %q", canonicalURIV4(rv.rawPath))

			p, err := s3.ParseRequest(req, cfg, time.Unix(1, 0))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.RawPath).To(Equal(rv.rawPath), "the path SigV4 canonicalizes")
			Expect(p.Req.RawQuery).To(Equal(rv.rawQuery), "the query SigV4 canonicalizes")
			args := url.Values{}
			for _, m := range []map[string]string{rv.params, rv.sysParams} {
				for k, v := range m {
					args[k] = []string{v}
				}
			}
			Expect(p.Req.Query).To(Equal(args), "the arguments SigV2 reads its sub-resources from")
		},
		Entry("origin form", "/b/k?acl&versionId=v1", "s3.example.com"),
		Entry("absolute form", "http://s3.example.com/b/k?acl", "s3.example.com"),
		Entry("absolute form over https", "https://s3.example.com/b/k?uploads", "s3.example.com"),
		Entry("absolute form over wss", "wss://s3.example.com/b/k", "s3.example.com"),
		Entry("a query holding the target's first slash", "http://s3.example.com?x=/b/k", "s3.example.com"),
		Entry("a query holding the first slash and a second ?", "http://s3.example.com?versionId=v1&x/b/k?acl", "s3.example.com"),
		Entry("a query holding the first slash and a second ? of rgwx- arguments", "http://s3.example.com?x/b/k?rgwx-uid=u", "s3.example.com"),
		Entry("an absolute-form target without a slash", "http://s3.example.com", "s3.example.com"),
		Entry("a query-only absolute-form target", "http://s3.example.com?acl", "s3.example.com"),
		Entry("another scheme, kept whole", "ftp://s3.example.com/b/k", "s3.example.com"),
		Entry("an upper-case scheme, kept whole", "HTTP://s3.example.com/b/k", "s3.example.com"),
		Entry("a scheme without an authority, kept whole", "foo:/b/k", "s3.example.com"),
		Entry("an opaque target", "foo:b%2Fk?acl", "s3.example.com"),
		Entry("the asterisk form", "*", "s3.example.com"),
		Entry("an encoded slash", "/b/a%2Fb", "s3.example.com"),
		Entry("an encoded slash in a query-held path", "http://s3.example.com?x=/b/a%2Fb", "s3.example.com"),
		Entry("an encoded space", "/b/a%20b", "s3.example.com"),
		Entry("an encoded percent sign", "/b/a%252Fb", "s3.example.com"),
		Entry("a plus", "/b/a+b", "s3.example.com"),
		Entry("a plus in a query-held path", "http://s3.example.com?x/b/a+b?", "s3.example.com"),
		Entry("a semicolon", "/b/k;v=1", "s3.example.com"),
		Entry("a double slash", "/b//k", "s3.example.com"),
		Entry("a leading double slash", "//b/k", "s3.example.com"),
		Entry("the root", "/", "s3.example.com"),
		Entry("a virtual-hosted key", "/k?acl", "b.s3.example.com"),
		Entry("a virtual-hosted root", "/", "b.s3.example.com"),
		Entry("a virtual-hosted query-held path", "http://b.s3.example.com?x/k?acl", "s3.example.com"),
		Entry("a virtual-hosted target without a slash", "http://b.s3.example.com", "s3.example.com"),
		Entry("a virtual-hosted encoded slash", "/a%2Fb", "b.s3.example.com"),
		Entry("a Host whose bucket holds an escape", "/k", "my%2Dbucket.s3.example.com"),
		Entry("a Host that names no hostname, taken as the bucket", "/k", "bucket.example.org"),
	)

	// preprocess reads the Host once, the website names over the S3 ones,
	// into the request_uri SigV2 signs, so a signature names the bucket the
	// request is served from. One over the bucket the S3 names alone read
	// would let a presigned URL for b.web, under the S3 name example.com,
	// reach the bucket b that the website name web.example.com names.
	DescribeTable("sign the bucket the router reads from a Host under the website names, for each rgw_enable_apis order",
		func(apis, s3Name, webName, host, bucket string) {
			a, err := cephconf.EnabledAPIs(cephconf.NewOptions(cephconf.MapGetter{"rgw_enable_apis": apis}))
			Expect(err).NotTo(HaveOccurred())
			cfg = s3.Config{DNSNames: []string{s3Name}, WebsiteNames: []string{webName}, Website: a.Website, WebsiteFirst: a.WebsiteFirst}
			req := read("/k", host)
			p, err := s3.ParseRequest(req, cfg, time.Unix(1, 0))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Bucket).To(Equal(bucket), "the bucket the request is served from")

			route := s3.DecodedURI(req, cfg.HostNames())
			rv := newRequestView(req)
			resource, _, _ := strings.Cut(canonicalResourceV2(rv, cfg.HostNames()), "?")
			Expect(rgwtext.URLDecode(resource, false)).To(Equal(route), "SigV2 canonical resource %q", resource)
			signed, _, _ := strings.Cut(strings.TrimPrefix(resource, "/"), "/")
			Expect(signed).To(Equal(bucket), "the bucket SigV2 signs")
			Expect(route).To(Equal(signedV4Route(req, rv)), "SigV4 canonical URI")
		},
		Entry("s3 before s3website: a website host", "s3, s3website", "s3.example.com", "web.example.com", "web.example.com", "k"),
		Entry("s3 before s3website: a bucket subdomain of a website host",
			"s3, s3website", "s3.example.com", "web.example.com", "site.web.example.com", "site"),
		Entry("s3 before s3website: a CNAME to a website host", "s3, s3website", "s3.example.com", "web.example.com", "www.example.org", "www.example.org"),
		Entry("s3 before s3website: a plain S3 host", "s3, s3website", "s3.example.com", "web.example.com", "site.s3.example.com", "site"),
		Entry("s3 before s3website: no Host", "s3, s3website", "s3.example.com", "web.example.com", "", "k"),
		Entry("s3website before s3: a website host", "s3website, s3", "s3.example.com", "web.example.com", "web.example.com", "k"),
		Entry("s3website before s3: a bucket subdomain of a website host",
			"s3website, s3", "s3.example.com", "web.example.com", "site.web.example.com", "site"),
		Entry("s3website before s3: a CNAME to a website host", "s3website, s3", "s3.example.com", "web.example.com", "www.example.org", "www.example.org"),
		Entry("s3website before s3: a plain S3 host", "s3website, s3", "s3.example.com", "web.example.com", "site.s3.example.com", "site"),
		Entry("s3website before s3: no Host", "s3website, s3", "s3.example.com", "web.example.com", "", "k"),
		Entry("s3website without s3: a website host", "s3website", "s3.example.com", "web.example.com", "web.example.com", "k"),
		Entry("s3website without s3: a bucket subdomain of a website host",
			"s3website", "s3.example.com", "web.example.com", "site.web.example.com", "site"),
		Entry("s3website without s3: a CNAME to a website host", "s3website", "s3.example.com", "web.example.com", "www.example.org", "www.example.org"),
		Entry("s3website without s3: a plain S3 host", "s3website", "s3.example.com", "web.example.com", "site.s3.example.com", "site"),
		Entry("s3website without s3: no Host", "s3website", "s3.example.com", "web.example.com", "", "k"),
		Entry("s3 without s3website: a website host, taken whole as the bucket",
			"s3", "s3.example.com", "web.example.com", "web.example.com", "web.example.com"),
		Entry("s3 without s3website: a bucket subdomain of a website host, taken whole as the bucket",
			"s3", "s3.example.com", "web.example.com", "site.web.example.com", "site.web.example.com"),
		Entry("s3 without s3website: a CNAME to a website host", "s3", "s3.example.com", "web.example.com", "www.example.org", "www.example.org"),
		Entry("s3 without s3website: a plain S3 host", "s3", "s3.example.com", "web.example.com", "site.s3.example.com", "site"),
		Entry("s3 without s3website: no Host", "s3", "s3.example.com", "web.example.com", "", "k"),
		Entry("a website name inside the S3 name, which the website API reads first",
			"s3, s3website", "example.com", "web.example.com", "b.web.example.com", "b"),
		Entry("a website name inside the S3 name, without the website API",
			"s3", "example.com", "web.example.com", "b.web.example.com", "b.web"),
		Entry("an S3 name inside the website name, which the website API still reads first",
			"s3, s3website", "web.example.com", "example.com", "b.web.example.com", "b.web"),
		Entry("an S3 name inside the website name, without the website API",
			"s3", "web.example.com", "example.com", "b.web.example.com", "b"),
		Entry("a website name alone, which lets a Host that names no hostname name the bucket whole",
			"s3, s3website", "", "web.example.com", "bucket.example.org", "bucket.example.org"),
		Entry("a website name alone without the website API, which counts toward the configured names all the same",
			"s3", "", "web.example.com", "bucket.example.org", "bucket.example.org"),
		Entry("a website host with a port, which req_info drops",
			"s3, s3website", "s3.example.com", "web.example.com", "site.web.example.com:8080", "site"),
		Entry("a website host in mixed case, the bucket in the Host's case",
			"s3, s3website", "s3.example.com", "web.example.com", "Site.WEB.Example.com", "Site"),
		Entry("a website host with a trailing dot, which ends in no name and is taken whole",
			"s3, s3website", "s3.example.com", "web.example.com", "site.web.example.com.", "site.web.example.com."),
		Entry("an IPv4 literal, never a bucket", "s3, s3website", "s3.example.com", "web.example.com", "192.0.2.1:7480", "k"),
		Entry("an IPv6 literal, never a bucket", "s3, s3website", "s3.example.com", "web.example.com", "[2001:db8::1]:7480", "k"),
		Entry("a name that is both an S3 and a website name", "s3, s3website", "example.com", "example.com", "b.example.com", "b"),
		Entry("a website name that is a suffix of the S3 name, which the website API reads first",
			"s3, s3website", "s3.example.com", "example.com", "b.s3.example.com", "b.s3"),
		Entry("a website name that is a suffix of the S3 name, without the website API",
			"s3", "s3.example.com", "example.com", "b.s3.example.com", "b"),
	)

	DescribeTable("leave to the router's refusal what radosgw would route elsewhere",
		func(target, host string) {
			Expect(s3.MalformedPath(read(target, host), cfg.HostNames())).To(BeTrue(), "the router refuses %q", target)
		},
		Entry("a bad hex digit in a query-held path", "http://s3.example.com?x=/b/%zz", "s3.example.com"),
		Entry("a truncated escape in a query-held path", "http://s3.example.com?x=/b/k%4", "s3.example.com"),
		Entry("a bad hex digit in a virtual-hosted query-held path", "http://b.s3.example.com?x/%zz?", "s3.example.com"),
		Entry("a bad hex digit in a target kept whole", "foo:%zz", "b.s3.example.com"),
		Entry("a bad hex digit in the Host's bucket", "/k", "my%zz.s3.example.com"),
		Entry("a lone percent sign ending the Host's bucket", "/", "my%.s3.example.com"),
	)

	// url_decode empties the whole URI at a bad hex digit, the Host's
	// bucket with it, and at a '%' the next two bytes do not complete, while
	// SigV4 signs the bucket in the Host and the path apart from it.
	DescribeTable("would route what the router refuses off the path SigV4 signs",
		func(target, host, route, signed string) {
			req := read(target, host)
			Expect(s3.DecodedURI(req, cfg.HostNames())).To(Equal(route), "radosgw's decoded_uri")
			Expect(signedV4Route(req, newRequestView(req))).To(Equal(signed), "SigV4's")
		},
		Entry("a bad hex digit in the path, which SigV4 signs as the bucket's root",
			"http://b.s3.example.com?x/%zz?", "s3.example.com", "", "/b/"),
		Entry("a bad hex digit in the Host's bucket, which SigV4 signs apart from the key",
			"/k", "my%zz.s3.example.com", "", "/k"),
		Entry("a percent sign ending the Host's bucket, which the path's slash cannot complete",
			"/k", "my%.s3.example.com", "", "/my/k"),
	)
})
