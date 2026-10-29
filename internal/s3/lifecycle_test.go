package s3_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// lcDocument is a LifecycleConfiguration document in S3's namespace holding
// rules.
func lcDocument(rules ...string) string {
	return `<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` + strings.Join(rules, "") + `</LifecycleConfiguration>`
}

// lcEncoded is c as radosgw stores it under user.rgw.lc.
func lcEncoded(c lifecycle.Configuration) []byte {
	e := denc.NewEncoder()
	c.Encode(e, denc.Squid)
	return e.Bytes()
}

// The first rule of s3-tests' test_lifecycle_set, as botocore 1.43.90
// writes it, and its configuration.
const lcSetRule = `<Rule><Expiration><Days>1</Days></Expiration><ID>rule1</ID><Prefix>test1/</Prefix><Status>Enabled</Status></Rule>`

func lcSetConfig() lifecycle.Configuration {
	return lifecycle.Configuration{Rules: []lifecycle.Rule{{
		ID: "rule1", Prefix: "test1/", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"},
	}}}
}

var _ = Describe("bucket lifecycle routes", func() {
	const target = "/plain?lifecycle"
	var (
		w  *subresWorld
		lc *opfakes.FakeLifecycleStore
	)
	// useWorld runs the specs on a fresh world at rel, whose lifecycle store
	// succeeds.
	useWorld := func(ctx context.Context, rel denc.Release) {
		w = newSubresWorld(ctx, rel)
		lc = &opfakes.FakeLifecycleStore{}
		w.env.Lifecycle = lc
	}
	BeforeEach(func(ctx SpecContext) { useWorld(ctx, denc.Squid) })

	// errorDocument is the document end_header's error branch sends for rec.
	errorDocument := func(rec *httptest.ResponseRecorder, code, message string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>` + message + `</Message>` +
			`<BucketName>plain</BucketName><RequestId>` + exactHeader(rec.Header(), "x-amz-request-id") +
			`</RequestId><HostId>4155-z-zg</HostId></Error>`
	}
	expectLCError := func(rec *httptest.ResponseRecorder, status int, code, message string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Body.String()).To(Equal(errorDocument(rec, code, message)))
	}
	unchanged := func(ctx context.Context, before meta.ObjVersion) {
		GinkgoHelper()
		Expect(w.bucket(ctx).Version).To(Equal(before), "nothing written")
		Expect(lc.Invocations()).To(BeEmpty(), "no entry set or removed")
	}

	It("binds the three routes in every handler", func() {
		h := newHandler(w.store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements("bucket/get_lifecycle", "bucket/put_lifecycle", "bucket/delete_lifecycle"))
	})

	Describe("GET", func() {
		// expectDocument checks rec is 200 with doc after the XML
		// declaration, as application/xml, with the length the frontend adds.
		expectDocument := func(rec *httptest.ResponseRecorder, doc string) {
			GinkgoHelper()
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), "to_mime_type, rgw_rest_s3.cc:3718 at v19.2.7")
			want := `<?xml version="1.0" encoding="UTF-8"?>` + doc
			Expect(rec.Body.String()).To(Equal(want), "dump_start, then encode_xml, rgw_rest_s3.cc:3719-3725 at v19.2.7")
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
			Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "the length the frontend adds, not dump_content_length's")
		}

		It("is 404 NoSuchLifecycleConfiguration, in radosgw's error document, for a bucket without one", func() {
			rec := w.send(w.alice, http.MethodGet, target, "")
			expectLCError(rec, http.StatusNotFound, "NoSuchLifecycleConfiguration", "")
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())),
				"ERR_NO_SUCH_LC through end_header's error branch, rgw_rest_s3.cc:3711-3712 and rgw_rest.cc:620-624 at v19.2.7")
		})
		It("renders the stored configuration under LifecycleConfiguration in S3's namespace", func(ctx SpecContext) {
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcEncoded(lcSetConfig())})
			expectDocument(w.send(w.alice, http.MethodGet, target, ""), lcDocument(
				`<Rule><ID>rule1</ID><Prefix>test1/</Prefix><Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>`))
		})
		It("is 404 NoSuchLifecycleConfiguration for a stored configuration without rules, which radosgw renders with none (docs/exclusions.md)",
			func(ctx SpecContext) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcEncoded(lifecycle.Configuration{})})
				expectLCError(w.send(w.alice, http.MethodGet, target, ""), http.StatusNotFound, "NoSuchLifecycleConfiguration", "")
			})
		It("answers a stored configuration that does not decode with 500 UnknownError, radosgw's -EIO", func(ctx SpecContext) {
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: {1}})
			expectLCError(w.send(w.alice, http.MethodGet, target, ""), http.StatusInternalServerError, "UnknownError", "")
		})
		It("refuses a requester the bucket's ACL refuses with 403 AccessDenied, with a configuration or without", func(ctx SpecContext) {
			expectDenied := func(state string) {
				GinkgoHelper()
				rec := w.send(w.bob, http.MethodGet, target, "")
				Expect(rec.Code).To(Equal(http.StatusForbidden), "%s: %s", state, rec.Body.String())
				Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"), state)
			}
			expectDenied("without a configuration")
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcEncoded(lcSetConfig())})
			expectDenied("with a configuration")
		})
	})

	// Rook's bucket provisioner reconciles an OBC's bucketLifecycle by
	// reading the bucket's configuration, treating NoSuchLifecycleConfiguration
	// as none, and putting the OBC's when they differ or deleting the
	// bucket's when the OBC has none (rook v1.21.1,
	// pkg/operator/ceph/object/bucket/provisioner.go:812-887). The bodies are
	// what aws-sdk-go-v2's s3 serializer writes for the rules of Rook's
	// documented example (Documentation/Storage-Configuration/Object-Storage-RGW/ceph-object-bucket-claim.md:57-77),
	// each rule's members in its order (service/s3 v1.98.0,
	// serializers.go:12452-12566).
	It("serves Rook's bucket/lifecycle sequence: read none, put, read back, update, read back, delete, read none", func(ctx SpecContext) {
		const (
			abortRule = `<Rule><AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload>` +
				`<ID>AbortIncompleteMultipartUploads</ID><Prefix></Prefix><Status>Enabled</Status></Rule>`
			expireRule = `<Rule><Expiration><Days>30</Days></Expiration><ID>ExpireAfter30Days</ID><Prefix></Prefix><Status>Enabled</Status></Rule>`
			// The rules as dump_xml writes them back.
			abortDump = `<Rule><ID>AbortIncompleteMultipartUploads</ID><Prefix></Prefix><Status>Enabled</Status>` +
				`<AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>`
			expireDump = `<Rule><ID>ExpireAfter30Days</ID><Prefix></Prefix><Status>Enabled</Status><Expiration><Days>30</Days></Expiration></Rule>`
			expire60   = `<Rule><Expiration><Days>60</Days></Expiration><ID>ExpireAfter30Days</ID><Prefix></Prefix><Status>Enabled</Status></Rule>`
			dump60     = `<Rule><ID>ExpireAfter30Days</ID><Prefix></Prefix><Status>Enabled</Status><Expiration><Days>60</Days></Expiration></Rule>`
		)
		read := func() *httptest.ResponseRecorder { return w.send(w.alice, http.MethodGet, target, "") }
		expectNone := func() {
			GinkgoHelper()
			rec := read()
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchLifecycleConfiguration</Code>"), "the code Rook takes for none")
		}
		expectRules := func(rules ...string) {
			GinkgoHelper()
			rec := read()
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>` + lcDocument(rules...)))
		}

		expectNone()
		expectEmptyXML(w.send(w.alice, http.MethodPut, target, lcDocument(abortRule, expireRule)), http.StatusOK)
		expectRules(abortDump, expireDump)
		expectEmptyXML(w.send(w.alice, http.MethodPut, target, lcDocument(abortRule, expire60)), http.StatusOK)
		expectRules(abortDump, dump60)
		expectEmptyXML(w.send(w.alice, http.MethodDelete, target, ""), http.StatusNoContent)
		expectNone()
		Expect(lc.SetEntryCallCount()).To(Equal(2))
		Expect(lc.RemoveEntryCallCount()).To(Equal(1))
	})

	It("puts with 200 and no body, storing the configuration and then the bucket's entry", func(ctx SpecContext) {
		expectEmptyXML(w.send(w.alice, http.MethodPut, target, lcDocument(lcSetRule)), http.StatusOK)
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrLC, lcEncoded(lcSetConfig())))
		Expect(lc.SetEntryCallCount()).To(Equal(1))
		_, rec := lc.SetEntryArgsForCall(0)
		Expect(rec.Info.Bucket.Name).To(Equal("plain"))
	})

	DescribeTable("deletes with 204 and no body, whether or not the bucket holds a configuration, removing its entry",
		func(ctx SpecContext, stored bool) {
			if stored {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcEncoded(lcSetConfig())})
			}
			expectEmptyXML(w.send(w.alice, http.MethodDelete, target, ""), http.StatusNoContent)
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrLC))
			Expect(lc.RemoveEntryCallCount()).To(Equal(1))
		},
		Entry("with one", true),
		Entry("without one, STATUS_NO_CONTENT all the same, rgw_rest_s3.cc:3737-3747 at v19.2.7", false),
	)

	It("refuses a document whose root is not LifecycleConfiguration with 400 MalformedXML, storing nothing", func(ctx SpecContext) {
		before := w.bucket(ctx).Version
		rec := w.send(w.alice, http.MethodPut, target, `<Tagging>`+lcSetRule+`</Tagging>`)
		expectLCError(rec, http.StatusBadRequest, "MalformedXML", "")
		unchanged(ctx, before)
	})

	It("refuses a body over rgw_max_put_param_size with 416 InvalidRange", func(ctx SpecContext) {
		doc := lcDocument(lcSetRule)
		w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": strconv.Itoa(len(doc) - 1)})
		before := w.bucket(ctx).Version
		rec := w.send(w.alice, http.MethodPut, target, doc)
		Expect(rec.Code).To(Equal(http.StatusRequestedRangeNotSatisfiable), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRange</Code>"))
		unchanged(ctx, before)
	})

	DescribeTable("answers a Content-MD5 it refuses with 400 and its message, storing nothing",
		func(ctx SpecContext, value, code, message string) {
			before := w.bucket(ctx).Version
			rec := w.send(w.alice, http.MethodPut, target, lcDocument(lcSetRule), "Content-MD5", value)
			expectLCError(rec, http.StatusBadRequest, code, message)
			unchanged(ctx, before)
		},
		Entry("another body's, BadDigest", md5B64("other"), "BadDigest", "The Content-MD5 you specified did not match what we received."),
		Entry("one that does not decode, InvalidDigest", "%%%", "InvalidDigest", "Request header Content-MD5 contains character that is not base64 encoded."),
		Entry("all '=', InvalidDigest", "====", "InvalidDigest", ""),
		Entry("one of 17 bytes, InvalidDigest", base64.StdEncoding.EncodeToString(make([]byte, 17)), "InvalidDigest", ""),
	)

	It("takes the body's own Content-MD5", func(ctx SpecContext) {
		doc := lcDocument(lcSetRule)
		expectEmptyXML(w.send(w.alice, http.MethodPut, target, doc, "Content-MD5", md5B64(doc)), http.StatusOK)
		Expect(w.bucket(ctx).Attrs).To(HaveKey(op.AttrLC))
	})

	DescribeTable("refuses an indexless bucket with 405 MethodNotAllowed and Tentacle's message on both releases",
		func(ctx SpecContext, rel denc.Release) {
			useWorld(ctx, rel)
			rec := w.bucket(ctx)
			rec.Info.Layout.Current.Layout.Type = meta.IndexIndexless
			Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
			before := w.bucket(ctx).Version
			resp := w.send(w.alice, http.MethodPut, target, lcDocument(lcSetRule))
			expectLCError(resp, http.StatusMethodNotAllowed, "MethodNotAllowed", "Indexless buckets do not support lifecycle policy")
			unchanged(ctx, before)
		},
		Entry("Squid, whose radosgw stores the configuration (docs/exclusions.md)", denc.Squid),
		Entry("Tentacle, rgw_op.cc:6594-6599 at v20.2.4", denc.Tentacle),
	)

	It("answers 503 ServiceUnavailable when the entry's shard stays locked, keeping the stored configuration", func(ctx SpecContext) {
		lc.SetEntryReturns(&radosclient.Error{Errno: -int32(syscall.EBUSY), Op: "lock"})
		rec := w.send(w.alice, http.MethodPut, target, lcDocument(lcSetRule))
		expectLCError(rec, http.StatusServiceUnavailable, "ServiceUnavailable", "")
		Expect(w.bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrLC, lcEncoded(lcSetConfig())), "set_bucket_config stores before guard_lc_modify, rgw_lc.cc:2642-2663 at v19.2.7")
	})

	DescribeTable("refuses a requester the bucket's ACL refuses with 403 AccessDenied, writing nothing",
		func(ctx SpecContext, method, body string) {
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcEncoded(lcSetConfig())})
			before := w.bucket(ctx).Version
			rec := w.send(w.bob, method, target, body)
			Expect(rec.Code).To(Equal(http.StatusForbidden), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
			unchanged(ctx, before)
		},
		Entry("PUT", http.MethodPut, lcDocument(lcSetRule)),
		Entry("DELETE", http.MethodDelete, ""),
	)

	It("answers 404 NoSuchBucket for a bucket that does not exist", func() {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			rec := w.send(w.alice, method, "/missing?lifecycle", lcDocument(lcSetRule))
			Expect(rec.Code).To(Equal(http.StatusNotFound), method)
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"), method)
		}
	})
})
