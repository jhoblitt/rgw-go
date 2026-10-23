package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// The OLH head operations are the OLH protocol's steps on a versioned key's
// head object, the key with no instance, which RGWRados runs around the
// bucket index's link, unlink, log, trim and clear. Each takes the head as
// get_obj_state read it without following the OLH, as radosgw's take its
// RGWObjState, and guards its writes on what that read saw.

const (
	// olhPendingTimeoutDefault is rgw_olh_pending_timeout_sec's default,
	// 1_hr
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2389-L2399 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2483-L2493 at v20.2.4).
	olhPendingTimeoutDefault = time.Hour
	// olhPendingRemoveBatch is remove_olh_pending_entries' max_entries, the
	// most pending attrs one op removes.
	olhPendingRemoveBatch = 1000
)

// errRemovalRefused marks an olhIndex.removeInstance that its expected
// write tag refused.
var errRemovalRefused = errors.New("the version's write tag is not the one expected; its removal was refused")

// errRefusalUnsettled marks a refused removal that is not final and that
// running the replay's tail again cannot settle: the writer's entries are
// still in the pending log of an OLH entry another writer's link re-tagged.
var errRefusalUnsettled = errors.New("the refused removal is still in an OLH log under another writer's tag, whose replay can remove the version")

// olhIndex is what the OLH head operations ask of the bucket index, on the
// shard of the head's name and with the head's OLH tag: the pending log's
// read after marker, its trim up to ver, the clear of the name's OLH entry,
// and bi_get of that entry, which answers not found when the name has none.
// removeInstance removes the version key names as the unversioned delete
// does, guarded on its head's write tag or, when expectTag is not nil, on
// expectTag; a version already gone answers not found.
//
// A removal that expectTag refuses removes nothing and answers an error
// wrapping errRemovalRefused: the version's head and instance entry stay,
// though the unlink that logged the removal has taken its list entry.
// applyOLHLog passes over such a removal and returns the refusal once its
// replay has landed: its head op removes the entry's pending attr, and its
// trim or clear drops the entry from the log. The refusal comes back one of
// three ways:
//   - alone: final;
//   - joined with an olhTailError, a trim, index clear or clear expiry that
//     failed, with the entry still in the log: the caller runs the tail it
//     names again through finishOLHLog, with the same head, before it acts
//     on the refusal;
//   - joined with errRefusalUnsettled: the entry is still in the pending log
//     of an OLH entry another writer's link re-tagged. Running the tail
//     again cannot restore the old tag, so the caller does not retry it, and
//     that writer's replay can remove the version.
//
// Once the writer's own replay has landed with its trim or clear, no later
// replay meets the entry. Until then the version can still go, guarded only
// on the write tag a replay reads, as radosgw's replay removes it: a replay
// by another reader running alongside the writer's own does not know
// expectTag, neither does one that runs after the writer's head op failed
// and its retry never landed, and neither does the replay of a re-tagged
// entry's kept log.
type olhIndex interface {
	readLog(ctx context.Context, head *op.ObjectState, marker uint64) (rgw.ReadOLHLogRet, error)
	trimLog(ctx context.Context, head *op.ObjectState, ver uint64) error
	clear(ctx context.Context, head *op.ObjectState) error
	removeInstance(ctx context.Context, key meta.ObjKey, expectTag []byte) error
	getOLH(ctx context.Context, head *op.ObjectState) (rgw.OLHEntry, error)
}

// olhCondGuard makes an init commit only on the OLH its caller evaluated a
// condition on: info is olh.info as that read saw it, empty when absent, and
// noPending also refuses the init while another writer's pending attr
// remains. radosgw's writers guard on the OLH tag alone, so this serializes
// rgw-go's conditional writers only. With checkVer the init also requires
// olh.ver to be ver, the bytes the same read saw, so that a replay applied
// since, one that left olh.info as it was included, refuses it too.
type olhCondGuard struct {
	info      []byte
	noPending bool
	ver       []byte
	checkVer  bool
}

// olhInitModification is olh_init_modification
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7799-L7910 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8462-L8573 at v20.2.4):
// one op on head that records a pending modification under a new op tag,
// creating the head exclusively when it is missing and otherwise asserting
// it and keeping its mtime. A head with an OLH tag is guarded on it. A head
// without one, a plain object becoming versioned, is guarded on having none
// and gets an olh.info naming itself, so the change is not seen; it and a
// missing head get fresh write and OLH tags and an empty olh.ver. A lost
// exclusive create is ECANCELED. cond, when not nil, adds its guard ahead of
// every set, so that it sees other writers only, and expectTag, when not
// nil, adds there cmpxattr(user.rgw.idtag, EQ, expectTag), which init itself
// sets on a head without an OLH tag. A missing head needs neither: the
// exclusive create refuses whatever another writer made, where a cmpxattr
// on a missing object answers ENOENT rather than reading the xattr as empty
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6403-L6408 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6472-L6477 at v20.2.4).
// On success head is updated as radosgw updates its state: it exists, holds
// the pending attr, and holds the new tags it was given, but not the
// olh.info or olh.ver written. It returns the op tag and the olh.ver head
// was read with.
func (s *Store) olhInitModification(ctx context.Context, head *op.ObjectState, cond *olhCondGuard, expectTag []byte) (opTag string, ver []byte, err error) {
	if head.Key.Instance != "" {
		return "", nil, fmt.Errorf("initializing an olh modification of %s: the head names instance %q", head.Key.Name, head.Key.Instance)
	}
	hasTag := olhTagged(head)
	w := radosclient.NewWriteOp()
	if !head.Exists {
		w.Create(true)
	} else {
		w.AssertExists()
		w.SetMtime(head.Mtime)
		if expectTag != nil {
			w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, expectTag)
		}
		if cond != nil {
			w.CmpXattr(meta.AttrOLHInfo, radosclient.CmpEQ, cond.info)
			if cond.noPending {
				rgw.ObjCheckAttrsPrefix(w, meta.AttrOLHPendingPrefix, true, s.release)
			}
			if cond.checkVer {
				w.CmpXattr(meta.AttrOLHVer, radosclient.CmpEQ, cond.ver)
			}
		}
	}
	switch {
	case hasTag:
		guardOLHTag(w, head)
	case head.Exists:
		w.CmpXattr(meta.AttrOLHIDTag, radosclient.CmpEQ, nil)
		self := meta.OLHInfo{Target: meta.Obj{Bucket: head.Bucket.Info.Bucket, Key: head.Key}}
		w.SetXattr(meta.AttrOLHInfo, encodeAt(self, s.release))
	}
	var idTag, olhTag []byte
	if !hasTag {
		idTag, olhTag = []byte(meta.NewOLHTag()), []byte(meta.NewOLHTag())
		w.SetXattr(meta.AttrIDTag, idTag)
		w.SetXattr(meta.AttrOLHIDTag, olhTag)
		w.SetXattr(meta.AttrOLHVer, []byte{})
	}
	now := s.now()
	opTag = meta.NewOLHOpTag(now)
	name := meta.AttrOLHPendingPrefix + opTag
	pending := encodeAt(meta.OLHPendingInfo{Time: now}, s.release)
	w.SetXattr(name, pending)
	ver = slices.Clone(head.Attrs[meta.AttrOLHVer])
	if err = s.writeOLHHead(ctx, head, w); err != nil {
		if errors.Is(err, radosclient.ErrExists) {
			return "", nil, fmt.Errorf("initializing an olh modification of %s/%s: another writer created the head: %w", head.Bucket.Info.Bucket.Name, head.Key.Name, radosclient.ErrCanceled)
		}
		return "", nil, err
	}
	if head.Attrs == nil {
		head.Attrs = map[string][]byte{}
	}
	if !hasTag {
		head.Attrs[meta.AttrIDTag] = idTag
		head.WriteTag = string(idTag)
		head.Attrs[meta.AttrOLHIDTag] = olhTag
	}
	head.Exists = true
	head.Attrs[name] = pending
	return opTag, ver, nil
}

// olhTagged reports whether head exists with an OLH tag, which
// olhInitModification guards on and keeps; it gives any other head fresh
// write and OLH tags.
func olhTagged(head *op.ObjectState) bool {
	_, ok := head.Attrs[meta.AttrOLHIDTag]
	return ok && head.Exists
}

// olhCancelModification is olh_cancel_modification
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7754-L7797 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8417-L8460 at v20.2.4):
// it removes opTag's pending attr under the OLH tag guard and then, when head
// was read without olh.info, removes the head unless it has gained one or a
// pending attr remains. It is best effort: a failure is logged, and the
// pending attr expires.
func (s *Store) olhCancelModification(ctx context.Context, head *op.ObjectState, opTag string) {
	w := radosclient.NewWriteOp()
	guardOLHTag(w, head)
	w.RmXattr(meta.AttrOLHPendingPrefix + opTag)
	err := s.writeOLHHead(ctx, head, w)
	if err == nil {
		if _, ok := head.Attrs[meta.AttrOLHInfo]; !ok {
			rm := radosclient.NewWriteOp()
			guardOLHTag(rm, head)
			rm.CmpXattr(meta.AttrOLHInfo, radosclient.CmpEQ, nil)
			rgw.ObjCheckAttrsPrefix(rm, meta.AttrOLHPendingPrefix, true, s.release)
			rm.Remove()
			err = s.writeOLHHead(ctx, head, rm)
		}
	}
	if err != nil && !errors.Is(err, radosclient.ErrNotFound) && !errors.Is(err, radosclient.ErrCanceled) {
		slog.WarnContext(ctx, "olh modification cancel failed; its pending attr is left to expire",
			slog.String("bucket", head.Bucket.Info.Bucket.Name), slog.String("key", head.Key.Name), slog.String("op_tag", opTag), slog.Any("error", err))
	}
}

// checkPendingOLHEntries is check_pending_olh_entries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8904-L8934 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9650-L9680 at v20.2.4):
// it walks head's pending attrs in name order, which is the order of the
// seconds their op tags start with, and expires each stamped at least
// rgw_olh_pending_timeout_sec before the gateway's clock until the first that
// is not. An attr that does not decode is logged and stays pending. It
// returns the expired names and those still pending, each in name order.
func (s *Store) checkPendingOLHEntries(ctx context.Context, head *op.ObjectState) (expired, pending []string) {
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range head.Attrs {
			if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) && !yield(name) {
				return
			}
		}
	})
	now, timeout := s.now(), s.olhPendingTimeout()
	for i, name := range names {
		info, err := decodeAttr(head.Attrs[name], meta.DecodeOLHPendingInfo)
		if err != nil {
			slog.ErrorContext(ctx, "pending olh entry does not decode; skipping it",
				slog.String("bucket", head.Bucket.Info.Bucket.Name), slog.String("key", head.Key.Name), slog.String("attr", name), slog.Any("error", err))
			pending = append(pending, name)
			continue
		}
		if now.Sub(info.Time) < timeout {
			pending = append(pending, names[i:]...)
			break
		}
		expired = append(expired, name)
	}
	return expired, pending
}

// olhPendingTimeout is rgw_olh_pending_timeout_sec as the configuration holds
// it now, as radosgw reads it on each check, or its default when it cannot be
// read.
func (s *Store) olhPendingTimeout() time.Duration {
	if s.conf == nil {
		return olhPendingTimeoutDefault
	}
	d, err := s.conf.Seconds("rgw_olh_pending_timeout_sec")
	if err != nil {
		return olhPendingTimeoutDefault
	}
	return d
}

// removeOLHPendingEntries is remove_olh_pending_entries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8936-L8967 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9682-L9713 at v20.2.4):
// it removes names from head in ops of up to olhPendingRemoveBatch, each
// under the OLH tag guard. A batch that finds the head gone or its tag
// changed ends the removal as success: another writer has moved the head on.
func (s *Store) removeOLHPendingEntries(ctx context.Context, head *op.ObjectState, names []string) error {
	for batch := range slices.Chunk(names, olhPendingRemoveBatch) {
		w := radosclient.NewWriteOp()
		guardOLHTag(w, head)
		for _, name := range batch {
			w.RmXattr(name)
		}
		err := s.writeOLHHead(ctx, head, w)
		if errors.Is(err, radosclient.ErrNotFound) || errors.Is(err, radosclient.ErrCanceled) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// expireOLHPending removes head's expired pending attrs and returns the
// names still pending.
func (s *Store) expireOLHPending(ctx context.Context, head *op.ObjectState) ([]string, error) {
	expired, pending := s.checkPendingOLHEntries(ctx, head)
	if len(expired) > 0 {
		if err := s.removeOLHPendingEntries(ctx, head, expired); err != nil {
			return nil, err
		}
	}
	return pending, nil
}

// followOLH is follow_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8969-L9021 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9715-L9769 at v20.2.4):
// it expires head's old pending attrs, runs update when any remains, and
// resolves the current version from head: its key, or not found with the
// delete-marker flag set when that version is a delete marker, as v20.2.4
// sets is_dm. update's ECANCELED, the OLH tag changed in the index or on the
// head, means the OLH was removed, so it answers not found. Without olh.ver
// head is no OLH (EINVAL); without olh.info it has no current version. The
// version is resolved from head as given, which radosgw's update_olh leaves
// as read, so a follow that ran update answers the version current before
// it.
func (s *Store) followOLH(ctx context.Context, head *op.ObjectState, update func(context.Context, *op.ObjectState) error) (meta.ObjKey, bool, error) {
	pending, err := s.expireOLHPending(ctx, head)
	if err != nil {
		return meta.ObjKey{}, false, err
	}
	what := head.Bucket.Info.Bucket.Name + "/" + head.Key.Name
	if len(pending) > 0 {
		err = update(ctx, head)
		if errors.Is(err, radosclient.ErrCanceled) {
			return meta.ObjKey{}, false, fmt.Errorf("following the olh of %s: it was replaced while it was updated: %w", what, radosclient.ErrNotFound)
		}
		if err != nil {
			return meta.ObjKey{}, false, err
		}
	}
	if _, ok := head.Attrs[meta.AttrOLHVer]; !ok {
		return meta.ObjKey{}, false, fmt.Errorf("following the olh of %s: the head has no olh.ver: %w", what, radosclient.ErrInvalid)
	}
	b, ok := head.Attrs[meta.AttrOLHInfo]
	if !ok {
		return meta.ObjKey{}, false, fmt.Errorf("following the olh of %s: the head has no olh.info: %w", what, radosclient.ErrNotFound)
	}
	info, err := decodeOLHInfo(head, b)
	if err != nil {
		return meta.ObjKey{}, false, err
	}
	if info.Removed {
		return meta.ObjKey{}, true, fmt.Errorf("following the olh of %s: the current version is a delete marker: %w", what, radosclient.ErrNotFound)
	}
	return info.Target.Key, false, nil
}

// applyOLHLog is apply_olh_log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8409-L8566 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9155-L9312 at v20.2.4):
// it replays log, the pending log by epoch, onto head in one op that sets
// olh.ver to the last epoch, keeps head's mtime and removes each entry's
// pending attr. In epoch order a LINK_OLH takes the current version when its
// epoch is above head's olh.ver, when the current version has no instance,
// or when its own instance sorts before the current one; an UNLINK_OLH marks
// the OLH for removal until a later link; a REMOVE_INSTANCE names a version
// to remove. Those versions are removed through idx before the head op, the
// entries carrying opTag guarded on expectTag when it is not nil; one already
// gone is passed over, and any other failure stops the replay before the head
// op, as radosgw's does. A removal of the null version that a later LINK_OLH
// of null data follows, in log or, with nullLinkLater, on a later page of the
// replay (replayPass), is dropped: the null version's removal names its key,
// not the data it replaced
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1810 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1971 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1951 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2118 at v20.2.4),
// and that link's write has put its own data in the head the two share,
// where radosgw's replay removes it (docs/exclusions.md, "A replay keeps the
// null data a later null link names"). After the head op, finishOLHLog
// clears the OLH marked for removal, and otherwise trims the log to the last
// epoch. It returns the last epoch whenever the head op landed, and 0 when
// the replay wrote nothing; a tail that did not finish comes with the last
// epoch and an olhTailError naming the step left.
//
// A removal expectTag refuses (errRemovalRefused, olhIndex) does not stop the
// replay: it is passed over, and once the head op and the clear or trim have
// landed the refusal is returned, with the last epoch. A head op that fails
// returns its own error alone, since the entry stays in the log for the
// retry; a tail that did not finish is returned joined with the refusal,
// which is then not final.
//
// The head op is guarded on head's OLH tag and on olh.ver equal to the bytes
// head was read with, where radosgw compares olh.ver in the OSD's u64 mode,
// which librados' C API cannot send (docs/cgo-limitations.md). An apply that
// another writer has moved past answers ECANCELED and writes nothing.
func (s *Store) applyOLHLog(ctx context.Context, idx olhIndex, head *op.ObjectState, log map[uint64][]rgw.OLHLogEntry, opTag string, expectTag []byte, nullLinkLater bool) (uint64, error) {
	if len(log) == 0 {
		return 0, nil
	}
	what := head.Bucket.Info.Bucket.Name + "/" + head.Key.Name
	epochs := slices.Sorted(maps.Keys(log))
	last := epochs[len(epochs)-1]
	w := radosclient.NewWriteOp()
	guardOLHTag(w, head)
	w.CmpXattr(meta.AttrOLHVer, radosclient.CmpEQ, head.Attrs[meta.AttrOLHVer])
	w.SetXattr(meta.AttrOLHVer, []byte(strconv.FormatUint(last, 10)))
	w.SetMtime(head.Mtime)

	// strict_strtoll reads olh.ver through c_str(), up to a NUL, and gives 0
	// for anything it refuses; the C++ stores the long long in a u64.
	digits, _, _ := strings.Cut(string(head.Attrs[meta.AttrOLHVer]), "\x00")
	n, _ := rgwtext.StrictStrtoll(digits)
	linkEpoch := uint64(n) //nolint:gosec // as link_epoch takes strict_strtoll's long long
	var cur rgw.ObjKey
	var deleteMarker bool
	if b, ok := head.Attrs[meta.AttrOLHInfo]; ok {
		info, err := decodeOLHInfo(head, b)
		if err != nil {
			return 0, err
		}
		cur = rgw.ObjKey{Name: info.Target.Key.IndexKeyName(), Instance: info.Target.Key.Instance}
		deleteMarker = info.Removed
	}
	var removals []rgw.OLHLogEntry
	var link, remove bool
	for _, epoch := range epochs {
		for _, e := range log[epoch] {
			switch e.Op {
			case rgw.OLHOpRemoveInstance:
				removals = append(removals, e)
			case rgw.OLHOpLinkOLH:
				if nullDataLink(e) {
					removals = slices.DeleteFunc(removals, func(r rgw.OLHLogEntry) bool { return r.Key.Instance == "" })
				}
				if linkEpoch < epoch || cur.Instance == "" || cur.Instance > e.Key.Instance {
					link, remove = true, false
					cur, deleteMarker = e.Key, e.DeleteMarker
				}
			case rgw.OLHOpUnlinkOLH:
				link, remove = false, true
			default:
				return 0, fmt.Errorf("%w: replaying the olh log of %s: epoch %d has op %d", op.ErrUnknown, what, epoch, e.Op)
			}
			w.RmXattr(meta.AttrOLHPendingPrefix + e.OpTag)
		}
	}
	if nullLinkLater {
		removals = slices.DeleteFunc(removals, func(r rgw.OLHLogEntry) bool { return r.Key.Instance == "" })
	}
	if link {
		target := meta.OLHInfo{Target: meta.Obj{Bucket: head.Bucket.Info.Bucket, Key: indexToObjKey(cur)}, Removed: deleteMarker}
		w.SetXattr(meta.AttrOLHInfo, encodeAt(target, s.release))
	}
	var refused error
	var keepHead bool
	for _, e := range removals {
		var guard []byte
		if expectTag != nil && e.OpTag == opTag {
			guard = expectTag
		}
		key := indexToObjKey(e.Key)
		err := idx.removeInstance(ctx, key, guard)
		switch {
		case err == nil, errors.Is(err, radosclient.ErrNotFound), errors.Is(err, op.ErrNoSuchKey):
		case guard != nil && errors.Is(err, errRemovalRefused):
			refused = errors.Join(refused, fmt.Errorf("replaying the olh log of %s: removing version %q: %w", what, key.Instance, err))
			keepHead = keepHead || key.Instance == ""
		default:
			return 0, fmt.Errorf("replaying the olh log of %s: removing version %q: %w", what, key.Instance, err)
		}
	}
	if err := s.writeOLHHead(ctx, head, w); err != nil {
		return 0, err
	}
	t := olhLogTail{last: last, clear: remove, keep: refused != nil, keepHead: keepHead, opTag: opTag}
	if err := s.finishOLHLog(ctx, idx, head, t); err != nil {
		return last, errors.Join(err, refused)
	}
	return last, refused
}

// olhLogTail is the step that ends a replay once its head op has written
// last to olh.ver: the clear of an OLH the replay left without a version,
// or otherwise the log's trim to last. keep is set while a removal the
// writer's expected write tag refused is pending, keepHead when that removal
// was the null version's, whose data is the OLH head's, and opTag is the
// writer's op tag, which its entries in the log carry.
type olhLogTail struct {
	last     uint64
	clear    bool
	keep     bool
	keepHead bool
	opTag    string
}

// olhTailError is a replay whose head op landed but whose tail did not
// finish: tail is the step left, which finishOLHLog runs again.
type olhTailError struct {
	tail olhLogTail
	err  error
}

func (e *olhTailError) Error() string { return e.err.Error() }

func (e *olhTailError) Unwrap() error { return e.err }

// finishOLHLog runs t on head's log, the tail of a replay whose head op has
// landed, and is how a caller runs again the tail an olhTailError names. A
// clear or trim that answers ECANCELED, another writer having moved on
// first, is success, but for a clear that another writer's pending attr
// refuses while t.keep is set: the trim follows it, which radosgw's replay,
// with no refusal to keep, never runs after a clear_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8550-L8563 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9296-L9309 at v20.2.4).
// The head op holds every entry up to t.last, and the class refuses the
// trim only on an olh_tag the index entry no longer carries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2102-L2105 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2243-L2246 at v20.2.4),
// which does not prove the writer's entries left: settleOLHRefusal decides
// such a refused trim while t.keep is set. A clear or trim that fails
// otherwise, the clear's expiry included, returns an olhTailError naming
// the step left: the clear, or the trim. With t.keepHead the trim takes the
// clear's place: the clear removes the OLH head object, which holds the null
// version's data the refusal kept, where radosgw's replay has removed that
// data before its clear.
func (s *Store) finishOLHLog(ctx context.Context, idx olhIndex, head *op.ObjectState, t olhLogTail) error {
	what := head.Bucket.Info.Bucket.Name + "/" + head.Key.Name
	if t.keepHead {
		t.clear = false
	}
	if t.clear {
		err := s.clearOLHHead(ctx, idx, head, t.last)
		switch {
		case err == nil:
			return nil
		case !errors.Is(err, radosclient.ErrCanceled):
			return &olhTailError{tail: t, err: fmt.Errorf("replaying the olh log of %s: clearing it at epoch %d: %w", what, t.last, err)}
		case !t.keep:
			return nil
		}
		// Another writer came between the head op and the clear, so the
		// refused entry is still in the log, where the next replay would
		// remove the version unguarded.
		t.clear = false
	}
	err := idx.trimLog(ctx, head, t.last)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, radosclient.ErrCanceled):
		return &olhTailError{tail: t, err: fmt.Errorf("replaying the olh log of %s: trimming it to epoch %d: %w", what, t.last, err)}
	case !t.keep:
		return nil
	}
	return s.settleOLHRefusal(ctx, idx, head, t)
}

// settleOLHRefusal decides a trim that the class refused, while a refused
// removal is pending, because the index's OLH entry no longer carries head's
// OLH tag. The class's set_tag replaces only the tag
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1563-L1565 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1690-L1692 at v20.2.4),
// and a link re-tags an OLH entry whose pending_removal is set, keeping its
// pending log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1768-L1777 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1909-L1918 at v20.2.4),
// so the writer's entries may still be in it, where the new writer's
// replay removes the version unguarded. The refusal is final when getOLH
// finds no entry, or an entry whose pending log holds nothing carrying
// t.opTag; otherwise it is errRefusalUnsettled. A getOLH that fails returns
// an olhTailError naming the trim, whose run again checks once more.
func (s *Store) settleOLHRefusal(ctx context.Context, idx olhIndex, head *op.ObjectState, t olhLogTail) error {
	what := head.Bucket.Info.Bucket.Name + "/" + head.Key.Name
	e, err := idx.getOLH(ctx, head)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return nil
	case err != nil:
		return &olhTailError{tail: t, err: fmt.Errorf("replaying the olh log of %s: reading its olh entry after a refused trim: %w", what, err)}
	}
	for _, entries := range e.PendingLog {
		for _, le := range entries {
			if le.OpTag == t.opTag {
				return fmt.Errorf("replaying the olh log of %s: its olh entry carries tag %q: %w", what, e.Tag, errRefusalUnsettled)
			}
		}
	}
	return nil
}

// clearOLHHead is clear_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8583-L8634 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9329-L9380 at v20.2.4),
// which removes an OLH left with no version: it expires head's old pending
// attrs, removes the head under its OLH tag, olh.ver equal to ver and no
// pending attr, then clears the name's OLH entry from the index. A removal
// those guards refuse is ECANCELED, the index left alone; a removal that
// fails otherwise, the head gone included, still clears the index, as
// clear_olh does (docs/ceph-upstream-bugs.md). The decimal ver is the bytes
// radosgw's u64 compare matches, as an apply writes olh.ver.
func (s *Store) clearOLHHead(ctx context.Context, idx olhIndex, head *op.ObjectState, ver uint64) error {
	if _, err := s.expireOLHPending(ctx, head); err != nil {
		return err
	}
	rm := radosclient.NewWriteOp()
	guardOLHTag(rm, head)
	rm.CmpXattr(meta.AttrOLHVer, radosclient.CmpEQ, []byte(strconv.FormatUint(ver, 10)))
	rgw.ObjCheckAttrsPrefix(rm, meta.AttrOLHPendingPrefix, true, s.release)
	rm.Remove()
	err := s.writeOLHHead(ctx, head, rm)
	if errors.Is(err, radosclient.ErrCanceled) {
		return err
	}
	if err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		slog.WarnContext(ctx, "olh head removal failed; clearing its index entry regardless, as radosgw does",
			slog.String("bucket", head.Bucket.Info.Bucket.Name), slog.String("key", head.Key.Name), slog.Any("error", err))
	}
	if err = idx.clear(ctx, head); err != nil {
		return fmt.Errorf("clearing the olh index entry of %s/%s: %w", head.Bucket.Info.Bucket.Name, head.Key.Name, err)
	}
	return nil
}

// repairOLH is repair_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8275-L8324 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9021-L9070 at v20.2.4),
// for a head whose OLH tag the index no longer has, as a multisite sync
// defect once left heads (https://tracker.ceph.com/issues/37792): unless the
// index's OLH entry already carries head's tag, it rewrites olh.idtag and
// olh.info from that entry under the old tag's guard, keeping head's mtime.
func (s *Store) repairOLH(ctx context.Context, idx olhIndex, head *op.ObjectState) error {
	e, err := idx.getOLH(ctx, head)
	if err != nil {
		return fmt.Errorf("repairing the olh of %s/%s: reading its index entry: %w", head.Bucket.Info.Bucket.Name, head.Key.Name, err)
	}
	if e.Tag == rgwBlStr(head.Attrs[meta.AttrOLHIDTag]) {
		return nil
	}
	w := radosclient.NewWriteOp()
	guardOLHTag(w, head)
	w.SetMtime(head.Mtime)
	w.SetXattr(meta.AttrOLHIDTag, []byte(e.Tag))
	info := meta.OLHInfo{Target: meta.Obj{Bucket: head.Bucket.Info.Bucket, Key: indexToObjKey(e.Key)}, Removed: e.DeleteMarker}
	w.SetXattr(meta.AttrOLHInfo, encodeAt(info, s.release))
	return s.writeOLHHead(ctx, head, w)
}

// guardOLHTag is bucket_index_guard_olh_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8178-L8182 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8924-L8928 at v20.2.4):
// the head's olh.idtag must still be what head was read with, absent read as
// empty.
func guardOLHTag(w *radosclient.WriteOp, head *op.ObjectState) {
	w.CmpXattr(meta.AttrOLHIDTag, radosclient.CmpEQ, head.Attrs[meta.AttrOLHIDTag])
}

// writeOLHHead runs w on head's object.
func (s *Store) writeOLHHead(ctx context.Context, head *op.ObjectState, w *radosclient.WriteOp) error {
	ref, err := s.headRef(ctx, head.Bucket, head.Key)
	if err != nil {
		return err
	}
	if _, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone); err != nil {
		return fmt.Errorf("writing the olh head of %s/%s: %w", head.Bucket.Info.Bucket.Name, head.Key.Name, err)
	}
	return nil
}

// decodeOLHInfo is decode_olh_info
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8397-L8407 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9143-L9153 at v20.2.4),
// whose failure is radosgw's -EIO.
func decodeOLHInfo(head *op.ObjectState, b []byte) (meta.OLHInfo, error) {
	info, err := decodeAttr(b, meta.DecodeOLHInfo)
	if err != nil {
		return meta.OLHInfo{}, fmt.Errorf("%w: decoding the olh info of %s/%s: %w", op.ErrUnknown, head.Bucket.Info.Bucket.Name, head.Key.Name, err)
	}
	return info, nil
}

// indexToObjKey is rgw_obj_key's constructor from an index key, which an
// rgw_obj built from one runs: the name through parse_index_key, the
// instance as it is.
func indexToObjKey(k rgw.ObjKey) meta.ObjKey {
	name, ns := parseIndexKey(k.Name)
	return meta.ObjKey{Name: name, Instance: k.Instance, NS: ns}
}
