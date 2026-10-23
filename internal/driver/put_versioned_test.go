package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
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

// sendFunc sends a write op as its pool would.
type sendFunc = func() (uint64, error)

// aroundCluster is a fakerados cluster whose pools hand each write op to
// around, when it is set, with the call that sends the op.
type aroundCluster struct {
	*fakerados.Cluster
	mu     sync.Mutex
	around func(op *radosclient.WriteOp, send sendFunc) (uint64, error)
}

func (c *aroundCluster) set(around func(op *radosclient.WriteOp, send sendFunc) (uint64, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.around = around
}

func (c *aroundCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return &aroundPool{Pool: p, c: c}, nil
}

type aroundPool struct {
	radosclient.Pool
	c *aroundCluster
}

func (p *aroundPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	p.c.mu.Lock()
	around := p.c.around
	p.c.mu.Unlock()
	send := func() (uint64, error) { return p.Pool.Write(ctx, oid, op, flags) }
	if around == nil {
		return send()
	}
	return around(op, send)
}

// callsMethod reports whether steps hold an exec of the cls method.
func callsMethod(steps []radosclient.Step, method string) bool {
	return slices.ContainsFunc(steps, func(st radosclient.Step) bool {
		x, ok := st.(*radosclient.ExecStep)
		return ok && x.Method == method
	})
}

// onFirstMatching is an around that hands the first write op match takes
// to fn, and sends every other op as it is.
func onFirstMatching(match func(*radosclient.WriteOp) bool, fn func(send sendFunc) (uint64, error)) func(*radosclient.WriteOp, sendFunc) (uint64, error) {
	var taken atomic.Bool
	return func(op *radosclient.WriteOp, send sendFunc) (uint64, error) {
		if !match(op) || !taken.CompareAndSwap(false, true) {
			return send()
		}
		return fn(send)
	}
}

// onFirst is onFirstMatching of the first write op calling method.
func onFirst(method string, fn func(send sendFunc) (uint64, error)) func(*radosclient.WriteOp, sendFunc) (uint64, error) {
	return onFirstMatching(func(op *radosclient.WriteOp) bool { return callsMethod(op.Steps(), method) }, fn)
}

// writesData reports whether op writes an object's data, as a head write
// does and no OLH step does.
func writesData(op *radosclient.WriteOp) bool { return writesDataSteps(op.Steps()) }

// writesDataSteps is writesData of a write's steps.
func writesDataSteps(steps []radosclient.Step) bool {
	return slices.ContainsFunc(steps, func(st radosclient.Step) bool {
		_, ok := st.(*radosclient.WriteFullStep)
		return ok
	})
}

// initsOLHSteps reports whether a write's steps record a pending
// modification on an OLH head, as an OLH init does.
func initsOLHSteps(steps []radosclient.Step) bool {
	return slices.ContainsFunc(steps, func(st radosclient.Step) bool {
		x, ok := st.(*radosclient.SetXattrStep)
		return ok && strings.HasPrefix(x.Name, meta.AttrOLHPendingPrefix)
	})
}

// linkOpTag is the op tag of the bucket_link_olh w sends.
func linkOpTag(w fakerados.RecordedWrite) string {
	GinkgoHelper()
	for _, st := range w.Steps() {
		if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_link_olh" {
			d := denc.NewDecoder(x.In)
			l := rgwcls.DecodeLinkOLHOp(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			return l.OpTag
		}
	}
	Fail("the write sends no bucket_link_olh")
	return ""
}

// failWith is an onFirst fn that answers errno without sending the op.
func failWith(errno syscall.Errno) func(sendFunc) (uint64, error) {
	return func(sendFunc) (uint64, error) { return 0, &radosclient.Error{Errno: int32(errno), Op: "write"} }
}

var _ = Describe("a versioned PutObject", func() {
	var (
		c      *fakerados.Cluster
		ac     *aroundCluster
		s      *driver.Store
		rec    *op.BucketRecord
		clock  atomic.Int64
		shard  string
		olhOID string
		attrs  map[string][]byte
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	open := func(ctx context.Context, r denc.Release) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, ac, conf(nil), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
	}
	// fresh is the fixture at release r: an empty cluster and a bucket whose
	// versioning is enabled.
	fresh := func(ctx context.Context, r denc.Release) {
		GinkgoHelper()
		clock.Store(time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC).UnixNano())
		c = newPutCluster()
		c.SetClock(now)
		ac = &aroundCluster{Cluster: c}
		open(ctx, r)
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
		olhOID = olhHeadOID(version(""))
		attrs = map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
	}
	BeforeEach(func(ctx SpecContext) { fresh(ctx, denc.Squid) })
	AfterEach(func() {
		ac.set(nil)
		settle(s)
	})

	putBytes := func(ctx context.Context, instance string, body []byte, p op.PutParams) (*op.PutResult, error) {
		if p.Attrs == nil {
			p.Attrs = attrs
		}
		p.Size = int64(len(body))
		if p.Tag == "" {
			p.Tag = "tag-" + instance
		}
		return s.PutObject(ctx, rec, version(instance), bytes.NewReader(body), p)
	}
	put := func(ctx context.Context, instance, body string, p op.PutParams) (*op.PutResult, error) {
		return putBytes(ctx, instance, []byte(body), p)
	}
	mustPut := func(ctx context.Context, instance, body string) {
		GinkgoHelper()
		_, err := put(ctx, instance, body, op.PutParams{})
		Expect(err).NotTo(HaveOccurred(), "the put of version %q", instance)
	}
	marker := func(ctx context.Context, instance string) {
		GinkgoHelper()
		link := driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}}
		Expect(s.SetOLHForTest(ctx, rec, version(instance), link)).To(Succeed())
	}
	suspend := func() { rec.Info.Flags |= meta.BucketVersionsSuspended }

	olhHead := func() *fakerados.Object { return c.Object(testDataPool, "", olhOID) }
	versionHead := func(instance string) *fakerados.Object {
		return c.Object(testDataPool, "", instanceHeadOID(version(instance)))
	}
	target := func() meta.OLHInfo {
		GinkgoHelper()
		o := olhHead()
		Expect(o).NotTo(BeNil(), "the OLH head")
		d := denc.NewDecoder(o.Xattrs[meta.AttrOLHInfo])
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred(), "the OLH head's olh.info")
		return info
	}
	pending := func() []string {
		var out []string
		for name := range olhHead().Xattrs {
			if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
				out = append(out, name)
			}
		}
		return out
	}
	listed := func() []string {
		var out []string
		for _, en := range c.ListEntries(rookIndexPool, "", shard, "k") {
			out = append(out, en.Key.Instance)
		}
		return out
	}
	instanceEntry := func(instance string) (rgwcls.DirEntry, bool) {
		return c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: instance})
	}
	olhEntry := func() rgwcls.OLHEntry {
		GinkgoHelper()
		e, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue(), "the OLH entry of k")
		return e
	}
	// shardWrites are the write ops on k's shard that call method.
	shardWrites := func(method string) []fakerados.RecordedWrite {
		var out []fakerados.RecordedWrite
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			if callsMethod(w.Steps(), method) {
				out = append(out, w)
			}
		}
		return out
	}
	links := func() int { return len(shardWrites("bucket_link_olh")) }
	completion := func(i int) rgwcls.CompleteOp {
		GinkgoHelper()
		ws := shardWrites("bucket_complete_op")
		Expect(len(ws)).To(BeNumerically(">", i), "completion %d", i)
		return execIn(ws[i], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
	}
	// gone checks that version instance has no head and is not listed.
	gone := func(instance string) {
		GinkgoHelper()
		settle(s)
		Expect(versionHead(instance)).To(BeNil(), "the head of version %q", instance)
		Expect(listed()).NotTo(ContainElement(instance), "the list entry of version %q", instance)
	}

	noVersion := func(context.Context) {}
	current := func(ctx context.Context) { mustPut(ctx, "v1", "one") }
	currentMarker := func(ctx context.Context) {
		mustPut(ctx, "v1", "one")
		marker(ctx, "dm")
	}
	overAnother := func(ctx context.Context) {
		mustPut(ctx, "v1", "one")
		mustPut(ctx, "v2", "two")
	}
	suspendedCurrent := func(ctx context.Context) {
		current(ctx)
		suspend()
	}
	suspendedMarker := func(ctx context.Context) {
		currentMarker(ctx)
		suspend()
	}

	Describe("on a bucket whose versioning is enabled", func() {
		It("writes a new key's version at its instance and links it at epoch 2 under a new OLH", func(ctx SpecContext) {
			res, err := put(ctx, "v1", "one", op.PutParams{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.PendingCompletionsForTest()).To(BeZero(), "the completion was awaited, not queued")

			head := versionHead("v1")
			Expect(head).NotTo(BeNil(), "the version's head at its instance's oid")
			Expect(head.Data).To(Equal([]byte("one")))
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tag-v1\x00")))
			Expect(head.Xattrs).NotTo(HaveKey(meta.AttrOLHVer))
			Expect(res.Epoch).To(Equal(head.Version))

			en, ok := instanceEntry("v1")
			Expect(ok).To(BeTrue(), "the instance entry")
			Expect([]any{en.Exists, en.Flags, en.VersionedEpoch, en.Meta.ETag, en.Tag}).
				To(Equal([]any{true, rgwcls.FlagVer | rgwcls.FlagCurrent, uint64(2), md5hex("one"), "tag-v1"}))
			Expect(listed()).To(Equal([]string{"v1"}), "the list entry")
			plain, ok := c.Entry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeTrue())
			Expect(plain.Flags).To(Equal(rgwcls.FlagVerMarker), "a version marker at the plain name")
			oe := olhEntry()
			Expect([]any{oe.Key, oe.Epoch, oe.Exists, oe.DeleteMarker}).To(Equal([]any{rgwcls.ObjKey{Name: "k", Instance: "v1"}, uint64(2), true, false}))
			Expect(oe.PendingLog).To(BeEmpty(), "the log is trimmed")

			olh := olhHead()
			Expect(olh.Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("2")))
			Expect(olh.Xattrs).To(HaveKeyWithValue(meta.AttrOLHIDTag, []byte(oe.Tag)))
			Expect(olh.Xattrs).To(HaveKey(meta.AttrIDTag))
			Expect(target()).To(Equal(meta.OLHInfo{Target: meta.Obj{Bucket: rec.Info.Bucket, Key: version("v1")}}))
			Expect(pending()).To(BeEmpty())

			prep := execIn(shardWrites("bucket_prepare_op")[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
			Expect([]any{prep.Key, prep.BILogFlags}).To(Equal([]any{rgwcls.ObjKey{Name: "k", Instance: "v1"}, rgwcls.BILogFlagVersionedOp}))
			comp := completion(0)
			Expect([]any{comp.Key, comp.BILogFlags}).To(Equal([]any{rgwcls.ObjKey{Name: "k", Instance: "v1"}, rgwcls.BILogFlagVersionedOp}))
		})

		It("links an overwrite at epoch 3 and demotes the version it replaces", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			mustPut(ctx, "v2", "two")
			first, _ := instanceEntry("v1")
			second, _ := instanceEntry("v2")
			Expect([]any{first.Flags, first.VersionedEpoch}).To(Equal([]any{rgwcls.FlagVer, uint64(2)}), "the first demoted")
			Expect([]any{second.Flags, second.VersionedEpoch}).To(Equal([]any{rgwcls.FlagVer | rgwcls.FlagCurrent, uint64(3)}))
			Expect(listed()).To(Equal([]string{"v2", "v1"}))
			Expect(olhHead().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("3")))
			Expect(target().Target.Key).To(Equal(version("v2")))
			Expect(versionHead("v1").Data).To(Equal([]byte("one")), "an overwrite keeps the version it replaces")
		})

		It("makes a pre-versioning object the null version at epoch 1, its head keeping its data", func(ctx SpecContext) {
			plain := testBucket(putBucketID, 11)
			_, err := s.PutObject(ctx, plain, version(""), strings.NewReader("old"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tag-old"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			mustPut(ctx, "v1", "one")

			null, ok := instanceEntry("")
			Expect(ok).To(BeTrue(), "the null version's instance entry")
			Expect([]any{null.Flags, null.VersionedEpoch, null.Meta.ETag}).To(Equal([]any{rgwcls.FlagVer, uint64(1), md5hex("old")}))
			Expect(listed()).To(Equal([]string{"v1", ""}))
			marker, _ := c.Entry(rookIndexPool, "", shard, "k")
			Expect(marker.Flags).To(Equal(rgwcls.FlagVerMarker), "a version marker at the plain name")
			head := olhHead()
			Expect(head.Data).To(Equal([]byte("old")), "the head keeps the null version's data")
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrETag, []byte(md5hex("old"))))
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("2")))
			Expect(head.Xattrs).To(HaveKey(meta.AttrOLHIDTag))
			Expect(target().Target.Key).To(Equal(version("v1")))
		})

		It("links an explicit epoch below the current one as a noncurrent version", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			mustPut(ctx, "v2", "two")
			_, err := s.PutObjectAtEpochForTest(ctx, rec, version("v0"), strings.NewReader("zero"),
				op.PutParams{Attrs: attrs, Size: 4, Tag: "tag-v0"}, 2)
			Expect(err).NotTo(HaveOccurred())
			en, ok := instanceEntry("v0")
			Expect(ok).To(BeTrue())
			Expect([]any{en.Flags, en.VersionedEpoch}).To(Equal([]any{rgwcls.FlagVer, uint64(2)}))
			Expect(listed()).To(ConsistOf("v2", "v1", "v0"))
			Expect(target().Target.Key).To(Equal(version("v2")), "the current version stays")
			Expect(olhHead().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("3")))
			Expect(olhEntry().Epoch).To(BeEquivalentTo(3))
			Expect(pending()).To(BeEmpty(), "a link that logs nothing leaves no pending attr")
		})

		It("links a write without an instance as the null version", func(ctx SpecContext) {
			_, err := put(ctx, "", "null", op.PutParams{})
			Expect(err).NotTo(HaveOccurred())
			Expect(links()).To(Equal(1), "versioning_enabled makes it a versioned op")
			Expect(olhHead().Data).To(Equal([]byte("null")))
			Expect(olhHead().Xattrs).To(HaveKey(meta.AttrOLHVer))
			Expect(target().Target.Key).To(Equal(version("")))
			Expect(listed()).To(Equal([]string{""}))
		})

		It("answers an If-Match write 412 when the version it matched is removed between its init and its link", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			c.AfterWrite(testDataPool, "", olhOID, func() {
				c.AfterWrite(testDataPool, "", olhOID, nil)
				Expect(s.UnlinkObjInstanceForTest(ctx, rec, version("v1"), driver.OLHUnlink{})).To(Succeed())
			})
			_, err := put(ctx, "v2", "two", op.PutParams{IfMatch: md5hex("one")})
			Expect(err).To(MatchError(op.ErrPreconditionFailed))
			gone("v2")
			Expect(listed()).To(BeEmpty())
		})

		It("never links before its completion's reply", func(ctx SpecContext) {
			arrived, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			releaseAll := func() { once.Do(func() { close(release) }) }
			DeferCleanup(releaseAll)
			ac.set(onFirst("bucket_complete_op", func(send sendFunc) (uint64, error) {
				v, err := send()
				close(arrived)
				<-release
				return v, err
			}))
			done := make(chan error, 1)
			go func() {
				_, err := put(ctx, "v1", "one", op.PutParams{})
				done <- err
			}()
			Eventually(arrived).WithTimeout(5 * time.Second).Should(BeClosed())
			en, ok := instanceEntry("v1")
			Expect([]any{ok, en.Exists}).To(Equal([]any{true, true}), "the completion landed")
			Consistently(links).WithTimeout(100*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeZero(),
				"no link reaches the shard while the completion's reply is held")
			releaseAll()
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(Succeed()))
			Expect(links()).To(Equal(1))
			Expect(target().Target.Key).To(Equal(version("v1")))
		})
	})

	Describe("on a bucket whose versioning is suspended", func() {
		It("writes the null version over the key's OLH head, its old data's tails to the GC", func(ctx SpecContext) {
			s.SetRandForTest(fixedRand(putPrefix))
			plain := testBucket(putBucketID, 11)
			old := bytes.Repeat([]byte("n"), 6<<20)
			_, err := s.PutObject(ctx, plain, version(""), bytes.NewReader(old), op.PutParams{Attrs: attrs, Size: int64(len(old)), Tag: "tag-old"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			mustPut(ctx, "v1", "one")
			suspend()
			idTag, olhTag := olhHead().Xattrs[meta.AttrIDTag], olhHead().Xattrs[meta.AttrOLHIDTag]
			s.SetRandForTest(fixedRand(secondPrefix))
			c.ResetCounters()

			_, err = put(ctx, "", "new", op.PutParams{Tag: "tag-null"})
			Expect(err).NotTo(HaveOccurred())

			head := olhHead()
			Expect(head.Data).To(Equal([]byte("new")))
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tag-null\x00")))
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrOLHIDTag, olhTag), "the reset keeps the OLH attrs")
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("3")))
			Expect(target().Target.Key).To(Equal(version("")), "the null version is current")
			Expect(pending()).To(BeEmpty())
			var headWrite fakerados.RecordedWrite
			for _, w := range c.WritesTo(testDataPool, "", olhOID) {
				if slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool { _, ok := st.(*radosclient.WriteFullStep); return ok }) {
					headWrite = w
				}
			}
			Expect(headWrite.Steps()).To(ContainElement(&radosclient.CmpXattrStep{Name: meta.AttrIDTag, Op: radosclient.CmpEQ, Value: idTag}),
				"an instance-less write is guarded on the tag it read")
			Expect(headWrite.Steps()).To(ContainElement(&radosclient.SetXattrStep{Name: meta.AttrOLHIDTag, Value: olhTag}),
				"the OLH tag it read is set again")

			null, _ := instanceEntry("")
			v1, _ := instanceEntry("v1")
			Expect([]any{null.Flags, null.VersionedEpoch, null.Meta.ETag}).To(Equal([]any{rgwcls.FlagVer | rgwcls.FlagCurrent, uint64(3), md5hex("new")}))
			Expect([]any{v1.Flags, v1.VersionedEpoch}).To(Equal([]any{rgwcls.FlagVer, uint64(2)}))
			Expect(listed()).To(Equal([]string{"", "v1"}))

			entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tag-old\x00")))
			Expect(entries).To(HaveLen(1), "the old null data's tails")
			Expect(entries[0].Chain).To(Equal([]rgwcls.GCObj{{Pool: testDataPool, Key: rgwcls.ObjKey{Name: tailOID(putPrefix, 1)}}}))

			prep := execIn(shardWrites("bucket_prepare_op")[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
			Expect(prep.BILogFlags).To(BeZero(), "prepared on the assume-no-entry pass, which reads no head")
			Expect(completion(0).BILogFlags).To(Equal(rgwcls.BILogFlagVersionedOp), "the guarded pass read an OLH head")
		})

		rgwLinks := func(ctx context.Context) { newRGWWriter(c, s, rec, denc.Squid, "rgw").Run(ctx) }
		rgwLinksUnreplayed := func(ctx context.Context) {
			rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
			for _, step := range []func(context.Context){rw.Prepare, rw.HeadWrite, rw.Complete, rw.ReadHead, rw.InitRetrying, rw.Link} {
				step(ctx)
			}
		}
		anotherMarker := func(ctx context.Context) { marker(ctx, "dm2") }
		DescribeTable("checks a conditional null write again when another writer changes the key before its head write, which carries its init",
			func(ctx SpecContext, interlope func(context.Context), want error, current string) {
				mustPut(ctx, "v1", "one")
				marker(ctx, "dm")
				suspend()
				ac.set(onFirstMatching(writesData, func(send sendFunc) (uint64, error) {
					interlope(ctx)
					return send()
				}))
				_, err := put(ctx, "", "new", op.PutParams{Tag: "tag-null", IfNoneMatch: "*"})
				settle(s)
				Expect(target().Target.Key).To(Equal(version(current)))
				Expect(pending()).To(BeEmpty(), "no modification is left pending")
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
					Expect(olhHead().Data).To(Equal([]byte("new")))
					return
				}
				Expect(err).To(MatchError(want))
				Expect(olhHead().Data).To(BeEmpty(), "the head keeps its data")
				_, ok := instanceEntry("")
				Expect(ok).To(BeFalse(), "the null version's prepared entry is canceled")
			},
			Entry("412 after a radosgw writer's link it replayed", rgwLinks, op.ErrPreconditionFailed, "rgw"),
			Entry("412 after a radosgw writer's link it has not replayed, whose pending attr refuses the head write and which the next check's follow replays",
				rgwLinksUnreplayed, op.ErrPreconditionFailed, "rgw"),
			Entry("success after another delete marker's link, which leaves the condition holding", anotherMarker, nil, ""),
		)

		DescribeTable("serializes two conditional null writes: the second waits out the first's modification and answers 412",
			func(ctx SpecContext, setup func(context.Context), ifMatch, ifNoneMatch string) {
				setup(ctx)
				suspend()
				bWaiting, aDone, bDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var waiting, finished sync.Once
				finishA := func() { finished.Do(func() { close(aDone) }) }
				DeferCleanup(finishA)
				var backoffs atomic.Int32
				driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
					backoffs.Add(1)
					waiting.Do(func() { close(bWaiting) })
					<-aDone
					return nil
				})
				params := func(tag string) op.PutParams {
					return op.PutParams{Tag: tag, IfMatch: ifMatch, IfNoneMatch: ifNoneMatch}
				}
				var bErr error
				ac.set(onFirst("bucket_complete_op", func(send sendFunc) (uint64, error) {
					go func() {
						defer close(bDone)
						_, bErr = put(ctx, "", "bbb", params("tag-b"))
					}()
					select {
					case <-bWaiting:
					case <-bDone:
					}
					return send()
				}))
				_, aErr := put(ctx, "", "aaa", params("tag-a"))
				finishA()
				Eventually(bDone).WithTimeout(5 * time.Second).Should(BeClosed())
				Expect([]error{aErr, bErr}).NotTo(Equal([]error{nil, nil}), "at most one of the two writes succeeds")
				Expect(aErr).NotTo(HaveOccurred(), "the write whose head write came first")
				Expect(bErr).To(MatchError(op.ErrPreconditionFailed), "the write that met the first's pending modification")
				Expect(backoffs.Load()).To(BeEquivalentTo(1), "the second waited once, for the first's link")
				settle(s)
				Expect(olhHead().Data).To(Equal([]byte("aaa")))
				Expect(target().Target.Key).To(Equal(version("")))
				Expect(pending()).To(BeEmpty())
			},
			Entry("If-None-Match * over a current delete marker", currentMarker, "", "*"),
			Entry("If-Match the current version's ETag", current, md5hex("one"), ""),
		)

		DescribeTable("sends its init in its head write's op, the guards first, and links the null version under the init's op tag",
			func(ctx SpecContext, setup func(context.Context), ifMatch, ifNoneMatch string, tagGuard bool) {
				setup(ctx)
				before := olhHead().Xattrs
				c.ResetCounters()
				_, err := put(ctx, "", "new", op.PutParams{Tag: "tag-null", IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
				Expect(err).NotTo(HaveOccurred())
				writes := c.WritesTo(testDataPool, "", olhOID)
				Expect(writes).To(HaveLen(2), "the head write carrying the init, then the replay's apply")
				steps := writes[0].Steps()
				Expect(initsOLHSteps(steps)).To(BeTrue(), "the head write records the pending modification")
				Expect(writesDataSteps(steps)).To(BeTrue())
				Expect(initsOLHSteps(writes[1].Steps()) || writesDataSteps(writes[1].Steps())).To(BeFalse())

				guards := []radosclient.Step{
					&radosclient.CmpXattrStep{Name: meta.AttrOLHIDTag, Op: radosclient.CmpEQ, Value: before[meta.AttrOLHIDTag]},
					&radosclient.CmpXattrStep{Name: meta.AttrOLHInfo, Op: radosclient.CmpEQ, Value: before[meta.AttrOLHInfo]},
				}
				Expect(len(steps)).To(BeNumerically(">", len(guards)+2))
				Expect(steps[:len(guards)]).To(Equal(guards), "the init's guards on the OLH head as the check read it come first")
				Expect(execIn(writes[0], len(guards), "obj_check_attrs_prefix", rgwcls.DecodeCheckAttrsPrefixOp)).
					To(Equal(rgwcls.CheckAttrsPrefixOp{CheckPrefix: meta.AttrOLHPendingPrefix, FailIfExist: true}), "then no other writer's pending attr")
				next := len(guards) + 1
				if tagGuard {
					Expect(steps[next]).To(Equal(&radosclient.CmpXattrStep{Name: meta.AttrIDTag, Op: radosclient.CmpEQ, Value: before[meta.AttrIDTag]}),
						"then radosgw's guard on the write tag it read")
					next++
				}
				Expect(steps[next]).To(Equal(&radosclient.CreateStep{Exclusive: false}), "the head write's own steps follow the guards")
				for _, st := range steps[next:] {
					_, cmp := st.(*radosclient.CmpXattrStep)
					Expect(cmp).To(BeFalse(), "no guard after the head write's first step")
				}
				var initTag string
				for _, st := range steps {
					if x, ok := st.(*radosclient.SetXattrStep); ok && strings.HasPrefix(x.Name, meta.AttrOLHPendingPrefix) {
						initTag = strings.TrimPrefix(x.Name, meta.AttrOLHPendingPrefix)
					}
				}
				linked := shardWrites("bucket_link_olh")
				Expect(linked).To(HaveLen(1))
				Expect(linkOpTag(linked[0])).To(Equal(initTag), "the link is made under the init's op tag")
				Expect(pending()).To(BeEmpty())
				Expect(olhHead().Data).To(Equal([]byte("new")))
				Expect(target().Target.Key).To(Equal(version("")))
				prep := execIn(shardWrites("bucket_prepare_op")[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
				Expect(prep.BILogFlags).To(Equal(rgwcls.BILogFlagVersionedOp), "the guarded pass read an OLH head")
			},
			Entry("If-None-Match * over a current delete marker, with no write-tag guard", suspendedMarker, "", "*", false),
			Entry("If-Match the current version's ETag, with radosgw's write-tag guard", suspendedCurrent, md5hex("one"), "", true),
		)

		It("folds the init of an OLH head without an OLH tag too, guarded on its having none, as olh_init_modification inits one", func(ctx SpecContext) {
			plain := testBucket(putBucketID, 11)
			_, err := s.PutObject(ctx, plain, version(""), strings.NewReader("old"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tag-old"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			pool, err := c.Pool(ctx, testDataPool, "")
			Expect(err).NotTo(HaveOccurred())
			w := radosclient.NewWriteOp()
			w.SetXattr(meta.AttrOLHVer, []byte{})
			w.SetXattr(meta.AttrOLHInfo, encode(meta.OLHInfo{Target: meta.Obj{Bucket: rec.Info.Bucket, Key: version("")}}))
			_, err = pool.Write(ctx, olhOID, w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			Expect(olhHead().Xattrs).NotTo(HaveKey(meta.AttrOLHIDTag))
			suspend()
			c.ResetCounters()

			_, err = put(ctx, "", "new", op.PutParams{Tag: "tag-null", IfMatch: md5hex("old")})
			Expect(err).NotTo(HaveOccurred())
			writes := c.WritesTo(testDataPool, "", olhOID)
			Expect(writes).NotTo(BeEmpty())
			steps := writes[0].Steps()
			Expect(initsOLHSteps(steps) && writesDataSteps(steps)).To(BeTrue(), "one op inits and writes the head")
			Expect(steps[0]).To(Equal(&radosclient.CmpXattrStep{Name: meta.AttrOLHIDTag, Op: radosclient.CmpEQ, Value: nil}),
				"guarded on the head having no OLH tag")
			head := olhHead()
			Expect(head.Data).To(Equal([]byte("new")))
			Expect(head.Xattrs[meta.AttrOLHIDTag]).NotTo(BeEmpty(), "the init gave the head an OLH tag")
			Expect(head.Xattrs[meta.AttrOLHIDTag]).To(Equal([]byte(olhEntry().Tag)), "the one the link used")
			Expect(target().Target.Key).To(Equal(version("")))
			Expect(pending()).To(BeEmpty())
		})

		It("cancels its modification when the link cannot read the OLH head, which takes no link under it", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			linked := links()
			ac.set(onFirst("bucket_complete_op", func(send sendFunc) (uint64, error) {
				v, err := send()
				c.FailNextRead(testDataPool, "", olhOID, syscall.EIO)
				return v, err
			}))
			_, err := put(ctx, "", "new", op.PutParams{Tag: "tag-null", IfMatch: md5hex("one")})
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(links()).To(Equal(linked), "no link is sent")
			Expect(pending()).To(BeEmpty(), "the modification its head write inited is canceled")
		})

		It("has the link cancel an inited modification it refuses for carrying a condition too", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			head, err := s.ReadOLHHeadForTest(ctx, rec, version(""))
			Expect(err).NotTo(HaveOccurred())
			opTag, _, err := s.OLHInitModificationForTest(ctx, head, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(pending()).To(HaveLen(1))
			cond := func(context.Context, *op.ObjectState) (driver.OLHPass, error) {
				return driver.OLHPass{Current: true}, nil
			}
			err = s.SetOLHInitedForTest(ctx, rec, version(""), driver.OLHLink{Cond: cond}, head, opTag)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(links()).To(Equal(1), "only v1's")
			Expect(pending()).To(BeEmpty(), "the refused link cancels the modification it was handed")
		})

		It("cancels its modification after a failed completion under a context its caller has ended", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			ac.set(onFirst("bucket_complete_op", func(sendFunc) (uint64, error) {
				cancel()
				return failWith(syscall.EIO)(nil)
			}))
			epoch := uint64(0)
			hw := driver.HeadWriteForTest{
				Key: version(""), Tag: "tag-null", IfMatch: md5hex("one"), Data: []byte("new"), Attrs: attrs,
				Create: true, Size: 3, AccountedSize: 3, OLHEpoch: &epoch,
			}
			_, err := s.WriteMetaForTest(cctx, rec, hw, s.NewIndexOpForTest(rec, version(""), "tag-null"))
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(pending()).To(BeEmpty(), "the cancel is sent past the context's end")
			Expect(target().Target.Key).To(Equal(version("v1")))
		})

		It("reads the head again when it is gone at the head write, as an init that finds it gone does", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			marker(ctx, "dm")
			suspend()
			ac.set(onFirstMatching(writesData, func(send sendFunc) (uint64, error) {
				pool, err := c.Pool(ctx, testDataPool, "")
				Expect(err).NotTo(HaveOccurred())
				rm := radosclient.NewWriteOp()
				rm.Remove()
				_, err = pool.Write(ctx, olhOID, rm, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				return send()
			}))
			_, err := put(ctx, "", "new", op.PutParams{Tag: "tag-null", IfNoneMatch: "*"})
			Expect(err).NotTo(HaveOccurred())
			head := olhHead()
			Expect(head).NotTo(BeNil(), "the next attempt, finding no head, writes one")
			Expect(head.Data).To(Equal([]byte("new")))
			Expect(head.Xattrs).NotTo(HaveKey(meta.AttrOLHVer), "a plain write, the key no longer having an OLH head")
		})

		It("answers a conditional null write whose head another null write replaced since its check as radosgw answers a lost race", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			mustPut(ctx, "", "mid")
			ac.set(onFirstMatching(writesData, func(send sendFunc) (uint64, error) {
				_, err := put(ctx, "", "other", op.PutParams{Tag: "tag-other"})
				Expect(err).NotTo(HaveOccurred())
				return send()
			}))
			_, err := put(ctx, "", "new", op.PutParams{Tag: "tag-new", IfMatch: md5hex("mid")})
			Expect(err).To(MatchError(op.ErrConcurrentModification), "the head's write tag changed: radosgw's ECANCELED, not a check again")
			settle(s)
			Expect(olhHead().Data).To(Equal([]byte("other")))
			Expect(target().Target.Key).To(Equal(version("")))
		})

		It("writes a key without an OLH as an unversioned object, with no link", func(ctx SpecContext) {
			suspend()
			_, err := put(ctx, "", "plain", op.PutParams{})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			head := olhHead()
			Expect(head.Data).To(Equal([]byte("plain")))
			for _, name := range []string{meta.AttrOLHVer, meta.AttrOLHInfo, meta.AttrOLHIDTag} {
				Expect(head.Xattrs).NotTo(HaveKey(name))
			}
			_, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeFalse(), "no OLH entry")
			en, ok := c.Entry(rookIndexPool, "", shard, "k")
			Expect([]any{ok, en.Exists, en.Flags}).To(Equal([]any{true, true, uint16(0)}), "the plain entry")
			Expect(links()).To(BeZero())
			Expect(completion(0).BILogFlags).To(BeZero())
		})
	})

	Describe("racing another conditional write", func() {
		It("links one of two interleaved If-None-Match: * writes and answers the other 412, its version removed", func(ctx SpecContext) {
			arrivedA, releaseA, aDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(releaseA) }) }
			DeferCleanup(release)
			ac.set(onFirst("bucket_link_olh", func(send sendFunc) (uint64, error) {
				close(arrivedA)
				<-releaseA
				return send()
			}))
			var aErr error
			go func() {
				defer close(aDone)
				_, aErr = put(ctx, "a", "a", op.PutParams{IfNoneMatch: "*"})
			}()
			Eventually(arrivedA).WithTimeout(5*time.Second).Should(BeClosed(), "a's init has landed and its link is held")
			backoffs := 0
			driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
				backoffs++
				release()
				<-aDone
				return nil
			})

			_, bErr := put(ctx, "b", "b", op.PutParams{IfNoneMatch: "*"})
			release()
			Eventually(aDone).WithTimeout(5 * time.Second).Should(BeClosed())
			Expect(aErr).NotTo(HaveOccurred(), "the write whose link came first")
			Expect(bErr).To(MatchError(op.ErrPreconditionFailed), "the write that met a's pending attr")
			Expect(backoffs).To(Equal(1), "b waited once for a's modification")
			gone("b")
			Expect(listed()).To(Equal([]string{"a"}))
			Expect(target().Target.Key).To(Equal(version("a")))
		})

		DescribeTable("answers by whether a radosgw writer that inits between its init and its link links first, on both releases",
			func(ctx SpecContext, rgwFirst bool) {
				for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
					fresh(ctx, r)
					rw := newRGWWriter(c, s, rec, r, "rgw")
					rw.Prepare(ctx)
					rw.HeadWrite(ctx)
					rw.Complete(ctx)
					replay := func() {
						rw.Link(ctx)
						rw.ReadLog(ctx)
						rw.Apply(ctx)
						rw.Trim(ctx)
					}
					afterNext := func(pool, oid string, fn func()) {
						c.AfterWrite(pool, "", oid, func() {
							c.AfterWrite(pool, "", oid, nil)
							fn()
						})
					}
					afterNext(testDataPool, olhOID, func() {
						rw.ReadHead(ctx)
						rw.InitRetrying(ctx)
						if rgwFirst {
							replay()
							return
						}
						afterNext(rookIndexPool, shard, replay)
					})

					_, err := put(ctx, "go", "go", op.PutParams{IfNoneMatch: "*"})
					settle(s)
					if rgwFirst {
						Expect(err).To(MatchError(op.ErrPreconditionFailed), "at %v", r)
						gone("go")
						Expect(listed()).To(Equal([]string{"rgw"}), "at %v", r)
					} else {
						Expect(err).NotTo(HaveOccurred(), "at %v", r)
						Expect(listed()).To(Equal([]string{"rgw", "go"}), "at %v", r)
					}
					Expect(target().Target.Key).To(Equal(version("rgw")), "at %v", r)
				}
			},
			Entry("412, its version removed, when the radosgw writer links first", true),
			Entry("success, the radosgw writer's version current over it, when that writer links second", false),
		)
	})

	It("finalizes its attrs once per head write, with the bucket, the mtime it stamps and whether it is PUT_OBJ_CREATE", func(ctx SpecContext) {
		type call struct {
			rec    *op.BucketRecord
			mtime  time.Time
			create bool
		}
		var calls []call
		DeferCleanup(driver.ObserveHeadAttrsForTest(s, func(r *op.BucketRecord, mtime time.Time, create bool) {
			calls = append(calls, call{r, mtime, create})
		}))
		plain := testBucket(putBucketID, 11)
		m1, m2 := now().Add(-time.Hour), now().Add(-time.Minute)
		_, err := s.PutObject(ctx, plain, version(""), strings.NewReader("a"), op.PutParams{Attrs: attrs, Size: 1, Tag: "t1", Mtime: m1})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		c.ResetCounters()
		_, err = s.PutObject(ctx, plain, version(""), strings.NewReader("b"), op.PutParams{Attrs: attrs, Size: 1, Tag: "t2", Mtime: m2})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Writes(testDataPool, "", olhOID)).To(Equal(2), "an overwrite's two passes")
		mustPut(ctx, "v1", "one")
		_, err = s.CreateUpload(ctx, plain, meta.ObjKey{Name: "mp"}, op.UploadParams{Owner: alice, Attrs: attrs})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]call{
			{plain, m1, true},
			{plain, m2, true},
			{rec, now(), true},
			{plain, now(), false},
		}), "the multipart meta object's PUT_OBJ_CREATE_EXCL is no PUT_OBJ_CREATE")
	})

	Describe("when a step after its prepare fails", func() {
		It("cancels its index entry and links nothing when its head write fails", func(ctx SpecContext) {
			c.FailNextWrite(testDataPool, "", instanceHeadOID(version("v1")), syscall.EIO)
			_, err := put(ctx, "v1", "one", op.PutParams{})
			Expect(err).To(HaveOccurred())
			settle(s)
			cancel := completion(0)
			Expect([]any{cancel.Op, cancel.BILogFlags}).To(Equal([]any{rgwcls.OpCancel, rgwcls.BILogFlagVersionedOp}))
			Expect(links()).To(BeZero())
			Expect(olhHead()).To(BeNil(), "no OLH is made")
		})

		DescribeTable("links nothing when its completion fails",
			func(ctx SpecContext, errno syscall.Errno, want error, removed bool) {
				mustPut(ctx, "v0", "zero")
				objects := func() uint64 { return driver.CachedBucketStatsForTest(s, rec).NumObjects }
				Expect(objects()).To(BeEquivalentTo(1))
				linked := links()
				ac.set(onFirst("bucket_complete_op", failWith(errno)))
				_, err := put(ctx, "v1", "one", op.PutParams{})
				Expect(err).To(MatchError(want))
				settle(s)
				Expect(links()).To(Equal(linked), "no link is sent")
				Expect(target().Target.Key).To(Equal(version("v0")))
				if removed {
					Expect(versionHead("v1")).To(BeNil(), "the version is removed")
					Expect(objects()).To(BeEquivalentTo(1), "the removal takes back what the write added")
				} else {
					Expect(versionHead("v1")).NotTo(BeNil(), "the version stays")
					Expect(objects()).To(BeEquivalentTo(2), "the version stays counted, as its index entry may")
				}
			},
			Entry("removing the version on an answer that proves the completion did not land", syscall.EIO, op.ErrUnknown, true),
			Entry("leaving the version on a timeout, after which the completion may yet land", syscall.ETIMEDOUT, op.ErrRequestTimedOut, false),
		)

		DescribeTable("leaves no pending modification when a conditional null write's head write, which carries its init, or its completion fails",
			func(ctx SpecContext, method string, errno syscall.Errno, want error) {
				mustPut(ctx, "v1", "one")
				suspend()
				linked := links()
				if method == "" {
					ac.set(onFirstMatching(writesData, failWith(errno)))
				} else {
					ac.set(onFirst(method, failWith(errno)))
				}
				_, err := put(ctx, "", "new", op.PutParams{Tag: "tag-null", IfMatch: md5hex("one")})
				Expect(err).To(MatchError(want))
				settle(s)
				Expect(links()).To(Equal(linked), "no link is sent")
				Expect(pending()).To(BeEmpty(), "a refused head write records nothing, and a failed completion's modification is canceled")
				Expect(target().Target.Key).To(Equal(version("v1")))
			},
			Entry("a head write the OSD refuses", "", syscall.EIO, op.ErrUnknown),
			Entry("a completion the class refuses", "bucket_complete_op", syscall.EIO, op.ErrUnknown),
			Entry("a completion that times out", "bucket_complete_op", syscall.ETIMEDOUT, op.ErrRequestTimedOut),
		)

		DescribeTable("keeps the tails its head names when its link fails",
			func(ctx SpecContext, errno syscall.Errno, want error, removed bool) {
				ac.set(onFirst("bucket_link_olh", failWith(errno)))
				_, err := putBytes(ctx, "v1", bytes.Repeat([]byte("t"), 6<<20), op.PutParams{})
				Expect(err).To(MatchError(want))
				settle(s)
				Expect(links()).To(BeZero(), "the failed link landed nothing")
				var tails []string
				for _, oid := range poolObjects(ctx, c, testDataPool, "") {
					if oid != instanceHeadOID(version("v1")) && oid != olhOID {
						tails = append(tails, oid)
					}
				}
				Expect(tails).NotTo(BeEmpty(), "the version's tails are not deleted")
				if !removed {
					Expect(versionHead("v1")).NotTo(BeNil(), "the version stays, its link's outcome unknown")
					return
				}
				Expect(versionHead("v1")).To(BeNil(), "the version is removed, its link refused")
				entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tag-v1\x00")))
				Expect(entries).To(HaveLen(1))
				var chain []string
				for _, o := range entries[0].Chain {
					chain = append(chain, o.Key.Name)
				}
				Expect(chain).To(ConsistOf(tails), "the removal sends the tails to the GC")
			},
			Entry("on a refusal, which removes the version", syscall.EIO, op.ErrUnknown, true),
			Entry("on a timeout, which leaves it", syscall.ETIMEDOUT, op.ErrRequestTimedOut, false),
		)
	})

	DescribeTable("checks its conditions on the current version, a delete marker counting as none, on both releases",
		func(ctx SpecContext, setup func(context.Context), instance, ifMatch, ifNoneMatch string, want error) {
			for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
				fresh(ctx, r)
				setup(ctx)
				var before []byte
				if o := olhHead(); o != nil {
					before = slices.Clone(o.Data)
				}
				writes := c.Writes(rookIndexPool, "", shard)
				_, err := put(ctx, instance, "new", op.PutParams{Tag: "tag-new", IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
				settle(s)
				if want == nil {
					Expect(err).NotTo(HaveOccurred(), "at %v", r)
					Expect(target().Target.Key).To(Equal(version(instance)), "the new version is current at %v", r)
					continue
				}
				Expect(err).To(MatchError(want), "at %v", r)
				Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(writes), "nothing reaches the index at %v", r)
				if instance == "" {
					Expect(olhHead().Data).To(Equal(before), "the head keeps its data at %v", r)
				} else {
					Expect(c.Writes(testDataPool, "", instanceHeadOID(version(instance)))).To(BeZero(), "no head is written at %v", r)
				}
			}
		},
		Entry("If-Match * with no version: 404", noVersion, "c", "*", "", op.ErrNoSuchKey),
		Entry("If-Match an ETag with no version: 404", noVersion, "c", md5hex("one"), "", op.ErrNoSuchKey),
		Entry("If-None-Match * with no version", noVersion, "c", "", "*", nil),
		Entry("If-Match the current version's ETag", current, "c", md5hex("one"), "", nil),
		Entry("If-Match another ETag: 412", current, "c", md5hex("other"), "", op.ErrPreconditionFailed),
		Entry("If-None-Match * over a current version: 412", current, "c", "", "*", op.ErrPreconditionFailed),
		Entry("If-None-Match the current version's ETag: 412", current, "c", "", md5hex("one"), op.ErrPreconditionFailed),
		Entry("If-None-Match another ETag over a current version", current, "c", "", md5hex("other"), nil),
		Entry("If-Match * over a current delete marker: 404", currentMarker, "c", "*", "", op.ErrNoSuchKey),
		Entry("If-Match an ETag over a current delete marker: 404", currentMarker, "c", md5hex("one"), "", op.ErrNoSuchKey),
		Entry("If-None-Match * over a current delete marker", currentMarker, "c", "", "*", nil),
		Entry("If-None-Match the ETag of the version a current delete marker hides", currentMarker, "c", "", md5hex("one"), nil),
		Entry("If-Match a noncurrent version's ETag: 412", overAnother, "c", md5hex("one"), "", op.ErrPreconditionFailed),
		Entry("If-Match the ETag of the version current over another", overAnother, "c", md5hex("two"), "", nil),
		Entry("a suspended bucket's null write If-Match the current version's ETag, which the OLH head does not hold",
			suspendedCurrent, "", md5hex("one"), "", nil),
		Entry("a suspended bucket's null write If-None-Match * over a current version: 412",
			suspendedCurrent, "", "", "*", op.ErrPreconditionFailed),
		Entry("a suspended bucket's null write If-None-Match * over a current delete marker, though its OLH head exists",
			suspendedMarker, "", "", "*", nil),
		Entry("a suspended bucket's null write If-Match * over a current delete marker: 404",
			suspendedMarker, "", "*", "", op.ErrNoSuchKey),
		Entry("a suspended bucket's null write If-Match an ETag over a current delete marker: 404",
			suspendedMarker, "", md5hex("one"), "", op.ErrNoSuchKey),
	)
})
