package rgw

import (
	"slices"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// UsageAddOp is rgw_cls_usage_log_add_op, the input of user_usage_log_add.
// User is an rgw_user in its string form; radosgw leaves it empty.
type UsageAddOp struct {
	Info UsageLogInfo
	User string
}

// Encode mirrors rgw_cls_usage_log_add_op::encode, ENCODE_START(2, 1).
func (o UsageAddOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	o.Info.Encode(e, r)
	e.String(canonicalUser(o.User))
	e.EndStruct(f)
}

// DecodeUsageAddOp mirrors rgw_cls_usage_log_add_op::decode, DECODE_START(2).
func DecodeUsageAddOp(d *denc.Decoder) UsageAddOp {
	h := d.BeginStruct(2)
	var o UsageAddOp
	o.Info = DecodeUsageLogInfo(d)
	if h.Version >= 2 {
		o.User = canonicalUser(d.String())
	}
	d.EndStruct(h)
	return o
}

// UsageLogAdd adds user_usage_log_add, which merges info's entries into the
// usage log object, as cls_rgw_usage_log_add sends it: with no user.
func UsageLogAdd(op radosclient.Execer, info UsageLogInfo, r denc.Release) {
	op.Exec(Class, methodUserUsageLogAdd, clsutil.Encode(UsageAddOp{Info: info}, r))
}

// UsageReadOp is rgw_cls_usage_log_read_op, the input of user_usage_log_read.
// Iter is empty on the first call and the previous NextIter after.
type UsageReadOp struct {
	StartEpoch uint64
	EndEpoch   uint64
	Owner      string
	Bucket     string
	Iter       string
	MaxEntries uint32
}

// Encode mirrors rgw_cls_usage_log_read_op::encode, ENCODE_START(2, 1).
func (o UsageReadOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.U64(o.StartEpoch)
	e.U64(o.EndEpoch)
	e.String(o.Owner)
	e.String(o.Iter)
	e.U32(o.MaxEntries)
	e.String(o.Bucket)
	e.EndStruct(f)
}

// DecodeUsageReadOp mirrors rgw_cls_usage_log_read_op::decode, DECODE_START(2).
func DecodeUsageReadOp(d *denc.Decoder) UsageReadOp {
	h := d.BeginStruct(2)
	var o UsageReadOp
	o.StartEpoch = d.U64()
	o.EndEpoch = d.U64()
	o.Owner = d.String()
	o.Iter = d.String()
	o.MaxEntries = d.U32()
	if h.Version >= 2 {
		o.Bucket = d.String()
	}
	d.EndStruct(h)
	return o
}

// UsageReadRet is rgw_cls_usage_log_read_ret, user_usage_log_read's output.
type UsageReadRet struct {
	Usage     map[UserBucket]UsageLogEntry
	Truncated bool
	NextIter  string
}

// Encode mirrors rgw_cls_usage_log_read_ret::encode, ENCODE_START(1, 1). The
// map is written in rgw_user_bucket order, by user then bucket.
func (o UsageReadRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	keys := make([]UserBucket, 0, len(o.Usage))
	for k := range o.Usage {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareUserBucket)
	denc.EncodeSlice(e, keys, func(e *denc.Encoder, k UserBucket) {
		k.Encode(e, r)
		o.Usage[k].Encode(e, r)
	})
	e.Bool(o.Truncated)
	e.String(o.NextIter)
	e.EndStruct(f)
}

// DecodeUsageReadRet mirrors rgw_cls_usage_log_read_ret::decode,
// DECODE_START(1). Neither the key nor the value has denc traits, so a
// repeated key keeps the last entry, as C++'s operator[] decode does.
func DecodeUsageReadRet(d *denc.Decoder) UsageReadRet {
	h := d.BeginStruct(1)
	var o UsageReadRet
	o.Usage = denc.DecodeMapLast(d, DecodeUserBucket, DecodeUsageLogEntry)
	o.Truncated = d.Bool()
	o.NextIter = d.String()
	d.EndStruct(h)
	return o
}

// UsageReadResult is the pending output of a user_usage_log_read call.
type UsageReadResult struct {
	res *radosclient.ExecResult
}

// Result decodes the usage read once the op has run.
func (res *UsageReadResult) Result() (UsageReadRet, error) {
	return clsutil.DecodeReply(res.res, Class, methodUserUsageLogRead, DecodeUsageReadRet)
}

// UsageLogRead adds user_usage_log_read.
func UsageLogRead(op *radosclient.ReadOp, q UsageReadOp, r denc.Release) *UsageReadResult {
	return &UsageReadResult{res: op.Exec(Class, methodUserUsageLogRead, clsutil.Encode(q, r))}
}

// UsageTrimOp is rgw_cls_usage_log_trim_op, the input of user_usage_log_trim.
type UsageTrimOp struct {
	StartEpoch uint64
	EndEpoch   uint64
	User       string
	Bucket     string
}

// Encode mirrors rgw_cls_usage_log_trim_op::encode, ENCODE_START(3, 2).
func (o UsageTrimOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(3, 2)
	e.U64(o.StartEpoch)
	e.U64(o.EndEpoch)
	e.String(o.User)
	e.String(o.Bucket)
	e.EndStruct(f)
}

// DecodeUsageTrimOp mirrors rgw_cls_usage_log_trim_op::decode, DECODE_START(3).
func DecodeUsageTrimOp(d *denc.Decoder) UsageTrimOp {
	h := d.BeginStruct(3)
	var o UsageTrimOp
	o.StartEpoch = d.U64()
	o.EndEpoch = d.U64()
	o.User = d.String()
	if h.Version >= 3 {
		o.Bucket = d.String()
	}
	d.EndStruct(h)
	return o
}

// UsageLogTrim adds user_usage_log_trim, which removes a bounded batch of
// matching entries; it fails with ENODATA once none remain, and radosgw
// repeats it until then.
func UsageLogTrim(op radosclient.Execer, t UsageTrimOp, r denc.Release) {
	op.Exec(Class, methodUserUsageLogTrim, clsutil.Encode(t, r))
}

// UsageLogClear adds usage_log_clear, which removes every usage entry in the
// object. Its input is empty.
func UsageLogClear(op radosclient.Execer) {
	op.Exec(Class, methodUsageLogClear, nil)
}
