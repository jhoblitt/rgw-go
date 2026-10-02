package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// opLogCluster is a fakerados cluster whose pools record, by object, the
// read lengths of each read op and the steps of each write op. They run
// afterRead once each read op returns, afterWrite once a write op has
// applied, whose error then replaces the op's result as a context ending
// while librados completes the op does, and beforeNotify before a notify is
// delivered.
type opLogCluster struct {
	*fakerados.Cluster
	mu           sync.Mutex
	readLens     map[string][][]uint64
	writes       map[string][][]radosclient.Step
	afterRead    func(oid string)
	afterWrite   func(oid string) error
	beforeNotify func(oid string)
}

func newOpLogCluster(c *fakerados.Cluster) *opLogCluster {
	return &opLogCluster{Cluster: c, readLens: map[string][][]uint64{}, writes: map[string][][]radosclient.Step{}}
}

func (c *opLogCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return &opLogPool{Pool: p, log: c}, nil
}

// reads returns the read lengths of each read op run on oid.
func (c *opLogCluster) reads(oid string) [][]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.readLens[oid])
}

// lastWrite describes the steps of the last write op run on oid.
func (c *opLogCluster) lastWrite(oid string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ops := c.writes[oid]
	if len(ops) == 0 {
		return nil
	}
	return describeSteps(ops[len(ops)-1])
}

type opLogPool struct {
	radosclient.Pool
	log *opLogCluster
}

func (p *opLogPool) Read(ctx context.Context, oid string, op *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	var lens []uint64
	for _, st := range op.Steps() {
		if r, ok := st.(*radosclient.ReadStep); ok {
			lens = append(lens, r.Length)
		}
	}
	p.log.mu.Lock()
	p.log.readLens[oid] = append(p.log.readLens[oid], lens)
	after := p.log.afterRead
	p.log.mu.Unlock()
	v, err := p.Pool.Read(ctx, oid, op, flags)
	if after != nil {
		after(oid)
	}
	return v, err
}

func (p *opLogPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	p.log.mu.Lock()
	p.log.writes[oid] = append(p.log.writes[oid], op.Steps())
	after := p.log.afterWrite
	p.log.mu.Unlock()
	v, err := p.Pool.Write(ctx, oid, op, flags)
	if err == nil && after != nil {
		if herr := after(oid); herr != nil {
			return 0, herr
		}
	}
	return v, err
}

func (p *opLogPool) Notify(ctx context.Context, oid string, payload []byte, timeout time.Duration) ([]radosclient.NotifyAck, error) {
	p.log.mu.Lock()
	before := p.log.beforeNotify
	p.log.mu.Unlock()
	if before != nil {
		before(oid)
	}
	return p.Pool.Notify(ctx, oid, payload, timeout)
}

// describeSteps names an op's steps as the specs compare them.
func describeSteps(steps []radosclient.Step) []string {
	var out []string
	for _, st := range steps {
		switch s := st.(type) {
		case *radosclient.CreateStep:
			out = append(out, fmt.Sprintf("create(exclusive=%t)", s.Exclusive))
		case *radosclient.RemoveStep:
			out = append(out, "remove")
		case *radosclient.StepFlagsStep:
			out = append(out, fmt.Sprintf("flags(%#x)", uint32(s.Flags)))
		case *radosclient.ExecStep:
			out = append(out, "exec("+s.Class+"."+s.Method+")")
		case *radosclient.WriteFullStep:
			out = append(out, "write_full("+string(s.Data)+")")
		case *radosclient.SetXattrStep:
			out = append(out, "setxattr("+s.Name+")")
		case *radosclient.RmXattrStep:
			out = append(out, "rmxattr("+s.Name+")")
		default:
			out = append(out, fmt.Sprintf("%T", st))
		}
	}
	return out
}

// cacheEnabled reports whether c is enabled.
func cacheEnabled(c *objectCache) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.enabled
}

// lastNotify decodes the last record delivered on the control object oid.
func lastNotify(c *fakerados.Cluster, oid string) meta.CacheNotifyInfo {
	GinkgoHelper()
	payloads := c.Notifies("zone.rgw.control", "", oid)
	Expect(payloads).NotTo(BeEmpty())
	d := denc.NewDecoder(payloads[len(payloads)-1])
	info := meta.DecodeCacheNotifyInfo(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return info
}

var _ = Describe("newWriteVersion", func() {
	It("is version 1 with generate_new_write_ver's tag, an underscore and 23 random characters", func() {
		v := newWriteVersion()
		Expect(v.Ver).To(BeEquivalentTo(1))
		Expect(v.Tag).To(MatchRegexp(`^_[A-Za-z0-9_-]{23}$`), "append_rand_alpha with TAG_LEN 24, rgw_common.h:1621-1628")
		Expect(newWriteVersion().Tag).NotTo(Equal(v.Tag))
	})
})

// knownVersion is a version a tracker read before.
var knownVersion = meta.ObjVersion{Ver: 3, Tag: "t"}

var _ = Describe("objv", func() {
	It("reads the version, checking it first when one was read before", func() {
		rop := radosclient.NewReadOp()
		(&objv{}).prepareRead(rop, denc.Squid)
		Expect(describeSteps(rop.Steps())).To(Equal([]string{"exec(version.read)"}))
		rop = radosclient.NewReadOp()
		(&objv{read: knownVersion}).prepareRead(rop, denc.Squid)
		Expect(describeSteps(rop.Steps())).To(Equal([]string{"exec(version.check_conds)", "exec(version.read)"}), "rgw_rados.cc:158-167")
	})

	DescribeTable("prepares a write and applies it as RGWObjVersionTracker does",
		func(v objv, steps []string, after meta.ObjVersion) {
			wop := radosclient.NewWriteOp()
			v.prepareWrite(wop, denc.Squid)
			Expect(describeSteps(wop.Steps())).To(Equal(steps), "rgw_rados.cc:169-183")
			v.applyWrite()
			Expect(v.read).To(Equal(after), "rgw_rados.cc:185-197")
			Expect(v.write).To(BeZero())
		},
		Entry("checked and incremented: the read version moves on by one",
			objv{read: knownVersion}, []string{"exec(version.check_conds)", "exec(version.inc)"}, meta.ObjVersion{Ver: 4, Tag: "t"}),
		Entry("checked and set: the read version becomes the written one",
			objv{read: knownVersion, write: meta.ObjVersion{Ver: 1, Tag: "n"}}, []string{"exec(version.check_conds)", "exec(version.set)"}, meta.ObjVersion{Ver: 1, Tag: "n"}),
		Entry("set unchecked", objv{write: meta.ObjVersion{Ver: 1, Tag: "n"}}, []string{"exec(version.set)"}, meta.ObjVersion{Ver: 1, Tag: "n"}),
		Entry("incremented unchecked: the version it reached is unknown", objv{}, []string{"exec(version.inc)"}, meta.ObjVersion{}),
	)
})

var _ = Describe("sysobjs", func() {
	const control = "zone.rgw.control"
	var (
		c     *fakerados.Cluster
		ops   *opLogCluster
		s     *sysobjs
		cache *objectCache
		root  meta.Pool
		obj   sysObj
		name  string
		now   time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(captureLog(GinkgoWriter))
		root = meta.ParsePool("zone.rgw.meta:root")
		obj = sysObj{pool: root, oid: "plain"}
		name = normalName(root, "plain")
		now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		c = fakerados.New()
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		ops = newOpLogCluster(c)
		pools := newPoolCache(ops)
		DeferCleanup(pools.closeAll)
		ctrl, err := pools.get(ctx, meta.ParsePool(control))
		Expect(err).NotTo(HaveOccurred())
		cache = newObjectCache(25000, 900*time.Second, root, func() time.Time { return now })
		n := newNotifier(ctrl, controlOIDs(8), denc.Squid, 10, cache.onNotify, cache.setEnabled)
		Expect(n.createControlObjects(ctx)).To(Succeed())
		// The watches outlive this node, so they run on a context its end
		// does not cancel.
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan error, 1)
		go func() { done <- n.run(runCtx) }()
		DeferCleanup(func() {
			cancel()
			Eventually(done).WithTimeout(time.Second).Should(Receive())
		})
		Eventually(func() bool { return cacheEnabled(cache) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeTrue())
		s = &sysobjs{pools: pools, cache: cache, notify: n, release: denc.Squid, now: func() time.Time { return now }}
	})

	It("writes exclusively with a fresh version, caches the record, distributes it, and drops it on its own notify", func(ctx SpecContext) {
		var (
			during    cacheInfo
			duringErr error
		)
		ops.beforeNotify = func(string) {
			during, duringErr = cache.get(name, meta.CacheFlagData|meta.CacheFlagXattrs|meta.CacheFlagMeta|meta.CacheFlagObjVersion)
		}
		v := &objv{write: meta.ObjVersion{Ver: 1, Tag: "tagtagtagtagtagtagtagtag"}}
		mt, err := s.write(ctx, obj, []byte("data"), map[string][]byte{"user.rgw.acl": {7}, "empty": {}}, true, time.Time{}, v)
		Expect(err).NotTo(HaveOccurred())
		Expect(mt).To(Equal(now), "mtime defaults to now, svc_sys_obj_core.cc:508-510")
		Expect(ops.lastWrite("plain")).To(Equal([]string{
			"create(exclusive=true)", "exec(version.set)", "write_full(data)", "setxattr(user.rgw.acl)",
		}), "svc_sys_obj_core.cc:496-526; an empty attr is skipped, :522-523")
		stored := c.Object("zone.rgw.meta", "root", "plain")
		Expect(stored.Data).To(Equal([]byte("data")))
		Expect(stored.Mtime).To(Equal(now))
		Expect(stored.Xattrs).To(HaveKeyWithValue("user.rgw.acl", []byte{7}))
		Expect(stored.Xattrs).NotTo(HaveKey("empty"))
		d := denc.NewDecoder(stored.Xattrs[version.XattrName])
		Expect(version.DecodeObjVersion(d)).To(Equal(version.ObjVersion{Ver: 1, Tag: "tagtagtagtagtagtagtagtag"}))
		Expect(v.read).To(Equal(meta.ObjVersion{Ver: 1, Tag: "tagtagtagtagtagtagtagtag"}), "apply_write, rgw_rados.cc:185-197")

		Expect(duringErr).NotTo(HaveOccurred(), "the record is put before it is distributed, svc_sys_obj_cache.cc:344-346")
		Expect(during.data).To(Equal([]byte("data")))
		Expect(during.size).To(BeEquivalentTo(4))
		Expect(during.mtime).To(Equal(now))
		Expect(during.version).To(Equal(v.read))
		_, err = cache.get(name, 0)
		Expect(err).To(MatchError(errCacheMiss), "this gateway's own notify invalidates the entry it put")

		info := lastNotify(c, s.notify.pick(name))
		Expect(info.Op).To(Equal(meta.CacheUpdateObj))
		Expect(info.Obj).To(Equal(meta.RawObj{Pool: root, OID: "plain"}))
		Expect(info.ObjInfo.Data).To(Equal([]byte("data")))
		Expect(info.ObjInfo.Flags).To(Equal(meta.CacheFlagXattrs | meta.CacheFlagData | meta.CacheFlagMeta | meta.CacheFlagObjVersion))
		Expect(info.ObjInfo.Version).To(Equal(v.read))
		Expect(info.ObjInfo.Meta.Size).To(BeEquivalentTo(4))
		Expect(info.ObjInfo.Meta.Mtime.Time).To(BeTemporally("==", now))
		Expect(info.ObjInfo.Xattrs).To(HaveKey("empty"), "the record carries the attrs as given, svc_sys_obj_cache.cc:326")
	})

	It("overwrites through a remove that may fail, then a create, as radosgw does", func(ctx SpecContext) {
		c.Put("zone.rgw.meta", "root", "plain", []byte("old"))
		mt := now.Add(-time.Hour)
		got, err := s.write(ctx, obj, []byte("new"), nil, false, mt, &objv{write: meta.ObjVersion{Ver: 1, Tag: "n"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(mt), "the mtime given is the one stored and returned")
		Expect(ops.lastWrite("plain")).To(Equal([]string{
			"remove", fmt.Sprintf("flags(%#x)", uint32(radosclient.StepFlagFailOK)), "create(exclusive=false)", "exec(version.set)", "write_full(new)",
		}), "svc_sys_obj_core.cc:496-502")
		Expect(c.Object("zone.rgw.meta", "root", "plain").Mtime).To(Equal(mt))
		_, err = s.write(ctx, sysObj{pool: root, oid: "fresh"}, []byte("x"), nil, false, now, nil)
		Expect(err).NotTo(HaveOccurred(), "the failed remove of a missing object does not fail the op")
	})

	It("fails an exclusive write of an existing object with EEXIST and invalidates the entry", func(ctx SpecContext) {
		c.Put("zone.rgw.meta", "root", "plain", []byte("old"))
		cache.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("old")})
		_, err := s.write(ctx, obj, []byte("new"), nil, true, now, &objv{write: newWriteVersion()})
		Expect(err).To(MatchError(radosclient.ErrExists))
		Expect(c.Object("zone.rgw.meta", "root", "plain").Data).To(Equal([]byte("old")))
		_, err = cache.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "svc_sys_obj_cache.cc:349-351")
		Expect(c.Notifies(control, "", s.notify.pick(name))).To(BeEmpty(), "a write that did not apply is not announced")
	})

	It("does not overwrite a newer record announced while it distributes its own", func(ctx SpecContext) {
		_, err := s.write(ctx, obj, []byte("v1"), nil, true, now, nil)
		Expect(err).NotTo(HaveOccurred())
		ctrl, err := s.pools.get(ctx, meta.ParsePool(control))
		Expect(err).NotTo(HaveOccurred())
		announced := false
		ops.beforeNotify = func(oid string) {
			if announced {
				return
			}
			announced = true
			// another gateway writes after us and announces it before our
			// notify is delivered, which then never reaches our own watch
			c.Object("zone.rgw.meta", "root", "plain").Data = []byte("theirs")
			theirs := meta.CacheNotifyInfo{Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: root, OID: "plain"}}
			_, nerr := ctrl.Notify(ctx, oid, encodeAt(theirs, denc.Squid), 0)
			Expect(nerr).NotTo(HaveOccurred())
			// Not a timeout: that would send invalidation retries our own
			// watch receives, which drop the entry under either order.
			c.FailNotify(control, "", oid, 1, errors.New("notify refused"))
		}
		_, err = s.write(ctx, obj, []byte("v2"), nil, false, now, nil)
		Expect(err).NotTo(HaveOccurred())
		got, err := s.read(ctx, obj, readParams{data: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("theirs")), "the newer record's notify came after our put, svc_sys_obj_cache.cc:344-346")
	})

	// change makes one of the three changes sysobjs makes to an object.
	change := func(ctx context.Context, kind string) error {
		switch kind {
		case "write":
			_, err := s.write(ctx, obj, []byte("new"), nil, false, now, nil)
			return err
		case "setAttrs":
			return s.setAttrs(ctx, obj, map[string][]byte{"b": {2}}, nil, false, nil)
		case "remove":
			return s.remove(ctx, obj, nil)
		}
		Fail("unknown change " + kind)
		return nil
	}

	DescribeTable("announces a change that applied even when the caller's context ends after it",
		func(ctx SpecContext, kind string, op meta.CacheNotifyOp) {
			_, err := s.write(ctx, obj, []byte("old"), nil, true, now, nil)
			Expect(err).NotTo(HaveOccurred())
			oid := s.notify.pick(name)
			before := len(c.Notifies(control, "", oid))
			changeCtx, cancel := context.WithCancel(ctx)
			ops.afterWrite = func(string) error {
				cancel()
				return nil
			}
			Expect(change(changeCtx, kind)).To(Succeed())
			Expect(changeCtx.Err()).To(MatchError(context.Canceled))
			Expect(c.Notifies(control, "", oid)).To(HaveLen(before+1), "radosgw never cancels its notify")
			Expect(lastNotify(c, oid).Op).To(Equal(op))
		},
		Entry("a write", "write", meta.CacheUpdateObj),
		Entry("an attr change", "setAttrs", meta.CacheUpdateObj),
		Entry("a remove", "remove", meta.CacheInvalidateObj),
	)

	DescribeTable("invalidates on every gateway a change whose context ended while it ran, since it may have applied",
		func(ctx SpecContext, kind string) {
			_, err := s.write(ctx, obj, []byte("old"), nil, true, now, nil)
			Expect(err).NotTo(HaveOccurred())
			_, err = s.read(ctx, obj, readParams{data: true})
			Expect(err).NotTo(HaveOccurred())
			oid := s.notify.pick(name)
			before := len(c.Notifies(control, "", oid))
			changeCtx, cancel := context.WithCancel(ctx)
			ops.afterWrite = func(string) error {
				cancel()
				return changeCtx.Err()
			}
			Expect(change(changeCtx, kind)).To(MatchError(context.Canceled))
			Expect(c.Notifies(control, "", oid)).To(HaveLen(before + 1))
			last := lastNotify(c, oid)
			Expect(last.Op).To(Equal(meta.CacheInvalidateObj))
			Expect(last.Obj).To(Equal(meta.RawObj{Pool: root, OID: "plain"}))
			_, err = cache.get(name, 0)
			Expect(err).To(MatchError(errCacheMiss))
		},
		Entry("a write", "write"),
		Entry("an attr change", "setAttrs"),
		Entry("a remove", "remove"),
	)

	It("checks the read version on a versioned overwrite and reports a race as ECANCELED", func(ctx SpecContext) {
		v := &objv{write: newWriteVersion()}
		_, err := s.write(ctx, obj, []byte("v1"), nil, true, now, v)
		Expect(err).NotTo(HaveOccurred())
		stale := &objv{read: meta.ObjVersion{Ver: 1, Tag: "someone-elses"}}
		_, err = s.write(ctx, obj, []byte("v2"), nil, false, now, stale)
		Expect(err).To(MatchError(radosclient.ErrCanceled), "cls_version check_conds EQ fails, cls_version.cc:194-197")
		Expect(stale.read).To(Equal(meta.ObjVersion{Ver: 1, Tag: "someone-elses"}), "a failed write applies nothing")
		fresh := &objv{read: v.read}
		_, err = s.write(ctx, obj, []byte("v2"), nil, false, now, fresh)
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh.read).To(Equal(meta.ObjVersion{Ver: 2, Tag: v.read.Tag}), "inc applied locally")
		Expect(c.Object("zone.rgw.meta", "root", "plain").Data).To(Equal([]byte("v2")))
		d := denc.NewDecoder(c.Object("zone.rgw.meta", "root", "plain").Xattrs[version.XattrName])
		Expect(version.DecodeObjVersion(d)).To(Equal(version.ObjVersion{Ver: 2, Tag: v.read.Tag}))
	})

	It("reads through the cache, caches a negative lookup and serves the remembered record", func(ctx SpecContext) {
		_, err := s.read(ctx, obj, readParams{data: true, attrs: true})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		c.Put("zone.rgw.meta", "root", "plain", []byte("late"))
		_, err = s.read(ctx, obj, readParams{data: true, attrs: true})
		Expect(err).To(MatchError(radosclient.ErrNotFound), "the negative entry is served until invalidated, svc_sys_obj_cache.cc:200-205")
		Expect(cache.invalidateRemove(name)).To(BeTrue())
		got, err := s.read(ctx, obj, readParams{data: true, attrs: true, meta: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("late")))
		Expect(got.size).To(BeEquivalentTo(4))
		c.Object("zone.rgw.meta", "root", "plain").Data = []byte("changed behind the cache")
		got, err = s.read(ctx, obj, readParams{data: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("late")), "served from the entry")
		Expect(ops.reads("plain")).To(HaveLen(2), "the first miss and the read that filled the entry")
	})

	It("reads the version with a tracker, checking one already known on a miss", func(ctx SpecContext) {
		w := &objv{write: newWriteVersion()}
		_, err := s.write(ctx, obj, []byte("d"), nil, true, now, w)
		Expect(err).NotTo(HaveOccurred())
		Expect(cache.invalidateRemove(name)).To(BeFalse(), "the write's own notify dropped its entry")

		v := &objv{}
		got, err := s.read(ctx, obj, readParams{data: true, objv: v})
		Expect(err).NotTo(HaveOccurred())
		Expect(v.read).To(Equal(w.read))
		Expect(got.version).To(Equal(w.read))
		hit := &objv{read: meta.ObjVersion{Ver: 9, Tag: "unchecked"}}
		_, err = s.read(ctx, obj, readParams{data: true, objv: hit})
		Expect(err).NotTo(HaveOccurred(), "a hit takes the cached version without checking, svc_sys_obj_cache.cc:159-160")
		Expect(hit.read).To(Equal(w.read))

		Expect(cache.invalidateRemove(name)).To(BeTrue())
		stale := &objv{read: meta.ObjVersion{Ver: 9, Tag: "stale"}}
		_, err = s.read(ctx, obj, readParams{data: true, objv: stale})
		Expect(err).To(MatchError(radosclient.ErrCanceled), "prepare_op_for_read's check, rgw_rados.cc:158-167")
		_, err = cache.get(name, 0)
		Expect(err).To(MatchError(errCacheMiss), "only ENOENT is cached")
	})

	It("returns only the user.rgw. attrs, from RADOS and from the cache, unless the raw set is asked for", func(ctx SpecContext) {
		_, err := s.write(ctx, obj, []byte("d"), map[string][]byte{"user.rgw.acl": {1}, "user.other": {2}}, true, now, &objv{write: newWriteVersion()})
		Expect(err).NotTo(HaveOccurred())
		want := map[string][]byte{"user.rgw.acl": {1}}
		got, err := s.read(ctx, obj, readParams{attrs: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.attrs).To(Equal(want), "from RADOS, svc_sys_obj_core.cc:196-198")
		cached, err := cache.get(name, meta.CacheFlagXattrs)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached.xattrs).To(HaveKey(version.XattrName), "the cache keeps the unfiltered set, svc_sys_obj_cache.cc:193-197")
		got, err = s.read(ctx, obj, readParams{attrs: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.attrs).To(Equal(want), "from the cache, :167-173")
		raw, err := s.read(ctx, obj, readParams{attrs: true, rawAttrs: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(raw.attrs).To(HaveKey("user.rgw.acl"))
		Expect(raw.attrs).To(HaveKey("user.other"))
		Expect(raw.attrs).To(HaveKey(version.XattrName))

		Expect(cache.invalidateRemove(name)).To(BeTrue())
		raw, err = s.read(ctx, obj, readParams{attrs: true, rawAttrs: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(raw.attrs).To(HaveKey("user.other"), "from RADOS")
		Expect(raw.attrs).To(HaveKey(version.XattrName))
	})

	It("reads a small object with a small buffer, and a larger one whole at the size its stat reported", func(ctx SpecContext) {
		c.Put("zone.rgw.meta", "root", "plain", []byte("small"))
		got, err := s.read(ctx, obj, readParams{data: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("small")))
		Expect(got.size).To(BeZero(), "the size only when meta is asked for")
		Expect(ops.reads("plain")).To(Equal([][]uint64{{sysobjReadSize}}))

		big := bytes.Repeat([]byte("b"), 3*sysobjReadSize+1)
		c.Put("zone.rgw.meta", "root", "big", big)
		got, err = s.read(ctx, sysObj{pool: root, oid: "big"}, readParams{data: true, meta: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal(big))
		Expect(got.size).To(BeEquivalentTo(len(big)))
		Expect(ops.reads("big")).To(Equal([][]uint64{{sysobjReadSize}, {uint64(len(big))}}))
		cached, err := cache.get(normalName(root, "big"), meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached.data).To(Equal(big))
	})

	It("gives up on an object that outgrows every read", func(ctx SpecContext) {
		c.Put("zone.rgw.meta", "root", "plain", bytes.Repeat([]byte("g"), sysobjReadSize+1))
		ops.afterRead = func(oid string) {
			o := c.Object("zone.rgw.meta", "root", oid)
			o.Data = append(o.Data, bytes.Repeat([]byte("g"), sysobjReadSize)...)
		}
		_, err := s.read(ctx, obj, readParams{data: true})
		Expect(err).To(MatchError(ContainSubstring("grew")))
		Expect(ops.reads("plain")).To(HaveLen(sysobjReadTries))
		_, err = cache.get(name, 0)
		Expect(err).To(MatchError(errCacheMiss))
	})

	It("re-reads after another gateway's notify, whatever payload it carried", func(ctx SpecContext) {
		v := &objv{write: newWriteVersion()}
		_, err := s.write(ctx, obj, []byte("mine"), nil, true, now, v)
		Expect(err).NotTo(HaveOccurred())
		got, err := s.read(ctx, obj, readParams{data: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("mine")))
		// another gateway overwrites the object and announces a forged record
		c.Object("zone.rgw.meta", "root", "plain").Data = []byte("theirs")
		ctrl, err := s.pools.get(ctx, meta.ParsePool(control))
		Expect(err).NotTo(HaveOccurred())
		forged := meta.CacheNotifyInfo{
			Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: root, OID: "plain"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("forged")},
		}
		_, err = ctrl.Notify(ctx, s.notify.pick(name), encodeAt(forged, denc.Squid), 0)
		Expect(err).NotTo(HaveOccurred())
		got, err = s.read(ctx, obj, readParams{data: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("theirs")), "re-read from RADOS, not the payload")
	})

	It("sets attrs with a modify record and removes with an invalidation", func(ctx SpecContext) {
		v := &objv{write: newWriteVersion()}
		_, err := s.write(ctx, obj, []byte("d"), map[string][]byte{"a": {1}}, true, now, v)
		Expect(err).NotTo(HaveOccurred())
		mod := &objv{read: v.read}
		Expect(s.setAttrs(ctx, obj, map[string][]byte{"b": {2}, "e": {}}, []string{"a"}, false, mod)).To(Succeed())
		Expect(ops.lastWrite("plain")).To(Equal([]string{
			"exec(version.check_conds)", "exec(version.inc)", "rmxattr(a)", "setxattr(b)",
		}), "svc_sys_obj_core.cc:253-276")
		Expect(mod.read).To(Equal(meta.ObjVersion{Ver: v.read.Ver + 1, Tag: v.read.Tag}))
		stored := c.Object("zone.rgw.meta", "root", "plain")
		Expect(stored.Xattrs).To(HaveKey("b"))
		Expect(stored.Xattrs).NotTo(HaveKey("a"))
		Expect(stored.Xattrs).NotTo(HaveKey("e"))
		oid := s.notify.pick(name)
		last := lastNotify(c, oid)
		Expect(last.Op).To(Equal(meta.CacheUpdateObj))
		Expect(last.ObjInfo.Flags).To(Equal(meta.CacheFlagModifyXattrs|meta.CacheFlagObjVersion), "svc_sys_obj_cache.cc:288-300")
		Expect(last.ObjInfo.Xattrs).To(HaveKeyWithValue("b", []byte{2}))
		Expect(last.ObjInfo.Xattrs).To(HaveKey("e"), "the record carries the attrs as given, svc_sys_obj_cache.cc:288")
		Expect(last.ObjInfo.RMXattrs).To(HaveKey("a"))
		Expect(last.ObjInfo.Version).To(Equal(mod.read))

		Expect(s.remove(ctx, obj, nil)).To(Succeed())
		Expect(c.Object("zone.rgw.meta", "root", "plain")).To(BeNil())
		last = lastNotify(c, oid)
		Expect(last.Op).To(Equal(meta.CacheInvalidateObj))
		Expect(last.Obj).To(Equal(meta.RawObj{Pool: root, OID: "plain"}))
		Expect(last.ObjInfo.Flags).To(BeZero())
		_, err = s.read(ctx, obj, readParams{data: true})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})

	It("skips an empty attr change but still records and distributes it", func(ctx SpecContext) {
		Expect(s.setAttrs(ctx, obj, map[string][]byte{"e": {}}, nil, false, nil)).To(Succeed())
		Expect(ops.lastWrite("plain")).To(BeNil(), "an empty op is not sent, svc_sys_obj_core.cc:278-279")
		Expect(c.Object("zone.rgw.meta", "root", "plain")).To(BeNil())
		Expect(lastNotify(c, s.notify.pick(name)).ObjInfo.Flags).To(Equal(meta.CacheFlagModifyXattrs))
	})

	It("creates exclusively on an exclusive attr change", func(ctx SpecContext) {
		c.Put("zone.rgw.meta", "root", "plain", []byte("d"))
		err := s.setAttrs(ctx, obj, map[string][]byte{"b": {2}}, nil, true, nil)
		Expect(err).To(MatchError(radosclient.ErrExists))
		Expect(ops.lastWrite("plain")).To(Equal([]string{"create(exclusive=true)", "setxattr(b)"}), "svc_sys_obj_core.cc:253-255")
	})

	It("removes under the version check and keeps the object when it fails", func(ctx SpecContext) {
		v := &objv{write: newWriteVersion()}
		_, err := s.write(ctx, obj, []byte("d"), nil, true, now, v)
		Expect(err).NotTo(HaveOccurred())
		_, err = s.read(ctx, obj, readParams{data: true})
		Expect(err).NotTo(HaveOccurred())
		oid := s.notify.pick(name)
		before := len(c.Notifies(control, "", oid))
		err = s.remove(ctx, obj, &objv{read: meta.ObjVersion{Ver: 1, Tag: "other"}})
		Expect(err).To(MatchError(radosclient.ErrCanceled))
		Expect(c.Object("zone.rgw.meta", "root", "plain")).NotTo(BeNil())
		_, err = cache.get(name, meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred(), "a failed remove leaves the entry, svc_sys_obj_cache.cc:92-95")
		Expect(c.Notifies(control, "", oid)).To(HaveLen(before), "and announces nothing")
		Expect(s.remove(ctx, obj, &objv{read: v.read})).To(Succeed())
		Expect(ops.lastWrite("plain")).To(Equal([]string{"exec(version.check_conds)", "exec(version.inc)", "remove"}), "svc_sys_obj_core.cc:463-469")
		Expect(c.Object("zone.rgw.meta", "root", "plain")).To(BeNil())
	})

	It("goes to RADOS for every read while the cache is disabled, and still distributes", func(ctx SpecContext) {
		cache.setEnabled(false)
		_, err := s.write(ctx, obj, []byte("d"), nil, true, now, &objv{write: newWriteVersion()})
		Expect(err).NotTo(HaveOccurred())
		Expect(lastNotify(c, s.notify.pick(name)).Op).To(Equal(meta.CacheUpdateObj))
		c.Object("zone.rgw.meta", "root", "plain").Data = []byte("changed")
		got, err := s.read(ctx, obj, readParams{data: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("changed")))
	})

	It("logs and carries on when the notify cannot be delivered", func(ctx SpecContext) {
		var buf bytes.Buffer
		DeferCleanup(captureLog(&buf))
		c.FailNotify(control, "", s.notify.pick(name), 20, errors.New("no watchers"))
		_, err := s.write(ctx, obj, []byte("d"), nil, true, now, &objv{write: newWriteVersion()})
		Expect(err).NotTo(HaveOccurred(), "svc_sys_obj_cache.cc:346-348: not fatal")
		Expect(logRecords(&buf)).To(ContainElement(SatisfyAll(
			HaveKeyWithValue("level", "ERROR"),
			HaveKeyWithValue("oid", "plain"),
			HaveKeyWithValue("pool", root.String()),
			HaveKeyWithValue("error", ContainSubstring("no watchers")),
		)))
		_, err = cache.get(name, meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred(), "the local entry is kept")
		Expect(s.remove(ctx, obj, nil)).To(Succeed(), "nor does a failed invalidation fail a remove")
	})
})

var _ = Describe("openSysObj", func() {
	const control = "zone.rgw.control"
	var (
		c      *fakerados.Cluster
		pools  *poolCache
		params meta.ZoneParams
		o      options
	)
	BeforeEach(func() {
		DeferCleanup(captureLog(GinkgoWriter))
		c = fakerados.New()
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		pools = newPoolCache(c)
		DeferCleanup(pools.closeAll)
		params = meta.ZoneParams{DomainRoot: meta.ParsePool("zone.rgw.meta:root"), ControlPool: meta.ParsePool(control)}
		o = options{cacheEnabled: true, cacheLRUSize: 25000, cacheExpiry: 900 * time.Second, numControlOIDs: 8, maxNotifyRetries: 10}
	})

	// run runs the notifier until the spec ends and waits for every control
	// watch.
	run := func(ctx context.Context, n *notifier) {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- n.run(runCtx) }()
		DeferCleanup(func() {
			cancel()
			Eventually(done).WithTimeout(time.Second).Should(Receive())
		})
		for _, oid := range n.oids {
			Eventually(func() int { return c.Watches(control, "", oid) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(1), oid)
		}
	}

	It("builds the cache and the notifier from the options and creates the control objects", func(ctx SpecContext) {
		o = options{cacheEnabled: true, cacheLRUSize: 7, cacheExpiry: 30 * time.Second, numControlOIDs: 0, maxNotifyRetries: 3}
		sys, err := openSysObj(ctx, pools, params, o, denc.Tentacle)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Object(control, "", "notify")).NotTo(BeNil(), "rgw_num_control_oids 0 is the legacy object")
		Expect(sys.notify.oids).To(Equal([]string{"notify"}))
		Expect(sys.notify.maxRetries).To(BeEquivalentTo(3))
		Expect(sys.notify.release).To(Equal(denc.Tentacle))
		Expect(sys.release).To(Equal(denc.Tentacle))
		Expect(sys.cache.limit).To(BeEquivalentTo(7))
		Expect(sys.cache.expiry).To(Equal(30 * time.Second))
		Expect(sys.cache.domainRoot).To(Equal(params.DomainRoot))
		Expect(cacheEnabled(sys.cache)).To(BeFalse(), "off until every control watch is registered")
	})

	It("enables the cache once every control watch is registered", func(ctx SpecContext) {
		sys, err := openSysObj(ctx, pools, params, o, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		run(ctx, sys.notify)
		Eventually(func() bool { return cacheEnabled(sys.cache) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeTrue())
	})

	It("keeps the cache off when rgw_cache_enabled is false, and still watches and sends", func(ctx SpecContext) {
		o.cacheEnabled = false
		sys, err := openSysObj(ctx, pools, params, o, denc.Squid)
		Expect(err).NotTo(HaveOccurred())
		run(ctx, sys.notify)
		Consistently(func() bool { return cacheEnabled(sys.cache) }).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeFalse())
		obj := sysObj{pool: params.DomainRoot, oid: "plain"}
		_, err = sys.write(ctx, obj, []byte("d"), nil, true, time.Time{}, &objv{write: newWriteVersion()})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Notifies(control, "", sys.notify.pick(normalName(obj.pool, obj.oid)))).To(HaveLen(1))
	})

	It("fails naming the control pool it cannot open", func(ctx SpecContext) {
		c.FailPool(control)
		_, err := openSysObj(ctx, pools, params, o, denc.Squid)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		Expect(err).To(MatchError(ContainSubstring(control)))
	})
})
