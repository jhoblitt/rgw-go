package policy

// PrincipalKind is rgw::auth::Principal::types (src/rgw/rgw_basic_types.h:144
// at v19.2.6, :146 at v20.2.4).
type PrincipalKind uint8

// The principal kinds, in rgw::auth::Principal::types order.
const (
	PrincipalUser PrincipalKind = iota
	PrincipalRole
	PrincipalAccount
	PrincipalWildcard
	PrincipalOIDCProvider
	PrincipalAssumedRole
	// PrincipalService exists from v20.2.4 (src/rgw/rgw_basic_types.h:146,
	// :187-191).
	PrincipalService
)

// Principal is rgw::auth::Principal (src/rgw/rgw_basic_types.h:143-235 at
// v19.2.6, :145-252 at v20.2.4): whom a statement's Principal or NotPrincipal
// names. radosgw's == and < compare only the kind and the rgw_user, tenant
// and id (:228-234 at v19.2.6, :245-251 at v20.2.4), so to radosgw any two
// OIDC providers, or any two services, are equal; Go's == also compares
// IDPURL and a service's name.
type Principal struct {
	Kind PrincipalKind
	// Account is get_account, the rgw_user's tenant, which holds the account
	// id for an account principal.
	Account string
	// ID is get_id, get_role or get_role_session: the user's or role's name,
	// or for an assumed role "<role>/<session>"; for a service principal it
	// is get_service, the service's name.
	ID string
	// IDPURL is get_idp_url, an OIDC provider's URL.
	IDPURL string
}

// WildcardPrincipal is Principal::wildcard, every principal.
func WildcardPrincipal() Principal {
	return Principal{Kind: PrincipalWildcard}
}

// UserPrincipal is Principal::user.
func UserPrincipal(account, id string) Principal {
	return Principal{Kind: PrincipalUser, Account: account, ID: id}
}

// RolePrincipal is Principal::role.
func RolePrincipal(account, id string) Principal {
	return Principal{Kind: PrincipalRole, Account: account, ID: id}
}

// AccountPrincipal is Principal::account.
func AccountPrincipal(account string) Principal {
	return Principal{Kind: PrincipalAccount, Account: account}
}

// OIDCProviderPrincipal is Principal::oidc_provider.
func OIDCProviderPrincipal(url string) Principal {
	return Principal{Kind: PrincipalOIDCProvider, IDPURL: url}
}

// AssumedRolePrincipal is Principal::assumed_role.
func AssumedRolePrincipal(account, id string) Principal {
	return Principal{Kind: PrincipalAssumedRole, Account: account, ID: id}
}

// ServicePrincipal is Principal::service, which v20.2.4 added.
func ServicePrincipal(name string) Principal {
	return Principal{Kind: PrincipalService, ID: name}
}

// IsWildcard is is_wildcard.
func (p Principal) IsWildcard() bool { return p.Kind == PrincipalWildcard }

// IsUser is is_user.
func (p Principal) IsUser() bool { return p.Kind == PrincipalUser }

// IsRole is is_role.
func (p Principal) IsRole() bool { return p.Kind == PrincipalRole }

// IsAccount is is_account.
func (p Principal) IsAccount() bool { return p.Kind == PrincipalAccount }

// IsOIDCProvider is is_oidc_provider.
func (p Principal) IsOIDCProvider() bool { return p.Kind == PrincipalOIDCProvider }

// IsAssumedRole is is_assumed_role.
func (p Principal) IsAssumedRole() bool { return p.Kind == PrincipalAssumedRole }

// IsService is is_service.
func (p Principal) IsService() bool { return p.Kind == PrincipalService }
