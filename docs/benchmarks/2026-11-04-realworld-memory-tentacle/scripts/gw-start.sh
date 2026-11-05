#!/bin/bash
# gw-start.sh VARIANT RUNDIR: starts the variant's gateway fresh and writes "PID CGROUPDIR ENDPOINT"
# to RUNDIR/target, as alloc/gw-start.sh. R: the radosgw pod is deleted and its ReplicaSet recreates
# it from the unchanged deployment. M or F: any rgw-go is stopped, then the variant's binary starts
# with rgw-go-up.sh's command line in a user systemd scope of its own whose MemoryMax and
# MemorySwapMax are the radosgw container's (2 GiB, no swap), so internal/memlimit sizes the Go
# memory limit from it; GOMAXPROCS, GOFLAGS and every allocator or Go variable unset,
# GODEBUG=gctrace=1. The environment, libraries, scope limits and the memory-limit and budget log
# lines the process actually has are recorded in RUNDIR/start.txt.
set -euo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
v=$1 rd=$2
mkdir -p "$rd"
if is_radosgw "$v"; then
	endpoint=$(radosgw_endpoint)
	# RMH runs with the heap release on, RM and R without: the option is set in the cluster's config
	# store (the cluster is disposable) before the pod restarts, so the new radosgw starts with it.
	case $v in
	RM) tb ceph --connect-timeout=20 config rm client.rgw.ceph.objectstore.a rgw_heap_release_interval ;;
	RMH) tb ceph --connect-timeout=20 config set client.rgw.ceph.objectstore.a rgw_heap_release_interval 1 ;;
	esac
	old=$(rgwpod)
	timeout 180 rooket k -n rook-ceph delete pod "$old" --wait=true >/dev/null
	timeout 300 rooket k -n rook-ceph rollout status deploy -l app=rook-ceph-rgw --timeout=280s >/dev/null
	for _ in $(seq 1 180); do curl -fs -m 2 -o /dev/null "$endpoint/" && break; sleep 1; done
	# The old radosgw may still be exiting; wait for exactly one.
	for _ in $(seq 1 60); do
		[ "$("$spike/rgwhost.sh" "$cluster" | wc -l)" = 1 ] && break
		sleep 1
	done
	read -r pid cg < <("$spike/rgwhost.sh" "$cluster")
	echo "$pid $cg $endpoint" >"$rd/target"
	{
		echo "deleted pod $old; now $(rgwpod)"
		echo "pid $pid cgroup $cg"
		echo "memory.max $(cat "$cg/memory.max") memory.swap.max $(cat "$cg/memory.swap.max") memory.high $(cat "$cg/memory.high")"
		echo "threads at start: $(grep Threads "/proc/$pid/status")"
		case $v in RM | RMH)
			echo "== the radosgw's own view (admin socket config get)"
			"$spike/rgwpod.sh" "$cluster" 'a=$(ls -t /var/run/ceph/ceph-client.rgw.*.asok | head -1); for o in rgw_heap_release_interval rgw_heap_release_bytes log_to_file; do ceph --admin-daemon "$a" config get $o; done; ceph --version' 2>&1 || true
			;;
		esac
	} >"$rd/start.txt"
	exit 0
fi
"$here/gw-stop.sh" >/dev/null
bin=$(variant_bin "$v")
unit=rw-$v-$(basename "$(dirname "$rd")")-$(basename "$rd")-$$
log=$rd/rgw-go.log
env -u GOMAXPROCS -u GOFLAGS -u GOMEMLIMIT -u GOGC -u CGO_CFLAGS -u CGO_LDFLAGS -u LD_PRELOAD -u MALLOC_ARENA_MAX \
	-u GLIBC_TUNABLES -u RGW_GO_MEMORY_LIMIT_RATIO -u RGW_GO_MEMORY_BUDGET_RATIO \
	RGW_GO_LOG_FORMAT=json GODEBUG=gctrace=1 \
	nohup systemd-run --user --scope --quiet --unit="$unit" -p MemoryAccounting=yes \
	-p MemoryMax="$memmax" -p MemorySwapMax=0 \
	"$bin" serve --metrics-addr="$rgwgo_metrics" -- \
	--id rgw.rgw-go --keyring "$out/ceph.client.rgw.rgw-go.keyring" -c "$out/ceph.conf" \
	"${site[@]}" "--rgw-frontends=beast endpoint=127.0.0.1:7482" >"$log" 2>&1 &
pid=
for _ in $(seq 1 120); do
	pid=$(pgrep -n -x rgw-go || true)
	if [ -n "$pid" ] && curl -fs -m 2 -o /dev/null "$rgwgo_endpoint/"; then break; fi
	sleep 0.5
done
[ -n "$pid" ] || { echo "rgw-go did not start; see $log" >&2; exit 1; }
echo "$pid" >"$out/rgw-go.pid"
cg=/sys/fs/cgroup$(sed -n 's/^0:://p' "/proc/$pid/cgroup")
case $cg in *"$unit"*) ;; *) echo "rgw-go $pid is not in scope $unit: $cg" >&2; exit 1 ;; esac
echo "$pid $cg $rgwgo_endpoint" >"$rd/target"
{
	echo "variant $v unit $unit pid $pid cgroup $cg"
	echo "binary $(readlink -f "/proc/$pid/exe") sha256 $(sha256sum "$bin" | cut -c1-64)"
	echo "memory.max $(cat "$cg/memory.max") memory.swap.max $(cat "$cg/memory.swap.max") memory.high $(cat "$cg/memory.high")"
	echo "== process environment (allocator, Go)"
	tr '\0' '\n' <"/proc/$pid/environ" | grep -E '^(LD_PRELOAD|MALLOC_|GLIBC_TUNABLES|TCMALLOC_|GO[A-Z]*|GODEBUG|RGW_GO_)' || echo "(none)"
	echo "== allocator and Ceph libraries mapped"
	grep -E 'tcmalloc|libunwind|librados|libceph' "/proc/$pid/maps" | awk '{print $6}' | sort -u
	echo "== threads at start: $(grep Threads "/proc/$pid/status")"
	echo "== memory limit and budget log lines"
	grep -E 'go memory limit|memory budget' "$log" || echo "(none yet)"
} >"$rd/start.txt" 2>&1
