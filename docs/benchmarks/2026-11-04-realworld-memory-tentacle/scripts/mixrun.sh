#!/bin/bash
# mixrun.sh RUNDIR ENDPOINT REQUESTS SEED: the mix driver with guard.sh watching the OSD beside it;
# exits with the driver's status.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
rd=$1 endpoint=$2 n=$3 seed=$4
rm -f "$rd/mix.pause" "$rd/mix.stop" "$rd/guard.stop"
"$rwtool" mix -op run -endpoint "$endpoint" -bucket rw-mix -threads 32 -requests "$n" -seed "$seed" \
	-out "$rd/mix.json" -timeline "$rd/mix-timeline.csv" -stop "$rd/mix.stop" -pause "$rd/mix.pause" &
mp=$!
"$here/guard.sh" "$rd/guard.log" "$rd/guard.stop" "$rd/mix.pause" "$rd/mix.stop" "$mp" &
gp=$!
wait "$mp"
rc=$?
touch "$rd/guard.stop"
wait "$gp"
exit "$rc"
