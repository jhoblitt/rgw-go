package meta

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// CacheNotifyOp is the op of a cache notification, the unnamed enum of
// src/rgw/rgw_cache.h.
type CacheNotifyOp uint32

// The cache notification ops.
const (
	CacheUpdateObj     CacheNotifyOp = 0
	CacheInvalidateObj CacheNotifyOp = 1
)

// The ObjectCacheInfo flags, CACHE_FLAG_*, naming which parts of it are set.
const (
	CacheFlagData         uint32 = 0x01
	CacheFlagXattrs       uint32 = 0x02
	CacheFlagMeta         uint32 = 0x04
	CacheFlagModifyXattrs uint32 = 0x08
	CacheFlagObjVersion   uint32 = 0x10
)

// CacheNotifyInfo is RGWCacheNotifyInfo, the payload radosgw sends through
// watch/notify on the control pool to keep other radosgws' metadata caches
// coherent.
type CacheNotifyInfo struct {
	Op      CacheNotifyOp   `json:"op"`
	Obj     RawObj          `json:"obj"`
	ObjInfo ObjectCacheInfo `json:"obj_info"`
	Ofs     int64           `json:"ofs"`
	NS      string          `json:"ns"`
}

// Encode mirrors RGWCacheNotifyInfo::encode, ENCODE_START(2, 2).
func (n CacheNotifyInfo) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 2)
	e.U32(uint32(n.Op))
	n.Obj.Encode(e, r)
	n.ObjInfo.Encode(e, r)
	e.I64(n.Ofs)
	e.String(n.NS)
	e.EndStruct(f)
}

// DecodeCacheNotifyInfo mirrors RGWCacheNotifyInfo::decode,
// DECODE_START_LEGACY_COMPAT_LEN(2, 2, 2).
func DecodeCacheNotifyInfo(d *denc.Decoder) CacheNotifyInfo {
	h := d.BeginStructLegacy(2, 2, 2, 0)
	var n CacheNotifyInfo
	n.Op = CacheNotifyOp(d.U32())
	n.Obj = DecodeRawObj(d)
	n.ObjInfo = DecodeObjectCacheInfo(d)
	n.Ofs = d.I64()
	n.NS = d.String()
	d.EndStruct(h)
	return n
}

// ObjectCacheInfo is ObjectCacheInfo, one cached system object. The C++ also
// holds time_added, a monotonic clock reading that is never encoded.
type ObjectCacheInfo struct {
	Status   int32             `json:"status"`
	Flags    uint32            `json:"flags"`
	Epoch    uint64            `json:"-"`
	Data     []byte            `json:"-"`
	Xattrs   map[string][]byte `json:"-"`
	RMXattrs map[string][]byte `json:"-"`
	Meta     ObjectMetaInfo    `json:"meta"`
	Version  ObjVersion        `json:"-"`
}

// MarshalJSON renders ObjectCacheInfo::dump in its order, meta last. It
// leaves out the epoch and the version, writes data as base64, and writes
// each attr map as encode_json_map with "name", "value" and "length".
func (c ObjectCacheInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Status   int32          `json:"status"`
		Flags    uint32         `json:"flags"`
		Data     bufferlistJSON `json:"data"`
		Xattrs   []attrJSON     `json:"xattrs"`
		RMXattrs []attrJSON     `json:"rm_xattrs"`
		Meta     ObjectMetaInfo `json:"meta"`
	}{c.Status, c.Flags, c.Data, attrsJSON(c.Xattrs), attrsJSON(c.RMXattrs), c.Meta})
}

// bufferlistJSON is a bufferlist as encode_json writes it: base64, "" when
// empty.
type bufferlistJSON []byte

func (b bufferlistJSON) MarshalJSON() ([]byte, error) {
	if b == nil {
		return json.Marshal("")
	}
	return json.Marshal([]byte(b))
}

type attrJSON struct {
	Name  string `json:"name"`
	Value struct {
		Length bufferlistJSON `json:"length"`
	} `json:"value"`
}

func attrsJSON(m map[string][]byte) []attrJSON {
	out := make([]attrJSON, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		a := attrJSON{Name: k}
		a.Value.Length = m[k]
		out = append(out, a)
	}
	return out
}

// Encode mirrors ObjectCacheInfo::encode, ENCODE_START(5, 3).
func (c ObjectCacheInfo) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(5, 3)
	e.I32(c.Status)
	e.U32(c.Flags)
	e.Bytes32(c.Data)
	denc.EncodeStringMap(e, c.Xattrs)
	c.Meta.Encode(e, r)
	denc.EncodeStringMap(e, c.RMXattrs)
	e.U64(c.Epoch)
	c.Version.Encode(e, r)
	e.EndStruct(f)
}

// DecodeObjectCacheInfo mirrors ObjectCacheInfo::decode,
// DECODE_START_LEGACY_COMPAT_LEN(5, 3, 3): rm_xattrs from version 2, the
// epoch from 4 and the version from 5.
func DecodeObjectCacheInfo(d *denc.Decoder) ObjectCacheInfo {
	h := d.BeginStructLegacy(5, 3, 3, 0)
	var c ObjectCacheInfo
	c.Status = d.I32()
	c.Flags = d.U32()
	c.Data = d.Bytes32()
	c.Xattrs = denc.DecodeStringMap(d)
	c.Meta = DecodeObjectMetaInfo(d)
	if h.Version >= 2 {
		c.RMXattrs = denc.DecodeStringMap(d)
	}
	if h.Version >= 4 {
		c.Epoch = d.U64()
	}
	if h.Version >= 5 {
		c.Version = DecodeObjVersion(d)
	}
	d.EndStruct(h)
	return c
}

// ObjectMetaInfo is ObjectMetaInfo, a cached object's size and mtime.
type ObjectMetaInfo struct {
	Size  uint64 `json:"size"`
	Mtime Time   `json:"mtime"`
}

// Encode mirrors ObjectMetaInfo::encode, ENCODE_START(2, 2).
func (m ObjectMetaInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 2)
	e.U64(m.Size)
	m.Mtime.Encode(e)
	e.EndStruct(f)
}

// DecodeObjectMetaInfo mirrors ObjectMetaInfo::decode,
// DECODE_START_LEGACY_COMPAT_LEN(2, 2, 2).
func DecodeObjectMetaInfo(d *denc.Decoder) ObjectMetaInfo {
	h := d.BeginStructLegacy(2, 2, 2, 0)
	var m ObjectMetaInfo
	m.Size = d.U64()
	m.Mtime = DecodeTime(d)
	d.EndStruct(h)
	return m
}

// ObjVersion is obj_version from src/cls/version/cls_version_types.h, the
// version cls_version keeps on a metadata object. It is duplicated here so
// that meta does not import the object-class packages.
type ObjVersion struct {
	Ver uint64 `json:"ver"`
	Tag string `json:"tag"`
}

// Encode mirrors obj_version::encode, ENCODE_START(1, 1).
func (v ObjVersion) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(v.Ver)
	e.String(v.Tag)
	e.EndStruct(f)
}

// DecodeObjVersion mirrors obj_version::decode, DECODE_START(1).
func DecodeObjVersion(d *denc.Decoder) ObjVersion {
	h := d.BeginStruct(1)
	var v ObjVersion
	v.Ver = d.U64()
	v.Tag = d.String()
	d.EndStruct(h)
	return v
}
