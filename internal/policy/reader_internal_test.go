package policy

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// recorder is a jsonHandler that writes each event as rj.cc does and refuses
// the event numbered refuse.
type recorder struct {
	ev     strings.Builder
	n      int
	refuse int
}

func (h *recorder) add(e string) bool {
	h.ev.WriteString(e + " ")
	h.n++
	return h.n-1 != h.refuse
}

func (h *recorder) startObject() bool       { return h.add("O") }
func (h *recorder) endObject() bool         { return h.add("o") }
func (h *recorder) startArray() bool        { return h.add("A") }
func (h *recorder) endArray() bool          { return h.add("a") }
func (h *recorder) key(s string) bool       { return h.add("K" + hex.EncodeToString([]byte(s))) }
func (h *recorder) str(s string) bool       { return h.add("S" + hex.EncodeToString([]byte(s))) }
func (h *recorder) rawNumber(s string) bool { return h.add("N" + hex.EncodeToString([]byte(s))) }
func (h *recorder) literal() bool           { return h.add("L") }
func (h *recorder) refusal() string         { return "Terminate parsing due to Handler error." }

var _ = Describe("jsonReader", func() {
	// testdata/rapidjson/golden.txt holds rapidjson fcb23c2d's events and
	// results, which rj.cc beside it regenerates.
	It("reports the events, messages and offsets rapidjson does", func() {
		b, err := os.ReadFile(filepath.Join("testdata", "rapidjson", "golden.txt"))
		Expect(err).NotTo(HaveOccurred())
		cases := 0
		for line := range strings.SplitSeq(strings.TrimSuffix(string(b), "\n"), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.SplitN(line, "\t", 3)
			Expect(fields).To(HaveLen(3), "line %q", line)
			refuse, err := strconv.Atoi(fields[0])
			Expect(err).NotTo(HaveOccurred(), "line %q", line)
			input, err := strconv.Unquote(fields[1])
			Expect(err).NotTo(HaveOccurred(), "line %q", line)

			h := &recorder{refuse: refuse}
			r := &jsonReader{src: rgwtext.CString(input), h: h}
			result := "OK"
			if pe := r.parse(); pe != nil {
				result = pe.Annotation + " @" + strconv.FormatInt(pe.Offset, 10)
			}
			got := h.ev.String() + "| " + result
			Expect(got).To(Equal(fields[2]), "input %s, refusing event %d", fields[1], refuse)
			cases++
		}
		Expect(cases).To(BeNumerically(">=", 50))
	})
})
