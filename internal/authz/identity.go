package authz

import (
	"strings"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// identity is an op.Identity as rgw::auth::LocalApplier answers for it
// (rgw_auth.cc:1019-1102 at v19.2.6, :1039-1131 at v20.2.4), the applier
// radosgw builds for local users and the anonymous user alike. The account
// it matches is op.Identity.Account, the account auth loaded from the user's
// account id.
type identity struct{ id *op.Identity }

var (
	_ acl.Identity    = identity{}
	_ policy.Identity = identity{}
)

// user is the identity's user record, nil when it has none.
func (v identity) user() *meta.UserInfo { return v.id.User }

// IsOwnerOf is is_owner_of for an owner id in its to_string form.
func (v identity) IsOwnerOf(ownerID string) bool {
	return v.IsOwnerOfOwner(meta.ParseOwner(ownerID))
}

// IsOwnerOfOwner is match_owner (rgw_auth.cc:67-75 at v19.2.6 and v20.2.4):
// a user owner is the user, tenant, id and namespace alike, and an account
// owner is the user's account.
func (v identity) IsOwnerOfOwner(o meta.Owner) bool {
	if o.User != nil {
		u := v.user()
		return u != nil && *o.User == u.UserID
	}
	return v.id.Account != nil && o.Account == v.id.Account.ID
}

// PermsFromACLSpec is get_perms_from_aclspec (rgw_auth.cc:1032-1046 at
// v19.2.6, :1052-1066 at v20.2.4): the user map's flags for the user, or-ed
// with the account's for an account user.
func (v identity) PermsFromACLSpec(m map[string]int32) acl.Permission {
	var perm acl.Permission
	if u := v.user(); u != nil {
		perm = acl.Permission(uint32(m[u.UserID.String()])) //nolint:gosec // C++ returns the int it stores as uint32_t
	}
	if v.id.Account != nil {
		perm |= acl.Permission(uint32(m[v.id.Account.ID])) //nolint:gosec // as above
	}
	return perm
}

// IsAnonymous is Identity::is_anonymous (rgw_auth.h:65-70 at v19.2.6, :74-79
// at v20.2.4), which asks is_owner_of for the user "anonymous" in the default
// tenant.
func (v identity) IsAnonymous() bool {
	return v.IsOwnerOfOwner(meta.UserOwner(meta.UserID{ID: op.AnonymousUserID}))
}

// IsIdentity is is_identity (rgw_auth.cc:1058-1076 at v19.2.6, :1086-1104 at
// v20.2.4): the wildcard; an account principal naming the user's account or
// tenant; a user principal under the user's account, matched by display
// name, or under its tenant, matched by user id.
func (v identity) IsIdentity(p policy.Principal) bool {
	u := v.user()
	switch {
	case p.IsWildcard():
		return true
	case u == nil:
		return false
	case p.IsAccount():
		return (v.id.Account != nil && v.id.Account.ID == p.Account) || u.UserID.Tenant == p.Account
	case p.IsUser():
		if v.id.Account != nil && p.Account == v.id.Account.ID {
			return matchPrincipal(u.Path, u.DisplayName, v.id.SubUser, p.ID)
		}
		return p.Account == u.UserID.Tenant && matchPrincipal(u.Path, u.UserID.ID, v.id.SubUser, p.ID)
	default:
		return false
	}
}

// matchPrincipal is match_principal (rgw_auth.cc:31-65 at v19.2.6 and
// v20.2.4): expected is the path, without the leading slash the principal's
// ":user/" already matched, then the name, then nothing or ":" and either "*"
// or the subuser.
func matchPrincipal(path, name, subuser, expected string) bool {
	if path != "" {
		path = path[1:]
	}
	rest, ok := strings.CutPrefix(expected, path)
	if !ok {
		return false
	}
	if rest, ok = strings.CutPrefix(rest, name); !ok {
		return false
	}
	if rest == "" {
		return true
	}
	if rest, ok = strings.CutPrefix(rest, ":"); !ok || rest == "" {
		return false
	}
	return rest == "*" || rest == subuser
}

// IdentityType is get_identity_type, the user's type; meta.IdentityRoot marks
// an account root.
func (v identity) IdentityType() meta.IdentityType {
	if u := v.user(); u != nil {
		return u.Type
	}
	return meta.IdentityNone
}

// PermMask is get_perm_mask (rgw_auth.cc:1086-1102 at v19.2.6, :1114-1130 at
// v20.2.4): full control without a subuser, a subuser's own mask, and none for
// a subuser the user does not have.
func (v identity) PermMask() acl.Permission {
	if v.id.SubUser == "" {
		return acl.PermFullControl
	}
	u := v.user()
	if u == nil {
		return acl.PermNone
	}
	s, ok := u.SubUsers[v.id.SubUser]
	if !ok {
		return acl.PermNone
	}
	return acl.Permission(s.Perm)
}

// ACLOwner is get_aclowner (rgw_auth.cc:1019-1030 at v19.2.6, :1039-1050 at
// v20.2.4): the account, named by its name, for an account user, and
// otherwise the user, named by its display name.
func (v identity) ACLOwner() acl.Owner {
	if v.id.Account != nil {
		return acl.Owner{ID: v.id.Account.ID, DisplayName: v.id.Account.Name}
	}
	if u := v.user(); u != nil {
		return acl.Owner{ID: u.UserID.String(), DisplayName: u.DisplayName}
	}
	return acl.Owner{}
}

// PolicyTenant is the tenant a user's identity policies are parsed with: the
// user's tenant, or nil for a user of an account, whose policies may name any
// account (load_account_and_policies, rgw_auth.cc:157-159 at v19.2.6 and
// v20.2.4).
func PolicyTenant(id *op.Identity) *string {
	if id.User == nil || id.User.AccountID != "" {
		return nil
	}
	return new(id.User.UserID.Tenant)
}
