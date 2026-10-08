#!/usr/bin/env bash
# Stops the rgw-go rgw-go-up.sh started against one release's cluster, then
# removes that cluster through rooket: the kind cluster, its OSD disk
# images and iSCSI targets (which, as their setup did, needs root), rooket's
# state for it, and hack/rooket/out/<release>/. It touches no other cluster.
#
# Usage: down.sh squid|tentacle
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=down.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"

# An rgw-go left running would outlive the cluster and the pid file that
# names it. Having none running is fine, and a failed stop does not keep the
# cluster.
"${here}/rgw-go-down.sh" "${release}" || echo "${script}: rgw-go-down.sh failed; removing the cluster anyway" >&2

# out/ goes even when rooket fails, so a client config never outlives its
# cluster; the script still exits with rooket's status.
trap 'rm -rf "${out}"' EXIT
"${rooket}" down --delete-disks
