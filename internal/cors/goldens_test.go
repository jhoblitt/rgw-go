package cors_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// dumpRule renders r as ceph-dencoder's dump_json prints an RGWCORSRule
// (https://github.com/ceph/ceph/blob/v19.2.7/src/tools/ceph-dencoder/ceph_dencoder.cc#L184-L194):
// a pretty JSONFormatter, RGWCORSRule::dump inside an "object" section, then
// a line break. The dump opens a CORSRule section it never closes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.cc#L44-L53 at v19.2.7),
// so the dencoder's close_section closes that one and the object section is
// left open: the corpus JSON is not a JSON document, and is compared as
// bytes.
func dumpRule(r cors.Rule) string {
	GinkgoHelper()
	f := formatter.NewJSON(true)
	f.OpenObjectSection("object")
	f.OpenObjectSection("CORSRule")
	f.DumpString("ID", r.ID)
	f.DumpUnsigned("MaxAgeSeconds", uint64(r.MaxAge))
	f.DumpUnsigned("AllowedMethod", uint64(r.Methods))
	for _, s := range []struct {
		name string
		vals []string
	}{{"AllowedOrigin", r.AllowedOrigins}, {"AllowedHeader", r.AllowedHeaders}, {"ExposeHeader", r.ExposeHeaders}} {
		f.OpenArraySection(s.name)
		for _, v := range s.vals {
			f.DumpString("obj", v)
		}
		f.CloseSection()
	}
	f.CloseSection()
	Expect(f.Err()).NotTo(HaveOccurred())
	return string(f.Bytes()) + "\n"
}

var _ = Describe("corpus goldens", func() {
	It("re-encodes every RGWCORSRule as ceph-dencoder does", func() {
		goldentest.RoundTrip("testdata", "RGWCORSRule", goldentest.Options{Release: denc.Squid, SkipJSON: true},
			cors.DecodeRule, cors.EncodeRule)
	})
	It("decodes every RGWCORSRule to ceph-dencoder's dump", func() {
		cs, err := goldentest.Load("testdata", "RGWCORSRule")
		Expect(err).NotTo(HaveOccurred())
		Expect(cs).NotTo(BeEmpty())
		for _, c := range cs {
			d := denc.NewDecoder(c.Bin)
			r := cors.DecodeRule(d)
			Expect(d.Err()).NotTo(HaveOccurred(), c.Archive+"/"+c.Name)
			Expect(dumpRule(r)).To(Equal(string(c.JSON)), c.Archive+"/"+c.Name)
		}
	})
})
