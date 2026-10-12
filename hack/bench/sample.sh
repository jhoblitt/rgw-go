#!/usr/bin/env bash
# Samples a gateway process once a second until TERM, appending one CSV line
# per sample to OUT: the host's time in seconds since the epoch, the
# process's user and system CPU in seconds (/proc/<pid>/stat fields 14 and
# 15, in clock ticks of getconf CLK_TCK), its thread count (Threads) and its
# resident memory in KiB (VmRSS, of /proc/<pid>/status). A new OUT gets the
# header time,utime,stime,threads,rss_kib.
#
# TARGET is pid:<pid>, a process on the host, or pod:<name>, the radosgw of a
# Rook pod in rook-ceph, read through `rooket kubectl exec` in its rgw
# container with ROOKET_NAME naming the cluster. Rook gives the radosgw pod a
# shared process namespace when its log collector runs (rook
# pkg/operator/ceph/object/spec.go), so the radosgw is not PID 1 there: the
# sampler first finds the one process named radosgw, and every sample reads
# that pid. A sample that fails is tried once more and then skipped; each skip
# and, once TERM ends the sampler, the count taken and skipped go to stderr.
#
# Usage: sample.sh pid:<pid>|pod:<name> OUT
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/../rooket" && pwd)
readonly here
readonly script=sample.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

target=${1:-}
csv=${2:-}
[[ "${target}" =~ ^(pid:[0-9]+|pod:[a-z0-9.-]+)$ && -n "${csv}" ]] || die "usage: ${script} pid:<pid>|pod:<name> OUT"
kind=${target%%:*}
name=${target#*:}
[[ "${kind}" == pid || -n "${ROOKET_NAME:-}" ]] || die "ROOKET_NAME must name the cluster of pod ${name}"
tick=$(getconf CLK_TCK)
readonly kind name tick

# read_proc prints the process's stat line, then its status.
pid=
if [[ "${kind}" == pid ]]; then
	pid=${name}
	read_proc() { cat "/proc/${pid}/stat" "/proc/${pid}/status"; }
else
	read_proc() { k -n rook-ceph exec "${name}" -c rgw -- cat "/proc/${pid}/stat" "/proc/${pid}/status"; }
	# shellcheck disable=SC2016 # expanded by the container's shell
	pids=$(k -n rook-ceph exec "${name}" -c rgw -- sh -c 'for c in /proc/[0-9]*/comm; do
		[ "$(cat "$c" 2>/dev/null)" = radosgw ] && basename "$(dirname "$c")"; done; true') ||
		die "cannot list the processes of pod ${name}"
	[[ "${pids}" =~ ^[0-9]+$ ]] || die "pod ${name} runs no single process named radosgw: pids '${pids//$'\n'/ }'"
	pid=${pids}
fi

# sample prints the CSV line of one sample. Fields 14 and 15 of stat count
# from the pid, so they are the 12th and 13th past the last ")", which closes
# the command name, itself free to hold spaces and parentheses.
sample() {
	local t0 t1 proc
	t0=${EPOCHREALTIME}
	proc=$(read_proc) || return 1
	t1=${EPOCHREALTIME}
	# The read happens somewhere within kubectl exec's round trip, so a
	# sample's time is the middle of it.
	awk -v now="$(awk -v a="${t0}" -v b="${t1}" 'BEGIN { printf "%.6f", (a + b) / 2 }')" -v tick="${tick}" '
		NR == 1 {
			match($0, /.*\)/)
			rest = substr($0, RLENGTH + 2)
			split(rest, f, " ")
			utime = f[12] / tick
			stime = f[13] / tick
			next
		}
		$1 == "Threads:" { threads = $2 }
		$1 == "VmRSS:" { rss = $2 }
		END {
			if (utime == "" || threads == "" || rss == "") exit 1
			printf "%.3f,%.2f,%.2f,%d,%d\n", now, utime, stime, threads, rss
		}' <<<"${proc}"
}

stopping=0
taken=0
skipped=0
trap 'stopping=1' TERM
[[ -s "${csv}" ]] || echo "time,utime,stime,threads,rss_kib" >"${csv}"
next=${EPOCHREALTIME}
while ((stopping == 0)); do
	# A sample that fails, as a kubectl exec does when the apiserver hiccups,
	# is tried once more and then skipped: the report interpolates the CPU
	# across the gap, and one lost second must not end the run.
	if line=$(sample) || line=$(sample); then
		echo "${line}" >>"${csv}"
		taken=$((taken + 1))
	else
		skipped=$((skipped + 1))
		echo "${script}: skipped a sample of ${target}'s process ${pid}, ${skipped} so far" >&2
	fi
	# One sample a second, however long a sample takes: kubectl exec takes a
	# good part of one.
	next=$(awk -v n="${next}" 'BEGIN { printf "%.6f", n + 1 }')
	wait_s=$(awk -v n="${next}" -v now="${EPOCHREALTIME}" 'BEGIN { d = n - now; printf "%.3f", (d > 0 ? d : 0) }')
	# In the background, so that TERM ends the wait at once.
	sleep "${wait_s}" &
	sleeper=$!
	wait "${sleeper}" || kill "${sleeper}" 2>/dev/null || true
done
echo "${script}: ${target}: ${taken} samples taken, ${skipped} skipped" >&2
