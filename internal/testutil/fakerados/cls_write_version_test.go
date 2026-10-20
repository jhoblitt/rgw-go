package fakerados_test

import (
	"context"
	"reflect"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// writeVersionOID is the object the class-write version specs stage and call.
const writeVersionOID = "obj"

// classCase registers one class emulator, stages the object a call finds,
// and makes that call, each through the class's own client.
type classCase struct {
	register func(c *fakerados.Cluster)
	stage    func(ctx context.Context, c *fakerados.Cluster, p radosclient.Pool)
	call     func(ctx context.Context, p radosclient.Pool) error
}

// objContent is what a stored object holds, its version and mtime aside.
type objContent struct {
	Data    []byte
	Xattrs  map[string][]byte
	Omap    map[string][]byte
	OmapHdr []byte
}

func contentOf(o *fakerados.Object) objContent {
	return objContent{Data: o.Data, Xattrs: o.Xattrs, Omap: o.Omap, OmapHdr: o.OmapHdr}
}

// writeOn runs the write op build composes on writeVersionOID.
func writeOn(build func(op *radosclient.WriteOp)) func(ctx context.Context, p radosclient.Pool) error {
	return func(ctx context.Context, p radosclient.Pool) error {
		return writeErr(ctx, p, writeVersionOID, build)
	}
}

// readOn runs the read op build composes on writeVersionOID.
func readOn(build func(op *radosclient.ReadOp)) func(ctx context.Context, p radosclient.Pool) error {
	return func(ctx context.Context, p radosclient.Pool) error {
		op := radosclient.NewReadOp()
		build(op)
		_, err := p.Read(ctx, writeVersionOID, op, radosclient.OpFlagNone)
		return err
	}
}

// stageWith stages the object with the write ops builds compose, after
// putting it empty when put is set.
func stageWith(put bool, builds ...func(op *radosclient.WriteOp)) func(ctx context.Context, c *fakerados.Cluster, p radosclient.Pool) {
	return func(ctx context.Context, c *fakerados.Cluster, p radosclient.Pool) {
		GinkgoHelper()
		if put {
			c.Put(poolName, ns, writeVersionOID, nil)
		}
		for _, build := range builds {
			Expect(writeErr(ctx, p, writeVersionOID, build)).To(Succeed())
		}
	}
}

// The fixtures the class-write version specs stage and call with.
func wvLCEntry() rgwcls.LCEntry {
	return rgwcls.LCEntry{Bucket: ":a:zone.4127.1", StartTime: 1790596800, Status: rgwcls.LCStatusComplete}
}

func wvLCHead() rgwcls.LCHead {
	return rgwcls.LCHead{StartDate: 1790596800, Marker: wvLCEntry().Bucket}
}

func wvObjVersion() version.ObjVersion { return version.ObjVersion{Ver: 3, Tag: "t"} }

func wvSyncTime() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }

// wvRenew is a lock its holder may take again, renewing it unchanged.
func wvRenew() lock.LockOp {
	return lock.LockOp{Name: "lc_process", Type: lock.TypeExclusive, Cookie: "c", Description: "d", Flags: lock.FlagMayRenew}
}

func wvGCInfo() rgwcls.GCObjInfo { return rgwcls.GCObjInfo{Tag: "tag"} }

func registerRGW(c *fakerados.Cluster) {
	c.RegisterClassMethods(rgwcls.Class, fakerados.RGWClass(), fakerados.RGWMethods, fakerados.RGWWriteMethods)
}

func registerVersion(c *fakerados.Cluster) {
	c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
}

func registerUser(c *fakerados.Cluster) {
	c.RegisterClass("user", fakerados.UserClass(), fakerados.UserWriteMethods...)
}

func registerLock(c *fakerados.Cluster) {
	c.RegisterClass(lock.Class, fakerados.LockClass(), fakerados.LockWriteMethods...)
}

func registerRefcount(c *fakerados.Cluster) {
	c.RegisterClass("refcount", fakerados.RefcountClass(), fakerados.RefcountWriteMethods...)
}

func registerGCQueue(c *fakerados.Cluster) {
	c.RegisterClass(gc.Class, fakerados.GCQueueClass(), fakerados.GCQueueWriteMethods...)
}

func registerOTP(c *fakerados.Cluster) {
	c.RegisterClassMethods(otp.Class, fakerados.OTPClass(), fakerados.OTPMethods, fakerados.OTPWriteMethods)
}

// The OSD bumps an object's version for every op whose transaction is not
// empty. A CLS_METHOD_WR call sets user_modify; prepare_transaction treats the
// op as a no-op only while op_t is empty and nothing set modify; and
// finish_ctx then takes the next user_version
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L8873-L8971 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8979-L9075 at v20.2.4).
// Each omap or xattr write a class sends goes into op_t whatever it leaves
// in the object, so a write call that changes nothing still bumps the
// version. A call that sends no write leaves op_t empty and the version
// alone.
var _ = Describe("A class call's version bump", func() {
	type outcome struct {
		Bumped    uint64
		Unchanged bool
	}
	run := func(ctx context.Context, release string, cc classCase) outcome {
		GinkgoHelper()
		c := fakerados.New()
		c.SetRequiredOSDRelease(release)
		cc.register(c)
		p, err := c.Pool(ctx, poolName, ns)
		Expect(err).NotTo(HaveOccurred())
		cc.stage(ctx, c, p)
		before := c.Object(poolName, ns, writeVersionOID)
		Expect(before).NotTo(BeNil(), "the stage leaves the object to call")
		Expect(cc.call(ctx, p)).To(Succeed())
		after := c.Object(poolName, ns, writeVersionOID)
		Expect(after).NotTo(BeNil())
		return outcome{Bumped: after.Version - before.Version, Unchanged: reflect.DeepEqual(contentOf(before), contentOf(after))}
	}
	// outcomes runs cc on a Squid-mode fake and on a Tentacle-mode one,
	// whose OSDs bump the version alike.
	outcomes := func(ctx context.Context, cc classCase) map[string]outcome {
		GinkgoHelper()
		out := map[string]outcome{}
		for _, release := range []string{"squid", "tentacle"} {
			out[release] = run(ctx, release, cc)
		}
		return out
	}
	both := func(o outcome) map[string]outcome { return map[string]outcome{"squid": o, "tentacle": o} }

	DescribeTable("bumps the version for a write call that leaves the object as it was",
		func(ctx SpecContext, cc classCase) {
			Expect(outcomes(ctx, cc)).To(Equal(both(outcome{Bumped: 1, Unchanged: true})))
		},
		Entry("rgw lc_set_entry of the entry the shard holds", classCase{
			register: registerRGW,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { rgwcls.LCSetEntry(op, wvLCEntry()) }),
			call:     writeOn(func(op *radosclient.WriteOp) { rgwcls.LCSetEntry(op, wvLCEntry()) }),
		}),
		Entry("rgw lc_rm_entry of a bucket the shard lacks", classCase{
			register: registerRGW,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { rgwcls.LCSetEntry(op, wvLCEntry()) }),
			call:     writeOn(func(op *radosclient.WriteOp) { rgwcls.LCRmEntry(op, rgwcls.LCEntry{Bucket: ":b:zone.4127.2"}) }),
		}),
		Entry("rgw lc_put_head of the head the shard holds", classCase{
			register: registerRGW,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { rgwcls.LCPutHead(op, wvLCHead()) }),
			call:     writeOn(func(op *radosclient.WriteOp) { rgwcls.LCPutHead(op, wvLCHead()) }),
		}),
		Entry("rgw bucket_clear_olh of a name without an OLH entry, under the empty tag it then matches", classCase{
			// rgw_bucket_clear_olh removes the OLH data key once the tag
			// matches, whether or not the shard holds it, through
			// cls_cxx_map_remove_key and, at v20.2.4, remove_entry
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2144-L2163 at v19.2.7,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2309-L2328 and
			// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L772-L787 at v20.2.4).
			register: registerRGW,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { rgwcls.LCSetEntry(op, wvLCEntry()) }),
			call: writeOn(func(op *radosclient.WriteOp) {
				rgwcls.ClearOLH(op, rgwcls.ClearOLHOp{Key: rgwcls.ObjKey{Name: "k"}}, denc.Squid)
			}),
		}),
		Entry("version set of the version the object holds", classCase{
			register: registerVersion,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { version.Set(op, wvObjVersion(), denc.Squid) }),
			call:     writeOn(func(op *radosclient.WriteOp) { version.Set(op, wvObjVersion(), denc.Squid) }),
		}),
		Entry("user complete_stats_sync dated before the header's last sync", classCase{
			register: registerUser,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { user.CompleteStatsSync(op, wvSyncTime(), denc.Squid) }),
			call:     writeOn(func(op *radosclient.WriteOp) { user.CompleteStatsSync(op, wvSyncTime().Add(-time.Hour), denc.Squid) }),
		}),
		Entry("lock renewed by its holder", classCase{
			register: registerLock,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { lock.Lock(op, wvRenew(), denc.Squid) }),
			call:     writeOn(func(op *radosclient.WriteOp) { lock.Lock(op, wvRenew(), denc.Squid) }),
		}),
		Entry("refcount get of a tag the object holds", classCase{
			register: registerRefcount,
			stage:    stageWith(true, func(op *radosclient.WriteOp) { refcount.Get(op, "a", false, denc.Squid) }),
			call:     writeOn(func(op *radosclient.WriteOp) { refcount.Get(op, "a", false, denc.Squid) }),
		}),
		Entry("rgw_gc queue init of a shard without a queue, whose head the fake keeps nowhere", classCase{
			register: registerGCQueue,
			stage:    stageWith(true),
			call:     writeOn(func(op *radosclient.WriteOp) { gc.QueueInit(op, 1<<20, 0, denc.Squid) }),
		}),
		Entry("rgw_gc remove_entries of an emptied queue", classCase{
			register: registerGCQueue,
			stage: stageWith(true,
				func(op *radosclient.WriteOp) { gc.QueueEnqueue(op, 0, wvGCInfo(), denc.Squid) },
				func(op *radosclient.WriteOp) { gc.QueueRemoveEntries(op, 1, denc.Squid) }),
			call: writeOn(func(op *radosclient.WriteOp) { gc.QueueRemoveEntries(op, 1, denc.Squid) }),
		}),
		Entry("otp_set of the device the object holds", classCase{
			register: registerOTP,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { otp.Set(op, []otp.OTPInfo{otpDev1()}, denc.Squid) }),
			call:     writeOn(func(op *radosclient.WriteOp) { otp.Set(op, []otp.OTPInfo{otpDev1()}, denc.Squid) }),
		}),
	)

	DescribeTable("leaves the version for a read call",
		func(ctx SpecContext, cc classCase) {
			Expect(outcomes(ctx, cc)).To(Equal(both(outcome{Unchanged: true})))
		},
		Entry("rgw lc_get_head", classCase{
			register: registerRGW,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { rgwcls.LCPutHead(op, wvLCHead()) }),
			call:     readOn(func(op *radosclient.ReadOp) { rgwcls.LCGetHead(op) }),
		}),
		Entry("rgw lc_get_head in a write op, which the OSD serves as a read", classCase{
			register: registerRGW,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { rgwcls.LCPutHead(op, wvLCHead()) }),
			call:     writeOn(func(op *radosclient.WriteOp) { op.Exec(rgwcls.Class, "lc_get_head", nil) }),
		}),
		Entry("version read", classCase{
			register: registerVersion,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { version.Set(op, wvObjVersion(), denc.Squid) }),
			call:     readOn(func(op *radosclient.ReadOp) { version.Read(op, denc.Squid) }),
		}),
		Entry("user get_header", classCase{
			register: registerUser,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { user.CompleteStatsSync(op, wvSyncTime(), denc.Squid) }),
			call:     readOn(func(op *radosclient.ReadOp) { user.GetHeader(op, denc.Squid) }),
		}),
		Entry("lock get_info", classCase{
			register: registerLock,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { lock.Lock(op, wvRenew(), denc.Squid) }),
			call:     readOn(func(op *radosclient.ReadOp) { lock.GetInfo(op, wvRenew().Name, denc.Squid) }),
		}),
		Entry("refcount read", classCase{
			register: registerRefcount,
			stage:    stageWith(true, func(op *radosclient.WriteOp) { refcount.Get(op, "a", false, denc.Squid) }),
			call:     readOn(func(op *radosclient.ReadOp) { refcount.Read(op, false, denc.Squid) }),
		}),
		Entry("rgw_gc list_entries", classCase{
			register: registerGCQueue,
			stage:    stageWith(true, func(op *radosclient.WriteOp) { gc.QueueEnqueue(op, 0, wvGCInfo(), denc.Squid) }),
			call:     readOn(func(op *radosclient.ReadOp) { gc.QueueList(op, "", 0, false, denc.Squid) }),
		}),
		Entry("otp get_all", classCase{
			register: registerOTP,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { otp.Set(op, []otp.OTPInfo{otpDev1()}, denc.Squid) }),
			call:     readOn(func(op *radosclient.ReadOp) { otp.GetAll(op, denc.Squid) }),
		}),
	)

	DescribeTable("leaves the version for a write method that sends no write",
		func(ctx SpecContext, cc classCase) {
			Expect(outcomes(ctx, cc)).To(Equal(both(outcome{Unchanged: true})))
		},
		Entry("rgw usage_log_clear of an object without omap, which OMAPCLEAR leaves out of the transaction", classCase{
			register: registerRGW,
			stage: func(_ context.Context, c *fakerados.Cluster, _ radosclient.Pool) {
				c.Put(poolName, ns, writeVersionOID, []byte("d"))
			},
			call: writeOn(func(op *radosclient.WriteOp) { rgwcls.UsageLogClear(op) }),
		}),
		Entry("rgw gc_remove of a tag the shard lacks", classCase{
			register: registerRGW,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { rgwcls.LCSetEntry(op, wvLCEntry()) }),
			call:     writeOn(func(op *radosclient.WriteOp) { rgwcls.GCRemove(op, []string{"absent"}, denc.Squid) }),
		}),
		Entry("user remove_bucket of a bucket the index lacks", classCase{
			register: registerUser,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { user.CompleteStatsSync(op, wvSyncTime(), denc.Squid) }),
			call:     writeOn(func(op *radosclient.WriteOp) { user.RemoveBucket(op, user.Bucket{Name: "absent"}, denc.Squid) }),
		}),
		Entry("refcount put of a tag the object does not hold", classCase{
			register: registerRefcount,
			stage:    stageWith(true, func(op *radosclient.WriteOp) { refcount.Get(op, "a", false, denc.Squid) }),
			call:     writeOn(func(op *radosclient.WriteOp) { refcount.Put(op, "b", false, denc.Squid) }),
		}),
		Entry("otp_remove of a device the object lacks", classCase{
			register: registerOTP,
			stage:    stageWith(false, func(op *radosclient.WriteOp) { otp.Set(op, []otp.OTPInfo{otpDev1()}, denc.Squid) }),
			call:     writeOn(func(op *radosclient.WriteOp) { otp.Remove(op, []string{"dev2"}, denc.Squid) }),
		}),
	)
})

// ClassCall's writes, through a class of the spec's own. CEPH_OSD_OP_CALL
// fails a method without CLS_METHOD_WR once it has sent a write, counted in
// num_write
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6170-L6185 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6239-L6254 at v20.2.4).
// CEPH_OSD_OP_DELETE counts its write before _delete_oid finds the object
// missing
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6952-L6959 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7021-L7028 at v20.2.4),
// and CEPH_OSD_OP_OMAPRMKEYS counts its write before it answers ENOENT
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7959-L7972 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8036-L8049 at v20.2.4).
var _ = Describe("ClassCall's writes", func() {
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		methods := func(call *fakerados.ClassCall) ([]byte, int32) {
			switch call.Method {
			case "create", "peek_create":
				call.Create().Xattrs["x"] = []byte("1")
			case "update", "peek_update":
				if obj := call.Update(); obj != nil {
					delete(obj.Omap, "absent")
				}
			case "remove", "peek_remove":
				call.Remove()
			}
			return nil, 0
		}
		c.RegisterClass("spec", methods, "create", "update", "remove")
	})
	stored := func() *fakerados.Object { return c.Object(poolName, ns, "obj") }
	exec := func(ctx context.Context, build func(op *radosclient.WriteOp)) (uint64, error) {
		return write(ctx, p, "obj", build)
	}

	DescribeTable("bumps the version for a write that leaves the object as it was",
		func(ctx SpecContext, method string) {
			Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				op.SetXattr("x", []byte("1"))
			})).To(Succeed())
			before := stored()
			ver, err := exec(ctx, func(op *radosclient.WriteOp) { op.Exec("spec", method, nil) })
			Expect(err).NotTo(HaveOccurred())
			Expect(ver).To(Equal(before.Version+1), "the version the op leaves")
			Expect(stored().Version).To(Equal(before.Version + 1))
			Expect(contentOf(stored())).To(Equal(contentOf(before)))
		},
		Entry("Create, setting a value the object holds", "create"),
		Entry("Update, removing a key the object lacks", "update"),
	)

	It("sends no write through Update on a missing object, so no version is handed out", func(ctx SpecContext) {
		ver, err := exec(ctx, func(op *radosclient.WriteOp) { op.Exec("spec", "update", nil) })
		Expect(err).NotTo(HaveOccurred())
		Expect(ver).To(BeZero(), "a no-op on a missing object")
		Expect(stored()).To(BeNil())
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Create(true) })).To(Succeed())
		Expect(stored().Version).To(Equal(uint64(1)), "the first version the object is handed")
	})

	DescribeTable("fails a method without the WR flag that sends a write with EIO, whatever the write leaves",
		func(ctx SpecContext, method string, removeFirst bool) {
			Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				op.SetXattr("x", []byte("1"))
			})).To(Succeed())
			before := stored()
			_, err := exec(ctx, func(op *radosclient.WriteOp) {
				if removeFirst {
					op.Remove()
				}
				op.Exec("spec", method, nil)
			})
			Expect(err).To(haveErrno(syscall.EIO))
			Expect(stored()).To(Equal(before), "the failed op applies nothing")
		},
		Entry("Create, setting a value the object holds", "peek_create", false),
		Entry("Update, removing a key the object lacks", "peek_update", false),
		Entry("Update of an object the op removed, as OMAPRMKEYS counts the write before it finds the object gone", "peek_update", true),
		Entry("Remove of an object the op removed, as DELETE counts the write before it finds the object gone", "peek_remove", true),
	)
})

// What a class call stages on a path that fails, or after an earlier step of
// its op removed the object. Only an op that sets FAILOK on the call, or
// removes the object ahead of it, sees the difference, and no rgw-go caller
// sends either.
var _ = Describe("A class call's writes on its failing paths", func() {
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
	})
	stored := func() *fakerados.Object { return c.Object(poolName, ns, writeVersionOID) }

	It("keeps the version init_version writes when inc_conds then fails its condition under FAILOK", func(ctx SpecContext) {
		registerVersion(c)
		c.Put(poolName, ns, writeVersionOID, nil)
		ver := stored().Version
		Expect(writeErr(ctx, p, writeVersionOID, func(op *radosclient.WriteOp) {
			version.IncConds(op, version.ObjVersion{Ver: 7, Tag: "t"}, version.CondEQ, denc.Squid)
			op.SetStepFlags(radosclient.StepFlagFailOK)
		})).To(Succeed(), "FAILOK keeps the op going past ECANCELED")
		Expect(stored().Version).To(Equal(ver + 1))
		Expect(stored().Xattrs).To(HaveKey(version.XattrName))
		v := decodeVersion(stored().Xattrs[version.XattrName])
		Expect(v.Ver).To(Equal(uint64(1)), "init_version's version, never incremented")
		Expect(v.Tag).To(HaveLen(24), "TAG_LEN characters of cls_gen_rand_base64")
	})

	It("sends no write from rgw_gc_queue_remove_entries that cannot read the queue's front, under FAILOK", func(ctx SpecContext) {
		registerGCQueue(c)
		c.Put(poolName, ns, writeVersionOID, nil)
		stored().Xattrs[fakerados.GCQueueFrontXattr] = []byte{1}
		before := contentOf(stored())
		ver := stored().Version
		Expect(writeErr(ctx, p, writeVersionOID, func(op *radosclient.WriteOp) {
			gc.QueueRemoveEntries(op, 1, denc.Squid)
			op.SetStepFlags(radosclient.StepFlagFailOK)
		})).To(Succeed(), "FAILOK keeps the op going past EIO")
		Expect(stored().Version).To(Equal(ver), "the class reads the head before it writes")
		Expect(contentOf(stored())).To(Equal(before))
	})

	DescribeTable("fails a removal on an object an earlier step removed with ENOENT, rather than creating it again",
		func(ctx SpecContext, stage, remove func(op *radosclient.WriteOp)) {
			registerUser(c)
			Expect(writeErr(ctx, p, writeVersionOID, stage)).To(Succeed())
			before := stored()
			Expect(writeErr(ctx, p, writeVersionOID, func(op *radosclient.WriteOp) {
				op.Remove()
				remove(op)
			})).To(MatchError(radosclient.ErrNotFound), "remove_entry's OMAPRMKEYS on a missing object")
			Expect(stored()).To(Equal(before), "the failed op applies nothing")
		},
		Entry("user remove_bucket",
			func(op *radosclient.WriteOp) {
				user.SetBucketsInfo(op, []user.BucketEntry{{Bucket: user.Bucket{Name: "b"}}}, true, wvSyncTime(), denc.Squid)
			},
			func(op *radosclient.WriteOp) { user.RemoveBucket(op, user.Bucket{Name: "b"}, denc.Squid) }),
		Entry("user account_resource_rm",
			func(op *radosclient.WriteOp) {
				user.AccountResourceAdd(op, user.AccountResource{Name: "r", Path: "/"}, false, 10, denc.Squid)
			},
			func(op *radosclient.WriteOp) { user.AccountResourceRm(op, "r", denc.Squid) }),
	)
})
