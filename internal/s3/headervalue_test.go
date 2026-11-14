package s3_test

import (
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// radosgw writes a header value's bytes as they are
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_client.cc#L167-L181 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_client.cc#L167-L181 at v20.2.4),
// so a NUL or another control byte an object's attr holds reaches the wire.
// rgw-go writes each byte below 0x20 but HTAB, and 0x7f, as a space.
var _ = Describe("a stored value's control bytes in a response header", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Context("on "+rel.String(), func() {
			var (
				w *readWorld
				h *s3.Handler
			)
			BeforeEach(func(ctx SpecContext) {
				w = newReadWorld(ctx, rel)
				h = w.handler(w.env(nil), w.alice)
			})

			DescribeTable("GET and HEAD send each such byte as a space",
				func(ctx SpecContext, attr, stored, header, want string) {
					w.setAttrs(ctx, "small", map[string][]byte{attr: []byte(stored)})
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						rec := send(h, method, "/plain/small", nil)
						Expect(rec.Code).To(Equal(http.StatusOK), method)
						Expect(rec.Header()[header]).To(Equal([]string{want}), "%s of %q", method, stored)
					}
				},
				Entry("user metadata's interior NUL", meta.AttrMetaPrefix+"color", "a\x00b\x00", "x-amz-meta-color", "a b"),
				Entry("user metadata's other control bytes", meta.AttrMetaPrefix+"color", "\x01a\x1fb\x7fc\r\nd\x00", "x-amz-meta-color", " a b c  d"),
				Entry("user metadata's tab and bytes above 0x7f, unchanged", meta.AttrMetaPrefix+"color", "a\tb\xc3\xa9\xff\x00", "x-amz-meta-color", "a\tb\xc3\xa9\xff"),
				Entry("Content-Disposition's interior NUL", meta.AttrContentDisp, "attachment\x00x\x00", "Content-Disposition", "attachment x"),
				Entry("Content-Disposition's other control bytes", meta.AttrContentDisp, "attachment;\x0bfilename=a\x1b", "Content-Disposition", "attachment; filename=a "),
				Entry("Cache-Control's interior NUL", meta.AttrCacheControl, "no-cache\x00private", "Cache-Control", "no-cache private"),
				Entry("Cache-Control's other control bytes", meta.AttrCacheControl, "no-cache\x08private", "Cache-Control", "no-cache private"),
				Entry("Content-Encoding's interior NUL, once aws-chunked is gone", meta.AttrContentEnc, "gzip\x00x, aws-chunked\x00", "Content-Encoding", "gzip x"),
				Entry("Content-Encoding's other control bytes, which keep a token from being aws-chunked",
					meta.AttrContentEnc, "gzip, aws-chunked\x01", "Content-Encoding", "gzip, aws-chunked "),
				Entry("Content-Language's interior NUL", meta.AttrContentLang, "en\x00fr", "Content-Language", "en fr"),
				Entry("Content-Language's other control bytes", meta.AttrContentLang, "en\x02fr", "Content-Language", "en fr"),
				Entry("Expires' interior NUL", meta.AttrExpires, "Thu, 01 Oct 2026\x0000:00:00 GMT", "Expires", "Thu, 01 Oct 2026 00:00:00 GMT"),
				Entry("Expires' other control bytes", meta.AttrExpires, "Thu, 01 Oct 2026\x0c00:00:00 GMT", "Expires", "Thu, 01 Oct 2026 00:00:00 GMT"),
				Entry("X-Robots-Tag's interior NUL", meta.AttrXRobotsTag, "noindex\x00nofollow", "X-Robots-Tag", "noindex nofollow"),
				Entry("X-Robots-Tag's other control bytes", meta.AttrXRobotsTag, "noindex\x7fnofollow", "X-Robots-Tag", "noindex nofollow"),
				Entry("X-Amz-Storage-Class' interior NUL", meta.AttrStorageClass, "COLD\x00x", "X-Amz-Storage-Class", "COLD x"),
				Entry("X-Amz-Storage-Class' other control bytes", meta.AttrStorageClass, "COLD\x1ex", "X-Amz-Storage-Class", "COLD x"),
				Entry("x-amz-website-redirect-location's interior NUL", meta.AttrWebsiteRedirect, "/a\x00b", "x-amz-website-redirect-location", "/a b"),
				Entry("x-amz-website-redirect-location's other control bytes", meta.AttrWebsiteRedirect, "/a\x03b", "x-amz-website-redirect-location", "/a b"),
				Entry("Content-Type, ended at its first NUL as end_header reads a C string",
					meta.AttrContentType, "text/plain\x00\x01x", "Content-Type", "text/plain"),
				Entry("Content-Type's other control bytes", meta.AttrContentType, "text/\x01plain\x7f", "Content-Type", "text/ plain "),
				Entry("the ETag, ended at its first NUL as dump_header_quoted's snprintf copies it", meta.AttrETag, "abcd\x00\x01x", "ETag", `"abcd"`),
				Entry("the ETag's other control bytes", meta.AttrETag, "ab\x01cd", "ETag", `"ab cd"`),
				Entry("x-amz-replication-status' interior NUL", meta.AttrReplicationStatus, "COMPLETED\x00x\x00", "x-amz-replication-status", "COMPLETED x"),
				Entry("x-amz-replication-status' other control bytes", meta.AttrReplicationStatus, "COMPLETED\x15x\x00", "x-amz-replication-status", "COMPLETED x"),
			)

			It("sends an attr rgw_extended_http_attrs maps with each such byte as a space", func(ctx SpecContext) {
				w.setAttrs(ctx, "small", map[string][]byte{meta.AttrPrefix + "x_custom_header": []byte("v\x00w\x01x\x00")})
				env := w.env(nil)
				env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_extended_http_attrs": "x-custom-header"})
				eh := w.handler(env, w.alice)
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					Expect(send(eh, method, "/plain/small", nil).Header()).To(HaveKeyWithValue("X-Custom-Header", []string{"v w x"}), method)
				}
			})

			It("sends each zone of the replication trace with each such byte as a space", func(ctx SpecContext) {
				trace := denc.NewEncoder()
				denc.EncodeSlice(trace, []string{"zone\x00a", "zone\x01b"}, func(e *denc.Encoder, s string) { e.String(s) })
				w.setAttrs(ctx, "small", map[string][]byte{meta.AttrReplicationTrace: trace.Bytes()})
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					Expect(send(h, method, "/plain/small", nil).Header()).To(HaveKeyWithValue("x-rgw-replicated-from", []string{"zone a", "zone b"}), method)
				}
			})

			It("sends a 304's Cache-Control, Expires and ETag with each such byte as a space", func(ctx SpecContext) {
				w.setAttrs(ctx, "small", map[string][]byte{
					meta.AttrCacheControl: []byte("no\x00store\x01\x00"),
					meta.AttrExpires:      []byte("Thu,\x0001 Oct\x7f2026\x00"),
					meta.AttrETag:         []byte("ab\x01cd\x00ef"),
				})
				rec := send(h, http.MethodGet, "/plain/small", map[string]string{"If-Modified-Since": "Mon, 28 Sep 2026 00:00:00 GMT"})
				Expect(rec.Code).To(Equal(http.StatusNotModified))
				Expect(rec.Header()).To(HaveKeyWithValue("Cache-Control", []string{"no store "}))
				Expect(rec.Header()).To(HaveKeyWithValue("Expires", []string{"Thu, 01 Oct 2026"}))
				Expect(rec.Header()).To(HaveKeyWithValue("ETag", []string{`"ab cd"`}))
			})

			// format_xattr re-encodes a value holding a control byte but not one
			// holding only a NUL, which is_control_character excludes
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L2145-L2164 at v19.2.7,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L2308-L2327 at v20.2.4).
			It("stores a query-string metadata value's NUL as radosgw does, and sends it as a space", func(ctx SpecContext) {
				ph := s3.NewHandler(w.env(nil), authAsOn(w.alice, op.RouteSigV4Header), testConfig(s3.Config{}))
				put := serveReq(ph, http.MethodPut, "/plain/q?x-amz-meta-a=b%00c", strings.NewReader("data"))
				Expect(put.Code).To(Equal(http.StatusOK), put.Body.String())
				st, err := w.store.StatObject(ctx, w.plain, meta.ObjKey{Name: "q"})
				Expect(err).NotTo(HaveOccurred())
				Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"a", []byte("b\x00c\x00")))
				Expect(send(ph, http.MethodGet, "/plain/q", nil).Header()).To(HaveKeyWithValue("x-amz-meta-a", []string{"b c"}))
			})

			It("puts no NUL on the wire, where net/http rewrites only CR and LF", func(ctx SpecContext) {
				w.setAttrs(ctx, "small", map[string][]byte{meta.AttrMetaPrefix + "color": []byte("a\x00b\x01c\x00")})
				lines := wireHeaders(ctx, h, http.MethodGet, "/plain/small")
				Expect(lines).To(ContainElement("x-amz-meta-color: a b c"))
				for _, l := range lines {
					Expect(strings.IndexFunc(l, func(c rune) bool { return c < 0x20 && c != '\t' || c == 0x7f })).To(Equal(-1), "%q", l)
				}
			})
		})
	}

	It("sends Tentacle's x-rgw-bucket-placement-target with each such byte as a space", func(ctx SpecContext) {
		w := newReadWorld(ctx, denc.Tentacle)
		w.plain.Info.PlacementRule.Name = "default\x00place\x01ment"
		Expect(w.store.PutBucketInfo(ctx, w.plain)).To(Succeed())
		rec := send(w.handler(w.env(nil), w.alice), http.MethodGet, "/plain?location", nil)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Header()).To(HaveKeyWithValue("x-rgw-bucket-placement-target", []string{"default place ment"}))
	})

	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		It("sends the website endpoint's Location of an object's redirect to its first NUL, each other such byte a space, on "+rel.String(),
			func(ctx SpecContext) {
				w := newWebsiteWorld(ctx, rel)
				w.setWebsite(ctx, &meta.WebsiteConf{IndexDocSuffix: "index.html"})
				w.put(ctx, "moved", "body", map[string][]byte{meta.AttrWebsiteRedirect: []byte("/a\x01b\x7fc\x00d\x00")})
				rec := w.serve(nil, http.MethodGet, "/moved")
				Expect(rec.Code).To(Equal(http.StatusMovedPermanently))
				Expect(rec.Header()).To(HaveKeyWithValue("Location", []string{"/a b c"}))
			})
	}
})

// A version's name holds a control byte only where a RADOS writer put one.
// A NUL is reached through the OLH alone, as refuseNUL refuses one in
// versionId.
var _ = Describe("a stored version id's control bytes in a response header", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Context("on "+rel.String(), func() {
			var w *writeWorld
			BeforeEach(func(ctx SpecContext) {
				w = newWriteWorld(ctx, rel)
				w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned })
				w.putVersion(ctx, "k", "i\x00d", "nul")
				w.putVersion(ctx, "k", "i\x01d", "ctl")
				w.env.Objects = versions{ObjectStore: w.store, current: map[string]string{"k": "i\x00d"}, markers: map[meta.ObjKey]bool{}}
			})
			DescribeTable("each read and copy sends a space in place of each such byte",
				func(method, target, header string, hdr ...string) {
					rec := w.send(w.alice, method, target, "", hdr...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).To(HaveKeyWithValue(header, []string{"i d"}))
				},
				Entry("GET of the current version, whose id holds a NUL", http.MethodGet, "/plain/k", "x-amz-version-id"),
				Entry("GET of a version named, whose id holds another control byte", http.MethodGet, "/plain/k?versionId=i%01d", "x-amz-version-id"),
				Entry("HEAD of the current version, whose id holds a NUL", http.MethodHead, "/plain/k", "x-amz-version-id"),
				Entry("HEAD of a version named, whose id holds another control byte", http.MethodHead, "/plain/k?versionId=i%01d", "x-amz-version-id"),
				Entry("GetObjectAttributes of the current version, whose id holds a NUL",
					http.MethodGet, "/plain/k?attributes", "x-amz-version-id", "X-Amz-Object-Attributes", "ETag"),
				Entry("GetObjectAttributes of a version named, whose id holds another control byte",
					http.MethodGet, "/plain/k?attributes&versionId=i%01d", "x-amz-version-id", "X-Amz-Object-Attributes", "ETag"),
				Entry("CopyObject from the current version, whose id holds a NUL",
					http.MethodPut, "/plain/dst", "x-amz-copy-source-version-id", "X-Amz-Copy-Source", "/plain/k"),
				Entry("CopyObject from a version named, whose id holds another control byte",
					http.MethodPut, "/plain/dst", "x-amz-copy-source-version-id", "X-Amz-Copy-Source", "/plain/k?versionId=i%01d"),
			)
		})
	}
})

var _ = Describe("SetHeader", func() {
	// RFC 9110's field-content is field-vchar (VCHAR %x21-7E and obs-text
	// %x80-FF) with SP and HTAB between them (section 5.5).
	It("keeps each byte a field value may hold and writes every other as a space", func() {
		for c := range 256 {
			h := http.Header{}
			s3.SetHeader(h, "x-test", "a"+string([]byte{byte(c)})+"z")
			want := []byte{'a', ' ', 'z'}
			if c == '\t' || 0x20 <= c && c <= 0x7e || 0x80 <= c {
				want[1] = byte(c)
			}
			Expect(h).To(HaveKeyWithValue("x-test", []string{string(want)}), "byte %#02x", c)
		}
	})
	It("leaves a value holding no other byte as it is", func() {
		h := http.Header{}
		v := "attachment; filename=\"r\xc3\xa9sum\xc3\xa9.pdf\"\t"
		s3.SetHeader(h, "Content-Disposition", v)
		Expect(h["Content-Disposition"]).To(Equal([]string{v}))
	})
})
