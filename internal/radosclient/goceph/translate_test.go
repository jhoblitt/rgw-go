package goceph_test

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"log/slog"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ceph/go-ceph/rados"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/budgetoracle"
)

// recorder records the go-ceph builder calls the translator makes, in order,
// and the buffers it hands to Read.
type recorder struct {
	calls []string
	bufs  [][]byte
}

func (r *recorder) add(call string) { r.calls = append(r.calls, call) }

func (r *recorder) AssertExists()          { r.add("AssertExists") }
func (r *recorder) AssertVersion(v uint64) { r.add("AssertVersion " + strconv.FormatUint(v, 10)) }
func (r *recorder) CmpXattr(name string, op rados.CmpXattrOp, value []byte) {
	r.add("CmpXattr " + name + " " + strconv.Itoa(int(op)) + " " + string(value))
}

func (r *recorder) Read(offset uint64, buf []byte) *rados.ReadOpReadStep {
	r.add("Read " + strconv.FormatUint(offset, 10) + " " + strconv.Itoa(len(buf)))
	r.bufs = append(r.bufs, buf)
	return new(rados.ReadOpReadStep)
}

func (r *recorder) Stat() *rados.ReadOpStatStep {
	r.add("Stat")
	return new(rados.ReadOpStatStep)
}

func (r *recorder) GetXattrs() *rados.ReadOpGetXattrsStep {
	r.add("GetXattrs")
	return new(rados.ReadOpGetXattrsStep)
}

func (r *recorder) GetOmapValues(startAfter, filterPrefix string, maxReturn uint64) *rados.GetOmapStep {
	r.add("GetOmapValues " + startAfter + " " + filterPrefix + " " + strconv.FormatUint(maxReturn, 10))
	return new(rados.GetOmapStep)
}

func (r *recorder) GetOmapValuesByKeys(keys []string) *rados.ReadOpOmapGetValsByKeysStep {
	r.add("GetOmapValuesByKeys " + strconv.Itoa(len(keys)))
	return new(rados.ReadOpOmapGetValsByKeysStep)
}

func (r *recorder) GetOmapKeys(startAfter string, maxReturn uint64) *rados.ReadOpOmapGetKeysStep {
	r.add("GetOmapKeys " + startAfter + " " + strconv.FormatUint(maxReturn, 10))
	return new(rados.ReadOpOmapGetKeysStep)
}

func (r *recorder) Exec(class, method string, in []byte) *rados.ReadOpExecStep {
	r.add("Exec " + class + "." + method + " " + string(in))
	return new(rados.ReadOpExecStep)
}

// writeRecorder is the write-op counterpart of recorder; its Exec returns nothing.
type writeRecorder struct{ recorder }

func (w *writeRecorder) Exec(class, method string, in []byte) {
	w.add("Exec " + class + "." + method + " " + string(in))
}

func (w *writeRecorder) Create(exclusive rados.CreateOption) {
	w.add("Create " + strconv.FormatBool(exclusive == rados.CreateExclusive))
}
func (w *writeRecorder) Remove() { w.add("Remove") }
func (w *writeRecorder) SetFlags(flags rados.OpFlags) {
	w.add("SetFlags " + strconv.FormatUint(uint64(flags), 10))
}

func (w *writeRecorder) WriteFull(b []byte) {
	w.add("WriteFull " + strconv.Itoa(len(b)))
}

func (w *writeRecorder) Write(b []byte, offset uint64) {
	w.add("Write " + strconv.Itoa(len(b)) + " " + strconv.FormatUint(offset, 10))
}
func (w *writeRecorder) Append(b []byte) { w.add("Append " + strconv.Itoa(len(b))) }
func (w *writeRecorder) Zero(offset, length uint64) {
	w.add("Zero " + strconv.FormatUint(offset, 10) + " " + strconv.FormatUint(length, 10))
}

func (w *writeRecorder) Truncate(offset uint64) { w.add("Truncate " + strconv.FormatUint(offset, 10)) }

func (w *writeRecorder) SetXattr(name string, value []byte) {
	w.add("SetXattr " + name + " " + strconv.Itoa(len(value)))
}
func (w *writeRecorder) RmXattr(name string) { w.add("RmXattr " + name) }
func (w *writeRecorder) SetOmap(pairs map[string][]byte) {
	w.add("SetOmap " + strconv.Itoa(len(pairs)))
}
func (w *writeRecorder) RmOmapKeys(keys []string) { w.add("RmOmapKeys " + strconv.Itoa(len(keys))) }
func (w *writeRecorder) CleanOmap()               { w.add("CleanOmap") }
func (w *writeRecorder) OmapCmp(key string, op rados.CmpXattrOp, value []byte) {
	w.add("OmapCmp " + key + " " + strconv.Itoa(int(op)) + " " + string(value))
}

func (w *writeRecorder) SetAllocationHint(objectSize, writeSize uint64, flags rados.AllocHintFlags) {
	w.add("SetAllocationHint " + strconv.FormatUint(objectSize, 10) + " " +
		strconv.FormatUint(writeSize, 10) + " " + strconv.FormatUint(uint64(flags), 10))
}

// codeErr stands in for go-ceph's errno-carrying error, which reports its
// errno, negative as librados returns it, through ErrorCode.
type codeErr int

func (e codeErr) Error() string  { return "errno " + strconv.Itoa(int(e)) }
func (e codeErr) ErrorCode() int { return int(e) }

var _ = Describe("translating seam ops", func() {
	It("turns stat, xattrs and read into three go-ceph steps in the same order", func() {
		op := radosclient.NewReadOp()
		op.Stat()
		op.GetXattrs()
		op.Read(4, 16)

		rec := &recorder{}
		Expect(goceph.TranslateRead(rec, op)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"Stat", "GetXattrs", "Read 4 16"}))
	})

	It("reads into the caller's buffer, and allocates one otherwise", func() {
		op := radosclient.NewReadOp()
		buf := make([]byte, 64)
		op.ReadInto(0, buf)
		op.Read(64, 32)

		rec := &recorder{}
		Expect(goceph.TranslateRead(rec, op)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"Read 0 64", "Read 64 32"}))
		Expect(rec.bufs).To(HaveLen(2))
		Expect(&rec.bufs[0][0]).To(BeIdenticalTo(&buf[0]), "the caller's buffer")
		Expect(rec.bufs[1]).To(HaveLen(32), "the allocated buffer")
	})

	It("translates every read step", func() {
		op := radosclient.NewReadOp()
		op.AssertExists()
		op.AssertVersion(7)
		op.CmpXattr("user.rgw.idtag", radosclient.CmpEQ, []byte("t"))
		op.OmapGetVals("a", "p", 5)
		op.OmapGetValsByKeys([]string{"k1", "k2"})
		op.OmapGetKeys("b", 6)
		op.Exec("version", "read", []byte("in"))

		rec := &recorder{}
		Expect(goceph.TranslateRead(rec, op)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{
			"AssertExists",
			"AssertVersion 7",
			"CmpXattr user.rgw.idtag 1 t",
			"GetOmapValues a p 5",
			"GetOmapValuesByKeys 2",
			"GetOmapKeys b 6",
			"Exec version.read in",
		}))
	})

	It("translates every write step in order and passes empty buffers through", func() {
		op := radosclient.NewWriteOp()
		op.AssertExists()
		op.AssertVersion(3)
		op.CmpXattr("user.rgw.idtag", radosclient.CmpNE, []byte("x"))
		op.Create(true)
		op.Create(false)
		op.WriteFull(nil)
		op.Write([]byte("abc"), 9)
		op.Append([]byte("d"))
		op.Zero(1, 2)
		op.Truncate(8)
		op.SetXattr("user.rgw.etag", nil)
		op.RmXattr("user.rgw.old")
		op.OmapSet(map[string][]byte{"k": nil})
		op.OmapRmKeys([]string{"k"})
		op.OmapClear()
		op.OmapCmp("k", radosclient.CmpGT, []byte("v"))
		op.SetAllocHint(4096, 1024, 2)
		op.Exec("version", "set", []byte("in"))
		op.Remove()
		op.SetStepFlags(radosclient.StepFlagFailOK)
		op.Create(false)

		rec := &writeRecorder{}
		Expect(goceph.TranslateWrite(rec, op)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{
			"AssertExists",
			"AssertVersion 3",
			"CmpXattr user.rgw.idtag 2 x",
			"Create true",
			"Create false",
			"WriteFull 0",
			"Write 3 9",
			"Append 1",
			"Zero 1 2",
			"Truncate 8",
			"SetXattr user.rgw.etag 0",
			"RmXattr user.rgw.old",
			"SetOmap 1",
			"RmOmapKeys 1",
			"CleanOmap",
			"OmapCmp k 3 v",
			"SetAllocationHint 4096 1024 2",
			"Exec version.set in",
			"Remove",
			"SetFlags 2",
			"Create false",
		}))
	})

	It("rejects step flags that no step precedes, which librados would abort on", func() {
		op := radosclient.NewWriteOp()
		op.SetStepFlags(radosclient.StepFlagFailOK)
		op.Remove()
		rec := &writeRecorder{}
		Expect(goceph.TranslateWrite(rec, op)).To(MatchError(radosclient.ErrBadOp))
		Expect(rec.calls).To(BeEmpty())
	})

	It("rejects a step flag librados does not pass on", func() {
		op := radosclient.NewWriteOp()
		op.Remove()
		op.SetStepFlags(0x80) // FADVISE_FUA, which librados drops
		Expect(goceph.TranslateWrite(&writeRecorder{}, op)).To(MatchError(radosclient.ErrBadOp))
	})

	It("rejects an unknown comparison operator", func() {
		op := radosclient.NewReadOp()
		op.CmpXattr("n", radosclient.CmpOp(9), nil)
		Expect(goceph.TranslateRead(&recorder{}, op)).To(MatchError(radosclient.ErrBadOp))
	})

	It("maps the seam's op flags onto go-ceph's", func() {
		Expect(goceph.TranslateFlags(radosclient.OpFlagNone)).To(Equal(rados.OperationNoFlag))
		Expect(goceph.TranslateFlags(radosclient.OpFlagReturnVec)).To(Equal(rados.OperationReturnVec))
		Expect(goceph.TranslateFlags(radosclient.OpFlagBalanceReads | radosclient.OpFlagIgnoreCache)).
			To(Equal(rados.OperationBalanceReads | rados.OperationIgnoreCache))
		Expect(goceph.TranslateFlags(radosclient.OpFlagLocalizeReads)).To(Equal(rados.OperationLocalizeReads))
		Expect(goceph.TranslateFlags(radosclient.OpFlagFullTry)).To(Equal(rados.OperationFullTry), "full try")
		Expect(goceph.TranslateFlags(radosclient.OpFlagFullTry|radosclient.OpFlagReturnVec)).
			To(Equal(rados.OperationFullTry|rados.OperationReturnVec), "full try with return vec")
	})

	It("refuses an op flag the seam does not define with ErrBadOp", func() {
		_, err := goceph.TranslateFlags(radosclient.OpFlagReturnVec | radosclient.OpFlags(1<<20))
		Expect(err).To(MatchError(radosclient.ErrBadOp))
		Expect(err).To(MatchError(ContainSubstring("0x100000")), "only the undefined bit is named")
	})
})

var _ = Describe("mapping go-ceph errors", func() {
	DescribeTable("an errno maps to its sentinel",
		func(errno int, sentinel error) {
			err := goceph.ToSeamError("read obj", codeErr(-errno))
			Expect(err).To(MatchError(sentinel))

			var seamErr *radosclient.Error
			Expect(errors.As(err, &seamErr)).To(BeTrue())
			Expect(seamErr.Op).To(Equal("read obj"))

			wrapped := goceph.ToSeamError("write obj", rados.OperationError{OpError: codeErr(-errno)})
			Expect(wrapped).To(MatchError(sentinel))
		},
		Entry("ENOENT", 2, radosclient.ErrNotFound),
		Entry("EEXIST", 17, radosclient.ErrExists),
		Entry("ECANCELED", 125, radosclient.ErrCanceled),
		Entry("ERR_BUSY_RESHARDING", 2300, radosclient.ErrBusyResharding),
	)

	It("picks the first failing step in step order, every time", func() {
		opErr := rados.OperationError{StepErrors: map[int]error{
			7: codeErr(-2), 3: codeErr(-17), 5: codeErr(-125), 4: nil,
		}}
		for range 100 {
			Expect(goceph.ToSeamError("read obj", opErr)).To(MatchError(radosclient.ErrExists))
		}
	})

	It("prefers the op's own error to its steps'", func() {
		opErr := rados.OperationError{OpError: codeErr(-125), StepErrors: map[int]error{0: codeErr(-2)}}
		Expect(goceph.ToSeamError("write obj", opErr)).To(MatchError(radosclient.ErrCanceled))
	})

	It("gives a step its own error when librados did not fail the op", func() {
		stepOnly := rados.OperationError{StepErrors: map[int]error{2: codeErr(-61)}}
		Expect(goceph.StepError("read obj", stepOnly, 2)).To(MatchError(radosclient.ErrNoData))
		Expect(goceph.StepError("read obj", stepOnly, 1)).To(Succeed())
		Expect(goceph.StepError("read obj", nil, 1)).To(Succeed())
	})

	It("gives every step the op's error when librados failed the op", func() {
		// The -EIO is what librados records for an action the OSD never ran.
		failed := rados.OperationError{OpError: codeErr(-2), StepErrors: map[int]error{0: codeErr(-5), 1: codeErr(-2)}}
		Expect(goceph.StepError("read obj", failed, 0)).To(MatchError(radosclient.ErrNotFound))
		Expect(goceph.StepError("read obj", failed, 1)).To(MatchError(radosclient.ErrNotFound))
		Expect(goceph.StepError("read obj", failed, 2)).To(MatchError(radosclient.ErrNotFound))
		Expect(goceph.StepError("read obj", codeErr(-2), 0)).To(MatchError(radosclient.ErrNotFound))
	})

	It("finds a step's errno when the op itself reports none", func() {
		err := goceph.ToSeamError("read obj", rados.OperationError{StepErrors: map[int]error{1: codeErr(-61)}})
		Expect(err).To(MatchError(radosclient.ErrNoData))
	})

	It("wraps an error without an errno", func() {
		err := goceph.ToSeamError("read obj", rados.ErrInvalidIOContext)
		Expect(err).To(MatchError(rados.ErrInvalidIOContext))
	})

	It("leaves nil alone", func() {
		Expect(goceph.ToSeamError("read obj", nil)).To(Succeed())
	})
})

// go-ceph's WriteOp builds the same OperationError for a synchronous Operate
// and for AioCompletion.Err, in callback and pipe mode alike: OpError carries
// librados's return code whenever it is nonzero, and a write step whose
// prval is nonzero reports it under the step's index.
var _ = Describe("a positive librados return", func() {
	var (
		op   *radosclient.WriteOp
		exec *radosclient.ExecResult
	)

	BeforeEach(func() {
		op = radosclient.NewWriteOp()
		op.WriteFull([]byte("x"))
		exec = op.Exec("hello", "write_return_data", nil)
	})

	DescribeTable("is success for a write op and its steps",
		func(err error) {
			Expect(goceph.CompleteWrite(&writeRecorder{}, op, err)).To(Succeed())
			out, execErr := exec.Bytes()
			Expect(execErr).NotTo(HaveOccurred())
			Expect(out).To(BeNil())
		},
		Entry("on the op", rados.OperationError{OpError: codeErr(2300)}),
		Entry("on the op and the exec step's prval",
			rados.OperationError{OpError: codeErr(42), StepErrors: map[int]error{1: codeErr(42)}}),
		Entry("on the exec step's prval alone", rados.OperationError{StepErrors: map[int]error{1: codeErr(42)}}),
		Entry("as a bare errno", codeErr(2300)),
	)

	It("leaves a step's own negative return an error", func() {
		mixed := rados.OperationError{OpError: codeErr(2300), StepErrors: map[int]error{0: codeErr(42), 1: codeErr(-61)}}
		Expect(goceph.CompleteWrite(&writeRecorder{}, op, mixed)).To(MatchError(radosclient.ErrNoData))
		_, err := exec.Bytes()
		Expect(err).To(MatchError(radosclient.ErrNoData))
		Expect(goceph.StepError("write obj", mixed, 0)).To(Succeed())
		Expect(goceph.StepError("write obj", mixed, 1)).To(MatchError(radosclient.ErrNoData))
	})

	DescribeTable("leaves a negative return as it was",
		func(err error) {
			Expect(goceph.CompleteWrite(&writeRecorder{}, op, err)).To(MatchError(radosclient.ErrBusyResharding))
			_, execErr := exec.Bytes()
			Expect(execErr).To(MatchError(radosclient.ErrBusyResharding))
		},
		Entry("on the op", rados.OperationError{OpError: codeErr(-2300)}),
		Entry("on the op, over a positive step",
			rados.OperationError{OpError: codeErr(-2300), StepErrors: map[int]error{1: codeErr(42)}}),
		Entry("on the exec step's prval", rados.OperationError{StepErrors: map[int]error{1: codeErr(-2300)}}),
		Entry("as a bare errno", codeErr(-2300)),
	)
})

// stepOutput stands in for go-ceph's read exec step after its op ran.
type stepOutput []byte

func (s stepOutput) Output() ([]byte, error) { return s, nil }

var _ = Describe("a read exec's result", func() {
	var res *radosclient.ExecResult

	BeforeEach(func() {
		res = radosclient.NewReadOp().Exec("rgw", "bucket_list", nil)
	})

	It("holds the output and a zero return value after a success", func() {
		goceph.FinishReadExec(stepOutput("listed"), res, 1, nil)
		out, rval, err := res.Reply()
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal([]byte("listed")), "output")
		Expect(rval).To(BeZero(), "return value")
	})

	It("keeps the output of the call that failed the op, beside its errno", func() {
		// The OSD stops at the failing call and returns its value as the op's,
		// and librados reports it as the step's prval too.
		efbig := rados.OperationError{OpError: codeErr(-27), StepErrors: map[int]error{1: codeErr(-27)}}
		goceph.FinishReadExec(stepOutput("marker"), res, 1, efbig)
		out, rval, err := res.Reply()
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal([]byte("marker")), "output")
		Expect(rval).To(Equal(int32(-27)), "return value")
		_, err = res.Bytes()
		Expect(err).To(MatchError(radosclient.ErrTooBig), "Bytes's error")
	})

	It("takes no output from a call when a later step failed the op", func() {
		canceled := rados.OperationError{OpError: codeErr(-125), StepErrors: map[int]error{2: codeErr(-125)}}
		goceph.FinishReadExec(stepOutput("listed"), res, 1, canceled)
		out, rval, err := res.Reply()
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(BeNil(), "output")
		Expect(rval).To(Equal(int32(-125)), "return value, the op's")
	})
})

// readSteps and writeSteps build an op and return its steps.
func readSteps(build func(op *radosclient.ReadOp)) []radosclient.Step {
	op := radosclient.NewReadOp()
	build(op)
	return op.Steps()
}

func writeSteps(build func(op *radosclient.WriteOp)) []radosclient.Step {
	op := radosclient.NewWriteOp()
	build(op)
	return op.Steps()
}

// The weights below follow Objecter::calc_op_budget
// (https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.cc#L3502-L3518,
// the same at v19.2.7) over the steps librados's C op builders make: a
// write-mode step is charged its indata, a read-mode step its extent length
// or its xattr name and value lengths, and a class call nothing. No public
// librados API reports an op's budget, so the table's oracle is these rules;
// the specs after it also ask libceph-common's own calc_op_budget, through
// budgetoracle.
var _ = Describe("an op's weight", func() {
	DescribeTable("is what librados's objecter budgets it, and its exec inputs, which the limiter takes beside one operation",
		func(ctx SpecContext, steps []radosclient.Step, want, wantPayload int64) {
			Expect(goceph.Weight(steps)).To(Equal(want), "weight")
			Expect(goceph.CountedBytes(steps)).To(BeEquivalentTo(wantPayload), "the payload the Stats counters count")
			l := goceph.NewLimiter(1, 1<<30)
			Expect(l.Acquire(ctx, goceph.Weight(steps))).To(Succeed())
			Expect(l.Stats()).To(Equal(radosclient.Stats{InflightOps: 1, InflightBytes: want}))
		},
		Entry("a read's length, and nothing for a stat, an assertion, getxattrs or an omap read",
			readSteps(func(op *radosclient.ReadOp) {
				op.Read(0, 4<<20)
				op.Stat()
				op.AssertExists()
				op.AssertVersion(7)
				op.GetXattrs()
				op.OmapGetVals("start", "prefix", 1000)
				op.OmapGetValsByKeys([]string{"key-1", "key-2"})
				op.OmapGetKeys("start", 1000)
			}), int64(4<<20), int64(4<<20)),
		Entry("the length of a caller's read buffer",
			readSteps(func(op *radosclient.ReadOp) { op.ReadInto(8, make([]byte, 100)) }), int64(100), int64(100)),
		Entry("a read op's exec input, though librados budgets a class call nothing",
			readSteps(func(op *radosclient.ReadOp) { op.Exec("version", "read", []byte("1234567")) }), int64(7), int64(7)),
		Entry("write-full and append data and a write op's exec input",
			writeSteps(func(op *radosclient.WriteOp) {
				op.WriteFull([]byte("abc"))
				op.Append([]byte("de"))
				op.Exec("version", "set", []byte("12345"))
			}), int64(10), int64(10)),
		Entry("the data of a write at an offset",
			writeSteps(func(op *radosclient.WriteOp) { op.Write([]byte("abcd"), 1<<20) }), int64(4), int64(4)),
		Entry("nothing for a remove, a create, a zero's length, a truncate, an omap clear or comparison, an allocation hint or an assertion, which still counts one operation",
			writeSteps(func(op *radosclient.WriteOp) {
				op.AssertExists()
				op.AssertVersion(3)
				op.Remove()
				op.SetStepFlags(radosclient.StepFlagFailOK)
				op.Create(false)
				op.Zero(0, 1<<20)
				op.Truncate(0)
				op.OmapClear()
				op.OmapCmp("key", radosclient.CmpEQ, []byte("value"))
				op.SetAllocHint(4<<20, 4<<20, 0)
			}), int64(0), int64(0)),
		Entry("a setxattr's name and value, its indata",
			writeSteps(func(op *radosclient.WriteOp) { op.SetXattr("user.rgw.etag", []byte("abc")) }), int64(13+3), int64(0)),
		Entry("an rmxattr's name, its indata",
			writeSteps(func(op *radosclient.WriteOp) { op.RmXattr("user.rgw.x-amz-meta-a") }), int64(21), int64(0)),
		Entry("a read op's cmpxattr name and value lengths",
			readSteps(func(op *radosclient.ReadOp) { op.CmpXattr("user.rgw.idtag", radosclient.CmpEQ, []byte("tag-1")) }),
			int64(14+5), int64(0)),
		Entry("a write op's cmpxattr name and value lengths, a read-mode step there too, beside its data",
			writeSteps(func(op *radosclient.WriteOp) {
				op.CmpXattr("user.rgw.idtag", radosclient.CmpEQ, []byte("tag-1"))
				op.WriteFull([]byte("abc"))
			}), int64(14+5+3), int64(3)),
		Entry("an omap set's encoded map: a count, then each key and value behind its length",
			writeSteps(func(op *radosclient.WriteOp) { op.OmapSet(map[string][]byte{"a": []byte("xyz"), "bc": nil}) }),
			int64(4+(4+1+4+3)+(4+2+4+0)), int64(0)),
		Entry("an empty omap set's count",
			writeSteps(func(op *radosclient.WriteOp) { op.OmapSet(map[string][]byte{}) }), int64(4), int64(0)),
		Entry("an omap removal's encoded key set, which holds a repeated key once",
			writeSteps(func(op *radosclient.WriteOp) { op.OmapRmKeys([]string{"a", "bc", "a"}) }),
			int64(4+(4+1)+(4+2)), int64(0)),
		Entry("every one of many setxattr steps, as a zero-byte PUT with 900 x-amz-meta-* headers makes",
			writeSteps(func(op *radosclient.WriteOp) {
				op.WriteFull(nil)
				for i := range 900 {
					op.SetXattr(fmt.Sprintf("user.rgw.x-amz-meta-%03d", i), []byte("v1"))
				}
			}), int64(900*(23+2)), int64(0)),
	)

	// A negative weight would give the limiter's semaphores back bytes no op
	// took.
	DescribeTable("saturates at math.MaxInt64 rather than wrapping negative",
		func(steps []radosclient.Step) {
			Expect(goceph.Weight(steps)).To(Equal(int64(math.MaxInt64)))
		},
		Entry("for a read longer than math.MaxInt64",
			readSteps(func(op *radosclient.ReadOp) { op.Read(0, 1<<63) })),
		Entry("for reads whose lengths add up past math.MaxInt64",
			readSteps(func(op *radosclient.ReadOp) {
				op.Read(0, math.MaxInt64)
				op.Read(0, 1)
			})),
	)

	It("knows every step kind the seam declares", func() {
		kinds, byValue := seamStepKinds()
		Expect(byValue).To(BeEmpty(), "Step's kinds are pointers, so isStep takes a pointer receiver")
		Expect(kinds).To(ConsistOf(slices.Collect(maps.Keys(everyStepKind))),
			"everyStepKind lists every type with radosclient's isStep method")
		for name, s := range everyStepKind {
			Expect(func() { goceph.Weight([]radosclient.Step{s}) }).NotTo(Panic(), "a %s step", name)
		}
	})

	It("panics on a step kind it does not know, rather than weighing it 0", func() {
		unknown := &struct{ radosclient.ExecStep }{}
		Expect(func() { goceph.Weight([]radosclient.Step{unknown}) }).To(PanicWith(ContainSubstring("no weight for")))
	})

	Describe("against libceph-common's own calc_op_budget", func() {
		// expect checks steps' weight against the budget librados reported
		// for the go-ceph op translated from them.
		expect := func(steps []radosclient.Step, budget int, err error) {
			if budgetoracle.Skipped(err) {
				Skip(budgetoracle.SkipEnv + "=skip, and libceph-common does not export Objecter::calc_op_budget")
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(goceph.Weight(steps)-execInput(steps)).To(BeEquivalentTo(budget),
				"weight is librados's budget and the exec inputs it does not charge")
		}

		DescribeTable("is the budget of a write op, and its exec inputs",
			func(build func(op *radosclient.WriteOp)) {
				op := radosclient.NewWriteOp()
				build(op)
				wop := rados.CreateWriteOp()
				DeferCleanup(wop.Release)
				Expect(goceph.TranslateWrite(wop, op)).To(Succeed())
				budget, err := budgetoracle.Budget(wop, osdOpsOf(op.Steps()))
				expect(op.Steps(), budget, err)
			},
			Entry("of every write step kind", func(op *radosclient.WriteOp) {
				op.AssertExists()
				op.AssertVersion(1)
				op.CmpXattr("user.rgw.idtag", radosclient.CmpEQ, []byte("tag-1"))
				op.Create(false)
				op.WriteFull([]byte("abc"))
				op.SetStepFlags(radosclient.StepFlagFailOK)
				op.Write([]byte("de"), 4)
				op.Append([]byte("f"))
				op.Zero(0, 1<<20)
				op.Truncate(9)
				op.SetXattr("user.rgw.etag", []byte("abc"))
				op.RmXattr("user.rgw.x-amz-meta-a")
				op.OmapSet(map[string][]byte{"a": []byte("xyz"), "bc": nil})
				op.OmapRmKeys([]string{"a", "bc", "a"})
				op.OmapClear()
				op.OmapCmp("key", radosclient.CmpEQ, []byte("value"))
				op.SetAllocHint(4<<20, 4<<20, 0)
				op.Exec("version", "set", []byte("12345"))
				op.Remove()
			}),
			Entry("of a zero-byte PUT's head write with 900 x-amz-meta-* headers", func(op *radosclient.WriteOp) {
				op.WriteFull(nil)
				for i := range 900 {
					op.SetXattr(fmt.Sprintf("user.rgw.x-amz-meta-%03d", i), []byte("v1"))
				}
			}),
		)

		DescribeTable("is the budget of a read op, and its exec inputs",
			func(build func(op *radosclient.ReadOp)) {
				op := radosclient.NewReadOp()
				build(op)
				rop := rados.CreateReadOp()
				DeferCleanup(rop.Release)
				Expect(goceph.TranslateRead(rop, op)).To(Succeed())
				budget, err := budgetoracle.Budget(rop, osdOpsOf(op.Steps()))
				expect(op.Steps(), budget, err)
			},
			Entry("of every read step kind", func(op *radosclient.ReadOp) {
				op.AssertExists()
				op.AssertVersion(7)
				op.CmpXattr("user.rgw.idtag", radosclient.CmpEQ, []byte("tag-1"))
				op.Read(0, 4<<20)
				op.ReadInto(8, make([]byte, 100))
				op.Stat()
				op.GetXattrs()
				op.OmapGetVals("start", "prefix", 1000)
				op.OmapGetValsByKeys([]string{"key-1", "key-2"})
				op.OmapGetKeys("start", 1000)
				op.Exec("version", "read", []byte("1234567"))
			}),
		)
	})
})

// everyStepKind is a step of each kind radosclient declares, by its type's
// name.
var everyStepKind = map[string]radosclient.Step{
	"ExecStep":              &radosclient.ExecStep{},
	"ReadStep":              &radosclient.ReadStep{},
	"StatStep":              &radosclient.StatStep{},
	"GetXattrsStep":         &radosclient.GetXattrsStep{},
	"CmpXattrStep":          &radosclient.CmpXattrStep{},
	"AssertExistsStep":      &radosclient.AssertExistsStep{},
	"AssertVersionStep":     &radosclient.AssertVersionStep{},
	"OmapGetValsStep":       &radosclient.OmapGetValsStep{},
	"OmapGetValsByKeysStep": &radosclient.OmapGetValsByKeysStep{},
	"OmapGetKeysStep":       &radosclient.OmapGetKeysStep{},
	"CreateStep":            &radosclient.CreateStep{},
	"RemoveStep":            &radosclient.RemoveStep{},
	"StepFlagsStep":         &radosclient.StepFlagsStep{},
	"WriteFullStep":         &radosclient.WriteFullStep{},
	"WriteStep":             &radosclient.WriteStep{},
	"AppendStep":            &radosclient.AppendStep{},
	"ZeroStep":              &radosclient.ZeroStep{},
	"TruncateStep":          &radosclient.TruncateStep{},
	"SetXattrStep":          &radosclient.SetXattrStep{},
	"RmXattrStep":           &radosclient.RmXattrStep{},
	"OmapSetStep":           &radosclient.OmapSetStep{},
	"OmapRmKeysStep":        &radosclient.OmapRmKeysStep{},
	"OmapClearStep":         &radosclient.OmapClearStep{},
	"OmapCmpStep":           &radosclient.OmapCmpStep{},
	"SetAllocHintStep":      &radosclient.SetAllocHintStep{},
}

// seamStepKinds reads radosclient's source for the types that implement
// Step, each through its isStep method, and for any whose isStep takes a
// value receiver.
func seamStepKinds() (kinds, byValue []string) {
	files, err := filepath.Glob(filepath.Join("..", "*.go"))
	Expect(err).NotTo(HaveOccurred())
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		Expect(err).NotTo(HaveOccurred())
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != "isStep" {
				continue
			}
			recv := fd.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok {
					kinds = append(kinds, id.Name)
					continue
				}
			}
			byValue = append(byValue, fset.Position(recv.Pos()).String())
		}
	}
	return kinds, byValue
}

// execInput is what weight counts and librados does not: the op's exec
// inputs.
func execInput(steps []radosclient.Step) int64 {
	var n int64
	for _, s := range steps {
		if e, ok := s.(*radosclient.ExecStep); ok {
			n += int64(len(e.In))
		}
	}
	return n
}

// osdOpsOf is how many OSD ops librados holds for steps: one each, but for
// step flags, which set the flags of the op before them.
func osdOpsOf(steps []radosclient.Step) int {
	n := 0
	for _, s := range steps {
		if _, ok := s.(*radosclient.StepFlagsStep); !ok {
			n++
		}
	}
	return n
}

// unsetenv removes key from the environment until the spec ends.
func unsetenv(key string) {
	if v, ok := os.LookupEnv(key); ok {
		Expect(os.Unsetenv(key)).To(Succeed())
		DeferCleanup(os.Setenv, key, v)
	}
}

// captureLogs sends slog's default logger to a buffer until the spec ends.
// slog.SetDefault also points the log package's output at the new handler,
// and restoring the old default logger leaves it there, so the cleanup puts
// log's writer and flags back too.
func captureLogs() *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

var _ = Describe("Config", func() {
	It("defaults to the ceph cluster, client.admin and callback completions", func() {
		cfg := goceph.WithDefaults(goceph.Config{})
		Expect(cfg.Cluster).To(Equal("ceph"))
		Expect(cfg.Name).To(Equal("client.admin"))
		Expect(cfg.Mode).To(Equal(goceph.ModeCallback))
	})

	It("keeps what the caller set", func() {
		cfg := goceph.WithDefaults(goceph.Config{Cluster: "c", Name: "client.rgw.a", Mode: goceph.ModePipe})
		Expect(cfg).To(Equal(goceph.Config{Cluster: "c", Name: "client.rgw.a", Mode: goceph.ModePipe}))
	})

	It("leaves the cluster unnamed beside a ConfigFile, for librados to name after the file", func() {
		Expect(goceph.WithDefaults(goceph.Config{ConfigFile: "/etc/ceph/prod.conf"}).Cluster).To(BeEmpty())
	})

	DescribeTable("refuses a negative in-flight limit before touching librados",
		func(ctx SpecContext, cfg goceph.Config) {
			cfg.ConfigFile = "/nonexistent/ceph.conf"
			cluster, err := goceph.Connect(ctx, cfg)
			Expect(err).To(MatchError(radosclient.ErrBadOp))
			Expect(cluster).To(BeNil())
		},
		Entry("operations", goceph.Config{MaxInflightOps: -1}),
		Entry("bytes", goceph.Config{MaxInflightBytes: -1}),
	)

	Describe("reading the config file", func() {
		// cluster names the files librados searches for by default,
		// $home/.ceph/<cluster>.conf among them, so no file on the host
		// matches.
		const cluster = "rgwgoconftest"

		var home string

		BeforeEach(func() {
			unsetenv("CEPH_CONF")
			home = GinkgoT().TempDir()
			GinkgoT().Setenv("HOME", home)
			Expect(os.Mkdir(filepath.Join(home, ".ceph"), 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(home, ".ceph", cluster+".conf"),
				[]byte("[global]\nobjecter_inflight_ops = 77\n"), 0o600)).To(Succeed())
		})

		It("reads the first file of the default search", func(ctx SpecContext) {
			Expect(goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster}, "objecter_inflight_ops")).To(Equal("77"))
		})

		It("skips the default search under NoConfigFile", func(ctx SpecContext) {
			Expect(goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster, NoConfigFile: true}, "objecter_inflight_ops")).
				To(Equal("1024"), "librados's default")
		})

		It("still reads a named file under NoConfigFile, as ceph reads -c under --no-config-file", func(ctx SpecContext) {
			cfg := goceph.Config{Cluster: "ceph", ConfigFile: filepath.Join(home, ".ceph", cluster+".conf"), NoConfigFile: true}
			Expect(goceph.ConfiguredOption(ctx, cfg, "objecter_inflight_ops")).To(Equal("77"))
		})

		It("continues with librados's defaults when the default search finds no file", func(ctx SpecContext) {
			Expect(goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster + "-missing"}, "objecter_inflight_ops")).To(Equal("1024"))
		})

		It("warns once, from Connect, that the default search found no file", func(ctx SpecContext) {
			logs := captureLogs()
			// An argument librados cannot parse stops Connect after the
			// config file is read and before it connects.
			cfg := goceph.Config{Cluster: cluster + "-missing", Args: []string{"--objecter_inflight_ops=bogus"}}
			_, err := goceph.ConfiguredOptions(ctx, goceph.Config{Cluster: cfg.Cluster}, "objecter_inflight_ops")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs.String()).NotTo(ContainSubstring("no ceph.conf found"), "ConfiguredOptions reads the files Connect reads next")
			_, err = goceph.Connect(ctx, cfg)
			Expect(err).To(MatchError(ContainSubstring("parse args")))
			Expect(strings.Count(logs.String(), `"msg":"no ceph.conf found, continuing with defaults"`)).To(Equal(1))
		})

		It("fails when a named file is missing", func(ctx SpecContext) {
			_, err := goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster, ConfigFile: filepath.Join(home, "missing.conf")}, "fsid")
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})

		It("reads the files CEPH_CONF names under NoConfigFile, as ceph does without -c", func(ctx SpecContext) {
			GinkgoT().Setenv("CEPH_CONF", filepath.Join(home, ".ceph", cluster+".conf"))
			Expect(goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster, NoConfigFile: true}, "objecter_inflight_ops")).
				To(Equal("77"))
		})

		It("goes on silently under NoConfigFile when CEPH_CONF names no readable file", func(ctx SpecContext) {
			logs := captureLogs()
			GinkgoT().Setenv("CEPH_CONF", filepath.Join(home, "missing.conf"))
			Expect(goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster, NoConfigFile: true}, "objecter_inflight_ops")).
				To(Equal("1024"))
			Expect(logs.String()).NotTo(ContainSubstring("no ceph.conf found"))
		})

		It("goes on under NoConfigFile when a named file is missing, as radosgw does with -c", func(ctx SpecContext) {
			cfg := goceph.Config{Cluster: cluster, ConfigFile: filepath.Join(home, "missing.conf"), NoConfigFile: true}
			Expect(goceph.ConfiguredOption(ctx, cfg, "objecter_inflight_ops")).To(Equal("1024"))
		})

		It("still fails under NoConfigFile on a file that does not parse", func(ctx SpecContext) {
			bad := filepath.Join(home, "bad.conf")
			Expect(os.WriteFile(bad, []byte("[global\n"), 0o600)).To(Succeed())
			_, err := goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster, ConfigFile: bad, NoConfigFile: true}, "fsid")
			Expect(err).To(HaveOccurred())
			Expect(err).NotTo(MatchError(radosclient.ErrNotFound))
		})
	})

	Describe("naming the cluster", func() {
		var dir string

		BeforeEach(func() {
			unsetenv("CEPH_CONF")
			dir = GinkgoT().TempDir()
			for _, name := range []string{"prod.conf", "settings"} {
				Expect(os.WriteFile(filepath.Join(dir, name), []byte("[global]\n"), 0o600)).To(Succeed())
			}
		})

		// $cluster in an option's value expands to the cluster's name when
		// the option is read.
		DescribeTable("as radosgw's parse_config_files names it",
			func(ctx SpecContext, cfg goceph.Config, want string) {
				if cfg.ConfigFile != "" {
					cfg.ConfigFile = filepath.Join(dir, cfg.ConfigFile)
				}
				cfg.Args = []string{"--rgw_zone=$cluster"}
				Expect(goceph.ConfiguredOption(ctx, cfg, "rgw_zone")).To(Equal(want))
			},
			Entry("after the -c file read, less .conf", goceph.Config{ConfigFile: "prod.conf"}, "prod"),
			Entry("ceph after a -c file whose name does not end in .conf", goceph.Config{ConfigFile: "settings"}, "ceph"),
			Entry("ceph without -c, under --no-config-file too", goceph.Config{NoConfigFile: true}, "ceph"),
			Entry("empty when --no-config-file lets a missing -c file go", goceph.Config{ConfigFile: "missing.conf", NoConfigFile: true}, ""),
			Entry("--cluster over the file's name", goceph.Config{Cluster: "given", ConfigFile: "prod.conf"}, "given"),
		)
	})

	Describe("ConfiguredOptions", func() {
		It("reads what the argv sets before any connect", func(ctx SpecContext) {
			cfg := goceph.Config{NoConfigFile: true, Args: []string{"--setuser=ceph", "--setgroup=disk"}}
			Expect(goceph.ConfiguredOptions(ctx, cfg, "setuser", "setgroup", "setuser_match_path")).To(Equal(map[string]string{
				"setuser":            "ceph",
				"setgroup":           "disk",
				"setuser_match_path": "",
			}))
		})

		It("fails on an option librados does not know", func(ctx SpecContext) {
			_, err := goceph.ConfiguredOptions(ctx, goceph.Config{NoConfigFile: true}, "setuser", "no_such_option")
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})
	})
})
