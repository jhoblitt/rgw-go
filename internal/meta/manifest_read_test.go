package meta_test

import (
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("Manifest.Seek", func() {
	const marker = "e7bceed5-d2a6-4b0b-b484-cac20e0beb53.4156.1"
	load := func(name string) meta.Manifest {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join("testdata", "manifests", name+".bin"))
		Expect(err).NotTo(HaveOccurred())
		return decodeWhole(b, meta.DecodeManifest)
	}
	oid := func(it *meta.StripeIter) string {
		obj, _, _ := it.Location()
		return meta.Stripe{Obj: obj}.OID()
	}

	Describe("large.bin: a 4 MiB head and two shadow stripes", func() {
		const prefix = ".AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_"
		It("seeks into the head and walks to the end in stripe order", func() {
			m := load("squid-large")
			it, err := m.Seek(0)
			Expect(err).NotTo(HaveOccurred())
			_, _, inHead := it.Location()
			Expect(inHead).To(BeTrue(), "offset 0 is in the head")
			Expect(it.StripeOfs()).To(BeZero())
			Expect(it.StripeSize()).To(BeEquivalentTo(4 << 20))
			Expect(it.LocOfs()).To(BeZero())
			Expect(it.PartID()).To(BeZero())
			Expect(it.Next()).To(Succeed())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + "1"))
			Expect(it.StripeOfs()).To(BeEquivalentTo(4 << 20))
			Expect(it.Next()).To(Succeed())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + "2"))
			Expect(it.StripeSize()).To(BeEquivalentTo(4<<20), "operator++ leaves the last stripe untrimmed")
			Expect(it.Next()).To(Succeed())
			Expect(it.Done()).To(BeTrue(), "at offset %d", it.Ofs())
			Expect(it.Ofs()).To(BeEquivalentTo(10 << 20))
		})
		It("seeks into the middle of a tail stripe", func() {
			m := load("squid-large")
			it, err := m.Seek(5 << 20)
			Expect(err).NotTo(HaveOccurred())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + "1"))
			Expect(it.Ofs()).To(BeEquivalentTo(5 << 20))
			Expect(it.StripeOfs()).To(BeEquivalentTo(4 << 20))
			Expect(it.StripeSize()).To(BeEquivalentTo(4 << 20))
		})
		It("trims the last stripe to the object when a seek lands in it", func() {
			m := load("squid-large")
			it, err := m.Seek(8 << 20)
			Expect(err).NotTo(HaveOccurred())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + "2"))
			Expect(it.StripeOfs()).To(BeEquivalentTo(8 << 20))
			Expect(it.StripeSize()).To(BeEquivalentTo(2 << 20))
		})
		It("clamps a seek past the end to obj_end, the iterator the golden dumps as end_iter", func() {
			m := load("squid-large")
			it, err := m.Seek(11 << 20)
			Expect(err).NotTo(HaveOccurred())
			Expect(it.Done()).To(BeTrue(), "at offset %d", it.Ofs())
			Expect(it.Ofs()).To(BeEquivalentTo(10 << 20))
			Expect(it.StripeOfs()).To(BeEquivalentTo(8 << 20))
			Expect(it.StripeSize()).To(BeEquivalentTo(2 << 20))
		})
		It("has a tail and no parts", func() {
			m := load("squid-large")
			Expect(m.HasTail()).To(BeTrue())
			Expect(m.PartsCount()).To(BeZero())
			_, _, _, ok, err := m.PartBounds(1)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})
	})

	Describe("multipart.bin: three parts of 8, 8 and 4 MiB across two rules", func() {
		const prefix = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO"
		It("counts the parts as radosgw's end iterator does (cur_part_id 4 in the golden dump)", func() {
			m := load("squid-multipart")
			Expect(m.PartsCount()).To(Equal(3))
			Expect(m.HasTail()).To(BeTrue())
		})
		DescribeTable("finds each part's bounds and head object",
			func(n int, ofs, size uint64, head string) {
				m := load("squid-multipart")
				gotOfs, gotSize, obj, ok, err := m.PartBounds(n)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue(), "part %d", n)
				Expect(gotOfs).To(Equal(ofs))
				Expect(gotSize).To(Equal(size))
				Expect(meta.Stripe{Obj: obj}.OID()).To(Equal(marker + "__multipart_" + head))
				Expect(obj.Key.NS).To(Equal(meta.NSMultipart))
			},
			Entry("part 1", 1, uint64(0), uint64(8<<20), prefix+".1"),
			Entry("part 2", 2, uint64(8<<20), uint64(8<<20), prefix+".2"),
			Entry("part 3", 3, uint64(16<<20), uint64(4<<20), prefix+".3"),
		)
		It("reports no part 4 and no part 0", func() {
			m := load("squid-multipart")
			for _, n := range []int{0, 4} {
				_, _, _, ok, err := m.PartBounds(n)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeFalse(), "part %d", n)
			}
		})
		It("seeks into part 2's second stripe and carries the part id", func() {
			m := load("squid-multipart")
			it, err := m.Seek(13 << 20)
			Expect(err).NotTo(HaveOccurred())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + ".2_1"))
			Expect(it.PartID()).To(BeEquivalentTo(2))
			Expect(it.StripeOfs()).To(BeEquivalentTo(12 << 20))
			Expect(it.Next()).To(Succeed())
			Expect(it.PartID()).To(BeEquivalentTo(3))
			Expect(oid(it)).To(Equal(marker + "__multipart_" + prefix + ".3"))
		})
	})

	Describe("a single-part upload", func() {
		// RGWObjManifest::append copies the first part's manifest whole, so a
		// one-part upload keeps the part's own rule: part 1 with no part size,
		// which leaves obj_end at part 1 rather than one past the last part.
		build := func() meta.Manifest {
			m := meta.NewManifest()
			m.Obj = headObj
			m.ObjSize = 10
			m.Prefix = "p"
			m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1, StripeMaxSize: 4}}
			return m
		}
		It("counts one part, as get_part_obj_state's max(1, last_part_id - 1) does", func() {
			Expect(build().PartsCount()).To(Equal(1))
		})
		It("bounds part 1 by the whole object", func() {
			ofs, size, head, ok, err := build().PartBounds(1)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect([]uint64{ofs, size}).To(Equal([]uint64{0, 10}))
			Expect(head.Key).To(Equal(meta.ObjKey{Name: "p.1", NS: meta.NSMultipart}))
		})
	})

	// A rule with a stripe size of 0 never moves operator++ on.
	DescribeTable("refuses as malformed a part walk whose stripes never advance",
		func(headSize uint64, n int) {
			m := meta.NewManifest()
			m.Obj = headObj
			m.ObjSize = 10
			m.HeadSize, m.MaxHeadSize = headSize, headSize
			m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1, PartSize: 10}}
			_, _, _, _, err := m.PartBounds(n)
			Expect(err).To(MatchError(denc.ErrMalformed))
		},
		Entry("part 2, which obj_find_part loops forever looking for", uint64(0), 2),
		Entry("part 1, whose zero stripe size get_part_obj_state's create_next divides by", uint64(0), 1),
		Entry("part 1 starting in a head, for which radosgw's HEAD answers and its GET never ends in iterate_obj",
			uint64(4), 1),
	)

	Describe("the stripe bound", func() {
		const limit = 8
		// Part 1 of part1 one-byte stripes, then a one-byte part 2 under a rule
		// of 1 MiB stripes, which keeps the up-front estimate at one stripe.
		build := func(part1 uint64) meta.Manifest {
			m := meta.NewManifest()
			m.Obj = headObj
			m.ObjSize = part1 + 1
			m.Prefix = "p"
			m.Rules = map[uint64]meta.ManifestRule{
				0:     {StartPartNum: 1, PartSize: part1, StripeMaxSize: 1},
				part1: {StartPartNum: 2, StartOfs: part1, PartSize: 1 << 20, StripeMaxSize: 1 << 20},
			}
			return m
		}
		// One rule of one-byte stripes over the whole object, all part 1.
		ones := func(size uint64) meta.Manifest {
			m := meta.NewManifest()
			m.Obj = headObj
			m.ObjSize = size
			m.Prefix = "p"
			m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1, PartSize: size, StripeMaxSize: 1}}
			return m
		}
		It("walks a manifest of exactly the bound's stripes", func() {
			ofs, size, _, ok, err := build(limit-1).PartBoundsUpTo(2, limit)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect([]uint64{ofs, size}).To(Equal([]uint64{limit - 1, 1}))
		})
		It("refuses a walk onto the stripe past the bound", func() {
			_, _, _, _, err := build(limit).PartBoundsUpTo(2, limit)
			Expect(err).To(MatchError(meta.ErrTooManyStripes))
			Expect(err).To(MatchError(ContainSubstring("more than 8 before offset 8")))
		})
		It("refuses at once a manifest whose tail needs more stripes than the bound", func() {
			_, _, _, _, err := ones(limit+1).PartBoundsUpTo(1, limit)
			Expect(err).To(MatchError(ContainSubstring("at least 9, more than 8")))
		})
		It("bounds PartBounds by MaxWalkStripes and Stripes by MaxStripes", func() {
			hostile := ones(meta.MaxWalkStripes + 1)
			_, _, _, _, err := hostile.PartBounds(1)
			Expect(err).To(MatchError(meta.ErrTooManyStripes))
			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("more than %d", meta.MaxWalkStripes))))
			_, err = hostile.Stripes()
			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("more than %d", meta.MaxStripes))))
		})
	})

	Describe("an explicit manifest", func() {
		// Two pieces: the head holds 1 MiB, a shadow piece holds the next 1 MiB from loc_ofs 512.
		build := func() meta.Manifest {
			head := meta.Obj{Bucket: meta.BucketID{Name: "plain", Marker: "m1"}, Key: meta.ObjKey{Name: "k"}}
			shadow := meta.Obj{Bucket: head.Bucket, Key: meta.ObjKey{Name: "s1", NS: meta.NSShadow}}
			m := meta.NewManifest()
			m.ExplicitObjs = true
			m.Obj = head
			m.ObjSize = 2 << 20
			m.HeadSize = 1 << 20
			m.MaxHeadSize = 1 << 20
			m.Objs = map[uint64]meta.ManifestPart{
				0:       {Loc: head, Size: 1 << 20},
				1 << 20: {Loc: shadow, LocOfs: 512, Size: 1 << 20},
			}
			return m
		}
		It("reports the piece's loc_ofs and size and has a tail", func() {
			m := build()
			Expect(m.HasTail()).To(BeTrue())
			it, err := m.Seek(1<<20 + 100)
			Expect(err).NotTo(HaveOccurred())
			Expect(it.StripeOfs()).To(BeEquivalentTo(1 << 20))
			Expect(it.StripeSize()).To(BeEquivalentTo(1 << 20))
			Expect(it.LocOfs()).To(BeEquivalentTo(512))
			obj, _, inHead := it.Location()
			Expect(inHead).To(BeFalse(), "the shadow piece is not the head")
			Expect(obj.Key.Name).To(Equal("s1"))
			Expect(it.Next()).To(Succeed())
			Expect(it.Done()).To(BeTrue(), "at offset %d", it.Ofs())
		})
		It("reports an empty stripe at the end once Next has passed the last piece, and stays there", func() {
			m := build()
			it, err := m.Seek(1 << 20)
			Expect(err).NotTo(HaveOccurred())
			Expect(it.Next()).To(Succeed())
			Expect(it.Next()).To(Succeed(), "operator++ past the end does not move")
			Expect(it.Done()).To(BeTrue(), "at offset %d", it.Ofs())
			Expect([]uint64{it.Ofs(), it.StripeOfs(), it.StripeSize(), it.LocOfs()}).
				To(Equal([]uint64{2 << 20, 2 << 20, 0, 0}))
		})
		It("rests on the last piece after a seek to the end, as obj_end leaves explicit_iter, until Next passes it", func() {
			m := build()
			it, err := m.Seek(m.ObjSize)
			Expect(err).NotTo(HaveOccurred())
			Expect(it.Done()).To(BeTrue(), "at offset %d", it.Ofs())
			Expect([]uint64{it.Ofs(), it.StripeOfs(), it.StripeSize(), it.LocOfs()}).
				To(Equal([]uint64{2 << 20, 1 << 20, 1 << 20, 512}))
			obj, _, _ := it.Location()
			Expect(obj.Key.Name).To(Equal("s1"))
			Expect(it.Next()).To(Succeed())
			Expect([]uint64{it.Ofs(), it.StripeOfs(), it.StripeSize(), it.LocOfs()}).
				To(Equal([]uint64{2 << 20, 2 << 20, 0, 0}))
		})
		It("has no parts", func() {
			m := build()
			Expect(m.PartsCount()).To(BeZero())
			_, _, _, ok, err := m.PartBounds(1)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})
		DescribeTable("with a single piece has a tail only when the piece is not the head",
			func(key meta.ObjKey, want bool) {
				m := build()
				delete(m.Objs, 1<<20)
				m.ObjSize = 1 << 20
				m.Objs[0] = meta.ManifestPart{Loc: meta.Obj{Bucket: m.Obj.Bucket, Key: key}, Size: 1 << 20}
				Expect(m.HasTail()).To(Equal(want))
			},
			Entry("the head", meta.ObjKey{Name: "k"}, false),
			Entry("the head's name in another namespace, which rgw_obj's == ignores",
				meta.ObjKey{Name: "k", NS: meta.NSShadow}, false),
			Entry("another object", meta.ObjKey{Name: "s1", NS: meta.NSShadow}, true),
		)
	})

	It("a head-only manifest without rules has no tail, one stripe, and a Next that stays put", func() {
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: meta.BucketID{Name: "plain", Marker: "m1"}, Key: meta.ObjKey{Name: "k"}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = 1024, 1024, 4<<20
		Expect(m.HasTail()).To(BeFalse())
		it, err := m.Seek(0)
		Expect(err).NotTo(HaveOccurred())
		_, _, inHead := it.Location()
		Expect(inHead).To(BeTrue(), "offset 0 is in the head")
		Expect(it.StripeSize()).To(BeEquivalentTo(1024))
		Expect(it.Next()).To(Succeed())
		Expect(it.Ofs()).To(BeZero(), "operator++ does not move without a rule")
		Expect(it.Done()).To(BeFalse())
	})
})
