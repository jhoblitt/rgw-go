package driver_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// faultCluster is a fakerados cluster whose pools fail the write ops that
// call a cls method a spec names, or run one and then answer an error, as
// an op lands whose reply never reaches its caller.
type faultCluster struct {
	*fakerados.Cluster
	mu     sync.Mutex
	faults map[string]*methodFault
}

// methodFault is how the write ops calling one method fail: the calls left,
// -1 for every one, and errno. With land set the op runs first; with cancel
// set too, cancel then ends its caller's context, whose error the call
// answers in errno's place.
type methodFault struct {
	left   int
	errno  syscall.Errno
	land   bool
	cancel context.CancelFunc
}

// fail makes the next n write ops calling method, every one for -1, fail
// with errno, running nothing.
func (c *faultCluster) fail(method string, n int, errno syscall.Errno) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.faults[method] = &methodFault{left: n, errno: errno}
}

// landThenCancel makes the next write op calling method run and then end
// its caller's context through cancel, answering that context's error.
func (c *faultCluster) landThenCancel(method string, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.faults[method] = &methodFault{left: 1, land: true, cancel: cancel}
}

// landThenFail makes the next write op calling method run and then answer
// errno, as an op that lands and times out does.
func (c *faultCluster) landThenFail(method string, errno syscall.Errno) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.faults[method] = &methodFault{left: 1, land: true, errno: errno}
}

// take spends the fault of the first method op calls that has one left.
func (c *faultCluster) take(op *radosclient.WriteOp) *methodFault {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, st := range op.Steps() {
		x, ok := st.(*radosclient.ExecStep)
		if !ok {
			continue
		}
		if f := c.faults[x.Method]; f != nil && f.left != 0 {
			if f.left > 0 {
				f.left--
			}
			return f
		}
	}
	return nil
}

func (c *faultCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return &faultPool{Pool: p, c: c}, nil
}

type faultPool struct {
	radosclient.Pool
	c *faultCluster
}

func (p *faultPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	f := p.c.take(op)
	switch {
	case f == nil:
		return p.Pool.Write(ctx, oid, op, flags)
	case f.land:
		if _, err := p.Pool.Write(ctx, oid, op, flags); err != nil {
			return 0, err
		}
		if f.cancel != nil {
			f.cancel()
			return 0, ctx.Err()
		}
	}
	return 0, &radosclient.Error{Errno: int32(f.errno), Op: "write " + oid}
}

// splitTail and errSplitRefusal are the unfinished tail and the refusal the
// splitOLHTail specs join.
var (
	splitTail       = driver.OLHTail{Last: 4, OpTag: "own-op"}
	errSplitRefusal = fmt.Errorf("replaying the olh log of plain/k: removing version %q: %w", "v1", driver.ErrRemovalRefused)
)

// A racePoint is a boundary of rgw-go's write in the race specs: just before
// or after its n-th write op on an object, the shard, its version's head or
// the OLH head.
type racePoint struct {
	object string
	n      int
	before bool
}

const raceInstanceHead, raceOLHHead, raceShard = "instance", "olh", "shard"

// goRaceEntries are the boundaries of rgw-go's write, its prepare, its
// version's head, its completion, its OLH head read, its init, its link, its
// log read and its apply, each with the version the class links last when
// radosgw's whole write runs there, on both releases.
func goRaceEntries() []TableEntry {
	points := []struct {
		name    string
		p       racePoint
		current string
	}{
		{"after its prepare", racePoint{object: raceShard, n: 1}, "go"},
		{"after its head write", racePoint{object: raceInstanceHead, n: 1}, "go"},
		{"after its completion", racePoint{object: raceShard, n: 2}, "go"},
		{"between its head read and its init", racePoint{object: raceOLHHead, n: 1, before: true}, "go"},
		{"after its init", racePoint{object: raceOLHHead, n: 1}, "go"},
		{"after its link", racePoint{object: raceShard, n: 3}, "rgw"},
		{"between its log read and its apply", racePoint{object: raceOLHHead, n: 2, before: true}, "rgw"},
		{"after its apply", racePoint{object: raceOLHHead, n: 2}, "rgw"},
	}
	var out []TableEntry
	for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
		for _, p := range points {
			out = append(out, Entry(fmt.Sprintf("%s, on %s", p.name, r), r, p.p, p.current))
		}
	}
	return out
}

// rgwRaceEntries are the gaps between the radosgw-shaped writer's steps, the
// gap before step i at index i, each with the version the class links last
// when rgw-go's whole write runs there, on both releases.
func rgwRaceEntries() []TableEntry {
	names := []string{
		"before its prepare", "after its prepare", "after its head write", "after its completion",
		"between its head read and its init", "after its init", "after its link",
		"between its log read and its apply", "after its apply", "after its trim",
	}
	const afterLink = 6
	var out []TableEntry
	for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
		for i, name := range names {
			current := "rgw"
			if i >= afterLink {
				current = "go"
			}
			out = append(out, Entry(fmt.Sprintf("%s, on %s", name, r), r, i, current))
		}
	}
	return out
}

// olhVerGuard is the olh.ver an apply's head op w compares, its second step.
func olhVerGuard(w fakerados.RecordedWrite) string {
	GinkgoHelper()
	steps := w.Steps()
	Expect(len(steps)).To(BeNumerically(">", 1))
	x, ok := steps[1].(*radosclient.CmpXattrStep)
	Expect(ok).To(BeTrue(), "step 1 is %T", steps[1])
	Expect(x.Name).To(Equal(meta.AttrOLHVer))
	return string(x.Value)
}

var _ = Describe("the versioned link and unlink", func() {
	var (
		c     *fakerados.Cluster
		fc    *faultCluster
		s     *driver.Store
		rec   *op.BucketRecord
		clock atomic.Int64
		shard string
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	open := func(ctx context.Context, r denc.Release) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, fc, conf(nil), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
	}
	BeforeEach(func(ctx SpecContext) {
		clock.Store(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).UnixNano())
		c = newPutCluster()
		c.SetClock(now)
		fc = &faultCluster{Cluster: c, faults: map[string]*methodFault{}}
		open(ctx, denc.Squid)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
	})
	AfterEach(func() { settle(s) })

	// write is rgw-go's versioned write of version instance up to its link,
	// as its versioned PutObject sends it: the prepare, the instance head and
	// the awaited ADD completion.
	write := func(ctx context.Context, instance string) meta.ObjKey {
		GinkgoHelper()
		v := version(instance)
		x := s.NewIndexOpForTest(rec, v, "go-tag-"+instance)
		x.SetBILogFlagsForTest(rgwcls.BILogFlagVersionedOp)
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		ver := writeInstanceHead(ctx, c, v)
		Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: ver}, versionMeta(v))).To(Succeed())
		return v
	}
	put := func(ctx context.Context, instance string) {
		GinkgoHelper()
		Expect(s.SetOLHForTest(ctx, rec, write(ctx, instance), driver.OLHLink{})).To(Succeed())
	}
	unlink := func(ctx context.Context, instance string, p driver.OLHUnlink) error {
		return s.UnlinkObjInstanceForTest(ctx, rec, version(instance), p)
	}
	readHead := func(ctx context.Context) *op.ObjectState {
		GinkgoHelper()
		h, err := s.ReadOLHHeadForTest(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		return h
	}
	update := func(ctx context.Context) error { return s.UpdateOLHForTest(ctx, rec, readHead(ctx)) }

	olhHead := func() *fakerados.Object { return c.Object(testDataPool, "", olhHeadOID(version(""))) }
	versionHead := func(instance string) *fakerados.Object {
		return c.Object(testDataPool, "", instanceHeadOID(version(instance)))
	}
	pending := func() []string {
		var out []string
		if o := olhHead(); o != nil {
			for name := range o.Xattrs {
				if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
					out = append(out, name)
				}
			}
		}
		slices.Sort(out)
		return out
	}
	// target is the instance the OLH head's olh.info names.
	target := func() string {
		GinkgoHelper()
		o := olhHead()
		Expect(o).NotTo(BeNil(), "the OLH head")
		b, ok := o.Xattrs[meta.AttrOLHInfo]
		Expect(ok).To(BeTrue(), "the OLH head's olh.info")
		d := denc.NewDecoder(b)
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info.Target.Key.Instance
	}
	olhVer := func() string { return string(olhHead().Xattrs[meta.AttrOLHVer]) }
	olhEntry := func(oid string) rgwcls.OLHEntry {
		GinkgoHelper()
		e, ok := c.OLHEntry(rookIndexPool, "", oid, "k")
		Expect(ok).To(BeTrue(), "the OLH entry of k on %s", oid)
		return e
	}
	listed := func(oid string) []string {
		var out []string
		for _, en := range c.ListEntries(rookIndexPool, "", oid, "k") {
			out = append(out, en.Key.Instance)
		}
		return out
	}
	hasInstanceEntry := func(oid, instance string) bool {
		_, ok := c.InstanceEntry(rookIndexPool, "", oid, rgwcls.ObjKey{Name: "k", Instance: instance})
		return ok
	}
	// settled checks what every writer of k must leave on index shard oid:
	// each of versions with its head, instance entry and list entry and no
	// other index entry but the version marker and the OLH entry; the list
	// newest epoch first, current first and alone flagged current; the OLH
	// head's olh.info on current and olh.ver its epoch, with no pending
	// attr; and the OLH entry on current with its log trimmed.
	settled := func(oid, current string, versions ...string) {
		GinkgoHelper()
		settle(s)
		for _, v := range versions {
			Expect(versionHead(v)).NotTo(BeNil(), "the head of version %q", v)
			Expect(hasInstanceEntry(oid, v)).To(BeTrue(), "the instance entry of version %q", v)
		}
		Expect(c.Object(rookIndexPool, "", oid).Omap).To(HaveLen(2+2*len(versions)), "no index entry of k but the marker, the OLH entry and each version's two")
		list := c.ListEntries(rookIndexPool, "", oid, "k")
		Expect(listed(oid)).To(ConsistOf(versions))
		Expect(list[0].Key.Instance).To(Equal(current), "the current version lists first")
		for i, en := range list {
			Expect(en.Flags&rgwcls.FlagCurrent).To(Equal(map[bool]uint16{true: rgwcls.FlagCurrent}[i == 0]), "the current flag of version %q", en.Key.Instance)
			if i > 0 {
				Expect(en.VersionedEpoch).To(BeNumerically("<", list[i-1].VersionedEpoch), "version %q lists after a newer one", en.Key.Instance)
			}
		}
		Expect(target()).To(Equal(current), "olh.info")
		Expect(pending()).To(BeEmpty(), "no pending attr")
		e := olhEntry(oid)
		Expect(olhVer()).To(Equal(strconv.FormatUint(e.Epoch, 10)), "olh.ver is the OLH entry's epoch, the last the class logged")
		Expect(e.Key.Instance).To(Equal(current), "the OLH entry's current version")
		Expect(e.PendingLog).To(BeEmpty(), "the log is trimmed")
	}
	// linkedLast checks, after settled, that the current version is the one
	// the class linked last: its epoch is olh.ver.
	linkedLast := func(oid string) {
		GinkgoHelper()
		list := c.ListEntries(rookIndexPool, "", oid, "k")
		Expect(olhVer()).To(Equal(strconv.FormatUint(list[0].VersionedEpoch, 10)), "olh.ver is the current version's epoch")
	}
	// commitReshard moves rec to its next index generation now, k's new
	// shard taking the old shard's entries and the old shard removed, as a
	// reshard's commit leaves it, and returns k's new shard.
	commitReshard := func() string {
		next := resharded(rec)
		seedShards(c, next)
		nextOID := indexShardOID(next, "k")
		maps.Copy(c.Object(rookIndexPool, "", nextOID).Omap, c.Object(rookIndexPool, "", shard).Omap)
		seedRecordInstance(c, next)
		c.Remove(rookIndexPool, "", shard)
		return nextOID
	}

	Describe("radosgw and rgw-go write versions of one key at once", func() {
		// at arms fn to run once, when rgw-go's write reaches p.
		at := func(p racePoint, fn func()) {
			oid := map[string]string{raceInstanceHead: instanceHeadOID(version("go")), raceOLHHead: olhHeadOID(version("")), raceShard: shard}[p.object]
			pool := testDataPool
			if p.object == raceShard {
				pool = rookIndexPool
			}
			seen := 0
			if p.before {
				c.BeforeWrite(pool, "", oid, func(*fakerados.Object) {
					if seen++; seen == p.n {
						c.BeforeWrite(pool, "", oid, nil)
						fn()
					}
				})
				return
			}
			c.AfterWrite(pool, "", oid, func() {
				if seen++; seen == p.n {
					c.AfterWrite(pool, "", oid, nil)
					fn()
				}
			})
		}
		goWrite := func(ctx context.Context) {
			GinkgoHelper()
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "go"), driver.OLHLink{})).To(Succeed())
		}
		DescribeTable("radosgw's whole write at a boundary of rgw-go's leaves both versions, the one linked last current",
			func(ctx SpecContext, r denc.Release, p racePoint, current string) {
				open(ctx, r)
				rw := newRGWWriter(c, s, rec, r, "rgw")
				at(p, func() { rw.Run(ctx) })
				goWrite(ctx)
				settled(shard, current, "go", "rgw")
				linkedLast(shard)
			},
			goRaceEntries(),
		)

		DescribeTable("rgw-go's whole write at a boundary of radosgw's leaves both versions, the one linked last current",
			func(ctx SpecContext, r denc.Release, gap int, current string) {
				open(ctx, r)
				rw := newRGWWriter(c, s, rec, r, "rgw")
				for i, step := range rw.Steps() {
					if i == gap {
						goWrite(ctx)
					}
					step(ctx)
				}
				if gap == len(rw.Steps()) {
					goWrite(ctx)
				}
				settled(shard, current, "go", "rgw")
				linkedLast(shard)
			},
			rgwRaceEntries(),
		)

		It("an apply that loses its olh.ver guard retries from a fresh read and lands", func(ctx SpecContext) {
			rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
			at(racePoint{object: raceOLHHead, n: 1}, func() { rw.Run(ctx) })
			goWrite(ctx)

			settled(shard, "go", "go", "rgw")
			Expect(olhVer()).To(Equal("3"), "rgw-go's link took epoch 3 after radosgw's 2")
			var guards []string
			for _, w := range c.WritesTo(testDataPool, "", olhHeadOID(version(""))) {
				for _, st := range w.Steps() {
					if x, ok := st.(*radosclient.SetXattrStep); ok && x.Name == meta.AttrOLHVer && string(x.Value) == "3" {
						guards = append(guards, olhVerGuard(w))
					}
				}
			}
			Expect(guards).To(Equal([]string{"", "2"}), "rgw-go's apply guarded on the empty olh.ver it read, refused, then on radosgw's epoch 2")
		})
	})

	Describe("setOLH", func() {
		It("links a version and replays its link: the version current, its pending attr gone, the log trimmed", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			settled(shard, "v2", "v1", "v2")
			Expect(olhVer()).To(Equal("3"))
		})

		It("carries the guard its condition returns on the head each attempt read in that attempt's init", func(ctx SpecContext) {
			put(ctx, "v1")
			var seen []string
			cond := func(_ context.Context, h *op.ObjectState) (*driver.OLHCondGuard, error) {
				seen = append(seen, string(h.Attrs[meta.AttrOLHVer]))
				return &driver.OLHCondGuard{Info: h.Attrs[meta.AttrOLHInfo]}, nil
			}
			info := slices.Clone(olhHead().Xattrs[meta.AttrOLHInfo])
			c.ResetCounters()
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{Cond: cond})).To(Succeed())
			Expect(seen).To(Equal([]string{"2"}), "run once, on the head the attempt read")
			init := c.WritesTo(testDataPool, "", olhHeadOID(version("")))[0]
			Expect(init.Steps()).To(ContainElement(&radosclient.CmpXattrStep{Name: meta.AttrOLHInfo, Op: radosclient.CmpEQ, Value: info}))
			settled(shard, "v2", "v1", "v2")
		})

		It("ends the write when its condition fails, with nothing written to the head and its version removed", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			c.ResetCounters()
			cond := func(context.Context, *op.ObjectState) (*driver.OLHCondGuard, error) {
				return nil, op.ErrPreconditionFailed
			}
			err := s.SetOLHForTest(ctx, rec, v, driver.OLHLink{Cond: cond})
			Expect(err).To(MatchError(op.ErrPreconditionFailed))
			Expect(err).To(MatchError(ContainSubstring(`linking plain/k version "v2"`)), "the write's context, as its other failures carry")
			Expect(c.Writes(testDataPool, "", olhHeadOID(version("")))).To(BeZero())
			settle(s)
			Expect(versionHead("v2")).To(BeNil())
			settled(shard, "v1", "v1")
		})

		Describe("a null delete marker over the null version", func() {
			// null makes k's null version and links it, which converts the
			// head into the OLH head with a fresh write tag.
			null := func(ctx context.Context) []byte {
				GinkgoHelper()
				Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
				return slices.Clone(olhHead().Xattrs[meta.AttrIDTag])
			}
			marker := func(expectTag []byte) driver.OLHLink {
				return driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}, ExpectTag: expectTag}
			}

			It("removes the null version's data under the expected write tag, keeping the OLH head", func(ctx SpecContext) {
				tag := null(ctx)
				Expect(s.SetOLHForTest(ctx, rec, version(""), marker(tag))).To(Succeed())
				settle(s)
				Expect(olhHead()).NotTo(BeNil(), "obj_remove keeps the OLH attrs")
				Expect(olhHead().Data).To(BeEmpty(), "the null version's data is gone")
				Expect(pending()).To(BeEmpty())
			})

			It("answers the refusal of the null version's removal by its expected write tag", func(ctx SpecContext) {
				null(ctx)
				err := s.SetOLHForTest(ctx, rec, version(""), marker([]byte("another-tag")))
				Expect(err).To(MatchError(driver.ErrRemovalRefused))
				_, unfinished := driver.UnfinishedOLHTail(err)
				Expect(unfinished).To(BeFalse(), "final")
				Expect(olhHead().Data).To(Equal([]byte("data-")), "the null version's data stays")
				Expect(pending()).To(BeEmpty())
			})
		})

		It("tries again from a fresh read after another writer's exclusive create beat its init", func(ctx SpecContext) {
			rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
			c.BeforeWrite(testDataPool, "", olhHeadOID(version("")), func(*fakerados.Object) {
				c.BeforeWrite(testDataPool, "", olhHeadOID(version("")), nil)
				rw.ReadHead(ctx)
				Expect(rw.Init(ctx)).To(BeTrue())
			})
			v := write(ctx, "v1")
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(Succeed())

			inits := 0
			for _, w := range c.WritesTo(testDataPool, "", olhHeadOID(version(""))) {
				if slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool { _, ok := st.(*radosclient.CreateStep); return ok }) {
					inits++
				}
			}
			Expect(inits).To(Equal(2), "the other writer's exclusive create and rgw-go's, which lost; olh_init_modification's EEXIST is ECANCELED")
			Expect(target()).To(Equal("v1"))
			Expect(pending()).To(ConsistOf(meta.AttrOLHPendingPrefix+rw.opTag), "the other writer's, which never linked")
		})

		It("repairs a head whose OLH tag the index no longer has, then links", func(ctx SpecContext) {
			put(ctx, "v1")
			tag := slices.Clone(olhHead().Xattrs[meta.AttrOLHIDTag])
			olhHead().Xattrs[meta.AttrOLHIDTag] = []byte("stalestalestalestalestalestale00")
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{})).To(Succeed())
			Expect(olhHead().Xattrs[meta.AttrOLHIDTag]).To(Equal(tag), "repair_olh rewrote the head's tag from the index's OLH entry")
			settled(shard, "v2", "v1", "v2")
		})

		It("answers EIO after 100 attempts another writer's guard refused, and removes the version it wrote", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			n := 0
			c.BeforeWrite(testDataPool, "", olhHeadOID(version("")), func(o *fakerados.Object) {
				n++
				o.Xattrs[meta.AttrOLHIDTag] = fmt.Appendf(nil, "racer%027d", n)
			})
			c.ResetCounters()
			err := s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})
			c.BeforeWrite(testDataPool, "", olhHeadOID(version("")), nil)
			Expect(err).To(MatchError(op.ErrUnknown), "radosgw's EIO, rgw_rados.cc:8741-8744 at v19.2.7")
			Expect(n).To(Equal(driver.OLHAttempts), "one init an attempt")
			Expect(c.Reads(testDataPool, "", olhHeadOID(version("")))).To(Equal(driver.OLHAttempts), "one head read an attempt")
			settle(s)
			Expect(versionHead("v2")).To(BeNil(), "no link landed, so the version's head is removed")
			Expect(hasInstanceEntry(shard, "v2")).To(BeFalse(), "and its instance entry")
			Expect(listed(shard)).To(Equal([]string{"v1"}))
		})

		It("cancels, runs a best-effort update and removes its version after a link the class refused", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			fc.fail("bucket_link_olh", 1, syscall.EIO)
			c.ResetCounters()
			err := s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})
			var re *radosclient.Error
			Expect(errors.As(err, &re)).To(BeTrue(), "the class's answer: %v", err)
			Expect(re.Errno).To(BeEquivalentTo(syscall.EIO))
			Expect(pending()).To(BeEmpty(), "olh_cancel_modification removed the pending attr")
			Expect(c.Reads(rookIndexPool, "", shard)).To(BeNumerically(">=", 1), "the best-effort update read the log")
			settle(s)
			Expect(versionHead("v2")).To(BeNil(), "the version's head is removed")
			Expect(hasInstanceEntry(shard, "v2")).To(BeFalse(), "and its instance entry")
			settled(shard, "v1", "v1")
		})

		It("removes nothing and keeps its pending attr after a link that timed out, which expires when it never landed", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			fc.fail("bucket_link_olh", 1, syscall.ETIMEDOUT)
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(MatchError(radosclient.ErrTimedOut))
			settle(s)
			Expect(versionHead("v2")).NotTo(BeNil(), "the version's head stays")
			Expect(hasInstanceEntry(shard, "v2")).To(BeTrue(), "and its instance entry, as radosgw leaves it")
			Expect(pending()).To(HaveLen(1), "the link may have landed, so its attr stays for the next reader's replay")

			follow := func() string {
				GinkgoHelper()
				key, _, err := s.FollowOLHForTest(ctx, readHead(ctx), func(ctx context.Context, h *op.ObjectState) error { return s.UpdateOLHForTest(ctx, rec, h) })
				Expect(err).NotTo(HaveOccurred())
				return key.Instance
			}
			Expect(follow()).To(Equal("v1"))
			Expect(pending()).To(HaveLen(1), "a replay of a log without the link leaves the attr")
			clock.Add(int64(time.Hour + time.Second))
			Expect(follow()).To(Equal("v1"))
			Expect(pending()).To(BeEmpty(), "check_pending_olh_entries expires it past rgw_olh_pending_timeout_sec")
		})

		It("applies, in its best-effort update, a link that landed and timed out", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			fc.landThenFail("bucket_link_olh", syscall.ETIMEDOUT)
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(MatchError(radosclient.ErrTimedOut))
			settled(shard, "v2", "v1", "v2")
		})

		It("keeps its pending attr after a link that landed and timed out, so the next follow applies it", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			fc.landThenFail("bucket_link_olh", syscall.ETIMEDOUT)
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{SkipUpdate: true})).To(MatchError(radosclient.ErrTimedOut))
			settle(s)
			Expect(listed(shard)).To(Equal([]string{"v2", "v1"}), "the link landed")
			Expect(target()).To(Equal("v1"))
			Expect(pending()).To(HaveLen(1), "a follow replays only while a pending attr remains")
			_, _, err := s.FollowOLHForTest(ctx, readHead(ctx), func(ctx context.Context, h *op.ObjectState) error { return s.UpdateOLHForTest(ctx, rec, h) })
			Expect(err).NotTo(HaveOccurred())
			settled(shard, "v2", "v1", "v2")
		})

		It("removes nothing after a link that landed as its context ended, and the next update applies it", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			wctx, cancel := context.WithCancel(ctx)
			fc.landThenCancel("bucket_link_olh", cancel)
			Expect(s.SetOLHForTest(wctx, rec, v, driver.OLHLink{})).To(MatchError(context.Canceled))
			settle(s)
			Expect(versionHead("v2")).NotTo(BeNil(), "a removal would leave the landed link listing a version with no head")
			Expect(listed(shard)).To(Equal([]string{"v2", "v1"}), "the link landed")
			Expect(pending()).To(HaveLen(1), "a context that ended leaves the outcome unknown, so no cancel runs")
			Expect(update(ctx)).To(Succeed())
			settled(shard, "v2", "v1", "v2")
		})

		It("answers its context's error when it ends after the link, leaving the link for the next update", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			wctx, cancel := context.WithCancel(ctx)
			writes := 0
			c.AfterWrite(rookIndexPool, "", shard, func() {
				if writes++; writes == 1 {
					cancel()
				}
			})
			Expect(s.SetOLHForTest(wctx, rec, v, driver.OLHLink{})).To(MatchError(context.Canceled))
			c.AfterWrite(rookIndexPool, "", shard, nil)
			Expect(versionHead("v2")).NotTo(BeNil())
			Expect(pending()).To(HaveLen(1))
			Expect(olhEntry(shard).PendingLog).To(HaveKey(uint64(3)))
			Expect(update(ctx)).To(Succeed())
			settled(shard, "v2", "v1", "v2")
		})

		It("lands the links after an OLH cleared despite a failed head removal, whose log starts again below olh.ver", func(ctx SpecContext) {
			put(ctx, "v1")
			fc.fail("obj_check_attrs_prefix", 1, syscall.EIO)
			Expect(unlink(ctx, "v1", driver.OLHUnlink{})).To(Succeed(), "clear_olh clears the index after the head removal's failure")
			Expect(olhHead()).NotTo(BeNil(), "the head stays")
			Expect(olhVer()).To(Equal("3"))
			_, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeFalse(), "the index's OLH entry is gone")

			put(ctx, "v2")
			Expect(olhEntry(shard).Epoch).To(BeEquivalentTo(2), "the class starts the new OLH entry at epoch 2")
			Expect(olhVer()).To(Equal("2"), "the replay read the link at epoch 2 and wrote olh.ver back to it")
			Expect(target()).To(Equal("v1"), "the tie-break keeps the head's version: v1 sorts first")
			Expect(pending()).To(BeEmpty())
			Expect(olhEntry(shard).PendingLog).To(BeEmpty())

			put(ctx, "v3")
			settled(shard, "v3", "v2", "v3")
			linkedLast(shard)
		})

		It("replays a link at the OLH entry's own epoch, as a lifecycle transition of the current version sends it", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			Expect(s.SetOLHForTest(ctx, rec, version("v2"), driver.OLHLink{Epoch: 3})).To(Succeed())
			settled(shard, "v2", "v1", "v2")
			linkedLast(shard)
		})

		It("retries, from a fresh read, an init that finds gone the head its read found, and links", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			c.BeforeWrite(testDataPool, "", olhHeadOID(version("")), func(*fakerados.Object) {
				c.BeforeWrite(testDataPool, "", olhHeadOID(version("")), nil)
				Expect(unlink(ctx, "v1", driver.OLHUnlink{})).To(Succeed())
				Expect(olhHead()).To(BeNil(), "unlinking the key's last version cleared its OLH")
			})
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(Succeed(), "set_olh answers this init's ENOENT")
			settled(shard, "v2", "v2")
			Expect(versionHead("v1")).To(BeNil())
		})

		Describe("a link at an epoch below the OLH entry's", func() {
			BeforeEach(func(ctx SpecContext) {
				put(ctx, "v1")
				put(ctx, "v2")
			})

			It("writes the version non-current and removes its own pending attr, which no log entry names", func(ctx SpecContext) {
				Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v0"), driver.OLHLink{Epoch: 2})).To(Succeed())
				Expect(pending()).To(BeEmpty())
				Expect(target()).To(Equal("v2"))
				Expect(listed(shard)).To(Equal([]string{"v2", "v0", "v1"}), "v0 at epoch 2, listed with its own instance")
				for _, en := range c.ListEntries(rookIndexPool, "", shard, "k") {
					want := uint16(0)
					if en.Key.Instance == "v2" {
						want = rgwcls.FlagCurrent
					}
					Expect(en.Flags&rgwcls.FlagCurrent).To(Equal(want), "the current flag of %q", en.Key.Instance)
				}
				Expect(olhEntry(shard).PendingLog).To(BeEmpty(), "cls_rgw.cc:1751-1760 at v19.2.7 logs nothing")
			})

			It("leaves the radosgw-shaped writer's pending attr, with no log entry, as radosgw does", func(ctx SpecContext) {
				rw := newRGWWriter(c, s, rec, denc.Squid, "v0")
				rw.epoch = 2
				rw.Run(ctx)
				Expect(pending()).To(ConsistOf(meta.AttrOLHPendingPrefix+rw.opTag), "apply_olh_log returns at once on an empty log")
				Expect(olhEntry(shard).PendingLog).To(BeEmpty())
				Expect(target()).To(Equal("v2"))
			})
		})

		It("keeps its pending attr when every apply loses its olh.ver guard, for the next follow to apply", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			writes := 0
			c.BeforeWrite(testDataPool, "", olhHeadOID(version("")), func(o *fakerados.Object) {
				// The first write is the init; every later one an apply, which
				// another writer's apply of the same epoch beats.
				if writes++; writes > 1 {
					o.Xattrs[meta.AttrOLHVer] = []byte(strings.Repeat("0", writes) + "2")
				}
			})
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(Succeed(), "ECANCELED past the bound is success")
			c.BeforeWrite(testDataPool, "", olhHeadOID(version("")), nil)
			Expect(writes).To(Equal(1+1+driver.OLHReplayRetries), "the init, the apply and its retries")
			Expect(pending()).To(HaveLen(1), "the link's pending attr stays")
			Expect(olhEntry(shard).PendingLog).To(HaveKey(uint64(3)), "the link is unapplied")
			Expect(target()).To(Equal("v1"), "olh.info stays on the previous version")

			h := readHead(ctx)
			_, _, err := s.FollowOLHForTest(ctx, h, func(ctx context.Context, h *op.ObjectState) error { return s.UpdateOLHForTest(ctx, rec, h) })
			Expect(err).NotTo(HaveOccurred())
			Expect(target()).To(Equal("v2"), "the stored olh.info names the version the listing shows current")
			Expect(listed(shard)[0]).To(Equal("v2"))
			Expect(pending()).To(BeEmpty())
		})

		It("leaves the pending attr and the log for the next update with SkipUpdate", func(ctx SpecContext) {
			put(ctx, "v1")
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{SkipUpdate: true})).To(Succeed())
			Expect(pending()).To(HaveLen(1))
			Expect(olhEntry(shard).PendingLog).To(HaveKey(uint64(3)))
			Expect(target()).To(Equal("v1"))
			Expect(update(ctx)).To(Succeed())
			settled(shard, "v2", "v1", "v2")
		})

		It("applies a writer's link that stopped before its replay with the next writer's", func(ctx SpecContext) {
			put(ctx, "v1")
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{SkipUpdate: true})).To(Succeed())
			put(ctx, "v3")
			settled(shard, "v3", "v1", "v2", "v3")
		})

		It("expires, on the next follow, a pending attr past rgw_olh_pending_timeout_sec", func(ctx SpecContext) {
			put(ctx, "v1")
			rw := newRGWWriter(c, s, rec, denc.Squid, "dead")
			rw.ReadHead(ctx)
			Expect(rw.Init(ctx)).To(BeTrue(), "a writer that dies after its init")
			clock.Add(int64(time.Hour + time.Second))
			key, _, err := s.FollowOLHForTest(ctx, readHead(ctx), func(ctx context.Context, h *op.ObjectState) error { return s.UpdateOLHForTest(ctx, rec, h) })
			Expect(err).NotTo(HaveOccurred())
			Expect(key.Instance).To(Equal("v1"))
			Expect(pending()).To(BeEmpty(), "removed by check_pending_olh_entries")
		})
	})

	Describe("the replay", func() {
		It("lands a log of more than the bound times 1000 epochs in one update, each page's apply guarded on the epoch the page before wrote", func(ctx SpecContext) {
			const tag = "pagespagespagespagespagespages00"
			n := driver.OLHReplayRetries*1000 + 1
			log := map[uint64][]rgwcls.OLHLogEntry{}
			for i := range n {
				epoch := uint64(i + 2)
				log[epoch] = []rgwcls.OLHLogEntry{{
					Epoch: epoch, Op: rgwcls.OLHOpLinkOLH, OpTag: fmt.Sprintf("op%06d", epoch), Key: rgwcls.ObjKey{Name: "k", Instance: fmt.Sprintf("v%06d", epoch)},
				}}
			}
			last := uint64(n + 1)
			c.Object(rookIndexPool, "", shard).Omap[rgwcls.OLHDataKey("k")] = encode(rgwcls.OLHEntry{
				Key: rgwcls.ObjKey{Name: "k", Instance: fmt.Sprintf("v%06d", last)}, Epoch: last, PendingLog: log, Tag: tag, Exists: true,
			})
			c.Put(testDataPool, "", olhHeadOID(version("")), nil)
			olhHead().Xattrs[meta.AttrOLHIDTag] = []byte(tag)
			olhHead().Xattrs[meta.AttrOLHVer] = []byte{}
			h := readHead(ctx)
			c.ResetCounters()

			Expect(s.UpdateOLHForTest(ctx, rec, h)).To(Succeed())
			Expect(h.Attrs).To(HaveKeyWithValue(meta.AttrOLHVer, BeEmpty()), "the caller's head is left as it read it")
			Expect(olhVer()).To(Equal(strconv.FormatUint(last, 10)))
			Expect(target()).To(Equal(fmt.Sprintf("v%06d", last)))
			Expect(olhEntry(shard).PendingLog).To(BeEmpty())
			Expect(c.Reads(testDataPool, "", olhHeadOID(version("")))).To(BeZero(), "no retry read the head again")
			var guards []string
			for _, w := range c.WritesTo(testDataPool, "", olhHeadOID(version(""))) {
				guards = append(guards, olhVerGuard(w))
			}
			want := []string{""}
			for page := 1; page <= driver.OLHReplayRetries; page++ {
				want = append(want, strconv.Itoa(page*1000+1))
			}
			Expect(guards).To(Equal(want), "one head op a page, each guarded on the epoch the page before wrote")
		})

		It("goes on to the next page after a page whose own removal the expected write tag refused, and answers the refusal", func(ctx SpecContext) {
			const tag = "pagespagespagespagespagespages00"
			write(ctx, "v1")
			log := map[uint64][]rgwcls.OLHLogEntry{2: {{Epoch: 2, Op: rgwcls.OLHOpRemoveInstance, OpTag: "own-op", Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}}}}
			for epoch := uint64(3); epoch <= 1002; epoch++ {
				log[epoch] = []rgwcls.OLHLogEntry{{Epoch: epoch, Op: rgwcls.OLHOpLinkOLH, OpTag: fmt.Sprintf("op%06d", epoch), Key: rgwcls.ObjKey{Name: "k", Instance: fmt.Sprintf("v%06d", epoch)}}}
			}
			c.Object(rookIndexPool, "", shard).Omap[rgwcls.OLHDataKey("k")] = encode(rgwcls.OLHEntry{
				Key: rgwcls.ObjKey{Name: "k", Instance: "v001002"}, Epoch: 1002, PendingLog: log, Tag: tag, Exists: true,
			})
			c.Put(testDataPool, "", olhHeadOID(version("")), nil)
			olhHead().Xattrs[meta.AttrOLHIDTag] = []byte(tag)
			olhHead().Xattrs[meta.AttrOLHVer] = []byte{}

			refused, err := s.ReplayOLHForTest(ctx, rec, readHead(ctx), "own-op", []byte("another-tag"))
			Expect(err).NotTo(HaveOccurred())
			Expect(refused).To(MatchError(driver.ErrRemovalRefused), "the first page's refusal, final")
			Expect(olhVer()).To(Equal("1002"), "the second page applied")
			Expect(olhEntry(shard).PendingLog).To(BeEmpty())
			Expect(versionHead("v1")).NotTo(BeNil())
		})

		It("runs a trim that failed once again, without a second head op", func(ctx SpecContext) {
			put(ctx, "v1")
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{SkipUpdate: true})).To(Succeed())
			fc.fail("bucket_trim_olh_log", 1, syscall.EIO)
			c.ResetCounters()
			Expect(update(ctx)).To(Succeed())
			Expect(c.Writes(testDataPool, "", olhHeadOID(version("")))).To(Equal(1), "one head op")
			settled(shard, "v2", "v1", "v2")
		})

		It("runs an index clear that failed once again, without a second head op", func(ctx SpecContext) {
			put(ctx, "v1")
			Expect(unlink(ctx, "v1", driver.OLHUnlink{SkipUpdate: true})).To(Succeed())
			fc.fail("bucket_clear_olh", 1, syscall.EIO)
			h := readHead(ctx)
			c.ResetCounters()
			Expect(s.UpdateOLHForTest(ctx, rec, h)).To(Succeed())
			heads := 0
			for _, w := range c.WritesTo(testDataPool, "", olhHeadOID(version(""))) {
				if slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
					x, ok := st.(*radosclient.SetXattrStep)
					return ok && x.Name == meta.AttrOLHVer
				}) {
					heads++
				}
			}
			Expect(heads).To(Equal(1), "one head op")
			settle(s)
			Expect(olhHead()).To(BeNil(), "the OLH head removed")
			_, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeFalse(), "the OLH entry cleared on the second run")
			Expect(c.Object(rookIndexPool, "", shard).Omap).To(BeEmpty(), "the version, its marker and the OLH entry are gone")
		})
	})

	Describe("a versioned write meets a reshard in progress", func() {
		It("links on the new shard when the reshard started between its completion and its link", func(ctx SpecContext) {
			v := write(ctx, "v1")
			setReshardStatus(ctx, c, shard, rgwcls.ReshardInProgress)
			var next string
			c.BeforeRead(rookIndexPool, "", shard, func() {
				c.BeforeRead(rookIndexPool, "", shard, nil)
				next = commitReshard()
			})
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(Succeed())
			Expect(next).NotTo(BeEmpty(), "the guard's wait saw the reshard commit")
			Expect(c.Object(rookIndexPool, "", shard)).To(BeNil())
			Expect(c.Reads(rookIndexPool, "", shard)).To(Equal(1), "the guard's one wait: the replay reads the shard its link found")
			settled(next, "v1", "v1")
		})

		It("trims on the new shard when the reshard started between its link and its trim", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			var next string
			links := 0
			c.AfterWrite(rookIndexPool, "", shard, func() {
				if links++; links != 1 {
					return
				}
				c.AfterWrite(rookIndexPool, "", shard, nil)
				setReshardStatus(ctx, c, shard, rgwcls.ReshardInProgress)
				reads := 0
				c.BeforeRead(rookIndexPool, "", shard, func() {
					// The first read is the replay's log read, which the reshard
					// guard does not stop; the second the trim's wait.
					if reads++; reads == 2 {
						c.BeforeRead(rookIndexPool, "", shard, nil)
						next = commitReshard()
					}
				})
			})
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(Succeed())
			Expect(next).NotTo(BeEmpty(), "the trim's wait saw the reshard commit")
			settled(next, "v2", "v1", "v2")
		})

		It("answers ErrBusyResharding for a reshard that outlasts the guard, its pending attr removed and its version left as radosgw leaves it", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			setReshardStatus(ctx, c, shard, rgwcls.ReshardInProgress)
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(MatchError(radosclient.ErrBusyResharding))
			Expect(pending()).To(BeEmpty(), "the cancel is on the head, which no reshard stops")
			Expect(target()).To(Equal("v1"))
			Expect(versionHead("v2")).NotTo(BeNil(), "the removal's prepare met the same reshard")
			Expect(hasInstanceEntry(shard, "v2")).To(BeTrue())
			Expect(listed(shard)).To(Equal([]string{"v1"}))
		})

		It("removes its version when the shard allows it after a reshard outlasted the link's guard", func(ctx SpecContext) {
			put(ctx, "v1")
			v := write(ctx, "v2")
			setReshardStatus(ctx, c, shard, rgwcls.ReshardInProgress)
			refused := 0
			c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
				// The link's guard sends ten, then the removal's prepare comes.
				if refused++; refused == 11 {
					c.BeforeWrite(rookIndexPool, "", shard, nil)
					setReshardStatus(ctx, c, shard, rgwcls.ReshardNone)
				}
			})
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(MatchError(radosclient.ErrBusyResharding))
			settle(s)
			Expect(versionHead("v2")).To(BeNil())
			Expect(hasInstanceEntry(shard, "v2")).To(BeFalse())
			settled(shard, "v1", "v1")
		})
	})

	Describe("unlinkObjInstance", func() {
		BeforeEach(func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
		})

		It("unlinks the current version, promotes the next and removes the unlinked version's head", func(ctx SpecContext) {
			Expect(unlink(ctx, "v2", driver.OLHUnlink{})).To(Succeed())
			Expect(versionHead("v2")).To(BeNil())
			settled(shard, "v1", "v1")
		})

		It("removes the OLH with the key's last version", func(ctx SpecContext) {
			Expect(unlink(ctx, "v2", driver.OLHUnlink{})).To(Succeed())
			Expect(unlink(ctx, "v1", driver.OLHUnlink{})).To(Succeed())
			settle(s)
			Expect(olhHead()).To(BeNil())
			Expect(c.Object(rookIndexPool, "", shard).Omap).To(BeEmpty())
		})

		It("tries an unlink the index refused with ECANCELED again from a fresh read, without a repair", func(ctx SpecContext) {
			fc.fail("bucket_unlink_instance", 1, syscall.ECANCELED)
			c.ResetCounters()
			Expect(unlink(ctx, "v1", driver.OLHUnlink{})).To(Succeed())
			Expect(c.Reads(testDataPool, "", olhHeadOID(version("")))).To(Equal(2), "one head read an attempt")
			Expect(c.Reads(rookIndexPool, "", shard)).To(Equal(1), "the replay's log read alone: unlink_obj_instance reads no OLH entry to repair")
			settled(shard, "v2", "v2")
		})

		It("removes, in its replay, the version an unlink naming an older epoch logs for removal", func(ctx SpecContext) {
			epoch := olhEntry(shard).Epoch
			Expect(unlink(ctx, "v1", driver.OLHUnlink{Epoch: epoch - 1})).To(Succeed())
			settle(s)
			Expect(versionHead("v1")).To(BeNil(), "the class logs the removal at the OLH entry's epoch, which a replay from the log's start reads")
			Expect(hasInstanceEntry(shard, "v1")).To(BeFalse())
			settled(shard, "v2", "v2")
		})

		It("answers NoSuchKey for a version the index does not have, leaving no pending attr", func(ctx SpecContext) {
			Expect(unlink(ctx, "nope", driver.OLHUnlink{})).To(MatchError(op.ErrNoSuchKey))
			settled(shard, "v2", "v1", "v2")
		})

		It("leaves the pending attr and the log for the next update with SkipUpdate", func(ctx SpecContext) {
			Expect(unlink(ctx, "v1", driver.OLHUnlink{SkipUpdate: true})).To(Succeed())
			Expect(pending()).To(HaveLen(1))
			Expect(versionHead("v1")).NotTo(BeNil(), "the replay removes it")
			Expect(update(ctx)).To(Succeed())
			Expect(versionHead("v1")).To(BeNil())
			settled(shard, "v2", "v2")
		})

		It("keeps its pending attr after an unlink that landed and timed out, so the next follow applies it", func(ctx SpecContext) {
			fc.landThenFail("bucket_unlink_instance", syscall.ETIMEDOUT)
			Expect(unlink(ctx, "v2", driver.OLHUnlink{SkipUpdate: true})).To(MatchError(radosclient.ErrTimedOut))
			settle(s)
			Expect(listed(shard)).To(Equal([]string{"v1"}), "the unlink landed")
			Expect(target()).To(Equal("v2"))
			Expect(pending()).To(HaveLen(1), "a follow replays only while a pending attr remains")
			_, _, err := s.FollowOLHForTest(ctx, readHead(ctx), func(ctx context.Context, h *op.ObjectState) error { return s.UpdateOLHForTest(ctx, rec, h) })
			Expect(err).NotTo(HaveOccurred())
			Expect(versionHead("v2")).To(BeNil())
			settled(shard, "v1", "v1")
		})

		It("answers one of two unlinks of one version NoSuchKey once the other's replay removed it", func(ctx SpecContext) {
			Expect(unlink(ctx, "v1", driver.OLHUnlink{})).To(Succeed())
			settle(s)
			Expect(unlink(ctx, "v1", driver.OLHUnlink{})).To(MatchError(op.ErrNoSuchKey))
			settled(shard, "v2", "v2")
		})

		It("answers both unlinks of one version success when the second lands between the first's unlink and its replay", func(ctx SpecContext) {
			var second error
			writes := 0
			c.AfterWrite(rookIndexPool, "", shard, func() {
				// The first is the first unlink's.
				if writes++; writes == 1 {
					c.AfterWrite(rookIndexPool, "", shard, nil)
					second = unlink(ctx, "v1", driver.OLHUnlink{})
				}
			})
			Expect(unlink(ctx, "v1", driver.OLHUnlink{})).To(Succeed())
			Expect(second).NotTo(HaveOccurred(), "the class refuses only a missing instance entry, which the first's replay removes")
			settled(shard, "v2", "v2")
		})

		It("leaves one consistent state after concurrent unlinks of one version", func(ctx SpecContext) {
			errs := make([]error, 4)
			var wg sync.WaitGroup
			for i := range errs {
				wg.Go(func() { errs[i] = unlink(ctx, "v1", driver.OLHUnlink{}) })
			}
			wg.Wait()
			ok := 0
			for _, err := range errs {
				if err == nil {
					ok++
					continue
				}
				Expect(err).To(MatchError(op.ErrNoSuchKey))
			}
			Expect(ok).To(BeNumerically(">=", 1), "at least one unlink lands")
			Expect(versionHead("v1")).To(BeNil())
			settled(shard, "v2", "v2")
		})

		Describe("with an expected write tag", func() {
			It("removes a version whose head still carries it", func(ctx SpecContext) {
				Expect(unlink(ctx, "v1", driver.OLHUnlink{ExpectTag: []byte(versionWriteTag(version("v1")))})).To(Succeed())
				Expect(versionHead("v1")).To(BeNil())
				settled(shard, "v2", "v2")
			})

			It("answers a final errRemovalRefused for one whose head does not, the head and instance entry kept, the list entry gone", func(ctx SpecContext) {
				err := unlink(ctx, "v1", driver.OLHUnlink{ExpectTag: []byte("another-tag")})
				Expect(err).To(MatchError(driver.ErrRemovalRefused))
				Expect(err).To(MatchError(ContainSubstring(`"v1"`)), "the refusal names the version")
				_, unfinished := driver.UnfinishedOLHTail(err)
				Expect(unfinished).To(BeFalse(), "final")
				settle(s)
				Expect(versionHead("v1")).NotTo(BeNil())
				Expect(hasInstanceEntry(shard, "v1")).To(BeTrue())
				Expect(listed(shard)).To(Equal([]string{"v2"}))
				Expect(pending()).To(BeEmpty())
				Expect(olhEntry(shard).PendingLog).To(BeEmpty())
			})

			It("trims the log when another writer's init refuses the clear, and that writer's replay leaves the version", func(ctx SpecContext) {
				Expect(unlink(ctx, "v2", driver.OLHUnlink{})).To(Succeed())
				rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
				rw.Prepare(ctx)
				rw.HeadWrite(ctx)
				rw.Complete(ctx)
				applies := 0
				c.AfterWrite(testDataPool, "", olhHeadOID(version("")), func() {
					if applies++; applies == 2 { // the init, then the apply
						c.AfterWrite(testDataPool, "", olhHeadOID(version("")), nil)
						rw.ReadHead(ctx)
						rw.InitRetrying(ctx)
					}
				})
				err := unlink(ctx, "v1", driver.OLHUnlink{ExpectTag: []byte("another-tag")})
				Expect(err).To(MatchError(driver.ErrRemovalRefused))
				_, unfinished := driver.UnfinishedOLHTail(err)
				Expect(unfinished).To(BeFalse(), "final: the trim took the refused entry out of the log")
				Expect(olhEntry(shard).PendingLog).To(BeEmpty())
				rw.Link(ctx)
				rw.ReadLog(ctx)
				rw.Apply(ctx)
				rw.Trim(ctx)
				Expect(versionHead("v1")).NotTo(BeNil(), "radosgw's replay, reading from 0, never met the refused removal")
				Expect(target()).To(Equal("rgw"))
			})

			It("answers, without running it again, a refusal that another writer's re-tag of the OLH entry left unsettled", func(ctx SpecContext) {
				Expect(unlink(ctx, "v2", driver.OLHUnlink{})).To(Succeed())
				rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
				rw.Prepare(ctx)
				rw.HeadWrite(ctx)
				rw.Complete(ctx)
				headWrites := 0
				c.AfterWrite(testDataPool, "", olhHeadOID(version("")), func() {
					// The unlink's init, its apply, then the clear's removal
					// of the head, after which the other writer finds no head.
					if headWrites++; headWrites == 3 {
						c.AfterWrite(testDataPool, "", olhHeadOID(version("")), nil)
						rw.ReadHead(ctx)
						rw.InitRetrying(ctx)
						rw.Link(ctx)
					}
				})
				c.ResetCounters()
				err := unlink(ctx, "v1", driver.OLHUnlink{ExpectTag: []byte("another-tag")})
				Expect(err).To(MatchError(driver.ErrRefusalUnsettled), "the re-tagged entry kept the unlink's log, cls_rgw.cc:1768-1777 at v19.2.7")
				Expect(err).To(MatchError(driver.ErrRemovalRefused))
				_, unfinished := driver.UnfinishedOLHTail(err)
				Expect(unfinished).To(BeFalse(), "no tail is left to run")
				trims := 0
				for _, w := range c.WritesTo(rookIndexPool, "", shard) {
					if execStep(w.Steps()[2]).Method == "bucket_trim_olh_log" {
						trims++
					}
				}
				Expect(trims).To(Equal(1), "no retry restores the old tag")
				Expect(versionHead("v1")).NotTo(BeNil())
				Expect(olhEntry(shard).Tag).To(Equal(string(rw.head.Attrs[meta.AttrOLHIDTag])))
			})

			It("runs a trim that failed once again and answers the refusal alone", func(ctx SpecContext) {
				fc.fail("bucket_trim_olh_log", 1, syscall.EIO)
				err := unlink(ctx, "v1", driver.OLHUnlink{ExpectTag: []byte("another-tag")})
				Expect(err).To(MatchError(driver.ErrRemovalRefused))
				_, unfinished := driver.UnfinishedOLHTail(err)
				Expect(unfinished).To(BeFalse())
				Expect(olhEntry(shard).PendingLog).To(BeEmpty())
			})

			It("answers the trim's error with the refusal when the trim keeps failing, the entry kept in the log", func(ctx SpecContext) {
				fc.fail("bucket_trim_olh_log", -1, syscall.EIO)
				c.ResetCounters()
				err := unlink(ctx, "v1", driver.OLHUnlink{ExpectTag: []byte("another-tag")})
				Expect(err).To(MatchError(driver.ErrRemovalRefused))
				tail, unfinished := driver.UnfinishedOLHTail(err)
				Expect(unfinished).To(BeTrue(), "not final")
				Expect(tail.Clear).To(BeFalse(), "the trim is left")
				Expect(olhEntry(shard).PendingLog).NotTo(BeEmpty())
				Expect(versionHead("v1")).NotTo(BeNil())

				fc.fail("bucket_trim_olh_log", 0, 0)
				Expect(update(ctx)).To(Succeed())
				settle(s)
				Expect(versionHead("v1")).To(BeNil(), "a later replay reads the log from its start and removes the version, guarded on the write tag it reads, as radosgw's does")
				settled(shard, "v2", "v2")
			})
		})
	})

	Describe("splitOLHTail", func() {
		DescribeTable("parts the unfinished tail from the refusal beside it",
			func(err func(tailErr error) error) {
				tailErr := driver.NewOLHTailErrorForTest(splitTail, errors.New("trim failed"))
				got, ok, rest := driver.SplitOLHTailForTest(err(tailErr))
				Expect(ok).To(BeTrue(), "the tail is found")
				Expect(got).To(Equal(splitTail))
				Expect(rest).To(MatchError(driver.ErrRemovalRefused), "the refusal is kept")
				Expect(rest).To(MatchError(ContainSubstring(`"v1"`)))
				_, unfinished := driver.UnfinishedOLHTail(rest)
				Expect(unfinished).To(BeFalse(), "the rest no longer holds the tail")
			},
			Entry("joined, as applyOLHLog answers", func(tailErr error) error { return errors.Join(tailErr, errSplitRefusal) }),
			Entry("joined and then wrapped", func(tailErr error) error { return fmt.Errorf("an update: %w", errors.Join(tailErr, errSplitRefusal)) }),
			Entry("wrapped beside the refusal", func(tailErr error) error { return fmt.Errorf("%w: %w", errSplitRefusal, tailErr) }),
		)

		It("answers no tail and the error as it is when the error names none", func() {
			_, ok, rest := driver.SplitOLHTailForTest(errSplitRefusal)
			Expect(ok).To(BeFalse())
			Expect(rest).To(Equal(errSplitRefusal))
		})
	})

	Describe("removeInstance", func() {
		It("answers NoSuchKey for a version with no head", func(ctx SpecContext) {
			err := s.NewOLHIndexForTest(rec, version("gone")).RemoveInstance(ctx, version("gone"), nil)
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})

		It("refuses, writing nothing, a head whose write tag is not the expected one", func(ctx SpecContext) {
			write(ctx, "v1")
			c.ResetCounters()
			err := s.NewOLHIndexForTest(rec, version("v1")).RemoveInstance(ctx, version("v1"), []byte("another-tag"))
			Expect(err).To(MatchError(driver.ErrRemovalRefused))
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
			Expect(c.Writes(testDataPool, "", instanceHeadOID(version("v1")))).To(BeZero())
		})

		It("refuses a head rewritten as the removal lands, canceling its prepare", func(ctx SpecContext) {
			write(ctx, "v1")
			c.BeforeWrite(testDataPool, "", instanceHeadOID(version("v1")), func(o *fakerados.Object) {
				o.Xattrs[meta.AttrIDTag] = []byte("rewritten")
			})
			err := s.NewOLHIndexForTest(rec, version("v1")).RemoveInstance(ctx, version("v1"), []byte(versionWriteTag(version("v1"))))
			Expect(err).To(MatchError(driver.ErrRemovalRefused))
			settle(s)
			Expect(versionHead("v1")).NotTo(BeNil())
			en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "v1"})
			Expect(ok).To(BeTrue())
			Expect(en.PendingMap).To(BeEmpty(), "the prepare was canceled")
		})

		It("guards on the write tag it read without an expected one", func(ctx SpecContext) {
			write(ctx, "v1")
			Expect(s.NewOLHIndexForTest(rec, version("v1")).RemoveInstance(ctx, version("v1"), nil)).To(Succeed())
			w := c.WritesTo(testDataPool, "", instanceHeadOID(version("v1")))
			Expect(w[len(w)-1].Steps()[0]).To(Equal(&radosclient.CmpXattrStep{Name: meta.AttrIDTag, Op: radosclient.CmpEQ, Value: []byte(versionWriteTag(version("v1")))}),
				"phase 1's guard, kept where Tentacle removes unguarded")
			Expect(versionHead("v1")).To(BeNil())
		})

		DescribeTable("sends apply_olh_log's VERSIONED_OP in its index calls, with NULL_VERSION for the null version alone",
			func(ctx SpecContext, instance string, flags uint16) {
				put(ctx, "v1")
				put(ctx, instance)
				c.ResetCounters()
				Expect(s.NewOLHIndexForTest(rec, version(instance)).RemoveInstance(ctx, version(instance), nil)).To(Succeed())
				settle(s)
				methods := map[string]uint16{}
				for _, w := range c.WritesTo(rookIndexPool, "", shard) {
					x := execStep(w.Steps()[2])
					d := denc.NewDecoder(x.In)
					switch x.Method {
					case "bucket_prepare_op":
						methods[x.Method] = rgwcls.DecodePrepareOp(d).BILogFlags
					case "bucket_complete_op":
						methods[x.Method] = rgwcls.DecodeCompleteOp(d).BILogFlags
					}
					Expect(d.Err()).NotTo(HaveOccurred())
				}
				Expect(methods).To(Equal(map[string]uint16{"bucket_prepare_op": flags, "bucket_complete_op": flags}),
					"delete_obj's flags, rgw_rados.cc:8534-8536 and :6064-6069 at v19.2.7")
			},
			Entry("a named version", "v2", rgwcls.BILogFlagVersionedOp),
			Entry("the null version", "", rgwcls.BILogFlagVersionedOp|rgwcls.BILogNullVersion),
		)
	})

	Describe("removeFailedVersion", func() {
		DescribeTable("removes a version a failed write made only when the failure proves nothing landed",
			func(ctx SpecContext, cause error, removed bool) {
				v := write(ctx, "v1")
				s.RemoveFailedVersionForTest(ctx, rec, v, cause)
				settle(s)
				if removed {
					Expect(versionHead("v1")).To(BeNil())
					Expect(hasInstanceEntry(shard, "v1")).To(BeFalse())
					return
				}
				Expect(versionHead("v1")).NotTo(BeNil())
				Expect(hasInstanceEntry(shard, "v1")).To(BeTrue())
			},
			Entry("a class's answer", &radosclient.Error{Errno: int32(syscall.EIO)}, true),
			Entry("the reshard guard's", &radosclient.Error{Errno: 2300}, true),
			Entry("a timeout", &radosclient.Error{Errno: int32(syscall.ETIMEDOUT)}, false),
			Entry("a context that ended", context.Canceled, false),
			Entry("a context past its deadline", context.DeadlineExceeded, false),
			Entry("a closed connection", radosclient.ErrClosed, false),
		)

		It("removes the version whose awaited completion the class refused", func(ctx SpecContext) {
			v := version("v1")
			writeInstanceHead(ctx, c, v)
			x := s.NewIndexOpForTest(rec, v, "never-prepared")
			err := x.CompleteSync(ctx, rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: 1}, versionMeta(v))
			Expect(err).To(MatchError(radosclient.ErrInvalid))
			s.RemoveFailedVersionForTest(ctx, rec, v, err)
			Expect(versionHead("v1")).To(BeNil())
		})

		It("leaves the null version, whose head is the OLH head", func(ctx SpecContext) {
			put(ctx, "v1")
			s.RemoveFailedVersionForTest(ctx, rec, version(""), &radosclient.Error{Errno: int32(syscall.EIO)})
			Expect(olhHead()).NotTo(BeNil())
			Expect(c.Writes(testDataPool, "", olhHeadOID(version("")))).To(Equal(2), "the init and the apply")
		})
	})

	Describe("the unfollowed head read", func() {
		It("reads an OLH head that StatObject refuses", func(ctx SpecContext) {
			put(ctx, "v1")
			_, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
			Expect(err).To(MatchError(op.ErrNotImplemented), "reads follow the OLH, which rgw-go does not do yet")
			h := readHead(ctx)
			Expect(h.Exists).To(BeTrue())
			Expect(h.Attrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("2")))
		})
	})
})
