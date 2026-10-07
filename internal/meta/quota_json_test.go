package meta_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("Quota.UnmarshalJSON", func() {
	It("decodes RGWQuotaInfo's JSON, taking max_size_kb when max_size is absent", func() {
		var q meta.Quota
		Expect(json.Unmarshal([]byte(`{"enabled":true,"max_size_kb":4096,"max_objects":-1}`), &q)).To(Succeed())
		Expect(q).To(Equal(meta.Quota{MaxSize: 4096 * 1024, MaxObjects: -1, Enabled: true}))
		Expect(json.Unmarshal([]byte(`{"max_size":10,"max_size_kb":4096,"max_objects":5,"check_on_raw":true}`), &q)).To(Succeed())
		Expect(q).To(Equal(meta.Quota{MaxSize: 10, MaxObjects: 5, CheckOnRaw: true}),
			"an absent field is reset, as JSONDecoder::decode_json resets it to T()")
	})

	DescribeTable("reads each field as decode_json_obj reads the member's text",
		func(in string, want meta.Quota) {
			q := meta.Quota{MaxSize: 77, MaxObjects: 77, Enabled: true, CheckOnRaw: true}
			Expect(json.Unmarshal([]byte(in), &q)).To(Succeed(), in)
			Expect(q).To(Equal(want), in)
		},
		Entry("an empty object resets every field", `{}`, meta.Quota{}),
		Entry("no size at all is 0, not unlimited", `{"max_objects":5}`, meta.Quota{MaxObjects: 5}),
		Entry("numbers in strings, white space around them", `{"max_size":" 12\n","max_objects":"-3"}`, meta.Quota{MaxSize: 12, MaxObjects: -3}),
		Entry("booleans as words in any case", `{"enabled":"TRUE","check_on_raw":"False"}`, meta.Quota{Enabled: true}),
		Entry("booleans as integers", `{"enabled":2,"check_on_raw":"0"}`, meta.Quota{Enabled: true}),
		Entry("the largest int64, which strtoll reads without ERANGE", `{"max_size":9223372036854775807}`, meta.Quota{MaxSize: 9223372036854775807}),
		Entry("the largest max_size_kb whose bytes fit", `{"max_size_kb":9007199254740991}`, meta.Quota{MaxSize: 9007199254740991 * 1024}),
		Entry("the first of a repeated member, as JSONObj::find_first finds it", `{"max_objects":1,"max_objects":2}`, meta.Quota{MaxObjects: 1}),
		Entry("a string cut at a NUL, as c_str() cuts it", `{"max_objects":"4\u0000x"}`, meta.Quota{MaxObjects: 4}),
		Entry("an array has no named members", `[{"max_objects":5}]`, meta.Quota{}),
		Entry("a string has no members", `"max_objects"`, meta.Quota{}),
	)

	DescribeTable("refuses a member decode_json_obj throws on",
		func(in string) {
			var q meta.Quota
			Expect(json.Unmarshal([]byte(in), &q)).NotTo(Succeed(), in)
		},
		Entry("a fraction", `{"max_size":1.5}`),
		Entry("an exponent", `{"max_objects":1e3}`),
		Entry("trailing text in a string", `{"max_objects":"12abc"}`),
		Entry("an empty string", `{"max_objects":""}`),
		Entry("a boolean for a number", `{"max_size":true}`),
		Entry("null", `{"max_objects":null}`),
		Entry("an object", `{"max_objects":{}}`),
		Entry("an int64 overflow", `{"max_size":9223372036854775808}`),
		Entry("an int64 underflow", `{"max_size":-9223372036854775809}`),
		Entry("a bad max_size even with max_size_kb", `{"max_size":"x","max_size_kb":1}`),
		Entry("a bad max_size_kb", `{"max_size_kb":"x"}`),
		Entry("a max_size_kb whose bytes overflow int64", `{"max_size_kb":9007199254740992}`),
		Entry("a max_size_kb whose bytes underflow int64", `{"max_size_kb":-9007199254740993}`),
		Entry("a word that is no boolean", `{"enabled":"yes"}`),
		Entry("a word that folds to false only outside ASCII, which strcasecmp does not fold", `{"enabled":"falſe"}`),
		Entry("a boolean past int's range", `{"enabled":2147483648}`),
	)
})
