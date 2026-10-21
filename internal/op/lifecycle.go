package op

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // Content-MD5 is an MD5
	"errors"
	"fmt"
	"math"

	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// The lifecycle configuration ops of this file are rgw_op.cc's RGWPutLC and
// RGWDeleteLC. Each loads the bucket in Init, so a missing bucket is
// NoSuchBucket before the permission check, and authorizes its action
// against the bucket, adding the bucket's tags when a policy needs them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5776-L5802 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6419-L6445 at v20.2.4).
// The configuration is the AttrLC attr, and a bucket that holds one has an
// entry in its lifecycle shard, which Env.Lifecycle keeps. The forward to
// the metadata master each write makes is a no-op on a single zone, which is
// its own master.

// The messages RGWPutLC::execute sets for a Content-MD5 that does not decode
// and for one that is not the body's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5947-L5958 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5980-L5988 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6613-L6624 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6646-L6654 at v20.2.4).
const (
	msgLCMD5NotBase64 = "Request header Content-MD5 contains character that is not base64 encoded."
	msgLCMD5Mismatch  = "The Content-MD5 you specified did not match what we received."
)

// PutBucketLifecycle is RGWPutLC
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1652-L1682 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1803-L1834 at v20.2.4),
// PutBucketLifecycleConfiguration. Its get_params is RGWPutLC_ObjStore's,
// the body read under rgw_max_put_param_size
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1484-L1489 at v19.2.7 and v20.2.4),
// which Execute reads itself.
type PutBucketLifecycle struct{}

var _ Op = (*PutBucketLifecycle)(nil)

// Name is RGWPutLC::name.
func (*PutBucketLifecycle) Name() string { return "put_lifecycle" }

// Action is s3:PutLifecycleConfiguration.
func (*PutBucketLifecycle) Action() policy.Action { return policy.S3PutLifecycleConfiguration }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutBucketLifecycle) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutBucketLifecycle) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWPutLC::verify_permission.
func (o *PutBucketLifecycle) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWPutLC::execute
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5933-L6028 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6592-L6695 at v20.2.4)
// with set_bucket_config
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2636-L2666 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2703-L2733 at v20.2.4),
// in its order:
//   - an indexless bucket is 405 MethodNotAllowed, as Tentacle refuses it;
//     Squid stores a configuration its worker cannot list (docs/exclusions.md);
//   - a Content-MD5 that does not decode, or decodes to other than an MD5's
//     16 bytes, is InvalidDigest, S3's answer and radosgw's PutObject's,
//     where radosgw's PutBucketLifecycle answers BadDigest and compares 16
//     bytes of whatever length it decodes to (docs/exclusions.md);
//   - the body, as ReadParamBody reads it, then BadDigest for a body whose
//     MD5 is not the Content-MD5;
//   - lifecycle.Parse under rgw_lc_max_rules, its refusals 400
//     MalformedXML, InvalidArgument or InvalidRequest, with no message, as
//     RGWPutLC::execute sets none;
//   - the configuration's encoding set over the bucket as the attr alone
//     through retryBucketWrite, which authorizes each retry again, where
//     radosgw makes one attempt and fails the request on a lost race
//     (docs/exclusions.md);
//   - the bucket's lifecycle entry, set under its shard's lock. When the
//     lock cannot be taken the attr stays, as in radosgw, and the request
//     fails with the lock's error: 503 ServiceUnavailable for EBUSY.
func (o *PutBucketLifecycle) Execute(ctx context.Context, r *Request) error {
	if r.BucketRec.Info.Layout.Current.Layout.Type == meta.IndexIndexless {
		return ErrMethodNotAllowed.WithMessage("Indexless buckets do not support lifecycle policy")
	}
	want, err := lifecycleContentMD5(r)
	if err != nil {
		return err
	}
	body, err := ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	if err != nil {
		return err
	}
	if want != nil {
		if sum := md5.Sum(body); !bytes.Equal(sum[:], want) { //nolint:gosec // Content-MD5 is an MD5
			return ErrBadDigest.WithMessage(msgLCMD5Mismatch)
		}
	}
	c, err := lifecycle.Parse(body, lcMaxRules(r))
	if err != nil {
		return lifecycleParseError(err)
	}
	set := map[string][]byte{AttrLC: encodeAt(c, r.Env.Zone.Release())}
	if err := retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, set, nil)
	}); err != nil {
		return err
	}
	if err := r.Env.Lifecycle.SetEntry(ctx, r.BucketRec); err != nil {
		return FromRADOS(fmt.Errorf("setting the lifecycle entry of bucket %s: %w", r.BucketRec.Info.Bucket.Name, err), ScopeBucket)
	}
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutBucketLifecycle) Complete(context.Context, *Request) {}

// lifecycleContentMD5 is the request's Content-MD5 decoded by rgw::from_base64
// (fromBase64), nil when the header is absent: the C string radosgw's env
// holds, the last of a repeated header. One that does not decode is 400
// InvalidDigest with the message RGWPutLC::execute gives its BadDigest for
// it, and one that decodes to other than 16 bytes, an MD5's length, is
// InvalidDigest with no message, as RGWPutObj::execute answers it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4184-L4194 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4393-L4403 at v20.2.4).
// fromBase64 decodes an input of '=' alone to no bytes, where radosgw's
// from_base64 reads before the input's start (docs/ceph-upstream-bugs.md,
// "radosgw's from_base64 reads before an all-'=' input").
func lifecycleContentMD5(r *Request) ([]byte, error) {
	v, ok := HeaderValue(r.Header, "Content-MD5")
	if !ok {
		return nil, nil
	}
	sum, ok := fromBase64(rgwtext.CString(v))
	switch {
	case !ok:
		return nil, ErrInvalidDigest.WithMessage(msgLCMD5NotBase64)
	case len(sum) != md5.Size:
		return nil, fmt.Errorf("%w: a %d-byte Content-MD5", ErrInvalidDigest, len(sum))
	}
	return sum, nil
}

// lcMaxRules is rgw_lc_max_rules, read at each request as
// RGWLifecycleConfiguration_S3::decode_xml reads it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L109-L114 at v19.2.7 and v20.2.4),
// lifecycle.DefaultMaxRules when it cannot be read, and the most an int
// holds for a count beyond it.
func lcMaxRules(r *Request) int {
	if r.Env.Conf == nil {
		return lifecycle.DefaultMaxRules
	}
	v, err := r.Env.Conf.Uint64("rgw_lc_max_rules")
	if err != nil {
		return lifecycle.DefaultMaxRules
	}
	return int(min(v, math.MaxInt))
}

// lifecycleParseError is the S3 error for a lifecycle.Parse refusal, with
// the refusal's reason kept for the log.
func lifecycleParseError(err error) error {
	switch {
	case errors.Is(err, lifecycle.ErrMalformedXML):
		return fmt.Errorf("%w: %w", ErrMalformedXML, err)
	case errors.Is(err, lifecycle.ErrInvalidArgument):
		return fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	case errors.Is(err, lifecycle.ErrInvalidRequest):
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return fmt.Errorf("parsing the lifecycle configuration: %w", err)
}

// DeleteBucketLifecycle is RGWDeleteLC
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1684-L1695 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1836-L1848 at v20.2.4).
type DeleteBucketLifecycle struct{}

var _ Op = (*DeleteBucketLifecycle)(nil)

// Name is RGWDeleteLC::name.
func (*DeleteBucketLifecycle) Name() string { return "delete_lifecycle" }

// Action is s3:PutLifecycleConfiguration, which RGWDeleteLC authorizes.
func (*DeleteBucketLifecycle) Action() policy.Action { return policy.S3PutLifecycleConfiguration }

// OpMask is RGW_OP_TYPE_WRITE, which RGWDeleteLC declares.
func (*DeleteBucketLifecycle) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*DeleteBucketLifecycle) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWDeleteLC::verify_permission.
func (o *DeleteBucketLifecycle) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWDeleteLC::execute
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6030-L6047 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6697-L6714 at v20.2.4)
// with remove_bucket_config
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2668-L2693 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2735-L2760 at v20.2.4):
// the attr removed from a bucket that holds it, through retryBucketWrite,
// each try writing only when the bucket as it read it holds the attr; then
// the bucket's lifecycle entry removed under its shard's lock whether or not
// the bucket held a configuration, a failure to lock answered with the
// lock's error.
func (o *DeleteBucketLifecycle) Execute(ctx context.Context, r *Request) error {
	if err := retryBucketWrite(ctx, r, o, func() error {
		if _, ok := r.BucketRec.Attrs[AttrLC]; !ok {
			return nil
		}
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, nil, []string{AttrLC})
	}); err != nil {
		return err
	}
	if err := r.Env.Lifecycle.RemoveEntry(ctx, r.BucketRec); err != nil {
		return FromRADOS(fmt.Errorf("removing the lifecycle entry of bucket %s: %w", r.BucketRec.Info.Bucket.Name, err), ScopeBucket)
	}
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*DeleteBucketLifecycle) Complete(context.Context, *Request) {}
