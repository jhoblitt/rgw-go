package policy_test

import (
	"math"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// tentacleOnly are the actions v20.2.4 added to action_t
// (src/rgw/rgw_iam_policy.h:84, :118-123 and :153-155 at v20.2.4); mainOnly
// are the two only ceph main has.
var (
	tentacleOnly = []policy.Action{
		policy.S3PostBucketLogging,
		policy.S3GetObjectAttributes,
		policy.S3GetObjectVersionAttributes,
		policy.S3ReplicateDelete,
		policy.S3ReplicateObject,
		policy.S3GetObjectVersionForReplication,
		policy.S3ReplicateTags,
		policy.IAMAddClientIDToOIDCProvider,
		policy.IAMRemoveClientIDFromOIDCProvider,
		policy.IAMUpdateOIDCProviderThumbprint,
	}
	mainOnly = []policy.Action{policy.S3PutAccountPublicAccessBlock, policy.S3GetAccountPublicAccessBlock}
)

// namesIn lists the names of the actions in s, for failure messages that
// show which bits differ.
func namesIn(s policy.ActionSet) []string {
	var names []string
	for a := range policy.ActionCount {
		if s.Has(a) {
			names = append(names, a.String())
		}
	}
	return names
}

var _ = Describe("actions", func() {
	It("numbers the other services' actions after s3All, as action_t does", func() {
		Expect(policy.S3ObjectLambdaGetObject).To(Equal(policy.S3All + 1))
		Expect(policy.ActionCount).To(Equal(policy.OrganizationsAll + 1))
	})
	DescribeTable("renders the name radosgw's actpairs table gives it",
		func(a policy.Action, want string) {
			Expect(a.String()).To(Equal(want))
		},
		Entry("the first s3-object-lambda action", policy.S3ObjectLambdaGetObject, "s3-object-lambda:GetObject"),
		Entry("the second s3-object-lambda action", policy.S3ObjectLambdaListBucket, "s3-object-lambda:ListBucket"),
		Entry("an iam action", policy.IAMListAttachedGroupPolicies, "iam:ListAttachedGroupPolicies"),
		Entry("an iam action radosgw spells with Id", policy.IAMAddClientIDToOIDCProvider, "iam:AddClientIdToOIDCProvider"),
		Entry("the iam action v20.2.4 misspells", policy.IAMRemoveClientIDFromOIDCProvider, "iam:RemoveCientIdFromOIDCProvider"),
		Entry("an sts action", policy.STSAssumeRoleWithWebIdentity, "sts:AssumeRoleWithWebIdentity"),
		Entry("an sns action", policy.SNSPublish, "sns:Publish"),
		Entry("the last named action", policy.OrganizationsListTargetsForPolicy, "organizations:ListTargetsForPolicy"),
		Entry("the s3-object-lambda wildcard", policy.S3ObjectLambdaAll, "s3-object-lambda:*"),
		Entry("the iam wildcard", policy.IAMAll, "iam:*"),
		Entry("the sts wildcard", policy.STSAll, "sts:*"),
		Entry("the sns wildcard", policy.SNSAll, "sns:*"),
		Entry("the organizations wildcard", policy.OrganizationsAll, "organizations:*"),
	)
	It("round-trips every action through ParseAction", func() {
		for a := range policy.ActionCount {
			got, ok := policy.ParseAction(a.String())
			Expect(ok).To(BeTrue(), "action %d, %q", a, a.String())
			Expect(got).To(Equal(a), "action %d, %q", a, a.String())
		}
	})
	It("refuses the correct spelling of the name v20.2.4 misspells", func() {
		a, ok := policy.ParseAction("iam:RemoveClientIdFromOIDCProvider")
		Expect(ok).To(BeFalse(), "parsed as %v", a)
	})
	DescribeTable("Known follows the release's actpairs table",
		func(a policy.Action, squid, tentacle bool) {
			Expect(policy.Known(a, denc.Squid)).To(Equal(squid), "%s on squid", a)
			Expect(policy.Known(a, denc.Tentacle)).To(Equal(tentacle), "%s on tentacle", a)
		},
		Entry("an action both releases have", policy.S3GetObject, true, true),
		Entry("an s3 action v20.2.4 added at the end of the block", policy.S3GetObjectAttributes, false, true),
		Entry("an s3 action v20.2.4 added among the logging ones", policy.S3PostBucketLogging, false, true),
		Entry("an iam action v20.2.4 added", policy.IAMUpdateOIDCProviderThumbprint, false, true),
		Entry("an action only ceph main has", policy.S3PutAccountPublicAccessBlock, false, false),
		Entry("a service wildcard", policy.IAMAll, true, true),
		Entry("a value past the table", policy.Action(math.MaxUint16), false, false),
	)
	It("leaves unknown only the actions a release's action_t lacks", func() {
		var squid, tentacle []policy.Action
		for a := range policy.ActionCount {
			if !policy.Known(a, denc.Squid) {
				squid = append(squid, a)
			}
			if !policy.Known(a, denc.Tentacle) {
				tentacle = append(tentacle, a)
			}
		}
		Expect(squid).To(ConsistOf(slices.Concat(tentacleOnly, mainOnly)))
		Expect(tentacle).To(ConsistOf(mainOnly))
	})
	DescribeTable("Known admits one action per row of the release's actpairs table",
		func(r denc.Release, rows map[string]int) {
			got := map[string]int{}
			for a := range policy.ActionCount {
				service, name, _ := strings.Cut(a.String(), ":")
				if name != "*" && policy.Known(a, r) {
					got[service]++
				}
			}
			Expect(got).To(Equal(rows))
		},
		// src/rgw/rgw_iam_policy.cc:64-215 at v19.2.6, :64-225 at v20.2.4.
		Entry("squid", denc.Squid, map[string]int{
			"s3": 73, "s3-object-lambda": 2, "iam": 55, "sts": 4, "sns": 6, "organizations": 10,
		}),
		Entry("tentacle", denc.Tentacle, map[string]int{
			"s3": 80, "s3-object-lambda": 2, "iam": 58, "sts": 4, "sns": 6, "organizations": 10,
		}),
	)
})

var _ = Describe("ActionSet", func() {
	It("sets, tests and unions actions", func() {
		var s policy.ActionSet
		Expect(s.IsZero()).To(BeTrue())
		s.Set(policy.S3GetObject)
		s.Set(policy.OrganizationsAll)
		Expect(namesIn(s)).To(ConsistOf("s3:GetObject", "organizations:*"))
		Expect(s.Has(policy.S3PutObject)).To(BeFalse(), "s3:PutObject")
		Expect(policy.AllValue().Contains(s)).To(BeTrue())
		Expect(policy.S3AllValue().Has(policy.S3All)).To(BeFalse(), "set_cont_bits(0, s3All) excludes s3All itself")
		Expect(policy.IAMAllValue().Has(policy.IAMPutUserPolicy)).To(BeTrue(), "iam:PutUserPolicy")
		Expect(policy.IAMAllValue().Has(policy.S3ObjectLambdaAll)).To(BeFalse(), "s3-object-lambda:*")

		var o policy.ActionSet
		o.Set(policy.SNSPublish)
		Expect(s.Contains(o)).To(BeFalse(), "before the union")
		s.Union(o)
		Expect(namesIn(s)).To(ConsistOf("s3:GetObject", "organizations:*", "sns:Publish"))
		Expect(s.Contains(o)).To(BeTrue(), "after the union")
		Expect(o.Contains(s)).To(BeFalse(), "the smaller set")
	})
	It("compares sets bit by bit", func() {
		var a, b policy.ActionSet
		a.Set(policy.S3GetObject)
		b.Set(policy.S3GetObject)
		Expect(a.Equal(b)).To(BeTrue(), "the same action")
		b.Set(policy.OrganizationsAll)
		Expect(a.Equal(b)).To(BeFalse(), "one more action, in the last word")
	})
	It("ignores an action at or past ActionCount", func() {
		var s policy.ActionSet
		s.Set(policy.ActionCount)
		s.Set(policy.Action(math.MaxUint16))
		Expect(s.IsZero()).To(BeTrue(), "set %v", namesIn(s))
		Expect(policy.AllValue().Has(policy.ActionCount)).To(BeFalse(), "ActionCount")
		Expect(policy.AllValue().Has(policy.Action(math.MaxUint16))).To(BeFalse(), "0xFFFF")
	})
	DescribeTable("a service's All value is set_cont_bits over its actions, its wildcard left out",
		func(value func() policy.ActionSet, service string) {
			v := value()
			for a := range policy.ActionCount {
				s, name, _ := strings.Cut(a.String(), ":")
				Expect(v.Has(a)).To(Equal(s == service && name != "*"), "%s", a)
			}
		},
		// src/rgw/rgw_iam_policy.h:226-231 at v19.2.6, :236-241 at v20.2.4.
		Entry("s3", policy.S3AllValue, "s3"),
		Entry("s3-object-lambda", policy.S3ObjectLambdaAllValue, "s3-object-lambda"),
		Entry("iam", policy.IAMAllValue, "iam"),
		Entry("sts", policy.STSAllValue, "sts"),
		Entry("sns", policy.SNSAllValue, "sns"),
		Entry("organizations", policy.OrganizationsAllValue, "organizations"),
	)
	It("has every action in AllValue, the wildcards included", func() {
		v := policy.AllValue()
		for a := range policy.ActionCount {
			Expect(v.Has(a)).To(BeTrue(), "%s", a)
		}
	})
})
