package meta

import (
	"cmp"
	"encoding/base64"
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
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2441-L2444 at v19.2.6).
func dumpPool(f formatter.Formatter, name string, p Pool) { f.DumpString(name, p.String()) }

// dumpMap is encode_json of a std::map: an array of "entry" sections, each
// with a "key" and a "val"
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.h#L596-L607 at v19.2.6),
// in key order.
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
// object section
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.h#L496-L502 at v19.2.6).
func dumpSection(f formatter.Formatter, name string, v Dumper, rel denc.Release) {
	f.OpenObjectSection(name)
	v.Dump(f, rel)
	f.CloseSection()
}

// Dump is RGWQuotaInfo::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_quota.cc#L1034-L1042 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_quota.cc#L1032-L1040 at v20.2.4):
// max_size_kb rounds the signed size, unlike the encoded field.
func (q Quota) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpBool("enabled", q.Enabled)
	f.DumpBool("check_on_raw", q.CheckOnRaw)
	f.DumpInt("max_size", q.MaxSize)
	f.DumpInt("max_size_kb", roundedKB(q.MaxSize))
	f.DumpInt("max_objects", q.MaxObjects)
}

// DumpAs is encode_json(name, RGWUserCaps), RGWUserCaps::dump(f, name)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2446-L2449
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2016-L2043 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2508-L2511
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2079-L2106 at v20.2.4):
// an array of "cap" sections in type order, each the type and its
// permission words.
func (c Caps) DumpAs(f formatter.Formatter, name string) {
	f.OpenArraySection(name)
	for _, typ := range slices.Sorted(maps.Keys(c)) {
		f.OpenObjectSection("cap")
		f.DumpString("type", typ)
		f.DumpString("perm", capPermString(c[typ]))
		f.CloseSection()
	}
	f.CloseSection()
}

// Dump is RGWUserCaps::dump(f), which names the array "caps"
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2002-L2005 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2065-L2068 at v20.2.4).
func (c Caps) Dump(f formatter.Formatter, _ denc.Release) { c.DumpAs(f, "caps") }

// dumpTime is encode_json of a utime_t or ceph::real_time: utime_t::gmtime
// through dump_stream
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.cc#L577-L590 at v19.2.6
// and v20.2.4).
func dumpTime(f formatter.Formatter, name string, t Time) { f.DumpStream(name, t.Gmtime()) }

// dumpStrings is encode_json of a std::list, std::set or flat_set of
// strings: an array of "obj" strings
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.h#L533-L573 at v19.2.6).
func dumpStrings(f formatter.Formatter, name string, ss []string) {
	f.OpenArraySection(name)
	for _, s := range ss {
		f.DumpString("obj", s)
	}
	f.CloseSection()
}

// dumpAttrs is encode_json of a std::map<std::string, bufferlist>: "entry"
// sections of "key" and "val"
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.h#L596-L607 at v19.2.6),
// each value base64 with padding and no line breaks
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.cc#L592-L603 at v19.2.6
// and v20.2.4).
func dumpAttrs(f formatter.Formatter, name string, attrs map[string][]byte) {
	dumpMap(f, name, attrs,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v []byte) { f.DumpString("val", base64.StdEncoding.EncodeToString(v)) })
}

// Dump is the body of encode_json(name, obj_version)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L41-L47 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L37-L43 at v20.2.4).
func (v ObjVersion) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("tag", v.Tag)
	f.DumpUnsigned("ver", v.Ver)
}
