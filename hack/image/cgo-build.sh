#!/usr/bin/env bash
# Runs `go "$@"` inside the rgw-go builder (Containerfile.builder) for the Ceph
# image a release's chart values (hack/rooket/<release>/values/) pin at the tag
# RGW_GO_CEPH_TAG, so a binary links against the librados and glibc of the
# image it will run in. The checkout is mounted at its own path
# and go runs in the caller's directory, so relative paths such as -o's
# resolve as they do on the host. GOOS, GOARCH, GOAMD64, CGO_ENABLED and
# GOFLAGS pass through from the caller's environment, which is where
# goreleaser puts a build's target and env when it runs the build's tool.
# RGW_GO_ENGINE picks the container engine, podman when installed, else
# docker.
#
# Usage: RGW_GO_CEPH_TAG=vX.Y.Z cgo-build.sh <go arguments>
set -euo pipefail

image_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly image_dir
repo=$(cd "${image_dir}/../.." && pwd -P)
readonly repo
# lib.sh finds a release's chart values under `here`.
readonly here=${repo}/hack/rooket
readonly script=cgo-build.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

tag=${RGW_GO_CEPH_TAG:-}
[[ "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "RGW_GO_CEPH_TAG '${tag}' is not a Ceph image tag vX.Y.Z"
base=
for r in squid tentacle; do
	use_release "${r}"
	pinned=$(pinned_tag)
	if [[ "${pinned}" == "${tag}" ]]; then
		base=$(pinned_image)
		break
	fi
done
[[ -n "${base}" ]] || die "no release's chart values pin RGW_GO_CEPH_TAG ${tag}"
cwd=$(pwd -P)
[[ "${cwd}/" == "${repo}/"* ]] || die "run it from inside the checkout, ${repo}"
engine=${RGW_GO_ENGINE:-}
[[ -n "${engine}" ]] || engine=$(command -v podman || command -v docker) || die "no podman or docker on PATH"

# The builder's tag carries a hash of its base image's name and its
# Containerfile, so a change to either, a new repository for the same Ceph tag
# or a Go bump, builds a new builder instead of reusing the old one.
hash=$({ echo "${base}" && cat "${image_dir}/Containerfile.builder"; } | sha256sum | cut -c1-12)
builder=localhost/rgw-go-builder:${tag}-${hash}
if ! "${engine}" image inspect "${builder}" >/dev/null 2>&1; then
	echo "${script}: building ${builder}" >&2
	# The builder copies nothing from its build context.
	context=$(mktemp -d)
	trap 'rmdir "${context}"' EXIT
	"${engine}" build --platform linux/amd64 -f "${image_dir}/Containerfile.builder" \
		--build-arg "CEPH_IMAGE=${base}" -t "${builder}" "${context}" >&2
	rmdir "${context}"
	trap - EXIT
fi

# go's build cache does not notice a change in the C headers cgo compiles
# against, so each builder, and so each librados, has a cache of its own.
gocache=${repo}/hack/image/out/gocache/${builder##*:}
# Outside the checkout, go env reads no go.mod, so a host Go older than its go
# line does not switch to, and under GOTOOLCHAIN=auto download, a newer
# toolchain just to print a path.
modcache=$(cd / && go env GOMODCACHE 2>/dev/null) || modcache=${HOME}/go/pkg/mod
mkdir -p "${gocache}" "${modcache}"

# go runs as the caller, which git requires of a checkout's owner and which
# keeps what go writes into the checkout and the module cache the caller's.
# Rootless podman maps the caller's uid to itself only under keep-id; rootful
# podman and rootful Docker run a container as whatever uid they are told.
# Rootless Docker is not supported: it maps the caller to the container's
# root, so under --user git would see the checkout owned by another user.
if [[ $("${engine}" info --format '{{.Host.Security.Rootless}}' 2>/dev/null) == true ]]; then
	user=(--userns=keep-id)
else
	user=(--user "$(id -u):$(id -g)")
fi

mounts=(-v "${repo}:${repo}" -v "${modcache}:/go/pkg/mod")
# A worktree's .git names a directory inside the main checkout's git directory;
# when go cannot reach it, it stamps no version and says nothing.
common=$(git -C "${repo}" rev-parse --path-format=absolute --git-common-dir 2>/dev/null) || common=
if [[ -n "${common}" ]]; then
	common=$(cd "${common}" && pwd -P)
	[[ "${common}/" == "${repo}/"* ]] || mounts+=(-v "${common}:${common}")
fi

# SELinux labelling is disabled rather than relabelling the mounts, which are
# the caller's checkout and module cache.
exec "${engine}" run --rm --pull=never "${user[@]}" --security-opt label=disable --platform linux/amd64 \
	"${mounts[@]}" -w "${cwd}" \
	-e GOMODCACHE=/go/pkg/mod -e GOCACHE="${gocache}" -e HOME=/tmp \
	-e GOOS="${GOOS:-linux}" -e GOARCH="${GOARCH:-amd64}" -e GOAMD64="${GOAMD64:-}" \
	-e CGO_ENABLED="${CGO_ENABLED:-1}" -e GOFLAGS="${GOFLAGS:-}" \
	"${builder}" go "$@"
