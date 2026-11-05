#!/bin/bash
# smoke-main.sh: the smoke test of the Ceph main radosgw in rgw-go-main, before any measurement: PUT, GET,
# listing, a multipart upload (an aborted one too) and delete through the bench user, then a grep of the
# radosgw's log for errors and -EOPNOTSUPP / -95 from cls calls. Prints PASS or FAIL lines.
set -uo pipefail
export RW_CLUSTER=main
. /tmp/claude-1000/spike-memory/realworld/env.sh
ep=$(radosgw_endpoint)
export AWS_ACCESS_KEY_ID=BENCH0000000000000AK AWS_SECRET_ACCESS_KEY=bench-secret-0123456789abcdefghijklmnopqrstuv
export AWS_DEFAULT_REGION=us-east-1 AWS_REQUEST_CHECKSUM_CALCULATION=when_required AWS_RESPONSE_CHECKSUM_VALIDATION=when_required
export AWS_EC2_METADATA_DISABLED=true
s3() { aws --endpoint-url "$ep" "$@"; }
t=$(mktemp -d)
b=smoke-$$
fail=0
check() { if [ "$2" = ok ]; then echo "PASS $1"; else echo "FAIL $1: $2"; fail=1; fi; }
head -c 100000 /dev/urandom >"$t/small"
head -c 25000000 /dev/urandom >"$t/big"
small=$(md5sum <"$t/small" | cut -c1-32)
big=$(md5sum <"$t/big" | cut -c1-32)
s3 s3 mb "s3://$b" >/dev/null 2>"$t/err" && check "create bucket" ok || check "create bucket" "$(cat "$t/err")"
s3 s3api put-object --bucket "$b" --key small --body "$t/small" >/dev/null 2>"$t/err" && check "PUT 100 kB" ok || check "PUT 100 kB" "$(cat "$t/err")"
s3 s3 cp "$t/big" "s3://$b/big" >/dev/null 2>"$t/err" && check "multipart upload 25 MB (aws s3 cp: 8 MB parts)" ok || check "multipart upload" "$(cat "$t/err")"
etag=$(s3 s3api head-object --bucket "$b" --key big --query ETag --output text 2>/dev/null)
case $etag in *-3\"*) check "multipart ETag $etag" ok ;; *) check "multipart ETag" "got $etag" ;; esac
s3 s3 cp "s3://$b/small" "$t/small.get" >/dev/null 2>"$t/err" && [ "$(md5sum <"$t/small.get" | cut -c1-32)" = "$small" ] && check "GET 100 kB, bytes equal" ok || check "GET 100 kB" "$(cat "$t/err")"
s3 s3 cp "s3://$b/big" "$t/big.get" >/dev/null 2>"$t/err" && [ "$(md5sum <"$t/big.get" | cut -c1-32)" = "$big" ] && check "GET 25 MB (multipart), bytes equal" ok || check "GET 25 MB" "$(cat "$t/err")"
s3 s3api get-object --bucket "$b" --key big --range bytes=1000000-1999999 "$t/range" >/dev/null 2>"$t/err" && [ "$(wc -c <"$t/range")" = 1000000 ] && check "ranged GET" ok || check "ranged GET" "$(cat "$t/err")"
n=$(s3 s3api list-objects-v2 --bucket "$b" --query 'length(Contents)' --output text 2>"$t/err")
[ "$n" = 2 ] && check "list objects (2)" ok || check "list objects" "got $n $(cat "$t/err")"
s3 s3api list-buckets --query 'Buckets[].Name' --output text 2>/dev/null | grep -q "$b" && check "list buckets" ok || check "list buckets" "missing"
up=$(s3 s3api create-multipart-upload --bucket "$b" --key aborted --query UploadId --output text 2>"$t/err")
[ -n "$up" ] && check "start a multipart upload" ok || check "start a multipart upload" "$(cat "$t/err")"
s3 s3api upload-part --bucket "$b" --key aborted --upload-id "$up" --part-number 1 --body "$t/big" >/dev/null 2>"$t/err" && check "upload a 25 MB part" ok || check "upload a part" "$(cat "$t/err")"
s3 s3api list-multipart-uploads --bucket "$b" --query 'length(Uploads)' --output text 2>/dev/null | grep -q '^1$' && check "list multipart uploads" ok || check "list multipart uploads" "not 1"
s3 s3api list-parts --bucket "$b" --key aborted --upload-id "$up" --query 'length(Parts)' --output text 2>/dev/null | grep -q '^1$' && check "list parts" ok || check "list parts" "not 1"
s3 s3api abort-multipart-upload --bucket "$b" --key aborted --upload-id "$up" >/dev/null 2>"$t/err" && check "abort the multipart upload" ok || check "abort" "$(cat "$t/err")"
s3 s3 rm "s3://$b" --recursive >/dev/null 2>"$t/err" && check "delete objects" ok || check "delete objects" "$(cat "$t/err")"
s3 s3 rb "s3://$b" >/dev/null 2>"$t/err" && check "delete bucket" ok || check "delete bucket" "$(cat "$t/err")"
tb radosgw-admin gc process --include-all "${site[@]}" >/dev/null 2>&1
echo "== radosgw log: error lines and cls -EOPNOTSUPP / -95"
pod=$(rgwpod)
timeout 120 rooket k -n rook-ceph logs "$pod" -c rgw >"$t/rgw.log" 2>&1
timeout 120 rooket k -n rook-ceph exec "$pod" -c rgw -- sh -c 'cat /var/log/ceph/ceph-client.rgw.*.log 2>/dev/null' >>"$t/rgw.log" 2>&1
cp "$t/rgw.log" /tmp/claude-1000/spike-memory/realworld/main/smoke-rgw.log
echo "log lines: $(wc -l <"$t/rgw.log")"
if grep -n -E 'EOPNOTSUPP|\(95\)|-95\b|Operation not supported|ERROR|ceph_assert|FAILED|failed to|\bcls\b.*(fail|error)' "$t/rgw.log" | grep -v -E 'client_not_found|NoSuchKey' | head -40; then
	echo "(the lines above matched; read them)"
else
	echo "no matching line"
fi
rm -rf "$t"
[ "$fail" = 0 ] && echo "SMOKE PASS" || echo "SMOKE FAIL"
