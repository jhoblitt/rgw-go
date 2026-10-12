#!/usr/bin/env bash
# Runs the gateway comparison's profiles against one gateway of one release's
# populated rooket cluster, radosgw or the rgw-go on the host, and renders the
# run's REPORT.md with hack/bench/report. Each run writes
# hack/bench/out/<release>/gateway-<gateway>-<UTC time>/:
#
#   env.json                the host, the cluster, the gateway and the settings
#   config.json             the parity options as each gateway's entity holds them
#   <profile>.json, .csv    elbencho's results, one line per measured phase
#   noop-<t>.json           httpload's result at t goroutines
#   <profile>.log           what elbencho, httpload and the preparation printed
#   samples-<profile>.csv   the gateway process once a second (sample.sh)
#   cpu-<profile>.pprof     rgw-go only: a CPU profile of the profile's last phase
#   gctrace-<profile>.log   rgw-go only: the GC lines it logged during the profile
#   run.log                 what ran when
#
# Profiles, in the order given (all of them by default):
#
#   small-put  4 KiB PUTs at each concurrency of BENCH_CONC, BENCH_PUT_OBJECTS per thread
#   small-get  4 KiB GETs at each concurrency, looping over a read set written first
#   noop       httpload's anonymous GET / at each concurrency, which radosgw answers
#              without touching RADOS
#   large-put  64 MiB PUTs, whole and in 8 MiB parts, each removed after it
#   large-get  64 MiB objects read in 4 MiB ranges and whole, looping over a set written first
#   list       ListObjectsV2 over BENCH_LIST_THREADS * BENCH_LIST_OBJECTS keys, from one
#              thread and with one prefix per thread
#
# Every measured phase is bounded by BENCH_SECONDS; a phase that finishes its
# objects first ends early. A read phase loops over objects written for it
# before it starts, so it never reads one that is not there.
#
# The clusters' one OSD holds 10 GiB, and the parity options turn radosgw's
# garbage collector off. Each profile's dataset, the most object data it holds
# at once, is sized from its threads, objects per thread and object size:
# small-put's lower concurrencies overwrite keys its top one writes, the large
# profiles write BENCH_LARGE_THREADS * BENCH_LARGE_OBJECTS objects of 64 MiB at
# a time and remove them before the next write, and list's keys are empty. A
# run starts by collecting any garbage left queued and reading the data pool's
# free space, and refuses to start when a profile's dataset is more than half
# of it; the other half is room for the index and the OSD's own metadata.
# Before each write the run waits for twice that write's dataset to be free.
# After each profile it removes every bench object and collects the garbage,
# so the next profile starts from the space the run started with.
#
# Both gateways run with TCP_NODELAY on, and the run refuses a gateway whose
# frontends leave it off: radosgw's beast sets it only for tcp_nodelay=1
# (rgw_asio_frontend.cc:702-706, :1165 at v19.2.6; :643-647, :1083 at
# v20.2.4), which the chart values give it, and rgw-go keeps Go's unless its
# frontend sets tcp_nodelay to anything else (docs/exclusions.md, "TCP
# options"). env.json records the gateway's frontends and the TCP_NODELAY they
# give.
#
# elbencho signs as the user bench, whose keys are fixed, so that every run
# writes the same commands; it is created through radosgw-admin and removed
# with its buckets whenever the run ends, so that make gate passes after it.
# The run then collects the garbage that leaves and compacts the OSD, so that
# the next run, rgw-go's after radosgw's, starts on an OSD without the
# tombstones this one's deletes left. For rgw-go the script restarts it
# through rgw-go-up.sh with GODEBUG=gctrace=1 and RGW_GO_FLAGS, and stops it
# when the run ends; a radosgw run refuses to start while an rgw-go runs
# against the cluster. Both refuse a gateway whose entity does not hold the
# parity options, which rgw-go-up.sh sets.
#
# Usage: gateway.sh squid|tentacle radosgw|rgw-go [PROFILE...]
#
# Environment:
#   BENCH_SECONDS        each measured phase's time limit (60)
#   BENCH_CONC           the small and noop profiles' concurrencies ("1 8 32 64 128 256")
#   BENCH_PUT_OBJECTS    small-put's objects per thread (3000)
#   BENCH_GET_OBJECTS    small-get's read set, objects per thread at the highest concurrency (400)
#   BENCH_LARGE_THREADS  the large profiles' threads (32)
#   BENCH_LARGE_OBJECTS  the large profiles' objects per thread (1)
#   BENCH_LIST_THREADS   the list profile's threads (64)
#   BENCH_LIST_OBJECTS   the list profile's keys per thread (1600)
#   BENCH_SMOKE=1        a run that only proves the tooling: 5 s phases, BENCH_CONC=8, 50
#                        objects per reader, 4 large threads and 16 keys per list thread;
#                        explicit settings still win. Its numbers measure nothing.
#   BENCH_PLAN=1         print each profile's dataset and the free space it needs against
#                        BENCH_IDLE_AVAIL bytes free, without touching the cluster, and fail
#                        when one needs more
#   RGW_GO_FLAGS         rgw-go serve flags for rgw-go-up.sh, such as --rados-completions=pipe
#   GO_TAGS              build tags (ceph_preview); CGO_* come from the environment
set -euo pipefail

bench_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "${bench_dir}/../.." && pwd)
readonly bench_dir repo
here=${repo}/hack/rooket
readonly script=gateway.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
gateway=${2:-}
[[ "${gateway}" == radosgw || "${gateway}" == rgw-go ]] || die "usage: ${script} squid|tentacle radosgw|rgw-go [PROFILE...]"
shift 2

readonly all_profiles=(small-put small-get noop large-put large-get list)
profiles=("$@")
((${#profiles[@]} > 0)) || profiles=("${all_profiles[@]}")
for p in "${profiles[@]}"; do
	[[ " ${all_profiles[*]} " == *" ${p} "* ]] || die "no profile ${p}; the profiles are ${all_profiles[*]}"
done
readonly profiles

if [[ "${BENCH_SMOKE:-}" == 1 ]]; then
	: "${BENCH_SECONDS:=5}" "${BENCH_CONC:=8}" "${BENCH_GET_OBJECTS:=50}" "${BENCH_LARGE_THREADS:=4}"
	: "${BENCH_LIST_OBJECTS:=16}"
fi
: "${BENCH_SECONDS:=60}" "${BENCH_CONC:=1 8 32 64 128 256}" "${BENCH_PUT_OBJECTS:=3000}" "${BENCH_GET_OBJECTS:=400}"
: "${BENCH_LARGE_THREADS:=32}" "${BENCH_LARGE_OBJECTS:=1}" "${BENCH_LIST_THREADS:=64}" "${BENCH_LIST_OBJECTS:=1600}"
: "${GO_TAGS:=ceph_preview}"
for v in BENCH_SECONDS BENCH_PUT_OBJECTS BENCH_GET_OBJECTS BENCH_LARGE_THREADS BENCH_LARGE_OBJECTS BENCH_LIST_THREADS \
	BENCH_LIST_OBJECTS; do
	[[ "${!v}" =~ ^[1-9][0-9]*$ ]] || die "${v} is '${!v}', not a positive count"
done
read -r -a conc <<<"${BENCH_CONC}"
((${#conc[@]} > 0)) || die "BENCH_CONC names no concurrency"
top=0
for t in "${conc[@]}"; do
	[[ "${t}" =~ ^[1-9][0-9]*$ ]] || die "BENCH_CONC holds '${t}', not a positive count"
	((t > top)) || continue
	top=${t}
done
readonly conc top
read -r -a rgw_go_flags <<<"${RGW_GO_FLAGS:-}"
readonly rgw_go_flags

# The user elbencho signs as. Its keys are fixed, as the s3-tests users' are,
# so that every run's commands are the same; the secret reaches elbencho
# through the environment, never its command line.
readonly bench_uid=bench bench_key=BENCH0000000000000AK
readonly bench_secret=bench-secret-0123456789abcdefghijklmnopqrstuv
# The buckets the profiles write, which the run empties after each profile and
# removes whenever it ends.
readonly buckets=(bench-small bench-large bench-large-mpu bench-list)
readonly obj_small=$((4 << 10)) obj_large=$((64 << 20))

# dataset prints the bytes of object data profile $1 holds at once, from the
# threads, objects per thread and object size its commands below use. A lower
# concurrency's keys are among the top one's, since elbencho names an object
# by its thread's rank and its index, r<rank>-f<index>.
dataset() {
	case "$1" in
	small-put) echo $((top * BENCH_PUT_OBJECTS * obj_small)) ;;
	small-get) echo $((top * BENCH_GET_OBJECTS * obj_small)) ;;
	large-put | large-get) echo $((BENCH_LARGE_THREADS * BENCH_LARGE_OBJECTS * obj_large)) ;;
	noop | list) echo 0 ;;
	*) die "no dataset for profile $1" ;;
	esac
}

# plan prints each profile's dataset and the free space it needs, twice the
# dataset, against $1 bytes free, and fails when one needs more.
plan() { # free
	local p bytes need failed=0 verdict
	for p in "${profiles[@]}"; do
		bytes=$(dataset "${p}")
		need=$((2 * bytes))
		verdict=fits
		if ((need > $1)); then
			verdict="DOES NOT FIT"
			failed=1
		fi
		printf '%-9s dataset %11d B, needs %11d B of %11d B free: %s\n' "${p}" "${bytes}" "${need}" "$1" "${verdict}"
	done
	return "${failed}"
}

if [[ "${BENCH_PLAN:-}" == 1 ]]; then
	[[ "${BENCH_IDLE_AVAIL:-}" =~ ^[1-9][0-9]*$ ]] || die "BENCH_PLAN=1 needs BENCH_IDLE_AVAIL, the data pool's free bytes"
	plan "${BENCH_IDLE_AVAIL}" || die "a profile's dataset is more than half of ${BENCH_IDLE_AVAIL} B: lower its threads or objects"
	exit 0
fi

[[ -f "${out}/ceph.conf" && -f "${out}/image" ]] || die "no ${out}/ceph.conf or ${out}/image: run make cluster-up RELEASE=${release}"
use_manifest_site
data_pool=$(jq -er '.pools.data' "${out}/manifest.json") || die "${out}/manifest.json names no data pool"
readonly data_pool

dir=${repo}/hack/bench/out/${release}/gateway-${gateway}-$(date -u +%Y%m%dT%H%M%SZ)
readonly dir
mkdir -p "${dir}"
readonly log=${dir}/run.log

say() {
	echo "$(date -u +%H:%M:%S) $*" | tee -a "${log}"
}

# Set as the run goes, for the exit trap.
sampler_pid=
pprof_pid=
user_created=0
rgw_go_started=0

# empty_buckets removes every bench bucket's objects through the radosgw with
# elbencho's multi-object delete, a thousand keys a request.
empty_buckets() {
	local have=() b
	for b in "${buckets[@]}"; do
		if admin bucket stats --bucket "${b}" >/dev/null 2>&1; then
			have+=("${b}")
		fi
	done
	((${#have[@]} > 0)) || return 0
	say "removing the objects of ${have[*]}"
	el "${radosgw_endpoint}" --s3multidel 1000 -t "${#have[@]}" "${have[@]}" >>"${log}" 2>&1
}

# remove_bench empties the bench buckets, then removes the user with its
# buckets and the garbage the removal leaves (remove-user.sh), so that make
# gate passes after the run.
remove_bench() {
	local failed=0
	empty_buckets || failed=1
	"${repo}/hack/admin/remove-user.sh" "${release}" "${bench_uid}" >>"${log}" 2>&1 || failed=1
	return "${failed}"
}

# drain_gc runs every entry of the garbage collection queue, expired or not,
# until the queue is empty, for up to three minutes: the parity options leave
# no collector thread to run them.
drain_gc() {
	local deadline=$((SECONDS + 180)) queued
	while :; do
		admin gc process --include-all >>"${log}" 2>&1 || return 1
		queued=$(admin gc list --include-all | jq 'length') || return 1
		((queued > 0)) || return 0
		((SECONDS < deadline)) || return 1
		sleep 5
	done
}

# settle_osd collects the garbage the run's removals left and compacts the
# OSD's RocksDB, which their deletes left full of tombstones, so that the next
# run does not start on them.
settle_osd() {
	drain_gc || return 1
	say "compacting the OSD"
	ceph_cmd tell 'osd.*' compact >>"${log}" 2>&1
}

# pool_avail prints the bytes ceph df says the data pool can still take.
pool_avail() {
	ceph_cmd df -f json | jq -e --arg pool "${data_pool}" '.pools[] | select(.name == $pool) | .stats.max_avail'
}

on_exit() {
	local status=$?
	trap - EXIT
	if [[ -n "${pprof_pid}" ]]; then
		kill "${pprof_pid}" 2>/dev/null || true
	fi
	if [[ -n "${sampler_pid}" ]]; then
		kill -TERM "${sampler_pid}" 2>/dev/null || true
		wait "${sampler_pid}" 2>/dev/null || true
	fi
	if ((status != 0)); then
		say "the run failed with status ${status}"
	fi
	if ((user_created)); then
		if remove_bench; then
			say "removed the user ${bench_uid} and its buckets"
		else
			echo "${script}: removing the user ${bench_uid} and its buckets failed; see ${log}" >&2
			((status != 0)) || status=1
		fi
	fi
	if ((rgw_go_started)); then
		"${here}/rgw-go-down.sh" "${release}" >>"${log}" 2>&1 ||
			echo "${script}: stopping rgw-go failed; see ${log}" >&2
	fi
	if ((user_created)); then
		if settle_osd; then
			say "collected the garbage and compacted the OSD; the data pool can take $(pool_avail || echo '?') B"
		else
			echo "${script}: collecting the garbage or compacting the OSD failed; see ${log}" >&2
			((status != 0)) || status=1
		fi
	fi
	exit "${status}"
}
trap on_exit EXIT

say "gateway comparison: ${gateway} on ${ROOKET_NAME}, profiles ${profiles[*]}, into ${dir}"
"${bench_dir}/elbencho-build.sh" >>"${log}" 2>&1 || die "building elbencho failed; see ${log}"
readonly elbencho=${repo}/hack/bench/out/elbencho httpload=${repo}/hack/bench/out/httpload
if [[ " ${profiles[*]} " == *" noop "* ]]; then
	(cd "${repo}" && go build -o "${httpload}" ./hack/bench/httpload)
fi

use_site
radosgw_endpoint=$(rgw_endpoint "${rgw}")
radosgw_entity=$(rgw_entity "${rgw}")
readonly radosgw_endpoint radosgw_entity rgw_go_entity=client.rgw.rgw-go
radosgw_pod=$(k -n rook-ceph get pods -l app=rook-ceph-rgw -o 'jsonpath={.items[*].metadata.name}')
[[ "${radosgw_pod}" =~ ^[a-z0-9.-]+$ ]] || die "want one radosgw pod, have '${radosgw_pod}'"
readonly radosgw_pod

# Garbage an earlier run left queued would hold space this run's datasets
# need, and would be collected during its phases.
say "collecting queued garbage"
drain_gc || die "the garbage collection queue did not drain in 180 s; see ${log}"
idle=$(pool_avail) || die "ceph df names no pool ${data_pool}"
readonly idle
plan "${idle}" | tee -a "${log}" ||
	die "a profile's dataset is more than half of the ${idle} B the data pool can take: lower its threads or objects"

# frontends_of prints the frontends process $1 was started with: the last
# --rgw-frontends on its command line, which is the one ceph's argument
# parsing keeps.
frontends_of() { # pid
	local arg value=
	while IFS= read -r -d '' arg; do
		case "${arg}" in
		--rgw-frontends=* | --rgw_frontends=*) value=${arg#*=} ;;
		esac
	done <"/proc/$1/cmdline"
	echo "${value}"
}

# no_delay prints on or off, the TCP_NODELAY gateway $1's frontends $2 give
# its connections from their first tcp_nodelay: radosgw's beast turns it on
# only for 1, and rgw-go keeps Go's, on, unless tcp_nodelay names anything
# else.
no_delay() { # gateway frontends
	local words w value=
	read -r -a words <<<"$2"
	for w in "${words[@]}"; do
		if [[ "${w}" == tcp_nodelay || "${w}" == tcp_nodelay=* ]]; then
			value=${w#tcp_nodelay}
			value=${value#=}
			[[ "${value}" == 1 ]] && echo on || echo off
			return
		fi
	done
	[[ "$1" == rgw-go ]] && echo on || echo off
}

# parity_check fails unless the entity holds every parity option: show reads
# what a running radosgw uses, get what the monitors give a client that
# registers no daemon with the manager, as rgw-go does.
parity_check() { # entity show|get
	local kv have
	for kv in "${parity_options[@]}"; do
		have=$(ceph_cmd config "$2" "$1" "${kv%%=*}") || die "ceph config $2 $1 ${kv%%=*} failed"
		[[ "${have}" == "${kv#*=}" ]] ||
			die "${1} holds ${kv%%=*}=${have}, not the parity setting ${kv#*=}: run make rgw-go-up RELEASE=${release} first"
	done
}

case "${gateway}" in
radosgw)
	if [[ -f "${out}/rgw-go.pid" ]] && [[ "$(ps -o comm= -p "$(<"${out}/rgw-go.pid")" 2>/dev/null)" == rgw-go ]]; then
		die "an rgw-go runs against ${ROOKET_NAME}: stop it with make rgw-go-down RELEASE=${release}, so that the radosgw run has the host to itself"
	fi
	parity_check "${radosgw_entity}" show
	frontends=$(jq -r '.metadata["frontend_config#0"] // empty' <<<"${rgw}")
	endpoint=${radosgw_endpoint}
	target=pod:${radosgw_pod}
	;;
rgw-go)
	# rgw-go-up.sh builds bin/rgw-go before it starts it, and under
	# GODEBUG=gctrace=1 the compiler, a Go program too, prints its own GC lines
	# into the build's output. Built here first, without it, the binary is
	# current there and nothing compiles.
	say "building rgw-go"
	(cd "${repo}" && go build "-tags=${GO_TAGS}" -o bin/rgw-go ./cmd/rgw-go) >>"${log}" 2>&1 ||
		die "building rgw-go failed; see ${log}"
	say "restarting rgw-go with GODEBUG=gctrace=1${RGW_GO_FLAGS:+ and ${RGW_GO_FLAGS}}"
	rgw_go_started=1
	GODEBUG=gctrace=1 GO_TAGS="${GO_TAGS}" "${here}/rgw-go-up.sh" "${release}" "${rgw_go_flags[@]}" >>"${log}" 2>&1 ||
		die "rgw-go-up.sh failed; see ${log}"
	parity_check "${radosgw_entity}" show
	parity_check "${rgw_go_entity}" get
	endpoint=$(<"${out}/rgw-go.endpoint")
	[[ -s "${out}/rgw-go.metrics" ]] || die "rgw-go serves no metrics address, so no CPU profile can be taken"
	metrics=$(<"${out}/rgw-go.metrics")
	rgw_go_pid=$(<"${out}/rgw-go.pid")
	frontends=$(frontends_of "${rgw_go_pid}")
	target=pid:${rgw_go_pid}
	;;
esac
[[ -n "${frontends}" ]] || die "cannot tell which frontends ${gateway} serves with"
tcp_nodelay=$(no_delay "${gateway}" "${frontends}")
readonly endpoint target frontends tcp_nodelay
[[ "${tcp_nodelay}" == on ]] ||
	die "${gateway} serves with the frontends '${frontends}', which leave TCP_NODELAY off, and both gateways run with it on: give the radosgw the chart values' rgwCommandFlags with make cluster-up RELEASE=${release}, then make populate RELEASE=${release}"
api_name=$(admin zonegroup get | jq -r '.api_name // empty')
readonly api_name

# config.json: every parity option as each gateway's entity holds it.
config_json() {
	local kv entity mode values=()
	for entity in "${radosgw_entity}" "${rgw_go_entity}"; do
		mode='get'
		[[ "${entity}" != "${radosgw_entity}" ]] || mode='show'
		for kv in "${parity_options[@]}"; do
			values+=("$(jq -cn --arg e "${entity}" --arg m "config ${mode}" --arg o "${kv%%=*}" \
				--arg v "$(ceph_cmd config "${mode}" "${entity}" "${kv%%=*}")" '{entity: $e, source: $m, option: $o, value: $v}')")
		done
	done
	printf '%s\n' "${values[@]}" | jq -s 'group_by(.entity) | map({key: .[0].entity,
		value: {source: .[0].source, options: (map({key: .option, value: .value}) | from_entries)}}) | from_entries'
}
config_json >"${dir}/config.json"

datasets=$(for p in "${profiles[@]}"; do printf '{"%s":%d}\n' "${p}" "$(dataset "${p}")"; done | jq -cs add)
readonly datasets stamp=${repo}/hack/bench/out/elbencho.commit
# shellcheck disable=SC2153 # ROOKET_VERSION is the caller's, when set
jq -n --arg release "${release}" --arg gateway "${gateway}" --arg ceph_version "$(pinned_version)" \
	--arg cluster_image "$(<"${out}/image")" --arg endpoint "${endpoint}" --arg api_name "${api_name}" \
	--arg frontends "${frontends}" --arg tcp_nodelay "${tcp_nodelay}" \
	--argjson pool_max_avail "${idle}" --argjson datasets "${datasets}" \
	--arg sampled "${target}" --arg radosgw_entity "${radosgw_entity}" \
	--arg rgw_go_flags "${RGW_GO_FLAGS:-}" --arg godebug "$([[ "${gateway}" == rgw-go ]] && echo gctrace=1)" \
	--arg gomaxprocs "${GOMAXPROCS:-unset}" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	--arg smoke "${BENCH_SMOKE:-0}" --argjson seconds "${BENCH_SECONDS}" --arg conc "${conc[*]}" \
	--argjson put_objects "${BENCH_PUT_OBJECTS}" --argjson get_objects "${BENCH_GET_OBJECTS}" \
	--argjson large_threads "${BENCH_LARGE_THREADS}" --argjson large_objects "${BENCH_LARGE_OBJECTS}" \
	--argjson list_threads "${BENCH_LIST_THREADS}" --argjson list_objects "${BENCH_LIST_OBJECTS}" \
	--arg elbencho "$("${elbencho}" --version | awk '$2 == "Version:" { print $3 }')" \
	--arg elbencho_commit "$(awk 'NR == 1 { print $1 }' "${stamp}")" \
	--arg elbencho_image "$(awk 'NR == 1 { print $3 }' "${stamp}")" \
	--arg elbencho_packages "$(awk 'NR == 2' "${stamp}")" \
	--arg rooket_version "${ROOKET_VERSION:-$("${rooket}" version 2>/dev/null | head -1)}" \
	--argjson nproc "$(nproc)" --argjson mem_total_kib "$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo)" \
	--arg cpu "$(awk -F': *' '/^model name/ { print $2; exit }' /proc/cpuinfo)" --arg kernel "$(uname -r)" \
	--arg go_version "$(go version)" --arg git_describe "$(git -C "${repo}" describe --always --dirty)" \
	--argjson clk_tck "$(getconf CLK_TCK)" --arg load "$(cut -d' ' -f1-3 /proc/loadavg)" \
	'$ARGS.named' >"${dir}/env.json"

# el runs elbencho against an endpoint as bench, without live statistics.
el() { # endpoint args...
	local ep=$1
	shift
	AWS_SECRET_ACCESS_KEY=${bench_secret} "${elbencho}" --s3endpoints "${ep}" --s3key "${bench_key}" \
		--s3region "${api_name}" --s3nocompress --s3reqtimeout 30000 --nolive "$@"
}

# ensure_user creates the user bench with its fixed keys unless it exists, and
# checks that it holds them; from then on the run removes it when it ends. A
# bench user without those keys is someone else's, and is left alone.
ensure_user() {
	local user
	if ! user=$(admin user info --uid "${bench_uid}" 2>/dev/null); then
		user=$(admin user create --uid "${bench_uid}" --display-name "${bench_uid}" \
			--access-key "${bench_key}" --secret-key "${bench_secret}") || die "creating the user ${bench_uid} failed"
	fi
	jq -e --arg ak "${bench_key}" --arg sk "${bench_secret}" \
		'any(.keys[]; .access_key == $ak and .secret_key == $sk)' <<<"${user}" >/dev/null ||
		die "the user ${bench_uid} exists without the key ${bench_key}; remove it and rerun"
	user_created=1
}
ensure_user

# prep runs an unmeasured elbencho command against the gateway: a bucket to
# create, a set to write for a read phase, objects to remove. The quiet after
# it keeps its CPU out of the samples the next phase's is read between.
prep() { # profile what args...
	local profile=$1 what=$2
	shift 2
	say "${profile}: ${what}"
	el "${endpoint}" "$@" >>"${dir}/${profile}.log" 2>&1 || die "${profile}: ${what} failed; see ${dir}/${profile}.log"
	quiet
}

# pprof_start takes rgw-go's CPU profile over the next BENCH_SECONDS in the
# background, and pprof_wait waits for it.
pprof_start() { # profile
	[[ "${gateway}" == rgw-go ]] || return 0
	curl -fsS -o "${dir}/cpu-$1.pprof" "http://${metrics}/debug/pprof/profile?seconds=${BENCH_SECONDS}" &
	pprof_pid=$!
}
pprof_wait() {
	[[ -n "${pprof_pid}" ]] || return 0
	wait "${pprof_pid}" || die "taking rgw-go's CPU profile failed"
	pprof_pid=
}

# phase runs one measured elbencho phase, appending its results to the
# profile's json and csv; last says it is the profile's last, which rgw-go is
# profiled through.
phase() { # profile last args...
	local profile=$1 last=$2
	shift 2
	say "${profile}: elbencho $*"
	((last == 0)) || pprof_start "${profile}"
	el "${endpoint}" --lat --latpercent --latpercent9s 1 --lathisto --csvfile "${dir}/${profile}.csv" \
		--jsonfile "${dir}/${profile}.json" "$@" 2>&1 | tee -a "${dir}/${profile}.log" ||
		die "${profile}: elbencho $* failed"
	((last == 0)) || pprof_wait
	quiet
}

# quiet leaves the gateway idle for two samples after a phase, so that the
# samples a phase's CPU is read between see little of its neighbours.
quiet() {
	sleep 2
}

# need_space waits up to three minutes, since the space a removal frees reaches
# ceph df a few seconds after it, for the data pool to take twice the dataset
# of profile $1, the run's plan, and fails the run otherwise.
need_space() { # profile
	local deadline=$((SECONDS + 180)) avail bytes
	bytes=$(dataset "$1")
	until avail=$(pool_avail) && ((avail >= 2 * bytes)); do
		((SECONDS < deadline)) || die "$1: the data pool can take ${avail:-unknown} B, under twice the ${bytes} B its dataset holds"
		sleep 10
	done
}

# remove_large removes a large write's objects and collects their tails,
# since the parity options leave nothing else collecting them.
remove_large() { # profile bucket
	prep "$1" "removing the objects of $2" -F -t "${BENCH_LARGE_THREADS}" -n 0 -N "${BENCH_LARGE_OBJECTS}" --nodelerr "$2"
	admin gc process --include-all >>"${dir}/$1.log" 2>&1 || die "$1: radosgw-admin gc process failed"
}

profile_small_put() {
	local t
	need_space small-put
	prep small-put "creating bench-small" -d -t 1 bench-small
	for t in "${conc[@]}"; do
		phase small-put "$([[ "${t}" == "${conc[-1]}" ]] && echo 1 || echo 0)" -w -t "${t}" -n 0 -N "${BENCH_PUT_OBJECTS}" \
			-s 4k -b 4k --timelimit "${BENCH_SECONDS}" bench-small
	done
}

profile_small_get() {
	local t
	need_space small-get
	prep small-get "writing ${top} x ${BENCH_GET_OBJECTS} objects of 4 KiB to read" \
		-d -w -t "${top}" -n 0 -N "${BENCH_GET_OBJECTS}" -s 4k -b 4k bench-small
	for t in "${conc[@]}"; do
		phase small-get "$([[ "${t}" == "${conc[-1]}" ]] && echo 1 || echo 0)" -r -t "${t}" -n 0 -N "${BENCH_GET_OBJECTS}" \
			-s 4k -b 4k --timelimit "${BENCH_SECONDS}" --infloop bench-small
	done
}

profile_noop() {
	local t
	for t in "${conc[@]}"; do
		say "noop: httpload at ${t}"
		[[ "${t}" != "${conc[-1]}" ]] || pprof_start noop
		"${httpload}" -url "${endpoint}/" -threads "${t}" -duration "${BENCH_SECONDS}s" -out "${dir}/noop-${t}.json" \
			2>&1 | tee -a "${dir}/noop.log" || die "noop: httpload at ${t} failed"
		[[ "${t}" != "${conc[-1]}" ]] || pprof_wait
		quiet
	done
}

profile_large_put() {
	prep large-put "creating bench-large and bench-large-mpu" -d -t 1 bench-large bench-large-mpu
	need_space large-put
	phase large-put 0 -w -t "${BENCH_LARGE_THREADS}" -n 0 -N "${BENCH_LARGE_OBJECTS}" -s 64m -b 64m \
		--timelimit "${BENCH_SECONDS}" bench-large
	remove_large large-put bench-large
	need_space large-put
	phase large-put 1 -w -t "${BENCH_LARGE_THREADS}" -n 0 -N "${BENCH_LARGE_OBJECTS}" -s 64m -b 8m \
		--timelimit "${BENCH_SECONDS}" bench-large-mpu
	remove_large large-put bench-large-mpu
}

profile_large_get() {
	need_space large-get
	prep large-get "writing ${BENCH_LARGE_THREADS} x ${BENCH_LARGE_OBJECTS} objects of 64 MiB to read" \
		-d -w -t "${BENCH_LARGE_THREADS}" -n 0 -N "${BENCH_LARGE_OBJECTS}" -s 64m -b 64m bench-large
	phase large-get 0 -r -t "${BENCH_LARGE_THREADS}" -n 0 -N "${BENCH_LARGE_OBJECTS}" -s 64m -b 4m \
		--timelimit "${BENCH_SECONDS}" --infloop bench-large
	phase large-get 1 -r -t "${BENCH_LARGE_THREADS}" -n 0 -N "${BENCH_LARGE_OBJECTS}" -s 64m -b 64m \
		--timelimit "${BENCH_SECONDS}" --infloop bench-large
	remove_large large-get bench-large
}

profile_list() {
	local keys=$((BENCH_LIST_THREADS * BENCH_LIST_OBJECTS))
	prep list "writing ${keys} empty objects to list" \
		-d -w -t "${BENCH_LIST_THREADS}" -n 0 -N "${BENCH_LIST_OBJECTS}" -s 0 bench-list
	phase list 0 --s3listobj "${keys}" -t 1 --timelimit "${BENCH_SECONDS}" --infloop bench-list
	phase list 1 --s3listobjpar -t "${BENCH_LIST_THREADS}" -n 0 -N "${BENCH_LIST_OBJECTS}" \
		--timelimit "${BENCH_SECONDS}" --infloop bench-list
}

say "endpoint ${endpoint} (api_name ${api_name}), sampling ${target}"
for p in "${profiles[@]}"; do
	started=${SECONDS}
	before=0
	if [[ "${gateway}" == rgw-go ]]; then
		before=$(wc -l <"${out}/rgw-go.log")
	fi
	"${bench_dir}/sample.sh" "${target}" "${dir}/samples-${p}.csv" 2>>"${log}" &
	sampler_pid=$!
	# Samples before the first phase too, so that its CPU is read between
	# samples that bracket it.
	quiet
	"profile_${p//-/_}"
	kill -TERM "${sampler_pid}"
	wait "${sampler_pid}" || die "${p}: the sampler of ${target} failed; see ${log}"
	sampler_pid=
	if [[ "${gateway}" == rgw-go ]]; then
		# GODEBUG=gctrace=1 prints one line per GC cycle to rgw-go's stderr,
		# which rgw-go.log holds beside its JSON log.
		tail -n "+$((before + 1))" "${out}/rgw-go.log" | grep -E '^gc [0-9]+ @' >"${dir}/gctrace-${p}.log" || true
	fi
	say "${p}: done in $((SECONDS - started)) s"
	# The next profile starts from the space the run started with.
	started=${SECONDS}
	empty_buckets || die "${p}: removing the bench objects failed; see ${log}"
	drain_gc || die "${p}: the garbage collection queue did not drain in 180 s; see ${log}"
	say "${p}: removed its objects and collected the garbage in $((SECONDS - started)) s"
done

(cd "${repo}" && go run "-tags=${GO_TAGS}" ./hack/bench/report gateway --dir "${dir}") | tee -a "${log}"
