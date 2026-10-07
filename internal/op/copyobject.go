package op

import (
	"context"
	"fmt"
	"maps"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// copyOntoItselfMsg is the message check_storage_class sets
// (rgw_rest_s3.cc:3557-3559 at v19.2.6, :3837-3839 at v20.2.4).
const copyOntoItselfMsg = "This copy request is illegal because it is trying to copy an object to itself without " +
	"changing the object's metadata, storage class, website redirect location or encryption attributes."

// CopyObject is RGWCopyObj (rgw_op.cc:5376-5693 at v19.2.6, :5942-6273 at
// v20.2.4; rgw_rest_s3.cc:3478-3603 at v19.2.6, :3758-3886 at v20.2.4). The
// handler fills the inputs; ObjectStore.CopyObject copies, and the op
// authorizes and checks.
type CopyObject struct {
	SrcTenant, SrcBucket string
	SrcKey               meta.ObjKey
	// Attrs are the handler's request attrs, which replace the source's under
	// REPLACE (set_copy_attrs).
	Attrs map[string][]byte
	// ACL is init_dest_policy's.
	ACL acl.Policy
	// Replace is x-amz-metadata-directive: REPLACE.
	Replace bool
	// StorageClass is x-amz-storage-class, "" when absent.
	StorageClass string
	// CheckStorageClass is need_to_check_storage_class: the source is the
	// destination, named without a version, and the metadata is not
	// replaced.
	CheckStorageClass bool
	// IfMatch, IfNoneMatch, IfModifiedSince and IfUnmodifiedSince are the
	// x-amz-copy-source-if-* headers' values, nil when absent: a header
	// present with an empty value is a condition, as radosgw takes it.
	IfMatch, IfNoneMatch *string
	IfModifiedSince      *string
	IfUnmodifiedSince    *string
	// Params, when set, is the protocol's get_params, which needs the
	// destination bucket: Init calls it once that bucket is loaded and its
	// placement checked, before it loads the source, as
	// RGWCopyObj::init_processing calls get_params (rgw_op.cc:5383-5400 at
	// v19.2.6, :5949-5966 at v20.2.4). It fills the inputs it reads, the
	// destination ACL among them.
	Params func(ctx context.Context, o *CopyObject) error

	ETag      string
	Mtime     time.Time
	VersionID string

	srcRec *BucketRecord
	src    *ObjectState
}

var _ Op = (*CopyObject)(nil)

// Name is RGWCopyObj::name.
func (o *CopyObject) Name() string { return "copy_obj" }

// Action is the destination's action; VerifyPermission also authorizes the
// source's read.
func (o *CopyObject) Action() policy.Action { return policy.S3PutObject }

// OpMask is RGW_OP_TYPE_WRITE.
func (o *CopyObject) OpMask() uint32 { return OpTypeWrite }

// Init loads the destination bucket and checks its placement as
// init_permissions does, loads the source bucket as init_processing does
// (rgw_op.cc:5376-5405 at v19.2.6, :5942-5971 at v20.2.4), a missing one
// NoSuchBucket, and reads the source's head with its first chunk, as
// read_obj_policy reads it with prefetch_data set (:5413-5422 at v19.2.6,
// :5979-5988 at v20.2.4). Params runs between the two buckets' loads. A
// public destination policy under the destination bucket's block of public
// ACLs is refused once Params has run, as PutObject refuses one; radosgw
// makes no such check for a copy (docs/exclusions.md).
func (o *CopyObject) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	if perr := checkDestPlacement(r, o.StorageClass); perr != nil {
		return perr
	}
	if o.Params != nil {
		if perr := o.Params(ctx, o); perr != nil {
			return perr
		}
	}
	if blockPublicACLs(rec) && o.ACL.IsPublic() {
		return fmt.Errorf("%w: a public acl under a block of public acls", ErrAccessDenied)
	}
	if o.srcRec, err = r.Env.Buckets.GetBucket(ctx, o.SrcTenant, o.SrcBucket); err != nil {
		return err
	}
	if o.src, err = r.Env.Objects.PrefetchObject(ctx, o.srcRec, o.SrcKey); err != nil {
		return err
	}
	if o.src == nil {
		o.src = &ObjectState{Bucket: o.srcRec, Key: o.SrcKey}
	}
	return nil
}

// VerifyPermission is RGWCopyObj::verify_permission (rgw_op.cc:5407-5498 at
// v19.2.6, :5973-6064 at v20.2.4) in its order: read_obj_policy's rule for a
// missing source and its decode of the source's ACL, check_storage_class
// for a copy onto itself, the source's read, then s3:PutObject on the
// destination. The destination is checked first all the same, so that the
// refusals radosgw makes on it in init_permissions, before verify_permission
// and never overridden (rgw_process.cc:174-177 and :225-237 at v19.2.6), come
// out marked and ahead of every source check; its other refusal comes last.
//
// radosgw authorizes the source's read against its bucket's ACL alone and
// reads the object's ACL without using it (docs/ceph-upstream-bugs.md,
// "radosgw checks a CopyObject source against its bucket's ACL, not the
// object's"). rgw-go needs READ on both: the bucket check, then the object
// check, each with the source bucket's policy and the identity's, which
// decide before either ACL (docs/exclusions.md).
func (o *CopyObject) VerifyPermission(ctx context.Context, r *Request) error {
	a := policy.S3GetObject
	if o.SrcKey.Instance != "" {
		a = policy.S3GetObjectVersion
	}
	perm := acl.PermFor(a)
	destErr := VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
	if IsBeforeVerify(destErr) {
		return destErr
	}
	if !o.src.Exists {
		if err := VerifyObjectPermissionIn(ctx, r, a, perm, o.srcRec, o.src); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", ErrNoSuchKey, o.SrcKey.Name)
	}
	if _, err := ObjectACLFor(ctx, o.src, o.srcRec); err != nil {
		return err
	}
	if o.CheckStorageClass {
		if err := o.checkStorageClass(r); err != nil {
			return err
		}
	}
	if err := VerifyBucketPermissionIn(ctx, r, a, perm, o.srcRec, o.SrcKey); err != nil {
		return err
	}
	if err := VerifyObjectPermissionIn(ctx, r, a, perm, o.srcRec, o.src); err != nil {
		return err
	}
	return destErr
}

// checkStorageClass is RGWCopyObj_ObjStore_S3::check_storage_class
// (rgw_rest_s3.cc:3553-3564 at v19.2.6, :3833-3844 at v20.2.4): a copy onto
// itself must change the storage class. The source's is its storage class attr on its bucket's
// placement, as read_obj_policy reads it (rgw_op.cc:314-322 and :5428-5435
// at v19.2.6), compared with s->dest_placement as rgw_placement_rule's ==
// does, an empty class being STANDARD.
func (o *CopyObject) checkStorageClass(r *Request) error {
	srcRule := meta.PlacementRule{StorageClass: o.src.StorageClass}.InheritFrom(o.srcRec.Info.PlacementRule)
	dstRule := meta.PlacementRule{StorageClass: o.StorageClass}.InheritFrom(r.BucketRec.Info.PlacementRule)
	if srcRule.Name == dstRule.Name && srcRule.CanonicalStorageClass() == dstRule.CanonicalStorageClass() {
		return ErrInvalidRequest.WithMessage(copyOntoItselfMsg)
	}
	return nil
}

// Execute is RGWCopyObj::execute (rgw_op.cc:5558-5693 at v19.2.6,
// :6124-6273 at v20.2.4) in its order: init_common's parse of the two dates,
// the source's cloud tier, its accounted size against rgw_max_put_size and
// the destination's quota unless the request is a system request, then
// copy_obj, whose read of the source applies the conditions (Read::prepare,
// driver/rados/rgw_rados.cc:4742-4754 at v19.2.6) before it copies under the
// request id as its tag. A destination whose versioning or object lock
// rgw-go does not serve, and a copy naming a version, are refused first.
func (o *CopyObject) Execute(ctx context.Context, r *Request) error {
	if err := versioningUnserved(r.BucketRec, r.Object, o.SrcKey); err != nil {
		return err
	}
	if err := bucketEncryptionUnserved(r.BucketRec); err != nil {
		return err
	}
	// An admin let through a refusal of a missing source reaches here.
	if !o.src.Exists {
		return fmt.Errorf("%w: %s", ErrNoSuchKey, o.SrcKey.Name)
	}
	conds, perr := parseReadConds(o.IfModifiedSince, o.IfUnmodifiedSince, o.IfMatch, o.IfNoneMatch)
	if perr != nil {
		return perr
	}
	if err := o.checkCloudTier(r); err != nil {
		return err
	}
	if !r.Identity.System {
		accounted := o.src.Size
		if o.src.Compression != nil {
			accounted = o.src.Compression.OrigSize
		}
		if maxPut := confSize(r, "rgw_max_put_size", defaultMaxPutSize); accounted > maxPut {
			return fmt.Errorf("%w: a source of %d bytes over rgw_max_put_size %d", ErrEntityTooLarge, accounted, maxPut)
		}
		if err := r.Env.Stats.CheckQuota(ctx, r.BucketRec, r.BucketRec.Info.Owner, int64(accounted), 1); err != nil { //nolint:gosec // within rgw_max_put_size
			return err
		}
	}
	if err := conds.check(o.src); err != nil {
		return err
	}
	attrs := maps.Clone(o.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[meta.AttrACL] = encodeAt(o.ACL, r.Env.Zone.Release())
	res, err := r.Env.Objects.CopyObject(ctx, o.src, r.BucketRec, r.Object, CopyParams{
		Attrs: attrs, ReplaceAttrs: o.Replace, StorageClass: o.StorageClass,
		IfMatch: storeCondition(o.IfMatch), IfNoneMatch: storeCondition(o.IfNoneMatch), Tag: r.ID,
	})
	if err != nil {
		return err
	}
	o.ETag, o.Mtime, o.VersionID = res.ETag, res.Mtime, res.Version
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *CopyObject) Complete(context.Context, *Request) {}

// checkCloudTier is execute's refusal of a source transitioned to a cloud
// tier: on Squid one whose manifest names cloud-s3 (rgw_op.cc:5604-5623 at
// v19.2.6), on Tentacle one whose manifest names either S3 tier type and
// whose size is zero, which a restored object's is not (:6170-6190 at
// v20.2.4).
func (o *CopyObject) checkCloudTier(r *Request) error {
	m := o.src.Manifest
	if m == nil {
		return nil
	}
	if r.Env.Zone.Release() < denc.Tentacle {
		if m.TierType != meta.TierTypeCloudS3 {
			return nil
		}
	} else if !isS3Tier(m.TierType) || o.src.Size != 0 {
		return nil
	}
	return ErrInvalidObjectState.WithMessage("This object was transitioned to cloud-s3")
}
