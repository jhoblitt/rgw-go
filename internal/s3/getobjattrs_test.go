package s3_test

import (
	"net/http/httptest"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

var _ = Describe("get_obj_attrs", func() {
	const docHead = `<?xml version="1.0" encoding="UTF-8"?><GetObjectAttributes>`
	var (
		w *readWorld
		h *s3.Handler
	)
	BeforeEach(func(ctx SpecContext) {
		w = newReadWorld(ctx, denc.Squid)
		h = w.handler(w.env(nil), w.alice)
	})
	do := func(method, target string, hdr map[string]string) *httptest.ResponseRecorder {
		return send(h, method, target, hdr)
	}
	multipart := func(hdr map[string]string) *httptest.ResponseRecorder {
		return send(w.handler(w.env(multipartObjects()), w.alice), "GET", "/plain/k?attributes", hdr)
	}

	It("renders the requested attributes of a single-part object, on Squid too", func() {
		rec := do("GET", "/plain/small?attributes", map[string]string{"x-amz-object-attributes": "ETag,Checksum,ObjectParts,StorageClass,ObjectSize"})
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Last-Modified")).To(Equal(readLastModified))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "end_header names no length, rgw_rest_s3.cc:4014 at v20.2.4")
		Expect(rec.Header()).NotTo(HaveKey("X-Amz-Version-Id"))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		Expect(rec.Body.String()).To(Equal(docHead + `<ETag>` + md5Of(readPayload(1024)) + `</ETag><Checksum></Checksum><ObjectSize>1024</ObjectSize><StorageClass>STANDARD</StorageClass></GetObjectAttributes>`))
	})
	It("renders nothing it was not asked for, and names a version that was asked for", func() {
		rec := do("GET", "/plain/small?attributes&versionId=null", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.String()).To(Equal(docHead + `</GetObjectAttributes>`))
		Expect(rec.Header().Get("x-amz-version-id")).To(Equal("null"))
	})
	It("escapes the ETag and storage class as XMLFormatter does", func(ctx SpecContext) {
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrStorageClass: []byte("A&B<")})
		rec := do("GET", "/plain/small?attributes", map[string]string{"x-amz-object-attributes": "StorageClass"})
		Expect(rec.Body.String()).To(Equal(docHead + `<StorageClass>A&amp;B&lt;</StorageClass></GetObjectAttributes>`))
	})
	It("renders ObjectParts for a multipart object with paging", func() {
		rec := multipart(map[string]string{"x-amz-object-attributes": "ObjectParts", "x-amz-max-parts": "2"})
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.String()).To(Equal(docHead + `<ObjectParts><Part><PartNumber>1</PartNumber><Size>8388608</Size></Part><Part><PartNumber>2</PartNumber><Size>8388608</Size></Part><PartsCount>3</PartsCount><TotalPartsCount>3</TotalPartsCount><IsTruncated>true</IsTruncated><MaxParts>2</MaxParts><NextPartNumberMarker>2</NextPartNumberMarker></ObjectParts></GetObjectAttributes>`))
	})
	It("caps x-amz-max-parts at 1000, renders the marker it was given and IsTruncated false", func() {
		rec := multipart(map[string]string{"x-amz-object-attributes": "objectparts", "x-amz-max-parts": "5000", "x-amz-part-number-marker": "2"})
		size3 := goldenManifest("squid-multipart").ObjSize - 16<<20
		Expect(rec.Body.String()).To(Equal(docHead + `<ObjectParts><Part><PartNumber>3</PartNumber><Size>` + strconv.FormatUint(size3, 10) + `</Size></Part>` +
			`<PartsCount>3</PartsCount><TotalPartsCount>3</TotalPartsCount><IsTruncated>false</IsTruncated><MaxParts>1000</MaxParts><PartNumberMarker>2</PartNumberMarker></ObjectParts></GetObjectAttributes>`))
	})
	It("rejects bad MaxParts and PartNumberMarker as InvalidPart with radosgw's messages", func() {
		rec := do("GET", "/plain/small?attributes", map[string]string{"x-amz-max-parts": "x"})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidPart</Code>"))
		Expect(rec.Body.String()).To(ContainSubstring("Invalid value for MaxParts: Expected option value to be integer, got &apos;x&apos;"))
		rec = do("GET", "/plain/small?attributes", map[string]string{"x-amz-part-number-marker": "99999999999"})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("Invalid value for PartNumberMarker: The option value &apos;99999999999&apos; seems to be invalid"))
		rec = do("GET", "/plain/small?attributes", map[string]string{"x-amz-max-parts": "x", "x-amz-part-number-marker": "y"})
		Expect(rec.Body.String()).To(ContainSubstring("MaxParts"), "get_params stops at the first")
	})
	It("refuses bad parameters only after the bucket and the key, as get_params runs in execute", func() {
		env := w.env(nil)
		env.Authz = missingObjectRule()
		rec := send(w.handler(env, w.alice), "GET", "/plain/nope?attributes", map[string]string{"x-amz-max-parts": "x"})
		Expect(rec.Code).To(Equal(404))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
	})
	It("HEAD ?attributes is a HEAD of the object (op_head has no attributes op)", func() {
		rec := do("HEAD", "/plain/small?attributes", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Length")).To(Equal("1024"))
		Expect(rec.Header().Get("ETag")).To(Equal(`"` + md5Of(readPayload(1024)) + `"`))
	})
	It("files a Squid request under get_obj and a Tentacle one under get_obj_attrs in the usage log", func(ctx SpecContext) {
		Expect(do("GET", "/plain/small?attributes", nil).Code).To(Equal(200))
		Expect(w.store.Usage()).To(ConsistOf(HaveField("Category", "get_obj")), "a Squid radosgw runs ?attributes as GetObject")
		tw := newReadWorld(ctx, denc.Tentacle)
		Expect(send(tw.handler(tw.env(nil), tw.alice), "GET", "/plain/small?attributes", nil).Code).To(Equal(200))
		Expect(tw.store.Usage()).To(ConsistOf(HaveField("Category", "get_obj_attrs")))
	})
	It("answers a refusal with the error document", func() {
		rec := send(w.handler(w.env(nil), w.bob), "GET", "/plain/small?attributes", nil)
		Expect(rec.Code).To(Equal(403))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
		Expect(rec.Header()).NotTo(HaveKey("Last-Modified"))
	})
})
