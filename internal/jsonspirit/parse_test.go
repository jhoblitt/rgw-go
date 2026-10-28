package jsonspirit_test

import (
	"runtime/debug"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/jsonspirit"
)

// u is the JSON escape of the UTF-16 code unit whose hex digits are h,
// spelled out so the spec's source holds the escape and not the character.
func u(h string) string { return `\` + "u" + h }

// read parses buf, failing the spec unless JSONParser takes it.
func read(buf string) *jsonspirit.Node {
	GinkgoHelper()
	n, ok, err := jsonspirit.Parse(buf)
	Expect(err).NotTo(HaveOccurred(), "%q", buf)
	Expect(ok).To(BeTrue(), "%q", buf)
	return n
}

// nested is depth arrays, one inside the next.
func nested(depth int) string { return strings.Repeat("[", depth) + strings.Repeat("]", depth) }

var _ = Describe("Parse", func() {
	DescribeTable("reads what JSONParser reads, get_data as json_spirit writes it",
		func(buf string, kind jsonspirit.Kind, data string) {
			n := read(buf)
			Expect(n.Kind()).To(Equal(kind), "%q", buf)
			Expect(n.Data()).To(Equal(data), "%q", buf)
		},
		Entry("an object, its members in document order and duplicates kept",
			`{"b": 1, "a": null, "b": true}`, jsonspirit.Object, `{"b":1,"a":null,"b":true}`),
		Entry("an array, without white space", `[ "a" , 1, 2.5, [], {} ]`, jsonspirit.Array, `["a",1,2.5000000000000000,[],{}]`),
		Entry("stray commas in an array", `[1,,2,]`, jsonspirit.Array, `[1,2]`),
		Entry("stray commas in an object", `{"a": 1,, "b": 2,}`, jsonspirit.Object, `{"a":1,"b":2}`),
		Entry("C white space between tokens, a form feed and a vertical tab among it",
			"\f[\v1\t,\r2\n]", jsonspirit.Array, `[1,2]`),
		Entry("white space before the value", "  [1]", jsonspirit.Array, `[1]`),
		Entry("whatever follows the first value, unread", "[true] trailing { \xff", jsonspirit.Array, `[true]`),
		Entry("a raw control byte in a string", "[\"a\x01b\"]", jsonspirit.Array, `["a`+u("0001")+`b"]`),
		Entry("a raw NUL in a string", "[\"a\x00b\"]", jsonspirit.Array, `["a`+u("0000")+`b"]`),
		Entry("a byte that is not UTF-8 in a string", "[\"caf\xe9\"]", jsonspirit.Array, `["caf`+u("00E9")+`"]`),
		Entry("the JSON escapes", `["\"\\\/\b\f\n\r\t"]`, jsonspirit.Array, `["\"\\/\b\f\n\r\t"]`),
		Entry("an escape json_spirit does not know, dropped with its backslash", `["a\qb"]`, jsonspirit.Array, `["ab"]`),
		Entry("a u escape encoded by encode_utf8, each byte written alone",
			`["`+u("00e9")+`"]`, jsonspirit.Array, `["`+u("00C3")+u("00A9")+`"]`),
		Entry("a u escape whose digits are not hex, each read as 0", `["`+u("0g41")+`"]`, jsonspirit.Array, `["A"]`),
		Entry("a u escape cut short by the string's end, dropped", `["ab`+`\`+`u12"]`, jsonspirit.Array, `["ab12"]`),
		Entry("an escaped surrogate pair, each half encoded alone",
			`["`+u("d83d")+u("de00")+`"]`, jsonspirit.Array, `["`+u("00ED")+u("00A0")+u("00BD")+u("00ED")+u("00B8")+u("0080")+`"]`),
		Entry("a lone low surrogate", `["`+u("de00")+`x"]`, jsonspirit.Array, `["`+u("00ED")+u("00B8")+u("0080")+`x"]`),
		Entry("signs, leading zeros and a dot at either end",
			`[+5, 007, -0, .5, 5., -.5, +1.5e1, 1E+2, 25e-1]`, jsonspirit.Array,
			`[5,7,0,0.50000000000000000,5.0000000000000000,-0.50000000000000000,15.000000000000000,100.00000000000000,2.5000000000000000]`),
		Entry("0.3 as boost's parser builds it, three times pow(10, -1)", `[0.3]`, jsonspirit.Array, `[0.30000000000000004]`),
		Entry("1e23, whose glibc pow is the double above the nearest", `[1e23]`, jsonspirit.Array, `[1.0000000000000001e+23]`),
		Entry("the least int64", `[-9223372036854775808]`, jsonspirit.Array, `[-9223372036854775808]`),
		Entry("an integer beyond int64, as uint64_p reads it", `[18446744073709551615]`, jsonspirit.Array, `[18446744073709551615]`),
		Entry("a real whose integer part has 301 digits, each added as a double",
			"[1"+strings.Repeat("0", 300)+".0]", jsonspirit.Array, `[1.0000000000000002e+300]`),
		Entry("a real whose integer part has 308 digits, the most a double holds",
			"["+strings.Repeat("9", 308)+".0]", jsonspirit.Array, `[9.9999999999999981e+307]`),
		Entry("a lone string", `"x"`, jsonspirit.String, "x"),
		Entry("a lone integer whose text is as long as the buffer", `42`, jsonspirit.Scalar, "42"),
		Entry("a lone true", `true`, jsonspirit.Scalar, "true"),
		Entry("a lone real whose text is as long as the buffer", "1e3"+strings.Repeat(" ", 15), jsonspirit.Scalar, "1000.0000000000000"),
	)

	DescribeTable("fails where JSONParser fails",
		func(buf string) {
			n, ok, err := jsonspirit.Parse(buf)
			Expect(err).NotTo(HaveOccurred(), "%q", buf)
			Expect(ok).To(BeFalse(), "%q", buf)
			Expect(n).To(BeNil(), "%q", buf)
		},
		Entry("an empty buffer", ``),
		Entry("white space alone", " \t"),
		Entry("a word", `x`),
		Entry("an unclosed object", `{`),
		Entry("an unclosed array", `[1`),
		Entry("a member without a colon", `{"a" 1}`),
		Entry("a member without a value", `{"a":}`),
		Entry("members without a comma", `{"a": 1 "b": 2}`),
		Entry("elements without a comma", `[1 2]`),
		Entry("a comma alone in an object", `{,}`),
		Entry("a comma alone in an array", `[,]`),
		Entry("a member named by a number", `{5: 1}`),
		Entry("an unterminated string", `"abc`),
		Entry("a backslash at the end of the buffer", `"abc\`),
		Entry("a sign alone", `[-]`),
		Entry("an exponent without digits, after which int64_p leaves the e", `[1e]`),
		Entry("an integer beyond uint64, which neither int64_p nor uint64_p takes", `[18446744073709551616]`),
		Entry("a negative integer beyond int64", `[-9223372036854775809]`),
		Entry("a real whose integer part passes DBL_MAX at its 309th digit", "["+strings.Repeat("9", 309)+".0]"),
		Entry("a literal's prefix", `[truex]`),
		Entry("a lone null and text json_spirit leaves unread", `nullx`),
		Entry("a lone integer whose text is shorter than the buffer", `042`),
		Entry("a lone real whose text is longer than the buffer", `1e3`),
	)

	DescribeTable("refuses what it does not read as json_spirit does",
		func(buf, why string) {
			n, ok, err := jsonspirit.Parse(buf)
			Expect(err).To(MatchError(why), "%q", buf)
			Expect(ok).To(BeFalse(), "%q", buf)
			Expect(n).To(BeNil(), "%q", buf)
		},
		Entry("a \\x escape", `["\x41"]`, `\x escape`),
		Entry("a \\X escape", `["\X41"]`, `\X escape`),
		Entry("an octal escape", `["\101"]`, `\1 escape`),
		Entry("a \\x escape in a string that never ends", `["\x41`, `\x escape`),
		Entry("a fraction of 301 digits", "[0."+strings.Repeat("1", 301)+"]", "a run of 301 digits"),
		Entry("an exponent past 300", `[1e301]`, "exponent of magnitude 301"),
		Entry("a negative exponent past 300", `[1e-301]`, "exponent of magnitude 301"),
		Entry("an exponent beyond an int", `[1e99999999999]`, "exponent of magnitude 99999999999"),
		Entry("an exponent of 50 digits, 40 of them named", "[1e"+strings.Repeat("9", 50)+"]", "exponent of magnitude "+strings.Repeat("9", 40)),
		Entry("exponent 126, whose glibc pow depends on the CPU", `[1e126]`, "exponent 126, whose glibc pow depends on the host CPU"),
		Entry("a real that overflows", `[1000000000.0e300]`, "a non-finite number"),
		Entry("a refused form in a document that would fail anyway", `[1e301`, "exponent of magnitude 301"),
		Entry("nesting 10001 levels deep", nested(10001), "arrays and objects nested deeper than 10000"),
		Entry("objects nested 10001 levels deep", strings.Repeat(`{"a":`, 10000)+`{}`+strings.Repeat("}", 10000),
			"arrays and objects nested deeper than 10000"),
	)

	It("reads arrays and objects nested 10000 levels deep", func() {
		Expect(read(nested(10000)).Data()).To(Equal(nested(10000)))
		obj := strings.Repeat(`{"a":`, 9999) + `{}` + strings.Repeat("}", 9999)
		Expect(read(obj).Data()).To(Equal(obj))
	})

	// json_spirit recurses once per level, as Parse does, so an unbounded
	// reader of 4 MiB of brackets would need hundreds of megabytes of stack.
	It("refuses 4 MiB of brackets without its stack growing past a fixed bound", func() {
		prev := debug.SetMaxStack(64 << 20)
		DeferCleanup(func() { debug.SetMaxStack(prev) })
		_, ok, err := jsonspirit.Parse(strings.Repeat("[", 4<<20))
		Expect(err).To(MatchError("arrays and objects nested deeper than 10000"))
		Expect(ok).To(BeFalse())
		Expect(read(nested(10000)).Kind()).To(Equal(jsonspirit.Array), "the bound leaves room for the deepest nesting read")
	})
})

var _ = Describe("Node", func() {
	It("walks an object's children by name, equal names in document order", func() {
		n := read(`{"b": 1, "a": 2, "b": 3, "": 4}`)
		var got []string
		for _, c := range n.Children() {
			got = append(got, c.Name+"="+c.Node.Data())
		}
		Expect(got).To(Equal([]string{"=4", "a=2", "b=1", "b=3"}))
	})

	It("walks an array's children in document order, each named \"\"", func() {
		n := read(`["b", "a", ["c"]]`)
		var got []string
		for _, c := range n.Children() {
			got = append(got, c.Name+"="+c.Node.Data())
		}
		Expect(got).To(Equal([]string{"=b", "=a", `=["c"]`}))
	})

	It("gives a string or a scalar no children", func() {
		Expect(read(`"x"`).Children()).To(BeEmpty())
		Expect(read(`[1]`).Children()[0].Node.Children()).To(BeEmpty())
	})

	DescribeTable("finds the child find_first(name) points at",
		func(buf, name string, want any) {
			got := read(buf).Find(name)
			if want == nil {
				Expect(got).To(BeNil(), "%q in %q", name, buf)
				return
			}
			Expect(got).NotTo(BeNil(), "%q in %q", name, buf)
			Expect(got.Data()).To(Equal(want), "%q in %q", name, buf)
		},
		Entry("the first of equal names", `{"b": 1, "a": 2, "b": 3}`, "b", "1"),
		Entry("a member's string, its contents", `{"a": "x\ny"}`, "a", "x\ny"),
		Entry("a name no member has", `{"b": 1}`, "a", nil),
		Entry("an array's first element, under the name its elements share", `["x", "y"]`, "", "x"),
		Entry("any other name in an array", `["x"]`, "x", nil),
		Entry("a name in an empty array", `[]`, "", nil),
		Entry("a name in a string", `"x"`, "", nil),
	)

	It("tells a string from a scalar written alike", func() {
		n := read(`["1", 1]`)
		Expect(n.Children()[0].Node.Kind()).To(Equal(jsonspirit.String))
		Expect(n.Children()[1].Node.Kind()).To(Equal(jsonspirit.Scalar))
		Expect(n.Data()).To(Equal(`["1",1]`))
	})
})
