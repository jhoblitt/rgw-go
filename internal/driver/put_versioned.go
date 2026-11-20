package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A versioned write is _do_write_meta's head write followed by set_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3312-L3375 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3433-L3496 at v20.2.4):
// the version's head at its instance's oid, or for the null version the
// key's own head, its index entry, then its link under the key's OLH. rgw-go
// awaits the entry's completion before the link, and a conditional write
// holds its condition through the link (docs/differences.md, "A versioned
// write awaits its index completion" and "A conditional write to a versioned
// bucket commits only if its condition held when it linked").

// isOLHHead reports whether st is an OLH head, one carrying olh.ver, as
// get_obj_state_impl sets is_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6226-L6230 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6840-L6844 at v20.2.4),
// and the head of a key without an instance: a version's own head carrying
// olh.ver holds attrs radosgw's copy of a null version left on it, of no OLH.
func isOLHHead(st *op.ObjectState) bool {
	_, ok := st.Attrs[meta.AttrOLHVer]
	return ok && st.Exists && (st.Key.Instance == "" || st.Key.Instance == meta.NullInstance)
}

// versionedOp reports whether hw, over st, its head as the pass read it or
// as missing on the assume-no-entry pass, is _do_write_meta's versioned_op:
// the bucket's versioning enabled, the head an OLH head, or a versioned
// target, a key with an instance or a nonzero epoch. A write without an
// epoch is never one: it keeps the unversioned head write.
func versionedOp(hw *headWrite, st *op.ObjectState) bool {
	if hw.olhEpoch == nil {
		return false
	}
	return hw.rec.Info.VersioningEnabled() || isOLHHead(st) || *hw.olhEpoch > 0 || hw.key.Instance != ""
}

// readWriteHead is the read of the head a write's guarded pass writes over:
// readHead, which refuses an OLH head, but for a write with an epoch, whose
// head may be one, the null version's.
func (s *Store) readWriteHead(ctx context.Context, hw *headWrite) (*op.ObjectState, error) {
	if hw.olhEpoch == nil {
		return s.readHead(ctx, hw.rec, hw.key)
	}
	return s.readOLHHead(ctx, hw.rec, hw.key)
}

// condState is the state a write's conditions are checked on before its
// head write, st being the head as read. For a versioned op on a key with
// an instance it is the version current on the key's OLH, followed as
// StatObject follows it, a current delete marker a state that does not
// exist: radosgw's get_current_version_state
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3187-L3197 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3284-L3294 at v20.2.4),
// but on the version current after any replay the follow runs. Otherwise it
// is st: a write over an OLH head without an instance is
// writeNullVersion's.
func (s *Store) condState(ctx context.Context, hw *headWrite, st *op.ObjectState) (*op.ObjectState, error) {
	if hw.ifMatch == "" && hw.ifNoneMatch == "" || !versionedOp(hw, st) || hw.key.Instance == "" {
		return st, nil
	}
	return s.objectState(ctx, hw.rec, olhHeadKey(hw.key), false)
}

// errOLHGuardRefused ends a pass of writeNullVersion whose head write the
// guards of the init it carries refused.
var errOLHGuardRefused = errors.New("driver: the key's OLH changed after the write's condition was checked")

// nullDataWrite reports whether hw over st, its head as read, is a write of
// the null version's data over the key's OLH head, a versioned op whose
// head write carries the init of its modification (nullInit).
func nullDataWrite(hw *headWrite, st *op.ObjectState) bool {
	return hw.olhEpoch != nil && hw.key.Instance == "" && isOLHHead(st)
}

// nullCondWrite reports whether hw over st is a conditional write of the
// null version's data over the key's OLH head, which writeNullVersion makes.
func nullCondWrite(hw *headWrite, st *op.ObjectState) bool {
	return nullDataWrite(hw, st) && (hw.ifMatch != "" || hw.ifNoneMatch != "")
}

// nullInit is the init of a modification that a write of the null
// version's data over the key's OLH head sends in its head write's op, the
// op olhInitModification sends on an existing head, so that its pending attr
// stands from its head write to its link's replay: there a conditional
// writer's attempt, a conditional delete marker's lifecycle's included,
// waits on it (waitOLHPending) and then evaluates its condition on the
// version the write made current, where radosgw's writer inits only after
// its index completion
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3375 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8694-L8706 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3490-L3496 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9440-L9452 at v20.2.4).
// The state it writes is radosgw's init's, a moment sooner. guard is a
// conditional write's init's guard, on the olh.info the condition's check
// read and, while the wait allows it, on no pending attr; an unconditional
// write's is nil, its init guarded on the OLH tag alone, as radosgw's is.
// opTag and pending are the modification's op tag and its pending attr's
// value; olhTag is the OLH tag a head without one is given, nil for a head
// with one; and left is the OLH head as the op leaves it, which a cancel of
// the modification is guarded on and setOLH links under (olhLink.Inited).
type nullInit struct {
	guard   *olhCondGuard
	opTag   string
	pending []byte
	olhTag  []byte
	left    *op.ObjectState
}

// newNullInit is the init a null write's head write over head, the OLH head
// as the attempt read or followed it, carries, under guard. A head
// without an OLH tag gets olhInitModification's fresh one, olh.info naming
// itself and an empty olh.ver, as a plain object becoming versioned does.
func (s *Store) newNullInit(head *op.ObjectState, guard *olhCondGuard) *nullInit {
	now := s.now()
	in := &nullInit{guard: guard, opTag: meta.NewOLHOpTag(now), pending: encodeAt(meta.OLHPendingInfo{Time: now}, s.release)}
	left := copyOLHHead(head)
	if !olhTagged(head) {
		in.olhTag = []byte(meta.NewOLHTag())
		left.Attrs[meta.AttrOLHIDTag] = in.olhTag
		left.Attrs[meta.AttrOLHInfo] = olhSelfInfo(head, s.release)
		left.Attrs[meta.AttrOLHVer] = []byte{}
	}
	left.Attrs[meta.AttrOLHPendingPrefix+in.opTag] = in.pending
	in.left = left
	return in
}

// olhSelfInfo is the olh.info olhInitModification gives a head without an
// OLH tag, naming the head itself.
func olhSelfInfo(head *op.ObjectState, r denc.Release) []byte {
	return encodeAt(meta.OLHInfo{Target: meta.Obj{Bucket: head.Bucket.Info.Bucket, Key: head.Key}}, r)
}

// addGuards adds to w, ahead of every other step of the head write, the
// guards olhInitModification's op carries on st, the OLH head as read: its
// OLH tag, or its having none, then in.guard when there is one. The OSD runs
// an op's steps in order and applies all of them or none, so they see the
// head as it was before the op, the head write's own pending attr not yet
// set.
func (in *nullInit) addGuards(w *radosclient.WriteOp, st *op.ObjectState, r denc.Release) {
	if in.olhTag == nil {
		guardOLHTag(w, st)
	} else {
		w.CmpXattr(meta.AttrOLHIDTag, radosclient.CmpEQ, nil)
	}
	if in.guard == nil {
		return
	}
	w.CmpXattr(meta.AttrOLHInfo, radosclient.CmpEQ, in.guard.info)
	if in.guard.noPending {
		rgw.ObjCheckAttrsPrefix(w, meta.AttrOLHPendingPrefix, true, r)
	}
}

// addSets adds to w the init's sets, after the head write's reset, which
// keeps the OLH attrs: for a head without an OLH tag, olhInitModification's
// olh.info naming the head and empty olh.ver, its new OLH tag set by the
// head write's own re-set of the tag (olhIDTag); and the pending attr.
func (in *nullInit) addSets(w *radosclient.WriteOp, st *op.ObjectState, r denc.Release) {
	if in.olhTag != nil {
		w.SetXattr(meta.AttrOLHInfo, olhSelfInfo(st, r))
		w.SetXattr(meta.AttrOLHVer, []byte{})
	}
	w.SetXattr(meta.AttrOLHPendingPrefix+in.opTag, in.pending)
}

// olhIDTag is the OLH tag a head write over st, the OLH head as read, sets
// again: the one st holds, or the one in gives a head without one.
func (in *nullInit) olhIDTag(st *op.ObjectState) []byte {
	if in != nil && in.olhTag != nil {
		return in.olhTag
	}
	return st.Attrs[meta.AttrOLHIDTag]
}

// inited is in as setOLH takes it, nil for no init.
func (in *nullInit) inited() *olhInited {
	if in == nil {
		return nil
	}
	return &olhInited{head: in.left, opTag: in.opTag}
}

// writeNullVersion is the guarded pass of a conditional write of the null
// version's data over head, the key's OLH head as read, in up to
// olhAttempts attempts, then EIO. The link of the null version's data
// carries no condition (olhCondition), since its head is the OLH head and
// the head write replaces what a check would read, so the condition is
// checked before the head write, and the write's modification is inited in
// the head write's own op rather than in its link.
//
// Each attempt follows the OLH head, replaying a pending modification, and
// then evaluates the condition as a conditional link's attempt does
// (evalOLHCond): on the version current on the head, a delete marker
// counting as no object, waiting on other writers' pending modifications
// within the bound. Its head write then carries the init of its
// modification (nullInit): ahead of everything else, the guards of a
// conditional link's init, the head's OLH tag, olh.info as the attempt read
// it and, while the wait allows it, no pending attr, then radosgw's own
// guard on the head's write tag; after the head write's reset, the init's
// pending attr. The link is made under the init's op tag (olhLink.Inited).
// The pending attr, from the head write to the link's replay, is what
// another conditional writer's init refuses and waits on, so a second
// conditional write waits for the first's link and checks its condition on
// the version that link made current.
//
// The op lands whole or not at all. One the init's guards refuse, another
// writer having applied or recorded a modification since the check, or the
// head gone, writes nothing and leaves the index's prepared entry for the
// next attempt, which reads the head again and checks the condition anew.
// One the write tag's guard refuses, the head replaced, is judged as radosgw
// judges a lost race (cancelWrite). One whose outcome is unknown may have
// landed, its pending attr left to expire. A completion that fails cancels
// the modification (linkVersion). A head that is no OLH head when the
// attempt has followed it takes the unversioned write's path. What the
// guards leave unheld is in docs/differences.md, "A conditional write to a
// versioned bucket commits only if its condition held when it linked".
func (s *Store) writeNullVersion(ctx context.Context, hw *headWrite, x *indexOp, head *op.ObjectState, mtime time.Time) (headResult, error) {
	ctx = withRemovalDeadline(ctx)
	idx := s.newOLHIndex(hw.rec, hw.key)
	cond := s.writeCond(hw)
	fail := func(err error) (headResult, error) {
		if x.prepared {
			x.cancel()
		}
		return headResult{}, err
	}
	var wait olhCondWait
	for range olhAttempts {
		if err := s.followOLHHead(ctx, idx, head); err != nil {
			return fail(err)
		}
		if !isOLHHead(head) {
			return s.doWriteMeta(ctx, hw, x, head, head, mtime, false, nil)
		}
		e, err := s.evalOLHCond(ctx, idx, head, cond, &wait)
		if err != nil {
			return fail(err)
		}
		if !e.wait {
			res, werr := s.doWriteMeta(ctx, hw, x, head, e.cur, mtime, false, s.newNullInit(head, e.guard))
			if !errors.Is(werr, errOLHGuardRefused) {
				return res, werr
			}
		}
		if head, err = s.readOLHHead(ctx, hw.rec, hw.key); err != nil {
			return fail(err)
		}
	}
	return fail(fmt.Errorf("%w: writing the null version of %s/%s: every one of %d attempts lost to another writer",
		op.ErrUnknown, hw.rec.Info.Bucket.Name, hw.key.Name, olhAttempts))
}

// olhGuardRefused reports whether err, the failure of hw's head write over
// st carrying an init (nullInit), is the refusal of the init's guards rather
// than of radosgw's own guard on the head's write tag (tagGuarded). An
// ENOENT is the init's: its first guard, on the OLH tag, answers it on a head
// removed since the read. Without a tag guard, as for If-None-Match: *, an
// ECANCELED is the init's too; with one, it is the init's only when the head
// still holds the write tag st read.
func (s *Store) olhGuardRefused(ctx context.Context, hw *headWrite, st *op.ObjectState, err error) bool {
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return true
	case !errors.Is(err, radosclient.ErrCanceled):
		return false
	case !tagGuarded(hw, st):
		return true
	}
	fresh, rerr := s.readOLHHead(ctx, hw.rec, hw.key)
	return rerr == nil && fresh.Exists && bytes.Equal(fresh.Attrs[meta.AttrIDTag], st.Attrs[meta.AttrIDTag])
}

// writeCond is hw's conditions as a conditional attempt evaluates them
// (olhCondition): checkPreconditions on the version current at the attempt,
// resting on that version for any comparison with it, If-None-Match with an
// ETag included, and on none for If-None-Match: * alone.
func (s *Store) writeCond(hw *headWrite) olhCondition {
	ifMatch, ifNoneMatch := hw.ifMatch, hw.ifNoneMatch
	matched := ifMatch != "" || ifNoneMatch != "*"
	return func(_ context.Context, cur *op.ObjectState) (olhPass, error) {
		return olhPass{Current: matched}, s.checkPreconditions(cur, ifMatch, ifNoneMatch)
	}
}

// linkCond is the condition the link of hw's version holds: writeCond, nil
// for a write without conditions and for the null version's data, whose
// condition writeNullVersion checks before its head write, as its head is
// the OLH head.
func (s *Store) linkCond(hw *headWrite) olhCondition {
	if hw.key.Instance == "" || hw.ifMatch == "" && hw.ifNoneMatch == "" {
		return nil
	}
	return s.writeCond(hw)
}

// linkVersion ends a versioned write whose head write landed: the index
// entry ver and m describe, completed and awaited (completeSync), then the
// link of hw's version under its key's OLH at hw's epoch with linkCond's
// condition, under in's op tag when the head write inited the write's
// modification (setOLH). A completion that fails links nothing, as one that
// lands after the link would rewrite the entry the link wrote, and cancels
// in's modification, past the end of ctx. On an answer that proves it did
// not land it also cancels the prepare, as radosgw's done_cancel cancels the
// prepare of a completion it could not send, after any error but a timeout
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3401-L3412 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3522-L3533 at v20.2.4),
// and removes the version (removeFailedVersion). Without the cancel the
// removal's completion would keep the entry, its pending tag left, as one
// that does not exist
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1141-L1166 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1275-L1299 at v20.2.4),
// which no listing reaches. setOLH removes the version itself when it must.
// A write that rewrites an existing version in place (headWrite.expect)
// removes it on no path, as its head names the version's data; neither
// done_cancel nor set_olh's failure removes a version
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3352-L3375 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3473-L3496 at v20.2.4),
// though radosgw's processor then deletes the tails the head names
// (docs/ceph-upstream-bugs.md, "radosgw's versioned PutObject deletes its
// tails when its link fails"). The head and its tails stay otherwise,
// whatever is answered. A link the class refuses because a delete took the
// version after its head write succeeds, the delete owning the tails, as
// setOLH says for a link with olhLink.WriteTag.
func (s *Store) linkVersion(ctx context.Context, hw *headWrite, x *indexOp, ver rgw.EntryVer, m rgw.DirEntryMeta, in *nullInit) error {
	rewrite := hw.expect != nil
	if err := x.completeSync(ctx, ver, m); err != nil {
		if in != nil {
			s.olhCancelModification(context.WithoutCancel(ctx), in.left, in.opTag)
		}
		if !outcomeUnknown(err) {
			x.cancel()
		}
		if !rewrite {
			s.removeFailedVersion(ctx, x.rec, hw.key, err)
		}
		return op.FromRADOS(fmt.Errorf("completing the index entry of %s: %w", versionName(x.rec, hw.key), err), op.ScopeObject)
	}
	l := olhLink{Epoch: *hw.olhEpoch, Cond: s.linkCond(hw), Inited: in.inited(), KeepVersion: rewrite}
	if hw.keepLock {
		// keepLock marks a lifecycle transition, whose raw copy alone sets
		// it (headWrite.keepLock). A transition keeps the refusal of a link
		// whose version a delete took first: its success would run
		// keepCurrent and, on Tentacle, notify a transition of a version
		// that is gone (lcBucket.transition).
		return op.FromRADOS(s.linkTransition(ctx, x.rec, hw.key, hw.tag, l), op.ScopeObject)
	}
	l.WriteTag = []byte(hw.tag + "\x00")
	return op.FromRADOS(s.setOLH(ctx, x.rec, hw.key, l), op.ScopeObject)
}

// errTransitionGaveWay ends a lifecycle transition's link, or keepCurrent's,
// that gave way to a delete's pending removal of the version: NoSuchKey, as
// a link the class refuses for a version a delete took first answers.
var errTransitionGaveWay = fmt.Errorf("%w: a delete's removal of the version is pending", op.ErrNoSuchKey)

// linkTransition is the link l of key's version, of rec's bucket, that a
// lifecycle transition rewrote in place under write tag tag, its index
// completion landed.
//
// It first reads the version's head again, the OLH head for the null
// version, which must still hold the version's data. A delete's removal
// landing between the rewrite's head write and its completion took the head,
// and the class then applies or cancels that completion as the version the
// entry carries says. The removal's DEL completion leaves the entry at the
// removal's version, above the rewrite's, so the completion is a stale one, a
// cancel, and removes the entry left behind. But a cancel at pool -1 and
// epoch 0 landing between the two, that of an attribute write that read the
// head before the removal took it and then found it gone, sets the entry's
// version back, and the completion then lands as an ADD, making the entry
// again with no head (docs/ceph-upstream-bugs.md, "cls_rgw complete_op
// writes a stale epoch back when it cancels"). A transition
// whose head is gone links nothing, removes the entry its completion made,
// if it is still that one (dropHeadless), and answers errTransitionGaveWay:
// radosgw's transition links that entry, lists it and notifies.
//
// Then it links in rounds, each guarded as a relist's link is. A round reads
// the name's OLH entry and gives way, linking nothing, while the entry's
// pending log holds a removal of the version that a replay is still to carry
// out, one no later link of the version's data drops (unpairedRemovals): a
// delete's unlink has landed and its replay, which removes the rewritten head
// and hands its tails to the GC, has not. Its link compares the entry as
// read, so an unlink landing after the read refuses it (errRelinkMoved), and
// the next round gives way. Without the guard the link would land below the
// epoch that unlink moved the entry to, on the class's old-epoch path, which
// writes the version's list entry and logs nothing
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1760 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1892-L1901 at v20.2.4),
// and the delete's replay would then find the version listed and keep it
// (removeInstance), losing a delete it may already have acknowledged. A
// round refused for any other writer's change of the entry links again at
// the same epoch, up to olhAttempts rounds, as setOLH links again after a
// repair. The modification l.Inited names is the first round's, and a
// transition that gives way before any link cancels it. radosgw's transition
// links with neither guard (docs/differences.md, "A replay keeps a version a
// link lists again after it read the log").
func (s *Store) linkTransition(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, tag string, l olhLink) error {
	cancelInited := func() {
		if l.Inited != nil {
			s.olhCancelModification(context.WithoutCancel(ctx), l.Inited.head, l.Inited.opTag)
		}
	}
	idx := s.newOLHIndex(rec, key)
	gone, err := s.dataGone(ctx, rec, key)
	switch {
	case err != nil:
		cancelInited()
		return err
	case gone:
		cancelInited()
		idx.dropHeadless(context.WithoutCancel(ctx), key, tag)
		return fmt.Errorf("linking %s: a delete took its head: %w", versionName(rec, key), errTransitionGaveWay)
	}
	for range olhAttempts {
		e, bi, err := idx.getOLHBI(ctx, &op.ObjectState{Bucket: rec, Key: olhHeadKey(key)})
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			bi = rgw.BIEntry{Type: rgw.BIIndexOLH, Idx: rgw.OLHDataKey(idx.olhKey().Name)}
		case err != nil:
			cancelInited()
			return fmt.Errorf("reading the olh entry of %s before its transition's link: %w", versionName(rec, key), err)
		case unpairedRemovals(e.PendingLog)[key.Instance]:
			cancelInited()
			return fmt.Errorf("linking %s: %w", versionName(rec, key), errTransitionGaveWay)
		}
		l.Relink = &olhRelink{olh: &bi}
		if err = s.setOLH(ctx, rec, key, l); !errors.Is(err, errRelinkMoved) {
			return err
		}
		l.Inited = nil
	}
	return fmt.Errorf("%w: linking %s: every one of %d rounds lost to another writer", op.ErrUnknown, versionName(rec, key), olhAttempts)
}

// dataGone reports whether key's version of rec's bucket no longer has the
// head that holds its data: a named version's own head is gone, or the OLH
// head no longer holds the null version's data.
func (s *Store) dataGone(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (bool, error) {
	st, err := s.readOLHHead(ctx, rec, key)
	if err != nil {
		return false, fmt.Errorf("reading the head of %s before its transition's link: %w", versionName(rec, key), err)
	}
	if key.Instance == "" {
		return !holdsNullData(st), nil
	}
	return !st.Exists, nil
}

// dropHeadless removes key's data entry when it is the one a write under tag
// completed and the version's head is gone: the DEL completion
// delete_obj_index sends for an object that no longer exists, with no tag,
// pool -1 and epoch 0 (deleteObjIndex), in an op that compares the entry as
// read and, when the entry names a list entry, that list entry as absent.
// The class uncounts the entry and takes it out or, while another writer's
// prepare is pending on it, keeps it as not existing, for that writer's
// completion or cancel to remove. An op the compares refuse is sent again,
// up to olhAttempts rounds, from a fresh read of the head and of the entry:
// an attribute write that read the head before the removal took it prepares
// on the entry and then, finding the head gone, cancels, which leaves an
// existing entry in place. A version listed, when read or by a link landing
// before the op, is left as the link left it, its instance entry under its
// list entry: removing the one would leave the other naming nothing
// (docs/differences.md, "A replay keeps a version a link lists again after it
// read the log"). Its failure is logged.
func (b *bucketOLHIndex) dropHeadless(ctx context.Context, key meta.ObjKey, tag string) {
	s, rec := b.x.s, b.x.rec
	bucket, name, instance := slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.String("instance", key.Instance)
	for round := range olhAttempts {
		if round > 0 {
			gone, err := s.dataGone(ctx, rec, key)
			switch {
			case err != nil:
				slog.WarnContext(ctx, "reading again the head of a version a transition completed over its removal failed; its index entry stays for bucket check",
					bucket, name, instance, slog.Any("error", err))
				return
			case !gone:
				return
			}
		}
		en, bi, err := b.dataEntry(ctx, key)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return
		case err != nil:
			slog.WarnContext(ctx, "reading the index entry a transition completed over a removed version failed; it stays for bucket check",
				bucket, name, instance, slog.Any("error", err))
			return
		case !en.Exists || en.Tag != tag:
			return
		}
		cmps := []rgw.BIEntry{bi}
		if listsData(en) {
			listed, lerr := s.listed(ctx, rec, key, en)
			switch {
			case lerr != nil:
				slog.WarnContext(ctx, "reading the list entry of a version a transition completed over its removal failed; its index entry stays for bucket check",
					bucket, name, instance, slog.Any("error", lerr))
				return
			case listed:
				slog.DebugContext(ctx, "a link listed a version whose head a removal took under its transition; its index entries stay",
					bucket, name, instance)
				return
			}
			cmps = append(cmps, rgw.BIEntry{Type: rgw.BIIndexPlain, Idx: rgw.ListIndexKey(en.Key, en.VersionedEpoch)})
		}
		x := &indexOp{s: s, rec: rec, obj: meta.Obj{Bucket: rec.Info.Bucket, Key: key}, blind: rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless}
		c := x.completion(rgw.OpDel, rgw.EntryVer{Pool: -1}, rgw.DirEntryMeta{Category: rgw.CategoryNone, Mtime: en.Meta.Mtime})
		err = x.guardedWrite(ctx, func(w *radosclient.WriteOp) {
			for _, e := range cmps {
				w.OmapCmp(e.Idx, radosclient.CmpEQ, e.Data)
			}
			rgw.BucketCompleteOp(w, c, s.release)
		})
		if !errors.Is(err, radosclient.ErrCanceled) {
			if err != nil {
				slog.WarnContext(ctx, "removing the index entry a transition completed over a removed version failed; it stays for bucket check",
					bucket, name, instance, slog.Any("error", err))
			}
			return
		}
	}
	slog.WarnContext(ctx, "removing the index entry a transition completed over a removed version lost every round to another writer; it stays for bucket check",
		bucket, name, instance, slog.Int("rounds", olhAttempts))
}
