#!/bin/bash
# chain-fu.sh: waits for the main campaign to end, then runs the follow-up plan, one run at a time,
# into runs-fu/ with a 30 s idle after the large load. Stops without starting if the main campaign
# failed or stopped.
set -uo pipefail
here=/tmp/claude-1000/spike-memory/realworld
until grep -q 'campaign done\|FAILED\|STOP' "$here/plan.txt.log"; do sleep 20; done
if ! grep -q 'campaign done' "$here/plan.txt.log"; then
	echo "$(date -Is) main campaign did not finish; follow-up not started" >>"$here/fu-plan.txt.log"
	exit 1
fi
RW_RUNS=$here/runs-fu idle_points_list=30 exec "$here/campaign-fu.sh" "$here/fu-plan.txt"
