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
	ErrNULName       = errors.New("rados: name holds a NUL byte")   // an object, locator, xattr or omap bound, or a service-map name, key or value, librados's C API would cut at it
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

// ReplyVersion reports whether err, the error of a failed write, is one only
// an OSD's reply carries, so that the version Pool's Write returned beside
// it is that reply's. They are:
//   - ENOENT, the removal of an object that does not exist
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L8232-L8233 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8309-L8310 at v20.2.4);
//   - EEXIST, an exclusive create of one that does
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6865-L6872 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6934-L6941 at v20.2.4);
//   - ECANCELED, a CMPXATTR that compared false
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6446-L6449 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6515-L6518 at v20.2.4)
//     or an OMAP_CMP
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7845-L7862 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7922-L7939 at v20.2.4);
//   - ERANGE and EOVERFLOW, an ASSERT_VER below or above the object's version
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6460-L6471 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6529-L6540 at v20.2.4).
//
// Any other error, a timeout, a lost connection, a context's or one the
// implementation reports itself, may come with no reply: its version is
// unknown, and Write returns 0 for it, which no caller may complete an index
// entry at.
//
// One ENOENT comes with no reply: the Objecter ends an op whose pool has been
// deleted with ENOENT itself
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osdc/Objecter.cc#L1556-L1582 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.cc#L1594-L1620 at v20.2.4),
// and librados then reports the version it started with: 0 for an
// asynchronous completion
// (https://github.com/ceph/ceph/blob/v19.2.7/src/librados/AioCompletionImpl.h#L31 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/librados/AioCompletionImpl.h#L31 at v20.2.4),
// and for the blocking operate a local it never set
// (https://github.com/ceph/ceph/blob/v19.2.7/src/librados/IoCtxImpl.cc#L639-L680 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/librados/IoCtxImpl.cc#L646-L687 at v20.2.4).
// The errno cannot tell it from an OSD's, so ReplyVersion takes it for one.
// radosgw's removal completes at that same version (docs/cgo-limitations.md,
// "A synchronous op's version is undefined when no reply came").
func ReplyVersion(err error) bool {
	e, ok := errors.AsType[*Error](err)
	if !ok {
		return false
	}
	switch e.errno() {
	case int32(syscall.ENOENT), int32(syscall.EEXIST), int32(syscall.ECANCELED), int32(syscall.ERANGE), int32(syscall.EOVERFLOW):
		return true
	}
	return false
}
