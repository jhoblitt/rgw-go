package s3_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

const deleteResultOpen = `<?xml version="1.0" encoding="UTF-8"?><DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`

// deleteDoc is a Delete document of objects, each an Object's inner XML.
func deleteDoc(objects ...string) string {
	return "<Delete><Object>" + strings.Join(objects, "</Object><Object>") + "</Object></Delete>"
}

// expectStatusLine checks rec is status alone, as send_status sends it: none
// of end_header's headers and no document (rgw_rest_s3.cc:4247-4255).
func expectStatusLine(rec *httptest.ResponseRecorder, status int) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(status), rec.Body.String())
	Expect(rec.Body.String()).To(BeEmpty())
	for _, h := range []string{"Content-Type", "X-Amz-Request-Id", "Server", "Accept-Ranges", "Content-Length"} {
		Expect(rec.Header()).NotTo(HaveKey(h))
	}
}

var _ = Describe("multi_object_delete", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var w *writeWorld
			BeforeEach(func(ctx SpecContext) {
				w = newWriteWorld(ctx, rel)
				w.put(ctx, "b&c", "x", nil)
				w.conf["rgw_multi_obj_del_max_aio"] = "1"
			})
			post := func(body string, hdr ...string) *httptest.ResponseRecorder {
				return w.send(w.alice, http.MethodPost, "/plain?delete", body, hdr...)
			}

			It("streams a DeleteResult with each key's element, missing keys included, as application/xml without a length", func(ctx SpecContext) {
				rec := post(deleteDoc("<Key>src</Key>", "<Key>b&amp;c</Key>", "<Key>missing</Key>"))
				Expect(rec.Code).To(Equal(200))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
				Expect(rec.Header()).NotTo(HaveKey("Content-Length"))
				Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
				Expect(rec.Header().Get("X-Amz-Request-Id")).NotTo(BeEmpty())
				Expect(rec.Body.String()).To(Equal(deleteResultOpen +
					"<Deleted><Key>src</Key></Deleted><Deleted><Key>b&amp;c</Key></Deleted><Deleted><Key>missing</Key></Deleted></DeleteResult>"))
				Expect(w.stat(ctx, "src").Exists).To(BeFalse())
				Expect(w.stat(ctx, "b&c").Exists).To(BeFalse())
			})
			It("hides successes under Quiet and still reports a failure", func(ctx SpecContext) {
				rec := post("<Delete><Quiet>TRUE</Quiet><Object><Key>src</Key><ETag>\"wrong\"</ETag></Object><Object><Key>b&amp;c</Key></Object></Delete>")
				Expect(rec.Code).To(Equal(200))
				Expect(rec.Body.String()).To(Equal(deleteResultOpen +
					"<Error><Key>src</Key><VersionId></VersionId><Code>PreconditionFailed</Code><Message>PreconditionFailed</Message></Error></DeleteResult>"))
				Expect(w.stat(ctx, "src").Exists).To(BeTrue(), "a key whose ETag condition fails is kept, on both releases (docs/exclusions.md)")
				Expect(w.stat(ctx, "b&c").Exists).To(BeFalse())
			})
			It("deletes under an ETag naming the object's", func(ctx SpecContext) {
				Expect(post(deleteDoc(`<Key>src</Key><ETag>"` + md5Hex("hello") + `"</ETag>`)).Code).To(Equal(200))
				Expect(w.stat(ctx, "src").Exists).To(BeFalse())
			})
			It("takes an ETag element present and empty as a condition, which fails", func(ctx SpecContext) {
				Expect(post(deleteDoc("<Key>src</Key><ETag></ETag>")).Body.String()).To(ContainSubstring("<Code>PreconditionFailed</Code>"),
					"rgw_multi_del.cc:38-40 at v20.2.4 sets if_match for any ETag element")
				Expect(w.stat(ctx, "src").Exists).To(BeTrue())
			})
			It("answers a key the requester may not delete with AccessDenied in its element", func(ctx SpecContext) {
				rec := w.send(w.bob, http.MethodPost, "/plain?delete", deleteDoc("<Key>src</Key>"))
				Expect(rec.Code).To(Equal(200))
				Expect(rec.Body.String()).To(Equal(deleteResultOpen +
					"<Error><Key>src</Key><VersionId></VersionId><Code>AccessDenied</Code><Message>AccessDenied</Message></Error></DeleteResult>"))
				Expect(w.stat(ctx, "src").Exists).To(BeTrue())
			})
			DescribeTable("answers a document the parser fails with the status line alone and deletes nothing",
				func(ctx SpecContext, body string) {
					expectStatusLine(post(body), 400)
					Expect(w.stat(ctx, "src").Exists).To(BeTrue())
				},
				Entry("a missing Key", "<Delete><Object><Key>src</Key></Object><Object><VersionId>v</VersionId></Object></Delete>"),
				Entry("an empty Key", deleteDoc("<Key>src</Key>", "<Key></Key>")),
				Entry("an Object without a Key anywhere in the document", "<Delete><Quiet><Object/></Quiet><Object><Key>src</Key></Object></Delete>"),
				Entry("a document that is not well-formed", "<Delete><Object><Key>src</Key>"),
				Entry("a root other than Delete", "<Remove><Object><Key>src</Key></Object></Remove>"),
				Entry("a LastModifiedTime that parse_time refuses", deleteDoc("<Key>src</Key><LastModifiedTime>yesterday</LastModifiedTime>")),
				Entry("an empty LastModifiedTime", deleteDoc("<Key>src</Key><LastModifiedTime></LastModifiedTime>")),
				Entry("a Size that is no integer", deleteDoc("<Key>src</Key><Size>5 bytes</Size>")),
			)
			It("answers more keys than rgw_delete_multi_obj_max_num with the status line alone", func(ctx SpecContext) {
				objects := make([]string, 1001)
				for i := range objects {
					objects[i] = "<Key>src</Key>"
				}
				expectStatusLine(post(deleteDoc(objects...)), 400)
				Expect(w.stat(ctx, "src").Exists).To(BeTrue())
			})
			It("answers an empty body with the status line alone", func() {
				expectStatusLine(post("", "Content-Length", "0"), 400)
			})
			It("answers a key naming a version with the status line alone until versioning is served", func(ctx SpecContext) {
				expectStatusLine(post(deleteDoc("<Key>src</Key><VersionId>v1</VersionId>")), 501)
				Expect(w.stat(ctx, "src").Exists).To(BeTrue())
			})
			It("answers a missing bucket with the error document before reading the body", func() {
				expectError(w.send(w.alice, http.MethodPost, "/nope?delete", "<Delete><Object>"), 404, "NoSuchBucket")
			})
			It("answers a body past rgw_max_put_param_size with InvalidRange and the error document", func(ctx SpecContext) {
				w.conf["rgw_max_put_param_size"] = "16"
				expectError(post(deleteDoc("<Key>src</Key>")), 416, "InvalidRange")
				Expect(w.stat(ctx, "src").Exists).To(BeTrue())
			})
			It("answers a body with neither a length nor chunking with MissingContentLength and the error document", func() {
				expectError(post(""), 411, "MissingContentLength")
			})
			It("reads a chunked body, as read_all_input does whatever its caller asks", func(ctx SpecContext) {
				rec := serveReq(w.handler(w.alice), http.MethodPost, "/plain?delete", io.MultiReader(strings.NewReader(deleteDoc("<Key>src</Key>"))))
				Expect(rec.Code).To(Equal(200))
				Expect(w.stat(ctx, "src").Exists).To(BeFalse())
			})
			It("refuses a body whose payload hash does not match with the error document and deletes nothing", func(ctx SpecContext) {
				expectError(w.sendTampered(http.MethodPost, "/plain?delete", deleteDoc("<Key>src</Key>")), 400, "XAmzContentSHA256Mismatch")
				Expect(w.stat(ctx, "src").Exists).To(BeTrue())
			})
		})
	}
	It("answers an empty Delete with an empty DeleteResult on Squid", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Squid)
		rec := w.send(w.alice, http.MethodPost, "/plain?delete", "<Delete/>")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.String()).To(Equal(deleteResultOpen + "</DeleteResult>"))
	})
	It("answers an empty Delete with the status line alone on Tentacle, which wants an Object", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		expectStatusLine(w.send(w.alice, http.MethodPost, "/plain?delete", "<Delete/>"), 400)
	})
	It("names no version for an empty VersionId", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Squid)
		Expect(w.send(w.alice, http.MethodPost, "/plain?delete", deleteDoc("<Key>src</Key><VersionId></VersionId>")).Body.String()).
			To(Equal(deleteResultOpen+"<Deleted><Key>src</Key></Deleted></DeleteResult>"), "an empty VersionId names no version")
	})
})

var _ = Describe("parseDelete", func() {
	DescribeTable("fails a document as each release's execute answers it",
		func(body string, squid, tentacle error, tentacleMsg string) {
			_, _, err := s3.ParseDeleteForTest([]byte(body), denc.Squid)
			Expect(err).To(MatchError(squid), "-EINVAL, rgw_op.cc:7025-7039 at v19.2.6")
			Expect(op.AsError(err).Message).To(BeEmpty())
			_, _, err = s3.ParseDeleteForTest([]byte(body), denc.Tentacle)
			Expect(err).To(MatchError(tentacle))
			Expect(op.AsError(err).Message).To(Equal(tentacleMsg), "v20.2.4 rgw_op.cc:7935-7946")
		},
		Entry("a document that does not parse", "<Delete>", op.ErrInvalidArgument, op.ErrMalformedXML, "Failed to parse xml input"),
		Entry("an Object without a Key", "<Delete><Object/></Delete>", op.ErrInvalidArgument, op.ErrMalformedXML, "Failed to parse xml input"),
		Entry("a root other than Delete", "<Remove/>", op.ErrInvalidArgument, op.ErrMalformedXML, "Missing require element Delete"),
	)
	It("reads the entries, their conditions and Quiet", func() {
		entries, quiet, err := s3.ParseDeleteForTest([]byte("<Delete><Quiet>true</Quiet><Object><Key>k</Key><VersionId>v</VersionId>"+
			"<ETag>e</ETag><Size>5</Size><LastModifiedTime>Mon, 28 Sep 2026 12:00:00 GMT</LastModifiedTime></Object></Delete>"), denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(quiet).To(BeTrue())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Key).To(Equal(meta.ObjKey{Name: "k", Instance: "v"}))
		Expect(entries[0].IfMatch).To(HaveValue(Equal("e")))
		Expect(entries[0].IfMatchSize).To(HaveValue(BeEquivalentTo(5)))
		Expect(entries[0].IfMatchLastModified).To(Equal(writeMtime))
	})
})
