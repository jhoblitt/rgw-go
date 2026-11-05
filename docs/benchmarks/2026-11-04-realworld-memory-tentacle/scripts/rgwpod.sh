#!/bin/bash
# Runs a shell snippet inside the release's radosgw container: rgwpod.sh <release> '<snippet>'.
# The snippet sees $RGWPID, the radosgw's pid in the pod's shared namespace.
set -euo pipefail
export ROOKET_NAME=rgw-go-$1
pod=$(timeout 60 rooket k -n rook-ceph get pod -l app=rook-ceph-rgw -o jsonpath='{.items[0].metadata.name}')
find='for c in /proc/[0-9]*/comm; do [ "$(cat "$c" 2>/dev/null)" = radosgw ] && basename "$(dirname "$c")"; done; true'
timeout 60 rooket k -n rook-ceph exec "$pod" -c rgw -- sh -c "RGWPID=\$($find); $2"
