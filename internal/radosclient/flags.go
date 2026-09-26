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
	OpFlagReturnVec     OpFlags = 1 << 10 // LIBRADOS_OPERATION_RETURNVEC
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
