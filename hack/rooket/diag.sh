#!/usr/bin/env bash
# Collects what explains a failed run against one release's cluster into a
# directory, for CI to upload: the pods' state and logs, the Rook resources'
# status, Ceph's own view, and hack/rooket/out/<release>/. Every step is best
# effort, so a half-built cluster still yields what it has, and every call is
# bounded. Nothing collected holds the admin keyring or an S3 secret key: the
# keyring is left out and the secrets are cut from the manifest.
#
# Usage: diag.sh squid|tentacle DIR
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=diag.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
dir=${2:-}
[[ -n "${dir}" ]] || die "usage: ${script} squid|tentacle DIR"
mkdir -p "${dir}/logs"

# Every call is bounded, so a wedged API server or mon costs a minute rather
# than the step's whole budget.
kb() {
	timeout --kill-after=10 60 "${rooket}" kubectl "$@"
}
cephb() {
	kb -n rook-ceph exec deploy/rook-ceph-tools -- ceph --connect-timeout=20 --rados-mon-op-timeout=20 "$@"
}

# save FILE CMD... runs CMD with its output, errors included, in FILE.
save() {
	local file=$1
	shift
	"$@" >"${dir}/${file}" 2>&1 || echo "(exit $?)" >>"${dir}/${file}"
}

save rooket-list.txt timeout --kill-after=10 60 "${rooket}" list
save nodes.txt kb get nodes -o wide
save pods.txt kb get pods -A -o wide
save events.txt kb get events -A --sort-by=.lastTimestamp
save pods-describe.txt kb -n rook-ceph describe pods
save rook-resources.yaml kb -n rook-ceph get cephclusters,cephobjectstores -o yaml
save ceph-status.txt cephb status
save ceph-health.txt cephb health detail
save ceph-versions.txt cephb versions
save ceph-osd-tree.txt cephb osd tree
save ceph-pools.txt cephb osd pool ls detail
save ceph-service-map.json cephb service dump -f json

for pod in $(kb get pods -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}' 2>/dev/null); do
	log=${dir}/logs/${pod//\//_}
	save "logs/${pod//\//_}.log" kb -n "${pod%%/*}" logs "${pod#*/}" --all-containers --prefix
	# Only a pod that restarted has a previous container to log.
	kb -n "${pod%%/*}" logs "${pod#*/}" --all-containers --prefix --previous >"${log}.previous.log" 2>/dev/null ||
		rm -f "${log}.previous.log"
done

if [[ -d "${out}" ]]; then
	mkdir -p "${dir}/out"
	for f in "${out}"/*; do
		case "${f##*/}" in
		*.keyring) ;;
		manifest.json) jq 'del(.users[]?.secret_key)' "${f}" >"${dir}/out/manifest.json" ;;
		*) cp "${f}" "${dir}/out/" ;;
		esac
	done
fi
echo "collected ${ROOKET_NAME} diagnostics in ${dir}"
