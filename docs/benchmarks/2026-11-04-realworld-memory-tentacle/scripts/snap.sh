#!/bin/bash
# snap.sh VARIANT PID PREFIX [full]: a gateway snapshot without stopping it. Both: status (from the
# host), smaps_rollup and smaps (radosgw's from inside its pod's log-collector container, as uid
# 167), the cgroup's memory.stat. rgw-go also: /metrics and the heap profile's MemStats. With
# "full", taken after sampling has ended: rgw-go's C heap from tcmalloc's own view (alloc/gdbheap.sh,
# which stops it for a moment), and radosgw's dump_mempools over its admin socket.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
v=$1 pid=$2 p=$3 full=${4:-}
cat "/proc/$pid/status" >"$p.status"
cg=/sys/fs/cgroup$(sed -n 's/^0:://p' "/proc/$pid/cgroup")
cat "$cg/memory.stat" >"$p.memory.stat"
if is_radosgw "$v"; then
	pod=$(rgwpod)
	# shellcheck disable=SC2016
	find='for c in /proc/[0-9]*/comm; do [ "$(cat "$c" 2>/dev/null)" = radosgw ] && P=$(basename "$(dirname "$c")"); done'
	timeout 60 rooket k -n rook-ceph exec "$pod" -c log-collector -- sh -c "$find; setpriv --reuid=167 --regid=167 --clear-groups cat /proc/\$P/smaps_rollup" >"$p.smaps_rollup"
	timeout 60 rooket k -n rook-ceph exec "$pod" -c log-collector -- sh -c "$find; setpriv --reuid=167 --regid=167 --clear-groups cat /proc/\$P/smaps" >"$p.smaps"
	if [ "$full" = full ]; then
		# shellcheck disable=SC2016
		timeout 60 rooket k -n rook-ceph exec "$pod" -c rgw -- sh -c 'ceph --admin-daemon "$(ls -t /var/run/ceph/ceph-client.rgw.*.asok | head -1)" dump_mempools' >"$p.mempools.json"
	fi
	# The Ceph main radosgw's tcmalloc view, when its admin socket has the command.
	case $v in RM | RMH)
		# shellcheck disable=SC2016
		timeout 60 rooket k -n rook-ceph exec "$pod" -c rgw -- sh -c 'ceph --admin-daemon "$(ls -t /var/run/ceph/ceph-client.rgw.*.asok | head -1)" heap stats' >"$p.heapstats.txt" 2>&1 || true
		;;
	esac
	exit 0
fi
cat "/proc/$pid/smaps_rollup" >"$p.smaps_rollup"
cat "/proc/$pid/smaps" >"$p.smaps"
curl -fs -m 20 "http://$rgwgo_metrics/metrics" -o "$p.metrics.txt"
curl -fs -m 20 "http://$rgwgo_metrics/debug/pprof/heap?debug=1" | grep -E '^# ' >"$p.heap.txt"
if [ "$full" = full ]; then
	"$spike/alloc/gdbheap.sh" "$pid" "$p" || true
fi
