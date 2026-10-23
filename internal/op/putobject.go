package op

import (
	"context"
	"fmt"
	"io"
	"maps"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// AttrBucketEncryption is RGW_ATTR_BUCKET_ENCRYPTION_POLICY
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L174 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L194 at v20.2.4),
// a bucket's default encryption.
const AttrBucketEncryption = meta.AttrPrefix + "sse-s3.policy"

// bucketEncryptionUnserved answers 501 NotImplemented for a write into a
// bucket with a default encryption, which radosgw applies to the write
// (get_encryption_defaults,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L146-L264 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L151-L269 at v20.2.4)
// and which rgw-go cannot apply until phase 2 encrypts. It runs once the
// requester is authorized, where radosgw's refusal for want of a key
// server comes, so a refused requester learns nothing of the bucket's
// configuration (docs/exclusions.md).
func bucketEncryptionUnserved(rec *BucketRecord) error {
	if _, ok := rec.Attrs[AttrBucketEncryption]; ok {
		return fmt.Errorf("%w: the bucket's default encryption is not served yet", ErrNotImplemented)
	}
	return nil
}

// AttrPublicAccess is RGW_ATTR_PUBLIC_ACCESS
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L157 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L176 at v20.2.4),
// the bucket attr holding its PublicAccessBlockConfiguration.
const AttrPublicAccess = meta.AttrPrefix + "public-access"

// defaultMaxPutSize is rgw_max_put_size's default, 5 GiB
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L134 at v19.2.6),
// for a configuration that cannot be read.
const defaultMaxPutSize = 5 << 30

// PutObject is RGWPutObj for S3
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3806-L4574 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4015-L4867 at v20.2.4).
// The handler fills the inputs; Execute streams the body through
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
	// Params, when set, is the part of the protocol's get_params that reads
	// the request alone: Init calls it once the bucket is loaded, where
	// RGWPutObj::init_processing calls get_params
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3911 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4120 at v20.2.4).
	// It fills the inputs it reads.
	Params func(ctx context.Context, o *PutObject) error
	// Authorized, when set, is the rest of get_params, the checks that read
	// stored state, the ACL's grantees among them: Execute calls it once the
	// requester is authorized, the placement checked and a public canned ACL
	// refused. It fills the inputs it reads, the ACL among them.
	Authorized func(ctx context.Context, o *PutObject) error
	// ChecksumField is the request's checksum fields, which ChecksumForPut
	// reads with the request's Trailers (ChecksumInput).
	ChecksumField func(name string) (string, bool)

	ETag  string
	Mtime time.Time
	// VersionID is "" until versioning is implemented.
	VersionID string
	// Checksum is the checksum the store computed, nil for none.
	Checksum *cksum.Cksum
}

var _ Op = (*PutObject)(nil)

// Name is RGWPutObj::name.
func (o *PutObject) Name() string { return "put_obj" }

// Action is the action RGWPutObj::verify_permission authorizes.
func (o *PutObject) Action() policy.Action { return policy.S3PutObject }

// OpMask is RGW_OP_TYPE_WRITE.
func (o *PutObject) OpMask() uint32 { return OpTypeWrite }

// Init is init_permissions' load of the bucket, a missing one NoSuchBucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L539-L541 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L569-L571 at v20.2.4),
// then Params. The checks of what the bucket holds that radosgw makes there
// and in init_processing wait for the requester to be authorized, so that a
// refused requester learns nothing of the bucket (Execute;
// docs/exclusions.md).
func (o *PutObject) Init(ctx context.Context, r *Request) error {
	if err := loadBucket(ctx, r); err != nil {
		return err
	}
	if o.Params != nil {
		return o.Params(ctx, o)
	}
	return nil
}

// VerifyPermission is RGWPutObj::verify_permission for a PUT without a copy
// source
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3982-L3985 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4191-L4194 at v20.2.4).
func (o *PutObject) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
}

// Execute first makes, in radosgw's order, the checks of stored state that
// radosgw makes before verify_permission: the request's storage class on the
// bucket's placement must be one the zone has
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L576-L583 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L606-L613 at v20.2.4);
// a public canned ACL under the bucket's block of public ACLs is refused
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3903-L3909,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4112-L4118);
// then Authorized runs, and a public policy it built from the grant headers
// is refused too, which radosgw stores (docs/exclusions.md). Then come
// RGWPutObj_ObjStore::verify_params
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1049-L1059 at v19.2.6
// and v20.2.4) and RGWPutObj::execute in its order
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4142-L4574 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4351-L4867 at v20.2.4):
// the key, the quota for a body of known length, the checksum request,
// before the body as RGWPutObj_Cksum::Factory reads it (ChecksumForPut;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611 at v20.2.4),
// and the write, whose attrs gain the policy and, when the request tags the
// object with any tag, the tag set (encode_obj_tags_attr,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2247-L2255 at v19.2.6).
// The write tag is the request id.
func (o *PutObject) Execute(ctx context.Context, r *Request) error {
	if err := checkDestPlacement(r, o.StorageClass); err != nil {
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
	if err := newHeadVersioningUnserved(r.BucketRec, r.Object); err != nil {
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
	ck, body, err := ChecksumForPut(ChecksumInput{Field: o.ChecksumField, Trailers: r.Trailers, Body: o.Body}, release)
	if err != nil {
		return err
	}
	res, err := r.Env.Objects.PutObject(ctx, r.BucketRec, r.Object, body, PutParams{
		Attrs: attrs, Size: o.Size, StorageClass: o.StorageClass,
		IfMatch: storeCondition(o.IfMatch), IfNoneMatch: storeCondition(o.IfNoneMatch), Tag: r.ID, ContentMD5: o.ContentMD5,
		Checksum: ck,
	})
	if err != nil {
		return err
	}
	o.ETag, o.Mtime, o.VersionID, o.Checksum = res.ETag, res.Mtime, res.Version, res.Checksum
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *PutObject) Complete(context.Context, *Request) {}

// checkDestPlacement is rgw_build_bucket_policies' check of s->dest_placement
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L576-L583 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L606-L613 at v20.2.4):
// the request's storage class on the bucket's placement rule must be one the
// zone has.
func checkDestPlacement(r *Request, storageClass string) error {
	rule := meta.PlacementRule{StorageClass: storageClass}.InheritFrom(r.BucketRec.Info.PlacementRule)
	if _, err := r.Env.Zone.Placement(rule); err != nil {
		return fmt.Errorf("%w: invalid dest placement %s", ErrInvalidArgument, rule)
	}
	return nil
}

// blockPublicACLs reports whether rec's public-access block sets
// BlockPublicAcls. A block that does not decode blocks: radosgw takes it for
// none (get_public_access_conf_from_attr,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L340-L355 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L370-L385 at v20.2.4),
// which would let a public ACL through a block its owner set
// (docs/exclusions.md).
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
// is or was enabled or that has object lock, and for a request that names a
// version of any of keys, which are objects of rec's that the request removes
// or changes in place. Every write rgw-go serves but the deletes takes
// radosgw's unversioned path, which on such a bucket would overwrite a
// version's head beside the bucket's olh, under an index entry for the
// plain key. The store's delete takes radosgw's versioned path on a bucket
// whose flags hold BUCKET_VERSIONED, but the delete ops do not yet answer
// the version id or the delete marker it reports, and would skip the
// retention and legal-hold checks radosgw makes for a versioned delete on a
// lock bucket (verify_object_lock,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5185-L5221 and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6845-L6869 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5576-L5610 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7782-L7806 at v20.2.4).
// Writes to such a bucket wait for versioning (docs/exclusions.md).
//
// On a bucket whose versioning was never enabled, a key naming the null
// version names the plain object: its head is the plain key's
// (rgw_obj_key::get_oid,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_obj_types.h#L226-L254 at v19.2.6
// and v20.2.4), the delete and the attr writes clear the instance before
// they reach the index (RGWRados::Object::Delete::delete_obj,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5768-L5770 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6456-L6458 at v20.2.4;
// RGWRados::set_attrs,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6601-L6603
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7401-L7403),
// and the delete takes the unversioned path whenever the bucket's flags lack
// BUCKET_VERSIONED
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5774,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6462),
// which RGWDeleteMultiObj passes for each key
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6889 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7826 at v20.2.4).
func versioningUnserved(rec *BucketRecord, keys ...meta.ObjKey) error {
	if !neverVersioned(rec) {
		return fmt.Errorf("%w: writes to bucket %s, which is or was versioned or has object lock, are not implemented",
			ErrNotImplemented, rec.Info.Bucket.Name)
	}
	for _, k := range keys {
		if k.Instance != "" && k.Instance != meta.NullInstance {
			return fmt.Errorf("%w: writes naming version %q of %s are not implemented", ErrNotImplemented, k.Instance, k.Name)
		}
	}
	return nil
}

// newHeadVersioningUnserved is versioningUnserved for a write that creates a
// head, whose keys may not name even the null version. radosgw writes the
// head of a PutObject, CopyObject or CompleteMultipartUpload naming it at
// the plain key's oid but files it in the index under the instance
// (cls_obj_prepare_op and UpdateIndex::complete,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9473
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7133 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10405
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7984 at v20.2.4),
// which cls_rgw keeps apart from the plain key's entry
// (encode_obj_index_key,
// https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L339-L346 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L389-L396 at v20.2.4),
// then links the key to an OLH as a versioned object
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3375 at v19.2.7);
// and it copies from a source naming it without the checks of a copy onto
// itself
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3541-L3548 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3821-L3828 at v20.2.4;
// copy_obj,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4890
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5150).
func newHeadVersioningUnserved(rec *BucketRecord, keys ...meta.ObjKey) error {
	if err := versioningUnserved(rec); err != nil {
		return err
	}
	for _, k := range keys {
		if k.Instance != "" {
			return fmt.Errorf("%w: writes naming version %q of %s are not implemented", ErrNotImplemented, k.Instance, k.Name)
		}
	}
	return nil
}

// neverVersioned reports whether rec's versioning was never enabled.
// Enabling versioning sets BUCKET_VERSIONED and suspending it keeps it set
// (RGWSetBucketVersioning::execute,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2837-L2843 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3069-L3075 at v20.2.4);
// object lock is enabled only with versioning
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2392
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8232 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2497
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9180 at v20.2.4).
// The suspended flag without it, which radosgw never writes, leaves the
// state unknown, and an unknown state is taken for a versioned one.
func neverVersioned(rec *BucketRecord) bool {
	return rec.Info.Flags&(meta.BucketVersioned|meta.BucketVersionsSuspended|meta.BucketObjLockEnabled) == 0
}

// EmptyCondition is how a condition header present with an empty value
// crosses the store seam, where "" means absent: a NUL, which no header can
// carry and which starts with no non-empty ETag, so the store's prefix compare fails it
// as radosgw's compare of the empty string with the ETag fails
// (check_preconditions,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7296-L7301
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7318-L7323 at v20.2.4).
// A store that compares an ETag whole reads it back as that empty string.
const EmptyCondition = "\x00"

// storeCondition is a condition header as the store seam takes it: "" when
// absent, EmptyCondition when present and empty, and otherwise its value.
func storeCondition(v *string) string {
	switch {
	case v == nil:
		return ""
	case *v == "":
		return EmptyCondition
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
