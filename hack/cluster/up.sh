#!/usr/bin/env bash
# Starts the disposable single-node cluster for one release: mon, mgr, one memstore
# OSD and a radosgw on 127.0.0.1:7480. It drives plain `podman run` because
# `podman compose` needs a provider that is not installed everywhere. A failure
# after the preflight check removes what this run created.
#
# Usage: up.sh squid|tentacle
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly roles=(mon mgr osd rgw)
readonly squid_image=quay.io/ceph/ceph:v19.2.6

die() {
	echo "up.sh: $*" >&2
	exit 1
}

resolve_tentacle() {
	local tag
	if command -v skopeo >/dev/null; then
		tag=$(skopeo list-tags docker://quay.io/ceph/ceph | jq -r '.Tags[]' |
			grep -E '^v20\.2\.[0-9]+$' | sort -V | tail -n 1)
	else
		tag=$(podman search --list-tags --limit 100000 --format '{{.Tag}}' quay.io/ceph/ceph |
			grep -E '^v20\.2\.[0-9]+$' | sort -V | tail -n 1)
	fi
	[[ -n "${tag}" ]] || die "no v20.2.* tag found for quay.io/ceph/ceph"
	echo "quay.io/ceph/ceph:${tag}"
}

dump_logs() {
	for role in "${roles[@]}"; do
		echo "--- rgw-go-${role} (last 30 lines)" >&2
		podman logs --tail 30 "rgw-go-${role}" >&2 || true
	done
}

ceph_cmd() {
	podman exec rgw-go-mon ceph --connect-timeout 5 "$@"
}

cluster_ready() {
	local status
	status=$(ceph_cmd -s -f json 2>/dev/null) || return 1
	jq -e '(.health.status == "HEALTH_OK" or .health.status == "HEALTH_WARN")
		and .osdmap.num_up_osds >= 1' <<<"${status}" >/dev/null
}

rgw_ready() {
	local code
	code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:7480/) || return 1
	[[ "${code}" == 200 || "${code}" == 403 ]]
}

wait_for() {
	local desc=$1 attempts=$2
	shift 2
	for ((i = 0; i < attempts; i++)); do
		if "$@"; then
			return 0
		fi
		sleep 2
	done
	dump_logs
	die "${desc} did not become ready"
}

release=${1:-}
case "${release}" in
squid) image=${squid_image} ;;
tentacle) image=$(resolve_tentacle) ;;
*) die "usage: up.sh squid|tentacle" ;;
esac

existing=$(podman ps -a --filter label=rgw-go.release --format '{{.Names}}')
[[ -z "${existing}" ]] || die "a cluster is already running (${existing//$'\n'/ }); run make cluster-down first"

out=${here}/out/${release}

rollback() {
	local rc=$?
	((rc == 0)) && return
	# Every step must run and the original rc must survive a failed removal.
	set +e
	echo "up.sh: failed, removing the ${release} cluster" >&2
	podman ps -a -q --filter "label=rgw-go.release=${release}" | xargs -r podman rm --force --time 5 >/dev/null
	podman volume ls -q --filter "label=rgw-go.release=${release}" | xargs -r podman volume rm --force >/dev/null
	rm -rf "${out}"
	exit "${rc}"
}
trap rollback EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

rm -rf "${out}"
mkdir -p "${out}"
echo "${image}" >"${out}/image"

podman image exists "${image}" || podman pull "${image}"

etc_volume=rgw-go-${release}-etc
podman volume create --label "rgw-go.release=${release}" "${etc_volume}" >/dev/null
for role in "${roles[@]}"; do
	lib_volume=rgw-go-${release}-${role}-lib
	podman volume create --label "rgw-go.release=${release}" "${lib_volume}" >/dev/null
	etc_mode=rw
	[[ "${role}" == mgr || "${role}" == rgw ]] && etc_mode=ro
	podman run --detach \
		--name "rgw-go-${role}" \
		--hostname "rgw-go-${role}" \
		--label "rgw-go.release=${release}" \
		--network host \
		--privileged \
		--volume "${etc_volume}:/etc/ceph:${etc_mode}" \
		--volume "${lib_volume}:/var/lib/ceph" \
		--volume "${here}/entrypoint.sh:/rgw-go/entrypoint.sh:ro" \
		--entrypoint bash \
		"${image}" /rgw-go/entrypoint.sh "${role}" >/dev/null
	echo "started rgw-go-${role} (${image})"
done

echo "waiting for the OSD to come up..."
wait_for "the cluster" 150 cluster_ready

ceph_cmd osd pool create rgw-go-test >/dev/null
ceph_cmd osd pool application enable rgw-go-test rados >/dev/null

echo "waiting for the radosgw on 127.0.0.1:7480..."
wait_for "the radosgw" 150 rgw_ready

podman cp rgw-go-mon:/etc/ceph/ceph.conf "${out}/ceph.conf"
podman cp rgw-go-mon:/etc/ceph/ceph.client.admin.keyring "${out}/ceph.client.admin.keyring"
# Point host clients at the copied keyring rather than /etc/ceph.
printf '\n[client.admin]\nkeyring = %s\n' "${out}/ceph.client.admin.keyring" >>"${out}/ceph.conf"

ceph_cmd -s
echo "${release} is up: CEPH_CONF=${out}/ceph.conf, radosgw at http://127.0.0.1:7480"
