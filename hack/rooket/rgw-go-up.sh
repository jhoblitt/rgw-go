#!/usr/bin/env bash
# Builds rgw-go and runs it on the host against one release's populated
# cluster, beside the cluster's radosgw and on the same site, with radosgw's
# command-line options after rgw-go's own. It runs as its own cephx entity,
# client.rgw.rgw-go, with radosgw's caps under Rook, and both gateways' config
# entities get the parity options first. radosgw reads most of them only when
# it starts, so its deployment restarts when one changed after the running
# radosgw started. A running rgw-go is stopped first, so this also restarts
# one. The arguments after the release go to rgw-go serve, after its default
# --metrics-addr; the endpoint, pid, log and metrics address land in
# hack/rooket/out/<release>/.
#
# Usage: rgw-go-up.sh squid|tentacle [rgw-go serve flags...]
#
#   RGW_GO_PORT          the port rgw-go serves S3 on, on 127.0.0.1 only
#                        (beast endpoint=); 7481 for squid, 7482 for tentacle
#   RGW_GO_METRICS_PORT  the port of its default --metrics-addr on 127.0.0.1;
#                        9481 for squid, 9482 for tentacle
#   GO_TAGS              the build tags (ceph_preview); CGO_* come from the environment
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=rgw-go-up.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
shift
repo=$(cd "${here}/../.." && pwd)
readonly repo

# Each release has its own ports, so both clusters can have an rgw-go at once.
case "${release}" in
squid) default_port=7481 default_metrics_port=9481 ;;
tentacle) default_port=7482 default_metrics_port=9482 ;;
esac
readonly port=${RGW_GO_PORT:-${default_port}} metrics_port=${RGW_GO_METRICS_PORT:-${default_metrics_port}}
[[ "${port}" =~ ^[0-9]+$ && "${metrics_port}" =~ ^[0-9]+$ ]] ||
	die "RGW_GO_PORT and RGW_GO_METRICS_PORT are port numbers, not '${port}' and '${metrics_port}'"
readonly endpoint=http://127.0.0.1:${port}
readonly rgw_go_entity=client.rgw.rgw-go
readonly conf=${out}/ceph.conf keyring=${out}/ceph.client.rgw.rgw-go.keyring
readonly log=${out}/rgw-go.log pidfile=${out}/rgw-go.pid
[[ -f "${conf}" ]] || die "no ${conf}: run make cluster-up RELEASE=${release} first"

if [[ -f "${pidfile}" ]]; then
	"${here}/rgw-go-down.sh" "${release}"
fi
# Otherwise the readiness check below could be answered by whatever holds the
# port while rgw-go fails to bind it.
! curl -sS --max-time 5 -o /dev/null "${endpoint}/" 2>/dev/null ||
	die "something already answers at ${endpoint}; set RGW_GO_PORT to a free port"

(cd "${repo}" && go build "-tags=${GO_TAGS:-ceph_preview}" -o bin/rgw-go ./cmd/rgw-go)

use_site
radosgw_entity=$(rgw_entity "${rgw}")

# get-or-create returns the entity's key when it exists with these caps, and
# fails when it exists with others.
key=$(ceph_cmd auth get-or-create "${rgw_go_entity}" mon 'allow rw' osd 'allow rwx') ||
	die "could not get or create ${rgw_go_entity} with radosgw's caps under Rook, mon 'allow rw' osd 'allow rwx'"
# umask covers a new file only; chmod also narrows an existing one.
(umask 077 && printf '%s\n' "${key}" >"${keyring}")
chmod 600 "${keyring}"

# converge ENTITY sets every parity option ENTITY's config section does not
# already hold.
converge() {
	local who=$1 kv
	for kv in "${parity_options[@]}"; do
		[[ "$(ceph_cmd config get "${who}" "${kv%%=*}")" != "${kv#*=}" ]] || continue
		ceph_cmd config set "${who}" "${kv%%=*}" "${kv#*=}"
		[[ "$(ceph_cmd config get "${who}" "${kv%%=*}")" == "${kv#*=}" ]] ||
			die "the mons hold ${kv%%=*} for ${who} as other than ${kv#*=} after setting it"
	done
}

converge "${rgw_go_entity}"
# The chart values' rgwConfig has Rook write the same options for its radosgw
# at each reconcile, in place of its own rgw_run_sync_thread=true (rook
# pkg/operator/ceph/object/config.go), so this finds them set unless they were
# changed by hand.
converge "${radosgw_entity}"

# The mons' config log entries and the service map's start_stamp are both
# utime_t, which prints the daemon's local time with its %z offset
# (include/utime.h localtime), as 2026-10-08T21:58:20.402329+0000 in Ceph's
# images, which run in UTC, or as seconds, 0.000000, for a time within ten
# years of the epoch, which the mons' first changeset carries. Any other form
# or offset fails the check rather than passing it. A change the log no longer
# reaches back to counts as newer.
#
# A change counts when it names a parity option in a section the radosgw
# reads: a config key is its sections and masks, then the option, joined by
# "/", and a daemon reads global, its type, and each dotted prefix of its name
# (ConfigMap::parse_mask and generate_entity_map in mon/ConfigMap.cc). A
# masked key counts whatever its mask, which may match the radosgw.
# shellcheck disable=SC2016 # the $names are jq's
readonly stale_filter='
def epoch: if test("^[0-9]+\\.[0-9]+$") then tonumber
	else (capture("^(?<s>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?<f>\\.[0-9]+)?(?:Z|\\+0000)$")
		// error("unparsed timestamp \(.)"))
		| (.s + "Z" | fromdateiso8601) + ("0" + (.f // "") | tonumber)
	end;
def section: split("/") | .[:-1]
	| reduce .[] as $part ("global"; if $part == "global" then "global" elif ($part | contains(":")) then . else $part end);
def applies: section as $section
	| $section == "global" or $section == $entity or ($entity | startswith($section + "."));
($start | epoch) as $started
| ($options | split(" ")) as $parity
| [.[] | select((.timestamp | epoch) > $started)] as $after
| (length >= $window and ($after | length) == length)
	or any($after[].changes[]?; .name as $name
		| ($name | split("/") | .[-1]) as $option
		| any($parity[]; . == $option) and ($name | applies))'
readonly config_log_window=1000

# stale tells whether a parity option of the radosgw's entity changed after the
# radosgw in rgw, the service map entry use_site read, started. None of them
# is a startup-only option to the mons, so config set reaches the running
# radosgw at once and config show reports the new value (common/config.cc).
# But radosgw reads the gc, lc, sync and reshard settings only when it builds
# its store, at startup and at each realm reload (rgw_appmain.cc
# init_storage, rgw_realm_reloader.cc), and registers in the service map,
# under a new gid with a new start_stamp, only after that (DaemonServer.cc).
stale() {
	local started changes status=0
	started=$(jq -r '.start_stamp // empty' <<<"${rgw}")
	[[ -n "${started}" ]] || die "the radosgw's service map entry has no start_stamp: ${rgw}"
	changes=$(ceph_cmd config log "${config_log_window}" -f json) || die "ceph config log failed"
	jq -e --arg start "${started}" --arg entity "${radosgw_entity}" --arg options "${parity_options[*]%%=*}" \
		--argjson window "${config_log_window}" "${stale_filter}" <<<"${changes}" >/dev/null || status=$?
	# jq -e exits 1 for false and higher when the filter itself failed.
	((status <= 1)) || die "could not compare the config log with the radosgw's start_stamp ${started}"
	((status == 0))
}

started=$(jq -r '.start_stamp // empty' <<<"${rgw}")
if ! stale; then
	echo "the radosgw started at ${started}, after its parity options last changed; not restarting it"
else
	echo "a parity option of ${radosgw_entity} changed after the radosgw started at ${started}; restarting it"
	k -n rook-ceph rollout restart deploy -l app=rook-ceph-rgw
	k -n rook-ceph rollout status deploy -l app=rook-ceph-rgw --timeout=5m
	# The manager can list the old radosgw alone for up to a minute before the
	# new one registers.
	deadline=$((SECONDS + 180))
	until use_site && ! stale; do
		((SECONDS < deadline)) || die "the radosgw in the service map started before its parity options were set, 180s after its restart: ${rgw}"
		sleep 5
	done
	echo "the radosgw restarted at $(jq -r '.start_stamp' <<<"${rgw}")"
fi

rm -f "${out}/rgw-go.endpoint" "${out}/rgw-go.metrics"
RGW_GO_LOG_FORMAT=json nohup "${repo}/bin/rgw-go" serve "--metrics-addr=127.0.0.1:${metrics_port}" "$@" -- \
	--id rgw.rgw-go --keyring "${keyring}" -c "${conf}" \
	--rgw-realm="${realm}" --rgw-zonegroup="${zonegroup}" --rgw-zone="${zone}" \
	--rgw-frontends="beast endpoint=127.0.0.1:${port}" \
	>"${log}" 2>&1 &
pid=$!
echo "${pid}" >"${pidfile}"

# radosgw answers an anonymous GET on the root with 200, which is Rook's probe.
deadline=$((SECONDS + 30))
until curl -fs --max-time 2 -o /dev/null "${endpoint}/" && kill -0 "${pid}" 2>/dev/null; do
	failure=
	if ! kill -0 "${pid}" 2>/dev/null; then
		failure="rgw-go exited before it answered at ${endpoint}"
	elif ((SECONDS >= deadline)); then
		failure="rgw-go did not answer at ${endpoint} within 30s"
	fi
	if [[ -n "${failure}" ]]; then
		tail -n 20 "${log}" >&2 || true
		"${here}/rgw-go-down.sh" "${release}" || true
		die "${failure}; see ${log}"
	fi
	sleep 1
done

# The flags may have moved or turned off the metrics listener, so its address
# is the one rgw-go reports serving on.
metrics=
for _ in 1 2 3 4 5; do
	if line=$(jq -R -c 'fromjson? | select(.msg == "serving")' "${log}") && [[ -n "${line}" ]]; then
		metrics=$(jq -r '.metrics_addr // empty' <<<"${line}" | tail -n 1)
		break
	fi
	sleep 1
done
[[ -z "${metrics}" ]] || echo "${metrics}" >"${out}/rgw-go.metrics"
echo "${endpoint}" >"${out}/rgw-go.endpoint"
echo "rgw-go (pid ${pid}) serves ${ROOKET_NAME}'s ${zone} at ${endpoint}${metrics:+, metrics at ${metrics}}; log ${log}"
