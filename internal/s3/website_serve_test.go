package s3_test

import (
	"context"
	"io"
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
}

func newWebsiteWorld(ctx context.Context, rel denc.Release) *websiteWorld {
	GinkgoHelper()
	store := memstore.New(memstore.Config{Release: rel, Now: func() time.Time { return readMtime }})
	w := &websiteWorld{store: store}
	w.alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
	w.env = testEnv(store)
	w.env.Authz = authz.New(authz.DefaultConfig(rel))
	var err error
	w.site, err = store.CreateBucket(ctx, op.CreateBucketParams{
		Name: "site", Owner: meta.UserOwner(w.alice.Info.UserID), Placement: meta.PlacementRule{Name: "default-placement"},
		Attrs: map[string][]byte{meta.AttrACL: w.publicRead()},
	})
	Expect(err).NotTo(HaveOccurred())
	return w
}

// publicRead is alice's public-read ACL.
func (w *websiteWorld) publicRead() []byte {
	GinkgoHelper()
	owner := acl.Owner{ID: "alice", DisplayName: "Alice"}
	p, err := acl.Canned(owner, owner, "public-read")
	Expect(err).NotTo(HaveOccurred())
	return encodeACL(p)
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
	rec := httptest.NewRecorder()
	s3.NewHandler(w.env, auth, testConfig(websiteGateway("s3, s3website"))).ServeHTTP(rec, req)
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
	items := []string{"Code: " + code, "Message: " + message}
	if bucket != "" {
		items = append(items, "BucketName: "+bucket)
	}
	items = append(items, "RequestId: "+id, "HostId: 4155-z-zg")
	want := htmlPage(title, items...)
	Expect(rec.Body.String()).To(Equal(want))
	Expect(rec.Header().Get("Content-Type")).To(Equal("text/html"), "to_mime_type(HTML), rgw_common.h:198-199 at v19.2.7")
	Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
	Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "dump_content_length, rgw_rest.cc:388-397")
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

	DescribeTable("routes GET and HEAD to the website endpoint alone, at every scope and whatever the query names",
		func(method, host, target, bucket string) {
			rec := w.serveHost(host, w.alice, method, target)
			Expect(rec.Code).To(Equal(http.StatusNotImplemented), rec.Body.String())
			if method == http.MethodGet {
				expectHTMLError(rec, http.StatusNotImplemented, "501 Not Implemented", "NotImplemented", "", bucket)
			}
		},
		Entry("a GET of an object", http.MethodGet, siteHost, "/k", "site"),
		Entry("a HEAD of an object", http.MethodHead, siteHost, "/k", "site"),
		Entry("a bucket's GET, not ListObjects", http.MethodGet, siteHost, "/", "site"),
		Entry("a bucket's ?acl, not GetBucketAcl", http.MethodGet, siteHost, "/?acl", "site"),
		Entry("a bucket's ?website, not GetBucketWebsite", http.MethodGet, siteHost, "/?website", "site"),
		Entry("a bucket's ?versionId, which S3 refuses at bucket scope", http.MethodGet, siteHost, "/?versionId=v", "site"),
		Entry("the service, not ListBuckets", http.MethodGet, "web.example.com", "/", ""),
	)
})
