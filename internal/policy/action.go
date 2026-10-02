package policy

// Action is one IAM action, numbered as rgw::IAM::action_t numbers them so a
// policy's action bitset lines up with radosgw's.
type Action uint16

// The S3 actions, in action_t order (src/rgw/rgw_iam_policy.h:46-129 at ceph
// main 7ed73efc1be). The block is the union of both release floors and main:
// Squid lacks the seven Tentacle added (PostBucketLogging, the two
// GetObject*Attributes, the four Replicate* ones), and neither floor knows
// the two Account*PublicAccessBlock actions. The numbering is never
// persisted, so the superset is harmless; Known says which actions a release
// knows.
const (
	S3GetObject Action = iota
	S3GetObjectVersion
	S3PutObject
	S3GetObjectAcl        //nolint:revive,staticcheck // radosgw's action name spells it Acl
	S3GetObjectVersionAcl //nolint:revive,staticcheck // radosgw's action name spells it Acl
	S3PutObjectAcl        //nolint:revive,staticcheck // radosgw's action name spells it Acl
	S3PutObjectVersionAcl //nolint:revive,staticcheck // radosgw's action name spells it Acl
	S3DeleteObject
	S3DeleteObjectVersion
	S3ListMultipartUploadParts
	S3AbortMultipartUpload
	S3GetObjectTorrent
	S3GetObjectVersionTorrent
	S3RestoreObject
	S3CreateBucket
	S3DeleteBucket
	S3ListBucket
	S3ListBucketVersions
	S3ListAllMyBuckets
	S3ListBucketMultipartUploads
	S3GetAccelerateConfiguration
	S3PutAccelerateConfiguration
	S3GetBucketAcl //nolint:revive,staticcheck // radosgw's action name spells it Acl
	S3PutBucketAcl //nolint:revive,staticcheck // radosgw's action name spells it Acl
	S3GetBucketOwnershipControls
	S3PutBucketOwnershipControls
	S3GetBucketCORS
	S3PutBucketCORS
	S3GetBucketVersioning
	S3PutBucketVersioning
	S3GetBucketRequestPayment
	S3PutBucketRequestPayment
	S3GetBucketLocation
	S3GetBucketPolicy
	S3DeleteBucketPolicy
	S3PutBucketPolicy
	S3GetBucketNotification
	S3PutBucketNotification
	S3GetBucketLogging
	S3PutBucketLogging
	S3PostBucketLogging
	S3GetBucketTagging
	S3PutBucketTagging
	S3GetBucketWebsite
	S3PutBucketWebsite
	S3DeleteBucketWebsite
	S3GetLifecycleConfiguration
	S3PutLifecycleConfiguration
	S3PutReplicationConfiguration
	S3GetReplicationConfiguration
	S3DeleteReplicationConfiguration
	S3GetObjectTagging
	S3PutObjectTagging
	S3DeleteObjectTagging
	S3GetObjectVersionTagging
	S3PutObjectVersionTagging
	S3DeleteObjectVersionTagging
	S3PutBucketObjectLockConfiguration
	S3GetBucketObjectLockConfiguration
	S3PutObjectRetention
	S3GetObjectRetention
	S3PutObjectLegalHold
	S3GetObjectLegalHold
	S3BypassGovernanceRetention
	S3GetBucketPolicyStatus
	S3PutPublicAccessBlock
	S3GetPublicAccessBlock
	S3DeletePublicAccessBlock
	S3GetBucketPublicAccessBlock
	S3PutBucketPublicAccessBlock
	S3DeleteBucketPublicAccessBlock
	S3GetBucketEncryption
	S3PutBucketEncryption
	S3DescribeJob
	S3GetObjectAttributes
	S3GetObjectVersionAttributes
	S3ReplicateDelete
	S3ReplicateObject
	S3GetObjectVersionForReplication
	S3ReplicateTags
	S3PutAccountPublicAccessBlock
	S3GetAccountPublicAccessBlock
	S3All
	// s3AllCount is where the next service's actions start.
	s3AllCount
)

// ActionNone is the action of an op radosgw's IAM never evaluates: the admin
// API's ops, which radosgw authorizes by user caps (RGWRESTOp::verify_permission,
// rgw_rest.cc:1687-1690 at v19.2.6, :1691-1694 at v20.2.4). It lies past
// ActionCount, so String renders it as "", ParseAction never returns it,
// Known is false for it and no ActionSet holds it.
const ActionNone Action = 0xFFFF

// actionNames is actpairs (rgw_iam_policy.cc) inverted. An s3 action's name is
// "s3:" and the action_t name without its service prefix; the other services'
// names are spelled as actpairs spells them, which is not always as the
// constants are ("iam:AddClientIdToOIDCProvider", and v20.2.4's misspelled
// "iam:RemoveCientIdFromOIDCProvider"). Each service's All is "<service>:*".
var actionNames = [ActionCount]string{ //nolint:gosec // G101 takes the action names for credentials
	S3GetObject:                        "s3:GetObject",
	S3GetObjectVersion:                 "s3:GetObjectVersion",
	S3PutObject:                        "s3:PutObject",
	S3GetObjectAcl:                     "s3:GetObjectAcl",
	S3GetObjectVersionAcl:              "s3:GetObjectVersionAcl",
	S3PutObjectAcl:                     "s3:PutObjectAcl",
	S3PutObjectVersionAcl:              "s3:PutObjectVersionAcl",
	S3DeleteObject:                     "s3:DeleteObject",
	S3DeleteObjectVersion:              "s3:DeleteObjectVersion",
	S3ListMultipartUploadParts:         "s3:ListMultipartUploadParts",
	S3AbortMultipartUpload:             "s3:AbortMultipartUpload",
	S3GetObjectTorrent:                 "s3:GetObjectTorrent",
	S3GetObjectVersionTorrent:          "s3:GetObjectVersionTorrent",
	S3RestoreObject:                    "s3:RestoreObject",
	S3CreateBucket:                     "s3:CreateBucket",
	S3DeleteBucket:                     "s3:DeleteBucket",
	S3ListBucket:                       "s3:ListBucket",
	S3ListBucketVersions:               "s3:ListBucketVersions",
	S3ListAllMyBuckets:                 "s3:ListAllMyBuckets",
	S3ListBucketMultipartUploads:       "s3:ListBucketMultipartUploads",
	S3GetAccelerateConfiguration:       "s3:GetAccelerateConfiguration",
	S3PutAccelerateConfiguration:       "s3:PutAccelerateConfiguration",
	S3GetBucketAcl:                     "s3:GetBucketAcl",
	S3PutBucketAcl:                     "s3:PutBucketAcl",
	S3GetBucketOwnershipControls:       "s3:GetBucketOwnershipControls",
	S3PutBucketOwnershipControls:       "s3:PutBucketOwnershipControls",
	S3GetBucketCORS:                    "s3:GetBucketCORS",
	S3PutBucketCORS:                    "s3:PutBucketCORS",
	S3GetBucketVersioning:              "s3:GetBucketVersioning",
	S3PutBucketVersioning:              "s3:PutBucketVersioning",
	S3GetBucketRequestPayment:          "s3:GetBucketRequestPayment",
	S3PutBucketRequestPayment:          "s3:PutBucketRequestPayment",
	S3GetBucketLocation:                "s3:GetBucketLocation",
	S3GetBucketPolicy:                  "s3:GetBucketPolicy",
	S3DeleteBucketPolicy:               "s3:DeleteBucketPolicy",
	S3PutBucketPolicy:                  "s3:PutBucketPolicy",
	S3GetBucketNotification:            "s3:GetBucketNotification",
	S3PutBucketNotification:            "s3:PutBucketNotification",
	S3GetBucketLogging:                 "s3:GetBucketLogging",
	S3PutBucketLogging:                 "s3:PutBucketLogging",
	S3PostBucketLogging:                "s3:PostBucketLogging",
	S3GetBucketTagging:                 "s3:GetBucketTagging",
	S3PutBucketTagging:                 "s3:PutBucketTagging",
	S3GetBucketWebsite:                 "s3:GetBucketWebsite",
	S3PutBucketWebsite:                 "s3:PutBucketWebsite",
	S3DeleteBucketWebsite:              "s3:DeleteBucketWebsite",
	S3GetLifecycleConfiguration:        "s3:GetLifecycleConfiguration",
	S3PutLifecycleConfiguration:        "s3:PutLifecycleConfiguration",
	S3PutReplicationConfiguration:      "s3:PutReplicationConfiguration",
	S3GetReplicationConfiguration:      "s3:GetReplicationConfiguration",
	S3DeleteReplicationConfiguration:   "s3:DeleteReplicationConfiguration",
	S3GetObjectTagging:                 "s3:GetObjectTagging",
	S3PutObjectTagging:                 "s3:PutObjectTagging",
	S3DeleteObjectTagging:              "s3:DeleteObjectTagging",
	S3GetObjectVersionTagging:          "s3:GetObjectVersionTagging",
	S3PutObjectVersionTagging:          "s3:PutObjectVersionTagging",
	S3DeleteObjectVersionTagging:       "s3:DeleteObjectVersionTagging",
	S3PutBucketObjectLockConfiguration: "s3:PutBucketObjectLockConfiguration",
	S3GetBucketObjectLockConfiguration: "s3:GetBucketObjectLockConfiguration",
	S3PutObjectRetention:               "s3:PutObjectRetention",
	S3GetObjectRetention:               "s3:GetObjectRetention",
	S3PutObjectLegalHold:               "s3:PutObjectLegalHold",
	S3GetObjectLegalHold:               "s3:GetObjectLegalHold",
	S3BypassGovernanceRetention:        "s3:BypassGovernanceRetention",
	S3GetBucketPolicyStatus:            "s3:GetBucketPolicyStatus",
	S3PutPublicAccessBlock:             "s3:PutPublicAccessBlock",
	S3GetPublicAccessBlock:             "s3:GetPublicAccessBlock",
	S3DeletePublicAccessBlock:          "s3:DeletePublicAccessBlock",
	S3GetBucketPublicAccessBlock:       "s3:GetBucketPublicAccessBlock",
	S3PutBucketPublicAccessBlock:       "s3:PutBucketPublicAccessBlock",
	S3DeleteBucketPublicAccessBlock:    "s3:DeleteBucketPublicAccessBlock",
	S3GetBucketEncryption:              "s3:GetBucketEncryption",
	S3PutBucketEncryption:              "s3:PutBucketEncryption",
	S3DescribeJob:                      "s3:DescribeJob",
	S3GetObjectAttributes:              "s3:GetObjectAttributes",
	S3GetObjectVersionAttributes:       "s3:GetObjectVersionAttributes",
	S3ReplicateDelete:                  "s3:ReplicateDelete",
	S3ReplicateObject:                  "s3:ReplicateObject",
	S3GetObjectVersionForReplication:   "s3:GetObjectVersionForReplication",
	S3ReplicateTags:                    "s3:ReplicateTags",
	S3PutAccountPublicAccessBlock:      "s3:PutAccountPublicAccessBlock",
	S3GetAccountPublicAccessBlock:      "s3:GetAccountPublicAccessBlock",
	S3All:                              "s3:*",

	S3ObjectLambdaGetObject:                 "s3-object-lambda:GetObject",
	S3ObjectLambdaListBucket:                "s3-object-lambda:ListBucket",
	S3ObjectLambdaAll:                       "s3-object-lambda:*",
	IAMPutUserPolicy:                        "iam:PutUserPolicy",
	IAMGetUserPolicy:                        "iam:GetUserPolicy",
	IAMDeleteUserPolicy:                     "iam:DeleteUserPolicy",
	IAMListUserPolicies:                     "iam:ListUserPolicies",
	IAMAttachUserPolicy:                     "iam:AttachUserPolicy",
	IAMDetachUserPolicy:                     "iam:DetachUserPolicy",
	IAMListAttachedUserPolicies:             "iam:ListAttachedUserPolicies",
	IAMCreateRole:                           "iam:CreateRole",
	IAMDeleteRole:                           "iam:DeleteRole",
	IAMModifyRoleTrustPolicy:                "iam:ModifyRoleTrustPolicy",
	IAMGetRole:                              "iam:GetRole",
	IAMListRoles:                            "iam:ListRoles",
	IAMPutRolePolicy:                        "iam:PutRolePolicy",
	IAMGetRolePolicy:                        "iam:GetRolePolicy",
	IAMListRolePolicies:                     "iam:ListRolePolicies",
	IAMDeleteRolePolicy:                     "iam:DeleteRolePolicy",
	IAMAttachRolePolicy:                     "iam:AttachRolePolicy",
	IAMDetachRolePolicy:                     "iam:DetachRolePolicy",
	IAMListAttachedRolePolicies:             "iam:ListAttachedRolePolicies",
	IAMCreateOIDCProvider:                   "iam:CreateOIDCProvider",
	IAMDeleteOIDCProvider:                   "iam:DeleteOIDCProvider",
	IAMGetOIDCProvider:                      "iam:GetOIDCProvider",
	IAMListOIDCProviders:                    "iam:ListOIDCProviders",
	IAMAddClientIDToOIDCProvider:            "iam:AddClientIdToOIDCProvider",
	IAMRemoveClientIDFromOIDCProvider:       "iam:RemoveCientIdFromOIDCProvider",
	IAMUpdateOIDCProviderThumbprint:         "iam:UpdateOIDCProviderThumbprint",
	IAMTagRole:                              "iam:TagRole",
	IAMListRoleTags:                         "iam:ListRoleTags",
	IAMUntagRole:                            "iam:UntagRole",
	IAMUpdateRole:                           "iam:UpdateRole",
	IAMCreateUser:                           "iam:CreateUser",
	IAMGetUser:                              "iam:GetUser",
	IAMUpdateUser:                           "iam:UpdateUser",
	IAMDeleteUser:                           "iam:DeleteUser",
	IAMListUsers:                            "iam:ListUsers",
	IAMCreateAccessKey:                      "iam:CreateAccessKey",
	IAMUpdateAccessKey:                      "iam:UpdateAccessKey",
	IAMDeleteAccessKey:                      "iam:DeleteAccessKey",
	IAMListAccessKeys:                       "iam:ListAccessKeys",
	IAMCreateGroup:                          "iam:CreateGroup",
	IAMGetGroup:                             "iam:GetGroup",
	IAMUpdateGroup:                          "iam:UpdateGroup",
	IAMDeleteGroup:                          "iam:DeleteGroup",
	IAMListGroups:                           "iam:ListGroups",
	IAMAddUserToGroup:                       "iam:AddUserToGroup",
	IAMRemoveUserFromGroup:                  "iam:RemoveUserFromGroup",
	IAMListGroupsForUser:                    "iam:ListGroupsForUser",
	IAMPutGroupPolicy:                       "iam:PutGroupPolicy",
	IAMGetGroupPolicy:                       "iam:GetGroupPolicy",
	IAMListGroupPolicies:                    "iam:ListGroupPolicies",
	IAMDeleteGroupPolicy:                    "iam:DeleteGroupPolicy",
	IAMAttachGroupPolicy:                    "iam:AttachGroupPolicy",
	IAMDetachGroupPolicy:                    "iam:DetachGroupPolicy",
	IAMListAttachedGroupPolicies:            "iam:ListAttachedGroupPolicies",
	IAMGenerateCredentialReport:             "iam:GenerateCredentialReport",
	IAMGenerateServiceLastAccessedDetails:   "iam:GenerateServiceLastAccessedDetails",
	IAMSimulateCustomPolicy:                 "iam:SimulateCustomPolicy",
	IAMSimulatePrincipalPolicy:              "iam:SimulatePrincipalPolicy",
	IAMAll:                                  "iam:*",
	STSAssumeRole:                           "sts:AssumeRole",
	STSAssumeRoleWithWebIdentity:            "sts:AssumeRoleWithWebIdentity",
	STSGetSessionToken:                      "sts:GetSessionToken",
	STSTagSession:                           "sts:TagSession",
	STSAll:                                  "sts:*",
	SNSGetTopicAttributes:                   "sns:GetTopicAttributes",
	SNSDeleteTopic:                          "sns:DeleteTopic",
	SNSPublish:                              "sns:Publish",
	SNSSetTopicAttributes:                   "sns:SetTopicAttributes",
	SNSCreateTopic:                          "sns:CreateTopic",
	SNSListTopics:                           "sns:ListTopics",
	SNSAll:                                  "sns:*",
	OrganizationsDescribeAccount:            "organizations:DescribeAccount",
	OrganizationsDescribeOrganization:       "organizations:DescribeOrganization",
	OrganizationsDescribeOrganizationalUnit: "organizations:DescribeOrganizationalUnit",
	OrganizationsDescribePolicy:             "organizations:DescribePolicy",
	OrganizationsListChildren:               "organizations:ListChildren",
	OrganizationsListParents:                "organizations:ListParents",
	OrganizationsListPoliciesForTarget:      "organizations:ListPoliciesForTarget",
	OrganizationsListRoots:                  "organizations:ListRoots",
	OrganizationsListPolicies:               "organizations:ListPolicies",
	OrganizationsListTargetsForPolicy:       "organizations:ListTargetsForPolicy",
	OrganizationsAll:                        "organizations:*",
}

// actionsByName inverts actionNames for ParseAction.
var actionsByName = func() map[string]Action {
	m := make(map[string]Action, len(actionNames))
	for a, name := range actionNames {
		m[name] = Action(a)
	}
	return m
}()

// String returns the "service:Name" form, "service:*" for a service's All,
// and "" for a value that names no action.
func (a Action) String() string {
	if int(a) >= len(actionNames) {
		return ""
	}
	return actionNames[a]
}

// ParseAction parses the "service:Name" form, exactly as String renders it; ok
// is false for a name no action has. Known says whether a release accepts it.
func ParseAction(s string) (a Action, ok bool) {
	a, ok = actionsByName[s]
	return a, ok
}
