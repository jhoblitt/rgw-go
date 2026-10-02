package lock

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The class and the methods its CLS_INIT registers (cls_lock.cc:611-648 at
// v19.2.6; the file is identical at v20.2.4). set_cookie and list_locks are
// not bound: radosgw never calls them.
const (
	Class              = "lock"
	methodLock         = "lock"
	methodUnlock       = "unlock"
	methodBreakLock    = "break_lock"
	methodGetInfo      = "get_info"
	methodAssertLocked = "assert_locked"
)

// Lock adds lock.lock: it takes p.Name for the request's client identity
// under p.Cookie, failing with EEXIST when that identity already holds it
// without MayRenew, EBUSY when another holder or tag stands in the way, ENOENT
// with MustRenew when it does not hold it (lock_obj, cls_lock.cc:134-244). A
// lock on a missing object CREATES the object; callers that need the object
// to exist use LockExisting.
func Lock(op radosclient.Execer, p LockOp, r denc.Release) {
	op.Exec(Class, methodLock, clsutil.Encode(p, r))
}

// LockExisting is MPRadosSerializer::try_lock's composition
// (rgw_sal_rados.cc:3750-3760 at v19.2.6, :4613 at v20.2.4): assert_exists,
// then lock.lock for an exclusive lock, in the one write op, so a vanished
// object answers ENOENT rather than being recreated as a bare lock holder.
func LockExisting(op *radosclient.WriteOp, name, cookie, description string, duration time.Duration, r denc.Release) { //nolint:revive // Lock's variant, read beside it
	op.AssertExists()
	Lock(op, LockOp{Name: name, Type: TypeExclusive, Cookie: cookie, Description: description, Duration: duration}, r)
}

// Unlock adds lock.unlock for this client's hold under cookie; ENOENT when it
// holds none (remove_lock, cls_lock.cc:282-315).
func Unlock(op radosclient.Execer, name, cookie string, r denc.Release) {
	op.Exec(Class, methodUnlock, clsutil.Encode(UnlockOp{Name: name, Cookie: cookie}, r))
}

// BreakLock adds lock.break_lock, releasing locker's hold under cookie;
// ENOENT when locker holds none under it.
func BreakLock(op radosclient.Execer, name string, locker EntityName, cookie string, r denc.Release) {
	op.Exec(Class, methodBreakLock, clsutil.Encode(BreakOp{Name: name, Locker: locker, Cookie: cookie}, r))
}

// AssertLocked adds lock.assert_locked: the op fails with EBUSY unless this
// client holds name under cookie and tag with type typ. Never send it for an
// ephemeral lock (see Type).
func AssertLocked(op radosclient.Execer, name string, typ Type, cookie, tag string, r denc.Release) {
	op.Exec(Class, methodAssertLocked, clsutil.Encode(AssertOp{Name: name, Type: typ, Cookie: cookie, Tag: tag}, r))
}

// InfoResult is a pending get_info reply.
type InfoResult struct{ res *radosclient.ExecResult }

// GetInfo adds lock.get_info on a read op. Never send it for an ephemeral lock
// (see Type).
func GetInfo(op *radosclient.ReadOp, name string, r denc.Release) *InfoResult {
	return &InfoResult{res: op.Exec(Class, methodGetInfo, clsutil.Encode(GetInfoOp{Name: name}, r))}
}

// Info decodes the reply once the op has run: the unexpired holders, the
// lock's type and its tag. A lock never taken reads as TypeNone without
// holders; one whose holders all unlocked or expired keeps the type and tag
// it was last taken with.
func (res *InfoResult) Info() (Info, error) {
	return clsutil.DecodeReply(res.res, Class, methodGetInfo, DecodeInfo)
}
