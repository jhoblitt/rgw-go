package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A copy is RGWRados::copy_obj's local branch (driver/rados/rgw_rados.cc
// :4667-5031 at v19.2.6, :4916-5291 at v20.2.4; a bare line number below is
// v19.2.6's) with copy_obj_data (:5034-5115, v20.2.4 :5294-5386).

var (
	// errCopyStreamDone ends a copy's source read once the destination
	// stops taking its bytes.
	errCopyStreamDone = errors.New("driver: copy destination stopped reading")
	// errStopRefs ends the walk of a copy's tails once a reference failed.
	errStopRefs = errors.New("driver: a tail reference failed")
)

// CopyObject implements op.ObjectStore. A source with tails in the
// destination's tail placement and pool shares them: every tail gets a
// refcount reference under the copy's tag, the head's own bytes are copied,
// and a new head names the source's tails; a copy onto itself rewrites the
// head alone and keeps its tails. Anything else streams the source's stored
// bytes into a new object (:4847-4879). src is the state the op read with
// PrefetchObject, as radosgw's object context holds it from
// read_obj_policy, so the head's bytes come from its Head without a second
// read.
//
// A copy that fails after taking references drops them again, as done_ret
// does (:4990-5031), except where its head write timed out and may yet land
// on the shared tails, and it also drops them when its head write loses a
// race, which radosgw answers as success without dropping them
// (docs/exclusions.md, "Object write differences").
func (s *Store) CopyObject(ctx context.Context, src *op.ObjectState, dst *op.BucketRecord, dstKey meta.ObjKey, p op.CopyParams) (*op.PutResult, error) {
	if !src.Exists {
		return nil, fmt.Errorf("copy source %s: %w", objName(src), op.ErrNoSuchKey)
	}
	attrs, err := s.copyAttrs(src, dst, p.Attrs, p.ReplaceAttrs)
	if err != nil {
		return nil, err
	}
	// get_max_chunk_size of the destination bucket's head pool (:4814-4818).
	headPool, _, err := s.dataPoolHandle(ctx, dst.Info.PlacementRule, dst.Info.Bucket)
	if err != nil {
		return nil, err
	}
	align, err := s.poolAlignment(ctx, headPool)
	if err != nil {
		return nil, err
	}
	maxChunk := alignedSize(s.w.opts.chunkSize, align)
	m := src.Manifest
	srcRule := src.Bucket.Info.PlacementRule
	if m != nil && m.TailPlacement.PlacementRule != (meta.PlacementRule{}) {
		srcRule = m.TailPlacement.PlacementRule
	}
	srcPool, ok := s.dataPool(srcRule, src.Bucket.Info.Bucket)
	if !ok {
		return nil, fmt.Errorf("%w: no data pool for placement %q of copy source %s", op.ErrUnknown, srcRule, objName(src))
	}
	// s->dest_placement: the request's storage class on the bucket's
	// placement (rgw_op.cc:577-578).
	destRule := meta.PlacementRule{StorageClass: p.StorageClass}.InheritFrom(dst.Info.PlacementRule)
	dstPool, ok := s.dataPool(destRule, dst.Info.Bucket)
	if !ok {
		return nil, fmt.Errorf("%w: no data pool for placement %q of bucket %s", op.ErrUnknown, destRule, dst.Info.Bucket.Name)
	}
	// "refcounting tail wouldn't work here, just copy the data": a source
	// without a manifest or tails, one whose tails lie under another rule or
	// in another pool, or one whose head holds more than a chunk (:4847-4866).
	copyData := m == nil || srcRule != destRule || srcPool != dstPool
	copyFirst := false
	if m != nil {
		switch {
		case !m.HasTail():
			copyData = true
		case m.HeadSize > maxChunk:
			copyData = true
		case m.HeadSize > 0:
			copyFirst = true
		}
	}
	if copyData {
		delete(attrs, meta.AttrTailTag)
		return s.copyData(ctx, src, dst, dstKey, p.StorageClass, attrs, p.Mtime)
	}
	tag := p.Tag
	if tag == "" {
		tag = s.w.randTag()
	}
	return s.shareTails(ctx, src, dst, dstKey, attrs, tag, p.Mtime, copyFirst)
}

// copyAttrs is copy_obj's attr rewrite (:4755-4794; v20.2.4 :5010-5049, with
// set_copy_attrs at :3673-3700 and v20.2.4 :3858-3885): the source's attrs
// with the request's ACL, without delete_at, the object-lock attrs other than
// the request's, the OLH attrs in a bucket without versioning, and the
// replication attrs; then the source's attrs whole for COPY, or the request's
// with the source's etag and tail tag where it has none for REPLACE; then
// without idtag, pg_ver and source_zone, and with the source's compression
// info. A Tentacle gateway also drops the source's storage class (v20.2.4
// :5028).
//
// An encrypted source is NotImplemented on both releases. A Squid radosgw
// refuses it (:4755-4763); a Tentacle one decrypts it and encrypts the copy
// as the request asks (v20.2.4 rgw_op.cc:5793-5805 and :5813-5819), which
// needs decryption rgw-go does not implement, and copying its ciphertext
// under a head without its crypt attrs would make it unreadable.
func (s *Store) copyAttrs(src *op.ObjectState, dst *op.BucketRecord, req map[string][]byte, replace bool) (map[string][]byte, error) {
	if _, ok := src.Attrs[meta.AttrCryptMode]; ok {
		return nil, fmt.Errorf("%w: copy source %s is encrypted", op.ErrNotImplemented, objName(src))
	}
	srcAttrs := maps.Clone(src.Attrs)
	if srcAttrs == nil {
		srcAttrs = map[string][]byte{}
	}
	srcAttrs[meta.AttrACL] = req[meta.AttrACL]
	delete(srcAttrs, meta.AttrDeleteAt)
	delete(srcAttrs, meta.AttrObjectRetention)
	delete(srcAttrs, meta.AttrObjectLegalHold)
	for _, n := range []string{meta.AttrObjectRetention, meta.AttrObjectLegalHold} {
		if v, ok := req[n]; ok {
			srcAttrs[n] = v
		}
	}
	if !versioningEnabled(dst.Info) {
		delete(srcAttrs, meta.AttrOLHIDTag)
		delete(srcAttrs, meta.AttrOLHInfo)
		delete(srcAttrs, meta.AttrOLHVer)
	}
	if s.release >= denc.Tentacle {
		delete(srcAttrs, meta.AttrStorageClass)
	}
	delete(srcAttrs, meta.AttrReplicationTrace)
	delete(srcAttrs, meta.AttrReplicatedAt)
	delete(srcAttrs, meta.AttrReplicationStatus)
	attrs := srcAttrs
	if replace {
		attrs = maps.Clone(req)
		if attrs == nil {
			attrs = map[string][]byte{}
		}
		if len(attrs[meta.AttrETag]) == 0 {
			attrs[meta.AttrETag] = srcAttrs[meta.AttrETag]
		}
		if tt, ok := srcAttrs[meta.AttrTailTag]; ok && len(attrs[meta.AttrTailTag]) == 0 {
			attrs[meta.AttrTailTag] = tt
		}
	}
	delete(attrs, meta.AttrIDTag)
	delete(attrs, meta.AttrPGVer)
	delete(attrs, meta.AttrSourceZone)
	if c, ok := srcAttrs[meta.AttrCompression]; ok {
		attrs[meta.AttrCompression] = c
	}
	return attrs, nil
}

// shareTails is copy_obj from its refcount loop on (:4881-4988): a reference
// under tag on every tail stripe of the source, unless the copy is onto
// itself; the head's bytes when the source's head holds data; then the head,
// written under tag with the source's manifest placed at the destination.
// Once the references are taken the copy runs to its end whether or not its
// client stays, as radosgw's does.
func (s *Store) shareTails(ctx context.Context, src *op.ObjectState, dst *op.BucketRecord, dstKey meta.ObjKey, attrs map[string][]byte, tag string, mtime time.Time, copyFirst bool) (*op.PutResult, error) {
	// rgw_obj's ==: rgw_bucket's and rgw_obj_key's, which compares the name
	// and instance only (rgw_obj_types.h:256-259 at v19.2.6 and v20.2.4).
	itself := sameBucketID(src.Bucket.Info.Bucket, dst.Info.Bucket) && src.Key.Name == dstKey.Name && src.Key.Instance == dstKey.Instance
	m := cloneManifest(src.Manifest)
	var refs []objRef
	if !itself {
		delete(attrs, meta.AttrTailTag)
		if m.TailPlacement.Bucket.Name == "" {
			m.TailPlacement.Bucket = src.Bucket.Info.Bucket
		}
		var err error
		if refs, err = s.refTails(ctx, src, copyFirst, tag); err != nil {
			return nil, err
		}
	}
	ctx = context.WithoutCancel(ctx)
	data := []byte{}
	if copyFirst {
		var err error
		if data, err = s.headData(ctx, src); err != nil {
			s.unrefTails(refs, tag)
			return nil, err
		}
	}
	setHead(&m, dst.Info.PlacementRule, meta.Obj{Bucket: dst.Info.Bucket, Key: dstKey}, uint64(len(data)))
	accounted := src.Size
	if c := src.Compression; c != nil && c.Type != compression.None {
		accounted = c.OrigSize
	}
	hw := &headWrite{
		rec: dst, key: dstKey, tag: tag, data: data, manifest: &m, attrs: attrs, mtime: mtime,
		create: true, modifyTail: !itself, keepTail: itself, size: src.Size, accountedSize: accounted,
	}
	if itself {
		// The head keeps src's tails, which hold no reference of the copy's,
		// so it may land only on the head src was read from. radosgw's
		// write_meta guards on the head as it reads it at the write, or
		// creates it when gone, so a PUT or DELETE since the read lets it
		// land src's manifest on tails already queued for the GC
		// (docs/ceph-upstream-bugs.md).
		hw.expect = src
	}
	res, err := s.writeMeta(ctx, hw, s.newIndexOp(dst, dstKey, tag))
	switch {
	case errors.Is(err, radosclient.ErrTimedOut):
		// The head may yet land and name these tails, so their references
		// stay, as writer.clear_written leaves a PUT's tails
		// (rgw_putobj_processor.cc:395-401); done_ret drops them, which lets
		// the tails go with their source (docs/ceph-upstream-bugs.md).
	case err != nil, res.canceled:
		// No head names the tails under tag. radosgw keeps the references
		// of a write that lost its race, leaking the tails.
		s.unrefTails(refs, tag)
	}
	if err != nil {
		return nil, err
	}
	return &op.PutResult{ETag: rgwBlStr(attrs[meta.AttrETag]), Size: src.Size, Mtime: res.mtime, Epoch: res.epoch}, nil
}

// versioningEnabled is RGWBucketInfo::versioning_enabled (rgw_common.h:1076
// at v19.2.6, :1118 at v20.2.4): versioned and not suspended.
func versioningEnabled(info meta.BucketInfo) bool {
	return info.Flags&(meta.BucketVersioned|meta.BucketVersionsSuspended) == meta.BucketVersioned
}

// sameBucketID is rgw_bucket's ==, which compares tenant, name and id only.
func sameBucketID(a, b meta.BucketID) bool {
	return a.Tenant == b.Tenant && a.Name == b.Name && a.ID == b.ID
}

// cloneManifest copies m deeply enough for setHead to change the copy alone.
func cloneManifest(m *meta.Manifest) meta.Manifest {
	c := *m
	c.Rules = maps.Clone(m.Rules)
	c.Objs = maps.Clone(m.Objs)
	return c
}

// headData is copy_first's read of the source head's bytes (:4959-4965):
// Read::read serves them from the prefetch and reads only what it lacks
// (:7283-7312), and they are the stored bytes, compressed where the source
// is, as are the tails the copy shares.
func (s *Store) headData(ctx context.Context, src *op.ObjectState) ([]byte, error) {
	n := src.Manifest.HeadSize
	if uint64(len(src.Head)) >= n {
		return src.Head[:n], nil
	}
	var buf bytes.Buffer
	buf.Grow(int(n)) //nolint:gosec // at most one chunk
	if err := s.readStored(ctx, src, 0, n, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// copyLimit is rgw_max_copy_obj_concurrent_io as make_throttle takes it: an
// unsigned window, so a negative value limits nothing. Open uses a zero as
// 1, and so does copyLimit for a Store built without it.
func (s *Store) copyLimit() int64 {
	switch n := s.w.opts.copyConcurrentIO; {
	case n > 0:
		return int64(n)
	case n < 0:
		return math.MaxInt64
	}
	return 1
}

// refTails is copy_obj's refcount loop (:4906-4952): refcount get(tag + NUL,
// implicit) on every stripe of src's manifest from obj_begin, the first
// skipped when the head's bytes are copied, rgw_max_copy_obj_concurrent_io at
// a time. Each get runs under the driver's lifetime context, so whether it
// took its reference is known; the client leaving stops further gets. On a
// failure the references taken are dropped again and the first error
// returned. The gets are not an errgroup: one failing must not cancel the
// others in flight, whose outcome the rollback needs, and the dispatch waits
// on the request's context, which errgroup's limit cannot.
func (s *Store) refTails(ctx context.Context, src *op.ObjectState, skipFirst bool, tag string) ([]objRef, error) {
	sem := semaphore.NewWeighted(s.copyLimit())
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		refs  []objRef
		first error
	)
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return first != nil
	}
	walkErr := s.walkTails(src, skipFirst, func(obj meta.Obj, rule meta.PlacementRule) error {
		if failed() {
			return errStopRefs
		}
		ref, err := s.rawRef(ctx, rule, obj)
		if err != nil {
			return err
		}
		if err := sem.Acquire(ctx, 1); err != nil {
			return err
		}
		wg.Go(func() {
			defer sem.Release(1)
			w := radosclient.NewWriteOp()
			refcount.Get(w, tag+"\x00", true, s.release)
			_, err := ref.pool.Write(s.w.completions.ctx, ref.oid, w, radosclient.OpFlagNone)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				refs = append(refs, ref)
			case first == nil:
				first = op.FromRADOS(err, op.ScopeObject)
			}
		})
		return nil
	})
	wg.Wait()
	err := first
	if err == nil && !errors.Is(walkErr, errStopRefs) {
		err = walkErr
	}
	if err != nil {
		s.unrefTails(refs, tag)
		return nil, err
	}
	return refs, nil
}

// walkTails calls fn with the object and placement of each stripe of src's
// manifest from obj_begin to obj_end, the first skipped when skipFirst, and
// stops at fn's first error. A manifest whose walk does not move forward or
// passes meta.MaxWalkStripes stripes fails, where radosgw's loop never ends.
func (s *Store) walkTails(src *op.ObjectState, skipFirst bool, fn func(meta.Obj, meta.PlacementRule) error) error {
	m := src.Manifest
	if m.ObjSize > 0 {
		if err := checkWalkBound(m, 0, m.ObjSize-1); err != nil {
			return manifestErr(src, err)
		}
	}
	it, err := m.Seek(0)
	if err != nil {
		return manifestErr(src, err)
	}
	for n := 0; !it.Done(); n++ {
		if n >= meta.MaxWalkStripes {
			return manifestErr(src, fmt.Errorf("%w: more than %d", meta.ErrTooManyStripes, meta.MaxWalkStripes))
		}
		if n > 0 || !skipFirst {
			obj, rule, _ := it.Location()
			if err := fn(obj, rule); err != nil {
				return err
			}
		}
		prev := it.Ofs()
		if err := it.Next(); err != nil {
			return manifestErr(src, err)
		}
		if !it.Done() && it.Ofs() <= prev {
			return manifestErr(src, fmt.Errorf("%w: manifest iteration does not advance past offset %d", denc.ErrMalformed, prev))
		}
	}
	return nil
}

// unrefTails is done_ret's rollback (:4990-5031): refcount put(tag + NUL,
// implicit) under full-try on each object whose get succeeded, at the same
// concurrency, under the driver's lifetime context; a failure is logged and
// leaks the reference. An object whose get failed is left alone, which keeps
// a put of a tag never taken from dropping the implicit reference of the
// tail's writer.
func (s *Store) unrefTails(refs []objRef, tag string) {
	if len(refs) == 0 {
		return
	}
	ctx := s.w.completions.ctx
	sem := semaphore.NewWeighted(s.copyLimit())
	var wg sync.WaitGroup
	for _, ref := range refs {
		if err := sem.Acquire(ctx, 1); err != nil {
			slog.ErrorContext(ctx, "copy reference rollback abandoned; the tails are leaked", slog.String("tag", tag), slog.Any("error", err))
			break
		}
		wg.Go(func() {
			defer sem.Release(1)
			w := radosclient.NewWriteOp()
			refcount.Put(w, tag+"\x00", true, s.release)
			if _, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagFullTry); err != nil {
				slog.ErrorContext(ctx, "copy reference rollback failed; the tail is leaked",
					slog.String("pool", ref.pool.Name()), slog.String("oid", ref.oid), slog.Any("error", err))
			}
		})
	}
	wg.Wait()
}

// copyData is copy_obj_data (:5034-5115; v20.2.4 :5294-5386): the source's
// stored bytes, read as Read::read hands them over (:5067), without
// decompressing, streamed through AtomicObjectProcessor into a new object
// under storageClass with a random tag (:5051) and without conditions. attrs
// keep the source's etag and a compressed source's compression info, block
// map included, from which putStream takes the accounted size
// (:5098-5108). The destination placement's compression is not consulted.
func (s *Store) copyData(ctx context.Context, src *op.ObjectState, dst *op.BucketRecord, dstKey meta.ObjKey, storageClass string, attrs map[string][]byte, mtime time.Time) (*op.PutResult, error) {
	l, err := s.planPut(ctx, dst, dstKey, storageClass)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	read := make(chan struct{})
	go func() {
		defer close(read)
		// An error closes the pipe with it, which consume returns as the
		// body's error, so the tails already written are deleted.
		pw.CloseWithError(s.readStored(ctx, src, 0, src.Size, pw))
	}()
	res, err := s.putStream(ctx, dst, dstKey, pr, l, streamWrite{
		tag: s.w.randTag(), attrs: attrs, size: int64(src.Size), mtime: mtime, //nolint:gosec // an object size is far below 2^63
	})
	pr.CloseWithError(errCopyStreamDone)
	<-read
	return res, err
}
