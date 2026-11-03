#!/usr/bin/env bash
# Prints the reference of one release's builder (Containerfile.builder), the
# Ceph image the release's chart values pin (hack/rooket/<release>/values/)
# with librados's headers, git and the Go toolchain, after building it when the
# engine lacks it. RGW_GO_STAGE=check names the check image instead
# (Containerfile.check): the builder with golangci-lint at the Makefile's pin,
# for make lint and make check. --print names the image without building
# anything, which CI keys its cache of the builder on. RGW_GO_ENGINE picks the
# container engine, podman when installed, else docker. Only the reference goes
# to stdout.
#
# Usage: builder.sh [--print] squid|tentacle
set -euo pipefail

image_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly image_dir
repo=$(cd "${image_dir}/../.." && pwd -P)
readonly repo
# lib.sh finds a release's chart values under `here`.
readonly here=${repo}/hack/rooket
readonly script=builder.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

print=false
if [[ "${1:-}" == --print ]]; then
	print=true
	shift
fi
(($# == 1)) || die "usage: ${script} [--print] squid|tentacle"
use_release "$1"
tag=$(pinned_tag)
base=$(pinned_image)

# The builder's tag carries a hash of its base image's name and its
# Containerfile, so a change to either, a new repository for the same Ceph tag
# or a Go bump, builds a new builder instead of reusing the old one. The check
# image's carries a hash of the builder, golangci-lint's version and its own
# Containerfile.
hash=$({ echo "${base}" && cat "${image_dir}/Containerfile.builder"; } | sha256sum | cut -c1-12)
builder=localhost/rgw-go-builder:${tag}-${hash}
image=${builder}
case "${RGW_GO_STAGE:-builder}" in
builder) ;;
check)
	lint=$(make -s --no-print-directory -C "${repo}" print-golangci-version)
	hash=$({ echo "${builder} ${lint}" && cat "${image_dir}/Containerfile.check"; } | sha256sum | cut -c1-12)
	image=localhost/rgw-go-check:${builder##*:}-${hash}
	;;
*) die "RGW_GO_STAGE is builder or check, not '${RGW_GO_STAGE}'" ;;
esac
if [[ "${print}" == true ]]; then
	echo "${image}"
	exit 0
fi
engine=${RGW_GO_ENGINE:-}
[[ -n "${engine}" ]] || engine=$(command -v podman || command -v docker) || die "no podman or docker on PATH"

# build FILE IMAGE ARG... builds IMAGE from FILE with the engine build
# arguments ARG unless the engine has it. Neither Containerfile copies anything
# from its build context.
build() {
	local file=$1 ref=$2 context status=0
	shift 2
	if "${engine}" image inspect "${ref}" >/dev/null 2>&1; then
		return 0
	fi
	echo "${script}: building ${ref}" >&2
	context=$(mktemp -d)
	"${engine}" build --platform linux/amd64 -f "${image_dir}/${file}" "$@" -t "${ref}" "${context}" >&2 || status=$?
	rmdir "${context}"
	return "${status}"
}
build Containerfile.builder "${builder}" --build-arg "CEPH_IMAGE=${base}"
if [[ "${image}" != "${builder}" ]]; then
	build Containerfile.check "${image}" --build-arg "BUILDER=${builder}" --build-arg "GOLANGCI_LINT_VERSION=${lint}"
fi
echo "${image}"
