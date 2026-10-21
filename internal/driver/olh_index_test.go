package driver_test

import (
	"context"
	"maps"
	"sync"
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

// encodeAt renders a class request as release r encodes it.
func encodeAt(v encoder, r denc.Release) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}

// readsCluster is a fakerados cluster whose pools record the steps of each
// read op they run, by object name.
type readsCluster struct {
	*fakerados.Cluster
	mu    sync.Mutex
	reads map[string][][]radosclient.Step
}

func (c *readsCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return &readsPool{Pool: p, c: c}, nil
}

// biGets decodes the bi_get request of each read op run on oid, in order.
func (c *readsCluster) biGets(oid string) []rgwcls.BIGetOp {
	GinkgoHelper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []rgwcls.BIGetOp
	for _, steps := range c.reads[oid] {
		Expect(steps).To(HaveLen(1), "bi_get is a read op of its own, rgw_rados.cc:9487-9502 at v19.2.7")
		x := execStep(steps[0])
		Expect(x.Method).To(Equal("bi_get"))
		d := denc.NewDecoder(x.In)
		out = append(out, rgwcls.DecodeBIGetOp(d))
		Expect(d.Err()).NotTo(HaveOccurred())
	}
	return out
}

type readsPool struct {
	radosclient.Pool
	c *readsCluster
}

func (p *readsPool) Read(ctx context.Context, oid string, op *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	p.c.mu.Lock()
	p.c.reads[oid] = append(p.c.reads[oid], op.Steps())
	p.c.mu.Unlock()
	return p.Pool.Read(ctx, oid, op, flags)
}

var _ = Describe("the OLH index operations", func() {
	const (
		bucketID = "zone-ceph-objectstore.4156.1"
		// tag ends in a NUL, which the requests carry whole, as
		// bufferlist::to_str and string(c_str(), length()) keep it.
		tag = "olhtag-olhtag-olhtag-olhtag\x00"
	)
	var (
		c   *fakerados.Cluster
		rc  *readsCluster
		s   *driver.Store
		rec *op.BucketRecord
		oid string
		idx *driver.OLHIndex
	)
	open := func(ctx context.Context, release denc.Release) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, rc, conf(nil), driver.Options{Release: &release})
		Expect(err).NotTo(HaveOccurred())
		s.SetReshardWaitForTest(time.Millisecond)
		idx = s.NewOLHIndexForTest(rec, meta.ObjKey{Name: "k"})
	}
	BeforeEach(func(ctx SpecContext) {
		c = newIndexCluster()
		rc = &readsCluster{Cluster: c, reads: map[string][][]radosclient.Step{}}
		rec = testBucket(bucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		oid = indexShardOID(rec, "k")
		open(ctx, denc.Squid)
	})
	trace := func() []string { return []string{s.Zone().ID + ":plain:" + bucketID} }
	// head is the OLH head of k as get_obj_state reads it, its olh.idtag idTag.
	head := func(idTag string) *op.ObjectState {
		return &op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: "k"}, Exists: true, Attrs: map[string][]byte{meta.AttrOLHIDTag: []byte(idTag)}}
	}
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	// write gives key the index entry a write gives it: a prepare, then the
	// awaited ADD completion with etag.
	write := func(ctx context.Context, key meta.ObjKey, etag string) {
		GinkgoHelper()
		x := s.NewIndexOpForTest(rec, key, "write-"+etag)
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 1},
			rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, ETag: etag})).To(Succeed())
	}
	link := func(ctx context.Context, key meta.ObjKey, opTag string) {
		GinkgoHelper()
		Expect(idx.LinkOLH(ctx, head(tag), key, opTag, driver.OLHLinkFields{})).To(Succeed())
	}
	// guarded checks that w is assert_exists, the reshard guard and one call
	// of method, and returns that call's input.
	guarded := func(w fakerados.RecordedWrite, method string) []byte {
		GinkgoHelper()
		steps := w.Steps()
		Expect(steps).To(HaveLen(3))
		Expect(steps[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}), "the bucket index shard must exist")
		Expect(execIn(w, 1, "guard_bucket_resharding", rgwcls.DecodeGuardOp).RetErr).To(BeEquivalentTo(-rgwcls.ErrBusyResharding))
		x := execStep(steps[2])
		Expect(x.Method).To(Equal(method))
		return x.In
	}
	// reshard commits a reshard of rec now: the instance moves to the next
	// index generation, whose shard of k takes the old shard's entries, and
	// the old shard is removed. It returns the oid of k's new shard.
	reshard := func() string {
		next := resharded(rec)
		seedShards(c, next)
		nextOID := indexShardOID(next, "k")
		Expect(nextOID).NotTo(Equal(oid))
		maps.Copy(c.Object(rookIndexPool, "", nextOID).Omap, c.Object(rookIndexPool, "", oid).Omap)
		seedRecordInstance(c, next)
		c.Remove(rookIndexPool, "", oid)
		return nextOID
	}

	Describe("linkOLH", func() {
		DescribeTable("sends bucket_link_olh with the head's tag whole, in a guarded op on the name's shard",
			func(ctx SpecContext, release denc.Release, deleteMarker bool) {
				open(ctx, release)
				want := rgwcls.LinkOLHOp{
					Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, OLHTag: tag, OpTag: "op-tag", ZonesTrace: trace(),
				}
				// A version's write links with no meta, no unmod_since and low
				// precision
				// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3371 at v19.2.7,
				// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3492 at v20.2.4);
				// a delete marker with its owner and mtime and the delete's
				// unmod_since
				// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5871-L5901 at v19.2.7,
				// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6511 at v20.2.4).
				var f driver.OLHLinkFields
				if deleteMarker {
					m := rgwcls.DirEntryMeta{Owner: "alice", OwnerDisplayName: "Alice", Mtime: indexMtime}
					unmod := time.Date(2026, 10, 2, 3, 4, 5, 600, time.UTC)
					f = driver.OLHLinkFields{DeleteMarker: true, Meta: m, UnmodSince: unmod, HighPrecision: true}
					want.DeleteMarker, want.Meta, want.UnmodSince, want.HighPrecisionTime = true, m, unmod, true
				} else {
					write(ctx, version("v1"), "e1")
				}
				Expect(idx.LinkOLH(ctx, head(tag), version("v1"), "op-tag", f)).To(Succeed())

				in := guarded(c.LastWrite(rookIndexPool, "", oid), "bucket_link_olh")
				Expect(in).To(Equal(encodeAt(want, release)), "rgw_rados.cc:8153-8165 at v19.2.7, :8899-8911 at v20.2.4; epoch 0 and log_op false in a single zone")
				Expect(execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_link_olh", rgwcls.DecodeLinkOLHOp)).To(Equal(want))

				e, err := idx.GetOLH(ctx, head(tag))
				Expect(err).NotTo(HaveOccurred())
				Expect(e.Key).To(Equal(rgwcls.ObjKey{Name: "k", Instance: "v1"}))
				Expect(e.DeleteMarker).To(Equal(deleteMarker))
				Expect(e.Epoch).To(BeEquivalentTo(2), "epoch 0 lets the class take the next, 2 for a name's first")
				Expect(e.Tag).To(Equal(tag))
			},
			Entry("a version on Squid", denc.Squid, false),
			Entry("a delete marker on Tentacle", denc.Tentacle, true),
		)

		It("sends the epoch its caller gives", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			Expect(idx.LinkOLH(ctx, head(tag), version("v1"), "op-tag", driver.OLHLinkFields{Epoch: 7})).To(Succeed())
			Expect(execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_link_olh", rgwcls.DecodeLinkOLHOp).OLHEpoch).To(BeEquivalentTo(7))
			e, err := idx.GetOLH(ctx, head(tag))
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Epoch).To(BeEquivalentTo(7), "start_modify takes a named epoch, cls_rgw.cc:1503-1517 at v19.2.7")
		})

		It("sends a name that starts with an underscore escaped, to the shard of its name", func(ctx SpecContext) {
			key := meta.ObjKey{Name: "_u", Instance: "v1"}
			uoid := indexShardOID(rec, "_u")
			Expect(uoid).NotTo(Equal(oid))
			write(ctx, key, "eu")
			u := s.NewOLHIndexForTest(rec, key)
			uhead := &op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: "_u"}, Exists: true, Attrs: map[string][]byte{meta.AttrOLHIDTag: []byte(tag)}}
			Expect(u.LinkOLH(ctx, uhead, key, "op-tag", driver.OLHLinkFields{})).To(Succeed())
			l := execIn(c.LastWrite(rookIndexPool, "", uoid), 2, "bucket_link_olh", rgwcls.DecodeLinkOLHOp)
			Expect(l.Key).To(Equal(rgwcls.ObjKey{Name: "__u", Instance: "v1"}), "get_index_key_name escapes it; get_hash_object hashes the name")
		})
	})

	DescribeTable("refuses a key of another name than the one whose shard it holds, sending nothing",
		func(ctx SpecContext, call func(ctx context.Context, other *op.ObjectState) error) {
			other := &op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: "other"}, Exists: true, Attrs: map[string][]byte{meta.AttrOLHIDTag: []byte(tag)}}
			Expect(call(ctx, other)).To(And(MatchError(op.ErrUnknown), MatchError(ContainSubstring(`given key "other"`))))
			for _, o := range []string{oid, indexShardOID(rec, "other")} {
				Expect(c.Writes(rookIndexPool, "", o)).To(BeZero(), "writes to %s", o)
				Expect(c.Reads(rookIndexPool, "", o)).To(BeZero(), "reads of %s", o)
			}
		},
		Entry("linkOLH", func(ctx context.Context, other *op.ObjectState) error {
			return idx.LinkOLH(ctx, head(tag), meta.ObjKey{Name: "other", Instance: "v1"}, "op-tag", driver.OLHLinkFields{})
		}),
		Entry("unlinkInstance", func(ctx context.Context, other *op.ObjectState) error {
			return idx.UnlinkInstance(ctx, head(tag), meta.ObjKey{Name: "other", Instance: "v1"}, "op-tag", 0, false)
		}),
		Entry("readLog", func(ctx context.Context, other *op.ObjectState) error {
			_, err := idx.ReadLog(ctx, other, 0)
			return err
		}),
		Entry("trimLog", func(ctx context.Context, other *op.ObjectState) error { return idx.TrimLog(ctx, other, 2) }),
		Entry("clear", func(ctx context.Context, other *op.ObjectState) error { return idx.Clear(ctx, other) }),
		Entry("getOLH", func(ctx context.Context, other *op.ObjectState) error {
			_, err := idx.GetOLH(ctx, other)
			return err
		}),
	)

	Describe("unlinkInstance", func() {
		DescribeTable("sends bucket_unlink_instance in a guarded op with VERSIONED_OP, and NULL_VERSION for the null version",
			func(ctx SpecContext, instance string, null bool, flags uint16) {
				write(ctx, version(instance), "e")
				link(ctx, version(instance), "link-tag")
				Expect(idx.UnlinkInstance(ctx, head(tag), version(instance), "unlink-tag", 0, null)).To(Succeed())

				want := rgwcls.UnlinkInstanceOp{
					Key: rgwcls.ObjKey{Name: "k", Instance: instance}, OpTag: "unlink-tag", BILogFlags: flags,
					OLHTag: tag, ZonesTrace: trace(),
				}
				in := guarded(c.LastWrite(rookIndexPool, "", oid), "bucket_unlink_instance")
				Expect(in).To(Equal(encodeAt(want, denc.Squid)), "rgw_rados.cc:8814-8818 and :8207-8216 at v19.2.7")
				Expect(execIn(c.LastWrite(rookIndexPool, "", oid), 2, "bucket_unlink_instance", rgwcls.DecodeUnlinkInstanceOp)).To(Equal(want))

				e, err := idx.GetOLH(ctx, head(tag))
				Expect(err).NotTo(HaveOccurred())
				Expect(e.PendingRemoval).To(BeTrue(), "the name's only version went")
			},
			Entry("a named version", "v1", false, rgwcls.BILogFlagVersionedOp),
			Entry("the null version", "", true, rgwcls.BILogFlagVersionedOp|rgwcls.BILogNullVersion),
		)
	})

	Describe("readLog", func() {
		It("reads the log after the marker with the empty instance and the head's tag, unguarded", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			link(ctx, version("v1"), "link-1")
			setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)

			ret, err := idx.ReadLog(ctx, head(tag), 0)
			Expect(err).NotTo(HaveOccurred(), "a read without the reshard guard is served while the shard reshards")
			Expect(ret.IsTruncated).To(BeFalse())
			Expect(ret.Log).To(HaveKeyWithValue(uint64(2), ConsistOf(rgwcls.OLHLogEntry{
				Epoch: 2, Op: rgwcls.OLHOpLinkOLH, OpTag: "link-1", Key: rgwcls.ObjKey{Name: "k", Instance: "v1"},
			})))
			steps := c.LastRead(rookIndexPool, "", oid).Steps()
			Expect(steps).To(HaveLen(1), "no assert_exists and no guard, rgw_rados.cc:8249-8256 at v19.2.7, :8995-9002 at v20.2.4")
			x := execStep(steps[0])
			Expect(x.Method).To(Equal("bucket_read_olh_log"))
			Expect(x.In).To(Equal(encodeAt(rgwcls.ReadOLHLogOp{OLH: rgwcls.ObjKey{Name: "k"}, OLHTag: tag}, denc.Squid)))

			ret, err = idx.ReadLog(ctx, head(tag), 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.Log).To(BeEmpty(), "the class answers the epochs above the marker")
			Expect(execStep(c.LastRead(rookIndexPool, "", oid).Steps()[0]).In).To(
				Equal(encodeAt(rgwcls.ReadOLHLogOp{OLH: rgwcls.ObjKey{Name: "k"}, VerMarker: 2, OLHTag: tag}, denc.Squid)))
		})

		It("passes on the class's ECANCELED for a head whose tag the OLH entry does not carry", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			link(ctx, version("v1"), "link-1")
			_, err := idx.ReadLog(ctx, head("another"), 0)
			Expect(err).To(MatchError(radosclient.ErrCanceled))
		})

		It("answers UnknownError for a reply that does not decode, as cls_rgw_get_olh_log's EIO", func(ctx SpecContext) {
			rgw := fakerados.RGWClass()
			c.RegisterClassMethods("rgw", func(call *fakerados.ClassCall) ([]byte, int32) {
				if call.Method == "bucket_read_olh_log" {
					return []byte{1, 1, 9, 0, 0, 0}, 0
				}
				return rgw(call)
			}, fakerados.RGWMethods, fakerados.RGWWriteMethods)
			_, err := idx.ReadLog(ctx, head(tag), 0)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(err).To(MatchError(ContainSubstring(oid)))
		})

		It("reads the log from the shard a reshard moved the name to", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			link(ctx, version("v1"), "link-1")
			nextOID := reshard()
			ret, err := idx.ReadLog(ctx, head(tag), 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.Log).To(HaveKey(uint64(2)))
			Expect(c.Reads(rookIndexPool, "", oid)).To(Equal(1), "the read the missing shard refused")
			Expect(c.Reads(rookIndexPool, "", nextOID)).To(Equal(1))
		})
	})

	Describe("trimLog and clear", func() {
		BeforeEach(func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			link(ctx, version("v1"), "link-1")
		})

		It("trims the log up to ver in a guarded op", func(ctx SpecContext) {
			Expect(idx.TrimLog(ctx, head(tag), 2)).To(Succeed())
			want := rgwcls.TrimOLHLogOp{OLH: rgwcls.ObjKey{Name: "k"}, Ver: 2, OLHTag: tag}
			Expect(guarded(c.LastWrite(rookIndexPool, "", oid), "bucket_trim_olh_log")).To(Equal(encodeAt(want, denc.Squid)),
				"rgw_rados.cc:8349-8356 at v19.2.7")
			e, err := idx.GetOLH(ctx, head(tag))
			Expect(err).NotTo(HaveOccurred())
			Expect(e.PendingLog).To(BeEmpty())
		})

		It("clears the name's OLH entry in a guarded op", func(ctx SpecContext) {
			Expect(idx.Clear(ctx, head(tag))).To(Succeed())
			want := rgwcls.ClearOLHOp{Key: rgwcls.ObjKey{Name: "k"}, OLHTag: tag}
			Expect(guarded(c.LastWrite(rookIndexPool, "", oid), "bucket_clear_olh")).To(Equal(encodeAt(want, denc.Squid)),
				"rgw_rados.cc:8380-8388 at v19.2.7")
			_, err := idx.GetOLH(ctx, head(tag))
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})
	})

	Describe("getOLH", func() {
		It("answers radosclient.ErrNotFound for a name without an OLH entry, once the bucket names the same shard", func(ctx SpecContext) {
			_, err := idx.GetOLH(ctx, head(""))
			Expect(err).To(MatchError(radosclient.ErrNotFound), "cls_cxx_map_get_val's ENOENT, cls_rgw.cc:2655-2659 at v19.2.7")
			Expect(rc.biGets(oid)).To(Equal([]rgwcls.BIGetOp{{Key: rgwcls.ObjKey{Name: "k"}, Type: rgwcls.BIIndexOLH}}))
			Expect(c.Reads(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())).To(Equal(1), "the bucket read again for its shard")
		})

		It("answers a re-tagged entry's tag and the pending log the link kept", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			link(ctx, version("v1"), "link-1")
			Expect(idx.UnlinkInstance(ctx, head(tag), version("v1"), "unlink-1", 0, false)).To(Succeed())
			write(ctx, version("v2"), "e2")
			Expect(idx.LinkOLH(ctx, head("newtag"), version("v2"), "link-2", driver.OLHLinkFields{})).To(Succeed())

			e, err := idx.GetOLH(ctx, head("newtag"))
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Tag).To(Equal("newtag"), "a link re-tags an entry pending removal, cls_rgw.cc:1768-1777 at v19.2.7")
			Expect(e.Key).To(Equal(rgwcls.ObjKey{Name: "k", Instance: "v2"}))
			opTags := map[uint64][]string{}
			for epoch, entries := range e.PendingLog {
				for _, le := range entries {
					opTags[epoch] = append(opTags[epoch], le.OpTag)
				}
			}
			Expect(opTags).To(Equal(map[uint64][]string{2: {"link-1"}, 3: {"unlink-1", "unlink-1"}, 4: {"link-2"}}),
				"the old tag's entries stay in the log")
		})

		It("finds a shard a reshard removed again and answers the entry on the new one, not not-found", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			link(ctx, version("v1"), "link-1")
			nextOID := reshard()
			e, err := idx.GetOLH(ctx, head(tag))
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Key).To(Equal(rgwcls.ObjKey{Name: "k", Instance: "v1"}))
			Expect(c.Reads(rookIndexPool, "", oid)).To(Equal(1), "the read the missing shard refused")
			Expect(rc.biGets(nextOID)).To(Equal([]rgwcls.BIGetOp{{Key: rgwcls.ObjKey{Name: "k"}, Type: rgwcls.BIIndexOLH}}))
		})

		It("answers ErrNotFound after ten reads when each re-read of the bucket names another shard that is gone too", func(ctx SpecContext) {
			gens := []*op.BucketRecord{rec}
			for range 10 {
				gens = append(gens, commitReshardBeforeRead(c, gens[len(gens)-1], "k"))
			}
			_, err := idx.GetOLH(ctx, head(tag))
			Expect(err).To(MatchError(radosclient.ErrNotFound), "within the ten reads a reshard's guard allows")
			for i, g := range gens[:10] {
				Expect(c.Reads(rookIndexPool, "", indexShardOID(g, "k"))).To(Equal(1), "generation %d's shard", i)
			}
			Expect(c.Reads(rookIndexPool, "", indexShardOID(gens[10], "k"))).To(BeZero(), "no eleventh read")
			Expect(c.Reads(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())).To(Equal(9), "no re-read after the tenth")
		})

		It("answers NoSuchKey when the bucket is gone by the time a removed shard reads it again", func(ctx SpecContext) {
			c.Remove(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())
			c.Remove(rookIndexPool, "", oid)
			_, err := idx.GetOLH(ctx, head(tag))
			Expect(err).To(MatchError(op.ErrNoSuchKey), "refreshIndexOp's answer for a bucket deleted meanwhile")
			Expect(err).NotTo(MatchError(radosclient.ErrNotFound), "never an absent OLH entry")
		})

		It("answers UnknownError for an OLH entry that does not decode, as bi_get_olh's EIO", func(ctx SpecContext) {
			c.Object(rookIndexPool, "", oid).Omap[rgwcls.OLHDataKey("k")] = []byte{1}
			_, err := idx.GetOLH(ctx, head(tag))
			Expect(err).To(MatchError(op.ErrUnknown), "rgw_rados.cc:9476-9482 at v19.2.7")
		})
	})

	Describe("the reshard guard", func() {
		It("waits out a busy shard and links on the shard the bucket, read again, names", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			setReshardStatus(ctx, c, oid, rgwcls.ReshardDone)
			next := nextGeneration(c, rec)
			nextOID := indexShardOID(next, "k")
			maps.Copy(c.Object(rookIndexPool, "", nextOID).Omap, c.Object(rookIndexPool, "", oid).Omap)
			before := c.Writes(rookIndexPool, "", oid)

			link(ctx, version("v1"), "link-1")
			Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(before+1), "the link the guard refused")
			Expect(c.Reads(rookIndexPool, "", oid)).To(Equal(1), "one poll, which reads the reshard as finished on Squid")
			Expect(c.Writes(rookIndexPool, "", nextOID)).To(Equal(1), "the link the new shard took")
			e, err := idx.GetOLH(ctx, head(tag))
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Key).To(Equal(rgwcls.ObjKey{Name: "k", Instance: "v1"}))
		})

		It("answers ErrBusyResharding after ten busy answers", func(ctx SpecContext) {
			s.SetReshardWaitForTest(0)
			setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
			Expect(idx.TrimLog(ctx, head(""), 0)).To(MatchError(radosclient.ErrBusyResharding))
			Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(1+10), "the staging write and ten trims")
			Expect(c.Reads(rookIndexPool, "", oid)).To(Equal(10*10), "ten polls after each")
		})

		It("finds a shard a reshard removed again from the bucket and sends the call there", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			next := commitReshardBefore(c, rec, "k")
			link(ctx, version("v1"), "link-1")
			Expect(c.Writes(rookIndexPool, "", indexShardOID(next, "k"))).To(Equal(1),
				"v19.2.7 retries a missing shard, rgw_rados.cc:7929-7933")
		})
	})

	Describe("completeSync", func() {
		meta5 := func() rgwcls.DirEntryMeta {
			return rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, Mtime: indexMtime, ETag: "e5", Owner: "alice"}
		}

		It("sends the ADD completion in a guarded op and returns once the class has applied it", func(ctx SpecContext) {
			x := s.NewIndexOpForTest(rec, version("v1"), "tag")
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 42}, meta5())).To(Succeed())

			Expect(s.PendingCompletionsForTest()).To(BeZero(), "not sent through the completion manager")
			en, _ := c.Entry(rookIndexPool, "", oid, rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k", Instance: "v1"}, false))
			Expect(en).To(And(HaveField("Exists", true), HaveField("Meta.ETag", "e5"), HaveField("PendingMap", BeEmpty())),
				"v1's instance entry on %s", oid)
			want := rgwcls.CompleteOp{
				Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, Ver: rgwcls.EntryVer{Pool: 7, Epoch: 42},
				Meta: meta5(), Tag: "tag", ZonesTrace: trace(),
			}
			Expect(guarded(c.LastWrite(rookIndexPool, "", oid), "bucket_complete_op")).To(Equal(encodeAt(want, denc.Squid)),
				"cls_obj_complete_op's request, rgw_rados.cc:9690-9709 at v19.2.7")
		})

		It("returns the class's error, and sends nothing in the background", func(ctx SpecContext) {
			x := s.NewIndexOpForTest(rec, version("v1"), "never-prepared")
			err := x.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 42}, meta5())
			Expect(err).To(MatchError(radosclient.ErrInvalid), "no pending entry under the tag, cls_rgw.cc:1066-1073 at v19.2.7")
			Expect(s.PendingCompletionsForTest()).To(BeZero())
			Expect(c.Writes(rookIndexPool, "", oid)).To(Equal(1))
		})

		It("returns the context's error when it has ended, and queues nothing", func(ctx SpecContext) {
			x := s.NewIndexOpForTest(rec, version("v1"), "tag")
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			ended, cancel := context.WithCancel(ctx)
			cancel()
			Expect(x.CompleteSync(ended, rgwcls.EntryVer{Pool: 7, Epoch: 42}, meta5())).To(MatchError(context.Canceled))
			Expect(s.PendingCompletionsForTest()).To(BeZero())
			en, _ := c.Entry(rookIndexPool, "", oid, rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k", Instance: "v1"}, false))
			Expect(en).To(And(HaveField("Exists", false), HaveField("PendingMap", HaveLen(1))), "the prepare's pending entry, never completed")
		})

		It("waits out a busy shard and completes on the shard the bucket, read again, names", func(ctx SpecContext) {
			x := s.NewIndexOpForTest(rec, version("v1"), "tag")
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			setReshardStatus(ctx, c, oid, rgwcls.ReshardDone)
			next := nextGeneration(c, rec)
			nextOID := indexShardOID(next, "k")
			maps.Copy(c.Object(rookIndexPool, "", nextOID).Omap, c.Object(rookIndexPool, "", oid).Omap)

			Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 42}, meta5())).To(Succeed())
			en, _ := c.Entry(rookIndexPool, "", nextOID, rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k", Instance: "v1"}, false))
			Expect(en).To(And(HaveField("Exists", true), HaveField("Meta.ETag", "e5")), "v1's instance entry on %s", nextOID)
			Expect(s.PendingCompletionsForTest()).To(BeZero())
		})

		It("returns ErrBusyResharding after ten busy answers rather than queueing the completion", func(ctx SpecContext) {
			x := s.NewIndexOpForTest(rec, version("v1"), "tag")
			Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
			s.SetReshardWaitForTest(0)
			setReshardStatus(ctx, c, oid, rgwcls.ReshardInProgress)
			Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 42}, meta5())).To(MatchError(radosclient.ErrBusyResharding))
			Expect(s.PendingCompletionsForTest()).To(BeZero())
		})

		DescribeTable("sends the op's bilog flags in its completion at both releases, and in its prepare on Squid only",
			func(ctx SpecContext, release denc.Release, prepareFlags uint16) {
				open(ctx, release)
				x := s.NewIndexOpForTest(rec, version("v1"), "tag")
				x.SetBILogFlagsForTest(rgwcls.BILogFlagVersionedOp)
				Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
				p := rgwcls.PrepareOp{Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, Tag: "tag", BILogFlags: prepareFlags}
				if release < denc.Tentacle {
					p.ZonesTrace = trace()
				}
				Expect(guarded(c.LastWrite(rookIndexPool, "", oid), "bucket_prepare_op")).To(Equal(encodeAt(p, release)),
					"rgw_rados.cc:9671 at v19.2.7, :10407 at v20.2.4")
				Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 42}, meta5())).To(Succeed())
				want := rgwcls.CompleteOp{
					Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, Ver: rgwcls.EntryVer{Pool: 7, Epoch: 42},
					Meta: meta5(), Tag: "tag", BILogFlags: rgwcls.BILogFlagVersionedOp, ZonesTrace: trace(),
				}
				Expect(guarded(c.LastWrite(rookIndexPool, "", oid), "bucket_complete_op")).To(Equal(encodeAt(want, release)),
					"rgw_rados.cc:9708-9709 at v19.2.7, :10444-10445 at v20.2.4")
			},
			Entry("on Squid", denc.Squid, rgwcls.BILogFlagVersionedOp),
			Entry("on Tentacle, whose prepare takes none", denc.Tentacle, uint16(0)),
		)

		It("does nothing for an indexless bucket", func(ctx SpecContext) {
			blind := testBucket("zone-ceph-objectstore.4156.3", 0)
			blind.Info.Layout.Current.Layout.Type = meta.IndexIndexless
			Expect(s.NewIndexOpForTest(blind, version("v1"), "tag").CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 1}, meta5())).To(Succeed())
			Expect(c.Writes(rookIndexPool, "", ".dir.zone-ceph-objectstore.4156.3")).To(BeZero(), "UpdateIndex::complete returns on blind")
		})
	})

	Describe("getInstanceEntry", func() {
		// nullGets are the reads that resolve the null version, in
		// read_key_entry's order: the plain key, the null delete marker's
		// entry and the null data entry.
		nullGets := func() (plain, dm, data rgwcls.BIGetOp) {
			k := rgwcls.ObjKey{Name: "k"}
			return rgwcls.BIGetOp{Key: k, Type: rgwcls.BIIndexInstance},
				rgwcls.BIGetOp{Key: rgwcls.ObjKey{Name: rgwcls.InstanceIndexKey(k, true)}, Type: rgwcls.BIIndexPlain},
				rgwcls.BIGetOp{Key: rgwcls.ObjKey{Name: rgwcls.InstanceIndexKey(k, false)}, Type: rgwcls.BIIndexPlain}
		}
		// converted gives k a null version from before versioning, then a
		// version v1, whose link converts the plain entry.
		converted := func(ctx context.Context) {
			GinkgoHelper()
			write(ctx, version(""), "null-data")
			write(ctx, version("v1"), "e1")
			link(ctx, version("v1"), "link-1")
			plain, _ := c.Entry(rookIndexPool, "", oid, "k")
			Expect(plain.Flags&rgwcls.FlagVerMarker).To(Equal(rgwcls.FlagVerMarker), "the plain key holds the version marker: %+v", plain)
		}
		get := func(ctx context.Context, key meta.ObjKey) (rgwcls.DirEntry, error) {
			rc.mu.Lock()
			delete(rc.reads, oid)
			rc.mu.Unlock()
			return s.GetInstanceEntryForTest(ctx, rec, key)
		}

		It("reads a named version with one bi_get of its instance entry", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			en, err := get(ctx, version("v1"))
			Expect(err).NotTo(HaveOccurred())
			Expect(en.Key).To(Equal(rgwcls.ObjKey{Name: "k", Instance: "v1"}))
			Expect(en.Meta.ETag).To(Equal("e1"))
			Expect(rc.biGets(oid)).To(Equal([]rgwcls.BIGetOp{{Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, Type: rgwcls.BIIndexInstance}}))
		})

		It("reads a plain entry that was never converted with one bi_get of the plain key", func(ctx SpecContext) {
			write(ctx, version(""), "plain")
			en, err := get(ctx, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(en.Meta.ETag).To(Equal("plain"))
			plainGet, _, _ := nullGets()
			Expect(rc.biGets(oid)).To(Equal([]rgwcls.BIGetOp{plainGet}))
		})

		It("reads a converted key's null data behind its version marker, after looking for a null delete marker", func(ctx SpecContext) {
			converted(ctx)
			en, err := get(ctx, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(en.Meta.ETag).To(Equal("null-data"), "read_key_entry, cls_rgw.cc:946-978 at v19.2.7")
			plainGet, dmGet, dataGet := nullGets()
			Expect(rc.biGets(oid)).To(Equal([]rgwcls.BIGetOp{plainGet, dmGet, dataGet}))
		})

		It("reads a converted key's null delete marker before its null data", func(ctx SpecContext) {
			converted(ctx)
			Expect(idx.LinkOLH(ctx, head(tag), version(""), "null-dm", driver.OLHLinkFields{
				DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: indexMtime},
			})).To(Succeed())
			en, err := get(ctx, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(en.Flags & rgwcls.FlagDeleteMarker).NotTo(BeZero())
			plainGet, dmGet, _ := nullGets()
			Expect(rc.biGets(oid)).To(Equal([]rgwcls.BIGetOp{plainGet, dmGet}))
		})

		It("answers radosclient.ErrNotFound for a converted key with neither", func(ctx SpecContext) {
			converted(ctx)
			delete(c.Object(rookIndexPool, "", oid).Omap, rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k"}, false))
			_, err := get(ctx, version(""))
			Expect(err).To(MatchError(radosclient.ErrNotFound), "the last read's ENOENT is the caller's")
			plainGet, dmGet, dataGet := nullGets()
			Expect(rc.biGets(oid)).To(Equal([]rgwcls.BIGetOp{plainGet, dmGet, dataGet}))
			Expect(c.Reads(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())).To(Equal(1), "the bucket read again, naming the same shard")
		})

		It("finds a shard a reshard removed again and reads a named version on the one the bucket now names", func(ctx SpecContext) {
			write(ctx, version("v1"), "e1")
			nextOID := reshard()
			en, err := get(ctx, version("v1"))
			Expect(err).NotTo(HaveOccurred(), "not the removed shard's ENOENT")
			Expect(en.Meta.ETag).To(Equal("e1"))
			want := []rgwcls.BIGetOp{{Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, Type: rgwcls.BIIndexInstance}}
			Expect(rc.biGets(oid)).To(Equal(want), "the read the removed shard refused")
			Expect(rc.biGets(nextOID)).To(Equal(want))
		})

		It("finds a shard a reshard removed again and resolves a converted key's null from the plain key on the new one", func(ctx SpecContext) {
			converted(ctx)
			nextOID := reshard()
			en, err := get(ctx, version(""))
			Expect(err).NotTo(HaveOccurred(), "not the removed shard's ENOENT")
			Expect(en.Meta.ETag).To(Equal("null-data"))
			plainGet, dmGet, dataGet := nullGets()
			Expect(rc.biGets(oid)).To(Equal([]rgwcls.BIGetOp{plainGet}), "the read the removed shard refused")
			Expect(rc.biGets(nextOID)).To(Equal([]rgwcls.BIGetOp{plainGet, dmGet, dataGet}), "read_key_entry's order again, from the plain key")
		})

		It("answers UnknownError for an instance entry that does not decode, as bi_get_instance's EIO", func(ctx SpecContext) {
			c.Object(rookIndexPool, "", oid).Omap[rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k", Instance: "v1"}, false)] = []byte{1}
			_, err := get(ctx, version("v1"))
			Expect(err).To(MatchError(op.ErrUnknown), "rgw_rados.cc:9454-9460 at v19.2.7")
		})
	})
})

// commitReshardBeforeRead makes a reshard of rec commit just before the next
// read of rec's shard of name, as commitReshardBefore does before a write: the
// next generation's shards are seeded, the shard of name takes the old
// shard's entries, the instance is stored, and the old shard is removed. It
// returns the resharded record.
func commitReshardBeforeRead(c *fakerados.Cluster, rec *op.BucketRecord, name string) *op.BucketRecord {
	next := resharded(rec)
	oid, nextOID := indexShardOID(rec, name), indexShardOID(next, name)
	c.BeforeRead(rookIndexPool, "", oid, func() {
		c.BeforeRead(rookIndexPool, "", oid, nil)
		seedShards(c, next)
		if old := c.Object(rookIndexPool, "", oid); old != nil {
			maps.Copy(c.Object(rookIndexPool, "", nextOID).Omap, old.Omap)
		}
		seedRecordInstance(c, next)
		c.Remove(rookIndexPool, "", oid)
	})
	return next
}
