package driver

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// reshardWait is RGWReshardWait's default duration, the pause between two
// reads of a resharding shard's status
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_reshard.h#L266 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_reshard.h#L275 at v20.2.4),
// which RGWRados::init_complete takes.
const reshardWait = 5 * time.Second

// gcObjMinWaitDefault is rgw_gc_obj_min_wait's default, 2_hr in
// rgw.yaml.in at v19.2.6 and v20.2.4.
const gcObjMinWaitDefault = 7200

// gcDueSkew is how far short of 2106 a saturated gc due time stops: the
// class adds the wait to the OSD's clock, not the gateway's, so a due time
// pinned exactly at the last 32-bit second would still wrap on an OSD whose
// clock runs ahead. cephx already refuses clocks a day apart.
const gcDueSkew = 24 * 60 * 60

// gcExpiration is the expiration_secs an enqueue sends for a wait of wait
// seconds at now: the wait, but no more than keeps now plus it within the
// 32 bits of seconds the class stores the due time in (encode(real_time),
// https://github.com/ceph/ceph/blob/v19.2.6/src/include/encoding.h#L346-L354 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/include/encoding.h#L347-L355 at v20.2.4),
// less gcDueSkew. radosgw narrows the wait to the uint32_t every gc call
// takes and lets the sum wrap, which makes the tails due at once
// (docs/exclusions.md, "A negative or overlong gc object wait").
func gcExpiration(wait int64, now time.Time) uint32 {
	limit := max(int64(math.MaxUint32)-now.Unix()-gcDueSkew, 0)
	return uint32(min(wait, limit)) //nolint:gosec // within [0, MaxUint32]
}

// writeOptions are the rgw_* tunables the write path and its workers read
// once at Open. The defaults in the comments are rgw.yaml.in's, identical
// at v19.2.6 and v20.2.4. Each is kept as radosgw reads it, for the code
// that uses it to treat it as radosgw does there, except where noted.
type writeOptions struct {
	putWindow          uint64 // rgw_put_obj_min_window_size, 16 MiB; radosgw never reads rgw_put_obj_max_window_size
	stripeSize         uint64 // rgw_obj_stripe_size, 4 MiB
	chunkSize          uint64 // rgw_max_chunk_size, 4 MiB
	maxPutSize         uint64 // rgw_max_put_size, 5 GiB
	gcMaxObjs          uint32 // rgw_gc_max_objs, 32: floored to 1, then capped at meta.ShardsMax as RGWGC::initialize caps it
	gcObjMinWait       int64  // rgw_gc_obj_min_wait, 7200 s; 7200 s when negative, and saturated per enqueue by gcExpiration
	gcProcessorMaxTime int32  // rgw_gc_processor_max_time, 3600 s, narrowed as RGWGC::process's int reads it
	gcProcessorPeriod  int32  // rgw_gc_processor_period, 3600 s, narrowed as GCWorker::entry's int reads it
	gcMaxConcurrentIO  int    // rgw_gc_max_concurrent_io, 10
	gcMaxTrimChunk     int    // rgw_gc_max_trim_chunk, 16
	gcMaxQueueSize     uint64 // rgw_gc_max_queue_size, 131068 KiB
	gcMaxDeferred      uint64 // rgw_gc_max_deferred, 50
	gcThreads          bool   // rgw_enable_gc_threads, true
	multiObjDelMaxAIO  int    // rgw_multi_obj_del_max_aio, 16, floored to 1 as RGWDeleteMultiObj::execute does
	copyConcurrentIO   int    // rgw_max_copy_obj_concurrent_io, 10, 0 used as 1 with an error-level log line
	maxRequests        int64  // Options.MaxConcurrentRequests, which Open sets
	// codec is global.yaml.in's compressor_zlib_level, 5,
	// compressor_zlib_winsize, -15, and compressor_zstd_level, 1, which
	// radosgw's compressors read at each compress
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/global.yaml.in#L768-L789 at v19.2.7
	// and v20.2.4).
	codec compression.EncoderOptions
}

// readWriteOptions reads the write options through conf, each through the
// accessor its rgw.yaml.in type calls for, and fails on the first option
// librados does not know or whose value does not parse. An rgw_gc_max_objs
// below 1 is used as 1 with an error-level log line, as floorShards uses
// the other shard counts, and so is a zero rgw_max_copy_obj_concurrent_io.
// A negative rgw_gc_obj_min_wait is used as its
// default, 7200 s, with an error-level log line, and one too long for the
// class's 32-bit due time is logged too and saturated at each enqueue
// (gcExpiration): radosgw narrows it to a uint32_t expiration whose due
// time wraps into the past, which frees an overwritten object's tails at
// the next gc pass, under readers still fetching them (docs/exclusions.md,
// "A negative or overlong gc object wait"). A zero
// stripe size, or a write window smaller
// than one chunk, refuses to start (docs/exclusions.md, "Write sizes that
// cannot write"). A compressor_zlib_level or compressor_zlib_winsize zlib
// refuses is logged at error level and kept: radosgw's zlib compress then
// fails for every object, which RGWPutObj_Compress stores uncompressed
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_compression.cc#L54-L64 at v19.2.7
// and v20.2.4), and so does rgw-go.
func readWriteOptions(conf *cephconf.Options) (writeOptions, error) {
	var r reads
	o := writeOptions{
		putWindow:          readOption(&r, conf.Size, "rgw_put_obj_min_window_size"),
		stripeSize:         readOption(&r, conf.Size, "rgw_obj_stripe_size"),
		chunkSize:          readOption(&r, conf.Size, "rgw_max_chunk_size"),
		maxPutSize:         readOption(&r, conf.Size, "rgw_max_put_size"),
		gcProcessorMaxTime: int32(readOption(&r, conf.Int64, "rgw_gc_processor_max_time")), //nolint:gosec // narrowed as C++ narrows it
		gcProcessorPeriod:  int32(readOption(&r, conf.Int64, "rgw_gc_processor_period")),   //nolint:gosec // narrowed as C++ narrows it
		gcMaxConcurrentIO:  int(readOption(&r, conf.Int64, "rgw_gc_max_concurrent_io")),
		gcMaxTrimChunk:     int(readOption(&r, conf.Int64, "rgw_gc_max_trim_chunk")),
		gcMaxQueueSize:     readOption(&r, conf.Uint64, "rgw_gc_max_queue_size"),
		gcMaxDeferred:      readOption(&r, conf.Uint64, "rgw_gc_max_deferred"),
		gcThreads:          readOption(&r, conf.Bool, "rgw_enable_gc_threads"),
		copyConcurrentIO:   int(readOption(&r, conf.Int64, "rgw_max_copy_obj_concurrent_io")),
	}
	gcObjs := readOption(&r, conf.Int64, "rgw_gc_max_objs")
	minWait := readOption(&r, conf.Int64, "rgw_gc_obj_min_wait")
	delAIO := readOption(&r, conf.Uint64, "rgw_multi_obj_del_max_aio")
	o.codec = compression.EncoderOptions{
		ZlibLevel:   int(readOption(&r, conf.Int64, "compressor_zlib_level")),
		ZlibWinsize: int(readOption(&r, conf.Int64, "compressor_zlib_winsize")),
		ZstdLevel:   int(readOption(&r, conf.Int64, "compressor_zstd_level")),
	}
	if r.err != nil {
		return writeOptions{}, fmt.Errorf("reading the write options: %w", r.err)
	}
	if _, err := compression.NewEncoder(compression.Zlib, o.codec); err != nil {
		slog.Error("zlib refuses the compressor options; objects of a zlib class are stored uncompressed", slog.Any("error", err))
	}
	o.gcMaxObjs = min(floorShards("rgw_gc_max_objs", gcObjs), meta.ShardsMax)
	if minWait < 0 {
		slog.Error("gc object wait is negative; using the default of 7200 seconds",
			slog.String("option", "rgw_gc_obj_min_wait"), slog.Int64("value", minWait))
		minWait = gcObjMinWaitDefault
	}
	if now := time.Now(); int64(gcExpiration(minWait, now)) < minWait {
		slog.Error("gc object wait reaches past 2106; queued tails will be due shortly before then",
			slog.String("option", "rgw_gc_obj_min_wait"), slog.Int64("value", minWait))
	}
	o.gcObjMinWait = minWait
	// std::max<uint32_t>(1, rgw_multi_obj_del_max_aio) narrows the option
	// before it floors it
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7011 at v19.2.6).
	o.multiObjDelMaxAIO = int(max(uint32(delAIO), 1)) //nolint:gosec // narrowed as C++ narrows it
	if o.copyConcurrentIO == 0 {
		// radosgw fails every copy that shares tails with EDEADLK then
		// (docs/ceph-upstream-bugs.md); a negative value, which limits
		// nothing there, is kept.
		slog.Error("copy refcount concurrency is zero; using 1",
			slog.String("option", "rgw_max_copy_obj_concurrent_io"), slog.Int("value", 0))
		o.copyConcurrentIO = 1
	}
	if o.chunkSize == 0 || o.stripeSize == 0 || o.putWindow < o.chunkSize {
		return writeOptions{}, fmt.Errorf("driver: rgw_max_chunk_size %d, rgw_obj_stripe_size %d, rgw_put_obj_min_window_size %d: "+
			"a zero size, or a window below one chunk, cannot write", o.chunkSize, o.stripeSize, o.putWindow)
	}
	return o, nil
}

// randAlnum is gen_rand_alphanumeric
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L45-L51 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L45-L51 at v20.2.4):
// n characters of alphanumeric, unbiased, as 64 divides 256.
func randAlnum(n int) string { return meta.ChooseFrom(alphanumeric, n) }

// writer is the write path's state on Store.
type writer struct {
	opts        writeOptions
	zoneID      string // svc.zone->get_zone().id: the zone half of every zones_trace entry
	shortZoneID uint32 // RGWPeriodMap::get_zone_short_id: the period map's entry for the zone, 0 when it has none
	logData     bool   // need_to_log_data: the zone's log_data, as the default sync module exports data
	gcShards    []string
	reshardWait time.Duration
	completions *completionManager
	body        *bodyBudget
	rand        func(n int) string
	newEncoder  func(codec string, o compression.EncoderOptions) (compression.Encoder, error)
	// unloadable holds each compression type a write met with no encoder,
	// logged the first time.
	unloadable sync.Map
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
		body:        newBodyBudget(bodyBudgetSize(s.cluster, opts)),
		rand:        randAlnum,
		newEncoder:  compression.NewEncoder,
	}
	// RGWGC::initialize names the shards "gc.0" to "gc.<n-1>"
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L39-L44).
	for i := range opts.gcMaxObjs {
		w.gcShards = append(w.gcShards, gc.ShardOID(int(i)))
	}
	w.completions = newCompletionManager(ctx, s)
	return w
}

// randTag is append_rand_alpha(cct, "", tag, 32)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1621-L1628 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1623-L1630 at v20.2.4):
// "_" and 31 random characters, the tag an index operation gets when its
// request brings none.
func (w *writer) randTag() string { return "_" + w.rand(31) }
