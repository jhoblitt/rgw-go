package radosclient_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

var _ = Describe("ReadOp", func() {
	It("records steps in call order with their results attached", func() {
		op := radosclient.NewReadOp()
		stat := op.Stat()
		xattrs := op.GetXattrs()
		read := op.Read(0, 4<<20)

		steps := op.Steps()
		Expect(steps).To(Equal([]radosclient.Step{
			&radosclient.StatStep{Result: stat},
			&radosclient.GetXattrsStep{Result: xattrs},
			&radosclient.ReadStep{Offset: 0, Length: 4 << 20, Result: read},
		}), "read op steps")
		Expect(steps[0]).To(HaveField("Result", BeIdenticalTo(stat)), "stat result pointer")
		Expect(steps[1]).To(HaveField("Result", BeIdenticalTo(xattrs)), "xattrs result pointer")
		Expect(steps[2]).To(HaveField("Result", BeIdenticalTo(read)), "read result pointer")
	})

	It("records every builder method as its own step", func() {
		op := radosclient.NewReadOp()
		op.AssertExists()
		op.AssertVersion(7)
		op.CmpXattr("user.rgw.idtag", radosclient.CmpEQ, []byte("tag"))
		vals := op.OmapGetVals("a", "b", 10)
		byKeys := op.OmapGetValsByKeys([]string{"k1", "k2"})
		keys := op.OmapGetKeys("c", 20)
		exec := op.Exec("rgw", "bucket_list", []byte("in"))

		Expect(op.Steps()).To(Equal([]radosclient.Step{
			&radosclient.AssertExistsStep{},
			&radosclient.AssertVersionStep{Version: 7},
			&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tag")},
			&radosclient.OmapGetValsStep{StartAfter: "a", FilterPrefix: "b", Max: 10, Result: vals},
			&radosclient.OmapGetValsByKeysStep{Keys: []string{"k1", "k2"}, Result: byKeys},
			&radosclient.OmapGetKeysStep{StartAfter: "c", Max: 20, Result: keys},
			&radosclient.ExecStep{Class: "rgw", Method: "bucket_list", In: []byte("in"), Result: exec},
		}), "read op steps")
	})

	It("does not let a caller's append to Steps reach the op", func() {
		op := radosclient.NewReadOp()
		op.AssertExists()
		steps := op.Steps()
		_ = append(steps[:0], &radosclient.RemoveStep{})
		Expect(op.Steps()).To(Equal([]radosclient.Step{&radosclient.AssertExistsStep{}}), "read op steps")
	})
})

var _ = Describe("WriteOp", func() {
	It("records steps in call order and keeps the mtime off the step list", func() {
		op := radosclient.NewWriteOp()
		_, set := op.Mtime()
		Expect(set).To(BeFalse(), "mtime set before SetMtime")

		mtime := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
		op.SetMtime(mtime)
		op.AssertExists()
		op.AssertVersion(3)
		op.CmpXattr("x", radosclient.CmpNE, []byte("v"))
		op.Create(true)
		op.Remove()
		op.SetStepFlags(radosclient.StepFlagFailOK)
		op.WriteFull([]byte("full"))
		op.Write([]byte("part"), 8)
		op.Append([]byte("tail"))
		op.Zero(1, 2)
		op.Truncate(9)
		op.SetXattr("n", []byte("v"))
		op.RmXattr("n")
		op.OmapSet(map[string][]byte{"k": []byte("v")})
		op.OmapRmKeys([]string{"k"})
		op.OmapClear()
		op.OmapCmp("k", radosclient.CmpLT, []byte("v"))
		op.SetAllocHint(4<<20, 1<<20, 0)
		exec := op.Exec("lock", "lock", nil)

		got, set := op.Mtime()
		Expect(set).To(BeTrue(), "mtime set after SetMtime")
		Expect(got).To(Equal(mtime), "mtime")
		Expect(op.Steps()).To(Equal([]radosclient.Step{
			&radosclient.AssertExistsStep{},
			&radosclient.AssertVersionStep{Version: 3},
			&radosclient.CmpXattrStep{Name: "x", Op: radosclient.CmpNE, Value: []byte("v")},
			&radosclient.CreateStep{Exclusive: true},
			&radosclient.RemoveStep{},
			&radosclient.StepFlagsStep{Flags: radosclient.StepFlagFailOK},
			&radosclient.WriteFullStep{Data: []byte("full")},
			&radosclient.WriteStep{Data: []byte("part"), Offset: 8},
			&radosclient.AppendStep{Data: []byte("tail")},
			&radosclient.ZeroStep{Offset: 1, Length: 2},
			&radosclient.TruncateStep{Offset: 9},
			&radosclient.SetXattrStep{Name: "n", Value: []byte("v")},
			&radosclient.RmXattrStep{Name: "n"},
			&radosclient.OmapSetStep{Values: map[string][]byte{"k": []byte("v")}},
			&radosclient.OmapRmKeysStep{Keys: []string{"k"}},
			&radosclient.OmapClearStep{},
			&radosclient.OmapCmpStep{Key: "k", Op: radosclient.CmpLT, Value: []byte("v")},
			&radosclient.SetAllocHintStep{ExpectedObjectSize: 4 << 20, ExpectedWriteSize: 1 << 20},
			&radosclient.ExecStep{Class: "lock", Method: "lock", Result: exec},
		}), "write op steps")
	})
})

var _ = Describe("ExecResult", func() {
	type builder func() radosclient.Execer
	readOp := func() radosclient.Execer { return radosclient.NewReadOp() }
	writeOp := func() radosclient.Execer { return radosclient.NewWriteOp() }

	DescribeTable("reports incomplete until Set, then the output",
		func(newOp builder) {
			res := newOp().Exec("rgw", "guard_bucket_resharding", []byte("in"))
			out, err := res.Bytes()
			Expect(err).To(MatchError(radosclient.ErrIncomplete), "error before Set")
			Expect(out).To(BeNil(), "output before Set")

			res.Set([]byte("out"), 0)
			out, err = res.Bytes()
			Expect(err).NotTo(HaveOccurred(), "error after Set")
			Expect(out).To(Equal([]byte("out")), "output after Set")
		},
		Entry("on a ReadOp", builder(readOp)),
		Entry("on a WriteOp", builder(writeOp)),
	)

	DescribeTable("maps a negative rval to the errno's sentinel",
		func(newOp builder) {
			res := newOp().Exec("rgw", "obj_check_attrs_prefix", nil)
			res.Set([]byte("ignored"), -2)
			out, err := res.Bytes()
			Expect(err).To(MatchError(radosclient.ErrNotFound), "error for rval -2")
			Expect(out).To(BeNil(), "output for rval -2")
			var rerr *radosclient.Error
			Expect(err).To(BeAssignableToTypeOf(rerr), "error type")
			Expect(err).To(HaveField("Errno", int32(2)), "errno")
			Expect(err).To(MatchError("rados: exec rgw.obj_check_attrs_prefix: no such file or directory"), "error text")
		},
		Entry("on a ReadOp", builder(readOp)),
		Entry("on a WriteOp", builder(writeOp)),
	)
})

var _ = Describe("Error", func() {
	It("matches ErrNotFound for ENOENT and nothing else", func() {
		err := &radosclient.Error{Errno: 2}
		Expect(err.Is(radosclient.ErrNotFound)).To(BeTrue(), "ENOENT is ErrNotFound")
		Expect(err.Is(radosclient.ErrExists)).To(BeFalse(), "ENOENT is not ErrExists")
	})

	It("matches ErrBusyResharding for cls_rgw's 2300", func() {
		Expect(&radosclient.Error{Errno: 2300}).To(MatchError(radosclient.ErrBusyResharding), "errno 2300")
	})

	DescribeTable("maps each errno to its sentinel",
		func(errno int32, want error) {
			Expect(&radosclient.Error{Errno: errno}).To(MatchError(want), "errno %d", errno)
		},
		Entry("ENOENT", int32(2), radosclient.ErrNotFound),
		Entry("EEXIST", int32(17), radosclient.ErrExists),
		Entry("ECANCELED", int32(125), radosclient.ErrCanceled),
		Entry("ENOSPC", int32(28), radosclient.ErrNoSpace),
		Entry("EPERM", int32(1), radosclient.ErrPermission),
		Entry("ENODATA", int32(61), radosclient.ErrNoData),
		Entry("ERANGE", int32(34), radosclient.ErrRange),
		Entry("EFBIG", int32(27), radosclient.ErrTooBig),
		Entry("EINVAL", int32(22), radosclient.ErrInvalid),
		Entry("ETIMEDOUT", int32(110), radosclient.ErrTimedOut),
		Entry("EOPNOTSUPP", int32(95), radosclient.ErrNotSupported),
		Entry("ERR_BUSY_RESHARDING", int32(2300), radosclient.ErrBusyResharding),
	)

	It("treats a negative errno as its absolute value", func() {
		err := &radosclient.Error{Errno: -2, Op: "stat"}
		Expect(err.Is(radosclient.ErrNotFound)).To(BeTrue(), "-ENOENT is ErrNotFound")
		Expect(err.Error()).To(Equal("rados: stat: no such file or directory"), "error text for -2")
	})

	It("does not map an unlisted errno to any sentinel", func() {
		err := &radosclient.Error{Errno: 16}
		Expect(err.Is(radosclient.ErrNotFound)).To(BeFalse(), "EBUSY is not ErrNotFound")
		Expect(err.Is(radosclient.ErrIncomplete)).To(BeFalse(), "EBUSY is not ErrIncomplete")
	})

	It("renders the operation and the errno text", func() {
		Expect((&radosclient.Error{Errno: 2, Op: "stat"}).Error()).
			To(Equal("rados: stat: no such file or directory"), "error text")
		Expect((&radosclient.Error{Errno: 2300, Op: "exec rgw.bucket_list"}).Error()).
			To(Equal("rados: exec rgw.bucket_list: bucket is resharding"), "error text for 2300")
	})
})

var _ = Describe("OpFlags", func() {
	It("carries librados's LIBRADOS_OP_FLAG_* values", func() {
		Expect(radosclient.StepFlagExcl).To(BeEquivalentTo(0x1))
		Expect(radosclient.StepFlagFailOK).To(BeEquivalentTo(0x2))
		Expect(radosclient.StepFlagFAdviseRandom).To(BeEquivalentTo(0x4))
		Expect(radosclient.StepFlagFAdviseSequential).To(BeEquivalentTo(0x8))
		Expect(radosclient.StepFlagFAdviseWillNeed).To(BeEquivalentTo(0x10))
		Expect(radosclient.StepFlagFAdviseDontNeed).To(BeEquivalentTo(0x20))
		Expect(radosclient.StepFlagFAdviseNoCache).To(BeEquivalentTo(0x40))
	})

	It("carries librados's LIBRADOS_OPERATION_* values", func() {
		Expect(radosclient.OpFlagBalanceReads).To(Equal(radosclient.OpFlags(1)), "BALANCE_READS")
		Expect(radosclient.OpFlagLocalizeReads).To(Equal(radosclient.OpFlags(2)), "LOCALIZE_READS")
		Expect(radosclient.OpFlagIgnoreCache).To(Equal(radosclient.OpFlags(8)), "IGNORE_CACHE")
		Expect(radosclient.OpFlagReturnVec).To(Equal(radosclient.OpFlags(1024)), "RETURNVEC")
	})
})
