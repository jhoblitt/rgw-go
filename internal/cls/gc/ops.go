package gc

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Class and method names, from src/cls/rgw_gc/cls_rgw_gc_const.h.
const (
	Class                    = "rgw_gc"
	MethodQueueInit          = "rgw_gc_queue_init"
	MethodQueueEnqueue       = "rgw_gc_queue_enqueue"
	MethodQueueListEntries   = "rgw_gc_queue_list_entries"
	MethodQueueRemoveEntries = "rgw_gc_queue_remove_entries"
	MethodQueueUpdateEntry   = "rgw_gc_queue_update_entry"
)

// XattrUrgentData is the xattr deferred tags spill to when the queue head is full.
const XattrUrgentData = "cls_queue_urgent_data"

// ListDefaultMax is the count the class substitutes for a list or remove count of 0.
const ListDefaultMax = 128

// ShardOID returns the name of GC shard i, as RGWGC::initialize builds it.
func ShardOID(i int) string { return fmt.Sprintf("gc.%d", i) }

// QueueInit mirrors cls_rgw_gc_queue_init: it creates the queue head, and the
// object when it is missing, with size bytes of entry space and room for
// numDeferredEntries deferred tags. The head's deferral space is the OSD's
// rgw_gc_max_deferred_entries_size, not a request field. See the package
// documentation for the write op radosgw wraps it in.
func QueueInit(op radosclient.Execer, size, numDeferredEntries uint64, r denc.Release) {
	op.Exec(Class, MethodQueueInit, clsutil.Encode(QueueInitOp{Size: size, NumDeferredEntries: numDeferredEntries}, r))
}

// QueueEnqueue mirrors cls_rgw_gc_queue_enqueue, whose request is package
// rgw's cls_rgw_gc_set_entry_op. The class ignores info.Time and stamps the
// entry with its own clock plus expirationSecs.
func QueueEnqueue(op radosclient.Execer, expirationSecs uint32, info rgw.GCObjInfo, r denc.Release) {
	op.Exec(Class, MethodQueueEnqueue, clsutil.Encode(rgw.GCSetEntryOp{ExpirationSecs: expirationSecs, Info: info}, r))
}

// QueueUpdateEntry mirrors cls_rgw_gc_queue_defer_entry, which calls
// rgw_gc_queue_update_entry: it defers info.Tag to the class's clock plus
// expirationSecs. Only the tag and the new time are recorded.
func QueueUpdateEntry(op radosclient.Execer, expirationSecs uint32, info rgw.GCObjInfo, r denc.Release) {
	op.Exec(Class, MethodQueueUpdateEntry, clsutil.Encode(QueueDeferEntryOp{ExpirationSecs: expirationSecs, Info: info}, r))
}

// QueueRemoveEntries mirrors cls_rgw_gc_queue_remove_entries: it removes
// entries from the front of the queue until numEntries of them that are not
// deferred have gone, along with the deferred ones among them.
func QueueRemoveEntries(op radosclient.Execer, numEntries uint32, r denc.Release) {
	op.Exec(Class, MethodQueueRemoveEntries, clsutil.Encode(QueueRemoveEntriesOp{NumEntries: uint64(numEntries)}, r))
}

// ListResult is the reply of a QueueList step.
type ListResult struct {
	res *radosclient.ExecResult
}

// QueueList mirrors cls_rgw_gc_queue_list_entries: up to maxEntries entries after
// marker, skipping deferred ones, and only those already expired when
// expiredOnly is set. Unexpired entries still count toward maxEntries, so a
// reply can be shorter than maxEntries and truncated: page on Truncated and
// NextMarker, never on the reply's length.
func QueueList(op *radosclient.ReadOp, marker string, maxEntries uint32, expiredOnly bool, r denc.Release) *ListResult {
	in := clsutil.Encode(ListOp{Marker: marker, Max: maxEntries, ExpiredOnly: expiredOnly}, r)
	return &ListResult{res: op.Exec(Class, MethodQueueListEntries, in)}
}

// Result decodes the cls_rgw_gc_list_ret the method returned.
func (l *ListResult) Result() (ListRet, error) {
	return clsutil.DecodeReply(l.res, Class, MethodQueueListEntries, DecodeListRet)
}
