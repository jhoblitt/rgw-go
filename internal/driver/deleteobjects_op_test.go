package driver_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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

// RGWDeleteMultiObj::handle_versioned_objects skips the OLH update of every
// delete of a name but its last, which replays the rest
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6924-L6979 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7857-L7892 at v20.2.4).
var _ = Describe("DeleteObjects over the RADOS store on a versioned bucket", func() {
	const zgID = "zg-ceph-objectstore"
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		env   *op.Env
		alice op.Identity
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_cache_enabled": "false", "rgw_multi_obj_del_max_aio": "1"},
			time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
		DeferCleanup(s.Close)
		env = s.Env()
		env.Authz = op.OwnerOnly{}
		u := meta.NewUserInfo()
		u.UserID, u.DisplayName = meta.UserID{ID: "alice"}, "Alice"
		alice = op.Identity{User: &u, Owner: initiator, OpMask: op.OpTypeAll}
	})
	AfterEach(func() { settle(s) })

	// bucket creates name with versioning enabled, holding versions v1 to v3
	// of k and v1 of j.
	bucket := func(ctx context.Context, name string) *op.BucketRecord {
		GinkgoHelper()
		rec, err := s.CreateBucket(ctx, op.CreateBucketParams{
			Name: name, Owner: initiator, Zonegroup: zgID,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     attrsWithACL(initiator), Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		rec.Info.Flags |= meta.BucketVersioned
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
		rec, err = s.GetBucket(ctx, "", name)
		Expect(err).NotTo(HaveOccurred())
		for _, v := range []meta.ObjKey{{Name: "k", Instance: "v1"}, {Name: "k", Instance: "v2"}, {Name: "k", Instance: "v3"}, {Name: "j", Instance: "v1"}} {
			body := []byte("data-" + v.Instance)
			_, err := s.PutObject(ctx, rec, v, bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred(), "the put of %s", v)
		}
		settle(s)
		return rec
	}
	// listed is name's list entries in rec's index, a delete marker's random
	// instance left out.
	type listedVersion struct {
		Instance string
		Flags    uint16
		Epoch    uint64
	}
	listed := func(rec *op.BucketRecord, name string) []listedVersion {
		var out []listedVersion
		for _, en := range c.ListEntries(rookIndexPool, "", indexShardOID(rec, name), name) {
			v := listedVersion{Instance: en.Key.Instance, Flags: en.Flags, Epoch: en.VersionedEpoch}
			if en.Flags&rgwcls.FlagDeleteMarker != 0 {
				v.Instance = "a delete marker"
			}
			out = append(out, v)
		}
		return out
	}
	// olhState is what name's OLH head holds once its modifications are
	// applied: its olh.ver and the pending attrs left.
	olhState := func(rec *op.BucketRecord, name string) (string, []string) {
		GinkgoHelper()
		head := c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_"+name)
		Expect(head).NotTo(BeNil(), "%s's OLH head", name)
		var pending []string
		for attr := range head.Xattrs {
			if strings.HasPrefix(attr, meta.AttrOLHPendingPrefix) {
				pending = append(pending, attr)
			}
		}
		return string(head.Xattrs[meta.AttrOLHVer]), pending
	}

	// multiDelete deletes es in bucket through DeleteObjects, at most maxAIO
	// at once, and returns each key's outcome.
	multiDelete := func(ctx context.Context, bucket, maxAIO string, es ...op.DeleteObjectsEntry) []op.DeleteResult {
		GinkgoHelper()
		var (
			mu      sync.Mutex
			results []op.DeleteResult
		)
		o := &op.DeleteObjects{
			Parse:  func([]byte) ([]op.DeleteObjectsEntry, error) { return es, nil },
			Status: func(err error) { Fail("the request was refused: " + err.Error()) },
			Begin:  func() error { return nil },
			Result: func(res op.DeleteResult) {
				mu.Lock()
				defer mu.Unlock()
				results = append(results, res)
			},
		}
		e := *env
		e.Conf = conf(map[string]string{"rgw_multi_obj_del_max_aio": maxAIO})
		r := &op.Request{
			Method: http.MethodPost, Bucket: bucket, Identity: alice, Env: &e,
			Header: http.Header{"Content-Length": {"1"}}, Query: url.Values{}, Body: strings.NewReader("x"), ContentLength: 1,
		}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		settle(s)
		Expect(results).To(HaveLen(len(es)))
		return results
	}
	// oneByOne deletes keys in bucket one DeleteObject at a time, each with
	// its own update, in the order handle_versioned_objects deletes them,
	// each name's skipped deletes first.
	oneByOne := func(ctx context.Context, bucket string, keys ...meta.ObjKey) {
		GinkgoHelper()
		for _, k := range keys {
			r := &op.Request{Method: http.MethodDelete, Bucket: bucket, Object: k, Identity: alice, Env: env, Header: http.Header{}, Query: url.Values{}}
			Expect(op.Run(ctx, &op.DeleteObject{}, r)).To(Succeed(), "%s", k)
			settle(s)
		}
	}
	// grouped is the request the final-state specs send, and inOrder its keys
	// in the order handle_versioned_objects deletes them.
	grouped := func() []op.DeleteObjectsEntry {
		return []op.DeleteObjectsEntry{
			{Key: meta.ObjKey{Name: "k", Instance: "v3"}},
			{Key: meta.ObjKey{Name: "j"}},
			{Key: meta.ObjKey{Name: "k"}},
			{Key: meta.ObjKey{Name: "j"}},
			{Key: meta.ObjKey{Name: "k", Instance: "v1"}},
		}
	}
	inOrder := func() []meta.ObjKey {
		return []meta.ObjKey{{Name: "j"}, {Name: "k", Instance: "v3"}, {Name: "k"}, {Name: "j"}, {Name: "k", Instance: "v1"}}
	}
	// expectSameState checks that grouped and single hold the same state for
	// k and j: their list entries, by epochs too when epochs is set, their
	// olh.ver, no pending attr, and a delete marker current.
	expectSameState := func(ctx context.Context, grouped, single *op.BucketRecord, epochs bool) {
		GinkgoHelper()
		for _, name := range []string{"k", "j"} {
			want, got := listed(single, name), listed(grouped, name)
			Expect(want).NotTo(BeEmpty())
			if !epochs {
				for i := range want {
					want[i].Epoch = 0
				}
				for i := range got {
					got[i].Epoch = 0
				}
			}
			Expect(got).To(ConsistOf(want), "%s's versions", name)
			wantVer, wantPending := olhState(single, name)
			gotVer, gotPending := olhState(grouped, name)
			Expect(gotPending).To(BeEmpty(), "%s's modifications all applied", name)
			Expect(wantPending).To(BeEmpty())
			Expect(gotVer).To(Equal(wantVer), "%s's olh.ver", name)
			_, err := strconv.ParseUint(gotVer, 10, 64)
			Expect(err).NotTo(HaveOccurred(), "olh.ver %q", gotVer)
			st, err := s.StatObject(ctx, grouped, meta.ObjKey{Name: name})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.DeleteMarker).To(BeTrue(), "%s's current version is the delete marker its last delete wrote", name)
		}
		Expect(listed(grouped, "k")).To(ConsistOf(
			HaveField("Flags", rgwcls.FlagVer|rgwcls.FlagCurrent|rgwcls.FlagDeleteMarker),
			HaveField("Instance", "v2"),
		), "v1 and v3 removed, v2 behind the marker")
		Expect(listed(grouped, "j")).To(ConsistOf(
			HaveField("Flags", rgwcls.FlagVer|rgwcls.FlagCurrent|rgwcls.FlagDeleteMarker),
			HaveField("Flags", rgwcls.FlagVer|rgwcls.FlagDeleteMarker),
			HaveField("Instance", "v1"),
		), "two delete markers over v1, the last current")
	}

	It("ends in the state the same deletes reach one by one, each with its own update, deleting one at a time", func(ctx SpecContext) {
		groupedRec, single := bucket(ctx, "grouped"), bucket(ctx, "single")
		for _, res := range multiDelete(ctx, "grouped", "1", grouped()...) {
			Expect(res.Err).NotTo(HaveOccurred(), "%s", res.Key)
		}
		oneByOne(ctx, "single", inOrder()...)
		expectSameState(ctx, groupedRec, single, true)
	})
	// With a name's skipped deletes run together, the order the class gives
	// them their epochs in is theirs, so a delete marker's epoch may differ.
	It("ends in the same versions, olh.ver and current version at the default rgw_multi_obj_del_max_aio", func(ctx SpecContext) {
		groupedRec, single := bucket(ctx, "grouped"), bucket(ctx, "single")
		for _, res := range multiDelete(ctx, "grouped", "16", grouped()...) {
			Expect(res.Err).NotTo(HaveOccurred(), "%s", res.Key)
		}
		oneByOne(ctx, "single", inOrder()...)
		expectSameState(ctx, groupedRec, single, false)
	})
	// handle_individual_object answers a key whose delete fails before
	// delete_obj runs, so the skipped deletes of its name stay pending
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6837-L6847 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7765-L7775 at v20.2.4).
	It("leaves the skipped deletes pending when a name's last delete is refused, for the key's next read to replay", func(ctx SpecContext) {
		rec := bucket(ctx, "grouped")
		results := multiDelete(ctx, "grouped", "1",
			op.DeleteObjectsEntry{Key: meta.ObjKey{Name: "k", Instance: "v3"}},
			op.DeleteObjectsEntry{Key: meta.ObjKey{Name: "k", Instance: "v1"}},
			op.DeleteObjectsEntry{Key: meta.ObjKey{Name: "k", Instance: "v2"}, IfMatch: new("nope")},
		)
		errs := map[string]error{}
		for _, res := range results {
			errs[res.Key.Instance] = res.Err
		}
		Expect(errs).To(HaveKeyWithValue("v3", BeNil()))
		Expect(errs).To(HaveKeyWithValue("v1", BeNil()))
		Expect(errs).To(HaveKeyWithValue("v2", MatchError(op.ErrPreconditionFailed)), "the last delete of k, refused before its unlink")
		_, pending := olhState(rec, "k")
		Expect(pending).NotTo(BeEmpty(), "the skipped deletes wait for the key's next update")

		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue())
		Expect(st.Key.Instance).To(Equal("v2"), "the read replays the removals and resolves the version left")
		settle(s)
		_, pending = olhState(rec, "k")
		Expect(pending).To(BeEmpty())
		Expect(listed(rec, "k")).To(ConsistOf(HaveField("Instance", "v2")))
		Expect(listed(rec, "k")[0].Flags & rgwcls.FlagCurrent).NotTo(BeZero())
	})
})
