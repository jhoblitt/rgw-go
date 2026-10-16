package fakerados

import (
	"strings"
	"syscall"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
)

// The method RGWClass emulates of those that check a head object's xattrs,
// obj_check_attrs_prefix, which the class registers CLS_METHOD_RD
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4707 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5124 at v20.2.4).
var _ = rgwTable(methodTable{
	"obj_check_attrs_prefix": {fn: noOutput(rgwObjCheckAttrsPrefix)},
})

// rgwObjCheckAttrsPrefix is rgw_obj_check_attrs_prefix
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2509-L2551 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2677-L2719 at v20.2.4,
// the same code): EINVAL for an empty prefix, and ECANCELED when whether an
// xattr has the prefix is fail_if_exist. The class walks the xattrs from the
// prefix's lower bound and stops at the first whose head sorts past it, which
// counts exactly the xattrs having it. cls_cxx_getxattrs reads the stored
// object, so an xattr an earlier step of the op set is not seen, and the class
// takes its ENOENT for a missing object as no xattrs.
func rgwObjCheckAttrsPrefix(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeCheckAttrsPrefixOp)
	if rval < 0 {
		return rval
	}
	if op.CheckPrefix == "" {
		return -int32(syscall.EINVAL)
	}
	exist := false
	if call.Stored != nil {
		for name := range call.Stored.Xattrs {
			if strings.HasPrefix(name, op.CheckPrefix) {
				exist = true
				break
			}
		}
	}
	if exist == op.FailIfExist {
		return -int32(syscall.ECANCELED)
	}
	return 0
}
