package policy

import (
	"slices"
	"strconv"
)

// Version is rgw::IAM::Version (src/rgw/rgw_iam_policy_keywords.h:117-120 at
// v19.2.6, :118-121 at v20.2.4), a policy's language version.
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

// Policy is rgw::IAM::Policy (src/rgw/rgw_iam_policy.h:581-638 at v19.2.6,
// :634-691 at v20.2.4).
type Policy struct {
	// Text is the document the policy was parsed from, as stored.
	Text       string
	Version    Version
	ID         *string
	Statements []Statement
}

// Eval is Policy::eval (src/rgw/rgw_iam_policy.cc:1828-1842 at v19.2.6,
// :1863-1877 at v20.2.4): Deny when any statement denies, else Allow when any
// allows, else Pass.
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

// iamAllEnv is the environment is_public evaluates a statement's conditions
// in (src/rgw/rgw_iam_policy.cc:1894-1898 at v19.2.6, :1929-1933 at v20.2.4).
var iamAllEnv = func() Env {
	var e Env
	e.Add("aws:SourceIp", "1.1.1.1")
	e.Add("aws:UserId", "anonymous")
	e.Add("s3:x-amz-server-side-encryption-aws-kms-key-id", "secret")
	return e
}()

// IsPublic is rgw::IAM::is_public (src/rgw/rgw_iam_policy.cc:1900-1922 at
// v19.2.6, :1935-1953 at v20.2.4) under sem: whether the policy counts as
// public, decided as the release sem describes decides it.
func (p *Policy) IsPublic(sem Semantics) bool {
	for i := range p.Statements {
		s := &p.Statements[i]
		if s.Effect != Allow {
			continue
		}
		if slices.ContainsFunc(s.Principals, Principal.IsWildcard) {
			if s.EvalConditions(iamAllEnv, sem) == Allow {
				return true
			}
			continue
		}
		if !sem.PublicNeedsWildcardPrincipal && !slices.ContainsFunc(s.NotPrincipals, Principal.IsWildcard) {
			return true
		}
	}
	return false
}

// HasConditionKeyPrefix is Policy::has_partial_conditional
// (src/rgw/rgw_iam_policy.h:630-632 at v19.2.6, :683-685 at v20.2.4): whether
// some condition's key starts with prefix, folding ASCII case as
// boost::istarts_with does in the C locale radosgw runs in.
func (p *Policy) HasConditionKeyPrefix(prefix string) bool {
	return p.anyCondition(func(c *Condition) bool { return hasPrefixFold(c.Key, prefix) })
}

// HasConditionValuePrefix is Policy::has_partial_conditional_value
// (src/rgw/rgw_iam_policy.h:635-637 at v19.2.6, :688-690 at v20.2.4): whether
// some condition value starts with prefix, folding ASCII case.
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
