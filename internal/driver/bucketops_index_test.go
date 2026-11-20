package driver_test

import (
	"bytes"
	"context"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

var _ = Describe("DeleteBucket's index check", func() {
	Describe("on a bucket the S3 ops wrote", func() {
		const zgID = "zg-ceph-objectstore"
		var (
			c   *fakerados.Cluster
			s   *driver.Store
			rec *op.BucketRecord
		)
		setup := func(ctx context.Context, r denc.Release) {
			GinkgoHelper()
			c = newPutCluster()
			s = openPutStore(ctx, c, r, map[string]string{"rgw_cache_enabled": "false"}, time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
			DeferCleanup(s.Close)
			var err error
			rec, err = s.CreateBucket(ctx, op.CreateBucketParams{
				Name: "b", Owner: initiator, Zonegroup: zgID,
				Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs:     attrsWithACL(initiator), Exclusive: true,
			})
			Expect(err).NotTo(HaveOccurred())
		}
		suspend := func(ctx context.Context) {
			GinkgoHelper()
			cur, err := s.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			cur.Info.Flags |= meta.BucketVersioned | meta.BucketVersionsSuspended
			Expect(s.PutBucketInfo(ctx, cur)).To(Succeed())
			rec, err = s.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
		}
		put := func(ctx context.Context) {
			GinkgoHelper()
			body := []byte("hello")
			_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(body))})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
		}
		del := func(ctx context.Context, key meta.ObjKey) {
			GinkgoHelper()
			_, err := s.DeleteObject(ctx, rec, key, op.DeleteParams{})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
		}
		sums := func(ctx context.Context) (stored, recount uint64) {
			GinkgoHelper()
			existing, calculated, err := s.CheckIndex(ctx, rec)
			Expect(err).NotTo(HaveOccurred())
			for _, st := range existing {
				stored += st.NumObjects
			}
			for _, st := range calculated {
				recount += st.NumObjects
			}
			return stored, recount
		}

		// A suspended bucket's delete links a null delete marker over the null
		// data, and the class writes the marker's instance entry with the data's
		// exists flag. The admin index fix counts it; removing the marker
		// uncounts nothing, so the stored header stays one above the index.
		DescribeTable("deletes a bucket whose stored header counts a null delete marker the admin index fix counted and a delete then removed",
			func(ctx SpecContext, r denc.Release, nullOnOLH bool) {
				setup(ctx, r)
				if nullOnOLH {
					suspend(ctx)
					put(ctx)
				} else {
					put(ctx)
					suspend(ctx)
				}
				del(ctx, meta.ObjKey{Name: "k"})
				Expect(s.RebuildIndex(ctx, rec)).To(Succeed())
				del(ctx, meta.ObjKey{Name: "k", Instance: meta.NullInstance})
				res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{ListVersions: true, MaxKeys: 100})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Entries).To(BeEmpty())
				stored, recount := sums(ctx)
				Expect([]uint64{stored, recount}).To(Equal([]uint64{1, 0}), "the stored header drifted one above the index")
				Expect(s.DeleteBucket(ctx, rec)).To(Succeed())
				Expect(c.Objects(rookIndexPool, "")).To(BeEmpty())
			},
			Entry("Squid, a plain object before versioning", denc.Squid, false),
			Entry("Tentacle, a plain object before versioning", denc.Tentacle, false),
			Entry("Squid, null data on the OLH", denc.Squid, true),
			Entry("Tentacle, null data on the OLH", denc.Tentacle, true),
		)
	})

	Describe("on a versioned bucket", func() {
		var (
			c   *fakerados.Cluster
			ac  *aroundCluster
			s   *driver.Store
			rec *op.BucketRecord
			v1  meta.ObjKey
		)
		setup := func(ctx context.Context, r denc.Release) {
			GinkgoHelper()
			c = newPutCluster()
			ac = &aroundCluster{Cluster: c}
			var err error
			s, err = driver.Open(ctx, ac, conf(nil), driver.Options{Release: &r})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(s.Close)
			s.SetReshardWaitForTest(0)
			rec = testBucket(putBucketID, 11)
			rec.Info.Flags |= meta.BucketVersioned
			seedShards(c, rec)
			seedRecordInstance(c, rec)
			v1 = meta.ObjKey{Name: "k", Instance: "v1"}
			DeferCleanup(func() { ac.set(nil); ac.setBeforeRead(nil) })
		}
		// unlinkedWrite leaves v1 as a versioned write whose link never landed:
		// its head and its completed instance entry, and no list entry.
		unlinkedWrite := func(ctx context.Context) {
			GinkgoHelper()
			x := s.NewIndexOpForTest(rec, v1, "go-tag-v1")
			x.SetBILogFlagsForTest(rgwcls.BILogFlagVersionedOp)
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			ver := writeInstanceHead(ctx, c, v1)
			Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: ver}, versionMeta(v1))).To(Succeed())
			settle(s)
		}
		headLeft := func() bool { return c.Object(testDataPool, "", instanceHeadOID(v1)) != nil }
		instanceOID := func() string { return rec.Info.Bucket.InstanceOID() }
		claimed := func() bool {
			o := c.Object(rookMetaPool, rookRoot, instanceOID())
			return o != nil && o.Xattrs[op.RemovingAttr] != nil
		}
		// reshard commits a reshard of rec's index to the next generation, as
		// radosgw's commit leaves it: the entries and headers copied, the
		// instance written at the next version, the old shards removed.
		reshard := func() *op.BucketRecord {
			next := *rec
			og := rec.Info.Layout.Current
			next.Info.Layout.Current.Gen = og.Gen + 1
			ng := next.Info.Layout.Current
			for i := range og.Layout.Normal.NumShards {
				oo, no := rec.Info.IndexShardOID(og, i), next.Info.IndexShardOID(ng, i)
				seedShardHeader(c, rookIndexPool, no, c.Header(rookIndexPool, "", oo))
				maps.Copy(c.Object(rookIndexPool, "", no).Omap, c.Object(rookIndexPool, "", oo).Omap)
			}
			// The commit writes the instance with its attrs, as radosgw's
			// put_bucket_instance_info does, and at a new version.
			attrs := map[string][]byte{}
			for k, v := range c.Object(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID()).Xattrs {
				if strings.HasPrefix(k, meta.AttrPrefix) {
					attrs[k] = v
				}
			}
			seedInstance(c, next.Info, attrs, meta.ObjVersion{Ver: 9, Tag: "reshard"})
			for i := range og.Layout.Normal.NumShards {
				c.Remove(rookIndexPool, "", rec.Info.IndexShardOID(og, i))
			}
			return &next
		}

		DescribeTable("refuses the S3 delete of a bucket whose index holds an unlisted version, and removes it with the purge's delete",
			func(ctx SpecContext, r denc.Release) {
				setup(ctx, r)
				unlinkedWrite(ctx)
				Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty))
				Expect(headLeft()).To(BeTrue())
				Expect(claimed()).To(BeFalse(), "the refusal cleared its removal's claim")
				Expect(c.Object(rookMetaPool, rookRoot, instanceOID())).NotTo(BeNil())
				Expect(s.DeleteBucketPurged(ctx, rec)).To(Succeed())
				Expect(c.Object(rookMetaPool, rookRoot, instanceOID())).To(BeNil())
				Expect(c.Objects(rookIndexPool, "")).To(BeEmpty(), "the purge's delete removes the index, as radosgw's does")
				Expect(headLeft()).To(BeTrue(), "and leaves the version's head, as radosgw's does")
			},
			Entry("on Squid", denc.Squid),
			Entry("on Tentacle", denc.Tentacle),
		)

		DescribeTable("refuses a delete whose index a reshard moved after the listing, and leaves no version's head orphaned",
			func(ctx SpecContext, r denc.Release) {
				setup(ctx, r)
				unlinkedWrite(ctx)
				var (
					next   *op.BucketRecord
					mu     sync.Mutex
					listed bool
				)
				// The listings read the shards in parallel: the first read of
				// the multipart abort's listing commits the reshard, once.
				ac.setBeforeRead(func(rop *radosclient.ReadOp) {
					for _, st := range rop.Steps() {
						x, ok := st.(*radosclient.ExecStep)
						if !ok || x.Method != "bucket_list" {
							continue
						}
						l := rgwcls.DecodeListOp(denc.NewDecoder(x.In))
						mu.Lock()
						if !strings.HasPrefix(l.FilterPrefix, "_multipart") {
							listed = true
						} else if listed && next == nil {
							next = reshard()
						}
						mu.Unlock()
						return
					}
				})
				Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty))
				ac.setBeforeRead(nil)
				Expect(next).NotTo(BeNil(), "the reshard committed at the multipart abort's listing")
				Expect(headLeft()).To(BeTrue())
				Expect(claimed()).To(BeFalse())
				Expect(s.DeleteBucket(ctx, next)).To(MatchError(op.ErrBucketNotEmpty), "a retry counts the new generation")
				Expect(headLeft()).To(BeTrue())
				Expect(c.Object(rookIndexPool, "", next.Info.IndexShardOID(next.Info.Layout.Current, 0))).NotTo(BeNil())
			},
			Entry("on Squid", denc.Squid),
			Entry("on Tentacle", denc.Tentacle),
		)

		// A reshard that commits once the check has passed moves the index to a
		// generation the check never judged; v1's entry is written into the new
		// generation alone, as a write that lands there after the commit. The
		// reshard lands either between the check and the entry point's removal,
		// at the instance's read that follows the check, or at the instance's
		// removal itself, once the entry point is gone.
		DescribeTable("refuses a delete whose index a reshard moved after the check, leaving the bucket as it stood",
			func(ctx SpecContext, r denc.Release, atRemoval bool) {
				setup(ctx, r)
				orig := rec
				ep := meta.NewBucketEntryPoint()
				ep.Bucket, ep.Owner, ep.Linked = orig.Info.Bucket, orig.Info.Owner, true
				c.Put(rookMetaPool, rookRoot, orig.Info.Bucket.EntryPointOID(), encode(ep))
				var (
					next *op.BucketRecord
					once sync.Once
				)
				commit := func() {
					once.Do(func() {
						next = reshard()
						rec = next
						unlinkedWrite(ctx)
						rec = orig
					})
				}
				if atRemoval {
					writes := 0
					c.BeforeWrite(rookMetaPool, rookRoot, instanceOID(), func(*fakerados.Object) {
						if writes++; writes == 2 {
							commit()
						}
					})
				} else {
					var checks atomic.Int32
					ac.setBeforeRead(func(rop *radosclient.ReadOp) {
						if callsMethod(rop.Steps(), "bucket_check_index") && checks.Add(1) == int32(orig.Info.Layout.Current.Layout.Normal.NumShards) {
							c.BeforeRead(rookMetaPool, rookRoot, instanceOID(), commit)
						}
					})
				}
				err := s.DeleteBucket(ctx, orig)
				c.BeforeWrite(rookMetaPool, rookRoot, instanceOID(), nil)
				c.BeforeRead(rookMetaPool, rookRoot, instanceOID(), nil)
				ac.setBeforeRead(nil)
				Expect(next).NotTo(BeNil(), "the reshard committed")
				Expect(err).To(MatchError(op.ErrBucketNotEmpty))
				Expect(err).To(MatchError(ContainSubstring("moved from generation 0 to 1")))
				Expect(headLeft()).To(BeTrue(), "v1's head")
				Expect(c.Object(rookMetaPool, rookRoot, instanceOID())).NotTo(BeNil(), "the instance")
				Expect(c.Object(rookMetaPool, rookRoot, orig.Info.Bucket.EntryPointOID())).NotTo(BeNil(), "the entry point, written back when the removal had taken it")
				Expect(claimed()).To(BeFalse(), "the claim, cleared")
				Expect(c.Object(rookIndexPool, "", next.Info.IndexShardOID(next.Info.Layout.Current, 0))).NotTo(BeNil(), "the new generation's index")

				Expect(s.DeleteBucket(ctx, orig)).To(MatchError(ContainSubstring("its index holds 1 entries")), "a retry counts the new generation")
				Expect(headLeft()).To(BeTrue())
				Expect(c.Object(rookMetaPool, rookRoot, orig.Info.Bucket.EntryPointOID())).NotTo(BeNil())
			},
			Entry("on Squid, before the entry point's removal", denc.Squid, false),
			Entry("on Tentacle, before the entry point's removal", denc.Tentacle, false),
			Entry("on Squid, at the instance's removal", denc.Squid, true),
			Entry("on Tentacle, at the instance's removal", denc.Tentacle, true),
		)

		// The instance's removal fails otherwise than by a moved generation,
		// once the entry point is gone: the OSD refuses it with EIO.
		DescribeTable("leaves the bucket named and deletable when its instance's removal fails after the entry point's removal",
			func(ctx SpecContext, r denc.Release) {
				setup(ctx, r)
				ep := meta.NewBucketEntryPoint()
				ep.Bucket, ep.Owner, ep.Linked = rec.Info.Bucket, rec.Info.Owner, true
				c.Put(rookMetaPool, rookRoot, rec.Info.Bucket.EntryPointOID(), encode(ep))
				var once sync.Once
				c.AfterWrite(rookMetaPool, rookRoot, instanceOID(), func() {
					once.Do(func() { c.FailNextWrite(rookMetaPool, rookRoot, instanceOID(), syscall.EIO) })
				})
				err := s.DeleteBucket(ctx, rec)
				c.AfterWrite(rookMetaPool, rookRoot, instanceOID(), nil)
				Expect(err).To(HaveOccurred(), "the instance's removal failed")
				Expect(err).NotTo(MatchError(op.ErrBucketNotEmpty))
				Expect(c.Object(rookMetaPool, rookRoot, rec.Info.Bucket.EntryPointOID())).NotTo(BeNil(), "the entry point, written back")
				Expect(c.Object(rookMetaPool, rookRoot, instanceOID())).NotTo(BeNil(), "the instance")
				Expect(claimed()).To(BeFalse(), "the claim, cleared")
				Expect(c.Objects(rookIndexPool, "")).NotTo(BeEmpty(), "the index")

				Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "a retry")
				Expect(c.Object(rookMetaPool, rookRoot, rec.Info.Bucket.EntryPointOID())).To(BeNil())
				Expect(c.Object(rookMetaPool, rookRoot, instanceOID())).To(BeNil())
				Expect(c.Objects(rookIndexPool, "")).To(BeEmpty())
			},
			Entry("on Squid", denc.Squid),
			Entry("on Tentacle", denc.Tentacle),
		)

		DescribeTable("answers BucketNotEmpty, removing nothing, when this gateway's completion does not land within the await's bound",
			func(ctx SpecContext, r denc.Release) {
				setup(ctx, r)
				unlinkedWrite(ctx)
				driver.SetIndexAwaitForTest(s, 100*time.Millisecond)
				release := make(chan struct{})
				var held atomic.Bool
				ac.set(onFirstMatching(isCompleteDel, func(send sendFunc) (uint64, error) {
					held.Store(true)
					<-release
					return send()
				}))
				x := s.NewIndexOpForTest(rec, v1, "del-v1")
				Expect(x.Prepare(ctx, rgwcls.OpDel)).To(Succeed())
				x.CompleteDel(rgwcls.EntryVer{Pool: 7, Epoch: 2}, indexMtime)
				Eventually(held.Load).Should(BeTrue())
				start := time.Now()
				Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty))
				Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))
				Expect(c.Object(rookMetaPool, rookRoot, instanceOID())).NotTo(BeNil())
				Expect(claimed()).To(BeFalse(), "the await comes before the claim")
				close(release)
				settle(s)
				Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "a retry once the completion landed")
			},
			Entry("on Squid", denc.Squid),
			Entry("on Tentacle", denc.Tentacle),
		)

		It("bounds the await at the relist's cap, 32 s", func() {
			Expect(driver.IndexAwaitCap).To(Equal(driver.RelistCap))
			Expect(driver.IndexAwaitCap).To(Equal(32 * time.Second))
		})
	})
})

// isCompleteDel takes a bucket_complete_op DEL.
func isCompleteDel(w *radosclient.WriteOp) bool {
	for _, step := range w.Steps() {
		if x, ok := step.(*radosclient.ExecStep); ok && x.Method == "bucket_complete_op" {
			return rgwcls.DecodeCompleteOp(denc.NewDecoder(x.In)).Op == rgwcls.OpDel
		}
	}
	return false
}
