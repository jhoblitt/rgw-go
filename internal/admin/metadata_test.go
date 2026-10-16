package admin_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// failingBody is a request body whose last read fails, as a payload whose
// signature does not verify fails when the reader reaches its end.
type failingBody struct {
	r   io.Reader
	err error
}

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, b.err
	}
	return n, err
}

// wireHeader is the header as the handler spelled its name, which
// http.Header's canonicalizing lookups would not find.
func wireHeader(h http.Header, name string) []string { return h[name] }

// listTimeout bounds a listing spec, so a listing that never ends fails it
// rather than the suite.
const listTimeout = 10 * time.Second

var _ = Describe("admin metadata routes", func() {
	var (
		fx   *fixture
		inst string
	)
	BeforeEach(func() {
		fx = newFixture(admin.Config{})
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: "leseb"}
		info.DisplayName = "This is leseb"
		info.Email = "leseb@example.com"
		info.AccessKeys = map[string]meta.AccessKey{"AKIALESEB": {ID: "AKIALESEB", Secret: "lesebsecret", Active: true}}
		fx.store.AddUser(info)
		rec, err := fx.store.CreateBucket(context.Background(), op.CreateBucketParams{
			Name: "test", Owner: meta.UserOwner(meta.UserID{ID: "admin"}), Placement: meta.PlacementRule{Name: "default-placement"},
		})
		Expect(err).NotTo(HaveOccurred())
		inst = "test:" + rec.Info.Bucket.ID
	})
	addUser := func(id, caps string) {
		GinkgoHelper()
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: id}
		if caps != "" {
			Expect(info.Caps.AddString(caps)).To(Succeed())
		}
		fx.store.AddUser(info)
	}
	// resIn issues a GET as admin under c, a listing spec's context.
	resIn := func(c context.Context, path string) *http.Response {
		GinkgoHelper()
		req, err := http.NewRequestWithContext(c, http.MethodGet, fx.server.URL+path, nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("X-Test-User", "admin")
		return do(req)
	}
	// getIn is resIn's body, asserting 200.
	getIn := func(c context.Context, path string) string {
		GinkgoHelper()
		res := resIn(c, path)
		b := body(res)
		Expect(res.StatusCode).To(Equal(http.StatusOK), b)
		return b
	}
	get := func(path string) string {
		GinkgoHelper()
		return getIn(context.Background(), path)
	}
	// serve runs a request through the handler directly, so the response
	// headers keep the spelling the handler wrote.
	serve := func(method, path string, reqBody io.Reader, length int64, user string) *httptest.ResponseRecorder {
		GinkgoHelper()
		req := httptest.NewRequestWithContext(context.Background(), method, path, reqBody)
		req.ContentLength = length
		if user != "" {
			req.Header.Set("X-Test-User", user)
		}
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
		return rec
	}
	// putDoc is the GET document of key in section with its version's ver
	// raised by bump.
	putDoc := func(section, key string, bump uint64) []byte {
		GinkgoHelper()
		var doc map[string]any
		Expect(json.Unmarshal([]byte(get("/admin/metadata/"+section+"?key="+url.QueryEscape(key))), &doc)).To(Succeed())
		ver, ok := doc["ver"].(map[string]any)
		Expect(ok).To(BeTrue(), "ver: %v", doc["ver"])
		n, ok := ver["ver"].(float64)
		Expect(ok).To(BeTrue(), "ver.ver: %v", ver["ver"])
		ver["ver"] = n + float64(bump)
		b, err := json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		return b
	}
	userVersion := func(id string) meta.ObjVersion {
		GinkgoHelper()
		rec, err := fx.store.GetUser(context.Background(), meta.UserID{ID: id})
		Expect(err).NotTo(HaveOccurred())
		return rec.Version
	}

	It("lists a section as the bare array go-ceph's GetUsers reads", func(sctx SpecContext) {
		res := resIn(sctx, "/admin/metadata/user")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(body(res)).To(Equal(`["admin","leseb","nocaps"]`))
		Expect(getIn(sctx, "/admin/metadata/bucket")).To(Equal(`["test"]`))
		Expect(getIn(sctx, "/admin/metadata/bucket.instance")).To(Equal(`["` + inst + `"]`))
		Expect(getIn(sctx, "/admin/metadata")).To(Equal(`["bucket","bucket.instance","user"]`), "the top level lists the sections")
	}, SpecTimeout(listTimeout))
	It("pages with max-entries, the marker base64-wrapped, and continues from it", func(sctx SpecContext) {
		b := getIn(sctx, "/admin/metadata/user?max-entries=1")
		marker := base64.StdEncoding.EncodeToString([]byte("admin"))
		Expect(b).To(Equal(`{"keys":["admin"],"truncated":true,"count":1,"marker":"` + marker + `"}`))
		b = getIn(sctx, "/admin/metadata/user?max-entries=1&marker="+url.QueryEscape(marker))
		Expect(b).To(Equal(`{"keys":["leseb"],"truncated":true,"count":1,"marker":"` + base64.StdEncoding.EncodeToString([]byte("leseb")) + `"}`))
		Expect(getIn(sctx, "/admin/metadata/user?max-entries=5&marker="+url.QueryEscape(marker))).To(Equal(`{"keys":["leseb","nocaps"],"truncated":false,"count":2}`))
		Expect(getIn(sctx, "/admin/metadata/user?max-entries=0")).To(Equal(`{"keys":[],"truncated":true,"count":0,"marker":""}`), "one call for none, which reports what follows")
	}, SpecTimeout(listTimeout))
	It("lists from the beginning for a marker that is not base64", func(sctx SpecContext) {
		Expect(getIn(sctx, "/admin/metadata/user?marker=%21%21%21")).To(Equal(`["admin","leseb","nocaps"]`))
	}, SpecTimeout(listTimeout))
	DescribeTable("reads max-entries as strict_strtol, cast to unsigned",
		func(sctx SpecContext, v string, status int) {
			res := resIn(sctx, "/admin/metadata/user?max-entries="+v)
			b := body(res)
			Expect(res.StatusCode).To(Equal(status), b)
			if status == 400 {
				Expect(b).To(HavePrefix(`{"Code":"InvalidArgument",`))
			}
		},
		Entry("a negative, which wraps to a large page", "-1", 200, SpecTimeout(listTimeout)),
		Entry("leading white space", "%202", 200, SpecTimeout(listTimeout)),
		Entry("trailing text", "2x", 400, SpecTimeout(listTimeout)),
		Entry("empty", "", 400, SpecTimeout(listTimeout)),
		Entry("past int", "2147483648", 400, SpecTimeout(listTimeout)),
	)
	It("lists every key for a wrapped negative max-entries", func(sctx SpecContext) {
		Expect(getIn(sctx, "/admin/metadata/user?max-entries=-1")).To(Equal(`{"keys":["admin","leseb","nocaps"],"truncated":false,"count":3}`))
	}, SpecTimeout(listTimeout))
	It("gets a user as RGWMetadataManager::get renders it", func() {
		b := get("/admin/metadata/user?key=admin")
		v := userVersion("admin")
		Expect(b).To(HavePrefix(`{"key":"user:admin","ver":{"tag":"` + v.Tag + `","ver":1},"mtime":"`))
		Expect(b).To(MatchRegexp(`"mtime":"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z","data":\{"user_id":"admin","display_name":"",`))
		Expect(b).To(HaveSuffix(`,"attrs":[]}}`))
		var doc map[string]any
		Expect(json.Unmarshal([]byte(b), &doc)).To(Succeed())
		Expect(doc).To(HaveKeyWithValue("key", "user:admin"))
		Expect(get("/admin/metadata/user?myself")).To(Equal(b), "myself names the requester")
		Expect(get("/admin/metadata?key=user:admin")).To(Equal(b), "a section-less key frames the whole key")
	})
	It("gets a bucket entry point and a bucket instance", func() {
		b := get("/admin/metadata/bucket?key=test")
		Expect(b).To(HavePrefix(`{"key":"bucket:test","ver":{"tag":"`))
		Expect(b).To(ContainSubstring(`"data":{"bucket":{"name":"test",`))
		b = get("/admin/metadata/bucket.instance?key=" + url.QueryEscape(inst))
		Expect(b).To(HavePrefix(`{"key":"bucket.instance:` + inst + `","ver":`))
		Expect(b).To(ContainSubstring(`"data":{"bucket_info":{"bucket":{"name":"test",`))
		Expect(b).To(MatchRegexp(`,"attrs":\[[^\]]*\]\}\}$`))
	})
	It("answers NotImplemented for a bucket instance whose sync policy with groups is carried opaque", func() {
		ctx := context.Background()
		doc := string(putDoc("bucket.instance", inst, 0))
		rec, err := fx.store.GetBucket(ctx, "", "test")
		Expect(err).NotTo(HaveOccurred())
		rec.Info.SyncPolicy = meta.RawStruct{1, 1, 4, 0, 0, 0, 1, 0, 0, 0}
		Expect(fx.store.PutBucketInfo(ctx, rec)).To(Succeed())
		res := fx.get("/admin/metadata/bucket.instance?key="+url.QueryEscape(inst), "admin")
		Expect(res.StatusCode).To(Equal(501))
		Expect(body(res)).To(HavePrefix(`{"Code":"NotImplemented",`))
		res = fx.requestBody(http.MethodPut, "/admin/metadata/bucket.instance?key="+url.QueryEscape(inst), "admin", strings.NewReader(doc))
		Expect(res.StatusCode).To(Equal(501), "the put reads the stored record rendered, though its document names no sync policy")
		Expect(body(res)).To(HavePrefix(`{"Code":"NotImplemented",`))
	})
	It("renders a bucket instance's website configuration and takes it back in a put", func() {
		ctx := context.Background()
		rec, err := fx.store.GetBucket(ctx, "", "test")
		Expect(err).NotTo(HaveOccurred())
		rec.Info.Website = &meta.WebsiteConf{IndexDocSuffix: "index.html"}
		Expect(fx.store.PutBucketInfo(ctx, rec)).To(Succeed())
		b := get("/admin/metadata/bucket.instance?key=" + url.QueryEscape(inst))
		Expect(b).To(ContainSubstring(`"has_website":true,"website_conf":{"index_doc_suffix":"index.html","error_doc":"","routing_rules":{"rules":[]}},`))
		doc := strings.Replace(string(putDoc("bucket.instance", inst, 1)), `"index_doc_suffix":"index.html"`, `"index_doc_suffix":"home.html"`, 1)
		res := fx.requestBody(http.MethodPut, "/admin/metadata/bucket.instance?key="+url.QueryEscape(inst), "admin", strings.NewReader(doc))
		Expect(res.StatusCode).To(Equal(204), body(res))
		Expect(res.Header.Get("RGWX_UPDATE_STATUS")).To(Equal("applied"))
		got, err := fx.store.GetBucket(ctx, "", "test")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Info.Website).To(Equal(&meta.WebsiteConf{IndexDocSuffix: "home.html"}))
	})
	DescribeTable("answers 404 NoSuchKey for what no section holds",
		func(path string) {
			res := fx.get(path, "admin")
			Expect(res.StatusCode).To(Equal(404))
			Expect(errCode(res)).To(Equal("NoSuchKey"))
		},
		Entry("an unknown section's listing", "/admin/metadata/kittens"),
		Entry("an unknown section's entry", "/admin/metadata/kittens?key=x"),
		Entry("a missing user", "/admin/metadata/user?key=nosuch"),
		Entry("a section radosgw serves and rgw-go does not", "/admin/metadata/otp"),
	)
	It("answers 500 UnknownError for a get that names no section, as the top-level handler's ENOTSUP", func() {
		res := fx.get("/admin/metadata?key=", "admin")
		Expect(res.StatusCode).To(Equal(500))
		Expect(errCode(res)).To(Equal("UnknownError"))
	})
	DescribeTable("answers 404 NoSuchKey for a put to a section it does not serve, whatever the body",
		func(reqBody string) {
			rec := serve(http.MethodPut, "/admin/metadata/kittens?key=x&update-type=update-by-version", strings.NewReader(reqBody), int64(len(reqBody)), "admin")
			Expect(rec.Code).To(Equal(404), rec.Body.String())
			Expect(rec.Body.String()).To(HavePrefix(`{"Code":"NoSuchKey",`))
			Expect(wireHeader(rec.Header(), "RGWX_UPDATE_VERSION")).To(Equal([]string{"ver:0,tag:"}))
			Expect(rec.Header()).NotTo(HaveKey("RGWX_UPDATE_STATUS"))
		},
		Entry("a document update-by-version would skip", `{"data":{}}`),
		Entry("a body that is not JSON", `not json`),
	)
	It("puts the GET document back, reporting the version read", func() {
		old := userVersion("admin")
		rec := serve(http.MethodPut, "/admin/metadata/user?key=admin", strings.NewReader(string(putDoc("user", "admin", 1))), -1, "admin")
		Expect(rec.Code).To(Equal(204), rec.Body.String())
		Expect(wireHeader(rec.Header(), "RGWX_UPDATE_STATUS")).To(Equal([]string{"applied"}))
		Expect(wireHeader(rec.Header(), "RGWX_UPDATE_VERSION")).To(Equal([]string{"ver:1,tag:" + old.Tag}))
		Expect(rec.Body.String()).To(BeEmpty())
		Expect(userVersion("admin")).To(Equal(meta.ObjVersion{Ver: 2, Tag: old.Tag}))

		doc := putDoc("user", "admin", 0)
		res := fx.requestBody(http.MethodPut, "/admin/metadata/user?key=admin&update-type=update-by-version", "admin", strings.NewReader(string(doc)))
		Expect(res.StatusCode).To(Equal(204))
		Expect(res.Header.Get("RGWX_UPDATE_STATUS")).To(Equal("skipped"))
		Expect(res.Header.Get("RGWX_UPDATE_VERSION")).To(Equal("ver:0,tag:"))
	})
	It("lets update-type decide on Tentacle too, whose radosgw applies every put", func() {
		fx = newFixtureAt(denc.Tentacle, admin.Config{})
		doc := string(putDoc("user", "admin", 0))
		res := fx.requestBody(http.MethodPut, "/admin/metadata/user?key=admin&update-type=update-by-version", "admin", strings.NewReader(doc))
		Expect(res.StatusCode).To(Equal(204), body(res))
		Expect(res.Header.Get("RGWX_UPDATE_STATUS")).To(Equal("skipped"))
		res = fx.requestBody(http.MethodPut, "/admin/metadata/user?key=admin&update-type=update-by-version", "admin", strings.NewReader(string(putDoc("user", "admin", 1))))
		Expect(res.StatusCode).To(Equal(204), body(res))
		Expect(res.Header.Get("RGWX_UPDATE_STATUS")).To(Equal("applied"))
	})
	It("answers an applied bucket entry point 200 without an update status, as radosgw's handler answers 0", func() {
		res := fx.requestBody(http.MethodPut, "/admin/metadata/bucket?key=test", "admin", strings.NewReader(string(putDoc("bucket", "test", 0))))
		Expect(res.StatusCode).To(Equal(200), body(res))
		Expect(res.Header).NotTo(HaveKey("Rgwx_update_status"))
		Expect(res.Header.Get("RGWX_UPDATE_VERSION")).To(MatchRegexp(`^ver:[1-9]\d*,tag:.+$`))
	})
	It("reads the body the authenticator verified, and fails on its verdict", func() {
		doc := string(putDoc("user", "leseb", 1))
		res := fx.requestBody(http.MethodPut, "/admin/metadata/user?key=leseb", "admin", strings.NewReader("not json"), "X-Test-Body", doc)
		Expect(res.StatusCode).To(Equal(204), body(res))

		verdict := op.ErrContentSHA256Mismatch
		h := admin.NewHandler(fx.env, s3.AuthenticatorFunc(func(ctx context.Context, req *http.Request, forms op.PayloadForms) (*op.AuthResult, error) {
			res, err := fx.authenticate(ctx, req, forms)
			if err == nil {
				res.Body = &failingBody{r: strings.NewReader(doc), err: verdict}
				res.ContentLength = int64(len(doc))
			}
			return res, err
		}), admin.Config{Prefix: "admin"})
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/admin/metadata/user?key=leseb", strings.NewReader(doc))
		req.Header.Set("X-Test-User", "admin")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(HavePrefix(`{"Code":"XAmzContentSHA256Mismatch",`))
		Expect(wireHeader(rec.Header(), "RGWX_UPDATE_VERSION")).To(Equal([]string{"ver:0,tag:"}))
	})
	DescribeTable("answers a put it cannot read with RGWX_UPDATE_VERSION and no status",
		func(path string, reqBody io.Reader, length, status int, code string) {
			rec := serve(http.MethodPut, path, reqBody, int64(length), "admin")
			Expect(rec.Code).To(Equal(status), rec.Body.String())
			Expect(rec.Body.String()).To(HavePrefix(`{"Code":"` + code + `",`))
			Expect(wireHeader(rec.Header(), "RGWX_UPDATE_VERSION")).To(Equal([]string{"ver:0,tag:"}))
			Expect(rec.Header()).NotTo(HaveKey("RGWX_UPDATE_STATUS"))
		},
		Entry("no body", "/admin/metadata/user?key=admin", http.NoBody, 0, 411, "MissingContentLength"),
		Entry("a bogus update-type", "/admin/metadata/user?key=admin&update-type=bogus", strings.NewReader(`{"data":{}}`), 11, 400, "InvalidArgument"),
		Entry("no body before a bogus update-type", "/admin/metadata/user?key=admin&update-type=bogus", http.NoBody, 0, 411, "MissingContentLength"),
		Entry("no data", "/admin/metadata/user?key=admin", strings.NewReader(`{"key":"user:admin"}`), 20, 400, "InvalidArgument"),
		Entry("a body that ends short", "/admin/metadata/user?key=admin", &failingBody{r: strings.NewReader(`{}`), err: io.ErrUnexpectedEOF}, 10, 400, "RequestTimeout"),
	)
	It("refuses a document over 16 MiB, by its declared length or by what it reads, writing nothing", func() {
		v := userVersion("admin")
		doc := string(putDoc("user", "admin", 1))
		rec := serve(http.MethodPut, "/admin/metadata/user?key=admin", strings.NewReader(doc), 16<<20+1, "admin")
		Expect(rec.Code).To(Equal(400), rec.Body.String())
		Expect(rec.Body.String()).To(HavePrefix(`{"Code":"InvalidArgument",`))
		Expect(wireHeader(rec.Header(), "RGWX_UPDATE_VERSION")).To(Equal([]string{"ver:0,tag:"}))
		padded := doc + strings.Repeat(" ", 16<<20)
		rec = serve(http.MethodPut, "/admin/metadata/user?key=admin", strings.NewReader(padded), -1, "admin")
		Expect(rec.Code).To(Equal(400), rec.Body.String())
		Expect(rec.Body.String()).To(HavePrefix(`{"Code":"InvalidArgument",`))
		Expect(userVersion("admin")).To(Equal(v))
		rec = serve(http.MethodPut, "/admin/metadata/user?key=admin", strings.NewReader(padded[:16<<20]), -1, "admin")
		Expect(rec.Code).To(Equal(204), "16 MiB exactly is read")
	})
	It("refuses through the user section's claims: another user's key or email, reporting the version read", func() {
		addUser("carol", "")
		ctx := context.Background()
		carol, err := fx.store.GetUser(ctx, meta.UserID{ID: "carol"})
		Expect(err).NotTo(HaveOccurred())
		carol.Info.Email = "carol@example.com"
		carol.Info.AccessKeys = map[string]meta.AccessKey{"AKIACAROL": {ID: "AKIACAROL", Secret: "s", Active: true}}
		Expect(fx.store.PutUser(ctx, carol, op.PutUserOptions{IfVersion: &carol.Version})).To(Succeed())

		var doc map[string]any
		Expect(json.Unmarshal(putDoc("user", "leseb", 0), &doc)).To(Succeed())
		data, ok := doc["data"].(map[string]any)
		Expect(ok).To(BeTrue())
		keys, ok := data["keys"].([]any)
		Expect(ok).To(BeTrue())
		data["keys"] = append(keys, map[string]any{"user": "leseb", "access_key": "AKIACAROL", "secret_key": "x"})
		b, err := json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		v := userVersion("leseb")
		rec := serve(http.MethodPut, "/admin/metadata/user?key=leseb", strings.NewReader(string(b)), int64(len(b)), "admin")
		Expect(rec.Code).To(Equal(409), rec.Body.String())
		Expect(rec.Body.String()).To(HavePrefix(`{"Code":"KeyExists",`))
		Expect(wireHeader(rec.Header(), "RGWX_UPDATE_VERSION")).To(Equal([]string{"ver:1,tag:" + v.Tag}))

		data["keys"] = keys
		data["email"] = "Carol@example.com"
		b, err = json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		res := fx.requestBody(http.MethodPut, "/admin/metadata/user?key=leseb", "admin", strings.NewReader(string(b)))
		Expect(res.StatusCode).To(Equal(409))
		Expect(errCode(res)).To(Equal("EmailExists"))
		Expect(userVersion("leseb")).To(Equal(v), "nothing written")
	})
	It("removes an entry, then answers 404 for it", func() {
		res := fx.request(http.MethodDelete, "/admin/metadata/user?key=leseb", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Length")).To(Equal("0"))
		Expect(body(res)).To(BeEmpty())
		res = fx.get("/admin/metadata/user?key=leseb", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchKey"))
	})
	It("lists users through user?list, the marker as the lister returns it", func(sctx SpecContext) {
		b := getIn(sctx, "/admin/user?list&max-entries=1")
		Expect(b).To(Equal(`{"keys":["admin"],"truncated":true,"count":1,"marker":"admin"}`))
		Expect(getIn(sctx, "/admin/user?list&max-entries=1&marker=admin")).To(Equal(`{"keys":["leseb"],"truncated":true,"count":1,"marker":"leseb"}`))
		Expect(getIn(sctx, "/admin/user?list")).To(Equal(`{"keys":["admin","leseb","nocaps"],"truncated":false,"count":3}`))
		Expect(getIn(sctx, "/admin/user?list&max-entries=bogus")).To(Equal(`{"keys":["admin","leseb","nocaps"],"truncated":false,"count":3}`), "get_uint32 unchecked: the default")
	}, SpecTimeout(listTimeout))
	It("renders metadata in XML without a declaration, and the user list with one", func(sctx SpecContext) {
		b := getIn(sctx, `/admin/metadata/user?key=admin&format=xml`)
		Expect(b).To(HavePrefix(`<metadata_info><key>user:admin</key><ver><tag>`))
		Expect(b).To(ContainSubstring(`</ver><mtime>`))
		Expect(b).To(ContainSubstring(`<data><user_id>admin</user_id><display_name>`))
		Expect(b).To(HaveSuffix(`</attrs></data></metadata_info>`))
		Expect(getIn(sctx, `/admin/metadata/user?key=leseb&format=xml`)).To(ContainSubstring(`<keys><key><user>leseb</user><access_key>`))
		res := resIn(sctx, `/admin/metadata/user?format=xml`)
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(body(res)).To(Equal(`<keys><key>admin</key><key>leseb</key><key>nocaps</key></keys>`))
		Expect(getIn(sctx, `/admin/metadata/user?max-entries=1&format=xml`)).To(MatchRegexp(`^<result><keys><key>admin</key></keys><truncated>true</truncated><count>1</count><marker>[A-Za-z0-9+/=]+</marker></result>$`))
		res = resIn(sctx, `/admin/user?list&format=xml`)
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(body(res)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><result><keys><key>admin</key><key>leseb</key><key>nocaps</key></keys><truncated>false</truncated><count>3</count></result>`))
	}, SpecTimeout(listTimeout))
	DescribeTable("refuses a caller without radosgw's cap, before anything is read",
		func(method, path, caps string) {
			addUser("caller", caps)
			rec := serve(method, path, strings.NewReader(`{"data":{}}`), 11, "caller")
			Expect(rec.Code).To(Equal(403), rec.Body.String())
			Expect(rec.Body.String()).To(HavePrefix(`{"Code":"AccessDenied",`))
			Expect(rec.Header()).NotTo(HaveKey("RGWX_UPDATE_VERSION"), "abort_early sends no op headers")
			Expect(rec.Header()).NotTo(HaveKey("RGWX_UPDATE_STATUS"))
		},
		Entry("get without caps", http.MethodGet, "/admin/metadata/user?key=admin", ""),
		Entry("get with users=*", http.MethodGet, "/admin/metadata/user?key=admin", "users=*"),
		Entry("get with metadata=write", http.MethodGet, "/admin/metadata/user?key=admin", "metadata=write"),
		Entry("myself without caps", http.MethodGet, "/admin/metadata/user?myself", ""),
		Entry("myself with users=read", http.MethodGet, "/admin/metadata/user?myself", "users=read"),
		Entry("list without caps", http.MethodGet, "/admin/metadata/user", ""),
		Entry("list with users=*", http.MethodGet, "/admin/metadata/user", "users=*"),
		Entry("put without caps", http.MethodPut, "/admin/metadata/user?key=admin", ""),
		Entry("put with metadata=read", http.MethodPut, "/admin/metadata/user?key=admin", "metadata=read"),
		Entry("put with users=*", http.MethodPut, "/admin/metadata/user?key=admin", "users=*"),
		Entry("remove without caps", http.MethodDelete, "/admin/metadata/user?key=leseb", ""),
		Entry("remove with metadata=read", http.MethodDelete, "/admin/metadata/user?key=leseb", "metadata=read"),
		Entry("remove with buckets=*", http.MethodDelete, "/admin/metadata/user?key=leseb", "buckets=*"),
		Entry("user list without caps", http.MethodGet, "/admin/user?list", ""),
		Entry("user list with metadata=*", http.MethodGet, "/admin/user?list", "metadata=*"),
		Entry("user list with users=write", http.MethodGet, "/admin/user?list", "users=write"),
	)
	DescribeTable("admits a caller with radosgw's cap",
		func(method, path, caps string, status int) {
			addUser("caller", caps)
			rec := serve(method, path, strings.NewReader(`{"data":{}}`), 11, "caller")
			Expect(rec.Code).To(Equal(status), rec.Body.String())
		},
		Entry("get with metadata=read", http.MethodGet, "/admin/metadata/user?key=admin", "metadata=read", 200),
		Entry("myself with metadata=read", http.MethodGet, "/admin/metadata/user?myself", "metadata=read", 200),
		Entry("list with metadata=read", http.MethodGet, "/admin/metadata/user", "metadata=read", 200),
		Entry("put with metadata=write, refused for its document", http.MethodPut, "/admin/metadata/user?key=admin", "metadata=write", 400),
		Entry("remove with metadata=write", http.MethodDelete, "/admin/metadata/user?key=leseb", "metadata=write", 200),
		Entry("user list with users=read", http.MethodGet, "/admin/user?list", "users=read", 200),
	)
	It("never removes or writes anything for a refused caller", func() {
		addUser("caller", "metadata=read")
		v := userVersion("leseb")
		Expect(serve(http.MethodDelete, "/admin/metadata/user?key=leseb", nil, 0, "caller").Code).To(Equal(403))
		doc := putDoc("user", "leseb", 1)
		Expect(serve(http.MethodPut, "/admin/metadata/user?key=leseb", strings.NewReader(string(doc)), int64(len(doc)), "caller").Code).To(Equal(403))
		Expect(userVersion("leseb")).To(Equal(v))
	})
})
