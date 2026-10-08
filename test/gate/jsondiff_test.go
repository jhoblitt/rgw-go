package gate_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/test/gate"
)

// canonical is gate.Canonical failing the spec on a document that does not
// decode.
func canonical(b []byte) any {
	GinkgoHelper()
	v, err := gate.Canonical(b)
	Expect(err).NotTo(HaveOccurred())
	return v
}

// jsonDiff is gate.JSONDiff.
var jsonDiff = gate.JSONDiff

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

var _ = Describe("DiffJSON", func() {
	type doc struct {
		A    int   `json:"a"`
		Pool *bool `json:"pool,omitempty"`
	}

	It("drops the keys an older dump omits before comparing", func() {
		Expect(gate.DiffJSON("d", doc{A: 1, Pool: new(true)}, canonical([]byte(`{"a":1}`)), "pool")).To(BeEmpty())
	})

	It("reports an absent path rgw-go's document does not hold, so a stale list fails", func() {
		Expect(gate.DiffJSON("d", doc{A: 1}, canonical([]byte(`{"a":1}`)), "pool")).To(ConsistOf(
			ContainSubstring("d lacks pool, which only an older dump omits")))
	})
})
