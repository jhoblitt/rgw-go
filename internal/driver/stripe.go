package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A PUT streams its body as radosgw's AtomicObjectProcessor does
// (driver/rados/rgw_putobj_processor.cc; a bare line number below is
// v19.2.6's, whose processor v20.2.4 keeps): the first head chunk stays in
// memory for the head write, and the rest goes to tail objects, stripe by
// stripe and chunk by chunk, with at most rgw_put_obj_min_window_size bytes
// of writes in flight.

// alignedSize is RGWRados::get_max_aligned_size (rgw_rados.cc:695-708 at
// v19.2.6, the same at v20.2.4).
func alignedSize(size, alignment uint64) uint64 {
	if alignment == 0 {
		return size
	}
	if size <= alignment {
		return alignment
	}
	return size - size%alignment
}

// poolAlignment is RGWRados::get_required_alignment (rgw_rados.cc:659-693 at
// v19.2.6): asked of the pool on every PUT, as radosgw asks librados, which
// answers from its OSD map.
func (s *Store) poolAlignment(ctx context.Context, p radosclient.Pool) (uint64, error) {
	a, err := p.RequiredAlignment(ctx)
	if err != nil {
		return 0, op.FromRADOS(err, op.ScopeObject)
	}
	return a, nil
}

// dataPoolHandle is rawRef without an object: the handle on the data pool
// rule places bucket's objects in, with rawRef's errors.
func (s *Store) dataPoolHandle(ctx context.Context, rule meta.PlacementRule, bucket meta.BucketID) (h radosclient.Pool, name meta.Pool, err error) {
	name, ok := s.dataPool(rule, bucket)
	if !ok {
		return nil, meta.Pool{}, fmt.Errorf("%w: no data pool for placement %q of bucket %s", op.ErrUnknown, rule, bucket.Name)
	}
	if name.Name == "" {
		return nil, meta.Pool{}, fmt.Errorf("%w: the data pool for placement %q of bucket %s has no name", op.ErrInvalidArgument, rule, bucket.Name)
	}
	h, err = s.pools.get(ctx, name)
	if err != nil {
		return nil, meta.Pool{}, op.FromRADOS(err, op.ScopeObject)
	}
	return h, name, nil
}

// layout is AtomicObjectProcessor::prepare's arithmetic for one PUT.
type layout struct {
	headPool, tailPool radosclient.Pool
	headRule, tailRule meta.PlacementRule
	chunk, stripe      uint64 // aligned rgw_max_chunk_size and rgw_obj_stripe_size
	maxHead            uint64 // the head chunk size, or 0 when the tail pool differs or inline_data is off
}

// planPut resolves the pools and sizes for a PUT of key under storageClass
// (rgw_putobj_processor.cc:268-341). The tail rule is the request's class
// on the bucket's placement, as rgw_op.cc:577-578 builds s->dest_placement.
// The stripe is aligned to the head pool's alignment, even when the tails
// live in another pool (:314-317).
func (s *Store) planPut(ctx context.Context, rec *op.BucketRecord, _ meta.ObjKey, storageClass string) (layout, error) {
	bucket := rec.Info.Bucket
	l := layout{headRule: rec.Info.PlacementRule}
	l.tailRule = meta.PlacementRule{StorageClass: storageClass}.InheritFrom(l.headRule)
	headPool, headName, err := s.dataPoolHandle(ctx, l.headRule, bucket)
	if err != nil {
		return layout{}, err
	}
	align, err := s.poolAlignment(ctx, headPool)
	if err != nil {
		return layout{}, err
	}
	headChunk := alignedSize(s.w.opts.chunkSize, align)
	l.headPool, l.tailPool, l.chunk = headPool, headPool, headChunk
	samePool := true
	if l.tailRule.Name != l.headRule.Name || l.tailRule.CanonicalStorageClass() != l.headRule.CanonicalStorageClass() {
		tailPool, tailName, err := s.dataPoolHandle(ctx, l.tailRule, bucket)
		if err != nil {
			return layout{}, err
		}
		if tailName != headName {
			samePool = false
			talign, err := s.poolAlignment(ctx, tailPool)
			if err != nil {
				return layout{}, err
			}
			l.tailPool, l.chunk = tailPool, alignedSize(s.w.opts.chunkSize, talign)
		}
	}
	if samePool {
		// get_placement looks the bucket's placement up by name alone, and
		// a placement it cannot find keeps the head inline (:304-310).
		if pi, ok := s.zone.Params.PlacementPools[l.headRule.Name]; !ok || pi.InlineData {
			l.maxHead = headChunk
		}
	}
	if l.chunk > s.w.opts.putWindow {
		// A pool's alignment raised the chunk above the window, through which
		// BlockingAioThrottle::get lets no piece of a chunk: it fails it with
		// EDEADLK (rgw_aio_throttle.cc:40-42 at v19.2.6 and v20.2.4), which no
		// S3 error names, so set_req_state_err answers 500 UnknownError
		// (rgw_common.cc:346-353 at v19.2.6, :359-366 at v20.2.4).
		return layout{}, fmt.Errorf("%w: the %d-byte chunk of pool %s exceeds rgw_put_obj_min_window_size %d",
			op.ErrUnknown, l.chunk, l.tailPool.Name(), s.w.opts.putWindow)
	}
	l.stripe = alignedSize(s.w.opts.stripeSize, align)
	return l, nil
}

// tailStripe is one tail object a PUT issued, its stripe number, whether a
// write to it succeeded, which is what RadosWriter's written set records, and
// the end of its latest piece's write, which the next piece waits for.
type tailStripe struct {
	n       uint64
	obj     meta.Obj
	written bool
	last    chan struct{}
}

// firstPiece is a part's first stripe as HeadObjectProcessor holds it for
// process_first_chunk: its buffer, the n bytes read into it, and whether the
// body ended within it.
type firstPiece struct {
	p    *[]byte
	n    int
	done bool
}

// tailWriter is RadosWriter with the StripeProcessor and ChunkProcessor above
// it and the put window around it: it streams a body into tail objects while
// the caller keeps the head's bytes, remembers every tail it wrote and can
// delete them all. Its writes run under the driver's lifetime context, so a
// client disconnect cannot abandon one half-applied.
type tailWriter struct {
	s     *Store
	m     *meta.Manifest
	pool  radosclient.Pool
	chunk uint64
	// maxSize is rgw_max_put_size, to which RGWPutObj_ObjStore::get_data
	// holds a request's body; copy_obj_data's stream, which no request
	// carries, has no limit.
	maxSize uint64
	sem     *semaphore.Weighted // the put window, in bytes
	wg      sync.WaitGroup

	// namer names stripe n; nil is m.TailObj. A part writer names stripe 0
	// the part head, "<prefix>.<part>", and the rest its shadow stripes,
	// "<prefix>.<part>_<n>".
	namer func(n uint64) meta.Obj
	// exclusiveFirst writes the body's first stripe as
	// MultipartObjectProcessor::process_first_chunk does, through writeFirst.
	exclusiveFirst bool
	// first is that stripe when its exclusive create met EEXIST, kept so a
	// writer under another prefix can send it without the body being read
	// again (rgw_putobj_processor.cc:414-439 at v19.2.6, :448-473 at
	// v20.2.4).
	first *firstPiece
	// keepHead leaves stripe 0 out of discard: ~RadosWriter removes a part
	// head that sits where the bucket's placement puts heads through the
	// bucket index, after every other stripe (rgw_putobj_processor.cc:184-229
	// at v19.2.6), which is the caller's to do.
	keepHead bool

	mu      sync.Mutex
	stripes []tailStripe
	err     error  // the first failed write
	held    uint64 // bytes of tail buffers taken and not handed back
	peak    uint64
}

func (s *Store) newTailWriter(m *meta.Manifest, l layout) *tailWriter {
	return &tailWriter{
		s: s, m: m, pool: l.tailPool, chunk: l.chunk, maxSize: s.w.opts.maxPutSize,
		sem: semaphore.NewWeighted(int64(s.w.opts.putWindow)), //nolint:gosec // rgw_put_obj_min_window_size is a size, far below 2^63
	}
}

// newPartWriter is newTailWriter for part n of m, a part manifest: its
// stripes are m.StripeObj(n, ·) in l.tailPool, the first created
// exclusively.
func (s *Store) newPartWriter(m *meta.Manifest, l layout, n uint32) *tailWriter {
	t := s.newTailWriter(m, l)
	t.namer = func(stripe uint64) meta.Obj { return m.StripeObj(n, stripe) }
	t.exclusiveFirst = true
	return t
}

// buf takes a buffer of at least n bytes, one chunk when it allocates.
func (t *tailWriter) buf(n uint64) *[]byte {
	p, ok := t.s.tailBufs.Get().(*[]byte)
	if !ok || uint64(cap(*p)) < n {
		b := make([]byte, max(n, t.chunk))
		p = &b
	}
	t.hold(p)
	return p
}

// hold counts p among the buffers the writer holds.
func (t *tailWriter) hold(p *[]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.held += uint64(cap(*p))
	t.peak = max(t.peak, t.held)
}

// release hands p back for the next piece; pool is false for a buffer an
// abandoned write may still read, which is dropped instead. Only a buffer of
// one chunk is pooled: a piece weighs its buffer's capacity in the window, so
// a part's first-stripe buffer reused for a smaller piece would hold room the
// piece does not need.
func (t *tailWriter) release(p *[]byte, pool bool) {
	t.mu.Lock()
	t.held -= uint64(cap(*p))
	t.mu.Unlock()
	if pool && uint64(cap(*p)) == t.chunk {
		t.s.tailBufs.Put(p)
	}
}

// stripeObj is the object stripe n is written to.
func (t *tailWriter) stripeObj(n uint64) meta.Obj {
	if t.namer != nil {
		return t.namer(n)
	}
	return t.m.TailObj(n)
}

// addStripe records tail stripe n as issued and returns its index.
func (t *tailWriter) addStripe(n uint64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stripes = append(t.stripes, tailStripe{n: n, obj: t.stripeObj(n)})
	return len(t.stripes) - 1
}

// write is RadosWriter::process (rgw_putobj_processor.cc:140-159) for the
// first n bytes of p at ofs within stripe idx: [set_alloc_hint2(0, 0, 0)]
// and write_full at offset 0, write elsewhere, once the window has room for
// the piece's buffer. A write that failed since the last piece is reported
// here, as process_completed reports the completions aio->get returns.
// Stripes are written in parallel, but radosgw submits a stripe's pieces from
// one thread in stream order, so each piece waits for the write of the one
// before it: a later piece landing first would leave the stripe's write_full
// to truncate it.
func (t *tailWriter) write(ctx context.Context, idx int, ofs uint64, p *[]byte, n int) error {
	t.mu.Lock()
	failed := t.err
	obj := t.stripes[idx].obj
	t.mu.Unlock()
	if failed != nil {
		t.release(p, true)
		return op.FromRADOS(failed, op.ScopeObject)
	}
	// planPut keeps every chunk, and so every buffer, within the window.
	weight := int64(cap(*p))
	if err := t.sem.Acquire(ctx, weight); err != nil {
		t.release(p, true)
		return err
	}
	done := make(chan struct{})
	t.mu.Lock()
	prev := t.stripes[idx].last
	t.stripes[idx].last = done
	t.mu.Unlock()
	stripe := meta.Stripe{Obj: obj}
	pool := t.pool
	if loc := stripe.Locator(); loc != "" {
		pool = pool.WithLocator(loc)
	}
	data := (*p)[:n]
	t.wg.Go(func() {
		defer t.sem.Release(weight)
		defer close(done)
		if prev != nil {
			<-prev
		}
		w := radosclient.NewWriteOp()
		// add_write_hint: only a write this request compresses is hinted
		// incompressible, and nothing here compresses.
		w.SetAllocHint(0, 0, 0)
		if ofs == 0 {
			w.WriteFull(data)
		} else {
			w.Write(data, ofs)
		}
		_, err := pool.Write(t.s.w.completions.ctx, stripe.OID(), w, radosclient.OpFlagNone)
		t.release(p, !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded))
		t.mu.Lock()
		defer t.mu.Unlock()
		switch {
		case err == nil:
			t.stripes[idx].written = true
		case t.err == nil:
			t.err = err
		}
	})
	return nil
}

// readPiece fills buf from r. done reports that r returned io.EOF, a complete
// body; io.ErrUnexpectedEOF from r itself (auth's truncated stream) is an
// error, while a short final piece is not.
func readPiece(r io.Reader, buf []byte) (n int, done bool, err error) {
	for n < len(buf) {
		k, rerr := r.Read(buf[n:])
		n += k
		switch {
		case rerr == nil:
		case errors.Is(rerr, io.EOF):
			return n, true, nil
		default:
			return n, false, rerr
		}
	}
	return n, false, nil
}

// bodyErr maps the reader's verdicts: io.ErrUnexpectedEOF is
// ErrRequestTimeout, as a body that ended short of its length is
// (rgw_op.cc:4430-4433 at v19.2.6, :4662-4665 at v20.2.4); an *op.Error
// (auth's verification failures) and a context error pass through.
func bodyErr(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return op.ErrRequestTimeout
	}
	return err
}

// readHead reads the first m.MaxHeadSize bytes of r, the head's data. Its
// buffer is sized as RGWPutObj_ObjStore::get_data sizes a read
// (rgw_rest.cc:1068-1085 at v19.2.6): the declared length when it is
// smaller, so a small object does not cost a whole chunk. A body that runs
// past its declared length grows it.
func (t *tailWriter) readHead(r io.Reader, declared int64) (head []byte, done bool, err error) {
	limit := t.m.MaxHeadSize
	if limit == 0 {
		return []byte{}, false, nil
	}
	n := limit
	if declared >= 0 && uint64(declared) < limit {
		n = uint64(declared)
	}
	head = make([]byte, n)
	got, done, err := readPiece(r, head)
	head = head[:got]
	var probe [512]byte
	for err == nil && !done && uint64(len(head)) < limit {
		var k int
		k, done, err = readPiece(r, probe[:min(uint64(len(probe)), limit-uint64(len(head)))])
		head = append(head, probe[:k]...)
	}
	return head, done, err
}

// consume reads body to EOF, writing every byte to h: the first
// m.MaxHeadSize bytes come back as the head's data, the rest go to tail
// objects in stripe order, each stripe in pieces of at most one chunk, as
// StripeProcessor and ChunkProcessor cut them (rgw_putobj.cc). A stripe is
// issued only when data for it arrives, and no empty write is sent. A body
// error is returned as it is, except io.ErrUnexpectedEOF, which is
// ErrRequestTimeout; a body past maxSize is EntityTooLarge
// (rgw_rest.cc:1096-1098 at v19.2.6, :1101-1103 at v20.2.4). declared is the
// body's length, -1 when unknown.
//
// With exclusiveFirst there is no head: the first stripe is read whole and
// written by writeFirst before anything else is read, and the rest follows
// as above. When its exclusive create meets EEXIST, consume returns that
// error, radosclient.ErrExists, keeping the stripe in t.first.
func (t *tailWriter) consume(ctx context.Context, body io.Reader, declared int64, h io.Writer) (head []byte, size uint64, err error) {
	r := io.TeeReader(body, h)
	if t.exclusiveFirst {
		size, err = t.consumeFirst(ctx, r, declared)
		return nil, size, err
	}
	head, done, err := t.readHead(r, declared)
	if err != nil {
		return nil, 0, bodyErr(err)
	}
	size = uint64(len(head))
	if size > t.maxSize {
		return nil, 0, op.ErrEntityTooLarge
	}
	size, err = t.stream(ctx, r, size, done)
	if err != nil {
		return nil, 0, err
	}
	return head, size, nil
}

// consumeFirst is consume for a part: HeadObjectProcessor holds the first
// stripe, a part's head chunk being the stripe size
// (rgw_putobj_processor.cc:476-478 at v19.2.6, :510-512 at v20.2.4), until it
// is whole or the body ends, then hands it to process_first_chunk. The
// stripe takes its room in the put window before it is read, and is read
// into a buffer sized as readHead sizes the head, so a small part costs no
// whole stripe and no part holds more than the window and one byte.
func (t *tailWriter) consumeFirst(ctx context.Context, r io.Reader, declared int64) (uint64, error) {
	window := t.s.w.opts.putWindow
	limit := min(t.m.Rules[0].StripeMaxSize, window+1)
	// Nothing was issued before this stripe, so the window is empty.
	weight := int64(min(limit, window)) //nolint:gosec // rgw_put_obj_min_window_size is a size, far below 2^63
	if err := t.sem.Acquire(ctx, weight); err != nil {
		return 0, err
	}
	f, err := t.readFirst(r, declared, limit)
	if err == nil {
		err = t.writeFirst(f)
	}
	t.sem.Release(weight)
	if err != nil {
		return 0, err
	}
	return t.stream(ctx, r, uint64(f.n), f.done) //nolint:gosec // a read count is never negative
}

// readFirst reads the first stripe, at most limit bytes, into a buffer of
// the declared length when that is smaller, grown to limit when the body
// runs past it. A stripe longer than the put window cannot pass
// BlockingAioThrottle::get, which fails it with EDEADLK
// (rgw_aio_throttle.cc:40-42 at v19.2.6 and v20.2.4), answered 500
// UnknownError by set_req_state_err; reading one byte past the window is
// enough to know.
func (t *tailWriter) readFirst(r io.Reader, declared int64, limit uint64) (*firstPiece, error) {
	n := limit
	if declared >= 0 && uint64(declared) < n {
		n = uint64(declared)
	}
	buf := make([]byte, n)
	got, done, err := readPiece(r, buf)
	if err == nil && !done && uint64(got) < limit { //nolint:gosec // a read count is never negative
		var probe [512]byte
		var k int
		k, done, err = readPiece(r, probe[:min(uint64(len(probe)), limit-uint64(got))]) //nolint:gosec // a read count is never negative
		if k > 0 {
			grown := make([]byte, got+k, limit)
			copy(grown, buf[:got])
			copy(grown[got:], probe[:k])
			buf, got = grown, got+k
			if err == nil && !done {
				k, done, err = readPiece(r, buf[got:limit])
				got += k
			}
		}
	}
	p := &buf
	t.hold(p)
	switch {
	case err != nil:
		t.release(p, true)
		return nil, bodyErr(err)
	case uint64(got) > t.maxSize: //nolint:gosec // a read count is never negative
		t.release(p, true)
		return nil, op.ErrEntityTooLarge
	case uint64(got) > t.s.w.opts.putWindow: //nolint:gosec // a read count is never negative
		t.release(p, true)
		return nil, fmt.Errorf("%w: the first stripe of %s exceeds rgw_put_obj_min_window_size %d",
			op.ErrUnknown, t.stripeObj(0).Key.Name, t.s.w.opts.putWindow)
	}
	return &firstPiece{p: p, n: got, done: done}, nil
}

// resume is consume for a part writer taking over prev's first stripe, which
// met EEXIST under prev's prefix: it sends that stripe again under its own,
// as process_first_chunk resubmits its data to the new head, and streams the
// rest of body, hashing it into h, which holds the first stripe already.
func (t *tailWriter) resume(ctx context.Context, prev *tailWriter, body io.Reader, h io.Writer) (uint64, error) {
	f := prev.first
	prev.first = nil
	held := uint64(cap(*f.p))
	prev.mu.Lock()
	prev.held -= held
	prev.mu.Unlock()
	t.hold(f.p)
	weight := int64(min(held, t.s.w.opts.putWindow)) //nolint:gosec // rgw_put_obj_min_window_size is a size, far below 2^63
	if err := t.sem.Acquire(ctx, weight); err != nil {
		t.release(f.p, true)
		return 0, err
	}
	err := t.writeFirst(f)
	t.sem.Release(weight)
	if err != nil {
		return 0, err
	}
	return t.stream(ctx, io.TeeReader(body, h), uint64(f.n), f.done) //nolint:gosec // a read count is never negative
}

// writeFirst is RadosWriter::write_exclusive (rgw_putobj_processor.cc:161-177
// at v19.2.6, :189-205 at v20.2.4) of a part's first stripe to stripe 0, the
// part head: create(true), set_alloc_hint2(0, 0, 0) and write_full in one op,
// awaited before anything else is issued, under the window room the caller
// holds. An empty part still sends it, with an empty write_full. EEXIST
// keeps f in t.first and is returned as it is.
func (t *tailWriter) writeFirst(f *firstPiece) error {
	obj := t.stripeObj(0)
	stripe := meta.Stripe{Obj: obj}
	pool := t.pool
	if loc := stripe.Locator(); loc != "" {
		pool = pool.WithLocator(loc)
	}
	w := radosclient.NewWriteOp()
	w.Create(true)
	w.SetAllocHint(0, 0, 0)
	w.WriteFull((*f.p)[:f.n])
	_, err := pool.Write(t.s.w.completions.ctx, stripe.OID(), w, radosclient.OpFlagNone)
	switch {
	case err == nil:
		t.release(f.p, true)
		t.mu.Lock()
		t.stripes = append(t.stripes, tailStripe{n: 0, obj: obj, written: true})
		t.mu.Unlock()
		return nil
	case errors.Is(err, radosclient.ErrExists):
		t.first = f
		return err
	default:
		t.release(f.p, !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded))
		return op.FromRADOS(err, op.ScopeObject)
	}
}

// stream writes the rest of r, from offset size, to the stripes it falls in,
// and returns the body's length. done reports that r has ended already.
func (t *tailWriter) stream(ctx context.Context, r io.Reader, size uint64, done bool) (uint64, error) {
	stripeMax := t.m.Rules[0].StripeMaxSize
	for !done {
		n := t.m.TailStripe(size)
		idx := -1
		for off := uint64(0); off < stripeMax && !done; {
			want := min(t.chunk, stripeMax-off)
			p := t.buf(want)
			got, d, err := readPiece(r, (*p)[:want])
			if err != nil {
				t.release(p, true)
				return 0, bodyErr(err)
			}
			done = d
			if got == 0 {
				t.release(p, true)
				continue
			}
			piece := uint64(got) //nolint:gosec // a read count is never negative
			if size+piece > t.maxSize {
				t.release(p, true)
				return 0, op.ErrEntityTooLarge
			}
			if idx < 0 {
				idx = t.addStripe(n)
			}
			wctx := ctx
			if done {
				// The body is whole: its last piece waits for the window
				// whether or not the client stays, as radosgw's does.
				wctx = context.WithoutCancel(ctx)
			}
			if err := t.write(wctx, idx, off, p, got); err != nil {
				return 0, err
			}
			off += piece
			size += piece
		}
	}
	return size, nil
}

// drain is RadosWriter::drain (rgw_putobj_processor.cc:179-182): it waits
// for every tail write and returns the first failure.
func (t *tailWriter) drain() error {
	t.wg.Wait()
	t.mu.Lock()
	defer t.mu.Unlock()
	return op.FromRADOS(t.err, op.ScopeObject)
}

// discard is ~RadosWriter's cleanup (rgw_putobj_processor.cc:184-229): after
// the writes in flight end, every tail a write reached is removed
// (delete_raw_obj), a missing one ignored and a failure logged as leaked;
// with keepHead, stripe 0 is left. It runs under the driver's lifetime
// context, not the request's.
func (t *tailWriter) discard() {
	t.wg.Wait()
	if t.first != nil {
		t.release(t.first.p, true)
		t.first = nil
	}
	ctx := t.s.w.completions.ctx
	t.mu.Lock()
	stripes := t.stripes
	t.mu.Unlock()
	for i := range stripes {
		if !stripes[i].written || t.keepHead && stripes[i].n == 0 {
			continue
		}
		stripe := meta.Stripe{Obj: stripes[i].obj}
		pool := t.pool
		if loc := stripe.Locator(); loc != "" {
			pool = pool.WithLocator(loc)
		}
		w := radosclient.NewWriteOp()
		w.Remove()
		if _, err := pool.Write(ctx, stripe.OID(), w, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
			slog.WarnContext(ctx, "tail object leaked after a failed write",
				slog.String("pool", pool.Name()), slog.String("oid", stripe.OID()), slog.Any("error", err))
		}
	}
}

// headWritten reports that a write reached stripe 0.
func (t *tailWriter) headWritten() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.stripes {
		if t.stripes[i].n == 0 && t.stripes[i].written {
			return true
		}
	}
	return false
}
