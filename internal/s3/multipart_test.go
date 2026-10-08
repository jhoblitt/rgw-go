package s3_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

const (
	xmlDecl      = `<?xml version="1.0" encoding="UTF-8"?>`
	xmlnsS3      = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`
	partFiveMiB  = 5 << 20
	writeMtimeMs = "2026-09-28T12:00:00.000Z"
)

var uploadIDPattern = regexp.MustCompile(`<UploadId>([^<]+)</UploadId>`)

// initUpload creates an upload of target, which may carry a query, as who
// and returns its id.
func (w *writeWorld) initUpload(who *op.UserRecord, target string, hdr ...string) string {
	GinkgoHelper()
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	rec := w.send(who, http.MethodPost, target+sep+"uploads", "", hdr...)
	Expect(rec.Code).To(Equal(200), rec.Body.String())
	m := uploadIDPattern.FindStringSubmatch(rec.Body.String())
	Expect(m).To(HaveLen(2), rec.Body.String())
	return m[1]
}

// sendBytes serves method and target as who with body and its length.
func (w *writeWorld) sendBytes(who *op.UserRecord, method, target string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	return serveReq(w.handler(who), method, target, bytes.NewReader(body), hdr...)
}

// upload is the upload id of key as the store holds it.
func (w *writeWorld) upload(ctx context.Context, key, id string) (*op.Upload, error) {
	return w.store.GetUpload(ctx, w.bucket(ctx), meta.ObjKey{Name: key}, id)
}

// uploads lists plain's uploads as the store holds them.
func (w *writeWorld) uploads(ctx context.Context) []op.Upload {
	GinkgoHelper()
	res, err := w.store.ListUploads(ctx, w.bucket(ctx), op.ListUploadsParams{MaxUploads: 1000})
	Expect(err).NotTo(HaveOccurred())
	return res.Uploads
}

// parts lists the parts of upload id of key as the store holds them.
func (w *writeWorld) parts(ctx context.Context, key, id string) []op.Part {
	GinkgoHelper()
	up, err := w.upload(ctx, key, id)
	Expect(err).NotTo(HaveOccurred())
	res, err := w.store.ListParts(ctx, up, 0, 1000)
	Expect(err).NotTo(HaveOccurred())
	return res.Parts
}

// completion is a CompleteMultipartUpload document naming each part by its
// number and ETag.
func completion(etags ...string) string {
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for i, e := range etags {
		b.WriteString("<Part><PartNumber>" + strconv.Itoa(i+1) + `</PartNumber><ETag>"` + e + `"</ETag></Part>`)
	}
	b.WriteString("</CompleteMultipartUpload>")
	return b.String()
}

// capturedParts is a multipart store that records the PutParams of each
// part it writes.
type capturedParts struct {
	op.MultipartStore
	params *[]op.PutParams
}

func (c capturedParts) PutPart(ctx context.Context, up *op.Upload, n int, body io.Reader, p op.PutParams) (*op.PartResult, error) {
	*c.params = append(*c.params, p)
	return c.MultipartStore.PutPart(ctx, up, n, body, p)
}

// brokenUploads is a multipart store whose uploads' info cannot be read, as
// a meta object that does not decode reads.
type brokenUploads struct{ op.MultipartStore }

func (brokenUploads) GetUpload(context.Context, *op.BucketRecord, meta.ObjKey, string) (*op.Upload, error) {
	return nil, op.ErrUnknown
}

// expectChunkedXML checks rec is a 200 listing framed as end_header(...,
// CHUNKED_TRANSFER_ENCODING) frames it: no length and no Accept-Ranges.
func expectChunkedXML(rec *httptest.ResponseRecorder) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(200), rec.Body.String())
	Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
	Expect(rec.Header()).NotTo(HaveKey("Content-Length"))
	Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
	Expect(rec.Body.String()).To(HavePrefix(xmlDecl))
}

var _ = Describe("multipart handlers", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })

	It("runs an upload end to end with radosgw's documents and headers", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodPost, "/plain/k?uploads", "")
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "send_response's end_header names no length, rgw_rest_s3.cc:4043")
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		Expect(rec.Body.String()).To(MatchRegexp(`^` + regexp.QuoteMeta(xmlDecl+`<InitiateMultipartUploadResult `+xmlnsS3+`>`) +
			`<Bucket>plain</Bucket><Key>k</Key><UploadId>2~[A-Za-z0-9_-]{31}</UploadId></InitiateMultipartUploadResult>$`))
		id := uploadIDPattern.FindStringSubmatch(rec.Body.String())[1]

		p1 := bytes.Repeat([]byte("a"), partFiveMiB)
		rec = w.sendBytes(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", p1)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Header().Get("ETag")).To(Equal(`"` + md5Of(p1) + `"`))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"), "dump_content_length(s, 0), rgw_rest_s3.cc:2747")
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"))
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
		Expect(rec.Body.Len()).To(BeZero())

		big := bytes.Repeat([]byte("s"), 8<<20)
		Expect(w.sendBytes(w.alice, http.MethodPut, "/plain/big", big).Code).To(Equal(200))
		p2 := big[:2<<20]
		rec = w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", "",
			"Content-Length", "0", "X-Amz-Copy-Source", "/plain/big", "X-Amz-Copy-Source-Range", "bytes=0-2097151")
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Header()).NotTo(HaveKey("ETag"), "CopyPartResult carries the ETag in the body")
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "the copy branch's end_header names no length, rgw_rest_s3.cc:2756")
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(xmlDecl + `<CopyPartResult ` + xmlnsS3 + `><LastModified>` + writeMtimeMs +
			`</LastModified><ETag>&quot;` + md5Of(p2) + `&quot;</ETag></CopyPartResult>`))

		rec = w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id+"&max-parts=1", "")
		expectChunkedXML(rec)
		Expect(rec.Body.String()).To(Equal(xmlDecl + `<ListPartsResult ` + xmlnsS3 + `><Bucket>plain</Bucket><Key>k</Key><UploadId>` + id +
			`</UploadId><StorageClass>STANDARD</StorageClass><PartNumberMarker>0</PartNumberMarker><NextPartNumberMarker>1</NextPartNumberMarker>` +
			`<MaxParts>1</MaxParts><IsTruncated>true</IsTruncated><Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner>` +
			`<Part><LastModified>` + writeMtimeMs + `</LastModified><PartNumber>1</PartNumber><ETag>&quot;` + md5Of(p1) +
			`&quot;</ETag><Size>5242880</Size></Part></ListPartsResult>`))

		rec = w.send(w.alice, http.MethodGet, "/plain?uploads&encoding-type=url&prefix=k", "")
		expectChunkedXML(rec)
		Expect(rec.Body.String()).To(Equal(xmlDecl + `<ListMultipartUploadsResult ` + xmlnsS3 + `><Bucket>plain</Bucket><Prefix>k</Prefix>` +
			`<NextKeyMarker>k</NextKeyMarker><NextUploadIdMarker>` + id + `</NextUploadIdMarker><MaxUploads>1000</MaxUploads>` +
			`<IsTruncated>false</IsTruncated><Upload><Key>k</Key><UploadId>` + id + `</UploadId>` +
			`<Initiator><ID>alice</ID><DisplayName>Alice</DisplayName></Initiator><Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner>` +
			`<StorageClass>STANDARD</StorageClass><Initiated>` + writeMtimeMs + `</Initiated></Upload></ListMultipartUploadsResult>`))

		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/plain/k?uploadId="+id, strings.NewReader(completion(md5Of(p1), md5Of(p2))))
		req.Host = "S3.Example.com:7480"
		rec = httptest.NewRecorder()
		w.handler(w.alice).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "send_response's end_header names no length, rgw_rest_s3.cc:4082")
		Expect(rec.Header()).NotTo(HaveKey("X-Amz-Version-Id"))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		Expect(rec.Body.String()).To(MatchRegexp(`^` + regexp.QuoteMeta(xmlDecl+`<CompleteMultipartUploadResult `+xmlnsS3+`>`) +
			`<Location>http://S3\.Example\.com:7480/plain/k</Location><Bucket>plain</Bucket><Key>k</Key>` +
			`<ETag>&quot;[0-9a-f]{32}-2&quot;</ETag></CompleteMultipartUploadResult>$`))

		head := w.send(w.alice, http.MethodHead, "/plain/k", "")
		Expect(head.Code).To(Equal(200))
		Expect(head.Header().Get("Content-Length")).To(Equal(strconv.Itoa(partFiveMiB + 2<<20)))
		Expect(w.uploads(ctx)).To(BeEmpty())
	})

	It("aborts with 204 and no body, and answers NoSuchUpload afterwards", func(ctx SpecContext) {
		id := w.initUpload(w.alice, "/plain/k")
		rec := w.send(w.alice, http.MethodDelete, "/plain/k?uploadId="+id, "")
		Expect(rec.Code).To(Equal(204))
		Expect(rec.Body.Len()).To(BeZero())
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
		_, err := w.upload(ctx, "k", id)
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		expectError(w.send(w.alice, http.MethodDelete, "/plain/k?uploadId="+id, ""), 404, "NoSuchUpload")
	})

	DescribeTable("refuses a request as radosgw's get_params and the ops do",
		func(ctx SpecContext, build func(id string) (method, target, body string, hdr []string), status int, code string) {
			id := w.initUpload(w.alice, "/plain/k")
			method, target, body, hdr := build(id)
			expectError(w.send(w.alice, method, target, body, hdr...), status, code)
			Expect(w.parts(ctx, "k", id)).To(BeEmpty())
		},
		Entry("a part without a number", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id, "x", nil
		}, 400, "InvalidArgument"),
		Entry("a part number strict_strtol refuses", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1x", "x", nil
		}, 400, "InvalidArgument"),
		Entry("a part number past int", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=2147483648", "x", nil
		}, 400, "InvalidArgument"),
		Entry("a part without a length that is not chunked", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "", nil
		}, 411, "MissingContentLength"),
		Entry("a Content-MD5 that is not 16 bytes", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"Content-MD5", "nope"}
		}, 400, "InvalidDigest"),
		Entry("a Content-MD5 that does not match", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"Content-MD5", md5B64("y")}
		}, 400, "BadDigest"),
		Entry("an x-amz-tagging set_from_string refuses", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Tagging", "a=b&"}
		}, 400, "InvalidArgument"),
		Entry("a legal hold on a bucket without object lock", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Object-Lock-Legal-Hold", "ON"}
		}, 400, "InvalidRequest"),
		Entry("a copy range without bytes=", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src", "X-Amz-Copy-Source-Range", "0-1"}
		}, 400, "InvalidArgument"),
		Entry("a copy range sent empty", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src", "X-Amz-Copy-Source-Range", ""}
		}, 400, "InvalidArgument"),
		Entry("an inverted copy range", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src", "X-Amz-Copy-Source-Range", "bytes=3-1"}
		}, 416, "InvalidRange"),
		Entry("a copy source with no key", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain"}
		}, 400, "InvalidArgument"),
		Entry("a copy source with a tenant and no bucket", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "t1:/src"}
		}, 400, "InvalidArgument"),
		Entry("a copy source whose bucket is missing", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/nope/src"}
		}, 404, "NoSuchBucket"),
		Entry("a copy-source condition sent empty", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src", "X-Amz-Copy-Source-If-Match", ""}
		}, 412, "PreconditionFailed"),
		Entry("a copy-source If-Unmodified-Since before the source's mtime", func(id string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src", "X-Amz-Copy-Source-If-Unmodified-Since", "Sun, 27 Sep 2026 00:00:00 GMT"}
		}, 412, "PreconditionFailed"),
		Entry("a completion without a length that is not chunked", func(id string) (string, string, string, []string) {
			return http.MethodPost, "/plain/k?uploadId=" + id, "", nil
		}, 411, "MissingContentLength"),
		Entry("a completion with an empty body", func(id string) (string, string, string, []string) {
			return http.MethodPost, "/plain/k?uploadId=" + id, "", []string{"Content-Length", "0"}
		}, 400, "MalformedXML"),
		Entry("a completion naming a part never uploaded", func(id string) (string, string, string, []string) {
			return http.MethodPost, "/plain/k?uploadId=" + id, completion(md5Hex("x")), nil
		}, 400, "InvalidPart"),
		Entry("a completion body over rgw_max_put_param_size", func(id string) (string, string, string, []string) {
			return http.MethodPost, "/plain/k?uploadId=" + id, strings.Repeat(" ", 1<<20+1), nil
		}, 416, "InvalidRange"),
		Entry("a ListParts part-number-marker strict_strtol refuses", func(id string) (string, string, string, []string) {
			return http.MethodGet, "/plain/k?uploadId=" + id + "&part-number-marker=one", "", nil
		}, 400, "InvalidArgument"),
		Entry("a max-parts parse_value_and_bound refuses", func(id string) (string, string, string, []string) {
			return http.MethodGet, "/plain/k?uploadId=" + id + "&max-parts=ten", "", nil
		}, 400, "InvalidArgument"),
		Entry("a max-uploads parse_value_and_bound refuses", func(string) (string, string, string, []string) {
			return http.MethodGet, "/plain?uploads&max-uploads=1x", "", nil
		}, 400, "InvalidArgument"),
		Entry("a ListParts of an upload that does not exist, under the missing-object rule", func(string) (string, string, string, []string) {
			return http.MethodGet, "/plain/k?uploadId=2~nope", "", nil
		}, 404, "NoSuchKey"),
		Entry("an abort of an upload that does not exist", func(string) (string, string, string, []string) {
			return http.MethodDelete, "/plain/k?uploadId=2~nope", "", nil
		}, 404, "NoSuchUpload"),
		Entry("a part of an upload that does not exist", func(string) (string, string, string, []string) {
			return http.MethodPut, "/plain/k?uploadId=2~nope&partNumber=1", "x", nil
		}, 404, "NoSuchUpload"),
	)

	It("answers an encoding-type other than url with radosgw's message", func() {
		rec := w.send(w.alice, http.MethodGet, "/plain?uploads&encoding-type=base64", "")
		expectError(rec, 400, "InvalidArgument")
		Expect(rec.Body.String()).To(ContainSubstring("<Message>Invalid Encoding Method specified in Request</Message>"))
		expectChunkedXML(w.send(w.alice, http.MethodGet, "/plain?uploads&encoding-type=URL", ""))
	})

	It("answers ListParts with an empty uploadId NoSuchUpload once the object's own ACL allows it", func(ctx SpecContext) {
		w.put(ctx, "k", "x", nil)
		expectError(w.send(w.alice, http.MethodGet, "/plain/k?uploadId=", ""), 404, "NoSuchUpload")
	})

	It("serves a PUT with an empty uploadId as a PutObject, as get_params reads it", func(ctx SpecContext) {
		Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId=&partNumber=3", "hello").Code).To(Equal(200))
		Expect(w.stat(ctx, "k").ETag).To(Equal(md5Hex("hello")))
	})

	It("serves a part whose x-amz-copy-source is empty as a part with a body, as init_processing reads it", func(ctx SpecContext) {
		id := w.initUpload(w.alice, "/plain/k")
		rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "hello", "X-Amz-Copy-Source", "")
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Header().Get("ETag")).To(Equal(`"` + md5Hex("hello") + `"`))
	})

	It("reads an UploadPartCopy source as init_processing does, decoding the whole value before it splits", func(ctx SpecContext) {
		w.put(ctx, "a?b", "question", nil)
		w.put(ctx, "c?versionId=v1", "named", nil)
		id := w.initUpload(w.alice, "/plain/k")
		rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "",
			"Content-Length", "0", "X-Amz-Copy-Source", "plain/a?b")
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("&quot;" + md5Hex("question") + "&quot;"))
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", "",
			"Content-Length", "0", "X-Amz-Copy-Source", "plain/c%3FversionId=v1"), 404, "NoSuchKey")
		Expect(w.parts(ctx, "k", id)).To(HaveLen(1), "the source is version v1 of c, which does not exist")
	})

	It("copies the whole source without a range and stores what the request's headers ask for", func(ctx SpecContext) {
		id := w.initUpload(w.alice, "/plain/k")
		rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "",
			"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src")
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(w.parts(ctx, "k", id)).To(ConsistOf(HaveField("ETag", md5Hex("hello"))))
	})

	It("stores the part's request attrs, tags and ACL on the part head, the query's metadata included, and no SSE header", func(ctx SpecContext) {
		var params []op.PutParams
		w.env.Multipart = capturedParts{MultipartStore: w.store, params: &params}
		id := w.initUpload(w.alice, "/plain/k")
		rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1&x-amz-meta-q=qv", "x",
			"Content-Type", "text/plain", "X-Amz-Meta-Color", "blue", "X-Amz-Tagging", "a=b")
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(params).To(HaveLen(1))
		attrs := params[0].Attrs
		Expect(attrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")))
		Expect(attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"color", []byte("blue\x00")))
		Expect(attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"q", []byte("qv\x00")), "map_qs_metadata, rgw_rest_s3.cc:2591-2593")
		set := tags.Set{}
		Expect(set.Add("a", "b", tags.MaxObjectTags)).To(Succeed())
		Expect(attrs).To(HaveKeyWithValue(tags.Attr, encodeTags(set)), "encode_obj_tags_attr, rgw_op.cc:4531")
		Expect(attrs).To(HaveKeyWithValue(meta.AttrACL, encodeACL(acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice"))))
		Expect(params[0].Tag).NotTo(BeEmpty(), "the request id")
	})

	It("stores the upload's request attrs, tags and ACL, but not the query's metadata", func(ctx SpecContext) {
		id := w.initUpload(w.alice, "/plain/k?x-amz-meta-q=qv",
			"Content-Type", "text/plain", "X-Amz-Meta-Color", "blue", "X-Amz-Tagging", "a=b", "X-Amz-Acl", "public-read")
		up, err := w.upload(ctx, "k", id)
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Attrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")))
		Expect(up.Attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"color", []byte("blue\x00")))
		Expect(up.Attrs).NotTo(HaveKey(meta.AttrMetaPrefix+"q"), "RGWInitMultipart_ObjStore_S3 calls no map_qs_metadata")
		set := tags.Set{}
		Expect(set.Add("a", "b", tags.MaxObjectTags)).To(Succeed())
		Expect(up.Attrs).To(HaveKeyWithValue(tags.Attr, encodeTags(set)))
		p, err := op.ObjectACLFor(ctx, &op.ObjectState{Exists: true, Attrs: up.Attrs}, w.bucket(ctx))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.IsPublic()).To(BeTrue())
	})

	It("builds a bucket-owner canned ACL for the bucket's ACL owner", func(ctx SpecContext) {
		w.grantBob(ctx, acl.PermWrite)
		id := w.initUpload(w.bob, "/plain/k", "X-Amz-Acl", "bucket-owner-full-control")
		up, err := w.upload(ctx, "k", id)
		Expect(err).NotTo(HaveOccurred())
		p, err := op.ObjectACLFor(ctx, &op.ObjectState{Exists: true, Attrs: up.Attrs}, w.bucket(ctx))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Owner.ID).To(Equal("bob"))
		Expect(p.ACL.Grants).To(ContainElement(HaveField("Grant.ID", "alice")), "create_s3_policy's bucket owner is s->bucket_owner")
	})

	It("refuses a public ACL under a block of public ACLs, on CreateMultipartUpload and UploadPart", func(ctx SpecContext) {
		id := w.initUpload(w.alice, "/plain/k")
		w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
		expectError(w.send(w.alice, http.MethodPost, "/plain/j?uploads", "", "X-Amz-Grant-Read", `uri="http://acs.amazonaws.com/groups/global/AllUsers"`), 403, "AccessDenied")
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x", "X-Amz-Acl", "public-read"), 403, "AccessDenied")
		Expect(w.uploads(ctx)).To(HaveLen(1))
		Expect(w.parts(ctx, "k", id)).To(BeEmpty())
		Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x", "X-Amz-Acl", "private").Code).To(Equal(200))
	})

	It("refuses an upload's tags and object-lock headers as get_params does", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodPost, "/plain/k?uploads", "", "X-Amz-Tagging", "a=b&"), 400, "InvalidArgument")
		expectError(w.send(w.alice, http.MethodPost, "/plain/k?uploads", "", "X-Amz-Object-Lock-Mode", "GOVERNANCE"), 400, "InvalidArgument")
		expectError(w.send(w.alice, http.MethodPost, "/plain/k?uploads", "", "X-Amz-Object-Lock-Legal-Hold", "ON"), 400, "InvalidRequest")
		Expect(w.uploads(ctx)).To(BeEmpty())
	})

	It("does not serve the object-lock headers on a bucket with object lock", func(ctx SpecContext) {
		id := w.initUpload(w.alice, "/plain/k")
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketObjLockEnabled | meta.BucketVersioned })
		expectError(w.send(w.alice, http.MethodPost, "/plain/j?uploads", "", "X-Amz-Object-Lock-Legal-Hold", "ON"), 501, "NotImplemented")
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x", "X-Amz-Object-Lock-Legal-Hold", "OFF"), 501, "NotImplemented")
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x",
			"X-Amz-Object-Lock-Mode", "COMPLIANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z"), 501, "NotImplemented")
		Expect(w.uploads(ctx)).To(HaveLen(1))
		Expect(w.parts(ctx, "k", id)).To(BeEmpty())
	})

	Describe("encryption", func() {
		DescribeTable("refuses a CreateMultipartUpload that asks for encryption with NotImplemented, creating no upload",
			func(ctx SpecContext, target string, hdr ...string) {
				expectError(w.send(w.alice, http.MethodPost, target, "", hdr...), 501, "NotImplemented")
				Expect(w.uploads(ctx)).To(BeEmpty())
			},
			append(sseEntries("/plain/k?uploads"),
				Entry("a name init_meta_info files under its 20-byte crypt prefix", "/plain/k?uploads", "X-Rgw-Server-Side-Enx", "1"),
			),
		)
		It("does not read SSE from a CreateMultipartUpload's query, which no map_qs_metadata reads", func(ctx SpecContext) {
			Expect(w.send(w.alice, http.MethodPost, "/plain/k?uploads&x-amz-server-side-encryption=AES256", "").Code).To(Equal(200))
			Expect(w.uploads(ctx)).To(HaveLen(1))
		})
		DescribeTable("refuses an UploadPart that asks for encryption with NotImplemented, storing no part",
			func(ctx SpecContext, target string, hdr ...string) {
				id := w.initUpload(w.alice, "/plain/k")
				expectError(w.send(w.alice, http.MethodPut, strings.ReplaceAll(target, "ID", id), "x", hdr...), 501, "NotImplemented")
				Expect(w.parts(ctx, "k", id)).To(BeEmpty())
			},
			append(sseEntries("/plain/k?uploadId=ID&partNumber=1"),
				Entry("a name init_meta_info files under its 20-byte crypt prefix", "/plain/k?uploadId=ID&partNumber=1", "X-Rgw-Server-Side-Enx", "1"),
				Entry("SSE in the query string, which map_qs_metadata reads", "/plain/k?uploadId=ID&partNumber=1&x-amz-server-side-encryption=AES256"),
			),
		)
		DescribeTable("refuses an UploadPartCopy that asks for encryption or names its source's key with NotImplemented",
			func(ctx SpecContext, hdr ...string) {
				id := w.initUpload(w.alice, "/plain/k")
				all := append([]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src"}, hdr...)
				expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "", all...), 501, "NotImplemented")
				Expect(w.parts(ctx, "k", id)).To(BeEmpty())
			},
			Entry("SSE-C", "X-Amz-Server-Side-Encryption-Customer-Key", "a2V5"),
			Entry("the source's SSE-C key", "X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key", "a2V5"),
			Entry("the source's SSE-C key under another prefix", "X-Goog-Copy-Source-Server-Side-Encryption-Customer-Algorithm", "AES256"),
		)
		It("refuses CreateMultipartUpload and UploadPart into a bucket with a default encryption once the requester is authorized", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
			expectError(w.send(w.alice, http.MethodPost, "/plain/j?uploads", ""), 501, "NotImplemented")
			expectError(w.send(w.bob, http.MethodPost, "/plain/j?uploads", ""), 403, "AccessDenied")
			expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x"), 501, "NotImplemented")
			expectError(w.send(w.bob, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x"), 403, "AccessDenied")
			expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "",
				"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src"), 501, "NotImplemented")
			Expect(w.uploads(ctx)).To(HaveLen(1))
			Expect(w.parts(ctx, "k", id)).To(BeEmpty())
		})
	})

	Describe("authorization", func() {
		It("refuses each multipart op to a requester the bucket refuses, changing nothing", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			p1 := bytes.Repeat([]byte("a"), partFiveMiB)
			Expect(w.sendBytes(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", p1).Code).To(Equal(200))
			expectError(w.send(w.bob, http.MethodPost, "/plain/j?uploads", ""), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", "x"), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", "",
				"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src"), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodGet, "/plain/k?uploadId="+id, ""), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodGet, "/plain?uploads", ""), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Of(p1))), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodDelete, "/plain/k?uploadId="+id, ""), 403, "AccessDenied")
			Expect(w.uploads(ctx)).To(ConsistOf(HaveField("ID", id)))
			Expect(w.parts(ctx, "k", id)).To(ConsistOf(HaveField("Number", 1)))
		})
		It("checks permission before it reads a part's or a completion's body", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			expectError(w.send(w.bob, http.MethodPost, "/plain/k?uploadId="+id, strings.Repeat(" ", 1<<20+1)), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodPost, "/plain/k?uploadId="+id, "", "Content-Length", "0"), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodGet, "/plain/k?uploadId="+id+"&max-parts=ten", ""), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodGet, "/plain?uploads&encoding-type=base64", ""), 403, "AccessDenied")
		})
		It("lets a grantee of the bucket's WRITE upload, complete and abort", func(ctx SpecContext) {
			w.grantBob(ctx, acl.PermWrite)
			id := w.initUpload(w.bob, "/plain/k")
			rec := w.send(w.bob, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(w.send(w.bob, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x"))).Code).To(Equal(200))
			Expect(w.stat(ctx, "k").ETag).To(HaveSuffix("-1"))
		})
	})

	Describe("the request body", func() {
		It("refuses a completion whose payload hash does not match, completing nothing", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			expectError(w.sendTampered(http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x"))), 400, "XAmzContentSHA256Mismatch")
			Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			Expect(w.uploads(ctx)).To(ConsistOf(HaveField("ID", id)))
			Expect(w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x"))).Code).To(Equal(200))
		})
		It("refuses a part whose payload hash does not match, storing no part", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			expectError(w.sendTampered(http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "hello"), 400, "XAmzContentSHA256Mismatch")
			Expect(w.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("reads a chunked part", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			rec := serveReq(w.handler(w.alice), http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", io.MultiReader(strings.NewReader("hello")))
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(w.parts(ctx, "k", id)).To(ConsistOf(HaveField("ETag", md5Hex("hello"))))
		})
	})

	Describe("conditions", func() {
		It("passes CompleteMultipartUpload's If-None-Match, refusing a completion over an existing key", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/src")
			Expect(w.send(w.alice, http.MethodPut, "/plain/src?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			expectError(w.send(w.alice, http.MethodPost, "/plain/src?uploadId="+id, completion(md5Hex("x")), "If-None-Match", "*"), 412, "PreconditionFailed")
			Expect(w.stat(ctx, "src").ETag).To(Equal(md5Hex("hello")))
		})
		It("takes CompleteMultipartUpload's If-Match sent empty as a condition, which fails", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/src")
			Expect(w.send(w.alice, http.MethodPut, "/plain/src?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			expectError(w.send(w.alice, http.MethodPost, "/plain/src?uploadId="+id, completion(md5Hex("x")), "If-Match", ""), 412, "PreconditionFailed")
			Expect(w.stat(ctx, "src").ETag).To(Equal(md5Hex("hello")))
			Expect(w.send(w.alice, http.MethodPost, "/plain/src?uploadId="+id, completion(md5Hex("x")), "If-Match", `"`+md5Hex("hello")+`"`).Code).To(Equal(200))
		})
		DescribeTable("answers a copy-source condition a GET would answer 304 with 304, copying nothing",
			func(ctx SpecContext, name, value string) {
				id := w.initUpload(w.alice, "/plain/k")
				rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "",
					"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src", name, value)
				Expect(rec.Code).To(Equal(304), rec.Body.String())
				Expect(w.parts(ctx, "k", id)).To(BeEmpty())
			},
			Entry("an If-None-Match that matches", "X-Amz-Copy-Source-If-None-Match", md5Hex("hello")),
			Entry("an If-Modified-Since after the source's mtime", "X-Amz-Copy-Source-If-Modified-Since", "Tue, 29 Sep 2026 00:00:00 GMT"),
		)
		It("copies a part under copy-source conditions that hold", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "", "Content-Length", "0", "X-Amz-Copy-Source", "/plain/src",
				"X-Amz-Copy-Source-If-Match", md5Hex("hello"), "X-Amz-Copy-Source-If-None-Match", `"other"`,
				"X-Amz-Copy-Source-If-Modified-Since", "Sun, 27 Sep 2026 00:00:00 GMT", "X-Amz-Copy-Source-If-Unmodified-Since", "Tue, 29 Sep 2026 00:00:00 GMT")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
		})
	})

	Describe("response headers", func() {
		It("answers a part and a copied part with the status rgw_s3_success_create_obj_status names", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			w.conf["rgw_s3_success_create_obj_status"] = "201"
			rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x")
			Expect(rec.Code).To(Equal(201))
			Expect(rec.Header().Get("ETag")).To(Equal(`"` + md5Hex("x") + `"`))
			rec = w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", "", "Content-Length", "0", "X-Amz-Copy-Source", "/plain/src")
			Expect(rec.Code).To(Equal(201), "send_response sets the status before either branch, rgw_rest_s3.cc:2734-2740")
			Expect(rec.Body.String()).To(ContainSubstring("<CopyPartResult"))
		})
		It("sends Rgwx-Mtime to a system request's part, and not with a copied part", func() {
			system := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				return &op.AuthResult{Identity: op.Identity{
					User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: op.OpTypeAll, System: true, Admin: true,
				}}, nil
			})
			h := s3.NewHandler(w.env, system, testConfig(s3.Config{}))
			id := w.initUpload(w.alice, "/plain/k")
			rec := serveReq(h, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", strings.NewReader("x"))
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Header().Get("Rgwx-Mtime")).To(Equal(strconv.FormatInt(writeMtime.Unix(), 10) + ".000000000"))
			rec = serveReq(h, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", nil, "Content-Length", "0", "X-Amz-Copy-Source", "/plain/src")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Header()).NotTo(HaveKey("Rgwx-Mtime"), "the copy branch returns before dump_epoch_header, rgw_rest_s3.cc:2771")
		})
		It("marks every multipart success to a requester-pays bucket's non-owner", func(ctx SpecContext) {
			w.grantBob(ctx, acl.PermFullControl)
			w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.RequesterPays = true })
			payer := []string{"X-Amz-Request-Payer", "requester"}
			rec := w.send(w.bob, http.MethodPost, "/plain/k?uploads", "", payer...)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Header().Get("X-Amz-Request-Charged")).To(Equal("requester"))
			id := uploadIDPattern.FindStringSubmatch(rec.Body.String())[1]
			for _, r := range []*httptest.ResponseRecorder{
				w.send(w.bob, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x", payer...),
				w.send(w.bob, http.MethodGet, "/plain/k?uploadId="+id, "", payer...),
				w.send(w.bob, http.MethodGet, "/plain?uploads", "", payer...),
				w.send(w.bob, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x")), payer...),
			} {
				Expect(r.Code).To(Equal(200), r.Body.String())
				Expect(r.Header().Get("X-Amz-Request-Charged")).To(Equal("requester"))
			}
			id = w.initUpload(w.bob, "/plain/j", payer...)
			rec = w.send(w.bob, http.MethodDelete, "/plain/j?uploadId="+id, "", payer...)
			Expect(rec.Code).To(Equal(204))
			Expect(rec.Header().Get("X-Amz-Request-Charged")).To(Equal("requester"))
		})
	})

	Describe("documents", func() {
		It("escapes text as XMLFormatter does", func(ctx SpecContext) {
			rec := w.send(w.alice, http.MethodPost, "/plain/a%26b%22c?uploads", "")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Key>a&amp;b&quot;c</Key>"))
			id := uploadIDPattern.FindStringSubmatch(rec.Body.String())[1]
			Expect(w.send(w.alice, http.MethodPut, "/plain/a%26b%22c?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			Expect(w.send(w.alice, http.MethodGet, "/plain/a%26b%22c?uploadId="+id, "").Body.String()).To(ContainSubstring("<Key>a&amp;b&quot;c</Key>"))
			Expect(w.send(w.alice, http.MethodGet, "/plain?uploads", "").Body.String()).To(ContainSubstring("<Key>a&amp;b&quot;c</Key>"))
			rec = w.send(w.alice, http.MethodPost, "/plain/a%26b%22c?uploadId="+id, completion(md5Hex("x")))
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Location>http://example.com/plain/a&amp;b&quot;c</Location><Bucket>plain</Bucket><Key>a&amp;b&quot;c</Key>"),
				"the Location's key is not url-encoded, rgw_rest_s3.cc:4096-4100")
		})

		It("renders the tenant in Location and in each document's Tenant element", func(ctx SpecContext) {
			carol := w.store.AddUser(meta.UserInfo{UserID: meta.UserID{Tenant: "t1", ID: "carol"}, DisplayName: "Carol", OpMask: op.OpTypeAll})
			owner := meta.UserOwner(carol.Info.UserID)
			_, err := w.store.CreateBucket(ctx, op.CreateBucketParams{
				Tenant: "t1", Name: "tb", Owner: owner, Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs: map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "Carol"))},
			})
			Expect(err).NotTo(HaveOccurred())
			auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				return &op.AuthResult{Identity: op.Identity{User: &carol.Info, Owner: owner, Tenant: "t1", OpMask: op.OpTypeAll}}, nil
			})
			h := s3.NewHandler(w.env, auth, testConfig(s3.Config{}))
			rec := serveReq(h, http.MethodPost, "/tb/k?uploads", nil)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring(`<InitiateMultipartUploadResult ` + xmlnsS3 + `><Tenant>t1</Tenant><Bucket>tb</Bucket><Key>k</Key>`))
			id := uploadIDPattern.FindStringSubmatch(rec.Body.String())[1]
			Expect(serveReq(h, http.MethodPut, "/tb/k?uploadId="+id+"&partNumber=1", strings.NewReader("x")).Code).To(Equal(200))
			rec = serveReq(h, http.MethodGet, "/tb/k?uploadId="+id, nil)
			Expect(rec.Body.String()).To(ContainSubstring(`<ListPartsResult ` + xmlnsS3 + `><Tenant>t1</Tenant><Bucket>tb</Bucket>`))
			Expect(rec.Body.String()).To(ContainSubstring(`<Owner><ID>t1$carol</ID><DisplayName>Carol</DisplayName></Owner>`))
			rec = serveReq(h, http.MethodGet, "/tb?uploads", nil)
			Expect(rec.Body.String()).To(ContainSubstring(`<ListMultipartUploadsResult ` + xmlnsS3 + `><Tenant>t1</Tenant><Bucket>tb</Bucket>`))
			Expect(rec.Body.String()).To(ContainSubstring(`<Initiator><ID>t1$carol</ID><DisplayName>Carol</DisplayName></Initiator>`))
			rec = serveReq(h, http.MethodPost, "/tb/k?uploadId="+id, strings.NewReader(completion(md5Hex("x"))))
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring(`<Location>http://example.com/t1:tb/k</Location><Tenant>t1</Tenant><Bucket>tb</Bucket><Key>k</Key>`))
		})

		It("builds Location with https for a TLS request", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://s3.example.com/plain/k?uploadId="+id, strings.NewReader(completion(md5Hex("x"))))
			rec := httptest.NewRecorder()
			w.handler(w.alice).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Location>https://s3.example.com/plain/k</Location>"))
		})

		It("clamps max-uploads and max-parts to rgw_max_listing_results and defaults them to 1000", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			Expect(w.send(w.alice, http.MethodGet, "/plain?uploads&max-uploads=5000", "").Body.String()).To(ContainSubstring("<MaxUploads>1000</MaxUploads>"))
			Expect(w.send(w.alice, http.MethodGet, "/plain?uploads&max-uploads=", "").Body.String()).To(ContainSubstring("<MaxUploads>1000</MaxUploads>"))
			Expect(w.send(w.alice, http.MethodGet, "/plain?uploads&max-uploads=-3", "").Body.String()).To(ContainSubstring("<MaxUploads>0</MaxUploads>"))
			Expect(w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id+"&max-parts=", "").Body.String()).To(ContainSubstring("<MaxParts>1000</MaxParts>"))
			Expect(w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id+"&max-parts=99999", "").Body.String()).To(ContainSubstring("<MaxParts>1000</MaxParts>"))
			w.conf["rgw_max_listing_results"] = "7"
			Expect(w.send(w.alice, http.MethodGet, "/plain?uploads&max-uploads=5000", "").Body.String()).To(ContainSubstring("<MaxUploads>7</MaxUploads>"))
			Expect(w.send(w.alice, http.MethodGet, "/plain?uploads", "").Body.String()).To(ContainSubstring("<MaxUploads>1000</MaxUploads>"),
				"parse_value_and_bound bounds a value sent, not its default, rgw_op.h:2640-2660")
			body := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id+"&max-parts=0", "").Body.String()
			Expect(body).To(ContainSubstring("<NextPartNumberMarker>0</NextPartNumberMarker><MaxParts>0</MaxParts><IsTruncated>true</IsTruncated>"))
			Expect(body).NotTo(ContainSubstring("<Part>"))
		})

		It("defaults max-uploads to Tentacle's rgw_max_listing_results bound of 5000", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Tentacle)
			Expect(w.send(w.alice, http.MethodGet, "/plain?uploads&max-uploads=9000", "").Body.String()).To(ContainSubstring("<MaxUploads>5000</MaxUploads>"))
		})

		It("takes the part-number marker as strict_strtol does, a negative one included", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", "y").Code).To(Equal(200))
			body := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id+"&part-number-marker=1", "").Body.String()
			Expect(body).To(ContainSubstring("<PartNumberMarker>1</PartNumberMarker><NextPartNumberMarker>2</NextPartNumberMarker>"))
			Expect(body).To(ContainSubstring("<PartNumber>2</PartNumber>"))
			Expect(body).NotTo(ContainSubstring("<PartNumber>1</PartNumber>"))
			body = w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id+"&part-number-marker=-5", "").Body.String()
			Expect(body).To(ContainSubstring("<PartNumberMarker>-5</PartNumberMarker><NextPartNumberMarker>2</NextPartNumberMarker>"))
		})

		It("echoes the markers as get_params keeps them: the upload id marker only beside a key marker", func(ctx SpecContext) {
			w.initUpload(w.alice, "/plain/k")
			body := w.send(w.alice, http.MethodGet, "/plain?uploads&key-marker=a&upload-id-marker=2~x&delimiter=/", "").Body.String()
			Expect(body).To(ContainSubstring("<Bucket>plain</Bucket><KeyMarker>a</KeyMarker><UploadIdMarker>2~x</UploadIdMarker>"))
			Expect(body).To(ContainSubstring("<MaxUploads>1000</MaxUploads><Delimiter>/</Delimiter><IsTruncated>false</IsTruncated>"))
			body = w.send(w.alice, http.MethodGet, "/plain?uploads&upload-id-marker=2~x", "").Body.String()
			Expect(body).NotTo(ContainSubstring("<UploadIdMarker>"), "an upload id marker without a key marker is not echoed")
			Expect(body).To(ContainSubstring("<Bucket>plain</Bucket><NextKeyMarker>k</NextKeyMarker>"))
		})

		It("url-encodes keys and common prefixes without the slash under encoding-type=url, and no other element", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/a%20b/c+d")
			w.initUpload(w.alice, "/plain/x%20y/z")
			body := w.send(w.alice, http.MethodGet, "/plain?uploads&encoding-type=url&prefix=a%20b/", "").Body.String()
			Expect(body).To(ContainSubstring("<Prefix>a b/</Prefix>"), "dump_string, rgw_rest_s3.cc:4191")
			Expect(body).To(ContainSubstring("<NextKeyMarker>a b/c+d</NextKeyMarker>"))
			Expect(body).To(ContainSubstring("<Upload><Key>a%20b/c%2Bd</Key><UploadId>" + id + "</UploadId>"))
			body = w.send(w.alice, http.MethodGet, "/plain?uploads&encoding-type=url&delimiter=/", "").Body.String()
			Expect(body).To(HaveSuffix("<CommonPrefixes><Prefix>a%20b/</Prefix><Prefix>x%20y/</Prefix></CommonPrefixes></ListMultipartUploadsResult>"),
				"one CommonPrefixes element holding every prefix, rgw_rest_s3.cc:4219-4225")
			body = w.send(w.alice, http.MethodGet, "/plain?uploads&delimiter=/", "").Body.String()
			Expect(body).To(ContainSubstring("<CommonPrefixes><Prefix>a b/</Prefix><Prefix>x y/</Prefix></CommonPrefixes>"))
		})

		for _, c := range []struct {
			rel                 denc.Release
			lastModified, start string
		}{
			{denc.Squid, "2026-09-28T12:00:00.456Z", "2026-09-28T12:00:00.456Z"},
			{denc.Tentacle, "2026-09-28T12:00:00.000Z", "2026-09-28T12:00:00.456Z"},
		} {
			It("renders ListParts' LastModified and ListMultipartUploads' Initiated as "+c.rel.String()+" does", func(ctx SpecContext) {
				w = newWriteWorldAt(ctx, c.rel, writeMtime.Add(456*time.Millisecond))
				id := w.initUpload(w.alice, "/plain/k")
				Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
				Expect(w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "").Body.String()).To(ContainSubstring("<LastModified>"+c.lastModified+"</LastModified>"),
					"dump_time on Squid, dump_time_exact_seconds on Tentacle (v20.2.4 rgw_rest_s3.cc:4704)")
				Expect(w.send(w.alice, http.MethodGet, "/plain?uploads", "").Body.String()).To(ContainSubstring("<Initiated>" + c.start + "</Initiated>"))
			})
		}

		It("leaves out an empty DisplayName, as dump_owner does", func(ctx SpecContext) {
			anon := w.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "nameless"}, OpMask: op.OpTypeAll})
			p := acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice")
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "nameless", Permission: acl.PermFullControl})
			w.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodeACL(p)})
			id := w.initUpload(anon, "/plain/k")
			Expect(w.send(anon, http.MethodGet, "/plain/k?uploadId="+id, "").Body.String()).To(ContainSubstring("<Owner><ID>nameless</ID></Owner>"))
			Expect(w.send(anon, http.MethodGet, "/plain?uploads", "").Body.String()).To(ContainSubstring("<Initiator><ID>nameless</ID></Initiator><Owner><ID>nameless</ID></Owner>"))
		})
	})

	Describe("framing", func() {
		It("frames both listings chunked over HTTP, with no Content-Length and no Accept-Ranges", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			srv := serveWorld(w)
			for _, target := range []string{"/plain/k?uploadId=" + id, "/plain?uploads"} {
				resp := doRequest(ctx, srv, http.MethodGet, target, "")
				b, err := io.ReadAll(resp.Body)
				Expect(resp.Body.Close()).To(Succeed())
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.StatusCode).To(Equal(200), target)
				Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "end_header(..., CHUNKED_TRANSFER_ENCODING), rgw_rest_s3.cc:4128 and :4181")
				Expect(resp.ContentLength).To(BeEquivalentTo(-1), target)
				Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty(), target)
				Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"), target)
				Expect(string(b)).To(HavePrefix(xmlDecl), target)
			}
		})
		It("serves HEAD for both listings with the headers alone: Content-Length 0, no Accept-Ranges, no body", func(ctx SpecContext) {
			id := w.initUpload(w.alice, "/plain/k")
			for _, target := range []string{"/plain/k?uploadId=" + id, "/plain?uploads"} {
				rec := w.send(w.alice, http.MethodHead, target, "")
				Expect(rec.Code).To(Equal(200), target)
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), target)
				Expect(rec.Header().Get("Content-Length")).To(Equal("0"), "the buffering filter completes a HEAD, rgw_client_io_filters.h:221-253")
				Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), target)
				Expect(rec.Header()).NotTo(HaveKey("Transfer-Encoding"), "dump_chunked_encoding skips a HEAD, rgw_rest.cc:399-411")
				Expect(rec.Body.Len()).To(BeZero(), target)
			}
			Expect(w.send(w.alice, http.MethodHead, "/plain/k?uploadId=2~nope", "").Code).To(Equal(404), "an ordinary error, end_header's error branch")
		})
	})

	It("keeps the attrs an upload's request names out of the completion's maps", func(ctx SpecContext) {
		id := w.initUpload(w.alice, "/plain/k", "X-Amz-Meta-Color", "blue")
		Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
		Expect(w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id+"&x-amz-meta-q=qv", completion(md5Hex("x")), "X-Amz-Meta-Other", "o").Code).To(Equal(200))
		attrs := maps.Clone(w.stat(ctx, "k").Attrs)
		Expect(attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"color", []byte("blue\x00")))
		Expect(attrs).NotTo(HaveKey(meta.AttrMetaPrefix + "other"))
		Expect(attrs).NotTo(HaveKey(meta.AttrMetaPrefix + "q"))
	})
	DescribeTable("orders each refusal against the permission check as radosgw does",
		func(ctx SpecContext, who func() *op.UserRecord, setup func(ctx context.Context), build func(id string) (method, target, body string, hdr []string), status int, code string) {
			id := w.initUpload(w.alice, "/plain/k")
			if setup != nil {
				setup(ctx)
			}
			method, target, body, hdr := build(id)
			expectError(w.send(who(), method, target, body, hdr...), status, code)
			Expect(w.parts(ctx, "k", id)).To(BeEmpty())
			Expect(w.uploads(ctx)).To(HaveLen(1))
		},
		Entry("an UploadPart's SSE header, refused in get_params before verify_permission (rgw_op.cc:3911)",
			func() *op.UserRecord { return w.bob }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Server-Side-Encryption", "AES256"}
			}, 501, "NotImplemented"),
		Entry("an UploadPart's SSE query parameter, refused before verify_permission",
			func() *op.UserRecord { return w.bob }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1&x-amz-server-side-encryption=AES256", "x", nil
			}, 501, "NotImplemented"),
		Entry("a CreateMultipartUpload's SSE header, read in execute once authorized (rgw_op.cc:6299)",
			func() *op.UserRecord { return w.bob }, nil, func(string) (string, string, string, []string) {
				return http.MethodPost, "/plain/j?uploads", "", []string{"X-Amz-Server-Side-Encryption", "AES256"}
			}, 403, "AccessDenied"),
		Entry("a CreateMultipartUpload's x-amz-tagging, read in execute once authorized",
			func() *op.UserRecord { return w.bob }, nil, func(string) (string, string, string, []string) {
				return http.MethodPost, "/plain/j?uploads", "", []string{"X-Amz-Tagging", "a=b&"}
			}, 403, "AccessDenied"),
		Entry("an UploadPart's legal hold on a bucket with object lock, refused once authorized",
			func() *op.UserRecord { return w.bob }, func(ctx context.Context) {
				w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketObjLockEnabled | meta.BucketVersioned })
			}, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Object-Lock-Legal-Hold", "ON"}
			}, 403, "AccessDenied"),
		Entry("an UploadPart's Content-MD5, decoded in execute once authorized (rgw_op.cc:4184-4198)",
			func() *op.UserRecord { return w.bob }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"Content-MD5", "nope"}
			}, 403, "AccessDenied"),
		Entry("an UploadPart's attr over rgw_max_attr_size, built once authorized",
			func() *op.UserRecord { return w.bob }, func(context.Context) { w.conf["rgw_max_attr_size"] = "3" }, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Meta-K", "abcd"}
			}, 403, "AccessDenied"),
		Entry("the same attr from the uploader, refused as UnknownError",
			func() *op.UserRecord { return w.alice }, func(context.Context) { w.conf["rgw_max_attr_size"] = "3" }, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Meta-K", "abcd"}
			}, 500, "UnknownError"),
		Entry("an UploadPart's storage class the zone lacks, which init_permissions refuses first (rgw_op.cc:576-583), refused only once authorized",
			func() *op.UserRecord { return w.bob }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Storage-Class", "NOPE"}
			}, 403, "AccessDenied"),
		Entry("the same storage class from the uploader, refused as InvalidArgument",
			func() *op.UserRecord { return w.alice }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Storage-Class", "NOPE"}
			}, 400, "InvalidArgument"),
		Entry("an UploadPart's SSE header, a refusal of the request alone, ahead of a public canned ACL under a block, which waits for authorization",
			func() *op.UserRecord { return w.alice }, func(ctx context.Context) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
			}, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x",
					[]string{"X-Amz-Acl", "public-read", "X-Amz-Server-Side-Encryption", "AES256"}
			}, 501, "NotImplemented"),
		Entry("an UploadPart's public canned ACL under a block of public ACLs, refused once authorized",
			func() *op.UserRecord { return w.alice }, func(ctx context.Context) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
			}, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Acl", "public-read"}
			}, 403, "AccessDenied"),
		Entry("an UploadPart's grant naming no user, looked up only once authorized",
			func() *op.UserRecord { return w.bob }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Grant-Read", `id="nobody"`}
			}, 403, "AccessDenied"),
		Entry("an UploadPart's legal hold on a bucket without object lock, refused only once authorized",
			func() *op.UserRecord { return w.bob }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Object-Lock-Legal-Hold", "ON"}
			}, 403, "AccessDenied"),
		Entry("an UploadPart's legal hold value the request alone refuses, before authorization",
			func() *op.UserRecord { return w.bob }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "x", []string{"X-Amz-Object-Lock-Legal-Hold", "on"}
			}, 400, "InvalidArgument"),
		Entry("an UploadPartCopy's storage class the zone lacks, checked before the source is read, as init_permissions checks it (rgw_op.cc:576-583)",
			func() *op.UserRecord { return w.alice }, nil, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
					[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/nope/src", "X-Amz-Storage-Class", "NOPE"}
			}, 400, "InvalidArgument"),
		Entry("an UploadPartCopy source init_processing cannot parse, refused before a public canned ACL",
			func() *op.UserRecord { return w.alice }, func(ctx context.Context) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
			}, func(id string) (string, string, string, []string) {
				return http.MethodPut, "/plain/k?uploadId=" + id + "&partNumber=1", "",
					[]string{"Content-Length", "0", "X-Amz-Copy-Source", "nope", "X-Amz-Acl", "public-read"}
			}, 400, "InvalidArgument"),
	)

	It("serves an UploadPart naming a storage class the zone has", func(ctx SpecContext) {
		id := w.initUpload(w.alice, "/plain/k")
		Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x", "X-Amz-Storage-Class", "STANDARD").Code).To(Equal(200))
	})

	It("answers a completion of a missing upload as the driver's lock does, 500 and radosgw's message", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId=2~nope", completion(md5Hex("x")))
		expectError(rec, 500, "InternalError")
		Expect(rec.Body.String()).To(ContainSubstring("<Message>This multipart completion is already in progress</Message>"))
		Expect(w.stat(ctx, "k").Exists).To(BeFalse())
	})

	DescribeTable("answers a completion retried after it succeeded 200, as check_previously_completed does",
		func(ctx SpecContext, rel denc.Release, wantETag string) {
			w = newWriteWorld(ctx, rel)
			id := w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			first := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x")))
			Expect(first.Code).To(Equal(200), first.Body.String())
			stored := w.stat(ctx, "k").ETag
			Expect(first.Body.String()).To(ContainSubstring("<ETag>&quot;" + stored + "&quot;</ETag>"))

			again := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x")))
			Expect(again.Code).To(Equal(200), again.Body.String())
			if wantETag == "stored" {
				wantETag = stored
			}
			Expect(again.Body.String()).To(ContainSubstring("<Key>k</Key><ETag>&quot;" + wantETag + "&quot;</ETag>"))
			Expect(w.stat(ctx, "k").ETag).To(Equal(stored), "the object is untouched")

			other := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("y")))
			expectError(other, 500, "InternalError")
			Expect(other.Body.String()).To(ContainSubstring("<Message>This multipart completion is already in progress</Message>"))
		},
		Entry("on Squid, whose check sets no ETag (v19.2.6 rgw_op.cc:6543-6580)", denc.Squid, ""),
		Entry("on Tentacle, whose check sets the stored ETag (v20.2.4 rgw_op.cc:7472)", denc.Tentacle, "stored"),
	)

	Describe("CompleteMultipartUpload's Location, compute_domain_uri's", func() {
		// complete completes a one-part upload of key through a handler over
		// cfg, its request's Host host as edit leaves it.
		complete := func(ctx context.Context, cfg s3.Config, host, path string, edit func(*http.Request)) string {
			GinkgoHelper()
			id := w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, path+"?uploadId="+id, strings.NewReader(completion(md5Hex("x"))))
			req.Host = host
			if edit != nil {
				edit(req)
			}
			rec := httptest.NewRecorder()
			s3.NewHandler(w.env, authAs(w.alice), testConfig(cfg)).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			m := regexp.MustCompile(`<Location>([^<]*)</Location>`).FindStringSubmatch(rec.Body.String())
			Expect(m).To(HaveLen(2), rec.Body.String())
			return m[1]
		}
		It("is the configured name the Host matched, as cut from the Host, without a scheme", func(ctx SpecContext) {
			Expect(complete(ctx, s3.Config{DNSNames: []string{"s3.example.com"}}, "S3.Example.com:7480", "/plain/k", nil)).To(Equal("S3.Example.com/plain/k"),
				"s->info.domain, rgw_rest.cc:2163-2165 and rgw_rest.h:806")
		})
		It("is the matched name for a virtual-hosted request too, the bucket after it", func(ctx SpecContext) {
			Expect(complete(ctx, s3.Config{DNSNames: []string{"s3.example.com"}}, "plain.s3.example.com", "/k", nil)).To(Equal("s3.example.com/plain/k"))
		})
		It("carries no scheme under TLS when a name matched", func(ctx SpecContext) {
			Expect(complete(ctx, s3.Config{DNSNames: []string{"s3.example.com"}}, "s3.example.com", "/plain/k", func(r *http.Request) { r.TLS = &tls.ConnectionState{} })).
				To(Equal("s3.example.com/plain/k"))
		})
		It("is rgw_dns_name as configured, a list included, when the Host matched no name", func(ctx SpecContext) {
			w.conf["rgw_dns_name"] = "a.example.com, b.example.com"
			Expect(complete(ctx, s3.Config{DNSNames: []string{"a.example.com", "b.example.com"}}, "192.0.2.1:7480", "/plain/k", nil)).
				To(Equal("a.example.com, b.example.com/plain/k"), "rgw_rest.cc:2178-2180")
		})
		It("is the scheme and the Host as sent when no name is configured", func(ctx SpecContext) {
			Expect(complete(ctx, s3.Config{}, "Other.Test:8080", "/plain/k", nil)).To(Equal("http://Other.Test:8080/plain/k"))
			Expect(complete(ctx, s3.Config{}, "other.test", "/plain/k", func(r *http.Request) { r.TLS = &tls.ConnectionState{} })).
				To(Equal("https://other.test/plain/k"))
		})
		It("is env.get's default, <HTTP_HOST>, for an HTTP/1.0 request that sent no Host", func(ctx SpecContext) {
			Expect(complete(ctx, s3.Config{}, "", "/plain/k", func(r *http.Request) { r.Proto, r.ProtoMinor = "HTTP/1.0", 0 })).
				To(Equal("http://&lt;HTTP_HOST&gt;/plain/k"), "rgw_rest.h:813")
		})
	})

	Describe("a refused requester", func() {
		// The bucket holds everything a refusal could depend on: object lock,
		// versioning, a quota no write fits, a default encryption and a block
		// of public ACLs; alice has an upload of k with a part, and src
		// exists.
		var id string
		BeforeEach(func(ctx SpecContext) {
			id = w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			w.setBucketInfo(ctx, func(i *meta.BucketInfo) {
				i.Flags |= meta.BucketObjLockEnabled | meta.BucketVersioned
				i.Quota = meta.Quota{MaxSize: -1, MaxObjects: 0, Enabled: true}
			})
			w.setBucketAttrs(ctx, map[string][]byte{
				op.AttrBucketEncryption: {1},
				op.AttrPublicAccess:     encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true}),
			})
		})
		DescribeTable("is answered 403 AccessDenied, whatever the store holds",
			func(ctx SpecContext, method string, target func(id string) string, body string, hdr []string) {
				for _, upload := range []string{id, "2~nope"} {
					expectError(w.send(w.bob, method, target(upload), body, hdr...), 403, "AccessDenied")
				}
				Expect(w.uploads(ctx)).To(ConsistOf(HaveField("ID", id)))
				Expect(w.parts(ctx, "k", id)).To(ConsistOf(HaveField("Number", 1)))
			},
			Entry("CreateMultipartUpload", http.MethodPost, func(string) string { return "/plain/j?uploads" }, "", storedStateHeaders()),
			Entry("CreateMultipartUpload with a grant naming no user", http.MethodPost, func(string) string { return "/plain/j?uploads" }, "",
				[]string{"X-Amz-Grant-Read", `id="nobody"`}),
			Entry("UploadPart", http.MethodPut, func(u string) string { return "/plain/k?uploadId=" + u + "&partNumber=2" }, "x", storedStateHeaders()),
			Entry("UploadPart with a grant naming no user", http.MethodPut, func(u string) string { return "/plain/k?uploadId=" + u + "&partNumber=2" }, "x",
				[]string{"X-Amz-Grant-Read", `id="nobody"`}),
			Entry("UploadPartCopy from a source that exists", http.MethodPut, func(u string) string { return "/plain/k?uploadId=" + u + "&partNumber=2" }, "",
				append([]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src"}, storedStateHeaders()...)),
			Entry("UploadPartCopy from a source key that does not exist", http.MethodPut, func(u string) string { return "/plain/k?uploadId=" + u + "&partNumber=2" }, "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/nope"}),
			Entry("UploadPartCopy from a source bucket that does not exist", http.MethodPut, func(u string) string { return "/plain/k?uploadId=" + u + "&partNumber=2" }, "",
				[]string{"Content-Length", "0", "X-Amz-Copy-Source", "/nope/src", "X-Amz-Copy-Source-Range", "bytes=0-1"}),
			Entry("CompleteMultipartUpload", http.MethodPost, func(u string) string { return "/plain/k?uploadId=" + u }, completion(md5Hex("x")),
				[]string{"If-Match", `"other"`}),
			Entry("AbortMultipartUpload", http.MethodDelete, func(u string) string { return "/plain/k?uploadId=" + u }, "", nil),
			Entry("ListParts", http.MethodGet, func(u string) string { return "/plain/k?uploadId=" + u }, "", nil),
			Entry("ListParts of another key's upload", http.MethodGet, func(u string) string { return "/plain/src?uploadId=" + u }, "", nil),
			Entry("ListMultipartUploads", http.MethodGet, func(u string) string { return "/plain?uploads&key-marker=k&upload-id-marker=" + u }, "", nil),
		)
		It("is answered 403 for ListParts with an empty upload id, whether or not the object exists", func() {
			for _, key := range []string{"src", "nope"} {
				expectError(w.send(w.bob, http.MethodGet, "/plain/"+key+"?uploadId=", ""), 403, "AccessDenied")
			}
		})
		It("is answered 403 for ListParts of an upload whose info cannot be read, which its bucket owner is answered", func() {
			w.env.Multipart = brokenUploads{MultipartStore: w.store}
			expectError(w.send(w.bob, http.MethodGet, "/plain/k?uploadId="+id, ""), 403, "AccessDenied")
			expectError(w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, ""), 500, "UnknownError")
		})
	})
})

// storedStateHeaders are headers that make an object write, a multipart one,
// PutObject or CopyObject, consult the bucket, the grantees or the source,
// none of them refused by the request alone.
func storedStateHeaders() []string {
	return []string{
		"X-Amz-Object-Lock-Legal-Hold", "ON", "X-Amz-Storage-Class", "NOPE", "X-Amz-Acl", "public-read",
		"Content-MD5", md5B64("x"),
	}
}
