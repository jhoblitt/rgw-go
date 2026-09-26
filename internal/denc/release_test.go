package denc_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

var _ = DescribeTable("ParseRelease",
	func(name string, want denc.Release, wantOK bool) {
		got, ok := denc.ParseRelease(name)
		Expect(ok).To(Equal(wantOK), "name %q", name)
		Expect(got).To(Equal(want), "name %q", name)
	},
	Entry("squid", "squid", denc.Squid, true),
	Entry("tentacle", "tentacle", denc.Tentacle, true),
	Entry("newer than known maps to the newest known", "umbrella", denc.Tentacle, true),
	Entry("uppercase", "Squid", denc.Squid, true),
	Entry("older than the floor", "reef", denc.Squid, false),
	Entry("unknown", "banana", denc.Squid, false),
)
