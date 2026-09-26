// Package goldentest checks Go encoders against goldens that hack/goldens/gen.sh
// generates from the ceph-object-corpus with ceph-dencoder, so tests never need
// the binary.
package goldentest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Case is one corpus object of one type: its stored bytes, ceph-dencoder's JSON dump of it,
// and ceph-dencoder's re-encoding of it at the dencoder's own version.
type Case struct {
	Type    string
	Archive string // corpus archive directory name, e.g. "19.2.0-404-g78ddc7f9027"
	Name    string // corpus object file name
	Bin     []byte
	JSON    []byte
	ReEnc   []byte
}

// Load returns every case under dir/goldens/<Type>/<Archive>/<Name>.{bin,json,reenc}.
// A .bin without its .json or .reenc is an error.
func Load(dir, typ string) ([]Case, error) {
	bins, err := filepath.Glob(filepath.Join(dir, "goldens", typ, "*", "*.bin"))
	if err != nil {
		return nil, fmt.Errorf("listing %s goldens: %w", typ, err)
	}
	cs := make([]Case, 0, len(bins))
	for _, bin := range bins {
		stem := strings.TrimSuffix(bin, ".bin")
		c := Case{
			Type:    typ,
			Archive: filepath.Base(filepath.Dir(bin)),
			Name:    filepath.Base(stem),
		}
		if c.Bin, err = os.ReadFile(bin); err != nil { //nolint:gosec // paths come from the test's own testdata
			return nil, fmt.Errorf("reading golden: %w", err)
		}
		if c.ReEnc, err = os.ReadFile(stem + ".reenc"); err != nil { //nolint:gosec // paths come from the test's own testdata
			return nil, fmt.Errorf("reading golden: %w", err)
		}
		if c.JSON, err = os.ReadFile(stem + ".json"); err != nil { //nolint:gosec // paths come from the test's own testdata
			return nil, fmt.Errorf("reading golden: %w", err)
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// Options tune a round-trip check for one type.
type Options struct {
	// Release is the release whose version the encoder emits; it must match the dencoder
	// that produced ReEnc (Squid for goldens generated from the v19 image).
	Release denc.Release
	// Nondeterministic marks a type whose C++ encoding is not byte-stable (unordered_map);
	// ReEnc is then compared by decoding both sides instead of by bytes.
	Nondeterministic bool
	// SkipJSON skips the JSON comparison for types whose Go JSON form need not match.
	SkipJSON bool
}

// RoundTrip decodes every case with decode, re-encodes with encode, and asserts the bytes
// equal ReEnc; unless SkipJSON is set, marshals the decoded value
// and asserts it equals the dencoder JSON after both are canonicalized. It uses Gomega
// assertions and is called from inside an It. A type with no goldens fails, and so does
// a decode that reports an error or leaves bytes unread.
func RoundTrip[T any](dir, typ string, o Options, decode func(*denc.Decoder) T, encode func(*denc.Encoder, T, denc.Release)) {
	GinkgoHelper()
	cs, err := Load(dir, typ)
	Expect(err).NotTo(HaveOccurred())
	Expect(cs).NotTo(BeEmpty(), "no goldens for %s under %s", typ, dir)
	for _, c := range cs {
		id := c.Type + "/" + c.Archive + "/" + c.Name
		v := decodeAll(id+" corpus bytes", c.Bin, decode)

		e := denc.NewEncoder()
		encode(e, v, o.Release)
		if o.Nondeterministic {
			Expect(decodeAll(id+" re-encoding", e.Bytes(), decode)).
				To(Equal(decodeAll(id+" dencoder re-encoding", c.ReEnc, decode)), "%s: re-encoding decodes differently", id)
		} else {
			Expect(e.Bytes()).To(Equal(c.ReEnc), "%s: re-encoding differs from ceph-dencoder", id)
		}

		if o.SkipJSON {
			continue
		}
		got, err := json.Marshal(v)
		Expect(err).NotTo(HaveOccurred(), "%s: marshaling JSON", id)
		Expect(canonical(got)).To(Equal(canonical(c.JSON)), "%s: JSON differs from ceph-dencoder", id)
	}
}

func decodeAll[T any](what string, b []byte, decode func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := decode(d)
	Expect(d.Err()).NotTo(HaveOccurred(), "decoding %s", what)
	Expect(d.Remaining()).To(BeZero(), "decoding %s left bytes unread", what)
	return v
}

// canonical parses b keeping numbers as json.Number, so integers above 2^53
// compare exactly rather than through float64.
func canonical(b []byte) any {
	GinkgoHelper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v, extra any
	Expect(d.Decode(&v)).To(Succeed(), "parsing JSON %q", b)
	Expect(d.Decode(&extra)).To(MatchError(io.EOF), "trailing data after JSON %q", b)
	return v
}
