package fakerados

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// GCQueueWriteMethods are the methods of the rgw_gc class registered with
// CLS_METHOD_WR (cls_rgw_gc.cc:551-555 at v19.2.6 and v20.2.4), which
// RegisterClass takes with GCQueueClass.
var GCQueueWriteMethods = []string{
	"rgw_gc_queue_init", "rgw_gc_queue_enqueue", "rgw_gc_queue_remove_entries", "rgw_gc_queue_update_entry",
}

// GCQueueClass emulates the rgw_gc class (src/cls/rgw_gc/cls_rgw_gc.cc at
// v19.2.6 and v20.2.4): rgw_gc_queue_init, rgw_gc_queue_enqueue, which
// appends the request's entry, due ExpirationSecs from the class's clock,
// rgw_gc_queue_list_entries and rgw_gc_queue_remove_entries. The class keeps
// its queue in the object's data, and so does the fake, though not in
// cls_queue's layout and without a queue head: each entry follows its
// length. A list marker is the decimal position of the entry it starts at,
// counted from the queue's first entry ever, as cls_queue's offsets stay
// valid when a removal moves the front: the count of entries removed is
// kept in the xattr GCQueueFrontXattr. So an enqueue on an existing shard
// needs no rgw_gc_queue_init, and the init, which writes nothing, finds a
// head only in a queue that holds or has held an entry. Without a head the
// fake keeps no deferred entries, so listing and removal skip none. A
// missing shard is ENOENT, as the class's read of the queue head answers.
// rgw_gc_queue_update_entry is EOPNOTSUPP.
func GCQueueClass() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		switch call.Method {
		case gc.MethodQueueInit:
			return nil, gcQueueInit(call)
		case gc.MethodQueueEnqueue:
			return nil, gcQueueEnqueue(call)
		case gc.MethodQueueListEntries:
			return gcQueueList(call)
		case gc.MethodQueueRemoveEntries:
			return nil, gcQueueRemove(call)
		}
		return nil, -int32(syscall.EOPNOTSUPP)
	}
}

// GCQueueFrontXattr is the xattr in which GCQueueClass counts the entries
// removed from a shard's queue, little-endian in 8 bytes; cls_queue keeps
// the front in its head instead.
const GCQueueFrontXattr = "fakerados.gc_queue_front"

// gcQueueFront is the count of entries removed from o's queue.
func gcQueueFront(o *Object) (uint64, bool) {
	b, ok := o.Xattrs[GCQueueFrontXattr]
	if !ok {
		return 0, true
	}
	if len(b) != 8 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(b), true
}

// gcQueueInit is cls_rgw_gc_queue_init through queue_init
// (cls_rgw_gc.cc:34-58; cls_queue_src.cc:107-138): ENOENT on a missing shard,
// EEXIST on one whose queue has a head.
func gcQueueInit(call *ClassCall) int32 {
	if _, rval := decodeRequest(call.In, gc.DecodeQueueInitOp); rval < 0 {
		return rval
	}
	obj := call.Object()
	if obj == nil {
		return -int32(syscall.ENOENT)
	}
	if _, held := obj.Xattrs[GCQueueFrontXattr]; held || len(obj.Data) > 0 {
		return -int32(syscall.EEXIST)
	}
	return 0
}

// gcQueueEnqueue is cls_rgw_gc_queue_enqueue (cls_rgw_gc.cc:60-95).
func gcQueueEnqueue(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeGCSetEntryOp)
	if rval < 0 {
		return rval
	}
	obj := call.Object()
	if obj == nil {
		return -int32(syscall.ENOENT)
	}
	info := op.Info
	info.Time = call.Now().Add(time.Duration(op.ExpirationSecs) * time.Second)
	entry := encodeSquid(info)
	obj.Data = binary.LittleEndian.AppendUint32(obj.Data, uint32(len(entry))) //nolint:gosec // an entry is far below 4 GiB
	obj.Data = append(obj.Data, entry...)
	return 0
}

// gcQueueList is cls_rgw_gc_queue_list_entries (cls_rgw_gc.cc:97-225): from
// the marker's entry, the front for none, max entries (128 for 0) are
// counted and those due by the class's clock, or all of them, returned, with
// the next entry's marker while more follow. A marker before the front or
// past the end is EINVAL. It reads the stored queue, as cls_cxx_read does.
func gcQueueList(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, gc.DecodeListOp)
	if rval < 0 {
		return nil, rval
	}
	if call.Stored == nil {
		return nil, -int32(syscall.ENOENT)
	}
	entries, ok := gcQueueEntries(call.Stored.Data)
	front, fok := gcQueueFront(call.Stored)
	if !ok || !fok {
		return nil, -int32(syscall.EIO)
	}
	start := 0
	if op.Marker != "" {
		n, err := strconv.ParseUint(op.Marker, 10, 64)
		if err != nil || n < front || n-front > uint64(len(entries)) {
			return nil, -int32(syscall.EINVAL)
		}
		start = int(n - front) //nolint:gosec // within len(entries)
	}
	maxEntries := int(op.Max)
	if maxEntries == 0 {
		maxEntries = gc.ListDefaultMax
	}
	end := min(start+maxEntries, len(entries))
	now := call.Now()
	ret := gc.ListRet{}
	for _, info := range entries[start:end] {
		if !op.ExpiredOnly || !info.Time.After(now) {
			ret.Entries = append(ret.Entries, info)
		}
	}
	if end < len(entries) {
		ret.Truncated, ret.NextMarker = true, strconv.FormatUint(front+uint64(end), 10) //nolint:gosec // end is a length
	}
	return encodeSquid(ret), 0
}

// gcQueueRemove is cls_rgw_gc_queue_remove_entries
// (cls_rgw_gc.cc:227-375): the oldest NumEntries entries (128 for 0) go,
// every entry when fewer remain, and the front moves past them.
func gcQueueRemove(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, gc.DecodeQueueRemoveEntriesOp)
	if rval < 0 {
		return rval
	}
	obj := call.Object()
	if call.Stored == nil || obj == nil {
		return -int32(syscall.ENOENT)
	}
	front, ok := gcQueueFront(obj)
	if !ok {
		return -int32(syscall.EIO)
	}
	n := op.NumEntries
	if n == 0 {
		n = gc.ListDefaultMax
	}
	rest := obj.Data
	for ; n > 0 && len(rest) >= 4; n-- {
		size := binary.LittleEndian.Uint32(rest)
		rest = rest[4:]
		if uint64(len(rest)) < uint64(size) {
			return -int32(syscall.EIO)
		}
		rest = rest[size:]
		front++
	}
	obj.Data = slices.Clone(rest)
	obj.Xattrs[GCQueueFrontXattr] = binary.LittleEndian.AppendUint64(nil, front)
	return 0
}

// gcQueueEntries decodes a queue in the fake's framing, oldest first, and
// reports whether it decoded.
func gcQueueEntries(data []byte) ([]rgwcls.GCObjInfo, bool) {
	var out []rgwcls.GCObjInfo
	for rest := data; len(rest) > 0; {
		if len(rest) < 4 {
			return nil, false
		}
		n := binary.LittleEndian.Uint32(rest)
		rest = rest[4:]
		if uint64(len(rest)) < uint64(n) {
			return nil, false
		}
		d := denc.NewDecoder(slices.Clone(rest[:n]))
		info := rgwcls.DecodeGCObjInfo(d)
		if d.Err() != nil {
			return nil, false
		}
		out = append(out, info)
		rest = rest[n:]
	}
	return out, true
}

// GCEntries decodes the entries GCQueueClass queued on the object, oldest
// first, nil when it has none. A queue that does not decode panics, failing
// the spec.
func (c *Cluster) GCEntries(pool, ns, oid string) []rgwcls.GCObjInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	obj := c.store(pool, ns).objects[oid]
	if obj == nil {
		return nil
	}
	out, ok := gcQueueEntries(obj.Data)
	if !ok {
		panic(fmt.Sprintf("fakerados: gc queue of %s/%s/%s does not decode", pool, ns, oid))
	}
	return out
}
