package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// plainUploadWithPart begins alice's upload of k in plain, with no crypt
// mode and a part of "x", and returns its id.
func plainUploadWithPart(w *writeWorld) string {
	GinkgoHelper()
	id := w.initUpload(w.alice, "/plain/k")
	Expect(w.send(w.alice, http.MethodPut, overTLS+"/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
	return id
}

// completePlain sends alice's completion of upload id of k in plain, naming
// its part of "x", over TLS, authenticated on route, with query added and
// each pair of hdr.
func completePlain(w *writeWorld, route op.AuthRoute, id, query string, hdr ...string) *httptest.ResponseRecorder {
	return w.sendAllOn(route, w.alice, http.MethodPost, overTLS+withQuery("/plain/k?uploadId="+id, query), completion(md5Hex("x")), hdr...)
}

// expectNotCompleted checks that k in plain is not completed, and that upload
// id and its part stay.
func expectNotCompleted(ctx context.Context, w *writeWorld, id string, rel denc.Release) {
	GinkgoHelper()
	Expect(w.stat(ctx, "k").Exists).To(BeFalse(), "release %d: nothing is completed", rel)
	Expect(w.uploads(ctx)).To(HaveLen(1), "release %d: the upload stays", rel)
	Expect(w.parts(ctx, "k", id)).To(HaveLen(1), "release %d: its part stays", rel)
}

// completeEchoHeaders are the encryption headers a completion echoes, which a
// refused one does not.
var completeEchoHeaders = []string{sseHeaderName, sseKMSKeyHeader, sseCAlgorithmHeader, sseKeyMD5Header}

// RGWCompleteMultipart::verify_permission adds the encryption condition keys
// from the request's headers, as a PutObject's does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6348-L6363 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7021-L7036 at v20.2.4),
// so a header with a value passes a Null Deny written to require encryption,
// and radosgw completes an upload stored in the clear. Its get_params files
// the query's encryption parameters too, after that check
// (map_qs_metadata,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4101 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4575 at v20.2.4).
// rgw-go encrypts nothing, so it refuses a completion that asks for
// encryption 501, as it refuses every write that does, once the requester is
// authorized and before anything is completed (docs/differences.md).
var _ = Describe("a CompleteMultipartUpload that asks for encryption", func() {
	DescribeTable("is refused 501 for an upload without a crypt mode on both releases, completing nothing and echoing nothing",
		func(ctx SpecContext, query string, hdr []string) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := plainUploadWithPart(w)
				rec := completePlain(w, op.RouteUnknown, id, query, hdr...)
				expectError(rec, 501, "NotImplemented")
				for _, name := range completeEchoHeaders {
					Expect(rec.Header()).NotTo(haveHeaderAnyCase(name), "release %d", rel)
				}
				expectNotCompleted(ctx, w, id, rel)
			}
		},
		Entry("SSE-S3", "", []string{sseHeaderName, "AES256"}),
		Entry("SSE-KMS", "", []string{sseHeaderName, "aws:kms", sseKMSKeyHeader, "key"}),
		Entry("a KMS key id alone, which no refusal of names precedes on a completion", "", []string{sseKMSKeyHeader, "key"}),
		Entry("SSE-C, which Amazon S3 takes on a completion of an SSE-C upload", "", sseCRequest(encryptedUploadKey())),
		Entry("the SSE-C algorithm alone", "", []string{sseCAlgorithmHeader, "AES256"}),
		Entry("the SSE-C key MD5 alone", "", []string{sseKeyMD5Header, "AAAA"}),
		Entry("any other name under the SSE-C prefix", "", []string{"X-Amz-Server-Side-Encryption-Customer-Other", "x"}),
		Entry("the context", "", []string{sseContextHeader, "{}"}),
		Entry("the bucket key", "", []string{sseBucketKeyHdr, "true"}),
		Entry("a name only the first 20 bytes of the prefix match", "", []string{sseTwentyBytesHdr, "1"}),
		Entry("a copy source's SSE-C key", "", []string{sseCopyKeyHeader, "a2V5"}),
		Entry("a value, then the same header empty: the value wins", "", []string{sseHeaderName, "AES256", sseHeaderName, ""}),
		Entry("the header empty, then a value", "", []string{sseHeaderName, "", sseHeaderName, "AES256"}),
		Entry("x-amz-server-side-encryption empty beside a KMS key id with a value", "", []string{sseHeaderName, "", sseKMSKeyHeader, "key"}),
		Entry("x-amz-server-side-encryption in the query", "x-amz-server-side-encryption=AES256", nil),
		Entry("the KMS key id in the query", "x-amz-server-side-encryption-aws-kms-key-id=key", nil),
		Entry("the SSE-C algorithm in the query, its name in another case", "X-Amz-Server-Side-Encryption-Customer-Algorithm=AES256", nil),
		Entry("a query parameter sent empty, then with a value", "x-amz-server-side-encryption=&x-amz-server-side-encryption=AES256", nil),
		Entry("the header empty, the query's parameter with a value", "x-amz-server-side-encryption=AES256", []string{sseHeaderName, ""}),
	)

	It("completes an upload without a crypt mode whose request names no encryption, or names it under another meta prefix, on both releases", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			for _, prefix := range append([]string{""}, otherPrefixes...) {
				w := newWriteWorld(ctx, rel)
				id := plainUploadWithPart(w)
				var hdr []string
				if prefix != "" {
					hdr = []string{prefix + "Server-Side-Encryption", "AES256", prefix + "Server-Side-Encryption-Customer-Key", "a2V5"}
				}
				rec := completePlain(w, op.RouteUnknown, id, "", hdr...)
				Expect(rec.Code).To(Equal(200), "release %d, prefix %q: %s", rel, prefix, rec.Body.String())
				Expect(w.stat(ctx, "k").Attrs).NotTo(HaveKey(meta.AttrCryptMode), "release %d, prefix %q", rel, prefix)
			}
		}
	})

	It("answers 403 without the header and 501 with a value under a Null Deny on each key the release sets, and 403 for a query parameter the policy does not read, on every route", func(ctx SpecContext) {
		routes := map[string]op.AuthRoute{"unknown": op.RouteUnknown, "SigV4Header": op.RouteSigV4Header, "SigV4Query": op.RouteSigV4Query}
		for _, rel := range sseReleases {
			keys := map[string]string{
				"s3:x-amz-server-side-encryption":                sseHeaderName,
				"s3:x-amz-server-side-encryption-aws-kms-key-id": sseKMSKeyHeader,
			}
			if rel >= denc.Tentacle {
				keys["s3:x-amz-server-side-encryption-customer-algorithm"] = sseCAlgorithmHeader
			}
			for key, header := range keys {
				for name, route := range routes {
					w := newWriteWorld(ctx, rel)
					id := plainUploadWithPart(w)
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny",` +
						`"Principal":{"AWS":["arn:aws:iam:::user/alice"]},"Action":["s3:PutObject"],"Resource":["arn:aws:s3:::plain/*"],` +
						`"Condition":{"Null":{"` + key + `":"true"}}}]}`)})
					none := completePlain(w, route, id, "")
					Expect(none.Code).To(Equal(403), "release %d, %s, %s, no header: %s", rel, key, name, none.Body.String())
					valued := completePlain(w, route, id, "", header, "AES256")
					Expect(valued.Code).To(Equal(501), "release %d, %s, %s, a value: %s", rel, key, name, valued.Body.String())
					Expect(valued.Body.String()).To(ContainSubstring("<Code>NotImplemented</Code>"), "release %d, %s, %s", rel, key, name)
					query := completePlain(w, route, id, strings.ToLower(header)+"=AES256")
					Expect(query.Code).To(Equal(403), "release %d, %s, %s, a query parameter: %s", rel, key, name, query.Body.String())
					expectNotCompleted(ctx, w, id, rel)
				}
			}
		}
	})

	It("refuses the completion of an SSE-C upload radosgw began, its key named, with or without a checksum, on both releases", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			for _, uc := range []op.UploadChecksum{{}, cryptSHA256Upload} {
				w := newWriteWorld(ctx, rel)
				id := w.beganEncryptedWith(ctx, "SSE-C-AES256", uc)
				up, err := w.upload(ctx, "k", id)
				Expect(err).NotTo(HaveOccurred())
				p := op.PutParams{Size: 1}
				if uc.Type != cksum.None {
					p.Checksum = &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}
				}
				_, err = w.store.PutPart(ctx, up, 1, strings.NewReader("x"), p)
				Expect(err).NotTo(HaveOccurred())
				rec := completePlain(w, op.RouteUnknown, id, "", sseCRequest(encryptedUploadKey())...)
				expectError(rec, 501, "NotImplemented")
				for _, name := range completeEchoHeaders {
					Expect(rec.Header()).NotTo(haveHeaderAnyCase(name), "release %d", rel)
				}
				expectNotCompleted(ctx, w, id, rel)
			}
		}
	})

	It("answers a requester the bucket refuses 403 before it refuses the encryption", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			id := plainUploadWithPart(w)
			rec := w.sendAll(w.bob, http.MethodPost, overTLS+"/plain/k?uploadId="+id, completion(md5Hex("x")), sseHeaderName, "AES256")
			expectError(rec, 403, "AccessDenied")
			expectNotCompleted(ctx, w, id, rel)
		}
	})
})
