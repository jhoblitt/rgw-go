#!/bin/bash
# verify-tentacle.sh: re-checks rgw-go-tentacle against its start state after the RM/RMH campaign (which ran on another
# cluster): users, buckets, gc, usage, config dump and the radosgw pod, df per pool, the crash list and the health.
set -uo pipefail
here=/tmp/claude-1000/spike-memory/realworld
export RW_CLUSTER=tentacle
. "$here/env.sh"
"$here/state.sh" >"$here/setup/state-final.txt" 2>&1
filt() { sed -e '/^== date/,+1d' -e '/^== ceph df/,/^== rgw pods/{/^== rgw pods/!d}' -e '/rook-ceph-rgw-ceph-objectstore-a-/d' -e '/^NAME  *READY/d' "$1"; }
if diff <(filt "$here/setup/state-start.txt") <(filt "$here/setup/state-final.txt"); then
	echo "tentacle: users, buckets, gc entries, usage and config dump identical to the start's"
fi
echo "== ceph df per pool (objects)"; tb ceph --connect-timeout=20 df | sed -n '/POOLS/,$p'
echo "== crash ls"; tb ceph --connect-timeout=20 crash ls | wc -l
echo "== health"; tb ceph --connect-timeout=20 health
echo "== osd, pgs"; tb ceph --connect-timeout=20 -s | grep -E "osd:|pgs:|objects:|usage:"
echo "== osd used"; tb ceph --connect-timeout=10 tell osd.0 perf dump | jq -r '(.bluestore.allocated + .bluefs.db_used_bytes)'
echo "== pods"; timeout 60 rooket k -n rook-ceph get pods | grep -E "osd-0|rgw|operator"
