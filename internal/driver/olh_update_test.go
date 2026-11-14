package driver_test

import (
	"bytes"
	"context"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("UpdateOLH", func() {
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
	)
	put := func(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) {
		GinkgoHelper()
		body := []byte("data-" + key.Instance)
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
		Expect(err).NotTo(HaveOccurred(), "the put of %s", key)
	}
	// BeforeEach makes bucket b with versioning enabled, holding versions v1
	// and v2 of k, v2 current.
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_cache_enabled": "false"}, time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
		DeferCleanup(s.Close)
		var err error
		rec, err = s.CreateBucket(ctx, op.CreateBucketParams{
			Name: "b", Owner: initiator, Zonegroup: "zg-ceph-objectstore",
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     attrsWithACL(initiator), Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		rec.Info.Flags |= meta.BucketVersioned
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
		rec, err = s.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		put(ctx, rec, meta.ObjKey{Name: "k", Instance: "v1"})
		put(ctx, rec, meta.ObjKey{Name: "k", Instance: "v2"})
		settle(s)
	})
	AfterEach(func() { settle(s) })

	headOID := func(name string) string { return rec.Info.Bucket.Marker + "_" + name }
	head := func() *fakerados.Object { return c.Object(testDataPool, "", headOID("k")) }
	pending := func() []string {
		var out []string
		if o := head(); o != nil {
			for name := range o.Xattrs {
				if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
					out = append(out, name)
				}
			}
		}
		return out
	}
	// current is the instance k's OLH head names current, read off the head
	// without the follow that would replay its log.
	current := func() string {
		GinkgoHelper()
		o := head()
		Expect(o).NotTo(BeNil(), "k's OLH head")
		d := denc.NewDecoder(o.Xattrs[meta.AttrOLHInfo])
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info.Target.Key.Instance
	}
	stored := func(ctx context.Context, instance string) bool {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k", Instance: instance})
		Expect(err).NotTo(HaveOccurred())
		return st.Exists
	}
	olhEntry := func() bool {
		_, ok := c.OLHEntry(rookIndexPool, "", indexShardOID(rec, "k"), "k")
		return ok
	}
	skipDelete := func(ctx context.Context, instance string) {
		GinkgoHelper()
		_, err := s.DeleteObject(ctx, rec, meta.ObjKey{Name: "k", Instance: instance}, op.DeleteParams{SkipOLHUpdate: true})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
	}

	It("replays the log a skipped delete left, whatever instance the key names", func(ctx SpecContext) {
		skipDelete(ctx, "v2")
		Expect(pending()).To(HaveLen(1))
		Expect(stored(ctx, "v2")).To(BeTrue(), "the skipped delete leaves v2's head to the replay")
		Expect(current()).To(Equal("v2"))

		Expect(s.UpdateOLH(ctx, rec, meta.ObjKey{Name: "k", Instance: "v9"})).To(Succeed())
		settle(s)
		Expect(pending()).To(BeEmpty())
		Expect(stored(ctx, "v2")).To(BeFalse())
		Expect(current()).To(Equal("v1"))
		Expect(stored(ctx, "v1")).To(BeTrue())
	})
	// apply_olh_log ends without a trim when clear_olh is refused
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8550-L8556 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9296-L9302 at v20.2.4),
	// and a writer whose unlink then fails cancels its pending attr, so no read
	// replays that log; Ceph main's update_olh passes over a head with no
	// pending attr
	// (https://github.com/ceph/ceph/blob/v22.0.0/src/rgw/driver/rados/rgw_sal_rados.cc#L3211-L3215).
	It("clears an OLH whose clear another writer's pending attr refused, though no pending attr is left, applying its log again", func(ctx SpecContext) {
		h, err := s.ReadOLHHeadForTest(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		opTag, _, err := s.OLHInitModificationForTest(ctx, h, nil)
		Expect(err).NotTo(HaveOccurred())
		for _, v := range []string{"v2", "v1"} {
			_, err := s.DeleteObject(ctx, rec, meta.ObjKey{Name: "k", Instance: v}, op.DeleteParams{})
			Expect(err).NotTo(HaveOccurred(), "the delete of %s", v)
			settle(s)
		}
		Expect(head()).NotTo(BeNil(), "the other writer's pending attr refused the clear")
		s.OLHCancelModificationForTest(ctx, h, opTag)
		Expect(pending()).To(BeEmpty())
		e, ok := c.OLHEntry(rookIndexPool, "", indexShardOID(rec, "k"), "k")
		Expect(ok).To(BeTrue())
		Expect(e.PendingLog).NotTo(BeEmpty(), "the refused clear left the log untrimmed")

		Expect(s.UpdateOLH(ctx, rec, meta.ObjKey{Name: "k"})).To(Succeed())
		settle(s)
		Expect(head()).To(BeNil())
		Expect(olhEntry()).To(BeFalse())
	})
	// apply_olh_log takes a link whose instance sorts before the current
	// one's whatever its epoch
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8482-L8496 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9228-L9242 at v20.2.4),
	// so a log already applied, replayed again, can take an older version
	// back.
	It("leaves as it is a log its replays applied but could not trim, no pending attr left", func(ctx SpecContext) {
		shard := indexShardOID(rec, "k")
		for _, v := range []string{"v3", "v4"} {
			// Once the replay's head op has removed the pending attr, every
			// write to the shard fails, the trim and its retries among them.
			c.AfterWrite(testDataPool, "", headOID("k"), func() {
				if len(pending()) > 0 {
					return
				}
				c.AfterWrite(testDataPool, "", headOID("k"), nil)
				c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
					c.FailNextWrite(rookIndexPool, "", shard, syscall.EIO)
				})
			})
			body := []byte("data-" + v)
			_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k", Instance: v}, bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).To(HaveOccurred(), "the put of %s, whose trim failed", v)
			c.BeforeWrite(rookIndexPool, "", shard, nil)
			settle(s)
		}
		e, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue())
		Expect(e.PendingLog).To(HaveLen(2), "the links of v3 and v4, applied and untrimmed")
		Expect(pending()).To(BeEmpty())
		Expect(current()).To(Equal("v4"))

		headWrites := c.Writes(testDataPool, "", headOID("k"))
		Expect(s.UpdateOLH(ctx, rec, meta.ObjKey{Name: "k"})).To(Succeed())
		Expect(c.Writes(testDataPool, "", headOID("k"))).To(Equal(headWrites))
		Expect(current()).To(Equal("v4"), "v3's link, applied again, would make v3 current")
	})
	DescribeTable("leaves a name without an OLH head as it is",
		func(ctx SpecContext, name string, plain bool) {
			if plain {
				unversioned := *rec
				unversioned.Info.Flags &^= meta.BucketVersioned
				put(ctx, &unversioned, meta.ObjKey{Name: name})
				settle(s)
			}
			headWrites := c.Writes(testDataPool, "", headOID(name))
			shardReads := c.Reads(rookIndexPool, "", indexShardOID(rec, name))
			shardWrites := c.Writes(rookIndexPool, "", indexShardOID(rec, name))
			Expect(s.UpdateOLH(ctx, rec, meta.ObjKey{Name: name})).To(Succeed())
			Expect(c.Writes(testDataPool, "", headOID(name))).To(Equal(headWrites))
			Expect(c.Reads(rookIndexPool, "", indexShardOID(rec, name))).To(Equal(shardReads), "no index call")
			Expect(c.Writes(rookIndexPool, "", indexShardOID(rec, name))).To(Equal(shardWrites))
			Expect(c.Object(testDataPool, "", headOID(name)) != nil).To(Equal(plain), "the head as it was")
		},
		Entry("a name with no head", "missing", false),
		Entry("a plain object, which no versioned write converted", "plain", true),
	)
	// update_olh's ECANCELED is success on main
	// (https://github.com/ceph/ceph/blob/v22.0.0/src/rgw/driver/rados/rgw_sal_rados.cc#L3216-L3223).
	It("takes another replay's removal of the OLH before its own head op for success", func(ctx SpecContext) {
		skipDelete(ctx, "v2")
		skipDelete(ctx, "v1")
		var readErr error
		beforeReplayHeadOp(c, headOID("k"), 2, func() {
			_, readErr = s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
		})
		Expect(s.UpdateOLH(ctx, rec, meta.ObjKey{Name: "k"})).To(Succeed())
		Expect(readErr).NotTo(HaveOccurred())
		settle(s)
		Expect(head()).To(BeNil(), "the read's replay cleared the OLH")
		Expect(olhEntry()).To(BeFalse())
	})
	It("returns a failure to read the pending log, leaving the pending attr for the next update", func(ctx SpecContext) {
		skipDelete(ctx, "v2")
		c.FailNextRead(rookIndexPool, "", indexShardOID(rec, "k"), syscall.EPERM)
		Expect(s.UpdateOLH(ctx, rec, meta.ObjKey{Name: "k"})).To(MatchError(radosclient.ErrPermission))
		Expect(pending()).To(HaveLen(1))
		Expect(stored(ctx, "v2")).To(BeTrue())
	})
})
