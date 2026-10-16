package rgw

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The OLH method names, RGW_BUCKET_LINK_OLH through RGW_BUCKET_CLEAR_OLH
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_const.h#L24-L28 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_const.h#L24-L28 at v20.2.4).
// Both releases register bucket_read_olh_log RD and the others RD|WR
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4699-L4703 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5116-L5120 at v20.2.4).
const (
	methodBucketLinkOLH        = "bucket_link_olh"
	methodBucketUnlinkInstance = "bucket_unlink_instance"
	methodBucketReadOLHLog     = "bucket_read_olh_log"
	methodBucketTrimOLHLog     = "bucket_trim_olh_log"
	methodBucketClearOLH       = "bucket_clear_olh"
)

// LinkOLHOp is rgw_cls_link_olh_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L167-L232 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L165-L230 at v20.2.4),
// the input of bucket_link_olh. OLHTag is the OLH object's user.rgw.olh.idtag
// xattr, its bytes whole, as cls_rgw_bucket_link_olh copies them. radosgw
// never sets BILogFlags, so it sends 0. OLHEpoch 0 lets the class take the
// OLH's next epoch.
type LinkOLHOp struct {
	Key               ObjKey
	OLHTag            string
	DeleteMarker      bool
	OpTag             string
	Meta              DirEntryMeta
	OLHEpoch          uint64
	LogOp             bool
	BILogFlags        uint16
	UnmodSince        time.Time
	HighPrecisionTime bool
	ZonesTrace        []string
}

// Encode mirrors rgw_cls_link_olh_op::encode, ENCODE_START(5, 1). The
// unmodified-since time goes twice: as to_time_t's whole seconds in a u64,
// then as a real_time.
func (l LinkOLHOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(5, 1)
	l.Key.Encode(e, r)
	e.String(l.OLHTag)
	e.Bool(l.DeleteMarker)
	e.String(l.OpTag)
	l.Meta.Encode(e, r)
	e.U64(l.OLHEpoch)
	e.Bool(l.LogOp)
	e.U16(l.BILogFlags)
	e.U64(timeT(l.UnmodSince))
	e.Time(l.UnmodSince)
	e.Bool(l.HighPrecisionTime)
	encodeZoneSet(e, l.ZonesTrace)
	e.EndStruct(f)
}

// DecodeLinkOLHOp mirrors rgw_cls_link_olh_op::decode, DECODE_START(5).
// Version 2 carries the time as whole seconds alone; from version 3 the
// real_time follows them and is the one kept.
func DecodeLinkOLHOp(d *denc.Decoder) LinkOLHOp {
	h := d.BeginStruct(5)
	var l LinkOLHOp
	l.Key = DecodeObjKey(d)
	l.OLHTag = d.String()
	l.DeleteMarker = d.Bool()
	l.OpTag = d.String()
	l.Meta = DecodeDirEntryMeta(d)
	l.OLHEpoch = d.U64()
	l.LogOp = d.Bool()
	l.BILogFlags = d.U16()
	if h.Version == 2 {
		l.UnmodSince = fromTimeT(d.U64())
	}
	if h.Version >= 3 {
		_ = d.U64()
		l.UnmodSince = d.Time()
	}
	if h.Version >= 4 {
		l.HighPrecisionTime = d.Bool()
	}
	if h.Version >= 5 {
		l.ZonesTrace = decodeZoneSet(d)
	}
	d.EndStruct(h)
	return l
}

// timeT is real_clock::to_time_t for a time from the epoch on, with the zero
// time.Time as the epoch.
func timeT(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.Unix()) //nolint:gosec // C++ stores the time_t in a u64
}

// fromTimeT is real_clock::from_time_t, with the epoch as the zero
// time.Time, as denc decodes a real_time.
func fromTimeT(sec uint64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(int64(sec), 0).UTC() //nolint:gosec // C++ casts the u64 back to time_t
}

// LinkOLH adds bucket_link_olh, which links a version of the name under its
// OLH entry: l.Key's instance, or a delete marker for it, at l.OLHEpoch
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1652-L1865 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1781-L2000 at v20.2.4).
// Its ECANCELED means the entry carries another tag and is not pending
// removal. It succeeds without linking when the instance exists with an
// mtime no older than l.UnmodSince, compared in whole seconds unless
// l.HighPrecisionTime. radosgw adds it to a write op after assert_exists and
// GuardBucketResharding.
func LinkOLH(op radosclient.Execer, l LinkOLHOp, r denc.Release) {
	op.Exec(Class, methodBucketLinkOLH, clsutil.Encode(l, r))
}

// UnlinkInstanceOp is rgw_cls_unlink_instance_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L234-L276 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L232-L274 at v20.2.4),
// the input of bucket_unlink_instance. OLHTag is the OLH object's tag xattr
// as a string.
type UnlinkInstanceOp struct {
	Key        ObjKey
	OpTag      string
	OLHEpoch   uint64
	LogOp      bool
	BILogFlags uint16
	OLHTag     string
	ZonesTrace []string
}

// Encode mirrors rgw_cls_unlink_instance_op::encode, ENCODE_START(3, 1).
func (u UnlinkInstanceOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(3, 1)
	u.Key.Encode(e, r)
	e.String(u.OpTag)
	e.U64(u.OLHEpoch)
	e.Bool(u.LogOp)
	e.U16(u.BILogFlags)
	e.String(u.OLHTag)
	encodeZoneSet(e, u.ZonesTrace)
	e.EndStruct(f)
}

// DecodeUnlinkInstanceOp mirrors rgw_cls_unlink_instance_op::decode,
// DECODE_START(3).
func DecodeUnlinkInstanceOp(d *denc.Decoder) UnlinkInstanceOp {
	h := d.BeginStruct(3)
	var u UnlinkInstanceOp
	u.Key = DecodeObjKey(d)
	u.OpTag = d.String()
	u.OLHEpoch = d.U64()
	u.LogOp = d.Bool()
	u.BILogFlags = d.U16()
	if h.Version >= 2 {
		u.OLHTag = d.String()
	}
	if h.Version >= 3 {
		u.ZonesTrace = decodeZoneSet(d)
	}
	d.EndStruct(h)
	return u
}

// UnlinkInstance adds bucket_unlink_instance, which unlinks u.Key's
// instance from the name's OLH entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1867-L2018 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2002-L2159 at v20.2.4).
// It fails with ENOENT when the instance has no entry. The class never
// compares u.OLHTag with the entry's tag; it stores it only on an entry the
// call creates. radosgw adds it to a write op after assert_exists and
// GuardBucketResharding.
func UnlinkInstance(op radosclient.Execer, u UnlinkInstanceOp, r denc.Release) {
	op.Exec(Class, methodBucketUnlinkInstance, clsutil.Encode(u, r))
}

// ReadOLHLogOp is rgw_cls_read_olh_log_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L278-L303 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L276-L301 at v20.2.4),
// the input of bucket_read_olh_log. OLH names the name with no instance.
type ReadOLHLogOp struct {
	OLH       ObjKey
	VerMarker uint64
	OLHTag    string
}

// Encode mirrors rgw_cls_read_olh_log_op::encode, ENCODE_START(1, 1).
func (q ReadOLHLogOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	q.OLH.Encode(e, r)
	e.U64(q.VerMarker)
	e.String(q.OLHTag)
	e.EndStruct(f)
}

// DecodeReadOLHLogOp mirrors rgw_cls_read_olh_log_op::decode, DECODE_START(1).
func DecodeReadOLHLogOp(d *denc.Decoder) ReadOLHLogOp {
	h := d.BeginStruct(1)
	var q ReadOLHLogOp
	q.OLH = DecodeObjKey(d)
	q.VerMarker = d.U64()
	q.OLHTag = d.String()
	d.EndStruct(h)
	return q
}

// ReadOLHLogRet is rgw_cls_read_olh_log_ret
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L306-L328 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L304-L326 at v20.2.4),
// bucket_read_olh_log's output: pending log entries by epoch, and whether
// epochs past them remain.
type ReadOLHLogRet struct {
	Log         map[uint64][]OLHLogEntry
	IsTruncated bool
}

// Encode mirrors rgw_cls_read_olh_log_ret::encode, ENCODE_START(1, 1).
func (q ReadOLHLogRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	encodeOLHLog(e, q.Log, r)
	e.Bool(q.IsTruncated)
	e.EndStruct(f)
}

// DecodeReadOLHLogRet mirrors rgw_cls_read_olh_log_ret::decode, DECODE_START(1).
func DecodeReadOLHLogRet(d *denc.Decoder) ReadOLHLogRet {
	h := d.BeginStruct(1)
	var q ReadOLHLogRet
	q.Log = decodeOLHLog(d)
	q.IsTruncated = d.Bool()
	d.EndStruct(h)
	return q
}

// ReadOLHLogResult is the pending output of a bucket_read_olh_log call.
type ReadOLHLogResult struct {
	res *radosclient.ExecResult
}

// Result decodes the log once the op has run.
func (r *ReadOLHLogResult) Result() (ReadOLHLogRet, error) {
	return clsutil.DecodeReply(r.res, Class, methodBucketReadOLHLog, DecodeReadOLHLogRet)
}

// ReadOLHLog adds bucket_read_olh_log, which answers the OLH entry's pending
// log after q.VerMarker
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2020-L2072 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2161-L2213 at v20.2.4):
// the whole log when its first epoch is above q.VerMarker and it holds at
// most 1000 epochs, otherwise the first 1000 epochs above q.VerMarker with
// IsTruncated set when more remain. It fails with EINVAL when q.OLH has an
// instance, and with ECANCELED when q.OLHTag is not the entry's tag, a
// missing entry's tag being empty. radosgw adds it to a read op, unguarded.
func ReadOLHLog(op *radosclient.ReadOp, q ReadOLHLogOp, r denc.Release) *ReadOLHLogResult {
	return &ReadOLHLogResult{res: op.Exec(Class, methodBucketReadOLHLog, clsutil.Encode(q, r))}
}

// TrimOLHLogOp is rgw_cls_trim_olh_log_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L330-L355 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L328-L353 at v20.2.4),
// the input of bucket_trim_olh_log.
type TrimOLHLogOp struct {
	OLH    ObjKey
	Ver    uint64
	OLHTag string
}

// Encode mirrors rgw_cls_trim_olh_log_op::encode, ENCODE_START(1, 1).
func (t TrimOLHLogOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	t.OLH.Encode(e, r)
	e.U64(t.Ver)
	e.String(t.OLHTag)
	e.EndStruct(f)
}

// DecodeTrimOLHLogOp mirrors rgw_cls_trim_olh_log_op::decode, DECODE_START(1).
func DecodeTrimOLHLogOp(d *denc.Decoder) TrimOLHLogOp {
	h := d.BeginStruct(1)
	var t TrimOLHLogOp
	t.OLH = DecodeObjKey(d)
	t.Ver = d.U64()
	t.OLHTag = d.String()
	d.EndStruct(h)
	return t
}

// TrimOLHLog adds bucket_trim_olh_log, which drops the pending log's epochs
// up to and including t.Ver
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2074-L2124 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2215-L2277 at v20.2.4).
// It fails with EINVAL and ECANCELED as ReadOLHLog does. radosgw adds it to a
// write op after assert_exists and GuardBucketResharding.
func TrimOLHLog(op radosclient.Execer, t TrimOLHLogOp, r denc.Release) {
	op.Exec(Class, methodBucketTrimOLHLog, clsutil.Encode(t, r))
}

// ClearOLHOp is rgw_cls_bucket_clear_olh_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L357-L380 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L355-L378 at v20.2.4),
// the input of bucket_clear_olh.
type ClearOLHOp struct {
	Key    ObjKey
	OLHTag string
}

// Encode mirrors rgw_cls_bucket_clear_olh_op::encode, ENCODE_START(1, 1).
func (c ClearOLHOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	c.Key.Encode(e, r)
	e.String(c.OLHTag)
	e.EndStruct(f)
}

// DecodeClearOLHOp mirrors rgw_cls_bucket_clear_olh_op::decode, DECODE_START(1).
func DecodeClearOLHOp(d *denc.Decoder) ClearOLHOp {
	h := d.BeginStruct(1)
	var c ClearOLHOp
	c.Key = DecodeObjKey(d)
	c.OLHTag = d.String()
	d.EndStruct(h)
	return c
}

// ClearOLH adds bucket_clear_olh, which removes the name's OLH entry and,
// when the plain entry is a version marker, that entry too
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2126-L2190 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2279-L2355 at v20.2.4).
// It fails with EINVAL and ECANCELED as ReadOLHLog does. radosgw adds it to a
// write op after assert_exists and GuardBucketResharding.
func ClearOLH(op radosclient.Execer, c ClearOLHOp, r denc.Release) {
	op.Exec(Class, methodBucketClearOLH, clsutil.Encode(c, r))
}
