#!/usr/bin/env bash
# Points Rook's e2e installer at IMAGE for RELEASE by rewriting the release's
# test-image constant in the Rook checkout at DIR: the installer takes the Ceph
# image only from those constants (tests/framework/installer/ceph_installer.go,
# ReturnCephVersion, keyed by CEPH_SUITE_VERSION). It fails unless exactly one
# constant is found in the form Rook ships, `<const> = "quay.io/ceph/ceph:vNN"`,
# so a renamed or reshaped constant cannot leave the suite on stock Ceph.
#
# Usage: set-image.sh squid|tentacle IMAGE [DIR]
set -euo pipefail

readonly script=set-image.sh
die() {
	echo "${script}: $*" >&2
	exit 1
}

(($# >= 2 && $# <= 3)) || die "usage: ${script} squid|tentacle IMAGE [DIR]"
release=$1
image=$2
dir=${3:-.}
case "${release}" in
squid) const=squidTestImage ;;
tentacle) const=tentacleTestImage ;;
*) die "release is squid or tentacle, not '${release}'" ;;
esac
# The image lands in a Go string literal through a sed replacement, so it is
# held to the characters an image reference uses.
[[ "${image}" =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] || die "'${image}' is not an image reference"

f=${dir}/tests/framework/installer/ceph_installer.go
[[ -f "${f}" ]] || die "${f} not found"
decl="[[:space:]]*${const}[[:space:]]*=[[:space:]]*"
n=$(grep -cE "^${decl}\"quay\.io/ceph/ceph:v[0-9]+\"[[:space:]]*$" "${f}") || true
((n == 1)) || die "want one ${const} = \"quay.io/ceph/ceph:vNN\" in ${f}, found ${n:-0}; Rook changed, update this script"

sed -i -E "s|^(${decl})\"[^\"]*\"|\1\"${image}\"|" "${f}"
got=$(sed -n -E "s|^${decl}\"([^\"]*)\".*|\1|p" "${f}")
[[ "${got}" == "${image}" ]] || die "${const} reads '${got}' after the rewrite, not ${image}"
(cd "${dir}" && go vet ./tests/framework/installer/)
echo "${f}: ${const} = \"${image}\""
