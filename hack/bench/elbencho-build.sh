#!/usr/bin/env bash
# Builds the jhoblitt/elbencho fork at the commit it pins as a static binary
# with S3 support and installs it as hack/bench/out/elbencho. The fork adds
# --s3reqtimeout and per-phase error and retry counts, which the gateway
# comparison reads. The build follows the fork's
# build_helpers/docker/build_static_local.sh in an Alpine container, but keeps
# the source and its external libraries in hack/bench/out/elbencho-src between
# runs: the AWS SDK it builds is a clone of over a gigabyte and takes minutes.
# The external libraries are built again only when the pinned commit or image
# changes. The recipe's image is pinned by digest and its packages are not
# upgraded, since libcurl, which the binary links statically, shapes the
# client's connections. Alpine's repositories keep only each package's latest
# release, so a later build can still link newer packages; the stamp
# hack/bench/out/elbencho.commit records the commit, the version, the image
# and the toolchain and library packages a binary, its external libraries
# included, was built with, and a binary built from the pinned commit in the
# pinned image is kept as it is.
#
# Usage: elbencho-build.sh
#
#   RGW_GO_ENGINE  the container engine, podman when installed, else docker
#   ELBENCHO_JOBS  make's parallel jobs (the host's processors)
#   ELBENCHO_SRC   where the source and its external libraries are kept, about
#                  5 GiB once built (hack/bench/out/elbencho-src)
set -euo pipefail

bench_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly bench_dir
readonly script=elbencho-build.sh
readonly ELBENCHO_REPO=https://github.com/jhoblitt/elbencho
readonly ELBENCHO_COMMIT=0637a922cbb71d56625f553750dbfbf12e5f0167 # s3-error-counts; bump by hand
# Alpine 3.24.2's multi-architecture index; bump by hand.
readonly build_image=docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
readonly out=${bench_dir}/out
readonly bin=${out}/elbencho stamp=${out}/elbencho.commit
src=$(realpath -m "${ELBENCHO_SRC:-${out}/elbencho-src}")
readonly src

die() {
	echo "${script}: $*" >&2
	exit 1
}

# A src without a repository of its own must not let git find rgw-go's, or
# whatever repository holds the directory ELBENCHO_SRC names.
GIT_CEILING_DIRECTORIES=$(dirname "${src}")
export GIT_CEILING_DIRECTORIES

# fork_version prints the version the pinned Makefile stamps, as
# elbencho --version prints it.
fork_version() {
	awk '$1 == "EXE_VER_MAJOR" { major = $3 } $1 == "EXE_VER_MINOR" { minor = $3 }
		$1 == "EXE_VER_PATCHLEVEL" { patch = $3 }
		END { if (major == "" || minor == "" || patch == "") exit 1; print major "." minor "-" patch }' "${src}/Makefile"
}

# verify checks that the installed binary prints the fork's version and has
# the fork's --s3reqtimeout, which only --help-all lists.
verify() { # version
	local version help
	version=$("${bin}" --version | awk '$2 == "Version:" { print $3 }')
	[[ "${version}" == "$1" ]] || die "${bin} --version prints '${version}', not the fork's $1"
	# Read whole: grep -q would stop reading at the match, and under pipefail
	# elbencho's SIGPIPE would fail the check.
	help=$("${bin}" --help-all) || die "${bin} --help-all failed"
	grep -q -- --s3reqtimeout <<<"${help}" || die "${bin} has no --s3reqtimeout: it is not the fork's build"
	echo "${bin} is elbencho ${version} from ${ELBENCHO_COMMIT}"
}

# The stamp's first line is the commit an installed binary was built from,
# the version its Makefile stamped and the image it was built in, so a binary
# built from the pins is kept whether or not its source still is; its second
# line lists the packages the build linked and compiled with.
mkdir -p "${out}"
read -r built_commit built_version built_image 2>/dev/null <"${stamp}" || true
if [[ -x "${bin}" && "${built_commit:-}" == "${ELBENCHO_COMMIT}" && "${built_image:-}" == "${build_image}" &&
	-n "${built_version:-}" ]]; then
	verify "${built_version}"
	exit 0
fi

if [[ "$(git -C "${src}" rev-parse -q --verify HEAD 2>/dev/null)" != "${ELBENCHO_COMMIT}" ]]; then
	git init -q "${src}"
	git -C "${src}" fetch -q --depth 1 "${ELBENCHO_REPO}" "${ELBENCHO_COMMIT}"
	git -C "${src}" checkout -q --force FETCH_HEAD
fi
# Objects another commit's sources or another image's compilers built would
# link into this binary: elbencho's own are deleted here, and the external
# libraries, the AWS SDK among them, in the container unless the mark beside
# them names this commit, whose prepare-external.sh pins their tags, and this
# image. The mark is written only once a build has succeeded.
rm -f "${src}/bin/elbencho" "${src}/.build-packages"
find "${src}/source" -name '*.[od]' -delete
readonly externals_mark=${src}/external/.built-with
clean_externals=1
if [[ "$(cat "${externals_mark}" 2>/dev/null)" == "${ELBENCHO_COMMIT} ${build_image}" ]]; then
	clean_externals=0
fi
readonly clean_externals

engine=${RGW_GO_ENGINE:-}
[[ -n "${engine}" ]] || engine=$(command -v podman || command -v docker) || die "no podman or docker on PATH"
jobs=${ELBENCHO_JOBS:-$(nproc)}
[[ "${jobs}" =~ ^[1-9][0-9]*$ ]] || die "ELBENCHO_JOBS is ${jobs}, not a count"

# make runs as the caller, so that what it writes into the checkout is the
# caller's: rootless podman maps the container's root to the caller, and a
# rootful engine runs make as a user with the caller's uid, as the fork's
# recipe does.
if [[ $("${engine}" info --format '{{.Host.Security.Rootless}}' 2>/dev/null) == true ]]; then
	as_caller=
else
	as_caller="adduser -u $(id -u) -D builduser 2>/dev/null || true; exec sudo -u builduser"
fi

# The packages and make variables are the recipe's. Of its make clean-all,
# only clean-externals runs, and only when the external libraries must be
# built again, so that they are otherwise kept; its apk upgrade is left out,
# since it would move the image's own packages.
# shellcheck disable=SC2016 # expanded by the container's shell
"${engine}" run --rm --security-opt label=disable -v "${src}:${src}" -w "${src}" "${build_image}" sh -c '
	set -e
	apk add -q bash boost-dev build-base gcc g++ git libaio-dev make numactl-dev \
		cmake curl-dev curl-static openssl-libs-static \
		boost-static zlib-static libretls-static nghttp2-static nghttp3-static \
		brotli-static sudo tar libidn2-static libunistring-static \
		libpsl-static c-ares-dev zstd-static
	apk info -v 2>/dev/null | grep -E "^(gcc|g\+\+|musl|boost-dev|curl-static|openssl-libs-static|nghttp2-static|nghttp3-static|zlib-static|zstd-static|brotli-static|c-ares-dev|libidn2-static|libpsl-static)-[0-9]" |
		sort | tr "\n" " " >"$2"
	if [ "$3" = 1 ]; then make clean-externals; fi
	'"${as_caller}"' make -j "$1" ALTHTTPSVC_SUPPORT=1 S3_SUPPORT=1 S3_AWSCRT=0 USE_MIMALLOC=1 BUILD_STATIC=1
' build "${jobs}" "${src}/.build-packages" "${clean_externals}"

file "${src}/bin/elbencho" | grep -q 'statically linked' || die "${src}/bin/elbencho is not statically linked"
version=$(fork_version) || die "no version in ${src}/Makefile"
packages=$(<"${src}/.build-packages") || die "the build recorded no packages"
echo "${ELBENCHO_COMMIT} ${build_image}" >"${externals_mark}"
install -m 755 "${src}/bin/elbencho" "${bin}"
verify "${version}"
printf '%s %s %s\n%s\n' "${ELBENCHO_COMMIT}" "${version}" "${build_image}" "${packages% }" >"${stamp}"
