package s3_test

import (
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

const copyResultOpen = `<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`

var _ = Describe("copy_obj", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) {
		w = newWriteWorld(ctx, denc.Squid)
		w.put(ctx, "meta", "data", map[string][]byte{meta.AttrMetaPrefix + "old": []byte("v\x00")})
	})
	copyFrom := func(src string, hdr ...string) []string { return append([]string{"X-Amz-Copy-Source", src}, hdr...) }

	It("copies and answers CopyObjectResult as application/xml without a length", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header()).NotTo(HaveKey("Content-Length"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
		Expect(rec.Body.String()).To(Equal(copyResultOpen+"<LastModified>2026-09-28T12:00:00.000Z</LastModified><ETag>&quot;"+
			md5Hex("hello")+"&quot;</ETag></CopyObjectResult>"), "dump_format escapes the quotes it adds, rgw_rest_s3.cc:3598")
		Expect(w.stat(ctx, "dst").ETag).To(Equal(md5Hex("hello")))
	})
	for _, c := range []struct {
		rel  denc.Release
		want string
	}{{denc.Squid, "2026-09-28T12:00:00.456Z"}, {denc.Tentacle, "2026-09-28T12:00:00.000Z"}} {
		It("renders LastModified as "+c.rel.String()+" does", func(ctx SpecContext) {
			w = newWriteWorldAt(ctx, c.rel, writeMtime.Add(456*time.Millisecond))
			rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...)
			Expect(rec.Body.String()).To(ContainSubstring("<LastModified>"+c.want+"</LastModified>"),
				"dump_time on Squid, dump_time_exact_seconds on Tentacle (v20.2.4 rgw_rest_s3.cc:3879)")
		})
	}
	It("keeps the source's metadata, and takes the request's with REPLACE in any case", func(ctx SpecContext) {
		Expect(w.send(w.alice, http.MethodPut, "/plain/kept", "", copyFrom("plain/meta", "X-Amz-Meta-New", "n")...).Code).To(Equal(200))
		Expect(w.stat(ctx, "kept").Attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"old", []byte("v\x00")))
		Expect(w.send(w.alice, http.MethodPut, "/plain/replaced", "", copyFrom("plain/meta", "X-Amz-Meta-New", "n", "X-Amz-Metadata-Directive", "replace")...).Code).To(Equal(200))
		st := w.stat(ctx, "replaced")
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"new", []byte("n\x00")))
		Expect(st.Attrs).NotTo(HaveKey(meta.AttrMetaPrefix + "old"))
	})
	It("gives the copy the request's ACL", func(ctx SpecContext) {
		Expect(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Acl", "public-read")...).Code).To(Equal(200))
		p, err := op.ObjectACLFor(ctx, w.stat(ctx, "dst"), w.bucket(ctx))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.IsPublic()).To(BeTrue())
	})
	It("refuses an unknown metadata directive with radosgw's message", func() {
		rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Metadata-Directive", "FOO")...)
		expectError(rec, 400, "InvalidArgument")
		Expect(rec.Body.String()).To(ContainSubstring("<Message>Unknown metadata directive.</Message>"))
	})
	It("refuses a copy onto itself that changes nothing, and allows one with REPLACE", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodPut, "/plain/src", "", copyFrom("/plain/src")...)
		expectError(rec, 400, "InvalidRequest")
		Expect(rec.Body.String()).To(ContainSubstring("trying to copy an object to itself"))
		Expect(w.send(w.alice, http.MethodPut, "/plain/src", "", copyFrom("/plain/src", "X-Amz-Metadata-Directive", "REPLACE")...).Code).To(Equal(200))
	})
	It("applies the copy-source conditions, a header sent empty among them", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Copy-Source-If-Match", `"wrong"`)...), 412, "PreconditionFailed")
		expectError(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Copy-Source-If-Match", "")...), 412, "PreconditionFailed")
		Expect(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Copy-Source-If-Match", md5Hex("hello"))...).Code).To(Equal(200))
	})
	It("resolves an explicit tenant in the source", func(ctx SpecContext) {
		Expect(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom(":plain/src")...).Code).To(Equal(200), "the legacy tenant, named explicitly")
		expectError(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("other:plain/src")...), 404, "NoSuchBucket")
	})
	It("answers a missing source key 404 NoSuchKey and a missing source bucket 404 NoSuchBucket, with the error document", func() {
		rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/missing")...)
		expectError(rec, 404, "NoSuchKey")
		Expect(rec.Header().Get("Content-Length")).NotTo(BeEmpty())
		expectError(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/nope/src")...), 404, "NoSuchBucket")
	})
	It("refuses a source with no key before authenticating, as RGWHandler_REST_S3::init does", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("nothing")...), 400, "InvalidArgument")
	})
	It("reads the source version the source names", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src?versionId=v1")...), 404, "NoSuchKey")
	})
	It("refuses the object-lock headers as get_params does, with its messages, and a retention or legal hold on a bucket without object lock", func() {
		rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Object-Lock-Legal-Hold", "maybe")...)
		expectError(rec, 400, "InvalidArgument")
		Expect(rec.Body.String()).To(ContainSubstring("<Message>invalid x-amz-object-lock-legal-hold value</Message>"))
		rec = w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Object-Lock-Mode", "GOVERNANCE")...)
		Expect(rec.Body.String()).To(ContainSubstring("<Message>need both x-amz-object-lock-mode and x-amz-object-lock-retain-until-date </Message>"))
		expectError(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Object-Lock-Legal-Hold", "ON")...), 400, "InvalidRequest")
	})
	DescribeTable("refuses a copy that would be encrypted with NotImplemented",
		func(ctx SpecContext, target string, hdr ...string) {
			expectError(w.send(w.alice, http.MethodPut, target, "", copyFrom("/plain/src", hdr...)...), 501, "NotImplemented")
			Expect(w.stat(ctx, "dst").Exists).To(BeFalse())
		},
		sseEntries("/plain/dst"),
	)
	It("refuses a copy into a bucket with a default encryption with NotImplemented once the requester is authorized", func(ctx SpecContext) {
		w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
		expectError(w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...), 501, "NotImplemented")
		expectError(w.send(w.bob, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...), 403, "AccessDenied")
		Expect(w.stat(ctx, "dst").Exists).To(BeFalse())
	})
	It("refuses a requester the destination refuses and copies nothing", func(ctx SpecContext) {
		expectError(w.send(w.bob, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...), 403, "AccessDenied")
		Expect(w.stat(ctx, "dst").Exists).To(BeFalse())
	})
	It("lets a requester the source refuses read nothing", func(ctx SpecContext) {
		w.grantBob(ctx, acl.PermWrite)
		expectError(w.send(w.bob, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...), 403, "AccessDenied")
		Expect(w.stat(ctx, "dst").Exists).To(BeFalse())
	})
})

var _ = Describe("parseCopySource", func() {
	DescribeTable("is parse_copy_location with postauth_init's tenant split",
		func(v, tenant, bucket string, key meta.ObjKey, ok bool) {
			gotTenant, gotBucket, gotKey, gotOK := s3.ParseCopySourceForTest(v, "t0")
			Expect(gotOK).To(Equal(ok), v)
			if ok {
				Expect([]any{gotTenant, gotBucket, gotKey}).To(Equal([]any{tenant, bucket, key}), v)
			}
		},
		Entry("a leading slash", "/b/k", "t0", "b", meta.ObjKey{Name: "k"}, true),
		Entry("no leading slash, and slashes in the key", "b/k/l", "t0", "b", meta.ObjKey{Name: "k/l"}, true),
		Entry("one leading slash removed", "//b/k", "t0", "", meta.ObjKey{Name: "b/k"}, true),
		Entry("a version", "b/k?versionId=v7", "t0", "b", meta.ObjKey{Name: "k", Instance: "v7"}, true),
		Entry("a version among other parameters", "b/k?x=1&versionId=v7", "t0", "b", meta.ObjKey{Name: "k", Instance: "v7"}, true),
		Entry("a ? escaped in the key is the key's", "b/k%3Fv", "t0", "b", meta.ObjKey{Name: "k?v"}, true),
		Entry("the name decoded once", "b/a%2520b", "t0", "b", meta.ObjKey{Name: "a%20b"}, true),
		Entry("a tenant", "t1:b/k", "t1", "b", meta.ObjKey{Name: "k"}, true),
		Entry("the legacy tenant named", ":b/k", "", "b", meta.ObjKey{Name: "k"}, true),
		Entry("no slash", "nothing", "", "", meta.ObjKey{}, false),
		Entry("no key", "b/", "", "", meta.ObjKey{}, false),
		Entry("empty", "", "", "", meta.ObjKey{}, false),
		Entry("a tenant and no bucket", "t1:/k", "", "", meta.ObjKey{}, false),
	)
})
