package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// UpdateOLH implements op.ObjectStore as Object::update_olh does on Ceph
// main: it reads key's OLH head without following it, does nothing when the
// head is missing or no OLH head, and otherwise replays the name's pending log
// (updateOLH), whose ECANCELED, another writer having replaced the OLH or
// applied the log, is success
// (https://github.com/ceph/ceph/blob/v22.0.0/src/rgw/driver/rados/rgw_sal_rados.cc#L3198-L3224).
//
// Main's replays only a head that carries a pending attr. This one replays a
// head without one too when the name's OLH entry is pending removal, so that
// it clears the OLH a refused clear left, its log untrimmed and its pending
// attrs since removed: apply_olh_log ends without a trim when clear_olh is
// refused
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8550-L8556 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9296-L9302 at v20.2.4),
// and no read replays a head without a pending attr (followOLH). The class
// sets pending removal only with the UNLINK_OLH of an unlink that leaves no
// version current, and clears it with every link at or above the entry's
// epoch, a link below that epoch logging nothing
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1760,
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1791 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1959-L1967 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1892-L1901,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1932 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2106-L2114 at v20.2.4),
// so while it holds no LINK_OLH follows that UNLINK_OLH in the log, and the
// replay, which reads the log from marker 0, ends in the clear. Any other
// head without a pending attr is left as main leaves it: its log was applied
// by a replay whose trim did not land, or holds an entry whose pending attr
// expired, which no follow replays either.
func (s *Store) UpdateOLH(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) error {
	what := rec.Info.Bucket.Name + "/" + key.Name
	head, err := s.readOLHHead(ctx, rec, olhHeadKey(key))
	if err != nil {
		return fmt.Errorf("reading the olh head of %s: %w", what, err)
	}
	if _, olh := head.Attrs[meta.AttrOLHVer]; !head.Exists || !olh {
		return nil
	}
	if !olhPendingLeft(head) {
		e, err := s.newOLHIndex(rec, head.Key).getOLH(ctx, head)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("reading the olh entry of %s: %w", what, err)
		case !e.PendingRemoval:
			return nil
		}
	}
	if err := s.updateOLH(ctx, rec, head); err != nil && !errors.Is(err, radosclient.ErrCanceled) {
		return fmt.Errorf("updating the olh of %s: %w", what, err)
	}
	return nil
}

// olhPendingLeft reports whether head carries a pending attr.
func olhPendingLeft(head *op.ObjectState) bool {
	for name := range head.Attrs {
		if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
			return true
		}
	}
	return false
}
