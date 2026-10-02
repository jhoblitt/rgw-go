package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The radosgw functions this file transcribes, in driver/rados/rgw_rados.cc
// unless named otherwise; each is the same at both tags.
//
//	                       v19.2.6       v20.2.4
//	Read::iterate          :7444-7464    :8295-8319
//	iterate_obj            :7466-7536    :8321-8391
//	get_obj_iterate_cb     :7391-7442    :8242-8293
//	get_obj_data::flush    :7345-7379    :8196-8230
//	RGWGetObj_Decompress   rgw_compression.cc:107-218 at both

// ReadObject implements op.ObjectStore: it streams rng of st to sink in offset
// order. rng is in the object's logical bytes, decompressed bytes for a
// compressed object, and must lie within the object, as the op's
// range_to_ofs leaves it. It is Read::iterate: pieces of at most
// rgw_get_obj_max_req_size per stripe, up to rgw_get_obj_window_size bytes in
// flight, head bytes already in st.Head served without a RADOS op, head reads
// guarded by the idtag, and pieces written in offset order. A compressed
// object's range is widened to its compression blocks and decoded through
// compression.Stream, as RGWGetObj_Decompress does.
//
// A compressed object's damage answers UnknownError, the class of radosgw's
// -EIO: a block map that does not hold together, and a block refused where
// radosgw's decompress hands on what it decoded or never returns. A block its
// codec refuses where radosgw's decompress fails as well is returned as the
// codec's *compression.DecodeError and no op error, so that the handler can
// answer radosgw's 403 or 404 while no byte of the body has gone out.
func (s *Store) ReadObject(ctx context.Context, st *op.ObjectState, rng op.ByteRange, sink io.Writer) error {
	if rng.Length == 0 {
		return nil
	}
	ci := st.Compression
	// rgw_compression_info_from_attr takes an object of type none as
	// uncompressed, once it has refused an empty block list with -EIO
	// (rgw_compression.cc:20-22 at v19.2.6 and v20.2.4), which is the op's to
	// answer.
	if ci == nil || ci.Type == compression.None {
		return s.readStored(ctx, st, rng.Offset, rng.Length, sink)
	}
	if rng.Offset > ci.OrigSize || rng.Length > ci.OrigSize-rng.Offset {
		return fmt.Errorf("%w: %d bytes at %d, past the %d of %s", op.ErrInternalError, rng.Length, rng.Offset, ci.OrigSize, objName(st))
	}
	dec, err := compression.NewDecoder(ci.Type)
	if err != nil {
		// handle_data's "Cannot load compressor of type", -EIO.
		return fmt.Errorf("%w: %s: %w", op.ErrUnknown, objName(st), err)
	}
	end := rng.Offset + rng.Length - 1
	// fixup_range takes every block for a request without a range and the
	// blocks a range touches otherwise; for a range of the whole object the
	// two agree.
	win, err := compression.Range(ci.Blocks, rng.Offset != 0 || end != ci.OrigSize-1, rng.Offset, end)
	switch {
	case err != nil:
	case win.CompEnd < win.CompOfs:
		// new_ofs going backward: the window would have no length.
		err = fmt.Errorf("%w: blocks %d-%d end at byte %d, before they start at %d", compression.ErrCorrupt, win.First, win.Last, win.CompEnd, win.CompOfs)
	case win.CompEnd >= st.Size:
		err = fmt.Errorf("%w: blocks %d-%d end at %d, past the %d stored bytes", compression.ErrCorrupt, win.First, win.Last, win.CompEnd+1, st.Size)
	}
	if err != nil {
		return fmt.Errorf("%w: compression info of %s: %w", op.ErrUnknown, objName(st), err)
	}
	stream := compression.NewStream(dec, ci.Blocks, ci.OrigSize, win, ci.CompressorMessage, sink)
	if err := s.readStored(ctx, st, win.CompOfs, win.CompEnd-win.CompOfs+1, decoding{st: st, stream: stream}); err != nil {
		return err
	}
	if err := stream.Close(); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = fmt.Errorf("%w: the stored bytes end inside a block: %w", compression.ErrCorrupt, err)
		}
		return decodeErr(st, err)
	}
	return nil
}

// decoding is the writer a compressed object's stored bytes go to: the
// Stream that decodes them, with its refusals classified as ReadObject
// answers them and the client sink's own errors passed on as they are.
type decoding struct {
	st     *op.ObjectState
	stream *compression.Stream
}

func (d decoding) Write(p []byte) (int, error) {
	n, err := d.stream.Write(p)
	return n, decodeErr(d.st, err)
}

// decodeErr classifies a Stream's error: a *compression.DecodeError stays
// one, any other compression.ErrCorrupt is UnknownError, and anything else,
// the client sink's, passes as it is.
func decodeErr(st *op.ObjectState, err error) error {
	if errors.As(err, new(*compression.DecodeError)) {
		return fmt.Errorf("decoding %s: %w", objName(st), err)
	}
	if errors.Is(err, compression.ErrCorrupt) {
		return fmt.Errorf("%w: decoding %s: %w", op.ErrUnknown, objName(st), err)
	}
	return err
}

// readStored streams the stored bytes [ofs, ofs+n) of st to w in offset order
// and does not decompress them: the bytes Read::read (:7224-7343 at v19.2.6,
// :8075-8194 at v20.2.4) and Read::iterate take from the object's stripes,
// under ReadObject's piece walk, window and buffers. ofs and n are stored
// offsets, which for a compressed object are not the logical ones; n == 0
// writes nothing. ReadObject serves an uncompressed object, and a compressed
// one's widened block range, through it; CopyObject's data copy streams a
// source's stored stripes with it, as copy_obj_data does (Read::read at
// v19.2.6 :5067, Read::iterate at v20.2.4 :5338).
func (s *Store) readStored(ctx context.Context, st *op.ObjectState, ofs, n uint64, w io.Writer) error {
	if n == 0 {
		return nil
	}
	if ofs > st.Size || n > st.Size-ofs {
		return fmt.Errorf("%w: %d stored bytes at %d, past the %d of %s", op.ErrInternalError, n, ofs, st.Size, objName(st))
	}
	if st.Manifest != nil {
		if err := checkWalkBound(st.Manifest, ofs, ofs+n-1); err != nil {
			return manifestErr(st, err)
		}
	}
	return s.stream(ctx, st, ofs, n, w)
}

// piece is one RADOS read: iterate_obj's (read_obj, obj_ofs, read_ofs, len,
// is_head_obj).
type piece struct {
	obj    meta.Obj
	rule   meta.PlacementRule
	inHead bool
	objOfs uint64 // offset in the object's stored bytes, the ordering key
	ofs    uint64 // offset within obj
	n      uint64
}

// rawObj is an rgw_raw_obj: what iterate_obj compares to tell the head.
type rawObj struct {
	pool     meta.Pool
	oid, loc string
}

// rawObj is rgw_obj_select::get_raw_obj: obj placed by rule, with the empty
// pool obj_to_raw leaves where none resolves.
func (s *Store) rawObj(rule meta.PlacementRule, obj meta.Obj) rawObj {
	pool, _ := s.dataPool(rule, obj.Bucket)
	stripe := meta.Stripe{Obj: obj}
	return rawObj{pool: pool, oid: stripe.OID(), loc: stripe.Locator()}
}

// walk is iterate_obj: it calls issue with each piece of the stored bytes
// [ofs, ofs+n), stripe by stripe and at most rgw_get_obj_max_req_size each,
// in offset order. A piece is the head's when its RADOS object is the head's,
// which iterate_obj compares as raw objects: an explicit manifest's pieces
// carry no placement rule, so the one naming the head is placed by the
// zonegroup's default placement and is the head only where that places the
// head too. A walk that stops moving, misses an offset, ends short of the
// range or passes meta.MaxWalkStripes stripes fails, where radosgw's loops
// forever, reads elsewhere or ends the response short.
func (s *Store) walk(st *op.ObjectState, ofs, n uint64, issue func(piece) error) error {
	end := ofs + n - 1
	rec := st.Bucket
	head := meta.Obj{Bucket: rec.Info.Bucket, Key: st.Key}
	if st.Manifest == nil {
		for ofs <= end {
			rd := min(end-ofs+1, s.readCfg.maxReq)
			if err := issue(piece{obj: head, rule: rec.Info.PlacementRule, inHead: true, objOfs: ofs, ofs: ofs, n: rd}); err != nil {
				return err
			}
			ofs += rd
		}
		return nil
	}
	headRaw := s.rawObj(rec.Info.PlacementRule, head)
	it, err := st.Manifest.Seek(ofs)
	if err != nil {
		return manifestErr(st, err)
	}
	for stripes := 1; !it.Done() && ofs <= end; stripes++ {
		if stripes > meta.MaxWalkStripes {
			return manifestErr(st, fmt.Errorf("%w: more than %d before offset %d", meta.ErrTooManyStripes, meta.MaxWalkStripes, ofs))
		}
		stripeOfs, locOfs := it.StripeOfs(), it.LocOfs()
		if ofs < stripeOfs {
			return manifestErr(st, fmt.Errorf("%w: no stripe holds offset %d, the next starts at %d", denc.ErrMalformed, ofs, stripeOfs))
		}
		next := stripeOfs + it.StripeSize()
		if next < stripeOfs {
			next = math.MaxUint64
		}
		obj, rule, _ := it.Location()
		inHead := s.rawObj(rule, obj) == headRaw
		read := false
		for ofs < next && ofs <= end {
			rd := min(end-ofs+1, next-ofs, s.readCfg.maxReq)
			if err := issue(piece{obj: obj, rule: rule, inHead: inHead, objOfs: ofs, ofs: locOfs + (ofs - stripeOfs), n: rd}); err != nil {
				return err
			}
			ofs += rd
			read = true
		}
		if ofs > end {
			return nil
		}
		prev := it.Ofs()
		if err := it.Next(); err != nil {
			return manifestErr(st, err)
		}
		if !it.Done() && (it.Ofs() < prev || it.Ofs() == prev && !read) {
			return manifestErr(st, fmt.Errorf("%w: manifest iteration does not advance past offset %d", denc.ErrMalformed, prev))
		}
	}
	if ofs <= end {
		return manifestErr(st, fmt.Errorf("%w: the manifest ends at %d, %d bytes short of the range", denc.ErrMalformed, ofs, end+1-ofs))
	}
	return nil
}

// checkWalkBound refuses a range whose walk would pass meta.MaxWalkStripes
// stripes before any of it is read: the bytes of [ofs, end] past the head,
// cut into the largest stripes any rule allows, are fewer stripes than the
// walk visits. An explicit manifest's walk is bounded by its pieces, which
// are already in memory.
func checkWalkBound(m *meta.Manifest, ofs, end uint64) error {
	if m.ExplicitObjs {
		return nil
	}
	var largest uint64
	for _, r := range m.Rules {
		largest = max(largest, r.StripeMaxSize)
	}
	start := max(ofs, m.HeadSize)
	if largest == 0 || start > end {
		return nil
	}
	span := end - start + 1
	if least := span/largest + min(span%largest, 1); least > meta.MaxWalkStripes {
		return fmt.Errorf("%w: at least %d, more than %d", meta.ErrTooManyStripes, least, meta.MaxWalkStripes)
	}
	return nil
}

// readQueue bounds the pieces issued ahead of the one being written.
const readQueue = 256

// result is one issued piece, written in issue order once done is closed.
type result struct {
	p    piece
	data []byte
	err  error
	// buf is the pooled buffer the read went into, nil for a piece served
	// from the prefetch or read into a buffer of its own.
	buf *[]byte
	// window is the piece's share of the window, its buffer's size, held
	// until it is written.
	window int64
	done   chan struct{}
}

// stream is Read::iterate with get_obj_iterate_cb and get_obj_data::flush:
// walk issues each piece, head bytes the prefetch holds are served without an
// op, the rest are read concurrently within rgw_get_obj_window_size, and the
// pieces are written to w in offset order. A piece's share of the window is
// the size of the buffer it reads into, at least the bytes it asks for, and it
// holds that share until it is written, not only while its read is in flight
// as in radosgw's throttle; so the buffers a request holds, the completions
// waiting on a slow earlier piece's among them, stay within the window. A
// buffer is at most rgw_get_obj_max_req_size, which Open keeps within the
// window, so a piece always fits. After a failure nothing more is written,
// and stream waits for every read it issued before it returns, as
// get_obj_data::cancel drains them.
func (s *Store) stream(ctx context.Context, st *op.ObjectState, ofs, n uint64, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	window := semaphore.NewWeighted(toInt64(s.readCfg.window))
	results := make(chan *result, readQueue)
	var reads sync.WaitGroup
	walked := make(chan error, 1)
	go func() {
		defer close(results)
		walked <- s.walk(st, ofs, n, func(p piece) error {
			return s.issue(ctx, st, p, window, &reads, results)
		})
	}()
	var failed error
	for r := range results {
		<-r.done
		if failed == nil {
			if failed = deliver(st, r, w); failed != nil {
				cancel()
			}
		}
		if r.window > 0 {
			window.Release(r.window)
		}
		s.recycle(r)
	}
	reads.Wait()
	if failed != nil {
		return failed
	}
	return <-walked
}

// issue queues p's result: from the prefetch for the head bytes it holds, as
// get_obj_iterate_cb serves them, and otherwise from a read started once the
// window has room for it.
func (s *Store) issue(ctx context.Context, st *op.ObjectState, p piece, window *semaphore.Weighted, reads *sync.WaitGroup, results chan<- *result) error {
	if have := uint64(len(st.Head)); p.inHead && p.objOfs < have {
		k := min(have-p.objOfs, p.n)
		served := p
		served.n = k
		done := make(chan struct{})
		close(done)
		results <- &result{p: served, data: st.Head[p.objOfs : p.objOfs+k], done: done}
		if k == p.n {
			return nil
		}
		p.objOfs, p.ofs, p.n = p.objOfs+k, p.ofs+k, p.n-k
	}
	weight := toInt64(s.readBufLen(p.n))
	if err := window.Acquire(ctx, weight); err != nil {
		return err
	}
	r := &result{p: p, window: weight, done: make(chan struct{})}
	reads.Go(func() {
		defer close(r.done)
		var buf []byte
		r.buf, buf = s.readBuf(p.n)
		r.data, r.err = s.readPiece(ctx, st, p, buf)
	})
	results <- r
	return nil
}

// readPiece is one op of get_obj_iterate_cb: on the head, append_atomic_test's
// guard that the idtag is still the one st was read with, then read(read_ofs,
// len) into buf. The guard's ECANCELED is ConcurrentModification
// (rgw_common.cc:141 at v19.2.6, :143 at v20.2.4).
func (s *Store) readPiece(ctx context.Context, st *op.ObjectState, p piece, buf []byte) ([]byte, error) {
	ref, err := s.rawRef(ctx, p.rule, p.obj)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	rop := radosclient.NewReadOp()
	if p.inHead && st.WriteTag != "" {
		rop.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, []byte(st.WriteTag))
	}
	rd := rop.ReadInto(p.ofs, buf)
	if _, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, op.FromRADOS(err, op.ScopeObject)
	}
	if rd.Err != nil {
		return nil, op.FromRADOS(rd.Err, op.ScopeObject)
	}
	return rd.Data[:rd.N], nil
}

// deliver writes r's bytes to w. A read shorter than its piece still hands on
// what it read, as flush does, and then fails: flush never meets the next
// piece's offset, so radosgw ends the response short without an error.
func deliver(st *op.ObjectState, r *result, w io.Writer) error {
	if r.err != nil {
		return r.err
	}
	if len(r.data) > 0 {
		k, err := w.Write(r.data)
		if err != nil {
			return err
		}
		if k < len(r.data) {
			return io.ErrShortWrite
		}
	}
	if got := uint64(len(r.data)); got < r.p.n {
		return fmt.Errorf("%w: read %d of %d bytes at %d of %s for %s: %w",
			op.ErrInternalError, got, r.p.n, r.p.ofs, meta.Stripe{Obj: r.p.obj}.OID(), objName(st), io.ErrUnexpectedEOF)
	}
	return nil
}

// pooledRead reports whether a read of n bytes takes a pooled buffer of
// rgw_get_obj_max_req_size: one of more than half that does, a smaller one a
// buffer of its own.
func (s *Store) pooledRead(n uint64) bool { return n > s.readCfg.maxReq/2 }

// readBufLen is the size of the buffer a read of n bytes goes into.
func (s *Store) readBufLen(n uint64) uint64 {
	if s.pooledRead(n) {
		return s.readCfg.maxReq
	}
	return n
}

// readBuf returns a buffer of n bytes for a read, cut from one of
// readBufLen(n), and that buffer when it is pooled.
func (s *Store) readBuf(n uint64) (pooled *[]byte, buf []byte) {
	if !s.pooledRead(n) {
		return nil, make([]byte, n)
	}
	if b, ok := s.readBufs.Get().(*[]byte); ok {
		s.pooledReadBufs.Add(-1)
		return b, (*b)[:n]
	}
	b := make([]byte, s.readCfg.maxReq)
	return &b, b[:n]
}

// recycle hands r's pooled buffer back, unless its read ended with a context
// error: the abandoned op may still write into it.
func (s *Store) recycle(r *result) {
	if r.buf == nil || errors.Is(r.err, context.Canceled) || errors.Is(r.err, context.DeadlineExceeded) {
		return
	}
	s.pooledReadBufs.Add(1)
	s.readBufs.Put(r.buf)
}

// manifestErr is a manifest a read cannot walk. radosgw has no answer of its
// own for one: its walk loops, reads elsewhere or ends short. rgw-go answers
// as for an attr that does not decode, -EIO's UnknownError.
func manifestErr(st *op.ObjectState, err error) error {
	return fmt.Errorf("%w: walking the manifest of %s: %w", op.ErrUnknown, objName(st), err)
}

// objName names st's object in errors: bucket/key.
func objName(st *op.ObjectState) string {
	return st.Bucket.Info.Bucket.Name + "/" + st.Key.Name
}

// toInt64 is u, or math.MaxInt64 when u is larger.
func toInt64(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}
