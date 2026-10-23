package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// lcPageSize is the entries a lifecycle listing page asks for
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L379-L388 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L379-L388 at v20.2.4).
const lcPageSize = 1000

// lcTierArchive is the tier type of an archive zone, which runs only the
// rules that carry the ArchiveZone flag.
const lcTierArchive = "archive"

// lcPass is the bucket pass the lifecycle workers run, bucket_lc_process
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1679-L1830 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1735-L1886 at v20.2.4),
// for the buckets its claim predicate admits.
type lcPass struct {
	s *Store
	// notifier is where an action's bucket notification goes.
	notifier lifecycle.Notifier
	// hook, when set, runs at each lcStep of an action on key; nil in a
	// running gateway.
	hook func(ctx context.Context, at lcStep, key meta.ObjKey)
}

func newLCPass(s *Store) *lcPass {
	return &lcPass{s: s, notifier: lifecycle.NopNotifier{}}
}

// claimable is the claim predicate, on the bucket record and its
// user.rgw.lc read under the shard's lock. It admits what radosgw's pass
// would claim and rgw-go's applies in full: an entry whose bucket is stale,
// which the post-state removes, a bucket without a configuration, which
// bucket_lc_process completes, one whose configuration does not decode,
// which it fails, and one refusal leaves unrefused. A refused bucket is
// logged, once per pass, since a pass asks once for each entry.
func (p *lcPass) claimable(ctx context.Context, _ *lcWorker, key string) (bool, error) {
	rec, err := p.s.lcLoadBucket(ctx, key)
	if errors.Is(err, errLCStaleEntry) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	b, configured := rec.Attrs[op.AttrLC]
	if !configured {
		return true, nil
	}
	conf, err := lifecycle.Decode(b)
	if err != nil {
		return true, nil //nolint:nilerr // bucket_lc_process fails the bucket once it is claimed
	}
	if reason, rule := p.refusal(rec, conf); reason != "" {
		slog.InfoContext(ctx, "lifecycle leaves a bucket for radosgw's worker",
			slog.String("bucket", key), slog.String("reason", reason), slog.String("rule", rule))
		return false, nil
	}
	return true, nil
}

// refusal is why rgw-go may not claim rec, whose configuration is conf,
// and the ID of the rule that makes it so, if one does; the reason is ""
// when it may. A rule that transitions to a storage class the zonegroup's
// placement tiers to a cloud is never applied: its claim, or a head moved
// past it, would hide it from radosgw's worker, which transitions it, for
// the day (docs/exclusions.md, "An rgw-go-only zone never processes a bucket
// whose rules transition to a cloud tier"). A bucket with a transition rule,
// and one that was ever versioned with a rule that expires noncurrent
// versions, are left to radosgw's worker too, as the bucket pass applies
// neither (docs/exclusions.md, "rgw-go's lifecycle worker leaves buckets with
// rules it does not apply to radosgw's"). Only the rules the zone runs
// count, as is_valid_op and zone_check choose them (lifecycle.Op.Applies).
func (p *lcPass) refusal(rec *op.BucketRecord, conf lifecycle.Configuration) (reason, rule string) {
	archive := p.s.zone.Zone.TierType == lcTierArchive
	ops := conf.PrefixMap()
	for i := range ops {
		o := &ops[i].Op
		if !o.Applies(archive) {
			continue
		}
		for _, ts := range []map[string]lifecycle.TransitionAction{o.Transitions, o.NoncurTransitions} {
			for _, t := range ts {
				if p.s.lcCloudTier(rec, t.StorageClass) {
					return "rule transitions to a cloud tier", o.ID
				}
			}
		}
	}
	for i := range ops {
		o := &ops[i].Op
		switch {
		case !o.Applies(archive):
		case len(o.Transitions)+len(o.NoncurTransitions) > 0:
			return "rule transitions objects", o.ID
		case rec.Info.Versioned() && o.NoncurExpiration > 0:
			return "rule expires noncurrent versions", o.ID
		}
	}
	return "", ""
}

// lcCloudTier reports whether class names a cloud tier of rec's placement
// target in the zonegroup, as the transition looks it up: the target the
// bucket's placement rule names and its tier target for class, of either
// cloud type, which v20.2.4's is_tier_type_s3 takes and of which v19.2.7
// tiers cloud-s3 alone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1479-L1488 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1525-L1534 at v20.2.4;
// get_placement_tier,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L5136-L5158 at v20.2.4).
func (s *Store) lcCloudTier(rec *op.BucketRecord, class string) bool {
	target, ok := s.zone.ZoneGroup.PlacementTargets[rec.Info.PlacementRule.Name]
	if !ok {
		return false
	}
	tier, ok := target.TierTargets[class]
	return ok && tier.IsTierTypeS3()
}

// run is bucket_lc_process on c's bucket until stopAt: rgwlc_skip_bucket_step
// succeeds before anything is read; a stale entry is errLCStaleEntry, a
// bucket without user.rgw.lc complete, and one whose configuration does not
// decode failed. A bucket the predicate refuses now, given new rules since
// its claim, fails, so either worker takes it again at its next pass. Then
// each rule of the prefix map, in order (lcBucket.rule), and the abort of
// expired multipart uploads (lcBucket.multipart), each page on the bucket's
// record as atPage reads it again.
func (p *lcPass) run(ctx context.Context, w *lcWorker, c lcClaim, stopAt time.Time) error {
	s := p.s
	if s.opts.lcSkipBucketStep {
		return nil
	}
	rec, err := s.lcLoadBucket(ctx, c.entry.Bucket)
	if err != nil {
		return err
	}
	attr, configured := rec.Attrs[op.AttrLC]
	if !configured {
		return nil
	}
	conf, err := lifecycle.Decode(attr)
	if err != nil {
		return fmt.Errorf("lifecycle configuration of %s: %w", c.entry.Bucket, err)
	}
	if reason, rule := p.refusal(rec, conf); reason != "" {
		return fmt.Errorf("bucket %s refused since its claim: %s %q", c.entry.Bucket, reason, rule)
	}
	b := &lcBucket{
		p: p, w: w, c: c, attr: attr, conf: conf, stopAt: stopAt,
		versioned: rec.Info.Versioned(), enabled: rec.Info.VersioningEnabled(),
	}
	archive := s.zone.Zone.TierType == lcTierArchive
	ops := conf.PrefixMap()
	for i := range ops {
		if !ops[i].Op.Applies(archive) {
			continue
		}
		if stop, err := b.rule(ctx, &ops[i]); stop {
			return err
		}
	}
	return b.multipart(ctx, ops)
}

// lcBucket is the pass on one claimed bucket, as the bucket was when the
// pass began: its user.rgw.lc attr, conf decoded, whether it was ever
// versioned (versioned), and whether its versioning was enabled rather than
// suspended (enabled).
type lcBucket struct {
	p                  *lcPass
	w                  *lcWorker
	c                  lcClaim
	attr               []byte
	conf               lifecycle.Configuration
	stopAt             time.Time
	versioned, enabled bool

	// mu guards changed, the first change to the bucket an action's record
	// check found, which ends the pass once the rule's pool drains.
	mu      sync.Mutex
	changed error
}

// atPage is the check before each listing page: ctx's end and the claim
// re-read, which finds the claim lost or fails, stop the bucket with their
// error; thread_stop_at stops it with nil, as worker_should_stop's return 0
// completes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L881-L884 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L902-L905 at v20.2.4).
// radosgw checks the stop at each rule and every 100 entries, rgw-go at each
// page, every rule's first included. Then the bucket's record is read again
// (current), and returned for the page's entries to act on; its error stops
// the bucket.
func (b *lcBucket) atPage(ctx context.Context) (*op.BucketRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	if err := b.w.claimHeld(ctx, b.c); err != nil {
		return nil, true, err
	}
	if b.w.now().Unix() > b.stopAt.Unix() {
		slog.InfoContext(ctx, "lifecycle bucket pass reached its stop time", slog.String("bucket", b.c.entry.Bucket), slog.Int("worker", b.w.ix))
		return nil, true, nil
	}
	rec, err := b.current(ctx)
	if err != nil {
		return nil, true, err
	}
	return rec, false, nil
}

// errLCChanged is current's finding that a bucket no longer admits its
// pass.
var errLCChanged = errors.New("bucket changed during its lifecycle pass")

// current reads the bucket's record again, through the metadata cache, before
// each page and each action. A bucket that no longer loads under the entry's
// marker is errLCStaleEntry, as run's own load is. A bucket whose user.rgw.lc
// is no longer the pass's, whose versioning was enabled or suspended since
// the pass began, or that the claim predicate now refuses, its rules now
// transitioning, is errLCChanged, so that nothing acts on a record the bucket
// no longer holds: a delete that would write a marker taken as an
// unversioned removal, or a delete marker written as the null one, unguarded.
// Any other error is the read's own. radosgw acts on the record it loaded at
// the pass's start until the pass ends (docs/exclusions.md, "Lifecycle stops
// a bucket's pass when the bucket changes under it").
func (b *lcBucket) current(ctx context.Context) (*op.BucketRecord, error) {
	rec, err := b.p.s.lcLoadBucket(ctx, b.c.entry.Bucket)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(rec.Attrs[op.AttrLC], b.attr) {
		return nil, fmt.Errorf("%w: the lifecycle configuration of %s is no longer the pass's", errLCChanged, b.c.entry.Bucket)
	}
	if rec.Info.Versioned() != b.versioned || rec.Info.VersioningEnabled() != b.enabled {
		return nil, fmt.Errorf("%w: the versioning of %s is no longer the pass's", errLCChanged, b.c.entry.Bucket)
	}
	if reason, rule := b.p.refusal(rec, b.conf); reason != "" {
		return nil, fmt.Errorf("%w: %s now refused: %s %q", errLCChanged, b.c.entry.Bucket, reason, rule)
	}
	return rec, nil
}

// noteChange keeps err, the first failure of an action's record check: the
// bucket changed or gone, or the read failed.
func (b *lcBucket) noteChange(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.changed == nil {
		b.changed = err
	}
}

func (b *lcBucket) change() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.changed
}

// rule is one rule's walk, LCObjsLister
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L351-L446 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L351-L454 at v20.2.4):
// the bucket's objects under the rule's prefix, unordered, with versions on
// a bucket that was ever versioned as list_versions takes bucket->versioned()
// there, each page's entries handed to the work pool with the lister's state
// in the order the listing gives them. A versions listing is paged by
// lcVersionPager. The pool drains before the rule ends, however it ends. A
// later page that fails ends the rule alone, as radosgw logs it and goes
// on. A change an action found ends the bucket with it, when the walk did
// not end on an error of its own. It reports whether the bucket stops, with
// the error it stops on.
func (b *lcBucket) rule(ctx context.Context, po *lifecycle.PrefixOp) (bool, error) {
	pool := lcWorkPool(b.p.s.opts.lcMaxWPWorker)
	var lister lifecycle.Lister
	p := op.ListObjectsParams{Prefix: po.Prefix, AllowUnordered: true, ListVersions: b.versioned}
	fresh := lcAll
	next := lcByName
	if b.versioned {
		vp := &lcVersionPager{}
		fresh, next = vp.fresh, vp.next
	}
	stop, err := b.walk(ctx, p, false, func(rec *op.BucketRecord, res *op.ListObjectsResult) {
		listed, at := fresh(res.Entries)
		entries, states := lcStates(&lister, res.Entries, listed, at, res.Truncated)
		for i := range listed {
			e, le, st := listed[i], entries[i], states[i]
			pool.Go(func() error {
				b.entry(ctx, rec, &po.Op, e, le, st)
				return nil
			})
		}
	}, next)
	lcDrain(pool)
	if cerr := b.change(); cerr != nil && err == nil {
		return true, cerr
	}
	return stop, err
}

// lcAll is every entry of es, each at its own index, as a listing without
// versions hands them out.
func lcAll(es []op.ObjectEntry) (kept []op.ObjectEntry, at []int) {
	at = make([]int, len(es))
	for i := range at {
		at[i] = i
	}
	return es, at
}

// lcStates is the lister's entry and state of each entry kept of page,
// kept[i] being page[at[i]]. Earlier and PrevMtime come from l fed the kept
// entries alone, so that each version counts once however often the walk
// lists it; Next and HasNext come from the page as listed, as next_key_name
// reads it, so that an entry a dropped version of its name follows still
// has a next entry of its name, and a current delete marker an older
// version follows is never taken for one no version follows.
func lcStates(l *lifecycle.Lister, page, kept []op.ObjectEntry, at []int, truncated bool) ([]lifecycle.Entry, []lifecycle.State) {
	entries := make([]lifecycle.Entry, len(kept))
	for i := range kept {
		entries[i] = lcEntry(&kept[i])
	}
	states := l.Page(entries, truncated)
	if len(kept) == len(page) {
		return entries, states
	}
	all := make([]lifecycle.Entry, len(page))
	for i := range page {
		all[i] = lcEntry(&page[i])
	}
	var listed lifecycle.Lister
	whole := listed.Page(all, truncated)
	for i, j := range at {
		states[i].Next, states[i].HasNext = whole[j].Next, whole[j].HasNext
	}
	return entries, states
}

// walk lists p from its start in pages of lcPageSize, hands each page to
// page with the record atPage read before it, then waits
// rgw_lc_thread_delay, as radosgw's listings do, and lists the page next
// says, until a page is not truncated or next finds no progress. A page that
// finds no index stops the bucket complete, as radosgw's -ENOENT returns 0;
// the first page's other failure stops it failed, and so does a later page's
// with failLater, while without it a later page's failure ends the walk
// alone. It reports whether the bucket stops, with the error it stops on.
func (b *lcBucket) walk(ctx context.Context, p op.ListObjectsParams, failLater bool, page func(rec *op.BucketRecord, res *op.ListObjectsResult),
	next func(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams, res *op.ListObjectsResult) (op.ListObjectsParams, bool),
) (bool, error) {
	p.MaxKeys = lcPageSize
	for first := true; ; first = false {
		rec, stop, err := b.atPage(ctx)
		if stop {
			return true, err
		}
		res, err := b.p.s.ListObjects(ctx, rec, p)
		switch {
		case err != nil && (first || failLater) && errors.Is(err, radosclient.ErrNotFound):
			return true, nil
		case err != nil && (first || failLater):
			return true, fmt.Errorf("listing bucket %s under %q: %w", b.c.entry.Bucket, p.Prefix, err)
		case err != nil:
			slog.WarnContext(ctx, "lifecycle could not list a rule's next page; the rule ends", slog.String("bucket", b.c.entry.Bucket),
				slog.String("prefix", p.Prefix), slog.Any("error", err))
			return false, nil
		}
		page(rec, &res)
		if d := b.p.s.opts.lcThreadDelay; d > 0 {
			if err := b.w.sleep(ctx, d); err != nil {
				return true, err
			}
		}
		if !res.Truncated {
			return false, nil
		}
		q, progress := next(ctx, rec, p, &res)
		if !progress {
			slog.WarnContext(ctx, "lifecycle listing made no progress; the rule ends", slog.String("bucket", b.c.entry.Bucket),
				slog.String("prefix", p.Prefix), slog.String("marker", p.Marker), slog.String("instance", p.MarkerInstance))
			return false, nil
		}
		p = q
	}
}

// lcByName pages a listing without versions: the next page starts after
// NextMarker. The pass removes entries it listed, the marker's among them,
// but a listing without versions starts after the marker's own name whether
// or not its entry remains: the class lists the omap keys after it
// (encode_list_index_key,
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L363 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L413 at v20.2.4),
// on the shard the marker's name hashes to, so no entry after it is
// skipped. A NextMarker that is the marker already is no progress.
func lcByName(_ context.Context, _ *op.BucketRecord, p op.ListObjectsParams, res *op.ListObjectsResult) (op.ListObjectsParams, bool) {
	if res.NextMarker == p.Marker {
		return p, false
	}
	p.Marker = res.NextMarker
	return p, true
}

// lcVersionPager pages a rule's versions listing, which the pass acts on as
// it lists, so that each version under the prefix is handed out at most once
// a pass, in the listing's order; the lister's state then counts each version
// once, as one listing of the bucket would. The next page starts after the
// listing's next marker, copied back unchanged
// (op.ListObjectsResult.NextMarkerInstance), and fresh drops what it lists
// again: a page that ends on the null version starts after the null entry
// the key holds when the next page is listed, which is the name's first
// version once the null delete marker has replaced that version.
//
// radosgw starts each page after the key of the last entry it listed, name
// and instance
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L397-L412 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L397-L412 at v20.2.4),
// and the null version's key has no instance, which the class takes as the
// name's plain key (encode_list_index_key,
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L363 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L413 at v20.2.4),
// so it lists the name's newer versions again, acts on them twice and
// counts them twice toward NewerNoncurrentVersions (docs/ceph-upstream-bugs.md,
// "radosgw's lifecycle lister lists a key's versions again after a page that
// ends on its null version").
type lcVersionPager struct {
	// last is the last entry handed out, once started, and lastKey its list
	// key; n counts the entries the current page handed out.
	last    op.ObjectEntry
	started bool
	lastKey string
	n       int
}

// lcListKey is e's list key, get_list_index_key's, which orders a name's
// versions newest first: by versioned_epoch, then instance.
func lcListKey(e *op.ObjectEntry) string {
	return rgw.ListIndexKey(rgw.ObjKey{Name: e.Key.Name, Instance: e.Key.Instance}, e.VersionedEpoch)
}

// fresh is es, a page as listed, without the entries of the last name
// handed out that list at or before the last of them: a page that starts
// again at the name's first version lists them again, and so do versions
// written since the walk passed them, the null delete marker the pass
// writes over a null version among them, which the next pass decides. A
// version lists under one key, which the walk passes once in key order, so
// what fresh keeps of a name is that key, whatever the name's versions
// number. It also passes over an entry that lists out of key order before
// another of its name: radosgw's listing repair copies the null version's
// entry to the name's plain key (docs/ceph-upstream-bugs.md, "radosgw's
// listing repair of a null version's list entry overwrites or removes the
// key's version marker"), which the class lists before every version of the
// name, at the null version's list key, and the null version is handed out
// at its own place. It returns the entries kept and each one's index in es.
func (v *lcVersionPager) fresh(es []op.ObjectEntry) (kept []op.ObjectEntry, at []int) {
	kept = make([]op.ObjectEntry, 0, len(es))
	at = make([]int, 0, len(es))
	v.n = 0
	for i := range es {
		e := &es[i]
		k := lcListKey(e)
		same := v.started && e.Key.Name == v.last.Key.Name
		if same && k <= v.lastKey || lcAhead(es, i, k) {
			continue
		}
		v.last, v.lastKey, v.started = *e, k, true
		v.n++
		kept = append(kept, *e)
		at = append(at, i)
	}
	return kept, at
}

// lcAhead reports whether es[i], whose list key is k, lists ahead of its
// place: the next entry is of its name and sorts before it.
func lcAhead(es []op.ObjectEntry, i int, k string) bool {
	return i+1 < len(es) && es[i+1].Key.Name == es[i].Key.Name && lcListKey(&es[i+1]) < k
}

// next is where the page after res, listed with p, starts: after its next
// marker. A page that hands nothing out and whose next marker is the one it
// started from is no progress.
func (v *lcVersionPager) next(_ context.Context, _ *op.BucketRecord, p op.ListObjectsParams, res *op.ListObjectsResult) (op.ListObjectsParams, bool) {
	q := p
	q.Marker, q.MarkerInstance = res.NextMarker, res.NextMarkerInstance
	return q, v.n > 0 || q.Marker != p.Marker || q.MarkerInstance != p.MarkerInstance
}

// multipart is handle_multipart_expiration
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L886-L1000 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L907-L1020 at v20.2.4):
// under the prefix of each enabled rule that has DaysAfterInitiation, in
// the prefix map's order, the bucket's multipart meta objects, unordered
// and without versions, each page's uploads handed to the work pool
// (abortUpload), which drains once every rule is walked. No other filter
// applies, nor the zone's ArchiveZone check, as radosgw applies neither
// here, and a failed page fails the bucket.
func (b *lcBucket) multipart(ctx context.Context, ops []lifecycle.PrefixOp) error {
	pool := lcWorkPool(b.p.s.opts.lcMaxWPWorker)
	defer lcDrain(pool)
	for i := range ops {
		o := &ops[i].Op
		if !o.Enabled || o.MPExpiration <= 0 {
			continue
		}
		p := op.ListObjectsParams{Prefix: ops[i].Prefix, NS: meta.NSMultipart, NameFilter: meta.IsMultipartMeta, AllowUnordered: true}
		stop, err := b.walk(ctx, p, true, func(rec *op.BucketRecord, res *op.ListObjectsResult) {
			for j := range res.Entries {
				e := res.Entries[j]
				pool.Go(func() error {
					b.abortUpload(ctx, rec, o, e)
					return nil
				})
			}
		}, lcByName)
		if stop {
			return err
		}
	}
	return nil
}

// lcEntry is e as Decide reads a listed entry.
func lcEntry(e *op.ObjectEntry) lifecycle.Entry {
	return lifecycle.Entry{Key: e.Key, Current: e.IsLatest, DeleteMarker: e.DeleteMarker, Mtime: e.Mtime, StorageClass: e.StorageClass}
}

// lcWorkPool is a worker's WorkPool
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L734-L871 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L755-L892 at v20.2.4):
// at most rgw_lc_max_wp_worker entries at once, Go waiting for a free slot.
// Its work items log their failures and return nil, as radosgw ignores its
// work function's results.
func lcWorkPool(n int) *errgroup.Group {
	g := new(errgroup.Group)
	g.SetLimit(max(n, 1))
	return g
}

// lcDrain waits for every work item handed to pool, as WorkPool::drain does.
func lcDrain(pool *errgroup.Group) {
	_ = pool.Wait() //nolint:errcheck // every work item returns nil
}
