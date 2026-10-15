package op

import (
	"context"
	"encoding/json"
)

//counterfeiter:generate . LayoutStore

// LayoutStore reads where an object's data sits in RADOS. It stays off
// ObjectStore, which every data path calls.
type LayoutStore interface {
	// ObjectLayout is st's head object, manifest and data locations, the
	// document RadosObject::dump_obj_layout writes
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2780-L2826 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3615-L3661 at v20.2.4).
	ObjectLayout(ctx context.Context, st *ObjectState) (json.RawMessage, error)
}
