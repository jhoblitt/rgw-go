package goldentest_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

type T struct {
	V uint8 `json:"v"`
}

func decodeT(d *denc.Decoder) T {
	h := d.BeginStruct(1)
	v := T{V: d.U8()}
	d.EndStruct(h)
	return v
}

func encodeT(e *denc.Encoder, v T, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U8(v.V)
	e.EndStruct(f)
}

type Big struct {
	V uint64 `json:"v"`
}

func decodeBig(d *denc.Decoder) Big {
	h := d.BeginStruct(1)
	v := Big{V: d.U64()}
	d.EndStruct(h)
	return v
}

func encodeBig(e *denc.Encoder, v Big, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(v.V)
	e.EndStruct(f)
}

var _ = DescribeTable("RoundTrip compares integers above 2^53 exactly",
	func(v uint64, js string, ok bool) {
		dir := GinkgoT().TempDir()
		caseDir := filepath.Join(dir, "goldens", "Big", "arch")
		Expect(os.MkdirAll(caseDir, 0o750)).To(Succeed())
		e := denc.NewEncoder()
		encodeBig(e, Big{V: v}, denc.Squid)
		for _, ext := range []string{".bin", ".reenc"} {
			Expect(os.WriteFile(filepath.Join(caseDir, "b"+ext), e.Bytes(), 0o600)).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(caseDir, "b.json"), []byte(js), 0o600)).To(Succeed())
		failures := InterceptGomegaFailures(func() {
			goldentest.RoundTrip(dir, "Big", goldentest.Options{}, decodeBig, encodeBig)
		})
		if ok {
			Expect(failures).To(BeEmpty())
		} else {
			Expect(failures).To(ContainElement(ContainSubstring("JSON differs from ceph-dencoder")))
		}
	},
	Entry("differing in the last digit", uint64(11111111111111111), `{"v":11111111111111112}`, false),
	Entry("at the uint64 maximum", uint64(18446744073709551615), `{"v":18446744073709551615}`, true),
)

var _ = Describe("RoundTrip", func() {
	var dir, caseDir string

	write := func(name string, b []byte) {
		GinkgoHelper()
		Expect(os.WriteFile(filepath.Join(caseDir, name), b, 0o600)).To(Succeed())
	}
	roundTrip := func(typ string, o goldentest.Options) []string {
		return InterceptGomegaFailures(func() {
			goldentest.RoundTrip(dir, typ, o, decodeT, encodeT)
		})
	}

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		caseDir = filepath.Join(dir, "goldens", "T", "arch")
		Expect(os.MkdirAll(caseDir, 0o750)).To(Succeed())
		write("a.bin", []byte{1, 1, 1, 0, 0, 0, 7})
		write("a.reenc", []byte{1, 1, 1, 0, 0, 0, 7})
		write("a.json", []byte(`{"v":7}`))
	})

	It("loads every case with its archive and name", func() {
		cs, err := goldentest.Load(dir, "T")
		Expect(err).NotTo(HaveOccurred())
		Expect(cs).To(ConsistOf(goldentest.Case{
			Type: "T", Archive: "arch", Name: "a",
			Bin:   []byte{1, 1, 1, 0, 0, 0, 7},
			JSON:  []byte(`{"v":7}`),
			ReEnc: []byte{1, 1, 1, 0, 0, 0, 7},
		}))
	})
	It("passes when the re-encoding and JSON match", func() {
		Expect(roundTrip("T", goldentest.Options{Release: denc.Squid})).To(BeEmpty())
	})
	It("fails when the re-encoding differs", func() {
		write("a.reenc", []byte{1, 1, 1, 0, 0, 0, 8})
		Expect(roundTrip("T", goldentest.Options{Release: denc.Squid})).
			To(ContainElement(ContainSubstring("T/arch/a: re-encoding differs from ceph-dencoder")))
	})
	It("compares a nondeterministic type by decoded value", func() {
		write("a.reenc", []byte{1, 1, 1, 0, 0, 0, 8})
		Expect(roundTrip("T", goldentest.Options{Nondeterministic: true})).
			To(ContainElement(ContainSubstring("T/arch/a: re-encoding decodes differently")))
		write("a.reenc", []byte{1, 1, 2, 0, 0, 0, 7, 0})
		Expect(roundTrip("T", goldentest.Options{Nondeterministic: true})).To(BeEmpty())
	})
	It("fails when the JSON differs unless it is skipped", func() {
		write("a.json", []byte(`{"v": 8}`))
		Expect(roundTrip("T", goldentest.Options{})).
			To(ContainElement(ContainSubstring("T/arch/a: JSON differs from ceph-dencoder")))
		Expect(roundTrip("T", goldentest.Options{SkipJSON: true})).To(BeEmpty())
	})
	It("fails when the JSON has trailing data", func() {
		write("a.json", []byte(`{"v":7} {"v":7}`))
		Expect(roundTrip("T", goldentest.Options{})).
			To(ContainElement(ContainSubstring("trailing data after JSON")))
	})
	It("canonicalizes JSON before comparing", func() {
		write("a.json", []byte("{\n    \"v\": 7\n}\n"))
		Expect(roundTrip("T", goldentest.Options{})).To(BeEmpty())
	})
	DescribeTable("fails to load a case missing a companion file",
		func(ext string) {
			Expect(os.Remove(filepath.Join(caseDir, "a"+ext))).To(Succeed())
			_, err := goldentest.Load(dir, "T")
			Expect(err).To(MatchError(ContainSubstring("a" + ext)))
			Expect(roundTrip("T", goldentest.Options{})).
				To(ContainElement(ContainSubstring("reading golden")))
		},
		Entry("without JSON", ".json"),
		Entry("without a re-encoding", ".reenc"),
	)
	It("fails when decoding leaves trailing bytes", func() {
		write("a.bin", []byte{1, 1, 1, 0, 0, 0, 7, 0})
		Expect(roundTrip("T", goldentest.Options{})).
			To(ContainElement(ContainSubstring("decoding T/arch/a corpus bytes left bytes unread")))
	})
	It("fails when decoding reports an error", func() {
		write("a.bin", []byte{1, 1, 9, 0, 0, 0, 7})
		Expect(roundTrip("T", goldentest.Options{})).
			To(ContainElement(And(ContainSubstring("decoding T/arch/a corpus bytes"), ContainSubstring("struct_len 9"))))
	})
	It("fails when the type has no goldens", func() {
		Expect(roundTrip("missing", goldentest.Options{})).
			To(ContainElement(ContainSubstring("no goldens for missing")))
	})
})
