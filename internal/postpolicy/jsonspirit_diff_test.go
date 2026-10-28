package postpolicy

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/jhoblitt/rgw-go/internal/jsonspirit"
)

// spiritSeeds are the policy documents this package's specs read, and
// json_spirit's grammar cases.
func spiritSeeds() []string {
	exp := `"expiration": "2030-01-01T00:00:00Z"`
	base := `{"bucket": "bkt"}, ["starts-with", "$key", "foo"], {"acl": "private"}, ["starts-with", "$Content-Type", "text/plain"]`
	return []string{
		`{` + exp + `, "conditions": [` + base + `, ["content-length-range", 0, 1024]]}`,
		`{` + exp + `, ` + exp + `, "conditions": {"b": ["eq", "$key", 1e3], "a": {"z": 0.3, "acl": [true, null, {"k": -0}]}}}`,
		`{` + exp + `, "conditions": [["eq", "$x", 18446744073709551615], ["eq", "$y", -9223372036854775808], ["eq", "$z", 1.5e-7]]}`,
		`{` + exp + `, "conditions": [["EQ\u0000", "$k\u0000x", "😀"]]} trailing`,
		`{` + exp + `, "conditions": [["eq", "$x", "\ud83d"], ["eq", "$y", "\ude00x"], ["eq", "$z", "éA"]]}`,
		`{"expiration": "2029-12-31 00:00:00.000000+00:00", "conditions": [` + base + `]}`,
		`{` + exp + `, "conditions": [{ }, [], "bucket", 5, {"a": "a\"b\\c\/d\n"}]}`,
		`{` + exp + `, "conditions": [[1.5, "$key", "a"], ["eq", "$x", [ "a" , 1, 2.5, [], {} ]], ["eq", "$x", {"b": 1, "a": null, "b": true}]]}`,
		`{` + exp + `, "conditions": [["eq", "$x", 0.12345678901234567890], ["eq", "$x", 1e23], ["eq", "$x", 1e20], ["eq", "$x", -0.0], ["eq", "$x", 25E+1]]}`,
		`{` + exp + `, "conditions": [["eq", "$x", 1` + strings.Repeat("0", 300) + `.0]]}`,
		`{` + exp + `, "conditions": [["eq", "$x", 1e301], ["eq", "$x", 1e126], ["eq", "$x", 1` + strings.Repeat("0", 20) + `e300]]}`,
		`{` + exp + `, "conditions": [{"bucket": "bkt"},]}`, "{\f" + exp + ", \"conditions\": []}",
		`{` + exp + `, "conditions": [{"bucket": "b` + "\x01\xff" + `"}, {"bucket": "\x41\101\u00zz"}, ["content-length-range", +1, 5]]}`,
		`[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]`,
		`"x"`, `5`, `5 `, ``, `1e3` + strings.Repeat(" ", 14), `1e3` + strings.Repeat(" ", 13), `  1.5` + strings.Repeat(" ", 12),
		`1e3` + strings.Repeat("x", 14), `true`, `null`, `{"expiration": `,
	}
}

// escapedHighSurrogate finds the escape of a surrogate pair's first half,
// which Go's decoder reads with the second as the one character and
// json_spirit encodes alone.
var escapedHighSurrogate = regexp.MustCompile(`\\u[dD][89abAB][0-9a-fA-F]{2}`)

// readerDump is n's kind and get_data text, and its children's, in the
// order JSONObj walks them.
func readerDump(n *node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d%q[", n.kind, n.data())
	for _, c := range n.children() {
		fmt.Fprintf(&b, "%q:%s,", c.name, readerDump(c.node))
	}
	b.WriteString("]")
	return b.String()
}

// sharedDump is readerDump of a jsonspirit node.
func sharedDump(n *jsonspirit.Node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d%q[", n.Kind(), n.Data())
	for _, c := range n.Children() {
		fmt.Fprintf(&b, "%q:%s,", c.Name, sharedDump(c.Node))
	}
	b.WriteString("]")
	return b.String()
}

// FuzzJSONSpirit holds this package's reader, Go's decoder and its checks,
// to internal/jsonspirit, which is to replace it. jsonspirit must take every
// document the reader and JSONParser's length check take, given the NUL
// get_policy appends, and read it alike: the same get_data texts and the
// same children in JSONObj's order, but for an escaped surrogate pair, which
// Go's decoder reads as the one character and json_spirit as two. What
// jsonspirit takes beyond the reader is json_spirit's grammar.
func FuzzJSONSpirit(f *testing.F) {
	for _, seed := range spiritSeeds() {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		old, err := readDocument(doc)
		if err != nil || old.kind == kindScalar && len(old.text) != len(doc)+1 {
			return
		}
		n, ok, err := jsonspirit.Parse(string(doc) + "\x00")
		if !ok {
			t.Fatalf("%q: the reader takes it, jsonspirit refuses it: %v", doc, err)
		}
		if escapedHighSurrogate.Match(doc) {
			return
		}
		if got, want := sharedDump(n), readerDump(old); got != want {
			t.Fatalf("%q: jsonspirit reads %s, the reader %s", doc, got, want)
		}
	})
}
