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
	"sync"
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
// unlisted (delete_versioned.go), or a version a lifecycle transition
// rewrote (linkTransition, keepCurrent): no failure removes the version,
// which the link did not write. head, when not nil, is the OLH head as the caller read
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
// its wait. olh, when not nil, is the name's OLH entry as the caller read it
// (relinkOLHEntry), which the link's op compares unchanged, a missing entry
// as empty: another writer's link or unlink of the name rewrites that entry,
// and one landing between the read and the link, which the head's guard does
// not see, refuses the link, which then answers errRelinkMoved. finish is set
// for a link whose context a cap can end between two of its writes, a
// relist's (refusedRemoval): an attempt whose init is begun before the cap
// sends that init, then its link, or after a link that did not land the
// cancel of the init's pending attr, whatever the cap does, under the
// relist's backstop (pairContext). An attempt cut between the two would
// leave the pending attr on the head until it expires, with nothing sent
// under it.
type olhRelink struct {
	head      *op.ObjectState
	pendingOK bool
	olh       *rgw.BIEntry
	finish    bool
}

// errRelinkMoved ends a link whose olhRelink head, or OLH entry, another
// writer has moved.
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
// the null delete marker the unlink must find (olhMarkerCmp). NoMarker, for
// an unlink of the null version's data, has the unlink's op compare the null
// delete marker's instance entry as absent, as a missing omap key reads
// empty, so a marker linked since its caller read the data refuses it,
// answering errMarkerLinked: the class would unlink that marker in the
// data's place.
type olhUnlink struct {
	Epoch       uint64
	NullVersion bool
	SkipUpdate  bool
	ExpectTag   []byte
	Marker      bool
	MarkerCmp   *olhMarkerCmp
	NoMarker    bool
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

// errMarkerLinked ends an unlink of the null version's data over which a
// null delete marker was linked since its caller read the data
// (olhUnlink.NoMarker).
var errMarkerLinked = fmt.Errorf("%w: a null delete marker was linked over the null data since it was read", op.ErrConcurrentModification)

// nullMarkerLinked reports, after an unlink of key's null data that its
// compare of the null delete marker as absent refused, whether key's null
// version now resolves to a delete marker: errMarkerLinked when it does, nil
// when it does not, another guard having refused the unlink, or when the
// name holds no null version, which the next attempt's unlink answers.
func (s *Store) nullMarkerLinked(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) error {
	en, err := s.getInstanceEntry(ctx, rec, key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return nil
	case err != nil:
		return err
	case en.Flags&rgw.FlagDeleteMarker != 0:
		return errMarkerLinked
	}
	return nil
}

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
//     attempt round again to evaluate the condition anew, and the link's own
//     replay removes the null version's data the link logged only under it,
//     so that replay keeps a null write that reaches the head after the init.
//     Another request's replay of the log applies the removal as logged
//     (docs/differences.md, "A versioned write whose version a delete
//     removes before its link succeeds").
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
// any path, and with its finish set an attempt begun before a cap on ctx
// sends its init, its link, and the cancel after a link that did not land,
// whatever that cap does (olhRelink).
func (s *Store) setOLH(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p olhLink) error {
	ctx = withRemovalDeadline(ctx)
	what := versionName(rec, key)
	idx := s.newOLHIndex(rec, key)
	f := olhLinkFields{deleteMarker: p.DeleteMarker, meta: p.Meta, epoch: p.Epoch, unmodSince: p.UnmodSince, highPrecision: p.HighPrecision}
	if p.Relink != nil {
		f.cmp = p.Relink.olh
	}
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
	// after is the context of an attempt's init and of the writes that
	// follow it.
	after, done := ctx, func() {}
	if p.Relink != nil && p.Relink.finish {
		after, done = pairContext(ctx)
	}
	defer done()
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
			if p.Relink != nil && p.Relink.finish && ctx.Err() != nil {
				return beforeInit(ctx.Err())
			}
			opTag, _, err = s.olhInitModification(after, head, a.guard, expect)
			switch {
			case errors.Is(err, radosclient.ErrCanceled), found && errors.Is(err, radosclient.ErrNotFound):
				continue
			case err != nil:
				return unlinked(fmt.Errorf("linking %s: %w", what, err))
			}
		}
		err = idx.linkOLH(after, head, key, opTag, f)
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
			s.olhCancelModification(after, head, opTag)
		}
		if errors.Is(err, radosclient.ErrCanceled) {
			if rerr := s.repairOLH(ctx, idx, head); rerr != nil && !errors.Is(rerr, radosclient.ErrCanceled) {
				return unlinked(fmt.Errorf("linking %s: %w", what, rerr))
			}
			if f.cmp != nil {
				return fmt.Errorf("linking %s: %w", what, errRelinkMoved)
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
// is tried again, within the attempts, with the bytes read now. With
// p.NoMarker the op compares that entry as absent, and a refusal reads the
// null version again: a marker is errMarkerLinked with nothing unlinked, and
// anything else is tried again. An unlink that carries either compare and
// runs out of attempts answers ConcurrentModification, nothing unlinked,
// where one without them answers EIO, as unlink_obj_instance does.
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
	ctx = withRemovalDeadline(ctx)
	what := versionName(rec, key)
	idx := s.newOLHIndex(rec, key)
	f := olhUnlinkFields{epoch: p.Epoch, nullVersion: p.NullVersion}
	if p.NoMarker {
		f.cmp = &rgw.BIEntry{Type: rgw.BIIndexInstance, Idx: rgw.InstanceIndexKey(versionKey(olhHeadKey(key)), true)}
	}
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
			if p.NoMarker {
				if err = s.nullMarkerLinked(ctx, rec, key); err != nil {
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
	if p.NoMarker || p.MarkerCmp != nil {
		return unlinkNotSent, nil, fmt.Errorf("%w: unlinking %s: every one of %d attempts was refused, its compare of the null delete marker among the guards", op.ErrConcurrentModification, what, olhAttempts)
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
	refused, err := s.replayOLH(withRemovalDeadline(ctx), s.newOLHIndex(rec, head.Key), head, &olhReplay{})
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
// returns it, and the pending attrs wait for the next reader. Past the bound
// with the head still there, the ECANCELED also carries errReplayOvertaken,
// which a read answers as SlowDown (followOLH): the OLH was not removed, as
// the ECANCELED of a head gone says. A pass one of
// whose removals another replay had carried out and trimmed from the log
// since the read (errRemovalLeftLog) ends there too, and runs again from a
// fresh read within the same bound, so that no prepare is sent from a log
// another replay has overtaken. A pass that
// stops before its head op on a removal deferred to the next replay
// (errRemovalDeferred) ends the replay with no error: its log and pending
// attrs stay for that replay, and its caller answers as for a replay another
// writer's has overtaken, where radosgw's removal, comparing nothing, goes
// on. Any other failure is returned at once.
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
		if errors.Is(err, errRemovalDeferred) {
			slog.DebugContext(ctx, "a replay leaves its log to the next one, a removal's prepares having been refused until their bound",
				slog.String("bucket", idx.x.obj.Bucket.Name), slog.String("key", head.Key.Name), slog.Any("error", err))
			return refused, nil
		}
		if !canceled || ctx.Err() != nil {
			return refused, err
		}
		fresh, rerr := s.readOLHHead(ctx, idx.x.rec, head.Key)
		switch {
		case rerr != nil:
			return refused, rerr
		case !fresh.Exists:
			return refused, err
		case retries >= olhReplayRetries:
			return refused, fmt.Errorf("%w: %w", errReplayOvertaken, err)
		}
		retries++
		cur = fresh
	}
}

// errReplayOvertaken ends a replay that ran out of passes, each refused by
// another writer's guard or overtaken by another replay's trim (replayOLH).
var errReplayOvertaken = errors.New("other writers overtook every pass of the replay")

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
// that holds a removal no link of its version's data in it follows has the
// rest of the log read first, from where the pass last read ahead
// (linkedLater), so that its apply drops that removal when a later page links
// that version's data, one logged during the pass included.
func (s *Store) replayPass(ctx context.Context, idx *bucketOLHIndex, cur *op.ObjectState, r *olhReplay, retries *int) (refused error, canceled bool, err error) {
	r.readAll, r.own = false, false
	var marker uint64
	var ahead readAhead
	for {
		ret, err := idx.readLog(ctx, cur, marker)
		if err != nil {
			return refused, false, err
		}
		if r.opTag != "" && logHolds(ret.Log, r.opTag) {
			r.own = true
		}
		later, err := s.linkedLater(ctx, idx, cur, ret, &ahead)
		if err != nil {
			return refused, false, err
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

// dataLink reports whether e is a LINK_OLH of a version's data, not of a
// delete marker (applyOLHLog).
func dataLink(e rgw.OLHLogEntry) bool {
	return e.Op == rgw.OLHOpLinkOLH && !e.DeleteMarker
}

// unpairedRemovals is the versions, by instance, the null version's empty,
// that a page of the pending log names for removal with no later link of
// their data in it, whose removal a later page's link may still drop.
func unpairedRemovals(log map[uint64][]rgw.OLHLogEntry) map[string]bool {
	left := map[string]bool{}
	for _, epoch := range slices.Sorted(maps.Keys(log)) {
		for _, e := range log[epoch] {
			switch {
			case e.Op == rgw.OLHOpRemoveInstance:
				left[e.Key.Instance] = true
			case dataLink(e):
				delete(left, e.Key.Instance)
			}
		}
	}
	return left
}

// readAhead is what a replay pass has read of the pending log past its
// pages: the last epoch of each data link there, by instance, and the last
// epoch read, 0 before the first read (dataLinksAfter).
type readAhead struct {
	links map[string]uint64
	last  uint64
}

// linkedLater is the versions, by instance, that page ret of cur's pending
// log names for removal and a link of whose data a later page holds: none
// for a page the log ends with. Each page that needs it reads ahead from
// where the pass's last read ahead ended, its last epoch included, or from
// its own last epoch when that is later, into ahead: so a link the class
// logged since an earlier page read ahead, at the epoch that read ended on
// or above it, still pairs this page's removal, and the log is read once a
// pass, with one page more for each later page that needs it.
func (s *Store) linkedLater(ctx context.Context, idx *bucketOLHIndex, cur *op.ObjectState, ret rgw.ReadOLHLogRet, ahead *readAhead) (map[string]bool, error) {
	if !ret.IsTruncated {
		return nil, nil
	}
	left := unpairedRemovals(ret.Log)
	if len(left) == 0 {
		return nil, nil
	}
	top := slices.Max(slices.Collect(maps.Keys(ret.Log)))
	from := top
	if ahead.last > top {
		from = ahead.last - 1
	}
	if err := s.dataLinksAfter(ctx, idx, cur, from, ahead); err != nil {
		return nil, err
	}
	later := map[string]bool{}
	for instance := range left {
		if ahead.links[instance] > top {
			later[instance] = true
		}
	}
	return later, nil
}

// dataLinksAfter reads cur's pending log above marker to its end into
// ahead: for each version a link of whose data it holds, by instance, the
// last epoch of such a link, and the last epoch read, so that a replay pairs
// a version's removal with a link on any later page of the log
// (applyOLHLog), not only on the removal's own.
func (s *Store) dataLinksAfter(ctx context.Context, idx *bucketOLHIndex, cur *op.ObjectState, marker uint64, ahead *readAhead) error {
	if ahead.links == nil {
		ahead.links = map[string]uint64{}
	}
	for {
		ret, err := idx.readLog(ctx, cur, marker)
		if err != nil {
			return err
		}
		next := marker
		for epoch, entries := range ret.Log {
			for _, e := range entries {
				if dataLink(e) {
					ahead.links[e.Key.Instance] = max(ahead.links[e.Key.Instance], epoch)
				}
			}
			next = max(next, epoch)
		}
		ahead.last = max(ahead.last, next)
		if !ret.IsTruncated || next <= marker {
			return nil
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
//
// The removal goes on only while the version is unlisted. The unlink that
// logged it took the version's list entry, and only a link writes one again,
// rewriting the instance entry at the link's epoch and flags as it does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1410-L1427 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1537-L1554 at v20.2.4),
// whether it lands above the OLH entry's epoch and logs a LINK_OLH or below
// it and logs nothing. So the prepare, the removal's first index op,
// compares the version's instance entry with the bytes read just before it
// (removalCmps) and the list entry that entry names as absent, and a link
// landed since the replay read the log answers errRemovalRelisted with
// nothing removed, the version kept as a later link in the log keeps it
// (applyOLHLog). A prepare those compares refuse reads the entries again: a
// version listed is kept, and any other is tried again from a fresh read of
// its head, up to olhAttempts. radosgw's removal compares neither entry and
// removes the version whoever links it after the replay read the log
// (docs/differences.md, "A replay keeps a version a link lists again after it
// read the log"). A link landing after the prepare and before the removal's
// completion finds the instance entry still there, and the completion then
// removes it from under the list entry the link wrote.
func (b *bucketOLHIndex) removeInstance(ctx context.Context, key meta.ObjKey, expectTag []byte, keepLocked bool) error {
	return b.removeVersion(ctx, key, expectTag, keepLocked, true)
}

// recordUnlisted records keys, versions of head's name a replay leaves
// unlisted, in the bucket's unlisted-version object (Store.recordUnlisted),
// under head's OLH tag, the one the replay read the log under.
func (b *bucketOLHIndex) recordUnlisted(ctx context.Context, head *op.ObjectState, keys []meta.ObjKey) error {
	return b.x.s.recordUnlisted(ctx, b.x.rec, keys, head.Attrs[meta.AttrOLHIDTag])
}

// pruneUnlisted prunes the bucket's unlisted-version object after a replay
// recorded keys (Store.pruneUnlisted).
func (b *bucketOLHIndex) pruneUnlisted(ctx context.Context, keys []meta.ObjKey) {
	b.x.s.pruneUnlisted(ctx, b.x.rec, keys)
}

// removeVersion is removeInstance, logged set when the removal is a
// replay's of an entry of the name's OLH log. A prepare the compares refuse,
// on a version still unlisted, is followed by a relist's backoff
// (relistBackoff's), and the rounds end at olhAttempts or at the request's
// removalDeadline, whichever comes first, with errRemovalDeferred: nothing is
// removed, and a logged removal stays in the log for the next replay. A round
// after the backoff goes on only while there is a removal to carry out: for a
// logged removal, while the name's OLH log, read just before the round's head
// read and prepare, still holds a removal of the version that no later link
// of its data drops (logsRemoval). Once another replay has applied, refused
// or kept that removal and trimmed it, the backoff included, the removal
// answers errRemovalLeftLog with nothing removed, which ends the replay's
// pass and sends it back to a fresh read of the log (applyOLHLog): a prepare
// sent then would follow a relist's give-way that found the log without the
// removal, and land in the window above.
func (b *bucketOLHIndex) removeVersion(ctx context.Context, key meta.ObjKey, expectTag []byte, keepLocked, logged bool) error {
	if err := b.ofName(key); err != nil {
		return err
	}
	s, rec := b.x.s, b.x.rec
	lost := 0
	deadline := removalDeadlineOf(ctx)
	for {
		if lost > 0 && logged {
			held, herr := b.logsRemoval(ctx, key)
			switch {
			case herr != nil:
				return fmt.Errorf("reading the olh log of %s after its removal was refused: %w", versionName(rec, key), herr)
			case !held:
				return fmt.Errorf("%s: %w", versionName(rec, key), errRemovalLeftLog)
			}
		}
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
		cmps, err := b.removalCmps(ctx, key)
		if err != nil {
			return err
		}
		flags := rgw.BILogFlagVersionedOp
		if key.Instance == "" {
			flags |= rgw.BILogNullVersion
		}
		err = s.removeHeadFlagged(ctx, rec, key, "", st, w, true, headRemoval{bilogFlags: flags, await: true, cmps: cmps})
		switch {
		case errors.Is(err, errRemovalEntryMoved):
			listed, lerr := b.dataListed(ctx, key)
			switch {
			case lerr != nil:
				return fmt.Errorf("reading the index entries of %s again after its removal was refused: %w", versionName(rec, key), lerr)
			case listed:
				return fmt.Errorf("%s: %w", versionName(rec, key), errRemovalRelisted)
			}
			lost++
			now := s.now()
			if lost >= olhAttempts || now.After(deadline.from(now)) {
				return fmt.Errorf("%w: removing %s: %d prepares found its index entries changed", errRemovalDeferred, versionName(rec, key), lost)
			}
			if berr := s.condBackoff(ctx, olhCondBackoff(min(lost-1, olhCondBackoffs))); berr != nil {
				return fmt.Errorf("%w: removing %s: %w", errRemovalDeferred, versionName(rec, key), berr)
			}
			continue
		case expectTag != nil && errors.Is(err, radosclient.ErrCanceled):
			return fmt.Errorf("the version's head changed as it was removed: %w", errRemovalRefused)
		}
		return err
	}
}

// removalDeadline bounds the backoffs of every removal a request makes
// (removeVersion), in every pass of every replay it runs and in its
// checkRemoval: relistCap after the first round any of them lost, unset
// until then. So the request backs off for relistCap at most, and one more
// backoff, however many removals and passes it runs. A request is one call
// of the store that writes, deletes or reads a versioned key, which carries
// its deadline in its context from the first of its OLH operations that
// takes one (withRemovalDeadline); a removal with none in its context has
// its own.
type removalDeadline struct {
	mu sync.Mutex
	at time.Time
}

// removalDeadlineKey is the context key of a request's removalDeadline.
type removalDeadlineKey struct{}

// withRemovalDeadline is ctx carrying a removalDeadline, a new one unless
// ctx carries one already.
func withRemovalDeadline(ctx context.Context) context.Context {
	if _, ok := ctx.Value(removalDeadlineKey{}).(*removalDeadline); ok {
		return ctx
	}
	return context.WithValue(ctx, removalDeadlineKey{}, &removalDeadline{})
}

// removalDeadlineOf is the removalDeadline ctx carries, or a new one.
func removalDeadlineOf(ctx context.Context) *removalDeadline {
	if d, ok := ctx.Value(removalDeadlineKey{}).(*removalDeadline); ok {
		return d
	}
	return &removalDeadline{}
}

// from is the deadline, which a round lost at now sets when it is the
// request's first.
func (d *removalDeadline) from(now time.Time) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.at.IsZero() {
		d.at = now.Add(relistCap)
	}
	return d.at
}

// logsRemoval reports whether the OLH log of key's name holds a removal of
// key's version that no later link of its data drops, one a replay has still
// to carry out (unpairedRemovals); a name without an OLH entry holds none.
func (b *bucketOLHIndex) logsRemoval(ctx context.Context, key meta.ObjKey) (bool, error) {
	e, err := b.getOLH(ctx, &op.ObjectState{Bucket: b.x.rec, Key: olhHeadKey(key)})
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return unpairedRemovals(e.PendingLog)[key.Instance], nil
}

// errRemovalLeftLog ends a replay's removal that a refused prepare would
// have sent round again once the OLH log no longer held it (removeVersion).
var errRemovalLeftLog = errors.New("another replay has carried out the removal, which has left the log")

// errRemovalDeferred ends a removal whose prepares other writers' changes to
// the version's index entries kept refusing until its bound (removeVersion):
// it removed nothing, and a logged removal stays in the log.
var errRemovalDeferred = errors.New("the version's index entries kept changing under its removal, which is left to the next replay")

// errRemovalEntryMoved ends a removal whose prepare the compares of the
// version's index entries refused (headRemoval.cmps).
var errRemovalEntryMoved = errors.New("the version's index entries changed since the removal read them")

// dataEntry is the index entry of key's version of data, as the class's link
// and unlink read it, with the raw entry bi_get answered: a named version's
// instance entry, or the null version's data entry, the plain key's or,
// behind the version marker, the null data key's, never the null delete
// marker's (nullDataEntry).
func (b *bucketOLHIndex) dataEntry(ctx context.Context, key meta.ObjKey) (rgw.DirEntry, rgw.BIEntry, error) {
	x, k := b.x, versionKey(key)
	var en rgw.DirEntry
	var bi rgw.BIEntry
	err := x.readShard(ctx, func(sh indexShard) error {
		var err error
		if en, bi, err = x.dirEntryBI(ctx, sh, k, rgw.BIIndexInstance); err != nil || k.Instance != "" || en.Flags&rgw.FlagVerMarker == 0 {
			return err
		}
		en, bi, err = x.dirEntryBI(ctx, sh, rgw.ObjKey{Name: rgw.InstanceIndexKey(k, false)}, rgw.BIIndexPlain)
		return err
	})
	return en, bi, err
}

// listsData reports whether en, a version's data entry, names a list entry:
// it carries FLAG_VER, which only a link and a conversion of a plain entry
// write, and is no delete marker, whose entry a data removal does not take.
func listsData(en rgw.DirEntry) bool {
	return en.Flags&rgw.FlagVer != 0 && en.Flags&rgw.FlagDeleteMarker == 0
}

// removalCmps is what the prepare of key's removal compares: the version's
// data entry as dataEntry reads it now and, when that entry names a list
// entry, the list entry the class keys at its versioned_epoch
// (get_list_index_key) as absent. A version with no data entry, or whose
// entry is a delete marker's, gets no compare. A link of its data needs the
// entry, so none lands before the prepare; but the prepare itself writes a
// placeholder entry, not existing, under its pending tag
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L847-L875 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L979-L1005 at v20.2.4),
// which a link landing after it finds and lists, as it finds any entry in the
// window between a removal's prepare and its completion.
func (b *bucketOLHIndex) removalCmps(ctx context.Context, key meta.ObjKey) ([]rgw.BIEntry, error) {
	en, bi, err := b.dataEntry(ctx, key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading the index entry of %s before its removal: %w", versionName(b.x.rec, key), err)
	case en.Flags&rgw.FlagDeleteMarker != 0:
		return nil, nil
	}
	cmps := []rgw.BIEntry{bi}
	if listsData(en) {
		cmps = append(cmps, rgw.BIEntry{Type: rgw.BIIndexPlain, Idx: rgw.ListIndexKey(en.Key, en.VersionedEpoch)})
	}
	return cmps, nil
}

// dataListed reports whether key's version of data is listed: its data entry
// names a list entry and that entry is there.
func (b *bucketOLHIndex) dataListed(ctx context.Context, key meta.ObjKey) (bool, error) {
	en, _, err := b.dataEntry(ctx, key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	case !listsData(en):
		return false, nil
	}
	return b.x.s.listed(ctx, b.x.rec, key, en)
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
	err := s.newOLHIndex(rec, key).removeVersion(ctx, key, nil, false, false)
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
// a writer's link listed again since, the replay having dropped the removal
// that link follows (applyOLHLog).
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
// (clearVersionlessOLH). A removal whose prepares other writers kept
// refusing until their bound (errRemovalDeferred) answers SlowDown, 503,
// where main answered UnknownError, 500: no log entry is left for a later
// replay to remove the version, so the delete must not be acknowledged, and
// the client's retry of it is what removes the version.
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
	err = idx.removeVersion(ctx, rm.key, rm.tag, false, false)
	switch {
	case errors.Is(err, radosclient.ErrNotFound), errors.Is(err, op.ErrNoSuchKey), errors.Is(err, errRemovalRelisted):
		return nil
	case errors.Is(err, errRemovalDeferred):
		return fmt.Errorf("%w: removing %s after another replay kept it: %w", op.ErrSlowDown, what, err)
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
