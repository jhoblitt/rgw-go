package radosclient_test

import (
	"encoding/base64"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The rjenkins vectors were printed by ceph_str_hash_rjenkins compiled from
// src/common/ceph_hash.cc:22-78, identical at v19.2.6 and v20.2.4.
var _ = Describe("list tokens", func() {
	DescribeTable("RJenkins matches ceph_str_hash_rjenkins",
		func(in string, want uint32) { Expect(radosclient.RJenkins([]byte(in))).To(Equal(want)) },
		Entry("empty", "", uint32(3175731469)),
		Entry("a", "a", uint32(703514648)),
		Entry("plain", "plain", uint32(2467236041)),
		Entry("alice", "alice", uint32(1882812382)),
		Entry("tenant$bob", "tenant$bob", uint32(3423182699)),
		Entry("instance oid", ".bucket.meta.plain:zone.4155.1", uint32(3442924146)),
		Entry("11 bytes", "0123456789a", uint32(2430042782)),
		Entry("12 bytes", "0123456789ab", uint32(2465405648)),
		Entry("13 bytes", "0123456789abc", uint32(2294398249)),
		Entry("26 bytes", "0123456789abcdefghijklmnop", uint32(3493940311)),
		Entry("bytes past 0x7f, which the C reads unsigned", "\xff\xfe\x80", uint32(239340333)),
		Entry("users.uid", "users.uid", uint32(3965031260)),
		Entry("admin", "admin", uint32(2364429183)),
		Entry("a namespaced key", "ns\x1fplain", uint32(3667301547)),
	)

	It("hashes a namespaced object as pg_pool_t::hash_key does", func() {
		Expect(radosclient.PlacementHash("ns", "plain")).To(Equal(uint32(3667301547)))
		Expect(radosclient.PlacementHash("", "plain")).To(Equal(radosclient.RJenkins([]byte("plain"))))
	})

	It("round-trips tokens and rejects foreign ones", func() {
		tok := radosclient.EncodeListToken("tenant/plain")
		Expect(tok).NotTo(ContainSubstring("/"))
		got, err := radosclient.DecodeListToken(tok)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("tenant/plain"))
		_, err = radosclient.DecodeListToken("3:b55a9110:root::bu_9:head")
		Expect(err).To(MatchError(radosclient.ErrBadOp), "a librados cursor string")
		_, err = radosclient.DecodeListToken(base64.RawURLEncoding.EncodeToString([]byte("2:x")))
		Expect(err).To(MatchError(radosclient.ErrBadOp), "another token version")
		_, err = radosclient.DecodeListToken(base64.RawURLEncoding.EncodeToString([]byte("1:")))
		Expect(err).To(MatchError(radosclient.ErrBadOp), "no object, which no listing delivers")
		_, err = radosclient.DecodeListToken("")
		Expect(err).To(MatchError(radosclient.ErrBadOp))
	})

	It("orders entries as hobject_t does: by bit-reversed hash, then by name", func() {
		Expect(radosclient.ListAfter(5, "b", 5, "a")).To(BeTrue())
		Expect(radosclient.ListAfter(5, "a", 5, "a")).To(BeFalse())
		Expect(radosclient.ListAfter(5, "0", 5, "a")).To(BeFalse())
		Expect(radosclient.ListAfter(3, "0", 5, "a")).To(BeTrue(), "3 reverses to 0xc0000000, past 5's 0xa0000000")
		Expect(radosclient.ListAfter(6, "z", 5, "a")).To(BeFalse(), "6 reverses to 0x60000000, before 5's 0xa0000000")
		Expect(radosclient.ListAfter(1, "a", 0x80000000, "z")).To(BeTrue(), "the low bit is the most significant")
		Expect(radosclient.ListAfter(0x80000000, "z", 1, "a")).To(BeFalse())
	})

	It("delivers exactly what follows the resume point when a listing restarts at the start of its placement group", func() {
		// rados_nobjects_list_seek on a fresh listing restarts at the start
		// of the hash's placement group; with one group that is the pool's
		// first object. users.uid's objects in hobject order:
		listing := []string{"dave", "alice", "alice.buckets", "bob", "carol", "erin"}
		for i, last := range listing {
			lastHash := radosclient.PlacementHash("users.uid", last)
			rest := []string{}
			for _, oid := range listing {
				if radosclient.ListAfter(radosclient.PlacementHash("users.uid", oid), oid, lastHash, last) {
					rest = append(rest, oid)
				}
			}
			Expect(rest).To(Equal(listing[i+1:]), "resuming after %s", last)
		}
	})
})
