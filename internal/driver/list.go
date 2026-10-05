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
// helpers under it (driver/rados/rgw_rados.cc). A bare line number is
// v19.2.6's rgw_rados.cc; v20.2.4 carries the same code.

//go:generate go tool counterfeiter -generate

//counterfeiter:generate . HeadStater

// HeadStater is the one object-store method the listing's disk check needs:
// an object's head as get_obj_state reads it.
type HeadStater interface {
	StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error)
}

const (
	// listAbsoluteMax is bucket_list_objects_absolute_max (rgw_rados.h:1018
	// at v19.2.6, :1068 at v20.2.4), the most entries one page returns.
	listAbsoluteMax = 25000
	// listSoftAttempts is SOFT_MAX_ATTEMPTS (:1859).
	listSoftAttempts = 8
	// listUnorderedReadAhead is list_objects_unordered's max_read_ahead
	// (:2201).
	listUnorderedReadAhead = 100
	// suggestTimeout bounds a listing's background index suggestion, which
	// radosgw sends with aio_operate and never waits for.
	suggestTimeout = 30 * time.Second
)

// ListObjects implements op.BucketStore: RGWRados::Bucket::List::list_objects,
// which lists ordered or, with AllowUnordered, unordered. An unordered
// listing with a delimiter is the -EINVAL RGWListBucket::execute answers
// (rgw_op.cc:3085-3090 at v19.2.6, :3326-3331 at v20.2.4).
func (s *Store) ListObjects(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	if p.AllowUnordered {
		if p.Delimiter != "" {
			return op.ListObjectsResult{}, fmt.Errorf("%w: unordered bucket listing requested with a delimiter", op.ErrInvalidArgument)
		}
		return s.listObjectsUnordered(ctx, rec, p)
	}
	return s.listObjectsOrdered(ctx, rec, p)
}

// listObjectsOrdered is list_objects_ordered (:1802-2159). The marker and
// prefix become index key names in p.NS; with a delimiter a marker inside a
// common prefix moves past it. Each round asks listOrdered for what the
// page still lacks, with the round as the expansion factor, and walks the
// entries: an unparsable name is skipped, so is an entry that is not
// visible unless versions are listed; a name outside p.NS ends a namespaced
// listing and is skipped by a plain one, which moves its marker past a run
// of namespaced names; then the prefix filter, the delimiter rollup and the
// page bound. Rounds end when the cls listing is complete, at least half the
// page is filled, or after eight rounds with anything, or when the marker
// stops moving.
//
// cls_filtered starts false here and cls_bucket_list_ordered only ANDs into
// it (:1817, :9783), so radosgw always takes the branch written for OSDs
// that do not roll up delimiters, and so does rgw-go (docs/ceph-upstream-bugs.md,
// "radosgw never takes its OSD-filtered delimiter path").
func (s *Store) listObjectsOrdered(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	maxN := min(max(p.MaxKeys, 0), listAbsoluteMax)
	readAhead := max(s.opts.listMinReadahead, maxN)
	cur := rgwcls.ObjKey{Name: meta.ObjKey{Name: p.Marker, NS: p.NS}.IndexKeyName()}
	prefix := meta.ObjKey{Name: p.Prefix, NS: p.NS}.IndexKeyName()
	if p.Delimiter != "" {
		if skip, ok := pastDelimiter(cur.Name, prefix, p.Delimiter); ok {
			cur = rgwcls.ObjKey{Name: skip}
		}
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
		if attempt > 1 && prev == cur {
			slog.ErrorContext(ctx, "bucket listing marker failed to make forward progress",
				slog.String("bucket", rec.Info.Bucket.Name), slog.Int("attempt", int(attempt)), slog.String("marker", cur.Name))
			break
		}
		prev = cur
		want := uint32(readAhead + 1 - count) //nolint:gosec // radosgw passes the int as a uint32 num_entries
		entries, trunc, last, visited, err := s.listOrdered(ctx, rec, cur, prefix, p.Delimiter, want, p.ListVersions, attempt)
		if err != nil {
			return op.ListObjectsResult{}, err
		}
		truncated = trunc
		if visited {
			cur = last
		}
		for i := range entries {
			e := &entries[i]
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
						cur = skip
						break // to another round from past the run
					}
				}
				continue
			}
			if count < maxN {
				res.NextMarker = key.Name
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
						res.NextMarker = pk
						prefixes[pk] = true
						count++
					}
					continue
				}
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
		res.NextMarker = ""
	}
	return res, nil
}

// pastDelimiter is the marker past the common prefix name falls in: name up
// to the first delimiter after prefix, then cls_rgw_after_delim(delim), the
// delimiter and 0xFF (cls_rgw_types.h:139-142). ok is false when name holds
// no delimiter there.
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

// current is rgw_bucket_dir_entry::is_current (cls_rgw_types.h:446-451):
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
// (get_canonical_storage_class), and the version flags.
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
	}
}

// calcPerShard is calc_ordered_bucket_list_per_shard (:9578-9611): the
// entries to ask each of numShards shards for so that numEntries are likely
// to come back, after Raab and Steger's balls-into-bins bound, at least 8,
// and 0 for no shards. The quotient is an integer division, as in C++.
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

// listOrdered is cls_bucket_list_ordered (:9614-9945): one bucket_list per
// shard of the current index generation, at most rgw_bucket_index_max_aio
// in flight, for calcPerShard entries scaled by the expansion factor, then
// a merge of the replies by name. An entry that is not there and is neither
// a delete marker nor a common prefix, or has a pending op, is checked
// against its head; one whose head is gone is dropped. The merge stops at
// numEntries, or when a truncated shard runs out, since the next name could
// be on it. The suggestions the checks made are sent, and the listing is
// truncated when any shard has entries left. last is the key of the last
// entry visited, kept or dropped, and visited is false when there was none.
func (s *Store) listOrdered(ctx context.Context, rec *op.BucketRecord, startAfter rgwcls.ObjKey, prefix, delimiter string,
	numEntries uint32, listVersions bool, attempt uint16,
) (entries []rgwcls.DirEntry, truncated bool, last rgwcls.ObjKey, visited bool, err error) {
	pool, oids, err := s.indexShards(ctx, rec)
	if err != nil {
		return nil, false, rgwcls.ObjKey{}, false, err
	}
	shardCount := uint32(len(oids)) //nolint:gosec // a shard count fits a u32
	if shardCount == 0 {
		return nil, false, rgwcls.ObjKey{}, false, fmt.Errorf("%w: the bucket index shard count of %s is 0", op.ErrInvalidBucketState, rec.Info.Bucket.Name)
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
		return nil, false, rgwcls.ObjKey{}, false, fmt.Errorf("%w: unable to calculate the entries to read from each index shard of %s", op.ErrInvalidBucketState, rec.Info.Bucket.Name)
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
		return nil, false, rgwcls.ObjKey{}, false, werr
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
				return nil, false, rgwcls.ObjKey{}, false, err
			}
		}
		last, visited = ent.Key, true
		if keep {
			if i, ok := listed[name]; ok {
				slog.WarnContext(ctx, "reassigned a listed entry, which should not happen", slog.String("key", name))
				entries[i] = ent
			} else {
				listed[name] = len(entries)
				entries = append(entries, ent)
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
			truncated = true
			break
		}
	}
	return entries, truncated, last, visited, nil
}

// needsDiskCheck is the test both shard listings make before
// check_disk_state (:9823-9827, :10083-10084): an entry with a pending op,
// or one that is not there and is not a delete marker, nor, in the ordered
// listing, a common prefix.
func needsDiskCheck(e *rgwcls.DirEntry, ordered bool) bool {
	absent := !e.Exists && e.Flags&rgwcls.FlagDeleteMarker == 0
	if ordered {
		absent = absent && e.Flags&rgwcls.FlagCommonPrefix == 0
	}
	return absent || len(e.PendingMap) > 0
}

// indexShards opens the bucket's index pool and names the shards of its
// current index generation, as open_bucket_index does; a bucket without an
// id is radosgw's -EIO (svc_bi_rados.cc:83-86 at v19.2.6, :91-94 at v20.2.4).
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

// bucketList runs one bucket_list on the shard oid and returns the reply and
// the version the read returned. A shard the OSD does not find is radosgw's
// -ENOENT, NoSuchKey, and a reply that does not decode its -EIO.
func (s *Store) bucketList(ctx context.Context, pool radosclient.Pool, oid string, l rgwcls.ListOp) (rgwcls.ListRet, uint64, error) {
	rop := radosclient.NewReadOp()
	r := rgwcls.BucketList(rop, l, s.release)
	ver, err := pool.Read(ctx, oid, rop, radosclient.OpFlagNone)
	if err != nil {
		return rgwcls.ListRet{}, 0, fmt.Errorf("listing index shard %s: %w", oid, op.FromRADOS(err, op.ScopeObject))
	}
	ret, err := r.Result()
	if err != nil {
		return rgwcls.ListRet{}, 0, fmt.Errorf("%w: listing index shard %s: %w", op.ErrUnknown, oid, err)
	}
	return ret, ver, nil
}

// checkDiskState is check_disk_state (:10323-10475): it reconciles the
// index entry ent, found on the shard oid, with the object's head and
// records the suggestion that repairs the shard under updates[oid]. keep is
// false when the head is gone and the entry is not a delete marker: the
// suggestion removes it, with ver set to indexVer. Otherwise the entry takes
// the head's size, accounted size, mtime, etag, content type, storage class,
// owner, appendability, Main category, data pool and epoch, write tag and
// existence, and the suggestion updates it, and the multipart parts its
// manifest names leave the index, as sweepParts says. Either way the entry
// leaves its pending ops behind. A head check that fails fails the listing,
// but one the object store cannot make yet keeps the entry as it is with no
// suggestion.
// A multipart upload's meta entry is kept as it is too: radosgw checks it in
// the data-extra pool, which the object store does not read
// (docs/exclusions.md, "A pending multipart upload's index entry is not
// reconciled by a listing"). radosgw picks that pool for every raw name
// MultipartMetaFilter accepts (:10341-10344), a plain object named like
// "a.b.meta" too, which it then fails to find; rgw-go checks a plain object
// in the data pool whatever its name (docs/exclusions.md, "A plain object
// whose name ends in .meta is checked where it is stored").
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
	ent.PendingMap = nil
	if ent.Flags&rgwcls.FlagDeleteMarker == 0 && !st.Exists {
		ent.Ver = indexVer
		updates[oid] = append(updates[oid], rgwcls.Suggestion{Op: rgwcls.SuggestRemove, Log: logOp, Entry: *ent})
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
	updates[oid] = append(updates[oid], rgwcls.Suggestion{Op: rgwcls.SuggestUpdate, Log: logOp, Entry: *ent})
	return true, nil
}

// sweepParts is check_disk_state's walk of an existing head's manifest
// (:10413-10429; v20.2.4 :11337-11353): every stripe in the multipart
// namespace, a part's head, has its index entry completed as deleted with
// delete_obj_index, hashed on the part's own name as raw_obj_to_obj leaves
// it (svc_tier_rados.h:131-143), though the part writer indexed it under the
// upload's (docs/ceph-upstream-bugs.md, "check_disk_state removes a
// multipart part's index entry from the wrong shard"). The walk stops with
// denc.ErrMalformed at a step that does not move forward and with
// meta.ErrTooManyStripes past meta.MaxWalkStripes stripes, where radosgw's
// never ends.
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

// blStr is rgw_bl_str (rgw_common.h:2063-2071 at v19.2.6): the attr's bytes
// with trailing NULs trimmed.
func blStr(b []byte) string { return strings.TrimRight(string(b), "\x00") }

// aclOwner is RGWRados::decode_policy (:1754-1768), which decodes only the
// owner of the head's ACL: an ACL that does not decode leaves the owner
// empty with a warning, as no ACL does.
func aclOwner(ctx context.Context, b []byte, rec *op.BucketRecord, key meta.ObjKey) acl.Owner {
	if b == nil {
		return acl.Owner{}
	}
	d := denc.NewDecoder(b)
	h := d.BeginStructLegacy(2, 2, 2, 0)
	o := acl.DecodeOwner(d)
	d.EndStruct(h)
	if err := d.Err(); err != nil {
		slog.WarnContext(ctx, "could not decode policy for object", slog.String("bucket", rec.Info.Bucket.Name),
			slog.String("key", key.Name), slog.Any("error", err))
		return acl.Owner{}
	}
	return o
}

// parseIndexKey is rgw_obj_key::parse_index_key (rgw_obj_types.h:125-146 at
// v19.2.6 and v20.2.4), which rgw_obj_key's constructor from an index key
// runs: like ParseIndexKeyName but with the instance left in the namespace,
// and a name it cannot split kept whole.
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
// blindly, as radosgw sends them with aio_operate (:9898-9912), guarded
// against resharding on Tentacle as v20.2.4 guards them (:10819-10832;
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

// listObjectsUnordered is list_objects_unordered (:2181-2330): rounds of
// listUnordered with a read-ahead of up to 100 entries past the page, while
// the shards have more and the page is not over. The next marker follows
// every parsable entry read while the page has room, before the entry is
// filtered; only the page's last counted entry can leave it, since a
// listing ends truncated only once the page is full. An unparsable name, an
// entry that is not visible unless versions are listed, one outside p.NS and
// one outside the prefix are then skipped. No common prefixes are formed.
func (s *Store) listObjectsUnordered(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	maxN := min(max(p.MaxKeys, 0), listAbsoluteMax)
	readAhead := uint32(maxN + min(maxN, listUnorderedReadAhead)) //nolint:gosec // at most 2 * listAbsoluteMax
	cur := rgwcls.ObjKey{Name: meta.ObjKey{Name: p.Marker, NS: p.NS}.IndexKeyName()}
	prefix := meta.ObjKey{Name: p.Prefix, NS: p.NS}.IndexKeyName()
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
			if ok && count < maxN {
				// next_marker.set(index_key) parses the raw name and keeps
				// the name it held when the parse fails.
				res.NextMarker = key.Name
			}
			if !ok {
				slog.ErrorContext(ctx, "could not parse object name", slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", e.Key.Name))
				continue
			}
			key.Instance = e.Key.Instance
			if (!p.ListVersions && !visible(e)) || key.NS != p.NS || !strings.HasPrefix(key.Name, p.Prefix) {
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
		res.NextMarker = ""
	}
	return res, nil
}

// listUnordered is cls_bucket_list_unordered (:9965-10155): the shards one
// after another, from the one startAfter hashes to (a multipart upload's
// meta entry by its upload's key, parse_index_hash_source) or from the
// first, each read with bucket_list and no delimiter until numEntries are
// listed and one more is seen, which makes the listing truncated. An entry
// is checked against its head as listOrdered checks one, the removals
// carrying the shard read's version, and the suggestions are sent. last is
// the last entry listed, valid when any is. A start key that does not parse
// is radosgw's -EINVAL.
func (s *Store) listUnordered(ctx context.Context, rec *op.BucketRecord, startAfter rgwcls.ObjKey, prefix string,
	numEntries uint32, listVersions bool,
) (entries []rgwcls.DirEntry, truncated bool, last rgwcls.ObjKey, err error) {
	pool, oids, err := s.indexShards(ctx, rec)
	if err != nil {
		return nil, false, rgwcls.ObjKey{}, err
	}
	numShards := uint32(len(oids)) //nolint:gosec // a shard count fits a u32
	var shard uint32
	if startAfter.Name != "" {
		k, ok := meta.ParseIndexKeyName(startAfter.Name)
		if !ok {
			return nil, false, rgwcls.ObjKey{}, fmt.Errorf("%w: invalid start marker %q", op.ErrInvalidArgument, startAfter.Name)
		}
		if k.Name != "" {
			src := k.Name
			if k.NS == meta.NSMultipart {
				if src, ok = indexHashSource(k.Name); !ok {
					return nil, false, rgwcls.ObjKey{}, fmt.Errorf("%w: no index hash source in %q", op.ErrInvalidArgument, k.Name)
				}
			}
			shard, _ = meta.IndexShard(src, numShards)
		}
	}
	indexVer := rgwcls.EntryVer{Pool: pool.ID()}
	updates := map[string][]rgwcls.Suggestion{}
	marker := startAfter
	var count uint32
shards:
	for count <= numEntries && shard < numShards {
		oid := oids[shard]
		ret, ver, err := s.bucketList(ctx, pool, oid, rgwcls.ListOp{StartObj: marker, NumEntries: numEntries, FilterPrefix: prefix, ListVersions: listVersions})
		if err != nil {
			return nil, false, rgwcls.ObjKey{}, err
		}
		indexVer.Epoch = ver
		for _, name := range slices.Sorted(maps.Keys(ret.Dir.Entries)) {
			ent := ret.Dir.Entries[name]
			keep := true
			if needsDiskCheck(&ent, false) {
				if keep, err = s.checkDiskState(ctx, rec, indexVer, &ent, updates, oid); err != nil {
					return nil, false, rgwcls.ObjKey{}, err
				}
			}
			if !keep {
				marker = ent.Key
				continue
			}
			if count >= numEntries {
				truncated = true
				break shards
			}
			marker, last = ent.Key, ent.Key
			entries = append(entries, ent)
			count++
		}
		if !ret.IsTruncated {
			shard++
			marker = rgwcls.ObjKey{}
		}
	}
	s.suggest(ctx, pool, updates)
	return entries, truncated, last, nil
}

// indexHashSource is parse_index_hash_source (:9951-9962): a multipart meta
// name up to its second-to-last dot, the upload's key its entries hash by;
// ok is false when either dot is missing or first.
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
