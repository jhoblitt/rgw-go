#!/bin/bash
# campaign.sh PLAN: runs the plan's lines "VARIANT LOAD REP" one at a time with run.sh, skipping a
# run whose directory already has `done` (so a stopped campaign resumes), and checks the cluster
# before each: osd.0 up and the OSD's use under 3 GiB, else it stops. Logs to PLAN.log.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
plan=$1
log=$plan.log
while read -r v load rep; do
	[ -z "$v" ] && continue
	case $v in \#*) continue ;; esac
	if [ -e "$runs/$load/$v-$rep/done" ]; then
		echo "$(date -Is) skip $v $load $rep (done)" >>"$log"
		continue
	fi
	# A mix's guard sets its marks from the OSD's use at its start, and RocksDB's WAL and SSTs grow
	# across runs; a compaction first gives every mix the same footing.
	if [ "$load" = mix ]; then
		echo "$(date -Is) compact osd.0: $(tb ceph --connect-timeout=10 tell osd.0 compact 2>&1 | tr '\n' ' ')" >>"$log"
	fi
	up=$(tb ceph --connect-timeout=10 osd dump --format json 2>/dev/null | jq -r '.osds[0].up')
	used=$(tb ceph --connect-timeout=10 tell osd.0 perf dump 2>/dev/null | jq -r '(.bluestore.allocated + .bluefs.db_used_bytes)')
	if [ "$up" != 1 ] || [ -z "$used" ] || [ "$used" = null ] || [ "$used" -gt $((4 << 30)) ]; then
		echo "$(date -Is) STOP before $v $load $rep: osd up=$up used=$used" >>"$log"
		exit 1
	fi
	echo "$(date -Is) start $v $load $rep (osd used $used) $(cat /proc/loadavg)" >>"$log"
	if "$here/run.sh" "$v" "$load" "$rep" >>"$log" 2>&1; then
		echo "$(date -Is) end $v $load $rep" >>"$log"
	else
		echo "$(date -Is) FAILED $v $load $rep" >>"$log"
		"$here/gw-stop.sh" >>"$log" 2>&1
		exit 1
	fi
done <"$plan"
echo "$(date -Is) campaign done" >>"$log"
