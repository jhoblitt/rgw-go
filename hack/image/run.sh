#!/usr/bin/env bash
# Runs a command inside one release's builder (builder.sh), or under
# RGW_GO_STAGE=check its check image, so that it compiles against, links and
# runs with the librados, tcmalloc and glibc of the Ceph image the release
# ships in. The checkout is mounted at its own path and the command runs in the
# caller's directory, so relative paths such as -o's resolve as they do on the
# host. GOOS, GOARCH, GOAMD64, CGO_ENABLED and GOFLAGS pass through from the
# caller's environment, which is where goreleaser puts a build's target and env
# when it runs the build's tool, and GOMAXPROCS, GOLANGCI_LINT_CONCURRENCY and
# CITECHECK_CEPH do when they are set. RGW_GO_TEST_CEPH_VERSION names the
# release's Ceph version, for the specs that check the librados they run.
# Arguments before -- go to the engine's run as they are, after its own and
# before the image, so a caller adds --network=host, mounts or variables.
# RGW_GO_ENGINE picks the container engine, podman when installed, else docker.
#
# Usage: run.sh [engine run arguments...] -- squid|tentacle command [arguments...]
set -euo pipefail

image_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly image_dir
repo=$(cd "${image_dir}/../.." && pwd -P)
readonly repo
# lib.sh finds a release's chart values under `here`.
readonly here=${repo}/hack/rooket
readonly script=run.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

engine_args=()
while (($#)) && [[ "$1" != -- ]]; do
	engine_args+=("$1")
	shift
done
(($# >= 3)) || die "usage: ${script} [engine run arguments...] -- squid|tentacle command [arguments...]"
use_release "$2"
shift 2
cwd=$(pwd -P)
[[ "${cwd}/" == "${repo}/"* ]] || die "run it from inside the checkout, ${repo}"
version=$(pinned_version)
image=$("${image_dir}/builder.sh" "${release}")
builder=$(RGW_GO_STAGE=builder "${image_dir}/builder.sh" --print "${release}")
engine=${RGW_GO_ENGINE:-}
[[ -n "${engine}" ]] || engine=$(command -v podman || command -v docker) || die "no podman or docker on PATH"

# go's build cache does not notice a change in the C headers cgo compiles
# against, so each builder, and so each librados, has a cache of its own, which
# its check image shares.
gocache=${repo}/hack/image/out/gocache/${builder##*:}
# Outside the checkout, go env reads no go.mod, so a host Go older than its go
# line does not switch to, and under GOTOOLCHAIN=auto download, a newer
# toolchain just to print a path.
modcache=$(cd / && go env GOMODCACHE 2>/dev/null) || modcache=${HOME}/go/pkg/mod
mkdir -p "${gocache}" "${modcache}"

# The command runs as the caller, which git requires of a checkout's owner and
# which keeps what it writes into the checkout and the module cache the
# caller's. Rootless podman maps the caller's uid to itself only under keep-id;
# rootful podman and rootful Docker run a container as whatever uid they are
# told. Rootless Docker is not supported: it maps the caller to the container's
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

env=(-e GOMODCACHE=/go/pkg/mod -e GOCACHE="${gocache}" -e HOME=/tmp
	-e GOOS="${GOOS:-linux}" -e GOARCH="${GOARCH:-amd64}" -e GOAMD64="${GOAMD64:-}"
	-e CGO_ENABLED="${CGO_ENABLED:-1}" -e GOFLAGS="${GOFLAGS:-}")
for name in GOMAXPROCS GOLANGCI_LINT_CONCURRENCY CITECHECK_CEPH; do
	[[ -z "${!name:-}" ]] || env+=(-e "${name}=${!name}")
done
if [[ "${image}" != "${builder}" ]]; then
	mkdir -p "${repo}/hack/image/out/lintcache/${builder##*:}"
	env+=(-e GOLANGCI_LINT_CACHE="${repo}/hack/image/out/lintcache/${builder##*:}")
fi
env+=(-e RGW_GO_TEST_CEPH_VERSION="${version}")

# SELinux labelling is disabled rather than relabelling the mounts, which are
# the caller's checkout and module cache.
exec "${engine}" run --rm --pull=never "${user[@]}" --security-opt label=disable --platform linux/amd64 \
	"${mounts[@]}" -w "${cwd}" "${env[@]}" "${engine_args[@]}" "${image}" "$@"
