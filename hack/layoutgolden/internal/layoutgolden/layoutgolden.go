// Package layoutgolden writes the goldens for ?layout from a capture of the
// ?layout and otp gate's run.
//
// # The capture
//
// A capture is lines of JSON, one for each object the gate laid out on a
// cluster of one release:
//
//	{"release":"squid","shape":"populated-small","bucket":"plain","key":"small.bin",
//	 "manifest_hex":"0806...","radosgw":"{\"head\":...}"}
//
// manifest_hex is the head's user.rgw.manifest as RADOS held it, radosgw is
// radosgw's ?layout document, and rgwgo is rgw-go's, present only where it
// differs from radosgw's: for a head whose manifest names a cloud tier, where
// rgw-go masks the secret radosgw writes in the clear. A line may follow any
// text that ends with Marker, as the gate's lines do in a CI log, so that
// the lines of a job's log are a capture once filtered by the marker:
//
//	gh run view --job ID --log | grep -F 'LAYOUT-GOLDEN-JSON ' > capture.jsonl
//
// Blank lines are skipped, and any other line that holds no record is an
// error.
//
// # The goldens
//
// Each shape becomes two files in the release's directory: <shape>.bin, the
// decoded manifest_hex, and <shape>.json, the golden document, which is the
// line's rgwgo where it has one and its radosgw otherwise, byte for byte and
// with no newline after it. Writing a release leaves no <shape>.bin or
// <shape>.json it did not write, so a shape the gate no longer prints is
// removed; files of any other extension stay. Nothing is written or removed
// unless every line is valid: a release, a shape, a hex manifest and a JSON
// document each, and no shape twice.
package layoutgolden

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Marker is what precedes a capture's record on a log line, and the gate's
// prefix for the lines it prints.
const Marker = "LAYOUT-GOLDEN-JSON "

// Golden is one shape's manifest and document.
type Golden struct {
	Shape string
	// Manifest is the head's stored manifest, the .bin.
	Manifest []byte
	// Document is the ?layout document the driver must write, the .json.
	Document []byte
}

// record is a capture's line.
type record struct {
	Release     string `json:"release"`
	Shape       string `json:"shape"`
	ManifestHex string `json:"manifest_hex"`
	Radosgw     string `json:"radosgw"`
	RGWGo       string `json:"rgwgo"`
}

// safeName is a release or a shape that can be a file name: the shapes are
// names such as populated-_underscore and rgwgo-compressed-zlib.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Parse reads the goldens of release from a capture.
func Parse(r io.Reader, release string) ([]Golden, error) {
	if !safeName.MatchString(release) {
		return nil, fmt.Errorf("%q is not a release name", release)
	}
	var goldens []Golden
	seen := map[string]int{}
	br := bufio.NewReader(r)
	for n := 1; ; n++ {
		line, err := br.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("reading line %d: %w", n, err)
		}
		if strings.TrimSpace(line) != "" {
			g, perr := parseLine(line, release)
			if perr != nil {
				return nil, fmt.Errorf("line %d: %w", n, perr)
			}
			if first, dup := seen[g.Shape]; dup {
				return nil, fmt.Errorf("line %d: shape %s is also line %d's", n, g.Shape, first)
			}
			seen[g.Shape] = n
			goldens = append(goldens, g)
		}
		if err != nil {
			break
		}
	}
	if len(goldens) == 0 {
		return nil, errors.New("the capture holds no record")
	}
	return goldens, nil
}

func parseLine(line, release string) (Golden, error) {
	if i := strings.Index(line, Marker); i >= 0 {
		line = line[i+len(Marker):]
	}
	var rec record
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return Golden{}, fmt.Errorf("not a record: %w", err)
	}
	switch {
	case rec.Release != release:
		return Golden{}, fmt.Errorf("a record of release %q, not %q", rec.Release, release)
	case !safeName.MatchString(rec.Shape):
		return Golden{}, fmt.Errorf("%q is not a shape name", rec.Shape)
	}
	manifest, err := hex.DecodeString(rec.ManifestHex)
	if err != nil || len(manifest) == 0 {
		return Golden{}, fmt.Errorf("shape %s: manifest_hex is not a hex manifest", rec.Shape)
	}
	doc := rec.RGWGo
	if doc == "" {
		doc = rec.Radosgw
	}
	if !json.Valid([]byte(doc)) {
		return Golden{}, fmt.Errorf("shape %s: the document is not JSON", rec.Shape)
	}
	return Golden{Shape: rec.Shape, Manifest: manifest, Document: []byte(doc)}, nil
}

// Write replaces the goldens in dir with goldens, creating dir.
func Write(dir string, goldens []Golden) error {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the goldens are checked in, readable like any tracked file
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	written := map[string]bool{}
	for _, g := range goldens {
		for ext, data := range map[string][]byte{".bin": g.Manifest, ".json": g.Document} {
			name := g.Shape + ext
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil { //nolint:gosec // see above
				return fmt.Errorf("writing %s: %w", name, err)
			}
			written[name] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listing %s: %w", dir, err)
	}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if !written[e.Name()] && !e.IsDir() && slices.Contains([]string{".bin", ".json"}, ext) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return fmt.Errorf("removing %s: %w", e.Name(), err)
			}
		}
	}
	return nil
}
