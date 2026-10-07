package op

import (
	"context"
	"fmt"
	"io"
	"maps"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// AttrBucketEncryption is RGW_ATTR_BUCKET_ENCRYPTION_POLICY (rgw_common.h:174
// at v19.2.6, :194 at v20.2.4), a bucket's default encryption.
const AttrBucketEncryption = meta.AttrPrefix + "sse-s3.policy"

// bucketEncryptionUnserved answers 501 NotImplemented for a write into a
// bucket with a default encryption, which radosgw applies to the write
// (get_encryption_defaults, rgw_rest_s3.cc:146-264 at v19.2.6, :151-269 at
// v20.2.4) and which rgw-go cannot apply until phase 2 encrypts. It runs
// once the requester is authorized, where radosgw's refusal for want of a
// key server comes, so a refused requester learns nothing of the bucket's
// configuration (docs/exclusions.md).
func bucketEncryptionUnserved(rec *BucketRecord) error {
	if _, ok := rec.Attrs[AttrBucketEncryption]; ok {
		return fmt.Errorf("%w: the bucket's default encryption is not served yet", ErrNotImplemented)
	}
	return nil
}

// AttrPublicAccess is RGW_ATTR_PUBLIC_ACCESS (rgw_common.h:157 at v19.2.6,
// :176 at v20.2.4), the bucket attr holding its PublicAccessBlockConfiguration.
const AttrPublicAccess = meta.AttrPrefix + "public-access"

// defaultMaxPutSize is rgw_max_put_size's default, 5 GiB (rgw.yaml.in:134 at
// v19.2.6), for a configuration that cannot be read.
const defaultMaxPutSize = 5 << 30

// PutObject is RGWPutObj for S3 (rgw_op.cc:3806-4574 at v19.2.6, :4015-4867
// at v20.2.4). The handler fills the inputs; Execute streams the body through
// ObjectStore.PutObject, which reads it to EOF and so surfaces the
// authenticator's verdict on it before the head is written.
type PutObject struct {
	// Body is r.Body, the authenticator's verifying reader.
	Body io.Reader
	// Size is r.ContentLength, -1 for a chunked body of unknown length.
	Size int64
	// Attrs are the handler's request attrs: the content type, the generic
	// headers and every x-amz-* header, each NUL-terminated.
	Attrs map[string][]byte
	// ACL is create_s3_policy's: the canned ACL, the grant headers or the
	// default.
	ACL acl.Policy
	// CannedACL is x-amz-acl, "" when absent.
	CannedACL string
	// Tags is x-amz-tagging, nil when absent.
	Tags *tags.Set
	// StorageClass is x-amz-storage-class, "" when absent.
	StorageClass string
	// ContentMD5 is the decoded Content-MD5, nil when absent.
	ContentMD5 []byte
	// IfMatch and IfNoneMatch are the headers' values, nil when absent: a
	// header present with an empty value is a condition, as radosgw takes it.
	IfMatch, IfNoneMatch *string
	// Params, when set, is the protocol's get_params, which needs the
	// bucket: Init calls it once the bucket is loaded, its placement checked
	// and a public canned ACL refused, where RGWPutObj::init_processing calls
	// get_params (rgw_op.cc:3903-3915 at v19.2.6, :4112-4124 at v20.2.4). It
	// fills the inputs it reads, the ACL among them.
	Params func(ctx context.Context, o *PutObject) error

	ETag  string
	Mtime time.Time
	// VersionID is "" until versioning is implemented.
	VersionID string
}

var _ Op = (*PutObject)(nil)

// Name is RGWPutObj::name.
func (o *PutObject) Name() string { return "put_obj" }

// Action is the action RGWPutObj::verify_permission authorizes.
func (o *PutObject) Action() policy.Action { return policy.S3PutObject }

// OpMask is RGW_OP_TYPE_WRITE.
func (o *PutObject) OpMask() uint32 { return OpTypeWrite }

// Init is what radosgw does before verify_op_mask: init_permissions loads the
// bucket, a missing one NoSuchBucket, and checks the destination placement
// (rgw_op.cc:539-541 and :576-583 at v19.2.6, :569-571 and :606-613 at
// v20.2.4), and init_processing refuses a public canned ACL under a block of
// public ACLs (:3903-3909 at v19.2.6, :4112-4118 at v20.2.4) and then runs
// get_params, Params here. rgw-go also refuses, once Params has built it, a
// public policy from the grant headers, which radosgw stores
// (docs/exclusions.md).
func (o *PutObject) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	if err := checkDestPlacement(r, o.StorageClass); err != nil {
		return err
	}
	block := blockPublicACLs(rec)
	if block && isPublicCannedACL(o.CannedACL) {
		return fmt.Errorf("%w: a public canned acl under a block of public acls", ErrAccessDenied)
	}
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	if block && o.ACL.IsPublic() {
		return fmt.Errorf("%w: a public acl under a block of public acls", ErrAccessDenied)
	}
	return nil
}

// VerifyPermission is RGWPutObj::verify_permission for a PUT without a copy
// source (rgw_op.cc:3982-3985 at v19.2.6, :4191-4194 at v20.2.4).
func (o *PutObject) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
}

// Execute is RGWPutObj_ObjStore::verify_params (rgw_rest.cc:1049-1059 at
// v19.2.6 and v20.2.4), which runs after verify_permission, then
// RGWPutObj::execute in its order (rgw_op.cc:4142-4574 at v19.2.6,
// :4351-4867 at v20.2.4): the key, the quota for a body of known length, and
// the write, whose attrs gain the policy and, when the request tags the
// object with any tag, the tag set (encode_obj_tags_attr, rgw_op.h:2247-2255
// at v19.2.6). The write tag is the request id.
func (o *PutObject) Execute(ctx context.Context, r *Request) error {
	if err := versioningUnserved(r.BucketRec, r.Object); err != nil {
		return err
	}
	if err := bucketEncryptionUnserved(r.BucketRec); err != nil {
		return err
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
	release := r.Env.Zone.Release()
	attrs := maps.Clone(o.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[meta.AttrACL] = encodeAt(o.ACL, release)
	if o.Tags != nil && o.Tags.Len() > 0 {
		attrs[tags.Attr] = encodeAt(*o.Tags, release)
	}
	res, err := r.Env.Objects.PutObject(ctx, r.BucketRec, r.Object, o.Body, PutParams{
		Attrs: attrs, Size: o.Size, StorageClass: o.StorageClass,
		IfMatch: storeCondition(o.IfMatch), IfNoneMatch: storeCondition(o.IfNoneMatch), Tag: r.ID, ContentMD5: o.ContentMD5,
	})
	if err != nil {
		return err
	}
	o.ETag, o.Mtime, o.VersionID = res.ETag, res.Mtime, res.Version
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *PutObject) Complete(context.Context, *Request) {}

// checkDestPlacement is rgw_build_bucket_policies' check of s->dest_placement
// (rgw_op.cc:576-583 at v19.2.6, :606-613 at v20.2.4): the request's storage
// class on the bucket's placement rule must be one the zone has.
func checkDestPlacement(r *Request, storageClass string) error {
	rule := meta.PlacementRule{StorageClass: storageClass}.InheritFrom(r.BucketRec.Info.PlacementRule)
	if _, err := r.Env.Zone.Placement(rule); err != nil {
		return fmt.Errorf("%w: invalid dest placement %s", ErrInvalidArgument, rule)
	}
	return nil
}

// blockPublicACLs reports whether rec's public-access block sets
// BlockPublicAcls. A block that does not decode blocks: radosgw takes it for
// none (get_public_access_conf_from_attr, rgw_op.cc:340-355 at v19.2.6,
// :370-385 at v20.2.4), which would let a public ACL through a block its
// owner set (docs/exclusions.md).
func blockPublicACLs(rec *BucketRecord) bool {
	b, ok := rec.Attrs[AttrPublicAccess]
	if !ok {
		return false
	}
	d := denc.NewDecoder(b)
	block := acl.DecodePublicAccessBlock(d)
	return d.Err() != nil || block.BlockPublicACLs
}

// versioningUnserved answers 501 NotImplemented for a bucket whose versioning
// is enabled or suspended or that has object lock, and for a request that
// names a version of any of keys. Every write rgw-go serves takes radosgw's
// unversioned path, which on such a bucket would overwrite or remove a
// version's head beside the bucket's olh, under an index entry for the plain
// key, without the retention and legal-hold checks radosgw makes for a
// versioned delete on a lock bucket (verify_object_lock, rgw_op.cc:5185-5221
// and :6845-6869 at v19.2.6, :5576-5610 and :7782-7806 at v20.2.4). Writes
// to such a bucket wait for versioning (docs/exclusions.md).
func versioningUnserved(rec *BucketRecord, keys ...meta.ObjKey) error {
	if f := rec.Info.Flags; f&(meta.BucketVersioned|meta.BucketVersionsSuspended|meta.BucketObjLockEnabled) != 0 {
		return fmt.Errorf("%w: writes to bucket %s, which is versioned or has object lock, are not implemented",
			ErrNotImplemented, rec.Info.Bucket.Name)
	}
	for _, k := range keys {
		if k.Instance != "" {
			return fmt.Errorf("%w: writes naming version %q of %s are not implemented", ErrNotImplemented, k.Instance, k.Name)
		}
	}
	return nil
}

// emptyCondition is how a condition header present with an empty value
// crosses the store seam, where "" means absent: a NUL, which no header can
// carry and which starts with no non-empty ETag, so the store's prefix compare fails it
// as radosgw's compare of the empty string with the ETag fails
// (check_preconditions, driver/rados/rgw_rados.cc:7296-7301 and :7318-7323 at
// v20.2.4).
const emptyCondition = "\x00"

// storeCondition is a condition header as the store seam takes it: "" when
// absent, emptyCondition when present and empty, and otherwise its value.
func storeCondition(v *string) string {
	switch {
	case v == nil:
		return ""
	case *v == "":
		return emptyCondition
	default:
		return *v
	}
}

// isPublicCannedACL is whether a canned ACL is one a block of public ACLs
// refuses.
func isPublicCannedACL(canned string) bool {
	switch canned {
	case "public-read", "public-read-write", "authenticated-read":
		return true
	}
	return false
}

// encodeAt is v's encoding at release r.
func encodeAt(v interface {
	Encode(e *denc.Encoder, r denc.Release)
}, r denc.Release,
) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}

// confSize reads a size option, def when it cannot be read.
func confSize(r *Request, name string, def uint64) uint64 {
	if r.Env.Conf == nil {
		return def
	}
	v, err := r.Env.Conf.Size(name)
	if err != nil {
		return def
	}
	return v
}
