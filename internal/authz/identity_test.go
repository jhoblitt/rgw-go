package authz_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

const acctID = "RGW12345678901234567"

// aliceInfo is the user record the identity specs view: tenant t, display
// name Alice, and a read-only subuser ro.
func aliceInfo() meta.UserInfo {
	return meta.UserInfo{
		UserID:      meta.ParseUserID("t$alice"),
		DisplayName: "Alice",
		SubUsers:    map[string]meta.SubUser{"ro": {Name: "ro", Perm: uint32(acl.PermRead)}},
		Type:        meta.IdentityRGW,
	}
}

func identityOf(u meta.UserInfo) op.Identity {
	return op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), Tenant: u.UserID.Tenant}
}

// accountIdentityOf makes u a user of account acctID, as auth loads one.
func accountIdentityOf(u meta.UserInfo) op.Identity {
	u.AccountID = acctID
	id := identityOf(u)
	id.Account = &meta.AccountInfo{ID: acctID, Name: "Acme"}
	id.Owner = meta.AccountOwner(acctID)
	return id
}

var _ = Describe("identity", func() {
	var alice op.Identity

	BeforeEach(func() {
		alice = identityOf(aliceInfo())
	})

	Describe("IsOwnerOf", func() {
		DescribeTable("compares the owner id with the user's, tenant included",
			func(owner string, want bool) {
				Expect(authz.ViewOf(&alice).IsOwnerOf(owner)).To(Equal(want), "owner %q", owner)
			},
			Entry("the user itself", "t$alice", true),
			Entry("the id without its tenant", "alice", false),
			Entry("another id in the tenant", "t$alice2", false),
			Entry("the user in a namespace", "t$ns$alice", false),
			Entry("an account id the user does not belong to", acctID, false),
		)

		It("matches the account id for an account user, and still the user", func() {
			id := accountIdentityOf(aliceInfo())
			v := authz.ViewOf(&id)
			Expect(v.IsOwnerOf(acctID)).To(BeTrue(), "account")
			Expect(v.IsOwnerOf("t$alice")).To(BeTrue(), "user")
			Expect(v.IsOwnerOf("RGW00000000000000001")).To(BeFalse(), "another account")
		})

		It("matches an rgw_owner directly", func() {
			v := authz.ViewOf(&alice)
			Expect(v.IsOwnerOfOwner(meta.UserOwner(meta.ParseUserID("t$alice")))).To(BeTrue())
			Expect(v.IsOwnerOfOwner(meta.AccountOwner(acctID))).To(BeFalse())
		})
	})

	Describe("IsAnonymous", func() {
		It("is the anonymous user and no one else", func() {
			anon := op.Anonymous()
			Expect(authz.ViewOf(&anon).IsAnonymous()).To(BeTrue(), "anonymous")
			Expect(authz.ViewOf(&alice).IsAnonymous()).To(BeFalse(), "alice")
			tenanted := identityOf(meta.UserInfo{UserID: meta.ParseUserID("t$anonymous")})
			Expect(authz.ViewOf(&tenanted).IsAnonymous()).To(BeFalse(), "t$anonymous")
		})
	})

	Describe("PermsFromACLSpec", func() {
		It("reads the user's entry", func() {
			v := authz.ViewOf(&alice)
			Expect(v.PermsFromACLSpec(map[string]int32{"t$alice": 3, "alice": 8})).To(Equal(acl.Permission(3)))
			Expect(v.PermsFromACLSpec(map[string]int32{acctID: 4})).To(Equal(acl.PermNone), "no account")
			Expect(v.PermsFromACLSpec(nil)).To(Equal(acl.PermNone), "empty map")
		})

		It("ors in the account's entry for an account user", func() {
			id := accountIdentityOf(aliceInfo())
			Expect(authz.ViewOf(&id).PermsFromACLSpec(map[string]int32{"t$alice": 1, acctID: 4})).
				To(Equal(acl.Permission(5)))
		})
	})

	Describe("IsIdentity", func() {
		DescribeTable("for a tenant user",
			func(subuser, path string, p policy.Principal, want bool) {
				u := aliceInfo()
				u.Path = path
				id := identityOf(u)
				id.SubUser = subuser
				Expect(authz.ViewOf(&id).IsIdentity(p)).To(Equal(want), "subuser %q path %q principal %+v", subuser, path, p)
			},
			Entry("the wildcard", "", "", policy.WildcardPrincipal(), true),
			Entry("the user's tenant as an account", "", "", policy.AccountPrincipal("t"), true),
			Entry("another account", "", "", policy.AccountPrincipal("u"), false),
			Entry("the user", "", "", policy.UserPrincipal("t", "alice"), true),
			Entry("the user under the root path", "", "/", policy.UserPrincipal("t", "alice"), true),
			Entry("the user in another tenant", "", "", policy.UserPrincipal("", "alice"), false),
			Entry("a subuser, signed by it", "ro", "", policy.UserPrincipal("t", "alice:ro"), true),
			Entry("a subuser, signed by the user", "", "", policy.UserPrincipal("t", "alice:ro"), false),
			Entry("a subuser, signed by another", "rw", "", policy.UserPrincipal("t", "alice:ro"), false),
			Entry("every subuser, signed by one", "ro", "", policy.UserPrincipal("t", "alice:*"), true),
			// match_principal compares the rest with "*" before the subuser
			// (rgw_auth.cc:64), so the user itself matches too.
			Entry("every subuser, signed by the user", "", "", policy.UserPrincipal("t", "alice:*"), true),
			Entry("an empty subuser", "", "", policy.UserPrincipal("t", "alice:"), false),
			Entry("a prefix of the name", "", "", policy.UserPrincipal("t", "alic"), false),
			Entry("a longer name", "", "", policy.UserPrincipal("t", "alices"), false),
			Entry("the user under its path", "", "/eng/", policy.UserPrincipal("t", "eng/alice"), true),
			Entry("the user without its path", "", "/eng/", policy.UserPrincipal("t", "alice"), false),
			Entry("a role", "", "", policy.RolePrincipal("t", "alice"), false),
			Entry("an assumed role", "", "", policy.AssumedRolePrincipal("t", "alice/s"), false),
		)

		It("matches an account user by the account and its display name", func() {
			id := accountIdentityOf(aliceInfo())
			v := authz.ViewOf(&id)
			Expect(v.IsIdentity(policy.AccountPrincipal(acctID))).To(BeTrue(), "account")
			Expect(v.IsIdentity(policy.AccountPrincipal("t"))).To(BeTrue(), "tenant")
			Expect(v.IsIdentity(policy.UserPrincipal(acctID, "Alice"))).To(BeTrue(), "display name")
			Expect(v.IsIdentity(policy.UserPrincipal(acctID, "alice"))).To(BeFalse(), "user id under the account")
			Expect(v.IsIdentity(policy.UserPrincipal("t", "alice"))).To(BeTrue(), "user id under the tenant")
		})
	})

	Describe("PermMask", func() {
		DescribeTable("is the subuser's mask",
			func(subuser string, want acl.Permission) {
				id := alice
				id.SubUser = subuser
				Expect(authz.ViewOf(&id).PermMask()).To(Equal(want), "subuser %q", subuser)
			},
			Entry("no subuser", "", acl.PermFullControl),
			Entry("a known subuser", "ro", acl.PermRead),
			Entry("an unknown subuser", "nope", acl.PermNone),
		)
	})

	Describe("IdentityType", func() {
		It("is the user's type", func() {
			Expect(authz.ViewOf(&alice).IdentityType()).To(Equal(meta.IdentityRGW))
			u := aliceInfo()
			u.Type = meta.IdentityRoot
			root := accountIdentityOf(u)
			Expect(authz.ViewOf(&root).IdentityType()).To(Equal(meta.IdentityRoot))
			Expect(authz.ViewOf(&op.Identity{}).IdentityType()).To(Equal(meta.IdentityNone), "no user")
		})
	})

	Describe("ACLOwner", func() {
		It("is the user, or the account for an account user", func() {
			Expect(authz.ViewOf(&alice).ACLOwner()).To(Equal(acl.Owner{ID: "t$alice", DisplayName: "Alice"}))
			id := accountIdentityOf(aliceInfo())
			Expect(authz.ViewOf(&id).ACLOwner()).To(Equal(acl.Owner{ID: acctID, DisplayName: "Acme"}))
		})
	})
})

var _ = Describe("PolicyTenant", func() {
	It("is the user's tenant, or nil for an account user", func() {
		alice := identityOf(aliceInfo())
		Expect(authz.PolicyTenant(&alice)).To(HaveValue(Equal("t")))
		acct := accountIdentityOf(aliceInfo())
		Expect(authz.PolicyTenant(&acct)).To(BeNil())
		anon := op.Anonymous()
		Expect(authz.PolicyTenant(&anon)).To(HaveValue(Equal("")))
	})
})
