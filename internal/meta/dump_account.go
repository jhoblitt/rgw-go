package meta

import (
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is RGWAccountInfo::dump (rgw_common.cc:3026-3039 at v19.2.6,
// :3088-3101 at v20.2.4).
func (a AccountInfo) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("id", a.ID)
	f.DumpString("tenant", a.Tenant)
	f.DumpString("name", a.Name)
	f.DumpString("email", a.Email)
	dumpSection(f, "quota", a.Quota, rel)
	dumpSection(f, "bucket_quota", a.BucketQuota, rel)
	f.DumpInt("max_users", int64(a.MaxUsers))
	f.DumpInt("max_roles", int64(a.MaxRoles))
	f.DumpInt("max_groups", int64(a.MaxGroups))
	f.DumpInt("max_buckets", int64(a.MaxBuckets))
	f.DumpInt("max_access_keys", int64(a.MaxAccessKeys))
}
