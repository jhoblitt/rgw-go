package rgw

import (
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// UsageData is rgw_usage_data, one category's usage counters.
type UsageData struct {
	BytesSent, BytesReceived, Ops, SuccessfulOps uint64
}

// Encode mirrors rgw_usage_data::encode, ENCODE_START(1, 1).
func (u UsageData) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	u.encodeFields(e)
	e.EndStruct(f)
}

func (u UsageData) encodeFields(e *denc.Encoder) {
	e.U64(u.BytesSent)
	e.U64(u.BytesReceived)
	e.U64(u.Ops)
	e.U64(u.SuccessfulOps)
}

// DecodeUsageData mirrors rgw_usage_data::decode, DECODE_START(1).
func DecodeUsageData(d *denc.Decoder) UsageData {
	h := d.BeginStruct(1)
	u := decodeUsageFields(d)
	d.EndStruct(h)
	return u
}

func decodeUsageFields(d *denc.Decoder) UsageData {
	var u UsageData
	u.BytesSent = d.U64()
	u.BytesReceived = d.U64()
	u.Ops = d.U64()
	u.SuccessfulOps = d.U64()
	return u
}

// S3SelectUsage is rgw_s3select_usage_data.
type S3SelectUsage struct {
	BytesProcessed, BytesReturned uint64
}

// Encode mirrors rgw_s3select_usage_data::encode, ENCODE_START(1, 1).
func (s S3SelectUsage) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(s.BytesProcessed)
	e.U64(s.BytesReturned)
	e.EndStruct(f)
}

// DecodeS3SelectUsage mirrors rgw_s3select_usage_data::decode, DECODE_START(1).
func DecodeS3SelectUsage(d *denc.Decoder) S3SelectUsage {
	h := d.BeginStruct(1)
	var s S3SelectUsage
	s.BytesProcessed = d.U64()
	s.BytesReturned = d.U64()
	d.EndStruct(h)
	return s
}

// canonicalUser is rgw_user::to_str of rgw_user::from_str: the form an
// rgw_user written as a string takes once C++ has parsed it back. The string
// is "[tenant$][ns$]id"; a leading "$" or an empty namespace is dropped.
func canonicalUser(s string) string {
	tenant, rest, ok := strings.Cut(s, "$")
	if !ok {
		return s
	}
	ns, id, hasNS := strings.Cut(rest, "$")
	if !hasNS {
		ns, id = "", rest
	}
	switch {
	case tenant != "" && ns != "":
		return tenant + "$" + ns + "$" + id
	case tenant != "":
		return tenant + "$" + id
	case ns != "":
		return "$" + ns + "$" + id
	default:
		return id
	}
}

// UsageLogEntry is rgw_usage_log_entry, one user's usage of one bucket in
// one hour. Owner and Payer are rgw_user in their string form; encode and
// decode both canonicalize them, as C++ holds them parsed. TotalUsage is kept for
// compatibility; UsageMap holds the per-category counters.
type UsageLogEntry struct {
	Owner         string
	Payer         string
	Bucket        string
	Epoch         uint64
	TotalUsage    UsageData
	UsageMap      map[string]UsageData
	S3SelectUsage S3SelectUsage
}

// Encode mirrors rgw_usage_log_entry::encode, ENCODE_START(4, 1): the total
// counters are written inline, not as an rgw_usage_data.
func (u UsageLogEntry) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(4, 1)
	e.String(canonicalUser(u.Owner))
	e.String(u.Bucket)
	e.U64(u.Epoch)
	u.TotalUsage.encodeFields(e)
	denc.EncodeMap(e, u.UsageMap, (*denc.Encoder).String, func(e *denc.Encoder, v UsageData) { v.Encode(e, r) })
	e.String(canonicalUser(u.Payer))
	u.S3SelectUsage.Encode(e, r)
	e.EndStruct(f)
}

// DecodeUsageLogEntry mirrors rgw_usage_log_entry::decode, DECODE_START(4).
// Below version 2 the total is the only category, named "".
func DecodeUsageLogEntry(d *denc.Decoder) UsageLogEntry {
	h := d.BeginStruct(4)
	var u UsageLogEntry
	u.Owner = canonicalUser(d.String())
	u.Bucket = d.String()
	u.Epoch = d.U64()
	u.TotalUsage = decodeUsageFields(d)
	if h.Version < 2 {
		u.UsageMap = map[string]UsageData{"": u.TotalUsage}
	} else {
		u.UsageMap = decodeMapLast(d, (*denc.Decoder).String, DecodeUsageData)
	}
	if h.Version >= 3 {
		u.Payer = canonicalUser(d.String())
	}
	if h.Version >= 4 {
		u.S3SelectUsage = DecodeS3SelectUsage(d)
	}
	d.EndStruct(h)
	return u
}

// UsageLogInfo is rgw_usage_log_info, a batch of entries to log.
type UsageLogInfo struct {
	Entries []UsageLogEntry
}

// Encode mirrors rgw_usage_log_info::encode, ENCODE_START(1, 1).
func (u UsageLogInfo) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, u.Entries, func(e *denc.Encoder, v UsageLogEntry) { v.Encode(e, r) })
	e.EndStruct(f)
}

// DecodeUsageLogInfo mirrors rgw_usage_log_info::decode, DECODE_START(1).
func DecodeUsageLogInfo(d *denc.Decoder) UsageLogInfo {
	h := d.BeginStruct(1)
	var u UsageLogInfo
	u.Entries = denc.DecodeSlice(d, DecodeUsageLogEntry)
	d.EndStruct(h)
	return u
}

// UserBucket is rgw_user_bucket, the key of a usage read's results.
type UserBucket struct {
	User, Bucket string
}

// Encode mirrors rgw_user_bucket::encode, ENCODE_START(1, 1).
func (u UserBucket) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(u.User)
	e.String(u.Bucket)
	e.EndStruct(f)
}

// DecodeUserBucket mirrors rgw_user_bucket::decode, DECODE_START(1).
func DecodeUserBucket(d *denc.Decoder) UserBucket {
	h := d.BeginStruct(1)
	var u UserBucket
	u.User = d.String()
	u.Bucket = d.String()
	d.EndStruct(h)
	return u
}

// compareUserBucket is rgw_user_bucket::operator<: by user, then bucket.
func compareUserBucket(a, b UserBucket) int {
	if c := strings.Compare(a.User, b.User); c != 0 {
		return c
	}
	return strings.Compare(a.Bucket, b.Bucket)
}
