package s3_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // an S3 ETag is an MD5
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// readMtime is when the read specs' objects were written; Last-Modified keeps
// its whole seconds.
var readMtime = time.Date(2026, 9, 27, 1, 2, 3, 456_000_000, time.UTC)

const readLastModified = "Sun, 27 Sep 2026 01:02:03 GMT"

// readPayload is n bytes that differ from offset to offset.
func readPayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func md5Of(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // an S3 ETag is an MD5
	return hex.EncodeToString(sum[:])
}

func encodeTags(set tags.Set) []byte {
	e := denc.NewEncoder()
	set.Encode(e, denc.Squid)
	return e.Bytes()
}

// readWorld is the read specs' cluster: alice's bucket plain holding small,
// 1024 bytes of text/plain whose user metadata and cache control radosgw
// stores NUL-terminated, and empty; bob is another user.
type readWorld struct {
	store      *memstore.Store
	alice, bob *op.UserRecord
	plain      *op.BucketRecord
}

func newReadWorld(ctx context.Context, rel denc.Release) *readWorld {
	GinkgoHelper()
	w := &readWorld{store: memstore.New(memstore.Config{Release: rel, Now: func() time.Time { return readMtime }})}
	w.alice = w.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
	w.bob = w.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll})
	var err error
	w.plain, err = w.store.CreateBucket(ctx, op.CreateBucketParams{
		Name: "plain", Owner: meta.UserOwner(w.alice.Info.UserID), Placement: meta.PlacementRule{Name: "default-placement"},
	})
	Expect(err).NotTo(HaveOccurred())
	w.put(ctx, "small", readPayload(1024), map[string][]byte{
		meta.AttrContentType:               []byte("text/plain"),
		meta.AttrMetaPrefix + "color":      []byte("blue\x00"),
		meta.AttrCacheControl:              []byte("no-store\x00"),
		meta.AttrPrefix + "unrelated-attr": []byte("x"),
	})
	w.put(ctx, "empty", nil, nil)
	return w
}

func (w *readWorld) put(ctx context.Context, key string, data []byte, attrs map[string][]byte) {
	GinkgoHelper()
	_, err := w.store.PutObject(ctx, w.plain, meta.ObjKey{Name: key}, bytes.NewReader(data), op.PutParams{Size: int64(len(data)), Attrs: attrs})
	Expect(err).NotTo(HaveOccurred())
}

// setAttrs sets attrs on key.
func (w *readWorld) setAttrs(ctx context.Context, key string, attrs map[string][]byte) {
	GinkgoHelper()
	st, err := w.store.StatObject(ctx, w.plain, meta.ObjKey{Name: key})
	Expect(err).NotTo(HaveOccurred())
	Expect(w.store.SetObjectAttrs(ctx, st, attrs, nil)).To(Succeed())
}

// env is the world's op.Env, its objects served by objects when it is not
// nil.
func (w *readWorld) env(objects op.ObjectStore) *op.Env {
	env := testEnv(w.store)
	if objects != nil {
		env.Objects = objects
	}
	return env
}

// handler serves env as who; a nil who is anonymous.
func (w *readWorld) handler(env *op.Env, who *op.UserRecord) *s3.Handler {
	var auth s3.Authenticator = s3.AnonymousOnly{}
	if who != nil {
		auth = authAs(who)
	}
	return s3.NewHandler(env, auth, testConfig(s3.Config{}))
}

// send sends method and target through h with hdr as headers.
func send(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	pairs := make([]string, 0, 2*len(hdr))
	for k, v := range hdr {
		pairs = append(pairs, k, v)
	}
	return serveReq(h, method, target, nil, pairs...)
}

// failingReads serves objects as its store does, except that a read writes
// the first n bytes of its range and then fails with err.
type failingReads struct {
	*memstore.Store
	n   int
	err error
}

func (f failingReads) ReadObject(ctx context.Context, st *op.ObjectState, rng op.ByteRange, sink io.Writer) error {
	var buf bytes.Buffer
	if err := f.Store.ReadObject(ctx, st, rng, &buf); err != nil {
		return err
	}
	if f.n > 0 {
		if _, err := sink.Write(buf.Bytes()[:f.n]); err != nil {
			return err
		}
	}
	return f.err
}

// missingObjectRule is OwnerOnly with the authorizer's refusal of a missing
// object, which radosgw makes in read_permissions, before verify_permission
// (read_obj_policy, rgw_op.cc:385-447 at v19.2.6).
func missingObjectRule() *opfakes.FakeAuthorizer {
	a := &opfakes.FakeAuthorizer{}
	a.VerifyObjectStub = func(ctx context.Context, r *op.Request, act policy.Action, perm acl.Permission) error {
		if r.ObjState == nil || !r.ObjState.Exists {
			return op.BeforeVerify(op.ErrNoSuchKey)
		}
		return op.OwnerOnly{}.VerifyObject(ctx, r, act, perm)
	}
	return a
}

// goldenManifest decodes a manifest radosgw wrote, from meta's goldens.
func goldenManifest(name string) meta.Manifest {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join("..", "meta", "testdata", "manifests", name+".bin"))
	Expect(err).NotTo(HaveOccurred())
	d := denc.NewDecoder(b)
	m := meta.DecodeManifest(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return m
}

// multipartObjects holds k, a three-part object of 8, 8 and 4 MiB with the
// squid-multipart golden's manifest, whose part heads carry no manifest of
// their own, as radosgw writes them, and whose bytes are zeros. Every other
// key is a part head.
func multipartObjects() *opfakes.FakeObjectStore {
	m := goldenManifest("squid-multipart")
	head := func(_ context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
		if key.Name == "k" {
			mm := m
			return &op.ObjectState{
				Bucket: rec, Key: key, Exists: true, Size: m.ObjSize, Mtime: readMtime, Manifest: &mm, ETag: "mp-3",
				Attrs: map[string][]byte{meta.AttrETag: []byte("mp-3")},
			}, nil
		}
		return &op.ObjectState{
			Bucket: rec, Key: key, Exists: true, Size: 4 << 20, Mtime: readMtime, ETag: "part",
			Attrs: map[string][]byte{meta.AttrETag: []byte("part")},
		}, nil
	}
	f := &opfakes.FakeObjectStore{}
	f.StatObjectStub = head
	f.PrefetchObjectStub = head
	f.ReadObjectStub = func(_ context.Context, _ *op.ObjectState, rng op.ByteRange, sink io.Writer) error {
		_, err := sink.Write(make([]byte, rng.Length))
		return err
	}
	return f
}

var _ = Describe("get_obj", func() {
	var (
		w   *readWorld
		h   *s3.Handler
		env *op.Env
	)
	BeforeEach(func(ctx SpecContext) {
		w = newReadWorld(ctx, denc.Squid)
		env = w.env(nil)
		h = w.handler(env, w.alice)
	})
	do := func(method, target string, hdr map[string]string) *httptest.ResponseRecorder {
		return send(h, method, target, hdr)
	}

	It("GET: 200 with radosgw's headers and the body", func() {
		rec := do("GET", "/plain/small", nil)
		Expect(rec.Code).To(Equal(200))
		hd := rec.Header()
		Expect(hd.Get("Content-Length")).To(Equal("1024"))
		Expect(hd.Get("Accept-Ranges")).To(Equal("bytes"))
		Expect(hd.Get("Last-Modified")).To(Equal(readLastModified))
		Expect(hd.Get("ETag")).To(Equal(`"` + md5Of(readPayload(1024)) + `"`))
		Expect(hd.Get("Content-Type")).To(Equal("text/plain"))
		Expect(hd.Get("x-amz-meta-color")).To(Equal("blue"), "one trailing NUL stripped")
		Expect(hd.Get("Cache-Control")).To(Equal("no-store"))
		Expect(hd.Get("x-rgw-object-type")).To(Equal("Normal"))
		Expect(hd.Get("x-amz-request-id")).To(HavePrefix("tx"))
		Expect(hd.Get("Server")).NotTo(BeEmpty())
		Expect(hd).NotTo(HaveKey("Content-Range"))
		Expect(hd).NotTo(HaveKey("X-Amz-Version-Id"))
		Expect(hd).NotTo(HaveKey("X-Amz-Mp-Parts-Count"))
		Expect(hd).NotTo(HaveKey("X-Amz-Tagging-Count"))
		Expect(hd).NotTo(HaveKey("X-Amz-Request-Charged"), "the owner is not charged")
		Expect(rec.Body.Bytes()).To(Equal(readPayload(1024)))
	})
	It("HEAD: the same headers, no body; a zero-byte object has Content-Length 0", func() {
		rec := do("HEAD", "/plain/small", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Length")).To(Equal("1024"))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "send_response_data's dump_content_length, rgw_rest_s3.cc:459")
		Expect(rec.Header().Get("ETag")).To(Equal(`"` + md5Of(readPayload(1024)) + `"`))
		Expect(rec.Header().Get("x-amz-meta-color")).To(Equal("blue"))
		Expect(rec.Body.Len()).To(BeZero())
		Expect(do("HEAD", "/plain/empty", nil).Header().Get("Content-Length")).To(Equal("0"))
	})
	It("defaults the content type to binary/octet-stream", func() {
		rec := do("GET", "/plain/empty", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("binary/octet-stream"))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
	})
	It("Range: 206, Content-Range and the slice; a suffix range; HEAD with Range", func() {
		rec := do("GET", "/plain/small", map[string]string{"Range": "bytes=10-19"})
		Expect(rec.Code).To(Equal(206))
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 10-19/1024"))
		Expect(rec.Header().Get("Content-Length")).To(Equal("10"))
		Expect(rec.Body.Bytes()).To(Equal(readPayload(1024)[10:20]))
		rec = do("GET", "/plain/small", map[string]string{"Range": "bytes=-7"})
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 1017-1023/1024"))
		rec = do("HEAD", "/plain/small", map[string]string{"Range": "bytes=0-9"})
		Expect(rec.Code).To(Equal(206))
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 0-9/1024"))
		Expect(rec.Body.Len()).To(BeZero())
	})
	It("sends Content-Range whenever a Range was sent, even one served whole", func() {
		env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "true"})
		rec := do("GET", "/plain/small", map[string]string{"Range": "bytes=10-5"})
		Expect(rec.Code).To(Equal(200), "rgw_ignore_get_invalid_range serves a range parse_range refuses whole")
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 0-1023/1024"), "dump_range keys on range_str, rgw_rest_s3.cc:406-407")
		Expect(rec.Body.Len()).To(Equal(1024))
	})
	It("reads a repeated header by its last value, as RGWEnv keeps it", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/plain/small", nil)
		req.Header.Add("Range", "bytes=0-1")
		req.Header.Add("Range", "bytes=10-19")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(206))
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 10-19/1024"))
	})
	It("an invalid range is 416 InvalidRange with the error document", func() {
		rec := do("GET", "/plain/small", map[string]string{"Range": "bytes=2000-3000"})
		Expect(rec.Code).To(Equal(416))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRange</Code>"))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "end_header's error branch, rgw_rest.cc:620-624")
		Expect(rec.Header()).NotTo(HaveKey("Last-Modified"), "an error skips the object's headers, rgw_rest_s3.cc:403-404")
		Expect(rec.Header()).NotTo(HaveKey("Etag"))
		rec = do("GET", "/plain/empty", map[string]string{"Range": "bytes=40-50"})
		Expect(rec.Code).To(Equal(416))
	})
	It("304 carries Last-Modified, ETag, Cache-Control and Expires and no body", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrExpires: []byte("Thu, 01 Oct 2026 00:00:00 GMT\x00")})
		etag := do("GET", "/plain/small", nil).Header().Get("ETag")
		rec := do("GET", "/plain/small", map[string]string{"If-None-Match": etag})
		Expect(rec.Code).To(Equal(304))
		Expect(rec.Header().Get("ETag")).To(Equal(etag))
		Expect(rec.Header().Get("Last-Modified")).To(Equal(readLastModified))
		Expect(rec.Header().Get("Cache-Control")).To(Equal("no-store"))
		Expect(rec.Header().Get("Expires")).To(Equal("Thu, 01 Oct 2026 00:00:00 GMT"))
		Expect(rec.Header().Get("x-amz-request-id")).To(HavePrefix("tx"))
		Expect(rec.Body.Len()).To(BeZero())
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
		Expect(rec.Header()).NotTo(HaveKey("X-Amz-Meta-Color"), "the 304 branch dumps four headers, rgw_rest_s3.cc:623-638")
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "is_err is false for 304 and end_header gets no length, rgw_rest_s3.cc:623-638")
		rec = do("GET", "/plain/small", map[string]string{"If-Modified-Since": "Mon, 28 Sep 2026 00:00:00 GMT"})
		Expect(rec.Code).To(Equal(304))
		Expect(rec.Header().Get("ETag")).To(Equal(etag))
	})
	It("412 and 400 for the other conditionals", func() {
		Expect(do("GET", "/plain/small", map[string]string{"If-Match": `"ABCORZ"`}).Code).To(Equal(412))
		rec := do("GET", "/plain/small", map[string]string{"If-Modified-Since": "yesterday"})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"))
	})
	It("404 NoSuchKey with the error document; 404 NoSuchBucket", func() {
		rec := do("GET", "/plain/nope", nil)
		Expect(rec.Code).To(Equal(404))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
		Expect(do("GET", "/nobucket/x", nil).Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
	})
	It("applies the six response-* overrides for a signed request and refuses them for an anonymous one", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrContentDisp: []byte("attachment\x00"), meta.AttrContentLang: []byte("en")})
		q := "?response-content-type=foo/bar&response-content-disposition=bla&response-content-encoding=aaa&response-content-language=esperanto&response-cache-control=no-cache&response-expires=123"
		rec := do("GET", "/plain/small"+q, nil)
		Expect(rec.Code).To(Equal(200))
		hd := rec.Header()
		Expect([]string{hd.Get("Content-Type"), hd.Get("Content-Disposition"), hd.Get("Content-Encoding"), hd.Get("Content-Language"), hd.Get("Cache-Control"), hd.Get("Expires")}).
			To(Equal([]string{"foo/bar", "bla", "aaa", "esperanto", "no-cache", "123"}), "an override beats the stored attr")
		Expect(hd.Values("Content-Disposition")).To(HaveLen(1))

		anonEnv := w.env(nil)
		anonEnv.Authz = &opfakes.FakeAuthorizer{}
		anon := w.handler(anonEnv, nil)
		Expect(send(anon, "GET", "/plain/small", nil).Code).To(Equal(200), "the object is readable anonymously")
		rec = send(anon, "GET", "/plain/small?response-content-type=x", nil)
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRequest</Code>"))
	})
	It("renders the attrs rgw_to_http_attrs maps, each without its trailing NULs", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{
			meta.AttrContentDisp:      []byte("inline\x00\x00"),
			meta.AttrContentLang:      []byte("en"),
			meta.AttrXRobotsTag:       []byte("noindex"),
			meta.AttrStorageClass:     []byte("COLD"),
			meta.AttrWebsiteRedirect:  []byte("/other"),
			meta.AttrContentType:      []byte("text/html\x00\x00"),
			meta.AttrMetaPrefix + "a": []byte("x\x00"),
		})
		hd := do("GET", "/plain/small", nil).Header()
		Expect(hd.Get("Content-Disposition")).To(Equal("inline"))
		Expect(hd.Get("Content-Language")).To(Equal("en"))
		Expect(hd.Get("X-Robots-Tag")).To(Equal("noindex"))
		Expect(hd.Get("X-Amz-Storage-Class")).To(Equal("COLD"))
		Expect(hd.Get("x-amz-website-redirect-location")).To(Equal("/other"))
		Expect(hd.Get("Content-Type")).To(Equal("text/html"), "rgw_bl_str drops every trailing NUL")
		Expect(hd.Get("x-amz-meta-a")).To(Equal("x"))
		Expect(hd).NotTo(HaveKey("X-Unrelated-Attr"))
	})
	DescribeTable("drops aws-chunked from Content-Encoding",
		func(ctx SpecContext, stored string, want []string) {
			w.setAttrs(ctx, "small", map[string][]byte{meta.AttrContentEnc: []byte(stored)})
			rec := do("GET", "/plain/small", nil)
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Header().Values("Content-Encoding")).To(Equal(want))
		},
		Entry("among others", "gzip, aws-chunked, br\x00", []string{"gzip, br"}),
		Entry("alone, which leaves no header", "aws-chunked", nil),
		Entry("after a comma alone", "gzip,aws-chunked", []string{"gzip"}),
		Entry("before a comma alone", "aws-chunked,gzip", []string{"gzip"}),
		Entry("after a comma and a space", "gzip, aws-chunked", []string{"gzip"}),
		Entry("absent, rejoining a comma alone with a comma and a space", "gzip,br", []string{"gzip, br"}),
		Entry("between doubled separators", "gzip,, aws-chunked ,  br", []string{"gzip, br"}),
		Entry("with leading and trailing separators", ", gzip,aws-chunked, ", []string{"gzip"}),
		Entry("as part of a longer token, which stays", "aws-chunked2, x-aws-chunked", []string{"aws-chunked2, x-aws-chunked"}),
	)
	It("reports an appendable object and its next append position", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrAppendPartNum: []byte("3")})
		hd := do("HEAD", "/plain/small", nil).Header()
		Expect(hd.Get("x-rgw-object-type")).To(Equal("Appendable"))
		Expect(hd.Get("x-rgw-next-append-position")).To(Equal("1024"))
	})
	It("reports the replication status, trace and time", func(ctx SpecContext) {
		trace := denc.NewEncoder()
		denc.EncodeSlice(trace, []string{"zone-a", "zone-b:key"}, func(e *denc.Encoder, s string) { e.String(s) })
		at := denc.NewEncoder()
		at.Time(time.Date(2026, 9, 28, 4, 5, 6, 0, time.UTC))
		w.setAttrs(ctx, "small", map[string][]byte{
			meta.AttrReplicationStatus: []byte("COMPLETED\x00"),
			meta.AttrReplicationTrace:  trace.Bytes(),
			meta.AttrReplicatedAt:      at.Bytes(),
		})
		hd := do("HEAD", "/plain/small", nil).Header()
		Expect(hd.Get("x-amz-replication-status")).To(Equal("COMPLETED"), "dump_header of a bufferlist drops one NUL")
		Expect(hd.Values("x-rgw-replicated-from")).To(Equal([]string{"zone-a", "zone-b:key"}))
		Expect(hd.Get("x-rgw-replicated-at")).To(Equal("Mon, 28 Sep 2026 04:05:06 GMT"))

		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrReplicationTrace: {1, 0, 0, 0, 9}, meta.AttrReplicatedAt: {1}})
		hd = do("HEAD", "/plain/small", nil).Header()
		Expect(hd).NotTo(HaveKey("X-Rgw-Replicated-From"), "a trace that does not decode is omitted")
		Expect(hd).NotTo(HaveKey("X-Rgw-Replicated-At"))
	})
	It("names a static large object's indicator as radosgw does, in place of its user metadata", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrMetaPrefix + "static-large-object": []byte("anything\x00")})
		hd := do("HEAD", "/plain/small", nil).Header()
		Expect(hd.Get("X-Object-Meta-Static-Large-Object")).To(Equal("True"))
		Expect(hd).NotTo(HaveKey("X-Amz-Meta-Static-Large-Object"))
	})
	// extended is a handler built with rgw_extended_http_attrs set to names.
	extended := func(names string) *s3.Handler {
		env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_extended_http_attrs": names})
		return w.handler(env, w.alice)
	}
	It("maps rgw_extended_http_attrs to their headers, read once as rgw_rest_init reads it", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{
			meta.AttrPrefix + "x_custom_header": []byte("v\x00"),
			meta.AttrPrefix + "other_thing":     []byte("w"),
		})
		Expect(do("HEAD", "/plain/small", nil).Header()).NotTo(HaveKey("X-Custom-Header"), "h was built without the option")
		eh := extended("x-custom-header, Other_thing")
		hd := send(eh, "HEAD", "/plain/small", nil).Header()
		Expect(hd.Get("X-Custom-Header")).To(Equal("v"))
		Expect(hd.Get("Other-Thing")).To(Equal("w"))
		env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_extended_http_attrs": ""})
		Expect(send(eh, "HEAD", "/plain/small", nil).Header().Get("X-Custom-Header")).To(Equal("v"), "a later change is not read")
		Expect(do("HEAD", "/plain/small", nil).Header()).NotTo(HaveKey("X-Custom-Header"))
	})
	It("sends a mapped content type ahead of end_header's, as radosgw sends both", func() {
		hd := send(extended("content-type"), "GET", "/plain/small", nil).Header()
		Expect(hd.Values("Content-Type")).To(Equal([]string{"text/plain", "binary/octet-stream"}),
			"response_attrs at done:, then end_header's default, rgw_rest_s3.cc:617-643")
		hd = send(extended("content-type"), "GET", "/plain/small?response-content-type=a/b", nil).Header()
		Expect(hd.Values("Content-Type")).To(Equal([]string{"text/plain", "a/b"}))
	})
	It("sends a mapped etag after dump_etag's quoted one", func() {
		hd := send(extended("etag"), "GET", "/plain/small", nil).Header()
		Expect(hd.Values("Etag")).To(Equal([]string{`"` + md5Of(readPayload(1024)) + `"`, md5Of(readPayload(1024))}),
			"dump_etag at rgw_rest_s3.cc:507-510, response_attrs at :617-621")
	})
	It("sends a mapped end_header name ahead of end_header's own value", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrPrefix + "server": []byte("stored")})
		hd := send(extended("server"), "HEAD", "/plain/small", nil).Header()
		Expect(hd.Values("Server")).To(Equal([]string{"stored", "Ceph Object Gateway (squid)"}))
	})
	It("never lets a mapped attr set the response's framing", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{
			meta.AttrPrefix + "content_length":    []byte("10"),
			meta.AttrPrefix + "transfer_encoding": []byte("chunked"),
			meta.AttrPrefix + "connection":        []byte("close"),
		})
		eh := extended("content-length, transfer-encoding, connection")
		rec := send(eh, "GET", "/plain/small", nil)
		Expect(rec.Header().Values("Content-Length")).To(Equal([]string{"1024"}))
		Expect(rec.Header()).NotTo(HaveKey("Transfer-Encoding"))
		Expect(rec.Header()).NotTo(HaveKey("Connection"))
		Expect(rec.Body.Bytes()).To(Equal(readPayload(1024)))

		srv := httptest.NewServer(eh)
		DeferCleanup(srv.Close)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/plain/small", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(resp.Body.Close)
		b, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.ContentLength).To(BeEquivalentTo(1024))
		Expect(resp.TransferEncoding).To(BeEmpty())
		Expect(b).To(Equal(readPayload(1024)))
	})
	It("never lets a mapped trailer or hop-by-hop header into the response or its framing", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{
			meta.AttrPrefix + "trailer":          []byte("X-Checksum"),
			meta.AttrPrefix + "keep_alive":       []byte("timeout=5"),
			meta.AttrPrefix + "upgrade":          []byte("h2c"),
			meta.AttrPrefix + "te":               []byte("trailers"),
			meta.AttrPrefix + "proxy_connection": []byte("close"),
		})
		eh := extended("trailer, keep-alive, upgrade, te, proxy-connection")
		hd := send(eh, "GET", "/plain/small", nil).Header()
		for _, name := range []string{"Trailer", "Keep-Alive", "Upgrade", "Te", "Proxy-Connection"} {
			Expect(hd).NotTo(HaveKey(name))
		}
		srv := httptest.NewServer(eh)
		DeferCleanup(srv.Close)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/plain/small", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(resp.Body.Close)
		b, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.TransferEncoding).To(BeEmpty())
		Expect(resp.ContentLength).To(BeEquivalentTo(1024))
		Expect(resp.Trailer).To(BeEmpty())
		Expect(b).To(Equal(readPayload(1024)))
	})
	It("partNumber: a bad value is 400 InvalidPart with strict_strtol's message", func() {
		rec := do("GET", "/plain/small?partNumber=x", nil)
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidPart</Code>"))
		Expect(rec.Body.String()).To(ContainSubstring("<Message>Invalid partNumber: Expected option value to be integer, got &apos;x&apos;</Message>"))
		rec = do("GET", "/plain/small?partNumber=99999999999", nil)
		Expect(rec.Body.String()).To(ContainSubstring("<Message>Invalid partNumber: The option value &apos;99999999999&apos; seems to be invalid</Message>"))
		rec = do("GET", "/plain/small?partNumber=1", nil)
		Expect(rec.Code).To(Equal(200), "part 1 of a single-part object is the object")
		Expect(rec.Header()).NotTo(HaveKey("X-Amz-Mp-Parts-Count"))
		Expect(do("GET", "/plain/small?partNumber=2", nil).Code).To(Equal(400))
		Expect(do("GET", "/plain/small?partNumber=%201", nil).Code).To(Equal(200), "strtoll skips leading blanks")
		Expect(do("GET", "/plain/small?partNumber=1%20", nil).Code).To(Equal(400), "and nothing may follow the digits")
	})
	It("partNumber: refuses a bad value only after the bucket, the key and the permission, and before the Range", func() {
		env.Authz = missingObjectRule()
		Expect(do("GET", "/nobucket/small?partNumber=x", nil).Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
		Expect(do("GET", "/plain/nope?partNumber=x", nil).Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
		rec := send(w.handler(env, w.bob), "GET", "/plain/small?partNumber=x", nil)
		Expect(rec.Code).To(Equal(403), "get_params runs in execute, after verify_permission, rgw_op.cc:2239")
		Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
		rec = do("GET", "/plain/small?partNumber=x", map[string]string{"Range": "bytes=5"})
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidPart</Code>"), "init_common parses the Range after get_params")
	})
	It("x-amz-tagging-count on GET and HEAD, x-amz-version-id for a versionId request", func(ctx SpecContext) {
		set := tags.Set{}
		Expect(set.Add("a", "1", 10)).To(Succeed())
		Expect(set.Add("b", "2", 10)).To(Succeed())
		w.setAttrs(ctx, "small", map[string][]byte{tags.Attr: encodeTags(set)})
		Expect(do("HEAD", "/plain/small", nil).Header().Get("x-amz-tagging-count")).To(Equal("2"))
		Expect(do("GET", "/plain/small", nil).Header().Get("x-amz-tagging-count")).To(Equal("2"))
		Expect(do("GET", "/plain/small?versionId=null", nil).Header().Get("x-amz-version-id")).To(Equal("null"))
	})
	DescribeTable("counts the tags RGWObjTags::decode's text fallback added before it failed",
		func(ctx SpecContext, stored, want string) {
			w.setAttrs(ctx, "small", map[string][]byte{tags.Attr: []byte(stored)})
			Expect(do("HEAD", "/plain/small", nil).Header().Get("x-amz-tagging-count")).To(Equal(want))
		},
		Entry("text that parses whole", "a=1&b=2\x00", "2"),
		Entry("text whose third tag has no key", "a=1&b=2&=3&c=4", "2"),
		Entry("text whose first tag has no key", "&a=1", "0"),
		Entry("nothing but NULs", "\x00\x00", "0"),
		Entry("more tags than an object may hold", "a=1&b=2&c=3&d=4&e=5&f=6&g=7&h=8&i=9&j=10&k=11&l=12", "10"),
	)
	It("x-amz-request-charged on a requester-pays bucket for a non-owner", func(ctx SpecContext) {
		w.plain.Info.RequesterPays = true
		Expect(w.store.PutBucketInfo(ctx, w.plain)).To(Succeed())
		Expect(do("GET", "/plain/small", nil).Header()).NotTo(HaveKey("X-Amz-Request-Charged"), "the owner is not charged")
		bobEnv := w.env(nil)
		bobEnv.Authz = &opfakes.FakeAuthorizer{}
		rec := send(w.handler(bobEnv, w.bob), "GET", "/plain/small", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("x-amz-request-charged")).To(Equal("requester"))
	})
	It("refuses x-amz-server-side-encryption on a read", func() {
		rec := do("GET", "/plain/small", map[string]string{"x-amz-server-side-encryption": "AES256"})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRequest</Code>"))
	})
	It("takes a forwarded https request as secure only under rgw_trust_forwarded_https", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrCryptMode: []byte("SSE-C-AES256")})
		insecure := do("GET", "/plain/small", map[string]string{"X-Forwarded-Proto": "https"})
		Expect(insecure.Code).To(Equal(400))
		Expect(insecure.Body.String()).To(ContainSubstring("<Code>InvalidRequest</Code>"), "rgw_crypt_require_ssl refuses an insecure request")
		env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_trust_forwarded_https": "true"})
		for _, hdr := range []map[string]string{{"X-Forwarded-Proto": "https"}, {"Forwarded": "for=1.2.3.4; proto=https"}} {
			rec := do("GET", "/plain/small", hdr)
			Expect(rec.Code).To(Equal(400), "%v", hdr)
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"), "secure, so the SSE-C checks run and find no algorithm: %v", hdr)
		}
		Expect(do("GET", "/plain/small", map[string]string{"X-Forwarded-Proto": "HTTPS"}).Body.String()).
			To(ContainSubstring("<Code>InvalidRequest</Code>"), "X-Forwarded-Proto is compared exactly, rgw_common.cc:1090")
	})
	It("an error after the first byte aborts the connection, never ending a 200 as if whole", func(ctx SpecContext) {
		m := &opfakes.FakeMetrics{}
		failEnv := w.env(failingReads{Store: w.store, n: 10, err: op.ErrServiceUnavailable})
		failEnv.Metrics = m
		fh := w.handler(failEnv, w.alice)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/plain/small", nil)
		rec := httptest.NewRecorder()
		Expect(func() { fh.ServeHTTP(rec, req) }).To(PanicWith(http.ErrAbortHandler))
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Length")).To(Equal("1024"))
		Expect(rec.Body.Bytes()).To(Equal(readPayload(1024)[:10]), "what went out before the failure")
		Expect(m.ObserveCallCount()).To(Equal(1))
		_, status, _, _, _ := m.ObserveArgsForCall(0)
		Expect(status).To(Equal(200))

		srv := httptest.NewServer(fh)
		DeferCleanup(srv.Close)
		hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/plain/small", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := http.DefaultClient.Do(hreq)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(resp.Body.Close)
		b, err := io.ReadAll(resp.Body)
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
		Expect(b).To(Equal(readPayload(1024)[:10]))
	})
	DescribeTable("answers a block its codec refuses before the first byte as radosgw's decompress return reads",
		func(ret, status int, code string) {
			decodeErr := fmt.Errorf("decoding small: %w", &compression.DecodeError{Codec: compression.Snappy, Ret: ret})
			rec := send(w.handler(w.env(failingReads{Store: w.store, err: decodeErr}), w.alice), "GET", "/plain/small", nil)
			Expect(rec.Code).To(Equal(status))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>" + code + "</Code>"))
		},
		Entry("-1, EPERM", -1, 403, "AccessDenied"),
		Entry("-2, ENOENT", -2, 404, "NoSuchKey"),
	)
	It("aborts the connection when a block fails to decode after the first byte", func(ctx SpecContext) {
		decodeErr := &compression.DecodeError{Codec: compression.Snappy, Ret: -2}
		fh := w.handler(w.env(failingReads{Store: w.store, n: 10, err: decodeErr}), w.alice)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/plain/small", nil)
		rec := httptest.NewRecorder()
		Expect(func() { fh.ServeHTTP(rec, req) }).To(PanicWith(http.ErrAbortHandler))
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.Len()).To(Equal(10), "no error document after the bytes")
	})
	It("Tentacle emits x-amz-mp-parts-count on a plain GET of a multipart object; Squid does not", func(ctx SpecContext) {
		Expect(send(w.handler(w.env(multipartObjects()), w.alice), "HEAD", "/plain/k", nil).Header()).NotTo(HaveKey("X-Amz-Mp-Parts-Count"))
		tw := newReadWorld(ctx, denc.Tentacle)
		rec := send(tw.handler(tw.env(multipartObjects()), tw.alice), "HEAD", "/plain/k", nil)
		Expect(rec.Header().Get("x-amz-mp-parts-count")).To(Equal("3"))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.FormatUint(goldenManifest("squid-multipart").ObjSize, 10)))
	})
	It("serves a part of a multipart object with its length, range and the parts count", func() {
		rec := send(w.handler(w.env(multipartObjects()), w.alice), "GET", "/plain/k?partNumber=2", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(8 << 20)))
		Expect(rec.Header().Get("x-amz-mp-parts-count")).To(Equal("3"))
		Expect(rec.Header().Get("ETag")).To(Equal(`"part"`), "the part head's attrs")
		Expect(rec.Body.Len()).To(Equal(8 << 20))
	})

	Describe("on Tentacle, a restored object", func() {
		BeforeEach(func(ctx SpecContext) {
			w = newReadWorld(ctx, denc.Tentacle)
			env = w.env(nil)
			h = w.handler(env, w.alice)
		})
		expiry := func(t time.Time) []byte {
			e := denc.NewEncoder()
			e.Time(t)
			return e.Bytes()
		}
		It("reports a restore in progress", func(ctx SpecContext) {
			w.setAttrs(ctx, "small", map[string][]byte{meta.AttrRestoreStatus: {byte(meta.RestoreAlreadyInProgress)}})
			Expect(do("HEAD", "/plain/small", nil).Header().Get("x-amz-restore")).To(Equal(`ongoing-request="true"`))
		})
		It("reports a temporary copy's expiry and the cloud tier's storage class", func(ctx SpecContext) {
			w.setAttrs(ctx, "small", map[string][]byte{
				meta.AttrRestoreStatus:         {byte(meta.CloudRestored)},
				meta.AttrRestoreType:           {1},
				meta.AttrRestoreExpiryDate:     expiry(readMtime.Add(24 * time.Hour)),
				meta.AttrCloudTierStorageClass: []byte("CLOUDTIER"),
				meta.AttrStorageClass:          []byte("STANDARD"),
			})
			hd := do("GET", "/plain/small", nil).Header()
			Expect(hd.Get("x-amz-restore")).To(Equal(`ongoing-request="false", expiry-date="Mon, 28 Sep 2026 01:02:03 GMT"`))
			Expect(hd.Get("X-Amz-Storage-Class")).To(Equal("CLOUDTIER"), "rgw_rest_s3.cc:619-624 at v20.2.4")
		})
		It("dates a temporary copy without an expiry at the epoch, and says nothing of a permanent one", func(ctx SpecContext) {
			w.setAttrs(ctx, "small", map[string][]byte{meta.AttrRestoreStatus: {byte(meta.CloudRestored)}, meta.AttrRestoreType: {1}})
			Expect(do("HEAD", "/plain/small", nil).Header().Get("x-amz-restore")).To(Equal(`ongoing-request="false", expiry-date="Thu, 01 Jan 1970 00:00:00 GMT"`))
			w.setAttrs(ctx, "small", map[string][]byte{meta.AttrRestoreType: {2}, meta.AttrCloudTierStorageClass: []byte("CLOUDTIER")})
			hd := do("HEAD", "/plain/small", nil).Header()
			Expect(hd).NotTo(HaveKey("X-Amz-Restore"))
			Expect(hd).NotTo(HaveKey("X-Amz-Storage-Class"))
		})
		It("omits the header for a restore attr that does not decode, where radosgw terminates", func(ctx SpecContext) {
			w.setAttrs(ctx, "small", map[string][]byte{meta.AttrRestoreStatus: {}})
			rec := do("HEAD", "/plain/small", nil)
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Header()).NotTo(HaveKey("X-Amz-Restore"))
			w.setAttrs(ctx, "small", map[string][]byte{meta.AttrRestoreStatus: {byte(meta.CloudRestored)}, meta.AttrRestoreType: {1}, meta.AttrRestoreExpiryDate: {1, 2}})
			rec = do("HEAD", "/plain/small", nil)
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Header()).NotTo(HaveKey("X-Amz-Restore"))
		})
	})
	It("ignores the restore attrs on Squid", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrRestoreStatus: {byte(meta.RestoreAlreadyInProgress)}})
		rec := do("HEAD", "/plain/small", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header()).NotTo(HaveKey("X-Amz-Restore"))
	})
})

var _ = Describe("get_obj_tags and object get_acls", func() {
	var (
		w *readWorld
		h *s3.Handler
	)
	BeforeEach(func(ctx SpecContext) {
		w = newReadWorld(ctx, denc.Squid)
		h = w.handler(w.env(nil), w.alice)
	})
	do := func(method, target string) *httptest.ResponseRecorder { return send(h, method, target, nil) }

	It("renders an empty TagSet and a populated one", func(ctx SpecContext) {
		rec := do("GET", "/plain/small?tagging")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "send_response_data's end_header names no length, rgw_rest_s3.cc:751")
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet></TagSet></Tagging>`))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		set := tags.Set{}
		Expect(set.Add("k", "v&", 10)).To(Succeed())
		w.setAttrs(ctx, "small", map[string][]byte{tags.Attr: encodeTags(set)})
		Expect(do("GET", "/plain/small?tagging").Body.String()).To(ContainSubstring("<Tag><Key>k</Key><Value>v&amp;</Value></Tag>"))
	})
	It("answers a missing object 404", func() {
		rec := do("GET", "/plain/nope?tagging")
		Expect(rec.Code).To(Equal(404))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
	})
	It("renders the object ACL through the shared get_acls route", func() {
		rec := do("GET", "/plain/small?acl")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
		Expect(rec.Body.String()).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID>`))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
	})
	It("answers HEAD ?acl with the headers alone", func() {
		get := do("GET", "/plain/small?acl")
		rec := do("HEAD", "/plain/small?acl")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.Len()).To(BeZero())
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(get.Body.Len())))
	})
	It("answers the ACL of a missing object 404", func() {
		Expect(do("GET", "/plain/nope?acl").Code).To(Equal(404))
	})
})
