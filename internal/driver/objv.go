package driver

import (
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// writeTagLen is generate_new_write_ver's TAG_LEN
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L3207 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L3269 at v20.2.4).
// append_rand_alpha spends it on an underscore and TAG_LEN-1 characters of
// gen_rand_alphanumeric
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1621-L1628 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1623-L1630 at v20.2.4).
const writeTagLen = 24

// alphanumeric is gen_rand_alphanumeric's table, a URL-safe base64 alphabet
// despite its name
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/random_string.cc#L48 at both tags).
const alphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// newWriteVersion is generate_new_write_ver: version 1 with a random tag, so
// that any other writer's version check of the object fails.
func newWriteVersion() meta.ObjVersion {
	return meta.ObjVersion{Ver: 1, Tag: "_" + randAlnum(writeTagLen-1)}
}

// objv is RGWObjVersionTracker
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L924,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L158-L197 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L948,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L166-L232 at v20.2.4):
// read is the version last read or written, which a later op checks when it is set,
// and write the version the next write sets, which it otherwise increments.
type objv struct{ read, write meta.ObjVersion }

func toCls(v meta.ObjVersion) version.ObjVersion   { return version.ObjVersion(v) }
func fromCls(v version.ObjVersion) meta.ObjVersion { return meta.ObjVersion(v) }

// prepareRead is prepare_op_for_read: a check of the read version when one
// is known, then a read of the stored one.
func (v *objv) prepareRead(rop *radosclient.ReadOp, r denc.Release) *version.ReadResult {
	if v.read.Ver != 0 {
		version.Check(rop, toCls(v.read), version.CondEQ, r)
	}
	return version.Read(rop, r)
}

// prepareWrite is prepare_op_for_write: a check of the read version when
// one is known, then a set of the write version when one is given and an
// increment otherwise.
func (v *objv) prepareWrite(wop *radosclient.WriteOp, r denc.Release) {
	if v.read.Ver != 0 {
		version.Check(wop, toCls(v.read), version.CondEQ, r)
	}
	if v.write.Ver != 0 {
		version.Set(wop, toCls(v.write), r)
	} else {
		version.Inc(wop, r)
	}
}

// applyWrite is apply_write: after a write prepareWrite composed, read is
// the version the object now holds, as far as the tracker can know it. An
// increment of an unchecked version leaves it unknown.
func (v *objv) applyWrite() {
	checked, incremented := v.read.Ver != 0, v.write.Ver == 0
	if checked && incremented {
		v.read.Ver++
	} else {
		v.read = v.write
	}
	v.write = meta.ObjVersion{}
}
