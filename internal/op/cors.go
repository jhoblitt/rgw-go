package op

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The bucket CORS ops of this file are rgw_op.cc's RGWGetCORS, RGWPutCORS,
// RGWDeleteCORS and RGWOptionsCORS, the preflight. The first three load the
// bucket in Init, so a missing bucket is NoSuchBucket before the permission
// check, and authorize their action against the bucket, whose tags the
// authorizer adds for a policy that needs them. The configuration is the
// AttrCORS attr, which the write replaces whole and the delete removes, each
// try setting or removing that attr alone over the bucket as the try read it.
// The forward to the metadata master each write makes is a no-op on a single
// zone, which is its own master.
//
// Each op's StorageClass is the request's x-amz-storage-class, "" when
// absent, which radosgw reads for every S3 request
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5073-L5076 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5603-L5606 at v20.2.4)
// and, while it loads the bucket, refuses with 400 InvalidArgument when the
// bucket's placement lacks its class, through checkDestPlacement. The first
// three ops make that check once the requester is authorized, as PutObject
// does, so a refused requester learns nothing of the bucket's placement
// (docs/exclusions.md); the preflight, which authorizes nobody, makes it
// where radosgw does.

// AttrCORS is RGW_ATTR_CORS
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L83 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L88 at v20.2.4),
// the bucket attr holding its CORS configuration as cors.Configuration
// encodes it.
const AttrCORS = meta.AttrPrefix + "cors"

// bucketCORS is read_bucket_cors
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1483-L1512 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1720-L1749 at v20.2.4):
// the configuration rec holds, nil for none. One that does not decode is
// -EIO, which no row of the error table holds, so set_req_state_err answers
// it 500 UnknownError
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L352-L353 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L365-L366 at v20.2.4).
func bucketCORS(rec *BucketRecord) (*cors.Configuration, error) {
	b, ok := rec.Attrs[AttrCORS]
	if !ok {
		return nil, nil
	}
	c, err := cors.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("%w: bucket %s: %w", ErrUnknown, rec.Info.Bucket.Name, err)
	}
	return c, nil
}

// GetBucketCORS is RGWGetCORS
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6049-L6073 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6716-L6740 at v20.2.4):
// the bucket's CORS configuration as stored.
type GetBucketCORS struct {
	StorageClass string
	// Config is the stored configuration.
	Config *cors.Configuration
}

var _ Op = (*GetBucketCORS)(nil)

// Name is RGWGetCORS::name.
func (*GetBucketCORS) Name() string { return "get_cors" }

// Action is s3:GetBucketCORS.
func (*GetBucketCORS) Action() policy.Action { return policy.S3GetBucketCORS }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketCORS) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketCORS) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetCORS::verify_permission.
func (o *GetBucketCORS) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is the storage-class check, then RGWGetCORS::execute: a bucket
// without a configuration is ERR_NO_CORS_FOUND, 404 NoSuchCORSConfiguration
// with no message.
func (o *GetBucketCORS) Execute(_ context.Context, r *Request) error {
	if err := checkDestPlacement(r, o.StorageClass); err != nil {
		return err
	}
	c, err := bucketCORS(r.BucketRec)
	if err != nil {
		return err
	}
	if c == nil {
		return ErrNoCORSFound
	}
	o.Config = c
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketCORS) Complete(context.Context, *Request) {}

// PutBucketCORS is RGWPutCORS
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6075-L6108 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6742-L6775 at v20.2.4).
// Params, when set, is the handler's get_params, which fills Config once the
// requester is authorized; a refusal comes back already mapped to an S3
// error.
type PutBucketCORS struct {
	StorageClass string
	Params       func(ctx context.Context, o *PutBucketCORS) error
	// Config is the configuration to store, which replaces the stored one
	// whole.
	Config *cors.Configuration
}

var _ Op = (*PutBucketCORS)(nil)

// Name is RGWPutCORS::name.
func (*PutBucketCORS) Name() string { return "put_cors" }

// Action is s3:PutBucketCORS.
func (*PutBucketCORS) Action() policy.Action { return policy.S3PutBucketCORS }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutBucketCORS) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutBucketCORS) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWPutCORS::verify_permission.
func (o *PutBucketCORS) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is the storage-class check, then RGWPutCORS::execute: Params, then
// the configuration's encoding set over the bucket as the attr alone through
// retryBucketWrite, which
// authorizes each retry again and stops when the name names another bucket.
// radosgw's tries merge the attrs the request started with, so a retry
// writes back what the write it lost to had changed, and authorize no retry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6103-L6107 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6770-L6774 at v20.2.4;
// docs/exclusions.md).
func (o *PutBucketCORS) Execute(ctx context.Context, r *Request) error {
	if err := checkDestPlacement(r, o.StorageClass); err != nil {
		return err
	}
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	if o.Config == nil {
		return fmt.Errorf("%w: no CORS configuration to store", ErrInternalError)
	}
	set := map[string][]byte{AttrCORS: o.Config.Encode()}
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, set, nil)
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutBucketCORS) Complete(context.Context, *Request) {}

// DeleteBucketCORS is RGWDeleteCORS
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6110-L6153 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6777-L6820 at v20.2.4).
type DeleteBucketCORS struct {
	StorageClass string
}

var _ Op = (*DeleteBucketCORS)(nil)

// Name is RGWDeleteCORS::name.
func (*DeleteBucketCORS) Name() string { return "delete_cors" }

// Action is s3:PutBucketCORS: "No separate delete permission".
func (*DeleteBucketCORS) Action() policy.Action { return policy.S3PutBucketCORS }

// OpMask is RGW_OP_TYPE_WRITE, which RGWDeleteCORS declares.
func (*DeleteBucketCORS) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*DeleteBucketCORS) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWDeleteCORS::verify_permission.
func (o *DeleteBucketCORS) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is the storage-class check, then RGWDeleteCORS::execute. Each of
// radosgw's tries reads the
// configuration from the attrs the request loaded, which no retry changes,
// so it is looked for once here, in the bucket Init loaded: none is -ENOENT,
// which send_response answers 204 as it answers success
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3843-L3852 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4294-L4303 at v20.2.4),
// with nothing written. Otherwise the attr is removed from the bucket
// through retryBucketWrite, which authorizes each retry again and stops when
// the name names another bucket. radosgw decodes the configuration first
// and answers one that does not decode 500, removing nothing; rgw-go removes
// it (docs/exclusions.md, "DeleteBucketCors removes a CORS configuration
// that does not decode").
func (o *DeleteBucketCORS) Execute(ctx context.Context, r *Request) error {
	if err := checkDestPlacement(r, o.StorageClass); err != nil {
		return err
	}
	if _, ok := r.BucketRec.Attrs[AttrCORS]; !ok {
		return nil
	}
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, nil, []string{AttrCORS})
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*DeleteBucketCORS) Complete(context.Context, *Request) {}

// OptionsCORS is RGWOptionsCORS
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1746-L1764 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6155-L6207 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1902-L1920 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6822-L6874 at v20.2.4),
// the CORS preflight at bucket or object scope, which the bucket's
// configuration alone answers. Its results are what the response's
// Access-Control headers come from, Rule.ResponseHeaders(Origin, Method,
// Headers, ...).
//
// It is the one op an Unverified identity may run (RefuseUnverified), whose
// sender has shown nothing but a key's id, so it reads nothing of the
// identity and its answer is the same for every key that resolves: it asks
// for no op mask and checks no permission, its bucket loads as an anonymous
// request's does, and LogUsage accounts it as an anonymous request
// (docs/exclusions.md, "A preflight's answer says nothing of its key's
// user"). What it does depend on is the bucket the request names, which the
// handler resolves under the identity's tenant when the name names none, as
// radosgw does.
type OptionsCORS struct {
	StorageClass string

	// Rule is the rule that allows the request.
	Rule *cors.Rule
	// Origin and Method are the request's Origin and
	// Access-Control-Request-Method.
	Origin, Method string
	// Headers is its Access-Control-Request-Headers as
	// cors.ParseRequestHeaders splits it, each of which Rule allows; empty
	// when it asks for none.
	Headers []string
}

var _ Op = (*OptionsCORS)(nil)

// Name is RGWOptionsCORS::name, OptionsCORSName.
func (*OptionsCORS) Name() string { return OptionsCORSName }

// Action is ActionNone: radosgw evaluates no policy for the preflight.
func (*OptionsCORS) Action() policy.Action { return policy.ActionNone }

// OpMask is none. radosgw's is RGW_OP_TYPE_READ, which verify_op_mask checks
// against the key's user, so a forged preflight would tell its sender
// whether that user may read (docs/exclusions.md).
func (*OptionsCORS) OpMask() uint32 { return 0 }

// Init is rgw_build_bucket_policies' load of the bucket
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L523-L574 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L553-L604 at v20.2.4),
// in the refusals whose answer is the same for every key, in its order: a
// missing bucket is NoSuchBucket, one of another zonegroup
// PermanentRedirect, r.BucketRec set either way, then the storage-class
// check, then a bucket ACL that does not decode decode_policy's -EIO, an
// UnknownError, which the load records and answers after the placement's
// refusal (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L576-L583 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L606-L613 at v20.2.4).
// radosgw lets a system request from the master
// zonegroup through to another zonegroup's bucket, and a preflight on a
// system user's key is one; that would tell the sender the key is a system
// user's, so the preflight is redirected whatever its key. The load's
// refusals that radosgw lifts for a system key, or on Tentacle an admin's,
// of a suspended bucket and of a policy that does not parse, rgw-go makes in
// its authorizer, which the preflight never calls, and it loads no owner
// for a quota (docs/exclusions.md).
func (o *OptionsCORS) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	if !r.Env.Zone.ZoneGroup().Equals(rec.Info.Zonegroup) {
		return ErrPermanentRedirect
	}
	if perr := checkDestPlacement(r, o.StorageClass); perr != nil {
		return perr
	}
	_, err = BucketACLFor(ctx, rec)
	return err
}

// VerifyPermission is RGWOptionsCORS::verify_permission, which allows
// every request.
func (*OptionsCORS) VerifyPermission(context.Context, *Request) error { return nil }

// Execute is RGWOptionsCORS::execute, in its order: a configuration that
// does not decode is UnknownError; a request without an Origin, or without
// an Access-Control-Request-Method, is 400 InvalidArgument; a bucket without
// a configuration, or one with no rule that cors.Match finds for the
// request, is -ENOENT, which send_response answers as 403 AccessDenied
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3854-L3875 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4305-L4326 at v20.2.4).
// Each header is read as RGWEnv holds it, the last of a repeated one, and
// one sent empty is sent.
func (o *OptionsCORS) Execute(_ context.Context, r *Request) error {
	c, err := bucketCORS(r.BucketRec)
	if err != nil {
		return err
	}
	origin, ok := HeaderValue(r.Header, "Origin")
	if !ok {
		return fmt.Errorf("%w: a preflight without an origin", ErrInvalidArgument)
	}
	method, ok := HeaderValue(r.Header, "Access-Control-Request-Method")
	if !ok {
		return fmt.Errorf("%w: a preflight without access-control-request-method", ErrInvalidArgument)
	}
	if c == nil {
		return fmt.Errorf("%w: bucket %s has no cors configuration", ErrAccessDenied, r.BucketRec.Info.Bucket.Name)
	}
	var headers []string
	if v, ok := HeaderValue(r.Header, "Access-Control-Request-Headers"); ok {
		headers = cors.ParseRequestHeaders(v)
	}
	rule := c.Match(origin, method, headers)
	if rule == nil {
		return fmt.Errorf("%w: no cors rule of bucket %s allows the preflight", ErrAccessDenied, r.BucketRec.Info.Bucket.Name)
	}
	o.Rule, o.Origin, o.Method, o.Headers = rule, origin, method, headers
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*OptionsCORS) Complete(context.Context, *Request) {}
