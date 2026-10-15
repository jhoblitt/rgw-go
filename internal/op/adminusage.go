package op

import (
	"context"
	"errors"
	"maps"
	"math"
	"slices"
	"syscall"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The usage API is RGWHandler_Usage's ops (rgw_rest_usage.cc, the same at
// v19.2.6 and v20.2.4) over RGWUsage (rgw_usage.cc, the same at both) and
// the RADOS driver's read_usage and trim_usage, which Env.UsageReader is.

// usageShowPage is the page RGWUsage::show asks read_usage for
// (rgw_usage.cc:39).
const usageShowPage = 1000

// errAccountUsage is RadosBucket::read_usage's and trim_usage's -ENOTSUP
// for a bucket an account owns
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L785-L806 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L803-L824 at v20.2.4),
// which set_req_state_err finds no row for and answers UnknownError
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L346-L353 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L359-L366 at v20.2.4).
var errAccountUsage = &Error{Code: "UnknownError", Status: 500, Errno: int(syscall.ENOTSUP)}

// UsageUser is one user section of the usage document's entries: the
// records of a run of one user's records in page order.
type UsageUser struct {
	User    string
	Buckets []UsageRecord
}

// UsageSummary is one user's summary_map entry, rgw_usage_log_entry
// aggregated under the category filter: only the categories it names, all
// when it names none, and s3select only when it names none or names
// s3select. Total is the sum of Categories.
type UsageSummary struct {
	User       string
	Categories map[string]UsageData
	Total      UsageData
	S3Select   S3SelectUsage
}

// usageScope is what RGWOp_Usage_Get and RGWOp_Usage_Delete hand RGWUsage:
// with a bucket, the bucket loaded by tenant and name, whose usage is its
// owner's under its name, load_bucket's ENOENT being NoSuchKey; else the
// user the uid parses to, which is every user when its string is empty.
func usageScope(ctx context.Context, env *Env, uid, tenant, bucket string) (user, name string, err error) {
	if bucket == "" {
		return meta.ParseUserID(uid).String(), "", nil
	}
	rec, err := env.Buckets.GetBucket(ctx, tenant, bucket)
	if errors.Is(err, ErrNoSuchBucket) {
		return "", "", ErrNoSuchKey
	}
	if err != nil {
		return "", "", err
	}
	if rec.Info.Owner.User == nil {
		return "", "", errAccountUsage
	}
	return rec.Info.Owner.User.String(), rec.Info.Bucket.Name, nil
}

// ShowUsage is RGWOp_Usage_Get with RGWUsage::show (rgw_rest_usage.cc:15-70,
// rgw_usage.cc:33-164). Cap usage=read (rgw_rest_usage.cc:20-22).
type ShowUsage struct {
	AdminOp
	// UID is the uid argument, in rgw_user's string form.
	UID    string
	Bucket string
	Tenant string
	// Start and End bound the epochs read, [Start, End); radosgw's
	// defaults are 0 and 2^64-1.
	Start, End  uint64
	ShowEntries bool
	ShowSummary bool
	// Categories filters the categories shown, all when it is empty.
	Categories map[string]bool
	// Entries are the records in the order the pages delivered them, a new
	// section whenever the user changes, so a user can have several.
	Entries []UsageUser
	// Summary is each user's, in user order.
	Summary []UsageSummary
}

// Name is get_usage.
func (o *ShowUsage) Name() string { return "get_usage" }

// VerifyPermission is RGWOp_Usage_Get::check_caps.
func (o *ShowUsage) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "usage", meta.CapRead)
}

// Execute pages the usage until a page is not truncated, grouping the
// entries and summing each user's summary as RGWUsage::show does.
func (o *ShowUsage) Execute(ctx context.Context, r *Request) error {
	user, bucket, err := usageScope(ctx, r.Env, o.UID, o.Tenant, o.Bucket)
	if err != nil {
		return err
	}
	o.Entries, o.Summary = nil, nil
	summary := map[string]*UsageSummary{}
	var it UsageIter
	for {
		// A listing of every user pages the whole usage log, which a client
		// that went away should not keep running.
		if err := ctx.Err(); err != nil {
			return err
		}
		recs, truncated, err := r.Env.UsageReader.ReadUsage(ctx, user, bucket, o.Start, o.End, usageShowPage, &it)
		if err != nil {
			return err
		}
		for i := range recs {
			rec := &recs[i]
			if o.ShowEntries {
				if n := len(o.Entries); n == 0 || o.Entries[n-1].User != rec.User {
					o.Entries = append(o.Entries, UsageUser{User: rec.User})
				}
				last := &o.Entries[len(o.Entries)-1]
				last.Buckets = append(last.Buckets, *rec)
			}
			s := summary[rec.User]
			if s == nil {
				s = &UsageSummary{User: rec.User}
				summary[rec.User] = s
			}
			s.aggregate(rec, o.Categories)
		}
		if !truncated {
			break
		}
	}
	for _, u := range slices.Sorted(maps.Keys(summary)) {
		o.Summary = append(o.Summary, *summary[u])
	}
	return nil
}

// aggregate is rgw_usage_log_entry::aggregate with a category filter
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L1005-L1023 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L1044-L1062 at v20.2.4),
// with sum's total over the categories it kept (:1025-1033, :1064-1072).
func (s *UsageSummary) aggregate(rec *UsageRecord, filter map[string]bool) {
	for cat, d := range rec.Categories {
		if _, ok := filter[cat]; len(filter) > 0 && !ok {
			continue
		}
		if s.Categories == nil {
			s.Categories = map[string]UsageData{}
		}
		s.Categories[cat] = addUsageData(s.Categories[cat], d)
		s.Total = addUsageData(s.Total, d)
	}
	if _, ok := filter["s3select"]; len(filter) == 0 || ok {
		s.S3Select.BytesProcessed += rec.S3Select.BytesProcessed
		s.S3Select.BytesReturned += rec.S3Select.BytesReturned
	}
}

// addUsageData is rgw_usage_data::aggregate.
func addUsageData(a, b UsageData) UsageData {
	return UsageData{
		BytesSent:     a.BytesSent + b.BytesSent,
		BytesReceived: a.BytesReceived + b.BytesReceived,
		Ops:           a.Ops + b.Ops,
		SuccessfulOps: a.SuccessfulOps + b.SuccessfulOps,
	}
}

// TrimUsage is RGWOp_Usage_Delete with RGWUsage::trim
// (rgw_rest_usage.cc:72-121, rgw_usage.cc:166-177). Cap usage=write
// (rgw_rest_usage.cc:77-79).
type TrimUsage struct {
	AdminOp
	UID, Bucket, Tenant string
	Start, End          uint64
	// RemoveAll is remove-all, which a trim of every user's whole usage
	// needs.
	RemoveAll bool
}

// Name is trim_usage.
func (o *TrimUsage) Name() string { return "trim_usage" }

// VerifyPermission is RGWOp_Usage_Delete::check_caps.
func (o *TrimUsage) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "usage", meta.CapWrite)
}

// Execute loads the bucket when one is named, refuses a trim of every
// user's whole usage without RemoveAll with InvalidArgument, a uid without
// an id naming no user, then trims.
func (o *TrimUsage) Execute(ctx context.Context, r *Request) error {
	user, bucket, err := usageScope(ctx, r.Env, o.UID, o.Tenant, o.Bucket)
	if err != nil {
		return err
	}
	if meta.ParseUserID(o.UID).ID == "" && o.Bucket == "" && o.Start == 0 && o.End == math.MaxUint64 && !o.RemoveAll {
		return ErrInvalidArgument
	}
	return r.Env.UsageReader.TrimUsage(ctx, user, bucket, o.Start, o.End)
}
