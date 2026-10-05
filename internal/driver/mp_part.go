package driver

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // an S3 ETag is the MD5 of the data
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"strconv"
	"sync"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A part upload is RGWPutObj::execute with MultipartObjectProcessor
// (rgw_op.cc:4142-4596 and driver/rados/rgw_putobj_processor.cc:414-610 at
// v19.2.6, rgw_op.cc:4374-4834 and :448-650 at v20.2.4; a bare line number
// below is v19.2.6's rgw_putobj_processor.cc).

// partLayout is the layout of a part, which
// MultipartObjectProcessor::prepare_head computes for the tail rule alone
// (:441-482): the part head and its shadow stripes all go to the tail rule's
// pool, and both the chunk and the stripe are aligned to that pool's
// alignment. indexedHead reports that the pool is the one the bucket's
// placement puts heads in, where ~RadosWriter removes the part head through
// the bucket index (:184-229).
type partLayout struct {
	layout
	indexedHead bool
}

// partLayout resolves the layout of a part of an upload whose destination
// placement is dest, which the part's tails inherit what it leaves empty
// from the bucket's rule, as generator::create_begin makes them
// (driver/rados/rgw_obj_manifest.cc:221-236).
func (s *Store) partLayout(ctx context.Context, rec *op.BucketRecord, dest meta.PlacementRule) (partLayout, error) {
	bucket := rec.Info.Bucket
	l := layout{headRule: rec.Info.PlacementRule}
	l.tailRule = dest.InheritFrom(l.headRule)
	pool, name, err := s.dataPoolHandle(ctx, l.tailRule, bucket)
	if err != nil {
		return partLayout{}, err
	}
	align, err := s.poolAlignment(ctx, pool)
	if err != nil {
		return partLayout{}, err
	}
	l.headPool, l.tailPool = pool, pool
	l.chunk = alignedSize(s.w.opts.chunkSize, align)
	l.stripe = alignedSize(s.w.opts.stripeSize, align)
	if l.chunk > s.w.opts.putWindow {
		// As planPut refuses a PUT whose chunk the window cannot take.
		return partLayout{}, fmt.Errorf("%w: the %d-byte chunk of pool %s exceeds rgw_put_obj_min_window_size %d",
			op.ErrUnknown, l.chunk, pool.Name(), s.w.opts.putWindow)
	}
	headPool, ok := s.dataPool(rec.Info.PlacementRule, bucket)
	return partLayout{layout: l, indexedHead: ok && headPool == name}, nil
}

// partResult is what one part's data write leaves: the manifest as written,
// the ETag and size, and the writer holding its stripes.
type partResult struct {
	m    meta.Manifest
	etag string
	size uint64
	tw   *tailWriter
}

// streamPart is MultipartObjectProcessor's data path and RGWPutObj::execute's
// checks on what arrived, for part n of upload uploadID of key: the body
// streams into the part head and its shadow stripes under the put window,
// the first stripe created exclusively; when the part head exists, as it
// does for a part number uploaded before, the prefix becomes
// "<key>.<32 random characters>" and the stripe is sent again, once
// (:414-439). Then the body's length (rgw_op.cc:4430-4433), the quota on the
// bytes received (:4444-4448), the Content-MD5 (:4483-4486) and the drain
// that MultipartObjectProcessor::complete starts with. It returns with every
// stripe drained, or with what it wrote removed, as ~RadosWriter removes it.
func (s *Store) streamPart(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string, n uint32, body io.Reader, pl partLayout, size int64, contentMD5 []byte) (partResult, error) {
	target := meta.Obj{Bucket: rec.Info.Bucket, Key: key}
	h := md5.New() //nolint:gosec // an S3 ETag is the MD5 of the data
	m := meta.NewPartManifest(target, pl.headRule, pl.tailRule, meta.MultipartPrefix(key.Name, uploadID), n, pl.stripe)
	tw := s.partWriter(&m, pl, n)
	_, got, err := tw.consume(ctx, body, size, h)
	if errors.Is(err, radosclient.ErrExists) && tw.first != nil {
		m = meta.NewPartManifest(target, pl.headRule, pl.tailRule, meta.MultipartPrefix(key.Name, s.w.rand(32)), n, pl.stripe)
		retry := s.partWriter(&m, pl, n)
		got, err = retry.resume(ctx, tw, body, h)
		tw = retry
	}
	// As PutObject: what follows the body does not watch the client.
	ctx = context.WithoutCancel(ctx)
	if err == nil && size >= 0 && uint64(size) != got {
		err = op.ErrRequestTimeout
	}
	if err == nil {
		err = s.CheckQuota(ctx, rec, rec.Info.Owner, int64(got), 1) //nolint:gosec // a part size is far below 2^63
	}
	sum := h.Sum(nil)
	if err == nil && contentMD5 != nil && !bytes.Equal(sum, contentMD5) {
		err = op.ErrBadDigest
	}
	if err == nil {
		err = tw.drain()
	}
	if err != nil {
		s.discardPart(ctx, rec, key, pl, tw, false)
		if errors.Is(err, radosclient.ErrExists) {
			// The second prefix's part head exists too.
			return partResult{}, op.FromRADOS(err, op.ScopeObject)
		}
		return partResult{}, err
	}
	m.SetObjSize(got)
	return partResult{m: m, etag: hex.EncodeToString(sum), size: got, tw: tw}, nil
}

// partWriter is newPartWriter for a part laid out as pl.
func (s *Store) partWriter(m *meta.Manifest, pl partLayout, n uint32) *tailWriter {
	tw := s.newPartWriter(m, pl.layout, n)
	tw.keepHead = pl.indexedHead
	return tw
}

// registerPart is MultipartObjectProcessor::complete after its drain
// (:491-610): the part head's write_meta, never set atomic, with no data and
// no manifest, in the tail rule's pool, its index entry on the shard of the
// upload's key (head_obj.index_hash_source, :466-467); then one op on the
// meta object: assert_exists, mp_upload_part_info_update of the part's
// RGWUploadPartInfo under partKey, and cls_version_inc (:539-576). It
// returns the part head's mtime. On a failure the part's objects are
// removed, as ~RadosWriter removes them, except when the meta object's op
// timed out and may yet land (:596-602); a missing meta object is
// NoSuchUpload, the upload completed or aborted under the part.
//
// Unlike radosgw, which registers the part and then removes its objects
// when the head write lost a race, rgw-go removes them and registers
// nothing (docs/exclusions.md, "A part whose head write lost a race").
func (s *Store) registerPart(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string, ref mpRef, n uint32, pr partResult, attrs map[string][]byte, mtime time.Time, pl partLayout) (time.Time, error) {
	headObj := pr.m.StripeObj(n, 0)
	stripe := meta.Stripe{Obj: headObj}
	a := maps.Clone(attrs)
	if a == nil {
		a = map[string][]byte{}
	}
	a[meta.AttrETag] = []byte(pr.etag)
	pool := pl.tailPool
	if loc := stripe.Locator(); loc != "" {
		pool = pool.WithLocator(loc)
	}
	hw := &headWrite{
		rec: rec, key: headObj.Key, attrs: a, mtime: mtime, nonAtomic: true, modifyTail: true,
		pool: pool, oid: stripe.OID(), loc: stripe.Locator(), size: pr.size, accountedSize: pr.size,
	}
	x := s.newIndexOp(rec, headObj.Key, "")
	x.hashName = key.Name
	// RGWPutObj::execute completes a part without FLAG_LOG_OP, as parts
	// replicate whole after CompleteMultipartUpload (rgw_op.cc:4553-4555 at
	// v19.2.6, :4828-4830 at v20.2.4), so neither the prepare nor the
	// completion is logged.
	x.noLog = true
	res, err := s.writeMeta(ctx, hw, x)
	if err == nil && res.canceled {
		err = fmt.Errorf("%w: the head write of part %d of %s lost a race", op.ErrInternalError, n, key.Name)
	}
	if err != nil {
		// A head write that failed or lost its race added nothing to the
		// quota cache.
		s.discardPart(ctx, rec, key, pl, pr.tw, false)
		return time.Time{}, err
	}
	info := meta.UploadPartInfo{
		Num: n, Size: pr.size, AccountedSize: pr.size, ETag: pr.etag, Modified: s.now(),
		Manifest: pr.m, Compression: meta.NewCompressionInfo(),
	}
	w := radosclient.NewWriteOp()
	w.AssertExists()
	rgw.MPUploadPartInfoUpdate(w, partKey(uploadID, n), encodeAt(info, s.release), s.release)
	version.Inc(w, s.release)
	if _, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone); err != nil {
		if !errors.Is(err, radosclient.ErrTimedOut) {
			s.discardPart(ctx, rec, key, pl, pr.tw, true)
		}
		return time.Time{}, op.FromRADOS(err, op.ScopeUpload)
	}
	return res.mtime, nil
}

// partKey is the meta object's omap key for part n of upload uploadID:
// "part.%08d" for a v2 upload id, and for an older one "part." and the number
// as the request spelled it (:534-543), which reaches the driver as an int
// and is printed in its canonical form (docs/exclusions.md, "An upload
// without a v2 id keys a part by its canonical number").
func partKey(uploadID string, n uint32) string {
	if meta.IsV2UploadID(uploadID) {
		return meta.MultipartPartKey(n)
	}
	return "part." + strconv.Itoa(int(int32(n))) //nolint:gosec // part_num is an int
}

// discardPart is ~RadosWriter for a part (:184-229): every stripe a write
// reached is removed. A part head in the pool the bucket's placement puts
// heads in goes last, through RGWRados::delete_obj, its index entry removed
// with it; any other part head is a raw delete among the stripes, first, as
// the written set orders "__multipart_" before "__shadow_". counted reports
// that the head's write_meta added it to the quota cache.
func (s *Store) discardPart(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, pl partLayout, tw *tailWriter, counted bool) {
	tw.discard()
	if pl.indexedHead && tw.headWritten() {
		s.removePartHead(ctx, rec, key, tw.stripeObj(0), counted)
	}
}

// removePartHead is RGWRados::delete_obj as ~RadosWriter calls it for a part
// head (removeHead), a missing head ignored and a failure logged as leaked:
// the head's state read from the bucket placement's pool, its removal on the
// shard of the upload's key. A part head is never set atomic, so it has no
// write tag to guard. The removal subtracts one object and the head's size
// from the quota cache, as radosgw does, but only when the head's write_meta
// had added the part: radosgw subtracts a part head it never counted too
// (docs/ceph-upstream-bugs.md, "A failed UploadPart shrinks radosgw's quota
// cache").
func (s *Store) removePartHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, head meta.Obj, counted bool) {
	ctx = context.WithoutCancel(ctx)
	st, err := s.readHead(ctx, rec, head.Key, false)
	if err == nil && !st.Exists {
		return
	}
	if err == nil {
		err = s.removeHead(ctx, rec, head.Key, key.Name, st, radosclient.NewWriteOp(), counted)
	}
	if err != nil {
		slog.WarnContext(ctx, "part head leaked after a failed part upload",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("oid", meta.Stripe{Obj: head}.OID()), slog.Any("error", err))
	}
}

// PutPart implements op.MultipartStore as RGWPutObj::execute writes part n
// of an upload: the meta object read for the destination placement before
// the body, as get_info reads it (rgw_op.cc:4244-4264), so an unknown upload
// is NoSuchUpload with the body unread; the part streamed (streamPart) and
// registered (registerPart). p.Attrs are the part head's attrs; p.Mtime its
// mtime, zero for now. A part takes no write conditions: Squid's
// MultipartObjectProcessor sets none, while Tentacle's checks them against
// the part head the request has just created (docs/exclusions.md,
// "UploadPart ignores If-Match and If-None-Match").
func (s *Store) PutPart(ctx context.Context, up *op.Upload, n int, body io.Reader, p op.PutParams) (*op.PartResult, error) {
	rec, key := up.Bucket, up.Key
	ref, err := s.metaRef(ctx, rec, key, up.ID)
	if err != nil {
		return nil, err
	}
	ms, err := s.readMeta(ctx, ref)
	if err != nil {
		return nil, err
	}
	pl, err := s.partLayout(ctx, rec, ms.info.DestPlacement)
	if err != nil {
		return nil, err
	}
	// part_num is an int, which the manifest and the part key print as one.
	num := uint32(n) //nolint:gosec // the low 32 bits, as radosgw's int keeps them
	pr, err := s.streamPart(ctx, rec, key, up.ID, num, body, pl, p.Size, p.ContentMD5)
	if err != nil {
		return nil, err
	}
	mtime, err := s.registerPart(context.WithoutCancel(ctx), rec, key, up.ID, ref, num, pr, p.Attrs, p.Mtime, pl)
	if err != nil {
		return nil, err
	}
	return &op.PartResult{ETag: pr.etag, Size: pr.size, Mtime: mtime}, nil
}

// CopyPart implements op.MultipartStore as RGWPutObj::execute copies a part
// from a source object (rgw_op.cc:4297-4400): rng of src read through
// ReadObject, decompressed, into PutPart, with up.Attrs as the part head's
// attrs and the range's length as the body's, so a source that ends short
// fails as a body cut short does. The source is read once the upload is
// known, and from the one state src holds, where radosgw prepares a read of
// the source again for each rgw_max_chunk_size of the range
// (docs/exclusions.md, "UploadPartCopy reads one version of its source").
func (s *Store) CopyPart(ctx context.Context, up *op.Upload, n int, src *op.ObjectState, rng op.ByteRange) (*op.PartResult, error) {
	cs := &copySource{read: func(w io.Writer) error { return s.ReadObject(ctx, src, rng, w) }}
	res, err := s.PutPart(ctx, up, n, cs, op.PutParams{Attrs: up.Attrs, Size: int64(rng.Length)}) //nolint:gosec // a range length is far below 2^63
	cs.stop()
	return res, err
}

// copySource is the body of a copied part: read runs on its first Read,
// writing into a pipe the part reads from, so nothing of the source is read
// for a part that never reads its body.
type copySource struct {
	read func(w io.Writer) error
	once sync.Once
	pr   *io.PipeReader
	done chan struct{}
}

func (c *copySource) Read(p []byte) (int, error) {
	c.once.Do(func() {
		pr, pw := io.Pipe()
		c.pr, c.done = pr, make(chan struct{})
		go func() {
			defer close(c.done)
			pw.CloseWithError(c.read(pw)) //nolint:errcheck // CloseWithError always returns nil
		}()
	})
	return c.pr.Read(p)
}

// errCopyStopped ends a source read whose part no longer reads it.
var errCopyStopped = errors.New("driver: the part copy stopped reading its source")

// stop ends the source read, if it started, and waits for it.
func (c *copySource) stop() {
	c.once.Do(func() {})
	if c.pr == nil {
		return
	}
	c.pr.CloseWithError(errCopyStopped) //nolint:errcheck // CloseWithError always returns nil
	<-c.done
}
