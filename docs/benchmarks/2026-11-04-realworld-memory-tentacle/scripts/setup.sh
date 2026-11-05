#!/bin/bash
# setup.sh: readies tentacle for the campaign and records what it found, as alloc/setup.sh: the start
# state; ceph.conf and the admin keyring in the worktree's hack/rooket/out/tentacle (rooket
# ceph-config); rgw-go's keyring (ceph auth get-or-create: an existing entity is left as it is); a
# read-only check that both entities hold the parity options; both entities' values of the options
# that size buffers, windows and concurrency, and the running radosgw's own (config show); the
# radosgw's endpoint, resources and environment; then the bench user.
set -euo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
s=$here/setup
mkdir -p "$s" "$out"
"$here/state.sh" >"$s/state-start.txt" 2>&1
rooket ceph-config --out "$out" >"$s/ceph-config.log" 2>&1
ls -la "$out" >>"$s/ceph-config.log"
cp "$out/ceph.conf" "$s/ceph.conf"
key=$(tb ceph --connect-timeout=20 auth get-or-create client.rgw.rgw-go mon 'allow rw' osd 'allow rwx')
(umask 077 && printf '%s\n' "$key" >"$out/ceph.client.rgw.rgw-go.keyring")
chmod 600 "$out/ceph.client.rgw.rgw-go.keyring"
echo "keyring written for client.rgw.rgw-go"
opts=(rgw_dynamic_resharding=false rgw_enable_gc_threads=false rgw_enable_lc_threads=false
	rgw_run_sync_thread=false rgw_d3n_l1_local_datacache_enabled=false rgw_enable_usage_log=true
	rgw_usage_log_tick_interval=1 rgw_usage_log_flush_threshold=1 rgw_lc_debug_interval=10)
bad=0
for who in client.rgw.rgw-go client.rgw.ceph.objectstore.a; do
	for kv in "${opts[@]}"; do
		[ "$who/${kv%%=*}" = client.rgw.ceph.objectstore.a/rgw_enable_lc_threads ] && continue
		have=$(tb ceph --connect-timeout=20 config get "$who" "${kv%%=*}")
		if [ "$have" != "${kv#*=}" ]; then
			echo "PARITY MISMATCH $who ${kv%%=*}: have $have want ${kv#*=}"
			bad=1
		fi
	done
done
[ "$bad" = 0 ] && echo "parity options held by both entities"
sizing=(rgw_max_chunk_size rgw_obj_stripe_size rgw_max_concurrent_requests rgw_get_obj_window_size
	rgw_get_obj_max_req_size rgw_put_obj_min_window_size rgw_put_obj_max_window_size
	objecter_inflight_op_bytes objecter_inflight_ops rgw_thread_pool_size rgw_multipart_min_part_size
	rgw_max_put_size rgw_cache_enabled rgw_cache_lru_size rgw_bucket_index_max_aio rgw_gc_obj_min_wait
	rgw_enable_lc_threads ms_type ms_async_op_threads rgw_beast_enable_async rgw_max_attr_size
	rgw_list_buckets_max_chunk rgw_max_listing_results)
{
	printf '%-32s %-22s %-22s %s\n' option client.rgw.rgw-go client.rgw.ceph.objectstore.a "radosgw config show"
	for o in "${sizing[@]}"; do
		a=$(tb ceph --connect-timeout=20 config get client.rgw.rgw-go "$o")
		b=$(tb ceph --connect-timeout=20 config get client.rgw.ceph.objectstore.a "$o")
		c=$(tb ceph --connect-timeout=20 config show client.rgw.ceph.objectstore.a "$o" 2>&1 || true)
		printf '%-32s %-22s %-22s %s\n' "$o" "$a" "$b" "$c"
	done
} | tee "$s/options.txt"
tb ceph --connect-timeout=20 config show client.rgw.ceph.objectstore.a >"$s/radosgw.config-show.txt" 2>&1 || true
pod=$(rgwpod)
fe=$(tb ceph --connect-timeout=20 config show client.rgw.ceph.objectstore.a rgw_frontends)
ip=$(timeout 60 rooket k -n rook-ceph get pod "$pod" -o jsonpath='{.status.podIP}')
port=$(sed -nE 's/.*port=([0-9]+).*/\1/p' <<<"$fe")
echo "http://$ip:$port" >"$s/radosgw.endpoint"
echo "radosgw pod $pod, frontends '$fe', endpoint $(cat "$s/radosgw.endpoint")"
curl -fsS -m 5 -o /dev/null -w 'radosgw answers %{http_code}\n' "$(cat "$s/radosgw.endpoint")/"
timeout 60 rooket k -n rook-ceph get pod "$pod" -o jsonpath='{range .spec.containers[*]}{.name}{" "}{.resources}{"\n"}{end}' >"$s/radosgw.resources.txt"
cat "$s/radosgw.resources.txt"
"$spike/rgwpod.sh" tentacle 'setpriv --reuid=167 --regid=167 --clear-groups cat /proc/$RGWPID/environ' | tr '\0' '\n' | sed -E 's/(KEY|SECRET|TOKEN|PASSWORD)=.*/\1=<redacted>/' >"$s/radosgw.environ"
echo "radosgw allocator environment: $(grep -E '^(TCMALLOC_|MALLOC_|LD_PRELOAD|GLIBC_TUNABLES)' "$s/radosgw.environ" | tr '\n' ' ')"
"$spike/benchuser.sh" tentacle create
