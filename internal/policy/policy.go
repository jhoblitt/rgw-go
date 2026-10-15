package policy

import (
	"slices"
	"strconv"
	"strings"
)

// Version is rgw::IAM::Version
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy_keywords.h#L117-L120 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy_keywords.h#L118-L121 at v20.2.4),
// a policy's language version.
type Version uint8

// The versions, in rgw::IAM::Version order.
const (
	V2008_10_17 Version = iota //nolint:revive,staticcheck // "2008-10-17", radosgw's v2008_10_17
	V2012_10_17                //nolint:revive,staticcheck // "2012-10-17", radosgw's v2012_10_17
)

// String returns the version as a policy's Version spells it, "2008-10-17"
// or "2012-10-17".
func (v Version) String() string {
	switch v {
	case V2008_10_17:
		return "2008-10-17"
	case V2012_10_17:
		return "2012-10-17"
	}
	return "Version(" + strconv.Itoa(int(v)) + ")"
}

// Policy is rgw::IAM::Policy
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L581-L638 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L634-L691 at v20.2.4).
type Policy struct {
	// Text is the document the policy was parsed from, as stored.
	Text       string
	Version    Version
	ID         *string
	Statements []Statement
}

// Eval is Policy::eval
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1828-L1842 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1863-L1877 at v20.2.4):
// Deny when any statement denies, else Allow when any allows, else Pass.
func (p *Policy) Eval(env Env, id Identity, a Action, res *ARN, sem Semantics) Effect {
	allowed := false
	for i := range p.Statements {
		switch p.Statements[i].Eval(env, id, a, res, sem) {
		case Deny:
			return Deny
		case Allow:
			allowed = true
		}
	}
	if allowed {
		return Allow
	}
	return Pass
}

// IsPublic reports whether the policy is public as Amazon S3 judges a bucket
// policy
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-control-block-public-access.html#access-control-block-public-access-policy-status):
// some Allow statement grants every principal, and none of its conditions
// limits the grant to fixed values of a key that page lists. The answer is
// the same on every release. A statement's actions and resources never count:
// Amazon S3 judges a grant to everyone public even for an action it does not
// support.
func (p *Policy) IsPublic() bool {
	for i := range p.Statements {
		s := &p.Statements[i]
		if s.Effect == Allow && s.grantsEveryone() && !slices.ContainsFunc(s.Conditions, Condition.limitsGrant) {
			return true
		}
	}
	return false
}

// grantsEveryone is whether the statement's principals admit every
// requester: its Principal listed one that countsAsEveryone, whatever its
// NotPrincipal holds, since EvalPrincipal does not check a role against
// NotPrincipal; or it names no Principal, and a NotPrincipal that excepts
// some principals but not the wildcard. A statement naming neither applies
// to no requester of a bucket. What Principal listed includes a principal
// radosgw's set dropped; a NotPrincipal entry it drops never changes this
// answer, since the set keeps the wildcard and never empties a list.
func (s *Statement) grantsEveryone() bool {
	if s.droppedOpenPrincipal || slices.ContainsFunc(s.Principals, countsAsEveryone) {
		return true
	}
	return len(s.Principals) == 0 && len(s.NotPrincipals) > 0 && !slices.ContainsFunc(s.NotPrincipals, Principal.IsWildcard)
}

// countsAsEveryone is a Principal entry that grants every principal: the
// wildcard, or a principal that is not fixed.
func countsAsEveryone(p Principal) bool {
	return p.IsWildcard() || !fixedPrincipal(p)
}

// fixedPrincipal is a principal that names fixed identities, as Amazon S3
// asks of a principal, and not the anonymous requester. Its account, name
// and provider URL hold no wildcard, "*" or "?", and no policy variable,
// though rgw-go matches a name literally but for a subuser's ":*"
// (matchPrincipal, internal/authz/identity.go). One that is not counts as
// every principal.
func fixedPrincipal(p Principal) bool {
	for _, s := range [...]string{p.Account, p.ID, p.IDPURL} {
		if strings.ContainsAny(s, "*?") || strings.Contains(s, "${") {
			return false
		}
	}
	return !admitsAnonymous(p)
}

// anonymousUser is the user an unauthenticated request runs as, in the
// default tenant, the one named "" (rgw_get_anon_user,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_user.cc#L106-L111 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_user.cc#L106-L111 at v20.2.4).
const anonymousUser = "anonymous"

// admitsAnonymous is whether is_identity matches p for the anonymous
// requester, whose applier is a LocalApplier for anonymousUser
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth.cc#L1058-L1076 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth.cc#L1086-L1104 at v20.2.4):
// an account principal for the default tenant, which holds every user of
// that tenant, or a user principal for anonymousUser in it. The requester has
// no subuser, so of the subuser forms only ":*" matches it, and that holds a
// wildcard. Such a principal grants every unauthenticated request.
func admitsAnonymous(p Principal) bool {
	switch {
	case p.IsAccount():
		return p.Account == ""
	case p.IsUser():
		return p.Account == "" && p.ID == anonymousUser
	}
	return false
}

// limitingKey is a condition key Amazon S3 lists as limiting a grant.
type limitingKey struct {
	name string
	// ip: the key takes IpAddress alone, and the string and ARN operators
	// otherwise.
	ip bool
	// value is the test each of a condition's values must pass.
	value func(v string) bool
}

// limitingKeys are the keys Amazon S3 lists. Of its "AWS principal, user,
// role, or service principal" it names aws:PrincipalOrgID alone. Keys are
// matched without regard to ASCII case, as AWS matches condition keys.
var limitingKeys = [...]limitingKey{
	{name: "aws:SourceIp", ip: true, value: narrowRange},
	{name: "aws:SourceArn", value: fixedValue},
	{name: "aws:SourceVpc", value: fixedValue},
	{name: "aws:SourceVpce", value: fixedValue},
	{name: "aws:SourceOwner", value: fixedValue},
	{name: "aws:SourceAccount", value: fixedValue},
	{name: "aws:userid", value: fixedValue},
	{name: "aws:PrincipalOrgID", value: fixedValue},
	{name: "s3:DataAccessPointArn", value: func(v string) bool { return fixedValue(v) || accessPointsOfAccount(v) }},
	{name: "s3:DataAccessPointAccount", value: fixedValue},
}

// limitsGrant is whether c limits a grant to fixed values of a key Amazon S3
// lists. Its operator must hold only for a request whose key matches one of
// its values: not an IfExists one or ForAllValues, each of which holds for a
// request without the key, nor a negated one, which holds for every other
// value. A condition with no value limits nothing, though it holds for no
// request, so that a malformed block never makes a grant non-public.
func (c Condition) limitsGrant() bool {
	if c.IfExists || len(c.Values) == 0 {
		return false
	}
	i := slices.IndexFunc(limitingKeys[:], func(k limitingKey) bool { return asciiEqualFold(k.name, c.Key) })
	if i < 0 {
		return false
	}
	k := &limitingKeys[i]
	switch c.Op {
	case OpIPAddress:
		if !k.ip {
			return false
		}
	case OpStringEquals, OpStringEqualsIgnoreCase, OpStringLike, OpForAnyValueStringEquals,
		OpForAnyValueStringEqualsIgnoreCase, OpForAnyValueStringLike, OpArnEquals, OpArnLike:
		if k.ip {
			return false
		}
	default:
		return false
	}
	return !slices.ContainsFunc(c.Values, func(v string) bool { return !k.value(v) })
}

// fixedValue is a condition value holding no pattern byte of the matching
// operators' MatchWildcards, "*", "?", "[" or "\", and no policy variable,
// whatever the operator.
func fixedValue(v string) bool {
	return !strings.ContainsAny(v, `*?[\`) && !strings.Contains(v, "${")
}

// accessPointsOfAccount is an access point ARN whose name alone may hold a
// wildcard, arn:partition:service:region:account:accesspoint/name: the text
// before the name is fixed and names an account.
func accessPointsOfAccount(v string) bool {
	prefix, name, ok := strings.Cut(v, ":accesspoint/")
	parts := strings.Split(prefix, ":")
	return ok && len(parts) == 5 && parts[0] == "arn" && parts[4] != "" && fixedValue(prefix) && !strings.Contains(name, "${")
}

// ipv4Blocks are the IPv6 blocks that hold IPv4 addresses: ::/96, since
// MaskedIP.Equal compares an IPv4 address with an IPv6 range as ::a.b.c.d,
// and ::ffff:0:0/96, the IPv4-mapped form a dual-stack socket reports an IPv4
// peer in.
var ipv4Blocks = [...][16]byte{{}, {10: 0xff, 11: 0xff}}

// narrowRange is a range Amazon S3 does not judge very broad: an IPv4 /8 or
// longer, or an IPv6 /32 or longer. Its exception for RFC 1918's private
// ranges admits none, the broadest of them being a /8. An IPv6 range holding
// one of ipv4Blocks holds every IPv4 address, and one inside such a block is
// as broad as the IPv4 range it covers.
func narrowRange(v string) bool {
	m, ok := ParseMaskedIP(v)
	switch {
	case !ok:
		return false
	case !m.V6:
		return m.Prefix >= 8
	}
	for _, b := range ipv4Blocks {
		switch {
		case m.Prefix <= 96 && samePrefix(m.Addr, b, m.Prefix):
			return false
		case m.Prefix > 96 && samePrefix(m.Addr, b, 96):
			return m.Prefix-96 >= 8
		}
	}
	return m.Prefix >= 32
}

// samePrefix is whether a and b agree in their first n bits, n at most 128.
func samePrefix(a, b [16]byte, n uint) bool {
	ah, al := shiftRight(a, 128-n)
	bh, bl := shiftRight(b, 128-n)
	return ah == bh && al == bl
}

// HasConditionKeyPrefix is Policy::has_partial_conditional
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L630-L632 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L683-L685 at v20.2.4):
// whether some condition's key starts with prefix, folding ASCII case as
// boost::istarts_with does in the C locale radosgw runs in.
func (p *Policy) HasConditionKeyPrefix(prefix string) bool {
	return p.anyCondition(func(c *Condition) bool { return hasPrefixFold(c.Key, prefix) })
}

// HasConditionValuePrefix is Policy::has_partial_conditional_value
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L635-L637 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L688-L690 at v20.2.4):
// whether some condition value starts with prefix, folding ASCII case.
func (p *Policy) HasConditionValuePrefix(prefix string) bool {
	return p.anyCondition(func(c *Condition) bool {
		return slices.ContainsFunc(c.Values, func(v string) bool { return hasPrefixFold(v, prefix) })
	})
}

func (p *Policy) anyCondition(f func(c *Condition) bool) bool {
	for i := range p.Statements {
		for j := range p.Statements[i].Conditions {
			if f(&p.Statements[i].Conditions[j]) {
				return true
			}
		}
	}
	return false
}
