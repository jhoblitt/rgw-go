package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// deleteVersioned is delete_obj's versioned branch
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5850-L5968 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6462-L6582 at v20.2.4),
// chosen on key's instance as the request named it, before "null" is cleared,
// as radosgw tests the original key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5853 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6465 at v20.2.4):
// a key without an instance gets a delete marker, and one with an instance,
// "null" included, loses that version and never gets a marker.
func (s *Store) deleteVersioned(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	if key.Instance == "" {
		return s.writeDeleteMarker(ctx, rec, key, p)
	}
	return s.removeVersion(ctx, rec, key, p)
}

// writeDeleteMarker is the versioned branch for a key named without an
// instance
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5853-L5904 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6465-L6516 at v20.2.4):
// setOLH links a delete marker under a fresh instance, or the null instance
// on a suspended bucket, whose index entry names p's marker owner and the
// delete time, p.Mtime or now. A marker on a marker is linked as any other.
// x-amz-delete-if-unmodified-since goes to the link, whose class compares it
// with the entry it reads for the marker's instance, in read_key_entry's
// order
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1682-L1701 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1823-L1842 at v20.2.4):
// a fresh instance has none, and the null instance's is the null delete
// marker's or, behind a version marker, the null data's, or else an
// unconverted plain entry. So on a suspended bucket it meets the null
// version, its data or its marker, and the link writes nothing over one no
// older than it.
//
// The other conditions are the link's condition (deleteMarkerCondition), so
// each attempt evaluates them on the version current once it has followed
// the OLH, and on an enabled bucket the marker commits only if they held when
// it linked: the check after the link requires the version they passed on,
// or the current delete marker and, when they passed on the null version's
// data in the OLH head, the null version, still listed, and no other version
// or marker linked in between (checkOLHLink). A null marker is not checked
// after its link, which replaces the null version's list entry and logs its
// data's removal, and no unlink restores either: it gets the init's guard,
// which holds it against the writers of the null version's data that init
// before their link, and the replay's guard on the data (prepareOLHAttempt,
// olhLink.ExpectHead). With no version current, a delete marker or a head no
// link has reached, the conditions are checked against the key's head as
// read without following it, the OLH head object, as radosgw's are
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5882-L5897 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6509 at v20.2.4;
// docs/differences.md, "A conditional delete marker is checked against the
// version current when it links").
//
// On a suspended bucket p.Expect is the null version the null marker
// replaces, whose data is the OLH head's: its write tag, or for a null
// version that holds no data the head's as nullMarkerExpect reads it, goes
// to the link as its ExpectTag, which each attempt compares on the head it
// reads and its init compares again, and which guards the replay's removal
// of that data, the link logging it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1731 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1810 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1872 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1951 at v20.2.4).
// The replay runs whatever p.SkipOLHUpdate says, as the guard is in it. A
// removal it refuses is answered as refusedRemoval says, and one it dropped
// or another replay kept as keptUnderLock says. A marker with
// conditions and no Expect takes the write tag of the head each attempt
// follows instead (olhLink.ExpectHead), so its own replay removes only the
// null data its condition was evaluated on, or none. On an enabled
// bucket the marker removes nothing, and p.Expect is the version its caller
// read as current, which the marker's condition requires current when the
// marker links (deleteMarkerCondition).
func (s *Store) writeDeleteMarker(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	marker := key
	res := &op.DeleteObjectResult{VersionID: meta.NullInstance, DeleteMarker: true}
	suspended := rec.Info.Flags&meta.BucketVersionsSuspended != 0
	var current *op.ObjectState
	if !suspended {
		marker.Instance = meta.NewInstance()
		res.VersionID = marker.Instance
		current = p.Expect
	}
	mtime := p.Mtime
	if mtime.IsZero() {
		mtime = s.now()
	}
	link := olhLink{
		DeleteMarker: true,
		Meta:         rgw.DirEntryMeta{Owner: p.MarkerOwner.String(), OwnerDisplayName: p.MarkerOwnerDisplayName, Mtime: mtime},
		UnmodSince:   p.UnmodifiedSince,
		SkipUpdate:   p.SkipOLHUpdate,
		Cond:         s.deleteMarkerCondition(p, current),
	}
	switch {
	case suspended && p.Expect != nil:
		tag, err := s.nullMarkerExpect(ctx, rec, key, p.Expect)
		if err != nil {
			return nil, err
		}
		link.ExpectTag, link.SkipUpdate = tag, false
	case suspended && link.Cond != nil:
		link.ExpectHead, link.SkipUpdate = true, false
	}
	err := s.setOLH(ctx, rec, marker, link)
	switch {
	case err == nil && suspended && p.Expect != nil:
		if kerr := s.keptUnderLock(ctx, rec, olhHeadKey(key)); kerr != nil {
			return nil, fmt.Errorf("deleting %s: %w", versionName(rec, key), kerr)
		}
		return res, nil
	case err == nil:
		return res, nil
	case (link.ExpectTag != nil || link.ExpectHead) && finalRefusal(err):
		return nil, s.refusedRemoval(ctx, rec, key, err, s.relistNull)
	}
	return nil, err
}

// nullMarkerExpect is the write tag a suspended bucket's null delete marker
// expects of key's head, e being the null version as its caller read it:
// e's own when e holds data, which is the OLH head's. A null version that
// holds no data, one that does not exist or a delete marker, is read off an
// OLH head without a manifest (headState), whose write tag, which the
// head's first OLH init gave it, e does not carry. The head is read again
// here: one that has gained null data since, a manifest or no OLH, is
// ConcurrentModification with nothing written, and one that has not is
// expected as read, so null data written after this read refuses the link.
func (s *Store) nullMarkerExpect(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, e *op.ObjectState) ([]byte, error) {
	if e.Exists {
		return expectTag(e), nil
	}
	head, err := s.readOLHHead(ctx, rec, key)
	if err != nil {
		return nil, fmt.Errorf("reading the head of %s: %w", versionName(rec, key), err)
	}
	if _, olh := head.Attrs[meta.AttrOLHVer]; head.Exists && (!olh || head.Manifest != nil) {
		return nil, fmt.Errorf("%w: %s holds null data written since its null version was read", op.ErrConcurrentModification, versionName(rec, key))
	}
	return expectTag(head), nil
}

// nullAsEmpty is instance with null, the null version's name in a request,
// read as the empty instance the null version's state carries.
func nullAsEmpty(instance string) string {
	if instance == meta.NullInstance {
		return ""
	}
	return instance
}

// expectTag is the write tag DeleteParams.Expect guards a removal on, the
// user.rgw.idtag e holds, absent read as empty, as string-mode cmpxattr EQ
// matches an absent xattr to the empty string.
func expectTag(e *op.ObjectState) []byte {
	return append([]byte{}, e.Attrs[meta.AttrIDTag]...)
}

// refusedRemoval answers a versioned delete whose own replay refused the
// removal of key's version for good, refusal being that answer: the class
// has taken the version's list entry and left its instance entry and head
// (errRemovalRefused), so relist lists the version again (runRelist), and
// the delete is ConcurrentModification whatever relist does. A relist that
// finds the version gone, its record or its instance entry removed since by
// another replay, has nothing to list; one that fails otherwise, or that
// other writers kept refusing until one of its bounds (relistBackoff), leaves
// the version unlisted and is logged, and the record the replay wrote of it
// before its trim has a lifecycle pass or a bucket delete list it again
// (relistUnlisted).
func (s *Store) refusedRemoval(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, refusal error,
	relist func(context.Context, *relistClock, *op.BucketRecord, meta.ObjKey) error,
) error {
	what := versionName(rec, key)
	v := key
	if v.Instance == meta.NullInstance {
		v.Instance = ""
	}
	err := s.runRelist(ctx, rec, v, relist)
	switch {
	case err == nil:
	case errors.Is(err, radosclient.ErrNotFound), errors.Is(err, op.ErrNoSuchKey):
		slog.DebugContext(ctx, "a version whose removal its write tag refused was removed since; nothing to list again",
			slog.String("version", what), slog.Any("error", err))
	default:
		slog.WarnContext(ctx, "listing again a version whose removal its write tag refused failed; it stays unlisted until a lifecycle pass or a bucket delete lists it",
			slog.String("version", what), slog.Any("error", err))
	}
	return fmt.Errorf("%w: deleting %s: %w", op.ErrConcurrentModification, what, refusal)
}

// runRelist runs relist on v, a version of rec's bucket left unlisted, the
// null version's instance empty, and returns its error, the cause of a bound
// that ended it wrapped in. The relist runs on ctx detached from ctx's
// cancellation and deadline: when it ends is the server's to choose. A
// client's disconnect, which cancels ctx, does not end it, and neither does
// a time its caller stops at, such as the stop time of a lifecycle pass,
// which the pass reads at each page alone (lcBucket.atPage): a relist that
// ended there would leave the version unlisted at its first lost link.
//
// Until a try is lost the relist is bounded by relistBackstop alone: with no
// other writer refusing its link, the version is listed however long the
// cluster, a reshard or the gateway's in-flight budget makes its calls wait,
// short of a stall. Its retries have two bounds more, the try count and
// relistCap from the first lost try (relistClock), whichever comes first.
// The cap and the backstop end the relist's waits and fail each RADOS call
// begun past them. A call in flight ends with them only where the seam's
// client returns at the context's end: goceph's asynchronous completion
// modes do, leaving the operation to librados, so a write so abandoned may
// still land; its synchronous mode returns once the operation has. Both
// writes of a link's pair, the init and the link, and the second write of
// the reset's, are sent whatever the cap does, under the backstop
// (pairContext). The backstop bounds the relist up to its link: once the
// link has landed, its replay runs to its end on a context of its own, as
// every write's does (finishOLHModification).
func (s *Store) runRelist(ctx context.Context, rec *op.BucketRecord, v meta.ObjKey,
	relist func(context.Context, *relistClock, *op.BucketRecord, meta.ObjKey) error,
) error {
	limit := s.relistTimeout
	if limit <= 0 {
		limit = relistCap
	}
	backstop := s.relistBackstopTimeout
	if backstop <= 0 {
		backstop = relistBackstop
	}
	bctx, stop := context.WithTimeoutCause(context.WithoutCancel(ctx), backstop, errRelistBackstop)
	defer stop()
	rctx, end := context.WithCancelCause(bctx)
	clock := &relistClock{limit: limit, end: end}
	defer clock.stop()
	err := relist(rctx, clock, rec, v)
	if cause := context.Cause(rctx); err != nil && cause != nil {
		err = fmt.Errorf("%w: %w", cause, err)
	}
	return err
}

// deleteMarkerCondition is a delete marker's condition, or nil when p sets
// none and current is nil: check_preconditions on the version current. Where
// follow_olh finds none, a delete marker current or a head without olh.info,
// it is checked on the key's head read again without following it, as
// radosgw falls back to the head it read first; a current version whose head
// is gone is checked as missing, as radosgw checks the state the follow
// resolved. Each of the three compares the attributes of what it is checked
// on, so a pass rests on it: on the head, on the null version's data there
// (headNullData). current, when not nil, is the version the caller read as
// current, which must be the one current, by its instance, the null one for
// an empty or null instance, and its user.rgw.idtag (expectTag): another
// version, a delete marker, none, or an attr write since is
// ConcurrentModification. Its pass rests on that version being current,
// which the link's check holds to the link (checkOLHLink).
func (s *Store) deleteMarkerCondition(p op.DeleteParams, current *op.ObjectState) olhCondition {
	if p.IfMatch == "" && p.IfMatchSize == nil && p.IfMatchLastModified.IsZero() && current == nil {
		return nil
	}
	return func(ctx context.Context, cur *op.ObjectState) (olhPass, error) {
		st := cur
		onHead := cur.DeleteMarker || (!cur.Exists && cur.Key.Instance == "")
		if current != nil && (onHead || !cur.Exists || nullAsEmpty(cur.Key.Instance) != nullAsEmpty(current.Key.Instance) ||
			!bytes.Equal(expectTag(cur), expectTag(current))) {
			return olhPass{}, fmt.Errorf("%w: the version current is not the one expected", op.ErrConcurrentModification)
		}
		if onHead {
			head, err := s.readOLHHead(ctx, cur.Bucket, olhHeadKey(cur.Key))
			if err != nil {
				return olhPass{}, err
			}
			st = head
		}
		if err := s.checkDeletePreconditions(st, p); err != nil {
			return olhPass{}, err
		}
		return olhPass{Current: true, Null: onHead && headNullData(st, p)}, nil
	}
}

// headNullData reports whether a pass of p's conditions on head, a key's
// OLH head, rests on the null version's data there: whether head holds it,
// as its ETag shows, since the null version's removal leaves the head its
// OLH attrs alone (removeHeadFlagged), and p compares it, with an ETag, a
// size or a last-modified time. If-Match: * alone passes on the head
// existing, which the removal leaves.
func headNullData(head *op.ObjectState, p op.DeleteParams) bool {
	if !holdsNullData(head) {
		return false
	}
	return comparesData(p)
}

// removeVersion is the versioned branch for a key that names a version
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5905-L5953 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6517-L6565 at v20.2.4):
// the version's index entry, NoSuchKey when it has none, the conditions on
// that entry's meta (versionConditions), then unlinkObjInstance, whose replay
// removes a version's head, the class a marker's entries. "null" names the
// empty instance for the read and the unlink alone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5844-L5846 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6456-L6458 at v20.2.4),
// and the result keeps it. x-amz-delete-if-unmodified-since is not read, as
// radosgw reads it on no removal of a named version
// (docs/ceph-upstream-bugs.md, "radosgw's versioned delete of a named
// version ignores unmod_since, which its lifecycle relies on").
//
// The entry is resolved in the class's read_key_entry order
// (getInstanceEntry), so for "null" on a converted key the conditions and the
// result's DeleteMarker are the null version's, its data or its delete
// marker. radosgw's bi_get_instance reads the plain key alone, there the
// version marker, whose meta is empty (docs/differences.md, "A versioned
// delete of versionId=null checks the null version itself").
//
// p.Expect is the version as its caller read it (expectRemoval), checked
// before the unlink, a mismatch being ConcurrentModification with nothing
// written; the unlink's replay then removes the head only under the write
// tag it carries, running whatever p.SkipOLHUpdate says. Without p.Expect,
// conditions on the null version's data must find the key's OLH head showing
// what they judged (nullDataJudged), its write tag then guarding the removal
// as Expect's does. A removal the replay refuses is answered as
// refusedRemoval says, and one given p.Expect that the replay dropped or
// another replay kept as keptUnderLock says.
//
// An entry that is a delete marker makes the unlink a marker's whatever
// p.Expect says (olhUnlink.Marker): the class takes a marker's entries itself
// and logs no removal
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1979 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2126 at v20.2.4),
// so no check of a removal follows its replay, which would otherwise take
// the null data below a null marker for a lock its writer never checked
// (checkRemoval). For the null delete marker the unlink's op compares the
// marker's instance entry with the raw entry this read returned
// (olhMarkerCmp), so a second null marker or a null write that replaced it
// refuses the unlink, where the class would unlink what replaced it in the
// marker's place while the result named the marker read. The null version's
// data, read with nothing holding the unlink to it, neither p.Expect nor a
// condition on its data, has the unlink's op compare the null delete
// marker's entry as absent (olhUnlink.NoMarker): a marker linked over the
// data since the read, which the class's unlink would take in its place
// (read_key_entry order), refuses the unlink. Either refusal of a delete
// without p.Expect starts it again from a fresh read, which finds what is
// then the null version and takes it, up to olhAttempts times; one given
// p.Expect, which a replaced version no longer matches, answers the refusal,
// ConcurrentModification.
func (s *Store) removeVersion(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	for tries := 1; ; tries++ {
		res, err := s.removeVersionOnce(ctx, rec, key, p)
		restart := errors.Is(err, errMarkerLinked) || p.Expect == nil && errors.Is(err, errMarkerGone)
		if !restart || tries >= olhAttempts {
			return res, err
		}
	}
}

// removeVersionOnce is one try of removeVersion, from its read of the
// version's index entry.
func (s *Store) removeVersionOnce(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	v := key
	null := key.Instance == meta.NullInstance
	if null {
		v.Instance = ""
	}
	en, bi, err := s.getInstanceEntryBI(ctx, rec, v)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return nil, fmt.Errorf("%s: %w", versionName(rec, key), op.ErrNoSuchKey)
	case err != nil:
		return nil, fmt.Errorf("reading the index entry of %s: %w", versionName(rec, key), err)
	}
	if cerr := versionConditions(en.Meta, p); cerr != nil {
		return nil, fmt.Errorf("deleting %s: %w", versionName(rec, key), cerr)
	}
	u := olhUnlink{NullVersion: null, SkipUpdate: p.SkipOLHUpdate, Marker: en.Flags&rgw.FlagDeleteMarker != 0}
	if u.Marker && v.Instance == "" {
		u.MarkerCmp = &olhMarkerCmp{bi: bi, en: en}
	}
	if p.Expect != nil {
		tag, eerr := s.expectRemoval(ctx, rec, v, en, p.Expect)
		if eerr != nil {
			return nil, fmt.Errorf("deleting %s: %w", versionName(rec, key), eerr)
		}
		u.ExpectTag, u.SkipUpdate = tag, false
	} else if null && !u.Marker && comparesData(p) {
		tag, jerr := s.nullDataJudged(ctx, rec, v, en, p)
		if jerr != nil {
			return nil, fmt.Errorf("deleting %s: %w", versionName(rec, key), jerr)
		}
		u.ExpectTag, u.SkipUpdate = tag, false
	}
	u.NoMarker = null && !u.Marker && u.ExpectTag == nil
	_, olhTag, err := s.unlinkVersion(ctx, rec, v, u)
	switch {
	case err == nil && p.Expect != nil && !u.Marker:
		if kerr := s.keptUnderLock(ctx, rec, v); kerr != nil {
			return nil, fmt.Errorf("deleting %s: %w", versionName(rec, key), kerr)
		}
		return &op.DeleteObjectResult{VersionID: key.Instance, DeleteMarker: en.Flags&rgw.FlagDeleteMarker != 0}, nil
	case err == nil:
		return &op.DeleteObjectResult{VersionID: key.Instance, DeleteMarker: en.Flags&rgw.FlagDeleteMarker != 0}, nil
	case u.ExpectTag != nil && finalRefusal(err):
		relist := func(ctx context.Context, clock *relistClock, rec *op.BucketRecord, v meta.ObjKey) error {
			return s.relistVersion(ctx, clock, rec, v, olhTag)
		}
		if null {
			relist = s.relistNull
		}
		return nil, s.refusedRemoval(ctx, rec, key, err, relist)
	}
	return nil, err
}

// expectRemoval checks e, a DeleteParams.Expect, against v's version before
// its unlink, en being the version's index entry, and returns the write tag
// the unlink's replay guards the version's removal on.
//   - A delete marker has no head, and every marker's entry carries the same
//     tag, so e must be a marker with the entry's versioned_epoch and mtime,
//     which StatObject's state of a marker read by its version carries
//     (namedVersion). A marker owns no data, so its unlink's replay guards a
//     removal on markerRemovalTag, which no head carries. The class takes a
//     marker's entries itself and logs no removal
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1979 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2126 at v20.2.4).
//     The null marker's unlink also compares the marker's instance entry as
//     read (olhMarkerCmp), so a second null marker or a null write that
//     replaced it before the unlink refuses it; were the class to unlink a
//     null write's data in the marker's place, read in read_key_entry's
//     order, the replay would refuse that removal.
//   - An e that found no version takes a marker too, as a marker holds no
//     lock: StatObject answers one whose list entry an older-epoch unlink
//     took as no version (namedVersion), and radosgw's unlink removes its
//     instance entry
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5905-L5953 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6517-L6565 at v20.2.4).
//   - Any other version must be one e names as data, and its head, the key's
//     OLH head for the null version, must carry e's write tag (expectTag),
//     which then guards the removal.
func (s *Store) expectRemoval(ctx context.Context, rec *op.BucketRecord, v meta.ObjKey, en rgw.DirEntry, e *op.ObjectState) ([]byte, error) {
	marker := en.Flags&rgw.FlagDeleteMarker != 0
	switch {
	case marker && !e.Exists && !e.DeleteMarker:
		return markerRemovalTag, nil
	case marker != e.DeleteMarker:
		return nil, fmt.Errorf("%w: the version is not the one expected, one a delete marker and the other not", op.ErrConcurrentModification)
	case marker && (en.VersionedEpoch != e.VersionedEpoch || !en.Meta.Mtime.Equal(e.Mtime)):
		return nil, fmt.Errorf("%w: the delete marker is not the one expected", op.ErrConcurrentModification)
	case marker:
		return markerRemovalTag, nil
	}
	head, err := s.readOLHHead(ctx, rec, v)
	if err != nil {
		return nil, fmt.Errorf("reading its head: %w", err)
	}
	tag := expectTag(e)
	if !bytes.Equal(head.Attrs[meta.AttrIDTag], tag) {
		return nil, fmt.Errorf("%w: its head's write tag is not the one expected", op.ErrConcurrentModification)
	}
	return tag, nil
}

// keptUnderLock answers a delete given DeleteParams.Expect whose unlink, or
// on a suspended bucket whose null delete marker, and replay succeeded while
// v, the version it removes, the OLH head's null data for the null version,
// is still stored: a link of v logged after the delete's removal made the
// replay drop that removal (applyOLHLog), or another replay kept v for its
// lock and a relist listed it again, the record that names that removal
// gone by the delete's check (checkRemoval). For the null version the data
// stored then may be a newer null write's, which a null link put in the head
// after the delete's read. Such a delete removed nothing it expected, and
// while that data carries a retention or a legal hold,
// or a lock attr that does not decode, success would report a locked version
// deleted: it answers ConcurrentModification, on which deleteUnderLock reads
// the version and checks its lock again, answering AccessDenied under a lock
// its bypass does not pass. A version kept with no lock leaves the success as
// it is (docs/differences.md, "A replay keeps a named version that a later
// link in the same log names").
func (s *Store) keptUnderLock(ctx context.Context, rec *op.BucketRecord, v meta.ObjKey) error {
	st, err := s.readOLHHead(ctx, rec, v)
	if err != nil {
		return fmt.Errorf("reading its head after the removal: %w", err)
	}
	stored := st.Exists
	if v.Instance == "" {
		stored = holdsNullData(st)
	}
	if !stored || objectlock.VerifyDelete(st.Attrs, s.now(), false, false) == nil {
		return nil
	}
	return fmt.Errorf("%w: the version was kept, and an object lock holds it", op.ErrConcurrentModification)
}

// comparesData reports whether p's conditions compare a version's data: an
// If-Match with an ETag, x-amz-if-match-size or
// x-amz-if-match-last-modified-time.
func comparesData(p op.DeleteParams) bool {
	return p.IfMatch != "" && p.IfMatch != "*" || p.IfMatchSize != nil || !p.IfMatchLastModified.IsZero()
}

// nullDataJudged checks, for a delete of v, the null version of rec's
// bucket, whose conditions p passed on en, its index entry, that the key's
// OLH head shows what they judged (headShows), and returns the head's write
// tag, which the unlink then compares on the head and the replay's removal is
// guarded on (olhUnlink.ExpectTag). A null write's head write lands on the
// OLH head before its index completion, so in between the entry is the
// previous null version's while the head holds the write's data, which the
// removal would take on the strength of the previous one's conditions; the
// class then cancels the write's completion as older than the removal's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1082-L1109 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1217-L1244 at v20.2.4).
// A head that shows other data is ConcurrentModification with nothing
// written, as an Expect the head no longer matches is (expectRemoval).
func (s *Store) nullDataJudged(ctx context.Context, rec *op.BucketRecord, v meta.ObjKey, en rgw.DirEntry, p op.DeleteParams) ([]byte, error) {
	head, err := s.readOLHHead(ctx, rec, v)
	if err != nil {
		return nil, fmt.Errorf("reading its head: %w", err)
	}
	_, pending := s.checkPendingOLHEntries(ctx, head)
	if !head.Exists || !headShows(head, en.Meta, p, len(pending) > 0) {
		return nil, fmt.Errorf("%w: the key's head does not show the null version its conditions were checked on", op.ErrConcurrentModification)
	}
	return expectTag(head), nil
}

// headShows reports whether head, a key's OLH head, shows what p's conditions
// judged of m, the meta of the null version's index entry: its ETag, its size
// where p carries x-amz-if-match-size, and, while a write is in flight over
// it (inFlight: the head carries a pending attr no timeout has expired), its
// last-modified time in whole seconds where p carries
// x-amz-if-match-last-modified-time. A null write over an OLH head sends one
// mtime to its head write and to its index completion
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3227-L3228 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3352-L3354 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3324-L3325 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3473-L3474 at v20.2.4)
// and carries its init in the head write (nullInit), so a write of the same
// bytes that has landed its head and not its index completion has the
// entry's ETag and size and only its mtime tells it from the version checked.
// The mtime is compared no other time: the head's mtime moves without a write
// of data, as radosgw's removal of a pending attr stamps it with the client's
// clock, while the entry's, which the delete's conditions and radosgw's
// listing judge, stays
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7754-L7797 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8936-L8967 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8417-L8460 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9682-L9713 at v20.2.4;
// docs/ceph-upstream-bugs.md, "radosgw's removal of an OLH head's pending attr
// moves the null version's mtime"), so a settled head is judged by its ETag
// and size alone (docs/differences.md, "A versioned write whose version a
// delete removes before its link succeeds").
func headShows(head *op.ObjectState, m rgw.DirEntryMeta, p op.DeleteParams, inFlight bool) bool {
	switch {
	case rgwBlStr(head.Attrs[meta.AttrETag]) != m.ETag:
		return false
	case p.IfMatchSize != nil && head.Size != m.Size:
		return false
	case inFlight && !p.IfMatchLastModified.IsZero() && !head.Mtime.Truncate(time.Second).Equal(m.Mtime.Truncate(time.Second)):
		return false
	}
	return true
}

// markerRemovalTag is the write tag a delete marker's unlink guards its
// replay's removals on: no head carries it, rgw-go's and radosgw's write tags
// being a request id or random characters, so the replay removes no data on
// a marker's account.
var markerRemovalTag = []byte("rgw-go:delete-marker:owns-no-data")

// versionConditions is delete_obj's check of a named version's index entry
// meta m, in its order
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5913-L5942 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6525-L6554 at v20.2.4):
// If-Match, unless "*", unquoted and equal to m's ETag, whole where
// check_preconditions takes the ETag for a prefix; x-amz-if-match-size; then
// x-amz-if-match-last-modified-time in whole seconds. An If-Match present with
// an empty value is the empty string radosgw compares, which an entry without
// an ETag, a delete marker's, matches.
func versionConditions(m rgw.DirEntryMeta, p op.DeleteParams) error {
	if p.IfMatch != "" && p.IfMatch != "*" {
		want := op.Unquote(p.IfMatch)
		if p.IfMatch == op.EmptyCondition {
			want = ""
		}
		if m.ETag != want {
			return op.ErrPreconditionFailed
		}
	}
	if p.IfMatchSize != nil && *p.IfMatchSize != m.Size {
		return op.ErrPreconditionFailed
	}
	if !p.IfMatchLastModified.IsZero() && !m.Mtime.Truncate(time.Second).Equal(p.IfMatchLastModified.Truncate(time.Second)) {
		return op.ErrPreconditionFailed
	}
	return nil
}

// relistRounds bounds relistVersion's rounds at epoch 0, current again: each
// reads the key afresh, and one whose link another writer's change refused
// gives way to the next, until relistBelow lists the version behind the
// newest. A round's wait on pending modifications is not one.
const relistRounds = 5

// errNullOwned ends a relist of the null version's data whose record a newer
// null write has taken over (relistNull).
var errNullOwned = errors.New("a newer null write owns the null version")

// relistCap is the wall-clock cap on one relist's retries (relistClock):
// twice olhAttempts backoffs of olhCondBackoffMax, 32 s. The try bound ends a
// relist that other writers keep refusing, which spends under half the cap in
// its backoffs. The cap ends one whose retries' reads and writes are slow as
// well, which the try bound does not: a try's RADOS operations have no bound
// of their own, and a lifecycle pass sends a page of removals, each of which
// may relist.
const relistCap = 2 * olhAttempts * olhCondBackoffMax

// relistBackstop is the bound on a relist from its start up to its link
// (refusedRemoval), sixteen times relistCap, 512 s. It is for a call that
// never returns, which nothing else ends once the relist is detached from
// its request: librados's rados_osd_op_timeout is 0, no timeout, by default,
// and the wait for the gateway's in-flight budget has none either. One index
// write that meets a reshard can wait 450 s: its guard sends the call up to
// reshardRetries times (guardReshard), and after each refusal reads the
// shard's status up to reshardRetries times reshardWait apart
// (blockWhileResharding), 45 s. With relistCap spent on its retries a
// healthy relist takes up to about 482 s, so the backstop ends only a relist
// whose calls have stalled.
const relistBackstop = 16 * relistCap

// errRelistBackstop is the cause a relist's context ends with at
// relistBackstop.
var errRelistBackstop = errors.New("the backstop on a relist passed")

// pairContext is the context of a pair of writes a relist sends, ctx being
// the relist's: free of the cap on its retries, so that the pair completes
// or is undone once its first write has been sent, and held to the
// backstop's deadline, so that a call that stalls there still ends.
func pairContext(ctx context.Context) (context.Context, context.CancelFunc) {
	free := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(free, deadline)
	}
	return free, func() {}
}

// errRelistEnded ends a relist that other writers kept refusing until one of
// its bounds (relistBackoff).
var errRelistEnded = errors.New("other writers kept refusing the relist's link until its bound")

// errRelistCap is the cause a relist's context ends with at the cap on its
// retries (relistClock).
var errRelistCap = errors.New("the cap on a relist's retries passed")

// relistClock is the cap on one relist's retries (refusedRemoval): start,
// which each lost try calls (relistBackoff), starts the clock at the first,
// and limit later the relist's context ends, with errRelistCap as its cause.
// A context's deadline is fixed when the context is made, so the clock ends
// this one through end, its cancel, and the context's own error is then
// context.Canceled. It serves the one goroutine its relist runs in.
type relistClock struct {
	limit time.Duration
	end   context.CancelCauseFunc
	timer *time.Timer
}

func (c *relistClock) start() {
	if c.timer == nil {
		c.timer = time.AfterFunc(c.limit, func() { c.end(errRelistCap) })
	}
}

// stop releases the clock and its context once the relist has returned.
func (c *relistClock) stop() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.end(nil)
}

// relistBackoff waits before a relist tries again after another writer's
// change refused its link or moved the key under its reads, n being the tries
// lost so far: a conditional link's backoff (olhCondBackoff), which doubles up
// to olhCondBackoffMax and stays there. A refused link is contention, not a
// reason to stop, so the relist goes on until one of its two bounds, both the
// server's, whichever comes first: olhAttempts tries, the bound radosgw's
// set_olh puts on the links other writers refuse, MAX_ECANCELED_RETRY
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8688-L8689 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9434-L9435 at v20.2.4),
// or the end of ctx, the relist's context, relistCap after the first lost
// try, which starts clock here. Past either it answers errRelistEnded.
func (s *Store) relistBackoff(ctx context.Context, clock *relistClock, n *int) error {
	*n++
	clock.start()
	switch {
	case *n >= olhAttempts:
		return fmt.Errorf("%w: every one of %d tries lost to another writer", errRelistEnded, olhAttempts)
	case ctx.Err() != nil:
		return fmt.Errorf("%w: %w", errRelistEnded, ctx.Err())
	}
	if err := s.condBackoff(ctx, olhCondBackoff(min(*n-1, olhCondBackoffs))); err != nil {
		return fmt.Errorf("%w: %w", errRelistEnded, err)
	}
	return nil
}

// relistVersion lists again v, a named version whose removal its write tag
// refused once the unlink had taken its list entry, olhTag being the OLH tag
// that unlink sent, and does nothing when its instance entry or its head is
// gone, another replay having removed it, or when its instance entry's list
// entry is there, another relist, the writer's or a reader's, having listed
// it (relistUnlisted). It works in rounds, each reading
// the version, the OLH head, its expired pending attrs removed first
// (readRelinkHead), and the listing afresh. When no listed version of
// the name is newer than v, the round links at epoch 0, current again at a
// fresh epoch over the version the unlink promoted, guarded on the OLH head
// as the round read it (olhRelink); a round whose link the guard refuses, a
// writer's modification having moved the head since, gives way to the next.
// While another writer's modification is pending the round waits, as a
// conditional writer does (waitOLHPending), and once the wait has passed
// those pending attrs no longer refuse the link: a pending attr no writer
// will remove, such as the one radosgw's link at an old epoch leaves, does
// not hold the version off, before its expiry or after. v goes to
// relistBelow, at its own epoch, when a newer version is listed, which lists
// it behind that version, as long as that version is still listed then,
// when the name's OLH entry was made again since (remade), or when every one
// of relistRounds rounds lost its link to another writer, each lost round
// followed by a backoff (relistBackoff).
//
// Before its link at epoch 0 a round looks for a newer version held out of
// the listing (heldNewer) and lists it again first, as a reader of its record
// does (listHeld): linked at epoch 0, v would list ahead of that version at
// the fresh epoch, and a later relist of it would put it behind v. The next
// round then finds it listed and goes below it. A held version is listed
// once, and not by a relist running inside that version's own relist
// (listingHeld): one not listed is taken as absent, and the round links as
// if it were not there. A version another writer's replay kept for its lock
// is no held one (heldNewer). While a newer version unlisted and stored has its removal
// still logged, its replay not yet having refused it, carried it out or
// trimmed it, the round waits for that replay within the wait on pending
// modifications (waitUnsettled), and past that wait links as if the version
// were not there, the link's own replay then applying the removal.
//
// Before a round's link it gives way to another delete of v whose removal is
// pending, linking nothing, reads the listing again after the name's OLH
// entry, and compares that entry in its link (relinkOrMove), so that a delete
// of v that unlinks it during the relist is not undone, and a writer's link
// that lands during the round leaves v below that writer's version. ctx is
// the relist's context and clock the cap on its retries (refusedRemoval).
func (s *Store) relistVersion(ctx context.Context, clock *relistClock, rec *op.BucketRecord, v meta.ObjKey, olhTag []byte) error {
	idx := s.newOLHIndex(rec, v)
	var w olhCondWait
	tries := 0
	tried := map[string]bool{}
	for round := 0; round < relistRounds; {
		en, err := s.getInstanceEntry(ctx, rec, v)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return nil
		case err != nil:
			return err
		}
		st, err := s.readOLHHead(ctx, rec, v)
		if err != nil || !st.Exists {
			return err
		}
		if listed, lerr := s.listed(ctx, rec, v, en); lerr != nil || listed {
			return lerr
		}
		head, err := s.readRelinkHead(ctx, rec, v)
		if err != nil {
			return err
		}
		remade, err := idx.remade(ctx, rec, olhTag)
		if err != nil {
			return err
		}
		newest, next, err := idx.newestListed(ctx)
		if err != nil {
			return err
		}
		var above *rgw.DirEntry
		if !remade && newest != nil && !listsBefore(en, *newest) {
			above = newest
		}
		below := remade || above != nil
		var refuse bool
		if !below {
			var wait, unsettled bool
			wait, refuse, err = s.waitOLHPending(ctx, idx, head, &w)
			switch {
			case err != nil:
				return err
			case wait:
				continue
			}
			var held *heldVersion
			held, unsettled, err = s.heldNewer(ctx, idx, rec, en)
			if err != nil {
				return err
			}
			switch {
			case held != nil && !tried[held.en.Key.Instance] && !listingHeld(ctx, held.en.Key.Instance):
				tried[held.en.Key.Instance] = true
				s.listHeld(ctx, rec, v.Instance, held)
				continue
			case unsettled:
				again, werr := s.waitUnsettled(ctx, &w)
				if werr != nil {
					return werr
				}
				if again {
					continue
				}
			}
		}
		if below {
			err = s.relistBelow(ctx, clock, rec, v, nil, 0, olhTag, above)
			if !errors.Is(err, errBelowGone) {
				return err
			}
		} else {
			round++
			err = s.relinkOrMove(ctx, idx, rec, v, olhLink{Relink: &olhRelink{head: head, pendingOK: !refuse}}, newest, next)
			switch {
			case errors.Is(err, errRelistGaveWay):
				return nil
			case !errors.Is(err, errRelinkMoved):
				return err
			}
		}
		if berr := s.relistBackoff(ctx, clock, &tries); berr != nil {
			return fmt.Errorf("listing %s again: %w", versionName(rec, v), berr)
		}
	}
	return s.relistBelow(ctx, clock, rec, v, nil, 0, olhTag, nil)
}

// relinkOLHEntry reads the OLH entry of key's name before a relist links
// key's version again, and reports removed when the entry's pending log holds
// a removal of that version: another delete's unlink of it has landed, its
// replay not yet run, and a link logged after it would make every replay
// drop the removal (applyOLHLog), undoing a delete that may have answered
// already. The relist then links nothing and that replay removes the
// version. The refused removal the relist answers was trimmed from the log
// before the relist ran (finalRefusal), so any removal of the version there
// is another writer's. Otherwise it returns the entry as read, a missing one
// as its key with no value, for the link to compare unchanged
// (olhRelink.olh), so that an unlink landing after this read refuses the link
// and the relist's next round finds its removal.
func (s *Store) relinkOLHEntry(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (olh *rgw.BIEntry, removed bool, err error) {
	idx := s.newOLHIndex(rec, key)
	e, bi, err := idx.getOLHBI(ctx, &op.ObjectState{Bucket: rec, Key: olhHeadKey(key)})
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return &rgw.BIEntry{Type: rgw.BIIndexOLH, Idx: rgw.OLHDataKey(idx.olhKey().Name)}, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("reading the olh entry of %s before listing it again: %w", versionName(rec, key), err)
	}
	for _, entries := range e.PendingLog {
		for _, le := range entries {
			if le.Op == rgw.OLHOpRemoveInstance && le.Key.Instance == key.Instance {
				return nil, true, nil
			}
		}
	}
	return &bi, false, nil
}

// relistNull lists again the null version's data, of key's name, whose
// removal its write tag refused once a link had taken its list entry: the
// null delete marker's on a suspended bucket, or the unlink of versionId=null.
// One record serves every null write, so each round first reads it, and the
// OLH head as relistVersion's rounds do (readRelinkHead), and
// does nothing when it is gone, when its versioned_epoch is no longer the
// one the first round read, a newer null write owning the key, when the OLH
// head no longer holds the data, or when the record is listed. A cleared OLH
// takes the OLH head object with the data, so the data check also ends a
// relist whose OLH was made again since. The class's link of the null data
// takes the null delete marker's list and instance entries whatever the
// epoch, before its epoch check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1760 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1901 at v20.2.4),
// and a link at an old epoch writes no OLH entry, so while that marker is
// current only a link at epoch 0 leaves a current version. The round then
// links, guarded on the OLH head as read before the listing (olhRelink):
//   - with a null delete marker current over a version newer than the null
//     data, the version the marker demoted, at epoch 0, current again, the
//     next round listing the null data below it;
//   - with a null delete marker current over nothing newer, with nothing
//     listed, or with the null data newer than every listed version, the
//     null data, at epoch 0;
//   - otherwise the null data below the newest version (relistBelow), as
//     long as that version is still listed then.
//
// Before any of these a round looks for a version newer than the null data
// held out of the listing (heldNewer) and lists it again first, as a reader
// of its record does (listHeld), once, as relistVersion's rounds do: the next
// round then finds it listed, the version the marker demoted or the newest,
// and lists the null data below it. A round waits for a newer version's
// removal still logged as relistVersion's does (waitUnsettled).
//
// A round whose link the guard or the compare refuses, or whose listing moved
// under it, is followed by another after a backoff, until a link lands or
// the relist reaches its bound (relistBackoff). While another writer's
// modification is pending a round waits, and once the wait has passed those
// pending attrs no longer refuse the link, as relistVersion's rounds do.
// Each epoch-0 link gives way to another delete's pending removal of the
// version it would link, reads the listing again after the OLH entry, and
// compares that entry (relinkOrMove): a round whose demoted version another
// delete is removing goes on to the next, which no longer lists it, and one
// whose null data another delete is removing ends the relist. ctx is the
// relist's context and clock the cap on its retries (refusedRemoval).
func (s *Store) relistNull(ctx context.Context, clock *relistClock, rec *op.BucketRecord, key meta.ObjKey) error {
	idx := s.newOLHIndex(rec, key)
	var first uint64
	var w olhCondWait
	tries := 0
	tried := map[string]bool{}
	for read := false; ; {
		dn, err := s.nullDataEntry(ctx, rec, key)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return nil
		case err != nil:
			return err
		case !read:
			first, read = dn.VersionedEpoch, true
		case dn.VersionedEpoch != first:
			return nil
		}
		head, err := s.readRelinkHead(ctx, rec, key)
		if err != nil {
			return err
		}
		if _, ok := head.Attrs[meta.AttrETag]; !ok || !head.Exists {
			return nil
		}
		if listed, lerr := s.listed(ctx, rec, key, dn); lerr != nil || listed {
			return lerr
		}
		wait, refuse, err := s.waitOLHPending(ctx, idx, head, &w)
		switch {
		case err != nil:
			return err
		case wait:
			continue
		}
		newest, next, err := idx.newestListed(ctx)
		if err != nil {
			return err
		}
		held, unsettled, err := s.heldNewer(ctx, idx, rec, dn)
		if err != nil {
			return err
		}
		switch {
		case held != nil && !tried[held.en.Key.Instance] && !listingHeld(ctx, held.en.Key.Instance):
			tried[held.en.Key.Instance] = true
			s.listHeld(ctx, rec, "", held)
			continue
		case unsettled:
			again, werr := s.waitUnsettled(ctx, &w)
			if werr != nil {
				return werr
			}
			if again {
				continue
			}
		}
		marker := newest != nil && newest.Key.Instance == "" && newest.Flags&rgw.FlagDeleteMarker != 0
		guard := &olhRelink{head: head, pendingOK: !refuse}
		target, demoted := key, marker && next != nil && listsBefore(*next, dn)
		l := olhLink{Relink: guard}
		switch {
		case demoted:
			target = indexToObjKey(next.Key)
			if next.Flags&rgw.FlagDeleteMarker != 0 {
				l.DeleteMarker, l.Meta = true, next.Meta
			}
		case marker, newest == nil, listsBefore(dn, *newest):
		default:
			err = s.relistBelow(ctx, clock, rec, key, guard, first, nil, newest)
			if err == nil || errors.Is(err, errNullOwned) {
				return nil
			}
		}
		if err == nil {
			err = s.relinkOrMove(ctx, idx, rec, target, l, newest, next)
			switch {
			case err == nil && demoted:
				continue
			case err == nil, errors.Is(err, errRelistGaveWay) && !demoted:
				return nil
			case errors.Is(err, errRelistGaveWay):
				// Another delete's removal of the version the marker demoted is
				// pending: the next round, which no longer lists it, looks past it.
				err = errRelinkMoved
			}
		}
		if !errors.Is(err, errRelinkMoved) {
			return err
		}
		if berr := s.relistBackoff(ctx, clock, &tries); berr != nil {
			return fmt.Errorf("listing %s again: %w", versionName(rec, key), berr)
		}
	}
}

// errRelistGaveWay ends a relist's link that gave way to another delete's
// pending removal of the version it would link (relinkOLHEntry).
var errRelistGaveWay = errors.New("another delete's removal of the version is pending")

// relinkOrMove is a relist round's link at epoch 0 of target, a version of
// idx's name, as l says, newest and next being the round's read of the
// listing: errRelistGaveWay when another delete's removal of target is
// pending; then the listing read again, after the OLH entry relinkOLHEntry
// read, and errRelinkMoved when it no longer lists those two as they were,
// with nothing linked; and otherwise the link, comparing that entry. Two
// kinds of change, landing between the round's listing and that entry's
// read, would pass both the give-way and the compare. The listing read after
// the entry shows them, and one landing after the entry's read moves the
// entry, which refuses the link:
//   - a delete of a delete marker, which logs no removal, the class taking
//     the marker's entries itself
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1978 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2126 at v20.2.4),
//     while the class's link of a marker writes its entries anew
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1682-L1688 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1823-L1829 at v20.2.4),
//     so the link would list the deleted marker again;
//   - a writer's link, whose version the link at epoch 0 would go current
//     over, though that writer is then answered success.
func (s *Store) relinkOrMove(ctx context.Context, idx *bucketOLHIndex, rec *op.BucketRecord, target meta.ObjKey, l olhLink,
	newest, next *rgw.DirEntry,
) error {
	olh, removed, err := s.relinkOLHEntry(ctx, rec, target)
	switch {
	case err != nil:
		return err
	case removed:
		return errRelistGaveWay
	}
	n2, x2, err := idx.newestListed(ctx)
	if err != nil {
		return err
	}
	if !sameListEntry(newest, n2) || !sameListEntry(next, x2) {
		return fmt.Errorf("linking %s: the listing changed since the round read it: %w", versionName(rec, target), errRelinkMoved)
	}
	g := *l.Relink
	g.olh, g.finish = olh, true
	l.Relink = &g
	return s.setOLH(ctx, rec, target, l)
}

// sameListEntry reports whether a and b, two reads of one place in a name's
// listing, are the same entry as listed: both absent, or the same version at
// the same epoch with the same flags.
func sameListEntry(a, b *rgw.DirEntry) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Key == b.Key && a.VersionedEpoch == b.VersionedEpoch && a.Flags == b.Flags
}

// readRelinkHead is the OLH head of key's name as a re-link round reads it:
// the pending attrs past rgw_olh_pending_timeout_sec are removed first, as
// check_pending_olh_entries removes them and as a follow does (followOLH),
// and the head read again. Such an attr is no writer's, and with it left on
// the head every round's guard would find the head moved.
func (s *Store) readRelinkHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	head, err := s.readOLHHead(ctx, rec, olhHeadKey(key))
	if err != nil || !head.Exists {
		return head, err
	}
	expired, _ := s.checkPendingOLHEntries(ctx, head)
	if len(expired) == 0 {
		return head, nil
	}
	if err := s.removeOLHPendingEntries(ctx, head, expired); err != nil {
		return nil, err
	}
	return s.readOLHHead(ctx, rec, olhHeadKey(key))
}

// relistBelow lists key's version again non-current, guarded as guard says
// when it is not nil (olhRelink), olhTag being the OLH tag the unlink sent,
// nil for the null marker's link. Its instance record, read again, is reset
// first (resetCurrent): the unlink or the null marker's link left it as it
// was, flagged current when it was current, and the class's link at an old
// epoch adds FLAG_VER and clears nothing
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1410-L1427 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1537-L1554 at v20.2.4),
// so it would list a second entry of the name flagged current. A record gone
// leaves nothing to list; for the null version the record must still be at
// epoch first, or the relist ends with errNullOwned, nothing reset. The link
// is at the version's own epoch, which the class writes back non-current
// with its list entry while it is below the OLH entry's, or, when the entry
// was made again since (remade), at epoch 1, below every version it holds.
// That link logs nothing, so a removal of the version another delete has
// logged would still remove the version and leave the list entry it wrote,
// naming nothing: the relist gives way to such a removal, and its link
// compares the OLH entry as read (relinkOLHEntry). The record is read once
// more after that entry, and a version it then shows listed, its list entry
// there, is not linked again: had another relist linked it at epoch 0, under
// an OLH entry made again with a fresh tag that this one takes for remade,
// the reset would take that link's current flag. The null version's relist
// also ends with errNullOwned on that read when the
// record's epoch has moved: a null write's link landing between the record's
// first read and the entry's is in the entry the link compares, and the reset
// and the link at the old epoch would then take that write's list entry back
// below the newer versions, with no entry of the name flagged current. One
// landing after the second read moves the entry and refuses the link, and the
// reset before that link leaves the write's record FLAG_VER alone, as any
// index completion on a record does, its list entry untouched. The reset
// compares that entry too (resetCurrent). Without guard, a reset or a link
// that compare refuses is tried again from a fresh read after a backoff,
// until the link lands, the relist gives way, or it reaches a bound
// (relistBackoff, on clock); with guard, the caller's round does that.
//
// above, when not nil, is the newer version the caller's round read listed
// and goes below, its list entry as read. It must still be listed after the
// OLH entry's read: a delete of it that landed since would leave the version
// the delete's unlink promoted, older than key's, current, with key's listed
// non-current ahead of it. One gone answers errBelowGone, nothing written, for
// the caller's round to read the listing again; one that lands after that
// read moves the OLH entry, which refuses the reset and the link.
func (s *Store) relistBelow(ctx context.Context, clock *relistClock, rec *op.BucketRecord, key meta.ObjKey, guard *olhRelink, first uint64, olhTag []byte, above *rgw.DirEntry) error {
	read := s.getInstanceEntry
	if key.Instance == "" {
		read = s.nullDataEntry
	}
	for tries := 0; ; {
		en, err := read(ctx, rec, key)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return nil
		case err != nil:
			return err
		case key.Instance == "" && en.VersionedEpoch != first:
			return errNullOwned
		}
		olh, removed, err := s.relinkOLHEntry(ctx, rec, key)
		switch {
		case err != nil:
			return err
		case removed:
			return nil
		}
		if above != nil {
			_, still, lerr := s.listEntry(ctx, rec, indexToObjKey(above.Key), *above)
			switch {
			case lerr != nil:
				return lerr
			case !still:
				return fmt.Errorf("listing %s again: %w", versionName(rec, key), errBelowGone)
			}
		}
		again, aerr := read(ctx, rec, key)
		switch {
		case errors.Is(aerr, radosclient.ErrNotFound):
			return nil
		case aerr != nil:
			return aerr
		case key.Instance == "" && again.VersionedEpoch != first:
			return errNullOwned
		}
		if listed, lerr := s.listed(ctx, rec, key, again); lerr != nil || listed {
			return lerr
		}
		err = s.resetCurrent(ctx, rec, key, en, olh)
		if err == nil {
			err = s.relinkBelow(ctx, rec, key, en, olh, guard, olhTag)
		}
		if guard != nil || !errors.Is(err, errRelinkMoved) {
			return err
		}
		if berr := s.relistBackoff(ctx, clock, &tries); berr != nil {
			return fmt.Errorf("listing %s again below the newest version: %w", versionName(rec, key), berr)
		}
	}
}

// errBelowGone ends a relistBelow whose newer version, the one it would list
// below, is no longer listed.
var errBelowGone = fmt.Errorf("%w: the newer version it would go below is no longer listed", errRelinkMoved)

// relinkBelow is relistBelow's link of key's version, en its instance record
// as read, at its own epoch, or at epoch 1 when the name's OLH entry was made
// again since (remade), comparing olh, the OLH entry relinkOLHEntry read, and
// guarded as guard says when it is not nil.
func (s *Store) relinkBelow(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, en rgw.DirEntry, olh *rgw.BIEntry, guard *olhRelink, olhTag []byte) error {
	remade, err := s.newOLHIndex(rec, key).remade(ctx, rec, olhTag)
	if err != nil {
		return err
	}
	epoch := en.VersionedEpoch
	if remade || epoch == 0 {
		epoch = 1
	}
	g := olhRelink{olh: olh, finish: true}
	if guard != nil {
		g.head, g.pendingOK = guard.head, guard.pendingOK
	}
	return s.setOLH(ctx, rec, key, olhLink{Epoch: epoch, Relink: &g})
}

// resetCurrent clears every flag but FLAG_VER on en, key's instance record
// as read: a prepare, then a cancel carrying en's own version, awaited. The
// prepare's op compares olh, the name's OLH entry as relinkOLHEntry read it,
// which the link that follows compares too: another relist's link landing
// since moves that entry, and the reset, which would take the current flag
// that link set, is refused with nothing prepared, errRelinkMoved, so the
// relist reads again and finds the version listed. The
// class's completion resets the flags whatever it completes, and its cancel
// writes the record back, or removes it when the record went since the read
// and the prepare made a placeholder in its place
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L851-L856 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1061-L1126 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L983-L988 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1196-L1260 at v20.2.4),
// where an ADD completion would write the placeholder back as an entry that
// exists. A cancel touches neither the record's meta nor the bucket's stats.
//
// The prepare's tag stays in the record's pending map until the cancel takes
// it out, and a record that holds a pending tag is written back, marked as
// not existing, where the version's removal would remove it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1141-L1166 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1275-L1299 at v20.2.4).
// So once the prepare has been sent the cancel follows whatever the relist's
// cap does (pairContext): after a prepare that returned, and after one whose
// outcome is unknown (outcomeUnknown), which may have landed. librados
// applies one client's operations on an object in the order it was given
// them (docs/cgo-limitations.md, "Bucket index completions to one shard can
// land out of order"), so the cancel reaches the shard after such a prepare,
// and where the prepare never landed the class refuses it, EINVAL, with
// nothing written
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1066-L1073 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1201-L1208 at v20.2.4).
func (s *Store) resetCurrent(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, en rgw.DirEntry, olh *rgw.BIEntry) error {
	x := s.newIndexOp(rec, key, "")
	x.prepareCmps = []rgw.BIEntry{*olh}
	perr := x.prepare(ctx, rgw.OpAdd)
	switch {
	case errors.Is(perr, radosclient.ErrCanceled):
		return fmt.Errorf("resetting %s: the olh entry changed since it was read: %w", versionName(rec, key), errRelinkMoved)
	case perr != nil && !outcomeUnknown(perr):
		return perr
	}
	c := x.completion(rgw.OpCancel, en.Ver, rgw.DirEntryMeta{Category: rgw.CategoryNone})
	pctx, done := pairContext(ctx)
	defer done()
	cerr := x.guardedWrite(pctx, func(w *radosclient.WriteOp) { rgw.BucketCompleteOp(w, c, x.s.release) })
	switch {
	case perr == nil:
		return cerr
	case cerr == nil, errors.Is(cerr, radosclient.ErrInvalid):
		return perr
	}
	return fmt.Errorf("%w; canceling it: %w", perr, cerr)
}

// remade reports whether b's name's OLH entry carries another tag than
// olhTag, the one the unlink that took a version's list entry sent: a
// cleared OLH entry is made again under the fresh OLH tag of a new head, and
// its epochs start again at 2 (start_modify), so its versions are all newer
// than that version, whatever their epochs. A name without an OLH entry, and
// a nil olhTag, report false.
func (b *bucketOLHIndex) remade(ctx context.Context, rec *op.BucketRecord, olhTag []byte) (bool, error) {
	if olhTag == nil {
		return false, nil
	}
	o, err := b.getOLH(ctx, &op.ObjectState{Bucket: rec, Key: olhHeadKey(b.x.obj.Key)})
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return o.Tag != string(olhTag), nil
}

// nullDataEntry is the instance entry of the null version's data of key's
// name: the plain key's entry, or, when that is the version marker, the null
// data key's behind it, as getInstanceEntry reads them but for the null
// delete marker's.
func (s *Store) nullDataEntry(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (rgw.DirEntry, error) {
	x := s.newOLHIndex(rec, key).x
	k := versionKey(olhHeadKey(key))
	var en rgw.DirEntry
	err := x.readShard(ctx, func(sh indexShard) error {
		var err error
		if en, err = x.dirEntry(ctx, sh, k, rgw.BIIndexInstance); err != nil || en.Flags&rgw.FlagVerMarker == 0 {
			return err
		}
		en, err = x.dirEntry(ctx, sh, rgw.ObjKey{Name: rgw.InstanceIndexKey(k, false)}, rgw.BIIndexPlain)
		return err
	})
	return en, err
}

// newestListed is the first two list entries of b's name, newest first
// (listVersions), each nil when the name lists fewer.
func (b *bucketOLHIndex) newestListed(ctx context.Context) (first, second *rgw.DirEntry, err error) {
	var got []rgw.DirEntry
	err = b.listVersions(ctx, func(en rgw.DirEntry) bool {
		got = append(got, en)
		return len(got) < 2
	})
	if err != nil {
		return nil, nil, err
	}
	if len(got) > 0 {
		first = &got[0]
	}
	if len(got) > 1 {
		second = &got[1]
	}
	return first, second, nil
}

// listsBefore reports whether version a lists before version b, as the class
// keys a name's versions newest first (get_list_index_key), a's list key
// taken from its instance record.
func listsBefore(a, b rgw.DirEntry) bool {
	return rgw.ListIndexKey(a.Key, a.VersionedEpoch) < rgw.ListIndexKey(b.Key, b.VersionedEpoch)
}

// heldVersion is a version heldNewer found held out of the listing: its
// instance record, and its record in the bucket's unlisted-version object as
// read.
type heldVersion struct {
	en     rgw.DirEntry
	record unlistedEntry
}

// heldNewer finds, for a relist of en, the instance record of a version of
// idx's name, the versions of that name newer than en that are stored and in
// no listing: each one, but en's own, that the name's OLH entry logs a
// removal of or the bucket's unlisted-version object records
// (recordUnlisted), whose instance record lists before en's (listsBefore),
// is no delete marker, names no list entry, and whose head is there, the OLH
// head holding the data for the null version. Instance records keep the
// order the versions were written in while they are unlisted: the class's
// unlink leaves the unlinked version's record as it was, and promotes the
// next version at its own epoch, its record read anew before it is written
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1388-L1427 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1936-L1958 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1514-L1554 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2083-L2105 at v20.2.4).
//
// held is the newest such version whose removal the log no longer holds and
// whose record names a refused removal and none pending (listableFirst): a
// replay refused that removal for the write tag its writer held it to,
// recording the version before its head op and trimming the removal after
// (applyOLHLog), or the writer's check after its replay refused it
// (checkRemoval), and that writer removes nothing more, so the version stays
// until a relist lists it again, whatever its lock. A version whose record
// names a removal a replay kept for its lock, still pending, is no held one,
// whatever its lock now: that removal's writer's check after its own replay
// decides it, and a relist that listed it first would have that check find
// it listed again. A record written before records named their removals is
// taken as such a one. unsettled reports a version newer than held whose
// removal the log still holds: no replay has yet carried that removal out,
// refused it, or trimmed it, so whether the version stays is not yet known.
// A record whose version was listed again since, or removed, names no such
// version.
func (s *Store) heldNewer(ctx context.Context, idx *bucketOLHIndex, rec *op.BucketRecord, en rgw.DirEntry) (held *heldVersion, unsettled bool, err error) {
	name := idx.x.obj.Key
	logged := map[string]bool{}
	e, err := idx.getOLH(ctx, &op.ObjectState{Bucket: rec, Key: olhHeadKey(name)})
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
	case err != nil:
		return nil, false, fmt.Errorf("reading the olh entry of %s before listing it again: %w", versionName(rec, name), err)
	}
	for _, entries := range e.PendingLog {
		for _, le := range entries {
			if le.Op == rgw.OLHOpRemoveInstance {
				logged[le.Key.Instance] = true
			}
		}
	}
	recorded, err := s.recordedInstances(ctx, rec, name)
	if err != nil {
		return nil, false, err
	}
	candidates := map[string]bool{}
	for k := range recorded {
		candidates[k] = true
	}
	maps.Copy(candidates, logged)
	var pending *rgw.DirEntry
	for _, instance := range slices.Sorted(maps.Keys(candidates)) {
		if instance == en.Key.Instance {
			continue
		}
		c, st, cerr := s.unlistedNewer(ctx, rec, meta.ObjKey{Name: name.Name, NS: name.NS, Instance: instance}, en)
		switch {
		case cerr != nil:
			return nil, false, cerr
		case st == nil:
		case logged[instance]:
			if pending == nil || listsBefore(c, *pending) {
				pending = &c
			}
		case !listableFirst(recorded[instance]):
		case held == nil || listsBefore(c, held.en):
			held = &heldVersion{en: c, record: recorded[instance]}
		}
	}
	return held, pending != nil && (held == nil || listsBefore(*pending, held.en)), nil
}

// unlistedNewer reads key's version, of rec's bucket, for heldNewer: its
// instance record, the null data's for the null version (nullDataEntry), and,
// when it lists before en, is no delete marker, names no list entry, and has
// its head, the OLH head holding the null version's data, that head as read;
// otherwise a nil head.
func (s *Store) unlistedNewer(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, en rgw.DirEntry) (rgw.DirEntry, *op.ObjectState, error) {
	read := s.getInstanceEntry
	if key.Instance == "" {
		read = s.nullDataEntry
	}
	c, err := read(ctx, rec, key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return c, nil, nil
	case err != nil:
		return c, nil, err
	case c.Flags&rgw.FlagDeleteMarker != 0, !listsBefore(c, en):
		return c, nil, nil
	}
	if listed, lerr := s.listed(ctx, rec, key, c); lerr != nil || listed {
		return c, nil, lerr
	}
	st, err := s.readOLHHead(ctx, rec, key)
	switch {
	case err != nil:
		return c, nil, err
	case key.Instance == "" && !holdsNullData(st), !st.Exists:
		return c, nil, nil
	}
	return c, st, nil
}

// recordedInstances is the records of key's name in the unlisted-version
// object of rec's bucket (recordUnlisted), by instance, none when the object
// does not exist.
func (s *Store) recordedInstances(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (map[string]unlistedEntry, error) {
	pool, err := s.indexPool(ctx, &rec.Info)
	if err != nil {
		return nil, err
	}
	prefix := unlistedEscaper.Replace(key.IndexKeyName()) + "/"
	out := map[string]unlistedEntry{}
	for after := ""; ; {
		rop := radosclient.NewReadOp()
		res := rop.OmapGetVals(after, prefix, unlistedPage)
		_, err := pool.Read(ctx, unlistedOID(&rec.Info), rop, radosclient.OpFlagNone)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return out, nil
		case err != nil:
			return nil, fmt.Errorf("reading the unlisted versions of %s/%s: %w", rec.Info.Bucket.Name, key.Name, err)
		}
		for k, v := range res.Values {
			if o, ok := parseUnlistedKey(k); ok && o.Name == key.Name && o.NS == key.NS {
				out[o.Instance] = unlistedEntry{k: k, v: v}
			}
			after = max(after, k)
		}
		if uint64(len(res.Values)) < unlistedPage {
			return out, nil
		}
	}
}

// listHeld lists again h, a version heldNewer found held out of the listing,
// through the relist a reader of its record runs (settleUnlisted): the record
// claimed, the version read again and left as it is when it is listed, gone
// or without its head, then the relist its refused removal runs, and the
// refused removals taken out of the record once the version is listed, a
// pending one merged in since kept (takeListed), or the record given back
// unclaimed. The record's age is not waited for: that wait keeps a late
// reader off a version whose writer may still be relisting it or checking
// its removal, and here the version must be listed before the version older
// than it, and two relists of one version list it once (relistVersion,
// relistBelow). What it leaves is logged by settleUnlisted; the caller's next
// round reads the listing again whatever it did. own is the instance of the
// version the caller relists, which the relist of h, and any relist it runs
// in turn, does not list (listingHeld).
func (s *Store) listHeld(ctx context.Context, rec *op.BucketRecord, own string, h *heldVersion) {
	pool, err := s.indexPool(ctx, &rec.Info)
	if err != nil {
		slog.WarnContext(ctx, "could not open the index pool to list again a newer version held out of the listing",
			slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
		return
	}
	chain := map[string]bool{own: true}
	if up, ok := ctx.Value(heldListingKey{}).(map[string]bool); ok {
		maps.Copy(chain, up)
	}
	s.settleUnlisted(context.WithValue(ctx, heldListingKey{}, chain), rec, pool, unlistedOID(&rec.Info), h.record, 0)
}

// heldListingKey keys, in the context of a relist listHeld runs, the
// instances of the versions whose relists are listing it, its caller's and
// theirs. An instance record moves up the listing order when its version is
// linked at epoch 0, so a version held now may be one of those, and a relist
// that listed it in turn would run inside its own.
type heldListingKey struct{}

// listingHeld reports whether instance's version is one whose relist ctx's
// relist runs inside (heldListingKey).
func listingHeld(ctx context.Context, instance string) bool {
	chain, ok := ctx.Value(heldListingKey{}).(map[string]bool)
	return ok && chain[instance]
}

// waitUnsettled spends one backoff of w, a relist round's wait on pending
// modifications (waitOLHPending), on a newer version whose removal is still
// logged (heldNewer), and reports whether the round should read again: false
// once w has passed its bound, which it then marks, as waitOLHPending does,
// so that a removal whose writer stopped before its replay holds the relist
// no longer than a pending attr does.
func (s *Store) waitUnsettled(ctx context.Context, w *olhCondWait) (bool, error) {
	switch {
	case w.pastBound:
		return false, nil
	case w.backoffs >= olhCondBackoffs:
		w.pastBound = true
		return false, nil
	}
	d := olhCondBackoff(w.backoffs)
	w.backoffs++
	if err := s.condBackoff(ctx, d); err != nil {
		return false, err
	}
	return true, nil
}
