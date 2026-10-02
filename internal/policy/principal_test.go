package policy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("Principal", func() {
	kinds := func(p policy.Principal) map[string]bool {
		return map[string]bool{
			"wildcard":      p.IsWildcard(),
			"user":          p.IsUser(),
			"role":          p.IsRole(),
			"account":       p.IsAccount(),
			"oidc provider": p.IsOIDCProvider(),
			"assumed role":  p.IsAssumedRole(),
			"service":       p.IsService(),
		}
	}

	DescribeTable("the constructors set the kind and its fields",
		func(p, want policy.Principal, kind string) {
			Expect(p).To(Equal(want))
			wantKinds := kinds(policy.Principal{Kind: policy.PrincipalKind(255)})
			wantKinds[kind] = true
			Expect(kinds(p)).To(Equal(wantKinds))
		},
		Entry("the wildcard", policy.WildcardPrincipal(),
			policy.Principal{Kind: policy.PrincipalWildcard}, "wildcard"),
		Entry("a user", policy.UserPrincipal("t", "alice:sub"),
			policy.Principal{Kind: policy.PrincipalUser, Account: "t", ID: "alice:sub"}, "user"),
		Entry("a role", policy.RolePrincipal("t", "admin"),
			policy.Principal{Kind: policy.PrincipalRole, Account: "t", ID: "admin"}, "role"),
		Entry("an account", policy.AccountPrincipal("RGW11111111111111111"),
			policy.Principal{Kind: policy.PrincipalAccount, Account: "RGW11111111111111111"}, "account"),
		Entry("an OIDC provider", policy.OIDCProviderPrincipal("idp.example.com/realms/r"),
			policy.Principal{Kind: policy.PrincipalOIDCProvider, IDPURL: "idp.example.com/realms/r"}, "oidc provider"),
		Entry("an assumed role", policy.AssumedRolePrincipal("t", "admin/session"),
			policy.Principal{Kind: policy.PrincipalAssumedRole, Account: "t", ID: "admin/session"}, "assumed role"),
		Entry("a service", policy.ServicePrincipal("s3.amazonaws.com"),
			policy.Principal{Kind: policy.PrincipalService, ID: "s3.amazonaws.com"}, "service"),
	)

	It("numbers the kinds as rgw::auth::Principal::types does", func() {
		Expect([]policy.PrincipalKind{
			policy.PrincipalUser, policy.PrincipalRole, policy.PrincipalAccount, policy.PrincipalWildcard,
			policy.PrincipalOIDCProvider, policy.PrincipalAssumedRole, policy.PrincipalService,
		}).To(Equal([]policy.PrincipalKind{0, 1, 2, 3, 4, 5, 6}))
	})
})
