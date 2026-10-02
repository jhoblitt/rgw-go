package policy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("SemanticsFor", func() {
	It("keeps every v19.2.6 rule on squid", func() {
		Expect(policy.SemanticsFor(denc.Squid)).To(Equal(policy.Semantics{}))
	})

	It("takes every v20.2.4 rule on tentacle", func() {
		Expect(policy.SemanticsFor(denc.Tentacle)).To(gstruct.MatchAllFields(gstruct.Fields{
			"NullTestsValues":              BeTrue(),
			"NotMeansNone":                 BeTrue(),
			"PublicNeedsWildcardPrincipal": BeTrue(),
			"RejectAllowWithNotPrincipal":  BeTrue(),
			"ServicePrincipals":            BeTrue(),
			"FindKeepsFirstValue":          BeTrue(),
		}))
	})
})
