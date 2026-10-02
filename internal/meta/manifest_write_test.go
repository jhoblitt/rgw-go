package meta_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var (
	putBucket        = meta.BucketID{Name: "plain", Marker: "m1", ID: "m1"}
	putHead          = meta.Obj{Bucket: putBucket, Key: meta.ObjKey{Name: "k"}}
	defaultPlacement = meta.PlacementRule{Name: "default-placement"}
	coldPlacement    = meta.PlacementRule{Name: "p", StorageClass: "COLD"}
)

var _ = Describe("NewTrivialManifest", func() {
	const prefix = ".RANDOMRANDOMRANDOMRANDOMRANDOM1_"
	build := func(size, maxHead uint64) meta.Manifest {
		m := meta.NewTrivialManifest(putHead, defaultPlacement, defaultPlacement, prefix, maxHead, 4<<20)
		m.SetObjSize(size)
		return m
	}

	It("lays a 10 MiB object out as head plus two tails, as Stripes reads it back", func() {
		m := build(10<<20, 4<<20)
		Expect(m.HeadSize).To(BeEquivalentTo(4 << 20))
		Expect(m.MaxHeadSize).To(BeEquivalentTo(4 << 20))
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 4 << 20}}),
			"set_trivial_rule: start_ofs is the max head size")
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(HaveLen(3))
		Expect(st[0].InHead).To(BeTrue(), "stripe 0 is the head")
		Expect(st[1].Obj).To(Equal(m.TailObj(1)))
		Expect(st[1].OID()).To(Equal("m1__shadow_" + prefix + "1"))
		Expect(st[2].OID()).To(Equal("m1__shadow_" + prefix + "2"))
		Expect(st[2].Size).To(BeEquivalentTo(2 << 20))
		Expect(m.TailStripe(4 << 20)).To(BeEquivalentTo(1))
		Expect(m.TailStripe(8<<20 + 1)).To(BeEquivalentTo(2))
	})
	It("keeps an object of exactly the head size in its head", func() {
		m := build(4<<20, 4<<20)
		Expect(m.HeadSize).To(BeEquivalentTo(4 << 20))
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(HaveLen(1))
		Expect(m.HasTail()).To(BeFalse())
	})
	It("keeps an object smaller than the head in its head", func() {
		m := build(1<<20, 4<<20)
		Expect(m.HeadSize).To(BeEquivalentTo(1 << 20))
		Expect(m.HasTail()).To(BeFalse())
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(HaveLen(1))
		Expect(st[0].InHead).To(BeTrue(), "stripe 0 is the head")
		Expect(st[0].Size).To(BeEquivalentTo(1 << 20))
	})
	It("keeps an empty object in an empty head, which obj_begin to obj_end does not visit", func() {
		m := build(0, 4<<20)
		Expect(m.HeadSize).To(BeZero())
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(BeEmpty())
	})
	It("numbers stripes from 0 when the head holds no data", func() {
		m := build(5<<20, 0)
		Expect(m.HeadSize).To(BeZero())
		Expect(m.Rules[0].StartOfs).To(BeZero())
		Expect(m.TailStripe(0)).To(BeZero())
		Expect(m.TailStripe(4 << 20)).To(BeEquivalentTo(1))
		Expect(m.TailObj(0).Key.Name).To(Equal(prefix + "0"))
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(HaveLen(2))
		Expect(st[0].OID()).To(Equal("m1__shadow_" + prefix + "0"))
		Expect(st[0].InHead).To(BeFalse(), "an empty head holds no stripe")
	})
	It("inherits the tail rule from the head rule", func() {
		m := meta.NewTrivialManifest(putHead, meta.PlacementRule{Name: "p", StorageClass: "STANDARD"},
			meta.PlacementRule{StorageClass: "COLD"}, ".x_", 0, 4<<20)
		Expect(m.TailPlacement.PlacementRule).To(Equal(meta.PlacementRule{Name: "p", StorageClass: "COLD"}))
		Expect(m.TailPlacement.Bucket).To(Equal(putBucket))
		Expect(m.HeadPlacementRule).To(Equal(meta.PlacementRule{Name: "p", StorageClass: "STANDARD"}))
		Expect(m.TailObj(0).Bucket).To(Equal(putBucket))
	})
	It("names a versioned head's tails with its instance", func() {
		v := meta.Obj{Bucket: putBucket, Key: meta.ObjKey{Name: "k", Instance: "v1"}}
		m := meta.NewTrivialManifest(v, defaultPlacement, defaultPlacement, prefix, 4<<20, 4<<20)
		m.SetObjSize(10 << 20)
		Expect(m.TailInstance).To(Equal("v1"))
		Expect(m.TailObj(1).Key).To(Equal(meta.ObjKey{Name: prefix + "1", NS: meta.NSShadow, Instance: "v1"}))
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st[1].Obj).To(Equal(m.TailObj(1)))
	})
	It("names tails in the head's bucket when the tail bucket is unset, as get_implicit_location does", func() {
		m := build(10<<20, 4<<20)
		m.TailPlacement.Bucket = meta.BucketID{}
		Expect(m.TailObj(1).Bucket).To(Equal(putBucket))
	})
	It("names a stripe by its index as an int, as get_implicit_location prints (int)cur_stripe", func() {
		m := build(10<<20, 4<<20)
		Expect(m.TailObj(1<<32 + 1).Key.Name).To(Equal(prefix + "1"))
		Expect(m.TailObj(1 << 31).Key.Name).To(Equal(prefix + "-2147483648"))
	})
	It("seeks past stripe 2^31 as obj_iterator::seek does, from the stripe number truncated to an int", func() {
		m := build(1<<53+8<<20, 4<<20)
		// The head plus 2^31 stripes of 4 MiB: (ofs - part_ofs) / stripe_max_size is 2^31.
		it, err := m.Seek(1<<53 + 4<<20)
		Expect(err).NotTo(HaveOccurred())
		Expect(it.StripeOfs()).To(Equal(uint64(18437736874459004928)),
			"part_ofs + (int)2^31 * stripe_max_size modulo 2^64, 2^32 stripes short of 2^53 + 4 MiB")
		obj, _, inHead := it.Location()
		Expect(inHead).To(BeFalse(), "past the head")
		Expect(obj).To(Equal(m.TailObj(1<<31+1)), "the name the writer gives stripe 2^31 + 1")
		Expect(obj.Key.Name).To(Equal(prefix + "-2147483647"))
	})
	It("round-trips through the encoder without a tail bucket or tail instance", func() {
		m := build(10<<20, 4<<20)
		Expect(decodeWhole(encodeWith(denc.Squid, m.Encode), meta.DecodeManifest)).To(Equal(m))
	})
	It("rebuilds the plain-PUT manifest radosgw v19.2.6 wrote byte for byte", func() {
		raw, err := os.ReadFile(filepath.Join("testdata", "manifests", "squid-large.bin"))
		Expect(err).NotTo(HaveOccurred())
		want := decodeWhole(raw, meta.DecodeManifest)
		m := meta.NewTrivialManifest(want.Obj, want.HeadPlacementRule, want.TailPlacement.PlacementRule,
			want.Prefix, want.MaxHeadSize, want.Rules[0].StripeMaxSize)
		m.SetObjSize(want.ObjSize)
		Expect(encodeWith(denc.Squid, m.Encode)).To(Equal(raw))
	})
})

var _ = Describe("PlacementRule.InheritFrom", func() {
	DescribeTable("takes from r only what is empty",
		func(p, want meta.PlacementRule) {
			Expect(p.InheritFrom(coldPlacement)).To(Equal(want))
		},
		Entry("an empty rule takes both", meta.PlacementRule{}, coldPlacement),
		Entry("a name keeps itself and takes the storage class",
			meta.PlacementRule{Name: "q"}, meta.PlacementRule{Name: "q", StorageClass: "COLD"}),
		Entry("a full rule keeps both",
			meta.PlacementRule{Name: "q", StorageClass: "HOT"}, meta.PlacementRule{Name: "q", StorageClass: "HOT"}),
	)
})
