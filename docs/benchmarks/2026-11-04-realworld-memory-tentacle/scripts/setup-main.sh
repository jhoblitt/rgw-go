#!/bin/bash
# setup-main.sh: readies the disposable rgw-go-main cluster (the Ceph main image, Rook's own radosgw)
# for RM and RMH and records what it found, as setup.sh did for tentacle: the start state, the
# fsid, the radosgw's endpoint, resources and environment, its running configuration, then the bench
# user. Nothing is changed in the cluster but the bench user.
set -euo pipefail
export RW_CLUSTER=main
. /tmp/claude-1000/spike-memory/realworld/env.sh
s=$setup
mkdir -p "$s"
"$here/state.sh" >"$s/state-start.txt" 2>&1
tb ceph --connect-timeout=20 fsid >"$s/fsid"
echo "fsid $(cat "$s/fsid")"
tb ceph --connect-timeout=20 versions >"$s/versions.txt"
cat "$s/versions.txt"
tb ceph --connect-timeout=20 config show client.rgw.ceph.objectstore.a >"$s/radosgw.config-show.txt" 2>&1 || true
pod=$(rgwpod)
fe=$(grep -E '^rgw_frontends ' "$s/radosgw.config-show.txt" | awk '{print $2, $3}')
ip=$(timeout 60 rooket k -n rook-ceph get pod "$pod" -o jsonpath='{.status.podIP}')
port=$(sed -nE 's/.*port=([0-9]+).*/\1/p' <<<"$fe")
echo "http://$ip:$port" >"$s/radosgw.endpoint"
echo "radosgw pod $pod, frontends '$fe', endpoint $(cat "$s/radosgw.endpoint")"
curl -fsS -m 5 -o /dev/null -w 'radosgw answers %{http_code}\n' "$(cat "$s/radosgw.endpoint")/"
timeout 60 rooket k -n rook-ceph get pod "$pod" -o jsonpath='{range .spec.containers[*]}{.name}{" "}{.resources}{"\n"}{end}' >"$s/radosgw.resources.txt"
cat "$s/radosgw.resources.txt"
"$spike/rgwpod.sh" main 'setpriv --reuid=167 --regid=167 --clear-groups cat /proc/$RGWPID/environ' | tr '\0' '\n' | sed -E 's/(KEY|SECRET|TOKEN|PASSWORD)=.*/\1=<redacted>/' >"$s/radosgw.environ"
echo "radosgw allocator environment: $(grep -E '^(TCMALLOC_|MALLOC_|LD_PRELOAD|GLIBC_TUNABLES)' "$s/radosgw.environ" | tr '\n' ' ')"
# The sizing options the running radosgw has, beside the Tentacle campaign's R (setup/options.txt, its last column):
# any that differ are listed, for the report.
sizing=(rgw_max_chunk_size rgw_obj_stripe_size rgw_max_concurrent_requests rgw_get_obj_window_size
	rgw_get_obj_max_req_size rgw_put_obj_min_window_size rgw_put_obj_max_window_size
	objecter_inflight_op_bytes objecter_inflight_ops rgw_thread_pool_size rgw_multipart_min_part_size
	rgw_max_put_size rgw_cache_enabled rgw_cache_lru_size rgw_bucket_index_max_aio rgw_gc_obj_min_wait
	rgw_enable_lc_threads ms_type ms_async_op_threads rgw_beast_enable_async rgw_max_attr_size
	rgw_list_buckets_max_chunk rgw_max_listing_results rgw_enable_gc_threads rgw_dynamic_resharding
	rgw_run_sync_thread rgw_d3n_l1_local_datacache_enabled rgw_enable_usage_log rgw_usage_log_tick_interval
	rgw_usage_log_flush_threshold rgw_lc_debug_interval log_to_file rgw_heap_release_interval rgw_heap_release_bytes)
{
	printf '%-32s %-30s %s\n' option "radosgw (main) config show" "R (tentacle) config show"
	for o in "${sizing[@]}"; do
		a=$(tb ceph --connect-timeout=20 config show client.rgw.ceph.objectstore.a "$o" 2>&1 || true)
		b=$(awk -v o="$o" '$1 == o {print $4}' "$here/setup/options.txt")
		[ -n "$b" ] || b=$(awk -v o="$o" '$1 == o {print $2}' "$here/setup/radosgw.config-show.txt")
		printf '%-32s %-30s %s\n' "$o" "$a" "${b:-(not in the tentacle record)}"
	done
} | tee "$s/options.txt"
"$spike/benchuser.sh" main create
