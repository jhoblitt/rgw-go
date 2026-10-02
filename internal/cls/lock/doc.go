// Package lock is the client of the RADOS object class "lock"
// (src/cls/lock), which keeps each named advisory lock on an object in its
// "lock.<name>" xattr. Request functions add one exec step to a caller's op
// and encode the request as src/cls/lock/cls_lock_client.cc does; GetInfo
// returns a result to decode once the op has run. Every request and the reply
// are (1,1) at v19.2.6 and v20.2.4, so the release argument selects nothing
// yet.
//
// The seam's Pool.LockExclusive takes a lock in an op of its own. radosgw's
// CompleteMultipartUpload sends assert_exists and the lock in one write op
// instead (MPRadosSerializer::try_lock), and LockExisting spells that pair:
// lock.lock stores the lock by setting the object's "lock.<name>" xattr,
// which creates a missing object, so without the assertion a meta object
// already removed would come back as an empty lock holder rather than answer
// ENOENT.
//
// # Holders and cookies
//
// A holder is the requesting client's entity name, client.<global id>, with
// a cookie, so one client holds a lock once per cookie. Unlock and
// AssertLocked speak for the requesting client; BreakLock names the holder it
// releases. radosgw's multipart serializer never sets a cookie, so its holder
// is client.<gid> with cookie "": another client gets EBUSY while it holds
// the lock, and the same client asking again gets EEXIST. A holder that dies
// leaves the lock until it expires.
//
// # Ephemeral locks
//
// The class removes the object of an ephemeral lock, TypeExclusiveEphemeral,
// when its last holder unlocks it, and also when a read finds every holder
// expired. get_info and assert_locked are registered without the write flag,
// so on such a lock the OSD fails them with EIO instead of answering
// (https://tracker.ceph.com/issues/80993). rgw-go takes no ephemeral lock and
// never sends GetInfo or AssertLocked for one.
package lock
