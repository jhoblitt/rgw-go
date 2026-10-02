package policy

import (
	"bytes"
	"slices"
	"strings"
)

// Operator is a condition operator: the cond_op subset of rgw::IAM::TokenID
// (src/rgw/rgw_iam_policy_keywords.h:29-56 at v19.2.6 and v20.2.4), in its
// order.
type Operator uint8

// The condition operators. String gives each one's keyword as the keyword
// table spells it (src/rgw/rgw_iam_policy_keywords.gperf:38-77 at v19.2.6 and
// v20.2.4), "IpAddress" for OpIPAddress.
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

// ParseOperator parses an operator keyword exactly as String renders it, as
// the keyword table's case-sensitive lookup does. The IfExists suffix is not
// part of the keyword; the policy parser strips it first.
func ParseOperator(s string) (Operator, bool) {
	o, ok := operatorsByName[s]
	return o, ok
}

// Condition is rgw::IAM::Condition (src/rgw/rgw_iam_policy.h:348-365 at
// v19.2.6, :367-384 at v20.2.4): an operator, the condition key it tests and
// the values it compares the key's values with.
type Condition struct {
	Op  Operator
	Key string
	// IfExists is the operator's IfExists suffix: the condition holds when
	// Key is absent.
	IfExists bool
	// IsRuntime is set when one of Values is an interpolation, "${...}"
	// (src/rgw/rgw_iam_policy.cc:703-720 at v19.2.6, :716-732 at v20.2.4).
	// The string and ARN operators then compare Key's values with the values
	// of the key the last of Values names.
	IsRuntime bool
	Values    []string
}

// Eval is Condition::eval (src/rgw/rgw_iam_policy.cc:854-1006 at v19.2.6,
// :873-1010 at v20.2.4) under sem's rules.
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
func (c Condition) Eval(env Env, sem Semantics) bool {
	s, present := env.Find(c.Key, sem)
	if c.Op == OpNull {
		if !sem.NullTestsValues {
			return !present
		}
		absent := "false"
		if !present {
			absent = "true"
		}
		return typedAny(absent, c.Values, asBool, equal[bool])
	}
	if !present {
		switch c.Op {
		case OpForAllValuesStringEquals, OpForAllValuesStringEqualsIgnoreCase, OpForAllValuesStringLike:
			return true
		}
		return c.IfExists
	}

	vs := env.Lookup(c.Key)
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
		// Both releases: v19.2.6 spells typed_none out (:974-992).
		return typedNone(s, c.Values, ParseMaskedIP, MaskedIP.Equal)

	case OpArnEquals, OpArnLike:
		return anyPair(vs, ds, arnLike)
	case OpArnNotEquals, OpArnNotLike:
		return notPair(sem, vs, ds, arnLike)
	}
	return false
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

// arnLike is arn_like (:845-852 at v19.2.6, :864-871 at v20.2.4): the
// environment's ARN must hold exactly five colons, and the condition value is
// the pattern, matched case-sensitively.
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
