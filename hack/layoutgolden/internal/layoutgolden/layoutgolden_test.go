package layoutgolden_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/layoutgolden/internal/layoutgolden"
)

// line is a capture's record for shape in release, holding the manifest bytes
// manifest and the documents radosgw and rgwgo, "" leaving one out.
func line(release, shape string, manifest []byte, radosgw, rgwgo string) string {
	rec := map[string]string{"release": release, "shape": shape, "bucket": "b", "key": "k", "manifest_hex": hex.EncodeToString(manifest)}
	if radosgw != "" {
		rec["radosgw"] = radosgw
	}
	if rgwgo != "" {
		rec["rgwgo"] = rgwgo
	}
	b, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// layoutDir is internal/driver's testdata/layout, from the package's directory.
var layoutDir = filepath.Join("..", "..", "..", "..", "internal", "driver", "testdata", "layout")

const doc = `{"head":{"pool":"p","oid":"o","loc":""},"data_location":[]}`

var _ = Describe("Parse", func() {
	parse := func(capture string) ([]layoutgolden.Golden, error) {
		return layoutgolden.Parse(strings.NewReader(capture), "squid")
	}

	It("takes radosgw's document, and rgw-go's where the line has one, byte for byte", func() {
		secret := `{"a":"<&>é ","tier":{"secret":"******"}}`
		got, err := parse(line("squid", "plain", []byte{0, 8, 255}, doc, "") + "\n" +
			line("squid", "tiered", []byte{9}, `{"secret":"in the clear"}`, secret) + "\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]layoutgolden.Golden{
			{Shape: "plain", Manifest: []byte{0, 8, 255}, Document: []byte(doc)},
			{Shape: "tiered", Manifest: []byte{9}, Document: []byte(secret)},
		}))
	})

	It("reads a record after the marker and the text a CI log puts before it, skipping blank lines, and a last line with no newline", func() {
		got, err := parse("\n" + "squid / cluster\tUNKNOWN STEP\t2026-11-14T21:33:03.236Z " + layoutgolden.Marker + line("squid", "a", []byte{1}, doc, "") +
			"\r\n\n  \n" + line("squid", "b", []byte{2}, doc, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect(got[0].Shape).To(Equal("a"))
		Expect(got[1].Shape).To(Equal("b"))
	})

	DescribeTable("refuses a capture and names the line",
		func(capture, want string) {
			got, err := parse(capture)
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(got).To(BeNil())
		},
		Entry("with another release's record", line("squid", "a", []byte{1}, doc, "")+"\n"+line("tentacle", "b", []byte{1}, doc, "")+"\n",
			`line 2: a record of release "tentacle", not "squid"`),
		Entry("with a line that is no record", line("squid", "a", []byte{1}, doc, "")+"\nthe gate ended\n", "line 2: not a record"),
		Entry("with a shape that climbs out of the directory", line("squid", "../a", []byte{1}, doc, "")+"\n", `line 1: "../a" is not a shape name`),
		Entry("with a shape holding a slash", line("squid", "a/b", []byte{1}, doc, "")+"\n", `"a/b" is not a shape name`),
		Entry("with no shape", line("squid", "", []byte{1}, doc, "")+"\n", `"" is not a shape name`),
		Entry("with a manifest that is not hex", `{"release":"squid","shape":"a","manifest_hex":"xyz","radosgw":"{}"}`+"\n", "shape a: manifest_hex is not a hex manifest"),
		Entry("with no manifest", `{"release":"squid","shape":"a","radosgw":"{}"}`+"\n", "shape a: manifest_hex is not a hex manifest"),
		Entry("with a document that is cut short", line("squid", "a", []byte{1}, `{"head":`, "")+"\n", "shape a: the document is not JSON"),
		Entry("with no document", `{"release":"squid","shape":"a","manifest_hex":"01"}`+"\n", "shape a: the document is not JSON"),
		Entry("with a shape twice", line("squid", "a", []byte{1}, doc, "")+"\n"+line("squid", "a", []byte{2}, doc, "")+"\n",
			"line 2: shape a is also line 1's"),
		Entry("with nothing in it", "\n\n", "the capture holds no record"),
	)

	It("refuses a release that is no name", func() {
		_, err := layoutgolden.Parse(strings.NewReader(line("squid", "a", []byte{1}, doc, "")), "../squid")
		Expect(err).To(MatchError(ContainSubstring("is not a release name")))
	})
})

var _ = Describe("Write", func() {
	var dir string
	BeforeEach(func() { dir = filepath.Join(GinkgoT().TempDir(), "squid") })

	read := func(name string) []byte {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		Expect(err).NotTo(HaveOccurred())
		return b
	}
	names := func() []string {
		GinkgoHelper()
		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		slices.Sort(out)
		return out
	}

	It("creates the directory and writes each shape's manifest and document, with no newline after either", func() {
		Expect(layoutgolden.Write(dir, []layoutgolden.Golden{{Shape: "a", Manifest: []byte{1, 2}, Document: []byte(doc)}})).To(Succeed())
		Expect(names()).To(Equal([]string{"a.bin", "a.json"}))
		Expect(read("a.bin")).To(Equal([]byte{1, 2}))
		Expect(read("a.json")).To(Equal([]byte(doc)))
	})

	It("removes the .bin and .json files of a shape it did not write, and leaves other files and its own", func() {
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		for _, name := range []string{"old.bin", "old.json", "keep.txt", "keep.jsonl"} {
			Expect(os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)).To(Succeed())
		}
		Expect(layoutgolden.Write(dir, []layoutgolden.Golden{{Shape: "a", Manifest: []byte{1}, Document: []byte(doc)}})).To(Succeed())
		Expect(names()).To(Equal([]string{"a.bin", "a.json", "keep.jsonl", "keep.txt"}))
	})

	It("replaces what a shape's files held", func() {
		Expect(layoutgolden.Write(dir, []layoutgolden.Golden{{Shape: "a", Manifest: []byte{1, 2, 3}, Document: []byte(doc + doc)}})).To(Succeed())
		Expect(layoutgolden.Write(dir, []layoutgolden.Golden{{Shape: "a", Manifest: []byte{9}, Document: []byte(doc)}})).To(Succeed())
		Expect(read("a.bin")).To(Equal([]byte{9}))
		Expect(read("a.json")).To(Equal([]byte(doc)))
	})
})

var _ = Describe("Run", func() {
	var (
		root, capture, out string
		stdout, stderr     *bytes.Buffer
	)
	BeforeEach(func() {
		root = GinkgoT().TempDir()
		capture, out = filepath.Join(root, "squid.jsonl"), filepath.Join(root, "layout")
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	run := func(ctx SpecContext, args ...string) error {
		return layoutgolden.Run(ctx, args, nil, stdout, stderr)
	}
	write := func(s string) {
		GinkgoHelper()
		Expect(os.WriteFile(capture, []byte(s), 0o644)).To(Succeed())
	}

	It("writes a release's goldens into <out>/<release> from --in, and says how many", func(ctx SpecContext) {
		write(line("squid", "a", []byte{1}, doc, "") + "\n" + line("squid", "b", []byte{2}, doc, "") + "\n")
		Expect(run(ctx, "--release", "squid", "--in", capture, "--out", out)).To(Succeed())
		Expect(stdout.String()).To(Equal("wrote 2 goldens of squid into " + filepath.Join(out, "squid") + "\n"))
		b, err := os.ReadFile(filepath.Join(out, "squid", "b.bin"))
		Expect(err).NotTo(HaveOccurred())
		Expect(b).To(Equal([]byte{2}))
	})

	It("reads <out>/<release>.jsonl when --in is not given", func(ctx SpecContext) {
		Expect(os.MkdirAll(out, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(out, "squid.jsonl"), []byte(line("squid", "a", []byte{1}, doc, "")), 0o644)).To(Succeed())
		Expect(run(ctx, "--release", "squid", "--out", out)).To(Succeed())
		Expect(filepath.Join(out, "squid", "a.json")).To(BeAnExistingFile())
	})

	It("takes its settings from the environment", func(ctx SpecContext) {
		write(line("squid", "a", []byte{1}, doc, "") + "\n")
		GinkgoT().Setenv("LAYOUTGOLDEN_RELEASE", "squid")
		GinkgoT().Setenv("LAYOUTGOLDEN_IN", capture)
		GinkgoT().Setenv("LAYOUTGOLDEN_OUT", out)
		Expect(run(ctx)).To(Succeed())
		Expect(filepath.Join(out, "squid", "a.bin")).To(BeAnExistingFile())
	})

	It("refuses to run without a release", func(ctx SpecContext) {
		Expect(run(ctx, "--in", capture, "--out", out)).To(MatchError("--release is required"))
	})

	It("refuses a capture it cannot open", func(ctx SpecContext) {
		Expect(run(ctx, "--release", "squid", "--in", capture, "--out", out)).To(MatchError(ContainSubstring("opening the capture")))
	})

	It("writes and removes nothing for a capture with a bad line", func(ctx SpecContext) {
		Expect(os.MkdirAll(filepath.Join(out, "squid"), 0o755)).To(Succeed())
		old := filepath.Join(out, "squid", "old.json")
		Expect(os.WriteFile(old, []byte("{}"), 0o644)).To(Succeed())
		write(line("squid", "a", []byte{1}, doc, "") + "\nnot a record\n")
		err := run(ctx, "--release", "squid", "--in", capture, "--out", out)
		Expect(err).To(MatchError(ContainSubstring("line 2: not a record")))
		Expect(old).To(BeAnExistingFile())
		Expect(filepath.Join(out, "squid", "a.bin")).NotTo(BeAnExistingFile())
	})

	It("refuses an unknown log format", func(ctx SpecContext) {
		write(line("squid", "a", []byte{1}, doc, "") + "\n")
		Expect(run(ctx, "--release", "squid", "--in", capture, "--out", out, "--log-format", "xml")).To(MatchError(ContainSubstring("unknown log format")))
	})
})

// The goldens internal/driver's specs lay objects out against are what this
// package makes of the captures beside them.
var _ = Describe("the checked-in goldens", func() {
	DescribeTable("are the capture's, byte for byte, and nothing else",
		func(ctx SpecContext, release string) {
			tmp := GinkgoT().TempDir()
			Expect(layoutgolden.Run(ctx, []string{"--release", release, "--in", filepath.Join(layoutDir, release+".jsonl"), "--out", tmp},
				nil, &bytes.Buffer{}, &bytes.Buffer{})).To(Succeed())

			made, err := os.ReadDir(filepath.Join(tmp, release))
			Expect(err).NotTo(HaveOccurred())
			held, err := os.ReadDir(filepath.Join(layoutDir, release))
			Expect(err).NotTo(HaveOccurred())
			names := func(es []os.DirEntry) []string {
				var out []string
				for _, e := range es {
					out = append(out, e.Name())
				}
				return out
			}
			Expect(names(made)).To(HaveLen(48), "24 shapes, a .bin and a .json each")
			Expect(names(made)).To(Equal(names(held)))
			for _, name := range names(made) {
				want, err := os.ReadFile(filepath.Join(tmp, release, name))
				Expect(err).NotTo(HaveOccurred())
				got, err := os.ReadFile(filepath.Join(layoutDir, release, name))
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(want), "%s/%s", release, name)
			}
		},
		Entry("squid's", "squid"),
		Entry("tentacle's", "tentacle"),
	)
})
