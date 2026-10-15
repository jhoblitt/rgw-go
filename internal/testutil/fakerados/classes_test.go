package fakerados_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("a class's registered methods", func() {
	var (
		c     *fakerados.Cluster
		p     radosclient.Pool
		calls []string
		probe fakerados.ClassFunc
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		calls = nil
		probe = func(call *fakerados.ClassCall) ([]byte, int32) {
			calls = append(calls, call.Method)
			if call.Method == "set" {
				call.Create().Xattrs["x"] = []byte("1")
			}
			return []byte(call.Method), 0
		}
		c.RegisterClassMethods("probe", probe, []string{"get", "set"}, []string{"set"})
	})
	read := func(ctx context.Context, oid string, build func(op *radosclient.ReadOp)) error {
		op := radosclient.NewReadOp()
		build(op)
		_, err := p.Read(ctx, oid, op, radosclient.OpFlagNone)
		return err
	}

	// OpInfo::set_from_op refuses the op before any step runs, where
	// get_method_flags finds no method of its name
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/osd_op_util.cc#L189-L196 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/osd_op_util.cc#L189-L196 at v20.2.4),
	// and do_op replies with that error before it looks the object up.
	DescribeTable("fails an op calling a method its class does not register with EOPNOTSUPP before any step runs",
		func(ctx SpecContext, stored []byte, run func(ctx context.Context) error) {
			if stored != nil {
				c.Put(poolName, ns, "obj", stored)
			}
			Expect(run(ctx)).To(MatchError(radosclient.ErrNotSupported), "OpInfo::set_from_op, osd_op_util.cc:189-196 at v19.2.7 and v20.2.4")
			Expect(calls).To(BeEmpty(), "the class runs no method of the op")
			obj := c.Object(poolName, ns, "obj")
			if stored == nil {
				Expect(obj).To(BeNil(), "the op creates nothing")
				return
			}
			Expect(obj.Data).To(Equal(stored), "the op changes nothing")
			Expect(obj.Version).To(BeEquivalentTo(1))
		},
		Entry("a read op of the call alone on a missing object, not ENOENT", nil, func(ctx context.Context) error {
			return read(ctx, "obj", func(op *radosclient.ReadOp) { op.Exec("probe", "no_such_method", nil) })
		}),
		Entry("a write op of the call alone on a missing object, not ENOENT", nil, func(ctx context.Context) error {
			return writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Exec("probe", "no_such_method", nil) })
		}),
		Entry("a read op whose earlier CmpXattr fails, not ECANCELED", []byte("abc"), func(ctx context.Context) error {
			return read(ctx, "obj", func(op *radosclient.ReadOp) {
				op.CmpXattr("x", radosclient.CmpEQ, []byte("y"))
				op.Exec("probe", "no_such_method", nil)
			})
		}),
		Entry("WriteFull, then the call flagged FailOK, on a stored object, which neither succeeds nor rewrites the data", []byte("abc"), func(ctx context.Context) error {
			return writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				op.WriteFull([]byte("new"))
				op.Exec("probe", "no_such_method", nil)
				op.SetStepFlags(radosclient.StepFlagFailOK)
			})
		}),
	)

	It("runs the methods it registers, and serves an op calling a write as a write", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("abc"))
		Expect(read(ctx, "obj", func(op *radosclient.ReadOp) { op.Exec("probe", "get", nil) })).To(Succeed())
		Expect(read(ctx, "absent", func(op *radosclient.ReadOp) { op.Exec("probe", "set", nil) })).
			To(Succeed(), "the WR flag makes the read op a write, which may create the object")
		Expect(calls).To(Equal([]string{"get", "set"}))
		Expect(c.Object(poolName, ns, "absent")).NotTo(BeNil())
	})

	It("panics on a write that is not one of the class's methods", func() {
		Expect(func() { c.RegisterClassMethods("probe", probe, []string{"get"}, []string{"set"}) }).
			To(PanicWith(`fakerados: class "probe": write method "set" is not one of its methods`))
	})

	It("refuses to register a class it emulates from method tables through RegisterClass, which takes no method set", func() {
		Expect(func() { c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...) }).
			To(PanicWith(`fakerados: class "rgw" has a method set: register it with RegisterClassMethods`))
		Expect(func() {
			c.RegisterClassMethods("rgw", fakerados.RGWClass(), fakerados.RGWMethods, fakerados.RGWWriteMethods)
		}).NotTo(Panic())
	})

	It("still hands a class RegisterClass registers every method name, checking only the class at op setup", func(ctx SpecContext) {
		c.RegisterClass("probe", probe, "set")
		c.Put(poolName, ns, "obj", []byte("abc"))
		var res *radosclient.ExecResult
		Expect(read(ctx, "obj", func(op *radosclient.ReadOp) { res = op.Exec("probe", "no_such_method", nil) })).To(Succeed())
		Expect(res.Bytes()).To(Equal([]byte("no_such_method")))
		Expect(calls).To(Equal([]string{"no_such_method"}))
		Expect(read(ctx, "absent", func(op *radosclient.ReadOp) { op.Exec("probe", "no_such_method", nil) })).
			To(MatchError(radosclient.ErrNotFound), "a method without the WR flag on a missing object, as before")
		Expect(calls).To(HaveLen(1))
	})
})
