package policy_test

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// Amazon S3 answers a condition on a key the request context lacks false, and
// a negated operator's true: "If the policy condition requires that the key is
// not matched, such as StringNotLike or ArnNotLike, and the right key is not
// present, the condition is true. This logic applies to all condition
// operators except ...IfExists and Null check."
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L10).
// radosgw's Condition::eval answers its IfExists flag instead, for every
// operator but Null and the ForAllValues ones
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L861-L869,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L881-L889),
// and Squid's Null ignores its values
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L857-L859).
// A Deny statement's condition holds where either answer does, and an Allow
// statement's keeps radosgw's, so that rgw-go never grants what a radosgw
// serving the same zone refuses. The specs name each effect's answer on each
// release, so that the one difference, a Deny's, is the only one they show.

// negatedOperator is a negated condition operator, with a key it can test and
// the values to test it against.
type negatedOperator struct {
	op  policy.Operator
	key string
	// values hold matching, first, and a value that matching does not match.
	values []string
	// matching is a key value that one of values matches, and differing one
	// that none does.
	matching, differing string
	// squidHoldsOnMatching is whether the operator holds on Squid for the
	// key's matching value: Squid's holds when any pair of the key's value
	// and a condition value differs, so the other of values makes it hold;
	// NotIpAddress alone holds only when no pair matches on both releases.
	squidHoldsOnMatching bool
	// stringOrARN: the operator compares the key's values with the values of
	// a policy variable's key, and holds Amazon S3's way when the variable
	// has no value.
	stringOrARN bool
}

// cond is the operator's condition on one value, or all of its values.
func (n negatedOperator) cond(vals ...string) policy.Condition {
	return policy.Condition{Op: n.op, Key: n.key, Values: vals}
}

var negatedOperators = []negatedOperator{
	{
		op: policy.OpStringNotEquals, key: "s3:x-amz-server-side-encryption",
		values: []string{"aws:kms", "AES256"}, matching: "aws:kms", differing: "aws:kms:dsse", squidHoldsOnMatching: true, stringOrARN: true,
	},
	{
		op: policy.OpStringNotEqualsIgnoreCase, key: "aws:username",
		values: []string{"alice", "bob"}, matching: "ALICE", differing: "carol", squidHoldsOnMatching: true, stringOrARN: true,
	},
	{
		op: policy.OpStringNotLike, key: "s3:prefix",
		values: []string{"home/*", "tmp/*"}, matching: "home/a", differing: "other/a", squidHoldsOnMatching: true, stringOrARN: true,
	},
	{
		op: policy.OpNumericNotEquals, key: "s3:max-keys",
		values: []string{"5", "6"}, matching: "5", differing: "7", squidHoldsOnMatching: true,
	},
	{
		op: policy.OpDateNotEquals, key: "aws:CurrentTime",
		values:   []string{"2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z"},
		matching: "2024-01-01T00:00:00Z", differing: "2026-01-01T00:00:00Z", squidHoldsOnMatching: true,
	},
	{
		op: policy.OpNotIPAddress, key: "aws:SourceIp",
		values: []string{"192.168.1.0/24", "10.0.0.0/8"}, matching: "192.168.1.5", differing: "172.16.0.1",
	},
	{
		op: policy.OpArnNotEquals, key: "aws:SourceArn",
		values:   []string{"arn:aws:s3:::b/*", "arn:aws:s3:::c/*"},
		matching: "arn:aws:s3:::b/k", differing: "arn:aws:s3:::d/k", squidHoldsOnMatching: true, stringOrARN: true,
	},
	{
		op: policy.OpArnNotLike, key: "aws:SourceArn",
		values:   []string{"arn:aws:s3:::b/*", "arn:aws:s3:::c/*"},
		matching: "arn:aws:s3:::b/k", differing: "arn:aws:s3:::d/k", squidHoldsOnMatching: true, stringOrARN: true,
	},
}

// verdict is whether a condition holds in a statement of each effect, on each
// release.
type verdict struct {
	allowSquid, allowTentacle, denySquid, denyTentacle bool
}

// same is a condition that holds, or does not, however it is evaluated.
func same(holds bool) verdict { return verdict{holds, holds, holds, holds} }

// radosgwWhere is the verdict of a condition radosgw evaluates as squid and
// tentacle say, in any statement.
func radosgwWhere(squid, tentacle bool) verdict { return verdict{squid, tentacle, squid, tentacle} }

var _ = Describe("a condition under its statement's effect", func() {
	var (
		squid, tentacle policy.Semantics
		bucket          policy.ARN
	)

	BeforeEach(func() {
		squid = policy.SemanticsFor(denc.Squid)
		tentacle = policy.SemanticsFor(denc.Tentacle)
		bucket = s3ARN("b")
	})

	// holds is whether c holds in a statement of effect over bucket's
	// s3:ListBucket, as Statement.Eval answers: the effect when every condition
	// holds, and Pass when one does not.
	holds := func(effect policy.Effect, c policy.Condition, env policy.Env, sem policy.Semantics) bool {
		GinkgoHelper()
		s := policy.Statement{
			Effect:     effect,
			Actions:    actionSet(policy.S3ListBucket),
			Resources:  []policy.ARN{s3ARN("b")},
			Conditions: []policy.Condition{c},
		}
		got := s.Eval(env, nil, policy.S3ListBucket, &bucket, sem)
		Expect(got).To(BeElementOf(effect, policy.Pass), "%+v", c)
		return got == effect
	}

	expectVerdict := func(c policy.Condition, kv []string, want verdict) {
		GinkgoHelper()
		env := envOf(kv...)
		Expect(holds(policy.Allow, c, env, squid)).To(Equal(want.allowSquid), "an Allow on squid: %+v, %v", c, kv)
		Expect(holds(policy.Allow, c, env, tentacle)).To(Equal(want.allowTentacle), "an Allow on tentacle: %+v, %v", c, kv)
		Expect(holds(policy.Deny, c, env, squid)).To(Equal(want.denySquid), "a Deny on squid: %+v, %v", c, kv)
		Expect(holds(policy.Deny, c, env, tentacle)).To(Equal(want.denyTentacle), "a Deny on tentacle: %+v, %v", c, kv)
	}

	It("names every negated operator rgw-go parses", func() {
		var parsed, named []string
		for op := policy.OpStringEquals; op <= policy.OpNull; op++ {
			if strings.Contains(op.String(), "Not") {
				parsed = append(parsed, op.String())
			}
		}
		for _, n := range negatedOperators {
			named = append(named, n.op.String())
		}
		Expect(named).To(ConsistOf(parsed))
	})

	for _, n := range negatedOperators {
		Describe(n.op.String(), func() {
			It("holds in a Deny and not in an Allow on a key the request lacks, on either release", func() {
				expectVerdict(n.cond(n.values...), nil, verdict{denySquid: true, denyTentacle: true})
				expectVerdict(n.cond(n.values[0]), nil, verdict{denySquid: true, denyTentacle: true})
				expectVerdict(n.cond(n.values...), []string{"other", "x"}, verdict{denySquid: true, denyTentacle: true})
			})

			It("holds in either effect, as IfExists says, on a key the request lacks", func() {
				c := n.cond(n.values...)
				c.IfExists = true
				expectVerdict(c, nil, same(true))
			})

			It("holds for a key value that differs, in either effect", func() {
				expectVerdict(n.cond(n.values...), []string{n.key, n.differing}, same(true))
			})

			It("holds for no key value that a value matches, in either effect", func() {
				expectVerdict(n.cond(n.values[0]), []string{n.key, n.matching}, same(false))
			})

			It("evaluates a key value that one of two values matches as the release does, in either effect", func() {
				expectVerdict(n.cond(n.values...), []string{n.key, n.matching}, radosgwWhere(n.squidHoldsOnMatching, false))
			})

			It("evaluates IfExists on a key the request holds as the operator does, in either effect", func() {
				c := n.cond(n.values...)
				c.IfExists = true
				expectVerdict(c, []string{n.key, n.differing}, same(true))
				expectVerdict(c, []string{n.key, n.matching}, radosgwWhere(n.squidHoldsOnMatching, false))
			})

			if n.stringOrARN {
				It("holds in a Deny, and on Tentacle in an Allow, for a policy variable the request has no value for", func() {
					c := runtime(n.cond("${missing}"))
					expectVerdict(c, []string{n.key, n.matching},
						verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true})
				})

				It("evaluates a policy variable the request has a value for as the release does, in either effect", func() {
					c := runtime(n.cond("${other}"))
					expectVerdict(c, []string{n.key, n.matching, "other", n.matching}, same(false))
					expectVerdict(c, []string{n.key, n.matching, "other", n.differing}, same(true))
				})

				It("holds for a key the request lacks and a policy variable it has no value for, as it does for the key alone", func() {
					expectVerdict(runtime(n.cond("${missing}")), nil, verdict{denySquid: true, denyTentacle: true})
				})
			}
		})
	}

	// Every other operator keeps radosgw's answer on an absent key: false, but
	// for the ForAllValues operators, which hold of no value, and IfExists.
	It("holds on a key the request lacks only for a ForAllValues operator or IfExists, whatever the effect, but for a negated operator in a Deny", func() {
		for op := range policy.OpNull {
			c := policy.Condition{Op: op, Key: "k", Values: []string{"x"}}
			forAll := strings.HasPrefix(op.String(), "ForAllValues:")
			negated := strings.Contains(op.String(), "Not")
			expectVerdict(c, nil, verdict{
				allowSquid: forAll, allowTentacle: forAll,
				denySquid: forAll || negated, denyTentacle: forAll || negated,
			})
			c.IfExists = true
			expectVerdict(c, nil, same(true))
		}
	})

	It("keeps radosgw's evaluation of a key the request holds, for every operator, in either effect", func() {
		for _, c := range []policy.Condition{
			cond(policy.OpStringEquals, "k", "x"),
			cond(policy.OpNumericLessThan, "k", "5"),
			cond(policy.OpBool, "k", "true"),
			cond(policy.OpIPAddress, "k", "10.0.0.0/8"),
			cond(policy.OpForAllValuesStringEquals, "k", "x"),
			cond(policy.OpForAnyValueStringLike, "k", "x*"),
		} {
			for _, v := range []string{"x", "4", "true", "10.1.1.1", "y"} {
				env := envOf("k", v)
				for _, sem := range []policy.Semantics{squid, tentacle} {
					want := c.Eval(env, sem)
					Expect(holds(policy.Allow, c, env, sem)).To(Equal(want), "an Allow: %+v, %q", c, v)
					Expect(holds(policy.Deny, c, env, sem)).To(Equal(want), "a Deny: %+v, %q", c, v)
				}
			}
		}
	})

	// The typed operators read the condition's own values and take no policy
	// variable: "${missing}" converts to nothing and is skipped.
	It("evaluates a typed negated operator whose value names a policy variable as radosgw does, in either effect", func() {
		expectVerdict(runtime(cond(policy.OpNumericNotEquals, "k", "${missing}")), []string{"k", "5"}, radosgwWhere(false, true))
		expectVerdict(runtime(cond(policy.OpDateNotEquals, "k", "${missing}")), []string{"k", "2024-01-01T00:00:00Z"}, radosgwWhere(false, true))
		expectVerdict(runtime(cond(policy.OpNotIPAddress, "k", "${missing}")), []string{"k", "192.168.1.5"}, same(true))
		expectVerdict(runtime(cond(policy.OpNumericEquals, "k", "${n}")), []string{"k", "5", "n", "5"}, same(false))
	})

	It("holds no other operator for a policy variable the request has no value for", func() {
		for _, op := range []policy.Operator{policy.OpStringEquals, policy.OpStringLike, policy.OpArnLike, policy.OpForAnyValueStringEquals} {
			expectVerdict(runtime(cond(op, "k", "${missing}")), []string{"k", "a"}, same(false))
		}
	})

	// parsedCondition is the condition the policy parser makes of a Deny
	// statement's op block over key and values, so that IsRuntime is as
	// radosgw's parser sets it.
	parsedCondition := func(op, key string, values ...string) policy.Condition {
		GinkgoHelper()
		vals, err := json.Marshal(values)
		Expect(err).NotTo(HaveOccurred())
		text := `{"Version": "2012-10-17", "Statement": {"Effect": "Deny", "Principal": "*", "Action": "s3:ListBucket",
			"Resource": "arn:aws:s3:::b", "Condition": {"` + op + `": {"` + key + `": ` + string(vals) + `}}}}`
		return mustParse(text, parseOpts(denc.Squid)).Statements[0].Conditions[0]
	}

	// Amazon S3 replaces a policy variable with the request's value for its
	// key, and a variable it has no value for makes the value null, which
	// matches nothing. radosgw, once a value starts with a "$", takes a key from
	// the last value, whatever it is, and compares the key's values with that
	// key's values in place of every value of the condition.
	DescribeTable("evaluates a string or ARN operator whose values name policy variables as either evaluation does in a Deny, and radosgw's in an Allow",
		func(op, key string, values, kv []string, want verdict) {
			expectVerdict(parsedCondition(op, key, values...), kv, want)
		},
		// A mixed list: radosgw's variable is the last value's alone.
		Entry("a variable then the key's own value, the variable unset", "StringNotEquals", "aws:username",
			[]string{"${aws:userid}", "alice"}, []string{"aws:username", "alice"}, radosgwWhere(false, true)),
		Entry("the key's own value then a variable, the variable unset", "StringNotEquals", "aws:username",
			[]string{"alice", "${missing}"}, []string{"aws:username", "alice"}, radosgwWhere(false, true)),
		Entry("a variable then another value, the variable unset and the key another", "StringNotEquals", "aws:username",
			[]string{"${aws:userid}", "alice"}, []string{"aws:username", "bob"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("another value then a variable, the variable unset and the key another", "StringNotEquals", "aws:username",
			[]string{"alice", "${missing}"}, []string{"aws:username", "bob"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("a variable then the key's own value, the variable set to another", "StringNotEquals", "aws:username",
			[]string{"${aws:userid}", "alice"}, []string{"aws:username", "alice", "aws:userid", "carol"}, radosgwWhere(false, true)),
		Entry("a variable then another value, the variable set to a third and the key another", "StringNotEquals", "aws:username",
			[]string{"${aws:userid}", "alice"}, []string{"aws:username", "bob", "aws:userid", "carol"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		// A variable alone.
		Entry("a variable alone, unset, and the key an empty value", "StringNotEquals", "aws:username",
			[]string{"${aws:userid}"}, []string{"aws:username", ""},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("a variable alone, set to the key's value", "StringNotEquals", "aws:username",
			[]string{"${aws:userid}"}, []string{"aws:username", "alice", "aws:userid", "alice"}, same(false)),
		Entry("a variable alone, unset", "StringNotEquals", "aws:username",
			[]string{"${aws:userid}"}, []string{"aws:username", "alice"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("a variable alone, holding two values, which Amazon S3 takes for no value", "StringNotEquals", "aws:username",
			[]string{"${aws:userid}"}, []string{"aws:username", "carol", "aws:userid", "carol", "aws:userid", "dave"},
			verdict{allowSquid: true, allowTentacle: false, denySquid: true, denyTentacle: true}),
		Entry("a variable alone under StringNotEqualsIgnoreCase, set to the key's value in another case", "StringNotEqualsIgnoreCase", "aws:username",
			[]string{"${aws:userid}"}, []string{"aws:username", "ALICE", "aws:userid", "alice"}, same(false)),
		Entry("a variable alone under StringNotEqualsIgnoreCase, unset", "StringNotEqualsIgnoreCase", "aws:username",
			[]string{"${aws:userid}"}, []string{"aws:username", "ALICE"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		// A variable inside a value, which radosgw's parser does not read as one.
		Entry("a variable inside a value, set, and the key that value written out", "StringNotEquals", "s3:prefix",
			[]string{"home/${aws:username}/x"}, []string{"s3:prefix", "home/${aws:username}/x", "aws:username", "alice"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable inside a value, unset, and the key that value written out", "StringNotEquals", "s3:prefix",
			[]string{"home/${aws:username}/x"}, []string{"s3:prefix", "home/${aws:username}/x"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable inside a value, set, and the key its value", "StringNotEquals", "s3:prefix",
			[]string{"home/${aws:username}/x"}, []string{"s3:prefix", "home/alice/x", "aws:username", "alice"}, same(true)),
		Entry("a variable inside a StringNotLike pattern, set, and the key that pattern written out", "StringNotLike", "s3:prefix",
			[]string{"home/${aws:username}/*"}, []string{"s3:prefix", "home/${aws:username}/k", "aws:username", "alice"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable inside an ArnNotLike pattern, set, and the key that pattern written out", "ArnNotLike", "aws:SourceArn",
			[]string{"arn:aws:s3:::${username}/*"}, []string{"aws:SourceArn", "arn:aws:s3:::${username}/k", "username", "alice"},
			verdict{denySquid: true, denyTentacle: true}),
		// A positive operator holds where some key value matches a value with
		// its variables replaced, a ForAllValues one where every key value does.
		Entry("StringEquals, a variable then the key's value, the variable unset", "StringEquals", "k",
			[]string{"${v}", "x"}, []string{"k", "x"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEquals, a variable then a value the key lacks, the variable unset", "StringEquals", "k",
			[]string{"${v}", "x"}, []string{"k", "y"}, same(false)),
		Entry("StringEquals, a variable then a value, the variable set to the key's value", "StringEquals", "k",
			[]string{"${v}", "x"}, []string{"k", "y", "v", "y"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEquals, a variable inside a value, set, and the key its value", "StringEquals", "k",
			[]string{"home/${v}/x"}, []string{"k", "home/alice/x", "v", "alice"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEquals, a variable inside a value, unset, and the key its value", "StringEquals", "k",
			[]string{"home/${v}/x"}, []string{"k", "home/alice/x"}, same(false)),
		Entry("StringEqualsIgnoreCase, a variable inside a value, set, and the key its value in another case", "StringEqualsIgnoreCase", "k",
			[]string{"HOME/${v}/X"}, []string{"k", "home/alice/x", "v", "alice"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("ArnLike, a variable inside a pattern, set", "ArnLike", "aws:SourceArn",
			[]string{"arn:aws:s3:::${v}/*"}, []string{"aws:SourceArn", "arn:aws:s3:::alice/k", "v", "alice"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("ArnEquals, a variable inside a pattern, set", "ArnEquals", "aws:SourceArn",
			[]string{"arn:aws:s3:::${v}/*"}, []string{"aws:SourceArn", "arn:aws:s3:::alice/k", "v", "alice"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("ForAnyValue:StringEquals, a variable inside a value, set", "ForAnyValue:StringEquals", "k",
			[]string{"home/${v}/x"}, []string{"k", "home/alice/x", "v", "alice"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("ForAllValues:StringLike, a variable inside a pattern, set, and every key value matching", "ForAllValues:StringLike", "k",
			[]string{"home/${v}/*"}, []string{"k", "home/alice/a", "k", "home/alice/b", "v", "alice"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("ForAllValues:StringLike, a variable inside a pattern, set, and one key value not matching", "ForAllValues:StringLike", "k",
			[]string{"home/${v}/*"}, []string{"k", "home/alice/a", "k", "other/b", "v", "alice"}, same(false)),
		Entry("ForAllValues:StringLike, a variable inside a pattern, unset", "ForAllValues:StringLike", "k",
			[]string{"home/${v}/*"}, []string{"k", "home/alice/a", "k", "home/alice/b"}, same(false)),
		Entry("ForAllValues:StringLike, a variable inside a pattern, the key absent", "ForAllValues:StringLike", "k",
			[]string{"home/${v}/*"}, nil, same(true)),
		// A variable's key name is found in any case.
		Entry("a variable written in upper case, the request's key in lower, and the key its value", "StringNotEquals", "k",
			[]string{"${V}"}, []string{"k", "x", "v", "x"}, radosgwWhere(false, true)),
		Entry("a variable written in upper case, the request's key in lower, and the key another value", "StringNotEquals", "k",
			[]string{"${V}"}, []string{"k", "x", "v", "y"}, verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("a default, the request's key in upper case and the default the key's value", "StringNotEquals", "k",
			[]string{"${v, 'x'}"}, []string{"k", "x", "V", "y"}, verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("a default, the request holding no key in any case and the default the key's value", "StringNotEquals", "k",
			[]string{"${V, 'x'}"}, []string{"k", "x"}, radosgwWhere(false, true)),
		Entry("a variable the request holds under two keys that differ in case, which is no value", "StringNotEquals", "k",
			[]string{"${v}"}, []string{"k", "x", "v", "x", "V", "x"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEquals, a variable inside a value written in upper case, the request's key in lower", "StringEquals", "k",
			[]string{"home/${V}/x"}, []string{"k", "home/alice/x", "v", "alice"}, verdict{denySquid: true, denyTentacle: true}),
		// A character variable holds a Deny's positive operator too.
		Entry("StringEquals, ${*}", "StringEquals", "k",
			[]string{"a${*}b"}, []string{"k", "zzz"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringLike, ${$}", "StringLike", "k",
			[]string{"a${$}b"}, []string{"k", "zzz"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("ForAllValues:StringEquals, ${?}", "ForAllValues:StringEquals", "k",
			[]string{"a${?}b"}, []string{"k", "zzz"}, verdict{denySquid: true, denyTentacle: true}),
		// A default, ${key, 'default'}, which the request's single value for the
		// key replaces, and which stands otherwise.
		Entry("a default, the variable set to another value than the key's", "StringNotEquals", "aws:username",
			[]string{"${aws:userid, 'anon'}"}, []string{"aws:username", "alice", "aws:userid", "carol"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("a default, the variable set to the key's value", "StringNotEquals", "aws:username",
			[]string{"${aws:userid, 'anon'}"}, []string{"aws:username", "alice", "aws:userid", "alice"}, radosgwWhere(false, true)),
		Entry("a default, the variable unset and the default another value than the key's", "StringNotEquals", "aws:username",
			[]string{"${aws:userid, 'anon'}"}, []string{"aws:username", "alice"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("a default, the variable unset and the default the key's value", "StringNotEquals", "aws:username",
			[]string{"${aws:userid, 'anon'}"}, []string{"aws:username", "anon"}, radosgwWhere(false, true)),
		Entry("a default, the variable holding two values and the default the key's value", "StringNotEquals", "aws:username",
			[]string{"${aws:userid, 'anon'}"}, []string{"aws:username", "anon", "aws:userid", "carol", "aws:userid", "dave"},
			radosgwWhere(false, true)),
		Entry("a default, the variable holding two values and the default another value than the key's", "StringNotEquals", "aws:username",
			[]string{"${aws:userid, 'anon'}"}, []string{"aws:username", "alice", "aws:userid", "carol", "aws:userid", "dave"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("a default inside a value, the variable unset, and the key that value written out", "StringNotEquals", "s3:prefix",
			[]string{"home/${aws:userid, 'anon'}/x"}, []string{"s3:prefix", "home/${aws:userid, 'anon'}/x"},
			verdict{denySquid: true, denyTentacle: true}),
		// ${*}, ${?} and ${$} stand for a character the matching cannot
		// express, and a Deny holds for any value that holds one.
		Entry("${*}", "StringNotEquals", "s3:prefix",
			[]string{"a${*}b"}, []string{"s3:prefix", "a${*}b"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("${?}", "StringNotEquals", "s3:prefix",
			[]string{"a${?}b"}, []string{"s3:prefix", "a${?}b"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("${$}", "StringNotEquals", "s3:prefix",
			[]string{"a${$}b"}, []string{"s3:prefix", "a${$}b"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("${*} in a StringNotLike pattern", "StringNotLike", "s3:prefix",
			[]string{"home/${*}"}, []string{"s3:prefix", "home/${*}"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("${*} in an ArnNotEquals pattern", "ArnNotEquals", "aws:SourceArn",
			[]string{"arn:aws:s3:::b/${*}"}, []string{"aws:SourceArn", "arn:aws:s3:::b/${*}"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("${*} after a variable the request has no value for", "StringNotEquals", "s3:prefix",
			[]string{"${missing}${*}"}, []string{"s3:prefix", "${missing}${*}"},
			verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
		Entry("${*} beside a value that matches", "StringNotEquals", "aws:username",
			[]string{"alice", "a${*}b"}, []string{"aws:username", "alice"},
			verdict{allowSquid: true, allowTentacle: false, denySquid: true, denyTentacle: true}),
	)

	// A statement holds when each of its conditions holds, and each takes its
	// own union of the two evaluations, so that one can hold only by Amazon S3's
	// and another only by radosgw's.
	Describe("a statement of two conditions", func() {
		statementOf := func(effect policy.Effect, cs ...policy.Condition) policy.Statement {
			return policy.Statement{
				Effect:     effect,
				Actions:    actionSet(policy.S3ListBucket),
				Resources:  []policy.ARN{s3ARN("b")},
				Conditions: cs,
			}
		}
		evaluate := func(s policy.Statement, kv []string, sem policy.Semantics) policy.Effect {
			GinkgoHelper()
			return s.Eval(envOf(kv...), nil, policy.S3ListBucket, &bucket, sem)
		}

		It("does not hold in a Deny when the condition that holds by the Deny rule has a partner that holds under neither evaluation", func() {
			s := statementOf(policy.Deny, cond(policy.OpStringNotEquals, "k1", "x"), cond(policy.OpStringEquals, "k2", "x"))
			Expect(evaluate(s, []string{"k2", "y"}, squid)).To(Equal(policy.Pass), "squid")
			Expect(evaluate(s, []string{"k2", "y"}, tentacle)).To(Equal(policy.Pass), "tentacle")
			Expect(evaluate(s, []string{"k2", "x"}, squid)).To(Equal(policy.Deny), "squid, the partner holding")
			Expect(evaluate(s, []string{"k2", "x"}, tentacle)).To(Equal(policy.Deny), "tentacle, the partner holding")
		})

		It("holds in no Allow, whatever the partner", func() {
			s := statementOf(policy.Allow, cond(policy.OpStringNotEquals, "k1", "x"), cond(policy.OpStringEquals, "k2", "x"))
			Expect(evaluate(s, []string{"k2", "x"}, squid)).To(Equal(policy.Pass), "squid")
			Expect(evaluate(s, []string{"k2", "x"}, tentacle)).To(Equal(policy.Pass), "tentacle")
		})

		It("holds in a Deny when one condition holds only by Amazon S3's evaluation and the other only by radosgw's", func() {
			// radosgw reads home/${aws:username}/x as text and Amazon S3 as home/alice/x.
			byRadosgw := parsedCondition("StringNotEquals", "k3", "home/${aws:username}/x")
			byAmazon := cond(policy.OpStringNotEquals, "k1", "x")
			kv := []string{"k3", "home/alice/x", "aws:username", "alice"}
			s := statementOf(policy.Deny, byAmazon, byRadosgw)
			Expect(evaluate(s, kv, squid)).To(Equal(policy.Deny), "squid")
			Expect(evaluate(s, kv, tentacle)).To(Equal(policy.Deny), "tentacle")
			allow := statementOf(policy.Allow, byAmazon, byRadosgw)
			Expect(evaluate(allow, kv, squid)).To(Equal(policy.Pass), "an Allow on squid")
			Expect(evaluate(allow, kv, tentacle)).To(Equal(policy.Pass), "an Allow on tentacle")
		})
	})

	// What neither Amazon S3 text nor Squid's answer decides stays as radosgw
	// answers it, in either effect.
	DescribeTable("keeps radosgw's answer where there is no Amazon S3 answer to take",
		func(c policy.Condition, kv []string, want verdict) {
			expectVerdict(c, kv, want)
		},
		Entry("an empty value list, on a key the request holds", cond(policy.OpStringNotEquals, "k"), []string{"k", "a"}, radosgwWhere(false, true)),
		Entry("a Numeric environment value that does not convert", cond(policy.OpNumericNotEquals, "k", "5"), []string{"k", "abc"}, same(false)),
		Entry("a Date environment value that does not convert", cond(policy.OpDateNotEquals, "k", "2024-01-01T00:00:00Z"), []string{"k", "abc"}, same(false)),
		Entry("Numeric values none of which converts", cond(policy.OpNumericNotEquals, "k", "x"), []string{"k", "5"}, radosgwWhere(false, true)),
		Entry("Date values none of which converts", cond(policy.OpDateNotEquals, "k", "x"), []string{"k", "2024-01-01T00:00:00Z"}, radosgwWhere(false, true)),
	)

	Describe("Null", func() {
		null := func(vals ...string) policy.Condition { return cond(policy.OpNull, "k", vals...) }

		// Amazon S3's Null holds for "true" when the key is absent and for
		// "false" when it is present, as v20.2.4's does
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L876-L879),
		// so Squid's, which holds exactly when the key is absent whatever
		// the values say, differs from it in a statement that refuses.
		DescribeTable("holds in a Deny where Amazon S3's or Squid's does, and in an Allow as radosgw's does",
			func(c policy.Condition, kv []string, want verdict) {
				expectVerdict(c, kv, want)
			},
			Entry("true, on an absent key", null("true"), nil, same(true)),
			Entry("true, on a present key", null("true"), []string{"k", "v"}, same(false)),
			Entry("false, on an absent key", null("false"), nil, radosgwWhere(true, false)),
			Entry("false, on a present key", null("false"), []string{"k", "v"},
				verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
			Entry("true and false, on an absent key", null("true", "false"), nil, same(true)),
			Entry("true and false, on a present key", null("true", "false"), []string{"k", "v"},
				verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
			Entry("a number as_bool takes for true, on an absent key", null("1"), nil, same(true)),
			Entry("a number as_bool takes for true, on a present key", null("1"), []string{"k", "v"}, same(false)),
			Entry("no value, on an absent key", null(), nil, radosgwWhere(true, false)),
			Entry("no value, on a present key", null(), []string{"k", "v"}, same(false)),
			Entry("false, on a key holding two values", null("false"), []string{"k", "a", "k", "b"},
				verdict{allowSquid: false, allowTentacle: true, denySquid: true, denyTentacle: true}),
			Entry("IfExists is no part of it: false, on an absent key", func() policy.Condition {
				c := null("false")
				c.IfExists = true
				return c
			}(), nil, radosgwWhere(true, false)),
		)
	})

	Describe("EvalFor", func() {
		It("is Eval, radosgw's evaluation, for an Allow and for Pass, and widens a Deny alone", func() {
			c := cond(policy.OpStringNotEquals, "k", "x")
			for _, sem := range []policy.Semantics{squid, tentacle} {
				Expect(c.Eval(policy.Env{}, sem)).To(BeFalse())
				Expect(c.EvalFor(policy.Env{}, sem, policy.Allow)).To(BeFalse())
				Expect(c.EvalFor(policy.Env{}, sem, policy.Pass)).To(BeFalse())
				Expect(c.EvalFor(policy.Env{}, sem, policy.Deny)).To(BeTrue())
			}
		})

		It("takes an Effect no statement names as radosgw's evaluation, a Deny's alone being wider", func() {
			c := cond(policy.OpNull, "k", "false")
			Expect(c.EvalFor(envOf("k", "v"), squid, policy.Effect(200))).To(BeFalse())
			Expect(c.EvalFor(envOf("k", "v"), squid, policy.Deny)).To(BeTrue())
		})
	})

	Describe("a statement that names no Effect", func() {
		It("takes a Deny's evaluation, as it is a Deny", func() {
			s := policy.Statement{
				Actions:    actionSet(policy.S3ListBucket),
				Resources:  []policy.ARN{s3ARN("b")},
				Conditions: []policy.Condition{cond(policy.OpStringNotEquals, "k", "x")},
			}
			Expect(s.Eval(policy.Env{}, nil, policy.S3ListBucket, &bucket, squid)).To(Equal(policy.Deny))
			Expect(s.EvalConditions(policy.Env{}, squid)).To(Equal(policy.Allow))
		})

		It("gives its conditions the evaluation of its Effect", func() {
			s := policy.Statement{Conditions: []policy.Condition{cond(policy.OpStringNotEquals, "k", "x")}}
			for effect, want := range map[policy.Effect]policy.Effect{policy.Deny: policy.Allow, policy.Allow: policy.Deny} {
				s.Effect = effect
				Expect(s.EvalConditions(policy.Env{}, squid)).To(Equal(want), "%v", effect)
				Expect(s.EvalConditions(policy.Env{}, tentacle)).To(Equal(want), "%v", effect)
			}
		})
	})

	Describe("a policy", func() {
		const deny = `{"Version": "2012-10-17", "Statement": [
			{"Effect": "Allow", "Principal": "*", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::b/*"},
			{"Effect": "Deny", "Principal": "*", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::b/*",
			 "Condition": {"StringNotEquals": {"s3:x-amz-server-side-encryption": "aws:kms"}}}]}`
		const allow = `{"Version": "2012-10-17", "Statement": [
			{"Effect": "Allow", "Principal": "*", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::b/*",
			 "Condition": {"StringNotEquals": {"s3:x-amz-server-side-encryption": "aws:kms"}}}]}`

		for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
			Describe("on "+rel.String(), func() {
				var obj policy.ARN
				var sem policy.Semantics

				BeforeEach(func() {
					obj = s3ARN("b/k")
					sem = policy.SemanticsFor(rel)
				})

				It("refuses a write that names no encryption under a Deny on any but aws:kms, as Amazon S3 does", func() {
					p := mustParse(deny, parseOpts(rel))
					Expect(p.Eval(policy.Env{}, nil, policy.S3PutObject, &obj, sem)).To(Equal(policy.Deny), "no header")
					Expect(p.Eval(envOf("s3:x-amz-server-side-encryption", "AES256"), nil, policy.S3PutObject, &obj, sem)).
						To(Equal(policy.Deny), "AES256")
					Expect(p.Eval(envOf("s3:x-amz-server-side-encryption", "aws:kms"), nil, policy.S3PutObject, &obj, sem)).
						To(Equal(policy.Allow), "aws:kms")
				})

				It("grants nothing to a write that names no encryption under an Allow on any but aws:kms, as radosgw does", func() {
					p := mustParse(allow, parseOpts(rel))
					Expect(p.Eval(policy.Env{}, nil, policy.S3PutObject, &obj, sem)).To(Equal(policy.Pass), "no header")
					Expect(p.Eval(envOf("s3:x-amz-server-side-encryption", "AES256"), nil, policy.S3PutObject, &obj, sem)).
						To(Equal(policy.Allow), "AES256")
					Expect(p.Eval(envOf("s3:x-amz-server-side-encryption", "aws:kms"), nil, policy.S3PutObject, &obj, sem)).
						To(Equal(policy.Pass), "aws:kms")
				})
			})
		}
	})
})
