#!/usr/bin/env bash
# Reads one release's populated corpus through rgw-go and through the
# cluster's radosgw and requires the same answers: it starts rgw-go with
# rgw-go-up.sh, runs the specs test/gate labels read against both gateways,
# and stops rgw-go again however they end. The arguments after the release
# go to rgw-go serve, as rgw-go-up.sh takes them.
#
# Usage: read-gate.sh squid|tentacle [rgw-go serve flags...]
#
#   GO_TAGS  the build tags (ceph_preview); CGO_* come from the environment
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=read-gate.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
shift
repo=$(cd "${here}/../.." && pwd)
readonly repo
readonly manifest=${out}/manifest.json
[[ -f "${manifest}" ]] || die "no ${manifest}: run make populate RELEASE=${release} first"

stop() {
	local status=$?
	"${here}/rgw-go-down.sh" "${release}" || ((status)) || status=1
	exit "${status}"
}
trap stop EXIT
"${here}/rgw-go-up.sh" "${release}" "$@"

[[ -s "${out}/rgw-go.metrics" ]] ||
	die "rgw-go reported no metrics address, which the one-round-trip spec reads; do not turn its listener off"
RGW_GO_TEST_RGW_ENDPOINT=$(rgw_endpoint "$(rgw_daemon)")
RGW_GO_TEST_RGW_GO_ENDPOINT=$(<"${out}/rgw-go.endpoint")
RGW_GO_TEST_RGW_GO_METRICS=http://$(<"${out}/rgw-go.metrics")/metrics
export RGW_GO_TEST_RGW_ENDPOINT RGW_GO_TEST_RGW_GO_ENDPOINT RGW_GO_TEST_RGW_GO_METRICS
export RGW_GO_TEST_CEPH_CONF=${out}/ceph.conf RGW_GO_TEST_MANIFEST=${manifest}
echo "radosgw at ${RGW_GO_TEST_RGW_ENDPOINT}, rgw-go at ${RGW_GO_TEST_RGW_GO_ENDPOINT}, its metrics at ${RGW_GO_TEST_RGW_GO_METRICS}"

cd "${repo}"
go test "-tags=${GO_TAGS:-ceph_preview},integration" -race -count=1 -v ./test/gate/... \
	-args -ginkgo.v -ginkgo.label-filter=read
