package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// LogUsage is radosgw's log_usage (rgw_log.cc:195-251, the same at v19.2.6
// and v20.2.4): it hands Env.Usage the usage entry of a finished request.
// The protocol handler calls it once per request, after it has written the
// response or the error document, when r.Status and the byte counters are
// final; no op calls it. radosgw logs at the same point, in
// process_request's tail once the response is complete, and so for requests
// refused at authentication, the bucket load or the permission check too
// (rgw_process.cc:454-462 at v19.2.6, :461-469 at v20.2.4). opName, the
// entry's category, is the Name of the op the request routes to, which
// radosgw builds before it authenticates (rgw_process.cc:325 and :345 at
// v19.2.6, :327 and :347 at v20.2.4), or "unknown" when no op serves the
// request (rgw_log.cc:556). Nothing is logged for a system identity or
// without a usage logger (rgw_log.cc:197-201), and the logger never receives
// an entry with neither a payer nor an owner, which radosgw's flush drops.
func LogUsage(ctx context.Context, r *Request, opName string) {
	if r.Identity.System || r.Env == nil || r.Env.Usage == nil {
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
	// until it is set: when the bucket loads (rgw_op.cc:552 at v19.2.6, :582 at
	// v20.2.4), except in CreateBucket, which skips that load
	// (rgw_rest.cc:1883-1891 at v19.2.6, :1886-1894 at v20.2.4) and sets it to
	// the owner it creates for once its checks pass (rgw_op.cc:3581 at
	// v19.2.6, :3809 at v20.2.4). r.BucketRec carries that owner either way.
	switch {
	case r.Bucket == "":
		e.Owner = r.Identity.Owner
	case r.BucketRec != nil:
		e.Owner = r.BucketRec.Info.Owner
		// A 403 is not charged to the requester (rgw_log.cc:210-219), and the
		// payer is s->user, the user itself even when it belongs to an account.
		if r.BucketRec.Info.RequesterPays && r.Status != 403 {
			e.Payer = meta.UserOwner(r.Identity.User.UserID)
		}
	}
	// Every 404 takes the invalid bucket name "-", a missing key's too, though
	// radosgw's comment speaks only of a missing bucket (rgw_log.cc:222-225).
	if r.Status == 404 {
		e.Bucket = "-"
	}
	// rgw_err::is_err calls only a status outside 200-399 an error
	// (rgw_common.cc:203-207 at v19.2.6 and v20.2.4).
	if 200 <= r.Status && r.Status <= 399 {
		e.SuccessfulOps = 1
	}
	// radosgw's flush drops an entry whose user, the payer when there is one
	// and else the owner, is empty (rgw_rados.cc:1645-1648 at v19.2.6,
	// :1748-1751 at v20.2.4), as it is when the bucket owner was never set or
	// authentication refused the request.
	if e.Payer.String() == "" && e.Owner.String() == "" {
		return
	}
	r.Env.Usage.Log(ctx, e)
}
