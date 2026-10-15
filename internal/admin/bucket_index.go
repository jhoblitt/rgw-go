package admin

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// newBucketIndexHandlers is the bucket index check route.
func newBucketIndexHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{"check_bucket_index": checkBucketIndex}
}

// checkBucketIndex is RGWOp_Check_Bucket_Index::execute
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_bucket.cc#L108-L126 at v19.2.6
// and v20.2.4): bucket, fix and check-objects. fix removes index entries and
// rewrites the shard headers, so a value get_bool cannot parse, which
// radosgw takes as false, and an empty one, which it takes as true, are
// refused after the cap check (docs/exclusions.md, "The admin bucket routes
// refuse a boolean argument they cannot parse"); check-objects is read as
// radosgw reads it. The body is check_index's, its flusher started
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1255-L1256 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1408-L1409 at v20.2.4).
func checkBucketIndex(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewCheckBucketIndex()
	o.Bucket, _ = a.String("bucket", "")
	fl := &flags{a: a}
	o.Fix = fl.destructive("fix")
	if v, _, err := a.Bool("check-objects", false); err == nil {
		o.CheckObjects = v
	}
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpBucketCheck(f, o) })
}

// dumpBucketCheck is check_index's document
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1258-L1276 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1411-L1438 at v20.2.4):
// every entry of both checks is a string named "object"
// (dump_mulipart_index_results,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L88-L94 at v19.2.6,
// and dump_bucket_index,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L291-L296),
// so the objects section repeats that key; then dump_index_check
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L312-L324).
func dumpBucketCheck(f formatter.Formatter, o *op.CheckBucketIndex) {
	f.OpenObjectSection("bucket_check")
	f.OpenArraySection("invalid_multipart_entries")
	for _, name := range o.InvalidMultipart {
		f.DumpString("object", name)
	}
	f.CloseSection()
	if o.CheckObjects {
		f.OpenObjectSection("objects")
		for _, name := range o.Objects {
			f.DumpString("object", name)
		}
		f.CloseSection()
	}
	f.OpenObjectSection("check_result")
	f.OpenObjectSection("existing_header")
	dumpBucketUsage(f, o.Existing)
	f.CloseSection()
	f.OpenObjectSection("calculated_header")
	dumpBucketUsage(f, o.Calculated)
	f.CloseSection()
	f.CloseSection()
	f.CloseSection()
}
