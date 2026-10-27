package memstore

import (
	"fmt"
	"maps"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// withDefaultRetention gives o, the object a create write to rec's bucket
// made, objectlock.DefaultRetention of rec and o's mtime, as the driver's
// head write gives it. A lock configuration that does not decode is
// UnknownError, and the caller stores nothing.
func withDefaultRetention(rec *op.BucketRecord, o *object) error {
	r, err := objectlock.DefaultRetention(&rec.Info, o.state.Attrs, o.state.Mtime)
	if err != nil {
		return fmt.Errorf("%w: %w", op.ErrUnknown, err)
	}
	if r != nil {
		o.state.Attrs[meta.AttrObjectRetention] = objectlock.EncodeRetention(*r)
	}
	return nil
}

// withoutLockAttrs is a copy source's attrs without its retention and legal
// hold, which copy_obj drops for the request's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4800-L4807 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5013-L5020 at v20.2.4).
func withoutLockAttrs(attrs map[string][]byte) map[string][]byte {
	out := maps.Clone(attrs)
	delete(out, meta.AttrObjectRetention)
	delete(out, meta.AttrObjectLegalHold)
	return out
}

// withUploadLock is attrs with u's retention and legal hold, which a
// completion writes as its object's attrs
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3601-L3610 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4445-L4454 at v20.2.4).
func withUploadLock(attrs map[string][]byte, u *upload) map[string][]byte {
	if u.retention == nil && u.legalHold == nil {
		return attrs
	}
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	if u.retention != nil {
		attrs[meta.AttrObjectRetention] = objectlock.EncodeRetention(*u.retention)
	}
	if u.legalHold != nil {
		attrs[meta.AttrObjectLegalHold] = objectlock.EncodeLegalHold(*u.legalHold)
	}
	return attrs
}
