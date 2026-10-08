package admin

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// The bucket routes are driver/rados/rgw_rest_bucket.cc's RGWOp_* execute
// bodies, whose line numbers are the same at v19.2.6 and v20.2.4. A bare
// rgw_bucket.cc line is v19.2.6's.

// newBucketHandlers are the bucket routes but the index check.
func newBucketHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"get_bucket_info":  getBucketInfo,
		"link_bucket":      linkBucket,
		"unlink_bucket":    unlinkBucket,
		"remove_bucket":    removeBucket,
		"set_bucket_quota": setBucketQuota,
		"get_policy":       getBucketPolicy,
		"remove_object":    removeObject,
	}
}

// destructive is flags.get for a flag that deletes: an empty value, which
// RESTArgs::get_bool reads as true, is refused with the unparsable ones.
func (f *flags) destructive(name string) bool {
	if v, ok := f.a.Get(name); ok && v == "" && f.err == nil {
		f.err = op.ErrInvalidArgument
	}
	return f.get(name, false)
}

// getBucketInfo is RGWOp_Bucket_Info::execute (rgw_rest_bucket.cc:33-62):
// uid, bucket, stats, max-entries and marker, the readers unchecked as
// radosgw's are, so a value they refuse is the default. The body is
// RGWBucketAdminOp::info's, its flusher started (rgw_bucket.cc:1649-1650).
func getBucketInfo(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewBucketInfo()
	o.UID = uidArg(a)
	o.UIDGiven = o.UID.ID != ""
	o.Bucket, _ = a.String("bucket", "")
	o.Stats = boolArg(a, "stats", false)
	if n, _, err := a.Uint32("max-entries", 0); err == nil {
		o.MaxEntries = n
	}
	o.Marker, _ = a.String("marker", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpBucketInfo(f, o, rel) })
}

// dumpBucketInfo is info's document: one bucket's stats; or a "buckets"
// array of stats or of "bucket" names, inside a "result" section with
// truncated, count and, when truncated, marker for a user's listing given
// max-entries (list_owner_bucket_info, rgw_bucket.cc:1582-1626).
func dumpBucketInfo(f formatter.Formatter, o *op.BucketInfo, rel denc.Release) {
	if o.Single != nil {
		dumpBucketStats(f, *o.Single, rel)
		return
	}
	if o.Paged {
		f.OpenObjectSection("result")
	}
	f.OpenArraySection("buckets")
	for i := range o.Entries {
		dumpBucketStats(f, o.Entries[i], rel)
	}
	for _, name := range o.Names {
		f.DumpString("bucket", name)
	}
	f.CloseSection()
	if o.Paged {
		f.DumpBool("truncated", o.Truncated)
		f.DumpUnsigned("count", o.Count)
		if o.Truncated {
			f.DumpString("marker", o.NextMarker)
		}
		f.CloseSection()
	}
}

// bucketCategories is RGWObjCategory's enum order under to_string's names
// (cls_rgw_types.cc:133-143 at v19.2.6 and v20.2.4), the order the stats
// std::map iterates in.
var bucketCategories = []string{"rgw.none", "rgw.main", "rgw.shadow", "rgw.multimeta", "rgw.cloudtiered", "unknown"}

// dumpBucketUsage is dump_bucket_usage (rgw_bucket.cc:298-310; v20.2.4
// :299-311): one section per category present.
func dumpBucketUsage(f formatter.Formatter, cats map[string]op.CategoryStats) {
	f.OpenObjectSection("usage")
	for _, name := range bucketCategories {
		if s, ok := cats[name]; ok {
			f.OpenObjectSection(name)
			dumpCategoryStats(f, s)
			f.CloseSection()
		}
	}
	f.CloseSection()
}

// dumpCategoryStats is RGWStorageStats::dump with dump_utilized true
// (rgw_common.cc:3102-3115 at v19.2.6, :3164-3177 at v20.2.4).
func dumpCategoryStats(f formatter.Formatter, s op.CategoryStats) {
	f.DumpUnsigned("size", s.Size)
	f.DumpUnsigned("size_actual", s.SizeRounded)
	f.DumpUnsigned("size_utilized", s.SizeUtilized)
	f.DumpUnsigned("size_kb", roundedKB(s.Size))
	f.DumpUnsigned("size_kb_actual", roundedKB(s.SizeRounded))
	f.DumpUnsigned("size_kb_utilized", roundedKB(s.SizeUtilized))
	f.DumpUnsigned("num_objects", s.NumObjects)
}

// dumpBucketStats is bucket_stats' "stats" section (rgw_bucket.cc:1351-1436;
// v20.2.4 :1514-1602, which adds reshard_status, judge_reshard_lock_time
// and read_tracker).
func dumpBucketStats(f formatter.Formatter, d op.BucketStatsData, rel denc.Release) {
	info := d.Rec.Info
	current := info.Layout.Current
	f.OpenObjectSection("stats")
	f.DumpString("bucket", info.Bucket.Name)
	f.DumpString("tenant", info.Bucket.Tenant)
	versioning := "off"
	if info.Flags&meta.BucketVersioned != 0 {
		versioning = "enabled"
		if info.Flags&meta.BucketVersionsSuspended != 0 {
			versioning = "suspended"
		}
	}
	f.DumpString("versioning", versioning)
	f.DumpString("zonegroup", info.Zonegroup)
	f.DumpString("placement_rule", info.PlacementRule.String())
	f.OpenObjectSection("explicit_placement")
	info.Bucket.ExplicitPlacement.Dump(f, rel)
	f.CloseSection()
	f.DumpString("id", info.Bucket.ID)
	f.DumpString("marker", info.Bucket.Marker)
	f.DumpStream("index_type", current.Layout.Type.String())
	f.DumpInt("index_generation", int64(current.Gen)) //nolint:gosec // dump_int of the u64 generation
	if rel >= denc.Tentacle {
		f.DumpString("reshard_status", info.Layout.Resharding.String())
		f.DumpStream("judge_reshard_lock_time", info.Layout.JudgeReshardLockTime.Gmtime())
	}
	f.DumpBool("object_lock_enabled", info.Flags&meta.BucketObjLockEnabled != 0)
	f.DumpBool("mfa_enabled", info.Flags&meta.BucketMFAEnabled != 0)
	f.DumpString("owner", info.Owner.String())
	if d.HasIndex {
		f.DumpInt("num_shards", int64(current.Layout.Normal.NumShards))
		f.DumpString("ver", d.Index.Ver)
		f.DumpString("master_ver", d.Index.MasterVer)
		f.DumpString("max_marker", d.Index.MaxMarker)
		dumpBucketUsage(f, d.Index.Categories)
	}
	f.DumpStream("mtime", meta.Time{Time: d.Rec.Mtime}.Gmtime())
	f.DumpStream("creation_time", info.CreationTime.Gmtime())
	f.OpenObjectSection("bucket_quota")
	info.Quota.Dump(f, rel)
	f.CloseSection()
	if d.Tags != nil {
		// RGWObjTags::dump (rgw_tag.cc:59-66): each tag's key is its field
		// name, in multimap order.
		f.OpenObjectSection("tagset")
		for _, t := range d.Tags.Tags {
			f.DumpString(t.Key, t.Value)
		}
		f.CloseSection()
	}
	if rel >= denc.Tentacle {
		f.DumpInt("read_tracker", int64(d.Rec.Version.Ver)) //nolint:gosec // dump_int of objv_tracker.read_version.ver
	}
	f.CloseSection()
}

// linkBucket is RGWOp_Bucket_Link::execute (rgw_rest_bucket.cc:142-170),
// which sends no body. Forwarding to the metadata master is multisite's.
func linkBucket(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewLinkBucket()
	o.UID = uidArg(a)
	o.AccountID, _ = a.String("account-id", "")
	o.Bucket, _ = a.String("bucket", "")
	o.BucketID, _ = a.String("bucket-id", "")
	o.NewBucketName, _ = a.String("new-bucket-name", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// unlinkBucket is RGWOp_Bucket_Unlink::execute (rgw_rest_bucket.cc:186-209),
// which sends no body.
func unlinkBucket(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewUnlinkBucket()
	o.UID = uidArg(a)
	o.AccountID, _ = a.String("account-id", "")
	o.Bucket, _ = a.String("bucket", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// removeBucket is RGWOp_Bucket_Remove::execute (rgw_rest_bucket.cc:225-248),
// which sends no body. purge-objects and bypass-gc delete, so a value
// get_bool cannot parse, which radosgw takes as false, and an empty one,
// which it takes as true, are refused after the cap check
// (docs/exclusions.md, "The admin bucket routes refuse a boolean argument
// they cannot parse").
func removeBucket(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewRemoveBucketAdmin()
	o.Bucket, _ = a.String("bucket", "")
	o.Tenant, _ = a.String("tenant", "")
	fl := &flags{a: a}
	o.PurgeObjects = fl.destructive("purge-objects")
	o.BypassGC = fl.destructive("bypass-gc")
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// setBucketQuota is RGWOp_Set_Bucket_Quota::execute
// (rgw_rest_bucket.cc:266-328), which sends no body. The quota arguments
// are read whether or not the request has a body, and one that does not
// parse is refused after the cap check, as on the user quota route.
func setBucketQuota(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewSetBucketQuota()
	var uid string
	uid, o.UIDGiven = a.String("uid", "")
	o.UID = meta.ParseUserID(uid)
	o.Bucket, o.BucketGiven = a.String("bucket", "")
	fl := &flags{a: a}
	o.Params.MaxObjects = quotaInt(a, fl, "max-objects")
	o.Params.MaxSize = quotaInt(a, fl, "max-size")
	o.Params.MaxSizeKB = quotaInt(a, fl, "max-size-kb")
	o.Params.Enabled = fl.ptr("enabled")
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// getBucketPolicy is RGWOp_Get_Policy::execute (rgw_rest_bucket.cc:78-92):
// the policy in a "policy" section, its flusher started
// (rgw_bucket.cc:972-978).
func getBucketPolicy(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewGetBucketPolicyAdmin()
	o.Bucket, _ = q.Args.String("bucket", "")
	o.Object, _ = q.Args.String("object", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteBody(w, r, q, func(f formatter.Formatter) {
		f.OpenObjectSection("policy")
		o.Result.Dump(f, rel)
		f.CloseSection()
	})
}

// removeObject is RGWOp_Object_Remove::execute (rgw_rest_bucket.cc:376-390),
// which sends no body.
func removeObject(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewRemoveObjectAdmin()
	o.Bucket, _ = q.Args.String("bucket", "")
	o.Object, _ = q.Args.String("object", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}
