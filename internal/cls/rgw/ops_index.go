package rgw

import (
	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// GuardOp is cls_rgw_guard_bucket_resharding_op, the input of
// guard_bucket_resharding.
type GuardOp struct {
	RetErr int32
}

// Encode mirrors cls_rgw_guard_bucket_resharding_op::encode, ENCODE_START(1, 1).
func (g GuardOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.I32(g.RetErr)
	e.EndStruct(f)
}

// DecodeGuardOp mirrors cls_rgw_guard_bucket_resharding_op::decode, DECODE_START(1).
func DecodeGuardOp(d *denc.Decoder) GuardOp {
	h := d.BeginStruct(1)
	g := GuardOp{RetErr: d.I32()}
	d.EndStruct(h)
	return g
}

// GuardBucketResharding adds guard_bucket_resharding with ret_err
// -ErrBusyResharding, as radosgw always sends it. While the shard is
// resharding the class returns ret_err verbatim, failing the whole op with
// radosclient.ErrBusyResharding; a positive value would read as success and
// let the guarded method run. On every release radosgw adds it before
// bucket_prepare_op, bucket_complete_op and the OLH methods link_olh,
// unlink_instance, trim_olh_log and clear_olh. From Tentacle on it also adds
// it before dir_suggest_changes, in both listing paths (v20.2.4
// rgw_rados.cc 10823, 11061); Squid sends that unguarded. It guards no other
// index method.
func GuardBucketResharding(op radosclient.Execer, r denc.Release) {
	op.Exec(Class, methodGuardBucketResharding, clsutil.Encode(GuardOp{RetErr: -ErrBusyResharding}, r))
}

// SetReshardingOp is cls_rgw_set_bucket_resharding_op, the input of
// set_bucket_resharding.
type SetReshardingOp struct {
	Entry InstanceEntry
}

// Encode mirrors cls_rgw_set_bucket_resharding_op::encode, ENCODE_START(1, 1).
func (o SetReshardingOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Entry.Encode(e, r)
	e.EndStruct(f)
}

// DecodeSetReshardingOp mirrors cls_rgw_set_bucket_resharding_op::decode, DECODE_START(1).
func DecodeSetReshardingOp(d *denc.Decoder) SetReshardingOp {
	h := d.BeginStruct(1)
	o := SetReshardingOp{Entry: DecodeInstanceEntry(d)}
	d.EndStruct(h)
	return o
}

// SetBucketResharding adds set_bucket_resharding, which sets the shard
// header's reshard status to entry's, as cls_rgw_set_bucket_resharding does.
func SetBucketResharding(op radosclient.Execer, entry InstanceEntry, r denc.Release) {
	op.Exec(Class, methodSetBucketResharding, clsutil.Encode(SetReshardingOp{Entry: entry}, r))
}

// getBucketReshardingOp is cls_rgw_get_bucket_resharding_op, the input of
// get_bucket_resharding: ENCODE_START(1, 1) around no fields.
type getBucketReshardingOp struct{}

func (getBucketReshardingOp) Encode(e *denc.Encoder, _ denc.Release) {
	e.EndStruct(e.BeginStruct(1, 1))
}

// GetBucketReshardingResult is the pending output of a get_bucket_resharding
// call.
type GetBucketReshardingResult struct {
	res *radosclient.ExecResult
}

// Result decodes cls_rgw_get_bucket_resharding_ret, ENCODE_START(1, 1)
// around the shard header's cls_rgw_bucket_instance_entry, once the op has
// run.
func (r *GetBucketReshardingResult) Result() (InstanceEntry, error) {
	return clsutil.DecodeReply(r.res, Class, methodGetBucketResharding, func(d *denc.Decoder) InstanceEntry {
		h := d.BeginStruct(1)
		i := DecodeInstanceEntry(d)
		d.EndStruct(h)
		return i
	})
}

// GetBucketResharding adds get_bucket_resharding, which answers the shard
// header's reshard status. radosgw polls it while it waits out a reshard
// (block_while_resharding), on a read op at every release.
func GetBucketResharding(op *radosclient.ReadOp, r denc.Release) *GetBucketReshardingResult {
	return &GetBucketReshardingResult{res: op.Exec(Class, methodGetBucketResharding, clsutil.Encode(getBucketReshardingOp{}, r))}
}

// BucketInitIndex adds bucket_init_index, which writes an empty header to a
// new shard. Its input is empty; radosgw precedes it with an exclusive create.
func BucketInitIndex(op radosclient.Execer) {
	op.Exec(Class, methodBucketInitIndex, nil)
}

// TagTimeoutOp is rgw_cls_tag_timeout_op, the input of bucket_set_tag_timeout.
type TagTimeoutOp struct {
	TagTimeout uint64
}

// Encode mirrors rgw_cls_tag_timeout_op::encode, ENCODE_START(1, 1).
func (t TagTimeoutOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(t.TagTimeout)
	e.EndStruct(f)
}

// DecodeTagTimeoutOp mirrors rgw_cls_tag_timeout_op::decode, DECODE_START(1).
func DecodeTagTimeoutOp(d *denc.Decoder) TagTimeoutOp {
	h := d.BeginStruct(1)
	t := TagTimeoutOp{TagTimeout: d.U64()}
	d.EndStruct(h)
	return t
}

// BucketSetTagTimeout adds bucket_set_tag_timeout, which sets the seconds
// after which a shard's pending entries count as stale.
func BucketSetTagTimeout(op radosclient.Execer, timeout uint64, r denc.Release) {
	op.Exec(Class, methodBucketSetTagTimeout, clsutil.Encode(TagTimeoutOp{TagTimeout: timeout}, r))
}

// PrepareOp is rgw_cls_obj_prepare_op, the input of bucket_prepare_op.
// ZonesTrace holds rgw_zone_set entries in their "zone[:location_key]" form.
type PrepareOp struct {
	Op         ModifyOp
	Key        ObjKey
	Tag        string
	Locator    string
	LogOp      bool
	BILogFlags uint16
	ZonesTrace []string
}

// Encode mirrors rgw_cls_obj_prepare_op::encode, ENCODE_START(7, 5).
func (p PrepareOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(7, 5)
	e.U8(uint8(p.Op))
	e.String(p.Tag)
	e.String(p.Locator)
	e.Bool(p.LogOp)
	p.Key.Encode(e, r)
	e.U16(p.BILogFlags)
	encodeZoneSet(e, p.ZonesTrace)
	e.EndStruct(f)
}

// DecodePrepareOp mirrors rgw_cls_obj_prepare_op::decode,
// DECODE_START_LEGACY_COMPAT_LEN(7, 3, 3). Below version 5 only the key
// name is carried, ahead of the tag. The C++ default op is OpUnknown.
func DecodePrepareOp(d *denc.Decoder) PrepareOp {
	h := d.BeginStructLegacy(7, 3, 3, 0)
	var p PrepareOp
	p.Op = ModifyOp(d.U8())
	if h.Version < 5 {
		p.Key.Name = d.String()
	}
	p.Tag = d.String()
	if h.Version >= 2 {
		p.Locator = d.String()
	}
	if h.Version >= 4 {
		p.LogOp = d.Bool()
	}
	if h.Version >= 5 {
		p.Key = DecodeObjKey(d)
	}
	if h.Version >= 6 {
		p.BILogFlags = d.U16()
	}
	if h.Version >= 7 {
		p.ZonesTrace = decodeZoneSet(d)
	}
	d.EndStruct(h)
	return p
}

// BucketPrepareOp adds bucket_prepare_op, which records a pending change
// under p.Tag before the head object is written.
func BucketPrepareOp(op radosclient.Execer, p PrepareOp, r denc.Release) {
	op.Exec(Class, methodBucketPrepareOp, clsutil.Encode(p, r))
}

// CompleteOp is rgw_cls_obj_complete_op, the input of bucket_complete_op.
// Its C++ default has Ver.Pool -1.
type CompleteOp struct {
	Op         ModifyOp
	Key        ObjKey
	Locator    string
	Ver        EntryVer
	Meta       DirEntryMeta
	Tag        string
	LogOp      bool
	BILogFlags uint16
	RemoveObjs []ObjKey
	ZonesTrace []string
}

// Encode mirrors rgw_cls_obj_complete_op::encode, ENCODE_START(9, 7): the
// epoch is written alone and again inside ver.
func (c CompleteOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(9, 7)
	e.U8(uint8(c.Op))
	e.U64(c.Ver.Epoch)
	c.Meta.Encode(e, r)
	e.String(c.Tag)
	e.String(c.Locator)
	denc.EncodeSlice(e, c.RemoveObjs, func(e *denc.Encoder, k ObjKey) { k.Encode(e, r) })
	c.Ver.Encode(e, r)
	e.Bool(c.LogOp)
	c.Key.Encode(e, r)
	e.U16(c.BILogFlags)
	encodeZoneSet(e, c.ZonesTrace)
	e.EndStruct(f)
}

// DecodeCompleteOp mirrors rgw_cls_obj_complete_op::decode,
// DECODE_START_LEGACY_COMPAT_LEN(9, 3, 3). Below version 7 only the key name
// is carried, first; versions 4 to 6 list the objects to remove by name
// alone, while every other version, 3 included, lists keys.
func DecodeCompleteOp(d *denc.Decoder) CompleteOp {
	h := d.BeginStructLegacy(9, 3, 3, 0)
	var c CompleteOp
	c.Op = ModifyOp(d.U8())
	if h.Version < 7 {
		c.Key.Name = d.String()
	}
	c.Ver.Epoch = d.U64()
	c.Meta = DecodeDirEntryMeta(d)
	c.Tag = d.String()
	if h.Version >= 2 {
		c.Locator = d.String()
	}
	if h.Version >= 4 && h.Version < 7 {
		c.RemoveObjs = denc.DecodeSlice(d, func(d *denc.Decoder) ObjKey { return ObjKey{Name: d.String()} })
	} else {
		c.RemoveObjs = denc.DecodeSlice(d, DecodeObjKey)
	}
	if h.Version >= 5 {
		c.Ver = DecodeEntryVer(d)
	} else {
		c.Ver.Pool = -1
	}
	if h.Version >= 6 {
		c.LogOp = d.Bool()
	}
	if h.Version >= 7 {
		c.Key = DecodeObjKey(d)
	}
	if h.Version >= 8 {
		c.BILogFlags = d.U16()
	}
	if h.Version >= 9 {
		c.ZonesTrace = decodeZoneSet(d)
	}
	d.EndStruct(h)
	return c
}

// BucketCompleteOp adds bucket_complete_op, which applies or cancels the
// change prepared under c.Tag.
func BucketCompleteOp(op radosclient.Execer, c CompleteOp, r denc.Release) {
	op.Exec(Class, methodBucketCompleteOp, clsutil.Encode(c, r))
}

// ListOp is rgw_cls_list_op, the input of bucket_list.
type ListOp struct {
	StartObj     ObjKey
	NumEntries   uint32
	FilterPrefix string
	ListVersions bool
	Delimiter    string
}

// Encode mirrors rgw_cls_list_op::encode, ENCODE_START(6, 4).
func (l ListOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(6, 4)
	e.U32(l.NumEntries)
	e.String(l.FilterPrefix)
	l.StartObj.Encode(e, r)
	e.Bool(l.ListVersions)
	e.String(l.Delimiter)
	e.EndStruct(f)
}

// DecodeListOp mirrors rgw_cls_list_op::decode,
// DECODE_START_LEGACY_COMPAT_LEN(6, 2, 2). Below version 4 only the start
// key's name is carried, first.
func DecodeListOp(d *denc.Decoder) ListOp {
	h := d.BeginStructLegacy(6, 2, 2, 0)
	var l ListOp
	if h.Version < 4 {
		l.StartObj.Name = d.String()
	}
	l.NumEntries = d.U32()
	if h.Version >= 3 {
		l.FilterPrefix = d.String()
	}
	if h.Version >= 4 {
		l.StartObj = DecodeObjKey(d)
	}
	if h.Version >= 5 {
		l.ListVersions = d.Bool()
	}
	if h.Version >= 6 {
		l.Delimiter = d.String()
	}
	d.EndStruct(h)
	return l
}

// ListRet is rgw_cls_list_ret, bucket_list's output: the shard header, the
// entries found, and where to continue when truncated.
type ListRet struct {
	Dir         Dir
	IsTruncated bool
	Marker      ObjKey
}

// Encode mirrors rgw_cls_list_ret::encode, ENCODE_START(4, 2).
func (l ListRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(4, 2)
	l.Dir.Encode(e, r)
	e.Bool(l.IsTruncated)
	l.Marker.Encode(e, r)
	e.EndStruct(f)
}

// DecodeListRet mirrors rgw_cls_list_ret::decode,
// DECODE_START_LEGACY_COMPAT_LEN(4, 2, 2).
func DecodeListRet(d *denc.Decoder) ListRet {
	h := d.BeginStructLegacy(4, 2, 2, 0)
	var l ListRet
	l.Dir = DecodeDir(d)
	l.IsTruncated = d.Bool()
	if h.Version >= 4 {
		l.Marker = DecodeObjKey(d)
	}
	d.EndStruct(h)
	return l
}

// ListResult is the pending output of a bucket_list call.
type ListResult struct {
	res *radosclient.ExecResult
}

// Result decodes the listing once the op has run.
func (res *ListResult) Result() (ListRet, error) {
	return clsutil.DecodeReply(res.res, Class, methodBucketList, DecodeListRet)
}

// BucketList adds bucket_list, which lists up to l.NumEntries entries after
// l.StartObj along with the shard header.
func BucketList(op *radosclient.ReadOp, l ListOp, r denc.Release) *ListResult {
	return &ListResult{res: op.Exec(Class, methodBucketList, clsutil.Encode(l, r))}
}

// GetDirHeader adds a bucket_list asking for no entries, which is how
// radosgw reads a shard header; the header is Result's Dir.Header.
func GetDirHeader(op *radosclient.ReadOp, r denc.Release) *ListResult {
	return BucketList(op, ListOp{}, r)
}

// Suggestion is one dir_suggest_changes element: SuggestRemove or
// SuggestUpdate, whether to log it, and the entry as the caller found it.
type Suggestion struct {
	Op    byte
	Log   bool
	Entry DirEntry
}

// SuggestChanges adds dir_suggest_changes, which reconciles index entries
// radosgw found stale while listing. The input has no framing: each change
// is its op byte, or'ed with SuggestLog to be logged, then the entry, as
// cls_rgw_encode_suggestion appends them. From Tentacle on radosgw puts
// GuardBucketResharding before it; Squid does not.
func SuggestChanges(op radosclient.Execer, changes []Suggestion, r denc.Release) {
	e := denc.NewEncoder()
	for i := range changes {
		b := changes[i].Op
		if changes[i].Log {
			b |= SuggestLog
		}
		e.U8(b)
		changes[i].Entry.Encode(e, r)
	}
	op.Exec(Class, methodDirSuggestChanges, e.Bytes())
}

// CheckIndexRet is rgw_cls_check_index_ret, bucket_check_index's output: the
// shard header as stored and as recalculated from its entries.
type CheckIndexRet struct {
	ExistingHeader   DirHeader
	CalculatedHeader DirHeader
}

// Encode mirrors rgw_cls_check_index_ret::encode, ENCODE_START(1, 1).
func (c CheckIndexRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	c.ExistingHeader.Encode(e, r)
	c.CalculatedHeader.Encode(e, r)
	e.EndStruct(f)
}

// DecodeCheckIndexRet mirrors rgw_cls_check_index_ret::decode, DECODE_START(1).
func DecodeCheckIndexRet(d *denc.Decoder) CheckIndexRet {
	h := d.BeginStruct(1)
	var c CheckIndexRet
	c.ExistingHeader = DecodeDirHeader(d)
	c.CalculatedHeader = DecodeDirHeader(d)
	d.EndStruct(h)
	return c
}

// CheckIndexResult is the pending output of a bucket_check_index call.
type CheckIndexResult struct {
	res *radosclient.ExecResult
}

// Result decodes the two headers once the op has run.
func (res *CheckIndexResult) Result() (CheckIndexRet, error) {
	return clsutil.DecodeReply(res.res, Class, methodBucketCheckIndex, DecodeCheckIndexRet)
}

// BucketCheckIndex adds bucket_check_index, whose input is empty.
func BucketCheckIndex(op *radosclient.ReadOp, _ denc.Release) *CheckIndexResult {
	return &CheckIndexResult{res: op.Exec(Class, methodBucketCheckIndex, nil)}
}

// BucketRebuildIndex adds bucket_rebuild_index, which rewrites the shard
// header's stats from its entries. Its input is empty.
func BucketRebuildIndex(op radosclient.Execer) {
	op.Exec(Class, methodBucketRebuildIndex, nil)
}
