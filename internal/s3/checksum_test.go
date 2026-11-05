package s3_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// The armored checksums of "hello", the bytes each write spec sends, of the
// two-part upload's parts, 5 MiB of "a" then "b", and of the three-part
// upload's, 5 MiB of "a", 5 MiB of "c" then "b", computed apart from
// rgw-go: zlib's CRC32, a bitwise CRC32C and CRC64NVME, and hashlib's
// digests, checked against s3-tests' values for 1024 bytes of "A".
const (
	helloCRC32     = "NhCmhg=="
	helloCRC32C    = "mnG7TA=="
	helloCRC64NVME = "M3eFcAZSQlc="
	helloSHA1      = "qvTGHdzF6KLavt4PO0gs2a6pQ00="
	helloSHA256    = "LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="
	helloSHA512    = "m3HSJL1i83hdltRq0+o9czGb+8KJDKra4t/3JRlnPKcjI8PZm6XBHXx6zG4UuMXaDEZjR1wuXDre9G9zvN7AQw=="

	twoPartSHA256Composite = "tfmzeY9XeH2OXmxrtS4iDWlNIKUTAFv0/csHs2lsoD8=-2"
	twoPartSHA512Composite = "FeBLPc5aDO4MWYXmwfb5CgU3Jwxi6ARrjjUvfTYVH9kbzlhebPXhIB6tCE+FZKWpqXvG7Sk7u/oruZkZTWL0DA==-2"
	twoPartCRC32Full       = "rBxv8Q=="
	twoPartCRC32Composite  = "dYPmEQ==-2"

	threePartSHA256Composite = "OCY3WtYi7K6v4iUtW1YIg7A/IOZpEn0Kbppoj0+M+ds=-3"
)

// checksumHeaders is every header h holds under a name that starts with
// x-amz-checksum- or x-rgw-checksum-, in any case, keyed as spelled, so an
// Equal pins each header's spelling and value and that there is no other.
func checksumHeaders(h http.Header) map[string]string {
	got := map[string]string{}
	for k, v := range h {
		l := strings.ToLower(k)
		if strings.HasPrefix(l, "x-amz-checksum-") || strings.HasPrefix(l, "x-rgw-checksum-") {
			got[k] = strings.Join(v, ",")
		}
	}
	return got
}

// encodeCksum is c's record as user.rgw.cksum holds it.
func encodeCksum(c cksum.Cksum) []byte {
	e := denc.NewEncoder()
	c.Encode(e, denc.Tentacle)
	return e.Bytes()
}

// sendHeader serves method and target as alice with body and the headers
// hdr holds, a repeated one included.
func (w *writeWorld) sendHeader(method, target, body string, hdr http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(GinkgoT().Context(), method, target, strings.NewReader(body))
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	w.handler(w.alice).ServeHTTP(rec, req)
	return rec
}

// sendTrailers serves alice's PUT of target with body, whose aws-chunked
// payload the authenticator read keeping trailers.
func (w *writeWorld) sendTrailers(target, body string, trailers keptTrailers, hdr ...string) *httptest.ResponseRecorder {
	auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
		return &op.AuthResult{
			Identity: op.Identity{User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: w.alice.Info.OpMask},
			Body:     strings.NewReader(body), ContentLength: int64(len(body)), Trailers: trailers,
		}, nil
	})
	return serveReq(s3.NewHandler(w.env, auth, testConfig(s3.Config{})), http.MethodPut, target, strings.NewReader(body), hdr...)
}

// twoPartUpload creates an upload of key with create's headers, uploads 5
// MiB of "a" and then "b" as its parts, each with part's headers, and
// returns its id and the completion naming both parts.
func (w *writeWorld) twoPartUpload(key string, create, part []string) (id, doc string) {
	GinkgoHelper()
	return w.partsUpload(key, create, part, bytes.Repeat([]byte("a"), partFiveMiB), []byte("b"))
}

// partsUpload creates an upload of key with create's headers, uploads parts
// in order, each with part's headers, and returns its id and the completion
// naming every part.
func (w *writeWorld) partsUpload(key string, create, part []string, parts ...[]byte) (id, doc string) {
	GinkgoHelper()
	id = w.initUpload(w.alice, "/plain/"+key, create...)
	etags := make([]string, len(parts))
	for i, p := range parts {
		rec := w.sendBytes(w.alice, http.MethodPut, "/plain/"+key+"?uploadId="+id+"&partNumber="+strconv.Itoa(i+1), p, part...)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		etags[i] = md5Of(p)
	}
	return id, completion(etags...)
}

// srcCopyPartResult is the CopyPartResult of a part copied from "src",
// extra following its ETag.
func srcCopyPartResult(extra string) string {
	return xmlDecl + `<CopyPartResult ` + xmlnsS3 + `><LastModified>` + writeMtimeMs +
		`</LastModified><ETag>&quot;` + md5Hex("hello") + `&quot;</ETag>` + extra + `</CopyPartResult>`
}

// initiateResultPattern matches the InitiateMultipartUploadResult of key k in
// plain, whatever its upload id.
func initiateResultPattern() string {
	return `^` + regexp.QuoteMeta(xmlDecl+`<InitiateMultipartUploadResult `+xmlnsS3+`>`) +
		`<Bucket>plain</Bucket><Key>k</Key><UploadId>2~[A-Za-z0-9_-]{31}</UploadId></InitiateMultipartUploadResult>$`
}

// storedDigest is the 32-byte digest the retried completion specs plant,
// which digestArmor armors.
func storedDigest() []byte { return bytes.Repeat([]byte{0xab}, 32) }

const digestArmor = "q6urq6urq6urq6urq6urq6urq6urq6urq6urq6urq6s="

// completeDoc is the CompleteMultipartUploadResult of key in plain whose
// ETag is etag, extra following the ETag.
func completeDoc(key, etag, extra string) string {
	return xmlDecl + `<CompleteMultipartUploadResult ` + xmlnsS3 + `><Location>http://example.com/plain/` + key +
		`</Location><Bucket>plain</Bucket><Key>` + key + `</Key><ETag>&quot;` + etag + `&quot;</ETag>` + extra +
		`</CompleteMultipartUploadResult>`
}

var _ = Describe("checksums on writes", func() {
	var w *writeWorld

	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Context("PutObject on "+rel.String(), func() {
			BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, rel) })

			// RGWPutObj_ObjStore_S3::send_response
			// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2908-L2917 at v20.2.4)
			// answers the two headers for an AWS type; Squid answers what it
			// stores, as on Tentacle (docs/differences.md, "Squid clusters get
			// single-object and part checksums, not an upload's").
			DescribeTable("answers x-amz-checksum-type FULL_OBJECT and the checksum under its type's name",
				func(ctx SpecContext, algorithm, header, value string) {
					rec := w.send(w.alice, http.MethodPut, "/plain/k", "hello", "X-Amz-Checksum-Algorithm", algorithm)
					Expect(rec.Code).To(Equal(200), rec.Body.String())
					Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-type": "FULL_OBJECT", header: value}))
					Expect(w.stat(ctx, "k").Checksum.Armor()).To(Equal(value), "the store computed the algorithm the request named")
				},
				Entry("CRC32", "CRC32", "x-amz-checksum-crc32", helloCRC32),
				Entry("CRC32C", "CRC32C", "x-amz-checksum-crc32c", helloCRC32C),
				Entry("CRC64NVME", "CRC64NVME", "x-amz-checksum-crc64nvme", helloCRC64NVME),
				Entry("SHA1", "SHA1", "x-amz-checksum-sha1", helloSHA1),
				Entry("SHA256", "SHA256", "x-amz-checksum-sha256", helloSHA256),
				Entry("SHA512 under AWS's x-amz-checksum-sha512, not Tentacle's x-rgw-checksum-sha512", "SHA512", "x-amz-checksum-sha512", helloSHA512),
			)
			It("sends neither header for a request that names no checksum, and stores none", func(ctx SpecContext) {
				rec := w.send(w.alice, http.MethodPut, "/plain/k", "hello")
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(BeEmpty())
				Expect(w.stat(ctx, "k").Checksum).To(BeNil())
			})
			It("verifies a value header, answering BadDigest and storing nothing when it differs", func(ctx SpecContext) {
				rec := w.send(w.alice, http.MethodPut, "/plain/k", "hello", "X-Amz-Checksum-Algorithm", "CRC32", "X-Amz-Checksum-Crc32", helloCRC32)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(HaveKeyWithValue("x-amz-checksum-crc32", helloCRC32))

				rec = w.send(w.alice, http.MethodPut, "/plain/j", "hello", "X-Amz-Checksum-Algorithm", "CRC32", "X-Amz-Checksum-Crc32", helloCRC32C)
				expectError(rec, 400, "BadDigest")
				Expect(checksumHeaders(rec.Header())).To(BeEmpty())
				Expect(w.stat(ctx, "j").Exists).To(BeFalse())
			})
			// A header sent empty is present. radosgw's
			// RGWPutObj_Cksum::Factory refuses an empty algorithm EINVAL
			// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611 at v20.2.4,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L46-L58 at v20.2.4),
			// and an empty value under an algorithm is an expected checksum
			// that verify never matches, ERR_BAD_DIGEST
			// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4757-L4790 at v20.2.4).
			// Squid's radosgw reads no checksum header; rgw-go reads them
			// on Squid as on Tentacle (docs/differences.md, "Squid clusters
			// get single-object and part checksums, not an upload's").
			It("refuses an empty value header under an algorithm with BadDigest, storing nothing", func(ctx SpecContext) {
				rec := w.sendHeader(http.MethodPut, "/plain/k", "hello", http.Header{"X-Amz-Checksum-Algorithm": {"CRC32"}, "X-Amz-Checksum-Crc32": {""}})
				expectError(rec, 400, "BadDigest")
				Expect(checksumHeaders(rec.Header())).To(BeEmpty())
				Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			})
			It("refuses an empty algorithm header with InvalidArgument, storing nothing", func(ctx SpecContext) {
				expectError(w.sendHeader(http.MethodPut, "/plain/k", "hello", http.Header{"X-Amz-Checksum-Algorithm": {""}}), 400, "InvalidArgument")
				Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			})
			It("takes the algorithm from a lone value header", func() {
				rec := w.send(w.alice, http.MethodPut, "/plain/k", "hello", "X-Amz-Checksum-Sha256", helloSHA256)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-type": "FULL_OBJECT", "x-amz-checksum-sha256": helloSHA256}))
			})
			It("reads the last of a repeated algorithm header, as radosgw's environment keeps it", func() {
				rec := w.sendHeader(http.MethodPut, "/plain/k", "hello", http.Header{"X-Amz-Checksum-Algorithm": {"MD5", "CRC32"}})
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(HaveKeyWithValue("x-amz-checksum-crc32", helloCRC32))
			})
			It("verifies the checksum trailer x-amz-trailer names from the trailers the payload kept", func(ctx SpecContext) {
				rec := w.sendTrailers("/plain/k", "hello", keptTrailers{"x-amz-checksum-crc32c": helloCRC32C}, "X-Amz-Trailer", "x-amz-checksum-crc32c")
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-type": "FULL_OBJECT", "x-amz-checksum-crc32c": helloCRC32C}))

				rec = w.sendTrailers("/plain/j", "hello", keptTrailers{"x-amz-checksum-crc32c": helloCRC32}, "X-Amz-Trailer", "x-amz-checksum-crc32c")
				expectError(rec, 400, "BadDigest")
				Expect(w.stat(ctx, "j").Exists).To(BeFalse())
			})
			It("refuses an algorithm rgw-go does not compute with InvalidArgument, storing nothing", func(ctx SpecContext) {
				expectError(w.send(w.alice, http.MethodPut, "/plain/k", "hello", "X-Amz-Checksum-Algorithm", "MD5"), 400, "InvalidArgument")
				Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			})
		})

		Context("UploadPart on "+rel.String(), func() {
			BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, rel) })

			It("answers a part's own checksum as a PutObject's", func() {
				id := w.initUpload(w.alice, "/plain/k")
				rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "hello", "X-Amz-Checksum-Algorithm", "CRC64NVME")
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-type": "FULL_OBJECT", "x-amz-checksum-crc64nvme": helloCRC64NVME}))
			})
			It("sends neither header for a part that names no checksum of an upload without a type", func() {
				id := w.initUpload(w.alice, "/plain/k")
				rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "hello")
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(BeEmpty())
			})
		})
	}

	Context("UploadPart and UploadPartCopy of an upload with a checksum type", func() {
		It("answers the upload's type, FULL_OBJECT whatever the upload's flags, on Tentacle", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Tentacle)
			id := w.initUpload(w.alice, "/plain/k", "X-Amz-Checksum-Algorithm", "SHA256")
			up, err := w.upload(ctx, "k", id)
			Expect(err).NotTo(HaveOccurred())
			Expect(up.Checksum).To(Equal(op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}))
			rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "hello")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-type": "FULL_OBJECT", "x-amz-checksum-sha256": helloSHA256}),
				"send_response names FULL_OBJECT for a part, rgw_rest_s3.cc:2915 at v20.2.4")
		})
		It("refuses a part naming another algorithm than its upload's with InvalidRequest, on Tentacle", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Tentacle)
			id := w.initUpload(w.alice, "/plain/k", "X-Amz-Checksum-Algorithm", "SHA256")
			expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "hello", "X-Amz-Checksum-Algorithm", "CRC32"), 400, "InvalidRequest")
			Expect(w.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("computes nothing for a part of an upload Squid created with an algorithm", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Squid)
			id := w.initUpload(w.alice, "/plain/k", "X-Amz-Checksum-Algorithm", "SHA256")
			rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "hello")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(checksumHeaders(rec.Header())).To(BeEmpty())
		})

		// Amazon S3 answers a copied part's checksum as CopyPartResult's
		// Checksum<ALG> element, with no checksum header, where radosgw sends
		// the headers and no element
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2918-L2940 at v20.2.4).
		copyPart := func(id string, hdr ...string) *httptest.ResponseRecorder {
			return w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "",
				append([]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src"}, hdr...)...)
		}
		DescribeTable("answers a copied part's checksum, its upload's type, in CopyPartResult alone, on Tentacle",
			func(ctx SpecContext, algorithm, element string) {
				w = newWriteWorld(ctx, denc.Tentacle)
				id := w.initUpload(w.alice, "/plain/k", "X-Amz-Checksum-Algorithm", algorithm)
				rec := copyPart(id)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(BeEmpty())
				Expect(rec.Body.String()).To(Equal(srcCopyPartResult(element)))
			},
			Entry("CRC32C", "CRC32C", "<ChecksumCRC32C>"+helloCRC32C+"</ChecksumCRC32C>"),
			Entry("SHA512 under AWS's ChecksumSHA512", "SHA512", "<ChecksumSHA512>"+helloSHA512+"</ChecksumSHA512>"),
		)
		It("answers no checksum for a copied part on Squid, whose uploads have no type", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Squid)
			id := w.initUpload(w.alice, "/plain/k", "X-Amz-Checksum-Algorithm", "CRC32C")
			rec := copyPart(id)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(checksumHeaders(rec.Header())).To(BeEmpty())
			Expect(rec.Body.String()).To(Equal(srcCopyPartResult("")))
		})
		It("reads no checksum request header for a copied part", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Tentacle)
			id := w.initUpload(w.alice, "/plain/k")
			rec := copyPart(id, "X-Amz-Checksum-Algorithm", "SHA256", "X-Amz-Checksum-Sha256", helloSHA1)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(checksumHeaders(rec.Header())).To(BeEmpty())
			Expect(rec.Body.String()).To(Equal(srcCopyPartResult("")))
		})
	})

	// RGWInitMultipart_ObjStore_S3::send_response answers the algorithm and no
	// type
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4540-L4542 at v20.2.4);
	// Amazon S3 answers both.
	Context("CreateMultipartUpload", func() {
		DescribeTable("answers x-amz-checksum-algorithm upper-cased and the upload's x-amz-checksum-type, on Tentacle",
			func(ctx SpecContext, hdr []string, want, typ string, uc op.UploadChecksum) {
				w = newWriteWorld(ctx, denc.Tentacle)
				rec := w.send(w.alice, http.MethodPost, "/plain/k?uploads", "", hdr...)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-algorithm": want, "x-amz-checksum-type": typ}))
				Expect(rec.Body.String()).To(MatchRegexp(initiateResultPattern()))
				id := uploadIDPattern.FindStringSubmatch(rec.Body.String())[1]
				up, err := w.upload(ctx, "k", id)
				Expect(err).NotTo(HaveOccurred())
				Expect(up.Checksum).To(Equal(uc))
			},
			Entry("a lower-case name, a digest COMPOSITE", []string{"X-Amz-Checksum-Algorithm", "sha256"}, "SHA256", "COMPOSITE",
				op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}),
			Entry("x-amz-sdk-checksum-algorithm, a CRC created COMPOSITE", []string{"X-Amz-Sdk-Checksum-Algorithm", "CRC32", "X-Amz-Checksum-Type", "COMPOSITE"},
				"CRC32", "COMPOSITE", op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagComposite}),
			Entry("a CRC FULL_OBJECT by default", []string{"X-Amz-Checksum-Algorithm", "CRC32"}, "CRC32", "FULL_OBJECT",
				op.UploadChecksum{Type: cksum.CRC32, Flags: cksum.FlagFullObject}),
			Entry("a CRC FULL_OBJECT under a checksum type that sets no flag", []string{"X-Amz-Checksum-Algorithm", "CRC32C", "X-Amz-Checksum-Type", "other"},
				"CRC32C", "FULL_OBJECT", op.UploadChecksum{Type: cksum.CRC32C}),
			Entry("SHA512", []string{"X-Amz-Checksum-Algorithm", "SHA512"}, "SHA512", "COMPOSITE", op.UploadChecksum{Type: cksum.SHA512, Flags: cksum.FlagComposite}),
		)
		DescribeTable("sends no checksum header for an upload without a type",
			func(ctx SpecContext, rel denc.Release, hdr ...string) {
				w = newWriteWorld(ctx, rel)
				rec := w.send(w.alice, http.MethodPost, "/plain/k?uploads", "", hdr...)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(checksumHeaders(rec.Header())).To(BeEmpty())
				Expect(rec.Body.String()).To(MatchRegexp(initiateResultPattern()))
				Expect(w.uploads(ctx)).To(ConsistOf(HaveField("Checksum", op.UploadChecksum{})))
			},
			Entry("no algorithm, on Tentacle", denc.Tentacle),
			Entry("a name that names no algorithm, on Tentacle", denc.Tentacle, "X-Amz-Checksum-Algorithm", "FOO"),
			Entry("an algorithm on Squid, which keeps no upload type", denc.Squid, "X-Amz-Checksum-Algorithm", "CRC32"),
		)
		DescribeTable("refuses what get_params refuses, creating no upload, on Tentacle",
			func(ctx SpecContext, status int, code string, hdr ...string) {
				w = newWriteWorld(ctx, denc.Tentacle)
				rec := w.send(w.alice, http.MethodPost, "/plain/k?uploads", "", hdr...)
				expectError(rec, status, code)
				Expect(w.uploads(ctx)).To(BeEmpty())
			},
			Entry("a COMPOSITE CRC64NVME, permitted_cksum_algo_and_type's refusal", 400, "InvalidRequest",
				"X-Amz-Checksum-Algorithm", "CRC64NVME", "X-Amz-Checksum-Type", "COMPOSITE"),
			Entry("an algorithm rgw-go does not compute", 400, "InvalidArgument", "X-Amz-Checksum-Algorithm", "MD5"),
		)
	})

	// RGWCompleteMultipart_ObjStore_S3::send_response
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4635-L4640 at v20.2.4),
	// its armor from try_sum_part_cksums
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7145-L7151 at v20.2.4).
	Context("CompleteMultipartUpload", func() {
		DescribeTable("answers the combined checksum and its type after the ETag, on Tentacle",
			func(ctx SpecContext, create []string, element, value, typ string) {
				w = newWriteWorld(ctx, denc.Tentacle)
				id, doc := w.twoPartUpload("k", create, nil)
				rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				want := completeDoc("k", w.stat(ctx, "k").ETag, "<"+element+">"+value+"</"+element+"><ChecksumType>"+typ+"</ChecksumType>")
				Expect(rec.Body.String()).To(Equal(want))
				Expect(checksumHeaders(rec.Header())).To(BeEmpty())
			},
			Entry("a SHA256 composite with its part count", []string{"X-Amz-Checksum-Algorithm", "SHA256"}, "ChecksumSHA256", twoPartSHA256Composite, "COMPOSITE"),
			Entry("a SHA512 composite under AWS's ChecksumSHA512", []string{"X-Amz-Checksum-Algorithm", "SHA512"}, "ChecksumSHA512", twoPartSHA512Composite, "COMPOSITE"),
			Entry("a CRC32 full-object CRC bare", []string{"X-Amz-Checksum-Algorithm", "CRC32"}, "ChecksumCRC32", twoPartCRC32Full, "FULL_OBJECT"),
			Entry("a CRC32 upload created COMPOSITE", []string{"X-Amz-Checksum-Algorithm", "CRC32", "X-Amz-Checksum-Type", "COMPOSITE"},
				"ChecksumCRC32", twoPartCRC32Composite, "COMPOSITE"),
		)
		It("compares the value the completion supplies, answering BadDigest and writing nothing when it differs, on Tentacle", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Tentacle)
			id, doc := w.twoPartUpload("k", []string{"X-Amz-Checksum-Algorithm", "SHA256"}, nil)
			expectError(w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc, "X-Amz-Checksum-Sha256", helloSHA256), 400, "BadDigest")
			Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc, "X-Amz-Checksum-Sha256", twoPartSHA256Composite)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<ChecksumSHA256>" + twoPartSHA256Composite + "</ChecksumSHA256>"))
		})
		It("answers the stored checksum again to a completion retried after it succeeded, on Tentacle", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Tentacle)
			id, doc := w.twoPartUpload("k", []string{"X-Amz-Checksum-Algorithm", "SHA256"}, nil)
			first := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc)
			Expect(first.Code).To(Equal(200), first.Body.String())
			again := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc)
			Expect(again.Code).To(Equal(200), again.Body.String())
			Expect(again.Body.String()).To(Equal(first.Body.String()))
			Expect(again.Body.String()).To(ContainSubstring("<ChecksumSHA256>" + twoPartSHA256Composite + "</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType>"))
		})
		It("suffixes a composite with the completion's part count, three parts answering -3, fresh and retried, on Tentacle", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Tentacle)
			id, doc := w.partsUpload("k", []string{"X-Amz-Checksum-Algorithm", "SHA256"}, nil,
				bytes.Repeat([]byte("a"), partFiveMiB), bytes.Repeat([]byte("c"), partFiveMiB), []byte("b"))
			first := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc)
			Expect(first.Code).To(Equal(200), first.Body.String())
			Expect(first.Body.String()).To(Equal(completeDoc("k", w.stat(ctx, "k").ETag,
				"<ChecksumSHA256>"+threePartSHA256Composite+"</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType>")))
			again := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc)
			Expect(again.Code).To(Equal(200), again.Body.String())
			Expect(again.Body.String()).To(Equal(first.Body.String()))
		})
		DescribeTable("answers no checksum element",
			func(ctx SpecContext, rel denc.Release, create, part []string) {
				w = newWriteWorld(ctx, rel)
				id, doc := w.twoPartUpload("k", create, part)
				rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(rec.Body.String()).To(Equal(completeDoc("k", w.stat(ctx, "k").ETag, "")))
				Expect(w.stat(ctx, "k").Checksum).To(BeNil())
			},
			Entry("for an upload without a type, its parts' own checksums aside, on Tentacle", denc.Tentacle, nil, []string{"X-Amz-Checksum-Algorithm", "SHA256"}),
			Entry("on Squid, whose uploads have no type", denc.Squid, []string{"X-Amz-Checksum-Algorithm", "SHA256"}, []string{"X-Amz-Checksum-Algorithm", "SHA256"}),
		)

		// A retried completion answers the record its head holds, as
		// check_previously_completed decodes it
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7474-L7501 at v20.2.4).
		retried := func(ctx SpecContext, rel denc.Release, stored cksum.Cksum) string {
			GinkgoHelper()
			w = newWriteWorld(ctx, rel)
			id, doc := w.twoPartUpload("k", nil, nil)
			Expect(w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc).Code).To(Equal(200))
			Expect(w.store.SetObjectAttrs(ctx, w.stat(ctx, "k"), map[string][]byte{meta.AttrCksum: encodeCksum(stored)}, nil)).To(Succeed())
			rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, doc)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			return rec.Body.String()
		}
		DescribeTable("types a retried completion's stored record by get_checksum_type, on Tentacle",
			func(ctx SpecContext, flags uint16, extra string) {
				body := retried(ctx, denc.Tentacle, cksum.Cksum{Type: cksum.SHA256, Digest: storedDigest(), Flags: flags})
				Expect(body).To(Equal(completeDoc("k", w.stat(ctx, "k").ETag, extra)))
			},
			Entry("a composite without V2 with its part count", cksum.FlagComposite|cksum.FlagCombined, "<ChecksumSHA256>"+digestArmor+"-2</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType>"),
			Entry("a record without V2 or the composite flags bare, typed COMPOSITE", uint16(0), "<ChecksumSHA256>"+digestArmor+"</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType>"),
			Entry("a V2 full-object record bare", cksum.FlagV2|cksum.FlagCombined|cksum.FlagFullObject, "<ChecksumSHA256>"+digestArmor+"</ChecksumSHA256><ChecksumType>FULL_OBJECT</ChecksumType>"),
		)
		It("answers no stored checksum to a retried completion on Squid", func(ctx SpecContext) {
			body := retried(ctx, denc.Squid, cksum.Cksum{Type: cksum.SHA256, Digest: storedDigest(), Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite})
			Expect(body).To(Equal(completeDoc("k", w.stat(ctx, "k").ETag, "")))
		})
		// Only a Tentacle radosgw completes an xxh3 upload, and its element
		// for one is ChecksumXXH3
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L189-L192 at v20.2.4).
		It("names a stored xxh3 record ChecksumXXHASH3, Amazon S3's name for the type, on Tentacle", func(ctx SpecContext) {
			body := retried(ctx, denc.Tentacle, cksum.Cksum{Type: cksum.XXH3, Digest: storedDigest()[:8], Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite})
			Expect(body).To(Equal(completeDoc("k", w.stat(ctx, "k").ETag,
				"<ChecksumXXHASH3>q6urq6urq6s=-2</ChecksumXXHASH3><ChecksumType>COMPOSITE</ChecksumType>")))
		})
		It("answers no stored record of type none, which holds no digest, on Tentacle", func(ctx SpecContext) {
			body := retried(ctx, denc.Tentacle, cksum.Cksum{Type: cksum.None, Flags: cksum.FlagV2})
			Expect(body).To(Equal(completeDoc("k", w.stat(ctx, "k").ETag, "")))
		})
	})

	It("answers no checksum header for a type AWS does not define, as radosgw's aws() check sends none", func() {
		h := http.Header{}
		s3.SetChecksumHeadersForTest(h, &cksum.Cksum{Type: cksum.BLAKE3, Digest: make([]byte, 32)})
		Expect(h).To(BeEmpty())
		s3.SetChecksumHeadersForTest(h, nil)
		Expect(h).To(BeEmpty())
	})
	It("answers no CopyPartResult checksum for a type AWS does not define, which the document has no element for", func() {
		Expect(s3.CopyPartChecksumForTest(&cksum.Cksum{Type: cksum.BLAKE3, Digest: make([]byte, 32)})).To(BeFalse())
		Expect(s3.CopyPartChecksumForTest(nil)).To(BeFalse())
		Expect(s3.CopyPartChecksumForTest(&cksum.Cksum{Type: cksum.SHA1, Digest: make([]byte, 20)})).To(BeTrue())
	})
})
