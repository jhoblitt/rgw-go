package policy

import "github.com/jhoblitt/rgw-go/internal/denc"

// Semantics selects the release whose policy rules rgw-go follows, so that a
// stored policy parses and evaluates as the zone's own radosgw parses and
// evaluates it. The zero value is v19.2.6's; each field takes v20.2.4's rule
// in its place.
type Semantics struct {
	// NullTestsValues: Null compares whether the key is absent, as a bool,
	// with the condition's values
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L876-L879 at v20.2.4),
	// where v19.2.6 answers whether the key is absent and ignores them
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L857-L859).
	NullTestsValues bool
	// NotMeansNone: the Not operators hold when no pair of an environment
	// value and a condition value matches (multimap_none and typed_none, e.g.
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L910-L912 at v20.2.4),
	// where v19.2.6 holds when some pair differs (orrible and shortible over
	// std::not_fn, e.g.
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L890-L892).
	NotMeansNone bool
	// PublicNeedsWildcardPrincipal: is_public counts an Allow statement only
	// for a wildcard Principal
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1935-L1947 at v20.2.4);
	// v19.2.6 also counts every other Allow statement whose NotPrincipal names
	// no wildcard
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1900-L1917).
	PublicNeedsWildcardPrincipal bool
	// RejectAllowWithNotPrincipal: an Allow statement with a NotPrincipal
	// fails the parse, "Allow with NotPrincipal is not allowed."
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L773-L776 at v20.2.4);
	// v19.2.6 accepts it.
	RejectAllowWithNotPrincipal bool
	// ServicePrincipals: a "Service" principal names that service
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L591-L592 at v20.2.4);
	// v19.2.6's parse_principal has no case for it and treats it as an
	// unsupported principal type
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L583-L586).
	ServicePrincipals bool
	// FindKeepsFirstValue: Env.Find answers as the environment's
	// unordered_multimap does in v20.2.4, built with GCC 13.3, whose emplace
	// keeps a key's first value first while the table holds 20 pairs or
	// fewer; v19.2.6, built with GCC 11.5, links each new value first, so
	// find returns the value added last. Env's doc cites the libstdc++ lines.
	// The rule follows the compiler, not the release: SemanticsFor models
	// upstream's el9 packages, and a Squid radosgw built with GCC 12 or later
	// behaves like Tentacle.
	FindKeepsFirstValue bool
}

// SemanticsFor returns r's Semantics: every rule v19.2.6's for Squid, and
// v20.2.4's for Tentacle.
func SemanticsFor(r denc.Release) Semantics {
	tentacle := r >= denc.Tentacle
	return Semantics{
		NullTestsValues:              tentacle,
		NotMeansNone:                 tentacle,
		PublicNeedsWildcardPrincipal: tentacle,
		RejectAllowWithNotPrincipal:  tentacle,
		ServicePrincipals:            tentacle,
		FindKeepsFirstValue:          tentacle,
	}
}
