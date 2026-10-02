package rgw

import (
	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// GCSetEntry adds gc_set_entry, the omap-era enqueue on a gc shard: it
// stores info under its tag, due expirationSecs from now. A shard already
// converted to the rgw_gc queue takes the gc package's enqueue instead.
func GCSetEntry(op radosclient.Execer, expirationSecs uint32, info GCObjInfo, r denc.Release) {
	op.Exec(Class, methodGCSetEntry, clsutil.Encode(GCSetEntryOp{ExpirationSecs: expirationSecs, Info: info}, r))
}

// GCListResult is the pending output of a gc_list call.
type GCListResult struct {
	res *radosclient.ExecResult
}

// Result decodes the listing once the op has run.
func (l *GCListResult) Result() (GCListRet, error) {
	return clsutil.DecodeReply(l.res, Class, methodGCList, DecodeGCListRet)
}

// GCList adds gc_list, the omap-era listing of a gc shard: up to maxEntries
// entries (128 for 0) after marker, earliest due first, and with expiredOnly
// only those already due. The OSD caps the keys and bytes one omap read
// returns, so a truncated reply can hold fewer than maxEntries: page on
// Truncated and NextMarker, never on the reply's length.
func GCList(op *radosclient.ReadOp, marker string, maxEntries uint32, expiredOnly bool, r denc.Release) *GCListResult {
	in := clsutil.Encode(GCListOp{Marker: marker, Max: maxEntries, ExpiredOnly: expiredOnly}, r)
	return &GCListResult{res: op.Exec(Class, methodGCList, in)}
}

// GCRemove adds gc_remove, which drops the omap-era entries named by tags,
// skipping any tag the shard does not hold.
func GCRemove(op radosclient.Execer, tags []string, r denc.Release) {
	op.Exec(Class, methodGCRemove, clsutil.Encode(GCRemoveOp{Tags: tags}, r))
}

// GCDeferEntry adds gc_defer_entry, which makes tag's omap-era entry due
// expirationSecs after the class's clock. It fails with ENOENT when the shard
// holds no entry for tag.
func GCDeferEntry(op radosclient.Execer, expirationSecs uint32, tag string, r denc.Release) {
	op.Exec(Class, methodGCDeferEntry, clsutil.Encode(GCDeferEntryOp{ExpirationSecs: expirationSecs, Tag: tag}, r))
}
