#!/usr/bin/env bash
# Stops the rgw-go rgw-go-up.sh started against one release's cluster: TERM,
# which drains its requests as radosgw's handler does, then up to 30 s for it
# to exit. It removes the pid, endpoint and metrics files and keeps the log,
# the keyring, the cephx entity and the config options, which the next
# rgw-go-up.sh reuses.
#
# Usage: rgw-go-down.sh squid|tentacle
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=rgw-go-down.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
readonly pidfile=${out}/rgw-go.pid log=${out}/rgw-go.log

forget() {
	rm -f "${pidfile}" "${out}/rgw-go.endpoint" "${out}/rgw-go.metrics"
}

if [[ ! -f "${pidfile}" ]]; then
	forget
	echo "rgw-go is not running against ${ROOKET_NAME}"
	exit 0
fi
pid=$(<"${pidfile}")
[[ "${pid}" =~ ^[0-9]+$ ]] || die "${pidfile} holds '${pid}', not a pid"
# A pid outlives its process and can be reused, so only an rgw-go is
# signalled.
if [[ "$(ps -o comm= -p "${pid}" 2>/dev/null)" != rgw-go ]]; then
	forget
	echo "rgw-go (pid ${pid}) had already stopped; see ${log}"
	exit 0
fi

kill -TERM "${pid}"
deadline=$((SECONDS + 30))
while kill -0 "${pid}" 2>/dev/null; do
	if ((SECONDS >= deadline)); then
		kill -KILL "${pid}" 2>/dev/null || true
		for _ in 1 2 3 4 5; do
			kill -0 "${pid}" 2>/dev/null || break
			sleep 1
		done
		! kill -0 "${pid}" 2>/dev/null ||
			die "rgw-go (pid ${pid}) outlived TERM by 30s and KILL by 5s; ${pidfile} still names it"
		forget
		die "rgw-go (pid ${pid}) did not stop within 30s of TERM and was killed; see ${log}"
	fi
	sleep 1
done
forget
# main prints the error that ended a run on its last line.
last=$(tail -n 1 "${log}" 2>/dev/null || true)
[[ "${last}" != "rgw-go: "* ]] || die "rgw-go (pid ${pid}) stopped with an error: ${last}"
echo "rgw-go (pid ${pid}) stopped"
