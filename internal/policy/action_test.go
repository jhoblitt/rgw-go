package policy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("Action", func() {
	DescribeTable("renders the name radosgw's actpairs table gives it",
		func(a policy.Action, want string) {
			Expect(a.String()).To(Equal(want))
		},
		Entry("the first action", policy.S3GetObject, "s3:GetObject"),
		Entry("an acronym kept as radosgw spells it", policy.S3GetBucketCORS, "s3:GetBucketCORS"),
		Entry("a Tentacle action", policy.S3GetObjectVersionForReplication, "s3:GetObjectVersionForReplication"),
		Entry("the last named action, from ceph main", policy.S3GetAccountPublicAccessBlock, "s3:GetAccountPublicAccessBlock"),
		Entry("the wildcard", policy.S3All, "s3:*"),
	)
	It("numbers the actions as rgw::IAM::action_t does", func() {
		Expect(policy.S3GetObject).To(BeEquivalentTo(0))
		Expect(policy.S3CreateBucket).To(BeEquivalentTo(14))
		Expect(policy.S3PostBucketLogging).To(BeEquivalentTo(40))
		Expect(policy.S3All).To(BeEquivalentTo(82))
	})
	It("parses a name radosgw knows", func() {
		a, ok := policy.ParseAction("s3:ListBucket")
		Expect(ok).To(BeTrue(), "s3:ListBucket")
		Expect(a).To(Equal(policy.S3ListBucket))
	})
	DescribeTable("refuses a name radosgw does not know",
		func(s string) {
			a, ok := policy.ParseAction(s)
			Expect(ok).To(BeFalse(), "%q parsed as %v", s, a)
		},
		Entry("an unknown action", "s3:Nope"),
		Entry("a name without its service", "GetObject"),
		Entry("the empty string", ""),
	)
	It("round-trips every s3 action through its name", func() {
		for a := policy.S3GetObject; a <= policy.S3All; a++ {
			got, ok := policy.ParseAction(a.String())
			Expect(ok).To(BeTrue(), "action %d, %q", a, a.String())
			Expect(got).To(Equal(a), "action %d, %q", a, a.String())
		}
	})
	It("renders no name for a value past the table", func() {
		Expect(policy.ActionCount.String()).To(BeEmpty())
	})
})
