package op

import (
	"context"
	"errors"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// ListParts is RGWListMultipart, ListParts (rgw_op.cc:6652-6694 at v19.2.6,
// :7576-7633 at v20.2.4; rgw_rest.cc:1597-1622 at v19.2.6, :1602-1627 at
// v20.2.4). The handler fills the inputs; Execute lists a page of the
// upload's parts through MultipartStore.ListParts.
type ListParts struct {
	UploadID string
	// Marker is part-number-marker, 0 when absent.
	Marker int
	// MaxParts is max-parts as parse_value_and_bound bounds it.
	MaxParts int

	// Upload is the upload GetUpload read, nil when there is none.
	Upload *Upload
	// Owner is the owner of the meta object's ACL, the result's Owner.
	Owner acl.Owner
	// StorageClass is the upload's, STANDARD when its placement names none.
	StorageClass string
	Result       ListPartsResult

	// loadErr is a failure to read the upload or the object, which Execute
	// answers once the requester is authorized.
	loadErr error
}

var _ Op = (*ListParts)(nil)

// Name is RGWListMultipart::name.
func (o *ListParts) Name() string { return "list_multipart" }

// Action is the action RGWListMultipart::verify_permission authorizes.
func (o *ListParts) Action() policy.Action { return policy.S3ListMultipartUploadParts }

// OpMask is RGW_OP_TYPE_READ.
func (o *ListParts) OpMask() uint32 { return OpTypeRead }

// Init is init_permissions' load of the bucket, a missing one NoSuchBucket,
// then read_permissions' read of the object policy, which for a request
// naming an upload is the upload's meta object's (read_obj_policy,
// rgw_op.cc:398-418 at v19.2.6, :428-448 at v20.2.4). r.ObjState becomes the
// meta object, in the multipart namespace, with the upload's attrs; a missing
// upload leaves it a missing object, whose answer the authorizer gives as
// read_obj_policy's rule does (:421-447 at v19.2.6), so a missing upload is
// never NoSuchUpload there. Without an upload id the policy is the object's
// own, as radosgw reads it. A read that fails otherwise is answered only
// once the requester is authorized, the state standing for an object whose
// policy is the bucket owner's default, so that a refused requester gets 403
// whatever the store holds, where radosgw answers the failure
// (docs/exclusions.md).
func (o *ListParts) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	if o.UploadID == "" {
		var st *ObjectState
		if st, err = r.Env.Objects.StatObject(ctx, rec, r.Object); err != nil {
			o.loadErr, st = err, &ObjectState{Bucket: rec, Key: r.Object, Exists: true}
		}
		if st == nil {
			st = &ObjectState{Bucket: rec, Key: r.Object}
		}
		r.ObjState = st
		return nil
	}
	key := meta.ObjKey{Name: meta.MultipartMetaName(r.Object.Name, o.UploadID), NS: meta.NSMultipart}
	var up *Upload
	if err = aliasedUpload(o.UploadID); err == nil {
		up, err = r.Env.Multipart.GetUpload(ctx, rec, r.Object, o.UploadID)
	}
	switch {
	case errors.Is(err, ErrNoSuchUpload):
		r.ObjState = &ObjectState{Bucket: rec, Key: key}
		return nil
	case err != nil:
		o.loadErr, r.ObjState = err, &ObjectState{Bucket: rec, Key: key, Exists: true}
		return nil //nolint:nilerr // Execute answers it once the requester is authorized
	}
	o.Upload = up
	r.ObjState = &ObjectState{Bucket: rec, Key: key, Exists: true, Attrs: up.Attrs}
	return nil
}

// VerifyPermission is RGWListMultipart::verify_permission
// (rgw_op.cc:6652-6662 at v19.2.6, :7576-7586 at v20.2.4).
func (o *ListParts) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyObjectPermission(ctx, r, policy.S3ListMultipartUploadParts, acl.PermFor(policy.S3ListMultipartUploadParts))
}

// Execute is RGWListMultipart::execute (rgw_op.cc:6669-6694 at v19.2.6,
// :7593-7633 at v20.2.4): get_info's NoSuchUpload, for an empty upload id
// too, whose meta object "<key>..meta" no upload has; the meta object's ACL,
// whose owner the result names and which is -EIO when it does not decode;
// the upload's storage class; then the page.
func (o *ListParts) Execute(ctx context.Context, r *Request) error {
	if o.loadErr != nil {
		return o.loadErr
	}
	if o.Upload == nil {
		return fmt.Errorf("%w: upload %q of %s", ErrNoSuchUpload, o.UploadID, r.Object.Name)
	}
	if b, ok := o.Upload.Attrs[meta.AttrACL]; ok {
		p, err := decodeACL(b, "upload "+o.UploadID)
		if err != nil {
			return err
		}
		o.Owner = p.Owner
	}
	o.StorageClass = o.Upload.Placement.CanonicalStorageClass()
	res, err := r.Env.Multipart.ListParts(ctx, o.Upload, o.Marker, o.MaxParts)
	if err != nil {
		return err
	}
	o.Result = res
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *ListParts) Complete(context.Context, *Request) {}
