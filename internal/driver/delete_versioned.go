package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
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
// or marker linked in between (checkOLHLink). A null marker gets the init's guard
// alone, which holds against rgw-go's conditional writers only: its link
// replaces the null version's list entry, which no unlink restores
// (prepareOLHAttempt). With no version current, a delete marker or a head no
// link has reached, the conditions are checked against the key's head as
// read without following it, the OLH head object, as radosgw's are
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5882-L5897 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6509 at v20.2.4;
// docs/exclusions.md, "A conditional delete marker is checked against the
// version current when it links").
//
// On a suspended bucket p.Expect is the null version the null marker
// replaces, whose data is the OLH head's: its write tag goes to the link as
// its ExpectTag, which each attempt compares on the head it reads and its
// init compares again, and which guards the replay's removal of that data,
// the link logging it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1731 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1810 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1872 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1951 at v20.2.4).
// The replay runs whatever p.SkipOLHUpdate says, as the guard is in it. A
// removal it refuses is answered as refusedRemoval says. On an enabled
// bucket the marker removes nothing, so p.Expect is not read.
func (s *Store) writeDeleteMarker(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	marker := key
	res := &op.DeleteObjectResult{VersionID: meta.NullInstance, DeleteMarker: true}
	suspended := rec.Info.Flags&meta.BucketVersionsSuspended != 0
	if !suspended {
		marker.Instance = meta.NewInstance()
		res.VersionID = marker.Instance
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
		Cond:         s.deleteMarkerCondition(p),
	}
	if suspended && p.Expect != nil {
		link.ExpectTag, link.SkipUpdate = expectTag(p.Expect), false
	}
	err := s.setOLH(ctx, rec, marker, link)
	switch {
	case err == nil:
		return res, nil
	case link.ExpectTag != nil && finalRefusal(err):
		return nil, s.refusedRemoval(ctx, rec, key, err, s.relistNull)
	}
	return nil, err
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
// (errRemovalRefused), so relist lists the version again, and the delete is
// ConcurrentModification whatever relist does. A relist that finds the
// version gone, its record or its instance entry removed since by another
// replay, has nothing to list; one that fails otherwise leaves the version
// unlisted and is logged.
func (s *Store) refusedRemoval(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, refusal error,
	relist func(context.Context, *op.BucketRecord, meta.ObjKey) error,
) error {
	what := versionName(rec, key)
	v := key
	if v.Instance == meta.NullInstance {
		v.Instance = ""
	}
	err := relist(context.WithoutCancel(ctx), rec, v)
	switch {
	case err == nil:
	case errors.Is(err, radosclient.ErrNotFound), errors.Is(err, op.ErrNoSuchKey):
		slog.DebugContext(ctx, "a version whose removal its write tag refused was removed since; nothing to list again",
			slog.String("version", what), slog.Any("error", err))
	default:
		slog.WarnContext(ctx, "listing again a version whose removal its write tag refused failed; it stays unlisted",
			slog.String("version", what), slog.Any("error", err))
	}
	return fmt.Errorf("%w: deleting %s: %w", op.ErrConcurrentModification, what, refusal)
}

// deleteMarkerCondition is a delete marker's condition, or nil when p sets
// none: check_preconditions on the version current. Where follow_olh finds
// none, a delete marker current or a head without olh.info, it is checked on
// the key's head read again without following it, as radosgw falls back to
// the head it read first; a current version whose head is gone is checked as
// missing, as radosgw checks the state the follow resolved. Each of the three
// compares the attributes of what it is checked on, so a pass rests on it:
// on the head, on the null version's data there (headNullData).
func (s *Store) deleteMarkerCondition(p op.DeleteParams) olhCondition {
	if p.IfMatch == "" && p.IfMatchSize == nil && p.IfMatchLastModified.IsZero() {
		return nil
	}
	return func(ctx context.Context, cur *op.ObjectState) (olhPass, error) {
		st := cur
		onHead := cur.DeleteMarker || (!cur.Exists && cur.Key.Instance == "")
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
	if _, ok := head.Attrs[meta.AttrETag]; !ok {
		return false
	}
	return (p.IfMatch != "" && p.IfMatch != "*") || p.IfMatchSize != nil || !p.IfMatchLastModified.IsZero()
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
// version marker, whose meta is empty (docs/exclusions.md, "A versioned
// delete of versionId=null checks the null version itself").
//
// p.Expect is the version as its caller read it (expectRemoval), checked
// before the unlink, a mismatch being ConcurrentModification with nothing
// written; the unlink's replay then removes the head only under the write
// tag it carries, running whatever p.SkipOLHUpdate says. For the null delete
// marker the unlink's op compares the marker's instance entry with the raw
// entry this read returned (olhMarkerCmp). A removal the replay refuses is
// answered as refusedRemoval says.
func (s *Store) removeVersion(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
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
	u := olhUnlink{NullVersion: null, SkipUpdate: p.SkipOLHUpdate}
	if p.Expect != nil {
		tag, eerr := s.expectRemoval(ctx, rec, v, en, p.Expect)
		if eerr != nil {
			return nil, fmt.Errorf("deleting %s: %w", versionName(rec, key), eerr)
		}
		u.ExpectTag, u.SkipUpdate = tag, false
		u.Marker = en.Flags&rgw.FlagDeleteMarker != 0
		if u.Marker && v.Instance == "" {
			u.MarkerCmp = &olhMarkerCmp{bi: bi, en: en}
		}
	}
	_, olhTag, err := s.unlinkVersion(ctx, rec, v, u)
	switch {
	case err == nil:
		return &op.DeleteObjectResult{VersionID: key.Instance, DeleteMarker: en.Flags&rgw.FlagDeleteMarker != 0}, nil
	case u.ExpectTag != nil && finalRefusal(err):
		relist := func(ctx context.Context, rec *op.BucketRecord, v meta.ObjKey) error {
			return s.relistVersion(ctx, rec, v, olhTag)
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
//   - Any other version must be one e names as data, and its head, the key's
//     OLH head for the null version, must carry e's write tag (expectTag),
//     which then guards the removal.
func (s *Store) expectRemoval(ctx context.Context, rec *op.BucketRecord, v meta.ObjKey, en rgw.DirEntry, e *op.ObjectState) ([]byte, error) {
	marker := en.Flags&rgw.FlagDeleteMarker != 0
	switch {
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

// relistRounds bounds relistVersion's and relistNull's rounds: each reads
// the key afresh, and one whose link another writer's change refused gives
// way to the next. A round's wait on pending modifications is not one.
const relistRounds = 5

// errNullOwned ends a relist of the null version's data whose record a newer
// null write has taken over (relistNull).
var errNullOwned = errors.New("a newer null write owns the null version")

// relistVersion lists again v, a named version whose removal its write tag
// refused once the unlink had taken its list entry, olhTag being the OLH tag
// that unlink sent, and does nothing when its instance entry or its head is
// gone, another replay having removed it. It works in rounds, each reading
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
// it behind that version, when the name's OLH entry was made again since
// (remade), or when every one of relistRounds rounds lost its link to
// another writer.
func (s *Store) relistVersion(ctx context.Context, rec *op.BucketRecord, v meta.ObjKey, olhTag []byte) error {
	idx := s.newOLHIndex(rec, v)
	var w olhCondWait
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
		head, err := s.readRelinkHead(ctx, rec, v)
		if err != nil {
			return err
		}
		remade, err := idx.remade(ctx, rec, olhTag)
		if err != nil {
			return err
		}
		newest, _, err := idx.newestListed(ctx)
		if err != nil {
			return err
		}
		if remade || newest != nil && !listsBefore(en, *newest) {
			break
		}
		wait, refuse, err := s.waitOLHPending(ctx, idx, head, &w)
		switch {
		case err != nil:
			return err
		case wait:
			continue
		}
		round++
		err = s.setOLH(ctx, rec, v, olhLink{Relink: &olhRelink{head: head, pendingOK: !refuse}})
		if !errors.Is(err, errRelinkMoved) {
			return err
		}
	}
	return s.relistBelow(ctx, rec, v, nil, 0, olhTag)
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
//   - otherwise the null data below the newest version (relistBelow).
//
// A round whose link the guard refuses gives way to the next, up to
// relistRounds. While another writer's modification is pending a round
// waits, and once the wait has passed those pending attrs no longer refuse
// the link, as relistVersion's rounds do.
func (s *Store) relistNull(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) error {
	idx := s.newOLHIndex(rec, key)
	var first uint64
	var w olhCondWait
	for round, read := 0, false; round < relistRounds; {
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
		round++
		newest, next, err := idx.newestListed(ctx)
		if err != nil {
			return err
		}
		marker := newest != nil && newest.Key.Instance == "" && newest.Flags&rgw.FlagDeleteMarker != 0
		guard := &olhRelink{head: head, pendingOK: !refuse}
		switch {
		case marker && next != nil && listsBefore(*next, dn):
			l := olhLink{Relink: guard}
			if next.Flags&rgw.FlagDeleteMarker != 0 {
				l.DeleteMarker, l.Meta = true, next.Meta
			}
			if err = s.setOLH(ctx, rec, indexToObjKey(next.Key), l); err == nil {
				continue
			}
		case marker, newest == nil, listsBefore(dn, *newest):
			if err = s.setOLH(ctx, rec, key, olhLink{Relink: guard}); err == nil {
				return nil
			}
		default:
			err = s.relistBelow(ctx, rec, key, guard, first, nil)
			if err == nil || errors.Is(err, errNullOwned) {
				return nil
			}
		}
		if !errors.Is(err, errRelinkMoved) {
			return err
		}
	}
	return fmt.Errorf("listing %s again: every one of %d rounds lost to another writer", versionName(rec, key), relistRounds)
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
func (s *Store) relistBelow(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, guard *olhRelink, first uint64, olhTag []byte) error {
	read := s.getInstanceEntry
	if key.Instance == "" {
		read = s.nullDataEntry
	}
	en, err := read(ctx, rec, key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return nil
	case err != nil:
		return err
	case key.Instance == "" && en.VersionedEpoch != first:
		return errNullOwned
	}
	if rerr := s.resetCurrent(ctx, rec, key, en); rerr != nil {
		return rerr
	}
	remade, err := s.newOLHIndex(rec, key).remade(ctx, rec, olhTag)
	if err != nil {
		return err
	}
	epoch := en.VersionedEpoch
	if remade || epoch == 0 {
		epoch = 1
	}
	if guard == nil {
		guard = &olhRelink{}
	}
	return s.setOLH(ctx, rec, key, olhLink{Epoch: epoch, Relink: guard})
}

// resetCurrent clears every flag but FLAG_VER on en, key's instance record
// as read: a prepare, then a cancel carrying en's own version, awaited. The
// class's completion resets the flags whatever it completes, and its cancel
// writes the record back, or removes it when the record went since the read
// and the prepare made a placeholder in its place
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L851-L856 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1061-L1126 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L983-L988 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1196-L1260 at v20.2.4),
// where an ADD completion would write the placeholder back as an entry that
// exists. A cancel touches neither the record's meta nor the bucket's stats.
func (s *Store) resetCurrent(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, en rgw.DirEntry) error {
	x := s.newIndexOp(rec, key, "")
	if err := x.prepare(ctx, rgw.OpAdd); err != nil {
		return err
	}
	c := x.completion(rgw.OpCancel, en.Ver, rgw.DirEntryMeta{Category: rgw.CategoryNone})
	return x.guardedWrite(ctx, func(w *radosclient.WriteOp) { rgw.BucketCompleteOp(w, c, x.s.release) })
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
