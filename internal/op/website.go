package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The bucket website ops of this file are rgw_op.cc's RGWGetBucketWebsite,
// RGWSetBucketWebsite and RGWDeleteBucketWebsite. Each loads the bucket in
// Init, so a missing bucket is NoSuchBucket before the permission check, and
// authorizes its action against the bucket, whose tags the authorizer adds
// for a policy that needs them. The configuration lives in the bucket
// instance, as has_website and website_conf, so the two writes rewrite the
// instance whole with PutBucketInfo, over the attrs and the rest of the
// instance as the try read them, as RadosBucket::put_info writes what
// try_refresh_info read
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L753-L757
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L780-L783 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L771-L775
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L798-L801 at v20.2.4).
// The forward to the metadata master each write makes is a no-op on a single
// zone, which is its own master.

// GetBucketWebsite is RGWGetBucketWebsite
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2862-L2885 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3094-L3117 at v20.2.4):
// the bucket's website configuration as stored.
type GetBucketWebsite struct {
	// Conf is the stored configuration, its Swift members with it.
	Conf meta.WebsiteConf
}

var _ Op = (*GetBucketWebsite)(nil)

// Name is RGWGetBucketWebsite::name.
func (*GetBucketWebsite) Name() string { return "get_bucket_website" }

// Action is s3:GetBucketWebsite.
func (*GetBucketWebsite) Action() policy.Action { return policy.S3GetBucketWebsite }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketWebsite) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketWebsite) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetBucketWebsite::verify_permission.
func (o *GetBucketWebsite) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWGetBucketWebsite::execute: a bucket without has_website is
// NoSuchWebsiteConfiguration.
func (o *GetBucketWebsite) Execute(_ context.Context, r *Request) error {
	w := r.BucketRec.Info.Website
	if w == nil {
		return ErrNoSuchWebsiteConfiguration
	}
	o.Conf = *w
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketWebsite) Complete(context.Context, *Request) {}

// SetBucketWebsite is RGWSetBucketWebsite
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2887-L2936 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3119-L3168 at v20.2.4).
// Params, when set, is the handler's get_params
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2279-L2345 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2374-L2440 at v20.2.4),
// which parses the body into Conf once the requester is authorized; a refusal
// comes back already mapped to an S3 error.
type SetBucketWebsite struct {
	Params func(ctx context.Context, o *SetBucketWebsite) error
	// Conf is the configuration to store, which replaces the stored one whole.
	Conf meta.WebsiteConf
}

var _ Op = (*SetBucketWebsite)(nil)

// Name is RGWSetBucketWebsite::name.
func (*SetBucketWebsite) Name() string { return "set_bucket_website" }

// Action is s3:PutBucketWebsite.
func (*SetBucketWebsite) Action() policy.Action { return policy.S3PutBucketWebsite }

// OpMask is RGW_OP_TYPE_WRITE.
func (*SetBucketWebsite) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*SetBucketWebsite) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWSetBucketWebsite::verify_permission.
func (o *SetBucketWebsite) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWSetBucketWebsite::execute: Params, then has_website and Conf
// set over the bucket and the instance rewritten through retryBucketWrite,
// which authorizes each retry again against the bucket it read.
func (o *SetBucketWebsite) Execute(ctx context.Context, r *Request) error {
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	conf := o.Conf
	return retryBucketWrite(ctx, r, o, func() error { return putWebsite(ctx, r, &conf) })
}

// Complete does nothing: the handler logs usage once the response is written.
func (*SetBucketWebsite) Complete(context.Context, *Request) {}

// DeleteBucketWebsite is RGWDeleteBucketWebsite
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2938-L2981 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3170-L3213 at v20.2.4).
type DeleteBucketWebsite struct{}

var _ Op = (*DeleteBucketWebsite)(nil)

// Name is RGWDeleteBucketWebsite::name.
func (*DeleteBucketWebsite) Name() string { return "delete_bucket_website" }

// Action is s3:DeleteBucketWebsite.
func (*DeleteBucketWebsite) Action() policy.Action { return policy.S3DeleteBucketWebsite }

// OpMask is RGW_OP_TYPE_WRITE, which RGWDeleteBucketWebsite declares.
func (*DeleteBucketWebsite) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*DeleteBucketWebsite) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWDeleteBucketWebsite::verify_permission.
func (o *DeleteBucketWebsite) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWDeleteBucketWebsite::execute: has_website and the
// configuration cleared and the instance rewritten through retryBucketWrite.
// A bucket without a website is rewritten too, as radosgw rewrites it.
func (o *DeleteBucketWebsite) Execute(ctx context.Context, r *Request) error {
	return retryBucketWrite(ctx, r, o, func() error { return putWebsite(ctx, r, nil) })
}

// Complete does nothing: the handler logs usage once the response is written.
func (*DeleteBucketWebsite) Complete(context.Context, *Request) {}

// putWebsite rewrites r's bucket instance with conf as its website
// configuration, nil for none. r.BucketRec takes the write only once it has
// landed, so a failed try leaves the request's bucket as stored.
func putWebsite(ctx context.Context, r *Request, conf *meta.WebsiteConf) error {
	rec := *r.BucketRec
	rec.Info.Website = conf
	if err := r.Env.Buckets.PutBucketInfo(ctx, &rec); err != nil {
		return err
	}
	r.BucketRec = &rec
	return nil
}
