package radosclient

import (
	"errors"
	"strconv"
	"syscall"
)

// Sentinels for the common RADOS failures; an *Error matches the one for its errno.
var (
	ErrNotFound       = errors.New("rados: not found")               // ENOENT
	ErrExists         = errors.New("rados: exists")                  // EEXIST
	ErrCanceled       = errors.New("rados: canceled")                // ECANCELED
	ErrNoSpace        = errors.New("rados: no space")                // ENOSPC
	ErrPermission     = errors.New("rados: operation not permitted") // EPERM
	ErrNoData         = errors.New("rados: no data")                 // ENODATA
	ErrRange          = errors.New("rados: range")                   // ERANGE
	ErrTooBig         = errors.New("rados: too big")                 // EFBIG
	ErrInvalid        = errors.New("rados: invalid argument")        // EINVAL
	ErrTimedOut       = errors.New("rados: timed out")               // ETIMEDOUT
	ErrBusyResharding = errors.New("rados: bucket is resharding")    // 2300, cls_rgw's ERR_BUSY_RESHARDING
	ErrIncomplete     = errors.New("rados: operation has not run")   // no errno: a result read before its op ran
	ErrNotSupported   = errors.New("rados: operation not supported") // EOPNOTSUPP
)

// Local failures, which an implementation reports without asking the cluster.
// No *Error matches them, so they cannot pass for an OSD's answer: ErrInvalid
// and ErrNotSupported stand for EINVAL and EOPNOTSUPP from the cluster.
var (
	ErrBadOp         = errors.New("rados: bad operation")           // a step, flag or mode the implementation cannot translate
	ErrClosed        = errors.New("rados: closed")                  // the Pool or Cluster was closed
	ErrReleaseTooOld = errors.New("rados: release below the floor") // the cluster requires a release older than Squid
	ErrNULName       = errors.New("rados: name holds a NUL byte")   // an object, locator, xattr or omap bound librados's C API would cut at it
)

// errBusyResharding is cls_rgw's ERR_BUSY_RESHARDING, which has no syscall constant.
const errBusyResharding = 2300

var sentinels = map[int32]error{
	int32(syscall.ENOENT):     ErrNotFound,
	int32(syscall.EEXIST):     ErrExists,
	int32(syscall.ECANCELED):  ErrCanceled,
	int32(syscall.ENOSPC):     ErrNoSpace,
	int32(syscall.EPERM):      ErrPermission,
	int32(syscall.ENODATA):    ErrNoData,
	int32(syscall.ERANGE):     ErrRange,
	int32(syscall.EFBIG):      ErrTooBig,
	int32(syscall.EINVAL):     ErrInvalid,
	int32(syscall.ETIMEDOUT):  ErrTimedOut,
	int32(syscall.EOPNOTSUPP): ErrNotSupported,
	errBusyResharding:         ErrBusyResharding,
}

// Error is a RADOS failure: the errno and the operation that returned it. A
// negative Errno, as librados reports it, is treated as its absolute value.
type Error struct {
	Errno int32
	Op    string
}

// Error renders the operation name and the errno text.
func (e *Error) Error() string {
	n := e.errno()
	var text string
	switch {
	case n == errBusyResharding:
		text = "bucket is resharding"
	case n > 0:
		text = syscall.Errno(n).Error()
	default:
		text = "errno " + strconv.Itoa(int(n))
	}
	if e.Op == "" {
		return "rados: " + text
	}
	return "rados: " + e.Op + ": " + text
}

// Is reports whether target is the sentinel for e's errno.
func (e *Error) Is(target error) bool {
	s, ok := sentinels[e.errno()]
	return ok && s == target
}

func (e *Error) errno() int32 {
	if e.Errno < 0 {
		return -e.Errno
	}
	return e.Errno
}
