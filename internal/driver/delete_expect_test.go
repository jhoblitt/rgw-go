package driver_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// writeHook runs fn once, around the n-th write op match takes, counted
// across every write the cluster sees, those fn itself sends included: after
// the op lands, or before it when before is set. With late set it sends
// nothing: the op answers success at once and late keeps its send, to land
// it later.
type writeHook struct {
	match  func(*radosclient.WriteOp) bool
	n      int
	before bool
	fn     func()
	late   func(send sendFunc)
	seen   int
}

// writeHooks is the aroundCluster hook list a spec arms, each run once.
type writeHooks struct {
	mu    sync.Mutex
	hooks []*writeHook
}

func (h *writeHooks) add(w *writeHook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hooks = append(h.hooks, w)
}

// take counts op against every hook it matches and returns the first whose
// count it completes, removing it.
func (h *writeHooks) take(op *radosclient.WriteOp) *writeHook {
	h.mu.Lock()
	defer h.mu.Unlock()
	var done *writeHook
	for _, w := range h.hooks {
		if !w.match(op) {
			continue
		}
		if w.seen++; w.seen == w.n && done == nil {
			done = w
		}
	}
	h.hooks = slices.DeleteFunc(h.hooks, func(w *writeHook) bool { return w == done })
	return done
}

func (h *writeHooks) around(op *radosclient.WriteOp, send sendFunc) (uint64, error) {
	w := h.take(op)
	switch {
	case w == nil:
		return send()
	case w.late != nil:
		w.late(send)
		return 0, nil
	case w.before:
		w.fn()
		return send()
	}
	ver, err := send()
	w.fn()
	return ver, err
}

var _ = Describe("DeleteObject with an expected state", func() {
	var (
		c      *fakerados.Cluster
		ac     *aroundCluster
		hooks  *writeHooks
		s      *driver.Store
		rec    *op.BucketRecord
		clock  atomic.Int64
		shard  string
		olhOID string
		attrs  map[string][]byte
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	open := func(ctx context.Context, r denc.Release) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, ac, conf(nil), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
	}
	BeforeEach(func(ctx SpecContext) {
		clock.Store(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC).UnixNano())
		c = newPutCluster()
		c.SetClock(now)
		hooks = &writeHooks{}
		ac = &aroundCluster{Cluster: c}
		ac.set(hooks.around)
		open(ctx, denc.Squid)
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
		olhOID = olhHeadOID(version(""))
		attrs = map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
		// Cleanups run last registered first, so a spec's releases of the
		// completions it holds (hold) run before this settle.
		DeferCleanup(func() {
			ac.set(nil)
			settle(s)
		})
	})

	suspend := func() { rec.Info.Flags |= meta.BucketVersionsSuspended }
	// write is a versioned write of version instance up to its link, its head
	// carrying versionWriteTag's tag.
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
	// nullPut is a suspended bucket's PutObject of the null version under
	// write tag tag.
	nullPut := func(ctx context.Context, tag, body string) {
		GinkgoHelper()
		_, err := s.PutObject(ctx, rec, version(""), strings.NewReader(body), op.PutParams{Attrs: attrs, Size: int64(len(body)), Tag: tag})
		Expect(err).NotTo(HaveOccurred(), "the null PutObject under tag %q", tag)
	}
	// head is version instance's head as a caller reads it for Expect, the
	// OLH head for the null version.
	head := func(ctx context.Context, instance string) *op.ObjectState {
		GinkgoHelper()
		st, err := s.ReadOLHHeadForTest(ctx, rec, version(instance))
		Expect(err).NotTo(HaveOccurred())
		return st
	}
	// attrWrite is an attr-only write of version instance, as PutObjectAcl
	// sends it, which gives its head a fresh write tag.
	attrWrite := func(ctx context.Context, instance string) {
		GinkgoHelper()
		policy := encode(acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: "carol"}), "Carol"))
		Expect(s.SetObjectAttrs(ctx, head(ctx, instance), map[string][]byte{meta.AttrACL: policy}, nil)).To(Succeed())
		settle(s)
	}
	del := func(ctx context.Context, instance string, p op.DeleteParams) (*op.DeleteObjectResult, error) {
		return s.DeleteObject(ctx, rec, version(instance), p)
	}
	calls := func(method string) func(*radosclient.WriteOp) bool {
		return func(w *radosclient.WriteOp) bool { return callsMethod(w.Steps(), method) }
	}
	// inits takes an OLH init's op: a null write's head write sets the
	// pending attrs again too, with its data.
	inits := func(w *radosclient.WriteOp) bool { return initsOLHSteps(w.Steps()) && !writesData(w) }
	// at arms fn to run once the n-th later write op match takes has landed.
	at := func(match func(*radosclient.WriteOp) bool, n int, fn func()) {
		hooks.add(&writeHook{match: match, n: n, fn: fn})
	}
	// before arms fn to run once just before the n-th later write op match
	// takes.
	before := func(match func(*radosclient.WriteOp) bool, n int, fn func()) {
		hooks.add(&writeHook{match: match, n: n, before: true, fn: fn})
	}
	// hold blocks the first later write op match takes until release, which
	// the spec's cleanup also calls, so a failed spec leaves nothing blocked.
	hold := func(match func(*radosclient.WriteOp) bool) (release func()) {
		held := make(chan struct{})
		before(match, 1, func() { <-held })
		release = sync.OnceFunc(func() { close(held) })
		DeferCleanup(release)
		return release
	}
	// late holds back the first later write op match takes: the op answers
	// success at once, as another gateway's write that has not yet reached
	// the shard looks to rgw-go, and reaches it at land, which answers the
	// write's own result. rgw-go awaits its own replay's removal completion,
	// but radosgw's goes to the shard while its head op goes to the head
	// object, and nothing orders the two.
	late := func(match func(*radosclient.WriteOp) bool) (land func() error) {
		var mu sync.Mutex
		var send sendFunc
		hooks.add(&writeHook{match: match, n: 1, late: func(s sendFunc) {
			mu.Lock()
			defer mu.Unlock()
			send = s
		}})
		land = sync.OnceValue(func() error {
			mu.Lock()
			defer mu.Unlock()
			if send == nil {
				return errors.New("the write held back never came")
			}
			_, err := send()
			return err
		})
		DeferCleanup(land)
		return land
	}
	// attrWriteLate is attrWrite whose index completion, which radosgw sends
	// unawaited, is held until release: until then the version's instance
	// record keeps the flags the unlink left on it.
	attrWriteLate := func(ctx context.Context, instance string) (release func()) {
		GinkgoHelper()
		release = hold(func(w *radosclient.WriteOp) bool {
			for _, st := range w.Steps() {
				if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_complete_op" {
					c := rgwcls.DecodeCompleteOp(denc.NewDecoder(x.In))
					return c.Op == rgwcls.OpAdd && strings.Contains(c.Meta.Owner, "carol")
				}
			}
			return false
		})
		policy := encode(acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: "carol"}), "Carol"))
		Expect(s.SetObjectAttrs(ctx, head(ctx, instance), map[string][]byte{meta.AttrACL: policy}, nil)).To(Succeed())
		return release
	}

	olhHead := func() *fakerados.Object { return c.Object(testDataPool, "", olhOID) }
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
		return out
	}
	olhInfo := func() meta.OLHInfo {
		GinkgoHelper()
		o := olhHead()
		Expect(o).NotTo(BeNil(), "the OLH head")
		d := denc.NewDecoder(o.Xattrs[meta.AttrOLHInfo])
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info
	}
	list := func() []rgwcls.DirEntry { return c.ListEntries(rookIndexPool, "", shard, "k") }
	listed := func() []string {
		var out []string
		for _, en := range list() {
			out = append(out, en.Key.Instance)
		}
		return out
	}
	listEntry := func(instance string) rgwcls.DirEntry {
		GinkgoHelper()
		for _, en := range list() {
			if en.Key.Instance == instance && en.Flags&rgwcls.FlagDeleteMarker == 0 {
				return en
			}
		}
		Fail("version " + instance + " is not listed")
		return rgwcls.DirEntry{}
	}
	currents := func() []string {
		var out []string
		for _, en := range list() {
			if en.Flags&rgwcls.FlagCurrent != 0 {
				out = append(out, en.Key.Instance)
			}
		}
		return out
	}
	instanceEntry := func(instance string) (rgwcls.DirEntry, bool) {
		return c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: instance})
	}
	// nullMarker reports whether k holds the null delete marker's instance or
	// list entry.
	nullMarker := func() bool {
		_, ok := c.Entry(rookIndexPool, "", shard, rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k"}, true))
		return ok || slices.ContainsFunc(list(), func(en rgwcls.DirEntry) bool {
			return en.Key.Instance == "" && en.Flags&rgwcls.FlagDeleteMarker != 0
		})
	}
	olhLog := func() map[uint64][]rgwcls.OLHLogEntry {
		GinkgoHelper()
		e, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue(), "the OLH entry of k")
		return e.PendingLog
	}
	links := func() int {
		n := 0
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			if callsMethod(w.Steps(), "bucket_link_olh") {
				n++
			}
		}
		return n
	}
	// listing is k's entries in a listing with versions or without.
	listing := func(ctx context.Context, versions bool) []string {
		GinkgoHelper()
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 100, ListVersions: versions})
		Expect(err).NotTo(HaveOccurred())
		return versionIDs(res.Entries)
	}
	concurrent := func(err error) {
		GinkgoHelper()
		Expect(err).To(MatchError(op.ErrConcurrentModification))
	}
	// settledOn checks the key's one current version, instance, on the head
	// and in the listing, with no pending attr and the log trimmed.
	settledOn := func(ctx context.Context, instance string) {
		GinkgoHelper()
		settle(s)
		Expect(currents()).To(Equal([]string{instance}), "exactly one entry of k flagged current")
		Expect(list()[0].Key.Instance).To(Equal(instance), "the current version lists first")
		info := olhInfo()
		Expect([]any{info.Target.Key.Instance, info.Removed}).To(Equal([]any{instance, false}), "olh.info")
		Expect(pending()).To(BeEmpty(), "no pending attr")
		Expect(olhLog()).To(BeEmpty(), "the log is trimmed")
		Expect(listing(ctx, false)).To(Equal([]string{"k@" + orNull(instance)}), "a listing without versions shows k once")
	}

	Describe("removing a named version", func() {
		It("removes it while its head carries the expected write tag", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			Expect(del(ctx, "v1", op.DeleteParams{Expect: head(ctx, "v1")})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
			settle(s)
			Expect(versionHead("v1")).To(BeNil())
			Expect(listed()).To(Equal([]string{"v2"}))
			settledOn(ctx, "v2")
		})

		DescribeTable("refuses one whose head carries another write tag before its unlink, writing nothing",
			func(ctx SpecContext, r denc.Release, expect func() *op.ObjectState) {
				open(ctx, r)
				put(ctx, "v1")
				put(ctx, "v2")
				c.ResetCounters()
				_, err := del(ctx, "v1", op.DeleteParams{Expect: expect()})
				concurrent(err)
				Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero(), "nothing written to the OLH head")
				Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "nothing written to the index")
				Expect(listed()).To(Equal([]string{"v2", "v1"}))
			},
			Entry("another tag, on squid", denc.Squid, func() *op.ObjectState {
				return &op.ObjectState{Attrs: map[string][]byte{meta.AttrIDTag: []byte("idtag-other")}}
			}),
			Entry("another tag, on tentacle", denc.Tentacle, func() *op.ObjectState {
				return &op.ObjectState{Attrs: map[string][]byte{meta.AttrIDTag: []byte("idtag-other")}}
			}),
			Entry("no tag, which reads as empty", denc.Squid, func() *op.ObjectState { return &op.ObjectState{} }),
		)

		DescribeTable("identifies a delete marker by its entry's versioned_epoch and mtime, which its state read by its version carries",
			func(ctx SpecContext, null bool, change func(ctx context.Context, e *op.ObjectState), current string) {
				put(ctx, "v1")
				if null {
					suspend()
				}
				m, err := del(ctx, "", op.DeleteParams{})
				Expect(err).NotTo(HaveOccurred())
				expect, err := s.StatObject(ctx, rec, version(m.VersionID))
				Expect(err).NotTo(HaveOccurred())
				Expect(expect.DeleteMarker).To(BeTrue())
				change(ctx, expect)
				settle(s)
				was := listed()
				c.ResetCounters()
				res, err := del(ctx, m.VersionID, op.DeleteParams{Expect: expect})
				settle(s)
				if current == "" {
					concurrent(err)
					Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero(), "nothing written to the OLH head")
					Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "nothing written to the index")
					Expect(listed()).To(Equal(was))
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: m.VersionID, DeleteMarker: true}))
				Expect(slices.ContainsFunc(list(), func(en rgwcls.DirEntry) bool { return en.Flags&rgwcls.FlagDeleteMarker != 0 })).To(BeFalse(),
					"no delete marker listed")
				Expect(nullMarker()).To(BeFalse())
				settledOn(ctx, current)
			},
			Entry("a named marker, unchanged, is removed", false, func(context.Context, *op.ObjectState) {}, "v1"),
			Entry("a named marker named with another marker's state answers 409", false, func(ctx context.Context, e *op.ObjectState) {
				clock.Add(int64(time.Second))
				m2, err := del(ctx, "", op.DeleteParams{})
				Expect(err).NotTo(HaveOccurred())
				other, err := s.StatObject(ctx, rec, version(m2.VersionID))
				Expect(err).NotTo(HaveOccurred())
				*e = *other
			}, ""),
			Entry("a marker named with a state of data answers 409", false, func(_ context.Context, e *op.ObjectState) {
				*e = op.ObjectState{Exists: true}
			}, ""),
			Entry("the null marker, unchanged, is removed", true, func(context.Context, *op.ObjectState) {}, "v1"),
			Entry("the null marker a null PutObject replaced since answers 409", true, func(ctx context.Context, _ *op.ObjectState) {
				nullPut(ctx, "tag-new", "new")
			}, ""),
			Entry("the null marker a second delete replaced since answers 409", true, func(ctx context.Context, _ *op.ObjectState) {
				clock.Add(int64(time.Second))
				Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			}, ""),
			Entry("the null marker a second delete replaced in the same instant answers 409, on its versioned_epoch", true, func(ctx context.Context, _ *op.ObjectState) {
				Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			}, ""),
		)

		// The key's last version gone, its OLH is cleared, and the next one's
		// epochs start again at 2.
		It("tells the null marker from one at the same versioned_epoch of an OLH made again since, on its mtime", func(ctx SpecContext) {
			suspend()
			Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			expect, err := s.StatObject(ctx, rec, version(meta.NullInstance))
			Expect(err).NotTo(HaveOccurred())
			Expect(del(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			settle(s)
			Expect(olhHead()).To(BeNil(), "the OLH cleared")
			clock.Add(int64(time.Second))
			Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			again, err := s.StatObject(ctx, rec, version(meta.NullInstance))
			Expect(err).NotTo(HaveOccurred())
			Expect(again.VersionedEpoch).To(Equal(expect.VersionedEpoch), "the same versioned_epoch")
			settle(s)
			c.ResetCounters()
			_, err = del(ctx, "null", op.DeleteParams{Expect: expect})
			concurrent(err)
			Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero(), "nothing written to the OLH head")
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "nothing written to the index")
			Expect(nullMarker()).To(BeTrue(), "the second marker stays")
		})

		It("refuses a version of data named with a delete marker's state, though its head has no write tag", func(ctx SpecContext) {
			put(ctx, "v1")
			v := version("v2")
			x := s.NewIndexOpForTest(rec, v, "go-tag-v2")
			x.SetBILogFlagsForTest(rgwcls.BILogFlagVersionedOp)
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			data, err := c.Pool(ctx, testDataPool, "")
			Expect(err).NotTo(HaveOccurred())
			w := radosclient.NewWriteOp()
			w.WriteFull([]byte("data-v2"))
			ver, err := data.Write(ctx, instanceHeadOID(v), w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: ver}, versionMeta(v))).To(Succeed())
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(Succeed())
			settle(s)
			c.ResetCounters()
			_, err = del(ctx, "v2", op.DeleteParams{Expect: &op.ObjectState{DeleteMarker: true}})
			concurrent(err)
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "nothing written to the index")
			Expect(listed()).To(Equal([]string{"v2", "v1"}))
		})

		It("guards on the empty expectation the removal of a version whose head has no write tag, refusing it once an attr write gave it one", func(ctx SpecContext) {
			put(ctx, "v1")
			v := version("v2")
			x := s.NewIndexOpForTest(rec, v, "go-tag-v2")
			x.SetBILogFlagsForTest(rgwcls.BILogFlagVersionedOp)
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			data, err := c.Pool(ctx, testDataPool, "")
			Expect(err).NotTo(HaveOccurred())
			w := radosclient.NewWriteOp()
			w.WriteFull([]byte("data-v2"))
			w.SetXattr(meta.AttrETag, []byte("etag-v2"))
			ver, err := data.Write(ctx, instanceHeadOID(v), w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: ver}, versionMeta(v))).To(Succeed())
			Expect(s.SetOLHForTest(ctx, rec, v, driver.OLHLink{})).To(Succeed())
			expect := head(ctx, "v2")
			Expect(expect.Attrs).NotTo(HaveKey(meta.AttrIDTag), "a head without a write tag")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			_, err = del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(versionHead("v2")).NotTo(BeNil())
			settledOn(ctx, "v2")
		})

		DescribeTable("a refused removal leaves the version listed",
			func(ctx SpecContext, r denc.Release, current bool) {
				open(ctx, r)
				put(ctx, "v1")
				put(ctx, "v2")
				target, other := "v1", "v2"
				if current {
					target, other = "v2", "v1"
				}
				was := listEntry(target)
				info := olhHead().Xattrs[meta.AttrOLHInfo]
				expect := head(ctx, target)
				at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, target) })
				c.ResetCounters()
				_, err := del(ctx, target, op.DeleteParams{Expect: expect})
				concurrent(err)
				Expect(err).To(MatchError(driver.ErrRemovalRefused), "the own replay's refusal")
				settle(s)
				h := versionHead(target)
				Expect(h).NotTo(BeNil(), "the version's head")
				Expect(h.Xattrs[meta.AttrIDTag]).NotTo(Equal(expect.Attrs[meta.AttrIDTag]), "with the attr write's tag")
				_, ok := instanceEntry(target)
				Expect(ok).To(BeTrue(), "its instance entry")
				Expect(listing(ctx, true)).To(ContainElement("k@"+target), "ListObjectVersions lists it")
				if !current {
					Expect(listEntry(target).VersionedEpoch).To(Equal(was.VersionedEpoch), "back at its own epoch")
					Expect(listEntry(target).Flags).To(Equal(rgwcls.FlagVer), "non-current")
					Expect(olhHead().Xattrs[meta.AttrOLHInfo]).To(Equal(info), "olh.info unchanged")
					settledOn(ctx, other)
					return
				}
				Expect(listEntry(target).VersionedEpoch).To(BeNumerically(">", was.VersionedEpoch), "current again at a fresh epoch")
				Expect(listEntry(other).Flags).To(Equal(rgwcls.FlagVer), "the version the unlink promoted, demoted")
				settledOn(ctx, target)
			},
			Entry("a noncurrent version, on squid", denc.Squid, false),
			Entry("a noncurrent version, on tentacle", denc.Tentacle, false),
			Entry("the current version, on squid", denc.Squid, true),
			Entry("the current version, on tentacle", denc.Tentacle, true),
		)

		It("re-links nothing after a refusal returned with an error, leaving the removal in the log", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v1")
			at(calls("bucket_unlink_instance"), 1, func() {
				attrWrite(ctx, "v1")
				ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
					if callsMethod(w.Steps(), "bucket_trim_olh_log") {
						return 0, &radosclient.Error{Errno: int32(syscall.EIO), Op: "write"}
					}
					return send()
				})
			})
			c.ResetCounters()
			_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
			Expect(err).To(MatchError(driver.ErrRemovalRefused))
			Expect(err).NotTo(MatchError(op.ErrConcurrentModification))
			_, unfinished := driver.UnfinishedOLHTail(err)
			Expect(unfinished).To(BeTrue(), "the trim is left")
			Expect(links()).To(BeZero(), "nothing re-linked")
			Expect(listed()).To(Equal([]string{"v2"}))
			Expect(olhLog()).NotTo(BeEmpty(), "the removal stays in the log")
			Expect(versionHead("v1")).NotTo(BeNil())
		})

		DescribeTable("another replay removes the refused version before the re-link, which re-links nothing",
			func(ctx SpecContext, landed bool) {
				put(ctx, "v1")
				put(ctx, "v2")
				expect := head(ctx, "v1")
				at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
				land := func() error { return nil }
				if !landed {
					land = late(completesDel)
				}
				// The own replay's head op is the first write to set olh.ver, the
				// unlink's init on a head with an OLH tag setting none; another
				// reader's replay, which read the log after the unlink, removes the
				// version first.
				before(func(w *radosclient.WriteOp) bool { return setsXattr(w, meta.AttrOLHVer) }, 1, func() {
					Expect(s.NewOLHIndexForTest(rec, version("v1")).RemoveInstance(ctx, version("v1"), nil)).To(Succeed())
					if landed {
						settle(s)
					}
				})
				c.ResetCounters()
				_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
				concurrent(err)
				Expect(links()).To(BeZero(), "nothing re-linked")
				Expect(land()).To(Succeed())
				settle(s)
				Expect(versionHead("v1")).To(BeNil())
				_, ok := instanceEntry("v1")
				Expect(ok).To(BeFalse())
				Expect(listed()).To(Equal([]string{"v2"}), "no list entry for a version with no head")
				settledOn(ctx, "v2")
			},
			Entry("its index completion landed, the instance entry gone", true),
			Entry("its index completion not yet landed, the instance entry still there", false),
		)

		DescribeTable("exactly one current entry after a fallback re-link",
			func(ctx SpecContext, arm func(ctx context.Context)) {
				put(ctx, "v1")
				put(ctx, "v2")
				was := listEntry("v2")
				expect := head(ctx, "v2")
				var release func()
				at(calls("bucket_unlink_instance"), 1, func() { release = attrWriteLate(ctx, "v2") })
				arm(ctx)
				_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
				concurrent(err)
				Expect(currents()).To(Equal([]string{"v3"}), "one entry flagged current while the attr write's completion is out")
				release()
				settle(s)
				Expect(listEntry("v2").VersionedEpoch).To(Equal(was.VersionedEpoch), "listed at its own epoch")
				Expect(listEntry("v2").Flags).To(Equal(rgwcls.FlagVer), "non-current")
				ie, ok := instanceEntry("v2")
				Expect(ok).To(BeTrue())
				Expect(ie.Flags).To(Equal(rgwcls.FlagVer), "its instance record carries FLAG_VER alone")
				Expect(listed()).To(Equal([]string{"v3", "v2", "v1"}))
				settledOn(ctx, "v3")
			},
			Entry("with a version linked and applied before the re-link reads the listing", func(ctx context.Context) {
				at(calls("bucket_trim_olh_log"), 1, func() { put(ctx, "v3") })
			}),
			// The inits are the unlink's, then the epoch-0 re-link's.
			Entry("with a version linked and applied between the re-link's read and its init", func(ctx context.Context) {
				before(inits, 2, func() { put(ctx, "v3") })
			}),
		)

		// The inits are the unlink's, then the epoch-0 re-link's first round's.
		It("re-links at epoch 0 in a later round when another writer's unlink moves the head before the first round's init", func(ctx SpecContext) {
			put(ctx, "v0")
			put(ctx, "v1")
			put(ctx, "v2")
			was := listEntry("v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			before(inits, 2, func() {
				Expect(del(ctx, "v0", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v0"}))
			})
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(listed()).To(Equal([]string{"v2", "v1"}))
			Expect(listEntry("v2").VersionedEpoch).To(BeNumerically(">", was.VersionedEpoch), "current again at a fresh epoch")
			settledOn(ctx, "v2")

			Expect(del(ctx, "v1", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
			Expect(listed()).To(Equal([]string{"v2"}))
			settledOn(ctx, "v2")
			st, err := s.StatObject(ctx, rec, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{st.Exists, st.Key.Instance}).To(Equal([]any{true, "v2"}))
		})

		It("waits for another writer's pending modification before a round's link, and re-links at epoch 0 once it leaves", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			rw := newRGWWriter(c, s, rec, denc.Squid, "slow")
			rw.ReadHead(ctx)
			Expect(rw.Init(ctx)).To(BeTrue(), "a writer whose modification is pending")
			Expect(pending()).To(HaveLen(1))
			opTag := strings.TrimPrefix(pending()[0], meta.AttrOLHPendingPrefix)
			waits := 0
			driver.SetCondSleepForTest(s, func(ctx context.Context, _ time.Duration) error {
				if waits++; waits == 1 {
					s.OLHCancelModificationForTest(ctx, head(ctx, ""), opTag)
				}
				return nil
			})
			was := listEntry("v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			Expect(waits).To(Equal(1), "one wait, which the writer's modification left during")
			settle(s)
			Expect(listed()).To(Equal([]string{"v2", "v1"}))
			Expect(listEntry("v2").VersionedEpoch).To(BeNumerically(">", was.VersionedEpoch), "current again at a fresh epoch")
			settledOn(ctx, "v2")
		})

		It("re-links at epoch 0 past a pending attr expired since, removing it first", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			rw := newRGWWriter(c, s, rec, denc.Squid, "dead")
			rw.ReadHead(ctx)
			Expect(rw.Init(ctx)).To(BeTrue(), "a writer that dies after its init")
			clock.Add(int64(2 * time.Hour))
			was := listEntry("v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(listed()).To(Equal([]string{"v2", "v1"}))
			Expect(listEntry("v2").VersionedEpoch).To(BeNumerically(">", was.VersionedEpoch), "current again at a fresh epoch")
			settledOn(ctx, "v2")
		})

		It("re-links at epoch 0 past a pending attr no writer removes, once its wait has passed", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			rw := newRGWWriter(c, s, rec, denc.Squid, "dead")
			rw.ReadHead(ctx)
			Expect(rw.Init(ctx)).To(BeTrue(), "a writer that dies after its init")
			stuck := pending()
			was := listEntry("v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(listed()).To(Equal([]string{"v2", "v1"}))
			Expect(currents()).To(Equal([]string{"v2"}))
			Expect(listEntry("v2").VersionedEpoch).To(BeNumerically(">", was.VersionedEpoch), "current again at a fresh epoch")
			Expect(olhInfo().Target.Key.Instance).To(Equal("v2"))
			Expect(pending()).To(Equal(stuck), "the dead writer's pending attr alone stays")
		})

		It("removes, not writes, the placeholder its reset's prepare makes when another replay removed the version since the reset read it", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			at(calls("bucket_trim_olh_log"), 1, func() { put(ctx, "v3") })
			// The prepares of an ADD of v2 are the attr write's, then the reset's.
			before(prepares("v2", rgwcls.OpAdd), 2, func() {
				Expect(s.NewOLHIndexForTest(rec, version("v2")).RemoveInstance(ctx, version("v2"), nil)).To(Succeed())
				settle(s)
			})
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(versionHead("v2")).To(BeNil())
			_, ok := instanceEntry("v2")
			Expect(ok).To(BeFalse(), "no entry for a version with no head")
			Expect(listed()).To(Equal([]string{"v3", "v1"}))
			settledOn(ctx, "v3")
		})

		It("lists below a remade OLH a version whose OLH entry the refused removal's replay cleared", func(ctx SpecContext) {
			put(ctx, "v1")
			expect := head(ctx, "v1")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
			at(calls("bucket_clear_olh"), 1, func() { put(ctx, "v2") })
			_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(listed()).To(Equal([]string{"v2", "v1"}), "v1, epoch 2 of the cleared OLH, lists below v2, epoch 2 of the new one")
			Expect(listEntry("v1").Flags).To(Equal(rgwcls.FlagVer))
			settledOn(ctx, "v2")
		})

		It("tells a remade OLH by its tag once the new OLH's epochs have passed the version's own", func(ctx SpecContext) {
			put(ctx, "v1")
			expect := head(ctx, "v1")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
			at(calls("bucket_clear_olh"), 1, func() {
				put(ctx, "v2")
				put(ctx, "v3")
			})
			_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(listed()).To(Equal([]string{"v3", "v2", "v1"}), "v1, epoch 2 of the cleared OLH, below v2 at epoch 2 of the new one")
			Expect(listEntry("v1").VersionedEpoch).To(BeEquivalentTo(1))
			settledOn(ctx, "v3")
		})

		It("runs its own replay whatever SkipOLHUpdate says", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			Expect(del(ctx, "v1", op.DeleteParams{Expect: head(ctx, "v1"), SkipOLHUpdate: true})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
			settle(s)
			Expect(versionHead("v1")).To(BeNil(), "the replay that removes it ran")
			settledOn(ctx, "v2")
		})
	})

	Describe("versionId=null", func() {
		It("removes the null version of a pre-versioning object no link has converted, the init's fresh write tag guarding the removal", func(ctx SpecContext) {
			write(ctx, "")
			Expect(del(ctx, "null", op.DeleteParams{Expect: head(ctx, "")})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
			settle(s)
			Expect(olhHead()).To(BeNil())
			Expect(listed()).To(BeEmpty())
		})

		It("keeps the data of the key's only version, the null version, whose removal was refused, and lists it again", func(ctx SpecContext) {
			write(ctx, "")
			expect := head(ctx, "")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "") })
			_, err := del(ctx, "null", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(olhHead()).NotTo(BeNil(), "the OLH head, which holds the null version's data")
			Expect(olhHead().Data).To(Equal([]byte("data-")))
			_, ok := instanceEntry("")
			Expect(ok).To(BeTrue())
			settledOn(ctx, "")
		})

		// nullMarkerOverV1 is a suspended bucket whose current version is the
		// null delete marker, over v1, and that marker's state read by its
		// version.
		nullMarkerOverV1 := func(ctx context.Context) *op.ObjectState {
			GinkgoHelper()
			put(ctx, "v1")
			suspend()
			Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			expect, err := s.StatObject(ctx, rec, version(meta.NullInstance))
			Expect(err).NotTo(HaveOccurred())
			Expect(expect.DeleteMarker).To(BeTrue())
			return expect
		}

		// The unlink's op compares the marker's instance entry, which the null
		// PutObject's link removed, so nothing is unlinked.
		It("refuses the unlink when a null PutObject replaced the expected null marker before its init, leaving the PutObject as it linked", func(ctx SpecContext) {
			expect := nullMarkerOverV1(ctx)
			rw := newRGWWriter(c, s, rec, denc.Squid, "")
			rw.Prepare(ctx)
			rw.HeadWrite(ctx)
			rw.Complete(ctx)
			var linked uint64
			before(inits, 1, func() {
				rw.ReadHead(ctx)
				rw.InitRetrying(ctx)
				rw.Link(ctx)
				linked = listEntry("").VersionedEpoch
			})
			_, err := del(ctx, "null", op.DeleteParams{Expect: expect})
			concurrent(err)
			rw.ReadLog(ctx)
			rw.Apply(ctx)
			rw.Trim(ctx)
			settle(s)
			Expect(olhHead().Data).To(Equal([]byte("data-")), "the PutObject's data")
			_, ok := instanceEntry("")
			Expect(ok).To(BeTrue(), "its instance entry")
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"", "v1"}))
			Expect(listEntry("").VersionedEpoch).To(Equal(linked), "at the epoch its link gave it, not listed again")
			settledOn(ctx, "")
		})

		// The second null marker lands just before the unlink's op, after the
		// delete read the first.
		It("refuses the unlink when a second null delete marker replaced the expected one just before it", func(ctx SpecContext) {
			expect := nullMarkerOverV1(ctx)
			var second *op.ObjectState
			before(calls("bucket_unlink_instance"), 1, func() {
				clock.Add(int64(time.Second))
				Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
				var err error
				second, err = s.StatObject(ctx, rec, version(meta.NullInstance))
				Expect(err).NotTo(HaveOccurred())
			})
			_, err := del(ctx, "null", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(nullMarker()).To(BeTrue(), "the second marker stays")
			st, err := s.StatObject(ctx, rec, version(meta.NullInstance))
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{st.DeleteMarker, st.VersionedEpoch, st.Mtime}).To(Equal([]any{true, second.VersionedEpoch, second.Mtime}))
			Expect(currents()).To(Equal([]string{""}), "the second marker current")
			Expect(listEntry("v1").Flags&rgwcls.FlagCurrent).To(BeZero(), "v1 not current")
			Expect(olhInfo().Removed).To(BeTrue())
		})

		It("refuses the unlink when a null PutObject replaced the expected null marker just before it, unlinking nothing", func(ctx SpecContext) {
			expect := nullMarkerOverV1(ctx)
			var linked uint64
			var linksThen int
			before(calls("bucket_unlink_instance"), 1, func() {
				nullPut(ctx, "tag-new", "new")
				linked, linksThen = listEntry("").VersionedEpoch, links()
			})
			_, err := del(ctx, "null", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(olhHead().Data).To(Equal([]byte("new")), "the PutObject's data")
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"", "v1"}))
			Expect(listEntry("").VersionedEpoch).To(Equal(linked), "at the epoch its link gave it")
			Expect(links()).To(Equal(linksThen), "nothing listed again")
			settledOn(ctx, "")
		})

		// A version linked over the marker demotes it, rewriting its entry's
		// flags alone, so the unlink tries again with the entry read now.
		It("removes the expected null marker when a version linked over it just before the unlink", func(ctx SpecContext) {
			expect := nullMarkerOverV1(ctx)
			before(calls("bucket_unlink_instance"), 1, func() { put(ctx, "v2") })
			Expect(del(ctx, "null", op.DeleteParams{Expect: expect})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			settle(s)
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"v2", "v1"}))
			settledOn(ctx, "v2")
		})

		// The unlink of the version above the marker promotes it, rewriting its
		// entry's flags alone.
		It("removes the expected null marker when the version above it was deleted just before the unlink", func(ctx SpecContext) {
			put(ctx, "v1")
			suspend()
			Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			put(ctx, "v2")
			expect, err := s.StatObject(ctx, rec, version(meta.NullInstance))
			Expect(err).NotTo(HaveOccurred())
			before(calls("bucket_unlink_instance"), 1, func() {
				Expect(del(ctx, "v2", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v2"}))
			})
			Expect(del(ctx, "null", op.DeleteParams{Expect: expect})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			settle(s)
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"v1"}))
			settledOn(ctx, "v1")
		})
	})

	Describe("the null delete marker of a suspended bucket", func() {
		// nullCurrent is a suspended bucket whose current version is the null
		// version under tag "tag-old", over v1.
		nullCurrent := func(ctx context.Context) {
			GinkgoHelper()
			put(ctx, "v1")
			suspend()
			nullPut(ctx, "tag-old", "old")
		}

		It("replaces the null version while the OLH head carries the expected write tag", func(ctx SpecContext) {
			nullCurrent(ctx)
			Expect(del(ctx, "", op.DeleteParams{Expect: head(ctx, "")})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			settle(s)
			Expect(olhHead().Data).To(BeEmpty(), "the null version's data, removed")
			Expect(currents()).To(Equal([]string{""}))
			Expect(olhInfo().Removed).To(BeTrue())
		})

		It("is not read on an enabled bucket, whose delete marker removes nothing", func(ctx SpecContext) {
			put(ctx, "v1")
			res, err := del(ctx, "", op.DeleteParams{Expect: &op.ObjectState{Attrs: map[string][]byte{meta.AttrIDTag: []byte("idtag-other")}}})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.DeleteMarker).To(BeTrue())
			Expect(currents()).To(Equal([]string{res.VersionID}))
		})

		DescribeTable("a null write between the expectation and the null marker refuses the marker",
			func(ctx SpecContext, r denc.Release, write func(ctx context.Context)) {
				open(ctx, r)
				nullCurrent(ctx)
				expect := head(ctx, "")
				write(ctx)
				written := olhHead().Xattrs[meta.AttrIDTag]
				c.ResetCounters()
				_, err := del(ctx, "", op.DeleteParams{Expect: expect})
				concurrent(err)
				Expect(links()).To(BeZero(), "no marker linked")
				Expect(nullMarker()).To(BeFalse())
				Expect(olhHead().Xattrs[meta.AttrIDTag]).To(Equal(written), "the new null version's head")
				_, ok := instanceEntry("")
				Expect(ok).To(BeTrue(), "its instance entry")
				settledOn(ctx, "")
			},
			Entry("a suspended PutObject, on squid", denc.Squid, func(ctx context.Context) { nullPut(ctx, "tag-new", "new") }),
			Entry("a suspended PutObject, on tentacle", denc.Tentacle, func(ctx context.Context) { nullPut(ctx, "tag-new", "new") }),
			Entry("an attr-only write, on squid", denc.Squid, func(ctx context.Context) { attrWrite(ctx, "") }),
		)

		// The marker linking first, the null write's link follows its removal
		// of the null version in the log, so the replay drops that removal and
		// the delete succeeds: it came first, and the write's data survives.
		DescribeTable("a null write after the marker's init and before its link ends current, with no marker",
			func(ctx SpecContext, r denc.Release, linkFirst, alongside bool) {
				open(ctx, r)
				nullCurrent(ctx)
				expect := head(ctx, "")
				var rw *rgwWriter
				if linkFirst {
					at(inits, 1, func() { nullPut(ctx, "tag-new", "new") })
				} else {
					rw = newRGWWriter(c, s, rec, r, "")
					at(inits, 1, func() {
						rw.Prepare(ctx)
						rw.HeadWrite(ctx)
						rw.Complete(ctx)
					})
					at(calls("bucket_link_olh"), 1, func() {
						rw.ReadHead(ctx)
						rw.InitRetrying(ctx)
						rw.Link(ctx)
						if alongside {
							Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "another reader's replay, before the marker's own")
						}
					})
				}
				res, err := del(ctx, "", op.DeleteParams{Expect: expect})
				want := []byte("new")
				if linkFirst {
					concurrent(err)
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
					rw.ReadLog(ctx)
					rw.Apply(ctx)
					rw.Trim(ctx)
					want = []byte("data-")
				}
				settle(s)
				Expect(nullMarker()).To(BeFalse(), "no null delete marker")
				Expect(olhHead().Data).To(Equal(want), "the new null version's data")
				_, ok := instanceEntry("")
				Expect(ok).To(BeTrue(), "its instance entry")
				Expect(listed()).To(Equal([]string{"", "v1"}))
				settledOn(ctx, "")
			},
			Entry("the null write linking first, on squid", denc.Squid, true, false),
			Entry("the null write linking first, on tentacle", denc.Tentacle, true, false),
			Entry("the marker linking first, on squid", denc.Squid, false, false),
			Entry("the marker linking first, on tentacle", denc.Tentacle, false, false),
			Entry("the marker linking first, another replay running alongside the marker's own", denc.Squid, false, true),
		)

		DescribeTable("another replay removes the refused null version before the re-link, which re-links nothing",
			func(ctx SpecContext, landed bool) {
				nullCurrent(ctx)
				expect := head(ctx, "")
				at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
				land := func() error { return nil }
				if !landed {
					land = late(completesDel)
				}
				// The marker's replay's head op is the first write to set olh.ver.
				before(func(w *radosclient.WriteOp) bool { return setsXattr(w, meta.AttrOLHVer) }, 1, func() {
					Expect(s.NewOLHIndexForTest(rec, version("")).RemoveInstance(ctx, version(""), nil)).To(Succeed())
					if landed {
						settle(s)
					}
				})
				c.ResetCounters()
				_, err := del(ctx, "", op.DeleteParams{Expect: expect})
				concurrent(err)
				Expect(links()).To(Equal(1), "the marker's link alone")
				Expect(land()).To(Succeed())
				settle(s)
				Expect(olhHead().Data).To(BeEmpty(), "the null version's data, which the other replay removed")
				_, ok := instanceEntry("")
				Expect(ok).To(BeFalse())
				Expect(listed()).To(Equal([]string{"", "v1"}), "the marker and v1, no list entry for the removed data")
				Expect(currents()).To(Equal([]string{""}))
				Expect(olhInfo().Removed).To(BeTrue(), "the marker current")
			},
			Entry("its index completion landed, the record gone", true),
			Entry("its index completion not yet landed, the record still there", false),
		)

		It("lists the null version again below a version another writer linked and applied after the marker", func(ctx SpecContext) {
			nullCurrent(ctx)
			was := listEntry("")
			expect := head(ctx, "")
			var release func()
			at(calls("bucket_link_olh"), 1, func() { release = attrWriteLate(ctx, "") })
			at(calls("bucket_trim_olh_log"), 1, func() { put(ctx, "v3") })
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			Expect(currents()).To(Equal([]string{"v3"}), "one entry flagged current while the attr write's completion is out")
			release()
			settle(s)
			Expect(listEntry("").VersionedEpoch).To(Equal(was.VersionedEpoch), "listed at its own epoch")
			Expect(listEntry("").Flags).To(Equal(rgwcls.FlagVer), "non-current")
			ie, ok := instanceEntry("")
			Expect(ok).To(BeTrue())
			Expect(ie.Flags).To(Equal(rgwcls.FlagVer), "its instance record carries FLAG_VER alone")
			Expect(nullMarker()).To(BeFalse(), "the class's null pair took the marker's entries")
			Expect(listed()).To(Equal([]string{"v3", "", "v1"}))
			settledOn(ctx, "v3")
		})

		It("makes current again the version the marker demoted when it is newer than the refused null version", func(ctx SpecContext) {
			nullCurrent(ctx)
			put(ctx, "v2")
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(nullMarker()).To(BeFalse())
			Expect(listEntry("").Flags).To(Equal(rgwcls.FlagVer))
			Expect(listed()).To(Equal([]string{"v2", "", "v1"}))
			settledOn(ctx, "v2")
		})

		// The inits below are the marker's, then the re-link's. Each writer in
		// these two leaves olh.info as the re-link read it and moves olh.ver.
		It("restores the null data past a pending attr expired since, removing it first", func(ctx SpecContext) {
			nullCurrent(ctx)
			rw := newRGWWriter(c, s, rec, denc.Squid, "dead")
			rw.ReadHead(ctx)
			Expect(rw.Init(ctx)).To(BeTrue(), "a writer that dies after its init")
			clock.Add(int64(2 * time.Hour))
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"", "v1"}))
			settledOn(ctx, "")
		})

		It("restores the null data past a pending attr no writer removes, once its wait has passed", func(ctx SpecContext) {
			nullCurrent(ctx)
			rw := newRGWWriter(c, s, rec, denc.Squid, "dead")
			rw.ReadHead(ctx)
			Expect(rw.Init(ctx)).To(BeTrue(), "a writer that dies after its init")
			stuck := pending()
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"", "v1"}))
			Expect(currents()).To(Equal([]string{""}))
			Expect(olhInfo().Target.Key.Instance).To(BeEmpty())
			Expect(pending()).To(Equal(stuck), "the dead writer's pending attr alone stays")
		})

		It("restores no version a client deleted by its id between the re-link's read and its init", func(ctx SpecContext) {
			nullCurrent(ctx)
			put(ctx, "v2")
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			var land func() error
			before(inits, 2, func() {
				land = late(completesDel)
				Expect(del(ctx, "v2", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v2"}))
			})
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			Expect(land()).To(Succeed())
			settle(s)
			Expect(versionHead("v2")).To(BeNil())
			_, ok := instanceEntry("v2")
			Expect(ok).To(BeFalse())
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"", "v1"}), "the next newest, the null version, current again")
			settledOn(ctx, "")
		})

		It("leaves the null data unlisted when a second null delete marker lands between the re-link's read and its init", func(ctx SpecContext) {
			nullCurrent(ctx)
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			var land func() error
			before(inits, 2, func() {
				land = late(completesDel)
				Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			})
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			Expect(land()).To(Succeed())
			settle(s)
			Expect(olhHead().Data).To(BeEmpty(), "the second marker's replay removed the data")
			_, ok := instanceEntry("")
			Expect(ok).To(BeFalse())
			Expect(nullMarker()).To(BeTrue(), "the second marker")
			Expect(currents()).To(Equal([]string{""}))
			Expect(olhInfo().Removed).To(BeTrue())
			Expect(listed()).To(Equal([]string{"", "v1"}))
		})

		It("a second null write during the fallback keeps the key current, resetting and re-linking nothing", func(ctx SpecContext) {
			nullCurrent(ctx)
			expect := head(ctx, "")
			at(inits, 1, func() { nullPut(ctx, "tag-p1", "p1") })
			// The inits are the marker's, P1's, then the epoch-0 re-link's. The
			// delete of v1 moves the OLH entry's epoch past P2's, so a reset of
			// P2's record and a link at its epoch would leave it non-current.
			before(inits, 3, func() {
				nullPut(ctx, "tag-p2", "p2")
				Expect(del(ctx, "v1", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
			})
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(olhHead().Data).To(Equal([]byte("p2")))
			ie, ok := instanceEntry("")
			Expect(ok).To(BeTrue())
			Expect(ie.Flags).To(Equal(rgwcls.FlagVer|rgwcls.FlagCurrent), "P2's record, not reset")
			Expect(listEntry("").VersionedEpoch).To(Equal(ie.VersionedEpoch), "P2's list entry at its epoch")
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{""}))
			settledOn(ctx, "")
		})
	})
})

// prepares is a writeHook match of a bucket index prepare of mod for k's
// version instance.
func prepares(instance string, mod rgwcls.ModifyOp) func(*radosclient.WriteOp) bool {
	return func(w *radosclient.WriteOp) bool {
		for _, st := range w.Steps() {
			if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_prepare_op" {
				p := rgwcls.DecodePrepareOp(denc.NewDecoder(x.In))
				return p.Op == mod && p.Key == rgwcls.ObjKey{Name: "k", Instance: instance}
			}
		}
		return false
	}
}

// completesDel reports whether w sends a bucket index DEL completion.
func completesDel(w *radosclient.WriteOp) bool {
	for _, st := range w.Steps() {
		if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_complete_op" {
			return rgwcls.DecodeCompleteOp(denc.NewDecoder(x.In)).Op == rgwcls.OpDel
		}
	}
	return false
}

// setsXattr reports whether w sets xattr name.
func setsXattr(w *radosclient.WriteOp, name string) bool {
	for _, st := range w.Steps() {
		if x, ok := st.(*radosclient.SetXattrStep); ok && x.Name == name {
			return true
		}
	}
	return false
}
