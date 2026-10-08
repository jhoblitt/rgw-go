#!/usr/bin/env bash
# Prints the URL the host reaches one release's radosgw at, once Ceph's
# service map has settled to one radosgw. It touches no other cluster.
#
# Usage: endpoint.sh squid|tentacle
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=endpoint.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
rgw=$(rgw_daemon)
rgw_endpoint "${rgw}"
