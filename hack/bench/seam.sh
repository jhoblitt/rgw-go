#!/usr/bin/env bash
# Sweeps the seam microbenchmark (test/bench/seam) against one release's
# rooket cluster and measures its floor with rados bench, run in a container
# on the host where the Go process runs. Writes
# hack/bench/out/<release>/seam-<UTC time>/ and renders its REPORT.md with
# hack/bench/report.
#
# The floor's rados comes from the floor image, the Ceph image of the release
# of the librados the benchmark binary links, so that both sides use one
# client release; the sweep stops when no such image can be found. Where the
# cluster's own image is another release, its rados bench also runs once at
# each cell the criteria compare, for comparison only.
#
# Usage: seam.sh squid|tentacle
#
# Each cell runs in each mode in a benchmark process of its own: the runtime
# keeps the OS threads a process grows, so a later cell of the same process
# would start from an earlier one's threads, and go-ceph's completion
# notifier is process-wide. The head and index shapes run at CONC, and read4k
# and write4k at the concurrencies of CONC the throughput and latency
# criteria do not compare, each followed by one floor run. Each cell the
# criteria compare, read4k and write4k at 64 and 256 and read4m and write4m
# at BENCH_LARGE_CONC, runs between two floor runs, one just before its three
# modes and one just after, so that the floor brackets the cell rather than
# running long after it. Then, per asynchronous mode, write4m runs at 64 in
# flight past the byte budget, once with the in-flight limiter's bounds above
# what the cell reaches and once with them derived from the objecter
# throttle; and, per mode, read4k runs at 256 under a CPU profile.
#
# A 4 MiB write cell or floor run writes BENCH_WRITE4M_OBJECTS objects, not
# a time window, so that both sides write the same bytes, and starts only
# once the pool reports BENCH_MIN_AVAIL_GIB available. Each rand floor reads
# among conc objects that a write wrote for it, as each seam worker reads its
# own object.
#
# Environment:
#   BENCH_TIME              benchtime of each time-bounded cell (20s)
#   BENCH_FLOOR_SECONDS     seconds of each time-bounded rados bench run (20)
#   CONC                    concurrencies of the 4 KiB, head and index shapes (1,16,64,256,512)
#   BENCH_LARGE_CONC        concurrencies of read4m and write4m (1,4,16); empty skips them
#   BENCH_WRITE4M_OBJECTS   objects per 4 MiB write cell and floor run (1000, 3.9 GiB)
#   BENCH_OVER_OBJECTS      objects per over-budget cell (640)
#   BENCH_MIN_AVAIL_GIB     pool space a 4 MiB write waits for (5)
#   BENCH_TIMEOUT           go test -timeout of each benchmark process (2h)
#   BENCH_QUICK=1           5s, 5, CONC=16,256, BENCH_LARGE_CONC=16, 100 and 128 objects
#   GO_TAGS                 build tags besides integration (ceph_preview)
#   BENCH_FLOOR_IMAGE       floor image (quay.io/ceph/ceph:v<linked librados version>); must run that release
#   ROOKET_ENGINE           container engine that runs rados bench (podman)
#   RGW_GO_TEST_CEPH_CONF   client config (hack/rooket/out/<release>/ceph.conf)
set -euo pipefail

bench_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "${bench_dir}/../.." && pwd)
readonly bench_dir repo
here=${repo}/hack/rooket
readonly script=seam.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"

if [[ "${BENCH_QUICK:-}" == 1 ]]; then
	: "${BENCH_TIME:=5s}" "${BENCH_FLOOR_SECONDS:=5}" "${CONC:=16,256}" "${BENCH_LARGE_CONC=16}"
	: "${BENCH_WRITE4M_OBJECTS:=100}" "${BENCH_OVER_OBJECTS:=128}"
fi
: "${BENCH_TIME:=20s}" "${BENCH_FLOOR_SECONDS:=20}" "${CONC:=1,16,64,256,512}" "${BENCH_LARGE_CONC=1,4,16}"
: "${BENCH_WRITE4M_OBJECTS:=1000}" "${BENCH_OVER_OBJECTS:=640}" "${BENCH_MIN_AVAIL_GIB:=5}"
: "${BENCH_TIMEOUT:=2h}" "${GO_TAGS:=ceph_preview}"
engine=${ROOKET_ENGINE:-podman}
conf=${RGW_GO_TEST_CEPH_CONF:-${out}/ceph.conf}
[[ -f "${conf}" && -f "${out}/image" ]] || die "no ${conf} or ${out}/image: run make cluster-up RELEASE=${release}"
cluster_image=$(cat "${out}/image")
readonly pool=rgw-go-test
# Bounds no cell reaches, which leave submission to the objecter throttle.
readonly no_limit=(-inflight-ops=1048576 -inflight-bytes=1099511627776)

dir=${repo}/hack/bench/out/${release}/seam-$(date -u +%Y%m%dT%H%M%SZ)
bin=${repo}/hack/bench/out/${release}/seam.test
mkdir -p "${dir}"
log=${dir}/sweep.log
noise=${dir}/noise.log

say() {
	echo "$(date -u +%H:%M:%S) $*" | tee -a "${log}"
}

# sample_noise appends the load and the names of the processes that used 5%
# of a CPU or more over one second to noise.log every 15 s, so that a busy
# host shows beside the cell it touched.
sample_noise() {
	while :; do
		{
			echo "== $(date -u +%H:%M:%S) load $(cut -d' ' -f1-3 /proc/loadavg)"
			top -b -n 2 -d 1 -w 200 | awk '/^top -/ { frame++ } frame == 2 && /^ *[0-9]+ / && $9 + 0 >= 5 { print $9 "%", $12 }' | head -12
		} >>"${noise}"
		sleep 14
	done
}
sample_noise &
sampler=$!
trap 'kill "${sampler}" 2>/dev/null || true' EXIT

# pool_avail prints the bytes ceph df says the pool can still take.
pool_avail() {
	ceph_cmd df -f json | jq -e --arg pool "${pool}" '.pools[] | select(.name == $pool) | .stats.max_avail'
}

# wait_avail waits up to three minutes for the pool to report
# BENCH_MIN_AVAIL_GIB available, since the space a removal frees reaches
# ceph df a few seconds after it.
wait_avail() {
	local need=$((BENCH_MIN_AVAIL_GIB << 30)) deadline=$((SECONDS + 180)) avail
	until avail=$(pool_avail) && ((avail >= need)); do
		if ((SECONDS >= deadline)); then
			say "the pool has ${avail:-unknown} bytes available, under ${BENCH_MIN_AVAIL_GIB} GiB"
			return 1
		fi
		sleep 10
	done
	say "the pool has ${avail} bytes available"
}

# bench runs one benchmark process in mode, appending its cells to results
# when one is given; the arguments after it are the test binary's.
bench() {
	local mode=$1 results=$2
	shift 2
	local args=(-test.run '^$' -test.bench BenchmarkSeam -test.timeout "${BENCH_TIMEOUT}"
		-test.benchtime "${BENCH_TIME}" -mode="${mode}")
	[[ -z "${results}" ]] || args+=(-results="${results}")
	say "bench ${mode}: $* (load $(cut -d' ' -f1-3 /proc/loadavg))"
	RGW_GO_TEST_CEPH_CONF="${conf}" "${bin}" "${args[@]}" "$@" 2>&1 | tee -a "${dir}/bench-${mode}.log"
	say "bench ${mode} done (load $(cut -d' ' -f1-3 /proc/loadavg))"
}

rados_cmd() { # image args...
	local image=$1
	shift
	# rados bench names its objects after the host name, so every run takes
	# the same one: rand and cleanup find what an earlier write wrote.
	"${engine}" run --rm --network host --hostname rgw-go-bench-floor -v "${out}:/etc/ceph:ro" "${image}" \
		rados -c /etc/ceph/ceph.conf -k /etc/ceph/ceph.client.admin.keyring -p "${pool}" "$@"
}

# rados_bench runs rados bench from image in namespace bench-floor-<size>
# and appends its summary to floor.jsonl as the shape's floor. Its other
# arguments are the shape, op, size, conc, seconds, max objects (0 for none),
# the objects a rand reads among (0 for a write), the run name, and the
# bracket: before or after for a run next to a cell the criteria compare,
# empty otherwise.
rados_bench() {
	local image=$1 shape=$2 op=$3 size=$4 conc=$5 seconds=$6 max=$7 objects=$8 run=$9 bracket=${10} raw summary
	local args=(-N "bench-floor-${size}" bench "${seconds}" "${op}" -t "${conc}" --run-name "${run}")
	if [[ "${op}" == write ]]; then
		args+=(-b "${size}" --no-cleanup)
		((max == 0)) || args+=(--max-objects "${max}")
	else
		args+=(--no-verify)
	fi
	raw=${dir}/${run}.txt
	say "rados from ${image}: ${args[*]} (load $(cut -d' ' -f1-3 /proc/loadavg))"
	rados_cmd "${image}" "${args[@]}" >"${raw}" 2>&1 || die "rados bench failed: $(tail -5 "${raw}")"
	# The summary's Average IOPS is truncated to an integer, so the rate is
	# the operations made over the time run.
	summary=$(awk '
		/^Total time run:/ { t = $NF }
		/^Total (writes|reads) made:/ { n = $NF }
		/^Bandwidth \(MB\/sec\):/ { bw = $NF }
		/^Average Latency\(s\):/ { lat = $NF }
		END { if (t == "" || n == "" || bw == "" || lat == "") exit 1; print t, n, bw, lat }' "${raw}") ||
		die "no summary in ${raw}"
	read -r t n bw lat <<<"${summary}"
	jq -cn --arg image "${image}" --arg shape "${shape}" --arg op "${op}" --argjson size "${size}" --argjson conc "${conc}" \
		--arg bracket "${bracket}" --argjson seconds "${seconds}" --argjson max "${max}" --argjson objects "${objects}" \
		--argjson t "${t}" --argjson n "${n}" --argjson bw "${bw}" --argjson lat "${lat}" '{
			kind: "floor", tool: "rados bench", image: $image, shape: $shape, op: $op, size: $size, conc: $conc,
			bracket: $bracket, seconds: $seconds, max_objects: $max, objects: $objects, ops: $n, elapsed_s: $t,
			ops_per_sec: ($n / $t), mean_us: ($lat * 1e6), bandwidth_mbps: $bw
		}' >>"${dir}/floor.jsonl"
	say "floor ${shape} at ${conc}: $(tail -1 "${dir}/floor.jsonl")"
}

rados_cleanup() { # image size run
	rados_cmd "$1" -N "bench-floor-$2" cleanup --run-name "$3" -t 64 >>"${log}" 2>&1 || die "rados cleanup of $3 failed"
}

# floor_run measures shape's floor at conc once with image's rados and
# removes what it wrote: a write over BENCH_FLOOR_SECONDS, or of
# BENCH_WRITE4M_OBJECTS objects at 4 MiB, or a rand over conc objects written
# for it. bracket is before or after for a run next to a cell the criteria
# compare, empty otherwise.
floor_run() { # image shape conc bracket
	local image=$1 shape=$2 conc=$3 bracket=$4 size=4096 run
	[[ "${shape}" != *4m ]] || size=4194304
	run=floor-${shape}-${conc}-${bracket:-single}-${image##*:}
	case "${shape}" in
	write4k)
		rados_bench "${image}" "${shape}" write "${size}" "${conc}" "${BENCH_FLOOR_SECONDS}" 0 0 "${run}" "${bracket}"
		;;
	write4m)
		wait_avail || die "not enough space for the write4m floor at ${conc}"
		rados_bench "${image}" "${shape}" write "${size}" "${conc}" 600 "${BENCH_WRITE4M_OBJECTS}" 0 "${run}" "${bracket}"
		;;
	read4k | read4m)
		rados_cmd "${image}" -N "bench-floor-${size}" bench 600 write -t "${conc}" -b "${size}" --max-objects "${conc}" \
			--no-cleanup --run-name "${run}" >>"${log}" 2>&1 || die "writing the rand set of ${run} failed"
		rados_bench "${image}" "${shape}" rand "${size}" "${conc}" "${BENCH_FLOOR_SECONDS}" 0 "${conc}" "${run}" "${bracket}"
		;;
	*) die "no floor for ${shape}" ;;
	esac
	rados_cleanup "${image}" "${size}" "${run}"
}

# judged_cell runs a cell the criteria compare: its floor, the cell in each
# mode in a process of its own, and its floor again; then, where the cluster
# runs another release, the cluster image's floor once.
judged_cell() { # shape conc
	local shape=$1 conc=$2 mode extra=()
	if [[ "${shape}" == write4m ]]; then
		extra=(-test.benchtime="${BENCH_WRITE4M_OBJECTS}x")
	fi
	floor_run "${floor_image}" "${shape}" "${conc}" before
	for mode in sync callback pipe; do
		if [[ "${shape}" == write4m ]]; then
			wait_avail || die "not enough space for write4m at ${conc} in ${mode} mode"
		fi
		bench "${mode}" "${dir}/seam-${mode}.jsonl" -shapes="${shape}" -conc="${conc}" "${extra[@]}"
	done
	floor_run "${floor_image}" "${shape}" "${conc}" after
	if [[ "${cluster_image}" != "${floor_image}" ]]; then
		floor_run "${cluster_image}" "${shape}" "${conc}" ""
	fi
}

# cell_modes runs a cell in each mode, each in a benchmark process of its
# own, so that the cell's thread growth is its own and not what an earlier
# cell of its process left.
cell_modes() { # shape conc
	local shape=$1 conc=$2 mode
	for mode in sync callback pipe; do
		bench "${mode}" "${dir}/seam-${mode}.jsonl" -shapes="${shape}" -conc="${conc}"
	done
}

conc_words() {
	echo "${1//,/ }"
}

# The concurrencies of CONC at which the throughput and latency criteria
# compare read4k and write4k, and the rest.
judged_small=()
other_small=()
for c in $(conc_words "${CONC}"); do
	case "${c}" in
	64 | 256) judged_small+=("${c}") ;;
	*) other_small+=("${c}") ;;
	esac
done

# The report pairs the 4 MiB cells with their floor runs only at 1, 4 and 16
# in flight, so a cell elsewhere would get floor runs it shows as no floor.
for c in $(conc_words "${BENCH_LARGE_CONC}"); do
	case "${c}" in
	1 | 4 | 16) ;;
	*) die "BENCH_LARGE_CONC is ${BENCH_LARGE_CONC}: the 4 MiB cells run only at 1, 4 and 16, where the report pairs them with their floor" ;;
	esac
done

objecter() {
	ceph_cmd config get client.admin "$1" 2>/dev/null || echo unknown
}

# host_librados prints the package version of the librados the benchmark
# links, which can differ from the release of the cluster.
host_librados() {
	rpm -q --qf '%{VERSION}-%{RELEASE}' librados2 2>/dev/null ||
		dpkg-query -W -f '${Version}' librados2 2>/dev/null ||
		echo unknown
}

# linked_librados prints the path of the librados the benchmark binary links
# and the Ceph version of the package that owns it.
linked_librados() {
	local lib version pkg
	lib=$(ldd "${bin}" | awk '$1 == "librados.so.2" { print $3 }')
	[[ -n "${lib}" ]] || return 1
	lib=$(readlink -f "${lib}")
	if version=$(rpm -qf --qf '%{VERSION}' "${lib}" 2>/dev/null); then
		:
	elif pkg=$(dpkg -S "${lib}" 2>/dev/null); then
		version=$(dpkg-query -W -f '${Version}' "${pkg%%:*}") || return 1
		version=${version#*:}
		version=${version%%-*}
	else
		return 1
	fi
	echo "${lib} ${version}"
}

say "seam sweep of ${ROOKET_NAME} (${cluster_image}) into ${dir}"
say "building ${bin}"
(cd "${repo}" && go test -c -tags="${GO_TAGS},integration" -o "${bin}" ./test/bench/seam)

librados=$(linked_librados) || die "cannot tell which Ceph release the librados ${bin} links belongs to"
read -r librados_path librados_version <<<"${librados}"
[[ "${librados_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
	die "the librados ${bin} links, ${librados_path}, is version '${librados_version}', not X.Y.Z"
floor_image=${BENCH_FLOOR_IMAGE:-quay.io/ceph/ceph:v${librados_version}}
if ! "${engine}" image inspect "${floor_image}" >/dev/null 2>&1; then
	"${engine}" pull "${floor_image}" >>"${log}" 2>&1 ||
		die "no floor image ${floor_image} for librados ${librados_version}: the floor's rados must be the release the benchmark links"
fi
floor_version=$("${engine}" run --rm "${floor_image}" rados --version | awk '{ print $3 }') ||
	die "cannot run rados --version in ${floor_image}"
[[ "${floor_version}" == "${librados_version}" ]] ||
	die "the floor image ${floor_image} runs Ceph ${floor_version}, but the benchmark links librados ${librados_version}"
say "floor from ${floor_image} (Ceph ${floor_version}, as ${librados_path}); the cluster runs ${cluster_image}"

# shellcheck disable=SC2153 # ROOKET_VERSION is the caller's, when set
jq -n --arg release "${release}" --arg ceph_version "$(pinned_version)" --arg cluster_image "${cluster_image}" \
	--arg floor_image "${floor_image}" --arg floor_image_version "${floor_version}" \
	--arg librados_version "${librados_version}" --arg librados_path "${librados_path}" \
	--arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	--arg rooket_version "${ROOKET_VERSION:-$("${rooket}" version 2>/dev/null | head -1)}" \
	--argjson nproc "$(nproc)" --argjson mem_total_kib "$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo)" \
	--arg cpu "$(awk -F': *' '/^model name/ { print $2; exit }' /proc/cpuinfo)" \
	--arg kernel "$(uname -r)" --arg go_version "$(go version)" --arg host_librados "$(host_librados)" \
	--arg git_describe "$(git -C "${repo}" describe --always --dirty)" \
	--arg gomaxprocs "${GOMAXPROCS:-unset}" --arg bench_time "${BENCH_TIME}" \
	--argjson floor_seconds "${BENCH_FLOOR_SECONDS}" --arg conc "${CONC}" --arg large_conc "${BENCH_LARGE_CONC}" \
	--argjson write4m_objects "${BENCH_WRITE4M_OBJECTS}" --argjson over_objects "${BENCH_OVER_OBJECTS}" \
	--arg objecter_inflight_ops "$(objecter objecter_inflight_ops)" \
	--arg objecter_inflight_op_bytes "$(objecter objecter_inflight_op_bytes)" \
	--argjson pool_max_avail "$(pool_avail)" --arg load "$(cut -d' ' -f1-3 /proc/loadavg)" \
	'$ARGS.named' >"${dir}/env.json"

for shape in headread4k headwrite4k indexrtt; do
	for c in $(conc_words "${CONC}"); do
		cell_modes "${shape}" "${c}"
	done
done
for shape in read4k write4k; do
	for c in "${other_small[@]}"; do
		cell_modes "${shape}" "${c}"
		floor_run "${floor_image}" "${shape}" "${c}" ""
	done
done

for shape in read4k write4k; do
	for c in "${judged_small[@]}"; do
		judged_cell "${shape}" "${c}"
	done
done
for shape in read4m write4m; do
	for c in $(conc_words "${BENCH_LARGE_CONC}"); do
		judged_cell "${shape}" "${c}"
	done
done

for mode in callback pipe; do
	for limits in off derived; do
		extra=()
		[[ "${limits}" == derived ]] || extra=("${no_limit[@]}")
		wait_avail || die "not enough space for the over-budget cell in ${mode} mode"
		bench "${mode}" "${dir}/overbudget-${mode}.jsonl" -shapes=write4m -conc=64 -budget-bytes=0 "${extra[@]}" \
			-test.benchtime="${BENCH_OVER_OBJECTS}x"
	done
done

for mode in sync callback pipe; do
	bench "${mode}" "" -shapes=read4k -conc=256 -test.cpuprofile="${dir}/cpu-${mode}-read4k-256.pprof"
done

left=$(toolbox rados -p "${pool}" ls --all | wc -l)
say "objects left in ${pool}: ${left}"

(cd "${repo}" && go run -tags="${GO_TAGS}" ./hack/bench/report seam --dir "${dir}")
