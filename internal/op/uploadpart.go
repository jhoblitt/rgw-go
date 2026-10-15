package op

import (
	"context"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// UploadPart is RGWPutObj with an uploadId, UploadPart and UploadPartCopy
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3806-L3989
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4142-L4560 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4015-L4197
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4351-L4867 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2597-L2704 at v19.2.6).
// The handler fills the inputs. Without a copy source Execute streams Body
// through MultipartStore.PutPart, which reads it to its final Read, so the
// authenticator's verdict on it comes before the part is registered; with
// one it copies Range of the source through MultipartStore.CopyPart.
type UploadPart struct {
	UploadID   string
	PartNumber int
	// Body is r.Body, the authenticator's verifying reader; nil for a copy.
	Body io.Reader
	// Size is r.ContentLength, -1 for a chunked body of unknown length.
	Size int64
	// Attrs are the handler's request attrs, each NUL-terminated.
	Attrs map[string][]byte
	// ACL is create_s3_policy's for the part's head.
	ACL acl.Policy
	// ContentMD5 is the decoded Content-MD5, nil when absent.
	ContentMD5 []byte
	// StorageClass is x-amz-storage-class, "" when absent: the part keeps the
	// upload's placement, but the class must be one the zone has.
	StorageClass string
	// CannedACL is x-amz-acl, "" when absent.
	CannedACL string
	// Params, when set, is the part of the protocol's get_params that reads
	// the request alone: Init calls it where RGWPutObj::init_processing calls
	// get_params
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3911 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4120 at v20.2.4).
	// It fills the inputs it reads.
	Params func(ctx context.Context, o *UploadPart) error
	// Authorized, when set, is the rest of get_params and the checks
	// radosgw makes in execute, those that read stored state the ACL's
	// grantees among them: Execute calls it once the requester is authorized,
	// the bucket's default encryption, the storage class and a public canned
	// ACL refused. It fills the inputs it reads, the ACL among them.
	Authorized func(ctx context.Context, o *UploadPart) error

	// SourceErr is the protocol's refusal of an x-amz-copy-source it cannot
	// parse, which Init returns where init_processing parses the source.
	SourceErr error
	// CopySource is set for UploadPartCopy, whose source the next fields name.
	CopySource           bool
	SrcTenant, SrcBucket string
	SrcKey               meta.ObjKey
	// Range is x-amz-copy-source-range as sent, nil when absent: a header
	// present with an empty value is a range, which the parse refuses
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3869-L3876 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4078-L4085 at v20.2.4).
	Range *string
	// IfMatch, IfNoneMatch, IfModifiedSince and IfUnmodifiedSince are the
	// x-amz-copy-source-if-* headers' values, nil when absent: a header
	// present with an empty value is a condition, as CopyObject takes it.
	IfMatch, IfNoneMatch               *string
	IfModifiedSince, IfUnmodifiedSince *string

	ETag  string
	Mtime time.Time

	srcRec      *BucketRecord
	src         *ObjectState
	first, last uint64
}

var _ Op = (*UploadPart)(nil)

// Name is RGWPutObj::name, the op name and usage category of both forms.
func (o *UploadPart) Name() string { return "put_obj" }

// Action is the destination's action; VerifyPermission also authorizes a
// copy source's read.
func (o *UploadPart) Action() policy.Action { return policy.S3PutObject }

// OpMask is RGW_OP_TYPE_WRITE.
func (o *UploadPart) OpMask() uint32 { return OpTypeWrite }

// Init is init_permissions' load of the bucket, a missing one NoSuchBucket,
// then what RGWPutObj::init_processing makes of the request alone
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3806-L3918 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4015-L4127 at v20.2.4):
// SourceErr, the copy source's parse; its range, parsed by
// ParseCopySourceRange; then Params. The checks of stored state radosgw
// makes there and in init_permissions wait for the requester to be
// authorized, so that a refused requester learns nothing of the bucket, the
// upload or the source: VerifyPermission checks a copy's storage class and
// reads its source once the destination and the op mask allow the request,
// and Execute checks the storage class of a part with a body and the block
// of public ACLs (docs/exclusions.md).
func (o *UploadPart) Init(ctx context.Context, r *Request) error {
	err := loadBucket(ctx, r)
	if err != nil {
		return err
	}
	if o.SourceErr != nil {
		return o.SourceErr
	}
	if o.CopySource && o.Range != nil {
		if o.first, o.last, err = ParseCopySourceRange(*o.Range); err != nil {
			return err
		}
	}
	if o.Params != nil {
		return o.Params(ctx, o)
	}
	return nil
}

// VerifyPermission is RGWPutObj::verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3920-L3988 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4129-L4197 at v20.2.4):
// for a copy, read_obj_policy's rule for a missing source and its decode of
// the source's ACL, then the source's read; then s3:PutObject on the
// destination. The destination is checked first all the same, and a refusal
// there that Run would not let an admin through is answered before the
// source is touched. For a copy, the op mask comes next, which radosgw
// checks before verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L208 at v19.2.6
// and v20.2.4), then the request's storage class, which init_permissions
// checks first
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L576-L583 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L606-L613 at v20.2.4).
// Only then are the source's bucket, NoSuchBucket when missing, and its
// head read, the head with its first chunk, as read_obj_policy reads it
// with prefetch_data set
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3926-L3934 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4135-L4143 at v20.2.4),
// where radosgw loads the source bucket in init_processing
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3854-L3866 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4063-L4075 at v20.2.4),
// before the op mask, and reads the source before the destination's check.
//
// radosgw authorizes the source's read against the object's ACL, the
// source bucket's ACL taking part only through Swift's READ_OBJS
// (verify_object_permission_no_policy,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1560-L1608 at v19.2.6).
// rgw-go needs READ on both, the bucket check then the object check, each
// with the source bucket's policy and the identity's, which decide before
// either ACL, as for CopyObject (docs/exclusions.md).
func (o *UploadPart) VerifyPermission(ctx context.Context, r *Request) error {
	destErr := VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
	if !o.CopySource || IsBeforeVerify(destErr) {
		return destErr
	}
	if destErr != nil && (!r.Identity.Admin || !isAccessDenial(destErr)) {
		return destErr
	}
	if err := verifyOpMask(o, r); err != nil {
		return err
	}
	if err := checkDestPlacement(r, o.StorageClass); err != nil {
		return err
	}
	var err error
	if o.srcRec, err = r.Env.Buckets.GetBucket(ctx, o.SrcTenant, o.SrcBucket); err != nil {
		return err
	}
	if o.src, err = r.Env.Objects.PrefetchObject(ctx, o.srcRec, o.SrcKey); err != nil {
		return err
	}
	if o.src == nil {
		o.src = &ObjectState{Bucket: o.srcRec, Key: o.SrcKey}
	}
	a := policy.S3GetObject
	if o.SrcKey.Instance != "" {
		a = policy.S3GetObjectVersion
	}
	perm := acl.PermFor(a)
	if !o.src.Exists {
		if err := VerifyObjectPermissionIn(ctx, r, a, perm, o.srcRec, o.src); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", ErrNoSuchKey, o.SrcKey.Name)
	}
	if _, err := ObjectACLFor(ctx, o.src, o.srcRec); err != nil {
		return err
	}
	if err := VerifyBucketPermissionIn(ctx, r, a, perm, o.srcRec, o.SrcKey); err != nil {
		return err
	}
	if err := VerifyObjectPermissionIn(ctx, r, a, perm, o.srcRec, o.src); err != nil {
		return err
	}
	return destErr
}

// Execute is RGWPutObj_ObjStore::verify_params
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1049-L1059 at v19.2.6
// and v20.2.4), which runs after verify_permission, then RGWPutObj::execute
// for a part in its order
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4142-L4560 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4351-L4867 at v20.2.4):
// the key; the quota for the request's length, unless it is chunked or the
// request is a system request; then the part, whose head's attrs are the
// request's with the policy, written under the request id. First come the
// checks of stored state that radosgw makes before verify_permission, which
// rgw-go makes once the requester is authorized: the request's storage class
// must be one the zone has
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L576-L583 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L606-L613 at v20.2.4),
// which VerifyPermission has checked for a copy before it read the source; a
// bucket with a default encryption is refused; a public canned ACL under the
// bucket's block of public ACLs is refused
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3903-L3909 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4112-L4118 at v20.2.4);
// then Authorized runs, and a public ACL it built from the grant headers is
// refused too, as PutObject refuses one, where radosgw stores it
// (docs/exclusions.md). Then a copy naming a source version is refused, until
// versioning is served.
func (o *UploadPart) Execute(ctx context.Context, r *Request) error {
	if !o.CopySource {
		if err := checkDestPlacement(r, o.StorageClass); err != nil {
			return err
		}
	}
	if err := bucketEncryptionUnserved(r.BucketRec); err != nil {
		return err
	}
	block := blockPublicACLs(r.BucketRec)
	if block && isPublicCannedACL(o.CannedACL) {
		return fmt.Errorf("%w: a public canned acl under a block of public acls", ErrAccessDenied)
	}
	if o.Authorized != nil {
		if err := o.Authorized(ctx, o); err != nil {
			return err
		}
	}
	if block && o.ACL.IsPublic() {
		return fmt.Errorf("%w: a public acl under a block of public acls", ErrAccessDenied)
	}
	if o.CopySource && o.SrcKey.Instance != "" {
		return fmt.Errorf("%w: copying version %q of %s is not implemented", ErrNotImplemented, o.SrcKey.Instance, o.SrcKey.Name)
	}
	if maxPut := confSize(r, "rgw_max_put_size", defaultMaxPutSize); o.Size > int64(maxPut) { //nolint:gosec // verify_params compares in off_t
		return fmt.Errorf("%w: %d bytes over rgw_max_put_size %d", ErrEntityTooLarge, o.Size, maxPut)
	}
	if r.Object.Name == "" {
		return ErrInvalidArgument
	}
	if o.Size >= 0 && !r.Identity.System {
		if err := r.Env.Stats.CheckQuota(ctx, r.BucketRec, r.BucketRec.Info.Owner, o.Size, 1); err != nil {
			return err
		}
	}
	attrs := maps.Clone(o.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[meta.AttrACL] = encodeAt(o.ACL, r.Env.Zone.Release())
	up := &Upload{ID: o.UploadID, Bucket: r.BucketRec, Key: r.Object, Attrs: attrs}
	var (
		res *PartResult
		err error
	)
	if o.CopySource {
		res, err = o.copyPart(ctx, r, up)
	} else if err = aliasedUpload(o.UploadID); err == nil {
		res, err = r.Env.Multipart.PutPart(ctx, up, o.PartNumber, o.Body, PutParams{Attrs: attrs, Size: o.Size, ContentMD5: o.ContentMD5, Tag: r.ID})
	}
	if err != nil {
		return err
	}
	o.ETag, o.Mtime = res.ETag, res.Mtime
	return nil
}

// copyPart is execute's copy of a part from its source in its order: the
// upload's info, which get_info reads before anything of the source
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4244-L4260 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4453-L4469 at v20.2.4),
// a NoSuchUpload ahead of every source error; the source's cloud tier and
// its existence
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4297-L4331 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4510-L4544 at v20.2.4);
// then the range against the source's accounted size, and the copy of
// that range from the one state the request read.
//
// radosgw checks the cloud tier only for a copy without a range, and reads
// no x-amz-copy-source-if-* header for a part: only RGWCopyObj's get_params
// reads them
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3511-L3514 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3791-L3794 at v20.2.4).
// rgw-go refuses a cloud-tiered source with a range too, and checks the
// conditions as CopyObject does, against the state the store copies, which
// reads that state's bytes or fails (docs/exclusions.md).
func (o *UploadPart) copyPart(ctx context.Context, r *Request, up *Upload) (*PartResult, error) {
	conds, perr := parseReadConds(o.IfModifiedSince, o.IfUnmodifiedSince, o.IfMatch, o.IfNoneMatch)
	if perr != nil {
		return nil, perr
	}
	if err := aliasedUpload(o.UploadID); err != nil {
		return nil, err
	}
	if _, err := r.Env.Multipart.GetUpload(ctx, r.BucketRec, r.Object, o.UploadID); err != nil {
		return nil, err
	}
	if err := o.checkCloudTier(r); err != nil {
		return nil, err
	}
	// An admin let through a refusal of a missing source reaches here.
	if !o.src.Exists {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchKey, o.SrcKey.Name)
	}
	if err := conds.check(o.src); err != nil {
		return nil, err
	}
	rng, err := o.rangeFor(o.src)
	if err != nil {
		return nil, err
	}
	return r.Env.Multipart.CopyPart(ctx, up, o.PartNumber, o.src, rng)
}

// checkCloudTier is execute's refusal of a source transitioned to a cloud
// tier: on Squid one whose manifest names cloud-s3, on Tentacle one whose
// manifest names either S3 tier type (is_tier_type_s3).
func (o *UploadPart) checkCloudTier(r *Request) error {
	m := o.src.Manifest
	if m == nil {
		return nil
	}
	if r.Env.Zone.Release() < denc.Tentacle {
		if m.TierType != meta.TierTypeCloudS3 {
			return nil
		}
	} else if !isS3Tier(m.TierType) {
		return nil
	}
	return ErrInvalidObjectState.WithMessage("This object was transitioned to cloud-s3")
}

// rangeFor is the part of src a copy reads. Without a range it is the whole
// of src's accounted size, its uncompressed length (lst = accounted_size - 1).
// With one, an empty source gives nothing: range_to_ofs skips its checks and
// the read ends at once. Otherwise a range must lie within the source: one
// starting at or past its end fails range_to_ofs with -ERANGE, and one
// ending past it fails the same way once the copy loop reaches the end and
// asks for the next piece
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4386-L4400 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4618-L4632 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_sal.cc#L429-L449 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_sal.cc#L426-L446 at v20.2.4),
// after the leading pieces were written, where rgw-go refuses before it
// copies.
func (o *UploadPart) rangeFor(src *ObjectState) (ByteRange, error) {
	size := src.Size
	if src.Compression != nil {
		size = src.Compression.OrigSize
	}
	switch {
	case o.Range == nil:
		return ByteRange{Offset: 0, Length: size}, nil
	case size == 0:
		return ByteRange{}, nil
	case o.first >= size || o.last >= size:
		return ByteRange{}, fmt.Errorf("%w: bytes %d-%d of a %d-byte source", ErrInvalidRange, o.first, o.last, size)
	}
	return ByteRange{Offset: o.first, Length: o.last - o.first + 1}, nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *UploadPart) Complete(context.Context, *Request) {}

// aliasedUpload is NoSuchUpload for an upload id holding a ".", and nil for
// any other. RGWMPObj::init names the meta object "<key>.<id>.meta" and the
// part prefix "<key>.<id>" without checking the id
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_tier_rados.h#L42-L55 at v19.2.6
// and v20.2.4), so key "a" with id "b.2~X" is key "a.b"'s upload "2~X",
// while the request is authorized as key "a" (docs/ceph-upstream-bugs.md,
// "radosgw lets an upload id address another key's multipart upload"). No
// gateway makes such an id: radosgw's is "2~" and 31 characters of
// gen_rand_alphanumeric's A-Za-z0-9-_
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3281-L3283 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4125-L4127 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.6/src/common/random_string.cc#L48),
// and rgw-go's the same. Each op answers it where a missing upload would be
// answered (docs/exclusions.md).
func aliasedUpload(id string) error {
	if !strings.Contains(id, ".") {
		return nil
	}
	return fmt.Errorf("%w: upload id %q holds a dot", ErrNoSuchUpload, id)
}
