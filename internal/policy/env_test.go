package policy_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("Env", func() {
	var (
		env             policy.Env
		squid, tentacle policy.Semantics
	)

	// fill adds n pairs under keys of their own, so that the environment
	// holds n more pairs when the next value is added.
	fill := func(e *policy.Env, from, n int) {
		for i := range n {
			e.Add(fmt.Sprintf("filler%02d", from+i), "v")
		}
	}
	unfill := func(e *policy.Env, from, n int) {
		for i := range n {
			e.Remove(fmt.Sprintf("filler%02d", from+i), "v")
		}
	}
	// found is Find for a key the spec expects present.
	found := func(e *policy.Env, key string, sem policy.Semantics) string {
		value, ok := e.Find(key, sem)
		Expect(ok).To(BeTrue(), "Find(%q) on %+v", key, sem)
		return value
	}

	BeforeEach(func() {
		env = policy.Env{}
		squid = policy.SemanticsFor(denc.Squid)
		tentacle = policy.SemanticsFor(denc.Tentacle)
	})

	It("keeps every value of a key, duplicates included, in the order added", func() {
		env.Add("k", "a")
		env.Add("k", "b")
		env.Add("k", "a")
		Expect(env.Lookup("k")).To(Equal([]string{"a", "b", "a"}))
	})

	It("holds a key whose only value is empty", func() {
		env.Add("k", "")
		Expect(env.Lookup("k")).To(Equal([]string{""}))
		Expect(found(&env, "k", squid)).To(BeEmpty())
	})

	It("answers an absent key with nil and no value", func() {
		Expect(env.Lookup("k")).To(BeNil())
		for _, sem := range []policy.Semantics{squid, tentacle} {
			value, ok := env.Find("k", sem)
			Expect(ok).To(BeFalse(), "Find on %+v", sem)
			Expect(value).To(BeEmpty(), "Find on %+v", sem)
		}
	})

	It("removes only the pairs equal to the one named", func() {
		env.Add("k", "v")
		env.Add("k", "w")
		env.Add("k", "v")
		env.Add("j", "v")
		env.Remove("k", "v")
		Expect(env.Lookup("k")).To(Equal([]string{"w"}))
		Expect(env.Lookup("j")).To(Equal([]string{"v"}))
	})

	It("drops a key once its last value is removed", func() {
		env.Add("k", "v")
		env.Remove("k", "v")
		Expect(env.Lookup("k")).To(BeNil())
		_, ok := env.Find("k", tentacle)
		Expect(ok).To(BeFalse())
	})

	It("ignores the removal of a pair it does not hold", func() {
		env.Add("k", "v")
		env.Remove("k", "w")
		env.Remove("j", "v")
		Expect(env.Lookup("k")).To(Equal([]string{"v"}))
		Expect(env.Lookup("j")).To(BeNil())
	})

	It("looks up a copy that later changes to the Env leave alone", func() {
		env.Add("k", "a")
		env.Add("k", "b")
		env.Add("k", "a")
		got := env.Lookup("k")
		env.Remove("k", "a")
		env.Add("k", "c")
		Expect(got).To(Equal([]string{"a", "b", "a"}))
		got[0] = "z"
		Expect(env.Lookup("k")).To(Equal([]string{"b", "c"}))
	})

	It("clones into an Env that changes independently and finds the same values", func() {
		env.Add("k", "1")
		env.Add("k", "2")
		env.Add("k", "3")
		clone := env.Clone()
		Expect(found(&clone, "k", tentacle)).To(Equal("1"))
		Expect(found(&clone, "k", squid)).To(Equal("3"))

		clone.Remove("k", "1")
		clone.Add("k", "4")
		clone.Add("j", "c")
		Expect(found(&env, "k", tentacle)).To(Equal("1"), "the original on tentacle, after the clone changed")
		Expect(found(&env, "k", squid)).To(Equal("3"), "the original on squid, after the clone changed")
		Expect(env.Lookup("k")).To(Equal([]string{"1", "2", "3"}))
		Expect(env.Lookup("j")).To(BeNil())

		env.Remove("k", "3")
		env.Add("k", "x")
		Expect(found(&clone, "k", tentacle)).To(Equal("3"), "the clone on tentacle, after the original changed")
		Expect(found(&clone, "k", squid)).To(Equal("4"), "the clone on squid, after the original changed")
		Expect(clone.Lookup("k")).To(Equal([]string{"2", "3", "4"}))
		Expect(clone.Lookup("j")).To(Equal([]string{"c"}))
		Expect(found(&env, "k", tentacle)).To(Equal("1"))
		Expect(found(&env, "k", squid)).To(Equal("x"))
	})

	DescribeTable("finds the value radosgw's find gives a typed condition",
		func(build func(e *policy.Env), wantSquid, wantTentacle string) {
			build(&env)
			Expect(found(&env, "k", squid)).To(Equal(wantSquid), "squid")
			Expect(found(&env, "k", tentacle)).To(Equal(wantTentacle), "tentacle")
		},
		Entry("a single value", func(e *policy.Env) {
			e.Add("k", "1")
		}, "1", "1"),
		Entry("values added while the environment is small", func(e *policy.Env) {
			e.Add("k", "1")
			e.Add("k", "2")
			e.Add("k", "3")
		}, "3", "1"),
		Entry("a value added while the environment holds 20 pairs", func(e *policy.Env) {
			fill(e, 0, 18)
			e.Add("k", "1")
			e.Add("k", "2")
			e.Add("k", "3")
		}, "3", "1"),
		Entry("a value added while the environment holds 21 pairs", func(e *policy.Env) {
			fill(e, 0, 19)
			e.Add("k", "1")
			e.Add("k", "2")
			e.Add("k", "3")
		}, "3", "3"),
		Entry("several values added past 20 pairs", func(e *policy.Env) {
			fill(e, 0, 20)
			e.Add("k", "1")
			e.Add("k", "2")
			e.Add("k", "3")
			e.Add("k", "4")
		}, "4", "4"),
		Entry("a value added at 20 pairs or fewer after one added past 20", func(e *policy.Env) {
			fill(e, 0, 20)
			e.Add("k", "1")
			e.Add("k", "2")
			unfill(e, 0, 10)
			e.Add("k", "3")
		}, "3", "2"),
		Entry("the first value removed", func(e *policy.Env) {
			e.Add("k", "1")
			e.Add("k", "2")
			e.Add("k", "3")
			e.Remove("k", "1")
		}, "3", "3"),
		Entry("the last value removed", func(e *policy.Env) {
			e.Add("k", "1")
			e.Add("k", "2")
			e.Add("k", "3")
			e.Remove("k", "3")
		}, "2", "1"),
		Entry("every value of the key removed, then one added", func(e *policy.Env) {
			fill(e, 0, 20)
			e.Add("k", "1")
			e.Add("k", "2")
			e.Remove("k", "1")
			e.Remove("k", "2")
			unfill(e, 0, 20)
			e.Add("k", "3")
			e.Add("k", "4")
		}, "4", "3"),
	)
})
