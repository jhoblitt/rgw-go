package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

const (
	fsid   = "75d1938b-2949-4933-8386-fb2d1449ff03"
	hostID = "4107-z1-zg1"
	reqID  = `tx[0-9a-f]{21}-[0-9a-f]{10}-4107-z1`
)

// fixture is one admin handler over a memstore with two users, admin with
// every admin cap and nocaps with none, behind an authenticator that takes
// the user from X-Test-User.
type fixture struct {
	store   *memstore.Store
	env     *op.Env
	metrics *opfakes.FakeMetrics
	handler *admin.Handler
	server  *httptest.Server

	mu           sync.Mutex
	lastPayloads op.PayloadForms
	authCalls    int
}

func newFixture(cfg admin.Config) *fixture {
	GinkgoHelper()
	return newFixtureAt(denc.Squid, cfg)
}

// newFixtureAt is newFixture with the zone at release rel.
func newFixtureAt(rel denc.Release, cfg admin.Config) *fixture {
	GinkgoHelper()
	fx := &fixture{metrics: new(opfakes.FakeMetrics)}
	pools := meta.NewZonePlacementInfo()
	pools.StorageClasses = meta.ZoneStorageClasses{meta.StorageClassStandard: {}, "FOO": {}}
	fx.store = memstore.New(memstore.Config{Release: rel, Params: meta.ZoneParams{
		ID: "zid", Name: "z1", DomainRoot: meta.ParsePool("z1.rgw.meta:root"),
		PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": pools},
	}})
	for _, u := range []struct{ id, caps string }{
		{"admin", "info=read;zone=read;users=*;buckets=*;usage=read;metadata=*;accounts=*"},
		{"nocaps", ""},
	} {
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: u.id}
		if u.caps != "" {
			Expect(info.Caps.AddString(u.caps)).To(Succeed())
		}
		fx.store.AddUser(info)
	}
	fx.env = &op.Env{
		Zone: fx.store, Users: fx.store, Accounts: fx.store, Buckets: fx.store, Objects: fx.store,
		Multipart: fx.store, Stats: fx.store, BucketAdmin: fx.store, Metadata: fx.store,
		Usage: fx.store, Metrics: fx.metrics, HostID: hostID, ClusterID: fsid,
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "admin"
	}
	cfg.TransIDSuffix = op.TransIDSuffix(4107, "z1")
	cfg.ServerHeader = "Ceph Object Gateway (squid)"
	fx.handler = admin.NewHandler(fx.env, s3.AuthenticatorFunc(fx.authenticate), cfg)
	fx.server = httptest.NewServer(fx.handler)
	DeferCleanup(fx.server.Close)
	return fx
}

// authenticate resolves X-Test-User as auth's identity does; X-Test-Body
// replaces the body as an aws-chunked decoder would. X-Test-Fail fails with
// an InternalError that carries the Authorization header, as a careless
// authenticator's error might.
func (fx *fixture) authenticate(ctx context.Context, req *http.Request, payloads op.PayloadForms) (*op.AuthResult, error) {
	fx.mu.Lock()
	fx.lastPayloads = payloads
	fx.authCalls++
	fx.mu.Unlock()
	if req.Header.Get("X-Test-Fail") != "" {
		return nil, fmt.Errorf("%w: credentials %s", op.ErrInternalError, req.Header.Get("Authorization"))
	}
	name := req.Header.Get("X-Test-User")
	if name == "" {
		return &op.AuthResult{Identity: op.Anonymous()}, nil
	}
	rec, err := fx.store.GetUser(ctx, meta.UserID{ID: name})
	if errors.Is(err, op.ErrNoSuchUser) {
		return nil, op.ErrInvalidAccessKeyID
	}
	if err != nil {
		return nil, err
	}
	info := rec.Info
	res := &op.AuthResult{Identity: op.Identity{
		User: &info, Owner: meta.UserOwner(info.UserID), Tenant: info.UserID.Tenant, OpMask: info.OpMask,
		Caps: info.Caps, Admin: info.Admin != 0 || info.System != 0, System: info.System != 0,
	}}
	if b := req.Header.Get("X-Test-Body"); b != "" {
		res.Body, res.ContentLength = strings.NewReader(b), int64(len(b))
	}
	return res, nil
}

func (fx *fixture) payloads() op.PayloadForms {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.lastPayloads
}

// authenticated counts the requests that reached the authenticator.
func (fx *fixture) authenticated() int {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.authCalls
}

func (fx *fixture) request(method, path, user string, hdr ...string) *http.Response {
	GinkgoHelper()
	return fx.requestBody(method, path, user, nil, hdr...)
}

func (fx *fixture) requestBody(method, path, user string, body io.Reader, hdr ...string) *http.Response {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(context.Background(), method, fx.server.URL+path, body)
	Expect(err).NotTo(HaveOccurred())
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return do(req)
}

// do sends req and returns its response with the body read and the
// connection released.
func do(req *http.Request) *http.Response {
	GinkgoHelper()
	res, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	b, err := io.ReadAll(res.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(res.Body.Close()).To(Succeed())
	res.Body = io.NopCloser(bytes.NewReader(b))
	return res
}

func (fx *fixture) get(path, user string) *http.Response {
	GinkgoHelper()
	return fx.request(http.MethodGet, path, user)
}

func body(res *http.Response) string {
	GinkgoHelper()
	b, err := io.ReadAll(res.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(res.Body.Close()).To(Succeed())
	return string(b)
}

func (fx *fixture) suspend(name string) {
	GinkgoHelper()
	ctx := context.Background()
	rec, err := fx.store.GetUser(ctx, meta.UserID{ID: name})
	Expect(err).NotTo(HaveOccurred())
	rec.Info.Suspended = 1
	Expect(fx.store.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
}

var _ = Describe("admin handler", func() {
	var fx *fixture
	BeforeEach(func() { fx = newFixture(admin.Config{}) })

	It("serves /admin/info in JSON with radosgw's bytes", func() {
		res := fx.get("/admin/info?format=json", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(res.Header.Get("Server")).To(Equal("Ceph Object Gateway (squid)"))
		Expect(res.Header.Get("x-amz-request-id")).To(MatchRegexp(`^` + reqID + `$`))
		b := body(res)
		Expect(b).To(Equal(`{"info":{"storage_backends":[{"name":"rados","cluster_id":"` + fsid + `"}]}}`))
		Expect(res.Header.Get("Content-Length")).To(Equal(strconv.Itoa(len(b))))
		Expect(res.Header).NotTo(HaveKey("Accept-Ranges"), "the flusher's end_header names no length, rgw_rest.cc:1035-1042")
	})
	It("serves /admin/info in XML: the declaration, then the names JSON drops", func() {
		res := fx.get("/admin/info?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(body(res)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><dummy><info><storage_backends><dummy><name>rados</name><cluster_id>` + fsid + `</cluster_id></dummy></storage_backends></info></dummy>`))
	})
	It("serves /admin/info in HTML: the closed status page, then the dump", func() {
		res := fx.get("/admin/info?format=html", "admin")
		Expect(res.Header.Get("Content-Type")).To(Equal("text/html"))
		Expect(body(res)).To(Equal(`<html><head><title>200 OK</title></head><body><h1>200 OK</h1><ul></ul></body></html><dummy><info><storage_backends><dummy><li>name: rados</li><li>cluster_id: ` + fsid + `</li></dummy></storage_backends></info></dummy>`))
	})
	DescribeTable("selects the format as allocate_formatter does",
		func(query, accept, want string) {
			res := fx.request(http.MethodGet, "/admin/info"+query, "admin", "Accept", accept)
			Expect(res.Header.Get("Content-Type")).To(Equal(want))
		},
		Entry("default", "", "", "application/json"),
		Entry("format=xml", "?format=xml", "", "application/xml"),
		Entry("format wins over Accept", "?format=json", "application/xml", "application/json"),
		Entry("format is exact", "?format=XML", "", "application/json"),
		Entry("Accept up to ';'", "", "application/xml; q=0.9", "application/xml"),
		Entry("text/xml", "", "text/xml", "application/xml"),
		Entry("text/html", "", "text/html", "text/html"),
		Entry("a list is not parsed", "", "application/xml, text/html", "application/json"),
		Entry("*/*", "", "*/*", "application/json"),
	)
	It("answers AccessDenied in radosgw's error document in each format", func() {
		res := fx.get("/admin/info", "nocaps")
		Expect(res.StatusCode).To(Equal(403))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(res.Header.Get("Accept-Ranges")).To(Equal("bytes"), "end_header's error branch sets the length through dump_content_length, rgw_rest.cc:620-624")
		b := body(res)
		Expect(res.Header.Get("Content-Length")).To(Equal(strconv.Itoa(len(b))))
		Expect(b).To(MatchRegexp(`^\{"Code":"AccessDenied","Message":"","RequestId":"` + reqID + `","HostId":"` + regexp.QuoteMeta(hostID) + `"\}$`))
		res = fx.get("/admin/info?format=xml", "nocaps")
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(body(res)).To(MatchRegexp(`^<\?xml version="1\.0" encoding="UTF-8"\?><Error><Code>AccessDenied</Code><Message></Message><RequestId>` + reqID + `</RequestId><HostId>` + regexp.QuoteMeta(hostID) + `</HostId></Error>$`))
		res = fx.get("/admin/info?format=html", "nocaps")
		Expect(res.Header.Get("Content-Type")).To(Equal("text/html"))
		Expect(body(res)).To(MatchRegexp(`^<html><head><title>403 Forbidden</title></head><body><h1>403 Forbidden</h1><ul><li>Code: AccessDenied</li><li>Message: </li><li>RequestId: ` + reqID + `</li><li>HostId: ` + regexp.QuoteMeta(hostID) + `</li></ul></body></html>$`))
		Expect(fx.get("/admin/info", "").StatusCode).To(Equal(403), "anonymous")
	})
	It("answers an authentication failure in the request's format", func() {
		res := fx.get("/admin/info?format=xml", "nosuch")
		Expect(res.StatusCode).To(Equal(403))
		Expect(body(res)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidAccessKeyId</Code>`))
	})
	It("answers 405 in JSON where radosgw has no handler and in the format where it has one", func() {
		res := fx.get("/admin/nosuch?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(405))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"), "abort_early allocates a JSONFormatter")
		Expect(body(res)).To(HavePrefix(`{"Code":"MethodNotAllowed","Message":"","RequestId":"`))
		Expect(fx.get("/admin?format=xml", "admin").Header.Get("Content-Type")).To(Equal("application/json"))
		res = fx.request(http.MethodPost, "/admin/info?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(405))
		Expect(body(res)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>MethodNotAllowed</Code>`))
		Expect(fx.get("/admin/config?type=zonegroup", "admin").StatusCode).To(Equal(405))
		Expect(fx.request(http.MethodPut, "/admin/bucket?sync&bucket=b", "admin").StatusCode).To(Equal(405), "Sync_Bucket is excluded")
		Expect(fx.get("/admin/log?type=metadata", "admin").StatusCode).To(Equal(405))
		Expect(fx.request(http.MethodPost, "/admin/realm/period", "admin").StatusCode).To(Equal(405))
		Expect(fx.request(http.MethodHead, "/admin/info", "admin").StatusCode).To(Equal(405))
	})
	It("answers 400 InvalidRequest in JSON for a NUL in the path, which preprocess refuses first", func() {
		res := fx.get("/admin/info%00?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(400))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(body(res)).To(HavePrefix(`{"Code":"InvalidRequest",`))
	})
	It("answers an unregistered route NotImplemented, in the request's format, for a caller its op admits", func() {
		res := fx.get("/admin/bucket?index&bucket=b&format=xml", "admin")
		Expect(res.StatusCode).To(Equal(501))
		Expect(body(res)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NotImplemented</Code>`))
	})
	It("refuses a caller an unregistered route's op would refuse", func() {
		Expect(fx.get("/admin/bucket?index&bucket=b", "").StatusCode).To(Equal(403), "anonymous")
		res := fx.get("/admin/bucket?index&bucket=b", "nocaps")
		Expect(res.StatusCode).To(Equal(403))
		Expect(body(res)).To(HavePrefix(`{"Code":"AccessDenied",`))
		Expect(fx.get("/admin/account?id=RGW00000000000000001", "admin").StatusCode).To(Equal(403),
			"Squid's account get checks the cap \"account\", which no caps command grants")
	})
	It("admits an accounts holder to the account get on Tentacle, which checks \"accounts\"", func() {
		fx = newFixtureAt(denc.Tentacle, admin.Config{})
		Expect(fx.get("/admin/account?id=RGW00000000000000001", "admin").StatusCode).To(Equal(404), "admitted, and no such account")
		Expect(fx.get("/admin/account?id=RGW00000000000000001", "nocaps").StatusCode).To(Equal(403))
	})
	It("hands the authenticator the forms the route's op type accepts", func() {
		fx.request(http.MethodPut, "/admin/metadata/user?key=admin", "admin")
		Expect(fx.payloads()).To(Equal(op.PayloadSigned), "RGW_OP_ADMIN_SET_METADATA is on the single-chunk list")
		fx.request(http.MethodPut, "/admin/user?quota&uid=admin&quota-type=user", "admin")
		Expect(fx.payloads()).To(Equal(op.PayloadForms(0)))
		fx.get("/admin/info", "admin")
		Expect(fx.payloads()).To(Equal(op.PayloadForms(0)))
	})
	It("gives the op the authenticator's body and length, and counts no bytes in", func() {
		var got string
		var gotLen int64
		fx.handler.Register("set_metadata", func(_ context.Context, w http.ResponseWriter, r *op.Request, _ admin.Request) error {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				return err
			}
			got, gotLen = string(b), r.ContentLength
			admin.WriteEmpty(w, r, http.StatusOK)
			return nil
		})
		res := fx.requestBody(http.MethodPut, "/admin/metadata/user?key=admin", "admin", strings.NewReader("the wire body"), "X-Test-Body", "verified")
		Expect(res.StatusCode).To(Equal(200))
		Expect(got).To(Equal("verified"))
		Expect(gotLen).To(Equal(int64(len("verified"))))
		Expect(fx.metrics.ObserveCallCount()).To(Equal(1))
		name, status, _, in, _ := fx.metrics.ObserveArgsForCall(0)
		Expect([]any{name, status, in}).To(Equal([]any{"set_metadata", 200, int64(0)}))
	})
	It("keeps the wire body when the authenticator replaces none", func() {
		var got string
		fx.handler.Register("set_metadata", func(_ context.Context, w http.ResponseWriter, r *op.Request, _ admin.Request) error {
			b, err := io.ReadAll(r.Body)
			got = string(b)
			admin.WriteEmpty(w, r, http.StatusOK)
			return err
		})
		fx.requestBody(http.MethodPut, "/admin/metadata/user?key=admin", "admin", strings.NewReader("the wire body"))
		Expect(got).To(Equal("the wire body"))
	})
	It("returns the zone params for config?type=zone without a declaration or dump_start", func() {
		want := func(f formatter.Formatter) string {
			f.OpenObjectSection("zone_params")
			fx.store.ZoneParams().Dump(f, fx.store.Release())
			f.CloseSection()
			return string(f.Bytes())
		}
		res := fx.get("/admin/config?type=zone", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(body(res)).To(Equal(want(formatter.NewJSON(false))))
		res = fx.get("/admin/config?type=zone&format=xml", "admin")
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		b := body(res)
		Expect(b).To(Equal(want(formatter.NewXML(false, false))))
		Expect(b).To(HavePrefix("<zone_params><id>zid</id><name>z1</name><domain_root>z1.rgw.meta:root</domain_root>"))
		Expect(fx.get("/admin/config?type=zone", "nocaps").StatusCode).To(Equal(403))
	})
	It("refuses a suspended user", func() {
		fx.suspend("admin")
		res := fx.get("/admin/info", "admin")
		Expect(res.StatusCode).To(Equal(403))
		var doc map[string]string
		Expect(json.Unmarshal([]byte(body(res)), &doc)).To(Succeed())
		Expect(doc["Code"]).To(Equal("UserSuspended"))
	})
	It("lets an admin user through without the cap, as rgw_process_authenticated does", func() {
		ctx := context.Background()
		rec, err := fx.store.GetUser(ctx, meta.UserID{ID: "nocaps"})
		Expect(err).NotTo(HaveOccurred())
		rec.Info.Admin = 1
		Expect(fx.store.PutUser(ctx, rec, op.PutUserOptions{})).To(Succeed())
		Expect(fx.get("/admin/info", "nocaps").StatusCode).To(Equal(200))
	})
	It("logs no usage: radosgw registers the admin API without set_logging", func() {
		Expect(fx.get("/admin/info", "admin").StatusCode).To(Equal(200))
		Expect(fx.get("/admin/info", "nocaps").StatusCode).To(Equal(403))
		Expect(fx.get("/admin/nosuch", "admin").StatusCode).To(Equal(405))
		Expect(fx.store.Usage()).To(BeEmpty())
	})
	It("observes each request under its route, or unknown before dispatch", func() {
		b := body(fx.get("/admin/info", "admin"))
		body(fx.get("/admin/nosuch", "admin"))
		Expect(fx.metrics.ObserveCallCount()).To(Equal(2))
		name, status, _, in, out := fx.metrics.ObserveArgsForCall(0)
		Expect([]any{name, status, in, out}).To(Equal([]any{"get_info", 200, int64(0), int64(len(b))}))
		name, status, _, _, _ = fx.metrics.ObserveArgsForCall(1)
		Expect([]any{name, status}).To(Equal([]any{"unknown", 405}))
		Expect(fx.metrics.InFlightCallCount()).To(Equal(4))
	})
	It("answers InternalError for a route that panics before writing", func() {
		fx.handler.Register("get_info", func(context.Context, http.ResponseWriter, *op.Request, admin.Request) error {
			panic("boom")
		})
		res := fx.get("/admin/info?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(500))
		Expect(body(res)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InternalError</Code>`))
	})
	It("answers NotImplemented for a record carried opaque, before writing anything", func() {
		fx.handler.Register("get_info", func(_ context.Context, w http.ResponseWriter, r *op.Request, q admin.Request) error {
			return admin.WriteBody(w, r, q, func(f formatter.Formatter) {
				f.DumpString("partial", "x")
				f.Fail(fmt.Errorf("dumping: %w", meta.ErrOpaqueJSON))
			})
		})
		res := fx.get("/admin/info", "admin")
		Expect(res.StatusCode).To(Equal(501))
		Expect(body(res)).To(HavePrefix(`{"Code":"NotImplemented",`))
	})
	Describe("logging", func() {
		const (
			secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
			authz  = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261005/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=" + secret
		)
		var logs *bytes.Buffer
		BeforeEach(func() {
			logs = &bytes.Buffer{}
			old := slog.Default()
			slog.SetDefault(slog.New(op.NewLogHandler(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
			DeferCleanup(func() { slog.SetDefault(old) })
		})
		records := func() []map[string]any {
			GinkgoHelper()
			var recs []map[string]any
			for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
				var rec map[string]any
				Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed(), line)
				recs = append(recs, rec)
			}
			return recs
		}

		DescribeTable("puts the request id on every line logged while a request is served, once",
			func(route admin.HandlerFunc, msgs ...string) {
				fx.handler.Register("get_info", route)
				res := fx.get("/admin/info", "admin")
				recs := records()
				for _, msg := range msgs {
					Expect(recs).To(ContainElement(HaveKeyWithValue("msg", msg)))
				}
				for _, r := range recs {
					Expect(r).To(HaveKeyWithValue("request_id", res.Header.Get("x-amz-request-id")), "line %q", r["msg"])
				}
				Expect(strings.Count(logs.String(), `"request_id"`)).To(Equal(len(recs)), "one request_id a line")
			},
			Entry("a route and the op layer under it", admin.HandlerFunc(func(ctx context.Context, w http.ResponseWriter, r *op.Request, _ admin.Request) error {
				slog.InfoContext(ctx, "serving")
				_, err := op.BucketACLFor(ctx, &op.BucketRecord{Info: meta.BucketInfo{Bucket: meta.BucketID{Name: "b"}}})
				if err != nil {
					return err
				}
				admin.WriteEmpty(w, r, http.StatusOK)
				return nil
			}), "serving", "couldn't find acl header for bucket, generating default", "admin request done"),
			Entry("a route's InternalError", admin.HandlerFunc(func(context.Context, http.ResponseWriter, *op.Request, admin.Request) error {
				return fmt.Errorf("%w: rados: timed out", op.ErrInternalError)
			}), "admin request failed", "admin request done"),
			Entry("a route that panics", admin.HandlerFunc(func(context.Context, http.ResponseWriter, *op.Request, admin.Request) error {
				panic("boom")
			}), "admin request handler panicked"),
			Entry("a route that fails after its response started", admin.HandlerFunc(func(_ context.Context, w http.ResponseWriter, _ *op.Request, _ admin.Request) error {
				w.WriteHeader(http.StatusOK)
				return op.ErrInternalError
			}), "admin route failed after its response started"),
		)
		// serveGone serves GET /admin/info as admin through h under rctx, which
		// the spec cancels as net/http cancels a request's context once its
		// connection is gone, and expects the connection to end with nothing
		// written.
		serveGone := func(rctx context.Context, h http.Handler) *httptest.ResponseRecorder {
			GinkgoHelper()
			req := httptest.NewRequestWithContext(rctx, http.MethodGet, "/admin/info", nil)
			req.Header.Set("X-Test-User", "admin")
			rec := httptest.NewRecorder()
			Expect(func() { h.ServeHTTP(rec, req) }).To(PanicWith(http.ErrAbortHandler), "net/http then sends nothing of its own")
			Expect(rec.Body.Len()).To(BeZero())
			Expect(rec.Header()).NotTo(HaveKey("Content-Type"), "no error document")
			return rec
		}
		goneLine := func(rec *httptest.ResponseRecorder, status int, code string) {
			GinkgoHelper()
			Expect(fx.metrics.ObserveCallCount()).To(Equal(1))
			_, observed, _, _, _ := fx.metrics.ObserveArgsForCall(0)
			Expect(observed).To(Equal(status))
			recs := records()
			Expect(recs).To(ContainElement(SatisfyAll(
				HaveKeyWithValue("level", "DEBUG"),
				HaveKeyWithValue("msg", "client went away"),
				HaveKeyWithValue("request_id", exactHeader(rec.Header(), "x-amz-request-id")),
				HaveKeyWithValue("code", code),
			)))
			Expect(recs).NotTo(ContainElement(HaveKeyWithValue("level", Not(Equal("DEBUG")))))
		}
		It("answers a client that went away with nothing, ends the connection and logs it at debug by its code alone", func(ctx SpecContext) {
			rctx, cancel := context.WithCancel(ctx)
			defer cancel()
			fx.handler.Register("get_info", func(ctx context.Context, _ http.ResponseWriter, _ *op.Request, _ admin.Request) error {
				cancel()
				// A store's error names the index object it read, here the key.
				return op.FromRADOS(fmt.Errorf("reading default.rgw.meta/AKIDGONE: %w", ctx.Err()), op.ScopeUser)
			})
			goneLine(serveGone(rctx, fx.handler), 408, "RequestTimeout")
			Expect(logs.String()).NotTo(ContainSubstring("AKIDGONE"), "the access key the lookup read")
		})
		It("ends a request whose client went away during authentication, answering nothing", func(ctx SpecContext) {
			rctx, cancel := context.WithCancel(ctx)
			defer cancel()
			h := admin.NewHandler(fx.env, s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				cancel()
				return nil, fmt.Errorf("%w: the request's access key does not load: %w", op.ErrInvalidAccessKeyID, context.Canceled)
			}), admin.Config{Prefix: "admin"})
			goneLine(serveGone(rctx, h), 403, "InvalidAccessKeyId")
		})

		It("logs a route's failure after its response started by its code alone", func() {
			fx.handler.Register("get_info", func(_ context.Context, w http.ResponseWriter, _ *op.Request, _ admin.Request) error {
				w.WriteHeader(http.StatusOK)
				// A store's error names the index object it read, here the key.
				return fmt.Errorf("%w: reading default.rgw.meta/AKAFTERSTART: rados: EIO", op.ErrInternalError)
			})
			Expect(fx.get("/admin/info", "admin").StatusCode).To(Equal(200))
			Expect(records()).To(ContainElement(SatisfyAll(
				HaveKeyWithValue("msg", "admin route failed after its response started"),
				HaveKeyWithValue("code", "InternalError"),
			)))
			Expect(logs.String()).NotTo(ContainSubstring("AKAFTERSTART"))
		})
		It("logs an authentication's server error once, with the request id and its code and no credential", func() {
			res := fx.request(http.MethodGet, "/admin/info", "admin", "Authorization", authz, "X-Test-Fail", "1")
			Expect(res.StatusCode).To(Equal(500))
			var errs []map[string]any
			for _, r := range records() {
				if r["level"] == "ERROR" {
					errs = append(errs, r)
				}
			}
			Expect(errs).To(ConsistOf(SatisfyAll(
				HaveKeyWithValue("msg", "authentication failed"),
				HaveKeyWithValue("request_id", res.Header.Get("x-amz-request-id")),
				HaveKeyWithValue("code", "InternalError"),
			)))
			Expect(logs.String()).NotTo(ContainSubstring(secret), "the signature")
			Expect(logs.String()).NotTo(ContainSubstring("AKIDEXAMPLE"), "the access key")
		})
		It("never logs credential material an authentication failure carries", func() {
			res := fx.request(http.MethodGet, "/admin/info", "admin", "Authorization", authz, "X-Test-Fail", "1")
			Expect(res.StatusCode).To(Equal(500))
			Expect(body(res)).To(HavePrefix(`{"Code":"InternalError",`))
			Expect(logs.String()).NotTo(ContainSubstring(secret))
			Expect(logs.String()).NotTo(ContainSubstring("AKIDEXAMPLE"))
		})
		It("logs a route's InternalError with its cause and no request header", func() {
			fx.handler.Register("get_info", func(context.Context, http.ResponseWriter, *op.Request, admin.Request) error {
				return fmt.Errorf("%w: reading the zone: rados timed out", op.ErrInternalError)
			})
			res := fx.request(http.MethodGet, "/admin/info", "admin", "Authorization", authz)
			Expect(res.StatusCode).To(Equal(500))
			Expect(logs.String()).To(ContainSubstring("rados timed out"))
			Expect(logs.String()).To(ContainSubstring(`"code":"InternalError"`))
			Expect(logs.String()).NotTo(ContainSubstring(secret))
		})
	})
	It("panics on a route name Dispatch never yields", func() {
		Expect(func() {
			fx.handler.Register("sync_bucket", func(context.Context, http.ResponseWriter, *op.Request, admin.Request) error { return nil })
		}).To(PanicWith(ContainSubstring("sync_bucket")))
	})
	It("parses the decoded URI, a Host-named bucket in front, as radosgw's manager lookup does", func() {
		fx = newFixture(admin.Config{DNSNames: []string{"s3.example.com"}})
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fx.server.URL+"/info", nil)
		Expect(err).NotTo(HaveOccurred())
		req.Host = "admin.s3.example.com"
		req.Header.Set("X-Test-User", "admin")
		res := do(req)
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(ContainSubstring(fsid))
	})
	It("serves under a nested rgw_admin_entry", func() {
		fx = newFixture(admin.Config{Prefix: "a/b"})
		Expect(fx.get("/a/b/info", "admin").StatusCode).To(Equal(200))
	})
	It("answers SlowDown past the concurrency cap, in the request's format", func() {
		fx = newFixture(admin.Config{MaxConcurrent: 1})
		release := make(chan struct{})
		entered := make(chan struct{})
		fx.handler.Register("get_info", func(_ context.Context, w http.ResponseWriter, r *op.Request, _ admin.Request) error {
			close(entered)
			<-release
			admin.WriteEmpty(w, r, http.StatusOK)
			return nil
		})
		done := make(chan int)
		go func() {
			defer GinkgoRecover()
			res := fx.get("/admin/info", "admin")
			_ = body(res)
			done <- res.StatusCode
		}()
		Eventually(entered).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(BeClosed())
		Expect(fx.handler.InFlight()).To(Equal(int64(1)))
		res := fx.get("/admin/info?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(503))
		Expect(body(res)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>SlowDown</Code>`))
		close(release)
		Eventually(done).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(200)))
		Expect(fx.handler.InFlight()).To(BeZero())
	})
})

// exactHeader is the first value the handler's header map h holds under
// exactly name, or "" when there is none. net/http writes a key as the
// handler spelled it; http.Header.Get would look up the canonical spelling
// instead.
func exactHeader(h http.Header, name string) string {
	if v := h[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}
