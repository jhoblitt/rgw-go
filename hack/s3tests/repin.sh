#!/usr/bin/env bash
# Moves the s3-tests pin to a commit that differs from it only in its
# messages, such as the pinned branch once its commits are signed: run.sh's
# S3TESTS_COMMIT and both baselines' s3tests_commit, together, since
# hack/parity refuses a baseline recorded under another commit. The outcomes
# the baselines hold were recorded from the same trees, so they stay valid.
#
# CLONE is a clone of Ceph that holds both the pinned commit and NEW. Each
# commit on the pinned branch since it left NEW's line must have the tree of
# the matching commit on NEW's, in the same order; anything else needs the
# baselines re-recorded instead.
#
# Usage: repin.sh CLONE NEW
set -euo pipefail

harness=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly harness
repo=$(cd "${harness}/../.." && pwd -P)
readonly repo
readonly here=${repo}/hack/rooket
readonly script=repin.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
(($# == 2)) || die "usage: ${script} CLONE NEW"
clone=$1
new=$2
[[ "${new}" =~ ^[0-9a-f]{40}$ ]] || die "${new} is not a full commit id"
git -C "${clone}" rev-parse -q --verify HEAD >/dev/null 2>&1 || die "${clone} is not a git clone"

readonly run_sh=${harness}/run.sh
readonly baselines=("${repo}/test/s3tests/baseline/squid.json" "${repo}/test/s3tests/baseline/tentacle.json")
old=$(env -u S3TESTS_REPO -u S3TESTS_COMMIT "${run_sh}" --print-commit)
[[ "${old}" != "${new}" ]] || die "the pin is already ${new}"
for c in "${old}" "${new}"; do
	git -C "${clone}" cat-file -e "${c}^{commit}" 2>/dev/null || die "${clone} does not hold the commit ${c}"
done

# The checks below and the edits use the same patterns, so a place the
# checks pass is a place the edits change.
readonly pin_re="^readonly S3TESTS_COMMIT=\${S3TESTS_COMMIT:-${old}}"
readonly pin_new="readonly S3TESTS_COMMIT=\${S3TESTS_COMMIT:-${new}}"
readonly meta_re="\"s3tests_commit\": \"${old}\""
readonly meta_new="\"s3tests_commit\": \"${new}\""

# Each place must hold the pin exactly once before any of them changes.
grep -c -- "${pin_re}" "${run_sh}" | grep -qx 1 ||
	die "${run_sh} does not pin ${old} where ${script} expects it"
for b in "${baselines[@]}"; do
	[[ "$(jq -r '.meta.s3tests_commit' "${b}")" == "${old}" ]] ||
		die "${b} was recorded at $(jq -r '.meta.s3tests_commit' "${b}"), not at the pin ${old}: re-record it"
	grep -c -- "${meta_re}" "${b}" | grep -qx 1 || die "${b} does not hold the pin where ${script} expects it"
done

# The proof: the same number of commits since the two lines met, and the
# same tree at each.
base=$(git -C "${clone}" merge-base "${old}" "${new}") || die "${old} and ${new} share no history"
mapfile -t olds < <(git -C "${clone}" rev-list --reverse --first-parent "${base}..${old}")
mapfile -t news < <(git -C "${clone}" rev-list --reverse --first-parent "${base}..${new}")
((${#olds[@]} > 0)) || die "${old} is an ancestor of ${new}: that adds commits, so re-record the baselines"
((${#olds[@]} == ${#news[@]})) ||
	die "${#olds[@]} commits lead from ${base} to the pin but ${#news[@]} to ${new}: re-record the baselines"
for i in "${!olds[@]}"; do
	ot=$(git -C "${clone}" rev-parse "${olds[$i]}^{tree}")
	nt=$(git -C "${clone}" rev-parse "${news[$i]}^{tree}")
	[[ "${ot}" == "${nt}" ]] ||
		die "commit $((i + 1)) after ${base}: ${olds[$i]} has the tree ${ot} but ${news[$i]} has ${nt}: re-record the baselines"
	echo "${olds[$i]} -> ${news[$i]}: tree ${nt}"
done

# The three places move together or not at all: run.sh first, checked as
# run.sh reads it, then the baselines, and any failure puts all three back.
backup=$(mktemp -d)
readonly backup
trap 'rm -rf "${backup}"' EXIT
cp "${run_sh}" "${backup}/run.sh"
for i in "${!baselines[@]}"; do cp "${baselines[$i]}" "${backup}/baseline.${i}"; done
undo() {
	cp "${backup}/run.sh" "${run_sh}"
	for i in "${!baselines[@]}"; do cp "${backup}/baseline.${i}" "${baselines[$i]}"; done
	die "$*; nothing moved"
}

sed -i "s/${pin_re}/${pin_new}/" "${run_sh}" || undo "cannot edit ${run_sh}"
[[ "$(env -u S3TESTS_REPO -u S3TESTS_COMMIT "${run_sh}" --print-commit)" == "${new}" ]] ||
	undo "run.sh does not pin ${new} after the edit"
for b in "${baselines[@]}"; do
	sed -i "s/${meta_re}/${meta_new}/" "${b}" || undo "cannot edit ${b}"
	jq -e --arg c "${new}" '.meta.s3tests_commit == $c' "${b}" >/dev/null || undo "${b} does not carry ${new} after the edit"
done
echo "pinned ${new} in hack/s3tests/run.sh and test/s3tests/baseline/{squid,tentacle}.json"
