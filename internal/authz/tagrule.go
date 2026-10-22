package authz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// objectTags is how an op adds an object's tags: as policies need them
// (rgw_check_policy_condition's pair), as s3:ExistingObjectTag alone, or under
// both keys whatever the policies name.
type objectTags uint8

const (
	noObjectTags objectTags = iota
	objectTagsAsNeeded
	objectTagsExisting
	objectTagsAlways
)

// bucketTags is how an op adds its bucket's tags, under s3:ResourceTag: when
// a policy needs them, or always.
type bucketTags uint8

const (
	noBucketTags bucketTags = iota
	bucketTagsAsNeeded
	bucketTagsAlways
)

// tagRule is which tags the op whose check this is adds before it
// authorizes. Each case is a verify_permission that calls
// rgw_iam_add_objtags or rgw_iam_add_buckettags, cited in rgw_op.cc at
// v19.2.6 unless noted; v20.2.4 makes the same calls at its own lines, and
// adds the bucket logging ops of rgw_rest_bucket_logging.cc.
func tagRule(f form, r *op.Request, a policy.Action) (objectTags, bucketTags) {
	switch f {
	case formBucketIn, formObjectIn:
		// The copy sources of RGWCopyObj
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5446-L5448)
		// and RGWPutObj
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3948-L3950);
		// DeleteObjects' per-key check adds none.
		if a == policy.S3GetObject || a == policy.S3GetObjectVersion {
			return objectTagsAsNeeded, noBucketTags
		}
		return noObjectTags, noBucketTags
	case formObject:
		switch a {
		case policy.S3PutObjectTagging, policy.S3PutObjectVersionTagging:
			// RGWPutObjTags
			// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1076-L1087).
			return objectTagsExisting, bucketTagsAsNeeded
		case policy.S3PutObjectAcl, policy.S3PutObjectVersionAcl:
			// RGWPutACLs
			// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5738-L5750).
			return objectTagsAlways, noBucketTags
		}
		if objectTagActions[a] {
			return objectTagsAsNeeded, noBucketTags
		}
		return noObjectTags, noBucketTags
	}
	switch a {
	case policy.S3DeleteObject, policy.S3DeleteObjectVersion, policy.S3AbortMultipartUpload, policy.S3BypassGovernanceRetention:
		// RGWDeleteObj
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5138-L5156),
		// RGWAbortMultipart
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6595-L6599),
		// and RGWDeleteMultiObj
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6768-L6775),
		// whose request names no object.
		return objectTagsAsNeeded, noBucketTags
	case policy.S3PutObject:
		switch {
		case r.Method == http.MethodPut:
			// RGWPutObj
			// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3978-L3980)
			// and RGWCopyObj's destination
			// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5478-L5480).
			return noObjectTags, bucketTagsAsNeeded
		case r.Method == http.MethodPost && (hasQuery(r, "uploads") || hasQuery(r, "uploadId")):
			// RGWInitMultipart
			// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6271-L6275)
			// and RGWCompleteMultipart
			// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6345-L6349).
			return objectTagsAsNeeded, noBucketTags
		}
	case policy.S3PutBucketAcl:
		// RGWPutACLs
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5750).
		return noObjectTags, bucketTagsAlways
	}
	if bucketTagActions[a] {
		return noObjectTags, bucketTagsAsNeeded
	}
	return noObjectTags, noBucketTags
}

// objectTagActions are the actions an object op checks once it has added the
// object's tags as policies need them: RGWGetObj
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L977-L987,
// which at v20.2.4 also checks the GetObjectAcl and
// GetObjectVersionForReplication actions of a replication request,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1132-L1180),
// RGWGetObjTags
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1037-L1045),
// RGWDeleteObjTags
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1117-L1126),
// RGWGetACLs
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5695-L5704),
// RGWListMultipart
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6652-L6656),
// RGWPutObjRetention, whose BypassGovernanceRetention check follows its own
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8321-L8337),
// RGWGetObjRetention
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8424-L8428),
// the legal hold ops
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8473-L8477,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8528-L8532),
// and v20.2.4's RGWGetObjAttrs
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6361-L6379).
var objectTagActions = map[policy.Action]bool{
	policy.S3GetObject: true, policy.S3GetObjectVersion: true,
	policy.S3GetObjectTorrent: true, policy.S3GetObjectVersionTorrent: true,
	policy.S3GetObjectVersionForReplication: true,
	policy.S3GetObjectTagging:               true, policy.S3GetObjectVersionTagging: true,
	policy.S3DeleteObjectTagging: true, policy.S3DeleteObjectVersionTagging: true,
	policy.S3GetObjectAcl: true, policy.S3GetObjectVersionAcl: true,
	policy.S3ListMultipartUploadParts: true,
	policy.S3PutObjectRetention:       true, policy.S3GetObjectRetention: true,
	policy.S3BypassGovernanceRetention: true,
	policy.S3PutObjectLegalHold:        true, policy.S3GetObjectLegalHold: true,
	policy.S3GetObjectAttributes: true, policy.S3GetObjectVersionAttributes: true,
}

// bucketTagActions are the actions a bucket op checks once it has added the
// bucket's tags as policies need them, every bucket op that calls
// rgw_iam_add_buckettags: the tagging, replication, versioning, website,
// logging, location, lifecycle, CORS, request-payment, policy, object-lock,
// policy-status and public-access-block ops, HEAD bucket and the listings
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1141-L1321,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2729-L2942,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2983-L3140,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3709-L3713,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5759-L5791,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6046-L6111,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6206-L6233,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6696-L6700,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8060-L8304,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8582-L8718),
// RGWGetACLs for a bucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5710-L5711),
// and v20.2.4's Get, Put and PostBucketLogging
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_bucket_logging.cc#L53-L56,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_bucket_logging.cc#L197-L208,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_bucket_logging.cc#L401-L406).
// CreateBucket and the encryption ops add none.
var bucketTagActions = map[policy.Action]bool{
	policy.S3GetBucketTagging: true, policy.S3PutBucketTagging: true,
	policy.S3GetReplicationConfiguration: true, policy.S3PutReplicationConfiguration: true,
	policy.S3DeleteReplicationConfiguration: true,
	policy.S3GetBucketVersioning:            true, policy.S3PutBucketVersioning: true,
	policy.S3GetBucketWebsite: true, policy.S3PutBucketWebsite: true, policy.S3DeleteBucketWebsite: true,
	policy.S3ListBucket: true, policy.S3ListBucketVersions: true,
	policy.S3GetBucketLogging: true, policy.S3PutBucketLogging: true, policy.S3PostBucketLogging: true,
	policy.S3GetBucketLocation: true, policy.S3DeleteBucket: true,
	policy.S3GetBucketAcl:              true,
	policy.S3GetLifecycleConfiguration: true, policy.S3PutLifecycleConfiguration: true,
	policy.S3GetBucketCORS: true, policy.S3PutBucketCORS: true,
	policy.S3GetBucketRequestPayment: true, policy.S3PutBucketRequestPayment: true,
	policy.S3ListBucketMultipartUploads: true,
	policy.S3PutBucketPolicy:            true, policy.S3GetBucketPolicy: true, policy.S3DeleteBucketPolicy: true,
	policy.S3PutBucketObjectLockConfiguration: true, policy.S3GetBucketObjectLockConfiguration: true,
	policy.S3GetBucketPolicyStatus:      true,
	policy.S3PutBucketPublicAccessBlock: true, policy.S3GetBucketPublicAccessBlock: true,
}

// tagAdder returns what adds the tags the op adds before it authorizes a,
// for buildEnv to run at the op's own point, or nil for none. A policy
// decides what is needed (rgw_check_policy_condition over the resource's
// policy and the identity's). An object's tags come from obj when given and
// the op loaded it for the key r names, and otherwise from a stat of the key,
// made only when needed, as radosgw reads the object's attrs then
// (rgw_iam_add_objtags,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L727-L745 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L757-L775 at v20.2.4).
// A missing object adds no keys. When a policy needs the tags, a failed read
// or a tag set that does not decode is an error: radosgw ignores either and
// evaluates without the tags, which skips a tag-conditioned Deny
// (docs/ceph-upstream-bugs.md, "radosgw evaluates a tag-conditioned policy
// without the tags it fails to read"). The ACL ops add tags whatever the
// policies name; when none needs them, a tag set they cannot read adds nothing
// and refuses nothing, as in radosgw.
func (e *Evaluator) tagAdder(ctx context.Context, r *op.Request, a policy.Action, b bucketInputs, key meta.ObjKey,
	obj *op.ObjectState, ids []*policy.Policy, f form,
) (func(*policy.Env), error) {
	objRule, bucketRule := tagRule(f, r, a)
	existing, resource := needsTags(append([]*policy.Policy{b.policy}, ids...)...)
	var existingObj, resourceObj bool
	switch objRule {
	case objectTagsAsNeeded:
		existingObj, resourceObj = existing, resource
	case objectTagsExisting:
		existingObj = existing
	case objectTagsAlways:
		existingObj, resourceObj = true, true
	}
	withBucket := bucketRule == bucketTagsAlways || bucketRule == bucketTagsAsNeeded && resource
	needed := existing || resource
	var objSet, bucketSet *tags.Set
	if existingObj || resourceObj {
		st, err := e.objectFor(ctx, r, a, b.rec, key, obj, f)
		if err == nil && st != nil && st.Exists {
			objSet, err = tagSet(st.Attrs)
		}
		if err != nil && needed {
			return nil, fmt.Errorf("reading the tags of %s: %w", key.Name, err)
		}
	}
	if withBucket {
		var err error
		if bucketSet, err = tagSet(b.rec.Attrs); err != nil && needed {
			return nil, fmt.Errorf("reading the tags of bucket %s: %w", b.rec.Info.Bucket.Name, err)
		}
	}
	if objSet == nil && bucketSet == nil {
		return nil, nil
	}
	return func(env *policy.Env) {
		addTags(env, objSet, existingObj, resourceObj)
		addTags(env, bucketSet, false, true)
	}, nil
}

// objectFor is the object whose tags a check adds: obj, the head the object
// op loaded, except for ListParts, whose loaded head is the upload's meta
// object and which reads the object under the upload's key; for a bucket
// check, r.ObjState when it is the key's, and otherwise a stat of the key.
// A missing object, whether the stat reports it or fails with NoSuchKey, is a
// state that does not exist; any other failure is an error.
func (e *Evaluator) objectFor(ctx context.Context, r *op.Request, a policy.Action, rec *op.BucketRecord,
	key meta.ObjKey, obj *op.ObjectState, f form,
) (*op.ObjectState, error) {
	if (f == formObject || f == formObjectIn) && a != policy.S3ListMultipartUploadParts {
		return obj, nil
	}
	if key.Name == "" {
		return nil, nil
	}
	if f == formBucket && r.ObjState != nil && loadedFor(r.ObjState, key) {
		return r.ObjState, nil
	}
	if r.Env == nil || r.Env.Objects == nil {
		return nil, errors.New("no object store")
	}
	st, err := r.Env.Objects.StatObject(ctx, rec, key)
	switch {
	case errors.Is(err, op.ErrNoSuchKey):
		slog.DebugContext(ctx, "no object to read tags from", slog.String("key", key.Name))
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("stat: %w", err)
	}
	return st, nil
}

// loadedFor reports whether st is the state of key: read for key itself, or,
// for key named without an instance, followed through its OLH to a version,
// whose key StatObject then carries.
func loadedFor(st *op.ObjectState, key meta.ObjKey) bool {
	if st.Key == key {
		return true
	}
	return key.Instance == "" && st.Key.Name == key.Name && st.Key.NS == key.NS
}
