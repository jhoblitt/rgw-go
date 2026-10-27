package op

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// VersionForWrite is the instance a write that creates an object's head
// gives it: PutObject, CopyObject's destination, CompleteMultipartUpload and
// POST object. On a bucket whose versioning is enabled it is a new one, as
// RGWPutObj, RGWCopyObj and RGWCompleteMultipart draw it
// (gen_rand_obj_instance_name;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4278-L4285,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5576-L5580 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6462-L6469 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4491-L4498,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6139-L6143 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7284-L7291 at v20.2.4).
// Otherwise it is empty: the plain object on a bucket whose versioning was
// never enabled, the null version on a suspended one. The request's
// versionId plays no part, as S3 has no such parameter on these writes,
// where radosgw keeps it as the instance on a bucket whose versioning is
// not enabled (docs/exclusions.md, "A write's versionId is ignored").
func VersionForWrite(r *Request) string {
	if r.BucketRec.Info.VersioningEnabled() {
		return meta.NewInstance()
	}
	return ""
}

// writeKey is r's object as a write that creates its head writes it, at
// VersionForWrite's instance.
func writeKey(r *Request) meta.ObjKey {
	k := r.Object
	k.Instance = VersionForWrite(r)
	return k
}

// ResponseVersionID is the version id a response names for instance, a
// version of an object in rec: the instance itself, and for the null version,
// an empty instance or "null", S3's id "null" on a bucket whose versioning
// was ever enabled and none on one whose versioning never was, where an
// object has no version. radosgw sends only a non-empty instance, so no id
// for the null version (docs/exclusions.md, "Writes name the null version's
// id and a copy's two versions").
func ResponseVersionID(rec *BucketRecord, instance string) string {
	switch {
	case instance != "" && instance != meta.NullInstance:
		return instance
	case rec.Info.Versioned():
		return meta.NullInstance
	}
	return ""
}

// objectLockUnserved answers 501 NotImplemented for a write that creates a
// head in a bucket with object lock: radosgw's PutObject and CopyObject
// store the retention and legal hold their object-lock headers name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4542-L4551 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5587-L5596 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4810-L4819 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6150-L6159 at v20.2.4),
// which rgw-go's ops do not yet. CompleteMultipartUpload's head already
// takes its upload's lock and the bucket's default retention, and the
// upload's writes refuse lock headers; it is refused too only so that the
// three head writes are served on such a bucket together, once the ops
// store those headers (docs/exclusions.md, "PutObject, CopyObject and
// CompleteMultipartUpload answer 501 on an object-lock bucket, and the
// deletes and the ACL and tag writes on a versioned one").
func objectLockUnserved(rec *BucketRecord) error {
	if rec.Info.ObjLockEnabled() {
		return fmt.Errorf("%w: writes to bucket %s, which has object lock, are not implemented", ErrNotImplemented, rec.Info.Bucket.Name)
	}
	return nil
}
