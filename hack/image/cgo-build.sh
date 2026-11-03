#!/usr/bin/env bash
# Runs `go "$@"` inside the rgw-go builder (Containerfile.builder) for the Ceph
# image a release's chart values (hack/rooket/<release>/values/) pin at the tag
# RGW_GO_CEPH_TAG, so a binary links against the librados and glibc of the
# image it will run in. run.sh runs it, with the checkout mounted at its own
# path and go in the caller's directory, so relative paths such as -o's resolve
# as they do on the host; GOOS, GOARCH, GOAMD64, CGO_ENABLED and GOFLAGS pass
# through from the caller's environment, which is where goreleaser puts a
# build's target and env when it runs the build's tool. RGW_GO_ENGINE picks the
# container engine, podman when installed, else docker.
#
# Usage: RGW_GO_CEPH_TAG=vX.Y.Z cgo-build.sh <go arguments>
set -euo pipefail

image_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly image_dir
# lib.sh finds a release's chart values under `here`.
here=$(cd "${image_dir}/../rooket" && pwd -P)
readonly here
readonly script=cgo-build.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

tag=${RGW_GO_CEPH_TAG:-}
[[ "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "RGW_GO_CEPH_TAG '${tag}' is not a Ceph image tag vX.Y.Z"
for r in squid tentacle; do
	use_release "${r}"
	if [[ "$(pinned_tag)" == "${tag}" ]]; then
		exec "${image_dir}/run.sh" -- "${r}" go "$@"
	fi
done
die "no release's chart values pin RGW_GO_CEPH_TAG ${tag}"
