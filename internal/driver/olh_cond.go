package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// A conditional link commits only if its condition held when the link
// landed, where radosgw evaluates the condition once and then links over
// whatever another writer did since. Each attempt evaluates the condition on
// the version current once it has followed the OLH head (prepareOLHAttempt),
// and guards its init on that head's olh.info and on no other writer's
// modification pending, which
// serializes rgw-go's conditional writers. radosgw's writers and rgw-go's
// unconditional ones guard their init on the OLH tag alone, so they can link
// between a conditional writer's init and its link: after its link the
// conditional writer lists the name's versions and unlinks its own when
// another took an epoch in between.

const (
	// olhCondBackoffs is the most backoffs a conditional link spends waiting
	// for other writers' pending modifications to leave the OLH head; past
	// them its init stops refusing a pending attr, and the check after its
	// link alone holds the condition. The backoffs add up to 318-635ms. A
	// pending attr already older than olhCondStale when first read adds no
	// wait: radosgw's link at an old epoch leaves one that stays until
	// rgw_olh_pending_timeout_sec expires it, and waiting on it would cost
	// every conditional link of the name the whole bound.
	olhCondBackoffs = 8
	// olhCondBackoffMin is the first backoff, which doubles each time up to
	// olhCondBackoffMax; each is jittered over its upper half.
	olhCondBackoffMin = 5 * time.Millisecond
	olhCondBackoffMax = 160 * time.Millisecond
	// olhCondListPage is the entries a page of the check after a
	// conditional link asks for; the check stops at the first version at or
	// below the epoch its attempt read, normally within the first page.
	olhCondListPage = 16
)

// errNullDataCond refuses a condition on a link of the null version's data
// (olhCondition).
var errNullDataCond = fmt.Errorf("%w: a link of the null version's data was given a condition, which the write must evaluate before its head write", op.ErrInternalError)

// olhCondition is a conditional link's condition, evaluated each attempt on
// cur, the state of the version current on the OLH head as the attempt's
// follow left it (followOLHHead, olhCurrent). Its pass names what the pass
// rests on (olhPass), which the check after the link requires still listed;
// a current delete marker is required listed whatever the pass names
// (prepareOLHAttempt). Its error, PreconditionFailed for a condition that
// does not hold, ends the write.
//
// A write of the null version's data evaluates its condition before its
// head write, and inits its modification in the head write's op, not in its
// link: its head is the OLH head, so after that write the head an attempt
// reads is the write's own data (writeNullVersion). setOLH refuses a
// condition on such a link, before it writes anything (errNullDataCond).
type olhCondition func(ctx context.Context, cur *op.ObjectState) (olhPass, error)

// olhPass is what a condition's pass rests on (olhCondition). Current is the
// version current being so, as any comparison with its ETag, size or
// last-modified time does, If-None-Match with an ETag included. Null is the
// null version's data in the OLH head, which a condition checked on that
// head compares where no version is current (deleteMarkerCondition): a
// removal of the null version takes its data from the head and lists
// nothing above the epoch the attempt read, so only the null version's own
// list entry shows it.
type olhPass struct {
	Current bool
	Null    bool
}

// olhAttempt is what an attempt of setOLH decides before its init, on the
// OLH head it read and, with a condition, followed: the guard its init
// carries, nil for none; check, the check its link needs, nil for none; and
// wait, set once it has backed off, after which the next attempt reads the
// head again.
type olhAttempt struct {
	guard *olhCondGuard
	check *olhLinkCheck
	wait  bool
}

// olhLinkCheck is what a conditional link's check after the link compares:
// v0, the epoch the attempt's head held (olhVerEpoch); matched, the
// instance of the version or delete marker the condition's pass rested on
// being current, nil when it rested on none; and null, set when the pass
// rested on the null version's data too (olhPass).
type olhLinkCheck struct {
	v0      uint64
	matched *string
	null    bool
}

// olhCondWait is a conditional link's wait on other writers' pending
// modifications across its attempts: the backoffs it spent, pastBound once
// they reach olhCondBackoffs, and stale, for each pending attr an attempt
// read, whether it was older than olhCondStale when first read.
type olhCondWait struct {
	backoffs  int
	pastBound bool
	stale     map[string]bool
}

// prepareOLHAttempt is what an attempt of setOLH linking key checks before
// its init on head, the OLH head it read, which the follow may replace with
// a fresh read:
//   - with p.ExpectTag, the head's user.rgw.idtag, absent read as empty, must
//     be p.ExpectTag, or the write is ConcurrentModification with nothing
//     written; the init then compares it again (olhInitModification);
//   - with p.Cond, the link must not be of the null version's data
//     (olhCondition); head is followed first (followOLHHead) and ExpectTag
//     compared on the head as followed; the condition holds on the version
//     current on it, the one current after any pending modification's
//     replay, where radosgw's evaluates on the one current before
//     (followOLHHead), or its error ends the write; while other writers'
//     modifications are pending the attempt waits (waitOLHPending); and the
//     init is guarded on
//     head's olh.info, and on no pending attr while the wait allows it. A
//     named version's link is then checked (checkOLHLink), which requires the
//     version the condition matched still listed, and a current delete
//     marker too: the removal of a marker makes the version below it current
//     again without listing anything above the epoch the attempt read
//     (checkOLHLink), so a pass that took the marker for no version would
//     otherwise commit over that version. A pass that rested on the null
//     version's data in the OLH head requires the null version listed as
//     well (olhPass). A null link is not checked, so on
//     a null version, the null delete marker's link included, the condition
//     holds only as far as the init's guard serializes rgw-go's conditional
//     writers: its link replaces the previous null version's list entry,
//     and a null delete marker's logs the null data's removal
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1731 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1872 at v20.2.4),
//     so no unlink can restore what the link replaced.
func (s *Store) prepareOLHAttempt(ctx context.Context, idx *bucketOLHIndex, head *op.ObjectState, key meta.ObjKey, p olhLink, w *olhCondWait) (olhAttempt, error) {
	if p.Cond != nil {
		if key.Instance == "" && !p.DeleteMarker {
			return olhAttempt{}, errNullDataCond
		}
		if err := s.followOLHHead(ctx, idx, head); err != nil {
			return olhAttempt{}, err
		}
	}
	if p.ExpectTag != nil && !bytes.Equal(head.Attrs[meta.AttrIDTag], p.ExpectTag) {
		return olhAttempt{}, fmt.Errorf("%w: the head's write tag is not the one expected", op.ErrConcurrentModification)
	}
	if p.Cond == nil {
		return olhAttempt{}, nil
	}
	e, err := s.evalOLHCond(ctx, idx, head, p.Cond, w)
	if err != nil || e.wait {
		return olhAttempt{wait: e.wait}, err
	}
	a := olhAttempt{guard: e.guard}
	if key.Instance != "" {
		a.check = &olhLinkCheck{v0: olhVerEpoch(head), null: e.pass.Null}
		if (e.pass.Current && e.cur.Exists) || e.cur.DeleteMarker {
			a.check.matched = &e.cur.Key.Instance
		}
	}
	return a, nil
}

// olhCondEval is a conditional attempt's evaluation on the OLH head it
// followed (evalOLHCond): cur, the state of the version current on it;
// pass, what the condition's pass rests on (olhPass); guard, the guard the
// attempt's init carries; and wait, set once the attempt has backed off,
// after which the next attempt reads the head again and the rest is unset.
type olhCondEval struct {
	cur   *op.ObjectState
	pass  olhPass
	guard *olhCondGuard
	wait  bool
}

// evalOLHCond is what both conditional writers' attempts do once they have
// followed head, the OLH head of idx's name (followOLHHead): setOLH's
// conditional link (prepareOLHAttempt) and the conditional write of the null
// version's data (writeNullVersion). cond is evaluated on the version current
// on head (olhCurrent), its error ending the write; the attempt waits while
// other writers' modifications are pending (waitOLHPending); and its init is
// then guarded on head's olh.info and, while the wait allows it, on no
// pending attr.
func (s *Store) evalOLHCond(ctx context.Context, idx *bucketOLHIndex, head *op.ObjectState, cond olhCondition, w *olhCondWait) (olhCondEval, error) {
	cur, err := s.olhCurrent(ctx, idx.x.rec, head)
	if err != nil {
		return olhCondEval{}, err
	}
	pass, err := cond(ctx, cur)
	if err != nil {
		return olhCondEval{}, err
	}
	wait, refuse, err := s.waitOLHPending(ctx, idx, head, w)
	if err != nil || wait {
		return olhCondEval{wait: wait}, err
	}
	return olhCondEval{cur: cur, pass: pass, guard: &olhCondGuard{info: head.Attrs[meta.AttrOLHInfo], noPending: refuse}}, nil
}

// followOLHHead follows head, the OLH head of idx's name as an attempt read
// it, before the attempt's condition is evaluated: it expires the pending
// attrs past rgw_olh_pending_timeout_sec and, while any remains, young or
// stale, replays the name's pending log through idx once, as follow_olh runs
// update_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8984-L8996 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9730-L9742 at v20.2.4),
// so that a writer's link logged and never replayed counts. When the replay
// applied anything, or another writer's guard refused a pass of it, head is
// read again into *head and the attempt works on that read: the condition
// is evaluated on the version current after the replay.
//
// radosgw's precondition check differs on both counts. It reads the current
// version through follow_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3187-L3197 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6624-L6644 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3284-L3294 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7238-L7258 at v20.2.4),
// which resolves it from the head as read before the replay
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8999-L9018 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9745-L9766 at v20.2.4),
// so it evaluates on the version current before the replay; and it takes a
// refused replay, update_olh's ECANCELED, for ENOENT, evaluating on no
// current version
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8989-L8995 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3190-L3191 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9735-L9741 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3287-L3288 at v20.2.4).
// rgw-go answers as its reads do (docs/exclusions.md, "A read that replays a
// pending modification answers the version current after it";
// docs/ceph-upstream-bugs.md, "radosgw's follow_olh answers the version that
// was current before the replay it runs").
func (s *Store) followOLHHead(ctx context.Context, idx *bucketOLHIndex, head *op.ObjectState) error {
	if !head.Exists {
		return nil
	}
	pending, err := s.expireOLHPending(ctx, head)
	if err != nil || len(pending) == 0 {
		return err
	}
	r := olhReplay{}
	refused, err := s.replayOLH(ctx, idx, head, &r)
	switch err = errors.Join(err, refused); {
	case err != nil && !errors.Is(err, radosclient.ErrCanceled):
		return err
	case err == nil && !r.applied && !r.raced:
		return nil
	}
	fresh, err := s.readOLHHead(ctx, idx.x.rec, head.Key)
	if err != nil {
		return err
	}
	*head = *fresh
	return nil
}

// olhReplayTag is the write tag the replay of a link made with expectTag
// guards the removals the link logged on: expectTag, but for a head that
// was not tagged before init (olhTagged), the user.rgw.idtag init then wrote
// on it. Init's compare proved, in the op that replaced the tag, that the
// head held expectTag, as a missing head's exclusive create proved that no
// head did; the version such a link logs for removal, the null version, is
// that head.
func olhReplayTag(expectTag []byte, head *op.ObjectState, tagged bool) []byte {
	if expectTag == nil || tagged {
		return expectTag
	}
	return slices.Clone(head.Attrs[meta.AttrIDTag])
}

// olhCurrent is the state of the version current on head, the OLH head of a
// name of rec's bucket as read without following it. A head without olh.ver
// is a plain object and its own state, as get_obj_state answers it unfollowed
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6226-L6230 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6422-L6434 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6840-L6844 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7036-L7048 at v20.2.4).
// Otherwise it is olh.info's target, read without following, the null
// version's being head itself, and a state that does not exist when the
// head is missing, has no olh.info or names a delete marker, for which
// follow_olh answers ENOENT (followOLH); a marker's state is flagged so.
func (s *Store) olhCurrent(ctx context.Context, rec *op.BucketRecord, head *op.ObjectState) (*op.ObjectState, error) {
	if _, ok := head.Attrs[meta.AttrOLHVer]; !ok || !head.Exists {
		return head, nil
	}
	b, ok := head.Attrs[meta.AttrOLHInfo]
	if !ok {
		return &op.ObjectState{Bucket: rec, Key: head.Key}, nil
	}
	info, err := decodeOLHInfo(head, b)
	switch {
	case err != nil:
		return nil, err
	case info.Removed:
		return &op.ObjectState{Bucket: rec, Key: info.Target.Key, DeleteMarker: true}, nil
	case info.Target.Key.Instance == "":
		return head, nil
	}
	return s.readOLHHead(ctx, rec, info.Target.Key)
}

// olhVerEpoch is the epoch head's olh.ver holds, read up to a NUL as
// apply_olh_log's strict_strtoll reads it, or 1 when it holds none: epoch 1
// is the class's converted plain entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644 at v20.2.4),
// the plain object a head without olh.ver is, which the condition already
// saw.
func olhVerEpoch(head *op.ObjectState) uint64 {
	digits, _, _ := strings.Cut(string(head.Attrs[meta.AttrOLHVer]), "\x00")
	if n, ok := rgwtext.StrictStrtoll(digits); ok && n > 1 {
		return uint64(n)
	}
	return 1
}

// waitOLHPending holds a conditional attempt while other writers'
// modifications are pending on head, as followOLHHead left it, which its init
// would refuse; the follow has expired those past
// rgw_olh_pending_timeout_sec, where radosgw's set_olh expires none, and
// replayed what a writer logged. It waits only on attrs no older than
// olhCondStale when first read (olhCondWait): while any such remains and w
// has backoffs left, it backs off and reports that it did, after which the
// next attempt reads and follows the head again. Once w's backoffs are spent
// it marks w past its bound. refuse says whether the init may refuse pending
// attrs: only while none remains that the attempt does not wait on.
func (s *Store) waitOLHPending(ctx context.Context, idx *bucketOLHIndex, head *op.ObjectState, w *olhCondWait) (wait, refuse bool, err error) {
	if !head.Exists {
		return false, true, nil
	}
	if w.pastBound {
		return false, false, nil
	}
	// The expired attrs head still lists are the ones the follow removed.
	_, pending := s.checkPendingOLHEntries(ctx, head)
	young := w.young(ctx, head, pending, s.now())
	switch {
	case young == 0:
		return false, len(pending) == 0, nil
	case w.backoffs >= olhCondBackoffs:
		w.pastBound = true
		slog.DebugContext(ctx, "conditional olh link done waiting for pending modifications; its init no longer refuses them",
			slog.String("key", idx.x.obj.Bucket.Name+"/"+head.Key.Name), slog.Int("pending", len(pending)))
		return false, false, nil
	}
	d := olhCondBackoff(w.backoffs)
	w.backoffs++
	if err := s.condBackoff(ctx, d); err != nil {
		return false, false, err
	}
	return true, false, nil
}

// young counts the attrs among pending, head's still pending, that were no
// older than olhCondStale at now when w first read them, and records the
// ones it reads for the first time. An attr that does not decode counts as
// stale, being no writer's.
func (w *olhCondWait) young(ctx context.Context, head *op.ObjectState, pending []string, now time.Time) int {
	if w.stale == nil {
		w.stale = map[string]bool{}
	}
	n := 0
	for _, name := range pending {
		stale, ok := w.stale[name]
		if !ok {
			info, err := decodeAttr(head.Attrs[name], meta.DecodeOLHPendingInfo)
			stale = err != nil || now.Sub(info.Time) > olhCondStale()
			w.stale[name] = stale
			if stale {
				slog.DebugContext(ctx, "conditional olh link not waiting on a stale pending modification",
					slog.String("key", head.Bucket.Info.Bucket.Name+"/"+head.Key.Name), slog.String("attr", name))
			}
		}
		if !stale {
			n++
		}
	}
	return n
}

// olhCondStale is the least the backoffs add up to, 317.5ms: a pending attr
// already older when first read is not a live writer's, which goes from its
// init to its replay well within it. The age compares the time the attr's
// writer stamped by its own gateway's clock with this one's, so a skew past
// the floor takes a live writer's attr for stale, and the wait is skipped, or
// a dead writer's for young, and every conditional link of the name waits
// the bound until the attr expires. A named version's condition does not rest
// on the wait: the check after its link holds it.
func olhCondStale() time.Duration {
	var t time.Duration
	for n := range olhCondBackoffs {
		t += olhCondBackoffCap(n) / 2
	}
	return t
}

// olhCondBackoffCap is backoff n's longest: olhCondBackoffMin doubled n
// times, at most olhCondBackoffMax.
func olhCondBackoffCap(n int) time.Duration { return min(olhCondBackoffMin<<n, olhCondBackoffMax) }

// olhCondBackoff is backoff n, from 0: olhCondBackoffCap(n), jittered over
// its upper half so that writers waiting on one name spread their retries.
func olhCondBackoff(n int) time.Duration {
	d := olhCondBackoffCap(n)
	return d/2 + rand.N(d/2+1) //nolint:gosec // spreads retries; nothing secret
}

// condBackoff waits d for a conditional link's backoff, through s.condSleep
// when set.
func (s *Store) condBackoff(ctx context.Context, d time.Duration) error {
	if s.condSleep != nil {
		return s.condSleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}

// checkOLHLink is a conditional link's check of key, a version of idx's
// name, after its link landed and before its replay. The link is void when
// another version or marker holds an epoch above c.v0 and below the link's
// own, or when the version c.matched names, or with c.null the null
// version, is no longer listed. An unlink
// leaves nothing above c.v0: the unlink of the current version promotes the
// next one at that version's own epoch, write() setting the unlink's epoch
// on an entry it has not read, which write_entries then reads back over it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1388-L1427 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1936-L1967 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1514-L1554 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2083-L2114 at v20.2.4),
// and that of the only version writes no list entry. So only the listing of
// c.matched, and of the null version with c.null, shows an unlink. The
// name's listing is read, not its pending log, which another gateway may
// have applied and trimmed.
//
// When the link's own version is no longer listed, removed since by another
// writer, its epoch is unknown: the link is void when the matched version,
// or with c.null the null version, is not listed, or another version or
// marker holds any epoch above c.v0 but the
// matched one, which only a link of it again puts up there, making it current
// again. A void link whose version is gone answers PreconditionFailed with
// nothing to unlink. One that is not void answers success: the removal shows
// the link landed.
//
// Otherwise a void link is unlinked through unlinkVersion, skip being its
// SkipUpdate. The unlink's replay applies the link with the rest of the log
// and removes the version, promoting the other writer's when the void one
// was current, and the write answers PreconditionFailed; once the unlink
// landed, a replay that failed is logged and left to the next update, as is
// a version the class no longer found. A listing that fails leaves the
// condition unknown, so the link is unlinked too and the listing's error
// answered, a 5xx. An unlink whose outcome is unknown answers its error; one
// that fails leaves the version linked and answers InternalError with it.
func (s *Store) checkOLHLink(ctx context.Context, idx *bucketOLHIndex, key meta.ObjKey, c *olhLinkCheck, skip bool) error {
	what := versionName(idx.x.rec, key)
	errVoid := fmt.Errorf("%w: another writer changed the key between the condition's check and the link", op.ErrPreconditionFailed)
	void, listed, cause := idx.linkVoid(ctx, key, c)
	switch {
	case cause == nil && !void:
		return nil
	case cause == nil && !listed:
		return fmt.Errorf("linking %s: %w", what, errVoid)
	case cause == nil:
		cause = errVoid
	}
	out, err := s.unlinkVersion(ctx, idx.x.rec, key, olhUnlink{SkipUpdate: skip})
	switch out {
	case unlinkLanded:
		if err != nil {
			slog.WarnContext(ctx, "the replay of a void conditional link's unlink failed; the next update applies it",
				slog.String("version", what), slog.Any("error", err))
		}
		return fmt.Errorf("linking %s: %w", what, cause)
	case unlinkGone:
		return fmt.Errorf("linking %s: %w", what, cause)
	case unlinkUnknown:
		return fmt.Errorf("linking %s: unlinking it after %s left the unlink's outcome unknown: %w", what, cause.Error(), err)
	}
	return fmt.Errorf("%w: linking %s: unlinking it after %s failed, so it stays linked: %w", op.ErrInternalError, what, cause.Error(), err)
}

// linkVoid reports whether c voids the link of key, from the name's versions
// on b's shard down to the first other one at or below c.v0, and with c.null
// on down to the null version's, wherever its epoch lists it, and whether
// key's version is listed (checkOLHLink).
func (b *bucketOLHIndex) linkVoid(ctx context.Context, key meta.ObjKey, c *olhLinkCheck) (void, listed bool, err error) {
	type above struct {
		epoch   uint64
		matched bool
	}
	var (
		own         uint64
		matchedSeen bool
		nullSeen    bool
		others      []above
	)
	err = b.listVersions(ctx, func(en rgw.DirEntry) bool {
		if en.Key.Instance == key.Instance {
			own, listed = en.VersionedEpoch, true
			return true
		}
		matched := c.matched != nil && en.Key.Instance == *c.matched
		matchedSeen = matchedSeen || matched
		nullSeen = nullSeen || en.Key.Instance == ""
		if en.VersionedEpoch <= c.v0 {
			return c.null && !nullSeen
		}
		others = append(others, above{epoch: en.VersionedEpoch, matched: matched})
		return true
	})
	switch {
	case err != nil:
		return false, false, err
	case c.matched != nil && !matchedSeen, c.null && !nullSeen:
		return true, listed, nil
	}
	void = slices.ContainsFunc(others, func(o above) bool {
		if listed {
			return o.epoch < own
		}
		return !o.matched
	})
	return void, listed, nil
}

// listVersions visits the list entries of b's name on its shard newest
// first, in the order the class keys them
// (get_list_index_key, https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L302-L315 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L352-L365 at v20.2.4),
// a page of olhCondListPage at a time, until visit returns false or the
// name's entries end. bucket_list from a key with no instance starts after
// that key as it is
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L363 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L413 at v20.2.4):
// the first page after the name's own key, its version marker, and each
// later page after the last list key read, which a version unlinked
// meanwhile does not move, as a start naming its instance would. A shard a
// reshard removed is found again from the bucket (indexOp.readShard).
func (b *bucketOLHIndex) listVersions(ctx context.Context, visit func(rgw.DirEntry) bool) error {
	name := b.olhKey().Name
	start := rgw.ObjKey{Name: name}
	for {
		var ret rgw.ListRet
		err := b.x.readShard(ctx, func(sh indexShard) error {
			var err error
			ret, _, err = b.x.s.bucketList(ctx, sh.pool, sh.oid, rgw.ListOp{StartObj: start, NumEntries: olhCondListPage, FilterPrefix: name, ListVersions: true})
			return err
		})
		if err != nil {
			return fmt.Errorf("listing the versions of %s in bucket %s: %w", b.x.obj.Key.Name, b.x.obj.Bucket.Name, err)
		}
		keys := slices.Sorted(maps.Keys(ret.Dir.Entries))
		for _, k := range keys {
			if en := ret.Dir.Entries[k]; en.Key.Name != name || !visit(en) {
				return nil
			}
		}
		if !ret.IsTruncated || len(keys) == 0 {
			return nil
		}
		start = rgw.ObjKey{Name: keys[len(keys)-1]}
	}
}
