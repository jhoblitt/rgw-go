#!/usr/bin/env bash
# Fails when .goreleaser.yaml, which can read neither, disagrees with the homes
# of what it repeats: each release's Ceph image, in its chart values
# (hack/rooket/<release>/values/), and the build tags, the Makefile's GO_TAGS.
#
# Usage: release-pins-check.sh
set -euo pipefail

image_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly image_dir
repo=$(cd "${image_dir}/../.." && pwd -P)
readonly repo
# lib.sh finds a release's chart values under `here`.
readonly here=${repo}/hack/rooket
readonly script=release-pins-check.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
(($# == 0)) || die "usage: ${script}"

readonly config=${repo}/.goreleaser.yaml
failed=0

fail() {
	echo "${script}: $*" >&2
	failed=1
}

# env_value prints the value the config's top-level env sets $1 to.
env_value() {
	awk -v name="$1" '/^env:/ { in_env = 1; next } /^[^ #]/ { in_env = 0 }
		in_env && $1 == "-" && index($2, name "=") == 1 { print substr($2, length(name) + 2) }' "${config}"
}

repository=$(env_value CEPH_REPOSITORY)
for r in squid tentacle; do
	use_release "${r}"
	want=$(pinned_image)
	var=${r^^}_CEPH_TAG
	got=${repository}:$(env_value "${var}")
	[[ "${got}" == "${want}" ]] ||
		fail "the config's CEPH_REPOSITORY and ${var} name ${got}, ${r}'s chart values pin ${want}"
done

# A Ceph tag or repository written outside the env block would escape the
# comparison above.
stray=$({
	grep -nE 'v[0-9]+\.[0-9]+\.[0-9]+' "${config}"
	[[ -z "${repository}" ]] || grep -nF -- "${repository}" "${config}"
} | grep -vE '^[0-9]+:[[:space:]]*(#|- (CEPH_REPOSITORY|[A-Z]+_CEPH_TAG)=)' | sort -nu) || true
[[ -z "${stray}" ]] || fail "name the Ceph images in the config only through its env block, not at: ${stray//$'\n'/; }"

go_tags=$(make -s --no-print-directory -C "${repo}" print-go-tags)
flags=$(grep -oE -- '-tags=[^[:space:]"]*' "${config}") || true
[[ -n "${flags}" ]] || fail "the config passes no -tags=, the Makefile's GO_TAGS is ${go_tags}"
while IFS= read -r flag; do
	[[ -z "${flag}" || "${flag}" == "-tags=${go_tags}" ]] ||
		fail "the config passes ${flag}, the Makefile's GO_TAGS is ${go_tags}"
done <<<"${flags}"

exit "${failed}"
