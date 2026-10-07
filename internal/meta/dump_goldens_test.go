package meta_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// dumpGolden renders v as ceph-dencoder's dump_json does
// (ceph_dencoder.cc:189-194): a pretty JSONFormatter, the value's dump()
// inside an "object" section, then a line break. The corpus JSON comes from
// the v19 dencoder, so the dump is Squid's.
func dumpGolden(v meta.Dumper) string {
	GinkgoHelper()
	f := formatter.NewJSON(true)
	f.OpenObjectSection("object")
	v.Dump(f, denc.Squid)
	f.CloseSection()
	Expect(f.Err()).NotTo(HaveOccurred())
	return string(f.Bytes()) + "\n"
}

// dumpGoldens compares the dump of every corpus object of typ with the
// dencoder's bytes, which carry dump()'s key order and number formatting.
func dumpGoldens[T meta.Dumper](typ string, decode func(*denc.Decoder) T) {
	GinkgoHelper()
	cs, err := goldentest.Load("testdata", typ)
	Expect(err).NotTo(HaveOccurred())
	Expect(cs).NotTo(BeEmpty(), "no goldens for %s", typ)
	for _, c := range cs {
		Expect(dumpGolden(decodeWhole(c.Bin, decode))).To(Equal(string(c.JSON)), c.Type+"/"+c.Archive+"/"+c.Name)
	}
}

var _ = Describe("Dump against the corpus", func() {
	It("RGWQuotaInfo", func() { dumpGoldens("RGWQuotaInfo", meta.DecodeQuota) })
	It("RGWUserCaps", func() { dumpGoldens("RGWUserCaps", meta.DecodeCaps) })
	It("RGWZoneParams", func() { dumpGoldens("RGWZoneParams", meta.DecodeZoneParams) })
	It("RGWZonePlacementInfo", func() { dumpGoldens("RGWZonePlacementInfo", meta.DecodeZonePlacementInfo) })
	It("RGWZoneStorageClasses", func() { dumpGoldens("RGWZoneStorageClasses", meta.DecodeZoneStorageClasses) })
	It("RGWZoneStorageClass", func() { dumpGoldens("RGWZoneStorageClass", meta.DecodeZoneStorageClass) })
})
