package policy

import (
	"slices"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Identity is what Statement::eval_principal asks of rgw::auth::Identity
// (src/rgw/rgw_iam_policy.cc:1244-1276 at v19.2.6, :1248-1280 at v20.2.4).
type Identity interface {
	// IsIdentity is Identity::is_identity: whether p names this identity.
	IsIdentity(p Principal) bool
	// IdentityType is get_identity_type; meta.IdentityRole selects
	// EvalPrincipal's rules for a role.
	IdentityType() meta.IdentityType
}

// Effect is rgw::IAM::Effect (src/rgw/rgw_iam_policy_keywords.h:123-127 at
// v19.2.6, :124-128 at v20.2.4). Deny is numbered first, unlike radosgw's
// enum, so that the zero value is the effect radosgw gives a statement that
// names none (rgw_iam_policy.h:541 at v19.2.6, :594 at v20.2.4).
type Effect uint8

// The effects.
const (
	// Deny refuses the request, whatever allows it.
	Deny Effect = iota
	// Allow grants the request unless something denies it.
	Allow
	// Pass is the effect of a statement or policy that does not apply.
	Pass
)

// String returns the effect's name, "Allow", "Deny" or "Pass".
func (e Effect) String() string {
	switch e {
	case Deny:
		return "Deny"
	case Allow:
		return "Allow"
	case Pass:
		return "Pass"
	}
	return "Effect(" + strconv.Itoa(int(e)) + ")"
}

// Statement is rgw::IAM::Statement (src/rgw/rgw_iam_policy.h:533-559 at
// v19.2.6, :586-612 at v20.2.4). radosgw keeps the principals and resources
// in flat sets; evaluation only asks whether some entry matches, so their
// order and repeats do not matter here.
type Statement struct {
	Sid           *string
	Principals    []Principal
	NotPrincipals []Principal
	// Effect is what the statement gives a request it applies to; one that
	// names none denies, as radosgw's does.
	Effect       Effect
	Actions      ActionSet
	NotActions   ActionSet
	Resources    []ARN
	NotResources []ARN
	Conditions   []Condition
}

// Eval is Statement::eval (src/rgw/rgw_iam_policy.cc:1192-1233 at v19.2.6,
// :1196-1237 at v20.2.4): the statement's Effect when it applies to id, a and
// res and every condition holds in env under sem, and Pass otherwise. A nil id
// is a request evaluated without an identity, as radosgw evaluates identity
// policies (src/rgw/rgw_common.cc:1153 at v19.2.6, :1166 at v20.2.4); a nil
// res is a request without a resource. A statement applies to a resource only
// when it names Resource or NotResource, and to a request without a resource
// only when it names neither. When it names Resource, NotResource is not
// consulted.
func (s *Statement) Eval(env Env, id Identity, a Action, res *ARN, sem Semantics) Effect {
	if s.EvalPrincipal(id) == Deny {
		return Pass
	}
	switch {
	case res == nil:
		if len(s.Resources) > 0 || len(s.NotResources) > 0 {
			return Pass
		}
	case len(s.Resources) > 0:
		if !matchesAny(s.Resources, *res) {
			return Pass
		}
	case len(s.NotResources) > 0:
		if matchesAny(s.NotResources, *res) {
			return Pass
		}
	default:
		return Pass
	}
	if !s.Actions.Has(a) || s.NotActions.Has(a) {
		return Pass
	}
	if s.EvalConditions(env, sem) == Allow {
		return s.Effect
	}
	return Pass
}

// EvalPrincipal is Statement::eval_principal (src/rgw/rgw_iam_policy.cc:
// 1244-1276 at v19.2.6, :1248-1280 at v20.2.4): Allow when the statement
// applies to id, Deny when it does not. Every statement applies to a nil id,
// and none that names neither Principal nor NotPrincipal applies to any other.
// An identity must be named by a non-empty Principal and must not be named by
// NotPrincipal, except that a role named by a non-empty Principal is not
// checked against NotPrincipal at all. The principal type radosgw reports
// alongside, for session policies, is not computed.
func (s *Statement) EvalPrincipal(id Identity) Effect {
	if id == nil {
		return Allow
	}
	if len(s.Principals) == 0 && len(s.NotPrincipals) == 0 {
		return Deny
	}
	if len(s.Principals) > 0 && !namesIdentity(s.Principals, id) {
		return Deny
	}
	if id.IdentityType() == meta.IdentityRole && len(s.Principals) > 0 {
		return Allow
	}
	if namesIdentity(s.NotPrincipals, id) {
		return Deny
	}
	return Allow
}

// EvalConditions is Statement::eval_conditions (src/rgw/rgw_iam_policy.cc:
// 1278-1285 at v19.2.6, :1282-1289 at v20.2.4): Allow when every condition
// holds in env under sem, Deny otherwise.
func (s *Statement) EvalConditions(env Env, sem Semantics) Effect {
	for _, c := range s.Conditions {
		if !c.Eval(env, sem) {
			return Deny
		}
	}
	return Allow
}

func matchesAny(patterns []ARN, res ARN) bool {
	return slices.ContainsFunc(patterns, func(p ARN) bool { return p.Match(res) })
}

// namesIdentity is is_identity (src/rgw/rgw_iam_policy.cc:1235-1242 at
// v19.2.6, :1239-1246 at v20.2.4).
func namesIdentity(ps []Principal, id Identity) bool {
	return slices.ContainsFunc(ps, id.IsIdentity)
}
