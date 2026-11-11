package op_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"encoding/base64"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// radosgwCryptAttrs are the crypt attrs rgw_s3_prepare_encrypt gives the
// meta object of an upload a radosgw began with mode
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1045-L1315 at v20.2.4),
// SSE-C's naming sseCKey; for a mode it does not write, the mode alone.
func radosgwCryptAttrs(mode string) map[string][]byte {
	keySel := bytes.Repeat([]byte{9}, 32)
	attrs := map[string][]byte{meta.AttrCryptMode: []byte(mode)}
	switch mode {
	case "SSE-C-AES256":
		attrs[meta.AttrCryptKeyMD5] = sseCKeyMD5()
	case "SSE-KMS", "AES256":
		attrs[meta.AttrCryptKeyID] = []byte("kid")
		attrs[meta.AttrCryptPrefix+"keysel"] = keySel
		attrs[meta.AttrCryptPrefix+"context"] = []byte(`{"aws:s3:arn":"arn:aws:s3:::plain/k"}`)
	case "RGW-AUTO":
		attrs[meta.AttrCryptPrefix+"keysel"] = keySel
	}
	return attrs
}

// encryptedUpload starts alice's upload of key in plain as a radosgw stores
// one it began with mode (radosgwCryptAttrs), and returns its id.
func (f *writeFixture) encryptedUpload(ctx context.Context, key, mode string) string {
	GinkgoHelper()
	attrs := radosgwCryptAttrs(mode)
	attrs[meta.AttrACL] = f.aliceACL
	up, err := f.store.CreateUpload(ctx, f.rec, meta.ObjKey{Name: key}, op.UploadParams{
		Owner: f.alice.Owner, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrs,
	})
	Expect(err).NotTo(HaveOccurred())
	return up.ID
}

var _ = Describe("UploadPart of an upload radosgw began encrypted", func() {
	key := func() string { return base64.StdEncoding.EncodeToString(sseCKey()) }
	keyMD5 := func() string { return base64.StdEncoding.EncodeToString(sseCKeyMD5()) }
	withKey := func(o *op.UploadPart) { o.SSECAlgorithm, o.SSECKey, o.SSECKeyMD5 = "AES256", key(), keyMD5() }
	otherKey := func(o *op.UploadPart) {
		k := bytes.Repeat([]byte{2}, 32)
		sum := md5.Sum(k) //nolint:gosec // SSE-C names its key by the key's MD5
		o.SSECAlgorithm = "AES256"
		o.SSECKey, o.SSECKeyMD5 = base64.StdEncoding.EncodeToString(k), base64.StdEncoding.EncodeToString(sum[:])
	}
	const (
		badAlgorithm = "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."
		mismatch     = "The calculated MD5 hash of the key did not match the hash that was provided."
	)

	// partFor is alice's part 1 of upload id, copied from plain's src, or
	// else with a body the spec fails if it is read.
	partFor := func(f *writeFixture, id string, copied bool) *op.UploadPart {
		if copied {
			return f.copyPartOf(id, 1, "src", "")
		}
		o := f.partOf(id, 1, "")
		o.Body, o.Size = failingReader{}, 1
		return o
	}

	DescribeTable("refuses the part on both releases, uploaded or copied, before a byte of it is read or stored",
		func(ctx SpecContext, mode string, secure bool, set func(*op.UploadPart), want error, msg string) {
			for _, rel := range bothReleases {
				for _, copied := range []bool{false, true} {
					f := newMPFixture(ctx, rel)
					f.put(ctx, "src", []byte("hello"))
					id := f.encryptedUpload(ctx, "k", mode)
					o := partFor(f, id, copied)
					o.Secure = secure
					if set != nil {
						set(o)
					}
					err := op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))
					Expect(err).To(MatchError(want), "release %d, copied %t", rel, copied)
					Expect(messageOf(err)).To(Equal(msg), "release %d, copied %t", rel, copied)
					Expect(f.parts(ctx, "k", id)).To(BeEmpty(), "release %d, copied %t", rel, copied)
				}
			}
		},
		Entry("SSE-C, the request naming no key, as radosgw refuses it", "SSE-C-AES256", true, nil, op.ErrInvalidArgument, badAlgorithm),
		Entry("SSE-C, the request naming another key than the upload's", "SSE-C-AES256", true, otherKey, op.ErrInvalidArgument, mismatch),
		Entry("SSE-C, the request naming the upload's key, with which radosgw encrypts and rgw-go cannot", "SSE-C-AES256", true, withKey, op.ErrNotImplemented, ""),
		Entry("SSE-C over plain HTTP, its key named, under rgw_crypt_require_ssl", "SSE-C-AES256", false, withKey, op.ErrInvalidRequest, ""),
		Entry("SSE-KMS, with no key server", "SSE-KMS", true, nil, op.ErrInvalidArgument, "Failed to retrieve the actual key, kms-keyid: kid"),
		Entry("SSE-KMS over plain HTTP, under rgw_crypt_require_ssl", "SSE-KMS", false, nil, op.ErrInvalidRequest, ""),
		Entry("SSE-S3, with no vault", "AES256", true, nil, op.ErrInvalidArgument, "Failed to retrieve the actual key"),
		Entry("RGW-AUTO over plain HTTP, with no default key: radosgw's -EIO, and no transport check", "RGW-AUTO", false, nil, op.ErrUnknown, ""),
		Entry("a GCM mode v20.2.5 adds, which rgw-go does not know, the request naming the key", "SSE-C-AES256-GCM", true, withKey, op.ErrNotImplemented, ""),
		Entry("a mode a NUL ends, which names no mode", "SSE-KMS\x00", true, nil, op.ErrNotImplemented, ""),
		Entry("a mode rgw-go does not know over plain HTTP, which no transport check answers first", "SSE-KMS-GCM", false, nil, op.ErrNotImplemented, ""),
	)

	It("refuses SSE-S3 over plain HTTP as each release's radosgw does: Squid checks the transport, Tentacle does not", func(ctx SpecContext) {
		for _, copied := range []bool{false, true} {
			for rel, want := range map[denc.Release]error{denc.Squid: op.ErrInvalidRequest, denc.Tentacle: op.ErrInvalidArgument} {
				f := newMPFixture(ctx, rel)
				f.put(ctx, "src", []byte("hello"))
				id := f.encryptedUpload(ctx, "k", "AES256")
				err := op.Run(ctx, partFor(f, id, copied), f.req(http.MethodPut, "plain", "k"))
				Expect(err).To(MatchError(want), "release %d, copied %t", rel, copied)
				Expect(f.parts(ctx, "k", id)).To(BeEmpty(), "release %d, copied %t", rel, copied)
			}
		}
	})

	It("stores the part of an upload without a crypt mode, on both releases, uploaded or copied", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			for _, copied := range []bool{false, true} {
				f := newMPFixture(ctx, rel)
				f.put(ctx, "src", []byte("hello"))
				id := f.initUpload(ctx, "k")
				o := f.partOf(id, 1, "hello")
				if copied {
					o = f.copyPartOf(id, 1, "src", "")
				}
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed(), "release %d, copied %t", rel, copied)
				Expect(o.ETag).To(Equal(md5Hex([]byte("hello"))), "release %d, copied %t", rel, copied)
				Expect(f.parts(ctx, "k", id)).To(HaveLen(1), "release %d, copied %t", rel, copied)
			}
		}
	})

	It("asks the store for nothing past the upload's read", func(ctx SpecContext) {
		for _, copied := range []bool{false, true} {
			f := newMPFixture(ctx, denc.Tentacle)
			f.put(ctx, "src", []byte("hello"))
			stub := &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(&op.Upload{ID: "2~x", Attrs: radosgwCryptAttrs("SSE-KMS")}, nil)
			f.env.Multipart = stub
			o := partFor(f, "2~x", copied)
			o.Secure = true
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument), "copied %t", copied)
			Expect(stub.GetUploadCallCount()).To(Equal(1), "copied %t", copied)
			Expect(stub.Invocations()).To(HaveLen(1), "only GetUpload, copied %t", copied)
		}
	})

	It("reads the upload first, so a missing one is NoSuchUpload", func(ctx SpecContext) {
		for _, copied := range []bool{false, true} {
			f := newMPFixture(ctx, denc.Tentacle)
			f.put(ctx, "src", []byte("hello"))
			Expect(op.Run(ctx, partFor(f, "2~nope", copied), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNoSuchUpload), "copied %t", copied)
		}
	})

	It("refuses before the part's checksum request is read, as get_encrypt_filter runs before the checksum Factory", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			id := f.encryptedUpload(ctx, "k", "RGW-AUTO")
			o := partFor(f, id, false)
			o.ChecksumField = checksumFields("x-amz-checksum-algorithm", "MD5")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrUnknown), "release %d: not the checksum's InvalidArgument", rel)
		}
	})

	Describe("copied", func() {
		var (
			f  *writeFixture
			id string
		)
		BeforeEach(func(ctx SpecContext) {
			f = newMPFixture(ctx, denc.Tentacle)
			f.put(ctx, "src", []byte("hello"))
			id = f.encryptedUpload(ctx, "k", "RGW-AUTO")
		})

		It("answers a missing source first, for an admin let through its refusal, as radosgw's unranged copy checks it before get_encrypt_filter", func(ctx SpecContext) {
			fake := &opfakes.FakeAuthorizer{}
			fake.VerifyObjectInReturns(op.ErrAccessDenied)
			f.env.Authz = fake
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity.Admin = true
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "missing", ""), r)).To(MatchError(op.ErrNoSuchKey))
		})
		It("refuses a source transitioned to a cloud tier first, as radosgw checks the source before get_encrypt_filter", func(ctx SpecContext) {
			objs := &opfakes.FakeObjectStore{}
			m := meta.NewManifest()
			m.TierType = meta.TierTypeCloudS3
			objs.PrefetchObjectReturns(&op.ObjectState{
				Exists: true, Key: meta.ObjKey{Name: "src"}, Size: 5, Manifest: &m, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL},
			}, nil)
			f.env.Objects = objs
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "src", ""), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidObjectState))
		})
		It("refuses the part ahead of a range past the source, which radosgw meets only in its copy loop", func(ctx SpecContext) {
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "src", "bytes=10-20"), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrUnknown))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("refuses the part ahead of a copy-source condition that fails", func(ctx SpecContext) {
			o := f.copyPartOf(id, 1, "src", "")
			o.IfMatch = new("other")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrUnknown))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("checks the request's own SSE-C headers for an SSE-C upload, as get_encrypt_filter passes copy_source false", func(ctx SpecContext) {
			sseC := f.encryptedUpload(ctx, "j", "SSE-C-AES256")
			r := f.req(http.MethodPut, "plain", "j")
			o := f.copyPartOf(sseC, 1, "src", "")
			o.Secure = true
			withKey(o)
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrNotImplemented))
			o = f.copyPartOf(sseC, 1, "src", "")
			o.Secure = true
			Expect(messageOf(op.Run(ctx, o, r))).To(Equal(badAlgorithm))
			Expect(f.parts(ctx, "j", sseC)).To(BeEmpty())
		})
	})

	It("keeps the upload's attrs out of the part's head, whose attrs are the request's", func(ctx SpecContext) {
		f := newMPFixture(ctx, denc.Squid)
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(&op.Upload{ID: "2~x", Attrs: map[string][]byte{meta.AttrACL: {1}, meta.AttrContentType: []byte("x\x00")}}, nil)
		stub.PutPartReturns(&op.PartResult{}, nil)
		f.env.Multipart = stub
		Expect(op.Run(ctx, f.partOf("2~x", 1, "hello"), f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		_, up, _, _, p := stub.PutPartArgsForCall(0)
		Expect(p.Attrs).To(Equal(map[string][]byte{meta.AttrACL: f.aliceACL}))
		Expect(up.Attrs).To(Equal(p.Attrs))
	})
})

var _ = Describe("UploadPartCopy of a source radosgw encrypted", func() {
	// keyFor names sseCKey with SSE-C headers: the copy-source ones when
	// copySource, else the request's own.
	keyFor := func(copySource bool) func(*op.UploadPart) {
		return func(o *op.UploadPart) {
			k, m := base64.StdEncoding.EncodeToString(sseCKey()), base64.StdEncoding.EncodeToString(sseCKeyMD5())
			if copySource {
				o.CopySourceSSECAlgorithm, o.CopySourceSSECKey, o.CopySourceSSECKeyMD5 = "AES256", k, m
				return
			}
			o.SSECAlgorithm, o.SSECKey, o.SSECKeyMD5 = "AES256", k, m
		}
	}
	const badAlgorithm = "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."

	// copyFrom copies plain's enc, which holds hello under attrs, into part 1
	// of a new upload of k on rel, over a secure transport unless set says
	// otherwise, and returns the parts the upload holds and the error.
	copyFrom := func(ctx context.Context, rel denc.Release, attrs map[string][]byte, rng string, set func(*op.UploadPart)) ([]op.Part, error) {
		GinkgoHelper()
		f := newMPFixture(ctx, rel)
		f.putAttrs(ctx, "enc", "hello", attrs)
		id := f.initUpload(ctx, "k")
		o := f.copyPartOf(id, 1, "enc", rng)
		o.Secure = true
		if set != nil {
			set(o)
		}
		err := op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))
		return f.parts(ctx, "k", id), err
	}

	DescribeTable("decides the source on both releases as radosgw's get_data does, copying nothing when it refuses",
		func(ctx SpecContext, attrs map[string][]byte, set func(*op.UploadPart), want error, msg string) {
			for _, rel := range bothReleases {
				parts, err := copyFrom(ctx, rel, attrs, "", set)
				Expect(err).To(MatchError(want), "release %d", rel)
				Expect(messageOf(err)).To(Equal(msg), "release %d", rel)
				Expect(parts).To(BeEmpty(), "release %d", rel)
			}
		},
		Entry("SSE-C, the request naming no key", radosgwCryptAttrs("SSE-C-AES256"), nil, op.ErrInvalidArgument, badAlgorithm),
		Entry("SSE-KMS, with no key server", radosgwCryptAttrs("SSE-KMS"), nil, op.ErrInvalidArgument, "Failed to retrieve the actual key, kms-keyid: kid"),
		Entry("SSE-KMS over plain HTTP, under rgw_crypt_require_ssl", radosgwCryptAttrs("SSE-KMS"), func(o *op.UploadPart) { o.Secure = false }, op.ErrInvalidRequest, ""),
		Entry("SSE-S3, with no vault", radosgwCryptAttrs("AES256"), nil, op.ErrInvalidArgument, "Failed to retrieve the actual key"),
		Entry("RGW-AUTO, with no default key: radosgw's -EIO", radosgwCryptAttrs("RGW-AUTO"), nil, op.ErrUnknown, ""),
		Entry("a GCM mode v20.2.5 adds, whose stored bytes an older radosgw copies", radosgwCryptAttrs("AES256-GCM"), nil, op.ErrNotImplemented, ""),
	)

	DescribeTable("takes an SSE-C source's key from the copy-source headers on Tentacle and the request's own on Squid, and answers it 501",
		func(ctx SpecContext, rel denc.Release, copySource bool, want error) {
			parts, err := copyFrom(ctx, rel, radosgwCryptAttrs("SSE-C-AES256"), "", keyFor(copySource))
			Expect(err).To(MatchError(want))
			Expect(parts).To(BeEmpty())
		},
		Entry("Squid, the request's own headers naming it", denc.Squid, false, op.ErrNotImplemented),
		Entry("Squid, which reads no copy-source header", denc.Squid, true, op.ErrInvalidArgument),
		Entry("Tentacle, the copy-source headers naming it", denc.Tentacle, true, op.ErrNotImplemented),
		Entry("Tentacle, which reads not the request's own", denc.Tentacle, false, op.ErrInvalidArgument),
	)

	It("copies a source whose mode is empty, as radosgw takes it for none, where CopyObject's store refuses one", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			attrs := map[string][]byte{meta.AttrCryptMode: {}}
			parts, err := copyFrom(ctx, rel, attrs, "", nil)
			Expect(err).NotTo(HaveOccurred(), "release %d", rel)
			Expect(parts).To(HaveLen(1), "release %d", rel)
			Expect(op.RefuseEncryptedSource(&op.ObjectState{Attrs: attrs})).To(MatchError(op.ErrNotImplemented), "copy_obj's rule, on presence")
		}
	})

	It("copies a source without a crypt mode", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			parts, err := copyFrom(ctx, rel, map[string][]byte{meta.AttrCryptKeyID: []byte("kid")}, "", nil)
			Expect(err).NotTo(HaveOccurred(), "release %d", rel)
			Expect(parts).To(HaveLen(1), "release %d", rel)
		}
	})

	Describe("an empty source", func() {
		It("copies nothing without a range, which radosgw's copy loop never reads, so it decides nothing", func(ctx SpecContext) {
			for _, rel := range bothReleases {
				f := newMPFixture(ctx, rel)
				f.putAttrs(ctx, "enc", "", radosgwCryptAttrs("SSE-KMS"))
				id := f.initUpload(ctx, "k")
				o := f.copyPartOf(id, 1, "enc", "")
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed(), "release %d", rel)
				Expect(o.ETag).To(Equal(md5Hex(nil)), "release %d", rel)
			}
		})
		It("decides the source for a copy with a range, whose get_data radosgw calls once", func(ctx SpecContext) {
			for _, rel := range bothReleases {
				f := newMPFixture(ctx, rel)
				f.putAttrs(ctx, "enc", "", radosgwCryptAttrs("SSE-KMS"))
				id := f.initUpload(ctx, "k")
				o := f.copyPartOf(id, 1, "enc", "bytes=0-0")
				o.Secure = true
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument), "release %d", rel)
			}
		})
	})

	Describe("the decision's place", func() {
		var f *writeFixture
		BeforeEach(func(ctx SpecContext) {
			f = newMPFixture(ctx, denc.Tentacle)
			f.putAttrs(ctx, "enc", "hello", radosgwCryptAttrs("RGW-AUTO"))
		})

		It("comes after the upload's own refusal, as radosgw runs get_encrypt_filter before it reads the source", func(ctx SpecContext) {
			id := f.encryptedUpload(ctx, "k", "SSE-KMS")
			o := f.copyPartOf(id, 1, "enc", "")
			o.Secure = true
			err := op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(messageOf(err)).To(Equal("Failed to retrieve the actual key, kms-keyid: kid"), "the upload's mode, not the source's")
		})
		DescribeTable("comes ahead of the range and the copy-source conditions, as radosgw decrypts before range_to_ofs",
			func(ctx SpecContext, rng string, set func(*op.UploadPart)) {
				stub := &opfakes.FakeMultipartStore{}
				stub.GetUploadReturns(&op.Upload{ID: "2~x"}, nil)
				f.env.Multipart = stub
				o := f.copyPartOf("2~x", 1, "enc", rng)
				if set != nil {
					set(o)
				}
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrUnknown))
				Expect(stub.CopyPartCallCount()).To(BeZero())
			},
			Entry("a range past the source", "bytes=10-20", nil),
			Entry("an If-Match the source fails", "", func(o *op.UploadPart) { o.IfMatch = new("other") }),
		)
	})
})
