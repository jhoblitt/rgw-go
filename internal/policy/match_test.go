package policy_test

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("MatchWildcards", func() {
	expectMatch := func(pattern, input string, want, wantFolded bool) {
		Expect(policy.MatchWildcards(pattern, input, false)).To(Equal(want), "%q against %q", pattern, input)
		Expect(policy.MatchWildcards(pattern, input, true)).To(Equal(wantFolded), "%q against %q, folding case", pattern, input)
	}

	DescribeTable("matches as radosgw's own tests expect",
		expectMatch,
		// TEST(MatchWildcards, Simple), src/test/rgw/test_rgw_iam_policy.cc:1369-1387 at v19.2.6.
		Entry("empty against empty", "", "", true, true),
		Entry("empty against text", "", "abc", false, false),
		Entry("text against empty", "abc", "", false, false),
		Entry("equal text", "abc", "abc", true, true),
		Entry("text against its uppercase", "abc", "abC", false, true),
		Entry("uppercase against its lowercase", "abC", "abc", false, true),
		Entry("a prefix of the input", "abc", "abcd", false, false),
		Entry("longer than the input", "abcd", "abc", false, false),
		// TEST(MatchWildcards, QuestionMark), :1389-1411.
		Entry("? against empty", "?", "", false, false),
		Entry("? against one byte", "?", "a", true, true),
		Entry("? leading", "?bc", "abc", true, true),
		Entry("? inside", "a?c", "abc", true, true),
		Entry("? in the input is literal", "abc", "a?c", false, false),
		Entry("? beside a case difference", "a?c", "abC", false, true),
		Entry("? trailing", "ab?", "abc", true, true),
		Entry("two ?", "a?c?e", "abcde", true, true),
		Entry("??? against three bytes", "???", "abc", true, true),
		Entry("??? against four bytes", "???", "abcd", false, false),
		// TEST(MatchWildcards, Asterisk), :1413-1447.
		Entry("* against empty", "*", "", true, true),
		Entry("* in the input is literal", "", "*", false, false),
		Entry("*a against empty", "*a", "", false, false),
		Entry("*a against a", "*a", "a", true, true),
		Entry("a* against a", "a*", "a", true, true),
		Entry("* matching nothing", "a*c", "ac", true, true),
		Entry("* matching a run", "a*c", "abbc", true, true),
		Entry("* beside a case difference", "a*c", "abbC", false, true),
		Entry("two *", "a*c*e", "abBce", true, true),
		Entry("* in a host name", "http://*.example.com", "http://www.example.com", true, true),
		Entry("* in a host name that differs in case", "http://*.example.com", "http://www.Example.com", false, true),
		Entry("* in a path", "http://example.com/*", "http://example.com/index.html", true, true),
		Entry("* in two path segments", "http://example.com/*/*.jpg", "http://example.com/fun/smiley.jpg", true, true),
		Entry("* backing off the last byte", "a*c", "abcc", true, true),
	)

	// Every expectation here is what fnmatch(3) answers in the v19.2.6 and
	// v20.2.4 container images, whose glibc is el9's 2.34.
	DescribeTable("follows fnmatch(3) as radosgw's glibc implements it",
		expectMatch,
		Entry("a class", "[ab]c", "bc", true, true),
		Entry("a class negated with !", "[!a]c", "ac", false, false),
		Entry("a class negated with !, another byte", "[!a]c", "bc", true, true),
		Entry("a class negated with ^", "[^a]c", "ac", false, false),
		Entry("a range", "[a-c]", "b", true, true),
		Entry("outside a range", "[a-c]", "d", false, false),
		Entry("an escaped *", `\*`, "*", true, true),
		Entry("an escaped * is no wildcard", `\*`, "a", false, false),
		Entry("* spans /, without FNM_PATHNAME", "a*", "a/b/c", true, true),
		Entry("? matches /, without FNM_PATHNAME", "a?c", "a/c", true, true),
		Entry("* matches a leading period, without FNM_PERIOD", "*", ".hidden", true, true),
		Entry("a range's ends fold with the input", "[A-Z]", "q", false, true),
		Entry("a named class ignores folding", "[[:upper:]]", "a", false, false),
		Entry("an equivalence class ignores folding", "[[=a=]]", "A", false, false),
		Entry("folding is the C locale's, ASCII only", "\xc3\x89", "\xc3\xa9", false, false),
		Entry("] first in a class is a member", "[]a]", "]", true, true),
		Entry("] first in a negated class is a member", "[!]a]", "]", false, false),
		Entry("- last in a class is a member", "[a-]", "-", true, true),
		Entry("an escaped range end", `[a-\z]`, "m", true, true),
		Entry("a range over bytes above 0x7f", "[\x80-\xff]", "\xc3", true, true),
		Entry("an inverted range is empty", "[z-a]", "m", false, false),
		Entry("an unterminated class is a literal [", "[a", "[a", true, true),
		Entry("an unterminated class whose member matched fails", "[[", "[[", false, false),
		Entry("an unterminated range never matches", "[a-", "[a-", false, false),
		Entry("a named class", "[[:alpha:]]", "q", true, true),
		Entry("a named class knows only ASCII, as the C locale does", "[[:alpha:]]", "\xc3", false, false),
		Entry("an unknown class name fails the whole match", "[![:foo:]]", "a", false, false),
		Entry("an unclosed class name is plain members", "[[:alpha]", ":", true, true),
		Entry("a collating symbol", "[[.a.]]", "a", true, true),
		Entry("a trailing backslash never matches", `a\`, `a\`, false, false),
		Entry("an escaped backslash", `\\`, `\`, true, true),
		Entry("? after * needs a byte", "*?", "", false, false),
		Entry("* then an unterminated class", "*[a", "x[a", true, true),
		Entry("the pattern ends at NUL, as a C string does", "abc\x00def", "abc", true, true),
		Entry("the input ends at NUL, as a C string does", "abc", "abc\x00xyz", true, true),
	)
})

var _ = Describe("MatchPolicy", func() {
	expectMatch := func(pattern, input string, wantAction, wantARN bool) {
		Expect(policy.MatchPolicy(pattern, input, true)).To(Equal(wantAction), "action %q against %q", pattern, input)
		Expect(policy.MatchPolicy(pattern, input, false)).To(Equal(wantARN), "ARN %q against %q", pattern, input)
	}

	DescribeTable("matches as radosgw's own tests expect",
		expectMatch,
		// TEST(MatchPolicy, Action) and TEST(MatchPolicy, ARN),
		// src/test/rgw/test_rgw_iam_policy.cc:1449-1465 at v19.2.6.
		Entry("equal components", "a:b:c", "a:b:c", true, true),
		Entry("components that differ in case", "a:b:c", "A:B:C", true, false),
		Entry("* within a component", "a:*:e", "a:bcd:e", true, true),
		Entry("* cannot span components", "a:*", "a:b:c", false, false),
	)

	DescribeTable("matches each colon-separated component with MatchWildcards",
		expectMatch,
		Entry("an action wildcard", "s3:Get*", "s3:GetObject", true, true),
		Entry("an action name in another case", "S3:getobject", "s3:GetObject", true, false),
		Entry("s3:* is not another service's wildcard", "s3:*", "s3-object-lambda:GetObject", false, false),
		Entry("a resource wildcard", "arn:aws:s3:::b/*", "arn:aws:s3:::b/k", true, true),
		Entry("a resource in another case", "arn:aws:s3:::B/*", "arn:aws:s3:::b/k", true, false),
		Entry("fewer components than the input", "a:b", "a:b:c", false, false),
		Entry("more components than the input", "a:b:c:d", "a:b:c", false, false),
		Entry("a pattern without a colon against an action", "*", "s3:GetObject", false, false),
		Entry("a wildcard region", "arn:aws:sns:*:123456789012:topic", "arn:aws:sns:us-east-1:123456789012:topic", true, true),
		Entry("a wildcard account", "arn:aws:sns:us-east-1:*:topic", "arn:aws:sns:us-east-1:123456789012:topic", true, true),
		Entry("wildcards matching empty components", "arn:aws:s3:*:*:bucket", "arn:aws:s3:::bucket", true, true),
		Entry("a wildcard inside the resource", "arn:aws:s3:::bucket/*/x", "arn:aws:s3:::bucket/abc/x", true, true),
		Entry("a wildcard component beside one that differs",
			"arn:aws:sns:*:123456789012:topic", "arn:aws:sns:us-east-1:123456789012:other", false, false),
		Entry("a backslash cannot escape the separator", `x:a\:y`, "x:a:y", false, false),
		Entry("a NUL ends only its own component", "a\x00:b", "a:b", true, true),
	)
})

var _ = Describe("MatchAction", func() {
	It("sets the actions of the release whose names match", func() {
		var squid, tentacle policy.ActionSet
		Expect(policy.MatchAction(&squid, "s3:Get*", denc.Squid)).To(BeTrue())
		Expect(policy.MatchAction(&tentacle, "s3:Get*", denc.Tentacle)).To(BeTrue())
		Expect(squid.Has(policy.S3GetObject)).To(BeTrue(), "s3:GetObject on squid")
		Expect(squid.Has(policy.S3GetObjectAttributes)).To(BeFalse(), "s3:GetObjectAttributes on squid")
		Expect(tentacle.Has(policy.S3GetObject)).To(BeTrue(), "s3:GetObject on tentacle")
		Expect(tentacle.Has(policy.S3GetObjectAttributes)).To(BeTrue(), "s3:GetObjectAttributes on tentacle")
		Expect(tentacle.Has(policy.S3GetAccountPublicAccessBlock)).To(BeFalse(), "s3:GetAccountPublicAccessBlock")
		Expect(squid.Has(policy.S3All)).To(BeFalse(), "s3:* after s3:Get*")
	})
	It("matches names in any case", func() {
		var s policy.ActionSet
		Expect(policy.MatchAction(&s, "S3:getobject", denc.Squid)).To(BeTrue())
		Expect(namesIn(s)).To(ConsistOf("s3:GetObject"))
	})
	It("matches the name v20.2.4 misspells, and only there", func() {
		var squid, tentacle policy.ActionSet
		Expect(policy.MatchAction(&squid, "iam:RemoveCientIdFromOIDCProvider", denc.Squid)).To(BeFalse())
		Expect(policy.MatchAction(&tentacle, "iam:RemoveCientIdFromOIDCProvider", denc.Tentacle)).To(BeTrue())
		Expect(namesIn(tentacle)).To(ConsistOf("iam:RemoveCientIdFromOIDCProvider"))
	})
	DescribeTable("sets a service's All bit once every action of it the release knows is set",
		func(pattern string, r denc.Release, value policy.ActionSet, all policy.Action) {
			unknown := mainOnly
			if r == denc.Squid {
				unknown = slices.Concat(tentacleOnly, mainOnly)
			}
			var want policy.ActionSet
			for a := range policy.ActionCount {
				if value.Has(a) && !slices.Contains(unknown, a) {
					want.Set(a)
				}
			}
			want.Set(all)

			var got policy.ActionSet
			Expect(policy.MatchAction(&got, pattern, r)).To(BeTrue())
			Expect(namesIn(got)).To(ConsistOf(namesIn(want)))
		},
		Entry("s3:* on squid", "s3:*", denc.Squid, policy.S3AllValue(), policy.S3All),
		Entry("s3:* on tentacle", "s3:*", denc.Tentacle, policy.S3AllValue(), policy.S3All),
		Entry("iam:* on squid", "iam:*", denc.Squid, policy.IAMAllValue(), policy.IAMAll),
		Entry("iam:* on tentacle", "iam:*", denc.Tentacle, policy.IAMAllValue(), policy.IAMAll),
		Entry("s3-object-lambda:*", "s3-object-lambda:*", denc.Tentacle, policy.S3ObjectLambdaAllValue(), policy.S3ObjectLambdaAll),
		Entry("sts:*", "sts:*", denc.Squid, policy.STSAllValue(), policy.STSAll),
		Entry("sns:*", "sns:*", denc.Tentacle, policy.SNSAllValue(), policy.SNSAll),
		Entry("organizations:*", "organizations:*", denc.Squid, policy.OrganizationsAllValue(), policy.OrganizationsAll),
	)
	It("sets a service's All bit only once the patterns matched so far cover the service", func() {
		// The IAMFullAccess managed policy names the ten organizations
		// actions one by one, and radosgw's test of it expects organizationsAll
		// (src/test/rgw/test_rgw_iam_policy.cc:792-801 at v19.2.6).
		names := []string{
			"organizations:DescribeAccount",
			"organizations:DescribeOrganization",
			"organizations:DescribeOrganizationalUnit",
			"organizations:DescribePolicy",
			"organizations:ListChildren",
			"organizations:ListParents",
			"organizations:ListPoliciesForTarget",
			"organizations:ListRoots",
			"organizations:ListPolicies",
			"organizations:ListTargetsForPolicy",
		}
		var s policy.ActionSet
		for _, name := range names[:len(names)-1] {
			Expect(policy.MatchAction(&s, name, denc.Squid)).To(BeTrue(), name)
		}
		Expect(s.Has(policy.OrganizationsAll)).To(BeFalse(), "after nine of the ten")
		Expect(policy.MatchAction(&s, names[len(names)-1], denc.Squid)).To(BeTrue(), names[len(names)-1])
		Expect(s.Has(policy.OrganizationsAll)).To(BeTrue(), "after all ten")
	})
	DescribeTable("reports a pattern that names no action of the release",
		func(pattern string, r denc.Release) {
			var s policy.ActionSet
			Expect(policy.MatchAction(&s, pattern, r)).To(BeFalse())
			Expect(namesIn(s)).To(BeEmpty())
		},
		Entry("an unknown name", "s3:Nope", denc.Tentacle),
		Entry("an action v20.2.4 added, on squid", "s3:GetObjectAttributes", denc.Squid),
		Entry("an action only ceph main has", "s3:GetAccountPublicAccessBlock", denc.Tentacle),
		Entry("the correct spelling of the name v20.2.4 misspells", "iam:RemoveClientIdFromOIDCProvider", denc.Tentacle),
		Entry("*, which the parser turns into AllValue before any match", "*", denc.Tentacle),
	)
})
