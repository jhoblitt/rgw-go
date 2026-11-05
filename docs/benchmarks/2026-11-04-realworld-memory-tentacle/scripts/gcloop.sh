#!/bin/bash
# gcloop.sh LOG STOP: during the mix, collects the garbage its overwrites leave (the parity options
# turn the gateways' own collector off) with back-to-back `radosgw-admin gc process --include-all`
# passes in the toolbox, 64 deletes at a time, until STOP exists, logging each pass's start and
# length. guard.sh watches the OSD's fill beside it.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
log=$1 stop=$2
echo "t0 secs" >>"$log"
while [ ! -e "$stop" ]; do
	t0=$EPOCHREALTIME
	tb radosgw-admin gc process --include-all --rgw-gc-max-concurrent-io=64 "${site[@]}" >/dev/null 2>&1
	t1=$EPOCHREALTIME
	echo "$t0 $(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.1f", b-a}')" >>"$log"
	sleep 1
done
