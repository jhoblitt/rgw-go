#!/usr/bin/env bash
# Builds the derived Ceph image for one release: the Ceph image the release's
# chart values pin (hack/rooket/<release>/values/), with rgw-go, built by
# cgo-build.sh against that image's librados, as /usr/bin/radosgw and Ceph's
# own radosgw kept as /usr/bin/radosgw.ceph. It prints the image reference,
# and nothing else, on stdout. The default reference is never pushed, so a
# kind node with the image loaded never tries to pull it. RGW_GO_IMAGE_TEST=1
# checks the image after building it; RGW_GO_ENGINE picks the container
# engine, podman when installed, else docker.
#
# Usage: build.sh squid|tentacle [image tag]
set -euo pipefail

image_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly image_dir
repo=$(cd "${image_dir}/../.." && pwd -P)
readonly repo
# lib.sh finds a release's chart values under `here`.
readonly here=${repo}/hack/rooket
readonly script=build.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
(($# <= 2)) || die "usage: ${script} squid|tentacle [image tag]"
use_release "${1:-}"

tag=$(pinned_tag)
version=$(pinned_version)
base=$(pinned_image)
image_tag=${2:-local-${tag}}
[[ "${image_tag}" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || die "${image_tag} is not an image tag"
image=ghcr.io/jhoblitt/rgw-go:${image_tag}
engine=${RGW_GO_ENGINE:-}
[[ -n "${engine}" ]] || engine=$(command -v podman || command -v docker) || die "no podman or docker on PATH"
export RGW_GO_ENGINE=${engine}

in_image() {
	"${engine}" run --rm --pull=never "${image}" "$@"
}

# check_image checks the image: rgw-go carries the checkout's version stamp,
# radosgw resolves to it, every library it links is in the image, the Ceph
# tools sharing the image are the base's, and the image names its base.
check_image() {
	local head stamp target libs librados want got line lines=0 base_name
	head=$(git -C "${repo}" rev-parse HEAD)
	stamp=$(in_image rgw-go version)
	[[ "${stamp}" == v* && "${stamp}" == *" (${head:0:12}"* ]] ||
		die "rgw-go version prints '${stamp}', not a module version stamped with ${head:0:12}"

	target=$(in_image readlink -f /usr/bin/radosgw)
	[[ "${target}" == /usr/bin/rgw-go ]] || die "/usr/bin/radosgw resolves to ${target}, not /usr/bin/rgw-go"

	libs=$(in_image ldd /usr/bin/rgw-go) || die "ldd cannot load /usr/bin/rgw-go"
	if grep -q 'not found' <<<"${libs}"; then
		die "rgw-go links libraries the image lacks: ${libs}"
	fi
	librados=$(grep -o 'librados\.so[^ ]* => [^ ]*' <<<"${libs}") || librados="no librados"
	echo "${script}: rgw-go links ${librados}" >&2

	# Under Rook every daemon and the toolbox run this image, so its Ceph tools
	# must be the base image's.
	want=$("${engine}" run --rm "${base}" sh -c 'radosgw --version && ceph --version && radosgw-admin --version && rados --version')
	got=$(in_image sh -c 'radosgw.ceph --version && ceph --version && radosgw-admin --version && rados --version')
	[[ "${got}" == "${want}" ]] || die "the Ceph tools print '${got}', the base's print '${want}'"
	while IFS= read -r line; do
		[[ "${line}" == "ceph version ${version} ("* ]] || die "a Ceph tool prints '${line}', not ceph version ${version}"
		lines=$((lines + 1))
	done <<<"${got}"
	((lines == 4)) || die "want a version line from each of the four Ceph tools, have '${got}'"

	base_name=$("${engine}" image inspect --format '{{index .Config.Labels "org.opencontainers.image.base.name"}}' "${image}")
	[[ "${base_name}" == "${base}" ]] || die "the image names its base '${base_name}', not ${base}"
	echo "${script}: ${image} passed its checks" >&2
}

cd "${repo}"
# The directory is the image's whole build context, so it holds the binary alone.
bin=hack/image/out/${release}
rm -rf "${bin}"
mkdir -p "${bin}"
echo "${script}: building rgw-go against ${base}" >&2
tags=$(make -s --no-print-directory -C "${repo}" print-go-tags)
RGW_GO_CEPH_TAG=${tag} "${image_dir}/cgo-build.sh" build -trimpath -tags "${tags}" -ldflags '-s -w' \
	-o "${bin}/rgw-go" ./cmd/rgw-go >&2
echo "${script}: building ${image}" >&2
"${engine}" build --platform linux/amd64 -f "${image_dir}/Containerfile" --build-arg "CEPH_IMAGE=${base}" \
	-t "${image}" "${bin}" >&2
if [[ "${RGW_GO_IMAGE_TEST:-}" == 1 ]]; then
	check_image
fi
echo "${image}"
