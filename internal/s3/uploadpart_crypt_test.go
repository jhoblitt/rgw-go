package s3_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// beganEncrypted stores alice's upload of k in plain as a radosgw stores one
// it began with mode, its meta object holding the crypt attrs
// rgw_s3_prepare_encrypt gives it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1045-L1315 at v20.2.4),
// SSE-C's naming encryptedUploadKey, and returns its id. rgw-go answers
// every write that asks for encryption 501, so only the store makes one.
func (w *writeWorld) beganEncrypted(ctx context.Context, mode string) string {
	GinkgoHelper()
	owner := meta.UserOwner(w.alice.Info.UserID)
	keySel := bytes.Repeat([]byte{9}, 32)
	attrs := map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "Alice")), meta.AttrCryptMode: []byte(mode)}
	switch mode {
	case "SSE-C-AES256":
		sum := md5.Sum(encryptedUploadKey()) //nolint:gosec // SSE-C names its key by the key's MD5
		attrs[meta.AttrCryptKeyMD5] = sum[:]
	case "SSE-KMS", "AES256":
		attrs[meta.AttrCryptKeyID] = []byte("kid")
		attrs[meta.AttrCryptPrefix+"keysel"] = keySel
		attrs[meta.AttrCryptPrefix+"context"] = []byte(`{"aws:s3:arn":"arn:aws:s3:::plain/k"}`)
	case "RGW-AUTO":
		attrs[meta.AttrCryptPrefix+"keysel"] = keySel
	}
	up, err := w.store.CreateUpload(ctx, w.bucket(ctx), meta.ObjKey{Name: "k"}, op.UploadParams{
		Owner: owner, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrs,
	})
	Expect(err).NotTo(HaveOccurred())
	return up.ID
}

var _ = Describe("a part of an upload radosgw began encrypted", func() {
	// sendPart sends alice's part 1 of upload id of k in plain over origin,
	// copied from plain's src or with a body, carrying hdr.
	sendPart := func(w *writeWorld, origin, id string, copied bool, hdr []string) *httptest.ResponseRecorder {
		target := origin + "/plain/k?uploadId=" + id + "&partNumber=1"
		if copied {
			return w.send(w.alice, http.MethodPut, target, "", append([]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src"}, hdr...)...)
		}
		return w.send(w.alice, http.MethodPut, target, "x", hdr...)
	}

	DescribeTable("is refused on both releases, uploaded or copied, storing no part",
		func(ctx SpecContext, mode, origin string, status int, code string, hdr []string) {
			for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
				w := newWriteWorld(ctx, rel)
				for _, copied := range []bool{false, true} {
					id := w.beganEncrypted(ctx, mode)
					rec := sendPart(w, origin, id, copied, hdr)
					Expect(rec.Code).To(Equal(status), "release %d, copied %t: %s", rel, copied, rec.Body.String())
					Expect(rec.Body.String()).To(ContainSubstring("<Code>"+code+"</Code>"), "release %d, copied %t", rel, copied)
					Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d, copied %t", rel, copied)
				}
			}
		},
		Entry("SSE-C, the request naming no key, as radosgw refuses it", "SSE-C-AES256", overTLS, 400, "InvalidArgument", nil),
		Entry("SSE-C, the request naming its key, with which radosgw encrypts: 501, as every SSE-C write",
			"SSE-C-AES256", overTLS, 501, "NotImplemented", sseCRequest(encryptedUploadKey())),
		Entry("SSE-C over plain HTTP, under rgw_crypt_require_ssl", "SSE-C-AES256", "", 400, "InvalidRequest", nil),
		Entry("SSE-KMS, with no key server", "SSE-KMS", overTLS, 400, "InvalidArgument", nil),
		Entry("SSE-KMS over plain HTTP, under rgw_crypt_require_ssl", "SSE-KMS", "", 400, "InvalidRequest", nil),
		Entry("SSE-S3, with no vault", "AES256", overTLS, 400, "InvalidArgument", nil),
		Entry("RGW-AUTO over plain HTTP, with no default key", "RGW-AUTO", "", 500, "UnknownError", nil),
		Entry("a GCM mode v20.2.5 adds, which rgw-go does not know", "SSE-KMS-GCM", overTLS, 501, "NotImplemented", nil),
	)

	It("refuses an SSE-S3 upload's part over plain HTTP as each release's radosgw does: Squid checks the transport, Tentacle does not", func(ctx SpecContext) {
		for rel, code := range map[denc.Release]string{denc.Squid: "InvalidRequest", denc.Tentacle: "InvalidArgument"} {
			w := newWriteWorld(ctx, rel)
			for _, copied := range []bool{false, true} {
				id := w.beganEncrypted(ctx, "AES256")
				rec := sendPart(w, "", id, copied, nil)
				Expect(rec.Code).To(Equal(400), "release %d, copied %t: %s", rel, copied, rec.Body.String())
				Expect(rec.Body.String()).To(ContainSubstring("<Code>"+code+"</Code>"), "release %d, copied %t", rel, copied)
				Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d, copied %t", rel, copied)
			}
		}
	})

	It("stores the part of an upload without a crypt mode, on both releases, uploaded or copied", func(ctx SpecContext) {
		for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
			w := newWriteWorld(ctx, rel)
			for _, copied := range []bool{false, true} {
				id := w.initUpload(w.alice, "/plain/k")
				rec := sendPart(w, "", id, copied, nil)
				Expect(rec.Code).To(Equal(200), "release %d, copied %t: %s", rel, copied, rec.Body.String())
				Expect(w.parts(ctx, "k", id)).To(HaveLen(1), "release %d, copied %t", rel, copied)
			}
		}
	})

	It("answers a refused requester 403 before reading the upload", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id := w.beganEncrypted(ctx, "RGW-AUTO")
		rec := w.send(w.bob, http.MethodPut, overTLS+"/plain/k?uploadId="+id+"&partNumber=1", "x")
		Expect(rec.Code).To(Equal(403), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code>"))
	})
})
