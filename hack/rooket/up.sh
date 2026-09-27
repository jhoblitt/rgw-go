#!/usr/bin/env bash
# Starts the disposable Rook cluster for one release on kind, through rooket:
# one worker with one OSD, host networking, the chart's ceph-objectstore, and
# the Ceph image hack/rooket/<release>/ pins. Once rooket reports it ready for
# clients, it checks that every Ceph daemon and the toolbox run that image,
# creates the scratch pool the integration specs use, and writes the host's
# client config to hack/rooket/out/<release>/.
#
# Usage: up.sh squid|tentacle
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=up.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"

want_tag=$(pinned_tag)
want_version=$(pinned_version)

"${rooket}" up --rook-version "${rook_version}" --workers 1 --config-dir "${here}/${release}" \
	--wait --wait-timeout "${RGW_GO_WAIT_TIMEOUT:-30m}"

image=$(k -n rook-ceph get cephcluster -o jsonpath='{.items[0].spec.cephVersion.image}')
[[ "${image}" == *":${want_tag}" ]] || die "the CephCluster runs ${image}, not the pinned tag ${want_tag}"

# Every Ceph daemon and the toolbox run the pinned image, so the toolbox's
# radosgw-admin and ceph-dencoder are the release the daemons run.
images=$(k -n rook-ceph get pods \
	-l 'app in (rook-ceph-mon,rook-ceph-mgr,rook-ceph-osd,rook-ceph-rgw,rook-ceph-tools)' -o json |
	jq -c '[.items[] | {app: .metadata.labels.app, image: .spec.containers[].image}] | unique')
for app in rook-ceph-mon rook-ceph-mgr rook-ceph-osd rook-ceph-rgw rook-ceph-tools; do
	jq -e --arg app "${app}" 'any(.[]; .app == $app)' <<<"${images}" >/dev/null || die "no ${app} pod"
done
jq -e --arg image "${image}" 'all(.[]; .image == $image)' <<<"${images}" >/dev/null ||
	die "not every Ceph pod runs ${image}: ${images}"

# A build is its version, commit and release; what follows them in a version
# string differs between binaries of one build. The radosgw must be among the
# daemons, and the one build must be the pinned version of the release.
versions=$(ceph_cmd versions -f json)
jq -e --arg release "${release}" --arg version "${want_version}" '
	(.rgw // {} | length) > 0 and
	([.overall | keys[] | capture("^ceph version (?<version>\\S+) \\((?<commit>[0-9a-f]+)\\) (?<release>\\S+) \\(")]
	| unique | length == 1 and .[0].release == $release and .[0].version == $version)' \
	<<<"${versions}" >/dev/null || die "the daemons, radosgw included, do not all run ${release} ${want_version}: ${versions}"

if ! ceph_cmd osd pool ls | grep -qx rgw-go-test; then
	ceph_cmd osd pool create rgw-go-test >/dev/null
fi
ceph_cmd osd pool application enable rgw-go-test rados >/dev/null

rm -rf "${out}"
mkdir -p "${out}"
echo "${image}" >"${out}/image"
"${rooket}" ceph-config --out "${out}"

# populate.sh, like any S3 client of the specs, reaches the radosgw from the
# host.
endpoint=$(rgw_endpoint "$(rgw_daemon)")
curl -fsS --max-time 10 -o /dev/null "${endpoint}/" || die "the host cannot reach the radosgw at ${endpoint}"
echo "${ROOKET_NAME} (${image}) is up: CEPH_CONF=${out}/ceph.conf, radosgw at ${endpoint}"
