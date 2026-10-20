package lifecycle_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
)

// corpusObject is one object of testdata/corpus, copied from the ceph
// checkout's ceph-object-corpus/archive/<archive>/objects/<type>/<name>.
type corpusObject struct {
	id  string
	bin []byte
}

func loadCorpus(typ string) []corpusObject {
	GinkgoHelper()
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", typ, "*", "*.bin"))
	Expect(err).NotTo(HaveOccurred())
	Expect(paths).NotTo(BeEmpty(), "no corpus objects for %s", typ)
	objs := make([]corpusObject, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p) //nolint:gosec // paths come from the test's own testdata
		Expect(err).NotTo(HaveOccurred())
		objs = append(objs, corpusObject{id: filepath.Join(filepath.Base(filepath.Dir(p)), filepath.Base(p)), bin: b})
	}
	return objs
}

// upgraded is a corpus struct as radosgw re-encodes it at version v: the
// version byte raised, the payload followed by grow zero bytes, which is
// what each field the older version lacks encodes to empty (an empty string
// or a zero u32), and the length raised to match. It is the upgrade
// radosgw's LCExpiration and LCFilter encoders imply (LCExpiration v3 to v4,
// newer_noncurrent: four bytes; LCFilter v2 to v4, flags and two strings:
// twelve bytes; v3 to v4, the strings: eight), applied by hand.
func upgraded(b []byte, v uint8, grow int) []byte {
	out := slices.Clone(b)
	out[0] = v
	binary.LittleEndian.PutUint32(out[2:6], binary.LittleEndian.Uint32(out[2:6])+uint32(grow)) //nolint:gosec // grow is at most 12
	return append(out, make([]byte, grow)...)
}

var _ = Describe("ceph-object-corpus", func() {
	It("reads every LCExpiration and writes it back as v4", func() {
		for _, o := range loadCorpus("LCExpiration") {
			x := decodeAll(o.bin, lifecycle.DecodeExpiration)
			want := o.bin
			switch o.bin[0] {
			case 3:
				want = upgraded(o.bin, 4, 4)
			case 4:
			default:
				Fail(o.id + ": a version the corpus was not expected to hold")
			}
			Expect(encode(x, lifecycle.Expiration.Encode, denc.Squid)).To(Equal(want), o.id)
		}
	})

	It("reads every LCFilter and writes it back as v4", func() {
		for _, o := range loadCorpus("LCFilter") {
			f := decodeAll(o.bin, lifecycle.DecodeFilter)
			want := o.bin
			switch o.bin[0] {
			case 2:
				want = upgraded(o.bin, 4, 12)
			case 3:
				want = upgraded(o.bin, 4, 8)
			case 4:
			default:
				Fail(o.id + ": a version the corpus was not expected to hold")
			}
			Expect(encode(f, lifecycle.Filter.Encode, denc.Squid)).To(Equal(want), o.id)
		}
	})

	// Every corpus LCRule is v6. Those from 15.0.0 to 16.2.0 hold v3
	// expirations and a v2 filter, those from 18.2.0 v3 expirations and a
	// v3 filter, and those from 19.2.0 are current throughout.
	It("reads every LCRule and writes it back with its parts at their current versions", func() {
		for _, o := range loadCorpus("LCRule") {
			r := decodeAll(o.bin, lifecycle.DecodeRule)
			got := encode(r, lifecycle.Rule.Encode, denc.Squid)
			Expect(decodeAll(got, lifecycle.DecodeRule)).To(Equal(r), o.id)
			switch filepath.Dir(o.id) {
			case "19.2.0-404-g78ddc7f9027":
				Expect(got).To(Equal(o.bin), o.id)
			case "18.2.0":
				Expect(got).To(HaveLen(len(o.bin)+3*4+8), o.id)
			default:
				Expect(got).To(HaveLen(len(o.bin)+3*4+12), o.id)
			}
		}
	})

	DescribeTable("writes an older LCRule back byte for byte as derived by hand",
		func(path string, want []string) {
			b, err := os.ReadFile(filepath.Join("testdata", "corpus", "LCRule", path)) //nolint:gosec // the test's own testdata
			Expect(err).NotTo(HaveOccurred())
			Expect(encode(decodeAll(b, lifecycle.DecodeRule), lifecycle.Rule.Encode, denc.Squid)).To(Equal(hexBytes(want...)))
		},
		// rule1, prefix test1/, Enabled, three empty v3 expirations,
		// ExpiredObjectDeleteMarker, an empty v2 filter, no transitions: its
		// length 0x65 grows by 3*4 for newer_noncurrent and 12 for the
		// filter's flags and sizes, to 0x7d.
		Entry("15.0.0, v3 expirations and a v2 filter", "15.0.0-539-g191ab33faf/35dd962ca930ac207e01035a9f7ba87b.bin", []string{
			"06 01 7d000000", "05000000 72756c6531", "06000000 74657374312f", "07000000 456e61626c6564",
			expEmpty, expEmpty, expEmpty, "01", filterEmpty, "00000000", "00000000",
		}),
		// The same with prefix "" and a v3 filter: 0x63 grows by 3*4 and 8,
		// to 0x77.
		Entry("18.2.0, v3 expirations and a v3 filter", "18.2.0/fa296b32deb728c113fd4a89b5867b07.bin", []string{
			"06 01 77000000", "05000000 72756c6531", "00000000", "07000000 456e61626c6564",
			expEmpty, expEmpty, expEmpty, "01", filterEmpty, "00000000", "00000000",
		}),
	)

	It("reads every RGWLifecycleConfiguration and writes it back unchanged", func() {
		for _, o := range loadCorpus("RGWLifecycleConfiguration") {
			c := decodeAll(o.bin, lifecycle.DecodeConfiguration)
			Expect(encode(c, lifecycle.Configuration.Encode, denc.Squid)).To(Equal(o.bin), o.id)
		}
	})
})
