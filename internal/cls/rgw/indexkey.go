package rgw

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The omap key prefixes of a shard's special namespaces, BI_PREFIX_CHAR and
// bucket_index_prefixes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L51-L66 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L51-L68 at v20.2.4).
// Plain entries, the listing namespace, sort below the 0x80 byte.
const (
	instanceIndexPrefix = "\x801000_"
	olhDataIndexPrefix  = "\x801001_"
)

// InstanceIndexKey is encode_obj_versioned_data_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L325-L337 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L375-L387 at v20.2.4):
// the omap key of a version's instance entry, 0x80 "1000_", the name, NUL
// "i", then the instance. An empty instance gives the null version's data
// key. nullDeleteMarker appends NUL "d", which the class does only for a
// delete marker whose instance is empty, so that the null version's marker
// and data keep separate entries.
func InstanceIndexKey(key ObjKey, nullDeleteMarker bool) string {
	k := instanceIndexPrefix + key.Name + "\x00i" + key.Instance
	if nullDeleteMarker {
		k += "\x00d"
	}
	return k
}

// OLHDataKey is encode_olh_data_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L348-L353 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L398-L403 at v20.2.4):
// the omap key of a name's OLH entry, 0x80 "1001_" then the name.
func OLHDataKey(name string) string {
	return olhDataIndexPrefix + name
}

// ListIndexKey is get_list_index_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L302-L315 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L352-L365 at v20.2.4):
// the plain-namespace key a version is listed under, the name, NUL "v",
// DecreasingStr of its versioned epoch, NUL "i", then the instance, so that a
// name's newer versions list first.
func ListIndexKey(key ObjKey, versionedEpoch uint64) string {
	return key.Name + "\x00v" + DecreasingStr(versionedEpoch) + "\x00i" + key.Instance
}

// DecreasingStr is decreasing_str
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L272-L290 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L322-L340 at v20.2.4).
// Each band is a leading digit and the distance to the band's top, zero
// padded, so a larger num sorts first. From 2^32 the class prints -num as a
// signed long long: up to 2^63 that is negative, so the string carries a
// minus sign and grows with num. From 2^32 on DecodeListIndexKey cannot read
// the list key: the negative form has trailing bytes, and the form above
// 2^63 overflows a long long. That is reproduced, since the class writes
// these keys.
func DecreasingStr(num uint64) string {
	switch {
	case num < 0x10:
		return fmt.Sprintf("9%02d", 15-num)
	case num < 0x100:
		return fmt.Sprintf("8%03d", 255-num)
	case num < 0x1000:
		return fmt.Sprintf("7%04d", 4095-num)
	case num < 0x10000:
		return fmt.Sprintf("6%05d", 65535-num)
	case num < 0x100000000:
		return fmt.Sprintf("5%010d", 0xFFFFFFFF-num)
	default:
		return fmt.Sprintf("4%020d", int64(-num)) //nolint:gosec // the class's (long long)-num, two's-complement
	}
}

// AfterVersions is cls_rgw_after_versions
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L319-L323 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L369-L373 at v20.2.4):
// the name with a 0x01 suffix, which sorts after every ListIndexKey of the
// name. A listing that skips a name's versions resumes there.
func AfterVersions(name string) string {
	return name + "\x01"
}

// DecodeListIndexKey is decode_list_index_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L392-L462 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L442-L512 at v20.2.4),
// which bucket_list applies to each plain-namespace key, skipping one it
// fails. A key without NUL is the name. Otherwise the key splits on NUL, a
// trailing NUL ending the last segment rather than opening an empty one; the
// first segment is the name, at least one must follow, an "i" segment sets
// the instance, the last one winning, and others are skipped. A "v" segment
// must hold a decimal long long, as strict_strtoll reads one, or the key
// fails with denc.ErrMalformed, the class's EIO. The class discards the
// value, so it is not returned.
func DecodeListIndexKey(key string) (ObjKey, error) {
	if !strings.Contains(key, "\x00") {
		return ObjKey{Name: key}, nil
	}
	vals := strings.Split(key, "\x00")
	if vals[len(vals)-1] == "" {
		vals = vals[:len(vals)-1]
	}
	if len(vals) < 2 {
		return ObjKey{}, fmt.Errorf("%w: list index key %q has nothing after its name", denc.ErrMalformed, key)
	}
	k := ObjKey{Name: vals[0]}
	for _, v := range vals[1:] {
		if inst, ok := strings.CutPrefix(v, "i"); ok {
			k.Instance = inst
		} else if ver, ok := strings.CutPrefix(v, "v"); ok && !strictDecimal(ver) {
			return ObjKey{}, fmt.Errorf("%w: list index key %q has version %q", denc.ErrMalformed, key, ver)
		}
	}
	return k, nil
}

// strictDecimal reports whether strict_strtoll(s, 10) succeeds: strtoll's
// leading white space and sign, at least one digit, no trailing bytes, and a
// value in range.
func strictDecimal(s string) bool {
	_, err := strconv.ParseInt(strings.TrimLeft(s, " \t\n\v\f\r"), 10, 64)
	return err == nil
}
