package s3_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/tags"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

const allowAll = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::plain/*"}]}`

var _ = Describe("bucket policy and tagging routes", func() {
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

	// expectEmpty checks rec is status with no body, no type and the
	// Content-Length: 0 radosgw's frontend completes it with.
	expectEmpty := func(rec *httptest.ResponseRecorder, status int) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Body.String()).To(BeEmpty())
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"), "end_header(s) names no type and the formatter is empty, rgw_rest.cc:611-619 at v19.2.6")
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
	}

	Describe("the policy", func() {
		It("is 404 NoSuchBucketPolicy with radosgw's message before one is put", func() {
			rec := w.send(w.alice, http.MethodGet, "/plain?policy", "")
			Expect(rec.Code).To(Equal(404))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucketPolicy</Code><Message>The bucket policy does not exist</Message>"))
		})
		It("puts with 204, returns the text verbatim as application/json, and deletes with 204", func(ctx SpecContext) {
			expectEmpty(w.send(w.alice, http.MethodPut, "/plain?policy", allowAll), 204)
			rec := w.send(w.alice, http.MethodGet, "/plain?policy", "")
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"), "rgw_op.cc:8129 at v19.2.6")
			Expect(rec.Body.String()).To(Equal(allowAll))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(allowAll))))
			Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
			expectEmpty(w.send(w.alice, http.MethodDelete, "/plain?policy", ""), 204)
			Expect(w.send(w.alice, http.MethodGet, "/plain?policy", "").Code).To(Equal(404))
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
		})
		It("answers a policy that does not parse 400 InvalidArgument with the parser's message", func(ctx SpecContext) {
			const bad = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:NoSuchAction","Resource":"*"}]}`
			tenant := ""
			_, perr := policy.Parse(bad, policy.ParseOptions{Tenant: &tenant, Release: denc.Squid})
			pe, ok := errors.AsType[*policy.ParseError](perr)
			Expect(ok).To(BeTrue(), "%v", perr)
			rec := w.send(w.alice, http.MethodPut, "/plain?policy", bad)
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code><Message>"+xmltext.Escape(pe.Error())+"</Message>"),
				"rgw_op.cc:8116-8120 at v19.2.6")
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
		})
		It("refuses a public policy under a block of public policies", func(ctx SpecContext) {
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicPolicy: true})})
			rec := w.send(w.alice, http.MethodPut, "/plain?policy", allowAll)
			Expect(rec.Code).To(Equal(403), "rgw_op.cc:8103-8108 at v19.2.6")
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
		})
		It("refuses a body past rgw_max_put_param_size as 416 InvalidRange", func(ctx SpecContext) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			rec := w.send(w.alice, http.MethodPut, "/plain?policy", allowAll)
			Expect(rec.Code).To(Equal(416), "read_all_input's -ERANGE, rgw_rest.cc:1551-1552 at v19.2.6")
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRange</Code>"))
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
		})
		It("refuses a request without a Content-Length with MissingContentLength", func() {
			Expect(w.send(w.alice, http.MethodPut, "/plain?policy", "").Code).To(Equal(411), "rgw_rest.cc:1566-1569 at v19.2.6")
		})
		It("takes a chunked body at the default limit up to the end of the buffer that first passes it", func(ctx SpecContext) {
			chunked := func(n int) *httptest.ResponseRecorder {
				return serveReq(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})), http.MethodPut, "/plain?policy",
					io.MultiReader(strings.NewReader(allowAll+strings.Repeat(" ", n-len(allowAll)))))
			}
			// Buffers of 4 KiB doubling to 128 KiB sum to 1044480 bytes, under
			// 1048576, and to 1175552 with the next 128 KiB one.
			rec := chunked(1175552)
			Expect(rec.Code).To(Equal(416), "that buffer fills past the limit, rgw_rest.cc:1521-1527 at v19.2.6")
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
			expectEmpty(chunked(1175551), 204)
		})
		It("reads a chunked policy whole, bounded by read_all_chunked_input's buffers", func(ctx SpecContext) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			chunked := func(n int) *httptest.ResponseRecorder {
				return serveReq(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})), http.MethodPut, "/plain?policy",
					io.MultiReader(strings.NewReader(allowAll+strings.Repeat(" ", n-len(allowAll)))))
			}
			rec := chunked(4096)
			Expect(rec.Code).To(Equal(416), "a full 4096-byte buffer past the limit, rgw_rest.cc:1526-1532 at v20.2.4")
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
			expectEmpty(chunked(4095), 204)
			Expect(w.bucket(ctx).Attrs).To(HaveKey(op.AttrIAMPolicy))
		})
		It("refuses a body whose payload hash does not match, storing nothing", func(ctx SpecContext) {
			rec := w.sendTampered("/plain?policy", allowAll)
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>XAmzContentSHA256Mismatch</Code>"))
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
		})
		It("refuses a requester the bucket's ACL refuses, before it reads the body", func(ctx SpecContext) {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			Expect(w.send(w.bob, http.MethodPut, "/plain?policy", allowAll).Code).To(Equal(403))
			Expect(w.send(w.bob, http.MethodGet, "/plain?policy", "").Code).To(Equal(403))
			Expect(w.send(w.bob, http.MethodDelete, "/plain?policy", "").Code).To(Equal(403))
		})
	})

	Describe("the tag set", func() {
		const doc = `<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`
		tagging := func(n int) string {
			var b strings.Builder
			b.WriteString("<Tagging><TagSet>")
			for i := range n {
				fmt.Fprintf(&b, "<Tag><Key>k%d</Key><Value>v</Value></Tag>", i)
			}
			b.WriteString("</TagSet></Tagging>")
			return b.String()
		}

		It("is 404 NoSuchTagSet before one is put", func() {
			rec := w.send(w.alice, http.MethodGet, "/plain?tagging", "")
			Expect(rec.Code).To(Equal(404))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchTagSet</Code>"))
		})
		It("puts with 200 and no body, returns the set, and deletes with 204", func(ctx SpecContext) {
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?tagging", doc), 200)
			rec := w.send(w.alice, http.MethodGet, "/plain?tagging", "")
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
			Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
			Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
			expectEmptyXML(w.send(w.alice, http.MethodDelete, "/plain?tagging", ""), 204)
			Expect(w.send(w.alice, http.MethodGet, "/plain?tagging", "").Code).To(Equal(404))
			Expect(w.bucket(ctx).Attrs).NotTo(HaveKey(tags.Attr))
		})
		It("takes fifty tags and refuses fifty-one with InvalidTag", func(ctx SpecContext) {
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?tagging", tagging(50)), 200)
			rec := w.send(w.alice, http.MethodPut, "/plain?tagging", tagging(51))
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidTag</Code>"), "RGWObjTags(50), rgw_rest_s3.cc:898 at v19.2.6")
			Expect(w.send(w.alice, http.MethodGet, "/plain?tagging", "").Body.String()).To(ContainSubstring("<Key>k49</Key>"), "the fifty stay")
		})
		It("refuses a document without a TagSet with MalformedXML", func() {
			rec := w.send(w.alice, http.MethodPut, "/plain?tagging", "<Tagging></Tagging>")
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>MalformedXML</Code>"))
		})
		It("refuses a body past rgw_max_put_param_size as 416 InvalidRange", func() {
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			Expect(w.send(w.alice, http.MethodPut, "/plain?tagging", doc).Code).To(Equal(416))
		})
		It("refuses a body whose payload hash does not match, keeping the set", func(ctx SpecContext) {
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?tagging", doc), 200)
			before := w.bucket(ctx).Attrs[tags.Attr]
			rec := w.sendTampered("/plain?tagging", "<Tagging><TagSet></TagSet></Tagging>")
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>XAmzContentSHA256Mismatch</Code>"))
			Expect(w.bucket(ctx).Attrs[tags.Attr]).To(Equal(before))
		})
		It("refuses a requester the bucket's ACL refuses", func() {
			Expect(w.send(w.bob, http.MethodPut, "/plain?tagging", doc).Code).To(Equal(403))
			Expect(w.send(w.bob, http.MethodGet, "/plain?tagging", "").Code).To(Equal(403))
			Expect(w.send(w.bob, http.MethodDelete, "/plain?tagging", "").Code).To(Equal(403))
		})
	})
})
