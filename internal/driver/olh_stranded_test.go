package driver_test

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
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

// A versioned write that creates a key's OLH head, whose version a delete
// removes before its link, leaves the key a version marker and an OLH entry
// marked for removal, with no OLH head. An unversioned write of the key on a
// suspended bucket then prepares and completes its index entry through the
// marker, where no listing reads it.
var _ = Describe("an unversioned write of a key whose OLH a delete left pending removal", func() {
	const olhTag = "strandedstrandedstrandedstranded0"
	var (
		c      *fakerados.Cluster
		ac     *aroundCluster
		s      *driver.Store
		rec    *op.BucketRecord
		rel    denc.Release
		clock  atomic.Int64
		shard  string
		olhOID string
		attrs  map[string][]byte
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	plainKey := func() rgwcls.ObjKey { return rgwcls.ObjKey{Name: "k"} }
	// fresh is the fixture at release r: an empty cluster and a bucket whose
	// versioning is suspended.
	fresh := func(ctx context.Context, r denc.Release) {
		GinkgoHelper()
		rel = r
		clock.Store(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).UnixNano())
		c = newPutCluster()
		c.SetClock(now)
		ac = &aroundCluster{Cluster: c}
		var err error
		s, err = driver.Open(ctx, ac, conf(nil), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned | meta.BucketVersionsSuspended
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
		olhOID = olhHeadOID(version(""))
		attrs = map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
	}
	AfterEach(func() {
		if s != nil {
			ac.set(nil)
			settle(s)
		}
	})

	put := func(ctx context.Context, instance, body string, p op.PutParams) (*op.PutResult, error) {
		if p.Attrs == nil {
			p.Attrs = attrs
		}
		p.Size = int64(len(body))
		if p.Tag == "" {
			p.Tag = "tag-" + instance
		}
		return s.PutObject(ctx, rec, version(instance), strings.NewReader(body), p)
	}
	// putOne writes the key's first body, "one", and putTwo its second,
	// "two", over whatever the key holds.
	putOne := func(ctx context.Context) {
		GinkgoHelper()
		_, err := put(ctx, "", "one", op.PutParams{Tag: "tag-first"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
	}
	putTwo := func(ctx context.Context, p op.PutParams) error {
		p.Tag = "tag-null"
		_, err := put(ctx, "", "two", p)
		return err
	}
	omap := func() map[string][]byte { return c.Object(rookIndexPool, "", shard).Omap }
	olhEntry := func() (rgwcls.OLHEntry, bool) { return c.OLHEntry(rookIndexPool, "", shard, "k") }
	clears := func() int {
		n := 0
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			if callsMethod(w.Steps(), "bucket_clear_olh") {
				n++
			}
		}
		return n
	}
	// clearCount is the clears the cluster answered, by what it answered.
	// clears counts a refused one too, as the cluster records the writes it
	// refuses.
	type clearCount struct{ landed, refused int }
	// countClears counts the clears the cluster answers from now on.
	countClears := func() *clearCount {
		n := &clearCount{}
		ac.set(func(o *radosclient.WriteOp, send sendFunc) (uint64, error) {
			if !callsMethod(o.Steps(), "bucket_clear_olh") {
				return send()
			}
			v, err := send()
			if err != nil {
				n.refused++
			} else {
				n.landed++
			}
			return v, err
		})
		return n
	}
	// failShardRead fails the n-th read of the key's index shard from now on
	// with EIO.
	failShardRead := func(n int) {
		reads := 0
		c.BeforeRead(rookIndexPool, "", shard, func() {
			reads++
			if reads == n {
				c.FailNextRead(rookIndexPool, "", shard, syscall.EIO)
			}
		})
	}
	// indexReads is the reads of every shard of the bucket's index.
	indexReads := func() int {
		n := 0
		gen := rec.Info.Layout.Current
		for i := range max(gen.Layout.Normal.NumShards, 1) {
			n += c.Reads(rookIndexPool, "", rec.Info.IndexShardOID(gen, i))
		}
		return n
	}
	// listed is what ListObjects, or with versions ListObjectVersions, shows.
	listed := func(ctx context.Context, versions bool) []string {
		GinkgoHelper()
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{ListVersions: versions, MaxKeys: 1000})
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, e := range res.Entries {
			name := e.Key.Name
			if versions {
				name += "/" + cmp.Or(e.Key.Instance, meta.NullInstance)
			}
			out = append(out, name)
		}
		return out
	}
	// bodyOf is what a read of key returns.
	bodyOf := func(ctx context.Context, key meta.ObjKey) string {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue(), "%v reads back", key)
		var buf bytes.Buffer
		Expect(s.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &buf)).To(Succeed())
		return buf.String()
	}
	// body is what a read of the key's current version returns.
	body := func(ctx context.Context) string {
		GinkgoHelper()
		return bodyOf(ctx, version(""))
	}
	// loggedRemoval reports whether the OLH entry's pending log holds the
	// removal of instance.
	loggedRemoval := func(e rgwcls.OLHEntry, instance string) bool {
		for _, entries := range e.PendingLog {
			for _, le := range entries {
				if le.Op == rgwcls.OLHOpRemoveInstance && le.Key.Instance == instance {
					return true
				}
			}
		}
		return false
	}

	// seedStranded is the stranded state with its index entries written whole: the
	// version marker, and an OLH entry marked for removal that holds no
	// version, under tag olhTag, its log holding log's entries.
	seedStranded := func(log map[uint64][]rgwcls.OLHLogEntry) {
		GinkgoHelper()
		omap()["k"] = encode(rgwcls.DirEntry{Key: plainKey(), Flags: rgwcls.FlagVerMarker, Ver: rgwcls.NewEntryVer()})
		omap()[rgwcls.OLHDataKey("k")] = encode(rgwcls.OLHEntry{
			Key: plainKey(), Epoch: 2, PendingLog: log, Tag: olhTag, PendingRemoval: true,
		})
	}

	// strandedByRGWGo is the state rgw-go's own writer and delete leave: a
	// PutObject of version X, a first version, and a DeleteObject of X after
	// the write's index completion and its init.
	strandedByRGWGo := func(ctx context.Context) {
		GinkgoHelper()
		afterOLHInit(c, olhOID, func() {
			_, err := s.DeleteObject(ctx, rec, version("X"), op.DeleteParams{})
			Expect(err).NotTo(HaveOccurred(), "the delete of X, completed and not yet linked")
		})
		_, err := put(ctx, "X", "one", op.PutParams{})
		Expect(err).NotTo(HaveOccurred(), "the write whose version a delete took answers success")
		settle(s)
	}
	// strandedByRadosgw is the state a radosgw writer leaves: its index
	// completion and its init, a delete of X whose replay cannot clear the OLH
	// past the writer's pending attr, the writer's link, which the class
	// answers ENOENT, and set_olh's cancel and best-effort update.
	strandedByRadosgw := func(ctx context.Context) {
		GinkgoHelper()
		w := newRGWWriter(c, s, rec, rel, "X")
		w.Prepare(ctx)
		w.HeadWrite(ctx)
		w.Complete(ctx)
		w.ReadHead(ctx)
		w.InitRetrying(ctx)
		_, err := s.DeleteObject(ctx, rec, version("X"), op.DeleteParams{})
		Expect(err).NotTo(HaveOccurred(), "the delete of X, completed and not yet linked")
		l := rgwcls.LinkOLHOp{
			Key: rgwcls.ObjKey{Name: "k", Instance: "X"}, OLHTag: string(w.head.Attrs[meta.AttrOLHIDTag]),
			OpTag: w.opTag, ZonesTrace: w.zonesTrace(),
		}
		Expect(w.shardWrite(ctx, func(op *radosclient.WriteOp) { rgwcls.LinkOLH(op, l, rel) })).To(MatchError(radosclient.ErrNotFound), "the class's answer for a version whose entry is gone")
		s.OLHCancelModificationForTest(ctx, w.head, w.opTag)
		if err := s.UpdateOLHForTest(ctx, rec, w.head); err != nil {
			Expect(err).To(MatchError(radosclient.ErrCanceled), "the best-effort update finds no head")
		}
		settle(s)
	}
	// expectStranded is the state both writers leave: the index holds the
	// version marker and an OLH entry marked for removal that holds no version
	// and logs X's removal, and nothing else of the key: no OLH head, no
	// instance entry.
	expectStranded := func() {
		GinkgoHelper()
		e, ok := olhEntry()
		Expect(ok).To(BeTrue(), "the OLH entry")
		Expect([]any{e.Exists, e.PendingRemoval}).To(Equal([]any{false, true}), "marked for removal, holding no version")
		Expect(loggedRemoval(e, "X")).To(BeTrue(), "the log still names X's removal")
		plain, ok := c.Entry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue(), "the plain entry")
		Expect(plain.Flags&rgwcls.FlagVerMarker).NotTo(BeZero(), "a version marker")
		Expect(c.Object(testDataPool, "", olhOID)).To(BeNil(), "no OLH head")
		_, ok = c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "X"})
		Expect(ok).To(BeFalse(), "X's instance entry")
		_, ok = c.InstanceEntry(rookIndexPool, "", shard, plainKey())
		Expect(ok).To(BeFalse(), "the null data entry")
	}
	// expectListedAsNewKey is the key after a write that is listed: the
	// shard holds the plain entry of the key alone, as it does for a key never
	// versioned, and every listing shows it.
	expectListedAsNewKey := func(ctx context.Context, want string) {
		GinkgoHelper()
		Expect(slices.Sorted(maps.Keys(omap()))).To(Equal([]string{"k"}), "the shard holds the key's plain entry alone")
		plain, ok := c.Entry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue())
		Expect([]any{plain.Flags, plain.Exists, len(plain.PendingMap)}).To(Equal([]any{uint16(0), true, 0}), "a plain entry, complete")
		Expect(listed(ctx, false)).To(Equal([]string{"k"}), "ListObjects")
		Expect(listed(ctx, true)).To(Equal([]string{"k/null"}), "ListObjectVersions")
		Expect(body(ctx)).To(Equal(want))
		st, err := s.BucketStats(ctx, rec)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{st.NumObjects, st.Size}).To(Equal([]any{uint64(1), uint64(len(want))}), "the bucket's stats count it once")
	}

	DescribeTable("lists the key after the write, which clears what the delete left",
		func(ctx SpecContext, r denc.Release, strand func(context.Context), ifNoneMatch string) {
			fresh(ctx, r)
			strand(ctx)
			expectStranded()

			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null", IfNoneMatch: ifNoneMatch})
			Expect(err).NotTo(HaveOccurred())
			settle(s)

			Expect(clears()).To(Equal(1), "one bucket_clear_olh, sent before the write's prepare")
			_, ok := olhEntry()
			Expect(ok).To(BeFalse(), "the OLH entry is cleared with the marker")
			expectListedAsNewKey(ctx, "two")
		},
		Entry("rgw-go's state, Squid", denc.Squid, strandedByRGWGo, ""),
		Entry("rgw-go's state, Tentacle", denc.Tentacle, strandedByRGWGo, ""),
		Entry("radosgw's state, Squid", denc.Squid, strandedByRadosgw, ""),
		Entry("radosgw's state, Tentacle", denc.Tentacle, strandedByRadosgw, ""),
		Entry("a write that holds If-None-Match: *, rgw-go's state", denc.Squid, strandedByRGWGo, "*"),
		Entry("a write that holds If-None-Match: *, radosgw's state", denc.Tentacle, strandedByRadosgw, "*"),
	)

	It("clears before it prepares the write's index entry, which the class would write behind the marker", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		strandedByRGWGo(ctx)
		var sent []string
		ac.set(func(o *radosclient.WriteOp, send sendFunc) (uint64, error) {
			switch {
			case callsMethod(o.Steps(), "bucket_clear_olh"):
				sent = append(sent, "clear")
			case callsMethod(o.Steps(), "bucket_prepare_op"):
				sent = append(sent, "prepare")
			case writesData(o):
				sent = append(sent, "head")
			}
			return send()
		})
		_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
		Expect(err).NotTo(HaveOccurred())
		Expect(sent).To(Equal([]string{"clear", "prepare", "head"}))
	})

	It("lists a CompleteMultipartUpload's object the same way", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		strandedByRGWGo(ctx)
		up, err := s.CreateUpload(ctx, rec, version(""), op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		part, err := s.PutPart(ctx, up, 1, strings.NewReader("parted"), op.PutParams{Attrs: map[string][]byte{meta.AttrACL: attrs[meta.AttrACL]}, Size: 6})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		u := *up
		u.Key, u.WriteTag = version(""), "tx-complete"
		_, err = s.Complete(ctx, &u, []op.CompletePart{{Number: 1, ETag: `"` + part.ETag + `"`}})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(clears()).To(Equal(1))
		Expect(listed(ctx, false)).To(Equal([]string{"k"}))
		Expect(listed(ctx, true)).To(Equal([]string{"k/null"}))
		Expect(body(ctx)).To(Equal("parted"))
	})

	It("lists a CopyObject's destination the same way", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "src"}, strings.NewReader("copied"), op.PutParams{Attrs: attrs, Size: 6, Tag: "tag-src"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		strandedByRGWGo(ctx)
		from, err := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: "src"})
		Expect(err).NotTo(HaveOccurred())
		_, err = s.CopyObject(ctx, from, rec, version(""), op.CopyParams{Attrs: attrs, Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(clears()).To(Equal(1))
		Expect(listed(ctx, false)).To(Equal([]string{"k", "src"}))
		Expect(listed(ctx, true)).To(Equal([]string{"k/null", "src/null"}))
		Expect(body(ctx)).To(Equal("copied"))
	})

	// What a write meets besides the stranded state is left as it is: no
	// bucket_clear_olh is sent.
	Describe("leaves the key as radosgw's write would", func() {
		DescribeTable("when the index is not in the stranded state",
			func(ctx SpecContext, seed func(), r denc.Release) {
				fresh(ctx, r)
				seed()
				entry, had := olhEntry()
				_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(clears()).To(BeZero(), "no clear is sent")
				after, has := olhEntry()
				Expect(has).To(Equal(had), "the OLH entry stays")
				if had {
					Expect(after.Tag).To(Equal(entry.Tag))
				}
			},
			Entry("a version marker with no OLH entry", func() {
				omap()["k"] = encode(rgwcls.DirEntry{Key: plainKey(), Flags: rgwcls.FlagVerMarker, Ver: rgwcls.NewEntryVer()})
			}, denc.Squid),
			Entry("an OLH entry that holds a version", func() {
				omap()["k"] = encode(rgwcls.DirEntry{Key: plainKey(), Flags: rgwcls.FlagVerMarker, Ver: rgwcls.NewEntryVer()})
				omap()[rgwcls.OLHDataKey("k")] = encode(rgwcls.OLHEntry{
					Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, Epoch: 2, Tag: olhTag, Exists: true,
				})
			}, denc.Squid),
			Entry("an OLH entry that holds a version, whatever else it records", func() {
				omap()["k"] = encode(rgwcls.DirEntry{Key: plainKey(), Flags: rgwcls.FlagVerMarker, Ver: rgwcls.NewEntryVer()})
				omap()[rgwcls.OLHDataKey("k")] = encode(rgwcls.OLHEntry{
					Key: rgwcls.ObjKey{Name: "k", Instance: "v1"}, Epoch: 2, Tag: olhTag, Exists: true, PendingRemoval: true,
				})
			}, denc.Squid),
			Entry("an OLH entry that holds no version and is not marked for removal", func() {
				omap()["k"] = encode(rgwcls.DirEntry{Key: plainKey(), Flags: rgwcls.FlagVerMarker, Ver: rgwcls.NewEntryVer()})
				omap()[rgwcls.OLHDataKey("k")] = encode(rgwcls.OLHEntry{Key: plainKey(), Epoch: 2, Tag: olhTag})
			}, denc.Squid),
			Entry("an OLH entry marked for removal behind a plain entry that is no version marker", func() {
				omap()["k"] = encode(rgwcls.DirEntry{Key: plainKey(), Ver: rgwcls.NewEntryVer()})
				omap()[rgwcls.OLHDataKey("k")] = encode(rgwcls.OLHEntry{Key: plainKey(), Epoch: 2, Tag: olhTag, PendingRemoval: true})
			}, denc.Squid),
		)

		// The same state with a writer that is still at it, the key's head
		// stored, or with a version no replay has removed, is not one a
		// clear may drop.
		DescribeTable("when the stranded OLH is not the whole of what the key holds behind its marker",
			func(ctx SpecContext, damage func(ctx context.Context, w *rgwWriter), r denc.Release) {
				fresh(ctx, r)
				strandedByRGWGo(ctx)
				expectStranded()
				// A radosgw unversioned write of the key, as it lands behind the
				// marker: its prepare, its head and its completion.
				w := newRGWWriter(c, s, rec, rel, "")
				damage(ctx, w)
				entry, _ := olhEntry()
				_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(clears()).To(BeZero(), "no clear is sent")
				after, has := olhEntry()
				Expect(has).To(BeTrue(), "the OLH entry stays")
				Expect(after.Tag).To(Equal(entry.Tag))
			},
			Entry("a plain head and the entry behind the marker an earlier write left", func(ctx context.Context, w *rgwWriter) {
				w.Prepare(ctx)
				w.HeadWrite(ctx)
				w.Complete(ctx)
			}, denc.Squid),
			Entry("a plain head alone, its entry gone", func(ctx context.Context, w *rgwWriter) {
				w.HeadWrite(ctx)
			}, denc.Tentacle),
			Entry("the entry behind the marker alone, its head gone", func(ctx context.Context, w *rgwWriter) {
				w.Prepare(ctx)
				w.HeadWrite(ctx)
				w.Complete(ctx)
				c.Remove(testDataPool, "", olhOID)
			}, denc.Squid),
			Entry("a pending entry behind the marker a write has prepared and not completed", func(ctx context.Context, w *rgwWriter) {
				w.Prepare(ctx)
			}, denc.Tentacle),
			Entry("an index entry of the version the log names for removal, which no replay removed", func(ctx context.Context, _ *rgwWriter) {
				x := newRGWWriter(c, s, rec, rel, "X")
				x.Prepare(ctx)
				x.HeadWrite(ctx)
				x.Complete(ctx)
			}, denc.Squid),
		)

		It("keeps a head that stands, an OLH head a writer has made included, and links the write over it", func(ctx SpecContext) {
			fresh(ctx, denc.Squid)
			strandedByRGWGo(ctx)
			w := newRGWWriter(c, s, rec, rel, "Y")
			w.ReadHead(ctx)
			w.InitRetrying(ctx)
			Expect(c.Object(testDataPool, "", olhOID)).NotTo(BeNil(), "another writer's init made the head")
			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(clears()).To(BeZero(), "a write over an OLH head is a versioned op, whose link takes the entry over")
			e, ok := olhEntry()
			Expect(ok).To(BeTrue())
			Expect([]any{e.Exists, e.PendingRemoval, e.Key}).To(Equal([]any{true, false, rgwcls.ObjKey{Name: "k"}}), "the entry names the null version")
		})

		// Only the versions a log names for removal are looked up, each a
		// read; the log itself comes with the entry, whatever its length.
		It("clears an entry whose log is long but names no removal", func(ctx SpecContext) {
			fresh(ctx, denc.Squid)
			log := map[uint64][]rgwcls.OLHLogEntry{}
			for i := range 4*1000 + 1 {
				epoch := uint64(i + 2)
				log[epoch] = []rgwcls.OLHLogEntry{{
					Epoch: epoch, Op: rgwcls.OLHOpLinkOLH, OpTag: fmt.Sprintf("op%06d", epoch),
					Key: rgwcls.ObjKey{Name: "k", Instance: fmt.Sprintf("v%06d", epoch)},
				}}
			}
			seedStranded(log)
			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(clears()).To(Equal(1))
			expectListedAsNewKey(ctx, "two")
		})

		It("looks up no more than a bounded number of the versions the log names", func(ctx SpecContext) {
			fresh(ctx, denc.Squid)
			removals := func(n int) map[uint64][]rgwcls.OLHLogEntry {
				log := map[uint64][]rgwcls.OLHLogEntry{}
				for i := range n {
					epoch := uint64(i + 2)
					log[epoch] = []rgwcls.OLHLogEntry{{
						Epoch: epoch, Op: rgwcls.OLHOpRemoveInstance, OpTag: fmt.Sprintf("op%06d", epoch),
						Key: rgwcls.ObjKey{Name: "k", Instance: fmt.Sprintf("v%06d", epoch)},
					}}
				}
				return log
			}
			seedStranded(removals(17))
			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(clears()).To(BeZero(), "a log of more than 16 versions is left")

			fresh(ctx, denc.Squid)
			seedStranded(removals(16))
			_, err = put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(clears()).To(Equal(1), "a log of 16 versions, all gone, is cleared")
			expectListedAsNewKey(ctx, "two")
		})
	})

	DescribeTable("writes a key that was never stranded as before, sending no clear",
		func(ctx SpecContext, setup func(context.Context), want []string) {
			fresh(ctx, denc.Squid)
			setup(ctx)
			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(clears()).To(BeZero())
			Expect(listed(ctx, true)).To(Equal(want))
			Expect(body(ctx)).To(Equal("two"))
		},
		Entry("a key with no history", func(context.Context) {}, []string{"k/null"}),
		Entry("a key written before, never versioned", func(ctx context.Context) {
			_, err := put(ctx, "", "one", op.PutParams{Tag: "tag-first"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
		}, []string{"k/null"}),
		Entry("a key with a version, whose OLH holds it", func(ctx context.Context) {
			rec.Info.Flags = meta.BucketVersioned
			_, err := put(ctx, "v1", "one", op.PutParams{})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			rec.Info.Flags |= meta.BucketVersionsSuspended
		}, []string{"k/null", "k/v1"}),
	)

	// The class refuses the clear with ECANCELED when the entry it compares
	// has changed, or a writer has taken it over; each attempt reads the key
	// again, as set_olh's loop does.
	It("reads the key again when the class refuses the clear with ECANCELED, and lists it", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		strandedByRGWGo(ctx)
		ac.set(onFirst("bucket_clear_olh", failWith(syscall.ECANCELED)))
		_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(clears()).To(Equal(1), "the second attempt's clear is the one sent")
		expectListedAsNewKey(ctx, "two")
	})

	It("fails the write, with nothing written, when every attempt's clear is refused", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		strandedByRGWGo(ctx)
		before := c.Writes(rookIndexPool, "", shard)
		attempts := 0
		ac.set(func(o *radosclient.WriteOp, send sendFunc) (uint64, error) {
			if callsMethod(o.Steps(), "bucket_clear_olh") {
				attempts++
				return 0, &radosclient.Error{Errno: int32(syscall.ECANCELED), Op: "write"}
			}
			return send()
		})
		_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(err).To(MatchError(ContainSubstring("attempts lost to another writer")))
		Expect(attempts).To(Equal(driver.OLHAttempts), "one compared clear an attempt, as set_olh's loop makes")
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(before), "no index write landed")
		Expect(c.Object(testDataPool, "", olhOID)).To(BeNil(), "no head was written")
		expectStranded()
	})

	// What changes between the reads and the clear is compared in the clear's
	// op, so the clear is refused and the next attempt reads it.
	Describe("when the key changes between its reads and its clear", func() {
		It("keeps the removal a delete of another version logs under the entry's tag", func(ctx SpecContext) {
			fresh(ctx, denc.Squid)
			strandedByRGWGo(ctx)
			y := newRGWWriter(c, s, rec, rel, "Y")
			c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
				c.BeforeWrite(rookIndexPool, "", shard, nil)
				// A second version in flight, whose delete's unlink logs its removal
				// without changing the entry's tag.
				y.Prepare(ctx)
				y.HeadWrite(ctx)
				y.Complete(ctx)
				e, ok := olhEntry()
				Expect(ok).To(BeTrue())
				e.Epoch = 3
				e.PendingLog[3] = []rgwcls.OLHLogEntry{{Epoch: 3, Op: rgwcls.OLHOpRemoveInstance, OpTag: "delete-of-y", Key: rgwcls.ObjKey{Name: "k", Instance: "Y"}}}
				omap()[rgwcls.OLHDataKey("k")] = encode(e)
			})
			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			e, ok := olhEntry()
			Expect(ok).To(BeTrue(), "the OLH entry stays")
			Expect(loggedRemoval(e, "Y")).To(BeTrue(), "with the removal logged since it was read")
			_, ok = c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "Y"})
			Expect(ok).To(BeTrue(), "and the version it names")
		})

		It("keeps the entry when a write prepares its own behind the marker", func(ctx SpecContext) {
			fresh(ctx, denc.Squid)
			strandedByRGWGo(ctx)
			w := newRGWWriter(c, s, rec, rel, "")
			c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
				c.BeforeWrite(rookIndexPool, "", shard, nil)
				w.Prepare(ctx)
			})
			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			_, ok := olhEntry()
			Expect(ok).To(BeTrue(), "the OLH entry stays")
			plain, ok := c.Entry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeTrue())
			Expect(plain.Flags&rgwcls.FlagVerMarker).NotTo(BeZero(), "and the marker the other write prepared behind")
		})

		// The head is no part of the clear's compare, so another writer's
		// init after the head read leaves the clear to land with that head
		// standing. The write's head write then meets EEXIST, and its second
		// pass, reading an OLH head, makes it a versioned op whose link of the
		// null version takes the key over under the new head's tag, as the
		// other writer's link then does for a key with one version.
		DescribeTable("links the null version under the head another writer's init made after the head read",
			func(ctx SpecContext, r denc.Release) {
				fresh(ctx, r)
				strandedByRGWGo(ctx)
				z := newRGWWriter(c, s, rec, rel, "Z")
				z.Prepare(ctx)
				z.HeadWrite(ctx)
				z.Complete(ctx)
				n := countClears()
				c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
					c.BeforeWrite(rookIndexPool, "", shard, nil)
					z.ReadHead(ctx)
					z.InitRetrying(ctx)
					Expect(c.Object(testDataPool, "", olhOID)).NotTo(BeNil(), "Z's head stands when the clear is sent")
				})
				_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(*n).To(Equal(clearCount{landed: 1}), "the clear landed")
				e, ok := olhEntry()
				Expect(ok).To(BeTrue(), "the OLH entry the null link made")
				zHead := c.Object(testDataPool, "", olhOID)
				Expect([]any{e.Exists, e.PendingRemoval, e.Key, e.Tag}).To(Equal(
					[]any{true, false, plainKey(), string(zHead.Xattrs[meta.AttrOLHIDTag])}), "linked under Z's head tag")
				z.Link(ctx)
				z.ReadLog(ctx)
				z.Apply(ctx)
				z.Trim(ctx)
				settle(s)
				e, ok = olhEntry()
				Expect(ok).To(BeTrue())
				Expect([]any{e.Exists, e.PendingRemoval, e.Key.Instance}).To(Equal([]any{true, false, "Z"}), "Z is current")
				Expect(listed(ctx, true)).To(ConsistOf("k/Z", "k/null"))
				Expect(body(ctx)).To(Equal("data-Z"))
				Expect(bodyOf(ctx, version(meta.NullInstance))).To(Equal("two"))
			},
			Entry("Squid", denc.Squid),
			Entry("Tentacle", denc.Tentacle),
		)

		// A null delete marker is a link, which takes a pending-removal entry
		// over, and an unversioned delete makes one on a suspended bucket. The
		// clear it overtakes is refused; the next attempt finds the key no
		// longer stranded and sends none, and the write becomes a null link
		// over the marker's OLH head.
		DescribeTable("sends no clear when an unversioned delete links its null marker after the reads",
			func(ctx SpecContext, r denc.Release) {
				fresh(ctx, r)
				strandedByRGWGo(ctx)
				n := countClears()
				var delErr error
				c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
					c.BeforeWrite(rookIndexPool, "", shard, nil)
					_, delErr = s.DeleteObject(ctx, rec, version(""), op.DeleteParams{})
					settle(s)
				})
				_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
				Expect(err).NotTo(HaveOccurred())
				Expect(delErr).NotTo(HaveOccurred(), "the delete")
				settle(s)
				Expect(*n).To(Equal(clearCount{refused: 1}), "one clear, refused; none after")
				e, ok := olhEntry()
				Expect(ok).To(BeTrue())
				Expect([]any{e.Exists, e.PendingRemoval, e.DeleteMarker, e.Key}).To(Equal(
					[]any{true, false, false, plainKey()}), "the null version, current")
				Expect(listed(ctx, false)).To(Equal([]string{"k"}))
				Expect(listed(ctx, true)).To(Equal([]string{"k/null"}))
				Expect(body(ctx)).To(Equal("two"))
			},
			Entry("Squid", denc.Squid),
			Entry("Tentacle", denc.Tentacle),
		)
	})

	// An entry that does not decode is a state radosgw's writes ignore, and
	// failing every write to the key would not mend it.
	It("goes on as radosgw's write does when the OLH entry does not decode", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		omap()["k"] = encode(rgwcls.DirEntry{Key: plainKey(), Flags: rgwcls.FlagVerMarker, Ver: rgwcls.NewEntryVer()})
		omap()[rgwcls.OLHDataKey("k")] = []byte{1}
		_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(clears()).To(BeZero())
		Expect(omap()[rgwcls.OLHDataKey("k")]).To(Equal([]byte{1}), "the entry is left as it was")
	})

	// cutReplies makes the class answer every bi_get of typ with a reply cut
	// short, which does not decode.
	cutReplies := func(typ rgwcls.BIIndexType) {
		inner := fakerados.RGWClass()
		c.RegisterClassMethods("rgw", func(call *fakerados.ClassCall) ([]byte, int32) {
			out, rval := inner(call)
			if call.Method == "bi_get" && rval == 0 && rgwcls.DecodeBIGetOp(denc.NewDecoder(call.In)).Type == typ {
				return out[:1], 0
			}
			return out, rval
		}, fakerados.RGWMethods, fakerados.RGWWriteMethods)
	}

	// Each place the check decodes what a read answers: the reply to the read
	// of the OLH entry, the reply to the read of the plain entry, and the
	// entry of a version the log names. The entry of the OLH itself is the
	// spec above. Each failure is a stored entry's, so the key is not stranded
	// and the write goes on with nothing cleared and nothing changed.
	DescribeTable("goes on as radosgw's write does when a reply or an entry the check reads does not decode",
		func(ctx SpecContext, arm func()) {
			fresh(ctx, denc.Squid)
			strandedByRGWGo(ctx)
			expectStranded()
			arm()
			marker, olh := slices.Clone(omap()["k"]), slices.Clone(omap()[rgwcls.OLHDataKey("k")])
			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred(), "a stored entry's failure is not the write's")
			settle(s)
			Expect(clears()).To(BeZero(), "nothing cleared")
			Expect(omap()["k"]).To(Equal(marker), "the version marker is left")
			Expect(omap()[rgwcls.OLHDataKey("k")]).To(Equal(olh), "the OLH entry is left")
		},
		Entry("the reply to the read of the OLH entry", func() { cutReplies(rgwcls.BIIndexOLH) }),
		Entry("the reply to the read of the plain entry", func() { cutReplies(rgwcls.BIIndexInstance) }),
		Entry("the entry of a version the log names", func() {
			omap()[rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k", Instance: "X"}, false)] = []byte{1}
		}),
	)

	// A read the check makes fails the write, with nothing written: the
	// write's next op is a prepare on the same shard or a head write.
	DescribeTable("fails the write, with nothing written, when a read of the check fails, and lists on a retry",
		func(ctx SpecContext, arm func()) {
			fresh(ctx, denc.Squid)
			strandedByRGWGo(ctx)
			before := c.Writes(rookIndexPool, "", shard)
			arm()
			_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			var re *radosclient.Error
			Expect(errors.As(err, &re)).To(BeTrue(), "the read's own failure is the write's: %v", err)
			Expect(re.Errno).To(Equal(int32(syscall.EIO)))
			Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(before), "no index write landed")
			Expect(c.Object(testDataPool, "", olhOID)).To(BeNil(), "no head was written")
			expectStranded()

			_, err = put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			expectListedAsNewKey(ctx, "two")
		},
		// The reads of the shard in order: the plain entry, the OLH entry, the
		// null version's plain entry and data entry, the entry of X.
		Entry("the plain entry", func() { failShardRead(1) }),
		Entry("the OLH entry", func() { failShardRead(2) }),
		Entry("the key's head", func() { c.FailNextRead(testDataPool, "", olhOID, syscall.EIO) }),
		Entry("the null version's plain entry", func() { failShardRead(3) }),
		Entry("the null version's data entry", func() { failShardRead(4) }),
		Entry("the entry of a version the log names", func() { failShardRead(5) }),
	)

	It("fails the write, with nothing prepared or written, when the clear fails, and clears again on a retry", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		strandedByRGWGo(ctx)
		before := c.Writes(rookIndexPool, "", shard)
		ac.set(onFirst("bucket_clear_olh", failWith(syscall.EIO)))
		_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
		var re *radosclient.Error
		Expect(errors.As(err, &re)).To(BeTrue(), "the clear's own failure is the write's: %v", err)
		Expect(re.Errno).To(Equal(int32(syscall.EIO)))
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(before), "no index write landed")
		Expect(c.Object(testDataPool, "", olhOID)).To(BeNil(), "no head was written")
		expectStranded()

		_, err = put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		expectListedAsNewKey(ctx, "two")
	})

	// A write that links takes the entry over, as the class's link of a
	// version over an entry marked for removal does.
	It("sends no clear for a write of an enabled bucket, whose link takes the entry over", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		strandedByRGWGo(ctx)
		rec.Info.Flags = meta.BucketVersioned
		_, err := put(ctx, "", "two", op.PutParams{Tag: "tag-null"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(clears()).To(BeZero())
		Expect(listed(ctx, true)).To(Equal([]string{"k/null"}))
		e, ok := olhEntry()
		Expect(ok).To(BeTrue())
		Expect([]any{e.Exists, e.PendingRemoval}).To(Equal([]any{true, false}))
	})

	It("sends no clear for a write of a suspended bucket at an epoch, a versioned op whose link takes the entry over", func(ctx SpecContext) {
		fresh(ctx, denc.Squid)
		strandedByRGWGo(ctx)
		_, err := s.PutObjectAtEpochForTest(ctx, rec, version(""), strings.NewReader("two"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tag-null"}, 5)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(clears()).To(BeZero())
		Expect(listed(ctx, true)).To(Equal([]string{"k/null"}))
		e, ok := olhEntry()
		Expect(ok).To(BeTrue())
		Expect([]any{e.Exists, e.PendingRemoval}).To(Equal([]any{true, false}))
	})

	// The read before the prepare is one bi_get of the plain entry: a missing
	// entry, or one that is no version marker, stops there, and no read of the
	// bucket instance follows, which readShard makes after a missing entry. A
	// write over a head that exists, a bucket's without an index, and a key in
	// a namespace pay nothing.
	DescribeTable("what the write pays in reads against a bucket never versioned",
		func(ctx SpecContext, prep func(context.Context), run func(context.Context) error, extra int) {
			reads := func(flags uint32) (shardReads, instanceReads int) {
				GinkgoHelper()
				fresh(ctx, denc.Squid)
				rec.Info.Flags = flags
				prep(ctx)
				c.ResetCounters()
				Expect(run(ctx)).To(Succeed())
				settle(s)
				return indexReads(), c.Reads(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())
			}
			neverShard, neverInstance := reads(0)
			suspendedShard, suspendedInstance := reads(meta.BucketVersioned | meta.BucketVersionsSuspended)
			Expect(suspendedShard-neverShard).To(Equal(extra), "reads of the index, against a bucket never versioned")
			Expect(suspendedInstance).To(Equal(neverInstance), "reads of the bucket instance")
		},
		Entry("a new key: the plain entry",
			func(context.Context) {}, func(ctx context.Context) error { return putTwo(ctx, op.PutParams{}) }, 1),
		Entry("a key written before: the plain entry",
			func(ctx context.Context) { putOne(ctx) }, func(ctx context.Context) error { return putTwo(ctx, op.PutParams{}) }, 1),
		Entry("a conditional overwrite: nothing, its head being read first",
			func(ctx context.Context) { putOne(ctx) },
			func(ctx context.Context) error { return putTwo(ctx, op.PutParams{IfMatch: md5hex("one")}) }, 0),
		Entry("a bucket without an index: nothing",
			func(context.Context) { rec.Info.Layout.Current.Layout.Type = meta.IndexIndexless },
			func(ctx context.Context) error { return putTwo(ctx, op.PutParams{}) }, 0),
		Entry("a key in a namespace: nothing",
			func(context.Context) {},
			func(ctx context.Context) error {
				_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k", NS: "other"}, strings.NewReader("two"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tag-ns"})
				return err
			}, 0),
	)
})
