package driver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// usageObjPrefix is RGW_USAGE_OBJ_PREFIX (driver/rados/rgw_rados.cc:120 at
// v19.2.6, :128 at v20.2.4).
const usageObjPrefix = "usage."

// usageFinalFlushTimeout is how long the usage worker goes on flushing once
// its context has ended: radosgw's ~UsageLogger waits for a flush without
// bound (rgw_log.cc:128-133), which would hold rgw-go's shutdown on a stuck
// write (docs/exclusions.md).
const usageFinalFlushTimeout = 10 * time.Second

// ticker is the part of a time.Ticker a worker uses, so that a spec can
// drive the worker from a clock of its own.
type ticker interface {
	C() <-chan time.Time
	Stop()
}

// timeTicker is a ticker over a time.Ticker.
type timeTicker struct{ t *time.Ticker }

func newTimeTicker(d time.Duration) ticker { return timeTicker{time.NewTicker(d)} }

// C returns the channel the ticks arrive on.
func (t timeTicker) C() <-chan time.Time { return t.t.C }

// Stop turns the ticker off.
func (t timeTicker) Stop() { t.t.Stop() }

// usageKey is rgw_user_bucket: the string form of the user an entry is filed
// under, and its bucket.
type usageKey struct{ user, bucket string }

// usageBatch is RGWUsageBatch (driver/rados/rgw_rados.h:140-148 at v19.2.6,
// :142-150 at v20.2.4): a key's merged entry for each hour, by the epoch of
// the hour's start.
type usageBatch map[uint64]*rgwcls.UsageLogEntry

// usageLogger is rgw_log.cc's UsageLogger (:95-180 at v19.2.6 and v20.2.4):
// the usage entries of finished requests, merged per user, bucket and hour
// until a flush writes them to the zone's usage log.
type usageLogger struct {
	mu sync.Mutex
	// flushing is a one-slot semaphore that serializes flushes, as
	// UsageLogger's timer_lock does, and that a flush stops waiting for when
	// its context ends. flushSlot makes it.
	flushing chan struct{}
	// finalFlush is how long the worker goes on flushing once its context has
	// ended.
	finalFlush time.Duration
	entries    map[usageKey]usageBatch
	numEntries int
	// roundTS is the start of the hour new entries are filed under.
	roundTS time.Time
	// loc is the time zone whose hours roundTS starts: radosgw rounds in its
	// process's.
	loc  *time.Location
	tick time.Duration
}

// reset empties the log and puts its round timestamp at the start of now's
// hour in loc, as UsageLogger's constructor does (rgw_log.cc:120-126).
func (u *usageLogger) reset(now time.Time, loc *time.Location) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.entries, u.numEntries = nil, 0
	u.loc = loc
	u.roundTS = roundToHour(now, loc)
}

// openUsageLog starts the usage log, with its flush worker, when
// rgw_enable_usage_log is set. A tick interval that is not positive is used
// as one second, with an error-level log line naming the option: radosgw
// re-arms its flush timer that far in the past and flushes without pause
// (docs/ceph-upstream-bugs.md, tracker #81226), and a time.Ticker refuses it.
func (s *Store) openUsageLog(ctx context.Context) {
	if !s.opts.usageLogEnabled {
		return
	}
	s.usage.reset(time.Now(), time.Local)
	s.usage.finalFlush = usageFinalFlushTimeout
	s.usage.tick = s.opts.usageTick
	if s.usage.tick <= 0 {
		slog.ErrorContext(ctx, "usage log tick interval is not positive; using 1 second (tracker #81226)",
			slog.String("option", "rgw_usage_log_tick_interval"), slog.Int64("value", int64(s.usage.tick/time.Second)))
		s.usage.tick = time.Second
	}
	s.AddWorker("usage-flush", s.runUsageFlush)
}

// roundToHour is utime_t::round_to_hour (include/utime.h:208-216 at v19.2.6
// and v20.2.4): the start of t's hour in loc. radosgw rounds through
// localtime_r and mktime, so in a time zone whose offset is not whole hours
// its hours do not start on UTC's.
func roundToHour(t time.Time, loc *time.Location) time.Time {
	_, off := t.In(loc).Zone()
	local := t.Unix() + int64(off)
	return time.Unix(local-local%3600-int64(off), 0).In(loc)
}

// usageOID is usage_log_hash (driver/rados/rgw_rados.cc:1615-1628 at v19.2.6,
// :1718-1731 at v20.2.4): the usage object of user's index-th shard, or,
// without a user, the index-th usage object.
func (s *Store) usageOID(user string, index uint32) string {
	val := index
	if user != "" {
		val = shardMod(val, s.opts.usageMaxUserShards) + meta.StrHashLinux(user)
	}
	return usageObjPrefix + strconv.FormatUint(uint64(shardMod(val, s.opts.usageMaxShards)), 10)
}

// Log implements op.UsageLogger. It is UsageLogger::insert and insert_user
// (rgw_log.cc:139-165): the entry is filed under its payer, or its owner when
// it has none, and its bucket, and merged into that key's entry for the hour
// the round timestamp starts. An entry filed under no user is kept, and
// counted, until the flush skips it. Once more than
// rgw_usage_log_flush_threshold entries are held, the caller flushes them.
// It does nothing unless rgw_enable_usage_log is set.
func (s *Store) Log(ctx context.Context, e op.UsageEntry) {
	if !s.opts.usageLogEnabled {
		return
	}
	// rgw_usage_log_entry holds both as rgw_user, parsed from their strings.
	owner, payer := meta.ParseUserID(e.Owner.String()), meta.ParseUserID(e.Payer.String())
	user := owner
	if payer.ID != "" {
		user = payer
	}
	data := rgwcls.UsageData{BytesSent: e.BytesSent, BytesReceived: e.BytesReceived, Ops: e.Ops, SuccessfulOps: e.SuccessfulOps}
	u := &s.usage
	u.mu.Lock()
	// insert_user compares whole seconds, and only moves the round timestamp
	// once a request is more than an hour past it.
	if e.Time.Unix() > u.roundTS.Unix()+3600 {
		u.roundTS = roundToHour(e.Time, u.loc)
	}
	epoch := uint64(u.roundTS.Unix()) //nolint:gosec // the hour of a request, after 1970
	if u.entries == nil {
		u.entries = map[usageKey]usageBatch{}
	}
	key := usageKey{user: user.String(), bucket: e.Bucket}
	batch := u.entries[key]
	if batch == nil {
		batch = usageBatch{}
		u.entries[key] = batch
	}
	ent := batch[epoch]
	if ent == nil {
		ent = &rgwcls.UsageLogEntry{UsageMap: map[string]rgwcls.UsageData{}}
		batch[epoch] = ent
		u.numEntries++
	}
	// rgw_usage_log_entry::aggregate (cls/rgw/cls_rgw_types.h:1005-1023 at
	// v19.2.6, :1044-1062 at v20.2.4) keeps the owner and payer of the first
	// entry merged, unless that entry's owner is empty.
	if meta.ParseUserID(ent.Owner).ID == "" {
		ent.Owner, ent.Payer, ent.Bucket, ent.Epoch = owner.String(), payer.String(), e.Bucket, epoch
	}
	ent.UsageMap[e.Category] = addUsage(ent.UsageMap[e.Category], data)
	ent.TotalUsage = addUsage(ent.TotalUsage, data)
	flush := u.numEntries > s.opts.usageFlushThreshold
	u.mu.Unlock()
	if flush {
		// The flush writes every user's entries, so the end of this request
		// must not cancel it.
		if err := s.flushUsage(context.WithoutCancel(ctx)); err != nil {
			slog.ErrorContext(ctx, "usage log flush failed", slog.Any("error", err))
		}
	}
}

// addUsage is rgw_usage_data::aggregate.
func addUsage(a, b rgwcls.UsageData) rgwcls.UsageData {
	return rgwcls.UsageData{
		BytesSent:     a.BytesSent + b.BytesSent,
		BytesReceived: a.BytesReceived + b.BytesReceived,
		Ops:           a.Ops + b.Ops,
		SuccessfulOps: a.SuccessfulOps + b.SuccessfulOps,
	}
}

// flushUsage is UsageLogger::flush (rgw_log.cc:167-175): it takes every entry
// out of the log and writes them. Entries logged meanwhile wait for the next
// flush, and a failed write loses the entries taken out, as radosgw's does.
// It waits for a flush under way, unless ctx ends first.
func (s *Store) flushUsage(ctx context.Context) error {
	u := &s.usage
	slot := u.flushSlot()
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("waiting for the usage log flush under way: %w", context.Cause(ctx))
	}
	defer func() { <-slot }()
	u.mu.Lock()
	batches := u.entries
	u.entries, u.numEntries = nil, 0
	u.mu.Unlock()
	return s.logUsage(ctx, batches)
}

// flushSlot returns the flushing semaphore, making it on first use.
func (u *usageLogger) flushSlot() chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.flushing == nil {
		u.flushing = make(chan struct{}, 1)
	}
	return u.flushing
}

// logUsage is RGWRados::log_usage (driver/rados/rgw_rados.cc:1630-1672 at
// v19.2.6, :1733-1775 at v20.2.4). In rgw_user_bucket order it skips each key
// without a user, with a warning, and gathers the entries of every other key
// under its user's usage object, named with an index that grows by one with
// each user. It then adds each object's entries with one user_usage_log_add,
// in the objects' name order, and stops at the first failure.
func (s *Store) logUsage(ctx context.Context, batches map[usageKey]usageBatch) error {
	keys := slices.SortedFunc(maps.Keys(batches), func(a, b usageKey) int {
		return cmp.Or(strings.Compare(a.user, b.user), strings.Compare(a.bucket, b.bucket))
	})
	objs := map[string]*rgwcls.UsageLogInfo{}
	var (
		index         uint32
		lastUser, oid string
	)
	for _, k := range keys {
		if k.user == "" {
			slog.WarnContext(ctx, "usage log entry has no user; skipping", slog.String("bucket", k.bucket))
			continue
		}
		if k.user != lastUser {
			oid = s.usageOID(k.user, index)
			index++
			lastUser = k.user
		}
		info := objs[oid]
		if info == nil {
			info = &rgwcls.UsageLogInfo{}
			objs[oid] = info
		}
		b := batches[k]
		for _, epoch := range slices.Sorted(maps.Keys(b)) {
			info.Entries = append(info.Entries, *b[epoch])
		}
	}
	if len(objs) == 0 {
		return nil
	}
	pool, err := s.pools.get(ctx, s.zone.Params.UsageLogPool)
	if err != nil {
		return err
	}
	for _, oid := range slices.Sorted(maps.Keys(objs)) {
		wop := radosclient.NewWriteOp()
		rgwcls.UsageLogAdd(wop, *objs[oid], s.release)
		if _, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil {
			return fmt.Errorf("writing usage log object %s: %w", oid, err)
		}
	}
	return nil
}

// runUsageFlush is UsageLogger's timer (rgw_log.cc:105-117): it flushes on
// every tick of rgw_usage_log_tick_interval, and once more as ctx ends, as
// ~UsageLogger does (:128-133), returning that flush's failure. Its flushes
// run on a context that outlives ctx by the final-flush timeout, so a tick
// flush under way as ctx ends can finish its write; once the timeout has
// passed, the flush under way and the last one give up, whether writing or
// waiting for a threshold flush that holds the semaphore.
func (s *Store) runUsageFlush(ctx context.Context) error {
	t := s.newTicker(s.usage.tick)
	defer t.Stop()
	flushCtx, stop := outlive(ctx, s.usage.finalFlush)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			if err := s.flushUsage(flushCtx); err != nil {
				return fmt.Errorf("final usage log flush: %w", err)
			}
			return nil
		case <-t.C():
			if err := s.flushUsage(flushCtx); err != nil {
				slog.ErrorContext(ctx, "usage log flush failed", slog.Any("error", err))
			}
		}
	}
}

// errFinalFlushTimeout is the cause of the end of outlive's context.
var errFinalFlushTimeout = errors.New("usage log final flush timed out")

// outlive returns a context that keeps ctx's values and ends d after ctx
// does, with errFinalFlushTimeout as its cause. stop releases it.
func outlive(ctx context.Context, d time.Duration) (_ context.Context, stop func()) {
	octx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	var (
		mu    sync.Mutex
		timer *time.Timer
	)
	stopAfter := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		timer = time.AfterFunc(d, func() { cancel(errFinalFlushTimeout) })
	})
	return octx, func() {
		stopAfter()
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		cancel(context.Canceled)
	}
}
