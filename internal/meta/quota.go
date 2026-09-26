package meta

import (
	"encoding/json"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Quota is RGWQuotaInfo. A negative limit means unlimited; RGW's own default
// for both limits is -1.
type Quota struct {
	MaxSize    int64 `json:"max_size"`
	MaxObjects int64 `json:"max_objects"`
	Enabled    bool  `json:"enabled"`
	CheckOnRaw bool  `json:"check_on_raw"`
}

// roundedKB is rgw_rounded_kb: bytes rounded up to whole KiB, with C++'s
// truncating division.
func roundedKB(bytes int64) int64 { return (bytes + 1023) / 1024 }

// maxSizeKB is the legacy max_size_kb field RGWQuotaInfo::encode writes: the
// size in KiB rounded away from zero.
func (q Quota) maxSizeKB() int64 {
	if q.MaxSize < 0 {
		return -roundedKB(-q.MaxSize)
	}
	return roundedKB(q.MaxSize)
}

// MarshalJSON adds max_size_kb, as RGWQuotaInfo::dump writes it: note that it
// rounds the signed size, unlike the encoded field.
func (q Quota) MarshalJSON() ([]byte, error) {
	type plain Quota
	return json.Marshal(struct {
		plain
		MaxSizeKB int64 `json:"max_size_kb"`
	}{plain(q), roundedKB(q.MaxSize)})
}

// Encode mirrors RGWQuotaInfo::encode, ENCODE_START(3, 1).
func (q Quota) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(3, 1)
	e.I64(q.maxSizeKB())
	e.I64(q.MaxObjects)
	e.Bool(q.Enabled)
	e.I64(q.MaxSize)
	e.Bool(q.CheckOnRaw)
	e.EndStruct(f)
}

// DecodeQuota mirrors RGWQuotaInfo::decode, DECODE_START_LEGACY_COMPAT_LEN(3, 1, 1).
// Version 1 stored the size only in KiB.
func DecodeQuota(d *denc.Decoder) Quota {
	h := d.BeginStructLegacy(3, 1, 1, 0)
	var q Quota
	maxSizeKB := d.I64()
	q.MaxObjects = d.I64()
	q.Enabled = d.Bool()
	if h.Version < 2 {
		q.MaxSize = maxSizeKB * 1024
	} else {
		q.MaxSize = d.I64()
	}
	if h.Version >= 3 {
		q.CheckOnRaw = d.Bool()
	}
	d.EndStruct(h)
	return q
}
