package driver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"

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
		// onResult, when set, runs as each key's outcome is reported,
		// holding the op's lock; deleteObjects' requests share it.
		onResult func(op.DeleteResult)
	)
	BeforeEach(func(ctx SpecContext) {
		onResult = nil
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

	// deleteObjects deletes es in bucket through DeleteObjects, at most maxAIO
	// at once, and returns each key's outcome and the request's. It asserts
	// nothing, so a hook running in another request's goroutine may call it.
	deleteObjects := func(ctx context.Context, bucket, maxAIO string, es ...op.DeleteObjectsEntry) ([]op.DeleteResult, error) {
		var (
			mu      sync.Mutex
			results []op.DeleteResult
			refused error
		)
		o := &op.DeleteObjects{
			Parse:  func([]byte) ([]op.DeleteObjectsEntry, error) { return es, nil },
			Status: func(err error) { refused = err },
			Begin:  func() error { return nil },
			Result: func(res op.DeleteResult) {
				mu.Lock()
				defer mu.Unlock()
				results = append(results, res)
				if onResult != nil {
					onResult(res)
				}
			},
		}
		e := *env
		e.Conf = conf(map[string]string{"rgw_multi_obj_del_max_aio": maxAIO})
		r := &op.Request{
			Method: http.MethodPost, Bucket: bucket, Identity: alice, Env: &e,
			Header: http.Header{"Content-Length": {"1"}}, Query: url.Values{}, Body: strings.NewReader("x"), ContentLength: 1,
		}
		if err := op.Run(ctx, o, r); err != nil {
			return results, err
		}
		return results, refused
	}
	// multiDelete is deleteObjects for the spec's own goroutine, which checks
	// that the request succeeded with an outcome for every key.
	multiDelete := func(ctx context.Context, bucket, maxAIO string, es ...op.DeleteObjectsEntry) []op.DeleteResult {
		GinkgoHelper()
		results, err := deleteObjects(ctx, bucket, maxAIO, es...)
		Expect(err).NotTo(HaveOccurred())
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
	// Another request's replay can apply every delete of a name and clear its
	// OLH between the last delete's log read and its head op, as
	// test_versioning_concurrent_multi_object_delete's five requests do;
	// radosgw's handle_individual_object answers the head op's ENOENT as
	// success
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6908-L6910 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7836-L7838 at v20.2.4).
	It("answers every key Deleted when another replay removes the OLH before the last delete's replay writes the head", func(ctx SpecContext) {
		rec := bucket(ctx, "grouped")
		beforeReplayHeadOp(c, rec.Info.Bucket.Marker+"_k", 3, func() {
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeFalse(), "the read's replay removed every version and cleared the OLH")
			Expect(c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k")).To(BeNil())
		})
		results := multiDelete(ctx, "grouped", "1",
			op.DeleteObjectsEntry{Key: meta.ObjKey{Name: "k", Instance: "v1"}},
			op.DeleteObjectsEntry{Key: meta.ObjKey{Name: "k", Instance: "v2"}},
			op.DeleteObjectsEntry{Key: meta.ObjKey{Name: "k", Instance: "v3"}},
		)
		for _, res := range results {
			Expect(res.Err).NotTo(HaveOccurred(), "%s", res.Key)
		}
		Expect(listed(rec, "k")).To(BeEmpty())
		Expect(c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k")).To(BeNil())
	})
	It("answers a DeleteObject of the key's last version 204 when another replay removes the OLH before its own replay writes the head", func(ctx SpecContext) {
		rec := bucket(ctx, "single")
		oneByOne(ctx, "single", meta.ObjKey{Name: "k", Instance: "v1"}, meta.ObjKey{Name: "k", Instance: "v2"})
		beforeReplayHeadOp(c, rec.Info.Bucket.Marker+"_k", 1, func() {
			Expect(s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})).To(HaveField("Exists", BeFalse()), "the read's replay cleared the OLH")
		})
		o := &op.DeleteObject{}
		r := &op.Request{Method: http.MethodDelete, Bucket: "single", Object: meta.ObjKey{Name: "k", Instance: "v3"}, Identity: alice, Env: env, Header: http.Header{}, Query: url.Values{}}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.VersionID).To(Equal("v3"), "x-amz-version-id names the version removed, where radosgw sends none")
		Expect(o.DeleteMarker).To(BeFalse())
		settle(s)
		Expect(listed(rec, "k")).To(BeEmpty())
		Expect(c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k")).To(BeNil())
	})
	// byID reports whether version instance of k is readable by its id, a
	// read that follows no OLH and so replays nothing.
	byID := func(ctx context.Context, rec *op.BucketRecord, instance string) bool {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k", Instance: instance})
		Expect(err).NotTo(HaveOccurred())
		return st.Exists
	}
	// current is the instance k's OLH head names current, read off the head
	// without the follow that would replay its log.
	current := func(rec *op.BucketRecord) string {
		GinkgoHelper()
		head := c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k")
		Expect(head).NotTo(BeNil(), "k's OLH head")
		d := denc.NewDecoder(head.Xattrs[meta.AttrOLHInfo])
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info.Target.Key.Instance
	}
	// noOLHLeft checks that k has nothing left: no version readable by its
	// id, no list entry, no OLH head and no OLH entry.
	noOLHLeft := func(ctx context.Context, rec *op.BucketRecord) {
		GinkgoHelper()
		for _, v := range []string{"v1", "v2", "v3"} {
			Expect(byID(ctx, rec, v)).To(BeFalse(), "%s, which both requests reported deleted, read by its id", v)
		}
		Expect(listed(rec, "k")).To(BeEmpty())
		Expect(c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k")).To(BeNil(), "k's OLH head")
		_, ok := c.OLHEntry(rookIndexPool, "", indexShardOID(rec, "k"), "k")
		Expect(ok).To(BeFalse(), "k's OLH entry")
	}
	version := func(instance string) op.DeleteObjectsEntry {
		return op.DeleteObjectsEntry{Key: meta.ObjKey{Name: "k", Instance: instance}}
	}
	// Ceph main applies a name's skipped updates after its last delete,
	// whatever that delete answered
	// (https://github.com/ceph/ceph/blob/v22.0.0/src/rgw/rgw_multi_del.cc#L153-L167);
	// both floors leave them pending
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6924-L6979 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7857-L7891 at v20.2.4).
	DescribeTable("applies the skipped deletes of a name whose last delete makes no update",
		func(ctx SpecContext, last op.DeleteObjectsEntry, lastErr error) {
			rec := bucket(ctx, "grouped")
			results := multiDelete(ctx, "grouped", "1", version("v3"), version("v1"), last)
			Expect(results).To(HaveLen(3))
			for _, res := range results {
				if res.Key == last.Key && lastErr != nil {
					Expect(res.Err).To(MatchError(lastErr), "the last delete of k, refused before its unlink")
					continue
				}
				Expect(res.Err).NotTo(HaveOccurred(), "%s", res.Key)
			}
			for _, v := range []string{"v3", "v1"} {
				Expect(byID(ctx, rec, v)).To(BeFalse(), "%s, which the request reported deleted, read by its id before any read of k", v)
			}
			_, pending := olhState(rec, "k")
			Expect(pending).To(BeEmpty())
			Expect(current(rec)).To(Equal("v2"), "the OLH names the version left")
			Expect(listed(rec, "k")).To(ConsistOf(HaveField("Instance", "v2")))
			Expect(listed(rec, "k")[0].Flags & rgwcls.FlagCurrent).NotTo(BeZero())
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Key.Instance).To(Equal("v2"))
		},
		Entry("a version that does not exist", version("missing"), nil),
		Entry("a failed condition", op.DeleteObjectsEntry{Key: meta.ObjKey{Name: "k", Instance: "v2"}, IfMatch: new("nope")}, op.ErrPreconditionFailed),
	)
	// A name's update that fails changes no key's answer, as main's dispatch
	// ignores it, and main logs the failure at level 0
	// (https://github.com/ceph/ceph/blob/v22.0.0/src/rgw/rgw_multi_del.cc#L153-L167,
	// https://github.com/ceph/ceph/blob/v22.0.0/src/rgw/driver/rados/rgw_sal_rados.cc#L3201-L3207 for the head's read,
	// https://github.com/ceph/ceph/blob/v22.0.0/src/rgw/driver/rados/rgw_sal_rados.cc#L3218-L3221 for the replay).
	Describe("a name's update that fails to read what it replays", func() {
		const updateFailed = "multi-object delete failed to update a key's olh after its last delete"
		// failedUpdate deletes k@v3, k@v1 and k@missing in bucket grouped,
		// whose last delete finds its version gone, with stage run once that
		// delete has been reported, before the name's update starts. It
		// returns the request's records of the update's failure and of its
		// summary, and checks that every key was answered Deleted.
		failedUpdate := func(ctx context.Context, stage func()) (failed []map[string]any, summary map[string]any) {
			GinkgoHelper()
			logs := gbytes.NewBuffer()
			DeferCleanup(driver.CaptureLog(logs))
			onResult = func(res op.DeleteResult) {
				if res.Key.Instance == "missing" {
					stage()
				}
			}
			for _, res := range multiDelete(ctx, "grouped", "1", version("v3"), version("v1"), version("missing")) {
				Expect(res.Err).NotTo(HaveOccurred(), "%s", res.Key)
			}
			for line := range strings.Lines(string(logs.Contents())) {
				var rec map[string]any
				Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed(), "%q", line)
				switch rec["msg"] {
				case updateFailed:
					failed = append(failed, rec)
				case "multi-object delete failed for some keys":
					summary = rec
				}
			}
			return failed, summary
		}
		It("logs a failure to read the OLH head at error level, and leaves the name's modifications pending for its next update", func(ctx SpecContext) {
			rec := bucket(ctx, "grouped")
			failed, summary := failedUpdate(ctx, func() {
				c.FailNextRead(testDataPool, "", rec.Info.Bucket.Marker+"_k", syscall.EPERM)
			})
			Expect(failed).To(ConsistOf(And(
				HaveKeyWithValue("level", "ERROR"),
				HaveKeyWithValue("bucket", "grouped"),
				HaveKeyWithValue("key", "k"),
				HaveKeyWithValue("error", ContainSubstring("reading the olh head of grouped/k")),
			)))
			Expect(summary).To(And(HaveKeyWithValue("level", "ERROR"), HaveKeyWithValue("failed_olh_updates", BeEquivalentTo(1))))
			_, pending := olhState(rec, "k")
			Expect(pending).To(HaveLen(2), "the skipped deletes' modifications")
			for _, v := range []string{"v3", "v1"} {
				Expect(byID(ctx, rec, v)).To(BeTrue(), "%s, which the request reported deleted, until the name's next update", v)
			}

			Expect(s.UpdateOLH(ctx, rec, meta.ObjKey{Name: "k"})).To(Succeed())
			settle(s)
			for _, v := range []string{"v3", "v1"} {
				Expect(byID(ctx, rec, v)).To(BeFalse(), "%s, once the next update has replayed the modifications", v)
			}
			Expect(current(rec)).To(Equal("v2"))
		})
		It("logs a failure to read the OLH entry at error level", func(ctx SpecContext) {
			rec := bucket(ctx, "grouped")
			failed, summary := failedUpdate(ctx, func() {
				head := c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k")
				for attr := range head.Xattrs {
					if strings.HasPrefix(attr, meta.AttrOLHPendingPrefix) {
						delete(head.Xattrs, attr)
					}
				}
				c.FailNextRead(rookIndexPool, "", indexShardOID(rec, "k"), syscall.EPERM)
			})
			Expect(failed).To(ConsistOf(And(
				HaveKeyWithValue("level", "ERROR"),
				HaveKeyWithValue("bucket", "grouped"),
				HaveKeyWithValue("key", "k"),
				HaveKeyWithValue("error", ContainSubstring("reading the olh entry of grouped/k")),
			)))
			Expect(summary).To(And(HaveKeyWithValue("level", "ERROR"), HaveKeyWithValue("failed_olh_updates", BeEquivalentTo(1))))
		})
	})
	// A skipped unlink takes a version's list entry and leaves its instance
	// entry for the replay to remove
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1984 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2131 at v20.2.4),
	// so a request whose skipped deletes land while another request's replay
	// is removing the versions finds its last version gone, its own entries
	// unread by that replay and their pending attrs refusing its clear.
	It("leaves no OLH head when a concurrent DeleteObjects' skipped deletes land in another's replay and its last version is gone", func(ctx SpecContext) {
		rec := bucket(ctx, "grouped")
		v2Head := rec.Info.Bucket.Marker + "_" + meta.ObjKey{Name: "k", Instance: "v2"}.OID()
		var (
			inner    []op.DeleteResult
			innerErr error
			ran      bool
		)
		// The outer request's replay removes v1, then v2: its first write to
		// v2's head is that removal, v1 gone and v2 and v3 still indexed.
		c.BeforeWrite(testDataPool, "", v2Head, func(*fakerados.Object) {
			c.BeforeWrite(testDataPool, "", v2Head, nil)
			ran = true
			inner, innerErr = deleteObjects(ctx, "grouped", "1", version("v2"), version("v3"), version("v1"))
		})
		outer := multiDelete(ctx, "grouped", "1", version("v1"), version("v2"), version("v3"))
		Expect(ran).To(BeTrue())
		Expect(innerErr).NotTo(HaveOccurred())
		Expect(inner).To(HaveLen(3))
		for _, res := range append(outer, inner...) {
			Expect(res.Err).NotTo(HaveOccurred(), "%s", res.Key)
		}
		noOLHLeft(ctx, rec)
	})
	// apply_olh_log ends without a trim when clear_olh is refused
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8550-L8556 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9296-L9302 at v20.2.4),
	// and a skipped unlink that fails runs no update
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8820-L8840 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9566-L9586 at v20.2.4).
	It("leaves no OLH head when a skipped delete's pending attr refuses a concurrent DeleteObjects' clear and its unlink then finds its version gone", func(ctx SpecContext) {
		rec := bucket(ctx, "grouped")
		shard := indexShardOID(rec, "k")
		var (
			inner      []op.DeleteResult
			innerErr   error
			ran        bool
			pendingLog bool
			left       []string
		)
		// The outer request's first write to the shard is its unlink of v1,
		// once its init's pending attr is on the head.
		c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
			c.BeforeWrite(rookIndexPool, "", shard, nil)
			ran = true
			inner, innerErr = deleteObjects(ctx, "grouped", "1", version("v1"), version("v2"), version("v3"))
			if head := c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k"); head != nil {
				for attr := range head.Xattrs {
					if strings.HasPrefix(attr, meta.AttrOLHPendingPrefix) {
						left = append(left, attr)
					}
				}
			}
			e, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
			pendingLog = ok && len(e.PendingLog) > 0
		})
		outer := multiDelete(ctx, "grouped", "1", version("v1"), version("v2"), version("v3"))
		Expect(ran).To(BeTrue())
		Expect(innerErr).NotTo(HaveOccurred())
		Expect(left).To(HaveLen(1), "the outer request's pending attr refused the inner request's clear")
		Expect(pendingLog).To(BeTrue(), "the refused clear left the log untrimmed")
		Expect(inner).To(HaveLen(3))
		for _, res := range append(outer, inner...) {
			Expect(res.Err).NotTo(HaveOccurred(), "%s", res.Key)
		}
		noOLHLeft(ctx, rec)
	})

	// A replay lands its head op, olh.ver its log's last epoch, before it
	// trims that log
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8543-L8563 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9289-L9309 at v20.2.4),
	// so a replay that reads the head between the two applies the same links
	// again over a head that already names the newest.
	Describe("a replay that reads the head between another replay's head op and its trim", func() {
		putVersion := func(ctx context.Context, rec *op.BucketRecord, instance string) error {
			body := []byte("data-" + instance)
			_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k", Instance: instance}, bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			return err
		}
		// inWindow reports whether k's head names v5 while the log of the
		// replay that named it is still untrimmed.
		inWindow := func(rec *op.BucketRecord) bool {
			o := c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k")
			if o == nil {
				return false
			}
			d := denc.NewDecoder(o.Xattrs[meta.AttrOLHInfo])
			info := meta.DecodeOLHInfo(d)
			if d.Err() != nil || info.Target.Key.Instance != "v5" {
				return false
			}
			e, ok := c.OLHEntry(rookIndexPool, "", indexShardOID(rec, "k"), "k")
			return ok && len(e.PendingLog) > 0
		}
		// expectV5 checks that k's OLH names v5, the newest PUT, and that a
		// GET of k serves it.
		expectV5 := func(ctx context.Context, rec *op.BucketRecord) {
			GinkgoHelper()
			Expect(current(rec)).To(Equal("v5"), "the OLH names the newest PUT")
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Key.Instance).To(Equal("v5"), "a GET of k serves the newest PUT")
		}
		// The PUT of v4's replay head op is held while a PUT of v5 runs; the
		// victim runs once v5's replay has landed its head op, before its trim.
		DescribeTable("leaves the newest link current when it applies the links again",
			func(ctx SpecContext, victim func(ctx context.Context) error) {
				rec := bucket(ctx, "grouped")
				headOID := rec.Info.Bucket.Marker + "_k"
				var fired bool
				var victimErr, put5Err error
				beforeReplayHeadOp(c, headOID, 1, func() {
					c.AfterWrite(testDataPool, "", headOID, func() {
						if !inWindow(rec) {
							return
						}
						c.AfterWrite(testDataPool, "", headOID, nil)
						fired = true
						victimErr = victim(ctx)
					})
					put5Err = putVersion(ctx, rec, "v5")
				})
				Expect(putVersion(ctx, rec, "v4")).To(Succeed())
				settle(s)
				Expect(put5Err).NotTo(HaveOccurred())
				Expect(fired).To(BeTrue(), "the victim ran in the window")
				Expect(victimErr).NotTo(HaveOccurred())
				expectV5(ctx, rec)
			},
			Entry("DeleteObjects' update after a last delete that finds its version gone",
				func(ctx context.Context) error {
					_, err := deleteObjects(ctx, "grouped", "1", version("v1"), version("missing"))
					return err
				}),
			Entry("a DeleteObject of a noncurrent version",
				func(ctx context.Context) error {
					r := &op.Request{Method: http.MethodDelete, Bucket: "grouped", Object: meta.ObjKey{Name: "k", Instance: "v1"}, Identity: alice, Env: env, Header: http.Header{}, Query: url.Values{}}
					return op.Run(ctx, &op.DeleteObject{}, r)
				}),
		)
		// The PUT of v4 loses its head op to v5's replay, and replayOLH reads
		// the head again for its retry while v5's replay is held after its
		// head op.
		It("leaves the newest link current when a PUT's replay retries from a head read in the window", func(ctx SpecContext) {
			rec := bucket(ctx, "grouped")
			headOID := rec.Info.Bucket.Marker + "_k"
			atWindow, release, done5 := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var put5Err error
			beforeReplayHeadOp(c, headOID, 1, func() {
				c.AfterWrite(testDataPool, "", headOID, func() {
					if !inWindow(rec) {
						return
					}
					c.AfterWrite(testDataPool, "", headOID, nil)
					close(atWindow)
					select {
					case <-release:
					case <-time.After(10 * time.Second):
					}
				})
				go func() {
					defer GinkgoRecover()
					defer close(done5)
					put5Err = putVersion(ctx, rec, "v5")
				}()
				select {
				case <-atWindow:
				case <-time.After(10 * time.Second):
				}
			})
			Expect(putVersion(ctx, rec, "v4")).To(Succeed())
			close(release)
			Eventually(done5).WithTimeout(10 * time.Second).WithPolling(time.Millisecond).Should(BeClosed())
			settle(s)
			Expect(put5Err).NotTo(HaveOccurred())
			expectV5(ctx, rec)
		})
	})
})
