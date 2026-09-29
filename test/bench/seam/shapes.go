package seam

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Shape is one operation composition the benchmark issues through the seam.
// Prepare runs once per cell and may be called again for a recalibrated cell,
// so it is idempotent; Run issues iteration i for worker w; Cleanup removes
// what Prepare and Run created, every object in the pool's namespace, and is
// idempotent too.
//
// What iterations create is named for the run their shape's latest Prepare
// started, so a recalibrated cell's round cannot collide with the objects of
// an earlier round, which stay until the cell's cleanup. Runs for different
// workers overlap; one worker's do not.
type Shape interface {
	Name() string
	// Size is the data extent one Run writes or asks to read, in bytes,
	// whatever the object holds: what librados's objecter charges the Run
	// against its byte throttle, less the names and values of any xattrs it
	// sets. The byte budget counts it. It is not always what a Run
	// transfers: headread4k's is the whole 4 MiB first chunk it asks for of
	// a 4 KiB object, and headwrite4k's is its 4096 bytes of data, though
	// with its xattrs the objecter charges 4717. Only the four rados bench
	// mirrors, read4k, write4k, read4m and write4m, have a floor, matched by
	// shape; theirs is the object size.
	Size() int
	// Ops is how many RADOS operations one Run issues (1, or 2 for the index shape).
	Ops() int
	Prepare(ctx context.Context, p radosclient.Pool, r denc.Release, workers int) error
	Run(ctx context.Context, p radosclient.Pool, w, i int) error
	Cleanup(ctx context.Context, p radosclient.Pool) error
}

const (
	smallSize = 4 << 10
	largeSize = 4 << 20 // rados bench's default object size
	// chunkSize is rgw_max_chunk_size's default: the data a head holds and
	// what a GET's first read asks for.
	chunkSize = 4 << 20

	// prepareInFlight bounds Prepare's writes: sixteen 4 MiB objects stay
	// under the objecter's 100 MiB byte throttle.
	prepareInFlight = 16
	removeInFlight  = 64
)

// runs numbers the Prepare calls of every shape that names objects by run.
var runs atomic.Int64

// Shapes returns every shape in sweep order: read4k, write4k, read4m,
// write4m (the four rados bench mirrors), headread4k, headwrite4k, indexrtt.
func Shapes() []Shape {
	return []Shape{
		&readShape{name: "read4k", size: smallSize, ops: 1},
		&writeShape{name: "write4k", size: smallSize, ops: 1},
		&readShape{name: "read4m", size: largeSize, ops: 1},
		&writeShape{name: "write4m", size: largeSize, ops: 1},
		// radosgw asks for the whole first chunk however small the object,
		// and the objecter charges a read the length it asks for.
		&headReadShape{name: "headread4k", size: chunkSize, ops: 1},
		&headWriteShape{name: "headwrite4k", size: smallSize, ops: 1},
		// Class-method calls carry no object data, and the objecter charges
		// them nothing.
		&indexShape{name: "indexrtt", size: 0, ops: 2},
	}
}

// ByName returns the named shapes, or an error naming the unknown one.
func ByName(names []string) ([]Shape, error) {
	all := Shapes()
	out := make([]Shape, 0, len(names))
	for _, name := range names {
		i := slices.IndexFunc(all, func(s Shape) bool { return s.Name() == name })
		if i < 0 {
			known := make([]string, len(all))
			for j, s := range all {
				known[j] = s.Name()
			}
			return nil, fmt.Errorf("unknown shape %q, want one of %s", name, strings.Join(known, ", "))
		}
		out = append(out, all[i])
	}
	return out, nil
}

// shape is what every composition shares.
type shape struct {
	name string
	size int
	ops  int
}

func (s *shape) Name() string { return s.name }

func (s *shape) Size() int { return s.size }

func (s *shape) Ops() int { return s.ops }

// Cleanup removes every object in p's namespace; one already gone counts as
// removed. Nothing the shapes write has a locator.
func (*shape) Cleanup(ctx context.Context, p radosclient.Pool) error {
	var oids []string
	if err := p.ListObjects(ctx, func(oid, _ string) error {
		oids = append(oids, oid)
		return nil
	}); err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(removeInFlight)
	for _, oid := range oids {
		g.Go(func() error {
			op := radosclient.NewWriteOp()
			op.Remove()
			_, err := p.Write(ctx, oid, op, radosclient.OpFlagNone)
			if errors.Is(err, radosclient.ErrNotFound) {
				return nil
			}
			return err
		})
	}
	return g.Wait()
}

// readShape is rados bench's rand mode: a read of a whole object, the
// worker's own, into a buffer the worker reuses.
type readShape struct {
	shape
	oids []string
	bufs [][]byte
}

// Prepare writes each worker's object, src-<w>, and gives every worker a
// new buffer: a read abandoned at an earlier deadline may still fill the old.
func (s *readShape) Prepare(ctx context.Context, p radosclient.Pool, _ denc.Release, workers int) error {
	s.oids = workerOIDs("src-", workers)
	s.bufs = buffers(workers, s.size)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(prepareInFlight)
	for w := range workers {
		g.Go(func() error {
			op := radosclient.NewWriteOp()
			op.WriteFull(payload(w, s.size))
			_, err := p.Write(ctx, s.oids[w], op, radosclient.OpFlagNone)
			return err
		})
	}
	return g.Wait()
}

// Run submits nothing once ctx has ended, so a buffer that an abandoned read
// may still fill is never handed to librados again.
func (s *readShape) Run(ctx context.Context, p radosclient.Pool, w, _ int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	op := radosclient.NewReadOp()
	op.ReadInto(0, s.bufs[w])
	_, err := p.Read(ctx, s.oids[w], op, radosclient.OpFlagNone)
	return err
}

// writeShape is rados bench's write mode: a whole-object write of a new
// object every iteration.
type writeShape struct {
	shape
	run      int64
	payloads [][]byte
}

func (s *writeShape) Prepare(_ context.Context, _ radosclient.Pool, _ denc.Release, workers int) error {
	s.run = runs.Add(1)
	s.payloads = make([][]byte, workers)
	for w := range workers {
		s.payloads[w] = payload(w, s.size)
	}
	return nil
}

func (s *writeShape) Run(ctx context.Context, p radosclient.Pool, w, i int) error {
	op := radosclient.NewWriteOp()
	op.WriteFull(s.payloads[w])
	_, err := p.Write(ctx, iterOID("w", s.run, w, i), op, radosclient.OpFlagNone)
	return err
}

// headReadShape is a GET within the head: raw_obj_stat with its first chunk,
// one read op of the object version read that prepare_op_for_read composes,
// every xattr, the stat and the first rgw_max_chunk_size bytes.
type headReadShape struct {
	shape
	release denc.Release
	oids    []string
	bufs    [][]byte
}

// Prepare writes each worker's head, head-<w>: 4 KiB of data under the seven
// xattrs a new object's head write sets. Every worker gets a new buffer, as
// readShape's do.
func (s *headReadShape) Prepare(ctx context.Context, p radosclient.Pool, r denc.Release, workers int) error {
	s.release = r
	s.oids = workerOIDs("head-", workers)
	s.bufs = buffers(workers, chunkSize)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(prepareInFlight)
	for w := range workers {
		g.Go(func() error {
			h := newHead(w)
			op := radosclient.NewWriteOp()
			op.WriteFull(h.data)
			op.SetXattr(meta.AttrIDTag, h.tag)
			op.SetXattr(meta.AttrTailTag, h.tag)
			op.SetXattr(meta.AttrManifest, h.manifest)
			op.SetXattr(meta.AttrACL, h.acl)
			op.SetXattr(meta.AttrContentType, h.contentType)
			op.SetXattr(meta.AttrETag, h.etag)
			op.SetXattr(meta.AttrSourceZone, h.sourceZone)
			_, err := p.Write(ctx, s.oids[w], op, radosclient.OpFlagNone)
			return err
		})
	}
	return g.Wait()
}

// Run submits nothing once ctx has ended, as readShape's does.
func (s *headReadShape) Run(ctx context.Context, p radosclient.Pool, w, _ int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	op := radosclient.NewReadOp()
	version.Read(op, s.release)
	op.GetXattrs()
	op.Stat()
	op.ReadInto(0, s.bufs[w])
	_, err := p.Read(ctx, s.oids[w], op, radosclient.OpFlagNone)
	return err
}

// headWriteShape is a PUT of a new object within the head.
type headWriteShape struct {
	shape
	release denc.Release
	run     int64
	heads   []head
}

func (s *headWriteShape) Prepare(_ context.Context, _ radosclient.Pool, r denc.Release, workers int) error {
	s.release = r
	s.run = runs.Add(1)
	s.heads = make([]head, workers)
	for w := range workers {
		s.heads[w] = newHead(w)
	}
	return nil
}

// Run composes the head write as radosgw does for an object that did not
// exist. prepare_atomic_modification creates it exclusively and sets the
// write tag as idtag and tail_tag; _do_write_meta stamps the mtime, writes
// the data, sends no alloc hint (it sends one only over a compressed
// object), sets the manifest and then the attrs in std::map name order,
// stores the PG version, and sets the source zone.
func (s *headWriteShape) Run(ctx context.Context, p radosclient.Pool, w, i int) error {
	h := &s.heads[w]
	op := radosclient.NewWriteOp()
	op.Create(true)
	op.SetXattr(meta.AttrIDTag, h.tag)
	op.SetXattr(meta.AttrTailTag, h.tag)
	op.SetMtime(time.Now())
	op.WriteFull(h.data)
	op.SetXattr(meta.AttrManifest, h.manifest)
	op.SetXattr(meta.AttrACL, h.acl)
	op.SetXattr(meta.AttrContentType, h.contentType)
	op.SetXattr(meta.AttrETag, h.etag)
	rgw.ObjStorePGVer(op, meta.AttrPGVer, s.release)
	op.SetXattr(meta.AttrSourceZone, h.sourceZone)
	_, err := p.Write(ctx, iterOID("hw", s.run, w, i), op, radosclient.OpFlagNone)
	return err
}

// indexShape is the bucket index half of a PUT: cls_obj_prepare_op's op, then
// the op the completion manager sends for bucket_complete_op, each asserting
// that the shard exists and guarding against resharding. Every worker has a
// shard of its own, so no two workers contend for one object, and reuses one
// write tag: each key's pending entry is its own, so the tag need only match
// between a key's prepare and its complete.
type indexShape struct {
	shape
	release denc.Release
	run     int64
	shards  []string
	tags    []string
	etags   []string
}

// Prepare names each worker's shard, .dir.bench-<run>.<w>, and creates the
// ones a stat finds missing as radosgw creates a bucket's index shards: an
// exclusive create and bucket_init_index.
func (s *indexShape) Prepare(ctx context.Context, p radosclient.Pool, r denc.Release, workers int) error {
	s.release = r
	s.run = runs.Add(1)
	s.shards = make([]string, workers)
	s.tags = make([]string, workers)
	s.etags = make([]string, workers)
	for w := range workers {
		s.shards[w] = ".dir.bench-" + strconv.FormatInt(s.run, 10) + "." + strconv.Itoa(w)
		rnd := rng(w)
		s.tags[w] = randTag(rnd)
		s.etags[w] = randETag(rnd)
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(prepareInFlight)
	for _, shard := range s.shards {
		g.Go(func() error {
			stat := radosclient.NewReadOp()
			stat.Stat()
			_, err := p.Read(ctx, shard, stat, radosclient.OpFlagNone)
			if !errors.Is(err, radosclient.ErrNotFound) {
				return err
			}
			op := radosclient.NewWriteOp()
			op.Create(true)
			rgw.BucketInitIndex(op)
			_, err = p.Write(ctx, shard, op, radosclient.OpFlagNone)
			return err
		})
	}
	return g.Wait()
}

// Run adds key-<run>-<w>-<i> to worker w's shard: bucket_prepare_op records
// the pending change under the write tag, and bucket_complete_op applies it
// under the same tag. Epoch stands in for the object version a head write
// returns, which radosgw records in the entry.
func (s *indexShape) Run(ctx context.Context, p radosclient.Pool, w, i int) error {
	key := rgw.ObjKey{Name: iterOID("key", s.run, w, i)}
	prepare := radosclient.NewWriteOp()
	prepare.AssertExists()
	rgw.GuardBucketResharding(prepare, s.release)
	rgw.BucketPrepareOp(prepare, rgw.PrepareOp{Op: rgw.OpAdd, Key: key, Tag: s.tags[w]}, s.release)
	if _, err := p.Write(ctx, s.shards[w], prepare, radosclient.OpFlagNone); err != nil {
		return fmt.Errorf("index prepare: %w", err)
	}
	complete := radosclient.NewWriteOp()
	complete.AssertExists()
	rgw.GuardBucketResharding(complete, s.release)
	rgw.BucketCompleteOp(complete, rgw.CompleteOp{
		Op:  rgw.OpAdd,
		Key: key,
		Tag: s.tags[w],
		Ver: rgw.EntryVer{Pool: p.ID(), Epoch: uint64(i) + 1}, //nolint:gosec // an iteration index is never negative
		Meta: rgw.DirEntryMeta{
			Category:         rgw.CategoryMain,
			Size:             smallSize,
			Mtime:            time.Now(),
			ETag:             s.etags[w],
			Owner:            "bench",
			OwnerDisplayName: "bench",
			ContentType:      "application/octet-stream",
			AccountedSize:    smallSize,
		},
	}, s.release)
	if _, err := p.Write(ctx, s.shards[w], complete, radosclient.OpFlagNone); err != nil {
		return fmt.Errorf("index complete: %w", err)
	}
	return nil
}

// head is one worker's head: its data and radosgw's xattrs at a small
// object's sizes. radosgw draws a new write tag for every PUT; a worker
// reuses its own, which the OSD stores alike.
type head struct {
	// tag is the write tag with the NUL prepare_atomic_modification appends
	// to the idtag and tail_tag xattrs.
	tag         []byte
	data        []byte
	manifest    []byte
	acl         []byte
	contentType []byte
	etag        []byte
	sourceZone  []byte
}

func newHead(w int) head {
	rnd := rng(w)
	h := head{
		tag:         append([]byte(randTag(rnd)), 0),
		etag:        []byte(randETag(rnd)),
		data:        make([]byte, smallSize),
		manifest:    make([]byte, 180),
		acl:         make([]byte, 200),
		contentType: []byte("application/octet-stream\x00"),
		sourceZone:  binary.LittleEndian.AppendUint32(nil, rnd.Uint32()),
	}
	fill(rnd, h.data)
	fill(rnd, h.manifest)
	fill(rnd, h.acl)
	return h
}

// tagChars is gen_rand_alphanumeric's table (src/common/random_string.cc).
const tagChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// randTag is append_rand_alpha(cct, "", tag, 32) (src/rgw/rgw_common.h): "_"
// and the 31 characters gen_rand_alphanumeric puts before the NUL of a
// 32-byte buffer.
func randTag(rnd *rand.Rand) string {
	b := make([]byte, 1, 32)
	b[0] = '_'
	for range 31 {
		b = append(b, tagChars[rnd.IntN(len(tagChars))])
	}
	return string(b)
}

// randETag is an MD5-sized ETag in hex, as a single-part PUT's is.
func randETag(rnd *rand.Rand) string {
	var sum [16]byte
	fill(rnd, sum[:])
	return hex.EncodeToString(sum[:])
}

// rng is worker w's deterministic source.
func rng(w int) *rand.Rand {
	return rand.New(rand.NewPCG(1, uint64(w))) //nolint:gosec // payloads and tags only need to be repeatable, not secret
}

func payload(w, size int) []byte {
	b := make([]byte, size)
	fill(rng(w), b)
	return b
}

func fill(rnd *rand.Rand, b []byte) {
	var word [8]byte
	for i := 0; i < len(b); i += len(word) {
		binary.LittleEndian.PutUint64(word[:], rnd.Uint64())
		copy(b[i:], word[:])
	}
}

func buffers(workers, size int) [][]byte {
	bufs := make([][]byte, workers)
	for w := range bufs {
		bufs[w] = make([]byte, size)
	}
	return bufs
}

func workerOIDs(prefix string, workers int) []string {
	oids := make([]string, workers)
	for w := range oids {
		oids[w] = prefix + strconv.Itoa(w)
	}
	return oids
}

// iterOID names what iteration i of worker w creates in run: <prefix>-<run>-<w>-<i>.
func iterOID(prefix string, run int64, w, i int) string {
	return prefix + "-" + strconv.FormatInt(run, 10) + "-" + strconv.Itoa(w) + "-" + strconv.Itoa(i)
}
