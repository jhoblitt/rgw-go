package meta_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("part manifests", func() {
	It("lays a part out as radosgw's MultipartObjectProcessor does", func() {
		m := meta.NewPartManifest(partTarget, partRule, meta.PlacementRule{}, "multipart.bin.2~id", 2, 4<<20)
		Expect(m.Obj).To(Equal(partTarget))
		Expect(m.HeadSize).To(BeZero())
		Expect(m.MaxHeadSize).To(BeZero())
		Expect(m.HeadPlacementRule).To(Equal(partRule))
		Expect(m.Prefix).To(Equal("multipart.bin.2~id"))
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{0: {StartPartNum: 2, StripeMaxSize: 4 << 20}}))
		Expect(m.TailPlacement).To(Equal(meta.BucketPlacement{Bucket: partBucket, PlacementRule: partRule}), "inherit_from the head rule")
		Expect(m.TierConfig).To(Equal(meta.NewObjTier()))
		Expect(m.StripeObj(2, 0)).To(Equal(meta.Obj{Bucket: partBucket, Key: meta.ObjKey{Name: "multipart.bin.2~id.2", NS: meta.NSMultipart}}), "the part head")
		Expect(m.StripeObj(2, 1)).To(Equal(meta.Obj{Bucket: partBucket, Key: meta.ObjKey{Name: "multipart.bin.2~id.2_1", NS: meta.NSShadow}}))
		Expect(m.StripeObj(0, 1)).To(Equal(m.TailObj(1)), "TailObj is part 0")
		m.SetObjSize(6 << 20)
		stripes := stripesOf(m)
		Expect(stripes).To(HaveLen(2))
		Expect(stripes[0].OID()).To(Equal("m1__multipart_multipart.bin.2~id.2"))
		Expect(stripes[0].Obj).To(Equal(m.StripeObj(2, 0)))
		Expect(stripes[1].OID()).To(Equal("m1__shadow_multipart.bin.2~id.2_1"))
		Expect(stripes[1].Size).To(BeEquivalentTo(2 << 20))
	})
	It("takes a tail storage class and keeps the target's instance on the tails", func() {
		target := partTarget
		target.Key.Instance = "v1"
		cold := meta.PlacementRule{StorageClass: "COLD"}
		m := meta.NewPartManifest(target, partRule, cold, "p", 1, 4<<20)
		Expect(m.TailPlacement.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
		Expect(m.TailInstance).To(Equal("v1"))
		Expect(m.StripeObj(1, 0).Key).To(Equal(meta.ObjKey{Name: "p.1", Instance: "v1", NS: meta.NSMultipart}))
	})
	It("names a part's objects with rule 0's override prefix", func() {
		m := meta.NewPartManifest(partTarget, partRule, meta.PlacementRule{}, "p", 4, 4<<20)
		r := m.Rules[0]
		r.OverridePrefix = "q"
		m.Rules[0] = r
		Expect(m.StripeObj(4, 0).Key.Name).To(Equal("q.4"))
		Expect(m.StripeObj(4, 3).Key.Name).To(Equal("q.4_3"))
	})

	It("appends parts into the rules the corpus multipart manifest carries", func() {
		// squid-multipart.bin: 20 MiB in parts of 8, 8 and 4 MiB, stripe 4 MiB
		b, err := os.ReadFile(filepath.Join("testdata", "manifests", "squid-multipart.bin"))
		Expect(err).NotTo(HaveOccurred())
		golden := decodeWhole(b, meta.DecodeManifest)
		// prepare_head's head rule is the bucket's placement rule, empty for
		// this bucket, and its tail rule the upload's destination placement.
		Expect(golden.HeadPlacementRule).To(BeZero())
		dest := meta.PlacementRule{Name: "default-placement"}
		var m meta.Manifest
		for i, size := range []uint64{8 << 20, 8 << 20, 4 << 20} {
			p := meta.NewPartManifest(golden.Obj, golden.HeadPlacementRule, dest, golden.Prefix, uint32(i+1), 4<<20)
			p.SetObjSize(size)
			Expect(m.Append(p)).To(Succeed())
		}
		Expect(m).To(Equal(golden), "rgw_obj_manifest.cc:44-127")
	})
	It("absorbs equal-sized parts, splits a shorter last part, and overrides a re-uploaded part's prefix", func() {
		var m meta.Manifest
		p1 := meta.NewPartManifest(partTarget, partRule, meta.PlacementRule{}, "k.2~id", 1, 4<<20)
		p1.SetObjSize(5 << 20)
		p2 := meta.NewPartManifest(partTarget, partRule, meta.PlacementRule{}, "k.RANDOMRANDOMRANDOMRANDOMRANDOM12", 2, 4<<20)
		p2.SetObjSize(5 << 20)
		p3 := meta.NewPartManifest(partTarget, partRule, meta.PlacementRule{}, "k.2~id", 3, 4<<20)
		p3.SetObjSize(1 << 20)
		Expect(m.Append(p1)).To(Succeed())
		Expect(m.Append(p2)).To(Succeed())
		Expect(m.Append(p3)).To(Succeed())
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{
			0:        {StartPartNum: 1, StartOfs: 0, PartSize: 5 << 20, StripeMaxSize: 4 << 20},
			5 << 20:  {StartPartNum: 2, StartOfs: 5 << 20, PartSize: 5 << 20, StripeMaxSize: 4 << 20, OverridePrefix: "k.RANDOMRANDOMRANDOMRANDOMRANDOM12"},
			10 << 20: {StartPartNum: 3, StartOfs: 10 << 20, PartSize: 1 << 20, StripeMaxSize: 4 << 20},
		}))
		Expect(m.ObjSize).To(BeEquivalentTo(11 << 20))
		Expect(m.Prefix).To(Equal("k.2~id"))
		stripes := stripesOf(m)
		Expect(stripes[2].OID()).To(Equal("m1__multipart_k.RANDOMRANDOMRANDOMRANDOMRANDOM12.2"), "the override prefix names part 2's objects")
		Expect(p1.Rules[0].PartSize).To(BeZero(), "the first part's rules are copied, not shared")
	})
	It("starts a new rule where a part number is skipped", func() {
		var m meta.Manifest
		for _, n := range []uint32{1, 3} {
			p := meta.NewPartManifest(partTarget, partRule, meta.PlacementRule{}, "k.2~id", n, 4<<20)
			p.SetObjSize(4 << 20)
			Expect(m.Append(p)).To(Succeed())
		}
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{
			0:       {StartPartNum: 1, PartSize: 4 << 20, StripeMaxSize: 4 << 20},
			4 << 20: {StartPartNum: 3, StartOfs: 4 << 20, PartSize: 4 << 20, StripeMaxSize: 4 << 20},
		}))
	})
	It("starts a new rule where the stripe size changes", func() {
		var m meta.Manifest
		for i, stripe := range []uint64{4 << 20, 2 << 20} {
			p := meta.NewPartManifest(partTarget, partRule, meta.PlacementRule{}, "k.2~id", uint32(i+1), stripe)
			p.SetObjSize(4 << 20)
			Expect(m.Append(p)).To(Succeed())
		}
		Expect(m.Rules).To(HaveLen(2))
		Expect(m.Rules[4<<20]).To(Equal(meta.ManifestRule{StartPartNum: 2, StartOfs: 4 << 20, PartSize: 4 << 20, StripeMaxSize: 2 << 20}))
	})
	It("takes the part's prefix when the manifest has rules but no prefix", func() {
		m := meta.Manifest{Rules: map[uint64]meta.ManifestRule{0: {StartPartNum: 1, PartSize: 4, StripeMaxSize: 4}}, ObjSize: 4}
		p := meta.NewPartManifest(partTarget, partRule, meta.PlacementRule{}, "k.2~id", 2, 4)
		p.SetObjSize(4)
		Expect(m.Append(p)).To(Succeed())
		Expect(m.Prefix).To(Equal("k.2~id"))
		Expect(m.Rules).To(HaveLen(1), "the prefixes then agree and the part is absorbed")
	})
	DescribeTable("refuses what append_explicit would convert, leaving the manifest as it was",
		func(build func() meta.Manifest, part meta.Manifest) {
			m := build()
			Expect(m.Append(part)).To(MatchError(meta.ErrExplicitManifest))
			Expect(m).To(Equal(build()))
		},
		Entry("an explicit part", func() meta.Manifest { return meta.Manifest{} }, meta.Manifest{ExplicitObjs: true}),
		Entry("an explicit manifest",
			func() meta.Manifest { return meta.Manifest{ExplicitObjs: true} },
			meta.NewPartManifest(partTarget, partRule, partRule, "p", 1, 4)),
		Entry("a part without rules after the first",
			func() meta.Manifest { return meta.NewPartManifest(partTarget, partRule, partRule, "p", 1, 4) },
			meta.Manifest{Prefix: "p"}),
		Entry("a part without rules after a first without a prefix",
			func() meta.Manifest {
				return meta.Manifest{Rules: map[uint64]meta.ManifestRule{0: {StartPartNum: 1, StripeMaxSize: 4}}, ObjSize: 4}
			},
			meta.Manifest{Prefix: "p"}),
	)
})
