package op

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The bucket versioning ops are rgw_op.cc's RGWGetBucketVersioning and
// RGWSetBucketVersioning. Each loads the bucket in Init, so a missing bucket
// is NoSuchBucket before the permission check, and authorizes its action
// against the bucket, with the bucket's tags added for a policy that names
// them. The forward to the metadata master the write makes is a no-op on a
// single zone, which is its own master.

// VersioningStatus is the Status of a VersioningConfiguration, radosgw's
// BucketVersionStatus without its invalid and unchanged values
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1020-L1025 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1082-L1087 at v20.2.4).
type VersioningStatus uint8

// The statuses a VersioningConfiguration may name.
const (
	// VersioningEnabled is "Enabled".
	VersioningEnabled VersioningStatus = iota + 1
	// VersioningSuspended is "Suspended".
	VersioningSuspended
)

// GetBucketVersioning is RGWGetBucketVersioning
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2729-L2757 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2961-L2989 at v20.2.4):
// the bucket's versioning state as its flags hold it.
type GetBucketVersioning struct {
	// Versioned is versioned(): versioning was enabled once.
	Versioned bool
	// Enabled is versioning_enabled(): versioned and not suspended.
	Enabled bool
	// MFAEnabled is mfa_enabled().
	MFAEnabled bool
}

var _ Op = (*GetBucketVersioning)(nil)

// Name is RGWGetBucketVersioning::name.
func (*GetBucketVersioning) Name() string { return "get_bucket_versioning" }

// Action is s3:GetBucketVersioning.
func (*GetBucketVersioning) Action() policy.Action { return policy.S3GetBucketVersioning }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketVersioning) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketVersioning) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetBucketVersioning::verify_permission.
func (o *GetBucketVersioning) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute reads the state from the bucket's flags.
func (o *GetBucketVersioning) Execute(_ context.Context, r *Request) error {
	info := r.BucketRec.Info
	o.Versioned, o.Enabled, o.MFAEnabled = info.Versioned(), info.VersioningEnabled(), info.MFAEnabled()
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketVersioning) Complete(context.Context, *Request) {}

// PutBucketVersioning is RGWSetBucketVersioning
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2759-L2860 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2991-L3092 at v20.2.4).
// Params, when set, is the handler's get_params, which fills Status and
// MFADelete once the requester is authorized; a refusal comes back already
// mapped to an S3 error.
type PutBucketVersioning struct {
	Params func(ctx context.Context, o *PutBucketVersioning) error
	// Status is the state the bucket is put in.
	Status VersioningStatus
	// MFADelete is the MfaDelete the document names, nil when it names none.
	MFADelete *bool
}

var _ Op = (*PutBucketVersioning)(nil)

// Name is RGWSetBucketVersioning::name.
func (*PutBucketVersioning) Name() string { return "set_bucket_versioning" }

// Action is s3:PutBucketVersioning.
func (*PutBucketVersioning) Action() policy.Action { return policy.S3PutBucketVersioning }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutBucketVersioning) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutBucketVersioning) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWSetBucketVersioning::verify_permission.
func (o *PutBucketVersioning) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWSetBucketVersioning::execute: Params, then the bucket's
// flags set and its instance rewritten with PutBucketInfo, which sends the
// cache notify, through retryBucketWrite. Every valid status writes, a status
// the bucket already has included, as radosgw's does. No index entry changes.
//
// radosgw checks object lock and MFA delete once, against the bucket its
// request loaded, and each retry then writes over the bucket as it read it
// again; it also puts back the attrs its request loaded
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2847 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3079 at v20.2.4).
// rgw-go makes both checks on every try against the bucket that try read, and
// writes that bucket's own attrs (docs/exclusions.md, "A retried
// PutBucketVersioning checks object lock and MFA delete again").
func (o *PutBucketVersioning) Execute(ctx context.Context, r *Request) error {
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	if o.Status != VersioningEnabled && o.Status != VersioningSuspended {
		return fmt.Errorf("%w: versioning status %d", ErrInternalError, o.Status)
	}
	return retryBucketWrite(ctx, r, o, func() error {
		info := &r.BucketRec.Info
		if err := o.allowed(*info, r.MFAVerified); err != nil {
			return err
		}
		info.Flags = o.flags(info.Flags)
		return r.Env.Buckets.PutBucketInfo(ctx, r.BucketRec)
	})
}

// allowed is execute's refusals for a bucket in the state info holds
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2788-L2817 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3020-L3049 at v20.2.4):
// any status but Enabled on an object-lock bucket is InvalidBucketState with
// radosgw's message; and unless verified, an MfaDelete other than the
// bucket's own, or, with MFA delete on, a status that flips
// BUCKET_VERSIONS_SUSPENDED, is ERR_MFA_REQUIRED, 403 AccessDenied. radosgw
// tests that flag alone, so with MFA delete on, Suspended on a bucket never
// versioned flips it and Enabled does not.
func (o *PutBucketVersioning) allowed(info meta.BucketInfo, verified bool) error {
	if info.ObjLockEnabled() && o.Status != VersioningEnabled {
		return ErrInvalidBucketState.WithMessage("bucket versioning cannot be disabled on buckets with object lock enabled")
	}
	if verified {
		return nil
	}
	if o.MFADelete != nil && *o.MFADelete != info.MFAEnabled() {
		return fmt.Errorf("%w: changing MFA delete needs a verified x-amz-mfa", ErrMFARequired)
	}
	suspended := info.Flags&meta.BucketVersionsSuspended != 0
	if info.MFAEnabled() && suspended == (o.Status == VersioningEnabled) {
		return fmt.Errorf("%w: changing the versioning of a bucket with MFA delete needs a verified x-amz-mfa", ErrMFARequired)
	}
	return nil
}

// flags is the retried write's flags from flags
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2829-L2846 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3061-L3078 at v20.2.4):
// MfaDelete sets or clears BUCKET_MFA_ENABLED; Enabled sets BUCKET_VERSIONED
// and clears BUCKET_VERSIONS_SUSPENDED, Suspended sets both.
func (o *PutBucketVersioning) flags(flags uint32) uint32 {
	if o.MFADelete != nil {
		if *o.MFADelete {
			flags |= meta.BucketMFAEnabled
		} else {
			flags &^= meta.BucketMFAEnabled
		}
	}
	if o.Status == VersioningEnabled {
		return flags&^meta.BucketVersionsSuspended | meta.BucketVersioned
	}
	return flags | meta.BucketVersioned | meta.BucketVersionsSuspended
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutBucketVersioning) Complete(context.Context, *Request) {}
