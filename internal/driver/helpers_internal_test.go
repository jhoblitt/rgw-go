package driver

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"maps"
	"strings"

	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

// captureLog sends slog's default logger to w as JSON until restore runs.
// slog.SetDefault also points the log package's output at the new handler,
// and restoring the old default logger leaves it there, so restore puts log's
// writer and flags back too.
func captureLog(w io.Writer) (restore func()) {
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil)))
	return func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	}
}

// logRecords decodes the JSON lines captureLog wrote to buf.
func logRecords(buf *bytes.Buffer) []map[string]any {
	var recs []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed(), line)
		recs = append(recs, rec)
	}
	return recs
}

// conf is Options over every option readOptions reads, at radosgw's v19.2.6
// defaults, with kv on top.
func conf(kv map[string]string) *cephconf.Options {
	m := cephconf.MapGetter{
		"rgw_cache_enabled": "true", "rgw_cache_lru_size": "25000", "rgw_cache_expiry_interval": "900",
		"rgw_num_control_oids": "8", "rgw_max_notify_retries": "10",
		"rgw_enable_usage_log": "false", "rgw_usage_log_flush_threshold": "1024", "rgw_usage_log_tick_interval": "30",
		"rgw_usage_max_shards": "32", "rgw_usage_max_user_shards": "1", "rgw_lc_max_objs": "32",
		"rgw_bucket_quota_ttl": "600", "rgw_bucket_quota_cache_size": "10000", "rgw_user_quota_bucket_sync_interval": "180",
		"rgw_user_quota_sync_interval": "86400", "rgw_user_quota_sync_wait_time": "86400",
		"rgw_user_quota_sync_idle_users": "false", "rgw_enable_quota_threads": "true",
		"rgw_list_bucket_min_readahead": "1000", "rgw_override_bucket_index_max_shards": "0", "rgw_bucket_index_max_aio": "128",
		"rgw_dynamic_resharding": "true", "rgw_run_sync_thread": "true",
	}
	maps.Copy(m, kv)
	return cephconf.NewOptions(m)
}
