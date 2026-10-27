package s3_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// setCORSDoc is test_set_cors's configuration as boto3 sends it, which is
// also the document GetBucketCors writes back for it.
const setCORSDoc = `<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><CORSRule>` +
	`<AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod>` +
	`<AllowedOrigin>*.get</AllowedOrigin><AllowedOrigin>*.put</AllowedOrigin></CORSRule></CORSConfiguration>`

// corsRulesDoc is a CORSConfiguration of n rules.
func corsRulesDoc(n int) string {
	rule := `<CORSRule><AllowedMethod>GET</AllowedMethod><AllowedOrigin>https://app.example</AllowedOrigin></CORSRule>`
	return `<CORSConfiguration>` + strings.Repeat(rule, n) + `</CORSConfiguration>`
}

var _ = Describe("bucket CORS configuration routes", func() {
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := w.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	setAttr := func(ctx context.Context, v []byte) {
		GinkgoHelper()
		Expect(w.store.PutBucketAttrs(ctx, bucket(ctx), map[string][]byte{op.AttrCORS: v}, nil)).To(Succeed())
	}
	options := func(kv ...string) {
		m := cephconf.MapGetter{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		w.env.Conf = cephconf.NewOptions(m)
	}
	// send serves method on plain's ?cors as who with body, whose
	// Content-Length is length, or none when length is negative; hdr
	// alternates header names and values.
	send := func(who *op.UserRecord, method string, body *readCalls, length int64, hdr ...string) *httptest.ResponseRecorder {
		GinkgoHelper()
		if body == nil {
			body = bodyOfString("")
		}
		req := httptest.NewRequestWithContext(GinkgoT().Context(), method, "/plain?cors", body)
		req.ContentLength = max(length, 0)
		if length >= 0 {
			req.Header.Set("Content-Length", strconv.FormatInt(length, 10))
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		s3.NewHandler(w.env, authAs(who), testConfig(s3.Config{})).ServeHTTP(rec, req)
		return rec
	}
	put := func(who *op.UserRecord, doc string, hdr ...string) *httptest.ResponseRecorder {
		GinkgoHelper()
		return send(who, http.MethodPut, bodyOfString(doc), int64(len(doc)), hdr...)
	}
	expectError := func(rec *httptest.ResponseRecorder, status int, code, message string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<Code>"+code+"</Code><Message>"+message+"</Message>"), rec.Body.String())
	}

	It("binds the three configuration routes at bucket scope and the preflight at bucket and object scope", func() {
		h := newHandler(w.store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements(
			"bucket/get_cors", "bucket/put_cors", "bucket/delete_cors", "bucket/options_cors", "object/options_cors"))
	})

	It("answers test_set_cors's requests: 404, a 200 that stores the configuration, the document stored, a 204 that removes it, 404",
		func(ctx SpecContext) {
			rec := send(w.alice, http.MethodGet, nil, -1)
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			id := exactHeader(rec.Header(), "x-amz-request-id")
			Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchCORSConfiguration</Code>`+
				`<Message></Message><BucketName>plain</BucketName><RequestId>`+id+`</RequestId><HostId>4155-z-zg</HostId></Error>`),
				"ERR_NO_CORS_FOUND, rgw_op.cc:6068-6071 at v19.2.7")

			expectEmptyXML(put(w.alice, setCORSDoc), http.StatusOK)
			stored, err := cors.Decode(bucket(ctx).Attrs[op.AttrCORS])
			Expect(err).NotTo(HaveOccurred())
			Expect(stored).To(Equal(&cors.Configuration{Rules: []cors.Rule{{
				MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet | cors.MethodPut, AllowedOrigins: []string{"*.get", "*.put"},
			}}}))

			rec = send(w.alice, http.MethodGet, nil, -1)
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(rec.Body.String()).To(Equal(setCORSDoc),
				"no XML declaration: dump_start's is never flushed, rgw_rest_s3.cc:3758-3769 at v19.2.7")
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(setCORSDoc))))
			Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "the frontend's length, not dump_content_length's")

			rec = send(w.alice, http.MethodDelete, nil, -1)
			Expect(rec.Code).To(Equal(http.StatusNoContent), rec.Body.String())
			Expect(rec.Body.String()).To(BeEmpty())
			Expect(rec.Header()).NotTo(HaveKey("Content-Type"), "end_header(s, NULL), rgw_rest_s3.cc:3843-3852 at v19.2.7")
			Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrCORS))

			Expect(send(w.alice, http.MethodGet, nil, -1).Code).To(Equal(http.StatusNotFound))
		})
	It("PUT replaces the stored configuration whole", func(ctx SpecContext) {
		expectEmptyXML(put(w.alice, setCORSDoc), http.StatusOK)
		expectEmptyXML(put(w.alice, corsRulesDoc(2)), http.StatusOK)
		rec := send(w.alice, http.MethodGet, nil, -1)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		rule := `<CORSRule><AllowedMethod>GET</AllowedMethod><AllowedOrigin>https://app.example</AllowedOrigin></CORSRule>`
		Expect(rec.Body.String()).To(Equal(`<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` + rule + rule + `</CORSConfiguration>`))
	})

	It("answer a PutBucketCors with the CORS headers of the configuration it stored, and a DeleteBucketCors with none", func() {
		allowsDelete := `<CORSConfiguration><CORSRule><AllowedMethod>DELETE</AllowedMethod>` +
			`<AllowedOrigin>https://app.example</AllowedOrigin></CORSRule></CORSConfiguration>`
		expectEmptyXML(put(w.alice, allowsDelete), http.StatusOK)
		rec := send(w.alice, http.MethodDelete, nil, -1, "Origin", "https://app.example")
		Expect(rec.Code).To(Equal(http.StatusNoContent), rec.Body.String())
		Expect(corsOf(rec.Header())).To(BeEmpty(), "the configuration it removed allowed the request")

		allowsPut := `<CORSConfiguration><CORSRule><AllowedMethod>PUT</AllowedMethod>` +
			`<AllowedOrigin>https://other.example</AllowedOrigin></CORSRule></CORSConfiguration>`
		rec = put(w.alice, allowsPut, "Origin", "https://other.example")
		expectEmptyXML(rec, http.StatusOK)
		Expect(corsOf(rec.Header())).To(Equal(map[string][]string{
			"Access-Control-Allow-Origin":  {"https://other.example"},
			"Vary":                         {"Origin"},
			"Access-Control-Allow-Methods": {"PUT"},
		}), `docs/exclusions.md, "Every S3 response carries CORS headers once its bucket is loaded"`)
	})

	DescribeTable("PUT answers a body it cannot store with its S3 error and radosgw's message, writing nothing",
		func(ctx SpecContext, doc string, length int64, status int, code string) {
			before := bucket(ctx).Version
			expectError(send(w.alice, http.MethodPut, bodyOfString(doc), length), status, code, "")
			Expect(bucket(ctx).Version).To(Equal(before))
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrCORS))
		},
		Entry("one that does not parse", "<CORSConfiguration", int64(len("<CORSConfiguration")),
			http.StatusBadRequest, "MalformedXML"),
		Entry("one whose root is not CORSConfiguration", "<Configuration/>", int64(len("<Configuration/>")),
			http.StatusBadRequest, "MalformedXML"),
		Entry("a CORSConfiguration without a CORSRule", "<CORSConfiguration/>", int64(len("<CORSConfiguration/>")),
			http.StatusBadRequest, "MalformedXML"),
		Entry("a rule without an AllowedOrigin",
			"<CORSConfiguration><CORSRule><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>",
			int64(len("<CORSConfiguration><CORSRule><AllowedMethod>GET</AllowedMethod></CORSRule></CORSConfiguration>")),
			http.StatusBadRequest, "MalformedXML"),
		Entry("an empty body with a Content-Length of 0", "", int64(0), http.StatusBadRequest, "MalformedXML"),
		Entry("no Content-Length, read_all_input's -ERR_LENGTH_REQUIRED", "", int64(-1),
			http.StatusLengthRequired, "MissingContentLength"),
	)
	It("PUT refuses a Content-Length past rgw_max_put_param_size 416 InvalidRange unread, as rgw_rest_read_all_input refuses it before recv_body",
		func(ctx SpecContext) {
			options("rgw_max_put_param_size", "16")
			before := bucket(ctx).Version
			body := bodyOfString(setCORSDoc)
			expectError(send(w.alice, http.MethodPut, body, int64(len(setCORSDoc))), http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "")
			Expect(body.calls).To(BeZero(), "rgw_rest.cc:1555-1558 at v19.2.7")
			Expect(bucket(ctx).Version).To(Equal(before))
		})
	DescribeTable("PUT holds the rules to rgw_cors_rules_max_num, read as get_params reads it into an int",
		func(ctx SpecContext, limit string, rules, admitted int) {
			if limit != "" {
				options("rgw_cors_rules_max_num", limit)
			}
			rec := put(w.alice, corsRulesDoc(rules))
			if admitted >= rules {
				expectEmptyXML(rec, http.StatusOK)
				stored, err := cors.Decode(bucket(ctx).Attrs[op.AttrCORS])
				Expect(err).NotTo(HaveOccurred())
				Expect(stored.Rules).To(HaveLen(rules))
				return
			}
			expectError(rec, http.StatusBadRequest, "InvalidRequest",
				"The number of CORS rules should not exceed allowed limit of "+strconv.Itoa(admitted)+" rules.")
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrCORS))
		},
		Entry("2 admits 2", "2", 2, 2),
		Entry("2 refuses 3", "2", 3, 2),
		Entry("0 refuses 1", "0", 1, 0),
		Entry("unset, its default 100, admits 100", "", 100, 100),
		Entry("unset, its default 100, refuses 101", "", 101, 100),
		Entry("a negative one is 100, which refuses 101", "-1", 101, 100),
		Entry("2^32+2 keeps its low 32 bits, 2", "4294967298", 3, 2),
		Entry("2^31 keeps its low 32 bits, a negative int, so 100", "2147483648", 101, 100),
	)
	It("DELETE answers 204 with no configuration to remove, writing nothing", func(ctx SpecContext) {
		before := bucket(ctx).Version
		rec := send(w.alice, http.MethodDelete, nil, -1)
		Expect(rec.Code).To(Equal(http.StatusNoContent), rec.Body.String())
		Expect(rec.Body.String()).To(BeEmpty())
		Expect(bucket(ctx).Version).To(Equal(before), "-ENOENT is answered 204, rgw_rest_s3.cc:3846-3847 at v19.2.7")
	})
	It("DELETE removes a configuration that does not decode, answering 204", func(ctx SpecContext) {
		setAttr(ctx, []byte{0xff})
		Expect(send(w.alice, http.MethodGet, nil, -1).Code).To(Equal(http.StatusInternalServerError), "read_bucket_cors' -EIO")
		rec := send(w.alice, http.MethodDelete, nil, -1)
		Expect(rec.Code).To(Equal(http.StatusNoContent), `docs/exclusions.md, "DeleteBucketCors removes a CORS configuration that does not decode": %s`, rec.Body.String())
		Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrCORS))
	})

	DescribeTable("refuse a requester the bucket grants nothing with 403 AccessDenied, reading no body and writing nothing",
		func(ctx SpecContext, method string) {
			setAttr(ctx, (&cors.Configuration{Rules: []cors.Rule{{Methods: cors.MethodGet, AllowedOrigins: []string{"*"}}}}).Encode())
			before := bucket(ctx).Version
			body := bodyOfString(setCORSDoc)
			expectError(send(w.bob, method, body, int64(len(setCORSDoc))), http.StatusForbidden, "AccessDenied", "")
			Expect(body.calls).To(BeZero())
			Expect(bucket(ctx).Version).To(Equal(before))
		},
		Entry("GET", http.MethodGet),
		Entry("PUT", http.MethodPut),
		Entry("DELETE", http.MethodDelete),
	)
	DescribeTable("refuse an x-amz-storage-class the bucket's placement lacks with 400 InvalidArgument once the requester is authorized",
		func(ctx SpecContext, method string) {
			setAttr(ctx, (&cors.Configuration{Rules: []cors.Rule{{Methods: cors.MethodGet, AllowedOrigins: []string{"*"}}}}).Encode())
			before := bucket(ctx).Version
			for _, c := range []struct {
				who          *op.UserRecord
				status       int
				code, reason string
			}{
				{w.alice, http.StatusBadRequest, "InvalidArgument", "the owner"},
				{w.bob, http.StatusForbidden, "AccessDenied", "a requester the bucket refuses learns nothing of its placement"},
			} {
				body := bodyOfString(setCORSDoc)
				rec := send(c.who, method, body, int64(len(setCORSDoc)), "X-Amz-Storage-Class", "GLACIAL")
				Expect(rec.Code).To(Equal(c.status), "%s: %s", c.reason, rec.Body.String())
				Expect(rec.Body.String()).To(ContainSubstring("<Code>"+c.code+"</Code>"), c.reason)
				Expect(body.calls).To(BeZero(), "%s: the body is not read", c.reason)
			}
			Expect(bucket(ctx).Version).To(Equal(before))

			body := bodyOfString(setCORSDoc)
			rec := send(w.alice, method, body, int64(len(setCORSDoc)), "X-Amz-Storage-Class", meta.StorageClassStandard)
			Expect(rec.Code).To(BeNumerically("<", http.StatusMultipleChoices), "a class the placement has: %s", rec.Body.String())
		},
		Entry("GET", http.MethodGet),
		Entry("PUT", http.MethodPut),
		Entry("DELETE", http.MethodDelete),
	)
})

// bodyOfString is a request body of s that counts its reads.
func bodyOfString(s string) *readCalls { return &readCalls{r: strings.NewReader(s)} }

// preflightCORS is three rules: https://app.example may GET and PUT with
// x-amz-* headers, any origin may HEAD, and https://app.example may DELETE.
func preflightCORS() *cors.Configuration {
	return &cors.Configuration{Rules: []cors.Rule{
		{
			ID: "app", MaxAge: 600, Methods: cors.MethodGet | cors.MethodPut,
			AllowedOrigins: []string{"https://app.example"}, AllowedHeaders: []string{"x-amz-*"}, ExposeHeaders: []string{"ETag"},
		},
		{ID: "anyone", MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodHead, AllowedOrigins: []string{"*"}},
		{ID: "app-delete", MaxAge: 60, Methods: cors.MethodDelete, AllowedOrigins: []string{"https://app.example"}},
	}}
}

// preflightKey is a key whose user is the kind its name says. None of their
// users owns plain.
type preflightKey struct {
	kind, id string
	owner    meta.Owner
}

// preflightWorld is alice's requester-pays bucket plain, whose CORS
// configuration is preflightCORS, served by the real handler and
// authenticator, with a key of each kind of user.
type preflightWorld struct {
	store *memstore.Store
	h     *s3.Handler
	keys  []preflightKey
}

// preflightAccount is the account the account user's key belongs to.
const preflightAccount = "RGW12345678901234567"

func newPreflightWorld(ctx context.Context, rel denc.Release) *preflightWorld {
	GinkgoHelper()
	store := memstore.New(memstore.Config{Release: rel, Now: func() time.Time { return unverifiedNow }})
	add := func(tenant, id string, set func(*meta.UserInfo)) meta.UserInfo {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{Tenant: tenant, ID: id}
		u.OpMask = op.OpTypeAll
		key := "AK" + strings.ToUpper(id)
		u.AccessKeys = map[string]meta.AccessKey{key: {ID: key, Secret: "secret-" + id, Active: true}}
		if set != nil {
			set(&u)
		}
		store.AddUser(u)
		return u
	}
	alice := add("", "alice", nil)
	store.AddAccount(meta.AccountInfo{ID: preflightAccount, Name: "acct"})
	w := &preflightWorld{store: store}
	for _, k := range []struct {
		kind, tenant, id string
		set              func(*meta.UserInfo)
	}{
		{"a plain user's", "", "plain", nil},
		{"a tenanted user's", "t", "tenanted", nil},
		{"an account user's", "", "member", func(u *meta.UserInfo) { u.AccountID = preflightAccount }},
		{"a suspended user's", "", "suspended", func(u *meta.UserInfo) { u.Suspended = 1 }},
		{"a system user's", "", "system", func(u *meta.UserInfo) { u.System = 1 }},
		{"an admin's", "", "admin", func(u *meta.UserInfo) { u.Admin = 1 }},
		{"a user's whose op mask lacks read", "", "noread", func(u *meta.UserInfo) { u.OpMask = op.OpTypeModify }},
	} {
		u := add(k.tenant, k.id, k.set)
		owner := meta.UserOwner(u.UserID)
		if u.AccountID != "" {
			owner = meta.AccountOwner(u.AccountID)
		}
		w.keys = append(w.keys, preflightKey{k.kind, "AK" + strings.ToUpper(k.id), owner})
	}
	w.createBucket(ctx, "", "plain", meta.UserOwner(alice.UserID), preflightCORS())
	env := testEnv(store)
	env.Authz = authz.New(authz.DefaultConfig(rel))
	cfg := auth.DefaultConfig()
	cfg.Now = func() time.Time { return unverifiedNow }
	w.h = s3.NewHandler(env, auth.New(cfg, store, store), testConfig(s3.Config{}))
	return w
}

// createBucket creates tenant's bucket name for owner, requester-pays, with
// owner's default ACL and c as its CORS configuration.
func (w *preflightWorld) createBucket(ctx context.Context, tenant, name string, owner meta.Owner, c *cors.Configuration) {
	GinkgoHelper()
	rec, err := w.store.CreateBucket(ctx, op.CreateBucketParams{
		Tenant: tenant, Name: name, Owner: owner, Placement: meta.PlacementRule{Name: "default-placement"},
		Attrs: map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "")), op.AttrCORS: c.Encode()},
	})
	Expect(err).NotTo(HaveOccurred())
	rec.Info.RequesterPays = true
	Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
}

// plain is the default tenant's bucket plain.
func (w *preflightWorld) plain(ctx context.Context) *op.BucketRecord {
	GinkgoHelper()
	rec, err := w.store.GetBucket(ctx, "", "plain")
	Expect(err).NotTo(HaveOccurred())
	return rec
}

// preflightAnswered is what the client sees of one request, less its request
// id, which every request draws anew, and the usage entries it wrote.
type preflightAnswered struct {
	Status int
	Header http.Header
	Body   string
	Usage  []op.UsageEntry
}

// serve sends req, after each edit, and returns what it answered. The
// headers are those the response was sent with, so one set after the status
// is not among them.
func (w *preflightWorld) serve(req *http.Request, edits ...func(*http.Request)) preflightAnswered {
	GinkgoHelper()
	for _, e := range edits {
		e(req)
	}
	before := len(w.store.Usage())
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	res := rec.Result()
	Expect(res.Body.Close()).To(Succeed())
	hdr := res.Header.Clone()
	id := exactHeader(hdr, "x-amz-request-id")
	Expect(id).NotTo(BeEmpty())
	delete(hdr, "x-amz-request-id")
	return preflightAnswered{rec.Code, hdr, strings.ReplaceAll(rec.Body.String(), id, ""), w.store.Usage()[before:]}
}

// setHeader is an edit that sets name to v.
func setHeader(name, v string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Set(name, v) }
}

// dropHeader is an edit that removes name.
func dropHeader(name string) func(*http.Request) {
	return func(req *http.Request) { req.Header.Del(name) }
}

// fromApp is the edit that sends the Origin preflightCORS allows.
var fromApp = setHeader("Origin", "https://app.example")

// signedCORSPut is a PutBucketCors of doc on bucket, signed with SigV4 for
// key and secret as the SDK's S3 client signs it.
func signedCORSPut(ctx context.Context, key, secret, bucket, doc string) *http.Request {
	GinkgoHelper()
	req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/"+bucket+"?cors", strings.NewReader(doc))
	req.Header.Set("Content-Length", strconv.Itoa(len(doc)))
	sum := sha256.Sum256([]byte(doc))
	hash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", hash)
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	Expect(signer.SignHTTP(ctx, aws.Credentials{AccessKeyID: key, SecretAccessKey: secret}, req, hash, "s3", "us-east-1", unverifiedNow)).To(Succeed())
	return req
}

// presignV2 is the query of a SigV2 presigned URL for method on resource,
// signed with secret.
func presignV2(key, secret, method, resource string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(method + "\n\n\n" + v2Expiry + "\n" + resource))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return "AWSAccessKeyId=" + key + "&Expires=" + v2Expiry + "&Signature=" + url.QueryEscape(sig)
}

// A preflight is granted on its key's id alone, so its answer must be an
// anonymous preflight's for every key that resolves, or it tells whoever
// holds a key's id something of the key's user.
var _ = Describe("the CORS preflight route", func() {
	DescribeTable("answers a forged preflight as an anonymous one, status, headers, body and usage, for every key that resolves",
		func(ctx SpecContext, target string, prepare func(context.Context, *preflightWorld), status int, edits ...func(*http.Request)) {
			captureErrors()
			for _, rel := range both {
				w := newPreflightWorld(ctx, rel)
				if prepare != nil {
					prepare(ctx, w)
				}
				want := w.serve(preflightRequest(ctx, target), edits...)
				Expect(want.Status).To(Equal(status), "%s, anonymous: %s", releaseName(rel), want.Body)
				Expect(want.Header).NotTo(haveHeaderAnyCase("x-amz-request-charged"), "%s, anonymous", releaseName(rel))
				for _, k := range w.keys {
					for _, f := range forgedPreflightsBy(k.id) {
						got := w.serve(f.build(ctx, target, edits...))
						Expect(got).To(Equal(want), "%s, %s key, %s", releaseName(rel), k.kind, f.name)
						for _, e := range got.Usage {
							Expect([]meta.Owner{e.Owner, e.Payer}).NotTo(ContainElement(k.owner),
								"%s, %s key, %s: usage charged to the key's user", releaseName(rel), k.kind, f.name)
						}
					}
				}
			}
		},
		Entry("at bucket scope, on a request a rule allows, the anonymous user its payer", "/:plain?x=1", nil, http.StatusOK,
			fromApp, setHeader("Access-Control-Request-Method", "PUT"), setHeader("Access-Control-Request-Headers", "x-amz-date")),
		Entry("at object scope, of a key that does not exist, which the preflight never reads", "/:plain/missing?x=1", nil, http.StatusOK,
			fromApp),
		Entry("on a request whose method no rule allows for its origin", "/:plain?x=1", nil, http.StatusForbidden,
			fromApp, setHeader("Access-Control-Request-Method", "POST")),
		Entry("on a request one of whose headers the rule does not allow", "/:plain/k?x=1", nil, http.StatusForbidden,
			fromApp, setHeader("Access-Control-Request-Headers", "x-amz-date,content-type")),
		Entry("on a request without an Origin", "/:plain?x=1", nil, http.StatusBadRequest, dropHeader("Origin")),
		Entry("on a bucket without a configuration", "/:plain?x=1", func(ctx context.Context, w *preflightWorld) {
			Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), nil, []string{op.AttrCORS})).To(Succeed())
		}, http.StatusForbidden, fromApp),
		Entry("on a configuration that does not decode", "/:plain?x=1", func(ctx context.Context, w *preflightWorld) {
			Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), map[string][]byte{op.AttrCORS: {0xff}}, nil)).To(Succeed())
		}, http.StatusInternalServerError, fromApp),
		Entry("on a missing bucket", "/:missing/k?x=1", nil, http.StatusNotFound, fromApp),
		Entry("on another zonegroup's bucket, from the master zonegroup, where radosgw lets a system key through",
			"/:plain/k?x=1", func(ctx context.Context, w *preflightWorld) {
				rec := w.plain(ctx)
				rec.Info.Zonegroup = "elsewhere"
				Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
			}, http.StatusMovedPermanently, fromApp),
		Entry("on a suspended bucket, which radosgw refuses but to a system key, and on Tentacle an admin's",
			"/:plain?x=1", func(ctx context.Context, w *preflightWorld) {
				rec := w.plain(ctx)
				rec.Info.Flags |= meta.BucketSuspended
				Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
			}, http.StatusOK, fromApp),
		Entry("on a bucket whose policy does not parse, which radosgw refuses but to a system key, and on Tentacle an admin's",
			"/:plain?x=1", func(ctx context.Context, w *preflightWorld) {
				Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), map[string][]byte{op.AttrIAMPolicy: []byte("{")}, nil)).To(Succeed())
			}, http.StatusOK, fromApp),
		Entry("on a bucket whose ACL does not decode, radosgw's 500 for every key",
			"/:plain?x=1", func(ctx context.Context, w *preflightWorld) {
				Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), map[string][]byte{meta.AttrACL: {0xff}}, nil)).To(Succeed())
			}, http.StatusInternalServerError, fromApp),
		Entry("on a bucket whose ACL names an owner that does not load, which radosgw refuses but to a system key or one without the modify bits",
			"/:plain?x=1", func(ctx context.Context, w *preflightWorld) {
				gone := meta.UserOwner(meta.UserID{ID: "gone"})
				rec := w.plain(ctx)
				rec.Info.Owner = gone
				Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
				Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(gone, ""))}, nil)).To(Succeed())
			}, http.StatusOK, fromApp),
		Entry("with an rgwx-bucket-instance that names no instance, which radosgw answers 400 for a system key alone",
			"/:plain?rgwx-bucket-instance=x", nil, http.StatusOK, fromApp),
		Entry("with an x-amz-storage-class the bucket's placement lacks, which radosgw answers 400 for every key too",
			"/:plain?x=1", nil, http.StatusBadRequest, fromApp, setHeader("X-Amz-Storage-Class", "GLACIAL")),
	)

	It("sends the Origin as sent, with the rule's headers, on a request a rule allows", func(ctx SpecContext) {
		w := newPreflightWorld(ctx, denc.Squid)
		got := w.serve(preflightRequest(ctx, "/plain/k?x=1"), fromApp, setHeader("Access-Control-Request-Method", "PUT"),
			setHeader("Access-Control-Request-Headers", "X-Amz-Date, x-amz-meta-a"))
		Expect(got.Status).To(Equal(http.StatusOK), got.Body)
		Expect(got.Body).To(BeEmpty())
		Expect(corsOf(got.Header)).To(Equal(map[string][]string{
			"Access-Control-Allow-Origin":   {"https://app.example"},
			"Vary":                          {"Origin"},
			"Access-Control-Allow-Methods":  {"PUT"},
			"Access-Control-Allow-Headers":  {"X-Amz-Date,x-amz-meta-a"},
			"Access-Control-Expose-Headers": {"ETag"},
			"Access-Control-Max-Age":        {"600"},
		}))
		Expect(got.Header).NotTo(HaveKey("Content-Type"), "end_header(s, NULL), rgw_rest_s3.cc:3874 at v19.2.7")
		Expect(got.Header.Get("Content-Length")).To(Equal("0"))
	})

	It("refuses a preflight without an Access-Control-Request-Method 400 InvalidArgument, a signed one before its key is looked up",
		func(ctx SpecContext) {
			for _, rel := range both {
				w := newPreflightWorld(ctx, rel)
				anonymous := w.serve(preflightRequest(ctx, "/:plain?x=1"), fromApp, dropHeader("Access-Control-Request-Method"))
				Expect(anonymous.Status).To(Equal(http.StatusBadRequest), anonymous.Body)
				Expect(anonymous.Body).To(ContainSubstring("<Code>InvalidArgument</Code>"))
				Expect(anonymous.Body).To(ContainSubstring("<BucketName>plain</BucketName>"), "the op's refusal, rgw_op.cc:6189-6194 at v19.2.7")
				for _, f := range forgedPreflightsBy("AKNOBODY") {
					want := w.serve(f.build(ctx, "/:plain?x=1", fromApp, dropHeader("Access-Control-Request-Method")))
					Expect(want.Status).To(Equal(http.StatusBadRequest), "%s: %s", f.name, want.Body)
					Expect(want.Body).NotTo(ContainSubstring("<BucketName>"), "%s: refused before postauth_init names the bucket", f.name)
					for _, k := range w.keys {
						for _, g := range forgedPreflightsBy(k.id) {
							if g.name != f.name {
								continue
							}
							got := w.serve(g.build(ctx, "/:plain?x=1", fromApp, dropHeader("Access-Control-Request-Method")))
							Expect(got).To(Equal(want), "%s, %s key, %s", releaseName(rel), k.kind, g.name)
						}
					}
				}
			}
		})

	// radosgw looks a preflight's key up for its tenant alone: a tenanted
	// user's page names its bucket without the tenant.
	It("resolves a forged preflight's bucket under its key user's tenant, as an anonymous preflight of the name under that tenant",
		func(ctx SpecContext) {
			for _, rel := range both {
				w := newPreflightWorld(ctx, rel)
				other := &cors.Configuration{Rules: []cors.Rule{
					{ID: "other", MaxAge: 30, Methods: cors.MethodGet, AllowedOrigins: []string{"https://other.example"}},
				}}
				w.createBucket(ctx, "t", "plain", meta.UserOwner(meta.UserID{Tenant: "t", ID: "towner"}), other)
				fromOther := setHeader("Origin", "https://other.example")
				tenanted := w.serve(preflightRequest(ctx, "/t:plain/k?x=1"), fromOther)
				Expect(tenanted.Status).To(Equal(http.StatusOK), "%s: %s", releaseName(rel), tenanted.Body)
				Expect(exactHeader(tenanted.Header, "Access-Control-Max-Age")).To(Equal("30"), "t:plain's rule")
				untenanted := w.serve(preflightRequest(ctx, "/plain/k?x=1"), fromOther)
				Expect(untenanted.Status).To(Equal(http.StatusForbidden), "%s: no rule of the default tenant's plain allows the origin", releaseName(rel))
				for _, f := range forgedPreflightsBy("AKTENANTED") {
					got := w.serve(f.build(ctx, "/plain/k?x=1", fromOther))
					Expect(got).To(Equal(tenanted), "%s, %s: rgw_rest_s3.cc:4995-4998 and rgw_bucket.cc:127-130 at v19.2.7", releaseName(rel), f.name)
					Expect(got).NotTo(Equal(untenanted), "%s, %s", releaseName(rel), f.name)
				}
			}
		})

	It("sends the Origin as sent on a rule that allows every origin, with an Authorization header and without one",
		func(ctx SpecContext) {
			w := newPreflightWorld(ctx, denc.Squid)
			anyone := &cors.Configuration{Rules: []cors.Rule{
				{MaxAge: 600, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}, ExposeHeaders: []string{"ETag"}},
			}}
			Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), map[string][]byte{op.AttrCORS: anyone.Encode()}, nil)).To(Succeed())
			want := map[string][]string{
				"Access-Control-Allow-Origin":   {"https://example.org"},
				"Vary":                          {"Origin"},
				"Access-Control-Allow-Methods":  {"GET"},
				"Access-Control-Expose-Headers": {"ETag"},
				"Access-Control-Max-Age":        {"600"},
			}
			anonymous := w.serve(preflightRequest(ctx, "/plain/k?x=1"))
			Expect(anonymous.Status).To(Equal(http.StatusOK), anonymous.Body)
			Expect(corsOf(anonymous.Header)).To(Equal(want), "rgw_rest_s3.cc:3872-3873 at v19.2.7")
			for _, f := range forgedPreflightsBy("AKPLAIN") {
				req := f.build(ctx, "/plain/k?x=1")
				got := w.serve(req)
				Expect(got.Status).To(Equal(http.StatusOK), "%s: %s", f.name, got.Body)
				Expect(corsOf(got.Header)).To(Equal(want), "%s, with an Authorization header: %t", f.name, req.Header.Get("Authorization") != "")
			}

			get := w.serve(httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/plain/k", nil),
				setHeader("Origin", "https://example.org"))
			Expect(exactHeader(get.Header, "Access-Control-Allow-Origin")).To(Equal("*"),
				"a GET without an Authorization header gets * from the same rule: %s", get.Body)
		})

	// _test_cors_options_presigned_method: a browser's preflight for a
	// presigned URL carries neither the signature's headers nor the
	// request's own method.
	DescribeTable("answers test_cors_presigned's preflights of a presigned URL",
		func(ctx SpecContext, tenant, owner, method string, signedACL, v2 bool) {
			for _, rel := range both {
				w := newPreflightWorld(ctx, rel)
				key, secret := "AK"+strings.ToUpper(owner), "secret-"+owner
				const bucketName = "presigned"
				Expect(w.store.CreateBucket(ctx, op.CreateBucketParams{
					Tenant: tenant, Name: bucketName, Owner: meta.UserOwner(meta.UserID{Tenant: tenant, ID: owner}),
					Placement: meta.PlacementRule{Name: "default-placement"},
					Attrs:     map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(meta.UserOwner(meta.UserID{Tenant: tenant, ID: owner}), ""))},
				})).Error().NotTo(HaveOccurred())

				var target string
				if v2 {
					target = "/" + bucketName + "/foo?" + presignV2(key, secret, method, "/"+bucketName+"/foo")
				} else {
					req := httptest.NewRequestWithContext(ctx, method, "http://s3.example.com/"+bucketName+"/foo?X-Amz-Expires=100000", nil)
					if signedACL {
						req.Header.Set("X-Amz-Acl", "private")
					}
					u, _, err := v4.NewSigner().PresignHTTP(ctx, aws.Credentials{AccessKeyID: key, SecretAccessKey: secret}, req,
						"UNSIGNED-PAYLOAD", "s3", "us-east-1", unverifiedNow)
					Expect(err).NotTo(HaveOccurred())
					parsed, err := url.Parse(u)
					Expect(err).NotTo(HaveOccurred())
					target = parsed.RequestURI()
				}

				bare := w.serve(preflightRequest(ctx, target), dropHeader("Origin"), dropHeader("Access-Control-Request-Method"))
				Expect(bare.Status).To(Equal(http.StatusBadRequest), "%s, before a configuration: %s", releaseName(rel), bare.Body)

				doc := `<CORSConfiguration><CORSRule><AllowedMethod>` + method + `</AllowedMethod>` +
					`<AllowedOrigin>example</AllowedOrigin></CORSRule></CORSConfiguration>`
				stored := w.serve(signedCORSPut(ctx, key, secret, bucketName, doc))
				Expect(stored.Status).To(Equal(http.StatusOK), "%s, PutBucketCors: %s", releaseName(rel), stored.Body)

				got := w.serve(preflightRequest(ctx, target), setHeader("Origin", "example"), setHeader("Access-Control-Request-Method", method))
				Expect(got.Status).To(Equal(http.StatusOK), "%s: %s", releaseName(rel), got.Body)
				Expect(exactHeader(got.Header, "Access-Control-Allow-Origin")).To(Equal("example"), releaseName(rel))
				Expect(exactHeader(got.Header, "Access-Control-Allow-Methods")).To(Equal(method), releaseName(rel))
			}
		},
		Entry("test_cors_presigned_get_object", "", "plain", http.MethodGet, false, false),
		Entry("test_cors_presigned_get_object_tenant", "t", "tenanted", http.MethodGet, false, false),
		Entry("test_cors_presigned_get_object_v2", "", "plain", http.MethodGet, false, true),
		Entry("test_cors_presigned_get_object_tenant_v2", "t", "tenanted", http.MethodGet, false, true),
		Entry("test_cors_presigned_put_object", "", "plain", http.MethodPut, false, false),
		Entry("test_cors_presigned_put_object_with_acl", "", "plain", http.MethodPut, true, false),
		Entry("test_cors_presigned_put_object_v2", "", "plain", http.MethodPut, false, true),
		Entry("test_cors_presigned_put_object_tenant_v2", "t", "tenanted", http.MethodPut, false, true),
		Entry("test_cors_presigned_put_object_tenant", "t", "tenanted", http.MethodPut, false, false),
		Entry("test_cors_presigned_put_object_tenant_with_acl", "t", "tenanted", http.MethodPut, true, false),
	)

	It("answers test_cors_header_option: a requested header no rule allows is 403 with no Access-Control headers", func(ctx SpecContext) {
		w := newPreflightWorld(ctx, denc.Squid)
		c := &cors.Configuration{Rules: []cors.Rule{
			{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}, ExposeHeaders: []string{"x-amz-meta-header1"}},
		}}
		Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), map[string][]byte{op.AttrCORS: c.Encode()}, nil)).To(Succeed())
		got := w.serve(preflightRequest(ctx, "/plain/bar"), setHeader("Origin", "example.origin"),
			setHeader("Access-Control-Request-Headers", "x-amz-meta-header2"))
		Expect(got.Status).To(Equal(http.StatusForbidden), got.Body)
		Expect(got.Body).To(ContainSubstring("<Code>AccessDenied</Code>"))
		Expect(corsOf(got.Header)).To(BeEmpty())
	})

	It("answers test_cors_origin_wildcard: the configuration a PutBucketCors stores answers an anonymous GET with *", func(ctx SpecContext) {
		w := newPreflightWorld(ctx, denc.Squid)
		Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), nil, []string{op.AttrCORS})).To(Succeed())
		doc := `<CORSConfiguration><CORSRule><AllowedMethod>GET</AllowedMethod><AllowedOrigin>*</AllowedOrigin></CORSRule></CORSConfiguration>`
		stored := w.serve(signedCORSPut(ctx, "AKALICE", "secret-alice", "plain", doc))
		Expect(stored.Status).To(Equal(http.StatusOK), stored.Body)

		// _setup_bucket_acl's public-read bucket; S3 refuses an anonymous
		// request to a requester-pays one.
		publicRead, err := acl.Canned(acl.Owner{ID: "alice"}, acl.Owner{ID: "alice"}, "public-read")
		Expect(err).NotTo(HaveOccurred())
		Expect(w.store.PutBucketAttrs(ctx, w.plain(ctx), map[string][]byte{meta.AttrACL: encodeACL(publicRead)}, nil)).To(Succeed())
		rec := w.plain(ctx)
		rec.Info.RequesterPays = false
		Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
		bare := w.serve(httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/plain", nil))
		Expect(bare.Status).To(Equal(http.StatusOK), bare.Body)
		Expect(corsOf(bare.Header)).To(BeEmpty())
		got := w.serve(httptest.NewRequestWithContext(ctx, http.MethodGet, "http://s3.example.com/plain", nil),
			setHeader("Origin", "example.origin"))
		Expect(got.Status).To(Equal(http.StatusOK), got.Body)
		Expect(exactHeader(got.Header, "Access-Control-Allow-Origin")).To(Equal("*"))
		Expect(exactHeader(got.Header, "Access-Control-Allow-Methods")).To(Equal("GET"))
	})
})
