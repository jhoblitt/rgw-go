package policy

import "github.com/jhoblitt/rgw-go/internal/denc"

// The AWS managed policies radosgw builds in, the raw strings of
// src/rgw/rgw_iam_managed_policy.cc:26-154 (the same at v19.2.6 and v20.2.4)
// byte for byte: each starts with the newline after its raw string's opening
// delimiter.
const (
	iamFullAccessText = `
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Effect" : "Allow",
      "Action" : [
        "iam:*",
        "organizations:DescribeAccount",
        "organizations:DescribeOrganization",
        "organizations:DescribeOrganizationalUnit",
        "organizations:DescribePolicy",
        "organizations:ListChildren",
        "organizations:ListParents",
        "organizations:ListPoliciesForTarget",
        "organizations:ListRoots",
        "organizations:ListPolicies",
        "organizations:ListTargetsForPolicy"
      ],
      "Resource" : "*"
    }
  ]
}`
	iamReadOnlyAccessText = `
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Effect" : "Allow",
      "Action" : [
        "iam:GenerateCredentialReport",
        "iam:GenerateServiceLastAccessedDetails",
        "iam:Get*",
        "iam:List*",
        "iam:SimulateCustomPolicy",
        "iam:SimulatePrincipalPolicy"
      ],
      "Resource" : "*"
    }
  ]
}`
	amazonSNSFullAccessText = `
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Action" : [
        "sns:*"
      ],
      "Effect" : "Allow",
      "Resource" : "*"
    }
  ]
}`
	amazonSNSReadOnlyAccessText = `
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Effect" : "Allow",
      "Action" : [
        "sns:GetTopicAttributes",
        "sns:List*"
      ],
      "Resource" : "*"
    }
  ]
}`
	amazonS3FullAccessText = `
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Effect" : "Allow",
      "Action" : [
        "s3:*",
        "s3-object-lambda:*"
      ],
      "Resource" : "*"
    }
  ]
}`
	amazonS3ReadOnlyAccessText = `
{
  "Version" : "2012-10-17",
  "Statement" : [
    {
      "Effect" : "Allow",
      "Action" : [
        "s3:Get*",
        "s3:List*",
        "s3:Describe*",
        "s3-object-lambda:Get*",
        "s3-object-lambda:List*"
      ],
      "Resource" : "*"
    }
  ]
}`
)

// managedPolicies is get_managed_policy's table
// (src/rgw/rgw_iam_managed_policy.cc:156-175).
var managedPolicies = map[string]string{
	"arn:aws:iam::aws:policy/IAMFullAccess":           iamFullAccessText,
	"arn:aws:iam::aws:policy/IAMReadOnlyAccess":       iamReadOnlyAccessText,
	"arn:aws:iam::aws:policy/AmazonSNSFullAccess":     amazonSNSFullAccessText,
	"arn:aws:iam::aws:policy/AmazonSNSReadOnlyAccess": amazonSNSReadOnlyAccessText,
	"arn:aws:iam::aws:policy/AmazonS3FullAccess":      amazonS3FullAccessText,
	"arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess":  amazonS3ReadOnlyAccessText,
}

// ManagedPolicy is get_managed_policy (src/rgw/rgw_iam_managed_policy.cc:
// 156-175): the built-in policy an AWS managed-policy ARN names, parsed on
// r with no tenant and invalid principals dropped, and false for any other
// ARN. Each call parses afresh, so callers do not share a Policy.
func ManagedPolicy(arn string, r denc.Release) (*Policy, bool) {
	text, ok := managedPolicies[arn]
	if !ok {
		return nil, false
	}
	p, err := Parse(text, ParseOptions{Release: r})
	if err != nil {
		panic("policy: built-in managed policy " + arn + " does not parse: " + err.Error())
	}
	return p, true
}
