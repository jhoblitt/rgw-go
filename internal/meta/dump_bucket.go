package meta

import (
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is rgw_data_placement_target::dump (rgw_basic_types.cc:131-136 at
// v19.2.6 and v20.2.4).
func (p DataPlacement) Dump(f formatter.Formatter, _ denc.Release) {
	dumpPool(f, "data_pool", p.DataPool)
	dumpPool(f, "data_extra_pool", p.DataExtraPool)
	dumpPool(f, "index_pool", p.IndexPool)
}

// Dump is rgw_bucket::dump (rgw_basic_types.cc:144-151 at v19.2.6 and
// v20.2.4).
func (b BucketID) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("name", b.Name)
	f.DumpString("marker", b.Marker)
	f.DumpString("bucket_id", b.ID)
	f.DumpString("tenant", b.Tenant)
	dumpSection(f, "explicit_placement", b.ExplicitPlacement, rel)
}
