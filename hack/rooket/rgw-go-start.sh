#!/usr/bin/env bash
# Starts the rgw-go rgw-go-up.sh runs against one release's cluster, with the
# rgw-go arguments given and its output, a JSON log, in LOG, and prints its
# pid. Without RGW_GO_IMAGE it builds bin/rgw-go on the host with cgo, taking
# CGO_* from the environment and the tags from GO_TAGS (ceph_preview), and
# starts it under nohup. With RGW_GO_IMAGE, a derived image (make image), it
# starts that image's /usr/bin/rgw-go instead, so rgw-go runs with the
# librados and tcmalloc of the Ceph image it ships in. The container is
# detached, removed when rgw-go exits, and on the host's network, where rgw-go
# binds what the host binary would and reaches the cluster as the host does. It
# runs as the caller, with the checkout mounted at its own path for LOG, the
# keyring and the conf, and with the caller's RGW_GO_* settings and GODEBUG.
# The pid is the host's pid of that rgw-go, which runs as the caller, so
# rgw-go-down.sh stops it as it stops a host binary. RGW_GO_ENGINE picks the
# container engine, podman when installed, else docker.
#
# Usage: rgw-go-start.sh squid|tentacle LOG rgw-go-arguments...
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=rgw-go-start.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
log=${2:-}
if [[ -z "${log}" ]] || (($# < 3)); then
	die "usage: ${script} squid|tentacle LOG rgw-go-arguments..."
fi
shift 2
# rgw-go-up.sh names LOG, the keyring and the conf under this same path.
repo=$(cd "${here}/../.." && pwd)
readonly repo
export RGW_GO_LOG_FORMAT=json

if [[ -z "${RGW_GO_IMAGE:-}" ]]; then
	(cd "${repo}" && go build "-tags=${GO_TAGS:-ceph_preview}" -o bin/rgw-go ./cmd/rgw-go)
	nohup "${repo}/bin/rgw-go" "$@" >"${log}" 2>&1 &
	echo "$!"
	exit 0
fi

engine=${RGW_GO_ENGINE:-}
[[ -n "${engine}" ]] || engine=$(command -v podman || command -v docker) || die "no podman or docker on PATH"
# The caller's uid, as hack/image/run.sh maps it: the keyring is the caller's
# alone, and rgw-go-down.sh signals the pid.
if [[ $("${engine}" info --format '{{.Host.Security.Rootless}}' 2>/dev/null) == true ]]; then
	user=(--userns=keep-id)
else
	user=(--user "$(id -u):$(id -g)")
fi
env=()
while IFS= read -r name; do
	env+=(-e "${name}")
done < <(compgen -e | grep -E '^(RGW_GO_[A-Z0-9_]+|GODEBUG)$')
# shellcheck disable=SC2016 # the container's shell expands its "$@" and "$0"
container=$("${engine}" run -d --rm --pull=never "${user[@]}" --security-opt label=disable --network=host \
	-v "${repo}:${repo}" "${env[@]}" --entrypoint /bin/sh "${RGW_GO_IMAGE}" \
	-c 'exec /usr/bin/rgw-go "$@" >"$0" 2>&1' "${log}" "$@")
# A container that has already exited reports pid 0, which kill would take
# for the caller's own process group.
pid=$("${engine}" inspect --format '{{.State.Pid}}' "${container}") || pid=
[[ "${pid}" =~ ^[1-9][0-9]*$ ]] || die "rgw-go's container ${container} ended before it started; see ${log}"
echo "${pid}"
