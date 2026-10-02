package policy

import "github.com/jhoblitt/rgw-go/internal/denc"

// Semantics selects the release whose policy rules rgw-go follows, so that a
// stored policy parses and evaluates as the zone's own radosgw parses and
// evaluates it. The zero value is v19.2.6's; each field takes v20.2.4's rule
// in its place. Citations without a file are src/rgw/rgw_iam_policy.cc.
type Semantics struct {
	// NullTestsValues: Null compares whether the key is absent, as a bool,
	// with the condition's values (:876-879 at v20.2.4), where v19.2.6
	// answers whether the key is absent and ignores them (:857-859).
	NullTestsValues bool
	// NotMeansNone: the Not operators hold when no pair of an environment
	// value and a condition value matches (multimap_none and typed_none, e.g.
	// :910-912 at v20.2.4), where v19.2.6 holds when some pair differs
	// (orrible and shortible over std::not_fn, e.g. :890-892).
	NotMeansNone bool
	// PublicNeedsWildcardPrincipal: is_public counts an Allow statement only
	// for a wildcard Principal (:1935-1947 at v20.2.4); v19.2.6 also counts
	// every other Allow statement whose NotPrincipal names no wildcard
	// (:1900-1917).
	PublicNeedsWildcardPrincipal bool
	// RejectAllowWithNotPrincipal: an Allow statement with a NotPrincipal
	// fails the parse, "Allow with NotPrincipal is not allowed." (:773-776 at
	// v20.2.4); v19.2.6 accepts it.
	RejectAllowWithNotPrincipal bool
	// ServicePrincipals: a "Service" principal names that service (:591-592
	// at v20.2.4); v19.2.6's parse_principal has no case for it and treats it
	// as an unsupported principal type (:583-586).
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
