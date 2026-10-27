package objectlock

import (
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// DefaultRetention is the retention a create write, PUT_OBJ_CREATE, stamped
// at mtime takes from its bucket's object lock rule, as _do_write_meta gives
// it in the head write's own op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3211-L3221 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3308-L3318 at v20.2.4):
// the rule's mode, retained until LockUntil of the rule and mtime. It is nil
// when info's flags do not enable object lock, when its lock has no rule,
// and when attrs, the write's, carry a user.rgw.object-retention. An empty
// attr counts as none, where radosgw's find takes it for one: a head write
// skips an empty attr, radosgw's as rgw-go's, so taking it would leave the
// object with no retention at all. A lock configuration that does not decode
// is an error, whatever attrs carry.
func DefaultRetention(info *meta.BucketInfo, attrs map[string][]byte, mtime time.Time) (*meta.ObjectRetention, error) {
	if !info.ObjLockEnabled() {
		return nil, nil
	}
	l, err := info.ObjectLock()
	if err != nil {
		return nil, fmt.Errorf("the default retention of bucket %s: %w", info.Bucket.Name, err)
	}
	if l.Rule == nil || len(attrs[meta.AttrObjectRetention]) > 0 {
		return nil, nil
	}
	d := l.Rule.DefaultRetention
	return &meta.ObjectRetention{Mode: d.Mode, RetainUntil: LockUntil(d, mtime)}, nil
}

// EncodeRetention is r as its user.rgw.object-retention attr holds it,
// RGWObjectRetention::encode, which encodes alike at both releases.
func EncodeRetention(r meta.ObjectRetention) []byte {
	e := denc.NewEncoder()
	r.Encode(e, denc.Squid)
	return e.Bytes()
}

// EncodeLegalHold is h as its user.rgw.object-legal-hold attr holds it,
// RGWObjectLegalHold::encode, which encodes alike at both releases.
func EncodeLegalHold(h meta.ObjectLegalHold) []byte {
	e := denc.NewEncoder()
	h.Encode(e, denc.Squid)
	return e.Bytes()
}
