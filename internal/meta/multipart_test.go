package meta_test

import (
	"encoding/hex"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("multipart names", func() {
	It("builds radosgw's meta, part and omap key names", func() {
		Expect(meta.MultipartMetaName("multipart.bin", "2~AkTb")).To(Equal("multipart.bin.2~AkTb.meta"))
		Expect(meta.MultipartMetaName("", "2~AkTb")).To(BeEmpty(), "RGWMPObj::init clears an object without a key")
		Expect(meta.MultipartPrefix("multipart.bin", "2~AkTb")).To(Equal("multipart.bin.2~AkTb"))
		Expect(meta.MultipartPartName("multipart.bin.2~AkTb", 3)).To(Equal("multipart.bin.2~AkTb.3"))
		Expect(meta.MultipartPartKey(3)).To(Equal("part.00000003"))
		Expect(meta.MultipartPartKey(123456789)).To(Equal("part.123456789"), "%08d pads to eight digits and no further")
	})
	DescribeTable("ParseMultipartMeta is RGWMPObj::from_meta",
		func(name, key, uploadID string, ok bool) {
			k, id, got := meta.ParseMultipartMeta(name)
			Expect([]any{k, id, got}).To(Equal([]any{key, uploadID, ok}))
		},
		Entry("a key with dots splits at the last two", "a.b.c.2~AkTb.meta", "a.b.c", "2~AkTb", true),
		Entry("an empty upload id", "key..meta", "key", "", true),
		Entry("no dot", "nodots", "", "", false),
		Entry("one dot after the first character", "k.meta", "", "", false),
		Entry("an empty key, which init clears", ".2~AkTb.meta", "", "", true),
		Entry("one leading dot, where rfind restarts from the end", ".meta", "", "", true),
	)
	DescribeTable("IsMultipartMeta is MultipartMetaFilter",
		func(name string, want bool) { Expect(meta.IsMultipartMeta(name)).To(Equal(want)) },
		Entry("a meta object", "k.2~id.meta", true),
		Entry("a part head", "k.2~id.3", false),
		Entry("the bare suffix", ".meta", false),
		// rfind('.') starts before the suffix's own dot, and "kmeta" has none.
		Entry("no dot before the suffix", "kmeta.meta", false),
		Entry("suffix only after a key", "k.meta", false),
		Entry("an empty key", ".x.meta", true),
	)
	It("recognizes both upload id prefixes", func() {
		Expect(meta.IsV2UploadID("2~x")).To(BeTrue())
		Expect(meta.IsV2UploadID("2/x")).To(BeTrue())
		Expect(meta.IsV2UploadID("x")).To(BeFalse())
		Expect(meta.IsV2UploadID("2")).To(BeFalse())
	})
})

var (
	partBucket = meta.BucketID{Name: "plain", Marker: "m1", ID: "m1"}
	partTarget = meta.Obj{Bucket: partBucket, Key: meta.ObjKey{Name: "multipart.bin"}}
	partRule   = meta.PlacementRule{Name: "default-placement"}
)

func newPart() meta.UploadPartInfo {
	return meta.UploadPartInfo{
		Num: 2, Size: 8 << 20, ETag: "0123456789abcdef0123456789abcdef", Modified: time.Unix(1700000000, 0).UTC(),
		Manifest:    meta.NewPartManifest(partTarget, partRule, partRule, "k.2~id", 2, 4<<20),
		Compression: meta.NewCompressionInfo(), AccountedSize: 8 << 20, PastPrefixes: []string{"k.oldprefix"},
	}
}

// partBody writes RGWUploadPartInfo's fields through accounted_size.
func partBody(e *denc.Encoder, p meta.UploadPartInfo, r denc.Release) {
	e.U32(p.Num)
	e.U64(p.Size)
	e.String(p.ETag)
	e.Time(p.Modified)
	p.Manifest.Encode(e, r)
	p.Compression.Encode(e, r)
	e.U64(p.AccountedSize)
}

// tentaclePartInfo is ceph-dencoder v20.2.4's re-encoding of the corpus
// object RGWUploadPartInfo/19.2.0-404-g78ddc7f9027/1a077e3237db126a58e0b4985defeb2d,
// a part uploaded again under a random prefix:
//
//	podman run --rm -v <corpus>/archive/19.2.0-404-g78ddc7f9027/objects/RGWUploadPartInfo:/in:ro \
//	  quay.io/ceph/ceph:v20.2.4 ceph-dencoder type RGWUploadPartInfo \
//	  import /in/1a077e3237db126a58e0b4985defeb2d decode encode export /dev/stdout | xxd -p
const tentaclePartInfo = "0602170200000100000000005000000000002000000033373063306235643066313139343965" +
	"363562646236373761613738663262645b1cf966b4f403010806800100000000500000000000" +
	"00000000000606a90000000a0a8c00000021000000796f75726e616d65686572652d65706372" +
	"776331656863396e6d3771752d3336322d00000031336565613036382d366434372d34303133" +
	"2d396661632d6238376164653536396131632e343533352e3337342d00000031336565613036" +
	"382d366434372d343031332d396661632d6238376164653536396131632e343533352e333734" +
	"0000000000000000000b0000006d796d756c7469706172740000000000000000000000000000" +
	"0000000000002c0000006d796d756c7469706172742e417a6856356336657058637249644742" +
	"566d523959387554383277337378514801000000000000000000000002012000000001000000" +
	"0000000000000000000000000000000000004000000000000000000000000000000011000000" +
	"64656661756c742d706c6163656d656e740000000002022d000000040000006e6f6e6504011e" +
	"000000000000000000000000000100000000000000080000005354414e444152440002011500" +
	"0000040000006e6f6e65000000000000000000000000000000500000000000010000002d0000" +
	"006d796d756c7469706172742e327e7139575a45537152744c786e2d6f626278387376553061" +
	"3162765352636a7000"

var _ = Describe("UploadPartInfo", func() {
	It("encodes v5 on Squid and v6 with an absent checksum on Tentacle, decoding both", func() {
		part := newPart()
		s := encodeWith(denc.Squid, part.Encode)
		t := encodeWith(denc.Tentacle, part.Encode)
		Expect(s).To(Equal(encoded(func(e *denc.Encoder) {
			beginEnd(e, 5, 2, func() {
				partBody(e, part, denc.Squid)
				e.U32(1)
				e.String("k.oldprefix")
			})
		})))
		Expect(t).To(Equal(encoded(func(e *denc.Encoder) {
			beginEnd(e, 6, 2, func() {
				partBody(e, part, denc.Tentacle)
				e.U32(1)
				e.String("k.oldprefix")
				e.U8(0)
			})
		})))
		Expect(decodeWhole(s, meta.DecodeUploadPartInfo)).To(Equal(part))
		Expect(decodeWhole(t, meta.DecodeUploadPartInfo)).To(Equal(part))
	})
	It("writes past_prefixes as the std::set it is: sorted, each once", func() {
		part := newPart()
		part.PastPrefixes = []string{"k.b", "k.a", "k.b"}
		got := decodeWhole(encodeWith(denc.Squid, part.Encode), meta.DecodeUploadPartInfo)
		Expect(got.PastPrefixes).To(Equal([]string{"k.a", "k.b"}))
		Expect(part.PastPrefixes).To(Equal([]string{"k.b", "k.a", "k.b"}), "Encode leaves the caller's slice alone")
	})
	It("reads a stored set as std::set decodes one, sorted and each once", func() {
		part := newPart()
		b := encoded(func(e *denc.Encoder) {
			beginEnd(e, 5, 2, func() {
				partBody(e, part, denc.Squid)
				denc.EncodeSlice(e, []string{"k.b", "k.a", "k.b"}, (*denc.Encoder).String)
			})
		})
		Expect(decodeWhole(b, meta.DecodeUploadPartInfo).PastPrefixes).To(Equal([]string{"k.a", "k.b"}))
	})
	It("decodes v1, with neither compat byte nor length, and v2, with accounted_size = size and no manifest", func() {
		want := meta.UploadPartInfo{
			Num: 1, Size: 10, ETag: "e", Modified: time.Unix(1, 0).UTC(),
			Manifest: meta.NewManifest(), Compression: meta.NewCompressionInfo(), AccountedSize: 10,
		}
		v1 := encoded(func(e *denc.Encoder) {
			e.U8(1)
			e.U32(1)
			e.U64(10)
			e.String("e")
			e.Time(time.Unix(1, 0))
		})
		Expect(decodeWhole(v1, meta.DecodeUploadPartInfo)).To(Equal(want))
		v2 := encoded(func(e *denc.Encoder) {
			beginEnd(e, 2, 2, func() {
				e.U32(1)
				e.U64(10)
				e.String("e")
				e.Time(time.Unix(1, 0))
			})
		})
		Expect(decodeWhole(v2, meta.DecodeUploadPartInfo)).To(Equal(want))
	})
	It("decodes v3, with a manifest but no compression info", func() {
		part := newPart()
		b := encoded(func(e *denc.Encoder) {
			beginEnd(e, 3, 2, func() {
				e.U32(2)
				e.U64(7)
				e.String("e")
				e.Time(part.Modified)
				part.Manifest.Encode(e, denc.Squid)
			})
		})
		got := decodeWhole(b, meta.DecodeUploadPartInfo)
		Expect(got.Manifest).To(Equal(part.Manifest))
		Expect(got.Compression).To(Equal(meta.NewCompressionInfo()))
		Expect(got.AccountedSize).To(BeEquivalentTo(7))
	})
	It("keeps a Tentacle checksum's bytes through a round trip, and drops them on Squid", func() {
		part := newPart()
		cksum := []byte{1, 1, 1, 2, 0, 0, 0, 7, 7}
		v6 := encoded(func(e *denc.Encoder) {
			beginEnd(e, 6, 2, func() {
				partBody(e, part, denc.Tentacle)
				e.U32(1)
				e.String("k.oldprefix")
				e.Raw(cksum)
			})
		})
		got := decodeWhole(v6, meta.DecodeUploadPartInfo)
		Expect(got.Cksum).To(Equal(cksum))
		Expect(encodeWith(denc.Tentacle, got.Encode)).To(Equal(v6))
		Expect(encodeWith(denc.Squid, got.Encode)).To(Equal(encodeWith(denc.Squid, part.Encode)))
	})
	It("re-encodes a checksum present under any non-zero flag with the flag 1, as bool's decode reads it", func() {
		part := newPart()
		v6 := func(flag byte) []byte {
			return encoded(func(e *denc.Encoder) {
				beginEnd(e, 6, 2, func() {
					partBody(e, part, denc.Tentacle)
					e.U32(0)
					e.U8(flag)
					beginEnd(e, 2, 1, func() { e.U16(1) })
				})
			})
		}
		got := decodeWhole(v6(7), meta.DecodeUploadPartInfo)
		Expect(encodeWith(denc.Tentacle, got.Encode)).To(Equal(v6(1)))
	})
	It("keeps a field a later version adds after the checksum out of it", func() {
		part := newPart()
		v6 := encoded(func(e *denc.Encoder) {
			beginEnd(e, 6, 2, func() {
				partBody(e, part, denc.Tentacle)
				e.U32(0)
				e.U8(0)
			})
		})
		v7 := encoded(func(e *denc.Encoder) {
			beginEnd(e, 7, 2, func() {
				partBody(e, part, denc.Tentacle)
				e.U32(0)
				e.U8(0)
				e.U64(0xFEED)
			})
		})
		got := decodeWhole(v7, meta.DecodeUploadPartInfo)
		Expect(got.Cksum).To(BeNil())
		Expect(encodeWith(denc.Tentacle, got.Encode)).To(Equal(v6))
	})
	It("refuses a checksum whose compat is past the version Tentacle decodes", func() {
		part := newPart()
		b := encoded(func(e *denc.Encoder) {
			beginEnd(e, 6, 2, func() {
				partBody(e, part, denc.Tentacle)
				e.U32(0)
				e.U8(1)
				beginEnd(e, 3, 3, func() {})
			})
		})
		Expect(decodeErr(b, meta.DecodeUploadPartInfo)).To(MatchError(denc.ErrIncompatible))
	})
	It("re-encodes a corpus part info as the Tentacle dencoder does", func() {
		cs, err := loadGoldens("RGWUploadPartInfo")
		Expect(err).NotTo(HaveOccurred())
		var bin []byte
		for _, c := range cs {
			if c.Archive == "19.2.0-404-g78ddc7f9027" && c.Name == "1a077e3237db126a58e0b4985defeb2d" {
				bin = c.Bin
			}
		}
		Expect(bin).NotTo(BeNil())
		want, err := hex.DecodeString(tentaclePartInfo)
		Expect(err).NotTo(HaveOccurred())
		info := decodeWhole(bin, meta.DecodeUploadPartInfo)
		Expect(encodeWith(denc.Tentacle, info.Encode)).To(Equal(want))
		Expect(decodeWhole(want, meta.DecodeUploadPartInfo)).To(Equal(info), "v6 decodes to what v5 did")
	})
})

// tentacleUploadInfo is ceph-dencoder v20.2.4's re-encoding of the corpus
// object multipart_upload_info/19.2.0-404-g78ddc7f9027/56240006220616a0e147f23db9c1a51e:
//
//	podman run --rm -v <corpus>/archive/19.2.0-404-g78ddc7f9027/objects/multipart_upload_info:/in:ro \
//	  quay.io/ceph/ceph:v20.2.4 ceph-dencoder type multipart_upload_info \
//	  import /in/56240006220616a0e147f23db9c1a51e decode encode export /dev/stdout | xxd -p
const tentacleUploadInfo = "04014f00000021000000646573745f706c6163656d656e742f646573745f73746f726167655f" +
	"636c617373000002011400000000000000000000000000000000000000000000000101040000" +
	"000000000000000000"

var _ = Describe("MultipartUploadInfo", func() {
	It("encodes Squid's v2 and Tentacle's v4 and decodes v1 to v4", func() {
		u := meta.MultipartUploadInfo{DestPlacement: meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}}
		placement := append([]byte{22, 0, 0, 0}, "default-placement/COLD"...)
		// a default RGWObjectRetention: its header, an empty mode, a zero
		// utime_t and round_trip_encode's zero nanoseconds
		retention := []byte{2, 1, 20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		// a default RGWObjectLegalHold: its header and an empty status
		legalHold := []byte{1, 1, 4, 0, 0, 0, 0, 0, 0, 0}
		body := append(append(append(placement, 0, 0), retention...), legalHold...)
		Expect(body).To(HaveLen(0x40))
		s := encodeWith(denc.Squid, u.Encode)
		Expect(s).To(Equal(append([]byte{2, 1, 0x40, 0, 0, 0}, body...)))
		t := encodeWith(denc.Tentacle, u.Encode)
		Expect(t).To(Equal(append(append([]byte{4, 1, 0x44, 0, 0, 0}, body...), 0, 0, 0, 0)), "cksum type none, flags 0")
		Expect(decodeWhole(s, meta.DecodeMultipartUploadInfo)).To(Equal(u))
		Expect(decodeWhole(t, meta.DecodeMultipartUploadInfo)).To(Equal(u))

		v1 := encoded(func(e *denc.Encoder) { beginEnd(e, 1, 1, func() { u.DestPlacement.Encode(e, denc.Squid) }) })
		Expect(decodeWhole(v1, meta.DecodeMultipartUploadInfo)).To(Equal(u))
		v3 := encoded(func(e *denc.Encoder) {
			beginEnd(e, 3, 1, func() {
				e.Raw(body)
				e.U16(3)
			})
		})
		Expect(decodeWhole(v3, meta.DecodeMultipartUploadInfo)).To(Equal(meta.MultipartUploadInfo{DestPlacement: u.DestPlacement, CksumType: 3}))
	})
	It("carries Tentacle's checksum type and flags", func() {
		u := meta.MultipartUploadInfo{DestPlacement: meta.PlacementRule{Name: "p"}, CksumType: 2, CksumFlags: 4}
		t := encodeWith(denc.Tentacle, u.Encode)
		Expect(t[len(t)-4:]).To(Equal([]byte{2, 0, 4, 0}))
		Expect(decodeWhole(t, meta.DecodeMultipartUploadInfo)).To(Equal(u))
		Expect(decodeWhole(encodeWith(denc.Squid, u.Encode), meta.DecodeMultipartUploadInfo)).
			To(Equal(meta.MultipartUploadInfo{DestPlacement: u.DestPlacement}), "Squid has neither")
	})
	It("carries retention and legal hold when set", func() {
		until := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
		u := meta.MultipartUploadInfo{
			DestPlacement: meta.PlacementRule{Name: "p"},
			Retention:     &meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: until},
			LegalHold:     &meta.ObjectLegalHold{Status: "ON"},
		}
		b := encodeWith(denc.Squid, u.Encode)
		Expect(b[6:13]).To(Equal([]byte{1, 0, 0, 0, 'p', 1, 1}), "both existence flags")
		Expect(decodeWhole(b, meta.DecodeMultipartUploadInfo)).To(Equal(u))
	})
	It("re-encodes a corpus upload info as the Tentacle dencoder does", func() {
		cs, err := loadGoldens("multipart_upload_info")
		Expect(err).NotTo(HaveOccurred())
		var bin []byte
		for _, c := range cs {
			if c.Archive == "19.2.0-404-g78ddc7f9027" && c.Name == "56240006220616a0e147f23db9c1a51e" {
				bin = c.Bin
			}
		}
		Expect(bin).NotTo(BeNil())
		want, err := hex.DecodeString(tentacleUploadInfo)
		Expect(err).NotTo(HaveOccurred())
		info := decodeWhole(bin, meta.DecodeMultipartUploadInfo)
		Expect(encodeWith(denc.Tentacle, info.Encode)).To(Equal(want))
		Expect(decodeWhole(want, meta.DecodeMultipartUploadInfo)).To(Equal(info), "v4 decodes to what v2 did")
	})
})
