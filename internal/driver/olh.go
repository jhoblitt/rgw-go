package driver

import (
	"bytes"
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
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A versioned write links its version under the name's OLH, and a versioned
// delete unlinks one, as RGWRados::set_olh and unlink_obj_instance do: each
// attempt reads the name's OLH head without following it, records a pending
// modification on it, sends the index call, and then replays the name's
// pending log onto the head as update_olh does.

const (
	// olhAttempts is MAX_ECANCELED_RETRY, the attempts set_olh and
	// unlink_obj_instance make before they answer EIO
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8688-L8689 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9434-L9435 at v20.2.4).
	olhAttempts = 100
	// olhReplayRetries bounds a replay's second chances: its pass read again
	// from a fresh head after another writer's guard refused its apply, and
	// a tail run again after its head op landed. radosgw's replay needs
	// none, its u64-mode olh.ver compare letting a later epoch through
	// (docs/differences.md, "apply_olh_log's olh.ver guard is EQ on the bytes
	// read, retried from a fresh read").
	olhReplayRetries = 10
)

// olhLink is a link's request, set_olh's arguments: whether the version is
// a delete marker and the meta a marker's entry is written with, the epoch,
// 0 letting the class take the OLH entry's next, and unmod_since, compared
// in whole seconds unless HighPrecision. SkipUpdate is
// skip_olh_obj_update, which leaves the replay to the name's next update.
// Cond, when set, is a conditional write's condition, which the link
// commits only if it held when the link landed (olh_cond.go). ExpectTag,
// when not nil, is the user.rgw.idtag the OLH head must hold, absent read as
// empty, checked on the head each attempt reads and in its init, and the
// write tag the replay's removal of a version the link logged for removal
// must find. ExpectHead, for a conditional null delete marker, makes that the
// write tag of the OLH head each attempt followed, the one its condition was
// evaluated on, in place of a caller's ExpectTag. Inited, when set, is a
// modification the caller inited on the OLH head in the op that wrote the
// version's head (writeNullVersion): the first attempt links under its op
// tag, with no init of its own, when the head it reads still holds that
// modification's pending attr; a link with a Cond is refused, as that
// attempt checks nothing. Relink, when set, links a version already written
// and once listed (olhRelink). KeepVersion keeps
// the version when no link of it lands, for a caller whose version names
// data it must not lose, a completion's parts; a link the check after it
// voids is still unlinked. WriteTag, when not nil, is the user.rgw.idtag the
// head write that made the version stored, for the link of a client's write,
// which succeeds when a delete took its version first (versionTaken).
type olhLink struct {
	DeleteMarker  bool
	Meta          rgw.DirEntryMeta
	Epoch         uint64
	UnmodSince    time.Time
	HighPrecision bool
	SkipUpdate    bool
	Cond          olhCondition
	ExpectTag     []byte
	ExpectHead    bool
	Inited        *olhInited
	Relink        *olhRelink
	KeepVersion   bool
	WriteTag      []byte
}

// olhRelink is a link that lists again a version a refused removal left
// unlisted (delete_versioned.go): no failure removes the version, which the
// link did not write. head, when not nil, is the OLH head as the caller read
// it before it chose the link's epoch, which each attempt's read must still
// match, its OLH tag, its olh.info, its olh.ver and no pending attr, or the
// link answers errRelinkMoved with nothing written; the init is guarded on
// olh.info, olh.ver and no pending attr, so that no writer's modification
// lands between that read and the link. olh.ver catches a replay that left
// olh.info as it was: the unlink of a noncurrent version, or a second null
// delete marker, whose olh.info is the first's. With pendingOK, set once the
// caller's wait on other writers' pending modifications has passed
// (waitOLHPending), a pending attr no longer refuses the link, and the init
// is guarded on olh.info and olh.ver alone, as a conditional writer's is past
// its wait.
type olhRelink struct {
	head      *op.ObjectState
	pendingOK bool
}

// errRelinkMoved ends a link whose olhRelink head another writer has moved.
var errRelinkMoved = errors.New("the key's OLH head changed since the re-link chose its epoch")

// moved reports whether head, an attempt's read of the OLH head, is no longer
// r's.
func (r *olhRelink) moved(head *op.ObjectState) bool {
	switch {
	case r.head == nil:
		return false
	case head.Exists != r.head.Exists:
		return true
	case !head.Exists:
		return false
	}
	if !bytes.Equal(head.Attrs[meta.AttrOLHIDTag], r.head.Attrs[meta.AttrOLHIDTag]) ||
		!bytes.Equal(head.Attrs[meta.AttrOLHInfo], r.head.Attrs[meta.AttrOLHInfo]) ||
		!bytes.Equal(head.Attrs[meta.AttrOLHVer], r.head.Attrs[meta.AttrOLHVer]) {
		return true
	}
	if r.pendingOK {
		return false
	}
	for name := range head.Attrs {
		if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
			return true
		}
	}
	return false
}

// olhInited is a modification a caller inited on an OLH head and hands
// setOLH to link under (olhLink.Inited): head is the OLH head as the op that
// inited it left it, which a cancel of it is guarded on, and opTag the
// modification's.
type olhInited struct {
	head  *op.ObjectState
	opTag string
}

// olhUnlink is an unlink's request, unlink_obj_instance's arguments: the
// epoch, as a link's, NullVersion for a request that named the null
// version, SkipUpdate as a link's, and ExpectTag, when not nil, the write
// tag the replay's removal of the version must find. Marker is set for a
// delete marker's unlink, whose ExpectTag no head carries
// (markerRemovalTag): nothing compares it before the unlink, and the replay
// guards on it as it is. Any other unlink logs the version's removal, which
// is checked after the replay (checkRemoval). MarkerCmp, when not nil, is
// the null delete marker the unlink must find (olhMarkerCmp).
type olhUnlink struct {
	Epoch       uint64
	NullVersion bool
	SkipUpdate  bool
	ExpectTag   []byte
	Marker      bool
	MarkerCmp   *olhMarkerCmp
}

// olhMarkerCmp is the null delete marker an unlink must find, as its caller
// read it: bi, its instance entry's omap key and value as the read's bi_get
// answered them, never encoded again, which the unlink's op compares
// unchanged (bucketOLHIndex.unlinkInstance), and en, that entry decoded,
// whose versioned_epoch and mtime are the marker's identity. A second null
// marker rewrites that key and a null write's link removes it, so either,
// landing after the read, refuses the unlink.
type olhMarkerCmp struct {
	bi rgw.BIEntry
	en rgw.DirEntry
}

// errMarkerGone ends an unlink whose null delete marker is no longer the one
// its caller read.
var errMarkerGone = fmt.Errorf("%w: the null delete marker is not the one expected", op.ErrConcurrentModification)

// recheck reads key's null delete marker again after an unlink its compare
// refused: errMarkerGone when the entry is gone, no longer a marker, or
// another marker by its versioned_epoch or mtime; otherwise the marker as
// read now, whose bytes a demotion, a promotion or a reshard's copy may have
// rewritten, for the next attempt to compare. The same bytes mean another
// guard refused the unlink.
func (c *olhMarkerCmp) recheck(ctx context.Context, s *Store, rec *op.BucketRecord, key meta.ObjKey) (*olhMarkerCmp, error) {
	en, bi, err := s.getInstanceEntryBI(ctx, rec, key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return nil, errMarkerGone
	case err != nil:
		return nil, err
	case en.Flags&rgw.FlagDeleteMarker == 0, en.VersionedEpoch != c.en.VersionedEpoch, !en.Meta.Mtime.Equal(c.en.Meta.Mtime):
		return nil, errMarkerGone
	}
	return &olhMarkerCmp{bi: bi, en: en}, nil
}

// olhStopAfterLink, which only the integration build sets, reports whether
// s's writers stop once their link or unlink has landed, before their
// replay, as a gateway that dies there leaves the name: the pending attr
// and the log entry wait for the next reader's replay.
var olhStopAfterLink func(s *Store) bool

// stopsAfterLink reports whether olhStopAfterLink stops s's writers.
func (s *Store) stopsAfterLink() bool { return olhStopAfterLink != nil && olhStopAfterLink(s) }

// setOLH is set_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8669-L8762 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9415-L9508 at v20.2.4):
// it links key, a version of rec's bucket, under its name's OLH in up to
// olhAttempts attempts, then EIO, and replays the name's pending log unless
// p.SkipUpdate.
//   - Each attempt reads the OLH head without following it, inits a
//     modification on it, and links. An init another writer's guard refuses
//     tries again from a fresh read, and so does one that finds gone the
//     head its read found, which set_olh answers ENOENT
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8699-L8706 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9445-L9452 at v20.2.4;
//     docs/differences.md, "A versioned write retries when the key's OLH
//     head goes between its read and its init").
//   - A link that fails cancels the modification, unless its outcome is
//     unknown (outcomeUnknown), when its pending attr stays: set_olh cancels
//     it then too
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8715-L8717 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9461-L9463 at v20.2.4;
//     docs/differences.md, "A versioned link or unlink whose outcome is
//     unknown keeps its pending attr"). One the index refused for the head's
//     OLH tag repairs the head from the index's OLH entry and tries again;
//     any other runs a best-effort update unless p.SkipUpdate, and is
//     returned, but for the class's ENOENT on a link with p.WriteTag and no
//     p.Cond whose version versionTaken finds a delete took first, which
//     is success with nothing removed (docs/differences.md, "A versioned
//     write whose version a delete removes before its link succeeds"). A
//     conditional link keeps the refusal, as its condition is known to hold
//     only once its link lands (checkOLHLink).
//   - The replay is finishOLHModification's, whose ECANCELED is success. A
//     null delete marker linked over null data, whose removal the class logs
//     with the link, has that removal checked after it (checkRemoval).
//   - p.ExpectTag, p.Cond and p.Relink add their checks to each attempt
//     before its init (prepareOLHAttempt), and a conditional link is
//     checked after it lands, before the replay (checkOLHLink). Each backoff
//     of a conditional link waiting for other writers is one of the
//     olhAttempts.
//   - p.ExpectHead takes the write tag of the head an attempt followed, after
//     its condition was evaluated on it, as that attempt's expected tag: the
//     init compares it, so a write that reached the head since sends the
//     attempt round again to evaluate the condition anew, and the replay
//     removes the null version's data the link logged only under it, so a
//     null write that reaches the head after the init is kept.
//   - p.Inited stands for the first attempt's init while the head holds its
//     pending attr (takeInitedTag); a later attempt inits its own. One the
//     first attempt never took, setOLH ending before it, is canceled.
//
// When setOLH gives up before a link of its own landed, on an answer that
// proves none did, it removes the version itself, as removeFailedVersion
// says, unless p.KeepVersion, and so it does whatever the answer when an
// attempt ends before its init, in its head read or in prepareOLHAttempt;
// once one landed it removes nothing, whatever it then returns, so its
// caller never removes the version. A conditional link the check voids is
// removed by that check's unlink. A link with p.Relink removes nothing on
// any path.
func (s *Store) setOLH(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p olhLink) error {
	what := versionName(rec, key)
	idx := s.newOLHIndex(rec, key)
	f := olhLinkFields{deleteMarker: p.DeleteMarker, meta: p.Meta, epoch: p.Epoch, unmodSince: p.UnmodSince, highPrecision: p.HighPrecision}
	removes := !p.DeleteMarker && p.Relink == nil && !p.KeepVersion
	unlinked := func(err error) error {
		if removes {
			s.removeFailedVersion(ctx, idx.x.rec, key, err)
		}
		return err
	}
	inited := p.Inited
	// beforeInit ends an attempt that sent no init. Every earlier attempt
	// ended before its link or on the class's refusal of it, so no link of
	// the write can land, even when the context that ended was the client's:
	// the version is removed past it, and the caller's inited modification,
	// when no attempt took it, is canceled past it.
	beforeInit := func(err error) error {
		err = fmt.Errorf("linking %s: %w", what, err)
		if inited != nil {
			s.olhCancelModification(context.WithoutCancel(ctx), inited.head, inited.opTag)
		}
		if removes {
			s.removeUnlinkedVersion(context.WithoutCancel(ctx), idx.x.rec, key, err)
		}
		return err
	}
	if p.Inited != nil && p.Cond != nil {
		return beforeInit(errInitedCond)
	}
	var wait olhCondWait
	for range olhAttempts {
		head, err := s.readOLHHead(ctx, rec, olhHeadKey(key))
		if err != nil {
			return beforeInit(err)
		}
		var a olhAttempt
		expect := p.ExpectTag
		opTag, tagged := takeInitedTag(head, &inited), true
		if opTag == "" {
			a, err = s.prepareOLHAttempt(ctx, idx, head, key, p, &wait)
			switch {
			case err != nil:
				return beforeInit(err)
			case a.wait:
				continue
			}
			if p.ExpectHead {
				expect = expectTag(head)
			}
			found := head.Exists
			tagged = olhTagged(head)
			opTag, _, err = s.olhInitModification(ctx, head, a.guard, expect)
			switch {
			case errors.Is(err, radosclient.ErrCanceled), found && errors.Is(err, radosclient.ErrNotFound):
				continue
			case err != nil:
				return unlinked(fmt.Errorf("linking %s: %w", what, err))
			}
		}
		err = idx.linkOLH(ctx, head, key, opTag, f)
		if err == nil {
			if a.check != nil && !s.stopsAfterLink() {
				if cerr := s.checkOLHLink(ctx, idx, key, a.check, p.SkipUpdate); cerr != nil {
					return cerr
				}
			}
			expect = olhReplayTag(expect, head, tagged)
			var rm *olhRemoval
			if p.DeleteMarker && key.Instance == "" && p.Relink == nil && holdsNullData(head) {
				rm = loggedRemoval(key, head, expect)
			}
			return s.finishOLHModification(ctx, idx, head, opTag, p.SkipUpdate, expect, rm)
		}
		if !outcomeUnknown(err) {
			s.olhCancelModification(ctx, head, opTag)
		}
		if errors.Is(err, radosclient.ErrCanceled) {
			if rerr := s.repairOLH(ctx, idx, head); rerr != nil && !errors.Is(rerr, radosclient.ErrCanceled) {
				return unlinked(fmt.Errorf("linking %s: %w", what, rerr))
			}
			continue
		}
		if !p.SkipUpdate {
			s.updateOLHAfterFailure(ctx, idx, head)
		}
		if p.WriteTag != nil && p.Cond == nil && !p.DeleteMarker && p.Relink == nil &&
			errors.Is(err, radosclient.ErrNotFound) && s.versionTaken(ctx, idx.x.rec, key, p.WriteTag) {
			slog.DebugContext(ctx, "a delete took the version before its link; the write stands",
				slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.String("instance", key.Instance))
			return nil
		}
		return unlinked(fmt.Errorf("linking %s: %w", what, err))
	}
	return unlinked(fmt.Errorf("%w: linking %s: every one of %d attempts lost to another writer", op.ErrUnknown, what, olhAttempts))
}

// versionTaken reports whether a delete took key's version, of rec's
// bucket, after the head write that stored writeTag and before a link the
// class then refused with ENOENT: the version's index entry, read as the
// link reads a version's data
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1668-L1688 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1809-L1829 at v20.2.4),
// the null version's behind the version marker but for the null delete
// marker's (nullDataEntry), is gone, and the version's head, the OLH head
// for the null version, no longer holds that write. A delete's replay
// removes the head, the null version's OLH attrs alone kept, then its
// entry, and queues the tails the head named for the GC
// (removeHeadFlagged), so the delete owns the write's tails. An entry gone
// while the head still holds the write was removed by no delete, and a
// read that fails proves nothing: both answer false.
func (s *Store) versionTaken(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, writeTag []byte) bool {
	read := s.getInstanceEntry
	if key.Instance == "" {
		read = s.nullDataEntry
	}
	if _, err := read(ctx, rec, key); !errors.Is(err, radosclient.ErrNotFound) {
		return false
	}
	head, err := s.readOLHHead(ctx, rec, key)
	return err == nil && (!head.Exists || !bytes.Equal(head.Attrs[meta.AttrIDTag], writeTag))
}

// errInitedCond refuses a link given both an inited modification and a
// condition (olhLink.Inited).
var errInitedCond = fmt.Errorf("%w: a link of an inited modification was given a condition, which its first attempt would not check", op.ErrInternalError)

// takeInitedTag is the op tag of *inited, taken once: a modification
// setOLH's caller inited on the OLH head, which an attempt links under in
// place of its own init while head, as it read it, holds that modification's
// pending attr, the init having given the head an OLH tag. Otherwise it is
// "" and the attempt inits its own: a head without the attr has had it
// expired, leaving nothing to cancel.
func takeInitedTag(head *op.ObjectState, inited **olhInited) string {
	in := *inited
	*inited = nil
	if in == nil {
		return ""
	}
	if _, ok := head.Attrs[meta.AttrOLHPendingPrefix+in.opTag]; !ok {
		return ""
	}
	return in.opTag
}

// unlinkObjInstance is unlink_obj_instance
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8764-L8866 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9510-L9612 at v20.2.4):
// setOLH's loop around the unlink of key, a version of rec's bucket, which
// takes it off the name's listing and logs its removal, then EIO. An unlink
// another writer's guard refuses tries again from a fresh read with no
// repair, as unlink_obj_instance does; any other failure runs a best-effort
// update unless p.SkipUpdate, and is returned; one whose outcome is unknown
// keeps its pending attr, as setOLH's link does. The class's ENOENT, the
// version not there, and init's, the head its read found gone, answer
// NoSuchKey. The unlink makes no version, so a failed one removes none. A
// version another replay kept for its object lock, the unlink's entries
// applied by that replay first, is removed after the unlink's own replay
// finds them gone, as checkRemoval says.
// p.ExpectTag guards the replay's removal of a named version alone, the init
// comparing no write tag: its caller checks the version's head before the
// unlink. The null version's head is the OLH head, which init gives a fresh
// write tag when it has no OLH tag, so for it each attempt compares the tag
// on the head it read, a mismatch being ConcurrentModification with nothing
// written, the init compares it again, and the replay's removal is guarded on
// the tag the head carries after the init (olhReplayTag). A delete marker's
// unlink (p.Marker) compares no head, and its replay guards on p.ExpectTag
// as it is. With p.MarkerCmp the unlink's op compares the null delete
// marker's instance entry with the bytes its caller read, and a refusal
// reads the marker again (olhMarkerCmp.recheck): another marker, or none,
// is ConcurrentModification with nothing unlinked; the same marker rewritten
// is tried again, within the attempts, with the bytes read now.
func (s *Store) unlinkObjInstance(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p olhUnlink) error {
	_, _, err := s.unlinkVersion(ctx, rec, key, p)
	return err
}

// unlinkOutcome is what an unlinkVersion that returned did to the version.
type unlinkOutcome int

const (
	// unlinkNotSent: no unlink of the version was sent, or the class
	// refused it, so the version stays linked.
	unlinkNotSent unlinkOutcome = iota
	// unlinkUnknown: the unlink was sent and its outcome is unknown.
	unlinkUnknown
	// unlinkLanded: the unlink landed; its replay may have failed.
	unlinkLanded
	// unlinkGone: the class found no such version to unlink.
	unlinkGone
)

// unlinkVersion is unlinkObjInstance, reporting as well what it did to the
// version, which a caller that must know whether the version stays linked
// reads, and, once the unlink landed, the OLH tag the head carried after
// the attempt's init, the one the unlink sent.
func (s *Store) unlinkVersion(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p olhUnlink) (unlinkOutcome, []byte, error) {
	what := versionName(rec, key)
	idx := s.newOLHIndex(rec, key)
	f := olhUnlinkFields{epoch: p.Epoch, nullVersion: p.NullVersion}
	cmp := p.MarkerCmp
	for range olhAttempts {
		if cmp != nil {
			f.cmp = &cmp.bi
		}
		head, err := s.readOLHHead(ctx, rec, olhHeadKey(key))
		if err != nil {
			return unlinkNotSent, nil, fmt.Errorf("unlinking %s: %w", what, err)
		}
		var initTag []byte
		if key.Instance == "" && p.ExpectTag != nil && !p.Marker {
			if !bytes.Equal(head.Attrs[meta.AttrIDTag], p.ExpectTag) {
				return unlinkNotSent, nil, fmt.Errorf("%w: unlinking %s: the head's write tag is not the one expected", op.ErrConcurrentModification, what)
			}
			initTag = p.ExpectTag
		}
		tagged := olhTagged(head)
		opTag, _, err := s.olhInitModification(ctx, head, nil, initTag)
		switch {
		case errors.Is(err, radosclient.ErrCanceled):
			continue
		case err != nil:
			return unlinkNotSent, nil, unlinkError(what, err)
		}
		err = idx.unlinkInstance(ctx, head, key, opTag, f)
		if err == nil {
			expect := p.ExpectTag
			var rm *olhRemoval
			if !p.Marker {
				if key.Instance == "" {
					expect = olhReplayTag(p.ExpectTag, head, tagged)
				}
				rm = loggedRemoval(key, head, expect)
			}
			return unlinkLanded, slices.Clone(head.Attrs[meta.AttrOLHIDTag]), s.finishOLHModification(ctx, idx, head, opTag, p.SkipUpdate, expect, rm)
		}
		unknown := outcomeUnknown(err)
		if !unknown {
			s.olhCancelModification(ctx, head, opTag)
		}
		if errors.Is(err, radosclient.ErrCanceled) {
			if cmp != nil {
				if cmp, err = cmp.recheck(ctx, s, rec, key); err != nil {
					return unlinkNotSent, nil, fmt.Errorf("unlinking %s: %w", what, err)
				}
			}
			continue
		}
		if !p.SkipUpdate {
			s.updateOLHAfterFailure(ctx, idx, head)
		}
		switch {
		case unknown:
			return unlinkUnknown, nil, unlinkError(what, err)
		case errors.Is(err, radosclient.ErrNotFound):
			return unlinkGone, nil, unlinkError(what, err)
		}
		return unlinkNotSent, nil, unlinkError(what, err)
	}
	return unlinkNotSent, nil, fmt.Errorf("%w: unlinking %s: every one of %d attempts lost to another writer", op.ErrUnknown, what, olhAttempts)
}

// unlinkError is an unlink's failure, NoSuchKey for an ENOENT.
func unlinkError(what string, err error) error {
	if errors.Is(err, radosclient.ErrNotFound) {
		return fmt.Errorf("%w: unlinking %s: %w", op.ErrNoSuchKey, what, err)
	}
	return fmt.Errorf("unlinking %s: %w", what, err)
}

// finishOLHModification ends an attempt whose link or unlink landed, head
// being the OLH head its init left and idx the index the attempt sent its
// call through, which holds the bucket record a reshard it waited out left:
// unless skip, or the integration build's
// fault point stops the writer here, it replays the name's pending log,
// whose ECANCELED is success, another writer having replaced the OLH or
// applied the log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8752-L8755 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9498-L9501 at v20.2.4).
// The replay guards the removals the log names under opTag on expectTag,
// when it is not nil, and returns any refusal of them after the error, if
// there is one. rm, when not nil, is the version the link or unlink logged
// for removal: when the replay found none of the writer's entries left to
// apply, another replay having applied them, it is checked with
// checkRemoval, whose refusal is returned as the replay's would be. A replay
// that did not read the whole log, having run out of passes or met an OLH
// entry another replay cleared, proves nothing of the writer's entries, so
// the key's OLH entry is read: while its pending log still holds opTag the
// removal is left to the next replay, which keeps a version under a lock no
// delete passes and removes any other, and a refusal acted on there would
// re-link a version that replay then removes.
//
// The link or unlink has landed, so the replay and the check run whatever
// the caller's context does, as a PutObject's head write outlives its
// client: a writer that stopped here would leave a version another replay
// keeps to no one. Each is bounded, the replay by olhReplayRetries.
//
// When the replay read the whole log and found nothing under opTag, the
// modification's pending attr is removed as olhCancelModification removes
// it. A link at an epoch below the OLH entry's writes the version
// non-current and logs nothing
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1760 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1892-L1901 at v20.2.4),
// so no apply removes its attr. radosgw's leaves it to expire, since
// apply_olh_log returns at once on an empty log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8423-L8425 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9169-L9171 at v20.2.4).
// An entry another writer applied and trimmed had its attr removed by that
// apply, so the removal finds nothing, and an entry still in the log keeps
// its attr, which makes the next follow apply it.
func (s *Store) finishOLHModification(ctx context.Context, idx *bucketOLHIndex, head *op.ObjectState, opTag string, skip bool, expectTag []byte, rm *olhRemoval) error {
	if skip || s.stopsAfterLink() {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	r := olhReplay{opTag: opTag, expectTag: expectTag}
	refused, err := s.replayOLH(ctx, idx, head, &r)
	if r.readAll && !r.own {
		s.olhCancelModification(ctx, head, opTag)
	}
	if errors.Is(err, radosclient.ErrCanceled) {
		err = nil
	}
	if err = errors.Join(err, refused); err != nil || rm == nil || r.readAll && r.own {
		return err
	}
	if !r.readAll {
		logged, lerr := ownEntriesLogged(ctx, idx, head, opTag)
		if lerr != nil || logged {
			return lerr
		}
	}
	return s.checkRemoval(ctx, idx, rm)
}

// ownEntriesLogged reports whether the OLH entry of head's name still holds,
// in its pending log, an entry under opTag; an entry gone holds none.
func ownEntriesLogged(ctx context.Context, idx *bucketOLHIndex, head *op.ObjectState, opTag string) (bool, error) {
	e, err := idx.getOLH(ctx, head)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("reading the olh entry of %s/%s after its replay: %w", head.Bucket.Info.Bucket.Name, head.Key.Name, err)
	}
	return logHolds(e.PendingLog, opTag), nil
}

// updateOLHAfterFailure is the best-effort update_olh after a failed link
// or unlink: "it's possible that the pending xattr from this op prevented
// the olh object from being cleaned by another thread that was deleting the
// last existing version", in set_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8727-L8735 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9473-L9481 at v20.2.4)
// and in unlink_obj_instance
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8830-L8839 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9576-L9585 at v20.2.4).
// Its failure is logged.
func (s *Store) updateOLHAfterFailure(ctx context.Context, idx *bucketOLHIndex, head *op.ObjectState) {
	refused, err := s.replayOLH(ctx, idx, head, &olhReplay{})
	if err = errors.Join(err, refused); err != nil && !errors.Is(err, radosclient.ErrCanceled) {
		slog.DebugContext(ctx, "best-effort olh update after a failed index call failed",
			slog.String("bucket", idx.x.obj.Bucket.Name), slog.String("key", head.Key.Name), slog.Any("error", err))
	}
}

// updateOLH is update_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8639-L8667 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9385-L9413 at v20.2.4):
// it replays the pending log of head, the OLH head of a name of rec's
// bucket as read without following it, as replayOLH says. It leaves *head
// as its caller read it, as update_olh leaves its caller's state, so a
// caller that needs the head as the update left it reads it again.
func (s *Store) updateOLH(ctx context.Context, rec *op.BucketRecord, head *op.ObjectState) error {
	refused, err := s.replayOLH(ctx, s.newOLHIndex(rec, head.Key), head, &olhReplay{})
	return errors.Join(err, refused)
}

// olhReplay is what a replay knows of the writer running it and what its
// last pass saw: opTag and expectTag are the writer's op tag and the write
// tag the removals logged under it must find, both empty for a reader's
// replay. readAll is set when the last pass read every page of the log, and
// own when one of those pages held an entry under opTag; applied once any
// pass's head op has landed, and raced once another writer's guard has
// refused one.
type olhReplay struct {
	opTag     string
	expectTag []byte
	readAll   bool
	own       bool
	applied   bool
	raced     bool
}

// replayOLH replays head's pending log through idx, the index of head's
// name, in passes, each over a copy of the head carrying the olh.ver it
// expects (replayPass). A pass whose apply another writer's guard refused
// with ECANCELED, having moved olh.ver or the OLH tag since the read, runs
// again from a fresh read of the head, up to olhReplayRetries, which keeps
// the progress radosgw's u64-mode olh.ver compare keeps
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8435 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9181 at v20.2.4);
// past the bound, or with the head gone, ECANCELED is returned, as radosgw
// returns it, and the pending attrs wait for the next reader. Any other
// failure is returned at once.
//
// refused is the removals refused by r's expectTag in pages whose head op
// and tail landed, each final; one whose tail did not finish comes in err.
func (s *Store) replayOLH(ctx context.Context, idx *bucketOLHIndex, head *op.ObjectState, r *olhReplay) (refused, err error) {
	cur := copyOLHHead(head)
	retries := 0
	for {
		pass, canceled, err := s.replayPass(ctx, idx, cur, r, &retries)
		refused = errors.Join(refused, pass)
		r.raced = r.raced || canceled
		if !canceled || retries >= olhReplayRetries || ctx.Err() != nil {
			return refused, err
		}
		retries++
		fresh, rerr := s.readOLHHead(ctx, idx.x.rec, head.Key)
		switch {
		case rerr != nil:
			return refused, rerr
		case !fresh.Exists:
			return refused, err
		}
		cur = fresh
	}
}

// replayPass reads cur's pending log a page at a time from its start, as
// update_olh reads it from marker 0
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8651 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9397 at v20.2.4),
// whatever epoch cur's olh.ver holds, and applies each page through
// applyOLHLog: an entry at or below that epoch is not always one an apply
// took, since the class logs a link naming the OLH entry's own epoch there,
// and starts a cleared name's entry again at epoch 2. The class answers up
// to 1000 epochs above the marker a page
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2057-L2066 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2198-L2207 at v20.2.4).
// Each page's landed head op wrote its last epoch to olh.ver, which cur then
// carries, as the next page's marker, as apply_olh_log hands update_olh its
// last epoch
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8429-L8430 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9175-L9176 at v20.2.4),
// and as the olh.ver the next apply's guard expects; the later pages' epochs
// all lie above it, so they take the links radosgw's do, whose update_olh
// hands every page the state it first read.
// An apply whose head op landed is not applied again: a tail it left is run
// again within the bound through finishOLHLog. canceled reports an apply that
// ECANCELED refused before its head op landed. A page with more to follow
// that ends with a removal of the null version no null link in it follows
// has the pages after it read first (nullLinkAfter), so that its apply drops
// that removal when a later page links null data.
func (s *Store) replayPass(ctx context.Context, idx *bucketOLHIndex, cur *op.ObjectState, r *olhReplay, retries *int) (refused error, canceled bool, err error) {
	r.readAll, r.own = false, false
	var marker uint64
	for {
		ret, err := idx.readLog(ctx, cur, marker)
		if err != nil {
			return refused, false, err
		}
		if r.opTag != "" && logHolds(ret.Log, r.opTag) {
			r.own = true
		}
		later := false
		if ret.IsTruncated && nullRemovalLeft(ret.Log) {
			if later, err = s.nullLinkAfter(ctx, idx, cur, slices.Max(slices.Collect(maps.Keys(ret.Log)))); err != nil {
				return refused, false, err
			}
		}
		last, err := s.applyOLHLog(ctx, idx, cur, ret.Log, r.opTag, r.expectTag, later)
		if last == 0 && err != nil {
			return refused, errors.Is(err, radosclient.ErrCanceled), err
		}
		if last > 0 {
			r.applied = true
			cur.Attrs[meta.AttrOLHVer] = []byte(strconv.FormatUint(last, 10))
			if err = s.rerunOLHTail(ctx, idx, cur, err, retries); err != nil && !finalRefusal(err) {
				return refused, false, err
			}
			refused = errors.Join(refused, err)
		}
		if !ret.IsTruncated || last <= marker {
			r.readAll = true
			return refused, false, nil
		}
		marker = last
	}
}

// rerunOLHTail runs again, within olhReplayRetries, the tail err names when
// an apply's head op landed and its tail did not finish, through finishOLHLog
// with the same head. It returns err when it names no tail: the refusal the
// apply returned once the tail has finished, or what the tail's last run
// left, joined with that refusal.
func (s *Store) rerunOLHTail(ctx context.Context, idx *bucketOLHIndex, cur *op.ObjectState, err error, retries *int) error {
	for {
		tail, rest := splitOLHTail(err)
		if tail == nil || *retries >= olhReplayRetries || ctx.Err() != nil {
			return err
		}
		*retries++
		err = rest
		if terr := s.finishOLHLog(ctx, idx, cur, tail.tail); terr != nil {
			err = errors.Join(terr, rest)
		}
	}
}

// splitOLHTail parts err, an apply's or a tail's answer, into the
// olhTailError it holds and everything beside it, the refusal it was joined
// with, however deep the errors that wrap the two put them.
func splitOLHTail(err error) (*olhTailError, error) {
	var tail *olhTailError
	if !errors.As(err, &tail) {
		return nil, err
	}
	return tail, besideOLHTail(err, tail)
}

// besideOLHTail is what err holds beside tail: nil for tail itself, and for
// an error wrapping tail alone; for one wrapping several errors, the join of
// what each holds beside tail; any other error, whole. The context of an
// error wrapping tail and the errors beside it is dropped, each of those
// keeping its own.
func besideOLHTail(err error, tail *olhTailError) error {
	if !errors.Is(err, tail) {
		return err
	}
	//nolint:errorlint // walking the error tree, not matching its chain
	switch u := err.(type) {
	case *olhTailError:
		if u == tail {
			return nil
		}
		return besideOLHTail(u.err, tail)
	case interface{ Unwrap() []error }:
		var rest []error
		for _, e := range u.Unwrap() {
			if r := besideOLHTail(e, tail); r != nil {
				rest = append(rest, r)
			}
		}
		return errors.Join(rest...)
	case interface{ Unwrap() error }:
		return besideOLHTail(u.Unwrap(), tail)
	}
	return nil
}

// finalRefusal reports whether err is a refusal alone: no tail left to run
// and not unsettled (olhIndex).
func finalRefusal(err error) bool {
	var tail *olhTailError
	return errors.Is(err, errRemovalRefused) && !errors.Is(err, errRefusalUnsettled) && !errors.As(err, &tail)
}

// logHolds reports whether a page of the pending log holds an entry under
// opTag.
func logHolds(log map[uint64][]rgw.OLHLogEntry, opTag string) bool {
	for _, entries := range log {
		for _, e := range entries {
			if e.OpTag == opTag {
				return true
			}
		}
	}
	return false
}

// nullDataLink reports whether e is a LINK_OLH of the null version's data,
// whose head write put that data in the OLH head (applyOLHLog).
func nullDataLink(e rgw.OLHLogEntry) bool {
	return e.Op == rgw.OLHOpLinkOLH && e.Key.Instance == "" && !e.DeleteMarker
}

// nullRemovalLeft reports whether a page of the pending log ends with a
// removal of the null version that no later link of null data in it
// follows, one a later page may follow.
func nullRemovalLeft(log map[uint64][]rgw.OLHLogEntry) bool {
	left := false
	for _, epoch := range slices.Sorted(maps.Keys(log)) {
		for _, e := range log[epoch] {
			switch {
			case e.Op == rgw.OLHOpRemoveInstance && e.Key.Instance == "":
				left = true
			case nullDataLink(e):
				left = false
			}
		}
	}
	return left
}

// nullLinkAfter reports whether cur's pending log links null data at an
// epoch above marker, reading the pages after it until one does or the log
// ends, so that a replay pairs a removal of the null version with a null link
// on any later page of it (applyOLHLog), not only on the removal's own.
func (s *Store) nullLinkAfter(ctx context.Context, idx *bucketOLHIndex, cur *op.ObjectState, marker uint64) (bool, error) {
	for {
		ret, err := idx.readLog(ctx, cur, marker)
		if err != nil {
			return false, err
		}
		next := marker
		for epoch, entries := range ret.Log {
			if slices.ContainsFunc(entries, nullDataLink) {
				return true, nil
			}
			next = max(next, epoch)
		}
		if !ret.IsTruncated || next <= marker {
			return false, nil
		}
		marker = next
	}
}

// copyOLHHead is a copy of head whose attrs the replay may change.
func copyOLHHead(head *op.ObjectState) *op.ObjectState {
	cur := *head
	cur.Attrs = maps.Clone(head.Attrs)
	if cur.Attrs == nil {
		cur.Attrs = map[string][]byte{}
	}
	return &cur
}

// olhHeadKey is the key of the OLH head of key's name: the name with no
// instance.
func olhHeadKey(key meta.ObjKey) meta.ObjKey {
	key.Instance = ""
	return key
}

// versionName names key, a version of rec's bucket, in an error.
func versionName(rec *op.BucketRecord, key meta.ObjKey) string {
	return fmt.Sprintf("%s/%s version %q", rec.Info.Bucket.Name, key.Name, key.Instance)
}

// removeInstance is the replay's removal of a version, apply_olh_log's
// delete_obj of it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8530-L8541 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9275-L9287 at v20.2.4):
// the unversioned delete of key, a version of the name, without conditions.
// Its head is read without following, as the null version's is the OLH head,
// and the removal is guarded on the write tag that read found, or on
// expectTag when it is not nil, on both releases (docs/differences.md,
// "Delete conditions are Tentacle's, and a delete is guarded, on both
// releases"); removeHead then keeps the head's OLH attrs. With keepLocked a
// version under a lock no delete passes is kept (lockHolds) and any other is
// guarded on its write tag even when the head has none, as olhIndex says,
// where radosgw's removal reads no lock. Its index calls
// carry the bilog flags apply_olh_log's delete_obj gives them, VERSIONED_OP,
// with NULL_VERSION for the null version alone, where radosgw adds
// NULL_VERSION to every removal of a replay whose caller's null_verid is set
// (docs/ceph-upstream-bugs.md, "radosgw's set_olh passes log_data_change as
// its replay's null_verid"). The class reads the flags only when it logs the
// change to the bucket index log, which needs log_op and a zone that logs
// data
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7369 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8028 at v20.2.4;
// indexOp.logOp), a zone rgw-go does not serve (docs/exclusions.md,
// "Multisite"). The removal's index completion is awaited, so it lands
// before the replay's head op and its trim or clear (completeDelSync). A
// head already gone answers NoSuchKey. A
// head whose write tag is not expectTag, as read or as the removal lands,
// keeps the version, its pending index DEL canceled, and answers
// errRemovalRefused.
func (b *bucketOLHIndex) removeInstance(ctx context.Context, key meta.ObjKey, expectTag []byte, keepLocked bool) error {
	if err := b.ofName(key); err != nil {
		return err
	}
	s, rec := b.x.s, b.x.rec
	st, err := s.readOLHHead(ctx, rec, key)
	if err != nil {
		return err
	}
	if !st.Exists {
		return fmt.Errorf("%w: the version has no head", op.ErrNoSuchKey)
	}
	w := radosclient.NewWriteOp()
	switch {
	case expectTag != nil:
		if !bytes.Equal(st.Attrs[meta.AttrIDTag], expectTag) {
			return errRemovalRefused
		}
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, expectTag)
	case keepLocked:
		if s.lockHolds(st) {
			return fmt.Errorf("%s: %w", versionName(rec, key), errRemovalLocked)
		}
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	case st.WriteTag != "":
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	flags := rgw.BILogFlagVersionedOp
	if key.Instance == "" {
		flags |= rgw.BILogNullVersion
	}
	err = s.removeHeadFlagged(ctx, rec, key, "", st, w, true, headRemoval{bilogFlags: flags, await: true})
	if expectTag != nil && errors.Is(err, radosclient.ErrCanceled) {
		return fmt.Errorf("the version's head changed as it was removed: %w", errRemovalRefused)
	}
	return err
}

// removeFailedVersion removes key, a version of rec's bucket a versioned
// write made and could not link, once cause, the write's failure, proves
// that no link of it landed: a class's or the OSD's answer, or a refusal
// made before anything was sent. A timeout, a context that ended or a closed
// connection proves nothing, since the op may still land, and removes
// nothing: a version linked after its removal would be listed with no head.
// The null version is left, its head being the name's OLH head, and so is a
// version whose removal fails, as radosgw leaves every such version for
// bucket check (docs/differences.md, "rgw-go removes the instance a failed
// versioned write made").
func (s *Store) removeFailedVersion(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, cause error) {
	if outcomeUnknown(cause) {
		return
	}
	s.removeUnlinkedVersion(ctx, rec, key, cause)
}

// removeUnlinkedVersion is removeFailedVersion for a version no link of
// which can land, whatever cause says.
func (s *Store) removeUnlinkedVersion(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, cause error) {
	if key.Instance == "" {
		return
	}
	err := s.newOLHIndex(rec, key).removeInstance(ctx, key, nil, false)
	if err != nil && !errors.Is(err, op.ErrNoSuchKey) {
		slog.WarnContext(ctx, "removing a version whose link failed failed; it stays for bucket check",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.String("instance", key.Instance),
			slog.Any("cause", cause), slog.Any("error", err))
	}
}

// lockHolds reports whether st, a version's head as read, carries a lock no
// delete passes, the governance bypass included: a legal hold ON, a
// COMPLIANCE retention whose date is still ahead, or a lock attr that does
// not decode. A GOVERNANCE retention keeps nothing because the pending log
// does not record whether the delete that logged the removal was authorized
// with the bypass: a replay cannot tell a removal that bypassed it from one
// that raced it, and keeping it would leave every bypassing delete another
// replay applies first a version no one removes
// (docs/rados-format-opportunities.md, "The OLH log's removal entry: no
// record of a GOVERNANCE bypass"). The version's own attrs decide, not its
// bucket's flag: a request may hold a bucket record read before object lock
// was turned on, and a lock attr is written only once it is.
func (s *Store) lockHolds(st *op.ObjectState) bool {
	return objectlock.VerifyDelete(st.Attrs, s.now(), true, true) != nil
}

// olhRemoval is the version a link or unlink logged for removal: key, with
// an empty instance for the null version, tag, the write tag its removal is
// guarded on, nil for none, and guarded, set when the writer holds the
// removal to tag, as a delete given DeleteParams.Expect does. A writer that
// is not guarded removes the version whatever it carries, as an admin
// removal does, and for the null version only the data its op saw, tag
// being the OLH head's write tag then.
type olhRemoval struct {
	key     meta.ObjKey
	tag     []byte
	guarded bool
}

// loggedRemoval is the olhRemoval of key, a version a link or unlink logged
// for removal, head being the OLH head as the attempt's init left it and
// expect the write tag the writer holds the removal to, nil for none.
func loggedRemoval(key meta.ObjKey, head *op.ObjectState, expect []byte) *olhRemoval {
	rm := &olhRemoval{key: key, tag: expect, guarded: expect != nil}
	if expect == nil && key.Instance == "" {
		rm.tag = expectTag(head)
	}
	return rm
}

// holdsNullData reports whether st, a key's OLH head as read, holds the null
// version's data, as its ETag shows: the null version's removal leaves the
// head its OLH attrs alone (removeHeadFlagged).
func holdsNullData(st *op.ObjectState) bool {
	_, ok := st.Attrs[meta.AttrETag]
	return st.Exists && ok
}

// unlistedVersion reports whether st, the head of key's version as read,
// the OLH head for the null version, whose data it must hold, is a version
// whose record no listing shows: one an unlink, or a null delete marker's
// link, took off the listing and no replay removed. A version listed is one
// a writer's link listed again since; for the null version, a later null
// write's, which its link lists, the replay having dropped the removal it
// follows (applyOLHLog).
func (s *Store) unlistedVersion(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, st *op.ObjectState) (bool, error) {
	read := s.getInstanceEntry
	if key.Instance == "" {
		if !holdsNullData(st) {
			return false, nil
		}
		read = s.nullDataEntry
	}
	en, err := read(ctx, rec, key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	listed, err := s.listed(ctx, rec, key, en)
	return !listed && err == nil, err
}

// checkRemoval finishes rm, the removal a writer's link or unlink logged,
// once the writer's replay has found the writer's entries applied by another
// replay, which keeps a version under a lock no delete passes (applyOLHLog).
// The version's head is read again, the OLH head for the null version, and a
// version still stored and still unlisted (unlistedVersion) is removed
// through idx:
//   - guarded, under rm.tag, the write tag the writer's check read, whose
//     refusal is final, as its own replay's is (refusedRemoval);
//   - otherwise only when lockHolds, the one case another replay keeps a
//     version, and the null version's data only while it carries rm.tag, so
//     that null data written since stays.
//
// A null version so removed whose name's OLH entry is left with no version
// has its OLH cleared, as the replay that kept it would have
// (clearVersionlessOLH).
func (s *Store) checkRemoval(ctx context.Context, idx *bucketOLHIndex, rm *olhRemoval) error {
	rec := idx.x.rec
	what := versionName(rec, rm.key)
	st, err := s.readOLHHead(ctx, rec, rm.key)
	if err != nil {
		return fmt.Errorf("reading the head of %s again after another replay: %w", what, err)
	}
	if !st.Exists {
		return nil
	}
	kept, kerr := s.unlistedVersion(ctx, rec, rm.key, st)
	switch {
	case kerr != nil:
		return fmt.Errorf("reading the index entry of %s again after another replay: %w", what, kerr)
	case !kept:
		return nil
	}
	if !rm.guarded && !s.lockHolds(st) {
		return nil
	}
	err = idx.removeInstance(ctx, rm.key, rm.tag, false)
	switch {
	case errors.Is(err, radosclient.ErrNotFound), errors.Is(err, op.ErrNoSuchKey):
		return nil
	case errors.Is(err, errRemovalRefused) && rm.guarded:
		return fmt.Errorf("removing %s after another replay kept it: %w", what, err)
	case errors.Is(err, errRemovalRefused):
		return nil
	case err != nil:
		return fmt.Errorf("removing %s after another replay kept it: %w", what, err)
	case rm.key.Instance == "":
		return s.clearVersionlessOLH(ctx, idx, rm.key)
	}
	return nil
}

// clearVersionlessOLH clears the OLH of key's name, as a replay's tail does
// (clearOLHHead), when its index entry holds no version. The head is read
// before the entry and the clear guarded on the head as read, its OLH tag,
// its olh.ver and no pending attr, so a writer whose init lands after the
// read refuses it, as does one whose link landed before it, whose pending
// attr the read finds or whose version the entry holds.
func (s *Store) clearVersionlessOLH(ctx context.Context, idx *bucketOLHIndex, key meta.ObjKey) error {
	head, err := s.readOLHHead(ctx, idx.x.rec, olhHeadKey(key))
	if err != nil || !head.Exists {
		return err
	}
	e, err := idx.getOLH(ctx, head)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("reading the olh entry of %s/%s: %w", idx.x.rec.Info.Bucket.Name, key.Name, err)
	case e.Exists:
		return nil
	}
	ver, perr := strconv.ParseUint(string(head.Attrs[meta.AttrOLHVer]), 10, 64)
	if perr != nil {
		slog.DebugContext(ctx, "a versionless olh whose olh.ver no apply wrote is left for its next update",
			slog.String("bucket", idx.x.rec.Info.Bucket.Name), slog.String("key", key.Name), slog.Any("error", perr))
		return nil
	}
	if err = s.clearOLHHead(ctx, idx, head, ver); errors.Is(err, radosclient.ErrCanceled) {
		return nil
	}
	return err
}

// outcomeUnknown reports whether err leaves unknown whether the op it ended
// landed: a timeout, a context that ended, or a closed connection, any of
// which may let the op land later.
func outcomeUnknown(err error) bool {
	return errors.Is(err, radosclient.ErrTimedOut) || errors.Is(err, radosclient.ErrClosed) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
