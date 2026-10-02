package fakerados

import (
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// LockWriteMethods are the methods LockClass emulates that the lock class
// registers with CLS_METHOD_WR (cls_lock.cc:625-633 at v19.2.6 and v20.2.4),
// which RegisterClass takes with LockClass.
var LockWriteMethods = []string{"lock", "unlock", "break_lock"}

// ClientName is the entity every fake request originates from,
// cls_get_request_origin's inst.name: the fake client, named by the global id
// InstanceID reports.
var ClientName = lock.EntityName{Type: lock.EntityTypeClient, Num: instanceID}

// clientAddr is the address the class records for ClientName: 127.0.0.1,
// port 0, nonce 0, typed TYPE_LEGACY as lock_obj types every holder's
// address, in the encoding a current client is sent.
var clientAddr = []byte{
	1,                 // entity_addr_t's marker
	1, 1, 28, 0, 0, 0, // ENCODE_START(1, 1)
	1, 0, 0, 0, // TYPE_LEGACY
	0, 0, 0, 0, // nonce
	16, 0, 0, 0, // sizeof(sockaddr_in)
	2, 0, // AF_INET
	0, 0, // port
	127, 0, 0, 1,
	0, 0, 0, 0, 0, 0, 0, 0,
}

// lockXattrPrefix is LOCK_PREFIX, the prefix of the xattr each named lock
// is kept in.
const lockXattrPrefix = "lock."

// LockClass emulates the lock class (src/cls/lock/cls_lock.cc at v19.2.6 and
// v20.2.4) over the object's "lock.<name>" xattr, which holds an encoded
// lock.Info: lock, with lock_obj's EEXIST, EBUSY, ENOENT and EINVAL rules,
// unlock, break_lock, get_info and assert_locked. The requester is always
// ClientName. A holder expires by the cluster's clock, which SetClock sets,
// and every method drops expired holders as it reads the lock. Like the
// class, a lock on a missing object creates it, and an ephemeral lock's
// object is removed when its last holder unlocks it or is found expired; the
// read-only get_info and assert_locked therefore fail with EIO on an expired
// ephemeral lock, as on an OSD (tracker #80993). set_cookie and list_locks
// are EOPNOTSUPP.
func LockClass() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		switch call.Method {
		case "lock":
			op, rval := decodeRequest(call.In, lock.DecodeLockOp)
			if rval < 0 {
				return nil, rval
			}
			return nil, lockObj(call, op)
		case "unlock":
			op, rval := decodeRequest(call.In, lock.DecodeUnlockOp)
			if rval < 0 {
				return nil, rval
			}
			return nil, removeLock(call, op.Name, ClientName, op.Cookie)
		case "break_lock":
			op, rval := decodeRequest(call.In, lock.DecodeBreakOp)
			if rval < 0 {
				return nil, rval
			}
			return nil, removeLock(call, op.Name, op.Locker, op.Cookie)
		case "get_info":
			op, rval := decodeRequest(call.In, lock.DecodeGetInfoOp)
			if rval < 0 {
				return nil, rval
			}
			info, rval := readLock(call, op.Name)
			if rval < 0 {
				return nil, rval
			}
			e := denc.NewEncoder()
			info.Encode(e, denc.Squid)
			return e.Bytes(), 0
		case "assert_locked":
			op, rval := decodeRequest(call.In, lock.DecodeAssertOp)
			if rval < 0 {
				return nil, rval
			}
			return nil, assertLocked(call, op)
		}
		return nil, -int32(syscall.EOPNOTSUPP)
	}
}

// validLockType is cls_lock_is_valid.
func validLockType(t lock.Type) bool {
	return t == lock.TypeExclusive || t == lock.TypeShared || t == lock.TypeExclusiveEphemeral
}

// readLock is read_lock: ENOENT for a missing object, an empty lock for a
// missing xattr, EIO for one that does not decode, and the lock without its
// expired holders otherwise. An ephemeral lock left without holders has its
// object removed. The xattr is read from the stored object, as
// cls_cxx_getxattr reads it.
func readLock(call *ClassCall, name string) (info lock.Info, rval int32) {
	info = lock.Info{Lockers: map[lock.LockerID]lock.LockerInfo{}}
	if call.Stored == nil {
		return info, -int32(syscall.ENOENT)
	}
	b, ok := call.Stored.Xattrs[lockXattrPrefix+name]
	if !ok {
		return info, 0
	}
	d := denc.NewDecoder(b)
	info = lock.DecodeInfo(d)
	if d.Err() != nil {
		return lock.Info{Lockers: map[lock.LockerID]lock.LockerInfo{}}, -int32(syscall.EIO)
	}
	now := call.Now()
	for id, li := range info.Lockers {
		if !li.Expiration.IsZero() && li.Expiration.Before(now) {
			delete(info.Lockers, id)
		}
	}
	if len(info.Lockers) == 0 && info.Type == lock.TypeExclusiveEphemeral {
		// clean_lock's failure is logged and ignored.
		call.Remove()
	}
	return info, 0
}

// writeLock is write_lock: the encoded lock in its xattr, creating the
// object.
func writeLock(call *ClassCall, name string, info lock.Info) {
	e := denc.NewEncoder()
	info.Encode(e, denc.Squid)
	call.Create().Xattrs[lockXattrPrefix+name] = e.Bytes()
}

// lockObj is lock_obj (cls_lock.cc:134-244), with its checks in its order.
func lockObj(call *ClassCall, op lock.LockOp) int32 {
	failIfExists := op.Flags&lock.FlagMayRenew == 0
	failIfAbsent := op.Flags&lock.FlagMustRenew != 0
	if !validLockType(op.Type) || op.Name == "" || (!failIfExists && failIfAbsent) {
		return -int32(syscall.EINVAL)
	}
	info, rval := readLock(call, op.Name)
	if rval < 0 && rval != -int32(syscall.ENOENT) {
		return rval
	}
	id := lock.LockerID{Locker: ClientName, Cookie: op.Cookie}
	if len(info.Lockers) > 0 && op.Tag != info.Tag {
		return -int32(syscall.EBUSY)
	}
	if _, held := info.Lockers[id]; held {
		if failIfExists && !failIfAbsent {
			return -int32(syscall.EEXIST)
		}
		delete(info.Lockers, id)
	} else if failIfAbsent {
		return -int32(syscall.ENOENT)
	}
	if len(info.Lockers) > 0 {
		exclusive := op.Type == lock.TypeExclusive || op.Type == lock.TypeExclusiveEphemeral
		if exclusive || info.Type != op.Type {
			return -int32(syscall.EBUSY)
		}
	}
	info.Type, info.Tag = op.Type, op.Tag
	var expiration time.Time
	if op.Duration != 0 {
		expiration = call.Now().Add(op.Duration)
	}
	info.Lockers[id] = lock.LockerInfo{Expiration: expiration, Addr: clientAddr, Description: op.Description}
	writeLock(call, op.Name, info)
	return 0
}

// removeLock is remove_lock (cls_lock.cc:282-315): ENOENT unless locker holds
// the lock under cookie; an ephemeral lock's object goes with its holder.
func removeLock(call *ClassCall, name string, locker lock.EntityName, cookie string) int32 {
	info, rval := readLock(call, name)
	if rval < 0 {
		return rval
	}
	id := lock.LockerID{Locker: locker, Cookie: cookie}
	if _, ok := info.Lockers[id]; !ok {
		return -int32(syscall.ENOENT)
	}
	delete(info.Lockers, id)
	if info.Type == lock.TypeExclusiveEphemeral {
		return call.Remove()
	}
	writeLock(call, name, info)
	return 0
}

// assertLocked is assert_locked (cls_lock.cc:462-521): EBUSY unless ClientName
// holds the lock under the cookie with the type and tag asked for.
func assertLocked(call *ClassCall, op lock.AssertOp) int32 {
	if !validLockType(op.Type) || op.Name == "" {
		return -int32(syscall.EINVAL)
	}
	info, rval := readLock(call, op.Name)
	if rval < 0 {
		return rval
	}
	if len(info.Lockers) == 0 || info.Type != op.Type || info.Tag != op.Tag {
		return -int32(syscall.EBUSY)
	}
	if _, ok := info.Lockers[lock.LockerID{Locker: ClientName, Cookie: op.Cookie}]; !ok {
		return -int32(syscall.EBUSY)
	}
	return 0
}
