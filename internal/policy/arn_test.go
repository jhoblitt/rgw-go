package policy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("ARN", func() {
	parsed := func(s string, wildcards bool) policy.ARN {
		a, ok := policy.ParseARN(s, wildcards)
		Expect(ok).To(BeTrue(), "ParseARN(%q, %t)", s, wildcards)
		return a
	}

	DescribeTable("ParseARN takes what ARN::parse takes",
		func(s string, wildcards bool, want policy.ARN, rendered string) {
			got := parsed(s, wildcards)
			Expect(got).To(Equal(want), "ParseARN(%q, %t)", s, wildcards)
			Expect(got.String()).To(Equal(rendered), "String of ParseARN(%q, %t)", s, wildcards)
		},
		Entry("a bucket", "arn:aws:s3:::example_bucket", true,
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceS3, Resource: "example_bucket"},
			"arn:aws:s3:::example_bucket"),
		Entry("* alone, with wildcards", "*", true,
			policy.ARN{Partition: policy.PartitionWildcard, Service: policy.ServiceWildcard, Region: "*", Account: "*", Resource: "*"},
			"arn:*:*:*:*:*"),
		Entry("every field a wildcard", "arn:*:*:*:*:*", true,
			policy.ARN{Partition: policy.PartitionWildcard, Service: policy.ServiceWildcard, Region: "*", Account: "*", Resource: "*"},
			"arn:*:*:*:*:*"),
		Entry("wildcards inside region, account and resource", "arn:aws:s3:us-*:t*:b/*", true,
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceS3, Region: "us-*", Account: "t*", Resource: "b/*"},
			"arn:aws:s3:us-*:t*:b/*"),
		Entry("a colon in the resource, without wildcards", "arn:aws:s3:::b/k:x", false,
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceS3, Resource: "b/k:x"},
			"arn:aws:s3:::b/k:x"),
		Entry("a * in the resource, without wildcards", "arn:aws:s3:::b/*", false,
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceS3, Resource: "b/*"},
			"arn:aws:s3:::b/*"),
		Entry("an IAM user, without wildcards", "arn:aws:iam::tenant:user/A", false,
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceIAM, Account: "tenant", Resource: "user/A"},
			"arn:aws:iam::tenant:user/A"),
		Entry("the aws-cn partition", "arn:aws-cn:s3:::x", true,
			policy.ARN{Partition: policy.PartitionAWSCN, Service: policy.ServiceS3, Resource: "x"},
			"arn:aws-cn:s3:::x"),
		Entry("the aws-us-gov partition", "arn:aws-us-gov:s3:::x", false,
			policy.ARN{Partition: policy.PartitionAWSUSGov, Service: policy.ServiceS3, Resource: "x"},
			"arn:aws-us-gov:s3:::x"),
		Entry("a hyphenated service", "arn:aws:aws-marketplace-management:r:a:x", true,
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceAWSMarketplaceManagement, Region: "r", Account: "a", Resource: "x"},
			"arn:aws:aws-marketplace-management:r:a:x"),
		Entry("a newline in the resource, with wildcards", "arn:aws:iam::t:user/a\nb", true,
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceIAM, Account: "t", Resource: "user/a\nb"},
			"arn:aws:iam::t:user/a\nb"),
		Entry("an empty resource", "arn:aws:s3:::", false,
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceS3},
			"arn:aws:s3:::"),
	)

	DescribeTable("ParseARN refuses what ARN::parse refuses",
		func(s string, wildcards bool) {
			_, ok := policy.ParseARN(s, wildcards)
			Expect(ok).To(BeFalse(), "ParseARN(%q, %t)", s, wildcards)
		},
		Entry("* alone, without wildcards", "*", false),
		Entry("a colon in the resource, with wildcards", "arn:aws:s3:::b/k:x", true),
		Entry("an unknown service", "arn:aws:nope:::x", true),
		Entry("an unknown partition", "arn:aws-eu:s3:::x", true),
		Entry("a service in another case", "arn:aws:S3:::x", true),
		Entry("a partial wildcard partition", "arn:aw*:s3:::x", true),
		Entry("a partial wildcard service", "arn:aws:s*:::x", true),
		Entry("a wildcard partition, without wildcards", "arn:*:s3:::x", false),
		Entry("a wildcard service, without wildcards", "arn:aws:*:::x", false),
		Entry("a wildcard region, without wildcards", "arn:aws:s3:*::x", false),
		Entry("a wildcard account, without wildcards", "arn:aws:s3::*:x", false),
		// libstdc++'s ECMAScript '.' matches neither '\n' nor '\r'.
		Entry("a newline in the resource, without wildcards", "arn:aws:iam::t:user/a\nb", false),
		Entry("a carriage return in the resource, without wildcards", "arn:aws:iam::t:user/a\rb", false),
		Entry("too few fields", "arn:aws:s3::b", true),
		Entry("no arn prefix", "urn:aws:s3:::b", true),
		Entry("text before the arn prefix", " arn:aws:s3:::b", false),
		Entry("the empty string", "", true),
	)

	It("knows radosgw's 79 services by their names", func() {
		Expect(int(policy.ServiceWildcard)).To(Equal(79))
		for s := range policy.ServiceWildcard {
			a := policy.ARN{Partition: policy.PartitionAWS, Service: s, Resource: "r"}
			Expect(parsed(a.String(), false)).To(Equal(a), "service %d", s)
		}
	})

	It("renders a partition or service it has no name for as *", func() {
		a := policy.ARN{Partition: policy.PartitionWildcard + 1, Service: policy.ServiceWildcard + 1, Resource: "r"}
		Expect(a.String()).To(Equal("arn:*:*:::r"))
	})

	DescribeTable("the constructors build radosgw's ARNs",
		func(a policy.ARN, want string) {
			Expect(a.String()).To(Equal(want))
		},
		Entry("a bucket", policy.BucketARN("t", "b"), "arn:aws:s3::t:b"),
		Entry("an object", policy.ObjectARN("", "b", "k"), "arn:aws:s3:::b/k"),
		Entry("an object in a tenant", policy.ObjectARN("t", "b", "k/l"), "arn:aws:s3::t:b/k/l"),
		Entry("an IAM resource", policy.IAMARN("r", "role", "t", false), "arn:aws:iam::t:role/r"),
		Entry("an IAM resource whose name carries its path", policy.IAMARN("/eng/r", "role", "t", true), "arn:aws:iam::t:role/eng/r"),
	)

	DescribeTable("Match is ARN::match",
		func(pattern string, candidate policy.ARN, want bool) {
			Expect(parsed(pattern, true).Match(candidate)).To(Equal(want), "%q against %q", pattern, candidate.String())
		},
		Entry("a resource wildcard", "arn:aws:s3:::b/*", policy.ObjectARN("", "b", "k/l"), true),
		Entry("the exact bucket", "arn:aws:s3:::b", policy.BucketARN("", "b"), true),
		Entry("a bucket pattern against an object", "arn:aws:s3:::b", policy.ObjectARN("", "b", "k"), false),
		Entry("an account wildcard against a tenant", "arn:aws:s3::*:b", policy.BucketARN("t", "b"), true),
		Entry("an empty account against a tenant", "arn:aws:s3:::b", policy.BucketARN("t", "b"), false),
		Entry("an account in another case", "arn:aws:s3::T:b", policy.BucketARN("t", "b"), true),
		Entry("a region in another case", "arn:aws:s3:US-*::b",
			policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceS3, Region: "us-east-1", Resource: "b"}, true),
		Entry("a resource in another case", "arn:aws:s3:::b/*", policy.ObjectARN("", "B", "k"), false),
		Entry("* against an object", "*", policy.ObjectARN("t", "b", "k"), true),
		Entry("a wildcard partition", "arn:*:s3:::b", policy.ARN{Partition: policy.PartitionAWSCN, Service: policy.ServiceS3, Resource: "b"}, true),
		Entry("another partition", "arn:aws:s3:::b", policy.ARN{Partition: policy.PartitionAWSCN, Service: policy.ServiceS3, Resource: "b"}, false),
		Entry("a wildcard service", "arn:aws:*:::b", policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceIAM, Resource: "b"}, true),
		Entry("another service", "arn:aws:s3:::b", policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceIAM, Resource: "b"}, false),
		Entry("a candidate with a wildcard partition", "*", policy.ARN{Partition: policy.PartitionWildcard, Service: policy.ServiceS3, Resource: "b"}, false),
		Entry("a candidate with a wildcard service", "*", policy.ARN{Partition: policy.PartitionAWS, Service: policy.ServiceWildcard, Resource: "b"}, false),
	)
})
