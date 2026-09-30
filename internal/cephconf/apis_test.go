package cephconf_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

var _ = Describe("EnabledAPIs", func() {
	DescribeTable("reads rgw_enable_apis",
		func(value string, want cephconf.APIs) {
			got, err := cephconf.EnabledAPIs(cephconf.NewOptions(cephconf.MapGetter{"rgw_enable_apis": value}))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("radosgw's default on main",
			"s3, s3control, s3website, swift, swift_auth, admin, sts, iam, notifications, s3vectors",
			cephconf.APIs{S3: true, Admin: true, Ignored: []string{
				"s3control", "s3website", "swift", "swift_auth", "sts", "iam", "notifications", "s3vectors",
			}}),
		Entry("radosgw's default at v19.2.6 and v20.2.4",
			"s3, s3website, swift, swift_auth, admin, sts, iam, notifications",
			cephconf.APIs{S3: true, Admin: true, Ignored: []string{
				"s3website", "swift", "swift_auth", "sts", "iam", "notifications",
			}}),
		Entry("swift and admin without s3", "swift, admin", cephconf.APIs{Admin: true, Ignored: []string{"swift"}}),
		Entry("s3website alone, which enables s3", "s3website", cephconf.APIs{S3: true, Ignored: []string{"s3website"}}),
		Entry("no spaces after the commas", "s3,admin", cephconf.APIs{S3: true, Admin: true}),
		Entry("an empty value, which enables nothing", "", cephconf.APIs{}),
		Entry("a name repeated, listed once", "s3, swift, swift", cephconf.APIs{S3: true, Ignored: []string{"swift"}}),
		Entry("names in another case, which radosgw does not match", "S3, Admin",
			cephconf.APIs{Ignored: []string{"S3", "Admin"}}),
	)
	It("passes a read failure through", func() {
		_, err := cephconf.EnabledAPIs(cephconf.NewOptions(cephconf.MapGetter{}))
		Expect(err).To(MatchError(cephconf.ErrUnknownOption))
	})
})
