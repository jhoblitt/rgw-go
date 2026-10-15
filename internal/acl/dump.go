package acl

import (
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// The dumps below are rgw_acl.cc's, the same at v19.2.6 and v20.2.4: the
// forms the admin API renders through a formatter, which the MarshalJSON
// methods give for ceph-dencoder.

// Dump is RGWAccessControlPolicy::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L417-L421):
// the list, then the owner.
func (p Policy) Dump(f formatter.Formatter, rel denc.Release) {
	f.OpenObjectSection("acl")
	p.ACL.Dump(f, rel)
	f.CloseSection()
	f.OpenObjectSection("owner")
	p.Owner.Dump(f, rel)
	f.CloseSection()
}

// Dump is ACLOwner::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L404-L408).
func (o Owner) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("id", o.ID)
	f.DumpString("display_name", o.DisplayName)
}

// Dump is RGWAccessControlList::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L369-L402):
// the user and group maps in key order and the grants in multimap
// order; the referer list is not dumped.
func (l List) Dump(f formatter.Formatter, rel denc.Release) {
	f.OpenArraySection("acl_user_map")
	for _, k := range slices.Sorted(maps.Keys(l.UserMap)) {
		f.OpenObjectSection("entry")
		f.DumpString("user", k)
		f.DumpInt("acl", int64(l.UserMap[k]))
		f.CloseSection()
	}
	f.CloseSection()
	f.OpenArraySection("acl_group_map")
	for _, k := range slices.Sorted(maps.Keys(l.GroupMap)) {
		f.OpenObjectSection("entry")
		f.DumpUnsigned("group", uint64(k))
		f.DumpInt("acl", int64(l.GroupMap[k]))
		f.CloseSection()
	}
	f.CloseSection()
	f.OpenArraySection("grant_map")
	for _, g := range l.sortedGrants() {
		f.OpenObjectSection("entry")
		f.DumpString("id", g.Key)
		f.OpenObjectSection("grant")
		g.Grant.Dump(f, rel)
		f.CloseSection()
		f.CloseSection()
	}
	f.CloseSection()
}

// Dump is ACLGrant::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L275-L302):
// the type, the fields of its grantee, then the permission.
func (g Grant) Dump(f formatter.Formatter, rel denc.Release) {
	k := g.kind()
	f.OpenObjectSection("type")
	k.Dump(f, rel)
	f.CloseSection()
	switch k {
	case GranteeCanonUser:
		f.DumpString("id", g.ID)
		f.DumpString("name", g.Name)
	case GranteeEmail:
		f.DumpString("email", g.Email)
	case GranteeGroup:
		f.DumpInt("group", int64(int32(g.Group))) //nolint:gosec // C++ dumps static_cast<int>(group.type)
	case GranteeReferer:
		f.DumpString("url_spec", g.URLSpec)
	default:
	}
	f.OpenObjectSection("permission")
	g.Permission.Dump(f, rel)
	f.CloseSection()
}

// Dump is ACLPermission::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L265-L268):
// the flags as the int C++ stores.
func (p Permission) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpInt("flags", int64(int32(p))) //nolint:gosec // dump_int of the int C++ stores
}

// Dump is ACLGranteeType::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L270-L273).
func (t GranteeType) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpUnsigned("type", uint64(t))
}
