package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A versioned write that creates its key's OLH head, whose version a delete
// removes before the write links it, leaves the key a version marker at its
// plain index entry and an OLH entry marked for removal that holds no
// version, with no OLH head: the delete's replay cannot clear the OLH past
// the writer's pending attr, and the writer's cancel then removes the head
// its init created (docs/ceph-upstream-bugs.md, "radosgw leaves a key's
// version marker and OLH entry when a delete takes the key's first version
// before its link"). An unversioned write of the key on a bucket whose
// versioning is suspended links nothing, so nothing takes the OLH entry over,
// and the class prepares and completes the write's index entry through the
// version marker, at the null data key in the 0x80 namespace no listing reads
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108 at v20.2.4).
// clearStrandedOLH clears the entry and the marker first.

// strandedRemovals bounds the versions clearStrandedOLH looks up in an OLH
// entry's log, each an index read of its own: a log that names more counts as
// one with a version left.
const strandedRemovals = 16

// meetsStrandedOLH reports whether hw, a write over st, its head as read or
// as missing on the assume-no-entry pass, may find its key's OLH stranded: a
// client's write, one with an epoch, that creates the head of a key on a
// bucket whose versioning was set and that is no versioned op. That is a
// write on a suspended bucket naming no version, as an enabled bucket links
// every write, which takes an OLH entry marked for removal over
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1768-L1777 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1909-L1918 at v20.2.4).
// A write over a head that exists is none, a rewrite of one included.
func meetsStrandedOLH(hw *headWrite, st *op.ObjectState, x *indexOp) bool {
	return hw.olhEpoch != nil && !st.Exists && !x.blind && hw.key.NS == "" &&
		hw.rec.Info.Versioned() && !versionedOp(hw, st)
}

// clearStrandedOLH clears the OLH of key's name in rec's bucket when it is
// stranded, so that the write that called it lists:
//   - the plain entry is a version marker, and the OLH entry is marked for
//     removal and holds no version, as radosgw's bucket check olh finds them;
//   - no head exists, so no replay can apply the entry's pending log or clear
//     it, and the head removal clear_olh sends before its index clear has
//     nothing to remove;
//   - nothing is left behind the marker that the clear would drop: the null
//     version's data entry is missing, as one a write left there is counted in
//     the bucket's stats and reached only through the marker, and no version
//     the OLH entry's log names for removal still has an index entry, as the
//     next link would apply that removal, having kept the log.
//
// The clear is bucket_clear_olh with the entry's own tag, the call radosgw's
// clear_olh sends. radosgw's bucket check olh --fix clears an entry with it only
// when its log is empty, and for one with a log reads the log under the
// head's OLH tag, which a missing head does not hold, so the class refuses
// that read with ECANCELED
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L470-L552 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L607-L689 at v20.2.4).
// The log a refused clear leaves is not empty, as apply_olh_log trims nothing
// after a clear
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8550-L8563 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9296-L9309 at v20.2.4).
//
// The class compares the tag alone, and a delete's unlink appends a removal to
// the log without touching it, so the clear carries a compare of the entry
// as read and of the null data key's absence (clearUnchanged), and any change
// since, a writer's link or a logged removal or a write behind the marker,
// answers ECANCELED. Each attempt then reads the key again, up to olhAttempts
// as set_olh does, and goes on without a clear when the key is no longer
// stranded.
//
// An entry that does not decode is no stranded OLH: the write goes on as
// radosgw's does, where a failure here would fail every write to the key. Any
// other failure is returned, with the write unwritten, so that a retry clears
// again.
func (s *Store) clearStrandedOLH(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) error {
	idx := s.newOLHIndex(rec, key)
	for range olhAttempts {
		retry, err := s.clearStrandedOnce(ctx, idx, key)
		switch {
		case errors.Is(err, errUndecodable):
			slog.WarnContext(ctx, "an index entry of a key does not decode, so its OLH is left as it is and the write goes on",
				slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.Any("error", err))
			return nil
		case err != nil:
			return err
		case !retry:
			return nil
		}
	}
	return fmt.Errorf("%w: clearing the stranded olh of %s/%s: every one of %d attempts lost to another writer",
		op.ErrUnknown, rec.Info.Bucket.Name, key.Name, olhAttempts)
}

// clearStrandedOnce is one attempt of clearStrandedOLH through idx, the OLH
// index of key's name. It reports retry when the class refused the clear with
// ECANCELED.
func (s *Store) clearStrandedOnce(ctx context.Context, idx *bucketOLHIndex, key meta.ObjKey) (retry bool, err error) {
	rec := idx.x.rec
	what := rec.Info.Bucket.Name + "/" + key.Name
	marked, err := idx.versionMarked(ctx)
	switch {
	case err != nil:
		return false, fmt.Errorf("reading the plain index entry of %s: %w", what, err)
	case !marked:
		return false, nil
	}
	head := &op.ObjectState{Bucket: rec, Key: olhHeadKey(key)}
	e, olh, err := idx.getOLHBI(ctx, head)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reading the olh entry of %s: %w", what, err)
	case e.Exists || !e.PendingRemoval:
		return false, nil
	}
	cur, err := s.readOLHHead(ctx, rec, head.Key)
	switch {
	case err != nil:
		return false, fmt.Errorf("reading the olh head of %s: %w", what, err)
	case cur.Exists:
		return false, nil
	}
	left, err := s.removalsLeft(ctx, rec, key, e.PendingLog)
	switch {
	case err != nil:
		return false, fmt.Errorf("looking for what the olh of %s leaves behind its version marker: %w", what, err)
	case left:
		return false, nil
	}
	head.Attrs = map[string][]byte{meta.AttrOLHIDTag: []byte(e.Tag)}
	err = idx.clearUnchanged(ctx, head, olh, nullDataKey(key))
	switch {
	case errors.Is(err, radosclient.ErrCanceled):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("clearing the olh of %s: %w", what, err)
	}
	slog.DebugContext(ctx, "cleared the OLH a delete left pending removal, so that the key's next write is listed",
		slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name))
	return false, nil
}

// nullDataKey is the omap key of the null version's data entry of key's
// name, behind its version marker.
func nullDataKey(key meta.ObjKey) string {
	return rgw.InstanceIndexKey(versionKey(olhHeadKey(key)), false)
}

// removalsLeft reports whether something is left behind the version marker
// of key's name that a clear of its OLH would drop: the null version's data
// entry, or the index entry of a version log, the OLH entry's pending log,
// names for removal (at most strandedRemovals of them).
func (s *Store) removalsLeft(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, log map[uint64][]rgw.OLHLogEntry) (bool, error) {
	if _, err := s.nullDataEntry(ctx, rec, key); !errors.Is(err, radosclient.ErrNotFound) {
		if err != nil {
			err = fmt.Errorf("reading the null version's data entry: %w", err)
		}
		return true, err
	}
	seen := map[string]bool{}
	for _, entries := range log {
		for _, e := range entries {
			if e.Op != rgw.OLHOpRemoveInstance || e.Key.Instance == "" || seen[e.Key.Instance] {
				continue
			}
			if len(seen) == strandedRemovals {
				return true, nil
			}
			seen[e.Key.Instance] = true
			if _, err := s.getInstanceEntry(ctx, rec, indexToObjKey(e.Key)); !errors.Is(err, radosclient.ErrNotFound) {
				if err != nil {
					err = fmt.Errorf("reading the entry of version %q: %w", e.Key.Instance, err)
				}
				return true, err
			}
		}
	}
	return false, nil
}

// versionMarked reports whether the plain index entry of the name is a
// version marker, read as bi_get reads the Instance entry of the plain key. An
// entry that is missing is no marker, taken as the shard the bucket record
// names answers it: readShard reads the bucket instance and the shard again
// to tell a missing entry from a shard a reshard has moved, which every
// unversioned write to a suspended bucket would pay for. A name a reshard has
// moved reads as no marker, and the write goes on as radosgw's does.
func (b *bucketOLHIndex) versionMarked(ctx context.Context) (bool, error) {
	x := b.x
	sh, err := x.currentShard(ctx)
	if err != nil {
		return false, err
	}
	if werr := x.s.w.completions.awaitSent(ctx, x.waitKey()); werr != nil {
		return false, werr
	}
	en, err := x.dirEntry(ctx, sh, b.olhKey(), rgw.BIIndexInstance)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return en.Flags&rgw.FlagVerMarker != 0, nil
}
