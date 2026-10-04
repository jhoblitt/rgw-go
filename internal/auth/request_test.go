package auth

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func rawRequest(ctx context.Context, method, target string, hdr map[string][]string) *http.Request {
	req := httptest.NewRequestWithContext(ctx, method, target, nil)
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req
}

var _ = Describe("requestView", func() {
	It("parses the query as RGWHTTPArgs does", func(ctx SpecContext) {
		rv := newRequestView(rawRequest(ctx, http.MethodGet, "/b/k?X-Amz-Credential=AK%2F20150830%2Fus%2Fs3%2Faws4_request&acl&versionId=v1&rgwx-uid=u1&x=1&x=2&a+b=c+d&X-AMZ-Date=x", nil))
		Expect(rv.params).To(HaveKeyWithValue("x-amz-credential", "AK/20150830/us/s3/aws4_request"), "decoded once, key lowercased except dashes")
		Expect(rv.params).NotTo(HaveKey("X-Amz-Credential"))
		Expect(rv.params).To(HaveKey("X-AMZ-Date"), "only the exact spelling X-Amz- is lowercased (rgw_common.cc:870)")
		Expect(rv.params).To(HaveKeyWithValue("acl", ""))
		Expect(rv.params).To(HaveKeyWithValue("x", "2"), "a repeated key keeps the last value")
		Expect(rv.params).To(HaveKeyWithValue("a b", "c d"), "+ is a space in the query")
		v, ok := rv.param("x-amz-credential")
		Expect(v).To(Equal("AK/20150830/us/s3/aws4_request"), "param reads val_map")
		Expect(ok).To(BeTrue(), "param reports x-amz-credential as present")
		_, ok = rv.param("rgwx-uid")
		Expect(ok).To(BeFalse(), "param does not read the rgwx- names")
		Expect(rv.subres).To(HaveKeyWithValue("acl", ""))
		Expect(rv.subres).To(HaveKeyWithValue("versionId", "v1"))
		Expect(rv.subres).NotTo(HaveKey("x"))
		Expect(rv.sysParams).To(HaveKeyWithValue("rgwx-uid", "u1"))
		Expect(rv.params).NotTo(HaveKey("rgwx-uid"))
		Expect(rv.rawQuery).To(Equal("X-Amz-Credential=AK%2F20150830%2Fus%2Fs3%2Faws4_request&acl&versionId=v1&rgwx-uid=u1&x=1&x=2&a+b=c+d&X-AMZ-Date=x"))
	})

	It("decodes a name=value pair before splitting it at its first =", func(ctx SpecContext) {
		rv := newRequestView(rawRequest(ctx, http.MethodGet, "/b?a%3Db=c", nil))
		Expect(rv.params).To(HaveKeyWithValue("a", "b=c"))
	})

	It("keeps the raw path and reduces an absolute-form target", func(ctx SpecContext) {
		rv := newRequestView(rawRequest(ctx, http.MethodGet, "/a%2Fb+c%41?x=1", nil))
		Expect(rv.rawPath).To(Equal("/a%2Fb+c%41"))
		abs := rawRequest(ctx, http.MethodGet, "/", nil)
		abs.RequestURI = "http://bkt.example.com:8080/k%20v?y"
		abs.Host = "bkt.example.com:8080"
		rv = newRequestView(abs)
		Expect(rv.rawPath).To(Equal("/k%20v"))
		Expect(rv.rawQuery).To(Equal("y"))
	})

	It("reduces an absolute-form target before splitting off the query, as req_info does", func(ctx SpecContext) {
		abs := rawRequest(ctx, http.MethodGet, "/", nil)
		abs.RequestURI = "http://h?x=/y"
		rv := newRequestView(abs)
		Expect(rv.rawPath).To(Equal("/y"), "get_abs_path finds the slash in the query")
		Expect(rv.rawQuery).To(Equal("x=/y"), "request_params falls back to beast's QUERY_STRING")
		other := rawRequest(ctx, http.MethodGet, "/", nil)
		other.RequestURI = "ftp://h/k?q"
		rv = newRequestView(other)
		Expect(rv.rawPath).To(Equal("ftp://h/k"), "only http, https, ws and wss targets are reduced")
		Expect(rv.rawQuery).To(Equal("q"))
	})

	It("adds only the first admin sub-resource, with an empty value", func(ctx SpecContext) {
		rv := newRequestView(rawRequest(ctx, http.MethodGet, "/b?quota=x&policy=y", nil))
		Expect(rv.subres).To(Equal(map[string]string{"quota": ""}))
	})

	It("resolves signed-header tokens the way beast's env does", func(ctx SpecContext) {
		req := rawRequest(ctx, http.MethodPut, "/b/k", map[string][]string{
			"X-Amz-Date":         {"20150830T123600Z", "20150830T123700Z"},
			"x-amz-meta-foo_bar": {"v"},
			"Content-Type":       {"text/plain"},
			"Content-Length":     {"5"},
		})
		req.Host = "example.com:8080"
		rv := newRequestView(req)
		v, _ := rv.header("host")
		Expect(v).To(Equal("example.com:8080"), "the raw Host, port included")
		v, _ = rv.header("x-amz-date")
		Expect(v).To(Equal("20150830T123700Z"), "the last of duplicate headers")
		v, _ = rv.header("X-AMZ-DATE")
		Expect(v).To(Equal("20150830T123700Z"), "RGWEnv's map ignores case")
		v, _ = rv.header("x-amz-meta-foo_bar")
		Expect(v).To(Equal("v"), "a name with an underscore")
		_, ok := rv.header("x-amz-meta-foo-bar")
		Expect(ok).To(BeFalse(), "beast and the token transform both swap - and _, so they stay distinct")
		v, _ = rv.header("content-type")
		Expect(v).To(Equal("text/plain"))
		v, _ = rv.header("content-length")
		Expect(v).To(Equal("5"))
		_, ok = rv.header("x-amz-missing")
		Expect(ok).To(BeFalse(), "x-amz-missing was not sent")
		Expect(rv.hasHeader("content-type")).To(BeTrue(), "Content-Type was sent")
		Expect(rv.hasHeader("content-md5")).To(BeFalse(), "Content-MD5 was not sent")
		Expect(rv.chunkedTE()).To(BeFalse(), "no Transfer-Encoding was sent")
	})

	It("reports a chunked transfer encoding", func(ctx SpecContext) {
		req := rawRequest(ctx, http.MethodPut, "/b/k", nil)
		req.TransferEncoding = []string{"chunked"}
		req.ContentLength = -1
		rv := newRequestView(req)
		Expect(rv.chunkedTE()).To(BeTrue(), "Transfer-Encoding: chunked was sent")
		Expect(rv.contentLength()).To(Equal(int64(-1)))
		v, _ := rv.header("transfer-encoding")
		Expect(v).To(Equal("chunked"))
	})

	It("takes an empty Host as sent empty on HTTP/1.1 and as absent on HTTP/1.0", func(ctx SpecContext) {
		req := rawRequest(ctx, http.MethodGet, "/b/k", nil)
		req.Host = ""
		v, ok := newRequestView(req).header("host")
		Expect(v).To(BeEmpty())
		Expect(ok).To(BeTrue(), "HTTP/1.1 requires a Host header, so an empty one was sent")
		req.Proto, req.ProtoMajor, req.ProtoMinor = "HTTP/1.0", 1, 0
		_, ok = newRequestView(req).header("host")
		Expect(ok).To(BeFalse(), "HTTP/1.0 needs no Host header, and net/http leaves an empty one and none alike")
	})

	It("reports SERVER_PORT and SERVER_PORT_SECURE from the listener", func(ctx SpecContext) {
		req := rawRequest(ctx, http.MethodGet, "/", nil)
		port, secure := newRequestView(req).localPort()
		Expect(port).To(BeEmpty(), "no listener address")
		Expect(secure).To(BeFalse(), "no TLS connection")
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv6loopback, Port: 8443}))
		req.TLS = &tls.ConnectionState{}
		port, secure = newRequestView(req).localPort()
		Expect(port).To(Equal("8443"))
		Expect(secure).To(BeTrue(), "a TLS connection")
	})

	DescribeTable("discoverFlavour picks the signature version and route as radosgw does",
		func(ctx SpecContext, auth, query string, wantV version, wantR route) {
			req := rawRequest(ctx, http.MethodGet, "/?"+query, nil)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			v, r := discoverFlavour(newRequestView(req))
			Expect(v).To(Equal(wantV))
			Expect(r).To(Equal(wantR))
		},
		Entry("v4 header", "AWS4-HMAC-SHA256 Credential=a/b/c/d/aws4_request, SignedHeaders=host, Signature=x", "", versionV4, routeHeaders),
		Entry("v2 header", "AWS AK:sig", "", versionV2, routeHeaders),
		Entry("unknown header scheme", "Bearer tok", "", versionUnknown, routeHeaders),
		Entry("empty header is the query route", "", "", versionUnknown, routeQuery),
		Entry("v4 query", "", "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=x", versionV4, routeQuery),
		Entry("v4 query needs the X-Amz- spelling", "", "X-AMZ-Algorithm=AWS4-HMAC-SHA256", versionUnknown, routeQuery),
		Entry("v2 query", "", "AWSAccessKeyId=AK&Signature=s&Expires=1", versionV2, routeQuery),
		Entry("header wins over query", "AWS AK:sig", "X-Amz-Algorithm=AWS4-HMAC-SHA256", versionV2, routeHeaders),
	)

	It("takes the query route when the Authorization header is present but empty", func(ctx SpecContext) {
		req := rawRequest(ctx, http.MethodGet, "/?AWSAccessKeyId=AK&Signature=s&Expires=1", map[string][]string{"Authorization": {""}})
		v, r := discoverFlavour(newRequestView(req))
		Expect(v).To(Equal(versionV2))
		Expect(r).To(Equal(routeQuery))
	})
})
