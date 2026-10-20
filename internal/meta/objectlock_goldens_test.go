package meta_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// testdata/corpus/<type>/<archive>/<object> holds the object-lock types'
// objects copied verbatim from the ceph-object-corpus. Each type is one
// ENCODE_START(v, 1) struct of fixed fields, at the same version from the
// archives' releases to both floors
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L12-L235 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L11-L230 at v20.2.4),
// so an object's bytes are what either floor's encoder writes for the value
// it holds.

// corpusObjects returns typ's corpus objects by archive.
func corpusObjects(typ string) map[string][][]byte {
	GinkgoHelper()
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", typ, "*", "*"))
	Expect(err).NotTo(HaveOccurred())
	Expect(paths).NotTo(BeEmpty(), "no corpus objects for %s", typ)
	byArchive := map[string][][]byte{}
	for _, p := range paths {
		b, err := os.ReadFile(p) //nolint:gosec // paths come from the test's own testdata
		Expect(err).NotTo(HaveOccurred())
		a := filepath.Base(filepath.Dir(p))
		byArchive[a] = append(byArchive[a], b)
	}
	return byArchive
}

// expectCorpus decodes each of typ's corpus objects whole, re-encodes it on
// both releases to its own bytes, and matches each archive's values with
// want's.
func expectCorpus[T any](typ string, decode func(*denc.Decoder) T, encode func(T, *denc.Encoder, denc.Release), want map[string][]T) {
	GinkgoHelper()
	got := map[string][]any{}
	for archive, objs := range corpusObjects(typ) {
		for _, b := range objs {
			v := decodeWhole(b, decode)
			for _, r := range bothReleases {
				Expect(encodeWith(r, func(e *denc.Encoder, r denc.Release) { encode(v, e, r) })).To(Equal(b), "%s/%s on %v", typ, archive, r)
			}
			got[archive] = append(got[archive], v)
		}
	}
	Expect(got).To(HaveLen(len(want)), "%s's archives", typ)
	for archive, vs := range want {
		Expect(got).To(HaveKeyWithValue(archive, ConsistOf(anySlice(vs))), "%s/%s", typ, archive)
	}
}

func anySlice[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// The archives the object-lock types appear in. 19.2.0-404-g78ddc7f9027
// adds the instances generate_test_instances makes at v19.2.7 and v20.2.4: a
// rule of the default retention, and RGWObjectLock disabled
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L60-L63 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L113-L122 at v19.2.7).
var olderArchives = []string{"15.2.1-40-ga838bb1aae", "16.2.0-90-g50f1821b4c", "18.2.0"}

const squidArchive = "19.2.0-404-g78ddc7f9027"

// byArchive gives every older archive common and the Squid archive common
// and squid.
func byArchive[T any](common, squid []T) map[string][]T {
	m := map[string][]T{squidArchive: append(append([]T{}, common...), squid...)}
	for _, a := range olderArchives {
		m[a] = common
	}
	return m
}

var (
	oneDayGovernance  = meta.DefaultRetention{Mode: "GOVERNANCE", Days: 1}
	oneYearCompliance = meta.DefaultRetention{Mode: "COMPLIANCE", Years: 1}
	encodeDefaultRet  = func(v meta.DefaultRetention, e *denc.Encoder, r denc.Release) { v.Encode(e, r) }
	encodeLockRule    = func(v meta.ObjectLockRule, e *denc.Encoder, r denc.Release) { v.Encode(e, r) }
	encodeObjectLock  = func(v meta.ObjectLock, e *denc.Encoder, r denc.Release) { v.Encode(e, r) }
	encodeRetention   = func(v meta.ObjectRetention, e *denc.Encoder, r denc.Release) { v.Encode(e, r) }
	encodeLegalHold   = func(v meta.ObjectLegalHold, e *denc.Encoder, r denc.Release) { v.Encode(e, r) }
)

var _ = Describe("the object-lock types' corpus objects", func() {
	It("DefaultRetention", func() {
		expectCorpus("DefaultRetention", meta.DecodeDefaultRetention, encodeDefaultRet,
			byArchive([]meta.DefaultRetention{oneDayGovernance, oneYearCompliance}, nil))
	})
	It("ObjectLockRule", func() {
		expectCorpus("ObjectLockRule", meta.DecodeObjectLockRule, encodeLockRule,
			byArchive(
				[]meta.ObjectLockRule{{DefaultRetention: oneDayGovernance}, {DefaultRetention: oneYearCompliance}},
				[]meta.ObjectLockRule{{}}))
	})
	It("RGWObjectLock", func() {
		expectCorpus("RGWObjectLock", meta.DecodeObjectLock, encodeObjectLock,
			byArchive(
				[]meta.ObjectLock{
					{Enabled: true},
					{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: oneDayGovernance}},
					{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: oneYearCompliance}},
				},
				[]meta.ObjectLock{{Enabled: true, Rule: &meta.ObjectLockRule{}}, {}}))
	})
	It("RGWObjectRetention", func() {
		expectCorpus("RGWObjectRetention", meta.DecodeObjectRetention, encodeRetention,
			map[string][]meta.ObjectRetention{squidArchive: {{}}})
	})
	It("RGWObjectLegalHold", func() {
		expectCorpus("RGWObjectLegalHold", meta.DecodeObjectLegalHold, encodeLegalHold,
			map[string][]meta.ObjectLegalHold{squidArchive: {{}}})
	})
})

// expectGolden encodes v on both releases to want and decodes want whole
// to v.
func expectGolden[T any](v T, want []byte, encode func(T, *denc.Encoder, denc.Release), decode func(*denc.Decoder) T) {
	GinkgoHelper()
	for _, r := range bothReleases {
		Expect(encodeWith(r, func(e *denc.Encoder, r denc.Release) { encode(v, e, r) })).To(Equal(want), "on %v", r)
	}
	Expect(decodeWhole(want, decode)).To(Equal(v))
}

// The goldens below are written out by hand from the encode functions at
// both floors, which are identical
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L12-L235 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L11-L230 at v20.2.4).
// ENCODE_START writes the version, the compat version and a u32 length of
// what follows; a string is a u32 length and its bytes; an int is four bytes
// and a bool one, little-endian.
var _ = Describe("the object-lock encodings, written out by hand", func() {
	DescribeTable("DefaultRetention: ENCODE_START(1, 1), mode, days and years as ints",
		func(v meta.DefaultRetention, want []byte) {
			expectGolden(v, want, encodeDefaultRet, meta.DecodeDefaultRetention)
		},
		Entry("GOVERNANCE for 30 days", meta.DefaultRetention{Mode: "GOVERNANCE", Days: 30}, []byte{
			1, 1, 22, 0, 0, 0,
			10, 0, 0, 0, 'G', 'O', 'V', 'E', 'R', 'N', 'A', 'N', 'C', 'E',
			30, 0, 0, 0,
			0, 0, 0, 0,
		}),
		Entry("COMPLIANCE for 7 years", meta.DefaultRetention{Mode: "COMPLIANCE", Years: 7}, []byte{
			1, 1, 22, 0, 0, 0,
			10, 0, 0, 0, 'C', 'O', 'M', 'P', 'L', 'I', 'A', 'N', 'C', 'E',
			0, 0, 0, 0,
			7, 0, 0, 0,
		}),
		Entry("a negative period, the int's two's complement", meta.DefaultRetention{Mode: "GOVERNANCE", Days: -1, Years: 365}, []byte{
			1, 1, 22, 0, 0, 0,
			10, 0, 0, 0, 'G', 'O', 'V', 'E', 'R', 'N', 'A', 'N', 'C', 'E',
			0xff, 0xff, 0xff, 0xff,
			0x6d, 0x01, 0, 0,
		}),
	)
	It("ObjectLockRule: ENCODE_START(1, 1) around its DefaultRetention", func() {
		expectGolden(meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "GOVERNANCE", Days: 30}}, []byte{
			1, 1, 28, 0, 0, 0,
			1, 1, 22, 0, 0, 0,
			10, 0, 0, 0, 'G', 'O', 'V', 'E', 'R', 'N', 'A', 'N', 'C', 'E',
			30, 0, 0, 0,
			0, 0, 0, 0,
		}, encodeLockRule, meta.DecodeObjectLockRule)
	})
	DescribeTable("RGWObjectLock: ENCODE_START(1, 1), enabled, rule_exist, and the rule only when rule_exist",
		func(v meta.ObjectLock, want []byte) {
			expectGolden(v, want, encodeObjectLock, meta.DecodeObjectLock)
		},
		Entry("RGWObjectLock(), enabled with no rule", meta.ObjectLock{Enabled: true}, []byte{
			1, 1, 2, 0, 0, 0,
			1, 0,
		}),
		Entry("disabled with no rule", meta.ObjectLock{}, []byte{
			1, 1, 2, 0, 0, 0,
			0, 0,
		}),
		Entry("enabled with a COMPLIANCE rule of 7 years", meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "COMPLIANCE", Years: 7}}}, []byte{
			1, 1, 36, 0, 0, 0,
			1, 1,
			1, 1, 28, 0, 0, 0,
			1, 1, 22, 0, 0, 0,
			10, 0, 0, 0, 'C', 'O', 'M', 'P', 'L', 'I', 'A', 'N', 'C', 'E',
			0, 0, 0, 0,
			7, 0, 0, 0,
		}),
	)
	DescribeTable("RGWObjectRetention: ENCODE_START(2, 1), mode, the date as encode writes a real_time, then round_trip_encode's u64 nanoseconds",
		func(v meta.ObjectRetention, want []byte) {
			expectGolden(v, want, encodeRetention, meta.DecodeObjectRetention)
		},
		// 2030-01-02T03:04:05Z is 1893553445 seconds, 0x70dd5525; with 6 ns,
		// 1893553445000000006 ns, 0x1a4740d364713206.
		Entry("GOVERNANCE until 2030", meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)}, []byte{
			2, 1, 30, 0, 0, 0,
			10, 0, 0, 0, 'G', 'O', 'V', 'E', 'R', 'N', 'A', 'N', 'C', 'E',
			0x25, 0x55, 0xdd, 0x70, 6, 0, 0, 0,
			0x06, 0x32, 0x71, 0x64, 0xd3, 0x40, 0x47, 0x1a,
		}),
		// 2200-01-02T03:04:05Z is 7258215845 seconds, which the u32 cuts to
		// 2963248549, 0xb09f95a5; the u64 keeps 7258215845000000006,
		// 0x64ba5cdaf8563206.
		Entry("COMPLIANCE until 2200, past the u32's seconds", meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: time.Date(2200, 1, 2, 3, 4, 5, 6, time.UTC)}, []byte{
			2, 1, 30, 0, 0, 0,
			10, 0, 0, 0, 'C', 'O', 'M', 'P', 'L', 'I', 'A', 'N', 'C', 'E',
			0xa5, 0x95, 0x9f, 0xb0, 6, 0, 0, 0,
			0x06, 0x32, 0x56, 0xf8, 0xda, 0x5c, 0xba, 0x64,
		}),
		// real_time's last instant: 18446744073 seconds, cut to 1266874889,
		// 0x4b82fa09, and 709551615 ns, 0x2a4ae5ff; the u64 is all ones.
		Entry("COMPLIANCE until real_time's last instant", meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: meta.MaxRealTime}, []byte{
			2, 1, 30, 0, 0, 0,
			10, 0, 0, 0, 'C', 'O', 'M', 'P', 'L', 'I', 'A', 'N', 'C', 'E',
			0x09, 0xfa, 0x82, 0x4b, 0xff, 0xe5, 0x4a, 0x2a,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		}),
	)
	DescribeTable("RGWObjectLegalHold: ENCODE_START(1, 1) and the status",
		func(v meta.ObjectLegalHold, want []byte) {
			expectGolden(v, want, encodeLegalHold, meta.DecodeObjectLegalHold)
		},
		Entry("ON", meta.ObjectLegalHold{Status: "ON"}, []byte{1, 1, 6, 0, 0, 0, 2, 0, 0, 0, 'O', 'N'}),
		Entry("OFF", meta.ObjectLegalHold{Status: "OFF"}, []byte{1, 1, 7, 0, 0, 0, 3, 0, 0, 0, 'O', 'F', 'F'}),
	)
})

var _ = Describe("a bucket record with an object lock rule", func() {
	It("re-encodes byte for byte through ObjectLock and SetObjectLock on both releases", func() {
		for _, r := range bothReleases {
			b := lockedBucket(nil)
			b.SetObjectLock(meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "COMPLIANCE", Years: 7}}})
			stored := encodeWith(r, b.Encode)
			read := decodeWhole(stored, meta.DecodeBucketInfo)
			l, err := read.ObjectLock()
			Expect(err).NotTo(HaveOccurred())
			read.SetObjectLock(l)
			Expect(encodeWith(r, read.Encode)).To(Equal(stored), "on %v", r)
		}
	})
})
