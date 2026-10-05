package op

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// statHead loads the bucket and the object's head without its data, as
// rgw_build_bucket_policies and rgw_build_object_policies do before
// verify_permission for an op that prefetches nothing (rgw_op.cc:494 and
// :631-649 at v19.2.6, :524 and :661-679 at v20.2.4). A missing bucket is
// NoSuchBucket before any object is read.
func statHead(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	st, err := r.Env.Objects.StatObject(ctx, rec, r.Object)
	if err != nil {
		return err
	}
	r.ObjState = st
	return nil
}

// GetObjectTagging is RGWGetObjTags (rgw_op.cc:1037-1074 at v19.2.6,
// :1236-1273 at v20.2.4): the object's tag set from the head Init read.
type GetObjectTagging struct {
	// Versioned is that the request names a version instance, which selects
	// the action. Init sets it from the request.
	Versioned bool

	// HasTags is that the object carries a tag set, and Tags is that set.
	HasTags bool
	Tags    tags.Set
}

var _ Op = (*GetObjectTagging)(nil)

// Name is RGWGetObjTags::name.
func (o *GetObjectTagging) Name() string { return "get_obj_tags" }

// Action is the action RGWGetObjTags::verify_permission authorizes.
func (o *GetObjectTagging) Action() policy.Action {
	if o.Versioned {
		return policy.S3GetObjectVersionTagging
	}
	return policy.S3GetObjectTagging
}

// OpMask is RGW_OP_TYPE_READ.
func (o *GetObjectTagging) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket and the object's head.
func (o *GetObjectTagging) Init(ctx context.Context, r *Request) error {
	o.Versioned = r.Object.Instance != ""
	return statHead(ctx, r)
}

// VerifyPermission is RGWGetObjTags::verify_permission.
func (o *GetObjectTagging) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	return VerifyObjectPermission(ctx, r, a, acl.PermFor(a))
}

// Execute is RGWGetObjTags::execute, whose get_obj_attrs answers a missing
// object with -ENOENT, and the decode RGWGetObjTags_ObjStore_S3's
// send_response_data makes (rgw_rest_s3.cc:746-773 at v19.2.6, :829-856 at
// v20.2.4), where a tag set that decodes neither as RGWObjTags nor as the text
// older objects store is -EIO, which rgw-go answers as UnknownError
// (docs/exclusions.md).
func (o *GetObjectTagging) Execute(_ context.Context, r *Request) error {
	st := r.ObjState
	if st == nil || !st.Exists {
		return fmt.Errorf("%w: %s", ErrNoSuchKey, r.Object.Name)
	}
	b, ok := st.Attrs[tags.Attr]
	if !ok {
		return nil
	}
	d := denc.NewDecoder(b)
	set := tags.Decode(d)
	if err := d.Err(); err != nil {
		return fmt.Errorf("%w: decoding the tags of %s: %w", ErrUnknown, r.Object.Name, err)
	}
	o.HasTags, o.Tags = true, set
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *GetObjectTagging) Complete(context.Context, *Request) {}

// GetObjectACL is RGWGetACLs at object scope (rgw_op.cc:5695-5734 at v19.2.6,
// :6275-6314 at v20.2.4). It renders s->object_acl, which
// rgw_build_object_policies read with get_obj_policy_from_attr (:288-325 at
// v19.2.6, :331-368 at v20.2.4): the head's user.rgw.acl or, when the head has
// none, a default policy for s->bucket_owner, the owner the bucket's ACL names
// (:552 at v19.2.6, :582 at v20.2.4).
type GetObjectACL struct {
	// Versioned is that the request names a version instance, which selects
	// the action. Init sets it from the request.
	Versioned bool

	// Policy is the object's ACL.
	Policy acl.Policy
}

var _ Op = (*GetObjectACL)(nil)

// Name is RGWGetACLs::name, which the bucket ACL shares.
func (o *GetObjectACL) Name() string { return "get_acls" }

// Action is the action RGWGetACLs::verify_permission authorizes for an
// object.
func (o *GetObjectACL) Action() policy.Action {
	if o.Versioned {
		return policy.S3GetObjectVersionAcl
	}
	return policy.S3GetObjectAcl
}

// OpMask is RGW_OP_TYPE_READ.
func (o *GetObjectACL) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket and the object's head.
func (o *GetObjectACL) Init(ctx context.Context, r *Request) error {
	o.Versioned = r.Object.Instance != ""
	return statHead(ctx, r)
}

// VerifyPermission is RGWGetACLs::verify_permission for an object.
func (o *GetObjectACL) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	return VerifyObjectPermission(ctx, r, a, acl.PermFor(a))
}

// Execute takes the policy get_obj_policy_from_attr builds (ObjectACLFor).
func (o *GetObjectACL) Execute(ctx context.Context, r *Request) error {
	p, err := ObjectACLFor(ctx, r.ObjState, r.BucketRec)
	if err != nil {
		return err
	}
	o.Policy = p
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *GetObjectACL) Complete(context.Context, *Request) {}

// BucketACLFor is rgw_op_get_bucket_policy_from_attr (rgw_op.cc:267-286 at
// v19.2.6, :310-329 at v20.2.4): the bucket's ACL as
// rgw_build_bucket_policies reads it into s->bucket_acl for every request
// that names a bucket. That is the bucket's user.rgw.acl, or, for a bucket
// without one, a policy giving the bucket's owner FULL_CONTROL under an empty
// display name. An ACL that does not decode is decode_policy's -EIO, which
// fails the request, and BucketACLFor returns it as an UnknownError.
//
// The policy's Owner is radosgw's s->bucket_owner, which names the owner of
// any default policy radosgw builds for an object without an ACL of its own.
// The bucket-scope ACL ops render the policy itself.
func BucketACLFor(rec *BucketRecord) (acl.Policy, error) {
	b, ok := rec.Attrs[meta.AttrACL]
	if !ok {
		slog.Warn("couldn't find acl header for bucket, generating default", slog.String("bucket", rec.Info.Bucket.Name))
		return acl.DefaultPolicy(rec.Info.Owner, ""), nil
	}
	return decodeACL(b, "bucket "+rec.Info.Bucket.Name)
}

// ObjectACLFor is get_obj_policy_from_attr (rgw_op.cc:288-326 at v19.2.6,
// :331-369 at v20.2.4): the ACL of the object st, in bucket, as
// rgw_build_object_policies reads it into s->object_acl. That is the head's
// user.rgw.acl or, for a head without one, a policy giving FULL_CONTROL to
// the owner bucket's ACL names, under its display name (s->bucket_owner,
// :552 at v19.2.6, :582 at v20.2.4); only then is the bucket's ACL read. An
// ACL that does not decode is decode_policy's -EIO, an UnknownError, and a
// missing object is the head read's -ENOENT, NoSuchKey. The warning about a
// head without an ACL is logged once per state, so once per request.
func ObjectACLFor(ctx context.Context, st *ObjectState, bucket *BucketRecord) (acl.Policy, error) {
	if st == nil || !st.Exists {
		var key string
		if st != nil {
			key = st.Key.Name
		}
		return acl.Policy{}, fmt.Errorf("%w: %s", ErrNoSuchKey, key)
	}
	if b, ok := st.Attrs[meta.AttrACL]; ok {
		return decodeACL(b, "object "+st.Key.Name)
	}
	if !st.defaultACLLogged {
		st.defaultACLLogged = true
		slog.WarnContext(ctx, "couldn't find acl header for object, generating default",
			slog.String("bucket", bucket.Info.Bucket.Name), slog.String("key", st.Key.Name))
	}
	bucketACL, err := BucketACLFor(bucket)
	if err != nil {
		return acl.Policy{}, err
	}
	return acl.DefaultPolicy(meta.ParseOwner(bucketACL.Owner.ID), bucketACL.Owner.DisplayName), nil
}

// decodeACL is decode_policy (rgw_op.cc:227-245 at v19.2.6, :270-288 at
// v20.2.4), which reads an RGWAccessControlPolicy and leaves any bytes after
// it unread.
func decodeACL(b []byte, of string) (acl.Policy, error) {
	d := denc.NewDecoder(b)
	p := acl.DecodePolicy(d)
	if err := d.Err(); err != nil {
		return acl.Policy{}, fmt.Errorf("%w: decoding the acl of %s: %w", ErrUnknown, of, err)
	}
	return p, nil
}

// ObjectAttrs is RGWGetObjAttrs' requested attributes, a ReqAttributes flag
// each, as as_flag numbers them ([T] rgw_op.h:1755-1766).
type ObjectAttrs uint16

// The attributes x-amz-object-attributes names.
const (
	AttrETag ObjectAttrs = 1 << iota
	AttrChecksum
	AttrObjectParts
	AttrStorageClass
	AttrObjectSize
)

// objectAttrNames are the names recognize_attrs compares, in its order.
var objectAttrNames = []struct {
	name string
	attr ObjectAttrs
}{
	{"etag", AttrETag},
	{"checksum", AttrChecksum},
	{"objectparts", AttrObjectParts},
	{"objectsize", AttrObjectSize},
	{"storageclass", AttrStorageClass},
}

// ParseObjectAttrs is RGWGetObjAttrs::recognize_attrs ([T] rgw_op.cc:6337-6359)
// over x-amz-object-attributes: the header split at its commas, each name
// compared whole, untrimmed, with ASCII case folded as boost::iequals folds it
// in radosgw's C locale. A name it does not know is ignored.
func ParseObjectAttrs(header string) ObjectAttrs {
	var attrs ObjectAttrs
	for name := range strings.SplitSeq(header, ",") {
		for _, a := range objectAttrNames {
			if len(name) == len(a.name) && strncaseEqual(name, a.name, len(name)) {
				attrs |= a.attr
			}
		}
	}
	return attrs
}

// ObjectPart is one Part of GetObjectAttributes' ObjectParts
// ([T] rgw_rest_s3.cc:4087-4096). Its checksum is not implemented yet.
type ObjectPart struct {
	Number int
	Size   uint64
}

// MaxObjectParts is the max-parts list_parts takes without x-amz-max-parts,
// and the most the header may ask for ([T] rgw_rest_s3.cc:3961, :4084).
const MaxObjectParts = 1000

// GetObjectAttributes is RGWGetObjAttrs ([T] rgw_op.cc:6337-6403,
// rgw_rest_s3.cc:3941-4137), which rgw-go serves on both releases although a
// Squid radosgw answers ?attributes as GetObject. It is a GetObject that reads
// no data: Init, the check order, the SSE checks and the cloud-tier rule are
// GetObject's. The handler sets Versioned, the SSE inputs and Secure; Init
// clears every other GetObject input.
type GetObjectAttributes struct {
	GetObject
	// Requested is x-amz-object-attributes.
	Requested ObjectAttrs
	// MaxParts is x-amz-max-parts, capped at MaxObjectParts by the handler;
	// nil when absent.
	MaxParts *int
	// PartMarker is x-amz-part-number-marker; nil when absent.
	PartMarker *int

	// Parts are the parts listed for ObjectParts. PartsTruncated is that
	// more parts follow them, and NextPartMarker is the marker that lists
	// those.
	Parts          []ObjectPart
	PartsTruncated bool
	NextPartMarker int
}

var _ Op = (*GetObjectAttributes)(nil)

// Name is RGWGetObjAttrs::name.
func (o *GetObjectAttributes) Name() string { return "get_obj_attrs" }

// Action is the first action RGWGetObjAttrs::verify_permission authorizes.
func (o *GetObjectAttributes) Action() policy.Action {
	if o.Versioned {
		return policy.S3GetObjectVersion
	}
	return policy.S3GetObject
}

// Init is GetObject's Init for a request with no data, so no prefetch, and
// none of the inputs RGWGetObjAttrs_ObjStore_S3::get_params, which replaces
// GetObject's, never reads ([T] rgw_rest_s3.cc:3941-3983): no Range, no
// conditionals, no partNumber, no torrent and no response-* overrides.
func (o *GetObjectAttributes) Init(ctx context.Context, r *Request) error {
	g := &o.GetObject
	g.GetData, g.Torrent, g.PartNumber, g.ResponseOverrides = false, false, nil, nil
	g.Range, g.IfMatch, g.IfNoneMatch, g.IfModifiedSince, g.IfUnmodifiedSince = "", "", "", "", ""
	return g.Init(ctx, r)
}

// VerifyPermission is RGWGetObjAttrs::verify_permission
// ([T] rgw_op.cc:6361-6393): s3:GetObject, or failing it
// s3:GetObjectAttributes, each for the version when one is named. A refusal
// radosgw makes before verify_permission, such as a missing object's, and an
// error that is no access denial come from the first check alone. A Squid
// radosgw has no such op, so there the first check decides: Squid knows no
// s3:GetObjectAttributes, and no Deny a Squid policy states could refuse it.
func (o *GetObjectAttributes) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	err := VerifyObjectPermission(ctx, r, a, acl.PermFor(a))
	if err == nil || IsBeforeVerify(err) || !errors.Is(err, ErrAccessDenied) || r.Env.Zone.Release() < denc.Tentacle {
		return err
	}
	a = policy.S3GetObjectAttributes
	if o.Versioned {
		a = policy.S3GetObjectVersionAttributes
	}
	return VerifyObjectPermission(ctx, r, a, acl.PermFor(a))
}

// Execute is RGWGetObjAttrs::execute, which is RGWGetObj::execute without
// data, then the part listing RGWGetObjAttrs_ObjStore_S3::send_response makes
// for ObjectParts ([T] rgw_rest_s3.cc:4059-4119). The parts count is
// Tentacle's Read::prepare count of a multipart manifest, which rgw-go's
// GetObject reports only on Tentacle, so it is taken here on Squid.
func (o *GetObjectAttributes) Execute(ctx context.Context, r *Request) error {
	o.Sink = discardSink{}
	if err := o.GetObject.Execute(ctx, r); err != nil {
		return err
	}
	if o.PartsCount == nil && o.State.Manifest != nil {
		n, err := o.State.Manifest.PartsCount()
		if err != nil {
			return manifestError(r, err)
		}
		if n > 0 {
			o.PartsCount = &n
		}
	}
	if o.Requested&AttrObjectParts == 0 || o.PartsCount == nil {
		return nil
	}
	return o.listParts(ctx, r)
}

// listParts is RadosObject::list_parts ([T]
// driver/rados/rgw_sal_rados.cc:2834-2933). From the part after the marker,
// which must exist, it lists up to max-parts parts in manifest order, each
// with a stat of its head through get_part_obj_state ([T]
// driver/rados/rgw_rados.cc:7595-7684), and truncates where a part is left
// over. The next marker counts the parts listed on from the marker. A part
// head that cannot be read ends the list without an error, as list_parts'
// failure only reaches radosgw's log. One walk of the manifest serves every
// part, where get_part_obj_state walks it again from the start for each.
func (o *GetObjectAttributes) listParts(ctx context.Context, r *Request) error {
	limit, marker := MaxObjectParts, 0
	if o.MaxParts != nil {
		limit = *o.MaxParts
	}
	if o.PartMarker != nil {
		marker = *o.PartMarker
	}
	// With nothing listed, the next page starts at the marker itself.
	o.NextPartMarker = marker
	if marker > *o.PartsCount-1 {
		return nil
	}
	w, err := o.State.Manifest.WalkParts()
	if err != nil {
		return manifestError(r, err)
	}
	if marker != 0 {
		found, err := w.Find(marker + 1)
		if err != nil {
			return manifestError(r, err)
		}
		if !found {
			return nil
		}
	}
	for !w.Done() {
		if limit < 1 {
			o.PartsTruncated = true
			return nil
		}
		n, _, head := w.Part()
		st, err := r.Env.Objects.StatObject(ctx, partBucket(r.BucketRec, head.Bucket), head.Key)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return fmt.Errorf("listing the parts of %s: %w", r.Object.Name, cerr)
			}
			slog.ErrorContext(ctx, "listing the parts of an object stopped at a part head that could not be read",
				slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name), slog.Int("part", n), slog.Any("error", err))
			return nil
		}
		extent, err := w.Skip()
		if err != nil {
			return manifestError(r, err)
		}
		o.Parts = append(o.Parts, ObjectPart{Number: n, Size: accountedSize(st, extent)})
		marker++
		o.NextPartMarker = marker
		limit--
	}
	return nil
}

// accountedSize is the accounted_size get_part_obj_state leaves in a part
// head's state: the decompressed size its compression info records, whatever
// the compression type; else the size of its own manifest; else, for a part
// head without a manifest, a missing one included, the part's extent in the
// object's manifest.
func accountedSize(st *ObjectState, extent uint64) uint64 {
	switch {
	case st == nil:
		return extent
	case st.Compression != nil:
		return st.Compression.OrigSize
	case st.Manifest != nil:
		return st.Size
	default:
		return extent
	}
}

// discardSink is the sink of an op whose handler renders the body: the
// status GetObject sends goes nowhere.
type discardSink struct{}

func (discardSink) WriteHeader(int, http.Header) {}
func (discardSink) Write(p []byte) (int, error)  { return len(p), nil }
func (discardSink) Flush() error                 { return nil }
