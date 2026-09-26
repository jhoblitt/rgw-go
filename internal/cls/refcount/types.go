package refcount

import (
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// XattrName is REFCOUNT_ATTR, the xattr the class stores the Refcount in.
const XattrName = "refcount"

// Refcount is obj_refcount. Refs maps each tag holding the object to true;
// the class never stores false, but decodes whatever is there. RetiredRefs is
// a std::set: the tags already put, so a repeated put is a no-op.
type Refcount struct {
	Refs        map[string]bool
	RetiredRefs []string
}

// Encode mirrors obj_refcount::encode, ENCODE_START(2, 1). RetiredRefs is
// written sorted and without duplicates, as a std::set holds it.
func (rc Refcount) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	denc.EncodeMap(e, rc.Refs, (*denc.Encoder).String, (*denc.Encoder).Bool)
	denc.EncodeSlice(e, stringSet(rc.RetiredRefs), (*denc.Encoder).String)
	e.EndStruct(f)
}

// DecodeRefcount mirrors obj_refcount::decode, DECODE_START(2); retired refs
// arrived in version 2.
func DecodeRefcount(d *denc.Decoder) Refcount {
	h := d.BeginStruct(2)
	var rc Refcount
	rc.Refs = denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).Bool)
	if h.Version >= 2 {
		rc.RetiredRefs = stringSet(denc.DecodeSlice(d, (*denc.Decoder).String))
	}
	d.EndStruct(h)
	return rc
}

// stringSet returns xs sorted without duplicates, a std::set<std::string>'s
// iteration order; nil stays nil.
func stringSet(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return slices.Compact(s)
}

// GetOp is cls_refcount_get_op, the "get" request. ImplicitRef makes an
// object without a refcount xattr count as held by the wildcard tag "".
type GetOp struct {
	Tag         string
	ImplicitRef bool
}

// Encode mirrors cls_refcount_get_op::encode, ENCODE_START(1, 1).
func (o GetOp) Encode(e *denc.Encoder, _ denc.Release) { encodeTagOp(e, o.Tag, o.ImplicitRef) }

// DecodeGetOp mirrors cls_refcount_get_op::decode, DECODE_START(1).
func DecodeGetOp(d *denc.Decoder) GetOp {
	tag, implicit := decodeTagOp(d)
	return GetOp{Tag: tag, ImplicitRef: implicit}
}

// PutOp is cls_refcount_put_op, the "put" request, the same shape as GetOp.
type PutOp struct {
	Tag         string
	ImplicitRef bool
}

// Encode mirrors cls_refcount_put_op::encode, ENCODE_START(1, 1).
func (o PutOp) Encode(e *denc.Encoder, _ denc.Release) { encodeTagOp(e, o.Tag, o.ImplicitRef) }

// DecodePutOp mirrors cls_refcount_put_op::decode, DECODE_START(1).
func DecodePutOp(d *denc.Decoder) PutOp {
	tag, implicit := decodeTagOp(d)
	return PutOp{Tag: tag, ImplicitRef: implicit}
}

func encodeTagOp(e *denc.Encoder, tag string, implicit bool) {
	f := e.BeginStruct(1, 1)
	e.String(tag)
	e.Bool(implicit)
	e.EndStruct(f)
}

func decodeTagOp(d *denc.Decoder) (string, bool) {
	h := d.BeginStruct(1)
	tag := d.String()
	implicit := d.Bool()
	d.EndStruct(h)
	return tag, implicit
}

// SetOp is cls_refcount_set_op, the "set" request. Refs is a std::list, sent
// as given.
type SetOp struct {
	Refs []string
}

// Encode mirrors cls_refcount_set_op::encode, ENCODE_START(1, 1).
func (o SetOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, o.Refs, (*denc.Encoder).String)
	e.EndStruct(f)
}

// DecodeSetOp mirrors cls_refcount_set_op::decode, DECODE_START(1).
func DecodeSetOp(d *denc.Decoder) SetOp {
	h := d.BeginStruct(1)
	o := SetOp{Refs: denc.DecodeSlice(d, (*denc.Decoder).String)}
	d.EndStruct(h)
	return o
}

// ReadOp is cls_refcount_read_op, the "read" request.
type ReadOp struct {
	ImplicitRef bool
}

// Encode mirrors cls_refcount_read_op::encode, ENCODE_START(1, 1).
func (o ReadOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Bool(o.ImplicitRef)
	e.EndStruct(f)
}

// DecodeReadOp mirrors cls_refcount_read_op::decode, DECODE_START(1).
func DecodeReadOp(d *denc.Decoder) ReadOp {
	h := d.BeginStruct(1)
	o := ReadOp{ImplicitRef: d.Bool()}
	d.EndStruct(h)
	return o
}

// ReadRet is cls_refcount_read_ret, the "read" reply: the held tags in
// std::map order, whether or not their value is true.
type ReadRet struct {
	Refs []string
}

// Encode mirrors cls_refcount_read_ret::encode, ENCODE_START(1, 1).
func (o ReadRet) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, o.Refs, (*denc.Encoder).String)
	e.EndStruct(f)
}

// DecodeReadRet mirrors cls_refcount_read_ret::decode, DECODE_START(1).
func DecodeReadRet(d *denc.Decoder) ReadRet {
	h := d.BeginStruct(1)
	o := ReadRet{Refs: denc.DecodeSlice(d, (*denc.Decoder).String)}
	d.EndStruct(h)
	return o
}
