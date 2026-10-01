package fakerados

import (
	"crypto/rand"
	"encoding/base64"
	"syscall"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// VersionWriteMethods are the methods of the version class registered with
// CLS_METHOD_WR (cls_version.cc:230-234 at v19.2.6 and v20.2.4), which
// RegisterClass takes with VersionClass.
var VersionWriteMethods = []string{"set", "inc", "inc_conds"}

// VersionClass emulates cls_version (src/cls/version/cls_version.cc at
// v19.2.6 and v20.2.4): set, inc, inc_conds, read and check_conds over the
// xattr version.XattrName. It reads the version from the stored object, as
// cls_cxx_getxattr does, so an object without one, or one missing before the
// op, reads as the zero version; inc gives such an object version 1 with a
// random tag before incrementing it. A failed condition is ECANCELED.
func VersionClass() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		switch call.Method {
		case "set":
			op, rval := decodeRequest(call.In, version.DecodeSetOp)
			if rval < 0 {
				return nil, rval
			}
			return nil, setVersion(call, op.Objv)
		case "inc", "inc_conds":
			op, rval := decodeRequest(call.In, version.DecodeIncOp)
			if rval < 0 {
				return nil, rval
			}
			return nil, incVersion(call, op.Conds)
		case "check_conds":
			op, rval := decodeRequest(call.In, version.DecodeCheckOp)
			if rval < 0 {
				return nil, rval
			}
			cur, _, rval := storedVersion(call)
			if rval < 0 {
				return nil, rval
			}
			if !checkConds(op.Conds, cur) {
				return nil, -int32(syscall.ECANCELED)
			}
			return nil, 0
		case "read":
			cur, _, rval := storedVersion(call)
			if rval < 0 {
				return nil, rval
			}
			e := denc.NewEncoder()
			version.ReadRet{Objv: cur}.Encode(e, denc.Squid)
			return e.Bytes(), 0
		}
		return nil, -int32(syscall.EOPNOTSUPP)
	}
}

// decodeRequest decodes a method's input, answering EINVAL for one that does
// not decode, as each method does.
func decodeRequest[T any](in []byte, decode func(*denc.Decoder) T) (op T, rval int32) {
	d := denc.NewDecoder(in)
	op = decode(d)
	if d.Err() != nil {
		return op, -int32(syscall.EINVAL)
	}
	return op, 0
}

// storedVersion is read_version without implicit creation: the stored
// object's version and whether it has one, the zero version when it or its
// xattr is missing, and EIO when the xattr does not decode.
func storedVersion(call *ClassCall) (v version.ObjVersion, present bool, rval int32) {
	if call.Stored == nil {
		return version.ObjVersion{}, false, 0
	}
	b, ok := call.Stored.Xattrs[version.XattrName]
	if !ok {
		return version.ObjVersion{}, false, 0
	}
	d := denc.NewDecoder(b)
	v = version.DecodeObjVersion(d)
	if d.Err() != nil {
		return version.ObjVersion{}, true, -int32(syscall.EIO)
	}
	return v, true, 0
}

// setVersion stores v in the op's object, creating it.
func setVersion(call *ClassCall, v version.ObjVersion) int32 {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	call.Create().Xattrs[version.XattrName] = e.Bytes()
	return 0
}

// incVersion is cls_version_inc: read_version with implicit creation, which
// initializes a missing xattr only, so a stored version 0 keeps its tag
// (cls_version.cc:58-66), then the conditions and the increment.
func incVersion(call *ClassCall, conds []version.Condition) int32 {
	cur, present, rval := storedVersion(call)
	if rval < 0 {
		return rval
	}
	if !present {
		// init_version's TAG_LEN characters of cls_gen_rand_base64.
		var b [18]byte
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		cur = version.ObjVersion{Ver: 1, Tag: base64.StdEncoding.EncodeToString(b[:])}
	}
	if !checkConds(conds, cur) {
		return -int32(syscall.ECANCELED)
	}
	cur.Ver++
	return setVersion(call, cur)
}

// checkConds is check_conds: EQ compares both fields, the orderings the
// counter alone, TAG_EQ and TAG_NE the tag alone, and a condition no case
// names holds.
func checkConds(conds []version.Condition, cur version.ObjVersion) bool {
	for _, c := range conds {
		var holds bool
		switch c.Cond {
		case version.CondEQ:
			holds = cur == c.Ver
		case version.CondGT:
			holds = cur.Ver > c.Ver.Ver
		case version.CondGE:
			holds = cur.Ver >= c.Ver.Ver
		case version.CondLT:
			holds = cur.Ver < c.Ver.Ver
		case version.CondLE:
			holds = cur.Ver <= c.Ver.Ver
		case version.CondTagEQ:
			holds = cur.Tag == c.Ver.Tag
		case version.CondTagNE:
			holds = cur.Tag != c.Ver.Tag
		default:
			holds = true
		}
		if !holds {
			return false
		}
	}
	return true
}
