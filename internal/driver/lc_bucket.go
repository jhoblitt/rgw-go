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
// whose rules transition to a cloud tier"). A bucket that was ever versioned,
// one with a transition rule and one with an abort of incomplete multipart
// uploads are left to radosgw's worker too, as the bucket pass applies none
// of them (docs/exclusions.md, "rgw-go's lifecycle worker leaves versioned
// buckets and transition rules to radosgw's"). The rules the zone runs
// count, as is_valid_op and zone_check choose them (lifecycle.Op.Applies),
// and for the abort every enabled rule with DaysAfterInitiation, as
// handle_multipart_expiration chooses them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L961-L963 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L981-L983 at v20.2.4).
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
	if rec.Info.Versioned() {
		return "ever versioned", ""
	}
	for i := range ops {
		if o := &ops[i].Op; o.Applies(archive) && len(o.Transitions)+len(o.NoncurTransitions) > 0 {
			return "rule transitions objects", o.ID
		}
	}
	for i := range ops {
		if o := &ops[i].Op; o.Enabled && o.MPExpiration > 0 {
			return "rule aborts multipart uploads", o.ID
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
// decode failed. A bucket the predicate refuses now, versioned or given new
// rules since its claim, fails, so either worker takes it again at its next
// pass. Then each rule of the prefix map, in order (lcBucket.rule), each
// page on the bucket's record as atPage reads it again.
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
	b := &lcBucket{p: p, w: w, c: c, attr: attr, conf: conf, stopAt: stopAt}
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
	return nil
}

// lcBucket is the pass on one claimed bucket, whose user.rgw.lc was attr,
// conf decoded, as the pass began.
type lcBucket struct {
	p      *lcPass
	w      *lcWorker
	c      lcClaim
	attr   []byte
	conf   lifecycle.Configuration
	stopAt time.Time

	// mu guards changed, the first change to the bucket a removal's record
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
// each page and each removal. A bucket that no longer loads under the entry's
// marker is errLCStaleEntry, as run's own load is. A bucket whose user.rgw.lc
// is no longer the pass's, or that the claim predicate now refuses, its
// versioning enabled since or its rules now transitioning, is errLCChanged,
// so that nothing acts on a record the bucket no longer holds, a delete that
// would write a marker taken as an unversioned removal among them. Any other
// error is the read's own. radosgw acts on the record it loaded at the
// pass's start until the pass ends (docs/exclusions.md, "Lifecycle stops a
// bucket's pass when the bucket changes under it").
func (b *lcBucket) current(ctx context.Context) (*op.BucketRecord, error) {
	rec, err := b.p.s.lcLoadBucket(ctx, b.c.entry.Bucket)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(rec.Attrs[op.AttrLC], b.attr) {
		return nil, fmt.Errorf("%w: the lifecycle configuration of %s is no longer the pass's", errLCChanged, b.c.entry.Bucket)
	}
	if reason, rule := b.p.refusal(rec, b.conf); reason != "" {
		return nil, fmt.Errorf("%w: %s now refused: %s %q", errLCChanged, b.c.entry.Bucket, reason, rule)
	}
	return rec, nil
}

// noteChange keeps err, the first failure of a removal's record check: the
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
// the bucket's objects under the rule's prefix, unordered and without
// versions, as on a bucket never versioned, each page's entries handed to
// the work pool with the lister's state. The pool drains before the rule
// ends, however it ends. A later page that fails ends the rule alone, as
// radosgw logs it and goes on. A change a removal found ends the bucket
// with it, when the walk did not end on an error of its own. It reports
// whether the bucket stops, with the error it stops on.
func (b *lcBucket) rule(ctx context.Context, po *lifecycle.PrefixOp) (bool, error) {
	pool := lcWorkPool(b.p.s.opts.lcMaxWPWorker)
	var lister lifecycle.Lister
	p := op.ListObjectsParams{Prefix: po.Prefix, AllowUnordered: true}
	stop, err := b.walk(ctx, p, false, func(rec *op.BucketRecord, res *op.ListObjectsResult) {
		entries := make([]lifecycle.Entry, len(res.Entries))
		for i := range res.Entries {
			entries[i] = lcEntry(&res.Entries[i])
		}
		states := lister.Page(entries, res.Truncated)
		for i := range res.Entries {
			e, le, st := res.Entries[i], entries[i], states[i]
			pool.Go(func() error {
				b.entry(ctx, rec, &po.Op, e, le, st)
				return nil
			})
		}
	})
	lcDrain(pool)
	if cerr := b.change(); cerr != nil && err == nil {
		return true, cerr
	}
	return stop, err
}

// walk lists p from its start in pages of lcPageSize, hands each page to
// page with the record atPage read before it, then waits
// rgw_lc_thread_delay, as radosgw's listings do. A page that finds no index
// stops the bucket complete, as radosgw's -ENOENT returns 0; the first
// page's other failure stops it failed, and so does a later page's with
// failLater, while without it a later page's failure ends the walk alone. It
// reports whether the bucket stops, with the error it stops on.
//
// The next page starts after NextMarker. The pass removes entries it
// listed, the marker's among them, but a listing without versions starts
// after the marker's own name whether or not its entry remains: the class
// lists the omap keys after it (encode_list_index_key,
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L363 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L413 at v20.2.4),
// on the shard the marker's name hashes to, so no entry after it is
// skipped.
func (b *lcBucket) walk(ctx context.Context, p op.ListObjectsParams, failLater bool, page func(rec *op.BucketRecord, res *op.ListObjectsResult)) (bool, error) {
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
		if res.NextMarker == p.Marker {
			slog.WarnContext(ctx, "lifecycle listing made no progress; the rule ends", slog.String("bucket", b.c.entry.Bucket),
				slog.String("prefix", p.Prefix), slog.String("marker", p.Marker))
			return false, nil
		}
		p.Marker = res.NextMarker
	}
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
