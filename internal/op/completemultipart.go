package op

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// defaultPartUploadLimit is rgw_multipart_part_upload_limit's default
// (rgw.yaml.in at v19.2.6 and v20.2.4), for a configuration that cannot be
// read.
const defaultPartUploadLimit = 10000

// CompleteMultipart is RGWCompleteMultipart, CompleteMultipartUpload
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6345-L6541 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7021-L7431 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1580-L1595 at v19.2.6;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4064-L4108 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4565-L4644 at v20.2.4).
// Execute reads the completion document from r.Body, through the
// authenticator's verifying reader to its final Read, before it parses it,
// and completes the upload through MultipartStore.Complete.
type CompleteMultipart struct {
	UploadID string
	// IfMatch and IfNoneMatch are the headers' values, nil when absent: a
	// header present with an empty value is a condition, as v20.2.4 takes it
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4572-L4573 at v20.2.4).
	// rgw-go applies them on both releases (Upload.IfMatch).
	IfMatch, IfNoneMatch *string
	// ChecksumField is the request's checksum fields, which
	// SuppliedForComplete reads (ChecksumInput).
	ChecksumField func(name string) (string, bool)

	// Parts is the document's parts as ParseCompleteMultipart reads them.
	Parts []CompletePart
	ETag  string
	Size  uint64
	Mtime time.Time
	// VersionID is the id of the version the store completed
	// (ResponseVersionID), "" for none: for a completion it finishes rather
	// than writes, the version that earlier completion wrote.
	VersionID string
	// Checksum is the combined checksum the store stored, nil for none.
	Checksum *cksum.Cksum
}

var _ Op = (*CompleteMultipart)(nil)

// Name is RGWCompleteMultipart::name.
func (o *CompleteMultipart) Name() string { return "complete_multipart" }

// Action is the action RGWCompleteMultipart::verify_permission authorizes.
func (o *CompleteMultipart) Action() policy.Action { return policy.S3PutObject }

// OpMask is RGW_OP_TYPE_WRITE.
func (o *CompleteMultipart) OpMask() uint32 { return OpTypeWrite }

// Init is init_permissions' load of the bucket, a missing one NoSuchBucket.
func (o *CompleteMultipart) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWCompleteMultipart::verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6345-L6360 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7021-L7036 at v20.2.4).
func (o *CompleteMultipart) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
}

// Execute is RGWCompleteMultipart::execute in its order
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6367-L6416 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7163-L7211 at v20.2.4):
// get_params, whose empty upload id is -ENOTSUP, 500 UnknownError, and whose
// body is read as ReadParamBody reads it under rgw_max_put_param_size; an
// empty body, a document that does not parse or holds no part, MalformedXML;
// more parts than rgw_multipart_part_upload_limit once repeats collapse,
// InvalidRange (-ERANGE); then, where execute reads the upload's info
// (readCompleteUpload), an upload of a crypt mode rgw-go does not know
// refused (refuseUnknownCryptMode) and the checksum the request supplies
// (completeChecksum); then the completion, whose head takes the request id
// as its tag, at VersionForWrite's instance.
func (o *CompleteMultipart) Execute(ctx context.Context, r *Request) error {
	if o.UploadID == "" {
		return fmt.Errorf("%w: no upload id", ErrUnknown)
	}
	body, err := ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("%w: an empty completion", ErrMalformedXML)
	}
	if o.Parts, err = ParseCompleteMultipart(body); err != nil {
		return err
	}
	if limit := partUploadLimit(r); int64(len(o.Parts)) > limit {
		return fmt.Errorf("%w: %d parts over rgw_multipart_part_upload_limit %d", ErrInvalidRange, len(o.Parts), limit)
	}
	if aerr := aliasedUpload(o.UploadID); aerr != nil {
		return aerr
	}
	up, err := readCompleteUpload(ctx, r, o.UploadID)
	if err != nil {
		return err
	}
	if cerr := refuseUnknownCryptMode(up); cerr != nil {
		return cerr
	}
	supplied, err := completeChecksum(r, up, ChecksumInput{Field: o.ChecksumField})
	if err != nil {
		return err
	}
	res, err := r.Env.Multipart.Complete(ctx, &Upload{
		ID: o.UploadID, Bucket: r.BucketRec, Key: writeKey(r), WriteTag: r.ID,
		IfMatch: storeCondition(o.IfMatch), IfNoneMatch: storeCondition(o.IfNoneMatch), SuppliedChecksum: supplied,
	}, o.Parts)
	if err != nil {
		return err
	}
	o.ETag, o.Size, o.Mtime, o.Checksum = res.ETag, res.Size, res.Mtime, res.Checksum
	o.VersionID = ResponseVersionID(r.BucketRec, res.Version)
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *CompleteMultipart) Complete(context.Context, *Request) {}

// readCompleteUpload is execute's read of upload id's info, nil for an
// upload GetUpload does not find, which MultipartStore.Complete answers, as
// it answers a completion retried after it succeeded.
func readCompleteUpload(ctx context.Context, r *Request, id string) (*Upload, error) {
	up, err := r.Env.Multipart.GetUpload(ctx, r.BucketRec, r.Object, id)
	if errors.Is(err, ErrNoSuchUpload) {
		return nil, nil
	}
	return up, err
}

// refuseUnknownCryptMode refuses with NotImplemented the completion of an
// upload whose crypt mode is not empty and is one rgw-go does not know, as
// v20.2.5's GCM modes are, before anything is written. A v20.2.5 radosgw's
// completion of such an upload writes the plaintext size and each part's
// number and salt
// (https://github.com/ceph/ceph/blob/v20.2.5/src/rgw/driver/rados/rgw_sal_rados.cc#L4486-L4500 at v20.2.5),
// which its read of the object needs; rgw-go's writes neither, so no gateway
// could return the data of the object it completed. A v19.2.7 or v20.2.4
// radosgw, which takes the mode for none, completes it
// (docs/differences.md).
func refuseUnknownCryptMode(up *Upload) error {
	if up == nil || storedCryptMode(up.Attrs) != cryptUnknown {
		return nil
	}
	return fmt.Errorf("%w: upload %s has crypt mode %q, which rgw-go does not know", ErrNotImplemented, up.ID, up.Attrs[meta.AttrCryptMode])
}

// partUploadLimit is rgw_multipart_part_upload_limit, its default when the
// configuration cannot be read.
func partUploadLimit(r *Request) int64 {
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Int64("rgw_multipart_part_upload_limit"); err == nil {
			return v
		}
	}
	return defaultPartUploadLimit
}
