package rgw

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// storageClassStandard is RGW_STORAGE_CLASS_STANDARD, which the dump of an
// entry prints for an empty storage class.
const storageClassStandard = "STANDARD"

// DirEntryMeta is rgw_bucket_dir_entry_meta, the object attributes a bucket
// index entry carries. RestoreStatus and RestoreExpiryDate exist only on
// main (struct version 8): they decode from a v8 entry and are never encoded,
// since Squid and Tentacle both write version 7.
type DirEntryMeta struct {
	Category          uint8
	Size              uint64
	Mtime             time.Time
	ETag              string
	Owner             string
	OwnerDisplayName  string
	ContentType       string
	AccountedSize     uint64
	UserData          string
	StorageClass      string
	AppendableValue   bool
	RestoreStatus     uint8
	RestoreExpiryDate time.Time
}

// Encode mirrors rgw_bucket_dir_entry_meta::encode at ENCODE_START(7, 3),
// the version Squid and Tentacle write. It drops main's version 8
// RestoreStatus and RestoreExpiryDate.
func (m DirEntryMeta) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(7, 3)
	e.U8(m.Category)
	e.U64(m.Size)
	e.Time(m.Mtime)
	e.String(m.ETag)
	e.String(m.Owner)
	e.String(m.OwnerDisplayName)
	e.String(m.ContentType)
	e.U64(m.AccountedSize)
	e.String(m.UserData)
	e.String(m.StorageClass)
	e.Bool(m.AppendableValue)
	e.EndStruct(f)
}

// DecodeDirEntryMeta mirrors rgw_bucket_dir_entry_meta::decode,
// DECODE_START_LEGACY_COMPAT_LEN(8, 3, 3) as main has it. Below version 4
// the accounted size is the size.
func DecodeDirEntryMeta(d *denc.Decoder) DirEntryMeta {
	h := d.BeginStructLegacy(8, 3, 3, 0)
	var m DirEntryMeta
	m.Category = d.U8()
	m.Size = d.U64()
	m.Mtime = d.Time()
	m.ETag = d.String()
	m.Owner = d.String()
	m.OwnerDisplayName = d.String()
	if h.Version >= 2 {
		m.ContentType = d.String()
	}
	if h.Version >= 4 {
		m.AccountedSize = d.U64()
	} else {
		m.AccountedSize = m.Size
	}
	if h.Version >= 5 {
		m.UserData = d.String()
	}
	if h.Version >= 6 {
		m.StorageClass = d.String()
	}
	if h.Version >= 7 {
		m.AppendableValue = d.Bool()
	}
	if h.Version >= 8 {
		m.RestoreStatus = d.U8()
		m.RestoreExpiryDate = d.Time()
	}
	d.EndStruct(h)
	return m
}

// MarshalJSON writes rgw_bucket_dir_entry_meta::dump as Squid and Tentacle
// have it.
func (m DirEntryMeta) MarshalJSON() ([]byte, error) {
	sc := m.StorageClass
	if sc == "" {
		sc = storageClassStandard
	}
	return json.Marshal(struct {
		Category         int             `json:"category"`
		Size             uint64          `json:"size"`
		Mtime            json.RawMessage `json:"mtime"`
		ETag             string          `json:"etag"`
		StorageClass     string          `json:"storage_class"`
		Owner            string          `json:"owner"`
		OwnerDisplayName string          `json:"owner_display_name"`
		ContentType      string          `json:"content_type"`
		AccountedSize    uint64          `json:"accounted_size"`
		UserData         string          `json:"user_data"`
		Appendable       bool            `json:"appendable"`
	}{
		int(m.Category), m.Size, dumpTime(m.Mtime), m.ETag, sc, m.Owner, m.OwnerDisplayName,
		m.ContentType, m.AccountedSize, m.UserData, m.AppendableValue,
	})
}

// PendingInfo is rgw_bucket_pending_info, one prepared but not completed
// operation on an index entry.
type PendingInfo struct {
	State     uint8
	Timestamp time.Time
	Op        uint8
}

// Encode mirrors rgw_bucket_pending_info::encode, ENCODE_START(2, 2).
func (p PendingInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 2)
	e.U8(p.State)
	e.Time(p.Timestamp)
	e.U8(p.Op)
	e.EndStruct(f)
}

// DecodePendingInfo mirrors rgw_bucket_pending_info::decode,
// DECODE_START_LEGACY_COMPAT_LEN(2, 2, 2).
func DecodePendingInfo(d *denc.Decoder) PendingInfo {
	h := d.BeginStructLegacy(2, 2, 2, 0)
	var p PendingInfo
	p.State = d.U8()
	p.Timestamp = d.Time()
	p.Op = d.U8()
	d.EndStruct(h)
	return p
}

// MarshalJSON writes rgw_bucket_pending_info::dump.
func (p PendingInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		State     int             `json:"state"`
		Timestamp json.RawMessage `json:"timestamp"`
		Op        int             `json:"op"`
	}{int(p.State), dumpTime(p.Timestamp), int(p.Op)})
}

// PendingEntry is one element of an entry's pending_map, a
// std::multimap<string, rgw_bucket_pending_info> keyed by the op tag.
type PendingEntry struct {
	Tag  string      `json:"key"`
	Info PendingInfo `json:"val"`
}

// sortPending orders entries as std::multimap iterates them: by tag, equal
// tags in insertion order.
func sortPending(ps []PendingEntry) []PendingEntry {
	ps = slices.Clone(ps)
	slices.SortStableFunc(ps, func(a, b PendingEntry) int { return strings.Compare(a.Tag, b.Tag) })
	return ps
}

// DirEntry is rgw_bucket_dir_entry, one bucket index entry. PendingMap is a
// multimap, so it is a slice: cls_rgw's prepare inserts rather than replaces,
// and a tag can appear more than once. Encode writes it sorted by tag, equal
// tags in slice order, and decode returns it in that order. The C++ default
// has Ver.Pool -1; NewDirEntry returns it.
type DirEntry struct {
	Key            ObjKey
	Ver            EntryVer
	Exists         bool
	Meta           DirEntryMeta
	PendingMap     []PendingEntry
	Locator        string
	IndexVer       uint64
	Tag            string
	Flags          uint16
	VersionedEpoch uint64
}

// NewDirEntry returns the rgw_bucket_dir_entry default.
func NewDirEntry() DirEntry { return DirEntry{Ver: NewEntryVer()} }

// Encode mirrors rgw_bucket_dir_entry::encode, ENCODE_START(8, 3). The key
// name leads and the instance trails; the epoch is written twice, alone and
// inside ver; index_ver is a packed value.
func (en DirEntry) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(8, 3)
	e.String(en.Key.Name)
	e.U64(en.Ver.Epoch)
	e.Bool(en.Exists)
	en.Meta.Encode(e, r)
	e.U32(uint32(len(en.PendingMap))) //nolint:gosec // a multimap count is a u32
	for _, p := range sortPending(en.PendingMap) {
		e.String(p.Tag)
		p.Info.Encode(e, r)
	}
	e.String(en.Locator)
	en.Ver.Encode(e, r)
	encodePacked(e, en.IndexVer)
	e.String(en.Tag)
	e.String(en.Key.Instance)
	e.U16(en.Flags)
	e.U64(en.VersionedEpoch)
	e.EndStruct(f)
}

// DecodeDirEntry mirrors rgw_bucket_dir_entry::decode,
// DECODE_START_LEGACY_COMPAT_LEN(8, 3, 3). Below version 4 the pool is -1
// and the epoch is the leading one.
func DecodeDirEntry(d *denc.Decoder) DirEntry {
	h := d.BeginStructLegacy(8, 3, 3, 0)
	var en DirEntry
	en.Key.Name = d.String()
	en.Ver.Epoch = d.U64()
	en.Exists = d.Bool()
	en.Meta = DecodeDirEntryMeta(d)
	en.PendingMap = decodePending(d)
	if h.Version >= 2 {
		en.Locator = d.String()
	}
	if h.Version >= 4 {
		en.Ver = DecodeEntryVer(d)
	} else {
		en.Ver.Pool = -1
	}
	if h.Version >= 5 {
		en.IndexVer = decodePacked(d)
		en.Tag = d.String()
	}
	if h.Version >= 6 {
		en.Key.Instance = d.String()
	}
	if h.Version >= 7 {
		en.Flags = d.U16()
	}
	if h.Version >= 8 {
		en.VersionedEpoch = d.U64()
	}
	d.EndStruct(h)
	return en
}

// decodePending reads a pending_map, keeping every entry as std::multimap
// does.
func decodePending(d *denc.Decoder) []PendingEntry {
	ps := denc.DecodeSlice(d, func(d *denc.Decoder) PendingEntry {
		return PendingEntry{Tag: d.String(), Info: DecodePendingInfo(d)}
	})
	if ps == nil {
		return nil
	}
	return sortPending(ps)
}

// MarshalJSON writes rgw_bucket_dir_entry::dump, which radosgw-admin bi list
// prints.
func (en DirEntry) MarshalJSON() ([]byte, error) {
	pending := sortPending(en.PendingMap)
	if pending == nil {
		pending = []PendingEntry{}
	}
	return json.Marshal(struct {
		Name           string         `json:"name"`
		Instance       string         `json:"instance"`
		Ver            EntryVer       `json:"ver"`
		Locator        string         `json:"locator"`
		Exists         bool           `json:"exists"`
		Meta           DirEntryMeta   `json:"meta"`
		Tag            string         `json:"tag"`
		Flags          int            `json:"flags"`
		PendingMap     []PendingEntry `json:"pending_map"`
		VersionedEpoch uint64         `json:"versioned_epoch"`
	}{
		en.Key.Name, en.Key.Instance, en.Ver, en.Locator, en.Exists, en.Meta, en.Tag,
		int(en.Flags), pending, en.VersionedEpoch,
	})
}

// CategoryStats is rgw_bucket_category_stats, one category's totals in a
// shard header.
type CategoryStats struct {
	TotalSize        uint64 `json:"total_size"`
	TotalSizeRounded uint64 `json:"total_size_rounded"`
	NumEntries       uint64 `json:"num_entries"`
	ActualSize       uint64 `json:"actual_size"`
}

// Encode mirrors rgw_bucket_category_stats::encode, ENCODE_START(3, 2).
func (s CategoryStats) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(3, 2)
	e.U64(s.TotalSize)
	e.U64(s.TotalSizeRounded)
	e.U64(s.NumEntries)
	e.U64(s.ActualSize)
	e.EndStruct(f)
}

// DecodeCategoryStats mirrors rgw_bucket_category_stats::decode,
// DECODE_START_LEGACY_COMPAT_LEN(3, 2, 2). Below version 3 the actual size
// is the total size.
func DecodeCategoryStats(d *denc.Decoder) CategoryStats {
	h := d.BeginStructLegacy(3, 2, 2, 0)
	var s CategoryStats
	s.TotalSize = d.U64()
	s.TotalSizeRounded = d.U64()
	s.NumEntries = d.U64()
	if h.Version >= 3 {
		s.ActualSize = d.U64()
	} else {
		s.ActualSize = s.TotalSize
	}
	d.EndStruct(h)
	return s
}

// InstanceEntry is cls_rgw_bucket_instance_entry, a shard's reshard status.
// Version 1 also carried the new bucket instance id and shard count; version
// 2 dropped them and version 3 writes them back as "" and -1, which every
// release discards on decode, so they are not kept here.
type InstanceEntry struct {
	ReshardStatus uint8
}

// Encode mirrors cls_rgw_bucket_instance_entry::encode, ENCODE_START(3, 1).
func (i InstanceEntry) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(3, 1)
	e.U8(i.ReshardStatus)
	e.String("")
	e.I32(-1)
	e.EndStruct(f)
}

// DecodeInstanceEntry mirrors cls_rgw_bucket_instance_entry::decode,
// DECODE_START(3). Every version but 2 carries the two discarded fields.
func DecodeInstanceEntry(d *denc.Decoder) InstanceEntry {
	h := d.BeginStruct(3)
	var i InstanceEntry
	i.ReshardStatus = d.U8()
	if h.Version != 2 {
		_ = d.String()
		_ = d.I32()
	}
	d.EndStruct(h)
	return i
}

// reshardStatusName is to_string(cls_rgw_reshard_status).
func reshardStatusName(s uint8) string {
	switch s {
	case ReshardNone:
		return "not-resharding"
	case ReshardInLogRecord:
		return "in-logrecord"
	case ReshardInProgress:
		return "in-progress"
	case ReshardDone:
		return "done"
	}
	return "Unknown reshard status"
}

// MarshalJSON writes cls_rgw_bucket_instance_entry::dump.
func (i InstanceEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ReshardStatus string `json:"reshard_status"`
	}{reshardStatusName(i.ReshardStatus)})
}

// DirHeader is rgw_bucket_dir_header, a bucket index shard's header.
// MaxMarker is the C++ max_marker. ReshardLogEntries is Tentacle's version 8
// field: it is written only when encoding for Tentacle.
type DirHeader struct {
	Ver               uint64
	MasterVer         uint64
	Stats             map[uint8]CategoryStats
	MaxMarker         string
	TagTimeout        uint64
	NewInstance       InstanceEntry
	SyncStopped       bool
	ReshardLogEntries uint32
}

// Encode mirrors rgw_bucket_dir_header::encode: ENCODE_START(7, 2) for
// Squid, ENCODE_START(8, 2) with the reshard log count for Tentacle.
func (h DirHeader) Encode(e *denc.Encoder, r denc.Release) {
	v := uint8(7)
	if r >= denc.Tentacle {
		v = 8
	}
	f := e.BeginStruct(v, 2)
	encodeStats(e, h.Stats, r)
	e.U64(h.TagTimeout)
	e.U64(h.Ver)
	e.U64(h.MasterVer)
	e.String(h.MaxMarker)
	h.NewInstance.Encode(e, r)
	e.Bool(h.SyncStopped)
	if v >= 8 {
		e.U32(h.ReshardLogEntries)
	}
	e.EndStruct(f)
}

// DecodeDirHeader mirrors rgw_bucket_dir_header::decode,
// DECODE_START_LEGACY_COMPAT_LEN(8, 2, 2) as Tentacle has it.
func DecodeDirHeader(d *denc.Decoder) DirHeader {
	hd := d.BeginStructLegacy(8, 2, 2, 0)
	var h DirHeader
	h.Stats = decodeStats(d)
	if hd.Version > 2 {
		h.TagTimeout = d.U64()
	}
	if hd.Version >= 4 {
		h.Ver = d.U64()
		h.MasterVer = d.U64()
	}
	if hd.Version >= 5 {
		h.MaxMarker = d.String()
	}
	if hd.Version >= 6 {
		h.NewInstance = DecodeInstanceEntry(d)
	}
	if hd.Version >= 7 {
		h.SyncStopped = d.Bool()
	}
	if hd.Version >= 8 {
		h.ReshardLogEntries = d.U32()
	}
	d.EndStruct(hd)
	return h
}

// MarshalJSON writes rgw_bucket_dir_header::dump as Squid has it, whatever
// the release: the versions as signed integers, and the stats as an array
// alternating the category and its totals. Tentacle's dump adds
// reshardlog_entries, which this form omits.
func (h DirHeader) MarshalJSON() ([]byte, error) {
	stats := make([]any, 0, 2*len(h.Stats))
	for _, c := range slices.Sorted(maps.Keys(h.Stats)) {
		stats = append(stats, int(c), h.Stats[c])
	}
	return json.Marshal(struct {
		Ver         int64         `json:"ver"`
		MasterVer   int64         `json:"master_ver"`
		Stats       []any         `json:"stats"`
		NewInstance InstanceEntry `json:"new_instance"`
	}{int64(h.Ver), int64(h.MasterVer), stats, h.NewInstance}) //nolint:gosec // dump_int of the u64, as C++ does
}

func encodeStats(e *denc.Encoder, m map[uint8]CategoryStats, r denc.Release) {
	denc.EncodeMap(e, m, (*denc.Encoder).U8, func(e *denc.Encoder, s CategoryStats) { s.Encode(e, r) })
}

// decodeStats reads the stats map. Its value has no denc traits, so C++
// decodes it through operator[] and a repeated category keeps the last value.
func decodeStats(d *denc.Decoder) map[uint8]CategoryStats {
	return decodeMapLast(d, (*denc.Decoder).U8, DecodeCategoryStats)
}

// Dir is rgw_bucket_dir: a shard header and entries keyed by index key.
type Dir struct {
	Header  DirHeader
	Entries map[string]DirEntry
}

// Encode mirrors rgw_bucket_dir::encode, ENCODE_START(2, 2).
func (dir Dir) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 2)
	dir.Header.Encode(e, r)
	denc.EncodeMap(e, dir.Entries, (*denc.Encoder).String, func(e *denc.Encoder, en DirEntry) { en.Encode(e, r) })
	e.EndStruct(f)
}

// DecodeDir mirrors rgw_bucket_dir::decode,
// DECODE_START_LEGACY_COMPAT_LEN(2, 2, 2). The entries are a flat_map
// decoded through operator[], so a repeated key keeps the last entry.
func DecodeDir(d *denc.Decoder) Dir {
	h := d.BeginStructLegacy(2, 2, 2, 0)
	var dir Dir
	dir.Header = DecodeDirHeader(d)
	dir.Entries = decodeMapLast(d, (*denc.Decoder).String, DecodeDirEntry)
	d.EndStruct(h)
	return dir
}

// decodeMapLast reads a u32 count then that many keys and values, the last
// value for a repeated key winning. It stands for C++'s legacy decode of a
// std::map or boost::container::flat_map whose key or value has no denc
// traits, which assigns through operator[]; ceph-dencoder v19.2.6 and
// v20.2.4 both show the last value for a repeated header category, dir entry
// and usage category. A map whose key and value both have denc traits keeps
// the first value instead, as denc.DecodeMap does. (C++ decodes a repeat into
// the earlier value in place, so a field an old struct version leaves out
// would keep the earlier value; no corpus or radosgw encoding repeats a key.)
// An empty or failed decode returns nil.
func decodeMapLast[K comparable, V any](d *denc.Decoder, decK func(*denc.Decoder) K, decV func(*denc.Decoder) V) map[K]V {
	type kv struct {
		k K
		v V
	}
	kvs := denc.DecodeSlice(d, func(d *denc.Decoder) kv { return kv{decK(d), decV(d)} })
	if kvs == nil {
		return nil
	}
	m := make(map[K]V, len(kvs))
	for _, p := range kvs {
		m[p.k] = p.v
	}
	return m
}

// zoneLess is rgw_zone_set_entry::operator< on the string forms: by zone,
// then an absent location key before any present one, then by location key.
func zoneLess(a, b string) int {
	az, al, ah := strings.Cut(a, ":")
	bz, bl, bh := strings.Cut(b, ":")
	if c := strings.Compare(az, bz); c != 0 {
		return c
	}
	if ah != bh {
		if !ah {
			return -1
		}
		return 1
	}
	return strings.Compare(al, bl)
}

// normalizeZones orders and deduplicates zone set entries as
// std::set<rgw_zone_set_entry> holds them.
func normalizeZones(zs []string) []string {
	if len(zs) == 0 {
		return nil
	}
	zs = slices.Clone(zs)
	slices.SortFunc(zs, zoneLess)
	return slices.Compact(zs)
}

// encodeZoneSet writes rgw_zone_set, a std::set of rgw_zone_set_entry each
// written as its "zone[:location_key]" string, with no struct header. The
// strings round-trip through from_str and to_str unchanged.
func encodeZoneSet(e *denc.Encoder, zs []string) {
	denc.EncodeSlice(e, normalizeZones(zs), (*denc.Encoder).String)
}

// decodeZoneSet reads rgw_zone_set.
func decodeZoneSet(d *denc.Decoder) []string {
	return normalizeZones(denc.DecodeSlice(d, (*denc.Decoder).String))
}
