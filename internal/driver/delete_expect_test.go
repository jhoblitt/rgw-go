package driver_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// writeHook runs fn once, around the n-th write op match takes, counted
// across every write the cluster sees, those fn itself sends included: after
// the op lands, or before it when before is set. With late set it sends
// nothing: the op answers success at once and late keeps its send, to land
// it later. With abandoned set the op lands and its sender is answered
// abandoned's error in place of the op's success.
type writeHook struct {
	match     func(*radosclient.WriteOp) bool
	n         int
	before    bool
	fn        func()
	late      func(send sendFunc)
	abandoned func() error
	seen      int
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
	case w.abandoned != nil:
		if ver, err := send(); err != nil {
			return ver, err
		}
		return 0, w.abandoned()
	case w.before:
		w.fn()
		return send()
	}
	ver, err := send()
	w.fn()
	return ver, err
}

// removalPrepareKey is the instance whose removal w prepares, when w is a
// replay's prepare of a version's removal, the one prepare that compares
// index entries.
func removalPrepareKey(w *radosclient.WriteOp) (string, bool) {
	steps := w.Steps()
	if !slices.ContainsFunc(steps, func(st radosclient.Step) bool {
		_, ok := st.(*radosclient.OmapCmpStep)
		return ok
	}) {
		return "", false
	}
	for _, st := range steps {
		if e, ok := st.(*radosclient.ExecStep); ok && e.Method == "bucket_prepare_op" {
			return rgwcls.DecodePrepareOp(denc.NewDecoder(e.In)).Key.Instance, true
		}
	}
	return "", false
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
	// openWith opens the store at r with the options kv over conf's.
	openWith := func(ctx context.Context, r denc.Release, kv map[string]string) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, ac, conf(kv), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
	}
	open := func(ctx context.Context, r denc.Release) {
		GinkgoHelper()
		openWith(ctx, r, nil)
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
	// late holds back the first later write op match takes: the op answers
	// success at once, as another gateway's write that has not yet reached
	// the shard looks to rgw-go, and reaches it at land, which answers the
	// write's own result. rgw-go awaits its own replay's removal completion,
	// but radosgw's goes to the shard while its head op goes to the head
	// object, and nothing orders the two. land first waits for the write to
	// arrive, which a completion rgw-go sends unawaited does from the
	// completion manager's goroutine, some time after the call that sent it
	// has returned.
	late := func(match func(*radosclient.WriteOp) bool) (land func() error) {
		var send sendFunc
		arrived := make(chan struct{})
		hooks.add(&writeHook{match: match, n: 1, late: func(s sendFunc) {
			send = s
			close(arrived)
		}})
		land = sync.OnceValue(func() error {
			select {
			case <-arrived:
			case <-time.After(5 * time.Second):
				return errors.New("the write held back never came")
			}
			_, err := send()
			return err
		})
		DeferCleanup(land)
		return land
	}
	// abandon makes the n-th later write op match takes one whose sender's
	// context ended while it was in flight: the op lands, and d later its
	// sender is answered a context's error, as goceph's asynchronous
	// completion modes answer a call whose context ends before its completion
	// and leave the operation to librados.
	abandon := func(match func(*radosclient.WriteOp) bool, n int, d time.Duration) {
		hooks.add(&writeHook{match: match, n: n, abandoned: func() error {
			time.Sleep(d)
			return context.Canceled
		}})
	}
	// holdWrite is attrWrite with a legal hold ON.
	holdWrite := func(ctx context.Context, instance string) {
		GinkgoHelper()
		hold := objectlock.EncodeLegalHold(meta.ObjectLegalHold{Status: "ON"})
		Expect(s.SetObjectAttrs(ctx, head(ctx, instance), map[string][]byte{meta.AttrObjectLegalHold: hold}, nil)).To(Succeed())
		settle(s)
	}
	// relinks takes a relist's link, the one link that compares the OLH entry
	// it read.
	relinks := func(w *radosclient.WriteOp) bool {
		return callsMethod(w.Steps(), "bucket_link_olh") && slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
			_, ok := st.(*radosclient.OmapCmpStep)
			return ok
		})
	}
	// afterListing arms fn to run once, just before the first later read of
	// the key's OLH entry that directly follows a read of the listing: the
	// read a named version's relist sends before its epoch-0 link
	// (relinkOLHEntry), in a round that did not wait. A round that waits goes
	// from the listing to the next round's reads.
	afterListing := func(fn func()) {
		var lastListed atomic.Bool
		ac.setBeforeRead(func(r *radosclient.ReadOp) {
			steps := r.Steps()
			if !lastListed.Swap(callsMethod(steps, "bucket_list")) {
				return
			}
			for _, st := range steps {
				if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bi_get" && rgwcls.DecodeBIGetOp(denc.NewDecoder(x.In)).Type == rgwcls.BIIndexOLH {
					ac.setBeforeRead(nil)
					fn()
					return
				}
			}
		})
	}
	// attrWriteLate is attrWrite whose index completion, which radosgw sends
	// unawaited, lands only at release, as another gateway's attr write that
	// has not reached the shard looks to rgw-go (late): until then the
	// version's instance record keeps the flags the unlink left on it.
	attrWriteLate := func(ctx context.Context, instance string) (release func()) {
		GinkgoHelper()
		land := late(func(w *radosclient.WriteOp) bool {
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
		return func() {
			GinkgoHelper()
			Expect(land()).To(Succeed())
		}
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

		// A second delete of the refused version unlinks it while the refused
		// delete lists it again: a DeleteObjects entry, whose replay waits for
		// the request's last delete of the key, or a plain delete. Its removal
		// is in the log before the relist would link the version, or its
		// unlink changes the OLH entry the relist's link compares, so the
		// relist links nothing, and the second delete's replay removes the
		// version: a link logged after that removal would make the replay
		// keep it, undoing the second delete.
		DescribeTable("a second delete of the refused version during its relist is honored",
			func(ctx SpecContext, current, skip bool, when func(fn func())) {
				put(ctx, "v1")
				put(ctx, "v2")
				target, other := "v1", "v2"
				if current {
					target, other = "v2", "v1"
				}
				expect := head(ctx, target)
				at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, target) })
				var res *op.DeleteObjectResult
				var derr error
				when(func() { res, derr = del(ctx, target, op.DeleteParams{SkipOLHUpdate: skip}) })
				_, err := del(ctx, target, op.DeleteParams{Expect: expect})
				concurrent(err)
				Expect(derr).NotTo(HaveOccurred(), "the second delete")
				Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: target}))
				settle(s)
				if skip {
					Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "the second delete's request replays the key")
				}
				settle(s)
				Expect(versionHead(target)).To(BeNil(), "the second delete removed the version")
				_, ok := instanceEntry(target)
				Expect(ok).To(BeFalse(), "its instance entry")
				Expect(listed()).To(Equal([]string{other}), "no list entry is left for it")
				settledOn(ctx, other)
			},
			// The trim is the refused delete's own replay's; the inits are the
			// refused delete's unlink's, then the relist's.
			Entry("the current version, a DeleteObjects entry before the relist reads the key", true, true,
				func(fn func()) { at(calls("bucket_trim_olh_log"), 1, fn) }),
			Entry("the current version, a DeleteObjects entry before the relist's init", true, true,
				func(fn func()) { before(inits, 2, fn) }),
			Entry("the current version, a DeleteObjects entry between the relist's init and its link", true, true,
				func(fn func()) { before(calls("bucket_link_olh"), 1, fn) }),
			Entry("the current version, a plain delete between the relist's init and its link", true, false,
				func(fn func()) { before(calls("bucket_link_olh"), 1, fn) }),
			Entry("the current version, a plain delete before the relist reads the key", true, false,
				func(fn func()) { at(calls("bucket_trim_olh_log"), 1, fn) }),
			Entry("a noncurrent version listed below, a DeleteObjects entry before the relist reads the key", false, true,
				func(fn func()) { at(calls("bucket_trim_olh_log"), 1, fn) }),
			Entry("a noncurrent version listed below, a DeleteObjects entry just before its link", false, true,
				func(fn func()) { before(calls("bucket_link_olh"), 1, fn) }),
			Entry("a noncurrent version listed below, a plain delete just before its link", false, false,
				func(fn func()) { before(calls("bucket_link_olh"), 1, fn) }),
		)

		// Another writer's link of the key just before each of the relist's
		// first n links changes the OLH entry the link compares, which refuses
		// it: contention, after which the relist reads again and tries again.
		DescribeTable("lists the refused version again once the writers contending with its relist stop",
			func(ctx SpecContext, current bool, n int) {
				put(ctx, "v1")
				put(ctx, "v2")
				target := "v1"
				if current {
					target = "v2"
				}
				backoffs := 0
				driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
					backoffs++
					return nil
				})
				expect := head(ctx, target)
				at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, target) })
				for k := 1; k <= n; k++ {
					before(relinks, k, func() { put(ctx, fmt.Sprintf("w%02d", k)) })
				}
				_, err := del(ctx, target, op.DeleteParams{Expect: expect})
				concurrent(err)
				settle(s)
				Expect(backoffs).To(BeNumerically(">=", n), "a backoff after each refused link")
				Expect(versionHead(target)).NotTo(BeNil())
				Expect(listed()).To(ContainElement(target), "listed again")
				Expect(listing(ctx, true)).To(ContainElement("k@"+target), "ListObjectVersions lists it")
				settledOn(ctx, fmt.Sprintf("w%02d", n))
			},
			Entry("a noncurrent version, six links in a row", false, 6),
			Entry("the current version, six links in a row", true, 6),
			Entry("a noncurrent version, a writer contending for twenty-five links", false, 25),
		)

		It("lists the refused version though its client cancels the request mid-relist", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			rctx, cancel := context.WithCancel(ctx)
			defer cancel()
			expect := head(ctx, "v1")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
			for k := 1; k <= 3; k++ {
				before(relinks, k, func() {
					put(ctx, fmt.Sprintf("w%02d", k))
					if k == 3 {
						cancel()
					}
				})
			}
			_, err := s.DeleteObject(rctx, rec, version("v1"), op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(versionHead("v1")).NotTo(BeNil())
			Expect(listed()).To(ContainElement("v1"), "the relist went on past the cancellation")
			settledOn(ctx, "w03")
		})

		// Each of the relist's links meets another writer's link of the key
		// first: the relist stops at its bound, olhAttempts tries, and the
		// version stays stored and unlisted, the window the differences entry
		// records.
		It("leaves the refused version unlisted once an unbroken stream of other writers' links has refused its relist olhAttempts times", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v1")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
			sent := 0
			for k := 1; k <= 2*driver.OLHAttempts; k++ {
				before(relinks, k, func() {
					sent++
					put(ctx, fmt.Sprintf("w%03d", k))
				})
			}
			_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(sent).To(BeNumerically(">=", driver.OLHAttempts), "relist links sent")
			Expect(sent).To(BeNumerically("<", 2*driver.OLHAttempts), "the relist stopped at its bound")
			Expect(versionHead("v1")).NotTo(BeNil(), "stored")
			_, ok := instanceEntry("v1")
			Expect(ok).To(BeTrue(), "its instance entry")
			Expect(listed()).NotTo(ContainElement("v1"), "listed nowhere")
		})

		// As above, each of the relist's links meets another writer's link of
		// the key first, and here each also takes 15 ms to send, under a cap of
		// 40 ms on its retries, whose clock the first lost link starts: the
		// relist ends at the cap, most of its tries unspent, and the version
		// stays stored and unlisted, as at the try bound.
		It("leaves the refused version unlisted once its relist's cap passes while its RADOS operations are slow", func(ctx SpecContext) {
			const slow, limit = 15 * time.Millisecond, 40 * time.Millisecond
			driver.SetRelistCapForTest(s, limit)
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v1")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
			sent := 0
			for k := 1; k <= 2*driver.OLHAttempts; k++ {
				before(relinks, k, func() {
					sent++
					put(ctx, fmt.Sprintf("w%03d", k))
					time.Sleep(slow)
				})
			}
			_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(sent).To(BeNumerically("<", driver.OLHAttempts), "relist links sent: ended by the cap, not by the try bound")
			Expect(logs.String()).To(ContainSubstring(driver.ErrRelistCap.Error()), "the relist's end, as logged")
			Expect(versionHead("v1")).NotTo(BeNil(), "stored")
			_, ok := instanceEntry("v1")
			Expect(ok).To(BeTrue(), "its instance entry")
			Expect(listed()).NotTo(ContainElement("v1"), "listed nowhere")
		})

		It("caps a relist at 32 s, the figure the differences entry gives", func() {
			Expect(driver.RelistCap).To(Equal(32 * time.Second))
		})

		// No other writer refuses the relist, so no retry starts the cap's
		// clock, and the relist's first read never returns: it answers only
		// when its context ends, as goceph's asynchronous completion modes
		// answer a call whose operation librados has not finished. The
		// backstop, 50 ms here, ends the relist, and the version stays stored
		// and unlisted.
		It("ends a relist whose first attempt's RADOS call never returns at its backstop", func(ctx SpecContext) {
			const backstop = 50 * time.Millisecond
			driver.SetRelistBackstopForTest(s, backstop)
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			var stalled atomic.Int32
			at(calls("bucket_trim_olh_log"), 1, func() {
				ac.setReadCtx(func(ctx context.Context, _ *radosclient.ReadOp) error {
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
						return nil
					}
					stalled.Add(1)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(10 * time.Second):
						return errors.New("the stalled read outlived its spec")
					}
				})
			})
			DeferCleanup(func() { ac.setReadCtx(nil) })
			start := time.Now()
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			ac.setReadCtx(nil)
			concurrent(err)
			Expect(time.Since(start)).To(BeNumerically(">=", backstop), "the relist waited for its backstop")
			settle(s)
			Expect(stalled.Load()).To(BeEquivalentTo(1), "the relist's calls: its first, which stalled until the backstop")
			Expect(logs.String()).To(ContainSubstring(driver.ErrRelistBackstop.Error()), "the relist's end, as logged")
			Expect(versionHead("v2")).NotTo(BeNil(), "stored")
			_, ok := instanceEntry("v2")
			Expect(ok).To(BeTrue(), "its instance entry")
			Expect(listed()).To(Equal([]string{"v1"}), "listed nowhere, v1 current")
		})

		// The second write of a pair is sent past the cap on the relist's
		// retries, and a call that stalls there still ends at the backstop.
		It("sends the second write of a relist's pair past its cap, under its backstop", func() {
			relist, stop := context.WithTimeout(context.Background(), time.Hour)
			defer stop()
			retries, capPassed := context.WithCancelCause(relist)
			pair, done := driver.PairContextForTest(retries)
			defer done()
			capPassed(driver.ErrRelistCap)
			Expect(pair.Err()).NotTo(HaveOccurred(), "the pair's context once the cap has passed")
			backstop, _ := relist.Deadline()
			deadline, ok := pair.Deadline()
			Expect(ok).To(BeTrue(), "the pair's context has a deadline")
			Expect(deadline).To(Equal(backstop), "the relist's backstop")
		})

		It("bounds a relist up to its link at 512 s, sixteen times its cap, the figure the differences entry gives", func() {
			Expect(driver.RelistBackstop).To(Equal(16 * driver.RelistCap))
			Expect(driver.RelistBackstop).To(Equal(512 * time.Second))
		})

		// The relist's first link meets a reshard of the bucket's index that
		// lasts 70 reshard waits, longer than half the backstop: the link's
		// reshard guard waits up to 90, up to ten calls each followed by up to
		// ten reads of the shard's status reshardWait apart. The reshard wait
		// and the backstop, 102.4 waits, are both scaled by the same factor from
		// their defaults, and the backstop lets the link wait the reshard out:
		// the version is listed, no pending attr stays. A backstop of half that,
		// 51.2 waits, would end the relist before the reshard ends; load only
		// lengthens the link's wait.
		It("lists the refused version when its link waits out a reshard that lasts longer than half its backstop", func(ctx SpecContext) {
			const wait = 20 * time.Millisecond
			lasts := 70 * wait
			s.SetReshardWaitForTest(wait)
			driver.SetRelistBackstopForTest(s, driver.RelistBackstop/(driver.ReshardWait/wait))
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			reshard := func(status uint8) error {
				p, err := c.Pool(context.Background(), rookIndexPool, "")
				if err != nil {
					return err
				}
				w := radosclient.NewWriteOp()
				rgwcls.SetBucketResharding(w, rgwcls.InstanceEntry{ReshardStatus: status}, denc.Squid)
				_, err = p.Write(context.Background(), shard, w, radosclient.OpFlagNone)
				return err
			}
			var started time.Time
			ended := make(chan error, 1)
			before(relinks, 1, func() {
				Expect(reshard(rgwcls.ReshardInProgress)).To(Succeed())
				started = time.Now()
				time.AfterFunc(lasts, func() { ended <- reshard(rgwcls.ReshardNone) })
			})
			DeferCleanup(func() error { return reshard(rgwcls.ReshardNone) })
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			Expect(started).NotTo(BeZero(), "the reshard began at the relist's link")
			Expect(<-ended).To(Succeed(), "the reshard's end")
			Expect(time.Since(started)).To(BeNumerically(">=", lasts), "the link waited the reshard out")
			settle(s)
			Expect(logs.String()).NotTo(ContainSubstring("it stays unlisted"), "the relist's end, as logged")
			Expect(listed()).To(Equal([]string{"v2", "v1"}), "the versions listed")
			Expect(pending()).To(BeEmpty(), "the pending attrs on the OLH head")
			settledOn(ctx, "v2")
		})

		// A relist resets the version's instance record with a prepare and a
		// cancel. A first link lost to another writer starts the cap's clock,
		// and the cap then passes at the retry's prepare: the cancel is sent all
		// the same. Left without it, the prepare's pending tag stays on the
		// record, which nothing expires, and a delete of the version then
		// leaves the record behind, marked as not existing.
		DescribeTable("cancels a retry's reset though the cap passes at its prepare, leaving no pending tag on the version's record",
			func(ctx SpecContext, cut func(limit time.Duration)) {
				const limit = 20 * time.Millisecond
				driver.SetRelistCapForTest(s, limit)
				put(ctx, "v1")
				put(ctx, "v2")
				expect := head(ctx, "v1")
				at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
				before(relinks, 1, func() { put(ctx, "w01") })
				cut(limit)
				_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
				concurrent(err)
				settle(s)
				ie, ok := instanceEntry("v1")
				Expect(ok).To(BeTrue(), "its instance entry")
				Expect(ie.PendingMap).To(BeEmpty(), "the pending tags on its instance record")
				Expect(versionHead("v1")).NotTo(BeNil(), "stored")
				Expect(listed()).NotTo(ContainElement("v1"), "the relist ended at its cap")
				Expect(del(ctx, "v1", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
				settle(s)
				_, ok = instanceEntry("v1")
				Expect(ok).To(BeFalse(), "the record a delete of the version by its id leaves")
			},
			// The prepares of an ADD of v1 are the attr write's, the first try's
			// reset's, then the retry's.
			Entry("the cap passes between the prepare and the cancel", func(limit time.Duration) {
				at(prepares("v1", rgwcls.OpAdd), 3, func() { time.Sleep(5 * limit) })
			}),
			Entry("the cap abandons the prepare in flight, and it lands", func(limit time.Duration) {
				abandon(prepares("v1", rgwcls.OpAdd), 3, 5*limit)
			}),
		)

		// A link follows an init on the key's OLH head, which leaves a pending
		// attr there until the link's replay or a cancel removes it. The cap
		// passes between a retry's init and its link, and the link is sent all
		// the same, under its compare: the version is listed, and no pending
		// attr stays.
		DescribeTable("sends a retry's link though the cap passes after its init, leaving no pending attr on the head",
			func(ctx SpecContext, arm func(ctx context.Context, past time.Duration) (target string, want []string, current string)) {
				const limit = 20 * time.Millisecond
				driver.SetRelistCapForTest(s, limit)
				put(ctx, "v0")
				put(ctx, "v1")
				put(ctx, "v2")
				target, want, current := arm(ctx, 5*limit)
				expect := head(ctx, target)
				at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, target) })
				_, err := del(ctx, target, op.DeleteParams{Expect: expect})
				concurrent(err)
				settle(s)
				Expect(pending()).To(BeEmpty(), "the pending attrs on the OLH head")
				Expect(listed()).To(Equal(want), "the retry's link landed")
				settledOn(ctx, current)
			},
			// A writer's link refuses the first try's link, and the retry lists
			// the version below that writer's, at its own epoch.
			Entry("a link at the version's own epoch", func(ctx context.Context, past time.Duration) (string, []string, string) {
				before(relinks, 1, func() { put(ctx, "w01") })
				before(relinks, 2, func() { time.Sleep(past) })
				return "v1", []string{"w01", "v2", "v1", "v0"}, "w01"
			}),
			// The inits are the refused delete's unlink's, then the first try's,
			// which a delete of another version refuses by moving the head. The
			// retry then links the version current again, at epoch 0.
			Entry("a link at epoch 0", func(ctx context.Context, past time.Duration) (string, []string, string) {
				before(inits, 2, func() {
					Expect(del(ctx, "v0", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v0"}))
				})
				before(relinks, 1, func() { time.Sleep(past) })
				return "v2", []string{"v2", "v1"}, "v2"
			}),
		)

		// As above, and another writer's link lands in the same gap, so the
		// retry's link is refused by its compare: the cancel of its pending
		// modification is sent past the cap too.
		It("cancels a retry's pending modification when the cap passes after its init and its link is then refused", func(ctx SpecContext) {
			const limit = 20 * time.Millisecond
			driver.SetRelistCapForTest(s, limit)
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v1")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
			before(relinks, 1, func() { put(ctx, "w01") })
			before(relinks, 2, func() {
				put(ctx, "w02")
				time.Sleep(5 * limit)
			})
			_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(pending()).To(BeEmpty(), "the pending attrs on the OLH head")
			Expect(versionHead("v1")).NotTo(BeNil(), "stored")
			Expect(listed()).NotTo(ContainElement("v1"), "the relist ended at its cap")
			settledOn(ctx, "w02")
		})

		// The cap passes while a retry's init is in flight: the init lands, and
		// its call answers its context's end only if that context has ended,
		// as goceph's asynchronous completion modes answer. The init and its
		// link are one pair, sent on the context the cap does not end, so the
		// link follows, and no pending attr stays. The inits are the refused
		// delete's unlink's, the first try's, the writer's, then the retry's.
		It("sends a retry's init and its link though the cap passes while the init is in flight, leaving no pending attr", func(ctx SpecContext) {
			const limit = 20 * time.Millisecond
			driver.SetRelistCapForTest(s, limit)
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v1")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v1") })
			before(relinks, 1, func() { put(ctx, "w01") })
			var n atomic.Int32
			ac.setWriteCtx(func(ctx context.Context, w *radosclient.WriteOp, send sendFunc) (uint64, error) {
				if !inits(w) || n.Add(1) != 4 {
					return send()
				}
				ver, err := send()
				time.Sleep(5 * limit)
				if cerr := ctx.Err(); err == nil && cerr != nil {
					return 0, cerr
				}
				return ver, err
			})
			DeferCleanup(func() { ac.setWriteCtx(nil) })
			_, err := del(ctx, "v1", op.DeleteParams{Expect: expect})
			ac.setWriteCtx(nil)
			concurrent(err)
			settle(s)
			Expect(n.Load()).To(BeNumerically(">=", 4), "the retry's init was sent")
			Expect(pending()).To(BeEmpty(), "the pending attrs on the OLH head")
			Expect(listed()).To(Equal([]string{"w01", "v2", "v1"}), "the retry's link landed")
			settledOn(ctx, "w01")
		})

		// What a relist can still leave: the backstop passes while an init is
		// in flight, which lands. Its pair's link is held to the backstop too,
		// so nothing removes the pending attr until rgw_olh_pending_timeout_sec
		// expires it, as a writer's that stops between its init and its link.
		// The inits are the refused delete's unlink's, then the relist's.
		It("leaves the pending attr of an init the backstop cuts in flight, until it expires", func(ctx SpecContext) {
			const backstop = 50 * time.Millisecond
			driver.SetRelistBackstopForTest(s, backstop)
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			var n atomic.Int32
			ac.setWriteCtx(func(ctx context.Context, w *radosclient.WriteOp, send sendFunc) (uint64, error) {
				if !inits(w) || n.Add(1) != 2 {
					return send()
				}
				ver, err := send()
				if err != nil {
					return ver, err
				}
				select {
				case <-ctx.Done():
					return 0, ctx.Err()
				case <-time.After(10 * time.Second):
					return 0, errors.New("the stalled init outlived its spec")
				}
			})
			DeferCleanup(func() { ac.setWriteCtx(nil) })
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			ac.setWriteCtx(nil)
			concurrent(err)
			settle(s)
			Expect(pending()).To(HaveLen(1), "the cut init's pending attr")
			Expect(listed()).To(Equal([]string{"v1"}), "the relist ended there")
			clock.Add(int64(2 * time.Hour))
			st, err := s.StatObject(ctx, rec, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Key.Instance).To(Equal("v1"), "the key's current version, read by following its OLH")
			Expect(pending()).To(BeEmpty(), "expired, and removed by the read's follow")
		})

		// A writer inits and stalls before its link, and the relist's rounds
		// wait on its pending modification until their wait has passed, after
		// which that pending attr no longer refuses the relist's link. The
		// writer's link then lands between a round's read of the listing and
		// its read of the OLH entry, so the entry the relist's link compares
		// already holds it: the listing read after that entry shows the
		// writer's version, the round is lost, and the next lists v2 below it.
		// A relist that linked there would put v2 current over a write its
		// client is then told succeeded.
		It("lists the refused version below a writer's version whose link lands between a round's read of the listing and its read of the OLH entry", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() { attrWrite(ctx, "v2") })
			w := newRGWWriter(c, s, rec, denc.Squid, "w")
			linked := false
			at(calls("bucket_trim_olh_log"), 1, func() {
				w.Prepare(ctx)
				w.HeadWrite(ctx)
				w.Complete(ctx)
				w.ReadHead(ctx)
				Expect(w.Init(ctx)).To(BeTrue(), "the writer's modification is pending")
				afterListing(func() {
					w.Link(ctx)
					linked = true
				})
			})
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			Expect(linked).To(BeTrue(), "the writer linked between the relist's two reads")
			w.ReadLog(ctx)
			w.Apply(ctx)
			w.Trim(ctx)
			settle(s)
			Expect(listed()).To(Equal([]string{"w", "v2", "v1"}), "the writer's version current, v2 listed below it")
			Expect(listEntry("v2").Flags).To(Equal(rgwcls.FlagVer), "v2 non-current")
			settledOn(ctx, "w")
		})

		// A link of the version logged after the delete's removal makes the
		// replay drop that removal: the delete removed nothing, and while a
		// lock holds the kept version it must not answer success.
		DescribeTable("a delete whose removal a later link of the version kept answers from the version's lock",
			func(ctx SpecContext, hold bool) {
				put(ctx, "v1")
				put(ctx, "v2")
				expect := head(ctx, "v2")
				at(calls("bucket_unlink_instance"), 1, func() {
					if hold {
						holdWrite(ctx, "v2")
					} else {
						attrWrite(ctx, "v2")
					}
					Expect(s.SetOLHForTest(ctx, rec, version("v2"), driver.OLHLink{KeepVersion: true, SkipUpdate: true})).To(Succeed(), "a link of v2, as a completion's retry sends")
				})
				res, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
				settle(s)
				Expect(versionHead("v2")).NotTo(BeNil(), "the version is kept")
				Expect(listed()).To(ContainElement("v2"))
				if hold {
					concurrent(err)
					Expect(versionHead("v2").Xattrs).To(HaveKey(meta.AttrObjectLegalHold))
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "v2"}))
			},
			Entry("under a legal hold: ConcurrentModification, which the lock check after it answers", true),
			Entry("with no lock: success, as for the version a later write lists again", false),
		)

		// Another replay that read the delete's removal keeps the version for
		// its hold and leaves the removal to the delete, and a link lists the
		// version again before the delete's own replay finds its entries gone
		// (checkRemoval).
		It("answers ConcurrentModification, not success, when another replay kept the version for its legal hold and a link listed it", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			expect := head(ctx, "v2")
			at(calls("bucket_unlink_instance"), 1, func() {
				holdWrite(ctx, "v2")
				Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "another reader's replay")
				Expect(versionHead("v2")).NotTo(BeNil(), "kept for its hold")
				Expect(s.SetOLHForTest(ctx, rec, version("v2"), driver.OLHLink{KeepVersion: true})).To(Succeed())
			})
			_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(versionHead("v2")).NotTo(BeNil())
			Expect(versionHead("v2").Xattrs).To(HaveKey(meta.AttrObjectLegalHold))
			settledOn(ctx, "v2")
		})

		// A PutObject's replay reads the log while it holds the guarded
		// delete's removal of v2. The delete's own replay refuses that removal
		// and trims it, and its relist lists v2 again before the PutObject's
		// replay gets to the removal, which then finds v2 listed and keeps it.
		// With the PutObject's v3 listed above v2 the relist links v2 below
		// it, at v2's own epoch; with v2 the PutObject's own version, nothing
		// is listed above it and the relist links it current at a new epoch.
		DescribeTable("keeps the refused version another request's replay read the removal of, once its relist lists it",
			func(ctx SpecContext, r denc.Release, newer bool) {
				open(ctx, r)
				put(ctx, "v1")
				written := "v2"
				if newer {
					put(ctx, "v2")
					written = "v3"
				}
				linked, resume, atRemoval, removeNow := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				resumePut, removeAfter := sync.OnceFunc(func() { close(resume) }), sync.OnceFunc(func() { close(removeNow) })
				DeferCleanup(removeAfter)
				DeferCleanup(resumePut)
				putDone := make(chan error, 1)
				at(calls("bucket_link_olh"), 1, func() {
					close(linked)
					<-resume
				})
				go func() {
					defer GinkgoRecover()
					_, err := s.PutObject(ctx, rec, version(written), strings.NewReader("data-"+written), op.PutParams{Attrs: attrs, Size: int64(len("data-" + written)), Tag: "tag-" + written})
					putDone <- err
				}()
				Eventually(linked).WithTimeout(10*time.Second).Should(BeClosed(), "the PutObject's link, its replay still to run")
				expect := head(ctx, "v2")
				at(calls("bucket_unlink_instance"), 1, func() {
					attrWrite(ctx, "v2")
					oid := instanceHeadOID(version("v2"))
					c.BeforeRead(testDataPool, "", oid, func() {
						c.BeforeRead(testDataPool, "", oid, nil)
						close(atRemoval)
						<-removeNow
					})
					resumePut()
					Eventually(atRemoval).WithTimeout(10*time.Second).Should(BeClosed(), "the PutObject's replay, about to remove v2")
				})
				_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
				concurrent(err)
				Expect(err).To(MatchError(driver.ErrRemovalRefused), "the delete's own replay refused the removal")
				Expect(listed()).To(ContainElement("v2"), "the relist listed v2 again")
				if newer {
					Expect(listEntry("v2").Flags).To(Equal(rgwcls.FlagVer), "below v3, at its own epoch")
				} else {
					Expect(listEntry("v2").Flags&rgwcls.FlagCurrent).To(Equal(rgwcls.FlagCurrent), "current again at a new epoch")
				}

				removeAfter()
				Eventually(putDone).WithTimeout(10*time.Second).Should(Receive(BeNil()), "the PutObject")
				settle(s)
				Expect(versionHead("v2")).NotTo(BeNil(), "the removal the PutObject's replay read found v2 listed again")
				_, ok := instanceEntry("v2")
				Expect(ok).To(BeTrue())
				Expect(listing(ctx, true)).To(ContainElement("k@v2"), "ListObjectVersions lists it")
				if newer {
					settledOn(ctx, "v3")
					return
				}
				settledOn(ctx, "v2")
			},
			Entry("v3 listed above it, on squid", denc.Squid, true),
			Entry("v3 listed above it, on tentacle", denc.Tentacle, true),
			Entry("nothing listed above it, on squid", denc.Squid, false),
			Entry("nothing listed above it, on tentacle", denc.Tentacle, false),
		)

		// removalPrepareOf takes a replay's prepare of x's removal.
		removalPrepareOf := func(x string) func(w *radosclient.WriteOp) bool {
			return func(w *radosclient.WriteOp) bool {
				k, ok := removalPrepareKey(w)
				return ok && k == x
			}
		}

		// Another request's replay reads a guarded delete's removal of x; its
		// first prepare of that removal is refused, another writer's prepare
		// having changed x's instance entry, and the delete's own replay then
		// refuses the removal and trims it. The other replay's removal has left
		// the log, so it stops there: a second prepare would land after the
		// relist's give-way found the log without it and before its link, and
		// its removal would leave the relist's list entry naming nothing. The
		// shapes are the harness's H4:220, a reader of the key, and H10:1547,
		// a suspended bucket's null delete marker over a refused delete of v1.
		DescribeTable("stops a replay's removal that a refused prepare would send round again once the removal has left the log",
			func(ctx SpecContext, r denc.Release, nullMarker bool) {
				open(ctx, r)
				x := "v2"
				var other func(ctx context.Context) error
				if nullMarker {
					suspend()
					nullPut(ctx, "tag-null", "null")
					rec.Info.Flags &^= meta.BucketVersionsSuspended
					put(ctx, "v1")
					suspend()
					x = "v1"
					other = func(ctx context.Context) error {
						_, err := del(ctx, "", op.DeleteParams{})
						return err
					}
				} else {
					put(ctx, "v1")
					put(ctx, "v2")
					other = func(ctx context.Context) error { return s.UpdateOLHForTest(ctx, rec, head(ctx, "")) }
				}
				expect := head(ctx, x)
				removalPrepare := removalPrepareOf(x)
				changer := s.NewIndexOpForTest(rec, version(x), "changer")
				parked1, go1, parked2, go2 := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				done, finished := make(chan error, 1), make(chan struct{})
				letGo1, letGo2 := sync.OnceFunc(func() { close(go1) }), sync.OnceFunc(func() { close(go2) })
				DeferCleanup(letGo2)
				DeferCleanup(letGo1)
				before(removalPrepare, 1, func() {
					close(parked1)
					<-go1
					Expect(changer.Prepare(ctx, rgwcls.OpAdd)).To(Succeed(), "another writer's prepare, which changes x's instance entry")
				})
				at(removalPrepare, 2, func() {
					close(parked2)
					<-go2
				})
				at(calls("bucket_unlink_instance"), 1, func() {
					attrWrite(ctx, x)
					at(calls("bucket_trim_olh_log"), 1, func() {
						letGo1()
						select {
						case <-parked2:
						case <-finished:
						case <-time.After(10 * time.Second):
						}
					})
					go func() {
						defer GinkgoRecover()
						done <- other(ctx)
						close(finished)
					}()
					Eventually(parked1).WithTimeout(10*time.Second).Should(BeClosed(), "the other replay, about to send its first prepare of x's removal")
				})
				_, err := del(ctx, x, op.DeleteParams{Expect: expect})
				concurrent(err)
				letGo2()
				Eventually(done).WithTimeout(10 * time.Second).Should(Receive())
				changer.Cancel()
				settle(s)
				Expect(versionHead(x)).NotTo(BeNil(), "x, which no delete was acknowledged for")
				_, ok := instanceEntry(x)
				Expect(ok).To(BeTrue())
				Expect(listing(ctx, true)).To(ContainElement("k@"+x), "ListObjectVersions lists it")
				if nullMarker {
					nullPut(ctx, "tag-later", "later")
					Expect(olhInfo().Target.Key.Instance).To(BeEmpty(), "a later write of the key links")
					return
				}
				_, err = s.PutObject(ctx, rec, version("v9"), strings.NewReader("data-v9"), op.PutParams{Attrs: attrs, Size: 7, Tag: "tag-v9"})
				Expect(err).NotTo(HaveOccurred(), "a later write of the key links")
				settledOn(ctx, "v9")
			},
			Entry("a reader's replay, on squid", denc.Squid, false),
			Entry("a reader's replay, on tentacle", denc.Tentacle, false),
			Entry("a null delete marker's replay, on squid", denc.Squid, true),
			Entry("a null delete marker's replay, on tentacle", denc.Tentacle, true),
		)

		// The harness's H1 writers=2 seed 626: another request's replay of a
		// guarded delete's removal of v2 has its first prepare refused, another
		// writer's prepare having changed v2's instance entry, and backs off.
		// During the backoff the delete's own replay refuses the removal and
		// trims it. The log is read again after the backoff, just before the
		// next round's head read, so the other replay stops there: a log read
		// before the backoff still held the removal, and its second prepare
		// would land before the relist's link and remove v2 from under it.
		DescribeTable("stops a replay's removal whose removal left the log during its backoff",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				put(ctx, "v1")
				put(ctx, "v2")
				expect := head(ctx, "v2")
				removalPrepare := removalPrepareOf("v2")
				changer := s.NewIndexOpForTest(rec, version("v2"), "changer")
				inBackoff, trimmed, parked2, go2 := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				done, finished := make(chan error, 1), make(chan struct{})
				letGo2, letTrimmed := sync.OnceFunc(func() { close(go2) }), sync.OnceFunc(func() { close(trimmed) })
				DeferCleanup(letGo2)
				DeferCleanup(letTrimmed)
				var sleeps atomic.Int32
				driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
					if sleeps.Add(1) == 1 {
						close(inBackoff)
						<-trimmed
					}
					return nil
				})
				before(removalPrepare, 1, func() {
					Expect(changer.Prepare(ctx, rgwcls.OpAdd)).To(Succeed(), "another writer's prepare, which changes v2's instance entry")
				})
				at(removalPrepare, 2, func() {
					close(parked2)
					<-go2
				})
				at(calls("bucket_unlink_instance"), 1, func() {
					attrWrite(ctx, "v2")
					at(calls("bucket_trim_olh_log"), 1, func() {
						letTrimmed()
						select {
						case <-parked2:
						case <-finished:
						case <-time.After(10 * time.Second):
						}
					})
					go func() {
						defer GinkgoRecover()
						done <- s.UpdateOLHForTest(ctx, rec, head(ctx, ""))
						close(finished)
					}()
					Eventually(inBackoff).WithTimeout(10*time.Second).Should(BeClosed(), "the other replay, its first prepare refused, in its backoff")
				})
				_, err := del(ctx, "v2", op.DeleteParams{Expect: expect})
				concurrent(err)
				letGo2()
				Eventually(done).WithTimeout(10 * time.Second).Should(Receive())
				changer.Cancel()
				settle(s)
				Expect(versionHead("v2")).NotTo(BeNil(), "v2, which no delete was acknowledged for")
				_, ok := instanceEntry("v2")
				Expect(ok).To(BeTrue())
				Expect(listing(ctx, true)).To(ContainElement("k@v2"), "ListObjectVersions lists it")
				_, err = s.PutObject(ctx, rec, version("v9"), strings.NewReader("data-v9"), op.PutParams{Attrs: attrs, Size: 7, Tag: "tag-v9"})
				Expect(err).NotTo(HaveOccurred(), "a later write of the key links")
				settledOn(ctx, "v9")
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)

		// The harness's H1 writers=2 seed 668 and H10 seeds 392 and 1441:
		// another request's replay reads two removals, first's and then a
		// guarded delete's of x. Its prepare of first's removal is refused,
		// another writer's prepare having changed first's index entry, and it
		// backs off. Meanwhile the delete's own replay removes first, refuses
		// its own removal of x and trims the log. After the backoff the other
		// replay finds first's removal gone from the log, and its pass ends
		// there: it reads the log again, finds it empty, and never prepares
		// x's removal from the read the trim overtook, which would land
		// before the relist's link and remove x from under it.
		DescribeTable("ends a replay's pass once one of its removals has left the log, sending nothing more from that read",
			func(ctx SpecContext, r denc.Release, null bool) {
				open(ctx, r)
				first, x := "v2", "v3"
				if null {
					suspend()
					nullPut(ctx, "tag-null", "null")
					rec.Info.Flags &^= meta.BucketVersionsSuspended
					put(ctx, "v1")
					suspend()
					first, x = "", "v1"
				} else {
					put(ctx, "v1")
					put(ctx, "v2")
					put(ctx, "v3")
				}
				Expect(del(ctx, first, op.DeleteParams{SkipOLHUpdate: true})).Error().To(Succeed(), "first's removal logged, its replay left")
				expect := head(ctx, x)
				changer := s.NewIndexOpForTest(rec, version(first), "changer")
				inBackoff, trimmed, parked2, go2 := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				done, finished := make(chan error, 1), make(chan struct{})
				letGo2, letTrimmed := sync.OnceFunc(func() { close(go2) }), sync.OnceFunc(func() { close(trimmed) })
				DeferCleanup(letGo2)
				DeferCleanup(letTrimmed)
				var sleeps atomic.Int32
				driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
					if sleeps.Add(1) == 1 {
						close(inBackoff)
						<-trimmed
					}
					return nil
				})
				before(removalPrepareOf(first), 1, func() {
					Expect(changer.Prepare(ctx, rgwcls.OpAdd)).To(Succeed(), "another writer's prepare, which changes first's index entry")
				})
				at(removalPrepareOf(x), 1, func() {
					close(parked2)
					<-go2
				})
				at(calls("bucket_unlink_instance"), 1, func() {
					attrWrite(ctx, x)
					at(calls("bucket_trim_olh_log"), 1, func() {
						letTrimmed()
						select {
						case <-parked2:
						case <-finished:
						case <-time.After(10 * time.Second):
						}
					})
					go func() {
						defer GinkgoRecover()
						done <- s.UpdateOLHForTest(ctx, rec, head(ctx, ""))
						close(finished)
					}()
					Eventually(inBackoff).WithTimeout(10*time.Second).Should(BeClosed(), "the other replay, its prepare of first's removal refused, in its backoff")
				})
				_, err := del(ctx, x, op.DeleteParams{Expect: expect})
				concurrent(err)
				letGo2()
				Eventually(done).WithTimeout(10 * time.Second).Should(Receive())
				changer.Cancel()
				settle(s)
				Expect(versionHead(x)).NotTo(BeNil(), "x, which no delete was acknowledged for")
				_, ok := instanceEntry(x)
				Expect(ok).To(BeTrue())
				Expect(listing(ctx, true)).To(ContainElement("k@"+x), "ListObjectVersions lists it")
				if null {
					nullPut(ctx, "tag-later", "later")
					Expect(olhInfo().Target.Key.Instance).To(BeEmpty(), "a later write of the key links")
					return
				}
				Expect(versionHead(first)).To(BeNil(), "first, removed by the delete's replay")
				_, err = s.PutObject(ctx, rec, version("v9"), strings.NewReader("data-v9"), op.PutParams{Attrs: attrs, Size: 7, Tag: "tag-v9"})
				Expect(err).NotTo(HaveOccurred(), "a later write of the key links")
				settledOn(ctx, "v9")
			},
			Entry("a named version first (H1 writers=2:668), on squid", denc.Squid, false),
			Entry("a named version first (H1 writers=2:668), on tentacle", denc.Tentacle, false),
			Entry("the null version first (H10:392, :1441), on squid", denc.Squid, true),
			Entry("the null version first (H10:392, :1441), on tentacle", denc.Tentacle, true),
		)

		// An attribute write's head write lands; then a delete unlinks v2 and
		// two replays of its removal run, both prepared before either removes
		// the head; then the attribute write's index completion lands. The
		// second removal finds the head gone and completes at the version its
		// ENOENT reply carries, at or above the first's, so the attribute
		// write's completion, at its head write's lower version, is a cancel:
		// nothing of v2 is left. Completed at 0, the class would apply it and
		// make v2's instance entry again with no head, counted.
		DescribeTable("leaves nothing of a version an attribute write rewrote before two replays of its removal",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				put(ctx, "v1")
				put(ctx, "v2")
				v2Head := instanceHeadOID(version("v2"))
				replay := func(ctx context.Context) error { return s.UpdateOLHForTest(ctx, rec, head(ctx, "")) }
				var replayed bool
				c.AfterWrite(testDataPool, "", v2Head, func() {
					defer GinkgoRecover()
					c.AfterWrite(testDataPool, "", v2Head, nil)
					written := c.Object(testDataPool, "", v2Head).Version
					Expect(del(ctx, "v2", op.DeleteParams{SkipOLHUpdate: true})).Error().To(Succeed(), "the delete, its replay left")
					// The first replay to reach the head's removal waits there
					// until the second has removed the head and completed.
					parked, release := make(chan struct{}), make(chan struct{})
					var removes atomic.Int32
					c.BeforeWrite(testDataPool, "", v2Head, func(*fakerados.Object) {
						if removes.Add(1) == 1 {
							close(parked)
							<-release
						}
					})
					first := make(chan error, 1)
					go func() {
						defer GinkgoRecover()
						first <- replay(ctx)
					}()
					Eventually(parked).WithTimeout(10*time.Second).Should(BeClosed(), "the first replay, prepared and about to remove the head")
					Expect(replay(ctx)).To(Succeed(), "the second replay, which removes the head")
					close(release)
					Eventually(first).WithTimeout(10*time.Second).Should(Receive(Succeed()), "the first replay, which meets the head gone")
					c.BeforeWrite(testDataPool, "", v2Head, nil)
					en, ok := instanceEntry("v2")
					Expect(ok).To(BeTrue(), "the DEL completions write the entry back while the attribute write's tag is pending")
					Expect(en.Exists).To(BeFalse())
					Expect(en.Ver.Epoch).To(BeNumerically(">", written), "above the attribute write's head write, whose completion is then stale")
					replayed = true
				})
				policy := encode(acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: "carol"}), "Carol"))
				Expect(s.SetObjectAttrs(ctx, head(ctx, "v2"), map[string][]byte{meta.AttrACL: policy}, nil)).To(Succeed(), "its head write landed")
				settle(s)
				Expect(replayed).To(BeTrue())
				Expect(versionHead("v2")).To(BeNil())
				_, ok := instanceEntry("v2")
				Expect(ok).To(BeFalse(), "no instance entry of v2 left without its head")
				Expect(listed()).NotTo(ContainElement("v2"))
				settledOn(ctx, "v1")
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)

		// Another writer changes the version's instance entry between each of
		// the delete's reads of it and the prepare it compares it in. The
		// removal backs off between its rounds and, at its bound, leaves the
		// log to the next replay; the delete, whose unlink has landed, answers
		// as radosgw's does.
		It("answers a delete whose removal another writer keeps refusing, leaving the removal in the log for the next replay", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			var changers []*driver.IndexOp
			var sleeps atomic.Int32
			driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
				sleeps.Add(1)
				return nil
			})
			ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
				if callsMethod(w.Steps(), "bucket_prepare_op") && slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
					_, ok := st.(*radosclient.OmapCmpStep)
					return ok
				}) {
					x := s.NewIndexOpForTest(rec, version("v2"), fmt.Sprintf("changer-%d", len(changers)))
					changers = append(changers, x)
					Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
				}
				return hooks.around(w, send)
			})
			res, err := del(ctx, "v2", op.DeleteParams{})
			ac.set(hooks.around)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "v2"}))
			Expect(changers).To(HaveLen(driver.OLHAttempts), "one refused prepare a round, up to the bound")
			Expect(sleeps.Load()).To(BeEquivalentTo(driver.OLHAttempts-1), "a backoff after each lost round but the last")
			Expect(versionHead("v2")).NotTo(BeNil(), "nothing removed yet")
			Expect(olhLog()).To(ContainElement(ContainElement(SatisfyAll(
				HaveField("Op", rgwcls.OLHOpRemoveInstance), HaveField("Key.Instance", "v2")))), "the removal stays in the log")
			for _, x := range changers {
				x.Cancel()
			}
			settle(s)
			Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "the next replay")
			Expect(versionHead("v2")).To(BeNil(), "removed by the next replay")
			settledOn(ctx, "v1")
		})

		// A delete's replay meets two removals that other writers keep
		// refusing: v2's, another delete's left to the next replay, refused for
		// 20 rounds, and then the delete's own removal of v3, refused on every
		// round. Each backoff takes a second. The two share the delete's one
		// deadline, 32 s after v2's first lost round, so v3's removal is
		// deferred 14 rounds in, after 33 backoffs in all, and the delete
		// answers as radosgw's does. With a deadline of its own, v3's removal
		// would back off for 32 s more.
		It("shares one backoff deadline among the removals of a delete's replay, deferring once 32 s have passed", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			put(ctx, "v3")
			Expect(del(ctx, "v2", op.DeleteParams{SkipOLHUpdate: true})).Error().To(Succeed(), "v2's removal logged, its replay left")
			var sleeps atomic.Int32
			driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
				sleeps.Add(1)
				clock.Add(int64(time.Second))
				return nil
			})
			refused := map[string]int{}
			var changers []*driver.IndexOp
			ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
				if x, ok := removalPrepareKey(w); ok && (x == "v3" || x == "v2" && refused["v2"] < 20) {
					refused[x]++
					ch := s.NewIndexOpForTest(rec, version(x), fmt.Sprintf("changer-%d", len(changers)))
					changers = append(changers, ch)
					Expect(ch.Prepare(ctx, rgwcls.OpAdd)).To(Succeed(), "another writer's prepare, which changes %s's instance entry", x)
				}
				return hooks.around(w, send)
			})
			res, err := del(ctx, "v3", op.DeleteParams{})
			ac.set(hooks.around)
			Expect(err).NotTo(HaveOccurred())
			Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "v3"}))
			Expect(refused).To(Equal(map[string]int{"v2": 20, "v3": 14}), "v3's last refused round, past the deadline, defers it")
			Expect(sleeps.Load()).To(BeEquivalentTo(33), "a backoff after every lost round but the deferred one")
			Expect(versionHead("v2")).To(BeNil(), "v2, removed on its 21st round")
			Expect(versionHead("v3")).NotTo(BeNil(), "v3, its removal deferred")
			Expect(olhLog()).To(ContainElement(ContainElement(SatisfyAll(
				HaveField("Op", rgwcls.OLHOpRemoveInstance), HaveField("Key.Instance", "v3")))), "v3's removal stays in the log")
			for _, x := range changers {
				x.Cancel()
			}
			settle(s)
			Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "the next replay")
			Expect(versionHead("v3")).To(BeNil(), "removed by the next replay")
			settledOn(ctx, "v1")
		})

		// A reader's replay meets, pass after pass, a removal another writer's
		// prepare refuses once, which another replay carries out and trims
		// during the reader's backoff, the next removal logged before the
		// reader reads the log again. Each of those passes ends there, the log
		// it read overtaken (errRemovalLeftLog). A reader whose replay runs out
		// of passes answers SlowDown, which a client retries, never NoSuchKey:
		// the key's current version is whole all along. With a pass to spare,
		// it answers that version.
		DescribeTable("answers a read whose replay other replays overtake on every pass SlowDown, never NoSuchKey",
			func(ctx SpecContext, r denc.Release, removals int) {
				open(ctx, r)
				put(ctx, "v1")
				var xs []string
				for i := range removals {
					x := fmt.Sprintf("x%02d", i)
					put(ctx, x)
					xs = append(xs, x)
				}
				put(ctx, "vtop")
				Expect(del(ctx, xs[0], op.DeleteParams{SkipOLHUpdate: true})).Error().To(Succeed(), "the first removal logged, its replay left")
				var other atomic.Bool
				next := 0
				driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
					other.Store(true)
					defer other.Store(false)
					Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "another replay, which carries out %s's removal and trims it", xs[next])
					if next++; next < len(xs) {
						Expect(del(ctx, xs[next], op.DeleteParams{SkipOLHUpdate: true})).Error().To(Succeed(), "the next removal logged")
					}
					return nil
				})
				var changers []*driver.IndexOp
				ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
					if x, ok := removalPrepareKey(w); ok && !other.Load() {
						ch := s.NewIndexOpForTest(rec, version(x), fmt.Sprintf("changer-%d", len(changers)))
						changers = append(changers, ch)
						Expect(ch.Prepare(ctx, rgwcls.OpAdd)).To(Succeed(), "another writer's prepare, which changes %s's instance entry", x)
					}
					return hooks.around(w, send)
				})
				st, err := s.StatObject(ctx, rec, version(""))
				ac.set(hooks.around)
				Expect(changers).To(HaveLen(removals), "one refused prepare a pass")
				Expect(next).To(Equal(removals), "every removal carried out by the other replays")
				for _, x := range changers {
					x.Cancel()
				}
				settle(s)
				if removals > driver.OLHReplayRetries {
					Expect(err).To(MatchError(op.ErrSlowDown))
					Expect(op.AsError(err).Status).To(Equal(http.StatusServiceUnavailable))
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(st.Exists).To(BeTrue())
					Expect(st.Key.Instance).To(Equal("vtop"))
				}
				Expect(versionHead("vtop")).NotTo(BeNil(), "the current version's head")
				settledOn(ctx, "vtop")
				st, err = s.StatObject(ctx, rec, version(""))
				Expect(err).NotTo(HaveOccurred(), "a later read")
				Expect(st.Key.Instance).To(Equal("vtop"))
			},
			Entry("a pass to spare, on squid", denc.Squid, driver.OLHReplayRetries),
			Entry("a pass to spare, on tentacle", denc.Tentacle, driver.OLHReplayRetries),
			Entry("out of passes, on squid", denc.Squid, driver.OLHReplayRetries+1),
			Entry("out of passes, on tentacle", denc.Tentacle, driver.OLHReplayRetries+1),
		)
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

		// The conditions passed on the null version's entry, and the key's head
		// held its data when the delete read it; a null write landing after that
		// read replaces the head, so the unlink must not take what it finds.
		It("answers a conditional delete ConcurrentModification when a null write lands between its read of the head and its unlink, keeping the write", func(ctx SpecContext) {
			put(ctx, "v1")
			suspend()
			nullPut(ctx, "tag-old", "old")
			before(inits, 1, func() { nullPut(ctx, "tag-new", "new") })
			_, err := del(ctx, "null", op.DeleteParams{IfMatch: md5hex("old")})
			concurrent(err)
			settle(s)
			Expect(olhHead().Data).To(Equal([]byte("new")), "the new null version's data")
			_, ok := instanceEntry("")
			Expect(ok).To(BeTrue(), "its instance entry")
			Expect(listed()).To(Equal([]string{"", "v1"}))
			settledOn(ctx, "")
		})

		// A null write that has landed its head and not yet its index
		// completion leaves the entry the previous null version's while the head
		// shows the write's data. A delete's conditions passed on the entry, so
		// the head must still show what they judged: a write of the same bytes
		// has the ETag the entry has, and only its mtime tells it from the
		// version the condition checked. The write's init stands on the head
		// from its head write, which is what marks it in flight (headShows).
		It("answers a conditional delete by last-modified time ConcurrentModification when the same bytes were written again, keeping the write", func(ctx SpecContext) {
			put(ctx, "v1")
			suspend()
			nullPut(ctx, "tag-old", "old")
			settle(s)
			judged := listEntry("").Meta.Mtime
			clock.Add(int64(time.Hour))
			var (
				res  *op.DeleteObjectResult
				derr error
				ran  bool
			)
			at(func(w *radosclient.WriteOp) bool { return initsOLHSteps(w.Steps()) && writesData(w) }, 1, func() {
				ran = true
				Expect(pending()).NotTo(BeEmpty(), "the write's init stands on the head")
				res, derr = del(ctx, "null", op.DeleteParams{IfMatchLastModified: judged})
			})
			nullPut(ctx, "tag-new", "old")
			Expect(ran).To(BeTrue(), "the delete ran after the write's head write")
			Expect(res).To(BeNil())
			concurrent(derr)
			settle(s)
			Expect(olhHead().Data).To(Equal([]byte("old")), "the new null version's data")
			en, ok := instanceEntry("")
			Expect(ok).To(BeTrue(), "its instance entry")
			Expect(en.Meta.Mtime).To(BeTemporally(">", judged), "the write's, not the one the condition judged")
			Expect(listed()).To(Equal([]string{"", "v1"}))
			settledOn(ctx, "")
		})

		// radosgw's cancel of a modification and its expiry of a pending attr are
		// removals that set no mtime, so the head takes the client's clock while
		// the entry, which the delete's conditions and radosgw's listing judge,
		// keeps the write's. With no pending attr standing nothing is in flight,
		// and the listing's last-modified time deletes the version.
		It("passes a conditional delete by last-modified time for a head whose mtime radosgw moved", func(ctx SpecContext) {
			put(ctx, "v1")
			suspend()
			nullPut(ctx, "tag-old", "old")
			settle(s)
			judged := listEntry("").Meta.Mtime
			clock.Add(int64(time.Hour))
			pool, err := c.Pool(ctx, testDataPool, "")
			Expect(err).NotTo(HaveOccurred())
			attr := meta.AttrOLHPendingPrefix + "canceled-by-radosgw"
			w := radosclient.NewWriteOp()
			w.SetXattr(attr, []byte("x"))
			_, err = pool.Write(ctx, olhOID, w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			w = radosclient.NewWriteOp()
			w.RmXattr(attr)
			_, err = pool.Write(ctx, olhOID, w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			Expect(pending()).To(BeEmpty(), "no write in flight")
			Expect(head(ctx, "").Mtime).To(BeTemporally(">", judged), "the head's mtime moved")
			Expect(del(ctx, "null", op.DeleteParams{IfMatchLastModified: judged})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
			settle(s)
			Expect(olhHead().Data).To(BeEmpty(), "the null version's data is gone")
			_, ok := instanceEntry("")
			Expect(ok).To(BeFalse(), "its instance entry")
			Expect(listed()).To(Equal([]string{"v1"}))
			settledOn(ctx, "v1")
		})

		// A pending attr that radosgw's removal of another attr left behind, or
		// whose writer died, expires after rgw_olh_pending_timeout_sec, an hour,
		// and an expired one marks no write in flight: the check that reads the
		// head's pending attrs counts it as none (checkPendingOLHEntries).
		It("passes a conditional delete by last-modified time for a head whose mtime moved under an expired pending attr", func(ctx SpecContext) {
			put(ctx, "v1")
			suspend()
			nullPut(ctx, "tag-old", "old")
			settle(s)
			judged := listEntry("").Meta.Mtime
			clock.Add(int64(2 * time.Hour))
			pool, err := c.Pool(ctx, testDataPool, "")
			Expect(err).NotTo(HaveOccurred())
			w := radosclient.NewWriteOp()
			w.SetXattr(meta.AttrOLHPendingPrefix+meta.NewOLHOpTag(judged), encode(meta.OLHPendingInfo{Time: judged}))
			_, err = pool.Write(ctx, olhOID, w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			Expect(pending()).To(HaveLen(1), "the pending attr stands, two hours old")
			Expect(head(ctx, "").Mtime).To(BeTemporally(">", judged), "the head's mtime moved")
			Expect(del(ctx, "null", op.DeleteParams{IfMatchLastModified: judged})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
			settle(s)
			Expect(olhHead().Data).To(BeEmpty(), "the null version's data is gone")
			_, ok := instanceEntry("")
			Expect(ok).To(BeFalse(), "its instance entry")
			Expect(listed()).To(Equal([]string{"v1"}))
		})

		// Only the size differs between the head and the entry here, which no
		// writer produces under one ETag, so the head is written under it.
		It("answers a conditional delete by size ConcurrentModification when the head holds another size under the entry's ETag", func(ctx SpecContext) {
			put(ctx, "v1")
			suspend()
			put(ctx, "")
			settle(s)
			p, err := c.Pool(ctx, testDataPool, "")
			Expect(err).NotTo(HaveOccurred())
			w := radosclient.NewWriteOp()
			w.WriteFull([]byte("data-longer"))
			w.SetXattr(meta.AttrIDTag, []byte("idtag-other"))
			w.SetXattr(meta.AttrETag, []byte("etag-"))
			_, err = p.Write(ctx, olhOID, w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			_, err = del(ctx, "null", op.DeleteParams{IfMatchSize: new(versionMeta(version("")).Size)})
			concurrent(err)
			settle(s)
			Expect(olhHead().Data).To(Equal([]byte("data-longer")))
			Expect(listed()).To(Equal([]string{"", "v1"}))
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

	Describe("the delete marker of an enabled bucket", func() {
		It("links while the expected version is current under its write tag", func(ctx SpecContext) {
			put(ctx, "v1")
			res, err := del(ctx, "", op.DeleteParams{Expect: head(ctx, "v1")})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.DeleteMarker).To(BeTrue())
			Expect(currents()).To(Equal([]string{res.VersionID}))
		})

		It("links over the null version current while the OLH head, which holds its data, carries the expected write tag", func(ctx SpecContext) {
			suspend()
			nullPut(ctx, "tag-old", "old")
			rec.Info.Flags &^= meta.BucketVersionsSuspended
			res, err := del(ctx, "", op.DeleteParams{Expect: head(ctx, "")})
			Expect(err).NotTo(HaveOccurred())
			Expect(currents()).To(Equal([]string{res.VersionID}))
			Expect(olhHead().Data).To(Equal([]byte("old")), "a marker on an enabled bucket keeps the null version")
		})

		DescribeTable("refuses, linking no marker, when the version current is not the one expected",
			func(ctx SpecContext, null bool, stage func(context.Context) *op.ObjectState) {
				if null {
					suspend()
					nullPut(ctx, "tag-old", "old")
					rec.Info.Flags &^= meta.BucketVersionsSuspended
				} else {
					put(ctx, "v1")
				}
				expect := stage(ctx)
				was := listed()
				_, err := del(ctx, "", op.DeleteParams{Expect: expect})
				Expect(err).To(MatchError(op.ErrConcurrentModification))
				settle(s)
				Expect(listed()).To(Equal(was), "no marker")
			},
			Entry("another write tag", false, func(context.Context) *op.ObjectState {
				return &op.ObjectState{Key: version("v1"), Exists: true, Attrs: map[string][]byte{meta.AttrIDTag: []byte("idtag-other")}}
			}),
			Entry("an attr-only write since", false, func(ctx context.Context) *op.ObjectState {
				e := head(ctx, "v1")
				attrWrite(ctx, "v1")
				return e
			}),
			Entry("another instance under the current version's write tag", false, func(ctx context.Context) *op.ObjectState {
				e := head(ctx, "v1")
				e.Key = version("v0")
				return e
			}),
			Entry("a version put since", false, func(ctx context.Context) *op.ObjectState {
				e := head(ctx, "v1")
				put(ctx, "v2")
				return e
			}),
			Entry("the OLH head while another version is current", false, func(ctx context.Context) *op.ObjectState {
				return head(ctx, "")
			}),
			Entry("the null version current, given an attr-only write since", true, func(ctx context.Context) *op.ObjectState {
				e := head(ctx, "")
				attrWrite(ctx, "")
				return e
			}),
			Entry("the null version current, given a version put since", true, func(ctx context.Context) *op.ObjectState {
				e := head(ctx, "")
				put(ctx, "v2")
				return e
			}),
		)
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
		// refusedNull leaves body, the null data of a suspended bucket's key,
		// over v1 when older, kept below the null delete marker of a delete
		// whose removal of it an attr write refused after the marker's link and
		// whose relist ended before it linked, as it does when its gateway stops
		// after the refused replay's trim: here its backstop passes at once.
		refusedNull := func(ctx context.Context, older bool, body string) {
			GinkgoHelper()
			if older {
				put(ctx, "v1")
			}
			suspend()
			nullPut(ctx, "tag-old", body)
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			driver.SetRelistBackstopForTest(s, time.Nanosecond)
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			driver.SetRelistBackstopForTest(s, 0)
			Expect(nullMarker()).To(BeTrue(), "the refused delete's marker stays")
			Expect(olhHead().Data).NotTo(BeEmpty(), "the refusal kept the null data in the OLH head")
		}

		It("replaces the null version while the OLH head carries the expected write tag", func(ctx SpecContext) {
			nullCurrent(ctx)
			Expect(del(ctx, "", op.DeleteParams{Expect: head(ctx, "")})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			settle(s)
			Expect(olhHead().Data).To(BeEmpty(), "the null version's data, removed")
			Expect(currents()).To(Equal([]string{""}))
			Expect(olhInfo().Removed).To(BeTrue())
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
			func(ctx SpecContext, r denc.Release, linkFirst, alongside, conditional bool) {
				open(ctx, r)
				nullCurrent(ctx)
				expect := head(ctx, "")
				// A marker with conditions and no Expect takes the write tag of
				// the head its attempt followed (olhLink.ExpectHead).
				params := op.DeleteParams{Expect: expect}
				if conditional {
					params = op.DeleteParams{IfMatch: md5hex("old")}
				}
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
				res, err := del(ctx, "", params)
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
			Entry("the null write linking first, on squid", denc.Squid, true, false, false),
			Entry("the null write linking first, on tentacle", denc.Tentacle, true, false, false),
			Entry("the marker linking first, on squid", denc.Squid, false, false, false),
			Entry("the marker linking first, on tentacle", denc.Tentacle, false, false, false),
			Entry("the marker linking first, another replay running alongside the marker's own", denc.Squid, false, true, false),
			Entry("a marker with conditions, the null write linking first, on squid", denc.Squid, true, false, true),
			Entry("a marker with conditions, the null write linking first, on tentacle", denc.Tentacle, true, false, true),
			Entry("a marker with conditions, the marker linking first, on squid", denc.Squid, false, false, true),
			Entry("a marker with conditions, the marker linking first, on tentacle", denc.Tentacle, false, false, true),
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

		// atOLHRead arms fn to run once, just before the first later read of
		// the key's OLH entry, the relist's (relinkOLHEntry).
		atOLHRead := func(fn func()) {
			ac.setBeforeRead(func(r *radosclient.ReadOp) {
				for _, st := range r.Steps() {
					if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bi_get" && rgwcls.DecodeBIGetOp(denc.NewDecoder(x.In)).Type == rgwcls.BIIndexOLH {
						ac.setBeforeRead(nil)
						fn()
						return
					}
				}
			})
		}

		// v2 > v1 > the null data: the null marker demotes v2, and another
		// delete unlinks v2 just before the relist reads the OLH entry to link
		// it again. The relist gives way and its next round, which no longer
		// lists v2, makes v1 current again, listing the older null data below
		// it.
		It("makes current again the next version when another delete is removing the one the marker demoted, not the older null data", func(ctx SpecContext) {
			suspend()
			nullPut(ctx, "tag-old", "old")
			rec.Info.Flags &^= meta.BucketVersionsSuspended
			put(ctx, "v1")
			put(ctx, "v2")
			suspend()
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			at(calls("bucket_trim_olh_log"), 1, func() {
				atOLHRead(func() {
					Expect(del(ctx, "v2", op.DeleteParams{SkipOLHUpdate: true})).To(Equal(&op.DeleteObjectResult{VersionID: "v2"}))
				})
			})
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "the DeleteObjects request replays the key")
			settle(s)
			Expect(versionHead("v2")).To(BeNil(), "the second delete removed v2")
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"v1", ""}), "v1 current, the older null data below it")
			settledOn(ctx, "v1")
		})

		// A delete of a named delete marker logs no removal and takes the
		// marker's entries itself; landing just before the relist reads the
		// OLH entry, it is in the listing the relist reads after that entry.
		It("does not list again a delete marker the null marker demoted when a delete of it lands just before the relist reads the OLH entry", func(ctx SpecContext) {
			nullCurrent(ctx)
			rec.Info.Flags &^= meta.BucketVersionsSuspended
			res, err := del(ctx, "", op.DeleteParams{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.DeleteMarker).To(BeTrue())
			dm := res.VersionID
			suspend()
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			at(calls("bucket_trim_olh_log"), 1, func() {
				atOLHRead(func() {
					Expect(del(ctx, dm, op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: dm, DeleteMarker: true}), "the delete of the marker, answered")
				})
			})
			_, err = del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			_, ok := instanceEntry(dm)
			Expect(ok).To(BeFalse(), "the deleted marker's entry")
			Expect(listed()).NotTo(ContainElement(dm), "not listed again")
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"", "v1"}), "the null data current again")
			settledOn(ctx, "")
		})

		// A newer version is listed, so the relist lists the null data below
		// it, at its own epoch. A null writer whose pending modification the
		// relist has waited out links between that link's read of the null
		// record and its read of the OLH entry, which then holds the writer's
		// link and passes the compare. The record read again after the entry
		// shows the writer's epoch, and the relist ends: its reset and its link
		// at the old epoch would leave the write, which the key's OLH names,
		// below v3 with no entry of the name flagged current.
		It("leaves a null write current when it links between the fallback's read of the null record and its read of the OLH entry", func(ctx SpecContext) {
			nullCurrent(ctx)
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			w := newRGWWriter(c, s, rec, denc.Squid, "")
			linked := false
			at(calls("bucket_trim_olh_log"), 1, func() {
				put(ctx, "v3")
				w.Prepare(ctx)
				w.HeadWrite(ctx)
				w.Complete(ctx)
				w.ReadHead(ctx)
				Expect(w.Init(ctx)).To(BeTrue(), "the null writer's modification is pending")
				atOLHRead(func() {
					w.Link(ctx)
					linked = true
				})
			})
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			Expect(linked).To(BeTrue(), "the null writer linked before the fallback's read of the OLH entry")
			w.ReadLog(ctx)
			w.Apply(ctx)
			w.Trim(ctx)
			settle(s)
			Expect(olhHead().Data).To(Equal([]byte("data-")), "the null writer's data")
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"", "v3", "v1"}), "the null write lists first, at its own epoch")
			settledOn(ctx, "")
		})

		It("lists the null data again once six writers' links in a row have each refused its relist's link", func(ctx SpecContext) {
			nullCurrent(ctx)
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			for k := 1; k <= 6; k++ {
				before(relinks, k, func() { put(ctx, fmt.Sprintf("w%02d", k)) })
			}
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(listEntry("").Flags).To(Equal(rgwcls.FlagVer), "the null data listed again, non-current")
			Expect(nullMarker()).To(BeFalse())
			settledOn(ctx, "w06")
		})

		// The marker's replay finds its own entries gone: another replay kept
		// the null data for its hold, and a null link listed it again
		// (checkRemoval).
		It("answers ConcurrentModification, not success, when another replay kept the null data for its legal hold and a link listed it", func(ctx SpecContext) {
			nullCurrent(ctx)
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() {
				holdWrite(ctx, "")
				Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "another reader's replay")
				Expect(olhHead().Xattrs).To(HaveKey(meta.AttrETag), "the null data kept for its hold")
				Expect(s.SetOLHForTest(ctx, rec, version(""), driver.OLHLink{KeepVersion: true})).To(Succeed())
			})
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(olhHead().Xattrs).To(HaveKey(meta.AttrETag), "the null data")
			Expect(olhHead().Xattrs).To(HaveKey(meta.AttrObjectLegalHold), "under its hold")
			Expect(listed()).To(ContainElement(""))
			Expect(nullMarker()).To(BeFalse())
		})

		// The links are the marker's, then the re-link's of the version the
		// marker demoted.
		It("honors a DeleteObjects entry of the demoted version that unlinks it between the re-link's init and its link", func(ctx SpecContext) {
			nullCurrent(ctx)
			put(ctx, "v2")
			expect := head(ctx, "")
			at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
			before(calls("bucket_link_olh"), 2, func() {
				Expect(del(ctx, "v2", op.DeleteParams{SkipOLHUpdate: true})).To(Equal(&op.DeleteObjectResult{VersionID: "v2"}))
			})
			_, err := del(ctx, "", op.DeleteParams{Expect: expect})
			concurrent(err)
			settle(s)
			Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "the DeleteObjects request replays the key")
			settle(s)
			Expect(versionHead("v2")).To(BeNil(), "the second delete removed v2")
			_, ok := instanceEntry("v2")
			Expect(ok).To(BeFalse())
			Expect(nullMarker()).To(BeFalse())
			Expect(listed()).To(Equal([]string{"", "v1"}), "the null version current again")
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
			// The inits are the marker's, then the epoch-0 re-link's: P1's
			// rides in its head write. The delete of v1 moves the OLH entry's
			// epoch past P2's, so a reset of P2's record and a link at its
			// epoch would leave it non-current.
			before(inits, 2, func() {
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

		// The null data, which has tails, stays stored and counted below the
		// marker a refused null delete left (refusedNull). Each delete naming
		// null then takes one version, the marker first, and with deleteV1 a
		// delete of v1, the key's last listed version once the marker is gone,
		// comes between the two. After each the bucket's count is the versions
		// stored, the null data's record is in the index exactly while its head
		// holds the data that a read naming null returns, and each tail belongs
		// to the data stored or is queued for the garbage collector.
		DescribeTable("deletes naming null after a refused null delete whose relist ended take the marker, then the data, leaving no record without its data",
			func(ctx SpecContext, r denc.Release, older, deleteV1 bool) {
				const chunk = 4096
				openWith(ctx, r, map[string]string{"rgw_max_chunk_size": "4096", "rgw_obj_stripe_size": "4096"})
				body := strings.Repeat("n", 3*chunk)
				refusedNull(ctx, older, body)
				nullStored := func() bool { return olhHead() != nil && len(olhHead().Data) > 0 }
				tails := func() []string {
					var out []string
					for _, oid := range poolObjects(ctx, c, testDataPool, "") {
						if strings.Contains(oid, "__shadow_") {
							out = append(out, oid)
						}
					}
					return out
				}
				Expect(tails()).To(HaveLen(2), "the null data's tails")
				queued := func() map[string]bool {
					out := map[string]bool{}
					for i := range 32 {
						for _, e := range c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", i)) {
							for _, o := range e.Chain {
								out[o.Key.Name] = true
							}
						}
					}
					return out
				}
				check := func(step string) {
					GinkgoHelper()
					settle(s)
					want := 0
					if nullStored() {
						want++
					}
					if versionHead("v1") != nil {
						want++
					}
					Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(want), "%s: the index header's count of the versions stored", step)
					stats, err := s.BucketStats(ctx, rec)
					Expect(err).NotTo(HaveOccurred())
					Expect(stats.NumObjects).To(BeEquivalentTo(want), "%s: BucketStats", step)
					_, recorded := instanceEntry("")
					Expect(recorded).To(Equal(nullStored()), "%s: the null data's record is in the index exactly while its head holds the data", step)
					if nullStored() {
						st, err := s.StatObject(ctx, rec, version("null"))
						Expect(err).NotTo(HaveOccurred(), "%s: a read naming null", step)
						Expect(readBack(ctx, s, st, 0, st.Size)).To(Equal([]byte(body)), "%s: a read naming null returns the null data", step)
					}
					q := queued()
					var unnamed []string
					for _, t := range tails() {
						if !nullStored() && !q[t] || nullStored() && q[t] {
							unnamed = append(unnamed, t)
						}
					}
					Expect(unnamed).To(BeEmpty(), "%s: tails neither the stored null data's nor, once it is gone, queued for the garbage collector", step)
				}

				check("after the refused delete")

				Expect(del(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}), "the first delete naming null takes the marker")
				Expect(nullMarker()).To(BeFalse())
				check("after the first delete naming null")
				Expect(nullStored()).To(BeTrue(), "the first delete naming null leaves the null data whole")
				cur, err := s.StatObject(ctx, rec, version(""))
				Expect(err).NotTo(HaveOccurred())
				if older {
					Expect(cur.Key.Instance).To(Equal("v1"), "the key reads the version below the marker")
					Expect(listing(ctx, true)).To(Equal([]string{"k@v1"}), "ListObjectVersions")
				} else {
					// olh.info still names the marker that went, so a read of the
					// key answers as for a current delete marker.
					Expect([]bool{cur.Exists, cur.DeleteMarker}).To(Equal([]bool{false, true}), "the key reads no version, as a delete marker")
					Expect(listing(ctx, true)).To(BeEmpty(), "ListObjectVersions")
				}
				if deleteV1 {
					Expect(del(ctx, "v1", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}), "the delete of v1")
					Expect(versionHead("v1")).To(BeNil())
					check("after the delete of v1")
					Expect(nullStored()).To(BeTrue(), "the delete of v1 leaves the null data whole")
					cur, err = s.StatObject(ctx, rec, version(""))
					Expect(err).NotTo(HaveOccurred())
					Expect([]bool{cur.Exists, cur.DeleteMarker}).To(Equal([]bool{false, false}), "the key reads no version, and no delete marker")
					Expect(listing(ctx, true)).To(BeEmpty(), "ListObjectVersions")
				}

				Expect(del(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}), "the second delete naming null takes the data")
				Expect(nullStored()).To(BeFalse())
				check("after the second delete naming null")
				_, err = del(ctx, "null", op.DeleteParams{})
				Expect(err).To(MatchError(op.ErrNoSuchKey), "a third delete naming null")

				Expect(nullMarker()).To(BeFalse(), "no null delete marker entry")
				q := queued()
				for _, t := range tails() {
					Expect(q).To(HaveKey(t), "tail %s, queued for the garbage collector", t)
				}
				if older && !deleteV1 {
					Expect(listed()).To(Equal([]string{"v1"}))
					settledOn(ctx, "v1")
					return
				}
				Expect(olhHead()).To(BeNil(), "the key's head")
				_, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
				Expect(ok).To(BeFalse(), "the key's OLH entry")
				_, ok = c.Entry(rookIndexPool, "", shard, "k")
				Expect(ok).To(BeFalse(), "the key's plain entry")
				Expect(list()).To(BeEmpty(), "the key's list entries")
			},
			Entry("the key's only version, on squid", denc.Squid, false, false),
			Entry("the key's only version, on tentacle", denc.Tentacle, false, false),
			Entry("over an older version, on squid", denc.Squid, true, false),
			Entry("over an older version, on tentacle", denc.Tentacle, true, false),
			Entry("over an older version deleted once the marker is gone, on squid", denc.Squid, true, true),
			Entry("over an older version deleted once the marker is gone, on tentacle", denc.Tentacle, true, true),
		)

		// A radosgw null write prepares its record, writes the OLH head and
		// dies before its index completion: its record is a placeholder not
		// marked as existing, which nothing counts. The delete of the key's
		// last listed version clears the OLH, the head with that write's data
		// included, as radosgw's replay clears it.
		It("clears the OLH, the head included, when the null data the head holds has a record not marked as existing", func(ctx SpecContext) {
			put(ctx, "v1")
			suspend()
			rw := newRGWWriter(c, s, rec, denc.Squid, "")
			rw.Prepare(ctx)
			rw.HeadWrite(ctx)
			placeholder, ok := instanceEntry("")
			Expect(ok).To(BeTrue(), "the null write's prepared record")
			Expect(placeholder.Exists).To(BeFalse(), "a placeholder")
			Expect(olhHead().Data).To(Equal([]byte("data-")), "the null write's data in the OLH head")
			Expect(del(ctx, "v1", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
			settle(s)
			Expect(olhHead()).To(BeNil(), "the OLH head")
			_, ok = c.OLHEntry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeFalse(), "the key's OLH entry")
			Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeZero(), "the index header's count")
		})

		// The null data a refused null delete kept below its marker gets a
		// legal hold once versioning is enabled again: first, while the marker
		// is current, or after the delete's init. A delete naming null without
		// Expect, as a request holding a bucket record read before object lock
		// was turned on sends it, takes the marker. The class logs no removal
		// for a marker's unlink, so no check after the replay removes the data
		// below, whichever replay applies the unlink first.
		DescribeTable("a delete naming null without Expect takes the null marker alone, whatever lock holds the null data below",
			func(ctx SpecContext, r denc.Release, older bool, lock string, race bool) {
				open(ctx, r)
				refusedNull(ctx, older, "null-body")
				rec.Info.Flags &^= meta.BucketVersionsSuspended
				if lock == "first" {
					holdWrite(ctx, "")
				}
				if race || lock == "after-init" {
					at(calls("bucket_unlink_instance"), 1, func() {
						if lock == "after-init" {
							holdWrite(ctx, "")
						}
						if race {
							Expect(s.UpdateOLHForTest(ctx, rec, head(ctx, ""))).To(Succeed(), "another reader's replay, before the delete's own")
						}
					})
				}
				Expect(del(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
				settle(s)
				Expect(nullMarker()).To(BeFalse(), "the marker taken")
				Expect(olhHead()).NotTo(BeNil(), "the OLH head")
				Expect(olhHead().Data).To(Equal([]byte("null-body")), "the null data kept")
				_, held := olhHead().Xattrs[meta.AttrObjectLegalHold]
				Expect(held).To(Equal(lock != "none"), "the null data's legal hold")
				ie, ok := instanceEntry("")
				Expect(ok).To(BeTrue(), "the null data's record")
				Expect(ie.Exists).To(BeTrue(), "the null data's record, marked as existing")
				want := 1
				if older {
					want = 2
					Expect(listed()).To(Equal([]string{"v1"}), "v1 current, the null data unlisted")
				}
				Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(want), "the index header's count of the versions stored")
			},
			Entry("the key's only version, a hold first, another replay first, on squid", denc.Squid, false, "first", true),
			Entry("the key's only version, a hold first, another replay first, on tentacle", denc.Tentacle, false, "first", true),
			Entry("over an older version, a hold first, another replay first, on squid", denc.Squid, true, "first", true),
			Entry("over an older version, a hold first, another replay first, on tentacle", denc.Tentacle, true, "first", true),
			Entry("the key's only version, a hold first, its own replay, on squid", denc.Squid, false, "first", false),
			Entry("the key's only version, a hold first, its own replay, on tentacle", denc.Tentacle, false, "first", false),
			Entry("over an older version, a hold first, its own replay, on squid", denc.Squid, true, "first", false),
			Entry("over an older version, a hold first, its own replay, on tentacle", denc.Tentacle, true, "first", false),
			Entry("the key's only version, a hold after the init, another replay first, on squid", denc.Squid, false, "after-init", true),
			Entry("the key's only version, a hold after the init, another replay first, on tentacle", denc.Tentacle, false, "after-init", true),
			Entry("over an older version, a hold after the init, another replay first, on squid", denc.Squid, true, "after-init", true),
			Entry("over an older version, a hold after the init, another replay first, on tentacle", denc.Tentacle, true, "after-init", true),
			Entry("the key's only version, no lock, another replay first, on squid", denc.Squid, false, "none", true),
			Entry("the key's only version, no lock, another replay first, on tentacle", denc.Tentacle, false, "none", true),
		)

		// A delete naming null without Expect reads the null marker; before its
		// init, another writer replaces that marker: a second suspended
		// delete's null marker, or a null write, conditional on no current
		// version or not. The unlink's compare of the marker it read refuses
		// the unlink, and the delete starts again from a fresh read and takes
		// what is then the null version, as a later delete naming null would:
		// the second marker, or the write's data.
		DescribeTable("a delete naming null without Expect takes what replaced the null marker it read",
			func(ctx SpecContext, r denc.Release, older bool, writer string) {
				open(ctx, r)
				if older {
					put(ctx, "v1")
				}
				suspend()
				nullPut(ctx, "tag-old", "old")
				Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
				settle(s)
				Expect(nullMarker()).To(BeTrue(), "the null marker current")
				before(inits, 1, func() {
					switch writer {
					case "marker":
						Expect(del(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}), "a second null marker")
					case "write":
						nullPut(ctx, "tag-new", "new")
					case "conditional write":
						_, err := s.PutObject(ctx, rec, version(""), strings.NewReader("new"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tag-new", IfNoneMatch: "*"})
						Expect(err).NotTo(HaveOccurred(), "the null write, If-None-Match: * while the marker is current")
					}
					settle(s)
				})
				want := &op.DeleteObjectResult{VersionID: "null", DeleteMarker: writer == "marker"}
				Expect(del(ctx, "null", op.DeleteParams{})).To(Equal(want), "the delete takes what replaced the marker")
				settle(s)
				Expect(nullMarker()).To(BeFalse(), "no null delete marker")
				_, ok := instanceEntry("")
				Expect(ok).To(BeFalse(), "no null record")
				if older {
					Expect(olhHead().Data).To(BeEmpty(), "no null data in the OLH head")
					Expect(listed()).To(Equal([]string{"v1"}))
					settledOn(ctx, "v1")
					return
				}
				Expect(olhHead()).To(BeNil(), "the key's head")
				Expect(list()).To(BeEmpty(), "the key's list entries")
				Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeZero(), "the index header's count")
			},
			Entry("a second null marker, over an older version, on squid", denc.Squid, true, "marker"),
			Entry("a second null marker, over an older version, on tentacle", denc.Tentacle, true, "marker"),
			Entry("a second null marker, the key's only version, on squid", denc.Squid, false, "marker"),
			Entry("a null write, over an older version, on squid", denc.Squid, true, "write"),
			Entry("a null write, over an older version, on tentacle", denc.Tentacle, true, "write"),
			Entry("a conditional null write, over an older version, on squid", denc.Squid, true, "conditional write"),
			Entry("a conditional null write, over an older version, on tentacle", denc.Tentacle, true, "conditional write"),
			Entry("a conditional null write, the key's only version, on squid", denc.Squid, false, "conditional write"),
			Entry("a conditional null write, the key's only version, on tentacle", denc.Tentacle, false, "conditional write"),
		)

		// A null delete marker's instance entry left under a plain entry that
		// is no version marker, as only multisite's unlink at an older epoch
		// leaves one, then an OLH clear and a suspended bucket's plain write:
		// the class resolves null to the plain entry, and a delete naming null
		// without Expect compares the marker's entry as absent, which refuses
		// every try. It answers ConcurrentModification and removes nothing.
		DescribeTable("a delete naming null whose compare of the null marker refuses every try answers ConcurrentModification, removing nothing",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				suspend()
				nullPut(ctx, "tag-old", "plain-body")
				pe, ok := c.Entry(rookIndexPool, "", shard, "k")
				Expect(ok).To(BeTrue(), "the plain entry")
				Expect(pe.Flags&rgwcls.FlagVerMarker).To(BeZero(), "no version marker")
				orphan := rgwcls.DirEntry{Key: rgwcls.ObjKey{Name: "k"}, Flags: rgwcls.FlagVer | rgwcls.FlagDeleteMarker, VersionedEpoch: 3}
				c.Object(rookIndexPool, "", shard).Omap[rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k"}, true)] = encode(orphan)
				_, err := del(ctx, "null", op.DeleteParams{})
				Expect(err).To(MatchError(op.ErrConcurrentModification))
				Expect(err).NotTo(MatchError(op.ErrUnknown), "never UnknownError, 500")
				settle(s)
				Expect(olhHead()).NotTo(BeNil(), "the null data's head")
				Expect(olhHead().Data).To(Equal([]byte("plain-body")), "the null data")
				_, ok = c.Entry(rookIndexPool, "", shard, "k")
				Expect(ok).To(BeTrue(), "the plain entry")
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)

		// A delete naming null with neither Expect nor a condition on the data
		// reads the null data; before its init, a null delete marker links
		// over that data and its removal is refused (refusedNull's). The class
		// would unlink that marker in the data's place: the unlink's compare of
		// the marker as absent refuses it, and the delete, read again, takes
		// the marker and answers a delete marker, the data kept below as the
		// refusal left it for the next delete naming null.
		DescribeTable("a delete naming null that read the null data takes a null marker linked over it before its unlink, answering a delete marker",
			func(ctx SpecContext, r denc.Release, older bool) {
				open(ctx, r)
				if older {
					put(ctx, "v1")
				}
				suspend()
				nullPut(ctx, "tag-old", "null-body")
				before(inits, 1, func() {
					expect := head(ctx, "")
					at(calls("bucket_link_olh"), 1, func() { attrWrite(ctx, "") })
					driver.SetRelistBackstopForTest(s, time.Nanosecond)
					_, err := del(ctx, "", op.DeleteParams{Expect: expect})
					concurrent(err)
					driver.SetRelistBackstopForTest(s, 0)
				})
				Expect(del(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
				settle(s)
				Expect(nullMarker()).To(BeFalse(), "the marker taken")
				Expect(olhHead().Data).To(Equal([]byte("null-body")), "the null data, kept whole")
				_, ok := instanceEntry("")
				Expect(ok).To(BeTrue(), "the null data's record")
				want := 1
				if older {
					want = 2
				}
				Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(want), "the index header's count of the versions stored")
				Expect(del(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}), "the next delete naming null takes the data")
				settle(s)
				_, ok = instanceEntry("")
				Expect(ok).To(BeFalse(), "the null data's record")
				Expect(c.Header(rookIndexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(want-1), "the index header's count")
			},
			Entry("the key's only version, on squid", denc.Squid, false),
			Entry("the key's only version, on tentacle", denc.Tentacle, false),
			Entry("over an older version, on squid", denc.Squid, true),
			Entry("over an older version, on tentacle", denc.Tentacle, true),
		)
	})

	// With no other writer refusing its link, a relist has no bound in time:
	// the cap is on its retries, and its clock starts at the first try a
	// writer's change loses. Each read and write from the refused replay's
	// trim on takes longer here than the whole cap, as a call does that waits
	// for the gateway's in-flight budget or for a reshard, and the version is
	// listed again all the same, on each of the relist's paths.
	Describe("a relist no other writer refuses", func() {
		type slowRelist struct {
			instance string
			first    func(*radosclient.WriteOp) bool
			atTrim   func()
			listed   []string
			check    func()
		}

		DescribeTable("lists the refused version again however slow its reads and writes are",
			func(ctx SpecContext, arrange func(ctx context.Context) slowRelist) {
				const limit, slow = 2 * time.Millisecond, 5 * time.Millisecond
				driver.SetRelistCapForTest(s, limit)
				logs := &syncBuffer{}
				DeferCleanup(driver.CaptureLog(logs))
				r := arrange(ctx)
				expect := head(ctx, r.instance)
				at(r.first, 1, func() { attrWrite(ctx, r.instance) })
				at(calls("bucket_trim_olh_log"), 1, func() {
					if r.atTrim != nil {
						r.atTrim()
					}
					ac.setBeforeRead(func(*radosclient.ReadOp) { time.Sleep(slow) })
					ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
						time.Sleep(slow)
						return hooks.around(w, send)
					})
				})
				_, err := del(ctx, r.instance, op.DeleteParams{Expect: expect})
				ac.setBeforeRead(nil)
				ac.set(hooks.around)
				concurrent(err)
				settle(s)
				Expect(logs.String()).NotTo(ContainSubstring("it stays unlisted"), "the relist's end, as logged")
				Expect(listed()).To(Equal(r.listed), "the versions listed")
				r.check()
			},
			Entry("a named version, current again at epoch 0", func(ctx context.Context) slowRelist {
				put(ctx, "v1")
				put(ctx, "v2")
				return slowRelist{instance: "v2", first: calls("bucket_unlink_instance"), listed: []string{"v2", "v1"}, check: func() { settledOn(ctx, "v2") }}
			}),
			Entry("a named version, below a newer one at its own epoch", func(ctx context.Context) slowRelist {
				put(ctx, "v1")
				put(ctx, "v2")
				return slowRelist{instance: "v1", first: calls("bucket_unlink_instance"), listed: []string{"v2", "v1"}, check: func() { settledOn(ctx, "v2") }}
			}),
			// The relist's rounds wait on the pending attr of a writer that
			// stopped after its init: a wait is no lost try, so it starts no
			// clock either.
			Entry("a named version whose rounds first wait on another writer's pending modification", func(ctx context.Context) slowRelist {
				put(ctx, "v1")
				put(ctx, "v2")
				rw := newRGWWriter(c, s, rec, denc.Squid, "dead")
				rw.ReadHead(ctx)
				Expect(rw.Init(ctx)).To(BeTrue(), "a writer that stops after its init")
				stuck := pending()
				return slowRelist{instance: "v2", first: calls("bucket_unlink_instance"), listed: []string{"v2", "v1"}, check: func() {
					Expect(currents()).To(Equal([]string{"v2"}))
					Expect(olhInfo().Target.Key.Instance).To(Equal("v2"))
					Expect(pending()).To(Equal(stuck), "the stopped writer's pending attr alone stays")
				}}
			}),
			Entry("the null version, current again at epoch 0", func(ctx context.Context) slowRelist {
				put(ctx, "v1")
				suspend()
				nullPut(ctx, "tag-old", "old")
				return slowRelist{first: calls("bucket_link_olh"), listed: []string{"", "v1"}, check: func() { settledOn(ctx, "") }}
			}),
			Entry("the null version, below a newer one at its own epoch", func(ctx context.Context) slowRelist {
				put(ctx, "v1")
				suspend()
				nullPut(ctx, "tag-old", "old")
				return slowRelist{first: calls("bucket_link_olh"), atTrim: func() { put(ctx, "v3") }, listed: []string{"v3", "", "v1"}, check: func() { settledOn(ctx, "v3") }}
			}),
		)
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
