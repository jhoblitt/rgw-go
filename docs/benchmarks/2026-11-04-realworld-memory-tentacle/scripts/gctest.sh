#!/bin/bash
# gctest.sh CONCURRENCY: measures how fast `radosgw-admin gc process --include-all` collects the
# garbage of 32 overwritten 64 MiB objects (2 GiB, 480 tail objects), with rgw_gc_max_concurrent_io
# given on its command line, through a fresh rgw-go M. Leaves nothing behind.
set -euo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
c=$1
rd=$here/gctest/c$c
rm -rf "$rd"
mkdir -p "$rd"
"$here/gw-start.sh" M "$rd"
read -r _ _ endpoint <"$rd/target"
M=$((1 << 20))
l() { "$s3load" -endpoint "$endpoint" "$@"; }
l -op mkbucket -bucket rw-gctest >/dev/null
l -op put -bucket rw-gctest -threads 32 -objects 1 -size $((64 * M)) -block $((64 * M)) -seconds 300 >/dev/null
l -op put -bucket rw-gctest -threads 32 -objects 1 -size $((64 * M)) -block $((64 * M)) -seconds 300 >/dev/null
echo "gc entries: $(tb radosgw-admin gc list --include-all "${site[@]}" | grep -c '"tag"')"
tb ceph --connect-timeout=20 df | grep -E 'TOTAL'
t0=$EPOCHREALTIME
tb radosgw-admin gc process --include-all --rgw-gc-max-concurrent-io="$c" "${site[@]}"
t1=$EPOCHREALTIME
echo "gc process with concurrency $c took $(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.1f", b-a}') s"
l -op purge -bucket rw-gctest >/dev/null
l -op rmbucket -bucket rw-gctest >/dev/null
"$here/gw-stop.sh" >/dev/null
tb radosgw-admin gc process --include-all "${site[@]}"
echo "gc entries after: $(tb radosgw-admin gc list --include-all "${site[@]}" | grep -c '"tag"')"
