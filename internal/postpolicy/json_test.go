package postpolicy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/postpolicy"
)

// These specs pin how ceph's JSONParser reads a policy: json_spirit keeps an
// object's members in document order, duplicates included, JSONObj files them
// in a std::multimap by name, which find() answers with the first of equal
// names and find_first() walks in name order, and get_data() is a string's
// contents or json_spirit's text for any other value.
var _ = Describe("Reading the policy's JSON", func() {
	Context("with duplicate names", func() {
		It("takes the first expiration, where Go's Unmarshal keeps the last", func() {
			doc := `{"expiration": "` + future + `", "expiration": "` + past + `", "conditions": []}`
			Expect(checkRefusal(mustParse([]byte(doc)), &postpolicy.Env{}, now)).To(BeEmpty())
			doc = `{"expiration": "` + past + `", "expiration": "` + future + `", "conditions": []}`
			Expect(checkRefusal(mustParse([]byte(doc)), &postpolicy.Env{}, now)).To(Equal("Policy expired"))
		})

		It("takes the first conditions", func() {
			doc := `{"expiration": "` + future + `", "conditions": [["eq", "$key", "a"]], "conditions": [["eq", "$key", "b"]]}`
			Expect(checkRefusal(mustParse([]byte(doc)), v2Env("bkt", "key", "b"), now)).To(Equal("Policy condition failed: eq: $key, a"))
		})

		It("checks the first of an object condition's equal names", func() {
			p := mustParse(policyDoc(future, `{"acl": "private", "acl": "public-read"}`))
			Expect(checkRefusal(p, v2Env("bkt", "acl", "public-read"), now)).To(Equal("Policy check failed, variable not met condition: acl"))
		})
	})

	It("checks an object condition's member of the smallest name alone, whatever the order", func() {
		p := mustParse(policyDoc(future, `{"z-other": "x", "acl": "private"}`, `{"bucket": "bkt"}`))
		Expect(checkRefusal(p, v2Env("bkt", "acl", "private"), now)).To(BeEmpty())
		Expect(checkRefusal(p, v2Env("bkt", "acl", "public-read"), now)).To(Equal("Policy check failed, variable not met condition: acl"))
	})

	It("walks conditions given as an object in name order", func() {
		p := mustParse([]byte(`{"expiration": "` + future + `", "conditions": {"b": ["eq", "$key", "1"], "a": ["eq", "$key", "2"]}}`))
		Expect(checkRefusal(p, v2Env("bkt", "key", "3"), now)).To(Equal("Policy condition failed: eq: $key, 2"))
	})

	It("reads conditions that are a string as none", func() {
		p := mustParse([]byte(`{"expiration": "` + future + `", "conditions": "none"}`))
		Expect(checkRefusal(p, &postpolicy.Env{}, now)).To(BeEmpty())
		Expect(checkRefusal(p, v2Env("bkt"), now)).To(Equal("Policy missing condition: bucket"))
	})

	// A value is compared as the text get_data() returns, so a condition on
	// a number fails with that text in the message.
	DescribeTable("compares a number as json_spirit writes it",
		func(literal, text string) {
			p := mustParse(policyDoc(future, `["eq", "$x", `+literal+`]`))
			Expect(checkRefusal(p, &postpolicy.Env{}, now)).To(Equal("Policy condition failed: eq: $x, " + text))
			env := &postpolicy.Env{}
			env.Add("x", text)
			Expect(checkRefusal(p, env, now)).To(BeEmpty())
		},
		Entry("an integer", "1024", "1024"),
		Entry("negative zero as an integer", "-0", "0"),
		Entry("the least int64", "-9223372036854775808", "-9223372036854775808"),
		Entry("an integer beyond int64, as uint64_p reads it", "18446744073709551615", "18446744073709551615"),
		Entry("an exponent, as a real", "1e3", "1000.0000000000000"),
		Entry("a capital exponent with a sign", "25E+1", "250.00000000000000"),
		Entry("a negative exponent", "5e-1", "0.50000000000000000"),
		Entry("a fraction", "1.5", "1.5000000000000000"),
		Entry("0.3 as boost's parser builds it, three times pow(10, -1)", "0.3", "0.30000000000000004"),
		Entry("negative zero as a real", "-0.0", "-0.0000000000000000"),
		Entry("a large real, in exponent form", "1e20", "1.0000000000000000e+20"),
		Entry("1e23, whose glibc pow is the double above the nearest", "1e23", "1.0000000000000001e+23"),
		Entry("a small real", "1.5e-7", "1.4999999999999999e-07"),
		Entry("a long fraction, accumulated as a double", "0.12345678901234567890", "0.12345678901234569"),
	)

	DescribeTable("compares other values as get_data returns them",
		func(literal, text string) {
			p := mustParse(policyDoc(future, `["eq", "$x", `+literal+`]`))
			Expect(checkRefusal(p, &postpolicy.Env{}, now)).To(Equal("Policy condition failed: eq: $x, " + text))
		},
		Entry("true", "true", "true"),
		Entry("false", "false", "false"),
		Entry("null", "null", "null"),
		Entry("a string's escapes decoded", `"a\"b\\c\/d`+u("00e9")+`\n"`, "a\"b\\c/dé\n"),
		Entry("an escaped surrogate pair as the one character, where json_spirit writes each half alone",
			`"`+u("d83d")+u("de00")+`"`, "\U0001F600"),
		Entry("an array, compact", `[ "a" , 1, 2.5, [], {} ]`, `["a",1,2.5000000000000000,[],{}]`),
		Entry("an object in document order, duplicates kept", `{"b": 1, "a": null, "b": true}`, `{"b":1,"a":null,"b":true}`),
		Entry("a nested string's escapes, and bytes iswprint refuses in the C locale",
			`["q\"\\/\b\f\n\r\t`+u("0001")+u("007f")+u("00e9")+`~"]`, `["q\"\\/\b\f\n\r\t`+u("0001")+u("007F")+u("00C3")+u("00A9")+`~"]`),
	)

	It("reads a member name and a value with a NUL as Go strings", func() {
		p := mustParse(policyDoc(future, `{"acl": "a`+u("0000")+`b"}`))
		Expect(checkRefusal(p, v2Env("bkt", "acl", "a"), now)).To(Equal("Policy check failed, variable not met condition: acl"))
		Expect(checkRefusal(p, v2Env("bkt", "acl", "a\x00b"), now)).To(Equal("Policy missing condition: bucket"))
	})
})
