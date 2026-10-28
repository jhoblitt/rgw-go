package op

import (
	"context"
	"errors"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/sse"
)

// The bucket encryption ops of this file are rgw_op.cc's
// RGWGetBucketEncryption, RGWPutBucketEncryption and
// RGWDeleteBucketEncryption. Each loads the bucket in Init, so a missing
// bucket is NoSuchBucket before the permission check, and authorizes its
// action against the bucket without the bucket's tags, as none of their
// verify_permission calls rgw_iam_add_buckettags. The configuration is the
// AttrBucketEncryption attr, which the write replaces whole and the delete
// removes; no op decodes it and writes it back, so a rule a later release
// encodes, such as ceph main's ServerSideEncryptionConfiguration at (2,1),
// whose blockedEncryptionTypes follows the bucket key
// (https://github.com/ceph/ceph/blob/96ed8b8c780/src/rgw/rgw_bucket_encryption.h#L91-L107 at 96ed8b8c780)
// and which sse's (1,1) encoding drops, stays as stored until a client
// replaces or deletes it. The forward to the metadata master each write
// makes is a no-op on a single zone, which is its own master.

// AttrBucketEncryptionKeyID is RGW_ATTR_BUCKET_ENCRYPTION_KEY_ID
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L175 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L195 at v20.2.4),
// the id of the bucket's SSE-S3 key, which only a radosgw with an SSE-S3
// backend writes, once get_sse_s3_bucket_key has created that key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_crypt.cc#L1002-L1020 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1017-L1035 at v20.2.4).
const AttrBucketEncryptionKeyID = meta.AttrPrefix + "sse-s3.key-id"

// GetBucketEncryption is RGWGetBucketEncryption
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8814-L8841 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9782-L9809 at v20.2.4):
// the bucket's default encryption as stored.
type GetBucketEncryption struct {
	// Config is the stored configuration.
	Config sse.BucketEncryptionConfig
}

var _ Op = (*GetBucketEncryption)(nil)

// Name is RGWGetBucketEncryption::name.
func (*GetBucketEncryption) Name() string { return "get_bucket_encryption" }

// Action is s3:GetBucketEncryption.
func (*GetBucketEncryption) Action() policy.Action { return policy.S3GetBucketEncryption }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketEncryption) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketEncryption) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetBucketEncryption::verify_permission.
func (o *GetBucketEncryption) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWGetBucketEncryption::execute. A bucket without the attr is
// -ENOENT with execute's message, which send_response answers as
// ServerSideEncryptionConfigurationNotFoundError
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3888-L3893 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4339-L4344 at v20.2.4).
// The attr is read as sse.DecodeBucketEncryptionConfig reads it, the bytes
// after the configuration ignored. One that does not decode, an empty one
// among them, is -EIO, which no row of the error table holds, so
// set_req_state_err answers it 500 UnknownError
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L352-L353 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L365-L366 at v20.2.4).
func (o *GetBucketEncryption) Execute(_ context.Context, r *Request) error {
	b, ok := r.BucketRec.Attrs[AttrBucketEncryption]
	if !ok {
		return ErrNoSuchBucketEncryption.WithMessage("The server side encryption configuration was not found")
	}
	d := denc.NewDecoder(b)
	c := sse.DecodeBucketEncryptionConfig(d)
	if err := d.Err(); err != nil {
		return fmt.Errorf("%w: decoding the encryption configuration of bucket %s: %w", ErrUnknown, r.BucketRec.Info.Bucket.Name, err)
	}
	o.Config = c
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketEncryption) Complete(context.Context, *Request) {}

// PutBucketEncryption is RGWPutBucketEncryption
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8757-L8812 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9725-L9780 at v20.2.4).
// Its get_params is the op's own, not the protocol handler's, so Execute
// reads and parses the request's body itself.
type PutBucketEncryption struct{}

var _ Op = (*PutBucketEncryption)(nil)

// Name is RGWPutBucketEncryption::name.
func (*PutBucketEncryption) Name() string { return "put_bucket_encryption" }

// Action is s3:PutBucketEncryption.
func (*PutBucketEncryption) Action() policy.Action { return policy.S3PutBucketEncryption }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutBucketEncryption) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutBucketEncryption) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWPutBucketEncryption::verify_permission.
func (o *PutBucketEncryption) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWPutBucketEncryption::execute. get_params reads the body
// with rgw_max_put_param_size as its limit, as ReadParamBody reads it: a
// request with neither a Content-Length nor a chunked body is 411
// MissingContentLength and one whose Content-Length is over the limit 416
// InvalidRange, both unread. get_params asks for no chunked body, which
// RGWOp::read_all_input does not pass on, so a chunked one is read. Then
// sse.ParseXML, whose every refusal is 400 MalformedXML, and the
// configuration's (1,1) encoding set over the bucket as the attr alone
// through retryBucketWrite, which authorizes each retry again and stops
// when the name names another bucket. radosgw's tries, through
// retry_raced_bucket_write, set it over the attrs that try read too, but
// authorize no retry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8807-L8811 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9775-L9779 at v20.2.4;
// docs/exclusions.md, "A PutBucketAcl that loses a race is retried, and
// every retried bucket write is authorized again").
func (o *PutBucketEncryption) Execute(ctx context.Context, r *Request) error {
	body, err := ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	if err != nil {
		return err
	}
	c, err := sse.ParseXML(body)
	switch {
	case errors.Is(err, sse.ErrMalformedXML):
		return fmt.Errorf("%w: %w", ErrMalformedXML, err)
	case err != nil:
		return fmt.Errorf("parsing the encryption configuration: %w", err)
	}
	set := map[string][]byte{AttrBucketEncryption: encodeAt(c, r.Env.Zone.Release())}
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, set, nil)
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutBucketEncryption) Complete(context.Context, *Request) {}

// DeleteBucketEncryption is RGWDeleteBucketEncryption
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8843-L8867 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9811-L9835 at v20.2.4).
type DeleteBucketEncryption struct{}

var _ Op = (*DeleteBucketEncryption)(nil)

// Name is RGWDeleteBucketEncryption::name.
func (*DeleteBucketEncryption) Name() string { return "delete_bucket_encryption" }

// Action is s3:PutBucketEncryption, which RGWDeleteBucketEncryption
// authorizes.
func (*DeleteBucketEncryption) Action() policy.Action { return policy.S3PutBucketEncryption }

// OpMask is RGW_OP_TYPE_WRITE, which RGWDeleteBucketEncryption declares.
func (*DeleteBucketEncryption) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*DeleteBucketEncryption) Init(ctx context.Context, r *Request) error {
	return loadBucket(ctx, r)
}

// VerifyPermission is RGWDeleteBucketEncryption::verify_permission.
func (o *DeleteBucketEncryption) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWDeleteBucketEncryption::execute: the configuration and the
// SSE-S3 key id removed through retryBucketWrite, which authorizes each retry
// again and stops when the name names another bucket. A bucket with neither
// is rewritten too, as radosgw's put_info rewrites it.
func (o *DeleteBucketEncryption) Execute(ctx context.Context, r *Request) error {
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, nil, []string{AttrBucketEncryption, AttrBucketEncryptionKeyID})
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*DeleteBucketEncryption) Complete(context.Context, *Request) {}
