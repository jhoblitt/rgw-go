package s3_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// readCalls is a request body that counts the calls made to its Read.
type readCalls struct {
	r     io.Reader
	calls int
}

func (b *readCalls) Read(p []byte) (int, error) {
	b.calls++
	return b.r.Read(p)
}

// websiteDoc is a WebsiteConfiguration whose sections are in the order
// GetBucketWebsite writes them, so the document it gets back is this one.
const websiteDoc = `<WebsiteConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
	`<IndexDocument><Suffix>index.html</Suffix></IndexDocument><ErrorDocument><Key>error.html</Key></ErrorDocument>` +
	`<RoutingRules><RoutingRule><Condition><KeyPrefixEquals>docs/</KeyPrefixEquals></Condition>` +
	`<Redirect><Protocol>https</Protocol><HostName>example.com</HostName><HttpRedirectCode>301</HttpRedirectCode>` +
	`<ReplaceKeyPrefixWith>documents/</ReplaceKeyPrefixWith></Redirect></RoutingRule></RoutingRules></WebsiteConfiguration>`

// websiteConf is websiteDoc as PutBucketWebsite stores it.
func websiteConf() meta.WebsiteConf {
	return meta.WebsiteConf{
		IndexDocSuffix: "index.html",
		ErrorDoc:       "error.html",
		RoutingRules: meta.RoutingRules{{
			Condition: meta.RoutingCondition{KeyPrefixEquals: "docs/"},
			RedirectInfo: meta.RoutingRedirect{
				Redirect:             meta.RedirectInfo{Protocol: "https", Hostname: "example.com", HTTPRedirectCode: 301},
				ReplaceKeyPrefixWith: "documents/",
			},
		}},
	}
}

// rulesDoc is an index document with n routing rules.
func rulesDoc(n int) string {
	rule := `<RoutingRule><Redirect><ReplaceKeyWith>k</ReplaceKeyWith></Redirect></RoutingRule>`
	return `<WebsiteConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IndexDocument><Suffix>index.html</Suffix></IndexDocument>` +
		`<RoutingRules>` + strings.Repeat(rule, n) + `</RoutingRules></WebsiteConfiguration>`
}

var _ = Describe("bucket website routes", func() {
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

	// options sets the options the requests read, with the website API on
	// unless kv says otherwise.
	options := func(kv ...string) {
		m := cephconf.MapGetter{"rgw_enable_static_website": "true"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		w.env.Conf = cephconf.NewOptions(m)
	}
	setWebsite := func(ctx context.Context, conf *meta.WebsiteConf) {
		GinkgoHelper()
		rec := w.bucket(ctx)
		rec.Info.Website = conf
		Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	// send serves method on plain's ?website as who with body, whose
	// Content-Length is length, or none when length is 0; a negative length
	// sends the body chunked.
	send := func(who *op.UserRecord, method string, body *readCalls, length int64) *httptest.ResponseRecorder {
		GinkgoHelper()
		req := httptest.NewRequestWithContext(GinkgoT().Context(), method, "/plain?website", body)
		req.ContentLength = length
		if length > 0 {
			req.Header.Set("Content-Length", strconv.FormatInt(length, 10))
		}
		rec := httptest.NewRecorder()
		s3.NewHandler(w.env, authAs(who), testConfig(s3.Config{})).ServeHTTP(rec, req)
		return rec
	}
	bodyOf := func(s string) *readCalls { return &readCalls{r: strings.NewReader(s)} }
	expectError := func(rec *httptest.ResponseRecorder, status int, code, message string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<Code>"+code+"</Code><Message>"+message+"</Message>"), rec.Body.String())
	}

	It("binds the three routes in every handler", func() {
		h := newHandler(w.store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements(
			"bucket/get_bucket_website", "bucket/set_bucket_website", "bucket/delete_bucket_website"))
	})

	DescribeTable("with rgw_enable_static_website off answer 405 MethodNotAllowed naming no bucket, as radosgw answers a request it finds no op for, and read, write and log nothing",
		func(ctx SpecContext, method, option string) {
			if option != "" {
				w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_enable_static_website": option})
			}
			stored := websiteConf()
			setWebsite(ctx, &stored)
			before := w.bucket(ctx).Version
			body := bodyOf(websiteDoc)
			rec := send(w.alice, method, body, int64(len(websiteDoc)))
			Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed), rec.Body.String())
			id := exactHeader(rec.Header(), "x-amz-request-id")
			Expect(id).NotTo(BeEmpty())
			want := `<?xml version="1.0" encoding="UTF-8"?><Error><Code>MethodNotAllowed</Code><Message></Message>` +
				`<RequestId>` + id + `</RequestId><HostId>4155-z-zg</HostId></Error>`
			Expect(rec.Body.String()).To(Equal(want),
				"no BucketName: get_op refuses at rgw_process.cc:325-329 and postauth_init names the bucket at :365, v19.2.7")
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
			Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"),
				"end_header's error branch sets the length through dump_content_length, rgw_rest.cc:620-624")
			Expect(body.calls).To(BeZero(), "nothing read of the body")
			Expect(w.bucket(ctx).Version).To(Equal(before), "nothing written")
			Expect(w.bucket(ctx).Info.Website).To(Equal(&stored))
			Expect(w.store.Usage()).To(BeEmpty(), "radosgw files a request with no op under no user, which its flush drops")
		},
		Entry("GET with the option unset, its default", http.MethodGet, ""),
		Entry("PUT with the option false", http.MethodPut, "false"),
		Entry("DELETE with the option false", http.MethodDelete, "false"),
	)
	// radosgw answers 405 at get_op, before schedule_request, verify_requester
	// and the suspended-user check
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L325-L376 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327-L378 at v20.2.4).
	DescribeTable("with rgw_enable_static_website off answer 405 MethodNotAllowed before the concurrency cap, authentication and the suspended-user check, whose refusal comes with it on",
		func(build func(*subresWorld) (s3.Authenticator, s3.Config), status int, code string) {
			auth, cfg := build(w)
			put := func() *httptest.ResponseRecorder {
				return serveReq(s3.NewHandler(w.env, auth, testConfig(cfg)), http.MethodPut, "/plain?website", strings.NewReader(websiteDoc))
			}
			expectError(put(), http.StatusMethodNotAllowed, "MethodNotAllowed", "")
			options()
			expectError(put(), status, code, "")
		},
		Entry("a request over rgw_max_concurrent_requests", func(w *subresWorld) (s3.Authenticator, s3.Config) {
			inflight := new(atomic.Int64)
			inflight.Store(1)
			return authAs(w.alice), s3.Config{MaxConcurrent: 1, InFlight: inflight}
		}, http.StatusServiceUnavailable, "SlowDown"),
		Entry("a bad signature", func(*subresWorld) (s3.Authenticator, s3.Config) {
			return s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				return nil, op.ErrSignatureDoesNotMatch
			}), s3.Config{}
		}, http.StatusForbidden, "SignatureDoesNotMatch"),
		Entry("a suspended user", func(w *subresWorld) (s3.Authenticator, s3.Config) {
			carol := w.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "carol"}, Suspended: 1, OpMask: op.OpTypeAll})
			return authAs(carol), s3.Config{}
		}, http.StatusForbidden, "UserSuspended"),
	)
	It("reads rgw_enable_static_website on each request, as radosgw reads its configuration", func() {
		h := s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{}))
		Expect(serveReq(h, http.MethodDelete, "/plain?website", nil).Code).To(Equal(http.StatusMethodNotAllowed))
		options()
		Expect(serveReq(h, http.MethodDelete, "/plain?website", nil).Code).To(Equal(http.StatusNoContent))
	})

	Describe("with rgw_enable_static_website on", func() {
		BeforeEach(func() { options() })

		It("PUT answers 200 with no body under radosgw's type and stores the configuration", func(ctx SpecContext) {
			expectEmptyXML(send(w.alice, http.MethodPut, bodyOf(websiteDoc), int64(len(websiteDoc))), http.StatusOK)
			Expect(w.bucket(ctx).Info.Website).To(Equal(new(websiteConf())))
		})
		DescribeTable("GET answers the stored configuration in S3's namespace after the XML declaration",
			func(ctx SpecContext, conf meta.WebsiteConf, doc string) {
				setWebsite(ctx, &conf)
				rec := send(w.alice, http.MethodGet, bodyOf(""), 0)
				Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
				want := `<?xml version="1.0" encoding="UTF-8"?>` + doc
				Expect(rec.Body.String()).To(Equal(want), "rgw_rest_s3.cc:2365-2383 at v19.2.7")
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
				Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
				Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
			},
			Entry("an index document, an error document and a rule", websiteConf(), websiteDoc),
			Entry("RedirectAllRequestsTo", meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Protocol: "https", Hostname: "example.org"}},
				`<WebsiteConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><RedirectAllRequestsTo>`+
					`<HostName>example.org</HostName><Protocol>https</Protocol></RedirectAllRequestsTo></WebsiteConfiguration>`),
		)
		It("GET of a bucket without a website answers 404 NoSuchWebsiteConfiguration", func() {
			expectError(send(w.alice, http.MethodGet, bodyOf(""), 0), http.StatusNotFound, "NoSuchWebsiteConfiguration", "")
		})
		It("DELETE answers 204 with no body under radosgw's type and clears the configuration", func(ctx SpecContext) {
			stored := websiteConf()
			setWebsite(ctx, &stored)
			expectEmptyXML(send(w.alice, http.MethodDelete, bodyOf(""), 0), http.StatusNoContent)
			Expect(w.bucket(ctx).Info.Website).To(BeNil())
			expectError(send(w.alice, http.MethodGet, bodyOf(""), 0), http.StatusNotFound, "NoSuchWebsiteConfiguration", "")
		})
		DescribeTable("refuse a requester the bucket's ACL refuses with 403 AccessDenied",
			func(ctx SpecContext, method string) {
				stored := websiteConf()
				setWebsite(ctx, &stored)
				expectError(send(w.bob, method, bodyOf(""), 0), http.StatusForbidden, "AccessDenied", "")
				Expect(w.bucket(ctx).Info.Website).To(Equal(&stored))
			},
			Entry("GET", http.MethodGet),
			Entry("DELETE", http.MethodDelete),
		)

		DescribeTable("PUT refuses an unauthorized requester before it reads the body, whose refusal an authorized one gets",
			func(ctx SpecContext, doc string, length int64, status int, code string) {
				options("rgw_max_put_param_size", "16")
				before := w.bucket(ctx).Version
				body := bodyOf(doc)
				expectError(send(w.bob, http.MethodPut, body, length), http.StatusForbidden, "AccessDenied", "")
				Expect(body.calls).To(BeZero(), "get_params runs in execute, after verify_permission")
				expectError(send(w.alice, http.MethodPut, bodyOf(doc), length), status, code, "")
				Expect(w.bucket(ctx).Version).To(Equal(before), "nothing written")
			},
			Entry("a document that does not parse", "<Website", int64(8), http.StatusBadRequest, "MalformedXML"),
			Entry("a Content-Length past rgw_max_put_param_size", websiteDoc, int64(len(websiteDoc)),
				http.StatusRequestedRangeNotSatisfiable, "InvalidRange"),
			Entry("a chunked body past rgw_max_put_param_size", websiteDoc+strings.Repeat(" ", 8192), int64(-1),
				http.StatusRequestedRangeNotSatisfiable, "InvalidRange"),
			Entry("no Content-Length", "", int64(0), http.StatusLengthRequired, "MissingContentLength"),
		)
		It("PUT refuses an authorized requester's Content-Length past rgw_max_put_param_size unread, as rgw_rest_read_all_input refuses it before recv_body",
			func(ctx SpecContext) {
				options("rgw_max_put_param_size", "16")
				before := w.bucket(ctx).Version
				body := bodyOf(websiteDoc)
				expectError(send(w.alice, http.MethodPut, body, int64(len(websiteDoc))), http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "")
				Expect(body.calls).To(BeZero(), "rgw_rest.cc:1555-1558 at v19.2.7 and v20.2.4")
				Expect(w.bucket(ctx).Version).To(Equal(before))
			})
		It("PUT to a missing bucket answers 404 NoSuchBucket before it reads the body", func() {
			body := bodyOf(websiteDoc)
			req := httptest.NewRequestWithContext(GinkgoT().Context(), http.MethodPut, "/missing?website", body)
			rec := httptest.NewRecorder()
			s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			Expect(body.calls).To(BeZero())
		})
		DescribeTable("PUT answers a document website.ParseXML refuses with its S3 error and radosgw's message, writing nothing",
			func(ctx SpecContext, doc, code, message string) {
				before := w.bucket(ctx).Version
				expectError(send(w.alice, http.MethodPut, bodyOf(doc), int64(len(doc))), http.StatusBadRequest, code, message)
				Expect(w.bucket(ctx).Version).To(Equal(before))
			},
			Entry("one outside S3's schema", strings.Replace(websiteDoc, "<Condition>", "<Conditions>", 1), "MalformedXML", ""),
			Entry("one without an IndexDocument",
				`<WebsiteConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><ErrorDocument><Key>e</Key></ErrorDocument></WebsiteConfiguration>`,
				"InvalidArgument", "A value for IndexDocument Suffix must be provided if RedirectAllRequestsTo is empty"),
			Entry("an HttpRedirectCode outside 301-399, a decode throw without a message",
				strings.Replace(websiteDoc, ">301<", ">200<", 1), "InvalidArgument", ""),
		)
		DescribeTable("PUT holds the routing rules to rgw_website_routing_rules_max_num, read as get_params reads it into an int",
			func(ctx SpecContext, limit string, rules, admitted int) {
				if limit != "" {
					options("rgw_website_routing_rules_max_num", limit)
				}
				doc := rulesDoc(rules)
				rec := send(w.alice, http.MethodPut, bodyOf(doc), int64(len(doc)))
				if admitted >= rules {
					expectEmptyXML(rec, http.StatusOK)
					Expect(w.bucket(ctx).Info.Website.RoutingRules).To(HaveLen(rules))
					return
				}
				expectError(rec, http.StatusBadRequest, "InvalidRequest", strconv.Itoa(rules)+
					" routing rules provided, the number of routing rules in a website configuration is limited to "+strconv.Itoa(admitted)+".")
				Expect(w.bucket(ctx).Info.Website).To(BeNil())
			},
			Entry("2 admits 2", "2", 2, 2),
			Entry("2 refuses 3", "2", 3, 2),
			Entry("0 refuses 1", "0", 1, 0),
			Entry("unset, its default 50, refuses 51", "", 51, 50),
			Entry("a negative one is 50, which admits 50", "-1", 50, 50),
			Entry("a negative one is 50, which refuses 51", "-1", 51, 50),
			Entry("2^32+2 keeps its low 32 bits, 2", "4294967298", 3, 2),
			Entry("2^31 keeps its low 32 bits, a negative int, so 50", "2147483648", 51, 50),
		)
	})
})
