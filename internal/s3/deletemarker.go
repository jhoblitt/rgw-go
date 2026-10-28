package s3

import (
	"context"
	"errors"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// setMissingDeleteMarker is the x-amz-delete-marker Tentacle's
// RGWHandler_REST::read_permissions adds to a GET or HEAD whose object
// policies were refused with -ENOENT
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1936-L1944 at v20.2.4;
// v19.2.7 adds none,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1898-L1938):
// it loads the key's state following the olh and names whether the current
// version is a delete marker. That refusal is the NoSuchKey the authorizer
// marks BeforeVerify; a missing bucket is -ERR_NO_SUCH_BUCKET, and a
// NoSuchKey execute finds comes after read_permissions.
//
// rgw-go sends true for a key named without a version whose current
// version is a delete marker on Squid too (docs/exclusions.md, "A key whose
// current version is a delete marker answers its 404 with
// x-amz-delete-marker: true on Squid too"), and true with the 405 a read
// naming a delete marker's own version answers, with the marker's
// Last-Modified when the op's answer carries it (op.MarkerVersionError;
// docs/exclusions.md, "A read naming a delete marker's version answers 405
// MethodNotAllowed"). Tentacle's false stays for every other missing object,
// a version named by a requester who may not read it among them, delete
// marker or not: radosgw follows no olh for a key that names an instance.
func setMissingDeleteMarker(ctx context.Context, h http.Header, r *op.Request, err error) {
	if r.Env == nil || r.Env.Zone == nil || r.Method != http.MethodGet && r.Method != http.MethodHead ||
		r.Object.Name == "" || r.BucketRec == nil || !op.IsBeforeVerify(err) {
		return
	}
	if me, ok := errors.AsType[*op.MarkerVersionError](err); ok {
		SetHeader(h, "x-amz-delete-marker", "true")
		if me.LastModified != nil {
			SetHeader(h, "Last-Modified", httpDate(*me.LastModified))
		}
		return
	}
	if !errors.Is(err, op.ErrNoSuchKey) {
		return
	}
	switch {
	case currentDeleteMarker(ctx, r):
		SetHeader(h, "x-amz-delete-marker", "true")
	case r.Env.Zone.Release() >= denc.Tentacle:
		SetHeader(h, "x-amz-delete-marker", "false")
	}
}

// currentDeleteMarker reports whether r names its key without a version and
// that key's current version is a delete marker, which the object store
// marks DeleteMarker. A state r already read for the key spares the stat;
// otherwise, for ListParts of a missing upload, whose state is the meta
// object's, the key is stated. A stat that fails is no marker, as radosgw
// ignores the load's failures.
func currentDeleteMarker(ctx context.Context, r *op.Request) bool {
	if r.Object.Instance != "" {
		return false
	}
	st := r.ObjState
	if st == nil || st.Key != r.Object {
		var err error
		if st, err = r.Env.Objects.StatObject(ctx, r.BucketRec, r.Object); err != nil || st == nil {
			return false
		}
	}
	return st.DeleteMarker
}
