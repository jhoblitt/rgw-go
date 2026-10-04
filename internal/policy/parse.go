package policy

import (
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// ParseOptions are Policy's constructor arguments
// (src/rgw/rgw_iam_policy.cc:1815-1826 at v19.2.6, :1850-1861 at v20.2.4).
type ParseOptions struct {
	// Tenant gives its value to every resource ARN whose account is "" or
	// "*", and refuses a resource of any account but its own; nil accepts
	// every account unchanged, as radosgw does for an account user's
	// identity policies and for the managed policies.
	Tenant *string
	// RejectInvalidPrincipals fails the parse on a principal radosgw does not
	// support, where it would otherwise be logged and dropped: PutBucketPolicy
	// passes rgw_policy_reject_invalid_principals, a stored policy false.
	RejectInvalidPrincipals bool
	// Release selects the actions a policy may name (Known) and the release's
	// parse rules (Semantics).
	Release denc.Release
}

// ParseError is PolicyParseException (src/rgw/rgw_iam_policy.h:563-579 at
// v19.2.6, :616-632 at v20.2.4). Offset is rapidjson's: a byte offset into
// the policy text, comments included. Annotation is the parser's own message
// when it refused the document, and rapidjson's message for a syntax error.
type ParseError struct {
	Offset     int64
	Annotation string
}

// Error is radosgw's message, which PutBucketPolicy and the IAM APIs return
// to the client: "At character offset N, <annotation>".
func (e *ParseError) Error() string {
	return "At character offset " + strconv.FormatInt(e.Offset, 10) + ", " + e.Annotation
}

// Parse parses an IAM policy document as radosgw's PolicyParser does, driven
// by rapidjson's reader with comments allowed and numbers kept as their text.
// Policy.Text is text exactly as given, which PutBucketPolicy stores; the
// parse ends at text's first NUL byte, as rapidjson's StringStream does.
// Principals and resources keep document order, which evaluation does not
// depend on; the principals radosgw's set takes for equal are kept once.
func Parse(text string, opts ParseOptions) (*Policy, error) {
	pol := &Policy{Text: text}
	h := &parser{
		opts:       opts,
		sem:        SemanticsFor(opts.Release),
		policy:     pol,
		annotation: "No error?",
	}
	r := &jsonReader{src: cString(text), h: h}
	if err := r.parse(); err != nil {
		return nil, err
	}
	return pol, nil
}

// tokenKind is rgw::IAM::TokenKind (src/rgw/rgw_iam_policy_keywords.h:9-12
// at v19.2.6 and v20.2.4).
type tokenKind uint8

const (
	kindPseudo tokenKind = iota
	kindTop
	kindStatement
	kindCondOp
	kindCondKey
	kindVersionKey
	kindEffectKey
	kindPrincType
)

// tokenID is the subset of rgw::IAM::TokenID the parser tells apart by
// identity; the condition operators, versions and effects are told apart
// by keyword.specific instead.
type tokenID uint8

const (
	tokTop tokenID = iota
	tokVersion
	tokID
	tokStatement
	tokSid
	tokEffect
	tokPrincipal
	tokNotPrincipal
	tokAction
	tokNotAction
	tokResource
	tokNotResource
	tokCondition
	tokAWS
	tokFederated
	tokService
	tokCanonicalUser
	tokOther
)

// seenBit is PolicyParser::dex (src/rgw/rgw_iam_policy.cc:277-314 at
// v19.2.6, :287-324 at v20.2.4): Version is 0x1 and each id up to
// CanonicalUser the next bit.
func seenBit(id tokenID) uint32 { return 1 << (id - tokVersion) }

// statementBits are the keys a statement may name once, which set and reset
// keep in PolicyParser::v: every key from Sid on, the principal types
// included.
const statementBits uint32 = (1<<(tokOther-tokVersion) - 1) &^ (1<<(tokSid-tokVersion) - 1)

// keyword is rgw::IAM::Keyword (rgw_iam_policy_keywords.gperf:8-15).
type keyword struct {
	name       string
	kind       tokenKind
	id         tokenID
	specific   uint8
	arrayable  bool
	objectable bool
}

// The pseudo keywords for the document's top level and for a condition key
// (src/rgw/rgw_iam_policy.cc:219-222 at v19.2.6, :229-232 at v20.2.4).
var (
	kwTop     = &keyword{name: "<Top>", kind: kindPseudo, id: tokTop}
	kwCondKey = &keyword{name: "<Condition Key>", kind: kindCondKey, id: tokOther, arrayable: true}
)

// keywords is rgw_iam_policy_keywords.gperf's table (:17-138 at v19.2.6,
// :17-139 at v20.2.4, where the one added line is a comment), which looks up
// a key or string exactly. A key or string holding a NUL is no keyword in
// radosgw either: gperf's hash of its length and its first and fourth bytes
// never places it on the keyword it starts with.
var keywords = func() map[string]*keyword {
	ks := []*keyword{
		{name: "Version", kind: kindTop, id: tokVersion},
		{name: "Id", kind: kindTop, id: tokID},
		{name: "Statement", kind: kindTop, id: tokStatement, arrayable: true, objectable: true},
		{name: "Sid", kind: kindStatement, id: tokSid},
		{name: "Effect", kind: kindStatement, id: tokEffect},
		{name: "Principal", kind: kindStatement, id: tokPrincipal, objectable: true},
		{name: "NotPrincipal", kind: kindStatement, id: tokNotPrincipal, arrayable: true, objectable: true},
		{name: "Action", kind: kindStatement, id: tokAction, arrayable: true},
		{name: "NotAction", kind: kindStatement, id: tokNotAction, arrayable: true},
		{name: "Resource", kind: kindStatement, id: tokResource, arrayable: true},
		{name: "NotResource", kind: kindStatement, id: tokNotResource, arrayable: true},
		{name: "Condition", kind: kindStatement, id: tokCondition, arrayable: true, objectable: true},
		{name: "2008-10-17", kind: kindVersionKey, id: tokOther, specific: uint8(V2008_10_17)},
		{name: "2012-10-17", kind: kindVersionKey, id: tokOther, specific: uint8(V2012_10_17)},
		{name: "Allow", kind: kindEffectKey, id: tokOther, specific: uint8(Allow)},
		{name: "Deny", kind: kindEffectKey, id: tokOther, specific: uint8(Deny)},
		{name: "AWS", kind: kindPrincType, id: tokAWS, arrayable: true},
		{name: "Federated", kind: kindPrincType, id: tokFederated, arrayable: true},
		{name: "Service", kind: kindPrincType, id: tokService, arrayable: true},
		{name: "CanonicalUser", kind: kindPrincType, id: tokCanonicalUser, arrayable: true},
	}
	for op, name := range operatorNames {
		ks = append(ks, &keyword{
			name: name, kind: kindCondOp, id: tokOther, specific: uint8(op), arrayable: true, objectable: true,
		})
	}
	m := make(map[string]*keyword, len(ks))
	for _, k := range ks {
		m[k.name] = k
	}
	return m
}()

// parseState is ParseState (src/rgw/rgw_iam_policy.cc:224-260 at v19.2.6,
// :234-270 at v20.2.4): the keyword whose value is being read, and whether
// that value is an array or object still open.
type parseState struct {
	w            *keyword
	arraying     bool
	objecting    bool
	condIfExists bool
}

// parser is PolicyParser and its ParseState methods
// (src/rgw/rgw_iam_policy.cc:262-809 at v19.2.6, :272-828 at v20.2.4), the
// handler rapidjson drives. A method that refuses an event sets annotation
// and returns false.
type parser struct {
	opts   ParseOptions
	sem    Semantics
	policy *Policy
	s      []parseState
	// seen holds the keys already given where each may appear once; v holds
	// the ones of the current statement, which an object closing inside an
	// array clears from seen.
	seen, v    uint32
	annotation string
}

func (p *parser) top() *parseState { return &p.s[len(p.s)-1] }

func (p *parser) pop() { p.s = p.s[:len(p.s)-1] }

func (p *parser) push(k *keyword) { p.s = append(p.s, parseState{w: k}) }

func (p *parser) refuse(annotation string) bool {
	p.annotation = annotation
	return false
}

func (p *parser) refusal() string { return p.annotation }

// literal is PolicyParser's own Default override, which rapidjson's
// BaseReaderHandler::Null and Bool call: it refuses them without an
// annotation of its own (src/rgw/rgw_iam_policy.cc:436-438 at v19.2.6,
// :446-448 at v20.2.4), so the message is radosgw's initial "No error?"
// (docs/ceph-upstream-bugs.md, the entry on JSON true, false and null).
func (p *parser) literal() bool { return false }

func (p *parser) statement() *Statement {
	if n := len(p.policy.Statements); n > 0 {
		return &p.policy.Statements[n-1]
	}
	return nil
}

func (p *parser) set(id tokenID) {
	p.seen |= seenBit(id)
	if seenBit(id)&statementBits != 0 {
		p.v |= seenBit(id)
	}
}

// resetStatement is ParseState::reset, which obj_end calls when any object
// closes inside an array: it forgets the keys the current statement gave.
// For an object in a Condition, NotPrincipal or condition operator array,
// not just in the Statement array, that lets the statement give them again
// (docs/ceph-upstream-bugs.md, "radosgw's policy parser forgets a
// statement's keys when an object in an array closes").
func (p *parser) resetStatement() {
	p.seen &^= p.v
	p.v = 0
}

func (p *parser) startObject() bool {
	if len(p.s) == 0 {
		p.s = append(p.s, parseState{w: kwTop, objecting: true})
		return true
	}
	st := p.top()
	if !st.w.objectable || st.objecting {
		return p.refuse("The " + st.w.name + " keyword cannot introduce an object.")
	}
	st.objecting = true
	if st.w.id == tokStatement {
		p.policy.Statements = append(p.policy.Statements, Statement{})
	}
	return true
}

func (p *parser) endObject() bool {
	if len(p.s) == 0 {
		return p.refuse("Attempt to end unopened object at top level.")
	}
	st := p.top()
	if !st.objecting {
		return p.refuse("Attempt to end unopened object on keyword `" + st.w.name + "`.")
	}
	st.objecting = false
	if st.arraying {
		p.resetStatement()
	} else {
		p.pop()
	}
	return true
}

func (p *parser) startArray() bool {
	if len(p.s) == 0 {
		return p.refuse("Array not allowed at top level.")
	}
	st := p.top()
	if !st.w.arrayable || st.arraying {
		return p.refuse("`" + st.w.name + "` does not take array.")
	}
	st.arraying = true
	return true
}

func (p *parser) endArray() bool {
	if len(p.s) == 0 {
		return false
	}
	st := p.top()
	if !st.arraying || st.objecting {
		return p.refuse("Attempt to close unopened array.")
	}
	p.pop()
	return true
}

// key is ParseState::key. Under Condition an IfExists suffix is cut before
// the lookup; under a condition operator any key no keyword has is a
// condition key.
func (p *parser) key(s string) bool {
	if len(p.s) == 0 {
		return p.refuse("Key not allowed at top level.")
	}
	st := p.top()
	token, ifExists := s, false
	if st.w.id == tokCondition {
		token, ifExists = strings.CutSuffix(s, "IfExists")
	}
	k := keywords[token]
	if k == nil {
		if st.w.kind != kindCondOp {
			return p.refuse("Unknown key `" + token + "`.")
		}
		c := Condition{Op: Operator(st.w.specific), Key: s, IfExists: st.condIfExists}
		p.push(kwCondKey)
		t := p.statement()
		t.Conditions = append(t.Conditions, c)
		return true
	}
	expected := st.w.id == tokTop && k.kind == kindTop ||
		st.w.id == tokStatement && k.kind == kindStatement ||
		(st.w.id == tokPrincipal || st.w.id == tokNotPrincipal) && k.kind == kindPrincType
	switch {
	case expected && p.seen&seenBit(k.id) == 0:
		p.set(k.id)
		p.push(k)
		return true
	case st.w.id == tokCondition && k.kind == kindCondOp:
		p.push(k)
		p.top().condIfExists = ifExists
		return true
	}
	return p.refuse("Token `" + k.name + "` is not allowed in the context of `" + st.w.name + "`.")
}

// str is ParseState::do_string.
func (p *parser) str(s string) bool {
	if len(p.s) == 0 {
		return p.refuse("String not allowed at top level.")
	}
	st := p.top()
	t := p.statement()
	wildcard := s != "" && s[0] == '*'
	isAction, validAction := false, false
	switch {
	case st.w.id == tokVersion:
		k := keywords[s]
		if k == nil || k.kind != kindVersionKey {
			return p.refuse("`" + s + "` is not a valid version. Valid versions are `2008-10-17` and `2012-10-17`.")
		}
		p.policy.Version = Version(k.specific)
	case st.w.id == tokID:
		p.policy.ID = &s
	case st.w.id == tokSid:
		t.Sid = &s
	case st.w.id == tokEffect:
		k := keywords[s]
		if k == nil || k.kind != kindEffectKey {
			return p.refuse("`" + s + "` is not a valid effect.")
		}
		t.Effect = Effect(k.specific)
	// radosgw tests only a Principal's, NotPrincipal's or Action's first
	// byte for "*" (docs/ceph-upstream-bugs.md, "radosgw takes any policy
	// Action starting with a wildcard for every action").
	case st.w.id == tokPrincipal && wildcard:
		t.Principals = addPrincipal(t.Principals, WildcardPrincipal())
	case st.w.id == tokNotPrincipal && wildcard:
		t.NotPrincipals = addPrincipal(t.NotPrincipals, WildcardPrincipal())
	case st.w.id == tokAction || st.w.id == tokNotAction:
		isAction = true
		dst := &t.Actions
		if st.w.id == tokNotAction {
			dst = &t.NotActions
		}
		if wildcard {
			*dst, validAction = AllValue(), true
		} else {
			validAction = MatchAction(dst, s, p.opts.Release)
		}
	case st.w.id == tokResource || st.w.id == tokNotResource:
		a, ok := ParseARN(s, true)
		if !ok {
			return p.refuse("`" + s + "` is not a valid ARN. Resource ARNs should have a format like " +
				"`arn:aws:s3::tenant:resource' or `arn:aws:s3:::resource`.")
		}
		tenant := p.opts.Tenant
		if a.Account != "" && tenant != nil && a.Account != *tenant && a.Account != "*" {
			return p.refuse("Policy owned by tenant `" + *tenant + "` cannot grant access to resource owned by tenant `" +
				a.Account + "`.")
		}
		if tenant != nil && (a.Account == "" || a.Account == "*") {
			a.Account = *tenant
		}
		if st.w.id == tokResource {
			t.Resources = addResource(t.Resources, a)
		} else {
			t.NotResources = addResource(t.NotResources, a)
		}
	case st.w.kind == kindCondKey:
		c := &t.Conditions[len(t.Conditions)-1]
		if s != "" && s[0] == '$' {
			if len(s) < 2 || s[1] != '{' || s[len(s)-1] != '}' {
				return p.refuse("Invalid interpolation `" + s + "`.")
			}
			c.IsRuntime = true
		}
		c.Values = append(c.Values, s)
	case st.w.kind == kindPrincType:
		if len(p.s) <= 1 {
			return p.refuse("Principle isn't allowed at top level.")
		}
		pr, errmsg, ok := p.principal(st.w, s)
		switch {
		case ok && p.s[len(p.s)-2].w.id == tokPrincipal:
			t.Principals = addPrincipal(t.Principals, pr)
		case ok:
			t.NotPrincipals = addPrincipal(t.NotPrincipals, pr)
		case p.opts.RejectInvalidPrincipals:
			return p.refuse(errmsg)
		default:
			slog.Warn("ignored policy principal", slog.String("principal", s), slog.String("reason", errmsg))
		}
	default:
		// v20.2.4 asserts a statement exists before this branch and aborts
		// on a string under Statement; rgw-go refuses it as v19.2.6 does
		// (docs/exclusions.md).
		return p.refuse("`" + s + "` is not valid in the context of `" + st.w.name + "`.")
	}

	if !st.arraying {
		p.pop()
	}
	if isAction && !validAction {
		return p.refuse("`" + s + "` is not a valid action.")
	}
	if p.sem.RejectAllowWithNotPrincipal && t != nil && t.Effect == Allow && len(t.NotPrincipals) > 0 {
		return p.refuse("Allow with NotPrincipal is not allowed.")
	}
	return true
}

// rawNumber is ParseState::number: a number is only a condition value.
func (p *parser) rawNumber(s string) bool {
	if len(p.s) == 0 {
		return p.refuse("Number not allowed at top level.")
	}
	st := p.top()
	if st.w.kind != kindCondKey {
		return p.refuse("Numbers are not allowed outside condition arguments.")
	}
	t := p.statement()
	c := &t.Conditions[len(t.Conditions)-1]
	c.Values = append(c.Values, s)
	if !st.arraying {
		p.pop()
	}
	return true
}

// unsupportedARN is parse_principal's message for an AWS or Federated
// principal it cannot read.
const unsupportedARN = "` is not a supported AWS or Federated ARN. Supported ARNs are forms like: " +
	"`arn:aws:iam::tenant:root` or a bare tenant name for a tenant, " +
	"`arn:aws:iam::tenant:role/role-name` for a role, " +
	"`arn:aws:sts::tenant:assumed-role/role-name/role-session-name` for an assumed role, " +
	"`arn:aws:iam::tenant:user/user-name` for a user, " +
	"`arn:aws:iam::tenant:oidc-provider/idp-url` for OIDC."

// principal is ParseState::parse_principal (src/rgw/rgw_iam_policy.cc:
// 521-587 at v19.2.6, :531-599 at v20.2.4) for a value under principal type
// w; errmsg says why it is not one radosgw supports.
func (p *parser) principal(w *keyword, s string) (pr Principal, errmsg string, ok bool) {
	switch w.id {
	case tokAWS, tokFederated:
		if w.id == tokAWS && s == "*" {
			return WildcardPrincipal(), "", true
		}
		if a, ok := ParseARN(s, false); ok {
			if a.Resource == "root" {
				return AccountPrincipal(a.Account), "", true
			}
			if typ, name, found := strings.Cut(a.Resource, "/"); found {
				switch typ {
				case "user":
					return UserPrincipal(a.Account, name), "", true
				case "role":
					return RolePrincipal(a.Account, name), "", true
				case "oidc-provider":
					return OIDCProviderPrincipal(name), "", true
				case "assumed-role":
					return AssumedRolePrincipal(a.Account, name), "", true
				}
			}
		} else if !strings.ContainsAny(s, ":/") {
			return AccountPrincipal(s), "", true
		}
		return Principal{}, "`" + s + unsupportedARN, false
	case tokCanonicalUser:
		return Principal{}, "RGW does not support canonical users.", false
	case tokService:
		if p.sem.ServicePrincipals {
			return ServicePrincipal(s), "", true
		}
	}
	return Principal{}, "RGW does not support principals of type `" + w.name + "`.", false
}

// addPrincipal adds pr to ps unless radosgw's principal set holds an equal
// one already. Principal's == and < compare only the kind and the rgw_user,
// tenant and id (src/rgw/rgw_basic_types.h:228-234 at v19.2.6, :245-251 at
// v20.2.4), which an OIDC provider and a service leave empty, so the set
// keeps the first of them and drops the rest (docs/ceph-upstream-bugs.md,
// "radosgw drops all but the first OIDC provider or Service principal in a
// statement").
func addPrincipal(ps []Principal, pr Principal) []Principal {
	key := func(q Principal) Principal {
		if q.Kind == PrincipalService {
			q.ID = ""
		}
		q.IDPURL = ""
		return q
	}
	k := key(pr)
	if slices.ContainsFunc(ps, func(q Principal) bool { return key(q) == k }) {
		return ps
	}
	return append(ps, pr)
}

// addResource adds a to as unless it is there already. radosgw's flat_set
// orders ARNs inconsistently and may keep a repeat (docs/ceph-upstream-bugs.md,
// "radosgw's ARN ordering is not a strict weak ordering"), which evaluation
// cannot tell from one entry.
func addResource(as []ARN, a ARN) []ARN {
	if slices.Contains(as, a) {
		return as
	}
	return append(as, a)
}
