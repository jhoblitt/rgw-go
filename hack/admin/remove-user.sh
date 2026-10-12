#!/usr/bin/env bash
# Removes a user an admin harness created on one release's populated
# cluster, with its buckets and objects, then collects the garbage the
# removal leaves, so that make gate passes afterwards. A user that does not
# exist is left alone; the collection runs either way.
#
# Usage: remove-user.sh squid|tentacle UID
set -euo pipefail

harness=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly harness
here=$(cd "${harness}/../rooket" && pwd)
readonly here
readonly script=remove-user.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

use_release "${1:-}"
uid=${2:-}
[[ -n "${uid}" ]] || die "usage: ${script} squid|tentacle UID"
use_manifest_site

if admin user info --uid "${uid}" >/dev/null 2>&1; then
	admin user rm --uid "${uid}" --purge-data
fi
admin gc process --include-all
