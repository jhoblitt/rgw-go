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
// (rgw_rest_bucket.cc:108-126 at v19.2.6 and v20.2.4): bucket, fix and
// check-objects. fix removes index entries and rewrites the shard headers,
// so a value get_bool cannot parse, which radosgw takes as false, and an
// empty one, which it takes as true, are refused after the cap check
// (docs/exclusions.md, "The admin bucket routes refuse a boolean argument
// they cannot parse"); check-objects is read as radosgw reads it. The body
// is check_index's, its flusher started (rgw_bucket.cc:1255-1256 at v19.2.6,
// :1408-1409 at v20.2.4).
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

// dumpBucketCheck is check_index's document (rgw_bucket.cc:1258-1276 at
// v19.2.6, :1411-1438 at v20.2.4): every entry of both checks is a string
// named "object" (dump_mulipart_index_results, :88-94 at v19.2.6, and
// dump_bucket_index, :291-296), so the objects section repeats that key;
// then dump_index_check (:312-324).
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
