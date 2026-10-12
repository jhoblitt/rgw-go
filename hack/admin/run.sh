#!/usr/bin/env bash
# Runs go-ceph's rgw/admin suite, TestRadosGWTestSuite, at a pinned tag
# against one gateway of a rooket cluster and leaves its go test -json
# stream for hack/parity. Upstream hard-codes the suite's endpoint, so
# endpoint.patch makes it read RGW_GO_ADMIN_ENDPOINT. Upstream's subtests
# assert on their method's test, suite.T(), so a subtest passes whatever it
# checks and a failure shows only on its method; subtest-t.patch makes each
# subtest assert on its own, so hack/parity compares every check. The suite
# signs as the user admin, which this script creates with the keys and caps
# go-ceph's own CI gives it (testing/containers/micro-osd.sh) and removes
# when it exits. For the
# radosgw it also sets the usage-log options that CI sets, which TestUsage
# needs, on the radosgw's own config entity, and restarts the radosgw when
# that changes them. It removes the users, buckets and account it and the
# suite create before and after each run, so make gate passes after it.
#
# Usage: run.sh squid|tentacle radosgw|rgw-go RUN-ID
#        run.sh --print-tag | --print-deselect
set -euo pipefail

harness=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly harness
here=$(cd "${harness}/../rooket" && pwd)
readonly here
readonly script=run.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"

readonly GO_CEPH_REPO=https://github.com/ceph/go-ceph
# Bump by hand: regenerate both patches, recheck suite_version below, and
# record both ledgers in full with make admin-record, which appends a base
# pin at the new tag.
readonly GO_CEPH_TAG=v0.39.0

# The subtests of later phases' features, by go test id, one file per phase.
# A phase deletes its lines as it lands its features, and its file and entry
# here with the last of them.
readonly DESELECT_LISTS=("${harness}/deselect-phase2.txt")

# deselected prints the ids the lists name, without comments and blanks.
# Once no list is left, sed reads the empty stdin rather than the caller's.
deselected() { sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e '/^$/d' "${DESELECT_LISTS[@]}" </dev/null; }

case "${1:-}" in
--print-tag)
	echo "${GO_CEPH_TAG}"
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

# TestGetInfo connects through librados's default config search, $CEPH_CONF
# before /etc/ceph/ceph.conf, and compares the cluster's fsid with the one
# the gateway reports; only the release's own conf names the right cluster.
conf=${out}/ceph.conf
[[ -f "${conf}" ]] || die "no ${conf}: run make cluster-up RELEASE=${release} first"
if [[ "${gateway}" == rgw-go ]]; then
	[[ -f "${out}/rgw-go.endpoint" ]] || die "no ${out}/rgw-go.endpoint: rgw-go is not running against the ${release} cluster"
	endpoint=$(<"${out}/rgw-go.endpoint")
fi

# go-ceph picks its expectations for a release from CEPH_VERSION, as its CI
# sets it. At this tag the only ones that differ between squid, tentacle and
# main are account_test.go's skips of GET and DELETE on /admin/account, which
# it takes for every release up to tentacle. v19.2.6's radosgw checks a cap
# named account for both calls (rgw_rest_account.cc:174, :196), which no
# caps command grants, so they answer 403 to the admin user and Squid keeps
# the skips. v20.2.4's checks accounts, which the admin user holds, so
# Tentacle runs as main and both calls are compared.
case "${release}" in
squid) suite_version=squid ;;
tentacle) suite_version=main ;;
esac

# go's ./... skips a directory whose name starts with "_" or ".", but
# golangci-lint fmt skips only the latter, so the go-ceph checkout's own
# directory starts with "." to stay out of make check.
outdir=${harness}/_out
src=${outdir}/.go-ceph
name=${outdir}/${release}-${gateway}-${run_id}
report=${name}.jsonl
log=${name}.log
mkdir -p "${outdir}"
rm -f "${report}" "${log}"

# Without its own .git, git -C would find the rgw-go checkout around it, and
# the checkout and clean below would run on that. A .git that is not a
# repository would too, so git may not look above the output directory.
export GIT_CEILING_DIRECTORIES=${outdir}
if [[ ! -d "${src}/.git" ]]; then
	[[ ! -e "${src}" ]] || die "${src} is not a git checkout; remove it and rerun"
	git init -q "${src}"
fi
if ! git -C "${src}" rev-parse -q --verify "refs/tags/${GO_CEPH_TAG}^{commit}" >/dev/null 2>&1; then
	git -C "${src}" fetch -q --depth 1 "${GO_CEPH_REPO}" "refs/tags/${GO_CEPH_TAG}:refs/tags/${GO_CEPH_TAG}"
fi
# go test runs whatever the tree holds, so each run starts from the tag and
# the patches alone.
git -C "${src}" checkout -q -f --detach "refs/tags/${GO_CEPH_TAG}"
git -C "${src}" clean -q -f -d -x
for patch in endpoint.patch subtest-t.patch; do
	git -C "${src}" apply --whitespace=nowarn "${harness}/${patch}"
done

# go test's -skip takes one pattern, in which "|" separates alternatives and
# "/" the levels of a test id, each level an expression of its own, so each
# id becomes an alternative of anchored levels that matches it alone. go
# test names a subtest by its t.Run name with its spaces as underscores.
mapfile -t deselect < <(deselected)
skip=
for id in "${deselect[@]}"; do
	IFS=/ read -r -a parts <<<"${id}"
	if ((${#parts[@]} != 3)) || [[ "${parts[0]}" != TestRadosGWTestSuite ]]; then
		die "the deselect id ${id} is not TestRadosGWTestSuite/<method>/<subtest>"
	fi
	# The method's body runs from its signature to the first closing brace at
	# the start of a line, as gofmt lays a function out.
	body=$(awk -v sig="func (suite *RadosGWTestSuite) ${parts[1]}() {" \
		'index($0, sig) == 1 { on = 1 } on { print } on && /^}/ { exit }' "${src}"/rgw/admin/*_test.go)
	if ! grep -qF -- "Run(\"${parts[2]//_/ }\"" <<<"${body}"; then
		die "the deselect lists name ${id}, which go-ceph ${GO_CEPH_TAG}'s suite does not hold"
	fi
	alternative=
	for part in "${parts[@]}"; do
		# shellcheck disable=SC2001,SC2016 # sed escapes every regexp metacharacter
		part=$(sed -e 's/[][\\.|$(){}?+*^]/\\&/g' <<<"${part}")
		alternative+=${alternative:+/}^${part}\$
	done
	skip+=${skip:+|}${alternative}
done
skip_args=()
[[ -z "${skip}" ]] || skip_args=(-skip "${skip}")

use_site

readonly admin_uid=admin admin_key=AKIAIOSFODNN7EXAMPLE admin_secret=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
readonly admin_caps="buckets=*;users=*;usage=read;metadata=read;info=read;accounts=*"
# What the suite creates and removes again, which a run that stops partway
# leaves behind. Its tests count the zone's buckets and users and the admin
# user's keys, and create the account under a fixed id, so every run starts
# without them.
readonly suite_users=(leseb test test-user1 test-user2 test-user-bucket-rename)
readonly suite_buckets=(test bucket-object-lock initial-name renamed-name)
readonly suite_account=RGW12345678901234567

# A rerun after a caps change converges: caps add merges into the caps the
# user holds.
if admin user info --uid "${admin_uid}" >/dev/null 2>&1; then
	user=$(admin caps add --uid "${admin_uid}" --caps "${admin_caps}")
else
	user=$(admin user create --uid "${admin_uid}" --display-name "Admin User" \
		--access-key "${admin_key}" --secret-key "${admin_secret}" --caps "${admin_caps}")
fi
jq -e --arg ak "${admin_key}" --arg sk "${admin_secret}" \
	'any(.keys[]; .access_key == $ak and .secret_key == $sk)' <<<"${user}" >/dev/null ||
	die "the user ${admin_uid} exists without the key ${admin_key} the suite signs with; remove it and rerun"
# The user signs with AWS's documented example keys and holds the users,
# buckets and accounts caps, so it goes when the script exits, whether the
# run passed, failed or stopped partway; make gate also refuses it.
trap '"${harness}/remove-user.sh" "${release}" "${admin_uid}" || echo "${script}: could not remove the user ${admin_uid}" >&2' EXIT

reset_fixtures() {
	local bucket uid info key keys
	for bucket in "${suite_buckets[@]}"; do
		admin bucket stats --bucket "${bucket}" >/dev/null 2>&1 || continue
		admin bucket rm --bucket "${bucket}" --purge-objects
		# bucket rm exits 0 whether or not it removed the bucket.
		! admin bucket stats --bucket "${bucket}" >/dev/null 2>&1 ||
			die "radosgw-admin bucket rm left the bucket ${bucket}"
	done
	for uid in "${suite_users[@]}"; do
		admin user info --uid "${uid}" >/dev/null 2>&1 || continue
		admin user rm --uid "${uid}" --purge-data
	done
	if admin account get --account-id "${suite_account}" >/dev/null 2>&1; then
		admin account rm --account-id "${suite_account}"
	fi
	# TestKeys adds two keys to the admin user and removes them at its end.
	info=$(admin user info --uid "${admin_uid}")
	mapfile -t keys < <(jq -r --arg ak "${admin_key}" '.keys[] | select(.access_key != $ak) | .access_key' <<<"${info}")
	for key in "${keys[@]}"; do
		admin key rm --uid "${admin_uid}" --key-type s3 --access-key "${key}" >/dev/null
	done
}

if [[ "${gateway}" == radosgw ]]; then
	entity=$(rgw_entity "${rgw}")
	changed=0
	for kv in "${usage_options[@]}"; do
		[[ "$(ceph_cmd config get "${entity}" "${kv%%=*}")" != "${kv#*=}" ]] || continue
		ceph_cmd config set "${entity}" "${kv%%=*}" "${kv#*=}"
		changed=1
	done
	# radosgw reads the three options at runtime, but its usage logger takes
	# a new tick interval only when its timer next fires (rgw_log.cc), up to
	# 30 s later; a restart starts the timer at 1 s.
	if ((changed)); then
		k -n rook-ceph rollout restart deploy -l app=rook-ceph-rgw
		k -n rook-ceph rollout status deploy -l app=rook-ceph-rgw --timeout=5m
		use_site
	fi
	for kv in "${usage_options[@]}"; do
		[[ "$(ceph_cmd config show "${entity}" "${kv%%=*}")" == "${kv#*=}" ]] ||
			die "the radosgw runs without ${kv}"
	done
	endpoint=$(rgw_endpoint "${rgw}")
fi
[[ "${endpoint}" =~ ^http://([^:/]+):([0-9]+)/?$ ]] || die "the ${gateway} endpoint ${endpoint} is not http://HOST:PORT"
endpoint=${endpoint%/}
curl -fsS --max-time 10 --retry 30 --retry-delay 2 --retry-connrefused -o /dev/null "${endpoint}/" ||
	die "the host cannot reach the ${gateway} at ${endpoint}"

# The suite's S3 client would also read the caller's ~/.aws and AWS_*
# settings.
unset "${!AWS_@}"
export AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null

reset_fixtures
echo "running go-ceph ${GO_CEPH_TAG}'s TestRadosGWTestSuite against the ${gateway} at ${endpoint}; log ${log}"
status=0
(cd "${src}" && GOWORK=off CEPH_CONF="${conf}" CEPH_VERSION="${suite_version}" RGW_GO_ADMIN_ENDPOINT="${endpoint}" \
	go test -tags ceph_preview -count=1 -v -json -run TestRadosGWTestSuite "${skip_args[@]}" ./rgw/admin/) >"${report}" 2>"${log}" || status=$?
reset_fixtures

# go test exits 1 when a test failed, which hack/parity judges, but also
# when the package did not build or the test binary panicked; neither
# finishes the suite.
((status <= 1)) || die "go test exited ${status}; see ${log} and ${report}"
jq -e -s 'any(.[]; .Test == "TestRadosGWTestSuite" and (.Action == "pass" or .Action == "fail"))' \
	"${report}" >/dev/null || die "TestRadosGWTestSuite did not finish; see ${log} and ${report}"
! jq -e -s 'any(.[]; .Action == "output" and (.Output | startswith("panic: ")))' "${report}" >/dev/null ||
	die "the test binary panicked; see ${report}"
for id in "${deselect[@]}"; do
	! jq -e -s --arg id "${id}" 'any(.[]; .Test == $id)' "${report}" >/dev/null ||
		die "go test ran ${id}, which the deselect lists skip; see ${report}"
done
jq -r -s '[.[] | select((.Test // "" | startswith("TestRadosGWTestSuite/")) and (.Action | IN("pass", "fail", "skip")))]
	| group_by(.Action) | map("\(length) \(.[0].Action)") | join(", ")' "${report}"
echo "wrote ${report}"
