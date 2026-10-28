package meta

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/jsonspirit"
)

// spiritSeeds are the tier config values this package's specs read, and
// json_spirit's grammar cases.
var spiritSeeds = []string{
	"[1.5]", "[0.3]", "[1e2]", "[1e23]", "[-0]", "[007,+5]", "[18446744073709551615]", "[-9223372036854775809]",
	"1.5", "-0", "42", `"a\tb"`, "http://host:80", "[1,,2,]", "[true] trailing", "[\"caf\xe9\"]", `["\ud83dé"]`,
	`["a\qb"]`, `{"b": [1, "x", {"c": null}], "a": false}`, `["\x41"]`, `["\101"]`,
	"[" + strings.Repeat("1", 301) + ".0]", "[1e301]", "[1e99999999999]", "[1e2147483648]", "[1e-99999999999]",
	"[1e-2147483649]", "[1e126]", "[1000000000.0e300]", "[0." + strings.Repeat("1", 301) + "]",
	`{"b": 1, "a": null, "b": true}`, `{"a": 1,, "b": 2,}`, "\f[\v1\t,\r2\n]", "[\"a\x01\x00b\"]",
	`["\u0g41"]`, `["ab\u12"]`, `["😀"]`, `[+5, 007, -0, .5, 5., -.5, +1.5e1, 1E+2, 25e-1]`,
	`"x"`, `042`, `true`, `nullx`, "1e3" + strings.Repeat(" ", 15), ``, `{"a" 1}`, `[truex]`, `[1e]`, `{,}`, `[,]`,
	strings.Repeat("[", 64) + strings.Repeat("]", 64),
}

// longRun finds a run of digits longer than this package's transcription
// reads.
var longRun = regexp.MustCompile(`[0-9]{301}`)

// spiritDump is n's kind and get_data text, and its children's, in the order
// JSONObj walks them.
func spiritDump(n spiritNode) string {
	switch n.kind {
	case 's':
		return fmt.Sprintf("s%q", n.text)
	case 'v':
		return fmt.Sprintf("v%q", n.text)
	case 'a':
		var b strings.Builder
		for _, c := range n.arr {
			b.WriteString(spiritDump(c) + ",")
		}
		return "a[" + b.String() + "]"
	}
	var b strings.Builder
	// applyJSON's order, JSONObj's multimap by name.
	order := make([]int, len(n.keys))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(n.keys[a], n.keys[b]) })
	for _, i := range order {
		b.WriteString(fmt.Sprintf("%q:", n.keys[i]) + spiritDump(n.values[i]) + ",")
	}
	return "o{" + b.String() + "}"
}

// sharedDump is spiritDump of a jsonspirit node.
func sharedDump(n *jsonspirit.Node) string {
	switch n.Kind() {
	case jsonspirit.String:
		return fmt.Sprintf("s%q", n.Data())
	case jsonspirit.Scalar:
		return fmt.Sprintf("v%q", n.Data())
	case jsonspirit.Array:
		var b strings.Builder
		for _, c := range n.Children() {
			b.WriteString(sharedDump(c.Node) + ",")
		}
		return "a[" + b.String() + "]"
	}
	var b strings.Builder
	for _, c := range n.Children() {
		b.WriteString(fmt.Sprintf("%q:", c.Name) + sharedDump(c.Node) + ",")
	}
	return "o{" + b.String() + "}"
}

// FuzzJSONSpirit holds this package's json_spirit transcription to
// internal/jsonspirit, which is to replace it. The two differ by design in
// two places, which it passes over: a real whose integer part has more than
// 300 digits, which jsonspirit accumulates until it passes DBL_MAX, and
// nesting past jsonspirit.MaxDepth, which jsonspirit refuses. Elsewhere they
// must give the same answer, the same tree in the order JSONObj walks it, and
// the same refusal, whose text jsonspirit cuts at a 40-digit exponent.
func FuzzJSONSpirit(f *testing.F) {
	for _, s := range spiritSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, val string) {
		old, oldOK, oldErr := parseJSON(val)
		n, ok, err := jsonspirit.Parse(val)
		if longRun.MatchString(val) || strings.Count(val, "[")+strings.Count(val, "{") > jsonspirit.MaxDepth {
			return
		}
		switch {
		case (oldErr == nil) != (err == nil):
			t.Fatalf("%q: jsonspirit refuses with %v, this package with %v", val, err, oldErr)
		case err != nil:
			want := fmt.Sprintf("%v: legacy tier config value %q: %v", denc.ErrMalformed, val, err)
			if !strings.HasPrefix(oldErr.Error(), want) {
				t.Fatalf("%q: jsonspirit refuses with %q, this package with %q", val, want, oldErr)
			}
		case ok != oldOK:
			t.Fatalf("%q: jsonspirit reads it %t, this package %t", val, ok, oldOK)
		case ok:
			if got, want := sharedDump(n), spiritDump(old); got != want {
				t.Fatalf("%q: jsonspirit reads %s, this package %s", val, got, want)
			}
		}
	})
}
