#!/usr/bin/env bash
# Stands in for rooket inside a release's builder, where in-builder.sh mounts
# it on PATH as rooket. rooket itself cannot run there: every one of its
# commands first probes for rootful podman or a running docker, and a builder
# has neither. Only rooket kubectl runs in a builder, and this does what it
# does (rooket v0.1.0, cmd/kubectl.go): runs kubectl with KUBECONFIG at the
# kubeconfig of the cluster ROOKET_NAME names, in rooket's state directory
# under HOME, which in-builder.sh mounts.
#
# Usage: rooket kubectl|k [kubectl arguments...]
set -euo pipefail

me="rooket (hack/rooket/builder-rooket.sh)"
case "${1:-}" in
kubectl | k) shift ;;
*)
	echo "${me}: only rooket kubectl runs in a builder, not rooket ${1:-}" >&2
	exit 2
	;;
esac
[[ -n "${ROOKET_NAME:-}" ]] || {
	echo "${me}: no cluster selected: set ROOKET_NAME" >&2
	exit 1
}
kubeconfig=${HOME}/.local/share/rooket/${ROOKET_NAME}/kubeconfig
[[ -f "${kubeconfig}" ]] || {
	echo "${me}: no kubeconfig for cluster \"${ROOKET_NAME}\" at ${kubeconfig} (is it up?)" >&2
	exit 1
}
KUBECONFIG=${kubeconfig} exec kubectl "$@"
