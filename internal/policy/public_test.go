package policy_test

import (
	"log/slog"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The rules and examples are Amazon S3's, "The meaning of "public""
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-control-block-public-access.html#access-control-block-public-access-policy-status).
// A document is parsed under each release, and both answers are held to the
// same expectation.

// grantToAll is a policy of one statement granting every principal
// s3:GetObject in the bucket b under condition, a Condition's JSON or "".
func grantToAll(condition string) string {
	s := `{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"`
	if condition != "" {
		s += `, "Condition": ` + condition
	}
	return `{"Version": "2012-10-17", "Statement": [` + s + `}]}`
}

// statements is a 2012-10-17 policy of the statements given, each a JSON
// object.
func statements(stmts ...string) string {
	return `{"Version": "2012-10-17", "Statement": [` + strings.Join(stmts, ", ") + `]}`
}

var _ = Describe("Policy.IsPublic", func() {
	BeforeEach(func() {
		// Squid's parse logs the Service principal it drops.
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.DiscardHandler))
		DeferCleanup(func() { slog.SetDefault(prev) })
	})

	parsed := func(r denc.Release, doc string) *policy.Policy {
		GinkgoHelper()
		p, err := policy.Parse(doc, policy.ParseOptions{Release: r})
		Expect(err).NotTo(HaveOccurred(), "%v: %s", r, doc)
		return p
	}
	expectPublic := func(doc string, want bool) {
		GinkgoHelper()
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			Expect(parsed(r, doc).IsPublic()).To(Equal(want), "%v: %s", r, doc)
		}
	}

	DescribeTable("judges Amazon S3's examples as it does, on both releases",
		expectPublic,
		Entry("a grant to every principal",
			statements(`{"Principal": "*", "Resource": "*", "Action": "s3:PutObject", "Effect": "Allow"}`), true),
		Entry("that grant under a wildcard aws:SourceVpc",
			statements(`{"Principal": "*", "Resource": "*", "Action": "s3:PutObject", "Effect": "Allow",
				"Condition": {"StringLike": {"aws:SourceVpc": "vpc-*"}}}`), true),
		Entry("that grant under a fixed aws:SourceVpc",
			statements(`{"Principal": "*", "Resource": "*", "Action": "s3:PutObject", "Effect": "Allow",
				"Condition": {"StringEquals": {"aws:SourceVpc": "vpc-91237329"}}}`), false),
		Entry("grants to a service, to another account and to the public",
			statements(
				`{"Effect": "Allow", "Principal": {"Service": "cloudtrail.amazonaws.com"}, "Action": "s3:PutObject", "Resource": "arn:aws:s3:::b/*"}`,
				`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::123456789012:root"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`,
				`{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`,
			), true),
		Entry("grants to a service and to another account alone",
			statements(
				`{"Effect": "Allow", "Principal": {"Service": "cloudtrail.amazonaws.com"}, "Action": "s3:PutObject", "Resource": "arn:aws:s3:::b/*"}`,
				`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::123456789012:root"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`,
			), false),
	)

	DescribeTable("keeps a grant to every principal public unless a condition limits it to fixed values of a key Amazon S3 lists",
		func(condition string, want bool) { expectPublic(grantToAll(condition), want) },
		Entry("aws:SourceIp, a /24", `{"IpAddress": {"aws:SourceIp": "203.0.113.0/24"}}`, false),
		// docs/ceph-upstream-bugs.md, "radosgw's is_public judges a
		// wildcard-principal statement against a fixed three-key environment".
		Entry("aws:SourceIp, the one address radosgw's is_public environment holds",
			`{"IpAddress": {"aws:SourceIp": "1.1.1.1/32"}}`, false),
		Entry("aws:SourceIp, a /8, the broadest RFC 1918 block", `{"IpAddress": {"aws:SourceIp": "10.0.0.0/8"}}`, false),
		Entry("aws:SourceIp, the RFC 1918 block 172.16.0.0/12", `{"IpAddress": {"aws:SourceIp": "172.16.0.0/12"}}`, false),
		Entry("aws:SourceIp, a /7, broader than /8 although it holds an RFC 1918 block",
			`{"IpAddress": {"aws:SourceIp": "10.0.0.0/7"}}`, true),
		Entry("aws:SourceIp, an empty prefix, which radosgw reads as /0",
			`{"IpAddress": {"aws:SourceIp": "203.0.113.0/"}}`, true),
		Entry("aws:SourceIp, Amazon S3's broad 0.0.0.0/1", `{"IpAddress": {"aws:SourceIp": "0.0.0.0/1"}}`, true),
		Entry("aws:SourceIp, a /24 and a broad range",
			`{"IpAddress": {"aws:SourceIp": ["203.0.113.0/24", "0.0.0.0/1"]}}`, true),
		Entry("aws:SourceIp, an IPv6 /32", `{"IpAddress": {"aws:SourceIp": "2001:db8::/32"}}`, false),
		Entry("aws:SourceIp, an IPv6 /31", `{"IpAddress": {"aws:SourceIp": "2001:db8::/31"}}`, true),
		Entry("aws:SourceIp, ::/96, which holds every IPv4 address as radosgw compares them",
			`{"IpAddress": {"aws:SourceIp": "::/96"}}`, true),
		Entry("aws:SourceIp, the IPv4-mapped block", `{"IpAddress": {"aws:SourceIp": "::ffff:0:0/96"}}`, true),
		Entry("aws:SourceIp, a /24 of the IPv4-mapped block",
			`{"IpAddress": {"aws:SourceIp": "::ffff:203.0.113.0/120"}}`, false),
		Entry("aws:SourceIp, a value that is no address", `{"IpAddress": {"aws:SourceIp": "x"}}`, true),
		Entry("aws:SourceIp under NotIpAddress", `{"NotIpAddress": {"aws:SourceIp": "203.0.113.0/24"}}`, true),
		Entry("aws:SourceIp under IfExists", `{"IpAddressIfExists": {"aws:SourceIp": "203.0.113.0/24"}}`, true),
		Entry("aws:SourceIp under StringEquals", `{"StringEquals": {"aws:SourceIp": "203.0.113.7"}}`, true),
		Entry("aws:SourceVpc under IpAddress", `{"IpAddress": {"aws:SourceVpc": "203.0.113.0/24"}}`, true),
		Entry("aws:SourceVpc under StringLike without a wildcard",
			`{"StringLike": {"aws:SourceVpc": "vpc-91237329"}}`, false),
		Entry("aws:SourceVpc under StringEqualsIgnoreCase",
			`{"StringEqualsIgnoreCase": {"aws:SourceVpc": "VPC-91237329"}}`, false),
		Entry("aws:SourceVpc whose value holds a '*' under StringEquals",
			`{"StringEquals": {"aws:SourceVpc": "vpc-*"}}`, true),
		Entry("aws:SourceVpc with a bracket class under StringLike",
			`{"StringLike": {"aws:SourceVpc": "vpc-[0-9a-f][0-9a-f]"}}`, true),
		Entry("aws:SourceVpc with a backslash escape under StringLike",
			`{"StringLike": {"aws:SourceVpc": "vpc-\\91237329"}}`, true),
		Entry("aws:SourceArn with a bracket class under ArnEquals",
			`{"ArnEquals": {"aws:SourceArn": "arn:aws:s3:::[!x]"}}`, true),
		Entry("aws:SourceVpc under StringNotEquals", `{"StringNotEquals": {"aws:SourceVpc": "vpc-91237329"}}`, true),
		Entry("aws:SourceVpc under StringEqualsIfExists", `{"StringEqualsIfExists": {"aws:SourceVpc": "vpc-91237329"}}`, true),
		Entry("aws:SourceAccount under NumericEquals", `{"NumericEquals": {"aws:SourceAccount": "123456789012"}}`, true),
		Entry("aws:SourceVpce under StringEquals with several fixed values",
			`{"StringEquals": {"aws:SourceVpce": ["vpce-1a2b3c4d", "vpce-5e6f7a8b"]}}`, false),
		Entry("aws:SourceVpc with no value", `{"StringEquals": {"aws:SourceVpc": []}}`, true),
		Entry("an empty Condition", `{}`, true),
		Entry("an operator naming no key", `{"StringEquals": {}}`, true),
		Entry("aws:SourceVpc tested by Null", `{"Null": {"aws:SourceVpc": "false"}}`, true),
		Entry("aws:SourceVpc spelled in another case", `{"StringEquals": {"AWS:SOURCEVPC": "vpc-91237329"}}`, false),
		Entry("aws:SourceVpce", `{"StringEquals": {"aws:SourceVpce": "vpce-1a2b3c4d"}}`, false),
		Entry("aws:SourceArn", `{"ArnEquals": {"aws:SourceArn": "arn:aws:s3:::source-bucket"}}`, false),
		Entry("aws:SourceArn with a wildcard", `{"ArnLike": {"aws:SourceArn": "arn:aws:s3:::source-*"}}`, true),
		Entry("aws:SourceArn under ArnNotEquals", `{"ArnNotEquals": {"aws:SourceArn": "arn:aws:s3:::source-bucket"}}`, true),
		Entry("aws:SourceOwner", `{"StringEquals": {"aws:SourceOwner": "123456789012"}}`, false),
		Entry("aws:SourceAccount", `{"StringEquals": {"aws:SourceAccount": "123456789012"}}`, false),
		Entry("aws:SourceAccount with a '?'", `{"StringLike": {"aws:SourceAccount": "12345678901?"}}`, true),
		Entry("aws:userid", `{"StringEquals": {"aws:userid": "AIDAEXAMPLEUSERID"}}`, false),
		Entry("aws:userid naming one role session", `{"StringEquals": {"aws:userid": "AROAEXAMPLEROLEID:session"}}`, false),
		Entry("aws:userid in the pattern AROLEID:*", `{"StringLike": {"aws:userid": "AROAEXAMPLEROLEID:*"}}`, true),
		Entry("aws:PrincipalOrgID", `{"StringEquals": {"aws:PrincipalOrgID": "o-a1b2c3d4e5"}}`, false),
		Entry("s3:DataAccessPointArn naming every access point of one account",
			`{"StringLike": {"s3:DataAccessPointArn": "arn:aws:s3:us-west-2:123456789012:accesspoint/*"}}`, false),
		Entry("s3:DataAccessPointArn with a wildcard account",
			`{"ArnLike": {"s3:DataAccessPointArn": "arn:aws:s3:us-west-2:*:accesspoint/*"}}`, true),
		Entry("s3:DataAccessPointArn naming no account",
			`{"StringLike": {"s3:DataAccessPointArn": "arn:aws:s3:us-west-2::accesspoint/*"}}`, true),
		Entry("s3:DataAccessPointArn whose account holds a bracket class",
			`{"StringLike": {"s3:DataAccessPointArn": "arn:aws:s3:us-west-2:[0-9]23456789012:accesspoint/*"}}`, true),
		Entry("s3:DataAccessPointArn of six parts before the access point name",
			`{"StringLike": {"s3:DataAccessPointArn": "arn:aws:s3:us-west-2:123456789012:x:accesspoint/*"}}`, true),
		Entry("s3:DataAccessPointArn not starting arn",
			`{"StringLike": {"s3:DataAccessPointArn": "nra:aws:s3:us-west-2:123456789012:accesspoint/*"}}`, true),
		Entry("s3:DataAccessPointArn whose access point name holds a policy variable",
			`{"StringLike": {"s3:DataAccessPointArn": "arn:aws:s3:us-west-2:123456789012:accesspoint/${aws:username}"}}`, true),
		Entry("s3:DataAccessPointArn with a wildcard outside the access point name",
			`{"ArnLike": {"s3:DataAccessPointArn": "arn:aws:s3:us-west-2:123456789012:*"}}`, true),
		Entry("s3:DataAccessPointAccount", `{"StringEquals": {"s3:DataAccessPointAccount": "123456789012"}}`, false),
		Entry("a multivalued key under ForAnyValue",
			`{"ForAnyValue:StringEquals": {"aws:SourceVpce": ["vpce-1a2b3c4d", "vpce-5e6f7a8b"]}}`, false),
		Entry("a multivalued key under ForAllValues, which holds without the key",
			`{"ForAllValues:StringEquals": {"aws:SourceVpce": ["vpce-1a2b3c4d", "vpce-5e6f7a8b"]}}`, true),
		Entry("a policy variable", `{"StringEquals": {"aws:SourceVpc": "${aws:username}"}}`, true),
		Entry("a policy variable inside a value", `{"ArnEquals": {"aws:SourceArn": "arn:aws:s3:::${aws:username}"}}`, true),
		// docs/ceph-upstream-bugs.md, "radosgw's is_public judges a
		// wildcard-principal statement against a fixed three-key environment".
		Entry("a key Amazon S3 does not list, aws:SecureTransport", `{"Bool": {"aws:SecureTransport": "true"}}`, true),
		Entry("a key naming the user that Amazon S3 does not list, aws:username",
			`{"StringEquals": {"aws:username": "alice"}}`, true),
		Entry("an unlisted key beside a limiting one",
			`{"Bool": {"aws:SecureTransport": "true"}, "IpAddress": {"aws:SourceIp": "203.0.113.0/24"}}`, false),
	)

	DescribeTable("judges whom a statement grants, on both releases",
		expectPublic,
		Entry("every principal as an AWS principal",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "*"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("every principal beside an account",
			statements(`{"Effect": "Allow", "Principal": {"AWS": ["arn:aws:iam::123456789012:root", "*"]},
				"Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		// docs/ceph-upstream-bugs.md, "Squid's is_public counts every Allow
		// statement whose NotPrincipal is not the wildcard".
		Entry("one user",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/alice"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("one role",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::123456789012:role/r"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("one assumed role session",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:sts::123456789012:assumed-role/r/s"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("one OIDC provider",
			statements(`{"Effect": "Allow", "Principal": {"Federated": "arn:aws:iam::123456789012:oidc-provider/example.com"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("another tenant's account",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::t1:root"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("an account named anonymous, which holds no anonymous requester",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "anonymous"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		// docs/ceph-upstream-bugs.md, "Tentacle's is_public misses a fixed
		// principal that admits the anonymous requester".
		Entry("the default tenant's account, which holds the anonymous requester",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::root"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("the default tenant's account, named bare",
			statements(`{"Effect": "Allow", "Principal": {"AWS": ""}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("the anonymous user",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/anonymous"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("a subuser of the anonymous user, which the anonymous requester is not",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/anonymous:x"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("the anonymous user in another tenant",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::t1:user/anonymous"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("a user whose name holds a wildcard, which is no fixed value",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/*"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("every subuser of one user, whose name holds a wildcard",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/alice:*"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("an account whose name holds a wildcard",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "acct*"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("an OIDC provider whose URL holds a wildcard",
			statements(`{"Effect": "Allow", "Principal": {"Federated": "arn:aws:iam:::oidc-provider/*"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("a user whose name holds a policy variable",
			statements(`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/${aws:username}"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		// docs/ceph-upstream-bugs.md, "radosgw takes any policy Action starting
		// with a wildcard for every action", which Principal shares.
		Entry("every principal, as a Principal string starting with '*'",
			statements(`{"Effect": "Allow", "Principal": "*x", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("no one, naming neither Principal nor NotPrincipal",
			statements(`{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("every principal, but as a Deny",
			statements(`{"Effect": "Deny", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		Entry("every principal, which a Deny under a condition narrows",
			statements(
				`{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`,
				`{"Effect": "Deny", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*",
					"Condition": {"NotIpAddress": {"aws:SourceIp": "203.0.113.0/24"}}}`,
			), true),
		Entry("every principal, beside a Deny of every action to every principal",
			statements(
				`{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`,
				`{"Effect": "Deny", "Principal": "*", "Action": "*", "Resource": "*"}`,
			), true),
		Entry("every principal, naming no Effect, which radosgw takes for Deny",
			statements(`{"Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`), false),
		// docs/exclusions.md, "BitTorrent files are not served".
		Entry("every principal an action rgw-go does not serve",
			statements(`{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObjectTorrent", "Resource": "arn:aws:s3:::b/*"}`), true),
		Entry("every principal every action but one, in another bucket",
			statements(`{"Effect": "Allow", "Principal": "*", "NotAction": "s3:GetObject", "Resource": "arn:aws:s3:::other/*"}`), true),
	)

	// radosgw's principal set keeps one OIDC provider and one service per
	// statement (docs/ceph-upstream-bugs.md, "radosgw drops all but the first
	// OIDC provider or Service principal in a statement"); IsPublic judges
	// every principal listed, whichever the set keeps.
	DescribeTable("judges every principal a statement lists, whatever radosgw's set drops",
		func(principal string, wantSquid, wantTentacle bool) {
			GinkgoHelper()
			doc := statements(`{"Effect": "Allow", "Principal": ` + principal + `, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`)
			Expect(parsed(denc.Squid, doc).IsPublic()).To(Equal(wantSquid), "squid: %s", doc)
			Expect(parsed(denc.Tentacle, doc).IsPublic()).To(Equal(wantTentacle), "tentacle: %s", doc)
		},
		Entry("an OIDC provider with a wildcard after a fixed one",
			`{"Federated": ["arn:aws:iam:::oidc-provider/a", "arn:aws:iam:::oidc-provider/*"]}`, true, true),
		Entry("an OIDC provider with a wildcard before a fixed one",
			`{"Federated": ["arn:aws:iam:::oidc-provider/*", "arn:aws:iam:::oidc-provider/a"]}`, true, true),
		Entry("two fixed OIDC providers",
			`{"Federated": ["arn:aws:iam:::oidc-provider/a", "arn:aws:iam:::oidc-provider/b"]}`, false, false),
		// Squid's parse drops a Service principal, which leaves the
		// statement no principal.
		Entry("the service wildcard after a fixed service",
			`{"Service": ["s3.amazonaws.com", "*"]}`, false, true),
		Entry("the service wildcard before a fixed service",
			`{"Service": ["*", "s3.amazonaws.com"]}`, false, true),
		Entry("two fixed services",
			`{"Service": ["s3.amazonaws.com", "cloudtrail.amazonaws.com"]}`, false, false),
	)

	DescribeTable("judges an Allow with NotPrincipal, which only Squid parses",
		func(stmt string, want bool) {
			doc := statements(stmt)
			_, err := policy.Parse(doc, policy.ParseOptions{Release: denc.Tentacle})
			Expect(err).To(HaveOccurred(), "Tentacle's parse of %s", doc)
			Expect(parsed(denc.Squid, doc).IsPublic()).To(Equal(want), "squid: %s", doc)
		},
		Entry("as a grant to every principal but those it names",
			`{"Effect": "Allow", "NotPrincipal": {"AWS": "arn:aws:iam:::user/alice"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`, true),
		Entry("limited as a grant to every principal is",
			`{"Effect": "Allow", "NotPrincipal": {"AWS": "arn:aws:iam:::user/alice"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*",
				"Condition": {"IpAddress": {"aws:SourceIp": "203.0.113.0/24"}}}`, false),
		Entry("as a grant to no one when it names every principal",
			`{"Effect": "Allow", "NotPrincipal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`, false),
		// radosgw's parser refuses one principal type under both keys
		// (docs/ceph-upstream-bugs.md, "radosgw's policy parser forgets a
		// statement's keys when an object in an array closes").
		Entry("as a grant to those Principal names when it names some",
			`{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/bob"},
				"NotPrincipal": {"Federated": "arn:aws:iam:::oidc-provider/example.com"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`, false),
		Entry("as a grant to every principal when Principal names every principal",
			`{"Effect": "Allow", "Principal": "*",
				"NotPrincipal": {"AWS": "arn:aws:iam:::user/alice"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::b/*"}`, true),
	)

	DescribeTable("never judges a condition block or operator the parse refuses",
		func(condition, refusal string) {
			for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
				_, err := policy.Parse(grantToAll(condition), policy.ParseOptions{Release: r})
				Expect(err).To(MatchError(ContainSubstring(refusal)), "%v", r)
			}
		},
		Entry("a Condition that is a string", `"x"`, "`x` is not valid in the context of `Condition`."),
		Entry("an operator whose value is a string", `{"StringEquals": "x"}`, "`x` is not valid in the context of `StringEquals`."),
		Entry("an unknown operator", `{"StringEqualz": {"aws:SourceVpc": "vpc-91237329"}}`, "Unknown key `StringEqualz`."),
	)

	It("lets no operator outside the policy language limit a grant", func() {
		p := policy.Policy{Statements: []policy.Statement{{
			Effect:     policy.Allow,
			Principals: []policy.Principal{policy.WildcardPrincipal()},
			Conditions: []policy.Condition{{Op: policy.Operator(255), Key: "aws:SourceVpc", Values: []string{"vpc-91237329"}}},
		}}}
		Expect(p.IsPublic()).To(BeTrue(), "a grant to everyone under operator 255 on aws:SourceVpc")
	})

	It("never judges Amazon S3's unsupported action, which the parse refuses", func() {
		doc := statements(`{"Effect": "Allow", "Principal": "*", "Action": "s3:NotASupportedAction", "Resource": "arn:aws:s3:::b/*"}`)
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			_, err := policy.Parse(doc, policy.ParseOptions{Release: r})
			Expect(err).To(MatchError(ContainSubstring("`s3:NotASupportedAction` is not a valid action.")), "%v", r)
		}
	})

	It("is false for a policy without statements", func() {
		var p policy.Policy
		Expect(p.IsPublic()).To(BeFalse(), "a policy without statements")
	})
})
