package admin

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// newUsageHandlers are RGWHandler_Usage's routes (rgw_rest_usage.cc:123-131
// at v19.2.6 and v20.2.4).
func newUsageHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"get_usage":  getUsage,
		"trim_usage": trimUsage,
	}
}

// usageArgs are the arguments both usage ops read with RESTArgs::get_string
// (rgw_rest_usage.cc:38-40 and :91-93).
func usageArgs(a Args) (uid, bucket, tenant string) {
	uid, _ = a.String("uid", "")
	bucket, _ = a.String("bucket", "")
	tenant, _ = a.String("tenant", "")
	return uid, bucket, tenant
}

// getUsage is RGWOp_Usage_Get::execute's argument handling
// (rgw_rest_usage.cc:28-70), whose get_epoch and get_bool returns go
// unchecked: a boolean get_bool cannot parse is its default, and so, in
// rgw-go, is a date get_epoch cannot parse (docs/exclusions.md, "The admin
// usage routes give a date they cannot parse a value of their own"). The
// categories split on get_str_list's delimiters.
func getUsage(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := &op.ShowUsage{}
	o.UID, o.Bucket, o.Tenant = usageArgs(a)
	o.Start = epochArg(a, "start", 0)
	o.End = epochArg(a, "end", math.MaxUint64)
	o.ShowEntries = boolArg(a, "show-entries", true)
	o.ShowSummary = boolArg(a, "show-summary", true)
	if cats, _ := a.String("categories", ""); cats != "" {
		o.Categories = map[string]bool{}
		for _, c := range strings.FieldsFunc(cats, func(r rune) bool { return strings.ContainsRune(";,= \t", r) }) {
			o.Categories[c] = true
		}
	}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpUsage(f, o) })
}

// epochArg is RESTArgs::get_epoch as the usage show calls it, unchecked,
// with a date it cannot parse read as def.
func epochArg(a Args, name string, def uint64) uint64 {
	v, _, err := a.Epoch(name, def)
	if err != nil {
		return def
	}
	return v
}

// trimUsage is RGWOp_Usage_Delete::execute's argument handling
// (rgw_rest_usage.cc:85-121) and its empty 200. A date get_epoch cannot
// parse is InvalidArgument, after the cap check (docs/exclusions.md); the
// unchecked get_bool reads remove-all as false when it cannot parse it.
func trimUsage(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := &op.TrimUsage{}
	o.UID, o.Bucket, o.Tenant = usageArgs(a)
	var argErr error
	for _, d := range []struct {
		name string
		def  uint64
		v    *uint64
	}{{"start", 0, &o.Start}, {"end", math.MaxUint64, &o.End}} {
		var err error
		if *d.v, _, err = a.Epoch(d.name, d.def); err != nil && argErr == nil {
			argErr = fmt.Errorf("%w: %s is not a date", err, d.name)
		}
	}
	o.RemoveAll = boolArg(a, "remove-all", false)
	if err := runChecked(ctx, o, r, argErr); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// dumpUsage is RGWUsage::show's document (rgw_usage.cc:50-160 at v19.2.6
// and v20.2.4), which it writes after starting the flusher (:48): the
// entries when shown, the summary when shown. A bucket's time is
// utime_t(epoch, 0)'s gmtime and its epoch dump_int's; its s3select
// counters, and the summary's categories and total, follow the category
// filter.
func dumpUsage(f formatter.Formatter, o *op.ShowUsage) {
	f.OpenObjectSection("usage")
	if o.ShowEntries {
		f.OpenArraySection("entries")
		for i, u := range o.Entries {
			// show opens a user's section when the user differs from the
			// last, which starts as "", so records filed under no user at the
			// listing's start go into entries bare (rgw_usage.cc:54-55 and
			// :86-96).
			bare := i == 0 && u.User == ""
			if !bare {
				f.OpenObjectSection("user")
				f.DumpString("user", u.User)
				f.OpenArraySection("buckets")
			}
			for j := range u.Buckets {
				b := &u.Buckets[j]
				epoch := int64(b.Epoch) //nolint:gosec // dump_int writes the epoch signed (:101)
				f.OpenObjectSection("bucket")
				f.DumpString("bucket", b.Bucket)
				f.DumpStream("time", meta.Time{Time: time.Unix(epoch, 0)}.Gmtime())
				f.DumpInt("epoch", epoch)
				f.DumpString("owner", b.Owner)
				if b.Payer != "" && b.Payer != b.Owner {
					f.DumpString("payer", b.Payer)
				}
				dumpUsageCategories(f, b.Categories, o.Categories)
				f.OpenObjectSection("s3select")
				if _, ok := o.Categories["s3select"]; len(o.Categories) == 0 || ok {
					f.DumpUnsigned("bytes_processed", b.S3Select.BytesProcessed)
					f.DumpUnsigned("bytes_returned", b.S3Select.BytesReturned)
				}
				f.CloseSection()
				f.CloseSection()
			}
			if !bare {
				f.CloseSection()
				f.CloseSection()
			}
		}
		f.CloseSection()
	}
	if o.ShowSummary {
		f.OpenArraySection("summary")
		for _, s := range o.Summary {
			f.OpenObjectSection("user")
			f.DumpString("user", s.User)
			dumpUsageCategories(f, s.Categories, o.Categories)
			f.OpenObjectSection("total")
			f.DumpUnsigned("bytes_sent", s.Total.BytesSent)
			f.DumpUnsigned("bytes_received", s.Total.BytesReceived)
			f.DumpUnsigned("ops", s.Total.Ops)
			f.DumpUnsigned("successful_ops", s.Total.SuccessfulOps)
			f.DumpUnsigned("bytes_processed", s.S3Select.BytesProcessed)
			f.DumpUnsigned("bytes_returned", s.S3Select.BytesReturned)
			f.CloseSection()
			f.CloseSection()
		}
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpUsageCategories is dump_usage_categories_info (rgw_usage.cc:14-31):
// the categories in name order, leaving out those a non-empty filter does
// not name.
func dumpUsageCategories(f formatter.Formatter, usage map[string]op.UsageData, filter map[string]bool) {
	f.OpenArraySection("categories")
	for _, name := range slices.Sorted(maps.Keys(usage)) {
		if _, ok := filter[name]; len(filter) > 0 && !ok {
			continue
		}
		d := usage[name]
		f.OpenObjectSection("entry")
		f.DumpString("category", name)
		f.DumpUnsigned("bytes_sent", d.BytesSent)
		f.DumpUnsigned("bytes_received", d.BytesReceived)
		f.DumpUnsigned("ops", d.Ops)
		f.DumpUnsigned("successful_ops", d.SuccessfulOps)
		f.CloseSection()
	}
	f.CloseSection()
}
