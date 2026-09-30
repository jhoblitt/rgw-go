package acl

import (
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Identity is what RGWAccessControlPolicy::get_perm asks of the requester's
// rgw::auth::Identity (rgw_auth.h:49-70).
type Identity interface {
	// IsOwnerOf is is_owner_of for an ACLOwner id in its to_string form.
	IsOwnerOf(ownerID string) bool
	// PermsFromACLSpec is get_perms_from_aclspec over a list's user map: the
	// flags the map gives the requester's own ids, 0 when it names none.
	PermsFromACLSpec(userMap map[string]int32) Permission
	// IsAnonymous is is_anonymous, which every radosgw applier answers as
	// is_owner_of(rgw_user("anonymous")), the test get_perm makes.
	IsAnonymous() bool
}

// Perm is RGWAccessControlPolicy::get_perm (rgw_acl.cc:160-199): the flags of
// mask the policy grants id. They are the user map's flags for id and the ACL
// flags for the owner; while they fall short of mask, the AllUsers group's
// unless ignorePublicACLs, and the AuthenticatedUsers group's unless that too
// or id is anonymous; and while those still fall short, for a request with a
// Referer, RefererPerm replaces them. An empty referer stands for radosgw's
// absent header: an empty one names no host, so it could change nothing.
func (p Policy) Perm(id Identity, mask Permission, referer string, ignorePublicACLs bool) Permission {
	perm := mask & id.PermsFromACLSpec(p.ACL.UserMap)
	if id.IsOwnerOf(p.Owner.ID) {
		perm |= mask & (PermReadACP | PermWriteACP)
	}
	if perm == mask {
		return perm
	}
	if !ignorePublicACLs && perm&mask != mask {
		perm |= p.ACL.GroupPerm(GroupAllUsers, mask)
		if !id.IsAnonymous() {
			perm |= p.ACL.GroupPerm(GroupAuthenticatedUsers, mask)
		}
	}
	if referer != "" && perm&mask != mask {
		perm = p.ACL.RefererPerm(perm, referer, mask)
	}
	return perm
}

// Verify is RGWAccessControlPolicy::verify_permission (rgw_acl.cc:201-232):
// whether id holds every flag of perm, each granted by the policy and allowed
// by userPermMask. Swift's container flags, which only a bucket's ACL
// carries, count as more: WRITE_OBJS as WRITE and WRITE_ACP, READ_OBJS as
// READ and READ_ACP.
func (p Policy) Verify(id Identity, userPermMask, perm Permission, referer string, ignorePublicACLs bool) bool {
	policyPerm := p.Perm(id, perm|PermReadObjs|PermWriteObjs, referer, ignorePublicACLs)
	if policyPerm&PermWriteObjs != 0 {
		policyPerm |= PermWrite | PermWriteACP
	}
	if policyPerm&PermReadObjs != 0 {
		policyPerm |= PermRead | PermReadACP
	}
	return perm == policyPerm&perm&userPermMask
}

// IsPublic is RGWAccessControlPolicy::is_public (rgw_acl.cc:235-247): whether
// the AllUsers or the AuthenticatedUsers group holds any S3 flag.
func (p Policy) IsPublic() bool {
	for _, g := range []uint32{GroupAllUsers, GroupAuthenticatedUsers} {
		if perm := p.ACL.GroupPerm(g, PermFullControl); perm != PermNone && perm != PermInvalid {
			return true
		}
	}
	return false
}

// DefaultPolicy is RGWAccessControlPolicy::create_default (rgw_acl.h:341-349,
// :430-434): owner, named displayName, with one FULL_CONTROL grant to itself.
func DefaultPolicy(owner meta.Owner, displayName string) Policy {
	id := owner.String()
	p := Policy{Owner: Owner{ID: id, DisplayName: displayName}}
	p.ACL.AddGrant(Grant{Type: GranteeCanonUser, ID: id, Name: displayName, Permission: PermFullControl})
	return p
}

// AddGrant is RGWAccessControlList::add_grant (rgw_acl.cc:92-102): it appends
// g under its grantee's key, which is a canonical user's id, an email
// address, or "" for any other grantee, and registers its flags in the maps
// as register_grant does (:71-90). A canonical user's id is kept in its
// to_string form, the only form the C++ grantee can hold.
func (l *List) AddGrant(g Grant) {
	var key string
	switch g.kind() {
	case GranteeCanonUser:
		g.ID = meta.ParseOwner(g.ID).String()
		key = g.ID
	case GranteeEmail:
		key = g.Email
	}
	l.Grants = append(l.Grants, GrantEntry{Key: key, Grant: g})
	registerGrant(l, g)
}

// RemoveCanonUserGrant is RGWAccessControlList::remove_canon_user_grant
// (rgw_acl.cc:104-109): it drops every grant keyed by ownerID's to_string
// form, whatever its grantee, and that key's user map entry.
func (l *List) RemoveCanonUserGrant(ownerID string) {
	id := meta.ParseOwner(ownerID).String()
	l.Grants = slices.DeleteFunc(l.Grants, func(e GrantEntry) bool { return e.Key == id })
	delete(l.UserMap, id)
}

// GroupPerm is RGWAccessControlList::get_group_perm (rgw_acl.cc:121-135): the
// flags of mask the group map gives group.
func (l List) GroupPerm(group uint32, mask Permission) Permission {
	return Permission(l.GroupMap[group]) & mask //nolint:gosec // C++ ands the int it stores with the unsigned mask
}

// RefererPerm is RGWAccessControlList::get_referer_perm (rgw_acl.cc:137-158):
// the flags of the last referer grant matching referer, or current when none
// matches, within mask. A match replaces rather than adds, so a negative
// Swift referer grant, stored with no flags, revokes what the other grants
// gave.
func (l List) RefererPerm(current Permission, referer string, mask Permission) Permission {
	perm := current
	for _, r := range l.RefererList {
		if r.IsMatch(referer) {
			perm = Permission(r.Perm)
		}
	}
	return perm & mask
}

// IsMatch is ACLReferer::is_match (rgw_acl.h:212-233): whether URLSpec
// matches the host of the Referer value httpReferer. The host must be at
// least as long as the spec; then the wildcard matches it, a spec starting
// with "." matches it when it ends with the spec, and any other spec only
// when the two are equal.
func (r Referer) IsMatch(httpReferer string) bool {
	host, ok := httpHost(httpReferer)
	if !ok || len(host) < len(r.URLSpec) {
		return false
	}
	if r.URLSpec == refererWildcard || host == r.URLSpec {
		return true
	}
	return strings.HasPrefix(r.URLSpec, ".") && strings.HasSuffix(host, r.URLSpec)
}

// httpHost is ACLReferer::get_http_host (rgw_acl.h:253-270): what follows the
// first "://", past the first "@" after it, up to the first "/" or ":". A
// value without "://", starting or ending with it, or ending with "@" has no
// host.
func httpHost(url string) (string, bool) {
	_, host, found := strings.Cut(url, "://")
	if !found || strings.HasPrefix(url, "://") || strings.HasSuffix(url, "://") || strings.HasSuffix(url, "@") {
		return "", false
	}
	if _, after, userinfo := strings.Cut(host, "@"); userinfo {
		host = after
	}
	if j := strings.IndexAny(host, "/:"); j >= 0 {
		host = host[:j]
	}
	return host, true
}
