#!/bin/bash
# gw-stop.sh: stops every rgw-go on the host with TERM (KILL after 30 s), as alloc/gw-stop.sh and
# rgw-go-down.sh do, and removes the pid file; its scope ends with it.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
for pid in $(pgrep -x rgw-go); do
	kill -TERM "$pid"
	for _ in $(seq 1 30); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
	kill -0 "$pid" 2>/dev/null && kill -KILL "$pid"
	echo "stopped rgw-go $pid"
done
rm -f "$out/rgw-go.pid"
