package policy_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The radosgw cases are PolicyTest's and IPPolicyTest's
// (src/test/rgw/test_rgw_iam_policy.cc), the same at v19.2.6 and v20.2.4 but
// for Eval3's action set; line numbers are v19.2.6's. Each example policy is
// built by hand from the testdata text of the same name, as parsed for
// arbitraryTenant, with each wildcard action taken from its per-service value
// rather than a release's parse. Each expectEval function takes its policy as
// an argument, so a parsed document can be held to the same expectations.

// arbitraryTenant is the tenant the cases parse their policies for. The parser
// gives it to every resource ARN that names no account.
const arbitraryTenant = "arbitrary_tenant"

// accountID is the account example2 and the IP policies name as principal.
const accountID = "ACCOUNT-ID-WITHOUT-HYPHENS"

// fakeIdentity is the cases' FakeIdentity (:144-208): the wildcard is every
// principal, and any other identity is the wildcard principal and the one
// equal to it. The cases' identities are TYPE_RGW; typ lets a spec make one a
// role.
type fakeIdentity struct {
	id  policy.Principal
	typ meta.IdentityType
}

func (f fakeIdentity) IsIdentity(p policy.Principal) bool {
	return f.id.IsWildcard() || p.IsWildcard() || p == f.id
}

func (f fakeIdentity) IdentityType() meta.IdentityType { return f.typ }

func fakeID(p policy.Principal) fakeIdentity { return fakeIdentity{id: p, typ: meta.IdentityRGW} }

func fakeRole(p policy.Principal) fakeIdentity { return fakeIdentity{id: p, typ: meta.IdentityRole} }

// testdata reads one of radosgw's policy texts.
func testdata(name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return string(b)
}

func actionSet(as ...policy.Action) policy.ActionSet {
	var s policy.ActionSet
	for _, a := range as {
		s.Set(a)
	}
	return s
}

// s3ARN is the ARN of a bucket or object of arbitraryTenant.
func s3ARN(resource string) policy.ARN {
	return policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceS3, Account: arbitraryTenant, Resource: resource}
}

func iamARN(account, resource string) policy.ARN {
	return policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceIAM, Account: account, Resource: resource}
}

// anyResource is what the parser makes of "Resource": "*" for arbitraryTenant
// (Parse3, :381-385).
func anyResource() policy.ARN {
	return policy.ARN{
		Partition: policy.PartitionWildcard, Service: policy.ServiceWildcard,
		Region: "*", Account: arbitraryTenant, Resource: "*",
	}
}

// s3Allow is Eval3's s3allow (:481-515), the actions "s3:List*" and "s3:Get*"
// name on r: 34 on v19.2.6, and on v20.2.4 three more (:488-525 there).
func s3Allow(r denc.Release) policy.ActionSet {
	s := actionSet(
		policy.S3ListMultipartUploadParts, policy.S3ListBucket, policy.S3ListBucketVersions,
		policy.S3ListAllMyBuckets, policy.S3ListBucketMultipartUploads, policy.S3GetObject,
		policy.S3GetObjectVersion, policy.S3GetObjectAcl, policy.S3GetObjectVersionAcl,
		policy.S3GetObjectTorrent, policy.S3GetObjectVersionTorrent, policy.S3GetAccelerateConfiguration,
		policy.S3GetBucketAcl, policy.S3GetBucketOwnershipControls, policy.S3GetBucketCORS,
		policy.S3GetBucketVersioning, policy.S3GetBucketRequestPayment, policy.S3GetBucketLocation,
		policy.S3GetBucketPolicy, policy.S3GetBucketNotification, policy.S3GetBucketLogging,
		policy.S3GetBucketTagging, policy.S3GetBucketWebsite, policy.S3GetLifecycleConfiguration,
		policy.S3GetReplicationConfiguration, policy.S3GetObjectTagging, policy.S3GetObjectVersionTagging,
		policy.S3GetBucketObjectLockConfiguration, policy.S3GetObjectRetention, policy.S3GetObjectLegalHold,
		policy.S3GetBucketPolicyStatus, policy.S3GetBucketPublicAccessBlock, policy.S3GetPublicAccessBlock,
		policy.S3GetBucketEncryption,
	)
	if r >= denc.Tentacle {
		s.Set(policy.S3GetObjectAttributes)
		s.Set(policy.S3GetObjectVersionAttributes)
		s.Set(policy.S3GetObjectVersionForReplication)
	}
	return s
}

func example1() *policy.Policy {
	return &policy.Policy{
		Text:    testdata("example1.json"),
		Version: policy.V2012_10_17,
		Statements: []policy.Statement{{
			Effect:    policy.Allow,
			Actions:   actionSet(policy.S3ListBucket),
			Resources: []policy.ARN{s3ARN("example_bucket")},
		}},
	}
}

// example2's "s3:*" is S3AllValue(): every s3 action rgw-go numbers.
func example2() *policy.Policy {
	return &policy.Policy{
		Text:    testdata("example2.json"),
		Version: policy.V2012_10_17,
		ID:      new("S3-Account-Permissions"),
		Statements: []policy.Statement{{
			Sid:        new("1"),
			Principals: []policy.Principal{policy.AccountPrincipal(accountID)},
			Effect:     policy.Allow,
			Actions:    policy.S3AllValue(),
			Resources:  []policy.ARN{s3ARN("mybucket"), s3ARN("mybucket/*")},
		}},
	}
}

func example3(r denc.Release) *policy.Policy {
	return &policy.Policy{
		Text:    testdata("example3.json"),
		Version: policy.V2012_10_17,
		Statements: []policy.Statement{
			{
				Sid:       new("FirstStatement"),
				Effect:    policy.Allow,
				Actions:   actionSet(policy.S3PutBucketPolicy),
				Resources: []policy.ARN{anyResource()},
			},
			{
				Sid:       new("SecondStatement"),
				Effect:    policy.Allow,
				Actions:   actionSet(policy.S3ListAllMyBuckets),
				Resources: []policy.ARN{anyResource()},
			},
			{
				Sid:        new("ThirdStatement"),
				Effect:     policy.Allow,
				Actions:    s3Allow(r),
				Resources:  []policy.ARN{s3ARN("confidential-data"), s3ARN("confidential-data/*")},
				Conditions: []policy.Condition{cond(policy.OpBool, "aws:MultiFactorAuthPresent", "true")},
			},
		},
	}
}

func example4() *policy.Policy {
	return &policy.Policy{
		Text:    testdata("example4.json"),
		Version: policy.V2012_10_17,
		Statements: []policy.Statement{{
			Effect:    policy.Allow,
			Actions:   actionSet(policy.IAMCreateRole),
			Resources: []policy.ARN{anyResource()},
		}},
	}
}

// example5's "iam:*" is IAMAllValue() and IAMAll.
func example5() *policy.Policy {
	actions := policy.IAMAllValue()
	actions.Set(policy.IAMAll)
	return &policy.Policy{
		Text:    testdata("example5.json"),
		Version: policy.V2012_10_17,
		Statements: []policy.Statement{{
			Effect:    policy.Allow,
			Actions:   actions,
			Resources: []policy.ARN{iamARN(arbitraryTenant, "role/example_role")},
		}},
	}
}

func example6() *policy.Policy {
	return &policy.Policy{
		Text:    testdata("example6.json"),
		Version: policy.V2012_10_17,
		Statements: []policy.Statement{{
			Effect:    policy.Allow,
			Actions:   policy.AllValue(),
			Resources: []policy.ARN{iamARN(arbitraryTenant, "user/A")},
		}},
	}
}

// example7's principal keeps its empty account: the parser gives the tenant
// to resources only (Parse7, :751).
func example7() *policy.Policy {
	return &policy.Policy{
		Text:    testdata("example7.json"),
		Version: policy.V2012_10_17,
		Statements: []policy.Statement{{
			Principals: []policy.Principal{policy.UserPrincipal("", "A:subA")},
			Effect:     policy.Allow,
			Actions:    actionSet(policy.S3ListBucket),
			Resources:  []policy.ARN{s3ARN("mybucket/*")},
		}},
	}
}

// blocklisted is the IP policies' NotIpAddress values (:1342, :1363).
func blocklisted() []string {
	return []string{"192.168.1.1/32", "2001:0db8:85a3:0000:0000:8a2e:0370:7334"}
}

func ipAllow() *policy.Policy {
	return &policy.Policy{
		Text:    testdata("ip_allow.json"),
		Version: policy.V2012_10_17,
		ID:      new("S3SimpleIPPolicyTest"),
		Statements: []policy.Statement{{
			Sid:        new("1"),
			Principals: []policy.Principal{policy.AccountPrincipal(accountID)},
			Effect:     policy.Allow,
			Actions:    actionSet(policy.S3ListBucket),
			Resources:  []policy.ARN{s3ARN("example_bucket")},
			Conditions: []policy.Condition{cond(policy.OpIPAddress, "aws:SourceIp", "192.168.1.0/24")},
		}},
	}
}

func ipDeny() *policy.Policy {
	return &policy.Policy{
		Text:    testdata("ip_deny.json"),
		Version: policy.V2012_10_17,
		ID:      new("S3IPPolicyTest"),
		Statements: []policy.Statement{{
			Sid:        new("IPDeny"),
			Principals: []policy.Principal{policy.AccountPrincipal(accountID)},
			Effect:     policy.Deny,
			Actions:    actionSet(policy.S3ListBucket),
			Resources:  []policy.ARN{s3ARN("example_bucket"), s3ARN("example_bucket/*")},
			Conditions: []policy.Condition{cond(policy.OpNotIPAddress, "aws:SourceIp", blocklisted()...)},
		}},
	}
}

func ipFull() *policy.Policy {
	return &policy.Policy{
		Text:    testdata("ip_full.json"),
		Version: policy.V2012_10_17,
		ID:      new("S3IPPolicyTest"),
		Statements: []policy.Statement{{
			Sid:        new("IPAllow"),
			Principals: []policy.Principal{policy.WildcardPrincipal()},
			Effect:     policy.Allow,
			Actions:    actionSet(policy.S3ListBucket),
			Resources:  []policy.ARN{s3ARN("example_bucket"), s3ARN("example_bucket/*")},
			Conditions: []policy.Condition{
				cond(policy.OpIPAddress, "aws:SourceIp", "192.168.1.0/24", "::1"),
				cond(policy.OpNotIPAddress, "aws:SourceIp", blocklisted()...),
			},
		}},
	}
}

// expectEval1 is Eval1 (:257-276).
func expectEval1(p *policy.Policy, sem policy.Semantics) {
	var e policy.Env
	ExpectWithOffset(1, p.Eval(e, nil, policy.S3ListBucket, new(s3ARN("example_bucket")), sem)).
		To(Equal(policy.Allow), "ListBucket on example_bucket")
	ExpectWithOffset(1, p.Eval(e, nil, policy.S3PutBucketAcl, new(s3ARN("example_bucket")), sem)).
		To(Equal(policy.Pass), "PutBucketAcl on example_bucket")
	ExpectWithOffset(1, p.Eval(e, nil, policy.S3ListBucket, new(s3ARN("erroneous_bucket")), sem)).
		To(Equal(policy.Pass), "ListBucket on erroneous_bucket")
}

// expectEval2 is Eval2 (:321-357).
func expectEval2(p *policy.Policy, sem policy.Semantics) {
	var e policy.Env
	trueAcct := fakeID(policy.AccountPrincipal(accountID))
	notAcct := fakeID(policy.AccountPrincipal("some-other-account"))
	for a := range policy.S3All {
		for _, res := range []string{"mybucket", "mybucket/myobject"} {
			ExpectWithOffset(1, p.Eval(e, trueAcct, a, new(s3ARN(res)), sem)).
				To(Equal(policy.Allow), "%v on %s for the account", a, res)
			ExpectWithOffset(1, p.Eval(e, notAcct, a, new(s3ARN(res)), sem)).
				To(Equal(policy.Pass), "%v on %s for another account", a, res)
		}
		for _, res := range []string{"notyourbucket", "notyourbucket/notyourobject"} {
			ExpectWithOffset(1, p.Eval(e, trueAcct, a, new(s3ARN(res)), sem)).
				To(Equal(policy.Pass), "%v on %s for the account", a, res)
		}
	}
}

// expectEval3 is Eval3 (:475-582) on r, whose parser gives example3's third
// statement s3Allow(r).
func expectEval3(p *policy.Policy, r denc.Release) {
	sem := policy.SemanticsFor(r)
	var em policy.Env
	envs := map[string]policy.Env{
		"no MFA key": em,
		"MFA true":   envOf("aws:MultiFactorAuthPresent", "true"),
		"MFA false":  envOf("aws:MultiFactorAuthPresent", "false"),
	}
	for _, res := range []string{"mybucket", "really-confidential-data/moo"} {
		ExpectWithOffset(1, p.Eval(em, nil, policy.S3PutBucketPolicy, new(s3ARN(res)), sem)).
			To(Equal(policy.Allow), "PutBucketPolicy on %s", res)
	}
	allow := s3Allow(r)
	for a := range policy.S3All {
		if a == policy.S3ListAllMyBuckets || a == policy.S3PutBucketPolicy {
			continue
		}
		for name, e := range envs {
			for _, res := range []string{"confidential-data", "confidential-data/moo"} {
				want := policy.Pass
				if name == "MFA true" && allow.Has(a) {
					want = policy.Allow
				}
				ExpectWithOffset(1, p.Eval(e, nil, a, new(s3ARN(res)), sem)).
					To(Equal(want), "%v on %s, %s", a, res, name)
			}
			for _, res := range []string{"really-confidential-data", "really-confidential-data/moo"} {
				ExpectWithOffset(1, p.Eval(e, nil, a, new(s3ARN(res)), sem)).
					To(Equal(policy.Pass), "%v on %s, %s", a, res, name)
			}
		}
	}
}

// expectEval4 is Eval4 (:614-627).
func expectEval4(p *policy.Policy, sem policy.Semantics) {
	var e policy.Env
	role := iamARN(arbitraryTenant, "role/example_role")
	ExpectWithOffset(1, p.Eval(e, nil, policy.IAMCreateRole, &role, sem)).To(Equal(policy.Allow), "CreateRole")
	ExpectWithOffset(1, p.Eval(e, nil, policy.IAMDeleteRole, &role, sem)).To(Equal(policy.Pass), "DeleteRole")
}

// expectEval5 is Eval5 (:659-677).
func expectEval5(p *policy.Policy, sem policy.Semantics) {
	var e policy.Env
	role := iamARN(arbitraryTenant, "role/example_role")
	ExpectWithOffset(1, p.Eval(e, nil, policy.IAMCreateRole, &role, sem)).To(Equal(policy.Allow), "CreateRole")
	ExpectWithOffset(1, p.Eval(e, nil, policy.S3ListBucket, &role, sem)).To(Equal(policy.Pass), "ListBucket")
	ExpectWithOffset(1, p.Eval(e, nil, policy.IAMCreateRole, new(iamARN("", "role/example_role")), sem)).
		To(Equal(policy.Pass), "CreateRole on a role of no account")
}

// expectEval6 is Eval6 (:709-722).
func expectEval6(p *policy.Policy, sem policy.Semantics) {
	var e policy.Env
	user := iamARN(arbitraryTenant, "user/A")
	ExpectWithOffset(1, p.Eval(e, nil, policy.IAMCreateRole, &user, sem)).To(Equal(policy.Allow), "CreateRole")
	ExpectWithOffset(1, p.Eval(e, nil, policy.S3ListBucket, &user, sem)).To(Equal(policy.Allow), "ListBucket")
}

// expectEval7 is Eval7 (:757-782).
func expectEval7(p *policy.Policy, sem policy.Semantics) {
	var e policy.Env
	res := s3ARN("mybucket/*")
	for id, want := range map[string]policy.Effect{"A:subA": policy.Allow, "A": policy.Pass, "A:sub2A": policy.Pass} {
		ExpectWithOffset(1, p.Eval(e, fakeID(policy.UserPrincipal("", id)), policy.S3ListBucket, &res, sem)).
			To(Equal(want), "user %q", id)
	}
}

// expectEvalIPAddress is EvalIPAddress (:1202-1307). The cases name the env
// holding ::1 allowedIPv6 and the one holding 2001:db8:85a3::8a2e:370:7334
// blocklistedIPv6.
func expectEvalIPAddress(allow, deny, full *policy.Policy, sem policy.Semantics) {
	var e policy.Env
	allowedIP := envOf("aws:SourceIp", "192.168.1.2")
	blocklistedIP := envOf("aws:SourceIp", "192.168.1.1")
	allowedIPv6 := envOf("aws:SourceIp", "::1")
	blocklistedIPv6 := envOf("aws:SourceIp", "2001:0db8:85a3:0000:0000:8a2e:0370:7334")
	trueAcct := fakeID(policy.AccountPrincipal(accountID))
	bucket := s3ARN("example_bucket")
	object := s3ARN("example_bucket/myobject")

	for _, c := range []struct {
		name string
		p    *policy.Policy
		env  policy.Env
		res  policy.ARN
		want policy.Effect
	}{
		{"allow, no address, bucket", allow, e, bucket, policy.Pass},
		{"full, no address, object", full, e, object, policy.Pass},
		{"allow, 192.168.1.2, bucket", allow, allowedIP, bucket, policy.Allow},
		{"allow, 2001:db8:85a3::8a2e:370:7334, bucket", allow, blocklistedIPv6, bucket, policy.Pass},
		{"deny, 192.168.1.2, bucket", deny, allowedIP, bucket, policy.Deny},
		{"deny, 192.168.1.2, object", deny, allowedIP, object, policy.Deny},
		{"deny, 192.168.1.1, bucket", deny, blocklistedIP, bucket, policy.Pass},
		{"deny, 192.168.1.1, object", deny, blocklistedIP, object, policy.Pass},
		{"deny, 2001:db8:85a3::8a2e:370:7334, bucket", deny, blocklistedIPv6, bucket, policy.Pass},
		{"deny, 2001:db8:85a3::8a2e:370:7334, object", deny, blocklistedIPv6, object, policy.Pass},
		{"deny, ::1, bucket", deny, allowedIPv6, bucket, policy.Deny},
		{"deny, ::1, object", deny, allowedIPv6, object, policy.Deny},
		{"full, 192.168.1.2, bucket", full, allowedIP, bucket, policy.Allow},
		{"full, 192.168.1.2, object", full, allowedIP, object, policy.Allow},
		{"full, 192.168.1.1, bucket", full, blocklistedIP, bucket, policy.Pass},
		{"full, 192.168.1.1, object", full, blocklistedIP, object, policy.Pass},
		{"full, ::1, bucket", full, allowedIPv6, bucket, policy.Allow},
		{"full, ::1, object", full, allowedIPv6, object, policy.Allow},
		{"full, 2001:db8:85a3::8a2e:370:7334, bucket", full, blocklistedIPv6, bucket, policy.Pass},
		{"full, 2001:db8:85a3::8a2e:370:7334, object", full, blocklistedIPv6, object, policy.Pass},
	} {
		ExpectWithOffset(1, c.p.Eval(c.env, trueAcct, policy.S3ListBucket, &c.res, sem)).To(Equal(c.want), c.name)
	}
}

var _ = Describe("Policy.Eval on radosgw's cases", func() {
	for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
		Context("on "+r.String(), func() {
			var sem policy.Semantics

			BeforeEach(func() {
				sem = policy.SemanticsFor(r)
			})

			It("allows example1's one action on its one bucket (Eval1)", func() {
				expectEval1(example1(), sem)
			})

			It("allows every s3 action to example2's account alone, on its bucket and objects (Eval2)", func() {
				expectEval2(example2(), sem)
			})

			It("allows example3's List and Get actions only under MFA (Eval3)", func() {
				expectEval3(example3(r), r)
			})

			It("matches one iam action on any resource (Eval4)", func() {
				expectEval4(example4(), sem)
			})

			It("matches an iam resource's account against the tenant the policy names (Eval5)", func() {
				expectEval5(example5(), sem)
			})

			It("matches every action, s3 ones included, on an iam resource (Eval6)", func() {
				expectEval6(example6(), sem)
			})

			It("matches a subuser principal to that subuser alone (Eval7)", func() {
				expectEval7(example7(), sem)
			})

			It("evaluates IpAddress and NotIpAddress on aws:SourceIp (EvalIPAddress)", func() {
				expectEvalIPAddress(ipAllow(), ipDeny(), ipFull(), sem)
			})
		})
	}
})

var _ = Describe("Statement", func() {
	var (
		squid, tentacle policy.Semantics
		alice, bob      policy.Principal
		admin           policy.Principal
		env             policy.Env
	)

	BeforeEach(func() {
		squid = policy.SemanticsFor(denc.Squid)
		tentacle = policy.SemanticsFor(denc.Tentacle)
		alice = policy.UserPrincipal("t", "alice")
		bob = policy.UserPrincipal("t", "bob")
		admin = policy.RolePrincipal("t", "admin")
		env = policy.Env{}
	})

	Describe("EvalPrincipal", func() {
		It("applies to a request without an identity, whatever it names", func() {
			s := policy.Statement{Principals: []policy.Principal{alice}, NotPrincipals: []policy.Principal{bob}}
			Expect(s.EvalPrincipal(nil)).To(Equal(policy.Allow))
			Expect((&policy.Statement{}).EvalPrincipal(nil)).To(Equal(policy.Allow), "a statement naming no principal")
		})

		It("applies to no identity when it names neither Principal nor NotPrincipal", func() {
			var s policy.Statement
			Expect(s.EvalPrincipal(fakeID(alice))).To(Equal(policy.Deny), "a user")
			Expect(s.EvalPrincipal(fakeRole(admin))).To(Equal(policy.Deny), "a role")
		})

		// The tables name users and roles of tenant "t".
		principals := func(mk func(account, id string) policy.Principal, names []string) []policy.Principal {
			var ps []policy.Principal
			for _, n := range names {
				ps = append(ps, mk("t", n))
			}
			return ps
		}

		DescribeTable("checks a user against Principal, then NotPrincipal",
			func(princ, notPrinc []string, id string, want policy.Effect) {
				s := policy.Statement{
					Principals:    principals(policy.UserPrincipal, princ),
					NotPrincipals: principals(policy.UserPrincipal, notPrinc),
				}
				Expect(s.EvalPrincipal(fakeID(policy.UserPrincipal("t", id)))).To(Equal(want))
			},
			Entry("Principal names the user", []string{"alice"}, nil, "alice", policy.Allow),
			Entry("Principal names another user", []string{"alice"}, nil, "bob", policy.Deny),
			Entry("NotPrincipal names the user", nil, []string{"alice"}, "alice", policy.Deny),
			Entry("NotPrincipal names another user", nil, []string{"alice"}, "bob", policy.Allow),
			Entry("Principal names the user and NotPrincipal another",
				[]string{"alice"}, []string{"bob"}, "alice", policy.Allow),
			Entry("Principal names another user and NotPrincipal the user",
				[]string{"alice"}, []string{"bob"}, "bob", policy.Deny),
			Entry("both name the user", []string{"alice"}, []string{"alice"}, "alice", policy.Deny),
		)

		DescribeTable("checks a role against Principal, and against NotPrincipal only when Principal is empty",
			func(princ, notPrinc []string, id string, want policy.Effect) {
				s := policy.Statement{
					Principals:    principals(policy.RolePrincipal, princ),
					NotPrincipals: principals(policy.RolePrincipal, notPrinc),
				}
				Expect(s.EvalPrincipal(fakeRole(policy.RolePrincipal("t", id)))).To(Equal(want))
			},
			Entry("Principal names the role", []string{"admin"}, nil, "admin", policy.Allow),
			Entry("Principal names another role", []string{"admin"}, nil, "auditor", policy.Deny),
			Entry("both name the role", []string{"admin"}, []string{"admin"}, "admin", policy.Allow),
			Entry("NotPrincipal alone names the role", nil, []string{"admin"}, "admin", policy.Deny),
			Entry("NotPrincipal alone names another role", nil, []string{"admin"}, "auditor", policy.Allow),
		)
	})

	Describe("EvalConditions", func() {
		It("allows when the statement has no condition", func() {
			Expect((&policy.Statement{}).EvalConditions(env, squid)).To(Equal(policy.Allow))
		})

		It("allows when every condition holds and denies when one does not", func() {
			s := policy.Statement{Conditions: []policy.Condition{
				cond(policy.OpStringEquals, "k", "a"),
				cond(policy.OpBool, "b", "true"),
			}}
			Expect(s.EvalConditions(envOf("k", "a", "b", "true"), squid)).To(Equal(policy.Allow), "both hold")
			Expect(s.EvalConditions(envOf("k", "a", "b", "false"), squid)).To(Equal(policy.Deny), "Bool fails")
			Expect(s.EvalConditions(envOf("k", "z", "b", "true"), squid)).To(Equal(policy.Deny), "StringEquals fails")
		})

		It("evaluates each condition under sem's rules", func() {
			s := policy.Statement{Conditions: []policy.Condition{cond(policy.OpStringNotEquals, "k", "a", "b")}}
			Expect(s.EvalConditions(envOf("k", "a"), squid)).To(Equal(policy.Allow), "squid")
			Expect(s.EvalConditions(envOf("k", "a"), tentacle)).To(Equal(policy.Deny), "tentacle")
		})
	})

	Describe("Eval", func() {
		var bucket policy.ARN

		BeforeEach(func() {
			bucket = s3ARN("b")
		})

		allowList := func() policy.Statement {
			return policy.Statement{
				Effect:    policy.Allow,
				Actions:   actionSet(policy.S3ListBucket),
				Resources: []policy.ARN{s3ARN("b")},
			}
		}

		It("passes an identity the principals exclude", func() {
			s := allowList()
			s.Principals = []policy.Principal{alice}
			Expect(s.Eval(env, fakeID(alice), policy.S3ListBucket, &bucket, squid)).To(Equal(policy.Allow), "alice")
			Expect(s.Eval(env, fakeID(bob), policy.S3ListBucket, &bucket, squid)).To(Equal(policy.Pass), "bob")
		})

		It("applies to a request without a resource only when it names none", func() {
			s := policy.Statement{Effect: policy.Allow, Actions: actionSet(policy.S3ListAllMyBuckets)}
			Expect(s.Eval(env, nil, policy.S3ListAllMyBuckets, nil, squid)).To(Equal(policy.Allow), "no resource")
			Expect(s.Eval(env, nil, policy.S3ListAllMyBuckets, &bucket, squid)).To(Equal(policy.Pass), "a resource")
		})

		It("passes a request without a resource when it names Resource or NotResource", func() {
			s := allowList()
			Expect(s.Eval(env, nil, policy.S3ListBucket, nil, squid)).To(Equal(policy.Pass), "Resource")
			s.Resources, s.NotResources = nil, []policy.ARN{s3ARN("other")}
			Expect(s.Eval(env, nil, policy.S3ListBucket, nil, squid)).To(Equal(policy.Pass), "NotResource")
		})

		It("passes a resource no Resource entry matches", func() {
			s := allowList()
			s.Resources = append(s.Resources, s3ARN("c/*"))
			Expect(s.Eval(env, nil, policy.S3ListBucket, new(s3ARN("c/k")), squid)).To(Equal(policy.Allow), "the second entry")
			Expect(s.Eval(env, nil, policy.S3ListBucket, new(s3ARN("d")), squid)).To(Equal(policy.Pass), "no entry")
		})

		It("passes a resource a NotResource entry matches and applies to every other", func() {
			s := policy.Statement{
				Effect:       policy.Deny,
				Actions:      actionSet(policy.S3GetObject),
				NotResources: []policy.ARN{s3ARN("b/public/*")},
			}
			Expect(s.Eval(env, nil, policy.S3GetObject, new(s3ARN("b/public/k")), squid)).To(Equal(policy.Pass), "b/public/k")
			Expect(s.Eval(env, nil, policy.S3GetObject, new(s3ARN("b/private/k")), squid)).To(Equal(policy.Deny), "b/private/k")
		})

		It("consults NotResource only when Resource is empty", func() {
			s := policy.Statement{
				Effect:       policy.Allow,
				Actions:      actionSet(policy.S3GetObject),
				Resources:    []policy.ARN{s3ARN("b/*")},
				NotResources: []policy.ARN{s3ARN("b/secret")},
			}
			Expect(s.Eval(env, nil, policy.S3GetObject, new(s3ARN("b/secret")), squid)).To(Equal(policy.Allow))
		})

		It("passes an action Action lacks or NotAction holds", func() {
			s := policy.Statement{
				Effect:     policy.Allow,
				Actions:    policy.S3AllValue(),
				NotActions: actionSet(policy.S3DeleteObject),
				Resources:  []policy.ARN{s3ARN("b/*")},
			}
			obj := s3ARN("b/k")
			Expect(s.Eval(env, nil, policy.S3GetObject, &obj, squid)).To(Equal(policy.Allow), "GetObject")
			Expect(s.Eval(env, nil, policy.S3DeleteObject, &obj, squid)).To(Equal(policy.Pass), "DeleteObject, in NotAction")
			Expect(s.Eval(env, nil, policy.IAMCreateRole, &obj, squid)).To(Equal(policy.Pass), "CreateRole, not in Action")
		})

		It("denies when it names no Effect", func() {
			s := policy.Statement{Actions: actionSet(policy.S3ListBucket), Resources: []policy.ARN{s3ARN("b")}}
			p := policy.Policy{Statements: []policy.Statement{s}}
			for name, sem := range map[string]policy.Semantics{"squid": squid, "tentacle": tentacle} {
				Expect(s.Eval(env, nil, policy.S3ListBucket, &bucket, sem)).To(Equal(policy.Deny), "%s: the statement", name)
				Expect(p.Eval(env, nil, policy.S3ListBucket, &bucket, sem)).To(Equal(policy.Deny), "%s: its policy", name)
			}
		})

		It("passes a value that names no action", func() {
			s := policy.Statement{Effect: policy.Allow, Actions: policy.AllValue()}
			Expect(s.Eval(env, nil, policy.ActionCount, nil, squid)).To(Equal(policy.Pass), "ActionCount")
			Expect(s.Eval(env, nil, policy.ActionNone, nil, squid)).To(Equal(policy.Pass), "ActionNone")
		})

		It("gives its effect only when every condition holds", func() {
			s := policy.Statement{
				Effect:  policy.Deny,
				Actions: actionSet(policy.S3ListBucket),
				Conditions: []policy.Condition{
					cond(policy.OpStringEquals, "k", "a"),
					cond(policy.OpBool, "b", "true"),
				},
				Resources: []policy.ARN{s3ARN("b")},
			}
			Expect(s.Eval(envOf("k", "a", "b", "true"), nil, policy.S3ListBucket, &bucket, squid)).
				To(Equal(policy.Deny), "both hold")
			Expect(s.Eval(envOf("k", "a"), nil, policy.S3ListBucket, &bucket, squid)).
				To(Equal(policy.Pass), "Bool's key absent")
		})

		It("evaluates its conditions under sem's rules", func() {
			s := allowList()
			s.Conditions = []policy.Condition{cond(policy.OpStringNotEquals, "k", "a", "b")}
			Expect(s.Eval(envOf("k", "a"), nil, policy.S3ListBucket, &bucket, squid)).To(Equal(policy.Allow), "squid")
			Expect(s.Eval(envOf("k", "a"), nil, policy.S3ListBucket, &bucket, tentacle)).To(Equal(policy.Pass), "tentacle")
		})
	})
})

var _ = Describe("Policy", func() {
	var (
		squid  policy.Semantics
		bucket policy.ARN
		env    policy.Env
	)

	BeforeEach(func() {
		squid = policy.SemanticsFor(denc.Squid)
		bucket = s3ARN("b")
		env = policy.Env{}
	})

	statement := func(effect policy.Effect, a policy.Action) policy.Statement {
		return policy.Statement{Effect: effect, Actions: actionSet(a), Resources: []policy.ARN{s3ARN("b")}}
	}

	Describe("Eval", func() {
		DescribeTable("gives Deny precedence, then Allow, then Pass",
			func(want policy.Effect, statements ...policy.Statement) {
				p := policy.Policy{Statements: statements}
				Expect(p.Eval(env, nil, policy.S3ListBucket, &bucket, squid)).To(Equal(want))
			},
			Entry("Allow, then Deny", policy.Deny,
				statement(policy.Allow, policy.S3ListBucket), statement(policy.Deny, policy.S3ListBucket)),
			Entry("Deny, then Allow", policy.Deny,
				statement(policy.Deny, policy.S3ListBucket), statement(policy.Allow, policy.S3ListBucket)),
			Entry("a statement that does not apply, then Allow", policy.Allow,
				statement(policy.Deny, policy.S3GetObject), statement(policy.Allow, policy.S3ListBucket)),
			Entry("no statement that applies", policy.Pass,
				statement(policy.Deny, policy.S3GetObject), statement(policy.Allow, policy.S3PutObject)),
			Entry("no statement", policy.Pass),
		)
	})

	Describe("IsPublic", func() {
		var (
			alice    policy.Principal
			everyone []policy.Principal
		)

		BeforeEach(func() {
			alice = policy.UserPrincipal("t", "alice")
			everyone = []policy.Principal{policy.WildcardPrincipal()}
		})

		allow := func(princ, notPrinc []policy.Principal, conds ...policy.Condition) policy.Statement {
			return policy.Statement{
				Principals:    princ,
				NotPrincipals: notPrinc,
				Effect:        policy.Allow,
				Actions:       actionSet(policy.S3GetObject),
				Resources:     []policy.ARN{s3ARN("b/*")},
				Conditions:    conds,
			}
		}
		DescribeTable("judges a policy public by its Allow statements",
			func(stmts func() []policy.Statement, wantSquid, wantTentacle bool) {
				p := policy.Policy{Statements: stmts()}
				Expect(p.IsPublic(policy.SemanticsFor(denc.Squid))).To(Equal(wantSquid), "squid")
				Expect(p.IsPublic(policy.SemanticsFor(denc.Tentacle))).To(Equal(wantTentacle), "tentacle")
			},
			Entry("for every principal, with no condition", func() []policy.Statement {
				return []policy.Statement{allow(everyone, nil)}
			}, true, true),
			Entry("for every principal, whatever its action and resource", func() []policy.Statement {
				s := allow(everyone, nil)
				s.Actions, s.Resources = actionSet(policy.S3PutObject), []policy.ARN{s3ARN("other/*")}
				return []policy.Statement{s}
			}, true, true),
			Entry("for every principal, with a condition 1.1.1.1 fails", func() []policy.Statement {
				return []policy.Statement{allow(everyone, nil, cond(policy.OpIPAddress, "aws:SourceIp", "10.0.0.0/8"))}
			}, false, false),
			Entry("for every principal, with a condition 1.1.1.1 meets", func() []policy.Statement {
				return []policy.Statement{allow(everyone, nil, cond(policy.OpIPAddress, "aws:SourceIp", "1.1.1.0/24"))}
			}, true, true),
			Entry("for every principal, with conditions on the user id and KMS key it sets", func() []policy.Statement {
				return []policy.Statement{allow(everyone, nil,
					cond(policy.OpStringEquals, "aws:UserId", "anonymous"),
					cond(policy.OpStringEquals, "s3:x-amz-server-side-encryption-aws-kms-key-id", "secret"))}
			}, true, true),
			Entry("for every principal, with a condition on a key it does not set", func() []policy.Statement {
				return []policy.Statement{allow(everyone, nil, cond(policy.OpBool, "aws:SecureTransport", "true"))}
			}, false, false),
			Entry("for every principal, with a condition whose result depends on sem", func() []policy.Statement {
				return []policy.Statement{allow(everyone, nil, cond(policy.OpStringNotEquals, "aws:UserId", "anonymous", "x"))}
			}, true, false),
			Entry("for one user", func() []policy.Statement {
				return []policy.Statement{allow([]policy.Principal{alice}, nil)}
			}, true, false),
			Entry("for one user, whatever its conditions", func() []policy.Statement {
				return []policy.Statement{allow([]policy.Principal{alice}, nil, cond(policy.OpIPAddress, "aws:SourceIp", "10.0.0.0/8"))}
			}, true, false),
			Entry("that names neither Principal nor NotPrincipal", func() []policy.Statement {
				return []policy.Statement{allow(nil, nil)}
			}, true, false),
			Entry("whose NotPrincipal names one user", func() []policy.Statement {
				return []policy.Statement{allow(nil, []policy.Principal{alice})}
			}, true, false),
			Entry("whose NotPrincipal names every principal", func() []policy.Statement {
				return []policy.Statement{allow(nil, everyone)}
			}, false, false),
			Entry("never for a Deny statement", func() []policy.Statement {
				s := allow(everyone, nil)
				s.Effect = policy.Deny
				return []policy.Statement{s}
			}, false, false),
			Entry("never for a statement for every principal that names no Effect", func() []policy.Statement {
				return []policy.Statement{{
					Principals: everyone,
					Actions:    actionSet(policy.S3GetObject),
					Resources:  []policy.ARN{s3ARN("b/*")},
				}}
			}, false, false),
			Entry("never for a statement that names nothing", func() []policy.Statement {
				return []policy.Statement{{}}
			}, false, false),
			Entry("when one statement of several is public", func() []policy.Statement {
				deny := allow(everyone, nil)
				deny.Effect = policy.Deny
				return []policy.Statement{
					deny,
					allow(everyone, nil, cond(policy.OpIPAddress, "aws:SourceIp", "10.0.0.0/8")),
					allow(everyone, nil),
				}
			}, true, true),
		)

		It("is false for a policy without statements", func() {
			var p policy.Policy
			Expect(p.IsPublic(policy.SemanticsFor(denc.Squid))).To(BeFalse(), "squid")
			Expect(p.IsPublic(policy.SemanticsFor(denc.Tentacle))).To(BeFalse(), "tentacle")
		})
	})

	Describe("HasConditionKeyPrefix and HasConditionValuePrefix", func() {
		var p policy.Policy

		BeforeEach(func() {
			p = policy.Policy{Statements: []policy.Statement{
				{Conditions: []policy.Condition{cond(policy.OpStringEquals, "s3:ExistingObjectTag/env", "${aws:username}")}},
				{},
				{Conditions: []policy.Condition{cond(policy.OpStringLike, "aws:Referer", "https://example.com/*", "ké")}},
				{Conditions: []policy.Condition{cond(policy.OpStringEquals, "ké", "v")}},
			}}
		})

		DescribeTable("HasConditionKeyPrefix folds ASCII case on condition keys",
			func(prefix string, want bool) {
				Expect(p.HasConditionKeyPrefix(prefix)).To(Equal(want))
			},
			Entry("a prefix of a key", "s3:ExistingObjectTag/", true),
			Entry("a prefix in another case", "S3:EXISTINGOBJECTTAG", true),
			Entry("a whole key of a later statement", "aws:Referer", true),
			Entry("text longer than every key", "aws:Referer/x", false),
			Entry("text no key starts with", "s3:RequestObjectTag", false),
			Entry("a value's prefix", "${aws:", false),
			Entry("a key's prefix differing in a non-ASCII letter's case", "KÉ", false),
			Entry("the empty prefix", "", true),
		)

		DescribeTable("HasConditionValuePrefix folds ASCII case on condition values",
			func(prefix string, want bool) {
				Expect(p.HasConditionValuePrefix(prefix)).To(Equal(want))
			},
			Entry("a prefix of a value", "${aws:", true),
			Entry("a prefix in another case", "${AWS:USERNAME}", true),
			Entry("a later value of a condition", "Ké", true),
			Entry("a value's prefix differing in a non-ASCII letter's case", "KÉ", false),
			Entry("a key's prefix", "s3:Existing", false),
		)

		It("finds nothing in a policy without conditions", func() {
			p = policy.Policy{Statements: []policy.Statement{{}}}
			Expect(p.HasConditionKeyPrefix("")).To(BeFalse(), "key")
			Expect(p.HasConditionValuePrefix("")).To(BeFalse(), "value")
		})
	})
})

var _ = Describe("Effect", func() {
	It("is Deny when zero", func() {
		var e policy.Effect
		Expect(e).To(Equal(policy.Deny))
	})

	DescribeTable("String names the effect",
		func(e policy.Effect, want string) {
			Expect(e.String()).To(Equal(want))
		},
		Entry("Deny", policy.Deny, "Deny"),
		Entry("Allow", policy.Allow, "Allow"),
		Entry("Pass", policy.Pass, "Pass"),
		Entry("a value that names no effect", policy.Effect(3), "Effect(3)"),
	)
})

var _ = Describe("Version", func() {
	DescribeTable("String spells the version as a policy does",
		func(v policy.Version, want string) {
			Expect(v.String()).To(Equal(want))
		},
		Entry("2008-10-17", policy.V2008_10_17, "2008-10-17"),
		Entry("2012-10-17", policy.V2012_10_17, "2012-10-17"),
		Entry("a value that names no version", policy.Version(2), "Version(2)"),
	)
})
