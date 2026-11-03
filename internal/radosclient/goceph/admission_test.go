package goceph_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ceph/go-ceph/rados"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

var (
	errSubmitted = errors.New("a recording op was submitted")
	errBuild     = errors.New("a recording op's builder panicked")
)

// opHooks is what the recording ops share with the spec: whether their
// builder panics, and the limiter's bytes in flight, which Release records.
type opHooks struct {
	panics   bool
	inflight func() int64
}

func (h opHooks) released(r *recorder) {
	r.add("Release with " + strconv.FormatInt(h.inflight(), 10) + " bytes held")
}

// readOpRecorder stands in for the go-ceph read op Read builds: it records
// what Read builds into it and when Read releases it, and refuses to be
// submitted.
type readOpRecorder struct {
	recorder
	opHooks
}

func (r *readOpRecorder) Exec(class, method string, in []byte) *rados.ReadOpExecStep {
	step := r.recorder.Exec(class, method, in)
	if r.panics {
		panic(errBuild)
	}
	return step
}

func (*readOpRecorder) Operate(*rados.IOContext, string, rados.OperationFlags) error {
	return errSubmitted
}

func (*readOpRecorder) OperateAsync(*rados.IOContext, string, rados.OperationFlags) (*rados.AioCompletion, error) {
	return nil, errSubmitted
}

func (r *readOpRecorder) Release() { r.released(&r.recorder) }

// writeOpRecorder is readOpRecorder's write-op counterpart. It records a
// write's data rather than its length.
type writeOpRecorder struct {
	writeRecorder
	opHooks
}

func (w *writeOpRecorder) WriteFull(b []byte) {
	w.add("WriteFull " + string(b))
	if w.panics {
		panic(errBuild)
	}
}

func (*writeOpRecorder) Operate(*rados.IOContext, string, rados.OperationFlags) error {
	return errSubmitted
}

func (*writeOpRecorder) OperateWithMtime(*rados.IOContext, string, rados.Timespec, rados.OperationFlags) error {
	return errSubmitted
}

func (*writeOpRecorder) OperateAsync(*rados.IOContext, string, rados.OperationFlags) (*rados.AioCompletion, error) {
	return nil, errSubmitted
}

func (*writeOpRecorder) OperateAsyncWithMtime(*rados.IOContext, string, rados.Timespec, rados.OperationFlags) (*rados.AioCompletion, error) {
	return nil, errSubmitted
}

func (w *writeOpRecorder) Release() { w.released(&w.recorder) }

// opCall runs one Read or Write on p.
type opCall func(ctx context.Context, p radosclient.Pool) error

// writeWith and readWith run a Write or a Read of the op steps fills.
func writeWith(steps func(op *radosclient.WriteOp)) opCall {
	return func(ctx context.Context, p radosclient.Pool) error {
		op := radosclient.NewWriteOp()
		steps(op)
		_, err := p.Write(ctx, "obj", op, radosclient.OpFlagNone)
		return err
	}
}

func readWith(steps func(op *radosclient.ReadOp)) opCall {
	return func(ctx context.Context, p radosclient.Pool) error {
		op := radosclient.NewReadOp()
		steps(op)
		_, err := p.Read(ctx, "obj", op, radosclient.OpFlagNone)
		return err
	}
}

// writeData weighs 7 bytes and readExec 5.
var (
	writeData = writeWith(func(op *radosclient.WriteOp) { op.WriteFull([]byte("payload")) })
	readExec  = readWith(func(op *radosclient.ReadOp) { op.Exec("rgw", "bucket_list", []byte("input")) })
)

// recovering turns a panic in call into an error wrapping errBuild.
func recovering(call opCall) opCall {
	return func(ctx context.Context, p radosclient.Pool) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%w: recovered %v", errBuild, r)
			}
		}()
		return call(ctx, p)
	}
}

var _ = Describe("an op the in-flight limiter has not admitted", func() {
	const budget = 1000

	var (
		pool  radosclient.Pool
		limit goceph.Limiter
		// built receives each go-ceph op Read or Write creates.
		built chan *recorder
		// panics makes the next op's builder panic.
		panics bool
	)

	BeforeEach(func(ctx SpecContext) {
		pool, limit = goceph.ClosedPool(8, budget)
		built = make(chan *recorder, 1)
		panics = false
		hooks := func() opHooks {
			return opHooks{panics: panics, inflight: func() int64 { return limit.Stats().InflightBytes }}
		}
		DeferCleanup(goceph.SetNewReadOp(func() goceph.RadosReadOp {
			op := &readOpRecorder{opHooks: hooks()}
			built <- &op.recorder
			return op
		}))
		DeferCleanup(goceph.SetNewWriteOp(func() goceph.RadosWriteOp {
			op := &writeOpRecorder{opHooks: hooks()}
			built <- &op.recorder
			return op
		}))
		Expect(limit.Acquire(ctx, budget)).To(Succeed(), "the spec holds every byte")
	})

	// park runs call on a goroutine of its own, which returns once ctx ends if
	// the limiter never admits it, and waits until the op parks on the
	// limiter the spec holds.
	park := func(ctx context.Context, call opCall) <-chan error {
		done := make(chan error, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			done <- call(ctx, pool)
		}()
		DeferCleanup(func() {
			Eventually(finished).WithTimeout(limiterTimeout).WithPolling(5 * time.Millisecond).Should(BeClosed())
		})
		Eventually(func() uint64 { return limit.Stats().ThrottleWaits }).
			WithTimeout(limiterTimeout).WithPolling(5*time.Millisecond).
			Should(BeEquivalentTo(1), "the op parks on the limiter before it builds anything")
		return done
	}

	// The op's Release records the bytes the limiter still holds: the op's
	// own, since the spec gave its hold back to admit it. An op that returned
	// its budget before its Release would record 0.

	DescribeTable("has built nothing, and once admitted builds its op with the payload",
		func(ctx SpecContext, call opCall, want []string) {
			done := park(ctx, call)
			Expect(built).NotTo(Receive(), "an op parked on the limiter has created no go-ceph op")

			limit.Release(budget)
			Eventually(done).WithTimeout(limiterTimeout).WithPolling(5*time.Millisecond).
				Should(Receive(MatchError(radosclient.ErrClosed)), "the closed pool refuses the admitted op where it would take an I/O context")
			var op *recorder
			Expect(built).To(Receive(&op), "the admitted op was built")
			Expect(op.calls).To(Equal(want), "the op was released before its budget")
			Expect(limit.Stats()).To(Equal(radosclient.Stats{ThrottleWaits: 1}), "the refused op gave back its budget")
		},
		Entry("a write's data", writeData, []string{"WriteFull payload", "Release with 7 bytes held"}),
		Entry("a read's exec input", readExec, []string{"Exec rgw.bucket_list input", "Release with 5 bytes held"}),
	)

	DescribeTable("whose op then fails to build gives back exactly the budget it took",
		func(ctx SpecContext, call opCall, want []string) {
			done := park(ctx, call)
			Expect(done).NotTo(Receive(), "an op that cannot be built still waits for admission")

			limit.Release(budget)
			Eventually(done).WithTimeout(limiterTimeout).WithPolling(5 * time.Millisecond).
				Should(Receive(MatchError(radosclient.ErrBadOp)))
			Expect(limit.Stats()).To(Equal(radosclient.Stats{ThrottleWaits: 1}), "no operation or byte is still held")
			var op *recorder
			Expect(built).To(Receive(&op))
			Expect(op.calls).To(Equal(want), "the half-built op was released before its budget")
			short, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			Expect(limit.Acquire(short, budget)).To(Succeed(), "every byte is free again")
		},
		Entry("a write with a step flag librados does not pass on",
			writeWith(func(op *radosclient.WriteOp) {
				op.WriteFull([]byte("payload"))
				op.SetStepFlags(0x80)
			}),
			[]string{"WriteFull payload", "Release with 7 bytes held"}),
		Entry("a read with an unknown comparison operator",
			readWith(func(op *radosclient.ReadOp) {
				op.Exec("rgw", "bucket_list", []byte("input"))
				op.CmpXattr("user.rgw.idtag", radosclient.CmpOp(9), nil)
			}),
			[]string{"Exec rgw.bucket_list input", "Release with 5 bytes held"}),
	)

	DescribeTable("whose builder panics once admitted gives back its budget",
		func(ctx SpecContext, call opCall, want []string) {
			panics = true
			done := park(ctx, recovering(call))

			limit.Release(budget)
			Eventually(done).WithTimeout(limiterTimeout).WithPolling(5 * time.Millisecond).
				Should(Receive(MatchError(errBuild)))
			Expect(limit.Stats()).To(Equal(radosclient.Stats{ThrottleWaits: 1}), "the panicking op holds no operation or byte")
			var op *recorder
			Expect(built).To(Receive(&op))
			Expect(op.calls).To(Equal(want), "the op was released before its budget")
		},
		Entry("a write", writeData, []string{"WriteFull payload", "Release with 7 bytes held"}),
		Entry("a read", readExec, []string{"Exec rgw.bucket_list input", "Release with 5 bytes held"}),
	)

	DescribeTable("whose caller gives up while it is parked has built nothing and leaves the budget whole",
		func(ctx SpecContext, call opCall) {
			opCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := park(opCtx, call)

			cancel()
			Eventually(done).WithTimeout(limiterTimeout).WithPolling(5 * time.Millisecond).
				Should(Receive(MatchError(context.Canceled)))
			Expect(built).NotTo(Receive(), "an op given up on while parked has created no go-ceph op")
			Expect(limit.Stats()).To(Equal(radosclient.Stats{InflightOps: 1, InflightBytes: budget, ThrottleWaits: 1}),
				"only the spec's own hold is in flight")
			limit.Release(budget)
			short, cancelShort := context.WithTimeout(ctx, time.Second)
			defer cancelShort()
			Expect(limit.Acquire(short, budget)).To(Succeed(), "every byte is free again")
		},
		Entry("a write", writeData),
		Entry("a read", readExec),
	)
})
