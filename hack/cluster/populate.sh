#!/usr/bin/env bash
# Writes the fixed data set into the running cluster through the radosgw itself,
# so every object on disk is laid out by the oracle, then records what it wrote
# in hack/cluster/out/<release>/manifest.json. Rerunning it rewrites the same
# data and the same manifest.
#
# Usage: populate.sh squid|tentacle
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly endpoint=http://127.0.0.1:7480

die() {
	echo "populate.sh: $*" >&2
	exit 1
}

release=${1:-}
[[ "${release}" == squid || "${release}" == tentacle ]] || die "usage: populate.sh squid|tentacle"
running=$(podman inspect rgw-go-rgw --format '{{index .Config.Labels "rgw-go.release"}}' 2>/dev/null) ||
	die "no cluster is running; run make cluster-up-${release} first"
[[ "${running}" == "${release}" ]] || die "the running cluster is ${running}, not ${release}"
out=${here}/out/${release}

admin() {
	podman exec rgw-go-rgw radosgw-admin "$@"
}

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

alice=$(ensure_user alice "")
bob=$(ensure_user bob t1)

# as_user runs the aws CLI with the keys from a user's JSON.
as_user() {
	local user=$1
	shift
	AWS_ACCESS_KEY_ID=$(jq -r '.keys[0].access_key' <<<"${user}") \
		AWS_SECRET_ACCESS_KEY=$(jq -r '.keys[0].secret_key' <<<"${user}") \
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

put() {
	local key=$1 size=$2
	shift 2
	payload "${key}" "${size}"
	as_user "${alice}" s3api put-object --bucket plain --key "${key}" --body "${work}/${key}" "$@" >/dev/null
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

objects=$(jq -n '[
	{bucket: "plain", key: "small.bin", size: 1024},
	{bucket: "plain", key: "head-full.bin", size: 4194304},
	{bucket: "plain", key: "large.bin", size: 10485760},
	{bucket: "plain", key: "_underscore.bin", size: 16},
	{bucket: "plain", key: "multipart.bin", size: 20971520, multipart: true},
	{bucket: "plain", key: "empty.bin", size: 0},
	{bucket: "plain", key: "meta.bin", size: 64, content_type: "text/plain",
		metadata: {"x-amz-meta-color": "blue"}}
]')

# Read every object back through the radosgw before recording it.
while IFS=$'\t' read -r key size; do
	got=$(as_user "${alice}" s3api head-object --bucket plain --key "${key}" | jq -r '.ContentLength')
	[[ "${got}" == "${size}" ]] || die "${key}: radosgw reports ${got} bytes, want ${size}"
done < <(jq -r '.[] | [.key, .size] | @tsv' <<<"${objects}")
meta=$(as_user "${alice}" s3api head-object --bucket plain --key meta.bin)
jq -e '.ContentType == "text/plain" and .Metadata.color == "blue"' <<<"${meta}" >/dev/null ||
	die "meta.bin lost its content type or metadata: ${meta}"

pools=$(jq -n '{
	root: ".rgw.root", meta: "default.rgw.meta", control: "default.rgw.control",
	log: "default.rgw.log", index: "default.rgw.buckets.index",
	data: "default.rgw.buckets.data", nonec: "default.rgw.buckets.non-ec"
}')
existing=$(podman exec rgw-go-mon ceph osd pool ls -f json)
jq -e --argjson have "${existing}" '[.[]] - $have | length == 0' <<<"${pools}" >/dev/null ||
	die "missing radosgw pools; have ${existing}"

bucket_entry() {
	local name=$1 bucket=$2 owner=$3 stats
	stats=$(admin bucket stats --bucket "${bucket}")
	jq -e --arg owner "${owner}" '.owner == $owner' <<<"${stats}" >/dev/null ||
		die "${bucket} is owned by $(jq -r .owner <<<"${stats}"), want ${owner}"
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
	--argjson pools "${pools}" \
	--argjson users "${users}" \
	--argjson buckets "${buckets}" \
	--argjson objects "${objects}" \
	'{release: $release, zone: "default", pools: $pools, users: $users, buckets: $buckets, objects: $objects}' \
	>"${work}/manifest.json"
mv "${work}/manifest.json" "${out}/manifest.json"
echo "wrote ${out}/manifest.json"
