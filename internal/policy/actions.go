package policy

import "github.com/jhoblitt/rgw-go/internal/denc"

// The actions after the s3 block, in action_t order
// (src/rgw/rgw_iam_policy.h:119-206 at v19.2.6, :126-216 at v20.2.4). Each
// service ends in its All, the "<service>:*" wildcard. The iam block is
// v20.2.4's, which added the three OIDC provider actions.
const (
	S3ObjectLambdaGetObject Action = s3AllCount + iota
	S3ObjectLambdaListBucket
	S3ObjectLambdaAll
	IAMPutUserPolicy
	IAMGetUserPolicy
	IAMDeleteUserPolicy
	IAMListUserPolicies
	IAMAttachUserPolicy
	IAMDetachUserPolicy
	IAMListAttachedUserPolicies
	IAMCreateRole
	IAMDeleteRole
	IAMModifyRoleTrustPolicy
	IAMGetRole
	IAMListRoles
	IAMPutRolePolicy
	IAMGetRolePolicy
	IAMListRolePolicies
	IAMDeleteRolePolicy
	IAMAttachRolePolicy
	IAMDetachRolePolicy
	IAMListAttachedRolePolicies
	IAMCreateOIDCProvider
	IAMDeleteOIDCProvider
	IAMGetOIDCProvider
	IAMListOIDCProviders
	IAMAddClientIDToOIDCProvider
	IAMRemoveClientIDFromOIDCProvider
	IAMUpdateOIDCProviderThumbprint
	IAMTagRole
	IAMListRoleTags
	IAMUntagRole
	IAMUpdateRole
	IAMCreateUser
	IAMGetUser
	IAMUpdateUser
	IAMDeleteUser
	IAMListUsers
	IAMCreateAccessKey
	IAMUpdateAccessKey
	IAMDeleteAccessKey
	IAMListAccessKeys
	IAMCreateGroup
	IAMGetGroup
	IAMUpdateGroup
	IAMDeleteGroup
	IAMListGroups
	IAMAddUserToGroup
	IAMRemoveUserFromGroup
	IAMListGroupsForUser
	IAMPutGroupPolicy
	IAMGetGroupPolicy
	IAMListGroupPolicies
	IAMDeleteGroupPolicy
	IAMAttachGroupPolicy
	IAMDetachGroupPolicy
	IAMListAttachedGroupPolicies
	IAMGenerateCredentialReport
	IAMGenerateServiceLastAccessedDetails
	IAMSimulateCustomPolicy
	IAMSimulatePrincipalPolicy
	IAMAll
	STSAssumeRole
	STSAssumeRoleWithWebIdentity
	STSGetSessionToken
	STSTagSession
	STSAll
	SNSGetTopicAttributes
	SNSDeleteTopic
	SNSPublish
	SNSSetTopicAttributes
	SNSCreateTopic
	SNSListTopics
	SNSAll
	OrganizationsDescribeAccount
	OrganizationsDescribeOrganization
	OrganizationsDescribeOrganizationalUnit
	OrganizationsDescribePolicy
	OrganizationsListChildren
	OrganizationsListParents
	OrganizationsListPoliciesForTarget
	OrganizationsListRoots
	OrganizationsListPolicies
	OrganizationsListTargetsForPolicy
	OrganizationsAll
	// ActionCount is allCount, one past the last action.
	ActionCount
)

// Known reports whether the radosgw of release r accepts a in a policy: whether
// a has a row in that release's actpairs table (src/rgw/rgw_iam_policy.cc:64-215
// at v19.2.6, :64-225 at v20.2.4). The All wildcards have no row but are known
// on every release, since "*" and "<service>:*" set them. A value that names
// no action is known on none.
func Known(a Action, r denc.Release) bool {
	switch a {
	case S3PutAccountPublicAccessBlock, S3GetAccountPublicAccessBlock:
		return false
	case S3PostBucketLogging, S3GetObjectAttributes, S3GetObjectVersionAttributes,
		S3ReplicateDelete, S3ReplicateObject, S3GetObjectVersionForReplication, S3ReplicateTags,
		IAMAddClientIDToOIDCProvider, IAMRemoveClientIDFromOIDCProvider, IAMUpdateOIDCProviderThumbprint:
		return r >= denc.Tentacle
	}
	return a < ActionCount
}
