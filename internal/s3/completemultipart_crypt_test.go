package s3_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"crypto/sha256"
	"encoding/base64"
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
// success, whether it echoes the key's SSE-C headers, and the
// x-amz-server-side-encryption and KMS key id it echoes.
type completeAnswer struct {
	status        int
	code          string
	echo          bool
	sse, kmsKeyID string
}

// The responses the encrypted completion specs expect, the checksum their
// uploads are created with, and SSE-C headers naming a key no upload has.
var (
	completeOK           = completeAnswer{status: 200}
	completeEchoed       = completeAnswer{status: 200, echo: true}
	completeKMS          = completeAnswer{status: 200, sse: "aws:kms", kmsKeyID: "kid"}
	completeSSES3        = completeAnswer{status: 200, sse: "AES256"}
	completeBadKey       = completeAnswer{status: 400, code: "InvalidArgument"}
	completeBadAlgorithm = completeAnswer{status: 400, code: "InvalidEncryptionAlgorithmError"}
	completeInsecure     = completeAnswer{status: 400, code: "InvalidRequest"}
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

	// oneHelloComposite is the SHA256 composite of one part of "hello", as a
	// completion's response renders it.
	oneHelloComposite := func() string {
		part := sha256.Sum256([]byte("hello"))
		sum := sha256.Sum256(part[:])
		return base64.StdEncoding.EncodeToString(sum[:]) + "-1"
	}

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

	// expectAnswer checks rec against want, hdr being the SSE-C headers the
	// request sent: a refusal by the decision echoes nothing; a success
	// echoes what want names under radosgw's spellings, and nothing else.
	expectAnswer := func(rec *httptest.ResponseRecorder, want completeAnswer, hdr []string) {
		GinkgoHelper()
		if want.status != 200 {
			expectError(rec, want.status, want.code)
			for _, name := range []string{algorithmHeader, keyMD5Header, sseHeader, kmsKeyIDHeader} {
				Expect(rec.Header()).NotTo(haveHeaderAnyCase(name))
			}
			return
		}
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		if want.echo {
			Expect(exactHeader(rec.Header(), algorithmHeader)).To(Equal("AES256"))
			Expect(exactHeader(rec.Header(), keyMD5Header)).To(Equal(hdr[5]), "the key MD5 as the request sent it")
		} else {
			Expect(rec.Header()).NotTo(haveHeaderAnyCase(algorithmHeader))
			Expect(rec.Header()).NotTo(haveHeaderAnyCase(keyMD5Header))
		}
		for name, v := range map[string]string{sseHeader: want.sse, kmsKeyIDHeader: want.kmsKeyID} {
			if v == "" {
				Expect(rec.Header()).NotTo(haveHeaderAnyCase(name))
			} else {
				Expect(exactHeader(rec.Header(), name)).To(Equal(v), name)
			}
		}
	}

	DescribeTable("answers an SSE-C upload's completion as Amazon S3 does, echoing the key's headers on Tentacle",
		func(ctx SpecContext, checksum bool, origin string, hdr []string, squid, tentacle completeAnswer) {
			uc := op.UploadChecksum{}
			if checksum {
				uc = cryptSHA256Upload
			}
			for rel, want := range map[denc.Release]completeAnswer{denc.Squid: squid, denc.Tentacle: tentacle} {
				w := newWriteWorld(ctx, rel)
				id, doc := withPart(ctx, w, "SSE-C-AES256", uc)
				rec := w.send(w.alice, http.MethodPost, origin+"/plain/k?uploadId="+id, doc, hdr...)
				expectAnswer(rec, want, hdr)
				if want.status != 200 {
					Expect(w.uploads(ctx)).To(HaveLen(1), "release %d: the upload is kept", rel)
				}
			}
		},
		Entry("with a checksum, its key named over TLS", true, overTLS, sseCRequest(encryptedUploadKey()), completeOK, completeEchoed),
		Entry("with a checksum, no SSE-C header: Tentacle asks for the key, Squid kept no checksum", true, overTLS, nil, completeOK, completeBadKey),
		Entry("with a checksum, another key named", true, overTLS, otherKeyHeaders, completeBadKey, completeBadKey),
		Entry("its key named over plain HTTP, under rgw_crypt_require_ssl", false, "", sseCRequest(encryptedUploadKey()), completeInsecure, completeInsecure),
		Entry("without a checksum, no SSE-C header, over plain HTTP", false, "", nil, completeOK, completeOK),
		Entry("without a checksum, its key named over TLS", false, overTLS, sseCRequest(encryptedUploadKey()), completeOK, completeEchoed),
		Entry("without a checksum, the key MD5 header alone, which names no algorithm", false, overTLS,
			[]string{"X-Amz-Server-Side-Encryption-Customer-Key-Md5", "AAAA"}, completeBadKey, completeBadKey),
		Entry("without a checksum, any header of the SSE-C prefix, as exists_prefix counts one", false, overTLS,
			[]string{"X-Amz-Server-Side-Encryption-Customer-Other", "x"}, completeBadKey, completeBadKey),
		Entry("without a checksum, x-amz-server-side-encryption, outside the SSE-C prefix", false, overTLS,
			[]string{"X-Amz-Server-Side-Encryption", "AES256"}, completeOK, completeOK),
		Entry("an algorithm header sent empty, which beast files, so it is no AES256", false, overTLS,
			append([]string{"X-Amz-Server-Side-Encryption-Customer-Algorithm", ""}, sseCRequest(encryptedUploadKey())[2:]...),
			completeBadAlgorithm, completeBadAlgorithm),
		Entry("an algorithm other than AES256", false, overTLS,
			append([]string{"X-Amz-Server-Side-Encryption-Customer-Algorithm", "AES128"}, sseCRequest(encryptedUploadKey())[2:]...),
			completeBadAlgorithm, completeBadAlgorithm),
	)

	It("answers a Tentacle SSE-C upload's combined checksum to the holder of its key, as for any upload", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id, doc := withPart(ctx, w, "SSE-C-AES256", cryptSHA256Upload)
		rec := w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+id, doc, sseCRequest(encryptedUploadKey())...)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<ChecksumSHA256>" + oneHelloComposite() + "</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType>"))
	})

	It("echoes the key MD5 as the request sent it, white space the decode skips included", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id, doc := withPart(ctx, w, "SSE-C-AES256", op.UploadChecksum{})
		hdr := sseCRequest(encryptedUploadKey())
		hdr[5] = hdr[5][:4] + " " + hdr[5][4:]
		expectAnswer(w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+id, doc, hdr...), completeEchoed, hdr)
	})

	// send_response dumps crypt_http_responses whatever op_ret is
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4604-L4612 at v20.2.4).
	It("sends the key's headers on an error after the decision passed, as radosgw does", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id, _ := withPart(ctx, w, "SSE-C-AES256", op.UploadChecksum{})
		hdr := sseCRequest(encryptedUploadKey())
		rec := w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+id, completion(md5Hex("other")), hdr...)
		expectError(rec, 400, "InvalidPart")
		Expect(exactHeader(rec.Header(), algorithmHeader)).To(Equal("AES256"))
		Expect(exactHeader(rec.Header(), keyMD5Header)).To(Equal(hdr[5]))
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
				expectAnswer(w.send(w.alice, http.MethodPost, origin+"/plain/k?uploadId="+id, doc), want, nil)
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
		expectAnswer(rec, completeAnswer{status: 200, sse: "aws:kms", kmsKeyID: "k\tid   \x80"}, nil)
	})

	It("completes an upload without a crypt mode whatever SSE-C headers the request sends, echoing none, on both releases", func(ctx SpecContext) {
		for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
			w := newWriteWorld(ctx, rel)
			id := w.initUpload(w.alice, "/plain/k")
			rec := w.sendBytes(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", []byte("hello"))
			Expect(rec.Code).To(Equal(200), "release %d: %s", rel, rec.Body.String())
			expectAnswer(w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("hello")), otherKeyHeaders...), completeOK, nil)
		}
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
				expectAnswer(rec, want, hdr)
				Expect(rec.Body.String()).NotTo(ContainSubstring("Checksum"))
			},
			Entry("SSE-C, its own id, no key, over plain HTTP", "SSE-C-AES256", false, "", nil, completeInsecure),
			Entry("SSE-C, an id made up, no key, over plain HTTP", "SSE-C-AES256", true, "", nil, completeInsecure),
			Entry("SSE-C, an id made up, no key, over TLS", "SSE-C-AES256", true, overTLS, nil, completeBadKey),
			Entry("SSE-C, its own id, another key", "SSE-C-AES256", false, overTLS, otherKeyHeaders, completeBadKey),
			Entry("SSE-KMS, its own id, over plain HTTP", "SSE-KMS", false, "", nil, completeInsecure),
			Entry("SSE-KMS, an id made up, over TLS, with no key server", "SSE-KMS", true, overTLS, nil, completeBadKey),
		)

		It("answers the key holder's retry with the combined checksum and no SSE-C pair, as no radosgw echoes one there, on Tentacle", func(ctx SpecContext) {
			w := newWriteWorld(ctx, denc.Tentacle)
			_, doc := completed(ctx, w, "SSE-C-AES256")
			rec := w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId=2~bogusbogusbogus", doc, sseCRequest(encryptedUploadKey())...)
			expectAnswer(rec, completeOK, nil)
			Expect(rec.Body.String()).To(ContainSubstring("<ChecksumSHA256>" + oneHelloComposite() + "</ChecksumSHA256>"))
		})

		It("echoes x-amz-server-side-encryption and the KMS key id on the retry of an SSE-KMS upload without a checksum, as on its completion, on Tentacle", func(ctx SpecContext) {
			w := newWriteWorld(ctx, denc.Tentacle)
			id, doc := withPart(ctx, w, "SSE-KMS", op.UploadChecksum{})
			Expect(w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+id, doc).Code).To(Equal(200))
			expectAnswer(w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploadId="+id, doc), completeKMS, nil)
		})
	})

	It("answers a refused requester 403 before it decides the upload's encryption", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id, doc := withPart(ctx, w, "SSE-C-AES256", cryptSHA256Upload)
		expectError(w.send(w.bob, http.MethodPost, overTLS+"/plain/k?uploadId="+id, doc), 403, "AccessDenied")
	})

	It("reads the last of a repeated SSE-C header, as radosgw's environment keeps it", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id, doc := withPart(ctx, w, "SSE-C-AES256", cryptSHA256Upload)
		sum := md5.Sum(encryptedUploadKey()) //nolint:gosec // SSE-C names its key by the key's MD5
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, overTLS+"/plain/k?uploadId="+id, strings.NewReader(doc))
		for i := 0; i+1 < len(otherKeyHeaders); i += 2 {
			req.Header.Add(otherKeyHeaders[i], otherKeyHeaders[i+1])
		}
		req.Header.Add("X-Amz-Server-Side-Encryption-Customer-Key", base64.StdEncoding.EncodeToString(encryptedUploadKey()))
		req.Header.Add("X-Amz-Server-Side-Encryption-Customer-Key-Md5", base64.StdEncoding.EncodeToString(sum[:]))
		rec := httptest.NewRecorder()
		w.handler(w.alice).ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(exactHeader(rec.Header(), keyMD5Header)).To(Equal(base64.StdEncoding.EncodeToString(sum[:])))
	})
})
