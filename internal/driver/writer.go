package driver

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cls/gc"
)

// rgwShardsMax is rgw_shards_max(), RGW_SHARDS_PRIME_1 (rgw_tools.h:40-43
// at v19.2.6, :57-60 at v20.2.4).
const rgwShardsMax = 65521

// reshardWait is RGWReshardWait's default duration, the pause between two
// reads of a resharding shard's status (rgw_reshard.h:266 at v19.2.6, :275
// at v20.2.4), which RGWRados::init_complete takes.
const reshardWait = 5 * time.Second

// writeOptions are the rgw_* tunables the write path and its workers read
// once at Open. The defaults in the comments are rgw.yaml.in's, identical
// at v19.2.6 and v20.2.4. Each is kept as radosgw reads it, for the code
// that uses it to treat it as radosgw does there, except where noted.
type writeOptions struct {
	putWindow          uint64        // rgw_put_obj_min_window_size, 16 MiB; radosgw never reads rgw_put_obj_max_window_size
	stripeSize         uint64        // rgw_obj_stripe_size, 4 MiB
	chunkSize          uint64        // rgw_max_chunk_size, 4 MiB
	maxPutSize         uint64        // rgw_max_put_size, 5 GiB
	gcMaxObjs          uint32        // rgw_gc_max_objs, 32: floored to 1, then capped at rgwShardsMax as RGWGC::initialize caps it
	gcObjMinWait       uint32        // rgw_gc_obj_min_wait, 7200 s, narrowed to the uint32_t expiration every gc call takes
	gcProcessorMaxTime time.Duration // rgw_gc_processor_max_time, 1 h
	gcProcessorPeriod  time.Duration // rgw_gc_processor_period, 1 h
	gcMaxConcurrentIO  int           // rgw_gc_max_concurrent_io, 10
	gcMaxTrimChunk     int           // rgw_gc_max_trim_chunk, 16
	gcMaxQueueSize     uint64        // rgw_gc_max_queue_size, 131068 KiB
	gcMaxDeferred      uint64        // rgw_gc_max_deferred, 50
	gcThreads          bool          // rgw_enable_gc_threads, true
	multiObjDelMaxAIO  int           // rgw_multi_obj_del_max_aio, 16, floored to 1 as RGWDeleteMultiObj::execute does
	copyConcurrentIO   int           // rgw_max_copy_obj_concurrent_io, 10
}

// readWriteOptions reads the write options through conf, each through the
// accessor its rgw.yaml.in type calls for, and fails on the first option
// librados does not know or whose value does not parse. An rgw_gc_max_objs
// below 1 is used as 1 with an error-level log line, as floorShards uses
// the other shard counts. A zero stripe size, or a write window smaller
// than one chunk, refuses to start (docs/exclusions.md, "Write sizes that
// cannot write").
func readWriteOptions(conf *cephconf.Options) (writeOptions, error) {
	var r reads
	o := writeOptions{
		putWindow:  readOption(&r, conf.Size, "rgw_put_obj_min_window_size"),
		stripeSize: readOption(&r, conf.Size, "rgw_obj_stripe_size"),
		chunkSize:  readOption(&r, conf.Size, "rgw_max_chunk_size"),
		maxPutSize: readOption(&r, conf.Size, "rgw_max_put_size"),
		// cls_rgw_gc_set_entry and the gc queue calls take it as a uint32_t.
		gcObjMinWait:       uint32(readOption(&r, conf.Int64, "rgw_gc_obj_min_wait")), //nolint:gosec // narrowed as C++ narrows it
		gcProcessorMaxTime: secondsToDuration(readOption(&r, conf.Int64, "rgw_gc_processor_max_time")),
		gcProcessorPeriod:  secondsToDuration(readOption(&r, conf.Int64, "rgw_gc_processor_period")),
		gcMaxConcurrentIO:  int(readOption(&r, conf.Int64, "rgw_gc_max_concurrent_io")),
		gcMaxTrimChunk:     int(readOption(&r, conf.Int64, "rgw_gc_max_trim_chunk")),
		gcMaxQueueSize:     readOption(&r, conf.Uint64, "rgw_gc_max_queue_size"),
		gcMaxDeferred:      readOption(&r, conf.Uint64, "rgw_gc_max_deferred"),
		gcThreads:          readOption(&r, conf.Bool, "rgw_enable_gc_threads"),
		copyConcurrentIO:   int(readOption(&r, conf.Int64, "rgw_max_copy_obj_concurrent_io")),
	}
	gcObjs := readOption(&r, conf.Int64, "rgw_gc_max_objs")
	delAIO := readOption(&r, conf.Uint64, "rgw_multi_obj_del_max_aio")
	if r.err != nil {
		return writeOptions{}, fmt.Errorf("reading the write options: %w", r.err)
	}
	o.gcMaxObjs = min(floorShards("rgw_gc_max_objs", gcObjs), rgwShardsMax)
	// std::max<uint32_t>(1, rgw_multi_obj_del_max_aio) narrows the option
	// before it floors it (rgw_op.cc:7011 at v19.2.6).
	o.multiObjDelMaxAIO = int(max(uint32(delAIO), 1)) //nolint:gosec // narrowed as C++ narrows it
	if o.chunkSize == 0 || o.stripeSize == 0 || o.putWindow < o.chunkSize {
		return writeOptions{}, fmt.Errorf("driver: rgw_max_chunk_size %d, rgw_obj_stripe_size %d, rgw_put_obj_min_window_size %d: "+
			"a zero size, or a window below one chunk, cannot write", o.chunkSize, o.stripeSize, o.putWindow)
	}
	return o, nil
}

// randAlnum is gen_rand_alphanumeric (common/random_string.cc:33-51 at
// v19.2.6 and v20.2.4): n characters of alphanumeric, each a random byte
// modulo its 64 characters, which is unbiased.
func randAlnum(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	for i := range b {
		b[i] = alphanumeric[int(b[i])%len(alphanumeric)]
	}
	return string(b)
}

// writer is the write path's state on Store.
type writer struct {
	opts        writeOptions
	zoneID      string // svc.zone->get_zone().id: the zone half of every zones_trace entry
	shortZoneID uint32 // RGWPeriodMap::get_zone_short_id: the period map's entry for the zone, 0 when it has none
	logData     bool   // need_to_log_data: the zone's log_data, as the default sync module exports data
	gcShards    []string
	reshardWait time.Duration
	completions *completionManager
	rand        func(n int) string
}

// newWriter builds the write path's state for s, whose zone and period are
// resolved, with a completion manager living as long as ctx's values.
func newWriter(ctx context.Context, s *Store, opts writeOptions) *writer {
	zone := s.Zone()
	w := &writer{
		opts:        opts,
		zoneID:      zone.ID,
		shortZoneID: s.Period().PeriodMap.ShortZoneIDs[zone.ID],
		logData:     zone.LogData,
		reshardWait: reshardWait,
		rand:        randAlnum,
	}
	// RGWGC::initialize names the shards "gc.0" to "gc.<n-1>" (rgw_gc.cc:39-44).
	for i := range opts.gcMaxObjs {
		w.gcShards = append(w.gcShards, gc.ShardOID(int(i)))
	}
	w.completions = newCompletionManager(ctx, s)
	return w
}

// randTag is append_rand_alpha(cct, "", tag, 32) (rgw_common.h:1621-1628 at
// v19.2.6, :1623-1630 at v20.2.4): "_" and 31 random characters, the tag an
// index operation gets when its request brings none.
func (w *writer) randTag() string { return "_" + w.rand(31) }
