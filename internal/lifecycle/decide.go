package lifecycle

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

//go:generate go tool counterfeiter -generate

// Entry is a bucket index entry as the lifecycle worker lists it, the fields
// of rgw_bucket_dir_entry it reads.
type Entry struct {
	Key meta.ObjKey
	// Current is is_current
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L446-L451 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L446-L451 at v20.2.4):
	// an entry of a bucket never versioned, or the version its OLH names.
	Current      bool
	DeleteMarker bool
	Mtime        time.Time
	// StorageClass is the class the entry holds, empty for STANDARD.
	StorageClass string
}

// State is what LCObjsLister knows of an entry when it hands it to the rule,
// as LCOpRule::update takes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1610-L1615 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1666-L1671 at v20.2.4).
type State struct {
	// Next is next_key_name, when HasNext: the name of the entry after this
	// one, or this one's own at the end of a truncated page.
	Next    string
	HasNext bool
	// Earlier is num_noncurrent: the entries of this one's name the listing
	// gave before it, which are the versions newer than it.
	Earlier uint64
	// PrevMtime is the mtime of the entry before this one, whatever its
	// name, which radosgw takes for a noncurrent version's effective mtime,
	// the time it became noncurrent; the zero time for a rule's first entry.
	PrevMtime time.Time
}

func (s State) nextIs(name string) bool { return s.HasNext && s.Next == name }

// Lister is LCObjsLister
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L351-L446 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L351-L454 at v20.2.4)
// over one rule's listing, fed its pages in order. Each rule's walk takes a
// new Lister, as radosgw builds one per rule.
type Lister struct {
	prevName  string
	prevMtime time.Time
	earlier   uint64
}

// Page returns the State of each of page's entries, in order, and carries
// its last entry to the next page. truncated is the listing's is_truncated,
// whether another page follows.
//
// The last entry of a truncated page sees its own name as Next, as v20.2.4's
// next_key_name gives it, on both releases. v19.2.7 gives it none, so a
// current delete marker there looks to have no older version and is
// removed, though its older versions start the next page, and the newest of
// them becomes current again (docs/exclusions.md, "A current delete marker
// at the end of a listing page is kept on Squid").
func (l *Lister) Page(page []Entry, truncated bool) []State {
	states := make([]State, len(page))
	for i, e := range page {
		s := &states[i]
		switch {
		case i+1 < len(page):
			s.Next, s.HasNext = page[i+1].Key.Name, true
		case truncated:
			s.Next, s.HasNext = e.Key.Name, true
		}
		if e.Key.Name == l.prevName {
			l.earlier++
		} else {
			l.earlier = 0
		}
		s.Earlier, s.PrevMtime = l.earlier, l.prevMtime
		l.prevName, l.prevMtime = e.Key.Name, e.Mtime
	}
	return states
}

// Clock is when and where the worker judges an object's age: now,
// rgw_lc_debug_interval when it is positive, and the gateway's time zone,
// time.Local in a running gateway. A nil Loc is UTC.
type Clock struct {
	Now   time.Time
	Debug time.Duration
	Loc   *time.Location
}

//counterfeiter:generate . Probe

// Probe reads what Decide needs of a listed version that its index entry
// does not carry. Each method is given the version's key as Decision.Key
// names it.
type Probe interface {
	// Size is the version's size, uncompressed, as S3 counts it: a stat's
	// op.ObjectState.Compression.OrigSize when Compression is set, and its
	// Size otherwise, which for a compressed object is the size stored. A
	// version that does not exist has size 0, as get_obj_state answers for a
	// head it does not find. An error is a stat that failed otherwise.
	Size(ctx context.Context, key meta.ObjKey) (uint64, error)
	// Tags is the version's user.rgw.x-amz-tagging attr, nil when it has
	// none. An error is a read that failed otherwise.
	Tags(ctx context.Context, key meta.ObjKey) ([]byte, error)
	// VerifyDelete is the lock check of a permanent removal, nil when the
	// version may be removed: objectlock.VerifyDelete of its attrs at now
	// with both bypass flags false, and nil for a version that does not
	// exist, as pass_object_lock_check answers. Decide reads any error as
	// "keep", so the implementation logs each failure but an
	// objectlock.ErrLocked refusal, as pass_object_lock_check logs an
	// RGWObjectRetention that does not decode
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L320-L327 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L320-L327 at v20.2.4):
	// slog.WarnContext(ctx, ..., slog.String("key", key.Name),
	// slog.String("instance", key.Instance), slog.Any("error", err)).
	VerifyDelete(ctx context.Context, key meta.ObjKey) error
}

// Action is the rule action Decide selected, one of LCOpRule::build's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1582-L1608 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1638-L1664 at v20.2.4).
type Action uint8

// The rule actions, in the order LCOpRule::build adds them.
const (
	ActionNone Action = iota
	// ActionExpiration is LCOpAction_CurrentExpiration.
	ActionExpiration
	// ActionDeleteMarkerExpiration is LCOpAction_DMExpiration.
	ActionDeleteMarkerExpiration
	// ActionNoncurrentExpiration is LCOpAction_NonCurrentExpiration.
	ActionNoncurrentExpiration
	// ActionTransition is LCOpAction_CurrentTransition.
	ActionTransition
	// ActionNoncurrentTransition is LCOpAction_NonCurrentTransition.
	ActionNoncurrentTransition
)

// Effect is what the selected action does to Decision.Key.
type Effect uint8

// The effects.
const (
	EffectNone Effect = iota
	// EffectRemove removes the version permanently: a delete marker, a
	// noncurrent version, or the object of a bucket never versioned.
	EffectRemove
	// EffectMarker writes a delete marker over the key's name, which keeps
	// the version as noncurrent.
	EffectMarker
	// EffectNullMarker writes the null delete marker over the key's name, as
	// DeleteObject does on a versioning-suspended bucket: it replaces the
	// null version and keeps any other as noncurrent.
	EffectNullMarker
	// EffectTransition moves the version to Decision.StorageClass.
	EffectTransition
)

// Decision is what Decide chose for one entry. Key is the entry's key, with
// the instance "null" for an empty one on a bucket that was ever versioned,
// as lc_op_ctx names a noncurrent version and remove_expired_obj a removal
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L497-L505 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L617-L621 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L505-L513 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L624-L628 at v20.2.4).
// StorageClass is a transition's target, canonical.
type Decision struct {
	Action       Action
	Effect       Effect
	Key          meta.ObjKey
	StorageClass string
}

// Applies is is_valid_op and zone_check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1009-L1028 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1029-L1048 at v20.2.4):
// whether the worker runs the rule at all, in an archive zone or not. It is
// enabled, has an object action, and carries the ArchiveZone flag exactly
// when the zone is an archive. A transition counts as an action even when
// its date is nil; an expiration's nil date does not.
func (op Op) Applies(archiveZone bool) bool {
	return op.hasObjectAction() && (op.Flags&FlagArchiveZone != 0) == archiveZone
}

func (op Op) hasObjectAction() bool {
	return op.Enabled && (op.Expiration > 0 || op.ExpirationDate != nil || op.NoncurExpiration > 0 || op.DMExpiration ||
		len(op.Transitions) > 0 || len(op.NoncurTransitions) > 0)
}

// action is one of an op's actions as LCOpRule::build adds it.
type action struct {
	kind       Action
	transition TransitionAction
}

// actions is LCOpRule::build over op: the current expiration when the op
// has positive days or a date, the delete-marker expiration, the noncurrent
// expiration, then each transition and each noncurrent transition in its
// std::map's order, by the class the rule names.
func actions(op *Op) []action {
	var as []action
	if op.Expiration > 0 || op.ExpirationDate != nil {
		as = append(as, action{kind: ActionExpiration})
	}
	if op.DMExpiration {
		as = append(as, action{kind: ActionDeleteMarkerExpiration})
	}
	if op.NoncurExpiration > 0 {
		as = append(as, action{kind: ActionNoncurrentExpiration})
	}
	for _, class := range slices.Sorted(maps.Keys(op.Transitions)) {
		as = append(as, action{kind: ActionTransition, transition: op.Transitions[class]})
	}
	for _, class := range slices.Sorted(maps.Keys(op.NoncurTransitions)) {
		as = append(as, action{kind: ActionNoncurrentTransition, transition: op.NoncurTransitions[class]})
	}
	return as
}

// Decide is LCOpRule::process
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1617-L1677 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1673-L1733 at v20.2.4)
// for entry e of op's listing on bucket, with s its Lister state, at c. Of
// the actions whose check holds, it selects the one due strictly latest, so
// the action built first wins a tie; drops a transition to the class e
// already holds; then runs the tag filter, which a delete marker passes. The
// error is why e was skipped: its size or its tags could not be read, or
// its tags do not decode.
//
// It reads the Probe only for an action that is due, and each of its
// answers at most once per entry, where radosgw stats the object in every
// check that has a size filter, due or not; the decisions are the same.
// They differ from radosgw's where
// docs/exclusions.md says so: "Lifecycle expiration on a versioning-
// suspended bucket writes the null delete marker", "Every permanent removal
// by lifecycle runs the object-lock check", "Lifecycle selection takes an
// action due at the epoch", "The lifecycle size filter reads a version's
// own, uncompressed size" and "The lifecycle tag filter needs each of a
// rule's tags among the object's".
func Decide(ctx context.Context, op *Op, bucket *meta.BucketInfo, e Entry, s State, c Clock, p Probe) (Decision, error) {
	if !op.hasObjectAction() {
		return Decision{}, nil
	}
	ev := &evaluation{op: op, bucket: bucket, e: e, s: s, c: c, p: p, key: versionKey(bucket, e)}
	var selected Decision
	var latest time.Time
	for _, a := range actions(op) {
		d, due, ok, err := ev.check(ctx, a)
		if err != nil {
			return Decision{}, err
		}
		if ok && (selected.Effect == EffectNone || due.After(latest)) {
			selected, latest = d, due
		}
	}
	switch {
	case selected.Effect == EffectNone:
		return Decision{}, nil
	case selected.Effect == EffectTransition && canonicalClass(e.StorageClass) == selected.StorageClass:
		return Decision{}, nil
	}
	if ok, err := ev.tagsMatch(ctx); !ok {
		return Decision{}, err
	}
	return selected, nil
}

// versionKey is the key Decision.Key names e's version by.
func versionKey(bucket *meta.BucketInfo, e Entry) meta.ObjKey {
	k := e.Key
	if k.Instance == "" && bucket.Versioned() {
		k.Instance = meta.NullInstance
	}
	return k
}

// evaluation is lc_op_ctx: one entry under one op, with the version's size
// once it is read.
type evaluation struct {
	op     *Op
	bucket *meta.BucketInfo
	e      Entry
	s      State
	c      Clock
	p      Probe
	key    meta.ObjKey

	sized   bool
	size    uint64
	sizeErr error
}

// check is a's check: the decision it makes, its due time, and whether it
// holds.
func (ev *evaluation) check(ctx context.Context, a action) (Decision, time.Time, bool, error) {
	var d Decision
	var due time.Time
	var ok bool
	switch a.kind {
	case ActionExpiration:
		d, due, ok = ev.expiration()
	case ActionDeleteMarkerExpiration:
		d, due, ok = ev.deleteMarkerExpiration()
		return d, due, ok, nil
	case ActionNoncurrentExpiration:
		d, due, ok = ev.noncurrentExpiration()
	case ActionTransition, ActionNoncurrentTransition:
		d, due, ok = ev.transition(a)
	}
	if !ok {
		return Decision{}, time.Time{}, false, nil
	}
	if fits, err := ev.sizeFits(ctx); !fits {
		return Decision{}, time.Time{}, false, err
	}
	if d.Effect != EffectTransition && !ev.removable(ctx, d.Effect) {
		return Decision{}, time.Time{}, false, nil
	}
	return d, due, true, nil
}

// expiration is LCOpAction_CurrentExpiration::check and process
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1116-L1207 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1136-L1227 at v20.2.4)
// but its size filter and lock check: a current entry due by its days or
// the op's date, unless it is a delete marker an entry of its name follows.
// A delete marker is removed. Otherwise the bucket decides: versioning
// enabled writes a delete marker, suspended the null delete marker, and a
// bucket never versioned removes the object. radosgw removes the current
// version of a suspended bucket permanently, so an older version becomes
// current again, where S3 writes the null delete marker, which radosgw's
// own DeleteObject writes there too (docs/exclusions.md, "Lifecycle
// expiration on a versioning-suspended bucket writes the null delete
// marker").
func (ev *evaluation) expiration() (Decision, time.Time, bool) {
	e, op := ev.e, ev.op
	if !e.Current || e.DeleteMarker && ev.s.nextIs(e.Key.Name) {
		return Decision{}, time.Time{}, false
	}
	var due time.Time
	if op.Expiration <= 0 {
		if op.ExpirationDate == nil {
			return Decision{}, time.Time{}, false
		}
		due = *op.ExpirationDate
		if !dateReached(due, ev.c.Now) {
			return Decision{}, time.Time{}, false
		}
	} else {
		if !Expired(e.Mtime, op.Expiration, ev.c.Now, ev.c.Debug, ev.c.Loc) {
			return Decision{}, time.Time{}, false
		}
		due = expiresAt(e.Mtime, op.Expiration, ev.c.Debug)
	}
	effect := EffectRemove
	switch {
	case e.DeleteMarker:
	case ev.bucket.VersioningEnabled():
		effect = EffectMarker
	case ev.bucket.Versioned():
		effect = EffectNullMarker
	}
	return Decision{Action: ActionExpiration, Effect: effect, Key: ev.key}, due, true
}

// deleteMarkerExpiration is LCOpAction_DMExpiration::check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1263-L1308 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1284-L1330 at v20.2.4):
// a delete marker no entry of its name follows, due now, with no size
// filter.
func (ev *evaluation) deleteMarkerExpiration() (Decision, time.Time, bool) {
	if !ev.e.DeleteMarker || ev.s.nextIs(ev.e.Key.Name) {
		return Decision{}, time.Time{}, false
	}
	return Decision{Action: ActionDeleteMarkerExpiration, Effect: EffectRemove, Key: ev.key}, ev.c.Now, true
}

// noncurrentExpiration is LCOpAction_NonCurrentExpiration::check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1209-L1261 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1229-L1282 at v20.2.4)
// but its size filter and lock check: a noncurrent entry due by its
// effective mtime, with more newer versions than NewerNoncurrentVersions.
func (ev *evaluation) noncurrentExpiration() (Decision, time.Time, bool) {
	e, op := ev.e, ev.op
	if e.Current || !Expired(ev.s.PrevMtime, op.NoncurExpiration, ev.c.Now, ev.c.Debug, ev.c.Loc) ||
		ev.s.Earlier <= op.NewerNoncurrent {
		return Decision{}, time.Time{}, false
	}
	due := expiresAt(ev.s.PrevMtime, op.NoncurExpiration, ev.c.Debug)
	return Decision{Action: ActionNoncurrentExpiration, Effect: EffectRemove, Key: ev.key}, due, true
}

// transition is LCOpAction_Transition::check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1321-L1360 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1343-L1382 at v20.2.4)
// but its size filter: never a delete marker; a current entry for a
// transition and a noncurrent one, by its effective mtime, for a noncurrent
// transition; due by the days, or the date when the days are negative.
func (ev *evaluation) transition(a action) (Decision, time.Time, bool) {
	e, t := ev.e, a.transition
	current := a.kind == ActionTransition
	if e.DeleteMarker || e.Current != current {
		return Decision{}, time.Time{}, false
	}
	mtime := e.Mtime
	if !current {
		mtime = ev.s.PrevMtime
	}
	var due time.Time
	if t.Days < 0 {
		if t.Date == nil {
			return Decision{}, time.Time{}, false
		}
		due = *t.Date
		if !dateReached(due, ev.c.Now) {
			return Decision{}, time.Time{}, false
		}
	} else {
		if !Expired(mtime, t.Days, ev.c.Now, ev.c.Debug, ev.c.Loc) {
			return Decision{}, time.Time{}, false
		}
		due = expiresAt(mtime, t.Days, ev.c.Debug)
	}
	return Decision{Action: a.kind, Effect: EffectTransition, Key: ev.key, StorageClass: t.StorageClass}, due, true
}

// sizeFits is pass_size_limit_checks
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L516-L545 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L524-L552 at v20.2.4)
// through Op.SizeMatches, the size read once. A delete marker has no object
// and is read as size 0, unstat'ed. radosgw stats the listed key: for a
// marker with an instance that finds no head and gives 0 too, but a null
// one, listed without an instance, follows the OLH, so the filter fails for
// a current null marker, whose OLH is removed, and reads the current
// version's size for a noncurrent one. The same follow reads the current
// version's size for a noncurrent null version, and radosgw compares the
// size stored, which for a compressed object is its compressed size
// (docs/exclusions.md, "The lifecycle size filter reads a version's own,
// uncompressed size").
func (ev *evaluation) sizeFits(ctx context.Context) (bool, error) {
	if ev.op.SizeGT == nil && ev.op.SizeLT == nil {
		return true, nil
	}
	if !ev.sized {
		ev.sized = true
		if !ev.e.DeleteMarker {
			ev.size, ev.sizeErr = ev.p.Size(ctx, ev.key)
		}
	}
	if ev.sizeErr != nil {
		return false, fmt.Errorf("reading the size of %s: %w", describe(ev.key), ev.sizeErr)
	}
	return ev.op.SizeMatches(ev.size), nil
}

// removable is the lock check of an effect that removes the version
// permanently, pass_object_lock_check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L304-L349 at v19.2.7 and v20.2.4),
// which passes a bucket without object lock unread. radosgw runs it for a
// noncurrent version alone, so its current expiration removes a retained or
// held object (docs/exclusions.md, "Every permanent removal by lifecycle
// runs the object-lock check"). A delete marker over a locked version is no
// removal, nor is the null delete marker over a version that is not null,
// and a delete marker holds no lock.
func (ev *evaluation) removable(ctx context.Context, effect Effect) bool {
	removes := effect == EffectRemove || effect == EffectNullMarker && ev.key.Instance == meta.NullInstance
	if !removes || ev.e.DeleteMarker || !ev.bucket.ObjLockEnabled() {
		return true
	}
	return ev.p.VerifyDelete(ctx, ev.key) == nil
}

// tagsMatch is LCOpFilter_Tags::check and check_tags
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1052-L1114 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1072-L1134 at v20.2.4):
// a delete marker passes, and an op without tags; an object without tags is
// skipped, and one whose tags cannot be read or decoded is skipped with the
// error. The attr is decoded through an iterator, as RGWObjTags::decode
// reads it there, so bytes past its encoding are left unread.
func (ev *evaluation) tagsMatch(ctx context.Context) (bool, error) {
	if ev.op.Tags == nil || ev.e.DeleteMarker {
		return true, nil
	}
	attr, err := ev.p.Tags(ctx, ev.key)
	if err != nil {
		return false, fmt.Errorf("reading the tags of %s: %w", describe(ev.key), err)
	}
	if attr == nil {
		return false, nil
	}
	d := denc.NewDecoder(attr)
	set := tags.Decode(d)
	if err := d.Err(); err != nil {
		return false, fmt.Errorf("decoding the tags of %s: %w", describe(ev.key), err)
	}
	return hasAllTags(*ev.op.Tags, set), nil
}

// hasAllTags reports whether every tag of rule is among obj's, its key with
// its value. radosgw's has_all_tags
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1030-L1050 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1050-L1070 at v20.2.4)
// counts the object's tags that match one of the rule's, so a repeated key
// counts twice and can stand in for a tag the object lacks, and
// s3_expiration_header reads only the first value of a repeated key
// (docs/exclusions.md, "The lifecycle tag filter needs each of a rule's tags
// among the object's").
func hasAllTags(rule, obj tags.Set) bool {
	for _, t := range rule.Tags {
		if !slices.Contains(obj.Tags, t) {
			return false
		}
	}
	return true
}

func describe(k meta.ObjKey) string { return fmt.Sprintf("%q instance %q", k.Name, k.Instance) }
