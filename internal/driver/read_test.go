package driver_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/snappy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/radosclientfakes"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// readStub is the shape of a FakePool's ReadStub.
type readStub = func(context.Context, string, *radosclient.ReadOp, radosclient.OpFlags) (uint64, error)

// readCall is one op a pool ran: its object and the extent of its read
// step, zero for an op without one.
type readCall struct {
	oid    string
	ofs, n uint64
}

// isStat reports that rop is a head op, which carries a stat.
func isStat(rop *radosclient.ReadOp) bool {
	for _, step := range rop.Steps() {
		if _, ok := step.(*radosclient.StatStep); ok {
			return true
		}
	}
	return false
}

// fillReadStep answers a read of data, trimmed at its end as RADOS trims it.
func fillReadStep(st *radosclient.ReadStep, data []byte) {
	start := min(st.Offset, uint64(len(data)))
	chunk := data[start:min(start+st.Length, uint64(len(data)))]
	if st.Buf == nil {
		st.Result.Data, st.Result.N = append([]byte(nil), chunk...), len(chunk)
		return
	}
	st.Result.N = copy(st.Buf, chunk)
	st.Result.Data = st.Buf[:st.Result.N]
}

// fillHead answers op as an object holding data, with attrs among its xattrs
// and cls_version {1, "t"}, and returns its version, 1.
func fillHead(rop *radosclient.ReadOp, data []byte, attrs map[string][]byte) uint64 {
	for _, step := range rop.Steps() {
		switch st := step.(type) {
		case *radosclient.ExecStep:
			st.Result.Set(encode(version.ReadRet{Objv: version.ObjVersion{Ver: 1, Tag: "t"}}), 0)
		case *radosclient.GetXattrsStep:
			st.Result.Xattrs = maps.Clone(attrs)
		case *radosclient.StatStep:
			st.Result.Size, st.Result.ModTime = uint64(len(data)), time.Unix(1700000000, 0)
		case *radosclient.ReadStep:
			fillReadStep(st, data)
		}
	}
	return 1
}

// fillRead answers a read of large.bin's layout with data: the head holds
// its first 4 MiB and the shadow stripe <prefix><i> the 4 MiB from 4 MiB × i.
func fillRead(rop *radosclient.ReadOp, data []byte, oid, prefix, marker string) error {
	stripe := data[:min(4<<20, len(data))]
	if oid != marker+"_large.bin" {
		i, err := strconv.Atoi(strings.TrimPrefix(oid, marker+"__shadow_"+prefix))
		if err != nil {
			return &radosclient.Error{Errno: 2, Op: "read " + oid}
		}
		stripe = data[min(i<<22, len(data)):min((i+1)<<22, len(data))]
	}
	for _, step := range rop.Steps() {
		if st, ok := step.(*radosclient.ReadStep); ok {
			fillReadStep(st, stripe)
		}
	}
	return nil
}

// recordingStub records each op in reads, as readCall describes it, and
// answers it with answer.
func recordingStub(reads *[]readCall, answer func(rop *radosclient.ReadOp)) readStub {
	var mu sync.Mutex
	return func(_ context.Context, oid string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
		call := readCall{oid: oid}
		for _, step := range rop.Steps() {
			if st, ok := step.(*radosclient.ReadStep); ok {
				call.ofs, call.n = st.Offset, st.Length
			}
		}
		mu.Lock()
		*reads = append(*reads, call)
		mu.Unlock()
		answer(rop)
		return 1, nil
	}
}

// recordReads answers the head op as a head of headLen bytes with attrs and
// every data read with zeros, recording each op in reads.
func recordReads(reads *[]readCall, attrs map[string][]byte, headLen int) readStub {
	head := make([]byte, headLen)
	return recordingStub(reads, func(rop *radosclient.ReadOp) {
		if isStat(rop) {
			fillHead(rop, head, attrs)
			return
		}
		for _, step := range rop.Steps() {
			if st, ok := step.(*radosclient.ReadStep); ok {
				fillReadStep(st, make([]byte, st.Offset+st.Length))
			}
		}
	})
}

// recordReadsFrom answers every op as the head object holding head with
// attrs, recording each op in reads.
func recordReadsFrom(reads *[]readCall, head []byte, attrs map[string][]byte) readStub {
	return recordingStub(reads, func(rop *radosclient.ReadOp) { fillHead(rop, head, attrs) })
}

// fakeHeadOnly answers every op as a head-only object holding data, written
// under the idtag tag.
func fakeHeadOnly(data []byte, tag string) readStub {
	attrs := map[string][]byte{meta.AttrETag: []byte("e"), meta.AttrIDTag: []byte(tag)}
	return func(_ context.Context, _ string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
		return fillHead(rop, data, attrs), nil
	}
}

// plaintextCompressible is compressible and seeded, like populate.sh's
// payload_compressible.
func plaintextCompressible(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	out := make([]byte, n)
	words := []string{"alpha ", "beta ", "gamma ", "delta "}
	for i := 0; i < n; {
		i += copy(out[i:], words[r.IntN(len(words))])
	}
	return out
}

// decodeGolden decodes the manifest golden name from meta's testdata and
// moves it into bucket, whose marker names its stripes.
func decodeGolden(name string, bucket meta.BucketID) meta.Manifest {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join("..", "meta", "testdata", "manifests", name+".bin"))
	Expect(err).NotTo(HaveOccurred())
	d := denc.NewDecoder(b)
	m := meta.DecodeManifest(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	m.Obj.Bucket, m.TailPlacement.Bucket = bucket, bucket
	return m
}

// errSinkGone is a client that went away mid-response.
var errSinkGone = errors.New("client went away")

// failingSink takes its first write and answers every later one with fail.
type failingSink struct {
	writes int
	fail   func(p []byte) (int, error)
}

func (f *failingSink) Write(p []byte) (int, error) {
	f.writes++
	if f.writes == 1 {
		return len(p), nil
	}
	return f.fail(p)
}

// blockingSink collects what it is written. On its at'th write it closes
// writing, then waits for resume before taking the bytes.
type blockingSink struct {
	at      int
	writes  int
	writing chan struct{}
	resume  chan struct{}
	buf     bytes.Buffer
}

func (b *blockingSink) Write(p []byte) (int, error) {
	b.writes++
	if b.writes == b.at {
		close(b.writing)
		<-b.resume
	}
	return b.buf.Write(p)
}

// openFake opens a Store over c serving the zone seedRookZone seeds for
// "ceph-objectstore", closed when the spec ends.
func openFake(ctx context.Context, c *fakerados.Cluster) *driver.Store {
	GinkgoHelper()
	s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(s.Close)
	return s
}

var _ = Describe("ReadObject", func() {
	const (
		dataPool = testDataPool
		marker   = "m1"
		prefix   = ".AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_"
	)
	var (
		c     *fakerados.Cluster
		ids   rookZone
		s     *driver.Store
		plain *op.BucketRecord
	)
	payload := func(n int, seed byte) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = seed + byte(i%251)
		}
		return b
	}
	put := func(oid string, data []byte, attrs map[string][]byte) {
		c.Put(dataPool, "", oid, data)
		maps.Copy(c.Object(dataPool, "", oid).Xattrs, attrs)
	}
	// largeManifestOfSize is large.bin's layout for an object of n bytes:
	// a 4 MiB head, then 4 MiB stripes.
	largeManifestOfSize := func(n uint64) meta.Manifest {
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "large.bin"}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = n, 4<<20, 4<<20
		m.Prefix = prefix
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 4 << 20}}
		m.TailPlacement = meta.BucketPlacement{Bucket: plain.Info.Bucket, PlacementRule: plain.Info.PlacementRule}
		m.HeadPlacementRule = plain.Info.PlacementRule
		return m
	}
	// largeManifest is large.bin's: 10 MiB.
	largeManifest := func() meta.Manifest { return largeManifestOfSize(10 << 20) }
	seedLarge := func(data []byte, tag string) {
		attrs := map[string][]byte{meta.AttrETag: []byte("e"), meta.AttrManifest: encode(largeManifest())}
		if tag != "" {
			attrs[meta.AttrIDTag] = []byte(tag)
		}
		put(marker+"_large.bin", data[:4<<20], attrs)
		put(marker+"__shadow_"+prefix+"1", data[4<<20:8<<20], nil)
		put(marker+"__shadow_"+prefix+"2", data[8<<20:], nil)
	}
	readWith := func(ctx context.Context, store *driver.Store, st *op.ObjectState, ofs, n uint64) ([]byte, error) {
		var out bytes.Buffer
		err := store.ReadObject(ctx, st, op.ByteRange{Offset: ofs, Length: n}, &out)
		return out.Bytes(), err
	}
	read := func(ctx context.Context, st *op.ObjectState, ofs, n uint64) ([]byte, error) {
		return readWith(ctx, s, st, ofs, n)
	}
	largeAttrs := func(m meta.Manifest) map[string][]byte {
		return map[string][]byte{meta.AttrETag: []byte("e"), meta.AttrManifest: encode(m)}
	}

	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		ids = seedRookZone(c, "ceph-objectstore", true)
		s = openFake(ctx, c)
		plain = &op.BucketRecord{Info: meta.BucketInfo{
			Bucket:        meta.BucketID{Name: "plain", Marker: marker, ID: marker},
			PlacementRule: meta.ParsePlacementRule("default-placement"),
		}}
	})

	It("reads a head-only object whole and by range", func(ctx SpecContext) {
		data := payload(1024, 1)
		put(marker+"_small.bin", data, map[string][]byte{meta.AttrETag: []byte("e")})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "small.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(ctx, st, 0, 1024)).To(Equal(data))
		Expect(read(ctx, st, 100, 50)).To(Equal(data[100:150]))
		Expect(read(ctx, st, 0, 0)).To(BeEmpty())
	})

	It("reads a head-only object larger than a request in pieces of rgw_get_obj_max_req_size", func(ctx SpecContext) {
		data := payload(9<<20, 2)
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		var reads []readCall
		pool.ReadStub = recordReadsFrom(&reads, data, map[string][]byte{meta.AttrETag: []byte("e")})
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "nine.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(readWith(ctx, s2, st, 1, 9<<20-1)).To(Equal(data[1:]))
		Expect(reads[1:]).To(ConsistOf(
			readCall{marker + "_nine.bin", 1, 4 << 20},
			readCall{marker + "_nine.bin", 4<<20 + 1, 4 << 20},
			readCall{marker + "_nine.bin", 8<<20 + 1, 1<<20 - 1},
		), "iterate_obj's loop without a manifest")
	})

	It("serves a prefetched head without a RADOS read, and an unprefetched one with a guarded read", func(ctx SpecContext) {
		data := payload(3<<20, 2)
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		pool.ReadStub = fakeHeadOnly(data, "tag\x00")
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(readWith(ctx, s2, st, 0, 3<<20)).To(Equal(data))
		Expect(pool.ReadCallCount()).To(Equal(1), "the prefetch op was the only RADOS op")

		st, err = s2.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(readWith(ctx, s2, st, 10, 100)).To(Equal(data[10:110]))
		_, oid, rop, _ := pool.ReadArgsForCall(pool.ReadCallCount() - 1)
		Expect(oid).To(Equal(marker + "_k"))
		steps := rop.Steps()
		Expect(steps).To(HaveLen(2))
		Expect(steps[0]).To(Equal(&radosclient.CmpXattrStep{Name: meta.AttrIDTag, Op: radosclient.CmpEQ, Value: []byte("tag\x00")}),
			"append_atomic_test on a head read")
		Expect(steps[1]).To(And(BeAssignableToTypeOf(&radosclient.ReadStep{}), HaveField("Offset", BeEquivalentTo(10)), HaveField("Length", BeEquivalentTo(100))))
	})

	It("reads a prefetched part past the prefetch with one guarded read for the rest", func(ctx SpecContext) {
		data := payload(5<<20, 3)
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		pool.ReadStub = fakeHeadOnly(data, "tag\x00")
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(HaveLen(4 << 20))
		Expect(readWith(ctx, s2, st, 3<<20, 2<<20)).To(Equal(data[3<<20:]))
		Expect(pool.ReadCallCount()).To(Equal(2))
		_, _, rop, _ := pool.ReadArgsForCall(1)
		steps := rop.Steps()
		Expect(stepTypes(steps)).To(Equal([]string{"*radosclient.CmpXattrStep", "*radosclient.ReadStep"}))
		Expect(steps[1]).To(And(HaveField("Offset", BeEquivalentTo(4<<20)), HaveField("Length", BeEquivalentTo(1<<20))),
			"get_obj_iterate_cb serves the prefetched part and reads from where it ends")
	})

	It("serves a head of exactly rgw_max_chunk_size from the prefetch alone", func(ctx SpecContext) {
		data := payload(4<<20, 4)
		m := largeManifestOfSize(4 << 20)
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		pool.ReadStub = func(_ context.Context, _ string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			return fillHead(rop, data, largeAttrs(m)), nil
		}
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(readWith(ctx, s2, st, 0, 4<<20)).To(Equal(data))
		Expect(pool.ReadCallCount()).To(Equal(1))
	})

	It("reads a tailed object whole from the prefetch plus the tails, in order", func(ctx SpecContext) {
		data := payload(10<<20, 3)
		seedLarge(data, "tag\x00")
		st, err := s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Size).To(BeEquivalentTo(10 << 20))
		Expect(read(ctx, st, 0, 10<<20)).To(Equal(data))
		Expect(c.Reads(dataPool, "", marker+"_large.bin")).To(Equal(1), "the head came from the prefetch")
	})

	DescribeTable("reads ranges across the head/tail boundary and inside tails",
		func(ctx SpecContext, ofs, n uint64) {
			data := payload(10<<20, 4)
			seedLarge(data, "")
			st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(ctx, st, ofs, n)).To(Equal(data[ofs : ofs+n]))
		},
		Entry("the last head byte and the first tail byte", uint64(4<<20-1), uint64(2)),
		Entry("inside the first tail", uint64(5<<20), uint64(1<<20)),
		Entry("the last byte", uint64(10<<20-1), uint64(1)),
		Entry("head through both tails", uint64(4<<20-3), uint64(6<<20+3)),
		Entry("whole object without a prefetch", uint64(0), uint64(10<<20)),
	)

	It("issues iterate_obj's pieces: per stripe, at most rgw_get_obj_max_req_size, with location_ofs", func(ctx SpecContext) {
		// An explicit manifest: head 1 MiB, then a 6 MiB piece stored at loc_ofs 512 in one shadow object.
		m := meta.NewManifest()
		m.ExplicitObjs = true
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "x"}}
		shadow := meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "sh", NS: meta.NSShadow}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = 7<<20, 1<<20, 1<<20
		m.Objs = map[uint64]meta.ManifestPart{0: {Loc: m.Obj, Size: 1 << 20}, 1 << 20: {Loc: shadow, LocOfs: 512, Size: 6 << 20}}
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		var reads []readCall
		pool.ReadStub = recordReads(&reads, largeAttrs(m), 1<<20)
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "x"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s2.ReadObject(ctx, st, op.ByteRange{Offset: 1<<20 - 10, Length: 6<<20 + 10}, io.Discard)).To(Succeed())
		Expect(reads[0]).To(Equal(readCall{oid: marker + "_x"}), "the stat")
		Expect(reads[1:]).To(ConsistOf(
			readCall{marker + "_x", 1<<20 - 10, 10},
			readCall{marker + "__shadow_sh", 512, 4 << 20},
			readCall{marker + "__shadow_sh", 512 + 4<<20, 2 << 20},
		))
	})

	It("reads a multipart object's range across a part boundary from the right stripes", func(ctx SpecContext) {
		m := decodeGolden("squid-multipart", plain.Info.Bucket)
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		var reads []readCall
		pool.ReadStub = recordReads(&reads, largeAttrs(m), 0)
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "multipart.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s2.ReadObject(ctx, st, op.ByteRange{Offset: 13 << 20, Length: 5 << 20}, io.Discard)).To(Succeed())
		const p = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO"
		Expect(reads[1:]).To(ConsistOf(
			readCall{marker + "__shadow_" + p + ".2_1", 1 << 20, 3 << 20},
			readCall{marker + "__multipart_" + p + ".3", 0, 2 << 20},
		))
	})

	It("holds a piece's share of the window until it is written, so reads held behind a slow one start no more", func(ctx SpecContext) {
		// 28 MiB: a prefetched 4 MiB head and six 4 MiB tails under a 16 MiB
		// window. Tails 1-4 wait for the spec; 5 and 6 answer at once. Tails
		// 2-4 finish their reads before tail 1, and the bytes still come out
		// in offset order.
		var started, inflight, peak atomic.Int32
		stripe := func(n string) string { return marker + "__shadow_" + prefix + n }
		release := map[string]chan struct{}{}
		for _, n := range []string{"1", "2", "3", "4"} {
			release[stripe(n)] = make(chan struct{})
		}
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		m := largeManifestOfSize(28 << 20)
		data := payload(28<<20, 5)
		pool.ReadStub = func(_ context.Context, oid string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			if isStat(rop) {
				return fillHead(rop, data[:4<<20], largeAttrs(m)), nil
			}
			started.Add(1)
			n := inflight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			defer inflight.Add(-1)
			if ch, ok := release[oid]; ok {
				<-ch
			}
			return 1, fillRead(rop, data, oid, prefix, marker)
		}
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- s2.ReadObject(ctx, st, op.ByteRange{Offset: 0, Length: 28 << 20}, &out) }()
		Eventually(started.Load).Should(BeEquivalentTo(4), "16 MiB window / 4 MiB pieces")
		for _, n := range []string{"2", "3", "4"} {
			close(release[stripe(n)])
		}
		Eventually(inflight.Load).Should(BeEquivalentTo(1), "tails 2-4 are read and wait to be written behind tail 1")
		Consistently(started.Load).Should(BeEquivalentTo(4),
			"no fifth read starts while tails 2-4 hold the window, where radosgw's throttle would start three more")
		close(release[stripe("1")])
		Eventually(done).Should(Receive(Succeed()))
		Expect(out.Bytes()).To(Equal(data))
		Expect(started.Load()).To(BeEquivalentTo(6))
		Expect(peak.Load()).To(BeNumerically("<=", 4))
		Expect(s2.PooledBuffersForTest()).To(BeNumerically(">", 0), "buffers of reads that ended went back to the pool")
	})

	It("holds a piece's share of the window while the sink takes its bytes, so a slow client starts no more reads", func(ctx SpecContext) {
		// 28 MiB: a prefetched 4 MiB head and six 4 MiB tails under a 16 MiB
		// window. Every read answers at once; the sink takes the head, then
		// blocks on tail 1.
		var started atomic.Int32
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		m := largeManifestOfSize(28 << 20)
		data := payload(28<<20, 15)
		pool.ReadStub = func(_ context.Context, oid string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			if isStat(rop) {
				return fillHead(rop, data[:4<<20], largeAttrs(m)), nil
			}
			started.Add(1)
			return 1, fillRead(rop, data, oid, prefix, marker)
		}
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		sink := &blockingSink{at: 2, writing: make(chan struct{}), resume: make(chan struct{})}
		done := make(chan error, 1)
		go func() { done <- s2.ReadObject(ctx, st, op.ByteRange{Length: 28 << 20}, sink) }()
		Eventually(sink.writing).Should(BeClosed(), "the sink took the head and is taking tail 1")
		Eventually(started.Load).Should(BeEquivalentTo(4), "tails 1-4 fill the window")
		Consistently(started.Load).Should(BeEquivalentTo(4),
			"no fifth read starts while tail 1 is being written: it holds its share, and tails 2-4, read, hold theirs")
		close(sink.resume)
		Eventually(done).Should(Receive(Succeed()))
		Expect(sink.buf.Bytes()).To(Equal(data))
		Expect(started.Load()).To(BeEquivalentTo(6))
	})

	It("counts a pooled buffer against the window, not the bytes its piece asks for", func(ctx SpecContext) {
		// Stripes of 2 MiB+1 each take a pooled 4 MiB buffer: four fit the
		// 16 MiB window, where their lengths alone would let seven in.
		const stripeLen = 2<<20 + 1
		m := largeManifestOfSize(4<<20 + 8*stripeLen)
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: stripeLen}}
		var started atomic.Int32
		block := make(chan struct{})
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		pool.ReadStub = func(ctx context.Context, _ string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			if isStat(rop) {
				return fillHead(rop, make([]byte, 4<<20), largeAttrs(m)), nil
			}
			started.Add(1)
			select {
			case <-block:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
			for _, step := range rop.Steps() {
				if st, ok := step.(*radosclient.ReadStep); ok {
					fillReadStep(st, make([]byte, st.Offset+st.Length))
				}
			}
			return 1, nil
		}
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		done := make(chan error, 1)
		go func() {
			done <- s2.ReadObject(ctx, st, op.ByteRange{Offset: 4 << 20, Length: 8 * stripeLen}, io.Discard)
		}()
		Eventually(started.Load).Should(BeEquivalentTo(4))
		Consistently(started.Load).Should(BeEquivalentTo(4), "four 4 MiB buffers fill the window")
		close(block)
		Eventually(done).Should(Receive(Succeed()))
		Expect(started.Load()).To(BeEquivalentTo(8))
	})

	DescribeTable("stops on a sink that fails mid-stream: it cancels the reads in flight, drops their buffers and waits for them",
		func(ctx SpecContext, fail func(p []byte) (int, error), want error) {
			// 20 MiB: the prefetched head, then four tails, all four in flight
			// before tail 1 answers; the sink takes the head and fails on tail 1.
			var started, active, canceled atomic.Int32
			allStarted := make(chan struct{})
			pool := &radosclientfakes.FakePool{}
			pool.WithLocatorReturns(pool)
			m := largeManifestOfSize(20 << 20)
			data := payload(20<<20, 14)
			pool.ReadStub = func(ctx context.Context, oid string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
				if isStat(rop) {
					return fillHead(rop, data[:4<<20], largeAttrs(m)), nil
				}
				active.Add(1)
				defer active.Add(-1)
				if started.Add(1) == 4 {
					close(allStarted)
				}
				if oid == marker+"__shadow_"+prefix+"1" {
					<-allStarted
					return 1, fillRead(rop, data, oid, prefix, marker)
				}
				<-ctx.Done()
				canceled.Add(1)
				return 0, ctx.Err()
			}
			s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
			st, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
			Expect(err).NotTo(HaveOccurred())
			sink := &failingSink{fail: fail}
			err = s2.ReadObject(ctx, st, op.ByteRange{Length: 20 << 20}, sink)
			Expect(err).To(MatchError(want))
			Expect(sink.writes).To(Equal(2), "the head, then tail 1")
			Expect(active.Load()).To(BeZero(), "every read had returned before ReadObject did")
			Expect(canceled.Load()).To(BeEquivalentTo(3), "the failure canceled tails 2-4")
			Expect(s2.PooledBuffersForTest()).To(BeEquivalentTo(1), "tail 1's buffer went back to the pool; the abandoned reads' did not")
		},
		Entry("an error", func([]byte) (int, error) { return 0, errSinkGone }, errSinkGone),
		Entry("a short write", func(p []byte) (int, error) { return len(p) / 2, nil }, io.ErrShortWrite),
	)

	It("stops at the first failed tail, drains the rest, and maps the error", func(ctx SpecContext) {
		data := payload(10<<20, 6)
		seedLarge(data, "")
		c.Remove(dataPool, "", marker+"__shadow_"+prefix+"2") // ENOENT on the third stripe
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		out, err := read(ctx, st, 0, 10<<20)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(out).To(Equal(data[:8<<20]), "the first two stripes were written, nothing after")
	})

	It("writes a stripe shorter than its manifest says, then fails with InternalError", func(ctx SpecContext) {
		data := payload(10<<20, 6)
		seedLarge(data, "")
		put(marker+"__shadow_"+prefix+"2", data[8<<20:9<<20], nil)
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		out, err := read(ctx, st, 0, 10<<20)
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
		Expect(out).To(Equal(data[:9<<20]), "get_obj_data::flush hands on the short read and stops there")
	})

	It("answers ConcurrentModification when the head's idtag changed under it", func(ctx SpecContext) {
		data := payload(2<<20, 7)
		put(marker+"_k", data, map[string][]byte{meta.AttrETag: []byte("e"), meta.AttrIDTag: []byte("old\x00")})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		c.Object(dataPool, "", marker+"_k").Xattrs[meta.AttrIDTag] = []byte("new\x00")
		_, err = read(ctx, st, 0, 2<<20)
		Expect(err).To(MatchError(op.ErrConcurrentModification))
	})

	It("returns the context error and drops the buffers of abandoned reads", func(ctx SpecContext) {
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		m := largeManifestOfSize(20 << 20)
		block := make(chan struct{})
		pool.ReadStub = func(ctx context.Context, _ string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			if isStat(rop) {
				return fillHead(rop, make([]byte, 4<<20), largeAttrs(m)), nil
			}
			select {
			case <-block:
				return 1, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- s2.ReadObject(rctx, st, op.ByteRange{Offset: 4 << 20, Length: 16 << 20}, io.Discard) }()
		Eventually(pool.ReadCallCount).Should(BeNumerically(">=", 2))
		cancel()
		Eventually(done).Should(Receive(MatchError(context.Canceled)))
		Expect(s2.PooledBuffersForTest()).To(BeZero(), "no buffer of an abandoned read went back to the pool")
		close(block)
	})

	It("takes a stripe for the head only when it is the head's RADOS object, as iterate_obj compares raw objects", func(ctx SpecContext) {
		// An explicit manifest's pieces carry no placement rule, so radosgw
		// places even the piece that names the head by the zonegroup's
		// default placement. In a bucket of another storage class that is
		// another pool, where the piece is read, unguarded, though the
		// prefetch holds it.
		zone := testZone()
		zone.Params.PlacementPools["default-placement"].StorageClasses["COLD"] = meta.ZoneStorageClass{DataPool: new(meta.ParsePool("cold.data"))}
		cold := &op.BucketRecord{Info: meta.BucketInfo{Bucket: plain.Info.Bucket, PlacementRule: meta.ParsePlacementRule("default-placement/COLD")}}
		m := meta.NewManifest()
		m.ExplicitObjs = true
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "old.bin"}}
		m.ObjSize, m.HeadSize = 1024, 1024
		m.Objs = map[uint64]meta.ManifestPart{0: {Loc: m.Obj, Size: 1024}}
		data := payload(1024, 9)
		attrs := largeAttrs(m)
		attrs[meta.AttrIDTag] = []byte("tag\x00")
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		pool.ReadStub = func(_ context.Context, _ string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			return fillHead(rop, data, attrs), nil
		}
		cluster := fakeClusterWith(pool)
		s2 := driver.NewStoreForTest(cluster, zone, testReadConfig())

		st, err := s2.PrefetchObject(ctx, cold, meta.ObjKey{Name: "old.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(Equal(data))
		Expect(readWith(ctx, s2, st, 0, 1024)).To(Equal(data))
		Expect(pool.ReadCallCount()).To(Equal(2), "the piece went to RADOS")
		_, oid, rop, _ := pool.ReadArgsForCall(1)
		Expect(oid).To(Equal(marker + "_old.bin"))
		Expect(stepTypes(rop.Steps())).To(Equal([]string{"*radosclient.ReadStep"}), "without the idtag guard")
		Expect(cluster.PoolCallCount()).To(Equal(2))
		_, name, _ := cluster.PoolArgsForCall(0)
		Expect(name).To(Equal("cold.data"), "the head")
		_, name, _ = cluster.PoolArgsForCall(1)
		Expect(name).To(Equal(testDataPool), "the piece")

		st, err = s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "old.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(readWith(ctx, s2, st, 0, 1024)).To(Equal(data))
		Expect(pool.ReadCallCount()).To(Equal(3), "in the default placement the piece is the head, served from the prefetch")
	})

	It("fails a read that needs a data pool that does not exist, naming the pool", func(ctx SpecContext) {
		// radosgw creates the pool there (rgw_get_rados_ref opens it with
		// create set) and then finds no stripe in it, which is NoSuchKey.
		editRoot(c, meta.ZoneInfoOID(ids.zoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			z.PlacementPools["default-placement"].StorageClasses["COLD"] = meta.ZoneStorageClass{DataPool: new(meta.ParsePool("cold.data"))}
		})
		c.FailPool("cold.data")
		s2 := openFake(ctx, c)
		data := payload(10<<20, 8)
		m := largeManifest()
		m.TailPlacement.PlacementRule = meta.ParsePlacementRule("default-placement/COLD")
		put(marker+"_large.bin", data[:4<<20], largeAttrs(m))
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		out, err := readWith(ctx, s2, st, 0, 10<<20)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(err).To(MatchError(ContainSubstring("cold.data")))
		Expect(out).To(Equal(data[:4<<20]))
	})

	It("refuses a manifest whose walk stops moving with UnknownError, where radosgw's never ends", func(ctx SpecContext) {
		data := payload(10<<20, 10)
		m := largeManifest()
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 0}}
		put(marker+"_large.bin", data[:4<<20], largeAttrs(m))
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		out, err := read(ctx, st, 0, 10<<20)
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(err).To(MatchError(denc.ErrMalformed))
		Expect(out).To(Equal(data[:4<<20]))
	})

	It("refuses a manifest that ends before the range, with UnknownError", func(ctx SpecContext) {
		// An explicit manifest whose only piece is the 1 KiB head of a
		// 2 KiB object.
		m := meta.NewManifest()
		m.ExplicitObjs = true
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "short.bin"}}
		m.ObjSize = 2048
		m.Objs = map[uint64]meta.ManifestPart{0: {Loc: m.Obj, Size: 1024}}
		data := payload(1024, 11)
		put(marker+"_short.bin", data, largeAttrs(m))
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "short.bin"})
		Expect(err).NotTo(HaveOccurred())
		out, err := read(ctx, st, 0, 2048)
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(out).To(Equal(data))
	})

	It("refuses before reading a range whose stripes pass meta.MaxWalkStripes", func(ctx SpecContext) {
		m := largeManifestOfSize(4<<20 + meta.MaxWalkStripes + 1)
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 1}}
		put(marker+"_large.bin", payload(4<<20, 12), largeAttrs(m))
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		out, err := read(ctx, st, 0, st.Size)
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(err).To(MatchError(meta.ErrTooManyStripes))
		Expect(out).To(BeEmpty())
		Expect(c.Reads(dataPool, "", marker+"_large.bin")).To(Equal(1), "the stat alone")
	})

	It("refuses a range past the object with InternalError", func(ctx SpecContext) {
		put(marker+"_small.bin", payload(10, 13), nil)
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "small.bin"})
		Expect(err).NotTo(HaveOccurred())
		_, err = read(ctx, st, 5, 6)
		Expect(err).To(MatchError(op.ErrInternalError))
	})

	Describe("a compressed object", func() {
		// 10 MiB plaintext, snappy per 4 MiB block (RGWPutObj_Compress), the compressed bytes laid out as
		// a head of half of them plus one tail, so the second block straddles the head/tail boundary.
		var (
			plain10 []byte
			comp    []byte
			ci      meta.CompressionInfo
		)
		BeforeEach(func() {
			plain10 = plaintextCompressible(10 << 20)
			ci = meta.NewCompressionInfo()
			ci.Type, ci.OrigSize = compression.Snappy, 10<<20
			comp = nil
			for ofs := 0; ofs < len(plain10); ofs += 4 << 20 {
				b := snappy.Encode(nil, plain10[ofs:min(ofs+4<<20, len(plain10))])
				ci.Blocks = append(ci.Blocks, meta.CompressionBlock{OldOfs: uint64(ofs), NewOfs: uint64(len(comp)), Len: uint64(len(b))})
				comp = append(comp, b...)
			}
		})
		compAttrs := func(m meta.Manifest) map[string][]byte {
			attrs := largeAttrs(m)
			attrs[meta.AttrCompression] = encode(ci)
			return attrs
		}
		seedCompressed := func(ctx context.Context, headLen int) *op.ObjectState {
			GinkgoHelper()
			m := largeManifestOfSize(uint64(len(comp)))
			m.HeadSize, m.MaxHeadSize = uint64(headLen), uint64(headLen)
			m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: uint64(headLen), StripeMaxSize: 4 << 20}}
			put(marker+"_large.bin", comp[:headLen], compAttrs(m))
			for i, ofs := 1, headLen; ofs < len(comp); i, ofs = i+1, ofs+4<<20 {
				put(marker+"__shadow_"+prefix+strconv.Itoa(i), comp[ofs:min(ofs+4<<20, len(comp))], nil)
			}
			st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Compression).NotTo(BeNil())
			Expect(ci.Blocks[1].NewOfs).To(BeNumerically("<", headLen), "the second block starts in the head")
			Expect(ci.Blocks[1].NewOfs+ci.Blocks[1].Len).To(BeNumerically(">", headLen), "and ends in the tail")
			return st
		}

		It("decodes the whole object", func(ctx SpecContext) {
			st := seedCompressed(ctx, len(comp)/2)
			Expect(read(ctx, st, 0, 10<<20)).To(Equal(plain10))
		})

		DescribeTable("decodes a range, reading only the blocks it needs",
			func(ctx SpecContext, ofs, n uint64) {
				st := seedCompressed(ctx, len(comp)/2)
				got, err := read(ctx, st, ofs, n)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(plain10[ofs : ofs+n]))
			},
			Entry("inside the first block", uint64(10), uint64(100)),
			Entry("across two blocks", uint64(4<<20-5), uint64(10)),
			Entry("the last bytes", uint64(10<<20-7), uint64(7)),
			Entry("all of the second block exactly", uint64(4<<20), uint64(4<<20)),
		)

		It("reads exactly the compressed range fixup_range selects", func(ctx SpecContext) {
			pool := &radosclientfakes.FakePool{}
			pool.WithLocatorReturns(pool)
			var reads []readCall
			m := largeManifestOfSize(uint64(len(comp)))
			Expect(len(comp)).To(BeNumerically("<", 4<<20), "everything is in the head")
			pool.ReadStub = recordReadsFrom(&reads, comp, compAttrs(m))
			s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
			st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
			Expect(err).NotTo(HaveOccurred())
			Expect(readWith(ctx, s2, st, 5<<20, 1<<20)).To(Equal(plain10[5<<20 : 6<<20]))
			b := ci.Blocks[1]
			Expect(reads[1:]).To(Equal([]readCall{{marker + "_large.bin", b.NewOfs, b.Len}}))
		})

		It("decodes a range that starts in a later part of a compressed multipart object", func(ctx SpecContext) {
			// Each 4 MiB of plaintext is a part, compressed as one block and
			// stored as the part's one stripe; parts of different sizes take
			// a rule each, as RGWObjManifest::append lays them out.
			const mp = "mp.bin.2~abc"
			m := meta.NewManifest()
			m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "mp.bin"}}
			m.Prefix = mp
			m.TailPlacement = meta.BucketPlacement{Bucket: plain.Info.Bucket, PlacementRule: plain.Info.PlacementRule}
			m.Rules = map[uint64]meta.ManifestRule{}
			for i, b := range ci.Blocks {
				m.Rules[b.NewOfs] = meta.ManifestRule{StartPartNum: uint32(i + 1), StartOfs: b.NewOfs, PartSize: b.Len, StripeMaxSize: 4 << 20}
				put(marker+"__multipart_"+mp+"."+strconv.Itoa(i+1), comp[b.NewOfs:b.NewOfs+b.Len], nil)
			}
			m.ObjSize = uint64(len(comp))
			put(marker+"_mp.bin", nil, compAttrs(m))
			st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "mp.bin"})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(ctx, st, 5<<20, 4<<20)).To(Equal(plain10[5<<20 : 9<<20]))
			Expect(c.Reads(dataPool, "", marker+"__multipart_"+mp+".1")).To(BeZero(), "the first part holds no block of the range")
			Expect(c.Reads(dataPool, "", marker+"_mp.bin")).To(Equal(1), "the stat alone: a multipart head holds no data")
		})

		It("answers a block its codec refuses as radosgw does with that codec's DecodeError, after the blocks before it", func(ctx SpecContext) {
			st := seedCompressed(ctx, len(comp)/2)
			// Past its length header, block 1's head bytes become copies from
			// offsets past what it has decoded, which RawUncompress refuses.
			obj := c.Object(dataPool, "", marker+"_large.bin")
			for i := ci.Blocks[1].NewOfs + 4; i < uint64(len(obj.Data)); i++ {
				obj.Data[i] = 0xff
			}
			out, err := read(ctx, st, 0, 10<<20)
			de, ok := errors.AsType[*compression.DecodeError](err)
			Expect(ok).To(BeTrue(), "a DecodeError: %v", err)
			Expect(de.Ret).To(Equal(-2), "radosgw's 404 NoSuchKey before the body")
			Expect(err).NotTo(MatchError(op.ErrUnknown), "the handler renders it")
			Expect(out).To(Equal(plain10[:4<<20]))
		})

		It("answers a block that decodes to other than its block map's length with UnknownError", func(ctx SpecContext) {
			st := seedCompressed(ctx, len(comp)/2)
			st.Compression.Blocks[2].OldOfs--
			out, err := read(ctx, st, 0, 10<<20)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(err).To(MatchError(compression.ErrCorrupt))
			Expect(errors.As(err, new(*compression.DecodeError))).To(BeFalse(), "radosgw's decompress does not refuse it")
			Expect(out).To(Equal(plain10[:4<<20]))
		})

		DescribeTable("refuses blocks whose new_ofs go backward with UnknownError before reading them",
			func(ctx SpecContext, newOfs func() uint64) {
				st := seedCompressed(ctx, len(comp)/2)
				st.Compression.Blocks[2].NewOfs = newOfs()
				out, err := read(ctx, st, 5<<20, 4<<20)
				Expect(err).To(MatchError(op.ErrUnknown))
				Expect(err).To(MatchError(compression.ErrCorrupt))
				Expect(out).To(BeEmpty())
				Expect(c.Reads(dataPool, "", marker+"_large.bin")).To(Equal(1), "the stat alone")
			},
			Entry("to the start", func() uint64 { return 0 }),
			Entry("to end one byte before they start", func() uint64 { return ci.Blocks[1].NewOfs - ci.Blocks[2].Len }),
		)

		It("refuses a codec it does not know with UnknownError, radosgw's -EIO", func(ctx SpecContext) {
			st := seedCompressed(ctx, len(comp)/2)
			st.Compression.Type = "brotli"
			out, err := read(ctx, st, 0, 10<<20)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(err).To(MatchError(compression.ErrUnknownCodec))
			Expect(out).To(BeEmpty())
		})

		It("refuses a block map that reaches past the stored bytes with UnknownError", func(ctx SpecContext) {
			st := seedCompressed(ctx, len(comp)/2)
			st.Compression.Blocks[2].Len++
			_, err := read(ctx, st, 9<<20, 1)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(err).To(MatchError(compression.ErrCorrupt))
		})

		It("refuses a range past orig_size with InternalError", func(ctx SpecContext) {
			st := seedCompressed(ctx, len(comp)/2)
			_, err := read(ctx, st, 10<<20-1, 2)
			Expect(err).To(MatchError(op.ErrInternalError))
		})

		It("serves a compressed object of type none undecoded, as radosgw does", func(ctx SpecContext) {
			st := seedCompressed(ctx, len(comp)/2)
			st.Compression.Type = compression.None
			Expect(read(ctx, st, 0, uint64(len(comp)))).To(Equal(comp))
		})

		It("hands a raw reader the stored bytes, undecoded, across the head and the tail", func(ctx SpecContext) {
			st := seedCompressed(ctx, len(comp)/2)
			var out bytes.Buffer
			Expect(s.ReadStoredForTest(ctx, st, 0, st.Size, &out)).To(Succeed())
			Expect(out.Bytes()).To(Equal(comp), "Read::read returns what the stripes hold (rgw_rados.cc:7224-7343)")
			out.Reset()
			Expect(s.ReadStoredForTest(ctx, st, 0, 0, &out)).To(Succeed())
			Expect(out.Len()).To(BeZero())
		})
	})
})
