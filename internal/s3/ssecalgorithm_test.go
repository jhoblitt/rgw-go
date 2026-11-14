package s3_test

import (
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

const (
	sseCAlgorithmHeader = "X-Amz-Server-Side-Encryption-Customer-Algorithm"
	sseCCopyAlgorithm   = "X-Amz-Copy-Source-Server-Side-Encryption-Customer-Algorithm"
)

// sseCObject stores in a world of release rel the object small as a
// radosgw stores one it encrypted with encryptedUploadKey, and returns the
// world and its handler.
func sseCObject(ctx context.Context, rel denc.Release) (*readWorld, *s3.Handler) {
	GinkgoHelper()
	w := newReadWorld(ctx, rel)
	sum := md5.Sum(encryptedUploadKey()) //nolint:gosec // SSE-C names its key by the key's MD5
	w.setAttrs(ctx, "small", map[string][]byte{meta.AttrCryptMode: []byte("SSE-C-AES256"), meta.AttrCryptKeyMD5: sum[:]})
	return w, w.handler(w.env(nil), w.alice)
}

// sseCReads sends the three reads that run rgw_s3_prepare_decrypt over an
// object's attrs, GET, HEAD and GetObjectAttributes, to small, over TLS
// with hdr, and returns their responses in that order.
func sseCReads(h *s3.Handler, hdr ...string) []*httptest.ResponseRecorder {
	GinkgoHelper()
	return []*httptest.ResponseRecorder{
		serveReq(h, http.MethodGet, overTLS+"/plain/small", nil, hdr...),
		serveReq(h, http.MethodHead, overTLS+"/plain/small", nil, hdr...),
		serveReq(h, http.MethodGet, overTLS+"/plain/small?attributes", nil, hdr...),
	}
}

var _ = Describe("the SSE-C algorithm header of a read", func() {
	// rgw_s3_prepare_decrypt reads the header as beast files it, present
	// when the request sent it, empty or not: an absent one is EINVAL, 400
	// InvalidArgument; anything but AES256, an empty value included, is 400
	// InvalidEncryptionAlgorithmError. A HEAD carries no body to name the code,
	// so its status alone is checked.
	DescribeTable("is answered as rgw_s3_prepare_decrypt answers it, per release, for GET, HEAD and GetObjectAttributes",
		func(ctx SpecContext, hdr []string, status int, code string) {
			for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
				_, h := sseCObject(ctx, rel)
				for i, rec := range sseCReads(h, hdr...) {
					Expect(rec.Code).To(Equal(status), "release %d, read %d: %s", rel, i, rec.Body.String())
					if i != 1 {
						Expect(rec.Body.String()).To(ContainSubstring("<Code>"+code+"</Code>"), "release %d, read %d", rel, i)
					}
				}
			}
		},
		Entry("sent not at all", nil, 400, "InvalidArgument"),
		Entry("sent not at all, the key headers sent", []string{"X-Amz-Server-Side-Encryption-Customer-Key-Md5", "AAAA"}, 400, "InvalidArgument"),
		Entry("sent empty", []string{sseCAlgorithmHeader, ""}, 400, "InvalidEncryptionAlgorithmError"),
		Entry("sent empty, beside a key that names the object's",
			append([]string{sseCAlgorithmHeader, ""}, sseCRequest(encryptedUploadKey())[2:]...), 400, "InvalidEncryptionAlgorithmError"),
		Entry("AES128", []string{sseCAlgorithmHeader, "AES128"}, 400, "InvalidEncryptionAlgorithmError"),
		Entry("aes256, which strcmp tells from AES256", []string{sseCAlgorithmHeader, "aes256"}, 400, "InvalidEncryptionAlgorithmError"),
		Entry("AES256 without a key", []string{sseCAlgorithmHeader, "AES256"}, 400, "InvalidArgument"),
		Entry("AES256 beside the object's key, which rgw-go cannot decrypt", sseCRequest(encryptedUploadKey()), 501, "NotImplemented"),
	)

	It("is read only for an object of mode SSE-C, so an object without a crypt mode is served and an SSE-KMS object answers its own refusal", func(ctx SpecContext) {
		for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
			w := newReadWorld(ctx, rel)
			h := w.handler(w.env(nil), w.alice)
			for i, rec := range sseCReads(h, sseCAlgorithmHeader, "") {
				Expect(rec.Code).To(Equal(200), "release %d, read %d, no crypt mode: %s", rel, i, rec.Body.String())
			}
			w.setAttrs(ctx, "small", map[string][]byte{meta.AttrCryptMode: []byte("SSE-KMS"), meta.AttrCryptKeyID: []byte("kid")})
			for i, rec := range sseCReads(h, sseCAlgorithmHeader, "") {
				Expect(rec.Code).To(Equal(400), "release %d, read %d, SSE-KMS", rel, i)
				if i != 1 {
					Expect(rec.Body.String()).To(ContainSubstring("Failed to retrieve the actual key, kms-keyid: kid"), "release %d, read %d", rel, i)
				}
			}
		}
	})

	It("reaches the op from the wire as a header present with no value, which net/http keeps", func(ctx SpecContext) {
		_, h := sseCObject(ctx, denc.Tentacle)
		srv := httptest.NewTLSServer(h)
		DeferCleanup(srv.Close)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/plain/small", nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header[sseCAlgorithmHeader] = []string{""}
		resp, err := srv.Client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(resp.Body.Close)
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(400), string(body))
		Expect(string(body)).To(ContainSubstring("<Code>InvalidEncryptionAlgorithmError</Code>"))
	})
})

var _ = Describe("an SSE-C algorithm header sent empty on a write", func() {
	// A write refuses a header by its name, whatever its value, so the empty
	// one that radosgw's rgw_s3_prepare_encrypt takes for none is 501 here,
	// as is the one an UploadPart sends to an upload whose prepare_decrypt
	// reads it only for an SSE-C upload.
	DescribeTable("answers a part 501 before the permission check, per release, whatever the upload's mode, storing nothing",
		func(ctx SpecContext, mode string, copied bool, hdr string) {
			for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
				w := newWriteWorld(ctx, rel)
				var id string
				if mode == "" {
					id = w.initUpload(w.alice, "/plain/k")
				} else {
					id = w.beganEncrypted(ctx, mode)
				}
				target := overTLS + "/plain/k?uploadId=" + id + "&partNumber=1"
				if copied {
					expectError(w.send(w.alice, http.MethodPut, target, "", "Content-Length", "0", "X-Amz-Copy-Source", "/plain/src", hdr, ""), 501, "NotImplemented")
				} else {
					expectError(w.send(w.alice, http.MethodPut, target, "x", hdr, ""), 501, "NotImplemented")
				}
				Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d", rel)
			}
		},
		Entry("UploadPart of an upload without a crypt mode, which radosgw stores", "", false, sseCAlgorithmHeader),
		Entry("UploadPart of an SSE-C upload, which radosgw refuses as no AES256", "SSE-C-AES256", false, sseCAlgorithmHeader),
		Entry("UploadPartCopy of an upload without a crypt mode, the request's own header", "", true, sseCAlgorithmHeader),
		Entry("UploadPartCopy of an upload without a crypt mode, the copy-source header", "", true, sseCCopyAlgorithm),
		Entry("UploadPartCopy of an SSE-C upload, the copy-source header", "SSE-C-AES256", true, sseCCopyAlgorithm),
	)

	DescribeTable("answers PutObject and CreateMultipartUpload 501, storing nothing",
		func(ctx SpecContext, method, target, body string) {
			for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
				w := newWriteWorld(ctx, rel)
				expectError(w.send(w.alice, method, overTLS+target, body, sseCAlgorithmHeader, ""), 501, "NotImplemented")
				Expect(w.stat(ctx, "k").Exists).To(BeFalse(), "release %d", rel)
				Expect(w.uploads(ctx)).To(BeEmpty(), "release %d", rel)
			}
		},
		Entry("PutObject", http.MethodPut, "/plain/k", "x"),
		Entry("CreateMultipartUpload", http.MethodPost, "/plain/k?uploads", ""),
	)
})
