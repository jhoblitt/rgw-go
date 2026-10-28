package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The events of a transition, named as to_string names them
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_notify_event_type.cc#L31-L56 at v20.2.4).
const (
	lcEventTransition           lifecycle.Event = "s3:LifecycleTransition"
	lcEventTransitionCurrent    lifecycle.Event = "s3:ObjectLifecycle:Transition:Current"
	lcEventTransitionNoncurrent lifecycle.Event = "s3:ObjectLifecycle:Transition:NonCurrent"
)

// attrCryptParts is RGW_ATTR_CRYPT_PARTS, the part lengths an encrypted
// object's decryption takes where its manifest no longer holds its parts
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L170 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L190 at v20.2.4).
const attrCryptParts = meta.AttrCryptPrefix + "part-lengths"

// transition is LCOpAction_Transition::process for d, the transition of e, a
// listed entry of rec, to a storage class inside the cluster, through
// RadosObject::transition and RGWRados::transition_obj
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1469-L1531 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1472-L1587 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5184-L5257 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5417-L5490 at v20.2.4).
// A target class the claim predicate refuses, one the zonegroup tiers to a
// cloud, never reaches it.
//   - An entry the index lists CloudTiered is left where it is.
//   - The target is the bucket's placement rule with d's class, which the
//     zone must have, as valid_placement asks, or the action fails.
//   - The version is read again by its key and goes on only while it is the
//     one listed (transitionListed) and at the RADOS version probe read for
//     Decide, so a write since the listing is decided again at the next pass.
//   - The epoch the version is linked at is chosen (transitionEpoch).
//   - The bucket's record is read again (recheck).
//   - The head's attrs as read are rewritten (transitionAttrs), with its
//     stored bytes, into d's class through copyData with raw set: a new
//     write tag, e's mtime, the compression as stored and the lock state as
//     it is, with no default retention added. The raw copy lands only on the
//     head as read, so a write or a removal since the read fails it; the
//     tails it wrote are deleted and the object is decided again at a later
//     pass. A failure once the head write has landed keeps the version,
//     which names the new tails, for a later pass too.
//   - On a bucket ever versioned, a version the OLH entry names once the
//     link has landed is listed current again should the link have landed
//     below the entry's epoch (keepCurrent).
//
// Then, on Tentacle, the notification of its LifecycleTransition and
// ObjectTransitionCurrent or ObjectTransitionNoncurrent; Squid's in-cluster
// transition publishes none.
func (b *lcBucket) transition(ctx context.Context, rec *op.BucketRecord, probe *lcProbe, d lifecycle.Decision, e op.ObjectEntry) {
	s := b.p.s
	key := d.Key
	attrs := []any{
		slog.String("bucket", b.c.entry.Bucket), slog.String("key", key.Name), slog.String("instance", key.Instance),
		slog.String("storage_class", d.StorageClass),
	}
	if e.Category == rgw.CategoryCloudTiered {
		slog.DebugContext(ctx, "lifecycle leaves an object already tiered to a cloud", attrs...)
		return
	}
	// valid_placement looks the placement up by the rule's own name, so a
	// bucket whose rule names none has no target.
	target := meta.PlacementRule{StorageClass: d.StorageClass}.InheritFrom(rec.Info.PlacementRule)
	if target.Name == "" {
		slog.WarnContext(ctx, "lifecycle cannot transition an object of a bucket whose placement rule names no placement", attrs...)
		return
	}
	if _, err := s.Placement(target); err != nil {
		slog.WarnContext(ctx, "lifecycle cannot transition an object to a storage class its bucket's placement lacks",
			append(attrs, slog.String("placement", target.String()), slog.Any("error", err))...)
		return
	}
	b.p.at(ctx, lcStepDecided, key)
	st, err := s.StatObject(ctx, rec, key)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not read an object due for transition again; it stays", append(attrs, slog.Any("error", err))...)
		return
	}
	same, err := b.transitionListed(ctx, rec, &e, st)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not read an object due for transition again; it stays", append(attrs, slog.Any("error", err))...)
		return
	}
	if !same {
		slog.DebugContext(ctx, "lifecycle skips an object changed since its listing", attrs...)
		return
	}
	if epoch, read := probe.epoch(); read && epoch != st.Epoch {
		slog.DebugContext(ctx, "lifecycle skips an object written since its decision", attrs...)
		return
	}
	dst := key
	if dst.Instance == meta.NullInstance {
		// "bi expects empty instance for the entries created when bucket
		// versioning is not enabled or suspended."
		dst.Instance = ""
	}
	epoch, err := b.transitionEpoch(ctx, rec, dst, st, &e)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not read the OLH of an object due for transition; it stays", append(attrs, slog.Any("error", err))...)
		return
	}
	b.p.at(ctx, lcStepReread, key)
	cur := b.recheck(ctx, attrs, true)
	if cur == nil {
		return
	}
	wattrs, err := transitionAttrs(st)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not transition an object", append(attrs, slog.Any("error", err))...)
		return
	}
	_, err = s.copyData(ctx, st, cur, dst, d.StorageClass, wattrs, e.Mtime, epoch, true)
	switch {
	case errors.Is(err, op.ErrConcurrentModification), errors.Is(err, op.ErrNoSuchKey):
		slog.DebugContext(ctx, "lifecycle leaves an object written since its read for a later pass", append(attrs, slog.Any("error", err))...)
		return
	case err != nil:
		slog.WarnContext(ctx, "lifecycle could not transition an object; it is decided again at a later pass", append(attrs, slog.Any("error", err))...)
		return
	}
	if b.versioned {
		if kerr := b.keepCurrent(ctx, cur, dst); kerr != nil {
			slog.WarnContext(ctx, "lifecycle could not list a transitioned version current again", append(attrs, slog.Any("error", kerr))...)
		}
	}
	slog.InfoContext(ctx, "lifecycle transitioned an object", attrs...)
	if s.release == denc.Squid {
		return
	}
	event := lcEventTransitionNoncurrent
	if !b.versioned || e.IsLatest && !e.DeleteMarker {
		event = lcEventTransitionCurrent
	}
	b.p.notifier.Notify(ctx, lifecycle.Notification{
		Bucket: cur.Info.Bucket, Key: key, ETag: st.ETag, Size: st.Size, VersionID: e.Key.Instance,
		Events: []lifecycle.Event{lcEventTransition, event},
	})
}

// transitionListed reports whether st, the version e lists read again by its
// key, is still e: a version of a bucket ever versioned as listedAs judges
// it, the null version by its index entry, and an object of a bucket never
// versioned at e's mtime and ETag. radosgw compares the head's mtime alone
// and fails the transition with ECANCELED when it differs
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5216-L5220 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5449-L5453 at v20.2.4),
// so a null version whose head's mtime a pending attr's removal moved is
// never transitioned there (docs/ceph-upstream-bugs.md, "radosgw's removal of
// an OLH head's pending attr moves the null version's mtime"); rgw-go
// rewrites it at its listed mtime.
func (b *lcBucket) transitionListed(ctx context.Context, rec *op.BucketRecord, e *op.ObjectEntry, st *op.ObjectState) (bool, error) {
	if b.versioned {
		return b.listedAs(ctx, rec, e, st)
	}
	return st.Exists && st.Mtime.Equal(e.Mtime) && st.ETag == e.ETag, nil
}

// transitionEpoch is the OLH epoch the rewrite of e's version, key, read
// again as st, links at: the name's OLH entry's epoch when the entry names
// that version, so that the link lands current, and the version's own
// versioned_epoch otherwise, which is below the entry's and keeps it
// noncurrent. radosgw links at the version's own versioned_epoch whatever the
// OLH names
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1515-L1516 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1561-L1562 at v20.2.4),
// and the removal of a noncurrent version raises the entry's epoch past the
// current version's, so its link of the current version then takes the
// class's old-epoch path, which writes the version non-current
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1760 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1892-L1901 at v20.2.4;
// docs/ceph-upstream-bugs.md, "radosgw's lifecycle transition hides the
// current version once another version of its key has been removed"). A
// bucket never versioned has no OLH, and a name without an OLH entry, an
// object written before versioning, links at its own epoch.
func (b *lcBucket) transitionEpoch(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, st *op.ObjectState, e *op.ObjectEntry) (uint64, error) {
	if !b.versioned {
		return e.VersionedEpoch, nil
	}
	olh, err := b.p.s.newOLHIndex(rec, key).getOLH(ctx, st)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return e.VersionedEpoch, nil
	case err != nil:
		return 0, err
	case !olhNames(olh, key):
		return e.VersionedEpoch, nil
	}
	return olh.Epoch, nil
}

// olhNames reports whether olh, a name's OLH entry, names key's version as
// the name's current one.
func olhNames(olh rgw.OLHEntry, key meta.ObjKey) bool {
	return olh.Exists && !olh.DeleteMarker && olh.Key == versionKey(key)
}

// keepCurrent lists key's version current again when the transition's link
// of it landed below the OLH entry's epoch while the entry named it, which
// leaves the version unlisted as current. Another version's removal between
// transitionEpoch's read of the entry and the link raises the entry's epoch:
// a noncurrent version's, or the current version's, whose unlink makes the
// version being transitioned current at the new epoch. So it runs after every
// transition on a bucket ever versioned, and does nothing unless the entry
// names the version and the version's entry is not flagged current.
// The epoch read cannot be held through the link. The head write's guard
// compares the head object, not the index's OLH entry, and setOLH answers an
// index refusal by repairing the head and linking again at the same epoch.
// So keepCurrent reads the entry again once the link has landed and, while it
// still names the version and the version's instance entry is not flagged
// current, links the version again at the entry's epoch, guarded on the OLH
// head as read (olhRelink), waiting on other writers' pending modifications
// as a re-link does (relistVersion), up to relistRounds rounds.
func (b *lcBucket) keepCurrent(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) error {
	s := b.p.s
	idx := s.newOLHIndex(rec, key)
	read := s.getInstanceEntry
	if key.Instance == "" {
		read = s.nullDataEntry
	}
	var w olhCondWait
	for round := 0; round < relistRounds; {
		head, err := s.readRelinkHead(ctx, rec, key)
		if err != nil || !head.Exists {
			return err
		}
		olh, err := idx.getOLH(ctx, head)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return nil
		case err != nil:
			return err
		case !olhNames(olh, key):
			return nil
		}
		en, err := read(ctx, rec, key)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return nil
		case err != nil:
			return err
		case en.Flags&rgw.FlagCurrent != 0:
			return nil
		}
		wait, refuse, err := s.waitOLHPending(ctx, idx, head, &w)
		switch {
		case err != nil:
			return err
		case wait:
			continue
		}
		round++
		err = s.setOLH(ctx, rec, key, olhLink{Epoch: olh.Epoch, Relink: &olhRelink{head: head, pendingOK: !refuse}})
		if !errors.Is(err, errRelinkMoved) {
			return err
		}
	}
	return fmt.Errorf("listing %s current again: every one of %d rounds lost to another writer", versionName(rec, key), relistRounds)
}

// transitionAttrs is the attrs transition_obj hands copy_obj_data for st:
// its own, without its write tags, which transition_obj erases
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5227-L5228 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5460-L5461 at v20.2.4),
// with the part lengths of an encrypted object (cryptPartLengths), and:
//   - without the OLH attrs, which a null version's head as read carries as
//     its key's OLH head and the head write's reset keeps as they are on
//     disk, where radosgw writes back the ones it read, undoing a link
//     applied since (docs/exclusions.md, "A lifecycle transition writes none
//     of the OLH attrs it read");
//   - without a composite checksum, which the rewritten object, without
//     parts, would misreport as its whole body's, as a streamed copy drops it
//     (compositeChecksum), where radosgw keeps it (docs/exclusions.md, "A
//     copy that streams its data drops a composite checksum").
func transitionAttrs(st *op.ObjectState) (map[string][]byte, error) {
	attrs := maps.Clone(st.Attrs)
	delete(attrs, meta.AttrIDTag)
	delete(attrs, meta.AttrTailTag)
	maps.DeleteFunc(attrs, func(name string, _ []byte) bool { return strings.HasPrefix(name, meta.AttrOLHPrefix) })
	if err := cryptPartLengths(attrs); err != nil {
		return nil, fmt.Errorf("the part lengths of %s: %w", objName(st), err)
	}
	if b, ok := attrs[meta.AttrCksum]; ok {
		if c, err := cksum.DecodeAttr(b); err == nil {
			composite, err := compositeChecksum(st, c)
			if err != nil {
				return nil, err
			}
			if composite {
				delete(attrs, meta.AttrCksum)
			}
		}
	}
	return attrs, nil
}

// cryptPartLengths is fixup_manifest_to_parts_len on attrs
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5155-L5182 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5388-L5415 at v20.2.4):
// an object with a manifest and user.rgw.crypt.mode but no
// user.rgw.crypt.part-lengths gets the latter from the stored manifest, as
// read_manifest_parts reads it, encoded as a vector<size_t>, before the
// rewrite replaces the manifest, without which a multipart object would be
// decrypted as one part (docs/ceph-upstream-bugs.md, "radosgw's lifecycle
// transition of an encrypted multipart object drops its part lengths"). A
// manifest that does not decode is an error, as radosgw's -EIO fails the
// transition.
func cryptPartLengths(attrs map[string][]byte) error {
	mb, ok := attrs[meta.AttrManifest]
	if !ok {
		return nil
	}
	if _, encrypted := attrs[meta.AttrCryptMode]; !encrypted {
		return nil
	}
	if _, kept := attrs[attrCryptParts]; kept {
		return nil
	}
	var parts []uint64
	if len(mb) > 0 {
		m, err := decodeAttr(mb, meta.DecodeManifest)
		if err != nil {
			return fmt.Errorf("decoding the manifest: %w", err)
		}
		if parts, err = manifestPartLengths(m); err != nil {
			return err
		}
	}
	e := denc.NewEncoder()
	e.U32(uint32(len(parts))) //nolint:gosec // a part count is far below 2^32
	for _, n := range parts {
		e.U64(n)
	}
	attrs[attrCryptParts] = e.Bytes()
	return nil
}

// manifestPartLengths is read_manifest_parts's walk of m
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_crypt.cc#L685-L710 at v19.2.7 and v20.2.4):
// the stripes from obj_begin to obj_end, a part starting at each stripe 0 and
// each stripe's get_stripe_size added to its part. The stripe count restarts
// exactly where the part number moves on a manifest of rules, and an
// explicit manifest's iterator never moves it, so each of its pieces starts
// a part. Like walkTails it fails on a walk that does not move forward or
// passes meta.MaxWalkStripes stripes, where radosgw's loop never ends.
func manifestPartLengths(m meta.Manifest) ([]uint64, error) {
	it, err := m.Seek(0)
	if err != nil {
		return nil, err
	}
	var parts []uint64
	var part int32
	for n := 0; !it.Done(); n++ {
		if n >= meta.MaxWalkStripes {
			return nil, fmt.Errorf("%w: more than %d", meta.ErrTooManyStripes, meta.MaxWalkStripes)
		}
		if n == 0 || m.ExplicitObjs || it.PartID() != part {
			parts = append(parts, 0)
		}
		part = it.PartID()
		parts[len(parts)-1] += it.StripeSize()
		prev := it.Ofs()
		if err := it.Next(); err != nil {
			return nil, err
		}
		if !it.Done() && it.Ofs() <= prev {
			return nil, fmt.Errorf("%w: manifest iteration does not advance past offset %d", denc.ErrMalformed, prev)
		}
	}
	return parts, nil
}
