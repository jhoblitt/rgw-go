package s3_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

const appOrigin = "https://app.example"

// corsConfig is a rule for appOrigin that allows GET, HEAD, PUT, POST and
// DELETE, the x-amz-* headers, two expose headers and a max age, then a rule
// for every origin that allows GET alone.
func corsConfig() *cors.Configuration {
	return &cors.Configuration{Rules: []cors.Rule{
		{
			MaxAge: 600, Methods: cors.MethodGet | cors.MethodHead | cors.MethodPut | cors.MethodPost | cors.MethodDelete,
			AllowedOrigins: []string{appOrigin}, AllowedHeaders: []string{"x-amz-*"},
			ExposeHeaders: []string{"ETag", "x-amz-meta-color"},
		},
		{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}},
	}}
}

// corsNames are the headers dump_access_control writes.
var corsNames = []string{
	"Access-Control-Allow-Origin", "Vary", "Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers", "Access-Control-Expose-Headers", "Access-Control-Max-Age",
}

// corsOf is every header of h that dump_access_control writes, under the
// spelling h holds it, so a stray canonical or lower-case write shows.
func corsOf(h http.Header) map[string][]string {
	out := map[string][]string{}
	for k, v := range h {
		for _, n := range corsNames {
			if strings.EqualFold(k, n) {
				out[k] = v
			}
		}
	}
	return out
}

// appHeaders is what corsConfig's first rule answers appOrigin with for
// method on a request that carries an Authorization header.
func appHeaders(method string) map[string][]string {
	return map[string][]string{
		"Access-Control-Allow-Origin":   {appOrigin},
		"Vary":                          {"Origin"},
		"Access-Control-Allow-Methods":  {method},
		"Access-Control-Expose-Headers": {"ETag,x-amz-meta-color"},
		"Access-Control-Max-Age":        {"600"},
	}
}

// signedAuth is the Authorization header of a signed request; the specs'
// authenticators read none of it.
const signedAuth = "AWS4-HMAC-SHA256 Credential=AKALICE/20260927/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=00"

var _ = Describe("CORS headers on S3 responses", func() {
	var (
		w   *readWorld
		env *op.Env
	)
	BeforeEach(func(ctx SpecContext) {
		w = newReadWorld(ctx, denc.Squid)
		env = w.env(nil)
		Expect(w.store.PutBucketAttrs(ctx, w.plain, map[string][]byte{op.AttrCORS: corsConfig().Encode()}, nil)).To(Succeed())
	})
	as := func(who *op.UserRecord, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
		return send(w.handler(env, who), method, target, hdr)
	}
	app := func(extra ...string) map[string]string {
		hdr := map[string]string{"Origin": appOrigin, "Authorization": signedAuth}
		for i := 0; i+1 < len(extra); i += 2 {
			hdr[extra[i]] = extra[i+1]
		}
		return hdr
	}

	It("are sent with a GET object's 200 for the rule its Origin and method match", func() {
		rec := as(w.alice, http.MethodGet, "/plain/small", app())
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodGet)))
	})
	It("are sent with an error document once the bucket has loaded, a 404 and a refusal alike", func() {
		rec := as(w.alice, http.MethodGet, "/plain/nope", app())
		Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodGet)))

		rec = as(w.bob, http.MethodGet, "/plain/small", app())
		Expect(rec.Code).To(Equal(http.StatusForbidden), "abort_early passes the op to end_header, rgw_rest.cc:746 at both tags")
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodGet)))
	})
	It("are not sent when authentication fails, before any bucket loads", func() {
		refuse := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return nil, op.ErrSignatureDoesNotMatch
		})
		rec := send(s3.NewHandler(env, refuse, testConfig(s3.Config{})), http.MethodGet, "/plain/small", app())
		Expect(rec.Code).To(Equal(http.StatusForbidden))
		Expect(corsOf(rec.Header())).To(BeEmpty())
	})
	It("are not sent for a bucket that does not exist, nor for a request that names none", func() {
		rec := as(w.alice, http.MethodGet, "/nobucket/x", app())
		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(corsOf(rec.Header())).To(BeEmpty())
		rec = as(w.alice, http.MethodGet, "/", app())
		Expect(rec.Code).To(Equal(http.StatusOK), "ListBuckets")
		Expect(corsOf(rec.Header())).To(BeEmpty())
	})
	It("send * to a request without an Authorization header, a presigned one, on a rule that allows every origin", func() {
		const target = "/plain/small?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=00"
		rec := as(w.alice, http.MethodGet, target, map[string]string{"Origin": "https://other.example"})
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(corsOf(rec.Header())).To(Equal(map[string][]string{
			"Access-Control-Allow-Origin":  {"*"},
			"Access-Control-Allow-Methods": {"GET"},
		}), "no Vary: Origin with *, rgw_rest.cc:536-538 at v19.2.7")
		rec = as(w.alice, http.MethodGet, target, map[string]string{"Origin": "https://other.example", "Authorization": signedAuth})
		Expect(corsOf(rec.Header())).To(Equal(map[string][]string{
			"Access-Control-Allow-Origin":  {"https://other.example"},
			"Vary":                         {"Origin"},
			"Access-Control-Allow-Methods": {"GET"},
		}), "an Authorization header keeps the Origin as sent")
	})
	It("are not sent for a method no rule allows the Origin", func() {
		rec := serveReq(w.handler(env, w.alice), http.MethodPut, "/plain/new", strings.NewReader("data"),
			"Origin", "https://other.example", "Authorization", signedAuth)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(corsOf(rec.Header())).To(BeEmpty(), "the rule for every origin allows GET alone")
	})
	It("take the method from Access-Control-Request-Method when a request sends one", func() {
		rec := as(w.alice, http.MethodGet, "/plain/small", app("Access-Control-Request-Method", http.MethodPut))
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodPut)))
		rec = as(w.alice, http.MethodGet, "/plain/small", map[string]string{
			"Origin": "https://other.example", "Access-Control-Request-Method": http.MethodPut, "Authorization": signedAuth,
		})
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(corsOf(rec.Header())).To(BeEmpty(), "no rule allows the other origin PUT, whatever the request's own method")
	})
	It("allow the requested headers the rule covers, which choose no rule", func() {
		rec := as(w.alice, http.MethodGet, "/plain/small", app("Access-Control-Request-Headers", "X-Amz-Date, Content-Type"))
		want := appHeaders(http.MethodGet)
		want["Access-Control-Allow-Headers"] = []string{"X-Amz-Date"}
		Expect(corsOf(rec.Header())).To(Equal(want))

		rec = as(w.alice, http.MethodGet, "/plain/small", map[string]string{
			"Origin": "https://other.example", "Access-Control-Request-Headers": "x-amz-date",
		})
		Expect(corsOf(rec.Header())).To(Equal(map[string][]string{
			"Access-Control-Allow-Origin":  {"*"},
			"Access-Control-Allow-Methods": {"GET"},
		}), "a rule that allows none of the headers still answers an ordinary request")
	})
	It("allow none of more than 32 distinct requested headers, and still answer by the rule", func() {
		names := make([]string, cors.MaxRequestHeaders+1)
		for i := range names {
			names[i] = fmt.Sprintf("x-amz-meta-%02d", i)
		}
		rec := as(w.alice, http.MethodGet, "/plain/small", app("Access-Control-Request-Headers", strings.Join(names, ",")))
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodGet)), "every name is an x-amz-* the rule covers")

		rec = as(w.alice, http.MethodGet, "/plain/small", app("Access-Control-Request-Headers", strings.Join(names[:cors.MaxRequestHeaders], ",")))
		Expect(exactHeader(rec.Header(), "Access-Control-Allow-Headers")).To(Equal(strings.Join(names[:cors.MaxRequestHeaders], ",")))
	})
	It("are not sent without an Origin, nor for an empty one", func() {
		Expect(corsOf(as(w.alice, http.MethodGet, "/plain/small", map[string]string{"Authorization": signedAuth}).Header())).To(BeEmpty())
		Expect(corsOf(as(w.alice, http.MethodGet, "/plain/small", map[string]string{"Origin": ""}).Header())).To(BeEmpty())
	})
	It("are not sent for a configuration that does not decode, and leave the op's answer as it is", func(ctx SpecContext) {
		Expect(w.store.PutBucketAttrs(ctx, w.plain, map[string][]byte{op.AttrCORS: {0xff}}, nil)).To(Succeed())
		rec := as(w.alice, http.MethodGet, "/plain/small", app())
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.Bytes()).To(Equal(readPayload(1024)))
		Expect(corsOf(rec.Header())).To(BeEmpty())
	})
	It("are sent with PutBucketEncryption's and DeleteBucketEncryption's answers, which radosgw sends without them", func() {
		h := w.handler(env, w.alice)
		hdr := []string{"Origin", appOrigin, "Authorization", signedAuth}
		rec := serveReq(h, http.MethodPut, "/plain?encryption", strings.NewReader(aesEncryptionDoc), hdr...)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodPut)))

		rec = serveReq(h, http.MethodPut, "/plain?encryption", strings.NewReader("<ServerSideEncryptionConfiguration"), hdr...)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>MalformedXML</Code>"))
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodPut)), "radosgw's execute refusals go through send_response's end_header(s)")

		rec = serveReq(h, http.MethodDelete, "/plain?encryption", nil, hdr...)
		Expect(rec.Code).To(Equal(http.StatusNoContent))
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodDelete)))
	})
	It("are sent with DeleteObjects' refusal, which radosgw answers with its status line alone", func() {
		rec := serveReq(w.handler(env, w.alice), http.MethodPost, "/plain?delete", strings.NewReader("<Delete><Object><Key>small</Key>"),
			"Origin", appOrigin, "Authorization", signedAuth)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-request-id"), "send_status, rgw_rest_s3.cc:4277-4285 at v19.2.7")
		Expect(corsOf(rec.Header())).To(Equal(appHeaders(http.MethodPost)))
	})

	// The preflight's answer must not depend on who sent it: a signed
	// preflight is granted on its key's id alone.
	Describe("on an OPTIONS response", func() {
		DescribeTable("add neither x-amz-request-charged nor Access-Control headers, which a GET of the same requester-pays bucket gets",
			func(ctx context.Context, identity func() op.Identity, status int) {
				rec := &op.BucketRecord{
					Info:  meta.BucketInfo{Owner: meta.UserOwner(meta.UserID{ID: "bob"}), RequesterPays: true},
					Attrs: map[string][]byte{op.AttrCORS: corsConfig().Encode()},
				}
				answer := func(method string) *httptest.ResponseRecorder {
					r := &op.Request{
						ID: "tx1", Method: method, Bucket: "plain", Identity: identity(), BucketRec: rec, Env: &op.Env{HostID: "4155-z-zg"},
						Header: http.Header{"Origin": {appOrigin}, "Access-Control-Request-Method": {http.MethodGet}, "Authorization": {signedAuth}},
					}
					out := httptest.NewRecorder()
					if status == http.StatusOK {
						s3.SetCommonHeaders(out, r)
						out.WriteHeader(status)
					} else {
						s3.WriteError(ctx, out, r, op.ErrPermanentRedirect)
					}
					Expect(out.Code).To(Equal(status))
					return out
				}
				get := answer(http.MethodGet)
				Expect(exactHeader(get.Header(), "x-amz-request-charged")).To(Equal("requester"))
				Expect(corsOf(get.Header())).To(Equal(appHeaders(http.MethodGet)))

				opts := answer(http.MethodOptions)
				Expect(opts.Header()).NotTo(haveHeaderAnyCase("x-amz-request-charged"))
				Expect(corsOf(opts.Header())).To(BeEmpty())
				Expect(exactHeader(opts.Header(), "x-amz-request-id")).To(Equal("tx1"))
			},
			Entry("a 200, anonymous", op.Anonymous, http.StatusOK),
			Entry("a 200, granted on a key's id alone", unverifiedAlice, http.StatusOK),
			Entry("the 301 of another zonegroup's bucket, anonymous", op.Anonymous, http.StatusMovedPermanently),
			Entry("the 301 of another zonegroup's bucket, granted on a key's id alone", unverifiedAlice, http.StatusMovedPermanently),
		)
	})
})

// unverifiedAlice is the identity a forged preflight of alice's key is
// granted: her user, with no signature compared.
func unverifiedAlice() op.Identity {
	alice := meta.UserInfo{UserID: meta.UserID{ID: "alice"}}
	return op.Identity{User: &alice, Owner: meta.UserOwner(alice.UserID), OpMask: op.OpTypeAll, AccessKey: unverifiedKey, Unverified: true}
}

var _ = Describe("SetPreflightHeaders", func() {
	It("sends the Origin as sent on a rule that allows every origin, with the rule's other headers", func() {
		o := &op.OptionsCORS{
			Rule: &cors.Rule{
				MaxAge: 300, Methods: cors.MethodGet | cors.MethodPut, AllowedOrigins: []string{"*"},
				AllowedHeaders: []string{"x-amz-*"}, ExposeHeaders: []string{"ETag"},
			},
			Origin: "https://example.org", Method: http.MethodPut, Headers: []string{"X-Amz-Date", "x-amz-meta-a"},
		}
		rec := httptest.NewRecorder()
		s3.SetPreflightHeaders(rec, o)
		Expect(corsOf(rec.Header())).To(Equal(map[string][]string{
			"Access-Control-Allow-Origin":   {"https://example.org"},
			"Vary":                          {"Origin"},
			"Access-Control-Allow-Methods":  {"PUT"},
			"Access-Control-Allow-Headers":  {"X-Amz-Date,x-amz-meta-a"},
			"Access-Control-Expose-Headers": {"ETag"},
			"Access-Control-Max-Age":        {"300"},
		}), "RGWOptionsCORS_ObjStore_S3::send_response, rgw_rest_s3.cc:3872-3873 at v19.2.7")
	})
	It("sends nothing for an op that found no rule", func() {
		rec := httptest.NewRecorder()
		s3.SetPreflightHeaders(rec, &op.OptionsCORS{Origin: "https://example.org", Method: http.MethodGet})
		Expect(rec.Header()).To(BeEmpty())
	})
})

// A signed preflight is granted on its key's id alone, so whatever the
// handler adds to a preflight's answer must be the same for a forged one as
// for an anonymous one, or the answer tells the sender something of the
// key's user.
var _ = Describe("a forged preflight's answer", func() {
	// preflight stands in for the options_cors route: the op, then the two
	// header writers this file's specs pin, then 200 with no body.
	preflight := func(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
		o := &op.OptionsCORS{}
		if err := op.Run(ctx, o, r); err != nil {
			return err
		}
		s3.SetPreflightHeaders(w, o)
		s3.SetCommonHeaders(w, r)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return nil
	}
	var h *s3.Handler
	BeforeEach(func(ctx SpecContext) {
		store := memstore.New(memstore.Config{Release: denc.Squid, Now: func() time.Time { return unverifiedNow }})
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: "alice"}
		info.OpMask = op.OpTypeAll
		info.AccessKeys = map[string]meta.AccessKey{unverifiedKey: {ID: unverifiedKey, Secret: "secret-alice", Active: true}}
		store.AddUser(info)
		bob := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, OpMask: op.OpTypeAll})
		// Every rule allows every origin, so a header derived from the
		// Authorization header would answer * to some and the Origin to others.
		allowAll := (&cors.Configuration{Rules: []cors.Rule{{
			MaxAge: 600, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}, ExposeHeaders: []string{"ETag"},
		}}}).Encode()
		for _, b := range []struct {
			name      string
			owner     meta.Owner
			zonegroup string
		}{
			{"mine", meta.UserOwner(info.UserID), ""},
			{"theirs", meta.UserOwner(bob.Info.UserID), ""},
			{"far", meta.UserOwner(info.UserID), "zg-west"},
		} {
			rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
				Name: b.name, Owner: b.owner, Zonegroup: b.zonegroup, Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs: map[string][]byte{op.AttrCORS: allowAll},
			})
			Expect(err).NotTo(HaveOccurred())
			rec.Info.RequesterPays = true
			Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
		}
		cfg := auth.DefaultConfig()
		cfg.Now = func() time.Time { return unverifiedNow }
		h = s3.NewHandler(testEnv(store), auth.New(cfg, store, store), testConfig(s3.Config{}))
		h.Register(op.OptionsCORSName, preflight)
	})
	type answer struct {
		Status int
		Header http.Header
		Body   string
	}
	// answerOf is rec less its request id, which every request draws anew.
	answerOf := func(rec *httptest.ResponseRecorder) answer {
		hdr := rec.Header().Clone()
		id := exactHeader(hdr, "x-amz-request-id")
		Expect(id).NotTo(BeEmpty())
		delete(hdr, "x-amz-request-id")
		return answer{rec.Code, hdr, strings.ReplaceAll(rec.Body.String(), id, "")}
	}
	// serve sends req asking for method and, unless it is empty, the
	// headers asked.
	serve := func(req *http.Request, method, asked string) answer {
		req.Header.Set("Access-Control-Request-Method", method)
		if asked != "" {
			req.Header.Set("Access-Control-Request-Headers", asked)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return answerOf(rec)
	}

	DescribeTable("is byte for byte an anonymous preflight's, on every signed route",
		func(ctx SpecContext, target, method, asked string, status int, check func(answer)) {
			anonymous := serve(preflightRequest(ctx, target), method, asked)
			Expect(anonymous.Status).To(Equal(status), anonymous.Body)
			Expect(anonymous.Header).NotTo(haveHeaderAnyCase("x-amz-request-charged"))
			check(anonymous)
			for _, f := range forgedPreflights {
				Expect(serve(f.build(ctx, target), method, asked)).To(Equal(anonymous), f.name)
			}
		},
		Entry("a 200 on a bucket the key's user owns", "/mine/k?x=1", http.MethodGet, "", http.StatusOK, func(a answer) {
			Expect(corsOf(a.Header)).To(Equal(map[string][]string{
				"Access-Control-Allow-Origin":   {"https://example.org"},
				"Vary":                          {"Origin"},
				"Access-Control-Allow-Methods":  {"GET"},
				"Access-Control-Expose-Headers": {"ETag"},
				"Access-Control-Max-Age":        {"600"},
			}))
		}),
		Entry("a 200 on a bucket another user owns", "/theirs/k?x=1", http.MethodGet, "", http.StatusOK, func(a answer) {
			Expect(exactHeader(a.Header, "Access-Control-Allow-Origin")).To(Equal("https://example.org"))
		}),
		// The rule allows the method, so an ordinary request's headers
		// would come from it; the preflight's refusal must carry none.
		Entry("a 403 for a requested header no rule allows", "/mine/k?x=1", http.MethodGet, "x-amz-meta-secret", http.StatusForbidden, func(a answer) {
			Expect(corsOf(a.Header)).To(BeEmpty())
		}),
		Entry("a 200 at bucket scope", "/theirs?x=1", http.MethodGet, "", http.StatusOK, func(a answer) {
			Expect(exactHeader(a.Header, "Access-Control-Allow-Origin")).To(Equal("https://example.org"))
		}),
		Entry("a 403 for a method no rule allows", "/mine/k?x=1", http.MethodDelete, "", http.StatusForbidden, func(a answer) {
			Expect(corsOf(a.Header)).To(BeEmpty())
		}),
		Entry("the 301 of another zonegroup's bucket", "/far/k?x=1", http.MethodGet, "", http.StatusMovedPermanently, func(a answer) {
			Expect(corsOf(a.Header)).To(BeEmpty())
		}),
		Entry("a 404 for a bucket that does not exist", "/missing/k?x=1", http.MethodGet, "", http.StatusNotFound, func(a answer) {
			Expect(a.Body).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
			Expect(corsOf(a.Header)).To(BeEmpty())
		}),
	)
})
