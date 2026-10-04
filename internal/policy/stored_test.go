package policy_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The managed-policy cases are ManagedPolicyTest's
// (src/test/rgw/test_rgw_iam_policy.cc:792-923 at v19.2.6, :804-938 at
// v20.2.4).

const (
	iamFullAccess           = "arn:aws:iam::aws:policy/IAMFullAccess"
	iamReadOnlyAccess       = "arn:aws:iam::aws:policy/IAMReadOnlyAccess"
	amazonSNSFullAccess     = "arn:aws:iam::aws:policy/AmazonSNSFullAccess"
	amazonSNSReadOnlyAccess = "arn:aws:iam::aws:policy/AmazonSNSReadOnlyAccess"
	amazonS3FullAccess      = "arn:aws:iam::aws:policy/AmazonS3FullAccess"
	amazonS3ReadOnlyAccess  = "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"
)

// encodeUserPolicies is RGW_ATTR_USER_POLICY's encoding of m, a bare
// std::map<string, string>.
func encodeUserPolicies(m map[string]string) []byte {
	e := denc.NewEncoder()
	denc.EncodeMap(e, m, (*denc.Encoder).String, (*denc.Encoder).String)
	return e.Bytes()
}

// encodeManagedPolicies is RGW_ATTR_MANAGED_POLICY's encoding of arns, in
// the order given, under ENCODE_START(version, compat).
func encodeManagedPolicies(version, compat uint8, arns ...string) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(version, compat)
	denc.EncodeSlice(e, arns, (*denc.Encoder).String)
	e.EndStruct(f)
	return e.Bytes()
}

// s3ReadOnly is AmazonS3ReadOnlyAccess's actions on r: s3:Get*, s3:List*,
// s3:Describe*, and both s3-object-lambda actions, which with them set
// s3-object-lambda's All.
func s3ReadOnly(r denc.Release) policy.ActionSet {
	s := s3Allow(r)
	for _, a := range []policy.Action{
		policy.S3DescribeJob, policy.S3ObjectLambdaGetObject, policy.S3ObjectLambdaListBucket, policy.S3ObjectLambdaAll,
	} {
		s.Set(a)
	}
	return s
}

var _ = Describe("DecodeUserPolicies", func() {
	var tenant *string

	BeforeEach(func() {
		tenant = new(arbitraryTenant)
	})

	It("parses each stored policy, in name order, for the tenant", func() {
		b := encodeUserPolicies(map[string]string{"p2": testdata("example4.json"), "p1": testdata("example1.json")})
		ps, err := policy.DecodeUserPolicies(b, tenant, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(2))
		Expect(ps[0].Text).To(Equal(testdata("example1.json")), "p1")
		Expect(ps[1].Text).To(Equal(testdata("example4.json")), "p2")
		expectEval1(ps[0], policy.SemanticsFor(denc.Squid))
	})

	It("decodes an empty map to no policies", func() {
		ps, err := policy.DecodeUserPolicies(encodeUserPolicies(nil), tenant, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(BeEmpty())
	})

	It("drops an unsupported principal rather than failing", func() {
		captureLogs()
		b := encodeUserPolicies(map[string]string{"p": stmt(`"Principal": {"CanonicalUser": "abc"}`)})
		ps, err := policy.DecodeUserPolicies(b, tenant, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps[0].Statements[0].Principals).To(BeEmpty())
	})

	It("checks resources against the tenant, and against none without one", func() {
		b := encodeUserPolicies(map[string]string{"p": stmt(`"Resource": "arn:aws:s3::othertenant:b"`)})
		_, err := policy.DecodeUserPolicies(b, tenant, denc.Squid)
		Expect(annotationOf(err)).To(ContainSubstring("cannot grant access to resource owned by tenant"))
		ps, err := policy.DecodeUserPolicies(b, nil, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps[0].Statements[0].Resources[0].Account).To(Equal("othertenant"))
	})

	It("parses with the release's vocabulary", func() {
		b := encodeUserPolicies(map[string]string{"p": stmt(`"Action": "s3:GetObjectAttributes"`)})
		_, err := policy.DecodeUserPolicies(b, tenant, denc.Squid)
		Expect(annotationOf(err)).To(Equal("`s3:GetObjectAttributes` is not a valid action."), "squid")
		_, err = policy.DecodeUserPolicies(b, tenant, denc.Tentacle)
		Expect(err).NotTo(HaveOccurred(), "tentacle")
	})

	It("fails on a policy that does not parse", func() {
		b := encodeUserPolicies(map[string]string{"good": testdata("example1.json"), "bad": `{"Version": "x"}`})
		_, err := policy.DecodeUserPolicies(b, tenant, denc.Squid)
		pe, _ := errors.AsType[*policy.ParseError](err)
		Expect(pe).To(Equal(&policy.ParseError{
			Offset:     15,
			Annotation: "`x` is not a valid version. Valid versions are `2008-10-17` and `2012-10-17`.",
		}))
	})

	It("fails on an attr that is empty or cut short", func() {
		b := encodeUserPolicies(map[string]string{"p1": testdata("example1.json")})
		_, err := policy.DecodeUserPolicies(nil, tenant, denc.Squid)
		Expect(err).To(MatchError(denc.ErrShortBuffer), "empty")
		_, err = policy.DecodeUserPolicies(b[:len(b)-1], tenant, denc.Squid)
		Expect(err).To(MatchError(denc.ErrShortBuffer), "cut short")
	})

	It("fails on bytes past the map", func() {
		b := append(encodeUserPolicies(map[string]string{"p1": testdata("example1.json")}), 0)
		_, err := policy.DecodeUserPolicies(b, tenant, denc.Squid)
		Expect(err).To(MatchError(denc.ErrMalformed))
	})
})

var _ = Describe("DecodeManagedPolicies", func() {
	It("returns the managed policies the set names, skipping an unknown ARN", func() {
		b := encodeManagedPolicies(1, 1, amazonS3ReadOnlyAccess, "arn:aws:iam::aws:policy/Unknown")
		ps, err := policy.DecodeManagedPolicies(b, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(1))
		Expect(namesIn(ps[0].Statements[0].Actions)).To(ConsistOf(namesIn(s3ReadOnly(denc.Squid))))
	})

	It("returns them in ARN order, once each, as radosgw's set holds them", func() {
		b := encodeManagedPolicies(1, 1, iamFullAccess, amazonS3ReadOnlyAccess, iamFullAccess)
		ps, err := policy.DecodeManagedPolicies(b, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(2))
		Expect(ps[0].Text).To(Equal(must(policy.ManagedPolicy(amazonS3ReadOnlyAccess, denc.Squid)).Text), "first")
		Expect(ps[1].Text).To(Equal(must(policy.ManagedPolicy(iamFullAccess, denc.Squid)).Text), "second")
	})

	It("decodes a later struct version, skipping the fields it adds", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 1)
		denc.EncodeSlice(e, []string{amazonSNSReadOnlyAccess}, (*denc.Encoder).String)
		e.U32(7)
		e.EndStruct(f)
		ps, err := policy.DecodeManagedPolicies(e.Bytes(), denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(HaveLen(1))
	})

	It("decodes an empty set to no policies", func() {
		ps, err := policy.DecodeManagedPolicies(encodeManagedPolicies(1, 1), denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		Expect(ps).To(BeEmpty())
	})

	It("fails on a struct whose compat version is newer than 1", func() {
		_, err := policy.DecodeManagedPolicies(encodeManagedPolicies(2, 2, iamFullAccess), denc.Squid)
		Expect(err).To(MatchError(denc.ErrIncompatible))
	})

	It("fails on an attr that is empty or cut short", func() {
		b := encodeManagedPolicies(1, 1, iamFullAccess)
		_, err := policy.DecodeManagedPolicies(nil, denc.Squid)
		Expect(err).To(MatchError(denc.ErrShortBuffer), "empty")
		_, err = policy.DecodeManagedPolicies(b[:len(b)-1], denc.Squid)
		Expect(err).To(MatchError(denc.ErrShortBuffer), "cut short")
	})

	It("fails on bytes past the struct", func() {
		b := append(encodeManagedPolicies(1, 1, iamFullAccess), 0)
		_, err := policy.DecodeManagedPolicies(b, denc.Squid)
		Expect(err).To(MatchError(denc.ErrMalformed))
	})
})

// must is v, failing the spec unless ok.
func must[T any](v T, ok bool) T {
	ExpectWithOffset(1, ok).To(BeTrue(), "value %v", v)
	return v
}

var _ = Describe("ManagedPolicy", func() {
	It("knows no other ARN", func() {
		p, ok := policy.ManagedPolicy("arn:aws:iam::aws:policy/Unknown", denc.Squid)
		Expect(ok).To(BeFalse(), "policy %v", p)
	})

	DescribeTable("holds radosgw's text byte for byte",
		func(arn, sum string) {
			for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
				p := must(policy.ManagedPolicy(arn, r))
				got := sha256.Sum256([]byte(p.Text))
				Expect(hex.EncodeToString(got[:])).To(Equal(sum), "%s on %s", arn, r)
			}
		},
		// The SHA-256 of each raw string of src/rgw/rgw_iam_managed_policy.cc,
		// the same at v19.2.6 and v20.2.4.
		Entry("IAMFullAccess", iamFullAccess, "4f4e519fc8f9cc4419e1e699c8920fe2765b1f0d70e88743f077a5dc73f2dea4"),
		Entry("IAMReadOnlyAccess", iamReadOnlyAccess, "bd07e9037cd16220b07149e43ce2fc7786c080d4a5a752065ea06a52a0473f90"),
		Entry("AmazonSNSFullAccess", amazonSNSFullAccess, "b50b64de699c66ffcec71e3b97bb25701b1a07126c7f74252eb10a639b6c052d"),
		Entry("AmazonSNSReadOnlyAccess", amazonSNSReadOnlyAccess, "d5b9d9544134a32dac9e06e694f6cd4719b6828252b2ecf9e5694d48a8b08509"),
		Entry("AmazonS3FullAccess", amazonS3FullAccess, "b44f8ddf27dad8a2650e9bf035cfe23a07c124988b000dc2af4fc62e8dd21af1"),
		Entry("AmazonS3ReadOnlyAccess", amazonS3ReadOnlyAccess, "baa4c2aa4108efc73a6f040fa0aa26ed686ff6149ce92a3b60a3271cbe8a37bf"),
	)

	DescribeTable("parses to radosgw's actions on each release",
		func(arn string, want func(r denc.Release) policy.ActionSet) {
			for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
				p := must(policy.ManagedPolicy(arn, r))
				Expect(p.Statements).To(HaveLen(1), "%s on %s", arn, r)
				Expect(namesIn(p.Statements[0].Actions)).To(ConsistOf(namesIn(want(r))), "%s on %s", arn, r)
			}
		},
		Entry("IAMFullAccess: iam:* and the ten organizations actions, with both Alls", iamFullAccess,
			func(r denc.Release) policy.ActionSet {
				s := parsedAll(r, policy.IAMAllValue(), policy.IAMAll)
				s.Union(parsedAll(r, policy.OrganizationsAllValue(), policy.OrganizationsAll))
				return s
			}),
		Entry("IAMReadOnlyAccess: iam:Get*, iam:List* and four more", iamReadOnlyAccess,
			func(denc.Release) policy.ActionSet {
				return actionSet(
					policy.IAMGenerateCredentialReport, policy.IAMGenerateServiceLastAccessedDetails,
					policy.IAMGetUserPolicy, policy.IAMGetRole, policy.IAMGetRolePolicy, policy.IAMGetOIDCProvider,
					policy.IAMGetUser, policy.IAMListUserPolicies, policy.IAMListAttachedUserPolicies,
					policy.IAMListRoles, policy.IAMListRolePolicies, policy.IAMListAttachedRolePolicies,
					policy.IAMListOIDCProviders, policy.IAMListRoleTags, policy.IAMListUsers, policy.IAMListAccessKeys,
					policy.IAMGetGroup, policy.IAMListGroups, policy.IAMListGroupsForUser, policy.IAMGetGroupPolicy,
					policy.IAMListGroupPolicies, policy.IAMListAttachedGroupPolicies,
					policy.IAMSimulateCustomPolicy, policy.IAMSimulatePrincipalPolicy,
				)
			}),
		Entry("AmazonSNSFullAccess: sns:*", amazonSNSFullAccess,
			func(r denc.Release) policy.ActionSet { return parsedAll(r, policy.SNSAllValue(), policy.SNSAll) }),
		Entry("AmazonSNSReadOnlyAccess: two sns actions", amazonSNSReadOnlyAccess,
			func(denc.Release) policy.ActionSet {
				return actionSet(policy.SNSGetTopicAttributes, policy.SNSListTopics)
			}),
		Entry("AmazonS3FullAccess: s3:* and s3-object-lambda:*", amazonS3FullAccess,
			func(r denc.Release) policy.ActionSet {
				s := parsedAll(r, policy.S3AllValue(), policy.S3All)
				s.Union(parsedAll(r, policy.S3ObjectLambdaAllValue(), policy.S3ObjectLambdaAll))
				return s
			}),
		Entry("AmazonS3ReadOnlyAccess: the read actions", amazonS3ReadOnlyAccess, s3ReadOnly),
	)

	It("parses without a tenant, keeping a resource's * account", func() {
		p := must(policy.ManagedPolicy(amazonS3FullAccess, denc.Squid))
		Expect(p.Statements[0].Resources).To(Equal([]policy.ARN{{
			Partition: policy.PartitionWildcard, Service: policy.ServiceWildcard, Region: "*", Account: "*", Resource: "*",
		}}))
	})

	It("returns a policy of its own to each caller", func() {
		a := must(policy.ManagedPolicy(iamFullAccess, denc.Squid))
		a.Statements[0].Effect = policy.Deny
		b := must(policy.ManagedPolicy(iamFullAccess, denc.Squid))
		Expect(b.Statements[0].Effect).To(Equal(policy.Allow))
	})
})
