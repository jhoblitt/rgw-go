package postpolicy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/postpolicy"
)

var _ = Describe("Env", func() {
	It("gets a variable whatever the case of its name", func() {
		env := &postpolicy.Env{}
		env.Add("Content-Type", "text/plain")
		v, ok := env.Get("content-TYPE")
		Expect([]any{v, ok}).To(Equal([]any{"text/plain", true}))
	})

	It("reports a variable it lacks", func() {
		v, ok := (&postpolicy.Env{}).Get("key")
		Expect([]any{v, ok}).To(Equal([]any{"", false}))
	})

	It("keeps the last value added under a name, as RGWPolicyEnv::add_var does", func() {
		env := &postpolicy.Env{}
		env.Add("kEy", "a")
		env.Add("key", "b")
		v, _ := env.Get("KEY")
		Expect(v).To(Equal("b"))
	})

	It("folds ASCII letters alone", func() {
		env := &postpolicy.Env{}
		env.Add("caf\xc3\xa9", "v")
		v, ok := env.Get("CAF\xc3\x89")
		Expect([]any{v, ok}).To(Equal([]any{"", false}), "an accented capital matched its small letter")
		v, _ = env.Get("CAF\xc3\xa9")
		Expect(v).To(Equal("v"))
	})

	It("reads a NUL in a name as part of it, where radosgw's strcasecmp ends the name", func() {
		env := &postpolicy.Env{}
		env.Add("key", "a")
		env.Add("key\x00x", "b")
		v, _ := env.Get("key")
		Expect(v).To(Equal("a"))
		p := mustParse(policyDoc(future, `["eq", "$key", "a"]`))
		Expect(checkRefusal(p, env, now)).To(Equal("Policy missing condition: key\x00x"))
	})

	It("keeps a mark made before the variable is added", func() {
		env := &postpolicy.Env{}
		env.MarkChecked("POLICY")
		env.Add("policy", "p")
		Expect(checkRefusal(mustParse(policyDoc(future)), env, now)).To(BeEmpty())
	})
})
