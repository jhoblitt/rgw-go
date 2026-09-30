package driver

import (
	"log/slog"
	"math"
)

// floorShards returns n as a shard count: 1, with an error-level log line
// naming the option, when n is not positive, and at most 2^32-1. radosgw
// accepts a zero rgw_usage_max_shards or rgw_lc_max_objs and faults on the
// first request that shards (docs/ceph-upstream-bugs.md, tracker #80991).
func floorShards(option string, n int64) uint32 {
	if n <= 0 {
		slog.Error("shard count is not positive; using 1 (tracker #80991)",
			slog.String("option", option), slog.Int64("value", n))
		return 1
	}
	return uint32(min(n, math.MaxUint32)) //nolint:gosec // bounded to [1, MaxUint32] above
}

// shardMod is the plain modulo the usage log reduces a hash by
// (usage_log_hash: val % max_user_shards, then % max_shards,
// driver/rados/rgw_rados.cc:1615-1628 at v19.2.6, :1718-1731 at v20.2.4),
// and the driver's one plain modulo of a hash by a shard count. It is not
// rgw_shards_mod, which the bucket index and GC reduce through, taking the
// hash modulo 7877 or 65521 first (driver/rados/rgw_tools.h:46-55 at
// v19.2.6, :63-72 at v20.2.4); meta.IndexShard does that for the index. A
// zero count cannot reach it after floorShards; it still answers 0 rather
// than dividing.
func shardMod(hash, shards uint32) uint32 {
	if shards == 0 {
		return 0
	}
	return hash % shards
}
