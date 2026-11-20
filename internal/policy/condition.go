package policy

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Operator is a condition operator: the cond_op subset of rgw::IAM::TokenID
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy_keywords.h#L29-L56 at v19.2.6
// and v20.2.4), in its order.
type Operator uint8

// The condition operators. String gives each one's keyword as the keyword
// table spells it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy_keywords.gperf#L38-L77 at v19.2.6
// and v20.2.4), "IpAddress" for OpIPAddress.
const (
	OpStringEquals Operator = iota
	OpStringNotEquals
	OpStringEqualsIgnoreCase
	OpStringNotEqualsIgnoreCase
	OpStringLike
	OpStringNotLike
	OpForAllValuesStringEquals
	OpForAnyValueStringEquals
	OpForAllValuesStringLike
	OpForAnyValueStringLike
	OpForAllValuesStringEqualsIgnoreCase
	OpForAnyValueStringEqualsIgnoreCase
	OpNumericEquals
	OpNumericNotEquals
	OpNumericLessThan
	OpNumericLessThanEquals
	OpNumericGreaterThan
	OpNumericGreaterThanEquals
	OpDateEquals
	OpDateNotEquals
	OpDateLessThan
	OpDateLessThanEquals
	OpDateGreaterThan
	OpDateGreaterThanEquals
	OpBool
	OpBinaryEquals
	OpIPAddress
	OpNotIPAddress
	OpArnEquals
	OpArnNotEquals
	OpArnLike
	OpArnNotLike
	OpNull
)

var operatorNames = [...]string{
	OpStringEquals:                       "StringEquals",
	OpStringNotEquals:                    "StringNotEquals",
	OpStringEqualsIgnoreCase:             "StringEqualsIgnoreCase",
	OpStringNotEqualsIgnoreCase:          "StringNotEqualsIgnoreCase",
	OpStringLike:                         "StringLike",
	OpStringNotLike:                      "StringNotLike",
	OpForAllValuesStringEquals:           "ForAllValues:StringEquals",
	OpForAnyValueStringEquals:            "ForAnyValue:StringEquals",
	OpForAllValuesStringLike:             "ForAllValues:StringLike",
	OpForAnyValueStringLike:              "ForAnyValue:StringLike",
	OpForAllValuesStringEqualsIgnoreCase: "ForAllValues:StringEqualsIgnoreCase",
	OpForAnyValueStringEqualsIgnoreCase:  "ForAnyValue:StringEqualsIgnoreCase",
	OpNumericEquals:                      "NumericEquals",
	OpNumericNotEquals:                   "NumericNotEquals",
	OpNumericLessThan:                    "NumericLessThan",
	OpNumericLessThanEquals:              "NumericLessThanEquals",
	OpNumericGreaterThan:                 "NumericGreaterThan",
	OpNumericGreaterThanEquals:           "NumericGreaterThanEquals",
	OpDateEquals:                         "DateEquals",
	OpDateNotEquals:                      "DateNotEquals",
	OpDateLessThan:                       "DateLessThan",
	OpDateLessThanEquals:                 "DateLessThanEquals",
	OpDateGreaterThan:                    "DateGreaterThan",
	OpDateGreaterThanEquals:              "DateGreaterThanEquals",
	OpBool:                               "Bool",
	OpBinaryEquals:                       "BinaryEquals",
	OpIPAddress:                          "IpAddress",
	OpNotIPAddress:                       "NotIpAddress",
	OpArnEquals:                          "ArnEquals",
	OpArnNotEquals:                       "ArnNotEquals",
	OpArnLike:                            "ArnLike",
	OpArnNotLike:                         "ArnNotLike",
	OpNull:                               "Null",
}

var operatorsByName = func() map[string]Operator {
	m := make(map[string]Operator, len(operatorNames))
	for op, name := range operatorNames {
		m[name] = Operator(op)
	}
	return m
}()

// String returns the operator's keyword, "ForAllValues:StringEquals" for
// instance, and "" for a value that names no operator.
func (o Operator) String() string {
	if int(o) >= len(operatorNames) {
		return ""
	}
	return operatorNames[o]
}

// negated is whether o is one of the Not operators, which Amazon S3 holds on a
// key the request lacks.
func (o Operator) negated() bool {
	switch o {
	case OpStringNotEquals, OpStringNotEqualsIgnoreCase, OpStringNotLike,
		OpNumericNotEquals, OpDateNotEquals, OpNotIPAddress,
		OpArnNotEquals, OpArnNotLike:
		return true
	}
	return false
}

// ParseOperator parses an operator keyword exactly as String renders it, as
// the keyword table's case-sensitive lookup does. The IfExists suffix is not
// part of the keyword; the policy parser strips it first.
func ParseOperator(s string) (Operator, bool) {
	o, ok := operatorsByName[s]
	return o, ok
}

// Condition is rgw::IAM::Condition
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.h#L348-L365 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L367-L384 at v20.2.4):
// an operator, the condition key it tests and the values it compares the
// key's values with.
type Condition struct {
	Op  Operator
	Key string
	// IfExists is the operator's IfExists suffix: the condition holds when
	// Key is absent.
	IfExists bool
	// IsRuntime is set when one of Values is an interpolation, "${...}"
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L703-L720 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L716-L732 at v20.2.4).
	// The string and ARN operators then compare Key's values with the values
	// of the key the last of Values names.
	IsRuntime bool
	Values    []string
}

// Eval is Condition::eval
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L854-L1006 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L873-L1010 at v20.2.4)
// under sem's rules.
//
// The string and ARN operators compare every value Key holds (equal_range)
// with every condition value. The typed operators, Numeric, Date, Bool,
// BinaryEquals, IpAddress and NotIpAddress, convert the one value env.find
// gives, Env.Find, and are false when it does not convert; they compare it
// with each of Values that converts and skip the rest. Either way the
// environment's value is on the left: NumericLessThan holds when it is less
// than a condition value. Null ignores IfExists: on Squid it holds when Key
// is absent, and on Tentacle (sem.NullTestsValues) it compares Key's
// absence, "true" when absent and "false" when present, with Values through
// as_bool.
//
// Eval stays as radosgw's evaluation, the one an Allow gets, which the specs
// pin on both releases; requests reach conditions through EvalFor.
func (c Condition) Eval(env Env, sem Semantics) bool {
	return c.EvalFor(env, sem, Allow)
}

// EvalFor is Eval for a condition of a statement whose effect is effect. A
// Deny statement's condition holds where Amazon S3's evaluation or radosgw's
// does, so that it refuses what either refuses; an Allow statement's keeps
// radosgw's, so that rgw-go never grants what a radosgw serving the same zone
// refuses (docs/differences.md, "A Deny's condition holds where Amazon S3's
// evaluation or radosgw's holds"). The two differ in four places:
//
//   - The case of the key's name. Amazon S3 finds the key whatever its case:
//     "Condition key names are not case-sensitive. For example, including the
//     aws:SourceIP condition key is equivalent to testing for AWS:SourceIp"
//     (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition.md?plain=1#L11).
//     radosgw keeps the key as the policy writes it
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L477-L483,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L487-L493)
//     and finds it byte for byte
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L326,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L345).
//     A request may hold several keys whose names differ only in case, as an
//     object's tags may, and a condition naming such a key "matches a
//     resource tag key named either TagKey1 or tagkey1, but not both"
//     (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition.md?plain=1#L25):
//     Amazon S3 evaluates one spelling, and AWS does not say which. So each
//     spelling counts alone. A Deny's condition holds where the evaluation of
//     the key as written holds, which is radosgw's answer for a key it does
//     not find when the request lacks that spelling, or where Amazon S3's
//     answer for some other spelling the request holds, in another ASCII
//     case, does. radosgw finds no key under another spelling, so its reading
//     of that key's values takes no part: where the IAM User Guide gives
//     Amazon S3 no answer for one (amazonAnswer), the condition holds,
//     whatever its operator, so that the Deny refuses.
//
//   - A negated operator on a key the request lacks in every case. Amazon S3
//     holds it:
//     "If the policy condition requires that the key is not matched, such as
//     StringNotLike or ArnNotLike, and the right key is not present, the
//     condition is true. This logic applies to all condition operators except
//     ...IfExists and Null check"
//     (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L10).
//     radosgw answers IfExists, as it does for any operator but Null and the
//     ForAllValues ones
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L861-L869,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L881-L889).
//
//   - Null on Squid, whose Null ignores Values
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L857-L859).
//     Amazon S3's holds for "true" when the key is absent and for "false" when
//     it is present, as Tentacle's does
//     (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L876-L879).
//
//   - A string or ARN operator whose values name policy variables, which
//     Amazon S3 allows in "string comparisons in the Condition element"
//     (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_variables.md?plain=1#L128),
//     and with the String and ARN operators
//     (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L55,
//     https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L309).
//     Amazon S3 replaces each variable with the request's value for its key,
//     and a variable the request has no value for makes its value null, which
//     matches nothing: "Inverted condition operators like StringNotEquals or
//     StringNotLike do match against a null value, as the value of the
//     condition key they are testing against is not equal to or like the
//     effectively null value"
//     (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_variables.md?plain=1#L249).
//     radosgw reads a policy variable only as a whole value, and once a value
//     starts with a "$" it takes a key from the last value, whatever it is,
//     and compares the key's values with that key's values in place of every
//     value of the condition; no value at all holds on Tentacle's
//     multimap_none and not on Squid's orrible
//     (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L514-L525,
//     https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L482-L493).
//
// A key the request holds as written is evaluated as radosgw evaluates it, in
// either effect, except where a condition value names a policy variable or,
// in a Deny, the request holds the key in another case too. An effect other
// than Deny, Pass included, gets radosgw's evaluation.
func (c Condition) EvalFor(env Env, sem Semantics, effect Effect) bool {
	if effect != Deny {
		return c.eval(env, sem, false)
	}
	keys := env.keysFolding(c.Key)
	// The key as written takes the Deny's evaluation where the request holds
	// that spelling or holds the key in no case, and radosgw's answer for a
	// key it does not find where the request holds other spellings alone.
	if c.eval(env, sem, len(keys) == 0 || slices.Contains(keys, c.Key)) {
		return true
	}
	for _, k := range keys {
		if k == c.Key {
			continue
		}
		found := c
		found.Key = k
		if holds, defined := found.amazonAnswer(env); holds || !defined {
			return true
		}
	}
	return false
}

// eval is radosgw's evaluation of c over the key it names as written, and with
// deny Amazon S3's too, as EvalFor describes but for the case of the key's
// name.
func (c Condition) eval(env Env, sem Semantics, deny bool) bool {
	s, present := env.Find(c.Key, sem)
	if c.Op == OpNull {
		absent := "false"
		if !present {
			absent = "true"
		}
		byValues := typedAny(absent, c.Values, asBool, equal[bool])
		if sem.NullTestsValues {
			return byValues
		}
		return !present || (deny && byValues)
	}
	if !present {
		switch c.Op {
		case OpForAllValuesStringEquals, OpForAllValuesStringEqualsIgnoreCase, OpForAllValuesStringLike:
			return true
		}
		return c.IfExists || (deny && c.Op.negated())
	}

	vs := env.Lookup(c.Key)
	if deny && c.holdsOverVariables(env, vs) {
		return true
	}
	ds := c.Values
	if c.IsRuntime {
		ds = env.Lookup(runtimeKey(c.Values))
	}
	switch c.Op {
	case OpStringEquals, OpForAnyValueStringEquals:
		return anyPair(vs, ds, equal[string])
	case OpStringNotEquals:
		return notPair(sem, vs, ds, equal[string])
	case OpStringEqualsIgnoreCase, OpForAnyValueStringEqualsIgnoreCase:
		return anyPair(vs, ds, asciiEqualFold)
	case OpStringNotEqualsIgnoreCase:
		return notPair(sem, vs, ds, asciiEqualFold)
	case OpStringLike, OpForAnyValueStringLike:
		return anyPair(vs, ds, stringLike)
	case OpStringNotLike:
		return notPair(sem, vs, ds, stringLike)
	case OpForAllValuesStringEquals:
		return allPairs(vs, ds, equal[string])
	case OpForAllValuesStringLike:
		return allPairs(vs, ds, stringLike)
	case OpForAllValuesStringEqualsIgnoreCase:
		return allPairs(vs, ds, asciiEqualFold)

	case OpNumericEquals:
		return typedAny(s, c.Values, AsNumber, equal[float64])
	case OpNumericNotEquals:
		return typedNot(sem, s, c.Values, AsNumber, equal[float64])
	case OpNumericLessThan:
		return typedAny(s, c.Values, AsNumber, less[float64])
	case OpNumericLessThanEquals:
		return typedAny(s, c.Values, AsNumber, lessEqual[float64])
	case OpNumericGreaterThan:
		return typedAny(s, c.Values, AsNumber, greater[float64])
	case OpNumericGreaterThanEquals:
		return typedAny(s, c.Values, AsNumber, greaterEqual[float64])

	case OpDateEquals:
		return typedAny(s, c.Values, asDate, equal[uint64])
	case OpDateNotEquals:
		return typedNot(sem, s, c.Values, asDate, equal[uint64])
	case OpDateLessThan:
		return typedAny(s, c.Values, asDate, less[uint64])
	case OpDateLessThanEquals:
		return typedAny(s, c.Values, asDate, lessEqual[uint64])
	case OpDateGreaterThan:
		return typedAny(s, c.Values, asDate, greater[uint64])
	case OpDateGreaterThanEquals:
		return typedAny(s, c.Values, asDate, greaterEqual[uint64])

	case OpBool:
		return typedAny(s, c.Values, asBool, equal[bool])
	case OpBinaryEquals:
		return typedAny(s, c.Values, AsBinary, bytes.Equal)
	case OpIPAddress:
		return typedAny(s, c.Values, ParseMaskedIP, MaskedIP.Equal)
	case OpNotIPAddress:
		// Both releases: v19.2.6 spells typed_none out
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L974-L992).
		return typedNone(s, c.Values, ParseMaskedIP, MaskedIP.Equal)

	case OpArnEquals, OpArnLike:
		return anyPair(vs, ds, arnLike)
	case OpArnNotEquals, OpArnNotLike:
		return notPair(sem, vs, ds, arnLike)
	}
	return false
}

// amazonAnswer is Amazon S3's answer for c over the one key the request holds
// under c.Key, and whether the IAM User Guide defines one. Where the guide's
// text reads more than one way, the answer is the one every reading gives,
// and it defines none where the readings differ. It defines none for an empty
// value list; for a key holding several values under an operator that is not
// a ForAnyValue or ForAllValues one; for a typed operator one of whose
// values, or the key's value, is not in a form the IAM User Guide settles
// (amazonTyped), or which names a policy variable; for a Like or ARN operator
// whose ? meets a character outside the Basic Multilingual Plane, where
// reading ? as one code point and as one UTF-16 unit give different answers;
// for an IgnoreCase operator over a letter outside ASCII, where folding
// ASCII's letters alone, folding every letter, and mapping every letter to
// upper case and then to lower case do not all give the same answer; for a
// Null with IfExists or with any value list but one "true" or "false";
// for a string or ARN operator whose values hold ${*}, ${?} or ${$}, a
// variable whose key the request holds several values for, under one name or
// several that differ in case, a variable written in a form
// substituteVariables does not read as one, or, under a Like or ARN operator,
// a variable whose value, or whose default where the request has no value,
// holds a wildcard; and for an ARN operator over a value or a pattern of fewer
// than six colon-delimited components.
//
// Its Like and ARN operators match with amazonLike, not radosgw's matcher, and
// its typed operators compare exactly, not with radosgw's arithmetic.
func (c Condition) amazonAnswer(env Env) (holds, defined bool) {
	vs := env.Lookup(c.Key)
	if c.Op == OpNull {
		if c.IfExists || len(c.Values) != 1 || (c.Values[0] != "true" && c.Values[0] != "false") {
			return false, false
		}
		return c.Values[0] == "false", true
	}
	if len(c.Values) == 0 || (len(vs) > 1 && !c.Op.forSet()) {
		return false, false
	}
	if match, form, ok := variableForm(c.Op); ok {
		values := valuesByFoldedKey(env)
		var ds []string
		for _, d := range c.Values {
			if !variablesDefined(values, d, c.Op.matchesPatterns()) {
				return false, false
			}
			if v, ok := substituteVariables(values, d); ok {
				ds = append(ds, v)
			}
		}
		var units func(v, d string) bool
		switch {
		case c.Op.matchesARNs():
			if slices.ContainsFunc(append(slices.Clone(vs), ds...), lacksARNComponents) {
				return false, false
			}
			match, units = amazonARNLike, amazonARNLikeUnits
		case c.Op.matchesPatterns():
			match, units = amazonLike, amazonLikeUnits
		}
		holds = form(vs, ds, match)
		// The two readings of ? can differ only where a ? meets a character
		// of two UTF-16 units.
		if units != nil && slices.ContainsFunc(ds, hasQuestionMark) && slices.ContainsFunc(vs, beyondBMP) &&
			form(vs, ds, units) != holds {
			return false, false
		}
		// "Exact matching, ignoring case"
		// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L34)
		// does not say which letters have a case, or how two are compared:
		// radosgw folds ASCII's, Unicode's simple folding every letter's, and
		// a comparison by case mapping differs from both.
		if c.Op.ignoresCase() && (form(vs, ds, strings.EqualFold) != holds || form(vs, ds, caseMappedEqual) != holds) {
			return false, false
		}
		return holds, true
	}
	if slices.ContainsFunc(c.Values, func(d string) bool { return strings.Contains(d, "${") }) {
		return false, false
	}
	return c.amazonTyped(vs[0])
}

func hasQuestionMark(s string) bool { return strings.Contains(s, "?") }

// beyondBMP is whether s holds a character outside the Basic Multilingual
// Plane, which UTF-16 writes as two units.
func beyondBMP(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r > 0xFFFF })
}

// forSet is whether o is a ForAnyValue or ForAllValues operator, which compare
// a key's every value.
func (o Operator) forSet() bool {
	return strings.HasPrefix(o.String(), "ForAnyValue:") || strings.HasPrefix(o.String(), "ForAllValues:")
}

// matchesPatterns is whether o reads its condition values as patterns.
func (o Operator) matchesPatterns() bool {
	switch o {
	case OpStringLike, OpStringNotLike, OpForAllValuesStringLike, OpForAnyValueStringLike,
		OpArnEquals, OpArnNotEquals, OpArnLike, OpArnNotLike:
		return true
	}
	return false
}

// matchesARNs is whether o is an ARN operator.
func (o Operator) matchesARNs() bool {
	switch o {
	case OpArnEquals, OpArnNotEquals, OpArnLike, OpArnNotLike:
		return true
	}
	return false
}

// ignoresCase is whether o is an IgnoreCase operator.
func (o Operator) ignoresCase() bool {
	switch o {
	case OpStringEqualsIgnoreCase, OpStringNotEqualsIgnoreCase,
		OpForAllValuesStringEqualsIgnoreCase, OpForAnyValueStringEqualsIgnoreCase:
		return true
	}
	return false
}

// caseMappedEqual is whether a and b are equal character by character once
// each character is mapped to upper case and, where the two still differ, to
// lower case from there. It calls U+0130 and U+0131, a capital I with a dot
// and a small i without one, equal to I and to i, which Unicode's simple
// folding does not.
func caseMappedEqual(a, b string) bool {
	for a != "" && b != "" {
		x, n := utf8.DecodeRuneInString(a)
		y, m := utf8.DecodeRuneInString(b)
		x, y = unicode.ToUpper(x), unicode.ToUpper(y)
		if x != y && unicode.ToLower(x) != unicode.ToLower(y) {
			return false
		}
		a, b = a[n:], b[m:]
	}
	return a == "" && b == ""
}

// lacksARNComponents is whether s has fewer than the six colon-delimited
// components Amazon S3's ARN operators compare one by one.
func lacksARNComponents(s string) bool { return strings.Count(s, ":") < 5 }

var (
	// amazonDateTime is the start of a W3C date and time, to the minute or
	// to the second, "YYYY-MM-DDThh:mm" or "YYYY-MM-DDThh:mm:ss"
	// (https://github.com/exelearning/iteexe/blob/3f4452780a4b2cfd2bc9d9152e92dfd86d2ff0cc/exe/webui/docs/W3C-NOTE-datetime.html#L141-L166),
	// one of "the W3C implementations of the ISO 8601 date formats"
	// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L131).
	// A fraction of a second may follow the seconds, and the time zone
	// designator ends it.
	amazonDateTime = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?`)
	// amazonZone is the time zone designator, "Z or +hh:mm or -hh:mm".
	amazonZone = regexp.MustCompile(`^(?:Z|([+-])(\d{2}):(\d{2}))$`)
	// amazonDate is a W3C year and month, or date, without a time: "YYYY-MM"
	// or "YYYY-MM-DD".
	amazonDate = regexp.MustCompile(`^(\d{4})-(\d{2})(?:-(\d{2}))?$`)
)

// amazonTyped is Amazon S3's answer for a typed operator over v, the key's
// one value, compared exactly, and whether the IAM User Guide defines one: a
// number is its decimal value (amazonNumbers), a date an instant, or any of
// several, that does not wrap and whose calendar fields exist (amazonDates),
// Bool takes "true" and "false" alone
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L168),
// a condition's binary value is padded standard base64 and the key's is read
// both ways the guide's sentence allows (amazonBinaryEquals), and an address
// is within a list of ranges "in the standard CIDR format", or bare
// addresses, when one of them holds it
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L226).
// A range of the other family holds no address of this one and decides
// nothing else: the guide's own list mixes "IPv4 and IPv6 addresses to cover
// all of your organization's valid IP addresses"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L258,
// https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L268-L272).
// radosgw's conversions take more, and its arithmetic is a double and a
// 64-bit count of nanoseconds; those stay radosgw's reading, for a key the
// request holds as written.
func (c Condition) amazonTyped(v string) (holds, defined bool) {
	switch {
	case c.Op >= OpNumericEquals && c.Op <= OpNumericGreaterThanEquals:
		return amazonNumbers(int(c.Op-OpNumericEquals), v, c.Values)
	case c.Op >= OpDateEquals && c.Op <= OpDateGreaterThanEquals:
		return amazonDates(int(c.Op-OpDateEquals), v, c.Values)
	case c.Op == OpBool:
		if v != "true" && v != "false" {
			return false, false
		}
		for _, d := range c.Values {
			if d != "true" && d != "false" {
				return false, false
			}
		}
		return slices.Contains(c.Values, v), true
	case c.Op == OpBinaryEquals:
		return amazonBinaryEquals(v, c.Values)
	case c.Op == OpIPAddress || c.Op == OpNotIPAddress:
		a, err := netip.ParseAddr(v)
		if err != nil || a.Zone() != "" || a.Is4In6() {
			return false, false
		}
		within := false
		for _, d := range c.Values {
			p, ok := amazonRange(d)
			if !ok {
				return false, false
			}
			// Contains is false for a range of the other family.
			within = within || p.Contains(a)
		}
		return within == (c.Op == OpIPAddress), true
	}
	return false, false
}

// amazonBinaryEquals is BinaryEquals over v, the key's value: "It compares the
// value of the specified key byte for byte against a base-64 encoded
// representation of the binary value in the policy"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L210).
// That reads two ways. The key's value is the binary value itself, set
// against what the policy's value decodes to; or it is base64 as well, and
// both are decoded: by radosgw's decoder, which takes the URL-safe alphabet
// too and ignores what follows the padding (AsBinary), or by another, which
// may take or refuse a value without its padding or with a line break. A
// value is longer than what it decodes to, so the two readings never both
// find the values equal: the operator's answer is false where no reading
// does, and it has none where any does.
func amazonBinaryEquals(v string, ds []string) (holds, defined bool) {
	// Both of these decoders skip a line break.
	padded, errPadded := base64.StdEncoding.DecodeString(v)
	bare, errBare := base64.RawStdEncoding.DecodeString(v)
	rados, okRados := AsBinary(v)
	for _, d := range ds {
		want, ok := amazonBinary(d)
		if !ok {
			return false, false
		}
		if v == string(want) || (errPadded == nil && bytes.Equal(padded, want)) ||
			(errBare == nil && bytes.Equal(bare, want)) || (okRados && bytes.Equal(rados, want)) {
			return false, false
		}
	}
	return false, true
}

// amazonBinary is s, a condition's binary value, as the bytes it encodes:
// padded standard base64. An empty value names no binary value, and a line
// break is no part of base64, though the decoder would skip one.
func amazonBinary(s string) ([]byte, bool) {
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return nil, false
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	return b, err == nil
}

// amazonNumbers is a Numeric operator over x, the key's value, and ds: rel is
// the operator's offset from NumericEquals, in the order Equals, NotEquals,
// LessThan, LessThanEquals, GreaterThan, GreaterThanEquals, which the Date
// operators keep. NotEquals holds when no value is equal, and the others when
// some value satisfies them.
func amazonNumbers(rel int, x string, ds []string) (holds, defined bool) {
	a, ok := amazonNumberValue(x)
	if !ok {
		return false, false
	}
	some := false
	for _, d := range ds {
		b, ok := amazonNumberValue(d)
		if !ok {
			return false, false
		}
		k := a.compare(b)
		switch rel {
		case 0, 1:
			some = some || k == 0
		case 2:
			some = some || k < 0
		case 3:
			some = some || k <= 0
		case 4:
			some = some || k > 0
		case 5:
			some = some || k >= 0
		}
	}
	if rel == 1 {
		return !some, true
	}
	return some, true
}

// amazonDecimal is a decimal number: its sign, its integer digits without
// leading zeros and its fraction digits without trailing zeros, so that equal
// numbers have equal fields. It is compared digit by digit, in time linear in
// its length, because a request's value is as long as a header may be and
// arithmetic on that many digits is quadratic.
type amazonDecimal struct {
	negative    bool
	whole, frac string
}

// newAmazonDecimal is the number whose integer digits are whole and whose
// fraction digits are frac. Zero has no sign.
func newAmazonDecimal(negative bool, whole, frac string) amazonDecimal {
	whole, frac = strings.TrimLeft(whole, "0"), strings.TrimRight(frac, "0")
	return amazonDecimal{negative: negative && (whole != "" || frac != ""), whole: whole, frac: frac}
}

func (a amazonDecimal) compare(b amazonDecimal) int {
	if a.negative != b.negative {
		if a.negative {
			return -1
		}
		return 1
	}
	k := cmp.Or(cmp.Compare(len(a.whole), len(b.whole)), strings.Compare(a.whole, b.whole), strings.Compare(a.frac, b.frac))
	if a.negative {
		return -k
	}
	return k
}

// amazonNumberValue is s, "an integer or decimal value"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L97):
// digits, a minus sign before them or not, and a point and more digits after
// them or not.
func amazonNumberValue(s string) (amazonDecimal, bool) {
	digits, negative := strings.CutPrefix(s, "-")
	whole, frac, point := strings.Cut(digits, ".")
	if !allDigits(whole) || (point && !allDigits(frac)) {
		return amazonDecimal{}, false
	}
	return newAmazonDecimal(negative, whole, frac), true
}

// allDigits is whether s is one or more ASCII digits.
func allDigits(s string) bool {
	return s != "" && strings.TrimLeft(s, "0123456789") == ""
}

// amazonInstant is an instant as exact seconds since the epoch: the whole
// seconds at or before it, and the digits of the fraction of a second past
// them, without trailing zeros.
type amazonInstant struct {
	seconds amazonDecimal
	frac    string
}

func (a amazonInstant) compare(b amazonInstant) int {
	return cmp.Or(a.seconds.compare(b.seconds), strings.Compare(a.frac, b.frac))
}

// amazonSpan is the instants a date value may name: each one from lo to hi,
// but for hi itself when open. A value that names one instant has lo and hi
// equal.
type amazonSpan struct {
	lo, hi amazonInstant
	open   bool
}

// before is whether every instant of a is before every instant of b.
func (a amazonSpan) before(b amazonSpan) bool {
	k := a.hi.compare(b.lo)
	return k < 0 || (k == 0 && a.open)
}

// notAfter is whether no instant of a is after an instant of b.
func (a amazonSpan) notAfter(b amazonSpan) bool { return a.hi.compare(b.lo) <= 0 }

// relates is whether the comparison rel, as amazonNumbers takes it, NotEquals
// as Equals, holds between every instant of a and every instant of b, and
// whether it holds between none.
func (a amazonSpan) relates(rel int, b amazonSpan) (always, never bool) {
	switch rel {
	case 2:
		return a.before(b), b.notAfter(a)
	case 3:
		return a.notAfter(b), b.before(a)
	case 4:
		return b.before(a), a.notAfter(b)
	case 5:
		return b.notAfter(a), a.before(b)
	}
	return a.notAfter(b) && b.notAfter(a), a.before(b) || b.before(a)
}

// amazonDates is a Date operator over x, the key's value, and ds: rel is as
// amazonNumbers takes it. A date may name any of several instants
// (amazonInstants), so the operator has the answer every reading of x and of
// ds gives, and none where the readings differ.
func amazonDates(rel int, x string, ds []string) (holds, defined bool) {
	as := amazonInstants(x)
	if as == nil {
		return false, false
	}
	// some is whether one value satisfies the comparison under every
	// reading, and none whether every value fails it under every reading.
	some, none := false, true
	for _, d := range ds {
		bs := amazonInstants(d)
		if bs == nil {
			return false, false
		}
		always, never := true, true
		for _, a := range as {
			for _, b := range bs {
				t, f := a.relates(rel, b)
				always, never = always && t, never && f
			}
		}
		some, none = some || always, none && never
	}
	switch {
	case some:
		return rel != 1, true
	case none:
		return rel == 1, true
	}
	return false, false
}

// amazonZoneReach is how far a time zone runs from UTC, in seconds: none is
// more than 14 hours ahead of it or behind it.
const amazonZoneReach = 14 * 3600

// amazonInstants is the instants s, epoch seconds or a W3C date, may name, and
// nil for a value that names none. A date and time names one
// (amazonInstantValue). A W3C date without a time is a form the IAM User
// Guide's policy checks recommend, "We recommend that you use the YYYY-MM-DD
// format"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/access-analyzer-reference-policy-checks.md?plain=1#L1952),
// and neither the guide nor the note says which instant it is: the note leaves
// "the meaning of the dates and times with reduced precision" to the standard
// that adopts it
// (https://github.com/exelearning/iteexe/blob/3f4452780a4b2cfd2bc9d9152e92dfd86d2ff0cc/exe/webui/docs/W3C-NOTE-datetime.html#L124-L131).
// So a year, a year and month, or a date may name any instant of it in any
// time zone (amazonCalendarSpan). Four digits alone are a W3C year and a count
// of seconds, which the guide does not tell apart, and may name either.
func amazonInstants(s string) []amazonSpan {
	if len(s) == 4 && allDigits(s) {
		count := amazonInstant{seconds: newAmazonDecimal(false, s, "")}
		year, _ := amazonCalendarSpan(s, "", "")
		return []amazonSpan{{lo: count, hi: count}, year}
	}
	if at, ok := amazonInstantValue(s); ok {
		return []amazonSpan{{lo: at, hi: at}}
	}
	if m := amazonDate.FindStringSubmatch(s); m != nil {
		if span, ok := amazonCalendarSpan(m[1], m[2], m[3]); ok {
			return []amazonSpan{span}
		}
	}
	return nil
}

// amazonCalendarSpan is every instant of a year, of its month when month is
// given, or of that month's day when day is too, in any time zone: from its
// start in the zone farthest ahead of UTC up to its end in the zone farthest
// behind. ok is false for a month or a day that does not exist.
func amazonCalendarSpan(year, month, day string) (span amazonSpan, ok bool) {
	m, d := 1, 1
	years, months, days := 1, 0, 0
	if month != "" {
		m = decimalValue(month)
		years, months = 0, 1
	}
	if day != "" {
		d = decimalValue(day)
		months, days = 0, 1
	}
	start := time.Date(decimalValue(year), time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if m < 1 || m > 12 || start.Day() != d {
		return amazonSpan{}, false
	}
	end := start.AddDate(years, months, days)
	return amazonSpan{
		lo:   amazonSecond(start.Unix() - amazonZoneReach),
		hi:   amazonSecond(end.Unix() + amazonZoneReach),
		open: true,
	}, true
}

// amazonSecond is the instant sec whole seconds from the epoch.
func amazonSecond(sec int64) amazonInstant {
	digits, negative := strings.CutPrefix(strconv.FormatInt(sec, 10), "-")
	return amazonInstant{seconds: newAmazonDecimal(negative, digits, "")}
}

// amazonInstantValue is s, epoch seconds or a W3C date and time, as the one
// instant it names. A time zone designator other than Z is the local time's
// offset from UTC, ahead of it for + and behind it for -, so that
// "1994-11-05T08:15:30-05:00" and "1994-11-05T13:15:30Z" are the same instant
// (https://github.com/exelearning/iteexe/blob/3f4452780a4b2cfd2bc9d9152e92dfd86d2ff0cc/exe/webui/docs/W3C-NOTE-datetime.html#L175-L198),
// a form the IAM User Guide's policy checks name: "YYYY-MM-DDThh:mm:ssTZD
// (1997-07-16T19:20:30+01:00)"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/access-analyzer-reference-policy-checks.md?plain=1#L1963).
//
// Four digits alone, and a W3C date without a time, name no one instant
// (amazonInstants). A date, time or offset whose fields do not exist, such as
// month 13, 30 February, second 60 or an offset of 24 hours, names nothing.
func amazonInstantValue(s string) (amazonInstant, bool) {
	if allDigits(s) {
		if len(s) == 4 {
			return amazonInstant{}, false
		}
		return amazonInstant{seconds: newAmazonDecimal(false, s, "")}, true
	}
	m := amazonDateTime.FindStringSubmatch(s)
	if m == nil {
		return amazonInstant{}, false
	}
	rest := s[len(m[0]):]
	var frac string
	if m[6] != "" && strings.HasPrefix(rest, ".") {
		zone := strings.TrimLeft(rest[1:], "0123456789")
		frac, rest = rest[1:len(rest)-len(zone)], zone
		if frac == "" {
			return amazonInstant{}, false
		}
	}
	z := amazonZone.FindStringSubmatch(rest)
	if z == nil {
		return amazonInstant{}, false
	}
	year, month, day := decimalValue(m[1]), decimalValue(m[2]), decimalValue(m[3])
	hour, minute, second := decimalValue(m[4]), decimalValue(m[5]), decimalValue(m[6])
	offsetHours, offsetMinutes := decimalValue(z[2]), decimalValue(z[3])
	if month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59 || offsetHours > 23 || offsetMinutes > 59 {
		return amazonInstant{}, false
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	if t.Day() != day || int(t.Month()) != month {
		return amazonInstant{}, false
	}
	offset := int64(offsetHours*3600 + offsetMinutes*60)
	if z[1] == "-" {
		offset = -offset
	}
	at := amazonSecond(t.Unix() - offset)
	at.frac = strings.TrimRight(frac, "0")
	return at, true
}

// decimalValue is the number a short run of ASCII digits spells, 0 for none.
func decimalValue(digits string) int {
	n := 0
	for i := range len(digits) {
		n = n*10 + int(digits[i]-'0')
	}
	return n
}

// amazonRange is d, a CIDR range or a bare address, as a range. A range
// written with host bits set is the network it names: "When you include
// non-zero bits after the masked bits, they are not considered for the
// condition"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/access-analyzer-reference-policy-checks.md?plain=1#L2951).
// An address written in an IPv6 form that holds an IPv4 one, or with a zone,
// has none.
func amazonRange(d string) (netip.Prefix, bool) {
	if strings.Contains(d, "/") {
		p, err := netip.ParsePrefix(d)
		if err != nil || p.Addr().Is4In6() {
			return netip.Prefix{}, false
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(d)
	if err != nil || a.Zone() != "" || a.Is4In6() {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}

// amazonLike is Amazon S3's StringLike: "The values can include
// multi-character match wildcards (*) and single-character match wildcards
// (?) anywhere in the string"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L36).
// * matches any run of characters, none included, ? one character, and every
// other byte of the pattern d itself, [, ] and \ included. radosgw's
// matcher, stringLike, is fnmatch's, which reads bracket expressions and
// escapes too. A character is a code point here; amazonLikeUnits is the other
// reading.
func amazonLike(v, d string) bool {
	star, from := -1, 0
	i, j := 0, 0
	for i < len(v) {
		if j < len(d) {
			switch d[j] {
			case '*':
				star, from = j, i
				j++
				continue
			case '?':
				_, n := utf8.DecodeRuneInString(v[i:])
				i += n
				j++
				continue
			}
			if v[i] == d[j] {
				i++
				j++
				continue
			}
		}
		if star < 0 {
			return false
		}
		_, n := utf8.DecodeRuneInString(v[from:])
		from += n
		i, j = from, star+1
	}
	for j < len(d) && d[j] == '*' {
		j++
	}
	return j == len(d)
}

// amazonARNLike is Amazon S3's ArnLike and ArnEquals: "Each of the six
// colon-delimited components of the ARN is checked separately and each can
// include multi-character match wildcards (*) or single-character match
// wildcards (?)"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L290),
// each through amazonLike. The sixth component holds the rest of the ARN,
// colons included.
func amazonARNLike(v, d string) bool { return arnComponentsLike(v, d, amazonLike) }

// amazonARNLikeUnits is amazonARNLike with each component through
// amazonLikeUnits.
func amazonARNLikeUnits(v, d string) bool { return arnComponentsLike(v, d, amazonLikeUnits) }

func arnComponentsLike(v, d string, like func(v, d string) bool) bool {
	vs, ds := strings.SplitN(v, ":", 6), strings.SplitN(d, ":", 6)
	if len(vs) != 6 || len(ds) != 6 {
		return false
	}
	for k := range vs {
		if !like(vs[k], ds[k]) {
			return false
		}
	}
	return true
}

// amazonLikeUnits is amazonLike with ? matching one UTF-16 code unit, so that
// a character outside the Basic Multilingual Plane takes two. The IAM User
// Guide's "single-character" does not say which a character is, and a
// condition the two readings answer differently has no Amazon S3 answer.
func amazonLikeUnits(v, d string) bool {
	vu, du := utf16Units(v), utf16Units(d)
	star, from := -1, 0
	i, j := 0, 0
	for i < len(vu) {
		if j < len(du) {
			if du[j] == '*' {
				star, from = j, i
				j++
				continue
			}
			if du[j] == '?' || du[j] == vu[i] {
				i++
				j++
				continue
			}
		}
		if star < 0 {
			return false
		}
		from++
		i, j = from, star+1
	}
	for j < len(du) && du[j] == '*' {
		j++
	}
	return j == len(du)
}

// utf16Units is s as UTF-16 code units, a character outside the Basic
// Multilingual Plane as its two surrogates. A byte that is not UTF-8 is a
// unit of its own, above every code point, so that it equals only itself.
func utf16Units(s string) []rune {
	units := make([]rune, 0, len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			units = append(units, unicode.MaxRune+1+rune(s[i]))
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			units = append(units, hi, lo)
		default:
			units = append(units, r)
		}
		i += n
	}
	return units
}

// variablesDefined is whether each ${...} of d is a variable whose value
// Amazon S3's text defines: ${key} or ${key, 'default'}, whose key the request
// holds at most one value for under every spelling, and, when patterns, whose
// value, or default where the request holds none, holds no wildcard: either is
// written into the pattern. It walks d as substituteVariables does.
func variablesDefined(values map[string][]string, d string, patterns bool) bool {
	for {
		i := strings.Index(d, "${")
		if i < 0 {
			return true
		}
		n := strings.IndexByte(d[i+2:], '}')
		if n < 0 {
			return true
		}
		inner := d[i+2 : i+2+n]
		name, def, hasDefault := strings.Cut(inner, ", '")
		switch {
		case inner == "*" || inner == "?" || inner == "$":
			return false
		case hasDefault && (!strings.HasSuffix(def, "'") || strings.Contains(strings.TrimSuffix(def, "'"), "'")):
			return false
		case strings.ContainsAny(name, "',{} "):
			return false
		}
		vals := values[asciiLowerString(name)]
		if len(vals) > 1 || (patterns && len(vals) == 1 && strings.ContainsAny(vals[0], "*?")) {
			return false
		}
		if patterns && len(vals) == 0 && hasDefault && strings.ContainsAny(def, "*?") {
			return false
		}
		d = d[i+2+n+1:]
	}
}

// variableForm is how a string or ARN operator, which Amazon S3 lets name
// policy variables, relates the values the key holds to the condition's: the
// comparison it makes, and whether the operator holds when some key value
// matches (the positive and ForAnyValue forms), every key value matches
// (ForAllValues) or none does (the negated forms).
func variableForm(op Operator) (match func(v, d string) bool, holds func(vs, ds []string, f func(v, d string) bool) bool, ok bool) {
	switch op {
	case OpStringEquals, OpForAnyValueStringEquals:
		return equal[string], anyPair, true
	case OpStringEqualsIgnoreCase, OpForAnyValueStringEqualsIgnoreCase:
		return asciiEqualFold, anyPair, true
	case OpStringLike, OpForAnyValueStringLike:
		return stringLike, anyPair, true
	case OpArnEquals, OpArnLike:
		return arnLike, anyPair, true
	case OpForAllValuesStringEquals:
		return equal[string], allPairs, true
	case OpForAllValuesStringEqualsIgnoreCase:
		return asciiEqualFold, allPairs, true
	case OpForAllValuesStringLike:
		return stringLike, allPairs, true
	case OpStringNotEquals:
		return equal[string], nonePair, true
	case OpStringNotEqualsIgnoreCase:
		return asciiEqualFold, nonePair, true
	case OpStringNotLike:
		return stringLike, nonePair, true
	case OpArnNotEquals, OpArnNotLike:
		return arnLike, nonePair, true
	}
	return nil, nil, false
}

// holdsOverVariables is Amazon S3's answer for a string or ARN operator whose
// values name policy variables, over the values vs the key holds: the
// operator relates them to the condition's values, with their variables
// replaced, as variableForm says. A value whose variable the request has no
// value for is null and matches nothing, so a negated operator naming no
// other value holds and a positive one does not. A value that holds ${*}, ${?}
// or ${$}, which stand for a character the matching cannot express, makes the
// operator hold: a Deny errs toward refusing there. A condition naming no
// variable is not answered here; radosgw's answer stands for it.
func (c Condition) holdsOverVariables(env Env, vs []string) bool {
	match, holds, ok := variableForm(c.Op)
	if !ok {
		return false
	}
	named := false
	for _, d := range c.Values {
		if strings.Contains(d, "${*}") || strings.Contains(d, "${?}") || strings.Contains(d, "${$}") {
			return true
		}
		named = named || strings.Contains(d, "${")
	}
	if !named {
		return false
	}
	values := valuesByFoldedKey(env)
	var ds []string
	for _, d := range c.Values {
		if !strings.Contains(d, "${") {
			ds = append(ds, d)
		} else if v, ok := substituteVariables(values, d); ok {
			ds = append(ds, v)
		}
	}
	return holds(vs, ds, match)
}

// nonePair holds when no environment value matches a condition value.
func nonePair(vs, ds []string, f func(v, d string) bool) bool { return !anyPair(vs, ds, f) }

// valuesByFoldedKey is the values env holds for each key name, the names
// folded to ASCII lower case and the values of names that differ only in case
// together: "Key names are case-insensitive"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_variables.md?plain=1#L55).
// A Deny's own condition key is found under the same fold, Env.keysFolding,
// but EvalFor never takes the values of several spellings together.
func valuesByFoldedKey(env Env) map[string][]string {
	m := make(map[string][]string, len(env.keys))
	for key, v := range env.keys {
		folded := asciiLowerString(key)
		m[folded] = append(m[folded], v.added...)
	}
	return m
}

// asciiLowerString is s with its ASCII letters in lower case.
func asciiLowerString(s string) string {
	b := []byte(s)
	for i, c := range b {
		b[i] = asciiLower(c)
	}
	return string(b)
}

// substituteVariables replaces each ${key} of d with the value values holds
// for key, or each ${key, 'default'} with that value, or with the default
// unless the request has exactly one value for key: "To add a default value
// to a variable, surround the default value with single quotes (' '), and
// separate the variable text and the default value with a comma and space
// (, )"
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_variables.md?plain=1#L305).
// ok is false when a variable has no value, or several, and no default, which
// Amazon S3 takes for a null value: it takes only a single-valued key as a
// variable. A ${ that nothing closes is text.
func substituteVariables(values map[string][]string, d string) (value string, ok bool) {
	var b strings.Builder
	for {
		i := strings.Index(d, "${")
		if i < 0 {
			b.WriteString(d)
			return b.String(), true
		}
		n := strings.IndexByte(d[i+2:], '}')
		if n < 0 {
			b.WriteString(d)
			return b.String(), true
		}
		name, def, hasDefault := strings.Cut(d[i+2:i+2+n], ", '")
		hasDefault = hasDefault && strings.HasSuffix(def, "'")
		val := strings.TrimSuffix(def, "'")
		if vals := values[asciiLowerString(name)]; len(vals) == 1 {
			val = vals[0]
		} else if !hasDefault {
			return "", false
		}
		b.WriteString(d[:i])
		b.WriteString(val)
		d = d[i+2+n+1:]
	}
}

// runtimeKey is the key a runtime condition's string and ARN operators read:
// the last value without its first two bytes and its last, whatever they are,
// as radosgw's Condition::eval derives it. A value shorter than three bytes
// gives the empty key, which no request sets.
func runtimeKey(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	k := vals[len(vals)-1]
	if len(k) < 3 {
		return ""
	}
	return k[2 : len(k)-1]
}

// anyPair is multimap_any (orrible at v19.2.6): f holds for some environment
// value v and condition value d.
func anyPair(vs, ds []string, f func(v, d string) bool) bool {
	for _, v := range vs {
		for _, d := range ds {
			if f(v, d) {
				return true
			}
		}
	}
	return false
}

// allPairs is multimap_all (andible at v19.2.6): every environment value
// satisfies f with some condition value.
func allPairs(vs, ds []string, f func(v, d string) bool) bool {
	for _, v := range vs {
		if !slices.ContainsFunc(ds, func(d string) bool { return f(v, d) }) {
			return false
		}
	}
	return true
}

// notPair is a negated string or ARN operator: multimap_none(f) when
// sem.NotMeansNone, otherwise orrible(std::not_fn(f)).
func notPair(sem Semantics, vs, ds []string, f func(v, d string) bool) bool {
	if sem.NotMeansNone {
		return !anyPair(vs, ds, f)
	}
	return anyPair(vs, ds, func(v, d string) bool { return !f(v, d) })
}

// typedAny is typed_any (shortible at v19.2.6): x converts the environment's
// value s, and f holds for it and some condition value that converts.
func typedAny[T any](s string, ds []string, x func(string) (T, bool), f func(a, b T) bool) bool {
	xs, ok := x(s)
	if !ok {
		return false
	}
	for _, d := range ds {
		if xd, ok := x(d); ok && f(xs, xd) {
			return true
		}
	}
	return false
}

// typedNone is typed_none: x converts s, and f holds for it and no condition
// value that converts.
func typedNone[T any](s string, ds []string, x func(string) (T, bool), f func(a, b T) bool) bool {
	xs, ok := x(s)
	if !ok {
		return false
	}
	for _, d := range ds {
		if xd, ok := x(d); ok && f(xs, xd) {
			return false
		}
	}
	return true
}

// typedNot is NumericNotEquals and DateNotEquals: typed_none(eq) when
// sem.NotMeansNone, otherwise shortible(std::not_fn(eq)).
func typedNot[T any](sem Semantics, s string, ds []string, x func(string) (T, bool), eq func(a, b T) bool) bool {
	if sem.NotMeansNone {
		return typedNone(s, ds, x, eq)
	}
	return typedAny(s, ds, x, func(a, b T) bool { return !eq(a, b) })
}

type ordered interface{ ~float64 | ~uint64 }

func equal[T comparable](a, b T) bool     { return a == b }
func less[T ordered](a, b T) bool         { return a < b }
func lessEqual[T ordered](a, b T) bool    { return a <= b }
func greater[T ordered](a, b T) bool      { return a > b }
func greaterEqual[T ordered](a, b T) bool { return a >= b }

// stringLike is string_like: the condition value is the pattern.
func stringLike(v, d string) bool { return MatchWildcards(d, v, false) }

// arnLike is arn_like
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_iam_policy.cc#L845-L852 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L864-L871 at v20.2.4):
// the environment's ARN must hold exactly five colons, and the condition
// value is the pattern, matched case-sensitively.
func arnLike(v, d string) bool {
	return strings.Count(v, ":") == 5 && MatchPolicy(d, v, false)
}

// asciiEqualFold is boost::iequals in the C locale radosgw runs in: equal
// lengths, and bytes equal once ASCII letters are folded.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if asciiLower(a[i]) != asciiLower(b[i]) {
			return false
		}
	}
	return true
}

func asciiLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
