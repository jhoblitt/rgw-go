#!/usr/bin/env bash
# Runs ceph/s3-tests at a pinned commit against one gateway of a rooket
# cluster and leaves a junit report for hack/parity. The parity set is
# test_s3.py and test_headers.py minus the markers of features not
# implemented yet and of docs/exclusions.md, and minus the tests the
# deselect-phase<N>.txt lists name: tests that call a later phase's feature
# without carrying such a marker. Both gateways run exactly this set.
# fails_on_rgw stays in, since both gateways must fail those alike. Users
# are created through radosgw-admin, so run this after make gate: the gate
# counts the zone's users.
#
# Usage: run.sh squid|tentacle radosgw|rgw-go RUN-ID
#        run.sh --print-commit | --print-deselect
set -euo pipefail

harness=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly harness
here=$(cd "${harness}/../rooket" && pwd)
readonly here
readonly script=run.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

readonly S3TESTS_REPO=https://github.com/ceph/s3-tests
readonly S3TESTS_COMMIT=5522d1c351f75bc00ae0f64f742f3f095f5939d9 # master, 2026-05-27; bump by hand and re-record the baselines
readonly PYTEST_TIMEOUT_VERSION=2.4.0

# Features not implemented yet and docs/exclusions.md's, by s3-tests marker.
readonly EXCLUDED_MARKERS=(lifecycle lifecycle_expiration lifecycle_transition cloud_transition cloud_restore
	target_by_bucket versioning delete_marker object_lock object_ownership encryption bucket_encryption sse_s3
	s3select s3website s3website_routing_rules s3website_redirect_location bucket_logging bucket_logging_cleanup
	fails_without_logging_rollover checksum sns appendobject iam_account iam_cross_account iam_tenant iam_user
	iam_role user_policy role_policy group group_policy session_policy abac_test test_of_sts webidentity_test
	token_claims_trust_policy_test token_principal_tag_role_policy_test token_request_tag_trust_policy_test
	token_resource_tags_test token_role_tags_test token_tag_keys_test s3control)
readonly TEST_FILES=(s3tests/functional/test_s3.py s3tests/functional/test_headers.py)
# The tests of later phases' features that no marker above removes, by pytest
# node id, one file per phase. A phase deletes its lines as it lands its
# features, and its file and entry here with the last of them.
readonly DESELECT_LISTS=("${harness}/deselect-phase2.txt" "${harness}/deselect-phase3.txt")

# deselected prints the node ids the lists name, without comments and blanks.
# Once no list is left, sed reads the empty stdin rather than the caller's.
deselected() { sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e '/^$/d' "${DESELECT_LISTS[@]}" </dev/null; }

case "${1:-}" in
--print-commit)
	echo "${S3TESTS_COMMIT}"
	exit 0
	;;
--print-deselect)
	deselected | LC_ALL=C sort | sha256sum | cut -c1-12
	exit 0
	;;
esac

use_release "${1:-}"
gateway=${2:-}
run_id=${3:-}
readonly usage="usage: ${script} squid|tentacle radosgw|rgw-go RUN-ID"
[[ "${gateway}" == radosgw || "${gateway}" == rgw-go ]] || die "${usage}"
[[ "${run_id}" =~ ^[A-Za-z0-9._-]+$ ]] || die "${usage}; RUN-ID, which names the reports, is [A-Za-z0-9._-]+"

src=${harness}/out/src
venv=${harness}/out/venv
name=${harness}/out/${release}-${gateway}-${run_id}
conf=${name}.conf
report=${name}.xml
log=${name}.log
freeze=${name}.freeze
mkdir -p "${harness}/out"
rm -f "${conf}" "${report}" "${log}" "${freeze}"

if [[ "$(git -C "${src}" rev-parse -q --verify HEAD 2>/dev/null)" != "${S3TESTS_COMMIT}" ]]; then
	git init -q "${src}"
	git -C "${src}" fetch -q --depth 1 "${S3TESTS_REPO}" "${S3TESTS_COMMIT}"
	git -C "${src}" checkout -q FETCH_HEAD
fi
# pytest would also load an untracked file, a conftest.py among them.
dirty=$(git -C "${src}" status --porcelain)
[[ -z "${dirty}" ]] ||
	die "${src} differs from s3-tests ${S3TESTS_COMMIT}; remove it and rerun:"$'\n'"${dirty}"

# requirements.txt pins no version, so the venv is built once per s3-tests
# commit and every report is left beside a record of what the venv held.
readonly venv_for="s3-tests ${S3TESTS_COMMIT}, pytest-timeout ${PYTEST_TIMEOUT_VERSION}"
if [[ "$(cat "${venv}/built-for" 2>/dev/null)" != "${venv_for}" ]]; then
	rm -rf "${venv}"
	python3 -m venv "${venv}"
	"${venv}/bin/pip" install -q --disable-pip-version-check -r "${src}/requirements.txt" \
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

expr="not ${EXCLUDED_MARKERS[0]}"
for m in "${EXCLUDED_MARKERS[@]:1}"; do expr+=" and not ${m}"; done

# test_s3.py parses the conf at import to parametrize its encryption tests,
# so collecting needs the conf too.
ids=$(cd "${src}" && S3TEST_CONF="${conf}" "${venv}/bin/pytest" --collect-only -q -p no:cacheprovider \
	-m "${expr}" "${TEST_FILES[@]}") || die "pytest could not collect the parity set:"$'\n'"${ids}"
mapfile -t deselect < <(deselected)
declare -A listed=()
deselect_args=()
for id in "${deselect[@]}"; do
	listed[${id}]=1
	deselect_args+=("--deselect=${id}")
done
# pytest's --deselect removes every node id that starts with the argument, so
# a line must remove exactly the test it names, and a line that names nothing
# the markers leave is stale.
mapfile -t collected < <(grep -E '^s3tests/.+\.py::' <<<"${ids}")
declare -A seen=()
selected=0
for c in "${collected[@]}"; do
	if [[ -n "${listed[${c%%\[*}]:-}" ]]; then
		seen[${c%%\[*}]=1
		continue
	fi
	for id in "${deselect[@]}"; do
		[[ "${c}" != "${id}"* ]] || die "${id} would also deselect ${c} (pytest matches --deselect as a node-id prefix)"
	done
	selected=$((selected + 1))
done
for id in "${deselect[@]}"; do
	[[ -n "${seen[${id}]:-}" ]] || die "${id} names no test the markers leave: renamed, removed or marker-excluded at this commit"
done

purge_buckets
echo "running ${selected} of the ${#collected[@]} tests the markers leave against the ${gateway} at ${endpoint}; log ${log}"
status=0
(cd "${src}" && S3TEST_CONF="${conf}" "${venv}/bin/pytest" -v -rA -p no:cacheprovider \
	--timeout=300 --junitxml="${report}" -m "${expr}" "${deselect_args[@]}" "${TEST_FILES[@]}") >"${log}" 2>&1 || status=$?
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
