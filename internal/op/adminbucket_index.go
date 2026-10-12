package op

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The index check is RGWOp_Check_Bucket_Index (rgw_rest_bucket.cc:94-126)
// over RGWBucketAdminOp::check_index (driver/rados/rgw_bucket.cc:1241-1280
// at v19.2.6, :1392-1442 at v20.2.4) and the RGWBucket checks it runs.

// indexCheckPage is listing_max_entries (rgw_bucket.cc:46 at v20.2.4, the
// same at v19.2.6): the page the checks list in and the batch the multipart
// check removes in.
const indexCheckPage = 1000

// CheckBucketIndex is RGWOp_Check_Bucket_Index and
// RGWBucketAdminOp::check_index. Cap buckets=write.
type CheckBucketIndex struct {
	AdminOp
	// Bucket may be "tenant/name".
	Bucket       string
	Fix          bool
	CheckObjects bool

	// InvalidMultipart are the multipart entries whose upload has no meta
	// entry, by their raw index key names, "_multipart_<name>".
	InvalidMultipart []string
	// Objects are the names the object check lists, set only with
	// CheckObjects.
	Objects    []string
	Existing   map[string]CategoryStats
	Calculated map[string]CategoryStats
}

// NewCheckBucketIndex returns a CheckBucketIndex.
func NewCheckBucketIndex() *CheckBucketIndex { return &CheckBucketIndex{} }

// Name is check_bucket_index.
func (o *CheckBucketIndex) Name() string { return "check_bucket_index" }

// VerifyPermission is RGWOp_Check_Bucket_Index::check_caps, buckets=write
// (rgw_rest_bucket.cc:99-101 at v19.2.6 and v20.2.4).
func (o *CheckBucketIndex) VerifyPermission(_ context.Context, r *Request) error {
	return bucketsCap(r, meta.CapWrite)
}

// Execute is check_index: RGWBucket::init by the bucket alone, then the
// multipart check, the object check when CheckObjects, and the header check,
// in radosgw's order. A failure after the bucket loads answers its error,
// where radosgw, its body started, answers 200 with the body cut short
// (docs/exclusions.md, "Admin responses carry a Content-Type on every
// body"). check-objects without fix is check_object_index's EINVAL, which
// radosgw reaches after a multipart check that, without fix, removes
// nothing.
func (o *CheckBucketIndex) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	o.InvalidMultipart, o.Objects, o.Existing, o.Calculated = []string{}, nil, nil, nil
	rec, err := loadAdminBucket(ctx, env, meta.UserID{}, o.Bucket)
	if err != nil {
		return err
	}
	if o.CheckObjects && !o.Fix {
		return ErrInvalidArgument
	}
	invalid, err := o.badMultipart(ctx, env, rec)
	if err != nil {
		return err
	}
	for _, k := range invalid {
		o.InvalidMultipart = append(o.InvalidMultipart, k.IndexKeyName())
	}
	if o.Fix && len(invalid) > 0 {
		if rerr := removeOrphanParts(ctx, env, rec, invalid); rerr != nil {
			return rerr
		}
	}
	if o.CheckObjects {
		if o.Objects, err = listObjectNames(ctx, env, rec); err != nil {
			return err
		}
	}
	if o.Existing, o.Calculated, err = env.BucketAdmin.CheckIndex(ctx, rec); err != nil {
		return err
	}
	if o.Fix {
		return env.BucketAdmin.RebuildIndex(ctx, rec)
	}
	return nil
}

// eachPage lists rec's bucket in the namespace ns from its start, a page of
// indexCheckPage at a time, handing each page's entries to fn. A listing
// that answers truncated without moving its marker is an InternalError.
func eachPage(ctx context.Context, env *Env, rec *BucketRecord, ns string, versions bool, fn func([]ObjectEntry)) error {
	marker := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := env.Buckets.ListObjects(ctx, rec, ListObjectsParams{NS: ns, ListVersions: versions, Marker: marker, MaxKeys: indexCheckPage})
		if err != nil {
			return err
		}
		fn(res.Entries)
		if !res.Truncated {
			return nil
		}
		if res.NextMarker == marker {
			return fmt.Errorf("%w: the listing of bucket %s made no progress past %q", ErrInternalError, rec.Info.Bucket.Name, marker)
		}
		marker = res.NextMarker
	}
}

// badMultipart is check_bad_index_multipart: the multipart namespace listed
// with versions, and the part entries whose upload has no meta entry, by
// each release's rule.
func (o *CheckBucketIndex) badMultipart(ctx context.Context, env *Env, rec *BucketRecord) ([]meta.ObjKey, error) {
	layout := rec.Info.Layout.Current.Layout
	tentacle := env.Zone.Release() >= denc.Tentacle
	if tentacle && layout.Type != meta.IndexNormal {
		return nil, ErrInvalidArgument
	}
	var keys []meta.ObjKey
	err := eachPage(ctx, env, rec, meta.NSMultipart, true, func(ents []ObjectEntry) {
		for i := range ents {
			keys = append(keys, ents[i].Key)
		}
	})
	if err != nil {
		return nil, err
	}
	if tentacle {
		return badMultipartByShard(keys, layout.Normal.NumShards), nil
	}
	return badMultipartSquid(keys), nil
}

// badMultipartSquid is Squid's check (rgw_bucket.cc:326-420 at v19.2.6):
// each entry's head object name split at its last '.'; a "meta" suffix
// marks an upload, and every other entry is a part of the upload its prefix
// names, the whole name when it has no '.'. The parts of uploads no meta
// entry marks are reported in index key order, all_objs being a std::map,
// which is the listing's order.
func badMultipartSquid(keys []meta.ObjKey) []meta.ObjKey {
	metas := map[string]bool{}
	var parts []meta.ObjKey
	prefixes := map[meta.ObjKey]string{}
	for _, k := range keys {
		oid := k.OID()
		prefix, suffix, found := strings.CutLast(oid, ".")
		if !found {
			parts, prefixes[k] = append(parts, k), oid
			continue
		}
		if suffix == "meta" {
			metas[prefix] = true
			continue
		}
		parts, prefixes[k] = append(parts, k), prefix
	}
	var out []meta.ObjKey
	for _, k := range parts {
		if !metas[prefixes[k]] {
			out = append(out, k)
		}
	}
	return out
}

// badMultipartByShard is Tentacle's check (rgw_bucket.cc:343-482 and
// :497-557 at v20.2.4), which reads each shard's multipart entries in index
// order, the REST op's max_aio of 0 giving one coroutine that takes the
// shards in turn. The listing's order is index order, and each entry
// belongs to the shard of its object
// (RGWMPObj::from_meta's key); on a shard, a run of part entries sharing the
// prefix before their last '.' is reported unless the next entry is that
// prefix's meta entry, and an entry without a '.' is skipped.
func badMultipartByShard(keys []meta.ObjKey, numShards uint32) []meta.ObjKey {
	shards := map[uint32][]meta.ObjKey{}
	for _, k := range keys {
		key, _, _ := meta.ParseMultipartMeta(k.Name)
		shard, _ := meta.IndexShard(key, numShards)
		shards[shard] = append(shards[shard], k)
	}
	var out []meta.ObjKey
	for shard := range max(numShards, 1) {
		ents := shards[shard]
		var window []meta.ObjKey
		prev := ""
		for _, k := range ents {
			pos := strings.LastIndexByte(k.Name, '.')
			if pos < 0 {
				continue
			}
			prefix := k.Name[:pos]
			if k.Name[pos+1:] == "meta" {
				if prefix != prev {
					out = append(out, window...)
				}
				window, prev = nil, ""
				continue
			}
			if prefix != prev {
				out = append(out, window...)
				window, prev = nil, prefix
			}
			window = append(window, k)
		}
		out = append(out, window...)
	}
	return out
}

// removeOrphanParts is the multipart check's fix: the reported entries
// removed through BucketAdmin.RemoveIndexEntries, indexCheckPage at a time,
// but for those of an object that has a live upload. radosgw removes every
// entry it reports, and it reports the head of a part sent again, whose
// prefix is random, so its fix drops a live upload's part from the index
// until the upload ends (docs/ceph-upstream-bugs.md, "radosgw-admin bucket
// check reports a live re-uploaded part as an invalid multipart entry"). An
// object has a live upload when a listing of the multipart namespace made
// after the check's own finds a meta entry of it: an upload's meta entry is
// written before its parts' and stays until the upload ends, so that
// listing finds the meta entry of every upload one of whose entries the
// check listed and that has not ended since (docs/exclusions.md, "The admin
// index check's fix keeps the entries of an object with an upload in
// flight").
func removeOrphanParts(ctx context.Context, env *Env, rec *BucketRecord, invalid []meta.ObjKey) error {
	live := map[string]bool{}
	err := eachPage(ctx, env, rec, meta.NSMultipart, true, func(ents []ObjectEntry) {
		for i := range ents {
			name := ents[i].Key.Name
			if !strings.HasSuffix(name, meta.MultipartMetaSuffix) {
				continue
			}
			if key, _, ok := meta.ParseMultipartMeta(name); ok && key != "" {
				live[key] = true
			}
		}
	})
	if err != nil {
		return err
	}
	var batch []meta.ObjKey
	for _, k := range invalid {
		if key, _, _ := meta.ParseMultipartMeta(k.Name); key != "" && live[key] {
			continue
		}
		batch = append(batch, k)
		if len(batch) == indexCheckPage {
			if err := env.BucketAdmin.RemoveIndexEntries(ctx, rec, batch); err != nil {
				return err
			}
			batch = nil
		}
	}
	if len(batch) == 0 {
		return nil
	}
	return env.BucketAdmin.RemoveIndexEntries(ctx, rec, batch)
}

// listObjectNames is check_object_index's listing (rgw_bucket.cc:422-468 at
// v19.2.6, :559-605 at v20.2.4): the plain namespace without versions, a
// page at a time, each entry's name. A listing radosgw meets as -ENOENT ends
// it. radosgw lists with a filter that reads the head of every entry and
// repairs the index from it, which no rgw-go listing does
// (docs/exclusions.md, "The admin index check does not re-read every
// object's head"). After any other listing failure radosgw lists the same
// page again, its marker and truncated flag left as they were
// (rgw_sal_rados.cc:897-901 at v19.2.6, :917-918 at v20.2.4), so a failure
// that persists holds the request for good; rgw-go answers the failure.
func listObjectNames(ctx context.Context, env *Env, rec *BucketRecord) ([]string, error) {
	names := []string{}
	err := eachPage(ctx, env, rec, "", false, func(ents []ObjectEntry) {
		for i := range ents {
			names = append(names, ents[i].Key.Name)
		}
	})
	if errors.Is(err, ErrNoSuchKey) {
		return names, nil
	}
	return names, err
}
