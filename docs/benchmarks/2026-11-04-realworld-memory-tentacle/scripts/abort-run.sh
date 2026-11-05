#!/bin/bash
# abort-run.sh RUNDIR: stops the campaign and the run in RUNDIR: its stop files, then TERM to the
# campaign, run, mixrun, gcloop and guard scripts, the mix driver and the sampler (matched by their
# executables' paths, never by a pattern this script's own command line holds).
set -uo pipefail
rd=$1
touch "$rd/mix.stop" "$rd/gc.stop" "$rd/guard.stop" "$rd/samples.jsonl.stop"
for p in $(pgrep -f '^/bin/bash /tmp/claude-1000/spike-memory/realworld/(campaign|run|mixrun|gcloop|guard|podpss)\.sh'); do
	kill -TERM "$p" 2>/dev/null
done
for p in $(pgrep -f '^/tmp/claude-1000/spike-memory/realworld/bin/rwtool (mix|sample)'); do
	kill -TERM "$p" 2>/dev/null
done
sleep 3
pgrep -af '^/bin/bash /tmp/claude-1000/spike-memory/realworld/|^/tmp/claude-1000/spike-memory/realworld/bin/rwtool' || echo "no harness process left"
