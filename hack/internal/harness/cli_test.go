package harness_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

var _ = Describe("BindEnv", func() {
	var v *viper.Viper

	BeforeEach(func() {
		for _, name := range []string{"GO_TAGS", "BENCH_GO_TAGS", "BENCH_PUT_OBJECTS", "PUT_OBJECTS"} {
			GinkgoT().Setenv(name, "")
			Expect(os.Unsetenv(name)).To(Succeed())
		}
		v = viper.New()
		v.SetDefault("go-tags", "ceph_preview")
		v.SetDefault("put-objects", "")
		Expect(harness.BindEnv(v, "BENCH", map[string]string{"go-tags": "GO_TAGS"})).To(Succeed())
	})

	It("reads a documented name", func() {
		GinkgoT().Setenv("GO_TAGS", "ceph_preview,extra")
		Expect(v.GetString("go-tags")).To(Equal("ceph_preview,extra"))
	})

	It("lets no prefixed alias of a documented name override it", func() {
		GinkgoT().Setenv("GO_TAGS", "ceph_preview,extra")
		GinkgoT().Setenv("BENCH_GO_TAGS", "alias")
		Expect(v.GetString("go-tags")).To(Equal("ceph_preview,extra"))
	})

	It("lets no prefixed alias stand in for a documented name left unset", func() {
		GinkgoT().Setenv("BENCH_GO_TAGS", "alias")
		Expect(v.GetString("go-tags")).To(Equal("ceph_preview"))
	})

	It("reads every other key from the prefix and the key upper-cased, dashes as underscores", func() {
		GinkgoT().Setenv("BENCH_PUT_OBJECTS", "100")
		GinkgoT().Setenv("PUT_OBJECTS", "200")
		Expect(v.GetString("put-objects")).To(Equal("100"))
	})

	It("takes an empty variable for an unset one", func() {
		GinkgoT().Setenv("GO_TAGS", "")
		Expect(v.GetString("go-tags")).To(Equal("ceph_preview"))
	})
})
