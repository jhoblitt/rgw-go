#!/bin/bash
# campaign-main.sh: starts the RM / RMH campaign (main-plan.txt) on rgw-go-main, detached: campaign.sh with
# RW_CLUSTER=main, runs in realworld/runs-main/, the idle curve at the campaign's five points, log in
# main-plan.txt.log. Run it once; campaign.sh skips runs that have a `done` file, so a stopped campaign resumes.
here=/tmp/claude-1000/spike-memory/realworld
export RW_CLUSTER=main RW_RUNS=$here/runs-main
mkdir -p "$RW_RUNS"
cd "$here" || exit 1
nohup setsid ./campaign.sh "$here/main-plan.txt" >>"$here/main-plan.txt.out" 2>&1 &
echo "campaign pid $!"
