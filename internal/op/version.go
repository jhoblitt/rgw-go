package op

import "github.com/jhoblitt/rgw-go/internal/meta"

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
// not enabled (docs/differences.md, "A write's versionId is ignored").
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
// was ever set, Suspended alone included, and none on one whose versioning
// never was, where an object has no version. radosgw sends only a non-empty
// instance, so no id for the null version (docs/differences.md, "Writes name
// the null version's id and a copy's two versions").
func ResponseVersionID(rec *BucketRecord, instance string) string {
	switch {
	case instance != "" && instance != meta.NullInstance:
		return instance
	case rec.Info.Versioned():
		return meta.NullInstance
	}
	return ""
}
