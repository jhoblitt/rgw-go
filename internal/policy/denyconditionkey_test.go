package policy_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// "Condition key names are not case-sensitive. For example, including the
// aws:SourceIP condition key is equivalent to testing for AWS:SourceIp."
// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition.md?plain=1#L11).
// radosgw keeps a condition's key as the policy writes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L477-L483,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L487-L493)
// and finds it in an Environment whose keys compare byte for byte
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L326,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L345).
// A Deny statement's condition holds where radosgw's finding holds, where
// Amazon S3's answer for the key found in another case does, or where the IAM
// User Guide gives Amazon S3 no answer for it; an Allow statement's keeps
// radosgw's.
var _ = Describe("a condition key spelled in another case than the request's", func() {
	var (
		squid, tentacle policy.Semantics
		bucket          policy.ARN
	)

	BeforeEach(func() {
		squid = policy.SemanticsFor(denc.Squid)
		tentacle = policy.SemanticsFor(denc.Tentacle)
		bucket = s3ARN("b")
	})

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

	// expectVerdicts checks squidCond on Squid and tentacleCond on Tentacle.
	expectVerdicts := func(squidCond, tentacleCond policy.Condition, kv []string, want verdict) {
		GinkgoHelper()
		env := envOf(kv...)
		Expect(holds(policy.Allow, squidCond, env, squid)).To(Equal(want.allowSquid), "an Allow on squid")
		Expect(holds(policy.Allow, tentacleCond, env, tentacle)).To(Equal(want.allowTentacle), "an Allow on tentacle")
		Expect(holds(policy.Deny, squidCond, env, squid)).To(Equal(want.denySquid), "a Deny on squid")
		Expect(holds(policy.Deny, tentacleCond, env, tentacle)).To(Equal(want.denyTentacle), "a Deny on tentacle")
	}

	expectVerdict := func(c policy.Condition, kv []string, want verdict) {
		GinkgoHelper()
		expectVerdicts(c, c, kv, want)
	}

	DescribeTable("holds in a Deny where the key found in any case or radosgw's exact key makes it hold, and in an Allow as radosgw's does",
		expectVerdict,
		Entry("an aws: key in upper case, its value matching",
			cond(policy.OpIPAddress, "AWS:SOURCEIP", "192.168.1.0/24"), []string{"aws:SourceIp", "192.168.1.5"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("an aws: key in lower case, its value matching",
			cond(policy.OpIPAddress, "aws:sourceip", "192.168.1.0/24"), []string{"aws:SourceIp", "192.168.1.5"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("an aws: key in another case, its value not matching",
			cond(policy.OpIPAddress, "AWS:SourceIP", "192.168.1.0/24"), []string{"aws:SourceIp", "10.0.0.1"}, same(false)),
		Entry("an aws: key spelled as the request spells it",
			cond(policy.OpIPAddress, "aws:SourceIp", "192.168.1.0/24"), []string{"aws:SourceIp", "192.168.1.5"}, same(true)),
		Entry("an aws: key spelled as the request spells it, its value not matching",
			cond(policy.OpIPAddress, "aws:SourceIp", "192.168.1.0/24"), []string{"aws:SourceIp", "10.0.0.1"}, same(false)),
		Entry("an aws: key in another case under a string operator",
			cond(policy.OpStringLike, "AWS:Referer", "http://www.example.com/*"), []string{"aws:Referer", "http://www.example.com/a"},
			verdict{denySquid: true, denyTentacle: true}),

		Entry("an s3: key in another case, its value matching",
			cond(policy.OpStringLike, "S3:Prefix", "home/*"), []string{"s3:prefix", "home/a"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("an s3: key in another case, its value not matching",
			cond(policy.OpStringLike, "S3:Prefix", "home/*"), []string{"s3:prefix", "other/a"}, same(false)),
		Entry("an s3: key spelled as the request spells it",
			cond(policy.OpStringLike, "s3:prefix", "home/*"), []string{"s3:prefix", "home/a"}, same(true)),
		Entry("an s3: key spelled as the request spells it, its value not matching",
			cond(policy.OpStringLike, "s3:prefix", "home/*"), []string{"s3:prefix", "other/a"}, same(false)),
		Entry("an s3: key in another case under a typed operator",
			cond(policy.OpNumericLessThan, "S3:MAX-KEYS", "10"), []string{"s3:max-keys", "5"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key in another case under a Date operator",
			cond(policy.OpDateLessThan, "AWS:CURRENTTIME", "2025-01-01T00:00:00Z"), []string{"aws:CurrentTime", "2024-06-01T00:00:00Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key in another case under a Date operator, its value not matching",
			cond(policy.OpDateLessThan, "AWS:CURRENTTIME", "2025-01-01T00:00:00Z"), []string{"aws:CurrentTime", "2026-06-01T00:00:00Z"}, same(false)),
		Entry("a key in another case under Bool",
			cond(policy.OpBool, "AWS:SECURETRANSPORT", "true"), []string{"aws:SecureTransport", "true"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key in another case under an ARN operator",
			cond(policy.OpArnLike, "AWS:SOURCEARN", "arn:aws:s3:::b/*"), []string{"aws:SourceArn", "arn:aws:s3:::b/k"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key in another case under an ARN operator, its value not matching",
			cond(policy.OpArnLike, "AWS:SOURCEARN", "arn:aws:s3:::b/*"), []string{"aws:SourceArn", "arn:aws:s3:::c/k"}, same(false)),
		Entry("a key in another case under a ForAnyValue operator",
			cond(policy.OpForAnyValueStringEquals, "S3:Prefix", "home/a"), []string{"s3:prefix", "home/a"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key in another case under a ForAnyValue operator, its value not matching",
			cond(policy.OpForAnyValueStringEquals, "S3:Prefix", "home/a"), []string{"s3:prefix", "home/b"}, same(false)),

		// Only the key's name folds: each operator keeps its own rule for the
		// values.
		Entry("StringEquals, the key in another case and the value too",
			cond(policy.OpStringEquals, "S3:PREFIX", "HOME/A"), []string{"s3:prefix", "home/a"}, same(false)),
		Entry("StringEqualsIgnoreCase, the key in another case and the value too",
			cond(policy.OpStringEqualsIgnoreCase, "S3:PREFIX", "HOME/A"), []string{"s3:prefix", "home/a"},
			verdict{denySquid: true, denyTentacle: true}),

		// A negated operator is evaluated over the values of the key found, not
		// as on a key the request lacks.
		Entry("StringNotLike, the key in another case, its value matching",
			cond(policy.OpStringNotLike, "S3:Prefix", "home/*"), []string{"s3:prefix", "home/a"}, same(false)),
		Entry("StringNotLike, the key in another case, its value not matching",
			cond(policy.OpStringNotLike, "S3:Prefix", "home/*"), []string{"s3:prefix", "other/a"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("NotIpAddress, the key in another case, its value matching",
			cond(policy.OpNotIPAddress, "AWS:SourceIP", "192.168.1.0/24"), []string{"aws:SourceIp", "192.168.1.5"}, same(false)),
		Entry("NotIpAddress, the key in another case, its value not matching",
			cond(policy.OpNotIPAddress, "AWS:SourceIP", "192.168.1.0/24"), []string{"aws:SourceIp", "10.0.0.1"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("StringNotLike, the request holding the key in no case",
			cond(policy.OpStringNotLike, "S3:Prefix", "home/*"), []string{"s3:delimiter", "/"},
			verdict{denySquid: true, denyTentacle: true}),

		// Amazon S3's answer for the key found reads a policy variable the
		// request has no value for as null, which nothing equals.
		Entry("StringNotEquals over a policy variable the request lacks, the key in another case",
			runtime(cond(policy.OpStringNotEquals, "S3:Prefix", "${aws:username}")), []string{"s3:prefix", "home"},
			verdict{denySquid: true, denyTentacle: true}),

		// radosgw's answer for its exact key, which the request lacks, stays a
		// Deny's.
		Entry("IfExists, the key in another case, its value not matching",
			ifExists(cond(policy.OpStringLike, "S3:Prefix", "home/*")), []string{"s3:prefix", "other/a"}, same(true)),
		Entry("ForAllValues:StringEquals, the key in another case, its value not matching",
			cond(policy.OpForAllValuesStringEquals, "S3:Prefix", "home/a"), []string{"s3:prefix", "other/a"}, same(true)),
		Entry("Null true, the key in another case",
			cond(policy.OpNull, "AWS:Referer", "true"), []string{"aws:Referer", "x"}, same(true)),
		Entry("Null false, the key in another case",
			cond(policy.OpNull, "AWS:Referer", "false"), []string{"aws:Referer", "x"},
			verdict{allowSquid: true, allowTentacle: false, denySquid: true, denyTentacle: true}),

		// A request may hold keys whose names differ only in case, as an
		// object's tags may. AWS says a condition on such a name "matches a
		// resource tag key named either TagKey1 or tagkey1, but not both"
		// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition.md?plain=1#L25),
		// so Amazon S3 evaluates one of the spellings, and it does not say
		// which: a Deny holds when the key as written makes it hold, or when
		// any other spelling's Amazon S3 answer, taken alone, holds or is
		// undefined. Spellings that all answer false leave it false.
		Entry("two keys that differ only in case, the one the condition names in neither's case matching",
			cond(policy.OpStringEquals, "s3:ExistingObjectTag/ENV", "prod"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case, neither spelled as the condition spells it, both answering false",
			cond(policy.OpStringEquals, "s3:ExistingObjectTag/ENV", "test"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"}, same(false)),
		Entry("two keys that differ only in case, neither spelled as the condition spells it, both answering true",
			cond(policy.OpStringEquals, "s3:ExistingObjectTag/ENV", "prod", "dev"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case, one answering false and one in a form with no answer",
			cond(policy.OpNumericLessThan, "S3:Max-Keys", "10"),
			[]string{"s3:max-keys", "50", "S3:MAX-KEYS", "0x5"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case, one spelled as the condition spells it, neither matching",
			cond(policy.OpStringEquals, "s3:ExistingObjectTag/env", "test"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"}, same(false)),
		Entry("two keys that differ only in case, the one spelled as the condition does not matching",
			cond(policy.OpStringEquals, "s3:ExistingObjectTag/env", "prod"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case under a negated operator, one matching",
			cond(policy.OpStringNotEquals, "s3:ExistingObjectTag/ENV", "prod"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case under a negated operator, both matching, so both answering false",
			cond(policy.OpStringNotEquals, "s3:ExistingObjectTag/ENV", "prod", "dev"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"}, same(false)),
		Entry("two keys that differ only in case under ForAllValues, the one spelled as the condition holding a value it lacks",
			cond(policy.OpForAllValuesStringEquals, "s3:ExistingObjectTag/env", "prod"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case under IpAddress, one matching",
			cond(policy.OpIPAddress, "AWS:SOURCEIP", "10.0.0.0/8"),
			[]string{"aws:SourceIp", "192.168.1.5", "AWS:SourceIP", "10.1.1.1"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case under Null false",
			cond(policy.OpNull, "s3:ExistingObjectTag/ENV", "false"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"},
			verdict{allowSquid: true, allowTentacle: false, denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case under IfExists, the one spelled as the condition not matching",
			ifExists(cond(policy.OpStringEquals, "s3:ExistingObjectTag/env", "prod")),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case under ForAnyValue, one matching",
			cond(policy.OpForAnyValueStringEquals, "s3:ExistingObjectTag/ENV", "prod"),
			[]string{"s3:ExistingObjectTag/Env", "prod", "s3:ExistingObjectTag/env", "dev"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two keys that differ only in case under a typed operator, one matching",
			cond(policy.OpNumericLessThan, "S3:Max-Keys", "10"),
			[]string{"s3:max-keys", "50", "S3:MAX-KEYS", "5"},
			verdict{denySquid: true, denyTentacle: true}),

		// Beside the key as written, each other spelling still counts alone,
		// by Amazon S3's answer and not by radosgw's reading of its values.
		Entry("the key as written not matching, another spelling a later date at a UTC offset",
			cond(policy.OpDateGreaterThan, "s3:RequestObjectTag/when", "2024-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2020-01-01T00:00:00Z", "s3:RequestObjectTag/When", "2030-01-01T00:00:00+00:00"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("the key as written the number, another spelling one less past 2^53",
			cond(policy.OpNumericNotEquals, "s3:RequestObjectTag/n", "9007199254740993"),
			[]string{"s3:RequestObjectTag/n", "9007199254740993", "s3:RequestObjectTag/N", "9007199254740992"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("the key as written not matching, another spelling the pattern's own brackets",
			cond(policy.OpStringLike, "s3:RequestObjectTag/path", "home/[a]/*"),
			[]string{"s3:RequestObjectTag/path", "tmp/x", "s3:RequestObjectTag/Path", "home/[a]/x"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("the key as written not matching, another spelling in a form with no answer",
			cond(policy.OpDateGreaterThan, "s3:RequestObjectTag/when", "2024-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2020-01-01T00:00:00Z", "s3:RequestObjectTag/When", "soon"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("the key as written not matching, another spelling a word radosgw's brackets would match",
			cond(policy.OpStringLike, "s3:RequestObjectTag/path", "a[bc]"),
			[]string{"s3:RequestObjectTag/path", "tmp/x", "s3:RequestObjectTag/Path", "ab"}, same(false)),
		Entry("the key as written matching, another spelling not",
			cond(policy.OpStringLike, "s3:RequestObjectTag/path", "a[bc]"),
			[]string{"s3:RequestObjectTag/path", "ab", "s3:RequestObjectTag/Path", "tmp/x"}, same(true)),

		// The name's fold is ASCII's.
		Entry("a key whose name differs in the case of a letter outside ASCII",
			cond(policy.OpStringEquals, "s3:ExistingObjectTag/É", "x"), []string{"s3:ExistingObjectTag/é", "x"}, same(false)),
		Entry("a key whose name differs by a letter that folds to an ASCII one outside ASCII",
			cond(policy.OpStringEquals, "s3:ExistingObjectTag/K", "x"), []string{"s3:ExistingObjectTag/k", "x"}, same(false)),
	)

	// parsedCondition is the condition the release's policy parser makes of a
	// Deny statement's op block over key and values, so that IsRuntime is as
	// radosgw's parser sets it.
	parsedCondition := func(rel denc.Release, op, key string, values ...string) policy.Condition {
		GinkgoHelper()
		vals, err := json.Marshal(values)
		Expect(err).NotTo(HaveOccurred())
		text := `{"Version": "2012-10-17", "Statement": {"Effect": "Deny", "Principal": "*", "Action": "s3:ListBucket",
			"Resource": "arn:aws:s3:::b", "Condition": {"` + op + `": {"` + key + `": ` + string(vals) + `}}}}`
		return mustParse(text, parseOpts(rel)).Statements[0].Conditions[0]
	}

	expectParsedVerdict := func(op, key string, values, kv []string, want verdict) {
		GinkgoHelper()
		expectVerdicts(parsedCondition(denc.Squid, op, key, values...), parsedCondition(denc.Tentacle, op, key, values...), kv, want)
	}

	// radosgw finds no key spelled as the condition spells it, so its reading
	// of the found key's values takes no part: a key found only in another case
	// takes Amazon S3's answer, where the IAM User Guide defines one. radosgw's
	// reading of these values, Squid's any-pair-differs, its runtime reading of
	// a whole-value variable or its literal reading of a variable inside a
	// value, would hold where Amazon S3's does not.
	DescribeTable("serves where radosgw finds no key and Amazon S3's answer for the key in another case is false",
		expectParsedVerdict,
		Entry("StringNotEquals naming two values, the key one of them, where Squid's any-pair-differs holds",
			"StringNotEquals", "S3:X-Amz-Server-Side-Encryption", []string{"AES256", "aws:kms"},
			[]string{"s3:x-amz-server-side-encryption", "AES256"}, same(false)),
		Entry("StringNotEquals naming two values, the key neither",
			"StringNotEquals", "S3:X-Amz-Server-Side-Encryption", []string{"AES256", "aws:kms"},
			[]string{"s3:x-amz-server-side-encryption", "aws:kms:dsse"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NumericNotEquals naming two values, the key one of them, where Squid's any-pair-differs holds",
			"NumericNotEquals", "S3:MAX-KEYS", []string{"10", "11"},
			[]string{"s3:max-keys", "10"}, same(false)),
		Entry("StringNotEquals over a whole-value variable in another case, set to the key's value, where Tentacle's runtime reading holds",
			"StringNotEquals", "S3:Prefix", []string{"${AWS:USERNAME}"},
			[]string{"s3:prefix", "alice", "aws:username", "alice"}, same(false)),
		Entry("StringNotEquals over a whole-value variable in another case, set to another value",
			"StringNotEquals", "S3:Prefix", []string{"${AWS:USERNAME}"},
			[]string{"s3:prefix", "bob", "aws:username", "alice"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringNotEquals over a default that is the key's value, the variable unset, where Tentacle's runtime reading holds",
			"StringNotEquals", "S3:Prefix", []string{"${aws:username, 'alice'}"},
			[]string{"s3:prefix", "alice"}, same(false)),
		Entry("StringNotEquals over the key's own name as a variable, where Tentacle's runtime reading holds",
			"StringNotEquals", "S3:Prefix", []string{"${S3:Prefix}"},
			[]string{"s3:prefix", "alice"}, same(false)),
		Entry("StringNotLike over a variable inside a pattern, the key matching it, where radosgw's literal reading holds",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:username}/*"},
			[]string{"s3:prefix", "home/alice/x", "aws:username", "alice"}, same(false)),
		Entry("StringNotLike over a variable inside a pattern, the key not matching it",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:username}/*"},
			[]string{"s3:prefix", "home/bob/x", "aws:username", "alice"}, verdict{denySquid: true, denyTentacle: true}),
	)

	// Amazon S3's Like and ARN operators read * and ? alone, and every other
	// character as itself; radosgw's matcher is fnmatch's, which reads [...]
	// and \ too. Amazon S3's answer for a key found only in another case uses
	// Amazon S3's matcher.
	DescribeTable("matches a key found only in another case with Amazon S3's wildcards alone",
		expectParsedVerdict,
		Entry("a variable inside a pattern, set to the value the key holds",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:Referer}/*"},
			[]string{"s3:prefix", "home/alice/x", "aws:Referer", "alice"}, same(false)),
		Entry("a variable inside a pattern, set to another value",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:Referer}/*"},
			[]string{"s3:prefix", "home/alice/x", "aws:Referer", "bob"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable inside a pattern, unset",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:Referer}/*"},
			[]string{"s3:prefix", "home/alice/x"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable inside a pattern, set to a value holding a wildcard",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:Referer}/*"},
			[]string{"s3:prefix", "home/alice/x", "aws:Referer", "a*"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable inside a pattern, set to bracket expressions",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:Referer}/*"},
			[]string{"s3:prefix", "home/alice/x", "aws:Referer", "[a-z][a-z][a-z][a-z][a-z]"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable inside a pattern, set to backslash escapes",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:Referer}/*"},
			[]string{"s3:prefix", "home/alice/x", "aws:Referer", `\a\l\i\c\e`}, verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable inside a pattern, set to bracket expressions, the key as written",
			"StringNotLike", "s3:prefix", []string{"home/${aws:Referer}/*"},
			[]string{"s3:prefix", "home/alice/x", "aws:Referer", "[a-z][a-z][a-z][a-z][a-z]"}, same(true)),
		Entry("a variable's default holding a wildcard, the variable unset",
			"StringNotLike", "S3:Prefix", []string{"home/${aws:username, '*'}"},
			[]string{"s3:prefix", "home/x"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringNotLike over a pattern holding a bracket expression, the key a word it would match",
			"StringNotLike", "S3:Prefix", []string{"[hH]ome/*"},
			[]string{"s3:prefix", "home/a"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringNotLike over a pattern holding a bracket expression, the key that text",
			"StringNotLike", "S3:Prefix", []string{"[hH]ome/*"},
			[]string{"s3:prefix", "[hH]ome/a"}, same(false)),
		Entry("StringNotLike over a pattern holding a backslash, the key the character it would escape",
			"StringNotLike", "S3:Prefix", []string{`a\*`},
			[]string{"s3:prefix", "a*"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringNotLike over a pattern holding a backslash, the key the backslash and more",
			"StringNotLike", "S3:Prefix", []string{`a\*`},
			[]string{"s3:prefix", `a\b`}, same(false)),
		Entry("ArnNotLike over a pattern holding a bracket expression",
			"ArnNotLike", "AWS:SOURCEARN", []string{"arn:aws:s3:::[b]/*"},
			[]string{"aws:SourceArn", "arn:aws:s3:::b/k"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringLike over a pattern holding a bracket expression, the key that text",
			"StringLike", "S3:Prefix", []string{"a[bc]"},
			[]string{"s3:prefix", "a[bc]"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringLike over a pattern holding a bracket expression, the key a word it would match",
			"StringLike", "S3:Prefix", []string{"a[bc]"},
			[]string{"s3:prefix", "ab"}, same(false)),
	)

	// Where the IAM User Guide gives Amazon S3 no answer for a key found only
	// in another case, the condition holds, whatever its operator, so the Deny
	// refuses: radosgw finds no key there, so its reading of the found key's
	// values, which a positive operator could fail, takes no part.
	DescribeTable("holds on a key found only in another case where Amazon S3 has no answer, whatever the operator",
		func(c policy.Condition, kv []string, want verdict) {
			expectVerdict(c, kv, want)
		},
		Entry("an empty value list under a positive operator",
			cond(policy.OpStringEquals, "S3:Prefix"), []string{"s3:prefix", "home"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key holding two values under a positive single-valued operator, neither matching",
			cond(policy.OpStringEquals, "S3:RequestObjectTag/Owner", "bob"),
			[]string{"s3:RequestObjectTag/owner", "alice", "s3:RequestObjectTag/owner", "mallory"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a hexadecimal number under a positive operator",
			cond(policy.OpNumericEquals, "S3:MAX-KEYS", "0x6"), []string{"s3:max-keys", "5"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a date without a time whose day holds the instant it is compared with",
			cond(policy.OpDateLessThan, "AWS:CurrentTime", "2026-11-20"),
			[]string{"aws:CurrentTime", "2026-11-20T12:00:00Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two dates without a time whose days may overlap in some time zone",
			cond(policy.OpDateLessThan, "S3:RequestObjectTag/When", "2020-01-02"),
			[]string{"s3:RequestObjectTag/when", "2020-01-01"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("the same date without a time on both sides, under DateEquals",
			cond(policy.OpDateEquals, "S3:RequestObjectTag/When", "2020-01-01"),
			[]string{"s3:RequestObjectTag/when", "2020-01-01"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a year and month whose month holds the instant it is compared with",
			cond(policy.OpDateGreaterThan, "S3:RequestObjectTag/When", "2020-01-15T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2020-01"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a date without a time whose day does not exist",
			cond(policy.OpDateGreaterThan, "S3:RequestObjectTag/When", "2030-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2024-02-30"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a date without a time whose day is 00",
			cond(policy.OpDateGreaterThan, "S3:RequestObjectTag/When", "2030-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2024-01-00"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a year and month whose month does not exist",
			cond(policy.OpDateGreaterThan, "S3:RequestObjectTag/When", "2030-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2024-13"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a year and month whose month is 00",
			cond(policy.OpDateGreaterThan, "S3:RequestObjectTag/When", "2030-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2024-00"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a fraction of a second without the seconds",
			cond(policy.OpDateLessThan, "S3:RequestObjectTag/When", "2024-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2030-01-01T00:00.5Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("four digits alone under a positive operator",
			cond(policy.OpDateGreaterThan, "S3:RequestObjectTag/When", "2024-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "9999"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a UTC offset of 24 hours",
			cond(policy.OpDateLessThan, "S3:RequestObjectTag/When", "2024-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2030-01-01T00:00:00+24:00"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a UTC offset of 60 minutes",
			cond(policy.OpDateLessThan, "S3:RequestObjectTag/When", "2024-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2030-01-01T00:00:00-00:60"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a Bool key value other than true or false",
			cond(policy.OpBool, "AWS:SECURETRANSPORT", "true"), []string{"aws:SecureTransport", "0"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a binary key value that is the policy's base64, equal once both are decoded and not as the bytes it is",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "QUJD"), []string{"s3:RequestObjectTag/b", "QUJD"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a binary key value that is the bytes the policy's value decodes to, and not base64 for them",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "WVdKag=="), []string{"s3:RequestObjectTag/b", "YWJj"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a binary key value of one byte, the one the policy's value decodes to",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "YQ=="), []string{"s3:RequestObjectTag/b", "a"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a binary key value without its padding, which a decoder may take or refuse",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "QUI="), []string{"s3:RequestObjectTag/b", "QUI"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a binary key value with a line break inside a group of four, which a decoder may skip or refuse",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "QUJD"), []string{"s3:RequestObjectTag/b", "QU\nJD"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a binary key value in the URL-safe alphabet, which radosgw's decoder reads as the policy's bytes",
			cond(policy.OpBinaryEquals, "S3:ExistingObjectTag/B", "Pz8/"), []string{"s3:ExistingObjectTag/b", "Pz8_"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a binary condition value that is not base64",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "hello"), []string{"s3:RequestObjectTag/b", "hello"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEqualsIgnoreCase over a letter outside ASCII in its other case",
			cond(policy.OpStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "é"), []string{"s3:ExistingObjectTag/b", "É"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("StringNotEqualsIgnoreCase over a letter outside ASCII in its other case",
			cond(policy.OpStringNotEqualsIgnoreCase, "S3:ExistingObjectTag/B", "é"), []string{"s3:ExistingObjectTag/b", "É"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEqualsIgnoreCase over a letter outside ASCII that folds to an ASCII one",
			cond(policy.OpStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "k"), []string{"s3:ExistingObjectTag/b", "K"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("ForAnyValue:StringEqualsIgnoreCase over letters outside ASCII in their other case",
			cond(policy.OpForAnyValueStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "direcção"),
			[]string{"s3:ExistingObjectTag/b", "DIRECÇÃO"},
			verdict{denySquid: true, denyTentacle: true}),
		// U+0130, a capital I with a dot, and U+0131, a small i without one,
		// are unequal to I and i under both folds, and equal to them once
		// each is mapped to upper case and then to lower case.
		Entry("StringEqualsIgnoreCase, i against U+0130",
			cond(policy.OpStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "i"), []string{"s3:ExistingObjectTag/b", "İ"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEqualsIgnoreCase, I against U+0130",
			cond(policy.OpStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "I"), []string{"s3:ExistingObjectTag/b", "İ"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEqualsIgnoreCase, I against U+0131",
			cond(policy.OpStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "I"), []string{"s3:ExistingObjectTag/b", "ı"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEqualsIgnoreCase, i against U+0131",
			cond(policy.OpStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "i"), []string{"s3:ExistingObjectTag/b", "ı"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a Numeric operator over a date and time, as radosgw sets aws:EpochTime",
			cond(policy.OpNumericLessThan, "aws:epochtime", "1700000000"),
			[]string{"aws:EpochTime", "2026-11-20T12:00:00.000000000Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("an ARN of fewer than six components, under a positive operator",
			cond(policy.OpArnLike, "AWS:SOURCEARN", "arn:aws:s3:::b/*"), []string{"aws:SourceArn", "b/k"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a question mark over a character outside the Basic Multilingual Plane, under ForAnyValue",
			cond(policy.OpForAnyValueStringLike, "S3:Prefix", "a?b"), []string{"s3:prefix", "a\U0001F600b"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("two question marks over one character outside the Basic Multilingual Plane",
			cond(policy.OpStringLike, "S3:Prefix", "??"), []string{"s3:prefix", "\U0001F600"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("an empty value list",
			cond(policy.OpStringNotEquals, "S3:X-Amz-Server-Side-Encryption"), []string{"s3:x-amz-server-side-encryption", "AES256"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key holding two values under a single-valued negated operator",
			cond(policy.OpStringNotEquals, "S3:RequestObjectTag/owner", "alice"),
			[]string{"s3:RequestObjectTag/owner", "alice", "s3:RequestObjectTag/owner", "mallory"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a typed operator one of whose values does not convert",
			cond(policy.OpNumericNotEquals, "S3:MAX-KEYS", "5", "x"), []string{"s3:max-keys", "10"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a hexadecimal number, which radosgw converts",
			cond(policy.OpNumericNotEquals, "S3:MAX-KEYS", "5", "0x6"), []string{"s3:max-keys", "5"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a hexadecimal key value",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/Level", "1", "2"), []string{"s3:RequestObjectTag/level", "0x1"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a hexadecimal key value under one value",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "1"), []string{"s3:RequestObjectTag/n", "0x1"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key value after a space",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/Level", "1", "2"), []string{"s3:RequestObjectTag/level", " 1"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key value with an exponent",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/Level", "1", "2"), []string{"s3:RequestObjectTag/level", "1e0"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a key value written as a plain decimal, which Amazon S3 answers",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/Level", "1", "2"), []string{"s3:RequestObjectTag/level", "1.0"},
			same(false)),
		Entry("a date in a form radosgw reads and the W3C forms lack",
			cond(policy.OpDateNotEquals, "AWS:CURRENTTIME", "2024-01-01T00:00:00Z", "2025-01-01T00:00:00Z"),
			[]string{"aws:CurrentTime", "2024-01-01T00Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("four digits alone, a W3C year or a count of seconds",
			cond(policy.OpDateNotEquals, "AWS:CURRENTTIME", "2024"), []string{"aws:CurrentTime", "1970-01-01T00:33:44Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a calendar date that does not exist",
			cond(policy.OpDateNotEquals, "AWS:CURRENTTIME", "2024-02-30T00:00:00Z"), []string{"aws:CurrentTime", "2024-03-01T00:00:00Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a month out of range",
			cond(policy.OpDateNotEquals, "AWS:CURRENTTIME", "2024-13-01T00:00:00Z"), []string{"aws:CurrentTime", "2025-01-01T00:00:00Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("an hour out of range",
			cond(policy.OpDateNotEquals, "AWS:CURRENTTIME", "2024-01-01T24:00:00Z"), []string{"aws:CurrentTime", "2024-01-02T00:00:00Z"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("an address written in an IPv6 form that holds an IPv4 one, against a range the IPv4 one is within",
			cond(policy.OpNotIPAddress, "AWS:SOURCEIP", "203.0.113.0/24"), []string{"aws:SourceIp", "::ffff:203.0.113.7"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a question mark over a character outside the Basic Multilingual Plane",
			cond(policy.OpStringNotLike, "S3:Prefix", "a?b"), []string{"s3:prefix", "a\U0001F600b"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a question mark over a character outside the Basic Multilingual Plane, under a positive operator",
			cond(policy.OpStringLike, "S3:Prefix", "a?b"), []string{"s3:prefix", "a\U0001F600b"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a whole-value variable whose default holds a wildcard",
			cond(policy.OpStringNotLike, "S3:Prefix", "${aws:Referer, '*'}"), []string{"s3:prefix", "home/x"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a whole-value variable whose value holds a wildcard",
			cond(policy.OpStringNotLike, "S3:Prefix", "${aws:username}"), []string{"s3:prefix", "home/x", "aws:username", "*"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a repeated tag key, one of its values matching",
			cond(policy.OpStringNotEquals, "S3:RequestObjectTag/Owner", "alice"),
			[]string{"s3:RequestObjectTag/owner", "alice", "s3:RequestObjectTag/owner", "mallory"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("an ARN of fewer than six components",
			cond(policy.OpArnNotLike, "AWS:SOURCEARN", "arn:aws:s3:::b/*"), []string{"aws:SourceArn", "b/k"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a special form",
			cond(policy.OpStringEquals, "S3:Prefix", "a${*}b"), []string{"s3:prefix", "zzz"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable whose key the request holds two values for",
			cond(policy.OpStringNotEquals, "S3:Prefix", "x", "${aws:username}"),
			[]string{"s3:prefix", "x", "aws:username", "a", "aws:username", "b"},
			verdict{denySquid: true, denyTentacle: true}),
		Entry("a variable whose value holds a wildcard, under StringNotLike",
			cond(policy.OpStringNotLike, "S3:Prefix", "x", "home/${aws:username}"),
			[]string{"s3:prefix", "home/b", "aws:username", "*"},
			verdict{denySquid: true, denyTentacle: true}),
	)

	// Amazon S3's typed operators compare exactly: a number as a rational, a
	// date as an instant that does not wrap. radosgw's double and 64-bit
	// nanosecond count call some of these values equal.
	DescribeTable("compares typed values exactly for a key found only in another case",
		func(c policy.Condition, kv []string, want verdict) {
			expectVerdict(c, kv, want)
		},
		Entry("DateNotEquals past 2554, the key 2^64 nanoseconds earlier",
			cond(policy.OpDateNotEquals, "S3:RequestObjectTag/When", "2600-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2015-06-13T00:25:26.290448384Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateNotEquals past 2554, the key another date",
			cond(policy.OpDateNotEquals, "S3:RequestObjectTag/When", "2600-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2024-01-01T00:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateNotEquals past 2554, the key that date",
			cond(policy.OpDateNotEquals, "S3:RequestObjectTag/When", "2600-01-01T00:00:00Z"),
			[]string{"s3:RequestObjectTag/when", "2600-01-01T00:00:00Z"}, same(false)),
		Entry("NumericNotEquals past 2^53, the key one less",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "9007199254740993"),
			[]string{"s3:RequestObjectTag/n", "9007199254740992"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NumericNotEquals past 2^53, the key another number",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "9007199254740993"),
			[]string{"s3:RequestObjectTag/n", "9007199254740000"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NumericNotEquals past 2^53, the key that number",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "9007199254740993"),
			[]string{"s3:RequestObjectTag/n", "9007199254740993"}, same(false)),
		Entry("NumericNotEquals on a decimal past a double's digits",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "0.1"),
			[]string{"s3:RequestObjectTag/n", "0.10000000000000001"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NumericNotEquals on decimals written with more zeros",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "0.1"),
			[]string{"s3:RequestObjectTag/n", "0.100"}, same(false)),
		Entry("DateLessThan 2600, the key now",
			cond(policy.OpDateLessThan, "AWS:CurrentTime", "2600-01-01T00:00:00Z"),
			[]string{"aws:CurrentTime", "2024-01-01T00:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateGreaterThan 2600, the key now",
			cond(policy.OpDateGreaterThan, "AWS:CurrentTime", "2600-01-01T00:00:00Z"),
			[]string{"aws:CurrentTime", "2024-01-01T00:00:00Z"}, same(false)),
		Entry("a date with a fraction, compared to the nanosecond and past it",
			cond(policy.OpDateLessThan, "AWS:CurrentTime", "2024-01-01T00:00:00.0000000001Z"),
			[]string{"aws:CurrentTime", "2024-01-01T00:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("epoch seconds against a W3C date of the same instant",
			cond(policy.OpDateNotEquals, "AWS:CurrentTime", "1704067200"),
			[]string{"aws:CurrentTime", "2024-01-01T00:00:00Z"}, same(false)),

		Entry("NumericNotEquals on a number written with leading zeros",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "007"),
			[]string{"s3:RequestObjectTag/n", "7"}, same(false)),
		Entry("NumericNotEquals on a number written with a zero fraction",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "7.0"),
			[]string{"s3:RequestObjectTag/n", "7"}, same(false)),
		Entry("NumericNotEquals on a negative zero",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "-0.0"),
			[]string{"s3:RequestObjectTag/n", "0"}, same(false)),
		Entry("NumericNotEquals on 40-digit numbers one apart",
			cond(policy.OpNumericNotEquals, "S3:RequestObjectTag/N", "1234567890123456789012345678901234567891"),
			[]string{"s3:RequestObjectTag/n", "1234567890123456789012345678901234567890"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NumericLessThan on integers of unequal length, the key shorter",
			cond(policy.OpNumericLessThan, "S3:RequestObjectTag/N", "10"),
			[]string{"s3:RequestObjectTag/n", "9"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NumericLessThan on integers of unequal length, the key longer",
			cond(policy.OpNumericLessThan, "S3:RequestObjectTag/N", "9"),
			[]string{"s3:RequestObjectTag/n", "10"}, same(false)),
		Entry("NumericLessThan on fractions of unequal length, the key less",
			cond(policy.OpNumericLessThan, "S3:RequestObjectTag/N", "0.5"),
			[]string{"s3:RequestObjectTag/n", "0.49"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NumericLessThan on fractions of unequal length, the key more",
			cond(policy.OpNumericLessThan, "S3:RequestObjectTag/N", "0.5"),
			[]string{"s3:RequestObjectTag/n", "0.51"}, same(false)),
		Entry("NumericLessThan between negative numbers, the key less",
			cond(policy.OpNumericLessThan, "S3:RequestObjectTag/N", "-1.5"),
			[]string{"s3:RequestObjectTag/n", "-2"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NumericLessThan between negative numbers, the key more",
			cond(policy.OpNumericLessThan, "S3:RequestObjectTag/N", "-2"),
			[]string{"s3:RequestObjectTag/n", "-1.5"}, same(false)),
		Entry("NumericLessThan a negative number, the key positive",
			cond(policy.OpNumericLessThan, "S3:RequestObjectTag/N", "-1"),
			[]string{"s3:RequestObjectTag/n", "1"}, same(false)),
		Entry("epoch seconds written with leading zeros",
			cond(policy.OpDateNotEquals, "AWS:CurrentTime", "01704067200"),
			[]string{"aws:CurrentTime", "2024-01-01T00:00:00Z"}, same(false)),
		Entry("epoch seconds past 2554 against a W3C date of the same instant",
			cond(policy.OpDateNotEquals, "AWS:CurrentTime", "19880899200"),
			[]string{"aws:CurrentTime", "2600-01-01T00:00:00Z"}, same(false)),
		Entry("a date before 1970 with a fraction of a second, the key earlier",
			cond(policy.OpDateLessThan, "S3:RequestObjectTag/When", "1969-12-31T23:59:59.5Z"),
			[]string{"s3:RequestObjectTag/when", "1969-12-31T23:59:59.25Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("a date before 1970 with a fraction of a second, the key later",
			cond(policy.OpDateLessThan, "S3:RequestObjectTag/When", "1969-12-31T23:59:59.25Z"),
			[]string{"s3:RequestObjectTag/when", "1969-12-31T23:59:59.5Z"}, same(false)),

		// A W3C date and time ends in a time zone designator, Z or an offset
		// from UTC, and "1994-11-05T08:15:30-05:00" and "1994-11-05T13:15:30Z"
		// are the same instant
		// (https://github.com/exelearning/iteexe/blob/3f4452780a4b2cfd2bc9d9152e92dfd86d2ff0cc/exe/webui/docs/W3C-NOTE-datetime.html#L195-L198).
		// radosgw reads no offset.
		Entry("DateGreaterThan, the key later and written with a UTC offset",
			cond(policy.OpDateGreaterThan, "S3:ExistingObjectTag/When", "2024-01-01T00:00:00Z"),
			[]string{"s3:ExistingObjectTag/when", "2030-01-01T00:00:00+00:00"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateGreaterThan, the key earlier and written with a UTC offset",
			cond(policy.OpDateGreaterThan, "S3:ExistingObjectTag/When", "2024-01-01T00:00:00Z"),
			[]string{"s3:ExistingObjectTag/when", "2020-01-01T00:00:00+00:00"}, same(false)),
		Entry("DateLessThan a date written with a UTC offset, the key earlier",
			cond(policy.OpDateLessThan, "S3:ExistingObjectTag/When", "2030-01-01T00:00:00+00:00"),
			[]string{"s3:ExistingObjectTag/when", "2024-06-01T00:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("an offset behind UTC against the same instant in UTC",
			cond(policy.OpDateNotEquals, "S3:ExistingObjectTag/When", "1994-11-05T08:15:30-05:00"),
			[]string{"s3:ExistingObjectTag/when", "1994-11-05T13:15:30Z"}, same(false)),
		Entry("an offset ahead of UTC against the same instant in UTC",
			cond(policy.OpDateNotEquals, "S3:ExistingObjectTag/When", "2024-01-01T02:30:00.5+02:30"),
			[]string{"s3:ExistingObjectTag/when", "2024-01-01T00:00:00.5Z"}, same(false)),
		Entry("an offset ahead of UTC that puts the instant in the day before, the key after it",
			cond(policy.OpDateLessThan, "S3:ExistingObjectTag/When", "2024-01-01T00:30+02:00"),
			[]string{"s3:ExistingObjectTag/when", "2023-12-31T23:00:00Z"}, same(false)),
		Entry("an offset ahead of UTC that puts the instant in the day before, the key before it",
			cond(policy.OpDateLessThan, "S3:ExistingObjectTag/When", "2024-01-01T00:30+02:00"),
			[]string{"s3:ExistingObjectTag/when", "2023-12-31T22:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),

		// "When you include non-zero bits after the masked bits, they are not
		// considered for the condition"
		// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/access-analyzer-reference-policy-checks.md?plain=1#L2951).
		Entry("NotIpAddress, a range written with host bits set, the address within the network it names",
			cond(policy.OpNotIPAddress, "AWS:SOURCEIP", "203.0.113.5/24"),
			[]string{"aws:SourceIp", "203.0.113.9"}, same(false)),
		Entry("NotIpAddress, a range written with host bits set, the address outside the network",
			cond(policy.OpNotIPAddress, "AWS:SOURCEIP", "203.0.113.5/24"),
			[]string{"aws:SourceIp", "198.51.100.7"}, verdict{denySquid: true, denyTentacle: true}),
	)

	// Where the IAM User Guide's sentence for an operator reads more than one
	// way, Amazon S3's answer is the one every reading gives. BinaryEquals:
	// "It compares the value of the specified key byte for byte against a
	// base-64 encoded representation of the binary value in the policy"
	// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L210),
	// so the key's value is the binary value itself, or base64 for it, by
	// radosgw's decoder or another; a key value that is neither is unequal
	// every way. The IgnoreCase operators: "Exact matching, ignoring
	// case"
	// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L34),
	// which folds ASCII's letters alone, folds every letter, or maps every
	// letter to upper case and then to lower case.
	DescribeTable("answers where every reading of the guide's sentence agrees, for a key found only in another case",
		func(c policy.Condition, kv []string, want verdict) {
			expectVerdict(c, kv, want)
		},
		Entry("BinaryEquals, the key base64 for other bytes",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "YQ=="),
			[]string{"s3:RequestObjectTag/b", "Yg=="}, same(false)),
		Entry("BinaryEquals, the key text that is neither the bytes nor base64",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "YQ=="),
			[]string{"s3:RequestObjectTag/b", "hello"}, same(false)),
		Entry("BinaryEquals, the key empty",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "QUJD"),
			[]string{"s3:RequestObjectTag/b", ""}, same(false)),
		Entry("BinaryEquals, the key base64 for other bytes and a line break",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "QUJD"),
			[]string{"s3:RequestObjectTag/b", "QUJE\n"}, same(false)),
		Entry("BinaryEquals, the key base64 for other bytes without its padding",
			cond(policy.OpBinaryEquals, "S3:RequestObjectTag/B", "QUI="),
			[]string{"s3:RequestObjectTag/b", "QUM"}, same(false)),
		Entry("BinaryEquals, the key in the URL-safe alphabet for other bytes",
			cond(policy.OpBinaryEquals, "S3:ExistingObjectTag/B", "Pz8/"),
			[]string{"s3:ExistingObjectTag/b", "Pz9_"}, same(false)),
		Entry("StringEqualsIgnoreCase over a letter outside ASCII in the same case",
			cond(policy.OpStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "Éa"),
			[]string{"s3:ExistingObjectTag/b", "ÉA"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringEqualsIgnoreCase over values that differ under either fold",
			cond(policy.OpStringEqualsIgnoreCase, "S3:ExistingObjectTag/B", "é"),
			[]string{"s3:ExistingObjectTag/b", "ü"}, same(false)),
		Entry("StringNotEqualsIgnoreCase over a letter outside ASCII in the same case",
			cond(policy.OpStringNotEqualsIgnoreCase, "S3:ExistingObjectTag/B", "Éa"),
			[]string{"s3:ExistingObjectTag/b", "ÉA"}, same(false)),
	)

	// "The following example shows how to mix IPv4 and IPv6 addresses to cover
	// all of your organization's valid IP addresses"
	// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L258),
	// over the list these rows take
	// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L268-L272):
	// an address is not within a range of the other family, which is an
	// answer.
	DescribeTable("answers an address against ranges of both families for a key found only in another case",
		expectParsedVerdict,
		Entry("IpAddress over the guide's list, an IPv4 address outside both ranges",
			"IpAddress", "aws:SourceIP", []string{"203.0.113.0/24", "2001:DB8:1234:5678::/64"},
			[]string{"aws:SourceIp", "10.9.9.9"}, same(false)),
		Entry("IpAddress over the guide's list, an IPv6 address outside both ranges",
			"IpAddress", "aws:SourceIP", []string{"203.0.113.0/24", "2001:DB8:1234:5678::/64"},
			[]string{"aws:SourceIp", "2001:db8:ffff::1"}, same(false)),
		Entry("IpAddress over the guide's list, an address within the IPv4 range",
			"IpAddress", "aws:SourceIP", []string{"203.0.113.0/24", "2001:DB8:1234:5678::/64"},
			[]string{"aws:SourceIp", "203.0.113.7"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("IpAddress over the guide's list, an address within the IPv6 range",
			"IpAddress", "aws:SourceIP", []string{"203.0.113.0/24", "2001:DB8:1234:5678::/64"},
			[]string{"aws:SourceIp", "2001:db8:1234:5678::1"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NotIpAddress over the guide's list, an address outside both ranges",
			"NotIpAddress", "aws:SourceIP", []string{"203.0.113.0/24", "2001:DB8:1234:5678::/64"},
			[]string{"aws:SourceIp", "10.9.9.9"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NotIpAddress over the guide's list, an address within the IPv4 range",
			"NotIpAddress", "aws:SourceIP", []string{"203.0.113.0/24", "2001:DB8:1234:5678::/64"},
			[]string{"aws:SourceIp", "203.0.113.7"}, same(false)),
		Entry("NotIpAddress over the guide's list, an address within the IPv6 range",
			"NotIpAddress", "aws:SourceIP", []string{"203.0.113.0/24", "2001:DB8:1234:5678::/64"},
			[]string{"aws:SourceIp", "2001:db8:1234:5678::1"}, same(false)),
		Entry("IpAddress over an IPv6 range alone, an IPv4 address",
			"IpAddress", "aws:SourceIP", []string{"2001:DB8:1234:5678::/64"},
			[]string{"aws:SourceIp", "198.51.100.7"}, same(false)),
		Entry("NotIpAddress over an IPv4 range alone, an IPv6 address",
			"NotIpAddress", "aws:SourceIP", []string{"10.0.0.0/8"},
			[]string{"aws:SourceIp", "2001:db8::1"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("NotIpAddress over an IPv4 range and every IPv6 address, an IPv4 address outside the range",
			"NotIpAddress", "aws:SourceIP", []string{"203.0.113.0/24", "::/0"},
			[]string{"aws:SourceIp", "198.51.100.7"}, verdict{denySquid: true, denyTentacle: true}),
	)

	// The IAM User Guide's policy checks recommend a date without a time: "We
	// recommend that you use the YYYY-MM-DD format"
	// (https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/access-analyzer-reference-policy-checks.md?plain=1#L1952).
	// Neither it nor the W3C note says which instant of that day, in which
	// time zone, such a date is, so it may be any instant of the day at any
	// offset from UTC, and of the month or the year for the shorter forms.
	// Where every such instant gives a comparison the same result, that is
	// Amazon S3's answer. No offset is more than 14 hours, so 2020-01-01
	// runs from 2019-12-31T10:00:00Z up to 2020-01-02T14:00:00Z.
	DescribeTable("answers a date without a time where every instant it may name agrees, for a key found only in another case",
		expectParsedVerdict,
		Entry("DateLessThan a date long past, the key now",
			"DateLessThan", "AWS:CurrentTime", []string{"2020-01-01"},
			[]string{"aws:CurrentTime", "1795089600"}, same(false)),
		Entry("DateGreaterThan a date long past, the key now",
			"DateGreaterThan", "AWS:CurrentTime", []string{"2020-01-01"},
			[]string{"aws:CurrentTime", "1795089600"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateGreaterThan a date past 2554, the key now, which radosgw's count of nanoseconds would call later",
			"DateGreaterThan", "AWS:CurrentTime", []string{"2600-01-01"},
			[]string{"aws:CurrentTime", "1795089600"}, same(false)),
		Entry("DateLessThan a date past 2554, the key now",
			"DateLessThan", "AWS:CurrentTime", []string{"2600-01-01"},
			[]string{"aws:CurrentTime", "1795089600"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateGreaterThan, a tag date without a time before the policy's",
			"DateGreaterThan", "S3:ExistingObjectTag/When", []string{"2024-01-01T00:00:00Z"},
			[]string{"s3:ExistingObjectTag/when", "2020-06-15"}, same(false)),
		Entry("DateGreaterThan, a tag date without a time past 2554",
			"DateGreaterThan", "S3:RequestObjectTag/When", []string{"2024-01-01T00:00:00Z"},
			[]string{"s3:RequestObjectTag/when", "2600-01-01"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateLessThan, a tag year and month after the policy's date",
			"DateLessThan", "S3:RequestObjectTag/When", []string{"2024-01-01T00:00:00Z"},
			[]string{"s3:RequestObjectTag/when", "2030-01"}, same(false)),
		Entry("DateLessThan, two dates without a time more than a day apart, the key earlier",
			"DateLessThan", "S3:RequestObjectTag/When", []string{"2020-01-05"},
			[]string{"s3:RequestObjectTag/when", "2020-01-01"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateLessThan, two dates without a time more than a day apart, the key later",
			"DateLessThan", "S3:RequestObjectTag/When", []string{"2020-01-01"},
			[]string{"s3:RequestObjectTag/when", "2020-01-05"}, same(false)),
		Entry("DateNotEquals a date without a time, the key outside its day",
			"DateNotEquals", "S3:RequestObjectTag/When", []string{"2020-01-01"},
			[]string{"s3:RequestObjectTag/when", "2020-01-03T00:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),

		Entry("DateGreaterThanEquals a date, the key a second before the day starts in the zone farthest ahead",
			"DateGreaterThanEquals", "S3:RequestObjectTag/When", []string{"2020-01-01"},
			[]string{"s3:RequestObjectTag/when", "2019-12-31T09:59:59Z"}, same(false)),
		Entry("DateGreaterThanEquals a date, the key the instant the day starts in the zone farthest ahead",
			"DateGreaterThanEquals", "S3:RequestObjectTag/When", []string{"2020-01-01"},
			[]string{"s3:RequestObjectTag/when", "2019-12-31T10:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateLessThanEquals a date, the key the instant the day ends in the zone farthest behind",
			"DateLessThanEquals", "S3:RequestObjectTag/When", []string{"2020-01-01"},
			[]string{"s3:RequestObjectTag/when", "2020-01-02T14:00:00Z"}, same(false)),
		Entry("DateLessThanEquals a date, the key a second before the day ends in the zone farthest behind",
			"DateLessThanEquals", "S3:RequestObjectTag/When", []string{"2020-01-01"},
			[]string{"s3:RequestObjectTag/when", "2020-01-02T13:59:59Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateEquals a date, the key the instant the day ends in the zone farthest behind",
			"DateEquals", "S3:RequestObjectTag/When", []string{"2020-01-01"},
			[]string{"s3:RequestObjectTag/when", "2020-01-02T14:00:00Z"}, same(false)),
		Entry("DateLessThanEquals a year and month, the key the instant a leap February ends in the zone farthest behind",
			"DateLessThanEquals", "S3:RequestObjectTag/When", []string{"2020-02"},
			[]string{"s3:RequestObjectTag/when", "2020-03-01T14:00:00Z"}, same(false)),
		Entry("DateLessThanEquals a year and month, the key a second before a leap February ends in the zone farthest behind",
			"DateLessThanEquals", "S3:RequestObjectTag/When", []string{"2020-02"},
			[]string{"s3:RequestObjectTag/when", "2020-03-01T13:59:59Z"}, verdict{denySquid: true, denyTentacle: true}),

		// Four digits alone are a W3C year and a count of seconds, so they may
		// be any instant of that year or that many seconds past the epoch.
		Entry("DateLessThan four digits, the key after that year and after that count of seconds",
			"DateLessThan", "AWS:CurrentTime", []string{"2020"},
			[]string{"aws:CurrentTime", "1795089600"}, same(false)),
		Entry("DateLessThan four digits, the key between that count of seconds and that year",
			"DateLessThan", "S3:RequestObjectTag/When", []string{"2020"},
			[]string{"s3:RequestObjectTag/when", "1980-01-01T00:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateEquals four digits, the key between that count of seconds and that year",
			"DateEquals", "S3:RequestObjectTag/When", []string{"2020"},
			[]string{"s3:RequestObjectTag/when", "2000-01-01T00:00:00Z"}, same(false)),
		Entry("DateEquals four digits, the key that count of seconds past the epoch",
			"DateEquals", "S3:RequestObjectTag/When", []string{"2020"},
			[]string{"s3:RequestObjectTag/when", "1970-01-01T00:33:40Z"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateLessThanEquals four digits, the key the instant that year ends in the zone farthest behind",
			"DateLessThanEquals", "S3:RequestObjectTag/When", []string{"2020"},
			[]string{"s3:RequestObjectTag/when", "2021-01-01T14:00:00Z"}, same(false)),
		Entry("DateLessThanEquals four digits, the key a second before that year ends in the zone farthest behind",
			"DateLessThanEquals", "S3:RequestObjectTag/When", []string{"2020"},
			[]string{"s3:RequestObjectTag/when", "2021-01-01T13:59:59Z"}, verdict{denySquid: true, denyTentacle: true}),
	)

	// A ? is one character, and for a character outside the Basic Multilingual
	// Plane the IAM User Guide does not say whether that is its one code point
	// or one of its two UTF-16 units. Where both readings of the condition
	// give the same answer, that is Amazon S3's.
	DescribeTable("answers a pattern over a character outside the Basic Multilingual Plane where both readings of ? agree",
		expectParsedVerdict,
		Entry("StringLike, a pattern without a ? matching beside one with a ?",
			"StringLike", "S3:ExistingObjectTag/Path", []string{"home/[a]/*", "zz?"},
			[]string{"s3:ExistingObjectTag/path", "home/[a]/\U0001F600"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringNotLike over the same patterns",
			"StringNotLike", "S3:ExistingObjectTag/Path", []string{"home/[a]/*", "zz?"},
			[]string{"s3:ExistingObjectTag/path", "home/[a]/\U0001F600"}, same(false)),
		Entry("StringLike, no pattern matching under either reading",
			"StringLike", "S3:ExistingObjectTag/Path", []string{"x", "y?"},
			[]string{"s3:ExistingObjectTag/path", "a\U0001F600"}, same(false)),
		Entry("StringLike, a ? and a * that take the character under either reading",
			"StringLike", "S3:ExistingObjectTag/Path", []string{"a?*"},
			[]string{"s3:ExistingObjectTag/path", "a\U0001F600"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("StringNotLike, a ? and a * that take the character under either reading",
			"StringNotLike", "S3:ExistingObjectTag/Path", []string{"a?*"},
			[]string{"s3:ExistingObjectTag/path", "a\U0001F600"}, same(false)),
		Entry("ArnLike, a pattern without a ? matching beside one with a ?",
			"ArnLike", "AWS:SOURCEARN", []string{"arn:aws:s3:::[a]*", "arn:aws:s3:::?"},
			[]string{"aws:SourceArn", "arn:aws:s3:::[a]\U0001F600"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("ArnNotLike over the same patterns",
			"ArnNotLike", "AWS:SOURCEARN", []string{"arn:aws:s3:::[a]*", "arn:aws:s3:::?"},
			[]string{"aws:SourceArn", "arn:aws:s3:::[a]\U0001F600"}, same(false)),
		Entry("ArnNotLike, a ? that takes the character under one reading alone",
			"ArnNotLike", "AWS:SOURCEARN", []string{"arn:aws:s3:::b?"},
			[]string{"aws:SourceArn", "arn:aws:s3:::b\U0001F600"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("ArnLike, a ? that takes the character under one reading alone",
			"ArnLike", "AWS:SOURCEARN", []string{"arn:aws:s3:::b?"},
			[]string{"aws:SourceArn", "arn:aws:s3:::b\U0001F600"}, verdict{denySquid: true, denyTentacle: true}),
	)

	// Dates through the parser, as a policy carries them.
	DescribeTable("reads a date with a UTC offset as its instant",
		expectParsedVerdict,
		Entry("DateGreaterThan, an object tagged with a later date at a UTC offset",
			"DateGreaterThan", "S3:ExistingObjectTag/When", []string{"2024-01-01T00:00:00Z"},
			[]string{"s3:ExistingObjectTag/when", "2030-01-01T00:00:00+00:00"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateGreaterThan, a write tagged with a later date at a UTC offset",
			"DateGreaterThan", "S3:RequestObjectTag/When", []string{"2024-01-01T00:00:00Z"},
			[]string{"s3:RequestObjectTag/when", "2030-01-01T00:00:00+00:00"}, verdict{denySquid: true, denyTentacle: true}),
		Entry("DateLessThan a date at a UTC offset",
			"DateLessThan", "S3:ExistingObjectTag/When", []string{"2030-01-01T00:00:00+00:00"},
			[]string{"s3:ExistingObjectTag/when", "2024-06-01T00:00:00Z"}, verdict{denySquid: true, denyTentacle: true}),
	)

	It("is radosgw's exact key for Pass and for an Effect no statement names", func() {
		c := cond(policy.OpStringLike, "S3:Prefix", "home/*")
		env := envOf("s3:prefix", "home/a")
		for _, sem := range []policy.Semantics{squid, tentacle} {
			Expect(c.Eval(env, sem)).To(BeFalse())
			Expect(c.EvalFor(env, sem, policy.Pass)).To(BeFalse())
			Expect(c.EvalFor(env, sem, policy.Effect(200))).To(BeFalse())
			Expect(c.EvalFor(env, sem, policy.Deny)).To(BeTrue())
		}
	})

	Describe("a policy", func() {
		const deny = `{"Version": "2012-10-17", "Statement": [
			{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"},
			{"Effect": "Deny", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*",
			 "Condition": {"NotIpAddress": {"AWS:SOURCEIP": "192.168.1.0/24"}}}]}`
		const allow = `{"Version": "2012-10-17", "Statement": [
			{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*",
			 "Condition": {"IpAddress": {"AWS:SOURCEIP": "192.168.1.0/24"}}}]}`
		// The s3-tests test_bucket_policy_deny_stringlike_key_other_case and
		// test_bucket_policy_deny_stringnotlike_key_other_case set these.
		listDeny := func(op, patterns string) string {
			return `{"Version": "2012-10-17", "Statement": [
				{"Effect": "Allow", "Principal": "*", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::b"},
				{"Effect": "Deny", "Principal": "*", "Action": "s3:ListBucket", "Resource": "arn:aws:s3:::b",
				 "Condition": {"` + op + `": {"S3:Prefix": ` + patterns + `}}}]}`
		}

		for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
			Describe("on "+rel.String(), func() {
				var obj policy.ARN
				var sem policy.Semantics

				BeforeEach(func() {
					obj = s3ARN("b/k")
					sem = policy.SemanticsFor(rel)
				})

				It("keeps the key as the policy writes it", func() {
					p := mustParse(deny, parseOpts(rel))
					Expect(p.Statements[1].Conditions[0].Key).To(Equal("AWS:SOURCEIP"))
				})

				It("refuses a request from outside the network a Deny names in another case than the request's key", func() {
					p := mustParse(deny, parseOpts(rel))
					Expect(p.Eval(envOf("aws:SourceIp", "10.0.0.1"), nil, policy.S3GetObject, &obj, sem)).To(Equal(policy.Deny))
					Expect(p.Eval(envOf("aws:SourceIp", "192.168.1.5"), nil, policy.S3GetObject, &obj, sem)).To(Equal(policy.Allow))
				})

				It("refuses a listing whose prefix a Deny's StringLike on S3:Prefix matches, and serves another", func() {
					p := mustParse(listDeny("StringLike", `["private/*", "home/[a]/*", "zz?"]`), parseOpts(rel))
					listed := s3ARN("b")
					Expect(p.Eval(envOf("s3:prefix", "private/"), nil, policy.S3ListBucket, &listed, sem)).To(Equal(policy.Deny))
					Expect(p.Eval(envOf("s3:prefix", "home/[a]/\U0001F600"), nil, policy.S3ListBucket, &listed, sem)).To(Equal(policy.Deny))
					Expect(p.Eval(envOf("s3:prefix", "public/"), nil, policy.S3ListBucket, &listed, sem)).To(Equal(policy.Allow))
				})

				It("serves a listing whose prefix a Deny's StringNotLike on S3:Prefix matches, and refuses another", func() {
					p := mustParse(listDeny("StringNotLike", `["home/*", "zz?"]`), parseOpts(rel))
					listed := s3ARN("b")
					Expect(p.Eval(envOf("s3:prefix", "home/"), nil, policy.S3ListBucket, &listed, sem)).To(Equal(policy.Allow))
					Expect(p.Eval(envOf("s3:prefix", "home/\U0001F600"), nil, policy.S3ListBucket, &listed, sem)).To(Equal(policy.Allow))
					Expect(p.Eval(envOf("s3:prefix", "other/"), nil, policy.S3ListBucket, &listed, sem)).To(Equal(policy.Deny))
				})

				It("grants nothing under an Allow that names the key in another case than the request's, as radosgw does", func() {
					p := mustParse(allow, parseOpts(rel))
					Expect(p.Eval(envOf("aws:SourceIp", "192.168.1.5"), nil, policy.S3GetObject, &obj, sem)).To(Equal(policy.Pass))
					Expect(p.Eval(envOf("aws:SourceIp", "10.0.0.1"), nil, policy.S3GetObject, &obj, sem)).To(Equal(policy.Pass))
				})
			})
		}
	})
})
