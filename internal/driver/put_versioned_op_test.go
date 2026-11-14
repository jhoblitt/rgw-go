package driver_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// afterOLHInit runs fn once, after the first write to the OLH head oid that
// leaves it holding a pending attr: a writer's init, the last op it sends
// before its link.
func afterOLHInit(c *fakerados.Cluster, oid string, fn func()) {
	c.AfterWrite(testDataPool, "", oid, func() {
		o := c.Object(testDataPool, "", oid)
		if o == nil {
			return
		}
		for name := range o.Xattrs {
			if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
				c.AfterWrite(testDataPool, "", oid, nil)
				fn()
				return
			}
		}
	})
}

// A suspended bucket's PUT writes the null version, whose id a client knows
// before the write links it: a DeleteObject without a versionId replaces it
// with the null delete marker, and one naming versionId=null removes it. The
// class lists a version only once its link lands, so no other delete can
// name a version an rgw-go write has not linked yet.
var _ = Describe("a versioned PutObject whose version a delete removes before its link, over the RADOS store", func() {
	const zgID = "zg-ceph-objectstore"
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		env   *op.Env
		alice op.Identity
		rec   *op.BucketRecord
		v1    string
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_cache_enabled": "false"},
			time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
		DeferCleanup(s.Close)
		env = s.Env()
		env.Authz = op.OwnerOnly{}
		u := meta.NewUserInfo()
		u.UserID, u.DisplayName = meta.UserID{ID: "alice"}, "Alice"
		alice = op.Identity{User: &u, Owner: initiator, OpMask: op.OpTypeAll}

		var err error
		rec, err = s.CreateBucket(ctx, op.CreateBucketParams{
			Name: "b", Owner: initiator, Zonegroup: zgID,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     attrsWithACL(initiator), Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		setFlags := func(flags uint32) {
			GinkgoHelper()
			rec.Info.Flags = flags
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
			rec, err = s.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
		}
		setFlags(rec.Info.Flags | meta.BucketVersioned)
		put := &op.PutObject{Body: strings.NewReader("one"), Size: 3}
		Expect(op.Run(ctx, put, reqOnB(http.MethodPut, meta.ObjKey{Name: "k"}, alice, env))).To(Succeed())
		v1 = put.VersionID
		Expect(v1).NotTo(BeEmpty())
		settle(s)
		setFlags(rec.Info.Flags | meta.BucketVersionsSuspended)
		s.SetRandForTest(fixedRand(secondPrefix))
	})
	AfterEach(func() { settle(s) })

	olhOID := func() string { return rec.Info.Bucket.Marker + "_k" }
	tail := func() string { return rec.Info.Bucket.Marker + "__shadow_." + secondPrefix + "_1" }
	versions := func(ctx context.Context) []string {
		GinkgoHelper()
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{ListVersions: true, MaxKeys: 1000})
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, e := range res.Entries {
			v := e.Key.Instance
			if v == "" {
				v = meta.NullInstance
			}
			if e.DeleteMarker {
				v += " (delete marker)"
			}
			out = append(out, v)
		}
		return out
	}

	DescribeTable("answers the PUT with the null version, and leaves what the delete made of the key",
		func(ctx SpecContext, del meta.ObjKey, marker bool) {
			body := bytes.Repeat([]byte("n"), 6<<20)
			deleted := false
			afterOLHInit(c, olhOID(), func() {
				d := &op.DeleteObject{}
				Expect(op.Run(ctx, d, reqOnB(http.MethodDelete, del, alice, env))).To(Succeed())
				Expect(d.VersionID).To(Equal(meta.NullInstance))
				deleted = true
			})
			put := &op.PutObject{Body: bytes.NewReader(body), Size: int64(len(body))}
			Expect(op.Run(ctx, put, reqOnB(http.MethodPut, meta.ObjKey{Name: "k"}, alice, env))).To(Succeed())
			Expect(deleted).To(BeTrue(), "the delete ran between the PUT's index completion and its link")
			Expect(put.VersionID).To(Equal(meta.NullInstance))
			settle(s)

			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			nst, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k", Instance: meta.NullInstance})
			Expect(err).NotTo(HaveOccurred())
			if marker {
				Expect([]any{st.Key.Instance, st.DeleteMarker}).To(Equal([]any{"", true}), "the OLH names the null delete marker")
				Expect([]any{nst.Exists, nst.DeleteMarker}).To(Equal([]any{false, true}), "versionId=null reads the delete marker")
				Expect(versions(ctx)).To(Equal([]string{"null (delete marker)", v1}))
			} else {
				Expect([]any{st.Key.Instance, st.Exists}).To(Equal([]any{v1, true}), "the OLH names the version before the PUT's")
				Expect([]any{nst.Exists, nst.DeleteMarker}).To(Equal([]any{false, false}), "versionId=null reads no version")
				Expect(versions(ctx)).To(Equal([]string{v1}))
			}
			Expect(chainOIDs(allGCEntries(c))).To(ConsistOf(tail()), "the PUT's tail is queued for the GC once, by the delete that removed its version")
			Expect(c.Object(testDataPool, "", olhOID()).Data).To(BeEmpty(), "the OLH head holds no null data")
		},
		Entry("a DeleteObject without a versionId, whose null delete marker replaces the null version",
			meta.ObjKey{Name: "k"}, true),
		Entry("a DeleteObject of versionId=null, which removes it",
			meta.ObjKey{Name: "k", Instance: meta.NullInstance}, false),
	)

	It("leaves the key no OLH when the delete of versionId=null removes its only version, the PUT's update clearing what its pending attr kept", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("n1"), Size: 2}, reqOnB(http.MethodPut, meta.ObjKey{Name: "k"}, alice, env))).To(Succeed())
		Expect(op.Run(ctx, &op.DeleteObject{}, reqOnB(http.MethodDelete, meta.ObjKey{Name: "k", Instance: v1}, alice, env))).To(Succeed())
		settle(s)
		Expect(versions(ctx)).To(Equal([]string{meta.NullInstance}))
		afterOLHInit(c, olhOID(), func() {
			Expect(op.Run(ctx, &op.DeleteObject{}, reqOnB(http.MethodDelete, meta.ObjKey{Name: "k", Instance: meta.NullInstance}, alice, env))).To(Succeed())
		})
		body := bytes.Repeat([]byte("n"), 6<<20)
		put := &op.PutObject{Body: bytes.NewReader(body), Size: int64(len(body))}
		Expect(op.Run(ctx, put, reqOnB(http.MethodPut, meta.ObjKey{Name: "k"}, alice, env))).To(Succeed())
		Expect(put.VersionID).To(Equal(meta.NullInstance))
		settle(s)
		Expect(c.Object(testDataPool, "", olhOID())).To(BeNil(), "no OLH head")
		_, ok := c.OLHEntry(rookIndexPool, "", indexShardOID(rec, "k"), "k")
		Expect(ok).To(BeFalse(), "no OLH entry")
		Expect(versions(ctx)).To(BeEmpty())
		Expect(chainOIDs(allGCEntries(c))).To(ConsistOf(tail()))
	})

	// The class turns a completion older than the entry's last change into a
	// cancel, which removes an entry a delete left pending
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1082-L1109 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1217-L1244 at v20.2.4),
	// so a delete of the null version that lands between the PUT's head write
	// and its completion takes the PUT's version too.
	It("answers the PUT with the null version when a delete of versionId=null lands between its head write and its index completion", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("n1"), Size: 2}, reqOnB(http.MethodPut, meta.ObjKey{Name: "k"}, alice, env))).To(Succeed())
		settle(s)
		body := bytes.Repeat([]byte("N"), 6<<20)
		deleted := false
		c.AfterWrite(testDataPool, "", olhOID(), func() {
			if o := c.Object(testDataPool, "", olhOID()); o == nil || !bytes.HasPrefix(o.Data, []byte("N")) {
				return
			}
			c.AfterWrite(testDataPool, "", olhOID(), nil)
			Expect(op.Run(ctx, &op.DeleteObject{}, reqOnB(http.MethodDelete, meta.ObjKey{Name: "k", Instance: meta.NullInstance}, alice, env))).To(Succeed())
			deleted = true
		})
		put := &op.PutObject{Body: bytes.NewReader(body), Size: int64(len(body))}
		Expect(op.Run(ctx, put, reqOnB(http.MethodPut, meta.ObjKey{Name: "k"}, alice, env))).To(Succeed())
		Expect(deleted).To(BeTrue(), "the delete ran after the PUT's head write")
		Expect(put.VersionID).To(Equal(meta.NullInstance))
		settle(s)
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{st.Key.Instance, st.Exists}).To(Equal([]any{v1, true}), "the OLH names v1")
		Expect(versions(ctx)).To(Equal([]string{v1}))
		Expect(chainOIDs(allGCEntries(c))).To(ConsistOf(tail()), "the PUT's tail, queued by the delete's removal")
		Expect(c.Object(testDataPool, "", olhOID()).Data).To(BeEmpty(), "the OLH head holds no null data")
	})

	// A conditional null write checks its condition before its head write,
	// whose op carries the write's init, so its link carries no condition
	// (linkCond) and the head write is its only write that leaves a pending
	// attr: the delete runs after its index completion instead.
	It("answers a conditional PUT with the null version when a delete takes it between its index completion and its link", func(ctx SpecContext) {
		body := bytes.Repeat([]byte("n"), 6<<20)
		shard := indexShardOID(rec, "k")
		deleted := false
		c.AfterWrite(rookIndexPool, "", shard, func() {
			en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k"})
			if !ok || !en.Exists || en.Meta.ETag != md5hex(body) || len(en.PendingMap) > 0 {
				return
			}
			c.AfterWrite(rookIndexPool, "", shard, nil)
			Expect(op.Run(ctx, &op.DeleteObject{}, reqOnB(http.MethodDelete, meta.ObjKey{Name: "k"}, alice, env))).To(Succeed())
			deleted = true
		})
		put := &op.PutObject{Body: bytes.NewReader(body), Size: int64(len(body)), IfMatch: new(md5hex("one"))}
		Expect(op.Run(ctx, put, reqOnB(http.MethodPut, meta.ObjKey{Name: "k"}, alice, env))).To(Succeed())
		Expect(deleted).To(BeTrue(), "the delete ran after the PUT's index completion")
		Expect(put.VersionID).To(Equal(meta.NullInstance))
		settle(s)
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{st.Key.Instance, st.DeleteMarker}).To(Equal([]any{"", true}), "the OLH names the null delete marker")
		Expect(versions(ctx)).To(Equal([]string{"null (delete marker)", v1}))
		Expect(chainOIDs(allGCEntries(c))).To(ConsistOf(tail()), "the PUT's tail, queued by the delete's removal")
		Expect(c.Object(testDataPool, "", olhOID()).Data).To(BeEmpty(), "the OLH head holds no null data")
	})
})

// reqOnB is a request by id for key of bucket b.
func reqOnB(method string, key meta.ObjKey, id op.Identity, env *op.Env) *op.Request {
	return &op.Request{Method: method, Bucket: "b", Object: key, Identity: id, Env: env, Header: http.Header{}, Query: url.Values{}}
}
