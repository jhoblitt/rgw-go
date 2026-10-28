package postpolicy

import (
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/jsonspirit"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

var (
	// ErrInvalidArgument is from_json's -EINVAL, which get_policy answers
	// 400 InvalidArgument.
	ErrInvalidArgument = errors.New("postpolicy: invalid argument")
	// ErrAccessDenied is check's -EACCES, which get_policy answers 403
	// AccessDenied.
	ErrAccessDenied = errors.New("postpolicy: access denied")
)

// Error is a refusal. Err is ErrInvalidArgument or ErrAccessDenied. Message
// is the text get_policy sets in s->err.message, which quotes the policy's
// operators and names and the form's field names as they are. Detail says
// why the reader refused a document whose Message is "Malformed JSON".
type Error struct {
	Err     error
	Message string
	Detail  string
}

// Error returns Err's text, Message, and Detail when set.
func (e *Error) Error() string {
	s := e.Err.Error() + ": " + e.Message
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// Unwrap returns Err.
func (e *Error) Unwrap() error { return e.Err }

// The messages from_json and check set, with get_policy's for a refusal
// from_json gives none
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L189-L305 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L198-L314 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3210-L3214 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3346-L3350 at v20.2.4).
const (
	msgMalformed     = "Malformed JSON"
	msgNoExpiration  = "Policy missing expiration"
	msgBadExpiration = "Failed to parse policy expiration"
	msgNoConditions  = "Policy missing conditions"
	msgBadArray      = "Bad condition array, expecting 3 arguments"
	msgBadLength     = "Bad content-length-range param"
	msgBadOperator   = "Invalid condition: "
	msgUnparsed      = "Failed to parse policy"
	msgExpired       = "Policy expired"
	msgNotFound      = "Policy check failed, variable not found: "
	msgNotMet        = "Policy check failed, variable not met condition: "
	msgEqFailed      = "Policy condition failed: eq"
	msgPrefixFailed  = "Policy condition failed: starts-with"
	msgUnchecked     = "Policy missing condition: "
)

// Policy is a POST policy as RGWPolicy holds it after from_json.
type Policy struct {
	// MinLength and MaxLength bound the upload's length: 0 and LLONG_MAX,
	// tightened by each content-length-range, whose bounds are not checked
	// against each other.
	MinLength int64
	MaxLength int64

	// expires is internal_timegm's seconds for the expiration.
	expires int64
	// checks are the object conditions, var_checks, and conds the array
	// conditions, each in the order from_json reads them.
	checks []simpleCheck
	conds  []condition
}

type simpleCheck struct{ name, value string }

// condition is eq or starts-with on two operands, each a literal or a $name.
type condition struct {
	startsWith    bool
	first, second string
}

func invalid(msg string) *Error { return &Error{Err: ErrInvalidArgument, Message: msg} }

func denied(msg string) *Error { return &Error{Err: ErrAccessDenied, Message: msg} }

// Parse reads a policy document, the form's policy field base64-decoded, as
// RGWPolicy::from_json does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L238-L305 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L247-L314 at v20.2.4).
// JSONParser reads the document with the NUL get_policy appends
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3206 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3342 at v20.2.4),
// so it takes a lone number, true, false or null only when json_spirit's
// text for it is as long as the document and the NUL, which only a real can
// be, with white space or unread text beside it, and then finds no
// expiration in it; a document JSONParser refuses is "Malformed JSON", as is
// one jsonspirit.Parse refuses where JSONParser reads it (docs/exclusions.md,
// "A POST policy with a `\x` or `\X` escape, a number json_spirit takes from
// the host's libm, or nesting past 10000 is refused"). The top-level
// object's first expiration and first conditions are read, then each child
// of conditions in the order JSONObj walks them: an array is a condition of
// exactly three elements, a non-empty object checks its member of the
// smallest name alone, and anything else is refused. A refusal is an *Error
// naming ErrInvalidArgument.
func Parse(doc []byte) (*Policy, error) {
	root, ok, err := jsonspirit.Parse(string(doc) + "\x00")
	switch {
	case err != nil:
		return nil, &Error{Err: ErrInvalidArgument, Message: msgMalformed, Detail: err.Error()}
	case !ok:
		return nil, &Error{Err: ErrInvalidArgument, Message: msgMalformed, Detail: "JSONParser refuses it"}
	}
	exp := root.Find("expiration")
	if exp == nil {
		return nil, invalid(msgNoExpiration)
	}
	expires, ok := parseISO8601(exp.Data())
	if !ok {
		return nil, invalid(msgBadExpiration)
	}
	conds := root.Find("conditions")
	if conds == nil {
		return nil, invalid(msgNoConditions)
	}
	p := &Policy{MaxLength: math.MaxInt64, expires: expires}
	for _, c := range conds.Children() {
		args := c.Node.Children()
		switch {
		case c.Node.Kind() == jsonspirit.Array:
			if len(args) != 3 {
				return nil, invalid(msgBadArray)
			}
			if err := p.addCondition(args[0].Node.Data(), args[1].Node.Data(), args[2].Node.Data()); err != nil {
				return nil, err
			}
		case c.Node.Kind() == jsonspirit.Object && len(args) > 0:
			p.checks = append(p.checks, simpleCheck{name: args[0].Name, value: args[0].Node.Data()})
		default:
			return nil, invalid(msgUnparsed)
		}
	}
	return p, nil
}

// addCondition is RGWPolicy::add_condition
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L143-L187 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L152-L196 at v20.2.4):
// the operator is matched by stringcasecmp, on it as a C string, and
// content-length-range reads its bounds with stringtoll.
func (p *Policy) addCondition(op, first, second string) error {
	switch {
	case cStringEqualFold(op, "eq"):
		p.conds = append(p.conds, condition{first: first, second: second})
	case cStringEqualFold(op, "starts-with"):
		p.conds = append(p.conds, condition{startsWith: true, first: first, second: second})
	case cStringEqualFold(op, "content-length-range"):
		lo, ok := rgwtext.StringToLL(first)
		if !ok {
			return invalid(msgBadLength)
		}
		hi, ok := rgwtext.StringToLL(second)
		if !ok {
			return invalid(msgBadLength)
		}
		p.MinLength = max(p.MinLength, lo)
		p.MaxLength = min(p.MaxLength, hi)
	default:
		return invalid(msgBadOperator + op)
	}
	return nil
}

// Check is RGWPolicy::check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L189-L235 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L198-L244 at v20.2.4)
// of env, the form's fields, at now: the policy has expired once its
// expiration is at or before now's whole second; then each object condition
// needs its field present and equal; then each array condition must hold,
// a $name operand reading the field, or "" when the form lacks it; then
// every field must have been checked (Env). Each refusal is an *Error naming
// ErrAccessDenied. Check marks in env each field it reads. An expiration
// before 1970 has passed, where radosgw's unsigned comparison never finds
// it passed (docs/exclusions.md, "A POST policy whose expiration is before
// 1970 has expired").
func (p *Policy) Check(env *Env, now time.Time) error {
	if p.expires <= now.Unix() {
		return denied(msgExpired)
	}
	for _, c := range p.checks {
		v, ok := env.Get(c.name)
		if !ok {
			return denied(msgNotFound + c.name)
		}
		env.MarkChecked(c.name)
		if v != c.value {
			return denied(msgNotMet + c.name)
		}
	}
	for _, c := range p.conds {
		first, second := env.operand(c.first), env.operand(c.second)
		switch {
		case c.startsWith && !strings.HasPrefix(first, second):
			return denied(msgPrefixFailed + ": " + c.first + ", " + c.second)
		case !c.startsWith && first != second:
			return denied(msgEqFailed + ": " + c.first + ", " + c.second)
		}
	}
	if name, ok := env.unchecked(); ok {
		return denied(msgUnchecked + name)
	}
	return nil
}

// cStringEqualFold is strcasecmp(s.c_str(), lit) == 0 in the C locale for a
// lower-case lit.
func cStringEqualFold(s, lit string) bool { return foldASCII(rgwtext.CString(s)) == lit }
