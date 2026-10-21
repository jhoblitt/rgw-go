package op

import (
	"context"
	"errors"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The bucket object-lock ops of this file are rgw_op.cc's
// RGWGetBucketObjectLock and RGWPutBucketObjectLock. Each loads the bucket in
// Init, so a missing bucket is NoSuchBucket before the permission check, and
// authorizes its action against the bucket, with the bucket's tags added for
// a policy that names them. The forward to the metadata master the write
// makes is a no-op on a single zone, which is its own master.

// msgLockNeedsVersioning is the message PutObjectLockConfiguration sets for
// a bucket whose versioning is not enabled
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8245-L8251 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9180-L9186 at v20.2.4).
const msgLockNeedsVersioning = "Object lock cannot be enabled unless the bucket has versioning enabled"

// GetObjectLockConfiguration is RGWGetBucketObjectLock
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8313-L8332 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9248-L9267 at v20.2.4):
// the bucket's object lock configuration as its instance holds it.
type GetObjectLockConfiguration struct {
	// Lock is the stored configuration.
	Lock meta.ObjectLock
}

var _ Op = (*GetObjectLockConfiguration)(nil)

// Name is RGWGetBucketObjectLock::name.
func (*GetObjectLockConfiguration) Name() string { return "get_bucket_object_lock" }

// Action is s3:GetBucketObjectLockConfiguration.
func (*GetObjectLockConfiguration) Action() policy.Action {
	return policy.S3GetBucketObjectLockConfiguration
}

// OpMask is RGW_OP_TYPE_READ.
func (*GetObjectLockConfiguration) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetObjectLockConfiguration) Init(ctx context.Context, r *Request) error {
	return loadBucket(ctx, r)
}

// VerifyPermission is RGWGetBucketObjectLock::verify_permission.
func (o *GetObjectLockConfiguration) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWGetBucketObjectLock::execute: a bucket whose flags do not
// enable object lock is ObjectLockConfigurationNotFoundError, with no
// message. A lock bucket that stores no configuration has RGWObjectLock(),
// enabled with no rule. A configuration whose encoding does not decode is
// -EIO, 500 UnknownError, which radosgw answers when it loads the bucket,
// before the permission check, since it decodes the whole instance then
// (docs/exclusions.md, "Some bucket sub-records are carried opaque in phase
// 0").
func (o *GetObjectLockConfiguration) Execute(_ context.Context, r *Request) error {
	info := r.BucketRec.Info
	if !info.ObjLockEnabled() {
		return ErrNoSuchObjectLockConfig
	}
	l, err := info.ObjectLock()
	if err != nil {
		return fmt.Errorf("%w: bucket %s: %w", ErrUnknown, info.Bucket.Name, err)
	}
	o.Lock = l
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetObjectLockConfiguration) Complete(context.Context, *Request) {}

// PutObjectLockConfiguration is RGWPutBucketObjectLock
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8230-L8306 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9165-L9241 at v20.2.4).
// radosgw's execute calls get_params itself, after its versioning check, so
// Execute reads and parses the request's body.
type PutObjectLockConfiguration struct{}

var _ Op = (*PutObjectLockConfiguration)(nil)

// Name is RGWPutBucketObjectLock::name.
func (*PutObjectLockConfiguration) Name() string { return "put_bucket_object_lock" }

// Action is s3:PutBucketObjectLockConfiguration.
func (*PutObjectLockConfiguration) Action() policy.Action {
	return policy.S3PutBucketObjectLockConfiguration
}

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutObjectLockConfiguration) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutObjectLockConfiguration) Init(ctx context.Context, r *Request) error {
	return loadBucket(ctx, r)
}

// VerifyPermission is RGWPutBucketObjectLock::verify_permission.
func (o *PutObjectLockConfiguration) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWPutBucketObjectLock::execute, in its order. A bucket whose
// versioning is not enabled is 409 InvalidBucketState, before the body is
// read. The body is read under rgw_max_put_param_size as ReadParamBody reads
// it, RGWPutBucketObjectLock_ObjStore::get_params
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1491-L1496 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1491-L1496 at v20.2.4),
// and objectlock.ParseConfiguration's every refusal is 400 MalformedXML. A
// rule whose period is not exactly one positive Days or Years is 400
// InvalidRetentionPeriod, and so is one whose period from now runs past
// meta.MaxRealTime, which radosgw stores and then wraps on every write it
// locks, often to a date already past (docs/exclusions.md, "A date
// real_time cannot hold is refused"). Then, through retryBucketWrite: a
// bucket without the object-lock flag must still have versioning enabled,
// and gets the flag; the parsed configuration replaces the stored one; and
// PutBucketInfo writes the instance with the attrs that try read. radosgw's
// retry_raced_bucket_write authorizes no retry and writes into a bucket
// re-created under the name; rgw-go's authorizes each retry again and stops
// there (docs/exclusions.md, "A PutBucketAcl that loses a race is retried,
// and every retried ACL, policy or tagging write is authorized again" and
// "A retried bucket write stops when the name names another bucket").
func (o *PutObjectLockConfiguration) Execute(ctx context.Context, r *Request) error {
	if !r.BucketRec.Info.VersioningEnabled() {
		return ErrInvalidBucketState.WithMessage(msgLockNeedsVersioning)
	}
	body, err := ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	if err != nil {
		return err
	}
	l, err := objectlock.ParseConfiguration(body)
	switch {
	case errors.Is(err, objectlock.ErrMalformedXML):
		return fmt.Errorf("%w: %w", ErrMalformedXML, err)
	case err != nil:
		return fmt.Errorf("parsing the object lock configuration: %w", err)
	}
	if l.Rule != nil {
		d := l.Rule.DefaultRetention
		if !objectlock.RetentionPeriodValid(d) || !objectlock.PeriodFits(d, r.Env.Clock()) {
			return ErrInvalidRetentionPeriod.WithMessage("retention period must be a positive integer value")
		}
	}
	return retryBucketWrite(ctx, r, o, func() error {
		info := &r.BucketRec.Info
		if !info.ObjLockEnabled() {
			if !info.VersioningEnabled() {
				return ErrInvalidBucketState.WithMessage(msgLockNeedsVersioning)
			}
			info.Flags |= meta.BucketObjLockEnabled
		}
		info.SetObjectLock(l)
		return r.Env.Buckets.PutBucketInfo(ctx, r.BucketRec)
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutObjectLockConfiguration) Complete(context.Context, *Request) {}
