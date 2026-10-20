package lifecycle_test

import (
	"encoding/hex"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// hexBytes joins hex fragments, ignoring the spaces that group them.
func hexBytes(parts ...string) []byte {
	GinkgoHelper()
	b, err := hex.DecodeString(strings.ReplaceAll(strings.Join(parts, ""), " ", ""))
	Expect(err).NotTo(HaveOccurred())
	return b
}

func encode[T any](v T, enc func(T, *denc.Encoder, denc.Release), r denc.Release) []byte {
	e := denc.NewEncoder()
	enc(v, e, r)
	return e.Bytes()
}

// decodeAll decodes b with dec and requires it to read every byte without
// error.
func decodeAll[T any](b []byte, dec func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := dec(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero(), "bytes left unread")
	return v
}

// The goldens below are derived by hand from radosgw's encoders, which
// internal/lifecycle/encode.go cites at v19.2.7 and v20.2.4: a struct is its
// version, its compat and a u32 length, then its fields; a string is a u32
// length and its bytes; a map is a u32 count, then each key and value in key
// order.
const (
	// Expiration{Days: "30"}: days "30", date "", newer_noncurrent "".
	expDays30 = "04 02 0e000000 02000000 3330 00000000 00000000"
	// Expiration{Days: "7", NewerNoncurrent: "3"}.
	expNoncur = "04 02 0e000000 01000000 37 00000000 01000000 33"
	// Expiration{Days: "2"}.
	expDays2 = "04 02 0d000000 01000000 32 00000000 00000000"
	// Expiration{Days: "1"}.
	expDays1 = "04 02 0d000000 01000000 31 00000000 00000000"
	// Expiration{}.
	expEmpty = "04 02 0c000000 00000000 00000000 00000000"
	// Filter{}: prefix "", an empty RGWObjTags (ENCODE_START(1, 1) around a
	// zero count), flags 0, size_gt "", size_lt "".
	filterEmpty = "04 01 1a000000 00000000 01 01 04000000 00000000 00000000 00000000 00000000"
	// Filter{Prefix: "logs/"}.
	filterLogs = "04 01 1f000000 05000000 6c6f67732f 01 01 04000000 00000000 00000000 00000000 00000000"
	// Transition{Days: "30", StorageClass: "GLACIER"}.
	transGlacier = "01 01 15000000 02000000 3330 00000000 07000000 474c4143494552"
	// Transition{Days: "5", StorageClass: "COLD"}.
	transCold = "01 01 11000000 01000000 35 00000000 04000000 434f4c44"
)

var (
	ruleFull = lifecycle.Rule{
		ID:               "r1",
		Status:           "Enabled",
		Expiration:       lifecycle.Expiration{Days: "30"},
		NoncurExpiration: lifecycle.Expiration{Days: "7", NewerNoncurrent: "3"},
		MPExpiration:     lifecycle.Expiration{Days: "2"},
		Filter:           lifecycle.Filter{Prefix: "logs/"},
		Transitions:      map[string]lifecycle.Transition{"GLACIER": {Days: "30", StorageClass: "GLACIER"}},
	}
	// ruleFullBytes: id "r1", prefix "", status "Enabled", the three
	// expirations, dm_expiration false, the filter, one transition under
	// "GLACIER" and no noncurrent transition. 6+4+11+20+20+19+1+37+42+4 =
	// 164 = 0xa4 bytes after the header.
	ruleFullBytes = []string{
		"06 01 a4000000", "02000000 7231", "00000000", "07000000 456e61626c6564",
		expDays30, expNoncur, expDays2, "00", filterLogs,
		"01000000 07000000 474c4143494552", transGlacier, "00000000",
	}
	ruleDM = lifecycle.Rule{
		ID:                "dm",
		Prefix:            "p/",
		Status:            "Disabled",
		DMExpiration:      true,
		NoncurTransitions: map[string]lifecycle.Transition{"COLD": {Days: "5", StorageClass: "COLD"}},
	}
	// ruleDMBytes: 6+6+12+18+18+18+1+32+4+35 = 150 = 0x96 bytes after the
	// header; dm_expiration true is the byte 01.
	ruleDMBytes = []string{
		"06 01 96000000", "02000000 646d", "02000000 702f", "08000000 44697361626c6564",
		expEmpty, expEmpty, expEmpty, "01", filterEmpty,
		"00000000", "01000000 04000000 434f4c44", transCold,
	}
	// ruleA and ruleB are 5+4+11+19+18+18+1+32+4+4 = 116 = 0x74 bytes after
	// their headers.
	ruleA      = lifecycle.Rule{ID: "a", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"}}
	ruleABytes = []string{
		"06 01 74000000", "01000000 61", "00000000", "07000000 456e61626c6564",
		expDays1, expEmpty, expEmpty, "00", filterEmpty, "00000000", "00000000",
	}
	ruleB      = lifecycle.Rule{ID: "b", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "2"}}
	ruleBBytes = []string{
		"06 01 74000000", "01000000 62", "00000000", "07000000 456e61626c6564",
		expDays2, expEmpty, expEmpty, "00", filterEmpty, "00000000", "00000000",
	}
)

// configBytes is a configuration of rules keyed by their IDs, in the order
// given: ENCODE_START(1, 1), the count, then each key and rule. Each key
// here is one byte, so an entry is 5 bytes of key and its rule.
func configBytes(entries ...[]string) []byte {
	GinkgoHelper()
	body := hexBytes(hex.EncodeToString([]byte{byte(len(entries)), 0, 0, 0}))
	for _, rule := range entries {
		r := hexBytes(rule...)
		id := r[10] // the rule's own one-byte ID, after its header and the ID's length
		body = append(append(body, 1, 0, 0, 0, id), r...)
	}
	n := len(body)
	return append([]byte{1, 1, byte(n), byte(n >> 8), 0, 0}, body...)
}

var _ = Describe("encodings", func() {
	DescribeTable("Expiration writes ENCODE_START(4, 2), days, date and newer_noncurrent",
		func(x lifecycle.Expiration, want string) {
			got := encode(x, lifecycle.Expiration.Encode, denc.Squid)
			Expect(got).To(Equal(hexBytes(want)))
			Expect(encode(x, lifecycle.Expiration.Encode, denc.Tentacle)).To(Equal(got), "both releases write the same bytes")
			Expect(decodeAll(got, lifecycle.DecodeExpiration)).To(Equal(x))
		},
		Entry("days", lifecycle.Expiration{Days: "30"}, expDays30),
		Entry("noncurrent days and the newer count", lifecycle.Expiration{Days: "7", NewerNoncurrent: "3"}, expNoncur),
		Entry("a date, 24 bytes long", lifecycle.Expiration{Date: "2026-01-01T00:00:00.000Z"},
			"04 02 24000000 00000000 18000000 323032362d30312d30315430303a30303a30302e3030305a 00000000"),
		Entry("nothing", lifecycle.Expiration{}, expEmpty),
	)

	DescribeTable("Expiration reads every version DECODE_START_LEGACY_COMPAT_LEN(4, 2, 2) takes",
		func(b string, want lifecycle.Expiration) {
			Expect(decodeAll(hexBytes(b), lifecycle.DecodeExpiration)).To(Equal(want))
		},
		Entry("v1: a version byte alone, then days", "01 02000000 3330", lifecycle.Expiration{Days: "30"}),
		Entry("v2: compat and length, then days", "02 02 06000000 02000000 3330", lifecycle.Expiration{Days: "30"}),
		Entry("v3: the date too", "03 02 14000000 02000000 3330 0a000000 323032362d30312d3031",
			lifecycle.Expiration{Days: "30", Date: "2026-01-01"}),
		Entry("v5 with compat 2: a field it does not know is skipped",
			"05 02 12000000 01000000 31 00000000 00000000 01000000 78", lifecycle.Expiration{Days: "1"}),
	)

	It("refuses an Expiration whose compat is newer than v4", func() {
		d := denc.NewDecoder(hexBytes("05 05 0c000000 00000000 00000000 00000000"))
		lifecycle.DecodeExpiration(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})

	DescribeTable("Transition writes ENCODE_START(1, 1), days, date and storage_class",
		func(t lifecycle.Transition, want string) {
			got := encode(t, lifecycle.Transition.Encode, denc.Squid)
			Expect(got).To(Equal(hexBytes(want)))
			Expect(encode(t, lifecycle.Transition.Encode, denc.Tentacle)).To(Equal(got), "both releases write the same bytes")
			Expect(decodeAll(got, lifecycle.DecodeTransition)).To(Equal(t))
		},
		Entry("days and a class", lifecycle.Transition{Days: "30", StorageClass: "GLACIER"}, transGlacier),
		Entry("a date and no class", lifecycle.Transition{Date: "2026-01-01"},
			"01 01 16000000 00000000 0a000000 323032362d30312d3031 00000000"),
	)

	It("skips what a later Transition version adds", func() {
		Expect(decodeAll(hexBytes("02 01 16000000 01000000 35 00000000 04000000 434f4c44 01000000 78"),
			lifecycle.DecodeTransition)).To(Equal(lifecycle.Transition{Days: "5", StorageClass: "COLD"}))
	})

	It("refuses a Transition whose compat is newer than v1", func() {
		d := denc.NewDecoder(hexBytes("02 02 0c000000 00000000 00000000 00000000"))
		lifecycle.DecodeTransition(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})

	DescribeTable("Filter writes ENCODE_START(4, 1), prefix, tags, flags, size_gt and size_lt",
		func(f lifecycle.Filter, want string) {
			got := encode(f, lifecycle.Filter.Encode, denc.Squid)
			Expect(got).To(Equal(hexBytes(want)))
			Expect(encode(f, lifecycle.Filter.Encode, denc.Tentacle)).To(Equal(got), "both releases write the same bytes")
			Expect(decodeAll(got, lifecycle.DecodeFilter)).To(Equal(f))
		},
		Entry("nothing", lifecycle.Filter{}, filterEmpty),
		Entry("every condition", lifecycle.Filter{
			Prefix: "logs/",
			Tags:   tags.Set{Tags: []tags.Tag{{Key: "k1", Value: "v1"}}},
			Flags:  lifecycle.FlagArchiveZone,
			SizeGT: "100",
			SizeLT: "1000",
		}, "04 01 32000000 05000000 6c6f67732f 01 01 10000000 01000000 02000000 6b31 02000000 7631 01000000 03000000 313030 04000000 31303030"),
	)

	It("writes a Filter's tags in multimap order whatever order they were built in", func() {
		f := lifecycle.Filter{Tags: tags.Set{Tags: []tags.Tag{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}}}}
		Expect(encode(f, lifecycle.Filter.Encode, denc.Squid)).To(Equal(hexBytes(
			"04 01 2e000000 00000000 01 01 18000000 02000000 01000000 61 01000000 31 01000000 62 01000000 32 00000000 00000000 00000000")))
	})

	DescribeTable("Filter reads every version DECODE_START(3) and DECODE_START(4) take",
		func(b string, want lifecycle.Filter) {
			Expect(decodeAll(hexBytes(b), lifecycle.DecodeFilter)).To(Equal(want))
		},
		Entry("v1: the prefix alone", "01 01 09000000 05000000 6c6f67732f", lifecycle.Filter{Prefix: "logs/"}),
		Entry("v2: the tags too", "02 01 0e000000 00000000 01 01 04000000 00000000", lifecycle.Filter{}),
		Entry("v3: the flags too", "03 01 12000000 00000000 01 01 04000000 00000000 01000000",
			lifecycle.Filter{Flags: lifecycle.FlagArchiveZone}),
		Entry("v4 with compat 4, which only v20.2.4's DECODE_START(4) admits, read on both",
			"04 04 1b000000 00000000 01 01 04000000 00000000 00000000 00000000 01000000 39",
			lifecycle.Filter{SizeLT: "9"}),
		Entry("flag bits radosgw names no flag for, kept", "03 01 12000000 00000000 01 01 04000000 00000000 06000000",
			lifecycle.Filter{Flags: 6}),
	)

	It("refuses a Filter whose compat is newer than v4", func() {
		d := denc.NewDecoder(hexBytes("05 05 1a000000 00000000 01 01 04000000 00000000 00000000 00000000 00000000"))
		lifecycle.DecodeFilter(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})

	It("reads a Filter whose tags are stored as text as RGWObjTags::decode falls back to it", func() {
		// A v2 filter whose length runs to the buffer's end over "k=v"
		// rather than a binary tag set: the binary decode of RGWObjTags fails,
		// and it reads every byte left as the URL-encoded text.
		Expect(decodeAll(hexBytes("02 01 07000000 00000000 6b3d76"), lifecycle.DecodeFilter)).To(Equal(
			lifecycle.Filter{Tags: tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}}))
	})

	DescribeTable("Rule writes ENCODE_START(6, 1) and its fields in rgw_lc.h's order",
		func(r lifecycle.Rule, want []string) {
			got := encode(r, lifecycle.Rule.Encode, denc.Squid)
			Expect(got).To(Equal(hexBytes(want...)))
			Expect(encode(r, lifecycle.Rule.Encode, denc.Tentacle)).To(Equal(got), "both releases write the same bytes")
			Expect(decodeAll(got, lifecycle.DecodeRule)).To(Equal(r))
		},
		Entry("every action but the delete marker", ruleFull, ruleFullBytes),
		Entry("the delete marker and a noncurrent transition", ruleDM, ruleDMBytes),
	)

	It("writes a Rule's transitions in key order", func() {
		r := lifecycle.Rule{Transitions: map[string]lifecycle.Transition{
			"COLD": {Days: "5", StorageClass: "COLD"}, "GLACIER": {Days: "30", StorageClass: "GLACIER"},
		}}
		got := encode(r, lifecycle.Rule.Encode, denc.Squid)
		Expect(string(got)).To(ContainSubstring(string(hexBytes("02000000 04000000 434f4c44", transCold, "07000000 474c4143494552", transGlacier))))
	})

	DescribeTable("Rule reads every version DECODE_START_LEGACY_COMPAT_LEN(6, 1, 1) takes",
		func(b []string, want lifecycle.Rule) {
			Expect(decodeAll(hexBytes(b...), lifecycle.DecodeRule)).To(Equal(want))
		},
		Entry("v0: no compat and no length, then id, prefix, status and expiration",
			[]string{"00", "01000000 61", "00000000", "07000000 456e61626c6564", expDays1}, ruleA),
		Entry("v1: compat and length",
			[]string{"01 01 27000000", "01000000 61", "00000000", "07000000 456e61626c6564", expDays1}, ruleA),
		Entry("v2: the noncurrent expiration",
			[]string{"02 01 3b000000", "01000000 61", "00000000", "07000000 456e61626c6564", expDays1, expNoncur},
			lifecycle.Rule{
				ID: "a", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"},
				NoncurExpiration: lifecycle.Expiration{Days: "7", NewerNoncurrent: "3"},
			}),
		Entry("v4: the delete-marker flag, any non-zero byte true",
			[]string{"04 01 4c000000", "01000000 61", "00000000", "07000000 456e61626c6564", expEmpty, expEmpty, expDays2, "02"},
			lifecycle.Rule{ID: "a", Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "2"}, DMExpiration: true}),
		Entry("v5: the filter",
			[]string{"05 01 71000000", "01000000 61", "00000000", "07000000 456e61626c6564", expDays1, expEmpty, expEmpty, "00", filterLogs},
			lifecycle.Rule{ID: "a", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"}, Filter: lifecycle.Filter{Prefix: "logs/"}}),
	)

	It("keeps the last of a Rule's repeated transition keys, as decode(m[k]) does", func() {
		b := hexBytes("06 01 b5000000", "01000000 61", "00000000", "07000000 456e61626c6564",
			expEmpty, expEmpty, expEmpty, "00", filterEmpty,
			"02000000", "04000000 434f4c44", transGlacier, "04000000 434f4c44", transCold, "00000000")
		r := decodeAll(b, lifecycle.DecodeRule)
		Expect(r.Transitions).To(Equal(map[string]lifecycle.Transition{"COLD": {Days: "5", StorageClass: "COLD"}}))
	})

	Describe("Configuration", func() {
		It("writes ENCODE_START(1, 1) and the rules keyed by ID in ID order", func() {
			c := lifecycle.Configuration{Rules: []lifecycle.Rule{ruleB, ruleA}}
			got := encode(c, lifecycle.Configuration.Encode, denc.Squid)
			Expect(got).To(Equal(configBytes(ruleABytes, ruleBBytes)))
			Expect(encode(c, lifecycle.Configuration.Encode, denc.Tentacle)).To(Equal(got), "both releases write the same bytes")
			Expect(decodeAll(got, lifecycle.DecodeConfiguration)).To(Equal(lifecycle.Configuration{Rules: []lifecycle.Rule{ruleA, ruleB}}))
		})

		It("keeps rules with the same ID in the order they were added", func() {
			first, second := ruleA, ruleA
			second.Expiration.Days = "2"
			c := lifecycle.Configuration{Rules: []lifecycle.Rule{first, second}}
			got := encode(c, lifecycle.Configuration.Encode, denc.Squid)
			Expect(decodeAll(got, lifecycle.DecodeConfiguration)).To(Equal(c))
		})

		It("reads rules stored out of key order in multimap order", func() {
			Expect(decodeAll(configBytes(ruleBBytes, ruleABytes), lifecycle.DecodeConfiguration)).To(Equal(
				lifecycle.Configuration{Rules: []lifecycle.Rule{ruleA, ruleB}}))
		})

		It("reads v0, which carries neither compat nor length", func() {
			Expect(decodeAll(hexBytes("00 00000000"), lifecycle.DecodeConfiguration)).To(Equal(lifecycle.Configuration{}))
		})

		It("reads the attr and ignores bytes after it, as GetBucketLifecycle does", func() {
			c, err := lifecycle.Decode(append(configBytes(ruleABytes), 0xff))
			Expect(err).NotTo(HaveOccurred())
			Expect(c).To(Equal(lifecycle.Configuration{Rules: []lifecycle.Rule{ruleA}}))
		})

		It("refuses an attr that does not decode", func() {
			_, err := lifecycle.Decode(configBytes(ruleABytes)[:20])
			Expect(err).To(MatchError(denc.ErrShortBuffer))
		})
	})
})
