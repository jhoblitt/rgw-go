package fakerados

import (
	"encoding/binary"
	"fmt"
	"slices"
	"syscall"
	"time"

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
// v19.2.6 and v20.2.4) as far as a write needs it: rgw_gc_queue_enqueue
// appends the request's entry, due ExpirationSecs from the class's clock, to
// the shard's queue. The class keeps its queue in the object's data, and so
// does the fake, though not in cls_queue's layout and without a queue head:
// an enqueue on an existing shard needs no rgw_gc_queue_init. A missing
// shard is ENOENT, as the class's read of the queue head answers. Every
// other method is EOPNOTSUPP.
func GCQueueClass() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		if call.Method != "rgw_gc_queue_enqueue" {
			return nil, -int32(syscall.EOPNOTSUPP)
		}
		op, rval := decodeRequest(call.In, rgwcls.DecodeGCSetEntryOp)
		if rval < 0 {
			return nil, rval
		}
		obj := call.Object()
		if obj == nil {
			return nil, -int32(syscall.ENOENT)
		}
		info := op.Info
		info.Time = call.Now().Add(time.Duration(op.ExpirationSecs) * time.Second)
		entry := encodeSquid(info)
		obj.Data = binary.LittleEndian.AppendUint32(obj.Data, uint32(len(entry))) //nolint:gosec // an entry is far below 4 GiB
		obj.Data = append(obj.Data, entry...)
		return nil, 0
	}
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
	var out []rgwcls.GCObjInfo
	for rest := obj.Data; len(rest) > 0; {
		if len(rest) < 4 {
			panic(fmt.Sprintf("fakerados: gc queue of %s/%s/%s ends inside a length", pool, ns, oid))
		}
		n := binary.LittleEndian.Uint32(rest)
		rest = rest[4:]
		if uint64(len(rest)) < uint64(n) {
			panic(fmt.Sprintf("fakerados: gc queue of %s/%s/%s ends inside an entry", pool, ns, oid))
		}
		d := denc.NewDecoder(slices.Clone(rest[:n]))
		info := rgwcls.DecodeGCObjInfo(d)
		if err := d.Err(); err != nil {
			panic(fmt.Sprintf("fakerados: gc queue entry of %s/%s/%s does not decode: %v", pool, ns, oid, err))
		}
		out = append(out, info)
		rest = rest[n:]
	}
	return out
}
