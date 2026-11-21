package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A version whose head and instance entry stay while its list entry is gone
// is in no listing, and lifecycle, the listing and DeleteBucket's emptiness
// check find versions through the listing alone, as radosgw's do
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L365-L380 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5259-L5304 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L365-L380 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5723-L5768 at v20.2.4).
// rgw-go's replay leaves a version so when it refuses a removal its writer
// guarded or keeps another writer's version for its object lock. It records
// each such version in the bucket's unlisted-version object before the
// replay's head op, so before the trim that drops the removal from the OLH
// log: a gateway that stops after that record and before the relist leaves
// the version named, and one that stops before it leaves the removal logged
// for a later replay. Lifecycle's pass, DeleteBucket and the purge read the
// record and list each version again (relistUnlisted). radosgw writes no such
// record and neither reads nor removes it (docs/rados-format-opportunities.md,
// "An rgw-go object naming the versions a replay left unlisted").

// unlistedOIDPrefix starts the name of a bucket's unlisted-version object,
// whose omap names each version a replay left unlisted. It lives in the
// bucket's index pool, beside the shards, under a name no shard or other
// radosgw object takes.
const unlistedOIDPrefix = "rgw-go.unlisted."

// unlistedSettle is how old a version's record is before lifecycle or
// DeleteBucket acts on it, and how long another reader's claim on it holds:
// relistBackstop, the bound on the writer's own relist up to its link. It
// keeps a reader from relisting while that relist or the writer's check after
// its replay (checkRemoval) usually still runs, measured from the recording
// gateway's clock on the reader's, so clocks that disagree by more than a
// few seconds shift it. It guarantees nothing: the backstop counts from the
// relist's start, after the record, and the relist's replay after its link
// runs to its end. Two relists of one version are safe without it, as each
// links nothing once the version is listed (relistVersion, relistBelow) and
// resets nothing once another's link has moved the OLH entry (resetCurrent).
const unlistedSettle = relistBackstop

// unlistedPage is the records one read of the object takes.
const unlistedPage uint64 = 1000

// unlistedPrunePerRecord is the records a prune examines after the
// object's prune cursor for each record its write added (pruneUnlisted).
const unlistedPrunePerRecord = 2

// unlistedCursorAttr is the xattr of the unlisted-version object holding the
// last key a prune examined.
const unlistedCursorAttr = "rgw-go.prune-cursor"

// unlistedNote is a version's record: when it was written, the OLH tag the
// replay read the log under, which relistVersion compares with the OLH
// entry's to find an entry cleared and made again since (remade), and the
// claim of the reader relisting it, while one does.
type unlistedNote struct {
	Time   time.Time      `json:"time"`
	OLHTag []byte         `json:"olh_tag,omitempty"`
	Claim  *unlistedClaim `json:"claim,omitempty"`
}

// unlistedClaim is a reader's claim on a record: a token of its own and when
// it claimed.
type unlistedClaim struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// unlistedOID is the unlisted-version object of info's bucket.
func unlistedOID(info *meta.BucketInfo) string { return unlistedOIDPrefix + info.Bucket.ID }

// unlistedEscaper percent-encodes the bytes a record key cannot hold as they
// are: the escape itself, the separator, and NUL, at which librados cuts an
// omap listing's start key (docs/cgo-limitations.md, "go-ceph cuts object
// names at a NUL byte").
var unlistedEscaper = strings.NewReplacer("%", "%25", "/", "%2F", "\x00", "%00")

// unlistedKey is key's omap key in the object: its index key name and its
// instance, each escaped (unlistedEscaper), joined by a slash.
func unlistedKey(key meta.ObjKey) string {
	return unlistedEscaper.Replace(key.IndexKeyName()) + "/" + unlistedEscaper.Replace(key.Instance)
}

// parseUnlistedKey is the version unlistedKey names, and whether k is one.
func parseUnlistedKey(k string) (meta.ObjKey, bool) {
	name, instance, ok := strings.Cut(k, "/")
	if !ok || name == "" || strings.Contains(instance, "/") {
		return meta.ObjKey{}, false
	}
	n, nerr := url.PathUnescape(name)
	i, ierr := url.PathUnescape(instance)
	if nerr != nil || ierr != nil {
		return meta.ObjKey{}, false
	}
	return indexToObjKey(rgw.ObjKey{Name: n, Instance: i}), true
}

// recordUnlisted records keys, versions of rec's bucket a replay leaves
// unlisted, olhTag being the OLH tag it read their removals under, in one
// write that creates the object when it is missing. A version recorded again
// takes the newer record. The replay prunes the object once its head op and
// trim are done (pruneUnlisted).
func (s *Store) recordUnlisted(ctx context.Context, rec *op.BucketRecord, keys []meta.ObjKey, olhTag []byte) error {
	pool, err := s.indexPool(ctx, &rec.Info)
	if err != nil {
		return err
	}
	v, err := json.Marshal(unlistedNote{Time: s.now(), OLHTag: olhTag})
	if err != nil {
		return err
	}
	kv := make(map[string][]byte, len(keys))
	for _, k := range keys {
		kv[unlistedKey(k)] = v
	}
	w := radosclient.NewWriteOp()
	w.Create(false)
	w.OmapSet(kv)
	if _, err := pool.Write(ctx, unlistedOID(&rec.Info), w, radosclient.OpFlagNone); err != nil {
		return fmt.Errorf("recording %d unlisted versions of bucket %s: %w", len(keys), rec.Info.Bucket.Name, err)
	}
	return nil
}

// pruneUnlisted bounds the object by the versions still left unlisted, once
// the replay that recorded keys has landed its head op and trim: it examines
// unlistedPrunePerRecord records for each of keys after the key
// unlistedCursorAttr names, wrapping to the first, but for keys' own, and
// removes each whose version is not stored and unlisted (storedUnlisted),
// then moves the cursor to the last it examined. A write that adds k records
// has its prune examine 2k, so a cycle of the cursor through the object
// removes every record stale when it began while adding at most half as
// many, and the object holds at most about twice the records of versions
// still unlisted. For k records it costs three reads of the object, up to
// 6k index or head reads, and up to 2k+1 writes. A failure is logged and
// changes no answer: the readers remove stale records too.
func (s *Store) pruneUnlisted(ctx context.Context, rec *op.BucketRecord, keys []meta.ObjKey) {
	attrs := []any{slog.String("bucket", rec.Info.Bucket.Name)}
	pool, err := s.indexPool(ctx, &rec.Info)
	if err != nil {
		slog.WarnContext(ctx, "could not open the index pool to prune a bucket's unlisted versions", append(attrs, slog.Any("error", err))...)
		return
	}
	oid := unlistedOID(&rec.Info)
	written := make(map[string]bool, len(keys))
	for _, k := range keys {
		written[unlistedKey(k)] = true
	}
	batch := min(unlistedPrunePerRecord*uint64(len(written)), unlistedPage)
	rop := radosclient.NewReadOp()
	xr := rop.GetXattrs()
	if _, err = pool.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
		slog.WarnContext(ctx, "could not read the prune cursor of a bucket's unlisted versions", append(attrs, slog.Any("error", err))...)
		return
	}
	cursor := string(xr.Xattrs[unlistedCursorAttr])
	examined, err := s.unlistedAfter(ctx, pool, oid, cursor, batch)
	if err == nil && uint64(len(examined)) < batch && cursor != "" {
		var more []unlistedEntry
		more, err = s.unlistedAfter(ctx, pool, oid, "", batch-uint64(len(examined)))
		for _, e := range more {
			if !slices.ContainsFunc(examined, func(x unlistedEntry) bool { return x.k == e.k }) {
				examined = append(examined, e)
			}
		}
	}
	if err != nil {
		slog.WarnContext(ctx, "could not read a bucket's unlisted versions to prune them", append(attrs, slog.Any("error", err))...)
		return
	}
	for _, e := range examined {
		if written[e.k] {
			continue
		}
		key, ok := parseUnlistedKey(e.k)
		if ok {
			kept, kerr := s.storedUnlisted(ctx, rec, key)
			if kerr != nil || kept {
				continue
			}
		}
		s.removeRecord(ctx, rec, pool, oid, e.k, e.v)
	}
	if len(examined) == 0 {
		return
	}
	w := radosclient.NewWriteOp()
	w.AssertExists()
	w.SetXattr(unlistedCursorAttr, []byte(examined[len(examined)-1].k))
	if _, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		slog.WarnContext(ctx, "could not move the prune cursor of a bucket's unlisted versions", append(attrs, slog.Any("error", err))...)
	}
}

// unlistedEntry is one record as read: its omap key and value.
type unlistedEntry struct {
	k string
	v []byte
}

// unlistedAfter reads up to n records after key after, in key order.
func (s *Store) unlistedAfter(ctx context.Context, pool radosclient.Pool, oid, after string, n uint64) ([]unlistedEntry, error) {
	rop := radosclient.NewReadOp()
	res := rop.OmapGetVals(after, "", n)
	if _, err := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
		return nil, err
	}
	out := make([]unlistedEntry, 0, len(res.Values))
	for _, k := range slices.Sorted(maps.Keys(res.Values)) {
		out = append(out, unlistedEntry{k: k, v: res.Values[k]})
	}
	return out, nil
}

// relistUnlisted lists again each version rec's unlisted-version object
// names whose record is settle old, and returns how many it listed. For each
// it reads the version's head, the OLH head for the null version, and its
// instance entry (storedUnlisted). The record of a version listed, gone, or
// left without data, which no link can list, is removed. One still stored
// and unlisted is claimed (claimUnlisted), skipped while another reader's
// claim is younger than settle, then goes through the relist a refused
// removal runs (runRelist): relistNull for the null version, relistVersion
// for a named one, under the OLH tag its record carries. The record of a
// version the relist listed is removed; one whose relist did not list it
// gets its unclaimed record back for the next reader, logged; both run, with
// the read before them, whatever ctx does, as the relist does. Each removal or
// rewrite compares the record as last read or written, so one a later
// replay wrote in between stays. A record that does not parse is removed,
// logged. No relist starts once stop reports true or ctx has ended. A purge
// passes settle 0, as it removes every version.
func (s *Store) relistUnlisted(ctx context.Context, rec *op.BucketRecord, settle time.Duration, stop func() bool) (int, error) {
	pool, err := s.indexPool(ctx, &rec.Info)
	if err != nil {
		return 0, err
	}
	oid := unlistedOID(&rec.Info)
	listed := 0
	for after := ""; ; {
		page, err := s.unlistedAfter(ctx, pool, oid, after, unlistedPage)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return listed, nil
		case err != nil:
			return listed, fmt.Errorf("reading the unlisted versions of bucket %s: %w", rec.Info.Bucket.Name, err)
		}
		for _, e := range page {
			if ctx.Err() != nil || stop() {
				return listed, ctx.Err()
			}
			if s.settleUnlisted(ctx, rec, pool, oid, e, settle) {
				listed++
			}
		}
		if uint64(len(page)) < unlistedPage {
			return listed, nil
		}
		after = page[len(page)-1].k
	}
}

// settleUnlisted acts on record e (relistUnlisted), reporting whether its
// version was listed again.
func (s *Store) settleUnlisted(ctx context.Context, rec *op.BucketRecord, pool radosclient.Pool, oid string, e unlistedEntry, settle time.Duration) bool {
	attrs := []any{slog.String("bucket", rec.Info.Bucket.Name)}
	key, ok := parseUnlistedKey(e.k)
	var note unlistedNote
	if ok {
		ok = json.Unmarshal(e.v, &note) == nil
	}
	if !ok {
		slog.WarnContext(ctx, "removing a record of an unlisted version that does not parse", append(attrs, slog.String("record", e.k))...)
		s.removeRecord(ctx, rec, pool, oid, e.k, e.v)
		return false
	}
	attrs = append(attrs, slog.String("key", key.Name), slog.String("instance", key.Instance))
	now := s.now()
	if now.Sub(note.Time) < settle || note.Claim != nil && now.Sub(note.Claim.At) < settle {
		return false
	}
	kept, err := s.storedUnlisted(ctx, rec, key)
	switch {
	case err != nil:
		slog.WarnContext(ctx, "could not read a version a replay left unlisted; the next pass reads it again", append(attrs, slog.Any("error", err))...)
		return false
	case !kept:
		s.removeRecord(ctx, rec, pool, oid, e.k, e.v)
		return false
	}
	claimed, ok := s.claimUnlisted(ctx, rec, pool, oid, e, note)
	if !ok {
		return false
	}
	relist := s.relistNull
	if key.Instance != "" {
		relist = func(ctx context.Context, clock *relistClock, rec *op.BucketRecord, v meta.ObjKey) error {
			return s.relistVersion(ctx, clock, rec, v, note.OLHTag)
		}
	}
	// The relist runs detached, so its outcome is read and the claim released
	// or the record removed whatever ctx does: a reader whose client left
	// must not hold its claim for the settle age.
	dctx := context.WithoutCancel(ctx)
	rerr := s.runRelist(ctx, rec, key, relist)
	kept, err = s.storedUnlisted(dctx, rec, key)
	switch {
	case err != nil, kept:
		slog.WarnContext(ctx, "listing again a version a replay left unlisted did not list it; the next pass tries again",
			append(attrs, slog.Any("error", errors.Join(rerr, err)))...)
		s.swapRecord(dctx, rec, pool, oid, e.k, claimed, e.v)
		return false
	}
	slog.InfoContext(ctx, "listed again a version a replay left unlisted", attrs...)
	s.removeRecord(dctx, rec, pool, oid, e.k, claimed)
	return true
}

// claimUnlisted claims record e, whose value parsed is note, for this reader:
// it rewrites the record with a claim of a fresh token and the time, under a
// compare of the value read, and returns the claimed value. A compare that
// fails, another reader's claim or a later replay's record landing since the
// read, reports false, and the reader leaves the record.
func (s *Store) claimUnlisted(ctx context.Context, rec *op.BucketRecord, pool radosclient.Pool, oid string, e unlistedEntry, note unlistedNote) ([]byte, bool) {
	note.Claim = &unlistedClaim{By: meta.NewOLHTag(), At: s.now()}
	claimed, err := json.Marshal(note)
	if err != nil {
		return nil, false
	}
	return claimed, s.swapRecord(ctx, rec, pool, oid, e.k, e.v, claimed)
}

// swapRecord rewrites record k from from to to, comparing from, and reports
// whether it did. A compare that fails or a record object gone is no error.
func (s *Store) swapRecord(ctx context.Context, rec *op.BucketRecord, pool radosclient.Pool, oid, k string, from, to []byte) bool {
	w := radosclient.NewWriteOp()
	w.AssertExists()
	w.OmapCmp(k, radosclient.CmpEQ, from)
	w.OmapSet(map[string][]byte{k: to})
	_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
	if err != nil && !errors.Is(err, radosclient.ErrCanceled) && !errors.Is(err, radosclient.ErrNotFound) {
		slog.WarnContext(ctx, "could not rewrite the record of an unlisted version", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
	return err == nil
}

// removeRecord removes record k while it holds v.
func (s *Store) removeRecord(ctx context.Context, rec *op.BucketRecord, pool radosclient.Pool, oid, k string, v []byte) {
	w := radosclient.NewWriteOp()
	w.OmapCmp(k, radosclient.CmpEQ, v)
	w.OmapRmKeys([]string{k})
	if _, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrCanceled) && !errors.Is(err, radosclient.ErrNotFound) {
		slog.WarnContext(ctx, "could not remove the record of a version listed again or gone; the next pass reads it again",
			slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
}

// storedUnlisted reports whether key's version, of rec's bucket, is stored
// and unlisted: its head is there, the OLH head holding the null version's
// data, and its instance entry names no list entry (unlistedVersion).
func (s *Store) storedUnlisted(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (bool, error) {
	st, err := s.readOLHHead(ctx, rec, key)
	if err != nil || !st.Exists {
		return false, err
	}
	return s.unlistedVersion(ctx, rec, key, st)
}

// RelistUnlisted implements op.BucketStore for a purge: every version rec's
// unlisted-version object names is listed again whatever its record's age,
// so that the purge's listing deletes it (relistUnlisted).
func (s *Store) RelistUnlisted(ctx context.Context, rec *op.BucketRecord) (int, error) {
	return s.relistUnlisted(ctx, rec, 0, func() bool { return false })
}

// removeUnlistedRecords removes info's unlisted-version object with its
// bucket, logging a failure: what it leaves is an object no request reads.
func (s *Store) removeUnlistedRecords(ctx context.Context, info *meta.BucketInfo) {
	pool, err := s.indexPool(ctx, info)
	if err == nil {
		w := radosclient.NewWriteOp()
		w.Remove()
		_, err = pool.Write(ctx, unlistedOID(info), w, radosclient.OpFlagNone)
	}
	if err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		slog.WarnContext(ctx, "could not remove the removed bucket's record of unlisted versions", slog.String("bucket", info.Bucket.Name), slog.Any("error", err))
	}
}
