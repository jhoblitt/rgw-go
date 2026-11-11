package s3

import (
	"github.com/jhoblitt/rgw-go/internal/iso8601"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// requestLock is the retention and the legal hold a write's object-lock
// headers name, as get_params keeps them in obj_retention and
// obj_legal_hold, each nil when the request names none.
type requestLock struct {
	retention *meta.ObjectRetention
	legalHold *meta.ObjectLegalHold
}

// objectLockHeaders is get_params' read of the three object-lock headers
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2647-L2676 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2800-L2829 at v20.2.4;
// RGWCopyObj_ObjStore_S3::get_params
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3510-L3539 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3760-L3789 at v20.2.4;
// RGWInitMultipart_ObjStore_S3::get_params
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4025-L4050 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4476-L4501 at v20.2.4),
// which reads nothing the bucket holds. A mode with a retain-until date
// that iso8601.Parse reads as later than the clock's second, the mode
// GOVERNANCE or COMPLIANCE, is the retention, its date kept to the
// nanosecond; a legal hold of ON or OFF is the legal hold; a mode or a date
// alone, and each other value, is InvalidArgument, with the message a copy
// sets when isCopy is set. Each value is read as the C string get_params
// compares. A date past real_time's range is refused, where radosgw wraps
// it (docs/differences.md, "A date real_time cannot hold is refused").
func objectLockHeaders(r *op.Request, isCopy bool) (requestLock, error) {
	mode, hasMode := op.HeaderValue(r.Header, "X-Amz-Object-Lock-Mode")
	date, hasDate := op.HeaderValue(r.Header, "X-Amz-Object-Lock-Retain-Until-Date")
	hold, hasHold := op.HeaderValue(r.Header, "X-Amz-Object-Lock-Legal-Hold")
	invalid := func(msg string) error {
		if isCopy {
			return op.ErrInvalidArgument.WithMessage(msg)
		}
		return op.ErrInvalidArgument
	}
	var l requestLock
	switch {
	case hasMode && hasDate:
		until, ok := iso8601.Parse(rgwtext.CString(date))
		if !ok || until.Unix() <= r.Env.Clock().Unix() {
			return requestLock{}, invalid("invalid x-amz-object-lock-retain-until-date value")
		}
		if mode = rgwtext.CString(mode); mode != "GOVERNANCE" && mode != "COMPLIANCE" {
			return requestLock{}, invalid("invalid x-amz-object-lock-mode value")
		}
		l.retention = &meta.ObjectRetention{Mode: mode, RetainUntil: until}
	case hasMode || hasDate:
		return requestLock{}, invalid("need both x-amz-object-lock-mode and x-amz-object-lock-retain-until-date ")
	}
	if hasHold {
		if hold = rgwtext.CString(hold); hold != "ON" && hold != "OFF" {
			return requestLock{}, invalid("invalid x-amz-object-lock-legal-hold value")
		}
		l.legalHold = &meta.ObjectLegalHold{Status: hold}
	}
	return l, nil
}

// addAttrs adds to attrs, a head's, the retention and the legal hold l
// names, as RGWPutObj::execute adds them to an object's or a part's head
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4542-L4551 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4810-L4819 at v20.2.4)
// and RGWCopyObj::execute to a copy's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5587-L5596 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6150-L6159 at v20.2.4),
// which then keeps them in place of its source's. An attr the request's
// headers gave attrs never bears either name.
func (l requestLock) addAttrs(attrs map[string][]byte) {
	if l.retention != nil {
		attrs[meta.AttrObjectRetention] = objectlock.EncodeRetention(*l.retention)
	}
	if l.legalHold != nil {
		attrs[meta.AttrObjectLegalHold] = objectlock.EncodeLegalHold(*l.legalHold)
	}
}
