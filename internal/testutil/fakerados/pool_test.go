package fakerados_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

const (
	poolName = "zone.rgw.meta"
	ns       = "root"
)

// newCluster returns a fresh cluster and a handle on poolName/ns.
func newCluster(ctx context.Context) (*fakerados.Cluster, radosclient.Pool) {
	GinkgoHelper()
	c := fakerados.New()
	p, err := c.Pool(ctx, poolName, ns)
	Expect(err).NotTo(HaveOccurred())
	return c, p
}

// usersUID is a users.uid namespace in the order RADOS lists it, which
// neither the names nor their unreversed hashes follow: by the bit-reversed
// placement hash ceph_str_hash_rjenkins gives each name, then by name.
//
//	dave 0x0694dfdd, alice 0x06a04cf5, alice.buckets 0x1f5ca3c3,
//	user-318489 and user-326802 0x259305f8, bob 0x954708d3,
//	carol 0xbcfb659a, user-155108 and user-327421 0xc4707780,
//	erin 0xf72c4da3.
var usersUID = []string{
	"dave", "alice", "alice.buckets", "user-318489", "user-326802",
	"bob", "carol", "user-155108", "user-327421", "erin",
}

// putUsersUID stores usersUID's objects in poolName's users.uid namespace,
// and an object in the default namespace that no listing of it may show.
func putUsersUID(c *fakerados.Cluster) {
	for _, oid := range usersUID {
		c.Put(poolName, "users.uid", oid, []byte("x"))
	}
	c.Put(poolName, "", "elsewhere", nil)
}

// collect is a listing callback that appends each name to oids.
func collect(oids *[]string) func(oid, locator string) error {
	return func(oid, _ string) error {
		*oids = append(*oids, oid)
		return nil
	}
}

// write runs the write op build composes on oid.
func write(ctx context.Context, p radosclient.Pool, oid string, build func(op *radosclient.WriteOp)) (uint64, error) {
	op := radosclient.NewWriteOp()
	build(op)
	return p.Write(ctx, oid, op, radosclient.OpFlagNone)
}

// writeErr is write's error alone.
func writeErr(ctx context.Context, p radosclient.Pool, oid string, build func(op *radosclient.WriteOp)) error {
	_, err := write(ctx, p, oid, build)
	return err
}

// haveErrno matches a *radosclient.Error carrying errno n, for the errnos
// the seam has no sentinel for.
func haveErrno(n syscall.Errno) types.GomegaMatcher {
	return WithTransform(func(err error) int32 {
		e, ok := errors.AsType[*radosclient.Error](err)
		if !ok {
			return -1
		}
		return e.Errno
	}, Equal(int32(n)))
}

func encodeVersion(v version.ObjVersion) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

func decodeVersion(b []byte) version.ObjVersion {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := version.DecodeObjVersion(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return v
}

// versionClass is as much of cls_version as the overwrite specs need:
// check_conds with EQ and an unconditional inc. It reads the version through
// Stored, as cls_version's cls_cxx_getxattr reads the store, and writes
// through the op's object.
func versionClass(call *fakerados.ClassCall) ([]byte, int32) {
	var cur version.ObjVersion
	if call.Stored != nil {
		if b, ok := call.Stored.Xattrs[version.XattrName]; ok {
			cur = version.DecodeObjVersion(denc.NewDecoder(b))
		}
	}
	switch call.Method {
	case "check_conds":
		op := version.DecodeCheckOp(denc.NewDecoder(call.In))
		for _, cond := range op.Conds {
			if cond.Cond == version.CondEQ && cond.Ver != cur {
				return nil, -int32(syscall.ECANCELED)
			}
		}
		return nil, 0
	case "inc":
		cur.Ver++
		call.Create().Xattrs[version.XattrName] = encodeVersion(cur)
		return nil, 0
	}
	return nil, -int32(syscall.EOPNOTSUPP)
}

var _ = Describe("a write op", func() {
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
	})
	stored := func(oid string) *fakerados.Object { return c.Object(poolName, ns, oid) }

	It("refuses an exclusive create of an existing object with ErrExists and leaves the object", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("old"))
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Create(true)
			op.WriteFull([]byte("new"))
		})
		Expect(err).To(MatchError(radosclient.ErrExists))
		Expect(stored("obj").Data).To(Equal([]byte("old")))
	})

	It("applies none of its steps when a later step fails", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("old"))
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.WriteFull([]byte("new"))
			op.SetXattr("a", []byte("1"))
			op.CmpXattr("missing", radosclient.CmpEQ, []byte("x"))
		})
		Expect(err).To(MatchError(radosclient.ErrCanceled))
		obj := stored("obj")
		Expect(obj.Data).To(Equal([]byte("old")))
		Expect(obj.Xattrs).NotTo(HaveKey("a"))
		Expect(obj.Version).To(BeEquivalentTo(1))
	})

	It("lets the step a FailOK flag follows fail and goes on, and only that step", func(ctx SpecContext) {
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Remove()
			op.SetStepFlags(radosclient.StepFlagFailOK)
			op.WriteFull([]byte("v"))
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(stored("obj").Data).To(Equal([]byte("v")))
		err = writeErr(ctx, p, "gone", func(op *radosclient.WriteOp) {
			op.Remove()
			op.SetStepFlags(radosclient.StepFlagFailOK)
			op.Remove()
		})
		Expect(err).To(MatchError(radosclient.ErrNotFound), "the flag covers the step before it alone")
	})

	It("reports a FailOK'd class failure in its result while the op succeeds", func(ctx SpecContext) {
		c.RegisterClass("probe", func(*fakerados.ClassCall) ([]byte, int32) { return nil, -int32(syscall.EBUSY) })
		var res *radosclient.ExecResult
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			res = op.Exec("probe", "m", nil)
			op.SetStepFlags(radosclient.StepFlagFailOK)
			op.WriteFull([]byte("v"))
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = res.Bytes()
		Expect(err).To(haveErrno(syscall.EBUSY))
	})

	It("fails a flags step with no step before it with ErrBadOp and runs nothing", func(ctx SpecContext) {
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.SetStepFlags(radosclient.StepFlagFailOK)
			op.WriteFull([]byte("v"))
		})
		Expect(err).To(MatchError(radosclient.ErrBadOp))
		Expect(stored("obj")).To(BeNil())
	})

	It("lets the flags that follow a create replace its exclusivity, as librados's set_last_op_flags does", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("old"))
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Create(true)
			op.SetStepFlags(radosclient.StepFlagFAdviseDontNeed)
		})
		Expect(err).NotTo(HaveOccurred(), "the flags dropped the create's EXCL")
		err = writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Create(false)
			op.SetStepFlags(radosclient.StepFlagExcl)
		})
		Expect(err).To(MatchError(radosclient.ErrExists), "an EXCL flag makes a create exclusive")
	})

	It("composes radosgw's overwrite of an absent object, remove with FailOK then create, into an empty object", func(ctx SpecContext) {
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Remove()
			op.SetStepFlags(radosclient.StepFlagFailOK)
			op.Create(false)
		})
		Expect(err).NotTo(HaveOccurred())
		obj := stored("obj")
		Expect(obj).NotTo(BeNil())
		Expect(obj.Data).To(BeEmpty())
		Expect(obj.Xattrs).To(BeEmpty())
		Expect(obj.Omap).To(BeEmpty())
	})

	It("increments the version once per write op that changes the object and reports it", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("a"))
		Expect(stored("obj").Version).To(BeEquivalentTo(1), "Put seeds version 1")
		v, err := write(ctx, p, "obj", func(op *radosclient.WriteOp) { op.WriteFull([]byte("b")) })
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(BeEquivalentTo(2))
		v, err = write(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.SetXattr("x", []byte("1"))
			op.OmapSet(map[string][]byte{"k": []byte("v")})
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(BeEquivalentTo(3), "one increment per op, not per step")
		v, err = write(ctx, p, "obj", func(op *radosclient.WriteOp) { op.AssertExists() })
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(BeEquivalentTo(3), "a write op that changes nothing leaves the version")
		Expect(stored("obj").Version).To(BeEquivalentTo(3))
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Remove() })).To(Succeed())
		v, err = write(ctx, p, "obj", func(op *radosclient.WriteOp) { op.WriteFull([]byte("c")) })
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(BeNumerically(">", 3), "a recreated object's version is past the removed one's")
	})

	Describe("the object a step reads", func() {
		var storedVer version.ObjVersion
		BeforeEach(func() {
			storedVer = version.ObjVersion{Ver: 3, Tag: "t"}
			c.RegisterClass("version", versionClass, "inc")
			c.Put(poolName, ns, "obj", []byte("old"))
			stored("obj").Xattrs[version.XattrName] = encodeVersion(storedVer)
		})
		// overwrite is radosgw's non-exclusive system-object write
		// (svc_sys_obj_core.cc:496-502 at v19.2.6).
		overwrite := func(op *radosclient.WriteOp) {
			op.Remove()
			op.SetStepFlags(radosclient.StepFlagFailOK)
			op.Create(false)
		}

		It("checks and increments the stored class version across radosgw's overwrite", func(ctx SpecContext) {
			err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				overwrite(op)
				version.Check(op, storedVer, version.CondEQ, denc.Squid)
				version.Inc(op, denc.Squid)
				op.WriteFull([]byte("new"))
			})
			Expect(err).NotTo(HaveOccurred())
			obj := stored("obj")
			Expect(obj.Data).To(Equal([]byte("new")))
			Expect(decodeVersion(obj.Xattrs[version.XattrName])).To(Equal(version.ObjVersion{Ver: 4, Tag: "t"}))
		})

		It("fails radosgw's overwrite with ECANCELED at another version and leaves the object", func(ctx SpecContext) {
			err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				overwrite(op)
				version.Check(op, version.ObjVersion{Ver: 2, Tag: "t"}, version.CondEQ, denc.Squid)
				version.Inc(op, denc.Squid)
				op.WriteFull([]byte("new"))
			})
			Expect(err).To(MatchError(radosclient.ErrCanceled))
			obj := stored("obj")
			Expect(obj.Data).To(Equal([]byte("old")))
			Expect(decodeVersion(obj.Xattrs[version.XattrName])).To(Equal(storedVer))
		})

		It("serves a class read from the stored object after a SetXattr of the same name", func(ctx SpecContext) {
			err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				op.SetXattr(version.XattrName, encodeVersion(version.ObjVersion{Ver: 9, Tag: "x"}))
				version.Check(op, storedVer, version.CondEQ, denc.Squid)
			})
			Expect(err).NotTo(HaveOccurred(), "the class saw the stored version, not the op's")
		})

		It("compares CmpXattr's value, on the left, with the stored xattr", func(ctx SpecContext) {
			stored("obj").Xattrs["k"] = []byte("m")
			cmp := func(op radosclient.CmpOp, v string) error {
				return writeErr(ctx, p, "obj", func(w *radosclient.WriteOp) { w.CmpXattr("k", op, []byte(v)) })
			}
			Expect(cmp(radosclient.CmpGT, "z")).To(Succeed(), "z > m")
			Expect(cmp(radosclient.CmpGT, "a")).To(MatchError(radosclient.ErrCanceled), "a > m is false")
			Expect(cmp(radosclient.CmpLTE, "m")).To(Succeed())
			Expect(writeErr(ctx, p, "obj", func(w *radosclient.WriteOp) {
				w.SetXattr("k", []byte("q"))
				w.CmpXattr("k", radosclient.CmpEQ, []byte("m"))
			})).To(Succeed(), "the stored m, not the op's q")
			Expect(writeErr(ctx, p, "obj", func(w *radosclient.WriteOp) {
				w.CmpXattr("absent", radosclient.CmpEQ, nil)
			})).To(Succeed(), "a missing xattr compares as empty")
		})

		It("checks AssertVersion against the version the object had before the op", func(ctx SpecContext) {
			assert := func(oid string, v uint64) error {
				return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) {
					op.WriteFull([]byte("x"))
					op.AssertVersion(v)
				})
			}
			Expect(assert("obj", 1)).To(Succeed(), "the op's own write does not move the version it asserts")
			Expect(assert("obj", 1)).To(MatchError(radosclient.ErrRange), "the stored version, 2, is larger")
			Expect(assert("obj", 3)).To(haveErrno(syscall.EOVERFLOW))
			Expect(assert("obj", 0)).To(MatchError(radosclient.ErrInvalid))
			Expect(assert("absent", 1)).To(haveErrno(syscall.EOVERFLOW), "an absent object is at version 0")
		})
	})

	It("compares omap values with EQ, LT and GT only, the stored value on the left", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", nil)
		stored("obj").Omap["k"] = []byte("m")
		cmp := func(key string, op radosclient.CmpOp, v string) error {
			return writeErr(ctx, p, "obj", func(w *radosclient.WriteOp) { w.OmapCmp(key, op, []byte(v)) })
		}
		Expect(cmp("k", radosclient.CmpLT, "z")).To(Succeed(), "m < z")
		Expect(cmp("k", radosclient.CmpGT, "a")).To(Succeed(), "m > a")
		Expect(cmp("k", radosclient.CmpLT, "a")).To(MatchError(radosclient.ErrCanceled))
		Expect(cmp("k", radosclient.CmpNE, "a")).To(MatchError(radosclient.ErrInvalid), "OMAP_CMP serves EQ, LT and GT")
		Expect(cmp("absent", radosclient.CmpEQ, "")).To(Succeed(), "a missing key compares as empty")
		Expect(writeErr(ctx, p, "obj", func(w *radosclient.WriteOp) {
			w.OmapSet(map[string][]byte{"k": []byte("q")})
			w.OmapCmp("k", radosclient.CmpEQ, []byte("m"))
		})).To(Succeed(), "the stored m, not the op's q")
		Expect(writeErr(ctx, p, "absent", func(w *radosclient.WriteOp) {
			w.OmapCmp("k", radosclient.CmpEQ, nil)
		})).To(MatchError(radosclient.ErrNotFound))
	})

	It("fails the op with the errno a class method returns and applies none of its writes", func(ctx SpecContext) {
		c.RegisterClass("probe", func(call *fakerados.ClassCall) ([]byte, int32) {
			call.Create().Data = []byte("x")
			return nil, -int32(syscall.EBUSY)
		}, "m")
		var res *radosclient.ExecResult
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { res = op.Exec("probe", "m", nil) })
		Expect(err).To(haveErrno(syscall.EBUSY))
		_, resErr := res.Bytes()
		Expect(resErr).To(haveErrno(syscall.EBUSY), "the op's error reaches every result")
		Expect(stored("obj")).To(BeNil())
	})

	It("reports a write op's class call by its return value alone, as librados's C write op does", func(ctx SpecContext) {
		c.RegisterClass("probe", func(call *fakerados.ClassCall) ([]byte, int32) {
			call.Create().Data = []byte(call.Method)
			return []byte("out"), 7
		}, "m")
		var res *radosclient.ExecResult
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { res = op.Exec("probe", "m", nil) })
		Expect(err).NotTo(HaveOccurred())
		out, err := res.Bytes()
		Expect(err).NotTo(HaveOccurred(), "a positive return is success")
		Expect(out).To(BeEmpty())
		Expect(stored("obj").Data).To(Equal([]byte("m")), "the class created the object")
	})

	It("fails an op calling a class no emulator serves with EOPNOTSUPP before running a step", func(ctx SpecContext) {
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.WriteFull([]byte("v"))
			op.Exec("nope", "m", nil)
		})
		Expect(err).To(MatchError(radosclient.ErrNotSupported))
		Expect(stored("obj")).To(BeNil())
	})

	It("hands a class the object with create and remove", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("old"))
		c.RegisterClass("probe", func(call *fakerados.ClassCall) ([]byte, int32) {
			switch call.Method {
			case "remove":
				Expect(call.Object()).NotTo(BeNil(), "the object exists when the class runs")
				call.Remove()
				Expect(call.Object()).To(BeNil())
			case "stat":
				if call.Object() == nil {
					return nil, -int32(syscall.ENOENT)
				}
			}
			return nil, 0
		}, "remove")
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Exec("probe", "remove", nil)
			op.Exec("probe", "stat", nil)
		})
		Expect(err).To(MatchError(radosclient.ErrNotFound), "a class's stat sees the op's removal")
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Exec("probe", "remove", nil) })).To(Succeed())
		Expect(stored("obj")).To(BeNil())
	})

	It("writes data as the OSD does: at an offset past the end, appended, zeroed and truncated", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Write([]byte("cd"), 2) })).To(Succeed())
		Expect(stored("obj").Data).To(Equal([]byte("\x00\x00cd")), "a gap reads as zeros")
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Append([]byte("ef")) })).To(Succeed())
		Expect(stored("obj").Data).To(Equal([]byte("\x00\x00cdef")))
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Zero(3, 100) })).To(Succeed())
		Expect(stored("obj").Data).To(Equal([]byte("\x00\x00c\x00\x00\x00")), "zeroing past the end does not grow it")
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Truncate(2) })).To(Succeed())
		Expect(stored("obj").Data).To(Equal([]byte("\x00\x00")))
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Truncate(4) })).To(Succeed())
		Expect(stored("obj").Data).To(Equal([]byte("\x00\x00\x00\x00")))
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Write(nil, 6) })).To(Succeed())
		Expect(stored("obj").Data).To(HaveLen(6), "an empty write past the end extends the object")
	})

	It("clears the omap and its header, removes keys, xattrs and the object", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", nil)
		obj := stored("obj")
		obj.Omap["a"], obj.Omap["b"], obj.OmapHdr = []byte("1"), []byte("2"), []byte("hdr")
		obj.Xattrs["x"] = []byte("1")
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.OmapRmKeys([]string{"a", "absent"})
			op.RmXattr("x")
			op.RmXattr("absent")
		})).To(Succeed())
		Expect(stored("obj").Omap).To(Equal(map[string][]byte{"b": []byte("2")}))
		Expect(stored("obj").Xattrs).To(BeEmpty())
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.OmapClear() })).To(Succeed())
		Expect(stored("obj").Omap).To(BeEmpty())
		Expect(stored("obj").OmapHdr).To(BeEmpty())
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Remove() })).To(Succeed())
		Expect(stored("obj")).To(BeNil())
	})

	DescribeTable("creates an absent object for a step that stores something",
		func(ctx SpecContext, build func(op *radosclient.WriteOp)) {
			Expect(writeErr(ctx, p, "obj", build)).To(Succeed())
			Expect(stored("obj")).NotTo(BeNil())
		},
		Entry("Create", func(op *radosclient.WriteOp) { op.Create(false) }),
		Entry("WriteFull", func(op *radosclient.WriteOp) { op.WriteFull(nil) }),
		Entry("Write", func(op *radosclient.WriteOp) { op.Write([]byte("x"), 0) }),
		Entry("Append", func(op *radosclient.WriteOp) { op.Append([]byte("x")) }),
		Entry("SetXattr", func(op *radosclient.WriteOp) { op.SetXattr("x", nil) }),
		Entry("OmapSet", func(op *radosclient.WriteOp) { op.OmapSet(map[string][]byte{"k": nil}) }),
		Entry("SetAllocHint", func(op *radosclient.WriteOp) { op.SetAllocHint(4<<20, 4<<20, 0) }),
	)

	DescribeTable("leaves an absent object absent for a step with nothing to change",
		func(ctx SpecContext, build func(op *radosclient.WriteOp)) {
			Expect(writeErr(ctx, p, "obj", build)).To(Succeed())
			Expect(stored("obj")).To(BeNil())
		},
		Entry("Truncate", func(op *radosclient.WriteOp) { op.Truncate(10) }),
		Entry("Zero", func(op *radosclient.WriteOp) { op.Zero(0, 10) }),
	)

	DescribeTable("fails a step that needs the object with ENOENT when it is absent",
		func(ctx SpecContext, build func(op *radosclient.WriteOp)) {
			Expect(writeErr(ctx, p, "obj", build)).To(MatchError(radosclient.ErrNotFound))
		},
		Entry("Remove", func(op *radosclient.WriteOp) { op.Remove() }),
		Entry("AssertExists", func(op *radosclient.WriteOp) { op.AssertExists() }),
		Entry("RmXattr", func(op *radosclient.WriteOp) { op.RmXattr("x") }),
		Entry("OmapRmKeys", func(op *radosclient.WriteOp) { op.OmapRmKeys([]string{"k"}) }),
		Entry("OmapClear", func(op *radosclient.WriteOp) { op.OmapClear() }),
		Entry("CmpXattr, which reads the store", func(op *radosclient.WriteOp) {
			op.Create(false)
			op.CmpXattr("x", radosclient.CmpEQ, nil)
		}),
	)

	It("stamps a changed object with the op's mtime, or the cluster's clock without one", func(ctx SpecContext) {
		clock := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
		c.SetClock(func() time.Time { return clock })
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.WriteFull(nil) })).To(Succeed())
		Expect(stored("obj").Mtime).To(Equal(clock))
		set := clock.Add(-time.Hour)
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.SetMtime(set)
			op.WriteFull(nil)
		})).To(Succeed())
		Expect(stored("obj").Mtime).To(Equal(set))
	})
})

var _ = Describe("a read op", func() {
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
	})

	It("fails on an absent object with ENOENT in every result when nothing in it writes", func(ctx SpecContext) {
		c.RegisterClass("probe", func(*fakerados.ClassCall) ([]byte, int32) { return []byte("out"), 0 })
		op := radosclient.NewReadOp()
		exec := op.Exec("probe", "m", nil)
		op.AssertVersion(1)
		_, err := p.Read(ctx, "absent", op, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "the OSD finds no object context, whatever the steps")
		_, execErr := exec.Bytes()
		Expect(execErr).To(MatchError(radosclient.ErrNotFound))
		op = radosclient.NewReadOp()
		read, stat := op.Read(0, 10), op.Stat()
		_, err = p.Read(ctx, "absent", op, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		Expect(read.Err).To(MatchError(radosclient.ErrNotFound))
		Expect(stat.Err).To(MatchError(radosclient.ErrNotFound))
	})

	It("keeps what a WR method writes in a read op, which the OSD serves as a write without an mtime", func(ctx SpecContext) {
		mtime := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
		c.SetClock(func() time.Time { return mtime })
		c.Put(poolName, ns, "obj", []byte("abc"))
		c.SetClock(func() time.Time { return mtime.Add(time.Hour) })
		c.RegisterClass("probe", func(call *fakerados.ClassCall) ([]byte, int32) {
			call.Create().Xattrs["x"] = call.In
			return []byte("done"), 0
		}, "set")
		op := radosclient.NewReadOp()
		res := op.Exec("probe", "set", []byte("1"))
		xattrs := op.GetXattrs()
		v, err := p.Read(ctx, "obj", op, radosclient.OpFlagReturnVec)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Bytes()).To(Equal([]byte("done")))
		Expect(xattrs.Xattrs).NotTo(HaveKey("x"), "a read in the same op sees the object as stored before it")
		obj := c.Object(poolName, ns, "obj")
		Expect(obj.Xattrs).To(HaveKeyWithValue("x", []byte("1")))
		Expect(v).To(BeEquivalentTo(2))
		Expect(obj.Version).To(BeEquivalentTo(2))
		Expect(obj.Mtime).To(Equal(mtime), "a read op carries no mtime")
		op = radosclient.NewReadOp()
		op.Exec("probe", "set", []byte("2"))
		_, err = p.Read(ctx, "created", op, radosclient.OpFlagReturnVec)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Object(poolName, ns, "created").Xattrs).To(HaveKeyWithValue("x", []byte("2")), "a class write creates the object")
	})

	It("counts the read ops run on each object with Reads, a failed one too, and no write op", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abc"))
		op := radosclient.NewReadOp()
		op.Stat()
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		op = radosclient.NewReadOp()
		op.Stat()
		_, err = p.Read(ctx, "absent", op, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		wop := radosclient.NewWriteOp()
		wop.WriteFull([]byte("x"))
		_, err = p.Write(ctx, "obj", wop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Reads(poolName, ns, "obj")).To(Equal(1), "obj")
		Expect(c.Reads(poolName, ns, "absent")).To(Equal(1), "absent")
		Expect(c.Reads(poolName, ns, "never")).To(BeZero(), "never")
	})

	It("counts the write ops run on each object with Writes, a failed one too, and keeps the last with its mtime and flags", func(ctx SpecContext) {
		mtime := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
		wop := radosclient.NewWriteOp()
		wop.WriteFull([]byte("x"))
		wop.SetMtime(mtime)
		_, err := p.Write(ctx, "obj", wop, radosclient.OpFlagFullTry)
		Expect(err).NotTo(HaveOccurred())
		wop = radosclient.NewWriteOp()
		wop.AssertExists()
		_, err = p.Write(ctx, "absent", wop, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		op := radosclient.NewReadOp()
		op.Stat()
		_, err = p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())

		Expect(c.Writes(poolName, ns, "obj")).To(Equal(1), "obj")
		Expect(c.Writes(poolName, ns, "absent")).To(Equal(1), "absent")
		Expect(c.Writes(poolName, ns, "never")).To(BeZero(), "never")
		last := c.LastWrite(poolName, ns, "obj")
		Expect(last.Steps()).To(HaveExactElements(BeAssignableToTypeOf(&radosclient.WriteFullStep{})))
		mt, stamped := last.Mtime()
		Expect([]any{mt, stamped}).To(Equal([]any{mtime, true}))
		Expect(last.Flags()).To(Equal(radosclient.OpFlagFullTry))
		failed := c.LastWrite(poolName, ns, "absent")
		Expect(failed.Steps()).To(HaveExactElements(BeAssignableToTypeOf(&radosclient.AssertExistsStep{})))
		_, stamped = failed.Mtime()
		Expect(stamped).To(BeFalse(), "an op without SetMtime carries none")
		Expect(c.LastWrite(poolName, ns, "never").Steps()).To(BeEmpty())
	})

	It("fails the next write op on an object with FailNextWrite's errno without applying it, and only that one", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("old"))
		c.FailNextWrite(poolName, ns, "obj", syscall.ETIMEDOUT)
		read := radosclient.NewReadOp()
		read.Stat()
		_, err := p.Read(ctx, "obj", read, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred(), "a read is not a write")
		wop := radosclient.NewWriteOp()
		wop.WriteFull([]byte("new"))
		_, err = p.Write(ctx, "other", wop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred(), "another object")
		_, err = p.Write(ctx, "obj", wop, radosclient.OpFlagFullTry)
		Expect(err).To(MatchError(radosclient.ErrTimedOut))
		Expect(c.Object(poolName, ns, "obj").Data).To(Equal([]byte("old")))
		Expect(c.Writes(poolName, ns, "obj")).To(Equal(1), "the failed op is counted")
		Expect(c.LastWrite(poolName, ns, "obj").Flags()).To(Equal(radosclient.OpFlagFullTry))
		Expect(c.WriteOrder(poolName, ns)).To(Equal([]string{"other"}), "the failed op is not among those that succeeded")
		_, err = p.Write(ctx, "obj", wop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Object(poolName, ns, "obj").Data).To(Equal([]byte("new")))
	})

	It("keeps the last read op run on each object, a failed one too, with LastRead", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abc"))
		op := radosclient.NewReadOp()
		op.Stat()
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		op = radosclient.NewReadOp()
		op.GetXattrs()
		_, err = p.Read(ctx, "obj", op, radosclient.OpFlagBalanceReads)
		Expect(err).NotTo(HaveOccurred())
		op = radosclient.NewReadOp()
		op.Stat()
		_, err = p.Read(ctx, "absent", op, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		last := c.LastRead(poolName, ns, "obj")
		Expect(last.Steps()).To(HaveExactElements(BeAssignableToTypeOf(&radosclient.GetXattrsStep{})))
		Expect(last.Flags()).To(Equal(radosclient.OpFlagBalanceReads))
		Expect(c.LastRead(poolName, ns, "absent").Steps()).To(HaveExactElements(BeAssignableToTypeOf(&radosclient.StatStep{})))
		Expect(c.LastRead(poolName, ns, "never").Steps()).To(BeEmpty())
	})

	It("forgets every read and write op so far with ResetCounters, keeping the objects", func(ctx SpecContext) {
		wop := radosclient.NewWriteOp()
		wop.WriteFull([]byte("x"))
		_, err := p.Write(ctx, "obj", wop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		op := radosclient.NewReadOp()
		op.Stat()
		_, err = p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		c.ResetCounters()
		Expect([]int{c.Reads(poolName, ns, "obj"), c.Writes(poolName, ns, "obj")}).To(Equal([]int{0, 0}))
		Expect(c.WritesTo(poolName, ns, "obj")).To(BeEmpty())
		Expect(c.LastWrite(poolName, ns, "obj").Steps()).To(BeEmpty())
		Expect(c.LastRead(poolName, ns, "obj").Steps()).To(BeEmpty())
		Expect(c.Object(poolName, ns, "obj").Data).To(Equal([]byte("x")))
		wop = radosclient.NewWriteOp()
		wop.Truncate(0)
		_, err = p.Write(ctx, "obj", wop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.WritesTo(poolName, ns, "obj")[0].Steps()).To(HaveExactElements(BeAssignableToTypeOf(&radosclient.TruncateStep{})),
			"the first op after a reset is the first recorded")
	})

	It("runs a BeforeWrite hook with the stored object before each write op on it, outside the cluster's lock", func(ctx SpecContext) {
		var seen []*fakerados.Object
		c.BeforeWrite(poolName, ns, "obj", func(o *fakerados.Object) {
			seen = append(seen, o)
			if o != nil {
				o.Xattrs["raced"] = []byte("1")
				c.Put(poolName, ns, "other", nil)
			}
		})
		create := func() error {
			return writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Create(true) })
		}
		Expect(create()).To(Succeed())
		Expect(seen).To(HaveExactElements(BeNil()), "the object did not exist")
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.CmpXattr("raced", radosclient.CmpEQ, nil)
			op.SetXattr("mine", []byte("1"))
		})).To(MatchError(radosclient.ErrCanceled), "the op meets what the hook staged")
		Expect(seen).To(HaveLen(2))
		Expect(c.Object(poolName, ns, "other")).NotTo(BeNil(), "the hook may call the cluster")
		op := radosclient.NewReadOp()
		op.Stat()
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(seen).To(HaveLen(2), "a read op runs no hook")
		c.BeforeWrite(poolName, ns, "obj", nil)
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.SetXattr("x", nil) })).To(Succeed())
		Expect(seen).To(HaveLen(2), "a nil hook removes it")
	})

	It("reports a pool's id with PoolID, the id its handles report", func(ctx SpecContext) {
		Expect(c.PoolID(poolName)).To(Equal(p.ID()))
		other, err := c.Pool(ctx, "other", "x")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.PoolID("other")).To(Equal(other.ID()))
		Expect(c.PoolID("other")).NotTo(Equal(p.ID()))
	})

	It("lists the write ops run on an object, oldest first, with WritesTo", func(ctx SpecContext) {
		for _, data := range []string{"a", "b"} {
			wop := radosclient.NewWriteOp()
			wop.WriteFull([]byte(data))
			_, err := p.Write(ctx, "obj", wop, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
		}
		writes := c.WritesTo(poolName, ns, "obj")
		Expect(writes).To(HaveLen(2))
		data := func(w fakerados.RecordedWrite) []byte {
			full, ok := w.Steps()[0].(*radosclient.WriteFullStep)
			Expect(ok).To(BeTrue(), "%T", w.Steps()[0])
			return full.Data
		}
		Expect(data(writes[0])).To(Equal([]byte("a")))
		Expect(data(writes[1])).To(Equal([]byte("b")))
		Expect(writes[1].Steps()).To(Equal(c.LastWrite(poolName, ns, "obj").Steps()))
		Expect(c.WritesTo(poolName, ns, "never")).To(BeEmpty())
	})

	It("names the objects of the write ops that succeeded in order with WriteOrder, until ResetCounters", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "b", func(op *radosclient.WriteOp) { op.WriteFull([]byte("1")) })).To(Succeed())
		Expect(writeErr(ctx, p, "a", func(op *radosclient.WriteOp) { op.WriteFull([]byte("2")) })).To(Succeed())
		Expect(writeErr(ctx, p, "absent", func(op *radosclient.WriteOp) { op.AssertExists() })).To(MatchError(radosclient.ErrNotFound))
		Expect(writeErr(ctx, p, "b", func(op *radosclient.WriteOp) { op.SetXattr("x", []byte("3")) })).To(Succeed())
		Expect(c.WriteOrder(poolName, ns)).To(Equal([]string{"b", "a", "b"}), "a failed op is left out")
		Expect(c.WriteOrder(poolName, "other")).To(BeEmpty())
		c.ResetCounters()
		Expect(c.WriteOrder(poolName, ns)).To(BeEmpty())
	})

	It("fails the next read op on an object with FailNextRead's errno, in every result, once", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abc"))
		c.FailNextRead(poolName, ns, "obj", syscall.EIO)
		op := radosclient.NewReadOp()
		read := op.Read(0, 3)
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).To(MatchError(ContainSubstring("input/output error")))
		Expect(read.Err).To(MatchError(err))
		Expect(c.Reads(poolName, ns, "obj")).To(Equal(1))
		op = radosclient.NewReadOp()
		read = op.Read(0, 3)
		_, err = p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(read.Data[:read.N]).To(Equal([]byte("abc")))
	})

	It("reads a range, a short tail and nothing past the end", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abcdef"))
		op := radosclient.NewReadOp()
		mid, tail, past := op.Read(1, 2), op.Read(4, 10), op.Read(9, 3)
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{mid.Data, mid.N}).To(Equal([]any{[]byte("bc"), 2}))
		Expect([]any{tail.Data, tail.N}).To(Equal([]any{[]byte("ef"), 2}))
		Expect(past.N).To(BeZero())
		Expect(past.Err).NotTo(HaveOccurred())
	})

	It("fails a zero-length read with ERANGE while data lies past the offset, and only that step", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abc"))
		op := radosclient.NewReadOp()
		whole, atEnd, ok := op.Read(0, 0), op.Read(3, 0), op.Read(0, 3)
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred(), "the op succeeds; librados fails the step's too-small buffer")
		Expect(whole.Err).To(MatchError(radosclient.ErrRange))
		Expect(atEnd.Err).NotTo(HaveOccurred())
		Expect(atEnd.N).To(BeZero())
		Expect(ok.Data).To(Equal([]byte("abc")))
	})

	It("reads into the caller's buffer", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abc"))
		buf := make([]byte, 8)
		op := radosclient.NewReadOp()
		res := op.ReadInto(1, buf)
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Data).To(Equal([]byte("bc")))
		Expect(buf[:2]).To(Equal([]byte("bc")), "the result shares the buffer")
	})

	It("stats the object, reads its xattrs and returns its version", func(ctx SpecContext) {
		mtime := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
		c.SetClock(func() time.Time { return mtime })
		c.Put(poolName, ns, "obj", []byte("abc"))
		c.Object(poolName, ns, "obj").Xattrs["user.rgw.x"] = []byte("1")
		op := radosclient.NewReadOp()
		stat, xattrs := op.Stat(), op.GetXattrs()
		v, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(v).To(BeEquivalentTo(1))
		Expect([]any{stat.Size, stat.ModTime}).To(Equal([]any{uint64(3), mtime}))
		Expect(xattrs.Xattrs).To(Equal(map[string][]byte{"user.rgw.x": []byte("1")}))
	})

	It("pages omap values after a marker and within a prefix, reporting More when entries remain", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", nil)
		for _, k := range []string{"a1", "b1", "b2", "b3", "c1"} {
			c.Object(poolName, ns, "obj").Omap[k] = []byte("v" + k)
		}
		page := func(after, prefix string, maxEntries uint64) *radosclient.OmapResult {
			op := radosclient.NewReadOp()
			res := op.OmapGetVals(after, prefix, maxEntries)
			_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			return res
		}
		first := page("", "b", 2)
		Expect(first.Values).To(Equal(map[string][]byte{"b1": []byte("vb1"), "b2": []byte("vb2")}))
		Expect(first.More).To(BeTrue())
		last := page("b2", "b", 2)
		Expect(last.Values).To(Equal(map[string][]byte{"b3": []byte("vb3")}))
		Expect(last.More).To(BeFalse(), "c1 lies outside the prefix")
		all := page("a1", "", 10)
		Expect(all.Values).To(HaveLen(4))
		Expect(all.Values).NotTo(HaveKey("a1"), "the marker is exclusive")
		none := page("", "b", 0)
		Expect(none.Values).To(BeEmpty())
		Expect(none.More).To(BeTrue(), "a zero page reports that entries remain")
	})

	It("caps an omap page at the OSD's default of 1024 entries", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", nil)
		for i := range 1100 {
			c.Object(poolName, ns, "obj").Omap[fmt.Sprintf("k%04d", i)] = nil
		}
		op := radosclient.NewReadOp()
		vals, keys := op.OmapGetVals("", "", 5000), op.OmapGetKeys("", 5000)
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(vals.Values).To(HaveLen(1024))
		Expect(vals.More).To(BeTrue())
		Expect(keys.Keys).To(HaveLen(1024))
		Expect(keys.More).To(BeTrue())
	})

	It("pages omap keys in order after a marker and reads values by key", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", nil)
		for _, k := range []string{"c", "a", "b"} {
			c.Object(poolName, ns, "obj").Omap[k] = []byte("v" + k)
		}
		op := radosclient.NewReadOp()
		keys := op.OmapGetKeys("a", 1)
		byKey := op.OmapGetValsByKeys([]string{"a", "c", "absent"})
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys.Keys).To(Equal([]string{"b"}))
		Expect(keys.More).To(BeTrue())
		Expect(byKey.Values).To(Equal(map[string][]byte{"a": []byte("va"), "c": []byte("vc")}))
	})

	It("hands a class the stored object and returns its output", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abc"))
		c.RegisterClass("probe", func(call *fakerados.ClassCall) ([]byte, int32) {
			return append(append([]byte(call.Method+":"), call.In...), call.Stored.Data...), 3
		})
		op := radosclient.NewReadOp()
		res := op.Exec("probe", "m", []byte("in:"))
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		out, err := res.Bytes()
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal([]byte("m:in:abc")))
	})

	Describe("an op whose class call writes, which the OSD serves as a write", func() {
		var calls int
		BeforeEach(func() {
			calls = 0
			c.Put(poolName, ns, "obj", []byte("abc"))
			c.RegisterClass("probe", func(call *fakerados.ClassCall) ([]byte, int32) {
				calls++
				if call.Method == "set" {
					call.Create().Xattrs["x"] = []byte("1")
				}
				return call.In, 0
			}, "set")
		})
		readOp := func(ctx context.Context, flags radosclient.OpFlags, out []byte) (*radosclient.ExecResult, *radosclient.ReadResult, error) {
			op := radosclient.NewReadOp()
			res := op.Exec("probe", "set", out)
			read := op.Read(0, 3)
			_, err := p.Read(ctx, "obj", op, flags)
			return res, read, err
		}

		It("drops every step's output without ReturnVec and keeps the write", func(ctx SpecContext) {
			res, read, err := readOp(ctx, radosclient.OpFlagNone, []byte("out"))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Bytes()).To(BeEmpty(), "the OSD clears a write's reply data (ignore_out_data)")
			Expect(read.Data).To(BeEmpty())
			Expect(c.Object(poolName, ns, "obj").Xattrs).To(HaveKey("x"))
		})

		It("returns every step's output with ReturnVec", func(ctx SpecContext) {
			res, read, err := readOp(ctx, radosclient.OpFlagReturnVec, []byte("out"))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Bytes()).To(Equal([]byte("out")))
			Expect(read.Data).To(Equal([]byte("abc")))
		})

		It("fails with EOVERFLOW and applies nothing when an output passes the reply limit, 64 bytes unless set", func(ctx SpecContext) {
			big := bytes.Repeat([]byte("o"), 65)
			res, _, err := readOp(ctx, radosclient.OpFlagReturnVec, big)
			Expect(err).To(haveErrno(syscall.EOVERFLOW))
			_, resErr := res.Bytes()
			Expect(resErr).To(haveErrno(syscall.EOVERFLOW))
			Expect(c.Object(poolName, ns, "obj").Xattrs).NotTo(HaveKey("x"))
			Expect(c.Object(poolName, ns, "obj").Version).To(BeEquivalentTo(1))
			Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				op.WriteFull([]byte("new"))
				op.Exec("probe", "get", big)
			})).To(Succeed(), "without ReturnVec a write's output is not bounded")
			c.SetMaxWriteOpReplyLen(128)
			_, _, err = readOp(ctx, radosclient.OpFlagReturnVec, big)
			Expect(err).NotTo(HaveOccurred())
		})

		It("counts every step's output against the limit, a read's and an omap read's too", func(ctx SpecContext) {
			c.Put(poolName, ns, "big", bytes.Repeat([]byte("d"), 65))
			op := radosclient.NewReadOp()
			op.Exec("probe", "set", nil)
			op.Read(0, 65)
			_, err := p.Read(ctx, "big", op, radosclient.OpFlagReturnVec)
			Expect(err).To(haveErrno(syscall.EOVERFLOW))
			omapRead := func(n int) error {
				c.Object(poolName, ns, "obj").Omap["k"] = bytes.Repeat([]byte("v"), n)
				op := radosclient.NewReadOp()
				op.Exec("probe", "set", nil)
				op.OmapGetValsByKeys([]string{"k"})
				_, err := p.Read(ctx, "obj", op, radosclient.OpFlagReturnVec)
				return err
			}
			Expect(omapRead(52)).To(haveErrno(syscall.EOVERFLOW), "a count, then the key and value after their lengths: 65 bytes")
			Expect(c.Object(poolName, ns, "obj").Xattrs).NotTo(HaveKey("x"))
			Expect(omapRead(51)).To(Succeed(), "64 bytes")
		})

		It("fails a method that changes the object without its WR flag with EIO", func(ctx SpecContext) {
			c.RegisterClass("sneaky", func(call *fakerados.ClassCall) ([]byte, int32) {
				call.Create().Xattrs["x"] = []byte("1")
				return nil, 0
			})
			err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Exec("sneaky", "m", nil) })
			Expect(err).To(haveErrno(syscall.EIO))
			Expect(c.Object(poolName, ns, "obj").Xattrs).NotTo(HaveKey("x"))
		})

		It("answers ENOENT before any step for an op of read-mode steps alone on an absent object", func(ctx SpecContext) {
			Expect(writeErr(ctx, p, "absent", func(op *radosclient.WriteOp) { op.AssertVersion(1) })).
				To(MatchError(radosclient.ErrNotFound), "a write op of read-mode steps is a read to the OSD")
			Expect(writeErr(ctx, p, "absent", func(op *radosclient.WriteOp) { op.Exec("probe", "get", nil) })).
				To(MatchError(radosclient.ErrNotFound))
			Expect(calls).To(BeZero(), "a method without the WR flag does not run on an absent object")
			Expect(writeErr(ctx, p, "absent", func(op *radosclient.WriteOp) { op.Exec("probe", "set", nil) })).
				To(Succeed(), "a WR method makes the op a write, which may create the object")
			Expect(c.Object(poolName, ns, "absent")).NotTo(BeNil())
		})
	})

	It("fails on a version or xattr mismatch and fills every result with the error", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abc"))
		op := radosclient.NewReadOp()
		read := op.Read(0, 3)
		op.AssertVersion(2)
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).To(haveErrno(syscall.EOVERFLOW))
		Expect(read.Err).To(haveErrno(syscall.EOVERFLOW))
		Expect(read.Data).To(BeNil(), "a failed op fills no data")
		op = radosclient.NewReadOp()
		op.CmpXattr("x", radosclient.CmpEQ, []byte("y"))
		_, err = p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrCanceled))
	})
})

var _ = Describe("watch and notify", func() {
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		c.Put(poolName, ns, "notify.0", nil)
	})
	type delivery struct {
		watch    string
		notifyID uint64
		payload  string
	}
	watch := func(ctx context.Context, name string, got *[]delivery) radosclient.Watch {
		GinkgoHelper()
		w, err := p.Watch(ctx, "notify.0", func(notifyID, _ uint64, payload []byte) {
			*got = append(*got, delivery{name, notifyID, string(payload)})
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(w.Close)
		return w
	}

	It("delivers a notify to every watch before it returns, with an ack from each", func(ctx SpecContext) {
		var got []delivery
		watch(ctx, "a", &got)
		watch(ctx, "b", &got)
		Expect(c.Watches(poolName, ns, "notify.0")).To(Equal(2))
		acks, err := p.Notify(ctx, "notify.0", []byte("one"), time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(acks).To(HaveLen(2))
		Expect(acks[0].Cookie).NotTo(Equal(acks[1].Cookie), "each watch acks under its own cookie")
		Expect(got).To(HaveLen(2))
		Expect([]string{got[0].watch, got[1].watch}).To(Equal([]string{"a", "b"}))
		Expect(got[0].notifyID).To(Equal(got[1].notifyID), "one notify, one id")
		_, err = p.Notify(ctx, "notify.0", []byte("two"), time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(got[2].notifyID).To(BeNumerically(">", got[0].notifyID))
		Expect(c.Notifies(poolName, ns, "notify.0")).To(Equal([][]byte{[]byte("one"), []byte("two")}))
	})

	It("breaks every watch on the object once with BreakWatches, which then stops receiving", func(ctx SpecContext) {
		var got []delivery
		a, b := watch(ctx, "a", &got), watch(ctx, "b", &got)
		boom := errors.New("boom")
		c.BreakWatches(poolName, ns, "notify.0", boom)
		Expect(a.Err()).To(Receive(MatchError(boom)))
		Expect(b.Err()).To(Receive(MatchError(boom)))
		Expect(a.Err()).NotTo(Receive(), "once")
		Expect(c.Watches(poolName, ns, "notify.0")).To(BeZero())
		acks, err := p.Notify(ctx, "notify.0", []byte("x"), time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(acks).To(BeEmpty())
		Expect(got).To(BeEmpty())
	})

	It("fails exactly n registrations with FailWatch", func(ctx SpecContext) {
		boom := errors.New("boom")
		c.FailWatch(poolName, ns, "notify.0", 2, boom)
		for range 2 {
			_, err := p.Watch(ctx, "notify.0", func(uint64, uint64, []byte) {})
			Expect(err).To(MatchError(boom))
		}
		var got []delivery
		watch(ctx, "a", &got)
		Expect(c.Watches(poolName, ns, "notify.0")).To(Equal(1))
	})

	It("fails exactly n notifies with FailNotify and delivers none of them", func(ctx SpecContext) {
		var got []delivery
		watch(ctx, "a", &got)
		c.FailNotify(poolName, ns, "notify.0", 1, radosclient.ErrTimedOut)
		acks, err := p.Notify(ctx, "notify.0", []byte("lost"), time.Second)
		Expect(err).To(MatchError(radosclient.ErrTimedOut))
		Expect(acks).To(BeEmpty())
		_, err = p.Notify(ctx, "notify.0", []byte("kept"), time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Notifies(poolName, ns, "notify.0")).To(Equal([][]byte{[]byte("kept")}))
		Expect(got).To(HaveLen(1))
	})

	It("refuses to watch or notify an absent object with ENOENT", func(ctx SpecContext) {
		_, err := p.Watch(ctx, "absent", func(uint64, uint64, []byte) {})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		_, err = p.Notify(ctx, "absent", nil, time.Second)
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})

	It("breaks the watches on an object a write op removes with ENOTCONN", func(ctx SpecContext) {
		var got []delivery
		w := watch(ctx, "a", &got)
		Expect(writeErr(ctx, p, "notify.0", func(op *radosclient.WriteOp) { op.Remove() })).To(Succeed())
		Expect(w.Err()).To(Receive(haveErrno(syscall.ENOTCONN)))
		Expect(c.Watches(poolName, ns, "notify.0")).To(BeZero())
	})

	It("stops delivering to a closed watch and closes its error channel", func(ctx SpecContext) {
		var got []delivery
		w := watch(ctx, "a", &got)
		Expect(w.Close()).To(Succeed())
		Expect(w.Err()).To(BeClosed())
		Expect(c.Watches(poolName, ns, "notify.0")).To(BeZero())
		_, err := p.Notify(ctx, "notify.0", nil, time.Second)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})
})

var _ = Describe("the cluster", func() {
	var c *fakerados.Cluster
	BeforeEach(func() {
		c = fakerados.New()
	})

	It("answers ConfigGet from SetConfig, and ENOENT, which cephconf reports as unknown, otherwise", func() {
		c.SetConfig("rgw_zone", "z")
		Expect(c.ConfigGet("rgw_zone")).To(Equal("z"))
		_, err := c.ConfigGet("rgw_nope")
		Expect(err).To(MatchError(radosclient.ErrNotFound), "rados_conf_get answers ENOENT")
		_, err = cephconf.NewOptions(c).String("rgw_nope")
		Expect(err).To(MatchError(cephconf.ErrUnknownOption))
	})

	It("reports the required OSD release it is given, Squid until then, and a fixed instance id", func(ctx SpecContext) {
		Expect(c.RequiredOSDRelease(ctx)).To(Equal("squid"))
		c.SetRequiredOSDRelease("tentacle")
		Expect(c.RequiredOSDRelease(ctx)).To(Equal("tentacle"))
		Expect(c.InstanceID()).To(BeEquivalentTo(4155))
	})

	It("fails opening a pool FailPool names with ErrNotFound", func(ctx SpecContext) {
		c.FailPool("missing")
		_, err := c.Pool(ctx, "missing", "")
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		_, err = c.Pool(ctx, "present", "")
		Expect(err).NotTo(HaveOccurred())
	})

	It("shares one namespace's objects between handles, keeps namespaces apart and ids each pool stably", func(ctx SpecContext) {
		a, err := c.Pool(ctx, "pool", "ns")
		Expect(err).NotTo(HaveOccurred())
		b, err := c.Pool(ctx, "pool", "ns")
		Expect(err).NotTo(HaveOccurred())
		other, err := c.Pool(ctx, "pool", "")
		Expect(err).NotTo(HaveOccurred())
		elsewhere, err := c.Pool(ctx, "elsewhere", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(writeErr(ctx, a, "obj", func(op *radosclient.WriteOp) { op.WriteFull([]byte("v")) })).To(Succeed())
		Expect(b.Read(ctx, "obj", radosclient.NewReadOp(), radosclient.OpFlagNone)).To(BeEquivalentTo(1))
		_, err = other.Read(ctx, "obj", radosclient.NewReadOp(), radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "another namespace")
		Expect([]any{a.Name(), a.Namespace()}).To(Equal([]any{"pool", "ns"}))
		Expect(a.ID()).To(Equal(other.ID()), "a pool has one id across its namespaces")
		Expect(a.ID()).NotTo(Equal(elsewhere.ID()))
		Expect(a.WithLocator("loc").ID()).To(Equal(a.ID()))
	})

	It("lists a namespace's objects in hobject order, by bit-reversed placement hash, without locators", func(ctx SpecContext) {
		putUsersUID(c)
		p, err := c.Pool(ctx, poolName, "users.uid")
		Expect(err).NotTo(HaveOccurred())
		var oids, locators []string
		Expect(p.ListObjects(ctx, func(oid, locator string) error {
			oids = append(oids, oid)
			locators = append(locators, locator)
			return nil
		})).To(Succeed())
		Expect(oids).To(Equal(usersUID))
		Expect(locators).To(HaveEach(BeEmpty()))
	})

	It("answers a fixed fsid until it closes", func() {
		Expect(c.FSID()).To(Equal(fakerados.FakeFSID))
		Expect(c.Close()).To(Succeed())
		_, err := c.FSID()
		Expect(err).To(MatchError(radosclient.ErrClosed))
	})

	DescribeTable("pages a namespace in its listing order with opaque tokens",
		func(ctx SpecContext, limit, pages int) {
			putUsersUID(c)
			p, err := c.Pool(ctx, poolName, "users.uid")
			Expect(err).NotTo(HaveOccurred())
			var paged []string
			token := ""
			for page := 1; ; page++ {
				Expect(page).To(BeNumerically("<=", pages), "the listing ends after %d pages", pages)
				before := len(paged)
				next, more, err := p.ListObjectsFrom(ctx, token, limit, collect(&paged))
				Expect(err).NotTo(HaveOccurred())
				if !more {
					Expect(next).To(BeEmpty())
					Expect(page).To(Equal(pages))
					break
				}
				Expect(len(paged)-before).To(Equal(limit), "a page before the last is full")
				Expect(next).NotTo(BeEmpty())
				token = next
			}
			Expect(paged).To(Equal(usersUID))
		},
		Entry("one at a time", 1, 10),
		Entry("two at a time", 2, 5),
		Entry("four at a time, a page boundary inside each same-hash pair", 4, 3),
		Entry("in a page the listing fills exactly, which says nothing follows", 10, 1),
		Entry("in a page larger than the listing", 11, 1),
		Entry("in one page when limit is 0", 0, 1),
	)

	It("resumes after a removed last object at the object that followed it", func(ctx SpecContext) {
		putUsersUID(c)
		p, err := c.Pool(ctx, poolName, "users.uid")
		Expect(err).NotTo(HaveOccurred())
		var first []string
		next, more, err := p.ListObjectsFrom(ctx, "", 2, collect(&first))
		Expect(err).NotTo(HaveOccurred())
		Expect(more).To(BeTrue())
		Expect(first).To(Equal(usersUID[:2]))
		Expect(writeErr(ctx, p, first[1], func(op *radosclient.WriteOp) { op.Remove() })).To(Succeed())
		var rest []string
		next, more, err = p.ListObjectsFrom(ctx, next, 0, collect(&rest))
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{next, more}).To(Equal([]any{"", false}))
		Expect(rest).To(Equal(usersUID[2:]))
	})

	It("refuses a token it did not make with ErrBadOp before listing", func(ctx SpecContext) {
		putUsersUID(c)
		p, err := c.Pool(ctx, poolName, "users.uid")
		Expect(err).NotTo(HaveOccurred())
		var listed []string
		_, _, err = p.ListObjectsFrom(ctx, "3:b55a9110:root::bu_9:head", 10, collect(&listed))
		Expect(err).To(MatchError(radosclient.ErrBadOp))
		Expect(listed).To(BeEmpty())
	})

	It("stops at the callback's error, the context's or a closed handle's", func(ctx SpecContext) {
		putUsersUID(c)
		p, err := c.Pool(ctx, poolName, "users.uid")
		Expect(err).NotTo(HaveOccurred())
		stop := errors.New("stop")
		n := 0
		_, _, err = p.ListObjectsFrom(ctx, "", 0, func(string, string) error {
			n++
			return stop
		})
		Expect(err).To(MatchError(stop))
		Expect(n).To(Equal(1))
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, _, err = p.ListObjectsFrom(canceled, "", 0, func(string, string) error { return nil })
		Expect(err).To(MatchError(context.Canceled))
		Expect(p.Close()).To(Succeed())
		_, _, err = p.ListObjectsFrom(ctx, "", 0, func(string, string) error { return nil })
		Expect(err).To(MatchError(radosclient.ErrClosed))
	})

	It("seeds an object at version 1 with Put and hands out the stored object, nil when absent", func() {
		Expect(c.Object("pool", "", "obj")).To(BeNil())
		c.Put("pool", "", "obj", []byte("v"))
		obj := c.Object("pool", "", "obj")
		Expect([]any{obj.Data, obj.Version}).To(Equal([]any{[]byte("v"), uint64(1)}))
		Expect(obj.Xattrs).NotTo(BeNil())
		Expect(obj.Omap).NotTo(BeNil())
	})

	It("answers the lock calls and mon commands with ErrNotSupported", func(ctx SpecContext) {
		p, err := c.Pool(ctx, "pool", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(p.LockExclusive(ctx, "o", "l", "c", "", time.Second, 0)).To(MatchError(radosclient.ErrNotSupported))
		Expect(p.LockShared(ctx, "o", "l", "c", "t", "", time.Second, 0)).To(MatchError(radosclient.ErrNotSupported))
		Expect(p.Unlock(ctx, "o", "l", "c")).To(MatchError(radosclient.ErrNotSupported))
		Expect(p.BreakLock(ctx, "o", "l", "client.1", "c")).To(MatchError(radosclient.ErrNotSupported))
		_, err = p.ListLockers(ctx, "o", "l")
		Expect(err).To(MatchError(radosclient.ErrNotSupported))
		_, _, err = c.MonCommand(ctx, []byte(`{"prefix":"status"}`))
		Expect(err).To(MatchError(radosclient.ErrNotSupported))
	})

	It("refuses a closed pool's operations and every operation once the cluster closes", func(ctx SpecContext) {
		p, err := c.Pool(ctx, "pool", "")
		Expect(err).NotTo(HaveOccurred())
		q, err := c.Pool(ctx, "pool", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Close()).To(Succeed())
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Create(false) })).To(MatchError(radosclient.ErrClosed))
		Expect(writeErr(ctx, q, "obj", func(op *radosclient.WriteOp) { op.Create(false) })).To(Succeed(), "another handle stays open")
		Expect(c.Close()).To(Succeed())
		Expect(writeErr(ctx, q, "obj", func(op *radosclient.WriteOp) { op.Create(false) })).To(MatchError(radosclient.ErrClosed))
		_, err = c.Pool(ctx, "pool", "")
		Expect(err).To(MatchError(radosclient.ErrClosed))
	})

	It("returns the context's error before running an op", func(ctx SpecContext) {
		p, err := c.Pool(ctx, "pool", "")
		Expect(err).NotTo(HaveOccurred())
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		Expect(writeErr(canceled, p, "obj", func(op *radosclient.WriteOp) { op.Create(false) })).To(MatchError(context.Canceled))
		Expect(c.Object("pool", "", "obj")).To(BeNil())
	})

	It("reports a pool's required alignment, 0 until set, across its namespaces and derived handles", func(ctx SpecContext) {
		ec, err := c.Pool(ctx, "ec", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(ec.RequiredAlignment(ctx)).To(BeZero(), "before SetRequiredAlignment")
		c.SetRequiredAlignment("ec", 8192)
		Expect(ec.RequiredAlignment(ctx)).To(BeEquivalentTo(8192), "the pool set")
		ns, err := c.Pool(ctx, "ec", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(ns.RequiredAlignment(ctx)).To(BeEquivalentTo(8192), "another namespace of the pool")
		Expect(ec.WithLocator("loc").RequiredAlignment(ctx)).To(BeEquivalentTo(8192), "a handle WithLocator derived")
		replicated, err := c.Pool(ctx, "replicated", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(replicated.RequiredAlignment(ctx)).To(BeZero(), "a pool never set")
	})

	It("refuses RequiredAlignment on a closed pool and after the context ends", func(ctx SpecContext) {
		c.SetRequiredAlignment("pool", 4096)
		p, err := c.Pool(ctx, "pool", "")
		Expect(err).NotTo(HaveOccurred())
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = p.RequiredAlignment(canceled)
		Expect(err).To(MatchError(context.Canceled))
		Expect(p.Close()).To(Succeed())
		_, err = p.RequiredAlignment(ctx)
		Expect(err).To(MatchError(radosclient.ErrClosed))
	})

	It("runs an op carrying OpFlagFullTry, which only a full pool would heed", func(ctx SpecContext) {
		c.Put("pool", "", "obj", []byte("v"))
		p, err := c.Pool(ctx, "pool", "")
		Expect(err).NotTo(HaveOccurred())
		op := radosclient.NewWriteOp()
		op.Remove()
		_, err = p.Write(ctx, "obj", op, radosclient.OpFlagFullTry)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Object("pool", "", "obj")).To(BeNil())
	})

	It("refuses an op flag or comparison librados does not define with ErrBadOp", func(ctx SpecContext) {
		p, err := c.Pool(ctx, "pool", "")
		Expect(err).NotTo(HaveOccurred())
		_, err = p.Write(ctx, "obj", radosclient.NewWriteOp(), radosclient.OpFlags(1<<20))
		Expect(err).To(MatchError(radosclient.ErrBadOp))
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Create(false)
			op.CmpXattr("x", radosclient.CmpOp(9), nil)
		})).To(MatchError(radosclient.ErrBadOp))
		Expect(c.Object("pool", "", "obj")).To(BeNil())
	})
})
