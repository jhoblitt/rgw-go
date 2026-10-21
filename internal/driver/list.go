package driver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The bucket listing is RGWRados::Bucket::List and the cls_bucket_list
// helpers under it (driver/rados/rgw_rados.cc). v20.2.4 carries the
// same code as the v19.2.6 rgw_rados.cc links below cite.

//go:generate go tool counterfeiter -generate

//counterfeiter:generate . HeadStater

// HeadStater is the one object-store method the listing's disk check needs:
// the head of the key it names as get_obj_state reads it for
// check_disk_state, without following an OLH
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10552 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11280 at v20.2.4),
// so the null version's is the OLH head itself.
type HeadStater interface {
	StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error)
}

const (
	// listAbsoluteMax is bucket_list_objects_absolute_max
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.h#L1018 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.h#L1068 at v20.2.4),
	// the most entries one page returns.
	listAbsoluteMax = 25000
	// listSoftAttempts is SOFT_MAX_ATTEMPTS
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1859).
	listSoftAttempts = 8
	// listUnorderedReadAhead is list_objects_unordered's max_read_ahead
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2201).
	listUnorderedReadAhead = 100
	// suggestTimeout bounds a listing's background index suggestion, which
	// radosgw sends with aio_operate and never waits for.
	suggestTimeout = 30 * time.Second
	// pastVersions, after a name, is a start key past every list key of the
	// name, the name, NUL "v" and more
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L302-L315 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L352-L365 at v20.2.4),
	// and before the name and 0x01, cls_rgw_after_versions's key
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L319-L323 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L369-L373 at v20.2.4),
	// which is also the plain key of an object named so.
	pastVersions = "\x00w"
)

// ListObjects implements op.BucketStore: RGWRados::Bucket::List::list_objects,
// which lists ordered or, with AllowUnordered, unordered. An unordered
// listing with a delimiter is the -EINVAL RGWListBucket::execute answers
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3085-L3090 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3326-L3331 at v20.2.4).
func (s *Store) ListObjects(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	if p.AllowUnordered {
		if p.Delimiter != "" {
			return op.ListObjectsResult{}, fmt.Errorf("%w: unordered bucket listing requested with a delimiter", op.ErrInvalidArgument)
		}
		return s.listObjectsUnordered(ctx, rec, p)
	}
	return s.listObjectsOrdered(ctx, rec, p)
}

// listObjectsOrdered is list_objects_ordered
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1815-L2172 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1905-L2262 at v20.2.4).
// The marker and its instance, and the prefix, become index keys in p.NS;
// with a delimiter a marker inside a common prefix moves past it, its
// instance dropped, and otherwise the listing starts where markerStart puts
// it. Each round asks listOrdered for what the page still lacks, with the
// round as the expansion factor, from where the last round's last entry
// visited resumes, and walks the entries: an unparsable name is skipped, so
// is an entry that is not visible unless versions are listed; a name outside
// p.NS ends a namespaced listing and is skipped by a plain one, which moves
// its marker past a run of namespaced names; then the prefix filter, the
// delimiter rollup, p.NameFilter and the page bound. Rounds end when the cls
// listing is complete, at least half the page is filled, or after eight
// rounds with anything, or when the marker stops moving. A round whose shard
// a reshard removed is listed again from the shards the bucket now names
// (reshardedRecord).
//
// cls_filtered starts false here and cls_bucket_list_ordered only ANDs into
// it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1817,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9783),
// so radosgw always takes the branch written for OSDs that do not roll up
// delimiters, and so does rgw-go (docs/ceph-upstream-bugs.md, "radosgw never
// takes its OSD-filtered delimiter path").
func (s *Store) listObjectsOrdered(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	maxN := min(max(p.MaxKeys, 0), listAbsoluteMax)
	readAhead := max(s.opts.listMinReadahead, maxN)
	// cur is radosgw's cur_marker, and start the key bucket_list is sent,
	// which differs from it where resumeAfter and markerStart say.
	cur := rgwcls.ObjKey{Name: meta.ObjKey{Name: p.Marker, NS: p.NS}.IndexKeyName(), Instance: p.MarkerInstance}
	prefix := meta.ObjKey{Name: p.Prefix, NS: p.NS}.IndexKeyName()
	var start rgwcls.ObjKey
	if skip, ok := pastDelimiter(cur.Name, prefix, p.Delimiter); p.Delimiter != "" && ok {
		cur = rgwcls.ObjKey{Name: skip}
		start = cur
	} else {
		pos, err := s.markerStart(ctx, rec, p, cur)
		if err != nil {
			return op.ListObjectsResult{}, err
		}
		start = pos.after
	}
	var (
		res       op.ListObjectsResult
		prefixes  = map[string]bool{}
		count     int
		truncated = true
		prev      rgwcls.ObjKey
	)
rounds:
	for attempt := uint16(1); ; attempt++ {
		if attempt > 1 && prev == start {
			slog.ErrorContext(ctx, "bucket listing marker failed to make forward progress",
				slog.String("bucket", rec.Info.Bucket.Name), slog.Int("attempt", int(attempt)), slog.String("marker", start.Name))
			break
		}
		prev = start
		want := uint32(readAhead + 1 - count) //nolint:gosec // radosgw passes the int as a uint32 num_entries
		round, err := s.listOrdered(ctx, rec, start, prefix, p.Delimiter, want, p.ListVersions, attempt)
		for range reshardRetries {
			if !errors.Is(err, radosclient.ErrNotFound) {
				break
			}
			next, rerr := s.reshardedRecord(ctx, rec)
			if rerr != nil {
				return op.ListObjectsResult{}, rerr
			}
			if next == nil {
				break
			}
			slog.DebugContext(ctx, "bucket index shard is gone; listing the shards the bucket now names", slog.String("bucket", rec.Info.Bucket.Name))
			rec = next
			round, err = s.listOrdered(ctx, rec, start, prefix, p.Delimiter, want, p.ListVersions, attempt)
		}
		if err != nil {
			return op.ListObjectsResult{}, err
		}
		truncated = round.truncated
		if round.visited {
			cur, start = round.last, resumeAfter(round.lastIndex, round.last, p.ListVersions).after
		}
		for i := range round.entries {
			e := &round.entries[i]
			key, ok := meta.ParseIndexKeyName(e.Key.Name)
			if !ok {
				slog.ErrorContext(ctx, "could not parse object name", slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", e.Key.Name))
				continue
			}
			key.Instance = e.Key.Instance
			if !p.ListVersions && !visible(e) {
				continue
			}
			if key.NS != p.NS {
				if p.NS != "" {
					truncated = false
					break rounds
				}
				if key.NS != "" {
					// Namespaced names sort between "_" and "_\xFF": skip the
					// run of them, before the escaped "__" names when the
					// namespace sorts before "_", and past them all after.
					skip := rgwcls.ObjKey{Name: "_\xFF"}
					if key.NS[0] < '_' {
						skip = rgwcls.ObjKey{Name: "_^\xFF"}
					}
					if keyLess(cur, skip) {
						cur, start = skip, skip
						break // to another round from past the run
					}
				}
				continue
			}
			if count < maxN {
				res.NextMarker, res.NextMarkerInstance = key.Name, nextInstance(e)
			}
			if !strings.HasPrefix(key.Name, p.Prefix) {
				continue
			}
			if p.Delimiter != "" {
				if i := strings.Index(key.Name[len(p.Prefix):], p.Delimiter); i >= 0 {
					pk := key.Name[:len(p.Prefix)+i+len(p.Delimiter)]
					if !prefixes[pk] {
						if count >= maxN {
							truncated = true
							break rounds
						}
						res.NextMarker, res.NextMarkerInstance = pk, ""
						prefixes[pk] = true
						count++
					}
					continue
				}
			}
			// radosgw runs access_list_filter before the prefix and the
			// delimiter
			// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2003-L2009),
			// so it also refuses a class common-prefix entry whose name the
			// filter refuses; a refused name is skipped here only where it
			// would be listed (docs/exclusions.md, "ListMultipartUploads keeps
			// the common prefixes radosgw drops").
			if p.NameFilter != nil && !p.NameFilter(key.Name) {
				continue
			}
			if count >= maxN {
				truncated = true
				break rounds
			}
			res.Entries = append(res.Entries, objectEntry(key, e))
			count++
		}
		if p.Delimiter != "" {
			if skip, ok := pastDelimiter(cur.Name, prefix, p.Delimiter); ok && skip > cur.Name {
				cur = rgwcls.ObjKey{Name: skip}
				start = cur
			}
		}
		if !truncated || count >= (maxN+1)/2 || (attempt > listSoftAttempts && count >= 1) {
			break
		}
	}
	res.Truncated = truncated
	if len(prefixes) > 0 {
		res.CommonPrefixes = slices.Sorted(maps.Keys(prefixes))
	}
	if !truncated {
		res.NextMarker, res.NextMarkerInstance = "", ""
	}
	return res, nil
}

// listPos is where a listing goes on from: after, the key bucket_list
// starts after, and name, the index key name of the object it follows, whose
// shard the unordered listing starts on, as radosgw hashes the marker's
// whole name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10215-L10246 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10941-L10972 at v20.2.4).
type listPos struct {
	after rgwcls.ObjKey
	name  string
}

// markerStart is where a listing from marker, p's marker as an index key,
// starts. The class resolves a marker through encode_list_index_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L440 at v20.2.4),
// and so does rgw-go's listing, but for a versions listing's marker with no
// instance or the instance null:
//   - With no instance the listing starts past every version of the name,
//     where the class starts after the name's plain key and lists them again
//     (docs/exclusions.md, "A versions listing starts after every version
//     of a key-marker given alone").
//   - With null it starts after the null version getInstanceEntry finds, in
//     read_key_entry's order. The class looks for it at the null data key
//     alone, which a null delete marker does not have, and starts past every
//     version of the name, or after an older null version the marker's link
//     left for its writer to remove (docs/exclusions.md, "A versions listing
//     resumes after the null delete marker its version-id-marker names").
//     Finding no null version, or an unversioned object's entry, it starts
//     past every version of the name, where the class would too but for an
//     object named like it and 0x01.
//
// A marker whose version is gone therefore starts past the rest of its name,
// whatever instance it names, as a deleting pager's does (nextInstance).
func (s *Store) markerStart(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams, marker rgwcls.ObjKey) (listPos, error) {
	past := listPos{after: rgwcls.ObjKey{Name: marker.Name + pastVersions}, name: marker.Name}
	if !p.ListVersions || p.Marker == "" {
		return listPos{after: marker, name: marker.Name}, nil
	}
	switch marker.Instance {
	case "":
		return past, nil
	case meta.NullInstance:
	default:
		return listPos{after: marker, name: marker.Name}, nil
	}
	en, err := s.getInstanceEntry(ctx, rec, meta.ObjKey{Name: p.Marker, NS: p.NS})
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return past, nil
	case err != nil:
		return listPos{}, fmt.Errorf("reading the null version of %q in bucket %s: %w", p.Marker, rec.Info.Bucket.Name, err)
	case en.Flags&rgwcls.FlagVer == 0:
		return past, nil
	}
	return resumeAfter(rgwcls.ListIndexKey(en.Key, en.VersionedEpoch), en.Key, true), nil
}

// resumeAfter is where a listing that visited the entry stored under the
// index key k, whose own key is key, goes on from: after key, as radosgw's
// marker is the key of the last entry visited
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10130-L10133 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10856-L10859 at v20.2.4),
// but in a versions listing for the null version. Its key has no instance,
// so the class would start after the name's plain key and list the name's
// newer versions again (docs/exclusions.md, "A versions listing resumes
// after the null version it last visited"). Its list key k ends in NUL "i",
// so a delimiter that ends so would make the class start past every version
// of k's epoch
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L548-L554 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L598-L604 at v20.2.4).
// The listing starts after k and NUL instead, which no key lies between it
// and k. bucket_list takes either whole as a start key with no instance, and
// a delimiter that ends in NUL moves it only to k, NUL and 0xFF, still before
// the epoch's versions, whose instances are alphanumeric.
func resumeAfter(k string, key rgwcls.ObjKey, listVersions bool) listPos {
	if listVersions && key.Instance == "" && k != key.Name {
		return listPos{after: rgwcls.ObjKey{Name: k + "\x00"}, name: key.Name}
	}
	return listPos{after: key, name: key.Name}
}

// nextInstance is the next marker's instance for the entry e: its own, null
// for the null version, which has none, as S3's NextVersionIdMarker names it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1817-L1825 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1920-L1928 at v20.2.4),
// so that the marker resumes after it when copied back.
//
// A verbatim round trip resumes after the last listed entry. A caller that
// deletes what it lists sends back a marker whose version is gone, which
// starts past the rest of that name (markerStart), so it restarts each page
// from an empty marker, or lists until a pass finds nothing. radosgw's bucket
// removal pages by the marker and leaves those versions behind
// (docs/ceph-upstream-bugs.md, "radosgw's bucket removal with purge leaves a
// key's older versions behind when a page ends on one of its versions").
func nextInstance(e *rgwcls.DirEntry) string {
	if e.Key.Instance == "" && e.Flags&rgwcls.FlagVer != 0 {
		return meta.NullInstance
	}
	return e.Key.Instance
}

// reshardedRecord reads rec's bucket instance again, past the metadata cache,
// and returns it when its current index generation names other shards than
// rec's, as a reshard that removed one of rec's leaves it; nil when it names
// the same shards, or the bucket is gone, so that the ENOENT a shard answered
// is the listing's (docs/exclusions.md, "An ordered listing reads the shards
// the bucket names after a reshard removes its own").
func (s *Store) reshardedRecord(ctx context.Context, rec *op.BucketRecord) (*op.BucketRecord, error) {
	o := s.instanceObj(rec.Info.Bucket)
	s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
	next, err := s.GetBucketInstance(ctx, rec.Info.Bucket)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("reading bucket %s again for its index shards: %w", rec.Info.Bucket.Name, err)
	}
	if slices.Equal(shardOIDs(&next.Info, next.Info.Layout.Current), shardOIDs(&rec.Info, rec.Info.Layout.Current)) {
		return nil, nil
	}
	return next, nil
}

// pastDelimiter is the marker past the common prefix name falls in: name up
// to the first delimiter after prefix, then cls_rgw_after_delim(delim), the
// delimiter and 0xFF
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L139-L142).
// ok is false when name holds no delimiter there.
func pastDelimiter(name, prefix, delim string) (string, bool) {
	if len(prefix) > len(name) {
		return "", false
	}
	i := strings.Index(name[len(prefix):], delim)
	if i < 0 {
		return "", false
	}
	return name[:len(prefix)+i] + delim + "\xFF", true
}

// keyLess is rgw_obj_index_key::operator<: by name, then instance.
func keyLess(a, b rgwcls.ObjKey) bool {
	return a.Name < b.Name || (a.Name == b.Name && a.Instance < b.Instance)
}

// current is rgw_bucket_dir_entry::is_current
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L446-L451):
// an entry of an unversioned object always is.
func current(e *rgwcls.DirEntry) bool {
	both := rgwcls.FlagVer | rgwcls.FlagCurrent
	return e.Flags&rgwcls.FlagVer == 0 || e.Flags&both == both
}

// visible is rgw_bucket_dir_entry::is_visible: current and not a delete
// marker.
func visible(e *rgwcls.DirEntry) bool {
	return current(e) && e.Flags&rgwcls.FlagDeleteMarker == 0
}

// objectEntry is the listing's rendering of index entry e under key, the
// name parsed from it with its instance: the accounted size as the Size
// radosgw lists, the owner as stored, none for an empty one, an empty
// storage class as STANDARD
// (get_canonical_storage_class), the version flags, the versioned epoch and
// the category.
func objectEntry(key meta.ObjKey, e *rgwcls.DirEntry) op.ObjectEntry {
	var owner meta.Owner
	if e.Meta.Owner != "" {
		owner = meta.ParseOwner(e.Meta.Owner)
	}
	return op.ObjectEntry{
		Key:              key,
		Size:             e.Meta.AccountedSize,
		Mtime:            e.Meta.Mtime,
		ETag:             e.Meta.ETag,
		Owner:            owner,
		OwnerDisplayName: e.Meta.OwnerDisplayName,
		StorageClass:     cmp.Or(e.Meta.StorageClass, meta.StorageClassStandard),
		IsLatest:         current(e),
		DeleteMarker:     e.Flags&rgwcls.FlagDeleteMarker != 0,
		Exists:           e.Exists,
		Appendable:       e.Meta.AppendableValue,
		VersionedEpoch:   e.VersionedEpoch,
		Category:         e.Meta.Category,
	}
}

// calcPerShard is calc_ordered_bucket_list_per_shard
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9578-L9611):
// the entries to ask each of numShards shards for so that numEntries are
// likely to come back, after Raab and Steger's balls-into-bins bound, at
// least 8, and 0 for no shards. The quotient is an integer division, as in
// C++.
func calcPerShard(numEntries, numShards uint32) uint32 {
	if numShards == 0 {
		return 0
	}
	const minRead = 8
	n, s := float64(numEntries), float64(numShards)
	calc := 1 + uint32(float64(numEntries/numShards)+math.Sqrt(2*n*math.Log(s)/s))
	return max(minRead, calc)
}

// shardTracker is cls_bucket_list_ordered's ShardTracker: one shard's reply,
// its names in order and how far the merge has taken them.
type shardTracker struct {
	oid   string
	ret   rgwcls.ListRet
	names []string
	pos   int
}

func (t *shardTracker) atEnd() bool { return t.pos >= len(t.names) }

// orderedRound is what one listOrdered call found: the entries kept, in
// name order; whether any shard has more; and the last entry visited, kept or
// dropped, by its own key and the index key it was listed under, which
// visited says there was.
type orderedRound struct {
	entries   []rgwcls.DirEntry
	truncated bool
	last      rgwcls.ObjKey
	lastIndex string
	visited   bool
}

// listOrdered is cls_bucket_list_ordered
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9810-L10141 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10535-L10867 at v20.2.4):
// one bucketList per shard of the current index generation, at most
// rgw_bucket_index_max_aio in flight, for calcPerShard entries scaled by
// the expansion factor, then a merge of the replies by name. An entry that
// is not there and is neither a delete marker nor a common prefix, or has a
// pending op, is checked against its head; one whose head is gone is
// dropped. The merge stops at numEntries, or when a truncated shard runs
// out, since the next name could be on it. The suggestions the checks made
// are sent, and the listing is truncated when any shard has entries left.
func (s *Store) listOrdered(ctx context.Context, rec *op.BucketRecord, startAfter rgwcls.ObjKey, prefix, delimiter string,
	numEntries uint32, listVersions bool, attempt uint16,
) (round orderedRound, err error) {
	pool, oids, err := s.indexShards(ctx, rec)
	if err != nil {
		return orderedRound{}, err
	}
	shardCount := uint32(len(oids)) //nolint:gosec // a shard count fits a u32
	if shardCount == 0 {
		return orderedRound{}, fmt.Errorf("%w: the bucket index shard count of %s is 0", op.ErrInvalidBucketState, rec.Info.Bucket.Name)
	}
	perShard := calcPerShard(numEntries, shardCount)
	switch {
	case attempt == 0:
	case attempt <= 11:
		perShard = min(numEntries, (uint32(1)<<(attempt-1))*perShard)
	default:
		perShard = numEntries
	}
	if perShard == 0 {
		return orderedRound{}, fmt.Errorf("%w: unable to calculate the entries to read from each index shard of %s", op.ErrInvalidBucketState, rec.Info.Bucket.Name)
	}
	trackers := make([]shardTracker, len(oids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(s.opts.bucketIndexMaxAIO, 1))
	for i, oid := range oids {
		g.Go(func() error {
			ret, _, lerr := s.bucketList(gctx, pool, oid, rgwcls.ListOp{
				StartObj: startAfter, NumEntries: perShard, FilterPrefix: prefix, ListVersions: listVersions, Delimiter: delimiter,
			})
			trackers[i] = shardTracker{oid: oid, ret: ret, names: slices.Sorted(maps.Keys(ret.Dir.Entries))}
			return lerr
		})
	}
	if werr := g.Wait(); werr != nil {
		return orderedRound{}, werr
	}
	// check_disk_state's removals carry the index pool and epoch 0: the
	// listing's reads are asynchronous, so get_last_version has nothing.
	indexVer := rgwcls.EntryVer{Pool: pool.ID()}
	updates := map[string][]rgwcls.Suggestion{}
	listed := map[string]int{}
	var count uint32
	for count < numEntries {
		best := -1
		for i := range trackers {
			if !trackers[i].atEnd() && (best < 0 || trackers[i].names[trackers[i].pos] < trackers[best].names[trackers[best].pos]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		t := &trackers[best]
		name := t.names[t.pos]
		ent := t.ret.Dir.Entries[name]
		keep := true
		if needsDiskCheck(&ent, true) {
			if keep, err = s.checkDiskState(ctx, rec, indexVer, &ent, updates, t.oid); err != nil {
				return orderedRound{}, err
			}
		}
		round.last, round.lastIndex, round.visited = ent.Key, name, true
		if keep {
			if i, ok := listed[name]; ok {
				slog.WarnContext(ctx, "reassigned a listed entry, which should not happen", slog.String("key", name))
				round.entries[i] = ent
			} else {
				listed[name] = len(round.entries)
				round.entries = append(round.entries, ent)
				count++
			}
		}
		stop := false
		for i := range trackers {
			m := &trackers[i]
			if m.atEnd() || m.names[m.pos] != name {
				continue
			}
			m.pos++
			if m.atEnd() && m.ret.IsTruncated {
				stop = true
				break
			}
		}
		if stop {
			break
		}
	}
	s.suggest(ctx, pool, updates)
	for i := range trackers {
		if !trackers[i].atEnd() || trackers[i].ret.IsTruncated {
			round.truncated = true
			break
		}
	}
	return round, nil
}

// needsDiskCheck is the test both shard listings make before
// check_disk_state
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10019-L10031 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10279-L10287 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10743-L10755 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11005-L11013 at v20.2.4):
// an entry with a pending op, or one that is not there and is not a delete
// marker, nor, in the ordered listing, a common prefix.
func needsDiskCheck(e *rgwcls.DirEntry, ordered bool) bool {
	absent := !e.Exists && e.Flags&rgwcls.FlagDeleteMarker == 0
	if ordered {
		absent = absent && e.Flags&rgwcls.FlagCommonPrefix == 0
	}
	return absent || len(e.PendingMap) > 0
}

// indexShards opens the bucket's index pool and names the shards of its
// current index generation, as open_bucket_index does; a bucket without an
// id is radosgw's -EIO
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bi_rados.cc#L83-L86 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L91-L94 at v20.2.4).
func (s *Store) indexShards(ctx context.Context, rec *op.BucketRecord) (radosclient.Pool, []string, error) {
	pool, err := s.indexPool(ctx, &rec.Info)
	if err != nil {
		return nil, nil, err
	}
	if rec.Info.Bucket.ID == "" {
		return nil, nil, fmt.Errorf("%w: bucket %s has no bucket id", op.ErrUnknown, rec.Info.Bucket.Name)
	}
	return pool, shardOIDs(&rec.Info, rec.Info.Layout.Current), nil
}

// bucketList runs bucket_list on the shard oid and returns the reply and
// the version the read returned. A shard the OSD does not find is radosgw's
// -ENOENT, NoSuchKey, and a reply that does not decode its -EIO. A shard
// that answers RGWBIAdvanceAndRetryError, having read only entries the
// listing skips, is listed again from the marker in its reply, as radosgw's
// ordered listing lists it again
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L99-L110 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L161-L166 at v19.2.7;
// ListReader,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L948-L968 at v20.2.4).
// The unordered listing does too, where radosgw's fails (docs/exclusions.md,
// "An unordered listing lists a shard that answers advance-and-retry again
// from its marker"). A marker past a name's versions, the name and 0x01,
// is listed again from past the name's versions instead (retryAfter). A
// reply whose marker is empty or the start key again fails with
// UnknownError, where radosgw's would list the same keys again without end.
func (s *Store) bucketList(ctx context.Context, pool radosclient.Pool, oid string, l rgwcls.ListOp) (rgwcls.ListRet, uint64, error) {
	for {
		rop := radosclient.NewReadOp()
		r := rgwcls.BucketList(rop, l, s.release)
		ver, rerr := pool.Read(ctx, oid, rop, radosclient.OpFlagNone)
		ret, retry, err := r.Reply()
		switch {
		case retry && (ret.Marker == rgwcls.ObjKey{} || retryAfter(ret.Marker) == l.StartObj):
			return rgwcls.ListRet{}, 0, fmt.Errorf("%w: index shard %s answered advance-and-retry with no marker past %q", op.ErrUnknown, oid, l.StartObj.Name)
		case retry:
			l.StartObj = retryAfter(ret.Marker)
			continue
		case rerr != nil && err != nil && errors.Is(rerr, radosclient.ErrTooBig):
			return rgwcls.ListRet{}, 0, fmt.Errorf("listing index shard %s: %w: %w", oid, op.FromRADOS(rerr, op.ScopeObject), err)
		case rerr != nil:
			return rgwcls.ListRet{}, 0, fmt.Errorf("listing index shard %s: %w", oid, op.FromRADOS(rerr, op.ScopeObject))
		case err != nil:
			return rgwcls.ListRet{}, 0, fmt.Errorf("%w: listing index shard %s: %w", op.ErrUnknown, oid, err)
		}
		return ret, ver, nil
	}
}

// retryAfter is the key a shard that answered advance-and-retry is listed
// again after: the reply's marker m, but for one past a name's versions, the
// name and 0x01
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L617-L618 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L667-L668 at v20.2.4),
// the name and pastVersions, before an object named like that marker, which
// a read after it leaves out (docs/ceph-upstream-bugs.md, "cls_rgw's listing
// can skip an object whose name is another versioned name and 0x01"). An
// object's own key named so lists it again only to skip it again, since the
// class answered so having listed nothing.
func retryAfter(m rgwcls.ObjKey) rgwcls.ObjKey {
	if name, ok := strings.CutSuffix(m.Name, "\x01"); ok && name != "" && m.Instance == "" {
		return rgwcls.ObjKey{Name: name + pastVersions}
	}
	return m
}

// checkDiskState is check_disk_state
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10323-L10475):
// it reconciles the index entry ent, found on the shard oid, with the
// object's head and records the suggestion that repairs the shard under
// updates[oid]. keep is false when the head is gone and the entry is not a
// delete marker: the suggestion removes it, with ver set to indexVer.
// Otherwise the entry takes the head's size, accounted size, mtime, etag,
// content type, storage class, owner, appendability, Main category, data
// pool and epoch, write tag and existence, and the suggestion updates it,
// and the multipart parts its manifest names leave the index, as sweepParts
// says. Either way the entry leaves its pending ops behind. A head check
// that fails fails the listing, but one the object store cannot make yet
// keeps the entry as it is with no suggestion. A multipart upload's meta
// entry is kept as it is too: radosgw checks it in the data-extra pool,
// which the object store does not read (docs/exclusions.md, "A pending
// multipart upload's index entry is not reconciled by a listing"). radosgw
// picks that pool for every raw name MultipartMetaFilter accepts
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10341-L10344),
// a plain object named like "a.b.meta" too, which it then fails to find;
// rgw-go checks a plain object in the data pool whatever its name
// (docs/exclusions.md, "A plain object whose name ends in .meta is checked
// where it is stored").
//
// dir_suggest_changes applies a change under encode_obj_index_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2249-L2250 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2419-L2420 at v20.2.4),
// so a version's reaches its instance entry and never the list entry it was
// made from (docs/ceph-upstream-bugs.md, "radosgw's listing never repairs a
// version's list entry"); rgw-go sends it so, as radosgw does. The null
// version's list entry has no instance, so its change would land on the
// name's plain key, the version marker, which an update overwrites and a
// removal removes (docs/ceph-upstream-bugs.md, "radosgw's listing repair of a
// null version's list entry overwrites or removes the key's version
// marker"): for it rgw-go sends nothing (docs/exclusions.md, "A listing sends
// no repair for the null version's list entry").
func (s *Store) checkDiskState(ctx context.Context, rec *op.BucketRecord, indexVer rgwcls.EntryVer, ent *rgwcls.DirEntry,
	updates map[string][]rgwcls.Suggestion, oid string,
) (keep bool, err error) {
	name, ns := parseIndexKey(ent.Key.Name)
	if ns == meta.NSMultipart && meta.IsMultipartMeta(name) {
		return true, nil
	}
	key := meta.ObjKey{Name: name, NS: ns, Instance: ent.Key.Instance}
	st, err := s.heads.StatObject(ctx, rec, key)
	if errors.Is(err, op.ErrNotImplemented) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	logOp := s.zone.Zone.LogData
	record := func(change byte) {
		if ent.Key.Instance == "" && ent.Flags&rgwcls.FlagVer != 0 {
			return
		}
		updates[oid] = append(updates[oid], rgwcls.Suggestion{Op: change, Log: logOp, Entry: *ent})
	}
	ent.PendingMap = nil
	if ent.Flags&rgwcls.FlagDeleteMarker == 0 && !st.Exists {
		ent.Ver = indexVer
		record(rgwcls.SuggestRemove)
		return false, nil
	}
	m := &ent.Meta
	m.Size, m.AccountedSize, m.Mtime = st.Size, st.Size, st.Mtime
	if st.Compression != nil {
		m.AccountedSize = st.Compression.OrigSize
	}
	m.ETag = blStr(st.Attrs[meta.AttrETag])
	m.ContentType = blStr(st.Attrs[meta.AttrContentType])
	m.StorageClass = blStr(st.Attrs[meta.AttrStorageClass])
	owner := aclOwner(ctx, st.Attrs[meta.AttrACL], rec, key)
	m.Owner, m.OwnerDisplayName = owner.ID, owner.DisplayName
	_, m.AppendableValue = st.Attrs[meta.AttrAppendPartNum]
	if st.Manifest != nil {
		if err := s.sweepParts(rec, st); err != nil {
			slog.WarnContext(ctx, "could not walk the manifest for its multipart parts' index entries",
				slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.Any("error", err))
		}
	}
	m.Category = rgwcls.CategoryMain
	if pool, ok := s.dataPool(rec.Info.PlacementRule, rec.Info.Bucket); ok && pool.Name != "" {
		if h, err := s.pools.get(ctx, pool); err == nil {
			ent.Ver = rgwcls.EntryVer{Pool: h.ID(), Epoch: st.Epoch}
		} else {
			slog.WarnContext(ctx, "unable to find head object data pool, not updating version pool/epoch",
				slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.Any("error", err))
		}
	} else {
		slog.WarnContext(ctx, "unable to find head object data pool, not updating version pool/epoch",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name))
	}
	if tag, _, _ := strings.Cut(st.WriteTag, "\x00"); tag != "" {
		ent.Tag = tag
	}
	ent.Exists = true
	record(rgwcls.SuggestUpdate)
	return true, nil
}

// sweepParts is check_disk_state's walk of an existing head's manifest
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10413-L10429;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11337-L11353):
// every stripe in the multipart namespace, a part's head, has its index
// entry completed as deleted with delete_obj_index, hashed on the part's own
// name as raw_obj_to_obj leaves it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_tier_rados.h#L131-L143),
// though the part writer indexed it under the upload's
// (docs/ceph-upstream-bugs.md, "check_disk_state removes a multipart part's
// index entry from the wrong shard"). The walk stops with denc.ErrMalformed
// at a step that does not move forward and with meta.ErrTooManyStripes past
// meta.MaxWalkStripes stripes, where radosgw's never ends.
func (s *Store) sweepParts(rec *op.BucketRecord, st *op.ObjectState) error {
	limit := cmp.Or(s.sweepLimit, meta.MaxWalkStripes)
	it, err := st.Manifest.Seek(0)
	for n := 1; err == nil && !it.Done(); n++ {
		if loc, _, _ := it.Location(); loc.Key.NS == meta.NSMultipart {
			s.deleteObjIndex(rec, loc.Key, st.Mtime)
		}
		prev := it.Ofs()
		switch err = it.Next(); {
		case err != nil, it.Done():
		case it.Ofs() <= prev:
			err = fmt.Errorf("%w: manifest iteration does not advance past offset %d of %d", denc.ErrMalformed, prev, st.Manifest.ObjSize)
		case n >= limit:
			err = fmt.Errorf("%w: more than %d before offset %d of %d", meta.ErrTooManyStripes, limit, it.Ofs(), st.Manifest.ObjSize)
		}
	}
	return err
}

// blStr is rgw_bl_str
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L2063-L2071 at v19.2.6):
// the attr's bytes with trailing NULs trimmed.
func blStr(b []byte) string { return strings.TrimRight(string(b), "\x00") }

// aclOwner is check_disk_state's RGWRados::decode_policy
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1754-L1768),
// which decodes only the owner of the head's ACL: an ACL that does not
// decode leaves the owner empty with a warning, as no ACL does.
func aclOwner(ctx context.Context, b []byte, rec *op.BucketRecord, key meta.ObjKey) acl.Owner {
	o, err := policyOwner(b)
	if err != nil {
		slog.WarnContext(ctx, "could not decode policy for object", slog.String("bucket", rec.Info.Bucket.Name),
			slog.String("key", key.Name), slog.Any("error", err))
	}
	return o
}

// policyOwner is RGWRados::decode_policy
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1754-L1768 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1857-L1871 at v20.2.4)
// through RGWAccessControlPolicy::decode_owner: the owner of an encoded ACL,
// its grants skipped undecoded, so a policy whose grants do not decode still
// names its owner. No ACL is no owner; one whose owner does not decode is no
// owner and the decode's error.
func policyOwner(b []byte) (acl.Owner, error) {
	if b == nil {
		return acl.Owner{}, nil
	}
	d := denc.NewDecoder(b)
	h := d.BeginStructLegacy(2, 2, 2, 0)
	o := acl.DecodeOwner(d)
	d.EndStruct(h)
	if err := d.Err(); err != nil {
		return acl.Owner{}, err
	}
	return o, nil
}

// parseIndexKey is rgw_obj_key::parse_index_key
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_obj_types.h#L125-L146 at v19.2.6
// and v20.2.4), which rgw_obj_key's constructor from an index key runs:
// like ParseIndexKeyName but with the instance left in the namespace, and a
// name it cannot split kept whole.
func parseIndexKey(k string) (name, ns string) {
	if !strings.HasPrefix(k, "_") {
		return k, ""
	}
	if len(k) > 1 && k[1] == '_' {
		return k[1:], ""
	}
	pos := strings.IndexByte(k[1:], '_')
	if pos < 0 {
		return k, ""
	}
	pos++
	return k[pos+1:], k[1:pos]
}

// suggest sends each shard's suggestions as one dir_suggest_changes write,
// blindly, as radosgw sends them with aio_operate
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9898-L9912),
// guarded against resharding on Tentacle as v20.2.4 guards them
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10819-L10832;
// docs/ceph-upstream-bugs.md, "Squid does not guard listing-time index
// suggestions against resharding"). Each write runs on its own goroutine,
// past the request's end and for at most suggestTimeout, at most
// rgw_bucket_index_max_aio of them at a time. radosgw never waits for a
// suggestion ("we don't care if we lose suggested updates"), so neither does
// the listing: a shard's suggestions are dropped when no slot is free, and a
// failed write is logged at debug level.
func (s *Store) suggest(ctx context.Context, pool radosclient.Pool, updates map[string][]rgwcls.Suggestion) {
	for _, oid := range slices.Sorted(maps.Keys(updates)) {
		changes := updates[oid]
		if len(changes) == 0 {
			continue
		}
		if !s.bgAIO.TryAcquire(1) {
			slog.DebugContext(ctx, "dropping index suggestions: every slot is busy", slog.String("oid", oid))
			continue
		}
		w := radosclient.NewWriteOp()
		if s.release >= denc.Tentacle {
			w.AssertExists()
			rgwcls.GuardBucketResharding(w, s.release)
		}
		rgwcls.SuggestChanges(w, changes, s.release)
		go func() {
			defer s.bgAIO.Release(1)
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), suggestTimeout)
			defer cancel()
			if _, err := pool.Write(bg, oid, w, radosclient.OpFlagNone); err != nil {
				slog.DebugContext(bg, "dir suggest failed", slog.String("oid", oid), slog.Any("error", err))
			}
		}()
	}
}

// listObjectsUnordered is list_objects_unordered
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2194-L2343 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2284-L2433 at v20.2.4):
// rounds of listUnordered with a read-ahead of up to 100 entries past the
// page, while the shards have more and the page is not over, from where
// markerStart puts the marker and its instance and then from where the last
// round's last entry resumes. The next marker, name and instance, follows
// every parsable entry read while the page has room, before the entry is
// filtered; only the page's last counted entry can leave it, since a listing
// ends truncated only once the page is full. An unparsable name, an entry
// that is not visible unless versions are listed, one outside p.NS, one
// p.NameFilter refuses
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2297-L2303)
// and one outside the prefix are then skipped. No common prefixes are formed.
func (s *Store) listObjectsUnordered(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	maxN := min(max(p.MaxKeys, 0), listAbsoluteMax)
	readAhead := uint32(maxN + min(maxN, listUnorderedReadAhead)) //nolint:gosec // at most 2 * listAbsoluteMax
	marker := rgwcls.ObjKey{Name: meta.ObjKey{Name: p.Marker, NS: p.NS}.IndexKeyName(), Instance: p.MarkerInstance}
	prefix := meta.ObjKey{Name: p.Prefix, NS: p.NS}.IndexKeyName()
	cur, err := s.markerStart(ctx, rec, p, marker)
	if err != nil {
		return op.ListObjectsResult{}, err
	}
	var (
		res       op.ListObjectsResult
		count     int
		truncated = true
	)
rounds:
	for truncated && count <= maxN {
		entries, trunc, last, err := s.listUnordered(ctx, rec, cur, prefix, readAhead, p.ListVersions)
		if err != nil {
			return op.ListObjectsResult{}, err
		}
		truncated = trunc
		if len(entries) > 0 {
			cur = last
		}
		for i := range entries {
			e := &entries[i]
			key, ok := meta.ParseIndexKeyName(e.Key.Name)
			if count < maxN {
				// next_marker.set(index_key) parses the raw name, which keeps
				// the name it held when the parse fails, and clears the
				// instance before it sets the entry's.
				res.NextMarkerInstance = ""
				if ok {
					res.NextMarker, res.NextMarkerInstance = key.Name, nextInstance(e)
				}
			}
			if !ok {
				slog.ErrorContext(ctx, "could not parse object name", slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", e.Key.Name))
				continue
			}
			key.Instance = e.Key.Instance
			if (!p.ListVersions && !visible(e)) || key.NS != p.NS || (p.NameFilter != nil && !p.NameFilter(key.Name)) ||
				!strings.HasPrefix(key.Name, p.Prefix) {
				continue
			}
			if count >= maxN {
				truncated = true
				break rounds
			}
			res.Entries = append(res.Entries, objectEntry(key, e))
			count++
		}
	}
	res.Truncated = truncated
	if !truncated {
		res.NextMarker, res.NextMarkerInstance = "", ""
	}
	return res, nil
}

// listUnordered is cls_bucket_list_unordered
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10161-L10351 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10887-L11079 at v20.2.4):
// the shards one after another, from the one the name of start's object
// hashes to (a multipart upload's meta entry by its upload's key,
// parse_index_hash_source) or, with no start key, from the first, each read
// with bucketList and no delimiter until numEntries are listed and one more
// is seen, which makes the listing truncated. A shard read again continues
// from where the last entry visited resumes (resumeAfter). An entry is
// checked against its head as listOrdered checks one, the removals carrying
// the shard read's version, and the suggestions are sent. last is where the
// last entry listed resumes, valid when any is. A start object whose name
// does not parse is radosgw's -EINVAL.
func (s *Store) listUnordered(ctx context.Context, rec *op.BucketRecord, start listPos, prefix string,
	numEntries uint32, listVersions bool,
) (entries []rgwcls.DirEntry, truncated bool, last listPos, err error) {
	pool, oids, err := s.indexShards(ctx, rec)
	if err != nil {
		return nil, false, listPos{}, err
	}
	numShards := uint32(len(oids)) //nolint:gosec // a shard count fits a u32
	var shard uint32
	if start.after.Name != "" {
		k, ok := meta.ParseIndexKeyName(start.name)
		if !ok {
			return nil, false, listPos{}, fmt.Errorf("%w: invalid start marker %q", op.ErrInvalidArgument, start.name)
		}
		if k.Name != "" {
			src := k.Name
			if k.NS == meta.NSMultipart {
				if src, ok = indexHashSource(k.Name); !ok {
					return nil, false, listPos{}, fmt.Errorf("%w: no index hash source in %q", op.ErrInvalidArgument, k.Name)
				}
			}
			shard, _ = meta.IndexShard(src, numShards)
		}
	}
	indexVer := rgwcls.EntryVer{Pool: pool.ID()}
	updates := map[string][]rgwcls.Suggestion{}
	marker := start
	var count uint32
shards:
	for count <= numEntries && shard < numShards {
		oid := oids[shard]
		ret, ver, err := s.bucketList(ctx, pool, oid, rgwcls.ListOp{StartObj: marker.after, NumEntries: numEntries, FilterPrefix: prefix, ListVersions: listVersions})
		if err != nil {
			return nil, false, listPos{}, err
		}
		indexVer.Epoch = ver
		for _, name := range slices.Sorted(maps.Keys(ret.Dir.Entries)) {
			ent := ret.Dir.Entries[name]
			keep := true
			if needsDiskCheck(&ent, false) {
				if keep, err = s.checkDiskState(ctx, rec, indexVer, &ent, updates, oid); err != nil {
					return nil, false, listPos{}, err
				}
			}
			if !keep {
				marker = resumeAfter(name, ent.Key, listVersions)
				continue
			}
			if count >= numEntries {
				truncated = true
				break shards
			}
			marker = resumeAfter(name, ent.Key, listVersions)
			last = marker
			entries = append(entries, ent)
			count++
		}
		if !ret.IsTruncated {
			shard++
			marker = listPos{}
		}
	}
	s.suggest(ctx, pool, updates)
	return entries, truncated, last, nil
}

// indexHashSource is parse_index_hash_source
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9951-L9962):
// a multipart meta name up to its second-to-last dot, the upload's key its
// entries hash by; ok is false when either dot is missing or first.
func indexHashSource(name string) (string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 1 {
		return "", false
	}
	i = strings.LastIndexByte(name[:i], '.')
	if i < 1 {
		return "", false
	}
	return name[:i], true
}
