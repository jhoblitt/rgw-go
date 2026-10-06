package meta

import (
	"cmp"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dumper is a value with a C++ dump(): Dump writes its fields into the
// section the caller opened, in the C++ order, as release rel writes them.
type Dumper interface {
	Dump(f formatter.Formatter, rel denc.Release)
}

// dumpPool is encode_json(name, rgw_pool): its string form
// (rgw_common.cc:2441-2444 at v19.2.6).
func dumpPool(f formatter.Formatter, name string, p Pool) { f.DumpString(name, p.String()) }

// dumpMap is encode_json of a std::map: an array of "entry" sections, each
// with a "key" and a "val" (ceph_json.h:596-607 at v19.2.6), in key order.
func dumpMap[K cmp.Ordered, V any](f formatter.Formatter, name string, m map[K]V, key func(formatter.Formatter, K), val func(formatter.Formatter, V)) {
	f.OpenArraySection(name)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		f.OpenObjectSection("entry")
		key(f, k)
		val(f, m[k])
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpSection is encode_json(name, T) for a struct: its dump() inside an
// object section (ceph_json.h:496-502 at v19.2.6).
func dumpSection(f formatter.Formatter, name string, v Dumper, rel denc.Release) {
	f.OpenObjectSection(name)
	v.Dump(f, rel)
	f.CloseSection()
}

// Dump is RGWQuotaInfo::dump (rgw_quota.cc:1034-1042 at v19.2.6, :1032-1040
// at v20.2.4): max_size_kb rounds the signed size, unlike the encoded field.
func (q Quota) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpBool("enabled", q.Enabled)
	f.DumpBool("check_on_raw", q.CheckOnRaw)
	f.DumpInt("max_size", q.MaxSize)
	f.DumpInt("max_size_kb", roundedKB(q.MaxSize))
	f.DumpInt("max_objects", q.MaxObjects)
}
