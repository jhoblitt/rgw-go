#!/bin/bash
# run.sh VARIANT LOAD REP: one measured run, as alloc/run.sh. It records the load average, starts the
# variant's gateway fresh (gw-start.sh), leaves it idle 15 s, samples it once a second (rwtool sample;
# for R also podpss.sh), runs LOAD's phases, each between two reads of the gateway cgroup's cpu.stat,
# snapshots it after each phase (snap.sh), and for the large and mix loads samples 600 s of idle with
# snapshots at the idle points. Then it stops sampling, takes a full snapshot, removes the run's
# objects through the same gateway, stops rgw-go and collects the garbage. Every phase is
# count-bound, so every variant does the same work. Loads:
#   large   32 threads x one 64 MiB PUT; then large_get_waves waves of 32 whole GETs of them; idle
#   vlarge  6 back-to-back 256 MiB PUTs, then vl_get_waves x 6 GETs, at 1 in flight; the same at 2
#   small   64 threads x 400 4 KiB PUTs; then small_get_waves waves of the 25 600 GETs
#   mix     the get set written and the gateway restarted; then the first mix_requests requests of
#           the seeded mix at 32 in flight, garbage collected beside it (gcloop.sh); idle
# Output in runs/LOAD/VARIANT-REP/.
set -euo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
. "$here/params.sh"
v=$1 load=$2 rep=$3
rd=$runs/$load/$v-$rep
rm -rf "$rd"
mkdir -p "$rd/snaps"
echo "$(date -Is) $(cat /proc/loadavg)" >"$rd/loadavg"
M=$((1 << 20))
mark() { echo "$EPOCHREALTIME,$1,$2" >>"$rd/marks.csv"; }
l() { "$s3load" -endpoint "$endpoint" "$@"; }
gc() { tb radosgw-admin gc process --include-all "${site[@]}"; }

if [ "$load" = mix ]; then
	"$here/gw-start.sh" "$v" "$rd/prep"
	read -r _ _ endpoint <"$rd/prep/target"
	mark prep start
	"$rwtool" mix -op prep -endpoint "$endpoint" -bucket rw-mix -threads 16 -out "$rd/prep.json" >"$rd/prep.out" 2>"$rd/prep.err"
	mark prep end
fi
"$here/gw-start.sh" "$v" "$rd"
read -r pid cg endpoint <"$rd/target"
sleep 15
"$here/snap.sh" "$v" "$pid" "$rd/snaps/start"
metrics=()
is_radosgw "$v" || metrics=(-metrics "$rgwgo_metrics")
"$rwtool" sample -pid "$pid" -cgroup "$cg" -out "$rd/samples.jsonl" "${metrics[@]}" &
spid=$!
ppid=
if is_radosgw "$v"; then
	"$here/podpss.sh" "$rd/pss.txt" 3600 &
	ppid=$!
fi
stop_sampling() {
	touch "$rd/samples.jsonl.stop"
	[ -n "$ppid" ] && kill "$ppid" 2>/dev/null
	wait "$spid" 2>/dev/null || true
	[ -n "$ppid" ] && { wait "$ppid" 2>/dev/null || true; }
	ppid=
}
trap stop_sampling EXIT
sleep 2
cpu() { awk '$1 ~ /^(usage|user|system)_usec$/ { printf "%s ", $2 }' "$cg/cpu.stat"; }
echo "phase,t0,t1,usage_usec,user_usec,system_usec" >"$rd/cpu.csv"
# phase NAME CMD...: one measured phase.
phase() {
	local name=$1 b a t0 t1 bu bs bsy au as asy
	shift
	b=$(cpu)
	t0=$EPOCHREALTIME
	mark "$name" start
	"$@" >"$rd/$name.out" 2>"$rd/$name.err"
	mark "$name" end
	t1=$EPOCHREALTIME
	a=$(cpu)
	read -r bu bs bsy <<<"$b"
	read -r au as asy <<<"$a"
	echo "$name,$t0,$t1,$((au - bu)),$((as - bs)),$((asy - bsy))" >>"$rd/cpu.csv"
	sleep 2
	"$here/snap.sh" "$v" "$pid" "$rd/snaps/after-$name"
}
# waves NAME N ARGS...: s3load ARGS N times, one result file a wave.
waves() {
	local name=$1 n=$2 w snp=
	shift 2
	for w in $(seq 1 "$n"); do
		# A snapshot under load, during the wave at the phase's middle.
		if [ "$w" = $(((n + 1) / 2)) ]; then
			(sleep 0.5 && "$here/snap.sh" "$v" "$pid" "$rd/snaps/mid-$name") &
			snp=$!
		fi
		l "$@" -out "$rd/$name-$w.json" >/dev/null
	done
	[ -z "$snp" ] || wait "$snp" || true
	return 0
}
step() {
	local name=$1
	shift
	mark "$name" start
	"$@" >"$rd/$name.out" 2>&1
	mark "$name" end
	sleep 2
}
# idle: samples the idle curve after the last phase, snapshots at each point.
idle() {
	local t0=$EPOCHREALTIME p c0 c1 t1 bu bs bsy au as asy
	c0=$(cpu)
	mark idle start
	for p in "${idle_points[@]}"; do
		while awk -v a="$t0" -v b="$EPOCHREALTIME" -v p="$p" 'BEGIN{exit !(b-a < p)}'; do sleep 0.5; done
		"$here/snap.sh" "$v" "$pid" "$rd/snaps/idle-$p"
	done
	mark idle end
	t1=$EPOCHREALTIME
	c1=$(cpu)
	# The idle's CPU as a row of cpu.csv like a phase's (from 2026-11-04 21:20; earlier runs have none).
	read -r bu bs bsy <<<"$c0"
	read -r au as asy <<<"$c1"
	echo "idle,$t0,$t1,$((au - bu)),$((as - bs)),$((asy - bsy))" >>"$rd/cpu.csv"
}
gcbg=
case $load in
large)
	step prep-bucket l -op mkbucket -bucket rw-large
	phase put64 l -op put -bucket rw-large -threads 32 -objects 1 -size $((64 * M)) -block $((64 * M)) -seconds 600 -out "$rd/put64.json"
	phase get64 waves get64 "$large_get_waves" -op get -bucket rw-large -threads 32 -objects 1 -size $((64 * M)) -block $((64 * M)) -seconds 600
	idle
	;;
vlarge)
	step prep-bucket bash -c "$s3load -endpoint $endpoint -op mkbucket -bucket rw-vl1 && $s3load -endpoint $endpoint -op mkbucket -bucket rw-vl2"
	phase put256x1 l -op put -bucket rw-vl1 -threads 1 -objects 6 -size $((256 * M)) -block $((256 * M)) -seconds 600 -out "$rd/put256x1.json"
	phase get256x1 waves get256x1 "$vl_get_waves" -op get -bucket rw-vl1 -threads 1 -objects 6 -size $((256 * M)) -block $((256 * M)) -seconds 600
	phase put256x2 l -op put -bucket rw-vl2 -threads 2 -objects 3 -size $((256 * M)) -block $((256 * M)) -seconds 600 -out "$rd/put256x2.json"
	phase get256x2 waves get256x2 "$vl_get_waves" -op get -bucket rw-vl2 -threads 2 -objects 3 -size $((256 * M)) -block $((256 * M)) -seconds 600
	sleep 13
	"$here/snap.sh" "$v" "$pid" "$rd/snaps/after-15s"
	;;
small)
	step prep-bucket l -op mkbucket -bucket rw-small
	phase put4k l -op put -bucket rw-small -threads 64 -objects 400 -size 4096 -seconds 600 -out "$rd/put4k.json"
	phase get4k waves get4k "$small_get_waves" -op get -bucket rw-small -threads 64 -objects 400 -size 4096 -seconds 600
	sleep 13
	"$here/snap.sh" "$v" "$pid" "$rd/snaps/after-15s"
	;;
mix)
	rm -f "$rd/gc.stop"
	"$here/gcloop.sh" "$rd/gc.log" "$rd/gc.stop" &
	gcbg=$!
	(sleep 300 && "$here/snap.sh" "$v" "$pid" "$rd/snaps/mid-mix") &
	phase mix "$here/mixrun.sh" "$rd" "$endpoint" "$mix_requests" "$mix_seed"
	touch "$rd/gc.stop"
	idle
	wait "$gcbg" || true
	;;
*)
	echo "unknown load $load" >&2
	exit 1
	;;
esac
stop_sampling
trap - EXIT
"$here/snap.sh" "$v" "$pid" "$rd/snaps/end" full
{
	case $load in
	large) for b in rw-large; do l -op purge -bucket "$b"; l -op rmbucket -bucket "$b"; done ;;
	vlarge) for b in rw-vl1 rw-vl2; do l -op purge -bucket "$b"; l -op rmbucket -bucket "$b"; done ;;
	small) l -op purge -bucket rw-small; l -op rmbucket -bucket rw-small ;;
	mix) l -op purge -bucket rw-mix; l -op rmbucket -bucket rw-mix ;;
	esac
	is_radosgw "$v" || "$here/gw-stop.sh"
	gc
	tb ceph --connect-timeout=20 df | grep -E 'TOTAL|buckets.data'
} >"$rd/cleanup.log" 2>&1
echo "$(date -Is) done $v $load $rep" >"$rd/done"
tail -2 "$rd/cleanup.log"
