package rgw

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// methodBIGet is RGW_BI_GET
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_const.h#L35 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_const.h#L35 at v20.2.4),
// which both releases register RD, as rgw_bi_get_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4710 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5127 at v20.2.4).
const methodBIGet = "bi_get"

// BIIndexType is BIIndexType, the namespace of a shard's omap an index entry
// lives in. It travels as a u8.
type BIIndexType uint8

// The BIIndexType values the class's bi_get accepts
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L471-L476 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L471-L477 at v20.2.4).
// Tentacle adds ReshardDeleted, 4, for its reshard log, which bi_get refuses.
const (
	BIIndexPlain    BIIndexType = 1
	BIIndexInstance BIIndexType = 2
	BIIndexOLH      BIIndexType = 3
)

// BIEntry is rgw_cls_bi_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L480-L511 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L481-L510 at v20.2.4):
// one omap entry of a shard, its namespace, its key, and its value as
// stored.
type BIEntry struct {
	Type BIIndexType
	Idx  string
	Data []byte
}

// Encode mirrors rgw_cls_bi_entry::encode, ENCODE_START(1, 1).
func (b BIEntry) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U8(uint8(b.Type))
	e.String(b.Idx)
	e.Bytes32(b.Data)
	e.EndStruct(f)
}

// DecodeBIEntry mirrors rgw_cls_bi_entry::decode, DECODE_START(1).
func DecodeBIEntry(d *denc.Decoder) BIEntry {
	h := d.BeginStruct(1)
	var b BIEntry
	b.Type = BIIndexType(d.U8())
	b.Idx = d.String()
	b.Data = d.Bytes32()
	d.EndStruct(h)
	return b
}

// BIGetOp is rgw_cls_bi_get_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L665-L700 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L667-L702 at v20.2.4),
// the input of bi_get.
type BIGetOp struct {
	Key  ObjKey
	Type BIIndexType
}

// Encode mirrors rgw_cls_bi_get_op::encode, ENCODE_START(1, 1).
func (b BIGetOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	b.Key.Encode(e, r)
	e.U8(uint8(b.Type))
	e.EndStruct(f)
}

// DecodeBIGetOp mirrors rgw_cls_bi_get_op::decode, DECODE_START(1).
func DecodeBIGetOp(d *denc.Decoder) BIGetOp {
	h := d.BeginStruct(1)
	var b BIGetOp
	b.Key = DecodeObjKey(d)
	b.Type = BIIndexType(d.U8())
	d.EndStruct(h)
	return b
}

// BIGetRet is rgw_cls_bi_get_ret
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L702-L728 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L704-L730 at v20.2.4),
// bi_get's output.
type BIGetRet struct {
	Entry BIEntry
}

// Encode mirrors rgw_cls_bi_get_ret::encode, ENCODE_START(1, 1).
func (b BIGetRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	b.Entry.Encode(e, r)
	e.EndStruct(f)
}

// DecodeBIGetRet mirrors rgw_cls_bi_get_ret::decode, DECODE_START(1).
func DecodeBIGetRet(d *denc.Decoder) BIGetRet {
	h := d.BeginStruct(1)
	b := BIGetRet{Entry: DecodeBIEntry(d)}
	d.EndStruct(h)
	return b
}

// BIGetResult is the pending output of a bi_get call. A missing entry is
// the class's ENOENT, which every method returns as radosclient.ErrNotFound.
type BIGetResult struct {
	res *radosclient.ExecResult
}

// Result returns the entry as stored once the op has run.
func (r *BIGetResult) Result() (BIEntry, error) {
	ret, err := clsutil.DecodeReply(r.res, Class, methodBIGet, DecodeBIGetRet)
	if err != nil {
		return BIEntry{}, err
	}
	return ret.Entry, nil
}

// DirEntry decodes the stored value of a Plain or Instance entry, an
// rgw_bucket_dir_entry at any version, as bi_get_instance does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9443-L9463 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10192-L10212 at v20.2.4).
func (r *BIGetResult) DirEntry() (DirEntry, error) {
	return decodeBIData(r, DecodeDirEntry)
}

// OLHEntry decodes the stored value of an OLH entry as bi_get_olh does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9465-L9485 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10214-L10234 at v20.2.4).
func (r *BIGetResult) OLHEntry() (OLHEntry, error) {
	return decodeBIData(r, DecodeOLHEntry)
}

// decodeBIData decodes the entry's stored value with dec, ignoring bytes past
// it as radosgw's decode from the value's iterator does.
func decodeBIData[T any](r *BIGetResult, dec func(*denc.Decoder) T) (T, error) {
	var zero T
	e, err := r.Result()
	if err != nil {
		return zero, err
	}
	d := denc.NewDecoder(e.Data)
	v := dec(d)
	if err := d.Err(); err != nil {
		return zero, fmt.Errorf("%s: decoding %s entry %q: %w", Class, methodBIGet, e.Idx, err)
	}
	return v, nil
}

// BIGet adds bi_get, which reads one index entry of the shard
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2617-L2664 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2785-L2832 at v20.2.4).
// The class keys it by typ: BIIndexPlain reads key.Name as the omap key
// verbatim, so any key, InstanceIndexKey's included, can be read through
// it; BIIndexInstance reads InstanceIndexKey(key, false), or the plain key
// key.Name when the instance is empty; BIIndexOLH reads OLHDataKey(key.Name).
// Any other typ fails with EINVAL. radosgw sends key.Instance verbatim and
// the class does not map "null" to the empty instance. radosgw runs it as a
// read op of its own.
func BIGet(op *radosclient.ReadOp, key ObjKey, typ BIIndexType, r denc.Release) *BIGetResult {
	return &BIGetResult{res: op.Exec(Class, methodBIGet, clsutil.Encode(BIGetOp{Key: key, Type: typ}, r))}
}
