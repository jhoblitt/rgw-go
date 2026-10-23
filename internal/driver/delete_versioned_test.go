package driver_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
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

var _ = Describe("DeleteObject on a versioned bucket", func() {
	var (
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		clock  atomic.Int64
		shard  string
		olhOID string
		bob    meta.Owner
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	open := func(ctx context.Context, r denc.Release) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, c, conf(nil), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
	}
	BeforeEach(func(ctx SpecContext) {
		clock.Store(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).UnixNano())
		c = newPutCluster()
		c.SetClock(now)
		open(ctx, denc.Squid)
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
		olhOID = olhHeadOID(version(""))
		bob = meta.UserOwner(meta.UserID{Tenant: "acme", ID: "bob"})
	})
	AfterEach(func() { settle(s) })

	suspend := func() { rec.Info.Flags |= meta.BucketVersionsSuspended }
	// write is a versioned write of version instance up to its link: the
	// prepare, the instance head and the awaited ADD completion. For the
	// empty instance, unlinked, it is the object a PutObject wrote before
	// the bucket's versioning was enabled.
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
	// converted is a key whose pre-versioning object, the null version, a
	// versioned write of v1 converted.
	converted := func(ctx context.Context) {
		GinkgoHelper()
		write(ctx, "")
		put(ctx, "v1")
	}
	del := func(ctx context.Context, instance string, p op.DeleteParams) (*op.DeleteObjectResult, error) {
		return s.DeleteObject(ctx, rec, version(instance), p)
	}
	mustDel := func(ctx context.Context, instance string, p op.DeleteParams) *op.DeleteObjectResult {
		GinkgoHelper()
		res, err := del(ctx, instance, p)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		return res
	}
	// after arms fn to run once, after the n-th later write op on oid lands.
	after := func(pool, oid string, n int, fn func()) {
		seen := 0
		c.AfterWrite(pool, "", oid, func() {
			if seen++; seen == n {
				c.AfterWrite(pool, "", oid, nil)
				fn()
			}
		})
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
		slices.Sort(out)
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
	// markers lists the instances of k's listed delete markers.
	markers := func() []string {
		var out []string
		for _, en := range list() {
			if en.Flags&rgwcls.FlagDeleteMarker != 0 {
				out = append(out, en.Key.Instance)
			}
		}
		return out
	}
	instanceEntry := func(instance string, nullDeleteMarker bool) (rgwcls.DirEntry, bool) {
		return c.Entry(rookIndexPool, "", shard, rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k", Instance: instance}, nullDeleteMarker))
	}
	// instanceMarkers lists the omap keys of k's instance entries flagged as
	// delete markers.
	instanceMarkers := func() []string {
		prefix := rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k"}, false)
		var out []string
		for _, key := range slices.Sorted(maps.Keys(c.Object(rookIndexPool, "", shard).Omap)) {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			if en, ok := c.Entry(rookIndexPool, "", shard, key); ok && en.Flags&rgwcls.FlagDeleteMarker != 0 {
				out = append(out, key)
			}
		}
		return out
	}
	olhLog := func() map[uint64][]rgwcls.OLHLogEntry {
		GinkgoHelper()
		e, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue(), "the OLH entry of k")
		return e.PendingLog
	}
	// gone checks that version instance has no head, instance entry or list
	// entry.
	gone := func(instance string) {
		GinkgoHelper()
		settle(s)
		Expect(versionHead(instance)).To(BeNil(), "the head of version %q", instance)
		_, ok := instanceEntry(instance, false)
		Expect(ok).To(BeFalse(), "the instance entry of version %q", instance)
		Expect(listed()).NotTo(ContainElement(instance), "the list entry of version %q", instance)
	}
	// current checks that instance is k's current version on the head and
	// in the listing, with no pending attr and the log trimmed.
	current := func(instance string) {
		GinkgoHelper()
		settle(s)
		Expect(olhInfo().Target.Key.Instance).To(Equal(instance), "olh.info")
		Expect(list()).NotTo(BeEmpty())
		Expect(list()[0].Key.Instance).To(Equal(instance), "the current version lists first")
		Expect(list()[0].Flags & rgwcls.FlagCurrent).To(Equal(rgwcls.FlagCurrent))
		Expect(pending()).To(BeEmpty(), "no pending attr")
		Expect(olhLog()).To(BeEmpty(), "the log is trimmed")
	}
	conditionFailed := func(err error) {
		GinkgoHelper()
		Expect(err).To(MatchError(op.ErrPreconditionFailed))
		Expect(err).NotTo(MatchError(op.ErrConcurrentModification))
	}
	// linkOps decodes the bucket_link_olh requests sent to k's shard.
	linkOps := func() []rgwcls.LinkOLHOp {
		var out []rgwcls.LinkOLHOp
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			for _, st := range w.Steps() {
				if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_link_olh" {
					out = append(out, rgwcls.DecodeLinkOLHOp(denc.NewDecoder(x.In)))
				}
			}
		}
		return out
	}
	// unlinkOps decodes the bucket_unlink_instance requests sent to k's shard.
	unlinkOps := func() []rgwcls.UnlinkInstanceOp {
		var out []rgwcls.UnlinkInstanceOp
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			for _, st := range w.Steps() {
				if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_unlink_instance" {
					out = append(out, rgwcls.DecodeUnlinkInstanceOp(denc.NewDecoder(x.In)))
				}
			}
		}
		return out
	}

	Describe("without a versionId", func() {
		DescribeTable("writes a delete marker under a fresh instance on an enabled bucket, current, its entry naming the owner and the delete time",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				put(ctx, "v1")
				res := mustDel(ctx, "", op.DeleteParams{MarkerOwner: bob, MarkerOwnerDisplayName: "Bob"})
				Expect(res.DeleteMarker).To(BeTrue())
				Expect(res.VersionID).To(HaveLen(31), "gen_rand_obj_instance_name")
				Expect(listed()).To(Equal([]string{res.VersionID, "v1"}))
				en := list()[0]
				Expect(en.Flags).To(Equal(rgwcls.FlagVer|rgwcls.FlagCurrent|rgwcls.FlagDeleteMarker), "the marker's list entry")
				Expect(en.Meta).To(Equal(rgwcls.DirEntryMeta{Owner: "acme$bob", OwnerDisplayName: "Bob", Mtime: now()}),
					"meta.owner, meta.owner_display_name and the delete time, rgw_rados.cc:5871-5880")
				ie, ok := instanceEntry(res.VersionID, false)
				Expect(ok).To(BeTrue(), "the marker's instance entry")
				Expect(ie.Flags & rgwcls.FlagDeleteMarker).To(Equal(rgwcls.FlagDeleteMarker))
				Expect(olhInfo()).To(Equal(meta.OLHInfo{Target: meta.Obj{Bucket: rec.Info.Bucket, Key: version(res.VersionID)}, Removed: true}))
				current(res.VersionID)
				Expect(versionHead("v1")).NotTo(BeNil(), "v1 stays, noncurrent")
				st, err := s.StatObject(ctx, rec, version(""))
				Expect(err).NotTo(HaveOccurred())
				Expect([]any{st.Exists, st.DeleteMarker}).To(Equal([]any{false, true}))
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)

		It("writes a delete marker for a key with no version, as radosgw does", func(ctx SpecContext) {
			res := mustDel(ctx, "", op.DeleteParams{})
			Expect(markers()).To(Equal([]string{res.VersionID}))
			current(res.VersionID)
		})

		DescribeTable("writes the null delete marker on a suspended bucket, replacing the null version",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				converted(ctx)
				suspend()
				deleted := now().Add(-time.Hour)
				res := mustDel(ctx, "", op.DeleteParams{MarkerOwner: bob, MarkerOwnerDisplayName: "Bob", Mtime: deleted})
				Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
				Expect(listed()).To(Equal([]string{"", "v1"}))
				Expect(markers()).To(Equal([]string{""}))
				Expect(list()[0].Meta).To(Equal(rgwcls.DirEntryMeta{Owner: "acme$bob", OwnerDisplayName: "Bob", Mtime: deleted}), "the delete time given")
				_, ok := instanceEntry("", true)
				Expect(ok).To(BeTrue(), "the null delete marker's own instance entry")
				_, ok = instanceEntry("", false)
				Expect(ok).To(BeFalse(), "the null version's data entry, whose removal the link logged")
				Expect(olhHead().Data).To(BeEmpty(), "the null version's data, removed from the OLH head")
				current("")
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)

		DescribeTable("writes a delete marker on a delete marker",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				put(ctx, "v1")
				first := mustDel(ctx, "", op.DeleteParams{})
				second := mustDel(ctx, "", op.DeleteParams{})
				Expect(second.VersionID).NotTo(Equal(first.VersionID))
				Expect(listed()).To(Equal([]string{second.VersionID, first.VersionID, "v1"}))
				Expect(markers()).To(Equal([]string{second.VersionID, first.VersionID}))
				current(second.VersionID)
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)

		Describe("x-amz-delete-if-unmodified-since", func() {
			It("goes to the link, where a marker's fresh instance has no entry to compare it with", func(ctx SpecContext) {
				put(ctx, "v1")
				c.ResetCounters()
				since := now().Add(-24 * time.Hour)
				res := mustDel(ctx, "", op.DeleteParams{UnmodifiedSince: since})
				Expect(linkOps()).To(HaveLen(1))
				Expect(linkOps()[0].UnmodSince).To(BeTemporally("==", since), "set_olh's unmod_since, rgw_rados.cc:5899-5901")
				current(res.VersionID)
			})

			It("leaves a null delete marker no older than it in place", func(ctx SpecContext) {
				put(ctx, "v1")
				suspend()
				first := now().Add(-time.Hour)
				mustDel(ctx, "", op.DeleteParams{Mtime: first})
				res := mustDel(ctx, "", op.DeleteParams{Mtime: now(), UnmodifiedSince: first})
				Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}), "the link returns 0 with nothing written")
				Expect(list()[0].Meta.Mtime).To(BeTemporally("==", first), "the first marker's entry stands")
				Expect(pending()).To(BeEmpty(), "the attr of a link that logged nothing is removed")
				mustDel(ctx, "", op.DeleteParams{Mtime: now(), UnmodifiedSince: first.Add(time.Second)})
				Expect(list()[0].Meta.Mtime).To(BeTemporally("==", now()), "an older marker is replaced")
				Expect(markers()).To(Equal([]string{""}))
			})

			It("leaves a converted key's null data no older than it in place on a suspended bucket, the entry the class reads behind the version marker", func(ctx SpecContext) {
				converted(ctx)
				suspend()
				null, ok := instanceEntry("", false)
				Expect(ok).To(BeTrue(), "the null version's instance entry")
				res := mustDel(ctx, "", op.DeleteParams{Mtime: now(), UnmodifiedSince: null.Meta.Mtime.Add(-time.Hour)})
				Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}), "the link returns 0 with nothing written")
				Expect(markers()).To(BeEmpty())
				_, ok = instanceEntry("", true)
				Expect(ok).To(BeFalse(), "no null delete marker")
				still, ok := instanceEntry("", false)
				Expect(ok).To(BeTrue())
				Expect(still).To(Equal(null), "the null version's entry stands")
				Expect(listed()).To(Equal([]string{"v1", ""}))
				Expect(olhHead().Data).To(Equal([]byte("data-")), "its data, in the OLH head")
				current("v1")
			})
		})

		It("keeps the data of a null write that links after the null delete marker, dropping the marker's removal of the null version", func(ctx SpecContext) {
			converted(ctx)
			suspend()
			rw := newRGWWriter(c, s, rec, denc.Squid, "")
			after(testDataPool, olhOID, 1, func() {
				rw.Prepare(ctx)
				rw.HeadWrite(ctx)
				rw.Complete(ctx)
				after(rookIndexPool, shard, 1, func() {
					rw.ReadHead(ctx)
					rw.InitRetrying(ctx)
					rw.Link(ctx)
				})
			})
			Expect(mustDel(ctx, "", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}))
			rw.ReadLog(ctx)
			rw.Apply(ctx)
			rw.Trim(ctx)
			Expect(olhHead().Data).To(Equal([]byte("data-")), "the null write's data, which radosgw's replay removes")
			Expect(markers()).To(BeEmpty(), "the null write's link took the marker's entries")
			_, ok := instanceEntry("", false)
			Expect(ok).To(BeTrue(), "the null write's instance entry")
			Expect(listed()).To(Equal([]string{"", "v1"}))
			current("")
		})

		It("leaves the pending attr and the log for the next update with SkipOLHUpdate", func(ctx SpecContext) {
			put(ctx, "v1")
			res := mustDel(ctx, "", op.DeleteParams{SkipOLHUpdate: true})
			Expect(pending()).To(HaveLen(1))
			Expect(olhLog()).NotTo(BeEmpty())
			Expect(olhInfo().Target.Key.Instance).To(Equal("v1"), "olh.info as before the marker")
			Expect(listed()).To(Equal([]string{res.VersionID, "v1"}), "the class linked it")
			st, err := s.StatObject(ctx, rec, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.DeleteMarker).To(BeTrue(), "the next read's replay applies it")
			current(res.VersionID)
		})
	})

	Describe("with a versionId", func() {
		It("removes a noncurrent version, the current one kept", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			Expect(mustDel(ctx, "v1", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
			gone("v1")
			Expect(listed()).To(Equal([]string{"v2"}))
			current("v2")
		})

		It("removes the current version and promotes the next", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			Expect(mustDel(ctx, "v2", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v2"}))
			gone("v2")
			current("v1")
		})

		It("removes the key's last version and the OLH with it", func(ctx SpecContext) {
			put(ctx, "v1")
			mustDel(ctx, "v1", op.DeleteParams{})
			gone("v1")
			Expect(listed()).To(BeEmpty())
			Expect(olhHead()).To(BeNil(), "the OLH head")
			_, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeFalse(), "the OLH entry")
		})

		It("removes a delete marker, answering DeleteMarker true, and the version below is current again", func(ctx SpecContext) {
			put(ctx, "v1")
			m := mustDel(ctx, "", op.DeleteParams{})
			Expect(mustDel(ctx, m.VersionID, op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: m.VersionID, DeleteMarker: true}))
			_, ok := instanceEntry(m.VersionID, false)
			Expect(ok).To(BeFalse(), "the marker's instance entry, which the class removes")
			Expect(markers()).To(BeEmpty())
			current("v1")
		})

		It("removes the null version", func(ctx SpecContext) {
			converted(ctx)
			put(ctx, "v2")
			c.ResetCounters()
			Expect(mustDel(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
			Expect(unlinkOps()).To(HaveLen(1))
			Expect(unlinkOps()[0].Key).To(Equal(rgwcls.ObjKey{Name: "k"}), "the null instance cleared for the unlink")
			Expect(unlinkOps()[0].BILogFlags).To(Equal(rgwcls.BILogFlagVersionedOp|rgwcls.BILogNullVersion),
				"null_verid, rgw_op.cc:5187 at v19.2.7")
			_, ok := instanceEntry("", false)
			Expect(ok).To(BeFalse(), "the null version's instance entry")
			Expect(listed()).To(Equal([]string{"v2", "v1"}))
			Expect(olhHead().Data).To(BeEmpty(), "its data, removed from the OLH head")
			current("v2")
		})

		It("removes the null version of a pre-versioning object no link has converted, and the key with it", func(ctx SpecContext) {
			write(ctx, "")
			Expect(mustDel(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
			Expect(olhHead()).To(BeNil(), "test_versioning_obj_plain_null_version_removal's 404")
			Expect(listed()).To(BeEmpty())
			_, ok := c.Entry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeFalse(), "no entry at the plain key")
			st, err := s.StatObject(ctx, rec, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeFalse())
		})

		It("answers NoSuchKey for a version the index does not have, writing nothing", func(ctx SpecContext) {
			put(ctx, "v1")
			c.ResetCounters()
			_, err := del(ctx, "nope", op.DeleteParams{})
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero())
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
			Expect(c.Reads(rookIndexPool, "", shard)).To(Equal(1), "one bi_get of the instance, rgw_rados.cc:5908")
			current("v1")
		})

		It("leaves the pending attr, the log and the version's head for the next update with SkipOLHUpdate", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			mustDel(ctx, "v1", op.DeleteParams{SkipOLHUpdate: true})
			Expect(pending()).To(HaveLen(1))
			Expect(olhLog()).NotTo(BeEmpty())
			Expect(versionHead("v1")).NotTo(BeNil(), "the replay that removes it has not run")
			Expect(listed()).To(Equal([]string{"v2"}), "the class unlinked it")
			Expect(s.StatObject(ctx, rec, version(""))).Error().To(Succeed())
			gone("v1")
			current("v2")
		})
	})

	Describe("versionId=null", func() {
		DescribeTable("never writes a delete marker",
			func(ctx SpecContext, suspended, withNull bool) {
				if withNull {
					converted(ctx)
				} else {
					put(ctx, "v1")
				}
				if suspended {
					suspend()
				}
				c.ResetCounters()
				res, err := del(ctx, "null", op.DeleteParams{})
				settle(s)
				if withNull {
					Expect(err).NotTo(HaveOccurred())
					Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
				} else {
					Expect(err).To(MatchError(op.ErrNoSuchKey))
					Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero(), "nothing written")
				}
				Expect(markers()).To(BeEmpty())
				_, ok := instanceEntry("", true)
				Expect(ok).To(BeFalse(), "no null delete marker")
				Expect(listed()).To(Equal([]string{"v1"}))
				current("v1")
			},
			Entry("on an enabled bucket, removing its null version", false, true),
			Entry("on an enabled bucket without a null version, NoSuchKey", false, false),
			Entry("on a suspended bucket, removing its null version", true, true),
			Entry("on a suspended bucket without a null version, NoSuchKey", true, false),
		)

		Describe("on a converted key", func() {
			BeforeEach(func(ctx SpecContext) { converted(ctx) })

			It("checks If-Match against the pre-versioning object's ETag, not the version marker's", func(ctx SpecContext) {
				c.ResetCounters()
				_, err := del(ctx, "null", op.DeleteParams{IfMatch: `"etag-nope"`})
				conditionFailed(err)
				Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero())
				Expect(mustDel(ctx, "null", op.DeleteParams{IfMatch: `"etag-"`})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
				Expect(listed()).To(Equal([]string{"v1"}))
			})

			It("passes If-Match, size and last-modified as the listing shows the null version", func(ctx SpecContext) {
				i := slices.IndexFunc(list(), func(en rgwcls.DirEntry) bool { return en.Key.Instance == "" })
				Expect(i).To(BeNumerically(">=", 0), "the null version's list entry")
				m := list()[i].Meta
				p := op.DeleteParams{IfMatch: `"` + m.ETag + `"`, IfMatchSize: &m.Size, IfMatchLastModified: m.Mtime}
				Expect(mustDel(ctx, "null", p)).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
				Expect(listed()).To(Equal([]string{"v1"}))
			})

			It("reports DeleteMarker true for the removal of the null delete marker", func(ctx SpecContext) {
				suspend()
				mustDel(ctx, "", op.DeleteParams{})
				Expect(markers()).To(Equal([]string{""}))
				Expect(mustDel(ctx, "null", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}),
					"read behind the version marker, where radosgw reads the marker's flags (docs/ceph-upstream-bugs.md, "+
						"\"radosgw's versioned delete of versionId=null checks the version marker, not the null version\")")
				_, ok := instanceEntry("", true)
				Expect(ok).To(BeFalse())
				Expect(markers()).To(BeEmpty())
				current("v1")
			})
		})
	})

	Describe("the conditions", func() {
		// The fixture's version heads are stamped with the clock at their
		// write, and their index entries with versionMeta's earlier mtime,
		// so a last-modified condition tells which the delete compared.
		DescribeTable("of a delete marker are checked against the current version, as check_preconditions checks them",
			func(ctx SpecContext, r denc.Release, p func() op.DeleteParams, want error) {
				open(ctx, r)
				put(ctx, "v1")
				res, err := del(ctx, "", p())
				settle(s)
				if want != nil {
					Expect(err).To(MatchError(want))
					Expect(markers()).To(BeEmpty(), "no marker")
					current("v1")
					return
				}
				Expect(err).NotTo(HaveOccurred())
				current(res.VersionID)
			},
			Entry("If-Match naming the current version's ETag, on squid", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"etag-v1"`} }, nil),
			Entry("If-Match naming the current version's ETag, on tentacle", denc.Tentacle, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"etag-v1"`} }, nil),
			Entry("If-Match that starts with the ETag, which the prefix compare passes", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: "etag-v1x"} }, nil),
			Entry("If-Match naming another ETag, on squid", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"etag-v0"`} }, op.ErrPreconditionFailed),
			Entry("If-Match naming another ETag, on tentacle", denc.Tentacle, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"etag-v0"`} }, op.ErrPreconditionFailed),
			Entry("If-Match: *", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: "*"} }, nil),
			Entry("x-amz-if-match-size of the current version", denc.Tentacle, func() op.DeleteParams {
				return op.DeleteParams{IfMatchSize: new(uint64(len("data-v1")))}
			}, nil),
			Entry("x-amz-if-match-size of another", denc.Tentacle, func() op.DeleteParams { return op.DeleteParams{IfMatchSize: new(uint64(1))} }, op.ErrPreconditionFailed),
			Entry("x-amz-if-match-last-modified-time of the current version's head", denc.Squid, func() op.DeleteParams {
				return op.DeleteParams{IfMatchLastModified: now().Add(500 * time.Millisecond)}
			}, nil),
			Entry("x-amz-if-match-last-modified-time of its index entry, which is not the head's", denc.Squid, func() op.DeleteParams {
				return op.DeleteParams{IfMatchLastModified: versionMeta(version("v1")).Mtime}
			}, op.ErrPreconditionFailed),
		)

		DescribeTable("of a delete marker are checked against the key's head when a delete marker is current, as radosgw checks them",
			func(ctx SpecContext, null bool, p func() op.DeleteParams, want error) {
				if null {
					converted(ctx)
				} else {
					put(ctx, "v1")
				}
				m := mustDel(ctx, "", op.DeleteParams{})
				res, err := del(ctx, "", p())
				settle(s)
				if want != nil {
					Expect(err).To(MatchError(want))
					Expect(markers()).To(Equal([]string{m.VersionID}), "no second marker")
					current(m.VersionID)
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(markers()).To(Equal([]string{res.VersionID, m.VersionID}), "a marker on the marker")
				current(res.VersionID)
			},
			Entry("If-Match: *, the head existing", false, func() op.DeleteParams { return op.DeleteParams{IfMatch: "*"} }, nil),
			Entry("If-Match naming the noncurrent version's ETag, the head having none", false, func() op.DeleteParams {
				return op.DeleteParams{IfMatch: `"etag-v1"`}
			}, op.ErrPreconditionFailed),
			Entry("x-amz-if-match-size 0, the head's", false, func() op.DeleteParams { return op.DeleteParams{IfMatchSize: new(uint64(0))} }, nil),
			Entry("x-amz-if-match-size of the noncurrent version", false, func() op.DeleteParams {
				return op.DeleteParams{IfMatchSize: new(uint64(len("data-v1")))}
			}, op.ErrPreconditionFailed),
			Entry("If-Match naming a noncurrent null version's ETag, its data in the head", true, func() op.DeleteParams {
				return op.DeleteParams{IfMatch: `"etag-"`}
			}, nil),
		)

		It("answers test_delete_object_current_if_match's sequence as radosgw does", func(ctx SpecContext) {
			// s3-tests' test_delete_object_current_if_match at the commit
			// hack/s3tests/PIN names: a missing key's deletes find nothing, and a
			// key whose current version is a delete marker is "marked as deleted
			// but still exists".
			Expect(del(ctx, "", op.DeleteParams{IfMatch: "*"})).Error().To(MatchError(op.ErrNoSuchKey), "a missing key, the op's 204")
			Expect(del(ctx, "", op.DeleteParams{IfMatch: "badetag"})).Error().To(MatchError(op.ErrNoSuchKey))
			put(ctx, "v1")
			Expect(del(ctx, "", op.DeleteParams{IfMatch: "badetag"})).Error().To(MatchError(op.ErrPreconditionFailed), "on the current version")
			first := mustDel(ctx, "", op.DeleteParams{IfMatch: `"etag-v1"`})
			Expect(first.DeleteMarker).To(BeTrue())
			second := mustDel(ctx, "", op.DeleteParams{IfMatch: "*"})
			Expect(del(ctx, "", op.DeleteParams{IfMatch: "badetag"})).Error().To(MatchError(op.ErrPreconditionFailed), "the marked key still exists")
			Expect(mustDel(ctx, "v1", op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
			third := mustDel(ctx, "", op.DeleteParams{IfMatch: "*"})
			Expect(markers()).To(Equal([]string{third.VersionID, second.VersionID, first.VersionID}))
			current(third.VersionID)
		})

		It("of a delete marker see a missing version when the current version's head is gone, as radosgw's follow resolves it", func(ctx SpecContext) {
			put(ctx, "v1")
			c.Remove(testDataPool, "", instanceHeadOID(version("v1")))
			_, err := del(ctx, "", op.DeleteParams{IfMatch: "*"})
			Expect(err).To(MatchError(op.ErrNoSuchKey), "not the OLH head's state")
			Expect(markers()).To(BeEmpty())
			current("v1")
		})

		It("of a delete marker on a key with no version answer NoSuchKey, writing nothing", func(ctx SpecContext) {
			_, err := del(ctx, "", op.DeleteParams{IfMatch: "*"})
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			Expect(olhHead()).To(BeNil())
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
		})

		DescribeTable("of a version are checked against its index entry, as delete_obj checks them there",
			func(ctx SpecContext, r denc.Release, p func() op.DeleteParams, want error) {
				open(ctx, r)
				put(ctx, "v1")
				put(ctx, "v2")
				c.ResetCounters()
				res, err := del(ctx, "v1", p())
				settle(s)
				if want != nil {
					Expect(err).To(MatchError(want))
					Expect(c.Writes(testDataPool, "", olhOID)).To(BeZero(), "nothing written")
					Expect(listed()).To(Equal([]string{"v2", "v1"}))
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "v1"}))
				gone("v1")
			},
			Entry("If-Match naming its ETag, on squid", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"etag-v1"`} }, nil),
			Entry("If-Match naming its ETag, on tentacle", denc.Tentacle, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"etag-v1"`} }, nil),
			Entry("If-Match naming the current version's ETag", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: `"etag-v2"`} }, op.ErrPreconditionFailed),
			Entry("If-Match that starts with its ETag, which the whole compare refuses", denc.Tentacle, func() op.DeleteParams { return op.DeleteParams{IfMatch: "etag-v1x"} }, op.ErrPreconditionFailed),
			Entry("If-Match: *", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: "*"} }, nil),
			Entry("If-Match present and empty", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatch: "\x00"} }, op.ErrPreconditionFailed),
			Entry("x-amz-if-match-size of its entry", denc.Tentacle, func() op.DeleteParams {
				return op.DeleteParams{IfMatchSize: new(versionMeta(version("v1")).Size)}
			}, nil),
			Entry("x-amz-if-match-size of another", denc.Squid, func() op.DeleteParams { return op.DeleteParams{IfMatchSize: new(uint64(1))} }, op.ErrPreconditionFailed),
			Entry("x-amz-if-match-last-modified-time of its entry, in whole seconds", denc.Squid, func() op.DeleteParams {
				return op.DeleteParams{IfMatchLastModified: versionMeta(version("v1")).Mtime.Add(999 * time.Millisecond)}
			}, nil),
			Entry("x-amz-if-match-last-modified-time of its head, which is not the entry's", denc.Tentacle, func() op.DeleteParams {
				return op.DeleteParams{IfMatchLastModified: now()}
			}, op.ErrPreconditionFailed),
			Entry("x-amz-delete-if-unmodified-since, which radosgw does not read there", denc.Squid, func() op.DeleteParams {
				return op.DeleteParams{UnmodifiedSince: time.Unix(0, 0)}
			}, nil),
		)

		It("of a delete marker's version match an empty If-Match to its entry, which has no ETag", func(ctx SpecContext) {
			put(ctx, "v1")
			m := mustDel(ctx, "", op.DeleteParams{})
			Expect(mustDel(ctx, m.VersionID, op.DeleteParams{IfMatch: "\x00"})).To(Equal(&op.DeleteObjectResult{VersionID: m.VersionID, DeleteMarker: true}))
			current("v1")
		})
	})

	Describe("a conditional delete marker", func() {
		// rgwLink is a radosgw-shaped PutObject of version rgw, its index
		// entry completed, whose head read, init, link and replay run once
		// the delete's init has landed.
		rgwLink := func(ctx context.Context, r denc.Release) {
			GinkgoHelper()
			rw := newRGWWriter(c, s, rec, r, "rgw")
			rw.Prepare(ctx)
			rw.HeadWrite(ctx)
			rw.Complete(ctx)
			after(testDataPool, olhOID, 1, func() {
				rw.ReadHead(ctx)
				rw.InitRetrying(ctx)
				rw.Link(ctx)
				rw.ReadLog(ctx)
				rw.Apply(ctx)
				rw.Trim(ctx)
			})
		}

		DescribeTable("answers 412 and leaves no marker on an enabled bucket when a radosgw writer links between its init and its link",
			func(ctx SpecContext, r denc.Release) {
				open(ctx, r)
				put(ctx, "v1")
				rgwLink(ctx, r)
				_, err := del(ctx, "", op.DeleteParams{IfMatch: `"etag-v1"`})
				conditionFailed(err)
				settle(s)
				Expect(markers()).To(BeEmpty())
				Expect(instanceMarkers()).To(BeEmpty(), "the void marker's unlink removed its instance entry")
				Expect(listed()).To(Equal([]string{"rgw", "v1"}))
				current("rgw")
			},
			Entry("on squid", denc.Squid),
			Entry("on tentacle", denc.Tentacle),
		)

		It("on a suspended bucket is not checked after its link, so the null marker lands over a radosgw writer that linked in between", func(ctx SpecContext) {
			put(ctx, "v1")
			suspend()
			rgwLink(ctx, denc.Squid)
			res := mustDel(ctx, "", op.DeleteParams{IfMatch: `"etag-v1"`})
			Expect(res).To(Equal(&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}),
				"its init's guard holds against rgw-go's conditional writers alone")
			Expect(listed()).To(Equal([]string{"", "rgw", "v1"}))
			current("")
		})

		// nullGoes arms an unconditional versionId=null delete of k to run
		// once the next write to the OLH head, the conditional marker's
		// init, has landed.
		nullGoes := func(ctx context.Context) {
			after(testDataPool, olhOID, 1, func() {
				Expect(s.DeleteObject(ctx, rec, version("null"), op.DeleteParams{})).To(Equal(&op.DeleteObjectResult{VersionID: "null"}))
			})
		}

		DescribeTable("answers 412 and leaves no second marker on an enabled bucket when the null version its pass rested on in the OLH head goes between its init and its link",
			func(ctx SpecContext, p func() op.DeleteParams) {
				converted(ctx)
				m := mustDel(ctx, "", op.DeleteParams{})
				nullGoes(ctx)
				_, err := del(ctx, "", p())
				conditionFailed(err)
				settle(s)
				Expect(markers()).To(Equal([]string{m.VersionID}), "no second marker")
				Expect(instanceMarkers()).To(HaveLen(1), "the void marker's unlink removed its instance entry")
				Expect(listed()).To(Equal([]string{m.VersionID, "v1"}), "the null version is gone")
				current(m.VersionID)
			},
			Entry("If-Match naming its ETag", func() op.DeleteParams { return op.DeleteParams{IfMatch: `"etag-"`} }),
			Entry("x-amz-if-match-size of its data", func() op.DeleteParams { return op.DeleteParams{IfMatchSize: new(uint64(len("data-")))} }),
			Entry("x-amz-if-match-last-modified-time of the OLH head", func() op.DeleteParams { return op.DeleteParams{IfMatchLastModified: now()} }),
		)

		It("commits on an enabled bucket when its If-Match: * passed on the OLH head and the null version goes between its init and its link: the head stays", func(ctx SpecContext) {
			converted(ctx)
			m := mustDel(ctx, "", op.DeleteParams{})
			nullGoes(ctx)
			res := mustDel(ctx, "", op.DeleteParams{IfMatch: "*"})
			Expect(markers()).To(Equal([]string{res.VersionID, m.VersionID}), "a marker on the marker")
			Expect(listed()).To(Equal([]string{res.VersionID, m.VersionID, "v1"}))
			current(res.VersionID)
		})

		It("commits on an enabled bucket when the null version its pass rested on in the OLH head is listed below more than a page of newer versions", func(ctx SpecContext) {
			converted(ctx)
			for i := range driver.OLHCondListPage + 2 {
				put(ctx, fmt.Sprintf("v%02d", i+2))
			}
			m := mustDel(ctx, "", op.DeleteParams{})
			res := mustDel(ctx, "", op.DeleteParams{IfMatch: `"etag-"`})
			Expect(markers()).To(Equal([]string{res.VersionID, m.VersionID}), "a marker on the marker")
			Expect(listed()).To(HaveLen(driver.OLHCondListPage + 6))
			Expect(listed()[len(listed())-1]).To(Equal(""), "the null version lists last, at epoch 1")
			current(res.VersionID)
		})
	})
})
