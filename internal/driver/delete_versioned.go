package driver

import (
	"context"
	"errors"
	"fmt"
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
func (s *Store) writeDeleteMarker(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	marker := key
	res := &op.DeleteObjectResult{VersionID: meta.NullInstance, DeleteMarker: true}
	if rec.Info.Flags&meta.BucketVersionsSuspended == 0 {
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
	if err := s.setOLH(ctx, rec, marker, link); err != nil {
		return nil, err
	}
	return res, nil
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
func (s *Store) removeVersion(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	v := key
	null := key.Instance == meta.NullInstance
	if null {
		v.Instance = ""
	}
	en, err := s.getInstanceEntry(ctx, rec, v)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return nil, fmt.Errorf("%s: %w", versionName(rec, key), op.ErrNoSuchKey)
	case err != nil:
		return nil, fmt.Errorf("reading the index entry of %s: %w", versionName(rec, key), err)
	}
	if err := versionConditions(en.Meta, p); err != nil {
		return nil, fmt.Errorf("deleting %s: %w", versionName(rec, key), err)
	}
	if err := s.unlinkObjInstance(ctx, rec, v, olhUnlink{NullVersion: null, SkipUpdate: p.SkipOLHUpdate}); err != nil {
		return nil, err
	}
	return &op.DeleteObjectResult{VersionID: key.Instance, DeleteMarker: en.Flags&rgw.FlagDeleteMarker != 0}, nil
}

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
