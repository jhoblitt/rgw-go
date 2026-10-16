package meta_test

import (
	"encoding/hex"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// unhex decodes a hex golden written with spaces between its fields.
func unhex(s string) []byte {
	GinkgoHelper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	Expect(err).NotTo(HaveOccurred())
	return b
}

var _ = Describe("OLHInfo", func() {
	// RGWOLHInfo v1: ENCODE_START(1, 1), the target as rgw_obj v6 (its
	// rgw_bucket v10, then ns, name and instance), then removed.
	It("encodes RGWOLHInfo v1 field by field on both releases", func() {
		info := meta.OLHInfo{
			Target: meta.Obj{
				Bucket: meta.BucketID{Name: "b", Marker: "m", ID: "id"},
				Key:    meta.ObjKey{Name: "k", Instance: "i"},
			},
			Removed: true,
		}
		want := unhex("0101 30000000" +
			" 0606 29000000" +
			" 0a0a 15000000 01000000 62 01000000 6d 02000000 6964 00000000 00" +
			" 00000000 01000000 6b 01000000 69" +
			" 01")
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			Expect(encodeWith(r, info.Encode)).To(Equal(want), "release %s", r)
		}
		Expect(decodeWhole(want, meta.DecodeOLHInfo)).To(Equal(info))
	})

	It("refuses a compat newer than DECODE_START(1) accepts", func() {
		Expect(decodeErr(unhex("0202 01000000 00"), meta.DecodeOLHInfo)).To(MatchError(denc.ErrIncompatible))
	})
})

var _ = Describe("OLHPendingInfo", func() {
	// RGWOLHPendingInfo v1: ENCODE_START(1, 1), then the real_time as u32
	// seconds and u32 nanoseconds.
	It("encodes RGWOLHPendingInfo v1 on both releases", func() {
		p := meta.OLHPendingInfo{Time: time.Unix(0x66f924fd, 0x0d0051de).UTC()}
		want := unhex("0101 08000000 fd24f966 de51000d")
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			Expect(encodeWith(r, p.Encode)).To(Equal(want), "release %s", r)
		}
		Expect(decodeWhole(want, meta.DecodeOLHPendingInfo)).To(Equal(p))
	})

	It("refuses a compat newer than DECODE_START(1) accepts", func() {
		Expect(decodeErr(unhex("0202 08000000 0000000000000000"), meta.DecodeOLHPendingInfo)).To(MatchError(denc.ErrIncompatible))
	})
})

// The tables of gen_rand_alphanumeric_no_underscore and
// gen_rand_alphanumeric_lower.
const (
	noUnderscoreTable = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-."
	lowerTable        = "0123456789abcdefghijklmnopqrstuvwxyz"
)

// drawn returns the distinct bytes of n draws of gen, sorted.
func drawn(n int, gen func() string) string {
	var seen []byte
	for range n {
		for _, c := range []byte(gen()) {
			if !slices.Contains(seen, c) {
				seen = append(seen, c)
			}
		}
	}
	slices.Sort(seen)
	return string(seen)
}

// sortedTable returns table's bytes sorted.
func sortedTable(table string) string {
	b := []byte(table)
	slices.Sort(b)
	return string(b)
}

var _ = Describe("version names", func() {
	It("makes an instance of 31 characters with no underscore, as the NUL takes the 32nd byte", func() {
		for range 64 {
			Expect(meta.NewInstance()).To(MatchRegexp(`^[A-Za-z0-9.-]{31}$`))
		}
	})
	It("draws an instance from every character of radosgw's no-underscore table", func() {
		Expect(drawn(256, meta.NewInstance)).To(Equal(sortedTable(noUnderscoreTable)))
	})

	It("makes an OLH tag of 32 lower alphanumerics", func() {
		for range 64 {
			Expect(meta.NewOLHTag()).To(MatchRegexp(`^[0-9a-z]{32}$`))
		}
	})
	It("draws an OLH tag from every lower alphanumeric", func() {
		Expect(drawn(64, meta.NewOLHTag)).To(Equal(sortedTable(lowerTable)))
	})

	DescribeTable("makes an op tag of the second in 16 hex digits, then 16 lower alphanumerics",
		func(now time.Time, prefix string) {
			tag := meta.NewOLHOpTag(now)
			Expect(tag).To(HaveLen(32))
			Expect(tag[:16]).To(Equal(prefix))
			Expect(tag[16:]).To(MatchRegexp(`^[0-9a-z]{16}$`))
		},
		Entry("a second of 2024", time.Unix(0x66f924fd, 999_999_999), "0000000066f924fd"),
		Entry("a second past 2^32, which utime_t keeps in 32 bits", time.Unix(1<<32+0x10, 0), "0000000000000010"),
	)
	It("draws an op tag's suffix from every lower alphanumeric", func() {
		now := time.Unix(1, 0)
		Expect(drawn(128, func() string { return meta.NewOLHOpTag(now)[16:] })).To(Equal(sortedTable(lowerTable)))
	})

	It("names pending modifications so that a later second sorts later", func() {
		// 0xf and 0x10 differ in hex width, so only the zero padding orders them.
		for range 64 {
			earlier := meta.AttrOLHPendingPrefix + meta.NewOLHOpTag(time.Unix(15, 999_999_999))
			later := meta.AttrOLHPendingPrefix + meta.NewOLHOpTag(time.Unix(16, 0))
			Expect(slices.Sorted(slices.Values([]string{later, earlier}))).To(Equal([]string{earlier, later}))
		}
	})
	It("prefixes pending modifications with RGW_ATTR_OLH_PENDING_PREFIX", func() {
		Expect(meta.AttrOLHPendingPrefix).To(Equal("user.rgw.olh.pending."))
	})
})
