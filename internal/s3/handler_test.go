package s3_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

func testEnv(store *memstore.Store) *op.Env {
	return &op.Env{
		Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store,
		Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{},
		Now: func() time.Time { return time.Unix(0x68d7a1b2, 0) }, HostID: "4155-z-zg",
	}
}

func testConfig(cfg s3.Config) s3.Config {
	if cfg.TransIDSuffix == "" {
		cfg.TransIDSuffix = "-4155-z"
	}
	if cfg.ServerHeader == "" {
		cfg.ServerHeader = "Ceph Object Gateway (squid)"
	}
	return cfg
}

func newHandler(store *memstore.Store, auth s3.Authenticator, cfg s3.Config) *s3.Handler {
	return s3.NewHandler(testEnv(store), auth, testConfig(cfg))
}

// authAs authenticates every request as rec's user.
func authAs(rec *op.UserRecord) s3.Authenticator {
	return s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
		return &op.AuthResult{Identity: op.Identity{User: &rec.Info, Owner: meta.UserOwner(rec.Info.UserID), OpMask: rec.Info.OpMask}}, nil
	})
}

// serveReq sends method and target through h under the spec's context; hdr
// alternates header names and values.
func serveReq(h http.Handler, method, target string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(GinkgoT().Context(), method, target, body)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// captureLog is captureLogAt at slog's default level, info.
func captureLog() *bytes.Buffer { return captureLogAt(slog.LevelInfo) }

// captureLogAt sends slog's default logger at level to a buffer as JSON until
// the spec ends, through the request-id handler the binary installs.
// slog.SetDefault also points the log package at the new handler, and
// restoring the old default leaves it there, so log's writer and flags are
// restored too.
func captureLogAt(level slog.Level) *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(op.NewLogHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level}))))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

// logRecords decodes the JSON lines captureLog collected.
func logRecords(buf *bytes.Buffer) []map[string]any {
	var recs []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed())
		recs = append(recs, rec)
	}
	return recs
}

// ok is a route that answers 200 and records the request it served.
func ok(seen **op.Request) s3.HandlerFunc {
	return func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
		*seen = r
		w.WriteHeader(http.StatusOK)
		return nil
	}
}

var _ = Describe("Handler", func() {
	var (
		store *memstore.Store
		h     *s3.Handler
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{})
		h = newHandler(store, s3.AnonymousOnly{}, s3.Config{})
	})
	get := func(target string, hdr ...string) *httptest.ResponseRecorder {
		return serveReq(h, http.MethodGet, target, nil, hdr...)
	}
	It("answers an anonymous GET / with 200 and an empty ListAllMyBucketsResult, the probe path", func() {
		rec := get("/")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
			`<Owner><ID>anonymous</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>`))
	})
	It("stamps x-amz-request-id and Server on every response", func() {
		const id = `^tx[0-9a-f]{21}-0068d7a1b2-4155-z$`
		rec := get("/")
		first := rec.Header().Get("x-amz-request-id")
		Expect(first).To(MatchRegexp(id))
		Expect(rec.Header().Get("Server")).To(Equal("Ceph Object Gateway (squid)"))
		second := get("/").Header().Get("x-amz-request-id")
		Expect(second).To(MatchRegexp(id))
		Expect(second).NotTo(Equal(first))
		other := serveReq(newHandler(store, s3.AnonymousOnly{}, s3.Config{}), http.MethodGet, "/", nil)
		Expect(other.Header().Get("x-amz-request-id")).NotTo(Equal(first),
			"a second handler's first number is drawn at random, as radosgw's get_new_req_id draws it, not counted from the same start")
		rec = serveReq(h, "PATCH", "/", nil)
		Expect(rec.Code).To(Equal(405))
		Expect(rec.Header().Get("x-amz-request-id")).To(MatchRegexp(id), "refused before dispatch")
		Expect(rec.Header().Get("Server")).To(Equal("Ceph Object Gateway (squid)"))
	})
	It("renders radosgw's error document for a route without a handler", func() {
		rec := get("/plain?website")
		Expect(rec.Code).To(Equal(501))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NotImplemented</Code><Message></Message>` +
			`<BucketName>plain</BucketName><RequestId>` + rec.Header().Get("x-amz-request-id") + `</RequestId><HostId>4155-z-zg</HostId></Error>`))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "end_header's error branch sets the length through dump_content_length, rgw_rest.cc:620-624")
	})
	It("answers 405 MethodNotAllowed where radosgw has no op", func() {
		rec := serveReq(h, http.MethodPost, "/", nil)
		Expect(rec.Code).To(Equal(405))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>MethodNotAllowed</Code>"))
		Expect(rec.Body.String()).NotTo(ContainSubstring("<BucketName>"), "no bucket in a service-scope error")
	})
	It("answers 400 for a NUL in the path before dispatch", func() {
		rec := get("/plain/a%00b")
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRequest</Code>"))
		Expect(rec.Body.String()).NotTo(ContainSubstring("<BucketName>"))
	})
	DescribeTable("never serves a signed request as anonymous",
		func(target string, hdr ...string) {
			rec := get(target, hdr...)
			Expect(rec.Code).To(Equal(501), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NotImplemented</Code>"))
			Expect(rec.Body.String()).NotTo(ContainSubstring("<BucketName>"), "authentication runs before postauth_init names the bucket")
		},
		Entry("Authorization header", "/plain", "Authorization", "AWS4-HMAC-SHA256 Credential=x"),
		Entry("SigV4 query signature", "/?X-Amz-Signature=abc"),
		Entry("SigV4 query signature, lowercased", "/?x-amz-signature=abc"),
		Entry("SigV4 query credential", "/?X-Amz-Credential=abc"),
		Entry("SigV2 query key", "/?AWSAccessKeyId=abc"),
		Entry("SigV2 query signature", "/?Signature=abc"),
	)
	It("answers 503 SlowDown past rgw_max_concurrent_requests and stays healthy for the probe", func(ctx SpecContext) {
		release := make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		DeferCleanup(unblock)
		m := &opfakes.FakeMetrics{}
		env := testEnv(store)
		env.Metrics = m
		slow := s3.NewHandler(env, s3.AnonymousOnly{}, testConfig(s3.Config{MaxConcurrent: 1}))
		slow.Register("list_buckets", func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
			<-release
			w.WriteHeader(200)
			return nil
		})
		first := make(chan int, 1)
		go func() { // stops once release closes, at the latest when the spec ends
			rec := httptest.NewRecorder()
			slow.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
			first <- rec.Code
		}()
		Eventually(slow.InFlight).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(1))
		rec := serveReq(slow, http.MethodGet, "/", nil)
		Expect(rec.Code).To(Equal(503))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>SlowDown</Code>"))
		Expect(rec.Header().Get("x-amz-request-id")).NotTo(BeEmpty())
		Expect(serveReq(slow, http.MethodGet, "/plain/a%00b", nil).Code).To(Equal(400), "a request refused before dispatch is not counted")
		name, status, _, _, _ := m.ObserveArgsForCall(0)
		Expect([]any{name, status}).To(Equal([]any{"list_buckets", 503}), "the cap refuses a dispatched op, which rgw_log_op names")
		name, status, _, _, _ = m.ObserveArgsForCall(1)
		Expect([]any{name, status}).To(Equal([]any{"unknown", 400}))
		unblock()
		Expect(<-first).To(Equal(200))
		Expect(slow.InFlight()).To(BeZero())
	})
	It("suspends a suspended user", func() {
		alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, Suspended: 1, OpMask: op.OpTypeAll})
		h = newHandler(store, authAs(alice), s3.Config{})
		rec := get("/")
		Expect(rec.Code).To(Equal(403))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>UserSuspended</Code>"))
		rec = get("/plain")
		Expect(rec.Code).To(Equal(403))
		Expect(rec.Body.String()).To(ContainSubstring("<BucketName>plain</BucketName>"), "postauth_init has named the bucket")
	})
	It("gives a bucket named in the URL the identity's tenant unless the URL named one", func() {
		var seen *op.Request
		as := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{User: &meta.UserInfo{}, Tenant: "t1", OpMask: op.OpTypeAll}}, nil
		})
		h = newHandler(store, as, s3.Config{})
		h.Register("stat_bucket", ok(&seen))
		serveReq(h, http.MethodHead, "/plain", nil)
		Expect(seen.Tenant).To(Equal("t1"), "identity tenant")
		Expect(seen.Bucket).To(Equal("plain"))
		serveReq(h, http.MethodHead, "/t2:plain", nil)
		Expect(seen.Tenant).To(Equal("t2"), "explicit tenant")
		Expect(seen.Bucket).To(Equal("plain"))
	})
	DescribeTable("names the bucket in an error document only once postauth_init has run",
		func(method, target, body string, hdr ...string) {
			rec := serveReq(h, method, target, nil, hdr...)
			Expect(rec.Body.String()).To(ContainSubstring(body))
			if strings.Contains(body, "<BucketName>") {
				return
			}
			Expect(rec.Body.String()).NotTo(ContainSubstring("<BucketName>"))
		},
		Entry("a route without an op, refused in get_op", http.MethodPut, "/plain?logging", "<Code>MethodNotAllowed</Code>"),
		Entry("an object subresource on a bucket, refused in get_handler", http.MethodGet, "/plain?partNumber=1", "<Code>MethodNotAllowed</Code>"),
		Entry("a bucket token with nothing after its colon, never the unsplit token", http.MethodGet, "/t1:", "<Code>InvalidBucketName</Code>"),
		Entry("a bad tenant, after the bucket is named", http.MethodGet, "/bad-t:plain", "<BucketName>plain</BucketName>"),
		Entry("an overlong object name, after the bucket is named", http.MethodGet, "/plain/"+strings.Repeat("k", 1025), "<BucketName>plain</BucketName>"),
	)
	// RGWHandler_REST_S3::init parses x-amz-copy-source inside get_handler,
	// after get_handler's own refusal of an object subresource on a bucket and
	// before get_op's refusals and authentication (rgw_process.cc:310-327 at
	// v19.2.6, rgw_rest_s3.cc:5026-5041).
	DescribeTable("refuses a copy source radosgw cannot parse where RGWHandler_REST_S3::init does",
		func(method, target string, code int, errCode string, hdr ...string) {
			rec := serveReq(h, method, target, nil, hdr...)
			Expect(rec.Code).To(Equal(code), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>" + errCode + "</Code>"))
		},
		Entry("on any method and scope", http.MethodGet, "/plain/k", 400, "InvalidArgument", "x-amz-copy-source", "/src"),
		Entry("with an empty key", http.MethodPut, "/plain/k", 400, "InvalidArgument", "x-amz-copy-source", "src/"),
		Entry("before get_op's refusal of an unknown method", "PATCH", "/plain/k", 400, "InvalidArgument", "x-amz-copy-source", "/src"),
		Entry("before get_op's refusal of a route without an op", http.MethodPut, "/plain?logging", 400, "InvalidArgument", "x-amz-copy-source", "/src"),
		Entry("before authentication", http.MethodGet, "/plain/k", 400, "InvalidArgument",
			"x-amz-copy-source", "/src", "Authorization", "AWS4-HMAC-SHA256 Credential=x"),
		Entry("after get_handler's refusal of an object subresource on a bucket", http.MethodPut, "/plain?partNumber=1", 405, "MethodNotAllowed",
			"x-amz-copy-source", "/src"),
		Entry("not with x-amz-copy-source-range, which init leaves unparsed", http.MethodPut, "/plain/k", 501, "NotImplemented",
			"x-amz-copy-source", "/src", "x-amz-copy-source-range", "bytes=0-1"),
	)
	It("records the status and byte counts on the request for metrics", func() {
		m := &opfakes.FakeMetrics{}
		env := &op.Env{Zone: store, Users: store, Authz: op.OwnerOnly{}, Metrics: m}
		h = s3.NewHandler(env, s3.AnonymousOnly{}, s3.Config{})
		rec := get("/")
		Expect(m.ObserveCallCount()).To(Equal(1))
		name, status, _, in, out := m.ObserveArgsForCall(0)
		Expect(name).To(Equal("list_buckets"))
		Expect(status).To(Equal(200))
		Expect(in).To(BeZero())
		Expect(out).To(BeEquivalentTo(rec.Body.Len()))
		Expect(m.InFlightCallCount()).To(Equal(2), "one increment, one decrement")
		Expect(m.InFlightArgsForCall(0) + m.InFlightArgsForCall(1)).To(BeZero())
	})
	It("observes a request refused before dispatch under radosgw's unknown op name", func() {
		m := &opfakes.FakeMetrics{}
		env := testEnv(store)
		env.Metrics = m
		h = s3.NewHandler(env, s3.AnonymousOnly{}, testConfig(s3.Config{}))
		serveReq(h, http.MethodPost, "/", nil)
		Expect(m.ObserveCallCount()).To(Equal(1))
		name, status, _, _, _ := m.ObserveArgsForCall(0)
		Expect(name).To(Equal("unknown"), "rgw_log_op names a request no op serves unknown, rgw_log.cc:556")
		Expect(status).To(Equal(405))
	})
	// process_request dispatches, sets s->op_type and only then authenticates
	// (rgw_process.cc:325-345 at v19.2.6), so the authenticator sees the
	// dispatched op's payload forms at the cluster's release.
	DescribeTable("hands the authenticator the dispatched route's payload forms",
		func(rel denc.Release, method, target string, want op.PayloadForms) {
			var got op.PayloadForms
			as := s3.AuthenticatorFunc(func(_ context.Context, _ *http.Request, payloads op.PayloadForms) (*op.AuthResult, error) {
				got = payloads
				return &op.AuthResult{Identity: op.Anonymous(), PayloadSHA256: "UNSIGNED-PAYLOAD"}, nil
			})
			rs := memstore.New(memstore.Config{Release: rel})
			serveReq(newHandler(rs, as, s3.Config{}), method, target, nil)
			Expect(got).To(Equal(want))
		},
		Entry("PUT object", denc.Squid, "PUT", "/plain/k", op.PayloadSigned|op.PayloadChunked),
		Entry("GET ?location", denc.Squid, "GET", "/plain?location", op.PayloadForms(0)),
		Entry("GET ?logging on Squid", denc.Squid, "GET", "/plain?logging", op.PayloadForms(0)),
		Entry("GET ?logging on Tentacle", denc.Tentacle, "GET", "/plain?logging", op.PayloadSigned),
	)
	Describe("the request body", func() {
		It("is the authenticator's body when it returns one, counted on put_obj as the op reads it", func() {
			as := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				return &op.AuthResult{Identity: op.Anonymous(), Body: strings.NewReader("decoded"), ContentLength: int64(len("decoded"))}, nil
			})
			h = newHandler(store, as, s3.Config{})
			var seen *op.Request
			var got []byte
			h.Register("put_obj", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
				seen = r
				var err error
				got, err = io.ReadAll(r.Body)
				w.WriteHeader(200)
				return err
			})
			serveReq(h, http.MethodPut, "/plain/k", strings.NewReader("5\r\nhello\r\n0\r\n\r\n"))
			Expect(string(got)).To(Equal("decoded"))
			Expect(seen.BytesIn).To(BeEquivalentTo(len("decoded")), "radosgw's accounting sits above the chunk decoder")
		})
		// AWSv4ComplMulti::modify_request_state sets content_length to
		// x-amz-decoded-content-length (rgw_auth_s3.cc:1532-1542 at v19.2.6,
		// :1509-1519 at v20.2.4).
		DescribeTable("comes with the authenticator's content length, not the wire's",
			func(length int64) {
				alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
				as := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
					return &op.AuthResult{
						Identity:      op.Identity{User: &alice.Info, Owner: meta.UserOwner(alice.Info.UserID), OpMask: op.OpTypeAll},
						Body:          strings.NewReader("hello"),
						ContentLength: length,
						PayloadSHA256: "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
					}, nil
				})
				h = newHandler(store, as, s3.Config{})
				var (
					seen *op.Request
					got  []byte
				)
				h.Register("put_obj", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
					seen = r
					var err error
					got, err = io.ReadAll(r.Body)
					w.WriteHeader(http.StatusOK)
					return err
				})
				const sig = "0000000000000000000000000000000000000000000000000000000000000000"
				wire := "5;chunk-signature=" + sig + "\r\nhello\r\n0;chunk-signature=" + sig + "\r\n\r\n"
				rec := serveReq(h, http.MethodPut, "/plain/k", strings.NewReader(wire), "X-Amz-Decoded-Content-Length", "5")
				Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
				Expect(string(got)).To(Equal("hello"))
				Expect(seen.ContentLength).To(Equal(length), "the authenticator's length, not the wire's %d", len(wire))
				Expect(seen.BytesIn).To(BeEquivalentTo(5), "the decoded bytes the op read, not the wire's")
			},
			Entry("the decoded length of an aws-chunked upload", int64(5)),
			Entry("-1, a length the authenticator does not know", int64(-1)),
		)
		It("counts what a plain put_obj reads, and keeps its length", func() {
			var seen *op.Request
			h.Register("put_obj", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
				seen = r
				_, err := io.Copy(io.Discard, r.Body)
				w.WriteHeader(200)
				return err
			})
			serveReq(h, http.MethodPut, "/plain/k", strings.NewReader("hello"))
			Expect(seen.BytesIn).To(BeEquivalentTo(5))
			Expect(seen.ContentLength).To(BeEquivalentTo(5), "the request's own, the authenticator returning no body")
		})
		It("counts nothing on any other route, though the op reads a body", func() {
			var seen *op.Request
			var got []byte
			h.Register("create_bucket", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
				seen = r
				var err error
				got, err = io.ReadAll(r.Body)
				w.WriteHeader(200)
				return err
			})
			serveReq(h, http.MethodPut, "/plain", strings.NewReader("<CreateBucketConfiguration/>"))
			Expect(string(got)).To(Equal("<CreateBucketConfiguration/>"))
			Expect(seen.BytesIn).To(BeZero(), "radosgw counts only PutObject's data reads, rgw_rest.cc:1082-1093 at v19.2.6")
		})
		It("is left unread when the op does not read it", func() {
			body := &countingBody{r: strings.NewReader("hello")}
			var seen *op.Request
			h.Register("put_obj", ok(&seen))
			serveReq(h, http.MethodPut, "/plain/k", body)
			Expect(body.n).To(BeZero())
			Expect(seen.BytesIn).To(BeZero())
		})
	})
	Describe("route binding", func() {
		var calls []string
		route := func(name string) s3.HandlerFunc {
			return func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
				calls = append(calls, name)
				w.WriteHeader(http.StatusOK)
				return nil
			}
		}
		BeforeEach(func() { calls = nil })
		It("binds each unit's entries to that unit's scope, so bucket and object get_acls reach different handlers", func() {
			h = s3.NewHandlerWithForTest(testEnv(store), s3.AnonymousOnly{}, testConfig(s3.Config{}), s3.UnitHandlersForTest{
				Bucket: map[string]s3.HandlerFunc{"get_acls": route("bucket")},
				Object: map[string]s3.HandlerFunc{"get_acls": route("object")},
			})
			Expect(get("/plain?acl").Code).To(Equal(200))
			Expect(get("/plain/k?acl").Code).To(Equal(200))
			Expect(calls).To(Equal([]string{"bucket", "object"}))
		})
		It("binds a multipart entry to the one scope that routes it", func() {
			h = s3.NewHandlerWithForTest(testEnv(store), s3.AnonymousOnly{}, testConfig(s3.Config{}), s3.UnitHandlersForTest{
				Multipart: map[string]s3.HandlerFunc{"list_bucket_multiparts": route("uploads"), "list_multipart": route("parts")},
			})
			Expect(get("/plain?uploads").Code).To(Equal(200))
			Expect(get("/plain/k?uploadId=u").Code).To(Equal(200))
			Expect(calls).To(Equal([]string{"uploads", "parts"}))
		})
		It("refuses an entry whose scope never routes its name", func() {
			Expect(func() {
				s3.NewHandlerWithForTest(testEnv(store), s3.AnonymousOnly{}, s3.Config{}, s3.UnitHandlersForTest{
					Object: map[string]s3.HandlerFunc{"list_buckets": route("x")},
				})
			}).To(PanicWith(ContainSubstring(`"list_buckets"`)))
		})
		It("refuses a multipart entry routed in more than one scope", func() {
			Expect(func() {
				s3.NewHandlerWithForTest(testEnv(store), s3.AnonymousOnly{}, s3.Config{}, s3.UnitHandlersForTest{
					Multipart: map[string]s3.HandlerFunc{"get_acls": route("x")},
				})
			}).To(PanicWith(ContainSubstring(`"get_acls"`)))
		})
		It("refuses a scope and name bound twice", func() {
			Expect(func() {
				s3.NewHandlerWithForTest(testEnv(store), s3.AnonymousOnly{}, s3.Config{}, s3.UnitHandlersForTest{
					Object:    map[string]s3.HandlerFunc{"list_multipart": route("x")},
					Multipart: map[string]s3.HandlerFunc{"list_multipart": route("y")},
				})
			}).To(PanicWith(ContainSubstring(`"list_multipart"`)))
		})
		It("registers a name under every scope that routes it, replacing what is there", func() {
			h.Register("get_acls", route("both"))
			get("/plain?acl")
			get("/plain/k?acl")
			Expect(calls).To(Equal([]string{"both", "both"}))
			Expect(func() { h.Register("no_such_op", route("x")) }).To(PanicWith(ContainSubstring(`"no_such_op"`)))
		})
		It("registers Dispatch's fallback routes outside the table", func() {
			h.Register("list_bucket_v2", route("v2"))
			h.Register("copy_obj", route("copy"))
			get("/plain?list-type=2")
			serveReq(h, http.MethodPut, "/plain/k", nil, "x-amz-copy-source", "/src/key")
			Expect(calls).To(Equal([]string{"v2", "copy"}))
		})
	})
	Describe("a panicking route", func() {
		var m *opfakes.FakeMetrics
		BeforeEach(func() {
			m = &opfakes.FakeMetrics{}
			env := testEnv(store)
			env.Metrics = m
			h = s3.NewHandler(env, s3.AnonymousOnly{}, testConfig(s3.Config{}))
		})
		It("is answered 500 when nothing was written, observed as 500 and usage-logged", func() {
			h.Register("list_buckets", func(context.Context, http.ResponseWriter, *op.Request) error {
				panic("boom")
			})
			rec := get("/")
			Expect(rec.Code).To(Equal(500))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InternalError</Code>"))
			name, status, _, _, _ := m.ObserveArgsForCall(0)
			Expect([]any{name, status}).To(Equal([]any{"list_buckets", 500}))
			Expect(store.Usage()).To(Equal([]op.UsageEntry{{
				Owner: op.Anonymous().Owner, Time: time.Unix(0x68d7a1b2, 0), Category: "list_buckets",
				BytesSent: uint64(rec.Body.Len()), Ops: 1,
			}}))
		})
		It("is answered 500 when it comes before the bucket is named, with no bucket in the document", func() {
			env := testEnv(store)
			env.Metrics = m
			boom := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				panic("boom")
			})
			rec := serveReq(s3.NewHandler(env, boom, testConfig(s3.Config{})), http.MethodGet, "/plain", nil)
			Expect(rec.Code).To(Equal(500))
			Expect(rec.Body.String()).NotTo(ContainSubstring("<BucketName>"))
			name, status, _, _, _ := m.ObserveArgsForCall(0)
			Expect([]any{name, status}).To(Equal([]any{"list_bucket", 500}), "the route was dispatched")
		})
		It("is answered 500 when the parse panics, observed under the name for no op", func(ctx SpecContext) {
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/plain", nil)
			req.URL = nil // net/http never serves such a request; its parse dereferences the URL
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(500))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InternalError</Code>"))
			Expect(rec.Header().Get("x-amz-request-id")).To(MatchRegexp(`^tx[0-9a-f]{21}-0068d7a1b2-4155-z$`))
			name, status, _, _, _ := m.ObserveArgsForCall(0)
			Expect([]any{name, status}).To(Equal([]any{"unknown", 500}))
			Expect(store.Usage()).To(BeEmpty(), "LogUsage drops a request with no identity, as radosgw's flush does")
		})
		It("aborts the connection when the response is under way", func(ctx SpecContext) {
			h.Register("list_buckets", func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
				w.WriteHeader(200)
				if _, err := w.Write([]byte("partial")); err != nil {
					return err
				}
				if err := http.NewResponseController(w).Flush(); err != nil {
					return err
				}
				panic("boom")
			})
			srv := httptest.NewServer(h)
			DeferCleanup(srv.Close)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(resp.Body.Close)
			Expect(resp.StatusCode).To(Equal(200))
			b, err := io.ReadAll(resp.Body)
			Expect(err).To(MatchError(io.ErrUnexpectedEOF), "a truncated chunked body, never a complete-looking one")
			Expect(string(b)).To(Equal("partial"))
			Eventually(m.ObserveCallCount).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(1))
			_, status, _, _, _ := m.ObserveArgsForCall(0)
			Expect(status).To(Equal(200))
		})
	})
	It("counts a route that writes nothing as the 200 net/http sends", func() {
		m := &opfakes.FakeMetrics{}
		env := testEnv(store)
		env.Metrics = m
		h = s3.NewHandler(env, s3.AnonymousOnly{}, testConfig(s3.Config{}))
		h.Register("list_buckets", func(context.Context, http.ResponseWriter, *op.Request) error { return nil })
		Expect(get("/").Code).To(Equal(200))
		_, status, _, _, _ := m.ObserveArgsForCall(0)
		Expect(status).To(Equal(200), "radosgw's status defaults to 200, and LogUsage counts 0 as a failure")
		Expect(store.Usage()).To(ConsistOf(HaveField("SuccessfulOps", BeEquivalentTo(1))))
	})
	It("writes no error document over a response a failing route has started", func() {
		h.Register("list_buckets", func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
			w.WriteHeader(200)
			if _, err := w.Write([]byte("ok")); err != nil {
				return err
			}
			return op.ErrInternalError
		})
		rec := get("/")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.String()).To(Equal("ok"))
	})
	Describe("the error log", func() {
		It("answers a refused authentication with its error document and logs none of the credentials its error carries", func() {
			buf := captureLog()
			const (
				keyID = "AKIAEXAMPLEKEYID0001"
				sig   = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
			)
			authz := "AWS4-HMAC-SHA256 Credential=" + keyID + "/20250927/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=" + sig
			as := s3.AuthenticatorFunc(func(_ context.Context, r *http.Request, _ op.PayloadForms) (*op.AuthResult, error) {
				return nil, fmt.Errorf("%w: key index read failed for %s", op.ErrInternalError, r.Header.Get("Authorization"))
			})
			h = newHandler(store, as, s3.Config{})
			rec := get("/plain/k", "Authorization", authz)
			Expect(rec.Code).To(Equal(500))
			Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InternalError</Code><Message></Message>` +
				`<RequestId>` + rec.Header().Get("x-amz-request-id") + `</RequestId><HostId>4155-z-zg</HostId></Error>`))
			Expect(buf.String()).NotTo(ContainSubstring(keyID))
			Expect(buf.String()).NotTo(ContainSubstring(sig))
		})
		DescribeTable("logs a route's server-side failure with the request id, its code and its cause",
			func(sentinel *op.Error) {
				buf := captureLog()
				h.Register("list_buckets", func(context.Context, http.ResponseWriter, *op.Request) error {
					return fmt.Errorf("%w: reading the bucket list: rados: timed out", sentinel)
				})
				rec := get("/")
				Expect(rec.Code).To(Equal(sentinel.Status))
				Expect(logRecords(buf)).To(ContainElement(SatisfyAll(
					HaveKeyWithValue("level", "ERROR"),
					HaveKeyWithValue("request_id", rec.Header().Get("x-amz-request-id")),
					HaveKeyWithValue("code", sentinel.Code),
					HaveKeyWithValue("error", ContainSubstring("rados: timed out")),
				)))
			},
			Entry("an InternalError", op.ErrInternalError),
			Entry("an UnknownError", op.ErrUnknown),
		)
		It("does not log a route's client error", func() {
			buf := captureLog()
			h.Register("list_buckets", func(context.Context, http.ResponseWriter, *op.Request) error {
				return op.ErrNoSuchBucket
			})
			Expect(get("/").Code).To(Equal(404))
			Expect(logRecords(buf)).To(BeEmpty())
		})
	})
	Describe("an authentication's error", func() {
		const (
			keyID  = "AKIAEXAMPLEKEYID0002"
			secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
			sig    = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
			authz  = "AWS4-HMAC-SHA256 Credential=" + keyID + "/20250927/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=" + sig

			presigned = "/plain/k?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=" + keyID +
				"%2F20250927%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-SignedHeaders=host&X-Amz-Signature=" + sig
		)
		// failing is an authenticator whose error carries everything the
		// request's credentials hold, the secret its store read included.
		failing := func(sentinel *op.Error) s3.Authenticator {
			return s3.AuthenticatorFunc(func(_ context.Context, r *http.Request, _ op.PayloadForms) (*op.AuthResult, error) {
				return nil, fmt.Errorf("%w: reading users.keys/%s with secret %s for %q?%s",
					sentinel, keyID, secret, r.Header.Get("Authorization"), r.URL.RawQuery)
			})
		}
		DescribeTable("logs a server error once, with the request id and its code and none of the request's credentials",
			func(sentinel *op.Error, target string, hdr ...string) {
				buf := captureLog()
				h = newHandler(store, failing(sentinel), s3.Config{})
				rec := get(target, hdr...)
				Expect(rec.Code).To(Equal(sentinel.Status))
				Expect(rec.Body.String()).To(ContainSubstring("<Code>" + sentinel.Code + "</Code>"))
				Expect(logRecords(buf)).To(ConsistOf(SatisfyAll(
					HaveKeyWithValue("level", "ERROR"),
					HaveKeyWithValue("request_id", rec.Header().Get("x-amz-request-id")),
					HaveKeyWithValue("code", sentinel.Code),
				)))
				Expect(buf.String()).NotTo(ContainSubstring(keyID), "the access key")
				Expect(buf.String()).NotTo(ContainSubstring(secret), "the secret")
				Expect(buf.String()).NotTo(ContainSubstring(sig), "the signature")
			},
			Entry("a store failure, its credentials in the Authorization header", op.ErrServiceUnavailable, "/plain/k", "Authorization", authz),
			Entry("a store failure, its credentials in a presigned query", op.ErrServiceUnavailable, presigned),
			Entry("an InternalError", op.ErrInternalError, "/plain/k", "Authorization", authz),
		)
		It("logs an authenticator-built code as InternalError, taking no text from it", func() {
			buf := captureLog()
			h = newHandler(store, s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				return nil, &op.Error{Code: keyID, Status: 500}
			}), s3.Config{})
			Expect(get("/plain/k", "Authorization", authz).Code).To(Equal(500))
			Expect(logRecords(buf)).To(ConsistOf(SatisfyAll(
				HaveKeyWithValue("level", "ERROR"),
				HaveKeyWithValue("code", "InternalError"),
			)))
			Expect(buf.String()).NotTo(ContainSubstring(keyID), "the access key")
		})
		It("logs nothing for a refusal", func() {
			buf := captureLog()
			h = newHandler(store, failing(op.ErrSignatureDoesNotMatch), s3.Config{})
			Expect(get("/plain/k", "Authorization", authz).Code).To(Equal(403))
			Expect(logRecords(buf)).To(BeEmpty())
		})
	})
	Describe("the request id", func() {
		It("is on every line logged while a request is served: the authenticator's, the op layer's and the handler's", func(ctx SpecContext) {
			buf := captureLogAt(slog.LevelDebug)
			alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
			// A bucket without an ACL makes the op layer warn as it builds one.
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(alice.Info.UserID)})
			Expect(err).NotTo(HaveOccurred())
			as := s3.AuthenticatorFunc(func(actx context.Context, r *http.Request, p op.PayloadForms) (*op.AuthResult, error) {
				slog.InfoContext(actx, "authenticating")
				return authAs(alice).Authenticate(actx, r, p)
			})
			h = newHandler(store, as, s3.Config{})
			rec := get("/plain?acl")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			recs := logRecords(buf)
			Expect(recs).To(ContainElements(
				HaveKeyWithValue("msg", "authenticating"),
				HaveKeyWithValue("msg", "couldn't find acl header for bucket, generating default"),
				HaveKeyWithValue("msg", "request done"),
			))
			for _, r := range recs {
				Expect(r).To(HaveKeyWithValue("request_id", rec.Header().Get("x-amz-request-id")), "line %q", r["msg"])
			}
			Expect(strings.Count(buf.String(), `"request_id"`)).To(Equal(len(recs)), "one request_id a line")
		})
		DescribeTable("is on every line of a request that fails, once",
			func(route s3.HandlerFunc, method, msg string) {
				buf := captureLogAt(slog.LevelDebug)
				if route != nil {
					h.Register("list_buckets", route)
				}
				rec := serveReq(h, method, "/", nil)
				recs := logRecords(buf)
				Expect(recs).To(ContainElement(HaveKeyWithValue("msg", msg)))
				for _, r := range recs {
					Expect(r).To(HaveKeyWithValue("request_id", rec.Header().Get("x-amz-request-id")), "line %q", r["msg"])
				}
				Expect(strings.Count(buf.String(), `"request_id"`)).To(Equal(len(recs)), "one request_id a line")
			},
			Entry("refused before dispatch", nil, "PATCH", "request done"),
			Entry("a route's InternalError", s3.HandlerFunc(func(context.Context, http.ResponseWriter, *op.Request) error {
				return fmt.Errorf("%w: rados: timed out", op.ErrInternalError)
			}), http.MethodGet, "request failed"),
			Entry("a route that panics", s3.HandlerFunc(func(context.Context, http.ResponseWriter, *op.Request) error {
				panic("boom")
			}), http.MethodGet, "request handler panicked"),
			Entry("a route that fails after its response started", s3.HandlerFunc(func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
				w.WriteHeader(200)
				return op.ErrInternalError
			}), http.MethodGet, "route failed after its response started"),
		)
	})
	Describe("a context error", func() {
		DescribeTable("is the 408 RequestTimeout to a client still there, and not logged as a server error",
			func(cause error) {
				buf := captureLog()
				h.Register("list_buckets", func(context.Context, http.ResponseWriter, *op.Request) error {
					return fmt.Errorf("listing buckets: %w", cause)
				})
				rec := get("/")
				Expect(rec.Code).To(Equal(408))
				Expect(rec.Body.String()).To(ContainSubstring("<Code>RequestTimeout</Code>"))
				Expect(logRecords(buf)).To(BeEmpty())
			},
			Entry("an expired deadline", context.DeadlineExceeded),
			Entry("a canceled context other than the request's", context.Canceled),
		)
		// serveGone serves GET / through a handler whose authenticator is as and
		// whose list_buckets route is route, both handed a cancel that stands
		// in for net/http's on a connection that is gone. It returns the
		// recorder, the metrics and the log, at debug.
		serveGone := func(ctx context.Context, as func(cancel func()) s3.Authenticator, route func(cancel func()) s3.HandlerFunc) (*httptest.ResponseRecorder, *opfakes.FakeMetrics, *bytes.Buffer) {
			GinkgoHelper()
			buf := captureLogAt(slog.LevelDebug)
			m := &opfakes.FakeMetrics{}
			env := testEnv(store)
			env.Metrics = m
			rctx, cancel := context.WithCancel(ctx)
			DeferCleanup(cancel)
			gone := s3.NewHandler(env, as(cancel), testConfig(s3.Config{}))
			if route != nil {
				gone.Register("list_buckets", route(cancel))
			}
			rec := httptest.NewRecorder()
			Expect(func() { gone.ServeHTTP(rec, httptest.NewRequestWithContext(rctx, http.MethodGet, "/", nil)) }).
				To(PanicWith(http.ErrAbortHandler), "net/http then sends nothing of its own")
			Expect(rec.Header()).NotTo(HaveKey("Content-Type"), "no error document")
			return rec, m, buf
		}
		goneLine := func(rec *httptest.ResponseRecorder, buf *bytes.Buffer, code string) {
			GinkgoHelper()
			recs := logRecords(buf)
			Expect(recs).To(ContainElement(SatisfyAll(
				HaveKeyWithValue("level", "DEBUG"),
				HaveKeyWithValue("msg", "client went away"),
				HaveKeyWithValue("request_id", rec.Header().Get("x-amz-request-id")),
				HaveKeyWithValue("code", code),
			)))
			Expect(recs).NotTo(ContainElement(HaveKeyWithValue("level", Not(Equal("DEBUG")))))
		}
		DescribeTable("writes nothing more to a client that went away, ends the connection and logs it at debug",
			func(ctx SpecContext, started bool, status int, body string, successful uint64) {
				alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
				rec, m, buf := serveGone(ctx, func(func()) s3.Authenticator { return authAs(alice) },
					func(cancel func()) s3.HandlerFunc {
						return func(ctx context.Context, w http.ResponseWriter, _ *op.Request) error {
							if started {
								w.WriteHeader(200)
								if _, err := w.Write([]byte(body)); err != nil {
									return err
								}
							}
							cancel()
							return fmt.Errorf("listing buckets: %w", ctx.Err())
						}
					})
				Expect(rec.Body.String()).To(Equal(body))
				Expect(m.ObserveCallCount()).To(Equal(1))
				route, observed, _, _, _ := m.ObserveArgsForCall(0)
				Expect(route).To(Equal("list_buckets"))
				Expect(observed).To(Equal(status))
				Expect(store.Usage()).To(Equal([]op.UsageEntry{{
					Owner: meta.UserOwner(alice.Info.UserID), Time: time.Unix(0x68d7a1b2, 0), Category: "list_buckets",
					BytesSent: uint64(len(body)), Ops: 1, SuccessfulOps: successful,
				}}), "the usage log files the request under the status it was observed with")
				goneLine(rec, buf, "RequestTimeout")
			},
			Entry("before anything was written: observed and usage-logged as the 408 it would have been", false, 408, "", uint64(0)),
			Entry("after the response started: observed and usage-logged as what was sent", true, 200, "<partial", uint64(1)),
		)
		DescribeTable("logs a gone client's end by its code alone, naming nothing the lookup it cut short read",
			func(ctx SpecContext, secret string) {
				rec, _, buf := serveGone(ctx, func(func()) s3.Authenticator { return s3.AnonymousOnly{} },
					func(cancel func()) s3.HandlerFunc {
						return func(ctx context.Context, _ http.ResponseWriter, _ *op.Request) error {
							cancel()
							// A store's error names the index object it read.
							return op.FromRADOS(fmt.Errorf("reading default.rgw.meta/%s: %w", secret, ctx.Err()), op.ScopeUser)
						}
					})
				goneLine(rec, buf, "RequestTimeout")
				Expect(buf.String()).NotTo(ContainSubstring(secret))
			},
			Entry("a lookup by access key", "AKIDEXAMPLE"),
			Entry("a lookup by email", "alice@example.com"),
		)
		It("ends a request whose client went away during authentication, logging it at debug and answering nothing", func(ctx SpecContext) {
			rec, m, buf := serveGone(ctx, func(cancel func()) s3.Authenticator {
				return s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
					cancel()
					return nil, fmt.Errorf("%w: the request's access key does not load: %w", op.ErrInvalidAccessKeyID, context.Canceled)
				})
			}, nil)
			Expect(rec.Body.Len()).To(BeZero())
			Expect(m.ObserveCallCount()).To(Equal(1))
			_, observed, _, _, _ := m.ObserveArgsForCall(0)
			Expect(observed).To(Equal(403))
			goneLine(rec, buf, "InvalidAccessKeyId")
		})
	})
	Describe("usage logging", func() {
		It("logs a successful ListBuckets once, with its final status and bytes", func() {
			alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
			h = newHandler(store, authAs(alice), s3.Config{})
			rec := get("/")
			Expect(rec.Code).To(Equal(200))
			Expect(store.Usage()).To(Equal([]op.UsageEntry{{
				Owner: meta.UserOwner(alice.Info.UserID), Bucket: "", Time: time.Unix(0x68d7a1b2, 0), Category: "list_buckets",
				BytesSent: uint64(rec.Body.Len()), Ops: 1, SuccessfulOps: 1,
			}}))
		})
		It("logs a refused request as LogUsage decides", func() {
			alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, Suspended: 1, OpMask: op.OpTypeAll})
			h = newHandler(store, authAs(alice), s3.Config{})
			rec := get("/")
			Expect(rec.Code).To(Equal(403))
			Expect(store.Usage()).To(Equal([]op.UsageEntry{{
				Owner: meta.UserOwner(alice.Info.UserID), Time: time.Unix(0x68d7a1b2, 0), Category: "list_buckets",
				BytesSent: uint64(rec.Body.Len()), Ops: 1,
			}}), "rgw_log_op runs in process_request's tail for a refused request too")
			store = memstore.New(memstore.Config{})
			h = newHandler(store, s3.AnonymousOnly{}, s3.Config{})
			Expect(serveReq(h, http.MethodPost, "/", nil).Code).To(Equal(405))
			Expect(store.Usage()).To(BeEmpty(), "no identity, so radosgw's flush drops the entry")
		})
		It("files a route under its radosgw op name", func() {
			alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
			h = newHandler(store, authAs(alice), s3.Config{})
			Expect(get("/?usage").Code).To(Equal(501))
			Expect(store.Usage()).To(ConsistOf(HaveField("Category", "get_self_usage")))
		})
	})
})

// countingBody counts what is read from it.
type countingBody struct {
	r io.Reader
	n int
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += n
	return n, err
}

var _ = Describe("response writer", func() {
	var store *memstore.Store
	BeforeEach(func() { store = memstore.New(memstore.Config{}) })
	serve := func(fn s3.HandlerFunc) *httptest.ResponseRecorder {
		h := newHandler(store, s3.AnonymousOnly{}, s3.Config{})
		h.Register("list_buckets", fn)
		return serveReq(h, http.MethodGet, "/", nil)
	}
	It("adds no Accept-Ranges to a Content-Length the route set itself", func() {
		rec := serve(func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(200)
			_, err := w.Write([]byte("ok"))
			return err
		})
		Expect(rec.Header().Get("Content-Length")).To(Equal("2"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "only dump_content_length adds it, rgw_rest.cc:388-397")
	})
	It("sets a length the op names together with Accept-Ranges, as dump_content_length does", func() {
		h := http.Header{}
		s3.SetContentLength(h, 1024)
		Expect(h).To(Equal(http.Header{"Content-Length": {"1024"}, "Accept-Ranges": {"bytes"}}))
	})
	It("renders a WriteXML success with its length, no Accept-Ranges and the formatter's escaping", func() {
		type result struct {
			XMLName xml.Name     `xml:"Result"`
			Name    xmltext.Text `xml:"Name"`
		}
		rec := serve(func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
			s3.WriteXML(w, r, 200, result{Name: `a&b "c" 'd' <e>`})
			return nil
		})
		body := `<?xml version="1.0" encoding="UTF-8"?><Result><Name>a&amp;b &quot;c&quot; &apos;d&apos; &lt;e&gt;</Name></Result>`
		Expect(rec.Body.String()).To(Equal(body))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(body))))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "end_header named no length, so the frontend's buffering filter adds this one without it")
	})
	It("escapes an error message as dump_string does and sends the length with Accept-Ranges", func() {
		rec := serve(func(context.Context, http.ResponseWriter, *op.Request) error {
			return op.ErrInvalidArgument.WithMessage(`bad "x" & 'y'`)
		})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring(`<Message>bad &quot;x&quot; &amp; &apos;y&apos;</Message>`))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"))
	})
	It("gives a streaming op a sink that merges its headers before the status", func() {
		rec := serve(func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
			w.Header().Set("x-amz-request-id", "tx1")
			sink := s3.SinkOfForTest(w)
			h := http.Header{"Content-Range": {"bytes 0-1/4"}}
			s3.SetContentLength(h, 2)
			sink.WriteHeader(206, h)
			if _, err := sink.Write([]byte("ab")); err != nil {
				return err
			}
			return sink.Flush()
		})
		Expect(rec.Code).To(Equal(206))
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 0-1/4"))
		Expect(rec.Header().Get("x-amz-request-id")).To(Equal("tx1"), "headers the route set first stay")
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "the op's header map reaches the response whole")
		Expect(rec.Body.String()).To(Equal("ab"))
		Expect(rec.Flushed).To(BeTrue(), "Flush reaches the connection through the wrapper")
	})
	Describe("x-amz-request-charged", func() {
		var alice, bob *op.UserRecord
		BeforeEach(func() {
			alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
			bob = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, OpMask: op.OpTypeAll})
		})
		// statBucket serves HEAD /plain as user with a requester-pays bucket
		// that alice owns loaded, answering with fail when it is set.
		statBucket := func(user *op.UserRecord, fail error) *httptest.ResponseRecorder {
			h := newHandler(store, authAs(user), s3.Config{})
			h.Register("stat_bucket", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
				r.BucketRec = &op.BucketRecord{Info: meta.BucketInfo{Owner: meta.UserOwner(alice.Info.UserID), RequesterPays: true}}
				if fail != nil {
					return fail
				}
				s3.SetCommonHeaders(w, r)
				w.WriteHeader(http.StatusOK)
				return nil
			})
			return serveReq(h, http.MethodHead, "/plain", nil)
		}
		It("is sent to a requester that does not own the requester-pays bucket", func() {
			Expect(statBucket(bob, nil).Header().Get("x-amz-request-charged")).To(Equal("requester"))
		})
		It("is not sent to the bucket's owner", func() {
			Expect(statBucket(alice, nil).Header()).NotTo(HaveKey("X-Amz-Request-Charged"))
		})
		It("is not sent with an error, which end_header sends only when !is_err()", func() {
			rec := statBucket(bob, op.ErrAccessDenied)
			Expect(rec.Code).To(Equal(403))
			Expect(rec.Header()).NotTo(HaveKey("X-Amz-Request-Charged"))
		})
	})
	Describe("a document sent whole in one chunk", func() {
		type doc struct {
			XMLName xml.Name     `xml:"Doc"`
			Name    xmltext.Text `xml:"Name"`
		}
		var srv *httptest.Server
		BeforeEach(func() {
			h := newHandler(store, s3.AnonymousOnly{}, s3.Config{})
			h.Register("list_buckets", func(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
				s3.WriteChunkedXMLForTest(ctx, w, r, doc{Name: "a<b"})
				return nil
			})
			srv = httptest.NewServer(h)
			DeferCleanup(srv.Close)
		})
		It("goes out chunked, with neither Content-Length nor Accept-Ranges", func(ctx SpecContext) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(resp.Body.Close)
			Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}))
			Expect(resp.ContentLength).To(BeEquivalentTo(-1))
			Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty())
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"))
			Expect(resp.Header.Get("x-amz-request-id")).NotTo(BeEmpty())
			b, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Doc><Name>a&lt;b</Name></Doc>`))
		})
		It("answers a HEAD with Content-Length 0 and no body", func(ctx SpecContext) {
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, srv.URL+"/", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(resp.Body.Close)
			Expect(resp.TransferEncoding).To(BeEmpty())
			Expect(resp.Header.Get("Content-Length")).To(Equal("0"))
			Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty())
		})
	})
})

// errReader fails every read with err.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

var _ = Describe("AnonymousOnly", func() {
	It("authenticates an unsigned request as anonymous, its payload unsigned", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/?versionId=x", errReader{errors.New("unread")})
		res, err := s3.AnonymousOnly{}.Authenticate(ctx, req, op.PayloadSigned)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity).To(Equal(op.Anonymous()))
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
		Expect(res.Body).To(BeNil(), "the request keeps its own body")
	})
	It("refuses credentials however the query spells their names", func(ctx SpecContext) {
		for _, target := range []string{"/?X-AMZ-CREDENTIAL=a", "/?awsaccesskeyid=a", "/?signature=a"} {
			_, err := s3.AnonymousOnly{}.Authenticate(ctx, httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil), 0)
			Expect(err).To(MatchError(op.ErrNotImplemented), target)
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", bytes.NewReader(nil))
		req.Header.Set("Authorization", "")
		_, err := s3.AnonymousOnly{}.Authenticate(ctx, req, 0)
		Expect(err).To(MatchError(op.ErrNotImplemented), "an empty Authorization header")
	})
})
