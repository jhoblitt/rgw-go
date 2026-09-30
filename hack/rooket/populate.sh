#!/usr/bin/env bash
# Writes the fixed data set into the running cluster through the radosgw itself,
# so every object on disk is laid out by the oracle, then records what it wrote
# in hack/rooket/out/<release>/manifest.json. Rerunning it rewrites the same
# data and the same manifest.
#
# Nothing Rook names is hard-coded: the realm, zonegroup and zone are the ones
# the radosgw serves, and the pools and placement come from the zone and
# zonegroup themselves.
#
# Usage: populate.sh squid|tentacle
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=populate.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
use_release "${1:-}"
[[ -f "${out}/ceph.conf" ]] || die "no ${out}/ceph.conf; run make cluster-up RELEASE=${release} first"
ceph_version=$(pinned_version)

rgw=$(rgw_daemon)
realm=$(jq -r '.metadata.realm_name // empty' <<<"${rgw}")
zonegroup=$(jq -r '.metadata.zonegroup_name // empty' <<<"${rgw}")
zone=$(jq -r '.metadata.zone_name // empty' <<<"${rgw}")
[[ -n "${realm}" && -n "${zonegroup}" && -n "${zone}" ]] ||
	die "the radosgw reports no realm, zonegroup or zone: ${rgw}"

# Without the site options radosgw-admin works in a zone named default, which
# it creates on first use and the radosgw never serves.
admin() {
	toolbox radosgw-admin "$@" --rgw-realm="${realm}" --rgw-zonegroup="${zonegroup}" --rgw-zone="${zone}"
}

# operator_admin runs radosgw-admin as Rook's operator does, in the operator's
# pod with the config and keyring it keeps for the cluster (Rook's
# FinalizeCephCommandArgs). Rook writes the realm, zonegroup, zone and periods
# with that radosgw-admin, whose release can differ from the cluster's, so
# changing them with it leaves them in the encoding Rook would.
operator_admin() {
	k -n rook-ceph exec deploy/rook-ceph-operator -- radosgw-admin "$@" \
		--rgw-realm="${realm}" --rgw-zonegroup="${zonegroup}" --rgw-zone="${zone}" \
		--cluster=rook-ceph --conf=/var/lib/rook/rook-ceph/rook-ceph.config \
		--name=client.admin --keyring=/var/lib/rook/rook-ceph/client.admin.keyring
}

endpoint=$(rgw_endpoint "${rgw}")

work=$(mktemp -d "${TMPDIR:-/tmp}/rgw-go-populate.XXXXXX")
trap 'rm -rf "${work}"' EXIT

# Isolated from ~/.aws: path-style addressing, and no default CRC checksums, which
# older radosgw releases reject on chunked uploads.
export AWS_CONFIG_FILE=${work}/aws-config
export AWS_SHARED_CREDENTIALS_FILE=${work}/aws-credentials
unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN AWS_ENDPOINT_URL
cat >"${AWS_CONFIG_FILE}" <<'EOF'
[default]
region = us-east-1
request_checksum_calculation = when_required
response_checksum_validation = when_required
s3 =
    addressing_style = path
EOF
: >"${AWS_SHARED_CREDENTIALS_FILE}"

# ensure_user prints the user's JSON, creating the user on first use.
ensure_user() {
	local uid=$1 tenant=$2 args=(--uid "$1")
	[[ -z "${tenant}" ]] || args+=(--tenant "${tenant}")
	admin user info "${args[@]}" 2>/dev/null ||
		admin user create "${args[@]}" --display-name "${uid}"
}

zonegroup_json=$(admin zonegroup get)
placement=$(jq -r '.default_placement' <<<"${zonegroup_json}")
[[ -n "${placement}" && "${placement}" != null ]] || die "zonegroup ${zonegroup} has no default placement"

# A cloud-s3 tier storage class gives the zonegroup a placement tier to encode.
# No lifecycle rule transitions to it, so radosgw never contacts the endpoint.
has_tier() {
	jq -e --arg p "${placement}" \
		'[.placement_targets[] | select(.name == $p) | .tier_targets // [] | length] | add >= 1' >/dev/null
}
commit=false
if ! has_tier <<<"${zonegroup_json}"; then
	operator_admin zonegroup placement add --placement-id "${placement}" \
		--storage-class CLOUDTIER --tier-type cloud-s3 \
		--tier-config endpoint=http://127.0.0.1:1,access_key=x,secret=y,target_path=rgw-go-cloud >/dev/null
	has_tier < <(admin zonegroup get) || die "zonegroup ${zonegroup} has no tier target after placement add"
	commit=true
fi

# radosgw compresses an object with the compressor the zone's placement names
# for the storage class it is written to, so each codec gets a class of its own
# on the STANDARD class's data pool. radosgw-admin adds a class to the zone's
# placement only once the zonegroup's placement target lists it.
readonly codecs=(zlib snappy zstd lz4)
storage_classes=$(for codec in "${codecs[@]}"; do
	jq -n --arg class "COMP_${codec^^}" --arg codec "${codec}" '{($class): $codec}'
done | jq -cs 'add')
class_names=$(jq -c 'keys' <<<"${storage_classes}")

# has_classes reads a zonegroup and succeeds when its placement target lists
# every storage class in the JSON array $1.
has_classes() {
	jq -e --arg p "${placement}" --argjson want "$1" \
		'($want - [.placement_targets[] | select(.name == $p) | .storage_classes[]]) == []' >/dev/null
}
zone_classes() {
	jq --arg p "${placement}" '.placement_pools[] | select(.key == $p) | .val.storage_classes'
}
zone_json=$(admin zone get)
standard_pool=$(zone_classes <<<"${zone_json}" | jq -r '.STANDARD.data_pool // empty')
[[ -n "${standard_pool}" ]] || die "zone ${zone} has no STANDARD data pool in placement ${placement}"
while IFS=$'\t' read -r class codec; do
	if ! has_classes "[\"${class}\"]" <<<"${zonegroup_json}"; then
		operator_admin zonegroup placement add --placement-id "${placement}" --storage-class "${class}" >/dev/null
		commit=true
	fi
	if ! zone_classes <<<"${zone_json}" | jq -e --arg class "${class}" 'has($class)' >/dev/null; then
		operator_admin zone placement add --placement-id "${placement}" --storage-class "${class}" \
			--data-pool "${standard_pool}" --compression "${codec}" >/dev/null
		commit=true
	fi
done < <(jq -r 'to_entries[] | [.key, .value] | @tsv' <<<"${storage_classes}")

period_zonegroup() {
	admin period get | jq --arg zg "${zonegroup}" '.period_map.zonegroups[] | select(.name == $zg)'
}

# wait_reload waits for the radosgw to reload the committed period: it pauses
# its frontends, replaces its driver, and registers the new one with the
# manager under a new gid just before its frontends resume. A request sent in
# that gap waits on the paused listener rather than failing. The reloads seen
# took 3 to 28 s, but one 19.2.6 radosgw was still paused minutes into its
# reload, and only a restart of its pod ended it. docs/ceph-upstream-bugs.md
# records the probable causes of the delay and of the hang.
wait_reload() {
	# shellcheck disable=SC2016 # $gid is the jq filter's variable
	wait_rgw "the radosgw did not finish reloading the committed period" \
		"restart it with 'ROOKET_NAME=${ROOKET_NAME} ${rooket} kubectl -n rook-ceph delete pod -l app=rook-ceph-rgw' and rerun once the service map lists it alone" \
		'any(.[]; .gid != $gid)' --argjson gid "$(jq '.gid' <<<"${rgw}")" >/dev/null
}

# The zonegroup belongs to a realm, so the changes are committed to a new
# period now rather than by Rook at some later reconcile, and the radosgw
# reloads its zonegroup and zone from that period. One commit carries them
# all: a realm notification that reaches the radosgw while it is already
# reloading is dropped. A run that stopped short of committing left them out
# of the period, so a period that lacks them is committed too.
current=$(period_zonegroup)
if [[ "${commit}" == true ]] || ! has_tier <<<"${current}" || ! has_classes "${class_names}" <<<"${current}"; then
	operator_admin period update --commit >/dev/null
	wait_reload
	current=$(period_zonegroup)
fi
has_tier <<<"${current}" || die "the current period's zonegroup ${zonegroup} has no tier target"
has_classes "${class_names}" <<<"${current}" ||
	die "the current period's zonegroup ${zonegroup} does not list the storage classes ${class_names}"
zone_classes < <(admin zone get) | jq -e --argjson want "${storage_classes}" --arg pool "${standard_pool}" \
	'. as $have | all($want | to_entries[]; $have[.key] == {data_pool: $pool, compression_type: .value})' >/dev/null ||
	die "zone ${zone} does not compress the storage classes ${storage_classes} on ${standard_pool}"

alice=$(ensure_user alice "")
bob=$(ensure_user bob t1)

# as_user runs the aws CLI with the keys from a user's JSON.
as_user() {
	local user=$1
	shift
	AWS_ACCESS_KEY_ID=$(jq -r '.keys[0].access_key' <<<"${user}") \
		AWS_SECRET_ACCESS_KEY=$(jq -r '.keys[0].secret_key' <<<"${user}") \
		AWS_RETRY_MODE=standard AWS_MAX_ATTEMPTS=10 \
		aws --endpoint-url "${endpoint}" "$@"
}

create_bucket() {
	local user=$1 bucket=$2
	as_user "${user}" s3api head-bucket --bucket "${bucket}" 2>/dev/null ||
		as_user "${user}" s3api create-bucket --bucket "${bucket}" >/dev/null
}

create_bucket "${alice}" plain
create_bucket "${bob}" tenanted

# payload writes size deterministic pseudo-random bytes seeded by the key, so a
# rerun uploads identical content.
payload() {
	python3 -c 'import random, sys
sys.stdout.buffer.write(random.Random(sys.argv[1]).randbytes(int(sys.argv[2])))' "$1" "$2" >"${work}/$1"
}

# payload_compressible writes size bytes of the key repeated, which every codec
# compresses, so a rerun uploads identical content.
payload_compressible() {
	python3 -c 'import sys
key, size = sys.argv[1].encode(), int(sys.argv[2])
sys.stdout.buffer.write((key * (size // len(key) + 1))[:size])' "$1" "$2" >"${work}/$1"
}

# upload puts the file a payload function wrote for key into plain.
upload() {
	local key=$1
	shift
	as_user "${alice}" s3api put-object --bucket plain --key "${key}" --body "${work}/${key}" "$@" >/dev/null
}

put() {
	local key=$1 size=$2
	shift 2
	payload "${key}" "${size}"
	upload "${key}" "$@"
}

put small.bin 1024
put head-full.bin 4194304
put large.bin 10485760
put _underscore.bin 16
put empty.bin 0
put meta.bin 64 --content-type text/plain --metadata color=blue

# The CLI's multipart threshold is 8 MiB, so a 20 MiB copy goes up in parts.
payload multipart.bin 20971520
as_user "${alice}" s3 cp --only-show-errors "${work}/multipart.bin" s3://plain/multipart.bin
etag=$(as_user "${alice}" s3api head-object --bucket plain --key multipart.bin | jq -r '.ETag')
[[ "${etag}" == *-* ]] || die "multipart.bin was not uploaded in parts (ETag ${etag})"

# Each codec's object goes to its storage class. radosgw stores an object as
# written when it cannot load the class's compressor, which would leave the
# read oracle nothing to decompress, so object stat must show the codec in the
# compression info radosgw keeps in the head's user.rgw.compression attr.
readonly comp_size=1048576
while IFS=$'\t' read -r class codec; do
	key=comp-${codec}.bin
	payload_compressible "${key}" "${comp_size}"
	upload "${key}" --storage-class "${class}"
	stat_json=$(admin object stat --bucket plain --object "${key}")
	jq -e --arg codec "${codec}" '.compression.compression_type == $codec' <<<"${stat_json}" >/dev/null ||
		die "radosgw did not store ${key} ${codec}-compressed: its compression is $(jq -c '.compression' <<<"${stat_json}")"
done < <(jq -r 'to_entries[] | [.key, .value] | @tsv' <<<"${storage_classes}")

objects=$(jq -n --argjson classes "${storage_classes}" --argjson comp_size "${comp_size}" '[
	{bucket: "plain", key: "small.bin", size: 1024},
	{bucket: "plain", key: "head-full.bin", size: 4194304},
	{bucket: "plain", key: "large.bin", size: 10485760},
	{bucket: "plain", key: "_underscore.bin", size: 16},
	{bucket: "plain", key: "multipart.bin", size: 20971520, multipart: true},
	{bucket: "plain", key: "empty.bin", size: 0},
	{bucket: "plain", key: "meta.bin", size: 64, content_type: "text/plain",
		metadata: {"x-amz-meta-color": "blue"}}
] + [$classes | to_entries[] |
	{bucket: "plain", key: "comp-\(.value).bin", size: $comp_size, storage_class: .key, compression: .value}]')

# Read every object back through the radosgw before recording it.
while IFS=$'\t' read -r key size; do
	got=$(as_user "${alice}" s3api head-object --bucket plain --key "${key}" | jq -r '.ContentLength')
	[[ "${got}" == "${size}" ]] || die "${key}: radosgw reports ${got} bytes, want ${size}"
done < <(jq -r '.[] | [.key, .size] | @tsv' <<<"${objects}")
meta=$(as_user "${alice}" s3api head-object --bucket plain --key meta.bin)
jq -e '.ContentType == "text/plain" and .Metadata.color == "blue"' <<<"${meta}" >/dev/null ||
	die "meta.bin lost its content type or metadata: ${meta}"

# radosgw syncs a user's <user>.buckets stats only every
# rgw_user_quota_bucket_sync_interval; sync them now so the header is
# deterministic when populating finishes.
admin user stats --uid alice --sync-stats >/dev/null
admin user stats --uid bob --tenant t1 --sync-stats >/dev/null

# The metadata pool holds the zone's namespaced pools; the index, data and
# extra pools are the default placement's STANDARD class, which the buckets
# use. The root pool is the one radosgw-admin reads the zone from.
root=$(ceph_cmd config get client.admin rgw_zone_root_pool)
pools=$(admin zone get | jq --arg root "${root}" --arg p "${placement}" '
	(.placement_pools[] | select(.key == $p) | .val) as $pp | {
	root: $root, meta: (.domain_root | split(":")[0]), control: .control_pool,
	log: .log_pool, index: $pp.index_pool, data: $pp.storage_classes.STANDARD.data_pool,
	nonec: $pp.data_extra_pool
}')
existing=$(ceph_cmd osd pool ls -f json)
jq -e --argjson have "${existing}" '[.[]] - $have | length == 0' <<<"${pools}" >/dev/null ||
	die "missing radosgw pools ${pools}; have ${existing}"

bucket_entry() {
	local name=$1 bucket=$2 owner=$3 stats
	stats=$(admin bucket stats --bucket "${bucket}")
	jq -e --arg owner "${owner}" --arg p "${placement}" '.owner == $owner and .placement_rule == $p' \
		<<<"${stats}" >/dev/null ||
		die "${bucket} is owned by $(jq -r .owner <<<"${stats}") with placement $(jq -r .placement_rule <<<"${stats}"), want ${owner} and ${placement}"
	jq --arg name "${name}" '{name: $name, owner, id, marker, num_shards}' <<<"${stats}"
}

plain=$(bucket_entry plain plain alice)
tenanted=$(bucket_entry tenanted t1/tenanted "t1\$bob")
buckets=$(jq -s '.' <<<"${plain}${tenanted}")

user_entry() {
	jq --arg tenant "$2" '{uid: .user_id | sub("^.*\\$"; ""), tenant: $tenant,
		access_key: .keys[0].access_key, secret_key: .keys[0].secret_key}' <<<"$1"
}
users=$(jq -s '.' <<<"$(user_entry "${alice}" "")$(user_entry "${bob}" t1)")

jq -n \
	--arg release "${release}" \
	--arg ceph_version "${ceph_version}" \
	--arg rooket_name "${ROOKET_NAME}" \
	--arg realm "${realm}" \
	--arg zonegroup "${zonegroup}" \
	--arg zone "${zone}" \
	--argjson pools "${pools}" \
	--argjson storage_classes "${storage_classes}" \
	--argjson users "${users}" \
	--argjson buckets "${buckets}" \
	--argjson objects "${objects}" \
	'{release: $release, ceph_version: $ceph_version, rooket_name: $rooket_name,
		realm: $realm, zonegroup: $zonegroup, zone: $zone,
		pools: $pools, storage_classes: $storage_classes, users: $users, buckets: $buckets,
		objects: $objects}' \
	>"${work}/manifest.json"
mv "${work}/manifest.json" "${out}/manifest.json"
echo "wrote ${out}/manifest.json"
