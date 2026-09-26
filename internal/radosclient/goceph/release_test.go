package goceph_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

var _ = Describe("mapping require_osd_release", func() {
	DescribeTable("a supported or newer name maps to a release",
		func(ctx SpecContext, name string, want denc.Release) {
			got, err := goceph.ReleaseFor(ctx, name)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("squid", "squid", denc.Squid),
		Entry("tentacle", "tentacle", denc.Tentacle),
		Entry("a known newer name", "umbrella", denc.Tentacle),
		Entry("an unknown newer name", "vampire", denc.Tentacle),
	)

	It("rejects a release below the squid floor", func(ctx SpecContext) {
		_, err := goceph.ReleaseFor(ctx, "reef")
		Expect(err).To(MatchError(radosclient.ErrNotSupported))
	})
})
