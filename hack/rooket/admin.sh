#!/usr/bin/env bash
# Runs radosgw-admin in one release's Rook toolbox on the site the populated
# manifest records, and prints its stdout. It touches no other cluster.
#
# Usage: admin.sh squid|tentacle <radosgw-admin arguments...>
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=admin.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
shift
use_manifest_site
admin "$@"
