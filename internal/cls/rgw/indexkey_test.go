package rgw_test

import (
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The expected keys below are the output of cls_rgw.cc's key helpers and
// strtol.cc's strict_strtoll, copied verbatim from v19.2.7 and from v20.2.4
// into a harness built with g++; both releases print the same bytes.

var _ = Describe("decreasing_str", func() {
	DescribeTable("prints each magnitude band as the class does",
		func(num uint64, want string) {
			Expect(rgw.DecreasingStr(num)).To(Equal(want), "%d", num)
		},
		Entry("band 9 starts at 0", uint64(0), "915"),
		Entry("band 9 counts down", uint64(1), "914"),
		Entry("band 9 ends at 15", uint64(15), "900"),
		Entry("band 8 starts at 16", uint64(16), "8239"),
		Entry("band 8 ends at 255", uint64(255), "8000"),
		Entry("band 7 starts at 256", uint64(256), "73839"),
		Entry("band 7 ends at 4095", uint64(4095), "70000"),
		Entry("band 6 starts at 4096", uint64(4096), "661439"),
		Entry("band 6 ends at 65535", uint64(65535), "600000"),
		Entry("band 5 starts at 65536", uint64(65536), "54294901759"),
		Entry("band 5 ends at 2^32-1", uint64(1<<32-1), "50000000000"),
		Entry("band 4 prints -num as a negative long long from 2^32", uint64(1<<32), "4-0000000004294967296"),
		Entry("band 4 is still negative at 2^63-1", uint64(1<<63-1), "4-9223372036854775807"),
		Entry("band 4 is negative at 2^63, the long long minimum", uint64(1<<63), "4-9223372036854775808"),
		Entry("band 4 turns positive above 2^63", uint64(1<<63+1), "409223372036854775807"),
		Entry("band 4 ends at 2^64-1", uint64(1<<64-1), "400000000000000000001"),
	)

	It("decreases as the epoch grows below 2^32", func() {
		nums := []uint64{0, 1, 15, 16, 200, 255, 256, 4095, 4096, 65535, 65536, 1 << 31, 1<<32 - 1}
		strs := make([]string, len(nums))
		for i, n := range nums {
			strs[i] = rgw.DecreasingStr(n)
		}
		want := slices.Clone(strs)
		slices.Sort(want)
		slices.Reverse(want)
		Expect(strs).To(Equal(want))
		Expect(slices.Compact(want)).To(HaveLen(len(strs)), "distinct strings %q", strs)
	})

	It("grows with the epoch inside band 4, as the class's does", func() {
		lo, hi := rgw.DecreasingStr(1<<32), rgw.DecreasingStr(1<<32+1)
		Expect(strings.Compare(hi, lo)).To(Equal(1), "%q sorts after %q", hi, lo)
	})
})

var _ = Describe("bucket index keys", func() {
	It("keys an instance under 0x80 1000_ with its name and instance after a NUL i", func() {
		Expect(rgw.InstanceIndexKey(rgw.ObjKey{Name: "obj", Instance: "inst"}, false)).To(Equal("\x801000_obj\x00iinst"))
	})

	It("keys the null instance's data under the same prefix with an empty instance", func() {
		Expect(rgw.InstanceIndexKey(rgw.ObjKey{Name: "obj"}, false)).To(Equal("\x801000_obj\x00i"))
	})

	It("appends a NUL d for the null instance's delete marker", func() {
		Expect(rgw.InstanceIndexKey(rgw.ObjKey{Name: "obj"}, true)).To(Equal("\x801000_obj\x00i\x00d"))
	})

	It("keys the OLH data under 0x80 1001_ with the name alone", func() {
		Expect(rgw.OLHDataKey("obj")).To(Equal("\x801001_obj"))
	})

	It("keys a listed version by name, NUL v, the decreasing epoch, NUL i and the instance", func() {
		Expect(rgw.ListIndexKey(rgw.ObjKey{Name: "obj", Instance: "inst"}, 2)).To(Equal("obj\x00v913\x00iinst"))
	})

	It("keeps the empty instance of a listed null version", func() {
		Expect(rgw.ListIndexKey(rgw.ObjKey{Name: "obj"}, 0)).To(Equal("obj\x00v915\x00i"))
	})

	It("writes band 4's minus sign into the list key", func() {
		Expect(rgw.ListIndexKey(rgw.ObjKey{Name: "obj", Instance: "inst"}, 1<<32)).
			To(Equal("obj\x00v4-0000000004294967296\x00iinst"))
	})

	It("starts after a name's versions with a 0x01 suffix", func() {
		Expect(rgw.AfterVersions("obj")).To(Equal("obj\x01"))
	})

	It("sorts every version of a name between the name and its after-versions key", func() {
		for _, epoch := range []uint64{0, 2, 300, 70000, 1<<32 - 1, 1 << 32, 1<<64 - 1} {
			for _, inst := range []string{"", "a", "\xff\xff"} {
				k := rgw.ListIndexKey(rgw.ObjKey{Name: "obj", Instance: inst}, epoch)
				Expect(strings.Compare(k, "obj")).To(Equal(1), "%q after the name", k)
				Expect(strings.Compare(k, rgw.AfterVersions("obj"))).To(Equal(-1), "%q before the after-versions key", k)
			}
		}
	})
})

var _ = Describe("decode_list_index_key", func() {
	DescribeTable("splits a list key on NUL as the class does",
		func(key string, want rgw.ObjKey) {
			got, err := rgw.DecodeListIndexKey(key)
			Expect(err).NotTo(HaveOccurred(), "%q", key)
			Expect(got).To(Equal(want), "%q", key)
		},
		Entry("a plain key is the name", "obj", rgw.ObjKey{Name: "obj"}),
		Entry("the empty key is the empty name", "", rgw.ObjKey{}),
		Entry("a versioned key", "obj\x00v913\x00iinst", rgw.ObjKey{Name: "obj", Instance: "inst"}),
		Entry("a null version's empty instance", "obj\x00v913\x00i", rgw.ObjKey{Name: "obj"}),
		Entry("an empty segment is skipped", "obj\x00\x00iinst", rgw.ObjKey{Name: "obj", Instance: "inst"}),
		Entry("an unknown segment is skipped", "obj\x00x\x00iinst", rgw.ObjKey{Name: "obj", Instance: "inst"}),
		Entry("the last instance wins", "obj\x00ia\x00ib", rgw.ObjKey{Name: "obj", Instance: "b"}),
		Entry("the version may lead with white space, as strtoll allows", "obj\x00v 12\x00ia", rgw.ObjKey{Name: "obj", Instance: "a"}),
		Entry("the version may carry a plus sign", "obj\x00v+12\x00ia", rgw.ObjKey{Name: "obj", Instance: "a"}),
		Entry("the version may be negative", "obj\x00v-12\x00ia", rgw.ObjKey{Name: "obj", Instance: "a"}),
		Entry("no version segment is needed", "obj\x00ia", rgw.ObjKey{Name: "obj", Instance: "a"}),
		Entry("the name may be empty", "\x00v915\x00ia", rgw.ObjKey{Instance: "a"}),
		Entry("a trailing NUL ends the last segment", "obj\x00v915\x00ia\x00", rgw.ObjKey{Name: "obj", Instance: "a"}),
		Entry("a doubled trailing NUL leaves one empty segment", "obj\x00\x00", rgw.ObjKey{Name: "obj"}),
	)

	DescribeTable("fails where the class answers EIO",
		func(key string) {
			_, err := rgw.DecodeListIndexKey(key)
			Expect(err).To(MatchError(denc.ErrMalformed), "%q", key)
		},
		Entry("nothing follows the name's NUL", "obj\x00"),
		Entry("the key is a lone NUL", "\x00"),
		Entry("the version is empty", "obj\x00v"),
		Entry("the version has trailing bytes", "obj\x00v12x\x00iinst"),
		Entry("the version is band 4's negative print", "obj\x00v4-0000000004294967296\x00iinst"),
		Entry("the version overflows a long long", "obj\x00v499999999999999999999\x00iinst"),
		Entry("the version is hexadecimal", "obj\x00v0x10\x00ia"),
	)

	It("reads back every list key below epoch 2^32 and refuses every one from 2^32 on", func() {
		key := rgw.ObjKey{Name: "obj", Instance: "inst"}
		for _, epoch := range []uint64{0, 15, 16, 255, 256, 65535, 65536, 1<<32 - 1} {
			got, err := rgw.DecodeListIndexKey(rgw.ListIndexKey(key, epoch))
			Expect(err).NotTo(HaveOccurred(), "epoch %d", epoch)
			Expect(got).To(Equal(key), "epoch %d", epoch)
		}
		for _, epoch := range []uint64{1 << 32, 1 << 63, 1<<63 + 1, 1<<64 - 1} {
			_, err := rgw.DecodeListIndexKey(rgw.ListIndexKey(key, epoch))
			Expect(err).To(MatchError(denc.ErrMalformed), "epoch %d", epoch)
		}
	})
})
