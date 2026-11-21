package policy

import (
	"slices"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Identity is what Statement::eval_principal asks of rgw::auth::Identity
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1244-L1276 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1248-L1280 at v20.2.4).
type Identity interface {
	// IsIdentity is Identity::is_identity: whether p names this identity.
	IsIdentity(p Principal) bool
	// IdentityType is get_identity_type; meta.IdentityRole selects
	// EvalPrincipal's rules for a role.
	IdentityType() meta.IdentityType
}

// Effect is rgw::IAM::Effect
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy_keywords.h#L123-L127 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy_keywords.h#L124-L128 at v20.2.4).
// Deny is numbered first, unlike radosgw's enum, so that the zero value is
// the effect radosgw gives a statement that names none
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L541 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L594 at v20.2.4).
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

// Statement is rgw::IAM::Statement
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L533-L559 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L586-L612 at v20.2.4).
// radosgw keeps the principals and resources in flat sets; evaluation only
// asks whether some entry matches, so their order and repeats do not matter
// here.
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

	// droppedOpenPrincipal is set when Principal listed a principal that
	// counts as every principal for IsPublic, and radosgw's principal set
	// dropped it as equal to one already held (addPrincipal). Evaluation
	// never sees it, as radosgw's does not.
	droppedOpenPrincipal bool
}

// Eval is Statement::eval
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1192-L1233 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1196-L1237 at v20.2.4):
// the statement's Effect when it applies to id, a and res and every condition
// holds in env under sem, and Pass otherwise. A nil id is a request evaluated
// without an identity, as radosgw evaluates identity policies
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1153 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1166 at v20.2.4);
// a nil res is a request without a resource. A statement applies to a resource
// only when it names Resource or NotResource, and to a request without a
// resource only when it names neither. When it names Resource, NotResource is
// not consulted. A Deny that names only NotAction covers actions as
// coversAction says, not as Statement::eval does.
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
	if !s.coversAction(a) {
		return Pass
	}
	if s.EvalConditions(env, sem) == Allow {
		return s.Effect
	}
	return Pass
}

// coversAction is whether the statement applies to action a. radosgw's
// Statement::eval needs a in Action and not in NotAction
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L1222 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1226 at v20.2.4),
// and its parser fills Action from an Action element alone, so there a
// statement that names only NotAction applies to no action. Here a Deny that
// names only NotAction applies to every action its NotAction does not name,
// as Amazon S3 applies NotAction
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_notaction.html),
// so that it refuses what it states; an Allow keeps radosgw's test, so that
// rgw-go never grants what a radosgw serving the same zone refuses
// (docs/differences.md, "A Deny that names NotAction and no Action refuses
// every action its NotAction does not name"). An Action or NotAction that
// names no action counts as none. A NotAction string that starts with "*"
// names every action, as radosgw's parser takes it, so a Deny naming only
// such a NotAction covers none. A value past the actions, such as ActionNone,
// is no action a Deny covers.
func (s *Statement) coversAction(a Action) bool {
	if s.Effect == Deny && s.Actions.IsZero() && !s.NotActions.IsZero() {
		return a < ActionCount && !s.NotActions.Has(a)
	}
	return s.Actions.Has(a) && !s.NotActions.Has(a)
}

// EvalPrincipal is Statement::eval_principal
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1244-L1276 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1248-L1280 at v20.2.4):
// Allow when the statement applies to id, Deny when it does not. Every
// statement applies to a nil id, and none that names neither Principal nor
// NotPrincipal applies to any other. An identity must be named by a
// non-empty Principal and must not be named by NotPrincipal, except that a
// role named by a non-empty Principal is not checked against NotPrincipal at
// all. The principal type radosgw reports alongside, for session policies,
// is not computed.
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

// EvalConditions is Statement::eval_conditions
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1278-L1285 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1282-L1289 at v20.2.4):
// Allow when every condition holds in env under sem, Deny otherwise. A
// statement that allows judges each condition as radosgw does, on env as
// built, which is radosgw's environment. One that denies judges each as
// Condition.EvalFor judges a Deny's, where Amazon S3's evaluation or
// radosgw's holds, on Amazon S3's request context, env with the values
// env.SetForDeny sets, and with Amazon S3's reading of a negated operator
// over several values (onAmazon); likewise on each second reading of a key
// Amazon's texts leave open (env.SetForDenyOr, env.SetForDenyOrAbsent); and
// as radosgw does on env as built; and the condition holds where any holds.
// So a Deny reads the keys Amazon S3 sets beside radosgw's, and a key radosgw
// leaves out is not absent to it where Amazon S3's request context holds it.
// A Deny statement is also judged on env without the keys env.MarkUnsent
// names, those radosgw adds for a header the request does not send, and its
// conditions hold where they all hold on either, so that a Deny written for
// the header's absence, by Null or an IfExists operator, still refuses.
func (s *Statement) EvalConditions(env Env, sem Semantics) Effect {
	if s.conditionsHold(env, sem) {
		return Allow
	}
	if s.Effect == Deny {
		if bare, ok := env.withoutUnsent(); ok && s.conditionsHold(bare, sem) {
			return Allow
		}
	}
	return Deny
}

func (s *Statement) conditionsHold(env Env, sem Semantics) bool {
	if s.Effect != Deny {
		for _, c := range s.Conditions {
			if !c.EvalFor(env, sem, s.Effect) {
				return false
			}
		}
		return true
	}
	if len(s.Conditions) == 0 {
		return true
	}
	amazon := env.forDeny()
	others := env.forDenyOr()
	for _, c := range s.Conditions {
		onAmazon := c.onAmazon(sem)
		holds := func(ctx Env) bool { return c.EvalFor(ctx, onAmazon, Deny) }
		if !holds(amazon) && !slices.ContainsFunc(others, holds) && !c.Eval(env, sem) {
			return false
		}
	}
	return true
}

// onAmazon is sem for c judged on Amazon S3's request context, where a
// negated operator over several values holds only when the key matches none
// of them, on either release: "When multiple values are listed in a policy
// for negated matching condition operators such as StringNotEquals and
// DateNotEquals, the effective permissions work like a logical AND"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_multi-value-conditions.md?plain=1#L36).
// Squid's radosgw holds one where some value differs, and its judgement of
// its own environment still does. Values that name policy variables keep
// sem: EvalFor reads them as Amazon S3 does already, and radosgw's reading of
// them compares the key with another key's values, not with the list.
func (c Condition) onAmazon(sem Semantics) Semantics {
	if len(c.Values) > 1 && !c.IsRuntime {
		sem.NotMeansNone = true
	}
	return sem
}

func matchesAny(patterns []ARN, res ARN) bool {
	return slices.ContainsFunc(patterns, func(p ARN) bool { return p.Match(res) })
}

// namesIdentity is is_identity
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L1235-L1242 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1239-L1246 at v20.2.4).
func namesIdentity(ps []Principal, id Identity) bool {
	return slices.ContainsFunc(ps, id.IsIdentity)
}
