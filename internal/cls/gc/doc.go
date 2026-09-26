// Package gc is the client for "rgw_gc", the object class behind radosgw's
// queue-era garbage-collection log. Each request function adds one exec step
// to a caller's op and encodes its request as the C++ client in
// src/cls/rgw_gc/cls_rgw_gc_client.cc does; ListResult decodes the reply.
// The GC entry itself, cls_rgw_gc_obj_info, belongs to package rgw, whose
// omap-era gc_set_entry takes the same request struct QueueEnqueue sends.
//
// # Shards
//
// radosgw keeps min(rgw_gc_max_objs, rgw_shards_max()) shards, 32 by default
// since rgw_shards_max is 65521, named gc.0 through gc.<n-1>, in the zone's
// gc_pool, which defaults to "<zone>.rgw.log" in
// namespace "gc". The format is per shard: a shard's cls_version is 0 while
// it is omap-era, driven by package rgw's gc methods, and 1 once its queue is
// initialized. radosgw enqueues under a version 1 check and falls back to
// rgw's gc_set_entry when that fails, and it drains any omap-era entries a
// version 1 shard still holds before listing its queue.
//
// # Initializing a shard
//
// QueueInit creates the object when it is missing: rgw_gc_queue_init reads the
// queue head, treats an empty or absent head (EINVAL) as uninitialized, and
// writes a new one. It fails with EEXIST only when a valid head is already
// there; any other object whose head does not decode is overwritten. radosgw
// never runs it bare: RGWGC::initialize issues one write op per shard, which
// a caller composes from this package and package version:
//
//	op.Create(false)
//	version.Check(op, version.ObjVersion{Ver: 0}, version.CondEQ, r)
//	gc.QueueInit(op, rgw_gc_max_queue_size, rgw_gc_max_deferred, r)
//	version.Set(op, version.ObjVersion{Ver: 1}, r)
//
// The version check fails the op with ECANCELED once a shard is queue-era, so
// initialization is idempotent. The create is not exclusive; radosgw's
// notification queues use an exclusive create instead, but GC does not.
//
// On a missing object every write method other than QueueInit fails with
// EINVAL, since it finds no head, while QueueList, a read, fails with ENOENT.
//
// Put at most one write method per op. Each reads the queue head as it was
// before the op and writes it back, so a second enqueue in the same op
// rewrites the head over the first and the first entry is lost.
//
// # Deferral
//
// QueueUpdateEntry does not move an entry: it records the entry's tag and new
// expiry in the cls_rgw_gc_urgent_data held in the queue head, spilling to
// the "cls_queue_urgent_data" xattr when the head is full. QueueList leaves
// out an entry whose urgent time is later than its own. QueueRemoveEntries
// does not count such an entry toward numEntries but still removes it with
// the entries around it, and since the class's re-enqueue on deferral is
// compiled out (Tracker 47866), a deferred entry removed that way is gone for
// good. QueueUpdateEntry fails with ENOSPC once more tags are deferred than
// the num_deferred_entries QueueInit recorded.
package gc
