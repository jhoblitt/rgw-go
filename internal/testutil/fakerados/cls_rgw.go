package fakerados

import (
	"syscall"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// RGWWriteMethods are the methods RGWClass emulates that the rgw class
// registers with CLS_METHOD_WR (cls_rgw.cc:4691 at v19.2.6, :5107 at
// v20.2.4), which RegisterClass takes with RGWClass.
var RGWWriteMethods = []string{"bucket_init_index"}

// RGWClass emulates the rgw class (src/cls/rgw/cls_rgw.cc at v19.2.6 and
// v20.2.4) over a bucket index shard's omap: one entry per index key, and
// the rgw_bucket_dir_header in the omap header. It runs bucket_init_index,
// and bucket_list asking for no entries, which is how radosgw reads a
// shard's header; every other method, and a bucket_list asking for entries,
// is EOPNOTSUPP. It reads the header from the stored object, as
// cls_cxx_map_read_header does, and encodes what it stores and replies at
// the Squid release, whose rgw_bucket_dir_header lacks only Tentacle's
// reshard log count.
func RGWClass() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		switch call.Method {
		case "bucket_init_index":
			return nil, rgwBucketInitIndex(call)
		case "bucket_list":
			return rgwBucketList(call)
		}
		return nil, -int32(syscall.EOPNOTSUPP)
	}
}

// rgwDirHeader is read_bucket_header (cls_rgw.cc:464-485 at v19.2.6,
// :514-535 at v20.2.4): the stored header, the zero header when the shard
// has none, and EIO when it does not decode.
func rgwDirHeader(call *ClassCall) (h rgwcls.DirHeader, rval int32) {
	if call.Stored == nil || len(call.Stored.OmapHdr) == 0 {
		return rgwcls.DirHeader{}, 0
	}
	d := denc.NewDecoder(call.Stored.OmapHdr)
	h = rgwcls.DecodeDirHeader(d)
	if d.Err() != nil {
		return rgwcls.DirHeader{}, -int32(syscall.EIO)
	}
	return h, 0
}

// rgwBucketInitIndex is rgw_bucket_init_index (cls_rgw.cc:741-764 at
// v19.2.6, :837-860 at v20.2.4): a shard that already has a header is
// EINVAL, "index already initialized", and any other gets an empty header
// at version 1, as write_bucket_header counts the write. radosgw precedes
// the call with an exclusive create, so it meets EEXIST from the create
// first.
func rgwBucketInitIndex(call *ClassCall) int32 {
	if call.Stored != nil && len(call.Stored.OmapHdr) != 0 {
		return -int32(syscall.EINVAL)
	}
	call.Create().OmapHdr = encodeSquid(rgwcls.DirHeader{Ver: 1})
	return 0
}

// rgwBucketList is rgw_bucket_list (cls_rgw.cc:487-694 at v19.2.6,
// :537-744 at v20.2.4) for a request of no entries: the shard's header,
// untruncated.
func rgwBucketList(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeListOp)
	if rval < 0 {
		return nil, rval
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return nil, rval
	}
	if op.NumEntries > 0 {
		return nil, -int32(syscall.EOPNOTSUPP)
	}
	return encodeSquid(rgwcls.ListRet{Dir: rgwcls.Dir{Header: h}}), 0
}
