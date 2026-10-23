package driver

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// CompleteMultipartUpload is RGWCompleteMultipart::execute
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6367-L6541 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7163-L7431 at v20.2.4)
// over RadosMultipartUpload::complete
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3433-L3640 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4279-L4490 at v20.2.4).

// maxMetaDeleteRetries is MAX_DELETE_RETRIES, the removals of the meta
// object a completion tries while parts keep racing it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6497).
const maxMetaDeleteRetries = 15

// partHeadStats bounds the part head stats a completion has in flight.
const partHeadStats = 16

// mergeCompression is complete's cs_info handling for the handled-th part
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3531-L3566):
// after the first part, one that differs from the parts before in being
// compressed, in its compressor, or in its compressor message when the
// object has one, is ErrInvalidPart; a compressed part's blocks are
// appended, shifted past the original and the stored bytes before them.
func mergeCompression(cs *meta.CompressionInfo, compressed *bool, part meta.CompressionInfo, handled int) error {
	partCompressed := part.Type != "none"
	if handled > 0 && (partCompressed != *compressed || cs.Type != part.Type ||
		(cs.CompressorMessage != nil && !equalMessage(cs.CompressorMessage, part.CompressorMessage))) {
		return fmt.Errorf("%w: compression %s changed to %s between parts", op.ErrInvalidPart, cs.Type, part.Type)
	}
	if !partCompressed {
		return nil
	}
	var newOfs uint64
	if n := len(cs.Blocks); n > 0 {
		newOfs = cs.Blocks[n-1].NewOfs + cs.Blocks[n-1].Len
	}
	for _, b := range part.Blocks {
		cs.Blocks = append(cs.Blocks, meta.CompressionBlock{OldOfs: b.OldOfs + cs.OrigSize, NewOfs: newOfs, Len: b.Len})
		newOfs += b.Len
	}
	if !*compressed {
		cs.Type = part.Type
		if part.CompressorMessage != nil {
			cs.CompressorMessage = part.CompressorMessage
		}
	}
	cs.OrigSize += part.OrigSize
	*compressed = true
	return nil
}

// equalMessage is std::optional<int32_t>'s operator==.
func equalMessage(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// assembly is what complete builds from the stored parts: the object's
// manifest, compression and ETag, its stored and accounted sizes, what
// completing it retires, and the stored records the request's parts
// matched, one for one in number order.
type assembly struct {
	manifest   meta.Manifest
	cs         meta.CompressionInfo
	compressed bool
	etag       string
	size       uint64
	accounted  uint64
	cleanup    partCleanup
	parts      []meta.UploadPartInfo
}

// assemble is complete's validation and assembly
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3462-L3590)
// of parts, sorted, against stored, the parts listed for upload uploadID of
// key, page by page as list_parts returns them: on the last page the counts
// must agree; each part, in lockstep, must be at least minPartSize unless it
// is the last, have the number and the unquoted ETag of the stored one, and
// a manifest. Each is appended to the object's manifest, its head's index
// entry, "<prefix>.<n>" in the multipart namespace, is to be retired, and so
// is its history (retirePart). A part whose manifest radosgw would append as
// an explicit one is ErrInvalidPart (docs/exclusions.md, "An explicit part
// manifest is not appended"). Once every part matched, the stored records
// are the assembly's parts.
func (s *Store) assemble(key meta.ObjKey, uploadID string, stored []meta.UploadPartInfo, parts []op.CompletePart) (*assembly, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("%w: a completion names no part", op.ErrInvalidPart)
	}
	a := &assembly{manifest: meta.NewManifest(), cs: meta.NewCompressionInfo()}
	etags := make([]string, 0, len(parts))
	for start := 0; ; start += listPartsChunk {
		end := min(start+listPartsChunk, len(stored))
		if end == len(stored) && len(stored) != len(parts) {
			return nil, fmt.Errorf("%w: %d parts listed of %d uploaded", op.ErrInvalidPart, len(parts), len(stored))
		}
		for i := start; i < end && i < len(parts); i++ {
			if err := s.assemblePart(a, key, uploadID, stored[i], parts[i], i, len(parts)); err != nil {
				return nil, err
			}
			etags = append(etags, stored[i].ETag)
		}
		if end == len(stored) {
			break
		}
	}
	etag, err := op.MultipartETag(etags)
	if err != nil {
		return nil, err
	}
	a.etag, a.parts = etag, stored
	return a, nil
}

// assemblePart is one step of complete's loop
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3478-L3577)
// for part p, the i-th of n requested, against st, the i-th stored.
func (s *Store) assemblePart(a *assembly, key meta.ObjKey, uploadID string, st meta.UploadPartInfo, p op.CompletePart, i, n int) error {
	if i < n-1 && st.AccountedSize < s.mp.minPartSize {
		return fmt.Errorf("%w: part %d of %d bytes", op.ErrEntityTooSmall, st.Num, st.AccountedSize)
	}
	if int(int32(st.Num)) != p.Number { //nolint:gosec // compared as (int)obj_iter->first
		return fmt.Errorf("%w: part %d requested where part %d was uploaded", op.ErrInvalidPart, p.Number, st.Num)
	}
	if op.Unquote(p.ETag) != st.ETag {
		return fmt.Errorf("%w: part %d's ETag %s", op.ErrInvalidPart, p.Number, p.ETag)
	}
	if partManifestEmpty(st.Manifest) {
		return fmt.Errorf("%w: part %d has an empty manifest", op.ErrInvalidPart, st.Num)
	}
	if err := a.manifest.Append(st.Manifest); err != nil {
		return fmt.Errorf("%w: part %d: %w", op.ErrInvalidPart, st.Num, err)
	}
	prefix := st.Manifest.Prefix
	if prefix != "" {
		a.cleanup.seen(st.Num, prefix)
	}
	if err := mergeCompression(&a.cs, &a.compressed, st.Compression, i); err != nil {
		return err
	}
	head := meta.ObjKey{Name: meta.MultipartPartName(cmp.Or(prefix, meta.MultipartPrefix(key.Name, uploadID)), st.Num), NS: meta.NSMultipart}
	a.cleanup.removeObjs = append(a.cleanup.removeObjs, rgw.ObjKey{Name: head.IndexKeyName()})
	if err := s.retirePart(st, &a.cleanup); err != nil {
		return err
	}
	a.size += st.Size
	a.accounted += st.AccountedSize
	return nil
}

// checkPartHeads stats the head object of each of parts, their first
// stripe, and answers ErrInvalidPart when one is missing: its data was
// removed, as a GC pass removes the parts of a completed object that was
// overwritten or deleted, and a head naming it would read as NoSuchKey.
// radosgw checks only the part infos (docs/exclusions.md,
// "CompleteMultipartUpload checks the part heads and keeps the parts until
// its head is written").
func (s *Store) checkPartHeads(ctx context.Context, parts []meta.UploadPartInfo) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(partHeadStats)
	for i := range parts {
		p := &parts[i]
		g.Go(func() error {
			it, err := p.Manifest.Seek(0)
			if err != nil {
				return fmt.Errorf("%w: part %d's manifest: %w", op.ErrInvalidPart, p.Num, err)
			}
			obj, placement, _ := it.Location()
			pool, ok := s.dataPool(placement, obj.Bucket)
			if !ok {
				return fmt.Errorf("%w: no data pool for part %d's placement %v", op.ErrUnknown, p.Num, placement)
			}
			stripe := meta.Stripe{Obj: obj}
			h, err := s.pools.get(gctx, pool)
			if err == nil {
				if loc := stripe.Locator(); loc != "" {
					h = h.WithLocator(loc)
				}
				rop := radosclient.NewReadOp()
				rop.Stat()
				_, err = h.Read(gctx, stripe.OID(), rop, radosclient.OpFlagNone)
			}
			if errors.Is(err, radosclient.ErrNotFound) {
				return fmt.Errorf("%w: part %d's head %s is missing", op.ErrInvalidPart, p.Num, stripe.OID())
			}
			return op.FromRADOS(err, op.ScopeObject)
		})
	}
	return g.Wait()
}

// checkPreviouslyCompleted is RGWCompleteMultipart::check_previously_completed
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6543-L6580;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7433-L7504):
// the key's current version (currentVersion) carries the ETag the request's
// parts make. Its Checksum is the one the head stores, nil when it stores none
// or one that does not decode, as Tentacle's check decodes it and answers 200
// either way
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7474-L7501 at v20.2.4).
func (s *Store) checkPreviouslyCompleted(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, parts []op.CompletePart) (*op.ObjectState, bool) {
	st, err := s.currentVersion(ctx, rec, key)
	if err != nil || !st.Exists {
		return nil, false
	}
	want, err := op.CompleteETag(parts)
	return st, err == nil && st.ETag == want
}

// currentVersion is the state of the version current on key's name, its OLH
// followed as a read follows it, a delete marker's state one that does not
// exist. The check for an earlier completion reads it so, as radosgw's
// check_previously_completed reads the object before the completion gives it
// an instance
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6438-L6445
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6462-L6469 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7249-L7256
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7284-L7291 at v20.2.4).
func (s *Store) currentVersion(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.objectState(ctx, rec, olhHeadKey(key), false)
}

// versionID is the version id a completion's result reports for the version
// key names: its instance, empty for the null version.
func versionID(key meta.ObjKey) string {
	if key.Instance == meta.NullInstance {
		return ""
	}
	return key.Instance
}

// sameCompletion reports that the head st is the object a reaches: the same
// ETag, size, prefix and rules, so the same part objects.
func sameCompletion(st *op.ObjectState, a *assembly) bool {
	m := st.Manifest
	return m != nil && !m.ExplicitObjs && st.ETag == a.etag && m.ObjSize == a.manifest.ObjSize &&
		m.Prefix == a.manifest.Prefix && maps.Equal(m.Rules, a.manifest.Rules)
}

// Complete implements op.MultipartStore as CompleteMultipartUpload completes
// an upload into up.Key, whose instance is the version the op chose, as
// RGWCompleteMultipart gives the object an instance on a bucket whose
// versioning is enabled
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6462-L6469 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7284-L7291 at v20.2.4):
//   - the RGWCompleteMultipart lock on the meta object (lockMeta), under a
//     cookie of this completion's own (newLockCookie), whose ENOENT, when
//     the key already carries the parts' ETag (checkPreviouslyCompleted), is
//     radosgw's re-sent completion, answered with that stored ETag and
//     checksum
//     (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6438-L6441;
//     v19.2.7
//     https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6584,
//     v20.2.4
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7472);
//     any other refusal is 500 "This multipart completion is already in
//     progress";
//   - the meta object's attrs and version (readMeta). A completion record
//     there is an earlier completion's whose meta object outlived its head
//     write (replayRecorded);
//   - its parts (listAllParts), validated and assembled (assemble);
//   - the head of every part (checkPartHeads), and the key's versions, the
//     current one and every noncurrent one (uploadVersion): one whose manifest
//     names this upload's objects, with no record, is an earlier completion a
//     gateway without the record left behind, which is finished when it is
//     this completion's object, with the checksum and the version id of that
//     version, and otherwise refused with NoSuchUpload (tracker #80896). A
//     version with an instance is linked already, as the current version's
//     read finds only the OLH's target and the listing only list entries; the
//     null version, however it was found, is first linked as a replay links
//     it (linkRecorded) when its list entry is another null version's
//     (byData);
//   - the checksum of the matched parts, compared with up.SuppliedChecksum,
//     or NotImplemented for an upload type rgw-go does not compute
//     (combineChecksums), so a completion that has both a part fault and a
//     checksum fault answers the part fault;
//   - the lock renewed with the completion record written (renewAndRecord),
//     naming up.Key's instance, or 500 as for a held lock;
//   - the head written as write_meta writes it for complete
//     (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3588-L3637):
//     the meta object's attrs with the ETag, the compression info, the
//     combined checksum and the object lock settings, under up.WriteTag or
//     a random tag, created,
//     atomic, its tail modified, with no bytes added to the quota cache,
//     with up's conditions, and at the OLH epoch RGWCompleteMultipart passes,
//     0 for every S3 request
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3636 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4480 at v20.2.4),
//     so the head is a version wherever a PUT's would be one, its condition
//     held through its link (put_versioned.go). A head write that certainly
//     wrote nothing takes the record back (clearRecord). Once a head is
//     written the record stays, whatever its completion or its link
//     answered: that head names the parts, and a removal of its version
//     queues them for the GC, so a retry must find the record rather than
//     complete the upload again (docs/ceph-upstream-bugs.md, "radosgw's
//     CompleteMultipartUpload leaves its upload whole when its head lands and
//     write_meta fails"). A lost race without conditions is success, as
//     radosgw answers it, and the parts' current objects then leak
//     (docs/ceph-upstream-bugs.md, "A CompleteMultipartUpload that loses its
//     head write's race leaks its parts");
//   - then the parts' history to the GC and the meta object's removal,
//     which also retires the parts' index entries (retireUpload).
//
// A completion refused before its head write leaves the upload whole: its
// parts' entries are not sent with the head write's index change, whose
// cancel would apply them (tracker #80907), and their history is queued
// for the GC only after the head is written (docs/exclusions.md,
// "CompleteMultipartUpload checks the part heads and keeps the parts until
// its head is written"). The lock is released on every path that leaves
// the meta object. Once the lock is taken the completion runs to its end
// whatever the client does.
func (s *Store) Complete(ctx context.Context, up *op.Upload, parts []op.CompletePart) (*op.PutResult, error) {
	rec, key := up.Bucket, up.Key
	parts = op.SortCompleteParts(parts)
	ref, err := s.metaRef(ctx, rec, key, up.ID)
	cookie := newLockCookie()
	if err == nil {
		err = s.lockMeta(ctx, ref, cookie)
	} else if !errors.Is(err, radosclient.ErrNotFound) {
		return nil, err
	}
	if err != nil {
		// radosgw opens a missing pool, creating it, and its lock then
		// meets no object, so a missing pool reads as ENOENT here.
		if errors.Is(err, radosclient.ErrNotFound) {
			if st, ok := s.checkPreviouslyCompleted(ctx, rec, key, parts); ok {
				slog.InfoContext(ctx, "multipart completion already completed", slog.String("upload", up.ID))
				return &op.PutResult{ETag: st.ETag, Size: st.Size, Mtime: st.Mtime, Epoch: st.Epoch, Version: versionID(st.Key), Checksum: st.Checksum}, nil
			}
		}
		return nil, fmt.Errorf("%w: locking upload %s: %w", op.ErrCompletionInProgress, up.ID, err)
	}
	ctx = context.WithoutCancel(ctx)
	res, released, err := s.completeLocked(ctx, up, ref, cookie, parts)
	if !released {
		s.unlockMeta(ctx, ref, cookie)
	}
	return res, err
}

// completeLocked is Complete under the lock; the bool reports that the lock
// went with the meta object.
func (s *Store) completeLocked(ctx context.Context, up *op.Upload, ref mpRef, cookie string, parts []op.CompletePart) (*op.PutResult, bool, error) {
	rec, key := up.Bucket, up.Key
	ms, err := s.readMeta(ctx, ref)
	if errors.Is(err, radosclient.ErrNotFound) {
		// get_obj_attrs's -ENOENT reaches the client as it is
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6448-L6453).
		return nil, false, fmt.Errorf("%w: the meta object %s is gone", op.ErrNoSuchKey, ref.oid)
	}
	if err != nil {
		return nil, false, err
	}
	if tag, ok := ms.attrs[attrMPCompletionTag]; ok {
		return s.replayRecorded(ctx, up, ref, ms, parts, tag)
	}
	stored, err := s.listAllParts(ctx, ref, up.ID)
	if err != nil {
		return nil, false, err
	}
	a, err := s.assemble(key, up.ID, stored, parts)
	if err != nil {
		return nil, false, err
	}
	if herr := s.checkPartHeads(ctx, stored); herr != nil {
		return nil, false, herr
	}
	cur, err := s.uploadVersion(ctx, up, ref, stored)
	if err != nil {
		return nil, false, err
	}
	if cur != nil {
		if !sameCompletion(cur, a) {
			return nil, false, fmt.Errorf("%w: upload %s already completed %s with other parts", op.ErrNoSuchUpload, up.ID, key.Name)
		}
		if isOLHHead(cur) {
			if lerr := s.linkRecorded(ctx, up, cur, cur.Attrs[meta.AttrIDTag], byData); lerr != nil {
				return nil, false, lerr
			}
		}
		slog.InfoContext(ctx, "finishing a multipart completion whose meta object outlived its head write", slog.String("upload", up.ID))
		released := s.retireUpload(ctx, up, ref, ms, &a.cleanup)
		return &op.PutResult{ETag: a.etag, Size: cur.Size, Mtime: cur.Mtime, Epoch: cur.Epoch, Version: versionID(cur.Key), Checksum: cur.Checksum}, released, nil
	}
	sum, err := combineChecksums(s.uploadChecksum(ms.info), a.parts, up.SuppliedChecksum)
	if err != nil {
		return nil, false, fmt.Errorf("completing upload %s: %w", up.ID, err)
	}
	tag := cmp.Or(up.WriteTag, s.w.randTag())
	if lerr := s.renewAndRecord(ctx, ref, cookie, tag, recordedInstance(key)); lerr != nil {
		return nil, false, fmt.Errorf("%w: renewing the lock of upload %s: %w", op.ErrCompletionInProgress, up.ID, lerr)
	}
	attrs := maps.Clone(ms.attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	delete(attrs, attrMPCompletionInstance)
	attrs[meta.AttrETag] = []byte(a.etag)
	if ms.info.Retention != nil {
		attrs[meta.AttrObjectRetention] = encodeAt(*ms.info.Retention, s.release)
	}
	if ms.info.LegalHold != nil {
		attrs[meta.AttrObjectLegalHold] = encodeAt(*ms.info.LegalHold, s.release)
	}
	if a.compressed {
		attrs[meta.AttrCompression] = encodeAt(a.cs, s.release)
	}
	if sum != nil {
		attrs[meta.AttrCksum] = encodeAt(*sum, s.release)
	}
	hw := &headWrite{
		rec: rec, key: key, tag: tag, manifest: &a.manifest, attrs: attrs, mtime: s.now(), create: true, modifyTail: true,
		completeMultipart: true, size: a.size, accountedSize: a.accounted, ifMatch: up.IfMatch, ifNoneMatch: up.IfNoneMatch,
		olhEpoch: new(uint64(0)),
	}
	hr, err := s.writeMeta(ctx, hw, s.newIndexOp(rec, key, tag))
	if err != nil {
		if !hr.written && !errors.Is(err, radosclient.ErrTimedOut) {
			// The head was not written, so a retry may still complete the
			// upload; after a timeout it may yet land, and once it landed it
			// names the parts, so the record stays.
			s.clearRecord(ctx, ref)
		}
		return nil, false, err
	}
	mtime, version := hr.mtime, versionID(key)
	if hr.canceled {
		// A lost race leaves no head to read a time from; this is the one
		// the head would have carried, and there is no version.
		mtime, version = hw.mtime, ""
	}
	released := s.retireUpload(ctx, up, ref, ms, &a.cleanup)
	return &op.PutResult{ETag: a.etag, Size: a.size, Mtime: mtime, Epoch: hr.epoch, Version: version, Checksum: sum}, released, nil
}

// replayRecorded answers a completion whose meta object holds the record of
// an earlier one, tag, as ceph/ceph#72103 at 2c1db8239cc does: when the head
// the record names (recordedHead) still carries tag and the ETag the
// request's parts make, that completion took effect, and this one finishes
// it, removing the meta object with the parts' entries and retiring what the
// head does not name, without writing a head or checking the request's
// conditions or its supplied checksum, and answers with the head's ETag,
// checksum and version id, not the instance up.Key carries. A version is
// first linked under its key's OLH when no link of it landed (linkRecorded).
// Otherwise that head was never written, or was replaced or deleted and its
// parts queued for the GC, and a head over them would lose the object's data:
// NoSuchUpload, with nothing written. A version the record names whose head
// is gone cannot be linked, and answers InternalError instead, the meta
// object kept.
func (s *Store) replayRecorded(ctx context.Context, up *op.Upload, ref mpRef, ms *metaState, parts []op.CompletePart, tag []byte) (*op.PutResult, bool, error) {
	refused := fmt.Errorf("%w: an earlier completion of upload %s did not take effect, or its object was replaced", op.ErrNoSuchUpload, up.ID)
	cur, err := s.recordedHead(ctx, up.Bucket, up.Key, ms.attrs)
	if err != nil {
		return nil, false, err
	}
	// An OLH head without a manifest holds the OLH attrs alone, its null
	// version gone.
	versioned := cur.Key.Instance != "" || isOLHHead(cur)
	if versioned && (!cur.Exists || cur.Manifest == nil) {
		return nil, false, fmt.Errorf("%w: %s, which an earlier completion of upload %s recorded, has no head", op.ErrInternalError, versionName(up.Bucket, cur.Key), up.ID)
	}
	if !cur.Exists || !bytes.Equal(cur.Attrs[meta.AttrIDTag], tag) {
		slog.InfoContext(ctx, "not completing an upload whose earlier completion did not take effect or was replaced", slog.String("upload", up.ID))
		return nil, false, refused
	}
	if want, eerr := op.CompleteETag(parts); eerr != nil || cur.ETag != want {
		return nil, false, fmt.Errorf("%w: upload %s already completed %s with other parts", op.ErrNoSuchUpload, up.ID, up.Key.Name)
	}
	if versioned {
		if lerr := s.linkRecorded(ctx, up, cur, tag, byTag); lerr != nil {
			return nil, false, lerr
		}
	}
	stored, err := s.listAllParts(ctx, ref, up.ID)
	if err != nil {
		return nil, false, err
	}
	cleanup, err := s.retainedCleanup(stored, manifestPrefixes(cur.Manifest))
	if err != nil {
		return nil, false, err
	}
	slog.InfoContext(ctx, "finishing a recorded multipart completion whose meta object outlived its head write", slog.String("upload", up.ID))
	released := s.retireUpload(ctx, up, ref, ms, &cleanup)
	return &op.PutResult{ETag: cur.ETag, Size: cur.Size, Mtime: cur.Mtime, Epoch: cur.Epoch, Version: versionID(cur.Key), Checksum: cur.Checksum}, released, nil
}

// recordedLink is what the index tells of a version a recorded completion
// wrote: its key, the null version's instance empty; the completion's write
// tag; its instance entry, when found; whether that entry is the
// completion's, completed under its tag or prepared under it; and whether a
// link of it landed.
type recordedLink struct {
	key       meta.ObjKey
	writeTag  string
	entry     rgw.DirEntry
	found     bool
	ours      bool
	completed bool
	linked    bool
}

// linkedBy is how recordedLinkState tells that the list entry at a version's
// versioned epoch is that version's own.
type linkedBy uint8

const (
	// byTag takes the list entry for the version's when it carries the
	// completion's write tag, which only that version's link writes there:
	// the test for a version whose head still carries the recorded tag.
	byTag linkedBy = iota
	// byData takes the list entry for the version's when it has the instance
	// entry's ETag and size. An attr change rewrites the head's idtag and the
	// instance entry's tag, but not the list entry's (RGWRados::set_attrs,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6853-L6860
	// and
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6909 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7467-L7474
	// and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7559 at v20.2.4;
	// SetObjectAttrs), so the tags of a version linked before such a change
	// differ. An earlier null version with the same ETag and size passes for
	// the version (docs/exclusions.md, "CompleteMultipartUpload finishes an
	// earlier completion of its upload instead of writing it again").
	byData
)

// recordedLinkState reads the index entries of cur, the head of a version a
// recorded completion wrote under tag: its instance entry (getInstanceEntry)
// and, once that entry is the completion's, completed and carrying a
// versioned epoch, the list entry at that epoch (listEntry). The version is
// linked only when that list entry is its own, as by tells. Only a link
// writes list entries (BIVerObjEntry::write,
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1388-L1427 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1514-L1554 at v20.2.4),
// and complete_op rewrites the entry read_key_entry finds without touching
// its versioned epoch
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1043-L1047 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1178-L1182 at v20.2.4),
// so a null version written over an earlier one carries that one's epoch,
// whose list entry is the earlier null version's.
func (s *Store) recordedLinkState(ctx context.Context, rec *op.BucketRecord, cur *op.ObjectState, tag []byte, by linkedBy) (recordedLink, error) {
	rl := recordedLink{key: cur.Key, writeTag: strings.TrimSuffix(string(tag), "\x00")}
	if rl.key.Instance == meta.NullInstance {
		rl.key.Instance = ""
	}
	en, err := s.getInstanceEntry(ctx, rec, rl.key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return rl, nil
	case err != nil:
		return rl, op.FromRADOS(fmt.Errorf("reading the index entry of %s: %w", versionName(rec, rl.key), err), op.ScopeObject)
	}
	rl.entry, rl.found = en, true
	rl.completed = en.Exists && en.Tag == rl.writeTag
	rl.ours = rl.completed || slices.ContainsFunc(en.PendingMap, func(p rgw.PendingEntry) bool { return p.Tag == rl.writeTag })
	if !rl.completed || en.VersionedEpoch == 0 {
		return rl, nil
	}
	le, ok, err := s.listEntry(ctx, rec, rl.key, en)
	if err != nil {
		return rl, op.FromRADOS(fmt.Errorf("reading the list entry of %s: %w", versionName(rec, rl.key), err), op.ScopeObject)
	}
	switch by {
	case byData:
		rl.linked = ok && le.Meta.ETag == en.Meta.ETag && le.Meta.Size == en.Meta.Size
	default:
		rl.linked = ok && le.Tag == rl.writeTag
	}
	return rl, nil
}

// linkRecorded makes sure cur, the version's head a recorded completion of up
// wrote under tag, is linked under its key's OLH before the replay answers
// for it, where ceph/ceph#72103's replay answers whether or not that link
// landed. recordedLinkState tells, testing the list entry as by says: a
// version linked needs nothing; one whose
// entry that completion completed but no link listed, or one it prepared and
// never completed, which is first completed from cur as its completion would
// have, is linked now at the epoch the class assigns, under up's conditions
// for an instance and unconditionally for the null version, as linkCond
// gives a null write's link, the version kept whatever the link answers so
// that its parts stay with the upload. An entry gone or another write's, or
// a link that does not land, cannot be established safely: InternalError,
// with the meta object kept for a later retry or an abort, which removes the
// version (Store.Abort); a condition that no longer holds answers as a
// completion's would (docs/exclusions.md, "A CompleteMultipartUpload records
// itself on the meta object before its head write").
func (s *Store) linkRecorded(ctx context.Context, up *op.Upload, cur *op.ObjectState, tag []byte, by linkedBy) error {
	rec := up.Bucket
	rl, err := s.recordedLinkState(ctx, rec, cur, tag, by)
	if err != nil {
		return err
	}
	key := rl.key
	unsafe := func(why string, err error) error {
		return fmt.Errorf("%w: linking %s, which an earlier completion of upload %s wrote: %s: %w",
			op.ErrInternalError, versionName(rec, key), up.ID, why, err)
	}
	switch {
	case !rl.found:
		return unsafe("its index entry is gone", radosclient.ErrNotFound)
	case rl.linked:
		return nil
	case !rl.ours:
		return unsafe("its index entry is another write's", radosclient.ErrCanceled)
	}
	slog.InfoContext(ctx, "linking the version a recorded multipart completion wrote and never linked",
		slog.String("upload", up.ID), slog.String("instance", key.Instance))
	if !rl.completed {
		if cerr := s.completeRecordedEntry(ctx, rec, cur, rl); cerr != nil {
			return unsafe("completing its index entry", cerr)
		}
	}
	hw := &headWrite{rec: rec, key: key, ifMatch: up.IfMatch, ifNoneMatch: up.IfNoneMatch}
	lerr := s.setOLH(ctx, rec, key, olhLink{Cond: s.linkCond(hw), KeepVersion: true})
	switch {
	case lerr == nil:
		return nil
	case errors.Is(lerr, op.ErrPreconditionFailed):
		return lerr
	}
	return unsafe("its link", lerr)
}

// completeRecordedEntry completes the instance entry of rl's version, which
// its recorded completion prepared under its write tag and never completed,
// from cur, the version's head, as that completion's own index completion
// would have, sent and awaited (completeSync).
func (s *Store) completeRecordedEntry(ctx context.Context, rec *op.BucketRecord, cur *op.ObjectState, rl recordedLink) error {
	ref, err := s.headRef(ctx, rec, rl.key)
	if err != nil {
		return err
	}
	x := s.newIndexOp(rec, rl.key, rl.writeTag)
	x.bilogFlags |= rgw.BILogFlagVersionedOp
	return x.completeSync(ctx, rgw.EntryVer{Pool: ref.pool.ID(), Epoch: cur.Epoch}, s.attrsEntry(cur, nil, cur.Mtime))
}

// retireUpload is what follows a completion's head write: the chain of
// cleanup, the parts' history, to the GC under the upload id,
// synchronously, as cleanup_part_history sends it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3132-L3146),
// then the meta object's removal with cleanup's index entries, retried up
// to maxMetaDeleteRetries times while parts race it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6493-L6530):
// after each ECANCELED the meta object is read again and
// cleanup_orphaned_parts runs, whose parts were registered after the
// listing and so are not the object's. The prefixes cleanup handled carry
// over, which keeps the object's own parts out of that cleanup. A removal
// that keeps failing is logged, as radosgw logs it; the completion stands.
// released reports that the lock went with the meta object.
func (s *Store) retireUpload(ctx context.Context, up *op.Upload, ref mpRef, ms *metaState, cleanup *partCleanup) (released bool) {
	if len(cleanup.chain) > 0 {
		s.enqueueGC(ctx, cleanup.chain, up.ID)
	}
	d := metaDelete{
		rec: up.Bucket, key: up.Key, ref: ref, version: ms.version, mtime: ms.mtime,
		removeObjs: cleanup.removeObjs, accounted: ms.size,
	}
	for attempt := range maxMetaDeleteRetries {
		err := s.deleteMeta(ctx, d)
		if err == nil {
			return true
		}
		if !errors.Is(err, radosclient.ErrCanceled) || attempt == maxMetaDeleteRetries-1 {
			slog.ErrorContext(ctx, "failed to remove the multipart meta object", slog.String("oid", ref.oid), slog.Any("error", err))
			return false
		}
		ms2, err := s.readMeta(ctx, ref)
		if err != nil {
			if !errors.Is(err, radosclient.ErrNotFound) {
				slog.ErrorContext(ctx, "failed to remove the multipart meta object", slog.String("oid", ref.oid), slog.Any("error", err))
			}
			return false
		}
		if err := s.cleanupOrphanedParts(ctx, up, ref, cleanup); err != nil {
			slog.ErrorContext(ctx, "failed to clean up orphaned parts", slog.String("upload", up.ID), slog.Any("error", err))
		}
		d.version, d.mtime, d.accounted, d.removeObjs = ms2.version, ms2.mtime, ms2.size, cleanup.removeObjs
	}
	return false
}

// cleanupOrphanedParts is RadosMultipartUpload::cleanup_orphaned_parts
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3038-L3101;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3882-L3945):
// every part the meta object lists whose current prefix c has not handled
// goes to the GC under the upload id, its stripes and head, with its head's
// index entry added to c's, and so does its history.
func (s *Store) cleanupOrphanedParts(ctx context.Context, up *op.Upload, ref mpRef, c *partCleanup) error {
	parts, err := s.listAllParts(ctx, ref, up.ID)
	if err != nil {
		return err
	}
	c.chain = nil
	for i := range parts {
		if err := s.retireCurrentPart(parts[i], c); err != nil {
			return err
		}
		if err := s.retirePart(parts[i], c); err != nil {
			return err
		}
	}
	if len(c.chain) > 0 {
		s.enqueueGC(ctx, c.chain, up.ID)
	}
	return nil
}
