package postpolicy_test

import (
	"math"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/postpolicy"
)

var _ = Describe("Parse", func() {
	It("reads s3-tests' base policy", func() {
		p := mustParse(policyDoc(future, baseConds...))
		Expect([]int64{p.MinLength, p.MaxLength}).To(Equal([]int64{0, 1024}))
	})

	DescribeTable("refuses a document with radosgw's message, as 400 InvalidArgument",
		func(doc, want string) {
			Expect(parseRefusal([]byte(doc))).To(Equal(want))
		},
		Entry("one that is not JSON", `{"expiration": `, "Malformed JSON"),
		Entry("an empty one", ``, "Malformed JSON"),
		Entry("a lone integer, shorter than the document and its NUL", `5`, "Malformed JSON"),
		Entry("a lone integer padded to its length and the NUL, which an integer's text never fills",
			`5 `, "Malformed JSON"),
		Entry("a lone real, whose text is not as long as the document and its NUL", `1e3`, "Malformed JSON"),
		Entry("a lone real padded to its text's length with the NUL, which JSONParser takes",
			`1e3`+strings.Repeat(" ", 14), "Policy missing expiration"),
		Entry("a lone real padded before and after", "  1.5"+strings.Repeat(" ", 12), "Policy missing expiration"),
		Entry("a lone real followed by text json_spirit leaves unread", `1e3`+strings.Repeat("x", 14), "Policy missing expiration"),
		Entry("a lone real padded one byte short", `1e3`+strings.Repeat(" ", 13), "Malformed JSON"),
		Entry("a lone real padded one byte long", `1e3`+strings.Repeat(" ", 15), "Malformed JSON"),
		Entry("a lone true", `true`, "Malformed JSON"),
		Entry("a lone null", `null`, "Malformed JSON"),
		Entry("a lone string, which json_spirit reads", `"x"`, "Policy missing expiration"),
		Entry("an array, whose members have no name", `[{"expiration": "`+future+`", "conditions": []}]`, "Policy missing expiration"),
		Entry("no expiration (test_post_object_missing_expires_condition)",
			`{"conditions": [{"bucket": "bkt"}]}`, "Policy missing expiration"),
		Entry("EXPIRATION, as names are matched exactly (test_post_object_expires_is_case_sensitive)",
			`{"EXPIRATION": "`+future+`", "conditions": []}`, "Policy missing expiration"),
		Entry("an expiration str(datetime) writes (test_post_object_invalid_date_format)",
			string(policyDoc("2029-12-31 00:00:00.000000+00:00", baseConds...)), "Failed to parse policy expiration"),
		Entry("an expiration without a time",
			string(policyDoc("2030-01-01", baseConds...)), "Failed to parse policy expiration"),
		Entry("an expiration that is a number", `{"expiration": 2030, "conditions": []}`, "Failed to parse policy expiration"),
		Entry("an expiration with a fraction stringtoul refuses",
			string(policyDoc("2030-01-01T00:00:00.5xZ")), "Failed to parse policy expiration"),
		Entry("no conditions (test_post_object_missing_conditions_list)",
			`{"expiration": "`+future+`"}`, "Policy missing conditions"),
		Entry("CONDITIONS (test_post_object_condition_is_case_sensitive)",
			`{"expiration": "`+future+`", "CONDITIONS": []}`, "Policy missing conditions"),
		Entry("a two-element condition (test_post_object_missing_content_length_argument)",
			string(policyDoc(future, `["content-length-range", 0]`)), "Bad condition array, expecting 3 arguments"),
		Entry("a four-element condition",
			string(policyDoc(future, `["eq", "$key", "a", "b"]`)), "Bad condition array, expecting 3 arguments"),
		Entry("an empty condition array",
			string(policyDoc(future, `[]`)), "Bad condition array, expecting 3 arguments"),
		Entry("an operator other than eq, starts-with and content-length-range",
			string(policyDoc(future, `["ne", "$key", "a"]`)), "Invalid condition: ne"),
		Entry("an operator equal to starts-with only under Unicode case folding",
			string(policyDoc(future, "[\"\xc5\xbftarts-with\", \"$key\", \"a\"]")), "Invalid condition: \xc5\xbftarts-with"),
		Entry("an operator that is not a string, named by its JSON text",
			string(policyDoc(future, `[1.5, "$key", "a"]`)), "Invalid condition: 1.5000000000000000"),
		Entry("a length bound that is not a number",
			string(policyDoc(future, `["content-length-range", "a", 5]`)), "Bad content-length-range param"),
		Entry("a length bound with trailing text",
			string(policyDoc(future, `["content-length-range", 0, "5 "]`)), "Bad content-length-range param"),
		Entry("LLONG_MAX, which stringtoll refuses",
			string(policyDoc(future, `["content-length-range", 0, 9223372036854775807]`)), "Bad content-length-range param"),
		Entry("a real bound, written with a decimal point",
			string(policyDoc(future, `["content-length-range", 0, 1024.0]`)), "Bad content-length-range param"),
		Entry("an exponent bound, which json_spirit reads as a real",
			string(policyDoc(future, `["content-length-range", 0, 1e3]`)), "Bad content-length-range param"),
		Entry("a boolean bound", string(policyDoc(future, `["content-length-range", 0, true]`)), "Bad content-length-range param"),
		Entry("an empty object condition (test_post_object_empty_conditions)",
			string(policyDoc(future, `{ }`)), "Failed to parse policy"),
		Entry("a string condition", string(policyDoc(future, `"bucket"`)), "Failed to parse policy"),
		Entry("a number condition", string(policyDoc(future, `5`)), "Failed to parse policy"),
		Entry("a \\x escape, which json_spirit takes and rgw-go does not transcribe",
			string(policyDoc(future, `{"bucket": "\x41"}`)), "Malformed JSON"),
		Entry("an octal escape, which json_spirit takes and rgw-go does not transcribe",
			string(policyDoc(future, `{"bucket": "`+`\`+`101"}`)), "Malformed JSON"),
		Entry("an integer beyond uint64, which neither int64_p nor uint64_p takes",
			string(policyDoc(future, `["eq", "$x", 18446744073709551616]`)), "Malformed JSON"),
		Entry("a negative integer beyond int64", string(policyDoc(future, `["eq", "$x", -9223372036854775809]`)), "Malformed JSON"),
		Entry("an exponent beyond 300, whose value json_spirit takes from the host's libm",
			string(policyDoc(future, `["eq", "$x", 1e301]`)), "Malformed JSON"),
		Entry("exponent 126, whose glibc power depends on the CPU",
			string(policyDoc(future, `["eq", "$x", 1e126]`)), "Malformed JSON"),
		Entry("a fraction of more than 300 digits", string(policyDoc(future, `["eq", "$x", 0.`+strings.Repeat("1", 301)+`]`)), "Malformed JSON"),
		Entry("a real that overflows", string(policyDoc(future, `["eq", "$x", 1`+strings.Repeat("0", 20)+`e300]`)), "Malformed JSON"),
		Entry("nesting 10001 levels deep",
			string(policyDoc(future, `["eq", "$x", `+strings.Repeat("[", 9998)+strings.Repeat("]", 9998)+`]`)), "Malformed JSON"),
		Entry("4 MiB of brackets", strings.Repeat("[", 4<<20), "Malformed JSON"),
		Entry("a real whose integer part passes DBL_MAX, which strict_real_p does not match",
			string(policyDoc(future, `["eq", "$x", `+strings.Repeat("9", 309)+`.0]`)), "Malformed JSON"),
	)

	DescribeTable("reads a document as json_spirit reads it where RFC 8259 refuses it",
		func(doc, bucket, want string) {
			Expect(checkRefusal(mustParse([]byte(doc)), v2Env(bucket), now)).To(Equal(want))
		},
		Entry("a stray comma", string(policyDoc(future, `{"bucket": "bkt"},`)), "bkt", ""),
		Entry("stray commas between members", `{"expiration": "`+future+`",, "conditions": [{"bucket": "bkt"}],}`, "bkt", ""),
		Entry("a form feed between tokens",
			"{\f\"expiration\": \""+future+"\", \"conditions\": []}", "bkt", "Policy missing condition: bucket"),
		Entry("a control byte in a string", string(policyDoc(future, "{\"bucket\": \"b\x01\"}")), "b\x01", ""),
		Entry("a u escape whose digits are not hex, each read as 0",
			string(policyDoc(future, `{"bucket": "`+u("00zz")+`"}`)), "\x00", ""),
		Entry("an escape json_spirit drops with its backslash", string(policyDoc(future, `{"bucket": "b\qkt"}`)), "bkt", ""),
		Entry("bytes that are not UTF-8 in a string, kept as they are",
			string(policyDoc(future, "{\"bucket\": \"b\xff\"}")), "b\xff", ""),
		Entry("bytes that are not UTF-8 in a member name",
			string(policyDoc(future, "{\"b\xc3\": \"bkt\"}")), "bkt", "Policy check failed, variable not found: b\xc3"),
		Entry("a lone high surrogate escape, as the three bytes encode_utf8 writes",
			string(policyDoc(future, `{"bucket": "`+u("d83d")+`"}`)), "\xed\xa0\xbd", ""),
		Entry("a lone low surrogate escape", string(policyDoc(future, `{"bucket": "`+u("de00")+`x"}`)), "\xed\xb8\x80x", ""),
		Entry("a high surrogate escape before a non-surrogate escape",
			string(policyDoc(future, `{"bucket": "`+u("d83d")+u("0041")+`"}`)), "\xed\xa0\xbdA", ""),
	)

	It("ignores what follows the document's first value, as json_spirit does", func() {
		doc := string(policyDoc(future, baseConds...)) + "} trailing [ text \xff"
		Expect(mustParse([]byte(doc)).MaxLength).To(Equal(int64(1024)))
	})

	It("reads nesting as deep as 10000 levels", func() {
		deep := strings.Repeat("[", 9997) + strings.Repeat("]", 9997)
		p := mustParse(policyDoc(future, `["eq", "$x", `+deep+`]`))
		Expect(checkRefusal(p, &postpolicy.Env{}, now)).To(HavePrefix("Policy condition failed: eq: $x, [[["))
	})

	DescribeTable("content-length-range only tightens the bounds",
		func(min, max int64, conds ...string) {
			p := mustParse(policyDoc(future, conds...))
			Expect([]int64{p.MinLength, p.MaxLength}).To(Equal([]int64{min, max}))
		},
		Entry("none leaves 0 and LLONG_MAX", int64(0), int64(math.MaxInt64), `["eq", "$key", "k"]`),
		Entry("0, 0 (test_post_object_upload_size_limit_exceeded)", int64(0), int64(0), `["content-length-range", 0, 0]`),
		Entry("a negative minimum leaves 0 (test_post_object_invalid_content_length_argument)",
			int64(0), int64(0), `["content-length-range", -1, 0]`),
		Entry("512, 1000 (test_post_object_upload_size_below_minimum)", int64(512), int64(1000), `["content-length-range", 512, 1000]`),
		Entry("4 MiB to 12 MiB (test_post_object_upload_size_rgw_chunk_size_bug)",
			int64(4<<20), int64(12<<20), `["content-length-range", 4194304, 12582912]`),
		Entry("the larger minimum and the smaller maximum of two, a looser one after", int64(20), int64(50),
			`["content-length-range", 20, 50]`, `["content-length-range", 10, 100]`),
		Entry("the larger minimum and the smaller maximum of two, a tighter one after", int64(20), int64(50),
			`["content-length-range", 10, 100]`, `["content-length-range", 20, 50]`),
		Entry("a maximum below the minimum, unchecked", int64(10), int64(5), `["content-length-range", 10, 5]`),
		Entry("strings strtoll reads, white space and sign included", int64(5), int64(10),
			`["content-length-range", "+5", " 10"]`),
		Entry("numbers with a plus sign and leading zeros, as int64_p reads them", int64(1), int64(10),
			`["content-length-range", +1, 010]`),
		Entry("an empty string, which strtoll reads as 0", int64(0), int64(0), `["content-length-range", "", ""]`),
		Entry("a bound cut at a NUL, as stringtoll reads a C string", int64(0), int64(10),
			`["content-length-range", 0, "10`+u("0000")+`x"]`),
		Entry("any case of the operator", int64(1), int64(2), `["Content-Length-RANGE", 1, 2]`),
		Entry("the most negative bound", int64(0), int64(math.MinInt64), `["content-length-range", 0, -9223372036854775808]`),
	)

	It("reads an operator as a C string, case folded", func() {
		p := mustParse(policyDoc(future, `["EQ`+u("0000")+`junk", "$key", "foo.txt"]`))
		Expect(checkRefusal(p, v2Env("bkt", "key", "bar"), now)).To(Equal("Policy condition failed: eq: $key, foo.txt"))
	})

	It("reads an expiration as a C string", func() {
		p := mustParse([]byte(`{"expiration": "` + future + u("0000") + `junk", "conditions": []}`))
		Expect(checkRefusal(p, &postpolicy.Env{}, now)).To(BeEmpty())
	})

	DescribeTable("takes an expiration parse_iso8601 takes",
		func(expiration string) {
			p := mustParse([]byte(`{"expiration": "` + expiration + `", "conditions": []}`))
			Expect(checkRefusal(p, &postpolicy.Env{}, now)).To(BeEmpty())
			Expect(checkRefusal(p, &postpolicy.Env{}, now.AddDate(0, 0, 2))).To(Equal("Policy expired"))
		},
		Entry("with Z", "2030-01-01T00:00:00Z"),
		Entry("without a zone", "2030-01-01T00:00:00"),
		Entry("with a space for T", "2030-01-01 00:00:00Z"),
		Entry("with milliseconds", "2030-01-01T00:00:00.000Z"),
		Entry("with an empty fraction", "2030-01-01T00:00:00.Z"),
		Entry("with white space around the zone", `2030-01-01T00:00:00 Z \t`),
	)
})
