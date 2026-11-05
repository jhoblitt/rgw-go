#!/bin/bash
# clusterstate.sh: a short check of tentacle: rw-* buckets left, gc entries, ceph df, health.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
# shellcheck disable=SC2016
tb sh -c '
s="--rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore"
echo "buckets: $(radosgw-admin bucket list $s | tr -d "\n ")"
echo "gc entries: $(radosgw-admin gc list --include-all $s | grep -c "\"tag\"")"
ceph df | grep -E "TOTAL|buckets"
ceph health
rados -p ceph-objectstore.rgw.buckets.data ls | wc -l'
