package op_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"encoding/base64"
	"maps"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// cryptOutcome is a completion's expected answer in the encrypted completion
// specs: err refuses it, msg being its message; otherwise it completes, echo
// being what it echoes.
type cryptOutcome struct {
	err  error
	msg  string
	echo op.CryptEcho
}

// The outcomes the encrypted completion specs expect, and the checksum their
// uploads are created with.
var (
	sseCCompletes    = cryptOutcome{}
	sseCEchoes       = cryptOutcome{echo: op.CryptEcho{SSECKeyMD5: base64.StdEncoding.EncodeToString(sseCKeyMD5())}}
	sseCInsecure     = cryptOutcome{err: op.ErrInvalidRequest}
	sseCNoKey        = cryptOutcome{err: op.ErrInvalidArgument, msg: "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."}
	sseCWrongKey     = cryptOutcome{err: op.ErrInvalidArgument, msg: "The calculated MD5 hash of the key did not match the hash that was provided."}
	sseCBadAlgorithm = cryptOutcome{err: op.ErrInvalidEncryptionAlgorithm, msg: "The requested encryption algorithm is not valid, must be AES256."}
	sseCBadKey       = cryptOutcome{err: op.ErrInvalidArgument, msg: "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."}
	kmsNoKeyServer   = cryptOutcome{err: op.ErrInvalidArgument, msg: "Failed to retrieve the actual key, kms-keyid: kid"}
	kmsCompletes     = cryptOutcome{echo: op.CryptEcho{SSE: "aws:kms", KMSKeyID: "kid"}}
	sseS3Completes   = cryptOutcome{echo: op.CryptEcho{SSE: "AES256"}}
	sseCSHA256Upload = op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}
)

var _ = Describe("CompleteMultipart of an upload radosgw began encrypted", func() {
	// withPart stores part 1, "x", of key's upload id in plain in the clear,
	// with uc's checksum when it has a type, and returns the completion
	// document naming it. Which bytes radosgw stored makes no difference to
	// a completion, which reads no part's data.
	withPart := func(ctx context.Context, f *writeFixture, key, id string, uc op.UploadChecksum) string {
		GinkgoHelper()
		up, err := f.upload(ctx, key, id)
		Expect(err).NotTo(HaveOccurred())
		p := op.PutParams{Size: 1}
		if uc.Type != cksum.None {
			p.Checksum = &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}
		}
		_, err = f.store.PutPart(ctx, up, 1, strings.NewReader("x"), p)
		Expect(err).NotTo(HaveOccurred())
		return completeDoc(xmlPart("1", md5Hex([]byte("x"))))
	}

	DescribeTable("refuses an upload of a crypt mode rgw-go does not know with NotImplemented on both releases, completing nothing",
		func(ctx SpecContext, mode string) {
			for _, rel := range bothReleases {
				f := newMPFixture(ctx, rel)
				id := f.encryptedUpload(ctx, "k", mode)
				doc := withPart(ctx, f, "k", id, op.UploadChecksum{})
				err := op.Run(ctx, &op.CompleteMultipart{UploadID: id}, f.bodyReq(http.MethodPost, "k", doc))
				Expect(err).To(MatchError(op.ErrNotImplemented), "release %d", rel)
				_, err = f.upload(ctx, "k", id)
				Expect(err).NotTo(HaveOccurred(), "release %d: the upload is kept", rel)
				st, err := f.store.StatObject(ctx, f.rec, meta.ObjKey{Name: "k"})
				Expect(err).NotTo(HaveOccurred(), "release %d", rel)
				Expect(st.Exists).To(BeFalse(), "release %d: no object is written", rel)
			}
		},
		Entry("a GCM mode v20.2.5 writes", "SSE-KMS-GCM"),
		Entry("a mode a NUL ends, which names no mode", "AES256\x00"),
	)

	It("asks the store to complete nothing, refusing before any write", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			stub := &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(&op.Upload{ID: "2~x", Attrs: radosgwCryptAttrs("RGW-AUTO-GCM")}, nil)
			f.env.Multipart = stub
			err := op.Run(ctx, &op.CompleteMultipart{UploadID: "2~x"}, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", md5Hex([]byte("x"))))))
			Expect(err).To(MatchError(op.ErrNotImplemented), "release %d", rel)
			Expect(stub.GetUploadCallCount()).To(Equal(1), "release %d", rel)
			Expect(stub.CompleteCallCount()).To(BeZero(), "release %d", rel)
		}
	})

	DescribeTable("completes an upload without a crypt mode on both releases, as before",
		func(ctx SpecContext, attrs map[string][]byte) {
			for _, rel := range bothReleases {
				f := newMPFixture(ctx, rel)
				all := map[string][]byte{meta.AttrACL: f.aliceACL}
				maps.Copy(all, attrs)
				up, err := f.store.CreateUpload(ctx, f.rec, meta.ObjKey{Name: "k"}, op.UploadParams{
					Owner: f.alice.Owner, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: all,
				})
				Expect(err).NotTo(HaveOccurred())
				doc := withPart(ctx, f, "k", up.ID, op.UploadChecksum{})
				Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: up.ID}, f.bodyReq(http.MethodPost, "k", doc))).To(Succeed(), "release %d", rel)
			}
		},
		Entry("no crypt attr", map[string][]byte(nil)),
		Entry("an empty mode, which radosgw takes for none", map[string][]byte{meta.AttrCryptMode: {}}),
	)

	// EmptyEncryption is the protocol's refusal of an encryption header sent
	// with no value. It stands behind the decision, which an SSE-C upload's
	// headers answer first, and applies to an upload that is found: one that
	// is not is the store's to answer.
	DescribeTable("refuses an encryption header sent empty once the decision has passed, completing nothing and echoing nothing",
		func(ctx SpecContext, mode string, release denc.Release) {
			f := newMPFixture(ctx, release)
			var id string
			if mode == "none" {
				id = f.initUpload(ctx, "k")
			} else {
				id = f.encryptedUpload(ctx, "k", mode)
			}
			doc := withPart(ctx, f, "k", id, op.UploadChecksum{})
			o := &op.CompleteMultipart{UploadID: id, Secure: true, EmptyEncryption: op.ErrInvalidArgument.WithMessage("an encryption header with no value")}
			err := op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", doc))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(messageOf(err)).To(Equal("an encryption header with no value"))
			Expect(o.Echo).To(Equal(op.CryptEcho{}), "the refusal echoes nothing")
			_, err = f.upload(ctx, "k", id)
			Expect(err).NotTo(HaveOccurred(), "the upload is kept")
			st, err := f.store.StatObject(ctx, f.rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeFalse(), "no object is written")
		},
		Entry("an upload with no mode attr, Squid", "none", denc.Squid),
		Entry("an upload with no mode attr, Tentacle", "none", denc.Tentacle),
		Entry("an upload with an empty mode attr, which radosgw takes for none", "", denc.Tentacle),
		Entry("SSE-S3 on Tentacle, whose decision would echo AES256", "AES256", denc.Tentacle),
		Entry("SSE-KMS on Tentacle, whose decision would echo aws:kms and its key id", "SSE-KMS", denc.Tentacle),
		Entry("SSE-KMS on Squid, whose decision passes", "SSE-KMS", denc.Squid),
	)

	It("leaves the SSE-C decision first, so an algorithm sent empty is InvalidEncryptionAlgorithmError", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			id := f.encryptedUpload(ctx, "k", "SSE-C-AES256")
			doc := withPart(ctx, f, "k", id, op.UploadChecksum{})
			o := &op.CompleteMultipart{
				UploadID: id, Secure: true, SSECAlgorithm: new(""), SSECSent: true,
				EmptyEncryption: op.ErrInvalidArgument.WithMessage("an encryption header with no value"),
			}
			Expect(op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", doc))).To(MatchError(op.ErrInvalidEncryptionAlgorithm), "release %d", rel)
		}
	})

	It("leaves an upload that is not found to the store, whatever EmptyEncryption says", func(ctx SpecContext) {
		f := newMPFixture(ctx, bothReleases[1])
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(nil, op.ErrNoSuchUpload)
		stub.CompleteReturns(&op.PutResult{ETag: "e"}, nil)
		f.env.Multipart = stub
		o := &op.CompleteMultipart{UploadID: "2~x", EmptyEncryption: op.ErrInvalidArgument.WithMessage("an encryption header with no value")}
		Expect(op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "e"))))).To(Succeed())
		Expect(stub.CompleteCallCount()).To(Equal(1))
	})

	It("leaves a completion retried after it succeeded to the store, which finds no upload", func(ctx SpecContext) {
		f := newMPFixture(ctx, bothReleases[1])
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(nil, op.ErrNoSuchUpload)
		stub.CompleteReturns(&op.PutResult{ETag: "e"}, nil)
		f.env.Multipart = stub
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: "2~x"}, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "e"))))).To(Succeed())
		Expect(stub.CompleteCallCount()).To(Equal(1))
	})

	// The SSE-C headers of a completion that names sseCKey, another key, or
	// sends a header of the SSE-C prefix and nothing else, or that names
	// sseCKey under another algorithm or with a key that does not decode.
	withKey := func(o *op.CompleteMultipart) {
		o.SSECAlgorithm, o.SSECSent = new("AES256"), true
		o.SSECKey, o.SSECKeyMD5 = base64.StdEncoding.EncodeToString(sseCKey()), base64.StdEncoding.EncodeToString(sseCKeyMD5())
	}
	otherKey := func(o *op.CompleteMultipart) {
		k := bytes.Repeat([]byte{2}, 32)
		sum := md5.Sum(k) //nolint:gosec // SSE-C names its key by the key's MD5
		o.SSECAlgorithm, o.SSECSent = new("AES256"), true
		o.SSECKey, o.SSECKeyMD5 = base64.StdEncoding.EncodeToString(k), base64.StdEncoding.EncodeToString(sum[:])
	}
	strayHeader := func(o *op.CompleteMultipart) { o.SSECSent = true }
	algorithm := func(alg string) func(*op.CompleteMultipart) {
		return func(o *op.CompleteMultipart) {
			withKey(o)
			o.SSECAlgorithm = new(alg)
		}
	}
	undecodableKey := func(o *op.CompleteMultipart) {
		withKey(o)
		o.SSECKey = "not base64!"
	}

	// complete runs alice's completion of a one-part upload of k in plain
	// that radosgw began with mode and uc, over a secure transport or not,
	// with set's headers, and checks want: a refusal keeps the upload,
	// writes no object and echoes nothing; a completion writes the object
	// with the upload's crypt attrs, as radosgw's completion gives its head
	// the meta object's, echoes want's, and answers the combined checksum of
	// an upload whose type the store kept.
	complete := func(ctx context.Context, f *writeFixture, mode string, uc op.UploadChecksum, secure bool, set func(*op.CompleteMultipart), want cryptOutcome) {
		GinkgoHelper()
		rel := f.env.Zone.Release()
		id := f.encryptedUploadWith(ctx, "k", mode, uc)
		doc := withPart(ctx, f, "k", id, uc)
		o := &op.CompleteMultipart{UploadID: id, Secure: secure}
		if set != nil {
			set(o)
		}
		err := op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", doc))
		st, serr := f.store.StatObject(ctx, f.rec, meta.ObjKey{Name: "k"})
		Expect(serr).NotTo(HaveOccurred(), "release %d", rel)
		_, uerr := f.upload(ctx, "k", id)
		if want.err != nil {
			Expect(err).To(MatchError(want.err), "release %d", rel)
			Expect(messageOf(err)).To(Equal(want.msg), "release %d", rel)
			Expect(o.Echo).To(BeZero(), "release %d", rel)
			Expect(uerr).NotTo(HaveOccurred(), "release %d: the upload is kept", rel)
			Expect(st.Exists).To(BeFalse(), "release %d: no object is written", rel)
			return
		}
		Expect(err).NotTo(HaveOccurred(), "release %d", rel)
		Expect(o.Echo).To(Equal(want.echo), "release %d", rel)
		Expect(uerr).To(MatchError(op.ErrNoSuchUpload), "release %d: the upload is completed", rel)
		Expect(st.Exists).To(BeTrue(), "release %d", rel)
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrCryptMode, []byte(mode)), "release %d", rel)
		if rel >= denc.Tentacle && uc.Type != cksum.None {
			Expect(o.Checksum).NotTo(BeNil(), "release %d: the combined checksum, as for any upload", rel)
		} else {
			Expect(o.Checksum).To(BeNil(), "release %d", rel)
		}
	}

	// v20.2.5's verify_encryption, Amazon S3's rule
	// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/rgw_rest_s3.cc#L4622-L4635 at v20.2.5),
	// on both releases, with rgw_s3_prepare_decrypt's checks
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1333-L1421 at v20.2.4).
	// Squid keeps no upload's checksum type, so on Squid an upload created
	// with a checksum is one without.
	DescribeTable("decides an SSE-C upload as Amazon S3 does, asking for its key for an upload with a checksum or a request that sends SSE-C headers",
		func(ctx SpecContext, checksum, secure bool, set func(*op.CompleteMultipart), squid, tentacle cryptOutcome) {
			uc := op.UploadChecksum{}
			if checksum {
				uc = sseCSHA256Upload
			}
			for rel, want := range map[denc.Release]cryptOutcome{denc.Squid: squid, denc.Tentacle: tentacle} {
				complete(ctx, newMPFixture(ctx, rel), "SSE-C-AES256", uc, secure, set, want)
			}
		},
		Entry("with a checksum, its key named over TLS: completed, Tentacle echoing the key's headers", true, true, withKey, sseCCompletes, sseCEchoes),
		Entry("with a checksum, another key named", true, true, otherKey, sseCWrongKey, sseCWrongKey),
		Entry("with a checksum, no SSE-C header: Tentacle asks for the key", true, true, nil, sseCCompletes, sseCNoKey),
		Entry("with a checksum over plain HTTP, no SSE-C header: rgw_crypt_require_ssl refuses first on Tentacle", true, false, nil, sseCCompletes, sseCInsecure),
		Entry("with a checksum, its key named over plain HTTP, under rgw_crypt_require_ssl", true, false, withKey, sseCInsecure, sseCInsecure),
		Entry("without a checksum, no SSE-C header, over plain HTTP: no check at all", false, false, nil, sseCCompletes, sseCCompletes),
		Entry("without a checksum, its key named over TLS: checked, Tentacle echoing", false, true, withKey, sseCCompletes, sseCEchoes),
		Entry("without a checksum, another key named: the headers sent are checked", false, true, otherKey, sseCWrongKey, sseCWrongKey),
		Entry("without a checksum, its key named over plain HTTP", false, false, withKey, sseCInsecure, sseCInsecure),
		Entry("without a checksum, a header of the SSE-C prefix alone, whose absent algorithm is missing", false, true, strayHeader, sseCNoKey, sseCNoKey),
		Entry("an algorithm header sent empty, which beast files, so it is no AES256", false, true, algorithm(""), sseCBadAlgorithm, sseCBadAlgorithm),
		Entry("an algorithm other than AES256", false, true, algorithm("AES128"), sseCBadAlgorithm, sseCBadAlgorithm),
		Entry("a key that does not decode from base64", true, true, undecodableKey, sseCBadKey, sseCBadKey),
	)

	It("takes an SSE-C key over plain HTTP when rgw_crypt_require_ssl is false, on both releases", func(ctx SpecContext) {
		for rel, want := range map[denc.Release]cryptOutcome{denc.Squid: sseCCompletes, denc.Tentacle: sseCEchoes} {
			f := newMPFixture(ctx, rel)
			f.conf["rgw_crypt_require_ssl"] = "false"
			complete(ctx, f, "SSE-C-AES256", sseCSHA256Upload, false, withKey, want)
		}
	})

	// A completion needs no key to write the head, so these modes complete
	// where Amazon S3 needs none: SSE-S3, and SSE-KMS without a checksum, a
	// Tentacle completion echoing what a radosgw with the key echoes
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1450-L1453 and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1518-L1520 at v20.2.4).
	// SSE-KMS with a checksum and RGW-AUTO are refused as a Tentacle radosgw
	// without a key server refuses them
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1423-L1493 at v20.2.4).
	// A Squid radosgw's completion reads no crypt attr
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4094-L4112 at v19.2.7).
	DescribeTable("completes an SSE-KMS, SSE-S3 or RGW-AUTO upload where Amazon S3 needs no key, on Tentacle, and every one on Squid",
		func(ctx SpecContext, mode string, checksum, secure bool, set func(*op.CompleteMultipart), tentacle cryptOutcome) {
			uc := op.UploadChecksum{}
			if checksum {
				uc = sseCSHA256Upload
			}
			complete(ctx, newMPFixture(ctx, denc.Squid), mode, uc, secure, set, sseCCompletes)
			complete(ctx, newMPFixture(ctx, denc.Tentacle), mode, uc, secure, set, tentacle)
		},
		Entry("SSE-KMS with a checksum over TLS, whose kms:Decrypt rgw-go cannot evaluate", "SSE-KMS", true, true, nil, kmsNoKeyServer),
		Entry("SSE-KMS with a checksum over plain HTTP, under rgw_crypt_require_ssl", "SSE-KMS", true, false, nil, sseCInsecure),
		Entry("SSE-KMS with a checksum, the request naming an SSE-C key, which the mode does not read", "SSE-KMS", true, true, withKey, kmsNoKeyServer),
		Entry("SSE-KMS without a checksum over TLS, echoing aws:kms and its key id", "SSE-KMS", false, true, nil, kmsCompletes),
		Entry("SSE-KMS without a checksum over plain HTTP, which rgw_crypt_require_ssl refuses ahead of any key", "SSE-KMS", false, false, nil, sseCInsecure),
		Entry("SSE-S3 with a checksum over plain HTTP, whose transport v20.2.4 does not check, echoing AES256", "AES256", true, false, nil, sseS3Completes),
		Entry("SSE-S3 without a checksum", "AES256", false, true, nil, sseS3Completes),
		Entry("RGW-AUTO with a checksum: -EIO for the default key that is not set", "RGW-AUTO", true, true, nil, cryptOutcome{err: op.ErrUnknown}),
		Entry("RGW-AUTO without a checksum", "RGW-AUTO", false, false, nil, cryptOutcome{err: op.ErrUnknown}),
	)

	It("completes an upload without a crypt mode whatever SSE-C headers the request sends, echoing none, on both releases", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			id := f.initUpload(ctx, "k")
			o := &op.CompleteMultipart{UploadID: id}
			otherKey(o)
			Expect(op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", withPart(ctx, f, "k", id, op.UploadChecksum{})))).To(Succeed(), "release %d", rel)
			Expect(o.Echo).To(BeZero(), "release %d", rel)
		}
	})

	It("decides once the document parses, as v20.2.5 does, where v20.2.4 decides first, on both releases", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			id := f.encryptedUploadWith(ctx, "k", "SSE-C-AES256", sseCSHA256Upload)
			o := &op.CompleteMultipart{UploadID: id, Secure: true}
			otherKey(o)
			err := op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", "<CompleteMultipartUpload>"))
			Expect(err).To(MatchError(op.ErrMalformedXML), "release %d", rel)
		}
	})

	It("refuses before the store completes anything, on both releases", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			stub := &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(&op.Upload{ID: "2~x", Attrs: radosgwCryptAttrs("SSE-C-AES256")}, nil)
			f.env.Multipart = stub
			o := &op.CompleteMultipart{UploadID: "2~x", Secure: true}
			otherKey(o)
			err := op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "e"))))
			Expect(err).To(MatchError(op.ErrInvalidArgument), "release %d", rel)
			Expect(stub.CompleteCallCount()).To(BeZero(), "release %d", rel)
		}
	})

	It("refuses an SSE-C upload before it reads the checksum the request supplies", func(ctx SpecContext) {
		f := newMPFixture(ctx, denc.Tentacle)
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(&op.Upload{ID: "2~x", Attrs: radosgwCryptAttrs("SSE-C-AES256"), Checksum: op.UploadChecksum{Type: cksum.XXH3}}, nil)
		f.env.Multipart = stub
		err := op.Run(ctx, &op.CompleteMultipart{UploadID: "2~x", Secure: true}, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "e"))))
		Expect(err).To(MatchError(op.ErrInvalidArgument), "the key is asked for ahead of computableUpload's NotImplemented")
		Expect(messageOf(err)).To(Equal(sseCNoKey.msg))
	})

	It("leaves a completion the store answers from an object without a crypt mode as it is, whatever key it names", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			stub := &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(nil, op.ErrNoSuchUpload)
			stub.CompleteReturns(&op.PutResult{ETag: "e"}, nil)
			f.env.Multipart = stub
			o := &op.CompleteMultipart{UploadID: "2~x"}
			otherKey(o)
			Expect(op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "e"))))).To(Succeed(), "release %d", rel)
			Expect(stub.CompleteCallCount()).To(Equal(1), "release %d", rel)
			Expect(o.Echo).To(BeZero(), "release %d", rel)
		}
	})

	// An upload id that names no upload, a retry of a completion that
	// succeeded or one made up, meets the store's answer from the object the
	// key holds (check_previously_completed,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7433-L7504 at v20.2.4),
	// which the op decides over that head's crypt attrs and combined
	// checksum as it decides an upload.
	DescribeTable("decides the store's answer from an object already completed as it decides an upload",
		func(ctx SpecContext, rel denc.Release, mode string, checksum, secure bool, set func(*op.CompleteMultipart), want cryptOutcome) {
			f := newMPFixture(ctx, rel)
			stub := &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(nil, op.ErrNoSuchUpload)
			res := &op.PutResult{ETag: "e", Crypt: radosgwCryptAttrs(mode)}
			if checksum {
				res.Checksum = &cksum.Cksum{Type: cksum.SHA256, Digest: bytes.Repeat([]byte{1}, 32), Flags: cksum.FlagComposite}
			}
			stub.CompleteReturns(res, nil)
			f.env.Multipart = stub
			o := &op.CompleteMultipart{UploadID: "2~bogusbogusbogus", Secure: secure}
			if set != nil {
				set(o)
			}
			err := op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "e"))))
			if want.err != nil {
				Expect(err).To(MatchError(want.err))
				Expect(messageOf(err)).To(Equal(want.msg))
				Expect(o.Checksum).To(BeNil(), "the head's checksum is not answered")
				Expect(o.Echo).To(BeZero())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Checksum).To(Equal(res.Checksum))
			Expect(o.Echo).To(Equal(want.echo))
		},
		Entry("SSE-C with a checksum, no key, over TLS", denc.Tentacle, "SSE-C-AES256", true, true, nil, sseCNoKey),
		Entry("SSE-C with a checksum, its key named over plain HTTP", denc.Tentacle, "SSE-C-AES256", true, false, withKey, sseCInsecure),
		Entry("SSE-C with a checksum, its key named over TLS, echoing no SSE-C pair, as no radosgw echoes one there", denc.Tentacle, "SSE-C-AES256", true, true, withKey, sseCCompletes),
		Entry("SSE-C without a checksum, no SSE-C header", denc.Tentacle, "SSE-C-AES256", false, false, nil, sseCCompletes),
		Entry("SSE-C on Squid, another key named", denc.Squid, "SSE-C-AES256", false, true, otherKey, sseCWrongKey),
		Entry("SSE-KMS with a checksum", denc.Tentacle, "SSE-KMS", true, true, nil, kmsNoKeyServer),
		Entry("SSE-KMS without a checksum", denc.Tentacle, "SSE-KMS", false, true, nil, kmsCompletes),
		Entry("SSE-KMS with a checksum on Squid, whose radosgw reads no crypt attr", denc.Squid, "SSE-KMS", true, false, nil, sseCCompletes),
		Entry("SSE-S3 with a checksum", denc.Tentacle, "AES256", true, false, nil, sseS3Completes),
		Entry("RGW-AUTO", denc.Tentacle, "RGW-AUTO", true, true, nil, cryptOutcome{err: op.ErrUnknown}),
		Entry("a mode rgw-go does not know", denc.Squid, "SSE-KMS-GCM", true, true, nil, cryptOutcome{err: op.ErrNotImplemented}),
	)
})
