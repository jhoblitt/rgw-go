#!/bin/bash
# guard.sh LOG STOP PAUSE MIXSTOP MIXPID: keeps the mix from filling the cluster's one 10 GiB OSD
# (it did once: notes, "INCIDENT"). Every 2 s until STOP exists it reads the OSD's own real-time
# counters (`ceph tell osd.0 perf dump`), not the mgr's lagging statfs: BlueStore's allocated bytes,
# which the overwrites' garbage raises and the collector lowers, and BlueFS's, RocksDB's WAL and
# SSTs, which grow over a run by up to ~1.3 GiB whatever the collector does. Pause and resume follow
# BlueStore's bytes against their value when the guard starts (the mix's data set): above it by
# 2.5 GiB the guard creates PAUSE, which holds the mix's new requests back while the collector
# catches up; within 1 GiB of it it removes PAUSE. It aborts, creating MIXSTOP and killing the mix
# driver MIXPID at once, when BlueStore's bytes pass the start by 4.5 GiB or the two together pass
# 8 GiB. Three failed reads in a row pause the mix.
# History: until 12:15 on 2026-11-04 the marks were fixed totals (5, 3.5, 7 GiB); until 13:10 they
# were relative totals, so BlueFS's growth kept the 30 000-request R mix paused for 19 minutes
# (realworld/aside/mix30000-stuck/R-1).
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
log=$1 stop=$2 pause=$3 mixstop=$4 mixpid=$5
read_counts() { tb ceph --connect-timeout=10 tell osd.0 perf dump 2>/dev/null | jq -r '"\(.bluestore.allocated) \(.bluefs.db_used_bytes)"'; }
read -r base _ < <(read_counts)
[[ "$base" =~ ^[0-9]+$ ]] || base=$((2 << 30))
hi=$((base + (5 << 29))) lo=$((base + (1 << 30))) abort=$((base + (9 << 29))) cap=$((8 << 30))
echo "base_allocated $base hi $hi lo $lo abort $abort total_cap $cap" >>"$log"
echo "t allocated bluefs state" >>"$log"
unknown=0
while [ ! -e "$stop" ]; do
	read -r alloc fs < <(read_counts)
	state=run
	if [[ "$alloc" =~ ^[0-9]+$ && "$fs" =~ ^[0-9]+$ ]]; then
		unknown=0
		if [ "$alloc" -gt "$abort" ] || [ $((alloc + fs)) -gt "$cap" ]; then
			touch "$mixstop" "$pause"
			kill -KILL "$mixpid" 2>/dev/null
			state=abort
		elif [ "$alloc" -gt "$hi" ]; then
			touch "$pause"
		elif [ "$alloc" -lt "$lo" ]; then
			rm -f "$pause"
		fi
		[ -e "$pause" ] && [ "$state" = run ] && state=pause
	else
		unknown=$((unknown + 1))
		state=unknown
		[ "$unknown" -ge 3 ] && touch "$pause"
	fi
	echo "$EPOCHREALTIME ${alloc:-} ${fs:-} $state" >>"$log"
	sleep 2
done
rm -f "$pause"
