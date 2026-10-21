#!/usr/bin/env bash
# Runs s3-tests, the suite ceph/ceph carries in src/test/rgw/s3-tests, at a
# pinned commit against one gateway of a rooket cluster and leaves a junit
# report for hack/parity. The parity set is
# test_s3.py and test_headers.py minus the tests of the markers
# excluded-markers.txt lists, of features not implemented yet and of
# docs/exclusions.md, plus the tests the include-phase<N>.txt lists name:
# tests such a marker removes that a phase serves already, and minus the
# tests the deselect-phase<N>.txt lists name: tests that call a later
# phase's feature without carrying such a marker. hack/s3tests's Go command,
# which run.sh builds into bin/s3tests, decides it from those lists and
# pytest's collections with and without the markers, and pytest runs exactly
# its node ids on both gateways. That set is the gate set, which gate runs;
# pr runs the pr set, the gate set less the tests gate-only.txt names, which
# only a phase gate and a run on demand afford. The node ids a run selects
# are left beside its report, in its .ids file, and those of the gate set's
# gate-only tests, whichever set ran, in its .gate-only.ids file.
# fails_on_rgw stays in, since both gateways must fail those alike. Users
# are created through radosgw-admin, so run this after make gate: the gate
# counts the zone's users. TESTS-FILE runs only the pytest node ids it lists,
# one a line, each of which the set must hold.
#
# Usage: [S3TESTS_REPO=URL|PATH S3TESTS_COMMIT=SHA] run.sh squid|tentacle radosgw|rgw-go RUN-ID pr|gate [TESTS-FILE]
#        run.sh --print-commit | --print-deselect
set -euo pipefail

harness=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly harness
here=$(cd "${harness}/../rooket" && pwd)
readonly here
readonly script=run.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

# ceph/s3-tests is frozen; the suite moved into ceph/ceph. The pin is a commit
# on the rgw-go-s3tests branch of the jhoblitt/ceph fork: ceph/ceph main with
# the suite fixes rgw-go needs on top, each also meant for ceph/ceph. A commit
# is merged into that branch before the change that pins it, and the branch
# only ever gains commits, so no pin names a commit that a rewritten branch
# would take away.
#
# S3TESTS_REPO and S3TESTS_COMMIT in the environment run another repository,
# a local clone's path among them, at another commit, to try a change to
# s3-tests before it is pinned. --print-commit prints the commit that runs,
# which a recorded result carries, and says on stderr where it came from;
# hack/parity refuses to compare such a run with a baseline recorded at the
# pin, and to record it.
S3TESTS_REPO=${S3TESTS_REPO:-https://github.com/jhoblitt/ceph}
# git runs the fetch inside the checkout, so a path is made absolute here,
# from the directory run.sh starts in. Neither a URL nor git's host:path
# form is a path: the latter has a colon before any slash.
if [[ "${S3TESTS_REPO}" != *://* && ! "${S3TESTS_REPO}" =~ ^[^/]*: ]]; then
	[[ -d "${S3TESTS_REPO}" ]] || die "S3TESTS_REPO ${S3TESTS_REPO} is neither a URL nor a directory"
	S3TESTS_REPO=$(cd "${S3TESTS_REPO}" && pwd)
fi
readonly S3TESTS_REPO
# The pin is hack/s3tests/PIN, the single place it is set: exactly 40
# lowercase hex digits and a newline, read here by the rule hack/parity reads
# it by too. Move it with make s3tests-pin, or write it by hand and record the
# baselines in full. S3TESTS_COMMIT in the environment runs another commit for
# a local try.
pin_file=${harness}/PIN
[[ -f "${pin_file}" && ! -L "${pin_file}" && "$(wc -c <"${pin_file}")" -eq 41 ]] && IFS= read -r pin <"${pin_file}" &&
	[[ "${pin}" =~ ^[0-9a-f]{40}$ ]] || die "${pin_file} is not exactly 40 lowercase hex digits and a newline"
readonly pin_file pin
readonly S3TESTS_COMMIT=${S3TESTS_COMMIT:-${pin}}
readonly S3TESTS_DIR=src/test/rgw/s3-tests
# A result records the commit it ran, and the checkout and the venv are reused
# while the commit stays the same, so it must be a commit id: a branch or tag
# names whatever it points at when fetched.
[[ "${S3TESTS_COMMIT}" =~ ^[0-9a-f]{40}$ ]] || die "the s3-tests commit ${S3TESTS_COMMIT}, from the environment, is not a full commit id"
readonly PYTEST_TIMEOUT_VERSION=2.4.0

readonly TEST_FILES=(s3tests/functional/test_s3.py s3tests/functional/test_headers.py)
root=$(cd "${harness}/../.." && pwd)
readonly root
readonly s3tests=${root}/bin/s3tests
# The command reads each of its flags from S3TESTS_ variables too, the prefix
# run.sh's own S3TESTS_REPO and S3TESTS_COMMIT share, and from a config file
# S3TESTS_CONFIG names. Every call gives every flag, so that no such setting
# changes what a run runs.
readonly s3tests_flags=(--config "" --log-level info --log-format json --lists "${harness}")

case "${1:-}" in
--print-commit)
	echo "${S3TESTS_COMMIT}"
	if [[ "${S3TESTS_COMMIT}" == "${pin}" ]]; then
		echo "${script}: s3-tests ${S3TESTS_COMMIT}, from ${pin_file}" >&2
	else
		echo "${script}: s3-tests ${S3TESTS_COMMIT}, from the environment, not ${pin_file}'s ${pin}" >&2
	fi
	exit 0
	;;
esac
# Built from this tree on every run, so that no build older than the tree's
# rules decides the set; go build leaves a current one as it is.
go -C "${root}" build -o bin/s3tests ./hack/s3tests ||
	die "cannot build ${s3tests} with go build -o bin/s3tests ./hack/s3tests in ${root}"
if [[ "${1:-}" == --print-deselect ]]; then
	exec "${s3tests}" hash "${s3tests_flags[@]}"
fi

use_release "${1:-}"
gateway=${2:-}
run_id=${3:-}
test_set=${4:-}
readonly usage="usage: ${script} squid|tentacle radosgw|rgw-go RUN-ID pr|gate [TESTS-FILE]"
[[ "${gateway}" == radosgw || "${gateway}" == rgw-go ]] || die "${usage}"
[[ "${run_id}" =~ ^[A-Za-z0-9._-]+$ ]] || die "${usage}; RUN-ID, which names the reports, is [A-Za-z0-9._-]+"
[[ "${test_set}" == pr || "${test_set}" == gate ]] || die "${usage}"
# --tests is given empty for the whole set. expr refuses a TESTS file that is
# missing or names no test, before the run removes its outputs or fetches
# anything.
readonly run_flags=("${s3tests_flags[@]}" --set "${test_set}" --tests "${5:-}")
expr=$("${s3tests}" expr "${run_flags[@]}")

src=${harness}/out/src
suite=${src}/${S3TESTS_DIR}
venv=${harness}/out/venv
name=${harness}/out/${release}-${gateway}-${run_id}
conf=${name}.conf
report=${name}.xml
log=${name}.log
freeze=${name}.freeze
selected_ids=${name}.ids
gate_only_ids=${name}.gate-only.ids
mkdir -p "${harness}/out"
rm -f "${conf}" "${report}" "${log}" "${freeze}" "${selected_ids}" "${gate_only_ids}"

# Only the suite is fetched from all of Ceph: the one commit, its trees
# without their blobs, and then just the blobs the sparse checkout asks the
# remote for as it checks them out: those under S3TESTS_DIR, and the files
# directly in its parent directories and at the top level, which cone mode
# always keeps. The checkout is reused only at the same commit and with the
# same sparse directory.
if [[ "$(git -C "${src}" rev-parse -q --verify HEAD 2>/dev/null)" != "${S3TESTS_COMMIT}" ||
	"$(git -C "${src}" sparse-checkout list 2>/dev/null)" != "${S3TESTS_DIR}" ]]; then
	rm -rf "${src}"
	git init -q "${src}"
	git -C "${src}" remote add origin "${S3TESTS_REPO}"
	git -C "${src}" config remote.origin.promisor true
	git -C "${src}" config remote.origin.partialclonefilter blob:none
	git -C "${src}" sparse-checkout set --cone "${S3TESTS_DIR}"
	git -C "${src}" fetch -q --depth 1 --filter=blob:none origin "${S3TESTS_COMMIT}" ||
		die "cannot fetch ${S3TESTS_COMMIT} from ${S3TESTS_REPO}: a pin must be pushed to its branch first"
	git -C "${src}" checkout -q FETCH_HEAD
fi
# pytest would also load an untracked file, a conftest.py among them.
dirty=$(git -C "${src}" status --porcelain)
[[ -z "${dirty}" ]] ||
	die "${src} differs from s3-tests ${S3TESTS_COMMIT}; remove it and rerun:"$'\n'"${dirty}"

# requirements.txt pins no version, so the venv is built once per s3-tests
# commit and every report is left beside a record of what the venv held.
readonly venv_for="s3-tests ${S3TESTS_COMMIT} ${S3TESTS_DIR}, pytest-timeout ${PYTEST_TIMEOUT_VERSION}"
if [[ "$(cat "${venv}/built-for" 2>/dev/null)" != "${venv_for}" ]]; then
	rm -rf "${venv}"
	python3 -m venv "${venv}"
	"${venv}/bin/pip" install -q --disable-pip-version-check -r "${suite}/requirements.txt" \
		"pytest-timeout==${PYTEST_TIMEOUT_VERSION}"
	echo "${venv_for}" >"${venv}/built-for"
fi
{
	echo "# $("${venv}/bin/python" --version)"
	"${venv}/bin/pip" freeze --disable-pip-version-check
} >"${freeze}"

use_site

case "${gateway}" in
radosgw) endpoint=$(rgw_endpoint "${rgw}") ;;
rgw-go)
	[[ -f "${out}/rgw-go.endpoint" ]] || die "no ${out}/rgw-go.endpoint: rgw-go is not running against the ${release} cluster"
	endpoint=$(<"${out}/rgw-go.endpoint")
	;;
esac
[[ "${endpoint}" =~ ^http://([^:/]+):([0-9]+)/?$ ]] || die "the ${gateway} endpoint ${endpoint} is not http://HOST:PORT"
host=${BASH_REMATCH[1]}
port=${BASH_REMATCH[2]}
# Any HTTP answer will do: the anonymous GET / is itself in the set, as
# test_list_buckets_anonymous.
curl -sS --max-time 10 -o /dev/null "${endpoint}/" || die "the host cannot reach the ${gateway} at ${endpoint}"
api_name=$(admin zonegroup get | jq -r '.api_name // empty')

# The fixture users' keys are fixed, so every run writes the same conf.
readonly main_id=s3tests-main main_key=S3MAIN000000000000AK
readonly main_secret=s3main-secret-0123456789abcdefghijklmnopqrstuv
readonly alt_id=s3tests-alt alt_key=S3ALT0000000000000AK
readonly alt_secret=s3alt-secret-0123456789abcdefghijklmnopqrstuv
readonly tenant=s3tt tenant_uid=s3tests-tenant tenant_key=S3TENANT0000000000AK
readonly tenant_secret=s3tenant-secret-0123456789abcdefghijklmnopqrstuv
readonly tenant_id=${tenant}\$${tenant_uid}
readonly iam_id=s3tests-iam iam_key=S3IAM0000000000000AK
readonly iam_secret=s3iam-secret-0123456789abcdefghijklmnopqrstuv
readonly root1_id=s3tests-root1 root1_key=S3ROOT100000000000AK account1=RGW11111111111111111
readonly root1_secret=s3root1-secret-0123456789abcdefghijklmnopqrstuv
readonly root2_id=s3tests-root2 root2_key=S3ROOT200000000000AK account2=RGW22222222222222222
readonly root2_secret=s3root2-secret-0123456789abcdefghijklmnopqrstuv

# ensure_user ID ACCESS-KEY SECRET-KEY [ARG...] creates the user ID, which is
# tenant$uid for a tenant's, with its keys and the radosgw-admin arguments
# after them, unless it exists, and checks that it holds the keys.
ensure_user() {
	local id=$1 access_key=$2 secret_key=$3 user
	shift 3
	user=$(admin user info --uid "${id}" 2>/dev/null) ||
		user=$(admin user create --uid "${id}" --display-name "${id#*\$}" --email "${id#*\$}@rgw-go.test" \
			--access-key "${access_key}" --secret-key "${secret_key}" "$@")
	jq -e --arg ak "${access_key}" --arg sk "${secret_key}" \
		'any(.keys[]; .access_key == $ak and .secret_key == $sk)' <<<"${user}" >/dev/null ||
		die "the user ${id} exists without the key ${access_key} the conf names; remove it and rerun"
}

ensure_account() {
	admin account get --account-id "$1" >/dev/null 2>&1 ||
		admin account create --account-id "$1" --account-name "$2" >/dev/null
}

ensure_user "${main_id}" "${main_key}" "${main_secret}"
ensure_user "${alt_id}" "${alt_key}" "${alt_secret}"
ensure_user "${tenant_id}" "${tenant_key}" "${tenant_secret}"
ensure_user "${iam_id}" "${iam_key}" "${iam_secret}" --caps "user-policy=*;roles=*;oidc-provider=*"
# At this commit only tests the markers remove act as an account root. The
# accounts exist so that one left unmarked fails on radosgw's behaviour, not
# on an access key radosgw does not know.
ensure_account "${account1}" s3tests-acct1
ensure_user "${root1_id}" "${root1_key}" "${root1_secret}" --account-id "${account1}" --account-root
ensure_account "${account2}" s3tests-acct2
ensure_user "${root2_id}" "${root2_key}" "${root2_secret}" --account-id "${account2}" --account-root

# An account root's user_id is its account's id, as Ceph's
# qa/tasks/s3tests.py writes it: an account owns what its users create.
cat >"${conf}" <<EOF
[DEFAULT]
host = ${host}
port = ${port}
is_secure = False

[fixtures]
bucket prefix = s3t-{random}-

[s3 main]
display_name = ${main_id}
user_id = ${main_id}
email = ${main_id}@rgw-go.test
api_name = ${api_name}
access_key = ${main_key}
secret_key = ${main_secret}

[s3 alt]
display_name = ${alt_id}
user_id = ${alt_id}
email = ${alt_id}@rgw-go.test
access_key = ${alt_key}
secret_key = ${alt_secret}

[s3 tenant]
display_name = ${tenant_uid}
user_id = ${tenant_id}
email = ${tenant_uid}@rgw-go.test
access_key = ${tenant_key}
secret_key = ${tenant_secret}
tenant = ${tenant}

[iam]
display_name = ${iam_id}
user_id = ${iam_id}
email = ${iam_id}@rgw-go.test
access_key = ${iam_key}
secret_key = ${iam_secret}

[iam root]
access_key = ${root1_key}
secret_key = ${root1_secret}
account_id = ${account1}
user_id = ${account1}
email = ${root1_id}@rgw-go.test

[iam alt root]
access_key = ${root2_key}
secret_key = ${root2_secret}
account_id = ${account2}
user_id = ${account2}
email = ${root2_id}@rgw-go.test
EOF

# purge_buckets removes the buckets the s3 users own. s3-tests removes only
# those under its own run's random prefix, and test_list_buckets_paginated
# wants the main user to own none when it starts.
purge_buckets() {
	local id tenant_prefix buckets bucket
	for id in "${main_id}" "${alt_id}" "${tenant_id}"; do
		# radosgw-admin lists a tenant's buckets by name but finds them by
		# tenant/name.
		tenant_prefix=
		[[ "${id}" != *\$* ]] || tenant_prefix=${id%%\$*}/
		buckets=$(admin bucket list --uid "${id}" | jq -r '.[]')
		[[ -n "${buckets}" ]] || continue
		while read -r bucket; do
			admin bucket rm --bucket "${tenant_prefix}${bucket}" --purge-objects
		done <<<"${buckets}"
		# bucket rm exits 0 whether or not it removed the bucket.
		buckets=$(admin bucket list --uid "${id}" | jq -c '.')
		[[ "${buckets}" == "[]" ]] || die "radosgw-admin bucket rm left the ${id} user's buckets ${buckets}"
	done
}

# boto3 would also read the caller's ~/.aws and AWS_* settings, which can
# change what the tests send; s3-tests gives each client what it needs.
unset "${!AWS_@}"
export AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null

# test_s3.py parses the conf at import to parametrize its encryption tests,
# so collecting needs the conf too. The include lists name tests the markers
# remove, which only the collection without them holds.
collect() {
	(cd "${suite}" && S3TEST_CONF="${conf}" "${venv}/bin/pytest" --collect-only -q -p no:cacheprovider \
		"$@" "${TEST_FILES[@]}")
}
ids=$(collect -m "${expr}") || die "pytest could not collect the parity set:"$'\n'"${ids}"
unfiltered=$(collect) || die "pytest could not collect the tests without the markers:"$'\n'"${unfiltered}"
# --unfiltered and --gate-only-ids are given here rather than in run_flags,
# which expr takes too, and on every call, so that no S3TESTS_UNFILTERED or
# S3TESTS_GATE_ONLY_IDS reaches the selection.
selection=$("${s3tests}" select "${run_flags[@]}" --unfiltered <(printf '%s\n' "${unfiltered}") \
	--gate-only-ids "${gate_only_ids}" <<<"${ids}") ||
	die "hack/s3tests selected no set from the collections; nothing ran"
mapfile -t selected <<<"${selection}"
# hack/parity diff --selection compares a run of the pr set over these alone,
# and --gate-only names the tests it leaves out by design.
printf '%s\n' "${selected[@]}" >"${selected_ids}"

purge_buckets
echo "running ${#selected[@]} tests against the ${gateway} at ${endpoint}; log ${log}"
status=0
(cd "${suite}" && S3TEST_CONF="${conf}" "${venv}/bin/pytest" -v -rA -p no:cacheprovider \
	--timeout=300 --junitxml="${report}" "${selected[@]}") >"${log}" 2>&1 || status=$?
purge_buckets
# A run deletes hundreds of megabytes of objects, whose tails radosgw leaves
# to its garbage collector: that waits rgw_gc_obj_min_wait (2 h), and the
# parity settings turn it off. Collecting them now keeps runs from filling
# the OSD.
admin gc process --include-all

# pytest exits 1 when a test failed, which hack/parity judges; any other
# nonzero status means the run itself fell short.
summary=$(tail -n 1 "${log}")
((status <= 1)) || die "pytest exited ${status}: ${summary}; see ${log}"
echo "${summary}"
echo "wrote ${report}"
