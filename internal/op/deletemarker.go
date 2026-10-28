package op

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// MarkerVersionError is a read's 405 MethodNotAllowed for a delete marker's
// own version, which the protocol answers with x-amz-delete-marker: true and,
// for GET and HEAD, the marker's Last-Modified, as Amazon S3 answers it
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/DeleteMarker.html).
// It unwraps to ErrMethodNotAllowed.
type MarkerVersionError struct {
	Key meta.ObjKey
	// LastModified is the marker's mtime when the answer carries it, nil
	// when it does not.
	LastModified *time.Time
}

func (e *MarkerVersionError) Error() string {
	return fmt.Sprintf("%s: version %s of %s is a delete marker", ErrMethodNotAllowed.Code, e.Key.Instance, e.Key.Name)
}

func (e *MarkerVersionError) Unwrap() error { return ErrMethodNotAllowed }

// markerVersion reports whether st, the state a read of key resolved, is a
// delete marker's own version: a version key names, which the object store
// marks DeleteMarker only for a marker that is listed.
func markerVersion(st *ObjectState, key meta.ObjKey) bool {
	return st != nil && !st.Exists && st.DeleteMarker && key.Instance != ""
}

// mayReadMarker reports whether r's requester holds one of actions on key in
// bucket, a delete marker's version (VerifyMarkerIn). A refusal of any kind,
// a policy's or a fault's, is no permission.
func mayReadMarker(ctx context.Context, r *Request, bucket *BucketRecord, key meta.ObjKey, actions ...policy.Action) bool {
	for _, a := range actions {
		if VerifyMarkerPermissionIn(ctx, r, a, acl.PermFor(a), bucket, key) == nil {
			return true
		}
	}
	return false
}

// deleteMarkerVersion is err, a read's refusal of r's object made before
// verify_permission, as read_obj_policy makes it for a missing object, with a
// delete marker's own version answered 405 MethodNotAllowed (MarkerVersionError)
// to a requester who holds one of actions, the read's own version actions,
// on it: Amazon S3 checks the read's permission and then answers the marker,
// where radosgw finds no head for the marker and answers it as a missing
// version, 404 to an admin or to a requester allowed s3:ListBucket and 403 to
// any other (docs/ceph-upstream-bugs.md, "radosgw answers a read naming a
// delete marker's version 404 NoSuchKey"; docs/exclusions.md, "A read naming
// a delete marker's version answers 405 MethodNotAllowed"). A requester who
// holds none keeps radosgw's answer, and learns no more of a marker than of
// a missing version. With lastModified the answer carries the marker's
// mtime.
func deleteMarkerVersion(ctx context.Context, r *Request, err error, lastModified bool, actions ...policy.Action) error {
	if !markerVersion(r.ObjState, r.Object) || !IsBeforeVerify(err) ||
		!errors.Is(err, ErrNoSuchKey) && !errors.Is(err, ErrAccessDenied) {
		return err
	}
	if !mayReadMarker(ctx, r, r.BucketRec, r.Object, actions...) {
		return err
	}
	me := &MarkerVersionError{Key: r.Object}
	if lastModified {
		me.LastModified = new(r.ObjState.Mtime)
	}
	return BeforeVerify(me)
}

// copyMarkerMsg is the message of a copy source naming a delete marker's
// version, rgw-go's own.
const copyMarkerMsg = "The copy source names a version that is a delete marker."

// copySourceMarker refuses src, a copy's source state read for key in
// bucket, with 400 InvalidRequest when it is a delete marker's own version
// and r's requester holds a, the source's version action, on it, as Amazon S3
// refuses such a source
// (https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPartCopy.html,
// x-amz-copy-source). radosgw answers it as a missing source, 404 or 403
// (docs/ceph-upstream-bugs.md, "radosgw answers a copy source naming a
// delete marker's version 404 NoSuchKey"; docs/exclusions.md, "A copy source
// naming a delete marker's version answers 400 InvalidRequest"). Any other
// source, and a requester who does not hold a, gets nil, and the missing
// source's answer.
func copySourceMarker(ctx context.Context, r *Request, a policy.Action, bucket *BucketRecord, src *ObjectState, key meta.ObjKey) error {
	if !markerVersion(src, key) || !mayReadMarker(ctx, r, bucket, key, a) {
		return nil
	}
	return fmt.Errorf("%w: version %s of %s is a delete marker", ErrInvalidRequest.WithMessage(copyMarkerMsg), key.Instance, key.Name)
}
