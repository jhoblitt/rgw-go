package driver_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// withoutOption reads the options through Options as librados would know
// them, but one, which it does not know.
type withoutOption struct {
	opts    *cephconf.Options
	missing string
}

func (w withoutOption) ConfigGet(name string) (string, error) {
	if name == w.missing {
		return cephconf.MapGetter{}.ConfigGet(name)
	}
	return w.opts.String(name)
}

var _ = Describe("multipart options", func() {
	It("reads the completion lock's duration and the minimum part size at Open", func(ctx SpecContext) {
		s, err := driver.Open(ctx, newPutCluster(), conf(map[string]string{"rgw_mp_lock_max_time": "120", "rgw_multipart_min_part_size": "1048576"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		lockTime, minPart := s.MPOptionsForTest()
		Expect(lockTime).To(Equal(2 * time.Minute))
		Expect(minPart).To(BeEquivalentTo(1 << 20))
	})
	It("narrows the lock duration to utime_t's 32-bit seconds, as radosgw does", func(ctx SpecContext) {
		s, err := driver.Open(ctx, newPutCluster(), conf(map[string]string{"rgw_mp_lock_max_time": "4294967897"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		lockTime, _ := s.MPOptionsForTest()
		Expect(lockTime).To(Equal(601*time.Second), "2^32 + 601 keeps its low 32 bits")
	})
	DescribeTable("refuses to open without an option it reads",
		func(ctx SpecContext, option string) {
			_, err := driver.Open(ctx, newPutCluster(), cephconf.NewOptions(withoutOption{conf(nil), option}), driver.Options{})
			Expect(err).To(MatchError(cephconf.ErrUnknownOption))
			Expect(err).To(MatchError(ContainSubstring(option)))
		},
		Entry("rgw_mp_lock_max_time", "rgw_mp_lock_max_time"),
		Entry("rgw_multipart_min_part_size", "rgw_multipart_min_part_size"),
	)
})

var _ = Describe("namesUploadObjects", func() {
	const upload = "k.2~upload"
	It("finds an upload prefix in a rule, the manifest's own or an override", func() {
		m := meta.NewManifest()
		m.Prefix = "k.RANDOM"
		m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1}, 5 << 20: {StartPartNum: 2, OverridePrefix: upload}}
		Expect(driver.NamesUploadObjectsForTest(&m, upload)).To(BeTrue(), "an override")
		Expect(driver.NamesUploadObjectsForTest(&m, "k.RANDOM")).To(BeTrue(), "a rule without an override takes the manifest's")
		Expect(driver.NamesUploadObjectsForTest(&m, "k.2~other")).To(BeFalse())
	})
	It("finds an upload prefix in an explicit manifest's part and shadow names", func() {
		m := meta.NewManifest()
		m.ExplicitObjs = true
		m.Objs = map[uint64]meta.ManifestPart{
			0:       {Loc: meta.Obj{Key: meta.ObjKey{Name: upload + ".1", NS: meta.NSMultipart}}},
			4 << 20: {Loc: meta.Obj{Key: meta.ObjKey{Name: upload + ".1_1", NS: meta.NSShadow}}},
		}
		Expect(driver.NamesUploadObjectsForTest(&m, upload)).To(BeTrue())
		Expect(driver.NamesUploadObjectsForTest(&m, "k.2~other")).To(BeFalse())
	})
	It("finds nothing in a head without a manifest", func() {
		Expect(driver.NamesUploadObjectsForTest(nil, upload)).To(BeFalse())
	})
})
