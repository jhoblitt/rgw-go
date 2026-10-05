package authz_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

var _ = Describe("ConfigFrom", func() {
	It("keeps radosgw's defaults for options librados does not report", func() {
		cfg, err := authz.ConfigFrom(cephconf.NewOptions(cephconf.MapGetter{}), denc.Tentacle)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(authz.Config{
			Release:                 denc.Tentacle,
			RejectInvalidPrincipals: true,
			EnforceSwiftACLs:        true,
			RemoteAddrParam:         "REMOTE_ADDR",
			ACLGrantsMaxNum:         100,
			MaxListingResults:       5000,
		}))
		Expect(authz.DefaultConfig(denc.Tentacle)).To(Equal(cfg))
		Expect(authz.DefaultConfig(denc.Squid).MaxListingResults).To(Equal(int64(1000)), "squid's listing bound")
	})

	It("reads every option", func() {
		cfg, err := authz.ConfigFrom(cephconf.NewOptions(cephconf.MapGetter{
			"rgw_policy_reject_invalid_principals": "false",
			"rgw_enforce_swift_acls":               "false",
			"rgw_remote_addr_param":                "HTTP_X_FORWARDED_FOR",
			"rgw_trust_forwarded_https":            "true",
			"rgw_acl_grants_max_num":               "7",
			"rgw_max_listing_results":              "2000",
		}), denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(Equal(authz.Config{
			Release:             denc.Squid,
			RemoteAddrParam:     "HTTP_X_FORWARDED_FOR",
			TrustForwardedHTTPS: true,
			ACLGrantsMaxNum:     7,
			MaxListingResults:   2000,
		}))
	})

	It("takes a negative grant limit as 100, as RGWPutACLs::execute does", func() {
		cfg, err := authz.ConfigFrom(cephconf.NewOptions(cephconf.MapGetter{"rgw_acl_grants_max_num": "-1"}), denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ACLGrantsMaxNum).To(Equal(100))
	})

	It("fails on an option that does not parse, naming it", func() {
		_, err := authz.ConfigFrom(cephconf.NewOptions(cephconf.MapGetter{"rgw_trust_forwarded_https": "yes"}), denc.Squid)
		Expect(err).To(MatchError(ContainSubstring("rgw_trust_forwarded_https")))
	})
})
