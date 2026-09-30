package auth_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

var _ = Describe("ConfigFrom", func() {
	It("reads the three options and keeps defaults for unknown ones", func() {
		o := cephconf.NewOptions(cephconf.MapGetter{
			"rgw_s3_auth_use_rados":             "false",
			"rgw_s3_auth_disable_signature_url": "true",
		})
		cfg, err := auth.ConfigFrom(o, []string{"s3.example.com"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.UseRados).To(BeFalse())
		Expect(cfg.DisablePresignedURLs).To(BeTrue())
		Expect(cfg.Insecure).To(BeFalse(), "rgw_sigv4_insecure unknown to this getter keeps its default")
		Expect(cfg.DNSNames).To(Equal([]string{"s3.example.com"}))
	})

	It("reads rgw_sigv4_insecure", func() {
		cfg, err := auth.ConfigFrom(cephconf.NewOptions(cephconf.MapGetter{"rgw_sigv4_insecure": "true"}), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Insecure).To(BeTrue())
		Expect(cfg.UseRados).To(BeTrue(), "rgw_s3_auth_use_rados unknown to this getter keeps its default")
	})

	It("fails on a value librados never renders for a bool", func() {
		_, err := auth.ConfigFrom(cephconf.NewOptions(cephconf.MapGetter{"rgw_s3_auth_use_rados": "yes"}), nil)
		Expect(err).To(MatchError(ContainSubstring("rgw_s3_auth_use_rados")))
	})

	It("has radosgw's defaults", func() {
		cfg := auth.DefaultConfig()
		Expect(cfg.UseRados).To(BeTrue())
		Expect(cfg.DisablePresignedURLs).To(BeFalse())
		Expect(cfg.Insecure).To(BeFalse())
	})
})
