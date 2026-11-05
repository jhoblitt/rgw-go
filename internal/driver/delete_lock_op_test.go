package driver_test

import (
	"bytes"
	"context"
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
