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
// (rgw_op.cc:3806-3989 and :4142-4560 at v19.2.6, :4015-4197 and :4351-4867
// at v20.2.4; rgw_rest_s3.cc:2597-2704 at v19.2.6). The handler fills the
// inputs. Without a copy source Execute streams Body through
// MultipartStore.PutPart, which reads it to its final Read, so the
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

	// CopySource is set for UploadPartCopy, whose source the next fields name.
	CopySource           bool
	SrcTenant, SrcBucket string
	SrcKey               meta.ObjKey
	// Range is x-amz-copy-source-range as sent, nil when absent: a header
	// present with an empty value is a range, which the parse refuses
	// (rgw_op.cc:3869-3876 at v19.2.6, :4078-4085 at v20.2.4).
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
// then RGWPutObj::init_processing in its order (rgw_op.cc:3806-3918 at
// v19.2.6, :4015-4127 at v20.2.4): a copy source's bucket, NoSuchBucket when
// missing, and its range, parsed by ParseCopySourceRange; then the refusal of
// a public ACL under the bucket's block of public ACLs, which radosgw makes
// for the canned ACLs and rgw-go, as for PutObject, for any public policy
// (docs/exclusions.md). The source's head is read with its first chunk, as
// verify_permission's read_obj_policy reads it with prefetch_data set
// (:3926-3934 at v19.2.6, :4135-4143 at v20.2.4).
func (o *UploadPart) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	if o.CopySource {
		if o.srcRec, err = r.Env.Buckets.GetBucket(ctx, o.SrcTenant, o.SrcBucket); err != nil {
			return err
		}
		if o.Range != nil {
			if o.first, o.last, err = ParseCopySourceRange(*o.Range); err != nil {
				return err
			}
		}
	}
	if blockPublicACLs(rec) && o.ACL.IsPublic() {
		return fmt.Errorf("%w: a public acl under a block of public acls", ErrAccessDenied)
	}
	if !o.CopySource {
		return nil
	}
	if o.src, err = r.Env.Objects.PrefetchObject(ctx, o.srcRec, o.SrcKey); err != nil {
		return err
	}
	if o.src == nil {
		o.src = &ObjectState{Bucket: o.srcRec, Key: o.SrcKey}
	}
	return nil
}

// VerifyPermission is RGWPutObj::verify_permission (rgw_op.cc:3920-3988 at
// v19.2.6, :4129-4197 at v20.2.4): for a copy, read_obj_policy's rule for a
// missing source and its decode of the source's ACL, then the source's read;
// then s3:PutObject on the destination. The destination is checked first
// all the same, so that the refusals radosgw makes on it in
// init_permissions, before verify_permission and never overridden, come
// out marked and ahead of every source check; its other refusal comes last.
//
// radosgw authorizes the source's read against the object's ACL, the
// source bucket's ACL taking part only through Swift's READ_OBJS
// (verify_object_permission_no_policy, rgw_common.cc:1560-1608 at v19.2.6).
// rgw-go needs READ on both, the bucket check then the object check, each
// with the source bucket's policy and the identity's, which decide before
// either ACL, as for CopyObject (docs/exclusions.md).
func (o *UploadPart) VerifyPermission(ctx context.Context, r *Request) error {
	destErr := VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
	if !o.CopySource || IsBeforeVerify(destErr) {
		return destErr
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

// Execute is RGWPutObj_ObjStore::verify_params (rgw_rest.cc:1049-1059 at
// v19.2.6 and v20.2.4), which runs after verify_permission, then
// RGWPutObj::execute for a part in its order (rgw_op.cc:4142-4560 at
// v19.2.6, :4351-4867 at v20.2.4): the key; the quota for the request's
// length, unless it is chunked or the request is a system request; then the
// part, whose head's attrs are the request's with the policy, written under
// the request id. A copy naming a source version is refused first, until
// versioning is served.
func (o *UploadPart) Execute(ctx context.Context, r *Request) error {
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
// (rgw_op.cc:4244-4260 at v19.2.6, :4453-4469 at v20.2.4), a NoSuchUpload
// ahead of every source error; the source's cloud tier and its existence
// (:4297-4331 at v19.2.6, :4510-4544 at v20.2.4); then the range against
// the source's accounted size, and the copy of that range from the one
// state the request read.
//
// radosgw checks the cloud tier only for a copy without a range, and reads
// no x-amz-copy-source-if-* header for a part: only RGWCopyObj's get_params
// reads them (rgw_rest_s3.cc:3511-3514 at v19.2.6, :3791-3794 at v20.2.4).
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
// asks for the next piece (rgw_op.cc:4386-4400 at v19.2.6, :4618-4632 at
// v20.2.4; rgw_sal.cc:429-449 at v19.2.6, :426-446 at v20.2.4), after the
// leading pieces were written, where rgw-go refuses before it copies.
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
// (services/svc_tier_rados.h:42-55 at v19.2.6 and v20.2.4), so key "a" with
// id "b.2~X" is key "a.b"'s upload "2~X", while the request is authorized
// as key "a" (docs/ceph-upstream-bugs.md, "radosgw lets an upload id
// address another key's multipart upload"). No gateway makes such an id:
// radosgw's is "2~" and 31 characters of gen_rand_alphanumeric's
// A-Za-z0-9-_ (driver/rados/rgw_sal_rados.cc:3281-3283 at v19.2.6,
// :4125-4127 at v20.2.4; common/random_string.cc:48), and rgw-go's the
// same. Each op answers it where a missing upload would be answered
// (docs/exclusions.md).
func aliasedUpload(id string) error {
	if !strings.Contains(id, ".") {
		return nil
	}
	return fmt.Errorf("%w: upload id %q holds a dot", ErrNoSuchUpload, id)
}
