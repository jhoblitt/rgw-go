package fakerados

import (
	"maps"
	"slices"
	"syscall"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// RefcountWriteMethods are the methods of the refcount class registered with
// CLS_METHOD_WR (cls_refcount.cc:210-212 at v19.2.6 and v20.2.4), which
// RegisterClass takes with RefcountClass.
var RefcountWriteMethods = []string{"get", "put", "set"}

// wildcardTag is the class's wildcard_tag, the empty tag an object without
// a refcount xattr is held by when a call passes implicit_ref.
const wildcardTag = ""

// RefcountClass emulates the refcount class (src/cls/refcount/cls_refcount.cc
// at v19.2.6 and v20.2.4) over the refcount xattr: get, put, set and read. It
// reads the refcount from the stored object, as cls_cxx_getxattr does, so a
// missing object is ENOENT for get, put and read.
func RefcountClass() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		switch call.Method {
		case "get":
			op, rval := decodeRequest(call.In, refcount.DecodeGetOp)
			if rval < 0 {
				return nil, rval
			}
			rc, rval := readRefcount(call, op.ImplicitRef)
			if rval < 0 {
				return nil, rval
			}
			rc.Refs[op.Tag] = true
			call.Create().Xattrs[refcount.XattrName] = encodeSquid(rc)
			return nil, 0
		case "put":
			op, rval := decodeRequest(call.In, refcount.DecodePutOp)
			if rval < 0 {
				return nil, rval
			}
			return nil, refcountPut(call, op)
		case "set":
			op, rval := decodeRequest(call.In, refcount.DecodeSetOp)
			if rval < 0 {
				return nil, rval
			}
			if len(op.Refs) == 0 {
				return nil, call.Remove()
			}
			rc := refcount.Refcount{Refs: map[string]bool{}}
			for _, tag := range op.Refs {
				rc.Refs[tag] = true
			}
			call.Create().Xattrs[refcount.XattrName] = encodeSquid(rc)
			return nil, 0
		case "read":
			op, rval := decodeRequest(call.In, refcount.DecodeReadOp)
			if rval < 0 {
				return nil, rval
			}
			rc, rval := readRefcount(call, op.ImplicitRef)
			if rval < 0 {
				return nil, rval
			}
			return encodeSquid(refcount.ReadRet{Refs: slices.Sorted(maps.Keys(rc.Refs))}), 0
		}
		return nil, -int32(syscall.EOPNOTSUPP)
	}
}

// readRefcount is read_refcount (cls_refcount.cc:22-45): the stored
// refcount, the wildcard alone for an object without one when implicit, and
// ENOENT for a missing object, EIO for one that does not decode.
func readRefcount(call *ClassCall, implicit bool) (rc refcount.Refcount, rval int32) {
	if call.Stored == nil {
		return refcount.Refcount{}, -int32(syscall.ENOENT)
	}
	rc = refcount.Refcount{Refs: map[string]bool{}}
	b, ok := call.Stored.Xattrs[refcount.XattrName]
	if !ok {
		if implicit {
			rc.Refs[wildcardTag] = true
		}
		return rc, 0
	}
	d := denc.NewDecoder(b)
	rc = refcount.DecodeRefcount(d)
	if d.Err() != nil {
		return refcount.Refcount{}, -int32(syscall.EIO)
	}
	if rc.Refs == nil {
		rc.Refs = map[string]bool{}
	}
	return rc, 0
}

// refcountPut is cls_rc_refcount_put (cls_refcount.cc:88-139): no refs at
// all is EINVAL; a tag neither held nor covered by the wildcard, or already
// retired, changes nothing; otherwise the ref goes, the tag is retired, and
// the object is removed when no ref is left.
func refcountPut(call *ClassCall, op refcount.PutOp) int32 {
	rc, rval := readRefcount(call, op.ImplicitRef)
	if rval < 0 {
		return rval
	}
	if len(rc.Refs) == 0 {
		return -int32(syscall.EINVAL)
	}
	held := op.Tag
	if _, ok := rc.Refs[held]; !ok {
		if !op.ImplicitRef {
			return 0
		}
		if _, ok := rc.Refs[wildcardTag]; !ok {
			return 0
		}
		held = wildcardTag
	}
	if slices.Contains(rc.RetiredRefs, op.Tag) {
		return 0
	}
	rc.RetiredRefs = append(rc.RetiredRefs, op.Tag)
	delete(rc.Refs, held)
	if len(rc.Refs) == 0 {
		return call.Remove()
	}
	call.Create().Xattrs[refcount.XattrName] = encodeSquid(rc)
	return 0
}
