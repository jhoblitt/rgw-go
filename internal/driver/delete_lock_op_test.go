package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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

// statHook is an object store whose StatObject runs fn once, after the first
// read it answers: a write landing between a delete's object-lock check and
// its removal.
type statHook struct {
	op.ObjectStore
	once sync.Once
	fn   func()
}

func (h *statHook) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	st, err := h.ObjectStore.StatObject(ctx, rec, key)
	h.once.Do(h.fn)
	return st, err
}

// RGWDeleteObj and RGWDeleteMultiObj check a named version's retention and
// legal hold through verify_object_lock before they remove it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5186-L5221 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5576-L5611 at v20.2.4).
var _ = Describe("DeleteObject and DeleteObjects over the RADOS store on a bucket with object lock", func() {
	const zgID = "zg-ceph-objectstore"
	var (
		c     *fakerados.Cluster
		ac    *aroundCluster
		hooks *writeHooks
		s     *driver.Store
		env   *op.Env
		rec   *op.BucketRecord
		alice op.Identity
		clock atomic.Int64
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	BeforeEach(func(ctx SpecContext) {
		clock.Store(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).UnixNano())
		c = newPutCluster()
		c.SetClock(now)
		hooks = &writeHooks{}
		ac = &aroundCluster{Cluster: c}
		ac.set(hooks.around)
		r := denc.Squid
		var err error
		s, err = driver.Open(ctx, ac, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
		DeferCleanup(s.Close)
		DeferCleanup(func() {
			ac.set(nil)
			settle(s)
		})
		env = s.Env()
		env.Authz = op.OwnerOnly{}
		env.Now = now
		u := meta.NewUserInfo()
		u.UserID, u.DisplayName = meta.UserID{ID: "alice"}, "Alice"
		alice = op.Identity{User: &u, Owner: initiator, OpMask: op.OpTypeAll}

		rec, err = s.CreateBucket(ctx, op.CreateBucketParams{
			Name: "locked", Owner: initiator, Zonegroup: zgID,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     attrsWithACL(initiator), Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		rec.Info.Flags |= meta.BucketVersioned | meta.BucketObjLockEnabled
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
		rec, err = s.GetBucket(ctx, "", "locked")
		Expect(err).NotTo(HaveOccurred())
		for _, v := range []string{"v1", "v2"} {
			body := []byte("data-" + v)
			_, err := s.PutObject(ctx, rec, version(v), bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred(), "the put of %s", v)
		}
		settle(s)
	})

	retention := func(mode string) []byte {
		e := denc.NewEncoder()
		meta.ObjectRetention{Mode: mode, RetainUntil: time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC)}.Encode(e, denc.Squid)
		return e.Bytes()
	}
	// setAttrs writes attrs to key's version, as PutObjectRetention and
	// PutObjectAcl do, which gives its head a fresh write tag.
	setAttrs := func(ctx context.Context, key meta.ObjKey, attrs map[string][]byte) {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue(), "version %v", key)
		Expect(s.SetObjectAttrs(ctx, st, attrs, nil)).To(Succeed())
		settle(s)
	}
	lockKey := func(ctx context.Context, key meta.ObjKey, mode string) {
		GinkgoHelper()
		setAttrs(ctx, key, map[string][]byte{meta.AttrObjectRetention: retention(mode)})
	}
	lockVersion := func(ctx context.Context, instance, mode string) {
		GinkgoHelper()
		lockKey(ctx, version(instance), mode)
	}
	aclWrite := func(ctx context.Context, instance string) {
		GinkgoHelper()
		setAttrs(ctx, version(instance), map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(initiator, "Alice "+instance))})
	}
	legalHold := func(ctx context.Context, key meta.ObjKey) {
		GinkgoHelper()
		setAttrs(ctx, key, map[string][]byte{meta.AttrObjectLegalHold: objectlock.EncodeLegalHold(meta.ObjectLegalHold{Status: "ON"})})
	}
	compliance := func(ctx context.Context, key meta.ObjKey) {
		GinkgoHelper()
		lockKey(ctx, key, "COMPLIANCE")
	}
	// getFirst is a GET of name, whose follow of its OLH replays a delete's
	// pending entries, and trims them or clears the OLH, before that delete's
	// own replay, with nothing of the write tag the delete's check read. Only
	// a replay takes the entries off the key's OLH entry, or the entry itself.
	getFirst := func(ctx context.Context, name string) {
		GinkgoHelper()
		_, err := s.StatObject(ctx, rec, meta.ObjKey{Name: name})
		Expect(err).NotTo(HaveOccurred())
		e, ok := c.OLHEntry(rookIndexPool, "", indexShardOID(rec, name), name)
		if ok {
			Expect(e.PendingLog).To(BeEmpty(), "the GET's replay applied the delete's entries")
		}
	}

	// run is a DeleteObject of key, its completions left pending.
	run := func(ctx context.Context, key meta.ObjKey, header bool) (*op.DeleteObject, error) {
		r := &op.Request{Method: http.MethodDelete, Bucket: "locked", Object: key, Identity: alice, Env: env, Header: http.Header{}, Query: url.Values{}}
		o := &op.DeleteObject{BypassHeader: header}
		err := op.Run(ctx, o, r)
		return o, err
	}
	del := func(ctx context.Context, key meta.ObjKey, header bool) (*op.DeleteObject, error) {
		o, err := run(ctx, key, header)
		settle(s)
		return o, err
	}
	multi := func(ctx context.Context, header bool, keys ...meta.ObjKey) []op.DeleteResult {
		GinkgoHelper()
		var (
			mu      sync.Mutex
			results []op.DeleteResult
		)
		es := make([]op.DeleteObjectsEntry, len(keys))
		for i, k := range keys {
			es[i] = op.DeleteObjectsEntry{Key: k}
		}
		o := &op.DeleteObjects{
			Parse:  func([]byte) ([]op.DeleteObjectsEntry, error) { return es, nil },
			Status: func(err error) { Fail("the request was refused: " + err.Error()) },
			Begin:  func() error { return nil },
			Result: func(res op.DeleteResult) {
				mu.Lock()
				defer mu.Unlock()
				results = append(results, res)
			},
			BypassHeader: header,
		}
		r := &op.Request{
			Method: http.MethodPost, Bucket: "locked", Identity: alice, Env: env,
			Header: http.Header{"Content-Length": {"1"}}, Query: url.Values{}, Body: strings.NewReader("x"), ContentLength: 1,
		}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		settle(s)
		Expect(results).To(HaveLen(len(keys)))
		return results
	}
	// deleteVia deletes key through DeleteObject, or through DeleteObjects
	// when viaMulti is set, and returns its answer.
	deleteVia := func(ctx context.Context, viaMulti bool, key meta.ObjKey, header bool) error {
		if viaMulti {
			return multi(ctx, header, key)[0].Err
		}
		_, err := del(ctx, key, header)
		return err
	}
	expectForbidden := func(viaMulti bool, err error) {
		GinkgoHelper()
		Expect(err).To(MatchError(op.ErrAccessDenied))
		if !viaMulti {
			Expect(op.AsError(err).Message).To(Equal("forbidden by object lock"))
		}
	}

	shard := func() string { return indexShardOID(rec, "k") }
	list := func() []rgwcls.DirEntry { return c.ListEntries(rookIndexPool, "", shard(), "k") }
	// listedKey is the list entries of key name, a delete marker's by what
	// it is and a version's by its instance.
	listedKey := func(name string) []string {
		var out []string
		for _, en := range c.ListEntries(rookIndexPool, "", indexShardOID(rec, name), name) {
			if en.Flags&rgwcls.FlagDeleteMarker != 0 {
				out = append(out, "a delete marker")
				continue
			}
			out = append(out, en.Key.Instance)
		}
		return out
	}
	listed := func() []string { return listedKey("k") }
	instanceEntry := func(instance string) bool {
		_, ok := c.InstanceEntry(rookIndexPool, "", shard(), rgwcls.ObjKey{Name: "k", Instance: instance})
		return ok
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
	head := func(instance string) *fakerados.Object {
		return c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_"+version(instance).OID())
	}
	versions := func(ctx context.Context) []string {
		GinkgoHelper()
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 100, ListVersions: true})
		Expect(err).NotTo(HaveOccurred())
		return versionIDs(res.Entries)
	}
	unlinks := func() int {
		n := 0
		for _, w := range c.WritesTo(rookIndexPool, "", shard()) {
			if callsMethod(w.Steps(), "bucket_unlink_instance") {
				n++
			}
		}
		return n
	}
	// atUnlink arms fn to run once the n-th later unlink of a version has
	// landed, before the replay that removes it.
	atUnlink := func(n int, fn func()) {
		hooks.add(&writeHook{match: func(w *radosclient.WriteOp) bool { return callsMethod(w.Steps(), "bucket_unlink_instance") }, n: n, fn: fn})
	}
	// expectKept checks version instance is as it was: its head, its instance
	// entry and its list entry, listed by ListObjectVersions, and current
	// again when it was current.
	expectKept := func(ctx context.Context, instance string, wasCurrent bool) {
		GinkgoHelper()
		Expect(head(instance)).NotTo(BeNil(), "the version's head")
		Expect(instanceEntry(instance)).To(BeTrue(), "its instance entry")
		Expect(listed()).To(ConsistOf("v1", "v2"), "its list entry, and the other version's")
		Expect(versions(ctx)).To(ContainElement("k@"+instance), "ListObjectVersions lists it")
		want := []string{"v2"}
		if wasCurrent {
			want = []string{instance}
		}
		Expect(currents()).To(Equal(want), "one version current, as before the delete")
	}

	DescribeTable("refuses a locked version and keeps it",
		func(ctx SpecContext, viaMulti bool, instance, mode string, header bool) {
			lockVersion(ctx, instance, mode)
			expectForbidden(viaMulti, deleteVia(ctx, viaMulti, version(instance), header))
			expectKept(ctx, instance, instance == "v2")
		},
		Entry("COMPLIANCE on a noncurrent version, DeleteObject with the header", false, "v1", "COMPLIANCE", true),
		Entry("GOVERNANCE on the current version, DeleteObject without the header", false, "v2", "GOVERNANCE", false),
		Entry("COMPLIANCE on a noncurrent version, DeleteObjects with the header", true, "v1", "COMPLIANCE", true),
		Entry("GOVERNANCE on the current version, DeleteObjects without the header", true, "v2", "GOVERNANCE", false),
	)
	DescribeTable("removes a GOVERNANCE version with the header and the owner's bypass",
		func(ctx SpecContext, viaMulti bool) {
			lockVersion(ctx, "v1", "GOVERNANCE")
			Expect(deleteVia(ctx, viaMulti, version("v1"), true)).To(Succeed())
			Expect(head("v1")).To(BeNil())
			Expect(listed()).To(Equal([]string{"v2"}))
		},
		Entry("DeleteObject", false),
		Entry("DeleteObjects", true),
	)
	It("writes a delete marker over a locked current version, which it keeps", func(ctx SpecContext) {
		lockVersion(ctx, "v2", "COMPLIANCE")
		o, err := del(ctx, version(""), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.DeleteMarker).To(BeTrue())
		Expect(listed()).To(Equal([]string{"a delete marker", "v2", "v1"}))
		Expect(head("v2")).NotTo(BeNil())
	})
	It("removes a delete marker by its version, whose state holds no lock, the version below it current again", func(ctx SpecContext) {
		lockVersion(ctx, "v2", "COMPLIANCE")
		m, err := del(ctx, version(""), false)
		Expect(err).NotTo(HaveOccurred())
		o, err := del(ctx, version(m.VersionID), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.VersionID).To(Equal(m.VersionID))
		Expect(o.DeleteMarker).To(BeTrue())
		Expect(listed()).To(Equal([]string{"v2", "v1"}))
		Expect(currents()).To(Equal([]string{"v2"}))
	})

	// An unlink at an epoch below the OLH entry's, which a multisite radosgw
	// makes, takes a delete marker's list entry and leaves its instance entry
	// (docs/ceph-upstream-bugs.md, "cls_rgw's bucket_unlink_instance at an
	// older epoch leaves a delete marker's instance entry"). Read by its
	// version the marker is then no version, and that read is the removal's
	// Expect.
	Describe("with a delete marker whose list entry an older-epoch unlink took", func() {
		var marker string
		BeforeEach(func(ctx SpecContext) {
			m, err := del(ctx, version(""), false)
			Expect(err).NotTo(HaveOccurred())
			marker = m.VersionID
			body := []byte("data-v3")
			_, err = s.PutObject(ctx, rec, version("v3"), bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(s.UnlinkObjInstanceForTest(ctx, rec, version(marker), driver.OLHUnlink{Epoch: 3})).To(Succeed())
			settle(s)
			Expect(instanceEntry(marker)).To(BeTrue(), "the marker's instance entry stays")
			Expect(listed()).To(ConsistOf("v3", "v2", "v1"), "its list entry is gone")
			st, err := s.StatObject(ctx, rec, version(marker))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeFalse())
			Expect(st.DeleteMarker).To(BeFalse(), "read by its version it is no version")
		})
		It("removes its instance entry by DeleteObject of its version, 204, as radosgw's unlink does", func(ctx SpecContext) {
			o, err := del(ctx, version(marker), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.VersionID).To(Equal(marker))
			Expect(o.DeleteMarker).To(BeTrue())
			Expect(instanceEntry(marker)).To(BeFalse(), "the orphan instance entry is removed")
			Expect(listed()).To(ConsistOf("v3", "v2", "v1"))
			Expect(currents()).To(Equal([]string{"v3"}))
		})
		It("removes its instance entry by a DeleteObjects key of its version", func(ctx SpecContext) {
			Expect(multi(ctx, false, version(marker))[0].Err).NotTo(HaveOccurred())
			Expect(instanceEntry(marker)).To(BeFalse(), "the orphan instance entry is removed")
			Expect(listed()).To(ConsistOf("v3", "v2", "v1"))
			Expect(currents()).To(Equal([]string{"v3"}))
		})
	})

	Describe("after a removal its expected state refused", func() {
		// window arms fn to run in a delete's window: between its check's read
		// and its unlink, or between the unlink and the removal.
		window := func(afterUnlink bool, fn func()) {
			if afterUnlink {
				atUnlink(1, fn)
				return
			}
			env.Objects = &statHook{ObjectStore: s, fn: fn}
		}

		DescribeTable("answers 403 forbidden by object lock for a retention written in the window, the version listed as before",
			func(ctx SpecContext, viaMulti, afterUnlink bool, instance string) {
				window(afterUnlink, func() { lockVersion(ctx, instance, "COMPLIANCE") })
				expectForbidden(viaMulti, deleteVia(ctx, viaMulti, version(instance), false))
				expectKept(ctx, instance, instance == "v2")
			},
			Entry("between the check and the unlink, a noncurrent version", false, false, "v1"),
			Entry("between the check and the unlink, the current version", false, false, "v2"),
			Entry("between the unlink and the removal, a noncurrent version", false, true, "v1"),
			Entry("between the unlink and the removal, the current version", false, true, "v2"),
			Entry("between the unlink and the removal, the current version, DeleteObjects", true, true, "v2"),
			Entry("between the check and the unlink, a noncurrent version, DeleteObjects", true, false, "v1"),
		)
		DescribeTable("removes the version on the retry after an ACL written in the window, answering once",
			func(ctx SpecContext, afterUnlink bool, instance string) {
				window(afterUnlink, func() { aclWrite(ctx, instance) })
				o, err := del(ctx, version(instance), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(o.VersionID).To(Equal(instance), "the retry's removal answers")
				Expect(head(instance)).To(BeNil())
				Expect(versions(ctx)).NotTo(ContainElement("k@" + instance))
			},
			Entry("between the check and the unlink", false, "v1"),
			Entry("between the unlink and the removal, the current version", true, "v2"),
		)
		DescribeTable("gives up once a writer has changed the version on every try, the version still listed",
			func(ctx SpecContext, viaMulti bool, instance string) {
				for n := range 4 {
					atUnlink(n+1, func() { aclWrite(ctx, instance) })
				}
				c.ResetCounters()
				err := deleteVia(ctx, viaMulti, version(instance), false)
				if viaMulti {
					Expect(err).To(MatchError(op.ErrConcurrentModification), "a key's lost race is its error")
				} else {
					Expect(err).NotTo(HaveOccurred(), "radosgw's lost race, 204")
				}
				Expect(unlinks()).To(Equal(4), "the first try and its three re-checks")
				expectKept(ctx, instance, instance == "v2")
			},
			Entry("DeleteObject, a noncurrent version", false, "v1"),
			Entry("DeleteObjects, the current version", true, "v2"),
		)
	})

	Describe("when another request's replay applies the delete's removal first", func() {
		DescribeTable("keeps a version locked since the check, with its lock, answering 403 forbidden by object lock",
			func(ctx SpecContext, viaMulti bool, instance string, lock func(context.Context, meta.ObjKey), attr string) {
				atUnlink(1, func() {
					lock(ctx, version(instance))
					getFirst(ctx, "k")
				})
				expectForbidden(viaMulti, deleteVia(ctx, viaMulti, version(instance), false))
				expectKept(ctx, instance, instance == "v2")
				Expect(head(instance).Xattrs).To(HaveKey(attr), "the lock written after the check")
			},
			Entry("a legal hold on a noncurrent version", false, "v1", legalHold, meta.AttrObjectLegalHold),
			Entry("a legal hold on the current version", false, "v2", legalHold, meta.AttrObjectLegalHold),
			Entry("a COMPLIANCE retention on the current version, DeleteObjects", true, "v2", compliance, meta.AttrObjectRetention),
		)
		DescribeTable("removes a version whose lock the delete passed, whichever replay applies the removal",
			func(ctx SpecContext, viaMulti bool, instance, mode string, header, first bool) {
				if mode != "" {
					lockVersion(ctx, instance, mode)
				}
				if first {
					atUnlink(1, func() { getFirst(ctx, "k") })
				}
				Expect(deleteVia(ctx, viaMulti, version(instance), header)).To(Succeed())
				Expect(head(instance)).To(BeNil(), "the version's head")
				Expect(instanceEntry(instance)).To(BeFalse(), "its instance entry")
				Expect(listed()).NotTo(ContainElement(instance))
			},
			Entry("no lock on a noncurrent version, the GET's replay first", false, "v1", "", false, true),
			Entry("no lock on the current version, the GET's replay first", false, "v2", "", false, true),
			Entry("no lock on a noncurrent version, its own replay", false, "v1", "", false, false),
			Entry("a GOVERNANCE retention bypassed, the GET's replay first", false, "v1", "GOVERNANCE", true, true),
			Entry("a GOVERNANCE retention bypassed on the current version, the GET's replay first, DeleteObjects", true, "v2", "GOVERNANCE", true, true),
		)
		// The admin removals pass the store no expected state, as they check no
		// lock (docs/differences.md, "Admin removals ignore object lock").
		It("removes a locked version for a delete with no expected state, the GET's replay first", func(ctx SpecContext) {
			lockVersion(ctx, "v1", "COMPLIANCE")
			atUnlink(1, func() { getFirst(ctx, "k") })
			Expect(s.DeleteObject(ctx, rec, version("v1"), op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
			settle(s)
			Expect(head("v1")).To(BeNil(), "the version's head")
			Expect(instanceEntry("v1")).To(BeFalse(), "its instance entry")
			Expect(listed()).To(Equal([]string{"v2"}))
		})
		// The delete's check after the GET's replay kept the version removes
		// it, but another writer changes its instance entry before each of the
		// removal's compared prepares. At their bound the removal is deferred.
		// No removal is left in the log for a later replay, so the delete
		// answers SlowDown, and its retry removes the version.
		It("answers SlowDown for a delete whose removal after another replay kept the version another writer keeps refusing", func(ctx SpecContext) {
			lockVersion(ctx, "v1", "COMPLIANCE")
			var changers []*driver.IndexOp
			atUnlink(1, func() {
				getFirst(ctx, "k")
				ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
					compared := false
					for _, st := range w.Steps() {
						if _, ok := st.(*radosclient.OmapCmpStep); ok {
							compared = true
						}
					}
					if compared && callsMethod(w.Steps(), "bucket_prepare_op") {
						x := s.NewIndexOpForTest(rec, version("v1"), fmt.Sprintf("changer-%d", len(changers)))
						changers = append(changers, x)
						Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
					}
					return hooks.around(w, send)
				})
			})
			_, err := s.DeleteObject(ctx, rec, version("v1"), op.DeleteParams{})
			ac.set(hooks.around)
			Expect(err).To(MatchError(op.ErrSlowDown))
			Expect(op.AsError(err).Status).To(Equal(http.StatusServiceUnavailable))
			Expect(changers).To(HaveLen(driver.OLHAttempts), "one refused prepare a round, up to the bound")
			for _, x := range changers {
				x.Cancel()
			}
			settle(s)
			Expect(head("v1")).NotTo(BeNil(), "the version's head, not removed")
			Expect(listed()).To(Equal([]string{"v2"}), "unlisted")
			Expect(s.DeleteObject(ctx, rec, version("v1"), op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}), "the client's retry")
			settle(s)
			Expect(head("v1")).To(BeNil(), "removed by the retry")
			Expect(instanceEntry("v1")).To(BeFalse())
			Expect(listed()).To(Equal([]string{"v2"}))
		})
		// The delete's own replay, the GET's replay having kept v1 for its
		// lock, meets v2's removal, which another writer refuses for 20 rounds
		// of a second each; the delete's check then removes v1, which another
		// writer refuses on every round. The check shares the deadline the
		// replay's first lost round started, 32 s on, so it is deferred 14
		// rounds in, after 33 backoffs in all, and the delete answers
		// SlowDown. With a deadline of its own the check would back off for
		// 32 s more.
		It("answers SlowDown once the backoff deadline its replay started passes in its check", func(ctx SpecContext) {
			body := []byte("data-v3")
			_, err := s.PutObject(ctx, rec, version("v3"), bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			lockVersion(ctx, "v1", "COMPLIANCE")
			var sleeps atomic.Int32
			driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
				sleeps.Add(1)
				clock.Add(int64(time.Second))
				return nil
			})
			refused := map[string]int{}
			var changers []*driver.IndexOp
			atUnlink(1, func() {
				getFirst(ctx, "k")
				Expect(s.DeleteObject(ctx, rec, version("v2"), op.DeleteParams{SkipOLHUpdate: true})).Error().To(Succeed(), "v2's removal logged, its replay left")
				ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
					if x, ok := removalPrepareKey(w); ok && (x == "v1" || x == "v2" && refused["v2"] < 20) {
						refused[x]++
						ch := s.NewIndexOpForTest(rec, version(x), fmt.Sprintf("changer-%d", len(changers)))
						changers = append(changers, ch)
						Expect(ch.Prepare(ctx, rgwcls.OpAdd)).To(Succeed(), "another writer's prepare, which changes %s's instance entry", x)
					}
					return hooks.around(w, send)
				})
			})
			_, err = s.DeleteObject(ctx, rec, version("v1"), op.DeleteParams{})
			ac.set(hooks.around)
			Expect(err).To(MatchError(op.ErrSlowDown))
			Expect(refused).To(Equal(map[string]int{"v2": 20, "v1": 14}), "the check's last refused round, past the deadline, defers it")
			Expect(sleeps.Load()).To(BeEquivalentTo(33), "a backoff after every lost round but the deferred one")
			for _, x := range changers {
				x.Cancel()
			}
			settle(s)
			Expect(head("v2")).To(BeNil(), "v2, removed on its 21st round")
			Expect(head("v1")).NotTo(BeNil(), "v1, its removal deferred")
			Expect(listed()).To(Equal([]string{"v3"}), "v1 unlisted")
		})
		// A GOVERNANCE retention is one a delete with the bypass passes, so a
		// replay cannot tell a removal that bypassed it from one that raced it,
		// and removes the version as radosgw's does (docs/differences.md, "A
		// replay keeps another writer's version whose object lock holds").
		It("removes a version a GOVERNANCE retention locked since the check, the GET's replay first", func(ctx SpecContext) {
			atUnlink(1, func() {
				lockVersion(ctx, "v1", "GOVERNANCE")
				getFirst(ctx, "k")
			})
			_, err := del(ctx, version("v1"), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(head("v1")).To(BeNil(), "the version's head")
			Expect(listed()).To(Equal([]string{"v2"}))
		})
		It("keeps, listed again, a key's only version held since the check, the GET's replay first clearing its OLH", func(ctx SpecContext) {
			only := meta.ObjKey{Name: "only", Instance: "o1"}
			body := []byte("data-o1")
			_, err := s.PutObject(ctx, rec, only, bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			atUnlink(1, func() {
				legalHold(ctx, only)
				getFirst(ctx, "only")
				_, ok := c.OLHEntry(rookIndexPool, "", indexShardOID(rec, "only"), "only")
				Expect(ok).To(BeFalse(), "the GET's replay cleared the key's OLH")
			})
			_, err = del(ctx, only, false)
			expectForbidden(false, err)
			Expect(c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_"+only.OID())).NotTo(BeNil(), "the version's head")
			Expect(listedKey("only")).To(Equal([]string{"o1"}), "listed again")
		})
		// An admin removal unlinked the version, another replay kept it for its
		// hold, and a writer linked it again before the removal's check.
		It("leaves a version linked again since another replay kept it, for a delete with no expected state", func(ctx SpecContext) {
			legalHold(ctx, version("v1"))
			atUnlink(1, func() {
				getFirst(ctx, "k")
				Expect(s.SetOLHForTest(ctx, rec, version("v1"), driver.OLHLink{})).To(Succeed(), "a writer links the version again")
			})
			Expect(s.DeleteObject(ctx, rec, version("v1"), op.DeleteParams{})).Error().NotTo(HaveOccurred())
			settle(s)
			Expect(head("v1")).NotTo(BeNil(), "the version's head")
			Expect(instanceEntry("v1")).To(BeTrue(), "its instance entry")
			Expect(listed()).To(ContainElement("v1"), "listed by the later link")
		})
	})

	// The request's context ends once the delete's unlink has landed, as
	// net/http ends it when the client goes; the delete's replay and its
	// check after it still run.
	Describe("when the delete's client goes after its unlink", func() {
		It("removes a GOVERNANCE version the delete bypassed, a GET's replay after", func(ctx SpecContext) {
			lockVersion(ctx, "v1", "GOVERNANCE")
			dctx, cancel := context.WithCancel(ctx)
			atUnlink(1, cancel)
			_, err := run(dctx, version("v1"), true)
			Expect(err).To(Or(Not(HaveOccurred()), MatchError(context.Canceled)), "the client is gone, so either answer reaches no one")
			settle(s)
			getFirst(ctx, "k")
			settle(s)
			Expect(head("v1")).To(BeNil(), "the version's head")
			Expect(instanceEntry("v1")).To(BeFalse(), "its instance entry")
			Expect(listed()).To(Equal([]string{"v2"}))
		})
		It("keeps, listed, a version held since the check, a GET's replay after", func(ctx SpecContext) {
			dctx, cancel := context.WithCancel(ctx)
			atUnlink(1, func() {
				legalHold(ctx, version("v1"))
				cancel()
			})
			_, err := run(dctx, version("v1"), false)
			Expect(err).To(HaveOccurred())
			settle(s)
			getFirst(ctx, "k")
			settle(s)
			expectKept(ctx, "v1", false)
			Expect(head("v1").Xattrs).To(HaveKey(meta.AttrObjectLegalHold))
		})
	})

	// An attr write after the check, which is no lock, and every pass of the
	// delete's replay losing its head op to another writer's apply: the
	// delete's removal stays in the log, for the next replay.
	It("leaves to the next replay a removal its own replay could not apply", func(ctx SpecContext) {
		appliesLog := func(w *radosclient.WriteOp) bool {
			cmp, set := false, false
			for _, st := range w.Steps() {
				switch x := st.(type) {
				case *radosclient.CmpXattrStep:
					cmp = cmp || x.Name == meta.AttrOLHVer
				case *radosclient.SetXattrStep:
					set = set || (x.Name == meta.AttrOLHVer && len(x.Value) > 0)
				}
			}
			return cmp && set
		}
		var canceled atomic.Int32
		var armed atomic.Bool
		ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
			if armed.Load() && appliesLog(w) && canceled.Load() < 11 {
				canceled.Add(1)
				return 0, fmt.Errorf("another writer's apply came first: %w", radosclient.ErrCanceled)
			}
			return hooks.around(w, send)
		})
		atUnlink(1, func() {
			aclWrite(ctx, "v2")
			armed.Store(true)
		})
		_, err := run(ctx, version("v2"), false)
		armed.Store(false)
		Expect(err).NotTo(HaveOccurred(), "the unlink landed and its removal is left to the next replay")
		Expect(canceled.Load()).To(BeEquivalentTo(11), "every pass of the delete's replay lost its head op")
		settle(s)
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(st.Key.Instance).To(Equal("v1"), "the key resolves to the version the unlink promoted")
		Expect(head("v2")).To(BeNil(), "the GET's replay removed the version, which no lock holds")
		Expect(listed()).To(Equal([]string{"v1"}))
	})

	// The bucket's versioning suspended, which a bucket.instance metadata put
	// and radosgw's PutBucketVersioning race reach on a lock bucket; k holds
	// v1 and v2 and no null version.
	Describe("on a suspended bucket", func() {
		BeforeEach(func(ctx SpecContext) {
			rec.Info.Flags |= meta.BucketVersionsSuspended
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
			var err error
			rec, err = s.GetBucket(ctx, "", "locked")
			Expect(err).NotTo(HaveOccurred())
		})
		// lockedNullWrite is a null write landing in a delete's window, given
		// a COMPLIANCE retention once it has landed.
		lockedNullWrite := func(ctx context.Context) {
			body := []byte("null-data")
			_, err := s.PutObject(ctx, rec, version(""), bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			lockVersion(ctx, meta.NullInstance, "COMPLIANCE")
		}

		It("writes the null delete marker for a delete naming no version where no null version exists", func(ctx SpecContext) {
			o, err := del(ctx, version(""), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.DeleteMarker).To(BeTrue())
			Expect(o.VersionID).To(Equal(meta.NullInstance))
			Expect(listed()).To(ConsistOf("a delete marker", "v2", "v1"))
		})
		It("writes the null delete marker over the null delete marker, for a delete naming no version", func(ctx SpecContext) {
			_, err := del(ctx, version(""), false)
			Expect(err).NotTo(HaveOccurred())
			o, err := del(ctx, version(""), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.DeleteMarker).To(BeTrue(), "the second marker was written")
			Expect(listed()).To(ConsistOf("a delete marker", "v2", "v1"))
		})
		DescribeTable("keeps a null version locked between the check's read, which found none, and the removal",
			func(ctx SpecContext, key meta.ObjKey) {
				env.Objects = &statHook{ObjectStore: s, fn: func() { lockedNullWrite(ctx) }}
				_, err := del(ctx, key, false)
				expectForbidden(false, err)
				st, err := s.StatObject(ctx, rec, version(meta.NullInstance))
				Expect(err).NotTo(HaveOccurred())
				Expect(st.Exists).To(BeTrue(), "the COMPLIANCE null version is kept")
				Expect(st.Attrs).To(HaveKey(meta.AttrObjectRetention))
			},
			Entry("a delete naming no version, whose null delete marker would replace it", version("")),
			Entry("a delete naming versionId=null", version(meta.NullInstance)),
		)

		// fresh has no OLH head, so a null write on it lands as a plain
		// object, which nullMarkerExpect's read finds without olh.ver.
		fresh := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "fresh", Instance: instance} }
		lockedPlainNullWrite := func(ctx context.Context) {
			body := []byte("null-data")
			_, err := s.PutObject(ctx, rec, fresh(""), bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			lockKey(ctx, fresh(meta.NullInstance), "COMPLIANCE")
		}
		DescribeTable("keeps a plain null object locked between the check's read, which found no head, and the removal",
			func(ctx SpecContext, key meta.ObjKey) {
				env.Objects = &statHook{ObjectStore: s, fn: func() { lockedPlainNullWrite(ctx) }}
				_, err := del(ctx, key, false)
				expectForbidden(false, err)
				st, err := s.StatObject(ctx, rec, fresh(meta.NullInstance))
				Expect(err).NotTo(HaveOccurred())
				Expect(st.Exists).To(BeTrue(), "the COMPLIANCE null object is kept")
				Expect(st.Attrs).To(HaveKey(meta.AttrObjectRetention))
			},
			Entry("a delete naming no version, whose null delete marker would replace it", fresh("")),
			Entry("a delete naming versionId=null", fresh(meta.NullInstance)),
		)
		// The null version's data is the OLH head's: a null delete marker's
		// link logs its removal, and so does the unlink of versionId=null.
		DescribeTable("keeps a null version held since the check, with its hold, the GET's replay first",
			func(ctx SpecContext, key meta.ObjKey, method string) {
				body := []byte("null-data")
				_, err := s.PutObject(ctx, rec, version(""), bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				hooks.add(&writeHook{match: func(w *radosclient.WriteOp) bool { return callsMethod(w.Steps(), method) }, n: 1, fn: func() {
					legalHold(ctx, version(meta.NullInstance))
					getFirst(ctx, "k")
				}})
				_, err = del(ctx, key, false)
				expectForbidden(false, err)
				st, err := s.StatObject(ctx, rec, version(meta.NullInstance))
				Expect(err).NotTo(HaveOccurred())
				Expect(st.Exists).To(BeTrue(), "the null version is kept")
				Expect(st.Attrs).To(HaveKey(meta.AttrObjectLegalHold))
				Expect(listed()).To(ContainElement(""), "listed again")
			},
			Entry("a delete naming no version, whose null delete marker would replace it", version(""), "bucket_link_olh"),
			Entry("a delete naming versionId=null", version(meta.NullInstance), "bucket_unlink_instance"),
		)
		// The GET's replay trims where it would clear the OLH, whose head
		// object holds the null data it keeps.
		It("keeps a key's only version, a null version held since the check, with its hold, the GET's replay first", func(ctx SpecContext) {
			body := []byte("null-data")
			_, err := s.PutObject(ctx, rec, fresh(""), bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			atUnlink(1, func() {
				legalHold(ctx, fresh(meta.NullInstance))
				getFirst(ctx, "fresh")
			})
			_, err = del(ctx, fresh(meta.NullInstance), false)
			expectForbidden(false, err)
			st, err := s.StatObject(ctx, rec, fresh(meta.NullInstance))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeTrue(), "the null version is kept")
			Expect(st.Attrs).To(HaveKey(meta.AttrObjectLegalHold))
			Expect(listedKey("fresh")).To(Equal([]string{""}), "listed again")
		})
		It("removes a key's only version, a locked null version, for a delete with no expected state, the GET's replay first, and clears its OLH", func(ctx SpecContext) {
			lockedPlainNullWrite(ctx)
			atUnlink(1, func() { getFirst(ctx, "fresh") })
			Expect(s.DeleteObject(ctx, rec, fresh(meta.NullInstance), op.DeleteParams{})).Error().NotTo(HaveOccurred())
			settle(s)
			Expect(c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_"+fresh("").OID())).To(BeNil(), "the head, its data and its OLH")
			_, ok := c.OLHEntry(rookIndexPool, "", indexShardOID(rec, "fresh"), "fresh")
			Expect(ok).To(BeFalse(), "no OLH entry")
			Expect(listedKey("fresh")).To(BeEmpty())
		})
		It("writes the null delete marker for a key that never existed", func(ctx SpecContext) {
			o, err := del(ctx, fresh(""), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.DeleteMarker).To(BeTrue())
			Expect(listedKey("fresh")).To(ConsistOf("a delete marker"))
		})
		DescribeTable("writes the marker of every one of concurrent deletes naming no version, none losing its race",
			func(ctx SpecContext, key meta.ObjKey) {
				const n = 8
				var wg sync.WaitGroup
				outs := make([]*op.DeleteObject, n)
				errs := make([]error, n)
				for i := range n {
					wg.Go(func() {
						defer GinkgoRecover()
						outs[i], errs[i] = run(ctx, key, false)
					})
				}
				wg.Wait()
				settle(s)
				for i := range n {
					Expect(errs[i]).NotTo(HaveOccurred())
					Expect(outs[i].DeleteMarker).To(BeTrue(), "delete %d wrote its marker", i)
				}
			},
			Entry("on a key that never existed", fresh("")),
			Entry("on a key with versions and no null version", version("")),
		)
		It("answers versionId=null 204 where no null version exists, removing nothing", func(ctx SpecContext) {
			o, err := del(ctx, version(meta.NullInstance), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.VersionID).To(BeEmpty())
			Expect(listed()).To(ConsistOf("v2", "v1"))
		})
	})
})
