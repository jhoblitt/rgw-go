package policy

import (
	"bytes"
	"slices"
	"strings"
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
// evaluation or radosgw's holds"). The two differ in three places:
//
//   - A negated operator on a key the request lacks. Amazon S3 holds it:
//     "If the policy condition requires that the key is not matched, such as
//     StringNotLike or ArnNotLike, and the right key is not present, the
//     condition is true. This logic applies to all condition operators except
//     ...IfExists and Null check"
//     (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L10).
//     radosgw answers IfExists, as it does for any operator but Null and the
//     ForAllValues ones
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L861-L869,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L881-L889).
//   - Null on Squid, whose Null ignores Values
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L857-L859).
//     Amazon S3's holds for "true" when the key is absent and for "false" when
//     it is present, as Tentacle's does
//     (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L876-L879).
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
// A key the request holds is evaluated as radosgw evaluates it, in either
// effect, except where a condition value names a policy variable. An effect
// other than Deny, Pass included, gets radosgw's evaluation.
func (c Condition) EvalFor(env Env, sem Semantics, effect Effect) bool {
	deny := effect == Deny
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
// A condition's own key stays exact, as radosgw's lookup does.
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
