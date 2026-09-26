#!/usr/bin/env bash
# Removes every disposable cluster up.sh started, with its volumes and its
# hack/cluster/out/<release>/ directory. It only touches containers and volumes
# carrying the rgw-go.release label.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here

mapfile -t containers < <(podman ps -a --filter label=rgw-go.release --format '{{.Names}}')
if ((${#containers[@]} > 0)); then
	podman rm --force --time 5 "${containers[@]}" >/dev/null
	echo "removed containers: ${containers[*]}"
fi

mapfile -t volumes < <(podman volume ls --filter label=rgw-go.release --format '{{.Name}}')
if ((${#volumes[@]} > 0)); then
	podman volume rm --force "${volumes[@]}" >/dev/null
	echo "removed volumes: ${volumes[*]}"
fi

rm -rf "${here}/out/squid" "${here}/out/tentacle"
