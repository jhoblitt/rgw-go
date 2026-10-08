# shellcheck shell=bash
# shellcheck disable=SC2034,SC2154 # its variables are set and read by the scripts that source it
# Sourced by the hack/rooket scripts. Callers set `here` to this directory and
# `script` to their own name before sourcing it.

# The Rook release every cluster deploys, which supports both Ceph pins. Bump
# it by hand: rooket takes an exact tag, never a range.
readonly rook_version=v1.20.7
readonly rooket=${ROOKET:-rooket}

# The usage-log options go-ceph's CI sets (testing/containers/micro-osd.sh),
# which its rgw/admin suite's TestUsage needs, as option=value.
readonly usage_options=(rgw_enable_usage_log=true rgw_usage_log_tick_interval=1 rgw_usage_log_flush_threshold=1)
# What both gateways run with when they are compared, as option=value: no
# background worker beside the requests, no local read cache between the
# gateway and RADOS, and the usage options.
readonly parity_options=(rgw_dynamic_resharding=false rgw_enable_gc_threads=false rgw_enable_lc_threads=false
	rgw_run_sync_thread=false rgw_d3n_l1_local_datacache_enabled=false "${usage_options[@]}")

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

# pinned_field prints the field $1 of cephImage in the release's chart values,
# the one place a release's Ceph image is set.
pinned_field() {
	local values=${here}/${release}/values/rook-ceph-cluster.yaml value
	value=$(awk -v field="$1:" '/^cephImage:/ { in_image = 1; next } /^[^ #]/ { in_image = 0 } in_image && $1 == field { print $2 }' \
		"${values}")
	[[ -n "${value}" ]] || die "no cephImage.$1 in ${values}"
	echo "${value}"
}

# pinned_tag prints the Ceph image tag the release's chart values pin.
pinned_tag() {
	pinned_field tag
}

# pinned_image prints the Ceph image the release's chart values pin, the one
# the cluster runs and the one an image deriving from it must be built from.
pinned_image() {
	local repository tag
	# Callers run this in a command substitution, where errexit is off, so a
	# failed read must end it explicitly.
	repository=$(pinned_field repository) || exit
	tag=$(pinned_tag) || exit
	echo "${repository}:${tag}"
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

# wait_rgw polls Ceph's service map until the jq filter $3, given the
# arguments after it, holds for the array of its radosgw entries, and prints
# that array. A radosgw registers under a new gid, its librados instance id,
# each time it starts or reloads its realm. The manager drops the old gid as
# soon as that instance closes its session, but one that ends without closing
# it stays until it has sent no beacon for mgr_service_beacon_grace (60 s), so
# after a restart or a reload the map can list both for about a minute. After
# 180 s it stops, reporting that $1 and, with the entries it last read, the
# caller's diagnosis $2; it never restarts a radosgw itself.
wait_rgw() {
	local what=$1 diagnosis=$2 filter=$3 deadline=$((SECONDS + 180)) daemons
	shift 3
	until daemons=$(ceph_cmd service dump -f json |
		jq -c '[.services.rgw.daemons // {} | to_entries[] | select(.key != "summary") | .value]') &&
		jq -e "$@" "${filter}" <<<"${daemons}" >/dev/null; do
		if ((SECONDS >= deadline)); then
			[[ -n "${daemons}" ]] || die "${what} in 180s: ceph service dump failed, so the service map could not be read"
			die "${what} in 180s (radosgw entries: ${daemons}); ${diagnosis}"
		fi
		sleep 5
	done
	echo "${daemons}"
}

# rgw_daemon prints the service map entry of the cluster's one radosgw: the
# realm, zonegroup and zone it serves are in its metadata.
rgw_daemon() {
	wait_rgw "the service map did not settle to one radosgw" \
		"the manager drops a stopped radosgw within about a minute, so none is running or more than one is" \
		'length == 1' | jq -c '.[0]'
}

# use_site sets rgw to the service map entry of the cluster's one radosgw, and
# realm, zonegroup and zone to the site it serves, which admin names.
use_site() {
	rgw=$(rgw_daemon)
	realm=$(jq -r '.metadata.realm_name // empty' <<<"${rgw}")
	zonegroup=$(jq -r '.metadata.zonegroup_name // empty' <<<"${rgw}")
	zone=$(jq -r '.metadata.zone_name // empty' <<<"${rgw}")
	[[ -n "${realm}" && -n "${zonegroup}" && -n "${zone}" ]] ||
		die "the radosgw reports no realm, zonegroup or zone: ${rgw}"
}

# use_manifest_site sets realm, zonegroup and zone to the site manifest.json
# records, which admin names. Unlike use_site it reads no service map, so it
# answers at once while a restarted radosgw's old entry lingers there.
use_manifest_site() {
	local manifest=${out}/manifest.json
	[[ -f "${manifest}" ]] || die "no ${manifest}; run make populate RELEASE=${release} first"
	realm=$(jq -r '.realm // empty' "${manifest}")
	zonegroup=$(jq -r '.zonegroup // empty' "${manifest}")
	zone=$(jq -r '.zone // empty' "${manifest}")
	[[ -n "${realm}" && -n "${zonegroup}" && -n "${zone}" ]] ||
		die "${manifest} names no realm, zonegroup or zone"
}

# Without the site options radosgw-admin works in a zone named default, which
# it creates on first use and the radosgw never serves.
admin() {
	toolbox radosgw-admin "$@" --rgw-realm="${realm}" --rgw-zonegroup="${zonegroup}" --rgw-zone="${zone}"
}

# rgw_entity prints the config entity of the radosgw whose service map entry
# is $1. Rook runs the radosgw as client.rgw. and its deployment's name past
# rook-ceph-rgw, dashes mapped to dots (generateCephXUser in rook
# pkg/operator/ceph/object/config.go), and the service map's id is that name
# past client.rgw. config show answers only for a section a running daemon
# reads, with the frontends that daemon reports, so a wrong name stops here
# rather than setting options nothing reads.
rgw_entity() {
	local rgw=$1 id entity frontends want
	id=$(jq -r '.metadata.id // empty' <<<"${rgw}")
	[[ -n "${id}" ]] || die "the radosgw reports no id: ${rgw}"
	entity=client.rgw.${id}
	frontends=$(ceph_cmd config show "${entity}" rgw_frontends) ||
		die "no running daemon reads the config section ${entity}"
	want=$(jq -r '.metadata["frontend_config#0"] // empty' <<<"${rgw}")
	[[ -n "${frontends}" && "${frontends}" == "${want}" ]] ||
		die "${entity} reports the frontends '${frontends}', the radosgw '${want}'"
	echo "${entity}"
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
