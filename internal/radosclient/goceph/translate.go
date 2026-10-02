package goceph

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"syscall"

	"github.com/ceph/go-ceph/rados"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// readBuilder is the part of *rados.ReadOp the translator drives.
type readBuilder interface {
	AssertExists()
	AssertVersion(ver uint64)
	CmpXattr(name string, op rados.CmpXattrOp, value []byte)
	Read(offset uint64, buffer []byte) *rados.ReadOpReadStep
	Stat() *rados.ReadOpStatStep
	GetXattrs() *rados.ReadOpGetXattrsStep
	GetOmapValues(startAfter, filterPrefix string, maxReturn uint64) *rados.GetOmapStep
	GetOmapValuesByKeys(keys []string) *rados.ReadOpOmapGetValsByKeysStep
	GetOmapKeys(startAfter string, maxReturn uint64) *rados.ReadOpOmapGetKeysStep
	Exec(clsName, method string, in []byte) *rados.ReadOpExecStep
}

// writeBuilder is the part of *rados.WriteOp the translator drives.
type writeBuilder interface {
	AssertExists()
	AssertVersion(ver uint64)
	CmpXattr(name string, op rados.CmpXattrOp, value []byte)
	Create(exclusive rados.CreateOption)
	Remove()
	WriteFull(b []byte)
	Write(b []byte, offset uint64)
	Append(b []byte)
	Zero(offset, length uint64)
	Truncate(offset uint64)
	SetXattr(name string, value []byte)
	RmXattr(name string)
	SetOmap(pairs map[string][]byte)
	RmOmapKeys(keys []string)
	CleanOmap()
	OmapCmp(key string, op rados.CmpXattrOp, value []byte)
	SetAllocationHint(expectedObjectSize, expectedWriteSize uint64, flags rados.AllocHintFlags)
	Exec(clsName, method string, in []byte)
	SetFlags(flags rados.OpFlags)
}

var (
	_ readBuilder  = (*rados.ReadOp)(nil)
	_ writeBuilder = (*rados.WriteOp)(nil)
)

// outcome is how an operation ended: the error Read or Write returns, the
// error librados returned for the operation itself, and the go-ceph errors of
// the steps that reported their own return value, keyed by the index of the
// step in the go-ceph op.
type outcome struct {
	name  string
	err   error
	opErr error
	steps map[int]error
}

func newOutcome(name string, err error) outcome {
	err = dropPositive(err)
	o := outcome{name: name, err: toSeamError(name, err)}
	if oe, ok := errors.AsType[rados.OperationError](err); ok {
		o.opErr = toSeamError(name, oe.OpError)
		o.steps = oe.StepErrors
	} else {
		o.opErr = o.err
	}
	return o
}

// dropPositive removes the positive return codes from err, the error a
// go-ceph Operate or AioCompletion.Err returned, and returns nil when nothing
// else is left. librados treats any return >= 0 as success and the OSD has
// committed such a write, but go-ceph's WriteOp reports every nonzero return,
// the op's and a write step's prval alike, as an error carrying that code.
func dropPositive(err error) error {
	if oe, ok := errors.AsType[rados.OperationError](err); ok {
		if positive(oe.OpError) {
			oe.OpError = nil
		}
		steps := make(map[int]error, len(oe.StepErrors))
		for i, e := range oe.StepErrors {
			if e != nil && !positive(e) {
				steps[i] = e
			}
		}
		oe.StepErrors = steps
		if oe.OpError == nil && len(steps) == 0 {
			return nil
		}
		return oe
	}
	if positive(err) {
		return nil
	}
	return err
}

// positive reports whether err carries a positive librados return code.
func positive(err error) bool {
	ec, ok := errors.AsType[errorCoder](err)
	return ok && ec.ErrorCode() > 0
}

// step returns the error of the go-ceph step at index i: its own return
// value when it reported one, the operation's otherwise.
//
// When librados failed the operation itself, every step takes that error.
// The OSD stops at the failing action, and librados's decode handler for
// each action it never ran throws on the empty output and records -EIO as
// that action's rval (Objecter::process_op_reply_handlers), so a step's own
// rval there is an artifact; the failing action's own rval is the op's.
func (o outcome) step(i int) error {
	if o.opErr != nil {
		return o.opErr
	}
	if e := o.steps[i]; e != nil {
		return toSeamError(o.name, e)
	}
	return nil
}

// finisher copies one step's go-ceph result into its seam result. It runs
// after the operation completed and before the go-ceph op or completion is
// released.
type finisher func(o outcome)

// finish runs every finisher against the operation's outcome.
func finish(fs []finisher, o outcome) {
	for _, f := range fs {
		f(o)
	}
}

// The go-ceph builders append a fixed number of entries to the op's step
// list, whose indices OperationError.StepErrors uses: Read appends two (the
// buffer and its result, in that order); Stat, GetXattrs, the omap reads,
// Exec, Write, WriteFull and OmapCmp append one; the rest append none.

// translateRead adds op's steps to b in order and returns the finishers that
// fill their results.
func translateRead(b readBuilder, op *radosclient.ReadOp) ([]finisher, error) {
	var fs []finisher
	idx := 0
	for _, s := range op.Steps() {
		switch s := s.(type) {
		case *radosclient.AssertExistsStep:
			b.AssertExists()
		case *radosclient.AssertVersionStep:
			b.AssertVersion(s.Version)
		case *radosclient.CmpXattrStep:
			cmp, err := translateCmp(s.Op)
			if err != nil {
				return nil, err
			}
			b.CmpXattr(s.Name, cmp, s.Value)
		case *radosclient.ReadStep:
			fs = append(fs, readStep(b, s))
			idx += 2
		case *radosclient.StatStep:
			fs = append(fs, statStep(b, s, idx))
			idx++
		case *radosclient.GetXattrsStep:
			fs = append(fs, getXattrsStep(b, s, idx))
			idx++
		case *radosclient.OmapGetValsStep:
			fs = append(fs, omapGetValsStep(b, s, idx))
			idx++
		case *radosclient.OmapGetValsByKeysStep:
			fs = append(fs, omapGetValsByKeysStep(b, s, idx))
			idx++
		case *radosclient.OmapGetKeysStep:
			fs = append(fs, omapGetKeysStep(b, s, idx))
			idx++
		case *radosclient.ExecStep:
			fs = append(fs, readExecStep(b, s, idx))
			idx++
		default:
			return nil, fmt.Errorf("goceph: %T in a read op: %w", s, radosclient.ErrBadOp)
		}
	}
	return fs, nil
}

func readStep(b readBuilder, s *radosclient.ReadStep) finisher {
	buf := s.Buf
	if buf == nil {
		buf = make([]byte, s.Length)
	}
	st := b.Read(s.Offset, buf)
	r := s.Result
	return func(o outcome) {
		if o.opErr != nil {
			r.Err = o.opErr
			return
		}
		// The read's result step never reports an error of its own; its
		// return value is the read's rval.
		if st.Result < 0 {
			r.Err = &radosclient.Error{Errno: errnoValue(st.Result), Op: o.name}
			return
		}
		n := min(int(st.BytesRead), len(buf))
		r.Data, r.N = buf[:n], n
	}
}

func statStep(b readBuilder, s *radosclient.StatStep, idx int) finisher {
	st := b.Stat()
	r := s.Result
	return func(o outcome) {
		if err := o.step(idx); err != nil {
			r.Err = err
			return
		}
		r.Size, r.ModTime = st.Size(), st.ModTime()
	}
}

func getXattrsStep(b readBuilder, s *radosclient.GetXattrsStep, idx int) finisher {
	st := b.GetXattrs()
	r := s.Result
	return func(o outcome) {
		if err := o.step(idx); err != nil {
			r.Err = err
			return
		}
		xattrs, err := st.Xattrs()
		if err != nil {
			r.Err = toSeamError("getxattrs", err)
			return
		}
		r.Xattrs = xattrs
	}
}

// omapIter is the Next of go-ceph's omap-values steps.
type omapIter interface {
	Next() (*rados.OmapKeyValue, error)
}

// drainOmap copies every entry out of it; the iterator's C memory is freed
// with the op.
func drainOmap(it omapIter) (map[string][]byte, error) {
	vals := map[string][]byte{}
	for {
		kv, err := it.Next()
		if err != nil {
			return nil, err
		}
		if kv == nil {
			return vals, nil
		}
		vals[kv.Key] = kv.Value
	}
}

func omapGetValsStep(b readBuilder, s *radosclient.OmapGetValsStep, idx int) finisher {
	st := b.GetOmapValues(s.StartAfter, s.FilterPrefix, s.Max)
	r := s.Result
	return func(o outcome) {
		if err := o.step(idx); err != nil {
			r.Err = err
			return
		}
		vals, err := drainOmap(st)
		if err != nil {
			r.Err = toSeamError("omap get vals", err)
			return
		}
		r.Values, r.More = vals, st.More()
	}
}

func omapGetValsByKeysStep(b readBuilder, s *radosclient.OmapGetValsByKeysStep, idx int) finisher {
	st := b.GetOmapValuesByKeys(s.Keys)
	r := s.Result
	return func(o outcome) {
		if err := o.step(idx); err != nil {
			r.Err = err
			return
		}
		vals, err := drainOmap(st)
		if err != nil {
			r.Err = toSeamError("omap get vals by keys", err)
			return
		}
		r.Values = vals
	}
}

func omapGetKeysStep(b readBuilder, s *radosclient.OmapGetKeysStep, idx int) finisher {
	st := b.GetOmapKeys(s.StartAfter, s.Max)
	r := s.Result
	return func(o outcome) {
		if err := o.step(idx); err != nil {
			r.Err = err
			return
		}
		keys, err := st.Keys()
		if err != nil {
			r.Err = toSeamError("omap get keys", err)
			return
		}
		r.Keys, r.More = keys, st.More()
	}
}

func readExecStep(b readBuilder, s *radosclient.ExecStep, idx int) finisher {
	st := b.Exec(s.Class, s.Method, s.In)
	r := s.Result
	return func(o outcome) {
		if err := o.step(idx); err != nil {
			r.Set(nil, -errnoOf(err))
			return
		}
		out, err := st.Bytes()
		if err != nil {
			r.Set(nil, -errnoOf(err))
			return
		}
		r.Set(out, 0)
	}
}

// translateWrite adds op's steps to b in order and returns the finishers that
// fill their results.
func translateWrite(b writeBuilder, op *radosclient.WriteOp) ([]finisher, error) {
	var fs []finisher
	idx := 0
	for i, s := range op.Steps() {
		switch s := s.(type) {
		case *radosclient.AssertExistsStep:
			b.AssertExists()
		case *radosclient.AssertVersionStep:
			b.AssertVersion(s.Version)
		case *radosclient.CmpXattrStep:
			cmp, err := translateCmp(s.Op)
			if err != nil {
				return nil, err
			}
			b.CmpXattr(s.Name, cmp, s.Value)
		case *radosclient.StepFlagsStep:
			// librados asserts that an action precedes the flags, which
			// would abort the process.
			if i == 0 {
				return nil, fmt.Errorf("goceph: step flags %#x before any step: %w", uint32(s.Flags), radosclient.ErrBadOp)
			}
			if s.Flags&^stepFlagsMask != 0 {
				return nil, fmt.Errorf("goceph: step flags %#x: %w", uint32(s.Flags), radosclient.ErrBadOp)
			}
			b.SetFlags(rados.OpFlags(s.Flags))
		case *radosclient.CreateStep:
			if s.Exclusive {
				b.Create(rados.CreateExclusive)
			} else {
				b.Create(rados.CreateIdempotent)
			}
		case *radosclient.RemoveStep:
			b.Remove()
		case *radosclient.WriteFullStep:
			b.WriteFull(s.Data)
			idx++
		case *radosclient.WriteStep:
			b.Write(s.Data, s.Offset)
			idx++
		case *radosclient.AppendStep:
			b.Append(s.Data)
		case *radosclient.ZeroStep:
			b.Zero(s.Offset, s.Length)
		case *radosclient.TruncateStep:
			b.Truncate(s.Offset)
		case *radosclient.SetXattrStep:
			b.SetXattr(s.Name, s.Value)
		case *radosclient.RmXattrStep:
			b.RmXattr(s.Name)
		case *radosclient.OmapSetStep:
			b.SetOmap(s.Values)
		case *radosclient.OmapRmKeysStep:
			b.RmOmapKeys(s.Keys)
		case *radosclient.OmapClearStep:
			b.CleanOmap()
		case *radosclient.OmapCmpStep:
			cmp, err := translateCmp(s.Op)
			if err != nil {
				return nil, err
			}
			b.OmapCmp(s.Key, cmp, s.Value)
			idx++
		case *radosclient.SetAllocHintStep:
			b.SetAllocationHint(s.ExpectedObjectSize, s.ExpectedWriteSize, rados.AllocHintFlags(s.Flags))
		case *radosclient.ExecStep:
			b.Exec(s.Class, s.Method, s.In)
			r, at := s.Result, idx
			// librados's C write-op exec has no output buffer, so a write
			// exec reports only its return value.
			fs = append(fs, func(o outcome) { r.Set(nil, -errnoOf(o.step(at))) })
			idx++
		default:
			return nil, fmt.Errorf("goceph: %T in a write op: %w", s, radosclient.ErrBadOp)
		}
	}
	return fs, nil
}

// stepFlagsMask is every step flag the seam defines.
const stepFlagsMask = radosclient.StepFlagExcl | radosclient.StepFlagFailOK |
	radosclient.StepFlagFAdviseRandom | radosclient.StepFlagFAdviseSequential |
	radosclient.StepFlagFAdviseWillNeed | radosclient.StepFlagFAdviseDontNeed |
	radosclient.StepFlagFAdviseNoCache

func translateCmp(op radosclient.CmpOp) (rados.CmpXattrOp, error) {
	switch op {
	case radosclient.CmpEQ:
		return rados.CmpXattrOpEq, nil
	case radosclient.CmpNE:
		return rados.CmpXattrOpNe, nil
	case radosclient.CmpGT:
		return rados.CmpXattrOpGt, nil
	case radosclient.CmpGTE:
		return rados.CmpXattrOpGte, nil
	case radosclient.CmpLT:
		return rados.CmpXattrOpLt, nil
	case radosclient.CmpLTE:
		return rados.CmpXattrOpLte, nil
	default:
		return 0, fmt.Errorf("goceph: comparison operator %d: %w", op, radosclient.ErrBadOp)
	}
}

var opFlags = []struct {
	seam  radosclient.OpFlags
	rados rados.OperationFlags
}{
	{radosclient.OpFlagBalanceReads, rados.OperationBalanceReads},
	{radosclient.OpFlagLocalizeReads, rados.OperationLocalizeReads},
	{radosclient.OpFlagIgnoreCache, rados.OperationIgnoreCache},
	{radosclient.OpFlagFullTry, rados.OperationFullTry},
	{radosclient.OpFlagReturnVec, rados.OperationReturnVec},
}

func translateFlags(f radosclient.OpFlags) (rados.OperationFlags, error) {
	out := rados.OperationNoFlag
	for _, m := range opFlags {
		if f&m.seam != 0 {
			out |= m.rados
			f &^= m.seam
		}
	}
	if f != 0 {
		return 0, fmt.Errorf("goceph: op flags %#x: %w", uint32(f), radosclient.ErrBadOp)
	}
	return out, nil
}

// errorCoder is how go-ceph's errors report their errno, negative as
// librados returns it.
type errorCoder interface {
	error
	ErrorCode() int
}

// toSeamError maps a go-ceph error to the seam's: an *radosclient.Error for
// anything carrying an errno, which errors.Is matches against the sentinels,
// and a wrapped error otherwise. An OperationError maps to the op's own
// error, or else to the first failing step in step order.
func toSeamError(op string, err error) error {
	if err == nil {
		return nil
	}
	if oe, ok := errors.AsType[rados.OperationError](err); ok {
		if oe.OpError != nil {
			return toSeamError(op, oe.OpError)
		}
		for _, i := range slices.Sorted(maps.Keys(oe.StepErrors)) {
			if e := oe.StepErrors[i]; e != nil {
				return toSeamError(op, e)
			}
		}
	}
	if ec, ok := errors.AsType[errorCoder](err); ok {
		return &radosclient.Error{Errno: errnoValue(ec.ErrorCode()), Op: op}
	}
	return fmt.Errorf("rados: %s: %w", op, err)
}

// errnoValue is the positive errno of a librados return code.
func errnoValue(code int) int32 {
	if code < 0 {
		code = -code
	}
	if code > math.MaxInt32 {
		return int32(syscall.EIO)
	}
	return int32(code) //nolint:gosec // bounded to MaxInt32 above
}

// errnoOf returns the positive errno err carries, EIO for an error without
// one, and 0 for nil.
func errnoOf(err error) int32 {
	if err == nil {
		return 0
	}
	if seamErr, ok := errors.AsType[*radosclient.Error](err); ok {
		return errnoValue(int(seamErr.Errno))
	}
	if ec, ok := errors.AsType[errorCoder](err); ok {
		return errnoValue(ec.ErrorCode())
	}
	return int32(syscall.EIO)
}

// opName names an operation on oid for the seam's errors.
func opName(kind, oid string) string {
	return kind + " " + strconv.Quote(oid)
}
