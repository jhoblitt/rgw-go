package gate_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// canonical decodes JSON into maps, slices, strings, bools, nil and
// json.Numbers, so two documents compare equal exactly when they hold the
// same values of the same JSON types whatever their key order and whitespace.
func canonical(b []byte) any {
	GinkgoHelper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	Expect(d.Decode(&v)).To(Succeed(), "decoding %s", b)
	return v
}

// jsonType names a canonical value's JSON type.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// jsonDiff lists the paths at which two canonical documents differ, in type
// or in value. Numbers compare by their text, as both sides print integers.
func jsonDiff(path string, got, want any) []string {
	mismatch := []string{fmt.Sprintf("%s: got %s %v, want %s %v", path, jsonType(got), got, jsonType(want), want)}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return mismatch
		}
		var out []string
		for _, k := range slices.Sorted(maps.Keys(w)) {
			if _, ok := g[k]; !ok {
				out = append(out, fmt.Sprintf("%s.%s: missing, radosgw-admin has %v", path, k, w[k]))
				continue
			}
			out = append(out, jsonDiff(path+"."+k, g[k], w[k])...)
		}
		for _, k := range slices.Sorted(maps.Keys(g)) {
			if _, ok := w[k]; !ok {
				out = append(out, fmt.Sprintf("%s.%s: extra, rgw-go has %v", path, k, g[k]))
			}
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return mismatch
		}
		if len(g) != len(w) {
			return []string{fmt.Sprintf("%s: got %d elements %v, want %d %v", path, len(g), got, len(w), want)}
		}
		var out []string
		for i := range w {
			out = append(out, jsonDiff(fmt.Sprintf("%s[%d]", path, i), g[i], w[i])...)
		}
		return out
	default:
		if jsonType(got) != jsonType(want) {
			return mismatch
		}
		if got != want {
			return []string{fmt.Sprintf("%s: got %v, want %v", path, got, want)}
		}
		return nil
	}
}

var _ = Describe("jsonDiff", func() {
	diff := func(got, want string) []string {
		return jsonDiff("$", canonical([]byte(got)), canonical([]byte(want)))
	}

	It("finds nothing between equal documents in any key order", func() {
		Expect(diff(`{"a":1,"b":[true,null,"x",{"c":-1}]}`, `{"b":[true,null,"x",{"c":-1}],"a":1}`)).To(BeEmpty())
	})

	It("reports a value of another JSON type that prints the same", func() {
		Expect(diff(`{"n":"1"}`, `{"n":1}`)).To(ConsistOf(ContainSubstring("$.n: got string 1, want number 1")))
		Expect(diff(`{"b":"true"}`, `{"b":true}`)).To(ConsistOf(ContainSubstring("$.b: got string true, want bool true")))
		Expect(diff(`{"z":"null"}`, `{"z":null}`)).To(ConsistOf(ContainSubstring("want null")))
		Expect(diff(`{"o":[]}`, `{"o":{}}`)).To(ConsistOf(ContainSubstring("got array [], want object")))
	})

	It("reports differing values, missing and extra keys, and array lengths", func() {
		Expect(diff(`{"a":1,"x":0}`, `{"a":2,"m":0}`)).To(ConsistOf(
			ContainSubstring("$.a: got 1, want 2"),
			ContainSubstring("$.m: missing"),
			ContainSubstring("$.x: extra"),
		))
		Expect(diff(`[1]`, `[1,2]`)).To(ConsistOf(ContainSubstring("got 1 elements")))
	})
})
