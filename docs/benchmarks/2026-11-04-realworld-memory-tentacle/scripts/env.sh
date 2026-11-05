# shellcheck shell=bash
# env.sh: settings every realworld script sources. After alloc/env.sh and gtcm/env.sh.
here=/tmp/claude-1000/spike-memory/realworld
spike=/tmp/claude-1000/spike-memory
wt=/home/jhoblitt/github/rgw-go/.claude/worktrees/agent-ab289e9e0d1899338
out=$wt/hack/rooket/out/tentacle
s3load=$spike/alloc/s3load
rwtool=$here/bin/rwtool
runs=${RW_RUNS:-$here/runs}
# The cluster the campaign runs against: tentacle for R, M, F and the follow-up, main for RM and RMH
# (RW_CLUSTER=main, the disposable rgw-go-main cluster of the Ceph main image).
cluster=${RW_CLUSTER:-tentacle}
export ROOKET_NAME=rgw-go-$cluster
setup=$here/setup
[ "$cluster" = tentacle ] || setup=$here/setup-$cluster
# is_radosgw VARIANT: R (the Tentacle cluster's own radosgw), RM and RMH (the Ceph main radosgw without and
# with rgw_heap_release_interval=1), which run in their Rook pod; the others are rgw-go on the host.
is_radosgw() { case $1 in R | RM | RMH) return 0 ;; *) return 1 ;; esac; }
rgwgo_endpoint=http://127.0.0.1:7482
rgwgo_metrics=127.0.0.1:9482
site=(--rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore)
# The radosgw pod's rgw container limit (2Gi, memory.swap.max 0), which rgw-go's scope gets too.
memmax=2147483648
tb() { timeout 900 rooket k -n rook-ceph exec deploy/rook-ceph-tools -- "$@"; }
rgwpod() { timeout 60 rooket k -n rook-ceph get pod -l app=rook-ceph-rgw -o jsonpath='{.items[0].metadata.name}'; }
radosgw_endpoint() { cat "$setup/radosgw.endpoint"; }
variant_bin() {
	case $1 in
	# Copies of bin/M and bin/F on disk: run from /tmp's tmpfs, the binary's pages count as RssShmem.
	M | F) echo "/home/jhoblitt/.cache/rgw-go-realworld/$1/rgw-go" ;;
	*)
		echo "no binary for variant $1" >&2
		return 1
		;;
	esac
}
