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
// policies were refused with -ENOENT (rgw_rest.cc:1936-1945 at v20.2.4;
// v19.2.6 adds none, :1893-1933): it loads the key's state following the
// olh and names whether the current version is a delete marker. That
// refusal is the NoSuchKey the authorizer marks BeforeVerify; a missing
// bucket is -ERR_NO_SUCH_BUCKET, and a NoSuchKey execute finds comes after
// read_permissions.
//
// radosgw's state is true only for an olh whose current version is a
// delete marker (follow_olh, driver/rados/rgw_rados.cc:9760-9764
// at v20.2.4), a head rgw-go answers with 501 before any permission check.
// So a state r already read for the key is false; otherwise, for ListParts
// of a missing upload, whose state is the meta object's, the key is
// stated, and an olh head, which rgw-go cannot read, sends no header
// (docs/exclusions.md). radosgw ignores the load's other failures, keeping
// false.
func setMissingDeleteMarker(ctx context.Context, h http.Header, r *op.Request, err error) {
	if r.Env == nil || r.Env.Zone == nil || r.Env.Zone.Release() < denc.Tentacle ||
		r.Method != http.MethodGet && r.Method != http.MethodHead || r.Object.Name == "" || r.BucketRec == nil ||
		!op.IsBeforeVerify(err) || !errors.Is(err, op.ErrNoSuchKey) {
		return
	}
	if st := r.ObjState; st == nil || st.Key != r.Object {
		if _, serr := r.Env.Objects.StatObject(ctx, r.BucketRec, r.Object); errors.Is(serr, op.ErrNotImplemented) {
			return
		}
	}
	SetHeader(h, "x-amz-delete-marker", "false")
}
