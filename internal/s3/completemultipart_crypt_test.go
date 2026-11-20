package s3_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// completeAnswer is a completion's expected response in the encrypted
// completion specs: its status, and either the error's code or, for a
// success, the x-amz-server-side-encryption and KMS key id it echoes.
type completeAnswer struct {
	status        int
	code          string
	sse, kmsKeyID string
}

// The responses the encrypted completion specs expect, the checksum their
// uploads are created with, and SSE-C headers naming a key no upload has.
var (
	completeOK           = completeAnswer{status: 200}
	completeKMS          = completeAnswer{status: 200, sse: "aws:kms", kmsKeyID: "kid"}
	completeSSES3        = completeAnswer{status: 200, sse: "AES256"}
	completeBadKey       = completeAnswer{status: 400, code: "InvalidArgument"}
	completeBadAlgorithm = completeAnswer{status: 400, code: "InvalidEncryptionAlgorithmError"}
	completeInsecure     = completeAnswer{status: 400, code: "InvalidRequest"}
	completeAsked        = completeAnswer{status: 501, code: "NotImplemented"}
	cryptSHA256Upload    = op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}
	otherKeyHeaders      = sseCRequest(bytes.Repeat([]byte{9}, 32))
)

var _ = Describe("CompleteMultipartUpload of an upload radosgw began encrypted", func() {
	const (
		algorithmHeader = "x-amz-server-side-encryption-customer-algorithm"
		keyMD5Header    = "x-amz-server-side-encryption-customer-key-MD5"
		sseHeader       = "x-amz-server-side-encryption"
		kmsKeyIDHeader  = "x-amz-server-side-encryption-aws-kms-key-id"
	)

	// withPart stores alice's upload of k in plain that radosgw began with
	// mode and uc, holding part 1 of "hello" with uc's checksum when it has
	// a type, and returns its id and the completion naming the part.
	withPart := func(ctx context.Context, w *writeWorld, mode string, uc op.UploadChecksum) (id, doc string) {
		GinkgoHelper()
		id = w.beganEncryptedWith(ctx, mode, uc)
		up, err := w.upload(ctx, "k", id)
		Expect(err).NotTo(HaveOccurred())
		p := op.PutParams{Size: 5}
		if uc.Type != cksum.None {
			p.Checksum = &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}
		}
		_, err = w.store.PutPart(ctx, up, 1, strings.NewReader("hello"), p)
		Expect(err).NotTo(HaveOccurred())
		return id, completion(md5Hex("hello"))
	}

	// expectAnswer checks rec against want: a refusal echoes nothing; a
	// success echoes what want names under radosgw's spellings, no SSE-C
	// header, as no request that names a key reaches it, and nothing else.
	expectAnswer := func(rec *httptest.ResponseRecorder, want completeAnswer) {
		GinkgoHelper()
		if want.status != 200 {
			expectError(rec, want.status, want.code)
			for _, name := range []string{algorithmHeader, keyMD5Header, sseHeader, kmsKeyIDHeader} {
				Expect(rec.Header()).NotTo(haveHeaderAnyCase(name))
			}
			return
		}
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Header()).NotTo(haveHeaderAnyCase(algorithmHeader))
		Expect(rec.Header()).NotTo(haveHeaderAnyCase(keyMD5Header))
		for name, v := range map[string]string{sseHeader: want.sse, kmsKeyIDHeader: want.kmsKeyID} {
			if v == "" {
				Expect(rec.Header()).NotTo(haveHeaderAnyCase(name))
			} else {
				Expect(exactHeader(rec.Header(), name)).To(Equal(v), name)
			}
		}
	}

	DescribeTable("answers an SSE-C upload's completion that names no key as Amazon S3 does, and refuses one that names it 501, as rgw-go serves no SSE-C",
		func(ctx SpecContext, checksum bool, origin string, hdr []string, squid, tentacle completeAnswer) {
			uc := op.UploadChecksum{}
			if checksum {
				uc = cryptSHA256Upload
			}
			for rel, want := range map[denc.Release]completeAnswer{denc.Squid: squid, denc.Tentacle: tentacle} {
				w := newWriteWorld(ctx, rel)
				id, doc := withPart(ctx, w, "SSE-C-AES256", uc)
				rec := w.send(w.alice, http.MethodPost, origin+"/plain/k?uploadId="+id, doc, hdr...)
				expectAnswer(rec, want)
				if want.status != 200 {
					Expect(w.uploads(ctx)).To(HaveLen(1), "release %d: the upload is kept", rel)
				}
			}
		},
		Entry("with a checksum, its key named over TLS", true, overTLS, sseCRequest(encryptedUploadKey()), completeAsked, completeAsked),
		Entry("with a checksum, no SSE-C header: Tentacle asks for the key, Squid kept no checksum", true, overTLS, nil, completeOK, completeBadKey),
		Entry("with a checksum, another key named", true, overTLS, otherKeyHeaders, completeAsked, completeAsked),
		Entry("its key named over plain HTTP, the 501 ahead of rgw_crypt_require_ssl", false, "", sseCRequest(encryptedUploadKey()), completeAsked, completeAsked),
		Entry("without a checksum, no SSE-C header, over plain HTTP", false, "", nil, completeOK, completeOK),
		Entry("without a checksum, its key named over TLS", false, overTLS, sseCRequest(encryptedUploadKey()), completeAsked, completeAsked),
		Entry("without a checksum, the key MD5 header alone", false, overTLS,
			[]string{"X-Amz-Server-Side-Encryption-Customer-Key-Md5", "AAAA"}, completeAsked, completeAsked),
		Entry("without a checksum, any header of the SSE-C prefix with a value", false, overTLS,
			[]string{"X-Amz-Server-Side-Encryption-Customer-Other", "x"}, completeAsked, completeAsked),
		Entry("without a checksum, x-amz-server-side-encryption, outside the SSE-C prefix", false, overTLS,
			[]string{"X-Amz-Server-Side-Encryption", "AES256"}, completeAsked, completeAsked),
		Entry("an algorithm header sent empty beside the key, whose value asks", false, overTLS,
			append([]string{"X-Amz-Server-Side-Encryption-Customer-Algorithm", ""}, sseCRequest(encryptedUploadKey())[2:]...),
			completeAsked, completeAsked),
		Entry("an algorithm header sent empty alone, which beast files, so it is no AES256", false, overTLS,
			[]string{"X-Amz-Server-Side-Encryption-Customer-Algorithm", ""}, completeBadAlgorithm, completeBadAlgorithm),
		Entry("an algorithm other than AES256", false, overTLS,
			append([]string{"X-Amz-Server-Side-Encryption-Customer-Algorithm", "AES128"}, sseCRequest(encryptedUploadKey())[2:]...),
			completeAsked, completeAsked),
	)

	// send_response dumps crypt_http_responses whatever op_ret is
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4604-L4612 at v20.2.4).
	It("sends the decision's headers on an error after it passed, as radosgw does", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id, _ := withPart(ctx, w, "SSE-KMS", op.UploadChecksum{})
		rec := w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+id, completion(md5Hex("other")))
		expectError(rec, 400, "InvalidPart")
		Expect(exactHeader(rec.Header(), sseHeader)).To(Equal("aws:kms"))
		Expect(exactHeader(rec.Header(), kmsKeyIDHeader)).To(Equal("kid"))
	})

	DescribeTable("completes an SSE-KMS, SSE-S3 or RGW-AUTO upload where Amazon S3 needs no key, on Tentacle, and every one on Squid",
		func(ctx SpecContext, mode string, checksum bool, origin string, tentacle completeAnswer) {
			uc := op.UploadChecksum{}
			if checksum {
				uc = cryptSHA256Upload
			}
			for rel, want := range map[denc.Release]completeAnswer{denc.Squid: completeOK, denc.Tentacle: tentacle} {
				w := newWriteWorld(ctx, rel)
				id, doc := withPart(ctx, w, mode, uc)
				expectAnswer(w.send(w.alice, http.MethodPost, origin+"/plain/k?uploadId="+id, doc), want)
			}
		},
		Entry("SSE-KMS with a checksum, with no key server", "SSE-KMS", true, overTLS, completeBadKey),
		Entry("SSE-KMS with a checksum over plain HTTP, under rgw_crypt_require_ssl", "SSE-KMS", true, "", completeInsecure),
		Entry("SSE-KMS without a checksum, echoing aws:kms and its key id", "SSE-KMS", false, overTLS, completeKMS),
		Entry("SSE-KMS without a checksum over plain HTTP, under rgw_crypt_require_ssl", "SSE-KMS", false, "", completeInsecure),
		Entry("SSE-S3 with a checksum over plain HTTP, echoing AES256", "AES256", true, "", completeSSES3),
		Entry("RGW-AUTO, with no default key", "RGW-AUTO", false, overTLS, completeAnswer{status: 500, code: "UnknownError"}),
	)

	It("echoes a stored KMS key id with each control byte but a tab as a space, and the rest as stored, on Tentacle", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		owner := meta.UserOwner(w.alice.Info.UserID)
		up, err := w.store.CreateUpload(ctx, w.bucket(ctx), meta.ObjKey{Name: "k"}, op.UploadParams{
			Owner: owner, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs: map[string][]byte{
				meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "Alice")), meta.AttrCryptMode: []byte("SSE-KMS"), meta.AttrCryptKeyID: []byte("k\tid\x01\x7f\x00\x80"),
			},
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = w.store.PutPart(ctx, up, 1, strings.NewReader("hello"), op.PutParams{Size: 5})
		Expect(err).NotTo(HaveOccurred())
		rec := w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+up.ID, completion(md5Hex("hello")))
		expectAnswer(rec, completeAnswer{status: 200, sse: "aws:kms", kmsKeyID: "k\tid   \x80"})
	})

	// A completion whose upload id names no meta object, a retry of one that
	// succeeded or any id made up, is answered from the object the key holds,
	// its combined checksum included, as radosgw's check_previously_completed
	// answers it
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7433-L7504 at v20.2.4).
	// rgw-go decides that answer as it decides a live upload.
	Describe("a completion answered from the object already completed", func() {
		// completed completes k from an upload radosgw began with mode and a
		// SHA256 checksum, as a gateway that holds its key completes it, and
		// returns the upload's id and the completion document.
		completed := func(ctx context.Context, w *writeWorld, mode string) (id, doc string) {
			GinkgoHelper()
			id, doc = withPart(ctx, w, mode, cryptSHA256Upload)
			up, err := w.upload(ctx, "k", id)
			Expect(err).NotTo(HaveOccurred())
			_, err = w.store.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: md5Hex("hello")}})
			Expect(err).NotTo(HaveOccurred())
			return id, doc
		}

		DescribeTable("refuses it as the live completion is refused, rendering no checksum, on Tentacle",
			func(ctx SpecContext, mode string, madeUp bool, origin string, hdr []string, want completeAnswer) {
				w := newWriteWorld(ctx, denc.Tentacle)
				id, doc := completed(ctx, w, mode)
				if madeUp {
					id = "2~bogusbogusbogus"
				}
				rec := w.send(w.alice, http.MethodPost, origin+"/plain/k?uploadId="+id, doc, hdr...)
				expectAnswer(rec, want)
				Expect(rec.Body.String()).NotTo(ContainSubstring("Checksum"))
			},
			Entry("SSE-C, its own id, no key, over plain HTTP", "SSE-C-AES256", false, "", nil, completeInsecure),
			Entry("SSE-C, an id made up, no key, over plain HTTP", "SSE-C-AES256", true, "", nil, completeInsecure),
			Entry("SSE-C, an id made up, no key, over TLS", "SSE-C-AES256", true, overTLS, nil, completeBadKey),
			Entry("SSE-C, its own id, another key", "SSE-C-AES256", false, overTLS, otherKeyHeaders, completeAsked),
			Entry("SSE-KMS, its own id, over plain HTTP", "SSE-KMS", false, "", nil, completeInsecure),
			Entry("SSE-KMS, an id made up, over TLS, with no key server", "SSE-KMS", true, overTLS, nil, completeBadKey),
		)

		It("echoes x-amz-server-side-encryption and the KMS key id on the retry of an SSE-KMS upload without a checksum, as on its completion, on Tentacle", func(ctx SpecContext) {
			w := newWriteWorld(ctx, denc.Tentacle)
			id, doc := withPart(ctx, w, "SSE-KMS", op.UploadChecksum{})
			Expect(w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+id, doc).Code).To(Equal(200))
			expectAnswer(w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+id, doc), completeKMS)
		})
	})

	It("answers a refused requester 403 before it decides the upload's encryption", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id, doc := withPart(ctx, w, "SSE-C-AES256", cryptSHA256Upload)
		expectError(w.send(w.bob, http.MethodPost, overTLS+"/plain/k?uploadId="+id, doc), 403, "AccessDenied")
	})
})
