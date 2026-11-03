#!/usr/bin/env bash
# Runs a command in one release's builder (hack/image/run.sh) beside that
# release's rooket cluster, as the Makefile's CGO_RUN does for every target
# whose go test links librados. The container is on the host's network, which
# reaches the cluster's daemons at its node's address and its API server on
# loopback. rooket itself cannot run in a builder, which has no container
# engine for it to probe, so builder-rooket.sh, which does what rooket kubectl
# does, stands in for it: on PATH as rooket, and at ROOKET's path when that
# names one, as the Makefile passes it on as ROOKET and RGW_GO_TEST_ROOKET.
# The host's kubectl is mounted on PATH, and the cluster's rooket state, which
# holds its kubeconfig, under the container's HOME, both read-only.
#
# Usage: in-builder.sh squid|tentacle command [arguments...]
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=in-builder.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
shift
(($#)) || die "usage: ${script} squid|tentacle command [arguments...]"

kubectl_bin=$(command -v kubectl) || die "no kubectl on PATH"
state=${HOME}/.local/share/rooket/${ROOKET_NAME}
[[ -f "${state}/kubeconfig" ]] || die "no kubeconfig for ${ROOKET_NAME} in ${state}: run make cluster-up RELEASE=${release} first"
mounts=(-v "${here}/builder-rooket.sh:/usr/local/bin/rooket:ro" -v "${kubectl_bin}:/usr/local/bin/kubectl:ro"
	-v "${state}:/tmp/.local/share/rooket/${ROOKET_NAME}:ro")
if [[ "${rooket}" == /* && "${rooket}" != /usr/local/bin/rooket ]]; then
	mounts+=(-v "${here}/builder-rooket.sh:${rooket}:ro")
fi
exec "${here}/../image/run.sh" --network=host "${mounts[@]}" -e ROOKET_NAME -- "${release}" "$@"
