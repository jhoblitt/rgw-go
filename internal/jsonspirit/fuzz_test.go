package jsonspirit_test

import (
	"strings"
	"testing"

	"github.com/jhoblitt/rgw-go/internal/jsonspirit"
)

// fuzzSeeds are json_spirit's grammar cases: stray commas, C white space,
// raw control and non-UTF-8 bytes, dropped, cut short and non-hex escapes,
// signs, leading zeros and bare dots, each half of a surrogate pair alone,
// the forms Parse refuses, and the lone scalars JSONParser measures.
func fuzzSeeds() []string {
	return []string{
		`{"b": 1, "a": null, "b": true, "c": [1, 2.5, "x", {}]}`,
		`[1,,2,]`, `{"a": 1,, "b": 2,}`, "\f[\v1\t,\r2\n]", "[\"a\x01\x00\xffb\"]",
		`["a\qb"]`, `["\u0g41"]`, `["ab\u12"]`, `["😀"]`, `["\ude00x"]`,
		`[+5, 007, -0, .5, 5., -.5, +1.5e1, 1E+2, 25e-1, 0.3, 1e23]`,
		`[18446744073709551615, -9223372036854775808, 18446744073709551616]`,
		"[1" + strings.Repeat("0", 300) + ".0]", "[" + strings.Repeat("9", 309) + ".0]",
		`["\x41"]`, `["\101"]`, `[1e301]`, `[1e126]`, `[1000000000.0e300]`, "[0." + strings.Repeat("1", 301) + "]",
		`"x"`, `42`, `042`, `true`, `nullx`, "1e3" + strings.Repeat(" ", 15), ``, `{"a" 1}`, `[truex]`,
		"[true] trailing { \xff", strings.Repeat("[", 64) + strings.Repeat("]", 64),
	}
}

// FuzzParse reads arbitrary buffers. A read must not panic and must answer
// the same twice; ok, a root and no error come together; and every node's
// get_data text and children must be there to walk.
func FuzzParse(f *testing.F) {
	for _, seed := range fuzzSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, buf string) {
		n, ok, err := jsonspirit.Parse(buf)
		again, ok2, err2 := jsonspirit.Parse(buf)
		if ok != ok2 || (err == nil) != (err2 == nil) || err != nil && err.Error() != err2.Error() {
			t.Fatalf("%q reads as %t, %v and then as %t, %v", buf, ok, err, ok2, err2)
		}
		if ok != (n != nil) || ok && err != nil {
			t.Fatalf("%q reads as %v, %t, %v", buf, n, ok, err)
		}
		if !ok {
			return
		}
		if d, d2 := n.Data(), again.Data(); d != d2 {
			t.Fatalf("%q reads as %q and then as %q", buf, d, d2)
		}
		var walk func(*jsonspirit.Node, int)
		walk = func(n *jsonspirit.Node, depth int) {
			if depth > jsonspirit.MaxDepth {
				t.Fatalf("%q reads deeper than %d", buf, jsonspirit.MaxDepth)
			}
			for _, c := range n.Children() {
				walk(c.Node, depth+1)
			}
		}
		walk(n, 0)
	})
}
