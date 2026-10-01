package meta

import (
	"encoding/json"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The layout types mirror src/rgw/rgw_bucket_layout.{h,cc}. Squid and Tentacle
// differ in two encodings: Tentacle added min_num_shards to
// bucket_index_normal_layout (v2) and judge_reshard_lock_time to
// rgw::BucketLayout (v3). Main adds the FIFO log type, which no targeted
// release encodes. Decoders read every version and start from the C++ member
// initializers, which is what a field an older version lacks keeps. JSON
// forms are the encode_json_impl overloads as Tentacle writes them.

// IndexType is rgw::BucketIndexType.
type IndexType uint8

// The rgw::BucketIndexType values.
const (
	IndexNormal    IndexType = 0
	IndexIndexless IndexType = 1
)

// String is to_string(const BucketIndexType&).
func (t IndexType) String() string {
	switch t {
	case IndexNormal:
		return "Normal"
	case IndexIndexless:
		return "Indexless"
	default:
		return "Unknown"
	}
}

// MarshalJSON writes the String form.
func (t IndexType) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }

// HashType is rgw::BucketHashType.
type HashType uint8

// HashMod is BucketHashType::Mod, the only value: IndexShard.
const HashMod HashType = 0

// String is to_string(const BucketHashType&).
func (t HashType) String() string {
	if t == HashMod {
		return "Mod"
	}
	return "Unknown"
}

// MarshalJSON writes the String form.
func (t HashType) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }

// LogType is rgw::BucketLogType.
type LogType uint8

// The rgw::BucketLogType values. Deleted is Tentacle's; FIFO exists only on
// main.
const (
	LogInIndex LogType = 0
	LogDeleted LogType = 1
	LogFIFO    LogType = 2
)

// String is to_string(const BucketLogType&) as Tentacle has it, which does
// not name FIFO.
func (t LogType) String() string {
	switch t {
	case LogInIndex:
		return "InIndex"
	case LogDeleted:
		return "Deleted"
	default:
		return "Unknown"
	}
}

// MarshalJSON writes the String form.
func (t LogType) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }

// ReshardState is rgw::BucketReshardState.
type ReshardState uint8

// The rgw::BucketReshardState values; InLogrecord is Tentacle's.
const (
	ReshardNone        ReshardState = 0
	ReshardInProgress  ReshardState = 1
	ReshardInLogrecord ReshardState = 2
)

// String is to_string(const BucketReshardState&).
func (s ReshardState) String() string {
	switch s {
	case ReshardNone:
		return "None"
	case ReshardInProgress:
		return "InProgress"
	case ReshardInLogrecord:
		return "InLogrecord"
	default:
		return "Unknown"
	}
}

// MarshalJSON writes the String form.
func (s ReshardState) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// IndexNormalLayout is rgw::bucket_index_normal_layout. A NumShards of 0 is
// an old unsharded bucket, whose index is a single object.
type IndexNormalLayout struct {
	NumShards uint32   `json:"num_shards"`
	HashType  HashType `json:"hash_type"`
	// MinNumShards is Tentacle's: Squid neither encodes nor dumps it.
	MinNumShards uint32 `json:"min_num_shards"`
}

// NewIndexNormalLayout returns the member initializers: one shard, Mod, at
// least one shard.
func NewIndexNormalLayout() IndexNormalLayout {
	return IndexNormalLayout{NumShards: 1, HashType: HashMod, MinNumShards: 1}
}

// Encode mirrors encode(const bucket_index_normal_layout&): ENCODE_START(1, 1)
// on Squid, and ENCODE_START(2, 1) with min_num_shards from Tentacle.
func (l IndexNormalLayout) Encode(e *denc.Encoder, r denc.Release) {
	if r == denc.Squid {
		f := e.BeginStruct(1, 1)
		e.U32(l.NumShards)
		e.U8(uint8(l.HashType))
		e.EndStruct(f)
		return
	}
	f := e.BeginStruct(2, 1)
	e.U32(l.NumShards)
	e.U8(uint8(l.HashType))
	e.U32(l.MinNumShards)
	e.EndStruct(f)
}

// DecodeIndexNormalLayout mirrors decode(bucket_index_normal_layout&),
// DECODE_START(2), into NewIndexNormalLayout.
func DecodeIndexNormalLayout(d *denc.Decoder) IndexNormalLayout {
	h := d.BeginStruct(2)
	l := NewIndexNormalLayout()
	l.NumShards = d.U32()
	l.HashType = HashType(d.U8())
	if h.Version >= 2 {
		l.MinNumShards = d.U32()
	}
	d.EndStruct(h)
	return l
}

// IndexLayout is rgw::bucket_index_layout. Normal is encoded only for a
// Normal index but, as in C++, is always present.
type IndexLayout struct {
	Type   IndexType         `json:"type"`
	Normal IndexNormalLayout `json:"normal"`
}

// Encode mirrors encode(const bucket_index_layout&), ENCODE_START(1, 1).
func (l IndexLayout) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U8(uint8(l.Type))
	if l.Type == IndexNormal {
		l.Normal.Encode(e, r)
	}
	e.EndStruct(f)
}

// DecodeIndexLayout mirrors decode(bucket_index_layout&), DECODE_START(1).
// Any type but Normal leaves Normal at its initializers.
func DecodeIndexLayout(d *denc.Decoder) IndexLayout {
	h := d.BeginStruct(1)
	l := IndexLayout{Type: IndexType(d.U8()), Normal: NewIndexNormalLayout()}
	if l.Type == IndexNormal {
		l.Normal = DecodeIndexNormalLayout(d)
	}
	d.EndStruct(h)
	return l
}

// IndexLayoutGen is rgw::bucket_index_layout_generation.
type IndexLayoutGen struct {
	Gen    uint64      `json:"gen"`
	Layout IndexLayout `json:"layout"`
}

// NewIndexLayoutGen returns the member initializers: generation 0 of a
// Normal index with NewIndexNormalLayout.
func NewIndexLayoutGen() IndexLayoutGen {
	return IndexLayoutGen{Layout: IndexLayout{Type: IndexNormal, Normal: NewIndexNormalLayout()}}
}

// Encode mirrors encode(const bucket_index_layout_generation&), ENCODE_START(1, 1).
func (l IndexLayoutGen) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(l.Gen)
	l.Layout.Encode(e, r)
	e.EndStruct(f)
}

// DecodeIndexLayoutGen mirrors decode(bucket_index_layout_generation&), DECODE_START(1).
func DecodeIndexLayoutGen(d *denc.Decoder) IndexLayoutGen {
	h := d.BeginStruct(1)
	var l IndexLayoutGen
	l.Gen = d.U64()
	l.Layout = DecodeIndexLayout(d)
	d.EndStruct(h)
	return l
}

// IndexLogLayout is rgw::bucket_index_log_layout: a log kept in the index
// shards of generation Gen.
type IndexLogLayout struct {
	Gen    uint64            `json:"gen"`
	Layout IndexNormalLayout `json:"layout"`
}

// Encode mirrors encode(const bucket_index_log_layout&), ENCODE_START(1, 1).
func (l IndexLogLayout) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(l.Gen)
	l.Layout.Encode(e, r)
	e.EndStruct(f)
}

// DecodeIndexLogLayout mirrors decode(bucket_index_log_layout&), DECODE_START(1).
func DecodeIndexLogLayout(d *denc.Decoder) IndexLogLayout {
	h := d.BeginStruct(1)
	var l IndexLogLayout
	l.Gen = d.U64()
	l.Layout = DecodeIndexNormalLayout(d)
	d.EndStruct(h)
	return l
}

// FIFOLogLayout is main's rgw::bucket_fifo_log_layout, decoded so that a
// layout main wrote reads, but encoded by no targeted release.
type FIFOLogLayout struct {
	NumShards uint32   `json:"num_shards"`
	HashType  HashType `json:"hash_type"`
}

// defaultFIFOShards is bucket_fifo_log_layout's num_shards initializer.
const defaultFIFOShards = 7

// DecodeFIFOLogLayout mirrors main's decode(bucket_fifo_log_layout&), DECODE_START(1).
func DecodeFIFOLogLayout(d *denc.Decoder) FIFOLogLayout {
	h := d.BeginStruct(1)
	var l FIFOLogLayout
	l.NumShards = d.U32()
	l.HashType = HashType(d.U8())
	d.EndStruct(h)
	return l
}

// LogLayout is rgw::bucket_log_layout. InIndex and FIFO are both always
// present, as in C++; Type says which one the log uses.
type LogLayout struct {
	Type    LogType
	InIndex IndexLogLayout
	FIFO    FIFOLogLayout
}

// newLogLayout returns the member initializers.
func newLogLayout() LogLayout {
	return LogLayout{
		Type:    LogInIndex,
		InIndex: IndexLogLayout{Layout: NewIndexNormalLayout()},
		FIFO:    FIFOLogLayout{NumShards: defaultFIFOShards},
	}
}

// Encode mirrors encode(const bucket_log_layout&), ENCODE_START(1, 1), as
// Squid and Tentacle have it: only InIndex carries a payload, so a FIFO log,
// which neither release knows, is written as its type alone, as they would
// re-encode one.
func (l LogLayout) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U8(uint8(l.Type))
	if l.Type == LogInIndex {
		l.InIndex.Encode(e, r)
	}
	e.EndStruct(f)
}

// DecodeLogLayout mirrors main's decode(bucket_log_layout&), DECODE_START(1),
// which reads the FIFO payload too.
func DecodeLogLayout(d *denc.Decoder) LogLayout {
	h := d.BeginStruct(1)
	l := newLogLayout()
	l.Type = LogType(d.U8())
	switch l.Type {
	case LogInIndex:
		l.InIndex = DecodeIndexLogLayout(d)
	case LogFIFO:
		l.FIFO = DecodeFIFOLogLayout(d)
	}
	d.EndStruct(h)
	return l
}

// MarshalJSON is encode_json_impl(const char*, const bucket_log_layout&) as
// Tentacle has it: in_index only for an InIndex log.
func (l LogLayout) MarshalJSON() ([]byte, error) {
	type inIndex struct {
		Type    LogType        `json:"type"`
		InIndex IndexLogLayout `json:"in_index"`
	}
	if l.Type == LogInIndex {
		return json.Marshal(inIndex{l.Type, l.InIndex})
	}
	return json.Marshal(struct {
		Type LogType `json:"type"`
	}{l.Type})
}

// LogLayoutGen is rgw::bucket_log_layout_generation.
type LogLayoutGen struct {
	Gen    uint64    `json:"gen"`
	Layout LogLayout `json:"layout"`
}

// LogLayoutFromIndex is rgw::log_layout_from_index: generation gen of a log
// kept in the shards of index.
func LogLayoutFromIndex(gen uint64, index IndexLayoutGen) LogLayoutGen {
	l := newLogLayout()
	l.InIndex = IndexLogLayout{Gen: index.Gen, Layout: index.Layout.Normal}
	return LogLayoutGen{Gen: gen, Layout: l}
}

// Encode mirrors encode(const bucket_log_layout_generation&), ENCODE_START(1, 1).
func (l LogLayoutGen) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(l.Gen)
	l.Layout.Encode(e, r)
	e.EndStruct(f)
}

// DecodeLogLayoutGen mirrors decode(bucket_log_layout_generation&), DECODE_START(1).
func DecodeLogLayoutGen(d *denc.Decoder) LogLayoutGen {
	h := d.BeginStruct(1)
	var l LogLayoutGen
	l.Gen = d.U64()
	l.Layout = DecodeLogLayout(d)
	d.EndStruct(h)
	return l
}

// BucketLayout is rgw::BucketLayout, the layout of a bucket's index objects.
type BucketLayout struct {
	Resharding ReshardState   `json:"resharding"`
	Current    IndexLayoutGen `json:"current_index"`
	// Target is the index a reshard is building.
	Target *IndexLayoutGen `json:"target_index,omitempty"`
	// Logs holds the untrimmed log generations, the current one last.
	Logs []LogLayoutGen `json:"logs"`
	// JudgeReshardLockTime is Tentacle's: Squid neither encodes nor dumps it.
	JudgeReshardLockTime Time `json:"judge_reshard_lock_time"`
}

// NewBucketLayout returns the member initializers: not resharding, with
// NewIndexLayoutGen current and no logs.
func NewBucketLayout() BucketLayout {
	return BucketLayout{Current: NewIndexLayoutGen()}
}

// Encode mirrors encode(const BucketLayout&): ENCODE_START(2, 1) on Squid,
// and ENCODE_START(3, 1) with judge_reshard_lock_time from Tentacle.
func (l BucketLayout) Encode(e *denc.Encoder, r denc.Release) {
	v := uint8(3)
	if r == denc.Squid {
		v = 2
	}
	f := e.BeginStruct(v, 1)
	e.U8(uint8(l.Resharding))
	l.Current.Encode(e, r)
	denc.EncodeOptional(e, l.Target, func(e *denc.Encoder, t IndexLayoutGen) { t.Encode(e, r) })
	denc.EncodeSlice(e, l.Logs, func(e *denc.Encoder, g LogLayoutGen) { g.Encode(e, r) })
	if r != denc.Squid {
		l.JudgeReshardLockTime.Encode(e)
	}
	e.EndStruct(f)
}

// DecodeBucketLayout mirrors decode(BucketLayout&), DECODE_START(3), into
// NewBucketLayout. Version 1 had no logs: a Normal index gets one derived
// from it.
func DecodeBucketLayout(d *denc.Decoder) BucketLayout {
	h := d.BeginStruct(3)
	l := NewBucketLayout()
	l.Resharding = ReshardState(d.U8())
	l.Current = DecodeIndexLayoutGen(d)
	l.Target = denc.DecodeOptional(d, DecodeIndexLayoutGen)
	if h.Version < 2 {
		if l.Current.Layout.Type == IndexNormal {
			l.Logs = []LogLayoutGen{LogLayoutFromIndex(0, l.Current)}
		}
	} else {
		l.Logs = denc.DecodeSlice(d, DecodeLogLayoutGen)
	}
	if h.Version >= 3 {
		l.JudgeReshardLockTime = DecodeTime(d)
	}
	d.EndStruct(h)
	return l
}

// MarshalJSON is encode_json_impl(const char*, const BucketLayout&): logs is
// always a list.
func (l BucketLayout) MarshalJSON() ([]byte, error) {
	type plain BucketLayout
	p := plain(l)
	if p.Logs == nil {
		p.Logs = []LogLayoutGen{}
	}
	return json.Marshal(p)
}

// The rgw_shards_mod primes, RGW_SHARDS_PRIME_0 and RGW_SHARDS_PRIME_1.
const (
	shardsPrime0 = 7877
	shardsPrime1 = 65521
)

// StrHashLinux is ceph_str_hash_linux.
func StrHashLinux(s string) uint32 {
	var h uint32
	for _, c := range []byte(s) {
		h = (h + uint32(c)<<4 + uint32(c)>>4) * 11
	}
	return h
}

// IndexShard is RGWSI_BucketIndex_RADOS::bucket_shard_index: the shard of
// numShards that holds key, which is the object name, or for a multipart
// upload's meta object its upload key. The name is hashed with
// ceph_str_hash_linux, folded by h ^= (h & 0xff) << 24, and reduced by
// rgw_shards_mod: (h % 7877) % numShards, or 65521 in place of 7877 above
// 7877 shards. An unsharded index, numShards 0, has no shard: ok is false,
// where C++ returns RGW_NO_SHARD.
func IndexShard(key string, numShards uint32) (shard uint32, ok bool) {
	if numShards == 0 {
		return 0, false
	}
	h := StrHashLinux(key)
	h ^= (h & 0xff) << 24
	if numShards <= shardsPrime0 {
		return h % shardsPrime0 % numShards, true
	}
	return h % shardsPrime1 % numShards, true
}
