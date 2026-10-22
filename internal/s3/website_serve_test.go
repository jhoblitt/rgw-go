package s3_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// siteHost is the Host of bucket site on the website endpoint of
// websiteGateway.
const siteHost = "site.web.example.com"

// websiteWorld is a gateway serving S3 at s3.example.com and the website
// endpoint at web.example.com, with alice's public-read bucket site.
type websiteWorld struct {
	store *memstore.Store
	env   *op.Env
	alice *op.UserRecord
	site  *op.BucketRecord
	// apis is the gateway's rgw_enable_apis.
	apis string
}

func newWebsiteWorld(ctx context.Context, rel denc.Release) *websiteWorld {
	GinkgoHelper()
	store := memstore.New(memstore.Config{Release: rel, Now: func() time.Time { return readMtime }})
	w := &websiteWorld{store: store, apis: "s3, s3website"}
	w.alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
	w.env = testEnv(store)
	w.env.Authz = authz.New(authz.DefaultConfig(rel))
	var err error
	w.site, err = store.CreateBucket(ctx, op.CreateBucketParams{
		Name: "site", Owner: meta.UserOwner(w.alice.Info.UserID), Placement: meta.PlacementRule{Name: "default-placement"},
		Attrs: map[string][]byte{meta.AttrACL: w.canned("public-read")},
	})
	Expect(err).NotTo(HaveOccurred())
	return w
}

// canned is alice's canned ACL.
func (w *websiteWorld) canned(name string) []byte {
	GinkgoHelper()
	owner := acl.Owner{ID: "alice", DisplayName: "Alice"}
	p, err := acl.Canned(owner, owner, name)
	Expect(err).NotTo(HaveOccurred())
	return encodeACL(p)
}

// setWebsite stores conf as site's website configuration, none for nil.
func (w *websiteWorld) setWebsite(ctx context.Context, conf *meta.WebsiteConf) {
	GinkgoHelper()
	rec, err := w.store.GetBucket(ctx, "", "site")
	Expect(err).NotTo(HaveOccurred())
	rec.Info.Website = conf
	Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
}

// put writes a public-read object key holding body, of type text/html, with
// attrs besides.
func (w *websiteWorld) put(ctx context.Context, key, body string, attrs map[string][]byte) {
	GinkgoHelper()
	set := map[string][]byte{meta.AttrACL: w.canned("public-read"), meta.AttrContentType: []byte("text/html\x00")}
	maps.Copy(set, attrs)
	rec, err := w.store.GetBucket(ctx, "", "site")
	Expect(err).NotTo(HaveOccurred())
	_, err = w.store.PutObject(ctx, rec, meta.ObjKey{Name: key}, strings.NewReader(body), op.PutParams{Size: int64(len(body)), Attrs: set})
	Expect(err).NotTo(HaveOccurred())
}

// serve is serveHost to site's website Host.
func (w *websiteWorld) serve(who *op.UserRecord, method, target string, hdr ...string) *httptest.ResponseRecorder {
	GinkgoHelper()
	return w.serveHost(siteHost, who, method, target, hdr...)
}

// serveHost sends method and target to host through a handler that
// authenticates as who, anonymously for nil; hdr alternates header names
// and values.
func (w *websiteWorld) serveHost(host string, who *op.UserRecord, method, target string, hdr ...string) *httptest.ResponseRecorder {
	GinkgoHelper()
	var auth s3.Authenticator = s3.AnonymousOnly{}
	if who != nil {
		auth = authAs(who)
	}
	return w.serveWith(auth, host, method, target, hdr...)
}

func (w *websiteWorld) serveWith(auth s3.Authenticator, host, method, target string, hdr ...string) *httptest.ResponseRecorder {
	GinkgoHelper()
	req := httptest.NewRequestWithContext(GinkgoT().Context(), method, target, nil)
	req.Host = host
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return w.serveRequest(auth, req)
}

func (w *websiteWorld) serveRequest(auth s3.Authenticator, req *http.Request) *httptest.ResponseRecorder {
	GinkgoHelper()
	rec := httptest.NewRecorder()
	s3.NewHandler(w.env, auth, testConfig(websiteGateway(w.apis))).ServeHTTP(rec, req)
	return rec
}

// htmlPage is the page radosgw's website endpoint answers an error with:
// its title, then one list item per field.
func htmlPage(title string, items ...string) string {
	var b strings.Builder
	b.WriteString("<html>\n <head><title>" + title + "</title></head>\n <body>\n  <h1>" + title + "</h1>\n  <ul>\n")
	for _, it := range items {
		b.WriteString("   <li>" + it + "</li>\n")
	}
	b.WriteString("  </ul>\n </body>\n</html>\n")
	return b.String()
}

// expectHTMLError checks rec is radosgw's website error page for status and
// code, naming bucket unless it is empty, with the headers end_header's
// error branch sends.
func expectHTMLError(rec *httptest.ResponseRecorder, status int, title, code, message, bucket string) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(status), rec.Body.String())
	id := exactHeader(rec.Header(), "x-amz-request-id")
	Expect(id).NotTo(BeEmpty())
	var items []string
	if code != "" {
		items = append(items, "Code: "+code)
	}
	items = append(items, "Message: "+message)
	if bucket != "" {
		items = append(items, "BucketName: "+bucket)
	}
	items = append(items, "RequestId: "+id, "HostId: 4155-z-zg")
	want := htmlPage(title, items...)
	Expect(rec.Body.String()).To(Equal(want))
	Expect(rec.Header().Get("Content-Type")).To(Equal("text/html"), "to_mime_type(HTML), rgw_common.h:198-199 at v19.2.7")
	Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
	Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "dump_content_length, rgw_rest.cc:388-397")
	Expect(rec.Header()).NotTo(haveHeaderAnyCase("Location"))
}

// expectRedirect checks rec is a website redirect to location at status
// with no body, as abort_early sends one.
func expectRedirect(rec *httptest.ResponseRecorder, status int, location string) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(status), rec.Body.String())
	Expect(exactHeader(rec.Header(), "Location")).To(Equal(location))
	Expect(rec.Body.String()).To(BeEmpty())
	Expect(rec.Header()).NotTo(haveHeaderAnyCase("Content-Type"))
	Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "abort_early names no length; the frontend's carries no Accept-Ranges")
}

// failingRead is an object store whose head read of key fails with err.
type failingRead struct {
	op.ObjectStore
	key string
	err error
}

func (f failingRead) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	if key.Name == f.key {
		return nil, f.err
	}
	return f.ObjectStore.PrefetchObject(ctx, rec, key)
}

// rule is a routing rule for keys under prefix and errors at status, 0 for
// none, that replaces the prefix with to on host, the request's when it is
// empty, answering code, WebsiteRedirect's when it is 0.
func rule(prefix string, status uint16, host, to string, code uint16) meta.RoutingRule {
	return meta.RoutingRule{
		Condition: meta.RoutingCondition{KeyPrefixEquals: prefix, HTTPErrorCodeReturnedEquals: status},
		RedirectInfo: meta.RoutingRedirect{
			Redirect:             meta.RedirectInfo{Hostname: host, HTTPRedirectCode: code},
			ReplaceKeyPrefixWith: to,
		},
	}
}

var _ = Describe("the website endpoint", func() {
	var w *websiteWorld
	BeforeEach(func(ctx SpecContext) { w = newWebsiteWorld(ctx, denc.Squid) })

	DescribeTable("answers every method but GET and HEAD 405 in HTML, naming no bucket, before it authenticates or reads anything",
		func(method, target string) {
			authenticated := false
			auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				authenticated = true
				return &op.AuthResult{Identity: op.Anonymous()}, nil
			})
			rec := w.serveWith(auth, siteHost, method, target)
			expectHTMLError(rec, http.StatusMethodNotAllowed, "405 Method Not Allowed", "MethodNotAllowed", "", "")
			Expect(authenticated).To(BeFalse(), "get_op refuses at rgw_process.cc:325-329, ahead of verify_requester at :343")
			Expect(w.store.Usage()).To(BeEmpty(), "radosgw files a request with no op under no user, which its flush drops")
		},
		Entry("PUT of an object", http.MethodPut, "/k"),
		Entry("PUT of the bucket's website configuration", http.MethodPut, "/?website"),
		Entry("POST, a browser upload's method", http.MethodPost, "/"),
		Entry("DELETE of an object", http.MethodDelete, "/k"),
		Entry("OPTIONS, a CORS preflight's method", http.MethodOptions, "/k"),
		Entry("a method no S3 op serves", "PATCH", "/k"),
	)

	It("renders a refusal after authentication in HTML", func() {
		auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return nil, op.ErrSignatureDoesNotMatch
		})
		rec := w.serveWith(auth, siteHost, http.MethodGet, "/k")
		expectHTMLError(rec, http.StatusForbidden, "403 Forbidden", "SignatureDoesNotMatch", "", "")
	})

	It("sends a HEAD the page's length and no body, for a refusal before dispatch too", func(ctx SpecContext) {
		srv := httptest.NewServer(s3.NewHandler(w.env, s3.AnonymousOnly{}, testConfig(websiteGateway("s3, s3website"))))
		DeferCleanup(srv.Close)
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, srv.URL+"/k", nil)
		Expect(err).NotTo(HaveOccurred())
		req.Host = siteHost
		req.Header.Set("X-Amz-Copy-Source", "nobucket")
		resp, err := srv.Client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(resp.Body.Close)
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(body).To(BeEmpty(), "rgw_flush_formatter_and_reset sends no HEAD a body, rgw_rest.cc:303-314")
		Expect(resp.Header.Get("Content-Type")).To(Equal("text/html"))
		page := htmlPage("400 Bad Request", "Code: InvalidArgument", "Message: ",
			"RequestId: "+resp.Header.Get("x-amz-request-id"), "HostId: 4155-z-zg")
		Expect(resp.Header.Get("Content-Length")).To(Equal(strconv.Itoa(len(page))),
			"RGWHandler_REST_S3::init refuses the copy source with the formatter allocated, rgw_rest_s3.cc:5026-5041 at v19.2.6")
	})

	It("keeps S3 requests' XML error documents", func() {
		rec := w.serveHost("site.s3.example.com", nil, http.MethodPut, "/k?website")
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), rec.Body.String())
		Expect(rec.Body.String()).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error>`))
	})

	DescribeTable("routes GET and HEAD to the website GetObject alone, at every scope and whatever the query names",
		func(ctx SpecContext, method, target, body string) {
			w.setWebsite(ctx, &meta.WebsiteConf{IndexDocSuffix: "index.html"})
			w.put(ctx, "index.html", "home", nil)
			w.put(ctx, "k", "kay", nil)
			rec := w.serve(w.alice, method, target)
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(rec.Body.String()).To(Equal(body))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(body))))
		},
		Entry("a GET of an object", http.MethodGet, "/k", "kay"),
		Entry("a bucket's GET, not ListObjects", http.MethodGet, "/", "home"),
		Entry("a bucket's ?acl, not GetBucketAcl", http.MethodGet, "/?acl", "home"),
		Entry("a bucket's ?website, not GetBucketWebsite", http.MethodGet, "/?website", "home"),
		Entry("a bucket's ?versionId, which S3 refuses at bucket scope", http.MethodGet, "/?versionId=v", "home"),
	)

	It("answers the service NoSuchBucket, naming none", func() {
		rec := w.serveHost("web.example.com", nil, http.MethodGet, "/")
		expectHTMLError(rec, http.StatusNotFound, "404 Not Found", "NoSuchBucket", "", "")
	})
})

var _ = Describe("the website GetObject", func() {
	var w *websiteWorld
	BeforeEach(func(ctx SpecContext) {
		w = newWebsiteWorld(ctx, denc.Squid)
		w.put(ctx, "index.html", "<p>home</p>", nil)
		w.put(ctx, "docs/index.html", "<p>docs</p>", nil)
		w.put(ctx, "a.txt", "plain text", map[string][]byte{meta.AttrContentType: []byte("text/plain\x00")})
		w.put(ctx, "error.html", "<p>oops</p>", map[string][]byte{meta.AttrCacheControl: []byte("no-cache\x00")})
	})
	site := func(rules ...meta.RoutingRule) *meta.WebsiteConf {
		return &meta.WebsiteConf{IndexDocSuffix: "index.html", RoutingRules: rules}
	}
	withErrorDoc := func(conf *meta.WebsiteConf) *meta.WebsiteConf {
		conf.ErrorDoc = "error.html"
		return conf
	}
	// expectErrorDoc checks rec is error.html under status, as
	// serve_errordoc's GetObject sends it with its custom status.
	expectErrorDoc := func(rec *httptest.ResponseRecorder, status int, head bool) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		if head {
			Expect(rec.Body.String()).To(BeEmpty())
		} else {
			Expect(rec.Body.String()).To(Equal("<p>oops</p>"))
		}
		Expect(rec.Header().Get("Content-Length")).To(Equal("11"))
		Expect(rec.Header().Get("Content-Type")).To(Equal("text/html"))
		Expect(rec.Header().Get("Cache-Control")).To(Equal("no-cache"))
		Expect(exactHeader(rec.Header(), "ETag")).To(Equal(`"` + md5Of([]byte("<p>oops</p>")) + `"`))
		Expect(rec.Header().Get("Last-Modified")).To(Equal(readLastModified))
		Expect(rec.Header()).NotTo(HaveKey("Content-Range"))
	}

	Describe("serving", func() {
		BeforeEach(func(ctx SpecContext) { w.setWebsite(ctx, site()) })

		It("serves an object with S3 GetObject's headers", func() {
			rec := w.serve(nil, http.MethodGet, "/a.txt")
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(Equal("plain text"))
			Expect(rec.Header().Get("Content-Type")).To(Equal("text/plain"))
			Expect(rec.Header().Get("Content-Length")).To(Equal("10"))
			Expect(exactHeader(rec.Header(), "ETag")).To(Equal(`"` + md5Of([]byte("plain text")) + `"`))
			Expect(rec.Header().Get("Last-Modified")).To(Equal(readLastModified))
		})
		It("serves a HEAD the headers alone", func() {
			rec := w.serve(nil, http.MethodHead, "/a.txt")
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(rec.Body.String()).To(BeEmpty())
			Expect(rec.Header().Get("Content-Length")).To(Equal("10"))
		})
		DescribeTable("serves the index document of a key that names no object",
			func(target, body string) {
				rec := w.serve(nil, http.MethodGet, target)
				Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
				Expect(rec.Body.String()).To(Equal(body))
			},
			Entry("the bucket", "/", "<p>home</p>"),
			Entry("a directory", "/docs", "<p>docs</p>"),
			Entry("a directory with its slash", "/docs/", "<p>docs</p>"),
		)
		It("answers a bucket with no website configuration NoSuchWebsiteConfiguration", func(ctx SpecContext) {
			w.setWebsite(ctx, nil)
			expectHTMLError(w.serve(nil, http.MethodGet, "/a.txt"), http.StatusNotFound, "404 Not Found", "NoSuchWebsiteConfiguration", "", "site")
		})
		It("answers a configuration with no index suffix with retarget's InvalidArgument", func(ctx SpecContext) {
			w.setWebsite(ctx, &meta.WebsiteConf{})
			expectHTMLError(w.serve(nil, http.MethodGet, "/a.txt"), http.StatusBadRequest, "400 Bad Request", "InvalidArgument",
				"The IndexDocument Suffix is not configurated or not well formed!", "site")
		})
		It("serves the error document of a configuration with no index suffix under retarget's 400", func(ctx SpecContext) {
			w.setWebsite(ctx, &meta.WebsiteConf{ErrorDoc: "error.html"})
			expectErrorDoc(w.serve(nil, http.MethodGet, "/a.txt"), http.StatusBadRequest, false)
		})
		It("answers a missing key with no error document as its NoSuchKey page", func() {
			expectHTMLError(w.serve(nil, http.MethodGet, "/missing.html"), http.StatusNotFound, "404 Not Found", "NoSuchKey", "", "site")
		})
		It("sends no x-amz-request-charged with an error page, as end_header adds it only to a success", func(ctx SpecContext) {
			rec, err := w.store.GetBucket(ctx, "", "site")
			Expect(err).NotTo(HaveOccurred())
			rec.Info.RequesterPays = true
			Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
			bob := w.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll})
			got := w.serve(bob, http.MethodGet, "/missing.html", "X-Amz-Request-Payer", "requester")
			Expect(got.Header().Get("Content-Type")).To(Equal("text/html"), got.Body.String())
			Expect(got.Header()).NotTo(haveHeaderAnyCase("x-amz-request-charged"))
			ok := w.serve(bob, http.MethodGet, "/a.txt", "X-Amz-Request-Payer", "requester")
			Expect(ok.Code).To(Equal(http.StatusOK), ok.Body.String())
			Expect(exactHeader(ok.Header(), "x-amz-request-charged")).To(Equal("requester"), "the success carries it")
		})
	})

	Describe("redirects", func() {
		DescribeTable("answer a routing rule matching the key at retarget with its Location and code, and no body",
			func(ctx SpecContext, code uint16, status int) {
				w.setWebsite(ctx, site(rule("old/", 0, "", "new/", code)))
				expectRedirect(w.serve(nil, http.MethodGet, "/old/x"), status, "http://site.web.example.com/new/x")
			},
			Entry("a code of 302, as stored", uint16(302), http.StatusFound),
			Entry("a code of 307, as stored", uint16(307), http.StatusTemporaryRedirect),
			Entry("no code, as WebsiteRedirect's 301", uint16(0), http.StatusMovedPermanently),
			Entry("a code of 200, as abort_early's WebsiteRedirect", uint16(200), http.StatusMovedPermanently),
			Entry("a code of 100, which radosgw writes as given", uint16(100), http.StatusMovedPermanently),
			Entry("a code of 101, which radosgw writes as given", uint16(101), http.StatusMovedPermanently),
			Entry("a code of 99, which radosgw writes as given", uint16(99), http.StatusMovedPermanently),
			Entry("a code of 1000, which radosgw writes as given", uint16(1000), http.StatusMovedPermanently),
		)
		It("answers a code from 400 on with that status, the Location and the page with no Code", func(ctx SpecContext) {
			w.setWebsite(ctx, site(rule("old/", 0, "", "new/", 418)))
			rec := w.serve(nil, http.MethodGet, "/old/x")
			Expect(exactHeader(rec.Header(), "Location")).To(Equal("http://site.web.example.com/new/x"))
			rec.Header().Del("Location")
			expectHTMLError(rec, 418, "418", "", "", "site")
		})
		It("answers a HEAD the same redirect", func(ctx SpecContext) {
			w.setWebsite(ctx, site(rule("old/", 0, "", "new/", 302)))
			expectRedirect(w.serve(nil, http.MethodHead, "/old/x"), http.StatusFound, "http://site.web.example.com/new/x")
		})
		It("redirects every key of a redirect-all with no index suffix at its stored code", func(ctx SpecContext) {
			w.setWebsite(ctx, &meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Protocol: "https", Hostname: "example.org", HTTPRedirectCode: 302}})
			expectRedirect(w.serve(nil, http.MethodGet, "/a.txt"), http.StatusFound, "https://example.org/a.txt")
		})
		It("answers a redirect-all over an index suffix 301, the code the error handler applies it again with", func(ctx SpecContext) {
			w.setWebsite(ctx, &meta.WebsiteConf{IndexDocSuffix: "index.html", RedirectAll: meta.RedirectInfo{Hostname: "example.org", HTTPRedirectCode: 302}})
			expectRedirect(w.serve(nil, http.MethodGet, "/a.txt"), http.StatusMovedPermanently, "http://example.org/a.txt")
		})
		It("lets a rule on 301 take over retarget's redirect, as the error handler matches WebsiteRedirect's status", func(ctx SpecContext) {
			w.setWebsite(ctx, site(rule("old/", 0, "", "new/", 302), rule("ol", 301, "other.example.org", "", 307)))
			expectRedirect(w.serve(nil, http.MethodGet, "/old/x"), http.StatusTemporaryRedirect, "http://other.example.org/old/x")
		})
		It("keeps retarget's code when the rule on 301 stores none", func(ctx SpecContext) {
			w.setWebsite(ctx, site(rule("old/", 0, "", "new/", 302), rule("ol", 301, "other.example.org", "", 0)))
			expectRedirect(w.serve(nil, http.MethodGet, "/old/x"), http.StatusFound, "http://other.example.org/old/x")
		})

		Describe("an object's x-amz-website-redirect-location", func() {
			BeforeEach(func(ctx SpecContext) {
				w.setWebsite(ctx, withErrorDoc(site(rule("", 301, "elsewhere.example.org", "", 302))))
			})
			DescribeTable("is 301 with a Content-Length of 0 and Accept-Ranges, from the op's own response, which no rule sees",
				func(ctx SpecContext, method string) {
					w.put(ctx, "moved", "body", map[string][]byte{meta.AttrWebsiteRedirect: []byte("/elsewhere\x00")})
					rec := w.serve(nil, method, "/moved")
					Expect(rec.Code).To(Equal(http.StatusMovedPermanently))
					Expect(exactHeader(rec.Header(), "Location")).To(Equal("/elsewhere"))
					Expect(rec.Body.String()).To(BeEmpty())
					Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
					Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "dump_content_length(s, 0), rgw_rest_s3.cc:285 at v19.2.7")
				},
				Entry("GET", http.MethodGet),
				Entry("HEAD", http.MethodHead),
			)
			It("sends no Location for an empty attr, as dump_redirect skips an empty one", func(ctx SpecContext) {
				w.put(ctx, "moved", "body", map[string][]byte{meta.AttrWebsiteRedirect: []byte("\x00")})
				rec := w.serve(nil, http.MethodGet, "/moved")
				Expect(rec.Code).To(Equal(http.StatusMovedPermanently))
				Expect(rec.Header()).NotTo(haveHeaderAnyCase("Location"))
			})
			It("sends a stored CR and LF as spaces", func(ctx SpecContext) {
				w.put(ctx, "moved", "body", map[string][]byte{meta.AttrWebsiteRedirect: []byte("/a\r\nX-Injected: 1\x00")})
				srv := httptest.NewServer(s3.NewHandler(w.env, s3.AnonymousOnly{}, testConfig(websiteGateway(w.apis))))
				DeferCleanup(srv.Close)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/moved", nil)
				Expect(err).NotTo(HaveOccurred())
				req.Host = siteHost
				client := srv.Client()
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
				resp, err := client.Do(req)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(resp.Body.Close)
				Expect(resp.Header.Get("Location")).To(Equal("/a  X-Injected: 1"))
				Expect(resp.Header).NotTo(HaveKey("X-Injected"))
			})
		})

		Describe("rgw-go declines to build", func() {
			DescribeTable("is 500 InternalError for a stored host holding CR and LF, on both paths",
				func(ctx SpecContext, conf *meta.WebsiteConf, target string) {
					captureLog()
					w.setWebsite(ctx, withErrorDoc(conf))
					expectHTMLError(w.serve(nil, http.MethodGet, target), http.StatusInternalServerError, "500 Internal Server Error", "InternalError", "", "site")
				},
				Entry("retarget's rule", site(rule("old/", 0, "evil.example.org\r\nX-Injected: 1", "", 302)), "/old/x"),
				Entry("the error handler's rule", site(rule("", 404, "evil.example.org\r\nX-Injected: 1", "", 302)), "/missing.html"),
				Entry("a redirect-all", &meta.WebsiteConf{IndexDocSuffix: "index.html", RedirectAll: meta.RedirectInfo{Hostname: "evil\r\n"}}, "/a.txt"),
			)
			It("is 500 InternalError for a stored protocol other than http or https", func(ctx SpecContext) {
				captureLog()
				r := rule("old/", 0, "example.org", "", 302)
				r.RedirectInfo.Redirect.Protocol = "javascript"
				w.setWebsite(ctx, site(r))
				expectHTMLError(w.serve(nil, http.MethodGet, "/old/x"), http.StatusInternalServerError, "500 Internal Server Error", "InternalError", "", "site")
			})
			DescribeTable("keeps a request with no Host at 400 InvalidRequest, never 500, on both paths",
				func(ctx SpecContext, conf *meta.WebsiteConf, target string) {
					w.apis = "s3website, s3"
					w.setWebsite(ctx, withErrorDoc(conf))
					req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
					req.Proto, req.ProtoMajor, req.ProtoMinor, req.Host = "HTTP/1.0", 1, 0, ""
					expectHTMLError(w.serveRequest(s3.AnonymousOnly{}, req), http.StatusBadRequest, "400 Bad Request", "InvalidRequest",
						"The routing rule names no host and the request sent no Host header.", "site")
				},
				Entry("retarget's rule", site(rule("old/", 0, "", "new/", 302)), "/site/old/x"),
				Entry("the error handler's rule", site(rule("", 404, "", "", 302)), "/site/missing.html"),
			)
		})
	})

	Describe("the error handler", func() {
		It("redirects on a rule matching the request's original key and the error's status", func(ctx SpecContext) {
			w.setWebsite(ctx, withErrorDoc(site(rule("gone", 404, "", "moved", 302))))
			expectRedirect(w.serve(nil, http.MethodGet, "/gone/page"), http.StatusFound, "http://site.web.example.com/moved/page")
		})
		It("matches the original key, not the effective key retarget read", func(ctx SpecContext) {
			w.setWebsite(ctx, site(rule("gone/", 404, "", "moved/", 302)))
			expectHTMLError(w.serve(nil, http.MethodGet, "/gone"), http.StatusNotFound, "404 Not Found", "NoSuchKey", "", "site")
		})
		It("redirects a refusal on a rule for 403", func(ctx SpecContext) {
			w.put(ctx, "private.html", "secret", map[string][]byte{meta.AttrACL: w.canned("private")})
			w.setWebsite(ctx, withErrorDoc(site(rule("", 403, "", "login", 302))))
			expectRedirect(w.serve(nil, http.MethodGet, "/private.html"), http.StatusFound, "http://site.web.example.com/loginprivate.html")
		})
		It("matches an error radosgw's table has no row for at status -1", func(ctx SpecContext) {
			captureLog()
			objects := &opfakes.FakeObjectStore{}
			objects.PrefetchObjectReturns(nil, op.ErrUnknown)
			w.env.Objects = objects
			w.setWebsite(ctx, withErrorDoc(site(rule("", 500, "", "five-hundred", 302), rule("", 65535, "", "unknown", 302))))
			expectRedirect(w.serve(nil, http.MethodGet, "/"), http.StatusFound, "http://site.web.example.com/unknown")
		})

		DescribeTable("serves the error document under the error's status",
			func(ctx SpecContext, method, target string, status int, hdr ...string) {
				w.put(ctx, "private.html", "secret", map[string][]byte{meta.AttrACL: w.canned("private")})
				w.setWebsite(ctx, withErrorDoc(site()))
				expectErrorDoc(w.serve(nil, method, target, hdr...), status, method == http.MethodHead)
			},
			Entry("for a missing key", http.MethodGet, "/missing.html", http.StatusNotFound),
			Entry("for a missing key's HEAD, with no body", http.MethodHead, "/missing.html", http.StatusNotFound),
			Entry("for a refused read", http.MethodGet, "/private.html", http.StatusForbidden),
			Entry("whole, for a Range", http.MethodGet, "/missing.html", http.StatusNotFound, "Range", "bytes=0-1"),
			Entry("unconditionally, for an If-None-Match on its ETag", http.MethodGet, "/missing.html", http.StatusNotFound,
				"If-None-Match", `"`+md5Of([]byte("<p>oops</p>"))+`"`),
			Entry("unconditionally, for an If-Match on another ETag", http.MethodGet, "/missing.html", http.StatusNotFound, "If-Match", `"nope"`),
			Entry("unconditionally, for an If-Modified-Since after it was written", http.MethodGet, "/missing.html", http.StatusNotFound,
				"If-Modified-Since", "Sun, 27 Sep 2037 00:00:00 GMT"),
			Entry("whole, for a partNumber", http.MethodGet, "/missing.html?partNumber=2", http.StatusNotFound),
		)
		It("logs the error document's request under the error's status and counts its bytes", func(ctx SpecContext) {
			w.setWebsite(ctx, withErrorDoc(site()))
			expectErrorDoc(w.serve(nil, http.MethodGet, "/missing.html"), http.StatusNotFound, false)
			Expect(w.store.Usage()).To(ConsistOf(And(
				HaveField("Bucket", "-"), HaveField("Category", "get_obj"), HaveField("BytesSent", uint64(len("<p>oops</p>"))),
				HaveField("Ops", uint64(1)), HaveField("SuccessfulOps", uint64(0)))),
				"a 404 takes the bucket name - and counts no successful op")
		})
		It("logs a failed read of the error document with its cause, at Warn for an internal error", func(ctx SpecContext) {
			logs := captureLog()
			w.env.Objects = failingRead{ObjectStore: w.store, key: "error.html", err: fmt.Errorf("reading the head: %w", op.ErrInternalError)}
			w.setWebsite(ctx, withErrorDoc(site()))
			expectHTMLError(w.serve(nil, http.MethodGet, "/missing.html"), http.StatusNotFound, "404 Not Found", "NoSuchKey", "", "site")
			Expect(logRecords(logs)).To(ContainElement(And(
				HaveKeyWithValue("level", "WARN"), HaveKeyWithValue("msg", "website error document not served"),
				HaveKeyWithValue("key", "error.html"), HaveKeyWithValue("error", "reading the head: InternalError"))))
		})

		Describe("holding x-amz-website-redirect-location", func() {
			BeforeEach(func(ctx SpecContext) {
				w.put(ctx, "error.html", "<p>oops</p>", map[string][]byte{meta.AttrWebsiteRedirect: []byte("/elsewhere\x00")})
				w.setWebsite(ctx, withErrorDoc(site()))
			})
			DescribeTable("answers the error document's redirect, once",
				func(ctx SpecContext, method string) {
					srv := httptest.NewServer(s3.NewHandler(w.env, s3.AnonymousOnly{}, testConfig(websiteGateway(w.apis))))
					DeferCleanup(srv.Close)
					conn, err := net.Dial("tcp", srv.Listener.Addr().String())
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(conn.Close)
					_, err = io.WriteString(conn, method+" /missing.html HTTP/1.1\r\nHost: "+siteHost+"\r\n\r\n"+
						"GET /a.txt HTTP/1.1\r\nHost: "+siteHost+"\r\n\r\n")
					Expect(err).NotTo(HaveOccurred())
					br := bufio.NewReader(conn)
					first, err := http.ReadResponse(br, &http.Request{Method: method})
					Expect(err).NotTo(HaveOccurred())
					Expect(first.StatusCode).To(Equal(http.StatusMovedPermanently))
					Expect(first.Header.Get("Location")).To(Equal("/elsewhere"))
					Expect(first.Header.Get("Content-Length")).To(Equal("0"))
					Expect(first.Header.Get("Accept-Ranges")).To(Equal("bytes"))
					Expect(io.ReadAll(first.Body)).To(BeEmpty())
					next, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
					Expect(err).NotTo(HaveOccurred())
					Expect(next.StatusCode).To(Equal(http.StatusOK), "the next response is the next request's, not a second 301")
					Expect(io.ReadAll(next.Body)).To(Equal([]byte("plain text")))
				},
				Entry("GET", http.MethodGet),
				Entry("HEAD", http.MethodHead),
			)
		})
		DescribeTable("serves the error document for a 4XX error only, as Amazon S3 does",
			func(ctx SpecContext, err error, code string) {
				captureLog()
				w.env.Objects = failingRead{ObjectStore: w.store, key: "index.html", err: err}
				w.setWebsite(ctx, withErrorDoc(site()))
				expectHTMLError(w.serve(nil, http.MethodGet, "/"), http.StatusInternalServerError, "500 Internal Server Error", code, "", "site")
			},
			Entry("not for a 500", op.ErrInternalError, "InternalError"),
			Entry("not for an error radosgw's table has no row for, matched at -1", op.ErrUnknown, "UnknownError"),
		)
		DescribeTable("falls back to the original error's page when the error document cannot be served",
			func(ctx SpecContext, prepare func(context.Context), target string, status int, title, code string) {
				prepare(ctx)
				expectHTMLError(w.serve(nil, http.MethodGet, target), status, title, code, "", "site")
			},
			Entry("missing", func(ctx context.Context) {
				w.setWebsite(ctx, &meta.WebsiteConf{IndexDocSuffix: "index.html", ErrorDoc: "nope.html"})
			}, "/missing.html", http.StatusNotFound, "404 Not Found", "NoSuchKey"),
			Entry("private to the requester", func(ctx context.Context) {
				w.put(ctx, "error.html", "<p>oops</p>", map[string][]byte{meta.AttrACL: w.canned("private")})
				w.setWebsite(ctx, withErrorDoc(site()))
			}, "/missing.html", http.StatusNotFound, "404 Not Found", "NoSuchKey"),
			Entry("refused by its read after the permission check", func(ctx context.Context) {
				w.put(ctx, "error.html", "<p>oops</p>", map[string][]byte{meta.AttrCryptMode: []byte("SSE-KMS")})
				w.setWebsite(ctx, withErrorDoc(site()))
			}, "/missing.html", http.StatusNotFound, "404 Not Found", "NoSuchKey"),
		)

		Describe("never sees a failure of the GetObject's execute", func() {
			BeforeEach(func(ctx SpecContext) {
				w.setWebsite(ctx, withErrorDoc(site(
					rule("", 304, "", "r304", 302), rule("", 400, "", "r400", 302),
					rule("", 412, "", "r412", 302), rule("", 416, "", "r416", 302))))
			})
			It("sends a 304 its four headers and nothing else", func() {
				rec := w.serve(nil, http.MethodGet, "/a.txt", "If-None-Match", `"`+md5Of([]byte("plain text"))+`"`)
				Expect(rec.Code).To(Equal(http.StatusNotModified))
				Expect(rec.Body.String()).To(BeEmpty())
				Expect(exactHeader(rec.Header(), "ETag")).To(Equal(`"` + md5Of([]byte("plain text")) + `"`))
				Expect(rec.Header().Get("Last-Modified")).To(Equal(readLastModified))
				Expect(rec.Header()).NotTo(haveHeaderAnyCase("Location"))
			})
			DescribeTable("answers each other failure with its page",
				func(status int, title, code, target string, hdr ...string) {
					expectHTMLError(w.serve(nil, http.MethodGet, target, hdr...), status, title, code, "", "site")
				},
				Entry("a failed If-Match", http.StatusPreconditionFailed, "412 Precondition Failed", "PreconditionFailed", "/a.txt", "If-Match", `"nope"`),
				Entry("a range past the object", http.StatusRequestedRangeNotSatisfiable, "416 Requested Range Not Satisfiable", "InvalidRange", "/a.txt", "Range", "bytes=100-200"),
				Entry("an encryption header on a read", http.StatusBadRequest, "400 Bad Request", "InvalidRequest", "/a.txt", "X-Amz-Server-Side-Encryption", "AES256"),
			)
			It("answers a partNumber get_params refuses with its page", func() {
				expectHTMLError(w.serve(nil, http.MethodGet, "/a.txt?partNumber=x"), http.StatusBadRequest, "400 Bad Request", "InvalidPart",
					"Invalid partNumber: Expected option value to be integer, got &apos;x&apos;", "site")
			})
		})
	})
})

var _ = Describe("the website GetObject on Tentacle", func() {
	It("carries x-amz-delete-marker for a missing key onto the redirect and the error document read_permissions answers it with", func(ctx SpecContext) {
		w := newWebsiteWorld(ctx, denc.Tentacle)
		w.put(ctx, "error.html", "<p>oops</p>", nil)
		w.setWebsite(ctx, &meta.WebsiteConf{IndexDocSuffix: "index.html", ErrorDoc: "error.html"})
		rec := w.serve(nil, http.MethodGet, "/missing.html")
		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Body.String()).To(Equal("<p>oops</p>"))
		Expect(rec.Header()).To(HaveKeyWithValue("x-amz-delete-marker", []string{"false"}), "rgw_rest.cc:1936-1945 at v20.2.4")
		w.setWebsite(ctx, &meta.WebsiteConf{IndexDocSuffix: "index.html", RoutingRules: meta.RoutingRules{rule("", 404, "", "x/", 302)}})
		rec = w.serve(nil, http.MethodGet, "/missing.html")
		expectRedirect(rec, http.StatusFound, "http://site.web.example.com/x/missing.html")
		Expect(rec.Header()).To(HaveKeyWithValue("x-amz-delete-marker", []string{"false"}))
	})
})
