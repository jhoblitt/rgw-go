package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// LogUsage is radosgw's log_usage
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L195-L251,
// the same at v19.2.6 and v20.2.4): it hands Env.Usage the usage entry of a
// finished request. The protocol handler calls it once per request, after it
// has written the response or the error document, when r.Status and the byte
// counters are final; no op calls it. radosgw logs at the same point, in
// process_request's tail once the response is complete, and so for requests
// refused at authentication, the bucket load or the permission check too
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L454-L462 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L461-L469 at v20.2.4).
// opName, the entry's category, is the Name of the op the request routes to,
// which radosgw builds before it authenticates
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L325 and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L345 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L347 at v20.2.4),
// or "unknown" when no op serves the request
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L556).
// Nothing is logged for a system identity or without a usage logger
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L197-L201),
// and the logger never receives an entry with neither a payer nor an owner,
// which radosgw's flush drops.
func LogUsage(ctx context.Context, r *Request, opName string) {
	id := r.Identity
	// An unverified identity proves only that its sender knows a key's id, so
	// its usage is accounted as an anonymous request's: nothing is filed
	// under the key's user or account, or charged to that user as payer, and
	// a system user's is not left out. radosgw takes s->owner, and s->user as
	// payer, from it like any other
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_log.cc#L197-L219 at v19.2.7
	// and v20.2.4; docs/exclusions.md).
	if id.Unverified {
		id = Anonymous()
	}
	if id.System || r.Env == nil || r.Env.Usage == nil {
		return
	}
	e := UsageEntry{
		Bucket:        r.Bucket,
		Time:          r.Env.Clock(),
		Category:      opName,
		BytesSent:     uint64(max(r.BytesOut, 0)),
		BytesReceived: uint64(max(r.BytesIn, 0)),
		Ops:           1,
	}
	// radosgw files a bucket request under s->bucket_owner, which stays empty
	// until it is set: when the bucket loads
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L552 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L582 at v20.2.4),
	// except in CreateBucket, which skips that load
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1883-L1891 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1886-L1894 at v20.2.4)
	// and sets it to the owner it creates for once its checks pass
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3581 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3809 at v20.2.4).
	// r.BucketRec carries that owner either way.
	switch {
	case r.Bucket == "":
		e.Owner = id.Owner
	case r.BucketRec != nil:
		e.Owner = r.BucketRec.Info.Owner
		// A 403 is not charged to the requester
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L210-L219),
		// and the payer is s->user, the user itself even when it belongs to an
		// account.
		if r.BucketRec.Info.RequesterPays && r.Status != 403 {
			e.Payer = meta.UserOwner(id.User.UserID)
		}
	}
	// Every 404 takes the invalid bucket name "-", a missing key's too, though
	// radosgw's comment speaks only of a missing bucket
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L222-L225).
	if r.Status == 404 {
		e.Bucket = "-"
	}
	// rgw_err::is_err calls only a status outside 200-399 an error
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L203-L207 at v19.2.6
	// and v20.2.4).
	if 200 <= r.Status && r.Status <= 399 {
		e.SuccessfulOps = 1
	}
	// radosgw's flush drops an entry whose user, the payer when there is one
	// and else the owner, is empty
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1645-L1648 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1748-L1751 at v20.2.4),
	// as it is when the bucket owner was never set or authentication refused
	// the request.
	if e.Payer.String() == "" && e.Owner.String() == "" {
		return
	}
	r.Env.Usage.Log(ctx, e)
}
