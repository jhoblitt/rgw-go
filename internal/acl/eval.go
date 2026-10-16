package acl

import (
	"maps"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Identity is what RGWAccessControlPolicy::get_perm asks of the requester's
// rgw::auth::Identity
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.h#L49-L70).
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

// Perm is RGWAccessControlPolicy::get_perm
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L160-L199 at v19.2.7 and v20.2.4):
// the flags of mask the policy grants id. They are the user map's flags for
// id and the ACL flags for the owner; while they fall short of mask, the
// AllUsers group's, and the AuthenticatedUsers group's unless id is
// anonymous; and while those still fall short, for a request with a Referer,
// RefererPerm replaces them. An empty referer stands for radosgw's absent
// header: an empty one names no host, so it could change nothing.
//
// ignorePublicACLs, IgnorePublicAcls, sets aside every grant that admits
// the anonymous requester: both groups, every referer grant, the user map's
// entry for the anonymous user, and the owner's ACL flags when the
// anonymous user owns the policy. The client chooses the Referer, so a
// referer grant is a grant to everyone; and a negative one revokes nothing
// from a request that leaves the header out. Every unauthenticated request
// runs as the anonymous user, so its grants and ownership are everyone's.
// radosgw reads the referer grants, the anonymous user's grants and its
// ownership whatever IgnorePublicAcls says (docs/exclusions.md).
func (p Policy) Perm(id Identity, mask Permission, referer string, ignorePublicACLs bool) Permission {
	users := p.ACL.UserMap
	if ignorePublicACLs {
		users = withoutAnonymous(users)
	}
	perm := mask & id.PermsFromACLSpec(users)
	if id.IsOwnerOf(p.Owner.ID) && (!ignorePublicACLs || !p.anonymousOwner()) {
		perm |= mask & (PermReadACP | PermWriteACP)
	}
	if perm == mask || ignorePublicACLs {
		return perm
	}
	perm |= p.ACL.GroupPerm(GroupAllUsers, mask)
	if !id.IsAnonymous() {
		perm |= p.ACL.GroupPerm(GroupAuthenticatedUsers, mask)
	}
	if referer != "" && perm&mask != mask {
		perm = p.ACL.RefererPerm(perm, referer, mask)
	}
	return perm
}

// Verify is RGWAccessControlPolicy::verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L201-L232):
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

// IsPublic is RGWAccessControlPolicy::is_public
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L235-L247 at v19.2.7 and v20.2.4)
// as Amazon S3 judges an ACL, public when "it grants any permissions to
// members of the predefined AllUsers or AuthenticatedUsers groups"
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-control-block-public-access.html#access-control-block-public-access-policy-status):
// whether the group map gives either group any flag, Swift's included, the
// ACL holds a referer grant, the user map gives the anonymous user any
// flag, or the anonymous user owns the policy. Those are what Perm sets
// aside under IgnorePublicAcls, so an ACL this calls private is one
// IgnorePublicAcls leaves as it is. The exception is a policy ForOwnWrite
// marks, whose writer's own rights do not count. radosgw counts only the S3
// flags of the two groups, and so calls a Swift container opened with .r:*
// or to the user anonymous private (docs/exclusions.md); an ACL an S3
// request builds for anyone but the anonymous user holds none of the rest,
// and the two agree on it.
func (p Policy) IsPublic() bool {
	anonymous := Permission(uint32(p.ACL.UserMap[anonymousUser])) &^ p.writer.grant //nolint:gosec // C++ stores the flags as an int
	return p.ACL.GroupMap[GroupAllUsers] != 0 || p.ACL.GroupMap[GroupAuthenticatedUsers] != 0 ||
		len(p.ACL.RefererList) > 0 || anonymous != PermNone || p.anonymousOwner() && !p.writer.owns
}

// ForOwnWrite returns p, the policy Canned or FromHeaders built for
// writer's own write of a new bucket, object, upload or part, with the
// rights that write gives an anonymous writer marked as its own: Canned's
// FULL_CONTROL grant to it, or the ownership FromHeaders gives it. IsPublic
// does not count them, so BlockPublicAcls refuses the write only when "the
// request includes a public ACL"
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-control-block-public-access.html).
// Any other grant to the anonymous user still counts. The mark is not
// encoded, so the stored policy, decoded again, is public. A writer other
// than the anonymous user leaves p as it is.
func (p Policy) ForOwnWrite(writer Owner) Policy {
	switch {
	case !sameOwner(writer.ID, anonymousUser):
	case p.anonymousOwner():
		p.writer.owns = true
	default:
		p.writer.grant = PermFullControl
	}
	return p
}

// anonymousOwner is whether the anonymous user owns p.
func (p Policy) anonymousOwner() bool { return sameOwner(p.Owner.ID, anonymousUser) }

// withoutAnonymous is users without the anonymous user's entry, users
// itself when it has none.
func withoutAnonymous(users map[string]int32) map[string]int32 {
	if _, ok := users[anonymousUser]; !ok {
		return users
	}
	users = maps.Clone(users)
	delete(users, anonymousUser)
	return users
}

// DefaultPolicy is RGWAccessControlPolicy::create_default
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.h#L341-L349,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.h#L430-L434):
// owner, named displayName, with one FULL_CONTROL grant to itself.
func DefaultPolicy(owner meta.Owner, displayName string) Policy {
	id := owner.String()
	p := Policy{Owner: Owner{ID: id, DisplayName: displayName}}
	p.ACL.AddGrant(Grant{Type: GranteeCanonUser, ID: id, Name: displayName, Permission: PermFullControl})
	return p
}

// AddGrant is RGWAccessControlList::add_grant
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L92-L102): it
// appends g under its grantee's key, which is a canonical user's id, an email
// address, or "" for any other grantee, and registers its flags in the maps
// as register_grant does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L71-L90). A
// canonical user's id is kept in its to_string form, the only form the C++
// grantee can hold.
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
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L104-L109):
// it drops every grant keyed by ownerID's to_string form, whatever its
// grantee, and that key's user map entry.
func (l *List) RemoveCanonUserGrant(ownerID string) {
	id := meta.ParseOwner(ownerID).String()
	l.Grants = slices.DeleteFunc(l.Grants, func(e GrantEntry) bool { return e.Key == id })
	delete(l.UserMap, id)
}

// GroupPerm is RGWAccessControlList::get_group_perm
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L121-L135):
// the flags of mask the group map gives group.
func (l List) GroupPerm(group uint32, mask Permission) Permission {
	return Permission(l.GroupMap[group]) & mask //nolint:gosec // C++ ands the int it stores with the unsigned mask
}

// RefererPerm is RGWAccessControlList::get_referer_perm
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L137-L158):
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

// IsMatch is ACLReferer::is_match
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.h#L212-L233):
// whether URLSpec matches the host of the Referer value httpReferer. The
// host must be at least as long as the spec; then the wildcard matches it,
// a spec starting with "." matches it when it ends with the spec, and any
// other spec only when the two are equal.
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

// httpHost is ACLReferer::get_http_host
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.h#L253-L270):
// what follows the first "://", past the first "@" after it, up to the first
// "/" or ":". A value without "://", starting or ending with it, or ending
// with "@" has no host.
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
