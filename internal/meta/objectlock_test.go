package meta_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// ceph-dencoder registers neither object-lock type, so the corpus objects are
// inlined: RGWObjectRetention/19.2.0-404-g78ddc7f9027/f241e07a3d03fa3ded89f7e97c3017d9
// and RGWObjectLegalHold/19.2.0-404-g78ddc7f9027/a3ce737f96d8c5380e229c9acf06403b,
// each the type's default.
var (
	corpusRetention = []byte{2, 1, 20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	corpusLegalHold = []byte{1, 1, 4, 0, 0, 0, 0, 0, 0, 0}
)

var _ = Describe("ObjectRetention", func() {
	It("round-trips the corpus default", func() {
		Expect(decodeWhole(corpusRetention, meta.DecodeObjectRetention)).To(Equal(meta.ObjectRetention{}))
		Expect(encodeWith(denc.Squid, meta.ObjectRetention{}.Encode)).To(Equal(corpusRetention))
	})
	It("writes the date as utime_t and again as round_trip_encode's nanosecond count", func() {
		until := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
		o := meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: until}
		want := encoded(func(e *denc.Encoder) {
			beginEnd(e, 2, 1, func() {
				e.String("GOVERNANCE")
				e.Time(until)
				e.U64(uint64(until.UnixNano()))
			})
		})
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			expectOrder(o, want, func(e *denc.Encoder) { o.Encode(e, r) }, meta.DecodeObjectRetention)
		}
	})
	It("takes the date from the nanosecond count, which keeps the seconds utime_t's u32 drops", func() {
		until := time.Date(2200, 1, 2, 3, 4, 5, 6, time.UTC)
		o := meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: until}
		b := encodeWith(denc.Squid, o.Encode)
		Expect(decodeWhole(b, meta.DecodeObjectRetention)).To(Equal(o))
	})
	It("takes the date from the nanosecond count even when it is zero, as round_trip_decode assigns it", func() {
		b := encoded(func(e *denc.Encoder) {
			beginEnd(e, 2, 1, func() {
				e.String("GOVERNANCE")
				e.Time(time.Unix(5, 0))
				e.U64(0)
			})
		})
		Expect(decodeWhole(b, meta.DecodeObjectRetention)).To(Equal(meta.ObjectRetention{Mode: "GOVERNANCE"}))
	})
	It("reads version 1's utime_t alone", func() {
		until := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
		b := encoded(func(e *denc.Encoder) {
			beginEnd(e, 1, 1, func() {
				e.String("GOVERNANCE")
				e.Time(until)
			})
		})
		Expect(decodeWhole(b, meta.DecodeObjectRetention)).To(Equal(meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: until}))
	})
})

var _ = Describe("ObjectLegalHold", func() {
	It("round-trips the corpus default", func() {
		Expect(decodeWhole(corpusLegalHold, meta.DecodeObjectLegalHold)).To(Equal(meta.ObjectLegalHold{}))
		Expect(encodeWith(denc.Squid, meta.ObjectLegalHold{}.Encode)).To(Equal(corpusLegalHold))
	})
	It("writes the status", func() {
		l := meta.ObjectLegalHold{Status: "ON"}
		want := encoded(func(e *denc.Encoder) { beginEnd(e, 1, 1, func() { e.String("ON") }) })
		expectOrder(l, want, func(e *denc.Encoder) { l.Encode(e, denc.Tentacle) }, meta.DecodeObjectLegalHold)
	})
})

// lockBytes is RGWObjectLock::encode of enabled and, when it is non-nil, a
// rule of the default retention r, each struct ENCODE_START(1, 1)
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L127-L135,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L74-L78
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L34-L40 at v19.2.7).
func lockBytes(enabled bool, r *meta.DefaultRetention) []byte {
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 1, 1, func() {
			e.Bool(enabled)
			e.Bool(r != nil)
			if r != nil {
				beginEnd(e, 1, 1, func() {
					beginEnd(e, 1, 1, func() {
						e.String(r.Mode)
						e.I32(r.Days)
						e.I32(r.Years)
					})
				})
			}
		})
	})
}

func governanceDays() meta.DefaultRetention {
	return meta.DefaultRetention{Mode: "GOVERNANCE", Days: 30}
}

func complianceYears() meta.DefaultRetention {
	return meta.DefaultRetention{Mode: "COMPLIANCE", Years: 7}
}

// lockedBucket is a bucket whose flags enable object lock, with raw as its
// ObjLock.
func lockedBucket(raw []byte) meta.BucketInfo {
	b := normalBucket()
	b.Flags |= meta.BucketObjLockEnabled
	b.ObjLock = raw
	return b
}

var _ = Describe("ObjectLock", func() {
	DescribeTable("round-trips byte for byte, through the bucket's ObjLock too",
		func(l meta.ObjectLock, want []byte) {
			for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
				expectOrder(l, want, func(e *denc.Encoder) { l.Encode(e, r) }, meta.DecodeObjectLock)
			}
			b := lockedBucket(want)
			got, err := b.ObjectLock()
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(l))
			b.SetObjectLock(got)
			Expect(b.ObjLock).To(Equal(meta.RawStruct(want)))
		},
		Entry("RGWObjectLock(), enabled with no rule, as ceph-dencoder writes it",
			meta.ObjectLock{Enabled: true}, []byte{1, 1, 2, 0, 0, 0, 1, 0}),
		Entry("a GOVERNANCE rule in days",
			meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: governanceDays()}}, lockBytes(true, new(governanceDays()))),
		Entry("a COMPLIANCE rule in years",
			meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: complianceYears()}}, lockBytes(true, new(complianceYears()))),
	)

	It("skips what a later version appends, and the bucket keeps those bytes until SetObjectLock", func() {
		v1 := lockBytes(true, new(governanceDays()))
		later := reframe(v1, 2, []byte("later"))
		l := decodeWhole(later, meta.DecodeObjectLock)
		Expect(l).To(Equal(meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: governanceDays()}}))
		b := decodeWhole(encodeWith(denc.Squid, lockedBucket(later).Encode), meta.DecodeBucketInfo)
		Expect(b.ObjLock).To(Equal(meta.RawStruct(later)))
		b.SetObjectLock(l)
		Expect(b.ObjLock).To(Equal(meta.RawStruct(v1)))
	})

	It("is the disabled zero value without the flag, whatever ObjLock holds", func() {
		b := lockedBucket(lockBytes(true, new(governanceDays())))
		b.Flags &^= meta.BucketObjLockEnabled
		Expect(b.ObjectLock()).To(Equal(meta.ObjectLock{}))
	})

	It("is RGWObjectLock() with the flag and no encoding", func() {
		Expect(lockedBucket(nil).ObjectLock()).To(Equal(meta.ObjectLock{Enabled: true}))
	})

	It("decodes the rule with the flag", func() {
		Expect(lockedBucket(lockBytes(true, new(complianceYears()))).ObjectLock()).To(
			Equal(meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: complianceYears()}}))
	})

	It("fails on an encoding that does not decode", func() {
		_, err := lockedBucket(meta.RawStruct{1, 1, 1, 0, 0, 0, 1}).ObjectLock()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
	})

	It("sets ObjLock and leaves the flags to the caller", func() {
		b := normalBucket()
		b.SetObjectLock(meta.ObjectLock{Enabled: true})
		Expect(b.Flags).To(Equal(uint32(meta.BucketVersioned)))
		Expect(b.ObjLock).To(Equal(meta.RawStruct{1, 1, 2, 0, 0, 0, 1, 0}))
	})
})
