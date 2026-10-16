package goceph_test

import (
	"time"

	"github.com/ceph/go-ceph/rados"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

var _ = Describe("converting a modification time for librados", func() {
	DescribeTable("gives librados the time's seconds and nanoseconds",
		func(t time.Time, want rados.Timespec) {
			Expect(goceph.Timespec(t)).To(Equal(want))
		},
		Entry("the zero time, which keeps the stored mtime, as {0,0}",
			time.Time{}, rados.Timespec{}),
		Entry("the Unix epoch, which keeps the stored mtime too, as {0,0}",
			time.Unix(0, 0), rados.Timespec{}),
		Entry("a real time unchanged",
			time.Date(2026, 10, 16, 1, 2, 3, 456789, time.UTC),
			rados.Timespec{Sec: 1792112523, Nsec: 456789}),
		Entry("a real time in another zone as the same instant",
			time.Date(2026, 10, 15, 18, 2, 3, 456789, time.FixedZone("PDT", -7*3600)),
			rados.Timespec{Sec: 1792112523, Nsec: 456789}),
	)
})
