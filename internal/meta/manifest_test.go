package meta_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var (
	headBucket = meta.BucketID{Name: "plain", Marker: "zone.4156.1", ID: "zone.4156.1"}
	tailBucket = meta.BucketID{Tenant: "t", Name: "copy", Marker: "zone.9.9", ID: "zone.9.9"}
	headObj    = meta.Obj{Bucket: headBucket, Key: meta.ObjKey{Name: "obj", Instance: "v1"}}
)

func cloudTier() meta.ZoneGroupPlacementTier {
	t := meta.NewZoneGroupPlacementTier()
	t.TierType = meta.TierTypeCloudS3
	t.StorageClass = "CLOUD"
	t.S3.Endpoint = "http://cloud"
	return t
}

// orderManifest sets every RGWObjManifest field the corpus only holds at its
// default: a tail bucket and instance unlike the head's, an override prefix
// and a cloud tier.
func orderManifest() meta.Manifest {
	return meta.Manifest{
		Objs:              map[uint64]meta.ManifestPart{0: {Loc: headObj, LocOfs: 3, Size: 4}},
		ObjSize:           20,
		Obj:               headObj,
		HeadSize:          4,
		HeadPlacementRule: meta.PlacementRule{Name: "default-placement"},
		MaxHeadSize:       4,
		Prefix:            ".pfx_",
		TailPlacement: meta.BucketPlacement{
			Bucket:        tailBucket,
			PlacementRule: meta.PlacementRule{Name: "cold-placement", StorageClass: "COLD"},
		},
		Rules: map[uint64]meta.ManifestRule{
			0: {StartOfs: 4, StripeMaxSize: 8, OverridePrefix: "ovr_"},
		},
		TailInstance: "tail-inst",
		TierType:     meta.TierTypeCloudS3,
		TierConfig:   meta.ObjTier{Name: "tier", Tier: cloudTier(), IsMultipartUpload: true},
	}
}

// orderManifestBytes is orderManifest written field by field in
// RGWObjManifest::encode's order.
func orderManifestBytes(r denc.Release) []byte {
	m := orderManifest()
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 8, 6, func() {
			e.U64(20)
			e.U32(1)
			e.U64(0)
			m.Objs[0].Encode(e, r)
			e.Bool(false)
			headObj.Encode(e, r)
			e.U64(4)
			e.U64(4)
			e.String(".pfx_")
			e.U32(1)
			e.U64(0)
			m.Rules[0].Encode(e, r)
			e.Bool(true)
			tailBucket.Encode(e, r)
			e.Bool(true)
			e.String("tail-inst")
			e.String("default-placement")
			e.String("cold-placement/COLD")
			e.String("cloud-s3")
			beginEnd(e, 2, 2, func() {
				e.String("tier")
				cloudTier().Encode(e, r)
				e.Bool(true)
			})
		})
	})
}

// legacyPart writes an RGWObjManifestPart at version 1: a u32 version and no
// compat byte or length, which DECODE_START_LEGACY_COMPAT_LEN_32 accepts.
func legacyPart(e *denc.Encoder, loc meta.Obj, locOfs, size uint64) {
	e.U32(1)
	loc.Encode(e, denc.Squid)
	e.U64(locOfs)
	e.U64(size)
}

func stripesOf(m meta.Manifest) []meta.Stripe {
	GinkgoHelper()
	ss, err := m.Stripes()
	Expect(err).NotTo(HaveOccurred())
	return ss
}

func tail(name, ns string) meta.Obj {
	return meta.Obj{Bucket: tailBucket, Key: meta.ObjKey{Name: name, Instance: "tail-inst", NS: ns}}
}

var _ = Describe("Manifest", func() {
	Describe("hand-built fixtures", func() {
		DescribeTable("encodes every field in the C++ order",
			func(r denc.Release) {
				want := orderManifestBytes(r)
				Expect(encodeWith(r, orderManifest().Encode)).To(Equal(want))
				Expect(decodeWhole(want, meta.DecodeManifest)).To(Equal(orderManifest()))
			},
			Entry("at Squid", denc.Squid),
			Entry("at Tentacle, whose placement tier is version 4", denc.Tentacle),
		)
		It("writes the tail bucket and instance only when they differ from the head's", func() {
			m := orderManifest()
			m.TailPlacement.Bucket = meta.BucketID{Name: headBucket.Name, ID: headBucket.ID, Marker: "other"}
			m.TailInstance = headObj.Key.Instance
			got := decodeWhole(encodeWith(denc.Squid, m.Encode), meta.DecodeManifest)
			Expect(got.TailPlacement.Bucket).To(Equal(headBucket), "rgw_bucket == ignores the marker, so the head's bucket is restored")
			Expect(got.TailInstance).To(Equal("v1"))
		})
	})

	Describe("legacy decoding", func() {
		It("reads version 1: a u32 version, explicit pieces, the first of them the head", func() {
			shadow := meta.Obj{Bucket: headBucket, Key: meta.ObjKey{Name: "t", NS: "shadow"}}
			b := encoded(func(e *denc.Encoder) {
				e.U32(1)
				e.U64(15)
				e.U32(2)
				e.U64(0)
				legacyPart(e, headObj, 0, 10)
				e.U64(10)
				legacyPart(e, shadow, 2, 5)
			})
			Expect(decodeWhole(b, meta.DecodeManifest)).To(Equal(meta.Manifest{
				ExplicitObjs: true,
				Objs: map[uint64]meta.ManifestPart{
					0: {Loc: headObj, Size: 10}, 10: {Loc: shadow, LocOfs: 2, Size: 5},
				},
				ObjSize: 15, Obj: headObj, HeadSize: 10, MaxHeadSize: 10,
				TailInstance: "v1", TierConfig: meta.NewObjTier(),
			}))
		})
		It("reads version 3: rules without a tail bucket, the tail instance taken from the head", func() {
			b := encoded(func(e *denc.Encoder) {
				beginEnd(e, 3, 2, func() {
					e.U64(9)
					e.U32(0)
					e.Bool(false)
					headObj.Encode(e, denc.Squid)
					e.U64(4)
					e.U64(4)
					e.String("p_")
					e.U32(1)
					e.U64(0)
					beginEnd(e, 1, 1, func() {
						e.U32(0)
						e.U64(4)
						e.U64(0)
						e.U64(8)
					})
				})
			})
			m := decodeWhole(b, meta.DecodeManifest)
			Expect(m).To(Equal(meta.Manifest{
				ObjSize: 9, Obj: headObj, HeadSize: 4, MaxHeadSize: 4, Prefix: "p_",
				Rules:        map[uint64]meta.ManifestRule{0: {StartOfs: 4, StripeMaxSize: 8}},
				TailInstance: "v1", TierConfig: meta.NewObjTier(),
			}))
			ss := stripesOf(m)
			Expect(ss).To(HaveLen(2))
			Expect(ss[1].Obj).To(Equal(meta.Obj{Bucket: headBucket, Key: meta.ObjKey{Name: "p_1", Instance: "v1", NS: "shadow"}}),
				"an empty tail bucket falls back to the head's")
		})
		It("reads version 5: the tail bucket and instance stored unconditionally", func() {
			b := encoded(func(e *denc.Encoder) {
				beginEnd(e, 5, 2, func() {
					e.U64(0)
					e.U32(0)
					e.Bool(false)
					headObj.Encode(e, denc.Squid)
					e.U64(0)
					e.U64(0)
					e.String("")
					e.U32(0)
					tailBucket.Encode(e, denc.Squid)
					e.String("ti")
				})
			})
			Expect(decodeWhole(b, meta.DecodeManifest)).To(Equal(meta.Manifest{
				Obj: headObj, TailPlacement: meta.BucketPlacement{Bucket: tailBucket},
				TailInstance: "ti", TierConfig: meta.NewObjTier(),
			}))
		})
		It("reads version 6: the tail bucket and instance behind presence flags", func() {
			b := encoded(func(e *denc.Encoder) {
				beginEnd(e, 6, 6, func() {
					e.U64(0)
					e.U32(0)
					e.Bool(false)
					headObj.Encode(e, denc.Squid)
					e.U64(0)
					e.U64(0)
					e.String("")
					e.U32(0)
					e.Bool(true)
					tailBucket.Encode(e, denc.Squid)
					e.Bool(false)
				})
			})
			Expect(decodeWhole(b, meta.DecodeManifest)).To(Equal(meta.Manifest{
				Obj: headObj, TailPlacement: meta.BucketPlacement{Bucket: tailBucket},
				TailInstance: "v1", TierConfig: meta.NewObjTier(),
			}))
		})
		// explicitV8 is an explicit manifest with a head whose pieces are parts.
		explicitV8 := func(parts map[uint64]meta.ManifestPart) []byte {
			m := meta.NewManifest()
			m.ExplicitObjs = true
			m.Objs = parts
			m.ObjSize = 20
			m.Obj = headObj
			m.HeadSize = 4
			m.TailPlacement.Bucket = headBucket
			m.TailInstance = "v1"
			return encodeWith(denc.Squid, m.Encode)
		}
		It("replaces a plain object at offset 0 of an explicit manifest with the head (Ceph issue 16435)", func() {
			other := meta.Obj{Bucket: headBucket, Key: meta.ObjKey{Name: "copied-from"}}
			m := decodeWhole(explicitV8(map[uint64]meta.ManifestPart{0: {Loc: other, LocOfs: 1, Size: 9}}), meta.DecodeManifest)
			Expect(m.Objs).To(Equal(map[uint64]meta.ManifestPart{0: {Loc: headObj, LocOfs: 1, Size: 4}}))
		})
		It("keeps a namespaced object at offset 0", func() {
			shadow := meta.Obj{Bucket: headBucket, Key: meta.ObjKey{Name: "s", NS: "shadow"}}
			m := decodeWhole(explicitV8(map[uint64]meta.ManifestPart{0: {Loc: shadow, Size: 9}}), meta.DecodeManifest)
			Expect(m.Objs).To(Equal(map[uint64]meta.ManifestPart{0: {Loc: shadow, Size: 9}}))
		})
		It("inserts an empty piece at offset 0 when there is none, as objs[0] does, and keeps it", func() {
			shadow := meta.Obj{Bucket: headBucket, Key: meta.ObjKey{Name: "s", NS: "shadow"}}
			m := decodeWhole(explicitV8(map[uint64]meta.ManifestPart{4: {Loc: shadow, Size: 16}}), meta.DecodeManifest)
			Expect(m.Objs).To(Equal(map[uint64]meta.ManifestPart{0: {}, 4: {Loc: shadow, Size: 16}}))
		})
		It("reads RGWObjManifestPart version 1 and RGWObjManifestRule version 1", func() {
			Expect(decodeWhole(encoded(func(e *denc.Encoder) { legacyPart(e, headObj, 1, 2) }), meta.DecodeManifestPart)).
				To(Equal(meta.ManifestPart{Loc: headObj, LocOfs: 1, Size: 2}))
			rule := encoded(func(e *denc.Encoder) {
				beginEnd(e, 1, 1, func() {
					e.U32(7)
					e.U64(1)
					e.U64(2)
					e.U64(3)
				})
			})
			Expect(decodeWhole(rule, meta.DecodeManifestRule)).
				To(Equal(meta.ManifestRule{StartPartNum: 7, StartOfs: 1, PartSize: 2, StripeMaxSize: 3}))
		})
		It("reads RGWObjTier version 1, with neither compat byte nor length", func() {
			b := encoded(func(e *denc.Encoder) {
				e.U8(1)
				e.String("t")
				cloudTier().Encode(e, denc.Squid)
				e.Bool(true)
			})
			Expect(decodeWhole(b, meta.DecodeObjTier)).To(Equal(meta.ObjTier{Name: "t", Tier: cloudTier(), IsMultipartUpload: true}))
		})
	})

	Describe("JSON", func() {
		It("writes tier_config for either cloud tier type, as v20.2.4 does", func() {
			for tt, want := range map[string]bool{meta.TierTypeCloudS3: true, meta.TierTypeCloudS3Glacier: true, "archive": false, "": false} {
				m := orderManifest()
				m.TierType = tt
				b, err := json.Marshal(m)
				Expect(err).NotTo(HaveOccurred())
				var got map[string]any
				Expect(json.Unmarshal(b, &got)).To(Succeed())
				Expect(got).To(HaveKeyWithValue("tier_type", tt))
				if want {
					Expect(got).To(HaveKey("tier_config"), tt)
				} else {
					Expect(got).NotTo(HaveKey("tier_config"), tt)
				}
			}
		})
	})
})

var _ = Describe("Manifest.Stripes", func() {
	It("walks the head, then the override prefix's shadow stripes in the tail bucket", func() {
		Expect(stripesOf(orderManifest())).To(Equal([]meta.Stripe{
			{Ofs: 0, Size: 4, Obj: headObj, Placement: meta.PlacementRule{Name: "default-placement"}, InHead: true},
			{Ofs: 4, Size: 8, Obj: tail("ovr_1", "shadow"), Placement: meta.PlacementRule{Name: "cold-placement", StorageClass: "COLD"}},
			{Ofs: 12, Size: 8, Obj: tail("ovr_2", "shadow"), Placement: meta.PlacementRule{Name: "cold-placement", StorageClass: "COLD"}},
		}))
	})
	It("names stripes as RADOS objects with the bucket marker and escaping", func() {
		ss := stripesOf(orderManifest())
		Expect(ss[0].OID()).To(Equal("zone.4156.1__:v1_obj"))
		Expect(ss[1].OID()).To(Equal("zone.9.9__shadow:tail-inst_ovr_1"))
		under := meta.Stripe{Obj: meta.Obj{Bucket: headBucket, Key: meta.ObjKey{Name: "_under"}}}
		Expect(under.OID()).To(Equal("zone.4156.1___under"))
		Expect(under.Locator()).To(Equal("zone.4156.1__under"))
		Expect(ss[1].Locator()).To(BeEmpty())
	})
	It("reports an explicit manifest's pieces with their sizes and offsets", func() {
		shadow := meta.Obj{Bucket: headBucket, Key: meta.ObjKey{Name: "t", NS: "shadow"}}
		m := meta.Manifest{
			ExplicitObjs: true, ObjSize: 15, Obj: headObj, HeadSize: 10,
			Objs: map[uint64]meta.ManifestPart{0: {Loc: headObj, Size: 10}, 10: {Loc: shadow, LocOfs: 2, Size: 5}},
		}
		Expect(stripesOf(m)).To(Equal([]meta.Stripe{
			{Ofs: 0, Size: 10, Obj: headObj, InHead: true},
			{Ofs: 10, Size: 5, LocOfs: 2, Obj: shadow},
		}))
	})
	DescribeTable("serves a manifest without rules from its head when the head holds the whole object",
		func(headSize uint64) {
			m := meta.Manifest{
				ObjSize: 3, HeadSize: headSize, MaxHeadSize: headSize, Obj: headObj,
				HeadPlacementRule: meta.PlacementRule{Name: "default-placement"},
			}
			Expect(stripesOf(m)).To(Equal([]meta.Stripe{
				{Ofs: 0, Size: 3, Obj: headObj, Placement: meta.PlacementRule{Name: "default-placement"}, InHead: true},
			}))
		},
		Entry("a head of exactly the object size", uint64(3)),
		Entry("a head larger than the object", uint64(8)),
	)

	// The expected layouts are traced by hand through src/rgw/rgw_obj_manifest.cc
	// in main: obj_iterator::seek (lines 130-172) picks rule 0 and part 1 at
	// offset 0; operator++ (lines 53-99) adds stripe_max_size to stripe_ofs,
	// moves to the next part once stripe_ofs reaches part_ofs + part_size
	// (lines 64-84), switching to next_rule_iter when stripe_ofs reaches its
	// start_ofs and taking its start_part_num and override_prefix (lines 72-78
	// and 89), and trims the stripe to the part (line 86); convert_to_explicit
	// (rgw/driver/rados/rgw_obj_manifest.cc lines 141-157) sizes each stripe as
	// the distance to the next stripe_ofs. get_implicit_location (lines
	// 209-259) names a part's first stripe <prefix>.<part> in multipart and the
	// rest <prefix>.<part>_<stripe> in shadow.
	DescribeTable("lays out multi-rule manifests as obj_iterator does",
		func(size uint64, rules map[uint64]meta.ManifestRule, want []stripeName) {
			m := meta.Manifest{ObjSize: size, Obj: headObj, Prefix: "p", Rules: rules}
			got := make([]stripeName, 0, len(want))
			for _, s := range stripesOf(m) {
				Expect(s.InHead).To(BeFalse())
				Expect(s.Obj.Bucket).To(Equal(headBucket), "an empty tail bucket falls back to the head's")
				got = append(got, stripeName{s.Ofs, s.Size, s.Obj.Key.NS + "/" + s.Obj.Key.Name, false})
			}
			Expect(got).To(Equal(want))
		},
		// Parts of 10 bytes in stripes of 4 leave each part's last stripe
		// 2 bytes long; the 5-byte last part gets its own rule, as
		// RGWObjManifest::append adds one when the part size changes.
		Entry("a part size that is not a multiple of the stripe size", uint64(25),
			map[uint64]meta.ManifestRule{
				0:  {StartPartNum: 1, PartSize: 10, StripeMaxSize: 4},
				20: {StartPartNum: 3, StartOfs: 20, PartSize: 5, StripeMaxSize: 4},
			},
			[]stripeName{
				{0, 4, "multipart/p.1", false},
				{4, 4, "shadow/p.1_1", false},
				{8, 2, "shadow/p.1_2", false},
				{10, 4, "multipart/p.2", false},
				{14, 4, "shadow/p.2_1", false},
				{18, 2, "shadow/p.2_2", false},
				{20, 4, "multipart/p.3", false},
				{24, 1, "shadow/p.3_1", false},
			}),
		// Part 2 was uploaded again under another prefix, so
		// RGWObjManifest::append gave it a rule of its own carrying that
		// prefix, and part 3 a rule going back to the manifest's.
		Entry("a re-uploaded part whose rule carries an override prefix", uint64(24),
			map[uint64]meta.ManifestRule{
				0:  {StartPartNum: 1, PartSize: 8, StripeMaxSize: 4},
				8:  {StartPartNum: 2, StartOfs: 8, PartSize: 8, StripeMaxSize: 4, OverridePrefix: "q"},
				16: {StartPartNum: 3, StartOfs: 16, PartSize: 8, StripeMaxSize: 4},
			},
			[]stripeName{
				{0, 4, "multipart/p.1", false},
				{4, 4, "shadow/p.1_1", false},
				{8, 4, "multipart/q.2", false},
				{12, 4, "shadow/q.2_1", false},
				{16, 4, "multipart/p.3", false},
				{20, 4, "shadow/p.3_1", false},
			}),
	)

	DescribeTable("fails where the C++ iterator would never finish or would read past a map",
		func(m meta.Manifest) {
			_, err := m.Stripes()
			Expect(err).To(MatchError(denc.ErrMalformed))
		},
		Entry("no rules and no head", meta.Manifest{ObjSize: 10, Prefix: "p"}),
		Entry("no rules and a tail beyond the head", meta.Manifest{ObjSize: 10, HeadSize: 4, MaxHeadSize: 4, Obj: headObj}),
		Entry("a zero stripe size", meta.Manifest{ObjSize: 10, Rules: map[uint64]meta.ManifestRule{0: {}}}),
		Entry("an explicit manifest without pieces", meta.Manifest{ExplicitObjs: true, ObjSize: 10}),
		Entry("a part boundary after the head, where next_rule_iter was never set", meta.Manifest{
			ObjSize: 10, HeadSize: 2, MaxHeadSize: 2,
			Rules: map[uint64]meta.ManifestRule{0: {PartSize: 4, StripeMaxSize: 4}},
		}),
	)

	Describe("the stripe bound", func() {
		var striped meta.Manifest

		BeforeEach(func() {
			// eight 1-byte stripes after a 2-byte head
			striped = meta.Manifest{
				ObjSize: 10, HeadSize: 2, MaxHeadSize: 2, Obj: headObj, Prefix: "p",
				Rules: map[uint64]meta.ManifestRule{0: {StripeMaxSize: 1}},
			}
		})

		It("lays out a manifest at the bound", func() {
			ss, err := striped.StripesUpTo(9)
			Expect(err).NotTo(HaveOccurred())
			Expect(ss).To(HaveLen(9))
		})

		It("refuses a manifest one stripe past the bound while walking it", func() {
			// The tail alone needs 8 stripes, so the bound passes the
			// up-front check and trips on the head's extra stripe.
			_, err := striped.StripesUpTo(8)
			Expect(err).To(MatchError(meta.ErrTooManyStripes))
		})

		It("refuses a manifest whose tail cannot fit the bound before walking it", func() {
			_, err := striped.StripesUpTo(7)
			Expect(err).To(MatchError(ContainSubstring("at least 8")))
		})

		It("refuses at once a 1 TiB manifest of 1-byte stripes", func() {
			hostile := striped
			hostile.ObjSize = 1 << 40
			_, err := hostile.Stripes()
			Expect(err).To(MatchError(meta.ErrTooManyStripes))
		})

		It("refuses an explicit manifest with more pieces than the bound", func() {
			m := meta.Manifest{ExplicitObjs: true, ObjSize: 3, Objs: map[uint64]meta.ManifestPart{
				0: {Loc: headObj, Size: 1}, 1: {Loc: headObj, Size: 1}, 2: {Loc: headObj, Size: 1},
			}}
			_, err := m.StripesUpTo(2)
			Expect(err).To(MatchError(meta.ErrTooManyStripes))
		})

		It("fits a 5 TiB object of 4 MiB stripes in 10,000 parts", func() {
			const parts, partSize = 10000, 5 << 40 / 10000
			stripesPerPart := (partSize + (4<<20 - 1)) / (4 << 20)
			Expect(parts * stripesPerPart).To(BeNumerically("<=", meta.MaxStripes))
		})
	})

	It("lays out every corpus manifest as the C++ iterator does", func() {
		cs, err := loadGoldens("RGWObjManifest")
		Expect(err).NotTo(HaveOccurred())
		var ruled, explicit, malformed int
		for _, c := range cs {
			id := c.Archive + "/" + c.Name
			m := decodeWhole(c.Bin, meta.DecodeManifest)
			ss, err := m.Stripes()
			switch {
			case !m.ExplicitObjs && len(m.Rules) == 0 && m.ObjSize > m.HeadSize:
				// Only pieces, but not marked explicit: operator++ returns
				// early without rules, so the C++ loop never ends.
				Expect(err).To(MatchError(denc.ErrMalformed), id)
				malformed++
			case m.ExplicitObjs:
				Expect(err).NotTo(HaveOccurred(), id)
				explicit++
				expectExplicitStripes(id, m, ss)
			default:
				Expect(err).NotTo(HaveOccurred(), id)
				ruled++
				expectContiguous(id, m, ss)
			}
		}
		Expect(ruled).To(BeNumerically(">", 30))
		Expect(explicit).To(BeNumerically(">=", 6))
		Expect(malformed).To(Equal(2))
	})

	Describe("the manifests radosgw v19.2.6 wrote for the populated cluster", func() {
		const marker = "e7bceed5-d2a6-4b0b-b484-cac20e0beb53.4156.1"
		load := func(name string) (meta.Manifest, []byte) {
			GinkgoHelper()
			b, err := os.ReadFile(filepath.Join("testdata", "manifests", name+".bin"))
			Expect(err).NotTo(HaveOccurred())
			return decodeWhole(b, meta.DecodeManifest), b
		}
		DescribeTable("re-encode byte for byte and dump as radosgw-admin object stat does",
			func(name string) {
				m, b := load(name)
				Expect(encodeWith(denc.Squid, m.Encode)).To(Equal(b))
				want, err := os.ReadFile(filepath.Join("testdata", "manifests", name+".json"))
				Expect(err).NotTo(HaveOccurred())
				got, err := json.Marshal(m)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(MatchJSON(want))
			},
			Entry("large.bin", "squid-large"),
			Entry("multipart.bin", "squid-multipart"),
		)
		It("lays large.bin out as a 4 MiB head and two shadow stripes", func() {
			m, _ := load("squid-large")
			const prefix = ".AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_"
			Expect(stripeNames(stripesOf(m))).To(Equal([]stripeName{
				{0, 4 << 20, marker + "_large.bin", true},
				{4 << 20, 4 << 20, marker + "__shadow_" + prefix + "1", false},
				{8 << 20, 2 << 20, marker + "__shadow_" + prefix + "2", false},
			}))
		})
		It("lays multipart.bin out by part across two rules, with no head data", func() {
			m, _ := load("squid-multipart")
			const prefix = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO"
			Expect(m.Rules).To(HaveLen(2))
			Expect(stripeNames(stripesOf(m))).To(Equal([]stripeName{
				{0, 4 << 20, marker + "__multipart_" + prefix + ".1", false},
				{4 << 20, 4 << 20, marker + "__shadow_" + prefix + ".1_1", false},
				{8 << 20, 4 << 20, marker + "__multipart_" + prefix + ".2", false},
				{12 << 20, 4 << 20, marker + "__shadow_" + prefix + ".2_1", false},
				{16 << 20, 4 << 20, marker + "__multipart_" + prefix + ".3", false},
			}))
		})
	})
})

type stripeName struct {
	Ofs, Size uint64
	OID       string
	InHead    bool
}

func stripeNames(ss []meta.Stripe) []stripeName {
	out := make([]stripeName, 0, len(ss))
	for _, s := range ss {
		out = append(out, stripeName{s.Ofs, s.Size, s.OID(), s.InHead})
	}
	return out
}

// expectContiguous asserts that a rule-based layout covers the object
// without gaps and starts with the head when there is one.
func expectContiguous(id string, m meta.Manifest, ss []meta.Stripe) {
	GinkgoHelper()
	var next uint64
	for _, s := range ss {
		Expect(s.Ofs).To(Equal(next), id)
		next += s.Size
	}
	Expect(next).To(Equal(m.ObjSize), "%s: stripe sizes sum to the object size", id)
	if m.HeadSize > 0 && m.ObjSize > 0 {
		Expect(ss[0].InHead).To(BeTrue(), id)
		Expect(ss[0].Obj).To(Equal(m.Obj), id)
		Expect(ss[0].Size).To(Equal(min(m.HeadSize, m.ObjSize)), id)
	}
}

// expectExplicitStripes asserts that an explicit layout is its pieces from
// the last one at or before offset 0, up to the object size. The corpus's
// generated explicit manifests key each piece by its end offset, so the C++
// iterator starts at the first piece's key and the sizes fall one piece short.
func expectExplicitStripes(id string, m meta.Manifest, ss []meta.Stripe) {
	GinkgoHelper()
	i := 0
	for _, k := range slices.Sorted(maps.Keys(m.Objs)) {
		if k >= m.ObjSize {
			break
		}
		p := m.Objs[k]
		Expect(i).To(BeNumerically("<", len(ss)), id)
		Expect(ss[i]).To(Equal(meta.Stripe{
			Ofs: k, Size: p.Size, LocOfs: p.LocOfs, Obj: p.Loc,
			InHead: p.Loc.Bucket == m.Obj.Bucket && p.Loc.Key == m.Obj.Key,
		}), id)
		i++
	}
	Expect(ss).To(HaveLen(i), id)
}

func orderCompressionInfo() meta.CompressionInfo {
	msg := int32(-7)
	return meta.CompressionInfo{
		Type: "zlib", OrigSize: 100, CompressorMessage: &msg,
		Blocks: []meta.CompressionBlock{{OldOfs: 1, NewOfs: 2, Len: 3}},
	}
}

var _ = Describe("CompressionInfo", func() {
	It("encodes the compressor message the corpus never sets", func() {
		info := orderCompressionInfo()
		want := encoded(func(e *denc.Encoder) {
			beginEnd(e, 2, 1, func() {
				e.String("zlib")
				e.U64(100)
				e.Bool(true)
				e.I32(-7)
				e.U32(1)
				beginEnd(e, 1, 1, func() {
					e.U64(1)
					e.U64(2)
					e.U64(3)
				})
			})
		})
		expectOrder(info, want, func(e *denc.Encoder) { info.Encode(e, denc.Squid) }, meta.DecodeCompressionInfo)
		b, err := json.Marshal(info)
		Expect(err).NotTo(HaveOccurred())
		Expect(b).To(MatchJSON(`{"compression_type":"zlib","orig_size":100,"compressor_message":-7,
			"blocks":[{"old_ofs":1,"new_ofs":2,"len":3}]}`))
	})
	It("defaults the type to none", func() {
		Expect(meta.NewCompressionInfo().Type).To(Equal("none"))
	})
})

func orderCacheInfo() meta.ObjectCacheInfo {
	return meta.ObjectCacheInfo{
		Status: -2, Flags: meta.CacheFlagData | meta.CacheFlagObjVersion, Epoch: 9,
		Data:     []byte("data"),
		Xattrs:   map[string][]byte{"x": []byte("1")},
		RMXattrs: map[string][]byte{"r": []byte("2")},
		Meta:     meta.ObjectMetaInfo{Size: 5},
		Version:  version.ObjVersion{Ver: 3, Tag: "tag"},
	}
}

func encCacheInfoBody(e *denc.Encoder, withRM bool) {
	e.I32(-2)
	e.U32(0x11)
	e.Bytes32([]byte("data"))
	e.U32(1)
	e.String("x")
	e.Bytes32([]byte("1"))
	meta.ObjectMetaInfo{Size: 5}.Encode(e, denc.Squid)
	if withRM {
		e.U32(1)
		e.String("r")
		e.Bytes32([]byte("2"))
	}
}

var _ = Describe("CacheNotifyInfo", func() {
	It("encodes the offset, namespace, status and epoch the corpus only holds at zero", func() {
		n := meta.CacheNotifyInfo{
			Op: meta.CacheInvalidateObj, Obj: meta.RawObj{Pool: meta.Pool{Name: "p"}, OID: "o"},
			ObjInfo: orderCacheInfo(), Ofs: -3, NS: "ns",
		}
		want := encoded(func(e *denc.Encoder) {
			beginEnd(e, 2, 2, func() {
				e.U32(1)
				n.Obj.Encode(e, denc.Squid)
				beginEnd(e, 5, 3, func() {
					encCacheInfoBody(e, true)
					e.U64(9)
					beginEnd(e, 1, 1, func() {
						e.U64(3)
						e.String("tag")
					})
				})
				e.I64(-3)
				e.String("ns")
			})
		})
		expectOrder(n, want, func(e *denc.Encoder) { n.Encode(e, denc.Squid) }, meta.DecodeCacheNotifyInfo)
	})
	It("reads ObjectCacheInfo version 1, with neither compat byte, length nor removed attrs", func() {
		b := encoded(func(e *denc.Encoder) {
			e.U8(1)
			encCacheInfoBody(e, false)
		})
		want := orderCacheInfo()
		want.RMXattrs, want.Epoch, want.Version = nil, 0, version.ObjVersion{}
		Expect(decodeWhole(b, meta.DecodeObjectCacheInfo)).To(Equal(want))
	})
	It("reads ObjectCacheInfo version 3, with removed attrs but no epoch", func() {
		b := encoded(func(e *denc.Encoder) { beginEnd(e, 3, 3, func() { encCacheInfoBody(e, true) }) })
		want := orderCacheInfo()
		want.Epoch, want.Version = 0, version.ObjVersion{}
		Expect(decodeWhole(b, meta.DecodeObjectCacheInfo)).To(Equal(want))
	})
	It("reads ObjectMetaInfo and RGWCacheNotifyInfo version 1, with neither compat byte nor length", func() {
		mi := encoded(func(e *denc.Encoder) {
			e.U8(1)
			e.U64(5)
			e.U32(0)
			e.U32(0)
		})
		Expect(decodeWhole(mi, meta.DecodeObjectMetaInfo)).To(Equal(meta.ObjectMetaInfo{Size: 5}))
		n := encoded(func(e *denc.Encoder) {
			e.U8(1)
			e.U32(0)
			meta.RawObj{OID: "o"}.Encode(e, denc.Squid)
			meta.ObjectCacheInfo{}.Encode(e, denc.Squid)
			e.I64(7)
			e.String("")
		})
		Expect(decodeWhole(n, meta.DecodeCacheNotifyInfo)).To(Equal(meta.CacheNotifyInfo{Obj: meta.RawObj{OID: "o"}, Ofs: 7}))
	})
	It("dumps attrs as encode_json_map names them and data as base64", func() {
		b, err := json.Marshal(orderCacheInfo())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`{"status":-2,"flags":17,"data":"ZGF0YQ==",`+
			`"xattrs":[{"name":"x","value":{"length":"MQ=="}}],`+
			`"rm_xattrs":[{"name":"r","value":{"length":"Mg=="}}],`+
			`"meta":{"size":5,"mtime":"0.000000"}}`), "ObjectCacheInfo::dump writes meta last")
	})
})

var _ = DescribeTable("attr names",
	func(got, want string) { Expect(got).To(Equal(want)) },
	Entry("manifest", meta.AttrManifest, "user.rgw.manifest"),
	Entry("user metadata prefix", meta.AttrMetaPrefix, "user.rgw.x-amz-meta-"),
	Entry("cls_version's attr", meta.AttrObjVersion, "ceph.objclass.version"),
)
