package fakerados

import (
	"syscall"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
)

// The method RGWClass emulates of those that read and write one index entry
// by its key, bi_get, which the class registers CLS_METHOD_RD
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4710 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5127 at v20.2.4).
var _ = rgwTable(methodTable{
	"bi_get": {fn: rgwBIGet},
})

// rgwBIGet is rgw_bi_get_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2617-L2664 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2785-L2832 at v20.2.4,
// the same code): the stored value of the key the type names, as stored.
// Plain names the name verbatim, so any key can be read through it; Instance
// is encode_obj_index_key, which keeps an empty instance on the plain key,
// the version marker once the name is versioned, and maps no "null"; OLH is
// the name's OLH data key, whatever the instance. Any other type is EINVAL
// and a key with no entry ENOENT.
func rgwBIGet(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeBIGetOp)
	if rval < 0 {
		return nil, rval
	}
	var idx string
	switch op.Type {
	case rgwcls.BIIndexPlain:
		idx = op.Key.Name
	case rgwcls.BIIndexInstance:
		idx = rgwObjIndexKey(op.Key)
	case rgwcls.BIIndexOLH:
		idx = rgwcls.OLHDataKey(op.Key.Name)
	default:
		return nil, -int32(syscall.EINVAL)
	}
	if call.Stored == nil {
		return nil, -int32(syscall.ENOENT)
	}
	b, ok := call.Stored.Omap[idx]
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	return encodeSquid(rgwcls.BIGetRet{Entry: rgwcls.BIEntry{Type: op.Type, Idx: idx, Data: b}}), 0
}
