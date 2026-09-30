package acl

import "github.com/jhoblitt/rgw-go/internal/policy"

// The RGW_PERM_* values beyond the S3 four (rgw_acl_types.h:35-40): Swift's
// container read and write, and the value op_to_perm gives an action no ACL
// permission covers.
const (
	PermReadObjs  Permission = 0x10
	PermWriteObjs Permission = 0x20
	PermInvalid   Permission = 0xFF00
)

// PermFor is rgw::IAM::op_to_perm: the ACL permission an action needs when no
// policy decides it, and PermInvalid for an action without one, every non-s3
// action included. The table is v20.2.4's (rgw_iam_policy.h:247-334), which is
// v19.2.6's (:237-317) plus rows for the seven s3 actions only Tentacle knows;
// a Squid radosgw never evaluates those actions, so the one table serves both
// releases.
func PermFor(a policy.Action) Permission {
	switch a {
	case policy.S3GetObject,
		policy.S3GetObjectTorrent,
		policy.S3GetObjectVersion,
		policy.S3GetObjectVersionTorrent,
		policy.S3GetObjectTagging,
		policy.S3GetObjectVersionTagging,
		policy.S3GetObjectRetention,
		policy.S3GetObjectLegalHold,
		policy.S3GetObjectAttributes,
		policy.S3GetObjectVersionAttributes,
		policy.S3ListAllMyBuckets,
		policy.S3ListBucket,
		policy.S3ListBucketMultipartUploads,
		policy.S3ListBucketVersions,
		policy.S3ListMultipartUploadParts,
		policy.S3GetObjectVersionForReplication:
		return PermRead

	case policy.S3AbortMultipartUpload,
		policy.S3CreateBucket,
		policy.S3DeleteBucket,
		policy.S3DeleteObject,
		policy.S3DeleteObjectVersion,
		policy.S3PutObject,
		policy.S3PutObjectTagging,
		policy.S3PutObjectVersionTagging,
		policy.S3DeleteObjectTagging,
		policy.S3DeleteObjectVersionTagging,
		policy.S3RestoreObject,
		policy.S3PutObjectRetention,
		policy.S3PutObjectLegalHold,
		policy.S3BypassGovernanceRetention,
		policy.S3ReplicateDelete,
		policy.S3ReplicateObject,
		policy.S3ReplicateTags:
		return PermWrite

	case policy.S3GetAccelerateConfiguration,
		policy.S3GetBucketAcl,
		policy.S3GetBucketCORS,
		policy.S3GetBucketEncryption,
		policy.S3GetBucketLocation,
		policy.S3GetBucketLogging,
		policy.S3GetBucketNotification,
		policy.S3GetBucketPolicy,
		policy.S3GetBucketPolicyStatus,
		policy.S3GetBucketRequestPayment,
		policy.S3GetBucketTagging,
		policy.S3GetBucketVersioning,
		policy.S3GetBucketWebsite,
		policy.S3GetLifecycleConfiguration,
		policy.S3GetObjectAcl,
		policy.S3GetObjectVersionAcl,
		policy.S3GetReplicationConfiguration,
		policy.S3GetBucketObjectLockConfiguration,
		policy.S3GetBucketPublicAccessBlock:
		return PermReadACP

	case policy.S3DeleteBucketPolicy,
		policy.S3DeleteBucketWebsite,
		policy.S3DeleteReplicationConfiguration,
		policy.S3PutAccelerateConfiguration,
		policy.S3PutBucketAcl,
		policy.S3PutBucketCORS,
		policy.S3PutBucketEncryption,
		policy.S3PutBucketLogging,
		policy.S3PostBucketLogging,
		policy.S3PutBucketNotification,
		policy.S3PutBucketPolicy,
		policy.S3PutBucketRequestPayment,
		policy.S3PutBucketTagging,
		policy.S3PutBucketVersioning,
		policy.S3PutBucketWebsite,
		policy.S3PutLifecycleConfiguration,
		policy.S3PutObjectAcl,
		policy.S3PutObjectVersionAcl,
		policy.S3PutReplicationConfiguration,
		policy.S3PutBucketObjectLockConfiguration,
		policy.S3PutBucketPublicAccessBlock:
		return PermWriteACP

	case policy.S3All:
		return PermFullControl
	}
	return PermInvalid
}
