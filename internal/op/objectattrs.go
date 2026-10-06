package op

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// PutObjectACL is RGWPutACLs at object scope (rgw_op.cc:5738-5928 at
// v19.2.6, :6316-6590 at v20.2.4). Build is the handler's closure over the
// authorizer's policy builder: it receives the object's stored policy once
// Init has read the head, and returns the new policy, or its refusal already
// mapped to an S3 error.
type PutObjectACL struct {
	// Versioned is that the request names a version instance, which selects
	// the action. Init sets it from the request.
	Versioned bool
	Build     func(existing acl.Policy) (acl.Policy, error)
}

var _ Op = (*PutObjectACL)(nil)

// Name is RGWPutACLs::name, which the bucket ACL shares.
func (o *PutObjectACL) Name() string { return "put_acls" }

// Action is the action RGWPutACLs::verify_permission authorizes for an
// object.
func (o *PutObjectACL) Action() policy.Action {
	if o.Versioned {
		return policy.S3PutObjectVersionAcl
	}
	return policy.S3PutObjectAcl
}

// OpMask is RGW_OP_TYPE_WRITE.
func (o *PutObjectACL) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket and the object's head, which read_permissions reads
// for an object update op (rgw_rest.cc:1910-1913 at v19.2.6).
func (o *PutObjectACL) Init(ctx context.Context, r *Request) error {
	o.Versioned = r.Object.Instance != ""
	return statHead(ctx, r)
}

// VerifyPermission is RGWPutACLs::verify_permission for an object.
func (o *PutObjectACL) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	return VerifyObjectPermission(ctx, r, a, acl.PermFor(a))
}

// Execute is RGWPutACLs::execute for an object (rgw_op.cc:5821-5928 at
// v19.2.6, :6467-6590 at v20.2.4): Build from s->object_acl, the head's
// policy or its owner's default (ObjectACLFor); the refusal of a public
// policy under a block of public ACLs; and modify_obj_attrs, which writes the
// object's attrs back whole with the ACL replaced
// (driver/rados/rgw_sal_rados.cc:2401-2419 at v19.2.6, :2994 at v20.2.4). A lost race is
// success, "because acls are immutable".
func (o *PutObjectACL) Execute(ctx context.Context, r *Request) error {
	if err := versioningUnserved(r.BucketRec, r.Object); err != nil {
		return err
	}
	existing, err := ObjectACLFor(ctx, r.ObjState, r.BucketRec)
	if err != nil {
		return err
	}
	p, err := o.Build(existing)
	if err != nil {
		return err
	}
	if blockPublicACLs(r.BucketRec) && p.IsPublic() {
		return fmt.Errorf("%w: a public acl under a block of public acls", ErrAccessDenied)
	}
	err = modifyObjAttr(ctx, r, meta.AttrACL, encodeAt(p, r.Env.Zone.Release()))
	if errors.Is(err, ErrConcurrentModification) {
		return nil
	}
	return err
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *PutObjectACL) Complete(context.Context, *Request) {}

// PutObjectTagging is RGWPutObjTags (rgw_op.cc:1076-1109 at v19.2.6,
// :1275-1327 at v20.2.4) with the tag set the handler parsed.
type PutObjectTagging struct {
	// Versioned is that the request names a version instance, which selects
	// the action. Init sets it from the request.
	Versioned bool
	Set       tags.Set
}

var _ Op = (*PutObjectTagging)(nil)

// Name is RGWPutObjTags::name.
func (o *PutObjectTagging) Name() string { return "put_obj_tags" }

// Action is the action RGWPutObjTags::verify_permission authorizes.
func (o *PutObjectTagging) Action() policy.Action {
	if o.Versioned {
		return policy.S3PutObjectVersionTagging
	}
	return policy.S3PutObjectTagging
}

// OpMask is RGW_OP_TYPE_WRITE.
func (o *PutObjectTagging) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket and the object's head, which read_permissions reads
// for an object update op.
func (o *PutObjectTagging) Init(ctx context.Context, r *Request) error {
	o.Versioned = r.Object.Instance != ""
	return statHead(ctx, r)
}

// VerifyPermission is RGWPutObjTags::verify_permission.
func (o *PutObjectTagging) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	return VerifyObjectPermission(ctx, r, a, acl.PermFor(a))
}

// Execute is RGWPutObjTags::execute: modify_obj_attrs with the tag set as
// get_params encodes it, empty or not, where a lost race is 409
// OperationAborted (rgw_op.cc:1104-1108 at v19.2.6, :1322-1326 at v20.2.4).
func (o *PutObjectTagging) Execute(ctx context.Context, r *Request) error {
	if err := versioningUnserved(r.BucketRec, r.Object); err != nil {
		return err
	}
	if r.ObjState == nil || !r.ObjState.Exists {
		return fmt.Errorf("%w: %s", ErrNoSuchKey, r.Object.Name)
	}
	err := modifyObjAttr(ctx, r, tags.Attr, encodeAt(o.Set, r.Env.Zone.Release()))
	if errors.Is(err, ErrConcurrentModification) {
		return ErrTagConflict
	}
	return err
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *PutObjectTagging) Complete(context.Context, *Request) {}

// DeleteObjectTagging is RGWDeleteObjTags (rgw_op.cc:1117-1139 at v19.2.6,
// :1335-1376 at v20.2.4).
type DeleteObjectTagging struct {
	// Versioned is that the request names a version instance, which selects
	// the action. Init sets it from the request.
	Versioned bool
}

var _ Op = (*DeleteObjectTagging)(nil)

// Name is RGWDeleteObjTags::name.
func (o *DeleteObjectTagging) Name() string { return "delete_obj_tags" }

// Action is the action RGWDeleteObjTags::verify_permission authorizes.
func (o *DeleteObjectTagging) Action() policy.Action {
	if o.Versioned {
		return policy.S3DeleteObjectVersionTagging
	}
	return policy.S3DeleteObjectTagging
}

// OpMask is RGW_OP_TYPE_DELETE.
func (o *DeleteObjectTagging) OpMask() uint32 { return OpTypeDelete }

// Init loads the bucket and the object's head, which read_permissions reads
// for a DELETE of ?tagging (rgw_rest.cc:1920-1923 at v19.2.6).
func (o *DeleteObjectTagging) Init(ctx context.Context, r *Request) error {
	o.Versioned = r.Object.Instance != ""
	return statHead(ctx, r)
}

// VerifyPermission is RGWDeleteObjTags::verify_permission.
func (o *DeleteObjectTagging) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	return VerifyObjectPermission(ctx, r, a, acl.PermFor(a))
}

// Execute is RGWDeleteObjTags::execute as v20.2.4 runs it, on both releases:
// the object's attrs loaded, then delete_obj_attrs, so that the store
// advances the mtime it read (rgw_op.cc:1351-1376 at v20.2.4). v19.2.6 skips
// the load and stamps the object with the epoch plus a nanosecond
// (docs/ceph-upstream-bugs.md, "Squid's DeleteObjectTagging stamps the
// object with the epoch plus one nanosecond"; docs/exclusions.md).
func (o *DeleteObjectTagging) Execute(ctx context.Context, r *Request) error {
	if err := versioningUnserved(r.BucketRec, r.Object); err != nil {
		return err
	}
	if r.ObjState == nil || !r.ObjState.Exists {
		return fmt.Errorf("%w: %s", ErrNoSuchKey, r.Object.Name)
	}
	return r.Env.Objects.SetObjectAttrs(ctx, r.ObjState, nil, []string{tags.Attr})
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *DeleteObjectTagging) Complete(context.Context, *Request) {}

// modifyObjAttr is RadosObject::modify_obj_attrs: the object's attrs as Init
// read them, with name set to value, written back whole.
func modifyObjAttr(ctx context.Context, r *Request, name string, value []byte) error {
	attrs := maps.Clone(r.ObjState.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[name] = value
	return r.Env.Objects.SetObjectAttrs(ctx, r.ObjState, attrs, nil)
}
