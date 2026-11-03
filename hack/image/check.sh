#!/usr/bin/env bash
# Runs make check, or the make targets given, in Tentacle's check image
# (run.sh under RGW_GO_STAGE=check), so that every gate compiles, lints and
# tests against the librados, tcmalloc, Go and golangci-lint CI uses, on any
# host. The ceph clone make citecheck-files-check reads, CITECHECK_CEPH or else
# citecheck's own default, is mounted read-only at its path when it exists,
# with its git directory when a worktree's lies outside it.
#
# Usage: check.sh [make targets...]
set -euo pipefail

image_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly image_dir

ceph=${CITECHECK_CEPH:-/home/jhoblitt/github/ceph}
mounts=()
if [[ -d "${ceph}" ]]; then
	ceph=$(cd "${ceph}" && pwd -P)
	mounts+=(-v "${ceph}:${ceph}:ro")
	common=$(git -C "${ceph}" rev-parse --path-format=absolute --git-common-dir 2>/dev/null) || common=
	if [[ -n "${common}" && "${common}/" != "${ceph}/"* ]]; then
		mounts+=(-v "${common}:${common}:ro")
	fi
	export CITECHECK_CEPH=${ceph}
fi
RGW_GO_STAGE=check exec "${image_dir}/run.sh" "${mounts[@]}" -- tentacle make "${@:-check}"
