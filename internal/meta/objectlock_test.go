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
