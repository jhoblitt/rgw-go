# shellcheck shell=bash
# shellcheck disable=SC2034,SC2154 # its variables are set and read by the scripts that source it
# Sourced by the hack/rooket scripts. Callers set `here` to this directory and
# `script` to their own name before sourcing it.

# The Rook release every cluster deploys, which supports both Ceph pins. Bump
# it by hand: rooket takes an exact tag, never a range.
readonly rook_version=v1.20.7
readonly rooket=${ROOKET:-rooket}

die() {
	echo "${script}: $*" >&2
	exit 1
}

# use_release validates $1 and sets release, out and ROOKET_NAME for it. Every
# rooket call names the cluster through ROOKET_NAME: without it rooket falls
# back to a name derived from the working directory.
use_release() {
	release=${1:-}
	[[ "${release}" == squid || "${release}" == tentacle ]] || die "usage: ${script} squid|tentacle"
	out=${here}/out/${release}
	export ROOKET_NAME=rgw-go-${release}
}

# pinned_tag prints the Ceph image tag the release's chart values pin under
# cephImage, the one place a release's Ceph version is set.
pinned_tag() {
	local values=${here}/${release}/values/rook-ceph-cluster.yaml tag
	tag=$(awk '/^cephImage:/ { in_image = 1; next } /^[^ #]/ { in_image = 0 } in_image && $1 == "tag:" { print $2 }' \
		"${values}")
	[[ -n "${tag}" ]] || die "no cephImage.tag in ${values}"
	echo "${tag}"
}

# pinned_version prints the Ceph version the pinned tag names, as the daemons
# report it: v19.2.6 is 19.2.6. A tag that is not a plain version is refused,
# so a dated build tag cannot pass for its release.
pinned_version() {
	local tag
	tag=$(pinned_tag)
	[[ "${tag}" =~ ^v([0-9]+\.[0-9]+\.[0-9]+)$ ]] || die "cephImage.tag ${tag} is not a vX.Y.Z version"
	echo "${BASH_REMATCH[1]}"
}

k() {
	"${rooket}" kubectl "$@"
}

# toolbox runs a command in the Rook toolbox, which runs the cluster's Ceph
# image with the admin keyring.
toolbox() {
	k -n rook-ceph exec deploy/rook-ceph-tools -- "$@"
}

# ceph_cmd bounds both the connect and each mon operation, so a wedged mon
# fails the command rather than hanging it.
ceph_cmd() {
	toolbox ceph --connect-timeout=20 --rados-mon-op-timeout=20 "$@"
}

# rgw_daemon prints the service map entry of the cluster's one radosgw: the
# realm, zonegroup and zone it serves are in its metadata.
rgw_daemon() {
	local daemons
	daemons=$(ceph_cmd service dump -f json | jq -c '[.services.rgw.daemons // {} | to_entries[] | select(.key != "summary") | .value]')
	[[ $(jq length <<<"${daemons}") == 1 ]] || die "want one radosgw in the service map, have ${daemons}"
	jq -c '.[0]' <<<"${daemons}"
}

# rgw_endpoint prints the URL the host reaches the radosgw rgw_daemon printed
# at: it is host-networked, so its pod's IP is its node's.
rgw_endpoint() {
	local ip port
	ip=$(k -n rook-ceph get pods -l app=rook-ceph-rgw -o jsonpath='{.items[*].status.podIP}')
	[[ "${ip}" =~ ^[0-9.]+$ ]] || die "want one radosgw pod IP, have '${ip}'"
	port=$(jq -r '.metadata["frontend_config#0"] | capture("port=(?<p>[0-9]+)").p' <<<"$1")
	[[ "${port}" =~ ^[0-9]+$ ]] || die "no port in the radosgw's frontend config: $1"
	echo "http://${ip}:${port}"
}
