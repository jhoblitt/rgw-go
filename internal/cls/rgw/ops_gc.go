package rgw

import (
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// GCSetEntry adds gc_set_entry, the omap-era enqueue on a gc shard: it
// stores info under its tag, due expirationSecs from now. A shard already
// converted to the rgw_gc queue takes the gc package's enqueue instead.
func GCSetEntry(op radosclient.Execer, expirationSecs uint32, info GCObjInfo, r denc.Release) {
	op.Exec(Class, methodGCSetEntry, encode(GCSetEntryOp{ExpirationSecs: expirationSecs, Info: info}.Encode, r))
}
