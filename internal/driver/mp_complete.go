package driver

import (
	"bytes"
	"cmp"
	"context"
	"crypto/md5" //nolint:gosec // a multipart ETag is the MD5 of the parts' MD5s
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strconv"

	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// CompleteMultipartUpload is RGWCompleteMultipart::execute (rgw_op.cc:6367-6541
// at v19.2.6, :7163-7431 at v20.2.4) over RadosMultipartUpload::complete
// (driver/rados/rgw_sal_rados.cc:3433-3640 at v19.2.6, :4279-4490 at
// v20.2.4); a bare line number below is v19.2.6's rgw_op.cc.

// maxMetaDeleteRetries is MAX_DELETE_RETRIES, the removals of the meta
// object a completion tries while parts keep racing it (:6497).
const maxMetaDeleteRetries = 15

// partHeadStats bounds the part head stats a completion has in flight.
const partHeadStats = 16

// inProgress is the answer to a completion whose lock is held or lost
// (:6443-6444).
var inProgress = op.ErrInternalError.WithMessage("This multipart completion is already in progress")

// multipartETag is the ETag complete computes: the hex MD5 of the parts'
// 16-byte MD5s, "-" and the part count (rgw_sal_rados.cc:3503-3505 and
// :3579-3585). An ETag that is not 32 hex digits is ErrInvalidPart, where
// radosgw's hex_to_buf hashes whatever it decodes.
func multipartETag(etags []string) (string, error) {
	h := md5.New() //nolint:gosec // a multipart ETag is the MD5 of the parts' MD5s
	for _, e := range etags {
		b, err := hex.DecodeString(e)
		if err != nil || len(b) != md5.Size {
			return "", fmt.Errorf("%w: the part ETag %q is not an MD5", op.ErrInvalidPart, e)
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(etags)), nil
}

// mergeCompression is complete's cs_info handling for the handled-th part
// (rgw_sal_rados.cc:3531-3566): after the first part, one that differs from
// the parts before in being compressed, in its compressor, or in its
// compressor message when the object has one, is ErrInvalidPart; a
// compressed part's blocks are appended, shifted past the original and the
// stored bytes before them.
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
// manifest, compression and ETag, its stored and accounted sizes, and what
// completing it retires.
type assembly struct {
	manifest   meta.Manifest
	cs         meta.CompressionInfo
	compressed bool
	etag       string
	size       uint64
	accounted  uint64
	cleanup    partCleanup
}

// assemble is complete's validation and assembly (rgw_sal_rados.cc:3462-3590)
// of parts, sorted, against stored, the parts listed for upload uploadID of
// key, page by page as list_parts returns them: on the last page the counts
// must agree; each part, in lockstep, must be at least minPartSize unless it
// is the last, have the number and the unquoted ETag of the stored one, and
// a manifest. Each is appended to the object's manifest, its head's index
// entry, "<prefix>.<n>" in the multipart namespace, is to be retired, and so
// is its history (retirePart). A part whose manifest radosgw would append as
// an explicit one is ErrInvalidPart (docs/exclusions.md, "An explicit part
// manifest is not appended").
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
	etag, err := multipartETag(etags)
	if err != nil {
		return nil, err
	}
	a.etag = etag
	return a, nil
}

// assemblePart is one step of complete's loop (rgw_sal_rados.cc:3478-3577)
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
// (:6543-6580; v20.2.4 :7433-7504): the key's head carries the ETag the
// request's parts make.
func (s *Store) checkPreviouslyCompleted(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, parts []op.CompletePart) (*op.ObjectState, bool) {
	st, err := s.readHead(ctx, rec, key, false)
	if err != nil || !st.Exists {
		return nil, false
	}
	etags := make([]string, len(parts))
	for i, p := range parts {
		etags[i] = op.Unquote(p.ETag)
	}
	want, err := multipartETag(etags)
	return st, err == nil && st.ETag == want
}

// sameCompletion reports that the head st is the object a reaches: the same
// ETag, size, prefix and rules, so the same part objects.
func sameCompletion(st *op.ObjectState, a *assembly) bool {
	m := st.Manifest
	return m != nil && !m.ExplicitObjs && st.ETag == a.etag && m.ObjSize == a.manifest.ObjSize &&
		m.Prefix == a.manifest.Prefix && maps.Equal(m.Rules, a.manifest.Rules)
}

// Complete implements op.MultipartStore as CompleteMultipartUpload completes
// an upload:
//   - a bucket whose writes take a versioned path, versioning enabled or
//     suspended or object lock, is answered 501 before the store is
//     touched: radosgw writes a version there (:6459-6466), and the plain
//     key's head written here would stand beside the bucket's olh;
//   - the RGWCompleteMultipart lock on the meta object (lockMeta), whose
//     ENOENT, when the key already carries the parts' ETag, is radosgw's
//     re-sent completion, answered without an ETag on Squid and with the
//     stored one on Tentacle (:6438-6441 and :6533; v20.2.4 :7472); any
//     other refusal is 500 "This multipart completion is already in
//     progress";
//   - the meta object's attrs and version (readMeta). A completion record
//     there is an earlier completion's whose meta object outlived its head
//     write (replayRecorded);
//   - its parts (listAllParts), validated and assembled (assemble);
//   - the head of every part (checkPartHeads), and the key's current head:
//     one whose manifest names this upload's objects, with no record, is an
//     earlier completion a gateway without the record left behind, which is
//     finished when it is this completion's object and otherwise refused
//     with NoSuchUpload (tracker #80896);
//   - the lock renewed with the completion record written (renewAndRecord),
//     or 500 as for a held lock;
//   - the head written as write_meta writes it for complete
//     (rgw_sal_rados.cc:3588-3637): the meta object's attrs with the ETag,
//     the compression info and the object lock settings, under up.WriteTag
//     or a random tag, created, atomic, its tail modified, with no bytes
//     added to the quota cache, and with up's conditions. A write that
//     certainly failed takes the record back (clearRecord). A lost race
//     without conditions is success, as radosgw answers it, and the parts'
//     current objects then leak (docs/ceph-upstream-bugs.md, "A
//     CompleteMultipartUpload that loses its head write's race leaks its
//     parts");
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
	if f := rec.Info.Flags; f&(meta.BucketVersioned|meta.BucketVersionsSuspended|meta.BucketObjLockEnabled) != 0 {
		return nil, fmt.Errorf("%w: completing an upload in bucket %s, which is versioned or has object lock",
			op.ErrNotImplemented, rec.Info.Bucket.Name)
	}
	parts = op.SortCompleteParts(parts)
	ref, err := s.metaRef(ctx, rec, key, up.ID)
	if err == nil {
		err = s.lockMeta(ctx, ref)
	} else if !errors.Is(err, radosclient.ErrNotFound) {
		return nil, err
	}
	if err != nil {
		// radosgw opens a missing pool, creating it, and its lock then
		// meets no object, so a missing pool reads as ENOENT here.
		if errors.Is(err, radosclient.ErrNotFound) {
			if st, ok := s.checkPreviouslyCompleted(ctx, rec, key, parts); ok {
				slog.InfoContext(ctx, "multipart completion already completed", slog.String("upload", up.ID))
				res := &op.PutResult{Size: st.Size, Mtime: st.Mtime, Epoch: st.Epoch}
				if s.release >= denc.Tentacle {
					res.ETag = st.ETag
				}
				return res, nil
			}
		}
		return nil, fmt.Errorf("%w: locking upload %s: %w", inProgress, up.ID, err)
	}
	ctx = context.WithoutCancel(ctx)
	res, released, err := s.completeLocked(ctx, up, ref, parts)
	if !released {
		s.unlockMeta(ctx, ref)
	}
	return res, err
}

// completeLocked is Complete under the lock; the bool reports that the lock
// went with the meta object.
func (s *Store) completeLocked(ctx context.Context, up *op.Upload, ref mpRef, parts []op.CompletePart) (*op.PutResult, bool, error) {
	rec, key := up.Bucket, up.Key
	ms, err := s.readMeta(ctx, ref)
	if errors.Is(err, radosclient.ErrNotFound) {
		// get_obj_attrs's -ENOENT reaches the client as it is (:6448-6453).
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
	cur, err := s.readHead(ctx, rec, key, false)
	if err != nil {
		return nil, false, err
	}
	if cur.Exists && namesUploadObjects(cur.Manifest, uploadPrefixes(key.Name, up.ID, stored)) {
		if !sameCompletion(cur, a) {
			return nil, false, fmt.Errorf("%w: upload %s already completed %s with other parts", op.ErrNoSuchUpload, up.ID, key.Name)
		}
		slog.InfoContext(ctx, "finishing a multipart completion whose meta object outlived its head write", slog.String("upload", up.ID))
		released := s.retireUpload(ctx, up, ref, ms, &a.cleanup)
		return &op.PutResult{ETag: a.etag, Size: cur.Size, Mtime: cur.Mtime, Epoch: cur.Epoch}, released, nil
	}
	tag := cmp.Or(up.WriteTag, s.w.randTag())
	if lerr := s.renewAndRecord(ctx, ref, tag); lerr != nil {
		return nil, false, fmt.Errorf("%w: renewing the lock of upload %s: %w", inProgress, up.ID, lerr)
	}
	attrs := maps.Clone(ms.attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	delete(attrs, attrMPCompletionTag)
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
	hw := &headWrite{
		rec: rec, key: key, tag: tag, manifest: &a.manifest, attrs: attrs, mtime: s.now(), create: true, modifyTail: true,
		completeMultipart: true, size: a.size, accountedSize: a.accounted, ifMatch: up.IfMatch, ifNoneMatch: up.IfNoneMatch,
	}
	hr, err := s.writeMeta(ctx, hw, s.newIndexOp(rec, key, tag))
	if err != nil {
		if !errors.Is(err, radosclient.ErrTimedOut) {
			// The head was not written, so a retry may still complete the
			// upload; after a timeout it may yet land, and the record stays.
			s.clearRecord(ctx, ref)
		}
		return nil, false, err
	}
	mtime := hr.mtime
	if hr.canceled {
		// A lost race leaves no head to read a time from; this is the one
		// the head would have carried, and there is no version.
		mtime = hw.mtime
	}
	released := s.retireUpload(ctx, up, ref, ms, &a.cleanup)
	return &op.PutResult{ETag: a.etag, Size: a.size, Mtime: mtime, Epoch: hr.epoch}, released, nil
}

// replayRecorded answers a completion whose meta object holds the record of
// an earlier one, tag, as ceph/ceph#72103 does: when the head the record
// names still carries tag and the ETag the request's parts make, that
// completion took effect, and this one finishes it, removing the meta object
// with the parts' entries and retiring what the head does not name, without
// writing a head or checking the request's conditions. Otherwise that head
// was never written, or was replaced or deleted and its parts queued for the
// GC, and a head over them would lose the object's data: NoSuchUpload, with
// nothing written. A record naming a version other than the key's own head
// is refused the same way, as no version is written here.
func (s *Store) replayRecorded(ctx context.Context, up *op.Upload, ref mpRef, ms *metaState, parts []op.CompletePart, tag []byte) (*op.PutResult, bool, error) {
	refused := fmt.Errorf("%w: an earlier completion of upload %s did not take effect, or its object was replaced", op.ErrNoSuchUpload, up.ID)
	if inst, ok := ms.attrs[attrMPCompletionInstance]; ok && string(inst) != nullInstance {
		return nil, false, refused
	}
	cur, err := s.readHead(ctx, up.Bucket, up.Key, false)
	if err != nil {
		return nil, false, err
	}
	if !cur.Exists || !bytes.Equal(cur.Attrs[meta.AttrIDTag], tag) {
		slog.InfoContext(ctx, "not completing an upload whose earlier completion did not take effect or was replaced", slog.String("upload", up.ID))
		return nil, false, refused
	}
	etags := make([]string, len(parts))
	for i, p := range parts {
		etags[i] = op.Unquote(p.ETag)
	}
	if want, eerr := multipartETag(etags); eerr != nil || cur.ETag != want {
		return nil, false, fmt.Errorf("%w: upload %s already completed %s with other parts", op.ErrNoSuchUpload, up.ID, up.Key.Name)
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
	return &op.PutResult{ETag: cur.ETag, Size: cur.Size, Mtime: cur.Mtime, Epoch: cur.Epoch}, released, nil
}

// retireUpload is what follows a completion's head write: the chain of
// cleanup, the parts' history, to the GC under the upload id,
// synchronously, as cleanup_part_history sends it
// (rgw_sal_rados.cc:3132-3146), then the meta object's removal with
// cleanup's index entries, retried up to maxMetaDeleteRetries times while
// parts race it (:6493-6530): after each ECANCELED the meta object is read
// again and cleanup_orphaned_parts runs, whose parts were registered after
// the listing and so are not the object's. The prefixes cleanup handled
// carry over, which keeps the object's own parts out of that cleanup. A
// removal that keeps failing is logged, as radosgw logs it; the completion
// stands. released reports that the lock went with the meta object.
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
// (rgw_sal_rados.cc:3038-3101; v20.2.4 :3882-3945): every part the meta
// object lists whose current prefix c has not handled goes to the GC under
// the upload id, its stripes and head, with its head's index entry added to
// c's, and so does its history.
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
