package radosclient

// LockFlags modify an advisory lock request.
type LockFlags uint8

// LockRenew renews a lock the caller already holds instead of failing with EEXIST.
const LockRenew LockFlags = 1

// OpFlags modify how a compound operation is dispatched.
type OpFlags uint32

// Operation flags; the values are librados's LIBRADOS_OPERATION_* bits.
const (
	OpFlagNone          OpFlags = 0
	OpFlagBalanceReads  OpFlags = 1 << 0
	OpFlagLocalizeReads OpFlags = 1 << 1
	OpFlagIgnoreCache   OpFlags = 1 << 3
	// OpFlagFullTry is LIBRADOS_OPERATION_FULL_TRY (librados.h:129). Without
	// it the Objecter holds a write to a full pool, or one at its quota, until
	// the pool has room; with it the OSD runs the op, which fails with EDQUOT
	// or ENOSPC only when it would add bytes or objects, so a delete succeeds.
	// It is the op-scoped form of the full-try radosgw sets through
	// set_pool_full_try on every I/O context. Every op through a Pool already
	// runs with full-try, so on a goceph Pool the flag is redundant; it
	// remains for op-scoped use, and fakerados accepts it.
	OpFlagFullTry   OpFlags = 1 << 6
	OpFlagReturnVec OpFlags = 1 << 10 // LIBRADOS_OPERATION_RETURNVEC
)

// StepFlags modify one step of a write op.
type StepFlags uint32

// Step flags; the values are the LIBRADOS_OP_FLAG_* bits librados's
// get_op_flags passes to the OSD, which drops FADVISE_FUA.
const (
	StepFlagExcl              StepFlags = 0x1
	StepFlagFailOK            StepFlags = 0x2 // the op succeeds even when this step fails
	StepFlagFAdviseRandom     StepFlags = 0x4
	StepFlagFAdviseSequential StepFlags = 0x8
	StepFlagFAdviseWillNeed   StepFlags = 0x10
	StepFlagFAdviseDontNeed   StepFlags = 0x20
	StepFlagFAdviseNoCache    StepFlags = 0x40
)

// CmpOp is the comparison a CmpXattr or OmapCmp step applies.
type CmpOp uint8

// Comparison operators; the values are librados's LIBRADOS_CMPXATTR_OP_* codes.
const (
	CmpEQ  CmpOp = 1
	CmpNE  CmpOp = 2
	CmpGT  CmpOp = 3
	CmpGTE CmpOp = 4
	CmpLT  CmpOp = 5
	CmpLTE CmpOp = 6
)

// AllocHintFlags are the LIBRADOS_ALLOC_HINT_FLAG_* bits a SetAllocHint step carries.
type AllocHintFlags uint32
