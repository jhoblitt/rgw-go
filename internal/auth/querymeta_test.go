package auth_test

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"encoding/base64"
	"encoding/xml"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// presignedV2 is a presigned SigV2 request for method and path carrying
// query, without its leading '?', and hdr, signed for key with secret over
// sts until expires. The caller spells sts out: it names the x-amz- lines
// in the order the string to sign writes them, the headers' and then the
// query's.
func presignedV2(ctx context.Context, method, path, query string, hdr map[string]string, key, secret, expires, sts string) *http.Request {
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(sts))
	target := "http://s3.example.com" + path + "?"
	if query != "" {
		target += query + "&"
	}
	target += "AWSAccessKeyId=" + key + "&Expires=" + expires + "&Signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(h.Sum(nil)))
	var body io.Reader
	if method == http.MethodPut {
		body = strings.NewReader("hello")
	}
	req := httptest.NewRequestWithContext(ctx, method, target, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req
}

// aliceV2 is presignedV2 for alice, valid at fixedNow.
func aliceV2(ctx context.Context, method, path, query string, hdr map[string]string, sts string) *http.Request {
	return presignedV2(ctx, method, path, query, hdr, accessKeyOf("alice"), secretOf("alice"), v2Expires, sts)
}

// userMeta is the user metadata of attrs, by x-amz- name, its trailing NUL
// cut.
func userMeta(attrs map[string][]byte) map[string]string {
	out := map[string]string{}
	for k, v := range attrs {
		if name, ok := strings.CutPrefix(k, meta.AttrPrefix); ok && strings.HasPrefix(name, "x-amz-meta-") {
			out[name] = strings.TrimSuffix(string(v), "\x00")
		}
	}
	return out
}

// signedAB is the metadata every form below signs, as stored.
var signedAB = map[string]string{"x-amz-meta-a": "1", "x-amz-meta-b": "2"}

var _ = Describe("A presigned SigV2 request's signed meta and session token, wherever its header/query split falls", func() {
	var (
		store *memstore.Store
		v     *auth.Verifier
		h     *s3.Handler
	)

	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		alice := store.AddUser(user("alice", nil))
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
		env := &op.Env{Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		h = s3.NewHandler(env, v, s3.Config{})
	})

	serve := func(req *http.Request) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	stored := func(ctx context.Context, key string) map[string]string {
		rec, err := store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: key})
		Expect(err).NotTo(HaveOccurred(), "object %s", key)
		return userMeta(st.Attrs)
	}

	// One signature serves all three forms: the meta lines end the headers'
	// run and start the query's alike.
	DescribeTable("stores the same attrs on PutObject",
		func(ctx SpecContext, query string, hdr map[string]string) {
			code, body := serve(aliceV2(ctx, http.MethodPut, "/b/k", query, hdr,
				"PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-b:2\n/b/k"))
			Expect(code).To(Equal(http.StatusOK), body)
			Expect(stored(ctx, "k")).To(Equal(signedAB))
		},
		Entry("with both names in the headers", "", map[string]string{"X-Amz-Meta-A": "1", "X-Amz-Meta-B": "2"}),
		Entry("with both in the query, read once", "x-amz-meta-a=1&x-amz-meta-b=2", nil),
		Entry("with the last header moved into the query", "x-amz-meta-b=2", map[string]string{"X-Amz-Meta-A": "1"}),
	)

	Describe("CopyObject", func() {
		BeforeEach(func(ctx SpecContext) {
			code, body := serve(aliceV2(ctx, http.MethodPut, "/b/src", "", map[string]string{"X-Amz-Meta-S": "src"},
				"PUT\n\n\n"+v2Expires+"\nx-amz-meta-s:src\n/b/src"))
			Expect(code).To(Equal(http.StatusOK), body)
		})

		// x-amz-metadata-directive sorts after every x-amz-meta- name, so
		// the forms sign different strings.
		DescribeTable("stores the same attrs under REPLACE",
			func(ctx SpecContext, query string, hdr map[string]string, sts string) {
				all := map[string]string{"X-Amz-Copy-Source": "b/src", "X-Amz-Metadata-Directive": "REPLACE"}
				maps.Copy(all, hdr)
				code, body := serve(aliceV2(ctx, http.MethodPut, "/b/k", query, all, "PUT\n\n\n"+v2Expires+"\n"+sts+"/b/k"))
				Expect(code).To(Equal(http.StatusOK), body)
				Expect(body).To(ContainSubstring("<CopyObjectResult"))
				Expect(stored(ctx, "k")).To(Equal(signedAB))
			},
			Entry("with the meta in the headers", "", map[string]string{"X-Amz-Meta-A": "1", "X-Amz-Meta-B": "2"},
				"x-amz-copy-source:b/src\nx-amz-meta-a:1\nx-amz-meta-b:2\nx-amz-metadata-directive:REPLACE\n"),
			Entry("with the meta in the query", "x-amz-meta-a=1&x-amz-meta-b=2", nil,
				"x-amz-copy-source:b/src\nx-amz-metadata-directive:REPLACE\nx-amz-meta-a:1\nx-amz-meta-b:2\n"),
			Entry("with the meta split", "x-amz-meta-b=2", map[string]string{"X-Amz-Meta-A": "1"},
				"x-amz-copy-source:b/src\nx-amz-meta-a:1\nx-amz-metadata-directive:REPLACE\nx-amz-meta-b:2\n"),
		)

		It("stores the same attrs under COPY, whichever side carries the meta", func(ctx SpecContext) {
			sts := "PUT\n\n\n" + v2Expires + "\nx-amz-copy-source:b/src\nx-amz-meta-a:1\nx-amz-meta-b:2\n/b/"
			forms := []struct {
				key, query string
				hdr        map[string]string
			}{
				{"header", "", map[string]string{"X-Amz-Meta-A": "1", "X-Amz-Meta-B": "2"}},
				{"query", "x-amz-meta-a=1&x-amz-meta-b=2", nil},
				{"split", "x-amz-meta-b=2", map[string]string{"X-Amz-Meta-A": "1"}},
			}
			for _, f := range forms {
				all := map[string]string{"X-Amz-Copy-Source": "b/src"}
				maps.Copy(all, f.hdr)
				code, body := serve(aliceV2(ctx, http.MethodPut, "/b/"+f.key, f.query, all, sts+f.key))
				Expect(code).To(Equal(http.StatusOK), "%s: %s", f.key, body)
			}
			want := stored(ctx, "header")
			Expect(want).To(HaveKeyWithValue("x-amz-meta-s", "src"), "COPY keeps the source's meta")
			Expect(stored(ctx, "query")).To(Equal(want), "the query form")
			Expect(stored(ctx, "split")).To(Equal(want), "the split form")
		})
	})

	DescribeTable("gives CreateMultipartUpload's upload the same attrs",
		func(ctx SpecContext, query string, hdr map[string]string) {
			code, body := serve(aliceV2(ctx, http.MethodPost, "/b/k", query, hdr,
				"POST\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-b:2\n/b/k?uploads"))
			Expect(code).To(Equal(http.StatusOK), body)
			var res struct {
				UploadID string `xml:"UploadId"`
			}
			Expect(xml.Unmarshal([]byte(body), &res)).To(Succeed())
			rec, err := store.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			up, err := store.GetUpload(ctx, rec, meta.ObjKey{Name: "k"}, res.UploadID)
			Expect(err).NotTo(HaveOccurred())
			Expect(userMeta(up.Attrs)).To(Equal(signedAB))
		},
		Entry("with the meta in the headers", "uploads", map[string]string{"X-Amz-Meta-A": "1", "X-Amz-Meta-B": "2"}),
		Entry("with the meta moved into the query", "uploads&x-amz-meta-a=1&x-amz-meta-b=2", nil),
		Entry("with the last header moved into the query", "uploads&x-amz-meta-b=2", map[string]string{"X-Amz-Meta-A": "1"}),
	)

	// The handler's own order: parse, authenticate, apply, then the
	// authorizer's environment.
	stsAuthentication := func(ctx context.Context, req *http.Request) (*op.AuthResult, *op.Request, []string) {
		p, err := s3.ParseRequest(req, s3.Config{}, fixedNow())
		Expect(err).NotTo(HaveOccurred())
		res, err := v.Authenticate(ctx, req, anyPayload)
		Expect(err).NotTo(HaveOccurred(), "%s", req.URL)
		s3.ApplyAuth(p.Req, res)
		env := authz.BuildEnv(authz.Config{Now: fixedNow}, p.Req, policy.S3GetObject)
		return res, p.Req, env.Lookup("sts:authentication")
	}

	DescribeTable("gives the same sts:authentication",
		func(ctx SpecContext, query string, hdr map[string]string, sts, want string) {
			_, _, got := stsAuthentication(ctx, aliceV2(ctx, http.MethodGet, "/b/k", query, hdr, "GET\n\n\n"+v2Expires+"\n"+sts+"/b/k"))
			Expect(got).To(Equal([]string{want}))
		},
		Entry("with the token in the headers", "", map[string]string{"X-Amz-Security-Token": "tok"}, "x-amz-security-token:tok\n", "true"),
		Entry("with the token moved into the query", "x-amz-security-token=tok", nil, "x-amz-security-token:tok\n", "true"),
		Entry("without a token", "", nil, "", "false"),
	)

	It("folds the query's signed meta and token into the headers, out of the query", func(ctx SpecContext) {
		req := aliceV2(ctx, http.MethodGet, "/b/k", "x-amz-meta-b=2&X-AMZ-META-B=3&x-amz-security-token=tok",
			map[string]string{"X-Amz-Meta-A": "1"},
			"GET\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-b:3,2\nx-amz-security-token:tok\n/b/k")
		res, r, _ := stsAuthentication(ctx, req)
		Expect(res.QueryMeta).To(Equal(map[string]string{"x-amz-meta-b": "3,2", "x-amz-security-token": "tok"}))
		Expect(r.Header).To(SatisfyAll(
			HaveKeyWithValue("X-Amz-Meta-A", []string{"1"}),
			HaveKeyWithValue("X-Amz-Meta-B", []string{"3,2"}),
			HaveKeyWithValue("X-Amz-Security-Token", []string{"tok"}),
		))
		Expect(r.Query).To(SatisfyAll(
			HaveKey("AWSAccessKeyId"), Not(HaveKey("x-amz-meta-b")), Not(HaveKey("X-AMZ-META-B")), Not(HaveKey("x-amz-security-token")),
		))
		Expect(req.Header).NotTo(HaveKey("X-Amz-Meta-B"), "the request the server holds keeps its own headers")
	})

	Describe("leaves unchanged", func() {
		It("a SigV2 header-signed request, whose query meta and token are not signed", func(ctx SpecContext) {
			req := newGet(ctx, "http://s3.example.com/b/k?x-amz-meta-a=q&x-amz-security-token=tok")
			req.Header.Set("Date", v2Date)
			req.Header.Set("X-Amz-Meta-A", "h")
			mac := hmac.New(sha1.New, []byte(secretOf("alice")))
			mac.Write([]byte("GET\n\n\n" + v2Date + "\nx-amz-meta-a:h\n/b/k"))
			req.Header.Set("Authorization", "AWS "+accessKeyOf("alice")+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			res, r, got := stsAuthentication(ctx, req)
			Expect(res.QueryMeta).To(BeEmpty())
			Expect(got).To(Equal([]string{"false"}), "the token is read from the header alone")
			Expect(r.Query).To(SatisfyAll(HaveKey("x-amz-meta-a"), HaveKey("x-amz-security-token")))
			Expect(r.Header.Values("X-Amz-Meta-A")).To(Equal([]string{"h"}))
		})

		It("a SigV4 presigned request, whose query names each parameter", func(ctx SpecContext) {
			req := newGet(ctx, "http://s3.example.com/b/k?x-amz-meta-a=q&X-Amz-Expires=300")
			creds := aws.Credentials{AccessKeyID: accessKeyOf("alice"), SecretAccessKey: secretOf("alice"), SessionToken: "tok"}
			signed, _, err := sdkSigner().PresignHTTP(ctx, creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", fixedNow())
			Expect(err).NotTo(HaveOccurred())
			Expect(signed).To(ContainSubstring("X-Amz-Security-Token=tok"))
			res, r, got := stsAuthentication(ctx, newGet(ctx, signed))
			Expect(res.QueryMeta).To(BeEmpty())
			Expect(got).To(Equal([]string{"false"}), "the token is read from the header alone")
			Expect(r.Query).To(SatisfyAll(HaveKey("x-amz-meta-a"), HaveKey("x-amz-security-token")))
			Expect(r.Header).NotTo(HaveKey("X-Amz-Meta-A"))
		})
	})

	// The refusal comes once the request has authenticated, so a request
	// radosgw refuses earlier gets radosgw's error.
	DescribeTable("refuses meta the headers and the query do not make one set of, with radosgw's error first",
		func(ctx SpecContext, build func(context.Context) *http.Request, want OmegaMatcher) {
			_, err := v.Authenticate(ctx, build(ctx), anyPayload)
			Expect(err).To(want)
		},
		Entry("a wrong signature", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a=2", map[string]string{"X-Amz-Meta-A": "1"},
				accessKeyOf("alice"), "not-the-secret", v2Expires, "PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-a:2\n/b/k")
		}, MatchError(op.ErrSignatureDoesNotMatch)),
		Entry("an unknown key", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a=2", map[string]string{"X-Amz-Meta-A": "1"},
				"AKNOBODY", secretOf("alice"), v2Expires, "PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-a:2\n/b/k")
		}, MatchError(op.ErrInvalidAccessKeyID)),
		Entry("an expired URL", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a=2", map[string]string{"X-Amz-Meta-A": "1"},
				accessKeyOf("alice"), secretOf("alice"), "1440000000", "PUT\n\n\n1440000000\nx-amz-meta-a:1\nx-amz-meta-a:2\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("expired")))),
		Entry("an empty token in the query, refused before the signature as radosgw refuses it", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodGet, "/b/k", "x-amz-security-token=", nil,
				accessKeyOf("alice"), "not-the-secret", v2Expires, "GET\n\n\n"+v2Expires+"\nx-amz-security-token:\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("empty security token")))),
		Entry("a meta name signed on both sides", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodPut, "/b/k", "X-Amz-Meta-A=2", map[string]string{"X-Amz-Meta-A": "1"},
				"PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-a:2\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("x-amz-meta-a is signed in both the headers and the query")))),
		Entry("the token signed on both sides", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "x-amz-security-token=q", map[string]string{"X-Amz-Security-Token": "h"},
				"GET\n\n\n"+v2Expires+"\nx-amz-security-token:h\nx-amz-security-token:q\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("x-amz-security-token is signed in both the headers and the query")))),
		Entry("an empty token in the headers, which authenticates", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "", map[string]string{"X-Amz-Security-Token": ""},
				"GET\n\n\n"+v2Expires+"\nx-amz-security-token:\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("empty security token")))),
		// authDataV2 refuses an empty token under the exact lower-case name
		// alone, as get_auth_data_v2's args.exists does, but the string to
		// sign and the fold take every case.
		Entry("an empty token under an upper-case query name, which authenticates", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "X-AMZ-SECURITY-TOKEN=", nil, "GET\n\n\n"+v2Expires+"\nx-amz-security-token:\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("empty security token in the query")))),
		Entry("an empty token under a mixed-case query name, which authenticates", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "x-Amz-security-token=", nil, "GET\n\n\n"+v2Expires+"\nx-amz-security-token:\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring("empty security token in the query")))),
		Entry("a line break in a query meta value with a wrong signature", func(ctx context.Context) *http.Request {
			return presignedV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a=1%0Ax-amz-meta-b:2", nil,
				accessKeyOf("alice"), "not-the-secret", v2Expires, "PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-b:2\n/b/k")
		}, MatchError(op.ErrSignatureDoesNotMatch)),
		// Each of these signs as the header lines of the comment's request.
		Entry("two signed meta headers packed into one query value", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a=1%0Ax-amz-meta-b:2", nil,
				"PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-b:2\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring(`"x-amz-meta-a" signs a line break`)))),
		Entry("a signed token header hidden in a query meta value", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "x-amz-meta-a=1%0Ax-amz-security-token:tok", nil,
				"GET\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-security-token:tok\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring(`"x-amz-meta-a" signs a line break`)))),
		Entry("two signed meta headers packed into one query name", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a:1%0Ax-amz-meta-b=2", nil,
				"PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-meta-b:2\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring(`"x-amz-meta-a:1\nx-amz-meta-b" signs a line break`)))),
		Entry("a CR in a query token", func(ctx context.Context) *http.Request {
			return aliceV2(ctx, http.MethodGet, "/b/k", "x-amz-security-token=t%0Dk", nil,
				"GET\n\n\n"+v2Expires+"\nx-amz-security-token:t\rk\n/b/k")
		}, SatisfyAll(MatchError(op.ErrAccessDenied), MatchError(ContainSubstring(`"x-amz-security-token" signs a line break`)))),
	)

	// The refusal relies on this: no header value reaches the string to sign
	// with a CR or LF in it.
	It("gets no CR or LF in a header value through net/http's server", func(ctx SpecContext) {
		seen := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- r.Header.Get("X-Amz-Meta-A")
		}))
		DeferCleanup(srv.Close)
		send := func(header string) string {
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
			Expect(err).NotTo(HaveOccurred())
			defer func() { Expect(conn.Close()).To(Succeed()) }()
			_, err = io.WriteString(conn, "GET /b/k HTTP/1.1\r\nHost: s3.example.com\r\n"+header+"Connection: close\r\n\r\n")
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Body.Close()).To(Succeed())
			return resp.Status
		}
		Expect(send("X-Amz-Meta-A: 1\r\n\tx-amz-meta-b:2\r\n")).To(Equal("200 OK"))
		Expect(seen).To(Receive(Equal("1 x-amz-meta-b:2")), "a continuation line joins with a space")
		Expect(send("X-Amz-Meta-A: 1\rx-amz-meta-b:2\r\n")).To(HavePrefix("400"), "a bare CR")
		Expect(seen).NotTo(Receive())
	})

	It("authenticates the same names on one side alone, and on both sides on the header route, which signs no query", func(ctx SpecContext) {
		for _, req := range []*http.Request{
			aliceV2(ctx, http.MethodPut, "/b/k", "", map[string]string{"X-Amz-Meta-A": "1", "X-Amz-Security-Token": "h"},
				"PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:1\nx-amz-security-token:h\n/b/k"),
			aliceV2(ctx, http.MethodPut, "/b/k", "x-amz-meta-a=2&x-amz-security-token=q", nil,
				"PUT\n\n\n"+v2Expires+"\nx-amz-meta-a:2\nx-amz-security-token:q\n/b/k"),
		} {
			_, err := v.Authenticate(ctx, req, anyPayload)
			Expect(err).NotTo(HaveOccurred(), "%s", req.URL)
		}
		req := newGet(ctx, "http://s3.example.com/b/k?x-amz-meta-a=2")
		req.Header.Set("Date", v2Date)
		req.Header.Set("X-Amz-Meta-A", "1")
		mac := hmac.New(sha1.New, []byte(secretOf("alice")))
		mac.Write([]byte("GET\n\n\n" + v2Date + "\nx-amz-meta-a:1\n/b/k"))
		req.Header.Set("Authorization", "AWS "+accessKeyOf("alice")+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		_, err := v.Authenticate(ctx, req, anyPayload)
		Expect(err).NotTo(HaveOccurred(), "the header route")
	})
})
